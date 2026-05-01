# aicli UI 重构计划：参考 Codex TUI 的分阶段高级能力方案

状态: **draft**

更新时间: **2026-05-01**

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

