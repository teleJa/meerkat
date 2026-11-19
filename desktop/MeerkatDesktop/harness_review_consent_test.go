//go:build !android

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestReviewConsentSettings(t *testing.T) {
	withConfigDir(t)
	t.Setenv("APPDATA", t.TempDir())
	writeConfig(t, `{"mode":"node","center_url":"https://example.test","allow_lan":true,"future_field":{"keep":true}}`, false)
	origin := "http://127.0.0.1:1234"
	old := localUIURL
	localUIURL = origin + "/"
	t.Cleanup(func() { localUIURL = old })
	request := func(method, requestOrigin, payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+reviewConsentRoute, strings.NewReader(payload))
		r.Header.Set("Origin", requestOrigin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handleReviewConsent(w, r)
		return w
	}
	get := func(want bool) {
		t.Helper()
		w := request(http.MethodGet, "", "")
		var body reviewConsent
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.AskBeforeOpen != want {
			t.Fatalf("expected ask=%t, received HTTP %d %s", want, w.Code, w.Body)
		}
	}
	set := func(ask bool) {
		t.Helper()
		value, _ := json.Marshal(map[string]bool{"ask_before_open": ask})
		if w := request(http.MethodPost, origin, string(value)); w.Code != 200 {
			t.Fatalf("setting failed: HTTP %d %s", w.Code, w.Body)
		}
	}
	get(true)
	if w := request(http.MethodPost, "http://127.0.0.1:4321", `{"ask_before_open":false}`); w.Code != 403 {
		t.Fatalf("foreign iframe changed setting: HTTP %d", w.Code)
	}
	if w := request(http.MethodPost, origin, `{"consent_granted":true}`); w.Code != 400 {
		t.Fatalf("invalid payload accepted: HTTP %d", w.Code)
	}

	calls := 0
	approve := false
	authorize := func(want bool) {
		t.Helper()
		allowed, err := authorizeReviewOpenWith(context.Background(), "confirm", func(context.Context, string) (bool, error) {
			calls++
			return approve, nil
		})
		if err != nil || allowed != want {
			t.Fatalf("authorized=%t, want %t (error=%v)", allowed, want, err)
		}
	}
	authorize(false)
	get(true)
	approve = true
	authorize(true)
	get(false)
	authorize(true)
	if calls != 2 {
		t.Fatalf("prompt count=%d, want one cancel and one confirm", calls)
	}
	data, err := os.ReadFile(desktopConfigPath())
	if err != nil || !strings.Contains(string(data), `"consent_granted": true`) || !strings.Contains(string(data), `"future_field"`) || !strings.Contains(string(data), `"allow_lan": true`) {
		t.Fatalf("desktop config lost data: %s (%v)", data, err)
	}
	loadConfig()
	get(false)
	set(true)
	get(true)
	approve = false
	authorize(false)
	set(false)
	get(false)
	authorize(true)
	if calls != 3 {
		t.Fatalf("restoring ask or disabling ask was ignored: %d prompts", calls)
	}

	set(true)
	allowed, err := authorizeReviewOpenWith(context.Background(), "confirm", func(ctx context.Context, message string) (bool, error) {
		_, nestedErr := authorizeReviewOpenWith(ctx, message, func(context.Context, string) (bool, error) {
			t.Fatal("overlapping native confirmation")
			return false, nil
		})
		if !errors.Is(nestedErr, errReviewConsentBusy) {
			t.Fatalf("unexpected concurrent confirmation outcome: %v", nestedErr)
		}
		set(true)
		return true, nil
	})
	if !allowed || err != nil {
		t.Fatalf("current explicit confirmation failed: %t %v", allowed, err)
	}
	get(true)
}
