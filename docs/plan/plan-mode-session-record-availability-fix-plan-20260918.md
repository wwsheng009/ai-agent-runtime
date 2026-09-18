# 计划模式会话记录依赖与缺失可用性修复方案（SESSION_NOT_FOUND 韧性）

- 日期：2026-09-18
- 状态：**已实施 Phase 0/1/2/4（2026-09-18）**——错误分类、入口不变量、actor 自愈、可观测性全部落地并全绿，
  见 §11 实施记录；Phase 3（耐久性解耦）待产品决策；§13 Codex 参考实现对照已补充
- 关联：
  - `docs/plan/aicli-chat-session-messages-decouple-plan.md`（会话记录与消息面解耦）
  - `docs/plan/runtime-server-aicli-shared-session-mechanism-plan.md`（runtime-server 共享会话）
  - `docs/plan/session-user-turn-backtrack-plan.md`（同类"工具依赖持久会话行"的场景）
- 证据来源：本会话实测（`enter_plan_mode` 单次调用失败被误标为 `TOOL_PATH_NOT_FOUND`，实际根因是会话记录缺失）+
  代码走读（行号均为 2026-09-18 工作区状态）

## 0. 摘要

现象：模型调用 `enter_plan_mode` 后工具返回 `TOOL_PATH_NOT_FOUND`，模型的恢复指引被引导到
"检查路径 / 用 ls/glob"——而真实原因是**当前会话在会话存储中没有对应记录**（`SESSION_NOT_FOUND`）。

根因分两层：

1. **分类层缺陷（Phase 0，已修复）**：`SessionActor.loadSession` 返回无类型的
   `fmt.Errorf("session not found: %s", ...)`，broker 的错误分类器按文案落到通用
   `"not found" → TOOL_PATH_NOT_FOUND` 分支，导致工具级误标与错误的恢复指引。
2. **机制层缺陷（本方案主体）**：`enter_plan_mode` 不是纯内存的模式开关，而是
   **会话记录的读-改-写事务**（权威副本在会话记录，引擎只是每 turn 重建的执行副本）。
   由此派生出四类结构性问题：
   - **P1 自愈不对称**：CLI 的会话同步路径遇 `ErrSessionNotFound` 会自愈重建
     （`chat_session.go:655-677`），actor 工具路径却视为致命错误；
   - **P2 flush 靠调用点纪律**：未落库内存壳（deferred SQLite）的 flush 全仓仅 2 个调用点
     （`chat_actor_executor.go:141,246`），warmup 还显式跳过未落库壳（`chat_actor_warmup.go:22`）；
   - **P3 可用性耦合**：真正拦截工具的是内存引擎，但 durable 行缺失会让"切模式"整体失败；
   - **P4 三处权威**：会话 context（耐久）/ `RuntimeState.RunMeta` / permission engine，
     靠顺序调用同步，无单一权威。

本方案给出四条可独立落地的改造（A 入口不变量 / C 自愈 / B 方向敏感的耐久性解耦 / D 可观测性补全），
推荐第一阶段实施 **A1+A2+C**，在**不放松安全语义**的前提下消除"会话记录缺失即工具失败"。

## 1. 现象与基线

### 1.1 Phase 0 已完成（错误分类修复，全绿）

| 改动 | 文件 | 要点 |
| --- | --- | --- |
| 新增错误码 | `backend/internal/errors/codes.go` | `ErrSessionNotFound = "SESSION_NOT_FOUND"`（与既有字面量拼写一致） |
| 源头类型化 | `backend/internal/chat/actor.go` | `sessionNotFoundError()` / `wrapSessionStoreError()`；`loadSession`（store err + nil session）、`persistSession`（reload + update）全部返回类型化错误，broker 对 `*RuntimeError` 直接放行，不再走文案匹配 |
| 分类器兜底 | `backend/internal/toolbroker/broker.go:1086-1091` | 在 agent 专属 case 之后、通用 `"not found"` 分支**之前**新增 `"session not found" → SESSION_NOT_FOUND`，覆盖旧主机/未类型化文本 |
| 结构化码注册 | `backend/internal/agent/tool_execution_result_helpers.go:81` | `knownToolOutcomeErrorCode` 接受 `SESSION_NOT_FOUND` |
| 诊断与指引 | `backend/internal/toolresult/diagnostic.go:2034-2037,2110,2222-2223` | 消息推断补 `session not found` / `agent session not found`；新增 host 生命周期 next_action（"不要原样重试，需重建会话或修复存储"） |
| 回归测试 | `chat/plan_mode_tools_test.go`、`toolbroker/broker_plan_mode_test.go`、`toolbroker/broker_agent_test.go`、`toolresult/diagnostic_test.go`、`agent/tool_execution_result_helpers_test.go` | 覆盖"源头类型化""兜底不误标""结构化码保真" |

验证：`go build ./...` + `internal/{errors,toolbroker,toolresult,agent,chat,toolexec,observability}` 全绿。

### 1.2 未解决（本方案范围）

分类正确后，**工具依然会失败**——因为记录确实不存在。需要回答并解决：
记录为什么会不存在？缺失时应该自愈、报错、还是降级继续？耐久性是否应该阻断运行时控制操作？

## 2. 机制剖析（为什么是"读-改-写会话记录"）

### 2.1 权威副本 vs 执行副本

| 副本 | 位置 | 作用 | 生命周期 |
| --- | --- | --- | --- |
| **权威副本** | 会话记录 context：`plan_mode` 状态（`planmode/state.go:20-21,112,136-140`）+ `permission_mode` / `requested_permission_mode` / `effective_permission_mode`（`plan_mode_tools.go:16-19,198-200`） | 跨进程/恢复/界面的一致真相 | 持久化 |
| **执行副本** | `policy.Engine`（Mode + PlanWriteAllowPaths）与 `RuntimeState.RunMeta` | 工具调用点真正拦截写操作 | 进程内、每 turn 重建 |

关键证据（`actor.go:2602-2606` 注释原文）：

```go
// Re-apply plan mode after prepareRun so durable plan_mode context and
// session permission mode stay enforced for this turn.
a.applyPlanModeStateToEngine(engine, planmode.Load(session))
```

每个 turn 开始时引擎都从会话记录**重新投影**——记录是真相，引擎是缓存。
因此 `EnterPlanMode` 的顺序是：`loadSession → planmode.Load/Save → applySessionPermissionMode → persistSession → applyPlanModeStateToEngine + syncLivePermissionMode`
（`plan_mode_tools.go:38-64`）。外部看到的"读-改-写"是这条链的前半段。

### 2.2 为什么必须是状态机（不是布尔开关）

进入/退出需要读旧状态才能计算转移（`plan_mode_tools.go:43-52,90-106,108-122`）：

- 嵌套 enter 保留最初的 `PreviousMode`（46-48）；
- enter 时若当前已是 plan 但无 previous，回退 `default`（49-52）；
- exit 需要判断 `IsActive/StatusExited`，兼容"只有 `permission_mode=plan` 无 durable state"的裸状态（92-97）；
- `request_changes` 保持 active 并记录决策（108-122）；
- 退出后要按 `PreviousMode` 恢复（`ResumeModeAfterExit`，105-106）。

这些是数据（status/previous mode/plan path/pending exit/decision/notes），必须持久化才能跨恢复保持安全姿态。

### 2.3 消费方清单（同一份记录，多宿主读取）

| 消费方 | 位置 | 用途 |
| --- | --- | --- |
| actor 每 turn 投影 | `actor.go:2602-2606`、`actor.go:5138-5149` | 从记录重建引擎模式 |
| 计划工具（本地） | `plan_mode_tools.go:43,90,231` | enter/exit/结果构造 |
| HTTP/Web 面 | `internal/api/skills/plan_mode_handlers.go:199-276` | 服务端同构 enter/exit/查询 |
| CLI 宿主 | `chat_actor_host.go:1892-1896` | 工具策略与 warmup 读取 |
| CLI UI/命令 | `chat_interaction.go:2647`、`chat_plan_command.go:229-366` | 状态展示与 `/plan` |
| 权限模式切换 | `permission_mode.go:56-92` | 切离 plan 时收口、模式落会话 context |

结论：状态放会话记录是**多面一致性的必然要求**，不是实现冗余。

### 2.4 "记录不存在"的实际来源（按可能性排序）

| # | 机制 | 证据 | 说明 |
| --- | --- | --- | --- |
| M1 | **未落库内存壳**：Deferred 存储（lazy SQLite）新会话保持内存态 `runtimeSessionUnpersisted=true`，直到首次真实内容/关闭才 flush | `chat_session.go:252-285,331-346,662-677`；`chat.go:110` | 会话"存在"于内存，但不在此刻读的 store 里 |
| M2 | **flush 不是不变量**：全仓仅 2 个调用点，warmup 显式跳过未落库壳 | `chat_actor_executor.go:141,246`；`chat_actor_warmup.go:22` | 绕开 executor 的入口（trigger-turn drain、远程派发、部分 resume）可能撞上窗口 |
| M3 | **外部删除无自愈（actor 侧）**：CLI sync 有 Save 自愈，actor `loadSession` 没有 | `chat_session.go:655-677` vs `actor.go` `loadSession` | 注释明说 `ErrSessionNotFound` 是"TTL 过期/手动清理的罕见竞态"——但只对 CLI sync 自愈 |
| M4 | **多后备存储可切换**：本地持久化 / 内存临时（初始化失败回退）/ 远程 runtime-server（404→`ErrSessionNotFound`）/ exec 内存态 | `chat_bootstrap.go:70-78`；`chat_runtime_server.go:165-216,374-375`；`exec_run.go:357` | 会话在 A store，actor 绑定 B store |
| M5 | **actor 与记录无一致性约束**：`NewSessionActor` 不校验记录；Hub 会驱逐重建 actor | `actor.go:224-282`（仅 `loadState`）；`hub.go:129,172-232` | "actor 存在 ⇒ 记录存在"从未被强制 |
| M6 | **标识大小写/规范化差异**被判为 not found | `file_storage.go:448-454`；`file_storage.go:459-474` | 少见但真实 |

## 3. 设计问题清单

- **P1 自愈不对称**（M3）：同一系统对"记录消失"有两套语义，工具路径更严格且无恢复。
- **P2 flush 依赖调用点纪律**（M1/M2）：缺少"任何 durable 依赖操作前必须已落库"的不变量。
- **P3 可用性耦合**：`plan_mode_tools.go:38-41`（load 失败即返回）与 `:57-59`（persist 失败即返回，引擎不更新）——
  真正生效的内存门因存储不可用而无法切换。对比 `checkpointSessionHistory`（`actor.go:2986-3008`）
  的 best-effort 语义（失败只上报事件、绝不冒泡），设计取向不一致。
- **P4 三处权威**：会话 context / RunMeta / engine 靠调用顺序同步，跨进程可能与记录分叉。
- **P5（次要）分类与可观测性**：`SESSION_NOT_FOUND` 尚未进入
  observability 分桶与 toolexec 重放熔断的显式名单（Phase 4）。

## 4. 修复目标与原则

**目标**

1. 计划模式/权限模式类工具不再因为"会话记录暂不可读/未落库"而失败，或在必须失败时给出可执行的宿主级指引。
2. 消除"记录消失"的结构性来源：未落库壳不再能被 durable 依赖路径撞见。
3. 保持安全语义：**收紧方向不得静默降级**；放松方向允许 fail-safe 的降级路径。

**原则**

- **安全优先于可用性**：`enter`（收紧）方向的持久化仍是硬要求；`exit`（放松）方向可先内存后持久化。
- **入口不变量优于事后补偿**：能在 actor 创建/回合入口消除的窗口，不要留给工具中途处理。
- **单一自愈语义**：actor 路径与 CLI sync 路径采用同一套 "not-found → 重建/落库" 口径。
- **可观测**：每一次自愈/降级都必须发事件，不允许静默。

## 5. 方案设计

### 5.1 方案 A：入口不变量（推荐，Phase 1）

#### A1. flush 前移并去重（消除 M1/M2）

**现状**：`ensureChatRuntimeSessionPersisted(session)`（`chat_session.go:331-346`）只在
`chat_actor_executor.go:141,246` 两处被调用；warmup 显式跳过未落库壳（`chat_actor_warmup.go:22`）。

**改造**：

1. 抽出幂等入口助手（命名建议 `ensureSessionDurableBeforeActor(ctx, session)`）：
   - `session == nil || !session.runtimeSessionUnpersisted` → 直接返回（零开销）；
   - 否则调用现有 `ensureChatRuntimeSessionPersisted`；
   - 失败时返回带上下文的宿主级错误（不进入 actor 构建）。
2. 调用点补齐（按优先级）：
   - `chat_actor_host.go` actor 工厂（`sessionStore := session.SessionManager.GetStorage()`，约 973 行）**之前**：
     在构建 `NewSessionActor` 前确保记录可 Load；
   - `chat_actor_warmup.go:22`：把"跳过未落库壳"改为"先 flush，再 warmup"（flush 失败跳过 warmup 并记录事件，不阻断用户回合）；
   - `chat_actor_executor.go:141,246`：保留（已是兜底）；
   - `TriggerTurnDrain`（`actor.go` drain 入口）与 followup/team 派发路径：确认这些入口是否可能首次触发未落库会话；若是，一并接入。
3. 语义保证：助手幂等、无 store 往返（flag 判断在前），可在多入口重复调用。

**验收**：构造 deferred 存储的新会话，在 flush 前直接走 actor 工厂/plan 工具路径，不再产生 `SESSION_NOT_FOUND`；
flush 失败时错误信息包含"会话未落库/无法落库"的宿主语义。

#### A2. actor 创建时的记录校验（消除 M5）

**现状**：`NewSessionActor`（`actor.go:224-282`）只 `loadState`（runtime store），不校验 session store。

**改造**（二选一，建议方案 A2-a）：

- **A2-a（保守）**：actor 工厂在 `NewSessionActor` 成功后做一次轻量探针 `sessionStore.Load(ctx, sessionID)`：
  - 成功 → 继续；
  - `ErrSessionNotFound` → 触发 A1 flush 后重试一次；
  - 仍失败 → 返回宿主级错误（含 session_id、存储类型），在**回合开始前**暴露，而不是工具中途。
- **A2-b（激进）**：在 `SessionActor` 内加 `ensureDurableSession(ctx)`，由 `loadSession` 自动调用（与方案 C 合并）。

> 取舍：A2-a 把成本放在 actor 创建（一次性），且不改 actor 内部语义；A2-b 覆盖所有工具但引入隐式 IO。
> 推荐 A2-a + 方案 C 组合（入口兜底 + 工具内自愈）。

### 5.2 方案 C：`loadSession` 自愈（推荐，Phase 2）

**现状**：CLI sync 路径在 `Update → ErrSessionNotFound` 时 fallback `Save` 重建
（`chat_session.go:655-677`，注释明说这是"已持久化会话被外部删除（TTL 过期/手动清理）时的罕见竞态恢复路径"）；
actor 的 `loadSession` 无此语义。

**改造**：

1. 在 `SessionActorConfig` 增加宿主注入钩子（命名建议）：
   ```go
   // EnsureSession 在记录缺失时尝试由宿主把内存快照落库（返回 nil 表示已恢复）。
   EnsureSession func(ctx context.Context, sessionID string) error
   ```
   由 `chat_actor_host.go` 注入为 `ensureChatRuntimeSessionPersisted(session)`（复用现有 Save 语义）。
2. `loadSession` 流程改为：
   ```
   Load
     ├─ ok → 返回
     ├─ ErrSessionNotFound 且 hook != nil → 调用 hook（单飞，防抖）
     │     └─ 成功后重试 Load 一次
     └─ 仍失败 → 返回类型化 SESSION_NOT_FOUND（Phase 0 已实现）
   ```
3. 防抖/单飞：actor 内 `sync.Mutex` + 短 TTL（如 5s）的负缓存，避免同一 turn 内多工具重复触发。
4. `persistSession` 的 `Update` 分支同样补自愈：`Update → ErrSessionNotFound` 时改用 `Save`
   （与 `chat_session.go:667-675` 完全同构；`persistSession` 已持有完整 `session` 对象，可直接落库）。
5. **防腐边界**（必须）：
   - 仅当 actor 有活跃 run / 当前进程持有该会话的快照时才允许重建（避免复活用户已 `/clear` 或 TTL 清理的会话）；
   - 自愈必须发事件（建议 `session_recovered_from_missing_row`，含 session_id、trigger=tool_load/update）；
   - 远程 runtime-server 模式（`chat_runtime_server.go`）下 hook 由同一 `ensureChatRuntimeSessionPersisted` 承担，
     其 Save 走 HTTP POST（服务端创建语义），天然幂等；**若服务端明确返回 404 也需区分"已删除"与"不同实例"**，
     建议在服务端响应中带 `deleted_at`/`instance_id` 供宿主决策（Phase 2 可选项）。

### 5.3 方案 B：方向敏感的耐久性解耦（可选，Phase 3，需产品决策）

**动机**：P3 希望"存储抖动不阻断模式切换"。但无差别地改成"先内存后持久化"会引入安全回归，需按方向区分：

| 方向 | 先内存后持久化的后果（持久化失败 + 进程崩溃） | 结论 |
| --- | --- | --- |
| **enter（收紧）** | durable 仍为 default → resume 后**写权限被恢复**（fail-open，危险） | **保持 durability-first**（现状正确） |
| **exit（放松）** | durable 仍为 plan → resume 后仍是计划模式（fail-safe，只损失一次操作） | 可改为内存先行 + best-effort 持久化 |

**改造**（仅 exit 方向）：

1. `ExitPlanMode` 调整顺序：状态机计算 → `applyPlanModeStateToEngine` + `syncLivePermissionMode`（立即生效）
   → `persistSession` best-effort；
2. 持久化失败：发 `plan_mode_persist_failed` 事件并返回**成功**结果（附 `persisted=false` 字段/警告文案），
   由回合结束的 post-turn sync（`persistSession` 兜底）或下一次 checkpoint 重试；
3. 灰度开关：建议 `plan.exit_best_effort_persist`（默认关，显式开启）；开启前需补"崩溃后 durable 仍是 plan"的语义测试；
4. `enter` 方向维持现状（持久化成功后才更新引擎），但可增加"持久化失败时给出可操作的宿主指引"。

> 该方案涉及安全语义，建议在 A/C 落地并观察一段时间后再评审。

### 5.4 方案 D：分类与可观测性补全（Phase 4）

1. **可观测分桶**：在 observability 的错误分类中显式映射 `SESSION_NOT_FOUND` → `session_lifecycle`
   （实施前确认具体函数，避免落到默认 `other_error`）；同时确认 `AGENT_SESSION_NOT_FOUND` 的归属。
2. **重放熔断**：核查 `internal/toolexec` 的终端错误名单（重复调用熔断表），将
   `SESSION_NOT_FOUND` / `AGENT_SESSION_NOT_FOUND` 标记为"不建议原样重放"（实施前确认具体函数名与语义）。
3. **会话诊断**：会话状态诊断接口（若已有 session-info/诊断命令）增加 `session_persisted` 字段，
   让用户/模型能直接看到"内存态未落库"，而不是等工具失败。

### 5.5 方案对比与推荐

| 方案 | 解决 | 改动面 | 风险 | 建议 |
| --- | --- | --- | --- | --- |
| A1 flush 不变量 | M1/M2 | `cmd/aicli` 宿主层，小 | 低（warmup 延迟一次落库） | **实施** |
| A2-a 创建校验 | M5 | actor 工厂，小 | 低（一次 Load） | **实施** |
| C 自愈 + Update→Save | M3 | `internal/chat` + 宿主钩子，中 | 中（复活语义需防腐边界） | **实施（带护栏）** |
| B exit 方向解耦 | P3 | `plan_mode_tools.go` + 事件，中 | 中（安全语义，需灰度） | 评审后实施 |
| D 可观测性 | P5 | 小 | 低 | **实施（快）** |

## 6. 分阶段实施步骤

### Phase 1：入口不变量（A1 + A2-a）

| 步骤 | 文件 / 位置 | 改动要点 | 完成标准 |
| --- | --- | --- | --- |
| 1.1 | `backend/cmd/aicli/commands/chat_session.go` | 新增幂等助手 `ensureSessionDurableBeforeActor(ctx, session) error`：仅当 `runtimeSessionUnpersisted` 时走 `ensureChatRuntimeSessionPersisted`；错误包装宿主语义（session_id、存储后端） | 助手有单测；已落库会话零 store 往返 |
| 1.2 | `backend/cmd/aicli/commands/chat_actor_host.go:973-974`（工厂） | 在 `sessionStore := session.SessionManager.GetStorage()` 后、构建 actor 前调用 1.1；失败直接返回宿主级错误 | 构造未落库壳会话时，actor 不会被创建；或创建前已自动落库 |
| 1.3 | `backend/cmd/aicli/commands/chat_actor_warmup.go:22` | 把"跳过未落库壳"改为"先 flush 再 warmup"；flush 失败记录事件并跳过 warmup（不阻断回合） | 新会话首回合 warmup 前记录已落库 |
| 1.4 | `backend/cmd/aicli/commands/chat_actor_executor.go:141,246` | 保留；评估是否统一改调 1.1（语义等价，减少入口分叉） | 行为不变，测试全绿 |
| 1.5 | `chat_actor_host.go` actor 工厂（`NewSessionActor` 调用点，约 1288 行） | A2-a：actor 构建成功后做一次 `sessionStore.Load` 探针；`ErrSessionNotFound` → 触发 1.1 后重试一次；仍失败返回宿主级错误 | 未落库/已删除会话在回合开始前得到确定结论 |
| 1.6 | `TriggerTurnDrain` / followup / team 派发入口 | 排查这些入口是否可能首次触达未落库会话；若是接入 1.1（以排查结论为准） | 排查记录写入本方案 §11 |

### Phase 2：actor 自愈（C）

| 步骤 | 文件 / 位置 | 改动要点 | 完成标准 |
| --- | --- | --- | --- |
| 2.1 | `backend/internal/chat/actor.go`（`SessionActorConfig` 定义处） | 新增可选钩子 `EnsureSession func(ctx context.Context, sessionID string) error` | 字段注释说明契约与调用时机；默认 nil 时行为与现状一致 |
| 2.2 | `backend/internal/chat/actor.go`（`loadSession`，Phase 0 已改区域） | Load 返回 `ErrSessionNotFound` 且 hook 非 nil → 单飞调用 hook → 成功则重试 Load 一次；仍失败返回类型化错误 | 新增单测：hook 恢复成功 / hook 失败 / hook 为 nil 三态 |
| 2.3 | `backend/internal/chat/actor.go`（`persistSession`，reload/Update 区域） | `Update → ErrSessionNotFound` 时改用 `Save`（与 `chat_session.go:667-675` 同构）；失败仍返回类型化错误 | 新增单测：Update 遇 not-found 自动 Save 成功 |
| 2.4 | `backend/internal/chat/actor.go`（加载状态辅助） | 防抖：`sync.Mutex` + 短 TTL（建议 5s）负缓存，避免同一 turn 内风暴式重试 | 单测：两次连续失败只触发一次 hook |
| 2.5 | `backend/cmd/aicli/commands/chat_actor_host.go` | 注入 hook = `ensureChatRuntimeSessionPersisted(session)`（复用 CLI 现有 Save 语义） | 端到端：外部删除记录后工具自愈继续 |
| 2.6 | 事件与护栏 | 新增事件 `session_recovered_from_missing_row`（session_id、trigger=load/update、store_backend）；仅当会话有活跃 run 或宿主持有快照时允许重建；`/clear`、TTL 清理语义需在实现时确认不会误复活 | 事件在测试中断言；护栏逻辑有单测（"用户已删除"场景不复活） |

### Phase 3：exit 方向 best-effort 持久化（B，可选，需评审）

| 步骤 | 文件 / 位置 | 改动要点 | 完成标准 |
| --- | --- | --- | --- |
| 3.1 | `backend/internal/chat/plan_mode_tools.go`（`ExitPlanMode`，70-140） | 顺序调整为：状态机 → 引擎/RunMeta 立即生效 → `persistSession` best-effort | 功能开关关闭时行为与现状一致 |
| 3.2 | 事件与返回值 | 持久化失败发 `plan_mode_persist_failed` 事件；结果带 `persisted=false`，不回滚内存态；由 post-turn sync / checkpoint 重试 | 崩溃语义测试：重启后 durable 仍为 plan（fail-safe） |
| 3.3 | 灰度开关 | `plan.exit_best_effort_persist`（默认关）；`enter` 方向保持 durability-first 不变 | 开关文档与测试；默认路径全绿 |

### Phase 4：分类与可观测性（D，快）

| 步骤 | 文件 / 位置 | 改动要点 |
| --- | --- | --- |
| 4.1 | `backend/internal/observability`（错误分桶） | 显式映射 `SESSION_NOT_FOUND` → `session_lifecycle`；确认 `AGENT_SESSION_NOT_FOUND` 归属 |
| 4.2 | `backend/internal/toolexec`（重放/熔断名单） | 核查终端错误名单，加入 `SESSION_NOT_FOUND` / `AGENT_SESSION_NOT_FOUND` |
| 4.3 | 会话诊断命令/接口 | 增加 `session_persisted`（是否已落库）字段，便于自诊断 |

## 7. 测试计划

### 7.1 单元测试

| 包 | 用例 | 断言 |
| --- | --- | --- |
| `internal/chat` | `TestSessionActorEnterPlanModeTypedSessionNotFound`（已有） | 源头返回 `SESSION_NOT_FOUND`，且非 `TOOL_PATH_NOT_FOUND` |
| `internal/chat` | 新增：hook 自愈成功 | `loadSession` 在 hook 恢复后成功返回，且仅调用 hook 一次（防抖） |
| `internal/chat` | 新增：hook 失败/未注入 | 返回类型化 `SESSION_NOT_FOUND`，不 panic、不无限重试 |
| `internal/chat` | 新增：`persistSession` Update→Save | 遇 not-found 时自动降级 Save 成功 |
| `internal/chat` | 新增：防抖负缓存 | 5s 内两次调用只触发一次 hook（用注入时钟） |
| `cmd/aicli/commands` | 新增：`ensureSessionDurableBeforeActor` | 未落库 → 调用 flush；已落库 → 零调用；flush 失败 → 宿主级错误文本含 session_id |
| `cmd/aicli/commands` | 新增：actor 工厂探针（A2-a） | 记录缺失 → flush 后重试；仍失败 → 不构建 actor 并报错 |
| `toolbroker` / `toolresult` / `agent` | Phase 0 已覆盖 | 兜底与结构化码保真 |

### 7.2 集成测试

1. **deferred 存储新会话 + 计划工具**（M1/M2）：
   以 lazy SQLite（deferred）建新会话（`runtimeSessionUnpersisted=true`）→ 不发送真实消息直接触发 actor/plan 路径 →
   Phase 1 后应自动落库并成功，或（若入口未覆盖）由 Phase 2 自愈成功；断言全程无 `SESSION_NOT_FOUND` 失败。
2. **外部删除竞态自愈**（M3）：
   会话已落库 → 运行中从存储删除该行 → 调用 `enter_plan_mode` → 断言 hook 触发、事件
   `session_recovered_from_missing_row` 发出、工具成功。
3. **远程 runtime-server 模式**（M4）：
   复用 `chat_runtime_server.go` 的 storage 测试：Load 收到 404 → `ErrSessionNotFound` → hook（HTTP Save）
   → 重试 Load；断言幂等（重复 Save 不产生重复会话）。
4. **安全回归**：`enter` 方向持久化失败（注入 store 写失败）→ 断言引擎模式**未**切换、工具返回错误、
   不产生"内存已 plan 但 durable 未 plan"的分叉。
5. **护栏**：模拟用户主动删除（`/clear` 或 TTL 清理）→ 断言不自愈复活、返回类型化错误与正确指引。

### 7.3 手工验证 Runbook（本地）

1. 以 deferred SQLite 配置启动 `aicli chat`，新建会话（不发送消息），直接要求模型切换计划模式；
2. 观察：是否出现 `SESSION_NOT_FOUND`；若出现，检查是否在 flush 前触达（日志/事件 `session_start` 前是否有落库动作）；
3. 用会话信息命令/存储检查确认记录存在（`session_persisted=true`）；
4. 运行中手工删除会话行，再触发 `enter_plan_mode`，确认自愈事件与成功结果；
5. 关闭自愈开关（若实现为开关）复验原始错误路径仍为类型化 + 可执行指引。

### 7.4 回归范围

```
cd backend
go build ./...
go test ./internal/chat/ ./internal/toolbroker/ ./internal/toolresult/ ./internal/agent/ -count=1
go test ./cmd/aicli/commands/ -run 'TestSessionActorEnterPlanMode|TestEnsureSessionDurable|TestChatActor' -count=1
```

（`cmd/aicli/commands` 全量测试体量大，按目标用例先跑，合入前跑全量或 CI 全量。）

## 8. 风险与回滚

| 风险 | 影响 | 缓解 | 回滚 |
| --- | --- | --- | --- |
| 自愈"复活"用户主动删除/清理的会话 | 语义错误、数据残留 | 护栏：仅活跃 run 或宿主持有快照时允许；事件留痕；`/clear`/TTL 场景专项测试 | 开关 `sessions.actor_self_heal`（建议默认开，可显式关） |
| warmup 前移 flush 增加延迟 | 新会话首回合变慢（deferred 存储的懒打开被提前） | 仅在 actor 构建/首回合前触发一次；已落库会话零开销 | 回退 1.3；保留 executor 现有 flush 兜底 |
| A2-a 探针增加一次 Load | actor 创建多一次 store 读 | 仅对"可疑"会话（未落库/远端模式）执行；本地已落库可跳过 | 回退 1.5 |
| Phase 3 放松 exit 持久化 | 崩溃后丢失一次 exit（保持 plan） | 方向敏感策略：仅 exit 方向；灰度默认关 | 关闭 `plan.exit_best_effort_persist` |
| 远程模式 HTTP Save 语义差异 | 自愈可能创建重复会话 | 幂等键/服务端按 client_session_id 去重（Phase 2.5 可选增强） | 远程模式先只读重试，不 Save |

## 9. 验收标准

1. **分类**：会话记录缺失在工具层只会以 `SESSION_NOT_FOUND`（或 `AGENT_SESSION_NOT_FOUND`）出现，
   不再出现 `TOOL_PATH_NOT_FOUND`；next_action 为宿主生命周期指引。（Phase 0 已达成）
2. **入口**：deferred 存储新会话在 actor 构建/首回合前已落库；flush 失败的报错为宿主级、
   含 session_id 与存储后端。（Phase 1）
3. **自愈**：外部删除竞态下，`enter_plan_mode` 成功恢复并发出
   `session_recovered_from_missing_row`；用户主动删除场景**不**复活。（Phase 2）
4. **安全**：`enter` 方向持久化失败时引擎**不**切换、无内存/durable 分叉；Phase 3 若启用，
   `exit` 崩溃后 durable 仍为 plan（fail-safe）。（Phase 1/3）
5. **可观测**：`SESSION_NOT_FOUND` 进入 `session_lifecycle` 分桶，`path_missing` 不再被污染。（Phase 4）
6. **测试**：§7 用例全绿；`go build ./...` + 相关包全量测试通过。

## 10. 证据索引（file:line）

| 主题 | 位置 |
| --- | --- |
| `EnterPlanMode` 读-改-写链 | `internal/chat/plan_mode_tools.go:23-67`（load:38、state:43-55、persist:57-59、engine/sync:61-64） |
| `ExitPlanMode` 状态机 | `internal/chat/plan_mode_tools.go:70-140`（分支:90-106、request_changes:108-122） |
| 会话 ID 护栏 | `internal/chat/plan_mode_tools.go:142-151` |
| 权限模式三个 context 键 | `internal/chat/plan_mode_tools.go:16-19,198-200` |
| plan 状态存储键与读写 | `internal/planmode/state.go:20-21,112,136-140` |
| 每 turn 从记录投影引擎 | `internal/chat/actor.go:2602-2606`、`:5138-5149` |
| actor 构造不校验记录 | `internal/chat/actor.go:224-282`（仅 `loadState`） |
| Phase 0 类型化位置 | `internal/chat/actor.go`：`sessionNotFoundError`/`wrapSessionStoreError`/`loadSession`/`persistSession`（约 2898-3010） |
| broker 分类器兜底 | `internal/toolbroker/broker.go:1086-1091` |
| 诊断推断与指引 | `internal/toolresult/diagnostic.go:2034-2037,2110,2222-2223` |
| 已知结构化码 | `internal/agent/tool_execution_result_helpers.go:81` |
| 未落库壳标记与落库策略 | `cmd/aicli/commands/chat.go:110`；`chat_session.go:252-285,331-346,655-677` |
| flush 调用点 | `chat_actor_executor.go:141,246`；`chat_actor_warmup.go:22` |
| 内存回退（bootstrap/exec） | `chat_bootstrap.go:70-78`；`exec_run.go:357` |
| 远程存储适配与 404 映射 | `chat_runtime_server.go:165-216,374-375` |
| actor 工厂存储来源 | `chat_actor_host.go:973-974,1288-1294` |
| hub 重建/驱逐 | `internal/chat/hub.go:129,172-232` |
| 文件存储 not-found 语义 | `internal/chat/file_storage.go:435-474`（大小写守卫:448-454） |
| SQLite Load/Cleanup | `internal/chat/sqlite_storage.go:1206-1221,2450-2458` |
| 权限模式落会话 context | `internal/chat/permission_mode.go:56-92` |
| 服务端同构 enter/exit | `internal/api/skills/plan_mode_handlers.go:199-276` |
| 每 turn 投影（消费方） | `chat_actor_host.go:1892-1896`；`chat_interaction.go:2647`；`chat_plan_command.go:229-366` |
| best-effort 持久化范式 | `internal/chat/actor.go:2986-3008`（`checkpointSessionHistory`） |
| Phase 0 测试 | `internal/chat/plan_mode_tools_test.go`（新增 fixture + 2 用例）；`internal/toolbroker/broker_plan_mode_test.go`；`internal/toolbroker/broker_agent_test.go:1418-1430`；`internal/toolresult/diagnostic_test.go:531-533,557-560`；`internal/agent/tool_execution_result_helpers_test.go:35-41` |

## 11. 实施记录

- 2026-09-18：Phase 0 完成（错误码/类型化/兜底/诊断/回归测试），全绿；本方案文档创建。
- 2026-09-18：**Phase 1 / Phase 2 / Phase 4 实施完成**，全绿。明细：

| Phase | 落地内容 | 文件 |
| --- | --- | --- |
| 1.1 | `ensureSessionDurableBeforeActor`（幂等 flush 助手） | `cmd/aicli/commands/chat_session.go` |
| 1.2 | actor 工厂入口不变量：基础会话在 `NewSessionActor` 前执行 flush + 行探针 | `cmd/aicli/commands/chat_actor_host.go`（`buildSessionActor`） |
| 1.3 | 保持 warmup 惰性跳过（偏差说明见下） | `cmd/aicli/commands/chat_actor_warmup.go` |
| 1.4 | 两个 turn 入口统一切换到新助手 | `cmd/aicli/commands/chat_actor_executor.go`（2 处） |
| 1.5 | A2-a 探针 `ensureSessionRowLoadable`：Load →（not-found）宿主恢复一次 → 复检；仍失败返回宿主级入口错误。**探针放在 `NewSessionActor` 之前**（偏差说明见下） | `chat_session.go` + `chat_actor_host.go` |
| 2.1-2.4 | `SessionActorConfig.EnsureSession` 钩子；`loadSession` 自愈（单飞 + 5s 负缓存）；`persistSession` reload/Update not-found → `Save` 重建；`session_recovered_from_missing_row` 事件（trigger=load/persist_reload/persist_update） | `internal/chat/actor.go` |
| 2.5 | 宿主注入 `restoreChatRuntimeSessionRow`：仅基础会话；拒绝跨会话 ID、closed/archived 状态；`AICLI_SESSION_SELF_HEAL=0` 可关 | `chat_session.go`（`localChatActorEnsureSession` 在 `chat_actor_host.go`） |
| 4.1 | `SESSION_NOT_FOUND` / `AGENT_SESSION_NOT_FOUND` → `session_lifecycle` 分桶 | `internal/observability/tool_efficiency_snapshot.go` |
| 4.2 | 两个码加入终态熔断名单（不再原样重放） | `internal/toolexec/memory.go` |

**与文档的两处偏差（已论证）**：

1. **1.3 warmup 未改为"先 flush 再 warmup"**：`chat_setup.go:449` 在启动期调用 warmup，对未落库壳强制 flush 会破坏
   lazy SQLite 的惰性打开（文档自身的目标之一）。改为把不变量放在**唯一构建路径** `buildSessionActor`
   （`chat_actor_host.go:1136-1138` 工厂闭包），warmup 保留惰性跳过并在注释中指向该不变量。
2. **1.5 探针放在 `NewSessionActor` 之前**：记录不可恢复时无需构造 actor、无需清理租约，入口失败更早更干净；
   同时探针内含一次宿主恢复尝试（复用 Phase 2 的 `restoreChatRuntimeSessionRow`）。

**Phase 1.6 入口排查结论**：本地 host 的 actor 生产路径唯一（`SessionHub` 工厂闭包 → `buildSessionActor`），
因此工厂级不变量覆盖 executor / warmup / trigger-turn drain / followup / registry 等全部 `GetOrCreate` 入口；
子会话（非 base）不触发 flush/探针，避免用子 ID 触碰基础会话行。远程 runtime-server 模式不受本改动影响（其会话在服务端管理）。

**测试与验证（2026-09-18）**：

```
go build ./...                                                          ✓
go test ./internal/chat/             (full, 含 4 个新自愈测试)            ✓
go test ./internal/toolbroker/ ./internal/toolresult/ ./internal/agent/ ✓
go test ./internal/observability/ ./internal/toolexec/ (full, 含新测试)   ✓
go test ./cmd/aicli/commands/ -run 'Actor|Session'  (含 7 个新持久性测试) ✓
```

新增测试：`internal/chat/actor_session_self_heal_test.go`（自愈成功/负缓存/持久重建/无钩子保持类型化错误）、
`cmd/aicli/commands/chat_session_durability_test.go`（shell flush/跳过已持久/恢复行/拒跨会话/拒 closed/开关生效/探针恢复）、
`internal/observability/session_lifecycle_bucket_test.go`、`internal/toolexec/session_lifecycle_circuit_test.go`。

**未实施（待决策）**：Phase 3（`exit` 方向 best-effort 持久化 + `plan.exit_best_effort_persist` 灰度开关）；
§12-6 的"事件追加式耐久"（Codex §13.5 建议）留作后续评审项。

**远程端到端冒烟测试（2026-09-18，运行程序 `backend/aicli-2x.exe`，含本修复）**：

- 入口：`POST /web/api/invoke`（session `session_20260918142217_xhi5qmru`，新建空会话 = deferred shell 场景），
  prompt 要求真实调用 `enter_plan_mode` → `exit_plan_mode(decision=approve)`。
- 结果：`status=completed`，`steps=8`，turn 记录无 `error`；`enter_plan_mode` → `status=active`（含 `write_allow_paths`），
  `exit_plan_mode` → `status=exited`；会话 `message_count` 0 → 18；`/web/api/analysis/errors` 无任何会话类错误。
- 全文转录检索 `session not found` = **0 次**；出现的 2 次 `TOOL_PATH_NOT_FOUND` 经核对为真实路径问题
  （会话 cwd=`backend`，模型先用相对路径 `docs/plan/...`，修正为仓库根绝对路径后成功），与 SESSION_NOT_FOUND 无关。
- 证据留存：`output/plan-mode-smoke-response.json`、`output/smoke-screen.json`、`docs/plan/remote-plan-mode-smoke-test.md`（工具产物）。
- 限制：Phase 2 自愈事件（`session_recovered_from_missing_row`）需"活动会话行被外部删除"才能触发，
  而 Web API 明确禁止删除当前活动会话，故无法在无破坏前提下远程复现；该路径由 4 个单测覆盖。
- 2026-09-18 追加修复（冒烟测试衍生的独立缺陷）：`enter_plan_mode` 的 `plan_path` 被通用"读路径存在性预检"
  误判为读目标，导致指向尚未创建的计划文件时被硬拒 `TOOL_PATH_NOT_FOUND`（与 schema "default plan.md" 语义矛盾）。
  修复：`types.ToolMetadataPathPreflightKey = "path_preflight"` 常量 + `enter_plan_mode` 定义声明
  `path_preflight: false`（`toolbroker/broker.go`）；回归测试 `TestPathPreflightMetadataOptOut`（toolexec）、
  broker 定义元数据断言（`broker_plan_mode_test.go`）。全量 `toolexec/toolbroker/types/agent/chat/skill/toolkit` 绿。
- 2026-09-18 参数扩展（A+B 均实施）：`enter_plan_mode` 计划写白名单支持多路径——
  A：`plan_path` 兼容 `string | string[]`（首项为主计划产物，其余并入白名单）；B：新增 `plan_write_paths: []string`
  （与 `plan_path` 并集，主产物语义不变，缺省仍为 `plan.md`）。实现：`toolbroker/plan_mode_args.go` 归一化
  （trim / 去重 / 剔除主路径重复项）、schema `anyOf` 声明、`brokerToolArgKinds` 与 `brokerToolArgKeys` 同步；
  `chat.EnterPlanMode` 与 `api/skills` REST（`plan_write_paths` 字段）贯通 `planmode.Enter` 可变白名单。
  测试：broker A/B/联合去重/非字符串数组拒绝、chat 装配（engine+state+result 三处白名单）、
  policy 多路径"白名单内允许 / 白名单外拒绝"、REST 直通。
- 2026-09-18 远程实测发现并修复（plan 写门禁跨轮失效，P0）：在 49949 实例用 `session_20260918144407_HY0r81J8`
  实测 A+B——`enter_plan_mode` 接受不存在的多路径（预检修复生效、artifact 原始结果白名单三项正确），但下一轮
  `write` 到**白名单外**路径仍被放行。根因：`policy.Engine.Evaluate` 中 `req.Mode`（来自 run meta，
  `permissionModeFromContext`）优先于 `engine.Mode`（`policy/engine.go:159-164`），而宿主按会话快照构造的
  run meta 仍带着进入 plan 前的 `bypass_permissions`（actor 侧 plan 持久化未回写宿主快照），导致整轮 plan 分支
  被跳过。修复：`chat.SessionActor.applyDurablePlanModeToRun`（actor.go:2642 调用点 + actor.go:5269 新增）在
  prepareRun 后重放 engine 状态之外，把**活跃 plan 态钉进本轮 run meta**（`syncLivePermissionMode(runCtx, plan)`）；
  `WithRunMeta` 会 clone，钉的是循环实际读取的 ctx 副本。回归测试
  `TestSessionActorPinsRunMetaToPlanWhilePlanStateActive`（含退出 plan 后不再钉的负例）。
- 2026-09-18 第二轮远程实测（修复版实例 50965）：A+B 与预检修复确认后，第三笔白名单外 `write` 仍被放行；
  用 broker 工具 `background_task`、本地 `shell` 变更命令探测同样放行，且跨轮 `exit_plan_mode` 返回
  "not in plan mode"。逐秒轮询共享库证实**根因是宿主回写覆盖**：15:16:31 行内为
  `plan_mode=active / permission_mode=plan`（actor 正确持久化），15:16:32.8 被宿主
  `syncRuntimeSessionFromChatMode` 用进入 plan 前的 CLI 快照整体覆盖为 `None / bypass_permissions`；
  下一轮 `planmode.Load(session)` 失活 → 既有的 run-meta 钉子从未触发 → 所有工具按 bypass 放行。
  修复：`cmd/aicli/commands/chat_session.go` 在宿主行更新前从存储合并 actor 独占的 `plan_mode` 键
  （`planmode.ContextKey`）；回归测试 `TestSyncRuntimeSessionFromChatPreservesDurablePlanMode`。
  至此完整修复链 = 预检豁免（工具层） + A/B 多路径（参数/白名单层） + run-meta 钉子（actor 层） +
  宿主回写合并（持久化层）。
- 2026-09-18 第三轮远程实测（实例 53226 / `aicli-4x`）：enter 与跨轮 enforcement 首次全通过——
  白名单内 `write` 成功落盘、白名单外 `write` 被 `plan_mode_write_path_not_allowed` 拒绝且无文件，
  `plan_mode=active` 跨轮保持（存储层兜底生效）。随后发现**第 6 层缺陷**：`exit_plan_mode` 返回
  `status=exited` 并已落库，却被 actor 自己的**运行期陈旧快照**（自带 active）在 turn 末
  `persistSession` 覆盖回 active——存储兜底只对"未携带该键"的写生效，对"携带陈旧值"的写无能为力。
  修复：`internal/chat/actor.go` 的 `persistSession` 在 Update 前按生命周期时间戳
  （`exited_at`/`entered_at`）对存储副本与出站快照**择新合并** `plan_mode`；新增回归测试
  `TestPersistSessionKeepsNewerStoredExitedPlanState`（退出不被复活）与
  `TestPersistSessionLetsNewerOutgoingPlanEntryWin`（新 enter 不被旧 exit 覆盖），并新增存储层
  回归 `TestSQLiteSessionStorageKeepsActorOwnedPlanModeContext`（陈旧快照不抹键 + 显式退出不复活）。
  完整修复链（六层）= 预检豁免 + A/B 多路径 + run-meta 钉子 + 宿主回写合并 + 存储兜底 +
  运行期择新合并。
- 2026-09-18 第四轮远程实测（实例 55407 / `aicli-4x` 15:39:25 重建，session
  `session_20260918154012_dmOiKrLL`）：**完整生命周期四轮全绿**——
  ① `enter_plan_mode`（多路径，文件不存在）→ `active` 落库并跨轮保持，白名单 3 项并集正确；
  ② 白名单内 `write` 成功落盘、白名单外 `write` 被 `mode:plan_mode_write_path_not_allowed` 拒绝；
  ③ `exit_plan_mode(quit)` → `status=exited` 落库且在 turn 末写回后仍保持 exited（第 6 层修复验证）；
  ④ 退出后原被拒路径与白名单路径均恢复可写。测试文件已清理（`planfix5-*`）。
- 2026-09-18 第五轮：**展示口径统一**（原"permission_mode 显示 bypass"外观问题）。宿主
  `syncRuntimeSessionFromChatMode` 在过去只合并 `plan_mode`，不反映到权限键；现在 plan active 时
  额外写 `permission_mode`（canonical+legacy）与 `effective_permission_mode` = `plan`，exit 后由同一
  同步路径恢复 CLI 模式。配套加固：`restoreChatRuntimeContext` 拒绝把 `plan` 灌入
  `session.PermissionMode`（plan 是生命周期而非 CLI 模式，避免退出后引擎仍按 plan 评估）；TUI
  `/permission-mode` 展示在 plan active 时统一显示 `plan`。回归测试
  `TestSyncRuntimeSessionFromChatReportsPlanAsEffectivePermissionMode`、
  `TestRestoreChatRuntimeContext_StoredPlanDoesNotPoisonCLIMode`。远程实测（实例 54061 /
  `session_20260918155107_x7VIX51y`）：enter 后行内 `permission_mode: plan | effective: plan` +
  `plan_mode: active`；exit 后 `permission_mode: bypass_permissions | effective: bypass_permissions` +
  `plan_mode: exited`——双向一致。
## 12. 开放问题（实施前确认）

1. **本次实际故障属于 M1-M6 中的哪一条**：需要故障时段的存储后端（deferred SQLite / 内存回退 / 远程 runtime-server）、
   `session_start` 与落库事件序列、是否跨进程。建议按 §7.3 Runbook 复现一次以固化证据。
2. **触发入口缺口清单**（1.6）：`TriggerTurnDrain`、followup、team 派发、远程派发是否存在"首次触达未落库会话"的路径。
3. **自愈护栏判定标准**：如何区分"外部清理"与"用户主动删除"（事件标志/时间窗/显式标记），需产品确认。
4. **Phase 4 具体位置**：observability 分桶函数、toolexec 熔断名单函数名与现有语义（实施时先核查）。
5. **远程模式下 Save 幂等**：服务端是否需要 `client_session_id` 去重键支持自愈重建。
6. **是否引入"事件追加式耐久"**（§13.5）：为模型自主模式切换提供仅次于会话行事务的耐久路径
   （模式变更事件 + 回合快照，恢复优先重放）；涉及恢复链改造与多写者幂等，需与远程存储方案一并确认。

## 13. 参考实现对照：Codex（`E:\projects\ai\codex`）

> 目的：用 Codex 的成熟设计校验本方案方向，并提炼可直接借鉴的机制。代码引用为 `codex-rs/` 工作区实测（2026-09-18）。

### 13.1 Codex 的模式体系速览

**数据模型**：`CollaborationMode { mode: ModeKind, settings{model, reasoning_effort, developer_instructions} }`；
Plan 只是 `ModeKind` 的一个取值（与 Default 并列），不是独立子系统。

**谁切换（关键）**：**客户端/宿主**，不是模型工具。两条路径：

1. **回合级**：`turn/start.collaborationMode`（EXPERIMENTAL，"Takes precedence over model, reasoning_effort, and developer instructions"）
   —— `app-server-protocol/src/protocol/v2/turn.rs:148-155`；
2. **线程级**：`thread/settings/update` → `TurnRequestProcessor` → `Session::update_settings(SessionSettingsUpdate{collaboration_mode})`
   —— `app-server/src/request_processors/turn_processor.rs:690-730`、`core/src/session/mod.rs:1440-1478`。
3. TUI 侧由用户操作（mask 切换/快捷键），并在会话恢复时从快照回填
   —— `tui/src/chatwidget/session_flow.rs:89-90`、`tui/src/chatwidget/settings.rs:139-165`。

**全仓不存在 `EnterPlanMode`/`ExitPlanMode` 之类工具**：模式切换完全在宿主控制面，模型不参与；
模型侧只有 `update_plan`（TODO/checklist 工具，且在 Plan 模式下被显式拒绝：`core/src/tools/handlers/plan.rs:84-87`）。

**状态位置**：运行期权威是**会话内存配置** `session_configuration.collaboration_mode`
（`core/src/session/mod.rs:3084-3087`、`core/src/session/session.rs:245-247`）；每个回合生成
`TurnContext { mode, collaboration_mode_developer_instructions, ... }`
（`core/src/session/turn_context.rs:138,170-179`），模型可见的世界状态由
`CollaborationModeState` 作为 `WorldStateSection` 以增量 diff 渲染（`core/src/context/world_state/collaboration_mode.rs:16-63`）。

**持久化形态**：**append-only rollout 快照**，随回合/事件写入，不做"读改写会话行"：

- `TurnContextItem`（"Persist once per real user turn … so resume/fork replay can recover the latest durable baseline"）
  内含 `collaboration_mode` —— `protocol/src/protocol.rs:3269-3274`、`core/src/session/turn_context.rs:391-401`；
- `TurnStartedEvent.collaboration_mode_kind`、`ThreadSettingsApplied.collaboration_mode`
  —— `thread-store/src/thread_metadata_sync.rs:559-568,597-634`。

**惰性物化**：rollout 也是延迟创建的；且设置类事件走
`send_event_raw_without_materializing_rollout`——**设置变更不强制物化**，只在已物化时持久化
（`core/src/session/mod.rs:1945-1951`、`core/src/session/handlers.rs:93-105`）。

**缺失语义**：恢复不存在的线程在**入口边界**返回 `invalid_request("no rollout found for thread id …")`
（`app-server/src/request_processors/thread_processor.rs:2394-2402,4569-4570`；
测试 `app-server/tests/suite/v2/thread_resume.rs:286-293`），运行期工具层不产生"记录不存在"错误。

**模式的能力面**：Plan 模式改变工具可用性与回合注入策略（`request_user_input` 差异、
`try_start_turn_if_idle` 以 `PlanMode` 理由拒绝注入：`core/src/session/inject.rs:58-61,89-92`），
由回合上下文/工具路由处理，而非工具自身读模式后改状态。

### 13.2 与我们的机制逐项对照

| 维度 | Codex | 我们（现状） | 差距含义 |
| --- | --- | --- | --- |
| 切换发起者 | 宿主/客户端（per-turn 参数、线程设置） | 模型工具 `enter/exit_plan_mode` | 我们的切换天然是运行期动作，必然触碰权威副本 |
| 运行期权威 | 会话内存配置（`SessionState`） | permission engine（投影副本） | 我们存在"耐久权威 + 执行副本"双份 |
| 耐久形态 | append-only 快照（TurnContextItem/事件） | 会话记录 context 的读-改-写 | 我们是"必须先读到旧行才能写"的事务 |
| 惰性持久化 | 有，且**设置变更不强制物化** | 有（deferred SQLite 壳），但工具路径**要求先物化** | M1/M2 窗口的来源 |
| 缺失处理 | 入口边界 `no rollout found`（invalid_request） | 工具中途 `SESSION_NOT_FOUND`（Phase 0 前误标为路径错误） | 记录依赖泄漏进工具运行期 |
| 恢复方式 | 宿主重放 rollout 快照 + 客户端重新下发模式 | actor 每 turn 从会话记录投影引擎 | 我们的投影依赖记录可读 |
| 模式的能力面 | 工具路由/回合注入门显式区分 | 主要靠 policy engine 拦截 | 可选优化方向 |

### 13.3 可借鉴的结论

1. **把"记录存在性"限定在装载期/入口边界**：Codex 的 `no rollout found` 是入口错误，不是工具错误。
   对应本方案 A2（actor 创建/回合入口校验），并把错误形态做成宿主级、可操作，而不是工具中途爆发。
2. **模式耐久采用"追加快照"而非"读改写事务"**：模式是回合上下文的一部分，随回合/设置事件落盘；
   恢复靠重放，不要求"切换时先读到旧值"。对应本方案 Phase 2 的增强形态（模式变更事件 + 回合快照）。
3. **惰性物化不应被设置变更强制**：Codex 明确用 `send_event_raw_without_materializing_rollout`
   让设置类变更不创建 rollout。说明"模式切换不依赖物化"是成熟设计取向，支撑 Phase 3（exit 方向）取舍。
4. **单一权威 + 投影**：Codex 的权威只有内存配置（运行期）和快照（恢复期），没有第三个副本；
   我们的 P4（context/RunMeta/engine 三处）可收敛为"快照为源、内存投影"，减少同步面。
5. **能力面随模式显式变化**：plan 模式在工具路由/回合注入层显式变化，而不是靠工具内部检查后再改状态——
   可减少"工具副作用式模式切换"的复杂度。
6. **模式与授权分离**：Codex 的 `CollaborationModeState` 只是模型可见指令（world state），
   不承担授权职责；授权由 approval/sandbox/permission profile 体系承担。我们的 `plan_mode` context
   同时承担"姿态记录 + 授权开关"，职责偏重，是 P1/P3 的深层原因。

### 13.4 不可照搬的差异

- **产品形态**：Codex 的模式选择完全属于用户/客户端；我们的产品允许**模型自主**进入计划模式（"先规划后执行"），
  因此不能删除 enter/exit 工具。可行方向是：工具只发起"运行期变更 + 追加事件"，耐久由回合边界机制保证。
- **写者模型**：Codex 是单进程 + app-server 单写者、rollout 单文件追加；我们存在本地/远程双写者、
  多后备存储（file/sqlite/内存/HTTP），自愈护栏与幂等（§5.2 的防腐边界）仍必要。
- **恢复责任**：Codex 恢复时由客户端重新下发模式；我们的 resume 路径（`restoreChatStateFromRuntimeSession`）
  需要自行从记录恢复，因此"记录可读"的依赖比 Codex 更深，改造需覆盖恢复链。

### 13.5 对本方案的修正与增补

| 影响 | 修正 |
| --- | --- |
| Phase 1（A2） | 明确"装载期校验 + 宿主级入口错误"的语义，对齐 Codex `no rollout found` 的边界定位；错误文案给出可操作恢复（新建/切换会话或修复存储），而非路径指引 |
| Phase 2（C 增强） | 在自愈之外增加**模式变更事件 + 回合快照**耐久路径：`plan_mode` 状态变化写入事件/回合元数据；恢复优先重放快照，会话记录成为回退源而非唯一源（进一步降低 P3 耦合，与 P4 收敛方向一致） |
| Phase 3（B） | 获得先例支持：设置类变更不强制物化（Codex）；`exit` 方向 best-effort 持久化可按此定调，`enter` 方向仍 durability-first |
| Phase 4（D） | 增加观察项：plan 模式的能力面过滤（工具路由层）与回合注入门语义，评估是否引入 |
| 开放问题 | 新增：是否需要为"模型自主模式切换"引入**事件追加式耐久**（替代/补充会话行事务）；涉及恢复链与多写者幂等，需与远程存储方案一并确认 |
