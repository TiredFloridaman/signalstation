//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// hideConsole stops a black cmd window from flashing up every time signal-cli
// runs, which on Windows would otherwise happen on every single call.
func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// applyRawCmdLine hands CreateProcess an exact command line instead of letting
// Go assemble one from Args. Go quotes per CommandLineToArgvW, which cmd.exe
// does not honour, so anything routed through cmd needs the line built by hand.
func applyRawCmdLine(cmd *exec.Cmd, line string) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = line
}

// rawCmdLine returns the hand-built command line, if one was set.
func rawCmdLine(cmd *exec.Cmd) string {
	if cmd.SysProcAttr == nil {
		return ""
	}
	return cmd.SysProcAttr.CmdLine
}

// detach starts Signal Desktop in its own process group so closing Signal
// Station does not take the messenger down with it.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
