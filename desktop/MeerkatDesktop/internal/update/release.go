package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"time"
)

type releaseInfo struct {
	ID     int64  `json:"id"`
	Tag    string `json:"tag_name"`
	Assets []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"assets"`
}

func readRelease(ctx context.Context, endpoint string) (releaseInfo, error) {
	var release releaseInfo
	resp, err := get(ctx, endpoint, 15*time.Second, "application/vnd.github+json")
	if err != nil {
		return release, err
	}
	defer resp.Body.Close()
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return release, err
	}
	if len(data) > limit {
		return release, fmt.Errorf("发布信息超过大小限制")
	}
	err = json.Unmarshal(data, &release)
	return release, err
}

func (release releaseInfo) asset(base, name string) (string, error) {
	for _, asset := range release.Assets {
		if asset.Name == name && asset.ID > 0 {
			return base + "/assets/" + strconv.FormatInt(asset.ID, 10), nil
		}
	}
	return "", fmt.Errorf("发布 %s 缺少 %s", release.Tag, name)
}

func FetchRelease(base string, key ed25519.PublicKey) ([]byte, Manifest, int64, error) {
	release, err := readRelease(context.Background(), base+"/latest")
	if err != nil {
		return nil, Manifest{}, 0, err
	}
	endpoint, err := release.asset(base, "latest.json")
	if err != nil {
		return nil, Manifest{}, 0, err
	}
	data, m, err := Fetch(endpoint, key)
	if err != nil {
		return nil, Manifest{}, 0, err
	}
	if release.Tag != "v"+m.Version {
		return nil, Manifest{}, 0, fmt.Errorf("发布标签与签名清单版本不符")
	}
	if release.ID <= 0 {
		return nil, Manifest{}, 0, fmt.Errorf("发布记录缺少有效 ID")
	}
	return data, m, release.ID, nil
}

// Resolve by the signed version even if another release became latest after the check.
func ReleasePackageURL(ctx context.Context, base, version, signedURL string, expectedID int64) (string, error) {
	u, err := parseURL(signedURL)
	if err != nil {
		return "", err
	}
	if !VersionValid(version) {
		return "", fmt.Errorf("更新清单版本无效")
	}
	tag := "v" + version
	release, err := readRelease(ctx, base+"/tags/"+url.PathEscape(tag))
	if err != nil {
		return "", err
	}
	if release.Tag != tag {
		return "", fmt.Errorf("发布标签与签名清单版本不符")
	}
	if expectedID <= 0 || release.ID != expectedID {
		return "", fmt.Errorf("发布记录已变化，请重新检查更新")
	}
	return release.asset(base, path.Base(u.Path))
}
