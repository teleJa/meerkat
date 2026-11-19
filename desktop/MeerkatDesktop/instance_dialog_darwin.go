//go:build darwin

package main

import (
	"os/exec"
	"strings"
)

func showInstanceConflictDialog(title, message string) {
	title = strings.Join(strings.Fields(title), " ")
	message = strings.Join(strings.Fields(message), " ")
	script := `display alert "` + escapeAppleScript(title) + `" message "` + escapeAppleScript(message) + `" as critical`
	_ = exec.Command("osascript", "-e", script).Run()
}

func escapeAppleScript(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
