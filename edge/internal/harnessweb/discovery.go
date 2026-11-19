// Package harnessweb 启动本机 DSH Web、解析访问 URL，并维护每个 profile 的发现文件。
package harnessweb

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DiscoveryDir 是发现文件相对 $DSH_HOME 的目录。
const DiscoveryDir = "runtime"

// Discovery 描述一个正在运行的 DSH Web 实例。
//
// 写入方是 Edge 自己（启动实例后）或外部 launcher（例如 launchd 脚本）；文件
// 权限必须是 0600，因为 URL 里带有 DSH 的 token。
type Discovery struct {
	Profile   string `json:"profile"`
	URL       string `json:"url"`
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	StartedAt string `json:"started_at"`
}

// DSHHome 返回 DSH 的数据目录：优先环境变量 DSH_HOME，否则 ~/.dsh。
func DSHHome() string {
	if value := strings.TrimSpace(os.Getenv("DSH_HOME")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".dsh")
	}
	return filepath.Join(home, ".dsh")
}

// DiscoveryPath 返回某个 profile 的发现文件路径。
func DiscoveryPath(home, profile string) string {
	return filepath.Join(home, DiscoveryDir, "web-"+profile+".json")
}

// Validate 校验发现文件内容是否足以用于连接。
func (d Discovery) Validate() error {
	if strings.TrimSpace(d.Profile) == "" {
		return fmt.Errorf("发现文件缺少 profile")
	}
	parsed, err := url.Parse(strings.TrimSpace(d.URL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("发现文件 URL 必须是 http/https 地址")
	}
	if d.PID < 0 {
		return fmt.Errorf("发现文件 pid 非法")
	}
	if d.Port < 0 || d.Port > 65535 {
		return fmt.Errorf("发现文件 port 非法")
	}
	return nil
}

// ReadDiscovery 读取并校验 profile 的发现文件。
func ReadDiscovery(home, profile string) (Discovery, error) {
	if err := safeProfilePath(profile); err != nil {
		return Discovery{}, err
	}
	data, err := os.ReadFile(DiscoveryPath(home, profile))
	if err != nil {
		return Discovery{}, err
	}
	var discovery Discovery
	if err := json.Unmarshal(data, &discovery); err != nil {
		return Discovery{}, fmt.Errorf("发现文件无法解析: %w", err)
	}
	if discovery.Profile == "" {
		discovery.Profile = profile
	}
	if discovery.Profile != profile {
		return Discovery{}, fmt.Errorf("发现文件的 profile %q 与 %q 不一致", discovery.Profile, profile)
	}
	if discovery.Port == 0 {
		if parsed, parseErr := url.Parse(discovery.URL); parseErr == nil {
			if port, convErr := strconv.Atoi(parsed.Port()); convErr == nil {
				discovery.Port = port
			}
		}
	}
	if err := discovery.Validate(); err != nil {
		return Discovery{}, err
	}
	return discovery, nil
}

// WriteDiscovery 原子写入发现文件；目录与文件权限分别是 0700 和 0600。
func WriteDiscovery(home string, discovery Discovery) error {
	if strings.TrimSpace(discovery.StartedAt) == "" {
		discovery.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := discovery.Validate(); err != nil {
		return err
	}
	if err := safeProfilePath(discovery.Profile); err != nil {
		return err
	}
	path := DiscoveryPath(home, discovery.Profile)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建发现文件目录失败: %w", err)
	}
	data, err := json.MarshalIndent(discovery, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".web-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := func() { _ = os.Remove(tempName) }
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		cleanup()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// safeProfilePath 阻止 profile 名跳出 $DSH_HOME/runtime。
func safeProfilePath(profile string) error {
	if strings.TrimSpace(profile) == "" || strings.ContainsAny(profile, `/\`) || strings.Contains(profile, "..") {
		return fmt.Errorf("profile 名不能包含路径分隔符或 ..")
	}
	return nil
}

// RemoveDiscovery 删除发现文件；文件不存在不算错误。
func RemoveDiscovery(home, profile string) error {
	err := os.Remove(DiscoveryPath(home, profile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Alive 判断发现文件对应的实例是否还在：pid 存活，或端口仍可连接。
func (d Discovery) Alive() bool {
	if d.PID > 0 && pidAlive(d.PID) {
		return true
	}
	port := d.Port
	if port == 0 {
		if parsed, err := url.Parse(d.URL); err == nil {
			if parsed.Port() != "" {
				port, _ = strconv.Atoi(parsed.Port())
			}
		}
	}
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
