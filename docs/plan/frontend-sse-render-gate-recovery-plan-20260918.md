# SSE 渲染闸门「有帧活动但页面不更新」修复方案

> 版本：**v2（2026-09-18 完整性审查后修订）**，替代 v1「SSE 活动自动开闸」草案。
> 本文件是方案，不是已实施改动。
> 现场：`session_20260918081157_TZduqX8h`（渲染闸门=关 / 在途回合=无 / 被拦增量=100 / 未认领回合=5 / 快照刷新=5 / SSE 有帧：tool_started、tool_finished、assistant_delta）
> 关联：
> - 现象与排查：[`../analysis/frontend-sse-render-gate-analysis-20260918.md`](../analysis/frontend-sse-render-gate-analysis-20260918.md)
> - 设计缺陷分析：[`../analysis/frontend-sse-render-gate-design-review-20260918.md`](../analysis/frontend-sse-render-gate-design-review-20260918.md)
> - 取证 runbook：[`./frontend-sse-live-diagnostics-runbook-20260918.md`](./frontend-sse-live-diagnostics-runbook-20260918.md)

---

## 0. 完整性审查结论（v1 的三处缺陷 + 本次新增证据）

### 0.1 v1 方案缺陷（已修正，勿再照 v1 实施）

| # | 缺陷 | 后果 | v2 对应 |
|---|------|------|---------|
| 1 | 「有 SSE 活动/字节就开闸」 | `/runtime/stream` 的初始 dump 与断点补齐会把**已结束回合的历史增量**写进当前消息，破坏「回放不渲染」既有不变式 | §2 帧分类 + §3 L2 活跃证据 |
| 2 | 只改 `renderLiveDeltas`，不引入回合身份 | 没有写入目标：`applyRuntimeDeltaToThread` 找不到 streaming 助手消息、且 `expectedTurnId` 为空时不会补建占位（`resolveInFlightTurnId` 要求两侧身份明确相等）→ 即使开闸，增量仍被丢弃 | §3 L3 认领 + 写入目标 |
| 3 | v1 示例代码不可用 | `useMemo(() => getLiveDiagnosticsSnapshot(id), [sessionId])` 无订阅、事件到达不会重算；且用诊断 store 驱动生产渲染违反该 store 定位（观测不反向驱动业务） | §3 设计（业务判定不读诊断 store） |

### 0.2 本次核实的代码事实（新增，含出处）

1. **后端已有 replay / live 标记，但语义与直觉不同**
   - `backend/internal/api/skills/session_runtime_event_view.go:34-45`：store 侧帧（初始 dump、断点补齐、**以及连接建立后新落库的行**）一律 `replay:true`；总线直投的 live-only 帧（`tool.progress`、`subagent.progress`）才带 `live:true`，两者互斥。
   - 结论：`replay:true` 只表示「经 EventStore 投递」，**不能**据此判定「历史」。
2. **精确的「补齐 vs 新增」分界在注释帧里，前端当前把它当 keepalive 丢弃**
   - `backend/internal/api/skills/session_runtime_stream.go:350-358`：带游标且确实补到新行时下发 `: resumed from=A to=B`，`B` = 本次 dump 追平到的最高 seq；此后经 eventWake / ticker 拉到的 store 帧 seq > B。
   - `frontend/src/api/runtime/sse.ts:271-274`：所有 `:` 注释一律按 keepalive 计数，内容被丢弃。
   - 结论：拿到 `B` 即可**不依赖时钟**区分 catch-up（seq ≤ B）与「连接建立后新产生」（seq > B）。
3. **`active_turn` 是进程内注册表**
   - `backend/internal/api/skills/session_active_turn.go:49-57`：只表达「本进程此刻在跑哪个 turn」，仅由本进程的 `POST /api/agent/chat` 注册；重启即失效、不落库。
   - 结论：跨进程/跨实例（另一个 server 实例、aicli TUI、其他客户端驱动同一会话）产生的活跃回合，`/runtime` 永远返回 `active_turn:null`；前端现有「快照续传」路径对它**永久失效**（自愈刷新必然失败）。
4. **前端没有任何 replay 消费者**：`grep '"replay"' frontend/src` 无命中（仅 trajectory reducer 读 `payload["live"]`）。
5. **自愈对回放帧同样触发**：`use-workspace-live.ts:217-235` 对任意带 turn 的 delta（不分类）调用 `handleUnownedTurn` → 刷新快照。回放帧 → 5 次「未认领」→ 5 次刷新 → 必然失败，正是现场数字。
6. **被拦增量口径混入回放**：`reportBlockedDelta` 对闸门关闭时的所有 delta 计账（含回放），面板据此给出「到达未渲染」结论（`derive.ts:87-94`）；现场 100 条里有多少是回放无法区分。
7. **诊断面板缺口**：store 已记录 `localResponding` / `resumedTurnId`（`types.ts:73-81`），但面板只显示 gate/turn/blocked/unowned/refreshes/gap（`session-detail-network.tsx:216-254`）；快照拉取失败次数/最近错误也没有出口。
8. **丢帧缓冲有界且可复放**：`mergeRuntimeEvent` 有 `MAX_RUNTIME_EVENTS` 上限（`events.ts:148-169`）；被跳过的 delta 未 claim（claim 只在 `shouldApplyLiveDelta` 分支执行），去重键未消费，具备「认领后补放」的条件。

### 0.3 现场判定树（先取证，再选修复面）

| 分支 | 证据组合 | 判定 | 修复面 |
|------|----------|------|--------|
| **A 回放补齐（已结束回合）** | store 帧 seq ≤ B；`/runtime` `active_turn=null`；事件 timestamp 陈旧 | 页面「不更新」是**正确行为**；缺陷 = 自愈误触发 + 面板误报 | L1 + L4 + L5 |
| **B 跨进程活跃回合** | store 帧 seq > B（或 `live:true`）持续出现；`/runtime` `active_turn=null` | 真活跃但身份不可见；需要前端从流上推断 + 认领 | L2 + L3（+ L1/L4/L5） |
| **C 同进程但 UI 未认领** | `/runtime` `active_turn` 非空 | 前端认领/刷新链路故障（fetch 失败、会话键不一致、线程尾不可认领） | 按 runbook 排查 + L5 暴露字段 |

取证命令与判读步骤见 runbook：DevTools 看帧上 `replay/live` 标记与 `: resumed from=A to=B` 注释；`curl /api/runtime/sessions/<id>/runtime` 看 `active_turn`。

---

## 1. 目标与不变式

**目标**

- G1 服务端确有**新输出**时，页面必须持续更新（不依赖 `active_turn` 是否可见）。
- G2 历史回放/补齐不得写入消息（方案 B 既有不变式）。
- G3 诊断不误报：回放拦截 ≠ 渲染故障。
- G4 自愈有界：同一回合的补救有上限、有终态，不成环。

**不变式**

- I1 每个增量只被 claim 一次（两通道共享 `deltaCoordinator`）；claim 在事件到达时执行、不进 updater。
- I2 只有「本回合仍在 streaming」的助手消息是写入目标；补建占位必须两侧身份明确相等（`resolveInFlightTurnId`）。
- I3 身份优先级：`localTurnId` > `resumedTurnId`（快照）> `inferredTurnId`（流推断）。
- I4 回放帧永不触发开闸、永不触发自愈、永不计入 `blockedDeltas`。
- I5 诊断 store 只接收上报，不参与业务判定。
- I6 无边界注释/无标记的旧后端：行为回退到今天（不推断、按原口径计 blocked），保证兼容。

---

## 2. 帧分类（唯一真源）

每次连接建立后维护本连接的边界：

- 收到 `: resumed from=A to=B` → 记录 `replayBoundary = { from: A, to: B }`；无该注释则为 `null`。
- 帧分类规则（按优先级）：

| 条件 | 分类 | 语义 | 允许开闸/推断 | 计数口径 |
|------|------|------|---------------|----------|
| `payload.live === true` | `bus-live` | 总线直投 live-only 帧（无 seq） | 可作活跃证据（E4） | liveDeltas |
| `payload.replay === true` 且 `seq > boundary.to` | `appended` | **连接建立后**服务端新落库的行 | 可作活跃证据（E3） | liveDeltas |
| `payload.replay === true` 且 `seq ≤ boundary.to` | `catchup` | 本次补齐的历史行 | 否（仅身份已可信时可渲染） | catchupDeltas |
| 本连接无 boundary（如 after=0 全量 dump） | `unknown` | 无法判定 | 否（保守） | 不计 blocked |
| 无 `replay`/`live` 标记（旧后端） | `unknown` | 兼容路径 | 否 | 按旧口径计 blocked、旧自愈 |

- 边界按连接维护：重连后重置，避免上一连接的 `to` 复用。
- `catchup` 帧在**身份已可信**（local/snapshot/inferred 且 `matchesActiveTurn` 通过）时照常渲染——这正是 P4 刷新续传要填的断档文本；身份不可信时只进事件快照，不入消息。

---

## 3. 修复设计

### L1 传输层：注释帧不再只当 keepalive

- `frontend/src/api/runtime/sse.ts`：handlers 增加可选 `onComment?(comment: string)`；`: keepalive` / `: open` / `: stream-retry` 照旧计入 keepalive，`resumed from=.. to=..` 额外回调 `onReplayBoundary({ from, to })`（保持既有「忽略 id:/retry: 行」测试不变）。
- `frontend/src/hooks/workspace/use-session-runtime-stream.ts`：`onReplayBoundary` 写入每次连接的 `replayBoundaryRef`；帧分类函数（纯函数，便于单测）产出 `origin`。
- 诊断帧样本增加 `origin` 字段（帧列表标注 `assistant_delta[catchup]` 之类），现场判读不再靠猜。

### L2 活跃证据与身份推断

**证据强弱（用于推断回合身份）**

- E1 本页 `localTurnId`（现状，最高优先）
- E2 `/runtime` `active_turn`（现状；**注意仅覆盖本进程回合**）
- E3 本连接 `appended` 帧携带的 `turn_id`（强：连接建立后服务端仍在写这个回合）
- E4 `bus-live` 帧携带的 `turn_id`（中）
- E5 仅 `catchup` 帧携带 `turn_id`（弱：可能是已结束回合的历史）→ **不作为开闸证据**

**推断状态 `inferredTurnId`**

- 存放：`use-workspace-live` 内以会话键控的 state（ref 镜像供事件回调同步读取）。
- 触发：`!liveTurnId` 且收到 E3/E4 帧且 `turn_id` 非空。
- 生效：先尝试认领（L3）；认领成功（线程已持有该 turn）→ 置 `inferredTurnId` → 此后 `liveTurnId = localTurnId ?? resumedTurnId ?? inferredTurnId`，闸门打开。
- 清除：该 turn 的终态事件到达（沿用既有终态归约点：finalize-turn / runtime 终态）；或 `FRESH` 窗口（建议 30s，可配）内没有该 turn 的新 E3/E4 帧；或快照/本地身份接管；或会话切换。
- 接入点：`activeTurnId` 与 `renderLiveDeltas` 都经 `liveTurnId` 统一，因此推断身份无需改流 hook 的渲染协议。

### L3 认领与写入目标（复用续传状态机）

- 把 `use-resumed-session-turn` 推广为**三种候选来源的统一状态机**：`local`（不认领）、`snapshot(active_turn)`、`inferred(E3/E4)`；快照优先于推断，local 仍然最高。
- 认领方式不变：`adoptResumedTurnInThread`（尾部半截助手消息 → `streaming + runtimeTurnId`）；成功即该 turn 有写入目标；失败（尾部不可认领）见下。
- 无认领目标时**允许补建占位**的收紧条件（这是与 v1 的关键区别）：
  - 帧必须为 E3/E4（活跃证据），且 `expectedTurnId = 推断身份` 与事件 `turn_id` 明确相等；
  - 即「活跃证据 + 双侧身份」双条件，才允许 `createInFlightAssistantTarget` 新建 `turn-<turnId>-assistant` 占位；
  - 纯 catchup / unknown 帧永远不允许新建消息（不为陈旧回放凭空建气泡）。
- **补放**：推断/认领生效后，从 `runtimeEventsRef` 中按 turn 过滤未 claim 的 delta（有 `deltaKey`、seq>0）按到达序重放一次，填补认领前被跳过的文本；受 `MAX_RUNTIME_EVENTS` 上限与历史同步兜底（可能截断，可接受）。
- 终态：沿用既有终态事件处理与 `releaseResumedTurnInThread`，避免气泡永久转圈。

### L4 自愈有界化（`use-workspace-live.ts`）

- `handleUnownedTurn` 只对 E3/E4 帧触发；`catchup` / `unknown` 不触发。
- 计数与退避：同一 turn 最多 3 次刷新（3s → 6s → 12s），用尽后置 `gaveUp`（`counters.unownedTurnGaveUp+1`，保留 `lastUnownedTurnId`），不再重试；快照出现该 turn、或收到其终态时清账。
- 既有 3s 节流保留为下限，避免抖动。

### L5 诊断（store / derive / panel）

- `counters` 新增：`catchupDeltas`、`snapshotRefreshFailures`、`unownedTurnGaveUp`；`blockedDeltas` 收紧为「活跃帧被拦」。
- `gate` 快照补齐展示：`localResponding`、`resumedTurnId`、`inferredTurnId`、`identitySource`（local | snapshot | inferred）、`lastGateChangeAt`（store 已有、面板未展示）。
- 帧列表显示 `origin`。
- `resolveLiveNetworkVerdict`：`render-blocked` 只由活跃被拦触发；catchup 单独提示（「回放补齐中，非渲染故障」），消除现场这种「面板喊渲染故障、其实是回放」的误报。
- `use-session-runtime-state` 的失败计数与最近错误上报到诊断（fetch 抛错时除 `error` 外给观测一个出口）。

### L6（可选/长期）后端活跃性契约

`active_turn` 跨进程不可见是结构性缺口，前端 L2/L3 已能恢复渲染；如需权威化，后续批次可选：

- a) 服务端在事件流中给出可归约的回合生命周期（持久化的 `turn.started` / `turn.finished`，或窗口端点暴露「最近 turn 是否活跃」）；
- b) 多实例部署时把注册表落到共享存储；
- c) `/runtime` 由 store 最新事件推断 `active_turn`（有误差，需谨慎评审）。

---

## 4. 实施步骤（文件级，按批次）

### 批次 1：口径与自愈修正（不改变渲染行为，可独立上线）

1. `frontend/src/api/runtime/sse.ts` + `sse.test.ts`：注释解析扩展（`onComment` / `onReplayBoundary`），keepalive 统计口径不变。
2. `frontend/src/hooks/workspace/use-session-runtime-stream.ts`：维护每连接的 `replayBoundary`；增加纯函数 `classifyRuntimeFrame(event, boundary)` 并单测。
3. `frontend/src/hooks/workspace/use-workspace-live.ts`：`handleUnownedTurn` 只对 E3/E4 触发；3 次上限 + 退避；`reportBlockedDelta` 仅活跃帧；新增 `reportCatchupDelta`。
4. `frontend/src/lib/live-diagnostics/{types,store,derive}.ts` + 面板 + i18n：新增计数与字段、`origin` 标注、结论口径修正。
5. `frontend/src/hooks/workspace/use-session-runtime-state.ts`：失败计数上报。

**验收批次 1**：重现回放场景（A）时，`blockedDeltas` 不再增长、`unownedTurns`/`snapshotRefreshes` 停止增长、面板不再显示「到达未渲染」；`catchupDeltas` 正常增长。

### 批次 2：身份推断与认领（渲染能力，带 flag）

6. `frontend/src/hooks/workspace/use-resumed-session-turn.ts`：泛化为 snapshot/inferred 双来源状态机（保持既有单测语义，新增参数默认关闭）。
7. `frontend/src/hooks/workspace/use-workspace-live.ts`：接入 `inferredTurnId`（E3/E4），并入 `liveTurnId`；上报 `identitySource`。
8. `frontend/src/lib/thread-state/events-live.ts`（或 `live-assistant-target.ts`）：补建占位的收紧条件 + 补放逻辑 + 单测。
9. flag：`lib/session-runtime/flags.ts` 增加 `session-runtime-inferred-turn`（默认关 → 验证后默认开）；回滚面 = 关 flag 即回批次 1 行为。

**验收批次 2**：B 场景（外进程活跃回合）下，连接后 1–2 帧内闸门打开、文本持续更新；A 场景下不产生任何新消息。

### 批次 3（可选）：L6 后端契约评审。

---

## 5. 测试计划

单元 / 集成（前端）：

- `sse.test.ts`：注释行 `: resumed from=3 to=5` 触发 `onReplayBoundary`；keepalive 计数不回归；`id:/retry:` 仍忽略。
- `classifyRuntimeFrame`：`live:true` → bus-live；`replay:true + seq≤B` → catchup；`replay:true + seq>B` → appended；无边界/无标记 → unknown。
- `use-session-runtime-stream`：
  - catchup delta（身份未知）→ 不渲染、不触发自愈、不计 blocked；
  - appended delta + turn_id（身份未知）→ 触发推断；
  - 身份可信时 catchup delta 正常渲染（P4 续传断档填补不回归）；
  - 补放：认领后重放被跳过的 delta，去重键只消费一次（与 chat 通道共享 coordinator 的既有用例扩展）。
- `use-workspace-live`：自愈 3 次上限 + 退避 + gaveUp；`inferredTurnId` 清除条件（终态 / FRESH 超时 / 身份接管 / 会话切换）。
- `use-resumed-session-turn`：双来源优先级与释放路径不回归。
- `live-diagnostics`：新计数/字段/结论口径；面板对 catchup-only 流量显示非阻塞提示。
- `resumed-turn` / `live-assistant-target`：收紧后的补建条件（仅 appended + 双侧身份）；system prompt 尾行仍不认领。

端到端（手动，按 runbook）：

- A（回放）与 B（外进程活跃）两个现场各自复现一次，核对面板四组数字与消息列 DOM。

---

## 6. 验收标准

1. B 场景：页面在 5s 内恢复增量渲染；`identitySource=inferred` 可见；消息正文与最终历史一致（不重复、不丢段）。
2. A 场景：无新增消息、无自愈刷新增长、面板结论为「回放补齐」而非「到达未渲染」。
3. C 场景：面板能直接读出 `localResponding/resumedTurnId/快照失败`，无需再靠 React DevTools。
4. 回归：既有「历史回放/reload 不渲染」「两通道增量只渲染一次」「刷新续传填补断档」「system prompt 不认领」四类测试全绿。

---

## 7. 回滚

- 批次 1 独立回滚：恢复 `reportBlockedDelta` 旧口径与自愈触发条件（注释清晰、无数据迁移）。
- 批次 2 回滚：关 flag（`session-runtime-inferred-turn`），行为回到批次 1；无持久化副作用。
- 诊断新增字段只增不改语义，无需回滚。

---

## 8. 未决问题（实施前需实测）

- `bus-live`（tool.progress 等）payload 是否携带 `turn_id`；若不带，E4 降级为不可用（不影响 E3 主路径）。
- `: resumed from=.. to=..` 只在「带游标且补到新行」时出现（`resumedAfterSeq > 0`）；`after=0` 的全量 dump 无边界 → 归 `unknown` 的保守策略是否符合现场（需确认首连游标不会为 0）。
- `MAX_RUNTIME_EVENTS` 的具体数值：决定补放窗口长度；超出部分依赖历史同步兜底。
- 多实例部署下事件流与 `/runtime` 可能命中不同实例 → L2 推断仍可工作，但 `active_turn` 的 E2 证据不可靠（文档已标注）。
- 现场需先按 runbook 判定 A/B/C，再决定验证批次 1 还是批次 2 优先。
