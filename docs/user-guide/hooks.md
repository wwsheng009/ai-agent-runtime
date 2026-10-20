# Hooks 使用指南

Hooks 让你在 Agent 生命周期的固定节点执行外部命令、调用 MCP 工具，并用标准输出影响 Agent 行为（阻断、拒绝、改写参数、注入上下文、终止回合）。

本文以当前代码为准（`backend/internal/hooks`），覆盖配置位置、事件、handler 写法、输出协议、启停与调试。

---

## 1. 配置放在哪里

支持的作用域与文件：

| 作用域 | 位置 | 说明 |
|---|---|---|
| managed | `AICLI_MANAGED_HOOKS_FILES` 指定的文件 | 管理员下发；多个路径用系统路径分隔符连接（Windows 为 `;`） |
| user | `~/.aicli/hooks.json` / `hooks.yaml` / `hooks.yml` | 当前用户全局生效 |
| project | `<workspace>/.aicli/hooks.{json,yaml,yml}` | 项目级；需要该工作区已通过 folder trust |
| project（兼容） | `<workspace>/hooks.{json,yaml,yml}` | 也属于 project 作用域，建议优先用 `.aicli/` |
| plugin | 插件清单 `hooks_file`；未声明时依次找 `hooks.yaml`、`hooks.yml`、`hooks.json`、`hooks/hooks.yaml` | 仅 trusted + enabled 的插件生效 |
| 原生 | `runtime.yaml` 的 `hooks:` 列表 | 旧式/高级格式，支持 HTTP 与 `on_error` |

同名扩展名会全部尝试加载（`hooks.json`、`hooks.yaml`、`hooks.yml` 都可能同时存在）。发现结果中的 `sources` 会列出实际读到的文件。

### 优先级与合并

评估顺序（先到先得，首个 `block`/`stop` 生效，后续 handler 不能把它降级为 `continue`）：

```
runtime.yaml hooks -> managed -> user -> project(.aicli) -> project(根目录)
                   -> plugin(声明式) -> 额外文件 -> plugin(旧式列表)
```

- 后面的配置**不会覆盖**前面的 handler，而是追加执行。
- `AICLI_ALLOW_MANAGED_HOOKS_ONLY=1` 时只保留 managed：`runtime.yaml`、user、project、plugin 全部忽略。
- project 作用域在 folder trust 未通过时整体跳过（服务端不会弹窗询问，未决即视为不信任）。

### 用户目录与工程目录：合并，不是覆盖

`~/.aicli/hooks.*` 与 `<workspace>/.aicli/hooks.*` 会**同时加载**，不存在"工程覆盖用户"或"同名 handler 覆盖"：

| 维度 | 行为 |
|---|---|
| handler 集合 | 两边的 handler 全部保留，按 user → project 顺序依次执行；不去重，也不按 ID/matcher 合并 |
| 阻断 / 终止 | 第一个 `block` / `stop` 立即返回，后面的 handler 不能把它降级成 `continue` |
| `updatedInput` 改写 | 后执行的覆盖先执行的结果（前提是没有更早的 `block`/`stop` 短路） |
| `additional_context` | 后执行的覆盖先执行的同名值，**不拼接** |
| `message` / `systemMessage` | 追加，用换行拼接 |
| 是否启用 | 各自文件 `state.<handler-id>.enabled`；ID 含 scope 与文件绝对路径，通常只影响声明它的文件 |

因此"项目里覆盖用户 hook"要靠 handler 自己判断：payload 带 `cwd`，可在脚本内按工程决定是否生效。

Codex 声明式的 `matcher` 只能匹配 `tool_name` / `source` / `reason` / `agent_type` / `trigger`，**不能匹配工作区路径**。若确实需要按路径过滤，用旧式列表格式的 `match`（与 `matcher` 是**与**关系，全部满足才触发）：

```yaml
hooks:
  - id: project-only-review
    event: pre_tool_use
    match:
      tools: [shell]
      path_glob: ["**/docs/**"]   # 支持 * 与 **，路径统一按 / 比较；不做变量展开
      command_glob: ["*git commit*"]
    exec:
      type: shell
      command: echo reviewed
```

`path_glob` 从 payload 的 `path`/`paths` 或工具参数里的路径字段取值，`command_glob` 从 `command`/`cmd` 取值。glob **不做环境变量或 `${workspace}` 展开**，要写相对/绝对路径的字面量。

另外注意：用户目录固定按 `os.UserHomeDir()` 解析（Windows 为 `%USERPROFILE%`），**不读取 `AICLI_HOME`**。

---

## 2. 支持的事件

Codex 兼容的 12 类事件（名称大小写不敏感，`pre_tool_use` 与 `PreToolUse` 等价）：

| 事件 | 触发时机 | 可阻断 | 可 `continue:false` | 可注入 additionalContext | 备注 |
|---|---|---|---|---|---|
| `PreToolUse` | 工具执行前 | ✅ | ❌ | ✅ | 可改写参数 |
| `PermissionRequest` | 权限裁决时 | ✅ | ❌ | ❌ | allow/deny |
| `PostToolUse` | 工具执行后 | ✅ | ✅ | ✅ | 可 `suppressOutput` |
| `PreCompact` | 上下文压缩前 | ✅ | ❌ | ❌ | block 表示跳过本次压缩 |
| `PostCompact` | 压缩后 | ❌ | ❌ | ❌ | 观测型，可 `suppressOutput` |
| `SessionStart` | 会话首次启动 | ❌ | ✅ | ✅ | 每会话只触发一次；`continue:false` 会终止该会话的首个 turn |
| `SessionEnd` | 回合/会话结束 | ❌ | ❌ | ❌ | 只记录，不解析 stdout |
| `UserPromptSubmit` | 用户提交 prompt | ✅ | ✅ | ✅ | 可在模型看到前拦截 |
| `SubagentStart` | 子代理启动 | ❌ | ❌ | ⚠️ | 宿主异步派发，输出仅观测 |
| `SubagentStop` | 子代理结束 | ⚠️ | ⚠️ | ❌ | 宿主异步派发，输出仅观测 |
| `Stop` | 回合正常结束前 | ✅ | ✅ | ❌ | block 让 Agent 继续干 |
| `Interrupt` | 用户/宿主中断 | ❌ | ❌ | ❌ | 仅观测 |

此外运行时内部还定义了 `post_tool_use_failure`、`stop_failure`、`checkpoint_created`、`rewind_completed`、`backtrack_completed`。它们不是 Codex 事件，`hooks.json` 和 `runtime.yaml` 都能识别，且均为异步观测。

**事件名必须写 snake_case**。别名表只覆盖 12 类 Codex 事件，`PostToolUseFailure` / `StopFailure` 这类 PascalCase 会被当成未知事件并产生 warning。

> 异步 handler（`"async": true`）只产生观测效果，`block` / 改写参数 / `continue:false` 会被丢弃。
>
> `SubagentStart` / `SubagentStop` / `PostCompact` / `Interrupt` / `SessionEnd` 由宿主用异步方式派发，因此即使协议层支持相应字段，返回值也不会改变主流程，只能用于观测与记录（`⚠️`）。

---

## 3. 最小示例

`<workspace>/.aicli/hooks.json`：

```json
{
  "description": "项目 hooks",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "shell|bash",
        "hooks": [
          {
            "type": "command",
            "command": "python .aicli/hooks/check_command.py",
            "timeout": 5,
            "statusMessage": "检查命令..."
          }
        ]
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "python .aicli/hooks/session_context.py"
          }
        ]
      }
    ]
  }
}
```

`check_command.py` 从 stdin 读 JSON，输出阻断决定：

```python
import json, sys

payload = json.load(sys.stdin)
command = (payload.get("tool_input") or {}).get("command", "")
if "rm -rf /" in command:
    json.dump({
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": "禁止执行该命令"
        }
    }, sys.stdout)
```

`session_context.py` 注入上下文：

```python
import json, sys

json.load(sys.stdin)
json.dump({
    "hookSpecificOutput": {
        "hookEventName": "SessionStart",
        "additionalContext": "本仓库使用 Go 1.22，测试必须加 -p 1。"
    }
}, sys.stdout)
```

---

## 4. 配置结构

```json
{
  "description": "可选说明",
  "hooks": {
    "<EventName>": [
      {
        "matcher": "Bash|Write",
        "hooks": [
          {
            "type": "command",
            "command": "echo hi",
            "commandWindows": "powershell -NoProfile -Command ...",
            "timeout": 30,
            "async": false,
            "statusMessage": "运行中",
            "additionalContextLimit": 2000,
            "server": "my-mcp",
            "tool": "check",
            "input": {"mode": "strict"}
          }
        ]
      }
    ]
  },
  "state": {
    "<handler-id>": {"enabled": true, "trusted_hash": "..."}
  }
}
```

### handler 字段

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `type` | string | `command` | `command` 或 `mcp_tool` |
| `command` | string | — | 命令字符串，交给平台 shell 执行（支持管道/重定向） |
| `commandWindows` | string | — | Windows 下覆盖 `command` |
| `timeout` | number | 10（command）/ 5（http） | 超时秒数 |
| `async` | bool | false | 异步执行，丢弃控制效果 |
| `statusMessage` | string | — | 同步执行期间展示给宿主的提示 |
| `additionalContextLimit` | number | 运行时默认 | 超过阈值把 context 落盘；负数表示不落盘 |
| `server` / `tool` / `input` | string/string/object | — | 仅 `mcp_tool` 使用，前两者必填 |

注意：

- Codex 声明式格式（`hooks` 是对象，事件名为 key）的 handler 类型只接受 `command` 和 `mcp_tool`；**HTTP 不在该格式中支持**。
- `on_error`（`fail_open` / `fail_closed`）同样不在 Codex 声明式格式中；该格式的 handler 固定按 fail-open 处理。
- 同一个文件若写成旧式列表格式（`{"hooks": [HookConfig...]}` 或裸数组），则走原生字段：`exec.type` 可以是 `shell`/`http`/`mcp`，并且支持 `on_error`。两种格式按 `hooks` 是对象还是数组自动区分。

### matcher 语义

按事件类型取匹配输入：工具事件取 `tool_name`；`SessionStart` 取 `source`；`SessionEnd` 取 `reason`；`SubagentStart/Stop` 取 `agent_type`；`Pre/PostCompact` 取 `trigger`。

规则：

- 空或 `*`：匹配全部。
- 仅由 `[A-Za-z0-9_|]` 组成：按 `|` 拆成多个精确值（**区分大小写**）。
- 含其他字符：按 Go 正则匹配（未自动加锚点，`Bash` 也会匹配 `BashOutput`，需要时自行写 `^Bash$`）。

示例：`Bash|Write`（精确二选一）、`^mcp__.*`（正则）、`*`（全部）。

---

## 5. 三种 handler 写法

### 5.1 command

- 事件 payload 以 JSON 写入进程 stdin。
- stdout 按第 6 节协议解析；空 stdout 表示不干预。
- 退出码 `2` = 阻断（原因读 stderr）；其他非零 = 执行失败，默认 fail-open。
- 默认超时 3 秒。

常用 payload 字段：

```json
{
  "session_id": "...",
  "turn_id": "...",
  "trace_id": "...",
  "hook_event_name": "PreToolUse",
  "tool_name": "Bash",
  "tool_input": {"command": "ls"},
  "tool_use_id": "...",
  "cwd": "...",
  "model": "...",
  "permission_mode": "..."
}
```

`SessionStart` 额外有 `source`（`startup`/`resume`/`clear`/`compact`/`fork`）与 `resume`；`SessionEnd` 有 `scope`（`turn`/`session`）、`reason`、`success`、`error`；`PostToolUse` 有 `tool_response`；`Stop` 有 `last_assistant_message`。

### 5.2 mcp_tool

```json
{
  "type": "mcp_tool",
  "server": "my-mcp",
  "tool": "review_command",
  "input": {"policy": "strict"}
}
```

调用时 `input` 会与 `hook_input`（完整事件 payload）合并后传给工具，工具返回的 JSON 再按第 6 节协议解析。

`mcp_tool` 不受 `timeout` 字段约束（该字段只作用于 `command` 与 `http`），超时由传入的上下文控制。

### 默认超时

| handler 类型 | 默认超时 | 说明 |
|---|---|---|
| `command` | 10 秒 | 覆盖解释器冷启动 + 实际工作；早期版本是 3 秒，容易在 fail-open 下静默失效 |
| `http` | 5 秒 | 远端应快速响应，慢端点不应拖住回合 |
| `mcp_tool` | 不适用 | 由调用方上下文控制 |

这些默认值在 `aicli hooks list` 与诊断 API 的 `timeout_seconds` 里同样生效（HTTP handler 会被强制施加 deadline，即使宿主注入了自定义 `http.Client`）。只有在配置里显式写了 `timeout` 才会覆盖默认值。

### 5.3 http（旧式列表格式）

可以写在 `runtime.yaml`，也可以写在 `.aicli/hooks.yaml`（旧式格式）：

```yaml
hooks:
  - id: review
    event: pre_tool_use
    exec:
      type: http
      url: http://127.0.0.1:8080/hook
      method: POST
      headers:
        Authorization: Bearer token
    timeout: 5s
    on_error: fail_closed
```

请求体为事件 payload JSON，响应体按同一输出协议解析。

---

## 6. 输出协议

stdout 有三种形态：

1. **空**：不干预。
2. **纯文本**：仅支持注入上下文的事件会把它作为 `additionalContext`；其他事件忽略。
3. **JSON 对象**：执行控制决策。

### 6.1 通用字段

```json
{
  "continue": true,
  "stopReason": "…",
  "suppressOutput": false,
  "systemMessage": "…"
}
```

### 6.2 事件专用字段

```json
{
  "decision": "block",
  "reason": "必须写明原因",
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "additionalContext": "…",
    "permissionDecision": "allow",
    "permissionDecisionReason": "…",
    "updatedInput": {"command": "ls -la"}
  }
}
```

实际求值顺序：`continue:false` 最先判定；随后 `hookSpecificOutput.decision.behavior`（仅 `PermissionRequest`）与 `decision:block` 都映射为阻断，前者优先提供原因。

### 6.3 各事件差异

- **PreToolUse**：用 `permissionDecision` 表达 `allow` / `deny`。`allow` 必须同时给 `updatedInput`，否则整条输出判为 invalid；`deny` 会阻断。
- **PermissionRequest**：用嵌套的 `hookSpecificOutput.decision.behavior`（`allow`/`deny`）。`allow` 不等于绕过——静态策略与 hard deny 仍会执行，只是 hook 不否决。
- **PostToolUse**：`decision:block` 让 Agent 基于反馈继续；`continue:false` 终止。
- **SessionStart**：只能 `continue:false` 或注入 context，不能 block。
- **SessionEnd / PostCompact / Interrupt**：观测型；返回控制字段会被判 invalid 并忽略（`suppressOutput` 对 `PostCompact`、`SessionEnd` 合法）。
- **PreCompact**：`block` = 跳过这次压缩。

### 6.4 合法性约束

- `hookSpecificOutput.hookEventName` 必须与所配事件一致，否则 invalid。
- `decision:block` 必须带非空 `reason`。
- `updatedInput` 只能用于 `PreToolUse` + `permissionDecision:allow`。
- `suppressOutput` 只允许 `PostToolUse` / `PostCompact` / `SessionEnd`。
- invalid 输出按 `continue` 处理并记录 warning（不阻断）；`fail_closed` 只对**执行失败**（非零退出、HTTP 非 2xx、进程启动失败等）生效，不会把 invalid 输出升级为阻断。

---

## 7. 启用与禁用

### Web 控制台

Settings → Harness → Hooks：

- 按事件、来源、同步/异步、matcher、命令/URL、超时查看所有生效 handler。
- 显示 `sources`、`warnings` 和最近运行记录。
- 可开关 handler，状态写回源文件的 `state.<id>.enabled`。

写回限制：

- 只允许 **user / project** 作用域的 handler；plugin 与 managed 由各自信任存储治理。
- 只能写 `sources` 中已发现的文件，其他路径一律 403。

### 直接改文件

```json
{
  "state": {
    "project:C:/repo/.aicli/hooks.json:pre_tool_use:0:0": {
      "enabled": false
    }
  }
}
```

声明式 handler 的 ID 格式为 `<scope>:<source_path>:<event>:<group_index>:<handler_index>`，其中 `source_path` 是绝对路径（Windows 下为反斜杠形式）。**不要手写拼接**，直接从 `GET /api/runtime/harness/hooks` 返回的 `id` 复制，否则容易因路径分隔符或事件名拼写对不上而失效。

旧式 `runtime.yaml` 列表格式的 ID 为 `<scope>:<abs_path>:<event>:<index>`（4 段）。

JSON 写回会重排整个文件（缩进统一为 2 空格）；YAML 写回通过 `yaml.Node` 保留注释与键序。

### trusted_hash

`state.<id>.trusted_hash` 非空时会校验 handler 的可执行声明哈希（`event` + `matcher` + `exec` + `async` + `on_error`，不含 `statusMessage` 与路径）。不匹配则禁用并给出 warning。适合在允许项目级 hook 前锁定内容。

---

## 8. 调试

### 本地诊断（CLI）

不启动 Agent 回合也能确认"文件有没有被读到、handler 有没有被解析、事件到底会不会命中"：

| 命令 | 作用 | 退出码 |
|---|---|---|
| `aicli hooks list` | 列出当前生效 handler（event / id / source / type / matcher）与实际读到的来源文件 | 0 |
| `aicli hooks check` | 只做加载检查，不执行 handler；有告警时失败 | 有告警为 1 |
| `aicli hooks test --event <Event>` | 用给定 payload **真实执行**匹配的 handler，打印合并后的决策 | 有 handler 执行失败时为 1 |
| `aicli hooks test --event <Event> --dry-run` | 只做匹配与 payload 归一化，**不执行**任何 handler | 0 |
| `aicli doctor hooks` | 体检视图：来源、folder trust、类型/事件分布与可操作建议 | 0（结论见 `health`） |

```bash
aicli hooks list
aicli hooks check --output json
aicli hooks test --event PreToolUse --payload '{"tool_name":"shell"}'
aicli hooks test --event PreToolUse --payload '{"tool_name":"shell"}' --dry-run
aicli hooks test --event SessionStart --payload-file ./payload.json
aicli doctor hooks --workspace ./my-repo
```

通用参数：

| 参数 | 说明 |
|---|---|
| `--workspace <dir>` | 工作区根目录，默认当前目录；决定项目级 hook 与 folder trust 的判定对象 |
| `--config-file <path>` | 指定 `runtime.yaml`；默认按 `.aicli` 分层解析 |
| `--output json` / `--json` | 结构化输出，便于 CI 与脚本消费 |

`hooks test` 的 payload 只需写业务字段，`hook_event_name` 会按 `--event` 自动补齐；输出回显 `matched hooks`（**经过 event + enabled + matcher 过滤后真正会跑的 handler**，不是按事件名粗算）与 `PrepareEventPayload` 处理后的最终 payload，方便确认 matcher 依据。

handler 执行失败（非零退出、超时、URL 不可达、MCP 调用错误）不会中断其余 handler，但会被单独列出并让命令以退出码 1 结束：

```
event=pre_tool_use matched=2 action=enrich
matched hooks: matching-shell, broken-shell
dispatch error: 1 个 handler 执行失败: broken-shell
```

这点很重要：fail-open 的 handler 崩溃后合并决策仍是 `continue`/`enrich`，若只看 `action` 会误以为一切正常。

`aicli hooks list/check/test` 与宿主使用同一套 `ResolveForHostFromEnv` 解析逻辑，作用域顺序固定为：

```
runtime.yaml -> managed -> user -> project(.aicli) -> project(根目录) -> plugin
```

项目作用域仍受 folder trust 约束：未通过时 `--workspace` 下的 `.aicli/hooks.*` 与根目录 `hooks.*` 会被整体跳过，`list` 的来源文件里也不会出现它们。

trust 判定针对 **`--workspace` 指向的目录**（不是进程 cwd），因此从任意目录检查另一个 checkout 都会得到与宿主一致的结论。诊断不会交互式弹窗：未决即按未信任处理，输出里会明确标注：

```
project scope: SKIPPED（未信任该工作区，source=headless_deny）
```

只有在工作区确实存在信任敏感配置（hooks / plugins / MCP / agents / profiles）时才会要求 trust；空目录不会因为没有 trust 记录而被阻挡。

`--dry-run` 适合先确认哪些 handler 会命中：它回显 `matched hooks` 与归一化后的 payload，`action` 是占位值 `continue`（没有执行就没有真实决策）。

### 健康体检

`aicli hooks check` 面向 CI（有告警即失败），`aicli doctor hooks` 面向排障：

```
workspace: E:/repo
health: ok
project trust: trusted (store)
sources: 1, handlers: 2, warnings: 0
types: http=1 shell=1
events: post_tool_use=1 pre_tool_use=1
```

`health` 取值：`ok` / `warning`（如项目作用域被跳过）/ `error`（存在加载告警）。`--json` 额外给出 `findings` 数组与 `sample_handlers`，便于脚本聚合。

宿主启动时（`aicli exec` / `aicli chat` 等）会把同一批 discovered warnings 打到 stderr：

```
Warning: hooks: <warning 内容>
```

告警按内容去重，同一进程内不会重复刷屏。CI 里想把它当成硬失败，用 `aicli hooks check` 而不是匹配 stderr 文本。

### 诊断 API

```http
GET /api/runtime/harness/hooks?workspace_path=E:/repo
```

返回：

```json
{
  "hooks": [{
    "id": "...",
    "event": "pre_tool_use",
    "handler_type": "shell",
    "execution_mode": "sync",
    "matcher": "Bash",
    "command": "...",
    "timeout_seconds": 5,
    "source": "project",
    "source_path": "E:/repo/.aicli/hooks.json",
    "enabled": true
  }],
  "sources": ["E:/repo/.aicli/hooks.json"],
  "warnings": [],
  "recent_runs": [{"hook_id": "...", "event": "pre_tool_use", "phase": "completed"}],
  "count": 1
}
```

该接口只读，不执行 handler。

开关：

```http
POST /api/runtime/harness/hooks?workspace_path=E:/repo
{"id": "...", "source_path": "E:/repo/.aicli/hooks.json", "enabled": false}
```

### 运行事件

`hooks.status`（live-only）在同步 handler 开始/结束时推送，payload 含 `hook_id`、`event`、`phase`、`source`、`source_path`、`status_message`。

### 排查顺序

1. `sources` 里没有你的文件 → 路径或 folder trust 不对。
2. 有文件但 handler 数量少 → 看 `warnings`（未知事件、空命令、`server`/`tool` 缺失、`trusted_hash` 不匹配）。
3. handler 在但未触发 → 检查 matcher（大小写、正则锚点）与事件是否在该宿主接线。
4. 触发了但没效果 → 看 `recent_runs`、`hooks.status` 与 `on_error`；invalid 输出不阻断。
5. Windows 下命令行为异常 → 用 `commandWindows` 单独指定。

---

## 9. 已知边界

| 项 | 说明 |
|---|---|
| `SessionStart(compact)` | 压缩后的下一 turn 触发；Codex 是压缩后立即触发 |
| `SessionEnd` 输出 | 不解析 stdout，只记录 exit/error |
| `updatedMCPToolOutput` | 未实现（Codex 亦不支持） |
| HTTP handler | 仅旧式列表格式（`runtime.yaml` 或 `{"hooks":[...]}`）支持；Codex 声明式对象格式不接受 |
| `on_error` | 仅旧式列表格式支持；Codex 声明式格式固定 fail-open |
| 异步 handler | 控制效果被丢弃，只做观测 |
| 项目作用域 | 依赖 folder trust；服务端不会为 hooks 单独弹窗 |
| `Stop` 继续上限 | 同一回合内 `block` 最多让 Agent 继续 5 次，达到上限后按 `continue` 结束并在消息中标注 `(stop hook continuation limit reached)`；payload 的 `stop_hook_continues` 可用于 hook 自行判断 |
| `SubagentStop` 触发条件 | 只在子代理真正跑过 turn 并结束时触发；创建后未运行即停止不会触发 |
| `Subagent` 生命周期 | 通过 `POST /sessions/{id}/agents` 创建的子代理**不绑定 HTTP 请求**，请求返回后继续运行，直到自身完成或收到 stop |
| `Interrupt` 典型结果 | 会话空闲时发 interrupt 仍会派发 hook，但响应为 `cancelled:false, reason:no_active_turn` |
| `Interrupt` 适用范围 | 流式与非流式 `/api/agent/chat` 回合都会登记在途回合，运行中可用 `POST /sessions/{id}/runtime/commands` 的 `interrupt` 取消 |
| `Interrupt` 的响应 | 被取消的回合返回 **HTTP 499**，响应体带 `cancel_source: "user_interrupt"`；不是 500，客户端不应把它当服务故障重试 |
| 内部事件 | `checkpoint_created` / `rewind_completed` / `backtrack_completed` 仅观测；checkpoint 需 `checkpoint.enabled: true` |
| `post_tool_use_failure` 触发条件 | 仅**执行层失败**（工具不存在、权限拒绝、超时）触发；命令非零退出码属于正常返回，不会触发 |
| `stop_failure` 触发条件 | 运行以失败结束（步数/工具调用上限、空回复、未满足完成要求）时触发 |
| 未知事件名 | `PostToolUseFailure` 这类 PascalCase 不在别名表内，必须写 `post_tool_use_failure` |

## 10. 生效时机

- Agent 的 hook manager 在会话/Agent 构建时解析并合并；修改配置文件后，**新建会话**按新内容执行，运行中的会话保持已有快照。
- 唯一例外是 `SessionEnd(scope=session)`：它在 `SessionManager.Close` 时重新发现一次 hooks，因此能读到关闭前的最新文件内容。
- `state.enabled` 的改动通过 Web 面板写入后，同样在下次构建时生效。
- `SessionStart` 每会话只触发一次；`clear` / `compact` / `fork` 会重置该标记，使下一 turn 重新触发并带上对应 `source`。
