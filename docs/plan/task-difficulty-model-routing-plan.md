# 任务难度评级驱动的子 Agent 模型/Provider 调度设计

更新时间: 2026-06-21

审查更新: 本文末尾新增 `18. 完整性审查与修订意见` 到 `24. 最终修订结论`。这些章节对初版方案做了代码事实核对、困难点分析和实施路线修正；若与前文存在冲突，以第 18-24 节为准。

## 1. 背景

当前 `ai-agent-runtime` 已经具备以下基础能力：

- `aicli.chat` 已支持默认 `provider`、`model`、`reasoning_effort` 和 `stream` 偏好。
- `Agent.Config` 已包含 `Provider`、`Model`、`DefaultMaxTokens`、`SystemPrompt`、`Options` 等字段。
- `SubagentTask` 已支持 `role`、`goal`、`tools_whitelist`、`depends_on`、`model`、`budget_tokens`、`timeout`、`read_only`。
- `spawn_subagents` 工具已经允许主 agent 派发多个子任务。
- `SubagentScheduler.runChild()` 当前会从 parent `Agent.Config` 复制 child 配置，并可按 `task.Model` 覆盖 `childConfig.Model`。

现有缺口：

1. 子任务没有统一的“难度/复杂度”评级字段。
2. 系统提示词没有强制要求模型在拆分任务时给每个子任务评级。
3. `spawn_subagents` 当前只支持按 task 显式传 `model`，不支持按难度自动匹配 `provider/model/thinking_effort`。
4. 本地配置中没有“难度 -> provider/model/reasoning_effort”的路由表。
5. Scheduler 当前复用 parent 的 `llmRuntime`，即使子任务指定不同 provider，也缺少重新构造 child LLM runtime / adapter 的路径。

因此，需要新增一套“任务难度评级 + 本地模型路由 + 子 agent 调度匹配”的设计，使系统可以做到：

- 主 agent 在系统提示词中被要求评估任务难度。
- 如需拆分多个子任务，每个子任务都明确难易程度和理由。
- 派发子 agent 时，根据难度自动选择本地配置的 provider、model、thinking/reasoning effort。
- 允许用户在本地配置中按难度自定义调度策略。
- 保留显式 override，但默认走安全、可观测、可回退的本地路由。

## 2. 目标

### 2.1 功能目标

1. 增加任务难度评级规范。
   - 主任务需要给出整体难度。
   - 多子任务需要分别给出每个子任务的难度。
   - 难度评级必须能被机器解析。

2. 增加本地模型路由配置。
   - 支持按难度配置 `provider`、`model`、`reasoning_effort` / `thinking_effort`。
   - 支持按 `role` 对难度路由做细化覆盖，例如 `researcher`、`writer`、`verifier`。
   - 支持默认 fallback。

3. 扩展子 agent 调度。
   - `SubagentTask` 增加 `difficulty`、`difficulty_rationale`、`provider`、`reasoning_effort` 等字段。
   - `spawn_subagents` 工具 schema 暴露这些字段。
   - Scheduler 在运行 child 前解析最终 routing profile。
   - 当 provider 不同于 parent 时，构造匹配 provider 的 child LLM runtime。

4. 增加治理与可观测性。
   - runtime event / hook payload 中带上 difficulty 和 routing 结果。
   - debug 输出能够解释某个子任务为什么使用某个 provider/model。
   - 配置不可用时有清晰 fallback 或错误。

### 2.2 非目标

本设计不做以下事项：

- 不要求主 agent 每个普通问题都必须调用子 agent。
- 不把“难度评级”作为权限绕过机制。
- 不允许子任务通过提示词自行提升到未经配置允许的 provider/model。
- 不改变已有 `/model` 当前会话主模型切换语义。
- 不在第一阶段引入远程分布式调度或云端队列。
- 不强制所有 provider 都支持 reasoning/thinking；不支持时按 capability 校验和 fallback 处理。

## 3. 难度评级模型

建议采用 4 档稳定枚举，避免过细导致模型输出不稳定：

| 难度 | 语义 | 典型任务 | 推荐策略 |
|---|---|---|---|
| `easy` | 低复杂度、低风险、上下文局部 | 单文件阅读、小范围解释、简单格式转换、轻量查询 | 低成本模型，低/无 reasoning |
| `normal` | 常规开发或分析任务 | 多文件局部修改、常规 bug 修复、普通文档设计 | 默认模型，中等 reasoning |
| `hard` | 高复杂度、多步骤、需要验证 | 跨模块改造、需要测试闭环、复杂排障、架构分析 | 强模型，高 reasoning，建议拆子任务 |
| `expert` | 高风险或高不确定性 | 权限/安全边界、协议迁移、跨 provider 行为、数据一致性、长链路重构 | 最强模型，高 reasoning，强制 verifier 或 coordinator-like 拆分 |

别名归一化建议：

| 输入别名 | 归一化 |
|---|---|
| `simple`, `low`, `trivial` | `easy` |
| `medium`, `standard`, `default` | `normal` |
| `complex`, `high`, `difficult` | `hard` |
| `critical`, `very_hard`, `architectural` | `expert` |

## 4. 系统提示词设计

### 4.1 主 agent 系统提示词新增片段

建议在主系统提示词或 tool guidance 中新增以下约束：

```text
Task difficulty rating and delegation policy:

Before decomposing or delegating work, rate the overall user request difficulty as one of: easy, normal, hard, expert.

If the work requires multiple subtasks, assign each subtask its own difficulty rating and a short rationale. Use the difficulty to decide whether a subagent is needed and what kind of child agent should run it.

Use easy for local, low-risk, single-step work. Use normal for regular multi-file or multi-step work. Use hard for complex implementation, broad investigation, or tasks requiring test verification. Use expert for high-risk architecture, security, permission, provider/protocol, migration, or cross-system consistency work.

Do not spawn subagents for easy work unless explicitly requested or clearly beneficial. Prefer one or more subagents for hard/expert work when subtasks can be isolated. When spawning subagents, include difficulty and difficulty_rationale for every subtask. Do not invent provider/model names; leave provider/model empty unless the user explicitly asked for a specific override. The runtime will map difficulty to the local configured provider/model.

For multiple subtasks, return or call tools with this structure:
- id
- role: researcher | writer | verifier | custom
- goal
- difficulty: easy | normal | hard | expert
- difficulty_rationale
- depends_on
- read_only
- tools_whitelist when needed
```

### 4.2 子 agent 系统提示词新增片段

`PromptBuilder.BuildSubagentPrompt()` 应在 child prompt 中加入路由结果，帮助子 agent 理解预算和期望：

```text
Subtask difficulty: hard.
Difficulty rationale: Cross-module code path with verification requirement.
Runtime routing: provider=<provider>, model=<model>, reasoning_effort=<effort>, source=<difficulty_route|role_override|explicit_override|fallback>.
Stay within the assigned difficulty scope. Do not request a stronger model yourself; report if the assigned model seems insufficient.
```

注意：provider/model 信息可用于可观测性，但不要让子 agent 把它当作权限来源。真正的权限仍由 runtime policy 控制。

## 5. 配置设计

### 5.1 YAML 配置位置

建议在现有 `aicli` 配置下新增 `subagents.routing`，避免污染主会话 `/model` 偏好：

```yaml
aicli:
  chat:
    default_provider: nvidia
    default_model: gpt-5.4
    reasoning_effort: medium

  subagents:
    routing:
      enabled: true
      default_difficulty: normal
      allow_explicit_provider_override: false
      allow_explicit_model_override: true
      inherit_parent_when_missing: true
      validate_model_capabilities: true
      max_expert_concurrency: 1

      levels:
        easy:
          provider: local_fast
          model: gpt-5.4-mini
          reasoning_effort: low
          max_tokens: 4096
          timeout: 120s

        normal:
          provider: nvidia
          model: gpt-5.4
          reasoning_effort: medium
          max_tokens: 8192
          timeout: 300s

        hard:
          provider: anthropic_local
          model: claude-sonnet-4-6
          reasoning_effort: high
          max_tokens: 16000
          timeout: 600s

        expert:
          provider: anthropic_local
          model: claude-opus-4-6
          reasoning_effort: high
          max_tokens: 24000
          timeout: 900s

      roles:
        researcher:
          easy:
            provider: local_fast
            model: gpt-5.4-mini
            reasoning_effort: low
          hard:
            provider: anthropic_local
            model: claude-sonnet-4-6
            reasoning_effort: high

        writer:
          normal:
            provider: nvidia
            model: gpt-5.4
            reasoning_effort: medium
          hard:
            provider: anthropic_local
            model: claude-sonnet-4-6
            reasoning_effort: high

        verifier:
          normal:
            provider: local_fast
            model: gpt-5.4-mini
            reasoning_effort: low
          hard:
            provider: nvidia
            model: gpt-5.4
            reasoning_effort: medium
```

### 5.2 Go 配置结构建议

在 `backend/internal/agentconfig/config.go` 中扩展：

```go
type AICLIConfig struct {
    MCP        *AICLIMCPConfig        `yaml:"mcp" mapstructure:"mcp"`
    Log        *AICLILogConfig        `yaml:"log" mapstructure:"log"`
    Retry      *AICLIRetryConfig      `yaml:"retry" mapstructure:"retry"`
    Timeout    *AICLITimeoutConfig    `yaml:"timeout" mapstructure:"timeout"`
    Theme      *AICLIThemeConfig      `yaml:"theme" mapstructure:"theme"`
    Chat       *AICLIChatConfig       `yaml:"chat" mapstructure:"chat"`
    Runtime    *AICLIRuntimeConfig    `yaml:"runtime" mapstructure:"runtime"`
    ModelCards *AICLIModelCardsConfig `yaml:"model_cards" mapstructure:"model_cards"`
    Subagents  *AICLISubagentsConfig  `yaml:"subagents" mapstructure:"subagents"`
}

type AICLISubagentsConfig struct {
    Routing *AICLISubagentRoutingConfig `yaml:"routing" mapstructure:"routing"`
}

type AICLISubagentRoutingConfig struct {
    Enabled                       *bool                                      `yaml:"enabled" mapstructure:"enabled"`
    DefaultDifficulty             string                                     `yaml:"default_difficulty" mapstructure:"default_difficulty"`
    AllowExplicitProviderOverride bool                                       `yaml:"allow_explicit_provider_override" mapstructure:"allow_explicit_provider_override"`
    AllowExplicitModelOverride    bool                                       `yaml:"allow_explicit_model_override" mapstructure:"allow_explicit_model_override"`
    InheritParentWhenMissing      *bool                                      `yaml:"inherit_parent_when_missing" mapstructure:"inherit_parent_when_missing"`
    ValidateModelCapabilities     *bool                                      `yaml:"validate_model_capabilities" mapstructure:"validate_model_capabilities"`
    MaxExpertConcurrency          int                                        `yaml:"max_expert_concurrency" mapstructure:"max_expert_concurrency"`
    Levels                        map[string]AICLISubagentRouteProfile       `yaml:"levels" mapstructure:"levels"`
    Roles                         map[string]map[string]AICLISubagentRouteProfile `yaml:"roles" mapstructure:"roles"`
}

type AICLISubagentRouteProfile struct {
    Provider        string        `yaml:"provider,omitempty" mapstructure:"provider"`
    Model           string        `yaml:"model,omitempty" mapstructure:"model"`
    ReasoningEffort string        `yaml:"reasoning_effort,omitempty" mapstructure:"reasoning_effort"`
    ThinkingEffort  string        `yaml:"thinking_effort,omitempty" mapstructure:"thinking_effort"`
    MaxTokens       int           `yaml:"max_tokens,omitempty" mapstructure:"max_tokens"`
    Timeout         time.Duration `yaml:"timeout,omitempty" mapstructure:"timeout"`
    Temperature     *float64      `yaml:"temperature,omitempty" mapstructure:"temperature"`
}
```

说明：

- 内部仍建议统一使用 `reasoning_effort`，`thinking_effort` 可作为配置别名解析到同一字段。
- `Enabled *bool` 用于区分未配置和显式关闭。
- `inherit_parent_when_missing` 默认 true，避免缺少某档配置导致无法运行。
- `allow_explicit_provider_override` 默认 false，防止模型通过工具参数自行切换到未经本地配置允许的 provider。
- `allow_explicit_model_override` 可默认 true，仅允许在最终 provider 内覆盖 model；也可按安全策略改为 false。

## 6. 子任务结构扩展

### 6.1 `SubagentTask` 扩展

当前 `SubagentTask` 已有 `Model`，建议新增：

```go
type SubagentTask struct {
    ID                  string      `json:"id,omitempty" yaml:"id,omitempty"`
    Role                string      `json:"role,omitempty" yaml:"role,omitempty"`
    Goal                string      `json:"goal" yaml:"goal"`
    Difficulty          string      `json:"difficulty,omitempty" yaml:"difficulty,omitempty"`
    DifficultyRationale string      `json:"difficulty_rationale,omitempty" yaml:"difficulty_rationale,omitempty"`
    Provider            string      `json:"provider,omitempty" yaml:"provider,omitempty"`
    Model               string      `json:"model,omitempty" yaml:"model,omitempty"`
    ReasoningEffort     string      `json:"reasoning_effort,omitempty" yaml:"reasoning_effort,omitempty"`
    ToolsWhitelist      []string    `json:"tools_whitelist,omitempty" yaml:"tools_whitelist,omitempty"`
    DependsOn           []string    `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
    PatchContext        []FilePatch `json:"patches,omitempty" yaml:"patches,omitempty"`
    BudgetTokens        int         `json:"budget_tokens,omitempty" yaml:"budget_tokens,omitempty"`
    TimeoutSec          int         `json:"timeout,omitempty" yaml:"timeout,omitempty"`
    ReadOnly            bool        `json:"read_only,omitempty" yaml:"read_only,omitempty"`
}
```

### 6.2 `spawn_subagents` schema 扩展

`spawnSubagentsToolDefinition()` 的 `agents.items.properties` 中新增：

```go
"difficulty": map[string]interface{}{
    "type": "string",
    "enum": []string{"easy", "normal", "hard", "expert"},
},
"difficulty_rationale": map[string]interface{}{"type": "string"},
"provider": map[string]interface{}{"type": "string"},
"reasoning_effort": map[string]interface{}{"type": "string"},
```

工具描述更新为：

```text
Spawn isolated subagents for parallel subtasks. Use only when tasks are independent or when hard/expert work benefits from isolated research, writing, or verification. For every child task, include difficulty and difficulty_rationale. Leave provider/model empty unless explicitly requested; runtime routing maps difficulty to local provider/model configuration.
```

## 7. 路由解析设计

### 7.1 路由结果结构

新增内部结构：

```go
type SubagentRouteDecision struct {
    Difficulty          string
    DifficultyRationale string
    Provider            string
    Model               string
    ReasoningEffort     string
    MaxTokens           int
    Timeout             time.Duration
    Temperature         *float64
    Source              string // explicit_override | role_override | difficulty_level | parent_inherit | provider_default
    Warnings            []string
}
```

### 7.2 解析优先级

最终 child provider/model 的优先级：

1. **显式用户/工具 override**
   - `task.Provider`：仅当 `allow_explicit_provider_override=true` 时允许。
   - `task.Model`：仅当 `allow_explicit_model_override=true` 时允许。
   - `task.ReasoningEffort`：需要 capability 校验。

2. **role + difficulty 覆盖**
   - `aicli.subagents.routing.roles.<role>.<difficulty>`。

3. **difficulty 默认配置**
   - `aicli.subagents.routing.levels.<difficulty>`。

4. **parent 继承**
   - 若缺少 provider/model 且 `inherit_parent_when_missing=true`，继承 parent session provider/model/reasoning。

5. **provider 默认值**
   - 若指定了 provider 但 model 为空，使用该 provider 的 `default_model`。

6. **报错或保守 fallback**
   - 交互模式可提示配置缺失。
   - 非交互模式建议报错或回退 parent，并记录 warning。

### 7.3 难度缺失 fallback

如果模型没有给 `difficulty`：

1. 使用 `routing.default_difficulty`，默认 `normal`。
2. 按 role 做轻量启发：
   - `verifier` 默认不低于 `normal`。
   - `writer` 且非 read-only 默认不低于 `normal`。
   - 目标包含 `security`、`permission`、`migration`、`architecture`、`provider`、`protocol` 等关键词可提升到 `hard` 或 `expert`。
3. 记录 warning：`difficulty_missing_defaulted`。

## 8. Provider / LLM Runtime 调度设计

### 8.1 当前问题

当前 `SubagentScheduler.runChild()` 大致流程是：

1. `childConfig := *s.parent.GetConfig()`。
2. 如果 `task.Model != ""`，覆盖 `childConfig.Model`。
3. `childAgent := NewAgentWithLLM(&childConfig, s.parent.mcpManager, s.parent.llmRuntime)`。

这意味着 child 即使指定 `Model`，仍然复用 parent 的 `llmRuntime`。如果目标是“根据难度调用本地不同 provider/model”，必须补齐 child LLM runtime 构造能力。

### 8.2 新增 ProviderResolver / RuntimeFactory

建议在 agent 或 chat runtime host 层引入接口：

```go
type ChildRuntimeFactory interface {
    BuildChildRuntime(ctx context.Context, route SubagentRouteDecision) (*llm.LLMRuntime, adapter.ProtocolAdapter, config.Provider, error)
}
```

或者更轻量地放在 `SubagentScheduler` 配置中：

```go
type SubagentSchedulerConfig struct {
    MaxConcurrent       int
    MaxDepth            int
    EnforceSingleWriter bool
    RouteResolver       SubagentRouteResolver
    RuntimeFactory      ChildRuntimeFactory
}
```

当 route provider 等于 parent provider 且 model 只做同 provider 切换时，可以复用 parent runtime，前提是 runtime 请求层确实按 `childConfig.Model` 传入。若 provider 不同，则必须创建新的 adapter / HTTP client / baseURL / auth 配置。

### 8.3 Child 配置应用

`runChild()` 应改为：

```go
route := s.routeResolver.Resolve(parentConfig, task)
childConfig := *s.parent.GetConfig()
childConfig.Provider = route.Provider
childConfig.Model = route.Model
childConfig.DefaultMaxTokens = firstPositive(task.BudgetTokens, route.MaxTokens, childConfig.DefaultMaxTokens)
childConfig.Options["reasoning_effort"] = route.ReasoningEffort
childConfig.Options["difficulty"] = route.Difficulty
childConfig.Options["routing_source"] = route.Source

childRuntime := s.parent.llmRuntime
if s.runtimeFactory != nil && route.Provider != "" && route.Provider != parentConfig.Provider {
    childRuntime = s.runtimeFactory.BuildChildRuntime(ctx, route)
}

childAgent := NewAgentWithLLM(&childConfig, s.parent.mcpManager, childRuntime)
```

同时在 `Agent.BuildRequest` / `loop.run` 路径中确保：

- `Agent.Config.Provider` 能传到 request metadata。
- `Agent.Config.Options["reasoning_effort"]` 或 `SubagentTask.ReasoningEffort` 能进入 `types.Request.ReasoningEffort`。
- provider 不同时使用对应 adapter 构造请求。

## 9. 任务拆分与 Planner 集成

### 9.1 PlanStep 扩展

如果 planner 已经生成 `Plan` / `PlanStep`，建议增加：

```go
type PlanStep struct {
    ID                  string
    Description         string
    Tool                string
    DependsOn           []string
    Difficulty          string
    DifficultyRationale string
}
```

`BuildSubagentTasksFromPlan()` 将 difficulty 从 step 复制到 `SubagentTask`。

### 9.2 自动 verifier 策略

现有逻辑已经会在 writer 后自动补 verifier。建议加强：

- `hard` / `expert` writer 必须有 verifier。
- `expert` verifier 难度至少为 `hard`，默认可低于 writer 一档但不能低于 `normal`。
- `expert` 任务如果涉及写操作，默认限制 `MaxExpertConcurrency=1`，避免多个高风险 writer 并发。

## 10. 用户可见入口

### 10.1 配置查看命令

后续可新增 `/agents routing` 或 `/model routing`：

```text
/agents routing
/agents routing easy
/agents routing set hard anthropic_local claude-sonnet-4-6 high
/agents routing test --role writer --difficulty hard
```

MVP 可以先只做 debug 输出，不做交互式写回。

### 10.2 Debug 输出

`/debug` 或 `/agents panel` 中增加：

```text
Subagent routing:
  enabled: true
  default_difficulty: normal
  easy:   local_fast / gpt-5.4-mini / low
  normal: nvidia / gpt-5.4 / medium
  hard:   anthropic_local / claude-sonnet-4-6 / high
  expert: anthropic_local / claude-opus-4-6 / high
```

子任务详情中显示：

```text
Task researcher_api_scan
  difficulty: hard
  route: role_override
  provider: anthropic_local
  model: claude-sonnet-4-6
  reasoning_effort: high
```

## 11. 事件与审计

### 11.1 Runtime event payload

`subagent.started` 增加字段：

```json
{
  "subagent_id": "researcher_api_scan",
  "role": "researcher",
  "difficulty": "hard",
  "difficulty_rationale": "Cross-module API and config analysis",
  "routing_source": "role_override",
  "provider": "anthropic_local",
  "model": "claude-sonnet-4-6",
  "reasoning_effort": "high",
  "budget_tokens": 16000
}
```

`subagent.completed` 增加 usage 统计维度：

```json
{
  "difficulty": "hard",
  "provider": "anthropic_local",
  "model": "claude-sonnet-4-6",
  "usage_total_tokens": 12345
}
```

### 11.2 Hook payload

`EventSubagentStart` / `EventSubagentStop` 同步加入 route 字段，便于外部审计和成本统计。

## 12. 安全与治理

1. 子任务不能通过 prompt 自行选择未启用 provider。
2. `task.Provider` 默认不生效，除非配置显式允许。
3. `task.Model` 需要在当前 provider 下通过 capability / allowlist 校验；无法校验时至少记录 warning。
4. reasoning/thinking effort 必须经过 provider/model capability 校验：
   - 支持该 effort：直接使用。
   - 不支持：降级到该模型支持的最高安全值或清空。
   - 显式 override 不支持：报错。
5. `expert` 任务默认限制并发，尤其是 writer。
6. read-only 子任务即使用强模型，也不能获得写工具。
7. provider/model 路由不改变 tool permission policy。
8. 路由结果需要可审计，避免“为什么这个子 agent 用了贵模型”不可解释。

## 13. 实施阶段

### P0. 文档与提示词

- 增加本设计文档。
- 更新主系统提示词，要求任务难度评级。
- 更新 `PromptBuilder.BuildSubagentPrompt()`，让子 agent 看到 difficulty / routing context。
- 更新 `spawn_subagents` 工具描述。

验收：

- 单元测试覆盖 prompt 中包含难度评级要求。
- 子 agent prompt 包含 difficulty 和 routing source。

### P1. 配置 schema 与路由解析器

- 扩展 `AICLIConfig`，加入 `Subagents.Routing`。
- 增加 `NormalizeTaskDifficulty()`。
- 增加 `SubagentRouteResolver`。
- 增加 provider/model/reasoning capability 校验。

验收：

- 配置解析测试覆盖 levels、roles、fallback。
- 缺失 difficulty 会 fallback 到 default。
- role override 优先于 level default。

### P2. 子任务结构和工具 schema

- 扩展 `SubagentTask`。
- 扩展 `spawn_subagents` schema。
- 扩展 tool argument decode。
- `BuildSubagentTasksFromPlan()` 复制 difficulty。

验收：

- `spawn_subagents` 支持传入 difficulty。
- 旧 payload 不带 difficulty 仍兼容。

### P3. Scheduler 路由应用

- `SubagentSchedulerConfig` 增加 route resolver / runtime factory。
- `runChild()` 在创建 child 前解析 route。
- 同 provider model override 继续兼容。
- 不同 provider 时构造 child runtime。
- event/hook payload 加入 route 字段。

验收：

- easy 子任务路由到 fast model。
- hard 子任务路由到强模型。
- provider 不同于 parent 时不复用错误 adapter。
- route 字段出现在 `subagent.started` 和 `subagent.completed`。

### P4. Debug 与命令入口

- `/debug` 展示 subagent routing summary。
- 可选新增 `/agents routing test`。
- 增加文档说明配置示例。

验收：

- 用户能在不读配置文件的情况下看到当前 difficulty routing。
- routing test 能解释最终 provider/model 来源。

## 14. 测试计划

建议新增或扩展测试：

1. `agentconfig` 配置解析测试。
2. difficulty normalize 测试。
3. route resolver 优先级测试。
4. provider/model capability 校验测试。
5. `spawn_subagents` schema 快照测试。
6. tool argument decode 兼容测试。
7. `PromptBuilder` 子 prompt 测试。
8. `BuildSubagentTasksFromPlan` difficulty 传递测试。
9. `SubagentScheduler.runChild` route 应用测试。
10. event payload 测试。
11. 不同 provider child runtime factory 调用测试。
12. provider 缺失 / model 不支持 / reasoning 不支持的 fallback 测试。

推荐最小测试命令：

```powershell
go test ./internal/agent ./internal/agentconfig ./cmd/aicli/commands -count=1
```

如果 child runtime factory 落在 chat runtime host，还需要：

```powershell
go test ./cmd/aicli/commands -run "Subagent|Routing|Model|Reasoning" -count=1
```

## 15. 示例流程

用户输入：

```text
分析当前多 agent 调度链路，设计并实现根据任务难度切换 provider/model 的能力，补充测试。
```

主 agent 评级：

```json
{
  "overall_difficulty": "hard",
  "rationale": "Requires config schema, prompt, scheduler, provider runtime, and tests across multiple modules."
}
```

拆分子任务：

```json
{
  "agents": [
    {
      "id": "research_config",
      "role": "researcher",
      "goal": "Inspect current config, provider, and subagent scheduler code paths and summarize required change points.",
      "difficulty": "normal",
      "difficulty_rationale": "Read-only multi-file code inspection.",
      "read_only": true
    },
    {
      "id": "implement_router",
      "role": "writer",
      "goal": "Implement config schema and subagent route resolver with tests.",
      "difficulty": "hard",
      "difficulty_rationale": "Cross-module implementation touching config and scheduler behavior.",
      "depends_on": ["research_config"],
      "read_only": false
    },
    {
      "id": "verify_routing",
      "role": "verifier",
      "goal": "Run focused tests and verify hard/easy task routing behavior.",
      "difficulty": "normal",
      "difficulty_rationale": "Verification after implementation; needs focused tests but no new architecture.",
      "depends_on": ["implement_router"],
      "read_only": true
    }
  ]
}
```

Runtime 路由：

| 子任务 | difficulty | role | provider/model |
|---|---|---|---|
| `research_config` | `normal` | `researcher` | `nvidia / gpt-5.4 / medium` |
| `implement_router` | `hard` | `writer` | `anthropic_local / claude-sonnet-4-6 / high` |
| `verify_routing` | `normal` | `verifier` | `local_fast / gpt-5.4-mini / low` |

## 16. 风险与取舍

| 风险 | 影响 | 缓解 |
|---|---|---|
| 模型误评级 | 简单任务用贵模型或复杂任务用弱模型 | default fallback + debug + user override + route test |
| provider 不可用 | 子任务失败 | 启动时校验 enabled provider，运行时 fallback parent 或报错 |
| 不同 provider adapter 复用错误 | 请求格式错误 | 引入 child runtime factory，provider 不同时强制重建 adapter |
| reasoning_effort 不兼容 | 上游报错 | capability 校验，不支持时降级或清空 |
| 子 agent 自行提升模型 | 成本/治理风险 | 默认禁止 provider override，所有 route 由本地配置决定 |
| expert 并发过高 | 成本和写入冲突 | `max_expert_concurrency` + single writer policy |
| 配置过复杂 | 用户难以理解 | 先支持 levels，roles 作为高级配置 |

## 17. 一句话结论

本设计把“任务难度评级”作为主 agent 拆分和子 agent 调度之间的结构化契约，把“不同难度使用哪个 provider/model/thinking effort”交给本地配置决定。模型负责给任务评级和拆分，runtime 负责可信路由、能力校验、provider runtime 构造和审计记录。这样既能让复杂任务自动使用更强模型和子 agent，也能避免提示词直接控制本地 provider 带来的治理风险。


## 18. 完整性审查与修订意见

本节是对前述方案的完整性复核。若本节与前文存在冲突，以本节的修订意见和实施顺序为准。

### 18.1 代码事实核对

基于当前代码路径，前述方案中有几处需要修正或细化：

| 代码事实 | 影响 |
|---|---|
| `internal/llm.LLMRuntime.Call()` 会优先使用 `LLMRequest.Provider`，其次才按 model alias / default provider 路由。 | 子 agent 切 provider 不一定需要重新构造一个全新的 `LLMRuntime`；如果目标 provider 已注册到同一个 runtime，只要设置 `Agent.Config.Provider` 即可。 |
| `bootstrap.Manager` 会用 `ProviderConfigs` 注册 enabled providers；`buildSkillsProviderConfigs(cfg)` 会把 `cfg.Providers.Items` 中启用的 provider 转成 runtime provider。 | difficulty route 中的 provider 应优先限制为已启用 provider；MVP 可以复用 shared runtime。 |
| `ReActLoop.think()` 构造 `LLMRequest` 时读取 `loop.agent.config.Provider`、`loop.agent.config.Model`、`loop.config.ReasoningEffort`。 | 路由不能只写 `childConfig.Options["reasoning_effort"]`；必须把 route reasoning 写入 child `LoopReActConfig.ReasoningEffort`。 |
| `SubagentScheduler.runChild()` 当前只按 `task.Model` 覆盖 `childConfig.Model`，没有覆盖 provider / reasoning，并且事件 payload 不包含 route 信息。 | 内部 `spawn_subagents` 是第一批需要改的执行点。 |
| `toolbroker.SpawnAgentArgs` 当前只有 `Model`，没有 `Provider`、`ReasoningEffort`、`Difficulty`。 | 轻量 `spawn_agent` 路径若要支持难度路由，需要扩展 toolbroker 参数和 session context。 |
| `localActorRegistry.Spawn()` 只把 `args.Model` 写入 `AgentSessionContextRequestedModel`；`buildSessionActor()` 只读 requested model。 | AgentControl / actor child session 目前无法独立指定 provider / reasoning，需要新增 context key 或复用 `sessionmeta.ProviderName`、`sessionmeta.ReasoningEffort`。 |
| `buildLocalChatLoopConfig()` 只从 parent `ChatSession.ReasoningEffort` 读取 reasoning。 | 对 `spawn_agent` child session，需在 actor build 阶段读 child session metadata/context，覆盖 loop config reasoning。 |
| `composeChatSystemPromptWithGuidanceForCWD()` 是主 chat 系统提示词的集中拼装点。 | 任务评级提示词应作为独立 render function 插入这里，最好可受配置开关控制。 |
| `PromptBuilder.BuildSubagentPrompt()` 是内部 fresh child prompt 的集中拼装点。 | 子任务 difficulty / routing 信息应在这里注入，但不应被描述为权限来源。 |

### 18.2 完整性结论

前述方案已经覆盖了概念层面的关键模块：提示词、配置、任务结构、schema、调度、事件、测试和治理。但从可实施性看，仍缺少以下闭环：

1. **执行面边界不够明确**
   当前仓库至少有两条“子 agent”路径：
   - `internal/agent` 内部 ReAct 工具 `spawn_subagents`。
   - `toolbroker` / AgentControl 暴露给 chat 模型的 `spawn_agent`、`spawn_team`。

   原方案主要覆盖 `spawn_subagents`，对 `spawn_agent` / `spawn_team` 的元数据、session context、actor build 阶段覆盖不够完整。

2. **Provider 切换方案过重**
   原方案提出 `ChildRuntimeFactory`，但当前 `LLMRuntime` 本身已经支持按 `LLMRequest.Provider` 路由，并且 bootstrap 已能注册多个 provider。MVP 不应先引入 runtime factory，而应优先复用 shared runtime；只有在目标 provider 没注册、或者未来引入隔离 runtime / remote runtime 时，才需要 factory。

3. **Reasoning/thinking effort 传播点不完整**
   对 ReAct child 来说，最终请求使用的是 `LoopReActConfig.ReasoningEffort`，不是单纯读取 `Agent.Config.Options`。因此实现时必须同时覆盖：
   - child loop config。
   - runtime session metadata。
   - debug/event payload。

4. **配置与持久化边界需要收敛**
   本方案只需要“读取本地路由配置”，不应在第一阶段实现自动写回。自动写回会引入配置层级、项目配置污染、环境变量模板保留等额外复杂度，应留到后续 `/agents routing set` 再处理。

5. **模型评级不可完全信任**
   difficulty 是模型输出，可能被 prompt injection、用户措辞或模型误判影响。runtime 需要做归一化、缺省、上限钳制和审计，而不是完全相信模型声明。

## 19. 实施困难点

### 19.1 Provider / model 路由和 runtime 注册

困难点：

- route profile 中的 provider 可能未启用、拼写错误、没有 API key、没有注册到 `LLMRuntime`。
- model 可能是 alias、mapped model、provider default model 或自定义 model。
- provider group / failover group 是否可作为路由目标尚不明确。
- 当前 `LLMRuntime` 支持 provider aliases，但 route 里应该保存 provider name 还是 alias 需要统一。

优化建议：

- MVP 只允许 route provider 指向 `cfg.Providers.Items` 中 `enabled=true` 的真实 provider name，不支持 provider group。
- model 允许为空；为空时使用 provider `default_model`。
- 路由解析器输出 `requested_model` 与 `resolved_model` 两个概念；事件中至少记录最终请求 model。
- 在启动或首次使用时做 provider existence 校验，失败时按策略 fallback parent 或报错。

### 19.2 Reasoning / thinking effort 兼容性

困难点：

- 不同 provider 对 reasoning/thinking 的字段不同。
- `reasoning_effort` 与 Anthropic `thinking`、OpenAI/Codex reasoning effort 并非完全等价。
- 当前 provider capability metadata 可能不完整；现有实现对未知 capability 可能采取兼容透传。

优化建议：

- 配置层只定义 canonical `reasoning_effort`；`thinking_effort` 仅作为输入别名，解析后写入 `ReasoningEffort`。
- 路由解析器只做 effort 归一化，不直接构造 provider-specific thinking object。
- provider-specific 转换继续留在 adapter / request builder 边界。
- 对已声明 capability 的 provider/model 严格校验；对未声明 capability 的 provider/model 先 warning + 透传或清空，具体策略由 `validate_model_capabilities` 控制。

### 19.3 两条子 agent 路径的一致性

困难点：

- `spawn_subagents` 是同一 ReAct loop 内部的 fresh child agent。
- `spawn_agent` 是 AgentControl / runtime session child，生命周期、session metadata、event bridge、mailbox 都不同。
- `spawn_team` 又有 task graph、teammate、lead planner，不能简单套用同一结构。

优化建议：

- 第一阶段只把共享 rating / route resolver 做成公共能力。
- 第二阶段接入 `spawn_subagents`，因为它最靠近 `SubagentTask`。
- 第三阶段接入 `spawn_agent`，扩展 `toolbroker.SpawnAgentArgs` 和 actor session context。
- `spawn_team` 放到第四阶段，只给 team task 增加 difficulty metadata，不立即做复杂 provider 路由。

### 19.4 Prompt 与工具 schema 的稳定性

困难点：

- 系统提示词增加评级要求后，模型可能对所有任务都输出评级，增加 token 和行为噪声。
- 如果要求过强，模型可能过度拆分，导致过多子 agent 和成本上升。
- 工具 schema 新增字段要保持旧 payload 兼容。

优化建议：

- 提示词中明确：“easy 任务不要为了评级而额外输出结构；只有在计划拆分或委派时必须给出 difficulty”。
- `spawn_subagents` / `spawn_agent` schema 新字段全部 optional。
- runtime 对缺失 difficulty 自动 fallback。
- 增加“不要为了使用强模型而夸大难度”的提示。

### 19.5 成本、并发与限流

困难点：

- `hard` / `expert` 可能映射到昂贵模型。
- 多个 expert 子任务并发可能触发 provider rate limit 或成本激增。
- writer 并发还可能引发工作区冲突。

优化建议：

- `max_expert_concurrency` 在 MVP 中至少用于调度器串行化 expert writer。
- 增加 `max_hard_concurrency` / `max_total_routed_children` 的后续扩展点。
- route event 中记录 difficulty、provider、model、usage，便于后续成本统计。
- 保持现有 single writer policy，不因模型更强而放宽写入约束。

### 19.6 配置可观测性和排障

困难点：

- 用户遇到“为什么用了这个模型”时，需要能解释来源。
- 如果 role override、level default、explicit override、parent fallback 混合，排障会困难。

优化建议：

- `SubagentRouteDecision.Source` 必须成为稳定枚举。
- event、hook、debug 都输出 source。
- 增加 dry-run 命令或 debug helper：输入 role+difficulty，输出最终 route。

## 20. 仍然不确定的问题

| 不确定性 | 建议决策 |
|---|---|
| route provider 是否允许 provider group / failover group | MVP 不支持，只允许 enabled provider name；后续单独设计 group routing。 |
| `thinking_effort` 是否作为正式配置字段 | 暂不作为 canonical 字段，仅作为 `reasoning_effort` 别名解析。 |
| 模型显式传入 `provider` 是否允许 | 默认不允许；只有 `allow_explicit_provider_override=true` 才允许，并仍需 provider allowlist。 |
| route 是否应用到 parent 主模型 | 不应用。主模型继续由 `/model` 和 `aicli.chat` 管理。 |
| route 是否应用到 `spawn_team` | 后续阶段再做；MVP 只记录 task difficulty，不改变 teammate provider。 |
| difficulty 是否必须由模型输出 | 不强制。缺失时 runtime fallback；只有模型主动委派子任务时建议输出。 |
| capability metadata 不完整时是否报错 | 默认 warning + fallback / 透传，严格模式再报错。 |
| child session provider/reasoning 如何持久化 | 对 `spawn_agent` 使用 runtime session metadata/context，优先新增 canonical context key 或复用 `sessionmeta.ProviderName`、`sessionmeta.ReasoningEffort`。 |

## 21. 修订后的实施方案

### P0. 冻结范围与增加 feature flag

目标：先把风险降下来，避免一次性改动所有 agent 路径。

实施项：

1. 增加配置开关：
   - `aicli.subagents.routing.enabled`
   - 默认 false 或默认只做 metadata，不改变 provider/model。
2. 明确 MVP 范围：
   - 支持 difficulty metadata。
   - 支持 route resolver dry-run。
   - 首先接入 `spawn_subagents`。
   - `spawn_agent` 作为第二执行面。
   - `spawn_team` 暂只记录，不调度模型。
3. 增加文档和测试基线，确保旧行为不变。

验收：

- routing disabled 时，所有子 agent 行为与当前一致。
- 旧 `spawn_subagents` / `spawn_agent` payload 仍可运行。

### P1. 纯配置与路由解析器

目标：先实现无副作用的 route decision，避免直接动 scheduler。

实施项：

1. 扩展 `agentconfig.AICLIConfig`，增加 `Subagents.Routing`。
2. 新增 difficulty 归一化：`easy|normal|hard|expert`。
3. 新增 `SubagentRouteResolver`，输入：
   - parent provider/model/reasoning。
   - task role/difficulty/provider/model/reasoning。
   - local config。
   - provider registry/capability view。
4. 输出 `SubagentRouteDecision`。
5. 增加 resolver 单元测试。

验收：

- role override > difficulty level > parent inherit 的优先级稳定。
- provider 未启用、model 不兼容、reasoning 不兼容都有测试。
- resolver 不启动 LLM、不创建 agent、无网络副作用。

### P2. Prompt 与 schema 接入，但默认不改变模型

目标：让模型开始提供 difficulty metadata，但不立即改变 provider/model，降低行为风险。

实施项：

1. 新增 `runtimeprompt.RenderTaskDifficultyGuidance()`。
2. 在 `composeChatSystemPromptWithGuidanceForCWD()` 中插入该 guidance。
3. 更新 `PromptBuilder.BuildSubagentPrompt()`，显示 task difficulty / route source。
4. 扩展 `SubagentTask` 和 `spawn_subagents` schema，新增 optional 字段。
5. tool decode 兼容新字段。

验收：

- prompt 测试覆盖评级要求。
- schema 测试覆盖 optional 新字段。
- routing disabled 时，difficulty 只进入 metadata，不改变 model/provider。

### P3. 接入内部 `spawn_subagents` 路径

目标：先让 ReAct 内部 child agent 根据 difficulty 使用不同 provider/model/reasoning。

实施项：

1. `SubagentSchedulerConfig` 增加 `RouteResolver`，不急于增加 `RuntimeFactory`。
2. `runChild()` 中解析 route。
3. 应用 route：
   - `childConfig.Provider = route.Provider`
   - `childConfig.Model = route.Model`
   - `childConfig.DefaultMaxTokens = route.MaxTokens`，如果 task budget 更小则用 task budget。
   - child loop `LoopReActConfig.ReasoningEffort = route.ReasoningEffort`
   - child timeout 使用 task timeout 优先，其次 route timeout。
4. 继续复用 parent `llmRuntime`，前提是 route provider 已注册。
5. provider 未注册时：
   - 若 `inherit_parent_when_missing=true`，fallback parent 并记录 warning。
   - 否则报错。
6. event/hook payload 加入 route 字段。

验收：

- `easy` child 使用配置的 fast provider/model。
- `hard` child 使用配置的 strong provider/model。
- `ReasoningEffort` 出现在实际 LLMRequest。
- provider 未注册 fallback 行为可测试。

### P4. 接入 AgentControl `spawn_agent` 路径

目标：让用户可见的轻量 child session 也支持 difficulty route。

实施项：

1. 扩展 `toolbroker.SpawnAgentArgs`：
   - `Difficulty`
   - `DifficultyRationale`
   - `Provider`
   - `ReasoningEffort`
2. 扩展 `spawn_agent` tool schema。
3. 在 broker 或 localActorRegistry spawn 前解析 route。
4. 将 route 写入 child runtime session context：
   - requested provider。
   - requested model。
   - requested reasoning effort。
   - difficulty / route source。
5. `buildSessionActor()` 读取 child context：
   - 覆盖 child agent provider/model。
   - 覆盖 child loop reasoning。
6. completion mailbox / display mirror metadata 带上 route 信息。

验收：

- `spawn_agent` 不传 difficulty 时旧行为不变。
- `spawn_agent` 传 `difficulty=hard` 时 child session 使用 hard route。
- child session metadata 可恢复，重启/恢复后仍知道 requested provider/model/reasoning。

### P5. Team / spawn_team 后续接入

目标：避免把 team 路径和子 agent MVP 混在一起。

详细设计见独立方案：[spawn-team-teammate-model-routing-plan.md](./spawn-team-teammate-model-routing-plan.md)。本文只保留阶段边界；`spawn_team` teammate-level provider/model/reasoning 路由的粒度、存储、runner 注入点、fallback 语义和测试矩阵以后者为准。

实施项：

1. 先给 `spawn_team.tasks[]` 增加 optional difficulty metadata。
2. LeadPlanner 在 task 分配时记录 difficulty。
3. teammate provider/model 路由单独设计，避免破坏现有 team lifecycle。

验收：

- team task difficulty 可见但不影响 provider。
- 后续再设计 teammate-level route。

### P6. Debug、成本和治理闭环

实施项：

1. `/debug` 输出 routing config summary。
2. `/agents routing test --role writer --difficulty hard` 输出 dry-run route。
3. runtime event / mailbox / hook 输出 route source 和 usage。
4. 增加 expert concurrency guard。

验收：

- 用户能解释每次 routed child 为什么使用某 provider/model。
- 使用量可按 difficulty/provider/model 聚合。

## 22. 修订后的最小可交付版本

为了降低风险，建议 MVP 定义为：

1. 配置支持 `aicli.subagents.routing.levels`。
2. 支持 difficulty normalizer 和 resolver。
3. `spawn_subagents` 支持 difficulty 字段。
4. 仅对内部 `spawn_subagents` 应用 route。
5. 复用 shared `LLMRuntime`，不引入 `ChildRuntimeFactory`。
6. reasoning route 必须写入 child `LoopReActConfig`。
7. 事件中输出 difficulty、provider、model、reasoning_effort、route_source。
8. `spawn_agent` / `spawn_team` 暂不改变 provider，只保留设计和后续阶段。

MVP 之后再进入 AgentControl `spawn_agent` 路径。这样可以先证明核心能力，不会一次性影响所有多 agent 子系统。

## 23. 修订后的测试重点

新增测试建议按以下顺序实现：

1. `NormalizeTaskDifficulty` 别名和非法值测试。
2. `SubagentRouteResolver` levels / roles / fallback 测试。
3. route provider 未启用 / model 缺失 / effort 不支持测试。
4. `spawn_subagents` schema optional 字段测试。
5. `PromptBuilder` difficulty prompt 测试。
6. `SubagentScheduler.runChild` 将 provider/model/reasoning 写入 LLMRequest 的 fake provider 测试。
7. `subagent.started` / `subagent.completed` route payload 测试。
8. routing disabled 旧行为回归测试。
9. 后续 `spawn_agent` child session context 持久化测试。
10. 后续 actor restore 后 provider/model/reasoning 仍生效测试。

## 24. 最终修订结论

方案方向是完整且可行的，但实施上应从“强行重建 child runtime”修正为“优先复用已注册多 provider 的 shared LLMRuntime，通过 child Agent.Config.Provider/Model 和 child LoopReActConfig.ReasoningEffort 完成路由”。

同时，必须把内部 `spawn_subagents` 和 AgentControl `spawn_agent` 分阶段处理。先完成无副作用 resolver 和内部 child route，再扩展用户可见 child session。这样既能满足按难度调度 provider/model/thinking effort 的目标，也能避免一次性牵动 session metadata、actor lifecycle、team task graph 和 provider adapter 的高风险改造。


## 25. 架构级代码规划与模块划分

本模型路由能力不建议直接散落在 `scheduler.go`、`prompt_builder.go`、`toolbroker` 或 `chat_session.go` 中。更合理的架构是把它做成一个独立的“路由策略子系统”：

- Prompt 层负责要求模型给出 `difficulty` 等机器可读元数据。
- Tool/schema 层负责承载这些元数据。
- Routing 层负责把 `difficulty/role/显式 override/父 agent 默认值/本地配置/provider 能力` 解析成最终决策。
- Scheduler/Actor 层只消费路由决策，不内嵌策略。
- LLMRuntime 层继续负责 provider/model 的最终调用和 provider adapter 分发。

换句话说，任务难度是输入信号，最终 provider/model/reasoning_effort 是 runtime policy 的输出，不应由 prompt 直接决定。

### 25.1 推荐新增模块

建议新增包：

```text
backend/internal/modelrouting/
  difficulty.go
  config.go
  resolver.go
  provider_catalog.go
  capability.go
  decision.go
  audit.go
  resolver_test.go
```

职责边界如下：

| 文件 | 职责 |
|---|---|
| `difficulty.go` | 难度枚举、别名归一化、非法值处理 |
| `config.go` | 路由配置结构、默认值、配置校验 |
| `resolver.go` | 核心路由决策算法，纯函数化、无网络副作用 |
| `provider_catalog.go` | provider/model 能力查询抽象，避免 resolver 直接依赖命令层 |
| `capability.go` | reasoning_effort、max_tokens、tool mode 等能力兼容性判断 |
| `decision.go` | `RouteRequest` / `RouteDecision` / `RouteSource` 等核心类型 |
| `audit.go` | route warning、debug 摘要、事件 payload 转换 |
| `resolver_test.go` | levels、roles、fallback、override、disabled 行为测试 |

不建议把这些逻辑放进 `backend/internal/agent/scheduler.go`。Scheduler 应该只做：接收 task、调用 resolver、应用 decision、启动 child agent。

### 25.2 配置层边界

配置结构仍应定义在 `backend/internal/agentconfig/config.go`，因为它负责 YAML/mapstructure 解析。但配置解析后应转换为 `modelrouting.Config`，避免路由核心包直接依赖庞大的顶层配置结构。

建议新增：

```go
type AICLISubagentsConfig struct {
    Routing *AICLISubagentRoutingConfig `yaml:"routing" mapstructure:"routing"`
}

type AICLISubagentRoutingConfig struct {
    Enabled bool `yaml:"enabled" mapstructure:"enabled"`

    DefaultDifficulty string `yaml:"default_difficulty" mapstructure:"default_difficulty"`
    AllowExplicitProvider bool `yaml:"allow_explicit_provider" mapstructure:"allow_explicit_provider"`
    AllowExplicitModel bool `yaml:"allow_explicit_model" mapstructure:"allow_explicit_model"`
    InheritParentWhenMissing bool `yaml:"inherit_parent_when_missing" mapstructure:"inherit_parent_when_missing"`

    Levels map[string]AICLIRouteProfileConfig `yaml:"levels" mapstructure:"levels"`
    Roles  map[string]AICLIRoleRouteConfig    `yaml:"roles" mapstructure:"roles"`
}

type AICLIRouteProfileConfig struct {
    Provider string `yaml:"provider" mapstructure:"provider"`
    Model string `yaml:"model" mapstructure:"model"`
    ReasoningEffort string `yaml:"reasoning_effort" mapstructure:"reasoning_effort"`
    ThinkingEffort string `yaml:"thinking_effort" mapstructure:"thinking_effort"` // alias
    MaxTokens int `yaml:"max_tokens" mapstructure:"max_tokens"`
    TimeoutSec int `yaml:"timeout_sec" mapstructure:"timeout_sec"`
}

type AICLIRoleRouteConfig struct {
    Levels map[string]AICLIRouteProfileConfig `yaml:"levels" mapstructure:"levels"`
}
```

然后在 `modelrouting` 包里提供 adapter：

```go
func FromAICLIConfig(cfg *agentconfig.AICLIConfig) (modelrouting.Config, error)
```

如果担心循环依赖，可以把 adapter 放在 `agentconfig` 或一个轻量 glue 包中，例如：

```text
backend/internal/modelrouting/configadapter/
```

但 MVP 可先在调用方完成字段映射，保持 `modelrouting` 核心包不 import `agentconfig`。

## 26. 核心类型设计

### 26.1 Difficulty

```go
type Difficulty string

const (
    DifficultyEasy   Difficulty = "easy"
    DifficultyNormal Difficulty = "normal"
    DifficultyHard   Difficulty = "hard"
    DifficultyExpert Difficulty = "expert"
)

func NormalizeDifficulty(input string, fallback Difficulty) (Difficulty, bool)
```

返回值中的 `bool` 表示是否发生了别名归一化或 fallback，便于记录 warning。

### 26.2 RouteProfile

```go
type RouteProfile struct {
    Provider string
    Model string
    ReasoningEffort string
    MaxTokens int
    TimeoutSec int
}
```

注意：`thinking_effort` 不建议在内部作为主字段流转，统一归一化为 `ReasoningEffort`。配置层可以接受 `thinking_effort`，但进入 resolver 后只保留 canonical 字段。

### 26.3 RouteRequest

```go
type RouteRequest struct {
    Enabled bool

    TaskID string
    Role string
    Difficulty string
    DifficultyRationale string

    ExplicitProvider string
    ExplicitModel string
    ExplicitReasoningEffort string

    ParentProvider string
    ParentModel string
    ParentReasoningEffort string

    ReadOnly bool
    BudgetTokens int
    TimeoutSec int
}
```

`RouteRequest` 是 scheduler/actor 层给 resolver 的唯一输入。这样后续 `spawn_subagents`、`spawn_agent`、`spawn_team` 都可以复用同一个 resolver。

### 26.4 RouteDecision

```go
type RouteDecision struct {
    Difficulty Difficulty
    Provider string
    Model string
    ReasoningEffort string
    MaxTokens int
    TimeoutSec int

    Source RouteSource
    Warnings []string
    ExplicitOverrideUsed bool
    FallbackUsed bool
}

type RouteSource string

const (
    RouteSourceDisabled RouteSource = "disabled"
    RouteSourceExplicit RouteSource = "explicit"
    RouteSourceRoleDifficulty RouteSource = "role_difficulty"
    RouteSourceDifficulty RouteSource = "difficulty"
    RouteSourceParent RouteSource = "parent"
    RouteSourceProviderDefault RouteSource = "provider_default"
    RouteSourceFallback RouteSource = "fallback"
)
```

`RouteDecision` 必须是可审计的：不仅告诉最终选了什么，还要说明为什么这样选、是否 fallback、是否使用显式 override。

## 27. Resolver 设计

### 27.1 Resolver 只做纯策略决策

建议接口：

```go
type Resolver struct {
    Config Config
    Catalog ProviderCatalog
}

func (r *Resolver) Resolve(req RouteRequest) (RouteDecision, error)
```

核心约束：

1. 不创建 agent。
2. 不创建 runtime。
3. 不访问网络。
4. 不读写文件。
5. 不调用真实 LLM。
6. 只依赖本地配置和 provider capability view。

这样 resolver 可以做大量单元测试，也能被命令行 dry-run 复用。

### 27.2 路由优先级

推荐顺序：

1. routing disabled：返回 parent provider/model/reasoning，`source=disabled`。
2. 显式 override：仅当配置允许时生效。
3. role + difficulty override：如 `roles.writer.levels.hard`。
4. difficulty level：如 `levels.hard`。
5. parent inherit：继承父 agent 当前 provider/model/reasoning。
6. provider default：provider 存在但 model 为空时，取 provider 默认 model。
7. fallback/error：根据配置决定 fallback 到 parent 还是返回错误。

### 27.3 ProviderCatalog 抽象

不要让 resolver 直接依赖 `cmd/aicli` 或具体 provider 初始化逻辑。建议定义：

```go
type ProviderCatalog interface {
    HasProvider(name string) bool
    DefaultModel(provider string) (string, bool)
    HasModel(provider, model string) CapabilityResult
    SupportsReasoningEffort(provider, model, effort string) CapabilityResult
}

type CapabilityResult struct {
    Supported bool
    Warning string
}
```

MVP 阶段如果 provider capability 不完整，可以先采用宽松策略：

- provider 是否存在必须校验。
- model 是否存在如果无法可靠判断，可只做 best-effort warning。
- reasoning_effort 不支持时降级或清空，并写 warning。

后续再接入 model card/provider manifest 做精确能力判断。

## 28. 与现有代码路径的集成方式

### 28.1 `spawn_subagents` 内部路径，MVP 首要目标

涉及文件：

```text
backend/internal/agent/scheduler.go
backend/internal/agent/loop.go
backend/internal/agent/prompt_builder.go
```

`SubagentTask` 增加字段：

```go
type SubagentTask struct {
    ID string
    Role string
    Goal string
    ToolsWhitelist []string
    DependsOn []string
    PatchContext string

    Difficulty string
    DifficultyRationale string
    Provider string
    Model string
    ReasoningEffort string

    BudgetTokens int
    TimeoutSec int
    ReadOnly bool
}
```

`SubagentScheduler` 增加 resolver 依赖：

```go
type SubagentSchedulerConfig struct {
    MaxConcurrent int
    RouteResolver *modelrouting.Resolver
}
```

`runChild()` 中的应用逻辑应类似：

```go
route, err := s.resolveChildRoute(task)
if err != nil {
    return err
}

childConfig := s.parent.config
childConfig.Name = childName
childConfig.Provider = route.Provider
childConfig.Model = route.Model

if route.MaxTokens > 0 {
    childConfig.DefaultMaxTokens = route.MaxTokens
}
if task.BudgetTokens > 0 && (childConfig.DefaultMaxTokens <= 0 || task.BudgetTokens < childConfig.DefaultMaxTokens) {
    childConfig.DefaultMaxTokens = task.BudgetTokens
}

childAgent := NewAgentWithLLM(childConfig, s.parent.llmRuntime)

loopCfg := LoopReActConfig{
    ReasoningEffort: route.ReasoningEffort,
    // 其他字段沿用 parent/default
}
loop := NewReActLoop(childAgent, s.parent.llmRuntime, &loopCfg)
```

关键点：当前 `LLMRuntime.Call()` 已经可以根据 `LLMRequest.Provider` 分发 provider，所以 MVP 不需要 `ChildRuntimeFactory`。只要 child agent 的 `Provider/Model` 和 child loop 的 `ReasoningEffort` 被正确设置，`think()` 生成的 `LLMRequest` 就能走正确路由。

### 28.2 `spawn_agent` / AgentControl 路径，后续阶段

涉及文件：

```text
backend/internal/toolbroker/types.go
backend/internal/toolbroker/broker.go
backend/cmd/aicli/commands/chat_actor_registry.go
backend/cmd/aicli/commands/chat_actor_host.go
```

后续扩展方向：

1. `SpawnAgentArgs` 增加 difficulty/provider/reasoning 字段。
2. broker schema 暴露这些字段。
3. 创建 child session 前调用 resolver。
4. 把 route decision 写入 session context，而不是只写 requested model。
5. `buildSessionActor()` 恢复 session 时读取 provider/model/reasoning/difficulty。
6. `buildLocalChatAgent()` 和 `buildLocalChatLoopConfig()` 同时应用 route。

该路径比内部 `spawn_subagents` 更复杂，因为涉及 session 持久化、actor restore、mailbox/collab event 和用户可见 child session 生命周期，建议放到 MVP 之后。

### 28.3 `spawn_team` 路径，最后接入

Team 不是简单 child agent，它有 task graph、teammate、lead planner 和生命周期事件。第一阶段只建议把 difficulty 作为 task metadata 记录，不立即控制 teammate 模型。等内部 resolver 和 `spawn_agent` 都稳定后，再决定 teammate-level routing。

`spawn_team` teammate-level provider/model/reasoning 路由的详细工程方案已拆到 [spawn-team-teammate-model-routing-plan.md](./spawn-team-teammate-model-routing-plan.md)。该文档明确采用 task execution 粒度路由，并覆盖 AgentControl task projection、TeammateRunner 注入点、per-run route override、事件审计和验收测试。

## 29. Prompt、Schema 与 Runtime Policy 的分离

架构上要避免“prompt 控制 provider 升级”。推荐原则：

1. 系统提示词要求模型输出 difficulty。
2. schema 接收 difficulty、difficulty_rationale。
3. 模型可以填写 provider/model，但只有配置允许显式 override 时才生效。
4. runtime resolver 才是最终权威。
5. event/debug 只展示 resolver 的最终 decision，不盲目信任模型输入。

这样可以避免提示词注入导致子任务自称 `expert` 或指定昂贵 provider/model 后直接生效。

如果要进一步治理，可以增加：

```yaml
aicli:
  subagents:
    routing:
      allow_explicit_provider: false
      allow_explicit_model: true
      max_expert_concurrency: 1
      require_rationale_for_hard: true
      require_rationale_for_expert: true
```

## 30. 代码优化与架构级处理建议

### 30.1 避免重复构造 runtime

MVP 不要为每个 child 创建新的 LLMRuntime。原因：

- provider registry 已在主 runtime 初始化。
- `LLMRuntime.Call()` 已支持按 `LLMRequest.Provider` 分发。
- 重建 runtime 会放大配置加载、provider adapter 初始化、credential 传递、日志 hook、retry policy 的复杂度。

优化方向：共享 runtime，child 只覆盖 `Agent.Config.Provider/Model` 和 `LoopReActConfig.ReasoningEffort`。

### 30.2 Resolver 配置快照化

不要每次派发子任务都重新解析 YAML。启动 chat/session 时构造一次 `modelrouting.Resolver`，内部持有不可变配置快照。配置变更可在新 session 或 reload 命令后生效。

### 30.3 事件可观测性标准化

所有 route 事件建议统一结构：

```json
{
  "task_id": "t1",
  "role": "verifier",
  "difficulty": "hard",
  "difficulty_rationale": "requires cross-module verification",
  "routing": {
    "provider": "anthropic_local",
    "model": "claude-sonnet-4-6",
    "reasoning_effort": "high",
    "source": "role_difficulty",
    "fallback_used": false,
    "warnings": []
  }
}
```

这样 debug、hook、测试和日志都可以复用同一个 payload，不要每个模块各写一套。

### 30.4 显式 override 要受策略控制

`task.Provider` / `task.Model` 是输入，不是最终决定。Resolver 要按配置判断：

- 是否允许模型指定 provider。
- 是否允许模型指定 model。
- 指定 provider/model 是否在 allowlist 中。
- 是否超过用户设置的成本/并发限制。

### 30.5 保持 routing disabled 零行为变化

这是最重要的回归约束：

```yaml
aicli:
  subagents:
    routing:
      enabled: false
```

关闭后：

- 子任务新字段只作为 metadata。
- 原有 `task.Model` override 行为保持一致。
- provider/reasoning 不被自动修改。
- 既有测试不应大量重写。

### 30.6 Capability 分阶段实现

不要一开始就强行精确判断所有 provider 能力。建议：

- P1：只校验 provider 是否注册。
- P2：结合 provider default model / model card 做 model best-effort 校验。
- P3：补齐 reasoning_effort 支持矩阵。
- P4：把 max tokens、tool support、stream support 等纳入 capability。

## 31. 推荐实施顺序

```text
P0: 新增 modelrouting 包和纯单元测试，不接入运行路径。
P1: 扩展 agentconfig 配置结构和配置校验。
P2: 扩展 prompt guidance、SubagentTask、spawn_subagents schema，只记录 metadata。
P3: 在 SubagentScheduler.runChild() 应用 RouteDecision，完成内部 spawn_subagents 路由 MVP。
P4: 增加 event/debug/dry-run，解决可观测性和排障。
P5: 扩展 spawn_agent / AgentControl session context。
P6: 扩展 spawn_team metadata，最后再考虑 teammate provider/model routing。
```

架构上最终应形成如下单向依赖：

```text
agentconfig  --->  modelrouting.Config
                         |
prompt/schema  ---> RouteRequest ---> Resolver ---> RouteDecision
                                            |
agent scheduler / actor host  <-------------+
                                            |
                                     shared LLMRuntime
```

这个结构的好处是：

1. 策略集中，不污染 scheduler 和 actor lifecycle。
2. 纯 resolver 可测试、可 dry-run、可审计。
3. Prompt 输出只是 hint，runtime policy 才是 authority。
4. MVP 可以小步落地，后续再扩展到 `spawn_agent` 和 `spawn_team`。
5. 保持与现有 shared `LLMRuntime` 架构兼容，避免过早引入 runtime factory。


## 32. 兼容性处理与 Agent 子系统升级建议

本节补充说明：当主 agent 没有严格按照“任务难度评级 + 子任务路由元数据”的协议输出时，runtime 应如何兼容；以及现有 agent 子系统是否需要为模型路由做结构化升级。

### 32.1 总体兼容原则

模型路由不能依赖主 agent 一定输出正确、完整、可信的决策。主 agent 的输出只能视为“任务意图和难度 hint”，不能视为最终调度策略。最终 `provider`、`model`、`reasoning_effort` 必须由本地 runtime policy 决定。

因此兼容处理应遵循以下原则：

1. **协议字段可选，runtime 必须能补齐默认值。**
   - `difficulty`、`difficulty_rationale`、`provider`、`reasoning_effort` 都不应成为老版本主 agent 调用 `spawn_subagents` 的强制字段。
   - 缺失字段由 `TaskNormalizer` 和 `modelrouting.Resolver` 兜底。

2. **主 agent 的显式 provider/model 只是请求，不是授权。**
   - 即使 task 中包含 `provider` / `model`，也必须经过配置开关、allowlist、capability 校验。
   - 默认建议禁用 provider override，只保留受控 model override 或完全交给难度路由。

3. **routing disabled 必须零行为变化。**
   - 当 `subagents.routing.enabled=false` 时，新增字段只作为 metadata。
   - 原有 `task.Model` 行为保持不变。
   - 不自动切 provider，不自动改 reasoning。

4. **默认采用 permissive 兼容模式，严格模式作为可选配置。**
   - MVP 阶段优先保证老 prompt、老工具调用、老测试不被破坏。
   - 对缺失或轻微错误字段进行 warning + fallback。
   - 对结构完全不可解析、依赖图非法、权限越界等高风险情况再 reject。

5. **所有兼容降级都必须可观测。**
   - 每个 fallback、忽略 override、难度归一化、provider/model 降级，都应进入 `RouteDecision.Warnings` 和 debug/event payload。

### 32.2 主 Agent 未按协议输出时的处理策略

建议将 `spawn_subagents` 输入先经过“解码 -> 归一化 -> 校验 -> 路由”流水线，而不是让 Scheduler 直接使用原始 task。

| 主 agent 输出异常 | 推荐处理 | 是否中断 |
|---|---|---|
| 缺失 `difficulty` | 使用 `routing.default_difficulty`，默认 `normal`；记录 warning | 否 |
| `difficulty` 为未知值 | 先按别名归一化；仍未知则 fallback 到默认难度 | 否 |
| 缺失 `difficulty_rationale` | 允许为空；记录低优先级 warning | 否 |
| 缺失 `role` | 设置为 `general` 或 `custom` | 否 |
| `role` 未知 | 视为 `custom`，不使用 role-specific override | 否 |
| 缺失 `id` | runtime 生成稳定 task id，例如 `task-1`、`task-2` | 否 |
| task id 重复 | 自动追加后缀或重写为稳定唯一 id；记录 warning | 否 |
| `depends_on` 指向不存在任务 | permissive 模式下丢弃该依赖并 warning；strict 模式下 reject | 取决于模式 |
| 依赖图出现环 | reject 当前批次，返回明确错误 | 是 |
| 指定 `provider` 但配置不允许 | 忽略该 provider，走本地路由；记录 warning | 否 |
| 指定 `model` 但配置不允许 | routing enabled 时忽略；routing disabled 时保留旧行为 | 否 |
| 指定 provider/model 不存在 | fallback 到难度路由或 parent route；记录 warning | 否 |
| 指定 `reasoning_effort` 不受支持 | 降级为 provider 支持的最近档位，或忽略 | 否 |
| `budget_tokens` / `timeout` 无效 | 使用配置默认值或裁剪到允许范围 | 否 |
| `tools_whitelist` 越权 | 按 runtime policy 过滤；必要时 reject | 视越权程度 |
| 主 agent 完全没有调用 `spawn_subagents` | 不进入子 agent 路由流程，保持单 agent 行为 | 否 |
| 工具参数 JSON 无法解析 | 工具层返回 validation error，让主 agent 修正或走单 agent fallback | 是 |

关键点是：**只要任务结构可以安全解释，就尽量继续执行；只要涉及权限、依赖拓扑、工具越权、不可解析结构，就应失败得明确。**

### 32.3 兼容模式配置建议

建议在 `aicli.subagents.routing` 下补充兼容策略配置：

```yaml
aicli:
  subagents:
    routing:
      enabled: true
      compatibility_mode: permissive # permissive | strict
      default_difficulty: normal

      # 主 agent 显式指定 provider/model 时的权限
      allow_explicit_provider_override: false
      allow_explicit_model_override: false

      # 非标准输入处理策略
      unknown_difficulty_policy: fallback_default # fallback_default | reject
      unknown_role_policy: custom                # custom | reject
      duplicate_task_id_policy: rewrite          # rewrite | reject
      invalid_dependency_policy: warn_drop       # warn_drop | reject
      malformed_task_policy: reject              # reject | skip
      unsupported_reasoning_policy: downgrade    # downgrade | ignore | reject
```

默认建议：

- `compatibility_mode=permissive`
- `unknown_difficulty_policy=fallback_default`
- `unknown_role_policy=custom`
- `duplicate_task_id_policy=rewrite`
- `invalid_dependency_policy=warn_drop`
- `malformed_task_policy=reject`

这样既能兼容旧 prompt，又不会吞掉完全非法的工具输入。

### 32.4 推荐新增 TaskNormalizer 与 PolicyValidator

当前设计如果直接在 `SubagentScheduler.runChild()` 内处理兼容逻辑，会导致 Scheduler 过度膨胀。建议新增两个明确模块：

```text
backend/internal/agent/subagenttask/
  decoder.go       # 将 tool args 解码为 RawSubagentTask
  normalizer.go    # 缺省值、别名归一化、id 去重、role 归一化
  validator.go     # 依赖图、权限、预算、工具白名单校验
  types.go         # RawSubagentTask / NormalizedSubagentTask
```

处理流程：

```text
spawn_subagents raw args
  ↓
SubagentTaskDecoder
  ↓
TaskNormalizer
  ↓
TaskPolicyValidator
  ↓
modelrouting.Resolver
  ↓
RoutedSubagentTask / ChildRunSpec
  ↓
SubagentScheduler 执行
```

建议类型分层：

```go
type RawSubagentTask struct {
    ID                  string
    Role                string
    Goal                string
    Difficulty          string
    DifficultyRationale string
    Provider            string
    Model               string
    ReasoningEffort     string
    DependsOn           []string
    ToolsWhitelist      []string
    BudgetTokens        int
    TimeoutSec          int
    ReadOnly            bool
}

type NormalizedSubagentTask struct {
    ID                  string
    Role                string
    Goal                string
    Difficulty          modelrouting.Difficulty
    DifficultyRationale string
    ExplicitProvider    string
    ExplicitModel       string
    ExplicitReasoning   string
    DependsOn           []string
    ToolsWhitelist      []string
    BudgetTokens        int
    TimeoutSec          int
    ReadOnly            bool
    Warnings            []string
}

type RoutedSubagentTask struct {
    Task     NormalizedSubagentTask
    Decision modelrouting.RouteDecision
}
```

这样可以把“不规范输入兼容”与“模型路由决策”分离，避免 resolver 承担太多 task 清洗职责。

### 32.5 当前 Agent 子系统是否需要升级

结论：**需要升级，但不建议重写。** 当前 agent 子系统已经具备 child agent、scheduler、shared LLMRuntime 等基础能力，适合在现有架构上做分层增强。

需要升级的原因：

1. **Scheduler 当前承担过多职责。**
   - 现在 `runChild()` 负责复制 parent config、处理 task model、创建 child agent、创建 loop、执行任务。
   - 如果再把难度归一化、provider/model 策略、capability fallback、event audit 全塞进去，会变成策略和执行耦合。

2. **缺少标准的子任务生命周期模型。**
   - 当前从 tool task 到 child agent 执行之间缺少明确的 normalized/validated/routed 状态。
   - 路由系统需要在每个阶段记录 warning 和 fallback reason。

3. **缺少 child agent 构造边界。**
   - provider/model/reasoning_effort 最终要同时影响 `Agent.Config` 和 `LoopReActConfig`。
   - 这些应用逻辑应集中在 `ChildAgentFactory` 或 `SubagentRunner` 中，而不是散落在 scheduler。

4. **可观测性需要统一事件模型。**
   - 难度、路由来源、fallback、显式 override 是否被忽略，都需要进入 event/debug/hook。
   - 如果不升级事件结构，后续排障会很困难。

### 32.6 推荐的 Agent 子系统升级边界

建议新增或调整以下组件：

```text
backend/internal/modelrouting/
  difficulty.go
  config.go
  resolver.go
  capability.go
  decision.go
  audit.go

backend/internal/agent/subagenttask/
  decoder.go
  normalizer.go
  validator.go
  types.go

backend/internal/agent/
  child_factory.go      # 根据 RoutedSubagentTask 构造 child Agent + Loop config
  scheduler.go          # 只保留调度、依赖、并发、结果汇总
```

职责划分：

| 模块 | 职责 | 不应承担 |
|---|---|---|
| `prompt_builder` | 提示主 agent 输出 difficulty metadata | 不决定最终 provider/model |
| `subagenttask.Normalizer` | 兼容非标准 task 输入 | 不查 provider capability |
| `subagenttask.Validator` | 校验依赖、预算、工具白名单、危险字段 | 不创建 child agent |
| `modelrouting.Resolver` | 根据本地配置输出 RouteDecision | 不执行 LLM 调用 |
| `ChildAgentFactory` | 应用 RouteDecision 到 child config / loop config | 不做依赖调度 |
| `SubagentScheduler` | 依赖排序、并发控制、执行生命周期 | 不内嵌路由策略 |
| `LLMRuntime` | 按 request provider/model 调用 adapter | 不理解 task difficulty |

升级后的关键路径：

```text
主 agent 调用 spawn_subagents
  ↓
工具层解码 raw tasks
  ↓
Normalizer 兼容旧字段/缺失字段/别名/重复 id
  ↓
Validator 校验依赖图、工具权限、预算 timeout
  ↓
Resolver 根据 difficulty + role + 本地配置得到 RouteDecision
  ↓
ChildAgentFactory 生成 ChildRunSpec
  ↓
Scheduler 按依赖和并发运行 child
  ↓
Child ReActLoop 使用 child provider/model/reasoning_effort 创建 LLMRequest
  ↓
Shared LLMRuntime 调用对应 provider adapter
```

### 32.7 MVP 落地建议

为降低风险，建议分三步做：

1. **第一步：只加兼容字段和 normalizer，不改变运行行为。**
   - `SubagentTask` 增加 `difficulty`、`difficulty_rationale`、`provider`、`reasoning_effort`。
   - `spawn_subagents` schema 增加可选字段。
   - Normalizer 输出 warnings，但 scheduler 仍按原逻辑运行。

2. **第二步：接入 modelrouting resolver，但默认关闭。**
   - 配置 `enabled=false` 时必须与旧行为一致。
   - 单元测试覆盖 missing/invalid difficulty、explicit override disabled、fallback 等情况。

3. **第三步：启用内部 `spawn_subagents` 路由。**
   - 只影响 child agent，不影响主 agent。
   - 不先扩展 `spawn_agent` / `spawn_team`。
   - 记录完整 route audit payload，便于回滚。

### 32.8 最终建议

兼容性处理的核心不是要求主 agent 永远遵守协议，而是让 runtime 对“不完整、不标准、不可信”的 task 输入有稳定处理路径。建议把主 agent 输出定位为 metadata hint，把本地 resolver 定位为 authority。

当前 agent 子系统应做架构级轻量升级：新增 task normalization、policy validation、routing resolver、child factory 和统一 audit event。这样可以在不破坏旧行为的前提下，引入按任务难度调度不同 provider/model/reasoning_effort 的能力，并为后续扩展到 `spawn_agent`、`spawn_team` 打好基础。

## 33. 完整性评估与剩余补充项

### 33.1 总体结论

当前方案已经达到“架构方案完整、MVP 闭环可实施”的程度，但还不能认为是“工程实现细节完全闭合”。

更准确的判断是：

- **设计闭环基本完整**：已经覆盖任务评级、主 agent 输出协议、本地配置、路由解析、子 agent 调度、兼容降级、审计观测和渐进上线。
- **MVP 可以进入实现**：可以先限定在 `spawn_subagents` 路径内实现，不影响 `spawn_agent`、`spawn_team` 和主 agent 自身模型选择。
- **完整工程落地仍需补齐实现级细节**：尤其是现有代码事实校验、配置迁移、provider 能力目录、失败降级、测试矩阵、调试可观测性和安全边界。

因此，本方案不建议一次性全量改造，而应按 P0/P1/P2 分阶段落地。

### 33.2 完整性矩阵

| 模块/领域 | 当前完整度 | 评价 |
|---|---:|---|
| 任务难度分级 | 高 | 已定义 easy/normal/hard/expert，并明确主 agent 输出只是 hint。 |
| 系统提示词改造 | 高 | 已要求主 agent 对任务拆分和难度给出结构化描述。 |
| 子 agent 任务协议 | 中高 | 已规划扩展字段，但还需要和现有 tool schema / JSON decoder 做代码级对齐。 |
| 本地路由配置 | 中高 | 已有配置结构方向，但需要补齐默认值、校验、迁移和 reload 策略。 |
| 路由 Resolver | 高 | 已明确 runtime policy authoritative，并给出优先级链路。 |
| 子 agent 调度集成 | 中高 | 已明确 scheduler 不承载策略，但需要实际拆出 ChildAgentFactory / subagenttask 模块。 |
| Provider/Model 能力匹配 | 中 | 思路完整，但需要明确能力数据来源、不可用 provider 处理和 capability fallback。 |
| 兼容性处理 | 高 | 已覆盖主 agent 未按协议返回、字段缺失、非法字段、路由关闭等情况。 |
| 可观测性/审计 | 中 | 已有 audit/event 方向，但事件字段、日志格式、debug UX 需要定稿。 |
| 成本/并发/预算治理 | 中 | 已提到预算，但还需要明确 token budget、并发池、超时和重试策略。 |
| 安全边界 | 中 | 已明确不信任主 agent override，但还需要 provider/model allowlist 和 prompt injection 防护规则。 |
| 测试方案 | 中 | 已有测试方向，但需要形成具体测试矩阵和 legacy regression tests。 |
| 渐进发布 | 中高 | 已建议 feature flag 和 routing disabled 零行为变化，但需要明确版本迁移策略。 |

### 33.3 还需要补齐的关键问题

#### 33.3.1 现有代码事实校验

在正式编码前，需要再次确认以下事实是否仍成立：

- `SubagentTask` 的现有字段和 JSON 解码方式。
- `SubagentScheduler.runChild()` 当前如何复制 parent config、覆盖 `task.Model`。
- `LoopReActConfig.ReasoningEffort` 是否可以安全按 child 覆盖。
- `LLMRuntime.Call()` 是否完全基于 request 中的 provider/model 分发。
- 当前 provider 配置是否支持同一进程内多 provider 并存。

如果这些事实有变化，方案中的接入点需要同步修正。

#### 33.3.2 Provider/Model capability catalog

需要明确能力目录来源：

- 是写死在配置文件中；
- 从 provider adapter 暴露；
- 从模型清单动态加载；
- 还是由配置显式声明。

建议 MVP 使用显式配置方式，后续再升级为 adapter capability discovery。

#### 33.3.3 配置校验与默认值

需要补齐配置加载阶段的校验：

- routing enabled 时，difficulty route 是否至少有一个可用 fallback；
- provider 是否存在；
- model 是否为空；
- reasoning_effort 是否被目标 provider 支持；
- role override 是否引用了不存在的 difficulty；
- strict/permissive 模式下错误是否应阻止启动。

#### 33.3.4 失败、重试和降级策略

需要明确以下运行时策略：

- 路由到的 provider 不可用时，是否回退 parent provider；
- 模型不存在或鉴权失败时，是否重试 fallback model；
- reasoning_effort 不支持时是降级、忽略还是失败；
- 子 agent 超时时是否允许换模型重试；
- 失败后的 audit event 如何记录。

#### 33.3.5 安全与权限边界

必须明确：

- 主 agent 不能直接提升到未授权 provider/model；
- explicit provider/model override 默认关闭；
- 可 override 的 provider/model 必须走 allowlist；
- 任务难度不能作为绕过工具权限、文件权限、审批策略的依据；
- prompt injection 不应影响本地路由策略。

#### 33.3.6 测试矩阵

至少需要覆盖：

| 场景 | 预期 |
|---|---|
| routing disabled | 保持旧行为，`task.Model` 兼容。 |
| missing difficulty | fallback 到 default difficulty。 |
| invalid difficulty | permissive 下 fallback，strict 下拒绝或报错。 |
| role+difficulty override | 优先使用 role override。 |
| provider unavailable | 触发 fallback 或失败策略。 |
| unsupported reasoning_effort | downgrade/ignore/warn。 |
| duplicate task id | permissive 下重写，strict 下拒绝。 |
| invalid dependency | permissive 下 warn/drop，strict 下失败。 |
| explicit model override disabled | 忽略主 agent 指定模型。 |
| legacy subagent task | 不要求新增字段也能正常执行。 |

#### 33.3.7 Debug/UX 支持

建议后续补充：

- `--debug-routing` 或等价 debug 开关；
- 在日志中输出 route decision；
- 在子 agent 启动事件中包含 provider/model/reasoning_effort/source/warnings；
- 提供配置 dry-run 校验命令；
- 提供“给定任务 metadata，显示最终路由结果”的诊断能力。

### 33.4 是否可以进入实现

可以进入实现，但建议分阶段：

1. **P0：兼容优先的 MVP**
   - 增加 difficulty 字段解析；
   - 增加 routing 配置；
   - 实现 `modelrouting.Resolver`；
   - 只接入 `spawn_subagents`；
   - routing disabled 默认零行为变化；
   - permissive compatibility 默认开启。

2. **P1：治理与可观测性**
   - 增加 audit event；
   - 增加 provider/model capability 校验；
   - 增加 route decision debug；
   - 增加失败 fallback；
   - 完善测试矩阵。

3. **P2：架构升级**
   - 拆分 `subagenttask` 模块；
   - 引入 `ChildAgentFactory`；
   - 减少 scheduler 内部配置拼装；
   - 支持更完整的 provider capability discovery；
   - 再评估是否扩展到 `spawn_agent` / `spawn_team`。

### 33.5 最终判断

方案整体上是完整的，已经覆盖从“主 agent 如何表达任务难度”到“本地运行时如何根据配置选择 provider/model/reasoning_effort”再到“子 agent 如何被调度执行”的主链路。

但它目前更接近一份**完整的架构设计与 MVP 实施方案**，不是一份已经完全锁定所有细节的工程变更说明。正式开发前还需要完成代码事实校验、配置校验规则、provider 能力来源、失败降级策略、测试矩阵和调试事件格式的最终确认。


## 34. 实现级落地蓝图（基于当前代码事实）

本节把前文的架构方案进一步落到当前代码结构中，目标是让后续实现能够按最小风险分阶段合入，而不是在 `scheduler.go` / `loop.go` 中继续堆叠策略逻辑。

> 当前代码事实校验时间：2026-06-21。若后续代码结构变化，本节的路径和接入点需要同步修订。

### 34.1 当前代码中的确定事实

| 领域 | 当前事实 | 对方案的影响 |
|---|---|---|
| 子任务 DTO | `backend/internal/agent/scheduler.go` 中 `SubagentTask` 只有 `ID/Role/Goal/ToolsWhitelist/DependsOn/PatchContext/Model/BudgetTokens/TimeoutSec/ReadOnly` | 需要新增 `Difficulty/DifficultyRationale/Provider/ReasoningEffort/Routing` 等字段，或引入规范化后的内部 DTO。 |
| tool 参数解码 | `backend/internal/agent/loop.go` 的 `decodeSubagentTasks()` 只解析现有字段，`goal` 缺失直接报错，`id` 缺失自动生成 | 需要把“原始解析”和“兼容归一化”拆开，避免把所有兼容逻辑写进 `loop.go`。 |
| tool schema | `spawnSubagentsToolDefinition()` 当前未声明 `difficulty/provider/reasoning_effort` | 需要扩展 schema，但字段必须 optional，保证旧主 agent 仍能调用。 |
| 子 agent 配置应用 | `SubagentScheduler.runChild()` 复制 parent config，只用 `task.Model` 覆盖 `childConfig.Model`，只用 `BudgetTokens` 覆盖 `DefaultMaxTokens` | 路由结果必须在 child 创建前应用到 `childConfig.Provider/Model/DefaultMaxTokens`。 |
| reasoning 生效点 | `ReActLoop` 构造 `LLMRequest` 时使用 `loop.config.ReasoningEffort`，而不是 `Agent.Config.Options` | `reasoning_effort` 必须写入子 loop 的 `LoopReActConfig.ReasoningEffort`，只写 agent config 不会生效。 |
| LLMRuntime 分发 | `LLMRuntime.Call()` 的 provider 选择顺序是 `req.Provider -> req.Model alias -> default provider -> runtime router -> req.Model -> default model` | 子 agent 若要稳定命中目标 provider，必须显式设置 `LLMRequest.Provider`，即设置 `childConfig.Provider`。 |
| 本地配置 | `agentconfig.AICLIConfig` 目前有 `Chat/Runtime/ModelCards` 等，没有 `Subagents/Routing` 配置段 | 需要新增 `AICLISubagentsConfig` / `AICLISubagentRoutingConfig`。 |
| 模型能力 | `agentconfig.ModelCapabilitySpec` 已有 `ReasoningModel/ReasoningEfforts/ReasoningEffortBudgets/MaxTokens/MaxContextTokens` | 可以复用为 provider/model capability catalog 的 MVP 数据源。 |
| spawn_agent | `toolbroker.SpawnAgentArgs` 当前只有 `Model`，没有 `Provider/ReasoningEffort/Difficulty`；`agentcontrol` session context 只有 `agent_requested_model` | 不建议 P0 接入 spawn_agent；后续需要单独升级 session metadata 和 child actor 初始化。 |
| 自动 plan 子任务 | `subagent_plan.go` 会从 `PlanStep` 推导 role/readOnly/tools，但没有 difficulty | 自动计划生成的子任务需要默认 `normal`，或在后续阶段增加 difficulty inference。 |

### 34.2 P0 的工程边界

P0 只解决一个闭环：

```text
主 agent 调用内部 spawn_subagents
  -> Go 侧解析/归一化子任务
  -> 本地 resolver 决定 provider/model/reasoning_effort
  -> scheduler 创建 child agent + child ReActLoop
  -> child LLMRequest 按路由结果调用 provider
```

P0 明确不做以下事情：

- 不改变主 agent 自身 provider/model。
- 不接入 `spawn_agent` 外部 child session 工具。
- 不接入 `spawn_team` 编排任务。
- 不重写 agent 子系统和 session registry。
- 不引入动态 provider capability discovery；先复用本地配置中的模型能力。
- 不允许主 agent 直接绕过本地策略指定任意 provider/model。

### 34.3 建议新增/调整的模块

#### 34.3.1 `backend/internal/modelrouting`

职责：只做模型路由决策，不依赖 `agent` 包，避免形成 Go import cycle。

建议文件：

```text
backend/internal/modelrouting/
  types.go          # Difficulty, RouteRequest, RouteDecision, Config, Policy
  resolver.go       # Resolver.Resolve(ctx, request) RouteDecision
  validate.go       # 配置校验、difficulty/reasoning_effort 归一化
  capability.go     # ProviderCatalog 接口、capability 匹配与降级
  audit.go          # routing warning/reason/source 的结构化表达
  resolver_test.go
```

核心类型建议：

```go
type Difficulty string

const (
    DifficultyEasy   Difficulty = "easy"
    DifficultyNormal Difficulty = "normal"
    DifficultyHard   Difficulty = "hard"
    DifficultyExpert Difficulty = "expert"
)

type RouteRequest struct {
    TaskID                string
    Role                  string
    Difficulty            Difficulty
    DifficultySource      string // explicit/default/inferred/fallback
    ProviderHint          string
    ModelHint             string
    ReasoningEffortHint   string
    ReadOnly              bool
    BudgetTokens          int
    ParentProvider        string
    ParentModel           string
    ParentReasoningEffort string
}

type RouteDecision struct {
    Enabled         bool
    Provider        string
    Model           string
    ReasoningEffort string
    MaxTokens       int
    Source          string // disabled/default/role_override/difficulty/explicit/fallback
    FallbackUsed    bool
    Warnings        []string
    Audit           map[string]interface{}
}
```

`modelrouting` 不应直接创建 child agent，也不应知道 `SubagentScheduler`。它只把“应该使用什么 provider/model/reasoning_effort”算出来。

#### 34.3.2 `backend/internal/agent` 内部 P0 文件拆分

P0 为了降低改动量，可以暂时保留 `SubagentTask` 在 `agent` 包中，但把逻辑拆到新文件：

```text
backend/internal/agent/subagent_task_decode.go       # decodeSubagentTasks 的迁移目标
backend/internal/agent/subagent_task_normalize.go    # difficulty/role/id/alias 归一化
backend/internal/agent/subagent_task_validate.go     # graph/goal/tool/readOnly 校验
backend/internal/agent/child_factory.go              # 根据 RouteDecision 创建 child Agent + LoopConfig
backend/internal/agent/subagent_routing_bridge.go    # agent.SubagentTask -> modelrouting.RouteRequest
```

这样可以先避免大规模移动 DTO。P1/P2 再考虑把 DTO 抽到独立包。

#### 34.3.3 DTO 抽包的长期建议

如果要把子任务 DTO 从 `agent` 中抽出，不建议让 `backend/internal/agent/subagenttask` 再反向依赖 `agent`。更稳妥的方式是：

```text
backend/internal/subagenttask/
  types.go
  decode.go
  normalize.go
  validate.go
```

然后 `agent` 包导入 `subagenttask`。如果必须放在 `backend/internal/agent/subagenttask`，也必须让 DTO 所有权迁移到该子包，且该子包不能 import 父级 `agent` 包，否则很容易出现 import cycle。

### 34.4 配置结构落点

建议在 `agentconfig.AICLIConfig` 增加：

```go
type AICLIConfig struct {
    MCP        *AICLIMCPConfig        `yaml:"mcp" mapstructure:"mcp"`
    Log        *AICLILogConfig        `yaml:"log" mapstructure:"log"`
    Retry      *AICLIRetryConfig      `yaml:"retry" mapstructure:"retry"`
    Timeout    *AICLITimeoutConfig    `yaml:"timeout" mapstructure:"timeout"`
    Theme      *AICLIThemeConfig      `yaml:"theme" mapstructure:"theme"`
    Chat       *AICLIChatConfig       `yaml:"chat" mapstructure:"chat"`
    Runtime    *AICLIRuntimeConfig    `yaml:"runtime" mapstructure:"runtime"`
    ModelCards *AICLIModelCardsConfig `yaml:"model_cards" mapstructure:"model_cards"`
    Subagents  *AICLISubagentsConfig  `yaml:"subagents" mapstructure:"subagents"`
}
```

配置示例：

```yaml
aicli:
  subagents:
    routing:
      enabled: false
      compatibility_mode: permissive   # permissive | strict
      default_difficulty: normal
      default_route:
        inherit_parent: true
      explicit_override:
        allow_provider: false
        allow_model: false
        allow_reasoning_effort: false
        allowlist:
          providers: []
          models: []
      routes:
        easy:
          provider: local-small
          model: qwen2.5-coder-7b
          reasoning_effort: low
          max_tokens: 4096
        normal:
          provider: local-default
          model: qwen2.5-coder-14b
          reasoning_effort: medium
          max_tokens: 8192
        hard:
          provider: openai-compatible
          model: gpt-4.1
          reasoning_effort: high
          max_tokens: 12000
        expert:
          provider: claude-compatible
          model: claude-4-opus
          reasoning_effort: high
          max_tokens: 16000
      role_overrides:
        verifier:
          easy:
            provider: local-small
            model: qwen2.5-coder-7b
            reasoning_effort: low
        writer:
          hard:
            provider: openai-compatible
            model: gpt-4.1
            reasoning_effort: high
      fallback:
        on_provider_unavailable: inherit_parent  # inherit_parent | next_route | fail
        on_model_unsupported: inherit_parent
        on_reasoning_unsupported: downgrade      # downgrade | ignore | fail
      audit:
        include_in_runtime_events: true
        include_in_debug_logs: true
```

P0 默认值原则：

- `routing.enabled=false`：必须完全保持旧行为。
- `compatibility_mode=permissive`：缺失/非法字段走 fallback，不阻断任务。
- `default_difficulty=normal`。
- 未配置 route 或 route 不可用时，默认继承 parent provider/model。
- 主 agent 提供的 `provider/model/reasoning_effort` 只是 hint；只有本地配置显式允许并通过 allowlist 才能生效。

## 35. 路由决策状态机与数据流

### 35.1 状态机

建议把内部处理拆成以下状态，便于测试和定位问题：

```text
RawToolArgs
  ↓ decode
RawSubagentTask
  ↓ normalize
NormalizedSubagentTask
  ↓ validate
ValidatedSubagentTask
  ↓ route resolve
RouteDecision
  ↓ child spec build
ChildRunSpec
  ↓ scheduler run
SubagentResult
```

| 状态 | 产生者 | 主要职责 | 失败处理 |
|---|---|---|---|
| `RawToolArgs` | LLM tool call | 保存原始 JSON args | args 不是对象/agents 不是数组：reject。 |
| `RawSubagentTask` | decoder | 只做类型读取，不做策略判断 | 单个 item 非对象：reject。 |
| `NormalizedSubagentTask` | normalizer | 生成 id、规范 role/difficulty、处理 alias、填默认 readOnly/tools | permissive 下补默认值并记录 warning；strict 下非法字段 reject。 |
| `ValidatedSubagentTask` | validator | 校验 goal、依赖、权限、single-writer、预算范围 | 高风险问题 reject；低风险问题 permissive 下修复/warn。 |
| `RouteDecision` | resolver | 根据 difficulty/role/local config/capability 得出 provider/model/reasoning | route 不可用时 fallback 或 reject。 |
| `ChildRunSpec` | child factory | 把 decision 转成 child config + loop config + audit metadata | spec 不完整 reject。 |
| `SubagentResult` | scheduler | 执行并汇总结果 | 运行时错误写入 result/audit，不反向污染父 agent 配置。 |

### 35.2 关键数据流

```text
spawn_subagents(args)
  ↓
decodeSubagentTasks(args)
  - 兼容读取旧字段：id/role/goal/model/budget_tokens/timeout/read_only
  - 新增读取字段：difficulty/difficulty_rationale/provider/reasoning_effort/thinking_effort
  ↓
NormalizeSubagentTasks(tasks, compatibilityPolicy)
  - missing difficulty -> normal
  - thinking_effort -> reasoning_effort alias
  - invalid difficulty -> fallback normal + warning（strict 下 reject）
  - missing id -> subagent_N
  ↓
ValidateSubagentTasks(tasks, toolPolicy)
  - goal 必填
  - depends_on 必须存在且无环
  - read_only 不能请求 write-like tools
  - parent read-only 不能派 writable child
  ↓
ResolveRoute(task, parentConfig, routingConfig, providerCatalog)
  - routing disabled -> legacy route
  - role+difficulty override
  - difficulty route
  - explicit hint allowlist
  - fallback / inherit parent
  ↓
BuildChildRunSpec(task, decision)
  - childConfig.Provider = decision.Provider
  - childConfig.Model = decision.Model
  - childConfig.DefaultMaxTokens = min(task.BudgetTokens, decision.MaxTokens)
  - loopConfig.ReasoningEffort = decision.ReasoningEffort
  - loopConfig.Thinking = nil 或由后续 adapter 层处理
  ↓
child ReActLoop -> LLMRequest
  - Provider 来自 childConfig.Provider
  - Model 来自 childConfig.Model
  - ReasoningEffort 来自 loopConfig.ReasoningEffort
  ↓
LLMRuntime.Call(req)
```

### 35.3 routing disabled 的兼容分支

`routing.enabled=false` 时建议强制进入 legacy 分支：

```text
if !routing.Enabled:
    childConfig = copy(parentConfig)
    if task.Model != "":
        childConfig.Model = task.Model       # 保留现有行为
    if task.BudgetTokens > 0:
        childConfig.DefaultMaxTokens = task.BudgetTokens
    loopConfig.ReasoningEffort = ""          # 保留现有子 agent 行为
    return
```

注意：即使 schema 和 DTO 已新增 `difficulty/provider/reasoning_effort` 字段，只要 routing disabled，就不应自动切 provider/model/reasoning。新增字段最多进入 event metadata，不能改变执行行为。

### 35.4 routing enabled 的授权分支

`routing.enabled=true` 时建议：

```text
route = routeByRoleAndDifficulty(task.Role, task.Difficulty)
if route empty:
    route = routeByDifficulty(task.Difficulty)
if route empty:
    route = defaultRouteOrInheritParent()

if explicitOverrideAllowed:
    route = mergeAllowlistedHints(route, task.Provider, task.Model, task.ReasoningEffort)
else:
    ignore hints and add warnings

route = validateAgainstProviderCatalog(route)
if unsupported:
    route = applyFallbackPolicy(route)
```

这能同时满足：

- 主 agent 可以表达任务难度；
- 本地策略掌握最终 provider/model；
- 旧 `model` 字段不会在 routing enabled 后无授权地提升模型；
- provider/model/reasoning 不可用时可控降级。

## 36. 当前代码的精确接入点

### 36.1 `decodeSubagentTasks()` 的改造

当前位置：`backend/internal/agent/loop.go`。

P0 可以先在原函数中扩展字段，但建议立即迁移到独立文件，避免 `loop.go` 继续膨胀。

新增字段建议：

```go
type SubagentTask struct {
    ID                  string
    Role                string
    Goal                string
    Difficulty          string
    DifficultyRationale string
    Provider            string
    Model               string
    ReasoningEffort     string
    // ThinkingEffort 只作为输入 alias，不建议长期保留为执行字段。
    BudgetTokens        int
    TimeoutSec          int
    ReadOnly            bool
    ToolsWhitelist      []string
    DependsOn           []string
    PatchContext        []FilePatch
    Routing             *SubagentRoutingMetadata
}
```

兼容读取规则：

- `difficulty`：读取 string，归一化为 `easy/normal/hard/expert`。
- `difficulty_rationale`：只用于审计和调试，不参与策略授权。
- `provider`：只作为 hint，默认不生效。
- `model`：routing disabled 时保持旧语义；routing enabled 时作为 hint，是否生效由本地策略决定。
- `reasoning_effort`：canonical 字段。
- `thinking_effort`：兼容 alias，解析后写入 `ReasoningEffort`，并记录 alias warning。

### 36.2 `spawnSubagentsToolDefinition()` 的改造

schema 中新增 optional properties：

```json
{
  "difficulty": {
    "type": "string",
    "enum": ["easy", "normal", "hard", "expert"],
    "description": "Estimated task difficulty. Local runtime treats this as a routing hint."
  },
  "difficulty_rationale": {
    "type": "string",
    "description": "Short reason for the difficulty rating."
  },
  "provider": {
    "type": "string",
    "description": "Optional provider hint. The local runtime may ignore it unless explicitly allowed."
  },
  "reasoning_effort": {
    "type": "string",
    "enum": ["low", "medium", "high"],
    "description": "Optional reasoning effort hint. The local runtime validates it against local policy."
  },
  "thinking_effort": {
    "type": "string",
    "description": "Deprecated alias for reasoning_effort."
  }
}
```

不要把这些字段放进 `required`。否则旧主 agent 或旧提示词产生的任务会被拒绝。

### 36.3 `SubagentScheduler.runChild()` 的改造

当前 `runChild()` 同时承担：

- child config 复制；
- legacy model 覆盖；
- prompt 构建；
- child agent 创建；
- event/hook 发送；
- loop config 创建；
- loop 执行；
- result 汇总。

建议引入 `ChildAgentFactory` 后，`runChild()` 只保留调度生命周期逻辑：

```go
spec, decision, err := s.childFactory.Build(ctx, ChildBuildRequest{
    Parent:  s.parent,
    Task:    task,
    Options: options,
})
if err != nil { ... }

childAgent := spec.Agent
loopConfig := spec.LoopConfig
childSessionID := spec.SessionID
```

`ChildAgentFactory` 负责：

- 复制 parent config；
- 调用 `modelrouting.Resolver`；
- 应用 `Provider/Model/MaxTokens`；
- 设置 `LoopReActConfig.ReasoningEffort`；
- 生成 routing audit payload；
- 构建 child system prompt。

这样可以避免 routing 策略污染 scheduler。

### 36.4 `LoopReActConfig` 的应用点

当前 `LLMRequest` 构造处已经读取：

```go
Provider:        loop.agent.config.Provider,
Model:           loop.agent.config.Model,
ReasoningEffort: loop.config.ReasoningEffort,
Thinking:        types.CloneThinkingConfig(loop.config.Thinking),
```

因此实现时必须保证：

```go
childConfig.Provider = decision.Provider
childConfig.Model = decision.Model
loopConfig.ReasoningEffort = decision.ReasoningEffort
```

不能只把 `reasoning_effort` 放进 `childConfig.Options` 或 prompt metadata，否则不会进入 `LLMRequest.ReasoningEffort`。

### 36.5 `LLMRuntime` 的路由关系

由于 `LLMRuntime.Call()` 已经支持 `req.Provider`，P0 不需要重写 runtime。重点是：

- `RouteDecision.Provider` 应该尽量是已注册 provider 名或可解析 alias；
- resolver 可以调用 `LLMRuntime.ResolveProviderName()` 或一个包装后的 `ProviderCatalog.ResolveProvider()` 做校验；
- 如果 `Provider` 为空，runtime 会继续按 model alias/default provider/router 选择，这可能导致不可预期的 provider；
- 因此 routing enabled 且 route 有明确 provider 时，应显式设置 `childConfig.Provider`。

### 36.6 `agentconfig` 的改造

需要新增配置结构并进入加载链路：

```go
type AICLISubagentsConfig struct {
    Routing *AICLISubagentRoutingConfig `yaml:"routing" mapstructure:"routing"`
}

type AICLISubagentRoutingConfig struct {
    Enabled             bool
    CompatibilityMode   string
    DefaultDifficulty   string
    DefaultRoute        AICLIRouteSpec
    Routes              map[string]AICLIRouteSpec
    RoleOverrides       map[string]map[string]AICLIRouteSpec
    ExplicitOverride    AICLIExplicitOverridePolicy
    Fallback            AICLIRoutingFallbackPolicy
    Audit               AICLIRoutingAuditConfig
}
```

配置校验建议在加载后执行，而不是等到首次子 agent 调用时才暴露明显错误。


## 37. 最小可行补丁拆分

为降低回归风险，建议拆成以下补丁序列。每个补丁都应能独立测试。

### 37.1 PR-1：纯类型与配置，不接入执行

目标：让配置可以被加载和校验，但不影响运行行为。

改动范围：

```text
backend/internal/agentconfig/config.go
backend/internal/modelrouting/types.go
backend/internal/modelrouting/validate.go
backend/internal/modelrouting/resolver_test.go
```

内容：

- 新增 `aicli.subagents.routing` 配置结构。
- 新增 difficulty/reasoning_effort 归一化函数。
- 新增配置默认值函数。
- 新增配置校验：provider/model 空值、difficulty key 非法、fallback 策略非法等。
- 不接入 `scheduler.go`，不改变任何子 agent 行为。

验收：

- 老配置文件可以正常加载。
- 未配置 `aicli.subagents` 时行为无变化。
- 配置了 routing 但 `enabled=false` 时不报错或只做轻量校验。
- `go test ./backend/internal/modelrouting ./backend/internal/agentconfig` 通过。

### 37.2 PR-2：扩展 spawn_subagents 协议，但 routing disabled 零行为变化

目标：主 agent 可以输出 difficulty 等字段，但默认不影响执行。

改动范围：

```text
backend/internal/agent/scheduler.go
backend/internal/agent/loop.go 或 backend/internal/agent/subagent_task_decode.go
backend/internal/agent/loop_test.go
```

内容：

- `SubagentTask` 新增 `Difficulty/DifficultyRationale/Provider/ReasoningEffort`。
- `decodeSubagentTasks()` 读取新字段。
- `spawnSubagentsToolDefinition()` schema 新增 optional 字段。
- 事件中可附带 difficulty metadata，但不改变 provider/model/reasoning。
- routing disabled 下保持：
  - `task.Model` 仍覆盖 `childConfig.Model`；
  - `task.Provider` 被忽略；
  - `task.ReasoningEffort` 被忽略；
  - `task.Difficulty` 只用于 metadata。

验收：

- 老格式 tool call 测试通过。
- 新格式 tool call 能解析 difficulty。
- routing disabled 下，子 LLMRequest 和旧版本一致。

### 37.3 PR-3：接入 Resolver，但默认仍 disabled

目标：实现完整 resolver，并在 child 创建前具备可调用能力，但默认不启用。

改动范围：

```text
backend/internal/modelrouting/resolver.go
backend/internal/modelrouting/capability.go
backend/internal/agent/subagent_routing_bridge.go
backend/internal/agent/child_factory.go
backend/internal/agent/scheduler.go
```

内容：

- 实现 difficulty route、role override、default route、fallback。
- 实现 provider/model allowlist。
- 实现 reasoning_effort capability 校验。
- 引入 `ChildAgentFactory`，但 disabled 时返回 legacy spec。
- `runChild()` 调用 factory，不直接承载 routing 策略。

验收：

- routing disabled 的 regression tests 必须全绿。
- routing enabled 时，不同 difficulty 能产生不同 `RouteDecision`。
- `hard/expert` 能写入 `childConfig.Provider/Model` 和 `loopConfig.ReasoningEffort`。

### 37.4 PR-4：观测、审计和 debug

目标：让路由决策可解释、可排查。

改动范围：

```text
backend/internal/agent/scheduler.go
backend/internal/modelrouting/audit.go
backend/internal/runtimeevents 或 hooks 相关文件
CLI debug/config validate 相关入口（若已有）
```

内容：

- `subagent.started` event 增加：
  - `difficulty`
  - `difficulty_source`
  - `route_provider`
  - `route_model`
  - `route_reasoning_effort`
  - `route_source`
  - `route_warnings`
- hook `EventSubagentStart/Stop` 同步带上 routing metadata。
- debug log 输出 route decision。
- 可选增加 dry-run：给定 role+difficulty 输出 route decision。

验收：

- debug 模式可以看到为什么某个任务使用某模型。
- warning 不包含 API key、base_url secret、完整 prompt 等敏感信息。

### 37.5 PR-5：扩展到自动 plan 子任务

目标：让 `BuildSubagentTasksFromPlan()` 生成的任务也能参与 routing。

内容：

- 最小实现：所有 plan task 默认 `Difficulty=normal`。
- 可选增强：根据 step 描述/工具类型推导：
  - 简单查找/读取 -> easy；
  - 普通编辑/测试 -> normal；
  - 跨模块重构/架构设计 -> hard；
  - 高风险迁移/复杂安全/多系统协同 -> expert。
- 推导结果必须标记 `DifficultySource=inferred`。

验收：

- 自动计划任务不因缺少 difficulty 失败。
- writer/verifier 任务仍遵守 single-writer 和 verifier 规则。

### 37.6 PR-6：再评估 spawn_agent / spawn_team

目标：决定是否把路由能力扩展到轻量 child agent session 和 team orchestration。

现状约束：

- `SpawnAgentArgs` 只有 `Model`，没有 `Provider/ReasoningEffort/Difficulty`。
- `agentcontrol` session context 只有 `agent_requested_model`，没有 route decision metadata。
- `spawn_team` 涉及 task graph、teammate session、mailbox、lead planner，不适合和 P0 混合实现。

升级方向：

- `SpawnAgentArgs` 新增 optional `difficulty/provider/reasoning_effort`。
- `agentcontrol` 新增 session context：
  - `agent_requested_provider`
  - `agent_requested_reasoning_effort`
  - `agent_task_difficulty`
  - `agent_route_provider`
  - `agent_route_model`
  - `agent_route_reasoning_effort`
- child session actor 初始化时应用 `RouteDecision`。
- `spawn_team` 任务模型增加 difficulty 或从 priority/role 推导 difficulty。

建议等 `spawn_subagents` 路径稳定后再做。

## 38. 实施困难点与不确定性

### 38.1 难点一：旧 `model` 字段的语义兼容

当前 `SubagentTask.Model` 是唯一可由主 agent 指定的模型字段，旧行为是在 `runChild()` 中直接覆盖 `childConfig.Model`。

引入 routing 后存在冲突：

- 若继续无条件尊重 `task.Model`，主 agent 可以绕过 difficulty route 和本地策略。
- 若完全忽略 `task.Model`，开启 routing 后可能破坏已有依赖模型字段的 prompt/流程。

建议处理：

| 模式 | 行为 |
|---|---|
| routing disabled | 完全保留旧 `task.Model` 覆盖行为。 |
| routing enabled + explicit model override disabled | `task.Model` 仅作为 hint 记录，默认不生效。 |
| routing enabled + explicit model override allowlist | 只有 model 在 allowlist 且 provider/capability 可用时才覆盖 route。 |
| strict 模式 | 非法/未授权 model hint 可以直接拒绝或记录 policy violation。 |
| permissive 模式 | 忽略非法/未授权 hint，继续使用本地 route，并写 warning。 |

### 38.2 难点二：provider capability 数据源不统一

目前能力信息分散在：

- runtime provider 的 `GetCapabilities()`；
- `agentconfig.Provider.ModelCapabilities`；
- `ModelCatalogProvider.SupportedModels()`；
- `aicli.model_cards` 加载体系。

P0 建议：

- 以本地配置 `providers.<name>.model_capabilities` 为主要 capability catalog；
- `LLMRuntime.ResolveProviderName()` 用于 provider alias 校验；
- 缺失 capability 时不要自动假设支持 high reasoning；
- 对 capability 缺失提供策略：`assume_supported=false` 默认更安全。

P1/P2 再考虑 provider adapter capability discovery。

### 38.3 难点三：reasoning_effort 与 provider-specific thinking 的边界

不同 provider 对 thinking/reasoning 的参数差异很大：

- 有的接受 `reasoning_effort=low/medium/high`；
- 有的接受 token budget；
- 有的需要 `thinking` object；
- 有的完全不支持。

建议边界：

- 路由层只输出 canonical `ReasoningEffort`。
- adapter 层根据 provider/model capability 转换为 provider-specific 参数。
- `thinking_effort` 只做输入 alias，不作为内部长期字段。
- 如果 capability 中声明了 `ReasoningEffortBudgets`，adapter 或请求构建层再转换预算。

### 38.4 难点四：routing fallback 不能导致隐性成本升级

如果 provider/model 不可用，fallback 不能默认升级到更贵模型。否则 easy/normal 任务可能因为本地小模型不可用而被无感路由到 expert 模型。

建议规则：

- fallback 默认优先 `inherit_parent` 或同难度备选，而不是自动升级 difficulty。
- 只有配置显式声明 `allow_difficulty_escalation=true` 才允许升级。
- fallback event 必须记录 `fallback_used=true` 和原因。
- 可配置 hard/expert 每轮最大并发，避免昂贵模型并发爆炸。

### 38.5 难点五：并发调度与 provider 限流

当前 `SubagentScheduler` 只按 reader/writer 和 `MaxConcurrent` 控制并发，不知道 provider 维度。

如果 easy 任务走本地模型、hard/expert 任务走远端模型，可能需要进一步治理：

- provider 级并发池；
- difficulty 级并发池；
- cost budget；
- retry budget；
- hard/expert 任务排队而不是全部同时启动。

P0 可以暂不实现，但文档和配置中应预留：

```yaml
aicli:
  subagents:
    routing:
      concurrency:
        by_difficulty:
          easy: 4
          normal: 3
          hard: 2
          expert: 1
        by_provider:
          openai-compatible: 2
          claude-compatible: 1
```

### 38.6 难点六：prompt 协议不能作为安全边界

系统提示词可以要求主 agent 输出 difficulty，但不能假设主 agent 总会遵守，也不能信任其输出。

需要明确：

- prompt 只是提高结构化输出概率；
- decoder/normalizer/validator/resolver 才是安全边界；
- 主 agent 给出的 `difficulty_rationale` 只用于审计；
- provider/model override 必须由本地配置授权；
- prompt injection 不能影响本地路由策略。

### 38.7 难点七：agent 子系统是否需要升级

结论：需要轻量升级，但不应在 P0 重写。

P0 需要：

- `SubagentTask` 协议升级；
- `ChildAgentFactory` 提取；
- runtime events 增加 routing metadata；
- scheduler 调用 factory。

P1/P2 再升级：

- `spawn_agent` 参数和 session context；
- `spawn_team` task difficulty；
- agent registry 中的 route metadata；
- provider/difficulty 并发治理；
- 子 agent debug UI/CLI。

## 39. 测试矩阵与验收标准

### 39.1 单元测试

#### modelrouting resolver

| 测试 | 输入 | 预期 |
|---|---|---|
| disabled legacy | routing disabled + task model | decision source=disabled，不改变 provider/reasoning。 |
| missing difficulty | difficulty 为空 | difficulty=normal，warning 可选。 |
| invalid difficulty permissive | difficulty=复杂 | fallback normal，记录 warning。 |
| invalid difficulty strict | difficulty=复杂 | 返回错误。 |
| route by difficulty | hard | 命中 hard route。 |
| role override | role=verifier,difficulty=easy | 命中 role override。 |
| explicit model denied | task model=expensive, override disabled | 忽略 model hint。 |
| explicit model allowed | allowlist 包含 model | 使用 hint 或合并 route。 |
| provider unavailable | provider 未注册 | 按 fallback 策略处理。 |
| reasoning unsupported | model 不支持 high | downgrade/ignore/fail 按配置处理。 |

#### subagent task decoder/normalizer

| 测试 | 预期 |
|---|---|
| 旧格式 agents | 成功解析，difficulty 可为空或默认 normal。 |
| 新格式 difficulty | 成功解析。 |
| thinking_effort alias | 写入 reasoning_effort，记录 alias warning。 |
| missing goal | reject。 |
| duplicate id | permissive 下 rewrite 或 reject，strict 下 reject。 |
| invalid dependency | permissive 下 warn/drop 或 reject；strict 下 reject。 |
| dependency cycle | reject。 |

#### scheduler / child factory

| 测试 | 预期 |
|---|---|
| disabled route request | 生成的 LLMRequest 与旧逻辑一致。 |
| enabled easy route | child request provider/model/reasoning_effort 符合 easy route。 |
| enabled hard route | child request provider/model/reasoning_effort 符合 hard route。 |
| budget min | task budget 与 route max_tokens 取更保守值。 |
| event metadata | `subagent.started` 带 route decision。 |
| failure metadata | provider fallback 或错误时 event 带 warning。 |

### 39.2 回归测试必须保护的旧行为

以下行为不能因新增 routing 配置结构而破坏：

- 未配置 `aicli.subagents.routing` 时，所有子 agent 行为保持现状。
- `spawn_subagents` 仍只要求 `goal`。
- `task.Model` 在 routing disabled 时仍覆盖 child model。
- `task.BudgetTokens` 仍覆盖 child `DefaultMaxTokens`。
- read-only parent 仍不能派 writable child。
- single-writer policy 仍生效。
- unknown dependency 仍会被 scheduler 拒绝。
- 子 agent 的 prompt 构建仍由 `PromptBuilder.BuildSubagentPrompt()` 负责。

### 39.3 集成测试建议

使用 mock provider 捕获 `LLMRequest`：

1. 注册两个 provider：`local-small`、`remote-strong`。
2. 配置 easy -> local-small，hard -> remote-strong。
3. 主 agent tool call 派发两个任务：
   - `difficulty=easy`；
   - `difficulty=hard`。
4. 断言两个 child request 的：
   - `Provider` 不同；
   - `Model` 不同；
   - `ReasoningEffort` 不同；
   - event 中 route metadata 正确。

### 39.4 手动验收场景

| 场景 | 命令/操作 | 预期 |
|---|---|---|
| 旧配置启动 | 不配置 routing，运行会派子 agent 的任务 | 行为与旧版本一致。 |
| 开启 routing | 配置 easy/hard 不同模型，要求主 agent 拆两个任务 | 子 agent 使用不同模型。 |
| 主 agent 不输出 difficulty | 刻意用旧提示词 | 子任务默认 normal，不失败。 |
| 主 agent 输出非法 difficulty | difficulty=`super-hard` | permissive 下 fallback，strict 下拒绝。 |
| provider 不存在 | route 指向不存在 provider | fallback 或清晰报错。 |
| reasoning 不支持 | route high 但模型只支持 low/medium | downgrade/ignore/fail 符合配置。 |
| explicit model injection | 主 agent 指定未授权昂贵模型 | 被忽略或拒绝，不能生效。 |

### 39.5 最终工程验收标准

方案实现完成后至少满足：

1. **兼容性**：默认配置下无行为变化。
2. **可控性**：所有 provider/model/reasoning_effort 最终由本地策略授权。
3. **可解释性**：每个子 agent 都能追踪 difficulty、route source、fallback/warnings。
4. **可测试性**：resolver、decoder、factory、scheduler 有独立测试。
5. **可回滚性**：关闭 `routing.enabled` 即可回到 legacy 子 agent 模型选择行为。
6. **可扩展性**：后续可以扩展到 `spawn_agent` / `spawn_team`，但 P0 不被其复杂度拖累。

## 40. 更新后的最终判断

经过进一步对当前代码结构的核对，方案整体仍然成立，但实现时必须注意三个关键点：

1. **reasoning_effort 的实际生效点在 `LoopReActConfig`，不是 Agent 配置。**
   因此 child factory 必须同时产出 child agent config 和 child loop config。

2. **LLMRuntime 已支持 request-level provider/model 分发，P0 不需要重写 runtime。**
   真正需要做的是在子 agent 创建前把 `RouteDecision.Provider/Model` 放到 child config 中。

3. **`spawn_agent` / `spawn_team` 当前协议不足，不应纳入第一阶段。**
   内部 `spawn_subagents` 是最小、最可控、最容易验证的落地点。

因此，推荐实施路线是：

```text
先实现配置 + resolver
  -> 扩展 spawn_subagents 协议
  -> 引入 ChildAgentFactory
  -> 仅接入内部 SubagentScheduler
  -> 补齐事件/测试/debug
  -> 再评估 spawn_agent/spawn_team 升级
```

这一路线可以在不破坏现有 agent 子系统的前提下，把“按任务难度路由不同 provider/model/reasoning_effort”的能力落地为一个可配置、可审计、可回滚的本地运行时策略。

## 41. 2026-06-22 实施状态补充

当前实现已经超过第 40 节最初建议的 P0 范围：除内部 `spawn_subagents` 路线外，`spawn_agent` 轻量子会话也已接入难度路由和权限模式传递。第 40 节应理解为“保守落地建议”，不是当前最终边界。

截至本次补充，`spawn_agent` 相关状态如下：

- `spawn_agent` schema 已暴露 `difficulty`、`difficulty_rationale`、`provider`、`model`、`reasoning_effort` / `thinking_effort`、`permission_mode`。
- child session 会持久化 route metadata，并在 status、completion event、hook payload、TUI timeline 中回显 provider/model/reasoning/difficulty/permission 信息。
- child 首轮 prompt 和后续 follow-up run 会携带 `RunMeta.PermissionMode`，因此可信 bounded 子任务可显式使用 `permission_mode="bypass_permissions"`，父会话自身为 bypass 时也可继承。
- `wait_agent` / `read_agent_events` 已把 `waiting_approval` 作为 ready state，并暴露 pending approval 的 id/reason/risk 信息，避免父 agent 无意义轮询。
- 新增 broker 工具 `resolve_agent_approval`，用于父 agent 对 `spawn_agent` child 的 pending tool approval 执行 approve/deny。输入 child `id` / `session_id` / path、`request_id`、`allow`，可选 `patched_args`；执行后返回 child 最新状态。
- 系统提示已明确：简单单命令检查应优先在父会话执行；如果 child 进入 `waiting_approval`，应调用 `resolve_agent_approval`，不要重复 wait/poll，也不要在父会话重跑同一个工具作为 fallback。

本补充解决的是 `spawn_agent` default 权限模式下的审批闭环缺口。路由能力仍不应被视为权限绕过机制；difficulty/provider/model/reasoning 只影响模型选择与审计，不改变 tool permission、workspace 权限或审批策略。
