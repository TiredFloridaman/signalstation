package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// LaunchDesktop starts Signal Desktop against a specific profile directory.
//
// Signal Desktop is an Electron app, and Electron keys its single-instance lock
// on --user-data-dir. Point two launches at two different directories and you
// get two independent Signal windows, each linked to a different number.
//
// On macOS the binary inside the .app bundle is invoked directly rather than
// via `open`, because `open` would hand the flags to the already-running
// instance instead of starting a new one.
func LaunchDesktop(binPath, profileDir string) error {
	if !fileExists(binPath) {
		return errors.New("Signal Desktop was not found. Set its location in Settings")
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return fmt.Errorf("create profile folder: %w", err)
	}

	cmd := exec.Command(binPath, "--user-data-dir="+profileDir)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	detach(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Signal Desktop: %w", err)
	}
	// Reap the child so it does not linger as a zombie if it exits while
	// Signal Station is still running.
	go func() { _ = cmd.Wait() }()
	return nil
}

// ProfileIsLinked reports whether a profile directory looks like it has
// completed linking. Signal Desktop creates its SQLCipher database only after a
// successful link, so its presence is a reliable enough signal to drive the UI.
func ProfileIsLinked(profileDir string) bool {
	if profileDir == "" {
		return false
	}
	for _, marker := range []string{
		filepath.Join(profileDir, "sql", "db.sqlite"),
		filepath.Join(profileDir, "config.json"),
	} {
		if fileExists(marker) {
			return true
		}
	}
	return false
}

// DetectSignalDesktop returns the first Signal Desktop executable found in the
// standard install locations for this platform.
func DetectSignalDesktop() string {
	var candidates []string

	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		program := os.Getenv("ProgramFiles")
		if program == "" {
			program = `C:\Program Files`
		}
		candidates = []string{
			filepath.Join(local, "Programs", "signal-desktop", "Signal.exe"),
			filepath.Join(local, "Programs", "Signal", "Signal.exe"),
			filepath.Join(program, "Signal", "Signal.exe"),
			filepath.Join(program, "signal-desktop", "Signal.exe"),
		}
	case "darwin":
		candidates = []string{
			"/Applications/Signal.app/Contents/MacOS/Signal",
			expandHome("~/Applications/Signal.app/Contents/MacOS/Signal"),
		}
	default:
		candidates = []string{
			"/opt/Signal/signal-desktop",
			"/usr/bin/signal-desktop",
			"/usr/local/bin/signal-desktop",
			"/snap/bin/signal-desktop",
		}
	}

	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	if p, err := exec.LookPath("signal-desktop"); err == nil {
		return p
	}
	return ""
}

// DesktopDownloadURL is shown when Signal Desktop is missing.
const DesktopDownloadURL = "https://signal.org/download/"

// SignalCLIReleasesURL is shown when signal-cli is missing.
const SignalCLIReleasesURL = "https://github.com/AsamK/signal-cli/releases"
