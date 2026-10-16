# DSH 借鉴方案审查与 v2 修订

> 创建日期：2026-10-16
> 审查对象：`docs/plan/deepseek-harness-agent-borrowing-implementation-plan-20260916.md`（20261016 版，下称"原方案"）
> 审查方式：对原方案全部关键假设做代码事实核查（`goal/continuation.go`、`sessionmeta`、`llm/adapter` ToolChoice、`agent/prompt_cache_breaker.go`、`agent/suspension_gate.go`、`checkpoint_manager.go`），并按"可做得更好"的目标标准重新设计。
> 结论：**原方案结构可用，但存在 1 处事实错误、1 处架构重叠、1 处机制缺失、1 处验收不可度量**；v2 按 G1–G8 目标重构实施项。

**实施状态更新（2026-10-16）**：

- P1-1' 能力协商已实施（`capabilities.go`：`Negotiate` + 三层一致 + `SUBAGENT_CAPABILITY_UNSUPPORTED` + schema 能力门控）。
- P1-2' handoff 契约已实施（`subagent_handoff.go` 状态机/防御解码 + 任务包解码 + 结果降级）；**`submit_report` 契约已实施**（按需注入 + 提交时校验 + 终态捕获 + `StructuredReport` 回填，见实施记录 §5.3）；逐请求 ToolChoice 强制未采用——强制每次请求的工具选择会与多步真实工具使用冲突，契约强度由提交时校验与状态机解码双保险提供。
- P2-1' fork 已实施（`subagent_context.go` 配平前缀/冻结/超限拒绝 + 双宿主 `ParentHistoryFor` 接线）；**fork 前缀度量已接 llm-cache-analytics 通道**（`fork_audit.go` 首请求审计字段写入 `cache_requests`，含 prefix 保持率与缓存读取证据口径，见实施记录 §3.4）。
- P2-2' iterate_goal 已实施（`iterate_goal.go` 固定编排 + G6 进展门控 stalled 终态 + `agents.iterateGoalEnabled` 灰度 + doom_loop 联动）；**goal 驱动器升级路径（入口 2）已实施**：CLI 与服务端驱动器在连续无进展达共享阈值（2）**或 doom_loop 告警上浮**时改用 `IterateEscalationPrompt`（至多一次、可用性由部署的 `agents.iterateGoalEnabled` 门控；告警标记被升级消费一次），见实施记录 §5.4。
- 真实终端验证 runbook（`docs/plan/multi-agent-real-terminal-validation-runbook.md`）已补齐验证 E（fork 上下文复述: `fork_prefix_*` 审计键 / `cache_read_tokens` 证据 / 超限与能力负路径）与验证 F（iterate_goal 多轮交接 / 可确定性构造的 stalled 变体 / 升级入口指纹），待真实 provider 凭据环境执行。
- 实施细节与测试矩阵见 `docs/multi-agents/agents/dsh-borrowing-implementation-record-20261016.md`。

---

## 1. 审查结论摘要

| # | 发现 | 严重度 | 处置 |
|---|---|---|---|
| F1 | fork 快照数据源写错：`sessionmeta` 是元数据 KV 表（键值合并/绑定复制），**不是轮次读取面**；checkpoint 是工件快照也不是对话历史 | 高（事实错误） | v2 P2-1' 改为快照父级**派生请求**（见 G5） |
| F2 | `iterate_goal` 与既有 goal 续跑体系架构重叠：`goal/continuation.go` 已实现"目标 + 多轮续跑 + 错误/无进展 strike ×3 + 预算收尾 + CLI 让位冷却"，且文案有逐字节一致性约束 | 高（第二事实源） | v2 P2-2' 把 iterate_goal 定位为 goal 驱动器的**升级路径**，共享 strike/预算语义 |
| F3 | 结构化输出能力位缺落地机制：原方案定义了 `OutputSchema` 能力位但没有说明子级输出如何被强制 | 中（机制缺失） | v2 P1-2' 用**强制工具调用**（ToolChoice 四适配器均已支持）实现 `submit_report` 契约 |
| F4 | fork 的 KV Cache 保持只有断言没有验收手段；且 `prompt_cache_breaker.go` 是重试熔断（同指纹失败退避），**不是缓存复用分析** | 中（不可度量） | v2 验收挂接 llm-cache-analytics 指标（G3） |
| F5 | 中断/暂停语义：`suspension_gate` + park/merge 已覆盖 DSH interrupt 的"暂停非销毁"——分析报告结论正确，原方案未再涉及，维持 | — | 无需动作 |
| F6 | P1-2 与 P2-3 是同一件事拆两处；P3-2 动态措辞被排到最后但它是能力位一致性的一部分 | 低 | v2 合并：P2-3 并入 P1-2'；P3-2 并入 P1-1' |

---

## 2. 事实核查证据

**F1 sessionmeta 的真实职责**（`sessionmeta/sessionmeta.go`）：`CanonicalKey/CandidateKeys/Set/Delete/CopyProfileBinding/CopyParentWorkspaceBinding`——是会话上下文 metadata 的键值读写与合并策略层，无轮次/消息读取接口。原方案"复用 sessionmeta / checkpoint 既有读取面"不成立。

**F2 goal 续跑已存在**（`goal/continuation.go:1-66`）：策略层含 `ContinuationErrorStrikeLimit=3`、`ContinuationNoProgressStrikeLimit=3`、`ContinuationCLICooldownWindow=5min`、`ContinuationPrompt`（含"无进展分类、blocked 需连续 3 轮同一阻塞"纪律）、`BudgetWrapUpPrompt`（预算耗尽收尾轮）。注释明确"CLI 循环与服务端驱动器共享同一套语义"。一个并行的 iterate_goal 会造成"同会话目标"与"子级迭代"两套推进语义竞争。

**F3 ToolChoice 已全线支持**：`llm/adapter/openai.go`、`gemini.go`、`codex.go`、`anthropic.go` 及 `llm/request_tools.go` 均有 tool_choice 通路。强制子级最后一次调用 `submit_report`（参数即 JSON Schema）在四个协议上都可落地，强度等价于 DSH 的 provider 级 outputSchema。

**F4 缓存验收**：`agent/prompt_cache_breaker.go` 只做"同 prompt_fingerprint 连续失败→退避熔断"，不度量缓存命中。仓库已有 `docs/plan/llm-cache-analytics-unified-plan.md` 方向的缓存分析面，fork 验收应产出"子级请求前缀与父级前缀共享 cache key 前缀"的实测指标，而非代码注释断言。

---

## 3. v2 设计目标（"可以做得更好"的标准）

原方案的目标是"把 DSH 的设计移植进来"。v2 把目标层次提高为"**在 AAR 既有强项上组合出超过 DSH 单点的设计**"：

- **G1 组合而非复制**：任何新机制必须与既有 goal / doom_loop / quota / turn_budget / suspension 组合；出现第二事实源即为设计失败。（修正 F2）
- **G2 契约优先于提示词**：能用结构化强制（tool_choice/JSON Schema/policy）表达的，绝不用自由文本约定。AAR 的 policy 栈（WriterArbiter、只读继承）本来就强于 DSH 的提示词建议——新能力保持同一水准。（修正 F3）
- **G3 可度量验收**：每个实施项绑定至少一个可观测指标（token 节省率、缓存共享率、轮数分布、握手失败率），验收=指标达标，不是"功能存在"。（修正 F4）
- **G4 默认安全 + 一键灰度**：新能力全部挂 `agents.capabilities.*` 双职责开关（既是能力位也是 feature flag），默认关；usageanalytics 提供灰度期数据。
- **G5 快照即派生请求**：fork 快照取父级**当前有效派生请求**（经 compaction 后的实际请求前缀），而非原始日志——天然处理压缩、天然继承缓存资格、天然对齐"已完成轮次"边界。（修正 F1，且优于 DSH 的原始日志前缀——DSH 无 compaction 后快照语义）
- **G6 进展门控**：迭代预算不只数轮，还要检测进展——复用 goal 的 no-progress strike 语义，卡住的迭代提前止损，而不是烧满 16 轮。（超越 DSH：ralph 只有轮数与状态机，无进展检测）
- **G7 协商三层一致**：能力位 → 工具措辞 → preflight/观察面 三层从同一份协商结果生成，杜绝"模型看到的承诺与运行时能力不一致"。（合并原 P3-2）
- **G8 文档与 runbook 同步交付**：每个实施项的 DoD 含 `docs/multi-agents` 更新与真实终端验证场景；没有 runbook 场景的功能不算完成。

---

## 4. 修订后的实施项（v2 delta，替代原方案对应章节）

### P1-1' 能力协商 v2（替代原 P1-1，吸收原 P3-2）

与原方案的差异：

1. **协商结果类型**取代静态 struct 单次检查：

```go
// backend/internal/agent/capabilities.go

// CapabilityNegotiation 是一次 spawn 请求的能力协商结果：
// 既是准入判决，也是工具措辞与 preflight 摘要的唯一数据源（G7）。
type CapabilityNegotiation struct {
    Granted  SubagentCapabilities      // 本次请求实际授予的能力位
    Refusals []CapabilityRefusal       // 逐字段拒绝原因（空=全过）
}
type CapabilityRefusal struct {
    Field   string // "provider_override" | "report_schema" | "fork_context" | ...
    Code    errors.ErrorCode
    Reason  string // 含可操作 next_action
}

// Negotiate 在任务入队前调用一次；结果随任务传递到 factory / prompt / 事件。
func Negotiate(caps SubagentCapabilities, task SubagentTask) CapabilityNegotiation
```

2. **三层一致生成**：
   - 工具 description：宿主装配时由 `DefaultCapabilities()` 生成（fork 可用/不可用两版措辞）；
   - preflight 摘要：协商结果进入 `toolexec/preflight` 的 spawn 前置说明；
   - 事件审计：`subagent.route.resolved` 载荷附 granted/refusals。
3. 错误码不变（`SUBAGENT_CAPABILITY_UNSUPPORTED`），但 refusal 明细让父级能逐字段修正，而非整包失败后猜测。

DoD 增项（G8）：`docs/multi-agents/agents.md` 能力位小节；措辞两版的 schema 快照测试。

### P1-2' 结构化回执 + handoff 契约（替代原 P1-2 与 P2-3）

核心升级（G2）：子级结构化报告从"自由文本 + 防御解码"升级为**强制工具调用契约**：

1. 子级 run 请求 `reportSchema` 时，loop 在最终步注入 `submit_report` 工具并以 ToolChoice 强制（四适配器已支持）；子级不调用即 run 失败（`REPORT_CONTRACT_VIOLATED`，不可重试原样请求）。
2. 参数即 schema 校验点：JSON Schema（object/properties/required/additionalProperties 子集，对齐既有 `assertObjectJsonSchema` 风格约束）在工具参数解析时校验——校验失败自然进入工具错误回路，子级可自行修正后重交。
3. **Handoff 就是第一个 submit_report schema**：`Handoff` 结构（状态机三态 + 字段约束 + `MaxHandoffChars`）原样保留，作为 `submit_report` 的内建 schema 之一；普通任务的 `ReportSchema` 是同机制的任意 schema 实例。
4. 防御解码保留为**第二道防线**（跨 run 边界的宿主侧 `DecodeHandoff`），不再是唯一防线。
5. **goal 态联动**（G1）：goal 会话的续跑驱动器可读取最近一次子级 handoff（存入 goal 状态），供 `ContinuationPrompt` 注入"上一子级轮交接摘要"。

DoD 增项：四适配器 ToolChoice 强制调用的 e2e 各一；handoff 失败率指标接入 usageanalytics（G3）。

### P2-1' fork = 派生请求快照（替代原 P2-1）

核心修正（G5，同时修复 F1）：

1. **快照对象 = 父级当前有效派生请求**：父会话下一次 `llm/stream` 会发送的消息前缀（system + 已完成轮次的渲染结果，compaction 后即压缩摘要 + 保留尾段）。读取面 = 会话存储的派生请求组装层（`prompt_builder` / `contextmgr` 的现有组装结果），不是原始日志、不是 sessionmeta。
   - 好处 a：compaction 语义天然正确——fork 看到的是父级"现在记得什么"，不是已被压缩掉的原始历史；
   - 好处 b：缓存资格天然成立——快照内容就是一份真实可发送前缀，与父级同 provider/model 时逐字节可复用；
   - 好处 c：轮次边界不再需要自定义"配平"规则——派生请求天然只含已完成轮次。
2. 注入方式：子会话初始历史 = 该前缀 + 新任务 user 消息；durable 字段不变（`ForkParentSessionID` + `ForkUpToSeq` → 更名为 `ForkPrefixFingerprint`，以派生请求指纹而非 seq 表达，避免压缩后 seq 语义漂移）。
3. 快照大小上限策略不变（超限整体拒绝建议 fresh，不静默截断）。
4. **验收指标（G3）**：fork 子级首请求与父级前缀的 cache key 共享率（经 llm-cache-analytics 通道），灰度目标 ≥90%（provider 支持 prompt caching 的前提下）；对比 fresh 基线的 prompt token 节省率同时上报。

其余约束（一次性冻结、只继承对话不继承权限、路由继承）与原方案一致；能力位联动、quota/arbiter 照常生效不变。

### P2-2' iterate_goal = goal 驱动器升级路径（替代原 P2-2）

核心修正（G1，同时吸收 G6）：

1. **定位重述**：iterate_goal 不是与 goal 续跑并列的第二套迭代机制，而是 goal 体系的**子级升级路径**：
   - 入口 1（独立工具）：模型显式调用 `iterate_goal`（原方案语义，用于无 goal 会话的多轮打磨）；
   - 入口 2（驱动器升级）：goal 续跑驱动器在**连续 no-progress strike ≥2** 或 doom_loop 告警触发时，可把下一轮续跑升级为"全新子级上下文的迭代轮"——子级带 goal objective + 最近 handoff 冷启动，完成后 handoff 写回 goal 态，goal 驱动器继续。
   - 两个入口共享同一实现内核（固定编排 + handoff + 进展门控），strike 计数与预算语义直接复用 `goal/continuation.go` 的常量与判定（G1：无第二事实源）。
2. **进展门控（G6，超越 DSH）**：每轮结算时按 goal 语义分类 progress / verified-wait / no-progress；连续 no-progress 达 `ContinuationNoProgressStrikeLimit` 提前返回 `{status: "stalled", rounds_started, report}`——新增第五终态，避免烧满轮数预算。
3. 预算联动升级：轮数预算（默认 16、硬上限 64）+ 每轮 turn_budget 之外，增加迭代总 token 预算（`agents.iterateGoal.maxTotalTokens`），超限立即 `budget-limited` 收尾（复用 `BudgetWrapUpPrompt` 语义做收尾轮）。
4. doom_loop 联动文案不变（advisory 建议改用 iterate_goal）。

DoD 增项：进展门控状态机测试（progress/wait/stall 分类 × strike 边界）；goal 驱动器升级路径的 e2e（goal 会话 → no-progress → 子级迭代轮 → handoff 写回）；`docs/multi-agents/teams/` 增补"goal 与 iterate_goal 的选择指引"。

### P3（维持，微调）

- P3-1 参数分层文档：不变（P1-1' 已吸收措辞部分）。
- P3-2 动态措辞：**已并入 P1-1'**，从本阶段移除。
- P3-3 观察项：不变（编排 DSL / 外部 provider 评估）。

---

## 5. 修订后阶段表

| 阶段 | 内容 | 预估 | 关键变化 vs 原方案 |
|---|---|---|---|
| Phase 1 | P1-1' 能力协商 v2（含动态措辞）+ P1-2' 结构化回执/handoff | 2 周 | ToolChoice 契约化；三层一致；P2-3/P3-2 并入 |
| Phase 2 | P2-1' fork 派生请求快照；P2-2' iterate_goal goal 升级路径 | 2–4 周 | 数据源修正；架构去重叠；进展门控 |
| Phase 3 | P3-1 参数分层文档 + P3-3 观察项 | 按需 | P3-2 已前移 |

依赖图（修订）：P1-1' → P1-2' → P2-2'（内核依赖 handoff + 能力位）；P2-1' 仅依赖 P1-1'，与 P1-2' 后并行。

## 6. 验收指标表（G3 汇总）

| 实施项 | 指标 | 灰度目标 |
|---|---|---|
| P1-1' | 能力拒绝误报率（拒绝后被宿主放宽确认的比例）；措辞/协商不一致 bug 数 | 0 |
| P1-2' | handoff 校验失败率；submit_report 重交收敛率（≤2 次内成功占比） | <1%；>95% |
| P2-1' | fork 前缀 cache key 共享率；prompt token 节省率 vs fresh | ≥90%；≥40%（长会话） |
| P2-2' | 迭代 stalled 提前止损占比；budget_limited 占比；每轮 no-progress 分布 | 有数据即可，用于调默认轮数 |

## 7. 审查后的风险补充

| 新增风险 | 缓解 |
|---|---|
| ToolChoice 强制调用在部分 provider 上行为差异（如强制后仍输出文本） | 四适配器 e2e 前置；不支持稳定的 provider 上该能力位置 false（协商自动降级为提示词 + 防御解码） |
| 派生请求快照与父级并发组装的竞态（父级正在新一轮 pre-step） | 快照读取限定父级 idle 或轮边界（复用 suspension/settlement 的轮边界判定）；父级 running 时 fork 请求排队或拒绝并说明 |
| goal 驱动器升级路径双写 goal 态 | handoff 写回走既有 goal status_update 通道，单写者（驱动器），子级不直接写 goal |

---

## 8. 与原方案的条目对照

| 原方案 | v2 处置 |
|---|---|
| P1-1 能力位 | 保留，升级为协商结果类型 + 三层一致（P1-1'） |
| P1-2 handoff | 保留结构，升级为 ToolChoice 契约 + goal 联动（P1-2'） |
| P2-1 fork | 保留约束，重定义快照对象与数据源（P2-1'） |
| P2-2 iterate_goal | 保留内核，重定位为 goal 升级路径 + 进展门控（P2-2'） |
| P2-3 防御解码 | 并入 P1-2'（第二道防线） |
| P3-1 参数分层 | 保留 |
| P3-2 动态措辞 | 并入 P1-1' |
| P3-3 观察项 | 保留 |
