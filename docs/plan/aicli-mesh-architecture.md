# aicli 节点网格（Node Mesh）架构方案

> 文档状态：设计（v1 深化稿）
> 定位：把「每进程一个 loopback 控制面」升级为「**本机多进程实时管理协作网格**」。
> 取代：`aicli-micro-web-client-session-window-plan.md` 的架构章节（§5 数据模型 / §6 生命周期 /
> §7 接口 / §9 spawn / §11 路线图）。该文档**已按本方案重写为 v2**（Web 客户端子方案）：
> 只保留问题定义、需求、前端交互与 Web 侧契约，架构内容全部归口本文。
> 关联：[docs/e2e/mesh-e2e.md](../e2e/mesh-e2e.md)（多进程 E2E 场景，原 debug-guide §8）、
> [docs/aicli/web-remote-api.md](../aicli/web-remote-api.md)（单进程远程 API 契约）。
> 命名与目录**不向后兼容**：`~/.aicli/web-ports/`、`~/.aicli/session-endpoints/` 全部作废。

## 目录

- [0. TL;DR（一页看完）](#0-tldr一页看完)
- [1. 背景：从「单进程控制面」到「多进程网格」](#1-背景从单进程控制面到多进程网格)
- [2. 命名与目录（旧 → 新，一次说清）](#2-命名与目录旧--新一次说清)
- [3. 数据模型](#3-数据模型)
- [4. 生命周期闭环](#4-生命周期闭环)
- [5. 控制面 API（每节点）](#5-控制面-api每节点)
- [6. 实时性设计](#6-实时性设计)
- [7. cmd/ 工具：aicli-mesh](#7-cmd-工具aicli-mesh)
- [8. 与 Web 客户端 / 会话切换的衔接](#8-与-web-客户端--会话切换的衔接)
- [9. 安全与隐私](#9-安全与隐私)
- [10. 风险与对策](#10-风险与对策)
- [11. 实施路线图](#11-实施路线图)
- [12. 测试与验收](#12-测试与验收)
- [13. 开放问题](#13-开放问题)
- [附录 A：术语对照（旧 → 新）](#附录-a术语对照旧--新)
- [附录 B：与旧方案章节对照](#附录-b与旧方案章节对照)
- [附录 C：目录与文件权限矩阵](#附录-c目录与文件权限矩阵)

---

## 0. TL;DR（一页看完）

**一句话**：每个 `aicli` 进程把自己的「身份 + 地址 + 令牌 + 当前会话 + 工作区」写成一份**节点档案**，
进程之间通过**文件注册表（发现）+ loopback HTTP 控制面（调用）+ SSE 扇入（实时）**组成一张
**本机网格**；`aicli-mesh` 工具与 Web 客户端都是这张网格的消费者。

**四件交付物**

| # | 交付物 | 说明 |
|---|--------|------|
| 1 | 目录与命名 | `~/.aicli/mesh/{nodes,bindings,leases,journal}/`，替换 `web-ports/` 与计划中的 `session-endpoints/` |
| 2 | 节点档案 + 会话绑定 + 租约 | 每进程只写自己一份档案（无跨进程锁）；会话↔地址绑定持久；租约带 TTL 做互斥 |
| 3 | 控制面 API | 每节点 `/web/api/mesh/{self,peers,events,call,spawn,stop}` + `/web/api/health` |
| 4 | `cmd/` 工具 | `backend/cmd/aicli-mesh`：`ls/show/call/send/screen/watch/open/stop/url/gc/doctor` |

**设计红线（沿用既有原则并强化）**

1. **无守护进程**：不需要中心协调者；第一个启动的进程不会自动变成「主节点」。
2. **写者唯一**：每个进程只写自己的档案与日志；读者扫描 + 聚合，永不改写别人的文件。
3. **best-effort**：网格任何一环失败（写盘、探活、订阅）都**不得**影响 chat 主流程。
4. **无新依赖**：只用标准库 + 现有 loopback HTTP / SSE 基础设施。
5. **令牌最小暴露**：令牌只存在于活动节点档案（0600）与内存；不落绑定、不落日志、不进 journal。
6. **工作区只是筛选维度**：可见性与可操作性**不以工作区为边界**——默认 `scope=all`（§5.4），
   跨工作区可显示、可调用（§9.3）；工作区只用于过滤与分组。
7. **纯增量、可一键回退**：网格整体可关（`--mesh=false`，§9.7）；关掉后只失去发现与实时，
   chat 回到今天的行为，不存在「关掉网格就不能 chat」的状态。

**最小可用切片（P0，不含拉进程）**：`internal/mesh` 包（registry + binding + lease）→
进程启动/会话切换时写档案与绑定 → `GET /web/api/mesh/self|peers` + `/web/api/health` →
`aicli-mesh ls / show / url / gc`。这一步就能回答「本机现在有哪些 aicli 在跑（**不分工作区，默认全量**）、
分别服务哪个会话、地址是什么」。

---

## 1. 背景：从「单进程控制面」到「多进程网格」

### 1.1 现状（已验证事实）

单进程控制面已经很完整：

| 能力 | 现状 | 证据 |
|------|------|------|
| 监听时机 | loopback HTTP 服务器在 `rootCmd.PersistentPreRunE` 阶段拉起，**早于 TUI** | `backend/cmd/aicli/main.go`（`resolveLoopbackServerAddr` / `stickyLoopbackServerAddr`） |
| 自描述清单 | `GET /debug/endpoints` 返回 `listen_mode` / `web_base_url` / `write_auth_header` + `endpoints[]`（method/path/enabled/url/note） | `backend/cmd/aicli/commands/chat_debug_endpoints.go` |
| 远程控制 | `/web/api/{screen,status,statusbar,runtime,events,turn,token,sessions/*,invoke,input,config/*,mcps/*,skills,analysis,cache}` | 同上 `webDebugEndpoints` 清单 |
| 实时事件 | `GET /web/api/events` SSE（keepalive 15s / heartbeat 30s / 会话切换 2s 重订阅） | `backend/cmd/aicli/commands/web_handlers.go` |
| 幂等调用 | `POST /web/api/invoke` 用 `client_request_id` 幂等回放，单飞锁 409 busy，可 SSE 化 | 同上 + `docs/e2e/debug-guide.md` §3 |
| 粘性端口 | 会话激活时把端口写入 `~/.aicli/web-ports/<session-id>.json`，`resume` 复用 | `chat_web_port_store.go`、`pprof_port_reuse.go` |

### 1.2 结构性缺口

| # | 缺口 | 具体表现 |
|---|------|----------|
| G1 | **发现** | 没有「本机有哪些 aicli 在跑」的权威视图。外部工具只能靠人给端口；端口一随机就失联 |
| G2 | **归属** | 同一个会话可以被两个进程同时打开（`aicli resume` + Web 端 resume），无仲裁、无提示 |
| G3 | **调用** | A 进程无法调用 B 进程；脚本要自己读端口、自己拼 URL、自己处理鉴权 |
| G4 | **实时** | 跨进程状态变化只能轮询或人工观察；没有「B 忙起来了 / B 退出了」的实时信号 |
| G5 | **生命周期** | 崩溃残留无对账：`web-ports/` 记录与进程存活无关，端口被占用也照写 |
| G6 | **命名** | `web-ports` 是历史命名：它实际表达的是「会话 ↔ 访问地址的偏好」，不是「端口」 |

### 1.3 目标能力

| 类型 | 编号 | 能力 |
|------|------|------|
| 功能 | MF1 | 任意进程可枚举本机全部节点：pid、地址、令牌可用性、当前会话、工作区、忙碌状态 |
| 功能 | MF2 | 任意进程可**调用**其它进程：读屏幕/状态/turn、注入 prompt、取消、恢复会话 |
| 功能 | MF3 | 会话归属互斥：同一会话最多被一个活节点占用；冲突可见、可显式接管 |
| 功能 | MF4 | 跨进程实时事件：节点加入/退出/状态变化在订阅方秒级可见 |
| 功能 | MF5 | 独立 CLI（`aicli-mesh`）覆盖上述全部能力，且在**没有进程存活时**也能读历史/清理 |
| 功能 | MF6 | 新窗口打开：复用已有节点或按需拉起新节点，并给出可访问 URL（含令牌策略） |
| 非功能 | MN1 | 网格故障不影响 chat：写盘失败/探活失败/订阅失败一律降级 |
| 非功能 | MN2 | 无守护进程、无中心协调者、无第三方依赖 |
| 非功能 | MN3 | 令牌与档案权限最小化；默认输出脱敏 |
| 非功能 | MN4 | Windows / POSIX 行为一致；Windows 无 POSIX 权限位时明确记录限制 |
| 非功能 | MN5 | 单进程路径零回归：E2E-DEBUG-01 / 02 断言全绿是硬门禁 |

### 1.4 非目标（明确不做）

| 不做 | 原因 | 将来若要做 |
|------|------|-----------|
| 跨机 / 跨用户网格 | 网格是**本机单用户**设施，安全模型 = 回环 + 同用户文件权限；`AICLI_MESH_DIR` 指到共享目录属于误用（R13） | 需要独立设计（mTLS / 服务发现 / 多租户），不在本方案范围 |
| 中心调度 / 守护进程 | 红线 MN2：没有「主节点」，第一个进程不会升级为协调者 | — |
| 会话数据同步 / 迁移 | 网格只管理「进程与会话的可见性、可调用性与互斥」，不搬运会话历史（`sessions/` 存储不动） | — |
| 隐式杀进程 | 网格不做任何隐式杀戮；`stop` 默认关且必须显式调用（§5.7 / §9.2） | — |
| 身份与权限体系 | 不做角色、不做 ACL、不做 per-workspace 权限（工作区只是筛选维度，§9.3） | — |
| 取代既有 loopback 控制面 | `/web/api/*` 既有端点语义不变；网格是新增一层发现与扇入 | — |
| 兼容旧目录 / 旧环境变量 | 明确不兼容：不双读、不迁移（§2.4） | — |

---

## 2. 命名与目录（旧 → 新，一次说清）

### 2.1 术语表

| 术语 | 英文 / 标识 | 定义 |
|------|-------------|------|
| **节点** | node | 一个 `aicli` 进程实例。可能带 loopback 端点（`--pprof`/`--web-port`），也可能是纯 TUI 进程 |
| **节点档案** | node record | 节点自述的**活体**档案：身份、端点、令牌、当前会话、工作区、心跳。写者唯一，读者聚合 |
| **会话绑定** | session binding | **持久**的「会话 ↔ 访问地址」偏好（粘性端口的新名字 + 新语义），不含任何秘密 |
| **租约** | lease | 带 TTL 的互斥占用：`session-<sid>`（会话所有权）、`spawn-<sid>`（拉起单飞） |
| **网格日志** | mesh journal | 每写者一份的追加事件日志（NDJSON）：审计 + 崩溃对账 + 离线可读 |
| **网格控制面** | mesh control plane | 每节点上的 `/web/api/mesh/*` 一族端点 |
| **网格工具** | aicli-mesh | 独立 CLI，网格的第一消费者与运维入口 |
| **工作区** | workspace | 进程/会话所属的项目目录（`workspace.path`）。**只是筛选与分组维度**，不是可见性边界，也不是权限边界（§9.3） |

### 2.2 目录布局

```text
~/.aicli/
|-- sessions/                     # 会话历史（既有，不动）
|-- chat-logs/                    # 聊天日志（既有，不动）
|-- logs/                         # 全局日志（既有，不动）
|-- workspace_directories.yaml    # 工作区注册表（既有，网格只读 join）
`-- mesh/                         # ← 新增：网格根目录（替换 web-ports/）
    |-- nodes/                    # 活体节点档案：每进程一份，写者唯一（0600）
    |   `-- node-8124-20260924T073012Z.json
    |-- bindings/                 # 会话 ↔ 地址绑定：持久偏好（0644，无秘密）
    |   `-- session_20260924072950_ltYRU9tG.json
    |-- leases/                   # 租约：会话所有权 + spawn 单飞（0600，TTL）
    |   |-- session-session_20260924072950_ltYRU9tG.lock
    |   `-- spawn-session_20260924072950_ltYRU9tG.lock
    `-- journal/                  # 网格事件日志：每写者一份 NDJSON（0600）
        `-- node-8124-20260924T073012Z.ndjson
```

**为什么是 `mesh/`**：这套机制的本质是「同机多个进程互相发现、互相调用、互相观测」，
既不是单一注册表（registry 只是一层），也不是中心化的 hub（没有中心进程）。
`mesh` 同时表达了「点对点」「可自愈」「无中心」三个设计事实。

### 2.3 命名规则

| 对象 | 规则 | 示例 | 理由 |
|------|------|------|------|
| 节点 ID | `node-<pid>-<yyyyMMddTHHmmssZ>`，冲突时追加 `-<rand4>` | `node-8124-20260924T073012Z` | 可读、可排序、可肉眼判断 pid 与启动时刻；UTC 避免时区歧义 |
| 节点档案文件 | `<node_id>.json` | `node-8124-20260924T073012Z.json` | 一进程一文件，避免跨进程写同一文件 |
| 会话绑定文件 | `<sanitized-session-id>.json` | `session_20260924072950_ltYRU9tG.json` | 与会话 ID 一一对应，便于人工对照 |
| 租约文件 | `<purpose>-<sanitized-key>.lock` | `session-session_2026....lock` | purpose 前缀让 `ls leases/` 一眼分清所有权与单飞 |
| 日志文件 | `<node_id>.ndjson` | `node-8124-20260924T073012Z.ndjson` | 每写者一份，追加无锁 |
| 文件名安全 | 仅保留 `[A-Za-z0-9._-]`，其余替换为 `_`；拒绝 `.`/`..` | 沿用 `sanitizeChatWebPortSessionID` 同款规则 | 防路径穿越 |
| 目录覆盖环境变量 | `AICLI_MESH_DIR` | `AICLI_MESH_DIR=$env:TEMP\mesh-test` | 测试与多环境隔离 |
| home 根 | `AICLI_HOME`（既有约定） | `AICLI_HOME=D:\aicli-home` | 与 `workspaceregistry` / foldertrust / plugins 保持一致 |

**路径解析优先级**（`internal/mesh` 内统一实现，任何入口不得各写一套）：

1. `AICLI_MESH_DIR`（显式覆盖，测试用）
2. `AICLI_HOME` + `/mesh`（既有 home 根约定，参考 `workspaceregistry.DefaultStorePath`）
3. `os.UserHomeDir()` + `/.aicli/mesh`
4. 无 home：**fail-closed**——只读视图返回空、写入静默跳过（绝不写 CWD 相对目录）

> 注意一个既有不一致：`aiclipaths.defaultAICLIDir` 只看 `os.UserHomeDir()`，而
> `workspaceregistry` 额外尊重 `AICLI_HOME`。网格按上面第 2 条对齐 `AICLI_HOME`，
> 并把这条差异写进 `internal/mesh` 的包注释，避免后来者踩坑。

### 2.4 旧目录处置（明确不兼容）

| 旧 | 状态 | 处置 |
|----|------|------|
| `~/.aicli/web-ports/` | **作废** | 不再读写；`aicli-mesh gc --purge-legacy` 一次性删除（默认 dry-run，需 `--apply`） |
| `~/.aicli/session-endpoints/` | **作废**（只在旧计划里出现，从未落地） | 不实现 |
| `AICLI_WEB_PORTS_DIR` | **作废** | 换成 `AICLI_MESH_DIR`；旧变量不再被读取 |
| `ChatWebPortRecord` / `chat_web_port_store.go` | **删除** | 语义迁移到「会话绑定」（`bindings/`）+「节点档案」（`nodes/`） |

**不做迁移、不做双读**：迁移期双读会让「谁是真源」变得不可判定；这里选择一次性切换，
代价只是「升级后第一次 resume 可能拿到随机端口」，可接受。
`gc --purge-legacy` 的存在是为了让老用户的 `~/.aicli` 保持干净，而不是为了兼容。

**升级过渡期（新旧版本并存）**：升级后旧版本进程仍在运行时，它不写 `mesh/` 档案，因此**不会出现在
`ls`/`peers` 里**——这是可见性的暂时缺口，且是显式的（网格不做版本探测、不做兼容读取）。
`doctor` 会检查 `web-ports/` 是否存在且文件 mtime 在心跳 TTL 内，若是则提示「可能有旧版本进程在运行
（未被网格收录）」并给出建议（升级或重启该进程）；旧进程退出后由 `gc --purge-legacy` 收尾。

落地时需同步更新两处「现状文档」：`docs/user-guide/aicli.md`（`AICLI_WEB_PORTS_DIR` 行）与
`docs/aicli/debug-chat-status.md`（端口档案路径），完整清单见 §11.6。

### 2.5 关键设计：绑定与档案分离

旧方案把「粘性端口」与「端点档案」混在一起，导致一个文件同时承担**持久偏好**与**活体状态**两种语义。
新架构显式拆开：

| 维度 | 会话绑定（bindings/） | 节点档案（nodes/） |
|------|----------------------|-------------------|
| 生命周期 | 持久（会话还在就一直有） | 活体（进程退出即删除） |
| 键 | 会话 ID | 节点 ID |
| 内容 | 地址偏好 + 上次服务者 | 身份 + 端点 + 令牌 + 当前会话 + 心跳 |
| 秘密 | **无** | **有令牌**（0600） |
| 写入时机 | 会话激活 / 端口确定 | 监听成功 / 会话切换 / 忙碌翻转 / 心跳 |
| 读者 | `resume` 时选端口；`ls` 时展示「上次地址」 | `ls`/`peers`/`call` 的全部实时信息 |

这条拆分直接消灭了 G5（残留记录与存活脱节）：**偏好是偏好，事实是事实**。

### 2.6 接入面：哪些进程写档案

| 进程形态 | 是否接入 | 写入内容 | 说明 |
|----------|----------|----------|------|
| `aicli chat` / `aicli resume`（含 `--pprof` / `--web-port`） | **是** | 档案 + 绑定 + 租约 + journal | 主目标：会话进程 |
| 无 loopback 端点的 chat 进程（纯 TUI） | **是** | 档案（`endpoint` 缺省） | 仍可被发现与展示；不可 `call`（§4.2 规则 2） |
| `mesh/spawn` 拉起的进程 | **是** | 同上，另带 `process.origin=mesh` / `spawned_by` | 用于回答「窗口是谁开的」（§5.7） |
| 其它长驻命令（如 `runtime-server`） | 预留 | — | 需要时按同一 `NodeRecord` 契约接入（`kind` 字段） |
| 一次性命令（`version` / `gc` / …）与 `aicli-mesh` 自身 | 否 | — | 没有会话，不写档案 |
| 旧版本进程（过渡期） | 否 | — | 见 §2.4 |

---

## 3. 数据模型

### 3.1 节点档案 `mesh/nodes/<node_id>.json`（schema_version = 2）

```json
{
  "schema_version": 2,
  "node_id": "node-8124-20260924T073012Z",
  "pid": 8124,
  "kind": "chat",
  "process": {
    "started_at": "2026-09-24T07:30:12Z",
    "exe": "E:\\projects\\ai\\ai-agent-runtime\\backend\\.tmp\\aicli.exe",
    "version": "1.4.2",
    "build_time": "2026-09-20T11:02:33Z",
    "parent_pid": 4200,
    "origin": "cli",
    "spawned_by": ""
  },
  "endpoint": {
    "scheme": "http",
    "host": "127.0.0.1",
    "port": 55124,
    "loopback": true,
    "base_url": "http://127.0.0.1:55124",
    "web_base_url": "http://127.0.0.1:55124/web",
    "manifest_url": "http://127.0.0.1:55124/debug/endpoints"
  },
  "auth": {
    "mode": "loopback-dev",
    "required": false,
    "token": "0f3a...",
    "token_source": "random"
  },
  "session": {
    "id": "session_20260924072950_ltYRU9tG",
    "title": "优化会话切换",
    "busy": false,
    "turn_id": "",
    "activated_at": "2026-09-24T07:30:14Z"
  },
  "workspace": {
    "path": "E:\\projects\\ai\\ai-agent-runtime",
    "name": "ai-agent-runtime"
  },
  "capabilities": ["manifest", "screen", "status", "events", "invoke", "input", "sessions", "mesh"],
  "liveness": {
    "started_at": "2026-09-24T07:30:12Z",
    "updated_at": "2026-09-24T07:31:42Z",
    "heartbeat_at": "2026-09-24T07:31:42Z",
    "heartbeat_ttl_sec": 90,
    "state": "live"
  }
}
```

| 字段 | 语义 | 备注 |
|------|------|------|
| `schema_version` | 结构版本 | 读者遇到未知大版本 → 视为 `unknown`，只展示不解析 |
| `node_id` / `pid` | 身份 | `pid` 用于存活判定；`node_id` 用于文件名与审计 |
| `kind` | 节点类型 | 预留 `chat` / `runtime-server` / 其它 |
| `process.origin` | 来源 | `cli` / `web` / `mesh` / `resume`，用于回答「这个进程是谁拉起来的」 |
| `process.spawned_by` | 拉起者 | 父节点 ID；用户手工启动为空 |
| `endpoint.*` | 访问地址 | 未启动 loopback 服务器时整个 `endpoint` 缺省（节点仍可被发现） |
| `auth.mode` | 鉴权模式 | `loopback-dev`（回环 + 开发模式，写操作免令牌）/ `loopback-strict` / `lan` |
| `auth.required` | 是否强制令牌 | 读者据此决定调用时是否附 `X-AICLI-Token` |
| `auth.token` | 写令牌原文 | **秘密**；档案 0600；默认输出脱敏；退出时随档案一起删除 |
| `session.busy` / `turn_id` | 当前 turn 状态 | 忙碌翻转时重写档案（低频，不抖动） |
| `workspace.*` | 工作区（**筛选维度**） | 供视图过滤/分组 + join `workspace_directories.yaml`；可为 `null`（无工作区进程 → 分组键 `(无工作区)`）；**不参与可见性与权限判定** |
| `capabilities[]` | 能力声明 | 便宜的能力探测（是否需要 pprof / web / mesh 端点） |
| `liveness.*` | 存活 | `heartbeat_at` 是 GC 判据；`state` 由写者自述，读者可覆盖为 `stale` |

**写者约束**：档案由本进程以「写临时文件 + 原子 rename」方式整体重写，绝不增量修改；
其它进程**永不**写这份文件（包括 GC —— GC 只删除，不改写）。

### 3.2 会话绑定 `mesh/bindings/<session_id>.json`

```json
{
  "schema_version": 2,
  "session_id": "session_20260924072950_ltYRU9tG",
  "preferred": { "host": "127.0.0.1", "port": 55124 },
  "last_node_id": "node-8124-20260924T073012Z",
  "last_workspace_path": "E:\\projects\\ai\\ai-agent-runtime",
  "updated_at": "2026-09-24T07:31:42Z"
}
```

- 作用一：`resume` 时优先复用 `preferred.port`（沿用既有粘性端口行为，仅换存储与命名）。
- 作用二：`aicli-mesh ls` / Web 侧栏展示「上次服务的地址」，即使当前没有节点在跑。
- **不含令牌**。任何需要令牌的消费方必须从活动节点档案读取。

### 3.3 租约 `mesh/leases/<purpose>-<key>.lock`

```json
{
  "schema_version": 2,
  "purpose": "session",
  "key": "session_20260924072950_ltYRU9tG",
  "owner_node_id": "node-8124-20260924T073012Z",
  "owner_pid": 8124,
  "acquired_at": "2026-09-24T07:30:14Z",
  "renewed_at": "2026-09-24T07:31:42Z",
  "ttl_sec": 90
}
```

| 规则 | 说明 |
|------|------|
| 获取 | `O_CREATE|O_EXCL` 创建；失败则读现有租约判断是否可回收 |
| 续约 | 随心跳刷新 `renewed_at`（写临时文件 + rename，同档案策略） |
| 释放 | 进程正常退出时删除；异常退出靠 TTL + pid 判定回收 |
| 回收 | 条件：`owner_pid` 不存在 **或** `now - renewed_at > ttl_sec`。回收用「先 rename 成 `.stale-<ts>` 再删除」的原子动作，保证多进程竞争下只有一个赢家 |
| 过期兜底 | 租约不可用时（无 home / 权限不足）**降级为「无互斥」**并记一条 `lease.degraded` 日志，绝不阻塞会话 |

### 3.4 网格日志 `mesh/journal/<node_id>.ndjson`

每行一个 JSON 对象，追加写（`O_APPEND`），只有写者自己的文件：

```json
{"ts":"2026-09-24T07:31:42Z","node_id":"node-8124-...","seq":42,"kind":"session.activated","session_id":"session_...","detail":{"port":55124}}
```

| `kind` | 触发 | 用途 |
|--------|------|------|
| `node.started` / `node.stopped` | 进程起停 | 审计、崩溃对账 |
| `session.activated` | 会话切换/新建 | 会话↔节点历史 |
| `busy.changed` | 忙碌翻转 | 事后分析「谁在什么时候忙」 |
| `lease.acquired` / `lease.released` / `lease.reclaimed` | 租约动作 | 冲突复盘 |
| `mesh.call.received` / `mesh.call.completed` | 跨进程调用 | **审计**（谁调用了谁、op、耗时、结果码） |
| `mesh.spawn.requested` / `mesh.spawn.completed` | 拉起新节点 | 排查「窗口是谁开的」 |
| `mesh.peer.observed` | 节点加入/退出 | 实时视图的离线回放 |

- **心跳不进 journal**（噪音太大）；心跳只写档案。
- 保留策略：默认保留 7 天；`gc` 删除死节点超过 `--keep-days` 的日志；单文件超过 8 MiB 轮转为 `.1`。
- 日志**永不写入令牌**（有专门的脱敏断言，见 §12）。

### 3.5 状态词汇（统一定义，避免歧义）

| 维度 | 取值 | 判定 |
|------|------|------|
| 节点 `liveness.state` | `live` | pid 存活 且 心跳在 TTL 内 |
| | `stale` | pid 存活 但心跳超 TTL（可能卡死）**或** pid 不存在但档案未清理 |
| | `stopped` | 档案自述 `stopped`（正常退出路径） |
| | `unknown` | 档案不可解析 / schema 未知 / 无 pid 信息 |
| 会话 `session.state` | `running` | 有活节点占用且不忙 |
| | `busy` | 有活节点占用且 `busy=true` |
| | `idle` | 无节点占用，但有历史（可在新窗口打开） |
| | `unknown` | 会话信息缺失（例如只有绑定、没有档案） |
| 归属 `ownership` | `owner` | 该会话由**本进程**服务 |
| | `peer` | 由另一个活节点服务（含节点 ID） |
| | `conflict` | 多个活节点声称服务同一会话（异常，需仲裁） |
| | `none` | 无节点服务 |
| 探活 `reachability` | `ok` / `unreachable` / `skipped` | `GET /web/api/health` 1s 超时；`skipped` = 未开启探活 |

### 3.6 schema 演进与字段规则

| 规则 | 内容 |
|------|------|
| 版本字段 | 档案 / 绑定 / 租约都带 `schema_version`；当前为 `2` |
| 何时 bump | 删除字段、改变字段语义、改变必填性 → 必须 bump；**只增可选字段不 bump** |
| 读者降级 | 未知**大版本** → 按 `unknown` 处理：只展示可读部分、不参与归属判定、不可 `call`（§3.1） |
| 写者约束 | 写者只写自己版本的结构；读者**永不写盘**，因此没有「读旧文件顺手升级」的路径（§4.2 规则 4） |
| 兼容窗口 | 不承诺跨大版本双向兼容；升级/回退都按「同一版本二进制」讨论 |
| 时间格式 | 时间戳一律 UTC RFC3339（`...Z`）；TTL 判定优先用单调时钟差值（R4） |
| 未知字段 | 读者忽略未知字段；未知 `kind` / `capabilities` 只影响能力探测，不影响展示 |

---

## 4. 生命周期闭环

### 4.1 写入（Write）

| 时机 | 动作 | 失败后果 |
|------|------|----------|
| 进程启动（无论是否 loopback） | 写节点档案（`kind` / `pid` / `workspace`，`endpoint` 待监听成功后补），`journal: node.started` | 记一条 warning；网格不可见，chat 不受影响 |
| loopback 监听成功 | 更新档案 `endpoint` + token | 记一条 warning；该节点不可被 `call`，其余可见性不受影响 |
| 会话激活（新建 / resume / `/resume`） | 更新档案 `session` 段；写会话绑定；获取 `session-<sid>` 租约 | 租约失败 → 降级无互斥 + `lease.degraded` |
| 忙碌翻转 | 更新档案 `session.busy` / `turn_id` | 静默 |
| 心跳（默认 30s） | 更新 `liveness.heartbeat_at`；续约租约 | 连续 3 次失败 → 停止心跳并记 warning |
| 会话失活（回到无会话） | 清空档案 `session` 段；释放租约 | 静默 |
| 正常退出 | 档案置 `liveness.state=stopped` → 删除档案；释放全部租约；`journal: node.stopped` | 残留由 GC 兜底 |
| 崩溃 | 无写入 | pid + 心跳判 `stale`；GC 清理 |

> 写入一律「临时文件 + rename」，且**只写自己的文件**。这是整套设计里唯一需要遵守的并发纪律，
> 它把跨进程同步问题降级为「文件系统原子性」问题。

### 4.2 读取（Read）

```text
扫描 mesh/nodes/*.json
   |-- 解析失败 / schema 未知        -> state=unknown（保留展示，不参与归属判定）
   |-- pid 不存在                    -> state=stale（不删除，交给 GC）
   `-- pid 存活 -> 心跳新鲜? live : stale
   |
   +--> 左连接 bindings/<sid>.json   -> 「上次地址」（节点已死时仍可展示）
   +--> 左连接 journal（可选）        -> 最近事件（`ls --recent`）
   +--> 左连接 workspace_directories.yaml -> 工作区别名（只读，仅用于展示/分组）
   +--> 聚合归属：按 session.id 分组 -> owner / peer / conflict
   `--> 可选探活（--probe / ?probe=1）-> reachability
```

**聚合规则**

1. 同一会话被多个 `live` 节点声明 → 全部标 `conflict`，并在输出里列出所有节点（这是 G2 的可见性保证）。
2. 节点无 endpoint（未开 loopback）仍出现在列表里，`reachability=skipped`，不可被 `call`。
3. 排序：`live` 优先，其次 `stale`，最后 `unknown`；同组按 `heartbeat_at` 倒序。
4. 读取路径**绝不写盘**（包括不「顺手修复」别人的档案）。
5. **全量可见（工作区不设边界）**：扫描与归属判定永远覆盖 `mesh/nodes/` 下**全部**档案，不按工作区过滤；
   `scope` / `workspace` 只作用于**输出层**（§5.4）。跨工作区节点与同工作区节点在归属、冲突、可调用性上**完全同权**。

### 4.3 探活（Probe）

- 端点：`GET /web/api/health`，超时 1s，只读、极轻量（不碰会话、不碰渲染器）。
- 触发：`aicli-mesh ls --probe`、`GET /web/api/mesh/peers?probe=1`、`doctor`。
- 结果**不落盘**：探活是瞬时事实，落盘会引入「上次探测结果」的陈旧性。
- 默认关闭：列表默认不做网络请求，保证在几十个节点时也是毫秒级。
- **并发与预算**：探活并发上限默认 8（可配），整体预算默认 3s；超预算的节点标 `reachability=skipped`
  （不是 `unreachable`）——「列表可用」优先于「探活完整」。
- **规模假设**：本机节点数量级为个位数、设计上限 200；`ls` 目标：50 节点（不带 `--probe`）< 100ms。
  超过 200 节点时 `doctor` 告警（提示清理僵尸档案）；列表**不分页**（全量可见是产品契约，§5.4）。

### 4.4 租约与所有权（时序）

```text
进程 A（已有会话 S）                        进程 B（想 resume S）
----------------------                      ---------------------
持有 leases/session-S.lock
  owner=A pid=A renewed_at=...
                                            acquire(session-S)
                                              读锁 -> owner pid A 存活且新鲜
                                              -> 拒绝：busy（不抢）
A 心跳续约（30s）                            B 的选择：
A 正常退出 -> 删除锁                           1) 走「新窗口打开」-> 复用 A 的地址（不抢）
                                               2) 显式接管 --takeover -> 回收锁并记
                                                  journal: lease.reclaimed + 提示 A
A 崩溃 -> 锁残留
                                            B 下次 acquire：pid A 不存在 -> 原子回收
```

**接管语义**：`--takeover` 只把「所有权」交给 B；A 进程仍在运行，但它的下次心跳会发现锁已易主，
于是把自己的档案 `session.state` 标为 `orphaned` 并在 TUI/Web 顶部提示「会话已被节点 B 接管」。
**不杀进程**：网格不做隐式杀戮，杀进程永远是需要显式 `aicli-mesh stop --force` 的独立动作。

### 4.5 清理（GC）

`aicli-mesh gc`（默认 dry-run，`--apply` 才动手）：

| 对象 | 删除条件 | 保护 |
|------|----------|------|
| `nodes/*.json` | `state=stopped` **或**（pid 不存在 且 `heartbeat_at` 超过 `--stale-ttl`，默认 10min） | pid 存活**永不删** |
| `leases/*.lock` | owner pid 不存在 或 超过 TTL | 正在被活跃进程续约的不动 |
| `journal/*.ndjson` | 对应节点已死 且 超过 `--keep-days`（默认 7） | — |
| `bindings/*.json` | 仅当对应会话在会话存储中不存在（需 `--prune-bindings` 显式开启） | 默认不删：绑定是偏好，宁留不误删 |
| 旧目录 | `--purge-legacy --apply` | 只删 `web-ports/`（及从未落地的 `session-endpoints/`），不动 `mesh/` |

启动对账：进程启动时**不做**全量 GC（避免多进程同时扫描），只做两件事：
清理自己上次遗留的同 pid 档案；清理自己拥有的孤儿 spawn 锁。

### 4.6 崩溃与断电对账

| 场景 | 现象 | 恢复 |
|------|------|------|
| 进程被 `Stop-Process` / SIGKILL | 档案仍在，pid 不存在 | 读者标 `stale`；GC 删除 |
| 机器断电 | 多个档案 + 多个锁残留 | 全部 pid 不存在 → 首次 `gc --apply` 清理干净 |
| 心跳线程卡死但进程活着 | 档案 `stale`，锁 TTL 过期 | 读者标 `stale`；`--takeover` 可回收（pid 存活时 TTL 到期允许接管，需显式） |
| 磁盘满 / 权限错误 | 写失败 | 记 warning 并**关闭网格写入**（避免刷屏），chat 继续 |

### 4.7 降级矩阵（红线 MN1 的落地表）

| 故障 | 降级行为 | 用户可见影响 |
|------|----------|--------------|
| 档案写失败 | 停止后续网格写入，保留内存态 | `ls` 看不到本节点 |
| 绑定写失败 | 静默跳过 | `resume` 可能换端口 |
| 租约不可用 | 无互斥模式 + 日志 | 可能双开（退回今天的行为） |
| 探活超时 | 标 `unreachable` | 列表仍可用 |
| peer SSE 订阅失败 | 指数退避（1s→30s） | 跨进程实时性变差，本进程不受影响 |
| journal 写失败 | 关闭 journal | 失去审计/对账，不影响运行 |
| （显式配置）`--mesh=false` | 不写档案、不订阅、不注册 `mesh/*` 端点 | 本节点不出现在网格视图；其余进程照常 |

---

## 5. 控制面 API（每节点）

### 5.1 端点总表

| 方法 | 路径 | 阶段 | 说明 |
|------|------|------|------|
| GET | `/web/api/health` | P0 | 存活探针（极轻量） |
| GET | `/web/api/mesh/self` | P0 | 本节点自述（= 档案 + 派生字段） |
| GET | `/web/api/mesh/peers` | P0 | 网格聚合视图（扫描 + join + 可选探活） |
| GET | `/web/api/mesh/events` | P1 | SSE：本节点 + peer 扇入事件流 |
| POST | `/web/api/mesh/call` | P1 | 跨节点调用（白名单 op） |
| POST | `/web/api/mesh/spawn` | P1 | 拉起新节点（新窗口打开） |
| POST | `/web/api/mesh/stop` | P2 | 优雅停止目标节点（默认关闭，需显式开关） |

全部端点同时登记进 `/debug/endpoints` 清单的新分组 `mesh`，让既有「只认清单」的脚本自动发现
（`chat_debug_endpoints.go` 增加 `meshDebugEndpoints` 列表 + `Scheme: "mesh"`）。
清单覆盖门禁（`Test-AicliEndpointCoverage`）会强制要求：新端点要么被断言，要么带理由豁免。

### 5.2 `GET /web/api/health`

```json
{ "available": true, "node_id": "node-8124-20260924T073012Z", "pid": 8124,
  "uptime_sec": 91, "session_active": true, "busy": false, "mesh_ready": true }
```

- 不依赖会话、不依赖渲染器；无会话时同样 200（`session_active=false`）。
- 用途：网格探活、外部脚本就绪等待、`aicli-mesh doctor`。

### 5.3 `GET /web/api/mesh/self`

返回本进程的节点档案视图，额外带 `derived` 段（内存实时值，可能比档案更新）：

```json
{ "schema_version": 2, "node_id": "node-8124-...", "endpoint": {...}, "session": {...},
  "derived": { "busy": false, "pending_inputs": 0, "turn_id": "", "peer_count": 2, "lease": "owner" },
  "mesh": { "root": "C:\\Users\\me\\.aicli\\mesh", "journal": "…\\journal\\node-8124-....ndjson" } }
```

- `auth.token` **默认脱敏**（`"token": "0f3a…"` 或省略）；带 `?reveal_token=1` 且请求来自回环同源时才返回原文
  （与既有 `GET /web/api/token` 的信任模型一致）。
- `mesh.root` 让脚本不用猜目录（对齐 `/debug/endpoints` 的「自描述」传统）。

### 5.4 `GET /web/api/mesh/peers`

查询参数：`probe=1`、`scope=self|all`（**默认 `all`**）、`workspace=<abs-path>`（可重复 / 逗号分隔，默认不过滤；
`scope=self` 等价于 `workspace=<本节点工作区>`）、`state=all|live`（默认 `all`）、`redact_token=1`（默认 1）。

```json
{
  "schema_version": 2,
  "generated_at": "2026-09-24T07:31:42Z",
  "self": { "node_id": "node-8124-...", "session_id": "session_..." },
  "counts": { "live": 2, "stale": 1, "unknown": 0, "conflict": 0 },
  "filter": { "scope": "all", "workspace": null, "state": "all" },
  "nodes": [
    { "node_id": "node-8124-...", "pid": 8124, "state": "live", "reachability": "ok",
      "endpoint": { "base_url": "http://127.0.0.1:55124", "loopback": true },
      "auth": { "required": false, "mode": "loopback-dev", "token_hint": "0f3a…" },
      "session": { "id": "session_...", "title": "优化会话切换", "state": "running", "busy": false },
      "workspace": { "path": "E:\\projects\\ai\\ai-agent-runtime", "name": "ai-agent-runtime" },
      "ownership": "owner", "heartbeat_at": "2026-09-24T07:31:40Z", "age_sec": 2,
      "journal_tail": ["session.activated", "busy.changed"] }
  ],
  "workspaces": [ { "path": "E:\\projects\\ai\\ai-agent-runtime", "name": "ai-agent-runtime", "nodes": 2 } ]
}
```

- 这是 Web 侧栏与 `aicli-mesh ls` 的**同一数据源**（工具与 API 不各写一套聚合逻辑，见 §7.5）。
- **默认全量（硬契约）**：`scope=all` 是默认值，**跨工作区节点默认就在列表里**；`scope=self` / `workspace=` /
  `state=live` 只是**输出过滤**，不改变聚合与归属判定（冲突判定永远全量，避免「过滤掉一半冲突」的假象）。
- 分组：消费方按 `workspace.path` 分组渲染（`workspaces[]` 提供计数）；缺 `workspace` 的节点归入 `(无工作区)`。
- `counts` 恒为**全量口径**（未过滤），`filter` 段回显实际过滤条件，便于 UI 提示「已过滤 N 个」。

### 5.5 `GET /web/api/mesh/events`（SSE）

- 帧类型：
  - `mesh.peer.joined` / `mesh.peer.left` / `mesh.peer.updated`（节点生命周期）
  - `mesh.session.changed`（会话归属变化、冲突出现/消解）
  - `mesh.call.invoked` / `mesh.call.completed`（本节点被调用 / 调用完成）
  - `mesh.peer.event`（peer 的转发事件，默认只转发 `turn.*` 与 `session.*` 边界，避免噪音）
- 续传：`?since_seq=<n>` + `id:` 行，语义与既有 `/web/api/events` 一致。
- 订阅拓扑由本节点决定（`?peers=auto|none`，默认 `auto`）：节点自己作为 fan-in 点，浏览器只连自己的进程。

### 5.6 `POST /web/api/mesh/call`

请求：

```json
{ "target": "node-9001-20260924T073500Z",
  "op": "invoke",
  "args": { "prompt": "只回复两个字：收到", "timeout_ms": 120000 },
  "client_request_id": "mesh-42-1",
  "timeout_ms": 130000,
  "allow_write": true }
```

`op` 白名单（P1）：

| op | args | 结果 | 写操作 |
|----|------|------|--------|
| `node.info` | — | 目标 `/web/api/mesh/self` | 否 |
| `status` | — | 目标 `/web/api/status` | 否 |
| `screen` | `view`,`tail`,`format` | 目标 `/web/api/screen` | 否 |
| `turn` | `id` | 目标 `/web/api/turn` | 否 |
| `sessions.list` | — | 目标 `/web/api/sessions` | 否 |
| `invoke` | `prompt`,`wait_only`,`timeout_ms` | 目标 `/web/api/invoke` | **是** |
| `input` | `prompt` / 审批决议 | 目标 `/web/api/input` | **是** |
| `cancel` | — | 目标 interrupt 路径 | **是** |
| `sessions.resume` | `session_id` | 目标 `/web/api/sessions/resume` | **是** |

响应统一信封：

```json
{ "status": "ok", "code": "", "node_id": "node-9001-...", "op": "invoke",
  "elapsed_ms": 3211, "duplicate": false, "result": { "...": "目标端点的原始响应" } }
```

`status` 取值：`ok` / `busy`（目标忙，可重试）/ `not_found`（目标不存在或已死）/ `unreachable` /
`refused`（策略拒绝：非回环、写操作未显式允许、收敛开关下的跨工作区限制）/ `timeout` / `error`。
`code` 给出机器可判的细分原因（如 `mesh_write_not_allowed`；`mesh_cross_workspace_denied` **仅**在
`--mesh-restrict-workspace` 开启时出现，§9.3）。

实现要点：
1. 调用方从**目标节点档案**读 token（0600），以 `X-AICLI-Token` 发起，附 `X-AICLI-Mesh-Caller: <caller node_id>`。
2. 被调用方校验：调用者地址是回环、`op` 在白名单内、写操作要求 `allow_write=true`；
   跨工作区**默认放行**（仅在 `--mesh-restrict-workspace` 开启时拒绝，§9.3）。
3. `client_request_id` 透传到目标端点的幂等键，网格层不重复实现幂等。
4. 双方各写一条 journal（`mesh.call.received` / `mesh.call.completed`），**只记 op 与耗时，不记 args 正文**（args 可能含用户 prompt）。
5. **令牌轮换**：调用前从目标档案取 token；若目标返回 401（目标重启导致令牌变化）→ **重读一次档案并重试一次**，
   仍失败则 `refused`（`mesh_token_stale`），不做无限重试。

### 5.7 `POST /web/api/mesh/spawn` / `stop`（P1 / P2）

`spawn` 请求：`{ "session_id": "...", "port": 0, "detach": true, "wait_ms": 8000, "origin": "web" }`
响应：`{ "status": "reused|started|not_running|failed", "node_id": "...", "url": "http://127.0.0.1:55124/web?token=...&session=..." }`

- 状态集合固定为四个：`reused`（复用已有节点）/ `started`（新节点已就绪）/ `not_running`（拉起后超时未见节点）/
  `failed`（进程启动失败，附日志尾部）；**不返回 `starting` 中间态**——`wait_ms` 内同步等待，
  前端处理见 Web 侧方案 §7.4。

- 单飞：先抢 `spawn-<sid>` 锁；抢不到 → 直接 `reused`（读对方的档案返回 URL）。
- 锁内二次检查：防止两个请求几乎同时到达时各拉一个进程。
- 拉起的命令行固定为：`aicli resume <sid> --pprof --web-port <port> --web-host 127.0.0.1 --web-token <token>`，
  工作目录 = 会话工作区；Windows 用 `DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP`，POSIX 用 `Setsid`；
  stdout/stderr 重定向到 `~/.aicli/logs/mesh-spawn-<sid>.log`。
- `stop`（P2，默认关闭）：`{ "target": "...", "mode": "graceful|force" }`；`graceful` 走 `/web/api/input {"prompt":"/exit"}`，
  `force` 才终止进程。开启需 `--mesh-allow-stop`（进程级开关），默认拒绝。

### 5.8 鉴权矩阵

| 端点 | 回环 + 开发模式 | 回环 + 严格模式 | 非回环 |
|------|----------------|----------------|--------|
| `GET /web/api/health` | 免令牌 | 免令牌（只读探针） | 需令牌 |
| `GET /web/api/mesh/self|peers` | 免令牌（令牌默认脱敏） | 需令牌 | 需令牌 |
| `GET /web/api/mesh/events` | 免令牌 | 需令牌 | 需令牌 |
| `POST /web/api/mesh/call`（只读 op） | 免令牌 | 需令牌 | **拒绝**（默认） |
| `POST /web/api/mesh/call`（写 op） | 免令牌 + `allow_write` | 需令牌 + `allow_write` | 拒绝 |
| `POST /web/api/mesh/spawn|stop` | 需显式进程开关 | 需令牌 + 开关 | 拒绝 |

**默认拒绝跨机**：`mesh/*` 的写路径与非回环一律拒绝，除非显式 `--mesh-allow-nonloopback`
（与 `--web-host 0.0.0.0` 的既有安全叙事保持一致：非回环必须带令牌，且写操作要额外开关）。

### 5.9 通用契约（所有 mesh 端点）

| 维度 | 约定 |
|------|------|
| 错误信封 | 非 2xx 与 `call` 的 `refused` / `error` 统一为 `{status, code, message, node_id?}`，`status` 取值见 §5.6 |
| HTTP 映射 | `ok`→200；`busy`→409；`not_found`→404；`refused`→403；`unreachable` / `timeout`→504；参数错误→400；内部错误→500 |
| 请求体上限 | 默认 1 MiB（`call.args` 与 `spawn` 参数都受此限）；超限→413 |
| 超时 | 探活 1s；`call` 默认 130s（可覆盖）；`spawn` 默认 `wait_ms=8000`；SSE keepalive 沿用既有 15s |
| 幂等 | 仅 `call` 透传 `client_request_id`；`spawn` 用 `spawn-<sid>` 单飞锁达成等价语义（§5.7） |
| 限流 | `mesh/events` 每 peer 20 帧/秒（§6.4）；其余端点不做令牌桶（本机单用户，收益低） |
| 版本 | 所有 JSON 响应带 `schema_version`；未知大版本按 §3.6 降级 |
| 日志 | 端点日志不含请求体与令牌原文（§9.5） |

---

## 6. 实时性设计

### 6.1 三层实时模型

```text
第 3 层  跨进程扇入（新）     peer 节点 --SSE--> 本节点 --> 本进程的 Web 客户端 / TUI
第 2 层  节点事件流（既有）   /web/api/events（EventBus -> SSE，keepalive 15s / heartbeat 30s）
第 1 层  进程内事件总线（既有） session.LocalRuntimeHost.EventBus
```

新增的只有第 3 层：**每个节点可以订阅其它节点的 `/web/api/mesh/events`**，把 peer 事件
以 `mesh.peer.*` 帧并入自己的 SSE 流。第 1、2 层完全复用，不改语义。

### 6.2 订阅拓扑（谁连谁）

```text
     浏览器 A          浏览器 B           aicli-mesh watch
        |                 |                     |
        v                 v                     v
    节点 A ============ 节点 B             journal/ 目录（tail）
      |  \              /  |                    ^
      |   `-- SSE 扇入 -'   |                    |
      |        （回环）      |                    |
      `---- 写 journal -----'--------------------'
```

规则：

1. **浏览器只连自己的节点**（单连接、单令牌、单信任边界）；扇入由节点承担。
2. 节点之间**默认全连**（本机节点数是个位数）；节点数超过阈值（默认 16）时切换为「**优先**订阅同工作区 +
   最近活跃的 N 个」，其余靠轮询——**这是推送覆盖面的降级，不是可见性边界**：`peers` 全量视图与 `call`
   的目标解析始终跨工作区（§5.4 / §9.3），未订阅节点只是实时性降为轮询粒度。
3. `aicli-mesh watch` **不依赖任何节点存活**：直接 tail `journal/*.ndjson`，因此能看历史、能在全部进程退出后复盘。

### 6.3 事件帧格式

```text
id: 17
event: mesh.peer.updated
data: {"seq":17,"ts":"2026-09-24T07:31:42Z","source_node_id":"node-9001-...","type":"mesh.peer.updated","data":{"state":"busy","session_id":"session_...","busy":true}}
```

| 字段 | 说明 |
|------|------|
| `id` / `seq` | 单调序号（每节点独立），支持 `?since_seq=` 续传 |
| `source_node_id` | 事件来源节点（本节点事件也带，便于统一消费） |
| `type` | `mesh.peer.joined|left|updated`、`mesh.session.changed`、`mesh.call.*`、`mesh.peer.event` |
| `data` | 类型相关载荷；`mesh.peer.event` 内含原始 `turn.*` / `session.*` 帧 |

**扇入白名单**：默认只转发 `turn.*`（起止/终态）与 `session.*`（切换/归属），不转发 token 级高频事件
（例如逐字 delta 与状态栏刷新），否则 N 个 peer 会把单进程 SSE 淹没。

**seq 语义**：SSE 的 `seq` 与 journal 的 `seq` 是**同一节点内的同一个单调计数器**（每节点独立、从 1 起）；
节点重启后从 1 重新开始（`node_id` 已变，因此不存在「跨重启续传」的歧义）。`mesh.peer.event` 外层 `seq`
仍按本节点计数器推进，内层保留原始帧供追溯。

**防环**：扇入只消费 peer 的**本地事件**（`mesh.peer.*` / `mesh.session.changed` / `mesh.call.*` /
白名单内的 `turn.*`、`session.*`），**绝不二次转发 `mesh.peer.event` 本身**——A↔B 互订不会形成事件回声。

### 6.4 背压与失败降级

| 情况 | 处理 |
|------|------|
| peer SSE 断线 | 指数退避重连（1s→2s→4s…上限 30s），期间该 peer 标 `reachability=unreachable` |
| peer 事件速率过高 | 每 peer 令牌桶（默认 20 帧/秒），超限丢弃并计数（`dropped_events` 出现在 peers 视图） |
| 本节点 SSE 客户端停滞 | 复用既有死流检测（writer goroutine 写失败/超时 → 退订） |
| 事件积压 | 单客户端缓冲上限（默认 256 帧），超限发 `mesh.lagged` 帧并跳号，由消费方重新拉全量视图 |
| 浏览器客户端过多 | 单节点 SSE 客户端上限默认 32（可配）；超限拒绝新连接（429），不影响既有连接 |

**原则**：实时性永远是可降级的增强；任何 peer 故障都不得阻塞本进程的事件循环。

### 6.5 为什么不让浏览器直连多个进程

| 方案 | 问题 |
|------|------|
| 浏览器连 N 个节点 | N 份令牌进浏览器（sessionStorage）、N 条 CORS/Host 校验、N 套断线重连 |
| 节点做扇入（选定） | 令牌只在回环进程之间流动；浏览器保持单连接；跨进程策略集中在一处可审计 |

### 6.6 journal 与 SSE 的分工

| 维度 | SSE 扇入 | journal |
|------|----------|---------|
| 时效 | 亚秒级 | 秒级（tail 轮询 500ms–1s） |
| 依赖 | 双方进程都活着 | 无（只依赖文件） |
| 内容 | 实时状态 | 状态转换 + 审计（谁调用谁） |
| 用途 | UI 实时刷新 | 复盘、对账、审计、`watch` |

---

## 7. cmd/ 工具：aicli-mesh

### 7.1 定位与命名

- 位置：`backend/cmd/aicli-mesh/`（与 `aicli`、`aicli-console`、`ssh-client` 同级）。
- 命名：`aicli-mesh` —— 名词即架构名（mesh），子命令即动作，避免 `ctl`/`hub` 这类无信息量词根。
- 核心包：`backend/internal/mesh/`（registry / binding / lease / journal / client / view），
  CLI 只做参数解析与渲染（`internal/mesh/cli`），**Web 端点与 CLI 复用同一套聚合逻辑**。
- 独立可运行：**没有任何 aicli 进程存活时也能工作**（直接读文件），这是它与「HTTP 客户端」的本质区别。

### 7.2 命令清单

| 命令 | 作用 | 关键参数 | 阶段 |
|------|------|----------|------|
| `aicli-mesh ls`（别名 `ps`） | 列出本机节点：状态/地址/会话/工作区（**默认全量、跨全部工作区**） | `--json` `--probe` `--live`（只看存活） `--workspace PATH`（可重复，过滤） `--sort age\|session\|workspace` | P0 |
| `aicli-mesh show <node\|session>` | 单节点或单会话详情（含 journal 尾部、绑定、租约） | `--json` `--events N` | P0 |
| `aicli-mesh url <node\|session>` | 打印可访问 URL | `--with-token` `--path /web` | P0 |
| `aicli-mesh gc` | 清理死节点/过期租约/旧日志/旧目录 | `--apply` `--stale-ttl 10m` `--keep-days 7` `--purge-legacy` `--prune-bindings` | P0 |
| `aicli-mesh doctor` | 自检：目录、权限、陈旧节点、双占用、令牌可读性、journal 完整性 | `--json` | P0 |
| `aicli-mesh call <node\|session> <op>` | 跨进程调用（op 白名单见 §5.6） | `--arg k=v` `--prompt TEXT` `--timeout 130s` `--request-id ID` `--allow-write` `--json` | P1 |
| `aicli-mesh send <node\|session> <text>` | 语义糖：`call <t> invoke --prompt <text>` | `--wait` `--request-id ID` | P1 |
| `aicli-mesh screen <node\|session>` | 读取目标进程屏幕合成帧 | `--view tui` `--tail N` `--format json` | P1 |
| `aicli-mesh watch` | 实时事件流（journal tail + 可选 SSE） | `--json` `--since 10m` `--node ID` `--no-color` | P1 |
| `aicli-mesh open <session>` | 新窗口打开：复用节点或拉起新节点 | `--port N` `--no-wait` `--print-url` `--with-token` | P1 |
| `aicli-mesh stop <node>` | 优雅停止目标节点 | `--force` `--wait 30s` | P2 |
| `aicli-mesh version` | 版本与构建信息 | `--json` | P0 |

**目标解析**：`<node|session>` 同时接受节点 ID、节点 ID 前缀、会话 ID、会话 ID 前缀、`pid:8124`；
歧义时列出候选并要求精确指定（不做「猜一个」）。

### 7.3 输出与退出码契约

- 默认输出：人类可读表格（UTF-8，等宽对齐，无颜色时自动降级）。
- `--json`：稳定 schema（`{"schema_version":2,"nodes":[...],"counts":{...}}`），供脚本与 Agent 消费。
- 退出码：

| 码 | 含义 |
|----|------|
| 0 | 成功 |
| 1 | 用法/参数错误 |
| 2 | 目标不存在（无匹配节点/会话） |
| 3 | 目标不可达（探活失败/无端点） |
| 4 | 冲突或忙碌（会话被占用、invoke busy） |
| 5 | 调用失败（目标返回错误、超时） |
| 6 | 被策略拒绝（非回环、写操作未显式允许、收敛开关下的跨工作区限制） |

### 7.4 典型用法

```powershell
# 本机现在有哪些 aicli 在跑？（默认不探活，毫秒级）
aicli-mesh ls
aicli-mesh ls --probe --json | ConvertFrom-Json | Select-Object -ExpandProperty nodes

# 某个会话的访问地址（给人复制的 URL）
aicli-mesh url session_20260924072950_ltYRU9tG
aicli-mesh url session_20260924072950_ltYRU9tG --with-token   # 显式带令牌

# 跨进程调用：让另一个进程跑一轮 prompt，并等结果
aicli-mesh send session_20260924072950_ltYRU9tG "只回复两个字：收到" --wait --json

# 读取另一个进程的屏幕（不打扰它）
aicli-mesh screen node-9001-20260924T073500Z --tail 40

# 实时观察全部节点
aicli-mesh watch --since 5m

# 新窗口打开（复用已有节点；没有则拉起）
aicli-mesh open session_20260924072950_ltYRU9tG --print-url

# 运维
aicli-mesh doctor
aicli-mesh gc --apply --purge-legacy
```

### 7.5 实现与构建登记

| 事项 | 位置 | 动作 |
|------|------|------|
| 工具二进制 | `backend/cmd/aicli-mesh/main.go` | 新建；只做 flag 解析 + 调 `internal/mesh/cli` |
| 共享逻辑 | `backend/internal/mesh/`（registry/binding/lease/journal/client/view/cli） | 新建；`aicli` 主程序与工具同时引用 |
| 发布登记 | `scripts/build.ps1` 的 `$script:toolRegistry`（第 83–90 行的表） | 追加一行：`Name=aicli-mesh, Package=./cmd/aicli-mesh, WindowsName=aicli-mesh.exe, Win7Name=aicli-mesh-win7.exe, LdflagsKind=main-version` |
| Makefile | `Makefile`（`.PHONY` 第 1 行 + 各目标） | 追加 `aicli-mesh:` 目标；如加入默认 `build` 需在 `build:` 目标里并列 |
| 文档 | `docs/aicli/`（与 `web-remote-api.md` 同级） | 落地后新增 `mesh-cli.md`；本方案 §7 是它的草稿 |
| 可选别名 | `backend/cmd/aicli/main.go` 的 `rootCmd.AddCommand` 区 | P2：`aicli mesh <sub>` 薄封装，复用 `internal/mesh/cli`，避免两套实现 |

**先例**：`backend/cmd/session-dedupe/` 已经把「共享子包 + 独立 main」的做法走通了
（`cmd/session-dedupe/dedupe` 与运行时代码共享判定规则），网格工具沿用同一模式。
`build.ps1` 里另有 `contractgen`、`supervision-metrics`、`conpty-probe` 等**未登记发布**的开发工具，
说明登记与否是显式选择：`aicli-mesh` 面向用户，应当登记进发布表。

### 7.6 与既有工具/脚本的关系

| 既有 | 关系 |
|------|------|
| `scripts/test-aicli-debug-endpoints-e2e.ps1` | 继续用 `/debug/endpoints` 驱动单进程；多进程场景（§12）改用 `aicli-mesh` 做发现与调用 |
| `docs/e2e/mesh-e2e.md` | **已落地**：多进程网格控制面场景（E2E-DEBUG-03，2026-09-24 由 debug-guide §8 独立成文），把「各进程 web endpoint 各自为战」升级为「网格统一发现 + 定向调用」；分工表（debug-guide §9）与相关文档同步更新 |
| `aicli resume` / `--web-port` | 不变；网格只是让「谁在跑」可见，并让 resume 能提示冲突 |

---

## 8. 与 Web 客户端 / 会话切换的衔接

### 8.1 三种「切换」语义（一次讲清）

| 语义 | 触发 | 结果 | 冲突处理 |
|------|------|------|----------|
| **本进程切换**（既有 in-place） | 侧栏「在当前进程切换」 | 当前进程 `/resume` 注入 | 目标会话被别的活节点占用 → 拒绝并提示（可显式接管） |
| **新窗口打开**（新增，默认） | 侧栏主点击 / `aicli-mesh open` | 复用已有节点地址，或拉起新进程 | 已有节点 → 直接复用；否则抢 spawn 锁拉起 |
| **接管**（显式） | `aicli-mesh open --takeover` / Web 端二次确认 | 所有权转移到新节点 | 旧节点保留运行，档案标 `orphaned` 并提示 |

### 8.2 新窗口打开流程

```text
用户点击「在新窗口打开」(同步 window.open("about:blank") 预开，规避弹窗拦截)
        |
        v
POST /web/api/mesh/spawn {session_id, port:0, wait_ms:8000}
        |
        +-- 读 nodes/ 找到该会话的活节点 -> status=reused, 返回其 URL
        |
        `-- 无活节点 -> 抢 leases/spawn-<sid>.lock
                 |-- 抢不到 -> 再读一次档案 -> reused（锁内二次检查）
                 `-- 抢到 -> 拉起 aicli resume <sid> --pprof --web-port <port> --web-token <token>
                            （cwd=会话工作区，detach，日志落 ~/.aicli/logs/mesh-spawn-<sid>.log）
                            -> 轮询 mesh/nodes/ 直到新节点 live（或超时）
                            -> status=started
        |
        v
前端 win.location.replace("http://127.0.0.1:<port>/web?token=<tok>&session=<sid>")
```

失败态：`not_running`（拉起后超时未见节点）/ `failed`（进程启动失败，附日志尾部 20 行）。
状态集合为 `reused | started | not_running | failed`（旧方案的 `opened` ≡ `reused|started`；
不返回 `starting` 中间态），**数据源从「端口档案」变成「节点档案」**。前端处理见 Web 侧方案 §7.4。

### 8.3 前端改动摘要

前端交互（状态徽标、跨工作区分组、`?session=` 深链、令牌处理）见
`aicli-micro-web-client-session-window-plan.md` §5（v2），差异只有两处：

1. 数据源从 `GET /web/api/sessions` 的 `endpoint` 字段改为 `GET /web/api/mesh/peers` 的 `nodes[]`
   （`sessions` 仍保留 `endpoint` 字段作为便捷视图，两者同源）。
2. 新增「节点」维度：同一会话可能同时有「历史」与「正在运行的节点」，UI 需要分别表达
   （`● 运行中 @127.0.0.1:55124` / `○ 已停止 · 上次 @127.0.0.1:55124`）。

### 8.4 冲突提示与深链

- 会话列表项在 `ownership=conflict` 时显示警示徽标，点开列出冲突节点与心跳时间。
- 深链 `?session=<id>` 打开时，若该会话已有活节点，页面顶部横幅提示「该会话正在另一个窗口运行」
  并提供「切换到那个窗口」按钮（= 复用对方 URL，不新建进程）。

---

## 9. 安全与隐私

### 9.1 令牌生命周期

| 阶段 | 存放 | 权限 | 说明 |
|------|------|------|------|
| 生成 | 内存 | — | 沿用既有：`--web-token` > `AICLI_WEB_TOKEN` > 每进程随机 |
| 写档案 | `nodes/<node_id>.json` | 0600（POSIX）/ 用户目录 ACL（Windows） | 只为让同用户进程之间能互相调用 |
| 读取 | 同用户进程 | — | `internal/mesh/client` 只在**回环**调用时读取 |
| 输出 | CLI/HTTP | — | 默认脱敏（`0f3a…`）；`--with-token` / `?reveal_token=1` 才给原文 |
| 退出 | 随档案删除 | — | 不留历史令牌 |
| 禁止 | journal / 日志 / 绑定 / `/debug/endpoints` | — | 有专门断言（§12） |

### 9.2 调用白名单与危险操作

- op 白名单硬编码在 `internal/mesh`（§5.6 表），未知 op 直接 `refused`。
- 写操作（`invoke`/`input`/`cancel`/`sessions.resume`）必须 `allow_write=true`；CLI 对应 `--allow-write`。
- `spawn`/`stop` 需要进程级开关（`--mesh-allow-spawn` 默认开、`--mesh-allow-stop` 默认关）。
- **工作区不参与权限判定**：写操作的门槛是「每次显式 `allow_write` + 审计」，与目标所在工作区无关（§9.3）；
  确需隔离的用户用 `--mesh-restrict-workspace`（默认关）。
- 目标方校验调用者来源是回环；跨机默认拒绝。
- 不提供「网格级别的 yolo」：没有「允许任何调用」的开关，避免一次误配置变成任意代码执行面。

### 9.3 跨工作区策略（工作区 = 筛选维度，不是权限边界）

| 场景 | 默认 |
|------|------|
| 只读（`status`/`screen`/`turn`/`sessions.list`）跨工作区 | 允许（与同工作区**无差别**） |
| 写操作（`invoke`/`input`/`cancel`/`sessions.resume`）跨工作区 | **允许**（每次仍需 `allow_write=true` + 审计） |
| `spawn` 跨工作区 | 允许（用户显式点了那个会话），记录审计 |
| `stop` 跨工作区 | 与同工作区同权；仍受进程级 `--mesh-allow-stop`（默认关）约束 |
| 可选收敛 | `--mesh-restrict-workspace`（默认关）开启后：跨工作区**写操作**拒绝（`mesh_cross_workspace_denied`），只读仍允许 |

理由：网格是**本机单用户**设施，安全边界是「回环 + 令牌 + 每次写操作的显式许可（`allow_write`）+ 审计」；
工作区是**组织维度**（按项目分组与过滤），不是权限边界。用户诉求是「显示并操作本机**所有**运行的进程」，
默认收敛会让跨项目的管理/协作变成「先改配置才能用」的二等公民；确需隔离的用户可显式开启收敛开关。
配套约束：`peers` 视图、目标解析（§7.2）、令牌读取都**不受工作区限制**。

### 9.4 非回环场景

- `--web-host 0.0.0.0` 时既有规则不变（所有请求需令牌）。
- 网格额外规则：`mesh/call`、`mesh/spawn`、`mesh/stop` 在非回环一律拒绝，除非 `--mesh-allow-nonloopback`。
- 节点档案在非回环模式下仍然 0600；`auth.mode=lan` 让消费方知道必须带令牌。

### 9.5 审计与隐私

- journal 记录：`op`、调用者/被调用者节点 ID、耗时、结果码、时间戳。
- journal **不记录**：prompt 正文、模型回复、令牌、屏幕内容（避免把用户数据复制到第二份文件）。
- 审计可关闭（`--mesh-journal=false`），关闭后 `watch`/`gc --keep-days` 相应退化，其余功能不受影响。

### 9.6 Windows 权限说明

Windows 没有 POSIX 权限位：`0600` 语义退化为「依赖用户目录 ACL」。因此：

1. 文档与 `doctor` 明确提示「Windows 下档案保密性依赖用户 Profile 权限」；
2. `doctor` 在 Windows 上检查档案所在目录是否位于用户 Profile 内（若被 `AICLI_MESH_DIR` 指到共享目录则告警）；
3. 不因为权限位不可用就拒绝写入（否则 Windows 上网格直接不可用）。

### 9.7 开关总表与回滚

| 开关 | 默认 | 作用 | 归属 |
|------|------|------|------|
| `AICLI_MESH_DIR` | 空（按 `AICLI_HOME` / home 推导） | 网格根目录覆盖 | §2.3 |
| `--mesh=false` | 开启 | **总开关**：不写档案、不订阅、不注册 `mesh/*` 端点；`aicli-mesh` 仍可读历史 | 本节 |
| `--mesh-allow-spawn` | 开启 | 允许 `mesh/spawn` 拉起进程 | §9.2 |
| `--mesh-allow-stop` | 关闭 | 允许 `mesh/stop`（治理动作） | §9.2 |
| `--mesh-restrict-workspace` | 关闭 | 收敛：跨工作区**写操作**拒绝 | §9.3 |
| `--mesh-allow-nonloopback` | 关闭 | 非回环下允许 mesh 写路径 | §9.4 |
| `--mesh-journal` | 开启 | 关闭后失去审计与对账，`watch` 退化 | §9.5 |

**回滚策略**：网格是**纯增量**能力——任一开关都能在不回退版本的前提下把行为退回今天
（`--mesh=false`，或直接删掉 `mesh/` 目录）；不存在「关掉网格就不能 chat」的状态（MN1）。
版本回退只需注意 §2.4 的一次性切换：旧版本不读 `mesh/`，粘性端口会退回随机端口行为。

---

## 10. 风险与对策

| # | 风险 | 影响 | 对策 |
|---|------|------|------|
| R1 | 档案写坏 / 半截文件 | 读者解析失败 | 临时文件 + 原子 rename；解析失败按 `unknown` 处理不崩溃 |
| R2 | 心跳线程与主循环争用 | 卡顿 | 心跳只做文件写 + 一次 rename，30s 一次；失败即退避 |
| R3 | 多进程同时 GC | 误删 | 删除条件含 pid 判定；先 rename 再删；`--apply` 才动手 |
| R4 | 租约误回收（时钟跳变） | 双占用 | TTL 判定用「本机单调时钟差值」优先，墙钟仅兜底 |
| R5 | 令牌泄露（档案被读） | 同机越权 | 0600 + 用户目录；默认脱敏输出；退出即删 |
| R6 | 扇入风暴（peer 事件过多） | SSE 卡顿 | 白名单事件 + 令牌桶 + 缓冲上限 + `mesh.lagged` 跳号 |
| R7 | spawn 拉起进程失败 | 新窗口打不开 | 单飞锁 + 二次检查 + 超时返回 `failed` + 日志尾部 |
| R8 | 残留进程堆积 | 资源占用 | 网格不隐式杀进程；`ls` 明示 + `stop` 显式；`doctor` 提示 |
| R9 | 磁盘占用（journal 增长） | 磁盘 | 7 天保留 + 8 MiB 轮转 + `gc` |
| R10 | 旧目录残留造成困惑 | 误判 | `gc --purge-legacy` + 文档明确「不兼容」 |
| R11 | 与既有 `web-ports` 逻辑并存导致双写 | 语义冲突 | 直接删除旧代码路径，不做双读双写 |
| R12 | 会话归属判定过于激进 | 误报 conflict | conflict 仅当「多个 live 节点声明同一会话」；stale 节点不参与判定 |
| R13 | `AICLI_MESH_DIR` 指向共享目录 | 令牌暴露给其它用户 | `doctor` 告警；写入前检查目录属主（POSIX） |
| R14 | 非回环误开 mesh 写路径 | 远程执行面 | 默认拒绝 + 显式开关 + 令牌 + 审计 |
| R15 | 工作区被误当作权限边界 | 跨项目操作被默认拒绝，与「工作区只是筛选维度」自相矛盾 | §9.3 明确默认全量可操作；隔离诉求用 opt-in 收敛开关表达 |
| R16 | 默认跨工作区可操作被滥用 | 误操作其它项目的进程 | 写操作仍需每次 `allow_write`（CLI `--allow-write`）；journal 审计；`stop` 默认关（§9.2 / §9.3） |

---

## 11. 实施路线图

> **施工执行细节**（逐切片改动面 / 验证命令 / 回滚手册 / 锚点核验）见
> [`aicli-mesh-implementation-plan.md`](./aicli-mesh-implementation-plan.md)；本节只给路线与依赖。

### 11.1 P0 — 网格地基（可见性，不拉进程）

| 交付 | 文件/位置 |
|------|-----------|
| 路径解析（`AICLI_MESH_DIR` / `AICLI_HOME` / home / fail-closed） | `backend/internal/aiclipaths`（新增 `DefaultMeshDir()`）+ `backend/internal/mesh/paths.go` |
| 节点档案读写（原子 rename、schema 校验、0600） | `backend/internal/mesh/registry.go` |
| 会话绑定读写（替代 `chat_web_port_store.go`） | `backend/internal/mesh/binding.go` |
| 租约（创建/续约/释放/回收） | `backend/internal/mesh/lease.go` |
| 聚合视图（扫描 + join + 归属判定 + 探活） | `backend/internal/mesh/view.go` |
| 进程接入：启动写档案、会话激活写绑定+租约、忙碌翻转、心跳、退出清理 | `backend/cmd/aicli/main.go`、`commands/chat_session.go`、`commands/chat_web_port_store.go`（删除并替换） |
| HTTP：`GET /web/api/health`、`/web/api/mesh/self`、`/web/api/mesh/peers` | `commands/web_handlers*.go` |
| 清单登记：`meshDebugEndpoints` 分组 | `commands/chat_debug_endpoints.go` |
| CLI：`ls / show / url / gc / doctor / version` | `backend/cmd/aicli-mesh/` + `internal/mesh/cli` |
| 发布登记 | `scripts/build.ps1` toolRegistry、`Makefile` |

**P0 完成即可回答**：本机有哪些 aicli 在跑（**跨全部工作区**）、服务哪个会话、地址是什么、上次地址是什么、谁是僵尸。

### 11.2 P1 — 调用与实时（新窗口打开）

| 交付 | 说明 |
|------|------|
| `GET /web/api/mesh/events`（SSE 扇入） | peer 订阅 + 白名单 + 令牌桶 + 续传 |
| `POST /web/api/mesh/call` | op 白名单 + 令牌转发 + 审计 + 错误信封 |
| `POST /web/api/mesh/spawn` | 单飞锁 + detach 启动 + 就绪等待 + URL 生成 |
| CLI：`call / send / screen / watch / open` | 复用 `internal/mesh/cli` |
| Web 前端：主点击=新窗口、节点徽标、`?session=` 深链、冲突横幅 | 见 `aicli-micro-web-client-session-window-plan.md` §5（v2） |
| `/web/api/sessions` 增加 `endpoint`/`ownership` 便捷字段 | 与 peers 同源 |

### 11.3 P2 — 协作与治理

| 交付 | 说明 |
|------|------|
| 接管（`--takeover`）+ 旧节点 `orphaned` 提示 | §4.4 |
| 工作区收敛开关（`--mesh-restrict-workspace`，opt-in，默认关）+ 分组侧栏 | §9.3 |
| `POST /web/api/mesh/stop`（默认关）+ CLI `stop` | §5.7 |
| journal 审计查询（`show --events`、`watch --since`） | §6.6 |
| `aicli mesh <sub>` 别名 | §7.5 |
| About/诊断页展示网格信息（节点数、冲突、GC 建议） | Web 端 |

### 11.4 最小切片顺序（建议提交粒度）

| 切片 | 内容 | 可验证结果 |
|------|------|-----------|
| S1 | `internal/mesh` 的 paths + registry + 单元测试 | `go test ./internal/mesh/...` 绿 |
| S2 | 进程启动写档案 + 退出清理 + 总开关（`--mesh=false`）+ `/web/api/health` | 手工：启动两个进程，`ls` 看到两份档案；带 `--mesh=false` 的进程不出现 |
| S3 | binding 替换 `web-ports`（含 sticky 端口回归测试） | 既有 `pprof_port_reuse_test.go` 改造后绿 |
| S4 | lease + 归属判定 + conflict 视图 | 双进程同会话 → `conflict` 可见 |
| S5 | `/web/api/mesh/self|peers` + 清单登记 | E2E-DEBUG-01 的清单覆盖门禁仍绿 |
| S6 | `aicli-mesh ls/show/url/gc/doctor` + 发布登记 | `build.ps1 -Tools aicli-mesh` 出包 |
| S7 | `mesh/events` SSE 扇入 | 两进程互见实时状态 |
| S8 | `mesh/call` + CLI `call/send/screen` | A 调用 B 完成一轮 prompt |
| S9 | `mesh/spawn` + `open` + 前端新窗口 | 浏览器点一下开新窗口 |
| S10 | 多进程 E2E（E2E-DEBUG-03）+ 基线固化 | 聚合回归绿 |

### 11.5 依赖与顺序约束

- S3 依赖 S1/S2（绑定替代必须先有 registry 的路径解析）。
- S5 之后才做 S7/S8（先有稳定视图，再谈实时与调用）。
- S10 必须在 S6 之后（E2E 用 CLI 做发现）。
- 全过程中 E2E-DEBUG-01/02 必须保持绿（MN5）。

### 11.6 改动面清单（落地 checklist）

> **落地状态：已完成（S1–S10，2026-09-24）。** 下表是设计期的改动面预估；实际落地与偏差见
> [aicli-mesh-implementation-plan.md](./aicli-mesh-implementation-plan.md) §15.3 的 D1–D12：
> 新增 `host.go` / `fanin.go` / `spawn.go`（D1–D3）、`cli.go` 未拆分（D4）、
> 前端不新增 `js/mesh.js`（D5）、`web_page.go` 深链自举（D6）、
> **Windows 判活必须读退出码**（D7）、`call`/`send` 目标解析与 CLI 合并为唯一实现（D8）、
> E2E M3/M5 的断言口径与前置（D9/D10）。
> Web 侧 `sessions.endpoint/ownership` 与 `resume running_elsewhere` **已落地（S11）**，D11/D12 收敛：
> 见 [aicli-mesh-implementation-plan.md](./aicli-mesh-implementation-plan.md) §19.4 与
> [web-remote-api.md](../aicli/web-remote-api.md) §9.7（前端手工清单见 web-testing.md §2.7.1）；
> `mesh/events` 前端订阅（P1 ②）仍留给 S12。
> 固化验证：E2E-DEBUG-03 单跑 13/13 绿（`artifacts/aicli-debug-endpoints-e2e-mesh/run5/`），
> 聚合 01 → 02 → 03 全绿（`PASS=6 FAIL=0`，`artifacts/aicli-e2e-all/20260924-131132/`）。

**代码：删除 / 改造 / 新增**

| 动作 | 位置 | 说明 |
|------|------|------|
| 删除 | `backend/cmd/aicli/commands/chat_web_port_store.go`（含 `chat_web_port_store_test.go`） | 语义迁到 `internal/mesh/binding.go`；同名 sanitize 规则保留 |
| 改造 | `backend/cmd/aicli/commands/pprof_port_reuse.go:70`（`LoadChatWebPortRecord`）、`backend/cmd/aicli/main.go:154`（`SaveChatWebPortRecord`）、`backend/cmd/aicli/commands/chat_session.go:196,281` 注释 | 全部改走 `internal/mesh` 绑定 |
| 改造 | `backend/cmd/aicli/pprof_port_reuse_test.go` | 粘性端口用例迁到 binding（S3 回归） |
| 改造 | `backend/internal/aiclipaths/paths.go:25-32` | 新增 `DefaultMeshDir()`；`DefaultWebPortsDir()` 随旧代码删除 |
| 新增 | `backend/internal/mesh/{paths,registry,binding,lease,journal,client,view,cli}.go`、`backend/cmd/aicli-mesh/` | §11.1–§11.2 切片 |
| 改造 | `commands/web_handlers*.go`、`commands/web_schema.go`、`commands/chat_debug_endpoints.go` | mesh 端点 + `sessions` 便捷字段 + 清单登记 |

**文档：落地时必须同步（否则出现「文档说 A、代码做 B」）**

| 文档 | 需改内容 |
|------|----------|
| `docs/user-guide/aicli.md:259` | `AICLI_WEB_PORTS_DIR` → `AICLI_MESH_DIR`；「粘性端口档案」改为「会话绑定」 |
| `docs/aicli/debug-chat-status.md:53-62` | 端口档案路径 `~/.aicli/web-ports/` → `mesh/bindings/` |
| `docs/aicli/web-remote-api.md` | `sessions` 新增字段（`endpoint` / `ownership` / …）与 `resume` 新错误码 |
| `docs/e2e/mesh-e2e.md` | **已同步**（场景 + M1–M10 + 故障排查；2026-09-24 由 debug-guide §8 独立成文） |
| `docs/aicli/mesh-cli.md` | 新增（§7.5）；本方案 §7 是设计草稿，落地后以该文档为准 |
| 本方案 + Web 子方案 | 实现完成后回填「已落地 / 偏差」标注，保持设计文档与代码一致 |

**脚本 / 构建 / CI**

| 位置 | 动作 |
|------|------|
| `scripts/build.ps1` 的 `$script:toolRegistry`（第 83 行） | 追加 `aicli-mesh`（§7.5） |
| `Makefile` | 追加 `aicli-mesh:` 目标 |
| `scripts/test-aicli-e2e-all.ps1` + `scripts/e2e-assertion-baseline.json` | 追加 E2E-DEBUG-03 场景并 `-UpdateBaseline` 固化（§12.3） |
| CI | 保持 E2E-DEBUG-01/02 基线门禁（MN5）；新增 `go test ./internal/mesh/...` 与 `aicli-mesh --help` 冒烟 |

---

## 12. 测试与验收

### 12.1 单元测试（`backend/internal/mesh`）

| 用例 | 断言 |
|------|------|
| 路径解析 | `AICLI_MESH_DIR` > `AICLI_HOME` > home；无 home 时 fail-closed（只读空、写入跳过） |
| 文件名安全 | 会话 ID 含 `..`、`/`、`\`、空格 → 被规整或拒绝 |
| 档案往返 | 写 → 读 → 字段一致；半截文件 / 非法 JSON → `unknown` 不 panic |
| 心跳与 stale | 心跳超 TTL → `stale`；pid 不存在 → `stale` |
| 租约 | 并发获取只有一个成功；pid 死亡 → 可回收；TTL 到期 → 可回收；续约刷新 |
| GC | 只删条件满足的对象；`--apply` 前后 dry-run 输出一致；pid 存活绝不删 |
| journal | 追加写、轮转、脱敏（不含 token 字段） |
| 归属聚合 | 单 owner → `owner`；双 live → `conflict`；stale 不参与 |
| 跨工作区聚合 | 两份不同 `workspace.path` 的档案默认都进视图；`scope=self` / `workspace=` 只过滤输出，归属/冲突判定仍全量；无 `workspace` 节点归 `(无工作区)` |
| schema 演进 | 未知大版本 → `unknown`（可展示、不参与归属、不可 `call`）；新增可选字段不 bump |
| 探活预算 | 并发上限与总预算生效：超预算节点标 `skipped` 而非 `unreachable` |
| 扇入防环与 seq | A↔B 互订：`mesh.peer.event` 不被二次转发（无回声）；SSE `seq` 与 journal `seq` 同源且单调 |

### 12.2 HTTP 层测试（`web_handlers_test.go` 风格）

- `GET /web/api/health`：无会话时 200 且 `session_active=false`。
- `GET /web/api/mesh/self`：默认脱敏；`?reveal_token=1` 回环返回原文。
- `GET /web/api/mesh/peers`：构造 2 live + 1 stale + 1 损坏档案 → counts 正确、排序正确、`probe=0` 不发网络请求。
- `GET /web/api/mesh/peers`：**默认 `scope=all`**（跨工作区节点在列）；`scope=self` / `workspace=` 只过滤 `nodes[]`，
  `counts` 仍为全量口径且 `filter` 段回显条件。
- `POST /web/api/mesh/call`：未知 op → `refused`；写 op 无 `allow_write` → `refused`；非回环调用 → `refused`；正常路径透传幂等键。
- 鉴权：严格模式 / 非回环下 mesh 端点行为与 §5.8 矩阵一致。
- `--mesh=false`：不写档案、不注册 `mesh/*` 端点；`aicli-mesh ls` 仍能读历史（stale / 绑定）。
- 令牌轮换：目标重启换 token → 调用方重读档案重试一次成功；二次失败 → `refused`（`mesh_token_stale`）。

### 12.3 多进程 E2E（`E2E-DEBUG-03`，已落到 [mesh-e2e.md](../e2e/mesh-e2e.md)）

新增第三个场景，harness 启动 **A/B 两个真实进程**（`--pprof`，端口从节点档案 `endpoint.port` 读取），全部交互走网格：

| 断言 | 内容 |
|------|------|
| M1 `mesh/discovery-both-nodes` | 两节点都出现在 `aicli-mesh ls --json` 与 `/web/api/mesh/peers` |
| M2 `mesh/cli-api-parity` | CLI 与 HTTP 两个视图的节点/会话/地址一致（同源校验） |
| M3 `mesh/session-lease-exclusive` | B 尝试 resume A 的会话 → `busy`/`running_elsewhere`，无第二个进程声明该会话 |
| M4 `mesh/cross-call-invoke` | A 通过 `mesh/call` 让 B 跑一轮 prompt，拿到 `turn_id` + `assistant`，`duplicate=false` |
| M5 `mesh/realtime-fanin` | B 的忙碌翻转在 A 的 `/web/api/mesh/events` 时间窗内可见 |
| M6 `mesh/crash-reconcile` | 强杀 B → A 的 peers 视图在 TTL 内把 B 标 `stale`；`gc --apply` 后档案消失 |
| M7 `mesh/no-token-leak` | peers 默认输出、journal 文件、`/debug/endpoints` 均不含令牌原文 |
| M8 `mesh/legacy-purge` | `gc --purge-legacy --apply` 只删旧目录，`mesh/` 不受影响 |
| M9 `mesh/self-containment` | 杀掉全部节点后 `aicli-mesh ls` 仍可读（含 stale 与绑定），不报错 |
| M10 `mesh/cross-workspace-ops` | B 以**另一个工作区**（不同 cwd）启动：默认 `ls`/`peers` 同时列出两个工作区（`workspace.path` 不同）；A 对 B 的写调用（`invoke`）默认成功；开启 `--mesh-restrict-workspace` 后同一调用 → `refused` + `mesh_cross_workspace_denied` |

**与既有 E2E 机制的对接**（事实，需同步修改）：

1. `scripts/test-aicli-e2e-all.ps1` 的 `$script:scenarios` 是**硬编码两场景**的表（第 108–131 行）→ 追加第三项。
2. 同脚本第 236–245 行按 `id` 硬编码每个场景的参数分支 → 增加 `E2E-DEBUG-03` 分支（端口/二进制/超时）。
3. 断言基线 `scripts/e2e-assertion-baseline.json`（`{version, generated_at, note, scenarios[]}`）→ 跑
   `-UpdateBaseline` 固化第三场景；基线抽取规则是源码里的 `Add-Result` / `Add-Skip` 字面量（第 138 行正则），
   因此新 harness 必须用同款调用点命名（`mesh/...`）。
4. 聚合脚本对 `summary.json` 做字段归一化（01 = `passed/failed`，02 = `pass/fail/skip`）并交叉校验
   `results == PASS+FAIL`；新场景建议直接用 `pass/fail/skip` + `results[]`，避免再添一种方言。
5. 观测工具集 `scripts/aicli-e2e-harness.ps1` 已提供 `Invoke-HarnessRequest`（返回 `ok/status_code/text/json/ms/error`）、
   `Get-AicliTimelineSample`、`Wait-AicliScreenStable`、`Save-AicliDiagnostics`、`Test-AicliEndpointCoverage`
   （清单覆盖门禁）→ 新场景 dot-source 复用，并对 `mesh/*` 新端点补断言或带理由豁免。

### 12.4 回归门禁

- E2E-DEBUG-01（28 条断言）与 02（31 条断言）**全绿**是硬门禁；`-BaselineOnly` 秒级门禁进 CI。
- 既有 `pprof_port_reuse_test.go`、`loopback_addr_test.go`、`web_handlers_test.go` 必须同步改造（粘性端口迁移）。
- 单进程场景下网格写入失败不得产生用户可见错误（断网/只读目录手工验证）。
- 新增 `go test ./internal/mesh/...` 与 `aicli-mesh --help` 冒烟进 CI（与既有清单覆盖门禁并列）。

### 12.5 手工验收清单

1. 两个终端各起一个 `aicli chat --pprof`：`aicli-mesh ls` 看到两行，地址、会话、工作区正确。
2. `aicli-mesh url <session>` 复制到浏览器：能直接打开对应窗口。
3. 在 A 的 Web 界面点另一个会话的「在新窗口打开」：新窗口起来，A 不受打扰。
4. `aicli-mesh watch` 期间在 B 里跑一轮：A 的界面秒级看到 B 变忙/变闲。
5. 强杀 B：`ls` 立刻标 `stale`，`gc --apply` 后干净。
6. `doctor`：无告警（或告警可解释）。
7. 跨工作区：W1 起 A、W2 起 B → `ls` 默认同时列出两行（工作区列不同）；`aicli-mesh call <B> invoke --allow-write`
   成功；以 `--mesh-restrict-workspace` 重启后同一调用被拒（`mesh_cross_workspace_denied`）。

---

## 13. 开放问题

| # | 问题 | 建议 |
|---|------|------|
| Q1 | 侧栏主点击默认「新窗口」还是「本进程切换」？ | 默认新窗口（网格成熟后）；P0 阶段先只提供显式按钮 |
| Q2 | 未运行的会话点「打开」是否自动拉起进程？ | 二次确认后拉起（spawn 会创建高权限进程） |
| Q3 | 端口分配：继续粘性优先 + 随机兜底？ | 是（绑定已持久化该偏好） |
| Q4 | 令牌是否允许跨工作区读取？ | **允许**：工作区只是筛选维度（§9.3）；写调用仍要求每次 `allow_write=true`；令牌默认脱敏、前端永不缓存 |
| Q5 | peers 视图默认是否跨工作区？ | **默认 `scope=all`（硬契约）**；`scope=self` / `workspace=` 仅过滤输出，归属判定永远全量 |
| Q6 | 是否提供「停止节点」按钮？ | P2 提供，默认关闭（`--mesh-allow-stop`） |
| Q7 | 是否保留 in-place 切换？ | 保留（终端场景需要），但冲突时必须提示 |
| Q8 | 接管（takeover）是否要通知旧节点？ | 要：旧节点档案标 `orphaned` + UI 横幅 |
| Q9 | journal 默认开启吗？ | 默认开启（审计价值高、成本低）；可 `--mesh-journal=false` 关闭 |
| Q10 | `aicli mesh` 别名是否要做？ | P2 做，复用同一 CLI 包，避免两套实现 |
| Q11 | 节点数上限与扇入策略？ | 默认全连；>16 节点降级为「优先同工作区 + 最近活跃 N 个」——**仅推送覆盖降级**，不改变可见性与可操作性（§6.2） |
| Q12 | 是否需要「网格状态落盘快照」供离线分析？ | 不需要：`ls --json > file` 已足够，避免第二份真源 |
| Q13 | 无工作区 / 多工作区进程如何呈现？ | 分组键取 `workspace.path`；缺省归 `(无工作区)`；一个进程一个工作区（cwd 语义），不做多值 |
| Q14 | 网格出问题如何一键回退？ | 进程级 `--mesh=false`：不写档案、不订阅、不注册 `mesh/*` 端点，`aicli-mesh` 仍可读历史（§9.7）；版本回退见 §2.4 |

---

## 附录 A：术语对照（旧 → 新）

| 旧（作废） | 新 | 变化要点 |
|------------|-----|----------|
| `~/.aicli/web-ports/<sid>.json` | `~/.aicli/mesh/bindings/<sid>.json` | 从「端口缓存」升级为「会话↔地址绑定」，不含令牌 |
| `~/.aicli/session-endpoints/<instance>.json`（计划中，未落地） | `~/.aicli/mesh/nodes/<node_id>.json` | 从「会话端点」改为「进程节点」，键从会话变节点 |
| `<sid>.spawn.lock` | `mesh/leases/spawn-<sid>.lock` | 统一为租约模型（TTL + pid + 续约） |
| `ChatWebPortRecord{session_id,port,host,updated_at}` | `NodeRecord` + `SessionBinding` | 一拆为二：活体状态与持久偏好分离 |
| `AICLI_WEB_PORTS_DIR` | `AICLI_MESH_DIR` | 覆盖整个网格根 |
| 「会话端点注册表」 | 「节点网格 / 网格注册表」 | 命名与架构语义对齐（多进程协作，而非会话列表） |
| 端点状态 `live/stale/stopped` | 保留，另加 `ownership` 与 `reachability` 两个维度 | 状态词汇从「一维」变「三维」 |

## 附录 B：与旧方案章节对照

下表针对 `aicli-micro-web-client-session-window-plan.md` 的 **v1** 结构。该文档已于 2026-09-24
按本方案重写为 **v2**（Web 客户端子方案），最后一列给出其在 v2 中的位置；
v2 新增 §0「本文与网格方案的分工」与附录 C「v1 → v2 变更对照」。

| 旧文档章节（v1） | 状态 | 说明 | v2 位置 |
|------------|------|------|---------|
| §1 背景与问题 | **保留** | 问题定义仍然有效（本方案 §1.2 用 G1–G6 重新归纳） | v2 §1（+ P→G 映射表） |
| §2 现状调研 | **保留** | 事实清单继续有效（本方案 §1.1 引用） | v2 §2（补各条归口） |
| §3 需求分析 | **保留** | FR/NFR 由本方案 §1.3 的 MF/MN 承接 | v2 §3（FR/NFR 加「归口」列；NFR3 作废） |
| §4 总体设计（两条路径） | **部分保留** | 「两条路径并存」升级为「三种切换语义」（本方案 §8.1） | v2 §4 |
| §5 数据模型（session-endpoints） | **被取代** | 见本方案 §2/§3 | v2 §0/§6.1（只引用，不再自建） |
| §6 生命周期 | **被取代** | 见本方案 §4 | v2 §0（只引用） |
| §7 接口设计 | **被取代** | 见本方案 §5 | v2 §6（Web 侧契约子集 + 便捷字段） |
| §8 前端交互 | **保留** | 本方案 §8.3 只列差异 | v2 §5（升级版：三维状态 + 实时 + 冲突） |
| §9 spawn 路径 | **被取代** | 见本方案 §5.7/§8.2 | v2 §7（前端消费视角） |
| §10 风险 | **保留并扩充** | 本方案 §10 | v2 §8（加归口列，R10 已消除） |
| §11 路线图 | **被取代** | 见本方案 §11 | v2 §9（对齐 S1–S10） |
| §12 测试 | **保留并扩充** | 本方案 §12 增加多进程 E2E | v2 §10（E1–E8 → M1–M10 映射） |
| §13 开放问题 | **被取代** | 见本方案 §13 | v2 §11（收敛为 Web 专属） |

## 附录 C：目录与文件权限矩阵

| 路径 | 内容 | POSIX | Windows | 含秘密 |
|------|------|-------|---------|--------|
| `mesh/` | 网格根 | 0700 | 用户目录 | 否（子项有） |
| `mesh/nodes/<id>.json` | 节点档案（含令牌） | 0600 | 用户目录 ACL | **是** |
| `mesh/bindings/<sid>.json` | 会话↔地址绑定 | 0644 | 用户目录 | 否 |
| `mesh/leases/*.lock` | 租约 | 0600 | 用户目录 | 否 |
| `mesh/journal/<id>.ndjson` | 事件日志 | 0600 | 用户目录 | 否（脱敏后） |
| `~/.aicli/logs/mesh-spawn-<sid>.log` | spawn 子进程 stdout/stderr | 0600 | 用户目录 | 否（启动行可能含令牌 → 需脱敏，见 §12.3 M7） |
