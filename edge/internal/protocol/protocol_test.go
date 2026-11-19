package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCenterURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "plain", in: "http://100.64.0.1:8787", want: "http://100.64.0.1:8787"},
		{name: "trailing slash", in: " http://100.64.0.1:8787/ ", want: "http://100.64.0.1:8787"},
		{name: "https host", in: "https://center.example.com", want: "https://center.example.com"},
		{name: "base path", in: "https://example.com/meerkat/", want: "https://example.com/meerkat"},
		{name: "empty", in: "  ", wantErr: "中心地址为空"},
		{name: "no scheme", in: "100.64.0.1:8787", wantErr: "必须以 http:// 或 https:// 开头"},
		{name: "bad scheme", in: "ftp://example.com", wantErr: "必须以 http:// 或 https:// 开头"},
		{name: "no host", in: "http://", wantErr: "缺少主机名"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeCenterURL(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NormalizeCenterURL(%q) error = %v, want contains %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeCenterURL(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeCenterURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWSURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "http://100.64.0.1:8787", want: "ws://100.64.0.1:8787/api/edges/ws"},
		{in: "https://center.example.com", want: "wss://center.example.com/api/edges/ws"},
		{in: "http://127.0.0.1:8787/", want: "ws://127.0.0.1:8787/api/edges/ws"},
		{in: "https://example.com/meerkat/", want: "wss://example.com/meerkat/api/edges/ws"},
	}
	for _, tt := range tests {
		u, err := WSURL(tt.in)
		if err != nil {
			t.Fatalf("WSURL(%q) unexpected error: %v", tt.in, err)
		}
		if u.String() != tt.want {
			t.Fatalf("WSURL(%q) = %q, want %q", tt.in, u.String(), tt.want)
		}
	}
	if _, err := WSURL("://bad"); err == nil {
		t.Fatal("WSURL 对非法地址应返回错误")
	}
}

func TestRegisterAndHealthURL(t *testing.T) {
	u, err := RegisterURL("http://10.0.0.5:8787")
	if err != nil {
		t.Fatalf("RegisterURL error: %v", err)
	}
	if u.String() != "http://10.0.0.5:8787/api/edges/register" {
		t.Fatalf("RegisterURL = %q", u.String())
	}
	u, err = HealthURL("http://10.0.0.5:8787")
	if err != nil {
		t.Fatalf("HealthURL error: %v", err)
	}
	if u.String() != "http://10.0.0.5:8787/api/health" {
		t.Fatalf("HealthURL = %q", u.String())
	}
}

func TestFormatTime(t *testing.T) {
	ts := time.Date(2026, 11, 6, 5, 41, 0, 0, time.UTC)
	if got := FormatTime(ts); got != "2026-11-06T05:41:00.000Z" {
		t.Fatalf("FormatTime = %q", got)
	}
	parsed, err := ParseTime("2026-11-06T05:41:00.100Z")
	if err != nil {
		t.Fatalf("ParseTime error: %v", err)
	}
	if got := parsed.UTC().Format(time.RFC3339Nano); got != "2026-11-06T05:41:00.1Z" {
		t.Fatalf("ParseTime = %q", got)
	}
	if _, err := ParseTime("not-a-time"); err == nil {
		t.Fatal("ParseTime 对非法时间应返回错误")
	}
}

func TestMetricsMarshalsNulls(t *testing.T) {
	data, err := json.Marshal(Metrics{})
	if err != nil {
		t.Fatalf("marshal metrics: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal metrics: %v", err)
	}
	if len(got) != 9 {
		t.Fatalf("metrics 字段数 = %d, want 9: %s", len(got), data)
	}
	for name, v := range got {
		if v != nil {
			t.Fatalf("字段 %s = %v, want null", name, v)
		}
	}
}

func TestMetricsBatchMarshal(t *testing.T) {
	cpu, mem, total := 18.4, uint64(20401094656), uint64(34359738368)
	gpuUtil, gpuTemp := 93.0, 72.0
	batch := MetricsBatch{
		Type:      TypeMetricsBatch,
		StreamID:  "str_4f2a",
		Seq:       21,
		SampledAt: "2026-11-06T05:41:00.000Z",
		Metrics: Metrics{
			CPUPct:           &cpu,
			MemUsedBytes:     &mem,
			MemTotalBytes:    &total,
			GPUUtilPct:       &gpuUtil,
			GPUMemTotalBytes: nil,
			GPUTempC:         &gpuTemp,
		},
	}
	data, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal batch: %v", err)
	}
	if decoded["type"] != "metrics.batch" || decoded["stream_id"] != "str_4f2a" {
		t.Fatalf("batch 头字段错误: %s", data)
	}
	if decoded["seq"].(float64) != 21 {
		t.Fatalf("seq = %v", decoded["seq"])
	}
	metrics := decoded["metrics"].(map[string]any)
	if metrics["cpu_pct"].(float64) != 18.4 {
		t.Fatalf("cpu_pct = %v", metrics["cpu_pct"])
	}
	if metrics["gpu_mem_total_bytes"] != nil {
		t.Fatalf("gpu_mem_total_bytes 应为 null，实际 %v", metrics["gpu_mem_total_bytes"])
	}
	if strings.Contains(string(data), "device_secret") {
		t.Fatalf("metrics.batch 不应包含 secret: %s", data)
	}
}

func TestDecodeServerMessages(t *testing.T) {
	ack, err := DecodeServerMessage([]byte(`{"type":"hello.ack","protocol_version":"1","server_time":"2026-11-06T05:40:00.000Z","device_id":"dev_a1b2c3d4","heartbeat_interval_ms":10000,"metrics_interval_ms":5000}`))
	if err != nil {
		t.Fatalf("decode hello.ack: %v", err)
	}
	helloAck, ok := ack.(*HelloAck)
	if !ok {
		t.Fatalf("decode hello.ack 类型 = %T", ack)
	}
	if helloAck.HeartbeatIntervalMS != 10000 || helloAck.MetricsIntervalMS != 5000 || helloAck.DeviceID != "dev_a1b2c3d4" {
		t.Fatalf("hello.ack 字段错误: %+v", helloAck)
	}

	msg, err := DecodeServerMessage([]byte(`{"type":"metrics.ack","seq":21,"received_at":"2026-11-06T05:41:00.100Z"}`))
	if err != nil {
		t.Fatalf("decode metrics.ack: %v", err)
	}
	metricsAck, ok := msg.(*Ack)
	if !ok || metricsAck.Seq != 21 || metricsAck.Type != "metrics.ack" {
		t.Fatalf("metrics.ack 解码错误: %+v", msg)
	}

	msg, err = DecodeServerMessage([]byte(`{"type":"error","code":"auth_failed","message":"device_secret 无效"}`))
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	serverErr, ok := msg.(*Error)
	if !ok || serverErr.Code != "auth_failed" {
		t.Fatalf("error 解码错误: %+v", msg)
	}

	_, err = DecodeServerMessage([]byte(`{"type":"something.new","x":1}`))
	var unknown *UnknownTypeError
	if !errors.As(err, &unknown) || unknown.Type != "something.new" {
		t.Fatalf("未知类型应返回 UnknownTypeError，实际 %v", err)
	}

	if _, err := DecodeServerMessage([]byte("not json")); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
}

func TestHelloMarshal(t *testing.T) {
	hello := Hello{
		Type:            TypeHello,
		ProtocolVersion: Version,
		DeviceID:        "dev_a1b2c3d4",
		DeviceSecret:    "deadbeef",
		EdgeVersion:     "0.1.0",
		BootID:          "boot_1e2d3c4b",
		Capabilities:    []string{"metrics"},
	}
	data, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	want := `{"type":"hello","protocol_version":"1","device_id":"dev_a1b2c3d4","device_secret":"deadbeef","edge_version":"0.1.0","boot_id":"boot_1e2d3c4b","capabilities":["metrics"]}`
	if string(data) != want {
		t.Fatalf("hello JSON = %s, want %s", data, want)
	}
}

func TestDecodeHarnessProfileCommand(t *testing.T) {
	msg, err := DecodeServerMessage([]byte(`{"type":"harness.profile","request_id":"dsh_1","action":"connect","profile":"bot","mode":"connect"}`))
	if err != nil {
		t.Fatalf("decode harness.profile: %v", err)
	}
	command, ok := msg.(*HarnessProfileCommand)
	if !ok {
		t.Fatalf("decode harness.profile 类型 = %T", msg)
	}
	if command.Action != "connect" || command.Profile != "bot" || command.Mode != "connect" || command.RequestID != "dsh_1" {
		t.Fatalf("harness.profile 字段错误: %+v", command)
	}
}
