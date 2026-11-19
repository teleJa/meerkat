//go:build !android

package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func TestFileProxyWriterOptionalInterfaces(t *testing.T) {
	recorder := httptest.NewRecorder()
	plain := (&fileProxyWriter{ResponseWriter: recorder}).withOptionalInterfaces()
	if _, ok := plain.(http.Flusher); !ok {
		t.Fatal("lost Flusher")
	}
	if _, ok := plain.(http.Hijacker); ok {
		t.Fatal("advertised an unsupported Hijacker")
	}
	plain.(http.Flusher).Flush()
	if !recorder.Flushed {
		t.Fatal("Flush did not reach original writer")
	}

	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	original := &fileProxyTestWriter{
		ResponseRecorder: httptest.NewRecorder(), conn: client,
		buffer: bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)),
	}
	writer := &fileProxyWriter{ResponseWriter: original}
	wrapped := writer.withOptionalInterfaces()
	if _, ok := wrapped.(http.Flusher); !ok {
		t.Fatal("lost Flusher on a writer that also supports Hijacker")
	}
	hijacker, ok := wrapped.(http.Hijacker)
	if !ok {
		t.Fatal("lost Hijacker")
	}
	conn, buffer, err := hijacker.Hijack()
	if conn != client || buffer != original.buffer || err != nil {
		t.Fatalf("Hijack did not preserve delegation: %v, %v, %v", conn, buffer, err)
	}
	wrapped.WriteHeader(http.StatusPartialContent)
	n, err := wrapped.Write([]byte("short write"))
	if n != original.Body.Len() || int64(n) != writer.bytesWritten || err != io.ErrShortWrite || !writer.writeFailed.Load() || writer.status != http.StatusPartialContent {
		t.Fatalf("wrote %d bytes, counted %d, status %d, error %v", n, writer.bytesWritten, writer.status, err)
	}
}

type fileProxyTestWriter struct {
	*httptest.ResponseRecorder
	conn   net.Conn
	buffer *bufio.ReadWriter
}

func (w *fileProxyTestWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, w.buffer, nil
}

func (w *fileProxyTestWriter) Write(body []byte) (int, error) {
	n, _ := w.ResponseRecorder.Write(body[:3])
	return n, io.ErrShortWrite
}

func TestFileProxyDiagnosticRecordsCompleteAndTruncatedBodies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/form-data; boundary=test")
		if r.URL.Path == "/api/workspaceFiles/readBytes" {
			w.Header().Set("Content-Length", "20")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "payload")
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	records := make(chan fileProxyDiagnostic, 2)
	server := httptest.NewServer(logFileProxyRequests(httputil.NewSingleHostReverseProxy(target), "test", func(entry fileProxyDiagnostic) {
		records <- entry
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/file?query=excluded", strings.NewReader("request"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "payload" {
		t.Fatalf("unexpected response body %q: %v", body, err)
	}
	entry := <-records
	if !entry.BodyComplete || entry.Outcome != "complete" || entry.Path != "/api/file" || entry.Method != http.MethodPost || entry.RequestContentLength != 7 || entry.AcceptEncoding != "gzip" || entry.Status != http.StatusOK || entry.BytesWritten != int64(len(body)) || entry.ContentType != "multipart/form-data; boundary=test" || entry.UpstreamTransferEncoding != "chunked" {
		t.Fatalf("unexpected complete diagnostic: %+v", entry)
	}

	// A truncated upstream must not look like a complete write even if the
	// downstream server buffered every Write and aborted before sending headers.
	response, err = server.Client().Get(server.URL + "/api/workspaceFiles/readBytes")
	if err == nil {
		_, err = io.ReadAll(response.Body)
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("client did not observe the truncated response")
	}
	entry = <-records
	if entry.BodyComplete || entry.Outcome != "proxy_interrupted" || entry.UpstreamContentLength == nil || *entry.UpstreamContentLength != 20 || entry.BytesWritten != 7 {
		t.Fatalf("unexpected interrupted diagnostic: %+v", entry)
	}
}

func TestFileProxyDiagnosticPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/file": true, "/api/workspaceFiles/readBytes": true,
		"/harness-proxy/dev_1/api/file": true, "/harness-proxy/dev_1/api/workspaceFiles/readBytes": true,
		"/api/remote.mux": false, "/harness-proxy/dev_1/api/remote.mux": false,
		"/plugins/events": false, "/harness-proxy/dev_1/plugins/events": false,
		"/api/files": false, "/harness-proxy//api/file": false,
	} {
		if got := isFileProxyDiagnosticPath(path); got != want {
			t.Errorf("isFileProxyDiagnosticPath(%q) = %t, want %t", path, got, want)
		}
	}
}
