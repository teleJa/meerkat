// Package cli 实现 meerkat-edge 的命令行入口。
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"meerkat/edge/internal/buildinfo"
	"meerkat/edge/internal/identity"
)

// Run 分发子命令并返回进程退出码：0 成功，1 运行失败，2 用法错误。
func Run(args []string) int {
	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)
	if len(args) == 0 {
		Usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "pair":
		return cmdPair(args[1:], logger)
	case "run":
		return cmdRun(args[1:], logger)
	case "doctor":
		return cmdDoctor(args[1:], logger)
	case "version":
		fmt.Fprintln(os.Stdout, buildinfo.Line())
		return 0
	case "help", "-h", "--help":
		Usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n", args[0])
		Usage(os.Stderr)
		return 2
	}
}

// Usage 打印用法说明。
func Usage(w io.Writer) {
	fmt.Fprintf(w, `meerkat-edge %s —— Meerkat Edge（设备注册、连接、指标上报与托管执行）

用法：
  meerkat-edge run    [--state-dir <目录>] [--metrics-interval <时长>] [--heartbeat-interval <时长>]
                      [--allow-root <目录>] [--claude-path <路径>] [--harness-cmd <路径>] [--harness-arg <参数>...]
                      [--no-console]
  meerkat-edge pair   --center <url> --token <配对令牌> [--name <设备名>] [--role <角色>] [--state-dir <目录>]
  meerkat-edge doctor [--center <url>] [--state-dir <目录>]
  meerkat-edge version

示例：
  meerkat-edge run
  meerkat-edge run --allow-root ~/ai-workspace --harness-cmd /tmp/fake-claude

说明：
  正常流程由 Meerkat App 驱动：托盘「加入工作区」写入 config.json 的
  pairing_token 与 tsnet_hub_url 后启动 run；run 先启动内嵌 tsnet、等待入网，
  再用一次性令牌向 Hub 注册并写入身份文件（0600），随后连接、上报心跳与指标、
  执行托管指令，按 Ctrl-C 优雅退出。
  --no-console（或 MEERKAT_NO_CONSOLE=1）：隐藏 Edge 拉起的子进程控制台窗口，
  由 App 以无窗口方式拉起时使用；手工在终端运行时不加，保留可见日志。
  pair 仅保留用于手工调试本地 Hub（普通 HTTP，不启动内嵌 tsnet）。
  doctor 检查身份、Hub 连通性与本机指标采集。

Edge 配置（可选，$STATE_DIR/config.json）：
  {"allow_root": "~/ai-workspace",
   "harness_command": "claude 或 [可执行文件, 前置参数...]",
   "harness_args": ["追加参数"],
   "tsnet_enabled": true,
   "tsnet_hostname": "meerkat-edge-<后缀>",
   "tsnet_hub_url": "http://100.x.x.x:7443",
   "pairing_token": "<Hub 生成的一次性配对令牌>"}
  配对地址复用 tsnet_hub_url：启用 tsnet 时经内嵌网络访问，未启用时按普通 HTTP 访问。
  优先级：命令行参数 > config.json > 默认值（allow_root=用户 home，harness=claude）。
`, buildinfo.Version)
}

// resolveStateDir 把 --state-dir 解析为绝对可用的目录，缺省走平台默认值。
func resolveStateDir(flagValue string) (string, error) {
	if dir := strings.TrimSpace(flagValue); dir != "" {
		return dir, nil
	}
	return identity.DefaultDir()
}

// parseFlags 统一处理子命令参数错误；--help 视为正常。
func parseFlags(fs *flag.FlagSet, args []string) (ok bool, exitCode int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "未知参数：%s\n", strings.Join(fs.Args(), " "))
		fs.Usage()
		return false, 2
	}
	return true, 0
}
