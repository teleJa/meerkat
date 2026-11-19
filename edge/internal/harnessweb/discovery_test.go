package harnessweb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveryRoundTrip(t *testing.T) {
	home := t.TempDir()
	discovery := Discovery{
		Profile: "bot",
		URL:     "http://127.0.0.1:51901/?token=secret",
		PID:     os.Getpid(),
		Port:    51901,
	}
	if err := WriteDiscovery(home, discovery); err != nil {
		t.Fatalf("write: %v", err)
	}
	path := DiscoveryPath(home, "bot")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	read, err := ReadDiscovery(home, "bot")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read.Profile != "bot" || read.URL != discovery.URL || read.PID != discovery.PID || read.Port != discovery.Port {
		t.Fatalf("read = %+v", read)
	}
	if read.StartedAt == "" {
		t.Fatalf("started_at should be filled")
	}
	if !read.Alive() {
		t.Fatalf("current process should count as alive")
	}
	if err := RemoveDiscovery(home, "bot"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("discovery should be gone")
	}
	if err := RemoveDiscovery(home, "bot"); err != nil {
		t.Fatalf("second remove should be a no-op: %v", err)
	}
}

func TestReadDiscoveryRejectsMismatchAndBadURL(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, DiscoveryDir), 0o700); err != nil {
		t.Fatal(err)
	}
	path := DiscoveryPath(home, "bot")
	if err := os.WriteFile(path, []byte(`{"profile":"web","url":"http://127.0.0.1:1/?token=x","pid":1,"port":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDiscovery(home, "bot"); err == nil {
		t.Fatalf("profile mismatch should fail")
	}
	if err := os.WriteFile(path, []byte(`{"profile":"bot","url":"not-a-url","pid":1,"port":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDiscovery(home, "bot"); err == nil {
		t.Fatalf("invalid URL should fail")
	}
}

func TestDiscoveryPortFallsBackToURL(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, DiscoveryDir), 0o700); err != nil {
		t.Fatal(err)
	}
	path := DiscoveryPath(home, "bot")
	if err := os.WriteFile(path, []byte(`{"profile":"bot","url":"http://127.0.0.1:1/?token=x","pid":999999,"port":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, err := ReadDiscovery(home, "bot")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if discovery.Port != 1 {
		t.Fatalf("port = %d, want 1", discovery.Port)
	}
	if discovery.Alive() {
		t.Fatalf("stale pid with closed port should be dead")
	}
}

func TestValidProfileName(t *testing.T) {
	valid := []string{"probe", "team-2", "a.b_c"}
	for _, name := range valid {
		if err := ValidProfileName(name); err != nil {
			t.Fatalf("%q should be valid: %v", name, err)
		}
	}
	invalid := []string{"", "Web", "../x", "a..b", "web", "desktop", "a b"}
	for _, name := range invalid {
		if err := ValidProfileName(name); err == nil {
			t.Fatalf("%q should be invalid", name)
		}
	}
}
