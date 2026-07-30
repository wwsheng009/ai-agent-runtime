# LLM 工具授权与执行策略收敛优化计划

## 1. 背景

当前 runtime 已经具备 `default`、`accept_edits`、`plan`、`bypass_permissions` 四种 permission mode，也具备静态工具策略、capability scope、sandbox、审批、hook、并行调度与子 Agent 派生策略。然而这些机制尚未形成单一、参数级、可审计的决策管道，导致两类相反风险同时存在：

1. **过度阻止**：LLM 明明应当看到并调用工具，再由参数与 permission mode 决定 `allow / ask / deny`，但工具名或粗粒度 capability 在前置阶段直接 hard deny。例如 read-only child 可看到 `shell`，却因 `exec_shell` 不在 capability scope 中无法执行 `git status`、`rg` 等只读命令。
2. **授权绕过**：hook、callback、approval 修改参数后，最终参数未必再次通过 sandbox、静态策略和 hard rule；并行和已批准调用恢复路径也没有稳定复用完整授权内核。

本计划遵循以下产品原则：

> **默认向 LLM 暴露真实可用及可审批的工具；只有显式 deny、sandbox/trust boundary、plan/read-only 不变量和资源上限才 hard deny；其他高风险操作进入统一审批管道。**

## 2. 目标与非目标

### 2.1 目标

- 统一工具定义展示与调用授权语义：hard deny 隐藏，approval-required 保留展示。
- 将 shell 风险从工具名级判断下沉到参数级判断，允许 read-only shell 命令。
- 保证每一组最终执行参数都经过 hard constraints 校验。
- callback、hook、approval 的 patched args 不可绕过 sandbox、capability、tool policy 和 hard rules。
- 并行、普通、broker、MCP、spawn 与 approved replay 使用同一授权事实源。
- Agent tool policy 与 PermissionEngine policy 始终同步。
- 通过结构化 reason code 区分显式拒绝、审批不可用、资源上限与 sandbox 拒绝。

### 2.2 非目标

- 不把所有工具无条件放行。
- 不允许 `bypass_permissions` 绕过显式 deny、sandbox、trust boundary 或资源上限。
- 不在本阶段重写整个 ToolBroker/MCP 架构。
- 不把 `parallel_support=true` 当成授权许可。
- 不以扩大默认 Agent 深度代替工具面与执行策略一致性修复。

## 3. 当前事实与问题分级

### 3.1 permission mode 的合理基础

`backend/internal/policy/modes.go` 当前语义总体正确：

- `default`：write/shell/network/external/background 为 `Ask`；
- `accept_edits`：普通写入为 `Allow`，shell/network/external/background 为 `Ask`；
- `plan`：read-only/ask-user 为 `Allow`，非允许计划路径写入为 `Deny`；
- `bypass_permissions`：跳过审批，但 hard constraints 仍应生效。

问题在于 `backend/internal/policy/engine.go` 会先执行静态 tool/capability policy；粗粒度 scope 可能在 mode 和只读命令分类前结束调用。

### 3.2 Critical：patched args 未统一复验

涉及：

- `backend/internal/policy/engine.go`
- `backend/internal/agent/loop.go`
- `backend/internal/agent/approved_tool.go`

风险：

- permission hook 的 `modify` 直接返回 allow，跳过后续 policy/rules/mode；
- callback 或 approval 修改参数后，执行侧直接替换并运行；
- approved replay 在恢复后不应重复弹审批，但必须验证最终参数的 hard constraints；
- 非法 patched JSON 不应静默降级为 `{}`。

### 3.3 Critical：并行路径授权不完整

`backend/internal/agent/tool_parallel_scheduler.go` 的并行资格与执行不能仅依赖 `parallel_support`、只读启发式或 `AllowToolCall`。每个实际并行调用仍需经过统一 evaluator；网络和外部副作用能力不得绕开 approval。

### 3.4 High：read-only child 过早禁止 shell

`backend/internal/policy/capability_scope.go` 的 `ReadOnlyChildCapabilities()` 不包含 `CapExecShell`，`CapabilitiesForTask()` 在 read-only 时也删除它。这会阻止已有的 shell read-only classifier 生效。

目标语义：

| 场景 | 预期 |
|---|---|
| read-only + `shell git status` | Allow |
| read-only + `shell rg ...` | Allow |
| read-only + `shell go test` | 按产品定义 Ask 或 Deny，不得误判为纯只读 |
| read-only + `shell rm ...` | Hard deny |
| default + 未知/修改性 shell | Ask |
| plan/read-only sandbox + 修改性 shell | Hard deny |

### 3.5 High：工具面与执行策略不一致

模型工具面存在多条构建路径。确定 hard denied 的工具有时仍展示，LLM 调用后才获得 `retryable=false`；而需要审批但真实可用的工具又可能被过早隐藏。

目标拆分两个概念：

```go
EligibleForSurface(definition) bool
EvaluateCall(definition, args) AllowAskDeny
```

- `EligibleForSurface` 只过滤宿主未提供、显式 deny、sandbox/trust/resource boundary 确定不可用的工具；
- `EvaluateCall` 使用最终 args 判断 allow/ask/deny；
- approval-required 工具必须保留在模型工具面。

### 3.6 High：policy 双事实源可能失步

`Agent.SetToolExecutionPolicy` 与 `Agent.SetPermissionEngine` 当前可令 `Agent.toolPolicy` 和 `PermissionEngine.Policy` 指向不同对象。工具面和执行路径因此可能应用不同策略。

## 4. 目标决策管道

```text
1. Resolve tool/provider
2. Resolve effective policy
3. Surface eligibility (definition-level hard boundary only)
4. Bind/normalize arguments
5. PreToolUse hook; apply patch
6. Resolve call-level capabilities from final candidate args
7. Validate hard constraints
   - explicit deny
   - capability scope
   - sandbox/path/network/trust boundary
   - resource/depth boundary
   - hard rules
8. Permission decision
   - grants
   - read-only auto classification
   - permission mode
   - callback
9. Ask approval if required
10. Apply approval/callback patch
11. Revalidate hard constraints only (no second approval)
12. Execute
13. Audit decision stage/reason/effective policy
```

关键不变量：

1. **最终执行参数必经 hard validation。**
2. **patched args 不重复触发审批，但必须复验 hard boundary。**
3. **定义级不可用与调用级高风险分离。**
4. **并行能力只影响调度，不影响授权。**
5. **只有一个 effective policy 事实源。**

## 5. 分阶段实施

### Phase 1：单一 policy 与 hard-only 复验

1. 在 policy engine 中提取 `ValidateHardConstraints(ctx, req)`：
   - 处理 permission hook block/modify；
   - 对最终候选参数重新解析 capability；
   - 运行 static tool/capability policy、sandbox/tool-call 检查及 hard deny rules；
   - 不运行 grants、mode、callback、AskHandler；
   - hook modify 不得递归无限重写。
2. 正常 `Evaluate` 在 hook/callback/approval patch 后调用 hard-only 复验。
3. approval request 展示实际候选参数；审批未 patch 时保留先前 patch。
4. `ExecuteApprovedToolCall` 在 PreToolUse 最终 patch 后调用 hard-only 复验，禁止重新进入 Ask。
5. `SetToolExecutionPolicy` 同步现有 engine；`SetPermissionEngine` 明确采用并绑定同一 policy。
6. malformed patched JSON fail closed。

### Phase 2：read-only shell 参数级判定

1. read-only capability surface 保留 `CapExecShell`，但不代表自动允许任意命令。
2. 在 `AllowToolCall`/sandbox 与 engine readonly-auto 中统一 shell classifier。
3. 明确只读命令 allowlist 和组合命令处理规则；重定向、管道、子 shell、命令替换必须保守解析。
4. 修改性命令在 read-only/plan sandbox 下 hard deny；default 下进入 Ask。
5. `go test` 等可能构建、写缓存或执行项目代码的命令不可归类为纯只读。

### Phase 3：模型工具面收敛

1. 建立统一 surface filtering helper，所有 toolkit/MCP/broker/spawn 定义均使用。
2. hard denied/resource unavailable 工具不展示。
3. approval-required 工具继续展示。
4. 达到 `MaxDepth` 时隐藏 spawn 工具，并输出结构化诊断：`current_depth`、`max_depth`、`reason=max_agent_depth_reached`。
5. 迁移要求“确定拒绝的 write 仍展示”的旧测试契约。

### Phase 4：并行授权收敛

1. 每个并行候选在调度前执行 evaluator；只有 `DecisionAllow` 才进入并行批次。
2. `DecisionAsk` 不得在 worker 中弹多次并发审批；先在主调度线程完成审批，再创建 worker。
3. patched args 在并行计划中固化为最终 call，并完成 hard-only 复验。
4. 网络、external side effect、write、shell 不因 `parallel_support` 自动获得许可。
5. worker 不修改共享 MessageBuilder；结果仍按原 tool call 次序写回。

### Phase 5：可观测性与错误语义

引入稳定 reason code，至少覆盖：

- `explicit_policy_deny`
- `capability_unavailable`
- `approval_required`
- `approval_unavailable`
- `approval_denied`
- `sandbox_boundary_deny`
- `untrusted_remote_deny`
- `max_agent_depth_reached`
- `resource_limit_reached`
- `patched_args_invalid`

日志与 runtime event 应携带：tool、stage、reason_code、permission_mode、policy source、trace/session/tool_call_id；不得记录敏感参数全文。

## 6. 测试矩阵

### 6.1 Policy engine

- hook modify 后仍被 sandbox 拒绝；
- callback patch 后 hard policy 重新验证；
- approval patch 后 hard policy 重新验证；
- approval 无 patch 时保留此前 callback/hook patch；
- hard-only 复验不再次调用 AskHandler；
- bypass mode 不能绕过 explicit deny/hard rules；
- malformed patch fail closed。

### 6.2 Read-only shell

- `git status`、`git log`、`rg`、`ls`、`pwd` allow；
- `rm`、`git reset --hard`、重定向写文件 deny；
- `go test` 按选定产品语义稳定测试；
- pipe/chain 中任一命令具有修改性则不允许 readonly-auto；
- Windows PowerShell 与 Unix shell 解析均覆盖关键样例。

### 6.3 Tool surface

- explicit deny 的 toolkit/MCP/broker/spawn 工具不展示；
- default mode 中需要 approval 的 write/shell 工具仍展示；
- max depth 时 spawn 工具不展示；
- policy 运行时替换后，surface 与 engine 使用同一对象。

### 6.4 Parallel execution

- parallel network/external-side-effect 调用走 evaluator；
- Ask 在主线程完成后才调度；
- Deny 调用不启动 worker；
- patched args 经复验后才执行；
- 所有 tool result 按原 call ID/顺序写回。

### 6.5 Approved replay

- replay 不重复审批；
- approval patch 与 PreToolUse patch 都经过 hard-only 复验；
- sandbox escape 被拒绝；
- 无 engine 时仍执行静态 policy fallback；
- 非法 JSON 不执行工具。

## 7. 兼容性与迁移策略

- 第一阶段保留现有 public API，通过内部 helper 收敛；成熟后再删除重复判断。
- 旧的 capability 测试从“read-only 一律禁止 shell”迁移为参数级矩阵。
- 旧的工具面测试从“被拒绝工具仍展示”迁移为“hard deny 隐藏、Ask 展示”。
- 对 trusted remote write 是否默认 Ask 单独增加配置迁移说明；未明确产品决定前不放宽 untrusted boundary。
- `MaxDepth` 默认值是否由 1 调整为 2 独立决策；无论默认值为何，达到上限都必须隐藏工具并给出明确 reason。

## 8. 验收标准

- read-only child 可实际调用 `shell git status` 与 `shell rg ...`。
- read-only child 无法通过 shell、patch、approval 或 hook 修改 workspace。
- default mode 下 write/shell 等高风险工具向 LLM 可见，并能进入审批。
- plan mode 只允许只读调用及显式计划文件写入。
- callback/hook/approval patched args 无法逃逸 sandbox/trust/hard rules。
- 并行调用不能绕过 evaluator。
- approved replay 不重复审批，也不能绕过 hard validation。
- Agent surface、PermissionEngine 与实际执行使用同一 effective policy。
- 针对 policy 与 agent 包的单元/回归测试全部通过，`go test ./...` 无新增失败。

## 9. 推荐落地顺序

1. 先修 policy 同步和 patched args hard-only 复验；
2. 再修 approved replay；
3. 再开放 read-only shell 的参数级能力；
4. 收敛 tool surface；
5. 收敛 parallel evaluator；
6. 最后补结构化错误码与全量迁移测试。

此顺序避免在授权绕过尚未封堵时扩大工具暴露范围。

## 10. 收敛完成状态

**状态：已完成 ✅**（2026-07-30）

落地结果：

- 工具授权默认暴露真实可用工具；显式 deny / plan / hard safety 仍 hard deny
- read-only shell 参数级校验：`git status`/`rg`/`ls` 允许，`rm` 等修改性命令拒绝
- `spawn_subagents`/`spawn_team` 作为 runtime-owned essential：执行可 bypass 非空 allowlist；模型可见面仍需显式 allowlist 才展示
- patched args（hook/callback/approval）后 hard revalidation 已统一
- 相关回归测试通过：
  - `go test ./internal/policy/...`
  - `go test ./internal/agent/...`
  - `go test ./cmd/aicli/commands -run 'TestApplyLocalChildReadOnlyPolicyOverridesBypassPermissions|TestBuildLocalChatToolPolicy|TestToolExecutionPolicy|TestCapability|TestSpawnSubagents'`

关键实现位置：

- `backend/internal/policy/tool_policy.go` — essentials + AllowTool/AllowToolCall
- `backend/internal/policy/engine.go` — patched-args hard revalidation
- `backend/internal/policy/capability_scope.go` — read-only child 保留 CapExecShell
- `backend/internal/agent/agent.go` — shouldExposeSpawnSubagents 可见面严格门控
- `backend/internal/agent/tool_parallel_scheduler.go` — 并行路径复验
- `backend/cmd/aicli/commands/chat_actor_host.go` — buildLocalChatToolPolicy 不 widen 显式 allowlist