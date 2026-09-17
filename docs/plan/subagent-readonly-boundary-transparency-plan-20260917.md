# 子代理只读边界透明度优化实施方案（subagent-readonly-boundary-transparency）

- 日期：2026-09-17
- 状态：已实施（P0-M3/M4、P1-M1/M2、P2-M5/M6 完成，全量构建通过；P3-M7 事件持久化/UI 聚合按范围评估后另行跟踪）
- 关联文档：`multi-agent-execution-optimization-plan.md`（N11 已修复"写型工具 schema 仍下发"）、`supervision-parent-child-control-optimization-plan-20260917.md`
- 触发场景：会话 `session_20260917155815_3vE4qX2F` 单个 turn 运行 ~54 分钟，`tool.denied` 从 4 增长到 89，全部来自 `read_only=true` 后台子代理反复尝试写操作后被硬拒（实时快照 `snapshot_revision=1126`）

---

## 1. 背景与问题

### 1.1 事实观察

| 现象 | 证据 |
|---|---|
| 只读子代理反复发起写操作 | `session_20260917155815_3vE4qX2F` 中 `tool.denied` 累计 89 次（08:33 时仅 4 次），且仍在增长 |
| 子代理无步数/预算上限 | 状态快照 `step=3/unlimited`、`max_steps=0`、无 `budget_tokens`/`timeout` |
| 父模型在 spawn 时无任何提示 | `spawn_subagents` 每条显式 `read_only: true`，goal 却要求产出产物，决策点无警告 |
| 模型读到的是裸错误串 | 拒绝路径 `result.Error = err.Error()`（`loop.go:2314/2721`），模型可见文本为 `read-only policy blocks write-like tool: write` 这类无出路描述 |
| 子代理 system prompt 无边界横幅 | 仅 builtin `explore` agent 有 Body（`agentdef/builtin.go:16`），显式 `read_only=true` 的子代理没有任何"你是只读的、不要尝试写、写需求回报父代理"的说明 |
| `tool.denied` 只活在实时通道 | 事件不进 `session_events` 持久化，无法事后审计；UI 逐条渲染造成刷屏 |

### 1.2 根因判断

**执行层已正确，透明度缺失。** 当前机制已经做到：

- 写型工具从只读子代理的模型可见工具面移除（`filterPolicyBlockedToolDefinitions`，`loop.go:3353-3358/3572`）；
- shell 逐命令分类且带结构化原因（`AssessShellReadOnlyCommand` + `ShellReadOnlyReason*`，`grants.go:216-221/231`）；
- `tool.denied` 事件携带 `error_code=ErrAgentReadOnly / overridable=false / retryable=false / next_action`（`loop.go:2925-2935`）；
- `ReadOnlyFilteredTools` 回传父代理（`child_factory.go:86-106`、`loop.go:6323-6326`）。

但**模型在违反边界之前看不到任何预示，违反之后读不到"为什么 + 该怎么办"**，于是只能靠试错撞边界，撞一次烧一次 LLM 调用。

### 1.3 目标

让只读机制对 LLM 呈现为一条完整链路：

1. **spawn 时警告**（父模型在决策点就知道边界与 goal 的冲突）；
2. **子代理启动即知边界**（system prompt 横幅）；
3. **shell 描述预告边界**（工具 description 动态注入）；
4. **被拒必带原因和出路**（拒绝文本结构化）；
5. **反复被拒升级为一次显式决策**（写需求上浮，不再无限空转）。

### 1.4 非目标

- 不改变只读边界的强制语义（`overridable=false` 保持不变）；
- 不引入模型权限提升路径；
- 不重写 spawn/delegation 调度主流程。

---

## 2. 架构原则

**"边界一次计算、三处同步可见、拒绝必带出路"。**

`read_only` 相关信息当前分散在 spawn schema 文案、`effective_tool_surface`、`ReadOnlyFilteredTools`、`tool.denied` payload 四处，各自独立生成。优化后统一为一个**边界清单对象（CapabilityManifest）**，在子代理配置生成处一次计算，由单一渲染函数输出到三个位置，延续 `ReadOnlyChildOptionDescription`（`capability_scope.go:33-43`）已有的"单一文案源防漂移"模式。

---

## 3. 设计总览

| # | 能力 | 现状 | 目标 | 主要落点 |
|---|---|---|---|---|
| M1 | CapabilityManifest | 信息散落四处 | 子代理配置处一次计算，单一渲染函数三处输出 | `child_factory.go` 新建 |
| M2 | 子代理 system prompt 边界横幅 | 缺失 | 会话启动注入 READ-ONLY BOUNDARY 块 | `child_factory.go` / prompt 注入 |
| M3 | 拒绝消息可读化 | 裸 `err.Error()` | 原因+规则+修复+替代 四元组 | `loop.go` `emitToolDenied` 前置 |
| M4 | shell 工具描述动态化 | 静态描述 | 只读子代理 description 注入只读命令示例与禁令 | shell tool definition 构建处 |
| M5 | spawn 契约检查 | 无 | goal 写意图检测 → `route_warnings` | `decodeSubagentTasks` |
| M6 | 写需求上浮 | 无限 deny | N 次拒绝后发 `subagent.requires_write` 事件并终止子代理 | `loop.go` 拒绝计数 |
| M7 | `tool.denied` 落库 + UI 聚合 | 只走实时通道 | 持久化 + 按 (agent, tool, reason) 聚合折叠 | 事件持久化 + 前端渲染 |

---

## 4. 模块变更清单

### M1 CapabilityManifest（边界清单一等公民）

新结构（建议放 `backend/internal/policy/capability_scope.go` 或新文件 `backend/internal/agent/boundary.go`）：

```go
type BoundaryManifest struct {
    ReadOnly        bool     `json:"read_only"`
    RemovedTools    []string `json:"removed_tools"`     // 写型/后台工具
    ShellReadOnly   bool     `json:"shell_read_only"`   // 逐命令分类
    PermissionMode  string   `json:"permission_mode"`
    DelegationDepth int      `json:"delegation_depth"`
    EscalationPath  string   `json:"escalation_path"`   // 写需求出路
    Source          string   `json:"source"`            // explicit / agentdef / parent_tool_execution_policy
}

func RenderBoundaryManifest(m BoundaryManifest) string // 单一文案源
```

- 计算位置：`child_factory.go` 已有全部输入（`task.ReadOnly / ReadOnlySource / ToolsWhitelist`、`childPolicy`、`read_only_source`）。
- 三处输出：
  1. **spawn 结果回传**：`renderSubagentResults`（`loop.go:6323`）从 `ReadOnlyFilteredTools` 扩展为完整 manifest；
  2. **子代理 system prompt**（见 M2）；
  3. **拒绝消息**（见 M3）。
- 回归约束：manifest 文案与 `ReadOnlyChildOptionDescription` 同源，禁止再出现第三份互相矛盾的文本（历史教训见 `multi-agent-execution-optimization-plan.md` N11"同源漂移"）。

### M2 子代理 system prompt 边界横幅

- 在 `child_factory.go` 生成 `childConfig.Options` 处（已有 `Options["read_only"]=true` 与 `read_only_source` 注入先例）追加一个 `options["boundary_banner"]`，或直接注入 prompt 扩展：
  ```
  READ-ONLY BOUNDARY（来源: explicit / agentdef / parent_tool_execution_policy）
  - 写型工具（write/edit/apply_patch/append_write/multiedit/download/background_task）已从本代理移除，执行期也会拒绝
  - shell 仅放行只读命令（git status/diff/log/show、rg、ls/glob、Get-Content…），复合/变更命令一律硬拒
  - 需要写文件时：不要尝试，把拟修改内容回报父代理，由父代理执行或以 read_only=false 重建本代理
  - 此边界不可被审批/bypass 放宽（overridable=false）
  ```
- 参考模板：`agentdef/builtin.go:16` explore Body（"You are a read-only explorer. Prefer view/grep/glob/ls..."），将其推广为显式 `read_only=true` 子代理的默认提示块。

### M3 拒绝消息可读化（核心改动）

现状：`loop.go:2314/2721/2344` 直接 `result.Error = err.Error()`；`finalizeDeniedToolResult`（`loop.go:2840-2850`）把 `result.Error` 作为模型可见 tool_result 文本，结构化字段只留在 metadata。

改法：在 `emitToolDenied` 之前加 `renderDenialGuidance(toolName, err, manifest)`，产出模型可读文本：

```
[TOOL_DENIED:ERR_READONLY_TOOL] apply_patch
boundary: read_only（子代理硬边界，approval/bypass 均不可放宽）
rule: 写型工具已从本代理工具面移除，执行期拒绝
fix: 不要再尝试此工具；把拟修改内容回报父代理执行，或请父代理以 read_only=false 重建本代理
```

shell 被拒时复用 `AssessShellReadOnlyCommand` 的 `Reason`（`grants.go:216-221` 四类），因类给出路：

| 错误码 | Reason | 附加提示 |
|---|---|---|
| `ERR_READONLY_SHELL` | `command_not_allowlisted` | 读取请用 Get-Content/rg；要写文件请回报父代理 |
| `ERR_READONLY_SHELL_COMPOUND` | `compound_command` | 请用 shell 的 commands 数组分条提交，逐条校验 |
| `ERR_READONLY_SHELL_DYNAMIC` | `dynamic_shell_syntax` | 只读策略不执行动态语法/变量拼接命令 |
| `ERR_READONLY_SHELL_EMPTY` | `empty_command` | 空命令，无需执行 |

实现要点：

- metadata 中的 `error_code / retryable / next_action / policy / policy_source / overridable` 字段**保持原样**（机器可读契约不回退）；
- 只替换模型可见文本：`result.Error = renderDenialGuidance(...)`；
- 新增单测断言 tool_result 文本包含 `fix:` 行与错误码（参照 `read_only_tool_surface_test.go` 风格）。

### M4 shell 工具描述动态化

- 构建 shell tool definition 时，若子代理执行策略为只读，description 动态注入：
  ```
  READ-ONLY MODE: only read-only commands are allowed (git status/diff/log/show, rg, ls, Get-Content, Select-Object, ...). Write/mutating/compound commands are hard-denied. If you need to change files, return the change to the parent instead.
  ```
- 落点：shell 工具定义构建处（`tool_list.go` / shell 定义），以 child policy 判定；文本同样来自 `RenderBoundaryManifest` 或共享常量。

### M5 spawn 契约检查（决策点前置）

- 位置：`decodeSubagentTasks`（`loop.go:5706`）或 `child_factory.go` 校验阶段。
- 逻辑：对每条 goal 做**写意图启发式检测**（动词表：write/edit/apply_patch/create/modify/implement/patch/生成/修改/创建/写入 等）。命中且 `read_only=true` → 追加 `route_warnings`：
  ```
  goal 疑似需要写权限，但 read_only=true 会剥离全部写型工具；请改为 read_only=false，或把目标收窄为纯调研并在提示词中写明只回报结论
  ```
- `route_warnings` 为现成机制（timeout/token 裁剪已走此通道），父模型在 spawn 工具结果中即时可见，可当场纠正。
- 注意：启发式检测只做提示，不阻断（避免误杀 "read-only policy review" 类合法目标）。

### M6 写需求上浮（把 89 次拒绝收敛成 1 次决策）

- `loop.go` 维护每个子代理（session）的连续 `AGENT_READ_ONLY` 拒绝计数；≥N（建议 3）时：
  1. 向父会话邮箱发 `subagent.requires_write` 生命周期事件（payload：子代理 id、goal、被拒工具与次数、fix 文案）；
  2. 该子代理提前终止，终态消息明确"goal requires write access; re-spawn with read_only=false 或接受只读结论"；
  3. 计数随成功调用重置。
- 复用既有的 stall/熔断观察模式（`tool_loop.exploration_stall_observed`、`MaxRepeatedToolCalls` 思路），但语义改为"写需求上浮"而非静默熔断。

### M7 `tool.denied` 落库 + UI 聚合

- 持久化：选择 `session_events` 写入路径（当前 0 行），事件类型 `tool.denied`；保留 `policy / error_code / policy_source / overridable` 字段。
- 可观测环：observe ring 增加 `tool.denied` 过滤类型统计（当前已被过滤，需显式放行类别计数）。
- UI：timeline（`chat_runtime_events.go:7306` 渲染处）对同 (session, tool, reason) 的连续拒绝聚合折叠，展示计数 + "终止该子代理"操作。

---

## 5. 错误码表（新增）

| 错误码 | 场景 | 是否可重试 |
|---|---|---|
| `ERR_READONLY_TOOL` | 只读边界拒绝写型工具 | 否 |
| `ERR_READONLY_SHELL` | 只读边界拒绝非白名单 shell 命令 | 否 |
| `ERR_READONLY_SHELL_COMPOUND` | 复合命令无法逐条校验 | 否（改 commands 数组后可重试） |
| `ERR_READONLY_SHELL_DYNAMIC` | 动态语法/变量拼接 | 否 |
| `ERR_READONLY_SHELL_EMPTY` | 空命令 | 否 |
| `ERR_SPAWN_GOAL_WRITE_INTENT` | spawn 契约检查警告（M5，route_warnings） | 是（修正 read_only/goal 后） |
| `ERR_REQUIRES_WRITE_ESCALATION` | 写需求上浮事件（M6） | 是（父代理决定重建） |

错误码常量建议统一放 `errors` 包或 `toolresult` 包，与既有 `ErrAgentReadOnly` 同族。

---

## 6. 文件级变更清单

### 后端

| 文件 | 变更 |
|---|---|
| `backend/internal/policy/capability_scope.go` | 新增 `BoundaryManifest` 与 `RenderBoundaryManifest`（或新文件 `boundary.go`） |
| `backend/internal/agent/child_factory.go` | 计算 manifest；注入边界横幅提示；ReadOnlySource 并入 manifest |
| `backend/internal/agent/loop.go` | M3 `renderDenialGuidance`；M5 写意图检测 + route_warnings；M6 拒绝计数与上浮事件 |
| `backend/internal/agent/scheduler.go` | `SubagentTask` 增加 manifest/横幅透传字段 |
| `backend/internal/agent/tool_list.go` | M4 shell 描述动态化 |
| `backend/internal/toolbroker/broker.go` |（仅当需要把 manifest 预览回传 spawn 结果时） |
| `backend/internal/agentdef/builtin.go` | explore Body 文本与横幅模板对齐 |
| 事件持久化路径（`session_events` 写入处） | M7 tool.denied 落库（结构字段与 `emitToolDenied` payload 对齐） |

### 前端/渲染

| 位置 | 变更 |
|---|---|
| `chat_runtime_events.go:7306` 附近 | M7 聚合折叠渲染 + "终止该子代理"操作 |
| 拒绝消息展示样式 | 补充错误码徽标，折叠 fix 建议 |

### 测试

| 文件 | 覆盖 |
|---|---|
| `read_only_tool_surface_test.go` 扩展 | M1 manifest 三处同步；M3 拒绝文本含错误码与 fix 行 |
| 新增 `boundary_manifest_test.go` | manifest 渲染与文案同源（防漂移） |
| 新增 `denial_guidance_test.go` | 四类 `ShellReadOnlyReason*` → 四类出路；metadata 字段不回退 |
| `decodeSubagentTasks` 单测 | M5 写意图检测命中/误报用例 |
| M6 单测 | 连续拒绝触发上浮事件、计数重置、终态消息 |

---

## 7. 风险与兼容性

| 风险 | 缓解 |
|---|---|
| 拒绝文本变长增加 token | 控制 fix 文案长度（≤4 行），错误码优先；只发生在未命中边界时的路径 |
| M5 启发式误报 | 只进 `route_warnings`，不阻断 spawn；动词表保持可配置 |
| M6 提前终止可能截断合法长任务 | 计数按"连续失败"且仅 read_only 类别；阈值可配置；终态消息给出重建指引 |
| 前端聚合改变既有事件流 | 聚合为纯展示层变化，事件本身仍逐条持久化（M7） |
| 依赖 provider 对超长 description 敏感 | shell 描述增量仅在只读子代理注入，量级 ~2 行 |

---

## 8. 分阶段落地与验证

| 阶段 | 内容 | 验证 |
|---|---|---|
| P0 | M3 + M4（拒绝可读化 + shell 动态描述） | 单测 + 手工复现 `session_20260917155815_3vE4qX2F` 场景：只读子代理第一次被拒即可看到 fix 出路 |
| P1 | M1 + M2（manifest + 子代理横幅） | 打开子代理 debug 日志确认横幅注入；spawn 结果含完整 manifest |
| P2 | M5 + M6（契约检查 + 上浮） | 用写意图 goal + read_only=true 复现 route_warnings；连续 3 次拒绝触发 `subagent.requires_write` 并终止 |
| P3 | M7（落库 + UI 聚合） | 事后 SQL 可查 `tool.denied`；UI 折叠展示并支持终止操作 |

回归基线：`go test ./backend/internal/agent/... ./backend/internal/policy/...` 全绿；`usage_subagents.read_only` 列语义不变；既有 `read_only_tool_surface_test.go`、`broker_agent_test.go` 不受影响。

部署说明：改动涉及 runtime-server 核心（loop/child_factory），需重启 runtime-server 生效；前端聚合随 UI 发布。---

## 9. 实施记录（2026-09-17）

### 9.1 交付清单

| 里程碑 | 内容 | 位置 |
|---|---|---|
| P0-M3 | 拒绝消息可读化：`renderDenialGuidance` 输出 `[TOOL_DENIED:ERR_READONLY_*]` + `boundary/rule/fix` 三段式；细分错误码 Tool / Shell / ShellCompound / ShellDynamic；连续拒绝达到 Escalate 阈值追加强提示 | `backend/internal/agent/denial_guidance.go`；接入 `loop.go finalizeDeniedToolResult` |
| P0-M4 | shell 工具 description 动态注入 `READ-ONLY MODE` 预告（只读子代理第一轮即见边界） | `backend/internal/agent/denial_guidance.go` `enrichReadOnlyToolDescriptions`；接入 `loop.go` 工具面过滤后 |
| P1-M1 | `BoundaryManifest` + `RenderReadOnlyBoundaryBlock`（来源/被剥离工具/出路三合一，文案唯一来源 `ReadOnlyEscalationPathText`）；`SubagentResult.ReadOnlySource` 上浮父 spawn 报告 | `backend/internal/policy/boundary.go`；`scheduler.go`（结构体字段 + 5 处填充 + `subagentReadOnlySource`）；`loop.go renderSubagentResults` |
| P1-M2 | 子代理 system prompt 只读边界横幅（写操作建议移入非只读分支，消除误导） | `prompt_builder.go BuildSubagentPrompt` |
| P2-M5 | spawn 契约检查：goal 写意图检测 → `route_warnings`（非阻断）；显式 read_only 默认来源打标 | `denial_guidance.go` `goalHasWriteIntent` / `writeIntentRouteWarning`；`loop.go decodeSubagentTasks` |
| P2-M6 | 写需求上浮：run 内连续拒绝计数（成功即清零）；达到 3 发 `subagent.requires_write` 事件；达到 5 硬停并输出"需重派 read_only=false"终态 | `loop.go`（结构体字段、run 重置、`emitToolDenied`、`resetReadOnlyDenyStreakOnSuccess`、step-loop 熔断） |

### 9.2 验证结果

- `go build ./...`：通过（exit 0）。
- `go test ./internal/agent/... ./internal/policy/... -count=1`：通过（新增 `denial_guidance_test.go` 11 项、`boundary_test.go` 2 项；既有 read_only 渲染/prompt/解码测试全部保持）。
- 全量 `go test ./...` 仅 `cmd/aicli/commands` 的 `TestBootstrapChatSession_UsesActorExecutorByDefault` 失败（"bootstrap must not create agent_control.sqlite early"）。经 `git stash` 对照实验，剔除本实施的全部改动后该测试**仍失败（STASH_TEST_EXIT=1）**，判定为工作区既有未提交改动（`cmd/aicli/commands/init.go`、`internal/toolresult/diagnostic.go` 等）引入的既有失败，与本次实施无关，待另行修复。

### 9.3 设计要点与后续跟踪

1. **文案同源**：出路文案只存在于 `policy.ReadOnlyEscalationPathText`；子代理 prompt 横幅、父代理 spawn 报告、拒绝指导三处均引用它，杜绝再次漂移。
2. **向后兼容**：非只读拒绝路径 `renderDenialGuidance` 原样透传；`tool.denied` 事件字段（`error_code/retryable/overridable`）不变；`renderSubagentResults` 既有行保留。
3. **熔断语义**：read-only 拒绝计数是"连续"而非"累计"（任一工具执行成功即清零），只读子代理完成一次只读分析后不会被历史拒绝误杀；硬停阈值 5 次优于 exploration_stall（15 次），因为写意图一旦被拒 5 次基本可断定 goal 与边界不匹配。
4. **P3-M7（事件持久化 + UI 聚合）** 未纳入本次范围：`subagent.requires_write` 事件已具备稳定 schema（`deny_streak/policy_source/advisory/escalate_after/hard_stop_after`），待会话事件持久化管线就绪后追加"按 deny_streak 聚合 + 建议操作按钮"即可。
