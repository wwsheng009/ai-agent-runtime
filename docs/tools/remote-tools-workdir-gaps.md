# remote-tools workdir 能力：缺口复核报告

- 日期：2026-10-30
- 复核对象：`backend/internal/remotetools`（P0+P1 实施后）
- 方法：MCP 端到端实测（10 个工具）+ 源码审计
- 关联方案：`docs/tools/remote-tools-workdir-plan.md`

## 1. 复核结论摘要

已确认生效（实测）：`ls`、`glob`、`grep`、`view`、`write`、`edit`、`append_write`、`apply_patch`、`shell`、`job_start`。

仍存在 8 项缺口，其中 2 项为高危（schema 承诺与实现不符：envd 后端、可选工具），2 项建议本轮修复。

| 编号 | 缺口 | 严重度 | 类型 |
|---|---|---|---|
| G1 | envd 后端声明 `workdir` 但完全不消费 | 高（潜在） | 契约不符 |
| G2 | `workdir` 不校验目录类型，与 shell/job_start 行为分裂 | 中 | 语义不一致 |
| G3 | 路径错误提示显示的 workdir 不是实际调用值 | 中 | 诊断误导 |
| G4 | 审计记录不含 `workdir`，无法追溯解析锚点 | 中 | 合规缺口 |
| G5 | `job_start` 句柄跨请求失效，影响 workdir 端到端验证 | 低 | 独立缺陷 |
| G6 | 路径参数注入依赖参数名白名单，缺回归守护 | 低 | 可维护性 |
| G7 | `workdir` 不缩小可访问范围，schema 未提示 | 低 | 文档/期望管理 |
| G8 | 可选工具（multiedit/download 等）声明 workdir 但内部不消费 | 高（潜在） | 契约不符 |

> 标「潜在」的两项在当前默认配置（`backend: local`、未启用 optional tools）下不触发，但都是 schema 承诺与实现不符，切换后端或启用可选工具即静默误导模型。

## 2. G1（高）envd 后端声明 workdir 但不生效

**证据链**

1. `envd_backend.go:934` — envd 工具经 `newPrefixedTool` 注册，与其他工具同一包装层。
2. `registry.go:60-72` — `prefixedTool.Parameters()` 对任何含路径参数的工具注入 `workdir`。
3. `envd_backend.go:719-723` — `envdTool.Execute` 直接把 `params` 交给 `spec.call`，**从不读取 `CallWorkdir(ctx)`**。
4. `envd_backend.go:267-286` — `lexicalResolve` 的锚点恒为 `be.workspace`。
5. `envd_backend.go:1008` — `envdView` 用 `envdStringParam(params,"file_path","path")`，未见 workdir。

**后果**：模型在 envd 后端看到 `workdir` 参数，传入后被静默忽略。这与 P1 的初衷（O6 已在 envd 层加 `envdDirParam` 处理 workdir/cwd 别名）形成悖论——参数被解析出来，但没有接进路径解析。

**当前是否触发**：`config.go:503-504` 中 `Backend` 为空时默认 `BackendLocal`，本机部署因此走 local 后端，G1 目前是**潜在缺陷**，只在显式 `backend: envd` 时激活。定级为「高」是因为它是契约撒谎（schema 承诺能力而实现不提供），一旦启用 envd 即静默误导模型，且不会被现有测试发现。

**修复方向（二选一，须一致）**

- 方案 A（推荐）：让 envd 路径解析也消费 workdir。给 `envdBackend` 增加 `resolvePathFrom(ctx, raw)`，在 `lexicalResolve` 前用 `CallWorkdir(ctx)` 改锚点；`workspace` 仍作为 jail 根不变。
- 方案 B：`prefixedTool.Parameters()` 对 envd 后端不注入 `workdir`（按 backend 能力开关），做到「不撒谎」。

选择 A 更符合「一个工具名在两种后端语义一致」的既有设计原则（`envd_backend.go:814-816` 注释即为此意）。

## 3. G2（中）workdir 目录类型校验缺失

**证据链**

- `workspace.go:208-223` — `ResolveFrom` 仅调用 `Resolve`，无 `IsDir` 校验。
- 实测：`ls(workdir=backend/internal/remotetools/callworkdir.go)` → 成功返回该文件 3926 bytes 元信息。
- 实测：`glob(workdir=...callworkdir.go)` → 返回 `callworkdir.go`。
- 对照：`shell(workdir=...callworkdir.go)` → `INVALID_ARGUMENT: workdir is not a directory`。
- 对照：`job_start(workdir=...callworkdir.go)` → 同样拒绝。

**后果**：同一 `workdir` 参数，路径类工具与命令类工具接受度不同。模型据一类工具得到的经验在另一类工具上失效。

**修复方向**：在 `ResolveFrom` 内对 `baseAbs` 做一次 `os.Stat` + `IsDir` 校验，失败返回 `INVALID_ARGUMENT: workdir is not a directory`，与 shell/job_start 文案对齐。

## 4. G3（中）错误提示中的 workdir 显示不实

**实测**：`view(file_path=callworkdir.go, workdir=backend/internal/remotetools/callworkdir.go)`

实际报错：

```
路径不存在: callworkdir.go
提示: 文件或目录不存在，请先确认当前 workdir=E:\projects\ai\ai-agent-runtime；可能的路径候选: ...
```

**问题**：提示中的 `workdir` 是 session workspace root，而非本次调用传入的 `backend/internal/remotetools/callworkdir.go`。提示文案来自 `backend/internal/executor/command_path_hints.go:120-127` 的 `formatPathRelatedHint`，其 `workdir` 入参在 remote 路径上取的是 workspace root。

**后果**：模型会据错误提示修正到错误方向，形成二次无效尝试。

**修复方向**：remote 侧构造该 hint 时传入 `CallWorkdir(ctx)`（为空再回落 workspace root）。

## 5. G4（中）审计不含 workdir

**证据**：`audit.go:17-48` 的 `AuditRecord` 无 workdir 字段；`registry.go:899-911` 组装记录时也不填。`Paths` 仅取 `mutated_paths`。

**后果**：事后无法从审计还原「这次写入是在哪个目录基准下解析的」。对 G7（workdir 不缩小范围）这一事实，审计缺失会放大争议。

**修复方向**：`AuditRecord` 增加 `Workdir string \`json:"workdir,omitempty"\``，在 `auditTool.Execute` 中取 `params["workdir"]` 填入。

**注意包装顺序**：`server.go:179` 的 `newAuditTool` 包裹的是 `registry.List()` 的产物，即 `auditTool` 在 `prefixedTool` **外层**；而 `CallWorkdir(ctx)` 是在 `prefixedTool.Execute` 内部才写入的。因此 audit 层读不到上下文值，**必须从原始 `params` 取**（auditTool 在剥离前看到的是含 workdir 的 map）。

## 6. G5（已修复）job_start 句柄跨请求失效

**实测**：`job_start` 返回 `job_cf77...`，紧接着 `job_output` → `NOT_FOUND: job not found`；`job_list` → `(no jobs)`。

**定位**：`jobs_tools.go:77-80` 的 owner 比对失败会返回完全相同的文案。即 job 存在，但 `RemoteJobOwner` 在两次请求间解析出不同 owner（SessionID 不稳定）。

**影响**：不影响 workdir 正确性（已用落点文件证明 `job_start` cwd 正确锚定到 `backend/internal/remotetools`），但使 workdir 无法通过 job 回读验证，且 job_* 工具族实际不可用。

**建议**：独立排查 owner 来源（`RequestInfoFromContext` vs `toolctx.SessionID` 在 stdio/HTTP stateless 下的取值），与本 workdir 工作解耦。

**修复（2026-10-30）**：HTTP transport 改为有状态会话（`Mcp-Session-Id`，`SessionTimeout: 30m`），客户端在同一连接内回传后 owner 稳定；owner mismatch 追加 stderr 诊断（模型侧仍保持 not-found，不泄露他会话 job）；新增 `TestHTTPJobHandleSurvivesAcrossCalls` 覆盖 `job_start` → `job_output`。**定性修正**：原标"低"不成立——该缺陷会让 `job_*` 工具族整体不可用，并掩盖队列饱和。

## 7. G6（低）路径参数白名单缺守护

**证据**：`callworkdir.go:45` — `pathParams = []string{"path","paths","files","file_path","source","destination","patch"}`。注入与否完全由参数名决定。

**风险**：新增工具若用 `target`、`dest`、`new_path`、`from`/`to` 等命名，会被静默漏注入；`tool_schema_audit_test.go` 只登记现存参数，无法发现新遗漏。

**修复方向**：在 audit test 中增加一条「工具名 → 是否应含 workdir」的期望表，新增工具时强制表态。

## 8. G7（低）workdir 不缩小可访问范围

**实测**：`write(workdir=backend/internal/remotetools, file_path=../../../configs/_probe_escape.txt)` → 成功写出到 `configs/`。

**定性**：这符合设计（workdir 只移动相对锚点，jail 仍由 allowedRoots 决定），`workspace.go:201-207` 注释已说明。但 `workdirProperty()` 的 description 未提示这一点。

**建议**：description 补一句「workdir 只改变相对路径的基准，不缩小可访问范围；越界仍由 allowed roots 拒绝」。

## 9. G8（高）可选工具声明 workdir 但不消费

**证据链**

1. `registry.go:813-821` — `buildOptionalTools` 产物与内置工具一起进入 `candidates`，统一经 `newPrefixedTool` 包装，因此 `multiedit`、`download`、`fetch` 等同样获得 `workdir` schema 注入（它们的 `pathParams` 含 `file_path`）。
2. `optional_tools.go:42/49` — `multiedit` 与 `download` 的 `pathParams: []string{"file_path"}`。
3. `optional_tools.go:215-237` — `remoteOptionalTool.Execute` 只调用 `workspaceForCall(ctx, t.workspace)`，**从不读取 `CallWorkdir(ctx)`**；预检用 `effective.Resolve(raw)`，锚点为 workspace root。
4. `registry.go:98-104` — `prefixedTool.Execute` 对非原生工具会把 `workdir` 从 map 中剥离再委派，只把值放入上下文。

**后果**：与 G1 同类。模型对 `multiedit`/`download` 传 `workdir` 时，schema 接受、预检按 workspace root 校验、实际写入/下载落点也在 workspace root —— 静默忽略，且不报错。

**当前是否触发**：`optional_tools.go:119-139` 的 `optionalToolEnabled` 要求运维方在 `optionalTools.enabled` 或 `tools` 中显式命名，且必须 `Backend == local`。当前部署暴露的 14 个工具中不含 `multiedit`/`download`，G8 目前是**潜在缺陷**。

**与 G1 的关系**：二者互斥。可选工具只在 `BackendLocal` 下启用（`optional_tools.go:123-125`），而 G1 只在 `backend: envd` 时激活。也就是说，任一时刻只有一个「schema 撒谎」缺陷可能真实发生；但两个都必须修，否则切换后端或启用可选工具时立即暴露。

**附带缺陷**：`optional_tools.go:222` 用 `params[key].(string)` 单值断言，数组型参数（如 `files`）会被静默跳过，预检不生效。

**修复方向**：把 `effective` 的锚点改为 `ResolveFrom(CallWorkdir(ctx), ...)`；同时预检循环支持 `[]string` / `[]interface{}`。更彻底的做法是把「路径预检」下沉到与 local 后端共享的解析层，避免每个包装器各自实现。

## 10. 已实测通过的行为（回归基线）

| 场景 | 结果 |
|---|---|
| 相对 workdir 锚定 | `ls/glob/view/grep/write/edit/append_write/apply_patch` 均命中 `backend/internal/remotetools` |
| 绝对 workdir | `shell` cwd = `E:/projects/ai/ai-agent-runtime/backend` |
| 绝对 file_path 优先 | 绝对路径 + 任意 workdir 正常读取 |
| 无粘性 | 不传 workdir 的下一次调用回到 workspace root |
| 越界拒绝 | `..\..\..\..\..\Windows` → `PATH_DENIED` 并回传 allowedRoots |
| 不存在目录 | 路径类报「路径不存在」；shell/job_start 报 `workdir is not a directory` |
| 空 workdir | 回落 workspace root |
| `..` 中间跳转 | `docs/../backend/internal/remotetools` 正常归一 |

## 11. 建议实施顺序

1. G1 — 消除 schema 撒谎（envd 后端），优先级最高。
2. G8 — 同一性质，且 standard profile 下即可触发，与 G1 一并处理。
3. G2 + G3 — 行为与诊断对齐，改动集中（`workspace.go` / hint 构造）。
4. G4 — 审计补字段，与 G1/G8 同批便于验证。
5. G6 + G7 — 测试守护与文档措辞。
6. G5 — 独立议题，不阻塞本工作。

P2 原定项（O7 sticky cwd、O8 持久 shell、O9 allowed-roots 热更新、O10 多根当前根）仍不在本轮范围。

## 12. 修复状态（本轮已实施）

| 编号 | 状态 | 改动位置 | 做法 |
|---|---|---|---|
| G1 | 已修 | `envd_backend.go` | 新增 `anchorCallWorkdir(ctx, raw)`：读 `CallWorkdir`，`lexicalResolve` 夹在 jail 内，再在目标侧 `test -d` 确认真是目录；`resolvePath` 与 `resolveMutationPath` 均先锚定再解析。绝对路径不受影响。 |
| G2 | 已修 | `workspace.go` | `ResolveFrom` 在 base 解析后增加 `isDirectory` 校验，失败返回 `INVALID_ARGUMENT: workdir is not a directory`，与 shell / job_start 文案对齐。 |
| G3 | 已修 | `runtime.go` | `PathHint` 先用 `ResolveFrom(workdir, "")` 求出本次调用的实际基准并传入 hint 构造；workdir 失效时回落 workspace root。 |
| G4 | 已修 | `audit.go` / `registry.go` | `AuditRecord` 增加 `Workdir`；`auditTool` 从**原始参数**读取（该层在 `prefixedTool` 外层，上下文里还没有值）。 |
| G5 | 定位为外部限制 | `jobs_tools.go` | 未放宽归属校验。NOT_FOUND 文案补充「任务只存在于启动它的服务进程，重启后不保留」，让「重启丢任务」与「id 写错」可区分；仍不暴露他人任务是否存在。 |
| G6 | 已修 | `workdir_coverage_test.go`（新增） | 对全注册表面断言：带路径参数的工具必须声明 `workdir`，无路径参数的工具不得声明（shell / job_start 例外，它们自己解析）。新增工具漏注入会直接失败。 |
| G7 | 已修 | `callworkdir.go` | description 补明「workdir 只移动相对路径基准，不缩小可访问范围」与「传文件会被拒绝」。 |
| G8 | 已修 | `optional_tools.go` | 新增 `pathValues` 支持 string / []string / []interface{}；有 workdir 时把路径改写为 `ResolveFrom` 后的绝对路径再交给内层工具，使 workdir 真正改变落点。 |

### 新增测试

- `workdir_coverage_test.go` — 全表面 workdir 声明守护（G6）。
- `workdir_resolve_test.go` — `ResolveFrom` 目录校验（相对/绝对写法、空 raw）、`PathHint` 基准锚定（候选只在 workdir 内可见）。

### 验证结果

- `go build ./internal/remotetools/` → 通过
- `go vet ./internal/remotetools/` → 通过
- 定向用例（`TestWithWorkdirProperty`、`TestResolveFromRequiresDirectoryWorkdir`、`TestCallWorkdirAnchorsHintBase`、`TestEveryPathToolDeclaresWorkdir`）→ 4/4 通过
- `go test ./internal/remotetools/ -p 1` → `ok 116.750s`，EXIT=0

### 仍未做端到端验证的部分

G1 与 G8 的修复有编译与单元覆盖，但没有运行时端到端证据：当前部署为 `backend: local` 且未启用 optional tools，两条路径无法在默认配置下触发。上线前建议：

1. 以 `backend: envd` 起一个实例，对 `remote_dev__view` 传相对路径 + `workdir`，确认读取落点随之改变。
2. 在 `optionalTools.enabled` 中启用 `multiedit`，传 `files` 数组 + `workdir`，确认多文件编辑均落在指定目录。

### 回归时注意

`TestWithWorkdirProperty` 断言 description 同时含 `not remembered`、`must already exist`、`workspace root` 三个短语。修改文案时这三个词必须保留，否则该用例会失败（本轮已踩过一次）。
