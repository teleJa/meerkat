# Meerkat Edge

Meerkat 的 Edge 程序：向中心注册设备、建立 WebSocket 长连接、采集并上报本机资源指标，
并在 P3 中执行中心下发的托管任务（start_turn / send_turn / cancel），把 Claude 的
stream-json 输出转换为事件回传。

协议契约见 [`shared/protocol/PROTOCOL-v1.md`](../shared/protocol/PROTOCOL-v1.md) 与
[`shared/protocol/PROTOCOL-v1-p3.md`](../shared/protocol/PROTOCOL-v1-p3.md)；本目录只依赖这些契约。

## 命令

```text
meerkat-edge pair   --center <url> --token <配对令牌> [--name <设备名>] [--role <角色>] [--state-dir <目录>]
meerkat-edge run    [--state-dir <目录>] [--metrics-interval <时长>] [--heartbeat-interval <时长>]
                    [--allow-root <目录>] [--claude-path <路径>] [--harness-cmd <路径>] [--harness-arg <参数>...]
                    [--runtime <headless|herdr>] [--herdr-bin <路径>] [--herdr-arg <参数>...]
                    [--magpie-url <url>] [--harness-model <模型>] [--no-magpie]
meerkat-edge doctor [--center <url>] [--state-dir <目录>]
meerkat-edge version
```

- `run`：正常入口。状态目录没有 `identity.json` 时先启动内嵌 tsnet、等待入网，再用
  `config.json` 里的 `pairing_token` 经 `tsnet_hub_url` 自动注册（首次加入等待上限 10 分钟）；
  配对完成前不启动本地 Hub API 代理与 WebSocket。已配对时直接连接 `/api/edges/ws`，先发 hello，
  收到 hello.ack 后按服务端间隔发心跳与 `metrics.batch`；断线按 1s→2s→4s…（上限 30s）指数退避 + 抖动重连；
  SIGINT 发送 1000 正常关闭；同时接收中心下发的托管指令并执行（见下文）；
- `pair`：仅保留给手工调试本地 Hub（普通 HTTP，不启动内嵌 tsnet）；App 的「加入工作区」不再调用它；
- `doctor`：只读检查身份、中心连通性与本机指标采集，退出码 0=无失败项，1=有失败项；
- `version`：版本与协议信息。

## 托管执行（P3）

消息流：`command` → `command.ack` → `turn.started` → `event.batch`（stream_id+seq）→
`turn.completed`；中心用 `event.ack` 累计确认事件批次。

- **串行执行**：同一 session（`sess_*`）同一时刻只跑一个 turn，新 turn 按到达顺序排队；
  不同 session 可并行。
- **去重**：每条 `command_id` 在状态目录 `commands.jsonl` 持久记录（含 ACK 状态与完成结果）；
  重复投递只重发 `command.ack`（+ 已记录的 `turn.completed`），不重复执行。
- **过期**：`expires_at` 已过的指令不执行；收到即 `command.ack rejected: command_expired`，
  排队期间过期则 `turn.completed failed`（error: 指令已过期，未执行）。
- **cwd 校验**：`cwd` 必须存在、是目录且位于允许根目录内（软链接按真实路径判断），
  否则 `command.ack rejected`，detail 为 `cwd_missing` / `cwd_not_found` / `cwd_not_allowed`。
- **取消**：`cancel` 指令 kill harness 进程组并上报 `turn.completed cancelled`；
  排队中的轮次直接上报 cancelled；中心当前实现不带 turn_id，此时取消该 session 全部运行中/排队轮次。
- **超时**：`payload.timeout_seconds`（默认 1800s）到期后 kill 进程并上报 failed。
- **事件 outbox**：`event.batch` 与 `turn.completed` 先追加到 `outbox.jsonl`（含 stream_id/seq），
  连接恢复后按原 seq 重发未确认记录；`event.ack` 累计截断。ack 丢失时允许重发，中心按
  `event_id` / `turn_id` 去重。
- **单批上限**：`event.batch.events` 最多 200 条（中心 schema 限制），超出自动拆批。
- **流式增量**：默认追加 `--include-partial-messages`，把 Claude 的 `text_delta` 转成 `assistant.delta` 事件（payload.text），供 Web Chat 做流式渲染；最终仍发送完整 `assistant.message`。

### 状态目录文件

| 文件 | 内容 |
|---|---|
| `identity.json` | 中心地址、device_id、device_secret、设备描述（0600） |
| `config.json` | 可选配置：`allow_root`、`harness_command`、`harness_args`、`magpie_url`、`harness_model`、`magpie_disabled`、`tsnet_enabled`、`tsnet_hostname`、`tsnet_hub_url`、`pairing_token` |
| `tsnet-status.json` | 内嵌 tsnet 状态：`state`、`auth_url`、`tailscale_ips`、`local_api_url` |
| `pairing-status.json` | 自动配对进度：`phase`（waiting_network/pairing/paired/error）、`error`、`device_id`，供 App 展示 |
| `outbox.jsonl` / `outbox.meta.json` | 未确认事件与 stream_id/seq 元数据 |
| `commands.jsonl` | command_id 去重记录与已完成结果 |
| `magpie-settings.json` | 默认生成的 Claude `--settings`（`ANTHROPIC_BASE_URL` 指向 magpie，模型名统一为路由组；0600，无密钥） |

### config.json

```json
{
  "allow_root": "/Users/example/projects",
  "harness_command": "claude",
  "harness_args": ["--model", "sonnet"],
  "magpie_url": "http://127.0.0.1:3425",
  "harness_model": "group/coding",
  "runtime": "headless",
  "herdr_bin": "herdr"
}
```

- `harness_command` 也可以是数组：`["/path/stub", "--fixture", "/tmp/stream.jsonl"]`，
  第一项为可执行文件，其余为追加在标准参数之前的前缀参数；
- 优先级：`--harness-cmd` / `--claude-path` > `config.json harness_command` > `claude`；
- `allow_root`：`--allow-root` > `config.json allow_root` > 用户 home；
- 标准动作 `--dangerously-skip-permissions` 由 Edge 自动注入，不需要写进 `harness_args`；
- 标准参数：`-p <request> --output-format stream-json --verbose`，续接时追加
  `--resume <harness_session_id>`；Edge 还会自动追加 `--dangerously-skip-permissions`
  （已有则去重），然后追加 `harness_args` / `--harness-arg`。
- **安全边界**：这意味着 Edge 上的 Claude 托管任务会跳过 Claude Code 的权限确认，
  只应部署在受信任、允许其执行本机命令的设备上；目前没有人工审批 UI。

### Herdr 运行时（可选）

默认 `runtime=headless`，即当前的 Claude stream-json 逐轮执行。设置
`--runtime herdr`（或 config.json `runtime: "herdr"`）后，Edge 会用本机 Herdr
agent pane 执行轮次：

- 自动确保 `herdr server` 运行；
- 为 session 创建 workspace/pane，启动 `claude` agent（kind=claude）；
- 首轮处理 Claude 的 trust 提示；
- 通过 `herdr agent prompt --wait` 提交消息，读取终端屏幕提取最终回答；
- 通过 Herdr `agent_session.value` 续接（Meerkat 的 harness_session_id）。

`--herdr-bin` 指定 herdr 可执行文件；`--herdr-arg` 追加给 agent start 的参数。
注意：Herdr 是终端 TUI，当前 adapter 输出最终 assistant.message，不产生 `assistant.delta`；
聊天主链路仍建议使用 headless 运行时，Herdr 用于终端/长会话（后续 Web 终端 Tab）。

### 默认使用本机 magpie

默认情况（harness 为 `claude` 且未传 `--settings`）下，Edge 会在状态目录生成
`magpie-settings.json`，并给 Claude 追加：

```text
--settings <state-dir>/magpie-settings.json --model group/coding
```

其中 settings 通过 Claude Code 的 `--settings` 覆盖 `ANTHROPIC_BASE_URL`
为 `http://127.0.0.1:3425`（magpie），并把 Sonnet/Haiku/Opus/Fable 模型名映射到
`group/coding` 路由组。这样无需修改用户的 `~/.claude/settings.json`，也不会
把密钥写进 Meerkat 文件；鉴权仍由用户原有 Claude 配置提供。

- 默认值：`magpie_url=http://127.0.0.1:3425`、`harness_model=group/coding`；
- 覆盖：`--magpie-url`、`--harness-model` 或 config.json 同名字段；
- 关闭：`--no-magpie` 或 `config.json {"magpie_disabled": true}`（使用用户自己的 Claude 配置）；
- 自定义 harness（如 fake-claude stub）或用户显式传 `--settings` 时不会注入；
- 默认配置已验证（2026-11-06）：真实 Claude 返回 `MAGPIE_DEFAULT_OK`，追问
  `--resume` 返回 `MAGPIE_DEFAULT_RESUME_OK`，两轮约 7.1s / 6.4s。

本机联调可用假 harness（不联网、输出 stream-json）：

```bash
meerkat-edge run --harness-cmd "$(pwd)/testdata/fake-claude.sh" --allow-root ~/ai-workspace
```

支持 `FAKE_CLAUDE_SLEEP=<秒>`（验证取消/超时）与 `FAKE_CLAUDE_FAIL=1`（验证失败路径）。

## 身份

- darwin：`~/Library/Application Support/Meerkat/edge/identity.json`
- windows：`%LOCALAPPDATA%\Meerkat\edge\identity.json`
- 其他：`~/.local/state/meerkat/edge/identity.json`

目录 `0700`、文件 `0600`。`device_secret` 只写入该文件并用于 hello，不打印、不写日志。

## 指标

| 指标 | 来源 | 失败时 |
|---|---|---|
| `cpu_pct` | gopsutil 整机 CPU（0–100） | `null` |
| `mem_used_bytes` / `mem_total_bytes` | gopsutil `VirtualMemory`（used = 总量 − 可用） | `null` |
| `disk_used_bytes` / `disk_total_bytes` | 系统盘（Windows）或 `/`（其他） | `null` |
| `gpu_*` | 仅 Windows：`nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu --format=csv,noheader,nounits` | `null` |

nvidia-smi 先查 `PATH`，再查 `C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`；
输出内存单位为 MiB，上报时换算为 bytes；多 GPU 时取第一张。

## 构建与测试

```bash
cd edge
go test ./...
go vet ./...

GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o bin/meerkat-edge-darwin-arm64 ./cmd/meerkat-edge
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o bin/meerkat-edge-windows-amd64.exe ./cmd/meerkat-edge
```

依赖：`github.com/shirou/gopsutil/v4`（v4.25.x，需 Go 1.25）、`github.com/gorilla/websocket`。

## 已知行为

- 中心返回 `error` 消息（如 `auth_failed`）或关闭码 4400/4401 视为致命错误，`run` 直接退出并给出可读提示，
  不做无意义重连；网络类断开才退避重连。
- 心跳 / 指标 seq 在进程内单调递增，跨重连延续；指标 `stream_id` 每个进程生成一次（`str_<4 hex>`）。
- 未收到指标 ack 只记日志（30s 超时清理等待表），不阻塞后续上报；P1 指标允许有界丢弃。
- 指令在"校验通过、开始执行前"持久记录 command_id（at-most-once 执行）；进程在启动进程与保存状态之间
  崩溃时该轮不会自动重跑，中心会在指令过期后标记失败。
- `turn.completed` 没有独立 ack，由后续 `event.ack` 连带清理；无后续批次时保留并在重连时重发
  （中心按 turn_id 去重）。
- 未安装/无法启动 harness：`command.ack accepted` 后以 `turn.completed failed` 上报错误。
- `run` 与 `pair` 不打印 secret；`doctor` 只显示身份摘要。
