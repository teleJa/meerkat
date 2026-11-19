//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func showInstanceConflictDialog(title, message string) {
	quote := func(value string) string {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	script := fmt.Sprintf(`Add-Type -AssemblyName PresentationFramework; [System.Windows.MessageBox]::Show(%s,%s,'OK','Warning') | Out-Null`, quote(message), quote(title))
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	hideConsole(cmd)
	_ = cmd.Run()
}
