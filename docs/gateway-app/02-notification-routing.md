# 02 · ntfy 与通知路由

## 1. 现象与实测

2026-10-25 检查结果：

| 检查 | 结果 |
|------|------|
| `https://ntfy.ttai.online/v1/health` | `200 {"healthy":true}` |
| `https://aicli.ttai.online/api/health` | `200`，网关在线 |
| `https://ntfy.ttai.online/aicli-gateway-0bf5wo3tkd/json?poll=1&since=24h` | 0 条消息 |
| `GET /api/push/config`（未登录） | `401`，无法读取当前部署配置 |

结论：**ntfy 服务端可达；问题在网关发布侧、路由策略或客户端订阅**。

## 2. 代码事实

### 2.1 当前 D6 路由

`backend/internal/notify/dispatcher.go`：

```text
high（approval/question/error） → 全部通道
normal（completed/waiting）     → 单主通道
low                             → 仅 inbox
```

`backend/cmd/gateway/push.go` 固定主通道：

```go
d.SetPrimary("webpush")
```

`backend/cmd/gateway/push_routing_test.go` 明确断言：

- `completed` → 只发 Web Push，不发 ntfy
- `approval` → Web Push + ntfy

因此，**如果测试的是任务完成，ntfy 不会收到任何消息**。这与
`docs/gateway/08-notifications.md` 中“Web Push 与 ntfy 并行扇出”的描述不一致；
以代码行为为准。

### 2.2 另一个静默丢失点

当 Web Push 已初始化、但没有任何订阅时：

- normal 仍然只选 `webpush`
- `WebPush.Deliver` 对 0 订阅静默成功
- 通知不会回退到 ntfy

这会让控制台看起来“通知已配置”，但手机完全收不到 normal 通知。

## 3. 修复策略

不直接把默认改成“normal 全通道扇出”，避免同一手机上的 Web Push 与 ntfy
重复响铃。新增显式配置，并保持旧行为为默认。

### 3.1 新增网关参数

```text
--notify-normal-channels webpush,ntfy
```

- 为空/未配置：沿用 D6 单主通道。
- 配置后：normal 按列表顺序投递；未知通道启动时告警并忽略。
- `high` / `low` 行为不变。
- 通道偏好（`notify-prefs.json`）仍然生效。

推荐部署值：

```text
--notify-normal-channels webpush,ntfy
```

如果暂时不再使用 Web Push，只保留：

```text
--notify-normal-channels ntfy
```

App 通道接入后改为：

```text
--notify-normal-channels apppush,webpush,ntfy
```

### 3.2 内核改动

`Dispatcher` 增加显式 normal 通道列表：

```go
d.SetNormalChannels("webpush", "ntfy")
```

解析顺序：

1. 显式 normal 列表（有效通道）
2. 旧的主通道
3. 首个注册通道

未配置时行为与现状一致，已有测试不改语义。

### 3.3 测试

新增：

- normal 显式多通道时，两个通道都收到。
- 重复通道名去重。
- 未知通道名被忽略，日志可见；若列表全部无效则回退主通道。
- 未配置时仍只走主通道（D6 行为不变）。
- `cmd/gateway` 集成：`--notify-normal-channels` 解析并注入。

## 4. 排查顺序

### 4.1 先点测试按钮

控制台“发送测试通知”走 `POST /api/push/test`，直接调用 ntfy Sender，
不经过 normal 路由。用于隔离：

| 返回 | 含义 |
|------|------|
| `ntfy.enabled=false` | 网关没带 `--ntfy-url/--ntfy-topic` |
| `ntfy.delivered=0` | 看 `ntfy.error`：token、权限、服务端拒绝 |
| `ntfy.delivered=1` 但手机不响 | 客户端订阅、Instant delivery、iOS upstream |
| 测试成功但完成通知没有 | normal 路由 / presence / 去重 |

### 4.2 服务端检查

```bash
systemctl show aicli-gateway -p ExecStart
journalctl -u aicli-gateway -n 200 --no-pager | grep -Ei 'ntfy|dispatch failed|push'
curl -s http://127.0.0.1:2586/v1/health
```

启动日志应包含：

```text
ntfy channel enabled: http://127.0.0.1:2586 topic=...
```

### 4.3 presence / 去重 / 免打扰

- 控制台正在看某会话 → presence 抑制，切走或等待 TTL（默认 90s）。
- 重启后旧 attention 被 seed，不会重推；要触发新状态变化。
- `notify-prefs.json` 中 `channel:ntfy` 静音或 `min_priority=high` 会拦截。
- quiet hours 只影响 normal；high 穿透。

### 4.4 客户端

- Android：服务器 `https://ntfy.ttai.online`，topic 完全一致；
  自建服务器需开启 *Instant delivery*，否则 FCM/Doze 可能延迟。
- iOS：自建 ntfy 需要 `upstream-base-url: https://ntfy.sh` 才能即时；
  否则可能延迟数小时。
- 当前 topic 未启鉴权，topic 名即口令；后续建议启用 token。

## 5. 验收

- [ ] 测试通知 `channels.ntfy.delivered=1`，手机收到。
- [ ] 新触发一次 `completed`，在配置 `webpush,ntfy` 后 ntfy 收到。
- [ ] `approval` 仍双通道投递。
- [ ] 未配置新参数时，现有 D6 测试全部通过。
- [ ] 控制台查看会话时，presence 仍能抑制通知。
