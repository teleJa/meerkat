//go:build windows

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var updateReadyOnce sync.Once

func initializeUpdate() {
	if strings.TrimSpace(appCfg.UpdateManifestURL) == "" {
		appCfg.UpdateManifestURL = "https://github.com/teleJa/meerkat/releases/latest/download/latest.json"
	}
	loadWindowsUpdateSettings()
}

func updateKey() ed25519.PublicKey {
	key, _ := hex.DecodeString(updatePublicKey)
	return ed25519.PublicKey(key)
}

func updateDir() string { return filepath.Join(configDir(), "updates") }

func automaticUpdateSetting() string { return filepath.Join(updateDir(), "automatic-disabled") }

func failedUpdateVersion() string {
	data, _ := os.ReadFile(filepath.Join(updateDir(), "failed-version"))
	return string(data)
}

func noTrackedUpdateWork() bool {
	if firstRunPending() {
		return true
	}
	deviceID := localDeviceID()
	if localUIURL == "" || deviceID == "" {
		return false
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout: 3 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	taskDevice := ""
	if appCfg.Mode == "node" {
		taskDevice = deviceID
	}
	return hubTasksIdle(client, localUIURL, taskDevice) && localDSHIdle(client, localUIURL, deviceID)
}

func hubTasksIdle(client *http.Client, base, deviceID string) bool {
	var result struct {
		Tasks []struct {
			Status   string `json:"status"`
			DeviceID string `json:"device_id"`
		} `json:"tasks"`
		ActiveFilterApplied bool `json:"active_filter_applied"`
	}
	if !readUpdateStatus(client, base+"api/tasks?limit=500&active=1", "", &result) || result.Tasks == nil {
		return false
	}
	// A full page can hide an older active task on this device, including on
	// older Hubs which return the newest 500 tasks without the active filter.
	if len(result.Tasks) >= 500 {
		return false
	}
	for _, task := range result.Tasks {
		if deviceID != "" && task.DeviceID != "" && task.DeviceID != deviceID {
			continue
		}
		if result.ActiveFilterApplied || task.Status == "running" || task.Status == "queued" || task.Status == "cancel_requested" || task.Status == "" {
			return false
		}
	}
	return true
}

func localDSHIdle(client *http.Client, base, deviceID string) bool {
	id := url.PathEscape(deviceID)
	var status struct {
		Online bool `json:"online"`
		DSH    struct {
			Status string `json:"status"`
			HasURL bool   `json:"has_url"`
		} `json:"dsh"`
	}
	if !readUpdateStatus(client, base+"api/devices/"+id+"/dsh", "", &status) || !status.Online {
		return false
	}
	if status.DSH.Status == "stopped" && !status.DSH.HasURL {
		return true
	}
	if status.DSH.Status != "running" || !status.DSH.HasURL {
		return false
	}
	var sessions struct {
		Result struct {
			OK    bool `json:"ok"`
			Value struct {
				Items []struct{ Running bool } `json:"items"`
			} `json:"value"`
		} `json:"result"`
	}
	body := `{"type":"client-request","rpcId":"meerkat-update-idle","method":"session/list","payload":{"args":{"_request":{}}}}`
	if !readUpdateStatus(client, base+"harness-proxy/"+id+"/api/session/list", body, &sessions) || !sessions.Result.OK || sessions.Result.Value.Items == nil {
		return false
	}
	for _, session := range sessions.Result.Value.Items {
		if session.Running {
			return false
		}
	}
	return true
}

func readUpdateStatus(client *http.Client, target, body string, out any) bool {
	var response *http.Response
	var err error
	if body == "" {
		response, err = client.Get(target)
	} else {
		response, err = client.Post(target, "application/json", strings.NewReader(body))
	}
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(out) == nil
}

func writeUpdateReady() {
	if len(os.Args) != 3 || os.Args[1] != "--update-ready" {
		return
	}
	staging, err := filepath.Abs(os.Args[2])
	if err != nil || !validUpdateStaging(staging) {
		return
	}
	updateReadyOnce.Do(func() {
		data, err := json.Marshal(readyUpdate{PID: os.Getpid(), Version: appVersion})
		if err != nil {
			return
		}
		client := &http.Client{Timeout: 2 * time.Second}
		deadline := time.Now().Add(50 * time.Second)
		for time.Now().Before(deadline) {
			if updateServicesReady(client) {
				if err := os.WriteFile(filepath.Join(staging, "ready.json"), data, 0o600); err == nil {
					return
				}
			}
			time.Sleep(time.Second)
		}
		log.Print("更新启动后，本机界面或服务未就绪")
	})
}

func updateServicesReady(client *http.Client) bool {
	route := localUIURL
	if firstRunPending() {
		route += "welcome"
	} else {
		if edgeDone == nil {
			return false
		}
		select {
		case <-edgeDone:
			return false
		default:
		}
		var health struct{ OK bool }
		if !readUpdateStatus(client, localUIURL+"api/health", "", &health) || !health.OK {
			return false
		}
	}
	response, err := client.Get(route)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode == http.StatusOK
}
