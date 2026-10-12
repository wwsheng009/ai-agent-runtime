# 审核记录：gateway console 时效性优化实施计划（P0/P1/P2）

> 被审文档：[gateway-console-timeliness-implementation-plan-20261012.md](./gateway-console-timeliness-implementation-plan-20261012.md)
> 审核方式：AI 自审（对照代码逐条核验）＋ **待用户批准** ｜ 日期：2026-10-12
> 结论：**待批准**（通过后按计划 §7 任务清单实施）

---

## 1. 核验清单（声明 → 证据 → 结论）

| # | 计划中的声明 | 代码证据 | 结论 |
|---|------------|---------|------|
| V1 | SSE 连接即发全量 nodes，变更时重发；空闲仅 20s `: ping` | `backend/cmd/gateway/main.go:465-509` | ✅ 属实 |
| V2 | hub 每订阅者 `chan(1)` 非阻塞信号（合并的是"信号"，每次唤醒仍重发全量快照） | `main.go:43-75` | ✅ 属实（措辞已按此理解） |
| V3 | registry 变更 → `OnRegistryChanged` → `hub.notify()` + `notifier.observe()` | `main.go:318-321` | ✅ 属实 |
| V4 | heartbeat digest **仅真变化**才广播+持久化；`LastHeartbeat` 更新本身不广播 | `tunnel/server.go:299-320`、`server.go:174-181` | ✅ 属实（G2 根因成立） |
| V5 | agent 端 **未消费** `HeartbeatAck.Pending`（P1-b 需新增） | `agent/wsconn.go:496-499`（仅 `recordGatewayLiveness()`） | ✅ 属实 |
| V6 | presence：TTL 默认 90s，控制台 30s 上报，现仅用于抑制 Web Push | `cmd/gateway/main.go:185`、`presence.go`、`push.go:100`、`app.js::setInterval(sendPresence, 30000)` | ✅ 属实 |
| V7 | `registry.json` 序列化全量 Node.Sessions → `StatusTail` 会随快照落盘 | `tunnel/persist.go:62-72`（`json.MarshalIndent(listFunc(), …)`） | ✅ 属实（R3 成立，需在 persist 裁剪或接受） |
| V8 | 宿主已有 **1s** dynamic-status tick（活动期运行） | `chat_interaction.go:1437`（`delay = time.Second - elapsed%time.Second`） | ✅ 属实（**修正后**表述见 §2-1） |
| V9 | aicli 存在可用 tail 来源（`streamBuffer`） | `chat_interaction.go:121`（`streamBuffer strings.Builder`，稳定提交路径已读取） | ✅ 可用（R2 降级不再必需） |
| V10 | `wire` 尚无 `synced_at` / `status_tail` 字段 | `rg` 无匹配（新字段） | ✅ 属实 |
| V11 | `HeartbeatAck.Pending` 已存在 | `wire/messages.go:150-153` | ✅ 属实（行号已修正） |
| V12 | 控制台现有 5s elapsed tick、HH:MM 时间、空 error 回调、无 last_heartbeat 渲染 | `app.js`（`refreshStatusElapsed` / `timeOf` / `subscribeEvents`） | ✅ 属实（G1–G4 根因成立） |

## 2. 已修正项（写入被审文档）

1. **宿主 tick 频率**：原文"宿主已有 5s surface tick，新增 1Hz 活动 tick"→ 改为"宿主已有 **1s** dynamic-status tick，**复用之**（无需新增定时器）；tick 关闭的宿主（管道模式）退回 surface 迁移时推送"。修正位置：P1 §4.2。
2. **tail 取用点**：由"候选（取用点不明）"明确为 **首选 `streamBuffer.String()` 尾部窗口**（`chat_interaction.go:121`），备选 `activeStream` 渲染缓存。修正位置：P1 §4.2。
3. 锚点行号修正：`HeartbeatAck` `wire/messages.go:111-115` → `:150-153`（P1 §4.5）。

## 3. 语义确认（重要，避免实现歧义）

- **`synced_at` 语义 = "网关最后一次收到该会话任何帧的时间"**：由于心跳 digest 每 20s 覆盖全部会话，其常态值 ≤20s（"同步 5s 前"），掉线后自然增大。这**不是**"内容变化时间"（后者是 `updated_at`）。UI 需同时展示两者，避免误读。
- **meta tick 只调用 `hub.notify()`，不调用 `notifier.observe()`**（避免周期性 Web Push 噪音）。
- **tail 与正文边界**：tail ≤512 runes，与既有 `last_message`（2000 runes）同级；完整正文仍在会话页（维持 07-console §6）。
- **性能预算**：单次全量快照 ≈20KB（13 节点）；meta tick 30s × 订阅者 ≈ 可忽略；P1 尾流在高频场景需按 §4.3 评估后再决定是否上 per-session 增量事件。

## 4. 待用户决策项（审核输入）

| # | 决策点 | 备选 | 建议 |
|---|--------|------|------|
| D1 | meta tick 间隔 | 30s / 60s / 关闭 | **30s**（PWA 移动端流量低，先 30s 观察） |
| D2 | P1 上线方式 | P1-a（无门控直推）先上 / 直接做 P1-b（presence 门控） | **P1-a 先上**；P1-b 视流量数据补 |
| D3 | `StatusTail` 是否落盘（registry.json） | 落盘 / persist 时裁剪 | **裁剪**（持久化前置空，控制快照体积） |
| D4 | 控制台 tail 默认开关 | 默认开（仅展开渲染）/ 默认关 | **默认开**（只有展开卡片渲染，带宽影响小） |
| D5 | P2 事件条范围 | 含历史面板 / 仅"刚刚"轻量条 | 轻量条（历史面板延后） |

## 5. 覆盖性检查（对照用户三问）

| 用户诉求 | 计划覆盖 |
|---------|---------|
| 看不到实时消息与状态 | L0 状态已是事件驱动（≤2s 目标验证 P0-8.1）；L1 活动脉冲 + P1-a tail（展开渲染）；`status_text`/elapsed 已有 |
| 不知道上次会话上传同步的时间 | P0：`synced_at`（网关盖章）+ 相对时间 + 心跳可见 + meta tick；P2：详情元数据块补 age/attention 时间 |
| 优化建议 | 已按 P0/P1/P2 分层并给出协议、节流、门控、验收、回退 |

## 6. 审核结论

- 自审未发现与现有机制冲突的硬伤；全部改动为 **additive**（wire 新字段 / 新 flag / 新 SSE event 类型），具备逐阶段回退能力；
- 3 处修正已回写被审文档；
- **待用户批准** D1–D5 后，按计划 §7 任务清单开始实施（建议顺序：P0 → 部署实测 → P1-a → P1-b（可选）→ P2）。

---

## 7. 第 2 轮审核（v2：四维补充与决定项确认）

> 触发：用户指示「按最佳实践确认，另外需要考虑 aicli/runtime-server 共用核心、性能问题、及时性、交互性多个维度」。
> 处理：计划新增 **§10 四维专项设计**，D1–D5 按最佳实践取默认值定稿；对 §2/§4 做三处回写。

### 7.1 决定项确认（D1–D5 定稿）

| # | 决策 | 定稿结论 | 落点 |
|---|------|---------|------|
| D1 | meta tick 间隔 | **30s**（`--event-tick`，`0`=off；只 `hub.notify()`，不触发 push 通知） | 计划 §3.2 / §10.5 |
| D2 | P1 上线方式 | **P1-a 先行**（tail + `event: session` 增量），P1-b（presence 门控）随后按流量数据评估 | §10.5 |
| D3 | `StatusTail` 落盘 | **persist 前复制裁剪**（内存保留、`registry.json` 不含 tail） | §4.3 / §10.2.2 |
| D4 | tail 默认开关 | **默认开**（仅展开卡片渲染；用户可关，prefs 记忆） | §10.4 |
| D5 | P2 事件条范围 | **仅轻量"刚刚"条**（历史面板延后） | §10.4 / §5 |

### 7.2 四维覆盖核验（v2 新增内容）

| 维度 | v2 新增设计 | 关键决策/机制 |
|------|------------|--------------|
| **共用核心** | §10.1：`SessionState.Tail/TailAt` + `SetTail` + `Apply()` 统一 512-rune 截断；`TailThrottle.Allow`（1s + 变化检测）；watch 门控进 `Controller.ApplyPending/Watched`——**算法/语义只写一遍**，宿主只映射事件 | 与已落地 `SessionStateStore` 分工一致；测试纪律=语义测在核心、映射测在宿主 |
| **性能** | §10.2：`event: session` **从可选提升为 P1 必选**（1Hz 全量快照 20KB/s → 增量 ~1.5KB/s，≈12× 降幅）；体积/内存预算表（tail ≤512 runes、digest 不含 tail、persist 裁剪、SSE drop-oldest + 30s 对账兜底）；渲染只重建 1 卡/秒 | 慢订阅者一致性由 meta tick 对账兜底 |
| **及时性** | §10.3：端到端时延分解（宿主 tick ≤1s + 节流 ≤1s + 网络 + apply <5ms + SSE <100ms）；SLO 定为**状态 ≤2s、tail P50 ≤1.5s / P95 ≤3s**（跨代理口径）；度量用 T0→T1(`synced_at`)→T2(SSE)→T3(DOM)；乱序防护：**digest 不再覆盖 `StatusText/StatusTail`** | 时钟统一以网关为准；相对时间显示允许本地时钟差 |
| **交互性** | §10.4：tail 跟随/暂停/跳到底部、开关与 prefs、空态/降级态、事件条点击定位、`aria-live` 策略、三类时间 tooltip 定义 | 降级路径显式：旧 agent 无 tail 不报错 |

### 7.3 对原计划的回写（已完成）

1. §2.2 L1 行补充"经 `event: session` 增量广播"；
2. §4.3 改写为 **P1 必选改造**（含成本对比）+ D3 定稿表述；
3. §4.6 / §7 任务清单：新增共享核心三件套、网关增量事件、digest 乱序防护；tail 验收口径改为 P50/P95；
4. 头部状态由"待审核"改为**"已审核确认"**。

### 7.4 结论

- 计划 v2 已覆盖用户点名的四个维度，并保持**全部 additive**（新字段/新回调/新 SSE event/新 flag）与逐阶段回退能力；
- **审核确认完成，进入实施**：按 §7 顺序 P0 → 部署实测 → P1-a → P1-b（可选）→ P2。

---

## 8. 实施记录：P0（2026-10-12，已完成并部署验证）

**代码变更**

- `wire.SessionSummary.SyncedAt`（网关盖章元数据）；
- `tunnel`：`stampSessionSynced` / `stampAllSessionsSynced` / `stampDigestSessions` 接入 REGISTER / SESSION_UPDATE(upsert,reconcile) / 心跳 digest；**盖章刻意不入变更检测**（心跳不会因时间流逝触发广播/持久化）；
- `cmd/gateway`：`--event-tick`（默认 30s）周期性 `hub.notify()`（只刷新 SSE 快照，不喂 push 通知）；
- `console`：`timeAgo`/`setRelTime`/`refreshRelativeTimes`（5s 本地 tick）、卡片"内容 x 前 + 同步 x 前"、节点"心跳 x 前"、`.age-stale`（>60s）、SSE 断线横幅 + 重连 `loadNodes()` 对齐 + `visibilitychange` 陈旧对齐、详情弹窗新增"同步"行；`sw.js` v22。

**测试**

- `tunnel/synced_at_test.go`：盖章规则 + "盖章不产生可见变更" + "digest 合并不自动盖章"（新增，通过）；
- jsdom `console_ux_dom_test.js`：新增第 7 组"新鲜度"断言（内容相对时间 / 同步标签 / stale 阈值 / 节点心跳 / 横幅 error→open 开关 / 重连触发全量重取）——全部通过；
- `go test ./internal/gateway/tunnel/ ./cmd/gateway/` 通过；`node --check` 通过。

**部署与实测（192.210.174.189）**

- 二进制 sha `d52b8d3b…` 部署并 active（新备份留存）；
- API 实测：**5/5 会话**带网关盖章 `synced_at`，live 节点 `last_heartbeat` 持续推进；
- 页面实测（`tmp/gw_probe_p0.json`）：默认 5 卡 / 0 离线 / 表格 477px；`同步 13s 前 → 18s 前`（**本地 tick 推进得到实测证明**，anyAdvanced=true）；断线横幅常态隐藏、详情含"同步"行；移动端工具栏 74px 回归通过。

**结论**：P0 验收达成（同步时间可见、相对时间自动推进、心跳可见、断线可见）。**下一步：P1-a**（共享核心 `SessionState.Tail`+`TailThrottle`、宿主 tail 接入、`event: session` 增量、展开卡片渲染）。

---

## 9. 实施记录：P1-a（2026-10-12，已完成并部署验证）

**共享核心（唯一实现，两宿主共用）**

- `SessionState.Tail/TailAt` + `SetTail()` + `Apply()` 统一 **512-rune 截断**（保留最新 runes），`BeginTurn/EndTurn` 清空；
- `TailThrottle`：1s 窗口 + 内容未变不推 + `Forget()`；单测覆盖截断方向、tail 生命周期、节流边界。

**宿主映射**

- **aicli**：复用现有 **1s** dynamic-status tick 作为采样点（`streamBuffer` 尾段 → 共享 throttle → store），活动迁移时也刷新；无活动/未变化零推送；
- **runtime-server**：`assistant_delta` / `assistant.delta` 事件 → 2048-rune 累积窗 → 共享 store/throttle；turn 起止/中断清空缓冲。

**网关**

- `tunnel.Config` 新增 `OnSessionChanged`（单会话增量）/`OnRegistryTouch`（喂 push notifier，不做全量广播）；
- `applySessionUpdate` 分流：upsert/reconcile 无结构变化 → 逐会话 delta；`remove`/reconcile 丢会话 → 全量快照兜底；
- `hub` 升级为 `hubEvent{full|session}`（drop-oldest，latest-wins）；SSE 新增 **`event: session`**（`{node_id, session}`）；
- **digest 不再覆盖 `StatusText/StatusTail`**（乱序防护，单测钉住）；tunnel 测试覆盖 delta 路径与 digest 边界。

**控制台**

- `event: session` → `patchSession()`（单会话 patch：`updateStats` + `renderInbox` + `renderSessionTable`，不触发整表全量重取）；
- 展开卡片新增"实时输出"块（monospace、8em 滚动、**跟随中/跳到底部**、字符数）；工具栏"实时输出：开/关"（prefs 持久化、参与渲染签名）；`sw.js` v23。

**实测（部署 192.210.174.189，sha `29d46392…`）**

- 端到端 tail 时延（真实页面 + 代理，harness 每 5s 推带 epoch 标记）：
  `[4133(附着期), 1089, 470, 1236, 398] ms` → 稳态 **P50≈0.78s / 最大 1.24s**，满足 P50 ≤1.5s、P95 ≤3s；
- `/api/nodes` 数据面校验：活动期 `status_tail`/`state`/`synced_at` 正确，turn 结束后 tail 按设计清空；
- 测试设备（revoke+purge）、节点（remove）、本地令牌文件全部清理。

**结论**：P1-a 验收达成。余项：P1-b（presence 门控，可选，按流量数据评估）与 P2（同步元数据块 + 迁移事件条）。

---

## 10. 实施记录：P2（2026-10-12，已完成并部署验证）

**网关**

- `wire.SessionSummary` 新增 `StateChangedAt` / `AttentionChangedAt`（**网关盖章**，agent 值被覆盖）：
  - `applySessionUpdate`（upsert/reconcile）与 `applyHeartbeatDigests` 两条来源统一检测迁移：字段变化 → 盖 `now`，未变化 → 保留旧时间戳（新会话两项都盖 now）；
  - `applyHeartbeatDigests` 签名升级为 `(changed bool, transitions []sessionTransition)`，digest 路径与 update 路径共同产出迁移；
- `tunnel.Config.OnSessionTransition` + `hub.publishTransition` → SSE 新增 **`event: session-event`**（`{node_id, session_id, state, attention, ts}`，轻量、不含快照）。

**控制台**

- 顶部"刚刚"事件条：最多 10 条 / 10 分钟（5s tick 剪枝），chip = 会话标题 + 状态/注意标签 + 相对时间；
  点击定位（当前筛选隐藏时自动切到「全部」）并脉冲卡片；一键清空；`aria-live=polite`；
- 详情弹窗新增"状态变更 / 注意力变更 / 节点心跳"三行网关时间（与"同步/最近更新"共同构成元数据块）；
- `sw.js` v24。

**测试**

- tunnel：`heartbeat_digest_test` 重写（迁移返回、StateChangedAt 盖章与保留、StatusText 不被 digest 覆盖）；`synced_at_test::TestApplySessionUpdateDeltaPath` 增补迁移断言（upsert 新会话 / reconcile 变化 / 删除不产生迁移）；
- jsdom：新增 7b 组（事件条出现/标签/点击脉冲/清空；详情三行非空）；
- `go build ./...` + `go test ./internal/gateway/... ./cmd/gateway/` 全绿。

**实测（部署 192.210.174.189，sha `e75dc033…`）**

- 真实页面 + harness（running→answering→completed 迁移）：事件条出现 **"tail e2e · 已完成 刚刚"**；点击 chip → 对应卡片被脉冲定位（`pulsedCard="tail e2e"`）；
- 测试设备（revoke+purge）/节点（remove）/本地令牌文件已清理。

**结论**：P2 验收达成。计划 §7 中 P0 / P1-a / P2 全部勾选；仅剩 **P1-b（presence 门控，可选）**——当前增量路径已比全量快照省 ≈12×，建议按实际流量数据再评估是否实施。

---

## 11. 实施记录：P1-b（2026-10-12，已完成并部署验证）

**协议与共享核心**

- `POST /api/presence` 请求体新增 `tail bool`：网关 presence 表按条目记录 `tail` 标志
  （`Active()` 语义不变——任何 presence 都抑制推送；新增 `TailWatched(nodeID)` 供门控使用）；
- `Controller.ApplyPending(actions)` 消费 `HEARTBEAT_ACK.pending`：
  `tail-gating:on`（网关通告参与门控）/ `watch:<sid>` / `unwatch:<sid>`；`Controller.TailWanted(sid)`
  三态语义：**未通告前恒为 true（P1-a 兜底）**，通告后仅 watch 集合内放行；
- `WSConnector.OnPendingActions` → readLoop 解析 HeartbeatAck 并回调（旧网关不下发 ⇒ 不回调 ⇒ 无门控）。

**网关**

- 每条 agent 连接维护 `watchSent` 差集：`watchActions(desired, sent)`（排序、确定性）→ 随心跳 ack 下发；
  首次 ack 附带 `tail-gating:on`；desired = presence(`tail=true`) ∩ 该节点活跃会话；
- tunnel `Config.TailWatchTargets` 注入；nil 时完全关闭门控（老行为）。

**宿主与控制台**

- aicli `publishGatewayTailLocked`、runtime-server `appendGatewayTail` 推送前检查 `TailWanted`（runtime-server
  经可选接口断言，兼容无门控的实现）；
- 控制台：展开卡片 → `presence {tail:true}`；收起 → `{tail:false}`；工具栏"实时输出：关" → 全部释放；
  普通 presence（打开会话）保持 `tail:false`，推送抑制语义不变。

**测试**

- `agent`：`TestControllerWatchGating`（三态：未通告 true / 通告后 false / watch 后精确放行 / unwatch 撤销）；
- `tunnel`：`TestWatchActions`（差集、排序、release-all）；
- `cmd/gateway`：`TestPresenceTailWatched`（tail 分流、释放后保留 presence、TTL 过期）+ 既有 presence 测试适配；
- jsdom：展开即上报 `tail:true`、收起上报 `tail:false`；
- `go build ./...` + 受影响包测试全绿。

**端到端实测（部署 192.210.174.189，sha `111c5641…`；harness 记录 `TailWanted` 时间线）**

- run1：`true`（连接）→ `false`（首个 ack 携带 gating:on，当时尚无 watch）→ `true`（presence watch 到达）；
  marker 5–8 输出 `marker N skipped (gate closed)` —— **门控广告、watch 启用、强制生效**三段全部得证；
- run2（确定性收起）：marker 1–10 全部推送（watch 生效期间尾流持续），探针收起卡片后
  `tailWanted=false` —— **unwatch 释放**得证；
- 测试设备（revoke+purge）/节点（remove）/本地令牌文件已清理。

**结论**：P1-b 验收达成。计划 §7 全部条目（P0 / P1-a / P1-b / P2）完成，gateway console 时效性优化收官。

---

## 12. 增量优化记录：折叠卡实时行 + 弹窗实时输出（2026-10-12，已完成并部署验证）

**动机**：P1-a/P1-b 之后，实时尾段仍只在"展开卡片"可见；用户希望**在会话列表里直接看到实时进度**。

**实现（纯控制台侧，协议/宿主零变更）**

- 折叠卡片新增 `.s-live` 行：取 `status_tail` 的**最后一行**（压缩空白、尾截断 ≤140 字符），
  带活动脉冲点；**点击该行即展开**并滚动到完整尾流；有实时行时隐藏普通预览，尾段清空后自动恢复预览；
- 详情弹窗新增"实时输出（进行中）"块（`sd-tail-wrap`/`sd-tail`，8em 滚动）；
- 实时行随既有 `event: session` 增量（签名含 `status_tail`）逐秒刷新；`sw.js` v25。

**测试与实测**

- jsdom 新增 5c 组：折叠卡显示实时行（含最新内容）、预览让位、点击展开、弹窗实时块、尾段清空后实时行隐藏且预览恢复——全绿；
- 真实页面实测（部署 `192.210.174.189`，sha `d8ea2345…`；harness 推送 TAILMARK 尾段）：
  `{"collapsed":true,"visible":true,"text":"TAILMARK:1791793671361-4"}` →
  点击后 `{"expanded":true,"tailVisible":true,"tailText":"TAILMARK:1791793671361-4"}` ——
  **折叠态即见实时内容、点击展开完整尾流**；
- 测试设备/节点与本地令牌文件已清理。

**说明**：实时行数据源仍为宿主上报的 `status_tail`（需要新构建的 aicli/runtime-server 才会产生）；
工具输出（stdout）与逐条消息历史仍不在列表内，属会话页职责。

---

## 13. 补记：D3「persist 裁剪 StatusTail」补齐（2026-10-12）

- **发现**：D3 在检查清单中已标记完成，但 `tunnel/persist.go::flush` 实际直接序列化 `listFunc()`，
  `StatusTail` 会随 `registry.json` 落盘（1Hz 尾流会让快照持续增长）——文档与代码存在落差；
- **修复**：`flush` 对快照副本按节点**复制 sessions 切片后置空 `StatusTail`** 再序列化
  （`Registry.List()` 返回浅拷贝、Sessions 切片与内部共享，必须先复制，避免清掉实时尾流）；
- **测试**：`TestPersisterTrimsStatusTail`——快照不含 tail、**实时 registry 仍保留 tail**；
  `go test ./internal/gateway/tunnel/` 全绿；
- **部署**：sha `10784d40…`（192.210.174.189，active）。

## 14. 修正：unified 渲染下 aicli 实时尾段被旧渲染守卫饿死（2026-10-12）

### 14.1 问题
- `publishGatewayTailLocked` 采样源是 `streamBuffer`（逐 chunk 累积，未完成即写），
  但门控条件写成了 `c.activeStream.Active()`。
- `ActiveStreamController` 属于 legacy（非 unified）渲染路径：unified 模式下
  `RenderAssistantDelta` 在写 `streamBuffer` 后于 unified 分支提前 return
  （chat_interaction.go:4243-4258），`BeginAssistant` 只在非 unified 分支被调用
  （4230 守卫 `!c.unifiedRenderer`）。生产 interactive 会话默认走 unified
  （chat_setup.go:234 → EnableUnifiedRendererGateway），因此 `Active()` 恒假，
  尾段永远不推。

### 14.2 修复
- `chat_interaction.go::publishGatewayTailLocked`：门控改为 `c.streamBuffer.Len() > 0`，
  空 buffer 采样为 ""（turn 结束 reset → 经共享节流的变化检测清除尾段）。
- 新增回归测试 `chat_gateway_tail_test.go`：
  - 无 activeStream 时仍推送 buffer 内容 + TailAt 盖章；
  - buffer 置空后（越过 1s 节流）清空尾段；
  - 512 rune 共享截断保留最新内容。

### 14.3 采样语义（回答"尾段是什么消息"）
- aicli：`runtimechatcore.EventResult` 流式事件 → `streamBuffer.WriteString`（逐 chunk；
  chat_core.go:932-965 / chat_interaction.go:4241），1Hz tick + 状态迁移时采样，
  1s 节流 + 未变不推，512 rune 截断。
- runtime-server：`assistant_delta / assistant.delta` 为增量打字机事件，累积 2048 rune 窗，
  展示截 512。
- 即：尾段是**未完成回答的流式增量**，不是 SSE 帧本身，也不是"处理完成后"的消息；
  完成后另有 `last_message`（turn 结束同步点）与 `EndTurn` 清尾。

### 14.4 验证
- `go test ./cmd/aicli/commands -run TestPublishGatewayTail -count=1` → PASS（2/2）。
- `go build ./cmd/aicli` → OK。
- 链路说明：store→SESSION_UPDATE→网关→SSE→console 的时效性此前已由 gwtail harness
  实测（P50≈0.78s）；本次修复补齐的正是 aicli buffer→store 这一段（此前仅 unified
  路径失效）。aicli 客户端需用修复后的构建重新连接网关方可见实时行。

### 14.5 全量包测试现状（与本修复无关的既有失败）
- 全量 `go test ./cmd/aicli/commands -count=1` 在本机存在既有失败（均不涉及 tail 采样路径；
  单独复跑同样失败，与本次一 hunk 修复无因果关系）：
  - `TestChatInteractiveDirectWriterInventory`：`chat.go` provider 启动选择器使用 `os.Std*`
    的行号清单与基线不一致（来自登录/选择器改动）。
  - `TestResolveChatProfileState_AppliesDefaultsPromptAndPolicy`：技能目录断言受用户环境
    `C:\Users\vince\.aicli\.agents\skills` 影响（环境相关）。
  - `TestRunProviderLogin_OpenAICompatibleCodexModelsUseCodexTemplate`：model card 数据
    漂移（gpt-5.x 卡片与断言期望不一致）。
- 全量运行末尾出现并行测试 goroutine dump（`agent_stdio_bridge_test.go:622` 附近），
  本机资源受限时的挂起，属既有问题。
- 与本次修复直接相关的测试全部通过：
  `-run 'TestPublishGatewayTail'`（2/2）、`-run 'Tail|Gateway'`（整组 ok）。

## 15. aicli / runtime-server 共享逻辑核查与验证（2026-10-12）

### 15.1 结论：实时尾段与状态同步共用同一套核心，不存在第二套实现

对照表（共享实现均为唯一来源，宿主只做事件→核心的映射）：

| 关注点 | 共享实现（internal/gateway/agent） | aicli 宿主映射 | runtime-server 宿主映射 |
| --- | --- | --- | --- |
| 状态词表 / attention↔state / busy 终态 | `SessionStateStore`（BeginTurn/EndTurn/SetAttention/SetSurface/Clear/Apply） | `chat_gateway_attention_store.go` 单例 + `chat_gateway_attention.go` 事件映射 | `gateway_attention.go` 单例 + `observeGatewayAttention` 事件映射 |
| 尾段截断（512 显示界） | `SessionTailMaxRunes=512` + `TruncateSessionTail`（`SetTail`/`Apply`/`TailThrottle` 统一使用） | 1Hz 采样 `streamBuffer` → `SetTail` | 累积 `assistant_delta` → `SetTail` |
| 推送节流 | `TailThrottle`（1s 窗口 + 内容未变不推 + `Forget`） | `gatewayTailThrottle` 包级单例 | `gatewayTailThrottleInst` 惰性单例 |
| watch 门控（P1-b） | `Controller.ApplyPending` + `TailWanted`（网关未通告时恒 true） | `c.session.Gateway.TailWanted(sessionID)` | `ctl.(interface{ TailWanted(string) bool })` 断言 |
| 摘要投影 | `SessionState.Apply`（含 `StatusTail`/`StatusStartedAt` 等） | `chatGatewaySessions` → `Apply` | `GatewaySessions` → `Apply` |
| 控制面实例 | `gatewayagent.NewController` | chat 会话持有同一类型 | `cmd/runtime-server/gateway_bootstrap.go:31-32` 注入同一类型 |

宿主专属差异仅三处：事件源（运行时事件总线 vs TUI 交互协调器）、推送触发（`pushGatewaySessions` vs gatewayStatusLane/同步器）、
runtime-server 的 `gatewayTailBufferRunes=2048` 仅是**宿主侧未展示累积窗**（显示仍由共享 512 截断收口，未复制截断/节流语义）。

### 15.2 实际测试与验证（本轮执行）

新增/扩展测试（全部通过）：

- runtime-server 新增 `internal/api/runtimeapi/gateway_tail_test.go`（真实 Handler + 真实事件总线 + 内存会话管理器 + 共享核心）：
  - `TestGatewayTailFromDeltaEventsAppliesSharedCore`：单帧 800 runes → 存储尾段 512 runes 且保留最新；1s 节流内第二帧不推、但累积进窗口；跨节流后推全窗（suffix `LATEEND`）；两次推送的 SESSION_UPDATE 摘要均携带 `StatusTail`。
  - `TestGatewayTailHonorsTailWantedGate`：门关（wanted=false）不推；门开（wanted=true）推。
  - `TestGatewaySessionsCarryStatusTail`：`GatewaySessions` 摘要经 `SessionState.Apply` 携带尾段。
- aicli 扩展 `cmd/aicli/commands/chat_gateway_tail_test.go`：新增汇总断言 `chatGatewaySessions(...).StatusTail == 采样值`（宿主映射段闭环）。
- 命令与结果：`go test ./internal/api/runtimeapi -run 'TestGatewayTail|TestGatewaySessionsCarryStatusTail|TestGatewayAttention'` → ok；
  `go test ./cmd/aicli/commands -run 'TestPublishGatewayTail'` → ok；`go build ./...` → exit 0。

部署态 e2e（既有实测证据，网关 binary sha `10784d40…` 仍为 `/opt/aicli-gateway/gateway`，服务 active）：

- P1/P2 两轮 gwtail harness（直连共享核心）：TAILMARK 1..8 全部推送，exit 0。
- P1-b 门控轮：`tailWanted=true` 推 1..4 → 收起后 `tailWanted=false`，5..10 全部 `gate marker N skipped (gate closed)`，exit 0。
- 浏览器探针轮：折叠卡实时行可见、点击展开（前序记录，jsdom UX 全绿）。
- 清理核验：线上状态目录无 `node-tail-e2e` 残留，本地临时令牌已删除。

### 15.3 覆盖边界（避免过度声称）

- gwtail harness 直连共享核心，**不覆盖** aicli `streamBuffer→store` 宿主适配段（unified 渲染门控 bug 正是由此漏检）；
  该段现由 aicli 回归测试（`TestPublishGatewayTailSamplesStreamBufferWithoutActiveController`）与新增汇总断言覆盖。
- runtime-server 未跑整进程级 e2e（需要完整 runtime-server 部署与配置）；其映射链路由上述 handler 级测试覆盖
  （真实 Handler、真实事件总线、真实会话管理器、共享核心全在链路中）。
- 本轮无生产代码改动（仅新增/扩展测试），网关无需重新部署；aicli 客户端实时行需使用含 unified 门控修复的构建。

## 16. 控制台样式/交互优化：实时输出置底 + 事件条按会话去重 — 2026-10-12

### 16.1 改动

- **实时输出改为卡片页脚**：`.s-live`（折叠：尾段最后一行）与 `.s-tail`（展开：8em 滚动尾流）
  在渲染末尾重新 append 到卡片最后（动作行、内联回复框之后）；新增
  `.session-card.expanded .s-live { display: none; }` 保证「一行行 / 全量尾流」互斥，
  不再出现两份实时信息叠在卡片中部。
- `.s-live` 改 `grid-column: 1 / -1`（整行通栏）+ `margin-top: 4px`，底部呈现更像页脚。
- **迁移事件条去重**：`pushStripEvent` 改为按 `node|session` 唯一 chip——同会话连续迁移原位刷新为
  最新状态并置顶，不再为每次迁移堆一个新 chip（保留最多 10 个不同会话 / 10 分钟 TTL / 清空按钮）。
- `sw.js` 缓存版本 `v25 → v26`（前端变更必须 bump）。

### 16.2 验证

**jsdom（`node tmp/console_ux_dom_test.js` → PASS）**新增断言：
- 底部定位：`.s-live` 索引 > `.s-actions`；`.s-tail` 是卡片最后一个元素；`.s-live` 紧随 `.s-tail` 之前；
- 事件条去重：同会话 3 次迁移（running→answering→completed）只产生 1 个 chip 且显示最新状态；
  另一会话出现后为 2 个 chip 且最新会话在最前。

**真实浏览器（Edge + puppeteer-core，经 127.0.0.1:10810 代理；`tmp/console_layout_probe.js`）**：
- 折叠态：`.s-live` bottom=280 > `.s-actions` bottom=257（位于动作行之下），`.s-tail` `display:none`
  且是卡片末元素；children 顺序 `… s-actions, s-live, s-tail`；
- 点击实时行展开：`.s-live` `display:none`、`.s-tail` `display:block` 且为末元素、包含 `TAILMARK:…`；
- 事件条：会话 `tail e2e` 走完（回答中→已完成）后仍只有 **1 个 chip**（"tail e2e · 已完成"），
  同时另一真实会话的 chip 独立存在（不同会话不误合并）；
- 截图证据：`tmp/gw_layout_collapsed.png`、`tmp/gw_layout_expanded.png`。

**门控回归（`tmp/gating_live_probe.js`，决定性实验）**：
- 展开早于首个 gating ack 时：`/api/presence` 200（0.6s 首发，其后每 ~30s 续期），
  TAILMARK 2..10 连续到达；gwtail 侧 `tailWanted=true` 全程保持、marker 1..10 全部 pushed；
- 结论：此前观察到的 `tailWanted=false` 属「折叠未观看 → 首个 ack 释放门控」的正常行为；
  展开与 agent 心跳（默认 20s）竞态时最坏等一个心跳周期恢复推流——已作为已知延迟记入 07-console.md。

### 16.3 部署与清理

- 网关（含内嵌控制台）重建并部署：sha256 `42d65a9b9913125c758c577b891ef3300c09d5256ccdf47c3e2ab126f9c9107b`，
  服务 active、`http://127.0.0.1:8080/` 200；HTTP 复核 `sw.js` v26、`app.js`（去重 + 置底 append）、
  `style.css`（展开互斥 + 通栏）全部就位；旧二进制留有 `gateway.bak-*` 备份。
- 清理：测试设备 `tmp-layout-probe` revoke+purge、`node-tail-e2e` 注册表条目删除
  （列表复核仅剩真实节点；某真实会话消息正文提及该字符串属正常文本而非残留条目）、owner logout、
  本地临时令牌/cookie 文件删除。
## 17. 控制台 UX 重构：卡片信息按业务属性分层 + 回复框单行化 — 2026-10-12

### 17.1 改动

- **回复框单行化**（用户点名「发送按钮不应单独占一行」）：`buildReplyForm` 由「textarea + 独立
  按钮行（`.s-reply-row`）」改为 `.s-reply-main` 单行 —— 输入框 `flex:1` + `发送` 紧排右侧
  （primary 样式，宽约 49px）；输入框随草稿自动增高（`scrollHeight` 回写，上限 160px；jsdom
  无布局引擎时跳过）；发送中禁用按钮；「发送中…/发送失败」提示降级为行下瞬态小字，不再占用整行。
- **卡片信息按业务属性分层**（自上而下）：身份行（状态点/标题/注意力徽标/内容时间）→
  **动态状态条 `.s-activity`**（状态胶囊 + 宿主活动文案 + 本地时钟；胶囊自标题行迁出，活跃/等待/
  中断时常驻，`--panel-2` 底 + 边框，与元信息视觉分离）→ 内容（预览 / 展开后完整消息）→
  **元信息行 `.s-meta`**（节点 · 会话 ID（点击复制）· 同步时间，原三行合并为一行）→ 动作行 →
  回复框 → 实时输出（页脚不变，见 §16）。
- 清理：移除 `.s-node`、`.s-status`、`.s-reply-row` 死规则；`sw.js` 缓存版本提升
  （本会话 `v26 → v27`；合并共享工作区后为 **v28**，部署侧复核一致）。

### 17.2 验证

- **jsdom（`node tmp/console_ux_dom_test.js` → PASS）**新增断言：动态状态条渲染（胶囊在位、
  标题行不再含 `.s-state`、活动文案 + 时钟就位、位于元信息/动作行之上）；元信息行合并；
  回复框 `.s-reply-main` 同排且无 `.s-reply-row`；Enter 派发真实提交（mock 侧收到
  `{"prompt":"来自 Enter 的回复"}` 且输入框清空）。
- **真实浏览器（Edge + puppeteer-core，本地静态服务 + API mock，`tmp/console_ux3_probe.js`
  → `ok:true`）**：桌面 1440×900 与移动 390×844 双档各 10/10 断言通过，含「输入框与发送同一行
  （midY 相等：324.6/324.6、401.6/401.6）」「发送按钮位于输入框右侧且宽 49px」「动态状态条位于
  身份行之下、元信息行之上」「节点/ID/同步同行」「尾流仍为卡片末元素」。截图：
  `tmp/gw_ux3_card.png`、`tmp/gw_ux3_mobile.png`。
- `go build`（backend 模块内 `go build ./cmd/gateway`，嵌入资源）通过。

### 17.3 部署与线上核验（2026-10-12，已完成）

- 交叉编译 `GOOS=linux GOARCH=amd64`（`go build ./cmd/gateway`）并部署到
  `/opt/aicli-gateway/gateway`：变更前备份 `gateway.bak-20261012-101041`；新二进制 sha256
  `32d2dc0a3d70a896aba91cd344da5f421e298b0f2df4deb1361219922970ed88`；
  `aicli-gateway.service` 重启后 `active`、本机 `http://127.0.0.1:8080/` 200。
- HTTP 逐字节复核（经 `127.0.0.1:10810` 代理拉取）：`app.js`/`style.css`/`sw.js` 与本地
  工作区文件 sha256 完全一致（`E5ABBA42…` / `0943C6F5…` / `FA9F84B7…`），线上控制台即
  本记录验证过的这一版。
- 部署端真实浏览器复核（`tmp/console_ux3_deploy_probe.js` → ok:true，8/8）：登录后在 3 个真实
  会话上确认「状态胶囊位于动态状态条」「状态条在元信息行之上」「节点/ID/同步同行」；展开带
  input 能力的会话后确认「输入框与发送同一行」「发送按钮紧凑（<140px）」「无独立按钮行」；
  外壳缓存 v28。截图 `tmp/gw_ux3_deployed.png`。
- 说明：共享工作区存在并行会话的未提交改动（如 `internal/gateway/tunnel/proxy.go` 的
  STREAM_RESET 文案），本次按当前工作区整体构建部署，未剥离；相关包测试
  `go test ./internal/gateway/tunnel/` 通过。
