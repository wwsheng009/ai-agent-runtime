# aicli-gateway App（全网页容器 + 原生通知桥）

> 状态：方案已定稿，按阶段实施  
> 目标平台：Android、iOS、Windows、macOS、Linux  
> 首个可运行目标：Windows（Tauri 2 PoC），随后补移动端与原生推送

## 1. 一句话方案

不重写现有 Web 控制台。App 通过受控 WebView 加载完整控制台
（登录、节点、会话、聊天、审批、上传下载都复用网页），原生层只负责
通知权限、APNs/FCM/UnifiedPush、角标、深链、后台与系统能力。

```text
┌──────────────────────────────────────────────┐
│ 原生 App（Tauri 2）                           │
│                                              │
│  WebView ── https://aicli.ttai.online/       │
│      │       完整 Web 控制台                  │
│      │                                       │
│  原生桥 ── 通知 / 角标 / 深链 / 后台          │
└──────────────────┬───────────────────────────┘
                   │
┌──────────────────▼───────────────────────────┐
│ aicli-gateway                                │
│  notify.Dispatcher                           │
│    ├─ WebPush                                │
│    ├─ ntfy                                   │
│    ├─ AppPush（后续：APNs/FCM/UnifiedPush）   │
│    └─ IM                                      │
└──────────────────────────────────────────────┘
```

## 2. 关键决策

| 决策 | 结论 | 理由 |
|------|------|------|
| UI 形态 | **完整 WebView，不重写控制台** | 现有控制台已覆盖全部操作；重写成本高且容易漂移 |
| 全端框架 | **Tauri 2 优先** | 一套代码覆盖桌面 + 移动；系统 WebView；Rust 原生桥；体积小 |
| 手机推送备选 | **Capacitor** | 若 APNs/FCM 官方插件成熟度优先，移动端可改用 Capacitor；桌面仍用 Tauri |
| 通知通道 | **Web Push / ntfy / AppPush 并存** | 浏览器、国内 Android、原生 App 各有可用路径 |
| 普通通知路由 | 默认保持 D6 单主通道；新增显式覆盖 | 避免默认重复打扰；需要 ntfy 收 `completed` 时显式配置 |
| 认证 | WebView 登录 + HttpOnly Cookie | 不在 App 内硬编码 owner 密码 |
| 通知设备 | 新增独立 App 设备存储 | `agw_` 是 agent 隧道令牌，不能复用 |
| 推送正文 | 不携带会话正文 | 保持现有隐私边界；点击后按需拉取 |

## 3. 文档索引

| 文档 | 内容 |
|------|------|
| [01-architecture.md](./01-architecture.md) | 目标架构、WebView/原生桥边界、认证与安全 |
| [02-notification-routing.md](./02-notification-routing.md) | ntfy 现状、D6 路由问题、可配置 normal 通道 |
| [03-implementation-plan.md](./03-implementation-plan.md) | 分阶段实施、验收标准、目录规划 |
| [04-operations.md](./04-operations.md) | 部署、验证、排障与回滚 |
| [05-verification.md](./05-verification.md) | 本轮构建、运行、WebView2 与原生通知验证记录 |
| [06-apppush-relay.md](./06-apppush-relay.md) | App 设备注册、AppPush 通道与 relay webhook 契约 |
| [07-desktop-ux.md](./07-desktop-ux.md) | 桌面托盘、关闭到托盘、单实例、开机启动、通知点击与窗口状态 |

## 4. 当前代码事实

- 控制台已内嵌在网关二进制：`backend/cmd/gateway/console/` + `console.go`。
- PWA / Service Worker / Web Push 已实现：`console/sw.js`、`push.go`、`internal/gateway/push/`。
- ntfy 已实现：`internal/gateway/ntfy/ntfy.go`。
- 通知内核：`internal/notify/`，通道接口为 `notify.Channel`。
- App 设备注册：`internal/gateway/appdevice/` + `/api/app/devices`。
- AppPush 通道：`internal/notify/channels/apppush.go`（relay webhook）。
- 桌面壳：`apps/gateway-app/src-tauri/src/desktop.rs`（托盘、单实例、自启动、窗口状态、通知点击）。
- 当前 normal 路由为单主通道：`backend/internal/notify/dispatcher.go`。
- 当前主通道固定为 Web Push：`backend/cmd/gateway/push.go`。
- 线上 ntfy 健康，但近期 topic 无可查消息；详见 02 文档。

## 5. 非目标

- 不在首期重写 React/Vite 工作台。
- 不在首期实现完整原生聊天 UI。
- 不在首期实现通知内直接审批/回答；涉及锁屏误操作，后续需生物识别。
- 不把 owner 密码或本机令牌写入 App。
