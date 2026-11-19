# Meerkat Desktop v0.1

基于 **Go + Wails v3** 的 Meerkat 桌面应用。当前版本已经移除 Node Center，使用**内嵌 Go Hub**。

## 架构

```text
Meerkat.app
├─ Wails WebView（React UI）
├─ Go Hub（HTTP + WebSocket + SQLite）
├─ 本地 Edge sidecar（Go）
└─ 托盘菜单 / 邀请 / 加入工作区
```

- macOS / Windows 共用 Go 代码；
- Center 数据在 App 数据目录的 SQLite；
- Edge 通过 WebSocket 连接 Hub；
- Hub 支持 Hub / Node 两种模式；
- 邀请码 + 加入向导完成多机配对。

## Dock / 菜单栏

- App 使用 regular activation policy，启动后会出现在 Dock 和菜单栏；
- 关闭主窗口后 App 不会退出，Dock 图标保留；
- 右键 Dock 图标 → 选项 → 「在程序坞中保留」即可固定；
- App 图标：`build/darwin/icons.icns`（macOS）/ `build/windows/icon.ico`（Windows）。

## 当前能力

- 自包含 App，不依赖 Node/pnpm/仓库；
- 首次启动自动建库、自动配对本地 Edge、自动登录；
- 托盘菜单：打开、邀请设备、加入工作区、Herdr 状态、退出；
- 邀请码：Hub 生成并复制到剪贴板；Node 粘贴后自动写配置、重启、配对、登录；
- Go Hub：设备注册、指标、任务、会话、追加输入、取消、WebSocket 事件；
- App 退出会关闭 Go Hub 和本地 Edge；
- `/desktop/health` 输出 Center/Edge/Herdr 状态；
- 内置 Go Hub 单元测试。

## 构建（macOS）

```bash
# 在仓库根目录（含前端构建、edge sidecar、bundle.sh 组装与 ad-hoc 签名）
make package-macos
```

产物：`bin/Meerkat.app`，约 **80MB**（含 edge sidecar；当前 bundle.sh 未裁剪符号）。

## 运行

直接双击 `bin/Meerkat.app`，或：

```bash
./bin/Meerkat.app/Contents/MacOS/meerkatdesktop
```

数据目录：

- Hub SQLite：`~/Library/Application Support/Meerkat/center`
- Edge 状态：`~/Library/Application Support/Meerkat/edge`
- 配置：`~/Library/Application Support/Meerkat/config.json`

## Hub / Node

Hub 默认只监听 `127.0.0.1:8790`。需要局域网/远程访问时，在 `config.json` 显式加 `"allow_lan": true`（默认 false）；需要指定具体地址时再配合 `"bind_host": "0.0.0.0"` 或某个网卡地址。`allow_lan` 未开启时即使旧配置残留 `bind_host: "0.0.0.0"` 也只会绑定 loopback。

同一配置目录只允许一个 Meerkat 实例。第二个实例会读取 `instance.lock` 中的 PID 并提示「Meerkat 已在运行」，然后退出，避免覆盖 `tsnet-status.json` 或抢占 8790/51857 端口。

Node App 配置 `mode: "node"` 并指向 Hub。

推荐用图形流程：

1. Hub 托盘「邀请设备」生成邀请码；
2. Node 托盘「加入工作区...」粘贴邀请码；
3. App 自动重启、写配置、配对 Edge、登录 Hub。

## Agent 执行权限

Edge 托管执行会为标准 Claude 参数自动追加：

```text
--dangerously-skip-permissions
```

这是所有 Edge 的标准动作，用于避免 headless 任务卡在无审批 UI 的权限提示上。
不需要在 `config.json` 的 `harness_args` 里重复配置；重复也会去重。

这意味着 Meerkat 控制的 Windows/macOS Edge 可以在允许根目录内执行本机命令和写文件。
只应在受信任设备上运行；当前还没有人工审批工具调用的 UI。

## 工件与预览

Agent 在任务工作目录内本轮新增/修改的文件（单文件 ≤2MB）会在轮次成功后由 Edge 自动上报中心：

- Hub 保存 artifact 元数据与内容；
- **对话详情直接内联显示本轮工件**，不需要跳转任务详情；
- 任务详情也有「工件」页签；
- 图片直接预览；
- HTML/PDF 在沙箱 iframe 中预览；
- Markdown/文本/代码以只读文本预览；
- 支持「打开」和「下载」。

Phase 1 限制：不跨断线重传、不做分片上传、暂不深度渲染 Office 文档。

## 对话滚动与工具调用

- 连续工具调用合并为可折叠的「工具调用 · N 步」；
- 自动滚动只跟随用户/助手消息，不跟随 tool.use/tool.result；
- 用户手动上滚后不会被拉回；
- 有新内容时显示「有新内容」悬浮按钮。

## 用户附件

对话输入框可以上传图片/文档（新对话首轮）：

- 单文件 ≤4MB，单次 ≤4 个，总计 ≤8MB；
- Hub 保存附件，随 command payload 下发 Edge；
- Edge 写入任务工作目录 `.meerkat-attachments/`；
- Agent 收到附件路径后先用 Read 查看再回答；
- 对话用户消息显示附件 chip、图片缩略图，可打开原文件。

支持常见图片、PDF、文本、Markdown、代码和 CSV。追问附件、断线重传和病毒扫描暂未实现。

## 远程 DSH Harness（嵌入）

侧栏「Harness」现在是主入口：

- 先选择设备，再打开该设备的 DSH；
- Hub 为设备启动同源反向代理 `127.0.0.1:8801`；
- iframe 内加载 DSH WebUI，HTTP + WebSocket 都经代理；
- 上游强制 HTTP/1.1/ALPN，避免 DSH WebSocket 在 HTTP/2 下失败；
- DSH 自己的 token 和 browser-trust 仍保留，Meerkat 不绕过。

现有的 `/live/chat` / 工件 / 附件页仅作为 fallback 保留，不再出现在主侧栏。

当前边界：`dsh web`、Tailscale Serve、Harness URL 注册仍是手工/测试配置；自动发现、启动和 token 轮换尚未接入 Edge 生命周期。

## 检查更新与后台下载

App 启动后会读取 `update_manifest_url`，在后台检查更新：

- 发现新版本后自动下载安装包到配置目录的 `updates/`；
- 下载完成后弹窗询问「立即安装 / 稍后」，**不会自动安装**；
- 托盘菜单「检查更新」可手动检查并后台下载；
- 托盘菜单「安装已下载更新」可随时打开已下载的更新；
- 配置 `sha256` 时会校验下载文件；没有配置则跳过；
- Windows 用户选择安装后静默运行新版安装包；macOS 用户选择后打开下载的安装包。

配置：

```json
{
  "update_manifest_url": "https://your-domain.example/meerkat/latest.json"
}
```

manifest 格式：

```json
{
  "version": "0.1.1",
  "notes": "更新说明",
  "platforms": {
    "darwin": { "url": "https://.../Meerkat-Setup-0.1.1.dmg", "sha256": "..." },
    "windows": { "url": "https://.../Meerkat-Setup-0.1.1.exe", "sha256": "..." }
  }
}
```

未配置更新地址时，托盘「检查更新」会明确提示，不会发起网络请求。

## Windows 安装包

最终用户直接运行：

构建或下载得到的 `Meerkat-Setup-0.1.0.exe`。自行构建的安装包在 `bin/` 下。

- 当前用户安装（无需管理员/UAC），目录：`%LOCALAPPDATA%\Programs\Meerkat`
- 创建桌面和开始菜单快捷方式
- 写入 HKCU Run：登录 Windows 自动启动
- 安装 `Meerkat.exe` + `meerkat-edge.exe`
- 自带 `Uninstall.exe`，卸载时清理快捷方式和 Run 键，但保留 `%APPDATA%\Meerkat` 配置
- 安装包约 **21MB**
- 已针对旧版 WebView2（Chromium 100）兼容：主题色使用 hex/rgba，不依赖 `oklch()`/`color-mix()`

构建（仓库根目录）：

```bash
make package-win           # 前端 + Meerkat.exe + meerkat-edge.exe + zip + NSIS 安装包
make package-win-portable  # 不跑 NSIS，只出免安装目录 + zip
```

具体编译参数（GUI 子系统、`-trimpath`、容器内 makensis 等）见仓库根 [Makefile](../../Makefile)。

也可分发 zip：`bin/Meerkat-Windows-v0.1.0.zip`。安装包未做代码签名，首次运行可能触发 SmartScreen。

### 排障：DSH 起不来且报 `listen EACCES`（端口被系统保留）

**症状**：设备卡片显示 `DSH 异常`，错误原文里有
`Error: listen EACCES: permission denied 127.0.0.1:51857`（或中文版
「以一种访问权限不允许的方式做了一个访问套接字的尝试」）。Edge 日志里也能看到同一条。

这是 Windows 侧问题，不是 Meerkat 的 bug：Hyper-V / WSL / Docker 会动态保留大量高位
TCP 端口段，监听落在保留段里的端口会被拒绝，即使该端口当下并没有进程占用。

**排查**：

```cmd
netsh interface ipv4 show excludedportrange protocol=tcp
```

对照错误里的端口，看它是否落在某个 `开始端口 - 结束端口` 区间内。该命令 `*` 标记的
是「管理的端口排除」，同样不可用。

**解法（已自动处理，一般不用手动改）**：Edge 现在会在首选端口绑不上时自动向后探测，
最多试 100 个，并把实际端口写进日志与状态文件：

```
本地 Hub API 代理端口 51900 被占用或被系统保留，改用 51901（http://127.0.0.1:51901）
DSH 端口 52000 被占用或被系统保留，改用 52001
```

因此撞上保留段不再需要手改配置。如果你想固定端口，仍可在 Edge 状态目录的 `config.json`
里指定（它只是**首选**，绑不上还会后移）：

```json
{
  "dsh_web_port": 52000,
  "local_api_addr": "127.0.0.1:51900"
}
```

- 文件位置：`%APPDATA%\Meerkat\edge-node\config.json`（与你设置的 `edge_state_dir` 一致，
  默认是 `%APPDATA%\Meerkat\edge`）；
- `dsh_web_port` 缺省或为 `0` 时回落默认值 `51857`；`local_api_addr` 缺省时回落到
  `127.0.0.1:51900`；
- 保存时务必**不带 BOM**：PowerShell 5.1 的 `Out-File -Encoding utf8` 会写 BOM，Go 解析时
  报 `invalid character '\ufeff' looking for beginning of value`。用
  `[System.IO.File]::WriteAllText($path, $json, (New-Object System.Text.UTF8Encoding($false)))`；
- 端口配置只在启动时读，改完必须重启 Meerkat。

**自动降级覆盖的两个端口**：

| 端口 | 默认 | 绑不上时的表现 |
| --- | --- | --- |
| DSH web | `51857`（`dsh_web_port`） | 自动后移，实际端口写进发现文件与 tsnet 转发目标 |
| 本机 Hub API 反代 | `51900`（`local_api_addr`） | 自动后移，实际地址写进 `tsnet-status.json` 的 `local_api_url` |

如果 100 个端口全部不可用，`tsnet-status.json` 会带 `local_api_error` 说明原因，UI 报
`edge_proxy_port_unavailable`（`Edge 本地代理端口不可用：…`），而不是笼统的「尚未就绪」。

默认端口及访问范围见[网络与安全](../../docs/arch/network-and-security.md#2-端口与监听面)。

## Android 客户端

Android 客户端是独立的只读客户端，不启动 Hub 或 Edge：

- 进程内 tsnet，经本地反代访问 Hub；不依赖系统 Tailscale；
- 设备/任务列表、DSH 移动适配、Keystore 凭证、`connectedDevice` 前台服务；
- 首次可用 admin token 兼容，推荐在 Hub「设置 → 移动设备」生成配对码，在 App「设置 → 设备配对」输入，换取 90 天只读凭证；
- 移动凭证只读（`devices:read`、`tasks:read`、`harness:proxy`），可在 Hub 单独撤销；轮换后旧 token 5 分钟宽限。

构建（仓库根目录，SDK/NDK 在 `.runtime/android-sdk`）：

```bash
export JAVA_HOME="$(/usr/libexec/java_home -v 21)"
export GRADLE_USER_HOME="$HOME/.gradle"
desktop/MeerkatDesktop/build/android/gradlew -p desktop/MeerkatDesktop/build/android clean
desktop/MeerkatDesktop/build/android/build-android.sh
```

产物：`desktop/MeerkatDesktop/bin/Meerkat-Android-debug.apk`（包名 `com.meerkat.app`，启动组件 `com.meerkat.app/com.wails.app.MainActivity`）。

真机验证前先卸载旧包，避免旧 `com.wails.app` 与新包抢同一个 tsnet 身份：

```bash
adb uninstall com.wails.app
adb install -r desktop/MeerkatDesktop/bin/Meerkat-Android-debug.apk
adb shell am start -n com.meerkat.app/com.wails.app.MainActivity
```

### 为 Android 调试包配置 Hub 地址

源码内的 `http://192.0.2.10:7443` 是文档示例。App 设置可输入配对码，Hub 地址从应用私有目录的 `files/client-config.json` 读取。

安装可调试 APK 后，用 `adb` 为新安装配置自己的 Hub。把下面 JSON 中的示例地址替换成你自己的 Hub 地址。命令仅在尚无配置文件时写入，以保留已有安装的配置。

```bash
adb shell am force-stop com.meerkat.app
printf '%s\n' '{"hub_url":"http://192.0.2.10:7443"}' |
  adb shell -T 'run-as com.meerkat.app sh -c "umask 077; mkdir -p files && test ! -e files/client-config.json && tee files/client-config.json >/dev/null"'
adb shell am start -n com.meerkat.app/com.wails.app.MainActivity
```

App 的内嵌 Tailscale 节点登录后，去 Hub「设置 → 移动设备」生成配对码，在 App「设置 → 设备配对」输入。`run-as` 只适用于可调试 APK。

**升级注意**：访问 `/harness-proxy` 需要凭证。旧客户端打开 DSH 时如果返回 401，请更新 Android 客户端并重新配对。详见[移动设备凭证](../../docs/arch/network-and-security.md#移动设备凭证)。

## 验证

- Go Hub 单测：`go test ./hub`；
- 端到端：macOS Hub + Windows Node，任务 `GO_HUB_OK`、`GO_HUB_FOLLOWUP`、`GO_HUB_JOIN_OK` 均成功；
- Windows 自包含 Go Hub 启动验证通过；
- 邀请码加入后 Windows Edge 在线；
- App 退出回收 Hub/Edge。

## 已知限制

- 桌面更新需要配置更新清单地址。macOS 公证、Windows 签名尚未接入；
- 邀请码仍携带 admin token（POC），正式版应改为短时邀请/会话交换；
- Herdr 仅状态读取；终端交互仍是 Edge adapter 的只读快照。