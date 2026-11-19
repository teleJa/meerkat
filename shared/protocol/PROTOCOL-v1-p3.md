# Meerkat 协议 v1 · P3 任务与会话（追加）

本文是 [PROTOCOL-v1.md](PROTOCOL-v1.md) 的追加部分，覆盖任务派发、托管会话、指令投递、事件回传与取消。字段命名与 v1 保持一致；管理接口鉴权同 v1（Bearer 或 HttpOnly 会话 Cookie + CSRF 头）。

## 1. REST（管理）

### POST /api/tasks

请求：

```json
{
  "request": "请回复 OK",
  "device_id": "dev_a1b2c3d4",
  "cwd": "/Users/example/projects/meerkat",
  "title": "可选标题",
  "run_profile": "default",
  "idempotency_key": "可选，重复提交返回同一任务"
}
```

响应 201：

```json
{
  "task": {
    "id": "tsk_1a2b3c4d",
    "title": "请回复 OK",
    "source": "managed",
    "device_id": "dev_a1b2c3d4",
    "session_id": "sess_9f8e7d6c",
    "status": "queued",
    "stage": "指令已持久化，等待 Edge 执行",
    "cwd": "/Users/example/projects/meerkat",
    "request": "请回复 OK",
    "created_at": "2026-11-06T07:00:00.000Z",
    "updated_at": "2026-11-06T07:00:00.000Z",
    "evidence_kind": null,
    "evidence_note": null
  },
  "session_id": "sess_9f8e7d6c",
  "command_id": "cmd_5c4b3a2f"
}
```

- 同时创建 session（mode=managed，status=idle）与 command（kind=start_turn，status=pending）。
- 设备在线则立即经 WS 投递；离线则保持 pending，设备重连且 hello 通过后补发。
- 命令默认 10 分钟过期；过期后 command=expired，task=failed（stage: 指令过期）。

### POST /api/tasks/:id/turns

```json
{ "request": "再补充一句", "idempotency_key": "可选" }
```

- 仅允许 managed 任务且 task 未被取消/结束；同一任务的上一条 command 未完成时返回 409 `turn_in_progress`。
- 创建 turn（status=queued）+ command（kind=send_turn），task.status=running（stage: 追加输入已排队）。

### POST /api/tasks/:id/cancel

- 创建 command（kind=cancel）；task.status=cancel_requested（stage: 取消已记录，等待 Edge 确认）。
- 若命令未投递则直接撤销（command=cancelled，task=cancelled）。

### GET /api/tasks?limit=&status=&device_id=

```json
{ "tasks": [ { "...": "task 字段", "session_mode": "managed" } ] }
```

### GET /api/tasks/:id

```json
{
  "task": { "...": "task 字段" },
  "session": {
    "id": "sess_...", "mode": "managed", "status": "running",
    "harness_session_id": "claude_...", "cwd": "...",
    "created_at": "...", "last_activity_at": "..."
  },
  "turns": [
    { "id": "turn_...", "seq": 1, "request": "...", "status": "running",
      "started_at": "...", "ended_at": null, "exit_code": null, "result": null, "error": null }
  ],
  "commands": [
    { "id": "cmd_...", "kind": "start_turn", "status": "delivered", "created_at": "...", "delivered_at": "...", "acked_at": null }
  ],
  "events": [
    { "event_id": "evt_...", "type": "tool.use", "at": "...", "turn_id": "turn_...", "payload": { "tool": "Bash", "summary": "..." } }
  ]
}
```

### GET /api/sessions?limit=&mode=

```json
{ "sessions": [ { "id": "sess_...", "task_id": "tsk_...", "device_id": "...", "mode": "managed",
  "status": "running", "cwd": "...", "harness_session_id": "...", "turn_count": 2,
  "created_at": "...", "last_activity_at": "..." } ] }
```

### GET /api/sessions/:id

同 task 详情的 session/turns/events 结构（不含 task）。

## 2. WebSocket 追加消息

### 中心 → Edge：command

在 hello.ack 之后发；或在创建命令时设备已连接则立即发。

```json
{
  "type": "command",
  "command_id": "cmd_5c4b3a2f",
  "kind": "start_turn",
  "task_id": "tsk_1a2b3c4d",
  "session_id": "sess_9f8e7d6c",
  "turn_id": "turn_1",
  "payload": {
    "request": "请回复 OK",
    "cwd": "/Users/example/projects/meerkat",
    "run_profile": "default",
    "harness_session_id": null,
    "timeout_seconds": 1800
  },
  "created_at": "2026-11-06T07:00:00.000Z",
  "expires_at": "2026-11-06T07:10:00.000Z"
}
```

- `kind`: `start_turn` | `send_turn` | `cancel`；cancel 只带 command_id/task_id/session_id/turn_id。
- Edge 必须用 command_id 去重；过期命令不得执行。

### Edge → 中心

```json
{ "type": "command.ack", "command_id": "cmd_...", "status": "accepted" | "rejected", "detail": "可选", "at": "..." }
{ "type": "session.started", "command_id": "cmd_...", "task_id": "tsk_...", "session_id": "sess_...",
  "harness_session_id": "claude_...", "harness_version": "2.1.304", "cwd": "...", "at": "..." }
{ "type": "turn.started", "command_id": "cmd_...", "task_id": "...", "session_id": "...", "turn_id": "turn_1", "at": "..." }
{ "type": "event.batch", "stream_id": "str_ab12", "seq": 7, "events": [
  { "event_id": "evt_...", "task_id": "...", "session_id": "...", "turn_id": "turn_1",
    "type": "assistant.message" | "tool.use" | "tool.result" | "result" | "system.init" | "stderr" | "permission.request",
    "at": "...", "payload": { } }
] }
{ "type": "turn.completed", "task_id": "tsk_...", "session_id": "sess_...", "turn_id": "turn_1",
  "status": "succeeded" | "failed" | "cancelled", "exit_code": 0, "duration_ms": 1234,
  "result": "OK", "error": null, "at": "..." }
```

中心 ACK：

```json
{ "type": "event.ack", "stream_id": "str_ab12", "seq": 7 }
```

- event.batch 至少一次投递；中心按 event_id 去重，ack 丢失时 Edge 重发同一 seq。
- turn.completed 使用相同 turn_id 去重；status 映射到 task 状态（succeeded/failed/cancelled）。
- 命令被拒绝时 Edge 发 command.ack rejected + detail；中心 task=failed。

### 状态映射

| 事件 | task.status |
|---|---|
| 创建 | queued |
| command.ack accepted / session.started / turn.started | running |
| turn.completed succeeded | succeeded（evidence=explicit_agent_report） |
| turn.completed failed | failed |
| turn.completed cancelled | cancelled |
| command rejected / expired | failed（stage 写明原因） |

## 3. Edge 行为要求（P3）

- 同一 session 的输入串行；同一时刻每条 session 至多一个执行中的 turn，新 turn 排队。
- 每条 command 在本机持久去重（state dir 下已执行 command_id 列表）。
- event/turn 结果写入本地 outbox（追加文件，ack 后截断），断网不丢；重连后按原 seq 重发。
- cwd 必须存在于本机且位于 Edge 允许根目录（默认用户 home）下，否则 rejected: cwd_not_allowed。
- Claude 适配：`claude -p <request> --output-format stream-json --verbose`；续接使用 `--resume <harness_session_id>`；解析 system/assistant/tool/result 事件；取消时 kill 进程并上报 cancelled。
- 未安装 claude 或启动失败 → command accepted 后 turn.completed failed，错误写入 result/error。
## 6. 流式增量事件

Edge 以 `claude --output-format stream-json --verbose --include-partial-messages` 运行时，会额外产生 `stream_event` 行。Edge 将其中的文本增量转换为 `assistant.delta` 事件：

```json
{
  "event_id": "evt_...",
  "type": "assistant.delta",
  "at": "2026-11-06T14:04:18.000Z",
  "turn_id": "turn_...",
  "payload": { "text": "狐獴是一种", "index": 0 }
}
```

- 仅转发 `delta.type == "text_delta"`；thinking/tool 增量不转发；
- 与其它事件一样进入 `event.batch`、outbox 与 `event.ack` 确认流程；
- 最终仍会发送完整的 `assistant.message`（与增量拼接结果一致），客户端可用它替换流式气泡；
- `assistant.delta` 只用于聊天流式渲染，任务时间线建议隐藏逐条增量。

## 7. 实现补充（与 P3 代码一致）

- 中心下发的 `cancel` 命令不带 `turn_id` / `payload`；Edge 视为取消该 session 的运行中与排队 turn。Edge 也兼容带 `turn_id` 的单 turn 取消。
- `turn.completed` 没有独立 ack：由后续 `event.batch` 的累计 ack 清理；没有后续批次时每次重连重发，中心按 turn_id 幂等更新。
- `command` 为 at-most-once：Edge 在执行前持久记录 command_id；崩溃窗口内不重跑，中心 10 分钟过期后把 task 置 failed（指令过期）。
- 追问：succeeded 的托管任务允许追加输入并重新打开为 running；failed/cancelled 拒绝；同任务存在运行中 command 时返回 `409 turn_in_progress`。
- hello capabilities 目前仍为 `["metrics"]`，中心暂不按能力位拦截 command；后续加 `tasks`/`sessions` 能力位时需升版本或明确兼容。
- outbox 暂未设置磁盘上限；任务事件表暂不自动清理。
