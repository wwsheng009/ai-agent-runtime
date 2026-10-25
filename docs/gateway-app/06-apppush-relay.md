# 06 · App 设备注册与 AppPush Relay

## 1. 设计

网关不保存 APNs/FCM 私钥。它把“设备 token + 通知元数据”发给一个受信任
relay，由 relay 使用 APNs/FCM/UnifiedPush 完成系统推送。

```text
App
  ├─ 登录网关（owner Cookie）
  ├─ 获取 APNs/FCM/UnifiedPush token
  └─ POST /api/app/devices
          ↓
    app-devices.json（0600）
          ↓
  notify.Dispatcher → apppush → relay webhook
          ↓
    APNs / FCM / UnifiedPush
```

为什么用 relay：

- 网关的公网攻击面不额外持有 Apple/Google 私钥；
- relay 可以独立扩缩容、灰度、替换推送供应商；
- 国内 Android 可以在 relay 侧切换 UnifiedPush/厂商通道；
- 后续也可以把 relay 换成进程内直连 APNs/FCM，而不改网关 API。

## 2. 网关配置

```text
--app-push-relay-url https://relay.example.com/push
--app-push-relay-token <bearer-token>
--notify-normal-channels apppush,webpush,ntfy
```

- relay URL 必须 HTTPS；仅 loopback 开发允许 HTTP。
- token 建议放进 `--config` flagfile。
- `apppush` 只有配置 relay 后才会注册到 Dispatcher。
- 如果只希望 App 作为主通道，可使用 `--notify-normal-channels apppush`。
- AppPush 不依赖 Web Push；即使 `--push-enabled=false` 且未配置 ntfy，
  只要 relay 和 App 设备存在，attention 事件仍会投递。

## 3. 设备注册 API

均需要 owner session（浏览器/App 已登录）。

### POST `/api/app/devices`

```json
{
  "platform": "android",
  "token": "fcm-or-apns-or-unifiedpush-token",
  "label": "vince-phone"
}
```

- `platform`: `ios` / `android` / `windows` / `macos` / `linux`
- 相同 token 重注册为 upsert，保留 device ID。
- 返回 `201`（新建）或 `200`（更新）。
- 响应只返回 `token_hint`，不返回完整 token。

### GET `/api/app/devices`

```json
{
  "count": 1,
  "devices": [
    {
      "id": "…",
      "platform": "android",
      "label": "vince-phone",
      "token_hint": "fcm-to…abcd",
      "created_at": "…",
      "updated_at": "…",
      "last_seen": "…"
    }
  ]
}
```

### DELETE `/api/app/devices/{id}`

撤销一个 App 设备。App 退出登录时应调用。

## 4. Relay Webhook 契约

网关向 `--app-push-relay-url` 发送：

```http
POST /push
Content-Type: application/json
Authorization: Bearer <app-push-relay-token>
```

```json
{
  "device": {
    "id": "…",
    "platform": "android",
    "token": "provider-token",
    "label": "vince-phone"
  },
  "notification": {
    "title": "会话",
    "body": "待审批",
    "tag": "node|session",
    "url": "https://aicli.ttai.online/n/node/?session=session",
    "urgency": "high",
    "priority": "high",
    "kind": "approval",
    "dedup_key": "node|session|approval",
    "node": "node",
    "session": "session",
    "ttl_seconds": 600
  }
}
```

Relay 返回：

| 状态 | 网关行为 |
|------|----------|
| `2xx` | 成功 |
| `404` / `410` | 删除该 device token |
| `401` / `403` | 记录 relay 凭证错误，不删除设备 |
| 其它 | 记录错误，不重试 |

通知正文不包含会话内容；点击 URL 由 App 打开后再按 owner 会话拉取。

## 5. App 端注册流程

```text
1. App 启动并登录 owner
2. 获取系统推送 token
3. POST /api/app/devices
4. 保存返回的 device id（可选，用于退出时 DELETE）
5. 退出登录：DELETE /api/app/devices/{id}
```

桌面 Tauri 目前使用原生本地通知，不需要远程 token；移动端接入 APNs/FCM 后
再调用此 API。不要在 App 中保存 owner 密码。

## 6. 测试

```bash
go test ./internal/gateway/appdevice
go test ./internal/notify/channels
go test ./cmd/gateway
```

Relay 可以用 `httptest` 或本地 HTTP 服务模拟；生产必须 HTTPS。
