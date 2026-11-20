package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"meerkat/desktop/internal/update"
)

// MEERKAT_UPDATER_TEST_INSTALLERS points to the old, new and failing test installers.
// /UPDATE writes only files into the requested directory. The test does not
// open the Meerkat UI, change its installed registry values, or start any Hub.
func TestWindowsInstallerTransaction(t *testing.T) {
	packages := os.Getenv("MEERKAT_UPDATER_TEST_INSTALLERS")
	if packages == "" {
		t.Skip("requires the Windows NSIS test installers")
	}
	testExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		installer string
		ready     bool
		wantError string
	}{
		{"upgrade from 0.1.0 to 0.2.0", filepath.Join(packages, "Meerkat-Setup-0.2.0.exe"), true, ""},
		{"installer failure restores 0.1.0", filepath.Join(packages, "Meerkat-Setup-Fails.exe"), false, "exit status 2"},
		{"new process fails readiness and restores 0.1.0", filepath.Join(packages, "Meerkat-Setup-0.2.0.exe"), false, "新版尚未就绪便退出"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "Test Programs", "Meerkat")
			original := filepath.Join(packages, "Meerkat-Setup-0.1.0.exe")
			oldCmd := exec.Command(original)
			oldCmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(original) + " /S /UPDATE /D=" + dir}
			if output, err := oldCmd.CombinedOutput(); err != nil {
				t.Fatalf("install old version: %v %s", err, output)
			}
			assertInstalledVersion(t, dir, "0.1.0")
			staging := t.TempDir()
			manifest, err := serveAndDownloadTestInstaller(tc.installer, staging)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("verified signed manifest %s and downloaded installer SHA-256", manifest.Version)
			launch := func(newDir string, args ...string) (*exec.Cmd, error) {
				assertInstalledVersion(t, newDir, "0.2.0")
				child := exec.Command(testExe, "-test.run=^TestUpdateReadinessChild$")
				child.Env = append(os.Environ(), "MEERKAT_UPDATER_TEST_READY="+args[1], "MEERKAT_UPDATER_TEST_SUCCESS="+fmt.Sprint(tc.ready))
				if err := child.Start(); err != nil {
					return nil, err
				}
				return child, nil
			}
			committed := false
			err = applyWindowsUpdate(dir, staging, manifest, log.New(os.Stdout, "updater: ", 0), launch, func() error {
				committed = true
				return nil
			})
			if tc.wantError == "" {
				if err != nil || !committed {
					t.Fatalf("update did not commit: %v", err)
				}
				assertInstalledVersion(t, dir, "0.2.0")
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || committed {
					t.Fatalf("unexpected result: committed=%t error=%v", committed, err)
				}
				t.Logf("expected update failure: %v", err)
				assertInstalledVersion(t, dir, "0.1.0")
			}
			if _, err := os.Stat(dir + ".old"); !os.IsNotExist(err) {
				t.Fatalf("old directory remained after transaction: %v", err)
			}
		})
	}
}

func serveAndDownloadTestInstaller(installer, staging string) (update.Manifest, error) {
	binary, err := os.ReadFile(installer)
	if err != nil {
		return update.Manifest{}, err
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return update.Manifest{}, err
	}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	sum := sha256.Sum256(binary)
	payload, err := json.Marshal(update.Manifest{Version: "0.2.0", Platforms: map[string]update.Platform{
		"windows-amd64": {URL: server.URL + "/setup.exe", SHA256: hex.EncodeToString(sum[:])},
	}})
	if err != nil {
		return update.Manifest{}, err
	}
	mux.HandleFunc("GET /latest.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(update.Envelope{Manifest: payload, Signature: ed25519.Sign(private, payload)})
	})
	mux.HandleFunc("GET /setup.exe", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(binary)
	})
	_, manifest, err := update.Fetch(server.URL+"/latest.json", public)
	if err != nil {
		return update.Manifest{}, err
	}
	platform, err := manifest.Package("windows", "amd64")
	if err != nil {
		return update.Manifest{}, err
	}
	return manifest, update.Download(platform, staging, func(int64, int64) {})
}

func TestLocalDSHUpdateGuard(t *testing.T) {
	for _, tc := range []struct {
		name, dshStatus, sessions string
		idle                      bool
	}{
		{"stopped", `{"online":true,"dsh":{"status":"stopped","has_url":false}}`, "", true},
		{"idle", `{"online":true,"dsh":{"status":"running","has_url":true}}`, `{"result":{"ok":true,"value":{"items":[]}}}`, true},
		{"running", `{"online":true,"dsh":{"status":"running","has_url":true}}`, `{"result":{"ok":true,"value":{"items":[{"running":true}]}}}`, false},
		{"offline", `{"online":false,"dsh":{"status":"stopped","has_url":false}}`, "", false},
		{"unknown", `{"online":true,"dsh":{"status":"running","has_url":true}}`, `{"result":{"ok":false}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/devices/dev/dsh", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, tc.dshStatus)
			})
			mux.HandleFunc("POST /harness-proxy/dev/api/session/list", func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Method string }
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Method != "session/list" {
					t.Error("unexpected session list request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = fmt.Fprint(w, tc.sessions)
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			if got := localDSHIdle(server.Client(), server.URL+"/", "dev"); got != tc.idle {
				t.Fatalf("idle = %t, want %t", got, tc.idle)
			}
		})
	}
}

func assertInstalledVersion(t *testing.T, dir, want string) {
	t.Helper()
	output, err := exec.Command(filepath.Join(dir, "Meerkat.exe"), "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != want {
		t.Fatalf("installed executable version: got %q (%v), want %s", output, err, want)
	}
	t.Logf("verified installed executable version %s", want)
}

func TestUpdateReadinessChild(t *testing.T) {
	staging := os.Getenv("MEERKAT_UPDATER_TEST_READY")
	if staging == "" {
		t.Skip("emulates the native startup readiness marker")
	}
	if os.Getenv("MEERKAT_UPDATER_TEST_SUCCESS") != "true" {
		os.Exit(2)
	}
	data, err := json.Marshal(readyUpdate{PID: os.Getpid(), Version: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "ready.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
}
