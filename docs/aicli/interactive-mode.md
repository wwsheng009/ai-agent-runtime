# aicli 交互模式：按键、终端能力与审批

本文是 `aicli` / `aicli chat` 交互模式的权威说明，覆盖按键分层、终端能力矩阵、审批提示结构与粘贴/附件语义。Slash 命令总表见 [install.md](./install.md#chat-内置斜杠命令补充)。

## 1. 按键分层

按键分三层，**只有第二层可被用户重映射**：

| 层 | 内容 | 是否可重映射 |
|---|---|---|
| 固定键 | `enter` 提交、`esc` 中断/回退、`ctrl+c` 中断、`ctrl+d` 退出、`ctrl+j`/`ctrl+o` 换行、`tab`（slash 补全 / `@` 路径补全 / plan 模式）、方向键导航 | 否 |
| 动作键 | `app.permission.cycle`（默认 `shift+tab`、`alt+m`）、`app.transcript.pager`（默认 `ctrl+t`）、`app.attach.clipboard_image`（默认 `alt+v`） | 是 |
| 行编辑器内部键 | Emacs 风格移动/删除（`ctrl+a`、`ctrl+e`、`ctrl+w`、`ctrl+k`、`ctrl+t` 未认领时的 transpose 等） | 否 |

固定键保持固定的原因：其中一部分是终端差异键（例如 Windows 控制台把 `shift+enter` 送成普通回车），重映射会制造"看起来生效、实际不通"的假象。当前生效表用 `/hotkeys` 查看。

### 1.1 覆盖快捷键

配置文件：`$AICLI_HOME/keybindings.json`；未设置 `AICLI_HOME` 时为 `~/.aicli/keybindings.json`。

```json
{
  "app.permission.cycle": ["alt+c"],
  "app.transcript.pager": "f2"
}
```

规则：

- 只写要改的动作，未提及的保持默认；绑定值可以是字符串或字符串数组，空数组 `[]` 表示禁用。
- 动作 id 必须已知；未知动作、坏 JSON、无法解析的按键都**只告警并回退默认**，不会导致启动失败。告警在 `/hotkeys` 的"配置提示"里展示。
- 同一 chord 绑定多个动作时按声明顺序生效，并在 `/hotkeys` 输出冲突告警。
- 修改后执行 `/hotkeys reload` 生效（注册表是进程级缓存）。

可重映射范围目前限于解码器能产出的 chord：`shift+tab`、`alt+m`、`ctrl+t`、`ctrl+o`、`tab`、`enter`、`backspace`。把动作绑到其它 chord（例如 `f2`）不会生效——终端不上报该序列，`/hotkeys` 会如实显示"不可用"。

## 2. 本终端能力矩阵

`/hotkeys` 的"本终端"一节给出当前终端的逐项能力与原因（基于 `ui/termcaps` 的环境探测）：

| 终端 | shift+tab | 多行输入 | 粘贴 | 剪贴板文本 | 剪贴板图片 |
|---|---|---|---|---|---|
| Windows Terminal / WSL | 可用 | 可用 | 可用 | Windows 可用 / WSL 不可用 | Windows 可用（CF_DIB/CF_DIBV5）；WSL 由宿主终端决定 |
| VS Code 集成终端 | 可用（IDE 可能抢占） | 可用 | 可用 | 同上 | 同上 |
| WezTerm / ConEmu / iTerm2 / Apple Terminal / xterm | 可用 | 可用 | 可用 | Windows 可用 / 其它不可用 | 按平台（见下） |
| mintty / Git Bash | 可用 | 可用 | 可用 | 可用 | 按平台（见下） |
| Windows 传统控制台 (conhost) | **不承诺**（用 `alt+m`） | **不可用**（用 `ctrl+j`/`ctrl+o`） | **不识别 bracketed paste** | 可用 | 按平台（见下） |
| 未识别终端 | 不承诺（保守） | 按可用处理 | 按可用处理 | 按平台 | 按平台（见下） |

说明：

- "剪贴板文本不可用"的终端请使用终端自身的粘贴（`Ctrl+Shift+V` / `Cmd+V`），它走 bracketed paste，粘贴折叠与提交语义一致。
- "剪贴板图片"由运行平台决定：Windows 走剪贴板位图（CF_DIB/CF_DIBV5）、macOS 走 `osascript`、Linux 需 `wl-paste` 或 `xclip`；缺失时 `/hotkeys` 会说明原因并给出 `/attach <path>` 退路。
- 非 TTY（headless / `--output json`）下按键矩阵不适用，`/hotkeys` 会带一行说明。
- `ui/termcaps` 只读环境变量、不做副作用调用；剪贴板图片能力是运行时事实（平台 + 外部工具），由 `/hotkeys` 命令层按 `clipboardimage.Availability()` 覆盖该行。

## 3. 权限模式与审批

### 3.1 权限模式

`/permission-mode`（别名 `/mode`）显式切换；`shift+tab` / `alt+m` 循环切换，顺序为：

```
default → accept_edits → plan → bypass_permissions → default
```

进入 `bypass_permissions` 仍需二次确认（与 `/yolo` 相同的确认流程）；循环键在确认弹层打开时已被按键处理器消费，不会漏进编辑器。审批复用策略见 `/approval-reuse`。

### 3.2 审批提示结构

审批面板按固定顺序展示：

```
[审批] Agent 请求执行需要授权的操作
[审批] 工具：execute_shell_command
[说明] 动作：删除文件或目录（递归）          ← 通俗解释（启发式）
[说明] 目标：rm -rf build/（完整内容用 [3] 查看）
[说明] 影响：命令涉及绝对路径或上级目录，影响范围可能超出当前项目
[审批] 原因：当前权限模式要求在执行前获得确认（permission_mode_requires_approval）
[审批] 风险等级：高（high）
[审批] 上下文：team=team-1 permission_mode=default
[审批] 命令：rm -rf build/
[审批] 操作：[1] 仅本次允许  [2] 拒绝  [3] 查看完整参数
```

通俗解释是**纯启发式规则**（动作类别 + 目标 + 影响范围），不调用模型：

- 覆盖删除、丢弃改动、推送远端、安装依赖、下载并执行、改权限、写设备、改 Git 索引、破坏性数据库操作、容器/集群删除，以及文件写/删/移动、网络访问、MCP 调用、子 agent、后台任务、只读读取。
- 识别不出来时**不输出解释行**（宁可不解释，也不猜错）。参数不可解析时只保留动作类别，并提示用 `[3]` 查看原文。
- 解释只用于帮助判断，不改变任何审批结果或权限判定。

## 4. 粘贴与附件

### 4.1 大段粘贴折叠

单次粘贴超过 1000 字符时折叠为占位符（`[已粘贴 N 字符 / M 行]`），**提交时发送全文**；编辑或删除占位符即视作丢弃（不会发送）。关闭折叠：

```yaml
aicli:
  chat:
    collapse_pasted_text: false
```

### 4.2 附件

- 文本/图片**路径**附件：`/attach <path>` 添加、`/attach` 列表、`/attach clear` 清空、`/attach remove N` 移除；随会话发送。
- 剪贴板**图片**：`alt+v`（可重映射的 `app.attach.clipboard_image`）或 `/attach paste` 读取剪贴板位图，落盘为临时 PNG（`aicli-clipboard-*.png`）后加入待发送附件；`/attach` 列表里可见、`/attach remove N` 可移除。
  - 仅读取**位图**：Windows 直接读 CF_DIB/CF_DIBV5（24/32 位、BI_RGB/BI_BITFIELDS、上下两种行序）；macOS 用 `osascript` 取 PNGf；Linux 依次尝试 `wl-paste`、`xclip`。
  - 不抢占终端自己的粘贴键（`ctrl+v` / `ctrl+shift+v` 仍由终端处理文本）；剪贴板里没有图片时只提示，不改动草稿。
  - 读取失败的原因会直接写状态行（没有图片 / 平台不支持 / 超时 / 其它），并始终给出 `/attach <path>` 退路。
- **发送前图片处理**（`/attach <path>` 与剪贴板图片共用）：长边超过 `1568px` 的等比缩小且只缩不放，含透明通道保持 PNG、其余转 JPEG；超过 `32MB` 的直接跳过。两者都**不静默**——会写明「已压缩 3000x2000 → 1568x1045（3.2MB → 420KB，JPEG）」或「已跳过 …：33.0MB 超过 32.0MB 上限（图片不会随消息发送）」。压缩产物按内容哈希命名并落在会话 `images` artifact 目录，同一张图重复粘贴不会重复入列。
  - 语义要点：长边上限是**硬规则**——扁平/合成类大图（UI 截图这类 PNG 压得极小的图）重编码成 JPEG 可能变大，但仍会缩放，因为视觉 token 开销由像素数决定；体积变化会如实写进提示。
  - 环境变量 `AICLI_IMAGE_MAX_DIMENSION` 覆盖长边上限：`0` 关闭压缩，负数表示不缩放（仍做 32MB 体积上限检查）。
- **图片令牌 `[Image #N]`**：附件成功后会插入一个可见令牌（`alt+v` 在光标处插入；键入的 `/attach <path>` 在命令执行后写回下一次输入框草稿），编号即 `/attach` 列表里的序号。
  - **删令牌即弃图**：提交时按草稿里存活的令牌裁剪附件——删掉 `[Image #2]`，第 2 张就不随消息发送。
  - **只约束令牌引入的附件**：来自 ACP/Web/resume 等其它来源的附件不受令牌影响，不会被误删；草稿里一个令牌都没有时也不做裁剪（保守策略，避免静默丢图）。
  - 发送成功后附件与令牌一并清空（与原有"每回合附件"语义一致）。
  - **`ctrl+v` 图片兜底**：`ctrl+v` 读不到剪贴板文本时（典型是剪贴板里只有图片），会按 `alt+v` 的同一套语义读图——落附件 + 在光标处插入令牌；**没有图片时完全静默**（不改行、不刷提示），只有明确按 `alt+v` 才给错误/去重提示。剪贴板里**同时有文本和图片**时 `ctrl+v` 仍旧粘贴文本（保持既有行为），要图片请按 `alt+v`。若你的终端自己截走了 `Ctrl+V`（例如宿主级粘贴），兜底不会触发，此时用 `alt+v`，或把 `app.attach.clipboard_image` 重映射到可用键。
  - **粘贴图片路径也会成为附件**：整段粘贴内容就是一个图片文件路径时（Windows Terminal 把复制的图片**文件**转成路径注入、资源管理器拖拽同样是路径），走与 `/attach` 同一条管线（校验 → 压缩/上限 → 去重）落附件，并把 `[Image #N]` 令牌插到光标处，而不是把路径当文本贴进去。判定刻意保守：必须单行、去引号后是存在的图片文件（未加引号时不允许含空白，避免把句子当路径）；夹在句子里的路径、多行粘贴、多文件粘贴一律原样插入文本，只在"像图片路径但没能加成附件"时给一行状态提示。
  - **Windows Terminal 特别说明**：WT 自己处理 `Ctrl+V` 与右键粘贴（按键不会到达应用），并把剪贴板里的图片**文件**转成路径文本。因此 WT 下 `ctrl+v` 兜底不会触发：**位图请用 `alt+v`**，**文件路径**则由上面的识别接住（或手动 `/attach <path>`）。想让 `Ctrl+V` 交给应用，可在 WT 里把 `paste` 的 `ctrl+v` 绑定改为 `unbound`。
  - **发送时对齐**（`sendMessage` 入口做且只做一次）：按令牌在文本里的**出现顺序**重排附件并把令牌重编号为 `1..k`——文本顺序就是发送顺序，模型看到的编号与图片一一对应；删掉中间令牌后编号会自动重排，不再出现 `[Image #1] [Image #3]` 这种跳号。
  - 没有对应附件的**悬空令牌**会在发送时丢弃（避免模型读到不存在的图片编号并顺手收拢遗留空格）；非令牌来源的附件（ACP/Web/resume）追加在末尾，且**不**替用户补写令牌。
- `@` 路径引用补全：输入 `@` 后按 `Tab` 触发，在工作区内按需扫描（有界）。
  - **唯一命中**：补全为完整相对路径；文件补一个空格，目录补 `/` 以便继续下钻。
  - **多命中**：先补到公共前缀，并在状态行给出「匹配 N 项」与若干示例；继续按 `Tab` 收窄。
  - **无命中**：不改动文本，状态行说明无匹配；按键仍被消费，**不会**误触发 `Tab` 的 plan mode 切换。
  - **语义是路径引用**：提交时原样发送 `@相对路径`，由模型自行 `view`/`grep` 读取；当前**不做内容注入**，也不做 AGENTS.md 片段注入。
  - 扫描根是进程工作目录（chat 的工作区），跳过 `.git`/`node_modules`/`vendor`/`dist`/`build` 等目录；单次最多扫描 6000 项、返回 40 个候选。
  - `@` 前必须是行首、空白或左括号，因此 `a@b` 这类邮箱不会被当作引用。

## 5. 输入所有权与中断

- keymap 只在编辑面生效：弹层、选择器、审批面板持有输入时，动作键不会抢占（例如审批弹层里的 `shift+tab` 不会切权限模式）。
- `esc`：有输入时清空/中断输入；空输入时打开 user-turn 回退选择器（等价 `/backtrack`）。
- `ctrl+c`：中断当前输入；空输入时退出。
- 运行中的回合用 `esc` 或状态栏提示的快捷键中断。

## 6. 排障

| 现象 | 处理 |
|---|---|
| `shift+tab` 没反应 | `/hotkeys` 看"本终端"：传统 conhost 与未识别终端不承诺该键，改用 `alt+m` 或 `/permission-mode` |
| VS Code 里 `shift+tab` 被 IDE 抢占 | 改用 `alt+m`，或在 IDE 里解除该快捷键绑定 |
| 改了 `keybindings.json` 没生效 | `/hotkeys reload`；再看"配置提示"里的告警（未知动作/坏按键/冲突都会提示） |
| 粘贴没有折叠 | 检查 `aicli.chat.collapse_pasted_text` 是否为 `false`；或单次粘贴未超过 1000 字符 |
| 剪贴板里的图片贴不进来 | 确认平台与依赖（`/hotkeys` 的"剪贴板图片"行有原因）；Windows/macOS 直接 `alt+v` 或 `/attach paste`，无依赖的 Linux 用 `/attach <path>` |
| 审批看不懂要执行什么 | 看 `[说明]` 行，或按 `[3]` 展开完整参数 |
