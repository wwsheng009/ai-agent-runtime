# aicli 聊天统一渲染链路梳理：事件流标识与块缩进

日期：2026-08-08
范围：`backend/cmd/aicli`（交互式 TUI 会话渲染）
结论：两条渲染路径最终汇聚到**同一套 history cell 模型**（`commands/chat_history_cell.go`），
assistant 正文事件流此前缺失「首行标识 + 块缩进」，本次修复补齐；unified 渲染路径的
Scene 侧另有独立呈现层，列为后续项。

---

## 1. 目标（参考格式）

参考目标渲染形态：

```
> 用户输入
• Running ls path=docs
  README.md
• Completed ls path=docs in 12ms
• assistant 正文第一行……
  续行缩进对齐（2 列，与 "• " 标识同宽）
```

要求：**每一事件流前面有一个标识**、**整个事件块有缩进**。

## 2. 渲染链路总览

### 2.1 统一渲染路径（unified plan，主链路）

```
runtime 事件 (runtimeevents.Event)
  → ui/render/encoding.EventEncoder        encoder.go（tool 事件生成 "• Running/• Completed" 显示头）
  → RenderModel                            render/encoding/model.go
  → ui/scene ChangeSetMapper              scene/from_changeset.go（映射为 TranscriptCell / CellKind）
  → TuiScene                              scene/scene.go
  → TerminalSessionPresenter              ui/terminal_session_presenter.go（写 scrollback / ActiveBand）
  → history 提交                          history_effect_planner / history_effect_queue
```

### 2.2 兼容/遗留路径（桥接 coordinator）

```
RuntimeEventBridge → commands/chatInteractionCoordinator（chat_interaction.go）
  → streaming 直写（writeIndentedStreamingDeltaLocked）
  → 终态提交 history cell（chat_history_cell.go / commitHistoryCellLocked）
  → 线性回放（chat_transcript_renderer.go，经 Interaction.RenderReplayedUserInput）
```

### 2.3 交汇点

两条路径最终都使用 `assistantStreamCell`/`historyCell` 的 `DisplayLines` 投影
（`chat_history_cell.go`）与 `ui.FormatAssistantRendered`/`ui.Message` chrome
（`ui/message.go`），因此 chrome 修复放在这两处即同时覆盖两条路径的**终态提交**；
流式增量另走 `chat_interaction.go` 的 streaming 写入。

## 3. 事件流样式盘点（修复前）

| 事件流 | 首行标识 | 块缩进 | 实现点（修复前） |
| --- | --- | --- | --- |
| 用户消息 | `> ` | 2 列 | `messageChrome()` MessageUser |
| 工具运行/完成/失败 | `• Running` / `• Completed` / `• Failed` | 2 列 | `encoder.go` 显示头；`chat_tool_rendering.go:389` |
| 推理块 | `── reasoning ──` 分隔线 | 4 列（`AssistantContentIndent()+"  "`） | `chat_interaction.go` |
| **assistant 正文** | **无** | **无** | `messageChrome()` MessageAssistant 分支为空；`AssistantContentIndent()` 硬编码返回 `""` |
| 系统消息 | `ℹ️ ` | 有 | `messageChrome()` MessageSystem |
| 错误 | `❌ ` | 有 | `messageChrome()` MessageError |

## 4. 差距根因

1. `ui/message.go` `messageChrome()` 的 `MessageAssistant` 分支没有 `plainPrefix`
   （用户/工具/系统/错误都有），assistant 线性消息无标识。
2. `AssistantContentIndent()` 是**设计钩子但从未启用**：直接 `return ""`，
   `message_test.go` 甚至断言「assistant 无前缀时缩进应为空」。
3. 流式增量 `writeIndentedStreamingDeltaLocked` 首行与续行使用同一 indent，
   首行不会带标识。
4. `assistantStreamCell.DisplayLines` 纯文本路径只走 `FormatAssistantRendered`
   （缩进为空时代理为空），没有块级 chrome。

## 5. 本次修复

### 5.1 `ui/message.go`（chrome 单一来源）

- `MessageAssistant` 分支增加 `plainPrefix = AssistantStreamMarker()`，
  并染色（`prefixRole`/`colorPrefix`），对齐工具块标识的视觉权重。
- `AssistantContentIndent()` = `"• "` 的显示宽度（2 列），续行缩进与标识同宽。
- 新增：
  - `AssistantStreamMarker() string` → `"• "`（统一标识常量）
  - `FormatAssistantBlockChrome(content)` → 首行 `"• "` + 后续行缩进
- 决策：assistant 标识与 `ShowIcon` 开关解耦（标识即事件流身份，恒显）。

### 5.2 `commands/chat_interaction.go`（流式增量与残差）

- `writeIndentedStreamingDeltaLocked(delta, firstLine, indent, ...)`：
  新增 `firstLine` 参数——首行写 `"• "`，后续行写 indent；推理块传 `""`
  保持原样（`AssistantContentIndent()+"  "` 全行缩进）。
- 5 处调用点：assistant 首激活、增量续写、buffered 补渲、
  `CompleteAssistantResponse` 文本后缀均传 `AssistantStreamMarker()`；
  推理两处传 `""`。
- 残差路径：续写**已打开的行**时（`!streamTrailingLF`）去掉块缩进
  （`stripAssistantContinuationIndent`），避免行中空档
  （`"This is " + "  bold"` → `"This is   bold"`）。
- `buildRenderedAssistantChunk`：空白源（纯终止符）返回空 chunk，
  避免被 `IndentAssistantContent` 变成缩进空行。

### 5.3 `commands/chat_history_cell.go`（终态 cell）

- `assistantStreamCell.DisplayLines` 纯文本路径改用
  `ui.FormatAssistantBlockChrome`（首行 `"• "` + 续行缩进）。
- markdown 路径保持原结构：仅 `FormatAssistantRendered` 统一缩进，
  不加 `"• "`（markdown 自带列表/引用层级，加 bullet 会破坏排版）。

### 5.4 测试更新

- `ui/message_test.go`：`MultilineHasNoPrefix` → `MarkerIsIndependentOfIconToggle`；
  表格测试新增 assistant 行；缩进不变式改为「缩进 == 标识宽度」。
- `commands/chat_interaction_test.go`：3 处断言改用 `AssistantStreamMarker()`。

## 6. 修复后效果

```
> 用户输入
• Running ls path=docs
  README.md
• Completed ls path=docs in 12ms
• 正文第一行……
  续行（2 列缩进，与标识同宽）
```

- 推理块仍为 `── reasoning ──` + 4 列缩进（未改动，风格自持）。
- markdown 正文块整体 2 列缩进，无 bullet（文档化决策）。

## 7. 测试验证

- `go test ./cmd/aicli/ui/` ✅
- `go test ./cmd/aicli/commands/` ✅
- `go test ./cmd/aicli/...`（全量，后台跑批）✅

## 8. 剩余差异与后续项（未在本轮处理）

1. **Scene 侧直接渲染路径的 chrome 核验**：unified 模式下
   `app_screen_layout.go` / `app_render_frame.go`（KindAssistant 分支）与
   `transcript_pager.go` 直接消费 Scene cell 源；若其绕过
   `ui.Message`/`FormatAssistantBlockChrome`，scrollback 回放时 assistant
   块仍无标识。建议：让 presenter 对 KindAssistant 提交统一走
   `FormatAssistantBlockChrome`（或 Scene 快照时注入首行标识）。
2. **ActiveBand 标题**：`ActiveStreamController.BeginAssistant("assistant")`
   的 band 标题为字面 "assistant"；可评估是否改为 `"• assistant"` 或隐藏标题、
   仅靠正文标识，与滚动回看风格一致。
3. **推理块标识**：当前推理块无 bullet（仅分隔线 + 缩进），若要与
   「每一事件流都有标识」严格对齐，可在 `── reasoning ──` 外增加 `• ` 前缀
   （需同步 `chatToolDivider` 相关测试）。
4. **"Ran" 文案**：部分工具终态使用 `• Ran`（本地编排集成测试
   `chat_local_orchestration_integration_test.go`），与 `• Completed` 并存；
   若目标格式统一为 `Completed`，需一并收敛。
5. **常量收敛**：`"• "` 现集中于 `ui.AssistantStreamMarker()`；
   工具链 chrome（`chat_tool_rendering.go`）仍各自内联 `"• "` 前缀，
   可后续统一引用（低风险，纯重构）。

## 9. 关键文件索引

| 文件 | 职责 |
| --- | --- |
| `ui/render/encoding/encoder.go` | 事件 → RenderModel（工具显示头） |
| `ui/scene/from_changeset.go` | RenderModel → Scene cell |
| `ui/scene/scene.go` | TuiScene 快照模型 |
| `ui/terminal_session_presenter.go` | Scene → scrollback/ActiveBand |
| `ui/message.go` | 消息 chrome（本次修复核心） |
| `commands/chat_interaction.go` | 遗留 coordinator 流式写入（本次修复核心） |
| `commands/chat_history_cell.go` | 历史 cell 投影（本次修复核心） |
| `commands/chat_tool_rendering.go` | 工具链 chrome |
