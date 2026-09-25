# CommandCode Interactive Mode 文档设计借鉴分析

- **日期**：2026-09-25
- **来源**：<https://commandcode.ai/docs/interactive-mode>（Command Code "Interactive Mode" 文档全文）
- **对照对象**：本仓库 `aicli` 交互式 TUI（`backend/cmd/aicli/ui/**`、`backend/cmd/aicli/commands/chat_*.go`）
- **结论口径**：区分「值得借鉴（缺口）」与「已有且更完整（不要回退）」；每条建议给出代码落点、验收判据与风险。所有 CommandCode 侧描述均取自原文行为，不含推测；所有 aicli 侧结论均附 `file:line` 证据。

---

## 1. TL;DR：可借鉴清单（结论先行）

| # | 主题 | CommandCode 做法 | aicli 现状（证据） | 建议 | 优先级 |
|---|------|------------------|--------------------|------|--------|
| 1 | 按键注册表与自定义 | `keybindings.json` 部分覆盖 + 稳定点分 action id + `/reload` 热加载 + 坏条目降级 + `/hotkeys` 自省 | **无**任何按键配置体系；键位硬编码分散在 `ui/inputbox_editor.go`、`ui/keyhandler.go`、transcript pager 各自的让位判断里（`grep -i keybind/hotkey/remap` 零命中；让位注释见 `chat_transcript_pager.go:9-21`） | 建立集中「按键 → action」注册表 + 用户覆盖文件 + `/hotkeys`；先纳管交互层 action，不动编辑器内部键 | **P0** |
| 2 | 权限模式循环键 | `shift+tab` 四档循环（default→accept-edits→plan→yolo），`alt+m` 作为 Windows 终端回退，`/mode:*` 直达 | 有 4 档模式与 `/permission-mode`（`/mode` 别名），**无循环快捷键**（`chat_permission_mode.go:14-25`、`command.go:607-620`） | 增加 cycle action：复用 bypass 二次确认，弹层/审批/选择器持有输入权时不抢占 | **P0** |
| 3 | 长文本粘贴折叠 | >300 字符折叠为 `[Text#N]`，提交仍发送全文，`collapsePastedText=false` 可关闭 | **已有**（阈值 1000、占位符 `[已粘贴 N 字符 / M 行]`、提交展开、编辑即丢弃：`composer_state.go:10/95-163`）；缺口只有**关闭开关** | 已补 `aicli.chat.collapse_pasted_text: false`（本次实施，见 §8） | **P0 → 已完成** |
| 4 | 图片粘贴与附件语义 | 平台按键矩阵、`[Image #N]` 令牌、删令牌即弃图、1200px 压缩、>32MB 跳过、失败给一行原因 | **路径附件已有**：`/attach <path>` + list/clear/remove（`command.go:689-748`）；**缺**剪贴板图片、内联令牌与发送前压缩；剪贴板文本读取仅 Windows（`inputbox_editor_windows.go:392-455`、`inputbox_editor_unix.go:36-38`） | 在既有 `/attach` 之上补剪贴板粘贴键 + 内联令牌 + 压缩/上限；Unix/WSL 先给**明确不可用提示**而非静默 | **P1** |
| 5 | `@` 文件 mention | `@` 路径补全 → 读入上下文，并带上文件目录到项目根之间的 `AGENTS.md` | 无 mention / 路径补全（composer 仅有通用 Tab 回调，供 slash 与参数补全使用，见 `inputbox_editor_completion_test.go:57-60`） | 实现 `@` 补全与最小上下文注入；与 foldertrust / workspace 边界对齐 | **P1** |
| 6 | 待批准命令的通俗解释 | 权限提示中 `ctrl+e` 把待执行 shell 命令用自然语言解释一遍（默认按需、可配置事前生成） | 无（`explainCommand`/`plain language` 零命中）；审批链路已有模式分级、复用与拒绝引导（`agent/permission_engine.go`、`agent/denial_guidance.go`） | 审批弹层增加「解释」动作，只读展示、不改变审批语义与默认焦点 | **P1** |
| 7 | 终端能力自省与回退 | kitty 键盘协议协商 `shift+enter`；输入框 `?` 显示**本终端实际生效**的按键；粘贴失败给一行原因 | 有 CSI modified-enter（`13;2/3/5`）与 Windows 控制台键事件（`inputbox_editor.go:2324-2330`、`inputbox_editor_windows.go:210-241`）；无协议协商、无按键自省、无失败原因提示（`kitty` 零命中） | 增加能力探测与帮助页「本终端生效键」；失败场景一律给原因行 | **P1** |
| 8 | 会话状态带 | feed / TODOS 带 / 输入 / 状态行四段；TODOS 常驻可管理 | 状态行已**强于**对标（mode、model+reasoning、provider、context 用量、balance、goal、队列；`chat_interaction.go:2833-2856` 等）；TODOS 带缺失（`ui/` 内 `todo` 零命中） | 复用既有 bottom pane 优先级策略增加紧凑 TODOS 带，可折叠、可让位 | **P2** |
| 9 | `esc esc` 快速回退 | 双击 `esc` 回到上一个 checkpoint | 无双击 esc；`/backtrack` 与 checkpoint 基础设施齐备，但 `/rewind` 非数字参数显式「尚未接线」（`command.go:314-326`） | 增加双击 esc 手势 → 打开回退选择器；先收口「未接线」路径 | **P2** |
| 10 | 交互模式文档 IA | 单页覆盖屏幕结构 / 按键 / 粘贴矩阵 / 终端选择 / 排错，并强调自省路径 | 内容散落在 `docs/aicli/install.md:640-712` 与 `docs/user-guide/aicli.md:100-109`；无按键参考、无终端矩阵、无「按键不生效」排错 | 新增 `docs/aicli/interactive-mode.md`，`/help` 指向；从 install.md 收敛引用 | **P2** |

> 反面对照（**已有且更强，不要回退**）见 §5：状态行信息密度、`ctrl+t` 全屏 transcript pager、`!`/`/shell` 输出的独立 turn 与截断/artifact 提示、54 个 slash 命令与参数级补全、ACP mode/model 通道、bypass 二次确认。

---

## 2. 来源文档的设计要点提炼

按「输入模型 / 状态可见性 / 模型语义 / 权限模式 / 输入人体工学 / 键位自定义 / 终端适配 / 文档结构」八组归纳。

### 2.1 输入模型：三字符 + 五键

| 形态 | 行为 |
|------|------|
| `/` 开头 | 打开 slash 命令菜单；**输入即排序：名字前缀 > 名字包含 > 描述匹配**；`↑/↓` 选择、`Enter` 执行、`Tab`/`→` 插入并补一个空格便于接参数、`Esc` 关闭 |
| `!` 开头 | Bash 模式：整行作为 shell 命令执行，**命令与输出都进入会话**（agent 能看到你看到的东西） |
| `@` 任意位置 | 文件路径补全；选中文件读入上下文；并**自动带上该文件目录到项目根之间的 `AGENTS.md`**（子目录记忆） |
| `shift+tab` | 权限模式循环：`default → accept-edits → plan → yolo → default`；`alt+m` 是 Windows 终端（Shift-Tab 送不到进程）的等价键 |
| `esc` | 停止正在运行的内容 |
| `esc esc` | 回退到上一个 checkpoint（等价 `/rewind`） |
| `ctrl+o` | 显示/隐藏完整工具输出 |
| `ctrl+g` | 把当前输入交给 `$EDITOR` 编辑 |
| `/hotkeys` | 列出**当前进程实际生效**的全部快捷键（含用户覆盖），`/help` 是短清单 |

### 2.2 状态可见性：屏幕分区与"始终同步"

- 自顶向下四段：feed（工具活动 + 回复）→ TODOS 带（`ctrl+x` 管理）→ 输入区 → 状态行（`当前文件 · model · mode`）。
- 「**banner 显示哪个模型，请求就用哪个模型**」被写成不变量（模型来源与请求解析同源），而不是各自展示。

### 2.3 模型选择语义：三层优先级 + 会话隔离 + resume 采纳

- `/model` 切换**当前会话**，并且**成为新会话的默认值**（下次启动沿用）；已打开的其它会话不被扰动（每个会话保持自己启动时的模型）。
- `--model`/`-m` 是**会话级 override，不改保存的默认值**；显式 `/model` 选择或 `/resume` 换会话会清除它。
- 解析顺序明确写出：`--model` flag（直到显式 `/model` 或 `/resume`）> 本会话 `/model` 选择（或 resume 采纳）> 默认值（本会话启动时快照的"最近一次 `/model` 选择"）> 内建默认。
- Resume 采纳规则分层：冷启动 `--resume/--continue` 采纳会话保存的模型（同一命令显式带 `--model` 则 flag 胜出）；TUI 内 `/resume` **总是**采纳目标会话模型，即使启动时带了 `--model`；**采纳是瞬时的，不写回默认值**——打开旧会话不会悄悄改变新会话的起始模型。
- 未知模型 id **启动前拒绝**；`--list-models` 给出全部合法 id。

### 2.4 权限模式

- 循环顺序显式且有限（4 档），`dont-ask` 明确声明**不在循环里**、只从 settings/`--permission-mode` 进入，从它按 `shift+tab` 会回到 `accept-edits` 重新入环。
- 提供 `/mode` 与 `/mode:default`、`/mode:accept-edits`、`/mode:plan` 这类**直达**写法，避免只能循环。
- 权限提示符里 `ctrl+e` 让**待批准的 shell 命令用自然语言解释一遍**再决定；`/config` 可配置「按需解释（默认）/ 事前全量生成」。
- 明确标注按键冲突与优先级（IDE 里 `ctrl+e` 被占用 → 用 `ctrl+y`；权限提示与 transcript 视图争用时谁优先）。

### 2.5 输入人体工学：粘贴、图片、多行、外部编辑器

- **长文本粘贴折叠**：>300 字符折叠成 `[Text#N]` 占位，**提交时仍发送全文**；`ctrl+g` 可打开真实内容编辑；`collapsePastedText=false` 可关闭。占位符让输入框保持可读，但语义上不损失内容。
- **图片粘贴矩阵**：按终端逐一定义"哪个键真正能拿到剪贴板图片"（macOS Terminal/iTerm2 `ctrl+v`/`cmd+v`；Ghostty/Kitty/Alacritty/WezTerm/Warp/VS Code `ctrl+v`；Windows Terminal `alt+v`；conhost/ConEmu/Cmder `ctrl+v`/`alt+v`；WSL `alt+v`；Wayland/X11 各自路径），并在**输入框右下角给出 20 秒的按键提示**、粘贴失败后重新出现、成功附加后消失。
- **失败要解释**：空粘贴、缺 `wl-clipboard`/`xclip`、SSH 下剪贴板在本地等，都在输入框下方给一行原因说明；同时承认"终端吞掉的键无法上报"（如 Ghostty 里只有图片时的 `cmd+v`），不假装成功。
- **`[Image #N]` 令牌语义**：光标处插入令牌；删除令牌文本 = 丢弃该图；`ctrl+c` 清空输入与所有待发附件；只发送令牌存活的图片；编号复用（删掉 #1 后下一个粘贴仍是 #1）；令牌可点击打开预览文件；预览目录 7 天清理。
- **发送前处理**：png/jpg/jpeg/gif/webp/bmp/tiff 全收；统一压到最长边 1200px（不放大小）、按需重编码（透明保 PNG，其余 JPEG）、>32MB 直接跳过；理由是"截图不应该吃掉上下文窗口"。
- **`@file.png` 是 mention 不是 attachment**：粘贴用于剪贴板，mention 用于仓库里已有的文件，两条路径语义分开讲清。
- **通用视觉**：文本模型遇到图片时走 `VISION` 侧调用（便宜的视觉模型描述图片），**会话模型不变**；首次询问、答案记忆在 `/config → Image vision`。
- **多行输入**：能识别 kitty keyboard protocol 的终端（列出 iTerm2 3.5+/Ghostty/Kitty/WezTerm/Alacritty/foot/Rio/Windows Terminal 1.25+）自动启用 `shift+enter`；不支持的终端给 `\` + `Enter` 回退（按下后反斜杠替换为换行）、`ctrl+j`、Terminal.app 的 `option+enter`；输入框里按 `?` 可以**看到你的终端实际拿到的是哪个快捷键**。
- **拖拽图片文件**：终端粘贴路径 → 识别为图片 → 从磁盘读取；引号、转义空格、`file://` URL、Windows 盘符/UNC 路径统一解包。

### 2.6 键位自定义：稳定 action id + 部分覆盖 + 优雅降级

- 单一可选文件 `~/.commandcode/keybindings.json`；**只写要改的项**，其余保持默认；会话内改完 `/reload` 立即生效。
- 语法：`modifier+key`（`ctrl`/`shift`/`alt|option|opt|meta`，可叠加、顺序无关），键名支持字母/数字/符号/命名键（`up/down/home/end/pageup/…`），大小写不敏感；一个 action 可绑定**字符串或数组**；绑定空数组 `[]` 表示禁用。
- action id 是**带命名空间的稳定点分 id**（`tui.editor.cursorUp`、`tui.input.submit`、`app.tools.expand`、`app.permission.cycle`…），文档按功能域给出「id | 默认键 | 作用」的完整参考表。
- **优雅降级**：文件缺失/某行写错 → 回退默认且**单个坏条目不拖垮整个文件**；旧短名（`cursorUp`）自动映射到点分 id；跨终端把 Home/End/Option-Arrow 的不同转义序列归一化后再匹配。
- **诚实边界**：单列一节「Fixed shortcuts」，明确哪些键属于具体界面、暂不可重映射，而不是假装全覆盖。

### 2.7 终端适配

- 直接给终端选型建议（WezTerm/Alacritty/Ghostty/Kitty：truecolor、超链接、bracketed paste、CSI u），并把 legacy console 的问题（颜色撕裂、滚动卡、快捷键送不到）说透。
- Windows Terminal 的 `ctrl+v` 抢占与移除配方；WSL 经 `powershell.exe` 读剪贴板；VS Code 系终端跑一次 `/terminal-setup` 让 `shift+enter`/`shift+tab` 送到进程。

### 2.8 文档结构

- 单页覆盖：屏幕结构图 → 三个字符 → 五个键 → slash 菜单 → 模型 → 权限模式 → 粘贴（文本/图片）→ 终端选择 → 编辑器 → 键位自定义 → 完整键位参考 → 固定快捷键 → See also。
- 所有小节都落到「可复制的配置片段 / 可执行的命令 / 症状→按键」三选一，并反复强调**自我发现路径**（`/hotkeys`、`?`、`/help`）。

---

## 3. aicli 现状盘点（代码证据）

> 口径：`exists` = 已有完整实现；`partial` = 有基础但缺对标语义；`missing` = 未发现实现（附搜索关键词）。

### 3.1 输入与命令面

| 维度 | 状态 | 证据 |
|------|------|------|
| `/` 命令目录与弹层 | exists | 目录 54 个顶层命令（含分组/别名/快捷键/隐藏项），弹层上限 8 行：`chat_slash_command_catalog.go`（54 处 `Name: "/…"`）、`chat_slash_completion.go:10-13` |
| 补全候选排序 | partial | 仅 `exact(0) > 命令前缀(10) > 别名/快捷键前缀(+20)`；**无"名字包含"匹配、无描述匹配**：`chat_slash_completion.go:295-335` |
| Tab 归属 | exists | 补全持有 Tab，不触发 plan mode 切换：`chat_composer_test.go:118-121` |
| 参数级补全 | exists | `/model`、`/profile`、`/agents routing` 等有枚举值补全：`chat_slash_argument_completion.go` |
| `!` Bash 模式 | exists | `!cmd` → 派发 `/shell cmd`，输出经 capture 后作为**独立 turn** 回送模型：`chat.go:1665-1674`、`chat_input_queue.go:825`、`chat_shell_command.go:10-35` |
| `@` 文件 mention / 路径补全 | missing | 搜索 `FileMention`/`AtMention`/`pathComplet`/`mention`（TUI 侧）零命中；composer 仅暴露通用 Tab 补全回调（`inputbox_editor_completion_test.go:57-60`） |
| 权限模式集合与切换命令 | exists | `default / accept_edits / plan / bypass_permissions`，`/permission-mode`（别名 `/mode`）：`chat_permission_mode.go:14-25`、`command.go:607-620` |
| 权限模式循环快捷键 | missing | 搜索 `PermissionModeCycle`/`shift+tab`/`alt+m` 零命中；`/mode` 仅支持显式值 |
| bypass 安全确认 | exists | 进入 `bypass_permissions` 需二次确认：`command.go:615`、`chat_command_result.go:1033-1042` |
| `esc` 中断 | exists | ESC 物理键通道 + 中断链路：`ui/keyhandler.go:11/88-100`、`chat_escape_interrupt.go` |
| `esc esc` 快速回退 | missing | 无双击检测（搜索 `doubleEsc`/`escEsc` 零命中）；`/backtrack` 可用，`/rewind` 非数字参数显式未接线：`command.go:314-326` |
| 全屏 transcript / 工具输出展开 | exists（形态不同） | `ctrl+t` 打开 alternate-screen 分页器并有严格让位条件：`chat_transcript_pager.go:9-21`；折叠/展开模型：`ui/cell/preview_expanded_test.go:28-64`、`ui/tool_fold_test.go:179-181` |
| 外部编辑器（`$EDITOR`） | partial | 仅 profile 生命周期使用 `$VISUAL`/`$EDITOR`（`chat_profile_lifecycle_ops.go:254`）；composer 无编辑器接棒 |
| bracketed paste | exists | 启用/关闭序列 + 空闲渲染等待：`inputbox_editor.go:41-42/205-208/881` |
| 剪贴板文本粘贴 | partial | Windows 原生 API 读取（重试、部分 UTF-16 校验）：`inputbox_editor_windows.go:392-455`；Unix 明确不支持：`inputbox_editor_unix.go:36-38` |
| 长文本粘贴折叠 | exists（**勘误**） | 单次粘贴超过 `LargePasteCharThreshold=1000`（`composer_state.go:10`）时折叠为 `[已粘贴 N 字符 / M 行]` 占位符；提交时展开全文，编辑/删除占位符即失配丢弃、同名占位符唯一化：`composer_state.go:95-163`，专测 `composer_state_test.go:31-136`。**剩余缺口只有关闭开关**（本次已补，见 §8） |
| 图片附件 | partial（**勘误**） | 路径附件已有：`/attach <path>` 添加、`/attach` 列表、`clear` 清空、`remove N` 移除，随会话发送：`command.go:689-748`。**缺**剪贴板图片 / 拖拽 / 内联令牌；`/image` 是图片**生成**（`chat_image_command.go:12-38`） |
| 多行输入 | exists | CSI modified-enter（`13;2/3/5`）与 Windows 控制台键事件均可产生换行；行编辑器内 `ctrl+o` 也插入换行：`inputbox_editor.go:2324-2330`、`inputbox_editor_windows.go:210-241`、`inputbox_editor_test.go:64` |
| 终端键盘协议协商 / 按键自省 | missing | 搜索 `kitty`/`CSI u`/`modifyOtherKeys`（TUI 侧）零命中；无「本终端生效键」提示 |
| 用户按键自定义 | missing | 搜索 `KeyBinding`/`keybindings`/`hotkeys`/`remap` 零命中（仅 ACP 注释提及 IDE keybinding） |

### 3.2 状态与会话面

| 维度 | 状态 | 证据 |
|------|------|------|
| 状态行信息密度 | exists（强于对标） | 已有 mode / model+reasoning / provider / context 用量 / balance / goal / 队列等分段：`chat_interaction.go:1191-1199`、`2833-2856`；`chat_balance_refresh_test.go:398-405` |
| TODOS 常驻带 | missing | `ui/` 内 `todo`（忽略大小写）零命中；todos 仅作为工具结果渲染：`chat_tool_rendering.go:359` |
| `/model` 的"会话 + 默认"双写 | exists | 同步当前 session；配置可写时写回 `aicli.chat`：`docs/aicli/install.md:326` |
| 会话级模型快照与 resume 恢复 | exists | `RequestedModel`/`EffectiveModel` 写入 session metadata，resume 读回：`chat_session.go:1106-1119`、`2060-2067`；`docs/aicli/install.md:321` |
| 帮助自省入口 | partial | `/?`（帮助组）与 `/help` 存在（`chat_slash_command_catalog.go:48`），但只列命令、不列**当前生效按键** |

---

## 4. 重点借鉴项：设计映射与落点

### 4.1 【P0】按键注册表 + 用户自定义键位（`/hotkeys` 自省）

- **CommandCode 语义**：单文件 `keybindings.json` 只写差异；稳定点分 action id；一个 action 可绑多个键、`[]` 禁用；文件缺失/某行写错 → 回退默认且**单个坏条目不拖垮整个文件**；`/reload` 热加载；`/hotkeys` 列出**当前实际生效**的快捷键；对不可重映射的键单列「Fixed shortcuts」。
- **aicli 现状**：没有任何按键配置面。键位判断分散在 `ui/inputbox_editor.go`（行编辑、reverse search、transpose）、`ui/keyhandler.go`（仅 ESC 物理键轮询）、`chat_transcript_pager.go`（让位条件）等处；已有严格的所有权纪律（`KeyHandler.Arm/Disarm/Suspend`、`canOpenChatTranscriptPager` 的 popup/approval/lease 判定）。
- **建议**：
  1. 新增 `ui/keymap` 包：`ActionID`（建议命名空间 `tui.editor.*` / `tui.input.*` / `app.*`，与现有 `ui/action.go` 的动作分类对齐）、`Scope`（global / composer / pager / picker）、默认键表、来源（default/user）。
  2. 读取 `~/.aicli/keybindings.json`（走 `internal/aiclipaths`，兼容 profile/win7 路径差异）；解析失败回退默认 + 一条 warning；未知 id 忽略并提示；`[]` 表示禁用。
  3. **仲裁优先级写入注册表**：审批/弹层 > 选择器/分页器 > slash 补全 > composer > 行编辑器内部键。运行时由单一按键路由分发，禁止新增独立轮询者（否则重演 Ctrl+T 类冲突，见 `docs/plan/aicli-tui-transcript-overlay-renderer-mode-plan.md` 的记录）。
  4. 新增 `/hotkeys`（并纳入 `/?` 帮助组）输出「action id | 当前键 | 默认键 | 是否被覆盖」；`/reload` 触发重载。
  5. 明确 **不可重映射清单**（终端差异键如 Backspace、`ctrl+c`、`ctrl+d`，以及固定界面键），写进 `/hotkeys` 与文档。
- **落点**：`backend/cmd/aicli/ui/keymap/`（新）、`ui/inputbox_editor*.go` 接入、`commands/chat_slash_command_catalog.go`（`/hotkeys`）、`commands/chat_command_result.go`。
- **风险**：最大风险是绕开既有输入所有权模型造成双消费者；必须「只做键→action 映射，不做新的 stdin 读取者」。
- **验收**：覆盖/禁用/坏 JSON/未知 id 四类单测；`/hotkeys` 输出与默认键表一致性回归（参考 `help_docs_regression_test.go` 风格）；`ctrl+t` 让位回归保持绿色。

### 4.2 【P0】权限模式循环键（`shift+tab`，Windows 回退 `alt+m`）

- **CommandCode 语义**：`shift+tab` 显式四档循环；`dont-ask` 声明为环外；`alt+m` 专治 Windows 终端 Shift-Tab 送不到；`/mode:*` 直达。
- **aicli 现状**：四档模式与 `/permission-mode`（别名 `/mode`）已存在，bypass 已有二次确认；**没有循环键**。状态行已展示 mode 段。
- **建议**：新增 `app.permission.cycle`（默认 `shift+tab`，备选 `alt+m`）；循环顺序 `default → accept_edits → plan → bypass_permissions → default`；进入 bypass 复用现有 `confirmBypassPermissionModeChange`；审批弹层/选择器/分页器活跃时不响应；切换后刷新状态行 mode 段与提示。
- **落点**：`ui/keymap`、`commands/command.go:607-620`（抽出可复用的 `applyChatPermissionMode`）、`commands/chat_command_result.go`。
- **风险**：把 bypass 放进循环会让"多按一次"直达高危模式——保留确认并确保确认默认焦点是取消（现状如此，回归测试需守住）。
- **验收**：循环顺序单测；取消确认后模式不变；弹层中按键不生效；`/mode plan` 与循环键结果一致。

### 4.3 【P0，已完成】大段粘贴折叠的「关闭开关」

- **CommandCode 语义**：>300 字符折叠为占位符，**提交仍发送全文**；可配置关闭；`ctrl+g` 查看/编辑真实内容。
- **aicli 现状（勘误）**：折叠**早已存在**，比我最初判断的更强——阈值 `LargePasteCharThreshold=1000`，占位符带字符数与行数（`[已粘贴 N 字符 / M 行]`），提交时按跟踪区间展开全文，编辑或删除占位符即自动失效（`composer_state.go:10/95-163`）。当时误判是因为搜索了英文 `collapsePasted`/`Text#` 而没有搜「已粘贴」与 `LargePasteCharThreshold`。
- **真实缺口与建议**：只缺 CommandCode 的 `collapsePastedText=false` 逃生口；已新增 `aicli.chat.collapse_pasted_text` 并透传到 `LineEditorHooks.CollapsePastedText`（见 §8）。
- **落点**：`ui/inputbox_editor.go`（折叠与渲染）、`commands/chat_composer.go`（提交前展开）、`commands/chat_send.go`（保证请求体为全文）、config。
- **风险**：折叠态与光标/选区/历史记录/多行草稿的交互复杂，需限定「只在单次粘贴块 > 阈值时折叠」；命令参数（`/shell` 等）与代码块粘贴不应破坏原意。
- **验收**：粘贴 >300 字符 → 单行占位且提交体含全文；删除占位符 = 不发送；关闭开关后行为与现状一致；编号复用单测。

### 4.4 【P1】图片粘贴与附件令牌

- **CommandCode 语义**：按终端矩阵给出真正可用的按键；`[Image #N]` 令牌语义（删令牌即弃图、`ctrl+c` 清空、只发存活令牌、编号复用、可点击预览）；发送前统一压到 1200px（不放大小）、透明保 PNG、其余 JPEG；>32MB 跳过；失败给一行原因；`@file.png` 仅作 mention。
- **aicli 现状（勘误）**：**路径附件通道已存在**——`/attach <path>` 添加、`/attach` 列表、`clear`、`remove N`，`session.ImagePaths` 随会话发送（`command.go:689-748`）。真实缺口是**剪贴板图片粘贴**与**内联令牌**：剪贴板读取仅 Unicode 文本且 Unix 直接返回不支持（`inputbox_editor_windows.go:392-455`、`inputbox_editor_unix.go:36-38`）。
- **建议（两步走）**：
  - **第一步（低风险）**：能力探测 + 诚实提示——有图但平台不支持时，在输入框下方给一行原因；把「当前终端哪个键能贴图」做成运行时提示（对齐 4.7）。
  - **第二步**：新增附件状态与 `[Image #N]` 令牌；Windows 扩展 `CF_DIB`/PNG 读取；提交时装配多模态请求并做压缩/上限（1200px、>32MB 跳过）；预览落临时目录并按期清理。
- **落点**：`ui/inputbox_editor_windows.go`/`_unix.go`、composer 附件状态、`commands/chat_send.go`、provider 适配层（image part 支持面）、config。
- **风险**：上下文成本与 provider 兼容性（非视觉模型必须有明确策略：先「拒绝并说明」，把"侧调用描述图片"留到 P2）；压缩实现若引入外部二进制会破坏 win7/无依赖约束，优先纯 Go 解码+缩放。
- **验收**：有图/无图/超限/平台不支持四条路径各有单测与用户可见提示；压缩后最长边 ≤1200px；删令牌不发送；编号复用正确。

### 4.5 【P1】`@` 文件 mention 与路径补全

- **CommandCode 语义**：`@` 打开路径补全；选中即入上下文；并把该文件目录到项目根之间的 `AGENTS.md` 一并带上；同时明确「`@x.png` 是 mention，不是附件」。
- **aicli 现状**：无 mention 与路径补全；composer 仅提供通用 Tab 补全回调（现由 slash/参数补全占用）；提示层已有 AGENTS.md 层次概念（`internal/prompt/project_instructions.go:12`）。
- **建议**：`@` token → 受 workspace 边界与 foldertrust 约束的路径补全；提交时以「路径引用 + 可选内容注入」两种语义明确区分（默认路径引用，由模型自行 `view`；提供注入开关）；注入时附带最小化上下文（文件目录到根的 AGENTS.md 片段）并复刻截断提示风格。
- **落点**：`ui/inputbox_editor.go`（补全触发）、`commands/chat_slash_argument_completion.go`（复用补全设施）、`commands/chat_send.go`（注入）、`internal/prompt/project_instructions.go`（层次读取）。
- **风险**：大文件注入爆上下文（必须有上限与可见截断提示）；越界读取需与 foldertrust/工作区策略一致。
- **验收**：补全候选不出 workspace；注入超限有截断提示；子目录 AGENTS.md 命中单测；`@` 出现在命令行参数中不被误判。

### 4.6 【P1】审批前「通俗解释待执行命令」

- **CommandCode 语义**：权限提示中 `ctrl+e` 用自然语言解释待执行的 shell 命令；默认按需解释，可配置为事前全量生成；IDE 终端冲突时用 `ctrl+y` 兼容。
- **aicli 现状**：无解释能力（零命中）；但审批链路完备——模式分级、拒绝引导（`internal/agent/denial_guidance.go`）、审批复用（`commands/chat_approval_reuse.go`）、等待审批的呈现与恢复都有既有实现。
- **建议**：审批弹层增加 `explain` 动作（默认按需，仅按键触发）；解释结果以只读块展示在弹层内/下方，**不改变默认焦点与审批语义**；调用失败/超时给一行「无法解释」并保持审批可用；按键默认 `ctrl+e`、备选 `alt+e`（IDE 终端占用时）。
- **落点**：审批弹层输入路由（`ui/` 弹层 + `commands/chat_*approval*`）、解释提示构造器（可放 `internal/agent/` 或 commands 层）。
- **风险**：额外 LLM 调用带来延迟与成本 → 只在按键时调用 + 超时降级；解释可能误导 → 标注「模型生成，仅供参考」。
- **验收**：不按键不发请求；超时/失败降级；解释不改变审批结果与默认选项；`plan mode` 与 bypass 确认场景均可解释且不互相干扰。

### 4.7 【P1】终端能力自省与失败原因提示

- **CommandCode 语义**：终端协议协商（kitty keyboard protocol）决定 `shift+enter` 是否可用；输入框 `?` 显示**本终端实际生效**的快捷键；粘贴失败（空剪贴板、缺 `wl-clipboard`、SSH）在输入框下方给一行原因。
- **aicli 现状**：modified-enter 的 CSI 解析与 Windows 控制台键事件已具备（`inputbox_editor.go:2324-2330`、`inputbox_editor_windows.go:210-241`），但没有协议协商、没有能力缓存、没有失败原因提示。
- **建议**：启动/首次交互时探测终端能力（超时降级，失败即按最小能力集处理）；`/hotkeys` 与输入框 `?` 展示「本终端生效键 + 原因」；粘贴/图片/剪贴板失败统一走一行 notice（原因 + 建议按键），持续到成功或 20 秒后消退。
- **落点**：`ui/terminal*.go`（探测序列与缓存）、`ui/keymap`（展示）、bottom pane 提示行（`ui/bottom_pane_row_plan.go` 已有行计划机制）。
- **风险**：协议查询会向终端写控制序列，老终端可能显示垃圾字符 → 必须白名单/黑名单 + 严格超时；Windows 7 legacy console 与 `win7compat` 构建需保持可用。
- **验收**：legacy 终端不产生可见垃圾；能力表在 Windows Terminal/WSL/VS Code 终端下与实际按键一致；提示行不破坏既有底栏行预算测试。

### 4.8 【P2】TODOS 常驻带与「当前文件」信息位

- **CommandCode 语义**：屏幕中部常驻 TODOS 进度带（`ctrl+x` 管理）；状态行含当前文件。
- **aicli 现状**：状态行信息已很丰富；但**没有 TODOS 带**，todos 仅作为工具结果出现（`chat_tool_rendering.go:359`）。
- **建议**：把「本次会话 todos 摘要」做成可选紧凑带，仅在存在未完成 todos 时占位；复用 `bottom_pane_row_plan.go` / `bottom_pane_layout_policy.go` 的让位顺序（先牺牲留白、再牺牲历史行），并提供展开查看（复用 transcript pager 或 `/todos` 命令）。「当前文件」信息位可跟随最近一次文件写入/读取工具动作更新。
- **落点**：`ui/bottom_pane_row_plan.go`、`ui/app_layout.go`、`commands/chat_tool_rendering.go`（todos 源数据）、commands 侧状态段构造（`chat_interaction.go`）。
- **风险**：底栏空间预算已有大量回归测试（`fixed_bottom_surface_*`、`bottom_pane_layout_matrix_test.go`）——新增行必须补进这些矩阵，否则易引入遮挡/重绘回归。
- **验收**：短终端下让位顺序符合既有 policy；无 todos 时不占行；`/todos` 与带内摘要一致。

### 4.9 【P2】`esc esc` 快速回退（并收口 `/rewind` 未接线）

- **CommandCode 语义**：双击 `esc` 回到上一个 checkpoint。
- **aicli 现状**：`/backtrack` 与 checkpoint/恢复基础设施齐备；无双击手势；`/rewind` 非数字参数路径显式「尚未接线」（`command.go:314-326`）。
- **建议**：批次 0 先收口 `/rewind` 分支（实现 checkpoint-id 恢复，或从帮助中移除并指向 `/backtrack`）；随后增加双击 esc 手势：**turn 运行中** `esc` 仍是中断，**空闲时** 时间窗内第二次 `esc` 打开回退选择器（`chat_backtrack_select.go`），不静默改文件。
- **落点**：`ui/keymap`（双击判定与状态门控）、`commands/chat_escape_interrupt.go`、`commands/chat_backtrack_command.go`。
- **风险**：中断与回退的状态语义冲突 → 第一个 esc 不得产生副作用；选择器取消后不得留下部分回退。
- **验收**：运行中 esc 仍中断（回归保持）；空闲双击打开选择器且不自动应用；时间窗与状态门控单测。

### 4.10 【P2】交互模式文档 IA

- **CommandCode 语义**：单页覆盖屏幕结构、三个输入字符、五个常用键、slash 菜单、模型、权限、粘贴（文本/图片）、终端选择、编辑器、键位自定义、完整参考、固定快捷键、See also；每段都落到「可复制配置 / 可执行命令 / 症状→按键」。
- **aicli 现状**：slash 命令清单与 session/resume 细节散落在 `docs/aicli/install.md:640-712` 与 `docs/user-guide/aicli.md:100-109`；没有按键参考页、没有终端矩阵、没有「按键不生效」排错。
- **建议**：新增 `docs/aicli/interactive-mode.md`，结构对齐：屏幕结构 → 输入模式（`/`、`!`、`@`）→ 快捷键表（含 `/hotkeys` 指引）→ 权限模式 → 粘贴与附件 → 终端选择与 Windows/WSL 注意 → 键位自定义 → 排错 → 参考。`docs/aicli/README.md` 登记，`/help` 与 `/hotkeys` 输出指向该页；install.md 保留摘要并引用。
- **验收**：帮助文本指向文档的回归测试（参考 `help_docs_regression_test.go` 风格）；文档中的每个命令与默认键都有代码/测试依据。

---

## 5. 已有且更强：不要回退的清单

| 能力 | aicli 现状 | 为什么优于对标 |
|------|------------|----------------|
| 状态行信息密度 | mode / model+reasoning / provider / context 用量 / balance / goal / 队列（`chat_interaction.go:2833-2856`、`chat_balance_refresh_test.go:398-405`） | 对标只有 `file · model · mode`；新增信息位应走既有优先级与紧凑标签（full/compact）机制，而非替换 |
| 全屏 transcript | `ctrl+t` alternate-screen pager + ScreenLease + 语义快照 + 严格让位（`chat_transcript_pager.go:9-21`） | 比 `ctrl+o` 就地展开更稳（独立屏幕、一次完整重绘）；两者可共存：pager 看全量、就地折叠看上下文 |
| Bash 模式输出回送 | `!` → `/shell`，输出作为**独立 turn** 回送，带 capture limit 截断提示 + 原始输出 artifact 落盘（`chat_shell_command.go:10-58`） | 比"命令与输出直接混入会话"更可控：截断可见、原始件可追溯 |
| Slash 数据模型 | 54 命令 + 分组/别名/快捷键/隐藏项 + 参数级值补全（`chat_slash_command_catalog.go`、`chat_slash_argument_completion.go`） | 缺的只是排序规则中的 contains/描述匹配（4.1 之外的独立小改进），不要简化既有目录结构 |
| 审批安全默认 | bypass 需二次确认 + 拒绝引导 + 审批复用（`command.go:615`、`agent/denial_guidance.go`、`chat_approval_reuse.go`） | 对标把 `yolo` 直接放进 `shift+tab` 循环；接入循环键时必须保留确认，不得为"顺滑"削弱 |
| ACP 控制通道 | IDE 客户端可经 mode/model config option 切换（`agent_stdio_mode.go`、`agent_stdio_config_option.go`） | 对标未提及；属既有优势，新增按键能力需与 ACP 通道语义一致（同一动作两个入口） |
| 兼容与分层 | profile/工作区/配置分层、Windows 7 兼容构建（`win7compat`） | 图片/剪贴板等新能力选型必须不破坏 win7 构建（优先纯 Go 实现，避免新增外部二进制依赖） |

---

## 6. 实施批次与验收

| 批次 | 内容 | 依赖 | 规模 | 退出判据 |
|------|------|------|------|----------|
| 批次 0 | 收口 `/rewind` 未接线分支；确认 keymap 与 `ui/action.go` 动作分类/输入所有权模型的映射 | — | 0.5–1 天 | `/rewind` 帮助与实际行为一致；keymap 设计评审通过（标注不可重映射清单） |
| 批次 1（P0） | 4.1 按键注册表 + `/hotkeys` + `keybindings.json`；4.2 权限循环键；4.3 粘贴折叠**关闭开关**（折叠本体早已存在） | 批次 0 | 2–4 天 | **已完成（2026-09-25，见 §8）**：单测覆盖 keymap 解析/覆盖/降级、动作路由认领与回落、权限循环顺序与 bypass 确认、`/hotkeys` 输出与 reload、折叠开关 |
| 批次 2（P1） | 4.5 `@` mention；4.6 审批解释；4.7 终端能力自省；4.4 第一步（能力探测与诚实提示） | 批次 1（按键与提示行） | 3–6 天 | 各能力在 Windows Terminal / WSL / legacy console 三档实测有明确行为与提示 |
| 批次 3（P1/P2） | 4.4 第二步（图片附件与压缩）；4.8 TODOS 带；4.9 `esc esc`；4.10 文档 | 批次 2 | 3–6 天 | 图片四路径单测；底栏矩阵回归；文档回归测试指向正确 |

**通用验收纪律**：新增按键一律先注册再分发（禁止旁路 stdin 消费者）；新增底栏行必须补进 `bottom_pane_layout_matrix_test.go` 与 `fixed_bottom_surface_*` 矩阵；新增用户可见字符串保持现有中文风格；文档与帮助同步更新。

---

## 7. 附录：对标按键与 aicli 现状对照

| CommandCode | 作用 | aicli 现状 |
|---|---|---|
| `/` | slash 菜单（前缀 > 包含 > 描述） | 有菜单；排序仅精确/前缀/别名（缺包含与描述匹配） |
| `!` | Bash 模式 | **有**（→ `/shell`，独立 turn 回送） |
| `@` | 文件 mention + 子目录 AGENTS.md | 无 |
| `shift+tab` / `alt+m` | 权限模式循环 / Windows 回退 | 无（仅 `/permission-mode`、`/mode`） |
| `esc` / `esc esc` | 停止 / 回退 checkpoint | 有停止；无双击回退（`/backtrack` 可用） |
| `ctrl+o` | 全量工具输出 | 形态不同：`ctrl+t` 全屏 pager；行编辑器内 `ctrl+o` = 换行 |
| `ctrl+g` | 交 `$EDITOR` 编辑输入 | 无（仅 profile 编辑用 `$VISUAL/$EDITOR`） |
| `ctrl+x` | TODOS 管理 | 无（todos 仅工具结果） |
| `/hotkeys` | 当前生效按键自省 | 无（`/?` 只列命令） |
| `[Text#N]` | 长粘贴折叠 | **已有等价实现**：`[已粘贴 N 字符 / M 行]` + 提交展开 + 编辑即丢弃；本次补了 `collapse_pasted_text` 关闭开关 |
| `ctrl+v`/`alt+v` + `[Image #N]` | 剪贴板图片附件 | 路径附件已有（`/attach`）；无剪贴板图片与内联令牌（剪贴板文本读取仅 Windows） |
| `?`（输入框） | 本终端实际生效快捷键 | 无 |

---

## 8. 实施状态（2026-09-25，本次改动）

按 §6 的批次 0/1 已落地以下内容；未完成项保持原优先级。

### 8.1 已完成

| 项 | 变更 | 落点 | 测试 |
|---|---|---|---|
| 按键注册表 | 新增 `ui/keymap` 包：稳定 action id、`Chord` 解析与规范化（大小写/顺序无关、命名键别名）、默认绑定、用户覆盖合并（字符串 / 字符串数组 / `[]` 禁用）、未知动作与坏 JSON 与坏按键只告警降级、chord 冲突告警 | `backend/cmd/aicli/ui/keymap/keymap.go` | `keymap_test.go`：解析、覆盖替换、禁用、坏条目互不影响、坏 JSON 回退、缺文件零告警 |
| 编辑器动作路由 | `editorKey.chord` + `LineEditorHooks.ActionForChord` / `OnActionKey(claimed, exitEditor)`；解码新增 `shift+tab`（CSI Z / CSI 1;2Z）与 `alt+m`；未认领动作自动回落到原语义（`ctrl+t` 仍是 transpose），需要接管屏幕时按既有 `ErrInteractiveInputTranscriptRequested` 退出 | `ui/inputbox_editor.go`、`ui/inputbox_editor_hooks.go` | `ui/inputbox_keymap_test.go`：按键解码、认领吞键、exitEditor 保留草稿、未认领回落 |
| 权限模式循环键 | 新增 `app.permission.cycle`（默认 `shift+tab`、`alt+m`），顺序 `default → accept_edits → plan → bypass_permissions → default`；进入 bypass 复用既有二次确认；命中即消费按键（含取消确认场景） | `commands/chat_permission_mode.go`、`commands/chat_composer.go` | `chat_hotkeys_command_test.go`：顺序表、消费语义、nil 会话、bypass 非交互拒绝 |
| `/hotkeys` | 展示当前生效绑定（含 `[用户覆盖]`）、固定快捷键清单、配置文件路径、配置告警与可重映射范围；`/hotkeys reload` 重新读取配置；catalog 与 `/help` 同步登记 | `commands/chat_hotkeys_command.go`、`commands/chat_slash_command_catalog.go`、`commands/command.go` | 同上：AICLI_HOME 用户覆盖展示、reload 路径、未知子命令报错 |
| 粘贴折叠关闭开关 | `aicli.chat.collapse_pasted_text: false` 关闭折叠（默认保持折叠），透传到 `LineEditorHooks.CollapsePastedText` → `ComposerState.SetCollapseLargePaste` | `internal/agentconfig/config.go`、`ui/composer_state.go`、`ui/inputbox_editor.go`、`commands/chat_composer.go`、`commands/chat_keymap.go` | `ui/inputbox_keymap_test.go`：编辑器级关闭后全量回显与提交；`ComposerState` 级原样插入 |
| `/rewind` 收口 | 文案明确「仅支持数字 user turn 序号与 list/select；checkpoint-id 直接恢复未提供」，不再暗示可用；帮助仍指向 `/backtrack` | `commands/command.go` | 既有 backtrack/rewind 回归 |

### 8.2 对前文结论的勘误（重要）

- **§1 #3 / §3.1 / §4.3**：大段粘贴折叠 **并非缺失**。`ui/composer_state.go` 早已实现等价且更强的语义：阈值 `LargePasteCharThreshold=1000`、占位符 `[已粘贴 N 字符 / M 行]`、提交按跟踪区间展开全文、编辑或删除占位符即失效、同尺寸占位符唯一化。真实缺口只有「关闭开关」，本次补齐。
- **§1 #4 / §3.1 / §4.4**：图片**路径附件**已存在（`/attach <path>`、列表、`clear`、`remove N`，随会话发送）。真实缺口是剪贴板图片粘贴、内联令牌语义与发送前压缩/上限。
- **复用教训**：判定「缺失」时不能只搜对标文档的英文命名（`collapsePasted`/`Text#`），必须同时搜实现侧的中文占位符与常量名（`已粘贴`、`LargePasteCharThreshold`），否则会重复造轮子。

### 8.3 未完成（保持原优先级）

- P1：4.5 `@` mention（补全 + 语义）与 4.4 第二步（剪贴板图片 + 内联令牌）。
- P2：4.8 TODOS 带。

### 8.4 批次 2 已实施（第二轮）

| 项 | 变更 | 落点 | 测试 |
|---|---|---|---|
| 4.7 终端能力自省 | 新增 `ui/termcaps` 包：按环境变量识别 Windows Terminal / WSL / VS Code / WezTerm / ConEmu / mintty / iTerm2 / Apple Terminal / 传统 conhost / xterm / 未识别，并给出 `shift+tab`、`alt+m`、多行输入、bracketed paste、剪贴板文本/图片的可用性与**一行原因**（未知按保守值，非 TTY 追加说明） | `ui/termcaps/termcaps.go` | `termcaps_test.go`：各终端矩阵、传统 conhost 降级原因、VS Code 抢占提示、WSL 宿主识别、非 TTY 说明 |
| 4.7 集成 | `/hotkeys` 增加「本终端」一节（逐项可用性 + 原因 + 备注），headless 下如实标注不适用 | `commands/chat_hotkeys_command.go` | `chat_hotkeys_command_test.go` |
| 4.6 审批前通俗解释 | 新增启发式解释器（动作类别 + 目标 + 影响范围），覆盖删除/丢弃改动/推送/安装依赖/下载执行/改权限/写设备/Git 索引/破坏性 DB/容器删除 + 文件写删移/网络/MCP/子 agent/后台任务/只读；识别不出**不输出**，参数不可解析时只留类别并指向 `[3]` | `commands/chat_approval_explain.go`，接线于 `chat_runtime_events.go:approvalPriorityPromptLines`（两条审批入口共用） | `chat_approval_explain_test.go`：分类表、未知工具静默、坏 JSON、长目标截断、提示行顺序 |
| 4.10 交互文档页 | 新增 `docs/aicli/interactive-mode.md`：按键分层与覆盖规则、终端能力矩阵、审批提示结构、粘贴与附件语义、输入所有权、排障表；已从 `install.md` 链接 | `docs/aicli/interactive-mode.md` | 文档，无编译面 |
| 4.4 第一步（部分） | 剪贴板图片的**诚实提示**由能力矩阵承担（「暂未实现剪贴板图片；可先用 `/attach <path>`」）；粘贴时刻的即时提示仍需编辑器提示行机制，未做 | `ui/termcaps`、`docs/aicli/interactive-mode.md` | 同上 |

### 8.5 结论修正与遗留清单

- **4.9（`esc esc` 快速回退）判定为无需实现**：编辑器在空输入下按单个 `esc` 已经返回 `ErrInteractiveInputBacktrackRequested`，由 `chat.go:1624-1628` 打开 user-turn 回退选择器，语义已覆盖对标行为；再加双击只会引入「第一次 Esc 要不要清空输入」的歧义。
- **4.5（`@` mention）未实施**：需要复用/扩展 `chatSlashCompletionController` 的弹层状态机（`ApplyCompletion` / `ApplySubmission` / `Navigate` / `Cancel` + 固定表面 popup 投影），并新增「路径引用 vs 内容注入」的语义决策与注入上限；属于独立批次，不宜以半成品合入。
- keymap 纳管范围说明：当前只注册 `app.permission.cycle` 与 `app.transcript.pager`；解码器仅对 `shift+tab`、`alt+m`、`ctrl+t`、`ctrl+o`、`tab`、`enter`、`backspace` 产出 chord，行编辑器内部键（Emacs 风格移动/删除）保持固定，并由 `/hotkeys` 的「固定快捷键」表如实列出。
