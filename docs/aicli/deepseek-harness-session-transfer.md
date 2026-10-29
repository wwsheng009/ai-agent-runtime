# DeepSeek Harness 会话格式兼容方案

> 状态：Phase 1–3 已实现（检测、官方 ZIP/原始 v4 导入、v4 bundle 导出；`--apply` 未实现）
>
> 日期：2026-10-29
>
> 范围：`E:\projects\ai\deepseek-harness` 的 Session JSONL / 官方导出包与 `aicli transfer` 的互操作
>
> 关联文档：[cross-agent-session-transfer.md](./cross-agent-session-transfer.md)

## 1. 结论

DeepSeek Harness（DSH）与 WorkBuddy 不同：它有显式版本化的 Session 格式、历史迁移链和官方导出能力，因此兼容性更好，但写入限制也更严格。

推荐策略：

1. **DSH → aicli 优先走官方 `/export` ZIP**。该 ZIP 中的日志已经由 DSH 当前版本规范化为 `session.v4.jsonl`，不需要 aicli 复现 v0→v4 迁移，也不需要直接处理 zstd。
2. **直接读取原始 DSH 数据根作为备选**：只支持当前 v4；读取 `session.v4.jsonl` 或 `session.v4.jsonl.zstd`，解压多帧 zstd，再按 surface 语义重建历史。
3. **aicli → DSH 生成 v4 原生 JSONL bundle**。DSH 当前没有可依赖的 import 命令或 API，因此原生写入只能作为版本门控的实验性 `--apply`。
4. **不把 DSH SQLite persistence 当作首版目标**。本机数据与默认 profile 使用 JSONL；SQLite backend 需要单独适配。
5. **不能按 JSONL 行顺序直接导入**。DSH 的 `surfaceOp: replace` 和 `sourceEventSeqs` 会隐藏被 compaction 替换的旧消息；必须实现 surface fold，或使用官方导出后的当前逻辑日志。

### 1.1 当前实现状态

代码：

```text
backend/internal/sessionport/dsh/
  types.go        header/event/message/import 类型
  paths.go        DSH_HOME、sessions、zstd、session 文件命名
  reader.go       扫描、header 校验、ZIP 根日志读取
  detect.go       Detect / DetectInstance / List
  surface.go      surface append/replace、transcript
  convert.go      DSH -> runtimechat.Session
  export.go       runtimechat.Session -> v4 JSONL/zstd bundle
  zip.go          官方 /export ZIP 根会话候选扫描
```

CLI：

```powershell
aicli transfer detect --agent deepseek-harness --json
aicli transfer detect --agent deepseek-harness --no-interactive
aicli transfer inspect --agent deepseek-harness --json
aicli transfer inspect --zip .\dsh-session-xxx.zip --json
aicli transfer list --agent deepseek-harness --instance deepseek-harness:default --json
aicli transfer import --from deepseek-harness --zip .\dsh-session-xxx.zip --view surface
aicli transfer import --zip .\dsh-session-xxx.zip --interactive
aicli transfer export --interactive
aicli transfer export --to deepseek-harness --session <aicli-id> --bundle-dir .\dsh-export --compression zstd
```

已实现：

- 当前 v4 原始 JSONL / `.jsonl.zstd`（多帧）读取；
- 官方 `/export` ZIP 根日志读取，子会话只给 warning；
- 官方 ZIP 候选预检：`transfer inspect --zip` 可列出根会话、标题、版本和 warning；
- `transfer import --zip --interactive` 可在 ZIP 内选择根会话后导入；
- `transfer export --interactive` 可选择 aicli 源会话、DSH 压缩方式、输出目录和 cwd；
- `surface` / `transcript` 两种视图；
- assistant text/reasoning/tool-call 到 aicli 的映射；
- `tool/call` 与 `tool/result` 的交叉说明和配对校验；
- aicli -> v4 bundle（plain / zstd）；
- DSH/WorkBuddy 共用的 `registry` 分派；
- 真实会话抽样和单元测试。

未实现：

- 写 `$DSH_HOME` / `workspace.json` 的 `--apply`；
- DSH SQLite persistence backend；
- v0–v3 的 Go 侧迁移；
- subagent 子树与附件完整迁移；
- 用 DSH 官方 validator 验证生成的 bundle（当前只做本项目 round-trip）。

---

## 2. DSH 格式事实

### 2.1 数据根与会话目录

默认：

```text
DSH_HOME = %USERPROFILE%\.dsh   (Windows)
            $HOME/.dsh          (Unix)
```

会话根：

```text
$DSH_HOME/sessions/
  --<sanitized-cwd>--/
    <encoded-session-id>/
      session.v4.jsonl.zstd
      session.v4.jsonl            # compression=none 时
      session.lock                # 写入租约，Windows 下可能没有文件足迹
```

本机实际抽样确认：

- `sessions` 下存在 `--E-projects-ai-ai-agent-runtime--` 等 project 目录；
- 每个 session 目录下是 `session.v4.jsonl.zstd`；
- zstd 文件是**多个独立 frame 的拼接**，第一帧包含 header，后续帧包含事件批次。

Go 实现已引入 `github.com/klauspost/compress/zstd`，并验证了多帧拼接、checksum 和损坏尾帧行为。

### 2.2 当前版本

- `SESSION_FORMAT_VERSION = 4`；
- `docs/session-format-status.md` 记录当前 finalized/released 均为 v4；
- 仓库提供 v0→v1→v2→v3→v4 的相邻迁移包；
- 新写入应只生成 v4。

### 2.3 Header

第一行是物理 header：

```json
{"type":"session","version":4,"id":"session-...","createdAt":1790996205221,"cwd":"E:\\projects\\ai\\ai-agent-runtime","isSeeded":false,"delegationDepth":0,"agentPreset":"standard"}
```

约束：

- `version`、`id`、`createdAt`、`isSeeded`、`delegationDepth` 必填；
- `cwd` 若存在必须是绝对路径；
- `origin` 只允许 `subagent`；
- `parentSession`、`agentPreset` 可选；
- 不接受任意额外 header 字段。

### 2.4 事件 envelope

```json
{"type":"user/message","seq":17,"time":1790996250505,"data":{...},"surfaceOp":"append"}
```

规则：

- `seq` 从 0 开始必须稠密连续；
- `time` 是 epoch milliseconds；
- `data` 必须是 lossless JSON；
- 可选 `surfaceOp`、`sourceEventSeqs`、`ignorable`；
- 所有 surface 事件都必须有 `surfaceOp`；
- 未知 required 事件必须拒绝，未知 `ignorable: true` 事件可以保留但不进入模型历史。

### 2.5 Surface 事件与 log-only 事件

会产生模型历史的五类 surface 事件：

```text
system/message
developer/message
user/message
assistant/message
tool/result
```

其中：

- `user/message` 的 `data` 就是消息本体；
- 其它四类的事件本体在 `data.message`；
- `assistant/message` 还可带 `usage`、`stream`、`interrupted`；
- `tool/call` 是 log-only，记录工具调用开始；工具结果由 `tool/result` surface 事件承载；
- `turn/start`、`turn/end`、`step/start`、`step/end`、`request/header`、`request/context`、`model/selection`、`command/*`、`permission/*`、`compaction/*` 等是 log-only 或辅助事件。

### 2.6 当前 surface 与人类 transcript 不是一回事

`surfaceOp` 有两种：

```text
append
{ "op": "replace", "startSeq": N, "endSeq": M }
```

`replace` 用当前事件替换当前 surface 中的一段节点，用于 compaction / prune / 某些重写。被替换的节点仍保留在 JSONL 中，但不再属于当前模型可见历史。

DSH 区分：

- **当前 surface**：模型下一轮看到的历史；
- **append-origin transcript**：用户实际看过的人类可读历史，replacement 副本通常不应替代原消息。

导入 aicli 时必须允许用户选择：

```text
--view surface      默认；保证 resume 时模型上下文一致
--view transcript   归档/阅读；保留 append-origin 历史，跳过 replacement 副本
```

不能只按文件行序导出所有 surface 事件，否则会把被 compaction 隐藏的历史重新带回来，或重复导入 checkpoint。

### 2.7 官方导出

DSH 的 Web bundle 挂载 `@deepseek-ai/dsh-session-log-export`：

- `/export` 下载 `dsh-session-<id>.zip`；
- 导出入口属于 Web bundle，DSH CLI 当前没有等价的 export 子命令；
- ZIP 内是**当前 v4 逻辑日志**，不是原始 zstd 帧；
- 根会话日志：`session.v4.jsonl`；
- 子会话：`subagents/<id>/session.v4.jsonl`；
- 图片：`media/<attachmentId>.<ext>`；
- 文件：`files/<digest-prefix>/<digest>/<name>`；
- 没有 manifest；
- 导出前会 flush 正在运行的会话；
- 目前没有对应的 import 命令。

因此官方 ZIP 是 DSH → aicli 的首选输入，因为它把格式迁移、压缩和逻辑规范化都交给 DSH 自己处理。

### 2.8 Workspace 元数据与 projection cache

- `$DSH_HOME/storages/workspace.json` 保存 workspace 与 `sessionIds`，是 Web/工作区导航的元数据；
- `$DSH_HOME/storages/session_projcache/sessions/<id>.json` 是 projection cache；
- projection cache 不是日志权威，可以删除/重建；
- 只写 JSONL 后，CLI 可以通过 session id 打开，但 Web 工作区列表未必自动出现；需要刷新/重建 workspace 元数据，或在目标关闭时安全更新 `workspace.json`。

---

## 3. 兼容策略

### 3.1 DSH → aicli

输入优先级：

1. **官方 `/export` ZIP**（推荐）：
   - 不处理 zstd；
   - 不实现 v0–v3 迁移；
   - 直接读 `session.v4.jsonl`；
   - 可同时获得 attachments 和 subagent 目录。
2. **原始 v4 JSONL**：
   - 支持 `.jsonl` 和 `.jsonl.zstd`；
   - 只接受 v4；
   - v0–v3 返回 `migration-required`，提示用官方导出或 DSH 自己的迁移流程。
3. **v0–v3 原始文件**：
   - 首版不做 Go 侧重实现；
   - 可通过可选的 DSH Node helper 调用 `@deepseek-ai/dsh-session-format-catalog`，或要求用户从 DSH Web 导出当前 ZIP。

导入算法：

```text
读取 header
  -> 校验 version == 4
  -> 逐事件验证 seq/time/envelope
  -> 构建 surface fold：
       append:  tail 追加 seq
       replace: 在当前 surface 中定位 startSeq/endSeq
                 用当前事件 seq 替换这一段
                 校验 sourceEventSeqs 覆盖被删除节点
  -> 根据 view 选择：
       surface   -> 使用 fold 后 nodes
       transcript -> 使用 append-origin surface 事件，跳过 replacement 副本
  -> 按 surface/transcript 顺序映射 aicli 消息
  -> 写入 aicli Session，保留 source event seq 到 metadata
```

### 3.2 DSH 事件到 aicli 的映射

| DSH | aicli |
|---|---|
| `user/message` | `types.Message{Role:"user"}` |
| `assistant/message` 中的 `text` block | assistant `Content` |
| `assistant/message` 中的 `reasoning` block | `types.SetReasoningBlock()` |
| `assistant/message` 中的 `tool-call` block | assistant `ToolCalls` |
| `tool/call` | 仅用于交叉校验；不要重复生成 tool call |
| `tool/result` | `types.Message{Role:"tool", ToolCallID: toolCallId}` |
| `system/message` | 默认跳过；不把 DSH system prompt 注入 aicli |
| `developer/message` | 默认跳过；tool-addition/removal 不迁移 |
| `compaction/*` | 仅参与 surface fold；不生成消息 |
| `session/title` | 标题 |
| `agent/inbox/spliced` | 不导入；它是 log-only 输入队列记录 |
| `session/end-seed` | 顶层导入时跳过；fork lineage 预留 |
| `assistant/attempt` | 不导入；失败尝试仅做 warning |

注意：

- `assistant/message` 已经包含 `tool-call` block，`tool/call` 只是生命周期记录；导入时不能同时按两者各生成一次 tool call。
- `tool/result` 的 `data.message.source.callId`、`toolCallId` 必须与该 assistant tool-call 的 id 对齐。
- `tool/result` 的 `isError` 保留到 aicli 的 metadata 或错误标记。
- `system/message` 默认跳过，避免把 DSH 的完整系统提示带进 aicli 并重复注入；可提供 `--include-system`，但必须标记为“导入的 system 消息”，而不是 aicli 当前系统提示。

### 3.3 DSH → aicli 的字段与归属

- `cwd` 写入 `metadata.context.workspace_path`；
- `createdAt` 优先使用 header，事件时间只做 fallback；
- `parentSession` / `delegationDepth` / `origin` / `agentPreset` 放入 source metadata，不改变 aicli 的权限和 agent 配置；
- `source_session_id`、`source_event_seq`、`source_view` 写入 metadata；
- 标题取最新 `session/title`，没有则用首条用户消息摘要；
- 子会话默认不导入；`--include-subagents` 后续实现；
- 附件：官方 ZIP 的 `media/` / `files/` 可复制到 aicli session-artifacts；MVP 文本优先，图片/文件完整支持放 Phase 2。

### 3.4 aicli → DSH

DSH 没有官方 import，因此有两条输出路径：

1. **bundle（默认）**：
   - 生成 `session.v4.jsonl` / `.jsonl.zstd`；
   - 生成 header、事件链、manifest 和目标路径计划；
   - 不写 `$DSH_HOME`。
2. **`--apply`（实验性）**：
   - 要求 DSH 进程已关闭；
   - 校验目标 JSONL backend 与 compression；
   - 写入正确 project/session 目录；
   - 备份并原子发布；
   - 可选更新 `workspace.json` 的 `sessionIds`；
   - 写入后由 DSH 自己重新建立 projection cache。

DSH 的 `sessionPersistence` 抽象是 create/open/stat/list，没有 export/import 语义；原生写入必须绕过服务层直接生成物理格式，因此是私有契约。

### 3.5 生成 v4 原生日志

目标 header：

```json
{"type":"session","version":4,"id":"session-<uuid>","createdAt":179...,"cwd":"E:\\...","isSeeded":false,"delegationDepth":0,"agentPreset":"standard"}
```

每个用户回合至少生成：

```text
turn/start
user/message (surfaceOp=append)
step/start
assistant/message (surfaceOp=append)
tool/call (若有工具调用)
tool/result (surfaceOp=append，每个结果)
step/end
turn/end {reason:{kind:"completed"}}
```

`assistant/message` 需要同时提供：

- `message`：`id`、`role:"assistant"`、`content`（text/reasoning/tool-call blocks）、`source:{kind:"model",provider,model}`；
- `usage`：可选；
- `stream`：至少是数组；建议从 content 生成紧凑的 `block-start`、`text-chunks` / `reasoning-chunks` / `tool-call-chunks`、`block-end`、`finish` 记录；
- `surfaceOp:"append"`；
- 不能带 `sourceEventSeqs`（assistant/message 禁止）。

工具调用必须满足：

- assistant content 中有 `tool-call` block；
- 同一 step 有对应 `tool/call` log-only 事件；
- `tool/result` 的 `message.toolCallId`、`source.callId` 与 block id 一致；
- 没有结果的工具链默认拒绝或按 DSH repair 语义生成错误结果，不能静默留下 dangling call。

### 3.6 目标路径与压缩

```text
$DSH_HOME/sessions/
  --<projectKey(cwd)>--/
    <encodeSegment(sessionId)>/
      session.v4.jsonl.zstd   // compression zstd
      session.v4.jsonl        // compression none
```

`projectKey` 规则：

```text
separator run -> "-"
非安全字符 -> "~XXXX"
trim leading "-"
trim to 251 bytes
prefix "--", suffix "--"
```

`encodeSegment` 对 session id 做 `~XXXX` 转义，禁止路径穿越。

压缩：

- 目标 profile 使用 `compression: zstd` 时，生成 checksummed zstd 帧；
- 首帧必须包含 header，后续帧可包含事件批次；
- 目标 profile 使用 `compression: none` 时，生成普通 JSONL；
- 不允许在同一个 session 目录混用两种压缩后缀。

### 3.7 Workspace 与 projection cache

- `storages/workspace.json` 是版本 2 JSON，保存 workspace path 与 `sessionIds`；
- `WorkspaceEntity.attachSession` 要求目标 session header 的 `cwd` 解析后等于 workspace path；
- `--apply` 时可以显式 `--update-workspace` 更新目标 workspace 的 `sessionIds`，否则只能通过 `dsh --session-id` 或刷新工作区看到；
- `session_projcache` 是缓存，不手工修改；
- 任何 workspace.json 更新都要求 DSH 关闭并做备份。

---

## 4. 检测设计

### 4.1 实例 Probe

默认根：

```text
DSH_HOME（环境变量优先）
  -> $HOME/.dsh（缺省）
```

`--root` 可以指向：

- `$DSH_HOME`；
- `$DSH_HOME/sessions`；
- 某个 project 目录或 session 目录（需要向上解析到 `DSH_HOME`）。

必须验证的 marker：

- `sessions/` 目录存在；
- 至少一个 `--*--/<session-id>/session.v*.jsonl[.zstd]`；
- header 可解析，`type == "session"`，`version` 是非负整数；
- session id 可编码为安全路径段；
- `storages/workspace.json` 可选，用于工作区导航；
- `profiles/`、`cordis.patch.yml` 可选，用于确认 DSH 实例身份。

置信度建议：

| 信号 | 分数 |
|---|---:|
| `DSH_HOME` / 目录名命中 | 0.2 |
| `sessions/` + project/session 目录布局 | 0.5 |
| 至少一个 header 可解析 | 0.8 |
| 当前 v4 或可识别历史版本 | 1.0 |

### 4.2 版本与 backend

Detection 需要报告：

```json
{
  "format_version": 4,
  "format_status": "current",
  "backend": "jsonl",
  "compression": "zstd",
  "event_count": 123,
  "readable": true,
  "writable": "bundle-only"
}
```

分类：

- `v4`：`current`，可直接读取/写入；
- `v0`–`v3`：`migration-required`，读取应优先走官方 ZIP；不要静默按 v4 解释；
- `v>4`：`unsupported`，必须拒绝；
- malformed header / corrupted frame：`malformed`，不能写入。

Backend：

- `session-persistence-jsonl`：首版支持；
- `session-persistence-sqlite`：检测并标记 `unsupported`，后续单独适配；
- 本机默认数据是 JSONL + zstd。

### 4.3 进程与写入锁

- DSH 使用 per-session write lease；Windows 上可能不留下可见的 lock 文件；
- detection 应检查目标进程名与 `session.lock`（存在时）；
- `--apply` 要求 DSH 已关闭；
- 读取允许在 DSH 运行时执行，但必须提示可能读到进行中的写入；
- 不能在目标 DSH 运行时写 `workspace.json` 或 session 文件。

---

## 5. CLI 契约

建议在 `aicli transfer` 下增加 DSH 适配器：

```powershell
# 检测 DSH 实例
aicli transfer detect --agent deepseek-harness --json

# 列出会话
aicli transfer list `
  --agent deepseek-harness `
  --instance deepseek-harness:default `
  --json

# 首选：导入 DSH 官方 /export ZIP
aicli transfer import `
  --from deepseek-harness `
  --zip .\dsh-session-session-xxx.zip `
  --view surface `
  --dry-run --json

# 直接读取 v4 原始会话
aicli transfer import `
  --from deepseek-harness `
  --instance deepseek-harness:default `
  --session session-09f64be1-1db1-4c03-ab37-01cbe17af4cb `
  --view surface `
  --user alice `
  --json

# 导出到 DSH：默认只生成 bundle
aicli transfer export `
  --to deepseek-harness `
  --session session_20260923112350_fny0opuB `
  --bundle-dir .\dsh-export `
  --compression zstd

# 实验性写入 DSH_HOME（后续；当前命令不支持 --apply）
aicli transfer export `
  --to deepseek-harness `
  --instance deepseek-harness:default `
  --session session_20260923112350_fny0opuB `
  --apply
```

建议 flag：

| Flag | 说明 |
|---|---|
| `--view surface\|transcript` | `surface` 是默认；`transcript` 用于人类历史/归档 |
| `--zip <file>` | 读取官方 `/export` ZIP |
| `--include-system` | 后续；当前默认跳过 |
| `--include-subagents` | 后续；当前不导出子会话树 |
| `--compression zstd\|none` | 导出 bundle 的物理编码；默认 `none` |
| `--update-workspace` | 后续 `--apply` 才支持；当前不写 workspace.json |
| `--repair-tools` | 后续；当前缺失结果只产生 warning/合成 bundle 结果 |
| `--raw-source-metadata` | 后续；当前只保留可追溯键 |

退出码沿用主方案：

- `0` 成功或 dry-run 通过；
- `1` 用法错误；
- `2` 版本不支持、目标 busy、格式损坏、写入/读回失败。

---

## 6. 安全与兼容边界

### 6.1 不迁移

- DSH system prompt、developer tool-addition/removal；
- DSH provider credentials、profiles、cordis patch、MCP 配置；
- DSH session lease、projection cache；
- DSH 的 `agent/inbox/spliced` 队列记录；
- 默认不迁移 subagent 子树、attachment store、workspace 归档/固定状态。

### 6.2 需要显式授权

- `--apply`：直接写入 DSH_HOME；
- `--update-workspace`：修改 `workspace.json`；
- `--include-system`：把外部 system prompt 放进 aicli 历史；
- `--include-subagents`：导入/导出子会话树；
- `--repair-tools`：为不完整工具链合成错误结果。

### 6.3 兼容性结论

| 能力 | 状态 |
|---|---|
| 检测 DSH_HOME / sessions / v4 header | 稳定可实现 |
| 官方 ZIP → aicli | 优先、低风险 |
| 原始 v4 JSONL(.zstd) → aicli | 可实现，需 surface fold 和 zstd |
| v0–v3 原始文件 → aicli | 不在 Go 首版重实现；走 DSH 官方导出/迁移 |
| aicli → DSH bundle | 可实现 |
| aicli → DSH `--apply` | 未实现；当前只支持 bundle，后续需 v4 + JSONL backend + DSH 关闭 |
| DSH SQLite backend | 暂不支持 |
| 附件/图片/文件 | 后续 Phase 5 |
| 子会话 / team / workspace 状态 | 后续单独设计 |

---

## 7. 测试与验收

### 7.1 Fixtures

- 最小 v4 header；
- 单用户/助手回合；
- assistant text + reasoning + tool-call block；
- tool/call + tool/result 配对；
- `surfaceOp: replace` 的 compaction；
- `sourceEventSeqs` 覆盖被替换节点；
- zstd 多帧；
- 损坏的尾帧；
- v0–v3 header；
- v>4 header；
- DSH 官方 ZIP（根 + subagents + media/files）。

fixture 必须脱敏，不提交真实用户内容。

### 7.2 Unit tests

- header 校验；
- seq 连续性；
- surface append/replace；
- 被 replacement 隐藏的节点不进入 surface；
- transcript 模式保留 append-origin；
- assistant tool-call 与 tool/result 配对；
- system/developer 默认跳过；
- zstd 多帧解压；
- projectKey / encodeSegment；
- workspace.json 读取与可选更新；
- 版本拒绝与错误信息。

### 7.3 Integration tests

- 临时 DSH_HOME：
  - 生成一个 v4 JSONL；
  - 用 DSH 的 persistence reader/测试 harness 读取；
  - 检查 header、event count、surface nodes；
- 官方 ZIP 导入 aicli：读回并 resume；
- aicli bundle → 目标临时目录 → 读回；
- `--apply` 时目标运行中必须拒绝；
- `--apply` 失败注入不留下半写 session；
- `workspace.json` 更新失败可回滚。

### 7.4 验收标准

- 当前 v4 会话导入 aicli 后消息顺序、工具配对、标题和 cwd 正确；
- compaction 后不会把被 shadow 的旧消息重复导入；
- 旧版本原始文件不会被误当成 v4；
- 官方 ZIP 的根日志和附件路径能正确解析；
- 导出 bundle 能通过 DSH v4 读取校验；
- `--apply` 之后 DSH 能打开该 session，且重启后仍可读；
- 目标运行时所有写入操作均 fail-closed。

---

## 8. 实施阶段

### Phase 1：只读检测 + 官方 ZIP 导入（已完成）

- DSH_HOME/sessions 检测；
- v4 header 分类；
- 官方 zip parser；
- `--view surface|transcript`；
- 文本/tool history 导入；
- 不写 DSH。

### Phase 2：原始 v4 JSONL + zstd（已完成）

- 多帧 zstd 解压；
- v4 surface fold；
- v0–v3 提示走官方导出；
- 大文件/损坏尾帧处理（当前返回可诊断错误）；
- 附件解析属于后续 Phase 5。

### Phase 3：aicli → DSH bundle（已完成）

- v4 JSONL 生成；
- header/event lifecycle/stream 生成；
- zstd/none；
- roundtrip 校验；
- workspace 元数据计划属于后续 `--apply`。

### Phase 4：实验性 `--apply`（未实现）

- DSH 进程/锁检查；
- 备份、临时文件、原子发布；
- 可选 workspace.json 更新；
- 写后 DSH 读取验证；
- 发布前限制 v4 + JSONL backend。

### Phase 5：扩展（未实现）

- DSH SQLite backend；
- subagent/team；
- attachments 完整迁移；
- DSH 官方 CLI/API import 入口（如果未来出现）。

---

## 9. 风险与开放问题

| 风险 | 影响 | 处理 |
|---|---|---|
| DSH v4 语义复杂 | 错误 surface fold 会重复/丢失历史 | 先走官方 ZIP；直接读取要求 fixtures |
| `assistant/message.stream` 合成不合法 | DSH 拒绝打开 | 生成完整 stream 记录，或先在临时 DSH_HOME 验证 |
| 没有官方 import | 只能私有格式写入 | 默认 bundle；`--apply` 实验性 |
| DSH 运行中写 workspace/session | 租约冲突、覆盖 | 必须关闭 DSH |
| `workspace.json` 未更新 | Web 工作区列表看不到 | 显式 `--update-workspace` 或提示刷新 |
| v0–v3 格式 | Go 重实现迁移成本高 | 使用 DSH 官方导出/迁移 |
| SQLite backend | 当前适配器不适用 | 检测为 unsupported，后续适配 |
| 附件/子会话 | 首版不完整 | 明确 warning 和后续阶段 |
| tool call 不完整 | DSH 生命周期校验失败 | 默认拒绝；`--repair-tools` 才合成 |
| DSH 更新格式 | 写入失效 | header version gate + 目标版本探测 |

需要进一步确认：

1. DSH 是否会在 Web 工作区刷新时自动从 persistence 重建 `workspace.json` 的 session membership。
2. v4 `assistant/message.stream` 是否允许空数组；若不允许，需按内容生成完整紧凑 stream。
3. DSH 的 SQLite persistence 是否有稳定导出 API，是否值得支持。
4. subagent 子树应作为独立 transfer 对象还是主会话附带元数据。
5. 附件在无 DSH attachment service 的离线 bundle 中如何表达。

---

## 10. 参考

DSH 源码与文档：

- `apps/cli/src/args.ts` — `--session-id` / `--resume` 入口；
- `docs/session-format-status.md` — 当前 v4 与 release/finalization 记录；
- `docs/subsystems/session.md` — SessionEventMap、surface 与生命周期；
- `docs/subsystems/persistence.md` — persistence seam、header、write lease；
- `docs/persistence-catalog.md` — 事件 payload 与 surface/log-only 分类；
- `docs/persistence-changes/` — v0–v4 历史格式与迁移；
- `packages/session/session-format/` — header 分类、相邻迁移、encode/decode；
- `packages/session/session-persistence-jsonl/` — zstd 多帧、目录布局、lease；
- `packages/session-query/session-log-export/` — 官方 `/export` ZIP；
- `packages/llm/llm/src/message.ts` — system/developer/user/assistant/tool 消息；
- `packages/llm/llm/src/types.ts` — ContentBlock（text/reasoning/image/file/tool-call）；
- `packages/core/session/src/surface.ts` — surface fold 与 `surfaceOp: replace`；
- `packages/workspace/workspace/src/entity.ts` — workspace/session membership。

本机抽样路径（仅排障参考）：

```text
%USERPROFILE%\.dsh
%USERPROFILE%\.dsh\sessions
%USERPROFILE%\.dsh\storages\workspace.json
%USERPROFILE%\.dsh\storages\session_projcache
```

相关文档：

- [cross-agent-session-transfer.md](./cross-agent-session-transfer.md)
- [session-export-import.md](./session-export-import.md)
