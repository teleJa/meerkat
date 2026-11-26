package tsnetedge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func testLogger(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(io.Discard, "", 0)
}

func readStatusFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读状态文件失败: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("状态文件不是合法 JSON: %v", err)
	}
	return value
}

// 本地代理起不来时，状态文件必须带 local_api_error，UI 才能显示具体原因
// （「端口被系统保留」），而不是笼统的「尚未就绪」。
func TestFailLocalAPIRecordsReason(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "tsnet-status.json")
	service := &Service{status: statusPath, state: "running", log: testLogger(t)}

	err := service.failLocalAPI(errors.New("51900-51999 共 100 个端口全部被占用或被系统保留"))
	if err == nil {
		t.Fatalf("failLocalAPI 应把错误返回给调用方")
	}
	status := readStatusFile(t, statusPath)
	if value, ok := status["local_api_url"]; ok && value != "" {
		t.Fatalf("失败时 local_api_url 应为空，实际 %v", value)
	}
	reason, _ := status["local_api_error"].(string)
	if reason == "" {
		t.Fatalf("状态里缺少 local_api_error")
	}
	if !contains(reason, "全部被占用") {
		t.Fatalf("local_api_error 应说明端口不可用：%q", reason)
	}
}

// 后续的状态刷新（setState）不能把已记录的失败原因和已绑定的地址丢掉。
func TestSetStateKeepsLocalAPIInformation(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "tsnet-status.json")
	service := &Service{status: statusPath, log: testLogger(t)}
	service.mu.Lock()
	service.apiURL = "http://127.0.0.1:51901"
	service.mu.Unlock()

	service.setState("running", "")
	status := readStatusFile(t, statusPath)
	if status["local_api_url"] != "http://127.0.0.1:51901" {
		t.Fatalf("local_api_url 被状态刷新丢掉了：%v", status["local_api_url"])
	}

	// 换成失败状态：地址清空、原因写入。
	service.mu.Lock()
	service.apiURL = ""
	service.apiErr = "监听失败"
	service.mu.Unlock()
	service.setState("running", "")
	status = readStatusFile(t, statusPath)
	if status["local_api_error"] != "监听失败" {
		t.Fatalf("local_api_error 未写入：%v", status["local_api_error"])
	}
}

func TestLocalAPIStartsOnceAfterLateLogin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &Service{hubURL: "http://100.64.0.1:7443", status: filepath.Join(t.TempDir(), "status.json"), log: testLogger(t)}
	service.startLocalAPIWhenReady(ctx, "NeedsLogin", false)
	service.startLocalAPIWhenReady(ctx, "Running", false)
	if service.apiURL != "" {
		t.Fatal("proxy started before login and address assignment")
	}
	service.startLocalAPIWhenReady(ctx, "Running", true)
	first := service.apiURL
	service.startLocalAPIWhenReady(ctx, "Running", true)
	if first == "" || service.apiURL != first {
		t.Fatalf("proxy URL after login %q, after refresh %q", first, service.apiURL)
	}
}

func contains(value, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
