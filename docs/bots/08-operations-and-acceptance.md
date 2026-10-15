# 08 · 运维手册与验收矩阵

> 上游：[07-config-ux.md](./07-config-ux.md)（配置面）、[05-review-and-hardening.md](./05-review-and-hardening.md) §2（可靠性与 SLO）、
> [docs/gateway/15-multi-instance-architecture.md](../gateway/15-multi-instance-architecture.md)（多实例边界）

## 1. 运行形态与启停

| 动作 | 入口 | 说明 |
|------|------|------|
| 全局开关（功能级回滚） | 宿主配置 `connectors.enabled=false` | 关闭后进程不加载任何 Provider、不监听 webhook、不启轮询；**行为回到未接入前** |
| 单连接器启停 | UI 开关 / `PATCH /api/connectors/{id}` `{"enabled":false}` | 只影响该通道；保留配置与历史 |
| 运行归属 | `runtime_owner: auto|aicli|runtime-server|gateway` | 同一轮询/WS 通道只允许一处运行；`auto` 先到先得，后启动者只读 |
| 抢占 | UI「抢占」/ `POST /api/connectors/{id}/takeover` | 要求对方停机确认；用于切换宿主 |
| 宿主重启恢复 | 自动 | 轮询游标（Telegram offset / 微信 buf / QQ session+seq）持久化；入站账本未完成项重放；微信 context_token 快照随状态存储保留（重启后无需等下一条入站即可回复，切片 2d）；出站死信继续重试 |

## 2. 日常巡检

### 2.1 指标（前缀见 06 D10）

| 指标 | 含义 | 建议阈值/告警 |
|------|------|----------------|
| `connectors_up{provider}` | 连接器是否启用且健康（1/0） | 期望 1；0 持续 2min 告警 |
| `connectors_inbound_total{provider,result}` | 入站消息（ok/dup/rejected/error） | `error` 比率 > 5% 告警 |
| `connectors_outbound_total{provider,result}` | 出站（ok/retry/failed） | `failed` 增长告警 |
| `connectors_queue_depth{connector_id}` | 每会话队列深度 | 持续接近上限告警（背压前兆） |
| `connectors_queue_dropped_total` | 丢弃（delta 为主） | 突增需关注（可能 agent 输出过快） |
| `connectors_poll_lag_seconds{provider}` | 轮询延迟（now - 最后消息时间） | > 60s 告警 |
| `notify_dispatch_total{channel,result}` | 通知投递（各通道） | 失败率 > 10% 告警 |
| `notify_dedup_hits_total` | 跨通道去重命中 | 用于验证"只响一次" |

### 2.2 巡检命令

```bash
# 列表与状态（管理面鉴权按宿主：gateway=owner cookie；runtime-server=Bearer；aicli=本机会话）
curl -s $BASE/api/connectors | jq
curl -s $BASE/api/connectors/$ID | jq '{status,last_error,last_activity,queue_depth}'
curl -s "$BASE/api/connectors/$ID/logs?limit=50" | jq
# 一键测试与自检
curl -s -X POST $BASE/api/connectors/$ID/test | jq
curl -s $BASE/api/connectors/$ID/diagnostics | jq
```

### 2.3 状态灯判据

| 灯 | 条件 |
|----|------|
| 🟢 正常 | `enabled=true` 且最近 5min 无错误、轮询延迟 < 60s、webhook 最近回调成功 |
| 🟡 降级 | 有可恢复错误（限流/瞬时失败）或轮询延迟超阈，但仍在工作 |
| 🔴 异常 | token 失效 / 被踢下线 / 连续失败熔断 / webhook 校验持续失败 |

## 3. 常见故障与处置

| 通道 | 现象 | 可能原因 | 处置 |
|------|------|----------|------|
| 通用 | 列表显示"未运行（由 X 持有）" | 运行归属在另一宿主 | 确认对方停机后「抢占」 |
| 通用 | 同一提醒在手机与桌面各响一次 | 未接 `notify` 中台/去重账本未共享 | 检查 `notify_dedup_hits_total`；按 05 §4 接入 |
| 通用 | agent 卡顿/主循环被拖慢 | 事件订阅未走 `SubscribeAsync`（P-1 项） | 启用异步订阅；检查队列深度 |
| Telegram | `409 Conflict` | 双消费者（多实例/多宿主同时轮询） | 修正运行归属；只留一个 owner |
| Telegram | `401 Unauthorized` | bot token 失效/被重置 | @BotFather 重新签发 → 详情页粘贴保存 |
| Telegram | webhook 设置失败 | 公网 URL/证书/反代不可达 | 先回退 polling；修 `--public-base-url` 与 edge |
| 飞书 | challenge 校验失败 | verification token 或回调 URL 不匹配 | 对照开放平台配置；重新保存触发注册 |
| 飞书 | 事件重复触发 | 平台重试 + 账本缺失 | 入站账本去重；确认账本持久化 |
| QQ | WS 频繁重连 / InvalidSession | 网络抖动 / intents 或沙箱配置错误 | 查看 session_id+seq 游标与重连日志 |
| QQ | 图片/文件收不到（仅 `[图片: 名称]` 占位、无 `local_path`） | 入站下载失败（出口守卫拦截/超限/附件签名 URL 过期） | 查看连接器日志「入站媒体下载失败」；附件 URL 带签名且短期有效，需及时下载 |
| QQ | 回复里的图片/文件没发出去 | 指令块名或路径不对（需绝对路径或 https URL） | 查看日志「发送媒体失败」与降级提示 `[附件发送失败: …]`；确认 ```qq-attachments``` 围栏或 `IMAGE:/FILE:` 指令行 |
| 微信 | `errcode -14`（会话失效） | 登录态过期 | 详情页「重新扫码」 |
| 微信 | `errcode -2`（上下文失效） | 会话上下文过期（真机形态：`ret=-2 errcode=0 errmsg="prepare failed"`，常见于重新扫码/登录态更换后） | 发送被拒时自动清除该会话已失效的 context_token（内存 + 持久化快照，避免后续出站反复撞墙），并自动保存失败快照（切片 2f）；等下一条入站重新记忆后恢复；用户在 IM 内发「重发/继续/retry」即用新会话上下文重放；仍失败会提示「重发失败，请稍后重试」 |
| 微信 | 图片/语音收不到（仅 `[Image/Voice attachment]` 块、无 `local_path`） | 媒体下载失败（CDN 不可达/密钥不符/未配置 `cdn_base_url`） | 查看连接器日志 `inbound media download failed`；检查 CDN 基址与网络 |
| 微信 | 语音落盘为 `.silk` 无法播放 | 未安装 silk 解码器/ffmpeg | 安装 ffmpeg 或设置 `AICLI_WECHAT_SILK_DECODER`；转码失败会保留原件 |
| 出站 | 429/Retry-After 频现 | 触发平台限速 | 降低连接器/用户限速；确认退避生效 |
| 流式 | 消息被频繁编辑/乱序 | 编辑节流未生效 | 检查 ≥1s 且内容变化才编辑的策略 |

### 3.1 密钥轮换

| 通道 | 步骤 |
|------|------|
| Telegram | BotFather `/revoke` → 新 token → 详情页粘贴 → 自动重连 → 发测试 |
| 飞书 | 开放平台重置 app_secret → 粘贴保存 → 重新注册 webhook → challenge 自检 |
| 微信/QQ | 「重新扫码/重新授权」；新凭据生效后才替换旧凭据（避免中途失效） |

## 4. 消息重放与修复

| 场景 | 操作 |
|------|------|
| 入站丢失/失败 | `POST /api/connectors/{id}/inbound/{message_id}/replay`（要求账本保留消息体，默认保留 24h） |
| 出站死信 | `POST /api/connectors/{id}/outbound/{dedup_key}/retry`；或详情页「重试」按钮 |
| 会话错乱 | 详情页解绑/重绑；IM 内 `/new` 开新会话 |
| 队列堆积 | 提升限速或降低 delta 频率；必要时临时关闭"流式"改一次性回复 |

## 5. 备份、迁移与回滚

| 层次 | 手段 | 说明 |
|------|------|------|
| 配置备份 | 导出（脱敏/含密钥，见 07 §5.4） | 建议随宿主配置目录一起纳入备份（gateway `data-dir`、`~/.aicli/config.yaml` + `connectors.secrets.yaml`） |
| 迁移 | 导入导出 + 运行归属抢占 | 跨宿主/跨机 |
| 数据回滚 | 账本可不备份（幂等重建）；死信建议保留 | 账本丢失只影响重放能力 |
| 功能回滚 | `connectors.enabled=false` | 立即停用全部连接器，webhook 返回 503（可配置为 200 忽略） |
| 版本回滚 | 回退宿主二进制 + 保留配置 | 配置向前兼容（schema_version 未知字段忽略） |

## 6. 容量与限速参数速查

| 参数 | 默认 | 调整入口 |
|------|------|----------|
| 每会话队列上限 | 32 | 连接器策略 |
| 每会话空闲回收 | 2min | 全局默认 |
| 流式合并窗口 | 800ms（且内容变化才发） | 连接器策略 |
| 出站重试 | 3 次指数退避，尊重 Retry-After | 全局默认 |
| 每用户限速 | 10 msg/min | 连接器策略 |
| 单会话并发 turn | 1 | 全局（不可配） |
| 入站账本保留 | 24h | 全局默认 |
| 媒体大小上限 | 100MB（下载/上传硬上限；连接器级策略可配待续） | 全局（媒体切片 2c 现状） |
| QQ 主动消息预检 | 10 qps / 1000 条·日·会话（0 = 不限制） | 连接器设置 `proactive_qps` / `proactive_daily_limit` |
| QQ 被动回复次数预检 | 单聊 4 次 / 群 5 次·条消息（本机计数，达上限改主动） | 内置（官方上限；`WithPassiveReplyLimits` 可覆盖） |
| Telegram 媒体上限 | 20MB（官方 `getFile` 限制） | 内置（`WithInboundMediaLimit` 可覆盖） |

## 7. 验收矩阵

### 7.1 阶段 × 能力

| 能力 | P-1 | P0 | P1 | P2 | P3 |
|------|-----|----|----|----|----|
| 事件异步订阅（不阻塞主循环） | ✅ 必验 | — | — | — | — |
| notify 中台（跨通道去重） | ✅ 必验 | — | ✅ 三端偏好页 | — | ✅ IM 通道静音 |
| 契约包 + 三宿主矩阵 | ✅ 骨架 | ✅ 扩展 | ✅ 全量 | — | — |
| Telegram 收/回/流式 | — | ✅ | — | — | — |
| 飞书 webhook + 审批闭环 | — | — | ✅ | — | — |
| gateway 隧道转发 | — | — | ✅ | — | — |
| aicli 本地 + 配置共享 | — | — | — | ✅ | — |
| QQ WS 断线续传 | — | — | — | ✅ | — |
| 微信扫码 + 媒体 | — | — | — | — | ✅ |

### 7.2 通道 × 能力矩阵

| 能力 | Telegram | 飞书 | QQ | 微信 |
|------|----------|------|----|------|
| 文本收/回 | ✅ | ✅ | ✅ | ✅ |
| 流式编辑 | ✅ | ✅（可更新卡片 PATCH；Provider 开关 `streaming=true`，默认关闭需真机验证） | 追加式 | 追加式（前缀提交） |
| typing | 表情/typing | 表情回复 | 输入状态（仅单聊） | typing ticket |
| 审批/提问命令 | ✅ | ✅（含卡片事件） | ✅ | ✅ |
| 媒体（图文/语音/文件） | ✅（图片/视频/语音/音频/文件；入站 `getFile` 下载、出站 multipart/URL/`file_id`） | ✅（图片/文件/语音/视频；入站资源下载、出站 multipart 上传 + 对应 `msg_type`） | ✅（图片/视频/语音/文件；入站 `local_path`、出站指令协议、远端 URL 直传、大文件分片上传、临时目录 24h 清扫） | ✅（入站下载/解密/语音转码；出站 CDN 上传、远端页面解析/清单转码） |
| 二维码/授权登录 | — | — | 可选 | **扫码登录** |
| webhook 模式 | ✅ | ✅（+WS 可选） | — | — |
| 长轮询/WS 模式 | ✅ | ✅(WS) | ✅ | ✅ |
| 断线续传（游标） | offset | —— | session+seq | get_updates_buf |

### 7.3 安全用例

- [ ] 未签名/错误签名的 webhook → 401/403，且不触发任何 turn。
- [ ] 重放同一平台事件（相同 message_id）→ 只处理一次（账本命中）。
- [ ] 白名单外用户 → 首次提示后静默，且不进入 agent。
- [ ] `GET /api/connectors*` 与前端 store 无明文密钥（自动化断言）。
- [x] 媒体 URL 指向内网/本机 → 被 SSRF 防护拒绝并记日志（2026-10-14 微信出站远端媒体；2026-10-15 飞书出站远端媒体同口径：仅 http(s)，默认拒绝回环/私网/链路本地/未指定/组播/CGNAT，重定向逐跳校验，拒绝时降级文本提示且零上传；用例 `TestRemoteMediaFetchBlocksPrivateHostByDefault` / `TestSendMediaByURLBlocksPrivateHost`）。QQ 的 URL 直传与下载路径已有宿主守卫；Telegram 远端 URL 由平台拉取（runtime 不下载，无 SSRF 面）。
- [ ] 非法命令（`/approve` 非本人请求）→ 拒绝并提示。
- [ ] 限速超限 → 提示一次 + 静默 5min，不产生 turn。

### 7.4 多前端一致性

| 检查 | 方式 |
|------|------|
| 三端渲染同一 Schema | JSON 快照测试（schema 变更需评审 diff） |
| 三宿主 API 行为一致 | 契约测试矩阵（CI 门禁） |
| 未知字段类型降级 | 注入假字段类型，三端不崩溃 |
| 移动端可用 | 375px 下完成：列表/启停/扫码/测试 |

### 7.5 "未配置连接器"回归

- [ ] 三宿主既有测试全绿（`cmd/gateway`、`cmd/aicli`、`cmd/runtime-server` 相关包）。
- [ ] 无 `connectors` 配置时：不监听 `/hooks/connectors/*`、无轮询 goroutine、无新增指标噪声。
- [ ] `connectors.enabled=false` 后 30 秒内全部通道停止（webhook 返回可配置状态码）。

## 8. 文档维护约定（防漂移）

| 变更类型 | 需同步的文档 |
|----------|--------------|
| 连接器接口/字段（Schema） | 02 §3、03 §5-6、07 §3-4、08 §6 |
| 通知通道/去重/偏好 | 02 §5、05 §4、06 §1（D6/D9）、08 §2 |
| 事件/订阅语义 | 05 §2、06 §1（D9）、`internal/events/contract.go` |
| 宿主装配/存储位置 | 03 §2-4、07 §5、08 §5 |
| 部署/多实例/防双写 | gateway 15 号文档、08 §1/§5 |
| 通道能力变化（如飞书卡片） | 08 §7.2、02 附录（如有） |

> 约定：文档头部标注"状态/日期"；**决策类变更必须先改 06 §1 再改实现文档**，避免多文档各说各话。
