package agent

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"meerkat/edge/internal/protocol"
)

const attachmentDirName = ".meerkat-attachments"

// materializeAttachments 把 command 内联的附件写到任务 cwd，返回给 Claude 的提示后缀。
func materializeAttachments(cwd string, attachments []protocol.Attachment) (string, error) {
	if len(attachments) == 0 {
		return "", nil
	}
	dir := filepath.Join(cwd, attachmentDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建附件目录失败: %w", err)
	}
	var lines []string
	for _, attachment := range attachments {
		content, err := base64.StdEncoding.DecodeString(attachment.ContentBase64)
		if err != nil || len(content) == 0 {
			return "", fmt.Errorf("附件 %s 内容无效", attachment.Name)
		}
		name := filepath.Base(strings.TrimSpace(attachment.Name))
		if name == "." || name == "/" || name == "" {
			name = "attachment"
		}
		name = attachment.ID + "-" + name
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return "", fmt.Errorf("写入附件失败: %w", err)
		}
		lines = append(lines, fmt.Sprintf("- %s (%s)", path, attachment.Mime))
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "\n\n用户上传了以下附件，已保存到任务工作目录，请先读取/查看这些文件再回答：\n" + strings.Join(lines, "\n"), nil
}
