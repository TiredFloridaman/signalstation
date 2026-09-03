//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// hideConsole is a no-op outside Windows: launching a child process does not
// create a visible terminal on macOS or Linux.
func hideConsole(cmd *exec.Cmd) {}

// applyRawCmdLine is a no-op outside Windows: raw command lines are a
// CreateProcess concept, and no batch launchers are used here.
func applyRawCmdLine(cmd *exec.Cmd, line string) {}

// rawCmdLine has no meaning outside Windows.
func rawCmdLine(cmd *exec.Cmd) string { return "" }

// detach puts Signal Desktop in a new session so it keeps running after Signal
// Station quits.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
