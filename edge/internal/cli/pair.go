package cli

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"meerkat/edge/internal/device"
	"meerkat/edge/internal/identity"
)

// cmdPair 处理 pair 子命令：用一次性配对令牌注册设备并保存身份。
//
// App 的“加入工作区”不再调用该子命令：Edge run 会先入网再自动配对。
// 保留它仅用于手工调试本地 Hub（走普通 HTTP），不启动内嵌 tsnet。
func cmdPair(args []string, logger *log.Logger) int {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	centerURL := fs.String("center", "", "Hub 地址，例如 http://127.0.0.1:8787（必填；不启用内嵌网络）")
	token := fs.String("token", "", "Hub 生成的一次性配对令牌（必填）")
	name := fs.String("name", "", "设备显示名，默认使用主机名")
	role := fs.String("role", device.DefaultRole, "设备角色标签")
	stateDir := fs.String("state-dir", "", "身份目录，默认按平台（macOS: ~/Library/Application Support/Meerkat/edge）")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if strings.TrimSpace(*token) == "" {
		logger.Printf("参数错误：--token 不能为空（在 Hub 的「接入设备」中生成邀请码后使用）")
		return 2
	}
	dir, err := resolveStateDir(*stateDir)
	if err != nil {
		logger.Printf("参数错误：%v", err)
		return 2
	}
	if _, statErr := os.Stat(identity.Path(dir)); statErr == nil {
		logger.Printf("提示：%s 已存在，将覆盖旧身份", identity.Path(dir))
	}

	logger.Printf("正在向 Hub %s 注册设备 …", strings.TrimSpace(*centerURL))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id, err := pairDevice(ctx, pairRequest{
		StateDir: dir,
		HubURL:   *centerURL,
		Token:    *token,
		Name:     *name,
		Role:     *role,
		Logger:   logger,
	})
	if err != nil {
		logger.Printf("配对失败：%v", err)
		return 1
	}

	// 注意：device_secret 只写入身份文件，不打印。
	fmt.Printf("配对成功\n  device_id : %s\n  Hub       : %s\n  身份文件  : %s\n  下一步    : meerkat-edge run\n",
		id.DeviceID, id.CenterURL, identity.Path(dir))
	return 0
}
