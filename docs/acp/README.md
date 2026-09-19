# ACP 使用手册（aicli agent stdio）

本目录是 aicli 的 Agent Client Protocol（ACP）宿主使用文档。aicli 以
`aicli agent stdio` 子命令在 stdin/stdout 上提供 ACP 子集，可被任意实现了
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
10. [常见问题](#常见问题)

## 快速开始

```powershell
# 1. 启动 ACP 宿主（stdout 只输出 NDJSON 协议消息，日志走 stderr）
aicli agent stdio --provider openai --model gpt-4o

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
| `initialize` | 握手。返回 `protocolVersion`、`agentCapabilities`、`agentInfo`（name=`aicli`）、`authMethods` |
| `session/new` | 创建会话。参数 `{cwd, mcpServers}`（MCP 参数暂不支持，保留字段）；返回 `{sessionId}` |
| `session/prompt` | 运行一轮对话。参数 `{sessionId, prompt: ContentBlock[]}`；阻塞至本轮结束，返回 `{stopReason}`。当前仅 `type=text` 内容块 |
| `session/cancel` | 取消某 session 的在途 prompt。参数 `{sessionId}`（通知，无响应） |
| `session/load` | 恢复会话（仅 `loadSession=true` 能力时可用）。agent 通过 `session/update` 回放历史后返回 `null` |
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
  "promptCapabilities": {"image": false, "audio": false, "embeddedContext": false},
  "mcpCapabilities": {"http": false, "sse": false}
}
```

当前 MVP 仅支持文本 prompt；图片 / 音频 / embeddedContext 暂未开放。

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

工具调用 `status`：`pending` → `in_progress` → `completed` / `failed`。

工具 `kind` 映射 ACP 分类：`read` / `edit` / `delete` / `move` / `search` /
`execute` / `think` / `fetch` / `other`。

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
  （user/assistant 消息、工具调用），然后返回 `null` 结果；客户端此后可
  直接 `session/prompt` 继续对话。
- 解析顺序：进程内已附着 session → 持久化 store（非 ephemeral 时）。

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
`session/new` / `session/load` 的 `mcpServers` 字段目前保留但不生效，
agent 侧 MCP 能力（http/sse）能力位均为 false。

**Q: 为什么 stdout 混入了非协议输出？**
不应发生。stdout 被保留为纯协议通道；若发现污染，检查是否误用了会写
stdout 的 hook/plugin，并提交 issue。日志请走 `--log-dir`。
