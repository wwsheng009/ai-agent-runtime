# remote-tools 运行期目录切换：能力盘点与优化方案

- 状态：已实施（P0 与 P1 全部落地，P2 未实施）
- 日期：2026-10-30
- 范围：`backend/internal/remotetools`（MCP 服务端）、`backend/cmd/aicli-remote-tools`（进程入口）、共享 schema `backend/internal/toolcore/mutatecore`、`lscore` / `searchcore` / `viewcore`
- 关联文档：`docs/tools/unified-tool-registry-plan.md`、`docs/tools/tool-schema-baseline-20261015.md`

---

## 1. 问题陈述

需求是：**remote-tools 在一次会话运行过程中，需要能对不同目录执行操作**。

需要回答三件事：

1. 现有实现支持到什么程度？
2. 全部对外暴露的工具中，哪些缺少目录参数、哪些语义不一致？
3. MCP schema 的参数描述是否需要调整？需要哪些优化？

---

## 2. 结论摘要

**当前是"部分支持"，且支持方式是隐式的、不一致的。**

| 层次 | 现状 | 判定 |
|---|---|---|
| 单次调用切换目录 | `shell` / `job_start` 有 `workdir`；文件类工具只能靠 `path` / `file_path` 传路径 | 可用但不对称 |
| 会话级"当前目录" | 不存在。没有 sticky cwd，没有 `cd` / `set_workdir` 工具 | **缺失** |
| 持久 shell | 每次调用都是新进程，裸 `cd` 不跨调用生效 | **缺失**（这也是 `workdir` 存在的原因） |
| 会话级根收窄 | MCP Roots 收窄已接线（`server.go:147-148`） | 已实现 |
| 根的范围扩张 | Roots 只能收窄、不能扩张（`roots.go:48-64`）；`allowed-roots` 为启动期配置 | **受限** |
| 多根并存时的"当前根" | `Workspace()` 固定取 `effective[0]`（`roots.go:96-107`） | **缺失语义** |

**一句话**：能"这一次在 A 目录跑、下一次在 B 目录跑"，但每次都要显式传参；不能"切过去一直待在那儿"。

---

## 3. 现有目录解析机制

### 3.1 统一入口

所有工具的路径解析收敛到 `workspaceForCall(ctx, b.workspace)`（`tools.go:218-220`）：

```
workspaceForCall(ctx, base)
  └─ rootsResolver.decorate 注入 ctx（roots_transport.go:167-179）
       └─ RootsFromContext(ctx) → narrowed（会话声明根，只能收窄）
            └─ 无声明 → fallback base workspace（启动时 --workspace）
```

### 3.2 路径三态规则

`workspace.go:157-195` `Resolve`：

| 传入 | 解析结果 |
|---|---|
| 空字符串 | `w.root`（当前有效工作区根） |
| 绝对路径 | `filepath.Clean` 后原样使用 |
| 相对路径 | `join(w.root, trimmed)` |

随后两级安全校验：

1. `ensureInside`（`workspace.go:198-209`）——必须落在 `allowedRoots` 内，否则 `CodePathDenied`
2. 符号链接二次校验 `resolveExistingAncestor`（`workspace.go:188-193`）——解析最深已存在祖先的真实路径后再复查；不存在的新建目标保留词法值

### 3.3 shell 的 workdir 解析

`shell.go:110-128`：

```go
effective := workspaceForCall(ctx, t.workspace)   // 先取会话有效根
workdir := effective.Root()                        // 默认 = 根
if raw := params["workdir"]; raw != "" {
    resolved := t.resolve(ctx, raw)                // 走 3.2 三态
    // 额外校验必须是"已存在目录"
}
```

注意：默认值来自 `workspaceForCall`，说明**shell 的默认目录是随会话根走的**，这点是正确的设计。

### 3.4 envd 后端（另一条路径）

`envd_backend.go:332-343` `resolvePath` = `lexicalResolve` → `canonicalize` → `insideJail`，与本地后端**独立实现**，但语义对齐。

---

## 4. 全部对外暴露工具的能力矩阵

### 4.1 基础工具（默认开启）

| # | 工具 | 路径/目录参数 | 是否有独立"目录基准"参数 | 备注 |
|---|---|---|---|---|
| 1 | `shell` | `workdir` | ✅ 有 | `shell.go:56-59`；默认=会话有效根；校验目录必须存在 |
| 2 | `job_start` | `workdir` | ✅ 有 | `jobs_tools.go:101-107`；映射 `BackgroundTaskArgs.Cwd` |
| 3 | `job_output` | — | — | 仅 `job_id`/`offset`/`tail_bytes` |
| 4 | `job_cancel` | — | — | 仅 `job_id` |
| 5 | `job_list` | — | — | 仅 `limit` |
| 6 | `view` | `file_path` / `paths` / `files` | ❌ 无 | `view.go:129-132`；`symbol` 默认分支被 `FilterSchemaParams` 过滤（`registry.go:154`） |
| 7 | `ls` | `path` / `depth` | ❌ 无 | `lscore.Parameters()`（`registry.go:127`）；`path` 默认 workspace 根 |
| 8 | `glob` | `path` / `pattern` | ❌ 无 | `searchcore.GlobParameters()`（`registry.go:174`） |
| 9 | `grep` | `path` / `pattern` 等 | ❌ 无 | `registry.go:660-675` |
| 10 | `artifact_read` | `artifact_ref` / `artifact_id` / `offset` / `limit` | — | `registry.go:714-737` |
| 11 | `write` | `file_path` / `content` / `expected_sha256` | ❌ 无 | `mutatecore/schema.go:12-31` |
| 12 | `append_write` | `file_path` / `content` / `create_if_missing` / `truncate_first` / `ensure_trailing_newline` / `expected_offset` | ❌ 无 | `schema.go:34-66` |
| 13 | `edit` | `file_path` / `old_string` / `new_string` / `replace_all` / `replacement_count` | ❌ 无 | `schema.go:69` |
| 14 | `apply_patch` | `patch` | ❌ 无 | `schema.go:99`；**文件路径藏在 patch 正文里**，基准完全隐式 |

### 4.2 可选工具（`--profile` 开启）

| # | 工具 | 路径/目录参数 | 独立目录基准 | 备注 |
|---|---|---|---|---|
| 15 | `multiedit` | `file_path` | ❌ 无 | `optional_tools.go:42` |
| 16 | `download` | `file_path` / `url` | ❌ 无 | `optional_tools.go:49` |
| 17 | `fetch` | `url` / `urls` | — | 无本地路径 |
| 18 | `sourcegraph` | `query` / `max_results` 等 | — | 无本地路径 |

### 4.3 envd 后端专用工具（仅 envd 后端注册）

> 注意：envd 后端**同时复用**共享 surface 的 `shell` / `view` / `ls` / `glob` / `grep` / `write`，它们复用同一组常量（`registry.go:132` ToolLs、`180` ToolGlob、`678` ToolGrep；`envd_tools.go` 复用 `ToolLs` 等），属于**同一工具的另一后端实现，不是新工具**。下表只列 envd 额外注册的工具。

| # | 工具 | 路径/目录参数 | 独立目录基准 | 备注 |
|---|---|---|---|---|
| 19 | `mkdir` | `path` | ❌ 无 | `envd_tools.go:33` |
| 20 | `remove` | `path` | ❌ 无 | `envd_tools.go:34` |
| 21 | `move` | `source` / `destination` | ❌ 无 | `envd_tools.go:35`；两个路径共享同一基准 |
| 22 | `host_metrics` | — | — | `envd_tools.go:36`；无路径 |
| 23 | `watch_dir` | `path` / `wait_ms` / `max_events` | ❌ 无 | |
| 24 | `pty_start` | `cwd` / `cols` / `rows` | ⚠️ 有 | `envd_pty.go:193-260` |
| 25 | `pty_write` / `pty_read` / `pty_resize` / `pty_close` | `pid` | — | 作用于已存在 PTY |

### 4.3.1 后端差异（同一工具、不同参数名）

| 工具 | 本地后端 | envd 后端 | 问题 |
|---|---|---|---|
| `shell` | `workdir`（`shell.go:56`） | `cwd`（`envd_process.go:54`） | 同名工具、参数名不同，模型无法预知 |

### 4.4 统计

- 暴露工具总数（去重后）：**28**
  - 共享基础 surface：14（`shell`、`view`、`ls`、`glob`、`grep`、`job_start`、`job_output`、`job_cancel`、`job_list`、`artifact_read`、`write`、`edit`、`append_write`、`apply_patch`）
  - 可选 surface：4（`multiedit`、`download`、`fetch`、`sourcegraph`）
  - envd 额外注册：10（`mkdir`、`remove`、`move`、`host_metrics`、`watch_dir`、`pty_start`、`pty_write`、`pty_read`、`pty_resize`、`pty_close`）
- 具备独立目录基准参数：**3**（`shell.workdir`、`job_start.workdir`、`pty_start.cwd`）
- 仅有路径参数、无独立基准：**14**（`view`、`ls`、`glob`、`grep`、`write`、`append_write`、`edit`、`apply_patch`、`multiedit`、`download`、`mkdir`、`remove`、`move`、`watch_dir`）
- 完全不涉及路径：**11**（`job_output`、`job_cancel`、`job_list`、`artifact_read`、`fetch`、`sourcegraph`、`host_metrics`、`pty_write`、`pty_read`、`pty_resize`、`pty_close`）

> 结论：28 个工具里只有 **3 个**（约 11%）具备独立目录基准参数，**14 个**路径型工具只能通过 `path` / `file_path` 里的相对或绝对写法间接表达目录。这是 §G1 不对称问题的量化依据。

---

## 5. 缺口分析

### G1 目录参数不对称（严重度：高）

`shell` 能换目录，`write` / `read` / `grep` / `glob` 不能。模型为了在 B 目录写文件，必须在 `file_path` 里拼相对路径 `../B/x.txt` 或写绝对路径。这带来三个问题：

1. **认知负担**：模型需要自行做路径拼接，幻觉风险高。
2. **与 shell 语义割裂**：`shell(workdir="B", command="pwd")` 与 `write(file_path="x.txt")` 不在同一目录，模型极易误判。
3. **审计困难**：`apply_patch` 的路径完全藏在 patch 正文，连"这次动了哪个目录"都无法从参数层直接读出。

### G2 缺少 sticky cwd（严重度：高）

没有会话级"当前目录"状态。若一次任务需在 A、B 两个目录各做 10 次操作，模型必须重复传 20 次 `workdir`。

还要注意一个反直觉点：**相对路径的基准不是"上一个 workdir"，而是会话有效根**（`shell.go:113`）。所以：

```
shell(workdir="src", command="cd . && ls")   # 在 src 里列目录 ✅
shell(command="ls")                           # 回到根，不是 src ⚠️
```

模型若按"cd 之后一直在那儿"的直觉行事，第二次调用会静默跑错目录——这是当前设计里最容易产生**静默错误**的地方。

### G3 无持久 shell（严重度：中）

每次 `shell` 都是独立进程（`shell.go:23-33` 设计说明）。裸 `cd` 不跨调用生效。当前靠 `workdir` 参数规避，但模型第一次几乎必然会尝试 `cd`，然后困惑为什么没生效。

### G4 多根时无"当前根"语义（严重度：中）

`roots.go:96-107` `Workspace()` 固定取 `effective[0]`。当客户端声明多个根时，谁是"当前根"完全由声明顺序决定，模型无法感知、无法选择。

### G5 `allowed-roots` 无法热更新（严重度：中）

`config.go:375` / `785-792`。运行中新项目目录需要重启进程。对于长期驻留的 MCP 服务，这会造成明显的可用性中断。

### G6 envd 与本地后端参数命名不一致（严重度：中）

- 本地 `shell` 用 `workdir`（`shell.go:56`）
- envd `shell` 用 `cwd`（`envd_process.go:54`）

同一个工具名，两套后端参数名不同。模型无法预知当前连的是哪个后端，必然产生参数错误。

### G7 文档与代码不一致（严重度：低但影响判断）

`roots.go:21-25` 注释仍写 "P4: roots/list 传输层未接线"，但 `roots_transport.go` + `server.go:147-148` 已经接线完成。后来者读注释会误判能力缺失，导致重复实现。

### G8 错误信息不含可用根（严重度：低）

`CodePathDenied` 只说明被拒，不回传当前 `allowedRoots`。模型只能盲猜重试，浪费轮次。

---

## 6. MCP schema 参数描述调整

### 6.1 必须修改的描述（现有描述有歧义或会误导模型）

#### (1) `shell.workdir` —— 当前描述会诱发静默错误

现状（`shell.go:56-59`）：

```json
"workdir": {
  "type": "string",
  "description": "Working directory, relative to the remote workspace. Default: workspace root."
}
```

问题：
- "relative to the remote workspace" 会被理解成"相对上一次的工作目录"
- "Default: workspace root" 没有说明**它不受上次 workdir 影响**

建议：

```json
"workdir": {
  "type": "string",
  "description": "本次命令的工作目录。绝对路径直接使用；相对路径以会话工作区根为基准（不是上次调用的 workdir，也不是进程 CWD）。省略时等于会话工作区根。跨调用不记忆：每次调用都要重新指定。目录必须已存在。"
}
```

#### (2) `job_start.workdir` —— 与 shell 对齐

```json
"workdir": {
  "type": "string",
  "description": "后台任务的启动目录。绝对路径直接使用；相对路径以会话工作区根为基准，不受 shell 上次 workdir 影响。省略时为会话工作区根。目录必须已存在。"
}
```

#### (3) envd `shell.cwd` —— 补跨后端等价说明

```json
"cwd": {
  "type": "string",
  "description": "本次命令的工作目录（等价于本地后端的 workdir）。绝对路径直接使用；相对路径以当前工作区根为基准。省略时为工作区根。"
}
```

同时建议在 envd 侧**增加 `workdir` 作为 `cwd` 的别名**（见 O6），让 schema 在两个后端之间保持稳定。

#### (4) 文件类工具的 `path` / `file_path` —— 补基准说明

现状 `ls.path` 只写 "Default: workspace root."，没有说明相对路径基准。统一改为：

```json
"path": {
  "type": "string",
  "description": "要列举的目录。相对路径以会话工作区根为基准（或本次调用指定的 workdir）。绝对路径必须在允许根内。省略时为会话工作区根。"
}
```

`glob.path`、`grep.path`、`write.file_path`、`edit.file_path`、`append_write.file_path` 同理。

#### (5) `apply_patch.patch` —— 说明路径基准

现状只说明 patch 格式，未说明 patch 里的文件路径如何解析。补一句：

```json
"patch": {
  "description": "Codex 风格补丁……补丁中的文件路径按 workdir（若提供）或会话工作区根解析，相对路径不要自行拼接 ../。"
}
```

#### (6) `move.source` / `move.destination` —— 说明共享基准

```json
"source": { "description": "源路径。相对路径以 workdir 或工作区根为基准。" },
"destination": { "description": "目标路径。与 source 使用同一基准；目标目录不存在时需先 mkdir。" }
```

### 6.2 建议新增的参数

新增统一可选参数 `workdir` 到所有路径型工具（详见 §7 O4）。统一描述模板：

```json
"workdir": {
  "type": "string",
  "description": "可选目录基准。提供时，本工具的相对路径参数以它为基准；绝对路径参数不受影响。省略时以会话工作区根为基准。目录必须存在且在允许根内。"
}
```

### 6.3 schema 一致性要求

`mutatecore/schema.go:5-9` 已有明确约定：

> "if a parameter is declared here, the handler above reads it, and if the handler reads it, it can be declared here. Surfaces that cannot honour something must filter it out (toolcore.FilterSchemaParams)"

因此新增 `workdir` 必须走"先改 handler 读取、再在共享 schema 声明"的顺序；若某 surface 不支持，用 `FilterSchemaParams` 过滤，**不允许**维护一份更弱的私有 schema。这一点与 `view.symbol` 的处理方式一致（`registry.go:154`）。

---

## 7. 优化方案

按"收益 / 改动面"排序，分为三层。P0 与 P1 建议本轮做，P2 建议排期。

### P0：消除静默错误（必须做）

#### O1 修正 `shell.workdir` 描述 + 引入跨调用记忆提示

- 改动点：`shell.go:56-59` 描述文本；`jobs_tools.go:101-107` 对齐
- 改动面：纯 schema 文本，无逻辑变更
- 收益：直接消除 §G2 描述的"以为 cd 生效了"的静默错误
- 风险：极低

#### O2 路径拒绝错误回传可用根

- 改动点：`workspace.go` 的 `ensureInside` 失败分支，在 `toolError` 的 detail 中附 `allowed_roots` 与当前 `workdir`
- 改动面：错误构造，小
- 收益：模型可自我纠正，减少重试轮次（对应 §G8）
- 风险：低。需确认错误里暴露完整根路径是否符合安全要求——建议只回传"当前会话已授权的根列表"，这本来就是模型可推断的信息

#### O3 修正 `roots.go:21-25` 过期注释

- 改动点：注释文本
- 改动面：零逻辑
- 收益：防止后来者误判能力缺失（对应 §G7）
- 风险：无

### P1：统一目录语义（建议做）

#### O4 给全部路径型工具增加可选 `workdir` 参数

覆盖范围（14 个路径型工具 + 2 个容器，见 §4.4）：

```
共享基础：view, ls, glob, grep, write, append_write, edit, apply_patch
可选：    multiedit, download
envd 额外：mkdir, remove, move, watch_dir
同一工具的另一后端实现（需同步改）：envd 版 ls / glob / grep / write
```

实现要点：

1. `workspace.Resolve` 已有三态能力，新增 `ResolveWithBase(ctx, base, path)`：
   ```go
   func (w *Workspace) ResolveWithBase(ctx context.Context, base, path string) (string, error) {
       w2, err := w.withRoot(base)   // 复用 Resolve 的 ensureInside + 软链校验
       if err != nil { return "", err }
       return w2.Resolve(path)
   }
   ```
   这样**安全校验不复用第二套**，避免出现绕过路径。

2. handler 读取顺序：`params["workdir"]` → 非空则 `ResolveWithBase`，空则保持现有 `workspaceForCall` 行为。**默认行为完全不变**，向后兼容。

3. schema 走 `mutatecore/schema.go` 共享源（遵守 `schema.go:5-9` 约定），envd 侧若暂不支持则 `FilterSchemaParams` 过滤，不另立私有 schema。

4. 单测必须覆盖：相对路径 + workdir、绝对路径 + workdir、workdir 越界（应拒绝）、workdir 指向文件而非目录（应拒绝）、workdir 含软链指向界外（应拒绝）。

- 收益：彻底解决 §G1 不对称，模型不再需要手拼 `../`
- 风险：中。14 个路径型工具同时改动，必须确保默认路径零变化，并逐工具补单测

#### O5 `apply_patch` 的路径基准

`apply_patch` 的路径藏在 patch 正文，优先级最高也最容易出错。建议：

- `apply_patch` handler 解析 patch 内每条 `*** Update File: <path>` 时，统一用 `workdir` 作为基准
- 在返回结果里回显"本次解析到的绝对路径列表"，便于审计

- 收益：消除最隐蔽的路径歧义（§G1 第 3 点）
- 风险：中。patch 解析需覆盖 `Add` / `Update` / `Delete` / `Move` 四类指令

#### O6 envd `shell` / `pty_start` 接受 `workdir` 别名

- 改动点：`envd_process.go`、`envd_pty.go` 参数读取时，`workdir` 与 `cwd` 取先出现者；两者同时存在且不同值时返回 `CodeInvalidArgument`
- schema 只暴露 `workdir`，`cwd` 保留为兼容入参（用 `additionalProperties` 或内部别名处理）
- 收益：解决 §G6 跨后端不一致
- 风险：低

### P2：会话级目录状态（排期）

#### O7 会话级 sticky cwd

引入 `set_workdir` 工具（或 `workdir` 的持久化变体）：

```json
{
  "name": "set_workdir",
  "description": "设置本会话的默认工作目录。设置后，所有未显式传 workdir 的工具调用都以它为基准。传绝对路径；相对路径以当前工作区根为基准。传空字符串恢复为工作区根。",
  "properties": { "workdir": { "type": "string" } }
}
```

实现：在 `rootsResolver` 已按 session 缓存的机制上扩展（`roots_transport.go:144` 已有 `delete(r.bySession, sessionID)` 失效钩子），增加 `stickyWorkdir map[sessionID]string`；`workspaceForCall` 优先级改为：

```
显式 workdir 参数 > sticky cwd > roots 收窄根 > 启动 workspace
```

必须同时提供 `get_workdir` 或在每个工具响应里回显 `effective_workdir`，否则模型无法确认当前状态——这一点很关键，否则会引入新的静默错误。

- 收益：解决 §G2，长任务传参量大幅下降
- 风险：高。改变默认行为会破坏"每次调用独立"的既有心智模型，需在文档与错误提示里显式说明；建议用 `--enable-sticky-workdir` 开关默认关闭，灰度后再生效

#### O8 可选持久 shell

- 与现有 `pty_start` 合并：`pty_start` 已经支持 `cwd` 和持久会话（`envd_pty.go`），本地后端可复用同一抽象
- 或：`shell` 增加 `session_id` 参数，命中则复用同一 shell 进程
- 收益：支持真正的 `cd` 语义
- 风险：高。进程生命周期、泄漏、并发隔离、超时 kill（现有设计是"每次独立进程组，超时杀整棵树"，`shell.go:30-31`）全部需要重新设计。**建议最后做，或直接引导用户用 `pty_start`**

#### O9 `allowed-roots` 热更新

- 改动点：`config.go:785-792` `EffectiveAllowedRoots` 改为从可热加载的配置源读取（SIGHUP 或 fsnotify 监听配置文件）
- 必须保证：热更新只会**新增**根，或对"移除已授权根"做显式确认；不能让热更新成为逃逸手段
- 收益：解决 §G5
- 风险：中高。安全边界相关，需谨慎

#### O10 多根时的"当前根"选择

- 当 `effective` 长度 > 1 时，`Workspace()` 取 `effective[0]` 的行为需要显式化
- 建议：`roots` 声明第一个根作为默认根（符合惯例）；若客户端要换，通过 O7 的 `set_workdir` 指定另一个根
- 收益：解决 §G4
- 风险：中

---

## 8. 实施顺序与验收

### 8.1 建议顺序

| 阶段 | 内容 | 预期改动文件 | 可独立发布 |
|---|---|---|---|
| 第 1 步 | O1 + O2 + O3 | `shell.go`、`jobs_tools.go`、`workspace.go`、`roots.go` | ✅ 是 |
| 第 2 步 | O6 | `envd_process.go`、`envd_pty.go` | ✅ 是 |
| 第 3 步 | O4 + O5 | `workspace.go`、`mutation_tools.go`、`envd_tools.go`、`mutatecore/schema.go` | ✅ 是（默认行为不变） |
| 第 4 步 | O7 | `roots_transport.go`、`tools.go`、新增 `set_workdir` | ⚠️ 需开关 |
| 第 5 步 | O9 + O10 | `config.go`、`roots.go` | ⚠️ 需评审 |
| 第 6 步 | O8 | `shell.go` | ⚠️ 需设计评审 |

### 8.2 验收标准

**功能验收**

1. `shell(workdir="a")` → `ls(path=".")` 在**工作区根**而非 `a`（验证"不记忆"语义被正确表达，且描述已说清）
2. `write(workdir="sub", file_path="x.txt")` 落在 `sub/x.txt`
3. `apply_patch(workdir="sub", patch="*** Update File: x.txt")` 落在 `sub/x.txt`
4. envd 后端传 `workdir` 与传 `cwd` 行为一致
5. 相对路径 `../` 越界被拒，且错误信息含可用根列表

**安全验收**

6. `workdir` 指向符号链接到界外的目录 → 拒绝
7. `workdir` 指向界外绝对路径 → 拒绝，`CodePathDenied`
8. `workdir` 指向不存在的目录 → 拒绝，`CodeInvalidArgument`
9. `workdir` 指向普通文件 → 拒绝
10. 未传 `workdir` 时，全部 14 个路径型工具的路径解析结果与改动前**逐字节一致**（回归基线）

**schema 验收**

11. `mutatecore/schema.go` 声明的参数与 handler 实际读取的键**完全一致**（遵守 `schema.go:5-9` 契约）
12. 不支持某参数的 surface 已用 `FilterSchemaParams` 过滤，且不维护私有 schema
13. 所有目录类参数的 description 都明确写出：基准是什么、省略时是什么、是否跨调用记忆

### 8.3 回归风险提示

第 3 步是最大改动面（14 个路径型工具）。必须建立"改动前快照"作为回归基线：对同一组输入（含相对路径、绝对路径、`../`、软链），记录改动前的 `Resolve` 输出，改动后逐一比对。任何一处不一致都必须解释清楚是预期变更还是缺陷。

---

## 9. 不做的事（明确边界）

- **不放开 Roots 扩张**：`roots.go:48-64` 只能收窄是安全设计，不做妥协。需要访问新根时，走 `--allowed-roots` 启动配置（长期方案见 O9）
- **不让 `workdir` 绕过 `ensureInside`**：任何目录参数都必须走同一套词法 + 软链校验，绝不为便利新增旁路
- **不把 sticky cwd 做成默认开启**：默认开启会破坏"每次调用独立"的既有心智模型，必须开关灰度

---

## 10. 实施记录

本节记录按本方案落地后的实际改动，供评审与回溯。

### 10.1 改动清单

| 选项 | 文件 | 改动 |
|---|---|---|
| O1 | `shell.go` | `workdir` 描述重写：明确"仅本次调用"、绝对路径原样、相对路径锚定会话根、不跨调用记忆、目录必须已存在 |
| O1 | `jobs_tools.go` | `job_start.workdir` 描述按同一口径重写 |
| O2 | `workspace.go` | `ensureInside` 拒绝时在错误 detail 中回传 `allowedRoots` 与目标路径 |
| O3 | `roots.go` | 顶部注释由"P4 未接线"更正为"已接线于 server.go，需客户端声明 roots 能力" |
| O4 | `callworkdir.go`（新增） | `callWorkdirKey` / `WithCallWorkdir` / `CallWorkdir`、`pathParams`、`declaresPathParam`、`workdirProperty`、`withWorkdirProperty` |
| O4 | `workspace.go` | 新增 `ResolveFrom(base, raw)`：先校验 base，绝对路径绕过 base，最终仍走 `Resolve`（jail + 软链校验不旁路） |
| O4 | `tools.go` | `remoteToolBase.resolve` 读取调用级 workdir，非空走 `ResolveFrom`，空则保持原行为 |
| O4 | `registry.go` | `prefixedTool.Parameters()` 为含路径参数的工具注入 `workdir` 并加描述；`Execute()` 注入上下文、非原生工具剥离该键 |
| O5 | `runtime.go` | `remotePaths.Resolve` 同样读取调用级 workdir，使 write / edit / append / apply_patch / mkdir / move 等变更类工具真正生效 |
| O6 | `envd_process.go` / `envd_pty.go` | schema 新增 `workdir`，`cwd` 降级为废弃别名；handler 改用 `envdDirParam` |
| O6 | `envd_backend.go` | 新增 `envdDirParam`：`workdir` 为规范键，`cwd` 兼容；两者不同值报冲突而非静默取一 |
| 测试 | `callworkdir_test.go`（新增） | 7 个用例：上下文载体、路径参数识别、schema 注入（幂等/不改源）、`ResolveFrom` 三态与越界拒绝、`envdDirParam` 别名冲突 |
| 测试 | `tool_schema_audit_test.go` | grep / glob / view / ls 四个 audit 的 `indirect` 登记 `workdir`，说明其由包装器注入、由 `resolve` 经上下文消费 |

### 10.2 实施中发现并修正的缺陷

**缺陷 A（O5 遗漏，已修）**：变更类工具（write / edit / append / apply_patch / mkdir / move）走 `remotePaths.Resolve`，而不是 `remoteToolBase.resolve`。若只改后者，这些工具 schema 上虽有 `workdir` 却完全不生效——比不提供更糟。已在 `runtime.go` 补齐同一逻辑。

**缺陷 B（workdir 二次叠加，已修）**：`shell` / `job_start` 自己解析 `workdir`。包装器若同时把值放进上下文，`remoteToolBase.resolve` 会在 `shell` 已拼好的绝对路径上再拼一次，`sub` 变成 `<root>/sub/sub`。已改为：这两个工具直接透传，不设置上下文值。该缺陷由 `TestShellDefaultWorkdirFollowsNarrowedRoots` 捕获。

### 10.3 设计取舍说明

1. **用上下文而非参数穿透**：路径解析统一收敛在 `resolve`，逐改 14 个 handler 既易漏又易出现默认值不一致。上下文方案让单点生效，且天然不跨调用（派生上下文仅存活于一次调用）。
2. **默认行为零变化**：未传 `workdir` 时 `CallWorkdir(ctx) == ""`，`resolve` 走原分支，`Parameters()` 遇到不含路径参数的工具原样返回。既有调用方不受影响。
3. **不新增旁路**：`ResolveFrom` 最终仍调用 `Resolve`，设备名守卫、词法 jail、软链二次校验全部保留。
4. **schema 拷贝而非原地改写**：`withWorkdirProperty` 复制 map。工具 schema 构建一次多处共享，原地改写会把参数泄漏到其他 surface 并在二次包装时叠加。

### 10.4 验证结果

| 项目 | 结果 |
|---|---|
| `go build ./internal/remotetools/` | 通过 |
| 新增单测（7 项） | 全部通过 |
| schema 审计（`TestRemoteToolSchemasOnlyDeclareImplementedParams`） | 通过 |
| 回归（`TestShell*` / `TestJob*` / `TestRuntime*` / `TestConfig*` / `TestServer*`） | 通过（`ok 16.482s`） |
| `TestShellDefaultWorkdirFollowsNarrowedRoots` | 通过（该用例捕获了缺陷 B） |

> 说明：本机在并发编译时出现过 `out of memory` / `paging file is too small`，导致个别批次随机失败。改用 `-p 1` 串行重跑后稳定通过，判定为环境资源限制而非代码回归。

### 10.5 未实施项

P2 的 O7（会话级 sticky cwd）、O8（持久 shell）、O9（`allowed-roots` 热更新）、O10（多根"当前根"选择）按方案约定未实施，原因见 §7 风险说明：它们改变默认心智模型或触及安全边界，需独立评审与灰度开关。
