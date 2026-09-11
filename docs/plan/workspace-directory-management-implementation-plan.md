# Workspace 工作目录管理实施方案（目录增加/删除 + 目录内会话管理）

> 状态：草案（待评审）
> 日期：2026-09-10
> 涉及端：frontend（React + Vite）、backend（Go runtime-server，:8101）
> 关联页面：`http://localhost:8101/workspace/chats/new`

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
   需与 §6.2 的根目录白名单一并收敛。

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

### F6 🟡 后续增强（不阻塞本期，记录备查）

1. **子树归并**：注册目录聚合其子目录下 cwd 的会话（需产品确认计数口径）；
2. **目录健康巡检**：后台定时 `os.Stat` 注册目录，`exists=false` 主动提示而非仅在 GET 时发现；
3. **会话删除入口**：`deleteRuntimeSession` 已在 P3 顺带封装，后续可在侧边栏补删除 UI；
4. **根目录白名单**：`$AICLI_WORKSPACE_ROOTS` 限制可注册路径（多租户前置条件）。

### F7 🟡 审查中新识别的安全说明（已补充 → §6.7）

`POST /workspace-directories` 的存在性校验构成路径探测面；因服务端已暴露
`/fs/read-file`、`/fs/write-file`，该信息面严格更弱，本机信任模型下可接受，
已写入 §6 作为显式已知项而非隐性风险。

### F8 🟢 阶段依赖修正（已修正 → §5）

会话重命名仅依赖既有 PATCH 端点，不依赖 P1；已注明可随 P2 先行交付，
缩短可感知交付路径。

### 审查结论

方案总体成立：复用 `sessionmeta.WorkspacePath` 作为绑定事实、注册表与派生分组
双轨合并、删除目录不动文件系统三个核心决策均与现有代码语义自洽。F1 是唯一
会导致功能静默退化的实质缺陷，已在 §4.3.2 给出精确插入点；其余为一致性/开销/
语义澄清类修正，均已回写到正文。方案可进入评审/实施。

---

## 6. 实施后修复：会话执行 CWD 接线与绑定防漂移（2026-09-10）

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

### 已知边界（已于 §7 消除）

第一轮结束时文件工具的相对路径仍解析到全局注册 basePath，未随会话绑定
目录切换（preflight 与 shell 已按会话 root 解析）。该缺口已在第二轮增强中
修复，见 §7。

---

## 7. 第二轮增强：文件工具与 aicli_exec 的会话根感知（2026-09-10）

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

## 8. 第三轮审查修复（2026-09-10，实施后审查）

对 §6/§7 两轮实施做独立审查后确认的 2 个 major + 4 个次要项，均已修复。

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
  构造提示，而实际解析已改走会话根（§7）。目录绑定会话下，工具报错给出的
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
