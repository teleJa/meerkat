package hub

import (
	"compress/gzip"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHarnessPathProxyRefreshesOnlyEntranceGET(t *testing.T) {
	var logins, version, entries atomic.Int32
	var rejectLogin, rejectEntry, rejectBusiness atomic.Bool
	var launchToken atomic.Value
	launchToken.Store("launch")
	var pauseEntry atomic.Bool
	entryStarted := make(chan struct{})
	resumeEntry := make(chan struct{})
	version.Store(1)
	rejectLogin.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := r.URL.Query().Get("token"); token != "" {
			logins.Add(1)
			if rejectLogin.Load() || token != launchToken.Load().(string) {
				http.Error(w, dshBrowserAuthError[:len(dshBrowserAuthError)-1], http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "dsh", Value: strconv.Itoa(int(version.Load())), Path: "/"})
			w.WriteHeader(http.StatusOK)
			return
		}
		entries.Add(1)
		if pauseEntry.CompareAndSwap(true, false) {
			close(entryStarted)
			<-resumeEntry
		}
		if rejectBusiness.Load() {
			http.Error(w, "business access denied", http.StatusUnauthorized)
			return
		}
		cookie, err := r.Cookie("dsh")
		if rejectEntry.Load() || err != nil || cookie.Value != strconv.Itoa(int(version.Load())) {
			if r.Header.Get("Accept-Encoding") == "gzip, deflate, br" {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Content-Encoding", "gzip")
				w.WriteHeader(http.StatusUnauthorized)
				gz := gzip.NewWriter(w)
				_, _ = io.WriteString(gz, dshBrowserAuthError)
				_ = gz.Close()
				return
			}
			http.Error(w, dshBrowserAuthError[:len(dshBrowserAuthError)-1], http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "authenticated")
	}))
	defer upstream.Close()

	h := testHub(t)
	now := time.Now()
	device, err := h.registerDevice(DeviceInfo{Name: "auth-test", Hostname: "auth-test", OS: "darwin", Arch: "arm64", Role: "test", BootID: "auth-test"}, "0.1.0", "1", now)
	if err != nil {
		t.Fatal(err)
	}
	// Use the fixture's dynamic port so this test can run alongside a local Hub.
	h.harnessHTTP[device.Device.ID] = newHarnessProxy(upstream.Listener.Addr().(*net.TCPAddr).Port, upstream.URL+"/")
	if err := h.setHarnessURL(device.Device.ID, upstream.URL+"/?token=launch", now); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.registerHTTP(mux)
	root := "/harness-proxy/" + device.Device.ID
	request := func(method, path, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, root+path, nil)
		req.Header.Set("Authorization", "Bearer "+h.AdminToken())
		req.Header.Set("Accept", accept)
		if path == "/index.html" {
			req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	check := func(method, path, accept string, want int) {
		t.Helper()
		if got := request(method, path, accept); got.Code != want {
			t.Fatalf("%s %s: status=%d wanted %d body=%q", method, path, got.Code, want, got.Body.String())
		}
	}

	check(http.MethodGet, "/", "", http.StatusBadGateway)
	rejectLogin.Store(false)
	check(http.MethodGet, "/", "", http.StatusOK)
	if logins.Load() != 2 {
		t.Fatalf("failed login prevented fresh bootstrap: logins=%d", logins.Load())
	}

	version.Store(2)
	check(http.MethodPost, "/", "", http.StatusUnauthorized)
	check(http.MethodGet, "/", "text/event-stream", http.StatusUnauthorized)
	check(http.MethodGet, "/api/remote.mux", "", http.StatusUnauthorized)
	if logins.Load() != 2 {
		t.Fatalf("non-entrance requests attempted re-login: logins=%d", logins.Load())
	}

	var concurrent sync.WaitGroup
	for range 8 {
		concurrent.Go(func() {
			if got := request(http.MethodGet, "/index.html", ""); got.Code != http.StatusOK {
				t.Errorf("parallel entrance failed: %d %q", got.Code, got.Body.String())
			}
		})
	}
	concurrent.Wait()
	if logins.Load() != 3 {
		t.Fatalf("concurrent navigation logged in more than once: logins=%d", logins.Load())
	}

	rejectEntry.Store(true)
	before := entries.Load()
	got := request(http.MethodGet, "/", "")
	if got.Code != http.StatusUnauthorized || got.Body.String() != dshBrowserAuthError || entries.Load()-before != 2 || logins.Load() != 4 {
		t.Fatalf("expected one replay then the upstream 401: status=%d body=%q attempts=%d logins=%d", got.Code, got.Body.String(), entries.Load()-before, logins.Load())
	}

	rejectLogin.Store(true)
	before = entries.Load()
	got = request(http.MethodGet, "/", "")
	if got.Code != http.StatusUnauthorized || got.Body.String() != dshBrowserAuthError || entries.Load()-before != 1 || logins.Load() != 5 {
		t.Fatalf("failed refresh should preserve the initial 401: status=%d body=%q attempts=%d logins=%d", got.Code, got.Body.String(), entries.Load()-before, logins.Load())
	}

	rejectLogin.Store(false)
	rejectEntry.Store(false)
	check(http.MethodGet, "/", "", http.StatusOK)
	t.Run("business 401 at the entrance does not trigger a login", func(t *testing.T) {
		rejectBusiness.Store(true)
		defer rejectBusiness.Store(false)
		beforeEntries, beforeLogins := entries.Load(), logins.Load()
		got := request(http.MethodGet, "/", "")
		if got.Code != http.StatusUnauthorized || got.Body.String() != "business access denied\n" || entries.Load()-beforeEntries != 1 || logins.Load() != beforeLogins {
			t.Fatalf("business 401 was changed or replayed: status=%d body=%q attempts=%d logins=%d", got.Code, got.Body.String(), entries.Load()-beforeEntries, logins.Load()-beforeLogins)
		}
	})
	t.Run("refresh uses the current launch URL", func(t *testing.T) {
		beforeEntries, beforeLogins := entries.Load(), logins.Load()
		result := make(chan *httptest.ResponseRecorder, 1)
		pauseEntry.Store(true)
		go func() { result <- request(http.MethodGet, "/", "") }()
		select {
		case <-entryStarted:
		case <-time.After(5 * time.Second):
			close(resumeEntry)
			t.Fatal("entrance request did not reach the upstream")
		}
		launchToken.Store("replacement")
		version.Store(3)
		err := h.setHarnessURL(device.Device.ID, upstream.URL+"/?token=replacement", time.Now())
		close(resumeEntry)
		got := <-result
		if err != nil {
			t.Fatal(err)
		}
		if got.Code != http.StatusOK || got.Body.String() != "authenticated" || entries.Load()-beforeEntries != 2 || logins.Load()-beforeLogins != 1 {
			t.Fatalf("expected a single refresh using the new launch URL: status=%d body=%q attempts=%d logins=%d", got.Code, got.Body.String(), entries.Load()-beforeEntries, logins.Load()-beforeLogins)
		}
	})
}

func TestHarnessPathProxyReusesConnectionsWithoutSharingCookies(t *testing.T) {
	h := testHub(t)
	mux := http.NewServeMux()
	h.registerHTTP(mux)

	type fixture struct {
		id          string
		entry       *harnessProxy
		connections *atomic.Int32
	}
	setup := func(label string) fixture {
		t.Helper()
		connections := &atomic.Int32{}
		upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("token") == label {
				http.SetCookie(w, &http.Cookie{Name: "dsh", Value: label, Path: "/"})
				return
			}
			cookie, err := r.Cookie("dsh")
			if err != nil || cookie.Value != label || r.Header.Get("Authorization") != "" {
				http.Error(w, "unexpected credentials", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, label)
		}))
		upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				connections.Add(1)
			}
		}
		upstream.Start()
		t.Cleanup(upstream.Close)
		now := time.Now()
		device, err := h.registerDevice(DeviceInfo{Name: label, Hostname: label, OS: "windows", Arch: "amd64", Role: "test", BootID: label}, "0.1.0", "1", now)
		if err != nil {
			t.Fatal(err)
		}
		entry := newHarnessProxy(upstream.Listener.Addr().(*net.TCPAddr).Port, upstream.URL+"/")
		h.harnessHTTP[device.Device.ID] = entry
		if err := h.setHarnessURL(device.Device.ID, upstream.URL+"/?token="+label, now); err != nil {
			t.Fatal(err)
		}
		return fixture{device.Device.ID, entry, connections}
	}
	a := setup("device-a")
	b := setup("device-b")
	check := func(device fixture, want string) {
		for range 8 {
			req := httptest.NewRequest(http.MethodPost, "/harness-proxy/"+device.id+"/api/commands/list", nil)
			req.Header.Set("Authorization", "Bearer "+h.AdminToken())
			req.Header.Set("Cookie", "dsh=inbound")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != 200 || w.Body.String() != want {
				t.Errorf("%s: got %d %q", want, w.Code, w.Body.String())
			}
		}
	}
	var group sync.WaitGroup
	group.Go(func() { check(a, "device-a") })
	group.Go(func() { check(b, "device-b") })
	group.Wait()
	if a.connections.Load() != 1 || b.connections.Load() != 1 {
		t.Fatalf("expected one connection per device for login and 8 POSTs, got %d/%d", a.connections.Load(), b.connections.Load())
	}
	h.dropHarnessProxy(a.id)
	_, err := a.entry.RoundTrip(httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	if !errors.Is(err, errHarnessProxyClosed) {
		t.Fatalf("retired pool still accepts requests: %v", err)
	}
	check(b, "device-b")
	if b.connections.Load() != 1 {
		t.Fatalf("dropping one device reset the other pool: %d connections", b.connections.Load())
	}
}
