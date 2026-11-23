package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/mod/semver"
)

const (
	MaxManifest = 64 << 10
	MaxPackage  = 512 << 20
)

type Platform struct {
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	VersionCode int64  `json:"versionCode,omitempty"`
	MinSDK      int    `json:"minSdk,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type Manifest struct {
	Version   string              `json:"version"`
	Notes     string              `json:"notes,omitempty"`
	Platforms map[string]Platform `json:"platforms"`
}

// The signature covers the exact manifest bytes, independent of JSON formatting.
type Envelope struct {
	Manifest  []byte `json:"manifest"`
	Signature []byte `json:"signature"`
}

func VersionValid(version string) bool {
	return semver.IsValid("v"+version) && semver.Canonical("v"+version) == "v"+version
}

func Newer(latest, current string) bool {
	return VersionValid(latest) && VersionValid(current) && semver.Compare("v"+latest, "v"+current) > 0
}

func Parse(data []byte, publicKey ed25519.PublicKey) (Manifest, error) {
	var envelope Envelope
	if len(data) > MaxManifest || json.Unmarshal(data, &envelope) != nil {
		return Manifest{}, fmt.Errorf("更新清单格式无效")
	}
	if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, envelope.Manifest, envelope.Signature) {
		return Manifest{}, fmt.Errorf("更新清单签名验证失败")
	}
	var manifest Manifest
	if json.Unmarshal(envelope.Manifest, &manifest) != nil || !VersionValid(manifest.Version) {
		return Manifest{}, fmt.Errorf("更新清单版本无效")
	}
	return manifest, nil
}

func (m Manifest) Package(goos, goarch string) (Platform, error) {
	platform := m.Platforms[goos+"-"+goarch]
	if platform.URL == "" {
		return Platform{}, fmt.Errorf("更新清单没有当前平台的安装包")
	}
	if _, err := parseURL(platform.URL); err != nil {
		return Platform{}, err
	}
	if _, err := parseHash(platform.SHA256); err != nil {
		return Platform{}, err
	}
	return platform, nil
}

func (m Manifest) AndroidPackage(current int64, sdk int) (Platform, bool, error) {
	if _, exists := m.Platforms["android-arm64"]; !exists {
		return Platform{}, false, nil
	}
	p, err := m.Package("android", "arm64")
	if err != nil {
		return Platform{}, false, err
	}
	if p.VersionCode <= 0 || p.MinSDK <= 0 || p.Size <= 0 || p.Size > MaxPackage {
		return Platform{}, false, fmt.Errorf("Android 安装包元信息无效")
	}
	return p, p.VersionCode > current && p.MinSDK <= sdk, nil
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && u.Hostname() == "127.0.0.1")) {
		return nil, fmt.Errorf("更新地址必须使用 HTTPS（本机 127.0.0.1 测试除外）")
	}
	return u, nil
}

func parseHash(value string) ([sha256.Size]byte, error) {
	var hash [sha256.Size]byte
	if len(value) != sha256.Size*2 {
		return hash, fmt.Errorf("更新清单必须包含有效的 SHA-256")
	}
	if _, err := hex.Decode(hash[:], []byte(value)); err != nil {
		return hash, fmt.Errorf("更新清单必须包含有效的 SHA-256")
	}
	return hash, nil
}

func get(ctx context.Context, raw string, timeout time.Duration) (*http.Response, error) {
	initial, err := parseURL(raw)
	if err != nil {
		return nil, err
	}
	client := http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 || (initial.Scheme == "https" && req.URL.Scheme != "https") {
				return fmt.Errorf("拒绝更新地址的重定向")
			}
			_, err := parseURL(req.URL.String())
			return err
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, initial.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("更新服务返回 HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func Fetch(raw string, key ed25519.PublicKey) ([]byte, Manifest, error) {
	if len(key) != ed25519.PublicKeySize {
		return nil, Manifest{}, fmt.Errorf("此构建未配置更新签名公钥")
	}
	resp, err := get(context.Background(), raw, 8*time.Second)
	if err != nil {
		return nil, Manifest{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxManifest+1))
	if err != nil {
		return nil, Manifest{}, err
	}
	manifest, err := Parse(data, key)
	return data, manifest, err
}

type progressWriter struct {
	written int64
	total   int64
	notify  func(int64, int64)
}

func (w *progressWriter) Write(data []byte) (int, error) {
	w.written += int64(len(data))
	w.notify(w.written, w.total)
	return len(data), nil
}

func Download(platform Platform, target string, progress func(int64, int64)) error {
	return DownloadTo(context.Background(), platform, PackagePath(target), progress)
}

func DownloadTo(ctx context.Context, platform Platform, target string, progress func(int64, int64)) error {
	want, err := parseHash(platform.SHA256)
	if err != nil {
		return err
	}
	resp, err := get(ctx, platform.URL, 10*time.Minute)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength > MaxPackage {
		return fmt.Errorf("更新安装包超过大小限制")
	}
	file, err := os.CreateTemp(filepath.Dir(target), "download-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash, &progressWriter{total: resp.ContentLength, notify: progress}), io.LimitReader(resp.Body, MaxPackage+1))
	if err != nil {
		return err
	}
	if n == 0 || n > MaxPackage || (platform.Size > 0 && n != platform.Size) || (resp.ContentLength >= 0 && n != resp.ContentLength) || [sha256.Size]byte(hash.Sum(nil)) != want {
		return fmt.Errorf("更新安装包 SHA-256 或大小校验失败")
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), target)
}

func PackagePath(staging string) string { return filepath.Join(staging, "Meerkat-Setup.exe") }

func VerifyFile(path, expected string) error {
	want, err := parseHash(expected)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, MaxPackage+1))
	if err != nil {
		return err
	}
	if n == 0 || n > MaxPackage || [sha256.Size]byte(hash.Sum(nil)) != want {
		return fmt.Errorf("更新安装包 SHA-256 或大小校验失败")
	}
	return nil
}
