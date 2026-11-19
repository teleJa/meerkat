// Package center 封装 Edge 侧使用的中心 REST 接口：设备注册与健康检查。
//
// 只处理 HTTP 与可读错误，不打印任何密钥。
package center

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"meerkat/edge/internal/protocol"
)

const maxResponseBytes = 64 << 10

// Client 是中心 REST 客户端。
type Client struct {
	http *http.Client
}

// New 创建客户端；timeout <= 0 时使用 15s。
func New(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{http: &http.Client{Timeout: timeout}}
}

// NewWithDialer 创建客户端，并让所有请求经 dial 建立连接（例如 Edge 的内嵌 tsnet）。
// dial 为 nil 时等价于 New；启用自定义 dial 时同时禁用系统代理，
// 避免 HTTP(S)_PROXY 拦截 tailnet 地址。
func NewWithDialer(timeout time.Duration, dial func(ctx context.Context, network, address string) (net.Conn, error)) *Client {
	client := New(timeout)
	if dial == nil {
		return client
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dial
	client.http.Transport = transport
	return client
}

// APIError 表示中心返回的非 2xx 响应。
type APIError struct {
	Method     string
	URL        string
	StatusCode int
	Code       string
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	hint := errorHint(e.Code)
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("%s %s 返回 %d（%s）：%s%s", e.Method, e.URL, e.StatusCode, e.Code, e.Message, hint)
	case e.Code != "":
		return fmt.Sprintf("%s %s 返回 %d（%s）%s", e.Method, e.URL, e.StatusCode, e.Code, hint)
	case e.Body != "":
		return fmt.Sprintf("%s %s 返回 %d：%s", e.Method, e.URL, e.StatusCode, e.Body)
	default:
		return fmt.Sprintf("%s %s 返回 %d", e.Method, e.URL, e.StatusCode)
	}
}

// errorHint 给常见错误码补一句可操作的中文提示。
func errorHint(code string) string {
	switch code {
	case "pairing_token_invalid":
		return "；配对令牌无效、已过期或已被使用，请在中心重新生成"
	case "device_conflict":
		return "；同一 boot_id 已注册，需要中心撤销旧凭据或重新生成配对令牌"
	case "unsupported_protocol":
		return "；中心不支持当前协议版本，请升级 Edge"
	case "invalid_device":
		return "；设备信息不完整，请检查 --name/--role"
	case "auth_failed":
		return "；设备凭据无效或已被撤销，请重新配对"
	default:
		return ""
	}
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func newAPIError(method, url string, resp *http.Response, data []byte) *APIError {
	apiErr := &APIError{
		Method:     method,
		URL:        url,
		StatusCode: resp.StatusCode,
		Body:       strings.TrimSpace(string(data)),
	}
	var eb errorBody
	if err := json.Unmarshal(data, &eb); err == nil {
		apiErr.Code = eb.Code
		apiErr.Message = eb.Message
		if apiErr.Body == "" || (eb.Code != "" && eb.Message != "") {
			apiErr.Body = ""
		}
	}
	return apiErr
}

// Register 用一次性配对令牌注册设备。
func (c *Client) Register(ctx context.Context, centerURL string, req protocol.RegisterRequest) (*protocol.RegisterResponse, error) {
	u, err := protocol.RegisterURL(centerURL)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化注册请求失败: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造注册请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求中心 %s 失败: %w", u.String(), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("读取中心响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, newAPIError(http.MethodPost, u.String(), resp, data)
	}
	var out protocol.RegisterResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("中心返回的注册响应无法解析: %w", err)
	}
	if strings.TrimSpace(out.DeviceID) == "" || strings.TrimSpace(out.DeviceSecret) == "" {
		return nil, fmt.Errorf("中心返回的注册响应缺少 device_id 或 device_secret")
	}
	return &out, nil
}

// Health 是 GET /api/health 的响应。
type Health struct {
	OK            bool   `json:"ok"`
	Version       string `json:"version"`
	Time          string `json:"time"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// Health 调用中心公开健康检查接口。
func (c *Client) Health(ctx context.Context, centerURL string) (*Health, error) {
	u, err := protocol.HealthURL(centerURL)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("构造健康检查请求失败: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求中心 %s 失败: %w", u.String(), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("读取中心响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, newAPIError(http.MethodGet, u.String(), resp, data)
	}
	var out Health
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("中心健康检查响应无法解析: %w", err)
	}
	return &out, nil
}
