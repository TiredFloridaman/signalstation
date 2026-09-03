package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ephemeralConfigPath is where Signal Desktop keeps its ephemeral settings,
// relative to a profile's --user-data-dir. This is the file Signal reads the
// contentProtection flag from at startup.
func ephemeralConfigPath(profileDir string) string {
	return filepath.Join(profileDir, "ephemeral.json")
}

// disableContentProtection writes contentProtection:false into a profile's
// ephemeral config so Signal Desktop does not set the screen-capture DRM flag
// on its windows.
//
// Why this works, from Signal Desktop's own startup code (app/main.ts):
//
//	const contentProtection = ephemeralConfig.get('contentProtection');
//	... contentProtection ?? isContentProtectionEnabledByDefault(OS, release)
//	if (typeof contentProtection === 'boolean') {
//	    window.setContentProtection(contentProtection);
//	}
//
// The nullish-coalescing means an explicit boolean overrides the
// Windows-11-on-by-default behaviour, and setContentProtection(false) is what
// omits WDA_EXCLUDEFROMCAPTURE. Writing this before the first launch means the
// linking QR screen is capturable from the very first frame.
//
// The write is careful in three ways, because this file belongs to Signal, not
// to us:
//
//   - It merges into whatever is already there rather than overwriting, so no
//     other ephemeral setting is lost. On a brand-new profile the file does not
//     exist yet and we create a minimal one.
//   - It only ever sets this single key. If the merge or parse fails, it leaves
//     the file untouched and returns the error rather than risking corruption.
//   - It writes atomically via a temp file and rename.
//
// A failure here is not fatal to linking: the caller treats it as best-effort,
// because the phone-camera paste path still works regardless.
func disableContentProtection(profileDir string) error {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return err
	}
	path := ephemeralConfigPath(profileDir)

	// Start from existing contents when present, so we merge rather than clobber.
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			// The file exists but is not the shape we expect. Leave it alone;
			// corrupting Signal's own config would be worse than a manual link.
			return err
		}
	}

	if v, ok := settings["contentProtection"].(bool); ok && !v {
		return nil // already disabled; nothing to do
	}
	settings["contentProtection"] = false

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// contentProtectionDisabled reports whether the profile's ephemeral config
// currently has contentProtection set to false, used to confirm the write took
// and to drive UI messaging.
func contentProtectionDisabled(profileDir string) bool {
	data, err := os.ReadFile(ephemeralConfigPath(profileDir))
	if err != nil {
		return false
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}
	v, ok := settings["contentProtection"].(bool)
	return ok && !v
}
