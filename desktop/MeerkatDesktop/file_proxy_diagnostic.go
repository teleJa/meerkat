//go:build !android

package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const fileProxyDiagnosticName = "workspace-file-proxy.log"

type fileProxyDiagnosticKey struct{}

type fileProxyDiagnostic struct {
	StartedAt                time.Time `json:"started_at"`
	DurationMS               int64     `json:"duration_ms"`
	Layer                    string    `json:"layer"`
	Method                   string    `json:"method"`
	Path                     string    `json:"path"`
	AcceptEncoding           string    `json:"accept_encoding"`
	RequestContentLength     int64     `json:"request_content_length"`
	Status                   int       `json:"status"`
	BytesWritten             int64     `json:"bytes_written"`
	ContentType              string    `json:"content_type"`
	ContentEncoding          string    `json:"content_encoding"`
	ContentLength            string    `json:"content_length"`
	TransferEncoding         string    `json:"transfer_encoding"`
	UpstreamStatus           int       `json:"upstream_status"`
	UpstreamContentLength    *int64    `json:"upstream_content_length"`
	UpstreamTransferEncoding string    `json:"upstream_transfer_encoding"`
	UpstreamContentEncoding  string    `json:"upstream_content_encoding"`
	UpstreamUncompressed     bool      `json:"upstream_uncompressed"`
	BodyComplete             bool      `json:"body_complete"`
	Outcome                  string    `json:"outcome"`
}

func isFileProxyDiagnosticPath(path string) bool {
	if rest, ok := strings.CutPrefix(path, "/harness-proxy/"); ok {
		device, suffix, found := strings.Cut(rest, "/")
		if !found || device == "" {
			return false
		}
		path = "/" + suffix
	}
	if path == "/api/file" {
		return true
	}
	method, ok := strings.CutPrefix(path, "/api/workspaceFiles/")
	return ok && method != "" && !strings.Contains(method, "/")
}

// Observe the upstream framing separately. net/http may add downstream framing
// after ServeHTTP returns, so downstream headers are reported as set by the handler.
func logFileProxyRequests(proxy *httputil.ReverseProxy, layer string, record func(fileProxyDiagnostic)) http.Handler {
	modifyResponse := proxy.ModifyResponse
	proxy.ModifyResponse = func(resp *http.Response) error {
		if entry, ok := resp.Request.Context().Value(fileProxyDiagnosticKey{}).(*fileProxyDiagnostic); ok {
			entry.UpstreamStatus = resp.StatusCode
			length := resp.ContentLength
			entry.UpstreamContentLength = &length
			entry.UpstreamTransferEncoding = strings.Join(resp.TransferEncoding, ", ")
			entry.UpstreamContentEncoding = resp.Header.Get("Content-Encoding")
			entry.UpstreamUncompressed = resp.Uncompressed
		}
		if modifyResponse != nil {
			return modifyResponse(resp)
		}
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isFileProxyDiagnosticPath(r.URL.Path) {
			proxy.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		entry := fileProxyDiagnostic{
			StartedAt: start.UTC(), Layer: layer, Method: r.Method, Path: r.URL.EscapedPath(),
			AcceptEncoding: strings.Join(r.Header.Values("Accept-Encoding"), ", "), RequestContentLength: r.ContentLength,
		}
		writer := &fileProxyWriter{ResponseWriter: w}
		returned := false
		defer func() {
			entry.DurationMS = time.Since(start).Milliseconds()
			entry.Status = writer.status
			entry.BytesWritten = writer.bytesWritten
			entry.ContentType = w.Header().Get("Content-Type")
			entry.ContentEncoding = w.Header().Get("Content-Encoding")
			entry.ContentLength = w.Header().Get("Content-Length")
			entry.TransferEncoding = w.Header().Get("Transfer-Encoding")
			switch {
			case r.Context().Err() != nil:
				entry.Outcome = "request_context_canceled"
			case writer.writeFailed.Load():
				entry.Outcome = "downstream_write_error"
			case !returned:
				entry.Outcome = "proxy_interrupted"
			case entry.UpstreamStatus == 0:
				entry.Outcome = "upstream_response_unavailable"
			default:
				entry.Outcome = "complete"
				entry.BodyComplete = true
			}
			record(entry)
		}()
		proxy.ServeHTTP(writer.withOptionalInterfaces(), r.WithContext(context.WithValue(r.Context(), fileProxyDiagnosticKey{}, &entry)))
		returned = true
	})
}

func appendFileProxyDiagnostic(entry fileProxyDiagnostic) {
	dir := configDir()
	if dir == "" {
		return
	}
	if err := writeFileProxyDiagnostic(filepath.Join(dir, fileProxyDiagnosticName), entry); err != nil {
		log.Printf("Workspace file proxy diagnostic: %v", err)
	}
}

func writeFileProxyDiagnostic(path string, entry fileProxyDiagnostic) error {
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}

type fileProxyWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	writeFailed  atomic.Bool
}

func (w *fileProxyWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *fileProxyWriter) WriteHeader(status int) {
	if w.status == 0 && (status >= 200 || status == http.StatusSwitchingProtocols) {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *fileProxyWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytesWritten += int64(n)
	if err != nil || n != len(body) {
		w.writeFailed.Store(true)
	}
	return n, err
}

type fileProxyFlusher struct{ *fileProxyWriter }

func (w fileProxyFlusher) Flush() { _ = w.FlushError() }

func (w fileProxyFlusher) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err != nil {
		w.writeFailed.Store(true)
	}
	return err
}

func (w *fileProxyWriter) withOptionalInterfaces() http.ResponseWriter {
	_, canFlush := w.ResponseWriter.(http.Flusher)
	hijacker, canHijack := w.ResponseWriter.(http.Hijacker)
	switch {
	case canFlush && canHijack:
		return struct {
			fileProxyFlusher
			http.Hijacker
		}{fileProxyFlusher{w}, hijacker}
	case canFlush:
		return fileProxyFlusher{w}
	case canHijack:
		return struct {
			*fileProxyWriter
			http.Hijacker
		}{w, hijacker}
	default:
		return w
	}
}
