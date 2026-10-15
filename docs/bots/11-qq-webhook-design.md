# 11 · QQ Bot Webhook 事件订阅（方案设计 · 待评审）

> 上游：[04-migration-roadmap.md](./04-migration-roadmap.md)（QQ 官方能力对照，缺口矩阵最后一项 P2）、
> [06-target-architecture.md](./06-target-architecture.md)（D3：webhook 通道只允许公网宿主）、
> [03-host-integration.md](./03-host-integration.md)（宿主接线）。
> 状态：**方案设计（待评审）** ｜ 日期：2026-10-15 ｜ 官方依据：`bot.q.qq.com/wiki/develop/api-v2/dev-prepare/event-emit/webhook.html`
> 与 `interface-framework/sign.html`、官方 SDK `tencent-connect/botgo`（`interaction/signature`、`interaction/webhook`）。

## 1. 为什么需要单独方案

- **跨宿主**：按 D3，webhook 入口只允许公网宿主（gateway）暴露；QQ 是当前唯一
  「WS 长连接为主」的通道，引入 Webhook 后需回答**双通道关系**（共存/互斥），
  属决策类变更（08 §8）。
- **安全面**：官方验签是 Ed25519 + Bot Secret 派生种子的非常规算法，且回调
  验证（op 13）要求服务端**用私钥签名应答**——存在重放/伪造/挑战误答面，
  需安全评审（先例：飞书 challenge/encrypt_key）。
- **契约面**：`mode` 枚举扩展（`gateway` → `gateway|webhook`）、Schema 新增
  字段、三端渲染器零改动约束需在动工前锁定。

## 2. 官方契约要点（已核对，2026-10-15）

| 项 | 契约 |
|----|------|
| 回调地址 | HTTPS 强制；端口仅允许 80/443/8080/8443；在 q.qq.com 管理端配置并勾选监听事件 |
| 事件载体 | 与 WS dispatch 相同的 `Payload`（`op/t/d`）；HTTP Header 携带 `X-Signature-Ed25519`（hex，64 字节）+ `X-Signature-Timestamp` |
| 请求验签 | `seed = repeat(secret)` 截取 32 字节 → `ed25519.GenerateKey` 得公钥；`msg = timestamp + rawBody`；`ed25519.Verify(publicKey, msg, sig)`；`sig[63]&224 != 0` 视为非法 |
| 回调验证（op 13） | 平台先发 `{"op":13,"d":{"plain_token","event_ts"}}`；服务端用**派生私钥**对 `event_ts + plain_token` 签名，返回 `{"plain_token","signature"}` |
| 错误处理 | 验签失败应 401/403（官方未规定具体码；与飞书切片口径一致：验签失败 400） |
| UA 识别 | 验证请求带 `User-Agent: QQBot-Callback`、`X-Bot-Appid` 头（可用于路由前校验，不可作为鉴权依据） |

## 3. 目标架构

```
QQ 开放平台 ──HTTPS(webhook, Ed25519 签名)──▶ gateway:/hooks/connectors/{id}
                                               │ 验签（X-Signature-Ed25519/Timestamp）
                                               │ op13 → 挑战应答（直接 200 返回签名）
                                               │ op0+消息事件 → WebhookProvider.HandleWebhook
                                               ▼
                                    connectors.Service.HandleInbound（既有管线）
                                               ▼
                                    Router/Queue/Backend（runtime-server / aicli runner）
```

- **复用既有入口**：`/hooks/connectors/{id}` 命名空间与 `WebhookProvider` 能力接口
  已就绪（飞书切片验证过），QQ 只需实现 `HandleWebhook` + Schema `modes` 增加
  `webhook`；三端前端零改动。
- **单文件落点**：`internal/connectors/providers/qqbot/webhook.go`（验签 + 挑战 +
  事件归一）+ `gateway.go` 的 `GROUP_*/C2C_*` 解析复用（同一 `parseGatewayMessage`）。

## 4. 决策清单（评审点）

| # | 决策 | 建议 | 备选与理由 |
|---|------|------|------------|
| D1 | **双通道关系** | `mode` 二选一（`gateway`=WS 长连接，`webhook`=HTTP 回调），保存/切换时 Reload 生效；不做双活 | 双活会导致事件重复消费（去重账本可兜底但浪费）与归属语义复杂化；QQ 平台侧同一机器人可同时配置两者，需在文档明示"二选一" |
| D2 | **验签失败状态码** | 400（与飞书 encrypt_key/verification_token 失败口径一致） | 401/403（官方未规定；网关统一风格优先） |
| D3 | **op 13 挑战应答位置** | `WebhookResultProvider.ParseWebhookResult` 直接应答（同飞书 challenge），不经队列 | 走队列会引入异步延迟，平台验证要求即时响应 |
| D4 | **Secret 派生缓存** | 私钥/公钥按 `app_secret` 派生后进程内缓存（与 token 缓存同风格）；secret 变更时缓存失效 | 每请求重复派生成本可接受但无意义；缓存键含 secret 哈希避免旧值 |
| D5 | **时间戳防重放** | `X-Signature-Timestamp` 与本机时间偏差 > 5 分钟拒绝（可配 `webhook_timestamp_skew`） | 官方未强制；QQ 事件本身携带 event_ts，二次校验成本低 |
| D6 | **事件类型过滤** | 仅处理 `GROUP_AT_MESSAGE_CREATE`/`GROUP_MESSAGE_CREATE`/`C2C_MESSAGE_CREATE`（与 WS 一致）；其余 op0 事件 200 吞掉（记 debug） | 平台按管理端勾选推送；未勾选事件到达属配置漂移，不应报错刷屏 |
| D7 | **宿主范围** | 仅 gateway 暴露 `/hooks/connectors/{id}`（D3）；runtime-server/aicli 的 webhook 模式可用但 Schema Help 注明"需公网可达，建议配 gateway" | 与飞书现状一致（两端已注册 provider，入口在 gateway） |
| D8 | **配置迁移** | 既有 `mode=gateway` 连接器零影响；`webhook` 模式复用 `app_id/app_secret`，新增可选 `webhook_timestamp_skew` | 无需新密钥；管理端回调 URL 手工配置（`https://<public-base-url>/hooks/connectors/<id>`，详情页展示复制） |

## 5. 实施切片（评审后执行）

| 切片 | 内容 | 验收 |
|------|------|------|
| W1 | `webhook.go`：seed 派生（含缓存）+ 验签 + op13 挑战 + 事件解析（复用 `parseGatewayMessage`）；Schema `modes` 增 `webhook` | 假平台 `httptest`：验签通过/篡改 body/缺失头/op13 正确应答/op0 消息入站归一；`go test -race` 全绿 |
| W2 | gateway 宿主接线核对（`/hooks/connectors/{id}` 已有，无需改）+ 三端 Schema 渲染确认 + 04 矩阵回写 | 契约矩阵 golden 不变（actions/modes 快照更新走 `-update` 流程）；真机：平台配置回调 → 挑战通过 → IM 消息往返 |
| W3 | 08 运维章节补 webhook 故障处置（验签失败排查/时钟偏差/平台回调状态） | 文档审计通过 |

## 6. 与既有文档的关系

- 04 矩阵「Webhook 事件订阅」行：实现后回写 ✅ 并引用本文；
- 06 D3：webhook 仅公网宿主——本文 D7 与之一致，无冲突；
- 03 §6 API 契约：`/hooks/connectors/{id}` 已登记，无需新增路由；
- 08 §4 密钥轮换：QQ 无 webhook 专属密钥，沿用 `app_secret` 轮换口径。

## 7. 明确不做（NG）

- 不做 WS+Webhook 双活（D1）；
- 不做平台回调 URL 的自动注册（QQ 管理端手工配置，官方无 API）；
- 不做 webhook 专属的独立限流（复用网关限流与去重账本）；
- 频道类事件（频道消息/论坛/表情）不在 webhook 首批（与 WS 同口径，按需扩展）。
