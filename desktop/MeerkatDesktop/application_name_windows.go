package main

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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

// COM starts the server outside the launcher's environment. Carry only the
// profile selection and its non-secret runtime paths into that activation.
var toastEnvironment = []string{"APPDATA", "DSH_HOME", "MEERKAT_EDGE_ALLOW_ROOT", "MEERKAT_BIND_HOST"}

func loadToastProfile() error {
	if len(os.Args) < 2 || os.Args[1] != "--toast-profile" {
		return nil
	}
	if len(os.Args) < 2+len(toastEnvironment) || !filepath.IsAbs(os.Args[2]) {
		return fmt.Errorf("invalid notification profile")
	}
	for i, name := range toastEnvironment {
		if err := os.Setenv(name, os.Args[2+i]); err != nil {
			return err
		}
	}
	return nil
}

func registerToastProfile() {
	name := desktopApplicationName()
	if name == "Meerkat" {
		return
	}
	if err := setToastServer(name); err != nil {
		log.Printf("通知冷启动配置失败: %v", err)
	}
}

func setToastServer(name string) error {
	app, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+name, registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer app.Close()
	guid, _, err := app.GetStringValue("CustomActivator")
	if err != nil {
		return err
	}
	if _, err = windows.GUIDFromString(guid); err != nil {
		return err
	}
	server, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\CLSID\`+guid+`\LocalServer32`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer server.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{exe, "--toast-profile"}
	for _, name := range toastEnvironment {
		args = append(args, os.Getenv(name))
	}
	args = append(args, "%1")
	return server.SetStringValue("", windows.ComposeCommandLine(args))
}
