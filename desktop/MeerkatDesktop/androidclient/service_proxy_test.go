package androidclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestServiceValidation(t *testing.T) {
	for _, raw := range []string{"https://100.64.1.1", "http://127.0.0.1", "http://example.org", "http://user@100.64.1.1", "http://100.64.1.1:65536"} {
		if _, err := serviceTarget(raw); err == nil {
			t.Fatal(raw)
		}
	}
}

func TestServiceTransfer(t *testing.T) {
	cancelled := make(chan struct{})
	data := bytes.Repeat([]byte("0123456789"), 12000)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "100.64.0.5:9000" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected upstream headers or host")
		}
		switch r.URL.Path {
		case "/stream":
			w.Header().Set("Content-Length", "1073741824")
			io.WriteString(w, "first")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(cancelled)
		case "/next":
			http.Redirect(w, r, "http://100.64.0.5:9000/media?view=1", 302)
		case "/external":
			http.Redirect(w, r, "//100.64.0.6:9000/", 302)
		case "/media":
			http.ServeContent(w, r, "movie.mp4", time.Time{}, bytes.NewReader(data))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	target, _ := serviceTarget("http://100.64.0.5:9000/start?stage=finish#player")
	s, err := startService(target, func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != target.Host {
			return nil, fmt.Errorf("wrong target %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	entry, _ := url.Parse(s.entry(target))
	if entry.Host != s.origin.Host || entry.RequestURI() != "/start?stage=finish" || entry.Fragment != "player" {
		t.Fatal(entry)
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, n, net.JoinHostPort("127.0.0.1", entry.Port()))
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, host string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", s.origin.String()+path, nil)
		req.Host = host
		req.Header.Set("Range", "bytes=60001-61000")
		req.Header.Set("Authorization", "test-only")
		req.Header.Set("Cookie", "test=fixture")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := get("/media", entry.Host)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 206 || resp.Header.Get("Content-Range") != "bytes 60001-61000/120000" || !bytes.Equal(body, data[60001:61001]) {
		t.Fatal(resp.Status, err)
	}
	for path, want := range map[string]int{"/external": 502, "/next": 302} {
		r := get(path, entry.Host)
		r.Body.Close()
		if r.StatusCode != want || path == "/next" && !strings.HasPrefix(r.Header.Get("Location"), s.origin.String()+"/media?") {
			t.Fatal(path, r.Status, r.Header)
		}
	}
	r := get("/media", "other.localhost")
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal(r.Status)
	}
	r = get("/stream", entry.Host)
	buf := make([]byte, 5)
	_, err = io.ReadFull(r.Body, buf)
	r.Body.Close()
	if err != nil || string(buf) != "first" {
		t.Fatal(err, string(buf))
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream request not cancelled")
	}
}
