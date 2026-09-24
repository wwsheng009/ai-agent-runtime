# aicli micro web client — 会话切换「新窗口打开」与网格可见性（Web 侧方案）

> 文档状态：🚧 **部分落地**（v2，已对齐节点网格架构；S9 落地「⧉ 新窗口打开 + 深链 + spawn/open 端点」，
> 其余 P0/P1 前端与 Web 层项未落地——**以 §0.1 为权威状态**，§5–§7 仍是设计目标而非现状）
> 定位：**`aicli-mesh-architecture.md` 的 Web 客户端子方案**。本文只定义「前端交互 + Web 侧接口契约 +
> Web 侧验收」；数据模型、生命周期、网格控制面、spawn 机制、CLI 工具一律由网格方案定义，本文不重复。
> 上位方案：[`aicli-mesh-architecture.md`](./aicli-mesh-architecture.md)（命名 `~/.aicli/mesh/`、
> 控制面 `/web/api/mesh/*`、`aicli-mesh` 工具、多进程 E2E）
> 适用范围：`backend/cmd/aicli/commands/web/js/*`（前端）、`commands/web_handlers.go` / `web_schema.go`
> （Web 端点）、`commands/chat_debug_endpoints.go`（清单登记，只读消费）
> 关联概念：`backend/internal/mesh`（网格共享包，本文唯一聚合数据源）、`backend/internal/workspaceregistry`、
> `backend/internal/sessionmeta`
>
> **变更记录**
> - **v2（2026-09-24）**：按节点网格架构重写。删除自建「会话端点注册表」设计（旧 §5/§6/§7/§9/§11/§13），
>   改为**网格消费方**；前端交互（旧 §8）保留并升级为本文 §5；问题定义与现状调研（旧 §1/§2/§3）保留。
>   逐项对照见[附录 C](#附录-cv1--v2-变更对照)。
> - v1（2026-09-24 早稿）：会话端点注册表 + 新窗口打开（**未实施，设计已作废**）。

---

## 目录

- [0. 本文与网格方案的分工（先读这个）](#0-本文与网格方案的分工先读这个)
- [1. 背景与问题](#1-背景与问题)
- [2. 现状调研：活动会话信息是怎么保存的](#2-现状调研活动会话信息是怎么保存的)
- [3. 需求分析](#3-需求分析)
- [4. 总体设计](#4-总体设计)
- [5. 前端交互设计](#5-前端交互设计)
- [6. Web 侧接口契约](#6-web-侧接口契约)
- [7. 新窗口打开：复用与拉起](#7-新窗口打开复用与拉起)
- [8. 风险与对策](#8-风险与对策)
- [9. 实施路线图（对齐网格 S1–S10）](#9-实施路线图对齐网格-s1s10)
- [10. 测试与验收](#10-测试与验收)
- [11. 开放问题（Web 专属）](#11-开放问题web-专属)
- [附录 A：术语](#附录-a术语)
- [附录 B：本节点 Web 端点清单](#附录-b本节点-web-端点清单)
- [附录 C：v1 → v2 变更对照](#附录-cv1--v2-变更对照)

---

## 0. 本文与网格方案的分工（先读这个）

v1 把「问题定义 + 数据模型 + 接口 + spawn + 路线图」全写在一份文档里，与网格方案产生了两套存储、
两套状态词汇、两套 API 的设计冲突。v2 明确切开：**网格方案是架构唯一真源，本文是它的 Web 侧实现说明**。

| 主题 | 定义方（唯一真源） | 本文的角色 |
| --- | --- | --- |
| 命名与目录（`mesh/{nodes,bindings,leases,journal}`） | 网格 §2 | 只引用 |
| 数据模型（节点档案 / 会话绑定 / 租约 / journal） | 网格 §3 | 只引用 |
| 生命周期（写入 / 读取 / 探活 / 租约 / GC / 降级） | 网格 §4 | 只引用 |
| 控制面 API（`/web/api/mesh/*`、`/web/api/health`） | 网格 §5 | 列「前端用到的子集」→ §6.1 |
| 实时（SSE 扇入、帧格式、背压、续传） | 网格 §6 | 前端订阅与降级策略 → §5.6 |
| `aicli-mesh` 工具 | 网格 §7 | 失败态里**建议命令**，不重复实现 |
| 新窗口打开的复用 / 拉起流程 | 网格 §5.7 / §8.2 | 前端交互与失败文案 → §7 |
| **前端交互（徽标 / 分组 / 深链 / 令牌 / 实时）** | **本文 §5** | 定义 |
| **Web 侧便捷契约（`sessions.endpoint/ownership`、`resume running_elsewhere`）** | **本文 §6** | 定义（与 peers 同源，不新增聚合逻辑） |
| 风险 | 网格 §10 + 本文 §8 | 网格承担项 + Web 专属项 |
| 路线图 | 网格 §11（S1–S10） | 标注 Web 侧落点 → §9 |
| 测试与验收 | 网格 §12 | Web 侧断言 + E2E 映射 → §10 |
| 开放问题 | 网格 §13 | 只留 Web 专属 → §11 |

**建议阅读顺序**：网格 §0 → 网格 §2/§3/§5 → 本文 §5–§7。

**三条不可越界的实现纪律**（评审重点）：

1. 前端**不读文件系统**、不拼 `mesh/` 路径、不自己解析档案——一切经本节点 HTTP API（网格 §7.1「CLI 与 Web 同源聚合」）。
2. Web 端点**不新写聚合逻辑**：`sessions.endpoint`、`peers.nodes` 必须复用 `internal/mesh/view` 的同一份结果。
3. 浏览器**只持有本节点令牌**，不缓存、不落 `localStorage`、不在 UI 显示 peer 令牌（网格 §6.5、§9.1）。

---

### 0.1 落地状态（2026-09-24 回填）

> 网格侧 S1–S10 已完成（见[实施计划](./aicli-mesh-implementation-plan.md) §10、§15.3）；Web 侧
> **S11 / S12 / S13 / S14 四批收口已落地**（§19–§22）。下表逐项回填，未落地项不视为已交付；
> P2 逐项对账见本表末五行（①③④⑤ 已落地，② 留 S15）。

| 交付项 | 状态 | 落点 / 说明 |
| --- | --- | --- |
| P1 ① 主点击 = 新窗口 + 预开窗口三态（失败关窗 + Toast） | ✅ 已落地 | `web/js/sessions.js`（`⧉`，悬停出现；手势内 `window.open` 占位 → `POST /web/api/mesh/spawn` → `location.replace(url)`）、`web_handlers_mesh.go`、`internal/mesh/spawn.go` |
| P1 ⑤ `?session=` 深链 | ✅ 已落地（无独立横幅 UI） | `web_page.go` 注入 head 内联脚本（`?token=` → `sessionStorage` + `replaceState` 抹除；`?session=` → `window.__aicli_deep_link_session`）+ `sessions.js::applyDeepLinkSession`（与 `current_session_id` 不同才 resume） |
| `aicli-mesh open`（网格 S9，本文范围外） | ✅ 已落地 | `internal/mesh/cli.go`（`--port/--wait/--no-wait/--json`） |
| P0 ① 侧栏徽标 + 端点行 | ✅ 已落地（S11） | `sessions` 条目新增 `session_state/ownership/conflict_count/workspace_*/endpoint/last_known`，响应新增 `self`/`workspaces`（`web_handlers_mesh_sessions.go`）；前端徽标 + 端点行见 `web/js/sessions.js`。偏差 **D11** 已收敛 |
| P0 ② 「仅复用 + `auth_required=false`」限制 | ⤳ 被取代 | S9 起主点击恒走 `mesh/spawn`（服务端复用活节点、必要时拉起），该 P0 阶段限制不再适用 |
| P0 ③ 打开方式开关 | ✅ 已落地（S11，默认取 `new_window`） | 侧栏 `#sessions-open-mode` + `localStorage: webSessionOpenMode`（`web/js/sessions.js`）；Q13 口径：只在用户未显式设置过时按新默认，显式选过 `in_place` 的老用户保持原选择 |
| P0 ④ 「关于」页网格小节 | ✅ 已落地（S11） | `web/js/ui.js::loadAboutMesh` 只读渲染 `GET /web/api/mesh/self` + `peers` 的 `counts`；**不提供** gc / stop / spawn 按钮（§5.8） |
| P1 ② `mesh/events` 实时徽标 + 退避重连 + 轮询兜底 | ✅ 已落地（S12） | `web/js/sessions.js` 订阅 `GET /web/api/mesh/events`（**D5**：不新增 `js/mesh.js`）：帧只作刷新信号 → `sessions?scope=all` 同源重算（200ms 合并）、`mesh.lagged` 全量兜底、1s→2s→4s…≤30s 退避 + `?since_seq=` 续传、SSE 不可用降级 10s 轮询；本进程令牌经 `util.js::webAuthToken` 单源。§10.2「实时徽标」断言**可执行**（手工清单见 `docs/aicli/web-testing.md` §2.7.2） |
| P1 ③ `resume` 的 `running_elsewhere` 三段式弹窗 | ✅ 已落地（S11） | `POST /web/api/sessions/resume` 未带 `force` 时先做归属检查（`running_elsewhere` / `conflict`，`web_handlers_mesh_sessions.go::chatWebResumeMeshGuard`）；前端三段式弹窗见 `web/js/sessions.js`。偏差 **D12** 已收敛 |
| P1 ④ 跨工作区分组 | ✅ 已落地（S11） | 「其他工作区（N）」可折叠分组（`?scope=all` 合并 peer 会话 + `workspaces[]` 汇总） |
| P1 ⑥ 开关默认切 `new_window` | ✅ 已落地（S11） | 同 P0 ③（`new_window` 为缺省；`in_place` 可选） |
| P2 ① 冲突详情横幅（节点列表 + 心跳） | ✅ 已落地（S11） | `conflict` 响应带 `nodes[]`（`node_id/pid/workspace/heartbeat_at`，`web_handlers_mesh_sessions.go`）；前端 `sessions.js::showSessionConflict` 渲染 `#session-conflict-nodes`（`节点 · pid · 工作区 · 心跳`）。只读：不提供「强制接管」入口（§5.4） |
| P2 ② 接管二次确认（`takeover`） | ✅ 已落地（S15） | `POST /web/api/sessions/resume {takeover:true}` → `chatWebTakeoverSession` 走 `Host.TakeoverSession`（网格 §4.4：唯一抢活租约的入口），成功回 `status=taken_over` + `previous_owner_node_id`，失败回 `takeover_failed`（不注入、不 5xx）；`takeover_available` 只在 `running_elsewhere` 且非 `conflict` 时为 `true`。前端 `session-conflict-takeover-btn` 二次确认（首次点击切确认态，再点才发）；旧节点下一跳心跳标 `orphaned` + 徽标「⚠ 已让渡」+ toast。CLI 入口 `aicli-mesh open <session> --takeover` |
| P2 ③ 收敛开关下的 `refused` 文案（`mesh_cross_workspace_denied`） | ✅ 已落地（S13） | `sessions.js::SPAWN_CODE_TEXT`（code → 可执行文案）+ §5.2 回退路径：`refused` → `aicli-mesh open <session> --print-url`，拉起失败 → `aicli-mesh show <session>`。注：`mesh_cross_workspace_denied` 目前只在 CLI/Agent 面的 `mesh/call` 上触发（§6.1 边界原则：前端不用 `call`），Web 侧映射是前瞻性的 |
| P2 ④ resume 的 SSE 事件化（去 8×300ms 轮询） | ✅ 已落地（S14） | **实测判定**：`session_start/session_end` 是 turn 边界事件（唯一发布点 `internal/chat/actor.go`），`/resume`、`/new`、`/load` 不产生 turn → 「订阅既有事件」不成立，改**服务端补发**：SSE handler 每 250ms 看会话身份，合成 `session_switched`（`web_handlers.go::chatWebSessionSwitchNotice`）；前端 `sessions.js` 删两处 8×300ms 轮询，`sse.js` 订阅该事件刷新，仅保留单次 4s 断连兜底。resume handler 的 stale 注释一并纠偏 |
| P2 ⑤ 窗口标题加节点后缀 | ✅ 已落地（S13） | `sessions.js::meshNodeSuffix()`（`· <工作区> · <节点短 id>`；网格不可用时空串）+ `chat.js::updateTitle` 拼接；`applyMeshView` 在 self 段变化时重算（否则要等下一次状态翻转才出现） |
| 偏差 D5 / D6 | ✅ 已登记 | 不新增 `js/mesh.js`（并入 `sessions.js`）；`web_page.go` 深链自举（计划未列该文件） |

> **已验证部分**：`web_handlers_mesh_spawn_test.go`（参数透传 / 四态 / 单飞 / 失败带日志尾部）、
> `internal/mesh/spawn_test.go`、`cli_open_test.go`；E2E-DEBUG-03 的 M1–M10 覆盖**网格行为**
> （归属不变、令牌不泄漏等），**不覆盖**前端 JS。§10.2 手工断言见 `docs/aicli/web-testing.md` §2.7
> （S12 起含「实时徽标」，见同文 §2.7.2；S13 起含「标题后缀 / refused 文案」，见 §2.7.3；
> S14 起含「切换事件化」，见 §2.7.4）；
> 前端 asset 契约由 `web_handlers_mesh_sessions_test.go` / `web_handlers_mesh_realtime_test.go` /
> `web_handlers_mesh_polish_test.go` / `web_handlers_session_switch_test.go` 锁定。

---

## 1. 背景与问题

### 1.1 现有切换机制

micro web client（`/web/`）由 aicli **当前聊天进程自己**的 loopback HTTP 服务器伺服
（`--pprof` / `--debug` / `--web-port` 启动，`backend/cmd/aicli/main.go:126-150`）。
因此「一个进程 = 一个当前会话 = 一个 web 端点」，浏览器里的所有窗口共享同一份会话状态。

当前会话切换链路：

```
web/js/sessions.js  resumeSession(id)
  → 确认弹窗（sessionSwitchOverlay）
  → POST /web/api/sessions/resume {session_id}
      → web_handlers.go HandleChatWebAPISessionsResume
      → queue.routeInputText("/resume <id>") + session.wakeComposerRead()   // 注入当前进程输入队列
  → 主循环（TTY composer 侧）消费队列 → 在【同一个进程内】执行 /resume
  → 前端轮询 GET /web/api/sessions（8 次 × 300ms）等待 current_session_id 变化
```

### 1.2 问题：in-place resume 会干扰当前进程

| # | 干扰点 | 说明 |
| --- | --- | --- |
| P1 | TTY 被连带切换 | `/resume` 由主循环执行，操作者终端里的会话也被切走，终端与网页窗口被强行绑成同一个会话 |
| P2 | 与进行中的 turn 竞态 | 忙碌时 `/resume` 排在输入队列 FIFO 之后：切换被推迟，且**排队输入仍在旧会话执行**（`sessions.js` 已用文案提示，但语义依然反直觉） |
| P3 | 完成时机靠轮询 | resume 不发布 `session_end/session_start` SSE（它们是 turn 边界事件），前端过去只能 8×300ms 轮询兜底，超时后显示「已切换(状态未同步)」。（**已收敛**：S14 由服务端合成 `session_switched`，见实施计划 §22） |
| P4 | 单会话视图 | 一个进程只有一个 `current_session_id`，无法并排观察两个会话；多窗口打开同一端点时，一个窗口切会话会把所有窗口一起带走 |
| P5 | 跨进程冲突 | 若目标会话已被**另一个进程**（例如另一个工作区的 `aicli resume` 窗口）作为当前会话，in-place 切换等于把同一会话加载进第二个进程，两个进程同时读写同一会话存储（SQLite 单写者 + 状态分叉风险） |
| P6 | 无法跨工作区 | 会话属于某个工作区目录（`sessionmeta.WorkspacePath` / metadata `cwd`），当前 web 端点只列出**本进程 SessionManager 可见**的会话，看不到其它工作区正在运行的会话 |

**v2 归口**：P1–P6 在网格方案 §1.2 被重新归纳为 G1–G6（发现 / 归属 / 调用 / 实时 / 生命周期 / 命名），
并由网格的节点档案、租约、SSE 扇入、GC 逐条承接：

| v1 问题 | 网格归口 | 网格对策 |
| --- | --- | --- |
| P1 TTY 被连带切换 | G2 归属 | 「新窗口打开」升为默认路径（网格 §8.1） |
| P2 与进行中 turn 竞态 | G2 | 忙节点只读复用；写操作需显式 `allow_write`（网格 §5.6） |
| P3 完成时机靠轮询 | G4 实时 | `mesh/events` 扇入取代跨进程轮询（网格 §6） |
| P4 单会话视图 | G1 发现 | 节点档案 + peers 聚合视图（网格 §3.1 / §5.4） |
| P5 跨进程冲突 | G2 | 租约 + `ownership=conflict` + resume 拒绝（网格 §4.4） |
| P6 无法跨工作区 | G1 发现 | peers 默认全量 + workspace join（网格 §4.2 / §5.4） |

### 1.3 目标（v2 表述）

在网格地基（可见性 + 归属 + 调用 + 实时）之上，给会话切换增加**「在新窗口打开」**这条默认路径：

1. 目标会话由**它自己的节点**承载（复用已有节点，或由 `mesh/spawn` 拉起新节点），
   当前进程、终端、既有 SSE 订阅完全不受影响。
2. 侧栏以**节点视角**展示「谁在跑、跑哪个会话、在哪个工作区、地址是什么、是否忙碌」，
   数据来自网格聚合视图（`/web/api/mesh/peers`），节点全死时仍能显示「上次地址」（来自 bindings）。
3. 「会话被别处占用」从**隐患**变成**可见事实**（`ownership=peer/conflict`），
   由 `resume` 拒绝 + 显式接管（P2）兜底，杜绝双写。

> v1 目标里的「建立一份工作区会话状态注册表」已归口网格方案（§2/§3）；
> 本文不再自建任何存储，也不再定义 `~/.aicli/session-endpoints/`（该目录**从未落地且已作废**，网格 §2.4）。

---

## 2. 现状调研：活动会话信息是怎么保存的

> 本节是 v1 的事实清单，**继续有效**（网格方案 §1.1 引用同一批证据）。
> 唯一变化：`~/.aicli/web-ports/` 与 `chat_web_port_store.go` 的处置从「兼容保留」改为**删除**
> （网格 §2.4：不做双读双写），其语义迁移到 `mesh/bindings/`（持久偏好）+ `mesh/nodes/`（活体事实）。

### 2.1 结论速览

| 信息 | 现状载体 | 是否持久化 | 缺口（→ v2 归口） |
| --- | --- | --- | --- |
| 当前活动会话 | 进程内存：`ChatSession.RuntimeSession`，经 `currentRuntimeSessionID()`（`chat_session.go:1513`）按请求实时导出为 `/web/api/sessions` 的 `current_session_id` | ❌ 仅内存 | 无「哪个进程正在服务哪个会话」的磁盘记录 → 网格节点档案 `session` 段（网格 §3.1） |
| 会话列表/历史 | SQLite 会话存储（runtime session store）+ `listResumeCandidateChatSessions` | ✅ | 只有「存在」，没有「正在运行/运行在哪」→ peers 视图 join（网格 §4.2） |
| 会话 ↔ 端口 | `~/.aicli/web-ports/<session-id>.json` = `{session_id, port, host, updated_at}`（`chat_web_port_store.go:26-31`） | ✅ | 无 pid、无 token、无工作区、无存活判定；**按会话为键**，切换会话后旧记录变成脏数据 → **删除**，改 `mesh/bindings/<sid>.json`（网格 §3.2） |
| 会话 ↔ 工作区 | `sessionmeta.WorkspacePath`（`sessionmeta/sessionmeta.go:24`）、metadata `cwd`；`~/.aicli/workspace_directories.yaml`（`workspaceregistry`） | ✅ | 未与 web 端点关联 → 节点档案 `workspace` 段 + 只读 join（网格 §3.1 / §4.2） |
| Web 写令牌 | 进程内存（`web_auth.go`）：`--web-token` > `AICLI_WEB_TOKEN` > 每进程随机；页面注入 meta + sessionStorage；`GET /web/api/token` 只读回显 | ❌ 仅内存 | 跨进程打开窗口时拿不到对方 token（随机令牌重启即轮换）→ 令牌落节点档案（0600），默认脱敏（网格 §9.1） |
| 端点存活 | 无记录 | — | 端口档案可能是死进程留下的，无法区分 → 三层存活判定（网格 §3.5 / §4.3） |

### 2.2 现有机制的细节（证据）

1. **端口档案写入时机**：会话成为活动会话（新建/恢复）且当前进程已启动 loopback 服务器时，
   `persistChatWebPortForSession()` 写入（`chat_session.go:195-199`、`:280-284`；`chat_web_port_store.go:181-187`）。
   进程未启动服务器时静默跳过（不会写脏记录）。
2. **端口档案读取时机**：`aicli resume <id> --pprof/--debug` 且未显式指定端口时，`pprof_port_reuse.go:14-22`
   用它做「会话粘性端口」，避免每次 resume 换端口导致 URL 失效。
   → **v2**：同一行为改读 `mesh/bindings/<sid>.json` 的 `preferred.port`（网格 §3.2 / §11.4 S3）。
3. **令牌注入链路**：`web_page.go` 在 `</head>` 前注入 `<meta name="aicli-web-token">` +
   fetch 包装脚本（回环模式只给写方法加 `X-AICLI-Token`；非回环模式所有方法都加）；
   SSE 无法设请求头，由 `sse.js` 在 URL 上带 `?token=`。
   → **v2 追加**：`mesh/events` 作为第二条 SSE，同样只能走 `?token=`（§5.6）。
4. **鉴权矩阵**（`web_auth.go`）：
   - 回环模式（默认 `127.0.0.1`）：Host 必须回环 + Origin 同源 + 写方法需令牌（`--web-dev` 可跳过）；
   - 非回环模式（`--web-host 0.0.0.0`）：放宽 Host/Origin，**所有**请求（含 GET/SSE）需令牌。
   → **v2 追加**：`mesh/*` 端点另有更严的矩阵（网格 §5.8），跨机写路径默认拒绝。
5. **可用的探活素材**：`GET /web/api/statusbar` 响应已含 `session_id`、`directory`、`project`（`web_statusbar.go:64-83`）；
   `GET /web/api/token` 可读令牌与来源（回环 + 同源）。
   → **v2**：网格探活统一改用 `GET /web/api/health`（更轻、不碰渲染器，网格 §4.3）。
6. **没有的东西**：`commands` 目录下不存在任何 pid/lock 文件机制（已检索 `*pid*` / `*lock*`），
   也没有「会话是否已在别处运行」的校验——`HandleChatWebAPISessionsResume`（`web_handlers.go:1029-1139`）
   只校验会话存在与当前会话相等。
   → **v2**：由 `mesh/leases/` + `ownership` 判定补齐（网格 §3.3 / §4.4）。

### 2.3 差距归纳

- **写**：只有「会话→端口」的静态快照，缺少进程身份（pid/启动时间）、令牌、工作区、状态机。
  → 节点档案（`nodes/`）承接。
- **读**：没有任何端点聚合读取入口（`/web/api/sessions` 只列本进程 SessionManager 的会话）。
  → `/web/api/mesh/peers` + `internal/mesh/view` 承接（Web 侧只做便捷字段映射，§6.2）。
- **清理**：无 GC/对账，死进程记录永久残留。→ `aicli-mesh gc`（默认 dry-run）承接。
- **关联**：会话、端点、工作区三者互不引用，无法回答「会话 X 现在跑在哪个端点上」。
  → 档案 + 绑定 + workspace join 三者同时出现在 peers 视图里。

**命名归位**：以上四条正是网格 G1–G6 的具体表现，不再由本文单独维护一份「差距清单」。

---

## 3. 需求分析

### 3.1 角色与场景

| 场景 | 描述 | 期望行为 | v2 归口 |
| --- | --- | --- | --- |
| S1 | 当前工作区的历史会话，未在运行 | 「在新窗口打开」→ 拉起该会话自己的节点（工作区 cwd）→ 打开新窗口；当前进程零打扰 | 网格 `mesh/spawn`（§5.7）+ 本文 §7 |
| S2 | 目标会话已在某节点运行（本工作区或别的窗口） | 直接打开**那个节点**的 URL，不新起进程、不切换当前进程 | 网格 peers + 本文 §7.1 |
| S3 | 目标会话属于另一个工作区 | 侧栏按工作区分组展示（含地址与状态徽标）；新窗口打开走 S1/S2 逻辑 | 本文 §5.3（数据源 peers） |
| S4 | 节点进程已死 | 记录判为 `stale`，显示「已停止 · 上次 @地址」+「拉起并打开」，绝不打开死链接 | 网格 GC/存活判定 + 本文 §5.1 |
| S5 | 非回环端点（`--web-host 0.0.0.0`） | 打开的 URL 必须带 `?token=`；回环端点可省略 | 网格 §9.4 + 本文 §5.5 |
| S6 | 用户只想让 TTY 与网页保持同一会话 | 保留 in-place 切换（现有行为），但降级为显式选项，且冲突时必须提示 | 本文 §5.7 |

### 3.2 功能需求（FR）

v1 的 FR 编号保留，逐条标注归口（**网格**=由网格方案定义/实现，**本文**=由本文定义/实现）：

| ID | 需求 | 归口 | 优先级 |
| --- | --- | --- | --- |
| FR1 | 会话列表项携带端点信息（节点 ID / 地址 / 状态 / 工作区 / 忙碌 / 心跳 / 上次地址） | 网格 peers（§5.4）+ 本文 §6.2 便捷字段 | P0 |
| FR2 | 提供「在新窗口打开」动作（默认对非当前会话），与「在当前进程切换」并列 | 本文 §5.1 / §5.2 | P0（只读复用）/ P1（默认+spawn） |
| FR3 | `POST /web/api/mesh/spawn`：解析目标会话节点；已运行→`reused`；未运行→拉起并等待就绪；失败→可读原因 | **网格** §5.7（本文只定义前端消费） | P1 |
| FR4 | 拉起进程使用当前 aicli 可执行文件 + `resume <id> --pprof --web-port <端口> --web-token <token>`，cwd = 会话工作区 | **网格** §5.7（本文不定义命令行） | P1 |
| FR5 | 节点档案写入：监听成功后建档；会话切换更新 `session` 段；30s 心跳；退出清理 | **网格** §4.1 | P0 |
| FR6 | 网格聚合读取：`GET /web/api/mesh/peers`（含 stale 标记、工作区分组、上次地址） | **网格** §5.4（本文 §6.2 只做便捷字段） | P0 |
| FR7 | 存活判定：pid + 心跳 TTL + 按需 HTTP 探活（`GET /web/api/health`） | **网格** §3.5 / §4.3 | P0 |
| FR8 | 清理闭环：`aicli-mesh gc`（默认 dry-run）+ 启动对账 | **网格** §4.5 | P0 |
| FR9 | 侧栏跨工作区分组 + 状态徽标（运行中 / 忙碌 / 已停止 / 仅历史 / 冲突 / 无响应） | 本文 §5.1 / §5.3 | P1（原 P2 提前，见 §9） |
| FR10 | `?session=<id>` 深链：窗口绑定会话；不一致时给出显式动作而非静默 | 本文 §5.4 | P1 |
| FR11 | in-place resume 增加「目标已在别处运行」检测，返回 `running_elsewhere` + 节点信息 | 本文 §6.3（服务端判定复用网格 `view`） | P1 |
| FR12 | 令牌存储与暴露规则（档案 0600、默认脱敏、绝不入日志/journal/绑定） | **网格** §9.1 + 本文 §5.5（前端不缓存） | P0 |

### 3.3 非功能需求（NFR）

| ID | 需求 | 归口 / v2 变化 |
| --- | --- | --- |
| NFR1 | 并发写安全：多进程各自只写自己的档案与日志（原子 rename），无跨进程锁 | 网格 §3.1 / §4.1「写者唯一」 |
| NFR2 | 安全边界不扩大：网格 = 本机同用户信任域；非回环暴露仍受令牌保护；mesh 写路径默认拒绝跨机 | 网格 §9（本文 §5.5 补前端三条红线） |
| NFR3 | ~~向后兼容：`~/.aicli/web-ports/<id>.json` 继续可读~~ **作废** | **改为「明确不兼容」**：旧目录不再读写，`aicli-mesh gc --purge-legacy` 一次性清理（网格 §2.4）；升级代价 = 首次 resume 可能换端口 |
| NFR4 | 跨平台：pid 存活判定、脱离式进程启动、文件权限在 Windows/POSIX 均可用 | 网格 §9.6（Windows 权限语义说明） |
| NFR5 | 可观测：网格读写失败 best-effort 不影响会话主流程；`/debug` 页面增补说明 | 网格 §4.7 降级矩阵 |
| NFR6 | 无新依赖：仅用 Go 标准库 + 现有前端（无构建步骤） | 网格 §0 红线 4；本文补「前端 vanilla JS，无打包器」 |
| NFR7 | 降级可用：网格不可用时 fail-closed（不显示臆造节点），主流程不受影响 | 网格 §2.3 路径解析 + §4.7 |

---

## 4. 总体设计

### 4.1 一张图

```
  ~/.aicli/mesh/                                   # 网格根（网格方案 §2.2）
    nodes/node-8124-…json      -> 节点 A：W1，会话 S1，127.0.0.1:55124，令牌（0600）
    nodes/node-9001-…json      -> 节点 B：W2，会话 S2，127.0.0.1:55130，令牌（0600）
    bindings/session_….json    -> 会话 S2 ↔ 上次地址 127.0.0.1:55130（无秘密；节点死了仍在）
    leases/session-….lock      -> 会话所有权（TTL 90s，心跳续约）
    journal/node-8124-….ndjson -> 事件日志（审计 / 崩溃对账 / 离线复盘）

      浏览器窗口 A（只连自己的节点）             aicli-mesh（CLI：无节点存活时也能工作）
            |                                          |
            v                                          v
  +----------------------------+   GET  /web/api/mesh/peers   （聚合 + 归属 + 上次地址）
  | 节点 A  :55124 /web/       |   GET  /web/api/mesh/events  （SSE 扇入：peer 状态）
  | 浏览器窗口 A               |   POST /web/api/mesh/spawn   （新窗口打开：复用或拉起）
  +-------------+--------------+   POST /web/api/mesh/call    （CLI / Agent 面，前端不用）
                |  SSE 扇入（回环）
                v
  +----------------------------+
  | 节点 B  :55130 /web/       |   ← 新窗口打开：复用 B 的地址；B 不存在则按需拉起节点 C
  | 浏览器窗口 B               |
  +----------------------------+

  中文注记：节点 A/B 分属工作区 W1/W2，各自把自己的「身份 + 地址 + 令牌 + 当前会话 + 心跳」
  写成一份档案；任一节点的 Web 界面都能读到全部档案（经自己的进程聚合），
  从而打开 S2 自己的节点（而不是把 A 切到 S2）。浏览器永远不直连 peer。
```

### 4.2 三种切换语义（并存）

网格方案 §8.1 定义了三种语义，本文只规定它们的**前端触发与呈现**：

| 语义 | 前端触发 | 结果 | 冲突处理 | 阶段 |
| --- | --- | --- | --- | --- |
| **本进程切换**（既有 in-place） | 条目二级动作「在当前进程切换」+ 确认弹窗 | 本节点 `/resume` 注入 | `ownership=peer` → 拒绝并提示（§5.7）；`force` / `takeover` 显式放行 | P1 |
| **新窗口打开**（新增，默认） | 条目主点击 / 「在新窗口打开」按钮 | 复用已有节点地址，或 `mesh/spawn` 拉起新节点 | 已有活节点 → 直接复用；否则抢 `spawn-<sid>` 租约拉起 | P0（仅复用）/ P1（含拉起） |
| **接管**（显式，P2） | 冲突弹窗「接管该会话」二次确认 | 所有权转移到本节点，旧节点档案标 `orphaned` 并提示 | 需二次确认；不杀进程（网格 §4.4） | P2 |

> 默认策略：会话列表主点击 = 新窗口打开；目标会话 == 本节点当前会话时，退化为「聚焦/刷新本窗口」。
> 本进程切换始终保留在二级动作中（S6 场景：TTY 与网页需要同步时使用）。

### 4.3 设计原则（Web 侧五条）

1. **只消费，不定义**：前端不读文件系统、不拼 `mesh/` 路径、不解析档案；一切经本节点 HTTP API。
2. **单信任边界**：浏览器只连自己的节点（网格 §6.5）；peer 数据由本节点扇入 / 代理，令牌不进浏览器历史。
3. **只增不改**：`/web/api/sessions`、`/web/api/events` 等既有响应只新增字段，不改既有字段语义（回归门禁）。
4. **失败可见、降级可用**：网格不可用时回到「只有 in-place + 无徽标」的现状，不报错、不臆造节点。
5. **不引入前端构建**：vanilla JS + 现有注入方式（无打包器、无新依赖）。

---

## 5. 前端交互设计

> 本节是 v1 §8 的升级版：交互形态不变（徽标 / 分组 / 深链 / 预开窗口），
> **数据源与状态词汇全部换成网格的**（`peers.nodes[]`、`state` × `ownership` × `reachability`）。

### 5.1 侧栏会话条目

```
  ● 优化会话切换                    12 条消息 · 09-24 07:29
    E:/projects/ai/ai-agent-runtime · 运行中 @127.0.0.1:55124 · node-8124-…
    [ 在新窗口打开 ]  [ 在当前进程切换 ]  重命名  删除
```

**状态徽标（三维组合，优先级从上到下）**

| 条件（网格词汇） | 徽标 | 主点击 | 二级动作 |
| --- | --- | --- | --- |
| `ownership=conflict` | `⚠ 冲突（N 个节点）` | 展开冲突详情（节点列表 + 心跳时间） | 禁用 in-place，提示 `aicli-mesh doctor` |
| `liveness.state=stale` 或 `reachability=unreachable` | `○ 已停止 · 上次 @127.0.0.1:55124` | 「拉起并打开」（spawn） | 复制诊断命令 |
| `session_state=busy`（`ownership=peer/owner`） | `◐ 忙碌` | 新窗口打开（只读复用，不打扰 turn） | 在当前进程切换（会排队，文案提示） |
| `session_state=running` + `ownership=owner` | `● 运行中（本窗口）` | 聚焦/刷新本窗口 | 重命名 / 删除 |
| `session_state=running` + `ownership=peer` | `● 运行中 @127.0.0.1:55130` | 新窗口打开（复用对方节点） | 在当前进程切换（提示占用）/ 接管（P2） |
| `session_state=idle`（有绑定） | `— 仅历史 · 上次 @127.0.0.1:55124` | 新窗口打开（二次确认 → spawn） | 在当前进程切换 |
| `session_state=idle`（无绑定） | `— 仅历史` | 同上 | 同上 |
| `session_state=unknown` | `? 状态未知` | 禁用（提示 `aicli-mesh show <session>`） | 在当前进程切换（若会话可 resume） |

- 端点信息行显示 `host:port` + 工作区名（跨工作区时高亮工作区名）+ 节点 ID 短后缀（排障用，可折叠）。
- 「仅历史」的 `上次 @地址` 来自 `mesh/bindings/`（节点全死也能显示，网格 §4.2）。
- 主点击行为由「打开方式」开关决定（`localStorage: webSessionOpenMode = new_window | in_place`）；
  **默认值随阶段推进**：P0 阶段默认 `in_place`（此时还没有 spawn 能力），P1 起默认 `new_window`
  （见 §9 与 §11 Q13；迁移只在开关未被用户显式设置时生效）。
- 「在当前进程切换」保留确认弹窗；当会话在别处运行时，弹窗追加警示文案与「打开那个窗口」按钮（§5.7）。
- 徽标**不只靠颜色**：形状 + 文本双编码（无障碍要求，§11 Q12）。

### 5.2 打开新窗口的弹窗拦截规避（必须）

`window.open` 在异步回调里调用会被浏览器拦截。做法（数据源换成 `mesh/spawn`）：

```js
// 1) 用户手势内同步预开空白窗口（保住 popup 资格）
var win = window.open("about:blank", "_blank");
// 2) 再请求本节点的 mesh/spawn（服务端负责：复用已有节点 or 拉起新节点）
fetch("/web/api/mesh/spawn", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ session_id: id, port: 0, wait_ms: 8000, origin: "web" })
}).then(r => r.json()).then(json => {
  if (json.status === "reused" || json.status === "started") {
    win.location.replace(json.url);          // url 由服务端内联令牌（§5.5）
  } else if (json.status === "starting") {   // 兼容分支：当前实现不返回（§7.4）
    showStarting(win); pollUntilReady(id, win);
  } else {                                    // not_running / failed
    win.close(); showToast(json.reason, "error"); showDiagnostics(json);
  }
}).catch(err => { win.close(); showToast(String(err), "error"); });
```

- `starting` 阶段窗口先显示本地「正在启动会话节点…」占位页（`srcdoc` 或 `document.write`）。
- 超时（默认 `wait_ms=8000`）→ 关闭占位窗口 + Toast 报错 + 提供「重试」与「复制诊断命令」
  （`aicli-mesh show <session>` / `aicli-mesh doctor`）。
- **回退路径**：若 `mesh/spawn` 返回 `refused`（如 `--mesh-allow-spawn=false`），
  提示改用 `aicli-mesh open <session> --print-url`（CLI 面），不在前端重试。
  （**已落地**：S13，`sessions.js::SPAWN_CODE_TEXT` + `spawnFailureText(json, id)`；
  拉起失败态另附 `aicli-mesh show <session>` 诊断命令。）

### 5.3 跨工作区分组

侧栏新增可折叠分组「其他工作区（N）」，数据来自 `GET /web/api/mesh/peers` 的 `workspaces[]` + `nodes[]`：

> **默认全量**：数据源默认 `scope=all`（网格 §5.4），**跨工作区节点默认就在侧栏里**；分组只是呈现方式，
> 不是可见性边界（网格 §4.2 规则 5）。「其他工作区」= 非本进程工作区的节点；缺 `workspace` 的归入 `(无工作区)`。

```
▼ 其他工作区
   ▸ ai-agent-runtime-web  · 1 个运行中
       ● 会话标题A  session_…  @127.0.0.1:55130  [打开]
   ▸ frontend-demo        · 0 个运行中（仅历史 3，上次 @127.0.0.1:55124）
```

- 节点默认渲染 `live`/`stale`；`unknown`（档案不可解析）折叠在分组底部（诊断用途）。仅历史会话仍在主列表（带工作区标签）。
- `stale` / `unreachable` 条目禁用「打开」，改为「拉起并打开」（触发 `mesh/spawn`）。
- 分组排序：本工作区优先 → 有 `live` 节点的工作区 → 其余按 `heartbeat_at` 倒序（与网格 §4.2 排序规则一致）。
- 工作区名缺失时退化为路径末段；路径不可读时显示原样路径（不做猜测）。

### 5.4 `?session=<id>` 深链与冲突横幅

加载后以 `GET /web/api/mesh/self` + `peers` 判定（**不轮询**）：

| 情况 | 横幅 | 动作 |
| --- | --- | --- |
| `?session` == 本节点 `session.id` | 无横幅，高亮对应条目 | — |
| `?session` 有活节点（`ownership=peer`） | 「此窗口未绑定该会话；该会话正在 `<workspace>` 的窗口运行」 | `[切换到那个窗口]`（复用对方 URL，不新建）/ `[在本进程切换]`（走 §5.7 冲突流程）/ `[忽略]` |
| `?session` 无活节点 | 「此窗口未绑定该会话」 | `[在本进程 resume]` / `[新窗口打开]` / `[忽略]` |
| 本节点会话 `ownership=conflict` | 「会话归属冲突：N 个节点声称服务同一会话」 | `[查看节点]`（列出 node_id/pid/心跳）/ `[复制诊断命令]` |

- 用途：spawn 出来的窗口 URL 自带 `?session=`，正常情况下 `session.id` 已一致，不产生歧义（§7.3）。
- 冲突横幅是**只读**的：不在 Web 端提供「强制接管」按钮的默认入口（接管需二次确认，P2，§5.7）。

### 5.5 令牌处理（三条红线）

1. **浏览器只持有本节点令牌**：页面注入的 meta/`sessionStorage` 机制不变；peer 令牌**不**写入前端任何持久存储。
2. **跨节点 URL 的令牌由服务端内联**：`mesh/spawn` 响应里的 `url` 已带 `?token=`（服务端读目标档案后拼装），
   前端只做 `win.location.replace(url)`，不解析、不显示、不记录该令牌。
3. **默认视图全部脱敏**：`peers` / `self` 默认 `redact_token=1`（`0f3a…`）；
   只有 `?reveal_token=1` 且回环同源才回原文（网格 §5.3）。
   `aicli-mesh url --with-token` 是人工取原文令牌的正规途径。

「关于」页签新增「网格」小节：本节点 `node_id`、`mesh.root` 路径、令牌提示（脱敏）、
`counts`（live/stale/conflict）、建议命令（`aicli-mesh ls --probe`、`aicli-mesh gc`）——**只读展示**。

### 5.6 实时刷新（`mesh/events` 扇入，取代跨进程轮询）

订阅 `GET /web/api/mesh/events`（与既有 `/web/api/events` **并列的第二条 SSE**，语义互不干扰）：

| 帧类型 | 前端反应 |
| --- | --- |
| `mesh.peer.joined` / `mesh.peer.left` | 侧栏节点/工作区分组增量增删；「其他工作区」计数更新 |
| `mesh.peer.updated` | 对应条目徽标刷新（busy / session / state） |
| `mesh.session.changed` | 会话归属变化（owner↔peer）、冲突出现/消解 → 刷新对应条目 + 横幅 |
| `mesh.peer.event`（白名单 `turn.*` / `session.*`） | 忙碌徽标翻转；不处理逐字 delta（网格 §6.3 扇入白名单） |
| `mesh.lagged` | 事件积压跳号 → 丢弃增量，重新拉一次 `peers` 全量（网格 §6.4） |

- **断线退避**：1s→2s→4s…上限 30s；重连带 `?since_seq=<last_seq>` 续传。
- **降级**：SSE 不可用（旧节点 / 严格模式无令牌 / 代理阻断）时降级为 10s 轮询 `peers`；
  轮询期间徽标仍可用（只是不实时）。
- **节流**：事件驱动 + 200ms 合并刷新，避免高频事件导致侧栏重排（§11 Q11）。
- 既有 `/web/api/events` 订阅**保持不变**（本进程事件）；本进程会话切换的事件化已由 S14 落地
  （服务端合成 `session_switched`，见实施计划 §22），与网格 `mesh.session.changed` 不冲突：
  前者是「本进程内部会话切换」，后者是「跨节点归属变化」。

### 5.7 本进程切换（in-place）的冲突提示

`POST /web/api/sessions/resume` 行为增强（契约见 §6.3），前端呈现：

```
┌─ 切换到「优化会话切换」？ ─────────────────────────────┐
│ 该会话正在 ai-agent-runtime-web 的窗口中运行（node-9001-…）  │
│ 在本进程切换会把它加载进第二个进程，可能造成状态分叉。        │
│                                                        │
│ [ 打开那个窗口（推荐） ]  [ 仍在本进程切换 ]  [ 取消 ]      │
└────────────────────────────────────────────────────────┘
```

- `status=running_elsewhere` → 上述三段式弹窗；「打开那个窗口」= 复用对方 URL（不新建进程）。
- 「仍在本进程切换」→ 二次请求带 `{"force": true}`，保留旧行为（回归测试锁定）。
- `ownership=conflict` → **禁用** in-place，提示先 `aicli-mesh doctor`（冲突需人工判断）。
- P2「接管」：`aicli-mesh open --takeover` 或 Web 端二次确认后走租约回收（网格 §4.4）；
  旧节点收到提示后在其 TUI/Web 顶部显示「会话已被节点 B 接管」（`orphaned`）。

### 5.8 关于 / 诊断页

- 展示：本节点档案摘要（`node_id`/`pid`/`endpoint`/`session`/`workspace`）、
  网格计数（`counts.live/stale/conflict`）、`mesh.root`、`journal` 路径、建议命令。
- **不提供** gc / stop / spawn-force 等治理按钮：治理动作留在 CLI（`aicli-mesh`），避免误点（§8 R13）。
- 诊断信息可一键复制为文本块（便于贴到 issue），内容默认脱敏（不含令牌原文）。

---

## 6. Web 侧接口契约

> 本节只写「前端会用到的字段」与「Web 侧新增/增强的端点」。
> 网格端点本身的完整定义在网格方案 §5，本文不复制（避免两处漂移）。

### 6.1 前端依赖的网格端点（子集）

| 端点 | 前端用途 | 关键参数 | 阶段 |
| --- | --- | --- | --- |
| `GET /web/api/health` | 前端**不用**（spawn 内部就绪等待、`doctor` 用） | — | P0 |
| `GET /web/api/mesh/self` | 本节点摘要、`mesh.root`、深链判定基准 | `reveal_token=1`（仅回环同源） | P0 |
| `GET /web/api/mesh/peers` | 侧栏节点/工作区/状态/上次地址（**主数据源**） | `probe=1`、`scope=self\|all`（默认 `all`）、`workspace=`（过滤）、`state=all\|live`（默认 `all`）、`redact_token=1`(默认) | P0 |
| `GET /web/api/mesh/events` | 实时增量（第二条 SSE） | `since_seq=`、`peers=auto\|none` | P1 |
| `POST /web/api/mesh/spawn` | 新窗口打开（复用或拉起） | `session_id`、`port`、`wait_ms`、`origin=web` | P1 |
| `POST /web/api/mesh/call` | **前端不用**（CLI / Agent 面） | — | P1 |
| `POST /web/api/mesh/stop` | **前端不提供按钮**（治理留 CLI） | — | P2 |

**边界原则**：前端只用 `self` / `peers` / `events` / `spawn` 四个；
`call` / `stop` / `gc` / `doctor` 属于 CLI/Agent 面，Web 端最多提供「复制命令」入口。

### 6.2 `GET /web/api/sessions` 扩展（便捷视图，与 peers 同源）

在现有 `{sessions, current_session_id}` 基础上**只增字段**：

```json
{
  "sessions": [
    { "id": "session_20260924072950_ltYRU9tG", "title": "优化会话切换", "message_count": 12,
      "created_at": "…", "updated_at": "…", "current": true,
      "workspace_path": "E:/projects/ai/ai-agent-runtime",
      "workspace_name": "ai-agent-runtime",
      "session_state": "running",
      "ownership": "owner",
      "endpoint": {
        "node_id": "node-8124-20260924T073012Z",
        "base_url": "http://127.0.0.1:55124",
        "web_url": "http://127.0.0.1:55124/web",
        "loopback": true, "auth_required": false,
        "reachability": "ok", "busy": false,
        "heartbeat_at": "2026-09-24T07:31:40Z"
      },
      "last_known": { "host": "127.0.0.1", "port": 55124, "from": "binding" } }
  ],
  "current_session_id": "session_20260924072950_ltYRU9tG",
  "self": { "node_id": "node-8124-20260924T073012Z", "mesh_root": "C:\\Users\\me\\.aicli\\mesh",
            "counts": { "live": 2, "stale": 1, "conflict": 0 } },
  "workspaces": [ { "path": "E:/projects/ai/ai-agent-runtime", "name": "ai-agent-runtime",
                    "session_count": 7, "running_count": 1 } ]
}
```

字段约定（**与网格词汇对齐，不另起一套**）：

| 字段 | 说明 |
| --- | --- |
| `session_state` | `running` / `busy` / `idle` / `unknown`（网格 §3.5 的会话维度；命名避免与节点 `state` 混淆） |
| `ownership` | `owner` / `peer` / `conflict` / `none`（网格 §3.5 归属维度） |
| `endpoint` | `null` = 无活节点；非空时字段与 `peers.nodes[].endpoint` 同源同义 |
| `endpoint.reachability` | `ok` / `unreachable` / `skipped`（仅 `?probe=1` 时刷新，默认 `skipped`） |
| `last_known` | 来自 `mesh/bindings/` 的「上次地址」，节点已死时仍可展示；可为 `null` |
| `self.mesh_root` | 网格根路径（排障/诊断页用，对齐网格 §5.3 的自描述传统） |
| `workspaces[]` | 工作区汇总（来自 peers 的 `workspaces`），用于侧栏分组计数 |

**明确不做**：

- ❌ 不在 `sessions` 响应里返回令牌原文（v1 曾计划内联 `token`；改为只在 `mesh/spawn` 的 `url` 里内联，§5.5）。
- ❌ 不使用 v1 的自定义字段名（`instance_id`、`url`、`state`）——统一为 `node_id`、`base_url`/`web_url`、`session_state`。
- ❌ 不新增 `GET /web/api/endpoints`（v1 设计）：跨工作区清单由 `mesh/peers` 承担，避免两套聚合。

**兼容性**：默认仍只列本进程 SessionManager 可见的会话（现状兼容）；
`?scope=all` 时并入 peers 发现的跨工作区会话（按 `session_id` 去重，标注 `ownership=peer`）。

### 6.3 `POST /web/api/sessions/resume` 行为增强（向后兼容）

请求：`{ "session_id": "…", "force": false }`

| 情况 | 响应 | 前端行为 |
| --- | --- | --- |
| 目标 == 本节点当前会话 | `{"status":"already_current"}`（既有语义） | 关闭弹窗 |
| 目标被**其它活节点**占用 | `{"status":"running_elsewhere","node_id":"…","endpoint":{…},"takeover_available":true}` | §5.7 三段式弹窗；**不注入** `/resume` |
| 目标被多节点声明（`conflict`） | `{"status":"conflict","nodes":[…]}` | 禁用 in-place，提示 `aicli-mesh doctor` |
| 其余（含 `force=true`） | 既有 `queued` / `not_found` 语义不变 | 现状行为 |

- `running_elsewhere` 的判定**复用 `internal/mesh/view` 的归属结果**（不新写判定逻辑）；
- `force=true` 时保留旧行为（用户知情下的强制 in-place），回归测试锁定；
- P2 追加 `{"takeover": true}`：走租约回收（网格 §4.4），响应 `{"status":"taken_over"}`。

### 6.4 前端不直连 peer（单连接原则）

| 方案 | 结论 |
| --- | --- |
| 浏览器直连 N 个节点 | ❌ N 份令牌进浏览器、N 套 CORS/Host 校验、N 条断线重连（网格 §6.5） |
| **浏览器只连本节点，peer 由节点扇入** | ✅ 采用：令牌只在回环进程之间流动；跨进程策略集中一处可审计 |

推论：前端**不实现**「跨节点 fetch」；所有跨节点动作 = 调用本节点的一个端点（`spawn` / `resume`）。

### 6.5 鉴权矩阵（Web 侧消费视角）

网格端点自身的矩阵见网格方案 §5.8；前端只需记住三条：

1. 前端所有请求都发往**本节点**，因此永远满足「回环 + 同源」条件；
2. 回环开发模式下 `peers`/`self`/`events` 免令牌（令牌默认脱敏）；
   回环严格模式（`--web-token`/`AICLI_WEB_TOKEN`）下需带本节点令牌；
3. `mesh/spawn` 需显式进程开关（`--mesh-allow-spawn`，默认开）+ 写令牌（严格模式）；
   非回环一律拒绝（除非 `--mesh-allow-nonloopback`）。

---

## 7. 新窗口打开：复用与拉起

### 7.1 复用已有节点（首选）

- **条件**：peers 中该会话 `ownership=peer`（或 `mesh/spawn` 返回 `reused`）。
- **动作**：`win.location.replace(url)`；**不**触碰任何进程、不改动本节点、不切换本节点会话。
- **副作用**：无——新窗口只是多一个 SSE 订阅者（既有 `web_sse_backpressure_test.go` 验证多连接写路径）。
- **令牌**：`url` 由服务端内联（§5.5）；回环 + 开发模式下可省略。

### 7.2 拉起新节点（由网格承担）

前端**只发一个请求** `POST /web/api/mesh/spawn {session_id, port:0, wait_ms:8000, origin:"web"}`，
其余全部由网格负责（本文不重复定义）：

| 环节 | 定义位置 |
| --- | --- |
| 单飞锁（`leases/spawn-<sid>.lock`）+ 锁内二次检查 | 网格 §5.7 / §8.2 |
| 命令行（`aicli resume <sid> --pprof --web-port <port> --web-host 127.0.0.1 --web-token <token>`） | 网格 §5.7 |
| 工作目录（会话工作区）、脱离式启动（Windows `DETACHED_PROCESS` / POSIX `Setsid`） | 网格 §5.7 |
| 日志（`~/.aicli/logs/mesh-spawn-<sid>.log`，失败时回传尾部 20 行） | 网格 §5.7 / §8.2 |
| 就绪判定（轮询 `nodes/` 直到 live 或超时） | 网格 §8.2 |
| 令牌生成与传递（不写进绑定、不写进 journal） | 网格 §9.1 |

> 安全提示（前端不可绕过）：`spawn` 是**高权限动作**，必须用户显式点击（`origin=web`），
> 不允许「自动为所有历史会话预热节点」。

### 7.3 打开后的窗口行为

- 新窗口 URL 形如 `http://127.0.0.1:55130/web?token=<tok>&session=<sid>`
  （**以网格 §8.2 为准**；v1 写的 `/web/?session=…` 形式作废）。
- 页面加载 → `GET /web/api/mesh/self` → `session.id` 应与 `?session` 一致（刚拉起的节点当前会话就是它）。
- 若因竞态不一致（例如用户在别处又切了会话）→ 走 §5.4 横幅，**不静默**。
- 窗口标题/图标可选加节点后缀（多窗口并排时的辨识，P2 打磨）。
  （**已落地**：S13，`sessions.js::meshNodeSuffix()` → `· <工作区> · <节点短 id>`；
  网格不可用（`self` 为空）时不加后缀。）

### 7.4 响应契约与失败态

`POST /web/api/mesh/spawn` 响应（状态集合在网格 §5.7 与 §8.2 已统一）：

| `status` | 含义 | 前端行为 |
| --- | --- | --- |
| `reused` | 已有活节点，直接返回其 URL | `win.location.replace(url)` |
| `started` | 新节点已拉起并就绪（在 `wait_ms` 内） | 同上 |
| `not_running` | 拉起后超时未见节点 | 关窗 + Toast + 「重试」/「复制诊断命令」 |
| `failed` | 进程启动失败（含 `reason` + 日志尾部 20 行） | 关窗 + Toast + 诊断面板 |

> 与 v1 四态的映射：`opened` ≡ `reused|started`；`starting` 中间态**不返回**——由 `wait_ms` 内同步等待吸收
> （网格 §5.7 已同步该状态集合）。前端保留 `starting` 分支作为**向前兼容**：若将来引入长等待（>10s）再启用（§11 Q1）。

---

## 8. 风险与对策

v1 的 R1–R10 保留，逐条标注**归口**（网格承担 / Web 侧承担 / 已消除），并补 3 条 Web 专属新风险：

| # | 风险 | 归口 | 对策 |
| --- | --- | --- | --- |
| R1 | 同一会话被两个节点同时打开（spawn 与 in-place 并发、双窗口并发） | **网格** | 租约互斥（`session-<sid>`）+ `ownership=conflict` 可见 + resume 返回 `running_elsewhere` + spawn 锁内二次检查（网格 §4.4 / §5.7） |
| R2 | 弹窗被浏览器拦截 | **Web 侧** | 用户手势内同步 `window.open("about:blank")` 预开（§5.2） |
| R3 | 令牌落盘 / 泄露 | **网格** + Web 侧 | 档案 0600 + 目录 0700（POSIX）/ 用户 ACL（Windows）；默认脱敏；journal/绑定/日志禁令牌（网格 §9.1）；前端不缓存 peer 令牌（§5.5） |
| R4 | 死进程记录残留 / pid 复用误判 | **网格** | 三层存活（pid + 心跳 TTL + 探活）；pid 存活绝不删；`process.started_at` 防御复用；`gc` 显式（网格 §3.5 / §4.5） |
| R5 | 端口被占用导致 spawn 失败 | **网格** | 绑定偏好优先 → 随机兜底；失败原因回传 UI（网格 §5.7 / §13 Q3） |
| R6 | 网格读写异常影响主流程 | **网格** | 全部 best-effort + 降级矩阵；任何错误只降级为「无网格信息」（网格 §4.7） |
| R7 | 跨平台差异（Windows 无 Setsid / 文件权限位） | **网格** | 平台分支（`chat_actor_process_*.go` 先例）；Windows 权限语义在 `doctor` 中提示（网格 §9.6） |
| R8 | 会话工作区目录不存在（已删除/移动） | **网格** + Web 侧 | spawn 前校验目录，失败返回 `failed` + 原因（网格 §5.7）；前端提供「复制命令，手动在目标目录启动」备选（§7.4） |
| R9 | 节点数多导致列表变慢 | **网格** + Web 侧 | 默认不探活（毫秒级）；排序 + 上限（网格 §4.2）；前端懒渲染 + 200ms 合并刷新（§5.6） |
| R10 | 旧 `web-ports` 与新存储双源不一致 | **已消除** | 网格 §2.4：明确不兼容、不做双读双写、`gc --purge-legacy` 清理；本文不再有「双源降级」逻辑 |
| R11 | 两条 SSE（`/web/api/events` + `mesh/events`）互相拖累 | **Web 侧** | 独立连接与独立退避；`mesh.lagged` → 丢弃增量拉全量；任一断线不影响另一条（§5.6） |
| R12 | 前端把 peer 令牌写进 `localStorage` / 页面源码 | **Web 侧** | 红线：peer 令牌只出现在 `mesh/spawn` 响应的 `url` 中，随即交给浏览器导航；代码评审 + 断言（§10.1） |
| R13 | 用户在 Web 端误点治理动作（停止/清理节点） | **Web 侧** | Web 端不提供 stop/gc/spawn-force 按钮；只提供「复制命令」（§5.8） |

### 8.1 安全边界复述

- 网格 = 本机同用户信任域（与 `~/.aicli` 下其它文件一致），**不**因为新增它而放宽任何 web 鉴权规则（网格 §9）。
- 本文新增的 Web 侧动作只有两个：`POST /web/api/mesh/spawn`（拉起节点）与 `resume` 的冲突前置检查。
  前者需显式开关 + 写令牌；后者只做**拒绝**（不新增权限），是安全性的净增强。
- 浏览器侧红线：不直连 peer、不缓存 peer 令牌、不在 UI 显示令牌原文、不提供治理按钮。

---

## 9. 实施路线图（对齐网格 S1–S10）

本文不单独排序，直接对齐网格 §11.4 的切片；下表只标注 **Web 侧落点**。

| 阶段 | 网格切片 | Web 侧交付 | 验证方式 |
| --- | --- | --- | --- |
| **P0** | S1–S6（`internal/mesh` + 档案/绑定 + `peers` + CLI） | ① 侧栏徽标 + 端点行（读 `sessions.endpoint/ownership`，与 peers 同源）<br>② 「在新窗口打开」**仅复用**已有节点，且仅当 `auth_required=false`（回环开发模式）可用；否则提示用 `aicli-mesh url --with-token`<br>③ 打开方式开关默认 `in_place`（此阶段无 spawn）<br>④ 「关于」页网格小节（只读） | 手工：双进程 `aicli-mesh ls` 与侧栏徽标一致；单测覆盖 sessions 扩展字段 |
| **P1** | S7–S9（events / call / spawn + CLI） | ① 主点击 = 新窗口（`mesh/spawn`）+ 预开窗口三态处理（§5.2）<br>② `mesh/events` 实时徽标 + 退避重连 + 轮询兜底（§5.6）<br>③ `resume` 的 `running_elsewhere` 三段式弹窗（§5.7）<br>④ 跨工作区分组（§5.3）<br>⑤ `?session=` 深链横幅（§5.4）<br>⑥ 开关默认切到 `new_window` | E2E（§10.3）+ 手工验收表 |
| **P2** | S10+（接管 / 工作区收敛开关 / stop / journal 查询） | ① 冲突详情横幅（节点列表 + 心跳）——**✅ S11**<br>② 接管二次确认（`takeover`）——**✅ S15**<br>③ 收敛开关（`--mesh-restrict-workspace`，opt-in）下的 `refused` 文案（`mesh_cross_workspace_denied`）——**✅ S13**<br>④ resume 的 SSE 事件化（去 8×300ms 轮询）——**✅ S14**<br>⑤ 窗口标题加节点后缀——**✅ S13** | E2E-DEBUG-03 + 手工（逐项状态见 §0.1） |

### 9.1 前端文件级实现清单

| 文件 | 改动 |
| --- | --- |
| `commands/web/js/sessions.js` | 徽标渲染、端点信息行、打开流程（预开窗口 + `mesh/spawn`）、打开方式开关、深链/冲突横幅、resume 冲突弹窗 |
| `commands/web/js/mesh.js`（新增） | `mesh/events` 订阅、退避重连、`since_seq` 续传、`mesh.lagged` 全量兜底、200ms 合并刷新 |
| `commands/web/index.html` | 条目模板（徽标/端点行）、「其他工作区」分组容器、横幅 DOM、「关于」页网格小节 |
| `commands/web/css/*`（现有样式文件） | 徽标样式（冲突/无响应/忙碌）、分组折叠样式；**形状+文本双编码** |
| `commands/web_handlers.go` | `sessions` 扩展字段（复用 `internal/mesh/view` 结果，不新写聚合）；`resume` 的 `running_elsewhere` 前置检查 |
| `commands/web_schema.go` | 响应结构体扩展（**只增不改**） |
| `commands/chat_debug_endpoints.go` | `mesh` 分组登记（网格 P0 负责；Web 侧只读消费，确保清单覆盖门禁绿） |
| 测试 | `web_handlers_test.go` 扩展（§10.1）+ 手工验收（§10.3） |

### 9.2 依赖与顺序约束

- P0 的「只读复用」依赖网格 S5（`peers` 可用）；在此之前前端无法拿到任何跨节点信息。
- P1 全部依赖网格 S7（events）+ S9（spawn）；**不要**在 S9 之前实现前端拉起逻辑（会与网格单飞锁重复）。
- 本文所有 Web 改动不得破坏 E2E-DEBUG-01/02（网格 MN5 硬门禁）。

---

## 10. 测试与验收

### 10.1 单元 / HTTP 层测试（Web 侧）

| 用例 | 断言 |
| --- | --- |
| `sessions` 扩展结构 | `endpoint=null`（无活节点）时结构稳定；`last_known` 缺失时字段为 `null` 而非省略 |
| `sessions` 与 `peers` 同源 | 同一 fixture（两份档案 + 一份绑定）下，`sessions[].endpoint` 与 `peers.nodes[].endpoint` 字段一致（防两套聚合漂移） |
| 字段只增不改 | 现有 sessions/resume 用例回归：既有字段名与语义不变 |
| `resume` 冲突前置 | `running_elsewhere` 时**未注入**输入队列（断言队列为空）；`force=true` 时正常注入；`conflict` 时拒绝 |
| `spawn` 参数透传 | 前端请求体 → 网格请求体：`session_id` / `port=0` / `wait_ms` / `origin=web` |
| 令牌脱敏 | `sessions` / `peers` 响应**不含**令牌原文；令牌原文只出现在 `spawn` 响应的 `url` 字段 |
| 降级 | 网格目录不可写 / 无 home：`sessions` 仍返回 200，`endpoint` 全为 `null`，无 5xx |

### 10.2 前端行为断言（手工 + 可选自动化）

| 用例 | 断言 |
| --- | --- |
| 弹窗资格 | 点击「在新窗口打开」后，`window.open` 在用户手势同步阶段被调用（DevTools 无「拦截弹窗」警告） |
| 失败关窗 | `not_running` / `failed` 时占位窗口被关闭，且 Toast 可见、诊断信息可复制 |
| 实时徽标（S12 起可执行，细化见 `web-testing.md` §2.7.2） | peer 忙碌翻转在 ≤2s 内反映到徽标；断网后 10s 轮询兜底仍能刷新 |
| 无令牌残留 | 打开新窗口后，`localStorage` / `sessionStorage` / 当前页面 DOM 中无 peer 令牌 |

> 若仓库后续引入前端自动化（如 Playwright），上表应转为脚本断言；当前保持手工 + DevTools 检查。

### 10.3 E2E 验收（v1 E1–E8 → 网格 M1–M10 映射）

| v1 场景 | Web 侧验收标准 | 网格 E2E 覆盖 |
| --- | --- | --- |
| E1 打开历史会话新窗口 | A 的 `current_session_id` **不变**，TTY 无输出；新窗口正常对话 | M1/M2（发现与同源）+ S9 切片 |
| E2 复用已有节点 | 不新起进程（进程数不变）；直接打开对方 URL；对方不受影响 | M3（`session-lease-exclusive`） |
| E3 跨工作区 | 「其他工作区」分组出现，显示 W2 节点与状态；从 A 打开成功 | M1（`discovery-both-nodes`）+ M10（`cross-workspace-ops`） |
| E4 节点被杀 | 徽标在 TTL 内转「已停止 / 无响应」；「打开」禁用、「拉起并打开」可用 | M6（`crash-reconcile`） |
| E5 非回环端点 | 打开的 URL 带令牌，页面无 401；令牌不出现在其它位置 | M7（`no-token-leak`） |
| E6 忙碌节点 | 徽标显示「忙碌」；打开新窗口不影响进行中的 turn | M5（`realtime-fanin`） |
| E7 网格不可用/降级 | 行为回到现状（in-place + 无徽标），无报错、无 5xx | 网格 §4.7 降级矩阵（手工断网/只读目录） |
| E8 双窗口并发打开同一未运行会话 | 只拉起一个节点；两窗口落到同一 URL | M3 + spawn 单飞（网格 §5.7） |

### 10.4 回归风险清单

- `web_handlers_test.go` 现有 sessions/resume 用例：响应新增字段必须**向后兼容**（只增不改）。
- 既有 `/web/api/events` SSE 语义**不变**；新增 `mesh/events` 是并列第二条流，不得复用同一连接/订阅状态。
- `pprof_port_reuse_test.go` / `loopback_addr_test.go`：由**网格 S3** 改造（粘性端口迁移到 bindings），本文不重复承担。
- 前端资源无构建步骤：新增 JS 必须 vanilla，禁止引入打包器或第三方库（NFR6）。

---

## 11. 开放问题（Web 专属）

v1 §13 的 8 个问题中，6 个已由网格方案 §13 决策（下表标注结论），本文只保留 Web 专属新增项。

| # | 问题 | 结论 / 建议 | 归口 |
| --- | --- | --- | --- |
| Q1 | `mesh/spawn` 是否需要 `starting` 中间态？ | **已决策：不返回**（`wait_ms` 内同步等待，超时即 `not_running`）；网格 §5.7/§8.2 已同步为 `reused\|started\|not_running\|failed`。将来若引入长等待（>10s）再恢复中间态 + 轮询 | 已解决（v2，两侧文档已同步） |
| Q2 | 未运行的会话，点「打开」时是否自动拉起节点？ | **二次确认后拉起**（拉进程属高权限动作，确认文案含工作区与将执行的命令） | 网格 §13 Q2 |
| Q3 | spawn 的端口策略 | **绑定偏好（`bindings.preferred.port`）优先，失败回退随机** | 网格 §13 Q3 |
| Q4 | peer 令牌是否允许被其它工作区读取 | **允许**：工作区不是权限边界（网格 §9.3）；写调用仍需每次 `allow_write`；前端**永不缓存**，URL 内联即用即弃 | 网格 §13 Q4 + 本文 §5.5 |
| Q5 | peers 视图默认是否跨工作区 | **默认 `scope=all`（硬契约）**；`scope=self` / `workspace=` 仅过滤输出，归属判定永远全量 | 网格 §13 Q5 |
| Q6 | Web 端是否提供「停止节点」按钮 | **不提供**（治理动作留 CLI；P2 仅 CLI `stop`，默认关） | 网格 §13 Q6 + 本文 §5.8 |
| Q7 | in-place 切换是否保留 | **保留**（TTY 与网页同步仍是有效诉求），但冲突时必须提示 | 网格 §13 Q7 + 本文 §5.7 |
| Q8 | 接管是否通知旧节点 | **要**：旧节点档案标 `orphaned` + UI 横幅 | 网格 §13 Q8 |
| Q9 | 是否新增「节点列表」独立视图？ | **不新增独立路由**：并入侧栏「其他工作区」+ 「关于」页网格小节，避免第二处信息架构 | 本文 |
| Q10 | `?session=` 与 `?token=` 同时存在时的处理顺序 | 先按既有规则校验令牌（失败即 401），再做会话绑定判定与横幅 | 本文 |
| Q11 | 徽标刷新节流策略 | 事件驱动 + 200ms 合并；`mesh.lagged` / 重连后只做一次全量拉取 | 本文 |
| Q12 | 深色模式与无障碍 | 徽标用**形状 + 文本**双编码（不只用颜色）；对比度按既有主题变量取值 | 本文 |
| Q13 | 「打开方式」开关默认值的切换时点 | P0 默认 `in_place`（无 spawn 能力）→ P1 切到 `new_window`；迁移只在开关**未被用户显式设置**时生效 | 本文 |
| Q14 | 多窗口辨识 | 新窗口标题追加节点后缀（如 `· node-8124`），P2 打磨；不影响 URL 契约 | 本文 |

---

## 附录 A：术语

与网格方案附录 A 对齐（**以网格词汇为准**，本表只补 Web 侧用法）：

| 术语 | 含义 | Web 侧用法 |
| --- | --- | --- |
| 节点（node） | 一个 aicli 进程实例；`node_id = node-<pid>-<yyyyMMddTHHmmssZ>` | 徽标与诊断信息中的「节点 ID」 |
| 节点档案（node record） | 节点自述的活体档案（`mesh/nodes/<node_id>.json`，含令牌 0600） | 只经 `peers`/`self` 读取，前端不直接读文件 |
| 会话绑定（session binding） | 持久的「会话 ↔ 地址偏好」（`mesh/bindings/<sid>.json`，无秘密） | 侧栏「上次 @地址」的来源 |
| 租约（lease） | 带 TTL 的互斥占用：`session-<sid>`（所有权）/ `spawn-<sid>`（拉起单飞） | 冲突判定与「接管」的依据 |
| 网格日志（journal） | 每节点一份 NDJSON 事件日志 | 「关于」页显示路径；`aicli-mesh watch` 复盘 |
| 网格控制面 | 每节点上的 `/web/api/mesh/*` 端点族 | 前端的主数据源（§6.1） |
| `aicli-mesh` 工具 | 网格的第一消费者与运维入口（无节点存活时也可用） | 失败态与诊断页里「建议命令」 |
| 归属（ownership） | `owner` / `peer` / `conflict` / `none` | 徽标与弹窗文案的关键输入 |
| 可达性（reachability） | `ok` / `unreachable` / `skipped`（探活结果，不落盘） | 「无响应」徽标 |
| 上次地址（last known） | 绑定中的 `preferred.host/port` | 「已停止 · 上次 @…」 |
| 单飞（single-flight） | 同一会话同一时刻最多一个 spawn 流程 | 前端不需要实现，只消费结果 |
| 会话粘性端口 | 由绑定 `preferred.port` 表达的偏好（v1 的 `web-ports` 语义） | `resume`/`spawn` 选端口依据 |

---

## 附录 B：本节点 Web 端点清单

**既有端点（本文职责）**

| 方法 | 路径 | 现状 | 本文（v2） |
| --- | --- | --- | --- |
| GET | `/web/` | 页面 | 支持 `?session=` 深链与 `?token=`（P1，§5.4） |
| GET | `/web/api/sessions` | `{sessions, current_session_id}` | + `session_state/ownership/endpoint/last_known/self/workspaces`（§6.2） |
| POST | `/web/api/sessions/resume` | in-place 注入 | + `running_elsewhere` / `conflict` / `force`（§6.3） |
| POST | `/web/api/sessions/new` | in-place 新建 | 不变（新建天然属于本节点） |
| GET | `/web/api/token` | 回显本节点 token | 不变 |
| GET | `/web/api/statusbar` | 状态栏 | 不变（探活统一改用 `/web/api/health`） |
| GET | `/web/api/events` | 本节点事件 SSE | 不变；`mesh/events` 为并列第二条流（§5.6） |

**网格端点（定义在网格方案 §5，前端只消费）**

| 方法 | 路径 | 阶段 | 前端角色 |
| --- | --- | --- | --- |
| GET | `/web/api/health` | P0 | 不用（spawn 内部就绪等待） |
| GET | `/web/api/mesh/self` | P0 | 本节点摘要 / 深链判定基准 |
| GET | `/web/api/mesh/peers` | P0 | **主数据源**（节点、工作区、上次地址、状态） |
| GET | `/web/api/mesh/events` | P1 | 实时增量 |
| POST | `/web/api/mesh/spawn` | P1 | 新窗口打开（复用或拉起） |
| POST | `/web/api/mesh/call` | P1 | 不用（CLI / Agent 面） |
| POST | `/web/api/mesh/stop` | P2 | 不用（Web 端不提供按钮） |

**v1 计划过、v2 作废的端点**

| 方法 | 路径 | 作废原因 |
| --- | --- | --- |
| GET | `/web/api/endpoints` | 与 `mesh/peers` 重复；聚合只保留一处（§6.2「明确不做」） |
| POST | `/web/api/sessions/open` | 由 `mesh/spawn` 取代（网格 §5.7；数据源从端口档案变节点档案） |
| POST | `/web/api/sessions/close` | 由 `aicli-mesh stop`（P2，默认关）取代，且 Web 端不提供治理按钮（§5.8） |

---

## 附录 C：v1 → v2 变更对照

| v1 章节 | v2 去向 | 变化要点 |
| --- | --- | --- |
| 顶部「架构已升级」横幅（占位） | **删除** | v2 正文已全面对齐，占位横幅不再需要 |
| §1 背景与问题 | 本文 §1（保留） | 目标改为「在网格地基上」；新增 P1–P6 → G1–G6 映射表 |
| §2 现状调研 | 本文 §2（保留） | 事实与证据不变；`web-ports` 处置从「兼容保留」改为**删除**；补各条的 v2 归口 |
| §3 需求分析 | 本文 §3（保留） | FR/NFR 表加「归口」列；**NFR3（向后兼容旧目录）作废**，改为明确不兼容 |
| §4 总体设计 | 本文 §4（重写） | 两条路径 → 三种切换语义；架构图改为网格拓扑 |
| §5 数据模型（会话端点注册表） | **删除** | 归口网格 §2/§3：`mesh/nodes`（活体）+ `mesh/bindings`（偏好）+ `leases` + `journal` |
| §6 生命周期与闭环 | **删除** | 归口网格 §4（写入/读取/探活/租约/GC/降级矩阵） |
| §7 后端接口设计 | 拆分为本文 §6.1（网格端点子集）+ §6.2/§6.3（Web 侧契约） | `/web/api/endpoints`、`/sessions/open`、`/sessions/close` 作废；令牌不再内联进 sessions |
| §8 前端交互设计 | 本文 §5（保留并升级） | 数据源 → `peers`；状态词汇 → `state`×`ownership`×`reachability`；新增 §5.6 实时、§5.7 冲突弹窗、§5.8 诊断页 |
| §9 新窗口打开的两种路径 | 本文 §7（精简） | spawn 细节归口网格 §5.7/§8.2；URL 形式对齐为 `/web?token=…&session=…` |
| §10 关键风险与对策 | 本文 §8（保留并扩充） | 加「归口」列；R10（双源不一致）标记**已消除**；补 R11–R13（SSE 双流 / 令牌残留 / 误点治理） |
| §11 实施路线图 | 本文 §9（重写） | 对齐网格 S1–S10；只列 Web 侧落点与依赖约束 |
| §12 测试与验收 | 本文 §10（重写） | 单测改为 Web 侧契约；E1–E8 → 网格 M1–M10 映射；回归清单更新 |
| §13 开放问题 | 本文 §11（收敛） | 6 项归口网格 §13（附结论）；保留/新增 Web 专属 Q1、Q9–Q14 |
| 附录 A 术语 | 本文附录 A（重写） | 按网格词汇重写，补 Web 侧用法列 |
| 附录 B 现有端点清单 | 本文附录 B（重写） | 分三段：既有端点 / 网格端点 / v1 作废端点 |
| —（新增） | 本文 §0 分工 | 明确「网格方案是架构唯一真源」与三条实现纪律 |
| —（新增） | 本文附录 C | 本对照表 |
