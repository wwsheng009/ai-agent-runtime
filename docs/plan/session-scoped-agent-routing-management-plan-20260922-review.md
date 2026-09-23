# 方案审查报告：《会话级 Agent 路由管理与切换方案》（2026-09-22）

> 审查对象：`docs/plan/session-scoped-agent-routing-management-plan-20260922.md`（Draft v1）
> 审查日期：2026-09-22
> 审查方式：作者自验（第一手代码证据）+ 三路只读代码核查子代理（TUI/agentconfig、runtime/events、frontend）
> 证据口径：全部结论附 `file:line`；无法定位的项标注"未获证实"，不作推断
> 本文档只记录审查结论与修订要求，**不含实现**。

---

## §0 总体结论

**结论：不建议按现稿直接进入实现。** 治理方向（三层解析 + 会话级 override + 工作区偏好 + 状态栏可见性）成立且与既有偏好机制同构，但存在 **6 项阻断级事实错误/设计缺口**、**13 项重要问题**，其中 3 项（B1/B2/B6）会导致"照着方案写出来的东西与真实系统语义不符"。

| 分级 | 数量 | 性质 |
| --- | --- | --- |
| B（阻断） | 6 | 事实错误或设计缺口，必须修订后才能实现 |
| M（重要） | 13 | 需在方案中明确/改写，否则实现期必然返工 |
| N（次要/编辑） | 10 | 锚点行号、命名、冗余字段等 |

**可保留的核心资产**：三层逐字段解析模型（§3.1）、指针表达"未设置=继承"（§3.2，但需按 M3 修正适用字段）、turn 边界生效（§4.4）、INV-A2/A3/A4、分期策略（§9，但 S1 需按 B3 补充"开启推导"）、测试编号体系（§10）。

---

## §1 阻断级问题（B）

### B1 `enable_routing` 语义错配——它不是主 Agent 路由开关

- **方案位置**：§0 问题 3、§7.1、§7.3(b)、§7.4、§8.4、§12-6。
- **事实**：Go 侧字段是 `EnableRoute bool \`json:"enable_routing,omitempty"\``（`backend/internal/api/skills/handler.go:1610`），消费点为**技能候选路由与编排模式**：
  - `handler.go:1934` `routeAttempted := req.EnableRoute || plannerPreferred || ...`
  - `handler.go:1937` `routeCandidates = routeCandidatesWithRuntime(...)`
  - `handler.go:2198`、`:2342`、`:2492` 用同一字段决定 `OrchestrationRoutePreferred`。
  - 与 `aicli.main_agent.routing`（`LoopReActConfig.MainAgentRouting`）**没有任何连线**。
- **影响**：若按方案把前端 `enable_routing` 改造成"主 Agent 路由开关"：
  1. 用户"关闭路由"实际关掉的是**技能候选路由/编排**（既有功能静默降级）；
  2. 用户"开启路由"不会开启主 Agent 难度路由（该字段不接 `MainAgentRouting`）。
- **修订要求**：
  - `enable_routing` 保持既有语义不动，方案中所有相关表述删除；
  - 主 Agent 路由使用独立字段（请求级 `routing.main_agent` 或新字段名），并在 §7.4 契约表新增一行「技能候选路由（既有 `enable_routing`，本方案不改）」以隔离两套概念；
  - §0 问题 3 需重写：前端当前的缺口不是"硬编码了主 Agent 路由开关"，而是"**完全没有**主 Agent 路由的请求/会话入口，且既有 `enable_routing` 是另一套功能"。

### B2 子 Agent 覆盖结构是虚构的

- **方案位置**：§3.2 `AICLISubagentRouteProfileOverride{Levels, DefaultDifficulty}`、§5.2 `/routing sub profile <name> levels|default_difficulty`、§9 S5、§13 O4。
- **事实**（`backend/internal/agentconfig/config.go`）：
  - `AICLISubagentRoutingConfig`（`:672-708`）= `Enabled *bool` / `CompatibilityMode` / `DefaultDifficulty` / `AllowExplicit{Provider,Model,Reasoning}Override` / `Allowed*Overrides` / `InheritParentWhenMissing` / `ValidateModelCapabilities` / `UnsupportedReasoningPolicy` / `OnReasoningUnsupported` / `MaxExpertConcurrency` / `PromoteExplicitDifficulty` / `Heuristics` / `Failover` / `AvailabilityPolicy` / `RequirePromptCache` / **`Levels map[string]AICLISubagentRouteProfile`** / `TaskTypes map[string]map[string]AICLISubagentRouteProfile` / `Roles`（兼容别名）。
  - `AICLISubagentRouteProfile`（`:720-738`）= `provider` / `model` / `reasoning_effort` / `thinking_effort` / `max_tokens` / `timeout` / `temperature` / `availability` / `availability_reason` / `prompt_cache` / `candidates[]`。**没有 `levels` 或 `default_difficulty` 字段**。
  - team 路由会回落 subagent 路由（`EffectiveTeamRoutingConfig`，`config.go:657-670`），覆盖设计必须覆盖这条链。
- **影响**：`/routing sub` 按现稿无法实现；若强行实现会造出与真实配置语义不同的"第二套 sub 路由"。
- **修订要求**：
  - S5 首发仅覆盖真实存在的标量：`sub_agent.enabled`（`*bool`）、`sub_agent.default_difficulty`；
  - `Levels` / `TaskTypes` / `Roles` 的覆盖单列为"待评估"，并明确 map 合并语义（replace 还是 merge-by-key；`candidates` 链如何合并）；
  - §5.2 的 `profile <name>` 候选表整体删除或改写为真实键；
  - 前置条件：先证实 `aicli.subagents.routing` 真的接到 scheduler（见 §5 待确认项 U-2）。

### B3 会话级"开启"在无全局配置时不可行

- **方案位置**：§4.1 步骤 4→5、§5.1 `/routing on`、§9 S1（主用例）。
- **事实**（`backend/internal/agentconfig/main_agent_routing.go`）：
  - `:147-149` `enabled=true` 时 `levels` 不允许为空；
  - `:150-152` `levels` 含 expert ⇒ `allow_expert=true`；
  - `:153-155` `allow_expert=true` ⇒ `max_consecutive_expensive_steps` 必须有限（不得为 0）；
  - `:160-162` `default_difficulty` 必须 ∈ `levels`；
  - `:207-209` `profiles` 键必须 ∈ `levels`。
- **影响**：用户配置里没有 `aicli.main_agent.routing` 时（最常见的"我想试一次"场景），`/routing on` 合并出的 `{enabled:true, levels:[]}` 会被校验拒绝，S1 主用例直接失败。
- **修订要求**：
  - 定义**开启推导**：`levels` 未设置时由当前 provider/model 能力推导（建议 `easy,normal,hard`；`expert` 仅在 `allow_expert=true` 且 `max_consecutive_expensive_steps` 取有限值（建议 6）时纳入）；
  - §5.4 输出契约需展示"推导值 + 推导来源"；
  - §10 增加用例：无配置 + `/routing on` → 可开启、校验通过、`loopConfig.MainAgentRouting != nil`。

### B4 逐字段覆盖与跨字段约束冲突（消解策略缺失）

- **方案位置**：§4.1 步骤 5、§10 U4/U5。
- **事实**：主 Agent 校验含多条**跨字段**规则（见 B3 证据），而方案只定义了"单字段非法 → 丢弃该字段 + warning"。
- **影响**：以下场景现稿无定义，实现期必然各自发明语义：
  - 继承 `levels=[normal]`，会话设 `default_difficulty=hard` → 组合非法，丢谁？
  - 会话设 `allow_expert=true`，继承 `max_consecutive_expensive_steps=0` → 必须丢会话字段（丢继承值会篡改配置语义）。
  - 丢弃后应回落到**下一层**（workspace/config）还是**内置默认**？现稿 §4.1 步骤 4/5 的顺序（先默认值、后校验）会导致"丢弃后落到内置默认"，与"继承配置"的承诺矛盾。
- **修订要求**：
  - 解析器定义确定性消解顺序：合并 → 校验 → 失败则**按覆盖层由近及远回退**（session 字段 → workspace 字段 → 保持继承值），逐轮重校验（限定轮数）；
  - warning 必须记录：被丢弃字段、丢弃原因、最终来源；
  - 默认值应用顺序改为"对基线应用默认值 → 再叠加覆盖"，或显式声明"显式 0 语义"（见 M3）；
  - §10 增加两条组合非法用例（U4 扩展）。

### B5 关闭态丢失来源信息（解析器返回 nil 与 UI 需求冲突）

- **方案位置**：§4.1 步骤 6（`Enabled=false` 时返回 `nil`）vs §5.4（关闭态显示 `off (inherited from config)`）、§6.2（关闭不渲染）、§6.4（`/status` 显示 `off (inherited from config)`）。
- **影响**：返回 nil 后，UI 无法区分"config 里显式关闭"与"从未配置（default）"，也无法渲染 `session off`（会话显式关闭）。而这三者是本方案要解决的核心可观测性诉求。
- **修订要求**：`RoutingResolution` 恒非 nil（`Effective` 可为 nil），并在宿主接线处映射：`Effective==nil || !Effective.Enabled → loopConfig.MainAgentRouting = nil`（与既有 `main_agent_route.go:237-246` 的 `cfg == nil || !cfg.Enabled` 语义一致）。

### B6 状态栏插入位置会破坏既有索引算法（方案声称"不用改"是错的）

- **方案位置**：§6.1「不得改动既有段索引算法」、§6.3、§10 REG3。
- **事实**（`backend/cmd/aicli/commands/chat_interaction.go:1081-1091`）：
  ```go
  func chatAccountBalanceStatusInsertIndex(segments []style.StatusSegment) int {
      for index, segment := range segments {
          if segment.Kind == style.StatusSegProvider { return index + 1 }
      }
      for index, segment := range segments {
          if segment.Kind == style.StatusSegModel { return index + 1 }
      }
      ...
  ```
  该函数**动态**返回 provider（或 model）之后的位置，正是方案要放 routing 段的位置。
- **影响**：routing 段插在 provider 之后后，balance 的插入点与 routing 段位置重叠 → 段顺序错乱或 routing 被挤走；`chat_balance_refresh_test.go:397` 锚定的行为会失败。REG3 清单遗漏该测试文件。
- **修订要求**：
  - 改为显式 canonical 段顺序表（model → provider → routing → balance → context → …），插入算法按 canonical 顺序定位前驱段（跳过未渲染段）；
  - REG3 增补 `chat_balance_refresh_test.go:397` 的修订说明，并明确 golden 测试的更新是**预期变更**而非回归；
  - §6.1 删除"不得改动既有段索引算法"，改为"必须重构插入算法并同步更新锚定测试"。

---

## §2 重要问题（M）

### M1 `guard off` 是非法取值

- **方案位置**：§3.2（`cost_guard_mode` 注释 `soft | hard | off`）、§5.1 `/routing guard soft|hard|off`、§5.2。
- **事实**：`main_agent_routing.go:188-192` 只接受 `soft` / `hard`，其余（含 `off`）一律报错。
- **修订要求**：删除所有 `off` 取值；"关闭护栏"的正确表达是"关闭整个路由"（`enabled=false`），或在方案中显式说明该诉求不支持。

### M2 health 键路径错误

- **方案位置**：§5.2「`health.respect_provider_health`」。
- **事实**：真实 YAML 路径为 **`health_gate.respect_provider_health`**（`config.go:78` `HealthGate ... yaml:"health_gate"`；`main_agent_routing.go:51`）。
- **修订要求**：更正为 `health_gate.respect_provider_health`；`latch_scope` / `on_chain_exhausted` / `honor_min_dwell` 的"不开放覆盖"结论不变（`:193-200`、`:123` 为硬规则）。

### M3 默认值应用顺序会吞掉"显式 0"

- **方案位置**：§3.2 指针语义、§10 U2「显式 `false`/`0` 覆盖上层」、§4.1 步骤 4。
- **事实**：`ApplyMainAgentRoutingDefaults`（`main_agent_routing.go:95-124`）把数值 0 视为"未配置"并回落默认值（`:99-107`：`downgrade_confirm_steps`→3、`min_dwell_steps`→2、`max_invalid_reports_per_turn`→3）；**唯一例外**是 `MaxConsecutiveExpensiveSteps`（`:92-93` 注释明确 0=不限）。
- **影响**：会话显式设置 `min_dwell_steps=0`（校验层合法，`:182-184`）会在步骤 4 被改写成 2——与 U2 的"显式 0 覆盖"矛盾；反之若改成"先覆盖后默认"，则 `max_invalid_reports_per_turn=0` 会变成"不限"（潜在安全语义）。
- **修订要求**：在 §3.2/§4.1 明确：`min_dwell_steps` / `max_invalid_reports_per_turn` / `downgrade_confirm_steps` 的 0 **等同未设置**（与配置层现状一致），§5.2 候选值不提供 0；仅 `max_consecutive_expensive_steps` 保留 0=不限（并在 §5.2 标注其与 `allow_expert` 的互斥约束）。

### M4 runtime 侧没有路由级鉴权中间件

- **方案位置**：§7.3(c)「复用 runtime 既有 session 路由与鉴权中间件」。
- **事实**（子代理核查 + 第一手抽查）：`/api/runtime` 前缀 `handler.go:74`，子路由 `:778-779`，会话族端点注册在 `:891-927`（`/sessions/{id}`、`/archive`、`/activate`、`/close`、`/branch`、`/history`、`/permission-mode`(GET+POST) 等）；**没有 `Use(middleware...)` 形态的路由级鉴权**，鉴权是 per-handler 检查（`hasValidSearchAdminToken` / `hasTrustedAdminRole` / `isLoopbackRequest`，如 `handler.go:7723`、`:7726`；token 注入 `SetAdminToken` `:630`）。
- **修订要求**：改为"逐 handler 复刻既有鉴权检查"，并明确新端点的授权模型（本地回环？admin token？只读 vs 写入分级），把它列为 S3 的验收项——否则新增写端点会默认暴露。

### M5 `model_changed` 在前端零消费，"复用该事件"无依据

- **方案位置**：§6.3 表格最后一行、§7.3(e)。
- **事实**：子代理在 `frontend/` 全量检索 `model_changed` 为空；后端 payload 与消费方未定位。
- **修订要求**：删除"routing 变更复用 `model_changed`"；同步路径只依赖新增的 `session.routing_changed`（含 TUI 写入路径），并把"模型/provider/effort 变更如何通知前端"列为独立待确认项（现状可能根本不通知）。

### M6 "热重载全局改变行为"的前提在 runtime 侧不成立

- **方案位置**：§0 问题 1、§11「与全局热重载竞态」。
- **事实**：配置注入唯一入口 `handler.go:448-454 SetAICLIConfig`（快照克隆 `:504`，MainAgent 分支 `:529-533`）；路由接线发生在**会话 actor 构建期**（`session_runtime_support.go:3933 buildSessionLoopConfig` → `:3937` 接线 → 定义 `:4146`）；`NewReActLoop` 再深拷贝冻结（`loop.go:207`）。**未找到把新配置回写到已建 actor 的路径**。
- **影响**：runtime 侧"改配置 → 热重载 → 所有会话行为变化"并不成立（只影响新建 actor/会话）；方案据此论证的痛点与风险都需要改写。
- **修订要求**：§0 问题 1 改为"配置级开关粒度错误 + 需重启/重建 actor 才能生效"；§11 竞态条目改为"actor 重建时机与快照一致性"；这反而强化了会话级 override 的必要性（会话 override 必须独立于配置热重载而稳定生效）。

### M7 前端事件契约是生成物，不能直接编辑

- **方案位置**：§8.4「`frontend/src/types/runtime/event-contract.ts`：`session.routing_changed`」。
- **事实**：文件头 `// Code generated by backend/cmd/contractgen from internal/events/contract.go. DO NOT EDIT.`（`frontend/src/types/runtime/event-contract.ts:1`）。
- **修订要求**：改为"在 `backend/internal/events/contract.go` 注册（参考 `:105-108`，`Channels: ChannelSessionStore`）+ 重跑 `backend/cmd/contractgen` 重新生成前端投影"；同时按 M5 复核前端是否有该事件的消费方。

### M8 既有测试未纳入 REG，且解析器改造会破坏指针同一性断言

- **方案位置**：§4.2（`chat_actor_host.go:2297` 改走解析器）、§10.3 REG 清单。
- **事实**：`backend/cmd/aicli/commands/chat_main_agent_routing_test.go:30` `TestApplyLocalChatMainAgentRoutingHostWiring`，其中 `:38-40` 断言 `base.MainAgentRouting == enabled.Config.AICLI.MainAgent.Routing`（**指针同一性**，注释说明"wiring must hand the configured object to the loop"）；另有 `:68` 隔离测试。
- **影响**：解析器若总是返回深拷贝/合成对象，该测试失败；方案 REG 清单（REG1–REG6）完全没提这个文件。
- **修订要求**：解析器定义 fast path——"无会话覆盖且无工作区偏好时返回原配置指针"；或显式声明该测试将被修订并列入 REG 变更清单。两种方式都必须在 §10.3 写明。

### M9 会话级键未复用既有 session-context 约定

- **方案位置**：§3.3（裸字面量 `"aicli_routing_override"`）。
- **事实**：既有约定是 `sessionmeta.LegacyAICLI*` 常量集合（`chat_session.go:32-46`，另见 `chat_team_binding.go:18-24`、`chat_token_usage.go:11-16`），读取 helper 为 `runtimeSessionContextString`（`chat_session.go:1679`，**仅支持字符串**）。
- **修订要求**：新增 `sessionmeta.LegacyAICLIRoutingOverride` 常量；明确结构化值的编码方式（建议 JSON 字符串，复用 `runtimeSessionContextString` 家族并新增解码 helper），并在 §3.3 说明与既有 key 命名的关系。

### M10 前端"工作区偏好"层缺服务端对应物（待 frontend 核查确认后定稿）

- **方案位置**：§7.2、§7.3(a)、§13 O5。
- **问题**：后端工作区偏好是 `$HOME/.aicli/workspace/<sha256(cwd)[:8]hex>/chat-prefs.yaml`（服务端/CLI 侧），而前端 `core/settings` 是浏览器本地存储；方案同时声称"字段名逐字对齐"与"后端工作区偏好为准（跨端一致）"。
- **修订要求**：二选一——(a) 新增服务端工作区偏好 API（前端读写同一 `chat-prefs.yaml`），才谈得上跨端一致；(b) 明确前端"工作区偏好"仅是浏览器本地默认值，不参与后端解析优先级，§7.4 契约表标注适用范围。**不允许保留现稿的模糊表述。**

### M11 会话 metadata 并发写入无保护

- **方案位置**：§4.4、§11。
- **事实**（子代理核查）：`Session.Metadata.Context` 是普通 `map[string]interface{}`（`chat/session.go:52,63`），随会话经 SQLite 落盘（`sqlite_storage.go:1299` marshal、`:1437-1439` unmarshal）；API 侧写入路径为 PATCH `/sessions/{id}` 的 `req.Context → session.SetContext`（`handler.go:3099-3101`）；handler 的 Get→改→Save 未见会话级锁（存储层只有 `beginWriteTx`/`snapshotMu`）。
- **修订要求**：§4.4 明确"同一会话的 override 写入必须串行化"（会话级 mutex 或版本号 CAS），并说明 TUI 与 API 同时写入时的胜出规则；§10 I 增加并发写用例。

### M12 `scope` 与 `save/reset` 语义重叠

- **方案位置**：§5.1 语义表（`on/off/toggle` 持久化列恒为"会话 metadata"，而 `scope` 声称"选择写入目标"）。
- **问题**：`scope` 到底改变什么？若只影响 `save/reset` 的默认目标，则与显式的 `save workspace` / `reset workspace` 重复；若影响 `on/off` 的写入目标，则与语义表自相矛盾。
- **修订要求**：删除 `scope`，保留显式 `save workspace|reset workspace`（更符合"默认不动工作区偏好"的安全取向）；或给出 `scope` 影响矩阵（哪些子命令受其影响）。

### M13 顶层 `Enabled` 与 `MainAgent.Enabled` 双重表达

- **方案位置**：§3.2（`Enabled *bool` + `MainAgent.Enabled *bool`）、§5.1（`/routing on` = main_agent.enabled）。
- **问题**：同一语义两个字段，未定义优先级与交互（尤其 `/routing sub on` 时顶层开关是否生效）。
- **修订要求**：二选一——(a) 删除顶层 `Enabled`，统一为 `MainAgent.Enabled` + `SubAgent.Enabled`；(b) 保留顶层为"main+sub 总开关"，并写明与两个分节开关的组合真值表。

### M14 `/debug routing` 现有语义是"子 Agent / Team routing"，不是主 Agent 路由

- **方案位置**：§6.4「`/debug routing`（既有，见 `chat_slash_command_catalog.go:115`）扩展为三层来源视图」。
- **事实**：`chat_slash_command_catalog.go:115` 的 spec 为 `{Token: "routing", Summary: "显示子 Agent / Team routing 配置摘要"}`（`/debug` 的 Args 表，`:106-119`）。
- **影响**：直接"扩展"会把一个既有的子/团队路由调试入口改成主 Agent 路由视图，破坏现有语义与用户预期。
- **修订要求**：保留 `/debug routing` 原语义；`/routing doctor` 自建渲染（可复用同一份解析结果），或在 `/debug` 下新增独立 token（如 `main-routing`），并在方案中显式说明二者边界。

### M15 `web_statusbar` / cfg-bar 属于 aicli micro web client，与 `frontend/` 是两套界面

- **方案位置**：§6.5（"前端据此渲染 chip"）、§7.3(f)（"前端底部条（与 cfg-bar 同区）"）、§8.4（"底部状态栏组件（cfg-bar 同区）"）。
- **事实**：
  - `web_statusbar.go:3-10` 注释明确："GET `/web/api/statusbar` —— 返回当前会话底部状态栏的紧凑投影……保证 **micro web client** 的底部状态栏与 aicli chat TUI 同一份数据"；"provider / model / reasoning_effort 在 **Web 客户端由底部 cfg-bar** 实时展示，此处不重复"。
  - `frontend/src` 全量检索 `statusbar` / `status-bar` / `cfg-bar` / `StatusBar` 均为空 → `frontend/` workspace app 既没有 cfg-bar，也没有底部状态栏组件。
- **修订要求**：
  - §6.5 显式限定该端点的消费方是 **aicli micro web client**（不是 `frontend/`）；
  - §7.3(f) 改为"在 `frontend/` 中新增底部状态条（当前不存在，需要新建组件并确定挂载位置：composer 下方或页面底部）"，并给出组件落点；
  - §8.4 的"底部状态栏组件（cfg-bar 同区）"相应改写。

### M16 非主会话（子会话/team 会话）中执行 `/routing` 的行为未定义

- **方案位置**：§4.2（"`applyLocalChatMainAgentRouting` 只接主会话 → 不变"）、§5.1。
- **问题**：`applyLocalChatMainAgentRouting` 对非 base session 直接 return（`chat_actor_host.go:2311-2316`）。在子会话里执行 `/routing on` 会写入会话 metadata，但**下一 turn 不会生效**，用户看到"已开启"却没有效果。
- **修订要求**：定义行为——建议在非主会话中把变更类子命令改为"提示 + 拒绝"（或明确写入的是"该会话若成为主会话时生效"），并在 §5.4 输出与 §10 用例中体现。

- **落地状态（2026-09-22 增补，TUI 部分已闭环）**：TUI 侧在 `backend/cmd/aicli/commands/chat_routing_command.go` 落地 `chatRoutingSessionIsChildAgent`（**同源判据**：`agent_type` / `depth>0` / `read_only` 任一命中即子会话，对齐 `chat_actor_host.go:1379-1403` 与 `session_runtime_support.go:3938-3942`）与唯一文案 `chatRoutingChildSessionReadOnlyNote`；`chatRoutingWriteKey`（面板最终写入亦经此）/ `chatRoutingResetFromArgs` / `chatRoutingSaveFromArgs` 在任何落盘之前拒绝，`chatRoutingPanelEntry` 在子会话退化为只读摘要 + 提示（入口退化形态，理由见主方案 §9.1.1「面板『只读打开』的落地形态」）。回归：`chat_routing_command_test.go:TestChatRoutingCommandChildSessionReadOnly`（三种子会话标记 × 全部写入类子命令被拒 + `show`/`doctor` 仍可用）、`TestChatRoutingCommandChildSessionBlocksWorkspaceSideEffects`（`--to workspace` 被拒且工作区零新增文件、主会话不被误伤）。API 侧对应 U-5（409）此前已闭环。

---

## §3 次要问题（N）

| 编号 | 问题 | 证据/要求 |
| --- | --- | --- |
| N1 | §1 表格若干行号需更正 | 见 §4 锚点更正表 |
| N2 | `chatPreferenceSource` 已含 `session`，方案 §3.1"新增来源值 session"与 §8.2 该行不成立 | `chat_preferences.go:15-22`（`chatPreferenceSourceSession` 在 `:17`）；改为"复用既有来源值，仅新增展示文案" |
| N3 | §4.3 表格把第三参写成 `isMain` 变量 | 实为内联表达式 `strings.TrimSpace(childAgentType) == "" && childDepth == 0 && !childReadOnly`（`session_runtime_support.go:3937-3938`） |
| N4 | §3.2 `Source` 字段语义冗余 | 会话覆盖的存储层恒为 session；建议删除或改为"最近写入端（aicli-tui/web/api）"（与 `UpdatedBy` 合并） |
| N5 | `<key>` 键空间未定义 | §5.1 `inherit [all|main|sub|<key>]` 与 `main <key> <value>` 需要唯一键名表（建议 `main.levels` / `sub.enabled` 形式），否则"未识别键"分支无法实现 |
| N6 | 请求级 `routing` 与 `enable_routing` 冲突优先级未定义 | 修 B1 后此条自动消解；若仍保留请求级 `routing`，需写明它与会话 override 的覆盖关系 |
| N7 | "三层解析"与请求级第 4 层命名冲突 | §3.1 vs §7.3(b)；建议统一表述为"请求级（仅本 turn） > 会话 > 工作区 > 配置 > 默认" |
| N8 | 关闭态来源判定依赖 B5 修复 | §5.4 的 `off (inherited from config)` 需要 `sources` 才能区分 config/default |
| N9 | 工作区偏好 key 与 cwd 的关系未定义 | `chat-prefs.yaml` 以 `sha256(cwd)` 定位；`/resume` 到其他目录的会话应以哪个 cwd 为准需写明（建议以会话创建时记录的 workspace 为准，写入时回显目标路径） |
| N10 | §6.2 的 Priority 数值与 `soft!`（已触发）状态缺少依据 | Priority 需与真实段优先级表对齐（见 §6 待确认 U-4）；`soft!` 需要运行期状态来源，方案未定义——建议首版不做 `!` 后缀，或明确复用成本护栏事件的瞬态标记 |

---

## §4 锚点更正表（对方案 §1 与各章引用）

| 方案写法 | 核查结论 | 更正 |
| --- | --- | --- |
| `main_agent_routing.go:83` `EffectiveMainAgentRoutingConfig` | ✅ | — |
| `chat_actor_host.go:2297/2311/1398` | ✅ | — |
| `session_runtime_support.go:3937` | ⚠️ 调用点正确，函数定义在 `:4146`；第三参为内联表达式 | 补定义行号 |
| `handler.go:476/529/581` | ⚠️ `mainAgentRoutingConfig()` 在 `:476` ✅；快照克隆调用在 `:454`、定义在 `:504`、MainAgent 分支 `:529-533` | 改为 `:454/:504/:529` |
| `loop.go:80` `MainAgentRouting` 字段 | ⚠️ `:80` 是注释，字段在 `:83`；`NewReActLoop` 深拷贝在 `:207` | 改为 `:83`（并引用 `:207` 支持"深拷贝快照"结论） |
| `chat_command_result.go:390-403` 白名单 | ✅（否定式 `commandMatches` 链） | — |
| `command.go:326-347` legacy 分派 | ❓ 本轮未逐行确认 | 实现前复核 |
| `chat_slash_command_catalog.go:38` / `:115` | ✅（`:115` 语义见 M14） | 补语义说明 |
| `chat_slash_argument_completion.go:53-140` | ✅（`CompleteSlashArgs` 在 `:53`；`:58-60` 会把别名归一到 canonical，`/route` 别名可行） | 可补充"别名自动归一"证据 |
| `statusline.go:12-24` `StatusSegmentKind` | ✅ 恰好 8 个：State/Model/Path/Usage/Balance/Mode/Meta/Provider | — |
| `chat_interaction.go:2535-2546` 段装配顺序 | ✅ model→provider→balance→context→cwd→project→branch→window→in→out→Fast | — |
| `chat_reasoning_command.go:206/211` | ✅（`:206-207` RefreshStatus；`:211` `publishChatModelSelectionChanged`） | 注意 M5：发布端存在、前端消费未证实 |
| `chat_status.go:93-105` `/status` 行表 | ✅ | — |
| `web_statusbar.go:1-36` | ✅ 注释在 `:3-10`；但对象是 micro web client（M15） | 限定消费方 |
| `chat_preferences.go` 来源枚举 | ✅（`session` 已存在，`:17`） | 见 N2 |
| `chat_persistence_workspace.go` 路径/原子写 | ❓ 本轮未逐行确认（沿用既有设计文档 D5） | 实现前复核 |
| `chat/session.go:52,63` `Metadata.Context` | ✅（随会话经 SQLite marshal/unmarshal 持久化：`sqlite_storage.go:1299/1437-1439`） | 补持久化证据与并发约束（M11） |
| `events/main_agent_routing.go`、`contract.go:105` | ✅ 6 个事件；契约登记 `:105-108` | — |
| `runtimeobserve/known_types.go:205` | ✅ | — |
| `frontend chat.ts:16`、`turn-bootstrap.ts:114`、`event-contract.ts:66-71` | ✅（后者为生成物，M7） | — |
| `main_agent_routing_wiring_test.go:26/67`、`chat_actor_host_test.go:1514` | ✅ | — |
| `chat_balance_refresh_test.go:397` | ✅ 存在，但结论是**必须随 B6 修订** | 列入 REG 变更 |
| （方案未提及）`chat_main_agent_routing_test.go` | ❗ 存在指针同一性断言（`:38-40`）与隔离测试（`:68`） | 纳入 REG（M8） |
| （方案未提及）`chat_reasoning.go:29` `reasoningEffortCatalogForModel` | ✅ 存在，"与 /reasoning_effort 同源"成立 | 可补锚点 |

---

## §5 修订清单（按方案章节，标注必须/建议）

**结论：方案方向成立（会话级三层解析 + 复用 `/model` 管线 + 状态栏扩展），但 B1/B2/B3/B6 属结构性错误，相关章节必须重写后才能进入实现。**

### 必须修订（阻断级）

| 方案位置 | 要求 | 关联 |
| --- | --- | --- |
| §0 摘要"问题 3" | 删除/改写 `enable_routing` 表述——它是技能候选路由/编排开关，不是主 Agent 路由 | B1 |
| §1 现状证据表 | 按本报告 §4 表逐行更正行号；补 `chat_main_agent_routing_test.go`、`chat_reasoning.go:29` | N1/M8 |
| §3.1 解析层 | 来源枚举复用既有 `session` 值；统一"请求级 > 会话 > 工作区 > 配置 > 默认"表述；关闭态必须返回来源信息（不能 nil） | N2/N7/B5 |
| §3.2 数据结构 | 删除虚构的 `AICLISubagentRouteProfileOverride{Levels, DefaultDifficulty}`；子覆盖结构以 `AICLISubagentRoutingConfig`（`config.go:672-708`）为准；给出 `<key>` 键名表；`Source` 语义改写或删除 | B2/N4/N5 |
| §3.3 约束校验 | 新增"会话级开启前置校验"：levels 非空、levels⊇expert⇒allow_expert、allow_expert⇒max_consecutive_expensive_steps 有限、default_difficulty∈levels、profiles 键∈levels；无配置时给出明确拒绝路径与文案 | B3 |
| §6.1 状态栏 | 改为"必须同步修订 `chatAccountBalanceStatusInsertIndex`（动态返回 provider 之后）或为 routing 段引入独立插入点"；补充真实段序 `chat_interaction.go:2526-2555`；把 `chat_balance_refresh_test.go:397` 列入 REG | B6 |
| §7.1 / §7.3(b) / §7.4 / §8.4 | 删除 `enable_routing` 作为路由开关的全部表述；若保留请求级 `routing`，定义它与会话 override 的优先级 | B1/N6 |

### 必须修订（重要）

| 方案位置 | 要求 | 关联 |
| --- | --- | --- |
| §3.3 / §5.1 | `cost_guard_mode` 仅 `soft|hard`；`health_gate.respect_provider_health`；显式 0 覆盖语义与 `ApplyMainAgentRoutingDefaults` 冲突需给出方案（如"仅在显式 JSON 出现时视为设置"） | M1/M2/M3 |
| §4.2/§4.3 | 第三参改为内联表达式；补 `loop.go:83` 字段与 `:207` 深拷贝；说明指针同一性测试约束 | N3/M8 |
| §4.4 / §9 | runtime 侧新增写端点必须自带鉴权（`/api/runtime` 无路由级中间件）；会话 metadata 写入需串行化（actor mailbox 或读改写锁） | M4/M11 |
| §5.1 | 定义非主会话中 `/routing` 变更类子命令行为（建议提示并拒绝）；`/debug routing` 保留原语义、`/routing doctor` 自建渲染 | M16/M14 |
| §6.5 | 限定 `web_statusbar` 端点消费方为 aicli micro web client | M15 |
| §7.2 / §8.2 | 事件契约先改 `backend/internal/events/contract.go` 再 `contractgen` 生成前端类型，禁止直改生成物；REG 补 `chat_main_agent_routing_test.go`、`chat_balance_refresh_test.go` | M7/M8 |
| §7.3(f) / §8.4 | `frontend/` 需新建底部状态条组件（当前不存在），明确挂载位置与数据来源 | M15 |
| §10 用例 | U2"显式 0 覆盖"与 M3 冲突需重写；新增"无全局配置时 `/routing on` 失败路径"、"非主会话执行 `/routing`"、"关闭态来源展示"用例 | M3/B3/M16/B5 |

### 建议修订

| 方案位置 | 要求 | 关联 |
| --- | --- | --- |
| §3.2 | `Source` 字段与 `UpdatedBy` 合并，避免同义字段 | N4 |
| §5.1 | 会话键复用 `sessionmeta.LegacyAICLI*` 常量与 `runtimeSessionContextString` 读取 helper | M9 |
| §6.2 | Priority 数值与真实段优先级表对齐；`soft!`（已触发）首版可去掉或明确状态来源 | N10 |
| §6.4 | `/status` 行表（`chat_status.go:93-105`）新增 Routing 行，与状态栏保持同源 | — |
| §6.5 | 补"微 web client 与 frontend 两套界面共享端点/各自的刷新时机"说明 | M15 |
| §11/§12 | 阶段计划前置"解析器返回来源信息 + 段索引改造"两项，否则 S1/S4 无法开工 | B5/B6 |
| 全文 | `cmd/aicli/...` 路径统一改为 `backend/cmd/aicli/...` | — |

---

## §6 待确认项（实现前必须闭环）

| 编号 | 待确认内容 | 影响 | 建议动作 |
| --- | --- | --- | --- |
| U-1 | `aicli.subagents.routing` 在调度器中的实际消费点（本轮子代理未追到，主 agent 亦未定位） | §3.2 子路由"默认继承配置"的语义与 S5 可行性 | 定位消费方（scheduler / difficulty 路由）后再定稿子覆盖结构 |
| U-2 | `model_changed` 事件的消费方：发布端存在（`chat_reasoning_command.go:209-211`），`frontend/` 零消费 | §7.2"复用该事件驱动前端 chip" | 确认 micro web client 是否消费；否则改为新增专用事件并登记契约 |
| U-3 | 会话级 override 的写入通道：复用 `PATCH /sessions/{id}` 通用 context 写入，还是新增专用端点 | §4.4/§7.4 接口设计、鉴权（M4） | 二选一并写明校验与审计 |
| U-4 | 状态栏段 `Priority` 真实数值表与折叠/截断算法 | §6.2 的数值与"软上限"行为 | 逐行确认 `statusline.go` 排序逻辑后回填 |
| U-5 | `command.go:326-347` legacy 分派、`chat_persistence_workspace.go` 原子写细节 | §5.1 命令注册、D5 落盘 | 实现前复核 |

---

## 变更记录

| 日期 | 版本 | 说明 |
| --- | --- | --- |
| 2026-09-22 | v1 | 审查报告初稿：主 agent 第一手核查 + 3 个只读子代理（runtime/events 子代理成功；TUI 与 frontend 子代理因执行策略阻断失败、产出为零，其核查项已由主 agent 逐条补全，未重复派发）。结论：方向成立，B1/B2/B3/B6 结构性错误必须重写，M1-M16 与 N1-N10 需逐条落实。 |
