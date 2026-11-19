# Meerkat tsnet 原型

验证目标：不安装 Tailscale 客户端，把 Tailscale 节点直接嵌进 Meerkat 进程。

## 运行

```bash
# Hub 节点
go run . hub

# 另开终端，边看状态边登录
open http://127.0.0.1:17443/

# 授权完成后，把状态里的 Tailscale IP 传给 Edge
go run . edge --hub <hub-tailnet-ip>:7443
```

状态：

- 本地状态页：`http://127.0.0.1:17443/`
- Hub tailnet 监听：`:7443`
- 状态目录：`~/.meerkat/tsnet-proto/hub`、`~/.meerkat/tsnet-proto/edge`
- 首次运行会显示 Tailscale 登录 URL；登录一次后会持久化节点状态。

## 已实现

- [x] tsnet.Server 嵌入 Hub 进程
- [x] 本地状态页显示 state / auth_url / tailnet IP
- [x] 授权后 tsnet Listen Hub HTTP 服务
- [x] Edge 节点模式，通过 tsnet.HTTPClient 访问 Hub
- [x] macOS / Windows 二进制构建
- [x] 不依赖系统 tailscaled、无 root

## 体积

- macOS 原型：约 29MB
- Windows 原型：约 31MB
- 相比现有 Hub 23MB / Edge 10MB，预计正式集成增加 20–30MB。

## 已知限制

- 首次仍需 Tailscale 登录；tsnet 不能代替账号/tailnet。
- 系统已安装 Tailscale 时会产生一个独立 tsnet 节点。
- 需要 tailnet ACL 允许 Hub/Edge 互通。
- 目前是独立原型，尚未接入 Meerkat 的 Settings、Hub、Edge 生命周期。
- 本地状态页只监听 127.0.0.1。
