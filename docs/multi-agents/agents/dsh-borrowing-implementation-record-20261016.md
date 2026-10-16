# DSH 借鉴实施方案——实施记录（P1-1' / P1-2' / P2-1' / P2-2'）

> 实施日期：2026-10-16
> 方案来源：`docs/plan/deepseek-harness-agent-borrowing-plan-review-v2-20261016.md`
> 背景分析：`docs/analysis/deepseek-harness-vs-aar-agent-system.md`
> 状态：**Phase 1 全部完成；Phase 2 内核全部完成；宿主接线完成**。剩余项见 §6。

---

## 1. 本次交付总览

| 实施项 | 内容 | 状态 |
|---|---|---|
| P1-1' | spawn 准入期能力位协商（`SUBAGENT_CAPABILITY_UNSUPPORTED`） | ✅ |
| P1-2' | handoff 契约（状态机 + 防御解码 + 结果降级） | ✅ |
| P1-2' 收尾 | spawn 任务包解码 `context`/`report_schema`/`handoff_in` | ✅ |
| P2-1' | fork 上下文快照（已完成轮次一次性注入，绝不静默降级） | ✅ |
| P2-1' 接线 | 宿主 `ParentHistory` 数据源（CLI + runtime-server 双宿主） | ✅ |
| P2-2' | iterate_goal 有界全新上下文迭代（含进展门控 stalled 终态） | ✅ |
| P2-2' 接线 | `agents.iterateGoalEnabled` 配置灰度（双宿主） | ✅ |
| P2-2' 入口 2 | goal 驱动器升级路径：无进展达共享阈值 **或 doom_loop 告警上浮** → `IterateEscalationPrompt`（CLI + 服务端双驱动器，至多一次） | ✅ |
| P1-2' 收尾 | `submit_report` 契约：按需注入 + 提交时校验 + 终态捕获 | ✅ |
| P2-1' 配置 | `agents.forkMaxSnapshotTokens`（双宿主，0=运行时缺省 96K） | ✅ |
| G7 第三层 | 未授予能力位不进模型 schema（schema 零增量） | ✅ |
| G4 | fork/schema/iterate_goal 全部默认关，配置显式开启 | ✅ |

## 2. 能力位模型（P1-1'）

### 2.1 行为契约

- **spawn 准入期一次性校验**：`prepareTasks` 在任何子会话、批次行、任务行产生之前完成协商；拒绝以 `SUBAGENT_CAPABILITY_UNSUPPORTED` 整批失败，错误信息含字段名与 `next_action`（可操作建议）。
- **默认能力位 = 现状零行为变化基线**：`AgentOptions`/`ToolFilter`/`Persona` 默认开（今天已支持的请求位）；`ForkContext`/`OutputSchema` 默认关。
- **fork + 路由覆盖组合拒绝**：即使 fork 已授予，fork 任务携带 provider/model/reasoning 覆盖仍然被拒（KV Cache 保持约束，G2）。
- **同源三层一致（G7）**：能力位 → 工具 schema 字段 → 准入判决/审计载荷，三层从同一协商模型生成。

### 2.2 配置（`agents.*`）

```yaml
agents:
  # 子代理上下文继承（P2-1'）：fork 子级以父会话已完成轮次的一次性快照起步。
  forkContextEnabled: false   # 默认关（G4）
  # 结构化回执通道（P1-2'）：report_schema / handoff_in 任务字段。
  outputSchemaEnabled: false  # 默认关（G4）
  # 有界全新上下文迭代工具（P2-2'）。
  iterateGoalEnabled: false   # 默认关（G4）
```

未开启时：对应字段**不出现在模型看到的 spawn_subagents schema**（零 token 增量），模型即使手动传入也在准入期被拒。

### 2.3 拒绝错误示例

请求 `context: "fork"` 而未开启能力：

```text
[SUBAGENT_CAPABILITY_UNSUPPORTED] task t1 requests unsupported capability
(fork_context: deployment does not grant capability "fork_context";
next_action=use_fresh_context_or_enable_agents.capabilities.fork_context)
```

## 3. fork 上下文继承（P2-1'）

### 3.1 语义

- **快照对象**：父会话消息的**配平前缀**——已完成轮次全含；尾部未回复的 user 消息（父级进行中的轮次）截断；父级 system 消息剥离（子级 system prompt 由子级 prompt_builder 独立组装）。
- **一次性冻结**：`BuildForkHistory` 深拷贝取走；此后父会话追加内容不到达子级（无实时共享）。
- **不静默降级**：宿主未接线 `ParentHistoryFor`、或快照超过 token 上限（默认 96K，超限前整体拒绝）时，fork 子级以可操作错误失败——绝不会悄悄变成 fresh 让委派丢失全部前期结论。
- **不继承权限**：fork 只传对话；工具面/只读边界仍由 `resolveChildToolSurface` 独立计算。
- **路由继承**：fork 全量注入走 `loopRunOptions.History`（scheduler 路径子级本就不落会话持久层），子级天然使用父级 provider/model——KV Cache 前缀复用资格成立。

### 3.2 与既有 `spawn_agent` fork 的关系

`spawn_agent`（单发）已有会话层 fork（`fork_context`/`fork_turns`/`agents.defaultForkTurns`，会话 `Clone` + `ReplaceHistory`）。
本次为 **spawn_subagents（批次）路径**补上 fork：任务包字段 `context: "fresh" | "fork"`。
两条路径语义一致（继承已完成轮次对话、不含 in-flight 轮次），实现层不同（会话克隆 vs loop 历史注入）——因为批次子级是进程内临时 worker，没有落盘会话。

### 3.3 使用示例

```json
// spawn_subagents 任务条目（forkContextEnabled: true 时）
{
  "agents": [{
    "id": "reviewer",
    "goal": "基于上文分析，为认证模块写单元测试",
    "context": "fork"
  }]
}
```

## 4. 结构化回执与 handoff（P1-2'）

### 4.1 handoff 契约

```
{status: "continue"|"complete"|"blocked", summary, evidence[], next_steps[], blocker}
```

- `continue`：至少一条 next_steps，blocker 必须为空；
- `complete`：至少一条 evidence，无 next_steps，blocker 为空；
- `blocked`：blocker 必须具体（非空白）。
- 所有文本非空且 trim 相等；序列化长度 ≤ 16K（默认，超限报错含实测值，不静默截断）。

### 4.2 两道防线

1. **任务入参解码**：`handoff_in` 经 `ValidateHandoff` 状态机预检，非法交接在解码期即拒绝（不会进子级 prompt）；
2. **产物防御解码**：子级最终输出若是 JSON 对象，经 `DecodeHandoff`（键集合精确匹配 + 类型断言 + 状态机）后进入 `HandoffOut`；无效输出降级为 route warning（其余产出不受影响），绝不把未校验数据带给父级。

### 4.3 使用示例

```json
// writer→verifier 交接（outputSchemaEnabled: true 时）
{ "id": "verify", "goal": "验证 writer 改动", "read_only": true,
  "handoff_in": { "status": "continue", "summary": "parser 骨架完成",
    "next_steps": ["补 CLI flag 测试"], "evidence": [], "blocker": "" } }
```

## 5. iterate_goal（P2-2'）

### 5.1 语义（deployment-owned）

- 模型只提供 `objective` + 可选 `max_rounds`；循环结构、handoff schema、进展门控全部部署侧固定。
- 每轮 spawn 一个**全新**子级（fresh 上下文；无父对话、无前轮会话），仅上一轮 handoff 跨轮。
- 默认 16 轮，硬上限 64（本地成本考虑；超限请求被拒并报实测值）。
- **进展门控（G6，超越 DSH）**：内建分类器按 nextSteps 复述检测无进展；连续 3 轮无进展 → `stalled` 终态提前止损，不烧满轮数预算。
- 终态五分类：`complete` / `blocked` / `budget-limited` / `round-failed` / `stalled`。
- 前台语义：调用阻塞父轮直到终态（对齐 DSH Ralph foreground 设计）。
- doom_loop 联动：重复语义批次的告警 payload 携带 `advice` 建议改用 iterate_goal（一次性低频；不进逐轮 prompt，避免 token 预算漂移）。

### 5.2 启用与调用

```yaml
agents:
  iterateGoalEnabled: true
```

模型侧调用：

```json
{"objective": "逐步完善 docs/x.md 直到逻辑自洽", "max_rounds": 8}
```

返回：

```text
iterate_goal finished: status=complete rounds_started=3

Final handoff: {"status":"complete","summary":"...","evidence":["..."],...}
```

### 5.3 结构化回执契约（submit_report，P1-2' 收尾）

任务携带 `report_schema` 时，子级 loop 在工具面冻结后**按需注入** `submit_report`
工具（参数即请求的 schema）。子级以其**一次性**交付结构化回执：

- **提交时校验**（G2 契约优先）：对象形态、`required` 键齐全、
  `additionalProperties=false` 时拒绝未声明键；校验失败按普通工具错误返回，
  模型可在下一步修正重交。
- **终态语义**（terminal protection）：首个被接受的回执即为最终交付物，
  覆盖 run 的文本 Output（JSON）；重复提交被拒。
- **蓝图回填**：scheduler 的解码路径把回执捕获为 `SubagentResult.StructuredReport`；
  handoff 通道（HandoffIn != nil）仍经严格状态机解码为 `HandoffOut`。
- **iterate_goal 内核**：每轮报告走部署自有 schema（`ReportSchemaGranted`，
  不经模型能力协商），提交失败按 no-progress 熔断计数。
- **未提交降级**：子级结束但未提交时，文本输出保留并追加
  `report_contract_missing` route warning；绝不把未校验数据当回执。

### 5.4 goal 驱动器升级路径（P2-2' 入口 2）

goal 自动续跑的两个驱动器（CLI `chat_goal_auto_continue.go` 与服务端
`session_goal_driver.go`）在**连续无进展达到共享阈值**
（`goal.IterationEscalationStrikeThreshold = 2`，先于停止线 3）**或收到
doom_loop 告警**（同一语义工具批反复回放）时，把下一次续跑改用共享的
`goal.IterateEscalationPrompt`：

- **升级路径**：提示模型调用 `iterate_goal`（一次、有界轮数），完成后从最终
  handoff 与工作区证据继续目标；handoff 自然沉淀在会话记录中，goal 态由既有
  `update_goal` 纪律维护（单写者：驱动器不直接写 goal 态）。
- **回退路径**：部署未启用 `agents.iterateGoalEnabled` 时**不注入**升级提示
  （避免指示不存在的工具）；提示词内置兜底文案（要求改变做法或中止重复工具批）。
- **doom_loop 信号上浮**：agent 循环发出的 `tool_loop.doom_loop_warning`
  （会话级运行时事件）被两个驱动器订阅——CLI 在续跑循环期间订阅本地宿主
  `EventBus`（循环外订阅自动释放）；服务端在 `subscribeGoalDriver` 与
  `session_end` 订阅同一条总线并记会话标记。该标记被升级注入**消费一次**；
  `resetStrikes` 不自动清除它（告警多发生在「有输出但重复工具批」的成功轮，
  否则信号会在评估阶段被误清）。
- **至多一次**：同一空转序列内升级只注入一次；升级轮若仍无进展则按原停止线
  置 blocked。升级轮产生实质进展（非空输出或有工具执行）时，strike 与升级
  标记一并重置，后续再次空转/告警允许再来一次。
- **双驱动器同源**：阈值、提示词、判定（`goal.DecideIterationEscalation`）均在
  `internal/goal` 共享层；两侧各自的可用性判定分别读本地宿主
  （`goalIterationEscalationAvailable`）与服务端 `agentsConfig()`。

## 6. 剩余工作与已知边界

| 项 | 说明 |
|---|---|
| ToolChoice 逐请求强制 | `submit_report` 采用**按需注入 + 一次性提交**（见 §5.3）：强制每次请求的工具选择会与多步真实工具使用冲突，因此未采用；契约强度由提交时校验（required / additionalProperties）+ 状态机解码双保险提供 |
| fork 的 KV Cache 共享率度量 | 验收指标（G3）待接 llm-cache-analytics 通道，灰度期观测 |
| 真实终端验证 | 按 `docs/plan/multi-agent-real-terminal-validation-runbook.md` 补充两个场景：fork 上下文复述、iterate_goal 多轮交接 |

## 7. 测试与验收

新增测试（全部通过）：

| 文件 | 覆盖 |
|---|---|
| `internal/agent/capabilities_test.go` | 默认基线、fork/schema 拒绝与授予、fork+路由覆盖组合拒绝、错误码与 next_action、工具过滤/路由覆盖降级开关 |
| `internal/agent/subagent_handoff_test.go` | 三态状态机、trim 归一化、16K 边界（=max/+1 含实测值）、键集合变体（多/缺/类型错/状态不符）、null 列表、降级、形态识别、深拷贝 |
| `internal/agent/subagent_context_test.go` | 配平前缀（in-flight 截断）、system 剥离、空父级等价 fresh、冻结性、超限拒绝、宿主未接线可操作错误 |
| `internal/agent/iterate_goal_test.go` | 首轮 complete/blocked、continue 链、budget-limited、stalled 门控、非结构化熔断、超限拒绝、默认关工具面、schema 能力门控 |
| `internal/config/agents_normalize_test.go` | bool 探针扩展（新字段纳入穷尽式归一化守卫） |

回归范围：`internal/agent`、`internal/config`、`internal/errors`、`internal/goal`、`internal/team`、`internal/supervision`、`internal/subagentbatch`、`internal/subagentquota`、`internal/chat`、`internal/api/runtimeapi` 全绿；`go build ./...` 全仓通过。

## 8. 关键实现文件索引

| 文件 | 职责 |
|---|---|
| `internal/agent/capabilities.go` | 能力位、`Negotiate`、`SubagentRequestWants`、上下文常量 |
| `internal/agent/subagent_handoff.go` | Handoff 状态机、防御解码、候选识别、降级包装 |
| `internal/agent/subagent_context.go` | fork 配平前缀、快照构建、token 上限、fork 批次探测 |
| `internal/agent/iterate_goal.go` | 迭代内核、进展分类器、工具 schema、执行入口、启用开关 |
| `internal/agent/scheduler.go` | 准入门控、任务/结果字段、fork 历史注入、route receipt |
| `internal/agent/loop.go` | 任务包解码、iterate_goal 执行分支与工具暴露、spawn schema 能力门控 |
| `internal/agent/subagent_batch_coordinator.go` | `ParentHistoryFor` 注入（仅 fork 批次触发） |
| `internal/errors/codes.go` | `SUBAGENT_CAPABILITY_UNSUPPORTED` |
| `internal/config/manager.go` | 三个灰度配置字段 |
| `cmd/aicli/commands/chat_actor_host.go` | CLI 宿主接线（能力位/父历史/iterate_goal 开关） |
| `internal/api/runtimeapi/{handler,supervision_batch_store}.go` | runtime-server 宿主接线 |