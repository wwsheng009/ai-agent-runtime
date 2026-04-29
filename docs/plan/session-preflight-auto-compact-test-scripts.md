# Session Preflight Auto Compact 测试脚本文档

## 目标

本文档用于验证 `session preflight` 自动压缩恢复修复是否有效，覆盖以下风险点：

- 已知模型能力存在 `auto_compact_token_limit` 时，默认 `contextmgr.DefaultBudget().MaxPromptTokens=12000` 不应误作为硬拦截线。
- `internal_operation=compact` 的内部摘要请求不应携带普通工具、MCP meta tools 或由工具触发的 `tool_choice=auto`。
- 未显式配置 `compact_reasoning_effort` 时，不应发送 `reasoning_effort=none`。
- OpenAI-compatible provider 返回 `choices=null` 或空数组时，应进入明确错误/重试链路，而不是变成空 `LLMResponse`。
- latest replay block 中超大的 tool result 应被内容级降载，同时保留 assistant tool-call 与 tool-result 的邻接结构。
- compact provider 返回空 summary 或失败时，应启用 deterministic fallback summary，使恢复链路继续推进。

## 前置条件

在 Windows PowerShell 中执行。仓库根目录为：

```powershell
E:\projects\ai\ai-agent-runtime
```

Go module 位于 `backend` 目录，因此所有 `go test` 命令都应在：

```powershell
E:\projects\ai\ai-agent-runtime\backend
```

执行。

## 一键定向回归脚本

该脚本覆盖本次修复直接相关的包，适合作为每次改动后的第一轮验证。

```powershell
$ErrorActionPreference = "Stop"
Set-Location "E:\projects\ai\ai-agent-runtime\backend"

$packages = @(
  "./internal/compactruntime",
  "./internal/historyguard",
  "./internal/llm",
  "./internal/agent",
  "./internal/chatcore",
  "./cmd/aicli/commands"
)

go test $packages
```

预期结果：

- 所有包返回 `ok`。
- 不应出现 `active_turn_not_compactable` 相关测试失败。
- 不应出现 `empty provider response` 被误判为成功的测试失败。

## 全量回归脚本

该脚本用于确认跨包编译和回归影响。

```powershell
$ErrorActionPreference = "Stop"
Set-Location "E:\projects\ai\ai-agent-runtime\backend"

go test ./...
```

已知说明：

- `internal/background` 中 `TestManagerRecoversPendingAndFailsInterruptedRunningJobs` 偶发存在时序等待超时。
- 如果全量测试只因该用例失败，应单独重跑 `go test ./internal/background` 做确认。

偶发超时复核脚本：

```powershell
$ErrorActionPreference = "Stop"
Set-Location "E:\projects\ai\ai-agent-runtime\backend"

go test ./internal/background
```

预期结果：

- 单独重跑通过时，可将首次全量失败判定为偶发时序超时。
- 如果单独重跑仍失败，需要按 `internal/background` 独立问题处理，不应归因到本次 compact 修复。

## 按风险点拆分测试

### 预算语义

验证默认 12k 不覆盖已知模型能力，同时显式 context 预算仍能约束。

```powershell
$ErrorActionPreference = "Stop"
Set-Location "E:\projects\ai\ai-agent-runtime\backend"

go test ./internal/agent -run "TestResolvePromptPreflightBudget"
```

关键断言：

- `TestResolvePromptPreflightBudget_DoesNotLetDefaultBudgetOverrideKnownCapability` 应通过。
- 对 `auto_compact_token_limit=200000` 的模型，`PromptBudget` 应解析为 `200000`。
- `BudgetCandidates` 不应包含 `default_context_max_prompt_tokens`。
- `TestResolvePromptPreflightBudget_ExplicitContextBudgetStillConstrainsCapability` 应通过。
- 显式 `context_max_prompt_tokens=12000` 时，`PromptBudget` 应保持 `12000`。

### compact 请求形态

验证内部 compact 请求禁用工具面，并默认省略 `reasoning_effort`。

```powershell
$ErrorActionPreference = "Stop"
Set-Location "E:\projects\ai\ai-agent-runtime\backend"

go test ./internal/compactruntime -run "TestMaybeCompactDefaultCompactRequestDisablesToolsAndOmitsReasoningEffort|TestMaybeCompactUsesModelSpecificCompactSettings"
go test ./internal/llm -run "TestProviderWrapper_InternalCompactRequestDisablesTools"
```

关键断言：

- compact 请求 metadata 应包含 `internal_operation=compact`。
- compact 请求 metadata 应包含 `disable_tools=true`。
- compact 请求 metadata 应包含 `disable_meta_tools=true`。
- 默认情况下 `ReasoningEffort` 应为空字符串。
- 只有 capability 显式配置 `CompactReasoningEffort: "none"` 时，请求才应带 `none`。
- OpenAI-compatible HTTP body 不应包含 `tools`。
- OpenAI-compatible HTTP body 不应包含 `tool_choice`。

### 空 choices 响应

验证 provider 返回 `choices=null` 或空数组时不再被当作成功响应。

```powershell
$ErrorActionPreference = "Stop"
Set-Location "E:\projects\ai\ai-agent-runtime\backend"

go test ./internal/llm -run "TestProviderWrapper_CallRejectsEmptyChoices"
```

关键断言：

- `provider.Call(...)` 应返回 error。
- error 文本应包含 `empty_provider_choices`。
- response 应为 `nil`。

### latest replay block 降载

验证最新工具调用块中的超大 tool result 会被内容级降载，并保持工具消息结构正确。

```powershell
$ErrorActionPreference = "Stop"
Set-Location "E:\projects\ai\ai-agent-runtime\backend"

go test ./internal/historyguard -run "TestCompactActiveTurnReplay_ReducesLatestReplayToolResultWithoutBreakingToolPair|TestCompactActiveTurnReplay_CompactsEarlierReplayAndKeepsLatestBlock"
```

关键断言：

- assistant tool-call message 仍在 tool result 前一条。
- tool result 的 `ToolCallID` 不变。
- tool result metadata 应包含 `active_turn_tool_result_reduced=true`。
- 降载后的 content 应包含 `Tool result content compacted for prompt budget.`。
- artifact/checkpoint 关键引用应尽量保留。

### deterministic fallback compact

验证 compact provider 空 summary 或报错时，本地 fallback summary 能接管。

```powershell
$ErrorActionPreference = "Stop"
Set-Location "E:\projects\ai\ai-agent-runtime\backend"

go test ./internal/compactruntime -run "TestMaybeCompactFallsBackToDeterministicSummaryWhenProviderSummaryEmpty|TestMaybeCompactFallsBackToDeterministicSummaryWhenProviderErrors"
```

关键断言：

- compact 不应返回 error。
- `ReplacementHistory` 不应为空。
- compact summary metadata 应包含 `summary_source=deterministic_fallback`。
- provider 错误场景下 metadata 应包含 `summary_fallback_reason`。
- `UsageSource` 应为 `deterministic_fallback`。

### chatcore prompt preflight 恢复

验证共享 chat tool loop 的 preflight 和 whole-history compactor 路径仍可继续执行。

```powershell
$ErrorActionPreference = "Stop"
Set-Location "E:\projects\ai\ai-agent-runtime\backend"

go test ./internal/chatcore -run "TestExecuteToolLoop_UsesWholeHistoryCompactorToContinueAfterPromptPreflight|TestExecuteToolLoop_FailsPromptPreflightAfterCompactionStillExceedsBudget"
```

关键断言：

- whole-history compactor 成功时，第三次 provider request 应使用 compacted history。
- compact 后仍超限时，应返回 `prompt_still_exceeds_budget_after_compaction`。
- 不应出现 provider request 在 preflight 失败后继续被发送的情况。

## HTTP artifact 检查脚本

该脚本用于检查真实运行后产生的 provider wrapper request artifact，确认 compact 请求不再携带工具面。

使用前替换 `$ArtifactDir` 为实际目录，例如：

```powershell
C:\Users\vince\.aicli\chat-logs\20260428_220328\runtime-http
```

脚本：

```powershell
$ErrorActionPreference = "Stop"
$ArtifactDir = "C:\Users\vince\.aicli\chat-logs\20260428_220328\runtime-http"

Get-ChildItem -Path $ArtifactDir -Filter "*request_provider_wrapper.json" |
  Sort-Object Name |
  ForEach-Object {
    $raw = Get-Content -Path $_.FullName -Raw
    if ($raw -notmatch '"internal_operation"\s*:\s*"compact"') {
      return
    }

    $hasTools = $raw -match '"tools"\s*:'
    $hasToolChoice = $raw -match '"tool_choice"\s*:'
    $hasReasoningEffort = $raw -match '"reasoning_effort"\s*:'

    [PSCustomObject]@{
      File = $_.Name
      HasTools = $hasTools
      HasToolChoice = $hasToolChoice
      HasReasoningEffort = $hasReasoningEffort
    }
  }
```

预期结果：

- compact request 的 `HasTools` 应为 `False`。
- compact request 的 `HasToolChoice` 应为 `False`。
- 未显式配置 `compact_reasoning_effort` 时，`HasReasoningEffort` 应为 `False`。

如果脚本没有输出，说明该 artifact 目录中没有发现 `internal_operation=compact` 请求，需要先执行会触发 compact 的真实场景。

## 复现类场景手工验收

构造或执行一个会话，使其满足以下条件：

- 模型能力配置包含较大的 `max_context_tokens` 和 `auto_compact_token_limit`，例如 `max_context_tokens=270000`、`auto_compact_token_limit=200000`。
- 不显式设置 `context_max_prompt_tokens=12000`。
- 同一 active turn 中触发多个并行或连续工具调用。
- 最新 replay block 中至少一个 tool result 输出超过 8k 字符，并包含 artifact 或 checkpoint 引用。

验收步骤：

1. 运行任务直到产生大 tool result。
2. 观察是否还出现 `prompt 14297 > budget 12000`。
3. 如果没有显式设置 12k，预期不再被默认 12k 本地拦截。
4. 如果显式设置较小 `context_max_prompt_tokens`，预期优先执行 active-turn compaction 或 latest replay tool result 降载。
5. 如果 provider compact 返回空 summary，预期 session history 中出现 `summary_source=deterministic_fallback` 的 compaction message。

关键通过标准：

- 普通大上下文模型不应再因默认 12k 失败。
- latest replay block 过大时，不应直接停在 `active_turn_not_compactable`。
- compact 请求不应携带 `tools` 或 `tool_choice=auto`。
- provider 空 choices 不应静默变成空 summary。
- 自动恢复成功后，应继续当前任务或给出明确的 compact recovery 失败原因。

## 日志和事件检查建议

运行真实会话后，重点检查以下事件或日志字段：

- `context.preflight.started`
- `context.preflight.compacted`
- `context.preflight.failed`
- `session_compact_started`
- `session_compact_completed`
- `session_compact_failed`
- `budget_source`
- `budget_candidates`
- `model_capability_auto_compact_token_limit`
- `active_turn_tool_result_reduced`
- `summary_source=deterministic_fallback`
- `empty_provider_choices`

通过标准：

- 已知模型能力场景下，`budget_source` 应优先来自 `model_capability_auto_compact_token_limit` 或 `model_capability_context_ratio`。
- 默认 fallback 场景才应出现 `default_context_max_prompt_tokens`。
- compact 成功时应看到 `session_compact_completed`。
- provider 空 choices 应显示为错误，而不是只显示 `compact summary response is empty`。

## 常见失败判断

### 仍显示 `budget_source=default_context_max_prompt_tokens`

说明没有解析到 provider/model capability 或 provider context limit。检查：

- provider 是否注册成功。
- model alias 是否能映射到 provider。
- `model_capabilities` 是否包含精确 model 或 `*`。
- `auto_compact_token_limit` 或 `max_context_tokens` 是否大于 0。

### compact request 仍包含 `tools`

说明请求没有进入 `internal_operation=compact` 路径，或 metadata 在中间层丢失。检查：

- `compactruntime.LocalAdapter` 是否设置了 `internal_operation=compact`。
- `ProviderWrapper.convertRequest()` 是否接收到 metadata。
- `GatewayClient.buildAdapterRequest()` 是否接收到 metadata。

### compact request 仍包含 `reasoning_effort=none`

检查模型能力中是否显式配置了：

```yaml
compact_reasoning_effort: none
```

如果显式配置存在，则发送 `none` 是预期行为。没有显式配置时仍发送，则是回归。

### latest replay block 仍无法降载

检查 tool result 是否满足：

- message role 为 `tool`。
- tool result content 字节数超过 `4096`。
- active turn 或总 prompt 已超过当前预算。

如果 tool result 本身很短，降载不会触发；此时应走 older replay compaction 或 whole-history compact。

## 建议 CI 分层

快速 PR 阶段：

```powershell
go test ./internal/historyguard ./internal/llm ./internal/compactruntime ./internal/agent ./internal/chatcore
```

合并前阶段：

```powershell
go test ./internal/historyguard ./internal/llm ./internal/compactruntime ./internal/agent ./internal/chatcore ./cmd/aicli/commands
```

夜间或发布前阶段：

```powershell
go test ./...
```
