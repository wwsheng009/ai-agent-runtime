# aicli-mesh 多进程网格 · 实施方案（2026-09-24）

> **定位**：`aicli-mesh-architecture.md`（下称「网格方案」）的**施工执行文档**。
> 设计契约（数据模型 / API / 开关 / 验收编号）以网格方案为准；Web 侧交互以
> `aicli-micro-web-client-session-window-plan.md`（下称「Web 子方案」）为准；多进程 E2E 断言以
> `docs/e2e/debug-guide.md` §8（M1–M10）为准。**本文不重复设计，只回答四件事**：
> 按什么顺序做（切片 S1–S10）、改哪些文件（改动面）、怎么证明做对了（验证与证据）、出问题怎么退（回滚）。
>
> 施工范围：`backend/internal/mesh`（新建）、`backend/cmd/aicli-mesh`（新建）、
> `backend/cmd/aicli`（进程接入 + 删除 `web-ports` 路径）、Web 前端资源（P1）、脚本与 CI。
> 全部代码锚点已于 2026-09-24 实测核验（§17）；行号漂移时以最新实测为准。

---

## 0. 执行总览

### 0.1 交付目标（DoD）

> 本机任意两个 `aicli chat` 进程，能互相发现（`ls` / `peers`）、能定向调用（`call` / `send`）、
> 能实时看到对方状态（`events`）、能把新窗口开在正确的进程上（`spawn` / `open`）；
> 且**任一处失败**都退回今天的行为、不产生用户可见错误（MN1）。

完成定义（全部满足才算「已落地」）：

1. S1–S10 全部完成，每片的「验证」证据留档（命令 + 输出，§12）。
2. E2E-DEBUG-01 / 02 基线**全绿**（MN5 硬门禁）；E2E-DEBUG-03（M1–M10）全绿且基线已固化。
3. `go test ./internal/mesh/...` 与 `aicli-mesh --help` 冒烟进 CI（§15）。
4. 文档同步清单（§15）全部完成，无「文档说 A、代码做 B」。
5. 一键回退可用：`--mesh=false` 下行为 = 今天（§14）。

### 0.2 切片总表

| 切片 | 阶段 | 内容 | 依赖 | 可验证结果（门禁） |
|------|------|------|------|--------------------|
| S1 | P0 | `internal/mesh` 地基：paths + registry | — | `go test ./internal/mesh/...` 绿 |
| S2 | P0 | 进程接入：启动写档案 / 心跳 / 退出清理 / `--mesh=false` / `/web/api/health` | S1 | 双进程可发现；`--mesh=false` 不出现 |
| S3 | P0 | binding 替换 `web-ports`（sticky 端口迁移） | S1 S2 | `pprof_port_reuse_test.go` 改造后绿 |
| S4 | P0 | lease + 归属判定 + `conflict` | S3 | 双进程同会话 → `conflict` 可见 |
| S5 | P0 | `mesh/self` / `mesh/peers` + 清单登记 | S2 S4 | E2E-DEBUG-01 清单覆盖门禁仍绿 |
| S6 | P0 | `aicli-mesh` CLI（ls/show/url/gc/doctor/version）+ 发布登记 | S5 | `build.ps1 -Tools aicli-mesh` 出包 |
| S7 | P1 | `mesh/events` SSE 扇入 | S5 | 两进程互见实时状态 |
| S8 | P1 | `mesh/call` + CLI `call/send/screen` | S5 S7 | A 调用 B 完成一轮 prompt |
| S9 | P1 | `mesh/spawn` + `open` + 前端新窗口 | S6 S8 | 浏览器点一下开新窗口 |
| S10 | P1 | E2E-DEBUG-03（M1–M10）+ 基线固化 | S6 S7 S8 S9 | 聚合回归绿 + 基线固化 |

> **P2 治理项**（接管 `--takeover`、`--mesh-restrict-workspace`、`stop`、journal 查询、
> `aicli mesh` 别名、About 页网格小节）作为 S10 之后的 **S11+** 追加切片，沿用本文同一模板；
> 其中 M10 的收敛开关断言在 S10 场景内先落地（E2E 需要它验证「默认放行」的反面）。

### 0.3 硬顺序约束（不可交换）

1. **S3 必须在 S2 之后**：绑定替换需要 registry 提供路径解析与原子写。
2. **S5 必须在 S4 之后**：先有归属判定，再暴露视图（否则视图口径反复）。
3. **S7 / S8 必须在 S5 之后**：先有稳定视图，再谈实时与调用（避免边改视图边改协议）。
4. **S10 必须在 S6 之后**：E2E 用 CLI 做发现（M1 / M2 依赖 `aicli-mesh`）。
5. **全过程中 E2E-DEBUG-01/02 必须保持绿**（MN5）：任何切片不得让基线变红；变红即回滚该切片。
6. **前端不得早于 S9** 实现拉起逻辑；P0 阶段只做「只读复用」（Web 子方案 §9.2）。

### 0.4 施工纪律

- 每切片独立提交（粒度见 §13），提交信息含切片号：`mesh(S3): 会话绑定替换 web-ports`。
- 每切片完成后执行 §12 的「切片验证命令集」，输出贴进提交说明 / PR（证据留痕）。
- **不引入第三方依赖**：`internal/mesh` 只用标准库（与 `aiclipaths` / `workspaceregistry` 同风格）；
  `aicli-mesh` 只依赖 `internal/mesh` + 标准库。
- 编辑红线：S3 **不改 sticky 端口行为**，只换存储与命名（语义等价迁移）。
- 失败降级一律按网格方案 §4.7 矩阵实现，**不得**让网格失败冒泡成 chat 错误（MN1）。
- 网格写入永远「写自己的文件」：进程只写 `nodes/<自己的 node_id>.json` 与 `journal/<自己的 node_id>.ndjson`；
  绑定 / 租约的并发协议见 §3.2 / §3.3。

### 0.5 施工前准入检查（一次性，S1 之前）

| # | 检查 | 命令 / 动作 | 通过标准 |
|---|------|-------------|----------|
| A1 | 工作区干净 | `git status --porcelain` | 仅有本方案文档改动 |
| A2 | 基线绿 | `.\scripts\test-aicli-e2e-all.ps1 -BaselineOnly` | E2E-DEBUG-01/02 全绿 |
| A3 | 现有单测绿 | `go test ./backend/cmd/aicli/...` | 全绿 |
| A4 | 工具链 | `go version`；`.\scripts\build.ps1 -Tools aicli` | 能出包 |
| A5 | 锚点复核 | §17 表逐条 `Test-Path` / `Select-String` | 全部命中（行号漂移按最新为准） |
| A6 | 测试隔离 | 所有手工测试设 `AICLI_MESH_DIR=$env:TEMP\mesh-<slice>` | 不污染真实 `~/.aicli/mesh` |

---

## 1. S1 · `internal/mesh` 地基：paths + registry

**目标**：路径解析与节点档案读写可用、可单测；不接触任何进程生命周期。

**改动面**

| 动作 | 路径 | 说明 |
|------|------|------|
| 新增 | `backend/internal/mesh/paths.go` | `ResolvePaths()` / `EnsureDirs()`：`AICLI_MESH_DIR` > `AICLI_HOME`+`/mesh` > `~/.aicli/mesh` > fail-closed（网格方案 §2.3） |
| 新增 | `backend/internal/mesh/registry.go` | `NodeRecord`（§3.1 字段）+ 原子写 + 容错读 + 列举 + 删除 |
| 新增 | `backend/internal/mesh/paths_test.go`、`registry_test.go` | §12.1 前四组用例（路径 / 文件名安全 / 往返 / 未知版本） |
| 改造 | `backend/internal/aiclipaths/paths.go`（`DefaultWebPortsDir` 实测 25–33 行） | 新增 `DefaultMeshDir()`（= `defaultAICLIDir("mesh")`）；`DefaultWebPortsDir()` 留到 S3 随旧代码删除 |

**实现要点**

1. 包注释必须写明两条**与既有实现的差异**（§2.3）：① 网格尊重 `AICLI_HOME`，而
   `aiclipaths.defaultAICLIDir` 只看 `os.UserHomeDir()`；② 无 home 时 fail-closed（只读空、写入跳过），
   **绝不写 CWD 相对目录**。
2. `SanitizeKey(s) (string, error)`：仅保留 `[A-Za-z0-9._-]`，其余替换 `_`；拒绝 `.` / `..` / 空串
   （沿用 `sanitizeChatWebPortSessionID` 同款规则，实测 `chat_web_port_store.go:88`）。
3. 原子写 `writeFileAtomic(path, data, perm)`：同目录临时文件（`.tmp-<pid>-<rand>`）→ `Sync` → `os.Rename`；
   失败不留半截文件（R1）。
4. 容错读：非法 JSON / 未知大版本 → `state=unknown` + error，**绝不 panic**；读者永不写盘（§3.6）。
5. 时间统一 UTC RFC3339（`...Z`）；TTL 判定后续用单调时钟差值（R4）。
6. 权限：目录 0700、档案 0600；Windows 上 `os.Chmod` 尽力而为，**不因权限位不可用而拒绝写入**（§9.6）。

**验证与证据**

```powershell
go test ./internal/mesh/... -run 'Paths|Sanitize|Registry' -v
gofmt -l backend/internal/mesh
```

期望：全绿；`AICLI_MESH_DIR` 覆盖生效；无 home 用例走 fail-closed 而非 panic。

**门禁**：`go test ./internal/mesh/...` 绿 + `gofmt` 无输出。
**回滚**：纯新增，`git revert` 该提交即可（无外部可见行为）。

---

## 2. S2 · 进程接入：写档案 / 心跳 / 退出清理 / 总开关 / health

**目标**：`aicli chat` 进程启动即可被网格看见；`--mesh=false` 完全隐身且不影响 chat。

**改动面**

| 动作 | 路径 | 说明 |
|------|------|------|
| 新增 | `backend/internal/mesh/host.go` | 进程侧宿主：注册 / 心跳（30s）/ 忙碌翻转 / 退出清理 / 总开关短路（**架构 §11.1 未列此文件 → 偏差 D1**，落地后回填） |
| 改造 | `backend/cmd/aicli/main.go`（实测 148–158 行是既有端口落盘区） | flag 解析后初始化 host；退出路径挂 `host.Close()` |
| 改造 | `backend/cmd/aicli/commands/chat_session.go`（实测 196 / 281 行） | 会话激活 / 切换时更新档案 `session.*` |
| 新增 | `backend/cmd/aicli/commands/web_handlers_mesh.go` | `HandleChatWebAPIHealth`（新文件，避免 `web_handlers.go` 继续膨胀） |
| 改造 | `backend/cmd/aicli/pprof.go`（实测 349–399 行是路由注册区） | 注册 `GET /web/api/health` |
| 改造 | `backend/cmd/aicli/commands/web_schema.go`（实测 19–31 行是路径常量区） | 新增 `ChatWebAPIHealthPath = "/web/api/health"` |
| 新增 | `backend/internal/mesh/host_test.go` | 心跳失败退避、总开关短路、退出清理 |

**实现要点**

1. 启动序列：解析 flag → `mesh.Enabled()`（`--mesh=false` 短路）→ `ResolvePaths()`（失败 → warning + 继续 chat）
   → 写 `nodes/<node_id>.json`（`kind=chat`、`origin`、`workspace`、`capabilities`、`liveness.heartbeat_at`）。
2. 心跳 goroutine：30s ticker，只做「内存态快照 → 原子写」；**连续 3 次失败 → 停止心跳 + 一条 warning**
   （不做重试风暴，§4.1）。
3. 忙碌翻转：turn 开始 / 结束各重写一次档案（`session.busy` / `turn_id`）；低频，不做节流抖动。
4. 退出清理：正常退出删档案并写 `node.stopped`；异常退出靠 pid + TTL 判定（S4 / S6）。
5. `/web/api/health` 响应按 §5.2（`available/node_id/pid/uptime_sec/session_active/busy/mesh_ready`）；
   **不依赖会话与渲染器**，无会话时同样 200（`session_active=false`）。
6. 档案写失败 → 停止后续网格写入（降级矩阵 §4.7），chat 不受影响。
7. `--mesh=false`：不写档案、不订阅、不注册 `mesh/*` 端点（§9.7）。

**验证与证据**（手工，双终端 + 观察终端）

```powershell
$env:AICLI_MESH_DIR = "$env:TEMP\mesh-s2"
# 终端 1 / 2：两个真实进程（--pprof 每次分配随机空闲端口；真实端口见节点档案 endpoint.port）
.\backend\dist\aicli.exe chat --pprof
.\backend\dist\aicli.exe chat --pprof --mesh=false
# 终端 3：
(Get-ChildItem "$env:AICLI_MESH_DIR\nodes").Count          # 期望 1（--mesh=false 不写）
$node = Get-ChildItem "$env:AICLI_MESH_DIR\nodes" | Get-Content -Raw | ConvertFrom-Json
(Invoke-RestMethod "http://127.0.0.1:$($node.endpoint.port)/web/api/health") # 期望 200 且字段齐全
```

**门禁**：手工证据 + `host_test.go` 绿 + E2E-DEBUG-01/02 `-BaselineOnly` 仍绿。
**回滚**：摘掉 `main.go` / `chat_session.go` 的接入点即回到 S1 状态；测试目录直接删除。

---

## 3. S3 · 会话绑定替换 `web-ports`（sticky 端口迁移）

**目标**：sticky 端口行为**逐字不变**，存储从 `~/.aicli/web-ports/` 迁到 `mesh/bindings/`；旧代码彻底删除（不双读）。

**改动面**

| 动作 | 路径 | 说明 |
|------|------|------|
| 删除 | `backend/cmd/aicli/commands/chat_web_port_store.go` + `chat_web_port_store_test.go` | 语义迁到 `internal/mesh/binding.go`；进程级端口信息（实测 46 / 58 行）一并迁 |
| 新增 | `backend/internal/mesh/binding.go` | `SessionBinding`（§3.2）+ `LoadBinding` / `SaveBinding` / `TouchBinding`；sanitize 复用 S1 |
| 改造 | `backend/cmd/aicli/pprof_port_reuse.go:70` | `commands.LoadChatWebPortRecord` → `mesh.LoadBinding`（`stickyLoopbackServerAddr` 逻辑不变） |
| 改造 | `backend/cmd/aicli/main.go:148-158` | `SaveChatWebPortRecord` → `mesh.SaveBinding`（含 runtime info 等价物） |
| 改造 | `backend/cmd/aicli/commands/chat_session.go:196,281` | 注释与调用改走 binding |
| 改造 | `backend/cmd/aicli/pprof_port_reuse_test.go` | 用例迁到 binding：命中 / 未命中 / 端口被占回退随机 |
| 改造 | `backend/internal/aiclipaths/paths.go:31-33` | 删除 `DefaultWebPortsDir()` 与 `web-ports` 目录名常量 |
| 改造 | `backend/cmd/aicli/loopback_addr_test.go` | 若引用旧符号则同步改名 |

**实现要点**

1. **行为等价**：命中绑定 → 用 `preferred.port`；端口被占 → 回退随机并**更新绑定**（与今天一致）。
2. 绑定**不含令牌**（§3.2）；`last_node_id` / `last_workspace_path` 供 `ls` 展示「上次服务者」。
3. 绑定写失败 → 静默跳过（§4.7），不阻塞会话。
4. 旧目录不读不写、不迁移；清理走 S6 的 `gc --purge-legacy`（M8）。
5. `AICLI_WEB_PORTS_DIR` 不再被读取；测试统一改 `AICLI_MESH_DIR`。

**验证与证据**

```powershell
go test ./backend/cmd/aicli/... -run 'PortReuse|Sticky|Loopback' -v
```

期望：改造后的 sticky 用例绿；全仓 `grep` 无 `chat_web_port_store` / `LoadChatWebPortRecord` 残留引用。

**门禁**：上述命令绿 + E2E-DEBUG-01/02 `-BaselineOnly` 绿。
**回滚**：该提交整体 revert（旧文件随提交恢复）；`mesh/bindings/` 残留删除即可。

---

## 4. S4 · 租约与归属判定（conflict）

**目标**：会话互斥可判定；「多个 **live** 节点声称同一会话」→ `conflict` 可见（R12 口径）。

**改动面**

| 动作 | 路径 | 说明 |
|------|------|------|
| 新增 | `backend/internal/mesh/lease.go` | `Acquire` / `Renew` / `Release` / `Reclaim`；`O_CREATE\|O_EXCL`（§3.3） |
| 新增 | `backend/internal/mesh/view.go` | 聚合：扫 nodes + join bindings + 归属判定 + 探活预算（§4.3 / §5.4） |
| 新增 | `backend/internal/mesh/lease_test.go`、`view_test.go` | 租约 / 归属 / 跨工作区 / 探活预算用例（§12.1） |
| 改造 | `backend/internal/mesh/host.go` | 会话激活抢 `session-<sid>` 租约；心跳续约 `renewed_at` |

**实现要点**

1. 租约 TTL 默认 90s（§3.3），随 30s 心跳续约 → 三次容错。
2. 回收原子动作：`rename` 成 `.stale-<ts>` 再删（多进程竞争只有一个赢家，R3 / R4）。
3. 回收判据：`owner_pid` 不存在 **或** `now - renewed_at > ttl_sec`；TTL 用单调时钟差值优先。
4. 租约不可用（无 home / 权限不足）→ **降级为无互斥** + `lease.degraded`，绝不阻塞会话（§3.3 兜底）。
5. 归属判定：`conflict` 仅当「多个 live 节点声明同一会话」；stale 不参与（R12）。
6. 探活：并发上限 8、总预算 3s、单请求 1s；超预算 → `reachability=skipped`（§4.3）。

**验证与证据**

```powershell
go test ./internal/mesh/... -run 'Lease|Ownership|Probe' -v
```

期望：并发获取只有一个成功；pid 死亡 / TTL 到期可回收；双 live → `conflict`；超预算 → `skipped`。

**门禁**：命令绿 + 手工：第二个进程 resume 同一会话 → 视图 `conflict`，无第二个进程真正占用。
**回滚**：提交 revert；清空 `mesh/leases/`。

---

## 5. S5 · `mesh/self` / `mesh/peers` + 清单登记

**目标**：视图经 HTTP 暴露；`/debug/endpoints` 出现 `mesh` 分组；清单覆盖门禁绿。

**改动面**

| 动作 | 路径 | 说明 |
|------|------|------|
| 改造 | `backend/cmd/aicli/commands/web_handlers_mesh.go` | `HandleChatWebAPIMeshSelf` / `HandleChatWebAPIMeshPeers`（复用 `internal/mesh/view`，**不新写聚合**） |
| 改造 | `backend/cmd/aicli/commands/web_schema.go` | 路径常量 + 响应结构体（只增不改） |
| 改造 | `backend/cmd/aicli/pprof.go`（349–399 行区） | 注册两条路由 |
| 改造 | `backend/cmd/aicli/commands/chat_debug_endpoints.go`（实测 `loopbackDebugEndpoints` 70 行、`buildChatDebugEndpointList` 140 行） | 新增 `meshDebugEndpoints` 列表 + `Scheme: "mesh"` |
| 新增 | `backend/cmd/aicli/commands/web_handlers_mesh_test.go` | §12.2 前四组用例 |

**实现要点**

1. `self`：档案 + `derived`（内存实时值）+ `mesh.root`；`auth.token` 默认脱敏，
   `?reveal_token=1` 且回环同源才返回原文（与 `GET /web/api/token` 信任模型一致，§5.3）。
2. `peers`：默认 `scope=all`（**硬契约**）；`counts` 恒全量口径；`filter` 回显条件；
   `probe=0` 时**不发任何网络请求**；`state=live` / `workspace=` 只过滤输出（§5.4）。
3. 鉴权按 §5.8 矩阵；非回环默认拒绝写路径。
4. 清单登记后跑覆盖门禁：新端点要么被断言、要么带理由豁免（§5.1）。
5. 降级：网格目录不可读 → 200 + 空视图，**绝不 5xx**（§4.7）。

**验证与证据**

```powershell
go test ./backend/cmd/aicli/commands/... -run 'Mesh|DebugEndpoints' -v
.\scripts\test-aicli-e2e-all.ps1 -BaselineOnly
```

期望：单测绿；E2E-DEBUG-01 的清单覆盖门禁仍绿；双进程 `peers` 手工抽查（2 live + 1 stale，排序正确）。

**门禁**：命令绿 + 手工 `peers` 口径正确（`counts` 全量、过滤只影响 `nodes[]`）。
**回滚**：提交 revert（端点消失，回到 S4 状态，无数据副作用）。

---

## 6. S6 · `aicli-mesh` CLI + 发布登记

**目标**：工具可构建、可发现、可清理；`gc` / `doctor` 可用于日常运维。

**改动面**

| 动作 | 路径 | 说明 |
|------|------|------|
| 新增 | `backend/cmd/aicli-mesh/main.go` | 只做 flag 解析 + 调 `internal/mesh/cli`（先例：`backend/cmd/session-dedupe/`） |
| 新增 | `backend/internal/mesh/cli.go` | P0 六条命令：`ls/show/url/gc/doctor/version`（§7.2）；超过 800 行再拆 `internal/mesh/cli/`（可选偏差 D4） |
| 改造 | `scripts/build.ps1`（实测 83–90 行 `$script:toolRegistry`） | 追加 `aicli-mesh` 行：`Package=./cmd/aicli-mesh`、`LdflagsKind=main-version` |
| 改造 | `Makefile`（`.PHONY` 与目标区） | 追加 `aicli-mesh:` 目标 |
| 新增 | `backend/internal/mesh/cli_test.go` | `--json` schema 稳定、退出码 0–6、`gc` dry-run 与 `--apply` 一致性 |

**实现要点**

1. 目标解析：节点 ID / 前缀、会话 ID / 前缀、`pid:8124`；歧义列出候选，**不猜**（§7.2）。
2. 输出契约：默认 UTF-8 表格（无颜色时自动降级）；`--json` 稳定 schema
   （`{"schema_version":2,"nodes":[...],"counts":{...}}`）；退出码 0–6（§7.3）。
3. `gc`：默认 dry-run，`--apply` 才动手；删除条件含 pid 判定（**存活永不删**，R3）；
   `--purge-legacy` 只删旧 `web-ports/`（M8）；`--keep-days 7`、`--stale-ttl 10m`。
4. `doctor`：目录 / 权限（Windows 提示 Profile 依赖，§9.6）/ 陈旧节点 / 双占用 / 令牌可读性 /
   journal 完整性 / 旧目录新鲜度提示（§2.4）；`AICLI_MESH_DIR` 指向共享目录 → 告警（R13）。
5. `url`：默认不带令牌；`--with-token` 显式。
6. `ls` 默认**不探活**（毫秒级）；`--probe` 才发请求。

**验证与证据**

```powershell
.\scripts\build.ps1 -Tools aicli-mesh
.\backend\dist\aicli-mesh.exe ls --json | ConvertFrom-Json | Select-Object -ExpandProperty counts
.\backend\dist\aicli-mesh.exe doctor --json
.\backend\dist\aicli-mesh.exe gc           # dry-run，应与 --apply 输出一致（§12.1）
```

**门禁**：出包；`ls --json` 可被 `ConvertFrom-Json` 消费；`doctor` 无未解释告警；`gc` dry-run 一致。
**回滚**：工具留在仓库无害（未注册构建时不产出）；彻底回退则 revert 提交。

---

## 7. S7 · `mesh/events` SSE 扇入

**目标**：两进程互见实时状态；浏览器只连自己的进程（§6.5）。

**改动面**

| 动作 | 路径 | 说明 |
|------|------|------|
| 新增 | `backend/internal/mesh/client.go` | peer 订阅客户端：SSE 读取、退避重连（1s→30s）、`since_seq` 续传（**架构已列**） |
| 新增 | `backend/internal/mesh/fanin.go` | 本节点 SSE 广播：白名单事件 + 令牌桶 + 客户端上限（**偏差 D2**，落地回填） |
| 改造 | `backend/cmd/aicli/commands/web_handlers_mesh.go` | `HandleChatWebAPIMeshEvents`（SSE） |
| 改造 | `backend/cmd/aicli/commands/web_schema.go` / `pprof.go` | 路径常量 + 路由注册 |
| 新增 | `backend/internal/mesh/fanin_test.go` | 防环、seq 同源单调、令牌桶丢弃计数 |

**实现要点**

1. 帧类型按 §5.5：`mesh.peer.joined/left/updated`、`mesh.session.changed`、
   `mesh.call.invoked/completed`、`mesh.peer.event`。
2. **防环硬约束**：`mesh.peer.event` **绝不二次转发**（A↔B 互订不得出现回声，§6.3 / §12.1）。
3. `seq` 与 journal `seq` **同源同计数器**；续传 `?since_seq=<n>` + `id:` 行，语义对齐既有 `/web/api/events`。
4. 限流：每 peer 20 帧/秒，超限丢弃并计数（`dropped_events` 出现在 peers 视图）；
   `mesh.lagged` 跳号提示消费方做全量兜底（§6.4）。
5. 客户端上限：单节点 32（可配），超限 429，**不影响既有连接**。
6. 订阅拓扑 `?peers=auto|none`，默认 `auto`（本节点作为 fan-in 点）。
7. peer 订阅失败 → 指数退避；本进程 chat 完全不受影响（§4.7）。

**验证与证据**

```powershell
go test ./internal/mesh/... -run 'Fanin|Seq|Loop' -v
# 手工：A/B 两进程，观察 A 的事件流，在 B 里跑一轮
curl.exe -N "http://127.0.0.1:<A>/web/api/mesh/events?since_seq=0"
```

**门禁**：单测绿 + M5 预演（B 的忙碌翻转 ≤2s 出现在 A 的流里，且 A 流中无 `mesh.peer.event` 回声）。
**回滚**：提交 revert（端点消失，S5 视图仍在）。

---

## 8. S8 · `mesh/call` + CLI `call/send/screen`

**目标**：A 让 B 跑一轮 prompt 并拿回结果；写操作每次显式允许；令牌轮换可恢复。

**改动面**

| 动作 | 路径 | 说明 |
|------|------|------|
| 改造 | `backend/internal/mesh/client.go` | 调用编排：读目标档案 token → `X-AICLI-Token` + `X-AICLI-Mesh-Caller` → 401 重读重试一次 |
| 改造 | `backend/cmd/aicli/commands/web_handlers_mesh.go` | `HandleChatWebAPIMeshCall`（op 白名单 + `allow_write` + 审计 + 错误信封） |
| 改造 | `backend/cmd/aicli/commands/web_schema.go` / `pprof.go` | 路径常量 + 路由注册 |
| 改造 | `backend/internal/mesh/cli.go` | `call` / `send` / `screen` 子命令 |
| 新增 | `backend/cmd/aicli/commands/web_handlers_mesh_call_test.go` | §12.2 call 用例 |

**实现要点**

1. op 白名单 9 项（§5.6）：只读 `node.info` / `status` / `screen` / `turn` / `sessions.list`；
   写 `invoke` / `input` / `cancel` / `sessions.resume`。
2. 请求体上限 1 MiB → 413；`call` 默认超时 130s（可覆盖）。
3. 幂等：`client_request_id` 透传目标端点幂等键，**网格层不重复实现幂等**。
4. journal 只记 op 与耗时，**不记 args 正文**（可能含用户 prompt，§5.6 实现要点 4）。
5. 错误信封与 HTTP 映射按 §5.9：`ok`→200、`busy`→409、`not_found`→404、`refused`→403、
   `unreachable`/`timeout`→504、参数错误→400、内部错误→500。
6. 令牌轮换：401 → 重读档案重试一次；仍失败 `refused` + `mesh_token_stale`（不做无限重试）。
7. 跨工作区**默认放行**；仅 `--mesh-restrict-workspace` 开启时拒绝（`mesh_cross_workspace_denied`，§9.3）。
8. 写操作每次都要 `allow_write=true`（CLI `--allow-write`）；**无隐式放行**。

**验证与证据**

```powershell
go test ./backend/cmd/aicli/commands/... -run 'MeshCall' -v
.\backend\dist\aicli-mesh.exe send <session> "只回复两个字：收到" --wait --json
```

**门禁**：单测绿 + 手工 A→B invoke 成功（返回 `turn_id` + `assistant`，`duplicate=false`）。
**回滚**：提交 revert（CLI 子命令与端点一并消失）。

---

## 9. S9 · `mesh/spawn` + `open` + 前端新窗口

**目标**：浏览器点一下在正确进程上开新窗口；单飞锁保证不重复拉起。

**改动面**

| 动作 | 路径 | 说明 |
|------|------|------|
| 新增 | `backend/internal/mesh/spawn.go` | 单飞锁 + 锁内二次检查 + detach 启动 + 就绪等待 + URL 生成（**偏差 D3**） |
| 改造 | `backend/cmd/aicli/commands/web_handlers_mesh.go` | `HandleChatWebAPIMeshSpawn`（四态：`reused/started/not_running/failed`） |
| 改造 | `backend/internal/mesh/cli.go` | `open` 子命令 |
| 改造 | `backend/cmd/aicli/commands/web/js/sessions.js` | 会话列表 `⧉`「在新窗口打开」+ `applyDeepLinkSession`（**偏差 D5**：不新增 `js/mesh.js`，「打开会话」与「在新窗口打开」是同一交互面，逻辑并入既有模块；样式复用 `.session-action`，`style.css` 无需改动） |
| 改造 | `backend/cmd/aicli/commands/web_page.go` | 注入的 head 内联脚本增加 §7.3 深链自举：`?token=` → `sessionStorage` + `history.replaceState` 抹除地址栏令牌，`?session=` → `window.__aicli_deep_link_session`（**偏差 D6**：计划未列此文件，但令牌必须在 ES 模块首个 fetch 之前落位） |
| 改造 | `backend/cmd/aicli/commands/web_handlers.go`、`web_schema.go` | `sessions` 增加 `endpoint` / `ownership` 便捷字段（与 peers 同源，**只增不改**）；`resume` 的 `running_elsewhere` 前置检查 |
| 新增 | `backend/cmd/aicli/commands/web_handlers_mesh_spawn_test.go` | spawn 参数透传、状态机、单飞、失败带日志尾部 |

**实现要点**

1. 拉起命令行固定（§5.7）：`aicli resume <sid> --pprof --web-port <port> --web-host 127.0.0.1 --web-token <token>`；
   工作目录 = 会话工作区。
2. detach：Windows `DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP`，POSIX `Setsid`；
   stdout/stderr → `~/.aicli/logs/mesh-spawn-<sid>.log`（0600；启动行可能含令牌 → 需脱敏，M7）。
3. 状态机固定四值，**不返回 `starting`**；`wait_ms` 默认 8000 内同步等待。
4. 单飞：先抢 `spawn-<sid>` 锁；抢不到 → 直接 `reused`（读对方档案返回 URL）；锁内二次检查。
5. 前端：主点击 = 新窗口；预开窗口必须在**用户手势同步阶段**调用（弹窗资格）；
   `not_running`/`failed` 关闭占位窗口 + Toast + 可复制诊断。
6. 令牌**永不**进 `localStorage` / `sessionStorage` / DOM（Web 子方案 §10.2）。
7. `--mesh-allow-spawn` 默认开；关闭时 `spawn` → `refused`。

**验证与证据**

```powershell
go test ./backend/cmd/aicli/commands/... -run 'MeshSpawn|Sessions' -v
# 手工：Web 界面点「在新窗口打开」→ 新窗口起来、A 不受打扰
```

**门禁**：单测绿 + Web 子方案 §10.2 手工断言四条（弹窗资格 / 失败关窗 / 实时徽标 ≤2s / 无令牌残留）。
**回滚**：前端改动独立提交可单独 revert（回退到 in-place 打开）；spawn 端点随提交消失。

---

## 10. S10 · 多进程 E2E（E2E-DEBUG-03）+ 基线固化

**目标**：多进程场景进聚合回归；M1–M10 全绿并固化基线。

**改动面**

| 动作 | 路径 | 说明 |
|------|------|------|
| 新增 | `scripts/test-aicli-debug-endpoints-e2e-mesh.ps1` | 场景脚本：A/B 两真实进程（`--pprof`，端口由节点档案 `endpoint.port` 提供），dot-source `aicli-e2e-harness.ps1` 复用 `Invoke-HarnessRequest` / `Wait-AicliScreenStable` / `Test-AicliEndpointCoverage` |
| 改造 | `scripts/test-aicli-e2e-all.ps1`（实测 108–131 行 `$script:scenarios`；236–245 行参数分支；138 行基线正则） | 追加第三场景 + 参数分支 |
| 改造 | `scripts/e2e-assertion-baseline.json` | 跑 `-UpdateBaseline` 固化 M1–M10 |
| 改造 | `docs/e2e/debug-guide.md` | **已同步**（§8 + M1–M10）；跑通后回填「已落地」标注 |

**实现要点**

1. 断言命名必须是源码 `Add-Result` / `Add-Skip` 字面量，形如 `mesh/discovery-both-nodes`
   （基线抽取规则见 138 行正则）。
2. 汇总字段用 `pass/fail/skip` + `results[]`（不引入第三种方言；§12.3 第 4 条）。
3. M7：peers 默认输出、journal 文件、`/debug/endpoints` 均不含令牌原文。
4. M10：B 用**另一个工作区**（不同 cwd）启动；默认 `ls`/`peers` 双工作区在列；A→B 写调用默认成功；
   开启 `--mesh-restrict-workspace` 后同一调用 → `refused` + `mesh_cross_workspace_denied`。
5. 覆盖门禁：`mesh/*` 新端点全部被断言或带理由豁免（§12.3 第 5 条）。

**验证与证据**

```powershell
.\scripts\test-aicli-e2e-all.ps1                 # 三场景
.\scripts\test-aicli-e2e-all.ps1 -BaselineOnly   # 秒级门禁
```

**落地结果（2026-09-24）**

- 03 单跑：`verdict=pass pass=13 fail=0 skip=0`
  （`artifacts/aicli-debug-endpoints-e2e-mesh/run5/`；M5 的 `busy=true→false` 与 M3 的归属不变均为实测）。
- 聚合：`PASS=6 FAIL=0`（基线 3 条 + 场景 3 个）——01 PASS=42 / 02 PASS=28 / 03 PASS=13
  （`artifacts/aicli-e2e-all/20260924-131132/`）。
- 施工期抓到并修掉两个**产品缺陷**：Windows 判活把已退出进程误判为 `live`（D7）、
  `call`/`send` 目标解析与 CLI 口径分叉（D8）；另两条是 harness 断言口径与前置（D9 拒绝语义、D10 扇入就绪门）。
- 追加的 harness 前置：`aicli-mesh` 由「缺失才构建」改为**恒构建**（旧二进制会掩盖网格代码修复，实测踩过）。
- Web 子方案回填：S9 只落地「⧉ 新窗口 + 深链 + spawn/open 端点」；P0/P1 其余前端项与
  `sessions.endpoint/ownership`、`resume running_elsewhere` **未落地**（D11/D12，状态表见 Web 子方案 §0.1）。

**门禁**：三场景全绿；基线文件含 E2E-DEBUG-03 的 M1–M10；01/02 未回归。
**回滚**：场景脚本与基线条目 revert（不影响产品代码）。

---

## 11. 验收总表（切片 ↔ 设计章节 ↔ 断言编号）

| 切片 | 设计章节（网格方案） | 验收编号 / 断言 | 类型 |
|------|----------------------|------------------|------|
| S1 | §2.2 §2.3 §3.1 §3.6 | §12.1 路径 / 文件名安全 / 档案往返 / schema 演进 | 单测 |
| S2 | §3.1 §4.1 §5.2 §9.7 | §12.2 `health`；M1 前半（进程可被发现） | 单测 + 手工 |
| S3 | §2.4 §3.2 §4.2 | `pprof_port_reuse_test.go` 改造；§12.4 回归门禁 | 单测 |
| S4 | §3.3 §3.5 §4.3 §4.4 | §12.1 租约 / 归属 / 探活预算 | 单测 |
| S5 | §5.1 §5.3 §5.4 §5.8 | §12.2 `self` / `peers`；E2E-DEBUG-01 清单覆盖 | 单测 + E2E 门禁 |
| S6 | §7.1–§7.5 §4.5 | §12.1 GC；M8 / M9 | 单测 + 手工 |
| S7 | §5.5 §6.1–§6.6 | M5；§12.1 防环与 seq | 单测 + E2E |
| S8 | §5.6 §5.9 §9.2 §9.3 | M4 / M10；§12.2 call 用例 | 单测 + E2E |
| S9 | §5.7 §8.2 §8.3 | M3；Web 子方案 §10.2 | 单测 + 手工 + E2E |
| S10 | §12.3 | M1–M10 全量 | E2E |

**M1–M10 归属**（断言表见 debug-guide §8.5）：

| 断言 | 主覆盖切片 |
|------|-----------|
| M1 `mesh/discovery-both-nodes` | S2 / S5 / S6 |
| M2 `mesh/cli-api-parity` | S6 |
| M3 `mesh/session-lease-exclusive` | S4 / S9 |
| M4 `mesh/cross-call-invoke` | S8 |
| M5 `mesh/realtime-fanin` | S7 |
| M6 `mesh/crash-reconcile` | S4 / S6 |
| M7 `mesh/no-token-leak` | S2 / S7 / S9 |
| M8 `mesh/legacy-purge` | S6 |
| M9 `mesh/self-containment` | S6 |
| M10 `mesh/cross-workspace-ops` | S8 + S11（收敛开关） |

---

## 12. 可复制执行手册（命令集）

> 约定：所有手工命令先设 `$env:AICLI_MESH_DIR = "$env:TEMP\mesh-<slice>"`（A6），跑完删除。
> 二进制路径以 `scripts/build.ps1` 实际输出为准（实测默认 `OutputDir` 为 `backend\dist\`，
> 产物如 `backend\dist\aicli.exe`；用 `-OutputDir` 时按实际输出调整）。

### 12.1 切片验证命令集（逐片）

```powershell
# S1
go test ./internal/mesh/... -run 'Paths|Sanitize|Registry' -v
# S2
go test ./internal/mesh/... -run 'Host|Heartbeat' -v
# S3
go test ./backend/cmd/aicli/... -run 'PortReuse|Sticky|Loopback' -v
# S4
go test ./internal/mesh/... -run 'Lease|Ownership|Probe' -v
# S5
go test ./backend/cmd/aicli/commands/... -run 'Mesh|DebugEndpoints' -v
# S6
.\scripts\build.ps1 -Tools aicli-mesh
.\backend\dist\aicli-mesh.exe ls --json | ConvertFrom-Json | Select-Object -ExpandProperty counts
.\backend\dist\aicli-mesh.exe doctor --json
# S7
go test ./internal/mesh/... -run 'Fanin|Seq|Loop' -v
# S8
go test ./backend/cmd/aicli/commands/... -run 'MeshCall' -v
# S9
go test ./backend/cmd/aicli/commands/... -run 'MeshSpawn|Sessions' -v
# S10
.\scripts\test-aicli-e2e-all.ps1
```

### 12.2 全量回归（每切片收尾 + 合并前）

```powershell
go test ./internal/mesh/...
go test ./backend/cmd/aicli/...
.\scripts\test-aicli-e2e-all.ps1 -BaselineOnly      # 秒级门禁（01/02）
```

### 12.3 基线固化（仅 S10 执行一次，之后进版本控制）

```powershell
.\scripts\test-aicli-e2e-all.ps1 -UpdateBaseline
git diff -- scripts/e2e-assertion-baseline.json    # 人工核对：只新增 E2E-DEBUG-03 的 M1–M10
```

### 12.4 手工验收清单（对齐网格方案 §12.5，逐条打勾）

1. 两个终端各起 `aicli chat --pprof`：`aicli-mesh ls` 两行，地址 / 会话 / 工作区正确。
2. `aicli-mesh url <session>` 浏览器可直接打开。
3. A 的 Web 界面点另一会话「在新窗口打开」：新窗口起来，A 不受打扰。
4. `aicli-mesh watch` 期间在 B 跑一轮：A 界面秒级看到 B 变忙 / 变闲。
5. 强杀 B：`ls` 标 `stale`，`gc --apply` 后干净。
6. `doctor`：无告警（或告警可解释）。
7. 跨工作区：W1 起 A、W2 起 B → `ls` 默认两行（工作区列不同）；`call <B> invoke --allow-write` 成功；
   以 `--mesh-restrict-workspace` 重启后同一调用被拒（`mesh_cross_workspace_denied`）。

---

## 13. 依赖图与提交粒度

### 13.1 依赖图

```text
S1 ──> S2 ──> S3 ──> S4 ──> S5 ──┬──> S6 ──┬──> S9 ──> S10
                                  │         │
                                  └──> S7 ──┴──> S8 ──┘
约束：S5 之后才 S7/S8；S6 之后才 S10；全程 E2E-DEBUG-01/02 保持绿（MN5）。
```

### 13.2 提交粒度与信息模板

| 切片 | 建议提交 | 信息模板 |
|------|----------|----------|
| S1 | 1 个 | `mesh(S1): internal/mesh 路径解析与节点档案读写` |
| S2 | 1 个（含 flag / 心跳 / health） | `mesh(S2): 进程接入网格 + /web/api/health + --mesh 总开关` |
| S3 | 1 个（删除与替换同一提交，保持可编译） | `mesh(S3): 会话绑定替换 web-ports（sticky 端口语义不变）` |
| S4 | 1 个 | `mesh(S4): 租约与归属判定（conflict）` |
| S5 | 1 个 | `mesh(S5): mesh/self|peers + 调试端点清单登记` |
| S6 | 1–2 个（CLI 主体 / 构建登记可拆） | `mesh(S6): aicli-mesh CLI（ls/show/url/gc/doctor/version）` |
| S7 | 1 个 | `mesh(S7): mesh/events SSE 扇入（含防环与限流）` |
| S8 | 1 个 | `mesh(S8): mesh/call + CLI call/send/screen` |
| S9 | 2 个（后端 spawn / 前端交互） | `mesh(S9): mesh/spawn + open` / `mesh(S9): Web 侧新窗口打开` |
| S10 | 2 个（场景脚本 / 基线固化） | `mesh(S10): E2E-DEBUG-03 场景脚本` / `mesh(S10): 基线固化` |

**硬规则**：S3 的删除与替换必须在**同一个提交**内完成（否则中间态编译不过，违反「每提交可构建」）。

---

## 14. 回滚操作手册（逐阶段）

### 14.1 运行期回退（不回退版本）

| 场景 | 操作 | 效果 |
|------|------|------|
| 网格行为异常，需立刻恢复今天行为 | 进程加 `--mesh=false` 重启 | 不写档案、不订阅、不注册 `mesh/*`；`aicli-mesh` 仍可读历史（§9.7） |
| 只想关闭审计 | `--mesh-journal=false` | 失去审计与对账，运行不受影响 |
| 不想让任何进程被拉起 | `--mesh-allow-spawn=false` | `mesh/spawn` → `refused`（前端提示用 `aicli-mesh url`） |
| 想禁止跨工作区写 | `--mesh-restrict-workspace` | 跨工作区写调用 → `refused` + `mesh_cross_workspace_denied` |
| 网格目录损坏 / 想彻底重置 | `Remove-Item $env:AICLI_MESH_DIR -Recurse` | 全部进程重新生成档案；绑定丢失 → resume 可能换端口 |

### 14.2 版本回退（按阶段）

| 回退到 | 操作 | 注意 |
|--------|------|------|
| S1–S5 之间 | `git revert <切片提交>` | 端点与 CLI 逐片消失；无数据副作用 |
| S6 之前 | revert S6 → 工具不再构建 | 手工测试改用 `go run ./backend/cmd/aicli-mesh`（若已存在） |
| S9 之前 | revert 前端提交 | 打开方式回到 in-place（Web 子方案 §9.2 的 P0 行为） |
| 完全回退到「无网格」 | revert S2 之后全部提交 | 旧版本二进制不读 `mesh/`；粘性端口退回随机（§2.4 一次性切换的既定代价） |

**回退后必做**：`.\scripts\test-aicli-e2e-all.ps1 -BaselineOnly` 确认 01/02 绿。

---

## 15. 文档同步与发布登记 checklist

### 15.1 文档（落地时同步，否则出现「文档说 A、代码做 B」）

| # | 文档 | 需改内容 | 归属切片 |
|---|------|----------|----------|
| 1 | `docs/user-guide/aicli.md:259` | `AICLI_WEB_PORTS_DIR` → `AICLI_MESH_DIR`；「粘性端口档案」→「会话绑定」 | S3 |
| 2 | `docs/aicli/debug-chat-status.md:53-62` | 端口档案路径 `~/.aicli/web-ports/` → `mesh/bindings/` | S3 |
| 3 | `docs/aicli/web-remote-api.md` | `mesh/*` 端点族已同步；`sessions` 新增字段（`endpoint` / `ownership`）与 `resume` 新错误码**未落地**（D11/D12），文档未声称已实现 | S5 / S9 |
| 4 | `docs/aicli/mesh-cli.md` | **新增**；网格方案 §7 是草稿，落地后以该文档为准 | S6 |
| 5 | `docs/e2e/debug-guide.md` | 已同步（§8 + M1–M10）；S10 后回填「已落地」 | S10 |
| 6 | `docs/plan/aicli-mesh-architecture.md` + Web 子方案 | 回填「已落地 / 偏差」标注（含 D1–D12） | S10 |

### 15.2 构建与 CI

| # | 位置 | 动作 | 归属切片 |
|---|------|------|----------|
| 1 | `scripts/build.ps1:83-90` | `toolRegistry` 追加 `aicli-mesh` | S6 |
| 2 | `Makefile` | 追加 `aicli-mesh:` 目标 | S6 |
| 3 | `scripts/test-aicli-e2e-all.ps1` | 追加 E2E-DEBUG-03 场景与参数分支 | S10 |
| 4 | `scripts/e2e-assertion-baseline.json` | `-UpdateBaseline` 固化 M1–M10 | S10 |
| 5 | CI | 新增 `go test ./internal/mesh/...` + `aicli-mesh --help` 冒烟；保留 01/02 基线门禁（MN5） | S6 / S10 |

### 15.3 偏差登记（落地后回填网格方案 §11.6）

| 编号 | 偏差 | 理由 | 回填动作 |
|------|------|------|----------|
| D1 | 新增 `internal/mesh/host.go` | 进程生命周期（注册 / 心跳 / 退出）不宜塞进 registry | 在 §11.6「新增」行补 `host.go` |
| D2 | 新增 `internal/mesh/fanin.go` | 入站 SSE 广播与出站订阅（client.go）是两件事 | 同上 |
| D3 | 新增 `internal/mesh/spawn.go` | 拉起逻辑与调用编排（client.go）分离，便于单测 | 同上 |
| D4 | `internal/mesh/cli.go` 超 800 行时拆 `internal/mesh/cli/` | 目录化更清晰；先例 `cmd/session-dedupe` | 视最终结构回填 |
| D5 | 前端不新增 `js/mesh.js`：「打开会话」与「在新窗口打开」并入既有 `sessions.js`，样式复用 `.session-action`（`style.css` 不动） | 同一交互面拆两个模块会带来状态双写；`web/js/*` 已按功能分文件 | 在 §11.6 记录（S9 落地时已在 §9 表内标注） |
| D6 | `web_page.go` 注入的 head 内联脚本做深链自举（`?token=` → `sessionStorage` + `history.replaceState` 抹除地址栏；`?session=` → `window.__aicli_deep_link_session`） | 计划未列该文件，但令牌必须在 ES 模块首个 `fetch` 之前落位，否则首个请求无令牌 | 同上（S9 落地时已在 §9 表内标注） |
| D7 | `process_alive_windows.go` 判活必须读退出码（`GetExitCodeProcess` == `STILL_ACTIVE`），不能只看 `OpenProcess` 成功 | Windows 只要还有句柄指向进程对象，PID 就不回收：`OpenProcess` 对**已退出**进程照样成功。E2E-DEBUG-03 实测到「被强杀的 B 永久 `live`」→ 视图不转 `stale`、`gc` 回收不掉 | §11.6 记录「判活口径」：Windows 以退出码为准（新增 `process_alive_windows_test.go` 两个方向对照） |
| D8 | `call.go::ResolveCallTarget` 与 CLI `resolveTarget` 合并为共享 `matchTargetNodes`（pid → node 精确 → node 前缀 → session 精确 → session 前缀；歧义时 node id 精确优先，大小写不敏感） | 两处各写一套匹配规则 → 口径分叉（CLI 能定位、`mesh call` 报 `target_not_found`） | §11.6 记录「目标解析唯一实现」（回归测试 `TestResolveCallTargetSharesCLITargetRules`） |
| D9 | M3 断言口径：拒绝语义以「目标返回的 store 查找结果」为准（`session not found` / `busy` / `running_elsewhere` 都可能），E2E 断的是**归属不变**（owner 仍为 A、`counts.conflict=0`） | `sessions.resume` 是转发到目标 `/web/api/sessions/resume` 的写 op；目标本地存储没有该会话时返回 404，压根到不了租约判定。文档原写「返回 busy / running_elsewhere」属过度指定 | debug-guide §8.5 该行改写；租约「不抢活租约」由 `lease_test.go`（默认不抢 / `--takeover` 才抢 / host 感知被抢）覆盖 |
| D10 | M5 前置「扇入就绪门」：先等 A 的流里出现 B 的帧（订阅接通时 A 合成的 `mesh.peer.joined`，上限 `-FaninReadySec`，缺省 20s）再发 invoke | 订阅由 `Subscriber.Sync` 按 tick 建立；订阅接通前的窗口里 B 的 `busy=true` 不会被扇入（流不重放历史）→ 断言会随 tick 时机抖动。实测：A 05:05:53 才接上 B，而 invoke 05:05:51.7 已开始，`busy=true` 永久丢失 | debug-guide §8.5/§8.6 记录该前置与失败排查路径 |
| D11 | Web `GET /web/api/sessions` 的 `endpoint` / `ownership` 字段**未落地**（`chatWebSessionListItem` 仍只有 id/title/summary/message_count/created_at/updated_at/current），前端侧栏徽标 / 端点行 / 跨工作区分组随之未落地 | S9 只做了「⧉ 新窗口打开 + 深链 + spawn/open 端点」；子方案 P0 ① 与其后端字段是纯展示项，被挤出 S9 范围且未单列切片 | Web 子方案新增 §0.1「落地状态」标注未落地；后续 Web 侧收口切片按子方案 §5.1/§6.1 落地 |
| D12 | `resume` 的 `running_elsewhere` 前置检查**未落地**（`/web/api/sessions/resume` 无归属判定，全仓 Go 代码无该标识） | 同 D11：归属/互斥已由网格租约（`session-<sid>`）与 `spawn` 锁内二次检查覆盖；Web 端冲突弹窗属体验项 | Web 子方案 §0.1 / FR11 标注未落地；租约语义由 `lease_test.go` 覆盖（见 D9） |

---

## 16. 风险与应对（施工期）

> 设计期风险（R1–R16）与对策见网格方案 §10；下表只列**施工期**新增风险。

| # | 风险 | 触发点 | 应对 |
|---|------|--------|------|
| C1 | S3 迁移遗漏引用 → 编译失败或运行期空指针 | 删除 `chat_web_port_store.go` | 删除与替换同一提交；合并前 `go build ./...` + 全仓 `grep` 旧符号清零 |
| C2 | sticky 端口行为被悄悄改变 | S3 重构 | 迁移前后跑同一组用例（命中 / 未命中 / 占用回退）；**只换存储，不换语义** |
| C3 | 心跳线程与主循环争用导致卡顿（R2） | S2 / S4 | 心跳只做「快照 + 一次 rename」；30s 一次；失败退避不重试风暴 |
| C4 | E2E 新场景不稳定（端口 / 时序抖动） | S10 | 用 `--pprof`（随机空闲端口，端口从档案 `endpoint.port` 读取）+ 就绪等待；断言用 `Wait-AicliScreenStable`；TTL 相关断言留足余量 |
| C5 | 基线漂移：01/02 断言被无意改动 | 全程 | 每切片收尾跑 `-BaselineOnly`；基线 diff 必须「只增不改」 |
| C6 | 测试污染真实网格目录 | 手工测试 | A6 强制 `AICLI_MESH_DIR=$env:TEMP\...`；测试后清理 |
| C7 | Windows 权限位不生效导致安全断言误判 | S1 / S6 | 断言口径：POSIX 校验 0600；Windows 校验「位于用户 Profile 内」而非 chmod（§9.6） |
| C8 | 视图两套聚合漂移（CLI 与 HTTP） | S5 / S6 | 单一实现 `internal/mesh/view`；CLI 与 handler 都调它（M2 同源断言兜底） |
| C9 | 扇入回声（A↔B 互订） | S7 | `mesh.peer.event` 不二次转发 + 单测断言（§12.1） |
| C10 | spawn 拉起进程失败留下孤儿窗口 / 僵尸 | S9 | 四态状态机 + `failed` 带日志尾部；`not_running` 关占位窗口；`doctor` 提示残留 |
| C11 | 令牌出现在日志 / 清单 / 前端存储 | S2 / S7 / S9 | journal 脱敏断言 + M7 + 前端「无令牌残留」手工断言 |
| C12 | 网格写入失败被误当 chat 错误上报 | 全程 | 所有写入路径按 §4.7 静默降级 + 单条 warning；代码评审专项检查 |

---

## 17. 锚点核验记录（2026-09-24 实测）

### 17.1 精确命中（可直接施工）

| 锚点 | 实测位置 | 核验方式 |
|------|----------|----------|
| `ChatWebPortRecord` / `SetChatWebPortRuntimeInfo` / `ChatWebPortRuntimeInfo` | `commands/chat_web_port_store.go:26 / 46 / 58` | grep |
| `sanitizeChatWebPortSessionID` / `LoadChatWebPortRecord` / `SaveChatWebPortRecord` | 同上 `:88 / :122 / :145` | grep |
| `stickyLoopbackServerAddr`（含 `LoadChatWebPortRecord` 调用） | `backend/cmd/aicli/pprof_port_reuse.go:65-75`（调用在 `:70`） | view |
| 端口落盘调用点（`SaveChatWebPortRecord` + runtime info） | `backend/cmd/aicli/main.go:148-158`（调用在 `:154`） | view |
| `DefaultWebPortsDir()`（含 `web-ports` 语义注释） | `backend/internal/aiclipaths/paths.go:25-33` | view |
| Web 路由注册区（`mux.HandleFunc` 连续块） | `backend/cmd/aicli/pprof.go:349-399` | grep |
| Web 路径常量区（`ChatWebPath` / `ChatWebAPIStatusPath` / `ChatWebAPISessionsPath`） | `commands/web_schema.go:19-31` | grep |
| 调试端点清单（列表 / 构建 / 文本 / JSON 四个入口） | `commands/chat_debug_endpoints.go:70 / 140 / 295 / 380` | grep |
| `$script:toolRegistry`（6 个工具行） | `scripts/build.ps1:83-90` | view |
| Makefile 目标（含 `aicli:` / `aicli-console:` / `test:` 等） | `Makefile`（`.PHONY` + 目标区） | shell |
| E2E 场景表 / 参数分支 / 基线正则 | `scripts/test-aicli-e2e-all.ps1:108-131 / 236-245 / 138` | 既有核验（debug-guide §12.3 记录） |
| E2E harness 可复用函数（`Invoke-HarnessRequest` 等） | `scripts/aicli-e2e-harness.ps1` | 既有核验（debug-guide §12.3 第 5 条） |
| 构建默认输出目录（`-OutputDir` 缺省） | `scripts/build.ps1:467-468` → `backend\dist\`（实测存在 `aicli.exe` 等产物） | grep + shell |
| Web 前端资源实测布局 | `commands/web/{index.html,style.css,app.js}` + `commands/web/js/*.js`（`sessions.js` 在列） | shell |

### 17.2 施工时复核（未完全确认，动手前先看）

| 锚点 | 待确认 | 复核命令 |
|------|--------|----------|
| `backend/cmd/aicli/loopback_addr_test.go` | 是否引用 `chat_web_port_store` 符号 | `Select-String -Path backend/cmd/aicli/loopback_addr_test.go -Pattern 'WebPort|LoadChatWebPort'` |
| `commands/web_handlers_test.go` | 现有 sessions / resume 用例规模（S9 只增不改的回归面） | `Select-String -Path backend/cmd/aicli/commands/web_handlers_test.go -Pattern 'func Test' \| Measure-Object` |
| `commands/chat_session.go:196,281` | 行号与注释现状 | `Select-String -Path backend/cmd/aicli/commands/chat_session.go -Pattern 'web-ports\|WebPort'` |
| Web 子方案 §9.1 的 `commands/web/css/*` | 实测**无** `css/` 目录，样式为单文件 `commands/web/style.css`（S9 以实测为准） | `Get-ChildItem backend/cmd/aicli/commands/web -Recurse -File` |

### 17.3 确认「尚不存在」（施工即新增，无冲突）

| 符号 | 核验 |
|------|------|
| `backend/internal/mesh`（包） | `Test-Path` → `False` |
| `backend/cmd/aicli-mesh`（工具） | `Test-Path` → `False` |
| `docs/aicli/mesh-cli.md` | 不存在（S6 新增） |
| `scripts/test-aicli-debug-endpoints-e2e-mesh.ps1` | 不存在（S10 新增） |

---

## 18. 索引：切片 ↔ 文件 ↔ 验收 ↔ 证据

| 切片 | 新增文件（核心） | 改造文件（核心） | 验收 | 证据留痕 |
|------|------------------|------------------|------|----------|
| S1 | `internal/mesh/{paths,registry}.go` + 测试 | `internal/aiclipaths/paths.go` | §12.1 单测 | 测试输出 |
| S2 | `internal/mesh/host.go`、`commands/web_handlers_mesh.go` | `cmd/aicli/main.go`、`commands/chat_session.go`、`pprof.go`、`web_schema.go` | §12.1 + 手工 | 档案截图 / health JSON |
| S3 | `internal/mesh/binding.go` | 删除 `chat_web_port_store.go`；`pprof_port_reuse.go`、`main.go`、`aiclipaths` | 单测 | 测试输出 |
| S4 | `internal/mesh/{lease,view}.go` + 测试 | `internal/mesh/host.go` | §12.1 | 测试输出 |
| S5 | `commands/web_handlers_mesh_test.go` | `web_handlers_mesh.go`、`web_schema.go`、`pprof.go`、`chat_debug_endpoints.go` | 单测 + 清单门禁 | `peers` JSON |
| S6 | `cmd/aicli-mesh/main.go`、`internal/mesh/cli.go` + 测试 | `scripts/build.ps1`、`Makefile` | 出包 + 手工 | `ls --json` / `doctor` 输出 |
| S7 | `internal/mesh/{client,fanin}.go` + 测试 | `web_handlers_mesh.go`、`web_schema.go`、`pprof.go` | 单测 + M5 预演 | 事件流抓取 |
| S8 | `web_handlers_mesh_call_test.go` | `internal/mesh/client.go`、`web_handlers_mesh.go`、`cli.go` | 单测 + M4 | 调用信封 JSON |
| S9 | `internal/mesh/spawn.go`、`web/js/mesh.js`、`web_handlers_mesh_spawn_test.go` | `web/js/sessions.js`、`index.html`、`style.css`、`web_handlers.go` | 单测 + Web §10.2 | 新窗口截图 |
| S10 | `scripts/test-aicli-debug-endpoints-e2e-mesh.ps1` | `scripts/test-aicli-e2e-all.ps1`、`e2e-assertion-baseline.json` | M1–M10 | `summary.json` + 基线 diff |

---

## 附录：本文与三份基准文档的分工

| 文档 | 回答的问题 | 何时看 |
|------|-----------|--------|
| `aicli-mesh-architecture.md` | 为什么这样设计 / 契约是什么 | 施工前通读；争议时以它为准 |
| **本文** | 按什么顺序做 / 改哪些文件 / 怎么验证 / 怎么回滚 | 施工全程 |
| `aicli-micro-web-client-session-window-plan.md` | Web 侧交互与前端落点 | S5 / S9 前 |
| `docs/e2e/debug-guide.md` §8 | 多进程 E2E 怎么断言 | S10 前 |

> 变更记录：2026-09-24 初版（S1–S10 + 验收 / 回滚 / 锚点核验）。
> 每完成一个切片，在 §15.3 登记实际偏差，并回填网格方案 §11.6。
