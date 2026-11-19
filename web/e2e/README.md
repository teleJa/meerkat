# Meerkat 发布冒烟 E2E

## 前置条件

1. 已启动本机 Meerkat（Wails App）或等价的本地 UI 服务。
2. 本机 Hub 已有至少一台设备；DSH keep-alive 用例需要一台 `harness_available` 的设备。
3. 安装依赖与 Playwright Chromium。

```bash
cd web
pnpm install
pnpm exec playwright install chromium
```

## 运行

先取得本机 Meerkat UI 地址，例如 `http://127.0.0.1:53529`，再运行：

```bash
cd web
MEERKAT_E2E_BASE_URL=http://127.0.0.1:53529 pnpm test:e2e
```

只看浏览器窗口：

```bash
MEERKAT_E2E_BASE_URL=http://127.0.0.1:53529 pnpm test:e2e:headed
```

## 覆盖范围

- 主路由渲染与横向溢出检查；
- 一次性配对令牌：生成、复制、重新生成、命令不溢出；
- 实时任务：刷新、派发弹窗打开/关闭、无横向溢出；
- DSH iframe：切换路由后不重建。

## 不在本套件内

- 不创建真实 DSH 会话，不调用模型；
- 不提交派发任务，不注册新 Edge；
- 不点击退出；
- 不启动/停止 Wails、Hub、Edge 或 DSH。
