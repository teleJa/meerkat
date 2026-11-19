package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meerkat/edge/internal/protocol"
)

const (
	maxArtifactBytes = 2 << 20
	maxArtifactsTurn = 12
	maxArtifactDepth = 3
)

func artifactMime(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".txt", ".log":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".ts", ".tsx", ".jsx", ".css", ".go", ".py", ".rs", ".sh":
		return "text/plain; charset=utf-8"
	case ".pdf":
		return "application/pdf"
	}
	if value := mime.TypeByExtension(ext); value != "" {
		return value
	}
	return "application/octet-stream"
}

// collectArtifacts 扫描本轮工作目录中新增/修改的常见文件，并通过 WebSocket 上报中心。
// Phase 1 只支持单文件 ≤2MB；失败只记日志，不影响轮次结果。
func (m *Manager) collectArtifacts(cmd *protocol.Command, since time.Time) {
	cwd := strings.TrimSpace(cmd.Payload.Cwd)
	if cwd == "" {
		return
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return
	}
	cutoff := since.Add(-2 * time.Second)
	var files []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__") {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(root, path)
			if rel != "." && strings.Count(rel, string(os.PathSeparator)) >= maxArtifactDepth {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() <= 0 || info.Size() > maxArtifactBytes {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}
		files = append(files, path)
		if len(files) >= maxArtifactsTurn {
			return filepath.SkipAll
		}
		return nil
	})
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil || len(content) == 0 || len(content) > maxArtifactBytes {
			continue
		}
		name := filepath.Base(path)
		sum := sha256.Sum256(content)
		artifact := protocol.Artifact{
			TaskID: cmd.TaskID, TurnID: cmd.TurnID, SessionID: cmd.SessionID,
			Name: name, Path: path, Mime: artifactMime(name), Size: int64(len(content)),
			SHA256: hex.EncodeToString(sum[:]), ContentBase64: base64.StdEncoding.EncodeToString(content),
		}
		if err := m.sendMessage(protocol.ArtifactRegister{Type: protocol.TypeArtifactRegister, Artifact: artifact}); err != nil {
			m.log.Printf("上报工件 %s 失败：%v", path, err)
			break
		}
		m.log.Printf("已上报工件 %s（%d bytes）", path, len(content))
	}
}
