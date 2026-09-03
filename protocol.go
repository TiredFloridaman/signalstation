package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CaptchaScheme is the custom URI scheme Signal's captcha page hands back. The
// "Open Signal" link on that page targets signalcaptcha://<token>, so if Signal
// Station is the registered handler, clicking it launches the app with the token
// instead of the user having to copy the link by hand.
const CaptchaScheme = "signalcaptcha"

// captchaURIFromArgs returns the signalcaptcha:// URI if this process was
// launched to handle one. The OS appends the whole URI as an argument, so it
// arrives in os.Args on every platform.
func captchaURIFromArgs(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, CaptchaScheme+"://") {
			return a
		}
	}
	return ""
}

// registerCaptchaHandler makes this executable the OS handler for
// signalcaptcha:// links. It is idempotent and safe to call on every launch: it
// only rewrites the registration when the recorded path differs from where the
// app is now, so moving or updating the app self-heals.
func registerCaptchaHandler() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)

	switch runtime.GOOS {
	case "windows":
		return registerWindows(exe)
	case "darwin":
		return registerDarwin(exe)
	default:
		return registerLinux(exe)
	}
}

// --- Windows -----------------------------------------------------------------

// registerWindows writes the scheme under HKCU\Software\Classes, which needs no
// administrator rights and applies to the current user. The command uses "%1"
// so the OS substitutes the full URI as the first argument.
func registerWindows(exe string) error {
	base := `HKCU\Software\Classes\` + CaptchaScheme

	// Skip if already pointing at this exe, to avoid touching the registry on
	// every launch.
	if cur, err := regQuery(base+`\shell\open\command`, ""); err == nil {
		if strings.Contains(strings.ToLower(cur), strings.ToLower(exe)) {
			return nil
		}
	}

	cmds := [][]string{
		{base, "/ve", "/d", "URL:Signal Captcha", "/f"},
		{base, "/v", "URL Protocol", "/d", "", "/f"},
		{base + `\DefaultIcon`, "/ve", "/d", exe + ",0", "/f"},
		{base + `\shell\open\command`, "/ve", "/d", fmt.Sprintf(`"%s" "%%1"`, exe), "/f"},
	}
	for _, args := range cmds {
		cmd := exec.Command("reg", append([]string{"add"}, args...)...)
		hideConsole(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("reg add failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func regQuery(key, value string) (string, error) {
	args := []string{"query", key}
	if value == "" {
		args = append(args, "/ve")
	} else {
		args = append(args, "/v", value)
	}
	cmd := exec.Command("reg", args...)
	hideConsole(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	// Output looks like: "    (Default)    REG_SZ    <data>"; take what follows
	// the type token.
	for _, line := range strings.Split(string(out), "\n") {
		for _, typ := range []string{"REG_SZ", "REG_EXPAND_SZ"} {
			if i := strings.Index(line, typ); i >= 0 {
				return strings.TrimSpace(line[i+len(typ):]), nil
			}
		}
	}
	return "", fmt.Errorf("value not found")
}

// --- macOS -------------------------------------------------------------------

// registerDarwin relies on the CFBundleURLTypes entry baked into the app
// bundle's Info.plist (added by the packager), and nudges Launch Services to
// notice this copy of the app. When running as a bare binary rather than a
// bundle, there is nothing to register, so it is a no-op.
func registerDarwin(exe string) error {
	app := bundlePath(exe)
	if app == "" {
		return nil // not running from a .app; scheme is declared in the bundle only
	}
	// Ask Launch Services to register the bundle so its declared schemes take
	// effect without waiting for a reboot or a Finder rescan.
	lsregister := "/System/Library/Frameworks/CoreServices.framework/Frameworks/" +
		"LaunchServices.framework/Support/lsregister"
	if fileExists(lsregister) {
		cmd := exec.Command(lsregister, "-f", app)
		_ = cmd.Run() // best-effort; the declaration in Info.plist is what counts
	}
	return nil
}

// bundlePath walks up from the executable to find the enclosing .app, if any.
// A macOS bundle executable lives at Foo.app/Contents/MacOS/Foo.
func bundlePath(exe string) string {
	dir := filepath.Dir(exe) // .../Contents/MacOS
	if filepath.Base(dir) != "MacOS" {
		return ""
	}
	contents := filepath.Dir(dir) // .../Contents
	if filepath.Base(contents) != "Contents" {
		return ""
	}
	app := filepath.Dir(contents) // .../Foo.app
	if strings.HasSuffix(app, ".app") {
		return app
	}
	return ""
}

// --- Linux -------------------------------------------------------------------

// registerLinux writes a .desktop file declaring the scheme and registers it
// with xdg-mime, the standard freedesktop mechanism.
func registerLinux(exe string) error {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	appsDir := filepath.Join(dataHome, "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return err
	}

	desktop := filepath.Join(appsDir, "signal-station.desktop")
	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=Signal Station\n" +
		"Exec=" + exe + " %u\n" +
		"Terminal=false\n" +
		"NoDisplay=true\n" +
		"MimeType=x-scheme-handler/" + CaptchaScheme + ";\n"
	if err := os.WriteFile(desktop, []byte(content), 0o644); err != nil {
		return err
	}

	cmd := exec.Command("xdg-mime", "default", "signal-station.desktop",
		"x-scheme-handler/"+CaptchaScheme)
	_ = cmd.Run() // best-effort; some minimal desktops lack xdg-mime
	return nil
}
