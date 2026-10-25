# 05 · 本轮验证记录

> 日期：2026-10-25  
> 环境：Windows、Rust 1.95.0、Tauri 2.12.5、WebView2 155、Node 24

## 1. 网关通知路由

```bash
cd backend
go test ./internal/notify ./cmd/gateway -count=1
```

结果：

```text
ok github.com/wwsheng009/ai-agent-runtime/internal/notify
ok github.com/wwsheng009/ai-agent-runtime/cmd/gateway
```

新增行为：

- 默认 normal 仍走 D6 单主通道（webpush）。
- 配置 `--notify-normal-channels webpush,ntfy` 后，completed/waiting
  同时投递到 Web Push 与 ntfy。
- 未注册通道会告警；全部无效时回退主通道。

## 2. Tauri 构建

```powershell
cd apps/gateway-app
$env:CARGO_BUILD_JOBS = "1"
$env:RUSTFLAGS = "-C debuginfo=0"
npm run build -- --no-bundle
```

结果：

```text
Finished `release` profile [optimized] target(s)
Built application at:
apps/gateway-app/src-tauri/target/release/aicli-gateway-app.exe
```

说明：首次并行构建遇到 Windows 链接器 `LNK1171 / 1455`（分页文件/内存压力），
改为单并发并关闭调试信息后成功。这不是源码错误。

后续因 `lto=true + codegen-units=1` 在 16GB 机器上触发
`rustc-LLVM out of memory`，release profile 已改为：

```toml
codegen-units = 16
lto = false
opt-level = 2
panic = "abort"
strip = true
```

这是小体积壳应用，优先保证构建可靠性；最终 exe 仍会 strip。

## 3. 运行验证

沙箱用户无法写 `%LOCALAPPDATA%`，测试时使用：

```powershell
$env:AICLI_WEBVIEW_DATA_DIR = "E:\projects\ai\ai-agent-runtime\apps\gateway-app\.webview2-data"
$env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = "--remote-debugging-port=9222"
apps\gateway-app\src-tauri\target\release\aicli-gateway-app.exe
```

CDP 探测：

```powershell
cd apps/gateway-app
node scripts/cdp-probe.mjs 9222
```

实测结果：

| 项 | 结果 |
|----|------|
| WebView URL | `https://aicli.ttai.online/` |
| 页面标题 | `aicli-gateway 控制台` |
| 页面内容 | 仪表盘、节点、会话、审批入口均可见 |
| Tauri 全局 API | `window.__TAURI__` 存在 |
| 通知 API | `window.__TAURI__.notification` 存在 |
| Service Worker | 已注册 |
| Web Notification 权限 | `granted` |
| Web PushManager | 存在 |

这说明**完整网页操作路径成立**：App 是现有控制台的 WebView 容器，而不是重写 UI。

## 3.1 无环境变量启动（修复后）

实际双击/无环境变量启动失败时，Release 构建不会显示控制台，根因是
Tauri/WebView2 默认数据目录不可写（`os error 5`）。修复后程序会：

1. 尝试 `AICLI_WEBVIEW_DATA_DIR`；
2. 尝试 `%LOCALAPPDATA%\aicli-gateway\webview2`；
3. 尝试 `%TEMP%\aicli-gateway\webview2`；
4. 回退到 exe 同目录 `.webview2-data`；
5. 最后回退当前目录 `.webview2-data`。

修复后**不设置 `AICLI_WEBVIEW_DATA_DIR`** 启动实测：

```text
Process: aicli-gateway-app
MainWindowTitle: aicli-gateway
Responding: True
CDP URL: https://aicli.ttai.online/
CDP Title: aicli-gateway 控制台
Body: Owner 登录页（全新数据目录）
```

启动日志记录了实际回退目录；在受限账户下最终使用 exe 同目录
`.webview2-data`。正常用户环境通常使用 `%LOCALAPPDATA%`。
在登录页状态下再次执行 `scripts/cdp-notify.mjs` 仍返回 `sent`，
说明通知能力不依赖 owner 登录。

## 4. Web Push 与原生通知

在 Tauri WebView2 中手动执行 PushManager 订阅：

```text
NotAllowedError: Registration failed - permission denied
```

结论：

- WebView2 虽然暴露 `PushManager` 和 `Notification`，但不能依赖它完成
  Web Push 订阅。
- 桌面 App 必须使用 Tauri 原生通知桥；浏览器 PWA 的 Web Push 链路保持不变。
- 移动端同理：iOS 必须 APNs，Android 需要 FCM/UnifiedPush/ntfy。

原生通知冒烟：

```powershell
node scripts/cdp-notify.mjs 9222
```

结果：

```text
sent
```

说明 `notification:default` 远程 capability 已生效，网关域名页面可以调用原生通知 API。

## 4.1 前台 attention 原生通知桥

控制台 v45 已接入：

- 检测 `window.__TAURI__.notification`；
- App 内“开启通知”改为原生权限流程，不再尝试 Web Push；
- fresh attention 的 `showAttentionToast()` 同时调用原生通知；
- “发送测试通知”在 App 内改为原生通知测试；
- 偏好存 `localStorage["aicli-gw-native-notify"]`。

静态测试：

```text
go test ./cmd/gateway -run TestConsoleNativeNotificationBridge
ok
```

注意：Tauri App 当前加载线上 `https://aicli.ttai.online/`。必须部署
控制台 v45 后，这条桥接才会在真实 App 里生效。部署前，线上仍运行旧
`app.js`，不会触发原生 attention 通知。

## 4.2 通知策略与独立配置页（v46）

本轮新增 `<data-dir>/notify-policy.json` + owner 保护的
`GET/PUT /api/notify/policy` + 控制台独立「通知设置」页，并让原生通知桥
先读策略再弹窗。

单测（干净 HEAD worktree + 本轮改动；工作区因无关 stash 冲突不可整体编译）：

```text
go test ./internal/notify/... -count=1
ok  github.com/wwsheng009/ai-agent-runtime/internal/notify
ok  github.com/wwsheng009/ai-agent-runtime/internal/notify/channels

go test ./cmd/gateway -count=1
ok  github.com/wwsheng009/ai-agent-runtime/cmd/gateway

go vet ./cmd/gateway ./internal/notify/...
ok
```

真实进程 API 冒烟（临时 data-dir、127.0.0.1:2599、owner 登录）：

```text
POST /api/auth/login                      → 200
GET  /api/notify/policy                   → 200 meta.source=default, 5 kinds, 3 channel rows
PUT  /api/notify/policy                   → 200 meta.source=file；落盘 notify-policy.json
外部改文件 allow_kinds/quiet_hours         → 下一次 GET 立即读到新值（热加载，无重启）
写入非法 JSON                              → GET warning=parse ...；policy 仍为最后一次成功值
GET  /                                    → index.html 含 data-view="notifications"
GET  /app.js                              → 含 /api/notify/policy 与 notifyPolicyAllowsAttention
GET  /sw.js                               → CACHE aicli-gateway-shell-v46
```

**已部署（2026-10-25，HEAD `8914e759`，sha256 `a0a85967…c3c527c`，备份
`gateway.bak-20261025-140810`）**：公网 `app.js?v=46` / `style.css?v=46` /
`sw.js` CACHE v46 已生效，未认证 `GET /api/notify/policy` 返回 401；
Tauri App 从线上控制台加载，独立配置页与策略门现已对真实 App 生效
（真机弹窗验证见 §5）。

## 4.3 通知链路修复（v47，端到端实测）

“真实注意力没有通知”由两个叠加故障造成：

1. 控制台 **v40 多视图重构丢了 `trackAttention` 的全部调用点**（只剩函数定义）——
   fresh attention 不再触发 toast 与 Tauri 原生通知，只有首屏 seed；
2. 网关 normal（completed/waiting）默认只走 Web Push，而 `push.json` 订阅数为 0，
   ntfy 只收 high —— 手机端收不到完成类通知。

修复与验证：

- 控制台在 `renderAll` 与增量 `patchSession` 恢复调用，资源版本 v47，新增
  `TestConsoleAttentionTrackingWired` 防回归；
- 服务端 unit 追加 `--notify-normal-channels webpush,ntfy`（unit 备份
  `aicli-gateway.service.bak-20261025-142415`），日志确认
  `normal notification channels: webpush,ntfy`；
- 部署 HEAD `2ba529eb`，sha256 `cb868fda…8ccdc8a92e`，备份
  `gateway.bak-20261025-143355`；
- 桌面端用 CDP 向运行中的 WebView 注入合成 session 增量，Windows 通知历史实测出现
  `node-… · 通知链路测试 / 出错`、`… · 通知链路测试2 / 已完成` 两条真实事件。

语义提醒：App 重载后的首屏只做 seed（已存在的注意力不补弹，只有加载后的新变化通知）；
浏览器 / PWA 需要重新“开启通知”建立 Web Push 订阅，桌面 App 走原生桥不依赖订阅。

## 5. 尚未验证

- 聊天面板发送、文件上传/下载、软键盘与返回手势（移动端）。
- 通知点击深链、冷启动深链。
- APNs/FCM/UnifiedPush 真实凭据与后台投递。
- iOS/Android 真机构建与商店签名。
- 控制台 v45 部署后的“真实 attention → 系统通知”真机验证。
- 控制台 v46 部署后的“策略关闭 completed → App 不弹窗”真机验证。

这些属于 Phase 3/4，不应在本轮标记为完成。

## 6. App 设备注册与 AppPush 骨架

```bash
go test ./internal/gateway/appdevice ./internal/notify/channels ./cmd/gateway -count=1
```

结果：

```text
ok internal/gateway/appdevice
ok internal/notify/channels
ok cmd/gateway
```

并发检查：

```text
go test -race ./internal/gateway/appdevice ./internal/notify/channels
go test -race ./cmd/gateway -run 'TestAppDevicesAPI|TestPushNotifierAppPushChannel'
ok
```

已覆盖：

- 设备 token 注册/upsert/撤销、JSON 持久化、token hint 脱敏；
- `/api/app/devices` 的 GET/POST/DELETE 与错误路径；
- `apppush` 通道 fan-out、失效 token 清理、错误聚合；
- App-only 网关（`--push-enabled=false`，无 ntfy）仍能唤醒并投递 `apppush`；
- relay webhook 请求体、Bearer 鉴权、404/410 映射；
- gateway 显式 `normalChannels=apppush` 与 high fan-out。

尚未覆盖：

- 真实 relay 服务；
- APNs/FCM/UnifiedPush 凭据与投递；
- App 启动时自动注册 token；
- 通知点击深链。

真实网关进程 API 验证（临时 data-dir、`--push-enabled=false`、owner 登录）：

```text
login=true
POST /api/app/devices -> 201, token_hint=e2e-se…-123
GET  /api/app/devices -> count=1
DELETE /api/app/devices/{id} -> deleted=true
GET  /api/app/devices -> count=0
```

返回体未包含完整 token。
