package androidclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestServiceLateClose(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.URL.Path) }))
	defer upstream.Close()
	var sessions serviceSessions
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	_, err := sessions.open(1, "http://100.64.1.1/first", dial)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sessions.open(2, "http://100.64.1.1/second", dial)
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.close(2)
	sessions.close(1) // delayed callback from the first dialog
	assertServicePage(t, second, "/second")
	if _, err := sessions.open(3, "invalid", dial); err == nil {
		t.Fatal("accepted invalid URL")
	}
	sessions.close(3)
	assertServicePage(t, second, "/second")
	sessions.close(4)
	if _, err := sessions.open(4, "http://100.64.1.1/late", dial); err == nil {
		t.Fatal("opened after activity destruction")
	}
	assertServicePage(t, second, "/second")
	sessions.close(2)
	if sessions.active != nil {
		t.Fatal("active session survived its own close")
	}

	const last = 20
	var wg sync.WaitGroup
	begin := make(chan struct{})
	var newest string
	for id := uint64(5); id <= last; id++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			<-begin
			entry, err := sessions.open(id, fmt.Sprintf("http://100.64.1.1/%d", id), dial)
			if id == last {
				if err != nil {
					t.Error(err)
				}
				newest = entry
			} else {
				sessions.close(id)
			}
		}(id)
	}
	close(begin)
	wg.Wait()
	defer sessions.close(last)
	assertServicePage(t, newest, "/20")
}

func assertServicePage(t *testing.T, entry, want string) {
	t.Helper()
	u, err := url.Parse(entry)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, n, net.JoinHostPort("127.0.0.1", u.Port()))
	}}
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, Timeout: 3 * time.Second}
	resp, err := client.Get(entry)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || string(body) != want {
		t.Fatalf("GET %s: %s %q %v", entry, resp.Status, body, err)
	}
}
