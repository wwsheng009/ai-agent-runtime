# `aicli` 工具输出契约

## 1. 背景

这份文档说明 `aicli` 当前关于工具输出的几个核心约束：

1. **返回给 LLM 的工具结果不应因为 CLI 展示而被截断。**
2. **CLI 可以为了可读性单独做摘要和折叠。**
3. **工具输出的“来源”和“类型”必须显式携带，而不是在链路后面猜。**
4. **结构化输出可以被序列化成 JSON 文本传输，但这不等于要把它裁剪成摘要。**

这个约束主要为了解决两类问题：

- 用户在 CLI 上看到的是紧凑摘要，但模型需要看到完整原文时，过去容易被同一套截断逻辑误伤。
- 工具输出经过 toolkit / MCP / broker / shared chat loop 多层转发后，`tool_source` 和 `output_kind` 容易在中途丢失。

## 2. 设计目标

### 2.1 模型视角

- 对模型来说，`tool_result` 是**上下文事实输入**，不能因为“终端不好看”而丢信息。
- `text` 输出默认保留全文。
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

- `text`：原样文本最重要，应优先保留全文
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
3. 若内容本身是 `string` / `[]byte` / `fmt.Stringer`，视作 text-like
4. 若不是 text-like 但有 envelope summary，优先 summary
5. 否则再回退到 full content

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
| - full text if text       |   | - compact preview         |
| - envelope if structured  |   | - source label            |
| - external MCP keep full  |   | - debug summary           |
+---------------------------+   +---------------------------+
          |                              |
          v                              v
  tool_result back to LLM         terminal / debug log / json log
```

可以把它理解成一句话：

- **先规范化原始结果，再分叉到“模型回传”和“CLI 展示”两条链路。**

其中最重要的约束是：

- **CLI 的截断只影响右侧展示链路，不影响左侧回传给 LLM 的内容。**

## 10. 当前实现结论

截至这次修复，当前契约是：

1. **所有工具调用返回给 LLM 时，不再因为 CLI 展示被统一截断。**
2. **CLI 仍然可以独立截断。**
3. **`tool_source` / `output_kind` 已在 legacy `aicli` 路径和 shared `chatcore` 路径中端到端透传。**
4. **success / error 两条路径都保留 metadata。**
5. **内置 toolkit 工具显式声明 `OutputKind`，减少误判。**
6. **外部 MCP 工具当前优先保留完整输出。**

## 11. 后续建议

还有两件事值得继续推进：

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
