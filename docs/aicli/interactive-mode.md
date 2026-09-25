# aicli 交互模式：按键、终端能力与审批

本文是 `aicli` / `aicli chat` 交互模式的权威说明，覆盖按键分层、终端能力矩阵、审批提示结构与粘贴/附件语义。Slash 命令总表见 [install.md](./install.md#chat-内置斜杠命令补充)。

## 1. 按键分层

按键分三层，**只有第二层可被用户重映射**：

| 层 | 内容 | 是否可重映射 |
|---|---|---|
| 固定键 | `enter` 提交、`esc` 中断/回退、`ctrl+c` 中断、`ctrl+d` 退出、`ctrl+j`/`ctrl+o` 换行、`tab`（slash 补全或 plan 模式）、方向键导航 | 否 |
| 动作键 | `app.permission.cycle`（默认 `shift+tab`、`alt+m`）、`app.transcript.pager`（默认 `ctrl+t`） | 是 |
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
| Windows Terminal / WSL | 可用 | 可用 | 可用 | Windows 可用 / WSL 不可用 | 未实现 |
| VS Code 集成终端 | 可用（IDE 可能抢占） | 可用 | 可用 | 同上 | 未实现 |
| WezTerm / ConEmu / iTerm2 / Apple Terminal / xterm | 可用 | 可用 | 可用 | Windows 可用 / 其它不可用 | 未实现 |
| mintty / Git Bash | 可用 | 可用 | 可用 | 可用 | 未实现 |
| Windows 传统控制台 (conhost) | **不承诺**（用 `alt+m`） | **不可用**（用 `ctrl+j`/`ctrl+o`） | **不识别 bracketed paste** | 可用 | 未实现 |
| 未识别终端 | 不承诺（保守） | 按可用处理 | 按可用处理 | 按平台 | 未实现 |

说明：

- "剪贴板文本不可用"的终端请使用终端自身的粘贴（`Ctrl+Shift+V` / `Cmd+V`），它走 bracketed paste，粘贴折叠与提交语义一致。
- 非 TTY（headless / `--output json`）下按键矩阵不适用，`/hotkeys` 会带一行说明。
- 探测只读环境变量，不做任何副作用调用；未知一律按保守值处理。

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
- 剪贴板**图片**粘贴尚未实现：`/hotkeys` 会如实显示不可用，并提示改用 `/attach <path>`。
- `@path` 目前只是普通文本，**没有**mention 补全与内容注入；要引用文件请写完整路径或让模型自行 `view`。

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
| 剪贴板里的图片贴不进来 | 当前不支持；用 `/attach <path>` 添加图片附件 |
| 审批看不懂要执行什么 | 看 `[说明]` 行，或按 `[3]` 展开完整参数 |
