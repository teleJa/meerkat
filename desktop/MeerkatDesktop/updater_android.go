//go:build android && cgo

package main

// #include <stdlib.h>
import "C"

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	"meerkat/desktop/internal/update"
)

// The test bootstrap overrides these at link time. Release builds use the public feed.
var androidUpdateURL = "https://github.com/teleJa/meerkat/releases/latest/download/latest.json"
var androidUpdateKey string
var downloadBytes atomic.Int64
var downloadLock sync.Mutex
var downloadContext context.Context
var stopDownload context.CancelFunc

//export AndroidDownloadControl
func AndroidDownloadControl(start C.int) {
	downloadLock.Lock()
	defer downloadLock.Unlock()
	if stopDownload != nil {
		stopDownload()
	}
	if start != 0 {
		downloadBytes.Store(0)
		downloadContext, stopDownload = context.WithCancel(context.Background())
	}
}

//export AndroidDownloaded
func AndroidDownloaded() C.longlong { return C.longlong(downloadBytes.Load()) }

type mobileUpdateResult struct {
	Available bool            `json:"available"`
	Version   string          `json:"version,omitempty"`
	Notes     string          `json:"notes,omitempty"`
	Package   update.Platform `json:"pkg"`
	Envelope  json.RawMessage `json:"envelope,omitempty"`
}

func runMobileUpdate(action, dir, envelope string, current int64, sdk int) (mobileUpdateResult, error) {
	var result mobileUpdateResult
	key, err := hex.DecodeString(androidUpdateKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return result, fmt.Errorf("此构建未配置更新公钥")
	}
	var m update.Manifest
	if action == "check" {
		var data []byte
		data, m, err = update.Fetch(androidUpdateURL, key)
		result.Envelope = data
	} else {
		m, err = update.Parse([]byte(envelope), key)
	}
	if err != nil {
		return result, err
	}
	p, available, err := m.AndroidPackage(current, sdk)
	if err != nil {
		return result, err
	}
	result.Package = p
	result.Available = available
	result.Version = m.Version
	result.Notes = m.Notes
	if !available || action == "check" || action == "restore" {
		return result, nil
	}
	path := filepath.Join(dir, fmt.Sprintf("meerkat-%d.apk", p.VersionCode))
	switch action {
	case "verify":
		err = update.VerifyFile(path, p.SHA256)
	case "download":
		downloadLock.Lock()
		ctx := downloadContext
		downloadLock.Unlock()
		if ctx == nil {
			return result, fmt.Errorf("下载尚未开始")
		}
		err = update.DownloadTo(ctx, p, path, func(n, _ int64) { downloadBytes.Store(n) })
	default:
		err = fmt.Errorf("无效的更新操作")
	}
	return result, err
}

//export AndroidUpdate
func AndroidUpdate(action, dir, envelope *C.char, current C.longlong, sdk C.int) *C.char {
	result, err := runMobileUpdate(C.GoString(action), C.GoString(dir), C.GoString(envelope), int64(current), int(sdk))
	var data []byte
	if err != nil {
		data, _ = json.Marshal(map[string]string{"error": err.Error()})
	} else {
		data, _ = json.Marshal(result)
	}
	return C.CString(string(data))
}
