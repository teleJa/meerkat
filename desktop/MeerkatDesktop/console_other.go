//go:build !windows

package main

import "os/exec"

// hideConsole 在非 Windows 平台上无操作。
func hideConsole(*exec.Cmd) {}
