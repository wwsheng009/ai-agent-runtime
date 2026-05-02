# aicli chat TUI slash 命令自动补全分析与实施方案

状态: **planned**

调研日期: **2026-05-02**

## 结论

`aicli chat` 可以在现有 TUI 基础上实现 slash 命令自动补全，最合理的短期路径不是引入全新的 TUI 框架，而是复用已经落地的两块能力：

- `backend/cmd/aicli/ui/inputbox_editor.go` 的逐键 line editor 已经支持 `onChange` 回调，能在用户输入 `/`、`/m`、`/mo` 时实时拿到输入草稿。
- `backend/cmd/aicli/ui/fixed_bottom_surface.go` 已经支持底部 popup，不会把临时选择列表写入聊天 scrollback；`/model` 选择器和 pending paste preview 已经证明这条路径可用。

> **UX 行为变更提示**：Phase 2 启用 `Enter` 接受候选后，用户输入 `/m<Enter>`（无 exact 匹配）的既有「未知命令」错误提示路径会被候选接受取代。如果 popup 未激活或无候选，则仍保留原 `default` 分支输出，避免完全屏蔽该错误反馈。

推荐分三步落地：

1. **先做被动建议列表**：用户输入 `/` 时底部显示系统支持的命令；继续输入 `/m` 时实时过滤出 `/model`、`/mode` 等匹配项。这个阶段只依赖现有 `onChange` 和 `FixedBottomSurface.ShowPopup`，改动小、风险低。
2. **再做真正补全动作**：扩展 line editor 的按键模型，支持 `Tab` 接受补全、候选循环和后续参数提示。当前 editor 会把 `Tab` 当控制字符忽略，必须显式加入补全按键事件，否则只能“显示建议”，不能可靠“改写输入框内容”。
3. **最后加入键盘导航**：当 completion popup 激活时，`Up/Down` 在候选中移动选中项，`Enter` 在未完成命令名上接受候选、在完整命令上提交，`Esc` 关闭 popup；当 popup 未激活时，`Up/Down` 必须保持原有历史导航行为。

这项能力的关键不是 popup 渲染，而是先把 chat slash 命令收敛为一个可复用的 command catalog。现在命令定义分散在 `handleCommand` 路由、`/help` 手写输出和 `ui.PrintHelp` 旧帮助文案里，如果直接从帮助文本解析候选，会继续制造不一致。

## 当前实现分析

### 输入路径

交互模式下主路径如下：

- `backend/cmd/aicli/commands/chat.go`
  - `runChatLoop` 在每轮 prompt 后调用 `chatInteractiveReadLine(session, session.cancelCtx)`。
- `backend/cmd/aicli/commands/chat_input_queue.go`
  - `chatInteractiveReadLine` 在支持交互 line editor 时调用 `session.InputBox.ReadWithHistoryPrompt(prompt, onChange)`。
  - 当前 `onChange` 只调用 `session.Interaction.SetPromptInput(text)`，用于让 prompt 清理逻辑知道当前草稿。
- `backend/cmd/aicli/ui/inputbox_editor.go`
  - `ReadWithHistoryPrompt` 进入 `readInteractiveLineWithOptions`。
  - 用户每次插入、删除、历史切换、粘贴更新后都会触发 `notifyChange()`，然后 `redraw()`。
  - 因此，实时补全可以从 `onChange` 接入，不需要等 Enter。

当前限制：

- `onChange` 只传 `text`，不传 cursor；slash 命令在第一 token 内匹配时够用，但要支持任意 cursor 位置、参数补全和未来 `@file` 补全，需要扩展为带 cursor 的 editor snapshot。
- `Tab` 没有专门 key kind。`decodeInteractiveKey` 会把字节 `9` 解成 rune `'\t'`，后续因为 `unicode.IsControl` 被忽略。真正自动补全必须增加 `editorKeyComplete`。
- `Up/Down` 已经用于历史导航。需要新增“completion popup 激活时才接管导航”的状态边界，否则会破坏现有历史切换体验。
- Windows 与 Linux 的键盘事件来源不同。Linux/WSL 主要依赖 raw stdin + ANSI escape sequence；Windows Terminal/ConPTY 通常也能走 VT 序列，但 legacy console 和部分宿主会有差异，必须保留 fallback。

### 命令处理路径

`backend/cmd/aicli/commands/command.go` 的 `handleCommand` 是当前 chat slash 命令的事实路由。已识别的命令包括：

- 会话控制：`/exit`、`/quit`、`/q`、`/clear`、`/cls`、`/new`、`/session`、`/sessions`、`/load`、`/resume`、`/title`、`/history`、`/h`
- 输出与模型：`/stream`、`/s`、`/normal`、`/n`、`/model`
- 上下文与附件：`/compact`、`/image`、`/queue`
- 权限与审批：`/permission-mode`、`/mode`、`/approval-reuse`、`/yolo`
- function/skill：`/functions`、`/catalog`、`/function`、`/describe`、`/call`、`/tool`、`/skill`
- shell：`/shell`、`/cmd`，以及非 slash 的 `!<command>`
- 帮助：`/help`、`/?`

需要注意：

- 当前 `/help` 是 `fmt.Println` 手写列表。
- `backend/cmd/aicli/ui/welcome.go` 的 `PrintHelp` 还包含 `/provider`、`/token`、`/save`、`/config` 等旧条目，不应作为补全数据源。
- 现有 `commandMatches` 已解决 `/mode` 与 `/model` 前缀误伤问题，补全匹配也必须沿用“命令 token 边界”思路，不能简单 `strings.HasPrefix` 后直接执行。

### Popup 与 bottom surface

`backend/cmd/aicli/ui/fixed_bottom_surface.go` 已提供：

- `ShowPopup(lines []string)`：展示临时 popup，不带输入 prompt。
- `ShowPopupInput(lines []string, prompt string)`：展示 popup + transient prompt，用于 `/model` 等运行期选择器。
- `ClearPopup()`：清理 popup 区域。
- `ShowPendingPastePreview` / `ClearPendingPastePreview`：已经在输入过程中从 bottom pane 展示临时状态。

对 slash 补全来说，第一阶段应使用 `ShowPopup`，因为正常聊天 prompt 仍由 line editor 管理；只有 `/model` 这种进入二级选择流程时才使用 `ShowPopupInput`。

### Linux/Windows 兼容现状

当前 line editor 的关键输入解析集中在 `backend/cmd/aicli/ui/inputbox_editor.go`，平台差异主要由终端 raw mode、VT 能力和输入字节序列体现，而不是由业务命令层处理。

Linux/WSL 主路径：

- `term.MakeRaw(fd)` 后从 stdin 读取字节。
- 方向键通常以 ANSI escape sequence 进入，例如 `ESC [ A` 表示 Up，`ESC [ B` 表示 Down。
- `decodeEscapeInteractiveKey` 已能识别 Up/Down/Left/Right/Home/End/Delete 等序列。
- bracketed paste 已通过 `ESC [ 200 ~` 和 `ESC [ 201 ~` 识别。

Windows 主路径：

- 当前工程已经把 Windows 输入收敛到同一 raw editor / paste burst 路径，`inputbox_editor_windows.go` 负责平台侧 readiness 和 console 差异。
- Windows Terminal、VS Code terminal、ConPTY 通常可以支持 VT 序列，因此可以复用同一 `decodeEscapeInteractiveKey`。
- legacy console 或 VT processing 不可用时，`FixedBottomSurface` 可能不会启用，实时 popup 必须降级为不可见状态；用户仍可通过 `/help` 查看命令。
- Windows 下 paste burst、CRLF、控制键时序更容易出现边界问题，补全导航不能假设每个按键都是独立、低延迟、可靠到达。

兼容原则：

- 命令匹配和候选状态必须平台无关，只处理 Unicode 字符串和 rune cursor。
- 平台差异只允许停留在 `ui` 输入事件层和 `FixedBottomSurface` 能力检测层。
- 不为 Linux 和 Windows 分别实现两套补全业务逻辑。
- 如果无法可靠解析方向键或无法启用 bottom surface，只禁用可见 popup 和候选导航，不影响普通输入、历史导航和 Enter 提交。
- 所有新增 key kind 必须在 Windows、Linux、WSL、VS Code terminal、tmux/Zellij fallback 下有明确测试或降级路径。

## 目标行为

MVP 行为：

- 用户在空输入框输入 `/`，底部 popup 自动列出 chat 支持的系统命令。
- 用户继续输入 `/m`，popup 只显示匹配 `/m` 的命令，例如 `/model`、`/mode`。
- 用户继续输入 `/mod`，精确前缀优先，`/model` 和 `/mode` 仍可同时出现；当输入 `/mode` 时 `/mode` 排在第一，`/model` 可作为仍匹配的长前缀候选。
- 用户输入普通消息中的 `/`，例如 `请解释 /model`，不触发命令补全。
- 用户输入 `/model ` 后，第一阶段可以清除命令候选；第二阶段再显示 `/model` 的参数提示。
- 用户提交、Ctrl+C、Ctrl+D、清空输入或输入不再是 slash 命令时，popup 必须立即消失。
- 如果 fixed-bottom surface 未启用、非交互、JSON 输出、pipe/CI 或 legacy terminal，不显示实时 popup，也不在每次按键时向 stdout 打印建议。

第二阶段行为：

- `Tab` 接受当前最佳候选。
- 多候选时第一次 `Tab` 补到最长公共前缀；如果没有更长公共前缀，则补全当前选中候选。
- `Up/Down` 在 completion popup 激活时移动选中候选，不再触发历史导航。
- `Up/Down` 在 completion popup 未激活时保持现有历史导航行为。
- `Enter` 在 completion popup 激活且当前 token 还不是完整命令时，接受选中候选并继续编辑；命令完整或 popup 未激活时，保持原有提交行为。
- `Esc` 关闭 completion popup 并恢复历史导航，不清空用户输入。
- 补全命令名后追加一个空格，例如 `/m<Tab>` 变成 `/model `。
- 后续支持命令参数提示，例如 `/model --` 显示 `--provider`、`--model`、`--reasoning-effort`、`--status`、`--clear-reasoning`。

## 非目标

- 不把命令补全列表写入聊天 transcript 或 scrollback。
- 不在第一阶段实现完整 command palette。
- 不在 completion popup 未激活时抢占 `Up/Down` 历史导航。
- 不从 `/help` 输出文本反向解析命令。
- 不为非交互和 JSON 输出增加 ANSI 控制序列。

## 推荐架构

### 1. 建立 chat slash command catalog

新增 `backend/cmd/aicli/commands/chat_slash_command_catalog.go`，定义纯数据结构：

```go
type chatSlashCommandSpec struct {
	Name         string
	Aliases      []string
	Usage        string
	Summary      string
	Group        string
	Args         []chatSlashCommandArgSpec
	Interactive  bool
	Hidden       bool
	AcceptsArgs  bool // Tab 补全完整命令后是否追加空格
	RequiresArgs bool // 纯 HasPrefix 分发，必须带参数才有效
	ShortcutOf   string // 与其他命令语义相近的快捷命令，但不是 alias
}

type chatSlashCommandArgSpec struct {
	Token   string
	Summary string
}
```

第一阶段只把 catalog 作为补全和帮助渲染的数据源，不强行重写 `handleCommand` 路由。这样可以降低行为回归风险。

后续可以逐步让 `/help` 也从 catalog 渲染，避免命令文案继续漂移。最终如果要进一步收敛，可以把 `handleCommand` 改为先解析 command token，再根据 catalog/handler 表分发。

catalog 初始建议：

| Command | Aliases | Summary |
| --- | --- | --- |
| `/help` | `/?` | 显示命令帮助 |
| `/exit` | `/quit`, `/q` | 退出聊天 |
| `/clear` | `/cls` | 清空当前会话历史 |
| `/new` | | 创建新会话 |
| `/stream` | | 查看或切换流式输出（支持 `on|off|toggle|status`） |
| `/s` | | 流式开启快捷（`ShortcutOf: /stream`，等价 `/stream on`） |
| `/normal` | `/n` | 流式关闭快捷（`ShortcutOf: /stream`，等价 `/stream off`） |
| `/history` | `/h` | 显示当前会话历史 |
| `/session` | | 显示当前会话信息 |
| `/sessions` | | 列出或筛选可恢复会话 |
| `/load` | | 加载指定会话 |
| `/resume` | | 恢复最近会话或弹出恢复菜单 |
| `/title` | | 更新当前会话标题 |
| `/model` | | 查看或切换 provider/model/thinking_effort |
| `/compact` | | 手动触发会话压缩 |
| `/image` | | 查看、添加或清空图片附件 |
| `/queue` | | 查看或清空排队输入 |
| `/permission-mode` | `/mode` | 查看或切换权限模式 |
| `/approval-reuse` | | 查看或切换审批复用策略 |
| `/yolo` | | 切换到 bypass_permissions |
| `/functions` | `/catalog` | 查看或预览 function catalog |
| `/function` | `/describe` | 查看单个 function 描述 |
| `/call` | `/tool` | 直接执行 function/tool |
| `/skill` | | 直接执行指定 skill |
| `/shell` | `/cmd` | 执行 shell 命令并把输出分享给 AI |

`!<command>` 是 shell 快捷入口，不属于 slash command catalog，仅在 query 为空（刚输入 `/`）且 popup 还有剩余行时附加一条「Shell 快捷：`!git status`」提示；query 非空、候选多于阈值或 popup 已撑满时不显示，避免挤占候选行。

**catalog ↔ handleCommand 双向同步**：`@backend/cmd/aicli/commands/command.go` 里命令分三类分发：

- 精确 `switch cmd` 分支：`/exit`、`/quit`、`/q`、`/clear`、`/cls`、`/new`、`/s`、`/normal`、`/n`、`/history`、`/h`、`/functions`、`/catalog`、`/session`、`/compact`、`/sessions`、`/yolo`、`/image`、`/help`、`/?`。
- `HasPrefix` + 空格分支（必须带参数）：`/shell `、`/cmd `、`/function `、`/describe `、`/functions `、`/catalog `、`/call `、`/tool `、`/skill `、`/sessions `、`/load `、`/title `。
- `HasPrefix` 无空格分支（可带可不带参数）：`/image`、`/queue`、`/compact`、`/approval-reuse`、`/model`、`/stream`、`/resume`、`/permission-mode`、`/mode`。

catalog 必须同时覆盖这三类，`RequiresArgs` 用来区分第二类；测试要求见「测试计划 → catalog 与路由同步测试」。

### 2. 纯匹配层

新增 `backend/cmd/aicli/commands/chat_slash_completion.go`，核心逻辑保持纯函数，方便单元测试：

```go
type chatSlashCompletionContext struct {
	Active      bool
	Query       string
	TokenStart  int
	TokenEnd    int
	InArguments bool
}

type chatSlashCompletionCandidate struct {
	Command string
	AliasOf string
	Usage   string
	Summary string
	Group   string
	Score   int
}
```

建议函数：

- `buildSlashCompletionState(text string, cursor int, previousSelected int) chatSlashCompletionState`
- `detectSlashCompletionContext(text string, cursor int) chatSlashCompletionContext`
- `matchSlashCommandCandidates(specs []chatSlashCommandSpec, query string) []chatSlashCompletionCandidate`
- `longestCommonSlashPrefix(candidates []chatSlashCompletionCandidate) string`
- `applySlashCommandCompletion(text string, cursor int, tokenStart, tokenEnd int, candidate string, acceptsArgs bool) (string, int)`

`applySlashCommandCompletion` 的契约：

- 只替换 `[tokenStart, tokenEnd)` 区间的 rune 序列为 `candidate`，保留 token 之前的前导空白和 token 之后的所有内容（例如 `"  /m hello"` 在 token 内补全只改写 `/m` 段）。
- 如果 `acceptsArgs == true` 且替换后下一字符不是空格，则追加一个空格，cursor 落在该空格后；否则 cursor 落在 `candidate` 末尾，不追加空格（避免 `/help `、`/exit ` 这类无意义尾空格）。
- 所有索引均为 rune 索引，不是字节索引，保证与 bracketed paste 中的中文/emoji 兼容。
- 规范化：若用户输入大小写混合（例如 `/Mo`），替换文本使用 catalog 中的规范形式（小写 `/model`），以匹配 `handleCommand` 的 `strings.ToLower` 分发。

触发规则：

- 只在第一 token 为 slash command 时激活。
- `strings.TrimLeft` 后第一个字符是 `/` 时可激活，但替换区间必须保留原始前导空白位置；是否允许前导空白触发可通过测试明确。
- cursor 位于第一个 token 内时显示命令候选。
- cursor 已进入第一个空格后的参数区时，第一阶段不显示命令候选；第二阶段交给命令专属参数 provider。
- query 为空或只有 `/` 时显示常用命令列表。
- 匹配为大小写无关：`/M`、`/Mo`、`/mO` 均能命中 `/model`；排序和渲染仍按 catalog 规范形式（小写）。
- query 以非字母开头的别名必须被正确识别：`/?` 命中 `/help` 的 alias，`detectSlashCompletionContext("/?")` 的 query 为 `"/?"`。

排序规则：

1. 精确匹配优先，例如输入 `/mode` 时 `/mode` 排在 `/model` 前面。
2. 主命令名 prefix 匹配优先于 alias 匹配。
3. alias 匹配显示为 `alias of /xxx`，但仍允许补全 alias 本身，避免用户输入 `/q<Tab>` 被强行改成 `/exit`。
4. 同分按 group 和命令名稳定排序，保证 popup 不跳动。

**exact match 与 longer-prefix 并存时的显式规则**（适用于 `/mode` vs `/model`、`/s` vs `/session`/`/sessions`/`/stream`/`/shell`/`/skill`）：

- popup 始终同时列出 exact 项和所有 longer-prefix 候选。
- `Selected` 默认指向 exact 项（排在第一）。
- `Tab`：
  - 若 Selected 为 exact 项，视为 no-op（不追加空格，不改写文本），保持用户明确意图；用户按 `Down` 后再 `Tab` 可切换到 longer-prefix。
  - 若 Selected 为 longer-prefix 候选，按 selected 补全。
- `Enter`：
  - 若 Selected 为 exact 项，按原 editor 行为提交（进入 `handleCommand`），popup 关闭。
  - 若 Selected 为 longer-prefix 候选（即用户 `Down` 过），接受 selected 作为补全，不提交。
- 无候选（unknown token）时 Enter 不被消费，保持原 `default` 分支「未知命令」输出，避免用户失去显式提交未知命令看错误提示的能力。

**Case sensitivity & 规范化**：所有匹配函数对 query 做 `strings.ToLower`，但返回的 `Command` 字段使用 catalog 规范形式；`applySlashCommandCompletion` 的 replacement 也用规范形式。

候选状态需要保存选中项：

```go
type chatSlashCompletionState struct {
	Active      bool
	Context     chatSlashCompletionContext
	Candidates  []chatSlashCompletionCandidate
	Selected    int
	Warning     string
}
```

`Selected` 的规则：

- 默认选中第一个候选。
- 用户按 `Down` 时向后移动，按 `Up` 时向前移动。
- 建议采用环绕导航：最后一项按 `Down` 回到第一项，第一项按 `Up` 回到最后一项。
- 用户继续输入导致候选列表变化时，优先保留同一 command 的选中项；如果不存在，则回到第一项。
- 没有候选时 `Selected` 为 `-1`。

### 3. Popup 渲染层

新增纯渲染函数：

```go
func renderSlashCommandCompletionPopup(state chatSlashCompletionState, width int) []string
```

建议显示形态：

```text
命令
> /model              查看或切换 provider/model/thinking_effort
  /mode               /permission-mode 的别名
  /stream             切换流式输出
  ...
提示: ↑↓ 选择，Tab/Enter 接受，Esc 关闭
```

宽度处理：

- 使用 `ui.DisplayWidth` / 已有截断工具处理中文和宽字符。
- 单行优先保留命令名，摘要可截断。
- 选中项使用 `>` 前缀；不依赖 ANSI 反色，保证 legacy 或颜色禁用时仍可读。
- 默认最多显示 8 条；候选更多时追加 `还有 N 个匹配，继续输入可过滤`。
- 当没有匹配时显示 `未找到匹配命令: /xxx`，但不阻止用户继续输入，因为最终仍由 `handleCommand` 给出未知命令错误。

### 4. Completion controller

在 `commands` 包内新增轻量 controller，避免把命令业务数据塞进 `ui` 包：

```go
type chatSlashCompletionController struct {
	session *ChatSession
	active  bool
	lastKey string
	state   chatSlashCompletionState
}

func (c *chatSlashCompletionController) Update(text string)
func (c *chatSlashCompletionController) Clear()
func (c *chatSlashCompletionController) Accept(text string, cursor int) (string, int, bool)
func (c *chatSlashCompletionController) Navigate(delta int) bool
```

第一阶段只需要 `Update` 和 `Clear`：

- `Update` 从输入草稿计算候选。
- 如果 `session.Surface != nil && session.Surface.Enabled()`，调用 `session.Surface.ShowPopup(lines)`。
- 如果不满足 surface 条件，只更新内部状态，不打印。
- 为避免重复刷新，可以缓存 `lastKey`，输入文本和候选列表没有变化时不重绘。
- **与 pending paste preview 的仲裁**：当 `session.Interaction` 已进入 paste active 状态或 `FixedBottomSurface` 正显示 pending paste preview 时，`Update` 只更新内部 state，不调用 `ShowPopup`；paste 结束后由 editor 的下一次 `onChange` 再触发补全渲染。pending paste preview 对 popup 的占用优先级高于 completion。
- **幂等性**：`Update("")` 与 `Clear()` 必须幂等且可重复调用——`readPrompt` 入口和出口都会触发 `onChange("")`，controller 需保证两次调用不会打印空 popup 或抖动 surface。
- **popup 高度稳定**：候选固定最多 8 条，少于 8 时 popup 真实行数随之变化。controller 在每次刷新前先 `ClearPopup` 再 `ShowPopup`，保证 `FixedBottomSurface.applyLayoutLocked` 以一致序列调整 scroll region，避免连续行数跳变导致 editor 保存的 cursor anchor 偏移。
- **非交互路径零介入**：`NoInteractive`、`JSONOutput`、`session.InputQueue` 分支、非 TTY `bufio` 分支都不创建 controller、不调用 `ShowPopup`；由 `chatInteractiveReadLine` 的 `shouldUseInteractiveLineEditor(session)` 分支守卫保证。

键盘导航阶段需要 `Navigate(delta)`：

- `delta=1` 表示 Down，`delta=-1` 表示 Up。
- 只有 `state.Active && len(state.Candidates) > 0` 时返回 `true`，表示按键已被 completion 消费。
- 返回 `false` 时 line editor 继续执行原有 Up/Down 历史导航。
- 每次导航后重新调用 popup renderer，选中项通过 `>` 前缀展示。

接入点在 `chatInteractiveReadLine`：

```go
completion := newChatSlashCompletionController(session)
defer completion.Clear()

line, err := session.InputBox.ReadWithHistoryPrompt(prompt, func(text string) {
	if session.Interaction != nil {
		session.Interaction.SetPromptInput(text)
	}
	completion.Update(text)
})
```

注意：`defer completion.Clear()` 必须覆盖正常提交、Ctrl+C、Ctrl+D 和错误返回，避免 popup 残留。

### 5. Editor hooks 扩展

第二阶段新增 line editor hooks，而不是把补全逻辑硬编码进 `ui/inputbox_editor.go`。

建议新增：

```go
type LineEditorSnapshot struct {
	Text   string
	Cursor int
}

type LineEditorReplacement struct {
	Text   string
	Cursor int
}

type LineEditorHooks struct {
	OnChange   func(LineEditorSnapshot)
	OnComplete func(LineEditorSnapshot) (LineEditorReplacement, bool)
	OnNavigate func(LineEditorSnapshot, int) bool
	OnSubmit   func(LineEditorSnapshot) (LineEditorReplacement, bool)
	OnCancelPopup func(LineEditorSnapshot) bool
}
```

然后提供兼容 wrapper：

```go
func (ib *InputBox) ReadWithHistoryPrompt(prompt string, onChange func(string)) (string, error)
func (ib *InputBox) ReadWithHistoryPromptWithHooks(prompt string, hooks LineEditorHooks) (string, error)
```

`ReadWithHistoryPrompt` 继续调用新 API，以减少现有调用点改动。

`inputbox_editor.go` 需要增加：

- `editorKeyComplete`
- `editorKeyCancelPopup`，由 bare `Esc` 或可安全识别的取消键触发；如果 bare `Esc` 无法可靠区分 Alt-modified key，则保持现有 ignore 行为，先不启用取消。此时的降级路径是：用户通过 `Backspace` 把 query 删到空（`/` 也删掉）即自动关闭 popup，不依赖 `Esc`。
- 在 `decodeInteractiveKey` 中把 `\t` / Ctrl+I 映射为 `editorKeyComplete`
- 保留现有 Up/Down decode：`ESC [ A` 和 Ctrl+P 仍进入 `editorKeyUp`，`ESC [ B` 和 Ctrl+N 仍进入 `editorKeyDown`。
- 在主循环处理 `editorKeyComplete`：
  - 先 flush paste burst。
  - 调用 `hooks.OnComplete(LineEditorSnapshot{Text: string(line), Cursor: cursor})`。
  - 如果返回 replacement，则替换 `line` 和 `cursor`，触发 `notifyChange()` 和 `redraw()`。
  - 如果没有 replacement，保持当前行为，不发出终端响铃。
- 在主循环处理 `editorKeyUp` / `editorKeyDown` 时先调用 `hooks.OnNavigate(snapshot, delta)`：
  - 如果 hook 返回 `true`，说明 completion popup 已消费该按键，不执行历史导航。
  - 如果 hook 返回 `false`，保持原有历史导航。
- 在主循环处理 `editorKeyEnter` 时先调用 `hooks.OnSubmit(snapshot)`：
  - 如果 hook 返回 replacement，说明 Enter 被 completion 用来接受候选，应替换输入并继续编辑，不提交。
  - 如果 hook 不消费，保持现有 Enter 提交行为。
- 在主循环处理取消键时调用 `hooks.OnCancelPopup(snapshot)`：
  - 如果 hook 返回 `true`，只关闭 completion popup，不中断输入。
  - 如果 hook 返回 `false`，保持现有 editor 行为。

这样 `ui` 包只知道“有一个补全 hook 可以改写文本”，不知道 slash 命令、模型、session 等业务概念，依赖方向保持正确。

## 分阶段实施计划

### Phase 1：命令 catalog + 被动 popup

目标：实现“输入 `/` 显示命令列表，输入 `/m` 实时过滤”的最小闭环。

改动范围：

- 新增 `backend/cmd/aicli/commands/chat_slash_command_catalog.go`
- 新增 `backend/cmd/aicli/commands/chat_slash_completion.go`
- 修改 `backend/cmd/aicli/commands/chat_input_queue.go`
- 可选修改 `backend/cmd/aicli/commands/command.go`，让 `/help` 从 catalog 渲染
- 新增 `backend/cmd/aicli/commands/chat_slash_completion_test.go`

实施步骤：

1. 建立 `chatSlashCommandSpec` 和 `defaultChatSlashCommandCatalog()`。
2. 实现 `detectSlashCompletionContext`，只识别第一 token 内的 slash 输入。
3. 实现 prefix 匹配、排序、去重和渲染。
4. 在 `chatInteractiveReadLine` 的 `onChange` 中创建并更新 completion controller。
5. 在读取返回路径 `defer Clear()`，确保 popup 不残留。
6. 保持 legacy terminal、NoInteractive、JSON 输出行为不变。

验收标准：

- 输入 `/` 后底部 popup 显示命令列表。
- 输入 `/m` 后至少显示 `/model` 和 `/mode`，并按确定排序。
- 输入普通消息 `hello /m` 不显示 popup。
- 输入 `/unknown` 显示无匹配提示或清空候选，但不提交、不报错。
- Enter 提交后 popup 消失。
- Ctrl+C / Ctrl+D / read error 后 popup 消失。
- surface 不可用时不会在每次按键打印任何候选文本。

### Phase 2：Tab 接受补全与上下键导航

目标：实现真正的自动补全动作，并在 completion popup 激活时支持键盘选择候选。

改动范围：

- 修改 `backend/cmd/aicli/ui/inputbox_editor.go`
- 修改 `backend/cmd/aicli/ui/inputbox.go`
- 修改 `backend/cmd/aicli/commands/chat_input_queue.go`
- 扩展 `backend/cmd/aicli/commands/chat_slash_completion.go`
- 新增或扩展 `backend/cmd/aicli/ui/inputbox_editor_test.go`
- 扩展 `backend/cmd/aicli/commands/chat_slash_completion_test.go`

实施步骤：

1. 在 `ui` 包新增 `LineEditorSnapshot`、`LineEditorReplacement`、`LineEditorHooks`。
2. 增加 `ReadWithHistoryPromptWithHooks`，保留旧 `ReadWithHistoryPrompt` API。
3. 在 key decoder 中增加 `editorKeyComplete`，将 `\t` 识别为补全按键。
4. 在 editor 主循环中处理 `editorKeyComplete`，调用 `hooks.OnComplete` 并替换 line/cursor。
5. 在 editor 主循环中处理 `editorKeyUp` / `editorKeyDown`，先交给 `hooks.OnNavigate`，未消费时再走历史导航。
6. 在 editor 主循环中处理 `editorKeyEnter`，先交给 `hooks.OnSubmit`，只有 hook 不消费时才提交。
7. 在 `commands` 包实现 `Accept`：
   - 如果只有一个候选，补全该候选。
   - 如果多个候选有更长公共前缀，先补公共前缀。
   - 如果公共前缀没有推进，则补全当前最佳候选。
8. 在 `commands` 包实现 `Navigate`，按 `Up/Down` 更新 `Selected` 并重绘 popup。
9. 在 `commands` 包实现 `AcceptOnSubmit`，只在当前 token 不是完整命令时消费 Enter。
10. 补全完整命令后自动追加空格，便于继续输入参数。

验收标准：

- 输入 `/m<Tab>` 能补成 `/mo`、`/mod` 或直接 `/model `，具体行为由测试固定。
- 输入 `/m` 后按 `Down/Up` 能在 `/model`、`/mode` 等候选之间移动选中项。
- 输入 `/m` 后按 `Enter` 会先接受当前选中候选并继续编辑，不会提交未知命令 `/m`。
- 输入 `/model` 后按 `Enter` 保持原有提交行为，进入现有 `/model` 处理流程。
- completion popup 未激活时，`Down/Up` 仍然是历史导航。
- 输入 `/q<Tab>` 可以补全为 `/q ` 或保留 alias，不应强制改成 `/exit`。
- 输入 `/help<Tab>` 不改变已完整命令，最多追加空格。
- 普通文本中的 Tab 不破坏现有行为。
- 粘贴状态、历史导航、Ctrl+C、Ctrl+D 行为不回归。

推荐固定的第一版 Tab 规则：

- 多候选时如果最长公共前缀能推进，则只补公共前缀。
- 多候选且公共前缀不能推进时，补全当前选中候选并追加空格。
- 单候选时补全该候选并追加空格。

这套规则确定性强，并且能与上下键选中态自然配合。

### Phase 3：命令参数补全

目标：用户输入命令名后的空格或参数前缀时，显示该命令支持的参数、子命令或动态候选。

建议优先级：

1. `/model`
   - 静态参数：`status`、`clear-reasoning`、`--provider`、`--model`、`--reasoning-effort`
   - 动态候选：provider、当前 provider 支持模型、reasoning_effort 选项
2. `/stream`
   - `on`、`off`、`toggle`、`status`
3. `/compact`
   - `auto`、`local`、`remote`
4. `/permission-mode`
   - `default`、`accept_edits`、`plan`、`bypass_permissions`
5. `/approval-reuse`
   - `off`、`session_readonly_shell`、`team_readonly_shell`
6. `/image`
   - `clear`，后续可接文件路径补全
7. `/queue`
   - `clear`、`status`
8. `/function` / `/describe` / `/call` / `/tool`
   - 动态读取 `session.FunctionCatalog`
9. `/skill`
   - 动态读取已加载 skill function 或 resolved skill dirs
10. `/resume` / `/load`
   - 动态读取 session id；注意不要在每次按键同步扫描大量会话文件

实现方式：

```go
type chatSlashArgumentProvider interface {
	CompleteSlashArgs(session *ChatSession, command string, argsText string, cursor int) []chatSlashCompletionCandidate
}
```

参数补全必须有预算：

- 静态候选同步计算。
- 动态候选只读已在内存里的 catalog/config/session 列表。
- 如果需要文件系统扫描或会话库查询，先做缓存或异步刷新，不要在每次 keypress 阻塞 raw editor。

### Phase 4：与未来 BottomPane/Frame Renderer 对齐

现有 `FixedBottomSurface.ShowPopup` 是短期可行方案，但长期 UI 计划已经指向 bottom pane view stack 和 frame renderer。slash completion 应提前按“状态 + 纯渲染”设计，避免后续重写：

- completion controller 只维护状态，不直接拼接业务输出。
- popup renderer 是纯函数，可被未来 `BottomPaneView` 复用。
- editor hook 只返回 replacement，不直接操作 terminal。
- 业务路径只发 UI state，不直接 `fmt.Println`。

未来迁移到 frame renderer 时，`chatSlashCompletionState` 可以成为一个 `BottomPaneView`：

```go
type SlashCommandPopupView struct {
	State chatSlashCompletionState
}
```

它只需要实现 `DesiredHeight(width)`、`Render(width)` 和可选 `CursorPolicy`，不影响命令匹配逻辑。

## 关键边界与风险

### catalog 与路由不一致

风险：补全显示了某个命令，但 `handleCommand` 并不支持；或路由支持了命令，但补全没有显示。

规避：

- 为 catalog 写覆盖测试，列出当前 `handleCommand` 支持的所有命令和 alias。
- 新增命令时要求同步更新 catalog。
- 中期让 `/help` 从 catalog 渲染，减少重复文案。
- 长期可以把 dispatch 也收敛到 command registry。

### Popup 与 line editor 光标互相干扰

风险：`ShowPopup` 会调整 scroll region 并移动光标，line editor 也会保存/恢复光标、重绘输入行。

现状判断：

- `inputbox_editor.go` 的更新顺序是先 `notifyChange()`，再 `redraw()`。
- 如果 `onChange` 里调用 `ShowPopup`，随后 editor 会用保存的输入锚点重绘当前输入。
- pending paste preview 已经在输入过程中使用 bottom surface，说明这个模式有基础可行性。

规避：

- 第一阶段只用 `ShowPopup`，不用 `ShowPopupInput`。
- 每次 read 结束都 `ClearPopup`。
- 加 snapshot 或 fake terminal 测试验证 popup 清理和 prompt state 不残留。
- 如果真实终端发现闪烁或 cursor 偏移，再把 completion popup 降级为 status-line hint，等待真正 bottom pane composer。

### 键盘导航与历史导航冲突

风险：补全 popup 抢占 `Up/Down` 会破坏用户熟悉的历史导航；不抢占则无法支持候选键盘导航。

规避：

- 只在 completion popup 激活且有候选时消费 `Up/Down`。
- popup 未激活、无候选、输入不在 slash command token 内时，`Up/Down` 必须继续走原有历史导航。
- `OnNavigate` 返回 bool 明确表达按键是否被 completion 消费。
- `Esc` 关闭 popup 后应立即恢复历史导航。
- 用单元测试覆盖 active/inactive 两类分支，防止后续改动误抢历史导航。

### 动态候选阻塞输入

风险：`/resume` 查询会话、路径补全、function catalog 构建等动作如果每次按键执行，会卡住 raw editor。

规避：

- Phase 1 只做静态命令名。
- Phase 3 动态候选只能读取内存已有状态。
- 文件系统和 session 查询必须缓存，或只在用户输入足够前缀后触发。
- 每次补全计算应保持 O(命令数) 或 O(候选数)，命令数当前很小。

### Linux/Windows 键盘序列差异

风险：Linux、WSL、Windows Terminal、VS Code terminal、legacy console、tmux/Zellij 对方向键、Esc、Tab、粘贴边界和 raw mode 的行为不同，导致补全导航在某些终端中错判或不可用。

规避：

- 复用现有 `decodeInteractiveKey` 的平台无关 key kind，不在命令层解析原始字节。
- 对 `Tab`、`Up`、`Down` 只依赖当前已经可识别的 key kind。
- `Esc` 关闭 popup 作为可选增强；如果 bare Esc 无法可靠识别，先不启用，避免破坏 Alt 组合键。
- `FixedBottomSurface.Enabled()` 为 false 时禁用可见 popup 和候选导航，只保留普通输入。
- Windows legacy console、dumb terminal、pipe、CI 必须保持原行为。
- 对 Windows Terminal、PowerShell、cmd、WSL、VS Code terminal、Linux terminal 分别做手工 smoke test。

### Popup 多消费者冲突

风险：`FixedBottomSurface` 当前被多方共享——pending paste preview、`/model` 的 `ShowPopupInput`、MCP 状态输出、`chatInteractionCoordinator` 的 status、新增的 slash completion。任意两者并发 `ShowPopup`/`ClearPopup` 会互相覆盖。

规避：

- completion controller 在 paste active 或 pending paste preview 存在时不调用 `ShowPopup`，只更新内部 state。
- `/model` 进入 `ShowPopupInput` 的二级流程时，外层 `ReadWithHistoryPrompt` 已返回并触发 `defer completion.Clear()`，不会与 completion popup 叠加。
- MCP 状态写入走 output region（`BeginOutput`），不占 popup region；方案不引入对 status 的新写入点。
- 中期如果引入 `BottomPaneView` stack，将 completion 作为其中一个 view，由 stack 统一仲裁。

### 兼容模式输出污染

风险：在不支持 fixed-bottom 的终端中按键时反复打印候选，会污染 scrollback。

规避：

- `session.Surface == nil || !session.Surface.Enabled()` 时不做可见实时建议。
- 不 fallback 到 stdout 实时打印。
- `/help` 仍是用户显式请求帮助的兼容入口。

### 粘贴触发误补全

风险：用户粘贴以 `/` 开头的大段文本时，补全 popup 频繁刷新。

规避：

- paste active 或 paste burst flush 阶段延迟一次性更新，不逐字符刷新；这是主规避手段。
- `ComposerState` 使用大粘贴占位符时，补全只看占位符，不展开真实内容。
- 仅第一 token 长度参与触发判断：若第一 token 超过例如 64 rune，视为非命令输入关闭 popup；不对整个 text 长度设阈值，避免「`/m` + 长 prompt 粘贴」场景误关闭。

## 测试计划

### 纯逻辑测试

新增 `backend/cmd/aicli/commands/chat_slash_completion_test.go`：

- `detectSlashCompletionContext("/")` 激活。
- `detectSlashCompletionContext("/m")` 激活，query 为 `/m`。
- `detectSlashCompletionContext("hello /m")` 不激活。
- `detectSlashCompletionContext("/model ")` 第一阶段进入 arguments 状态，不显示命令候选。
- `matchSlashCommandCandidates("/m")` 返回 `/model` 和 `/mode`。
- `matchSlashCommandCandidates("/mode")` 让 `/mode` 排在 `/model` 前面，但 `/model` 仍作为 longer-prefix 候选出现。
- `matchSlashCommandCandidates("/s")` 同时包含 `/s`（exact）、`/session`、`/sessions`、`/stream`、`/shell`、`/skill`，且 `/s` 排在第一。
- 大小写无关：`matchSlashCommandCandidates("/Mo")` 等价于 `/mo`，返回的 `Command` 是规范小写形式。
- alias 匹配，例如 `/q` 返回 `/q` 或 `alias of /exit`；`/?` 命中 `alias of /help`。
- **catalog ↔ handleCommand 同步测试**：枚举 `handleCommand` 的三类 dispatch 表（`switch` 精确分支、`HasPrefix+空格` 分支、`HasPrefix` 无空格分支）并与 catalog 双向比对，任一侧新增命令未同步则测试失败。
- 无匹配时返回空候选但 state 仍可渲染无匹配提示。
- 最长公共前缀计算覆盖 `/m`、`/mo`、`/mod`、`/mode`。
- `applySlashCommandCompletion` 保留前导空白和光标位置。
- `Navigate(1)` 和 `Navigate(-1)` 能环绕移动 `Selected`。
- 候选列表变化后优先保留同一 command 的选中项。
- popup inactive 或无候选时 `Navigate` 返回 false。

### 渲染测试

覆盖：

- 80 列下命令、摘要和 hint 正常显示。
- 窄宽度下摘要截断但命令名保留。
- 候选超过最大显示数时显示 `还有 N 个匹配`。
- 中文摘要宽度计算不破坏对齐。
- 无匹配状态渲染稳定。

### line editor 测试

扩展 `backend/cmd/aicli/ui/inputbox_editor_test.go`：

- `"/m\t\r"` 触发 `OnComplete` 并提交 replacement；在 Windows editor 路径（`inputbox_editor_windows.go`）下同样覆盖，验证 `\t` 映射为 `editorKeyComplete`。
- `"/mode\t"` 在 Selected 为 exact 项时为 no-op，不改变输入；`"/mode\x1b[B\t"` 切到 `/model` 再 Tab 后补全为 `/model `。
- `"/mode\r"` 在 Selected 为 exact 项时正常提交，不被候选接受消费。
- `"/m\r"` 无 exact 匹配但 popup 激活，Enter 接受 Selected 候选，不提交未知命令。
- `"/unknown\r"` 无候选时 Enter 不被 controller 消费，保留 `default` 分支「未知命令」输出。
- 保留 token 后内容：`"/m hello"` cursor 在第 2 列时 Tab 补全只改写第一 token，` hello` 完整保留，cursor 落在命令名后空格处。
- `applySlashCommandCompletion` 对 `acceptsArgs=false` 的命令（`/help`、`/exit`）不追加尾空格。
- `"/m\x1b[B\t\r"` 在 popup active 时触发 Down 导航并补全选中项。
- `"/m\x1b[A\t\r"` 在 popup active 时触发 Up 导航并补全选中项。
- `"/m\r"` 在 popup active 且 token 未完成时先接受候选，不提交未知命令。
- `"/model\r"` 在 token 已是完整命令时正常提交。
- popup inactive 时 `\x1b[A` / `\x1b[B` 仍走历史导航。
- `Tab` 在无 replacement 时不改变输入。
- 补全后 cursor 位于命令名后空格处。
- `Backspace`、`Ctrl+C`、`Ctrl+D` 原行为不回归。
- 粘贴多行以 `/` 开头的文本不被逐行错误提交。

### 集成级测试

可在 `commands` 包用 fake surface 或可观察 controller 验证：

- `chatInteractiveReadLine` 调用期间 `onChange` 会更新 completion controller。
- 正常提交后调用 `Clear`。
- interrupt / EOF 后调用 `Clear`。
- surface 不启用时不会产生可见输出。
- 与 `session.Interaction.SetPromptInput` 共存，不破坏 prompt 清理。

### 手工验证矩阵

重点终端：

- Windows Terminal + PowerShell
- Windows Terminal + `cmd.exe`
- Windows Terminal + WSL Ubuntu
- VS Code integrated terminal + PowerShell
- VS Code integrated terminal + WSL
- Linux terminal
- tmux
- Zellij 或 legacy fallback

手工用例：

- 输入 `/`，观察底部候选出现且不进入 scrollback。
- 输入 `/m`，候选过滤为 `/model`、`/mode`。
- 输入 `/m` 后按 `Down/Up`，选中项在候选间移动；popup 关闭后 `Down/Up` 恢复历史导航。
- 输入 `/m` 后按 `Enter`，先接受选中候选而不是提交未知命令；再按 `Enter` 执行完整命令。
- 输入 `/model` 后按 Enter，现有 `/model` popup-first 选择器仍正常打开。
- 输入 `/m<Tab>`，补全行为符合测试定义。
- 在 Windows Terminal、VS Code terminal、WSL 和 Linux terminal 中分别验证 `Tab`、`Up`、`Down` 的字节序列能被现有 editor 正确识别。
- 输入 `hello /m`，不出现命令 popup。
- assistant 流式输出结束后输入 `/`，底部状态和 popup 不互相覆盖。
- 粘贴多行文本，不触发错误补全和自动提交。
- 退出后 scroll region 恢复，终端不残留底部 popup。

## 建议的文件级落地顺序

第一批：

1. `backend/cmd/aicli/commands/chat_slash_command_catalog.go`
2. `backend/cmd/aicli/commands/chat_slash_completion.go`
3. `backend/cmd/aicli/commands/chat_slash_completion_test.go`
4. `backend/cmd/aicli/commands/chat_input_queue.go`

第二批：

1. `backend/cmd/aicli/ui/inputbox_editor.go`
2. `backend/cmd/aicli/ui/inputbox.go`
3. `backend/cmd/aicli/ui/inputbox_editor_test.go`
4. `backend/cmd/aicli/commands/chat_input_queue.go`

第三批：

1. `backend/cmd/aicli/commands/command.go`
2. `backend/cmd/aicli/commands/chat_model_command.go`
3. `backend/cmd/aicli/commands/chat_stream_command.go`
4. `backend/cmd/aicli/commands/chat_resume_command.go`
5. 其他命令专属参数 provider

## 推荐的最小代码形态

Phase 1 的核心接入可以控制在很小：

```go
func chatInteractiveReadLine(session *ChatSession, ctx context.Context) (string, error) {
	if shouldUseInteractiveLineEditor(session) {
		prompt := formatSessionUserPrompt(session)
		completion := newChatSlashCompletionController(session)
		defer completion.Clear()

		if session.Interaction != nil {
			session.Interaction.SetPromptInput("")
		}
		line, err := session.InputBox.ReadWithHistoryPrompt(prompt, func(text string) {
			if session.Interaction != nil {
				session.Interaction.SetPromptInput(text)
			}
			completion.Update(text)
		})
		// existing error handling remains unchanged
		return line, err
	}
	// existing non-editor paths remain unchanged
}
```

这里的重点是：

- 不改 `runChatLoop` 的命令执行语义。
- 不在 `ui` 包引入 `commands` 包依赖。
- 不在 surface 不可用时打印实时建议。
- 不改变现有 `/model`、`/stream`、`/resume` 等命令处理。

Phase 2 再切换为 hooks：

```go
line, err := session.InputBox.ReadWithHistoryPromptWithHooks(prompt, ui.LineEditorHooks{
	OnChange: func(snapshot ui.LineEditorSnapshot) {
		if session.Interaction != nil {
			session.Interaction.SetPromptInput(snapshot.Text)
		}
		completion.UpdateSnapshot(snapshot)
	},
	OnComplete: func(snapshot ui.LineEditorSnapshot) (ui.LineEditorReplacement, bool) {
		return completion.AcceptSnapshot(snapshot)
	},
	OnNavigate: func(snapshot ui.LineEditorSnapshot, delta int) bool {
		return completion.NavigateSnapshot(snapshot, delta)
	},
	OnSubmit: func(snapshot ui.LineEditorSnapshot) (ui.LineEditorReplacement, bool) {
		return completion.AcceptOnSubmitSnapshot(snapshot)
	},
	OnCancelPopup: func(snapshot ui.LineEditorSnapshot) bool {
		return completion.CancelSnapshot(snapshot)
	},
})
```

## 验收标准

功能验收：

- `/` 可列出命令。
- `/m` 可过滤到 `/model` 等匹配命令。
- completion popup 激活时支持 `Up/Down` 选择候选，popup 未激活时保持历史导航。
- `Enter` 在未完成 token 上接受候选，在完整命令上保持提交。
- 候选列表在底部 popup，不进入聊天历史和 shell scrollback。
- popup 生命周期正确：提交、取消、错误、退出都会清理。
- `Tab` 补全在第二阶段可用，且不会破坏历史导航和粘贴。

代码质量验收：

- 命令 catalog 是补全的唯一数据源。
- 匹配和渲染为纯函数，有单元测试。
- `ui` 包不依赖 `commands` 包。
- `commands` 包不把补全逻辑写死到 line editor 内部。
- 非交互、JSON、legacy terminal 路径无行为变化。

回归测试建议：

```powershell
cd backend
go test ./cmd/aicli/ui ./cmd/aicli/commands
```

如果修改了 terminal capability、surface 或 input editor 的底层逻辑，再补跑：

```powershell
cd backend
go test ./cmd/aicli/...
```

## 审查补充要点汇总

下列条目是对原方案的补齐，避免实施阶段出现语义歧义：

- `/s`、`/normal`、`/n` 是 `/stream` 的快捷命令（`ShortcutOf`），不是 alias；catalog 分别建档。
- catalog spec 增加 `AcceptsArgs` 和 `RequiresArgs`，控制 Tab 补全是否追加空格；`/help`、`/exit`、`/new` 等零参命令不追加。
- `applySlashCommandCompletion` 只替换 `[tokenStart, tokenEnd)` 区间，保留 token 前后所有内容；索引为 rune；replacement 用 catalog 规范形式。
- exact match 与 longer-prefix 并存时：`Tab` 在 exact 选中态为 no-op；`Enter` 在 exact 选中态按原行为提交；`Enter` 在 longer-prefix 选中态消费为接受候选。
- 匹配大小写无关，渲染与替换使用 catalog 规范形式。
- 非字母 alias（`/?`）必须被 query 正确识别。
- completion controller 与 pending paste preview 的 surface 占用有显式优先级：paste preview 优先。
- popup 固定最大高度 8 行，刷新前先 `ClearPopup` 再 `ShowPopup`，避免行数跳变导致 editor cursor anchor 偏移。
- `Update("")` 与 `Clear()` 幂等；入口和出口双重 `onChange("")` 不产生 popup 抖动。
- Phase 2 的 Enter 接受候选是显式 UX 变更；无候选时保持 `default` 分支输出。
- Esc 不可用时的降级：`Backspace` 把 query 删到空自动关闭 popup。
- 粘贴阈值改为「第一 token 长度阈值」而不是整体文本长度阈值。
- 非交互 / InputQueue / JSON / 非 TTY 路径不创建 controller、不 ShowPopup，作为 Phase 1 验收条目。
- 测试要求 catalog ↔ `handleCommand` 三类 dispatch 双向覆盖；Windows editor 路径单独覆盖 Tab 键行为。

## 最终建议

最优先落地 Phase 1。它能快速提供用户可见价值，并验证 popup 在真实输入过程中的稳定性；同时不会把风险集中到 line editor 按键改造上。

Phase 2 的 `Tab` 补全和 `Up/Down` 候选导航必须通过 editor hooks 做，不能在 `onChange` 里尝试直接改写输入或拦截方向键，因为当前 `onChange` 没有 cursor，也没有返回 replacement 或消费按键的通道。强行在 `onChange` 里操作 terminal 文本会破坏 editor 的单一状态源。

Phase 3 的参数补全应只在命令名补全稳定后推进。尤其是 `/resume`、文件路径、session id 这类动态候选必须谨慎处理性能和缓存，不能让每次 keypress 触发阻塞式 IO。

Linux/Windows 兼容性要作为每个阶段的验收条件，而不是上线前补测项。实现上应坚持“输入事件层消化平台差异，命令补全层只处理抽象 key kind 和字符串状态”，这样才能在 Windows Terminal、PowerShell、cmd、WSL、Linux terminal、VS Code terminal 和终端复用器之间保持可维护性。
