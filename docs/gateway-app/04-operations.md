# 04 · 部署与排障

## 1. 网关配置

推荐在 systemd `ExecStart` 或 `--config` flagfile 中加入：

```text
--ntfy-url http://127.0.0.1:2586
--ntfy-topic <topic>
--notify-normal-channels webpush,ntfy
--app-push-relay-url https://relay.example.com/push
--app-push-relay-token <relay-token>
```

说明：

- `ntfy-url` 走本机回环，不经过 Cloudflare。
- 手机客户端订阅公网 `https://ntfy.ttai.online` + 同一 topic。
- 只有希望 `completed/waiting` 也发到 ntfy 时才配置
  `--notify-normal-channels`；不配置则保持旧的单主通道行为。
- 如果 topic 启用鉴权，网关需要 `--ntfy-token`；客户端也需要 token。
- `--app-push-relay-url` 为空时 `apppush` 不注册，但 App 设备注册 API 仍可用。
- relay URL 必须 HTTPS；仅 loopback 开发允许 HTTP。

## 2. 重启与检查

```bash
systemctl daemon-reload
systemctl restart aicli-gateway
systemctl status aicli-gateway
journalctl -u aicli-gateway -n 100 --no-pager
```

应看到：

```text
ntfy channel enabled: http://127.0.0.1:2586 topic=...
```

## 3. 分通道测试

控制台登录后点击“发送测试通知”，或：

```bash
curl -sS -X POST -b cookies.txt \
  https://aicli.ttai.online/api/push/test | jq
```

预期：

```json
{
  "channels": {
    "webpush": { "enabled": true, "delivered": 1, "subscriptions": 1 },
    "ntfy": { "enabled": true, "delivered": 1, "topic": "..." }
  }
}
```

注意：测试接口直接调用发送器，不经过 normal 路由，因此它只能证明
“网关到 ntfy 服务端”链路。`completed` 是否能到 ntfy，取决于
`--notify-normal-channels`。

App 设备注册与 `apppush` relay 契约见
[06-apppush-relay.md](./06-apppush-relay.md)。

## 3.1 桌面 App

Windows 发布版默认关闭窗口到系统托盘，托盘菜单提供显示/隐藏、重新加载、
测试通知、开机启动和退出。第二次启动会唤醒已有实例。

```powershell
# 正常启动
apps\gateway-app\src-tauri\target\release\aicli-gateway-app.exe

# 直接启动到托盘（开机启动使用同一参数）
apps\gateway-app\src-tauri\target\release\aicli-gateway-app.exe --minimized

# 结束正在运行的实例
apps\gateway-app\src-tauri\target\release\aicli-gateway-app.exe --quit
```

排障：

- 托盘图标不出现：查看 `%TEMP%\aicli-gateway-app.log` 中的
  `failed to create system tray`。
- 关闭窗口后找不到：托盘菜单“显示主窗口”；若托盘创建失败，应用会退化为
  普通关闭，不会隐藏窗口。
- 通知点击未跳转：确认通知 `extra` 含 `node_id`/`session_id`，且地址属于
  `AICLI_GATEWAY_URL` 主机。
- 自启动设置：开发构建不写系统启动项；发布版菜单勾选状态来自系统真实状态。

详细交互与验收见 [07-desktop-ux.md](./07-desktop-ux.md)。

## 4. ntfy 自检

```bash
# 服务端
curl -s http://127.0.0.1:2586/v1/health

# 公网
curl -s https://ntfy.ttai.online/v1/health

# 最近消息（不打印正文，只看条数）
curl -s 'https://ntfy.ttai.online/<topic>/json?poll=1&since=30m' | wc -l
```

### Android

- 服务器地址不要带 topic。
- topic 必须与网关配置完全一致。
- 自建服务器打开 *Instant delivery*。
- 关闭对 ntfy 的电池优化。

### iOS

- 自建 ntfy 若需即时通知，配置：

  ```yaml
  upstream-base-url: "https://ntfy.sh"
  ```

- 未配置时通知可能延迟数小时，这是 iOS 后台限制，不是网关丢消息。

## 5. 常见故障

| 现象 | 排查 |
|------|------|
| `ntfy.enabled=false` | 网关缺少 `--ntfy-url/--ntfy-topic`，或 flagfile 未加载 |
| `ntfy.delivered=0` | 看 `error`；token 失效/服务端 401/403/404 |
| 测试成功，completed 不响 | 配置 `--notify-normal-channels webpush,ntfy` |
| 所有通知都不响 | presence 抑制、quiet hours、去重、notify-prefs |
| 手机完全不响 | topic/服务器错误、Instant delivery、iOS upstream |
| Web Push 0 订阅 | normal 会静默丢失；改用 ntfy 列表或设置 AppPush 主通道 |

## 6. App 构建与运行

Windows PoC：

```powershell
cd apps/gateway-app
npm install
npm run tauri dev
```

远程地址覆盖：

```powershell
$env:AICLI_GATEWAY_URL = "https://aicli.ttai.online/"
npm run tauri dev
```

构建：

```powershell
npm run tauri build -- --no-bundle
```

移动端需要额外安装 Android Studio / Xcode，并初始化 Tauri 移动工程；
本轮只验证桌面 PoC，不把“未实机验证”写成已完成。

### 运行探测与原生通知

带 WebView2 远程调试启动：

```powershell
$env:AICLI_WEBVIEW_DATA_DIR = "E:\tmp\aicli-gateway-webview"
$env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = "--remote-debugging-port=9222"
.\src-tauri\target\release\aicli-gateway-app.exe
```

```powershell
node scripts/cdp-probe.mjs 9222      # URL/标题/Tauri API/PushManager
node scripts/cdp-notify.mjs 9222     # 原生通知，成功输出 sent
```

实测注意：Tauri WebView2 虽然暴露 `PushManager`，但 `subscribe()` 返回
`NotAllowedError`。因此：

- 浏览器/PWA：继续使用 Web Push。
- 桌面 App：使用 Tauri 原生通知桥，不依赖 Web Push。
- 移动 App：iOS APNs；Android FCM/UnifiedPush/ntfy。

### 启用 App 前台原生通知

1. 部署网关控制台 v45（`app.js?v=45` / Service Worker v45）；
2. 打开 Tauri App 并登录；
3. 侧栏按钮应显示“开启应用通知”或“关闭应用通知”；
4. 首次使用按提示授予系统通知权限；
5. 点击“发送测试通知”，应出现系统通知；
6. 触发一次新的 attention（审批/提问/完成），应同时出现页面 toast 和系统通知。

如果按钮仍显示“开启通知”而不是“开启应用通知”，说明线上控制台还是旧
`app.js`，需要先部署 v45。

### 程序无法启动

Release 构建是 Windows GUI 子系统，双击启动时不会弹出控制台。启动过程会写入：

```text
%TEMP%\aicli-gateway-app.log
```

如果 TEMP 不可写，会依次尝试：

1. exe 同目录的 `aicli-gateway-app.log`
2. 当前工作目录的 `aicli-gateway-app.log`

WebView2 数据目录按以下顺序选择：

1. `AICLI_WEBVIEW_DATA_DIR`（显式覆盖）
2. `%LOCALAPPDATA%\aicli-gateway\webview2`
3. `%TEMP%\aicli-gateway\webview2`
4. exe 同目录 `.webview2-data`
5. 当前工作目录 `.webview2-data`

排除启动问题：

```powershell
# 1. 看日志
Get-Content "$env:TEMP\aicli-gateway-app.log" -Tail 50

# 2. 显式指定可写数据目录
$env:AICLI_WEBVIEW_DATA_DIR = "E:\tmp\aicli-gateway-webview"
.\src-tauri\target\release\aicli-gateway-app.exe

# 3. 确认 WebView2 Runtime 已安装
Get-ChildItem "${env:ProgramFiles(x86)}\Microsoft\EdgeWebView\Application"
```

常见原因：

- WebView2 Runtime 未安装；
- 默认数据目录无写权限（受限账户/沙箱）；
- 网关地址非法（`AICLI_GATEWAY_URL` 不是绝对 http(s) URL）；
- 远程 capability/网关域名不匹配（不会导致窗口创建失败，但会禁用 Tauri API）。

## 7. 安全

- 生产必须 HTTPS；不要用 `--allow-unprotected`。
- WebView 只允许网关主域；外链交系统浏览器。
- 远程 capability 只授权通知相关命令。
- App 不保存 owner 密码；退出登录清 Cookie。
- 通知 payload 不含正文。
- 当前 ntfy topic 未启鉴权，建议后续启用 token。
