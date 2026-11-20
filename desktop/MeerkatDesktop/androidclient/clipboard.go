package androidclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	clipboardPath          = "/__meerkat/clipboard"
	clipboardTokenLifetime = 10 * time.Minute
	clipboardTextLimit     = 1 << 20
	clipboardTokenHeader   = "X-Meerkat-Clipboard"
)

//go:embed clipboard.js
var clipboardScript string

type clipboardNavigationKey struct{}

// Only the Android shell's direct DSH iframe navigation can obtain a grant.
// All devices' DSH scripts run on one trusted origin. User-authored documents
// must be on a different origin or in an opaque sandbox.
func isClipboardNavigation(r *http.Request) bool {
	const prefix = "/harness-proxy/"
	device, ok := strings.CutPrefix(r.URL.Path, prefix)
	device, trailingSlash := strings.CutSuffix(device, "/")
	return ok && trailingSlash && device != "" && !strings.Contains(device, "/") &&
		r.Method == http.MethodGet && r.Header.Get("Sec-Fetch-Dest") == "iframe" &&
		r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Site") == "cross-site" &&
		r.Referer() == "https://wails.localhost/"
}

func prepareClipboardNavigation(r *http.Request) {
	if !isClipboardNavigation(r) {
		return
	}
	*r = *r.WithContext(context.WithValue(r.Context(), clipboardNavigationKey{}, true))
	r.Header.Del("Referer")
	r.Header.Del("If-None-Match")
	r.Header.Del("If-Modified-Since")
}

type clipboardBridge struct {
	host    string
	write   func(string) bool
	log     func(string, ...any)
	mu      sync.Mutex
	token   string
	expires time.Time
}

func (b *clipboardBridge) inject(r *http.Request, body []byte) ([]byte, bool) {
	if b.write == nil || r.Context().Value(clipboardNavigationKey{}) != true {
		return body, false
	}
	// Leave the document's charset declaration near the start of the response.
	// DSH loads its client bundle as a module, which runs after parsing the head.
	idx := bytes.LastIndex(body, []byte("</head>"))
	if idx < 0 {
		return body, false
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		b.log("copy: could not issue page authorization")
		return body, false
	}
	token := hex.EncodeToString(random[:])
	b.mu.Lock()
	b.token = token
	b.expires = time.Now().Add(clipboardTokenLifetime)
	b.mu.Unlock()
	script := `<script data-meerkat-clipboard="` + token + `">` + clipboardScript + `</script>`
	next := make([]byte, 0, len(body)+len(script))
	next = append(next, body[:idx]...)
	next = append(next, script...)
	return append(next, body[idx:]...), true
}

func (b *clipboardBridge) proxy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != clipboardPath {
			next.ServeHTTP(w, r)
			return
		}
		b.serve(w, r)
	})
}

func (b *clipboardBridge) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Host != b.host || r.Header.Get("Origin") != "http://"+b.host ||
		r.Header.Get("Sec-Fetch-Site") != "same-origin" || r.Header.Get("Sec-Fetch-Mode") != "same-origin" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	b.mu.Lock()
	valid := time.Now().Before(b.expires) && subtle.ConstantTimeCompare([]byte(r.Header.Get(clipboardTokenHeader)), []byte(b.token)) == 1
	b.mu.Unlock()
	if !valid {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	// JSON escaping can use six bytes for a single byte of clipboard text.
	r.Body = http.MaxBytesReader(w, r.Body, 6*clipboardTextLimit+64)
	var payload struct {
		Text   *string `json:"text"`
		Native bool    `json:"native"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(new(any)) != io.EOF || (payload.Text != nil) == payload.Native {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if payload.Native {
		b.log("copy=native")
	} else {
		if !utf8.ValidString(*payload.Text) || len(*payload.Text) > clipboardTextLimit {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		if b.write == nil || !b.write(*payload.Text) {
			b.log("copy: system clipboard write failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		b.log("copy=bridge")
	}
	w.WriteHeader(http.StatusNoContent)
}
