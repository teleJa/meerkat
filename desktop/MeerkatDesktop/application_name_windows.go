package main

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Wails uses the application name as the Toast AUMID and COM registration key.
// Keep the installed user's identity and give alternate profiles their own activator.
func desktopApplicationName() string {
	roaming, err := windows.KnownFolderPath(windows.FOLDERID_RoamingAppData, 0)
	dir := filepath.Clean(configDir())
	if err == nil && strings.EqualFold(dir, filepath.Join(roaming, "Meerkat")) {
		return "Meerkat"
	}
	digest := sha256.Sum256([]byte(strings.ToLower(dir)))
	return fmt.Sprintf("Meerkat.%x", digest[:8])
}
