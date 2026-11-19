package hub

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

type harnessLoginCache struct {
	mu      sync.Mutex
	current *harnessLogin
}

// Each device proxy owns its own cache. In-flight entries publish their
// result by closing done. The URL identifies the launch credential in use.
type harnessLogin struct {
	url  string
	done chan struct{}
	jar  http.CookieJar
	err  error
}

func (entry *harnessProxy) jar(localURL string, stale http.CookieJar) (http.CookieJar, error) {
	cache := &entry.login
	cache.mu.Lock()
	if current := cache.current; current != nil && current.url == localURL {
		select {
		case <-current.done:
			if stale == nil || current.jar != stale {
				cache.mu.Unlock()
				return current.jar, current.err
			}
		default:
			cache.mu.Unlock()
			<-current.done
			return current.jar, current.err
		}
	} else if stale != nil {
		// Another entrance already discarded this jar. A new navigation can
		// try to log in again, but late 401s from the old jar cannot do so.
		cache.mu.Unlock()
		return nil, errors.New("DSH session already invalidated")
	}
	current := &harnessLogin{url: localURL, done: make(chan struct{})}
	cache.current = current
	cache.mu.Unlock()

	current.jar, current.err = loginHarnessPath(localURL, entry)
	cache.mu.Lock()
	if current.err != nil && cache.current == current {
		cache.current = nil
	}
	close(current.done)
	cache.mu.Unlock()
	return current.jar, current.err
}

func loginHarnessPath(localURL string, transport http.RoundTripper) (http.CookieJar, error) {
	jar := newJar()
	// Exchange the launch URL for a DSH session before serving any iframe,
	// resource, or WebSocket request with this jar.
	client := &http.Client{Jar: jar, Transport: transport, Timeout: 15 * time.Second}
	response, err := client.Get(localURL)
	if err != nil {
		return nil, errors.New("DSH login request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("DSH login returned HTTP %d", response.StatusCode)
	}
	return jar, nil
}

const dshBrowserAuthError = "dsh web authentication required; reopen the URL printed by dsh web.\n"

type harnessEntranceTransport struct {
	*jarTransport
	refresh func() (*jarTransport, error)
}

type bufferedHarnessResponse struct {
	io.Reader
	io.Closer
}

func (t *harnessEntranceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.jarTransport.RoundTrip(req)
	if err != nil || response.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain") {
		return response, err
	}
	if !isDSHBrowserAuthError(response) {
		return response, nil
	}

	refreshed, err := t.refresh()
	if err != nil {
		return response, nil
	}
	_ = response.Body.Close()
	// Use the base jar transport directly so a second 401 is returned as-is.
	return refreshed.RoundTrip(req)
}

func isDSHBrowserAuthError(response *http.Response) bool {
	// Inspect only the small authentication response and preserve its original
	// bytes and headers if it must be returned to the caller.
	reader := bufio.NewReader(response.Body)
	response.Body = bufferedHarnessResponse{Reader: reader, Closer: response.Body}
	limit := len(dshBrowserAuthError) + 1
	switch strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding"))) {
	case "", "identity":
		data, err := reader.Peek(limit)
		return err == io.EOF && string(data) == dshBrowserAuthError
	case "gzip":
		const maxCompressedAuthError = 1024
		data, err := reader.Peek(maxCompressedAuthError)
		if err != io.EOF {
			return false
		}
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return false
		}
		defer gz.Close()
		decoded, err := io.ReadAll(io.LimitReader(gz, int64(limit)))
		return err == nil && string(decoded) == dshBrowserAuthError
	default:
		return false
	}
}

func isHarnessEntranceGET(r *http.Request, path string) bool {
	if r.Method != http.MethodGet || (path != "/" && path != "/index.html") || (r.Body != nil && r.Body != http.NoBody) || r.Header.Get("Upgrade") != "" {
		return false
	}
	return !strings.Contains(strings.ToLower(strings.Join(r.Header.Values("Accept"), ",")), "text/event-stream")
}

// handleHarnessPathProxy 把 /harness-proxy/{deviceID}/... 代理到 Hub 本机的
// DSH 反向代理，让远端 Meerkat UI 也能通过 Hub 访问 DSH。
func (h *Hub) handleHarnessPathProxy(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	localURL, entry, err := h.ensureHarnessProxyEntry(id)
	if err != nil {
		http.Error(w, "harness proxy unavailable", http.StatusBadGateway)
		return
	}
	target, err := url.Parse(localURL)
	if err != nil {
		http.Error(w, "invalid harness proxy", http.StatusBadGateway)
		return
	}
	jar, err := entry.jar(localURL, nil)
	if err != nil {
		http.Error(w, "harness auth unavailable", http.StatusBadGateway)
		return
	}
	prefix := "/harness-proxy/" + id
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == "" {
		rest = "/"
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	authenticated := &jarTransport{base: entry, jar: jar}
	proxy.Transport = authenticated
	if isHarnessEntranceGET(r, rest) {
		proxy.Transport = &harnessEntranceTransport{
			jarTransport: authenticated,
			refresh: func() (*jarTransport, error) {
				currentURL, currentEntry, err := h.ensureHarnessProxyEntry(id)
				if err != nil {
					return nil, err
				}
				var stale http.CookieJar
				if currentEntry == entry && currentURL == localURL {
					stale = jar
				}
				updatedJar, err := currentEntry.jar(currentURL, stale)
				if err != nil {
					return nil, err
				}
				return &jarTransport{base: currentEntry, jar: updatedJar}, nil
			},
		}
	}
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		req.URL.Path = rest
		req.URL.RawPath = ""
		// 保留 DSH 的原始 query（插件 bundle 使用 /plugins/??@... 形式），
		// 只移除 Hub 注入的 token，避免重新编码破坏路径语义。
		rawQuery := r.URL.RawQuery
		if rawQuery != "" {
			if values, parseErr := url.ParseQuery(rawQuery); parseErr == nil && values.Has("token") {
				values.Del("token")
				rawQuery = values.Encode()
			}
		}
		req.URL.RawQuery = rawQuery
		req.Host = target.Host
		// Hub 侧的 session/bearer 只用于 Hub 鉴权，不能泄露给 DSH。
		req.Header.Del("Cookie")
		req.Header.Del("Authorization")
	}
	proxy.ServeHTTP(w, r)
}
