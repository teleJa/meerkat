# Meerkat 当前架构

> 适用范围：当前仓库的运行时代码。

## 先读什么

| 文档 | 内容 |
| --- | --- |
| [overview.md](overview.md) | 系统上下文、组件、两种运行模式、核心数据流 |
| [dsh-remote-access.md](dsh-remote-access.md) | DSH 启动、Hub 代理、本地 origin 代理、iframe keep-alive |
| [network-and-security.md](network-and-security.md) | 内嵌 tsnet、端口、认证、信任边界与风险 |
| [testing.md](testing.md) | 单元测试、发布冒烟 E2E、手工验证边界 |

## 一句话架构

**Meerkat 是单用户、桌面优先的多设备 Agent 控制面：Wails 桌面壳承载 React UI；桌面内置 Go Hub 与本地 Edge，或作为 Node 通过内嵌 Tailscale 连接远端 Hub；每台设备上的 Edge 托管本机 DSH，Hub 提供 DSH 的同源路径代理，让 UI 在任何设备上打开任意设备的 DSH。**

## 关键事实

- 运行时主实现：`desktop/MeerkatDesktop`（Go + Wails v3 + 内嵌 Go Hub）。
- `center/` 提供可独立运行的 Node.js 开发中心。桌面应用使用内嵌 Go Hub。
- UI：React 19 + Vite + Tailwind + shadcn/Radix + TanStack Query，构建为单 HTML 后嵌入 Wails。
- Hub：Go `net/http` + WebSocket + SQLite（`modernc.org/sqlite`，无需 CGO）。
- Edge：Go sidecar，配对、心跳、指标、任务执行、DSH 生命周期、内嵌 tsnet。
- 网络：`tailscale.com/tsnet` 内嵌，不依赖系统安装 Tailscale。
- Harness：DSH（`dsh web`）是主入口。每台设备可选 profile，并可连接已在运行的实例。详见 [DSH 远程访问](dsh-remote-access.md)。
- Android：独立只读客户端（`desktop/MeerkatDesktop/androidclient`），进程内 tsnet + 本地反代；Keystore 保存移动凭证，前台服务保活；`/harness-proxy` 同时接受桌面 session 和移动 bearer。详见[网络与安全](network-and-security.md#移动设备凭证)。
- DSH 权限：Edge 写入权限补丁，默认 `danger-full-access`。
- Windows：Wails/WebView2；目标机需要现代 WebView2 Runtime。
- 打包：仓库根 [Makefile](../../Makefile)（`make package-win` / `make package-macos` / `make package-all`），产物在 `desktop/MeerkatDesktop/bin/`；运行依赖与数据目录见 [overview.md](overview.md)。

## 两种运行模式

| 模式 | `config.json` `mode` | Hub | 本地 Edge | 远端访问 |
| --- | --- | --- | --- | --- |
| 内置 Hub | `bundled` | App 内嵌 Go Hub | 自动配对并启动 | Hub tsnet 端口 7443 |
| 加入 Hub（Node） | `node` | 远端 Hub | 启动并连接远端 | UI 走 Edge 本地 Hub 代理 51900 |

> 有效配置值为 `bundled` 和 `node`。

> 产品文案里的“Hub / Node”不是代码字段值；代码字段以 `mode` 为准。  
> `node` 模式不会回退到系统 Tailscale 或 `center_url`；Edge 本地代理未就绪时 UI 返回 503。

## 端口与地址速查

| 组件 | 默认地址 | 说明 |
| --- | --- | --- |
| Desktop 本地 UI | `127.0.0.1:<随机>` | Wails + 内嵌前端，前端与 `/api`、`/harness-proxy` 同源 |
| 内嵌 Hub HTTP | `127.0.0.1:8790` | 默认仅 loopback；可被 `center_url` 覆盖，`allow_lan=true` 才绑局域网 |
| Hub tsnet | `:<7443>`（tailnet IP） | 远端 Edge/客户端访问 Hub |
| Hub DSH 反向代理 | `127.0.0.1:8801-8900` | 按 deviceID FNV 分配，每个设备一个 |
| Edge 本地 Hub API 代理 | `127.0.0.1:51900` | `node` 模式 UI 访问 Hub 的出口 |
| Edge 本机 DSH | `127.0.0.1:51857` | `dsh web`，端口可配置 |
| Edge tsnet DSH | `:<51858>`（tailnet IP） | TCP 转发到本机 51857 |
| Desktop 本地 DSH origin | `127.0.0.1:<随机>` | 每设备一个，转回本地 UI 的 `/harness-proxy` |
| Web dev server | `127.0.0.1:5173` | 仅开发，`/api` 代理到 8787 |

DSH 的默认入口是 `/#/live/harness`。
