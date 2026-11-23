package update

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReleasePackagePinsRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tags/v0.2.4" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		io.WriteString(w, `{"id":123,"tag_name":"v0.2.4","assets":[{"id":456,"name":"app.apk"}]}`)
	}))
	defer server.Close()
	for _, id := range []int64{123, 124, 0} {
		got, err := ReleasePackageURL(context.Background(), server.URL, "0.2.4", "https://example.org/app.apk", id)
		if id == 123 {
			if err != nil || got != server.URL+"/assets/456" {
				t.Fatalf("matching release: %q %v", got, err)
			}
		} else if err == nil || got != "" {
			t.Fatalf("accepted changed or missing release ID %d", id)
		}
	}
}
