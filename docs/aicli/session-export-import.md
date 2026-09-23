# aicli 会话导出与导入操作手册

`aicli export` 把会话导出为完整 JSON 或 Markdown 投影；`aicli import` 是 `export --full` 的反向入口，把导出的完整 JSON 写回会话库。两条命令与 chat 内的 `/export` 复用同一套导出实现与格式语义，适合备份、跨机器迁移、跨会话库复制与 CI 场景。

本文档涵盖：

- [一、适用场景](#一适用场景)
- [二、导出](#二导出)
- [三、导入](#三导入)
- [四、ID 与归属策略](#四id-与归属策略)
- [五、落库语义](#五落库语义)
- [六、退出码](#六退出码)
- [七、结构化输出与脚本集成](#七结构化输出与脚本集成)
- [八、排障](#八排障)

---

## 一、适用场景

优先使用 `export` / `import` 的场景：

- **备份单个会话**：把重要会话导出成自包含的 JSON 文件（含 metadata、tool_calls、tool 结果）。
- **跨机器/跨用户迁移**：在新机器上导入，默认沿用原 ID 与原用户；用户不同时用 `--user` 改写归属。
- **跨会话库复制**：用 `--session-dir` 指向另一个会话库，把会话复制过去。
- **CI / 脚本**：导出与导入都有稳定退出码；导入还支持 `--json` 摘要与 `--dry-run` 预检。
- **只读分享**：`--body` / `--tools` / `--trace` 生成给人读的 Markdown（不用于导入）。

不适用的场景：

- **跨大版本迁移**：导入只接受 `version == 1` 的导出文件，版本不匹配会明确报错。
- **整库备份 / 故障取证**：导出面向单个会话，不含 debug 日志与 artifacts；整包请用 `/debug export` / `/debug zip`（见 [install.md](./install.md) 的 `/debug` 条目）。

---

## 二、导出

### 命令与格式

```bash
aicli export [current|latest|<session-id>] [--full|--body|--tools|--trace] [--format <fmt>] [--output <path>|--dir <dir>] [--session-dir <dir>] [--user <id>]
```

| 格式 | 等价 flag | 产物 |
|---|---|---|
| `full`（默认） | `--full` | 完整 JSON：messages + metadata + tool_calls + tool 结果全量导出；**唯一可被 `aicli import` 接受的格式** |
| `body` | `--body` | 正文 Markdown：仅用户/助手正文，不含工具链 |
| `md-tools` | `--tools` | Markdown + 工具名、call id 与输入参数 |
| `md-trace` | `--trace` | 在 `--tools` 基础上按 `tool_call_id` 内联输出结果（单条输出超过 32 KB 自动截断并标记，完整内容用 `--full`） |

`--format full|body|md-tools|md-trace` 是等价写法，也接受裸格式词。格式来源互相冲突时（例如同时写 `--full --trace`）直接报参数错误，不会静默取最后一个。

### 会话目标

| 目标 | 说明 |
|---|---|
| `latest`（默认） | 最近一次可恢复会话 |
| `current` | 顶层命令没有「当前会话」上下文，等价于 `latest`；实际导出的会话 ID 会打印在摘要里 |
| `<session-id>` | 指定会话 ID，不校验用户归属（跨身份平面创建的会话同样可导出） |

### 输出位置

- 默认：`~/.aicli/chat-logs/exports/<session-id>_<时间戳>_<格式>.<json|md>`；
- `--output <path>`：指定输出文件（指向已存在的目录时等价于 `--dir`）；父目录不存在会自动创建；
- `--dir <dir>`：指定输出目录。

### 成功输出

```text
会话已导出
Session:           session_20260923112350_fny0opuB
Format:            full
Output File:       /home/user/.aicli/chat-logs/exports/session_20260923112350_fny0opuB_20260923-120000_full.json
Messages:          123
Tool Calls:        45
Tool Results:      45
```

`Tool Calls:` / `Tool Results:` 两行对 `full` 与两种 Markdown 变体都会显示，只有纯正文的 `--body` 导出省略。

### 导出文件结构（`--full`）

```json
{
  "version": 1,
  "exported_at": "2026-09-23T12:00:00+08:00",
  "format": "full",
  "source": "cli",
  "session_store": "...",
  "preview": { "id": "session_...", "messageCount": 123, "updatedAt": "..." },
  "stats": {
    "message_count": 123,
    "tool_call_count": 45,
    "tool_result_count": 45,
    "content_part_count": 2
  },
  "session": {
    "id": "session_20260923112350_fny0opuB",
    "userId": "alice",
    "state": "active",
    "history": [ { "role": "user", "content": "..." } ],
    "metadata": { "title": "...", "context": { "workspace_path": "..." } },
    "createdAt": "...",
    "updatedAt": "..."
  }
}
```

说明：

- `version` 当前为 `1`；`import` 只接受 `version == 1` 且带 `session` 对象的文件。
- `source` / `session_store` / `session_path` 是诊断字段（`session_path` 仅在能解析出会话文件位置时出现），导入不依赖它们。
- `stats` 是摘要，导入时用于一致性检查：与 `history` 条数不一致会提示文件可能被截断。
- `session.history` 是 canonical 全量历史，不是可能被窗口裁剪的 prompt 投影。

### chat 内 `/export` 与顶层命令的差异

- chat 内 `/export` 出错只打印提示、仍返回 `0`；脚本请用顶层 `aicli export` 判断成败。
- chat 内 `current` 指当前会话；顶层命令没有该上下文，`current` 落到 `latest`。
- 顶层命令的 `--output` 是文件路径；chat 内 `/export` 用 `--output <path>` / `--dir <dir>` 指定落点，语义一致。

---

## 三、导入

### 命令

```bash
aicli import <file> [--session-dir <dir>] [--user <id>] [--new-id] [--dry-run] [--output text|json] [--json]
```

> 注意：`import` 的 `--output` 是**输出格式**（`text|json`），不是路径；`--json` 等价于 `--output json`。

| Flag | 说明 |
|---|---|
| `--session-dir <dir>` | 目标会话库目录（默认 `~/.aicli/sessions`） |
| `--user <id>` | 覆盖导入后的会话用户 ID（默认沿用导出文件里的 `userId`） |
| `--new-id` | 目标库已存在同 ID 会话（或文件缺少/不可寻址 ID）时自动生成新 ID；默认报错，不覆盖 |
| `--dry-run` | 只校验文件与冲突并打印摘要，不写入会话库 |
| `--output text\|json` / `--json` | 输出格式；JSON 为单行紧凑结构，便于脚本消费 |

### 只接受 `--full` 产物

`--body` / `--tools` / `--trace` 生成的是 Markdown 投影，导入会以「解析失败 / 缺少 session 字段」拒绝。请始终用 `aicli export --full` 导出的 JSON 作为输入。

### 基本示例

```bash
# 1) 默认：沿用原 ID 与原用户；同 ID 已存在则报错、绝不覆盖
aicli import ./exports/session_20260923_full.json

# 2) 目标已有同 ID 会话时，生成新 ID 两份并存
aicli import ./session.json --new-id

# 3) 覆盖归属
aicli import ./session.json --user alice

# 4) 只预检（校验文件、冲突与 ID 策略，不写库）
aicli import ./session.json --dry-run --json

# 5) 指向隔离的会话库（例如迁移到新机器前先验证）
aicli import ./session.json --session-dir ./sessions
```

### 成功输出

```text
会话已导入
Session:           session_20260923112350_fny0opuB
User:              alice
State:             active
Messages:          123
Tool Calls:        45
Source File:       /path/to/session_20260923112350_fny0opuB_full.json
Exported At:       2026-09-23T12:00:00+08:00
Session Dir:       /home/user/.aicli/sessions
提示: 用 aicli chat --resume 继续该会话（updatedAt 已置为导入时间，它会排在最近会话首位），或用 aicli export <session-id> --full 再次导出。
```

`--dry-run` 打印「导入预检通过（未写入）」+ 同一份摘要，退出码语义与真实导入一致（通过 = `0`），可安全用在 CI 前置检查。

---

## 四、ID 与归属策略

### 会话 ID

默认沿用导出文件里的 `session.id`：

| 文件里的 `session.id` | 默认行为 | 加 `--new-id` |
|---|---|---|
| 可寻址、目标库无同 ID | 沿用原 ID 导入 | 同左（无冲突不生成） |
| 可寻址、目标库已有同 ID | 报错退出（`exit=2`），**绝不覆盖** | 生成新 ID，两份并存 |
| 缺失 | 报错退出（`exit=1`） | 生成新 ID |
| 不可寻址（含路径分隔符、首尾空白或 `<nil>` 占位值） | 报错退出（`exit=1`） | 生成新 ID |

为什么默认拒绝覆盖：存储层的 `Save` 对已存在 ID 是 upsert 语义，放过一次就等于静默覆盖用户的原会话（不可逆）。确要替换原会话时，请先导出备份、确认后再手动删除原会话，然后重新导入。

为什么不可寻址 ID 必须拦截：存储层读写两侧都会对 ID 做规范化（去首尾空白、去尾部分隔符、取路径最后一段），`"dir/abc"`、`"abc/"`、`" abc "` 这类 ID 写得进、读不回，落库后就是列表里点开必然 404 的孤儿记录。

`--new-id` 生成的新 ID 沿用 `NewSession` 的 ID 规则，并在写入前回查存储，避免同秒撞车时静默覆盖。

### 用户归属

优先级：`--user <id>` > 导出文件里的 `userId` > 当前解析出的默认用户。

- `--user` 与文件里的值不一致时，摘要中会告警并把 `user_overridden` 置为 `true`；
- 文件缺少 `userId` 且当前环境没有默认用户时直接报错，提示用 `--user` 指定。

---

## 五、落库语义

导入不是「拷贝数据库文件」，而是走与 `SessionManager.Create` 同一条写路径（`storage.Save`），并在落库前做归一化：

| 字段 | 处理 | 原因 |
|---|---|---|
| `createdAt` | 保留原值（缺失才用当前时间） | 会话的创建时间不应因搬运而改变 |
| `updatedAt` | 刷新为导入时间 | 导入后立刻排在 `resume` / `latest` 列表顶部，也避免刚导入就被 idle 归档 |
| `expiresAt` | 丢弃 | 带着过期的 `expiresAt` 落库，会话会在第一次读取时被判过期删除 |
| `headOffset` | 归零 | 它是源库 prompt 窗口的相对游标，沿用会让 `DroppedHistory()` 误报被压缩丢弃的前缀 |
| `canonicalMessageCount` | 由存储按 `history` 重算 | 同上，避免沿用源库计数 |
| `state` | 保留原值 | 非 `active` 状态会在摘要里提示（会话列表可能默认隐藏它） |
| `history` | 按完整视图全量写入 | 导入声明 `HistoryLoaded=true`，存储写入 canonical 消息 |
| `message_id` / `turn_id` | 缺失补齐；复用的重复身份重新铸造 | 读取/分页路径会折叠「相邻 + 同 `message_id` + 同内容」的消息，重复身份等于静默少消息（内容一条不动，只换掉重复的 ID） |
| 工具链身份（`tool_calls[].id` / `tool_call_id`） | 不完整只告警、不拒绝 | 存储不校验这类数据，但渲染与回放可能对不上工具结果 |
| `metadata.context` | 原有键保持不变；新增 `imported_from` / `imported_at` | 保留 `workspace_path` 等键，导入的会话仍回到原来的工作目录分组；改名时另有 `import_original_session_id`，状态被改写时另有 `import_original_state` |
| `metadata.tags` | 空值补 `[]` | 避免 `null` 破坏下游消费 |

写入后立即读回校验：确认会话可加载、ID 与归属正确，且 canonical 消息条数与导出文件一致；不一致会以确定性错误（`exit=2`）返回。

---

## 六、退出码

`export` 与 `import` 使用同一套退出码约定：

| 退出码 | 含义 | `import` 的典型场景 |
|---|---|---|
| `0` | 成功 | 导入成功；`--dry-run` 预检通过 |
| `1` | 参数错误 | 缺少文件参数、`session.id` 缺失或不可寻址、选项冲突 |
| `2` | 确定性错误 | 文件不可读或不是 `--full` 产物、目标已有同 ID 会话、写库或读回校验失败 |

`export` 的对应场景：`1` 为格式 flag 冲突或未知格式；`2` 为会话不存在、会话存储不可读、输出发布失败。

chat 内的 `/export` 出错仍返回 `0`（只打印提示），脚本请使用顶层命令。

---

## 七、结构化输出与脚本集成

`--json` / `--output json` 输出单行紧凑 JSON（成功）：

```json
{"session_id":"session_...","source_session_id":"session_...","renamed":false,"user_id":"alice","user_overridden":false,"state":"active","message_count":123,"tool_call_count":45,"content_part_count":2,"source_file":"/abs/path.json","source_format":"full","exported_at":"2026-09-23T12:00:00+08:00","store_dir":"/home/user/.aicli/sessions","dry_run":false,"warnings":["..."]}
```

| 字段 | 说明 |
|---|---|
| `session_id` | 导入后的会话 ID（生成新 ID 时是新值） |
| `source_session_id` | 导出文件里的原 ID（仅当有值） |
| `renamed` | 是否因冲突、缺失或不可寻址而改用新 ID |
| `user_id` / `user_overridden` | 落库归属；后者表示被 `--user` 改写 |
| `state` | 落库状态 |
| `message_count` / `tool_call_count` / `content_part_count` | 写入的消息数、工具调用数、内容分片数 |
| `source_file` / `source_format` / `exported_at` | 源文件绝对路径、导出格式与导出时间 |
| `store_dir` | 目标会话库目录 |
| `dry_run` | 是否为预检 |
| `warnings` | 告警列表（缺失/重复身份、工具链不完整、状态非 active、stats 不一致等） |

失败时（`--json` / `--output json`）stdout 输出 `{"ok":false,"command":"import","error":"...","details":{"hint":"..."}}`，脚本无需解析中文提示。加全局 `--envelope` 时，成功输出包装为 `{"ok":true,"command":"import","data":{...}}`。

脚本示例（导出 → 预检 → 导入到隔离目录）：

```bash
#!/usr/bin/env bash
set -euo pipefail
SRC=session_20260923112350_fny0opuB

aicli export "$SRC" --full --output "./exports/${SRC}_full.json"
aicli import "./exports/${SRC}_full.json" --session-dir ./sessions --dry-run --json
aicli import "./exports/${SRC}_full.json" --session-dir ./sessions --json
```

`export` 本身不提供 `--json`：机器可读的产物就是导出的文件，成功与否请用退出码判断。

---

## 八、排障

| 症状 | 原因 | 处理 |
|---|---|---|
| `会话已存在: session_xxx（导入不会覆盖已有会话）` | 目标库已有同 ID 会话 | 加 `--new-id` 两份并存；确要替换则先备份、再手动删除原会话 |
| `导出文件里的 session.id "..." 不可寻址` | ID 含路径分隔符、首尾空白或占位值 | 修正文件里的 ID，或加 `--new-id` |
| `导出文件缺少 session.id` | 文件里的 ID 被手工删掉 | 加 `--new-id` |
| `不支持的导出文件版本 N` | 文件不是当前版本的产物 | 用 `aicli export --full` 重新导出 |
| `解析导入文件失败` / `导出文件缺少 session 字段` | 用了 `--body` / `--tools` / `--trace` 的 Markdown，或文件被截断 | 改用 `--full` 导出的 JSON |
| `读取导入文件失败: ...` | 路径不存在或无权限 | 检查路径与权限 |
| 导入成功但列表里看不到 | 会话 `state` 非 `active`，或 `workspace_path` 与当前工作目录不同 | 用 `--cwd=false` 查看全部；或直接 `aicli resume <session-id>` |
| `提示: 导出文件 stats.message_count=N 与 history 条数 M 不一致` | 文件可能被截断 | 重新导出后再导入 |
| `提示: ... 复用了重复的 message_id，已重新铸造` | 源文件里消息身份重复 | 属自动修复；如需源文件本身干净，请重新导出 |
| `提示: N 条消息缺少工具调用 ID 或 tool_call_id` | 源会话工具链身份不完整 | 导入不拒绝；渲染/回放可能对不上工具结果，可接受则忽略 |
| `无法确定导入会话的用户归属` | 文件无 `userId` 且环境无默认用户 | 加 `--user <id>` |
| 导入报错但会话看起来已存在 | 写库成功、读回校验失败（`exit=2`） | 按提示检查目标库状态；确认后可删除该会话重新导入 |

---

相关文档：

- [install.md](./install.md)：安装、配置与全部子命令速查（含 `export` / `import` 一行式说明）
- [exec.md](./exec.md)：headless `aicli exec`（含会话恢复与 CI 用法）
- [quickstart.md](./quickstart.md)：快速上手
- [faq.md](./faq.md)：常见问题排查
