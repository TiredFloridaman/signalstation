package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CaptchaURL is where the user solves the registration challenge. After
// solving, the "Open Signal" link target is the signalcaptcha:// token that
// signal-cli wants.
const CaptchaURL = "https://signalcaptchas.org/registration/generate.html"

// ErrCaptchaRequired is returned when the Signal server refuses a registration
// without a fresh captcha token.
var ErrCaptchaRequired = errors.New("captcha required")

// ErrRateLimited is returned on HTTP 429 from the registration endpoint. Only
// waiting fixes this; retrying immediately makes it worse.
var ErrRateLimited = errors.New("rate limited by the Signal server")

type CLI struct {
	Bin      string // path to signal-cli, signal-cli.bat, or a native build
	DataDir  string // passed as --config
	JavaHome string // optional; only needed for JVM builds of signal-cli
}

func NewCLI(cfg Config) *CLI {
	return &CLI{
		Bin:      resolveBinary(cfg.SignalCLIPath),
		DataDir:  signalCLIDataDir(),
		JavaHome: cfg.JavaHome,
	}
}

func (c *CLI) Available() bool { return fileExists(c.Bin) }

// resolveBinaryFor returns the file that will actually be executed for a
// configured path.
//
// This exists because of a Windows trap. signal-cli ships bin/signal-cli (a
// shell script) next to bin/signal-cli.bat, and users naturally point at the
// extensionless one. Windows then resolves it through PATHEXT to the .bat, and
// CreateProcess runs batch files by spawning cmd.exe internally — so a shell
// parses the arguments even though nothing in this program asked for one. That
// shell reads the & in a Signal linking URI as a command separator and truncates
// it, which is invisible from the configured path alone.
//
// Resolving the extension ourselves means isBatch sees the truth, and the
// java-direct launch path is chosen instead.
//
// goos is a parameter so the Windows behaviour is testable from any host.
func resolveBinaryFor(path, goos string) string {
	if path == "" || goos != "windows" {
		return path
	}
	// An explicit, existing extension is already unambiguous.
	if filepath.Ext(path) != "" && fileExists(path) {
		return path
	}
	// Prefer the native build; it needs no Java and no shell.
	for _, ext := range []string{".exe", ".com", ".bat", ".cmd"} {
		if p := path + ext; fileExists(p) {
			return p
		}
	}
	return path
}

func resolveBinary(path string) string { return resolveBinaryFor(path, runtime.GOOS) }

// findJava locates a JVM, preferring an explicitly configured one.
func findJava(javaHome string) string {
	name := "java"
	if runtime.GOOS == "windows" {
		name = "java.exe"
	}
	for _, home := range []string{javaHome, os.Getenv("JAVA_HOME")} {
		if home == "" {
			continue
		}
		if p := filepath.Join(expandHome(home), "bin", name); fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath("java"); err == nil {
		return p
	}
	return ""
}

// javaLaunch works out whether signal-cli can be started as a plain Java
// process instead of through its .bat launcher.
//
// This is strongly preferred on Windows. The .bat has to be run via cmd.exe,
// and cmd applies its own parsing to the command line: an unquoted & in a Signal
// linking URI is read as a command separator, so the shell tries to execute
// everything after it and reports "not recognized as an internal or external
// command". Launching java directly means CreateProcess receives the arguments
// as-is and no shell ever sees them.
//
// The layout is bin/signal-cli.bat alongside lib/*.jar, so the classpath is the
// sibling lib directory. Java expands a trailing * in a classpath entry itself.
func (c *CLI) javaLaunch() (javaExe, classpath string, ok bool) {
	if !isBatch(c.Bin) {
		return "", "", false
	}
	libDir := filepath.Join(filepath.Dir(filepath.Dir(c.Bin)), "lib")
	if st, err := os.Stat(libDir); err != nil || !st.IsDir() {
		return "", "", false
	}
	java := findJava(c.JavaHome)
	if java == "" {
		return "", "", false
	}
	return java, filepath.Join(libDir, "*"), true
}

// command builds the exec.Cmd, preferring in order: a native signal-cli
// executable, a direct Java invocation, and only as a last resort cmd.exe.
func (c *CLI) command(ctx context.Context, args ...string) *exec.Cmd {
	full := append([]string{"--config", c.DataDir}, args...)

	var cmd *exec.Cmd
	switch {
	case !isBatch(c.Bin):
		// Native binary or shell script: run it directly.
		cmd = exec.CommandContext(ctx, c.Bin, full...)

	default:
		if java, cp, ok := c.javaLaunch(); ok {
			jargs := append([]string{"-classpath", cp, "org.asamk.signal.Main"}, full...)
			cmd = exec.CommandContext(ctx, java, jargs...)
		} else {
			// No JVM found, so fall back to the launcher with a hand-built
			// command line quoted for cmd's parser rather than Go's.
			cmd = exec.CommandContext(ctx, "cmd.exe")
			applyRawCmdLine(cmd, batchCommandLine(c.Bin, full))
		}
	}

	cmd.Env = os.Environ()
	if c.JavaHome != "" {
		cmd.Env = append(cmd.Env, "JAVA_HOME="+c.JavaHome)
	}
	hideConsole(cmd)
	return cmd
}

// run executes signal-cli and returns stdout+stderr together. signal-cli writes
// most of what we care about (including error detail) to stderr, so the two are
// merged deliberately.
func (c *CLI) run(ctx context.Context, args ...string) (string, error) {
	if !c.Available() {
		return "", errors.New("signal-cli was not found. Set its location in Settings")
	}
	// A shell must never see these arguments. If the only available launch
	// route is one that reinterprets metacharacters, stop rather than send a
	// silently truncated URI to the Signal service.
	if err := c.guardShellArgs(args); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	cmd := c.command(ctx, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	// Record what was actually executed rather than a reconstruction, so the
	// log shows the real launch method (native binary, java, or cmd.exe).
	shown := strings.Join(cmd.Args, " ")
	if cmd.SysProcAttr != nil {
		if raw := rawCmdLine(cmd); raw != "" {
			shown = raw
		}
	}

	err := cmd.Run()
	out := buf.String()
	appendLog(shown, out, err)

	if err != nil {
		if ctx.Err() != nil {
			return out, fmt.Errorf("signal-cli timed out after %s", ctx.Err())
		}
		return out, classify(out, err)
	}
	return out, nil
}

// guardShellArgs blocks the one combination that silently corrupts data: an
// argument containing cmd metacharacters, on a launch route that goes through
// cmd.exe. A Signal linking URI always contains &, so this would otherwise
// truncate it at the first parameter and report an unhelpful "Invalid device
// link" from the far end.
func (c *CLI) guardShellArgs(args []string) error {
	return c.guardShellArgsFor(args, runtime.GOOS)
}

func (c *CLI) guardShellArgsFor(args []string, goos string) error {
	if goos != "windows" || !isBatch(c.Bin) {
		return nil
	}
	if _, _, ok := c.javaLaunch(); ok {
		return nil // java directly; no shell involved
	}
	// Falling back to cmd.exe. Our own quoting handles this, but only because
	// we build the command line by hand; verify that assumption holds.
	for _, a := range args {
		if strings.ContainsAny(a, "&|<>^") {
			return errors.New(
				"signal-cli can only be started through cmd.exe here, which would corrupt " +
					"this command. Set JAVA_HOME in Settings, or use the signal-cli.exe " +
					"native build, which needs no Java")
		}
	}
	return nil
}

// LaunchMethod describes how signal-cli will be started, for display in
// Settings so the user can confirm the app is not going through a shell.
func (c *CLI) LaunchMethod() string {
	switch {
	case !c.Available():
		return "not found"
	case !isBatch(c.Bin):
		return "native executable, no shell involved: " + c.Bin
	default:
		if java, _, ok := c.javaLaunch(); ok {
			return "java directly (no shell): " + java
		}
		return "cmd.exe with the .bat launcher — no JVM found. " +
			"Set JAVA_HOME, or use signal-cli.exe instead"
	}
}

// classify turns signal-cli's stderr into something a person can act on.
func classify(out string, err error) error {
	l := strings.ToLower(out)
	switch {
	case strings.Contains(l, "captcha required"), strings.Contains(l, "invalid captcha"),
		strings.Contains(l, "use --captcha"):
		return ErrCaptchaRequired
	case strings.Contains(l, "statuscode: 429"), strings.Contains(l, "registrationretryexception"):
		return ErrRateLimited
	case strings.Contains(l, "statuscode: 411"), strings.Contains(l, "devicelimitexceeded"):
		return errors.New("this number already has the maximum of 5 linked devices. Remove one before linking another")
	case strings.Contains(l, "invalid verification code"), strings.Contains(l, "incorrect verification code"):
		return errors.New("that verification code was not accepted. Check the digits and try again")
	case strings.Contains(l, "registration lock"), strings.Contains(l, "pin required"):
		return errors.New("this number has a registration lock PIN. Enter the PIN and try again")
	case strings.Contains(l, "user is not registered"):
		return errors.New("this number is not registered with signal-cli yet")
	case strings.Contains(l, "authentication failed"):
		return errors.New("authentication failed. signal-cli must be the primary device for this number to link others")
	}
	if msg := lastMeaningfulLine(out); msg != "" {
		return errors.New(msg)
	}
	return err
}

// lastMeaningfulLine pulls the most specific line out of signal-cli's output,
// skipping the JVM and connection-pool noise that precedes real errors.
func lastMeaningfulLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		l := strings.ToLower(line)
		if strings.Contains(l, "hikari") || strings.Contains(l, "libsignal") ||
			strings.Contains(l, "xdg_") || strings.Contains(l, "\tat ") ||
			strings.HasPrefix(l, "at org.") {
			continue
		}
		if len(line) > 300 {
			line = line[:300] + "…"
		}
		return line
	}
	return ""
}

func (c *CLI) Version(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "--version")
	return strings.TrimSpace(out), err
}

// Register starts registration for number. captcha may be empty on the first
// attempt; if the server demands one, ErrCaptchaRequired comes back and the UI
// prompts for a token.
func (c *CLI) Register(ctx context.Context, number, captcha string, voice bool) error {
	args := []string{"-a", number, "register"}
	if voice {
		args = append(args, "--voice")
	}
	if captcha != "" {
		args = append(args, "--captcha", normalizeCaptcha(captcha))
	}
	_, err := c.run(ctx, args...)
	return err
}

// normalizeCaptcha accepts either the full signalcaptcha:// link the user
// copies from the browser or the bare token, and gives signal-cli what it
// expects.
func normalizeCaptcha(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'")
	return strings.TrimPrefix(s, "signalcaptcha://")
}

// Verify completes registration with the SMS or voice code. pin is only needed
// if the number has a registration lock.
func (c *CLI) Verify(ctx context.Context, number, code, pin string) error {
	code = strings.ReplaceAll(strings.TrimSpace(code), "-", "")
	args := []string{"-a", number, "verify", code}
	if strings.TrimSpace(pin) != "" {
		args = append(args, "--pin", strings.TrimSpace(pin))
	}
	_, err := c.run(ctx, args...)
	return err
}

// AddDevice links a new device (here, a Signal Desktop profile) to number.
// Only works when signal-cli holds the primary registration for that number.
func (c *CLI) AddDevice(ctx context.Context, number, uri string) error {
	_, err := c.run(ctx, "-a", number, "addDevice", "--uri", strings.TrimSpace(uri))
	return err
}

func (c *CLI) ListDevices(ctx context.Context, number string) (string, error) {
	return c.run(ctx, "-a", number, "listDevices")
}

func (c *CLI) RemoveDevice(ctx context.Context, number string, deviceID int) error {
	_, err := c.run(ctx, "-a", number, "removeDevice", "-d", fmt.Sprint(deviceID))
	return err
}

// SendContacts pushes the primary's contact list to linked devices. Freshly
// linked Signal Desktop profiles otherwise start with an empty contact list.
func (c *CLI) SendContacts(ctx context.Context, number string) error {
	_, err := c.run(ctx, "-a", number, "sendContacts")
	return err
}

// Receive drains pending messages. Signal expects the primary device to come
// online regularly; if it never does, delivery to linked devices degrades.
func (c *CLI) Receive(ctx context.Context, number string, timeout time.Duration) error {
	_, err := c.run(ctx, "-a", number, "receive", "-t", fmt.Sprintf("%.0f", timeout.Seconds()))
	return err
}

func (c *CLI) ListAccounts(ctx context.Context) (string, error) {
	return c.run(ctx, "listAccounts")
}

// DetectSignalCLI looks in the usual install locations, preferring a copy the
// user has dropped next to Signal Station itself.
func DetectSignalCLI() string {
	var candidates []string

	// Native .exe first. Recent signal-cli releases ship a GraalVM-built
	// signal-cli.exe alongside the .bat launcher; using it avoids cmd.exe
	// entirely, and needs no Java.
	roots := []string{}
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Join(filepath.Dir(exe), "signal-cli"))
	}
	roots = append(roots, filepath.Join(appDataDir(), "tools", "signal-cli"))

	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		roots = append(roots,
			filepath.Join(local, "signal-cli"),
			`C:\signal-cli`,
			`C:\Program Files\signal-cli`,
		)
	}
	for _, r := range roots {
		for _, name := range []string{"signal-cli.exe", "signal-cli", "signal-cli.bat"} {
			candidates = append(candidates, filepath.Join(r, "bin", name))
		}
	}

	switch runtime.GOOS {
	case "windows":
		// Nothing further; the roots above cover it.
	case "darwin":
		candidates = append(candidates,
			"/opt/homebrew/bin/signal-cli",
			"/usr/local/bin/signal-cli",
			expandHome("~/signal-cli/bin/signal-cli"),
		)
	default:
		candidates = append(candidates,
			"/usr/local/bin/signal-cli",
			"/usr/bin/signal-cli",
			"/opt/signal-cli/bin/signal-cli",
		)
	}

	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	if p, err := exec.LookPath("signal-cli"); err == nil {
		return p
	}
	return ""
}

// appendLog writes a trimmed record of every signal-cli invocation. Verification
// codes and captcha tokens are redacted so the log is safe to attach to a bug
// report.
func appendLog(cmdline, output string, runErr error) {
	f, err := os.OpenFile(filepath.Join(logDirPath(), "signal-station.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	if len(output) > 4000 {
		output = output[:4000] + "\n…truncated…"
	}
	status := "ok"
	if runErr != nil {
		status = runErr.Error()
	}
	fmt.Fprintf(f, "\n=== %s ===\n$ %s\n[%s]\n%s\n",
		time.Now().Format(time.RFC3339), redact(cmdline), status, redact(output))
}

func redact(s string) string {
	for _, flag := range []string{"--captcha", "verify", "--pin"} {
		idx := strings.Index(s, flag)
		if idx < 0 {
			continue
		}
		rest := s[idx+len(flag):]
		fields := strings.Fields(rest)
		if len(fields) > 0 {
			s = strings.Replace(s, fields[0], "«redacted»", 1)
		}
	}
	return s
}
