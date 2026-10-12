# gateway console 时效性优化实施计划（P0 / P1 / P2）

> 日期：2026-10-12 ｜ 状态：**已审核确认**（D1–D5 按最佳实践确认，见 §10.5）；v2 补充四维专项（§10）
> 关联文档：[docs/gateway/07-console.md](../gateway/07-console.md)、[08-notifications.md](../gateway/08-notifications.md)、[03-protocol.md](../gateway/03-protocol.md)、[02-architecture.md](../gateway/02-architecture.md)
> 审核记录：[gateway-console-timeliness-implementation-plan-20261012-review.md](./gateway-console-timeliness-implementation-plan-20261012-review.md)
> 上游动机：用户反馈「控制台消息不够及时：看不到实时消息与状态，也不知道上次会话上传同步的时间」

---

## 0. 现状与问题

### 0.1 当前数据链路（已实现，代码锚点）

```text
宿主（aicli / runtime-server）
  ├─ SESSION_UPDATE（事件驱动：turn 启停 / attention / surface 迁移 / runtime 事件）
  │     aicli:      chat_gateway_attention.go（BeginTurn/EndTurn）、
  │                 chat_interaction.go::publishGatewaySurfaceStatusLocked（surface 迁移→锁外 lane）
  │     runtime-sv: runtimeapi/gateway_attention.go（tool.started→running、llm→answering…）
  ├─ HEARTBEAT 20s（携带 sessions_digest：state/attention/status_text/updated_at）
  │     agent/wsconn.go:17（默认 20s）、wsconn.go:372-385（digest 装载）
  └─ NOTIFY（瞬时注意力事件）
        │
网关 registry（tunnel）
  ├─ applySessionUpdate（session_update.go:7-28）
  ├─ applyHeartbeatDigests（仅 digest 真变化才广播+持久化；server.go:299-320）
  └─ notifyRegistryChangedAndPersist → OnRegistryChanged（main.go:318-321）
        ├─ hub.notify()：每订阅者 chan(1) 非阻塞信号（main.go:43-75，天然合并）
        └─ notifier.observe（Web Push，presence 抑制 push.go:100）
              │
控制台 SSE /api/events（main.go:465-509）
  └─ 每次收到信号即重发【全量 nodes 快照】；空闲时仅 20s 一次 `: ping`
        │
console（app.js）
  ├─ subscribeEvents()：`event: nodes` → renderAll
  ├─ timeOf()：卡片时间只显示 HH:MM（updated_at）
  └─ refreshStatusElapsed()：仅活动态 elapsed 走 5s 本地 tick
```

### 0.2 问题清单（与用户反馈一一对应）

| # | 用户感受 | 根因 | 代码证据 |
|---|---------|------|---------|
| G1 | 看不到"正在生成的消息" | 宿主只在**同步点**推送；`preview/last_message` 取自已落定消息，流式中的正文不进入推送 | `chat_gateway_host.go::chatGatewaySessions`（preview/last_message 组装）；surface 迁移只带 `status_text`，不带正文 |
| G2 | 不知道上次会话上传/同步的时间 | 会话只有 `updated_at`（宿主侧消息时间），**没有"网关最后一次收到该会话更新的时间"**；`Node.last_heartbeat` 每 20s 更新但**无变化时不广播**、UI 也不展示 | `wire/messages.go:48-65`（无 synced_at）；`tunnel/server.go:299-320`（digest 无变化 → 不 notify）；console 无 last_heartbeat 渲染 |
| G3 | 时间无感知（3s 前还是 20min 前？） | 卡片/列表只显示 `HH:MM`，不做相对时间、不会本地推进 | `app.js::timeOf` |
| G4 | 断线不可见 | SSE `error` 回调为空；重连后不主动对齐 | `app.js::subscribeEvents`（`source.addEventListener("error", function () {})`） |
| G5 | 状态及时但"没有时间线" | attention/state 迁移没有独立 SSE 事件与记录，只有 toast | `tunnel/notify.go`（Notify 只做推送派生）；console 无事件条 |

---

## 1. 目标 / 非目标

**目标**

1. 任何状态变化（running/completed/interrupted/approval…）**≤2s** 出现在控制台；
2. 活动期（生成/工具执行）提供**有界**的实时视图：1Hz 活动脉冲 + 可选的正文尾段（tail）；
3. 明确展示三类时间：**内容时间（updated_at）、同步时间（synced_at，网关盖章）、节点心跳（last_heartbeat）**，且空闲时也会本地推进；
4. SSE 断线 ≤5s 可见提示，恢复后自动对齐；
5. 全部改动保持**增量、不移全量正文**：正文体验仍归会话页（iframe / `/s/{id}/web/`）。

**非目标**

- 不在控制台重复渲染完整会话正文（维持 07-console.md §6 边界）；
- 不引入 WebSocket 双工给控制台（继续用 SSE；写路径仅既有 REST）；
- 不做全量轮询同步（维持"事件 + 心跳对账"架构）。

---

## 2. 设计总览

### 2.1 三条时间轴（新增语义定义）

| 时间 | 定义 | 来源 | 用途 |
|------|------|------|------|
| `updated_at`（已有） | 宿主侧"最后消息"时间 | agent 上报 | "内容多久没变" |
| `synced_at`（新增） | **网关最后一次收到该会话任何帧**（SESSION_UPDATE 或 heartbeat digest）的服务器时间 | 网关盖章（不可由 agent 伪造） | "上传同步 x 前"；掉线时自然变大 |
| `last_heartbeat`（已有） | 节点最近心跳时间 | 网关盖章 | 节点活性 |

### 2.2 推送分层

| 层 | 频率 | 内容 | 门控 |
|----|------|------|------|
| L0 状态迁移（已有） | 事件驱动（每次迁移一次） | state/attention/status_text/elapsed 锚点 | 无（全量客户端） |
| L1 活动脉冲（P1-a） | 活动期 1Hz，节流 + 变化才推 | `status_text` + `StatusTail`（≤512 runes 正文尾段）；经 **`event: session` 增量**广播（§10.2.1） | 无 |
| L2 聚焦尾流（P1-opt/P1-b） | 同上 | 同上，但仅在"有人看"时下发 | **presence 门控**（网关→agent 控制帧） |

> 关键设计：**presence 复用**。网关已有 `presenceRegistry`（`cmd/gateway/presence.go`，TTL 默认 90s，`main.go:185`），控制台每 30s 上报（`app.js::setInterval(sendPresence, 30000)`），当前仅用于抑制 Web Push（`push.go:100`）。P1-b 把它升级为"尾流订阅开关"的输入。

### 2.3 控制台渲染规则

- 列表卡片：L0 状态 + L1 **不渲染正文**（仅当 `status_text`/state 变化时"脉冲"高亮，已有）；
- **展开的卡片**：渲染 tail 块（等宽小字、最多 8 行、自动"跟随最新"，可关闭）；
- 详情弹窗：P2 增加"同步元数据块"（synced_at / last_heartbeat / updated_at / status_started_at 的 age）。

---

## 3. P0 — 新鲜度可感知（预计 1 天，风险低）

### 3.1 协议与网关：`synced_at`（网关盖章）

- `wire.SessionSummary` 增加字段：

  ```go
  // SyncedAt is stamped by the GATEWAY when it last accepted any frame
  // (SESSION_UPDATE or heartbeat digest) for this session. Agents do not set
  // it; the gateway overwrites any inbound value so it is always server time.
  SyncedAt time.Time `json:"synced_at,omitempty"`
  ```

- 盖章点（网关侧，二选一统一为工具函数 `stampSyncedAt(node, now)`）：
  - `tunnel/session_update.go::applySessionUpdate`：reconcile/upsert 后对被触达的会话盖章；remove 不盖；
  - `tunnel/session_update.go::applyHeartbeatDigests`：digest 命中的会话也盖章（语义=“对账成功”）。
- 兼容性：字段为 additive；旧 agent 不发、旧网关忽略；新网关对新旧 agent 均盖章。
- 注意：`registry.json` 会随之持久化该字段（现状 `last_message` 已随快照落盘，同级行为，可接受；如需减体在 persist 处裁剪，列为可选）。

### 3.2 网关：心跳可见 + `meta tick` 广播

- `main.go` 增加 flag：`--event-tick`（默认 `30s`，`0`=关闭）；`hub.notify()` 每 tick 触发一次。
- 目的：空闲时（无任何 registry 变化）也让 SSE 刷新一次全量快照，使 `last_heartbeat`/`synced_at` 在 UI 中保持新鲜；否则这两个值会停在最后一次广播。
- 成本：13 节点快照 ≈ 20KB / 30s / 订阅者，可忽略；移动端 1 次/30s 亦可接受。
- 前端**同时**做本地 5s tick（3.3），即使不开 meta tick 相对时间也会推进；meta tick 只解决"数值本身"的新鲜度。

### 3.3 控制台：相对时间 + 过期样式

- 新增 `timeAgo(ts)`：`刚刚 / x 秒前 / x 分钟前 / x 小时前 / MM-DD HH:MM`。
- 卡片：`s-time` 改为相对时间（`title` = 完整时间）；`s-node` 行尾追加 `· 同步 x 前`（`synced_at`，缺失时退回 `updated_at` 并标注"内容 x 前"）。
- 阈值样式：`synced_at` 或 `last_heartbeat` age > `max(2×心跳间隔, 60s)` → 加 `.age-stale`（变灰 + ⚠ title"可能已过期"）；节点卡片同样显示 `心跳 x 前`。
- tick：把现有 5s `refreshStatusElapsed` 扩展为 `refreshTimeSensitiveLabels()`（相对时间 + elapsed 统一刷新，纯本地）。

### 3.4 控制台：SSE 断线可见 + 重连对齐

- `subscribeEvents`：
  - `onerror` → `setConnState("reconnecting")`：topbar `conn-chip` 变红 + 顶部持久横幅"与网关连接中断，正在重连…"；
  - `onopen`（重连成功）→ `loadNodes()` 对齐 + 清除横幅；
  - 页面 `visibilitychange` 恢复时若"距上次 SSE 事件 > 60s"主动 `loadNodes()` 一次。

### 3.5 P0 验收 / 测试 / 回退

- 单测：`stampSyncedAt` 覆盖 reconcile/upsert/digest（tunnel 包）；`applyHeartbeatDigests` 原有测试补断言。
- jsdom：相对时间渲染、age-stale 阈值、断线横幅开关、重连后 refresh 调用（fetch stub 记录）。
- 实机验收：`synced_at` 与 `Date.now()` 误差 ≤2s；断开一个 agent 后其会话 `同步 x 前` 持续增大且进入 stale 样式；SSE 断线（kill -STOP 网关进程/断代理）≤5s 出横幅，恢复后自动对齐。
- 回退：仅新增字段 + UI；回退=还原二进制，旧快照数据无影响。

---

## 4. P1 — 实时活动与消息尾流

### 4.1 协议：`StatusTail`（additive，展示专用）

```go
// StatusTail is the display-only tail (≤512 runes) of the host's in-progress
// content while a turn is active. Gateway and console treat it as an opaque
// string; it never replaces the full transcript (see 07-console §6).
StatusTail string `json:"status_tail,omitempty"`
```

- 仅活动期（`SessionStateActive`）携带；turn 结束时置空；
- 节流：宿主侧 **1Hz + 仅变化时推**（与 status_text 同一路 SESSION_UPDATE(upsert)）；
- 长度：宿主截断到 ≤512 runes；网关侧不做再截断（信任边界=与 last_message 同级）。

### 4.2 宿主接入

- **aicli**：在 `chatInteractionCoordinator` 暴露"当前流式尾段快照"——首选 `streamBuffer.String()` 取尾部窗口（`chat_interaction.go:121`，已在稳定提交路径中被读取，锁内快照成本低），备选 `activeStream` 渲染缓存；实现时若取用点受限，P1-a 先只推 `status_text`+elapsed，tail 作为 P1-a.2 单独提交；
- **runtime-server**：`runtimeapi` 的 live stream 管道（`live_tool_stream.go` 相邻）已有增量；同样只取尾部窗口；
- 触发：宿主已有 **1s** dynamic-status tick（`chat_interaction.go::scheduleDynamicStatusTickLocked`，delay = `time.Second - elapsed%time.Second`，仅活动期运行）——直接复用该 tick 做"变化才推 + ≤1Hz"节流，无需新增定时器；tick 关闭的宿主（如管道模式 `DynamicStatusTicksEnabled=false`）退回"surface 迁移时推送"（现状）；
- 无活动 = 零推送（保持现状）。

### 4.3 网关侧

- 应用路径无需新增（`applySessionUpdate` 通用）；`applyHeartbeatDigests` 不带 tail（保持 digest 轻量）；
- 广播（**P1 必选改造，见 §10.2.1**）：新增 `event: session` 单会话增量；`event: nodes` 全量快照只保留给首连/结构变化/meta tick 对账。`hub` 每订阅者 chan(1) 保持信号合并；增量通道 drop-oldest，慢客户端由 30s 全量对账兜底。
- 成本对比：单活跃会话 1Hz 全量直推 ~20KB/s/订阅者 → 增量 ~1.5KB/s/订阅者（≈12× 降幅，随节点规模优势扩大）。
- 持久化：**D3 已确认**——`persist` 写出时复制裁剪 `StatusTail`（内存保留、快照不含），控制 `registry.json` 体积（last_message 已有 2000 runes 上限）。

### 4.4 控制台渲染

- 展开卡片新增 `.s-tail` 块：等宽 11.5px、`white-space: pre-wrap`、`max-height: 8em`、`overflow-y:auto`；
- "跟随最新"默认开（滚到底）；用户上滚后暂停跟随，回到底部自动恢复；按钮可关 tail 渲染（存 prefs）；
- 未展开卡片不渲染 tail（数据仍在内存，切换展开即时显示）；
- 活动指示：`生成中 · 2m05s · tail` 计数（runes）。

### 4.5 P1-opt：presence 门控（网关→agent 控制帧）

- 现状：presence 只做 push 抑制；控制台仅 `openSession` 时 `rememberPresence`（`app.js`），需扩展为**展开/聚焦即上报**（同一 `/api/presence` 接口，body 不变，语义扩展）；
- 网关：在 `HeartbeatAck.Pending []string`（已有字段，`wire/messages.go:150-153`）下发 `watch:<nodeID>:<sessionID>` / `unwatch:<...>`（按 presence 集合差计算；presence TTL 90s 自然过期后下发 unwatch）；
- agent：`wsconn` readLoop 消费 HeartbeatAck.Pending（**现状未消费，需新增**），维护 `tailWatch` 集合；仅对 watch 中的会话开启 1Hz tail 推送；
- 降级策略：**默认不推 tail，收到 watch 才开** → 旧 agent（不认识 Pending）自然永不推 tail（安全降级）；旧网关（不下发 watch）不影响新 agent 的状态推送；
- 决策点（审核时定）：P1-a（无门控直接推）与 P1-b（门控）是否分两次上线；建议 **P1-a 先上（simple），P1-b 作为流量优化后续**。

### 4.6 P1 验收 / 测试 / 回退

- 隐私/边界：tail 属正文片段，与既有 `last_message`（2000 runes）同级；仅内存 + 可选不落盘；文档在 03-protocol §3.5 补充说明；
- 单测：宿主 tail 截断/节流（1Hz、无变化不推）；gateway 对新字段透传；agent Pending 解析（P1-b）；
- E2E：`gwstate` 探针宿主推送带 tail 的 5 个状态变更，headless Edge/SSE 客户端断言 tail 延迟 **P50 ≤1.5s、P95 ≤3s**（跨代理场景口径，见 §10.3.1）；
- 回退：`--event-tick`/`StatusTail` 全 additive；回退二进制即恢复。

---

## 5. P2 — 同步元数据与事件时间线

1. **详情弹窗"同步元数据"块**：synced_at / last_heartbeat / updated_at / status_started_at（部分已有）+ 各自 age；
2. **网关盖章补充时间**：`attention_changed_at`、`state_changed_at`（在 `applySessionUpdate`/`applyHeartbeatDigests` 检测字段变化时盖章；纯网关侧，agent 无感）→ 详情展示"等待审批 2m 前"；
3. **SSE `session-event`（迁移事件条）**：网关在 apply 时检测 attention/state 迁移，额外 emit：
   ```text
   event: session-event
   data: {"node_id":"…","session_id":"…","title":"…","state":"…","attention":"…","ts":"…"}
   ```
   控制台顶部做"刚刚：会话 X 已完成 / 需要审批"轻量事件条（保留最近 10 条 / 10 分钟），点击定位卡片；旧前端忽略未知 event 类型（EventSource 只派发给已注册监听器），兼容；
4. **可选（P2+）**：per-session SSE 增量事件（`event: session`）替代全量快照，用于大规模节点/会话场景。

---

## 6. 兼容性 / 迁移 / 回退

| 变更 | 兼容性 | 回退 |
|------|--------|------|
| `synced_at` / `StatusTail` / 变更时间戳（wire additive） | 旧 agent 不发、旧网关忽略；新网关对新旧 agent 均生效 | 还原二进制 |
| `HeartbeatAck.Pending` watch/unwatch（P1-b） | 旧 agent 不消费 → 永不推 tail（安全降级）；新 agent 不认旧网关（无 Pending）→ 同降级 | 关 flag |
| `--event-tick` | 新 flag，默认 30s；`0` 关闭 | 改 flag |
| SSE `session-event` | 新增 event 类型，旧前端忽略 | 网关照常发，前端不监听即可 |

---

## 7. 任务清单（按阶段）

### P0（≈1 天）
- [x] wire：`SessionSummary.SyncedAt` + 文档（03-protocol/02-architecture）（2026-10-12 完成）
- [x] tunnel：`stampSessionSynced/AllSessionsSynced/DigestSessions` 接入 + 单测（盖章不入变更检测）
- [x] main.go：`--event-tick` 默认 30s → `hub.notify()` 定时器（只 SSE，不喂 push）
- [x] console：`timeAgo` + 卡片"同步 x 前"/节点"心跳 x 前" + `.age-stale` + 5s tick 扩展
- [x] console：SSE 断线横幅 + 重连 `loadNodes()` + visibilitychange 对齐
- [x] jsdom 用例扩展 + 探针"新鲜度断言"（实测 `同步 13s 前→18s 前` 本地推进）
- [x] 构建/部署/实测（192.210.174.189）

### P1-a（≈1–1.5 天）
- [x] wire：`StatusTail` 字段 + 03-protocol 说明（隐私边界）（2026-10-12 完成）
- [x] **共享核心**：`SessionState.Tail/TailAt` + `SetTail` + `Apply()` 统一 512-rune 截断（单测）
- [x] **共享核心**：`TailThrottle.Allow(id, tail, now)`（1s 窗口 + 变化检测）+ 单测
- [x] aicli：tail 快照来源接入（`streamBuffer` 尾部）+ 复用 1s surface tick + 节流单测
- [x] runtime-server：同上（`assistant_delta/assistant.delta` → 2048-rune 累积窗）
- [x] **网关 P1 必选**：`OnSessionChanged` 回调 + `event: session` 增量广播 + 控制台单会话 patch（§10.2.1）
- [x] 网关：digest 合并不再覆盖 `StatusText/StatusTail`（§10.3.3 乱序防护）
- [x] console：展开卡片 `.s-tail` 渲染 + 跟随最新 + 开关
- [x] 探针：tail 端到端延迟断言（实测 P50≈0.78s / max 1.24s）

### P1-b（≈1 天，可选）
- [x] presence 语义扩展（console 展开/收起上报 `tail` 标志；普通 presence 仍抑制推送）
- [x] gateway：`watchActions` 差集 → `HeartbeatAck.Pending`（`tail-gating:on` / `watch:<sid>` / `unwatch:<sid>`，与活跃会话求交）+ 单测
- [x] agent：readLoop 消费 Pending + `Controller.ApplyPending/TailWanted` 门控（未通告前默认放行 = P1-a 兜底）+ 单测
- [x] 文档：协议/控制台文档更新（门控协议与降级语义）
- [x] 端到端实测（2026-10-12）：`tailWanted=true→false→true`（通告+watch）、marker 5–8 门关跳过（强制生效）、收起后 `tailWanted=false`（unwatch）

### P2（≈1–1.5 天）
- [x] gateway：`attention_changed_at` / `state_changed_at` 盖章（upsert/reconcile/digest 三路径，单测钉住）
- [x] SSE `session-event` 迁移事件 + 控制台事件条（点击定位 + 脉冲 + 清空 + 10 条/10 分钟）
- [x] 详情弹窗同步元数据块（状态变更/注意力变更/节点心跳/同步）
- [x] per-session 增量 SSE 已在 P1-a 落地（`event: session`）；本阶段无需额外评估

---

## 8. 端到端验收方法（探针）

复用现有资产：`tmp/gw_page_probe.js`（无头 Edge + 代理 + 真登录）、`tmp/console_ux_dom_test.js`（jsdom）、以及可重建的 `backend/tmp/gwstate`（共享核心测试宿主）。

1. **状态迁移延迟**：gwstate 推送 `running→completed` 时记录 T0；Node 端 SSE 客户端（或探针注入 `EventSource` 记录）测量 `nodes` 事件包含新 state 的时刻 T1，断言 `T1-T0 ≤ 2s`（经代理场景放宽到 3s）；
2. **tail 延迟**：gwstate 连续推送 5 条 tail，展开卡片断言文本出现延迟 ≤1.5s；
3. **同步时间**：断言 `同步 x 前` 文本在无 SSE 事件的 10s 内持续推进；`synced_at` 与网关时间误差 ≤2s；
4. **断线**：停网关 → ≤5s 出横幅；恢复 → 自动对齐且横幅消失；
5. **回归**：现有 jsdom 用例 + `go test ./cmd/gateway/ ./internal/gateway/...`。

---

## 9. 风险与开放问题

| # | 风险/问题 | 处置 |
|---|-----------|------|
| R1 | SSE 全量快照在 P1 下放大（1Hz × 活跃会话 × 订阅者） | 先量测（13 节点 ≈20KB）；超预算转 per-session 增量事件（P2+ 预案） |
| R2 | aicli 流式缓冲取用点不明确 | 实现时先探针验证取用点；不可得则 P1-a 仅 status_text，tail 拆到 P1-a.2 |
| R3 | tail 落盘（registry.json 体积） | 默认 persist 时裁剪（待审核决策） |
| R4 | presence 门控需新增 agent 消费 Pending | 独立 P1-b，默认关闭 tail 的安全降级路径 |
| R5 | 旧移动端 PWA 缓存旧壳 | `sw.js` 版本号随实现递增（当前 v21） |
| Q1 | meta tick 间隔（30s?）与移动端流量权衡 | 审核决策；可先 60s |
| Q2 | tail 默认开/关 | 审核决策；建议默认开（仅展开渲染），流量敏感用户可关 |
| Q3 | 是否保留"事件条"历史 | P2 范围裁剪点 |

---

# 10. 四维专项设计（v2 补充 · 按最佳实践确认）

> 本节回答四个维度的系统性要求：**共用核心（aicli/runtime-server）、性能、及时性、交互性**。
> §10.5 记录 D1–D5 的确认结论（按最佳实践取默认值）。

## 10.1 共用核心：任何新机制只写一遍（`internal/gateway/agent`）

原则沿用已落地的 `SessionStateStore` 分工：**核心拥有语义与算法，宿主只做事件映射**。

| 机制 | 归属（唯一实现） | aicli 侧 | runtime-server 侧 |
|------|-----------------|----------|-------------------|
| `synced_at`（网关盖章） | 网关 `tunnel`（`stampSyncedAt`） | 无感（自动进 summary） | 无感 |
| `StatusTail` 字段与**512-rune 截断** | `agent.SessionState.Tail` + `Apply()` 统一裁剪 | 传原始尾段 | 传原始尾段 |
| **1Hz 节流 + 变化才推**（`TailThrottle.Allow(id, tail, now)`） | `agent` 新类型 | 复用于 surface tick | 复用于事件流 |
| P1-b watch 门控 | `agent.Controller.ApplyPending` + `Controller.Watched(id)`（wsconn 消费 `HeartbeatAck.Pending`） | 查询即可 | 查询即可 |
| 活动状态文本（status_text/state） | `agent.SessionStateStore`（已落地） | surface 状态机映射 | 运行时事件映射（已落地） |

落地清单：

- [ ] `internal/gateway/agent/sessionstate.go`：`SessionState` 增加 `Tail string`/`TailAt time.Time`；新增 `SetTail()`；`Apply()` 内做**唯一一处** 512-rune 截断（两端不可能漂移）；
- [ ] `internal/gateway/agent/tailthrottle.go`（新）：`TailThrottle{minInterval}` + `Allow(id, tail, now) bool`（1s 窗口 + 内容未变不推）+ 单测；
- [ ] `internal/gateway/agent/control.go` + `wsconn.go`：`Pending` 消费 → `ApplyPending` → `watchSet`；`Watched()` 查询；单测；
- [ ] 宿主仅在"取流式尾段"处有差异（aicli `streamBuffer` / runtime-server live stream buffer）。

**测试纪律**：语义/算法测试全部落在 `internal/gateway/agent`（共享）；宿主侧只测"映射是否正确"。

## 10.2 性能：预算与机制

### 10.2.1 传输模型（P1 的必选改造，避免全量快照放大）

| 事件 | 用途 | 频率 | 负载 |
|------|------|------|------|
| `event: nodes`（全量快照） | 首连、注册/移除/离线切换、meta tick 对账 | 事件驱动 + 30s | ~20KB（13 节点，随规模线性） |
| **`event: session`（单会话增量，新增）** | L1：state/status_text/tail 变化 | 活动期 ≤1Hz/会话 | ~1.5KB/次 |

- 成本对比（1 个活跃会话）：全量直推 20KB/s/订阅者 → 增量 ~1.5KB/s/订阅者（**≈12× 降幅**，随节点数增长优势更大）；
- 网关实现：`tunnel.Config` 增加可选回调 `OnSessionChanged(nodeID, sessionID)`（`applySessionUpdate` 后触发）；`main.go` 注入 → 新 `sessionHub` 广播 `event: session`（payload=单条 `SessionSummary`）；
- 一致性：增量是 **best-effort**（慢订阅者丢弃由 hub `chan(1)` 合并语义决定），**meta tick 的 30s 全量对账兜底**；
- 控制台：收到 `event: session` 时**patch 单会话进 `lastNodes`** 后再走现有签名去重渲染（不整表重建）；未知字段/未知 event 自然忽略（向后兼容）。

### 10.2.2 体积与内存预算

| 项 | 上限 | 机制 |
|----|------|------|
| `last_message` | 2000 runes（已有） | 宿主截断 |
| `StatusTail` | **512 runes（核心统一截断）** | `Apply()` |
| `SessionSummary` JSON | ≤ ~8KB | 上述两项之和 |
| heartbeat digest | 不含 tail（~几十字节/会话） | 现状维持 |
| registry 内存 | tail ≤512B/会话 | 有界 |
| `registry.json` | persist 前裁剪 `StatusTail`（D3） | `persist` 快照函数内复制裁剪，不改内存数据 |
| SSE 单订阅者缓冲 | `chan(1)` 信号 + 增量通道 drop-oldest | 慢客户端丢增量、靠对账收敛 |

### 10.2.3 渲染性能

- 列表：签名去重（已有）+ 单会话 patch（新）→ 活动期每秒最多重建 1 张卡；
- tail：仅**展开卡片**渲染（≤1 个 DOM/秒）；未展开不创建 tail 节点（数据仍在内存，展开即显）；
- 移动端：meta tick 30s；tail 仅活动期；整页数据量 ≈ 首屏快照（~20KB）+ 增量 1.5KB/s。

## 10.3 及时性：端到端路径与度量

### 10.3.1 时延分解（P95 目标）

| 跳 | 典型值 | 说明 |
|----|--------|------|
| 宿主采样（tick 对齐） | ≤1s（1Hz tick） | aicli 已有 1s tick；runtime-server 事件驱动 |
| 节流窗口 | ≤1s | `TailThrottle` 1s |
| WS/网络 | 10–150ms（经代理场景放宽） | 15s/20s 心跳仅为兜底 |
| 网关 apply | <5ms | registry 内存操作 |
| SSE 广播 → 渲染 | <100ms | 增量事件 |
| **合计** | **状态 ≤2s / tail P50 ≤1.5s、P95 ≤3s** | 跨公网 + 代理走 P95 口径 |

### 10.3.2 度量方法（探针 v2）

- 时间点：T0=宿主推送（探针本地时钟）→ T1=网关接收（**`synced_at` 即服务器时间**，需与 T0 同机或做时钟差校正）→ T2=SSE 送达（Node SSE 客户端计时）→ T3=UI 可见（puppeteer 轮询 DOM / MutationObserver）；
- 断言：`T3-T1 ≤ 1s`（同机）或 `T2-T1 ≤ 1s`（跨机网络）；`T3-T0 ≤ 2s`（本机直连）/ `≤ 3s`（经代理）；
- 无事件时的活性：断言 `同步 x 前` 文本 10s 内持续推进（纯本地 tick），meta tick 到达后数值刷新。

### 10.3.3 乱序与时钟（健壮性补充）

- `SESSION_UPDATE` 走单条 WS 有序；`digest` 是兜底快照，存在"旧值覆盖"的极端窗口。**建议（P1-a 一并做）**：digest 合并**不再覆盖 `StatusText`/`StatusTail`**（只校正 state/attention/updated_at），状态文本始终由事件流维护；重连时 REGISTER 全量自带最新值，无丢失；
- `synced_at`/`last_heartbeat` 一律网关时钟；控制台相对时间用本地时钟差显示（允许 ±偏差，tooltip 给绝对时间）。

## 10.4 交互性

| 场景 | 设计 |
|------|------|
| tail 跟随 | 默认"跟随最新"；用户上滚暂停；出现"跳到底部"悬浮按钮；回底自动恢复；`aria-live=off`（避免读屏刷屏） |
| tail 开关 | 卡片级"显示实时输出"开关（prefs 持久化）；关闭时仅保留状态行 |
| tail 空态 | "等待首个输出…"（活动期无 tail）/ "—"（宿主未提供）；绝不显示错误 |
| 降级 | 旧 agent（无字段）→ 无 tail 不报错；SSE 断 → 横幅；tail 接收中断 → 状态行仍在（事件流） |
| 事件条（P2） | 点击定位卡片（scrollIntoView + 脉冲高亮）、可清空；`aria-live=polite`；10 条/10 分钟上限 |
| 键盘（已有 + 增补） | `/` 搜索、↑/↓ 移动、Enter 详情、Space 展开（已有）；P2 事件条 `Alt+点击` 抑制？→ 不做，保持最小集 |
| 信息透明 | "同步 x 前 / 心跳 x 前 / 内容 x 前"三者 tooltip 给出定义与绝对时间，避免误读（见 §2.1） |

## 10.5 决策确认（D1–D5，按最佳实践）

| # | 决策 | 确认结论 |
|---|------|---------|
| D1 | meta tick 间隔 | **30s**（`--event-tick`，`0`=off） |
| D2 | P1 上线方式 | **P1-a 先行**（tail + 增量事件），**P1-b 门控随后**（有流量数据再评估） |
| D3 | `StatusTail` 落盘 | **persist 前裁剪**（快照不含 tail；内存保留） |
| D4 | tail 默认开关 | **默认开**（仅展开卡片渲染；用户可关，prefs 记忆） |
| D5 | P2 事件条范围 | **仅轻量"刚刚"条**（历史面板延后） |

> 因 §10.2.1 将 `event: session` 增量从"可选优化"提升为 **P1 必选**，P1 工期由 ≈1–1.5 天调整为 ≈1.5–2 天（网关回调 + 广播 + 控制台 patch + 测试）。
