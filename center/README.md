# Meerkat Center（P1）

中心服务：Fastify 5 + @fastify/websocket + `node:sqlite`。实现协议见 [shared/protocol/PROTOCOL-v1.md](../shared/protocol/PROTOCOL-v1.md)。

## 运行

```bash
cd center
pnpm install
pnpm dev                 # tsx src/server.ts，默认 0.0.0.0:8787
# 或指定参数：
MEERKAT_HOST=0.0.0.0 MEERKAT_PORT=8787 MEERKAT_DATA_DIR=./data pnpm dev
```

- 数据目录默认 `center/data`：`meerkat.db`（SQLite WAL）与 `admin-token`（0600）。
- 环境变量：
  | 变量 | 默认 | 说明 |
  |---|---|---|
  | `MEERKAT_HOST` | `0.0.0.0` | 监听地址（测试期经 Tailscale 访问） |
  | `MEERKAT_PORT` | `8787` | 端口 |
  | `MEERKAT_DATA_DIR` | `./data` | 数据目录 |
  | `MEERKAT_DB_PATH` | `<data>/meerkat.db` | SQLite 路径 |
  | `MEERKAT_ADMIN_TOKEN` | 自动生成 | 管理接口 Bearer token；未设置时写入 `<data>/admin-token` |
- 管理接口：`Authorization: Bearer <admin-token>`。token 不打印到日志，从文件读取即可（本地测试）。

## 接口（P1）

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/api/health` | 无 | 健康检查 |
| POST | `/api/session` | admin token | 登录，换 HttpOnly 会话 Cookie（需 `x-meerkat-request: 1`） |
| GET | `/api/session` | Cookie/Bearer | 当前会话 |
| DELETE | `/api/session` | Cookie/Bearer | 退出并清 Cookie |
| POST | `/api/pairing-tokens` | 管理 | 生成一次性配对令牌（默认 600s） |
| POST | `/api/edges/register` | 配对令牌 | Edge 注册，返回 device_id/secret |
| GET | `/api/devices` | 管理 | 设备列表 + 最近指标 + 在线状态 |
| GET | `/api/devices/:id` | 管理 | 单设备 |
| GET | `/api/devices/:id/metrics?limit=` | 管理 | 指标历史（默认 60，最大 500） |
| POST | `/api/devices/:id/revoke` | 管理 | 撤销设备凭据 |
| GET | `/api/events?limit=` | 管理 | 注册/连接/断开事件 |
| POST | `/api/tasks` | 管理 | 派发任务（创建 task/session/turn/command） |
| GET | `/api/tasks` | 管理 | 任务列表（可按 status/device_id 过滤） |
| GET | `/api/tasks/:id` | 管理 | 任务详情（session/turns/commands/events） |
| POST | `/api/tasks/:id/turns` | 管理 | 追加输入（同会话串行） |
| POST | `/api/tasks/:id/cancel` | 管理 | 取消（两阶段） |
| GET | `/api/sessions` | 管理 | 会话列表 |
| GET | `/api/sessions/:id` | 管理 | 会话详情（turns/events/task） |
| WS | `/api/edges/ws` | hello 内 device_secret | Edge 实时通道 |

浏览器面板：`pnpm dev` 时若存在 `web/dist/index.html`（`pnpm --dir ../web build`），中心会在 `/` 直接托管面板；`/#/live` 使用 admin token 登录后可查看真实设备、指标曲线、事件，并生成配对令牌或撤销设备。`MEERKAT_WEB_DIST` 可覆盖构建目录，`MEERKAT_COOKIE_SECURE=1` 时 Cookie 带 `Secure`（HTTPS/Tailscale Serve）。

快速验证：

```bash
TOKEN=$(cat data/admin-token)
curl -s -X POST http://127.0.0.1:8787/api/pairing-tokens \
  -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"label":"local edge","ttl_seconds":600}'
```

## 状态口径

- `online`：last_seen ≤ 30s；`stale`：≤ 60s；`offline`：> 60s 或从未连接。
- 设备状态与任务状态正交；指标缺失/采集失败为 `null`，不记为 0。
- 设备身份以 device_id 为准，不以 IP/主机名代替；secret 只保存 sha256。

## 测试

```bash
pnpm typecheck
pnpm test        # 37 个用例：REST 鉴权/配对/注册/设备列表、浏览器会话与 CSRF、静态托管、WS hello/心跳/指标/错误码/撤销、状态推导、任务派发/事件/追问/取消/拒绝、指标保留
```

## P3 任务与会话

- 协议见 [shared/protocol/PROTOCOL-v1-p3.md](../shared/protocol/PROTOCOL-v1-p3.md)。
- 派发后命令持久化；设备在线立即经 WS 投递，离线排队，重连 hello 后补发。
- 任务状态：queued → running → succeeded/failed/cancelled；cancel_requested 为两阶段取消。
- 事件按 event_id 去重入库；指标保留 24 小时（启动清理 + 每 10 分钟）。
- Web 页面由 `/#/live/tasks`、`/#/live/sessions` 使用这些接口。

## P1 已实现 / 未实现

已实现：配对令牌（一次性、过期）、设备注册与凭据校验、WS hello/心跳/指标入库、设备在线状态、设备撤销、事件记录、管理接口。

未实现（后续版本）：浏览器会话认证与 CSRF、REST/WS 的 JSON Schema 文件校验、事件 outbox/ACK 重传、指标保留清理（24h）、Drizzle 迁移体系、TLS 终止（建议 Tailscale Serve）。P1 使用 Node 内置 `node:sqlite`（实验 API）替代 better-sqlite3，避免原生编译；接口层已隔离，可在后续替换。
