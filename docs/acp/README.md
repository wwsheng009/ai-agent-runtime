# ACP 使用手册（aicli agent stdio / aicli acp）

本目录是 aicli 的 Agent Client Protocol（ACP）宿主使用文档。aicli 以
`aicli agent stdio` 或 `aicli acp` 子命令在 stdin/stdout 上提供 ACP 子集，可被任意实现了
ACP 客户端侧的编辑器 / IDE（如 Zed 类 host）作为外部 Agent 驱动。

- 代码位置：`backend/internal/acp`（协议层）、`backend/cmd/aicli/commands/agent_stdio.go`（宿主适配）
- E2E 验证脚本：`backend/scripts/acp_e2e_cancel.go`

## 目录

1. [快速开始](#快速开始)
2. [传输与帧格式](#传输与帧格式)
3. [命令行参数](#命令行参数)
4. [协议方法](#协议方法)
5. [session/update 事件](#sessionupdate-事件)
6. [权限请求流程](#权限请求流程)
7. [取消语义（$/cancel_request 与 session/cancel）](#取消语义)
8. [session 持久化与恢复](#session-持久化与恢复)
9. [端到端自测](#端到端自测)
10. [MCP 集成](#mcp-集成)
11. [常见问题](#常见问题)

## 快速开始

```powershell
# 1. 启动 ACP 宿主（stdout 只输出 NDJSON 协议消息，日志走 stderr）
aicli agent stdio --provider openai --model gpt-4o
# 等价快捷：
aicli acp --provider openai --model gpt-4o
# 通用协议 flag 形式（被其他宿主工具广泛支持）：
aicli --acp --provider openai --model gpt-4o

# 无审批阻塞的自动化场景
aicli agent stdio --yolo --enable-tools

# 会话持久化 + 恢复（支持 session/load）
aicli agent stdio --session-dir %USERPROFILE%\.aicli\sessions
```

最小握手示例（客户端 → agent，每行一个 JSON-RPC 消息）：

```jsonc
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}}}
{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"E:\\demo","mcpServers":[]}}
{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"<上一步返回的 sessionId>","prompt":[{"type":"text","text":"hello"}]}}
```

agent 会先通过 `session/update` 通知流式返回消息/工具事件，最后对 id=3 返回
`{"stopReason":"end_turn"}`（或 `cancelled` / `refusal` 等其它停止原因）。

## 传输与帧格式

- **JSON-RPC 2.0 over NDJSON**：每行一条消息，`\n` 分隔。
- **stdout 仅承载协议消息**；日志与诊断一律写入 stderr 或 `--log-dir`。
- **stdin 是协议流，不是 prompt 文本**——不要向 stdin 粘贴用户输入。
- 协议主版本：`1`（`initialize` 协商返回）。
- JSON-RPC id 同时接受数字与字符串形态；`$/cancel_request` 的 `requestId`
  会做归一化匹配（`"12"` 与 `12` 指向同一在途请求，见[取消语义](#取消语义)）。

## 命令行参数

`aicli agent stdio` 复用 `aicli exec` 的共享 flags（除 `--prompt` /
`--image` / `--output-schema` 外），常用项：

| 参数 | 默认 | 说明 |
|---|---|---|
| `--provider` / `-P` | 配置解析链 | 指定 provider 名称 |
| `--model` / `-m` | 配置解析链 | 指定模型名称 |
| `--profile` | — | profile 名称或目录路径 |
| `--agent` | — | profile 内 agent 标识 |
| `--permission-mode` | `default` | `default\|accept_edits\|plan\|bypass_permissions` |
| `--yolo` | false | 等价 `--permission-mode bypass_permissions`（自动启用 tools） |
| `--enable-tools` / `--disable-tools` | **默认启用 tools** | 工具审批经 `session/request_permission` RPC 走 ACP 客户端 |
| `--ephemeral` | **true**（agent stdio 专属默认） | 不持久化会话文件；需 session/load 恢复时改用 `--session-dir` |
| `--session-dir` | — | 会话持久化目录（启用后支持跨进程 session/load） |
| `--log-dir` | 默认日志目录 | 会话日志目录 |
| `--request-timeout` | — | 单次 LLM 请求超时（如 `60s`、`2m`） |
| `--allow-tool` / `--deny-tool` | — | 工具 allow/deny 规则（`--deny-tool` 硬拒绝优先） |
| `--skills-dir` 等 | — | skills 暴露控制，同 exec |

provider/model 的完整解析链（flag → runtime session → workspace 偏好 →
`aicli.chat` 配置 → 交互选择器 → `providers.default_provider`）见
`docs/aicli/agents.md` 与 `docs/aicli/exec.md`。

## 协议方法

### 客户端 → Agent

| 方法 | 说明 |
|---|---|
| `initialize` | 握手。返回 `protocolVersion`、`agentCapabilities`、`agentInfo`（name=`aicli`）、`authMethods`。`authMethods` 恒为**非 nil 空数组** `[]`：本 agent 凭据由本地 `aicli login` 管理，不实现 `authenticate` / `logout`（裁定见实施计划 §11.4-D1） |
| `session/new` | 创建会话。参数 `{cwd, mcpServers}`（`mcpServers` 逐条容错解析并装配为会话级 MCP，见 [MCP 集成](#mcp-集成)）；返回 `{sessionId, configOptions?}`（`configOptions` 携带模型 / provider 选择器） |
| `session/prompt` | 运行一轮对话。参数 `{sessionId, prompt: ContentBlock[]}`；阻塞至本轮结束，返回 `{stopReason}`。当前仅 `type=text` 内容块 |
| `session/cancel` | 取消某 session 的在途 prompt。参数 `{sessionId}`（通知，无响应） |
| `session/set_config_option` | 修改会话选项（模型 / provider 选择）。参数 `{sessionId, configId, value}`，`value` 为 `value_id` 字符串或布尔值；返回**完整的** `{configOptions}` 供客户端刷新选择器。Zed 的模型菜单即通过该方法切换模型，provider 选择器走同一方法（`configId: "provider"`） |
| `session/load` | 恢复会话（仅 `loadSession=true` 能力时可用）。agent 通过 `session/update` 回放历史后返回空对象 `{}`（`LoadSessionResponse`，字段均可选）。返回 `null` 违反 ACP v1 schema，会导致 Zed 等严格客户端反序列化失败 |
| `session/resume` | 重连到持久化会话（需声明 `sessionCapabilities.resume`）。参数 `{sessionId, cwd?, mcpServers?}`；与 `session/load` 的区别是**不重放历史**——客户端已持有 transcript 时用它重连，返回 `{}`。provider 恢复失败不阻塞重连（会话仍挂载） |
| `session/list` | 枚举持久化会话（需声明 `sessionCapabilities.list`）。参数 `{cursor?, cwd?}`；返回 `{sessions: [{sessionId, cwd?, title?, updatedAt?}], nextCursor?}`，`sessions` 恒为数组（空时为 `[]`，绝不返回 `null`）。`cursor` 为上一页返回的 `nextCursor`（数字偏移） |
| `session/delete` | 删除持久化会话（需声明 `sessionCapabilities.delete`）。参数 `{sessionId}`；返回 `{}`。会话仍在内存中时先 detach 再删除存储记录；sessionId 已不存在（内存与存储都没有）时**幂等成功**——客户端重连后会重试删除，不该看到假失败。删除后 `session/load` 该 id 返回 `-32002`（资源不存在） |
| `session/close` | 释放内存中的会话（需声明 `sessionCapabilities.close`）。参数 `{sessionId}`；返回 `{}`。未挂载的会话按幂等处理（不报错），供客户端退出时清理 |
| `session/set_mode` | legacy 权限模式切换（`{sessionId, modeId}`）。与 `configId: "mode"` 配置项双通道并存，见下节 |
| `$/cancel_request` | JSON-RPC 标准取消通知。参数 `{requestId}`，按请求 id 取消在途调用（双向） |

### Agent → 客户端

| 方法 | 说明 |
|---|---|
| `session/update` | 会话事件流（消息块 / 工具调用 / plan / 用量），见下节 |
| `session/request_permission` | 工具审批请求，客户端须返回 `{outcome: {outcome: "selected"\|"cancelled", optionId}}` |

### 能力协商（默认值）

```jsonc
{
  "loadSession": true,
  "sessionCapabilities": {"list": {}, "delete": {}, "resume": {}, "close": {}},
  "promptCapabilities": {"image": false, "audio": false, "embeddedContext": false},
  "mcpCapabilities": {"http": true, "sse": true}
}
```

当前 MVP 仅支持文本 prompt；图片 / 音频 / embeddedContext 暂未开放。

`mcpCapabilities.http/sse = true` 是**置位即承诺**：置位的传输必须真的能连（见
[MCP 集成](#mcp-集成)），未置位的传输会被「跳过 + 可见诊断」。stdio 是 ACP v1
要求所有 agent 必须支持的传输，规范里没有对应开关，因此**不由能力位表达**。

ACP v1 把 `sessionCapabilities` 的每一项定型为**能力对象**（不是布尔值）：键存在
（序列化为 `{}`）才代表方法可用，布尔 `true` 会被严格客户端判为 schema 违规。

`initialize` 返回前会按后端**实际实现**裁剪这些键：声明的方法一定可达，未实现的方法
既不会被声明、调用时也返回 `-32601`（见 `internal/acp/server.go` 的
`effectiveAgentCapabilities`）。aicli 宿主实现了 `list` / `delete` / `resume` / `close` 四者。

### stopReason 取值

| 值 | 含义 |
|---|---|
| `end_turn` | 正常结束 |
| `cancelled` | 被 `session/cancel` 或 `$/cancel_request` 中断 |
| `max_tokens` | 达到 token 上限 |
| `max_turn_requests` | 达到回合内请求次数上限 |
| `refusal` | 模型拒绝回答 |

## session/update 事件

`session/update` 通知的 `update.sessionUpdate` 字段区分事件类型：

| sessionUpdate | 载荷 | 说明 |
|---|---|---|
| `agent_message_chunk` | `{messageId, content: {type:"text", text}}` | 助手回复流式块 |
| `user_message_chunk` | 同上 | 用户消息回显（session/load 回放时出现） |
| `agent_thought_chunk` | 同上 | 思考流式块 |
| `tool_call` | `{toolCallId, title, kind, status, rawInput, content?, locations?}` | 工具调用开始 |
| `tool_call_update` | 同上 | 工具调用进度 / 终态 |
| `plan` | `{entries: [{content, priority, status}]}` | 计划面板更新 |
| `usage_update` | `{used, size, cost?}` | token 用量 |
| `session_info_update` | `{title, updatedAt}` | 会话标题 / 更新时间变化（自动生成标题后的回合结束、session/load 回放完成后发送） |
| `available_commands_update` | `{availableCommands: [{name, description?, input?}]}` | 斜杠命令目录（session/new、session/load 后发送；空目录序列化为 `[]`） |
| `config_option_update` | `{configOptions}` | 会话选项在带外变化（如模型被切换）时的刷新通知 |

工具调用 `status`：`pending` → `in_progress` → `completed` / `failed`。

工具 `kind` 映射 ACP 分类：`read` / `edit` / `delete` / `move` / `search` /
`execute` / `think` / `fetch` / `other`。

`plan` 由 `todos` 工具的终态快照驱动（`payload.protocol_result.metadata.todo_snapshot`，
与 Web 任务面板同源）：每次发送全量列表，客户端整体替换；条目 `priority` 恒为 `medium`
（todos 无优先级字段），`status` 与工具状态一致；`session/load` 回放时用历史中最新的
todos 快照重建任务列表，重复快照不再重发。

## 权限请求流程

启用 tools 后（agent stdio 默认启用），工具执行前会向客户端发起
`session/request_permission` 请求：

```jsonc
// agent → client
{"jsonrpc":"2.0","id":10,"method":"session/request_permission",
 "params":{"sessionId":"...","toolCallId":"...","options":[
   {"optionId":"allow_once","name":"Allow once","kind":"allow_once"},
   {"optionId":"reject_once","name":"Reject once","kind":"reject_once"}]}}

// client → agent（响应）
{"jsonrpc":"2.0","id":10,"result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}
```

- option `kind`：`allow_once` / `allow_always` / `reject_once` / `reject_always`
- 客户端关闭选择器时应返回 `{"outcome":{"outcome":"cancelled"}}`
- prompt 被取消时，挂起的权限请求会随 promptCtx 一起中止
- 想完全不弹审批：启动时用 `--yolo`（bypass_permissions）或 `--disable-tools`

## 取消语义

两条取消路径，行为一致（最终 prompt 返回 `{stopReason:"cancelled"}`）：

1. **`session/cancel` 通知**（ACP 规范）：按 sessionId 取消该会话在途
   prompt，触发 chat 会话的 interrupt 机制（中止流式调用、清理工具终态）。
2. **`$/cancel_request` 通知**（JSON-RPC 标准）：按请求 id 取消在途请求。
   conn 层为每个入站请求派生独立 context，收到 cancel 后中止 handler；
   宿主侧通过 `context.AfterFunc` 把该取消传导给 chat 会话 interrupt。

实现要点：

- `$/cancel_request` 在读取循环内联处理，不占用 handler goroutine，
  不会阻塞并发的响应分发（对齐 LSP 传输行为）。
- 客户端主动断开 ctx 时，`Conn.Call` 也会向对端发送 `$/cancel_request`。
- **requestId 形态归一化**：请求以数字 id 发出、取消以字符串 `"12"` 到达
  （或反之）均能正确匹配。id key 对 JSON 字符串形态做解引号处理
  （`internal/acp/conn.go` 的 `idKey`），这是实测发现的互操作坑，
  部分客户端会把数字 id 字符串化。
- 取消后 agent 会补发 `tool_call_update`（failed/cancelled 终态），
  保证客户端不会看到悬挂的 `in_progress` 工具调用。
- 取消以 `stopReason: "cancelled"` 正常响应返回，**不是** JSON-RPC error。

## session 持久化与恢复

- 默认 `--ephemeral=true`：会话仅存在于进程内存，退出即失。
- `--session-dir <dir>`：会话写入持久化 store；跨进程仍可
  `session/load`（capabilities 中 `loadSession=true`）。
- `session/load` 语义：agent 先通过 `session/update` 回放完整对话历史
  （user/assistant 消息、工具调用），然后返回空对象 `{}`（`LoadSessionResponse`）；客户端此后可
  直接 `session/prompt` 继续对话。
- 解析顺序：进程内已附着 session → 持久化 store（非 ephemeral 时）。

## 模型、thinking effort 与 provider 切换（session config options）

`session/new` 与 `session/load` 的响应会带 `configOptions`，最多三个 select
选项：

- `id: "model"`（`category: "model"`）：模型选择器。客户端（Zed 等）据此
  渲染模型菜单与 "Change Model" 快捷键。
- `id: "thought_level"`（`category: "thought_level"`）：reasoning effort
  选择器。仅当当前模型声明了 reasoning effort 目录
  （provider 配置的 `model_capabilities`，或协议默认能力）时下发；分类名与
  ACP 规范一致，Zed 会渲染为 "Change Thinking Effort"。
- `id: "provider"`（`category: "_provider"`）：provider 选择器。仅当配置中
  存在 ≥2 个 enabled provider 时下发；分类以 `_` 开头是 ACP 的扩展分类
  约定，客户端不识别该分类时按普通 select 渲染。

用户切换时发送 `session/set_config_option`，agent 分别复用交互式 `/model`、
`/reasoning_effort`、`/provider` 的同一套运行时切换逻辑，并返回更新后的完整
`configOptions`。

```jsonc
// session/new 响应（片段）
{"sessionId":"...","configOptions":[
  {"id":"model","name":"Model","category":"model","type":"select",
   "currentValue":"gpt-4o",
   "options":[{"value":"gpt-4o","name":"gpt-4o"},{"value":"gpt-4o-mini","name":"gpt-4o-mini"}]},
  {"id":"thought_level","name":"Thinking Effort","category":"thought_level","type":"select",
   "currentValue":"default",
   "options":[{"value":"default","name":"default",
               "description":"Use the provider default reasoning effort."},
              {"value":"low","name":"low"},{"value":"medium","name":"medium"}]},
  {"id":"provider","name":"Provider","category":"_provider","type":"select",
   "currentValue":"openai",
   "options":[{"value":"openai","name":"openai"},{"value":"anthropic","name":"anthropic"}]}]}

// 客户端切换模型
{"jsonrpc":"2.0","id":7,"method":"session/set_config_option",
 "params":{"sessionId":"...","configId":"model","value":"gpt-4o-mini"}}

// 客户端切换 provider（自动选中该 provider 的默认模型）
{"jsonrpc":"2.0","id":8,"method":"session/set_config_option",
 "params":{"sessionId":"...","configId":"provider","value":"anthropic"}}

// 客户端切换 reasoning effort；"default" 清除会话覆盖、回到 provider 默认值
{"jsonrpc":"2.0","id":9,"method":"session/set_config_option",
 "params":{"sessionId":"...","configId":"thought_level","value":"high"}}
```

实现要点：

- 选项按客户端能力门控：select 是 ACP v1 基线能力，始终下发；boolean 选项
  只有在客户端 `initialize` 声明了 `clientCapabilities.session.configOptions.boolean`
  时才下发（`internal/acp/server.go` 的 `allowsBooleanConfigOptions` /
  `filterClientConfigOptions`）。门控同时作用于 `session/new` / `load` /
  `resume` / `set_config_option` 的响应与 `config_option_update` 通知；
  若某次通知的选项**全部**是 boolean 且客户端不支持，则整条通知被丢弃
  （发空数组会把客户端的选项目录清空）。
- 模型列表来自 provider 的 `supported_models`（含当前模型与默认模型，
  去重后按名称排序），当前模型排在最前，便于客户端预选。
- provider 列表只含 enabled provider，当前 provider 排在最前并始终可选；
  切换到当前 provider 是 no-op。切换 provider 会连带选中其
  `default_model`（缺失默认模型时沿用当前模型名），协议、适配器、HTTP
  client 与工具表面同步刷新；切换后响应中的 `model` 选项同步更新为新
  provider 的模型列表。
- reasoning effort 选项的取值来自当前模型的 effort 目录（去重、按
  minimal/low/medium/high/max 等既有排序规则排列），当前值排在最前便于
  客户端预选；目录为空时不下发该选项，避免客户端选择运行时无法识别的值。
  额外提供合成值 `"default"`：ACP 要求 `currentValue` 必须属于已下发取值，
  而"未覆盖、用 provider 默认"本身也是一个合法状态，因此需要一个真实取值
  承载它；选中后清除会话级覆盖。会话中残留的旧值（切换模型/provider 后新
  目录不再包含它）仍会被列出并可再次选中，避免切换后状态无法表达。
  不可识别的取值（既不在目录中、也不是当前值）返回 `invalid params`。
- model / thought_level / provider 三类切换均在**下一轮**生效；带内 prompt
  进行中会返回错误请客户端稍后重试，避免与运行中的 turn 竞争会话状态。
- `mode`（权限模式）是例外：权限判定在每次工具调用前重新求值，因此带内
  prompt 进行中允许切换，从下一次工具调用起对运行中的 turn 立即生效；响应
  与 `config_option_update` / `current_mode_update` 通知都会带上新值。`plan`
  是持久生命周期（进入/退出要走 plan 工件流程），prompt 进行中仍返回错误
  请客户端稍后重试。
- 切换后 agent 通过 `config_option_update` 之外的响应回传完整选项集，
  客户端无需再发 `session/load` 刷新。

## 端到端自测

`backend/scripts/acp_e2e_cancel.go` 对真实编译产物跑全链路验证
（真实 stdio 子进程 + 隔离 USERPROFILE + 本地 mock 流式 provider）：

```powershell
cd backend
go build -o $env:TEMP\aicli-e2e.exe ./cmd/aicli/
go run ./scripts/acp_e2e_cancel.go $env:TEMP\aicli-e2e.exe
# 期望输出：
# OK initialize
# OK session/new sessionId=session_...
# OK sent $/cancel_request
# RESULT prompt stopReason="cancelled"
# E2E PASS
```

脚本流程：initialize → session/new → session/prompt（mock provider 首块
延迟挂起）→ 500ms 后发 `$/cancel_request` → 断言最终响应
`stopReason == "cancelled"`。

协议层单测：`go test ./internal/acp/ -count=1`，覆盖
`$/cancel_request` 出站/入站、requestId 归一化、prompt 中止等。

配置项切换（model / thought_level / provider）另有一条全链路脚本，
断言切换值真的落到上游请求体上：

```powershell
cd backend
go build -o $env:TEMP\aicli-e2e.exe ./cmd/aicli/
go run ./scripts/acp_e2e_thought_level.go $env:TEMP\aicli-e2e.exe
# 期望输出：
# OK initialize
# OK session/new sessionId=session_... thought_level=[low medium high default]
# OK session/set_config_option thought_level=high
# OK upstream request carries reasoning_effort=high
# OK session/set_config_option thought_level=default
# OK upstream request omits reasoning_effort after clearing
# OK session/set_config_option rejects unknown effort
# E2E PASS
```

脚本流程：initialize → session/new（断言 `thought_level` 选项与 `default`
当前值）→ 切到 `high` 并发 prompt（断言 mock provider 收到的请求体
`reasoning_effort == "high"`）→ 切回 `default` 并发 prompt（断言请求体不再
带该字段）→ 断言未知取值被 `invalid params` 拒绝。

其余全链路脚本（同样先 `go build -o $env:TEMP\aicli-e2e.exe ./cmd/aicli/`，
再 `go run ./scripts/<脚本> $env:TEMP\aicli-e2e.exe`）：

| 脚本 | 断言要点 |
|---|---|
| `acp_e2e_session_mgmt.go` | 能力对象形状（`list/delete/close` 为 `{}`）；`session/list` 的 cwd 过滤、空结果 `[]`、`cursor=abc` 负例；delete 幂等 + list 消失 + load 报 `-32002`；close 取消在途 prompt 且保留历史（22 项断言） |
| `acp_e2e_notifications.go` | `available_commands_update` 名称无前导 `/`；`usage_update` 的 `used ≤ size`；`session_info_update` 标题非空、不含 sessionId/cwd、同标题去重；`plan` 条目与 todos 快照一致 |
| `acp_e2e_plan.go` | `plan` 通知（实时）与 `session/load` 回放重建的条目一致性 |
| `acp_e2e_thought_chunk.go` | `agent_thought_chunk` 与 `messageId` 分组（实时 + 回放） |
| `acp_e2e_thought_toolcall.go` | 同一 SSE delta 内的 reasoning + tool_calls 仍产出 thought 事件 |

## MCP 集成

### 三层口径（先看这里）

| 层 | 现状 |
|---|---|
| **本地配置链 MCP** | 可用。`~/.aicli/mcp.yaml` 里的 server 由全局 manager 连接，ACP 会话直接可用其工具（与交互式 chat 共用同一条引导路径） |
| **客户端下发 MCP**（`session/new` / `session/load` / `session/resume` 的 `mcpServers`） | 可用：stdio / http / sse 均支持，逐条容错解析 + 会话级生命周期 |
| **`additionalDirectories`** | 字段解析但**不生效**（未声明对应能力位，Zed 因此恒发 `[]`，无行为差异） |

因此「ACP 不支持 MCP」的旧口径已作废；准确说法是上面三层。

### 装配策略：`--acp-mcp`

| 取值 | 行为 |
|---|---|
| `merge`（默认） | 本地配置链 + 客户端下发 |
| `local` | 仅本地配置链 |
| `client` | 仅客户端下发 |
| `off` | 全部关闭 |

### 安全模型

- **folder trust 门控**：工作区未受信任时，客户端下发的 stdio server **不启动**，
  stderr 给出拒绝原因与 server 名；`session/new` 仍正常返回 `sessionId`。
  （下发来源包含仓库级 `.zed/settings.json`，所以门控不能外包给客户端。）
- **审计**：每次因客户端下发而启动的 server 都写一条 `acp.mcp.audit` 日志
  （来源、会话、server 名、传输类型、命令/cwd 或 url）。
- **凭据不入日志**：`env` / `headers` 只记录**键名**（`env_keys=` / `header_keys=`），
  值永不落盘、永不出现在 stderr。
- **回收**：会话私有 server 随 `session/close` / `session/delete` 回收；宿主进程被
  客户端强杀（Zed 的 `Drop` → `child.kill()`，不走协议关闭）时，stdio 子进程树由
  Windows Job Object（`KILL_ON_JOB_CLOSE`）连带回收，不留孤儿进程。

### 会话语义

- 三个入口（`session/new`、`session/load`、`session/resume`）共用同一条
  解析 → 映射 → 建连 → 去重路径，不存在「只有 new 生效」的分叉。
- 工具在**首个 prompt 前**最多等 2s（`WaitReady` 短超时），超时放行、不阻塞会话。
- 同名 server：客户端下发优先；工具名冲突走规范名 `mcp__<server>__<tool>`。
- 单条形状非法 / 未知 `type` / 缺必填字段：跳过该条 + 可见诊断，其余照常，
  `session/new` 不报错。
- 未声明传输（能力位为 `false` 的 http/sse）：跳过该条 + 诊断，**不产生连接尝试**。

### 报文示例

```jsonc
{"jsonrpc":"2.0","id":2,"method":"session/new","params":{
  "cwd":"E:\\demo",
  "mcpServers":[
    {"name":"echo","command":"C:/tools/echo-mcp-server.exe","args":["--stdio"],
     "env":[{"name":"TOKEN","value":"<secret>"}]},
    {"name":"remote","type":"http","url":"https://example.com/mcp",
     "headers":[{"name":"Authorization","value":"Bearer <token>"}]},
    {"name":"events","type":"sse","url":"https://example.com/sse"}
  ]}}
```

注意 `env` / `headers` 是 `[{name,value}]` **数组**（ACP v1 联合类型）；
对象形式会被判为解码失败并跳过该条。

### 诊断

- 会话级 MCP 状态与跳过原因走 stderr 诊断行（含 server 名与原因）；
- 传输生命周期事件复用 manager 的 observer（`mcp.transport.*`、
  `mcp.stdio.tree_guard_*`），便于定位「连没连上」「树收没收回」。

## 常见问题

**Q: session/new 报 `provider ... not found` / `no provider specified`？**
provider 解析链全部落空（flag、workspace、`aicli.chat.default_provider`、
`providers.default_provider` 均未指向启用的 provider）。用
`--provider` 显式指定，或在 `~/.aicli/config.yaml` 配置
`providers.default_provider`。注意残留的 runtime session 上下文里若存着
已删除的 provider 名，会被校验后安全降级，不会硬失败。

**Q: 取消后 prompt 返回 `end_turn` 而不是 `cancelled`？**
先确认 `requestId` 与 prompt 请求 id 指向同一值（本实现已做数字/字符串
归一化）。若仍复现，检查 agent 版本是否包含 `context.AfterFunc` 传导
（`agent_stdio.go` Prompt 内）与 `idKey` 归一化两处修复。

**Q: 客户端看到悬挂的 in_progress 工具调用？**
取消路径会补发终态 `tool_call_update`；若宿主进程被强杀（非协议取消），
进程退出本身会断开连接，客户端应按断连清理 UI 状态。

**Q: MCP servers 参数支持吗？**
支持。`session/new` / `session/load` / `session/resume` 的 `mcpServers` 会被逐条
容错解析并装配成会话级 MCP（stdio / http / sse），能力位 `mcpCapabilities` 为
`{http:true, sse:true}`。本地配置链（`~/.aicli/mcp.yaml`）不受影响、照旧可用。
装配范围由 `--acp-mcp=off|client|local|merge`（默认 `merge`）控制，未信任工作区
拒绝启动客户端下发的 server。细节见 [MCP 集成](#mcp-集成)。
`additionalDirectories` 仍解析但不生效。

**Q: 为什么 stdout 混入了非协议输出？**
不应发生。stdout 被保留为纯协议通道；若发现污染，检查是否误用了会写
stdout 的 hook/plugin，并提交 issue。日志请走 `--log-dir`。

**Q: 如何在 Zed 中添加 aicli 作为 ACP 服务？**
在 Zed 中添加 aicli ACP 服务（External Agent）：

1. 打开 **Agent Settings** → **External Agents** 页面 → 点击 **Add Agent** →
   选择 **Add Custom Agent**。Zed 会打开 `~/.config/zed/settings.json`
   （Windows 为 `%APPDATA%\Zed\settings.json`）并插入 `agent_servers` 模板。
2. 配置 aicli 二进制路径。推荐使用 `acp` 子命令（或 `--acp` flag，二者等价）：

   ```jsonc
   // ~/.config/zed/settings.json
   {
     "agent_servers": {
       "aicli": {
         "type": "custom",
         "command": "C:/Users/<user>/go/bin/aicli.exe",  // 必须是绝对路径
         "args": ["acp", "--provider", "openai", "--model", "gpt-4o"],
         "env": {}
       }
     }
   }
   ```

   > **Windows 用户**：`command` 必须是 `.exe` 的绝对路径，否则 Zed 无法
   > 找到可执行文件。`args` 推荐用 `["acp", ...]` 子命令形式；`["--acp", ...]`
   > flag 形式也支持，但旧版本 aicli 存在 `--acp` 被误写为 `acp chat ...`
   > 的 bug（已修复），请确保使用最新构建。

   > **会话持久化**：ACP 模式下会话默认持久化到本地会话目录
   > (`~/.aicli/sessions` 或 `%APPDATA%\aicli\sessions`)，以便
   > `session/load` 在 Zed 重启后能恢复对话。如需自定义存储位置，
   > 可添加 `--session-dir` 参数：
   > ```jsonc
   > "args": ["acp", "--session-dir", "C:/path/to/sessions"]
   > ```

3. 保存后无需重启 Zed，新代理会出现在 Agent Panel 的新建线程菜单中。
4. 如需调试：命令Palette 运行 `dev: open acp logs` 可查看 Zed 与 agent
   之间的协议消息。

**Q: session/load 报 `session not found`？**
ACP 会话默认持久化到本地会话目录，`session/load` 会从该目录加载会话。
请确认：
- 会话 ID 来自之前 `session/new` 的返回结果（而非手动编造）。
- `aicli acp` 进程在加载时使用**相同的会话目录**（默认
  `~/.aicli/sessions` / `%APPDATA%\aicli\sessions`）。如果
  `session/new` 时指定了 `--session-dir`，加载时也必须使用相同的目录。
- 会话目录有读写权限。
- 该会话之前是在**非 ephemeral** 模式下创建的（ACP 默认即为非
  ephemeral，参见上一问）。历史版本 aicli 可能默认 ephemeral，
   升级到最新构建即可。

**Q: Zed 侧报 `Parse error: invalid type: null, expected struct LoadSessionResponse`？**
这是历史版本 aicli 的 `session/load` 响应不符合 ACP v1 schema 导致的：
schema 将 `LoadSessionResponse` 定义为对象（`modes` / `configOptions` /
`_meta` 字段均可选），旧实现返回 JSON-RPC `result: null`，Zed 的 Rust
客户端反序列化到结构体时直接失败，导致会话附加中断（表现为"无法连接会话"）。
修复后成功加载的响应形如 `{"jsonrpc":"2.0","id":N,"result":{}}`；可运行
`dev: open acp logs` 确认 `session/load` 的 `result` 是 `{}` 而不是 `null`。
升级到包含该修复的构建即可恢复。

**Q: Zed 添加后无法启动 / 初始化失败怎么排查？**

- 用 `aicli acp` 手动在终端测试握手：
  ```powershell
  aicli acp --provider openai --model gpt-4o < $null
  ```
  正确时应在 stderr 输出 `Info: pprof endpoint...` 等启动日志，stdout
  保持静默（等待 NDJSON）。如果 stdout 有非 JSON 输出，说明存在
  stdout 污染，需检查是否启用了交互式 surface。
- 确认 `command` 是绝对路径且可执行；Zed 不会解析 `PATH`。
- 确认 `--provider` / `--model` 可在该环境解析到可用 provider。
- 使用 `dev: open acp logs` 检查 Zed 侧的 `initialize` 响应解析错误。
