# WorkBuddy / WorkBuddy AI 会话格式兼容与迁移

> 状态：只读导入 + bundle 导出已实现；直接写 `workbuddy.db` 的 `--apply` 未实现
>
> 日期：2026-10-29
>
> 关联文档：[cross-agent-session-transfer.md](./cross-agent-session-transfer.md)、[deepseek-harness-session-transfer.md](./deepseek-harness-session-transfer.md)

## 1. 结论

WorkBuddy / WorkBuddy AI 使用「SQLite 索引 + 每会话 JSONL 正文」的本地存储：

```text
%USERPROFILE%\.workbuddy/
  workbuddy.db
  projects/<project-slug>/<session-id>.jsonl
%USERPROFILE%\.workbuddy-ai/
  workbuddy.db
  projects/<project-slug>/<session-id>.jsonl
```

`workbuddy.db` 不是消息正文库；消息、reasoning、工具调用和工具结果都在 JSONL。
因此不能只读数据库，也不能只把 JSONL 文件复制到目标目录就假定 WorkBuddy UI
能发现会话。

当前实现：

- 检测两个默认实例，并支持任意可验证的数据根；
- 只读打开 SQLite，按 `sessions` 索引列出会话；
- 解析 WorkBuddy JSONL；
- 处理 `resend-fork-notice` 的 `surface` / `transcript` 语义；
- 默认只提取 `<user_query>`，不把 SOUL/USER/项目树等注入上下文带进 aicli；
- 按 `callId` 配对工具调用与结果；
- 导出 WorkBuddy-readable JSONL bundle + `session-index.json`，不写目标 DB。

尚未实现：

- 直接写 `workbuddy.db` / `workspaces` / `session_usage`；
- 官方 import 入口（当前没发现可依赖的命令或 API）；
- 图片、附件、file-history-snapshot、subagent/team 完整迁移；
- 大工具输出外部化到 `tool-results/`；
- `portable` 通用 bundle。

## 2. 数据根与实例

| 默认根 | 实例 ID | 说明 |
|---|---|---|
| `%USERPROFILE%\.workbuddy` | `workbuddy:default` | 独立 profile |
| `%USERPROFILE%\.workbuddy-ai` | `workbuddy:ai` | 独立 profile |
| 任意非标准根 | `workbuddy:<short-hash>` | 需通过 DB + projects 签名 |

兼容别名：`workbuddy-ai:default` 可解析到 `.workbuddy-ai`。

环境变量：

- `WORKBUDDY_HOME`：追加/覆盖主根；
- `WORKBUDDY_AI_HOME`：追加/覆盖 AI 根。

检测不是只看目录名。WorkBuddy 签名至少要求：

1. 存在 `workbuddy.db`；
2. SQLite 可只读打开；
3. `sessions` 表存在，且至少有 `id` / `cwd`；
4. `projects/` 目录存在；
5. 至少一个 `projects/**/<session-id>.jsonl` 可解析（空实例也可被识别，但标记 `empty`）。

`<root>/sessions/*.json` 是**运行实例心跳**，不是历史正文。它们只用于辅助判断
目标会话是否正在运行，不能当作 session transcript。

## 3. 磁盘布局

```text
<root>/
  workbuddy.db
  projects/
    <project-slug>/
      <session-id>.jsonl
      <session-id>.file-rollback.ndjson
      <session-id>/
        tool-results/
          <call-id>-render.txt
  sessions/
    <pid>.json                 # runtime heartbeat，非历史
```

重要字段：

- `projects/<slug>/<id>.jsonl`：append-only 事件流；
- `.file-rollback.ndjson`：文件回滚快照，不导入；
- `tool-results/`：大工具输出的 sidecar，首版只导入 JSONL 里已有的文本；
- `workbuddy.db.sessions`：列表、标题、cwd、用户、状态、时间、模型等索引；
- `workbuddy.db.workspaces`：工作区最近打开时间；
- `__workbuddy_drizzle_migrations`：schema migration 线索，不是格式版本号。

## 4. JSONL 事件

### 4.1 通用 envelope

```json
{
  "type": "message",
  "id": "01a...",
  "parentId": "01a...",
  "sessionId": "e628...",
  "timestamp": 1792235106293,
  "cwd": "E:/projects/ai/opencode-proxy",
  "providerData": {
    "messageId": "01a...",
    "stepSeq": 1,
    "model": "deepseek-v4.1-flash",
    "conversationRequestId": "01a..."
  }
}
```

### 4.2 已观察事件

| type | 用途 | 导入策略 |
|---|---|---|
| `session-meta` | 会话/宿主元数据 | 跳过 |
| `message` role=user | 用户输入 | 提取 `<user_query>` |
| `message` role=assistant | assistant 正文 | 转为 assistant |
| `reasoning` | 推理文本（`rawContent[].type=reasoning_text`） | 转为 reasoning metadata |
| `function_call` | 工具调用 | 转为 assistant `ToolCalls` |
| `function_call_result` | 工具结果 | 转为 tool 消息 |
| `ai-title` | AI 标题 | 标题 fallback |
| `file-history-snapshot` | 文件快照 | 跳过，warning |
| `resend-fork-notice` | 重发/编辑分支 | 只参与 active branch |
| `summary` / `topic` / `goal-*` | 压缩/辅助 | 首版跳过 |

### 4.3 工具调用

```json
{
  "type": "function_call",
  "id": "01a...event",
  "parentId": "01a...assistant-or-reasoning",
  "callId": "call_...",
  "name": "Bash",
  "arguments": "{\"command\":\"pwd\"}"
}
```

```json
{
  "type": "function_call_result",
  "id": "01a...event",
  "parentId": "01a...call-event",
  "callId": "call_...",
  "name": "Bash",
  "status": "completed",
  "output": {"type": "text", "text": "..."}
}
```

配对键是 `callId`，不是事件 `id`。导入实现会：

- 优先按 `providerData.messageId`、否则按 `parentId == assistant.id` 将
  `function_call` 聚合到 assistant；同一 step 的多个 call 生成一个 assistant；
- 对没有匹配 result 的 call、没有匹配 call 的 result 生成 warning；
- 保留 `arguments` 原文，JSON 解析失败时放入 `_raw` 并 warning；
- 不把同一 call 重复生成为两条 assistant 消息。

## 5. Active branch / `resend-fork-notice`

WorkBuddy 的 JSONL 会保留被编辑或重发替换的旧分支。样例：

```text
u-first -> a-first -> u-old -> a-old
                    -> u-new -> a-new
resend-fork-notice:
  parentId = a-first
  editedUserItemId = u-old
```

`surface` 视图：

1. 用 `id/parentId` 建立事件树；
2. 对每个 `resend-fork-notice`，把 `editedUserItemId` 及其整棵子树标记 inactive；
3. 保留 fork 点之前的公共历史 + 新分支。

`transcript` 视图：

- 保留 append-origin 的全部消息；
- 对 fork 产生 warning；
- 适合阅读/归档，不适合继续作为模型上下文。

## 6. 用户上下文清洗

WorkBuddy 把系统提示和项目上下文塞进 user message：

```text
<system-reminder data-role="user-context">
  <user_info>...</user_info>
  <identity_context>...</identity_context>
  <project_context>...</project_context>
</system-reminder>
<user_query>真正的用户输入</user_query>
```

默认策略：

- 存在 `<user_query>` 时只取其中文本；
- 兼容 `<user_message>` / `<user_input>` / `<user_intent>`；
- 没有显式 query 时删除 `system-reminder` / `project_context` / `identity_context`，
  仅保留剩余文本；
- 跳过只有注入上下文的 user message；
- 不把 WorkBuddy 的 system/developer 内容变成 aicli system 消息；
- 首版不保存原始完整 user context，避免隐私和 prompt injection 风险。

## 7. aicli 映射

| WorkBuddy | aicli |
|---|---|
| user message | `types.NewUserMessage` |
| assistant message | `types.NewAssistantMessage` |
| reasoning | `ReasoningBlock{Provider:"workbuddy"}` |
| function_call | assistant `ToolCalls` |
| function_call_result | `types.NewToolMessage(callId, ...)` |
| ai-title / DB title | `Session.Metadata.Title` |
| DB cwd | `metadata.context.workspace_path` |

溯源字段：

```text
source_agent          = workbuddy
source_path           = JSONL 绝对路径
source_session_id     = WorkBuddy session id
source_format         = workbuddy-session-jsonl
source_format_version = 1
source_view           = surface | transcript
source_cwd            = DB/事件 cwd
source_event_count    = JSONL 记录数
```

用户归属仍由既有 aicli import 规则决定：`--user` > 当前默认用户；不沿用
WorkBuddy 的 `user_id`。

## 8. CLI

```powershell
# 检测两个默认 profile
aicli transfer detect --agent workbuddy --json

# 列出 WorkBuddy AI
aicli transfer list --agent workbuddy --instance workbuddy:ai --limit 10 --json

# 导入最新会话（dry-run）
aicli transfer import `
  --from workbuddy `
  --instance workbuddy:ai `
  --latest --dry-run --json

# 导入指定会话
aicli transfer import `
  --from workbuddy `
  --instance workbuddy:default `
  --session 832fbbd9-f534-4ae4-a327-ed9de6f5048c `
  --user alice --new-id --json

# 导入 bundle 中的单文件
aicli transfer import `
  --from workbuddy `
  --file .\export\projects\e-projects-ai-opencode-proxy\<uuid>.jsonl `
  --user alice --json
```

非标准布局：

```powershell
# 数据根可识别
aicli transfer list --agent workbuddy --root D:\PortableApps\WorkBuddy\data --json

# DB 和 projects 分离
aicli transfer list `
  --agent workbuddy `
  --root D:\AgentData `
  --db D:\AgentData\index\workbuddy.db `
  --projects-dir D:\AgentData\projects `
  --json

aicli transfer import `
  --from workbuddy `
  --root D:\AgentData `
  --db D:\AgentData\index\workbuddy.db `
  --projects-dir D:\AgentData\projects `
  --latest --json
```

`--root` 可以是：

- 数据根（含 `workbuddy.db` + `projects`）；
- `projects/`；
- 某个 project 目录；
- 某个 session JSONL 文件所在目录。

## 9. 导出 bundle

```powershell
aicli transfer export `
  --to workbuddy `
  --session <aicli-session-id> `
  --bundle-dir .\workbuddy-export `
  --target-user <workbuddy-user-id> `
  --json
```

产物：

```text
< bundle >/
  manifest.json
  session-index.json
  projects/<project-slug>/<new-uuid>.jsonl
```

`project-slug` 规则：

```text
C:\Users\<user>\WorkBuddy AI\ai-agent-runtime
-> c-Users-<user>-WorkBuddy AI-ai-agent-runtime
```

即：盘符转小写并与其后的第一个分隔符合并为一个 `-`，其余 `/`/`\`/`:` 转 `-`，
保留空格和路径中的普通 `-`。

`manifest.json` 记录 `requires_index: true`、`apply_supported: false`；
`session-index.json` 是给人工/后续 `--apply` 的合并提示，不是可直接导入的
WorkBuddy 数据库。导出不写 `workbuddy.db`，也不写 `workspaces` / `session_usage`。

### 9.1 生成事件

```text
session-meta
user message
  -> reasoning（可选）
  -> assistant message
  -> function_call（每个 call）
  -> function_call_result（每个 tool 结果）
ai-title
```

缺失 tool result 时会写入一条错误结果并 warning，避免 dangling call。

## 10. 目录结构与代码

```text
backend/internal/sessionport/workbuddy/
  types.go       session/db/record/import/export 类型
  paths.go       默认根、实例 ID、路径解析、project slug
  storage.go     只读 SQLite、sessions 行读取、runtime heartbeat
  detect.go      Detect / DetectInstance / List
  reader.go      JSONL、fork、用户清洗、工具配对、aicli 转换
  export.go      aicli -> WorkBuddy JSONL bundle

backend/internal/sessionport/registry/
  registry.go    自动识别 DSH/WorkBuddy、统一 CLI 分派、实例别名
```

CLI：

```text
backend/cmd/aicli/commands/transfer.go
backend/cmd/aicli/commands/transfer_detect.go
backend/cmd/aicli/commands/transfer_list.go
backend/cmd/aicli/commands/transfer_import.go
backend/cmd/aicli/commands/transfer_export.go
```

## 11. 测试与验证

已覆盖：

- SQLite fixture + JSONL fixture 的 detect/list/read；
- `<user_query>` 提取；
- reasoning 与 tool call/result 映射；
- `resend-fork-notice` surface/transcript；
- WorkBuddy bundle round-trip；
- project slug 规则；
- 重复导出拒绝覆盖；
- CLI WorkBuddy dry-run import。

验证命令：

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/sessionport/workbuddy -count=1
go test ./cmd/aicli/commands -run 'TestTransfer' -count=1
```

真实抽样：

```powershell
go run ./cmd/aicli transfer detect --agent workbuddy --json
go run ./cmd/aicli transfer list --agent workbuddy --instance workbuddy:ai --limit 3 --json
```

## 12. 风险与后续

| 风险 | 处理 |
|---|---|
| WorkBuddy 私有 schema 升级 | 只读签名校验；写入必须版本门控 |
| DB 有会话但 JSONL 缺失 | 列表标 unreadable + warning |
| orphan JSONL 没有 DB 行 | 列表仍可见，但标记索引缺失 |
| fork 旧分支 | surface 删除子树；transcript 保留 |
| 注入上下文 | 默认清洗 `<user_query>` |
| 大工具输出 | 首版保留 JSONL 文本；后续接 `tool-results/` |
| 图片/文件 | 首版文本占位 + warning |
| 目标 WorkBuddy 运行中 | 只生成 bundle，不写目标 |
| 没有官方 import | bundle-first；`--apply` 后续实验 |
| UUIDv7 | 当前 UUID v4；后续可升级 |

后续开发顺序：

1. `--apply`：进程检查、备份、JSONL 原子写、SQLite 事务、读回验证；
2. `workspaces` / `session_usage` 索引；
3. 附件与 `tool-results` 外部化；
4. subagent/team；
5. CodeBuddy CLI / Codex / Claude Code / Gemini 适配器。
