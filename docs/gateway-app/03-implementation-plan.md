# 03 · 实施计划

## 1. 目录规划

```text
backend/
  cmd/gateway/                 # 网关、通知路由、控制台
  internal/notify/             # 通知内核与通道

apps/
  gateway-app/                 # Tauri 2 全网页 App
    package.json
    ui/                        # 本地加载/错误壳
    src-tauri/
      src/lib.rs               # 创建远程 WebView + 原生能力
      capabilities/            # 只授权网关域名

docs/
  gateway-app/                 # 本专项方案与运维文档
```

App 代码独立于 `frontend/`，避免影响 runtime-server React 工作台。

## 2. 阶段

### Phase 0 · 方案与诊断（本次）

- [x] 建立 `docs/gateway-app/`
- [x] 记录 ntfy 实测结果
- [x] 明确完整 WebView + 原生通知桥架构
- [x] 明确安全边界与设备令牌分离

### Phase 1 · 通知路由修复（本次）

- [x] `notify.Dispatcher` 支持显式 `normal` 通道
- [x] 网关新增 `--notify-normal-channels`
- [x] 未配置时保持 D6 行为
- [x] 新增/更新单测
- [x] 部署文档给出 `webpush,ntfy` 配置

验收：

```bash
go test ./internal/notify ./cmd/gateway
```

### Phase 2 · Tauri 2 桌面 PoC（本次）

- [x] 创建 `apps/gateway-app`
- [x] 远程加载 `https://aicli.ttai.online/`
- [x] 支持 `AICLI_GATEWAY_URL` 覆盖
- [x] 配置最小远程 capability（仅通知）
- [x] Windows 构建通过（`tauri build --no-bundle`；单并发 + `debuginfo=0`）
- [x] 无环境变量启动通过（WebView2 数据目录自动回退 + 启动日志）

验收：

- App 打开后显示网关登录页或已登录仪表盘。
- 登录后可看到节点/会话，可打开会话面板。
- SSE、iframe、上传/下载、剪贴板做真机/实机检查。
- 外链不劫持主窗口。

本轮实测：

- WebView 实际加载 `https://aicli.ttai.online/`，标题与仪表盘正确。
- `window.__TAURI__.notification` 可用，原生通知调用返回 `sent`。
- WebView2 的 Web Push 订阅失败（`NotAllowedError`），因此桌面必须走原生通知桥。
- 详见 [05-verification.md](./05-verification.md)。

### Phase 3 · 原生通知桥

- [x] Tauri 原生通知权限与 `notification:default` 远程 capability 验证
- [x] 原生通知冒烟（`scripts/cdp-notify.mjs` → `sent`）
- [x] 前台 attention → Tauri 原生通知桥（控制台 v45；待部署线上控制台）
- [x] `POST /api/app/devices` 注册/列表
- [x] App token 存储与撤销（`app-devices.json`，0600）
- [x] `apppush` 通道骨架（受信任 relay webhook）
- [ ] iOS APNs / Android FCM / Android 国内 UnifiedPush（relay 侧实现）
- [ ] 通知点击深链到会话
- [ ] 前台 presence 抑制
- [ ] 角标计数

验收矩阵：

| 场景 | iOS | Android GMS | Android 无 GMS | Desktop |
|------|-----|-------------|----------------|---------|
| App 前台 | 本地通知/角标 | 本地通知/角标 | 本地通知/角标 | toast |
| App 后台 | APNs | FCM | ntfy/UnifiedPush/前台服务 | 托盘/本地连接 |
| 点击深链 | 是 | 是 | 是 | 是 |
| 正文隐私 | 不含正文 | 不含正文 | 不含正文 | 不含正文 |

### Phase 4 · 商店与分发

- [ ] iOS 证书/描述文件/APNs key
- [ ] Android 签名与 FCM/厂商通道
- [ ] Windows 安装器/签名
- [ ] macOS 签名与公证
- [ ] 崩溃/日志/版本策略
- [ ] 隐私说明（通知、WebView Cookie）

## 3. 技术验证清单

WebView 兼容性：

- [ ] owner 登录 Cookie 在重启后保留
- [ ] `/api/events` SSE 正常
- [ ] `/n/{node}/` iframe 正常
- [ ] `/web/api/input` 发送消息正常
- [ ] 文件上传/下载
- [ ] 复制/粘贴、软键盘、返回手势
- [ ] 证书错误/离线/登录过期错误态

原生桥：

- [ ] 通知权限拒绝后的引导
- [ ] token 轮换/注销
- [ ] 冷启动点击通知
- [ ] App 前台抑制
- [ ] 多设备 token 扇出

## 4. 已知风险

| 风险 | 处理 |
|------|------|
| 纯 WebView 套壳被 App Store 拒 | 加原生通知、角标、深链、离线收件箱、生物识别 |
| 远程页面 + Tauri IPC 被 XSS 利用 | capability 只到网关域名 + 只开通知命令 |
| iOS 后台长连接不可用 | 必须 APNs；不承诺 WebSocket 常驻 |
| 国内 Android 无 GMS | ntfy/UnifiedPush/前台服务，不只依赖 FCM |
| Web Push 与 ntfy 重复 | 默认单主通道；显式 fanout 时用通道偏好静音 |
| 多设备 normal 只发主通道 | 显式 normal 通道列表或后续 apppush 按设备路由 |
| 远程页面升级导致 App 兼容性回退 | WebView 只依赖稳定 API；关键操作保留网页错误态 |

## 5. 回滚

- 通知路由：删除 `--notify-normal-channels` 即恢复 D6。
- App：独立目录，不影响现有 PWA / 控制台构建。
- 推送：关闭 AppPush 通道，保留 Web Push / ntfy。
- 服务器：保留现有 `aicli-gateway` 二进制与 systemd 单元，灰度发布。
