# ai-agent-runtime 并行执行工具方案

状态：planned (review fixes applied)
调研日期：2026-05-02
复核日期：2026-05-02
适用仓库：`E:\projects\ai\ai-agent-runtime`

## 结论

当前项目已经能承接多条 `tool call`，但还没有把一次 assistant turn 里的多条调用真正并发化。`backend/internal/agent/loop.go` 中的 `act(...)` 仍是顺序 `for` 循环，因此并行能力如果要在本项目落地，核心改造点应该放在这里，而不是下沉到 `backend/internal/toolkit/registry.go`。

可行的最小方案是：

1. 先串行完成 hook、permission、tool info 解析和 args patch，得到最终 `ToolCall`。
2. 再基于 patch 后的最终调用判断整批是否可并行。
3. 对全部可并行、且 MCP/server 并发门控允许的批次，使用 Go 并发执行器同时跑。
4. 对写类、shell 类、带 mutation hints、broker tools、`spawn_subagents`、未声明可并发的 MCP 调用继续串行。
5. 执行结束或取消后仍按原始调用顺序写回 `MessageBuilder`，保持历史稳定。

这份方案不要求先改模型协议。`backend/internal/types/message.go`、`backend/internal/llm/provider.go` 这条链路已经能承接 `types.ToolCall` 列表，当前缺口主要在执行层。

## 现状

- `ReActLoop.act(...)` 目前对 `toolCalls` 逐个执行，没有并发调度。位置在 `backend/internal/agent/loop.go:808`。
- `act(...)` 内部并不是单一执行路径，而是按工具种类分了三条互斥分支，必须一并纳入考虑：
  1. `ToolBroker` 路径（`broker.IsBrokerTool(tc.Name)`，例如 `spawn_team` 等）。
  2. `spawn_subagents` 路径，本身就是一次 fan-out 调度。
  3. 默认 MCP 路径，通过 `mcpManager.FindTool` + `CallTool` / `CallToolWithMeta` 执行。
- `toolResultsToPayloads(...)` 和 `MessageBuilder.AppendToolResults(...)` 已经负责结果对齐、缺失补修和顺序回填。
- `backend/internal/agent/tool_batch_context.go` 已经保存了完整 assistant tool batch、当前 tool call 和已完成结果，并且 `WithToolBatchContext` / `ToolBatchContextFromContext` 都做了深拷贝，天然适合作为只读快照传给 worker。
- `backend/internal/agent/approved_tool.go` 的单个已批准工具调用，走的是同一套权限、事件和 checkpoint 语义。
- `backend/internal/policy/tool_policy.go` 已经提供了 `IsWriteLikeToolName`、`IsShellLikeToolName`、`HasMutationHints`、`AllowTool*` 这些并行门控所需的判定入口。
- `backend/internal/toolkit/registry.go` 只是单个工具执行入口，不适合作为 batch scheduler。
- 现有 `act(...)` 中存在 `pendingCheckpoints` 这一**批内共享 map**（`loop.go:812,1387,1420,1447`），用于 mutation 工具的 `BeforeMutation` / `AfterMutation` 配对。该 map 当前没有任何同步原语。
- `act(...)` 当前调用 `promoteTeamRunContext(ctx, toolCallContext(ctx, toolCalls, tc.ID, results[:i], ...), results[:i])`，用 `results[:i]` 表示“当前 call 之前已完成的 peer 结果”。在并行模式下 `results[:i]` 不再具有这个语义，必须改为显式快照。
- `agent.emitRuntimeEvent(...)`、`OutputGateway.Process(...)`、`HookManager.Dispatch(...)`、`runPreToolUseHooks` / `runPostToolUseHooks` 在并行模式下会被多个 goroutine 同时调用，必须确认线程安全（如果不安全，应在组件边界加锁，而不是退回串行）。

## 设计原则

1. 默认保守。不能明确证明可并行的调用，一律按串行处理。
2. 批次回填必须稳定。并发完成顺序可以乱，写回历史的顺序不能乱。
3. 并行调度只管“何时启动、何时收集”，不负责改写工具语义。
4. 每个工具调用的 hook、事件、权限和 checkpoint 仍然独立处理。
5. 单个工具失败不能拖垮整个批次，调度层只处理调度错误和上下文取消。
6. 共享对象必须可并发访问；如果某个下游组件不安全，就在组件边界加锁，不要把整个批次退回串行。
7. 并行分类必须基于最终调用。任何 hook 或 permission patch 之后出现 mutation hint，都必须让该批次回退串行。
8. v1 默认关闭真实并行。只有配置显式开启、并发上限大于 1、且目标 MCP/server 通过并发门控时才并行。

## 总体架构

### 1. 调度入口放在 `ReActLoop.act(...)`

建议把 `act(...)` 拆成两层：

- 外层负责预处理、分类、调度和结果收集。
- 内层负责单个已准备工具调用的真实执行流程。

推荐做法是新增一个很小的调度 helper，例如 `backend/internal/agent/tool_parallel_scheduler.go`，里面提供：

- `prepareToolCallBatch(...)`
- `classifyToolCallBatch(...)`
- `runToolCallBatchSerial(...)`
- `runToolCallBatchParallel(...)`
- `shouldExecuteToolParallel(...)`

这样 `loop.go` 不会继续膨胀，单工具逻辑也更容易和 `approved_tool.go` 共享。

预处理阶段必须串行执行，且不真正调用工具。它负责：

- 运行 `runPreToolUseHooks(...)` 和 `HookManager.Dispatch(EventPreToolUse, ...)`。
- 应用 hook `PatchedPayload`。
- 执行 tool whitelist、permission engine、`ToolExecutionPolicy.AllowTool*`。
- 解析 broker / `spawn_subagents` / MCP route，以及 MCP `toolInfo`。
- 应用 permission engine `PatchedArgs`。
- 如果 hook 或 permission 拒绝调用，直接生成该 slot 的 `toolExecutionResult`，后续调度不再执行该工具。

预处理产物建议定义为 `preparedToolCall`，至少包含：

- 原始下标。
- patch 后的 `types.ToolCall`。
- route 类型（broker、spawn_subagents、mcp）。
- MCP metadata（`mcp_name`、`execution_mode`、`trust_level`）。
- 本次调用的 metadata。
- 如果已被拒绝，则包含完整 `toolExecutionResult`。

只有 `prepareToolCallBatch(...)` 完成之后，才能调用 `classifyToolCallBatch(...)`。这样可以保证并行资格基于最终 args，而不是模型最初给出的 args。

并发执行建议使用 `errgroup.WithContext` 或等价 worker pool：

- `errgroup` 只负责传递取消信号，**不**承担工具失败的归集。
- 工具本身的错误写入 `toolExecutionResult.Error`，不要让 worker 返回 non-nil error 给 errgroup（否则会触发整批 cancel）。worker 函数对正常完成、工具失败、policy 拒绝一律返回 `nil`，仅在 `ctx.Err() != nil` 时才返回 ctx 错误。
- `results` 预分配为固定长度，每个 worker 只写自己的下标，最终按下标回填。
- 用一个有缓冲的 channel/semaphore 限制并发到 `MaxParallelToolCalls`。如果引入 `golang.org/x/sync/semaphore` 已经存在于 `go.sum`，直接复用；否则用容量固定的 channel 即可，不引入新依赖。
- 进入 worker 之前必须 `select { case <-ctx.Done(): return ctx.Err() ... }` 检查一次，避免在主调用方已取消后还启动新工具调用。
- 如果上下文在批次中途取消，调度器仍要为每个未启动或未完成的 slot 写入 `toolExecutionResult{Call: prepared.Call, Error: ctx.Err().Error()}`，避免历史中出现 assistant tool call 但缺少 tool result。

### 2. 并行资格由现有 policy 决定

先复用现有边界，不额外发明一套新规则。

可并行的前提建议同时满足。以下判断必须基于预处理后的最终 `preparedToolCall.Call`：

- `runtimepolicy.IsWriteLikeToolName(name) == false`
- `runtimepolicy.IsShellLikeToolName(name) == false`
- `runtimepolicy.HasMutationHints(args) == false`
- 工具不属于 broker tools（`ToolBroker.IsBrokerTool(name) == false`）。这类工具往往承载团队/会话级状态变更，例如 `spawn_team`，v1 一律串行。
- 工具不是 `spawn_subagents`。它本身就是 fan-out 调度，二次并发只会复杂化错误归集和 history 写回。
- `ToolExecutionPolicy.AllowTool(...)` / `AllowToolInfo(...)` / `AllowToolCall(...)` 通过
- 目标 MCP/server 的并发上限大于 1，或该工具不走 MCP manager 共享 session。
- 没有额外的 hook、broker、MCP transport 或 server 级别独占要求。

如果预处理阶段存在 hook 或 permission patch，需要 patch 后重新评估上述条件。只要 patch 后 args 出现 `patch`、`diff`、`mutated_paths`、`changed_files` 等 mutation hint，整批立即回退串行。

推荐第一版采用“全批次一致”策略：

- 只要批次里出现一个必须串行的调用，这一轮就整体退回串行。
- 这样可以避免混合调度时在 `tool_batch_context`、checkpoint 和 history 修复上引入更多边界条件。
- 这一限制还有一个额外好处：v1 的并行分支天然不会触碰 `pendingCheckpoints` 这条路径（因为所有 mutation/shell/write 工具都会让批次退回串行），无需为这张 map 引入额外的并发同步。
- 如果后续 profiling 证明有收益，再升级成“安全调用并行、危险调用串行”的混合模式；混合模式下必须给 `pendingCheckpoints` 加锁，或者把 mutation 工具的 checkpoint 配对收敛回主 goroutine。

并发上限：

- 即使整批可并行，也应有上限以避免 N 路并发打爆远端 MCP 或本地 IO。
- 建议在 `LoopReActConfig` 上新增 `MaxParallelToolCalls`（v1 默认 `1`，`<=1` 等价于禁用真实并行），并由 `LoopOptions` 或运行时配置覆盖。
- 实际并发数取 `min(len(batch), MaxParallelToolCalls)`。
- 对 MCP 工具还需要 per-MCP/server gate，实际并发数取全局上限和 server 上限的较小值。
- 在没有 MCP/server 并发能力声明之前，MCP 调用的 server 上限默认为 `1`。

功能开关：

- 新增配置项（例如 `LoopReActConfig.EnableParallelTools`）v1 默认关闭。只有显式开启且 `MaxParallelToolCalls > 1` 时才进入并行调度。
- 增加环境变量回滚开关，例如 `AICLI_DISABLE_PARALLEL_TOOLS=1`，优先级高于 runtime config。
- runtime config schema、`Agent.RunReAct*` 默认配置、`buildLocalChatLoopConfig(...)`、API session 构造路径都必须接入该配置，否则功能开关只会停留在结构体字段上。

MCP 并发门控：

- 在没有明确 server/tool 元数据前，MCP manager 对同一 `mcpName` 的并发调用必须串行。
- 如果后续新增 `parallel_safe` 或 `max_parallel_tool_calls`，建议挂在 MCP server 配置或 tool metadata 上，再由调度器读取。
- 不建议直接假设 `mcpClient.session.CallTool(...)` 可并发。当前 manager/client 路径复用同一个 client/session，对 stdio、单连接 SSE 或部分 SDK session 来说，默认并发风险过高。

### 3. 历史和批次语义保持不变

并发执行最容易出问题的地方不是工具本身，而是历史写回。

现有链路已经给出了正确的修复层：

- `toolResultsToPayloads(...)` 负责把执行结果转成 `tool_result` payload。
- `MessageBuilder.AppendToolResults(...)` 会按 `toolCallID` 对齐，并自动补齐缺失结果。
- `toolExecutionResultMessage(...)` 负责把单个结果转成 `types.Message`。
- `ToolBatchContext` 负责保留整批工具调用和已完成结果，适合做 crash-safe 恢复。

这里需要特别注意几件事：

- 并行模式下，不能再把 `results[:i]` 当成“当前调用之前已经完成的结果”。具体的调用点是 `toolCallContext(...)` 和 `promoteTeamRunContext(...)`：现有代码同时在两处使用 `results[:i]`。
- 调度器应该显式构造一个**调度前快照**（通常就是空切片，因为同一批 peer call 此时尚未开始或尚未完成），传给所有并行 worker；这与“串行模式下用前序结果”是不同的语义。
- `promoteTeamRunContext` 用来在 spawn_team 完成后把 team 上下文向后续 peer call 传播。在 v1 中 broker tools 已被排除出并行批次，所以这条路径本身只在串行批次里生效，不会受影响。
- 对于真正并行的批次，快照应当只表示“调度前已经存在的结果”，而不是依赖 peer call 的实时完成顺序。
- `MessageBuilder.AppendToolResults(...)` 必须由 `act(...)` 主 goroutine 在收齐所有 worker 后调用一次，worker 不能直接写 builder。
- `pendingCheckpoints` 在 v1 不会被并行批次触碰；如果后续混合模式启用，需要把它从 `map` 改为 `sync.Map` 或在主 goroutine 收敛配对。
- `act(...)` 不能在 assistant tool calls 已写入 builder 后直接返回一个裸 error。对于 context cancel、worker 启动失败、并发调度失败，必须把每个 tool call 转成对应的 `toolExecutionResult`，让后续 `toolResultsToPayloads(...)` 和 `AppendToolResults(...)` 能补齐 tool result。
- 如果某个 worker 尚未启动就被取消，结果内容应明确写成取消错误，并保留原始 `ToolCallID`。这样下一轮模型不会看到未配对的 assistant tool call。

运行时事件顺序：

- `tool.requested` / `tool.completed` / `tool.reduced` 在并行模式下不再保证按调用顺序到达消费者。
- 这些事件已经携带 `tool_call_id` 和 `step`，下游（UI、observability）应以这两个字段重新排序，方案不应承诺事件顺序。
- 但 `MessageBuilder.AppendToolResults` 写回的 `tool_result` 历史顺序仍然必须严格按 `toolCalls` 原始下标。

### 4. 单调用路径要和批量路径共用内核

`backend/internal/agent/approved_tool.go` 里的已批准工具调用，当前是单独走执行流程的。
为了避免并行版和单调用版出现两套逻辑，建议把“执行一个工具 call”的核心逻辑提炼成公共 helper，让下面这些路径共享：

- `ReActLoop.act(...)`
- `Agent.ExecuteToolCall(...)`
- `Agent.ExecuteApprovedToolCall(...)`

这样做的好处是：

- 权限判定一致
- checkpoint 语义一致
- 事件上报一致
- 后续如果要增加工具并发统计，也只改一处

## 落地步骤

### 阶段 1：抽出单工具执行内核

目标是不改行为，只重构结构。

1. 把 `act(...)` 中“执行单个工具”的代码提炼为可复用 helper。
2. 把 hook、permission、args patch、tool route 解析抽成 `prepareToolCallBatch(...)`。
3. 新增批次分类 helper，明确哪些调用可并行、哪些必须串行。
4. 保持当前顺序执行路径不变，先把测试补齐。

### 阶段 2：接入配置与并发门控

目标是让并行能力可控，默认仍保持串行。

1. 在 `LoopReActConfig` 增加 `EnableParallelTools` 与 `MaxParallelToolCalls`。
2. 在 runtime config schema、`Agent.RunReAct*`、`buildLocalChatLoopConfig(...)` 和 API session 构造路径接入配置。
3. 增加环境变量强制关闭开关。
4. 增加 per-MCP/server gate，未知能力的 MCP 上限默认为 `1`。

### 阶段 3：启用批次并行

目标是只对完全可并行的批次并发化。

1. `act(...)` 在预处理完成后做批次分类。
2. 如果整批都可并行，就同时启动所有 worker。
3. worker 完成后按原始下标汇总结果。
4. context 取消或调度失败时，也要为每个 tool call 生成对应错误结果。
5. 仍然只在一个地方调用 `builder.AppendToolResults(...)` 和 `persistBuilderHistory(...)`。

### 阶段 4：统一单调用与批量执行语义

目标是避免 approved tool 路径漂移。

1. `ExecuteToolCall(...)`、`ExecuteApprovedToolCall(...)` 复用同一执行内核。
2. mutation 相关 checkpoint 仍然只包裹单个调用。
3. 如果 future 里要支持更细的混合模式，再补一个独立的调度策略层，不回退到 registry。

### 阶段 5：补观测与回归

目标是让并发问题可诊断。

1. 记录 tool call id、batch index、是否并行、是否命中 mutation gate。
2. 增加批次级日志，能看出谁先完成、谁被串行化。
3. 补齐并发和回填顺序的回归测试。

## 代码改造点

必须修改：

- `backend/internal/agent/loop.go`：拆出单工具执行内核、引入批次分类与调度入口、给 `LoopReActConfig` 增加 `EnableParallelTools` 与 `MaxParallelToolCalls`。
- `backend/internal/agent/approved_tool.go`：复用单工具执行内核，避免 approved 路径与批量路径漂移。
- `backend/internal/config/manager.go`：为 runtime config 增加并行工具配置字段，并给出默认关闭语义。
- `backend/cmd/aicli/commands/chat_actor_host.go`：把 runtime config 中的并行工具配置传入 `LoopReActConfig`。
- `backend/internal/mcp/manager/manager.go` 或相邻辅助文件：增加 per-MCP/server 并发 gate；未知能力默认并发上限为 `1`。

可能修改（取决于内核抽取的边界）：

- `backend/internal/agent/message_builder.go`：仅当抽内核时需要把某些 helper 暴露给调度器。如果 `AppendToolResults` 当前 API 已够用，则**不改**。
- `backend/internal/agent/hook_manager.go`、`backend/internal/agent/tool_runtime_events.go`、`backend/internal/output/`：如发现并发调用不安全，需要在组件边界加锁，而不是退回串行。
- `backend/internal/mcp/client/client.go`：如果最终选择在 client 层串行化同一 session 调用，需要在这里加锁；如果选择 manager 层 gate，则无需修改。

**不需要修改**（在 v1 范围内）：

- `backend/internal/agent/tool_batch_context.go`：`WithToolBatchContext` / `ToolBatchContextFromContext` 都已经做了深拷贝，已经是只读快照语义。
- `backend/internal/policy/tool_policy.go`：分类函数已经齐全，v1 无需新增 helper。
- `backend/internal/toolkit/registry.go`：调度逻辑明确不放在这里。

建议新增的辅助文件：

- `backend/internal/agent/tool_parallel_scheduler.go`：批次分类、并发执行、信号量门控。

测试重点文件：

- `backend/internal/agent/loop_test.go`
- `backend/internal/agent/message_builder_test.go`
- `backend/internal/agent/tool_error_output_test.go`
- 新增 `backend/internal/agent/tool_parallel_scheduler_test.go`，覆盖分类、并发、取消、错误隔离、并发上限。
- 新增或扩展 runtime config 测试，覆盖默认关闭、显式开启、环境变量强制关闭。
- 新增 MCP 并发门控测试，覆盖未知 server 默认串行、显式上限后才允许并发。

如果后续要暴露工具级并行元数据（例如工具自声明 `parallel_safe`），再考虑扩展 `backend/internal/toolkit/registry.go` 或 `skill.ToolInfo`，但不要把调度逻辑放进去。

## 测试策略

至少覆盖下面这些场景：

1. 两个及以上可并行的只读工具，实际运行时间短于串行总和。
2. 先快后慢、先慢后快的混合完成顺序，历史回填仍按调用顺序稳定输出。
3. 包含 write-like / shell-like / mutation hint 的批次，整体回退到串行。
4. 批次中某个工具失败，其他工具仍然完成，结果不丢失。
5. `AppendToolResults(...)` 仍能修复缺失结果。
6. `ExecuteApprovedToolCall(...)` 与 `act(...)` 的 checkpoint 和权限语义一致。
7. 取消上下文时，worker 能及时退出，且不会污染历史。
8. hook 或 permission engine 把原本只读 args patch 成 mutation args 后，整批回退串行。
9. context cancel 发生在 assistant tool calls 写入之后时，所有 tool call 都有对应错误 tool result。
10. `EnableParallelTools=false`、`MaxParallelToolCalls<=1`、未知 MCP server 三种情况都保持原有串行行为。

## 风险与约束

- 并行会放大外部资源竞争，尤其是 shell、文件写入、补丁应用和部分 MCP 工具。
- 某些工具实现可能并不天然线程安全，需要靠 policy 分类或工具内部锁兜底。
- 部分 MCP 传输（例如 stdio 或单连接 SSE）在传输层就会序列化请求，工具级并发不一定能转化成 wallclock 收益；这种情况下并发只是不会变慢，而不是变快。需要在第一阶段 profiling 中确认。
- 当前 MCP manager/client 共享 client/session，不能默认认为 `CallTool` 可并发。v1 必须通过 per-MCP/server gate 保守放行。
- hook 与 permission engine 可以修改 args。任何分类逻辑如果发生在 patch 之前，都可能把 mutation 调用误判为可并行。
- assistant tool calls 已经写入 builder 后，`act(...)` 如果裸返回 error，会留下未配对历史。并发取消路径必须补齐错误 tool result。
- 并行调度会增加日志和排障复杂度，必须保留稳定的 `tool_call_id`、`batch_id`、`step`、`trace_id`，并在日志里标注 `parallel=true|false`。
- 如果把 `tool_batch_context` 做成活跃共享状态，后面很容易出现顺序依赖和 race condition，所以它应该继续保持快照语义。
- `MessageBuilder` 仍然必须是历史回填的唯一入口，不要让 worker 直接改历史。
- `OutputGateway.Process`、`HookManager.Dispatch`、`agent.emitRuntimeEvent` 一旦在并行下被同时调用，就必须保证线程安全；任何不安全的下游组件都应在自己边界加锁，而不是回滚整体并发策略。
- 运行时事件顺序在并行模式下不再稳定，依赖事件顺序的消费者需要改为按 `tool_call_id` + `step` 重排。

## 验收标准

方案完成后应满足：

- 对于可并行工具批次，`act(...)` 能同时启动多个调用，且并发数受 `MaxParallelToolCalls` 限制。
- 对于写类、shell 类、带 mutation hints、broker tools、`spawn_subagents` 的调用，仍然不会并发越界。
- 默认配置保持原有顺序行为；只有 `EnableParallelTools=true` 且 `MaxParallelToolCalls > 1` 才会真实并行。
- 通过 `EnableParallelTools=false` 或环境变量强制关闭开关可以一键回退到原有顺序行为。
- hook/permission patch 后出现 mutation hint 时，该批次回退串行，`pendingCheckpoints` 不会在并行 worker 中被访问。
- 未声明并发能力的 MCP/server 默认串行，同一 `mcpName` 不会被多个 worker 同时调用。
- 历史中的 tool result 顺序和原始 tool call 顺序一致，即使 worker 完成顺序被打乱。
- 单工具失败、context 取消、policy 拒绝都不会污染同批其他 worker 的结果。
- context 取消、worker 未启动或调度失败时，每个 assistant tool call 都有对应错误 tool result。
- 单调用路径（approved tool）和批量路径共享同一套权限、checkpoint 和结果封装逻辑。
- `pendingCheckpoints` 在 v1 范围内仍然只在串行批次中被触碰，无 race。
- 相关单元测试和回归测试全部通过，新增并发回归测试覆盖完成顺序乱序、取消、并发上限。

## 推荐实施顺序

1. 先抽单工具执行内核，不改行为。
2. 再把 `act(...)` 改成“先预处理、patch 后分类、后调度”。
3. 接入 runtime config、环境变量回滚开关和 per-MCP/server gate，默认保持串行。
4. 然后接上并发执行器，先做全批次可并行的保守版本。
5. 补齐取消时的错误 tool result 生成，确保历史永远配对。
6. 最后统一 approved tool 路径和观测、测试。
7. 如果后续有实际收益，再考虑混合模式和更细粒度的工具级并发标记。
