# aicli UI 重构计划：参考 Codex TUI 的分阶段高级能力方案

状态: **implementing**

更新时间: **2026-05-01**

## 当前实施进度

截至 2026-05-01，Phase 0 和 Phase 1 的最小闭环已经开始落地：

- 已新增终端能力探测和 `FixedBottomSurface`，在支持 ANSI scroll region 的交互终端中预留底部状态行。
- 已修复 `StatusBar`、prompt 清理和 line editor redraw 的跨区域清屏问题，避免使用清到屏幕底部的 `ESC[J` 破坏固定底部区域。
- 已将 `chatInteractionCoordinator` 接入 fixed-bottom surface，并在 Thinking、Streaming、Ready、Retrying、模型切换、消息数和 token 变化时刷新状态行。
- 已将 MCP/system status output、HTTP retry notice、debug output、运行期 priority prompt 的直接输出路径接入 surface-aware 输出入口。
- 已新增 `ui.ComposerState`，让 bracketed paste 和 plain burst flush 通过统一 paste handler；大粘贴显示 `[Pasted Content N chars]` 占位符，提交时展开真实内容。
- 已把 WSL/VS Code terminal 的 plain paste burst settle 窗口放宽，并让 Windows 交互输入收敛到同一 raw editor / paste burst 路径，避免 Windows 继续单独走行缓冲。
- 已实现 `FixedBottomSurface` 的 popup 层和 composer 预览层，底部现在能分别承载 popup / composer / status 三类临时视图，不再写入 transcript；popup 行数会按终端高度裁剪，避免压没输出区。
- 已把 `Paste draft N lines` 从状态栏提示升级为 bottom pane 里的 pending paste preview，并保持在底部临时视图内渲染。
- 已把 `/model` 迁移到 popup-first 选择器：模型选择和 reasoning_effort 选择优先走 popup，legacy stdout 菜单仅作为降级路径。
- 已将 modal priority prompt 的读取切到 transient line/prompt 路径，避免选择数字、审批回答和问题回答回写到普通聊天 history 或共享输入流。
- 已补充 transient prompt 回归测试，覆盖 `ReadTransientLine`、popup priority prompt 和共享 `InputReader` 隔离。
- 已补充终端 profile matrix snapshot 测试，覆盖 Windows Terminal、PowerShell、WSL Ubuntu、Linux terminal、VS Code terminal、legacy console 和 Zellij 的启用/回退分支。
- 保留非交互、JSON、pipe、legacy TUI 的降级路径，不在这些模式启用固定底部 UI。

仍需继续完成：

- 引入真正的 bottom pane/composer 输入栈，把 composer preview 进一步接到真实编辑器和 modal stack。
- 补更真实的终端级集成测试或手工 smoke 测试，覆盖实际 PTY/ConPTY 环境，而不只是 profile matrix snapshot。

## 近期新增任务与实现分析

### 1. 弹出框组件渲染不占用信息流

目标：弹出框、选择器和临时提示不应写入聊天 transcript，也不应通过普通 `fmt.Println` 进入终端 scrollback。它们应作为 bottom pane 的临时视图渲染，关闭后由下一帧重绘覆盖，不污染信息流。

实现原则：

- Popup 属于 UI state，不属于 chat history/message stream。
- 交互模式下 popup 渲染必须走统一 surface，而不是业务函数直接写 stdout。
- Popup 占用 bottom pane 或 modal overlay 的保留区域，通过 scroll region/viewport 与滚动信息流隔离。
- 普通输出写入前必须调用 surface 的 output cursor 恢复逻辑，确保不会写到底部 popup 区域。
- popup 关闭时只清理 popup 自己占用的行并恢复底部状态/composer，不使用 `ESC[J` 清到屏幕底部。
- 非交互、JSON、pipe、legacy terminal 必须降级为当前线性选择器输出。

短期实现方案：

- 在 `FixedBottomSurface` 上增加动态 bottom rows 能力，例如 `ShowPopup(lines []string, prompt string)` 和 `ClearPopup()`。
- 当 popup 打开时，将 `bottomRows` 从 1 行状态栏扩展为 `popupHeight + statusRows`，重设 scroll region 为 `1..height-bottomRows`。
- popup 内容渲染在底部保留区域内，状态行仍位于最底部或 popup footer 行。
- `BeginOutput()` 继续保证聊天输出只写入 scroll region，不覆盖 popup。
- popup 输入读取先复用 `chatInteractiveReadPriorityLine`，后续再迁移到真正 composer key event。
- popup renderer 只接收状态和宽度，返回字符串行数组，便于单元测试。

中期实现方案：

- 引入 `BottomPaneView` stack，参考 Codex 的 bottom pane modal/popup 模型。
- `Composer`、`StatusLine`、`Footer`、`PendingPastePreview`、`ApprovalOverlay`、`ModelSelectorPopup` 都是 bottom pane renderable。
- 每次状态变化只更新 UI state 并 request redraw，不由业务路径直接移动光标。
- 在 Phase 4 frame renderer 落地后，popup 由 diff buffer 增量刷新，避免闪烁和重复输出。

### 2. `/model` 功能改为弹出框

当前问题：

- `/model` 的模型列表、reasoning 选择和提示语直接写 stdout，会进入 scrollback。
- 在 fixed-bottom surface 启用后，直接输出虽然可通过 `BeginOutput()` 降低覆盖风险，但仍不是真正 popup。
- 模型选择属于临时交互状态，不应该成为聊天信息流的一部分。

目标行为：

- 用户输入 `/model` 后，底部打开模型选择 popup。
- popup 显示当前模型、可选模型列表、当前项标记、输入提示和错误提示。
- 用户可输入编号、完整模型名或自定义模型名。
- 空 Enter 保持当前模型。
- 选择模型后，如果该模型有 reasoning_effort 限制，再打开 reasoning_effort popup。
- 选择完成后关闭 popup，只刷新状态栏，不向信息流打印“当前模型”列表。
- 如果 terminal 不支持 fixed-bottom popup，则保留当前线性输出 fallback。

建议数据结构：

- `ModelSelectorState`：包含 `Title`、`CurrentModel`、`Options`、`SelectedIndex`、`AllowCustom`、`Prompt`、`Error`。
- `ReasoningSelectorState`：包含 `CurrentEffort`、`Options`、`DefaultOption`、`AllowClear`、`Prompt`、`Error`。
- `PopupResult`：包含 `SubmittedText`、`Cancelled`、`NeedsRedraw`。

建议改造路径：

- 将 `promptRuntimeModelSelection(session)` 拆分为纯渲染函数和输入解析函数。
- 新增 `renderModelSelectorPopup(state,width) []string`，单元测试覆盖宽度截断、当前项标记、自定义提示。
- 新增 `readModelSelectorPopup(session,state)`，优先使用 `session.Surface` 渲染 popup；不可用时回落到当前 stdout 选择器。
- `handleModelCommand` 在交互模式下优先走 popup 路径；`noInteractive` 和 JSON 保持现有行为。
- `printRuntimeModelState` 在 popup 成功后不再强制进入信息流，改为 `Interaction.RefreshStatus("")`；legacy fallback 仍打印摘要。

验收标准：

- `/model` popup 打开和关闭不会在聊天历史 scrollback 留下模型列表。
- popup 打开时 assistant streaming、MCP/status 异步输出不会覆盖底部区域。
- 空 Enter、编号、自定义模型名、无效编号重试都能工作。
- 模型切换后状态栏立即显示新模型。
- legacy terminal、NoInteractive、JSON 输出行为不变。

### 3. PasteBurst 后续任务

当前落地状态：

1. 完整 Codex 风格 `PasteBurst` 状态机已经落地，并接入 `InputBox` 的逐键编辑器主路径。
2. Windows 交互输入已经收敛到同一 raw editor / paste burst 路径，和 Unix/WSL 共享同一套粘贴处理逻辑。
3. `Paste draft N lines` 已从状态栏提示升级为 bottom pane 里的 pending paste preview。
4. 仍需补充真实终端级集成测试或快照测试，覆盖 Windows Terminal、PowerShell、WSL Ubuntu、Linux terminal、VS Code terminal。

## 背景

`aicli` 当前交互 UI 的核心仍是线性终端输出：聊天内容、异步提示、流式输出、输入提示符、状态信息都主要通过 `fmt.Print/Fprintln` 或 `chatInteractionCoordinator` 写到 stdout。这个模型实现简单，但在高级交互场景下存在结构性限制：

- 普通输出、输入提示和状态栏争用同一片终端区域，无法天然保证“底部固定区域不随滚动刷新”。
- 粘贴、多行输入、流式输出和异步团队事件同时发生时，缺少统一的 UI 状态机来决定内容是“编辑区文本”还是“已提交消息”。
- 当前 `ui.StatusBar`、`ui.Layout` 已经提供了局部组件，但它们没有接管所有终端写入，也没有 scroll region、viewport、diff buffer 或统一帧渲染，因此只能做“附加打印”，不能提供稳定的固定 UI。
- Windows、WSL、Linux、tmux、Zellij、VS Code terminal 对输入事件、换行、光标控制和粘贴事件的行为不同；如果继续以分散 stdout 输出修补，会持续出现平台差异。

本计划的目标是以 Codex 的 UI 设计逻辑为参考，设计一条适合本项目 Go 代码库逐步落地的重构路线。短期先解决固定底部状态栏和输入区域稳定性，中期引入更可靠的 composer、状态面、粘贴状态机和弹层，长期迁移到统一帧渲染模型。

## 目标

1. 建立统一 UI 输出所有权：交互模式下所有可见终端输出必须经过一个 UI surface/coordinator，禁止业务路径直接写 stdout 破坏界面。
2. 实现底部固定区域：状态栏、输入框、footer/help 不随聊天输出滚动，普通内容只进入滚动区域或历史 viewport。
3. 修复并固化跨平台输入模型：Windows、Linux、WSL 都能正确处理多行粘贴、bracketed paste、paste burst、回车提交和 Shift+Enter 换行。
4. 引入 Codex 风格高级能力：状态面、终端标题、任务状态、队列预览、审批弹层、命令弹层、文件/技能/插件提及、外部编辑器入口等。
5. 提供可回归测试策略：单元测试覆盖状态机和布局计算，集成/快照测试覆盖终端渲染，手工矩阵覆盖主流终端环境。

## 非目标

- 不直接把 Codex Rust TUI 代码移植到 Go；本项目应提取设计原则和交互模型。
- 不在第一阶段强制引入全屏 alternate screen。Codex 的 inline viewport 能保留 shell scrollback，本项目也应优先保留这个体验。
- 不在非交互、JSON 输出、pipe 输出中启用复杂 UI；这些模式必须保持脚本友好。
- 不把状态栏简单实现为“每次输出后再打印一行”。这无法解决滚动、异步输出、输入提示覆盖和终端 resize。

## 参考范围

本计划参考以下本项目文件：

- `backend/cmd/aicli/commands/chat_interaction.go`
- `backend/cmd/aicli/commands/chat.go`
- `backend/cmd/aicli/commands/chat_input_queue.go`
- `backend/cmd/aicli/commands/chat_send.go`
- `backend/cmd/aicli/commands/chat_setup.go`
- `backend/cmd/aicli/ui/statusbar.go`
- `backend/cmd/aicli/ui/layout.go`
- `backend/cmd/aicli/ui/terminal.go`
- `backend/cmd/aicli/ui/inputbox_editor.go`
- `backend/cmd/aicli/ui/inputbox_editor_windows.go`
- `backend/cmd/aicli/ui/inputbox_editor_unix.go`

参考 Codex 文件：

- `E:\projects\ai\codex\codex-rs\tui\src\tui.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\custom_terminal.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\chatwidget.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\bottom_pane\mod.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\bottom_pane\chat_composer.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\bottom_pane\footer.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\bottom_pane\paste_burst.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\bottom_pane\status_line_setup.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\chatwidget\status_surfaces.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\tui\frame_requester.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\tui\frame_rate_limiter.rs`

## Codex UI 设计逻辑与优点

Codex 的关键不是“画得更复杂”，而是把终端视为一个可重复渲染的状态投影，而不是一条不可控的 stdout 日志流。

### 1. 统一帧渲染入口

Codex 在 `tui.rs` 中通过 `Tui::draw(height, draw_fn)` 执行一次完整绘制。绘制前先计算 inline viewport 高度，必要时滚动 viewport 上方内容，再调用 terminal draw closure。

核心优点：

- 终端写入集中在单一绘制入口，减少异步输出和输入区互相覆盖。
- 每一帧都从内存状态重新渲染，不依赖“上一次终端实际显示了什么”的隐式假设。
- 可以在绘制前统一处理 resize、viewport 变化、pending history lines、Zellij 兼容和 cursor 位置。

### 2. Inline viewport，而不是简单 alternate screen

Codex 不总是进入 alternate screen。默认交互仍可保留终端 scrollback，但底部 UI 通过 inline viewport 进行控制。`update_inline_viewport` 根据底部区域期望高度调整 viewport，如果区域会超过屏幕底部，则滚动 viewport 上方内容。

核心优点：

- 用户退出后仍能看到历史输出，符合 CLI 使用习惯。
- 底部 composer/status/footer 可以像固定区域一样稳定存在。
- 在 Zellij 等不可靠支持 DECSTBM 的环境下，可以切换为 raw newline 滚动并触发 full repaint。

### 3. Diff buffer 降低闪烁和错位

Codex 的 `custom_terminal.rs` 维护 current/previous 两个 buffer，每次 draw 后计算 diff，只把变化 cell 写到终端。渲染回调必须完整绘制当前帧，terminal 层负责增量刷新。

核心优点：

- 避免全屏清空重绘导致闪烁。
- 状态行、footer、输入框只在内容变化时更新。
- cursor 位置由 frame 统一设置，不由各组件各自保存/恢复光标。

### 4. Renderable 组合模型

Codex 的 `ChatWidget::as_renderable()` 将 active cell、hook cell、bottom pane 组合成一个 `FlexRenderable`。`BottomPane::as_renderable()` 再组合 status indicator、exec footer、pending input preview、composer 或 modal view。

核心优点：

- 每个组件只声明自己的 `desired_height`、`render`、`cursor_pos`。
- 底部面板高度由实际内容决定，状态行、弹层和 composer 可以自然共存。
- 运行中消息、工具调用、审批请求、用户输入队列都进入同一布局树，不需要每条路径手工移动光标。

### 5. Bottom pane 统一管理输入和底部状态

Codex 的 bottom pane 不是单一输入框，而是底部交互容器：

- `ChatComposer` 管理文本输入、历史、粘贴、附件、命令弹层。
- `StatusIndicatorWidget` 管理任务运行状态和中断提示。
- `PendingInputPreview` 显示已排队输入、pending steer、rejected steer。
- `UnifiedExecFooter` 显示命令执行摘要。
- `BottomPaneView` stack 支持审批、配置、选择器等 modal/popup。
- `footer.rs` 负责纯渲染 footer，根据宽度折叠快捷提示、状态行和右侧上下文。

核心优点：

- 底部所有交互状态由一个容器拥有，输入和状态不会互相踩。
- modal view 可以临时替代 composer，但不丢失 composer 草稿。
- footer 是纯函数式渲染输入 `FooterProps`，便于测试和宽度适配。

### 6. 粘贴处理是状态机，不是平台 if-else

Codex 的 `ChatComposer::handle_paste` 是粘贴内容唯一入口，会统一做 CRLF 归一化、大粘贴占位符、图片路径识别、pending paste 展开和 popup 同步。对于没有可靠 bracketed paste 的终端，`paste_burst.rs` 通过时间窗口和字符计数识别快速输入流，将其转换为显式 paste。

核心优点：

- Windows 上 paste 经常以 `Char`/`Enter` 快速流进入，paste burst 能避免回车误触发提交。
- Linux/WSL 上 bracketed paste 和普通 key event 都可进入同一 `handle_paste` 路径。
- 大粘贴不会把 composer 撑爆，而是显示 `[Pasted Content N chars]`，提交时再还原真实内容。
- `\r\n`、`\r`、`\n` 在进入编辑器前归一化，避免多平台换行歧义。

### 7. 状态面和终端标题可配置

Codex 的 `status_surfaces.rs` 将 status line 和 terminal title 作为两种状态面统一刷新；`status_line_setup.rs` 定义可配置项目，包括 model、cwd、project root、git branch、run-state、context、limits、tokens、session id、thread title、task progress 等。

核心优点：

- 状态数据来源集中，避免不同 UI 区域显示不一致。
- git branch 等昂贵或异步信息有缓存和 stale result 处理。
- 配置项无效时只警告一次，避免噪音。
- terminal title 和 footer status line 共用解析结果，减少重复计算。

### 8. 事件驱动和限频刷新

Codex 通过 `FrameRequester` 请求重绘，并通过 frame rate limiter 合并频繁请求。流式输出、状态动画、paste burst flush、footer hint 超时都以事件或定时 frame 触发。

核心优点：

- 流式输出不会每个 token 都无节制刷新终端。
- 临时 hint 可以在无用户输入时自动消失。
- paste burst 可以在 idle timeout 后转换为 paste，不需要阻塞输入线程。

## aicli 当前 UI 模型分析

### 已有基础

本项目已有若干可复用基础：

- `chatInteractionCoordinator` 已经集中处理 prompt、thinking、assistant streaming、async line、error 和 prompt redraw，是收敛输出的自然入口。
- `ui.Terminal` 已有基础 ANSI 操作，如移动光标、清行、保存/恢复光标、备用屏幕。
- `ui.StatusBar` 已有状态项数据结构和渲染函数。
- `ui.Layout` 已有 chat/status/input 区域概念。
- `inputbox_editor_*` 已经开始区分 Windows/Unix 输入处理，可作为 composer 重构基础。

### 核心缺口

当前实现离 Codex 模型还缺以下层：

- 没有统一 terminal surface：仍有多处直接 `fmt.Print`、`fmt.Println`、`Fprintln(os.Stdout)` 或组件内部直接写 stdout。
- 没有真实终端高度：`Terminal.updateSize()` 当前高度仍固定为 24，`GetTerminalSize()` 也返回默认值，无法可靠计算底部区域。
- 没有 scroll region 或 viewport：`Layout.PrintMessage` 只是普通 `fmt.Println`，输出仍会推动整屏滚动。
- 没有 diff buffer：状态栏每次 render 都直接移动光标和清行，容易与流式输出交错。
- 状态栏不是 UI 状态源：`StatusBar` 存储的是局部 items，但模型、tokens、run state、session、cwd、工具执行状态没有统一聚合。
- 输入框不是 bottom pane：prompt、输入内容、状态栏、footer hint、队列提示没有同一个所有者。
- 粘贴仍属于输入读取层问题：没有把 bracketed paste、paste burst、大粘贴占位符、图片路径和提交展开统一成 composer 状态机。

结论：可以参考 Codex 实现高级 UI，但不应继续在现有 stdout 模型上堆更多 `MoveTo` 和 `ClearLine`。必须先建立“所有交互输出由 UI surface 拥有”的边界。

## 目标架构

推荐目标架构分为五层：

### 1. Terminal Driver

职责：

- 获取真实终端宽高。
- 启用/禁用 raw mode、bracketed paste、VT processing。
- 提供 cursor、clear、scroll region、alternate screen、terminal title、synchronized update 等底层能力。
- 检测 Windows Terminal、WSL、VS Code terminal、tmux、Zellij、dumb terminal 等能力差异。

建议文件：

- 新增 `backend/cmd/aicli/ui/terminal_driver.go`
- 新增 `backend/cmd/aicli/ui/terminal_driver_windows.go`
- 新增 `backend/cmd/aicli/ui/terminal_driver_unix.go`
- 改造 `backend/cmd/aicli/ui/terminal.go`

### 2. UI Surface

职责：

- 成为交互模式唯一可见输出入口。
- 维护 transcript、active assistant stream、tool cell、system notice、bottom pane state。
- 对外暴露事件式 API，例如 `AppendUserMessage`、`AppendAssistantDelta`、`SetRunState`、`SetComposerText`、`ShowApproval`、`SetStatusLine`。
- 内部决定是使用短期 scroll region 模式，还是长期 frame buffer 模式。

建议文件：

- 新增 `backend/cmd/aicli/ui/surface.go`
- 新增 `backend/cmd/aicli/ui/surface_state.go`
- 新增 `backend/cmd/aicli/ui/surface_render.go`
- 新增 `backend/cmd/aicli/ui/surface_events.go`

### 3. Render Tree

职责：

- 用组件组合替代分散打印。
- 每个组件提供高度计算和渲染。
- 支持 active cell、history cell、bottom pane、popup overlay、status/footer。

建议接口：

```go
type Renderable interface {
	DesiredHeight(width int) int
	Render(frame *Frame, area Rect)
	Cursor(area Rect) (row int, col int, ok bool)
}
```

建议核心组件：

- `TranscriptView`
- `HistoryCell`
- `ActiveAssistantCell`
- `ToolCallCell`
- `BottomPane`
- `Composer`
- `Footer`
- `StatusLine`
- `PopupView`

### 4. Frame Renderer

职责：

- 维护 current/previous buffer。
- 每帧完整渲染内存状态。
- 计算 diff 并输出最小 ANSI 更新。
- 对不支持 diff 或能力不足的终端降级为局部清行/全量重绘。

建议文件：

- 新增 `backend/cmd/aicli/ui/frame.go`
- 新增 `backend/cmd/aicli/ui/buffer.go`
- 新增 `backend/cmd/aicli/ui/diff.go`
- 新增 `backend/cmd/aicli/ui/renderable.go`

### 5. Event Loop 与 Frame Requester

职责：

- 收敛输入事件、LLM 流式事件、工具事件、timer tick、resize 事件。
- 所有状态变更只修改内存 UI state，然后请求绘制。
- 高频事件限频合并。

建议文件：

- 新增 `backend/cmd/aicli/ui/frame_requester.go`
- 新增 `backend/cmd/aicli/ui/frame_rate_limiter.go`
- 改造 `backend/cmd/aicli/commands/chat.go`
- 改造 `backend/cmd/aicli/commands/chat_interaction.go`

## 分阶段实施计划

### Phase 0：输出收敛与终端能力探测

目标：先停止 UI 继续变复杂前的“输出失控”。

实施内容：

- 在 `chatInteractionCoordinator` 中引入 `InteractiveSurface` 接口，先让现有 coordinator 作为 surface 的适配层。
- 梳理所有交互模式下直接 stdout/stderr 输出路径，改为经 coordinator/surface 输出。
- 在非交互、JSON、stdout 非 TTY 时禁用高级 UI，保持当前纯文本输出。
- 用 `golang.org/x/term.GetSize(int(os.Stdout.Fd()))` 替代固定高度 24。
- Windows 启用 Virtual Terminal Processing；失败时降级为 legacy prompt 模式。
- Unix/WSL 启用 bracketed paste 的能力探测和清理逻辑。
- 增加统一 cleanup，异常退出也恢复 cursor、scroll region、bracketed paste、raw mode。

验收标准：

- 交互输出入口列表清晰，新增 lint/测试避免在关键路径直接写 `os.Stdout`。
- `aicli` 在 pipe/JSON/CI 中行为不变。
- Windows PowerShell、Windows Terminal、WSL Ubuntu、Linux terminal 能正确获取终端尺寸。

优先改动文件：

- `backend/cmd/aicli/commands/chat_interaction.go`
- `backend/cmd/aicli/commands/chat.go`
- `backend/cmd/aicli/commands/chat_setup.go`
- `backend/cmd/aicli/ui/terminal.go`

### Phase 1：固定底部区域最小可用版

目标：先实现底部状态栏/输入区不随聊天输出滚动。

短期方案采用 ANSI scroll region，而不是立即实现完整 diff buffer：

- 将终端划分为滚动区域和底部区域。
- 普通聊天输出只写入滚动区域。
- 底部区域保留 1 到 3 行，用于 status line、footer、prompt/composer。
- 每次状态变化时保存 cursor，移动到底部区域渲染，清行后恢复 cursor。
- 退出时恢复全屏 scroll region。

关键规则：

- scroll region 初始化格式为 `ESC[{top};{bottom}r`，top 通常为 1，bottom 为 `height - bottomRows`。
- 所有普通输出写入前必须确保 cursor 位于滚动区。
- 状态栏渲染只能清理自己的行，不能使用从 cursor 到屏幕底部的清屏。
- prompt/input 和 status/footer 必须由同一个 bottom pane 计算区域，不能分别移动光标。
- resize 时重新计算 scroll region 并重绘 bottom pane。

需要改造：

- `ui.StatusBar.Render()` 不能调用 `ClearFromCursorToEnd()` 清到屏幕底部，应改为清当前行或指定区域。
- `ui.Layout.PrintMessage()` 不能继续普通 `fmt.Println`，应走 surface writer。
- `chatInteractionCoordinator.PrintPrompt/ClearPrompt/RenderAsyncLine/RenderAssistantDelta` 需要调用 bottom pane redraw，而不是直接操作 prompt 行。

兼容策略：

- 如果终端不支持 scroll region，降级到现有线性输出。
- Zellij 等兼容风险较高环境可先禁用固定底部，或采用 Codex 类似 raw newline + full repaint 策略。
- Windows legacy console 不保证 ANSI 能力，必须检测并降级；Windows Terminal/ConPTY 应支持 VT。

验收标准：

- 粘贴多行文本时，不会把状态栏推入历史。
- Assistant 流式输出时，底部状态栏仍固定。
- 异步团队事件输出时，输入区不会被插入内容打断。
- resize 后底部区域位置正确。

### Phase 2：Codex 风格 Composer 与粘贴状态机

目标：把输入、粘贴、草稿、提交从“readline 字符串”升级为 composer 状态。

实施内容：

- 新增 `ui.ComposerState`，包含当前文本、cursor、selection、history、pending paste、attachments、mode。
- `handlePaste(text)` 成为唯一粘贴入口，统一执行 `\r\n` 和 `\r` 到 `\n` 的归一化。
- 启用 bracketed paste：收到 paste start/end 时，将中间内容作为一次 paste，不把换行当提交。
- 实现 paste burst 状态机：对于 Windows 或未提供 bracketed paste 的终端，把短时间内大量 `Char`/`Enter` 事件识别为 paste。
- 实现大粘贴占位符：超过阈值的内容在 composer 显示 `[Pasted Content N chars]`，提交时展开。
- 保留图片路径识别入口：后续可参考 Codex 的 image paste path，将本地图片转成附件或 image input。
- Enter 行为明确化：普通 Enter 提交，Shift+Enter/Ctrl+J 插入换行；paste burst 活跃时 Enter 永远作为粘贴换行。

建议文件：

- 新增 `backend/cmd/aicli/ui/composer.go`
- 新增 `backend/cmd/aicli/ui/paste.go`
- 新增 `backend/cmd/aicli/ui/paste_burst.go`
- 新增 `backend/cmd/aicli/ui/composer_test.go`
- 新增 `backend/cmd/aicli/ui/paste_burst_test.go`
- 改造 `backend/cmd/aicli/ui/inputbox_editor.go`
- 改造 `backend/cmd/aicli/ui/inputbox_editor_windows.go`
- 改造 `backend/cmd/aicli/ui/inputbox_editor_unix.go`

验收标准：

- Windows 粘贴多行内容不会自动触发 LLM 调用。
- Linux/WSL 粘贴多行内容能保留换行，不出现行首错位和重复回显。
- 同一次 paste 只进入 composer 一次，不会按字符重复追加历史 prompt。
- 大粘贴显示占位符，提交 payload 保持原文。

### Phase 3：状态面、footer 和任务运行态

目标：将底部状态从固定文本升级为可配置、可测试、可扩展的状态面。

实施内容：

- 新增 `StatusSurfaceState`，统一汇聚 model、provider、cwd、project root、git branch、run state、tokens、session file、chat log、debug log、context usage、tool state。
- 新增 `StatusLineItem` 枚举或等价配置项，参考 Codex 的 `StatusLineItem`。
- 支持配置状态栏项目和顺序，例如 `model,run-state,project-name,git-branch,tokens,session-id`。
- git branch 异步查询并按 cwd 缓存，cwd 改变时丢弃旧结果。
- footer 纯渲染：输入为空显示快捷提示；任务运行中显示 queue hint；有状态行时显示上下文状态；宽度不足时按规则折叠。
- 支持 terminal title surface，可显示 app/project/run-state/task progress。
- `StartThinking/ClearThinking`、工具调用 begin/end、LLM streaming begin/end、队列变更都只更新 run state，然后请求 redraw。

建议文件：

- 新增 `backend/cmd/aicli/ui/status_surface.go`
- 新增 `backend/cmd/aicli/ui/status_line.go`
- 新增 `backend/cmd/aicli/ui/footer.go`
- 新增 `backend/cmd/aicli/ui/terminal_title.go`
- 改造 `backend/cmd/aicli/ui/statusbar.go`
- 改造 `backend/cmd/aicli/commands/chat_send.go`
- 改造 `backend/cmd/aicli/commands/chat_tool_rendering.go`
- 改造 `backend/cmd/aicli/commands/chat_session_info.go`

验收标准：

- 状态行内容由状态源生成，渲染函数可脱离终端做单元测试。
- 宽度变化时状态行能截断/折叠，不破坏输入区。
- run state 从 `Ready`、`Thinking`、`Streaming`、`Running shell`、`Waiting approval`、`Error` 中正确切换。
- terminal title 不在非交互或 unsupported terminal 中输出控制序列。

### Phase 4：统一帧渲染与 diff buffer

目标：从 scroll region 局部方案升级到 Codex 风格 frame model。

实施内容：

- 引入 `Frame` 和二维 cell buffer，支持 text、style、宽字符、ANSI style、清行。
- 每次 redraw 由 UI state 完整渲染当前 frame。
- 对比 previous/current buffer，只输出变化区域。
- cursor 由 `Composer.Cursor()` 决定，其他组件不直接移动 cursor。
- Transcript/history cell 内存化，assistant streaming 作为 active cell 原地更新。
- 工具调用、shell 输出、HTTP artifacts、reasoning blocks 渲染为不同 cell 类型。
- 实现 inline viewport：根据 `ChatWidget.DesiredHeight(width)` 决定 viewport 高度，必要时滚动历史区。
- 对 Zellij/tmux/不支持 scroll region 的终端提供 full repaint 或 legacy fallback。

建议文件：

- 新增 `backend/cmd/aicli/ui/frame.go`
- 新增 `backend/cmd/aicli/ui/cell.go`
- 新增 `backend/cmd/aicli/ui/buffer.go`
- 新增 `backend/cmd/aicli/ui/diff.go`
- 新增 `backend/cmd/aicli/ui/render_tree.go`
- 新增 `backend/cmd/aicli/ui/transcript.go`
- 新增 `backend/cmd/aicli/ui/history_cell.go`
- 新增 `backend/cmd/aicli/ui/active_cell.go`

验收标准：

- 流式输出时终端不闪烁，输入区稳定。
- 状态栏更新不会进入 scrollback。
- 同一段消息不会因 redraw 重复写入历史。
- 所有组件快照测试可验证固定宽度下的渲染结果。

### Phase 5：高级交互能力

目标：在稳定 UI surface 上逐步引入 Codex 风格高级功能。

建议能力清单：

- Slash command popup：输入 `/` 时展示命令列表、参数提示、快捷选择。
- File search popup：输入 `@` 或路径触发文件搜索和补全。
- Skill/plugin mention popup：结合本项目 skills/plugin 能力，提供可搜索提及。
- Approval overlay：shell、文件修改、外部工具权限请求在 bottom pane modal 中确认。
- Pending input preview：任务运行中输入新消息进入队列，底部显示 queued messages。
- Plan/status widget：展示 `update_plan` 进度、当前步骤和运行状态。
- Unified exec footer：显示当前 shell/tool 调用摘要，任务结束后折叠。
- External editor：复杂多行输入可打开外部编辑器，返回 composer。
- Image attachment preview：粘贴或输入图片路径时显示占位符并进入附件列表。
- Theme system：收敛 `theme.go`、`theme_render.go`，为状态、diff、tool、error、warning 定义稳定 token。
- Terminal notifications：终端失焦时任务完成发出 BEL/OSC9/desktop notification，可配置关闭。

引入顺序建议：

1. Approval overlay，因为它能减少当前权限请求与流式输出互相插入的问题。
2. Pending input preview，因为任务运行期间继续输入是高频交互。
3. Slash command popup，因为它能统一 `/model`、`/statusline`、`/theme` 等配置入口。
4. File/skill/plugin mention，因为依赖更完整的 composer 和 popup 框架。
5. External editor 和 image preview，因为涉及更多平台差异。

## 与现有代码的集成点

### chatInteractionCoordinator

当前 coordinator 应从“直接打印协调器”升级为“业务事件到 UI surface 的适配器”。

建议映射：

- `PrintPrompt()` -> `surface.BottomPane().ShowComposer()`
- `SetPromptInput(input)` -> `surface.SetComposerText(input)`
- `ClearPrompt()` -> `surface.BottomPane().ClearPromptLine()` 或 no-op，由 redraw 覆盖
- `StartThinking()` -> `surface.SetRunState(Thinking)`
- `ClearThinking()` -> `surface.SetRunState(Ready/Streaming)`
- `RenderAssistantDelta(delta)` -> `surface.AppendAssistantDelta(delta)`
- `CompleteAssistantResponse(response)` -> `surface.FinalizeActiveAssistant(response)`
- `RenderAsyncLine(line)` -> `surface.AppendSystemNotice(line)`
- `RenderError(err)` -> `surface.AppendError(err)`

### chat.go 主循环

主循环应从“读取一行字符串后立即打印/提交”逐步改为事件循环：

- 输入事件进入 composer。
- composer 返回 `Submitted`、`Queued`、`Command`、`None` 等结果。
- LLM/tool/runtime 事件进入 surface。
- 所有状态变更后通过 frame requester 请求 redraw。

### chat_input_queue.go

输入队列应和 bottom pane 打通：

- 任务运行中用户提交应进入 queue，而不是直接调用 LLM。
- bottom pane 显示 queued messages 数量和可编辑提示。
- 队列消费后刷新状态行。

### ui/statusbar.go 与 ui/layout.go

短期保留并收敛：

- `StatusBar` 可作为 Phase 1 状态行渲染器，但必须移除跨区域清屏。
- `Layout` 可保留区域计算思路，但输出必须经 surface，不再内部 `fmt.Println`。

长期替换：

- `StatusBar` 数据结构迁移为 `StatusSurfaceState` + `StatusLineRenderer`。
- `Layout` 迁移为 render tree 的 area splitter。

## 终端兼容策略

### Windows

- 启用 VT processing 后才使用 ANSI cursor、scroll region、bracketed paste。
- ConPTY 下多行 paste 可能表现为快速 key event，需要 paste burst。
- legacy console 降级为线性 prompt，不启用固定底部和高级弹层。
- 避免依赖 `cmd.exe` 处理长命令；UI 重构中任何 helper 脚本或测试命令也要遵守本仓库 Windows 命令长度限制。

### Linux

- 优先使用 raw mode + bracketed paste。
- Enter、Shift+Enter、Ctrl+J、Ctrl+D、Ctrl+C 应由统一 key event 层解析。
- 终端 resize 通过 SIGWINCH 触发 layout 重算。

### WSL

- WSL 终端本质上经 Windows terminal/ConPTY 转发，必须同时处理 Unix raw mode 和 Windows 终端粘贴行为。
- 粘贴路径可预留 Windows path 到 WSL path 归一化能力。
- CRLF 必须在 paste 入口归一化，不应进入 editor 后再处理。

### tmux 和 Zellij

- tmux 通常支持 scroll region，但 key binding 和 bracketed paste 可能被配置影响，需要能力检测和手工验证。
- Zellij 对 DECSTBM/scroll region 支持存在差异，参考 Codex：必要时用 raw newline 滚动并 full repaint，或直接降级 legacy。

### dumb terminal、pipe、CI

- 禁用固定底部、raw mode、ANSI diff、terminal title、bracketed paste。
- 保留纯文本日志输出，保证脚本可消费。

## 测试计划

### 单元测试

重点覆盖纯逻辑，不依赖真实终端：

- `StatusLineItem` 解析、排序、无效项收集、默认项。
- status line 宽度截断、分隔符、空值省略。
- footer 在不同宽度下的折叠策略。
- composer 光标移动、多行编辑、删除、历史切换。
- paste normalizer：`\r\n`、`\r`、`\n` 全部归一为 `\n`。
- paste burst：快速字符流识别为 paste，慢速输入保留为 typed chars。
- paste burst 中 Enter 作为 newline，不触发 submit。
- large paste placeholder 生成、删除、提交展开。
- terminal capability detection 的 fallback 分支。
- render buffer diff：宽字符、ANSI style、清行、cursor 位置。

### 快照测试

固定宽度和高度下渲染组件：

- 空 composer + footer。
- 有状态行 + composer。
- 任务运行中 status indicator + composer。
- 流式 assistant active cell。
- shell/tool call cell。
- pending input preview。
- approval overlay。
- slash command popup。
- 窄屏状态行折叠。

### 集成测试

可用 pseudo terminal 或 expect 类工具验证：

- 启动 aicli 后底部状态栏不进入历史输出。
- Assistant streaming 时输入区位置稳定。
- resize 后底部区域重新定位。
- bracketed paste 多行不会提交。
- 非 bracketed paste burst 多行不会提交。
- Ctrl+C 清理后恢复 cursor、scroll region、bracketed paste。
- JSON/pipe 输出无 ANSI 控制序列。

### 手工矩阵

必须覆盖：

- Windows Terminal + PowerShell
- Windows Terminal + `cmd.exe`
- Windows Terminal + WSL Ubuntu
- VS Code integrated terminal + PowerShell
- VS Code integrated terminal + WSL
- Linux GNOME Terminal 或等价 VTE
- tmux
- Zellij

重点手工用例：

- 粘贴 5 行文本，不自动提交，换行保留。
- 粘贴 2000 字符文本，显示占位符，提交时 payload 完整。
- assistant 流式输出时继续输入，输入不被覆盖。
- 任务运行中提交新消息进入队列，底部显示 queued。
- shell approval 弹层显示时，状态行/输入草稿不丢失。
- resize 到 40 列窄屏，footer 和状态行不乱行。

## 风险与规避

### 风险 1：半重构状态下输出路径仍然分散

规避：

- Phase 0 先做输出路径盘点和 surface 接口。
- 对关键交互路径添加测试，捕获直接 stdout 写入。
- 新增开发规则：交互模式下新增输出必须经 surface/coordinator。

### 风险 2：scroll region 在部分终端表现不一致

规避：

- 固定底部功能默认只在能力检测通过时启用。
- 提供环境变量或配置禁用高级 UI，例如 `AICLI_TUI=legacy`。
- 对 Zellij/tmux 单独记录兼容策略。

### 风险 3：raw mode 影响 shell 子进程和外部编辑器

规避：

- 运行外部 shell、editor、pager 前恢复终端模式，返回后重新启用。
- 参考 Codex `with_restored` 思路，封装 `TerminalDriver.WithRestored(fn)`。
- 子进程期间暂停输入事件读取，避免 stdin 被抢读。

### 风险 4：paste burst 误判快速人工输入

规避：

- 阈值平台化：Windows 稍大，Unix 稍小。
- ASCII 首字符短暂 hold，非 ASCII/IME 不 hold。
- 提供 `AICLI_DISABLE_PASTE_BURST=1` 逃生开关。
- 以单元测试覆盖慢速输入、IME、快速 paste、多行 Enter。

### 风险 5：全帧渲染引入性能问题

规避：

- Phase 1 先用 scroll region 实现最小固定底部。
- Phase 4 再引入 diff buffer 和 frame rate limiter。
- 对流式输出做 chunk 合并，不每个 token 立即重绘。

## 推荐配置项

建议新增或规划以下配置：

- `aicli.ui.mode`: `legacy | inline | frame`
- `aicli.ui.fixed_bottom`: `auto | always | never`
- `aicli.ui.status_line`: 字符串数组，例如 `["model", "run-state", "project-name", "git-branch", "tokens"]`
- `aicli.ui.terminal_title`: 字符串数组，例如 `["spinner", "project-name", "run-state"]`
- `aicli.ui.paste_burst`: `auto | enabled | disabled`
- `aicli.ui.large_paste_threshold`: 默认 `1000`
- `aicli.ui.animations`: `auto | enabled | disabled`
- `aicli.ui.notifications`: `off | bell | osc9 | desktop`

## 里程碑

### M1：稳定固定底部

范围：

- Phase 0 + Phase 1。
- 修复真实终端尺寸。
- 输出路径收敛。
- scroll region 固定底部。
- 状态栏不再随聊天滚动。

可交付：

- `aicli.ui.fixed_bottom=auto` 可用。
- Windows Terminal、WSL、Linux 主路径通过手工验证。

### M2：稳定 composer 和粘贴

范围：

- Phase 2。
- bracketed paste + paste burst。
- 大粘贴占位符。
- 多行输入、提交、队列初步打通。

可交付：

- Windows/Linux/WSL 粘贴行为一致。
- 粘贴不会自动触发 LLM 调用。
- 粘贴换行不会导致 prompt 错位。

### M3：状态面和 footer

范围：

- Phase 3。
- 状态行配置。
- run state、tokens、cwd、git branch、session id。
- terminal title。

可交付：

- `/statusline` 或配置文件可调整状态项。
- footer 可根据宽度折叠。
- 任务运行态与工具执行状态可见。

### M4：frame renderer

范围：

- Phase 4。
- render tree + diff buffer。
- active/history cell。
- inline viewport。

可交付：

- 流式输出、状态栏、输入框统一帧渲染。
- 状态栏和输入区不依赖临时 save/restore cursor。
- 快照测试覆盖核心组件。

### M5：高级交互

范围：

- Phase 5。
- popup、approval overlay、pending input preview、file/skill/plugin mention、external editor。

可交付：

- 主要交互能力达到 Codex TUI 的设计水平，但保留本项目业务语义。

## 实施优先级

P0 必须先做：

- 真实终端尺寸。
- 输出路径收敛。
- 高级 UI 开关和 fallback。
- scroll region 固定底部最小实现。
- paste 入口统一和 CRLF 归一化。

P1 紧随其后：

- paste burst。
- composer state。
- status surface。
- footer 纯渲染。
- terminal title。

P2 再做：

- frame buffer + diff。
- popup/modal view stack。
- pending input preview。
- approval overlay。

P3 后续增强：

- file/skill/plugin mention。
- external editor。
- image attachment preview。
- notifications。
- richer theme。

## 关键设计原则

1. 业务代码只发 UI 事件，不直接画终端。
2. UI 组件只读状态并渲染，不直接调用 LLM 或工具。
3. 输入事件先进入 composer，由 composer 决定是否提交、排队、打开弹层或仅更新草稿。
4. 粘贴内容只有一个入口，所有平台差异在进入 composer 前消解。
5. 状态栏和 terminal title 共用状态源，不复制业务判断。
6. 固定底部必须有 fallback，不以牺牲脚本输出和终端兼容为代价。
7. 每个阶段都要有可运行验收，不把全部风险推到最终全量 TUI 重写。

## 总结

`aicli` 可以实现 Codex 风格高级 UI，但正确路线不是继续强化现有 `StatusBar.Render()` 或 `Layout.PrintMessage()`，而是逐步建立统一 UI surface。短期通过输出收敛、真实终端尺寸和 scroll region 实现固定底部；中期通过 composer、paste burst、状态面和 footer 解决跨平台输入与状态展示；长期通过 frame renderer、render tree 和 diff buffer 达到 Codex 的稳定性和可扩展性。

最重要的工程边界是：交互模式下终端只有一个所有者。只要仍允许多个组件各自移动光标、清屏、打印 prompt，高级 UI 就会在 Windows、Linux、WSL 和终端复用器中反复出现错位、重复、自动提交和覆盖问题。
