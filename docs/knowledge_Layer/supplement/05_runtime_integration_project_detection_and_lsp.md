# 05 — 知识层与 Runtime 集成、项目类型感知与 LSP 接入

> 定位：**supplement（扩展规格）**，不是第 5 份并列设计文档（遵循 `README.md` §7 约束）。
> 事实源边界：core schema 仍以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以 `03` 拆分后的 `supplement/*` 为准。**本文件只新增"集成 / 检测 / LSP"三类规格，不复制 DDL。**
> 落地计划与验收口径仍以 `04_completeness_review_and_optimized_plan.md` 为准；本文件是其 §4.5 / §4.7 / Phase 4 的展开。
> 状态：设计完成，待评审。依赖 04 的 Phase 0（基线）→ Phase 1（索引 MVP）→ Phase 4（Adapter SPI 与 LSP）。

---

## 0. 本文回答的问题

1. 知识库层如何与现有 runtime 集成？
2. 在 `runtime-server` / `aicli cmd tui` / `aicli acp` 三种入口如何使用？
3. 如何自动智能感知当前项目的类型？目录里有多种类型项目（Go backend + frontend React/Vue）怎么办？
4. LSP 服务如何接入？Go 项目如何连 `gopls`？
5. 没有安装 LSP 服务如何处理？

---

## 1. 集成总原则

### 1.1 为什么是"进程内库 + 可选 LSP 子进程"，不是独立服务

已裁决（见 `README.md` §3 与 `04` §3.2），理由在本仓库语境下是硬的：

| 反方案 | 本仓库中的具体反证 |
|---|---|
| 独立 knowledge 服务 | 多一个端口 / 生命周期 / 鉴权面 / **多一个 SQLite writer**。`internal/sqliteutil/sqliteutil.go` 顶部注释已记录 `aicli local` 与 `runtime-server` 同库写竞争导致"启动长时间无响应"的事故；再加一个进程只会放大该问题。 |
| 只在 runtime-server 内实现 | `aicli local` 必须能离线独立工作（无 server 场景），知识层不可用会直接破坏既有 TUI 体验。 |
| 每语言写一个完整分析器 | `01` §7 已明确 Adapter 三来源（LSP / tree-sitter-parser / custom），本文件沿用它，不重写。 |

**结论**：知识层是一个可嵌入的 Go 包 `backend/internal/knowledge/`，由 `cmd/runtime-server` 与 `cmd/aicli` 共同消费；LSP 是它按需 spawn 的**可选**子进程。

### 1.2 依赖方向与现有包映射（全部为已核实的仓库包）

```
cmd/runtime-server ─┐
cmd/aicli (tui)    ─┼─→ internal/knowledge ─┬─→ internal/sqliteutil      （DB 打开/pragma/重试）
cmd/aicli (acp)    ─┘                       ├─→ internal/fsscope          （文件访问边界权威源）
                                            ├─→ internal/workspace        （扫描；需修正测试文件策略）
                                            ├─→ internal/artifact         （大结果归档）
                                            ├─→ internal/background       （索引任务）
                                            ├─→ internal/observability    （metrics/tracing）
                                            └─→ internal/knowledge/lsp    （新增，JSON-RPC 子进程）
        ↑ 反向不被依赖（knowledge 不 import acp / chat / ui）
```

与既有组件的集成方式（**方向明确，避免"双上下文 + 三套记忆"**）：

| 现有组件 | 集成方式 | 方向 |
|---|---|---|
| `internal/sqliteutil` | knowledge DB 必须走同一套打开/pragma/busy_timeout/重试 | 复用，不另起 |
| `internal/workspace` | 扫描器升级：`*.test.*` 由"忽略"改为"索引并标 `is_test`" | **修改**（见 §3.4） |
| `internal/fsscope` | 文件可见性/忽略的权威源；knowledge 不自行发明规则 | 复用 |
| `internal/contextmgr` | 新增 knowledge 对 `LayerPlan`/`Budget` 的贡献；不替换既有 Hot/Warm/Cold | **扩展** |
| `internal/contextpack` | 新增一个 `knowledge` Provider，与现有 provider 并列 | **扩展** |
| `internal/memorystore` | **不合并**。它是"笔记"；knowledge 的 exploration memory 是"任务工作集" | 边界清晰 |
| `internal/factledger` | **不合并**。knowledge 产出的事实可**单向**写入 factledger 作为证据 | 单向 |
| `internal/usageledger` | 新增探索归因字段（`explore_reason` / `knowledge_hit` / `confidence` / `would_hit`） | **扩展** |
| `internal/toolkit` / `internal/tools` | 注册 `code.*`；`code.*` 内部 fallback 到 `grep`/`view` | **扩展** |
| `internal/policy` | `code.*` 走同一 capability/policy 引擎，**无特权** | 复用 |
| `internal/compactruntime` | knowledge snippet 参与压缩预算 | 复用 |
| `internal/workspaceregistry` | 多 workspace 下 DB 路径注册与复用 | **扩展** |
| `internal/aiclipaths` | DB 路径 `<workspace>/.aicli/knowledge/knowledge.db` | 复用 |

### 1.3 owner 选举与读写降级（补 04 的 G2）

**问题**：`aicli local` 与 `runtime-server` 可能同时打开同一 workspace 的 knowledge DB。

**方案**：单写者仲裁 + 只读降级，**不依赖 SQLite 自身锁**（SQLite 的 WAL + busy_timeout 已被证明不足以解决本仓库的写饿死）。

```
<workspace>/.aicli/knowledge/owner.lock
内容（JSON）：{ "pid": 1234, "role": "writer", "started_at": "...", "heartbeat_at": "...", "host": "..." }
```

规则：
1. 打开 DB 前先尝试获取 `owner.lock`（`O_CREATE|O_EXCL` 或带 CAS 的原子替换）。
2. 若锁存在且 `heartbeat_at` 新鲜（< 30s）→ 本进程降级为 **reader**：以 `mode=ro&_query_only=1` 打开，**不注册写任务**。
3. 若 `heartbeat_at` 过期 → 尝试接管（CAS 更新 pid/heartbeat）；接管成功即成为 writer。
4. writer 每 10s 刷新 heartbeat；正常退出时删除锁。
5. reader 读到的快照带 `snapshot_ts` 与 `staleness = now - last_commit_ts`；`knowledge/status` 必须暴露 `role` 与 `owner_pid`。
6. reader 模式下 `code.*` 仍可用，只是数据陈旧；**绝不因为不能写而拒绝服务**。

> 为什么不用"第二个进程只读打开 WAL 就没事"：WAL 允许并发读，但本仓库的实际故障是**写者被长事务饿死**，只读打开不解决写侧排队；而且 `_query_only` 未设置时，某些驱动路径仍可能尝试获取写锁。

### 1.4 三种运行模式

`knowledge.mode = off | shadow | on`

| mode | 行为 | 用途 |
|---|---|---|
| `off`（**默认**） | 不打开 DB、不注册 `code.*`、不注入上下文。行为与今天**字节级一致** | Phase 0 之前 / 排障 / 用户显式关闭 |
| `shadow` | 后台索引 + **不暴露工具给模型**；每次 `grep`/`view` 调用时并行跑一次 knowledge 查询，只记录 `would_hit`/`would_miss`/`diff` 到 `usageledger` | Phase 1 差异率来源；零用户可见风险 |
| `on` | 工具暴露 + contextpack provider 生效 | Phase 2+ |

配置落点：`configs/runtime.yaml` 与 `configs/config.yaml` 新增 `knowledge:` 段；`aicli` 命令支持 `--knowledge=off|shadow|on` 覆盖（见 §2.2）。

**硬约束**：`mode=off` 时不得改变任何现有行为——包括不得创建 `.aicli/knowledge/` 目录。

---

## 2. 三种入口的用法

### 2.1 runtime-server（`backend/cmd/runtime-server/main.go`）

- **启动阶段**：在 workspace 解析之后、session runtime 初始化之前调用 `knowledge.Open`。runtime-server 是长驻进程，**默认角色是 writer owner**（除非已有其它 owner）。
- **后台任务**：向 `internal/background` 注册 `knowledge.index.initial` 与 `knowledge.index.incremental`；索引不阻塞启动、不阻塞 turn。
- **HTTP 面**（挂在既有 workspace 作用域下，不新增独立前缀）：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/workspaces/{ws}/knowledge/status` | mode / role / owner_pid / snapshot_ts / staleness / 表计数 / 索引进度 / 降级原因 |
| POST | `/api/workspaces/{ws}/knowledge/reindex` | 触发全量或增量（可选 `paths`） |
| GET | `/api/workspaces/{ws}/knowledge/projects` | 检测到的项目列表 + confidence + evidence |
| GET | `/api/workspaces/{ws}/knowledge/lsp` | LSP 状态矩阵（语言 / server / 状态 / pid / last_error / hint） |
| POST | `/api/workspaces/{ws}/knowledge/lsp/{lang}/restart` | 重启某语言的 LSP |

- **工具执行仍走既有 tool broker**，不新增执行通道；`code.*` 与 `grep`/`view` 共用 policy/approval/artifact 链路。
- **多实例**：第二个 runtime-server 启动时若 `owner.lock` 被占 → reader，`/knowledge/status` 返回 `role:"reader"` 与 `owner_pid`。

### 2.2 aicli cmd / tui（`backend/cmd/aicli/`）

- **启动**：`commands/chat.go` 解析 workspace → `knowledge.Open`。
  - 若 runtime-server 在跑且是 owner → aicli 是 reader（**最常见形态**）。
  - 若无 server → aicli 竞争 owner。
- **TUI 呈现**：
  - 状态栏（`ui/statusbar.go`）增加可选的 `KB` 指示：`off` / `KB·ok` / `KB·ro` / `KB·idx 42%` / `KB·err`。
  - 首次索引进度复用 `ui/progress.go`，作为**非阻塞状态行**，不占 transcript。
- **`/knowledge` 斜杠命令**（纳入 `commands/chat_*_command.go` 体系）：

| 命令 | 行为 |
|---|---|
| `/knowledge status` | 与 HTTP status 同构 |
| `/knowledge projects` | 打印检测结果表（含 evidence 与 confidence） |
| `/knowledge reindex [path]` | 触发重索引 |
| `/knowledge lsp` | 打印 LSP 矩阵 |
| `/knowledge lsp restart <lang>` | 重启某语言 server |
| `/knowledge lsp install <lang>` | 展示建议命令并**请求审批**后才执行（见 §5.4） |
| `/knowledge off\|shadow\|on` | **会话级**切换，只覆盖本次会话，不写配置文件 |

- **`aicli exec`（非交互）**：默认跟随配置；`--knowledge=off|shadow|on` 可覆盖。短任务默认倾向 `off`/`shadow`；若 DB 已存在且新鲜，`on` 无额外成本。
- **硬要求**：TUI 启动**不得**被索引进度阻塞。索引未完成时 `code.*` 返回 `partial:true` + 已覆盖范围。

### 2.3 aicli acp（`backend/internal/acp/`、`backend/cmd/aicli/commands/agent_stdio*.go`、`acp_mcp_host.go`）

ACP 是 editor 通过 stdio JSON-RPC 驱动 aicli 作为 agent 的协议（已核实：`internal/acp/` 有 `conn.go` / `server.go` / `types.go` / `mcp_types.go` / `doc.go`）。

- **能力协商**：**不新增顶层 capability**（避免老 client 拒绝）。knowledge 是**服务端内部能力**，通过两条路径暴露：
  1. **工具面（主路径）**：`code.*` 作为普通 tool 出现在 `session/new` 的 tools 列表里。editor 无需知道 knowledge 存在，模型自行调用。
  2. **资源面（可选，v1 可推迟）**：通过 MCP 桥（`acp_mcp_host.go`）把 `knowledge/projects`、`knowledge/status` 暴露为 MCP resource，让 editor 能显示项目类型与索引状态。
- **workspace root 来源**：**必须取 `session/new` 携带的 roots/cwd**，不要用进程 cwd（editor 可能从任意目录启动 aicli）。在 session 建立阶段将其传入 `knowledge.Open`。
- **权限**：`code.*` 走既有 `internal/policy` capability。ACP 的 approval 流程（`agent_stdio_approval_recovery.go`）天然覆盖；只读的 `code.find_*` 建议声明为与 `grep`/`view` 同级、免审批。
- **ACP 场景下 LSP 归属（重要）**：editor 自己通常已有 LSP（Zed 有 gopls/Volar）。**不要抢**。默认 `knowledge.lsp.mode = off`：**不自启 LSP**，退到 parser；用户可经会话级 **select** 选项显式切到 `self`。否则会出现两个 `gopls` 指向同一 `go.mod`，内存翻倍且互相干扰。

  > **已裁决 → [ADR-0002](../adr/0002-acp-lsp-ownership.md)**。原表述 `external_preferred` 已废弃：ACP v1 的能力面（`backend/internal/acp/types.go`）中**不存在任何 LSP 能力位**，"先探测 editor 是否提供 LSP"在 v1 **无对象可探测**。ADR-0002 另说明三点：为何用 **select** 而非 boolean（boolean 受 `clientCapabilities.session.configOptions.boolean` 门控，触达面小，见 `internal/acp/doc.go:14-15`）、为何 v1 不提供 `external` 枚举值（与 `off` 行为相同，属 no-op 选项）、以及进程内重复防护的锁位置。
- **无 workspace 场景**：若 client 不提供 root → knowledge 直接 `off`，`code.*` 不注册，模型只用 `grep`/`view`。**必须优雅降级，不报错。**

### 2.4 入口对照表

| 维度 | runtime-server | aicli tui | aicli acp |
|---|---|---|---|
| 默认角色 | writer owner | reader（有 server 时）/ 竞争 owner | reader 优先 |
| workspace 锚点 | workspace 绑定 | 进程 cwd / 显式 `--workspace` | **`session/new` roots** |
| 索引触发 | 启动后 background | 启动后 background | session 建立后 background |
| 进度可见性 | HTTP status | 状态栏 | 静默（经 MCP resource 可选暴露） |
| LSP 默认策略 | 自管 | 自管 | `off`（见 ADR-0002） |
| 无 root 时 | 不适用 | 不适用 | `mode=off` 优雅降级 |
| 会话级 mode 覆盖 | HTTP 参数 | `/knowledge off\|shadow\|on` | `session/new` 配置项（若 client 支持） |

---

## 3. 项目类型自动感知

目标：一个目录下有 Go backend + frontend(React/Vue) 时，识别出**两个 project**、各自的语言/工具链/LSP 需求，而不是把整个目录当成一个 Go 项目。

### 3.1 三层模型

```
Workspace（用户打开 / ACP root 的目录）
  └── Project（可独立构建 / 依赖管理的最小单元）
        └── Module（可选：go.work 的多个 module、pnpm workspace 的多个 package）
```

对应表：`workspace`（1 行）、`projects`、`project_modules`、`project_languages`、`project_evidence`（逐条证据，用于解释 confidence）。

> 与 `02`/`03` 的关系：`03` 已有 `projects/modules` 概念，本文件**不新增并列概念**，而是把 `03` 的 `projects/modules` 明确为"Project/Module 层"，把 `02` 的 `language_projects` 收敛为 `project_languages` 的视图。**该收敛已裁决 → [ADR-0001](../adr/0001-project-module-language-schema.md)**（对应 `04` 附录 B 的 B6）。

### 3.2 检测信号（分层，权重递减）

**Tier A — 显式配置（最高权重，可覆盖一切）**

- `.aicli/knowledge.yaml` 中的 `projects[]` 显式声明
- 用户会话级覆盖（`/knowledge projects --set ...`）

**Tier B — 构建 / 包管理清单（决定性信号）**

| 文件 | project kind | language | toolchain | LSP 候选 |
|---|---|---|---|---|
| `go.mod` | go-module | go | go | gopls |
| `go.work` | go-workspace | go | go | gopls（多 module） |
| `package.json` | node-package | js/ts | npm/yarn/pnpm/bun | typescript-language-server |
| `pnpm-workspace.yaml` | node-monorepo | js/ts | pnpm | 同上 |
| `package-lock.json` / `yarn.lock` / `pnpm-lock.yaml` | （锁文件，仅判定包管理器） | — | — | — |
| `tsconfig.json` | （TS 标记，通常与 package.json 共存） | ts | — | — |
| `vite.config.{ts,js,mts,mjs}` | frontend-vite | ts/js | vite | + volar（若 vue） |
| `vue.config.js` / `nuxt.config.{ts,js}` | frontend-vue | vue/ts | vue-cli / nuxt | volar |
| `next.config.{ts,js,mjs}` | frontend-next | ts/js | next | typescript-language-server |
| `Cargo.toml` | rust-crate | rust | cargo | rust-analyzer |
| `pyproject.toml` / `setup.py` / `requirements.txt` | python-project | python | pip/poetry/uv | pyright / ruff-lsp |
| `pom.xml` / `build.gradle{,.kts}` | jvm-project | java/kotlin | maven/gradle | jdtls / kotlin-lsp |
| `*.csproj` / `*.sln` | dotnet-project | c# | dotnet | omnisharp |
| `CMakeLists.txt` | cmake-project | c/cpp | cmake | clangd |

**Tier C — 框架 / 库依赖（细化 frontend 子类型）**

读 `package.json` 的 `dependencies` / `devDependencies`：

| 依赖 | 推断 |
|---|---|
| `react` / `react-dom` | React |
| `vue` | Vue |
| `@angular/core` | Angular |
| `svelte` / `@sveltejs/kit` | Svelte |
| `solid-js` | Solid |
| `typescript` | TS（否则 JS） |
| `vite` / `webpack` / `rollup` / `esbuild` / `rspack` | bundler |
| `vitest` / `jest` / `playwright` / `cypress` | 测试框架（决定 test index 方式） |
| `eslint` / `prettier` / `biome` | lint 工具 |

**Tier D — 文件扩展名统计（fallback，仅在 Tier B 全 miss 时）**

- 有界扫描（`depth ≤ 4`，排除 ignore 集），统计扩展名直方图。
- 例：`.go ≥ 5 个且占比 > 60%` → go；`.ts/.tsx` 主导 → ts。
- **confidence 上限 0.6**，evidence = 直方图快照。

**Tier E — 目录名启发（最低，仅用于命名，不用于判定语言）**

- `backend/`、`server/`、`api/` → **不**强行判定 Go
- `frontend/`、`web/`、`ui/`、`client/` → **不**强行判定 React
- 用途：给 project 一个人类可读名，而非判定依据。

### 3.3 多项目（polyglot monorepo）算法

以 `backend/`（`go.mod`）+ `frontend/`（`package.json` + `vite.config.ts` + `vue`）为例：

1. **自顶向下 walk**：遇到 Tier B 文件即认为该目录是一个 project root。发现 root 后**不再向上合并**，但**继续向下**寻找嵌套 root。
2. **嵌套规则**：
   - `go.work` 存在 → 其中的 `use` 目录各成为一个 **module**（同属一个 Go project）。
   - `pnpm-workspace.yaml` / `package.json#workspaces` 存在 → 各 package 成为 **module**。
   - 同一目录同时有 `go.mod` 和 `package.json`（合法，如 e2e 目录）→ **两者都记**，project 变为 multi-kind，**不是冲突**。
3. **冲突处理（真正的歧义）**：
   - 同层级出现互斥前端框架配置（`next.config.ts` + `nuxt.config.ts`）→ 记 `ambiguity_penalty`，两个候选都保留，confidence 打折，并在 `/knowledge projects` 显示候选。
   - `package.json` 无 `dependencies` 也无 `devDependencies` → 只判定为 node-package，不猜框架。
4. **输出**：projects 列表，每个带 `root`（相对 workspace 的路径）、`kind`、`languages`、`toolchain`、`lsp` 候选、`confidence`、`evidence[]`。
5. **`root` 的绝对化**：一律存相对路径，查询时再拼 workspace 绝对路径——保证 DB 跨机器可移植。

### 3.4 排除集修正（必须改 `internal/workspace/scanner.go`）

已核实的现状（`backend/internal/workspace/scanner.go` L62、L65-76）：

```go
regexp.MustCompile(`.*\.test\.(go|py|js|ts)$`), // 测试文件  ← 被忽略
ignorePathComponents = { ".git",".idea",".vscode","node_modules","vendor","dist","build","__pycache__",".DS_Store",".aicli" }
```

**问题**：把 `*.test.*` 整个忽略，会导致方案里的 `code.tests` / `TestIndex` **永久为空**；且该正则过于宽泛（会误伤 `foo.testutil.ts` 之类命名）。

**修正**：

1. `*.test.*` 由"忽略"改为"**索引并标记 `is_test=true`**"。
2. 测试判定改为可配置的多规则集，而不是单一正则：
   - Go：`*_test.go`
   - JS/TS：`*.test.ts` / `*.spec.ts` / `__tests__/**`
   - Python：`test_*.py` / `*_test.py` / `tests/**`
3. `is_test` / `is_generated`（`*.pb.go`、`zz_generated*`）/ `is_vendor` 三个标记位落到文件表。
4. **ignore 集补齐**：`.next`、`.nuxt`、`.turbo`、`.venv`、`venv`、`target`（rust）、`bin`、`obj`（dotnet）、`coverage`、`.pytest_cache`、`.mypy_cache`、`.cache`。
5. **Windows 特化**：路径比较大小写不敏感（`node_modules` vs `NODE_MODULES` 都要命中）；用 `filepath.Clean` + 统一小写比较。
6. **权威源**：ignore 集的最终权威是 `internal/fsscope`，`workspace/scanner.go` 应改为消费它，而不是维护第二份列表。

### 3.5 project ID 与重命名

- `project_key = H(rel_path_of_root + "\x00" + kind)`——不用绝对路径（跨机器可移植），不用目录名单独做键（重命名会漂移）。
- 目录重命名 → 表现为"old 消失 + new 出现"。用 Tier B 文件的**内容 hash + 依赖指纹**做 alias 迁移：若新 root 的 `go.mod` module path 与旧的一致 → 判定为同一 project，迁移 `stable_key` 并保留历史。
- 与 `04` §4.4 的 `stable_key` 算法共用同一套 alias 迁移逻辑，不另写。

### 3.6 confidence 公式（对接 04 §4.4）

```
confidence = clamp( Σ w_i · s_i · (1 - ambiguity_penalty) · (1 - staleness_penalty), 0, 1 )
```

| 层 | w | s（命中且可解析时） | 上限 |
|---|---|---|---|
| Tier A | 1.0 | 1.0 | 1.0 |
| Tier B | 0.9 | 1.0 | 1.0 |
| Tier C | 0.6 | 1.0 | 0.9 |
| Tier D | 0.3 | 1.0 | 0.6 |
| Tier E | 0.1 | 1.0 | 0.3 |

- `ambiguity_penalty`：同层互斥 kind 同时命中 → 0.3。
- `staleness_penalty`：Tier B 文件 mtime 落后于 lock 文件且目录内容 hash 变化未重扫 → 按 `04` §4.4。
- **禁止**在没有 Tier A/B 的情况下报 confidence > 0.6。

### 3.7 触发时机与成本

| 时机 | 动作 |
|---|---|
| session 建立（TUI 启动 / ACP `session/new` / server 绑定 workspace） | 全量检测 |
| `go.mod` / `package.json` / lock 文件变更 | **立即**重检测（这几个文件少且关键，可 watch） |
| 其它文件变更 | 5s debounce + 定期 reconcile（fsnotify 在 Windows 不可靠） |
| 显式 `/knowledge reindex` 或 HTTP reindex | 全量 |

**成本控制**：检测本身只 `stat` 约 20 个已知文件名（Tier B），**毫秒级**。全量扩展名统计（Tier D）仅在 Tier B 全 miss 时才做，且有 depth 与文件数上限。

### 3.8 多项目下的检索 / 工具语义

- `code.*` 带可选 `project` 作用域参数；缺省 = 全部 project，返回带 project 标注。
- `code.find_refs` 严格在 project 内，除非显式 `cross_project=true`——避免 Go backend 与前端构建产物互相污染。
- **上下文组装只放相关 project 的摘要**（用户谈 React 组件时不塞 Go 符号表）。这是 knowledge 层省 token 的主要来源之一，也是 `04` §7 收益指标的落脚点。

### 3.9 输出契约（喂给模型 / 上下文）

```json
{
  "workspace_root": "<ws>",
  "projects": [
    {
      "name": "backend", "root": "backend", "kind": "go-module",
      "languages": ["go"], "toolchain": {"go": "1.23"},
      "lsp": ["gopls"], "confidence": 0.98,
      "evidence": ["go.mod", "go.sum", "*.go=137"]
    },
    {
      "name": "frontend", "root": "frontend", "kind": "frontend-vue",
      "languages": ["ts", "vue"],
      "toolchain": {"package_manager": "pnpm", "bundler": "vite", "framework": "vue"},
      "lsp": ["volar", "typescript-language-server"], "confidence": 0.96,
      "evidence": ["package.json:deps.vue", "vite.config.ts", "pnpm-lock.yaml"]
    }
  ]
}
```

---

## 4. LSP 接入

**现状核实**：仓库中**没有** `internal/lsp` 包（对 `backend` 全量 grep `gopls|LSP|lsp|language server` 仅命中 `internal/acp/cancel_request_test.go` 一处子串误报）。因此 LSP 是完全新增能力，必须落在 `04` 的 Phase 4，且 **Phase 1–3 必须在无 LSP 条件下完整可用**。

### 4.1 Adapter SPI 与"能力位"

沿用 `01` §7 的三来源：

```
Language Adapter
├── LSP Adapter         精度最高，需外部 server
├── Tree-sitter/Parser  内置，无外部依赖
└── Custom Adapter      语言特有启发（如 go/ast）
```

```go
type Adapter interface {
    Language() string
    // 索引期
    IndexFile(ctx context.Context, file string, content []byte) (*FileIndex, error)
    // 能力声明（关键：按位降级，而非按"有没有 LSP"降级）
    Capabilities() AdapterCaps
    // 查询期
    FindSymbol(ctx context.Context, name string, scope Scope) ([]Symbol, error)
    FindRefs(ctx context.Context, sym Symbol) ([]Ref, error)
    Callers(ctx context.Context, sym Symbol) ([]Symbol, error)
    Diagnostics(ctx context.Context, file string) ([]Diagnostic, error)
}

type AdapterCaps struct {
    HasRefs           bool
    HasCallHierarchy  bool
    HasTypes          bool
    HasDiagnostics    bool
    HasWorkspaceSymbol bool
}
```

**为什么必须是能力位**：即使 `gopls` 在场，某些查询（跨 module 的 workspace symbol、未 didOpen 的文件）仍可能不可用；按"有没有 LSP"做二值降级会导致"有 gopls 但结果为空"被误判为"没有引用"。按能力位降级才能给出正确的 `completeness`。

### 4.2 LSP Manager（新增 `backend/internal/knowledge/lsp/`）

职责与要点：

1. **发现**：语言 → server 候选列表（可配置）。Go 默认 `gopls`。
2. **定位可执行文件**（按序，结果缓存）：
   - `knowledge.lsp.servers.go.command` 显式配置（绝对路径）
   - workspace 本地 `<ws>/.aicli/lsp/bin/gopls{,.exe}`（允许项目自带，保证版本一致）
   - `PATH` 查找（Windows 注意 `.exe` / `.cmd` / `.bat`）
   - `GOBIN` / `GOPATH/bin`（Go 特有）
   - `go env GOBIN` / `GOPATH` 的解析结果（延迟执行，带超时）
3. **版本探测**：`gopls version`（超时 2s），记录 version 并纳入 `knowledge_version`（`04` §4.4）；版本变化 → 触发重新索引。
4. **启动**：
   - `exec.CommandContext(ctx, "gopls", "-mode=stdio")`，stdio JSON-RPC，`Content-Length` 分帧（与 LSP 规范一致）。
   - `initialize` 参数：
     - `processId`：自身 pid
     - `rootUri`：**project root 的 `file://` URI，不是 workspace root**——多项目时这是关键，否则 `gopls` 会把整个 monorepo 当一个 module
     - `workspaceFolders`：Go 场景一个实例一个 root 更稳；`go.work` 存在时给 `go.work` 所在目录，由 `gopls` 自行处理多 module
     - `capabilities`：只声明真正用到的（`textDocument/references`、`callHierarchy`、`workspace/symbol`、`publishDiagnostics`），不声明用不上的，避免 server 做无用功
     - `initializationOptions`：`gopls` 特有——`buildFlags`（build tags）、`env`（`GOFLAGS`）、`analyses`（关掉昂贵分析以省内存）
   - **必须处理 server 反向请求**：`window/workDoneProgress/create`、`$/progress`、`client/registerCapability`、`workspace/configuration`。不处理会导致部分 server 挂起等待。
5. **生命周期**：
   - **按需启动**：首次 LSP 查询才 spawn，不在索引期盲启（否则打开一个没有 Go 的 workspace 也会白起 `gopls`）。
   - **空闲回收**：默认 5 min 无请求 → `shutdown` / `exit`，可配置。
   - **崩溃重启**：指数退避 1s/2s/4s/…上限 60s；连续 5 次失败 → 标记 `unavailable`，本 session 不再尝试，降级到 parser。
   - **并发**：每个 `(language, project_root)` 一个实例；同一 root 不得起多个。
6. **同步**：索引期已有文件内容 → 用 `textDocument/didOpen` 注入内存态，而不是让 server 读磁盘（减少 IO 且保证与索引一致）。文件变更 → `didChange`。另一半是 server 主动推 `publishDiagnostics`。
7. **超时与取消**：每个请求带 ctx + 超时（默认 3s；`workspace/symbol` 放宽到 10s）。超时 → 降级 parser 并记 `lsp_timeout` 指标。**绝不允许 LSP 阻塞 turn。**

### 4.3 Go 项目接入 `gopls`（端到端）

```
检测到 go.mod（project root = backend/）
  → Adapter = GoAdapter
      ├── 内置：go/parser + go/ast → 符号表 / 导入图 / 包结构（无外部依赖）
      └── 可选：gopls
  → 索引期（Phase 1）：只用 go/parser（快、稳、无依赖）
  → 查询期（Phase 4）：
      需要 FindRefs / Callers / 类型信息 / Diagnostics
        → gopls 可用且 project 已 didOpen → LSP 查询
        → 否则 → go/parser 的保守结果（定义能找到，引用不全）
        → 结果统一带 source: "lsp" | "parser" | "heuristic" + completeness
```

**为什么索引期不依赖 `gopls`**：`gopls` 对大型仓库的首次 `workspace/symbol` 可能几十秒，且需要完整加载所有依赖。索引期用 `go/parser` 可做到"秒级出基础符号表"，之后 `gopls` 慢慢补精度——这是分阶段收敛，不是二选一。

Go 特有注意点：

- `GOFLAGS` 的 `-mod=mod` vs `-mod=vendor` 影响解析；存在 `vendor/` 时要读 `vendor/modules.txt`。
- build tags：默认只解析非排除文件；`GOOS` / `GOARCH` 由 `go env` 决定。
- 多 module（`go.work`）：`gopls` 支持但要求版本 ≥ 0.13，版本探测要做硬门槛。
- 生成的 `*.pb.go`、`zz_generated*` 标 `is_generated`，不进符号主索引（否则淹没真实符号）。

### 4.4 前端（React / Vue）接入

- 探测：`package.json` 含 `vue` → Vue；含 `react` → React；有 `tsconfig.json` → TS。
- LSP：
  - TS/JS：`typescript-language-server --stdio`。**优先用 workspace 本地 `<ws>/node_modules/typescript`**，否则全局。
  - Vue：`@vue/language-server`（Volar），对 `.vue` 提供 LSP，内部代理 TS。
  - React 无独立 LSP：用 TS LSP（+ 可选 ESLint LSP）。
- 坑：
  - Node 项目 LSP 通常在 `node_modules/.bin/`，Windows 下是 `.cmd` shim。spawn `.cmd` 需 `cmd.exe /c`；**推荐改用 `node <path-to-js>` 直连**（更可控，避免 shell 注入面）。
  - `node_modules` 必须在**我们自己的索引**中排除，但 LSP 仍需读它（类型定义）——排除的是索引，不是 LSP 视野。两者不可混淆。
  - monorepo 多 package → 每 package 一个 TS server 实例过重；优先根 `tsconfig.json` + project references；无则按 workspace 顶层配置起一个。

---

## 5. 没有安装 LSP 怎么办

### 5.1 降级阶梯（能力位驱动）

| 层 | 名称 | 提供能力 | 外部依赖 |
|---|---|---|---|
| L0 | LSP Adapter | refs / callers / types / diagnostics（精确） | 需 server |
| L1 | Parser Adapter | symbols / imports / 同文件保守 refs | 无（`go/parser`、tree-sitter 或结构启发） |
| L2 | 结构启发 | 命名/目录约定（`*_test.go`、`internal/`） | 无 |
| L3 | 纯文本 | `grep` / `view` 原始能力（等价 `mode=off` 行为） | 无 |

**关键**：每一层不是全有全无。`FindRefs` 在 L0 精确；L1 只能给"定义点 + 文本匹配"；L2/L3 给"可能相关文件"。返回结构带 `source` 与 `completeness`，让模型知道该不该相信。

### 5.2 检测不到 LSP 时的行为契约

1. **不阻塞**：索引、`code.*` 工具、turn 全部照常。LSP 只影响精度，不影响可用性。
2. **提示一次，不反复骚扰**：TUI 首轮若用户触发需要 LSP 的查询（如"这个函数被谁调用"），状态栏短暂提示 `KB·parser (gopls 未安装)`；详情保留在 `/knowledge lsp`。**不是每个 turn 都提示。**
3. **记录证据**：写入 LSP 状态表 `{lang, server, status:"not_found", search_paths:[...], hint:"..."}`。
4. **给建议命令，不自动执行**：
   - **明确不做静默安装。** 理由：安装是网络 + 磁盘 + 版本决策，属用户环境变更；且不同项目可能需要不同 `gopls` 版本（由 `go.mod` 的 go 版本决定）。agent 不得越权。
   - 提供的 hint 必须精确可复制：
     - Go：`go install golang.org/x/tools/gopls@latest`（并提示检查 `GOBIN` 是否在 `PATH`）
     - TS：`npm i -D typescript typescript-language-server`（项目本地优先）
     - Vue：`npm i -D @vue/language-server`
     - Rust：`rustup component add rust-analyzer`
   - 只有用户显式同意（TUI approval 或 `/knowledge lsp install go`）才执行，且走既有 `shell` 工具 + policy 审批链。
5. **可恢复**：装好后 `/knowledge lsp restart go`，或下次 session 自动重探测。探测结果缓存 TTL 5 min；失败态 TTL 更长，避免反复 spawn 探测。
6. **配置逃生舱**：`knowledge.lsp.enabled=false` 全局关闭；`knowledge.lsp.servers.go.command` 显式指定路径绕开 PATH 探测。

### 5.3 精度预期（必须诚实标注）

| 能力 | L0 LSP | L1 Parser | L2 / L3 |
|---|---|---|---|
| 符号定义 | 精确（含类型） | 精确（Go 用 `go/ast`） | 文件名/命名猜测 |
| 引用查找 | 精确（含跨包） | 同文件 + 同名文本（可能误报） | 文本匹配 |
| 调用层级 | 精确 | 不可靠 | 不可用 |
| 类型信息 | 精确 | 局部 | 无 |
| 诊断 | server 诊断 | 语法错误 | 无 |
| 跨语言 | 有限 | 无 | 无 |

此表要同时进 `code.*` 的返回（`source` + `completeness`）与 `knowledge/status`。

### 5.4 Windows 特有坑（必须写进实现清单）

- `.exe` / `.cmd` / `.bat` 的 PATH 探测与 exec 语义不同。
- 长路径（>260）需 `\\?\` 前缀或启用 long path。
- **进程树清理**：Go 的 `Process.Kill()` 在 Windows 上不杀子进程；`gopls` 会派生 `go list` 等。需用 Job Object，或 `taskkill /F /T /PID` 兜底。
- 路径大小写不敏感 → 所有路径比较需规范化（`filepath.Clean` + 统一小写）。
- **LSP position 编码**：LSP 用 UTF-16 code units 做 position，Go 的 `utf8` 字节偏移不能直接换算，必须转码——这是最常见的行号错位 bug 源。CRLF/LF 混用会放大该问题。

---

## 6. 端到端时序（首次打开 monorepo）

```
1. TUI 启动 / ACP session/new / runtime-server 绑定 workspace
2. knowledge.Open
     - 解析 workspace root（TUI: cwd；ACP: session roots；server: workspace 绑定）
     - 打开/创建 <ws>/.aicli/knowledge/knowledge.db（走 sqliteutil）
     - 竞争 owner.lock → role = writer | reader
     - mode=off → 到此为止，行为与今天完全一致
3. 项目检测（Tier A→E，毫秒级）
     → projects = [backend(go, conf .98), frontend(vue+ts, conf .96)]
     → 写 projects / project_languages / project_evidence
4. 后台索引（background task）
     - 按 project 并行（Go 用 go/parser；前端用 ts parser / 结构启发）
     - 尊重 fsscope + ignore 集；测试文件标 is_test 而非忽略
     - 进度写 status；TUI 状态栏显示，ACP 静默
5. LSP 探测（惰性，不阻塞步骤 4）
     - gopls: 配置 → 本地 .aicli/lsp/bin → PATH → GOBIN/GOPATH/bin
     - 未找到 → 记 not_found + hint，Adapter 停在 L1
6. turn 开始
     - contextpack 的 knowledge provider 按当前意图选 project（不全塞）
     - 模型调用 code.find_refs → api 层按 AdapterCaps 路由 → L0/L1/L2
     - 结果带 source / completeness / confidence；大结果进 artifact
7. turn 结束
     - usageledger 写探索归因（explore_reason / knowledge_hit / confidence）
     - shadow 模式写 would_hit / would_miss（供 Phase 0/1 差异率）
```

---

## 7. 验收指标（针对本文件范围）

| 指标 | 定义 | 目标 |
|---|---|---|
| 检测准确率 | 标注集上 project kind 正确率 | ≥ 95%（Tier B 覆盖范围内） |
| 多项目召回 | monorepo 中应识别的 project 全部识别 | 100%（Phase 1 门槛） |
| 检测延迟 | workspace 首次检测（不含索引） | P95 ≤ 200ms |
| LSP 发现率 | 已安装 server 被正确发现 | 100% |
| **LSP 不阻塞** | turn 因 LSP 超时被拖慢 | **0（硬门槛）** |
| 无 LSP 可用性 | 卸载 gopls 后 `code.*` 仍可用 | 100%（降级到 L1） |
| 无 LSP 精度损失 | refs 召回（L1 vs L0） | 记录基线，不设死阈值（`04` §7.6） |
| owner 冲突 | 双进程写冲突导致失败 | 0（reader 降级） |
| `mode=off` 零影响 | 现有测试全绿、无新目录创建 | 100% |

> 阈值均为初始建议值，必须用 Phase 0 基线校准（`04` §7.6）。

---

## 8. 落地清单（文件级）

**新增**

- `backend/internal/knowledge/`（`doc.go`、`runtime.go`、`options.go`、`mode.go`）
- `backend/internal/knowledge/detect/`（`detector.go`、`signals.go`、`evidence.go`、`confidence.go`）
- `backend/internal/knowledge/store/`（`store.go`、`owner.go`、`migrate.go`；DDL 引用 `02`/`03`，不复制）
- `backend/internal/knowledge/index/`（`indexer.go`、`walk.go`、`ignore.go`）
- `backend/internal/knowledge/adapter/`（`adapter.go`、`caps.go`、`go_parser.go`、`ts_parser.go`、`fallback.go`）
- `backend/internal/knowledge/lsp/`（`manager.go`、`client.go`、`jsonrpc.go`、`framing.go`、`discover.go`、`position.go`、`process_windows.go`、`process_unix.go`）
- `backend/internal/knowledge/api/`（`codeapi.go`、`find_symbol.go`、`find_refs.go`、`callers.go`）
- `backend/internal/knowledge/tool/`（`code_find_symbol.go`、`code_find_refs.go`、`code_search.go`）
- `backend/internal/contextpack/knowledge_provider.go`
- `backend/cmd/aicli/commands/chat_knowledge_command.go`
- `backend/cmd/runtime-server/knowledge_handlers.go`

**修改**

- `backend/internal/workspace/scanner.go`（`*.test.*` → `is_test` 标记；ignore 集对齐 `fsscope`；Windows 大小写；`is_generated`）
- `backend/internal/contextmgr/manager.go`（`LayerPlan` 纳入 knowledge；Budget 计算）
- `backend/internal/toolkit/`（注册 `code.*`）
- `backend/internal/usageledger/`（探索归因字段）
- `backend/internal/acp/`（`session/new` 的 root 传给 `knowledge.Open`）
- `configs/runtime.yaml`、`configs/config.yaml`（新增 `knowledge:` 段）
- `backend/cmd/runtime-server/main.go`（bootstrap 阶段 `knowledge.Open`）

**明确不新增**

- 不新增独立 knowledge 服务 / 端口
- 不新增第 5 份并列设计文档（本内容即 `supplement/`）
- 不复制 DDL（core schema 单一事实源在 `02`）
- 不静默安装 LSP
- 不在 `knowledge.mode=off` 时改变任何现有行为

---

## 9. 已裁决问题（ADR 索引）

> 本节原为"待裁决问题（需 ADR）"。**下列 5 项均已裁决**，决策唯一事实源在 [`adr/`](../adr/README.md)。
> 本节**不再新增散文条目**：新问题先写 ADR，再回填到下表（见 [`adr/README.md`](../adr/README.md) §1 的反模式清单）。

| # | 原提问摘要 | 裁决 | Status | Gate |
|---|---|---|---|---|
| 1 | `03` 的 `projects/modules` 与 `02` 的 `language_projects` 如何收敛为本文的 Project/Module 层 | [ADR-0001](../adr/0001-project-module-language-schema.md) | Proposed | `Phase1-start` |
| 2 | ACP 场景 LSP 归属：`external_preferred` 具体探测什么信号 | [ADR-0002](../adr/0002-acp-lsp-ownership.md) | Proposed | `Phase4-start` |
| 3 | `shadow` 模式的差异率分母定义：按 turn / 按 `grep/view` 调用次数 / 按 token | [ADR-0003](../adr/0003-exploration-attribution-metrics.md) | Proposed | `Phase0-baseline`（阈值）/ `Phase1-start`（口径） |
| 4 | reader 模式下 `code.*` 是否仍注册给模型 | [ADR-0004](../adr/0004-stale-index-tool-surface.md) | Proposed | `Phase2-start` |
| 5 | Windows 进程树清理用 Job Object 还是 `taskkill /T` | [ADR-0005](../adr/0005-windows-child-process-lifecycle.md) | Proposed | `Phase4-start` |

**不在本节 5 项内、但同样源于本文的新增 ADR**

| ADR | 主题 | 它澄清 / 取代的对象 |
|---|---|---|
| [ADR-0006](../adr/0006-lsp-position-encoding-boundary.md) | LSP 位置编码转换边界与缓存键 | 补齐 `03` §5.3 的三条规则；给 `03.lsp_servers` 补 `position_encoding` 列 |
| [ADR-0007](../adr/0007-phantom-tables-and-doc-invariants.md) | 幽灵表清理与文档不变量 | `02` §8 总览块中 6 个无 DDL 的表名；`04` L546 的 `index_jobs` DDL 越位 |

> **注意**：上表 5 项中第 5 项的**答案已存在于仓库代码**——`internal/executor/process_guard_windows.go`
> 已用 Job Object（`KILL_ON_JOB_CLOSE`）为主、`taskkill /T /F` 为降级。
> ADR-0005 的实质是"复用既有守卫"，而不是"二选一"。

### 9.1 原始提问（保留备查）

1. `03` 的 `projects/modules` 与 `02` 的 `language_projects` 如何收敛为本文的 Project/Module 层？（影响 schema，必须先 ADR）
2. ACP 场景 LSP 归属：`external_preferred` 具体探测什么信号？（v1 可能无信号可用，需明确默认行为）
3. `shadow` 模式的差异率分母定义：按 turn、按 `grep/view` 调用次数、还是按 token？（决定 Phase 1 门槛可复算性）
4. reader 模式下 `code.*` 是否仍注册给模型？（本文建议"注册但带 staleness"，需与 `04` §4.6 工具面收敛共同裁决）
5. Windows 进程树清理用 Job Object 还是 `taskkill /T`？（影响 `internal/winconsole` 是否被复用）

---

## 附录 F：本文自我限制

- 本文所有 LSP 行为均**未在本仓库验证**——`internal/lsp` 尚不存在，`gopls` 实际握手细节需 Phase 4 用真实 server 打通后回填。
- Windows 进程树、UTF-16 position 映射、`.cmd` shim 三项是**已知高风险点**，但未做原型验证。
- 项目检测的 Tier 权重与 confidence 公式为**初始建议值**，需用真实 monorepo 样本校准。
- 未覆盖：Git submodule、符号链接目录、网络驱动器、WSL 挂载路径下的 workspace。
