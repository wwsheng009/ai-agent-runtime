# aicli 跨 Agent 会话历史迁移方案

> 状态：已实施 DSH Phase 1–3、WorkBuddy Phase 1–2（检测、列表、只读导入、bundle 导出；`--apply` 未实现）
>
> 日期：2026-10-29
>
> 范围：`aicli transfer` 命令、registry 分派、WorkBuddy / DeepSeek Harness 适配
>
> 关联文档：[session-export-import.md](./session-export-import.md)、[workbuddy-session-transfer.md](./workbuddy-session-transfer.md)、[deepseek-harness-session-transfer.md](./deepseek-harness-session-transfer.md)

## 1. 背景与结论

目标是让 aicli 能与其它本地 Agent 交换会话历史：

- 把其它 Agent 的历史会话导入 aicli 会话库；
- 把 aicli 会话导出为目标 Agent 可识别的历史；
- 自动检测本机存在哪些 Agent，以及哪些实例具备读取/写入能力。

结论：

1. **可以实现**，但应把「跨 Agent 迁移」与现有的 aicli 原生 `export/import` 分开设计。
2. 建议新增独立命令组 **`aicli transfer`**，内部复用现有会话归一化、存储和读回校验逻辑。
3. 首批适配 **WorkBuddy / WorkBuddy AI** 与 **DeepSeek Harness**。先做只读导入，再产出原生导出包，最后把直接写入目标存储作为受控实验能力。
4. 不建议默认直接写 `workbuddy.db`。WorkBuddy 本地历史是私有格式，版本升级后可能变化；直接写入必须显式 `--apply`，并要求目标应用关闭、自动备份、失败回滚。
5. 检测必须基于数据签名，而不是只看目录名。目录存在不等于存在可迁移历史。

### 1.1 当前实现结果

截至 2026-10-29，已经落地：

- `aicli transfer`：交互终端中默认进入操作菜单，可选择检测、检查、导入、导出向导或退出；非 TTY 回退到命令帮助；
- `aicli transfer detect`：默认同时探测 DSH 与 WorkBuddy；交互终端中进入全屏实例/会话浏览，也支持 `--agent` / 显式 `--root` / `--no-interactive`；
- `aicli transfer inspect`：只读列出默认实例的可导入会话，或列出 DSH ZIP 内的根会话候选；文本结果逐条显示会话 CWD，JSON 结果包含 `cwd`；
- `aicli transfer list`：DSH 与 WorkBuddy 统一列表，WorkBuddy 支持 `--db` / `--projects-dir` 非标准布局；
- `aicli transfer import`：默认 `--from auto`，支持 DSH 官方 `/export` ZIP、DSH v4 JSONL（含多帧 zstd）、WorkBuddy `projects/<slug>/<id>.jsonl`；
- `aicli transfer import --interactive`：按默认路径探测实例和会话，交互选择后确认导入；ZIP 模式会列出根会话候选；
- `aicli transfer export`：DSH v4 bundle、WorkBuddy JSONL + `session-index.json` bundle；支持 `--interactive` 完整导出向导；
- WorkBuddy `resend-fork-notice` 的 `surface` / `transcript` 分支处理；
- WorkBuddy 导入默认从 `<user_query>` 提取真实用户输入，跳过注入上下文；
- DSH / WorkBuddy 工具调用与结果按 `callId` 配对，缺失时产生 warning；
- 复用既有 aicli import 核心完成 ID 冲突、用户归属、消息身份补齐和读回校验。

还没有实现：

- 直接写 `workbuddy.db` 或 DSH `$DSH_HOME` 的 `--apply`；
- WorkBuddy `workspaces` / `session_usage` 索引写入；
- 通用 `portable` bundle；
- 附件、图片、文件快照、subagent / team 子树完整迁移；
- Codex / Claude Code / Gemini CLI 等其它适配器。

### 1.2 非目标

首个版本不做：

- 整机/整目录备份；
- 云同步账号迁移；
- provider 凭证、MCP 密钥、workspace trust、permission 设置迁移；
- 文件历史（file history/checkpoint）迁移；
- subagent、team、background job 全量迁移；
- 跨 Agent 的「继续运行」语义保证；
- 云端 Agent 的远程 API 导入导出。

---

## 2. 当前实现与目标格式

### 2.1 aicli 侧

现有能力：

- `aicli export --full`：导出完整会话 JSON envelope；
- `aicli import <file>`：只接受 `version == 1` 的完整 JSON；
- `--new-id`：目标已有同 ID 时生成新 ID；
- 默认不覆盖已有会话；
- 导入通过 `runtimechat.SessionStorage.Save` 写入，而不是直接操作 SQLite；
- 导入后会读回并校验 canonical 消息数量。

主要代码：

- `backend/cmd/aicli/commands/export.go`
- `backend/cmd/aicli/commands/import.go`
- `backend/cmd/aicli/commands/chat_export_command.go`
- `backend/internal/chat/sqlite_storage.go`
- `backend/internal/chat/session.go`
- `docs/aicli/session-export-import.md`

当前 aicli 会话库：

```text
~/.aicli/sessions/session_history.sqlite
  sessions
  session_messages          // canonical 历史
  session_prompt_messages   // prompt 投影
```

跨 Agent 导入不应自行写这些表；应构造 `runtimechat.Session` 后走与现有 import 相同的写路径。

### 2.2 WorkBuddy 侧

抽样环境中的 WorkBuddy / WorkBuddy AI 是两个独立数据根；具体版本以
`sessions/*.json` 的 `version` 字段或应用元数据为准，不作为兼容门控：

```text
%USERPROFILE%\.workbuddy
%USERPROFILE%\.workbuddy-ai
```

每个实例包含：

```text
workbuddy.db                 // 会话索引和设置，不保存消息正文
projects/<project-slug>/
  <session-id>.jsonl         // 真正的会话历史
```

`workbuddy.db` 的 `sessions` 表保存：

- `id`
- `cwd`
- `user_id`
- `title` / `custom_title`
- `status`
- `created_at` / `updated_at` / `last_activity_at`
- `model`
- `permission_mode`
- `transport`
- 以及 WorkBuddy 自己的会话设置

`projects/<slug>/<session-id>.jsonl` 是事件流。抽样中观察到的事件类型：

| 事件类型 | 用途 | 是否作为聊天历史导入 |
|---|---|---|
| `session-meta` | 会话元信息 | 否 |
| `message` | 用户/助手消息 | 是 |
| `reasoning` | 推理内容 | 是，转为 reasoning metadata |
| `function_call` | 工具调用 | 是，转为 assistant tool calls |
| `function_call_result` | 工具结果 | 是，转为 tool 消息 |
| `ai-title` | AI 标题 | 转为标题 |
| `file-history-snapshot` | 文件快照 | 否 |
| `resend-fork-notice` | 用户编辑/重发分支 | 仅参与分支重建 |
| `summary` / `topic` / `goal-*` | 压缩或辅助信息 | 默认否，保留为 warning/metadata |

注意：`sessions/*.json` 不是历史。它描述运行实例/端点，包含 `pid`、`endpoint`、`kind`、`cwd` 等字段；不要把它当作会话内容。

### 2.3 WorkBuddy 是否有官方导入入口

WorkBuddy 安装包内包含 `codebuddy` CLI，支持 `--resume` / `--continue`，但当前没有发现可依赖的 session import/export 子命令；其 `/export` 执行器目前是空实现。

因此：

- **读取** WorkBuddy JSONL 可以按现有数据模型实现；
- **写入** WorkBuddy 原生存储属于私有格式写入，必须标记为实验能力；
- 如果未来发现官方导入 API 或 CLI，应优先切换到官方入口。

### 2.4 检测不能只看目录

本机抽样结果说明目录名不是可靠信号：

- `~/.cursor` 只有 `skills/`，没有可迁移历史；
- `~/.claude` 只有 `skills/`，没有 `projects` 历史；
- `~/.gemini` 有 `history/`，但当前为空；
- `~/.codex` 有 SQLite 状态库、配置和日志，会话格式与 WorkBuddy 不同；
- `~/.codebuddy` 存在，但没有实际 projects 历史。

检测层必须交叉验证数据库签名、transcript 文件和可解析性。

---

## 3. 设计原则

1. **适配器隔离**：目标 Agent 的格式处理放在独立包中，CLI 不直接拼 JSONL 或 SQL。
2. **单一会话模型**：内部以 `runtimechat.Session` 为 canonical session；不为每个目标另造消息模型。
3. **安全默认值**：
   - 导入源只读；
   - 导出默认只生成 bundle；
   - 直接写入必须显式 `--apply`；
   - 目标应用运行时拒绝写入；
   - 不覆盖目标已有会话。
4. **不迁移凭证和权限**：provider key、MCP secret、workspace trust、permission mode、identity/memory 文件一律不跨 Agent 复制。
5. **可追溯**：每次跨 Agent 迁移写入 source agent/root/session id、目标 id、时间、备份路径和 warnings。
6. **版本门控**：只对已测试的目标 schema 开启写入；未知版本降级为只读或不支持。
7. **两遍解析**：大会话文件先建轻量索引，再只解析 active branch；避免第一遍把全部内容读进内存。
8. **原子写入**：临时文件 + rename；SQLite 事务；失败恢复备份。

---

## 4. CLI 契约

### 4.1 命令组

建议新增：

```text
aicli transfer detect
aicli transfer list
aicli transfer import
aicli transfer export
aicli transfer probe              # 后续
aicli transfer roots list|add|remove  # 后续
```

不直接改动 `aicli export` / `aicli import` 的现有语义。等 `transfer` 稳定后，可以再考虑增加别名：

```text
aicli import --from workbuddy ...
aicli export --to workbuddy ...
```

### 4.2 示例

```powershell
# 进入 transfer 交互菜单（TTY）
aicli transfer

# 直接进入导出向导
aicli transfer export --interactive

# 向导默认：DSH 使用 zstd，WorkBuddy 使用 none；可用 --yes 跳过最终确认
aicli transfer export --interactive --yes

# 检测本机实例
aicli transfer detect --json
aicli transfer detect --no-interactive

# 交互终端默认进入全屏实例/会话浏览；界面只读，不会自动导入
aicli transfer detect

# 只读检查默认实例和会话，或检查 ZIP 内的根会话
aicli transfer inspect --agent auto --json
aicli transfer inspect --zip .\dsh-session-xxx.zip --json

# 交互选择实例/会话；ZIP 模式会列出 ZIP 内候选
aicli transfer import --interactive
aicli transfer import --zip .\dsh-session-xxx.zip --interactive

# 列出某个 WorkBuddy 实例的会话
aicli transfer list `
  --agent workbuddy `
  --instance workbuddy:ai `
  --json

# 预检从 WorkBuddy 导入，不写 aicli
aicli transfer import `
  --from workbuddy `
  --instance workbuddy:ai `
  --latest `
  --dry-run `
  --json

# 导入指定 WorkBuddy 会话到 aicli
aicli transfer import `
  --from workbuddy `
  --instance workbuddy:ai `
  --session 21f62f31-db3e-460c-a5e4-8aa3ecea21f7 `
  --new-id `
  --user alice `
  --json

# 导出 aicli 会话为 WorkBuddy 原生 bundle，不写目标数据根
aicli transfer export `
  --to workbuddy `
  --session session_20260923112350_fny0opuB `
  --bundle-dir .\workbuddy-export

# 非标准目录：显式 root 检测和列出
aicli transfer detect `
  --root D:\AgentData `
  --json

aicli transfer list `
  --agent workbuddy `
  --root D:\PortableApps\WorkBuddy\data `
  --json

# 非标准布局：显式指定 DB 和 projects 目录
aicli transfer list `
  --agent workbuddy `
  --db D:\AgentData\workbuddy.db `
  --projects-dir D:\AgentData\projects `
  --json

# 导出到非标准 root：当前 export 只生成 bundle，不接收 target root
aicli transfer export `
  --to workbuddy `
  --session session_20260923112350_fny0opuB `
  --bundle-dir .\portable-export
```

### 4.3 Flag 约定

`detect`：

| Flag | 说明 |
|---|---|
| `--agent <id>` | 只检测指定适配器 |
| `--root <dir>` | 显式追加/指定根目录，可重复 |
| `--db <path>` / `--projects-dir <path>` | WorkBuddy 非标准布局显式覆盖 |
| `--no-interactive` | 强制纯文本输出；`--json` 和管道也会自动关闭交互界面 |
| `--json` | 机器可读输出 |

`inspect`：

| Flag | 说明 |
|---|---|
| `--agent <id>` | `auto`、`deepseek-harness` 或 `workbuddy` |
| `--root <dir>` | 实例根，可重复；按内容签名识别适配器 |
| `--instance <id>` | 只检查指定实例 |
| `--db <path>` / `--projects-dir <path>` | WorkBuddy 非标准布局 |
| `--zip <file>` | 只读列出 DSH 官方 `/export` ZIP 内的根会话 |
| `--session <id>` | 只显示指定源会话 |
| `--limit <n>` | 每个实例最多列出的会话数；0 = 不限制 |
| `--json` | 机器可读输出 |

`list`：

| Flag | 说明 |
|---|---|
| `--agent <id>` | 适配器 id |
| `--instance <id>` | `detect` 返回的实例 id |
| `--root <dir>` | 直接指定实例根 |
| `--db <path>` | 适配器特定：覆盖数据库路径，目前用于 WorkBuddy |
| `--projects-dir <path>` | 适配器特定：覆盖 transcript 根目录，目前用于 WorkBuddy |
| `--limit <n>` | 最大条数（0 = 不限制） |
| `--json` | 机器可读输出 |

`import`：

| Flag | 说明 |
|---|---|
| `--from <agent>` | 源适配器；默认 `auto`，也支持 `deepseek-harness` / `workbuddy` |
| `--instance <id>` / `--root <dir>` | 源实例 |
| `--zip <file>` | DSH 官方 `/export` ZIP（仅 deepseek-harness） |
| `--file <file>` | WorkBuddy 独立 JSONL/bundle 文件 |
| `--db <path>` / `--projects-dir <path>` | 源布局覆盖；必须显式提供，且必须通过签名校验 |
| `--session <id>` / `--latest` | 源会话选择；二选一 |
| `--view surface\|transcript` | WorkBuddy/DSH 历史视图 |
| `--user <id>` | aicli 目标用户 |
| `--new-id` | 冲突时生成新 aicli ID |
| `--dry-run` | 只解析和预检 |
| `--interactive` | 探测实例、列出会话并交互选择；ZIP 模式列出 ZIP 内候选；仅支持 text 输出 |
| `--yes` | 交互模式跳过最终确认 |
| `--json` | 机器可读摘要 |

`export`：

| Flag | 说明 |
|---|---|
| `--to <agent>` | 目标适配器：`deepseek-harness` 或 `workbuddy` |
| `--session <id>` | aicli 会话 ID |
| `--bundle-dir <dir>` | bundle 输出目录；默认 `~/.aicli/transfer/out/<timestamp>` |
| `--cwd <path>` | 覆盖目标工作目录 |
| `--target-user <id>` | WorkBuddy 目标用户；写入 session-index，不迁移身份 |
| `--compression none\|zstd` | DSH bundle 物理编码；WorkBuddy 只支持 `none` |
| `--interactive` | 交互选择目标、源会话、压缩、输出目录和 cwd |
| `--yes` | 导出向导跳过最终确认 |
| `--json` | 机器可读输出 |

### 4.4 退出码

沿用现有 aicli 命令约定：

| 退出码 | 含义 |
|---|---|
| `0` | 成功；`--dry-run` 预检通过 |
| `1` | 参数错误、选项冲突、缺少目标 |
| `2` | 确定性失败：格式不支持、目标进程运行、锁冲突、写入/读回失败 |

失败 JSON 使用：

```json
{
  "ok": false,
  "command": "transfer export",
  "error": "target is busy",
  "details": {
    "reason": "target_busy",
    "instance": "workbuddy:default",
    "processes": ["WorkBuddy.exe:13896"]
  }
}
```

具体进程 pid 只用于本机诊断，不写入跨机器可分享的 bundle。

---

## 5. 总体架构

```text
aicli transfer
  ├─ detect  -> registry.Detect -> dsh/workbuddy signatures
  ├─ inspect -> default roots / ZIP root-session candidates
  ├─ list    -> registry.List   -> adapter.List
  ├─ import  -> registry.Read   -> adapter JSONL/ZIP -> aicli writer
  └─ export  -> aicli reader    -> registry.Export -> adapter bundle
```

当前实现目录（已落地）：

```text
backend/internal/sessionport/
  types.go                 // AgentID / Instance / SessionRef / Capabilities
  registry/
    registry.go            // 自动识别、统一 Read/Export 分派、实例别名
  dsh/
    types.go
    paths.go
    reader.go
    detect.go
    surface.go
    convert.go
    export.go
    zip.go                 // 官方 /export ZIP 候选扫描
  workbuddy/
    types.go
    paths.go
    storage.go             // 只读 SQLite 索引
    detect.go
    reader.go              // JSONL、fork、用户上下文清洗、工具配对
    export.go
```

CLI 只依赖 `registry`，不直接解析目标 Agent 的 SQL/JSONL。后续增加
Codex/Claude 等适配器时，新增 `sessionport/<agent>/` 并在 registry 注册即可。

CLI 放在：

```text
backend/cmd/aicli/commands/transfer.go
backend/cmd/aicli/commands/transfer_root.go
backend/cmd/aicli/commands/transfer_detect.go
backend/cmd/aicli/commands/transfer_inspect.go
backend/cmd/aicli/commands/transfer_list.go
backend/cmd/aicli/commands/transfer_import.go
backend/cmd/aicli/commands/transfer_interactive.go
backend/cmd/aicli/commands/transfer_export.go
```

### 5.1 当前分派边界

首版没有引入跨包 `Adapter` interface，而是用
`sessionport.Instance.AgentID` + `registry` 做显式分派，原因是两个
适配器的导入/导出参数不同（DSH ZIP、zstd、WorkBuddy DB/projects/file）。
当第三个适配器出现时，再把公共部分收敛为 interface，避免现在为两个实现
过早抽象。

公共能力仍由 `sessionport.Capabilities` 暴露：

```go
type Capabilities struct {
    List        bool
    Read        bool
    WriteBundle bool
    Apply       string // "none" | "experimental" | "stable"
}
```

### 5.2 TransferSession

```go
type TransferSession struct {
    Session  *runtimechat.Session
    Source   SourceInfo
    Assets   []Asset
    Warnings []Warning
}

type SourceInfo struct {
    AgentID       string
    InstanceID    string
    Root          string
    SessionID     string
    Format        string
    AppVersion    string
    SchemaVersion string
}

type Warning struct {
    Code    string
    Message string
    Source  string
}
```

复用 `runtimechat.Session` 的好处：

- 导入 aicli 时直接走现有存储层；
- 导出 aicli 时复用 canonical history 读取；
- ID 冲突、用户归属、消息身份补齐、读回校验都沿用同一份实现。

### 5.3 关键复用点

需要把现有 import 核心从「读文件 + 写库」中拆成两层：

```text
importSessionFromEnvelope(envelope, manager, options)
```

这样：

- 现有 `aicli import` 继续读 JSON 文件；
- `aicli transfer import --from workbuddy` 直接把 WorkBuddy reader 产出的 envelope/session 交给同一个写库函数；
- 不复制一套 ID/游标/metadata 归一化逻辑。

---

## 6. 检测设计

### 6.1 默认扫描根

只扫描已知根和显式传入的 `--root`，不要递归扫描整个用户目录或系统盘。

| Agent | 默认根 | 备注 |
|---|---|---|
| WorkBuddy | `%USERPROFILE%\.workbuddy` | 独立实例 |
| WorkBuddy AI | `%USERPROFILE%\.workbuddy-ai` | 独立实例 |
| WorkBuddy 自定义 | `WORKBUDDY_HOME` / `WORKBUDDY_AI_HOME` | 由用户指定 |
| CodeBuddy CLI | `%USERPROFILE%\.codebuddy` | 与 WorkBuddy 同属 JSONL 家族，但需单独验证 |
| Codex | `%USERPROFILE%\.codex` | 先只读识别，后续适配 |
| Claude Code | `%USERPROFILE%\.claude` | 有 `projects` transcript 才可读 |
| Gemini CLI | `%USERPROFILE%\.gemini` | 有实际历史才可读 |
| Cursor | `%USERPROFILE%\.cursor` | 目前只观察到 skills，不视为历史 |
| aicli | `%USERPROFILE%\.aicli\sessions` | 原生 SQLite |

Linux/macOS 使用对应的 `$HOME` 路径，逻辑一致。

### 6.2 数据签名与置信度

WorkBuddy 的 Probe 至少检查：

- `workbuddy.db` 存在；
- SQLite 可只读打开；
- 存在 `sessions` 表，且列包含 `id,cwd,user_id,status,updated_at`；
- `projects/` 目录存在；
- 至少一个 `*.jsonl` 能解析出 JSON 对象；
- 可选读取 `last-launch.json` / `app/renderer-version.json` 得到版本。

建议置信度：

| 信号 | 分数 |
|---|---:|
| 目录名命中 | 0.2 |
| `workbuddy.db` 存在 | 0.3 |
| `sessions` 表签名匹配 | 0.6 |
| 至少一个 transcript 可解析 | 0.9 |
| 版本可识别且 schema 在支持范围 | 1.0 |

置信度低于阈值时只显示为「发现疑似目录」，不提供写入能力。

其它 Agent 同样使用签名，而不是硬编码 `supported=true`：

- Codex：`.codex/config.toml` + 状态 SQLite；
- Claude Code：`.claude/projects/**/*.jsonl`；
- Gemini CLI：`history/` 或 `tmp/` 下存在会话文件；
- Cursor：只有 skills/settings 时返回 `no transcript store found`。

### 6.3 实例与能力输出

`detect --json` 返回实例列表，而不是单一布尔值：

```json
{
  "instances": [
    {
      "agent_id": "workbuddy",
      "instance_id": "workbuddy:default",
      "root": "C:\\Users\\<user>\\.workbuddy",
      "app_version": "<detected>",
      "schema_version": "<detected-migration>",
      "format": "workbuddy-session-jsonl",
      "format_version": 1,
      "backend": "sqlite+jsonl",
      "capabilities": {
        "list": true,
        "read": true,
        "write_bundle": true,
        "apply": "none"
      },
      "session_count": 3,
      "transcript_count": 2,
      "busy": true,
      "busy_reason": "WorkBuddy.exe is running",
      "confidence": 1.0,
      "warnings": ["1 session has no transcript file"]
    }
  ]
}
```

`busy` 检测：

- 当前实现解析 `<root>/sessions/*.json` 的 `lastHeartbeat` / `sessionId`，作为 advisory 信号；
- 进程枚举、`app/lockfile`、DB 独占锁检查属于后续 `--apply` 阶段；
- 目标正在运行时：
  - 读取仍可允许，但标记可能读到进行中的写入；
  - 默认拒绝 `--apply`；
  - 仍然可以生成 bundle。

### 6.4 能力矩阵

| Agent | 检测 | 读取 | 写 bundle | 直接 apply |
|---|---:|---:|---:|---:|
| aicli（原生 export/import） | 是 | 是 | 是 | 是（原生） |
| WorkBuddy `.workbuddy` | 是 | 是 | 是 | 否（bundle） |
| WorkBuddy AI `.workbuddy-ai` | 是 | 是 | 是 | 否（bundle） |
| DeepSeek Harness | 是 | 是（v4 JSONL / 官方 ZIP） | 是 | 否（bundle） |
| CodeBuddy CLI `.codebuddy` | 未实现 | 否 | 否 | 否 |
| Codex `.codex` | 未实现 | 否 | 否 | 否 |
| Claude Code `.claude` | 未实现 | 否 | 否 | 否 |
| Gemini CLI `.gemini` | 未实现 | 否 | 否 | 否 |
| Cursor `.cursor` | 未实现 | 否 | 否 | 否 |

当前版本只实现 DSH 与 WorkBuddy 两个适配器；其它 Agent 尚未探测。

DeepSeek Harness 的版本化 JSONL、官方 `/export` ZIP、surface fold 和 v4 写入约束见 [deepseek-harness-session-transfer.md](./deepseek-harness-session-transfer.md)。

### 6.5 非标准目录、便携版与未知格式

必须区分三类情况，不能混为一谈：

| 情况 | 示例 | 处理方式 |
|---|---|---|
| 已知 Agent，非标准根目录 | WorkBuddy 数据放在 `D:\PortableApps\WorkBuddy\data` | 显式 `--root` + 数据签名校验 |
| 已知 Agent，布局被改造 | `workbuddy.db` 和 `projects/` 被移到不同目录 | `--db` / `--projects-dir` 覆盖，仍走同一适配器 |
| 未知 Agent / 未知格式 | 目标只有自定义 SQLite、JSON 或二进制存储 | 只产出通用 bundle；要原生写入必须新增适配器 |

#### 6.5.1 已知 Agent 的任意根目录

未知路径不等于未知格式。只要能验证目标格式，就应支持任意绝对路径：

```powershell
aicli transfer detect --root D:\AgentData --json
aicli transfer list `
  --agent workbuddy `
  --root D:\PortableApps\WorkBuddy\data `
  --json
```

规则：

- 检测对象是**历史数据根**，不是程序安装目录；安装目录只有在有界探测发现 `data/`、`profile/` 等数据目录时才接受，否则提示用户指向真正的数据根；
- `--root` 必须是目录，或指向包含数据目录的父目录；
- 路径先 `Abs` + `Clean`；
- Windows 下实例身份使用大小写不敏感比较，但展示保留用户原始路径；
- 不把相对路径留到 adapter 内部解析，统一在 CLI 层转为绝对路径；
- root 必须通过内容签名，不能仅凭目录名认为适配成功。

WorkBuddy 还可以通过 `WORKBUDDY_HOME` / `WORKBUDDY_AI_HOME` 指定数据根；环境变量和 `--root` 冲突时以 `--root` 为准。

实例 ID 建议：

```text
<agent>:<short-hash(canonical-root)>
```

例如：

```text
workbuddy:8f3a91c2
workbuddy:ai
```

当路径等于默认根时使用稳定的 `workbuddy:default` / `workbuddy:ai`；非标准路径使用稳定 hash，避免把用户名或盘符写进 ID。`workbuddy-ai:default` 作为兼容别名仍可解析。

#### 6.5.2 持久化非标准根（后续）

> 当前版本只支持命令行 `--root` / `--db` / `--projects-dir`；下面的 roots
> 配置文件和 `transfer roots` 子命令尚未实现。

支持四种来源，优先级从高到低：

1. 命令行 `--root` / `--target-root`；
2. 环境变量 `AICLI_TRANSFER_ROOTS`；
3. `~/.aicli/transfer/roots.yaml`；
4. 内置默认根。

配置文件示例：

```yaml
version: 1
roots:
  - agent: workbuddy
    label: portable
    path: D:\PortableApps\WorkBuddy\data
  - agent: workbuddy
    label: archive
    path: E:\AgentBackups\WorkBuddy-2026-10
```

提供：

```text
aicli transfer roots list
aicli transfer roots add --agent workbuddy --path D:\PortableApps\WorkBuddy\data
aicli transfer roots remove <id>
```

配置文件只保存路径和标签，不保存凭证。`--root` 中显式给出的路径不应自动写回配置，除非用户执行 `roots add`。

#### 6.5.3 有界探测（后续）

> 当前 `detect` 只检查显式根和固定默认根；`transfer probe --depth` 尚未实现。

非标准根下可以寻找固定候选位置，但不能递归扫描整块磁盘。

建议顺序：

1. 精确候选：`<root>/workbuddy.db` + `<root>/projects`；
2. 已知包装目录：`<root>/data`、`<root>/profile`、`<root>/.workbuddy`、`<root>/.workbuddy-ai`；
3. 最多向下探测 2 层；
4. 找到多个候选时停止并列出，不自动选择；
5. 找到 0 个候选时返回 `unsupported`，并输出检查过的路径和缺失的 marker。

`aicli transfer probe` 只做检测，不导入、不写入：

```powershell
aicli transfer probe `
  --agent workbuddy `
  --root D:\PortableApps\WorkBuddy `
  --depth 2 `
  --json
```

输出至少包含：

```json
{
  "root": "D:\\PortableApps\\WorkBuddy",
  "root_source": "cli",
  "probe_depth": 2,
  "candidates": [
    {
      "path": "D:\\PortableApps\\WorkBuddy\\data",
      "layout": "workbuddy",
      "matched_markers": ["workbuddy.db", "projects", "sessions-table"],
      "confidence": 1.0
    }
  ],
  "selected": "D:\\PortableApps\\WorkBuddy\\data",
  "warnings": []
}
```

有多个候选时，`detect` 可以全部列出；`import` / `export` 必须要求用户显式选择，不能猜第一个。

#### 6.5.4 布局覆盖

对于已知 Agent 但目录结构被移动的情况，提供适配器特定的显式覆盖：

```powershell
aicli transfer list `
  --agent workbuddy `
  --db D:\AgentData\workbuddy.db `
  --projects-dir D:\AgentData\projects `
  --json
```

规则：

- 显式覆盖优先于自动发现；
- 覆盖后仍必须验证 DB 签名和 transcript 目录；
- 不允许“只找到 DB 就假装成功”；
- 不允许写入未通过签名校验的显式路径；
- 目标 root 不存在、或没有已初始化的目标数据库时，默认拒绝 `--apply`，只生成 bundle；不自动初始化目标应用的事务型数据库。便携版可提示用户先启动一次目标应用生成数据根；
- 后续适配器可以增加 `--adapter-opt key=value`，但必须由适配器声明支持哪些 key，并拒绝未知 key。

#### 6.5.5 未知格式的处理（后续）

> 当前尚未实现 `--to portable`。未知 Agent 请先用该 Agent 自己的导出入口，
> 或新增适配器；不要把任意文件当成本项目支持的原生格式。

如果目标 Agent 的存储格式未知：

- **不做原生 `--apply`**；
- 只能导出通用 bundle：`--to portable`；
- bundle 包含 manifest、canonical session、可读 Markdown/JSON 和可选 assets；
- 明确告诉用户“这是可迁移包，不是目标 Agent 的原生历史”；
- 如果目标 Agent 自己支持导入，用户可再用它的官方 UI/CLI 导入。

通用 bundle 的目的不是伪装成目标格式，而是保证：

- 信息不丢失；
- 可人工审阅；
- 后续适配器可复用；
- 不会被误认为已经写入目标。

要长期支持未知 Agent，必须新增适配器。优先级：

1. Go 内置 adapter（最安全、可测试）；
2. 声明式 adapter manifest（只描述路径、格式和字段映射）；
3. 外部命令 adapter（高风险，必须显式 trust、timeout、dry-run、输出校验，默认不做）。

#### 6.5.6 路径安全

- 写入路径必须位于解析后的实例 root 内；
- 防止 `..`、符号链接、junction 逃逸；
- UNC/网络盘只做警告，默认不允许 `--apply`；
- 读取允许用户显式指定，但 Web/远程 API 不能接受任意路径；
- 所有路径校验使用文件系统 API，不通过 shell 拼接命令。

---

## 7. WorkBuddy 适配器详细设计

> 实现状态、代码目录和真实命令见 [workbuddy-session-transfer.md](./workbuddy-session-transfer.md)。

### 7.1 存储布局

```text
<root>/
  workbuddy.db
  projects/
    <project-slug>/
      <session-id>.jsonl
      <session-id>.meta.json        // 可选；实际存在性随版本变化
      <session-id>/
        tool-results/
          <call-id>-render.txt
```

会话索引和正文分离：

- `workbuddy.db` 负责列表、标题、cwd、用户、状态和时间；
- JSONL 负责消息、推理、工具调用与结果；
- 不能只写 DB，也不能只写 JSONL 就假定 UI 一定能看到。
- 非标准布局使用 `--db` / `--projects-dir` 显式覆盖，见 §6.5；覆盖后仍必须通过 DB 签名和 transcript 目录校验。

### 7.2 事件模型

最小消息事件形态：

```json
{
  "type": "message",
  "id": "019...uuid",
  "parentId": "019...parent",
  "sessionId": "019...session",
  "timestamp": 1793247677414,
  "role": "user",
  "content": [{"type": "input_text", "text": "..."}],
  "providerData": {"startsNewUserRequest": true, "agent": "cli"},
  "cwd": "C:/workspace"
}
```

工具调用：

```json
{
  "type": "function_call",
  "id": "019...event",
  "parentId": "019...previous",
  "sessionId": "019...session",
  "timestamp": 1793247678000,
  "callId": "call_...",
  "name": "Bash",
  "arguments": "{\"command\":\"...\"}",
  "providerData": {"messageId": "019...step", "stepSeq": 3},
  "cwd": "C:/workspace"
}
```

工具结果：

```json
{
  "type": "function_call_result",
  "id": "019...event",
  "parentId": "019...call-event",
  "sessionId": "019...session",
  "timestamp": 1793247679000,
  "callId": "call_...",
  "name": "Bash",
  "status": "completed",
  "output": {"type": "text", "text": "..."},
  "cwd": "C:/workspace"
}
```

关键字段：

- `id`：事件 ID；生成时应保证唯一；
- `parentId`：事件链父节点；
- `providerData.messageId` / `stepSeq`：同一 assistant step 的分组依据；
- `callId`：工具调用与结果的配对键；
- `resend-fork-notice`：用户编辑/重发时的分支跳转提示。

### 7.3 active branch 重建

不能按 JSONL 行顺序直接导入。WorkBuddy 的文件保留被编辑掉的旧分支。

当前实现（`workbuddy/reader.go`）使用事件 `parentId` 建立树，然后对每个
`resend-fork-notice.editedUserItemId` 标记该 user 节点及其整个子树为
inactive：

```text
children[parentId] += id
for notice in resend-fork-notice:
    remove_subtree(notice.editedUserItemId)

surface:    过滤 remove 集合，保留 fork 点之前的公共历史 + 新分支
transcript: 不删除旧分支，保留 append-origin 全量记录并 warning
```

这与“从尾部回溯单链”的结果在常见编辑/重发场景等价，但实现更直接，也能
保留 fork 点之前的公共历史。以下情况仍需后续加强：

- 没有 `resend-fork-notice` 但存在多条叶分支；
- 缺少 `parentId` 的旧文件；
- 重复 ID / 循环 parent；
- 最后一行被截断。

必须测试：

- 普通 user/assistant 链；
- function_call/function_call_result 链；
- `resend-fork-notice` 替换旧分支；
- 缺失 parentId；
- 最后一行被截断；
- 重复 ID / 循环 parent；
- CRLF、BOM、空行。

### 7.4 导入映射

| WorkBuddy | aicli |
|---|---|
| `message role=user` | `types.Message{Role:"user"}` |
| `message role=assistant` | `types.Message{Role:"assistant"}` |
| `reasoning` | `types.SetReasoningBlock()`，`Provider="workbuddy"` |
| `function_call` | assistant 的 `ToolCalls` |
| `function_call_result` | `types.Message{Role:"tool", ToolCallID:callId}` |
| `ai-title` | `Session.Metadata.Title` |
| `file-history-snapshot` | 不导入，产生 warning |
| `session-meta` | 不导入为消息 |
| `resend-fork-notice` | 只参与分支重建 |

当前实现优先按 `providerData.messageId`、否则按 `parentId` 把 `function_call`
聚合到对应 assistant message；`callId` 用于和 `function_call_result` 配对。
收到的 reasoning 先缓冲，
再附加到下一条 assistant 消息：

1. 收集 `providerData.messageId` 匹配或 `parentId == assistant.id` 的 `function_call`；
2. 生成一个 assistant 消息，包含正文、reasoning metadata 和 `ToolCalls`；
3. 按 `callId` 配对 `function_call_result`，生成紧随其后的 tool 消息；
4. 缺失结果、孤立结果、非法 arguments 产生 warning，不静默丢弃。

`providerData.messageId` / `stepSeq` 已保留到 metadata，但首版没有用它
做更复杂的多 assistant step 合并。

### 7.5 用户消息清洗

WorkBuddy 的 user message 常包含注入上下文：

```text
<system-reminder data-role="user-context">
  <user_info>...</user_info>
  <identity_context>...</identity_context>
  <user_query>真正的用户输入</user_query>
</system-reminder>
```

如果原样导入 aicli，下一轮模型会把 WorkBuddy 的 identity、SOUL/USER/BOOTSTRAP、项目树和 system reminder 当成用户输入，造成：

- 上下文膨胀；
- 身份提示污染；
- prompt injection 风险；
- 重复系统提醒。

默认策略：

- 存在 `<user_query>...</user_query>` 时，只取其中文本作为 aicli 用户内容；
- 当前版本不保存原始完整 user context，避免把 SOUL/USER/项目树写入目标；
- `--raw-user-context` 是后续能力，尚未实现；
- 任何时候都不把 WorkBuddy 的 system-reminder 转成 aicli 的 `system` 消息；
- system/developer 消息不降级为 user 消息。

### 7.6 标题、cwd、模型和时间

优先级：

- 标题：`custom_title` > `sessions.title` > `ai-title` > 首条用户消息摘要；
- cwd：`sessions.cwd` > JSONL 中首个非空事件的 `cwd`；
- 模型：最后一个 assistant 的 `providerData.model`（DB `sessions.model` 目前只用于列表/索引提示）；
- 创建时间：首个事件时间（DB `created_at` 目前不传入 reader）；
- 更新时间：最后事件时间；列表使用 `last_activity_at` / `updated_at` 排序；
- 状态：所有源状态都映射为 aicli `active`；源状态仅保留在列表/索引，不伪造 archived/deleted 副作用。

### 7.7 导入 aicli 的归属与溯源

使用现有 import 规则：

- 默认使用 `--user`；
- 未指定时回退到当前 aicli 默认用户；
- 不沿用 WorkBuddy 的 `user_id` 作为 aicli 用户；
- 写入 `metadata.context`：
  - `source_agent = workbuddy`
  - `source_path`
  - `source_session_id`
  - `source_format = workbuddy-session-jsonl`
  - `source_format_version = 1`
  - `source_view = surface|transcript`
  - `source_cwd`
  - `workspace_path`
  - `source_event_count`
- 保留源 session id 仅用于追溯，不作为 aicli 主键。

### 7.8 WorkBuddy 路径 slug

WorkBuddy 的 project 目录名规则可从其实现中归纳为：

```text
replace [/\\:] with "-"
trim leading/trailing "-"
drive 后的第一个 separator 与 drive 冒号合并为一个 "-"
```

示例：

```text
C:\Users\<user>\WorkBuddy AI\ai-agent-runtime
→ c-Users-<user>-WorkBuddy AI-ai-agent-runtime
```

超长路径需要按 WorkBuddy 的 255 字节限制策略处理，或直接报错要求 `--cwd`。

当前 export 直接按规则计算 bundle 目录；未来 `--apply` 应优先查找目标已有
project 目录，找不到再按规则创建，避免路径规则随版本变化导致重复项目。

### 7.9 生成 WorkBuddy JSONL

从 aicli 消息生成：

```text
user message
  -> message role=user, content=[{type:"input_text", text}]

assistant content + reasoning + tool_calls
  -> reasoning event（若有）
  -> message role=assistant, content=[{type:"output_text", text}]（有正文时）
  -> function_call event（每个 call）
  -> function_call_result event（对应 tool 消息）

tool message
  -> function_call_result
```

规则：

- 新 session ID 当前使用 UUID v4（`github.com/google/uuid`）；UUIDv7 是后续可选升级；
- 每个事件 ID 使用新的 UUID v4；
- `parentId` 指向链上前一个事件；
- custom 事件（session-meta/ai-title）不参与主链；
- 当前 bundle 末尾写 `ai-title`；如果未来直接 append 到目标文件，需要重新评估 tip 语义；
- `system` / `developer` 默认跳过并 warning；
- 工具调用参数保留原始 JSON；无法解析时保留 raw string 并 warning；
- 大 tool result 的外部化（`tool-results/`）是后续能力；当前 bundle 保留 JSONL 文本，不做静默截断。

### 7.10 生成 WorkBuddy 索引行（后续 apply）

当前 bundle 只生成 `session-index.json` 作为合并提示，不写目标 `workbuddy.db`。
未来 `--apply` 时目标 `sessions` 至少写入：

```text
id              新 UUID
cwd             目标工作目录（使用目标路径风格）
user_id         目标 WorkBuddy 用户
title           源标题
custom_title    可选
status          completed
created_at      源创建时间或当前时间
updated_at      当前时间
last_activity_at 当前时间
is_playground   0
source_mode     craft
mode            craft
transport       local
unread          0
```

建议同时 upsert `workspaces(path, last_opened_at)`；`session_usage` 可选。

安全约束：

- 不继承 aicli 的 `permission_mode`；
- 不继承 MCP、workspace trust、provider 凭证；
- 不写入源 `file-history-snapshot`；
- 不默认写入 subagent/team 会话。

### 7.11 写入流程与原子性（后续 apply）

`--apply` 流程：

```text
1. probe target instance / version / schema
2. 检查目标进程和 DB 锁
3. 检查目标 session ID 冲突
4. 备份 workbuddy.db + -wal + -shm + 目标 JSONL
5. 写 JSONL 临时文件 -> fsync -> rename
6. SQLite 事务写 sessions / workspaces
7. 只读打开 DB 并重新解析 JSONL 验证
8. 失败时恢复备份并清理新文件
9. 追加 transfer audit
```

任何一步失败都不能返回成功。目标应用正在运行时返回 `target_busy`，不尝试写入。

### 7.12 已知限制

- WorkBuddy 是私有存储格式，升级可能改变字段、路径或读取逻辑；
- 大工具输出需要后续实现 `tool-results` 外部化；
- 图片/附件暂不支持完整迁移；
- 文件历史、快照、subagent/team 不在首版范围；
- 直接写入不能替代 WorkBuddy 自己的数据库迁移和校验；
- WorkBuddy 运行时的内存状态可能覆盖外部写入，因此必须关闭应用。

---

## 8. 安全与隐私

跨 Agent 导出本质上是数据边界迁移，必须显式处理以下风险。

### 8.1 不迁移的敏感数据

- provider API key、OAuth token、auth 文件；
- MCP secrets 和 server 凭证；
- workspace trust 决策；
- permission 设置、自动批准规则；
- memory / identity / SOUL / USER / BOOTSTRAP 文件；
- 文件历史、file-history-snapshot；
- debug 日志、HTTP trace、crash dump；
- 未显式要求的 subagent/team transcript。

### 8.2 用户上下文清洗

WorkBuddy 会把系统提醒注入为 user message；导入 aicli 时默认提取 `<user_query>`。否则历史会话会在下一轮变成新的 prompt injection 入口。`--raw-user-context` 目前尚未实现。

建议在 `Warning` 中明确记录：

- `user_context_stripped`
- `system_reminder_ignored`
- `foreign_identity_context_ignored`

### 8.3 工具输出

工具输出可能包含：

- 源码和私有路径；
- shell 环境变量；
- token、cookie；
- 大体积日志。

提供：

- `--max-tool-output <bytes>`；
- `--redact-secrets`；
- `--strip-tool-output`（只保留工具调用元数据）。

默认不自动篡改内容，但必须显示体积和风险 warning。

### 8.4 路径和身份重映射

- `--cwd` 覆盖目标工作目录；
- `--map-cwd old=new` 支持批量迁移到不同机器；
- WorkBuddy 的 `user_id` 与 aicli 的 `user_id` 是两个身份平面，不能互相覆盖；
- 导出时使用 `--target-user`，导入时使用 `--user`。

### 8.5 审计

每次 `--apply` 写入 `~/.aicli/transfer/audit.jsonl`，至少记录：

```json
{
  "at": "2026-10-29T13:00:00+08:00",
  "operation": "export",
  "source_agent": "aicli",
  "source_session_id": "session_...",
  "target_agent": "workbuddy",
  "target_instance": "workbuddy-ai:default",
  "target_session_id": "019...",
  "backup_dir": "...",
  "warnings": []
}
```

不记录完整消息正文，不记录凭证。

### 8.6 Web / 远程 API

如果后续把 transfer 暴露到 runtime-server：

- 默认只允许 loopback；
- 写入操作要求 token；
- 禁止把「读取任意本机 Agent 数据根」做成无鉴权的远程能力；
- 不允许通过 HTTP 指定任意 `C:\` 根目录而不做路径约束。

---

## 9. 实施阶段

### Phase 1：检测 + WorkBuddy 只读导入（已完成）

交付：

- `aicli transfer detect`
- `aicli transfer list --agent workbuddy`
- `aicli transfer import --from workbuddy`
- 两个 WorkBuddy 实例的 Probe
- active branch 重建
- user context 清洗
- tool call / result 映射
- 复用现有 aicli import 归一化与存储

未包含在本阶段的内容：`transfer probe`、`transfer roots` 配置文件、
CodeBuddy/Codex/Claude 检测。

退出标准：

- 检测能区分 `.workbuddy` 和 `.workbuddy-ai`；
- 能识别「DB 有会话但 transcript 缺失」；
- 带 `resend-fork-notice` 的会话只导入 active branch；
- 工具调用与结果正确配对；
- 导入后 aicli 可 resume，消息数量和 canonical count 一致；
- 默认不覆盖已有 aicli 会话。

### Phase 2：WorkBuddy bundle 导出（已完成）

交付：

- `aicli transfer export --to workbuddy`
- manifest、JSONL、DB plan
- 路径 slug、UUID、事件链生成
- 默认不写目标数据根

`--to portable` 尚未实现。

退出标准：

- 生成的 bundle 能被本项目的 WorkBuddy reader 重新解析；
- 文本、标题、cwd、工具调用/结果 roundtrip 一致；
- 目标应用运行时仍可生成 bundle，但不会写目标；
- bundle 内不包含凭证和权限设置。

### Phase 3：WorkBuddy `--apply` 实验写入

交付：

- 目标进程检查
- schema/version gate
- 备份、临时文件、SQLite 事务
- 写后读回验证
- 失败恢复
- 文档标注实验性

退出标准：

- 目标运行中必定拒绝；
- 目标关闭时写入后 WorkBuddy 能列出并打开会话；
- 写入后重启目标仍可读取；
- 失败注入测试不会留下半写会话；
- 备份可以恢复。

### Phase 4：其它 Agent

建议顺序：

1. Codex（先只读识别和 session list）
2. CodeBuddy CLI（若与 WorkBuddy JSONL 格式同源，复用 reader）
3. Claude Code
4. Gemini CLI

Codex / Claude / Gemini 的第一阶段只提供 `detect` 与 `list`，不应提前承诺 `import` / `export`。

---

## 10. 测试与验收

### 10.1 单元测试

- 检测签名：目录存在但无历史时不报告可读；
- 两个 WorkBuddy 实例独立；
- path slug 生成；
- UUID / event parent 链（当前 UUID v4）；
- `resend-fork-notice` 分支选择；
- function call/result 配对；
- 非法 arguments；
- 缺失 parentId；
- 截断尾部 JSONL；
- CRLF/BOM/空行；
- 用户 context 清洗；
- ID 冲突和 `--new-id`。

### 10.2 Golden fixtures

在 `testdata` 中放人工构造、已脱敏的 WorkBuddy JSONL：

- 普通双轮会话；
- 工具调用 + 结果；
- 用户编辑/重发分支；
- 大工具输出；
- 孤立工具结果；
- 损坏的末行。

不要把真实用户会话直接作为 fixture 提交。

### 10.3 集成测试

- 用临时目录模拟 `.workbuddy`：
  - 创建 `workbuddy.db`
  - 创建 `projects/<slug>/<id>.jsonl`
  - 运行 `transfer list/import`
- 用临时 aicli session dir 验证写库和读回；
- bundle 导出后再用 reader 读回；
- `--apply`（后续）在目标进程不存在时写入临时目标根；
- 目标进程存在时返回 `target_busy`（后续）。

### 10.4 性能测试

- 100 MB 级 JSONL 不一次性加载全部消息；
- 两遍解析只解析 active branch；
- 大量 tool calls 不导致 O(n²) 配对；
- 写入大 bundle 时使用流式编码。

### 10.5 安全测试

- source user context 不被导入为 aicli system prompt；
- provider token / MCP secret 不出现在 bundle；
- permission mode 不被继承；
- 远程 HTTP 路径不能读取任意目录；
- audit 不含正文和凭证。

---

## 11. 风险与开放问题

| 风险 | 影响 | 处理 |
|---|---|---|
| WorkBuddy 私有格式升级 | 写入失败或 UI 不识别 | 版本门控；默认 bundle；`--apply` 后续 |
| JSONL 分支语义变化 | 导入重复旧分支 | 复现 active branch 算法并加 fixtures |
| DB 与 JSONL 不一致 | 列表有会话但无法打开 | detect 交叉校验；缺失 transcript 不生成空会话 |
| 目标应用运行中 | 数据竞争、覆盖、损坏 | 拒绝 apply；只允许 bundle |
| 大工具输出 | 截断或内存压力 | 两遍解析、上限、后续 tool-results 外部化 |
| 图片/附件 | 迁移不完整 | 首版文本优先；明确 warning |
| subagent/team | 历史不完整 | 首版明确非目标；后续单独设计 |
| 非标准目录/未知格式 | 误判或无法写入 | 显式 root、布局覆盖、portable bundle；未知格式不 apply |
| 身份/系统提示污染 | prompt injection | 默认清洗；原始上下文保留为后续能力 |
| 目标用户推断失败 | 无法列出或写入 | 要求 `--target-user` |
| 官方 API 出现 | 当前私有写入方案过时 | 适配器优先探测官方入口 |

需要后续确认的问题：

1. WorkBuddy 5.8.x 之后是否会出现官方 session import/export API。
2. WorkBuddy 的 `session-meta` / `ai-title` / `.meta.json` 在新版本中的必要性。
3. 大工具输出是否应使用 `tool-results` 外部化，还是写 `<persisted-output>` 指针。
4. 是否支持图片 content parts；需要新的脱敏 fixture。
5. 是否要把 subagent/team 作为独立 transfer 对象。
6. WorkBuddy 之外的 Agent 是否值得做成正式适配器，取决于真实用户需求和使用频率。

---

## 12. 参考

### aicli 代码

- `backend/cmd/aicli/commands/export.go`
- `backend/cmd/aicli/commands/import.go`
- `backend/cmd/aicli/commands/chat_export_command.go`
- `backend/internal/chat/sqlite_storage.go`
- `backend/internal/chat/session.go`
- `backend/internal/types/message.go`
- `docs/aicli/session-export-import.md`

### WorkBuddy 默认数据根（排障参考）

```text
%USERPROFILE%\.workbuddy
%USERPROFILE%\.workbuddy-ai
```

### 相关文档

- [session-export-import.md](./session-export-import.md)
- [deepseek-harness-session-transfer.md](./deepseek-harness-session-transfer.md)
- [agents.md](./agents.md)
- [profiles.md](./profiles.md)
