//go:build !android

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const reviewConsentRoute = "/desktop/review-html-open"
const reviewConsentKey = "review_html_open"

type reviewConsent struct {
	AskBeforeOpen  bool `json:"ask_before_open"`
	ConsentGranted bool `json:"consent_granted"`
}

func (c reviewConsent) shouldAsk() bool { return c.AskBeforeOpen && !c.ConsentGranted }

var (
	desktopConfigMu      sync.Mutex
	reviewConsentEpoch   uint64 // Protected by desktopConfigMu.
	reviewConsentPrompt  sync.Mutex
	errReviewConsentBusy = errors.New("已有本机预览确认正在处理。")
)

func desktopConfigPath() string { return filepath.Join(configDir(), "config.json") }

// readReviewConsentLocked preserves fields this version of Meerkat may not know.
func readReviewConsentLocked() (map[string]json.RawMessage, reviewConsent, error) {
	var document map[string]json.RawMessage
	consent := reviewConsent{AskBeforeOpen: true}
	data, err := os.ReadFile(desktopConfigPath())
	if err != nil {
		return nil, consent, err
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, consent, err
	}
	if document == nil {
		return nil, consent, errors.New("无效的桌面配置")
	}
	if raw, exists := document[reviewConsentKey]; exists {
		if err := json.Unmarshal(raw, &consent); err != nil {
			return nil, consent, err
		}
	}
	return document, consent, nil
}

func writeReviewConsentLocked(document map[string]json.RawMessage, consent reviewConsent) error {
	raw, err := json.Marshal(consent)
	if err != nil {
		return err
	}
	document[reviewConsentKey] = raw
	return writeDesktopConfigLocked(document)
}

func writeDesktopConfig(config appConfig) error {
	desktopConfigMu.Lock()
	defer desktopConfigMu.Unlock()
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	if err := writeDesktopConfigLocked(config); err != nil {
		return err
	}
	reviewConsentEpoch++
	return nil
}

func writeDesktopConfigLocked(config any) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(configDir(), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(append(data, '\n'))
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), desktopConfigPath())
}

func handleReviewConsent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fail := func(status int, message string) { writeDesktopJSON(w, status, map[string]string{"message": message}) }
	origin := strings.TrimRight(localUIURL, "/")
	if origin == "" || "http://"+r.Host != origin {
		fail(http.StatusForbidden, "请在 Meerkat 桌面设置中操作。")
		return
	}
	var ask *bool
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if r.Header.Get("Origin") != origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" || r.Header.Get("Content-Type") != "application/json" {
			fail(http.StatusForbidden, "请在 Meerkat 桌面设置中操作。")
			return
		}
		var input struct {
			Ask *bool `json:"ask_before_open"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Ask == nil {
			fail(http.StatusBadRequest, "无法识别 HTML 打开设置。")
			return
		}
		ask = input.Ask
	default:
		w.Header().Set("Allow", "GET, POST")
		fail(http.StatusMethodNotAllowed, "不支持的设置操作。")
		return
	}
	desktopConfigMu.Lock()
	defer desktopConfigMu.Unlock()
	document, consent, err := readReviewConsentLocked()
	if err != nil {
		log.Printf("读取 HTML 打开设置: %v", err)
		fail(http.StatusInternalServerError, "无法读取本机的 HTML 打开设置。")
		return
	}
	if ask != nil {
		consent = reviewConsent{AskBeforeOpen: *ask}
		if err := writeReviewConsentLocked(document, consent); err != nil {
			log.Printf("保存 HTML 打开设置: %v", err)
			fail(http.StatusInternalServerError, "无法保存本机的 HTML 打开设置。")
			return
		}
		reviewConsentEpoch++
	}
	writeDesktopJSON(w, http.StatusOK, map[string]bool{"ask_before_open": consent.shouldAsk()})
}

func authorizeReviewOpen(ctx context.Context, message string) (bool, error) {
	return authorizeReviewOpenWith(ctx, message, showReviewOpenConfirmation)
}

func authorizeReviewOpenWith(ctx context.Context, message string, show func(context.Context, string) (bool, error)) (bool, error) {
	desktopConfigMu.Lock()
	_, consent, err := readReviewConsentLocked()
	desktopConfigMu.Unlock()
	if err != nil {
		return false, err
	}
	if !consent.shouldAsk() {
		return true, nil
	}
	if !reviewConsentPrompt.TryLock() {
		return false, errReviewConsentBusy
	}
	defer reviewConsentPrompt.Unlock()

	// Another request may have settled after our first read and before the lock.
	desktopConfigMu.Lock()
	_, consent, err = readReviewConsentLocked()
	epoch := reviewConsentEpoch
	desktopConfigMu.Unlock()
	if err != nil || !consent.shouldAsk() {
		return err == nil, err
	}
	allowed, err := show(ctx, message)
	if err != nil || !allowed {
		return false, err
	}
	desktopConfigMu.Lock()
	defer desktopConfigMu.Unlock()
	if epoch != reviewConsentEpoch {
		// A setting explicitly saved while the dialog was open takes precedence.
		return true, nil
	}
	document, consent, err := readReviewConsentLocked()
	if err != nil {
		return false, err
	}
	if consent.shouldAsk() {
		consent.ConsentGranted = true
		if err := writeReviewConsentLocked(document, consent); err != nil {
			return false, err
		}
		reviewConsentEpoch++
	}
	return true, nil
}
