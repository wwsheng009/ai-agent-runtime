# aicli-gateway App Shell

Tauri 2 全网页容器。运行时直接加载 `https://aicli.ttai.online/`，登录、
会话、聊天、审批和文件操作全部复用现有 Web 控制台；Rust 层负责窗口、
通知权限和后续原生推送/深链。

## 运行

```powershell
cd apps/gateway-app
npm install
npm run dev
```

覆盖网关地址：

```powershell
$env:AICLI_GATEWAY_URL = "https://aicli.ttai.online/"
npm run dev
```

原生通知冒烟测试（Windows 开发态可能显示为 PowerShell 通知，安装后为应用通知）：

```powershell
$env:AICLI_NOTIFICATION_SMOKE = "1"
npm run dev
```

程序会自动选择可写的 WebView2 数据目录；如需显式覆盖：

```powershell
$env:AICLI_WEBVIEW_DATA_DIR = "E:\tmp\aicli-gateway-webview"
npm run dev
```

启动失败时查看 `%TEMP%\aicli-gateway-app.log`。程序会自动尝试
`%LOCALAPPDATA%\aicli-gateway\webview2`、`%TEMP%` 和 exe 同目录的
`.webview2-data`；详细排查见 `docs/gateway-app/04-operations.md`。
日志写入失败时会回退到 exe 同目录或当前目录的 `aicli-gateway-app.log`。

## 构建

```powershell
$env:CARGO_BUILD_JOBS = "1"
$env:RUSTFLAGS = "-C debuginfo=0"
npm run build -- --no-bundle
```

安装包构建（需要平台签名配置）：

```powershell
npm run build
```

## 桌面体验

- 系统托盘菜单：显示/隐藏、重新加载、发送测试通知、开机启动、
  关闭到托盘、退出。
- 点击窗口关闭按钮默认隐藏到托盘；首次隐藏会提示一次。
- 第二次启动会唤醒已有窗口，不会产生第二个常驻进程。
- 窗口尺寸/位置自动恢复；最小尺寸 `900×600`。
- 通知点击会唤醒窗口，并优先跳到通知携带的 node/session。
- “开机启动”使用系统自启动项，并以 `--minimized` 启动到托盘。

启动到托盘：

```powershell
apps\gateway-app\src-tauri\target\release\aicli-gateway-app.exe --minimized
```

桌面偏好保存在应用配置目录的 `desktop-settings.json`；完整行为见
[docs/gateway-app/07-desktop-ux.md](../../docs/gateway-app/07-desktop-ux.md)。

## 安全边界

- WebView 只允许导航到 `AICLI_GATEWAY_URL` 的主机。
- 仅对 `https://aicli.ttai.online` 开放 `notification:default`；
  修改网关域名时必须同步修改
  `src-tauri/capabilities/gateway-remote.json`。
- 不保存 owner 密码；登录 Cookie 由 WebView 管理。
- WebView2 的 `PushManager.subscribe()` 实测失败（`NotAllowedError`），
  桌面通知必须走 Tauri 原生通知；浏览器 PWA 仍使用 Web Push。
- App 内“开启应用通知”和 attention 原生通知要求线上控制台为 **v45**；
  部署旧 `app.js` 时按钮仍会显示“开启通知”。
- 首期未实现后台远程推送；iOS 需 APNs，Android 需 FCM/UnifiedPush。

## 验证脚本

```powershell
node scripts/cdp-probe.mjs 9222      # 页面 URL / Tauri API / PushManager
node scripts/cdp-notify.mjs 9222     # 原生通知，成功输出 sent
node scripts/cdp-push-subscribe.mjs 9222  # 诊断 WebView2 Web Push 限制
```

详细设计与阶段见 `docs/gateway-app/`。
