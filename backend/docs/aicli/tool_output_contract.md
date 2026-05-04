# `aicli` 工具输出契约

## 1. 背景

这份文档说明 `aicli` 当前关于工具输出的几个核心约束：

1. **返回给 LLM 的工具结果不应因为 CLI 展示而被截断，但模型回传链路可以有独立的上下文安全截断。**
2. **CLI 可以为了可读性单独做摘要和折叠。**
3. **工具输出的“来源”和“类型”必须显式携带，而不是在链路后面猜。**
4. **结构化输出可以被序列化成 JSON 文本传输，但这不等于要把它裁剪成摘要。**

这个约束主要为了解决两类问题：

- 用户在 CLI 上看到的是紧凑摘要，但模型需要看到完整原文时，过去容易被同一套截断逻辑误伤。
- 工具输出经过 toolkit / MCP / broker / shared chat loop 多层转发后，`tool_source` 和 `output_kind` 容易在中途丢失。

## 2. 设计目标

### 2.1 模型视角

- 对模型来说，`tool_result` 是**上下文事实输入**，不能因为“终端不好看”而丢信息。
- 内置 / toolkit 的 `text` 输出在**较小**时保留全文，在**超大**时会在写入 history 前做独立的 history-safe truncation。
- shell / exec 类工具在执行层还会额外应用 **capture limit**，避免 stdout/stderr 原始聚合本身无限膨胀。
- `structured` / `binary` 输出允许通过 reducer 产出 envelope 摘要，但这是**模型上下文治理**，不是 CLI 展示截断。
- 外部 MCP 工具当前优先保留完整输出，避免 host 过早压缩远端工具结果。

### 2.2 CLI 视角

- CLI 的目标是**让人快速扫读**，不是复刻完整工具结果。
- 因此 CLI 会保留紧凑行数和字节预算，例如只显示前几行 summary。
- CLI 上的 `[meta]` / `[mcp]` / `[broker]` 前缀只影响展示，不改变回传给模型的内容。

### 2.3 调试与日志视角

- 调试日志需要看到 `tool_source` / `output_kind`，便于追查链路中是哪一层做了变形。
- success / error 两条路径都必须保留相同的 metadata。

## 3. 元数据契约

当前统一使用两组 key：

- `tool_source`
- `output_kind`

定义在：

- `backend/internal/toolresult/kind.go`

### 3.1 `tool_source`

当前一等分类：

- `meta`
- `toolkit`
- `mcp`
- `broker`

含义：

- `meta`：runtime 自带的元工具，例如 `list_mcp_resources`
- `toolkit`：内置 toolkit 工具
- `mcp`：外部 MCP server 工具
- `broker`：通过 broker 统一暴露的工具

### 3.2 `output_kind`

当前规范值：

- `text`
- `structured`
- `binary`
- `empty`

含义：

- `text`：原样文本最重要，应优先保留原文；必要时仅在模型回传层做大文本截断
- `structured`：结构化对象，允许通过 envelope 或 JSON 文本承载
- `binary`：二进制结果，不适合直接原样内联
- `empty`：成功但无有效正文

## 4. 为什么会“转 JSON”

这是当前运行时的**传输折中**，不是展示策略。

### 4.1 原因

`aicli` 旧 Function 接口和一部分 runtime 执行面仍以：

- `string`
- `error`

作为主返回值。

因此当 toolkit 工具产生结构化或二进制输出时，runtime 需要先把结果**文本化**，否则这段结果没法穿过现有的接口层。

实现点在：

- `backend/internal/toolkit/interface.go`
- `backend/internal/tools/manager.go`

其中：

- `toolkit.ToolResult.ToJSON()` 负责把 `content` / `data` / `mimeType` / `metadata` 包成可传输 JSON。
- `formatToolkitResult(...)` 在 `structured` / `binary` 分支中，如果没有纯文本 `Content`，会回落到 JSON 文本。

### 4.2 目的

目的不是“为了让 CLI 好显示”，而是：

1. 让结构化结果能穿过 string-only 的执行接口
2. 保留 metadata，不把结构化对象拍平成不可追溯的字符串
3. 为后续 reducer / logger / debug 留下稳定文本载体

### 4.3 这不等于截断

“转成 JSON 文本”和“截断内容”是两件事：

- JSON 文本化：**格式转换**
- CLI 摘要：**展示裁剪**
- 输出治理 envelope：**模型上下文治理**
- 超大文本 history truncation：**模型回传链路的上下文安全保护**

三者必须分开。

## 5. 如何判断文本输出

优先级是：

1. **显式 `OutputKind`**
2. metadata 中已有的 `output_kind`
3. fallback heuristics

关键实现：

- `backend/internal/toolkit/interface.go`
- `backend/internal/output/tool_result_content.go`

### 5.1 toolkit 侧

`toolkit.ToolResult.NormalizedOutputKind()` 的规则是：

1. 若 `ToolResult.OutputKind` 已设置且合法，直接使用
2. 否则看 `Metadata` 中是否已有 `output_kind`
3. 若存在 `Data` 或 `MIMEType`，判为 `binary`
4. 若 `Content` 为空，判为 `empty`
5. 否则默认 `text`

### 5.2 模型回传侧

`RenderToolResultContentForModel(...)` 的规则是：

1. 外部 MCP 结果优先保留 full content
2. 若 envelope 中已有 `output_kind`，按该类型处理
3. 内置 / toolkit 的 `text-like` 结果先构造 full content；若体量超过 history 安全预算，则改为“头 + 尾 + 截断标记”的格式化文本
4. 若内容本身是 `string` / `[]byte` / `fmt.Stringer`，也按同样规则视作 text-like
5. 若不是 text-like 但有 envelope summary，优先 summary
6. 否则再回退到 full content

## 6. 会不会误判

会，尤其在工具没有明确声明 `OutputKind` 时。

典型误判场景：

### 6.1 结构化数据被提前 stringify

如果工具把一个 JSON 对象自己序列化成字符串，但又没有声明 `output_kind=structured`，系统会把它当成 `text`。

结果：

- 模型会看到全文
- CLI 也可能按纯文本预览
- reducer 不一定能知道它原本是结构化结果

### 6.2 文本看起来像 JSON，但本质仍是文本

例如命令输出一段 JSON 日志，这时把它判成 `text` 其实也未必错，因为用户和模型很多时候就是要看原文。

### 6.3 MCP 返回 metadata 不完整

外部 MCP 工具若未提供 `output_kind`，host 只能根据当前内容形式和适配结果处理。

## 7. 当前采取的缓解方式

### 7.1 内置 toolkit 工具逐个补全 `OutputKind`

当前内置 toolkit 工具已经逐个显式设置 `OutputKind`，例如：

- `backend/internal/toolkit/tools/apply_patch.go`
- `backend/internal/toolkit/tools/bash.go`
- `backend/internal/toolkit/tools/view.go`
- `backend/internal/toolkit/tools/write.go`

这能显著减少 fallback 猜测。

### 7.2 外部 MCP 工具优先保留完整输出

当前 `RenderToolResultContentForModel(...)` 对外部 MCP 结果采用 full content 优先策略，避免 host 在不了解远端语义的情况下过度压缩。

实现点：

- `backend/internal/output/tool_result_content.go`

### 7.3 metadata 端到端透传

当前链路已经把 metadata 收口到以下几层：

- `backend/internal/tools/manager.go`
- `backend/cmd/aicli/functions/function.go`
- `backend/cmd/aicli/commands/function_catalog.go`
- `backend/internal/chatcore/provider_loop.go`
- `backend/cmd/aicli/commands/chat_core.go`
- `backend/cmd/aicli/commands/message.go`

包括：

- tool history message
- `ChatResult.ToolExecutions`
- debug summary
- `tool_result` 日志
- success / error 路径

## 8. 为什么 CLI 仍然要截断

CLI 不截断会带来两个直接问题：

1. shell / grep / MCP 大结果会把交互界面淹没
2. 用户很难定位“这次工具到底做了什么”

所以 CLI 的策略是：

- 向人展示：紧凑 preview
- 向模型回传：按 `output_kind` 和 envelope 策略处理

实现点：

- `backend/cmd/aicli/commands/chat_tool_rendering.go`
- `backend/cmd/aicli/commands/chat_runtime_events.go`
- `backend/cmd/aicli/commands/message.go`

其中常见行为：

- `truncateOutputPreview(...)` 只用于 preview / summary
- `renderCompactToolRequestedWithSource(...)` / `renderCompactToolCompletedWithSource(...)` 只用于 CLI 时间线
- `[meta]` / `[mcp]` / `[broker]` 只用于来源提示

## 9. 当前链路分层

建议把这条链路理解成 4 层：

### 9.1 执行层

工具真实执行，产出原始结果：

- toolkit tool
- MCP tool
- broker tool

对于 shell / exec 一类高风险文本工具，执行层会先做：

- 受控聚合 stdout/stderr
- 超限后保留头尾并追加 capture-limit 标记
- 继续 drain 子进程输出，避免因为停止读取而卡住

### 9.2 规范化层

统一补齐：

- `tool_source`
- `output_kind`
- 字符串化输出

主要在：

- `backend/internal/tools/manager.go`
- `backend/internal/toolkit/interface.go`

### 9.3 模型回传层

决定 `tool_result` 真正写回模型的是什么：

- full raw text
- history-safe truncated text
- envelope summary

主要在：

- `backend/internal/output/tool_result_content.go`
- `backend/internal/chatcore/provider_loop.go`

### 9.4 CLI / 日志层

决定人类看到什么：

- 紧凑 preview
- source label
- debug summary
- structured log payload

主要在：

- `backend/cmd/aicli/commands/chat_tool_rendering.go`
- `backend/cmd/aicli/commands/chat_runtime_events.go`
- `backend/cmd/aicli/commands/chat_core.go`
- `backend/cmd/aicli/commands/message.go`

### 9.5 数据流示意

```text
toolkit / MCP / broker tool
          |
          v
+------------------------------+
| execution result             |
| - content / error            |
| - metadata                   |
| - output_kind                |
+------------------------------+
          |
          v
+------------------------------+
| normalization                |
| - fill tool_source           |
| - fill output_kind           |
| - stringify if needed        |
+------------------------------+
          |
          +------------------------------+
          |                              |
          v                              v
+---------------------------+   +---------------------------+
| model return path         |   | CLI / log path            |
| - full text if small text |   | - compact preview         |
| - truncate head/tail if   |   | - source label            |
|   oversized internal text |   | - debug summary           |
| - envelope if structured  |   |                           |
| - external MCP keep full  |   |                           |
+---------------------------+   +---------------------------+
          |                              |
          v                              v
  tool_result back to LLM         terminal / debug log / json log
```

可以把它理解成一句话：

- **先规范化原始结果，再分叉到“模型回传”和“CLI 展示”两条链路。**

其中最重要的约束是：

- **CLI 的截断只影响右侧展示链路；左侧是否截断，由独立的模型上下文治理策略决定。**

## 10. 当前实现结论

截至这次修复，当前契约是：

1. **所有工具调用返回给 LLM 时，不再因为 CLI 展示被统一截断。**
2. **内置 / toolkit 的超大文本输出，在进入 LLM history 前会做独立的 history-safe truncation。**
3. **shell / exec 的原始聚合输出会在执行层应用 capture limit，避免还没进入 history 就先失控。**
4. **同一用户轮次内如果连续多次工具重放导致 active turn 过大，会自动把更早的 replay 压成摘要，仅保留最新一段 raw replay；这套压缩现在同时覆盖 `agent.MessageBuilder` 与 shared `chatcore`，并支持 byte / token 双预算。**
5. **shared chat 路径在进入 tool loop 前，也会基于 model capability 尝试一次 pre-turn auto compaction；如果整段会话历史已经明显逼近上下文窗口，会先把更早 turns 压成 summary，再继续当前请求。shared renderer 也会输出对应的 `[context] shared auto-compact ...` 提示，方便观察是否已提前压缩。**
6. **在真正发起 LLM 请求前，还会执行一次 prompt preflight budget gate。若 prompt token 仍超预算，会优先尝试基于 token 的 active turn replay 压缩；若压缩后仍超限，则直接在本地失败，不再把超大请求发给模型。预算推导已开始结合 provider / model capability（`MaxContextTokens`、`AutoCompactRatio`、`AutoCompactTokenLimit`）与 provider fallback context limit。**
7. **preflight fail-fast 现在会返回结构化的 `PromptPreflightError`，上层 actor / shared chat CLI 可以直接消费其中的失败码、建议动作、provider/model/budget source 等信息；runtime timeline 也能基于 `session_end` payload 渲染更清晰的本地拦截提示，而不再只能靠字符串匹配。**
8. **如果 preflight 在“已完成 active-turn compaction 但仍超预算”后失败，错误对象可以携带 replacement history 作为恢复参考；prompt-only 压缩只影响当次发送视图，不再默认回写持久化 session history。只有 session-level compact recovery 或显式 `/compact` 成功后，才会替换并持久化 session history。**
9. **CLI 仍然可以独立截断。**
10. **`tool_source` / `output_kind` 已在 legacy `aicli` 路径和 shared `chatcore` 路径中端到端透传。**
11. **success / error 两条路径都保留 metadata。**
12. **内置 toolkit 工具显式声明 `OutputKind`，减少误判。**
13. **外部 MCP 工具当前优先保留完整输出。**
14. **`aicli` 状态栏的 `ctx used` 表示已发送给 LLM API 的 prompt context 大小，由 provider usage 确认；request-start 本地预估不会提前显示，普通请求只允许单调递增，compact / reset / new / clear 才允许降低。**

## 11. 后续建议

还有几件事值得继续推进：

### 11.1 减少 string-only 接口

只要中间层还要求 `(string, error)`，结构化结果就仍然需要 JSON 文本化。

长期更合理的方向是：

- 中间层直接传 `content + metadata + error`
- 最终只在最外层决定是否 stringify

### 11.2 把 `output_kind` 当成必填契约

当前 fallback heuristics 已经足够稳，但它本质上仍是兜底。

更强的约束是：

- 内置工具必须显式声明
- broker 返回必须显式补齐
- MCP 适配层尽量保留远端 metadata，不在 host 侧猜

### 11.3 继续增强 preflight gate 的 provider 感知与恢复策略

当前实现里，preflight gate 不再只看 context manager 预算；它还会综合：

- context manager 传入的有效 prompt 预算
- 当前剩余 token budget
- runtime 侧的通用 token 计数
- provider / model capability 的 `MaxContextTokens`
- capability 上的 `AutoCompactRatio` / `AutoCompactTokenLimit`
- provider `GetCapabilities()` 暴露的 fallback context limit

并且，preflight 失败事件现在已经附带结构化原因，例如：

- `failure_reason_code`
- `failure_reason_detail`
- `active_turn_message_count`
- `latest_replay_block_message_count`
- `can_retry_after_compaction`
- `suggested_action`
- `replacement_history_available`
- `replacement_history_message_count`
- `replacement_history_applied`

同时，agent 主链路与 shared `chatcore` tool loop 现在都会返回结构化 `PromptPreflightError`，因此：

- actor `session_end` payload 可以携带结构化错误字段
- skills API `/api/agent/chat` 的错误响应现在也会直接返回 `error_type=prompt_preflight`、`failure_reason_code`、`replacement_history_*` 等结构化字段
- skills API 在 prompt preflight fail-fast 时会回传 `trace_id`，并补发一条带结构化 payload 的 `session_end` runtime event，便于和 runtime trace / timeline 对齐
- team / orchestrator 链路现在也会把 teammate session 上浮的 `prompt_preflight` 失败元数据带到 `team.task.failed` 事件里，便于在 runtime trace / timeline 中直接定位“某个任务为什么在本地被上下文预算拦截”
- lead planning / replan 失败现在也会把 lead session 的 `trace_id`、`error_type=prompt_preflight` 和恢复元数据上浮到 `team.plan.failed`、`team.plan.replan_failed` 以及 blocked-task 的 `replan_*` 字段中，便于继续追踪“不是执行任务失败，而是自动规划/重规划阶段被上下文预算拦截”
- team final summary 路径现在也不会再静默吞掉 lead summary 的 `prompt_preflight` / session 失败：terminal reconciliation 会先发 `team.summary.failed`，再在可回退时继续发带 `summary_source=fallback`、`fallback_reason`、`trace_id`、`error_type` 和恢复元数据的 `team.summary`；`GET /api/runtime/teams/{id}/summary/final` 与 `team.summary.generated` 也会返回同样的结构化字段，CLI runtime timeline 现在也会把 `team.summary.generated` 以和 `team.summary` 一致的方式 humanize 展示出来
- runtime trace 聚合层现在还会把这些信号汇总进 `recovery` 视图：包括 `prompt_preflight_events`、失败码分布、`replacement_history_*` 次数，以及 `team.summary(.generated)` 的 fallback 次数/原因；因此 `GET /api/runtime/traces/{trace_id}`、`GET /api/runtime/traces`、`GET /api/runtime/traces/stats` 都可以直接用于更高层诊断，而不必手工逐条扫事件
- 其中 `GET /api/runtime/traces` 现在也会在顶层返回一份针对当前返回 trace 列表的聚合 `recovery` 摘要，便于列表页/后台面板直接展示“最近这些 traces 是否集中发生 prompt preflight / summary fallback”
- shared chat / actor 两条 CLI 路径都可以直接把失败原因和恢复建议 humanize 给用户
- 当“压缩过但仍超限”时，prompt-only replacement history 只作为恢复参考；如果需要让后续轮次从紧凑 history 起步，应通过 session-level compact recovery 或显式 `/compact` 完成一次真正的 history replacement
- 上层不必再依赖 `strings.Contains(err.Error(), "...")` 这类脆弱分支
- actor / skills 侧原有的 `session_compact_*` 事件也已可在 runtime timeline 中直接看见 started/completed/skipped/failed

后续还可以继续增强：

- 把 prompt / completion / reasoning 预算拆得更明确
- 针对不同失败码提供自动恢复策略，而不只是 fail-fast
- 在 shared chat / actor / team 等更高层把这些结构化失败原因做成统一恢复动作
