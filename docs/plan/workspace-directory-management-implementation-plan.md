# Workspace 工作目录管理实施方案（目录增加/删除 + 目录内会话管理）

> 状态：**已实施**（P0–P4 已落地，并含三轮实施后修复）；§14 为参考实现借鉴的**增强候选（尚未实施）**
> 日期：2026-09-10（初稿）／2026-09-15（章节重编号 + §14 增强候选）
> 涉及端：frontend（React + Vite）、backend（Go runtime-server，:8101）
> 关联页面：`http://localhost:8101/workspace/chats/new`

## 0. 变更记录与阅读导航

| 日期 | 变更 | 章节 |
| --- | --- | --- |
| 2026-09-10 | 方案初稿：需求、现状分析、详细设计、实施计划、兼容/验收/风险、自审记录 | §1–§10 |
| 2026-09-10 | 实施记录（一）：会话执行 CWD 接线与绑定防漂移 | §11 |
| 2026-09-10 | 实施记录（二）：文件工具与 `aicli_exec` 的会话根感知 | §12 |
| 2026-09-10 | 实施记录（三）：第三轮实施后审查（M1/M2/m4） | §13 |
| 2026-09-15 | 修正重复章节号（原实施记录误用「6./7./8.」，与 §6–§8 冲突）；新增参考实现借鉴增强候选 | §11–§14 |
| 2026-09-15 | 侧栏「工作目录 / 会话」分区合并落地（Phase 0–2 + Phase 4 清理）：分区一分为二造成的「同一会话两行」重复问题收敛；§14 增强候选的落点按合并后组件结构更新 | §14、`workspace-sidebar-directory-session-merge-plan.md` §10 |

**阅读导航**

- 需求与设计：§1 背景与目标 → §2 现状与差距 → §3 目标方案 → §4 详细设计
- 交付与状态：§5 实施计划 → §7 测试与验收 → §11/§12/§13 实施记录（含已修缺陷与已知边界）
- 后续增强：§10-F6（方案自审遗留）+ §14（参考实现借鉴项，含优先级、落点与验收）
- 排障速查：§8 风险与回滚、§9 关键代码位置索引

---

## 1. 背景与目标

### 1.1 背景

Workspace 页面（`/workspace/chats/new`）当前按"用户 → 工作目录 → 会话"三级结构展示运行时会话，
但这个"工作目录"层级**不是用户可管理的实体**：它完全由会话元数据
`metadata.context.workspace_path` 在前端派生（见 `workspace-sidebar-shared.ts` 的
`groupRuntimeSessionsByDirectory`）。由此导致：

1. **无法增加目录**：用户不能在 workspace 上主动登记一个服务端真实文件目录
   （例如 `E:\projects\demo`），再在该目录下发起会话；
2. **无法删除目录**：目录分组随会话存在而存在，最后一个会话被删后目录分组即消失，
   用户无法固定/移除一个目录条目；
3. **无法在指定目录下新建会话**：新建会话（`NEW_THREAD_ID = "new"`）是纯前端草稿，
   运行时会话要到第一轮 agent chat 才由后端 `GetOrCreate` 物化，且创建时
   `POST /api/runtime/sessions` 只接受 `user_id/title`，**不接受 workspace_path**，
   因此新会话无法预先绑定到某个目录；
4. **无法重命名会话**：后端 `PATCH /api/runtime/sessions/{id}` 已支持 `title` 更新，
   但前端 `api/runtime/sessions.ts` 未封装该调用，侧边栏也没有重命名入口。

### 1.2 目标

在 workspace 上提供"工作目录（Working Directory）"的一等公民管理能力：

| 能力 | 说明 |
| --- | --- |
| 增加目录 | 用户输入服务端真实目录路径（可选别名），后端校验路径存在且为目录后登记 |
| 删除目录 | 从注册表移除目录条目；**不删除服务器文件**，已有关联会话不受影响 |
| 目录下新建会话 | 在某个目录节点上"新建会话"，会话创建即绑定 `workspace_path`，后续 agent chat 自动以该目录为工作目录 |
| 会话重命名 | 侧边栏内联重命名会话标题（走既有 `PATCH /sessions/{id}` 的 `title` 字段） |
| 目录别名重命名 | 修改目录展示名（不改真实路径） |

非目标（本期不做）：
- 目录/文件浏览器（列出目录下子文件）；
- 目录的移动端专属交互（沿用现有响应式侧边栏）；
- 多用户目录共享/权限体系（沿用单机 runtime-server 信任模型，见 §6）。

---

## 2. 现状架构分析

### 2.1 前端架构

#### 2.1.1 路由与页面

`frontend/src/App.tsx`：

- `/workspace`、`/workspace/chats`、`/workspace/sessions` → 重定向到默认路由
  `/workspace/chats/new`（`defaultWorkspaceRoute`）；
- `/workspace/chats/new` 命中 `/workspace/chats/:threadId`（`threadId = "new"`）→ `WorkspacePage`；
- `/workspace/sessions/:sessionId`、`/workspace/chats/:threadId` → `WorkspacePage`。

`pages/workspace-page.tsx` 渲染 `WorkspaceSidebar`（左侧栏）+ workspace shell（会话主区）。

#### 2.1.2 侧边栏与目录分组（现状核心）

`frontend/src/components/workspace/workspace-sidebar.tsx`：

- 三个可折叠分区：`chats`（本地草稿线程）、`sessions`（运行时会话）、`runtime`（团队/状态）；
- `sessions` 分区内部按 **用户 → 目录 → 会话** 三级渲染：
  - 用户层：`sessionUserMenuItems`（来自 `GET /api/runtime/sessions/users`）；
  - 目录层：`sessionDirectoryGroups = groupRuntimeSessionsByDirectory(runtimeSessions)`；
  - 会话层：目录组内遍历 `group.sessions` 渲染会话条目（标题取
    `thread.title || session.metadata.title || session.id`）。

`frontend/src/components/workspace/workspace-sidebar-shared.ts`（目录分组的真正来源）：

- `resolveRuntimeSessionDirectory(session)`：从 `session.metadata.context` 依次读取
  `workspace_path / workspacePath / cwd / workdir / working_dir / profile_root / profileRoot / aicli_profile_root`；
- 无路径的会话落入 `__runtime-session-directory-unknown__` 组，展示为 "Unscoped sessions"；
- `normalizeRuntimeDirectoryPath`：统一 `\` → `/`、去尾部斜杠；分组 key 用小写全路径；
  label 取路径 basename；
- 排序：目录组按最新会话 `updatedAt` 降序，组内会话同样按更新时间降序。

**关键结论：目录是"派生态"，不是"注册态"。** 没有任何持久化的目录清单，也没有目录的
增删改 API。

#### 2.1.3 会话创建链路（现状）

- `hooks/workspace/use-workspace-thread-selection.ts`：`NEW_THREAD_ID = "new"`，
  `createDraftThread()` 生成纯前端草稿；URL 规范化由 `buildWorkspaceThreadPath` 完成
  （草稿 → `/workspace/chats/new`，绑定会话 → `/workspace/sessions/{id}`）；
- 运行时会话在**第一轮 agent chat** 时由后端 `GetOrCreate` 物化
  （`handler.go` 的 `resolveSessionForRequest` → `sessionManager.GetOrCreate(ctx, userID, requestedSessionID)`）；
- `RuntimeCreateSessionRequest = { title?, user_id? }`（`types/runtime.ts`），
  `createRuntimeSession()`（`api/runtime/sessions.ts`）目前仅被 runtime-teams 调度使用，
  普通新建会话不预创建会话记录；
- `AgentChatRequest` **已有** `workspace_path?: string` 字段，但 workspace 前端发送
  agent chat 时并未传它——后端使用请求里的值或空值（见 2.2.3）。

#### 2.1.4 前端 API 层

`frontend/src/api/runtime/sessions.ts` 已封装：`createRuntimeSession`、`listRuntimeSessions`、
`getRuntimeSession`、history/checkpoints/turns/backtrack/plan 等。
**缺失**：`updateRuntimeSession`（重命名需要）、`deleteRuntimeSession`（后端已有 DELETE 路由），
以及整个工作目录 API 模块。

i18n：`frontend/src/i18n/resources/zh-CN.ts` / `en-US.ts`，workspace 命名空间下已有
`sidebar.*` 键，需要新增 `sidebar.directories.*` 与会话重命名相关键。

### 2.2 后端架构

#### 2.2.1 路由注册

`backend/internal/api/skills/handler.go`（`registerRoutes` 内，约 L618 起）：

```go
runtimeRouter := router.PathPrefix("/api/runtime").Subrouter()
...
// Sessions（L702-748）
runtimeRouter.HandleFunc("/sessions", h.ListSessions).Methods(GET)
runtimeRouter.HandleFunc("/sessions", h.CreateSession).Methods(POST)
runtimeRouter.HandleFunc("/sessions/{id}", h.UpdateSession).Methods(PATCH)
runtimeRouter.HandleFunc("/sessions/{id}", h.DeleteSession).Methods(DELETE)
```

前端静态资源由 `internal/webui`（`assets.go` + `dist/`）托管，runtime-server 监听 :8101
（`start-web.ps1`）。

#### 2.2.2 会话模型与存储

`backend/internal/chat/session.go`：

- `SessionMetadata.Context map[string]interface{}`：会话级上下文 KV，`SetContext(key, value)` 写入；
- 上下文键常量集中在 `backend/internal/sessionmeta/sessionmeta.go`：
  `WorkspacePath = "workspace_path"`（L24）、`ProfileRoot`、`Client` 等；
- 存储为共享 `session_history.sqlite`（并发 aicli CLI 进程共用），所有会话读写必须走
  `sessionStoreQueryContext(r)`（5s 截止 → 503 快速失败），新端点必须遵守同一约定。

#### 2.2.3 会话 CRUD 现状

`handler.go`：

- `CreateSession`（L2249-2290）：body `{user_id, title}` → `sessionManager.CreateSession(ctx, userID)`，
  可选 `UpdateTitle`。**不支持 workspace_path**；
- `ListSessions`（L2293-2320）：`SearchSessions(ctx, &chat.SessionSearchOptions{})` 返回全部会话
  （含 `metadata.context`），前端据此做目录分组；
- `UpdateSession`（L2516-2580）：支持 `title / state / tags_add / tags_remove / context`
  （`context` 逐键 `session.SetContext`）——**重命名会话的后端能力已就绪**；
- `DeleteSession`（L2348-2374）：删除会话记录。

#### 2.2.4 workspace_path 在 agent 链路中的语义

`AgentChat`（`handler.go` L1430 起）：

```go
workspacePath := strings.TrimSpace(req.WorkspacePath)
...
agentContext := map[string]interface{}{ "workspace_path": workspacePath, ... }
...
if workspacePath != "" {
    agentConfig.Options["workspace_path"] = workspacePath   // L1628-1633
}
```

- `workspace_path` 驱动 agent 的 cwd、环境上下文冻结（`sessionmeta.EnvironmentContextBlock`）、
  harness permissions/memory/plugins 的 workspace 根（`RuntimeHarness*Response.workspace_path`）；
- `buildSessionActor`（测试 `handler_test.go:2660` 佐证）会读取会话上下文中的
  `sessionmeta.WorkspacePath` 作为会话 actor 的工作目录；
- 即：**会话上下文里的 `workspace_path` 是"会话绑定目录"的既定事实来源**，
  前端目录分组也读同一字段。新方案应复用该字段，而不是另起炉灶。

#### 2.2.5 持久化注册表先例

`backend/internal/foldertrust/store.go`：

- YAML 注册表 `~/.aicli/trusted_folders.yaml`（`AICLI_HOME` 优先，否则用户主目录）；
- `Store{mu, doc, path}` + Load/Save 模式、路径规范化、幂等写盘。

工作目录注册表可直接复刻该模式（新包 `internal/workspaceregistry`），
存储文件建议 `~/.aicli/workspace_directories.yaml`。

### 2.3 差距分析（Gap）

| # | 差距 | 现状 | 目标 |
| --- | --- | --- | --- |
| G1 | 目录注册表 | 无（前端派生） | `workspaceregistry` YAML 注册表 + CRUD API |
| G2 | 路径校验 | 无（信任会话元数据） | 新增目录时 `os.Stat` 校验存在且为目录、绝对路径、去重 |
| G3 | 新会话绑定目录 | `CreateSession` 不收 workspace_path | 请求扩展 `workspace_path`（或 `directory_id`），落 `sessionmeta.WorkspacePath` |
| G4 | agent chat 目录回填 | `req.WorkspacePath` 为空时无会话上下文回退 | 空时回退会话上下文的 `workspace_path` 并回写 |
| G5 | 会话重命名 UI/API 封装 | 后端 PATCH 支持，前端无封装无 UI | `updateRuntimeSession` + 侧边栏内联重命名 |
| G6 | 目录删除语义 | 不存在 | 仅删注册表条目；文件系统与会话不受影响 |
| G7 | 空目录展示 | 目录随会话消失 | 注册目录恒显示（0 会话也显示） |

---

## 3. 目标方案总览

### 3.1 数据流总览

```
┌─────────────────────────── frontend (workspace) ───────────────────────────┐
│                                                                            │
│  WorkspaceSidebar                                                          │
│  ├─ [工作目录] 分区（新增，注册目录恒显示）                                    │
│  │    ├─ + 添加目录 ──► POST /api/runtime/workspace-directories             │
│  │    ├─ 目录行：新会话 / 重命名别名 / 删除                                    │
│  │    │     └─ 新会话 ──► POST /api/runtime/sessions {workspace_path}       │
│  │    │                    └─► navigate /workspace/sessions/{id}           │
│  │    └─ 目录展开：该目录下的会话列表（合并派生分组）                             │
│  ├─ 会话条目：内联重命名 ──► PATCH /api/runtime/sessions/{id} {title}        │
│  └─ 会话主区：agent chat（session 已绑定 workspace_path，无需重复传）           │
│                                                                            │
└────────────────────────────────────────────────────────────────────────────┘
              │ fetch                                    │ SSE/stream 不变
┌─────────────▼────────────── backend (runtime-server :8101) ────────────────┐
│  /api/runtime/workspace-directories   (新增 4 个端点)                        │
│        └─ internal/workspaceregistry (YAML: ~/.aicli/workspace_directories)│
│  POST /api/runtime/sessions           (扩展 workspace_path / directory_id) │
│        └─ session.SetContext(sessionmeta.WorkspacePath, path)              │
│  POST /api/agent/chat                 (空 workspace_path 时回退会话上下文)     │
│        └─ agentConfig.Options["workspace_path"]（既有链路）                  │
└────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 核心设计决策

| 决策点 | 选择 | 理由 |
| --- | --- | --- |
| 目录事实来源 | 新增注册表（注册态）+ 会话上下文（绑定态）双轨 | 注册表回答"有哪些目录"；`sessionmeta.WorkspacePath` 回答"会话在哪个目录"，二者以路径字符串关联 |
| 注册表存储 | YAML 文件（复刻 foldertrust） | 与既有 `trusted_folders.yaml` 同构；无 schema 迁移；并发写低频 |
| 目录标识 | `id = sha1(normalizedLowerPath)[:12]` | 同一路径重复添加幂等；Windows 大小写不敏感去重 |
| 会话绑定字段 | 复用 `sessionmeta.WorkspacePath` | 前端分组、`buildSessionActor`、agent 链路已消费该字段，零迁移 |
| 新建会话方式 | 显式 `POST /sessions {workspace_path}` 后跳转 `/workspace/sessions/{id}` | 会话立即可见、可重命名，避免"草稿发首条消息才物化"的不可见期 |
| 删除目录语义 | 仅删注册表条目 | 绝不触碰文件系统；会话保留自身 `workspace_path`，落入"未注册目录"派生分组继续展示 |
| 重命名会话 | 复用 `PATCH /sessions/{id}` `title` | 后端已支持（`UpdateTitle` + `titleSource=manual`），只补前端 |

---

## 4. 详细设计

### 4.1 后端：工作目录注册表存储（新包 `internal/workspaceregistry`）

新文件：`backend/internal/workspaceregistry/store.go`（+ `store_test.go`）。

```go
// DirectoryRecord 注册表中的一条工作目录
type DirectoryRecord struct {
    ID         string `yaml:"id"  json:"id"`          // sha1(lower(path))[:12]
    Path       string `yaml:"path" json:"path"`       // 规范化后的绝对路径（保留原生分隔符）
    Name       string `yaml:"name,omitempty" json:"name,omitempty"` // 别名，空则前端用 basename
    CreatedAt  int64  `yaml:"created_at" json:"created_at"`   // unix seconds
    LastUsedAt int64  `yaml:"last_used_at,omitempty" json:"last_used_at,omitempty"`
}

type storeDocument struct {
    Version     int              `yaml:"version,omitempty"`
    Directories []DirectoryRecord `yaml:"directories"`
}

type Store struct {
    mu  sync.Mutex
    doc storeDocument
    path string // 空 => no-home，fail closed（与 foldertrust 一致）
}

func DefaultStorePath() string            // ~/.aicli/workspace_directories.yaml，AICLI_HOME 优先
func Load() *Store / func LoadFrom(path string) *Store
func (s *Store) List() []DirectoryRecord
func (s *Store) Add(path, name string) (DirectoryRecord, error)   // 校验+去重+持久化
func (s *Store) Rename(id, name string) (DirectoryRecord, error)
func (s *Store) Remove(id string) (DirectoryRecord, bool, error)
func (s *Store) Touch(id string) error           // 会话创建成功时刷新 LastUsedAt（尽力而为）
func (s *Store) Get(id string) (DirectoryRecord, bool)
```

路径规范化规则（与前端 `normalizeRuntimeDirectoryPath` 对齐）：

1. `strings.TrimSpace`；拒绝空串；
2. 必须是绝对路径（`filepath.IsAbs`；UNC 路径 `\\server\share` 允许）；
3. `filepath.Clean` 规范化；Windows 下比较用 `strings.ToLower`；
4. `os.Stat` 校验存在且 `IsDir()`（不存在 → `ErrDirectoryNotFound`；是文件 → `ErrNotDirectory`）；
5. 去重：规范化 key（Windows 大小写不敏感）已存在 → 返回既有记录（幂等 200/201 语义，
   handler 层返回 200 + `existing: true`）。

**注意**：`Add` 只登记，不 `MkdirAll`（避免误创建目录）；"目录必须已存在"是产品语义。

### 4.2 后端：HTTP API

路由注册位置：`internal/api/skills/handler.go` `registerRoutes`（Sessions 段之后）：

```go
// Workspace directories（工作目录注册表）
runtimeRouter.HandleFunc("/workspace-directories", h.ListWorkspaceDirectories).Methods(GET)
runtimeRouter.HandleFunc("/workspace-directories", h.CreateWorkspaceDirectory).Methods(POST)
runtimeRouter.HandleFunc("/workspace-directories/{id}", h.UpdateWorkspaceDirectory).Methods(PATCH)
runtimeRouter.HandleFunc("/workspace-directories/{id}", h.DeleteWorkspaceDirectory).Methods(DELETE)
```

#### 4.2.1 `GET /api/runtime/workspace-directories`

响应 200：

```json
{
  "directories": [
    {
      "id": "1a2b3c4d5e6f",
      "path": "E:\\projects\\demo",
      "name": "demo",
      "created_at": 1725900000,
      "last_used_at": 1725999999,
      "exists": true,
      "session_count": 3
    }
  ],
  "count": 1
}
```

- `exists`：响应时实时 `os.Stat`（目录可能被外部删除；`false` 时前端置灰并提示）；
- `session_count`（**可选字段，服务端尽力统计**）：遍历会话存储统计绑定路径相等的
  会话数。注意：workspace 前端本来就会全量拉取 `GET /sessions`，若目录列表再触发
  一次全量扫描会造成双份开销——因此**前端展示以 `mergeDirectoryGroups` 本地聚合为准**，
  本字段仅保留给非 workspace 调用方（如 CLI 脚本），允许为 0/缺省。

#### 4.2.2 `POST /api/runtime/workspace-directories`

请求：`{ "path": "E:\\projects\\demo", "name": "demo" }`（`name` 可选）。

- 校验失败 → 400（`ErrValidationFailed`，message 指明"目录不存在/不是目录/路径为空"）；
- 成功 → 201 `{ "directory": {...}, "existing": false }`；重复添加 → 200 + `existing: true`。

#### 4.2.3 `PATCH /api/runtime/workspace-directories/{id}`

请求：`{ "name": "新别名" }`。仅允许改 `name`（**路径不可变**：改路径等于删了重加，
避免会话绑定漂移）。404：未知 id。

#### 4.2.4 `DELETE /api/runtime/workspace-directories/{id}`

响应 200：`{ "deleted": true, "id": "...", "sessions_affected": 3 }`
（`sessions_affected` 仅提示用途，**不**级联改会话）。

并发与存储约定：

- 所有 handler 内的会话扫描沿用 `sessionStoreQueryContext(r)`（5s 截止 → 503）；
- 注册表自身写入低频，`Store` 内部 `sync.Mutex` + 临时文件原子替换写盘
  （与 foldertrust 的 Save 策略一致）。

### 4.3 后端：会话与目录绑定

#### 4.3.1 `CreateSession` 扩展（`handler.go` L2249-2290）

请求体新增字段：

```go
var req struct {
    UserID        string `json:"user_id,omitempty"`
    Title         string `json:"title,omitempty"`
    WorkspacePath string `json:"workspace_path,omitempty"` // 新增
    DirectoryID   string `json:"directory_id,omitempty"`   // 新增（可选，二选一）
}
```

处理逻辑（在 `CreateSession` 成功后、写回前）：

1. `DirectoryID` 非空 → 从注册表取 `Path`（未知 id → 400）；
2. 否则用 `WorkspacePath`：`workspaceregistry.ValidatePath(path)`（绝对路径 + 存在 + 是目录；
   校验失败 → 400，错误信息透出给前端表单）；
3. `session.SetContext(sessionmeta.WorkspacePath, normalizedPath)`；
4. `h.sessionManager.Update(ctx, session)`；
5. `registry.Touch(directoryID)`（尽力而为，失败仅记日志）；若调用方只传了
   `workspace_path` 而未传 `directory_id`，且该路径与某注册条目精确匹配，
   同样按路径定位并 `Touch`（保持 `last_used_at` 语义完整）。

响应不变（`{ "session": ... }`），`session.metadata.context.workspace_path` 即时携带。

#### 4.3.2 `AgentChat` 目录回填（`handler.go` L1430 附近，含顺序修正）

现状（已核实的执行顺序，`handler.go`）：

```go
L1421  usageScope := h.resolveUsageScope(...)          // 先于 workspacePath，可前移复用
L1430  workspacePath := strings.TrimSpace(req.WorkspacePath)
L1431  profileState, ... := h.resolveProfileRuntimeState(..., workspacePath) // profile 回退已消费 workspacePath
L1443  session, err := h.getOrCreateSession(storeCtx, ...) // 会话在此才可用
L1521  agentContext := map[...]{ "workspace_path": workspacePath, ... }
```

**关键约束**：`resolveProfileRuntimeState`（profile auto 回退、profile root 解析）在
会话解析**之前**就消费了 `workspacePath`。若回退逻辑放在会话解析之后，目录绑定会话的
首轮 profile 解析将永远拿到空路径，行为退化为"服务器 cwd"。因此回退必须在
`resolveProfileRuntimeState` 之前生效。

**修正后的实施方式**：将 `getOrCreateSession` 代码块（L1442-1448）整体前移到
L1430 之前（`usageScope` 在 L1421 已就绪，前移无依赖冲突），随后：

```go
// 前移后的会话解析（原 L1442-1448）
storeCtx, storeCancel := sessionStoreQueryContext(r)
session, err := h.getOrCreateSession(storeCtx, usageScope.UserID, req.SessionID)
storeCancel()
if err != nil {
    writeSessionStoreError(w, err)
    return
}

// 目录回退：请求未显式指定时，采用会话创建时绑定的目录
workspacePath := strings.TrimSpace(req.WorkspacePath)
if workspacePath == "" && session != nil {
    if v, ok := session.Metadata.Context[sessionmeta.WorkspacePath].(string); ok {
        workspacePath = strings.TrimSpace(v)   // 会话创建时绑定的目录
    }
}
if workspacePath != "" && session != nil {
    if cur, _ := session.Metadata.Context[sessionmeta.WorkspacePath].(string); cur == "" {
        session.SetContext(sessionmeta.WorkspacePath, workspacePath) // 首轮物化时回写，保证分组
        _ = h.sessionManager.Update(ctx, session)
    }
}

// 之后才是 resolveProfileRuntimeState(..., workspacePath)（原 L1431 逻辑后移）
```

- 兼容性：显式传 `req.WorkspacePath` 的既有调用方（CLI/团队调度）行为不变；
- 会话上下文已有值时**不覆盖**（会话目录一经绑定不随单轮请求漂移，与
  `sessionmeta` "环境事实冻结"的设计一致）。
- 顺序前移的副作用：共享 SQLite 被锁时的 503 会先于 profile 校验错误返回
  （两者本都是快速失败语义，可接受）；lease 获取（原 L1449+）保持在会话解析之后不变；
- 回写仅在"会话上下文为空且请求显式带了 workspace_path"时发生一次
  （存量 GetOrCreate 流路的补齐路径），目录绑定会话常态下零额外写。

### 4.4 前端：API 层与数据 hook

#### 4.4.1 类型（`frontend/src/types/runtime.ts` 追加）

```ts
export type RuntimeWorkspaceDirectory = {
  id: string;
  path: string;
  name?: string;
  created_at?: number;
  last_used_at?: number;
  exists?: boolean;
  session_count?: number;
};
export type RuntimeWorkspaceDirectoriesResponse = {
  directories: RuntimeWorkspaceDirectory[];
  count: number;
};
export type RuntimeCreateWorkspaceDirectoryRequest = { path: string; name?: string };
export type RuntimeUpdateWorkspaceDirectoryRequest = { name?: string };
export type RuntimeCreateSessionRequest 扩展: workspace_path?: string; directory_id?: string;
```

#### 4.4.2 API 模块（新文件 `frontend/src/api/runtime/workspace-directories.ts`）

```ts
listWorkspaceDirectories(): Promise<RuntimeWorkspaceDirectoriesResponse>          // GET
createWorkspaceDirectory(req): Promise<{ directory; existing?: boolean }>         // POST
updateWorkspaceDirectory(id, req): Promise<{ directory }>                         // PATCH
deleteWorkspaceDirectory(id): Promise<{ deleted: boolean; id: string }>           // DELETE
```

#### 4.4.3 会话 API 补齐（`frontend/src/api/runtime/sessions.ts`）

```ts
export async function updateRuntimeSession(
  sessionId: string,
  request: { title?: string; context?: Record<string, unknown> },
): Promise<{ session: RuntimeSessionRecord }>   // PATCH /api/runtime/sessions/{id}
```

（`deleteRuntimeSession` 本期一并补上，供后续"删除会话"复用，非本方案验收项。）

#### 4.4.4 数据 hook（新文件 `frontend/src/hooks/workspace/use-runtime-workspace-directories.ts`）

仿照 `use-runtime-sessions-data.ts` 的状态机模式：

```ts
type UseRuntimeWorkspaceDirectoriesResult = {
  directories: RuntimeWorkspaceDirectory[];
  loading: boolean; refreshing: boolean; error: string | null;
  refresh: () => Promise<void>;
  addDirectory: (path: string, name?: string) => Promise<RuntimeWorkspaceDirectory>; // 失败抛出后端 message
  renameDirectory: (id: string, name: string) => Promise<void>;
  removeDirectory: (id: string) => Promise<void>;
};
```

- 挂载时拉取一次；`add/remove/rename` 成功后自动 `refresh()`；
- 会话数展示由 `mergeDirectoryGroups`（§4.5.2）从**已拉取的 sessions 本地聚合**，
  不依赖 `GET workspace-directories` 的 `session_count`，避免对共享 SQLite 的
  第二次全量扫描（`RUNTIME_FETCH_TIMEOUT_MS = 10s` 客户端超时内需完成）。

### 4.5 前端：侧边栏目录管理 UI（`workspace-sidebar.tsx`）

#### 4.5.1 新增"工作目录"分区

在 `sessions` 分区上方新增 `SidebarSection`（id: `directories`，默认展开）：

> **2026-09-15 状态修正**：本节描述的是当时（双分区）形态。侧栏「工作目录 / 会话」已合并为单一分区，
> 该分区即合并段（会话浏览工具条整体迁入），`sessions` 分区已删除；段头徽标语义按合并方案 D5 定为
> **会话数**（不再是目录数），目录则作为分组载体、组头挂管理动作。详见
> `workspace-sidebar-directory-session-merge-plan.md` §10。
>
> **2026-09-15 样式优化**：目录行右侧三个平铺图标（新建会话 / 重命名 / 移除）已收敛为**一个**
> hover 才显示的菜单入口（`directory-group-actions.tsx`：`aria-haspopup="menu"` + 完整键盘导航 +
> 忙碌禁用）；目录内会话行的行内铅笔按钮并入行菜单作为首项「重命名会话」。目录/会话行同时去掉嵌套
> 缩进、顶到侧栏最左侧，标题获得更多宽度。详见该方案 §11。

- 标题行：`FolderIcon` + `t("sidebar.sections.directories")` + 数量 `Badge` +
  **"+" 添加按钮**（`MessageSquarePlusIcon` 语义不合适，用 `FolderPlusIcon`）；
- 目录行（每个注册目录）：
  - 左：`FolderIcon` + 名称（`name || basename(path)`），`title` 悬浮显示完整 path；
  - 中：会话数 `Badge`（`mergeDirectoryGroups` 本地聚合数）；`exists === false` 时置灰 + `TriangleAlertIcon`
    （tooltip：目录在服务器上不存在）；
  - 右（hover 显示）三个图标按钮：
    - `MessageSquarePlusIcon` → 在该目录下新建会话（§4.6）；
    - `PencilIcon` → 重命名别名（行内输入框，Enter 提交 / Esc 取消）；
    - `TrashIcon` → 删除目录（确认对话框）；
- 目录行点击 = 展开/折叠该目录下的会话列表（复用现有 `openSessionDirectories` 折叠状态与
  会话条目渲染，把"派生分组"与"注册目录"合并渲染，见 §4.5.2）。

#### 4.5.2 注册目录与派生分组合并

`workspace-sidebar-shared.ts` 新增纯函数（便于单测）：

```ts
export type MergedDirectoryGroup = {
  key: string;              // 注册目录用 directory.id，未注册派生组用 normalized path key
  directoryId?: string;     // 注册目录才有
  label: string;
  fullPath: string;
  registered: boolean;
  exists?: boolean;
  sessions: RuntimeSessionRecord[];
};

export function mergeDirectoryGroups(
  directories: RuntimeWorkspaceDirectory[],
  sessions: RuntimeSessionRecord[],
): MergedDirectoryGroup[]
```

合并规则：

1. 注册目录恒出现（0 会话也显示），其 `sessions` = 绑定路径与目录规范化路径
   **精确相等**的会话（大小写策略与注册表去重一致：Windows 不敏感、其余敏感）；
   CLI 在子目录产生的 cwd 会话**不**归并进注册目录组，仍落各自派生组
   （v1 明确不做前缀/子树归并，避免"注册根目录吞掉所有子目录会话"的计数意外；
   子树归并列为后续增强，见 §10-F6）；
2. 有会话但未注册的路径 → 派生组照旧展示（`registered: false`），避免老数据"消失"；
3. 完全无路径的会话 → 保留 "Unscoped sessions" 组；
4. 排序：注册目录在前（按 `last_used_at` 降序），派生组在后（沿用现有 updatedAt 排序）。

#### 4.5.3 添加目录对话框

新组件 `components/workspace/workspace-directory-add-dialog.tsx`：

- 受控 Dialog：路径输入（必填）+ 别名输入（可选，占位符为路径 basename）；
- 提交 → `addDirectory`；后端 400（目录不存在等）时在表单下方展示错误
  （`error.message` 已由 `fetchRuntimeJson` 透出）；
- 成功后关闭对话框并刷新目录列表。

#### 4.5.4 删除目录确认对话框

新组件 `components/workspace/workspace-directory-delete-dialog.tsx`：

- 文案明确："仅从工作区移除该目录登记，不会删除服务器上的文件；已有 N 个关联会话将保留"；
- 确认 → `removeDirectory(id)`。

### 4.6 前端：目录下新建会话

目录行"新会话"按钮流程：

1. `const { session } = await createRuntimeSession({
     title: t("workspace.newChatUnderDirectory", { directory: label }),
     user_id: selectedRuntimeSessionUserId,
     workspace_path: directory.path,
   });`
2. `refresh()` 会话列表（新会话立即出现在该目录组下）；
3. `navigate(`/workspace/sessions/${encodeURIComponent(session.id)}`)`；
4. 用户在主区输入首条消息 → `POST /api/agent/chat { session_id, ... }`；
   后端经 §4.3.2 回退逻辑取会话上下文的 `workspace_path`，agent 以该目录为 cwd。

同时保留 `/workspace/chats/new` 通用入口行为不变（无目录绑定的草稿流）。

### 4.7 前端：会话重命名

- `workspace-sidebar.tsx` 会话条目（目录组内与 chats 分区内的草稿除外）增加重命名入口：
  - hover 出现 `PencilIcon`；或双击标题进入行内编辑；
  - 行内 `<input>` 初值 = 当前标题，Enter 提交 / Esc 取消 / blur 取消；
- 提交 → `updateRuntimeSession(sessionId, { title })` → 成功后本地乐观更新 +
  `refresh()`；空串提交视为取消（后端 `UpdateTitle("")` 语义为清空，前端拦截）；
- **「未修改直接提交」必须照常发 PATCH**：该手势的语义是把当前自动标题“钉住”
  （`titleSource` 由 auto 变 `manual`），不能被“值未变化”短路掉；只有空串才是取消
  （对齐 deepseek-harness `Rows.tsx` 的 `useRenameSession` 约定，见 §14-E9）；
- 重命名后 `metadata.titleSource` 变为 `manual`，现有标题展示逻辑
  （`thread?.title || session.metadata?.title || session.id`）自动生效。

### 4.8 i18n

`frontend/src/i18n/resources/zh-CN.ts` 与 `en-US.ts` 的 workspace 命名空间新增：

```
sidebar.sections.directories        工作目录 / Directories
sidebar.directories.add             添加目录 / Add directory
sidebar.directories.addTitle        添加工作目录 / Add working directory
sidebar.directories.pathLabel       服务器目录路径 / Server directory path
sidebar.directories.pathPlaceholder E:\projects\demo 或 /home/user/project
sidebar.directories.nameLabel       别名（可选） / Alias (optional)
sidebar.directories.existsWarning   目录当前在服务器上不存在 / Directory currently missing on server
sidebar.directories.deleteTitle     删除工作目录 / Remove working directory
sidebar.directories.deleteConfirm   仅移除登记，不删除服务器文件；{count} 个关联会话将保留。/ ...
sidebar.directories.newChat         在此目录新建会话 / New chat in this directory
sidebar.directories.rename          重命名目录别名 / Rename directory alias
sidebar.session.rename              重命名会话 / Rename chat
sidebar.session.renamePlaceholder   输入新名称 / Enter a new name
workspace.newChatUnderDirectory     {directory} 的新会话 / New chat in {directory}
```

---

## 5. 实施计划（阶段拆分）

### P0 后端：目录注册表 + HTTP API

| 文件 | 改动 |
| --- | --- |
| `backend/internal/workspaceregistry/store.go` | 新建：DirectoryRecord/Store/Add/Rename/Remove/Touch/ValidatePath |
| `backend/internal/workspaceregistry/store_test.go` | 新建：规范化、去重（Windows 大小写）、不存在/非目录报错、持久化 roundtrip、no-home fail-closed |
| `backend/internal/api/skills/handler.go` | `registerRoutes` 注册 4 条 `/workspace-directories` 路由；新增 4 个 handler（建议单独文件 `workspace_directory_handlers.go`） |
| `backend/internal/api/skills/workspace_directory_handlers_test.go` | 新建：CRUD happy path + 400/404 语义 + 重复添加幂等（200 + `existing: true`） |

验收：`go test ./internal/workspaceregistry/... ./internal/api/skills/...` 通过；
curl 冒烟四个端点。

### P1 后端：会话绑定目录

| 文件 | 改动 |
| --- | --- |
| `backend/internal/api/skills/handler.go` | `CreateSession` 请求体扩展 `workspace_path/directory_id`；校验 + `SetContext(sessionmeta.WorkspacePath)`；`AgentChat` 空路径回退会话上下文并回写 |
| `backend/internal/api/skills/handler_test.go`（或新 `session_directory_binding_test.go`） | 新建：CreateSession 带/不带 workspace_path；directory_id 无效；AgentChat 回退与不覆盖语义 |

验收：`POST /sessions {workspace_path}` 后 `GET /sessions` 可见
`metadata.context.workspace_path`；首轮 agent chat 后上下文不丢、不漂移。

### P2 前端：API + hook + 目录管理 UI

| 文件 | 改动 |
| --- | --- |
| `frontend/src/types/runtime.ts` | 目录类型 + `RuntimeCreateSessionRequest` 扩展 |
| `frontend/src/api/runtime/workspace-directories.ts` | 新建 4 个 API 函数 |
| `frontend/src/hooks/workspace/use-runtime-workspace-directories.ts` | 新建 hook |
| `frontend/src/components/workspace/workspace-directory-add-dialog.tsx` | 新建添加对话框 |
| `frontend/src/components/workspace/workspace-directory-delete-dialog.tsx` | 新建删除确认 |
| `frontend/src/components/workspace/workspace-sidebar.tsx` | 新增 directories 分区；hover 操作；折叠合并渲染 |
| `frontend/src/components/workspace/workspace-sidebar-shared.ts` | `mergeDirectoryGroups` 纯函数 |
| `frontend/src/pages/workspace-page.tsx` | 接线 hook → sidebar props |

### P3 前端：目录下新建会话 + 会话重命名

| 文件 | 改动 |
| --- | --- |
| `frontend/src/api/runtime/sessions.ts` | `updateRuntimeSession`（含 `deleteRuntimeSession` 顺带补齐） |
| `frontend/src/components/workspace/workspace-sidebar.tsx` | 目录行"新会话"→ create + navigate；会话条目内联重命名 |
| `frontend/src/hooks/workspace/use-runtime-sessions-data.ts` | 重命名后的本地乐观更新辅助（可选） |

### P4 i18n、构建与验收

| 事项 | 说明 |
| --- | --- |
| i18n | zh-CN / en-US 新键（§4.8） |
| 构建 | `frontend` `pnpm build` → 产物经既有流程嵌入 `backend/internal/webui/dist`（`assets.go`），重启 runtime-server（`start-web.ps1`，:8101） |
| 手工验收 | 按 §7.3 清单逐项走查 |
| 文档 | 本方案状态改为"已实施"；如交互有偏差回写差异 |

依赖关系：P0 → P1 →（P2、P3 可并行，P3 中"目录下新建会话"依赖 P1 的
CreateSession 扩展；"会话重命名"仅依赖既有 PATCH 端点，可随 P2 先行交付）→ P4。

---

## 6. 兼容性与安全考量

1. **不触碰文件系统**：删除目录仅删注册表条目；`Add` 不创建目录。所有写盘仅限
   `~/.aicli/workspace_directories.yaml`。
2. **路径校验即攻击面收敛**：仅接受绝对路径且必须已存在；注册表本身不做任意路径白名单
   （单机 runtime-server 信任模型与 foldertrust 一致）。若后续需要限制可添加根
   （如仅允许 `$AICLI_WORKSPACE_ROOTS` 下目录），在 `ValidatePath` 增加前缀校验即可，
   接口语义不变。
3. **会话目录一经绑定不漂移**：`AgentChat` 回退只读不覆盖；显式传参的既有调用方
   （aicli CLI、团队调度）行为完全不变。
4. **共享 SQLite 并发**：目录 API 中的会话扫描沿用 `sessionStoreQueryContext`
   5s 截止 → 503，与既有 sessions 端点一致，不新增锁行为。
5. **老数据兼容**：未注册目录的既有会话仍按派生分组展示（§4.5.2 规则 2/3），
   升级无感。
6. **no-home 环境**：注册表 path 为空时 fail closed（List 返回空、Add 返回 503/错误），
   与 foldertrust 一致，不落 cwd 相对路径文件。
7. **路径探测面（path-probing oracle）**：`POST /workspace-directories` 的
   存在性校验会向调用方泄露"任意绝对路径是否为存在目录"。该信息面严格弱于
   既有 `/api/runtime/fs/read-file`、`/fs/write-file` 端点已暴露的能力，
   在 runtime-server 现行"本机单用户信任模型"下可接受；若未来引入远程多租户，
   需与 §6 第 2 条（根目录白名单）及 §10-F6 第 4 项一并收敛。

---

## 7. 测试与验收

### 7.1 后端单测（`go test`）

- `workspaceregistry`：规范化/去重/校验/持久化/no-home；
- handler：4 端点 CRUD、400/404、`session_count`、CreateSession 绑定、
  AgentChat 回退与不覆盖。

### 7.2 前端单测（vitest）

- `mergeDirectoryGroups`：注册在前/派生在后、0 会话注册目录、unscoped 组保留；
- `use-runtime-workspace-directories`：加载/错误/增删改后刷新；
- `workspace-sidebar`：添加按钮触发对话框、目录行三个操作回调、重命名行内编辑提交/取消
  （参照既有 `workspace-sidebar.test.ts` / `workspace-sidebar-responsive.test.tsx` 模式）。

### 7.3 手工验收清单（:8101）

1. `/workspace/chats/new` → 侧边栏出现"工作目录"分区（空态文案）；
2. 点"+"→ 输入一个真实存在的服务器目录 → 列表出现该目录（0 会话）；
3. 输入不存在的路径 → 表单显示后端错误，不关闭对话框；
4. 重复添加同一路径（Windows 下大小写变体）→ 提示已存在，不产生重复条目；
5. 目录行"新会话" → 跳转 `/workspace/sessions/{id}`，会话出现在该目录组下；
6. 发送首条消息 → agent 正常响应；会话分组仍在该目录下（上下文未漂移）；
7. 会话条目重命名 → 列表与主区标题同步更新；刷新页面后仍保留；
8. 删除目录 → 确认文案显示关联会话数；删除后目录消失、会话保留并落入派生分组；
9. 删除目录下全部会话后再刷新 → 注册目录仍显示（0 会话）；
10. `/workspace/chats/new` 原有通用新建流程回归正常。

---

## 8. 风险与回滚

| 风险 | 影响 | 缓解/回滚 |
| --- | --- | --- |
| 注册表文件损坏 | 目录列表丢失 | 原子替换写盘（temp+rename）；损坏时 List 返回空并告警日志，不阻断会话功能 |
| `AgentChat` 回退改变既有行为 | CLI/团队会话意外获得 workspace | 回退仅对"会话上下文已绑定 workspace_path"的会话生效；该状态当前仅由新 CreateSession/回写产生，存量会话不受影响；如异常可特性开关关闭回退 |
| `session_count` 全量扫描开销 | 会话数很大时 GET 变慢 | 本期可接受（ListSessions 已全量返回）；后续可加缓存或由会话列表接口聚合 |
| 前端嵌入产物过期 | 新 UI 不生效 | P4 明确"前端 build + 重启 runtime-server"为验收前置步骤 |

回滚策略：注册表为独立文件与独立路由，回滚 = 还原 handler 注册与前端构建产物；
会话上下文中新增的 `workspace_path` 键对旧版本前端/后端均为无害字段（旧前端本就读它做分组）。

---

## 9. 附录：关键代码位置索引

| 位置 | 说明 |
| --- | --- |
| `frontend/src/App.tsx` | workspace 路由与 `/workspace/chats/new` 默认路由 |
| `frontend/src/pages/workspace-page.tsx` | 页面装配，sidebar props 接线点 |
| `frontend/src/components/workspace/workspace-sidebar.tsx` | 三分区侧边栏；目录组渲染（用户→目录→会话） |
| `frontend/src/components/workspace/workspace-sidebar-shared.ts` | `groupRuntimeSessionsByDirectory`/`resolveRuntimeSessionDirectory`（派生分组，含路径键清单） |
| `frontend/src/hooks/workspace/use-workspace-thread-selection.ts` | `NEW_THREAD_ID`、草稿线程、URL 规范化 |
| `frontend/src/api/runtime/sessions.ts` | 会话 API 封装（缺 update/delete） |
| `frontend/src/types/runtime.ts` | `RuntimeCreateSessionRequest`、`AgentChatRequest.workspace_path` |
| `frontend/src/i18n/resources/{zh-CN,en-US}.ts` | workspace 命名空间文案 |
| `backend/internal/api/skills/handler.go` | 路由注册（L618+，Sessions L702-748）；`CreateSession` L2249；`ListSessions` L2293；`UpdateSession` L2516（title/context 已支持）；`AgentChat` workspacePath L1430/L1521/L1628 |
| `backend/internal/chat/session.go` | `SessionMetadata.Context`、`SetContext` |
| `backend/internal/sessionmeta/sessionmeta.go` | `WorkspacePath = "workspace_path"` 等上下文键 |
| `backend/internal/foldertrust/store.go` | YAML 注册表持久化先例（`~/.aicli/trusted_folders.yaml`，AICLI_HOME） |
| `backend/internal/webui/` | 前端产物嵌入与静态托管 |
| `backend/start-web.ps1` | runtime-server :8101 启动脚本 |

---

## 10. 审查记录（2026-09-10，方案自审）

审查方式：逐条核对文档断言与代码事实（handler.go / session.go / shared.ts / profile_support.go），
标记 🔴 必须修正 / 🟡 建议修正 / 🟢 已验证无误。

### F1 🔴 AgentChat 回退插入点与 profile 解析顺序冲突（已修正 → §4.3.2）

- **问题**：原方案写"回退逻辑在会话解析完成后插入"，但实际执行顺序是
  `workspacePath`（L1430）→ `resolveProfileRuntimeState`（L1431，profile auto 回退
  消费 workspacePath）→ `getOrCreateSession`（L1443）。若按原方案在会话解析后回退，
  目录绑定会话的首轮 profile 解析永远拿到空路径，profile root/auto 回退退化为服务器 cwd。
- **修正**：将 `getOrCreateSession` 块（L1442-1448）整体前移至 L1430 之前
  （`usageScope` L1421 已就绪），回退后再进入 profile 解析；并注明 503 错误顺序
  前移的副作用与 lease 获取位置不变。

### F2 🔴 文档内部不一致：重复添加语义（已修正 → §5-P0）

- **问题**：§4.1/§4.2.2 定义重复添加为幂等（200 + `existing: true`），
  但 P0 测试行写了"409 语义"，两处矛盾。
- **修正**：统一为幂等 200；测试行改为"重复添加幂等（200 + existing）"。

### F3 🟡 `session_count` 双份全量扫描（已修正 → §4.2.1/§4.4.4/§4.5.1）

- **问题**：原方案让 `GET /workspace-directories` 服务端统计会话数，且前端"以后者为准"。
  但 workspace 前端本就全量拉取 `GET /sessions`，目录列表再扫一次共享 SQLite 属于
  双份开销（还受 10s 客户端超时约束）。
- **修正**：前端会话数改为 `mergeDirectoryGroups` 从已拉取 sessions 本地聚合；
  后端 `session_count` 降级为可选字段（供 CLI 等非 workspace 调用方）。

### F4 🟡 目录与会话的匹配语义未定义清楚（已修正 → §4.5.2）

- **问题**：原方案只写"上下文路径匹配的会话"，未定义精确相等还是前缀归并。
  前端派生分组读取 8 个上下文键（workspace_path/cwd/profile_root 等），若做前缀归并，
  注册根目录会"吞掉"所有子目录 cwd 的 CLI 会话，计数意外。
- **修正**：v1 明确**精确相等**匹配（大小写策略与注册表去重一致）；子树归并列为
  后续增强（F6）。

### F5 🟢 已验证无误的关键断言

| 断言 | 验证结果 |
| --- | --- |
| `UpdateSession`（PATCH）支持 `title/context` 更新 | ✅ handler.go L2516-2580 |
| `UpdateTitle("")` 语义为清空标题且 `titleSource=manual` | ✅ session.go L313-320（前端拦截空串的决策成立） |
| `fetchRuntimeJson` 透出后端 `payload.error` 消息 | ✅ api/runtime/shared.ts L58-73/L184-199（添加对话框错误展示可行） |
| `sessionmeta.WorkspacePath = "workspace_path"` 为既定绑定字段 | ✅ sessionmeta.go L24；`buildSessionActor` 消费（handler_test.go:2660 佐证） |
| `foldertrust` YAML 注册表先例（AICLI_HOME、fail-closed） | ✅ foldertrust/store.go |
| workspace 路由 `/workspace/chats/new` → `WorkspacePage` | ✅ App.tsx（命中 `/workspace/chats/:threadId`） |
| `RUNTIME_FETCH_TIMEOUT_MS = 10s` 默认超时 | ✅ shared.ts L182（目录 API 需在此窗口内返回） |

### F6 🟡 后续增强（自审遗留项，不阻塞本期，记录备查）

> 本节为方案自审（§10）遗留项；§14 为参考实现（deepseek-harness）借鉴项，
> 两份清单互补，排期时合并排序。

1. **子树归并**：注册目录聚合其子目录下 cwd 的会话（需产品确认计数口径）；
2. **目录健康巡检**：后台定时 `os.Stat` 注册目录，`exists=false` 主动提示而非仅在 GET 时发现；
3. **会话删除入口**：`deleteRuntimeSession` 已在 P3 顺带封装，后续可在侧边栏补删除 UI；
4. **根目录白名单**：`$AICLI_WORKSPACE_ROOTS` 限制可注册路径（多租户前置条件）。

### F7 🟡 审查中新识别的安全说明（已补充 → §6 第 7 条）

`POST /workspace-directories` 的存在性校验构成路径探测面；因服务端已暴露
`/fs/read-file`、`/fs/write-file`，该信息面严格更弱，本机信任模型下可接受，
已写入 §6 第 7 条作为显式已知项而非隐性风险。

### F8 🟢 阶段依赖修正（已修正 → §5）

会话重命名仅依赖既有 PATCH 端点，不依赖 P1；已注明可随 P2 先行交付，
缩短可感知交付路径。

### 审查结论

方案总体成立：复用 `sessionmeta.WorkspacePath` 作为绑定事实、注册表与派生分组
双轨合并、删除目录不动文件系统三个核心决策均与现有代码语义自洽。F1 是唯一
会导致功能静默退化的实质缺陷，已在 §4.3.2 给出精确插入点；其余为一致性/开销/
语义澄清类修正，均已回写到正文。方案可进入评审/实施。

---

## 11. 实施记录（一）：会话执行 CWD 接线与绑定防漂移（2026-09-10）

### 现象

目录绑定会话中 shell 实际工作目录是后端进程启动目录（repo 根），与会话
workspace 上下文记录值（如 `E:\temp`）不一致：`bash.go` 的 `resolveWorkdir("")`
回退 `os.Getwd()`；`SetBasePath` 只影响文件工具相对路径解析，且为全局注册时
一次性设置。同时 `handler.go` 旧逻辑在显式请求路径 ≠ 绑定值时会覆盖
`sessionmeta.WorkspacePath`，违反 §4.3"一经绑定不漂移"。

### 修复

1. **toolctx**：新增 `WithWorkspaceRoot/WorkspaceRoot`（`toolctx/context.go`）。
2. **agent loop**：`toolCallContext` 注入会话 workspace root（options 优先
   `tool_base_path`，回退 `workspace_path`；提取 `toolWorkspaceRootForAgent`）。
3. **shell 工具**：`bash.go` 新增 `resolveWorkdirWithBase`，优先级为
   显式 workdir（相对路径 join 会话根）> 会话 workspace root > 进程 cwd；
   单命令与 batch（递归 `Execute`）路径均生效，`cmd.Dir` 随之落定。
4. **绑定防漂移**：`handler.go` 会话元数据回写改为
   `sessionNeedsWorkspacePathMaterialization`（仅当存储为空时物化，绝不覆盖）。
5. **前端**：`use-workspace-agent-chat-turn.ts` 提取
   `resolveChatTurnWorkspacePath`——已物化会话不再逐轮携带 workspace_path
   （由后端绑定回退兜底），仅新草稿线程首轮携带身份级路径完成绑定。

### 测试

- 后端：toolctx / toolkit/tools / agent / api/skills 全包通过；新增 toolctx
  往返、`resolveWorkdirWithBase` 优先级、`toolCallContext` 携带 root、
  materialization 不覆盖四组用例。
- 前端：hook 测试 5/5（新增 workspace_path 决策两例），`npm run build` 通过。

### 已知边界（已于 §12 消除）

第一轮结束时文件工具的相对路径仍解析到全局注册 basePath，未随会话绑定
目录切换（preflight 与 shell 已按会话 root 解析）。该缺口已在第二轮增强中
修复，见 §12。

---

## 12. 实施记录（二）：文件工具与 aicli_exec 的会话根感知（2026-09-10）

### 缺口

第一轮后 preflight 与 shell 已按会话绑定目录解析，但文件工具
（view/edit/write/append_write/multiedit/glob/grep/ls/download/apply_patch）
的相对路径仍解析到注册期全局 basePath（`SetBasePath`，取
`config.Workspace.Root`），aicli_exec 的 CWD 也与旧 bash.go 同病（空值回退
进程 cwd）。目录绑定会话中相对路径读写会落到错误目录，与 preflight
判定、shell CWD 三者不一致。

### 修复

1. `sandbox_support.go` 新增 `effectiveBasePath` / `resolvePathWithContext` /
   `buildPathNotFoundHintWithContext`：优先级 = 会话根
   （`toolctx.WorkspaceRoot`）> 注册 basePath > 原样（绝对路径不动）。
2. 上述文件工具 Execute 内全部改用 ctx 感知解析；grep 的
   `parseOptions(ctx, …)` 内联解析并经 `opts.basePath` 贯通
   `resolveSearchScopes` 与 pattern_file 提示。
3. `apply_patch.go`：`patchApplier` 携带 ctx，补丁相对路径同样锚定会话根。
4. `aicli_exec.go`：复用 `resolveWorkdirWithBase`（与 bash 完全同源），
   删除等价的旧 `resolveAICLIExecWorkdir`。

### 兼容性

无 ctx 会话根时行为与旧逻辑一致（fallback basePath），全部既有测试不改
即通过；两条工具执行路径（顺序 `loop.go` 顺序执行、并行
`tool_parallel_scheduler.go`）均经 `toolCallContext` 注入，覆盖完整。

### 测试

新增 `workspace_root_resolution_test.go` 4 例：解析器优先级单元、glob
端到端（会话文件命中/全局文件排除）、apply_patch 相对路径落点、grep
parseOptions 双路径断言。toolkit/tools 全包 + agent/toolctx/toolexec/tools
回归 + `go vet` 全部通过。

## 13. 实施记录（三）：第三轮实施后审查修复（2026-09-10）

对 §11/§12 两轮实施做独立审查后确认的 2 个 major + 4 个次要项，均已修复。

### M2（major）目录绑定物化早于 workspace 校验 → 会话锁死

- 现象：`AgentChat` 在解析出本轮 `workspacePath` 后立即把
  `sessionmeta.WorkspacePath` 写入会话元数据；而路径存在性/类型校验发生在
  后面的 `buildWorkspaceContext`。请求显式传入不存在目录时返回 400，但坏路径
  已落库；此后任何**不带** `workspace_path` 的轮次都会回退到这个坏目录并持续
  400，会话被锁死在无效目录上。
- 修复（`handler.go`）：
  1. 删除 L1444 附近的早物化块；
  2. 物化移动到 `buildWorkspaceContext` 成功之后执行（失败轮次零写入）；
  3. 顺带修掉早物化块缺失的 `h.sessionManager != nil` 防护（m3）。
- 回归：`handler_workspace_binding_test.go::TestAgentChatInvalidWorkspacePathDoesNotBindSession`
  断言「400 + 会话元数据未绑定」。已用临时补丁复现旧行为验证该测试确实失败
  （got `…\missing-workspace`），确认判别力后还原。
- 语义不变：绑定仍「一经绑定不漂移」，显式路径不覆盖既有绑定。

### M1（major）路径提示锚定全局 basePath，与解析根不一致

- 现象：`buildPathNotFoundHint` / `buildPathKindMismatchHint`（及
  `buildPathNotFoundError` / `buildPathKindMismatchError`）用 `p.basePath`
  构造提示，而实际解析已改走会话根（§12）。目录绑定会话下，工具报错给出的
  workdir/候选路径来自另一个目录，误导模型重试。grep 分支当时已用有效根，
  工具间行为不一致。
- 修复（`sandbox_support.go` + 13 处调用点）：提示构造器统一改为
  **ctx 感知**（`effectiveBasePath(ctx)` = 会话根 > 注册 basePath > 空），
  与 `resolvePathWithContext` 同源；调用点覆盖 view/ls/glob/edit/multiedit/
  write/download（Execute 内传 `ctx`）与 apply_patch（经 `patchApplier.ctx`）。
- 顺带删除死代码（m1/m2）：
  `buildPathNotFoundHintWithContext`（零调用）与
  `sandboxPolicy.resolvePath`（零调用，apply_patch 的 `patchApplier.resolvePath`
  是另一个方法，保留）。

### m4 workspace 目录注册表懒加载数据竞争

`workspaceDirectoryRegistry` 原先在 fast path 裸读 `h.workspaceDirectories`，
与该字段在 `sync.Once.Do` 内的赋值构成竞争。现统一走 `Once`（注入值优先，
其余懒加载），并在 `-race` 下验证。

### 测试与验证

- 新增：`workspace_root_resolution_test.go` 4 例（解析器优先级、view 未找到、
  write 类型不匹配、apply_patch 删除缺失提示锚定会话根）+
  `TestFileToolsExecuteRelativePathsInSessionRoot`（view/ls/write/edit/
  multiedit/append_write 六个子用例，端到端断言相对路径落会话根且全局
  basePath 不被触碰）；
  `handler_workspace_binding_test.go` 2 例（M2 绑定顺序、注册表并发）。
- `go build ./...`、`go vet`（skills/tools/toolctx）通过；
  `go test` skills/tools/toolctx/executor/workspaceregistry/agent 全绿；
  `go test -race ./internal/api/skills/` 通过。

### 已知边界（不阻塞，记录备查）

- Windows 下 `\foo`、`C:foo` 这类「驱动器相对」写法不被 `filepath.IsAbs`
  识别为绝对路径，会按相对路径拼接会话根；属既有解析语义（与旧 basePath
  行为一致），未在本轮改变。
- 前端 eslint 与 `cmd/` 少量既有失败与本轮改动无关（改动前即存在）。

---

## 14. 参考实现借鉴：deepseek-harness 左侧栏增强候选（2026-09-15）

> 来源：`E:\projects\ai\deepseek-harness`（同构客户端，其侧栏为 Figma 设计稿驱动）。
> 本节只收录「与本方案同一功能面、且本仓库当前确实缺失或更弱」的条目；每条给出
> **参考证据 / 本仓库现状 / 建议 / 落点 / 验收**。全部为增强候选，不改动 §11–§13
> 已交付的 CWD 与工具根感知语义，也不推翻 §3.2 的核心设计决策。
>
> **前置阅读**：`docs/plan/workspace-sidebar-directory-session-merge-plan.md`（2026-09-15，**已落地**）
> 已把「工作目录 / 会话」两个分区合并为单一目录骨架的会话浏览器（原「同一会话在侧栏出现两行」
> 的重复问题随之消除）；其中 Phase 2 交付了「组内超过 5 条只渲染 5 条 + 展开其余 N 个会话」，
> 与本文件 E1 重叠，E3/E4/E5/E7/E8 的落点也已随合并后的组件结构确定（`sessions-section.tsx` 已删除）。
> 分工与排序以 §14.4 为准。

### 14.0 对照总表

| 能力面 | deepseek-harness | 本仓库现状 | 结论 |
| --- | --- | --- | --- |
| 添加目录 | 只有「选目录」一条路径：应用内浏览选择器（`ui-workspace/src/client/WorkspacePicker.tsx` + 独立包 `ui-directory-picker-browse`），不提供手输 | 手输绝对路径 + 校验（`workspace-directory-add-dialog.tsx`） | 借鉴 → E2 |
| 目录组内会话过多 | 阈值折叠 + 「展开其余 N 个」（`ui-workspace/src/client/rows/WorkspaceBrowser.tsx`） | 目录分区不折叠；折叠交互只用在会话分组（`session-group-toggle.tsx`；合并后由 `directories-section.tsx` 使用） | 借鉴 → E1 |
| 目录行动作 | 「+」常驻；重命名/删除收进「…」菜单；hover 时右侧信息位由相对时间**交换**为「…」（`rows/Rows.tsx`） | 三个图标在 hover 时同时出现（`directories-section.tsx` L210 起） | 借鉴 → E3 |
| 目录行信息 | HoverCard：完整路径 + `~` 缩写 + 复制，并复用为「目录缺失」提示载体（`rows/Rows.tsx`） | 无 HoverCard 原语（`frontend/src` grep 无命中） | 借鉴 → E4 |
| 视图选项 | `groupBy` + `orderBy` 存于单一版本化键、账户切换时清理（`ui-workspace/src/client/stores.ts`、`tree.ts`） | 已有 orderBy（`session-order-control.tsx` + `session-order-store.ts` v1、按账户分桶）；groupBy 未持久化 | 部分借鉴 → E5 |
| 搜索 | 250ms 防抖、分页 `hasMore`、后端检索不可用时给降级文案（`rows/WorkspaceBrowser.tsx`） | `use-session-search.ts` 已有 AbortController + 请求序号保护；无防抖、无降级文案 | 借鉴 → E6 |
| 行状态 | 待处理交互优先于运行态；子代理活动沿 lineage 冒泡；已完成未读提示（`rows/Rows.tsx`、`subagent-lineage.ts`） | 优先级已实现（`session-row-status.ts` L84–L96、L149–L160）；无 unseen 提醒 | 借鉴 → E7 |
| i18n 对齐 | key 联合类型 + 逐键断言（`ui-workspace/src/client/locales.ts`） | 已有编译期对齐（`i18n/resources/shape.ts` + en-US `satisfies DeepStringShape`） | **无需重复建设**；仅补 a11y → E8 |
| 重命名提交语义 | 未修改的非空标题同样提交，用于把自动标题钉成 manual（`rows/Rows.tsx` `useRenameSession`） | §4.7 原先未写明 | 语义补强 → E9（正文已并入 §4.7） |

### 14.1 增强候选（按建议优先级排列）

#### E1 🟠 目录组内会话折叠阈值（P1，纯前端）

- **参考**：`WorkspaceBrowser.tsx` 用 `COLLAPSED_SESSION_LIMIT`（默认 5）截断目录组内会话，
  尾部渲染「展开其余 N 个」；空白（尚未命名）的新会话行单独处理，不占用折叠名额。
- **现状**：`directories-section.tsx` 无截断；`session-group-toggle.tsx` 的折叠交互只服务会话分组。
- **建议**：目录组内会话 > 5 条时默认折叠，尾部「展开其余 N 个 / 收起」；**刚在目录下新建的空白会话
  必须可见**（不参与计数）；折叠状态仅存内存，不写 localStorage（与 dsh 一致，避免"上次折叠状态"造成困惑）。
- **与在飞方案重叠（见 §14.4）**：合并方案 §3.5-A 已把同一阈值行为写入 Phase 2 交付范围
  （并补了「被选中的会话若被上限隐藏则自动展开该组」）；若其按计划落地，E1 随其交付，
  本文件只保留验收口径与「空白会话不占名额」这一补充约束。
- **落点**：`frontend/src/components/workspace/workspace-sidebar/directories-section.tsx`、
  `workspace-sidebar-shared.ts`（截断计算做成纯函数便于单测）、
  `i18n/resources/{zh-CN,en-US}/workspace/base.ts`（复用 `sidebar.sessionGrouping.showMore` 的文案模式）。
- **验收**：vitest 三例——「6 条 → 显示 5 + 其余 1」「空白会话不计入且始终可见」「展开不写入存储」。

#### E2 🟠 添加目录：应用内目录浏览选择器（P1，需后端新端点）

- **参考**：dsh 添加目录只有一条路径——从菜单项 `::add-workspace` 进入 `WorkspacePicker.tsx` 的
  `useDirectoryFlow`，最终由独立包 `ui-directory-picker-browse` 渲染浏览选择器；
  选择结果天然是**真实存在的绝对路径**，从根上消除手输歧义。
- **现状**：`workspace-directory-add-dialog.tsx` 仅支持手输路径，错误路径只能靠后置校验纠正；
  §8 已记录的 Windows `C:foo` / `\foo`「驱动器相对」写法无法被 `filepath.IsAbs` 识别，
  手输路线下只能报错，无法自愈。
- **建议**：对话框增加「浏览…」入口（面包屑 + 目录列表 + 上一级 / 刷新 / 选中即填），
  手输保留为「手动输入」折叠区（兼容既有习惯、脚本化输入与 no-home 环境）。
  **后端需新增目录列举端点**：现有 `/api/runtime/fs/*` 只有 `read-file` / `write-file` / `append-file`
  （`backend/internal/api/skills/handler.go` L701–L703），没有 list。
- **安全（必须与 §6 同步）**：该端点把 §6 第 7 条的「路径存在性探测面」升级为「目录内容枚举面」，
  需显式设计边界：只返回目录项（不回传文件名之外的内容元信息）、默认过滤隐藏项、
  符号链接不跟随或跟随但不得逃出所选根、沿用与目录 API 一致的 5s 超时/错误语义；
  落地时在 §6 第 7 条后追加一条已知项并同步 §10-F7。
- **落点**：`backend/internal/api/skills/handler.go` + 新 handler 文件（含 go test）、
  `frontend/src/api/runtime/workspace-directories.ts`（或复用 fs 客户端封装）、
  `workspace-directory-add-dialog.tsx`、i18n。
- **验收**：后端——不存在路径 400/404、指向文件而非目录、隐藏项过滤、超时 503；
  前端——面包屑上/下级导航、选中回填后原校验链路不变（仍走 `POST /workspace-directories` 的 `ValidatePath`）。

#### E3 🟡 目录行动作收敛：「+」常驻 + 其余进「…」菜单（P2，纯前端）

- **参考**：`rows/Rows.tsx` 目录行常驻「+」（在该工作区新建会话），重命名/删除进「…」菜单；
  右侧信息位 hover 时由相对时间**交换**为「…」，不额外占位、不引发布局跳动。
- **现状**：`directories-section.tsx` L210 起 hover 同时浮现 3 个图标（新建会话、重命名、删除），
  点击目标小、删除与重命名相邻易误触（删除虽有 §4.5.4 确认弹窗兜底）。
- **建议**：目录行 = 「+」常驻 + hover 显示「…」（重命名 / 删除，删除保持危险色与二次确认）；
  会话行已有「…」菜单（`session-item.tsx` L227），对齐其 hover 交换语义即可，无需重构。
- **落点**：`directories-section.tsx`、`session-item.tsx`（仅 hover 语义对齐）、
  复用 `session-row-actions.ts` 的菜单原语，i18n 新增菜单项文案键。
- **验收**：Tab 可聚焦「…」→ ArrowDown 选择 → Esc 关闭并归还焦点；删除仍需二次确认；
  hover 交换不改变行高（快照/计算样式断言任选其一）。

#### E4 🟡 目录行 HoverCard：完整路径 + 复制 + 缺失提示（P2，纯前端）

- **参考**：`rows/Rows.tsx` 的 `WorkspaceHoverContent` 展示完整路径，`abbreviateHomePath` 做 `~` 缩写，
  带复制按钮；同一卡片复用于「目录已不存在」的状态提示。
- **现状**：无 HoverCard；目录名被截断后（同名尾目录场景）无法确认指向哪个路径。
- **建议**：hover 约 500ms 显示卡片：`~` 缩写路径（可展开全路径）、复制按钮（复制后就地反馈）、
  `exists=false` 时提示「目录不存在」并内联给出移除入口（与既有 `sidebar.directories.existsWarning` 联动）。
- **落点**：新组件 `frontend/src/components/workspace/workspace-sidebar/workspace-directory-hover-card.tsx`
  + `directories-section.tsx`；i18n 增加 `pathLabel` / `pathCopied` / `expandFullPath` 等键。
- **验收**：键盘 focus 同样触发；`prefers-reduced-motion` 下无延迟动画；超长路径不撑破侧栏
  （`max-w` + 折行）；复制走 `navigator.clipboard` 失败时降级为可选中文本。

#### E5 🟡 视图选项统一持久化：补齐 `groupBy`（P2，改动存储）

- **参考**：`stores.ts` 把 `{ groupBy, orderBy }` 存进**单一版本化键**（`dsh.workspace.view.v5`），
  文档内 `version` 不匹配即整体丢弃回默认，并提供账户维度的 key 清理；
  `tree.ts` 用纯函数完成"构建树 / 拉平"，UI 只做渲染。
- **现状**：orderBy 已完整落地（`session-order-control.tsx`：最近更新 / 手动 + 拖拽重排，
  `session-order-store.ts` 有 `SESSION_ORDER_STORAGE_VERSION = 1` 与按账户分桶）；
  groupBy 未持久化（会话分组的展开/折叠只存内存，符合预期），
  但「按工作区 / 单列表」这类**视图模式**尚无存储位。
- **建议**：把视图模式并入 `session-order-store` 的同一份版本化文档（升到 v2，旧 v1 文档安全回退默认），
  沿用其账户隔离、解析失败回退与纯函数测试范式；不新增第二套存储键。
- **落点**：`frontend/src/lib/workspace/session-order-store.ts`（v2 + 迁移回退）、
  `session-grouping-control.tsx`、`hooks/workspace/use-session-order.ts`。
- **验收**：旧 v1 文档在 v2 下不抛错且取默认值；账户 A/B 互不串味（沿用现有测试模式新增用例）；
  存储写入失败（隐私模式）不阻塞渲染。

#### E6 🟡 搜索健壮性补齐：防抖 + 降级文案 + 命中定位（P2，纯前端）

- **参考**：`WorkspaceBrowser.tsx` 用 `SEARCH_DEBOUNCE_MS = 250` 防抖；结果带 `hasMore` 分页；
  宿主检索不可用时给出明确降级提示，而不是渲染空列表。
- **现状**：`use-session-search.ts` 已有 `AbortController` 与请求序号保护（旧请求不覆盖新结果，
  取消不落错误态），但**没有防抖**；`session-search-dialog.tsx` 未区分"无匹配"与"检索不可用"，
  也没有"命中后展开所在目录组并滚动定位"。
- **建议**：输入 250ms 防抖（防抖窗口内新输入直接 abort 在途请求）；空结果区分两态并给重试按钮；
  `hasMore=true` 时尾部「加载更多」；选中命中项后展开其所属目录组（与 E1 联动）并滚动到可见区。
- **落点**：`hooks/workspace/use-session-search.ts`、`components/workspace/session-search-dialog.tsx`、
  `i18n/resources/{zh-CN,en-US}/workspace/panels-session-search.ts`。
- **验收**：fake timers 下连续输入只发一次请求；失败态与空态文案不同（快照断言）；
  命中后目标行进入可视区；abort 不产生未处理 rejection。

#### E7 🟡 已完成未读提醒（unseen）（P2，纯前端派生状态）

- **参考**：`rows/Rows.tsx` 对"本轮已跑完但用户尚未查看"的会话打点提示；
  子代理活动沿 `subagent-lineage.ts` 冒泡，父行同步可见。
- **现状**：`session-row-status.ts` 已实现「待审批 / 待回答 / 计划评审 > 运行中 > 子代理运行中」
  的优先级（L84–L96 与 L149–L160），**没有**"已完成未读"；`lib/workspace/session-lineage.ts` 已存在，
  可复用于父行聚合。
- **建议**：未读判据放在前端派生层：会话切走时处于 running → 切回前收到完成事件即标记 unseen，
  点击/切回后清除；父会话按 lineage 汇总子代理完成；渲染顺序让 unseen 弱于 pending、强于纯 running。
  （dsh 的分组头聚合状态是其已知局限，我们不必照抄其取舍——分组头可只做计数不做状态汇总。）
- **落点**：`session-row-status.ts`（新增 `completedUnseen` 分支）、`session-row-view-model.ts`、
  `hooks/workspace/use-session-runtime-state.ts`。
- **验收**：单测覆盖「未读 → 点击清除」「子代理完成冒泡到父行」「unseen 不覆盖 pending 优先级」。

#### E8 🟢 行级可访问性与状态文案（P3，纯前端）

- **参考**：`rows/Rows.tsx` 用 `role="treeitem"` / `aria-expanded` 表达分组层级，
  状态图标配 visually-hidden 文本。
- **现状**：i18n 键对齐已有**编译期**保障（`i18n/resources/shape.ts` 把 zh-CN 键树投影为宽字符串形状，
  en-US 用 `satisfies DeepStringShape` 对齐），无需照搬 dsh 的联合类型方案；
  但侧栏目录行/分组行缺少 `aria-expanded` 与状态朗读文本（`session-agents-tree.tsx` 已有同类实践可参考）。
- **建议**：目录行与分组行补 `aria-expanded`；状态图标补 visually-hidden 文本（运行中 / 待处理 / 未读）；
  「…」菜单补 `aria-haspopup="menu"` 与 `aria-expanded`。
- **落点**：`directories-section.tsx`、`session-row.tsx`、`session-item.tsx`、
  `i18n/resources/{zh-CN,en-US}/workspace/base.ts`（新增 a11y 文案键）。
- **验收**：可访问性树中分组行带展开状态；状态语义不只由图标形状承载（读屏可辨）。

#### E9 🟢 重命名「未修改提交」语义（P3，语义 + 测试）

- **参考**：`rows/Rows.tsx` 的 `useRenameSession` 明确约定：**非空但未修改**的提交同样发出请求，
  用于把自动标题钉成 manual；只有空串才是取消。
- **现状**：§4.7 已并入该条正文（本节不重复描述），但尚未列入测试清单。
- **落点/验收**：`hooks/workspace/use-workspace-session-actions.ts` 及对应测试；
  断言「输入未改 → 仍发 PATCH 且 `titleSource` 变 manual」「输入清空 → 不发请求」两例。

### 14.2 明确不照搬（避免二期评审重复讨论）

1. **宿主侧 Workspace 实体 + `sessionIds` 成员表**：dsh 的 workspace 是宿主一等实体、成员关系显式存储；
   本方案已定型「`sessionmeta.WorkspacePath` 单源 + 注册表与派生分组双轨合并」（§3.2），
   照搬会引入双写与迁移，收益不足。
2. **会话重命名改对话框**：dsh 用对话框承载重命名；本方案 §4.7 的行内编辑更贴合桌面习惯，保留。
3. **归档语义**：dsh 的归档直接影响默认可见性与计数；本仓库无归档概念，属产品决策，不在本方案引入。
4. **OS 原生目录选择器**：dsh 通过 slot 组合选择器后端；我们面对的是 runtime-server 主机文件系统，
   只能走服务端列举（E2），不引入原生对话框依赖。

### 14.3 排期建议（与 §10-F6 合并为「目录管理二期」）

| 批次 | 内容 | 依赖 | 说明 |
| --- | --- | --- | --- |
| 第一批 | E1、E3、E4、E8 | 无 | 纯前端，可与 §10-F6 第 3 项（会话删除入口）一起做一个迭代 |
| 第二批 | E6、E7、E9 | 无 | 状态/搜索类，需要测试补齐，可与第一批并行 |
| 第三批 | E2 | 后端新端点 + §6 安全条目 | 需先定枚举端点边界（隐藏项/符号链接/超时），再动前端 |
| 第四批 | E5 | 存储 v2 + 迁移回退 | 改动面小但影响既有偏好，单独提交便于回滚 |

- 每项均为**增量且可独立回滚**：不触碰 §11–§13 已交付的 CWD 接线、绑定防漂移与会话根解析语义。
- 若二期立项，建议把本节提升为独立文档，本文件保留 14.0 对照表作为索引。

### 14.4 与在飞方案的协调（`workspace-sidebar-directory-session-merge-plan.md`）

该合并方案（2026-09-15，**已落地**，纯前端）与本节部分条目同址；下表据此更新处置口径，
避免按已删除的 `sessions-section.tsx` 排期：

| 本文件条目 | 与合并方案的关系 | 建议处置 |
| --- | --- | --- |
| E1 折叠阈值 | **重叠**：其 §3.5-A / Phase 2 已承诺同一行为，并额外补了「被选中的会话若被上限隐藏则自动展开该组」 | 以合并方案为准交付；本文件只保留「空白会话不占名额」的补充约束与验收口径 |
| E2 目录选择器 | **互补**：需后端新端点，与该方案「纯前端、后端零改动」范围不冲突 | 独立立项；先定枚举端点边界（§14-E2 安全条目）再动前端 |
| E3 目录行动作收敛 | **互补且同址**：合并方案已把组头抽为 `directory-group-header.tsx`（动作区由 `directory-group-actions.tsx` 承载），当前仍是 hover 三图标 | 直接在 `directory-group-header.tsx` / `directory-group-actions.tsx` 上做（`directories-section.tsx` 只负责接线） |
| E4 HoverCard | **互补且受益**：合并后的组头具备「标签 / 计数 / 告警 / 落点 / 动作槽」结构，挂卡片更自然 | 建议排在合并方案 Phase 2 之后 |
| E5 groupBy 持久化 | **独立**：合并方案只做工具条整体迁入，不触碰存储 | 可并行；落点：合并后的 `session-browser-toolbar.tsx`（`sessions-section.tsx` 已删除） |
| E6 搜索健壮性 | **独立**：搜索弹窗不在合并范围内 | 可并行 |
| E7 unseen 提醒 | **互补且受益**：合并方案统一为单一 `session-row.tsx`，状态语义只需实现一处 | 建议排在合并方案 Phase 2 之后，直接落在统一行组件 |
| E8 行级 a11y | 同 E3/E7，落点随新组件（`directory-group-header.tsx` / `session-row.tsx`） | 与 E3 同批 |
| E9 重命名语义 | 合并方案 §3.5-E 会删除 `origin` 双入口机制，语义不变 | 与 E9 的测试用例一起补即可 |

- **排序结论**：合并方案 Phase 0–2（消除「同一会话在侧栏出现两行」）已落地，本节 UI 类增强
  （E1/E3/E4/E7/E8）可直接按合并后结构排期；E2/E5/E6/E9 与之并行不冲突。
- **文档维护约定**：合并方案 Phase 4 已回填本文件与优化台账（见 §0 变更记录）。本节作为「参考实现借鉴」
  的唯一入口，新增借鉴项一律记在此处，避免三份文档各自立排期。
