package main

import (
	"path/filepath"
	"strings"
)

// isBatch reports whether path is a Windows batch launcher, which cannot be
// executed directly and has to go through cmd.exe.
func isBatch(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".bat", ".cmd":
		return true
	}
	return false
}

// windowsQuote wraps an argument so cmd.exe passes it through untouched.
//
// This exists because Go's exec package quotes arguments per CommandLineToArgvW
// rules, which cmd.exe does not follow. Go only adds quotes when an argument
// contains whitespace, so a Signal linking URI —
//
//	sgnl://linkdevice?uuid=…&pub_key=…
//
// — goes through bare. cmd.exe then reads the & as a command separator and tries
// to execute everything after it as a second command, producing the useless
// "not recognized as an internal or external command" error.
//
// Quoting unconditionally also covers the rest of cmd's metacharacters:
// & | < > ^ ( ) and spaces.
func windowsQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')

	// A run of backslashes is only special when it precedes a quote, where each
	// pair collapses. Double them there so a path ending in \ does not escape
	// the closing quote.
	slashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			slashes++
			b.WriteByte(c)
		case '"':
			// Escape this quote, plus every backslash leading up to it.
			b.WriteString(strings.Repeat(`\`, slashes+1))
			b.WriteByte('"')
			slashes = 0
		default:
			slashes = 0
			b.WriteByte(c)
		}
	}
	b.WriteString(strings.Repeat(`\`, slashes)) // trailing run, doubled
	b.WriteByte('"')
	return b.String()
}

// batchCommandLine builds the raw command line for running a .bat through
// cmd.exe with arguments preserved exactly.
//
// The /s flag selects cmd's simple parsing rule: when the string after /c both
// begins and ends with a quote, cmd strips exactly those two characters and
// treats the remainder verbatim. Without /s, cmd applies a convoluted
// quote-stripping heuristic that mangles quoted paths.
func batchCommandLine(bat string, args []string) string {
	var b strings.Builder
	b.WriteString(`cmd.exe /s /c "`)
	b.WriteString(windowsQuote(bat))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(windowsQuote(a))
	}
	b.WriteByte('"')
	return b.String()
}
