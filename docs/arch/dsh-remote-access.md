# DSH 远程访问架构

## 1. 问题

DSH 的 Web UI 不是为“嵌入任意前缀路径”设计的：

- 页面按根路径加载 `/assets/...`；
- WebSocket 固定走 `/api/remote.mux`；
- 插件 bundle 使用 `/plugins/??@...` 这种特殊 query；
- DSH 自己还有 token 和浏览器 cookie。

如果直接把 DSH 挂在 `/harness-proxy/{id}/` 后面给 iframe 用，绝对路径 `/api/remote.mux` 会打到 Meerkat 自己的 API，导致“页面加载了但一直显示重新连接中”。

因此当前采用**两层代理**：

1. Hub 侧的路径代理：负责认证、cookie、跨 tailnet；
2. Desktop 侧的每设备本地 origin 代理：让 DSH 以为自己运行在独立 origin。

## 2. 全链路

```mermaid
flowchart LR
  Iframe[DSH iframe<br/>http://127.0.0.1:A/] --> Origin[Desktop 本地 origin 代理<br/>127.0.0.1:A]
  Origin --> LocalUI[Desktop 本地 UI Server]
  LocalUI -->|/harness-proxy/id/...| HubPath[Hub /harness-proxy/{id}/...]
  HubPath -->|server-side DSH cookie jar| HubProxy[Hub 本地 DSH 代理<br/>127.0.0.1:8801-8900]
  HubProxy -->|tsnet/HTTP 1.1| DSH[Edge 上的 dsh web]
```

### 2.1 Edge 侧：管理 DSH 实例并上报状态

- `edge/internal/harnessweb.Manager` 管理本机唯一的活动实例（详见 2.5“每设备 DSH profile”）：
  ```text
  dsh --profile <profile> --no-open --host 127.0.0.1 --port 51857
  ```
- 带权限补丁时额外使用 `--patch <dsh-permission.yml>`；Edge 从 stdout 解析 `dsh web: http://...?token=...`；
- 通过 WebSocket `harness.update` 上报 URL 与 profile 状态；
- 没有保存过 profile 选择时保持旧默认：启动 `web`；
- 默认 `dsh_web_enabled=true`；启动超时 90s；Edge 启动的实例随 Edge 退出而关闭；
- `dsh` 定位顺序：`dsh_web_command` → PATH → 常见安装位置（`~/.local/bin`、`~/.local/node22/bin`、`~/Library/pnpm`、`~/.nvm/versions/node/*/bin`、`/opt/homebrew/bin` 等）→ 登录 shell `command -v dsh`（缓存 5 分钟）。Finder 启动的 Edge 只继承系统 PATH，不能只依赖 `exec.LookPath`。找不到时返回可操作错误（提示安装或用 `dsh_web_command` 指定绝对路径），dsh 秒退时错误带 stderr 尾部；
- 拉起 dsh 的子进程 PATH 会前置 dsh 自身目录、符号链接目标目录与 node 目录，满足 `bin.js` 的 `#!/usr/bin/env node`；
- 启动前做端口预检，端口被未知进程占用时报错，不再无条件 kill 端口占用者。

### 2.2 Hub 侧：每设备反向代理

```text
Edge harness URL
  -> Hub device_harness.url
  -> ensureHarnessProxy(deviceID)
  -> 127.0.0.1:8801-8900（deviceID FNV 哈希）
```

- 上游强制 HTTP/1.1（`ForceAttemptHTTP2=false`、ALPN `http/1.1`），避免 DSH WebSocket 在 HTTP/2 下失败；
- Hub 启用 tsnet 时，代理的 `DialContext` 使用 `ts.Dial`，不依赖系统 Tailscale；
- DSH 的网页、资源、WebSocket 都通过该本地代理。每设备复用一组 HTTP 连接池，覆盖 Hub 路径代理到本地监听器、本地监听器到 DSH 两段；
- 设备上报新的 DSH 目标、撤销设备或切换 tsnet 时，对应的代理释放空闲连接并按需重建；切换 tsnet 会重建所有设备的代理；
- 代理请求日志写入 Hub 数据目录。下一条请求会按本地日期或 32 MiB 切分日志，并清理超过 7 天的归档，将当前文件和归档合计限制在 128 MiB。旧版超大日志首次轮转时保留最多 32 MiB 的最新完整行。

### 2.3 Hub 路径代理

对外暴露：

```text
/harness-proxy/{deviceID}/
/harness-proxy/{deviceID}/{任意路径}
```

行为：

- 所有 HTTP 方法都转发（包含 POST `/api/session/create`）；
- 路径前缀剥离后交给本地 DSH 代理；
- **保留原始 query**，不重新编码，兼容 `/plugins/??@...`；
- 删除 URL 中 Hub 注入的 `token`；
- 首次访问时用带 token 的 URL 建立服务端 DSH cookie jar；
- 忽略浏览器带来的旧 `Cookie`，改用服务端 jar；
- 代理自己的 `/api/remote.mux` WebSocket 升级；
- 入口要求 `meerkat_session` cookie 或移动 bearer（`harness:proxy`）；转发给 DSH 前删除 `Authorization` 与 `Cookie`，Hub 凭证不进入 DSH；
- 撤销移动凭证后，新请求/WS 升级返回 401，已建立的连接不强制断开。

> 桌面 UI 的 iframe 继续使用同源 `/harness-proxy/...`，鉴权由 `meerkat_session` cookie 完成；Android App 的本地反代在请求上注入移动 bearer。详见[移动设备凭证](network-and-security.md#移动设备凭证)。

### 2.4 Desktop 侧：每设备本地 origin

如果 iframe 直接加载 `http://<desktop>/harness-proxy/{id}/`，DSH 的绝对 `/api/remote.mux` 会命中 Desktop 自己的 `/api`，连接失败。

`ensureHarnessOrigin(deviceID)` 为每个设备启动一个 **随机 loopback 端口**：

```text
http://127.0.0.1:<随机>/
  -> Desktop UI /harness-proxy/{deviceID}/...
  -> Hub /harness-proxy/{deviceID}/...
  -> DSH
```

因此：

- iframe origin 是该随机端口；
- DSH 的 `/assets`、`/plugins`、`/api/remote.mux` 都以它为根；
- WebSocket 不再是跨路径问题；
- 每个设备一个 origin，互不污染 cookie/缓存；
- 页面从 `/desktop/harness-origin?device=...` 取得该 URL。

对于远端 `DSH 0.2.2-rc.2`，桌面 origin 还接管差异面板 HTML 的 `POST /api/changes.open`。代理用变更摘要中的相对路径和文件接口的会话工作目录定位文件，核对原按钮点击后生成 60 秒目录票据。初次打开显示原生确认，接受后在本机桌面配置中记住选择；可在设置恢复询问。系统浏览器通过独立的 `localhost` 预览 origin 读取 HTML 和同目录的受支持资源。浏览器可执行本次预览页面的脚本，预览 origin 只提供对应文件读取，隔离 Meerkat 的 RPC、Cookie 和开放应用入口。版本不匹配时，仍可使用 DSH 差异面板内的预览。

### 2.5 每设备 DSH profile

- 发现文件：`$DSH_HOME/runtime/web-<profile>.json`，字段 `profile`、`url`（带 token）、`pid`、`port`、`started_at`，权限 0600。Edge 启动实例后写入；外部 launcher（如 launchd）启动常驻实例后也写同一文件。
- Edge 列出 `$DSH_HOME/profiles/*`，并按发现文件标记运行中；`connect` 直接接管外部实例（不 spawn），`launch` 由 Edge 启动所选 profile（已有同 profile 的有效实例时不重复拉起），`stop` 只停止 Edge 自己启动的实例，外部实例只断开。
- 新建 profile 走 `dsh --profile <name> --from-default-profile web --help`：先复制模板创建，再由帮助退出，不启服务。
- Hub 用 `device_dsh` 保存每台设备的选择（profile + mode），用 `device_dsh_state` 保存最近状态；Edge 重连时 Hub 下发 `harness.profile` 的 `select` 恢复选择。带 token 的 URL 仍只存 `device_harness`，不返回浏览器。
- tailnet 的 DSH 转发目标跟随当前活动实例端口，外部实例可以监听 51857 之外的端口。
- 外部实例必须让 Hub 可达（`node` 模式下配置 `--public-url` / `--trusted-host`）；Edge 上报时会把发现文件里的 loopback 地址改写为 tailnet 公网地址。

## 3. 认证链路

```text
Edge 启动 DSH -> 带 token 的 URL
Hub ensureHarnessProxy -> 连接本地代理
Hub path proxy 首次请求 -> GET token URL -> 保存 Set-Cookie
后续 iframe/assets/WS -> 服务端 jar 注入 Cookie
浏览器/iframe 不需要知道 DSH token
```

- DSH token 不返回给 UI；
- DSH cookie 存在 Hub 内存 jar，不写 SQLite；
- Hub 重启后首次访问会重新建立 jar；
- token 轮换/过期时，重新打开即可重建。

## 4. iframe keep-alive

旧行为：路由切走时 `LiveHarnessContent` 被 React 卸载，iframe 销毁，回来重建，DSH 看起来“又要重新打开一次”。

当前：

- `AppShell` 常驻渲染 `PersistentHarnessDetail`；
- `/#/live/harness/:deviceId` 的路由元素是空占位；
- 路由变化只切换 CSS `hidden`，不卸载 iframe；
- 回到同一设备直接显示原 iframe；
- 切换到另一台设备才更换 origin/URL；
- 新建窗口和主窗口相互独立。

边界：

- 退出 App、Edge 重启、DSH 进程重启仍会重建；
- 隐藏时 iframe 仍保持 WebSocket 连接；
- 只保留当前设备一个常驻 iframe，避免内存无限增长。

## 5. 新窗口

- 「新窗口打开」优先复用 Desktop 的设备 origin。设备 origin 不可用时，从 Hub 的 `/api/devices/{id}/harness-proxy` 取得路径。
- 调 Desktop 原生 `POST /desktop/open-window`；
- Wails 创建子窗口 `Window.NewWithOptions`，`Show()` + `Focus()`；
- 原生端点不可用时退回系统浏览器 `POST /desktop/open-url` / `openExternal`；
- 不使用 WebView 的 `window.open`（会被拦截）。

## 6. Windows WebView2

- Wails v3 在 Windows 使用 WebView2；
- 目标机需要**现代 WebView2 Runtime**；旧 Chromium 100 会导致 DSH UI 加载但 WebSocket 一直“重新连接中”；
- Windows 应用给 WebView2 传 `--no-proxy-server`，避免系统代理拦截本机 HTTP/WebSocket；

## 7. 失败模式

| 现象 | 常见原因 | 处理 |
| --- | --- | --- |
| DSH 设备显示“未安装/未启动” | Edge 没有 DSH URL | 点“安装 DSH”或检查 Edge 日志 |
| 页面加载但一直重连 | 旧 WebView2 / 绝对 WS 路径未代理 | 更新 WebView2、确认走本地 origin |
| 插件加载失败 | query 被重新编码 | 保留 raw query，勿用 `url.Values.Encode()` 全量重写 |
| 401/需要登录 | 服务端 DSH jar 丢失/过期 | 重新打开 DSH 触发重新建 jar |
| `edge_proxy_unavailable` | Node 模式 Edge 本地代理未就绪 | 检查 tsnet 登录/Edge 进程 |
