# Meerkat 狐獴

**Meerkat 是一个面向单用户的多设备 Agent 控制面。**

在一台机器上启动 Hub，再把运行编码 Agent 的电脑接入同一个工作区。你可以在一个界面里选设备、派任务、打开远程 Agent 会话，并查看任务进度和设备状态。任务在选中的设备上执行，可以使用那台机器的工作目录和开发环境。

![Meerkat 实时设备页，显示两台在线设备、一台离线设备及资源用量](docs/images/devices.webp)

界面截图均使用演示数据。[查看系统架构图](docs/arch/overview.md#系统总览)。

## 核心能力

- **设备接入**：用邀请码把多台电脑接入同一个工作区，查看各设备的连接状态。
- **任务派发**：选定设备和工作目录后下发任务，在 Agent 会话中查看实时回复。
- **会话管理**：打开远程设备上的 Agent 会话，查看上下文，并对支持的会话继续追问。
- **实时看板**：集中查看任务进度、设备在线状态和资源用量。
- **桌面与 Web**：桌面端可以创建工作区并运行 Hub，也可以作为 Node 加入已有工作区。Web 面板提供设备、任务和会话视图。

### 任务看板

![Meerkat 任务看板，按待处理、进行中、已完成、失败或取消分栏显示任务](docs/images/task-board.webp)

### 会话详情

![Meerkat 实时会话页，右侧展开演示会话的 Agent 输出](docs/images/sessions.webp)

## 仓库结构

| 目录 | 职责 |
|---|---|
| [`center/`](center/) | 独立运行的开发中心，提供本地 Web 开发所需的 API 和 WebSocket。 |
| [`edge/`](edge/) | 运行在各设备上的 Go 进程，负责接入 Hub、执行任务和上报状态。 |
| [`web/`](web/) | 设备、任务和会话的 Web 界面，也用于桌面端。 |
| [`desktop/`](desktop/) | macOS 和 Windows 桌面应用，包含内嵌 Hub 和设备接入流程。 |
| [`shared/protocol/`](shared/protocol/) | Hub 与 Edge 的通信协议和消息示例。 |

## 快速开始

### 启动桌面工作区

从源码构建需要 Go 1.27.3、Node.js、pnpm 和 make。在仓库根目录安装依赖并构建 Web 界面：

```bash
(cd web && pnpm install --frozen-lockfile)
make web
```

按下方的「打包」步骤构建并打开桌面应用。Windows 需要 WebView2 Runtime。

1. 在第一台电脑上选择「创建新工作区」。打开「设置 → 网络」，启动内嵌 Tailscale 并完成登录。
2. 在 Hub 上选择「邀请设备」。在另一台电脑打开 Meerkat，选择「加入现有工作区」，粘贴邀请码并按提示完成配对。
3. 设备上线后，可在「实时任务」中派发任务，或在「Harness」中打开该设备已安装的 Agent 环境。

### 启动本地 Web 开发面板

执行上面的 Web 构建步骤后，使用 Node.js 22.13 或更新版本，从仓库根目录启动开发中心：

```bash
(cd center && pnpm install --frozen-lockfile && MEERKAT_HOST=127.0.0.1 pnpm dev)
```

打开 `http://127.0.0.1:8787/#/live`，使用启动时在 `center/data/admin-token` 生成的令牌登录。

## 打包

在 macOS 构建机上，从仓库根目录执行：

```bash
make package-macos          # macOS App
make package-win-portable   # Windows x64 免安装 ZIP
make package-win            # Windows x64 免安装 ZIP 和 NSIS 安装包
```

macOS 构建需要 Xcode 命令行工具。Windows 安装包构建需要 Docker 和项目使用的 NSIS 镜像，或本机 `makensis`。产物位于 `desktop/MeerkatDesktop/bin/`。

## 许可证

Meerkat 使用 [Apache-2.0](/LICENSE) 许可证。
