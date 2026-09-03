package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// appDataDir returns the root directory where Signal Station keeps everything:
// its own config, the signal-cli account store, and one Signal Desktop profile
// per account.
//
//	macOS   ~/Library/Application Support/SignalStation
//	Windows %APPDATA%\SignalStation
//	Linux   ~/.config/SignalStation
func appDataDir() string {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("APPDATA")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, "AppData", "Roaming")
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "Library", "Application Support")
	default:
		base = os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(base, "SignalStation")
}

func configFilePath() string { return filepath.Join(appDataDir(), "accounts.json") }

// signalCLIDataDir is passed to signal-cli as --config. Keeping it inside our
// own folder means Signal Station never touches a signal-cli install the user
// may already be running elsewhere.
func signalCLIDataDir() string { return filepath.Join(appDataDir(), "signal-cli-data") }

// profilesDir holds one --user-data-dir per account.
func profilesDir() string { return filepath.Join(appDataDir(), "profiles") }

func logDirPath() string { return filepath.Join(appDataDir(), "logs") }

func ensureDirs() error {
	for _, d := range []string{appDataDir(), signalCLIDataDir(), profilesDir(), logDirPath()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// slugify turns a label or phone number into something safe to use as a
// directory name on both NTFS and APFS.
func slugify(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		case r == '+':
			b.WriteString("p")
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if out == "" {
		out = "account"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

// fileExists reports whether path exists and is not a directory.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}
