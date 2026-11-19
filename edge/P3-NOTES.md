# Edge P3 实现说明（任务执行 / Claude 适配 / outbox / 去重）

## 行为增量

| 场景 | P1（改动前） | P3（改动后） |
|---|---|---|
| 收到 `command` | 未知类型，记日志忽略 | 校验过期/cwd/kind 后发 `command.ack`；accepted 后按 kind 执行或排队 |
| 同一 session 多个 turn | 不支持 | 串行执行：同一时刻至多一个 turn，其余按到达顺序排队 |
| claude 执行 | 无 | spawn `claude -p <request> --output-format stream-json --verbose`，续接加 `--resume <id>`，可配置 harness 命令与参数 |
| 事件回传 | 无 | 解析 system/assistant/tool/result → `event.batch`（stream_id+seq，≤200 条/批） |
| 轮次结束 | 无 | 发 `turn.completed`（succeeded/failed/cancelled，exit_code/duration/result/error） |
| 断网事件 | 指标允许有界丢弃 | 事件先追加本地 outbox，收到 `event.ack` 后截断，重连重发未确认 |
| 重复指令 | 无 | command_id 持久去重；重复投递只重发 ack（+已完成结果），不重复执行 |
| cwd | 无 | 必须存在且位于允许根（默认 home；--allow-root / config.json allow_root 覆盖），否则 rejected: cwd_not_allowed |
| 取消/超时 | 无 | cancel 发 SIGKILL 进程组并上报 cancelled；默认 1800s 超时上报 failed |

## 推断假设

1. 中心按协议 v1-p3 发送 command；cancel 指令可能不带 turn_id/payload（当前 center 实现即如此），此时取消该 session 的全部运行中与排队 turn。
2. `event.ack` 的 seq 视为对同 stream 的累计确认：删除 seq ≤ ack.seq 的全部批次，以及它们之前的 turn.completed。
3. `turn.completed` 没有独立 ack：由后续 event.batch 的 ack 连带清理；无后续批次时保留并在重连时重发（中心按 turn_id 去重）。
4. command_id 在“校验通过、开始执行前”持久记录（at-most-once 执行）；进程崩溃后同一 command 不会重跑，代价是该轮可能停在中心侧直到过期。
5. `harness_session_id` 以 init 事件的 session_id 为准；命令行 payload 传来的值作为续接提示，仅在没有本地记录时使用。
6. session.started 在拿到 init 事件后发送；若当时未连接导致发送失败，会在下一轮重试（harness_version 取 init 的 claude_code_version，缺失则为空）。
7. harness 可执行文件优先级：--harness-cmd / --claude-path > config.json harness_command（字符串=可执行文件；数组=可执行文件+前缀参数）> `claude`；追加参数来自 config.json harness_args 与 --harness-arg。
8. event.batch 单批上限按中心 zod schema 取 200 条；异常/未知 stream 事件类型直接跳过，不影响连接。
9. 取消/超时用进程组 kill（Unix setpgid；Windows taskkill /T /F），确保 claude 子进程一并结束。
10. 事件 ID 使用 `evt_<8 hex>`，由本机随机生成；中心按 event_id 去重。

## 设计

新增 Go 包（均在 `edge/internal/` 下）：

- `outbox`：追加文件 `outbox.jsonl` + 元数据 `outbox.meta.json`（stream_id/next_seq/next_id）；`Ack` 累计截断并原子重写；内存 sent 标记用于重连重发。
- `dedup`：追加文件 `commands.jsonl`，记录 command_id + ack 状态 + 可选 turn.completed；重复指令据此重发 ack/结果。
- `claude`：stream-json 解析（system/assistant/user/result，含 tool_use/tool_result）、exec runner（stdout 逐行解析、stderr 限长、进程组 kill）。
- `agent`：指令校验、会话串行 worker、事件批处理、session/turn 生命周期消息、outbox flush goroutine、config.json 读取。
- `wsclient`：读写锁化 `writeJSON`，hello.ack 后挂载 Sender 并重发未确认事件，收到 command/event.ack 分发到 agent。
- `cli run`：新增 `--allow-root`、`--claude-path`、`--harness-cmd`、`--harness-arg`，并装配 agent.Manager。

状态目录文件：`identity.json`（原有）、`config.json`（可选）、`outbox.jsonl`、`outbox.meta.json`、`commands.jsonl`。
