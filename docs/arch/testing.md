# 测试与发布验证

## 1. 测试分层

| 层级 | 内容 | 命令或环境 |
| --- | --- | --- |
| Go Hub 单测 | 设备/任务/命令/DSH 代理与 profile 状态/认证/SQLite/移动凭证/配对码/代理鉴权 | 在桌面模块运行 `go test ./hub` |
| Android 单测 | 凭证迁移、Keystore fake store、401 分类、过期窗口、配置 | 在桌面模块运行 `go test ./androidclient` |
| Edge 单测 | 协议、配对（令牌注册、先入网再配对、重试分类）、命令、DSH 生命周期/profile/发现文件、tsnet | `go test ./...`（Edge 模块） |
| Web 构建 | Vite 单文件构建并复制进桌面嵌入目录 | 在仓库根目录运行 `make web` |
| 发布冒烟 E2E | 路由、Windows 窗口控制、设置页角色卡片、网络引导、Tailscale 登录入口、配对弹窗、任务弹窗、DSH keep-alive | 在 Web 模块运行 `pnpm test:e2e` |
| 手工验证 | Windows WebView2、真实 DSH、跨设备路径 | 每次改动 DSH/网络/Windows 时执行 |

开发中心的 Node.js 测试可在 `center/` 运行。桌面运行时的 Hub 测试位于 `desktop/MeerkatDesktop/hub`。

## 2. 发布冒烟 E2E

前置：

```bash
cd web
pnpm install
pnpm exec playwright install chromium
```

启动本机 Meerkat 后，取本地 UI 端口（如 `http://127.0.0.1:58389`）：

```bash
cd web
MEERKAT_E2E_BASE_URL=http://127.0.0.1:58389 pnpm test:e2e
```

覆盖：

- 6 个主路由可打开且无横向溢出；
- Windows 桌面窗口控制按钮可见；
- 设置页节点角色只读卡片可见；
- 连接失败时显示网络配置引导；
- 旧 `/#/live/chat` 重定向 DSH，页面不再出现旧版对话；
- 接入设备弹窗：邀请码 + 二维码、复制、重新生成，邀请码在弹窗内不溢出；
- 实时任务刷新、派发弹窗打开/关闭；
- DSH iframe 在路由切换后不重建。

不覆盖：

- 真实 DSH 模型调用；
- 实际提交任务/注册 Edge；
- 真实 Tailscale 授权与跨设备 tsnet 配对（需要真机浏览器登录）；
- 退出、撤销、删除等破坏性操作；
- Windows WebView2 的真实渲染。

## 3. 手工验证要求

以下改动必须做真实验证，不能只看单测：

### DSH/代理改动

- 打开本机 DSH，确认插件加载、WebSocket 连接、无“重新连接中”；
- 新建 1 个会话并发送一句话，确认收到回复；
- 从 Meerkat 其他页面切回，确认 iframe 未重建。

### Windows 改动

- 用交互桌面截图确认 Wails 窗口真实渲染；
- 确认 WebView2 版本现代；
- 确认本机 UI 的 `/api` 不出现 Bad Gateway；
- 确认 DSH 子窗口可打开；
- 观察 App 启动、健康轮询、DSH 启动、任务执行期间不出现控制台窗口闪烁；手工在终端运行 `meerkat-edge run` 仍能看到日志。

### tsnet/远程改动

- Hub tsnet 与 Edge tsnet 同时在线；
- Windows Node 通过 Edge 本地 Hub 代理访问，不依赖系统 Tailscale；
- Hub 路径代理在远端浏览器/iframe 中加载 DSH，而不仅是 curl。

### Android 移动端改动

- 覆盖安装后旧 `client-config.json` 不再含 token，Keystore 中有凭证；
- 输入 Hub 配对码完成配对，设备/任务/DSH 均可使用；
- Hub 撤销后下一次请求显示「凭证已被撤销」，重新配对恢复；
- 锁屏/切后台数分钟后前台进程和常驻通知仍在；
- 桌面 Web UI 的 DSH iframe 仍能加载（session cookie 路径未被鉴权改坏）。

## 4. 发布前最小检查

打包统一走仓库根目录的 [Makefile](../../Makefile)（`make help` 列出所有目标；命令细节与产物路径见 Makefile 注释）：

1. `cd desktop/MeerkatDesktop && go test ./hub ./androidclient`；
2. `cd edge && go test ./...`；
3. 前端构建：`make web`（等价 `cd web && npx vite build`；`pnpm build` 的 tsc 步骤缺 @types/node，不走这条）；
4. 启动 Meerkat，设置 `MEERKAT_E2E_BASE_URL`，运行 `pnpm test:e2e`；
5. 手工打开本机 DSH，确认页面加载、WebSocket 已连接、可新建会话；
6. Windows 目标机额外确认 WebView2 版本 >= 现代版本，并检查 DSH 页面不处于“重新连接中”；
7. 出包并核对产物：
   - `make package-win`：Windows 安装包 + 免安装 zip（容器或本机 NSIS）；
   - `make package-win-portable`：只出免安装目录 + zip（不需要 docker）；
   - `make package-macos`：macOS `Meerkat.app`；
   - 目标结束时会打印路径、大小与 sha256，直接用于发布记录。

当前尚未接入 CI；上述命令需在发布机上执行，或后续接入仓库 CI。
