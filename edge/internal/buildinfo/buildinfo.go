// Package buildinfo 保存 Edge 的构建信息。
package buildinfo

import (
	"fmt"
	"runtime"

	"meerkat/edge/internal/protocol"
)

// Version 是 Edge 版本号，与协议样例中的 edge_version 保持一致。
const Version = "0.1.0"

// Line 返回单行版本描述，供 version 子命令使用。
func Line() string {
	return fmt.Sprintf("meerkat-edge %s（协议 %s，%s，%s/%s）",
		Version, protocol.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
