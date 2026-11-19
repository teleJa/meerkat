# Meerkat Web 实时面板

React + Vite + TypeScript + Tailwind CSS v4 + shadcn/ui 实现的中心 Web 面板。当前只保留三个真实数据页面，所有数据来自中心 REST API，无 mock 数据。

## 页面

| 路由 | 说明 |
|---|---|
| `/live/chat`（默认新建对话） | **Cumora 式 Chat**：左侧团队/成员与房间列表，右侧设备健康与任务信息；选设备/工作目录后像聊天一样下发指令；工作目录会自动带出；Enter 发送、Shift+Enter 换行 |
| `/live/chat/:taskId` | 打开某个任务对应的对话；Edge 上报 `assistant.delta` 时文本流式追加 |
| `/live` | 实时设备：设备卡片、在线状态、CPU/内存/磁盘、5s 轮询 |
| `/live/devices/:deviceId` | 设备详情：指标曲线、事件、生成配对令牌、撤销设备 |
| `/live/board` | 任务看板：按状态分列，点击卡片回到房间 |
| `/live/tasks` | 实时任务：筛选/搜索、派发 Dialog、状态与回执 |
| `/live/tasks/:taskId` | 任务详情：时间线/轮次/指令/终端快照、完成依据、追问与取消 |
| `/live/sessions` | 实时会话：托管/观察筛选、会话详情（turns + 事件） |

根路径会重定向到 `/live`；未匹配路由也回到 `/live`。侧栏第一项「对话」进入 Chat 页面。

## 登录

首次打开会显示登录页，粘贴中心管理令牌（默认在中心数据目录的 `admin-token` 文件）。登录后中心写入 HttpOnly 会话 Cookie，12 小时有效；写操作需要 `x-meerkat-request: 1` CSRF 头（API 层已处理）。

## 开发与构建

```bash
cd web
pnpm install
pnpm dev          # 本地开发
npx tsc --noEmit  # 类型检查
npx vite build    # 产出单文件 dist/index.html（vite-plugin-singlefile）
```

构建产物由中心静态托管（中心 `MEERKAT_WEB_DIST` 指向 `web/dist`）。

## 目录

```text
src/
├── App.tsx              路由（仅 /live 系列）
├── components/          布局、侧栏、指标图/环、状态徽章、shadcn/ui
├── data/types.ts        与中心 API 共用的类型
├── hooks/               移动端判断
├── lib/                 工具、主题、状态映射
├── live/                API 客户端、登录门、设备/任务/会话页面、时间线
└── main.tsx             入口
```
