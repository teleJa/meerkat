//go:build !android

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateSettingsRequiresDesktopCapability(t *testing.T) {
	const base = "http://127.0.0.1:52829/"
	const token = "desktop-test-capability"
	cases := []struct {
		name string
		edit func(*http.Request)
	}{
		{"desktop main window", nil},
		{"browser with same origin", func(r *http.Request) { r.Header.Del("X-Meerkat-Update-Token") }},
		{"wrong capability", func(r *http.Request) { r.Header.Set("X-Meerkat-Update-Token", "stale") }},
		{"HTML preview", func(r *http.Request) { r.Header.Set("Origin", "http://localhost:52900") }},
		{"DSH frame", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
		{"other authority", func(r *http.Request) { r.Host = "localhost:52829" }},
		{"form navigation", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }},
		{"plain body", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"cross-origin preflight", func(r *http.Request) { r.Method = http.MethodOptions }},
		{"GET", func(r *http.Request) { r.Method = http.MethodGet }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, base+strings.TrimPrefix(updateSettingsRoute, "/"), nil)
			r.Header.Set("Origin", strings.TrimRight(base, "/"))
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
			r.Header.Set("X-Meerkat-Update-Token", token)
			if tc.edit != nil {
				tc.edit(r)
			}
			if got := authorizedUpdateSettingsRequest(r, base, token); got != (tc.edit == nil) {
				t.Fatalf("authorized = %t", got)
			}
		})
	}
}
