# Meerkat 协议 v1 · 设备连接

本文描述设备注册、鉴权、心跳、指标和设备查询。所有时间使用 UTC ISO 8601，毫秒可选。服务端另存 `received_at`。

- 监听地址取决于运行模式。桌面 Hub 默认使用 `127.0.0.1:8790`，开发中心默认使用端口 `8787`。
- 管理接口鉴权：`Authorization: Bearer <MEERKAT_ADMIN_TOKEN>`。
- Edge 注册：一次性配对令牌；注册成功后拿 `device_secret`，用于 WebSocket hello。
- 设备凭据只以 sha256 存储；任何日志、响应回显不回传 secret。

## 1. REST

### POST /api/pairing-tokens（管理）

请求：

```json
{ "label": "MacBook Pro 本机", "ttl_seconds": 600 }
```

响应 201：

```json
{
  "token": "mkp_9f8a...（64 hex）",
  "expires_at": "2026-11-06T05:50:00.000Z",
  "label": "MacBook Pro 本机"
}
```

- 令牌一次性使用，默认 10 分钟有效；过期或已用返回 401 `pairing_token_invalid`。

### 移动设备凭证（M3）

移动设备不复用 Edge 的 `device_secret`。管理端生成一次性配对码，App 换取独立 bearer：

```text
POST /api/mobile/pairing-tokens   管理员；响应 { code, expires_at, scopes, ttl_seconds }
POST /api/mobile/register         配对码；响应 { credential_id, token, scopes, expires_at, credential }
GET  /api/mobile/credentials      管理员；响应 { credentials[], server_time }
POST /api/mobile/credentials/:id/revoke  管理员
POST /api/mobile/credentials/rotate      移动 bearer；响应新 token 与 5 分钟宽限
```

- 配对码格式 `MEERKAT-XXXX-XXXX`，10 分钟有效、一次性、只存 SHA-256；
- token 格式 `mkm_<credential_id>_<32 字节 base64url secret>`，Hub 只存 hash；响应只返回一次明文；
- scopes 固定为 `devices:read`、`tasks:read`、`harness:proxy`；移动 bearer 的写请求返回 `403 mobile_read_only`；
- 过期返回 `401 mobile_credential_expired`，撤销返回 `401 mobile_credential_revoked`，无效返回 `401 mobile_credential_invalid`；
- `/harness-proxy/*` 接受管理员 session、admin bearer 或移动 bearer；转发 DSH 前删除 `Authorization` 与 `Cookie`。

### POST /api/edges/register（Edge）

请求：

```json
{
  "pairing_token": "mkp_9f8a...",
  "protocol_version": "1",
  "edge_version": "0.1.0",
  "device": {
    "name": "MacBook Pro",
    "hostname": "MacBookPro",
    "os": "macOS 15.4",
    "arch": "arm64",
    "role": "Edge · 本机",
    "capabilities": ["metrics"],
    "boot_id": "boot_1e2d3c4b"
  }
}
```

响应 200：

```json
{
  "device_id": "dev_a1b2c3d4",
  "device_secret": "<64 hex>",
  "protocol_version": "1",
  "center_time": "2026-11-06T05:40:00.000Z",
  "heartbeat_interval_ms": 10000,
  "metrics_interval_ms": 5000
}
```

错误：`401 pairing_token_invalid`、`400 invalid_device`、`409 device_conflict`（同 boot_id 且令牌已用时不覆盖，需重新配对或撤销旧凭据）。

### GET /api/devices（管理）

```json
{
  "server_time": "2026-11-06T05:41:00.000Z",
  "devices": [
    {
      "id": "dev_a1b2c3d4",
      "name": "MacBook Pro",
      "hostname": "MacBookPro",
      "os": "macOS 15.4",
      "arch": "arm64",
      "role": "Edge · 本机",
      "status": "online",
      "status_age_seconds": 2,
      "last_seen_at": "2026-11-06T05:40:58.000Z",
      "last_metrics_at": "2026-11-06T05:40:57.000Z",
      "edge_version": "0.1.0",
      "protocol_version": "1",
      "boot_id": "boot_1e2d3c4b",
      "capabilities": ["metrics"],
      "created_at": "2026-11-06T05:39:00.000Z",
      "metrics": {
        "sampled_at": "2026-11-06T05:40:57.000Z",
        "cpu_pct": 18.4,
        "mem_used_bytes": 20401094656,
        "mem_total_bytes": 34359738368,
        "disk_used_bytes": 195421011968,
        "disk_total_bytes": 494384795648,
        "gpu_util_pct": null,
        "gpu_mem_used_bytes": null,
        "gpu_mem_total_bytes": null,
        "gpu_temp_c": null
      }
    }
  ]
}
```

- `metrics` 为最近一条采样；缺失或采集失败为 `null`，不记为 0。
- 设备状态由 last_seen 推导：`online` ≤ 30s，`stale` ≤ 60s，`offline` > 60s 或从未连接；设备状态与任务状态正交。

### GET /api/devices/:id/metrics?limit=60（管理）

```json
{
  "device_id": "dev_a1b2c3d4",
  "samples": [
    { "sampled_at": "...", "cpu_pct": 18.4, "mem_used_bytes": 1, "mem_total_bytes": 2, "disk_used_bytes": 3, "disk_total_bytes": 4, "gpu_util_pct": null, "gpu_mem_used_bytes": null, "gpu_mem_total_bytes": null, "gpu_temp_c": null }
  ]
}
```

### 浏览器会话（Web 面板）

中心可以直接托管 web 构建（`MEERKAT_WEB_DIST`，默认 `../web/dist`）。浏览器通过 HttpOnly 会话 Cookie 访问管理接口，admin token 只用于登录，不写入浏览器存储。

```http
POST /api/session
content-type: application/json
x-meerkat-request: 1

{ "token": "<MEERKAT_ADMIN_TOKEN>" }
```

响应 200 + `Set-Cookie: meerkat_session=<id>; Path=/; HttpOnly; SameSite=Lax; Max-Age=43200`；错误 401 `invalid_credentials`、403 `csrf_required`。

- `GET /api/session` → 200 `{ "authenticated": true, "via": "session", "expires_at": "..." }`；未登录 401。
- `DELETE /api/session` → 200 并清除 Cookie。
- 会话 Cookie 与 Bearer token 均可访问管理接口；使用 Cookie 的非 GET 请求必须携带 `x-meerkat-request: 1`（CSRF 防护）。
- 会话有效期 12 小时（`sessionTtlSeconds`），过期自动清理。

### GET /api/health（公开）

```json
{ "ok": true, "version": "0.1.0", "time": "2026-11-06T05:41:00.000Z", "uptime_seconds": 42 }
```

## 2. WebSocket

地址：`ws://<center>/api/edges/ws`。连接后 10 秒内必须发送 hello，否则关闭（4400）。文本帧 JSON，一次一条消息。

### 客户端 → 中心

hello：

```json
{ "type": "hello", "protocol_version": "1", "device_id": "dev_a1b2c3d4", "device_secret": "<64 hex>", "edge_version": "0.1.0", "boot_id": "boot_1e2d3c4b", "capabilities": ["metrics"] }
```

heartbeat（默认 10s）：

```json
{ "type": "heartbeat", "seq": 7, "sent_at": "2026-11-06T05:41:00.000Z" }
```

metrics.batch（默认 5s）：

```json
{
  "type": "metrics.batch",
  "stream_id": "str_4f2a",
  "seq": 21,
  "sampled_at": "2026-11-06T05:41:00.000Z",
  "metrics": {
    "cpu_pct": 18.4,
    "mem_used_bytes": 20401094656,
    "mem_total_bytes": 34359738368,
    "disk_used_bytes": 195421011968,
    "disk_total_bytes": 494384795648,
    "gpu_util_pct": 93,
    "gpu_mem_used_bytes": 11453251584,
    "gpu_mem_total_bytes": 17179869184,
    "gpu_temp_c": 72
  }
}
```

harness.update（连接建立时上报一次，之后状态变化时上报）：

```json
{
  "type": "harness.update",
  "url": "http://192.0.2.17:51858/?token=...",
  "dsh": {
    "profile": "bot",
    "mode": "connect",
    "status": "running",
    "owner": "external",
    "error": "",
    "installed": true,
    "profiles": [
      { "name": "bot", "available": true, "running": true },
      { "name": "web", "available": true, "running": false }
    ],
    "updated_at": "2026-11-13T05:41:00Z"
  }
}
```

- `url` 为空表示该设备当前没有可代理的 DSH；中心会清除旧 URL。
- `dsh` 可选，旧 Edge 只上报 `url`。带 token 的 URL 只走 Edge→Hub，不下发给浏览器。

### 中心 → 客户端

hello.ack：

```json
{ "type": "hello.ack", "protocol_version": "1", "server_time": "...", "device_id": "dev_a1b2c3d4", "heartbeat_interval_ms": 10000, "metrics_interval_ms": 5000 }
```

heartbeat.ack / metrics.ack：

```json
{ "type": "metrics.ack", "seq": 21, "received_at": "2026-11-06T05:41:00.100Z" }
```

harness.profile（Hub 是 profile 选择的权威来源；Edge 重连后下发 `select`）：

```json
{ "type": "harness.profile", "request_id": "dsh_...", "action": "connect", "profile": "bot", "mode": "connect" }
```

- `action`：`select`（保存选择并按其 mode 恢复）、`connect`（只接管已运行实例）、`launch`（由 Edge 启动）、`stop`（只停 Edge 自己启动的实例）、`create`（新建 profile，名字放在 `profile` 字段）、`refresh`。
- Edge 状态变化后通过 `harness.update` 回报，Hub 不等待单条命令的响应帧。

error（随后关闭）：

```json
{ "type": "error", "code": "auth_failed", "message": "device_secret 无效" }
```

### 关闭码

| code | 含义 |
|---|---|
| 4400 | 协议错误（未知类型、字段缺失、未先 hello） |
| 4401 | 鉴权失败（device_id/secret 不匹配或设备已撤销） |
| 1000 | 正常关闭（Edge 主动断开） |

### 重连

- Edge 指数退避 + 抖动（1s → 2s → 4s … 上限 30s）；重连后重新 hello，seq 延续（进程内）。
- 中心对同一 device_id 的新连接替换旧连接（关闭旧连接 1000）。
- 至少一次投递：ack 丢失可重发；P1 指标允许有界丢弃（仅保留最近 N 条），任务类事件后续版本再引入 outbox。

## 3. 状态与存储约定（P1）

- `devices`：device_id 为稳定身份，不以 IP/主机名代替；保存 secret 的 sha256、last_seen_at、boot_id。
- `metrics`：宽表行存，保留最近 24 小时（清理任务后续版本）；索引 `(device_id, sampled_at)`。
- `events`：P1 记录 device.registered / device.connected / device.disconnected 三类，便于排查。
- 中心与 Edge 的时钟差异由 received_at 兜底；排序使用 sampled_at + seq。

## 4. 版本与兼容

- `protocol_version: "1"`；未知版本注册返回 400 `unsupported_protocol`，WS hello 返回 4400。
- 新增可选字段向后兼容；删除或改语义必须升版本。

## 5. 样例

见 `shared/protocol/samples/`（中心与 Edge 测试共用，防止两端各自解释）。
