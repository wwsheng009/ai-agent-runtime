# Agent Harness 技术设计详细规格

> 版本：v1.0  
> 日期：2026-09-20  
> 数据库：SQLite  
> 目标：将 Low-Token Multi-Language Agent Harness 从架构方案落到可实现的技术规格。  
> 适用：AI Coding Agent、Agent Runtime、Code Agent、IDE Agent、CLI Agent。

> **⚠️ 待补 schema 应用顺序声明（`04` 附录 B 的 B15）**
>
> 本文件目前**没有**声明 core schema 与 extension schema 的应用顺序。`03` §1.3 的
> `ALTER TABLE symbols ADD COLUMN stable_key/...` 依赖本文件 §15 的 `symbols` 定义；
> 只读本文件或只读 `03` 都会得到不完整的 schema。
>
> 待 `04` 附录 B 的 B15 转为 ADR 并被 Accept 后，在本文件顶部加入
> "core 先行；启用 extension 前必须先应用 `supplement/*` 的 ALTER" 一类的声明。
> **在此之前不执行本改动**，此处仅作标注。

---

# 1. 文档目标

本规格在《Low-Token Multi-Language AI Agent Harness 设计方案》的基础上，进一步定义：

- Agent Harness 模块边界
- SQLite 数据模型
- Universal Code Model（UCM）
- Code Index
- Language Adapter SPI
- LSP / Tree-sitter / Custom Adapter
- File Change / Incremental Index
- Exploration Cache
- Exploration Graph
- Context Planner
- Context Compiler
- Tool API
- Cache Invalidation
- Version Management
- Event Model
- API / Interface
- Agent Loop
- MVP 实施路线
- 测试和性能指标

核心目标：

> **让 Agent 从“不断探索代码”转变为“查询已经构建的代码知识，并只获取当前任务真正需要的上下文”。**

---

# 2. 总体设计原则

## 2.1 五项核心原则

```text
Incremental
Lazy
Cached
Versioned
Language-Agnostic
```

### Incremental

文件变化只重新处理变化部分。

### Lazy

没有被任务访问的代码不做深度分析。

### Cached

已经探索过的结果尽量复用。

### Versioned

所有代码知识和上下文都绑定代码版本。

### Language-Agnostic

Agent Runtime 不直接理解具体编程语言。

---

# 3. 系统总体架构

```text
┌──────────────────────────────────────────────────────────┐
│                    Agent Application                     │
│                                                          │
│  CLI / IDE / Server / Coding Agent                       │
└──────────────────────────┬───────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────┐
│                    Agent Runtime                         │
│                                                          │
│ Task Manager                                             │
│ Agent Loop                                               │
│ Tool Runtime                                             │
│ Memory                                                   │
└──────────────────────────┬───────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────┐
│                 Context Intelligence                     │
│                                                          │
│ Context Planner                                          │
│ Context Compiler                                         │
│ Observation Compressor                                   │
└──────────────────────────┬───────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────┐
│                 Code Knowledge Runtime                   │
│                                                          │
│ File Index                                               │
│ Symbol Index                                             │
│ Reference Index                                          │
│ Call Graph                                               │
│ Type Graph                                               │
│ Dependency Graph                                         │
│ Exploration Graph                                        │
│ Knowledge Cache                                           │
└──────────────────────────┬───────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────┐
│                 Universal Code Model                     │
└──────────────────────────┬───────────────────────────────┘
                           │
            ┌──────────────┼──────────────┐
            ▼              ▼              ▼
          LSP         Tree-sitter      Custom
            │              │              │
        Go/Java/...        AST           ABAP
                           │
                           ▼
                    Source Repository
                           ▲
                           │
                 Change Manager
                           ▲
              ┌────────────┼────────────┐
              │            │            │
            Agent         IDE          Git
             Edit        Changes       Changes
```

---

# 4. 推荐技术栈

第一版建议：

| 层 | 技术 |
|---|---|
| Agent Runtime | Go |
| HTTP API | Go net/http / chi |
| Database | SQLite |
| DB Driver | modernc.org/sqlite 或 mattn/go-sqlite3 |
| SQL Layer | database/sql + 手写 SQL，或 sqlc |
| Parser | Tree-sitter |
| Language Integration | LSP |
| File Watcher | fsnotify |
| Git | git CLI / go-git |
| Embedding | 可选 |
| Vector Store | 第一版不需要独立 Vector DB |
| Cache | SQLite + Process Memory |
| Serialization | JSON |
| Testing | Go test |
| Observability | OpenTelemetry / Prometheus 可选 |

推荐原则：

> 第一版尽量减少基础设施依赖。SQLite 足以承担单 Agent / 单 Workspace / 中小规模项目的 Code Knowledge Store。

---

# 5. 运行模式

系统支持三种模式。

## 5.1 Embedded

```text
Agent Process
 ├── Agent Runtime
 ├── Code Intelligence
 └── SQLite
```

适合 CLI Agent。

---

## 5.2 Local Service

```text
Agent
  │
  ▼
Code Knowledge Service
  │
  ▼
SQLite
```

适合多个 Agent Client。

---

## 5.3 Future Distributed

未来可以：

```text
Agent Runtime
      │
      ▼
Code Knowledge Service
      │
      ├── SQLite
      ├── PostgreSQL
      └── Vector DB
```

但 v1 不实现分布式数据库。

---

# 6. Workspace 模型

一个 Agent 工作空间对应一个 Workspace。

```text
Workspace
 ├── Repository
 ├── Branch
 ├── Working Tree
 ├── Index
 ├── Knowledge
 └── Tasks
```

示例：

```text
workspace_id = 01HX...
root = /workspace/my-project
```

---

# 7. SQLite 数据库

数据库文件：

```text
.agent/
    harness.db
```

建议：

```text
SQLite WAL mode
foreign_keys = ON
busy_timeout = 5000
```

初始化：

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
```

---

# 8. SQLite 数据模型总览

> **⚠️ 本块与下方 DDL 不一致（2026-09-20 核查）**
>
> 下列清单共 **28** 个名字，但本文件的 `CREATE TABLE` 区块（L320–1039）只定义了 **22** 张
> （另有 1 张虚拟表 `symbol_fts`，L2159）。**6 个名字在本文件中没有 DDL**：
> `branches`、`inheritance`、`dependencies`、`language_projects`、`index_jobs`、`events`。
>
> - `language_projects` / `events` 的处置见 [ADR-0001](adr/0001-project-module-language-schema.md) §4.7；
> - 其余 4 个见 [ADR-0007](adr/0007-phantom-tables-and-doc-invariants.md) §4.1，其中包含两处**命名漂移**
>   （`inheritance` vs `03.inheritance_edges`、`dependencies` vs `03.dependency_versions`）
>   与一处 **DDL 越位**（`index_jobs` 的 DDL 在 `04` L546，而非本文件）。
> - [ADR-0007](adr/0007-phantom-tables-and-doc-invariants.md) §4.2 要求本块拆为
>   **【v1 core】/【extension】/【deferred】三分组**，并配机械不变量检查（I1–I5）。
>   **在 ADR-0007 被 Accept 之前不执行重组**，此处仅作标注。
>
> **阅读本块时请勿假定其中每个名字都已存在。**

```text
workspaces
repositories
branches
commits

files
file_versions

symbols
symbol_versions

references
imports

calls
inheritance
dependencies

exploration_sessions
exploration_nodes
exploration_edges

tasks
task_files
task_symbols

context_snapshots
context_items

tool_calls
tool_results

cache_entries
invalidation_events

language_projects
index_jobs
events
```

---

# 9. Workspace 表

```sql
CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    root_path TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
```

---

# 10. Repository 表

```sql
CREATE TABLE repositories (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    vcs_type TEXT NOT NULL DEFAULT 'git',
    remote_url TEXT,
    current_branch TEXT,
    head_commit TEXT,
    working_tree_hash TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_repositories_workspace
ON repositories(workspace_id);
```

---

# 11. Commit 表

```sql
CREATE TABLE commits (
    id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL,
    commit_hash TEXT NOT NULL,
    parent_hash TEXT,
    author TEXT,
    message TEXT,
    committed_at INTEGER,

    UNIQUE(repository_id, commit_hash),

    FOREIGN KEY(repository_id)
        REFERENCES repositories(id)
        ON DELETE CASCADE
);
```

---

# 12. File 表

```sql
CREATE TABLE files (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    path TEXT NOT NULL,
    normalized_path TEXT NOT NULL,
    language TEXT,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    is_binary INTEGER NOT NULL DEFAULT 0,
    is_deleted INTEGER NOT NULL DEFAULT 0,
    current_hash TEXT,
    index_state TEXT NOT NULL DEFAULT 'UNINDEXED',
    index_version INTEGER NOT NULL DEFAULT 0,
    last_indexed_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    UNIQUE(workspace_id, normalized_path),

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_files_workspace
ON files(workspace_id);

CREATE INDEX idx_files_language
ON files(workspace_id, language);

CREATE INDEX idx_files_state
ON files(workspace_id, index_state);
```

---

# 13. File Index State

允许：

```text
UNINDEXED
LIGHT
DEEP
DIRTY
STALE
ERROR
```

状态：

```text
UNINDEXED
    ↓
LIGHT
    ↓
DEEP

DEEP
  ↓
DIRTY
  ↓
LIGHT
  ↓
DEEP
```

---

# 14. File Version

```sql
CREATE TABLE file_versions (
    id TEXT PRIMARY KEY,
    file_id TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    git_commit TEXT,
    size_bytes INTEGER,
    mtime_ns INTEGER,
    indexed_at INTEGER,
    parser_version TEXT,
    index_version INTEGER,

    UNIQUE(file_id, content_hash),

    FOREIGN KEY(file_id)
        REFERENCES files(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_file_versions_file
ON file_versions(file_id);
```

---

# 15. Symbol 表

```sql
CREATE TABLE symbols (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT NOT NULL,

    parent_id TEXT,
    name TEXT NOT NULL,
    qualified_name TEXT NOT NULL,

    kind TEXT NOT NULL,
    language TEXT NOT NULL,

    signature TEXT,
    visibility TEXT,

    start_line INTEGER,
    start_column INTEGER,
    end_line INTEGER,
    end_column INTEGER,

    confidence REAL NOT NULL DEFAULT 1.0,

    status TEXT NOT NULL DEFAULT 'FRESH',

    current_version INTEGER NOT NULL DEFAULT 0,

    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE,

    FOREIGN KEY(file_id)
        REFERENCES files(id)
        ON DELETE CASCADE,

    FOREIGN KEY(parent_id)
        REFERENCES symbols(id)
);

CREATE INDEX idx_symbols_file
ON symbols(file_id);

CREATE INDEX idx_symbols_name
ON symbols(workspace_id, name);

CREATE INDEX idx_symbols_qualified_name
ON symbols(workspace_id, qualified_name);

CREATE INDEX idx_symbols_kind
ON symbols(workspace_id, kind);
```

---

# 16. Symbol Kind

统一：

```text
MODULE
PACKAGE
NAMESPACE
CLASS
INTERFACE
STRUCT
ENUM
TYPE
FUNCTION
METHOD
FIELD
VARIABLE
CONSTANT
PROPERTY
PARAMETER
MACRO
ANNOTATION
PROGRAM
```

语言特有类型通过 extension 字段处理。

---

# 17. Symbol Version

```sql
CREATE TABLE symbol_versions (
    id TEXT PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    file_version_id TEXT NOT NULL,

    content_hash TEXT NOT NULL,
    signature TEXT,
    start_line INTEGER,
    end_line INTEGER,

    summary TEXT,

    created_at INTEGER NOT NULL,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE,

    FOREIGN KEY(file_version_id)
        REFERENCES file_versions(id)
        ON DELETE CASCADE
);
```

---

# 18. Reference 表

```sql
CREATE TABLE references (
    id TEXT PRIMARY KEY,

    workspace_id TEXT NOT NULL,
    source_symbol_id TEXT,
    target_symbol_id TEXT,

    file_id TEXT NOT NULL,

    name TEXT NOT NULL,

    start_line INTEGER,
    start_column INTEGER,
    end_line INTEGER,
    end_column INTEGER,

    resolution_status TEXT NOT NULL DEFAULT 'UNKNOWN',
    confidence REAL NOT NULL DEFAULT 0.5,

    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE,

    FOREIGN KEY(source_symbol_id)
        REFERENCES symbols(id)
        ON DELETE SET NULL,

    FOREIGN KEY(target_symbol_id)
        REFERENCES symbols(id)
        ON DELETE SET NULL,

    FOREIGN KEY(file_id)
        REFERENCES files(id)
        ON DELETE CASCADE
);
```

---

# 19. Call Graph

```sql
CREATE TABLE calls (
    id TEXT PRIMARY KEY,

    workspace_id TEXT NOT NULL,

    caller_symbol_id TEXT,
    callee_symbol_id TEXT,

    file_id TEXT NOT NULL,

    resolution_status TEXT NOT NULL DEFAULT 'UNKNOWN',
    confidence REAL NOT NULL DEFAULT 0.5,

    line INTEGER,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE,

    FOREIGN KEY(caller_symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE,

    FOREIGN KEY(callee_symbol_id)
        REFERENCES symbols(id)
        ON DELETE SET NULL,

    FOREIGN KEY(file_id)
        REFERENCES files(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_calls_caller
ON calls(caller_symbol_id);

CREATE INDEX idx_calls_callee
ON calls(callee_symbol_id);
```

---

# 20. Import / Dependency

```sql
CREATE TABLE imports (
    id TEXT PRIMARY KEY,

    workspace_id TEXT NOT NULL,
    file_id TEXT NOT NULL,

    target TEXT NOT NULL,
    resolved_file_id TEXT,

    confidence REAL NOT NULL DEFAULT 1.0,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE,

    FOREIGN KEY(file_id)
        REFERENCES files(id)
        ON DELETE CASCADE,

    FOREIGN KEY(resolved_file_id)
        REFERENCES files(id)
        ON DELETE SET NULL
);
```

---

# 21. Exploration Session

一次 Agent 任务可以产生一个 Exploration Session。

```sql
CREATE TABLE exploration_sessions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    task_id TEXT,

    status TEXT NOT NULL DEFAULT 'ACTIVE',

    started_at INTEGER NOT NULL,
    ended_at INTEGER,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);
```

---

# 22. Exploration Node

```sql
CREATE TABLE exploration_nodes (
    id TEXT PRIMARY KEY,

    session_id TEXT NOT NULL,
    task_id TEXT,

    node_type TEXT NOT NULL,

    target TEXT,

    file_id TEXT,
    symbol_id TEXT,

    summary TEXT,

    confidence REAL NOT NULL DEFAULT 0.5,

    source TEXT,

    version TEXT,

    created_at INTEGER NOT NULL,
    last_used_at INTEGER,

    FOREIGN KEY(session_id)
        REFERENCES exploration_sessions(id)
        ON DELETE CASCADE,

    FOREIGN KEY(file_id)
        REFERENCES files(id)
        ON DELETE SET NULL,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE SET NULL
);
```

---

# 23. Exploration Edge

```sql
CREATE TABLE exploration_edges (
    id TEXT PRIMARY KEY,

    session_id TEXT NOT NULL,

    from_node_id TEXT NOT NULL,
    to_node_id TEXT NOT NULL,

    relation TEXT NOT NULL,

    confidence REAL NOT NULL DEFAULT 0.5,

    source TEXT,

    FOREIGN KEY(session_id)
        REFERENCES exploration_sessions(id)
        ON DELETE CASCADE,

    FOREIGN KEY(from_node_id)
        REFERENCES exploration_nodes(id)
        ON DELETE CASCADE,

    FOREIGN KEY(to_node_id)
        REFERENCES exploration_nodes(id)
        ON DELETE CASCADE
);
```

关系：

```text
CONTAINS
CALLS
REFERENCES
IMPORTS
IMPLEMENTS
EXTENDS
DEPENDS_ON
RELATED_TO
DISCOVERED_FROM
```

---

# 24. Task 表

```sql
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,

    workspace_id TEXT NOT NULL,

    session_id TEXT,

    title TEXT,
    description TEXT,

    status TEXT NOT NULL DEFAULT 'ACTIVE',

    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE,

    FOREIGN KEY(session_id)
        REFERENCES exploration_sessions(id)
        ON DELETE SET NULL
);
```

---

# 25. Task 与 File

```sql
CREATE TABLE task_files (
    task_id TEXT NOT NULL,
    file_id TEXT NOT NULL,

    relevance REAL NOT NULL DEFAULT 0.5,

    PRIMARY KEY(task_id, file_id),

    FOREIGN KEY(task_id)
        REFERENCES tasks(id)
        ON DELETE CASCADE,

    FOREIGN KEY(file_id)
        REFERENCES files(id)
        ON DELETE CASCADE
);
```

---

# 26. Task 与 Symbol

```sql
CREATE TABLE task_symbols (
    task_id TEXT NOT NULL,
    symbol_id TEXT NOT NULL,

    relevance REAL NOT NULL DEFAULT 0.5,

    PRIMARY KEY(task_id, symbol_id),

    FOREIGN KEY(task_id)
        REFERENCES tasks(id)
        ON DELETE CASCADE,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);
```

---

# 27. Context Snapshot

Context Compiler 每次生成的 Context 可以保存快照。

```sql
CREATE TABLE context_snapshots (
    id TEXT PRIMARY KEY,

    task_id TEXT NOT NULL,

    version TEXT NOT NULL,

    token_estimate INTEGER NOT NULL DEFAULT 0,

    content_hash TEXT NOT NULL,

    created_at INTEGER NOT NULL,

    FOREIGN KEY(task_id)
        REFERENCES tasks(id)
        ON DELETE CASCADE
);
```

---

# 28. Context Item

```sql
CREATE TABLE context_items (
    id TEXT PRIMARY KEY,

    snapshot_id TEXT NOT NULL,

    item_type TEXT NOT NULL,

    source_id TEXT,

    content TEXT,

    token_estimate INTEGER NOT NULL DEFAULT 0,

    relevance REAL NOT NULL DEFAULT 0.5,

    version TEXT,

    FOREIGN KEY(snapshot_id)
        REFERENCES context_snapshots(id)
        ON DELETE CASCADE
);
```

---

# 29. Tool Call

```sql
CREATE TABLE tool_calls (
    id TEXT PRIMARY KEY,

    task_id TEXT,

    tool_name TEXT NOT NULL,

    arguments_json TEXT,

    started_at INTEGER NOT NULL,
    finished_at INTEGER,

    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,

    status TEXT,

    FOREIGN KEY(task_id)
        REFERENCES tasks(id)
        ON DELETE SET NULL
);
```

---

# 30. Tool Result

```sql
CREATE TABLE tool_results (
    id TEXT PRIMARY KEY,

    tool_call_id TEXT NOT NULL,

    raw_content TEXT,

    compressed_content TEXT,

    content_hash TEXT,

    token_estimate INTEGER DEFAULT 0,

    compressed_token_estimate INTEGER DEFAULT 0,

    FOREIGN KEY(tool_call_id)
        REFERENCES tool_calls(id)
        ON DELETE CASCADE
);
```

---

# 31. Cache

```sql
CREATE TABLE cache_entries (
    cache_key TEXT PRIMARY KEY,

    cache_type TEXT NOT NULL,

    value TEXT NOT NULL,

    version TEXT,

    dependencies_json TEXT,

    token_estimate INTEGER DEFAULT 0,

    created_at INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL,

    expires_at INTEGER
);

CREATE INDEX idx_cache_type
ON cache_entries(cache_type);
```

---

# 32. Invalidation Event

```sql
CREATE TABLE invalidation_events (
    id TEXT PRIMARY KEY,

    workspace_id TEXT NOT NULL,

    event_type TEXT NOT NULL,

    target_type TEXT NOT NULL,
    target_id TEXT,

    old_version TEXT,
    new_version TEXT,

    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);
```

---

# 33. Event Model

系统统一使用事件驱动。

核心事件：

```text
workspace.created

file.created
file.changed
file.deleted
file.renamed

agent.file.edited

git.checkout
git.pull
git.merge
git.rebase

index.started
index.light.completed
index.deep.completed
index.failed

symbol.changed
reference.changed
graph.changed

cache.invalidated
context.invalidated

task.created
task.updated
task.completed
```

---

# 34. File Change Event

```json
{
  "event": "file.changed",
  "workspace_id": "ws_123",
  "path": "internal/agent/executor.go",
  "old_hash": "abc123",
  "new_hash": "def456",
  "source": "agent"
}
```

---

# 35. Change Manager

职责：

```text
File Change Detection
Git Change Detection
Agent Edit Event
Hash Comparison
Rename Detection
Delete Detection
Debounce
```

流程：

```text
File Changed
      ↓
Normalize Path
      ↓
Hash
      ↓
Compare
      ↓
No Change → ignore
      ↓
Changed
      ↓
Mark DIRTY
      ↓
Index Job
```

---

# 36. Agent 编辑路径

Agent 修改文件时：

```text
EditTool
  ↓
Write File
  ↓
Calculate Hash
  ↓
Emit agent.file.edited
  ↓
Change Manager
  ↓
Incremental Index
```

不要等待 filesystem watcher。

---

# 37. Filesystem Watcher

监听：

```text
CREATE
WRITE
REMOVE
RENAME
```

使用 debounce：

```text
300~1000ms
```

避免：

```text
每个字符
→ parse
→ index
```

---

# 38. Git Synchronization

周期性检查：

```text
HEAD
Branch
Working Tree
```

发现：

```text
indexed_commit != current_commit
```

执行：

```text
git diff
```

获取 changed files。

只更新受影响文件。

---

# 39. Incremental Index Pipeline

```text
Changed File
    ↓
Language Detection
    ↓
Parser
    ↓
AST
    ↓
Symbol Extraction
    ↓
Light Index
    ↓
Reference Resolution
    ↓
Graph Update
    ↓
Deep Index
```

---

# 40. Fast Index

Fast Index 必须尽可能快：

```text
File
 ↓
Parser
 ↓
AST
 ↓
Symbols
 ↓
Imports
```

目标：

```text
单文件 < 100~300ms
```

具体性能取决于语言和文件大小。

---

# 41. Deep Index

异步执行：

```text
References
Type Resolution
Call Graph
Inheritance
Dependency
Semantic Summary
```

Deep Index 不应阻塞 Agent。

---

# 42. Language Detection

优先级：

```text
1. Extension
2. Shebang
3. Build configuration
4. Parser detection
```

例如：

```text
.go → Go
.java → Java
.py → Python
.ts → TypeScript
.abap → ABAP
```

---

# 43. Language Adapter SPI

建议定义统一接口：

```go
type LanguageAdapter interface {
    ID() string

    Detect(path string, content []byte) bool

    Parse(ctx context.Context, input ParseInput) (*ParseResult, error)

    ExtractSymbols(
        ctx context.Context,
        tree *SyntaxTree,
    ) ([]Symbol, error)

    ExtractImports(
        ctx context.Context,
        tree *SyntaxTree,
    ) ([]Import, error)

    Capabilities() Capabilities
}
```

---

# 44. Semantic Adapter

```go
type SemanticAdapter interface {
    ResolveDefinition(
        ctx context.Context,
        query DefinitionQuery,
    ) ([]Definition, error)

    FindReferences(
        ctx context.Context,
        query ReferenceQuery,
    ) ([]Reference, error)

    FindImplementations(
        ctx context.Context,
        query ImplementationQuery,
    ) ([]Symbol, error)

    FindCalls(
        ctx context.Context,
        query CallQuery,
    ) ([]Call, error)
}
```

---

# 45. LSP Adapter

```go
type LSPAdapter interface {
    Initialize(ctx context.Context) error

    Definition(
        ctx context.Context,
        file string,
        line int,
        column int,
    ) ([]Location, error)

    References(
        ctx context.Context,
        file string,
        line int,
        column int,
    ) ([]Location, error)

    WorkspaceSymbols(
        ctx context.Context,
        query string,
    ) ([]Symbol, error)

    Shutdown(ctx context.Context) error
}
```

---

# 46. Adapter Capability

```go
type Capabilities struct {
    Syntax          bool
    Symbols         bool
    Definitions     bool
    References      bool
    Types           bool
    Calls           bool
    Implementations bool
    Diagnostics     bool
    RuntimeEvidence bool
}
```

这样 Context Planner 可以知道：

```text
当前语言能做到什么。
```

---

# 47. Tool API

LLM 不直接操作底层索引。

推荐：

```text
code.search
code.inspect
code.navigate
code.trace
code.impact
code.diff
```

---

# 48. code.search

输入：

```json
{
  "query": "timeout",
  "scope": "backend",
  "limit": 20
}
```

输出：

```json
{
  "matches": [
    {
      "type": "symbol",
      "name": "TimeoutConfig",
      "file": "config.go",
      "line": 42,
      "relevance": 0.94
    }
  ],
  "total": 183,
  "truncated": true
}
```

---

# 49. code.inspect

```json
{
  "symbol": "Agent.run",
  "include": [
    "definition",
    "calls",
    "callers",
    "references",
    "tests"
  ]
}
```

返回结构化结果，而不是整文件。

---

# 50. code.navigate

```json
{
  "from": "Agent.run",
  "relation": "calls",
  "depth": 2
}
```

返回：

```text
Agent.run
 └── ToolExecutor.execute
      └── ToolRuntime.execute
```

---

# 51. code.trace

```json
{
  "from": "HTTPHandler",
  "to": "Database"
}
```

返回可能路径：

```text
HTTPHandler
 → Service
 → Repository
 → Database
```

---

# 52. code.impact

```json
{
  "symbol": "TimeoutConfig"
}
```

返回：

```text
Affected:
ToolExecutor.execute
Agent.run
AgentTest
```

---

# 53. code.diff

用于告诉 Agent：

```text
当前修改了什么。
```

输出：

```text
changed files
changed symbols
added symbols
deleted symbols
affected symbols
```

---

# 54. Context Planner

Context Planner 输入：

```text
User Task
Current Task State
Project Knowledge
Exploration Graph
Code Graph
Git Diff
Cache
```

输出：

```json
{
  "need_exploration": false,
  "required_symbols": [
    "ToolExecutor.execute",
    "TimeoutConfig"
  ],
  "required_files": [
    "executor.go",
    "config.go"
  ],
  "confidence": 0.94
}
```

---

# 55. Confidence 策略

建议：

```text
0.90 - 1.00
直接使用

0.70 - 0.90
局部验证

0.40 - 0.70
补充探索

< 0.40
重新分析
```

这是默认策略，实际阈值应通过 telemetry 调整。

---

# 56. Context Compiler

Context Compiler 根据 Planner 结果生成最终 LLM Context。

输入：

```text
System Context
+
Project Context
+
Task Context
+
Relevant Code
+
Tool Results
```

输出：

```text
Minimal Sufficient Context
```

原则：

> 最小充分上下文，而不是最大上下文。

---

# 57. Context Item 优先级

建议：

```text
P0:
当前用户任务

P1:
当前修改文件

P2:
直接相关 Symbol

P3:
调用者 / 被调用者

P4:
测试

P5:
模块摘要

P6:
项目级摘要

P7:
无关代码
```

Context Budget 不足时从低优先级开始裁剪。

---

# 58. Observation Compressor

Tool 原始输出：

```text
1000~10000 tokens
```

压缩：

```text
100~500 tokens
```

压缩内容：

```text
What was found
Where
Why relevant
Confidence
Version
Next possible action
```

不要把所有 raw output 永久放入上下文。

---

# 59. Raw Tool Output Storage

原始结果仍然可以保存：

```text
tool_results.raw_content
```

但：

```text
LLM Context
```

默认只使用：

```text
compressed_content
```

需要时再展开。

---

# 60. Cache Key

推荐：

```text
workspace
+
repository
+
commit
+
working_tree_hash
+
query
+
language
+
scope
```

示例：

```text
ws1:
repo1:
commit8f31:
query=Agent.run:
scope=backend
```

---

# 61. Cache Dependency

每个 Cache 记录：

```json
{
  "dependencies": [
    "file:executor.go@def456",
    "symbol:ToolExecutor.execute@17",
    "symbol:TimeoutConfig@19"
  ]
}
```

任一 dependency 变化：

```text
Cache = STALE
```

---

# 62. Cache Invalidation

```text
File Changed
      ↓
Symbol Changed
      ↓
Reference Changed
      ↓
Graph Changed
      ↓
Invalidate:
  symbol cache
  exploration cache
  context cache
  semantic cache
```

不是简单：

```text
clear all cache
```

---

# 63. SQLite 并发策略

SQLite 适合：

```text
Single Agent
Single Workspace
Local Coding Agent
```

推荐：

```text
WAL
busy_timeout
short transactions
batch writes
prepared statements
```

索引写入不要每条记录 commit。

应该：

```text
BEGIN
  delete old symbols
  insert new symbols
  update references
  update calls
COMMIT
```

---

# 64. SQLite 数据分层

推荐：

```text
Persistent:
files
symbols
references
calls
tasks
exploration

Cache:
context
tool results
semantic cache

Ephemeral:
in-memory planner state
LLM state
active tool execution
```

---

# 65. SQLite 数据库大小

Code Index 可能增长较快。

因此：

```text
VACUUM
ANALYZE
WAL checkpoint
```

定期维护。

历史版本不要无限保留。

建议：

```text
当前版本：永久
最近 N 个版本：保留
更旧版本：GC
```

---

# 66. Knowledge Garbage Collection

定期删除：

```text
过期 cache
无引用 exploration node
旧 symbol version
旧 tool result
旧 context snapshot
```

但 Git / Audit 需要的记录可以单独保留。

---

# 67. Agent Loop 详细流程

```text
1. Receive User Task
       ↓
2. Create Task
       ↓
3. Context Planner
       ↓
4. Query Knowledge Runtime
       ↓
5. Build Context
       ↓
6. LLM
       ↓
7. Tool / Edit / Answer
       ↓
8. Capture Observation
       ↓
9. Compress Observation
       ↓
10. Update Knowledge
       ↓
11. Re-plan
       ↓
12. Continue / Finish
```

---

# 68. Agent 编辑后的处理

```text
LLM
 ↓
edit_file
 ↓
write
 ↓
hash
 ↓
file.changed
 ↓
mark DIRTY
 ↓
fast index
 ↓
update symbols
 ↓
invalidate context
 ↓
deep index async
```

这样下一轮 LLM 就不会看到旧代码。

---

# 69. 多轮会话

第一轮：

```text
探索 Agent.run
探索 ToolExecutor
探索 TimeoutConfig
```

保存：

```text
Exploration Graph
```

第二轮：

```text
用户：
改成 60 秒
```

Planner：

```text
已有相关知识
confidence = 0.96
```

直接使用：

```text
TimeoutConfig
ToolExecutor
```

不会重新 grep。

---

# 70. 多语言项目

例如：

```text
frontend/
  TypeScript

backend/
  Java

worker/
  Python

agent/
  Go

sap/
  ABAP
```

Code Knowledge Runtime 统一：

```text
Project
 ├── TypeScript Symbols
 ├── Java Symbols
 ├── Python Symbols
 ├── Go Symbols
 └── ABAP Symbols
```

跨语言关系也可以进入 Graph：

```text
TypeScript API
    ↓
Java REST Controller
    ↓
Python RPC
    ↓
Go Worker
```

---

# 71. 跨语言关系

Universal Graph 支持：

```text
HTTP_CALL
RPC_CALL
MESSAGE
DATABASE
FILE
PROCESS
CLI
```

这些不是语言特性，而是架构关系。

例如：

```text
TypeScript
  └── HTTP_CALL
       ↓
Java Controller
```

---

# 72. Runtime Evidence

静态分析不能解决所有问题。

未来可以加入：

```text
Static Evidence
+
Runtime Evidence
+
Git Evidence
```

例如：

```text
Static:
A → B → C

Runtime:
A → B → D

Git:
B 最近修改

Confidence:
D = 0.88
```

Runtime Evidence v1 不实现，但 UCM 应预留接口。

---

# 73. Semantic Search

第一版：

```text
SQLite FTS5
```

即可实现：

```text
symbol search
identifier search
text search
summary search
```

例如：

```sql
CREATE VIRTUAL TABLE symbol_fts
USING fts5(
    name,
    qualified_name,
    signature,
    summary,
    content='symbols'
);
```

Vector Search 延后。

---

# 74. 为什么第一版不需要 Vector DB

因为：

```text
find_symbol
find_definition
find_reference
find_callers
```

结构化查询比 embedding 更准确。

Vector Search 主要用于：

```text
模糊语义查询
自然语言代码搜索
```

因此第一版：

```text
SQLite
+
FTS5
+
Symbol Graph
```

足够。

---

# 75. Context Retrieval Pipeline

```text
User Query
    ↓
Query Analyzer
    ↓
Exact Symbol Search
    ↓
Graph Search
    ↓
FTS5
    ↓
Semantic Search (optional)
    ↓
Rank
    ↓
Context Compiler
```

---

# 76. Ranking

可以计算：

```text
Score =
  lexical_score
+ symbol_score
+ graph_score
+ task_relevance
+ recency
+ confidence
```

例如：

```text
symbol exact match：高
直接 caller：高
同模块：中
语义相关：中
其他模块：低
```

---

# 77. 项目摘要

Project Summary 不需要每次重新生成。

可以缓存：

```text
project architecture
modules
entrypoints
important symbols
build system
language distribution
```

文件变化后：

```text
只标记 stale
```

需要时再更新。

---

# 78. Module Summary

例如：

```text
internal/agent

Purpose:
Agent orchestration

Important symbols:
Agent
Agent.run
AgentLoop

Dependencies:
Planner
ToolExecutor
Memory
```

---

# 79. Symbol Summary

例如：

```text
ToolExecutor.execute

Purpose:
Execute registered tools

Calls:
ToolRuntime.execute

Called by:
Agent.run

Tests:
executor_test.go

Confidence:
0.96

Version:
executor.go@def456
```

---

# 80. 代码读取策略

不要：

```text
read_file 1~1000
```

优先：

```text
read_symbol
```

只有：

```text
symbol unavailable
```

才 fallback：

```text
read file region
```

---

# 81. Fallback Strategy

Code Intelligence 不完整时：

```text
UCM
 ↓
LSP
 ↓
Tree-sitter
 ↓
FTS5
 ↓
Regex/Grep
```

保证系统不会因为某个语言没有 Adapter 就无法工作。

---

# 82. Adapter 优先级

```text
Custom Semantic Adapter
       ↓
LSP
       ↓
Parser
       ↓
Text Search
```

能力越强，优先级越高。

---

# 83. 错误处理

任何 Code Intelligence 失败：

```text
Adapter Error
```

不能直接导致 Agent 失败。

应该：

```text
Adapter
 ↓
Error
 ↓
Fallback
 ↓
Lower Fidelity Result
```

例如：

```text
LSP unavailable
 ↓
Tree-sitter
 ↓
Symbol Index
```

---

# 84. Offline 模式

Agent Harness 应支持没有 LSP 的情况：

```text
Parser
+
SQLite
+
FTS5
```

仍可以：

```text
search
symbol
file
dependency
basic graph
```

---

# 85. 目录结构

推荐 Go 项目：

```text
agent-harness/
├── cmd/
│   └── harness/
│
├── internal/
│   ├── agent/
│   ├── task/
│   ├── context/
│   ├── knowledge/
│   ├── code/
│   │   ├── index/
│   │   ├── graph/
│   │   ├── retrieval/
│   │   └── change/
│   │
│   ├── language/
│   │   ├── detector/
│   │   ├── lsp/
│   │   ├── parser/
│   │   └── adapters/
│   │
│   ├── storage/
│   │   └── sqlite/
│   │
│   ├── tools/
│   │
│   └── telemetry/
│
├── pkg/
│   └── api/
│
├── migrations/
│
├── testdata/
│
└── docs/
```

---

# 86. Interface Boundary

核心接口：

```go
type CodeKnowledge interface {
    Search(ctx context.Context, q SearchQuery) (*SearchResult, error)

    Inspect(ctx context.Context, q InspectQuery) (*InspectResult, error)

    Navigate(ctx context.Context, q NavigateQuery) (*GraphResult, error)

    Trace(ctx context.Context, q TraceQuery) (*TraceResult, error)

    Impact(ctx context.Context, q ImpactQuery) (*ImpactResult, error)

    ApplyChange(ctx context.Context, change Change) error
}
```

---

# 87. Indexer Interface

```go
type Indexer interface {
    IndexLight(
        ctx context.Context,
        file FileInput,
    ) error

    IndexDeep(
        ctx context.Context,
        file FileInput,
    ) error

    Reindex(
        ctx context.Context,
        files []FileInput,
    ) error
}
```

---

# 88. Change Manager Interface

```go
type ChangeManager interface {
    HandleFileChange(
        ctx context.Context,
        event FileChangeEvent,
    ) error

    HandleGitChange(
        ctx context.Context,
        event GitChangeEvent,
    ) error
}
```

---

# 89. Context Compiler Interface

```go
type ContextCompiler interface {
    Compile(
        ctx context.Context,
        input CompileInput,
    ) (*CompiledContext, error)
}
```

---

# 90. Context Planner Interface

```go
type ContextPlanner interface {
    Plan(
        ctx context.Context,
        input PlanningInput,
    ) (*ContextPlan, error)
}
```

---

# 91. Context Plan

```go
type ContextPlan struct {
    NeedExploration bool

    Files []ResourceRef
    Symbols []ResourceRef

    GraphQueries []GraphQuery

    TokenBudget int

    Confidence float64
}
```

---

# 92. Context Compiler 算法

伪代码：

```text
compile(task):

    plan = planner.plan(task)

    candidates = retrieve(
        files=plan.files,
        symbols=plan.symbols,
        graph=plan.graph_queries
    )

    candidates = rank(candidates)

    context = []

    for candidate in candidates:
        if budget_available(candidate):
            context.append(candidate)

    context = compress(context)

    return context
```

---

# 93. Exploration Cache 命中

```text
query
 ↓
normalize
 ↓
cache lookup
 ↓
version validation
 ↓
hit?
 ├── YES → reuse
 └── NO  → explore
```

---

# 94. Version Validation

```text
cache.version == current.version
```

如果：

```text
true
```

可以复用。

如果：

```text
false
```

进入：

```text
stale
```

重新验证。

---

# 95. Stale 不等于删除

重要原则：

```text
STALE
```

知识仍然可以作为：

```text
historical evidence
```

但不能作为：

```text
current source of truth
```

---

# 96. Git History Knowledge

未来可以支持：

```text
why changed
who changed
when changed
related commits
```

例如用户：

```text
为什么这里 timeout 是 30 秒？
```

Agent 可以查询 Git History。

这比重新探索代码更高效。

---

# 97. Test Intelligence

Symbol 可以关联测试：

```text
ToolExecutor.execute
       ↓
executor_test.go
```

实现：

```text
find_tests(symbol)
```

修改代码后：

```text
Impact
 ↓
Tests
```

Context Compiler 可以自动加入最相关测试。

---

# 98. Diff Intelligence

修改完成后：

```text
Git Diff
 ↓
Changed Symbols
 ↓
Impact
 ↓
Tests
 ↓
Validation
```

Agent 不需要重新扫描整个项目。

---

# 99. Token Budget

每次 Task 设置：

```text
max_context_tokens
max_exploration_tokens
max_tool_result_tokens
```

例如：

```text
Context = 20k
Exploration = 5k
Tool result = 2k
```

超过预算：

```text
compress
rank
drop low relevance
```

---

# 100. Token Cost Policy

可以定义：

```text
Exploration Budget
```

如果：

```text
exploration_tokens > threshold
```

触发：

```text
Exploration Review
```

让 Context Planner 检查：

```text
是不是重复探索？
是不是已有知识？
是不是应该提高查询粒度？
```

---

# 101. Anti-Repetition Detector

检测：

```text
同一文件重复 read
同一 symbol 重复 search
相同 query 重复执行
同一 graph path 重复探索
```

例如：

```text
read executor.go
```

第二次：

```text
cache hit
```

第三次：

```text
禁止重复 raw read，除非版本变化
```

---

# 102. Tool Result Deduplication

通过：

```text
content_hash
```

判断：

```text
结果是否相同。
```

相同：

```text
不要重新注入 Context。
```

---

# 103. Agent 自我纠错

如果 LLM 根据旧知识修改了代码：

```text
validation
 ↓
发现 symbol changed
 ↓
Context invalid
 ↓
recompile context
 ↓
retry
```

---

# 104. 安全边界

Code Intelligence 只提供：

```text
Evidence
```

不直接决定：

```text
是否修改
```

流程：

```text
Evidence
 ↓
LLM Decision
 ↓
Diff
 ↓
Validation
 ↓
Apply
```

---

# 105. 可靠性等级

每个 Code Evidence 带：

```text
source
confidence
version
timestamp
```

例如：

```json
{
  "source": "LSP",
  "confidence": 0.98,
  "version": "executor.go@def456"
}
```

---

# 106. Telemetry

必须记录：

```text
task_id
session_id
model
input_tokens
output_tokens

exploration_tokens
tool_tokens
context_tokens

tool_calls
files_read
symbols_read

cache_hit
cache_miss

index_hit
index_miss

context_reuse
repeated_exploration

latency
```

---

# 107. 核心 KPI

推荐：

```text
Exploration Token Ratio

Repeated Exploration Ratio

Context Cache Hit Rate

Code Retrieval Precision

Tokens / Task

Tokens / Successful Change

Average Tool Calls / Task

Average Files Read / Task

Index Update Latency

Context Compilation Latency
```

---

# 108. 目标指标

MVP 可以设：

```text
重复探索 Token
< 20%

Context Cache Hit
> 50%

常见 Symbol 查询
< 100ms

Light Index
< 300ms / file

Context Compiler
< 200ms（缓存命中）
```

这些是工程目标，不是保证值，应根据实际项目调整。

---

# 109. 测试策略

## Unit Test

测试：

```text
Parser
Symbol extraction
Hash
Version
Cache
Invalidation
Ranking
Context budget
```

## Integration Test

测试：

```text
Agent edit
 ↓
Index
 ↓
Cache invalidate
 ↓
Context refresh
```

## Golden Test

固定项目：

```text
testdata/go
testdata/java
testdata/python
testdata/typescript
testdata/abap
```

验证：

```text
expected symbols
expected references
expected calls
expected context
```

---

# 110. Multi-language Test Matrix

至少准备：

```text
Go
Java
Python
TypeScript
Rust
C/C++
ABAP
```

每种语言测试：

```text
File
Symbol
Definition
Reference
Call
Import
Test
Change
```

---

# 111. Failure Injection

模拟：

```text
LSP unavailable
Parser failure
SQLite locked
File deleted
File renamed
Git checkout
Concurrent edit
Corrupted cache
Stale context
```

系统都应该：

```text
fallback
recover
invalidate
rebuild
```

而不是整个 Agent 失败。

---

# 112. MVP 第一阶段

只做：

```text
SQLite
File Index
Language Detection
Tree-sitter
Symbol Index
Exploration Cache
Change Manager
Context Compiler
FTS5
```

暂时不做：

```text
复杂 Vector DB
完整 Type Graph
Runtime Analysis
复杂跨语言语义
```

---

# 113. MVP 第二阶段

增加：

```text
LSP
References
Definition
Call Graph
Impact Analysis
Test Intelligence
```

---

# 114. MVP 第三阶段

增加：

```text
Semantic Search
Embedding
Cross-language Graph
Runtime Evidence
Git History Intelligence
```

---

# 115. ABAP Adapter 路线

ABAP 是特殊语言。

第一阶段：

```text
ABAP syntax
REPORT
CLASS
METHOD
FUNCTION MODULE
FORM
```

第二阶段：

```text
DDIC
CDS
TABLE
DATA ELEMENT
DOMAIN
```

第三阶段：

```text
BAdI
Enhancement
Transaction
Function Module dependency
```

第四阶段：

```text
SAP Runtime Evidence
STAD
SMGW
Work Process
RFC/JCo
```

这样未来可以和 ABAP Runtime / ABAP Compiler 项目结合。

---

# 116. 未来从 SQLite 演进

SQLite 是 v1 的默认方案。

当出现：

```text
多个 Agent
多个 Workspace
远程 Code Intelligence
数百万 symbol
团队共享知识
```

再演进：

```text
SQLite
 ↓
PostgreSQL
 ↓
PostgreSQL + Vector DB
```

核心 UCM / API 不改变。

---

# 117. 不应绑定数据库

所有上层模块只依赖：

```go
Repository Interfaces
```

例如：

```go
type SymbolRepository interface {
    FindByName(...)
    FindByQualifiedName(...)
    FindReferences(...)
}
```

SQLite 只是实现。

---

# 118. Repository Interface

```go
type FileRepository interface {
    Get(ctx context.Context, id string) (*File, error)
    GetByPath(ctx context.Context, path string) (*File, error)
    Upsert(ctx context.Context, file *File) error
    MarkDirty(ctx context.Context, id string) error
}
```

---

# 119. SQLite Migration

使用：

```text
migrations/
    001_init.sql
    002_symbols.sql
    003_exploration.sql
    004_context.sql
    005_cache.sql
```

不要手工修改生产数据库。

---

# 120. 初始化流程

```text
harness init
    ↓
Create .agent/
    ↓
Create harness.db
    ↓
SQLite PRAGMA
    ↓
Run migrations
    ↓
Detect repository
    ↓
Detect languages
    ↓
Build LIGHT index
```

---

# 121. Workspace 初始化

```text
Project
 ↓
Git detect
 ↓
File scan
 ↓
Ignore:
  .git
  node_modules
  vendor
  build
  dist
  target
  .venv
  generated
```

具体 ignore 规则可以由语言和项目配置扩展。

---

# 122. Generated Code

生成代码默认：

```text
LOW PRIORITY
```

但不能简单忽略。

例如：

```text
generated API
protobuf
OpenAPI
ORM
```

需要：

```text
generated=true
source_relation
```

---

# 123. Vendor / Dependency

默认：

```text
不深度索引第三方依赖。
```

只建立：

```text
package/module dependency
```

用户明确进入第三方代码时：

```text
Lazy Index
```

---

# 124. Monorepo

Workspace 支持：

```text
repository
 ├── project A
 ├── project B
 ├── project C
```

Project Scope：

```text
workspace
repository
module
package
```

Context Planner 默认限制 Scope。

---

# 125. 大型项目

大型项目不能把全部 Graph 放内存。

采用：

```text
SQLite persistent graph
+
memory cache
```

只加载：

```text
current task subgraph
```

---

# 126. Context Virtual Memory

可以把 Context 看成 RAM：

```text
Repository
 ↓
Persistent Knowledge
 ↓
Knowledge Cache
 ↓
Context Cache
 ↓
LLM Context
```

不是所有代码都进入 LLM。

---

# 127. 最终 Agent Tool 数量

建议：

```text
code.search
code.inspect
code.navigate
code.trace
code.impact
code.diff
```

基础：

```text
edit_file
run_command
run_test
git_diff
```

不要给 LLM 暴露几十个底层搜索工具。

---

# 128. 最终 Agent 行为

传统：

```text
LLM
 ↓
grep
 ↓
read
 ↓
grep
 ↓
read
 ↓
grep
```

新架构：

```text
LLM
 ↓
Context Planner
 ↓
Code Knowledge
 ↓
Context Compiler
 ↓
LLM
 ↓
edit
 ↓
incremental index
 ↓
validate
```

---

# 129. 关键架构结论

整个系统最终形成三个稳定边界：

```text
┌──────────────────────────────────────────┐
│              Agent Runtime               │
│                                          │
│ Task / Planning / Tool / Memory / LLM   │
└────────────────────┬─────────────────────┘
                     │
                     │ Stable API
                     ▼
┌──────────────────────────────────────────┐
│          Code Knowledge Runtime           │
│                                          │
│ Index / Graph / Cache / Context Compiler │
└────────────────────┬─────────────────────┘
                     │
                     │ Language Bridge
                     ▼
┌──────────────────────────────────────────┐
│            Language Ecosystem             │
│                                          │
│ LSP / Tree-sitter / Compiler / Custom    │
│ Go / Java / Python / TS / ABAP / ...    │
└──────────────────────────────────────────┘
```

---

# 130. 最终原则

不要把系统设计成：

```text
“一个支持所有语言的超级代码分析器”
```

而应设计成：

```text
“一个语言无关的 Agent Code Knowledge Runtime”
```

核心负责：

```text
Index
Graph
Version
Cache
Invalidation
Exploration
Context
```

语言生态负责：

```text
Parse
Symbol
Definition
Reference
Type
Call
```

LLM 负责：

```text
Understand
Reason
Plan
Modify
Validate
```

---

# 131. 最终目标

最终系统：

```text
                       User
                        │
                        ▼
                  Agent Runtime
                        │
                        ▼
                 Context Planner
                        │
                        ▼
              Code Knowledge Runtime
                        │
       ┌────────────────┼────────────────┐
       │                │                │
    File Index      Symbol Graph      Cache
       │                │                │
       └────────────────┼────────────────┘
                        │
                Universal Code Model
                        │
             ┌──────────┼──────────┐
             │          │          │
            LSP      Tree-sitter  Custom
             │          │          │
          Go/Java/...   AST       ABAP
                        │
                        ▼
                    Repository
                        ▲
                        │
                Change Manager
                        │
                Agent / IDE / Git
```

最终 LLM 每次获得：

```text
最新
+
相关
+
最小
+
可验证
```

的上下文。

因此 Agent 的核心工作从：

```text
探索项目
```

转变为：

```text
理解问题
推理
修改
验证
```

这就是整个 Low-Token Agent Harness 的最终技术目标。
