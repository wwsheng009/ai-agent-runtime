# 01 · 目标架构

## 1. 分层

```text
┌─────────────────────────────────────────────────────────┐
│ Tauri 2 Shell                                           │
│                                                         │
│  ┌───────────────────────────────────────────────────┐  │
│  │ WebView: https://aicli.ttai.online/               │  │
│  │  - owner 登录（HttpOnly Cookie）                   │  │
│  │  - 节点/会话列表、聊天、审批、上传下载              │  │
│  │  - 现有 SSE / iframe / 反向代理                    │  │
│  └───────────────────────────────────────────────────┘  │
│                                                         │
│  Native Bridge                                          │
│   - 通知权限 / APNs / FCM / UnifiedPush                 │
│   - 通知点击 → 深链 → WebView 会话                      │
│   - 角标 / 前台 presence / 文件与外链                    │
│   - Windows 托盘、开机启动；移动端后台生命周期           │
└──────────────────────────┬──────────────────────────────┘
                           │ HTTPS
┌──────────────────────────▼──────────────────────────────┐
│ aicli-gateway                                            │
│                                                          │
│  owner auth ── console/PWA ── /api/* ── /n/{node}/...    │
│                                                          │
│  notify.Dispatcher                                       │
│    ├─ webpush（浏览器/PWA）                              │
│    ├─ ntfy（国内 Android / iOS 兜底）                    │
│    ├─ apppush（后续：APNs/FCM/UnifiedPush）              │
│    └─ IM（Telegram/飞书/QQ…）                            │
└──────────────────────────────────────────────────────────┘
```

## 2. 为什么是 Tauri 2

| 需求 | Tauri 2 的支持方式 |
|------|-------------------|
| 完整复用网站 | WebView 直接加载远程网关页面 |
| 桌面端 | Windows WebView2 / macOS WKWebView / Linux WebKitGTK |
| 移动端 | iOS WKWebView / Android WebView |
| 原生通知 | 官方 notification 插件覆盖桌面与移动的本地通知 |
| 系统能力 | Rust 命令 + 插件；可按域名限制远程页面权限 |
| 体积 | 使用系统 WebView，不打包 Chromium |

注意：

- Tauri 官方 notification 插件是**本地通知**；App 在后台/被杀后的远程推送
  仍需 APNs、FCM 或 UnifiedPush。
- 远程页面默认不能调用 Tauri API；必须通过 capability 的 `remote.urls`
  只授权网关域名和通知相关命令。
- iOS 的 WKWebView 不能假设具备 PWA 的 Web Push 能力；原生 App 必须走 APNs。

## 3. WebView 边界

### 允许

- 加载网关主域（默认 `https://aicli.ttai.online`）。
- owner 登录、Cookie 持久化、SSE、iframe 内反向代理页面。
- 受控的原生通知、角标、深链、文件下载、外链打开。

### 禁止

- 把 `owner-password` 写入 App 配置或 JS。
- 把 agent 的 `agw_` 设备令牌复用到 App。
- 对任意远程域开放完整 Tauri IPC。
- 在通知 payload 中放入会话正文。

### 安全建议

1. WebView 只允许网关域名导航；外链交给系统浏览器。
2. Tauri capability 的 `remote.urls` 精确到网关域名，不写 `*`。
3. 通知点击只接受网关生成的深链；拒绝任意 URL。
4. 退出登录时清理 WebView Cookie，并撤销 App 通知设备 token。
5. 通知内审批/回答必须加生物识别或二次确认，首期不做。

## 4. 认证与设备配对

推荐两条独立链路：

1. **网页会话**：App 打开网关登录页，owner 登录后使用 HttpOnly Cookie。
2. **通知设备**：登录成功后，App 调 `POST /api/app/devices` 注册
   `platform + push_token + label`，换取只用于通知的 App 设备 ID。

不要复用 `backend/internal/gateway/device` 的 agent 令牌。两者语义不同：

| 令牌 | 用途 | 权限 |
|------|------|------|
| `agw_*` | agent 隧道注册 | 节点/会话隧道 |
| App 设备令牌 | App 通知注册 | 仅注册/撤销自己的推送 token |

## 5. 通知通道模型

后续在 `internal/notify/channels/` 增加 `apppush` 通道：

```go
type AppPush struct {
    Devices AppDeviceStore
    APNs    APNsSender
    FCM     FCMSender
    Unified UnifiedPushSender
}

func (a *AppPush) Name() string { return "apppush" }
func (a *AppPush) Deliver(ctx context.Context, ev notify.Event) error { ... }
```

路由建议：

- `high`：仍全通道扇出。
- `normal`：按配置或设备偏好投递；App 安装后可将 `apppush` 作为主通道。
- `low`：只进入收件箱，不产生系统通知。
- App 前台/正在看某会话：presence 抑制该会话通知。

## 6. 移动端推送现实约束

| 平台 | 可靠后台通道 | 备注 |
|------|--------------|------|
| iOS | APNs | 必须 Apple Developer；不能靠长连接常驻 |
| Android（有 GMS） | FCM | 依赖 Google Play Services |
| Android（国内无 GMS） | ntfy / UnifiedPush / 前台服务 | 不能只依赖 FCM |
| Windows/macOS/Linux | 原生 toast + 本地连接 | App 运行时可用 SSE/WebSocket；托盘常驻 |

因此 App 的“完整网页操作”和“通知”是两层能力，不能因为 WebView 能打开网页
就假设后台推送自动可用。
