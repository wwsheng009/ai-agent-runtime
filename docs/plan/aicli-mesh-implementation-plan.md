# aicli-mesh 多进程网格 · 实施方案（2026-09-24）

> **定位**：`aicli-mesh-architecture.md`（下称「网格方案」）的**施工执行文档**。
> 设计契约（数据模型 / API / 开关 / 验收编号）以网格方案为准；Web 侧交互以
> `aicli-micro-web-client-session-window-plan.md`（下称「Web 子方案」）为准；多进程 E2E 断言以
> `docs/e2e/mesh-e2e.md` §5（M1–M12）为准。**本文不重复设计，只回答四件事**：
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
2. E2E-DEBUG-01 / 02 基线**全绿**（MN5 硬门禁）；E2E-DEBUG-03（M1–M12）全绿且基线已固化。
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
| S10 | P1 | E2E-DEBUG-03（M1–M10；S19 起扩为 M1–M12）+ 基线固化 | S6 S7 S8 S9 | 聚合回归绿 + 基线固化 |

> **P2 治理项**（接管 `--takeover`、`--mesh-restrict-workspace`、`stop`、journal 查询、
> `aicli mesh` 别名）作为 S10 之后的 **S11+** 追加切片，沿用本文同一模板；
> 其中 M10 的收敛开关断言在 S10 场景内先落地（E2E 需要它验证「默认放行」的反面）。
> **S11（Web 侧收口一：sessions 便捷视图 + 前端徽标/分组/开关 + resume 冲突）定义见 §19**，
> About 页网格小节并入该切片；实时收口（`mesh/events` 前端订阅）为 S12。
> 之后的治理项切片：S15 接管二次确认（§23）、**S16 `stop`（§24）**、**S17 `watch`（§25）**、
> **S18 `aicli mesh` 别名（§26）**。
> **S19 治理开关收口（§27）**：补 `--mesh-allow-nonloopback` / `--mesh-journal` 两个
> 开关的实现，并把 P9/P10/P11 三条手工治理语义机器化为 M11/M12（03 断言 13 → 16）。

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
8. warning 出口单一化：`HostConfig.Warn` / `PeerSyncConfig.Warn` / 启动失败都走 main 的 `meshWarn`；
   交互式会话期间经 `commands.NotifyChatDiagnostic` 投递为语义补充 cell（TerminalSession 是唯一物理写者），
   其余情况回退 stderr。**禁止在会话期间直接写 stderr**：那会落在 FixedBottomSurface 的底部保留区上，
   把 TUI 状态栏覆盖成半截文本。

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
| 改造 | `docs/e2e/debug-guide.md` | **已同步**（§8 + M1–M10）；跑通后回填「已落地」标注；2026-09-24 该场景独立成文为 `docs/e2e/mesh-e2e.md` |

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
| S10 | §12.3 | M1–M10 全量（S19 起扩为 M1–M12） | E2E |
| S11 | Web 子方案 §5.x §6.2 §6.3 | Web 子方案 §10.1 单测 + asset 契约；D11/D12 落地 | 单测 + 手工 + E2E 门禁 |
| S19 | 架构 §9.4 §9.5 §9.7 | M11 / M12；两个治理开关的默认值与降级语义 | 单测 + E2E |

**M1–M12 归属**（断言表见 mesh-e2e.md §5）：

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
| M11 `mesh/nonloopback-default-deny` + `mesh/nonloopback-cli-parity` | S19 |
| M12 `mesh/journal-disabled` | S19 |

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
git diff -- scripts/e2e-assertion-baseline.json    # 人工核对：只新增 E2E-DEBUG-03 的 M1–M12
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
| S11 | 1 个（后端契约 + 前端视图，同一提交内前后端同源切换） | `mesh(S11): Web 侧收口——sessions 扩展 + 徽标/分组/开关 + resume 冲突` |

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
| 3 | `docs/aicli/web-remote-api.md` | `mesh/*` 端点族已同步；`sessions` 便捷视图与 `resume` 归属检查（D11/D12）**已落地并写入 §9.7**（S11） | S5 / S9 / S11 |
| 4 | `docs/aicli/mesh-cli.md` | **新增**；网格方案 §7 是草稿，落地后以该文档为准 | S6 |
| 5 | `docs/e2e/mesh-e2e.md` | 已同步（场景 + M1–M12 + 故障排查）；2026-09-24 由 debug-guide §8 独立成文 | S10 / S19 |
| 6 | `docs/plan/aicli-mesh-architecture.md` + Web 子方案 | 回填「已落地 / 偏差」标注（含 D1–D13） | S10 / S19 |

### 15.2 构建与 CI

| # | 位置 | 动作 | 归属切片 |
|---|------|------|----------|
| 1 | `scripts/build.ps1:83-90` | `toolRegistry` 追加 `aicli-mesh` | S6 |
| 2 | `Makefile` | 追加 `aicli-mesh:` 目标 | S6 |
| 3 | `scripts/test-aicli-e2e-all.ps1` | 追加 E2E-DEBUG-03 场景与参数分支 | S10 |
| 4 | `scripts/e2e-assertion-baseline.json` | `-UpdateBaseline` 固化 M1–M12（S10 首固 03=13；S19 追加 3 条后重固 03=16） | S10 / S19 |
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
| D9 | M3 断言口径：拒绝语义以「目标返回的 store 查找结果」为准（`session not found` / `busy` / `running_elsewhere` 都可能），E2E 断的是**归属不变**（owner 仍为 A、`counts.conflict=0`） | `sessions.resume` 是转发到目标 `/web/api/sessions/resume` 的写 op；目标本地存储没有该会话时返回 404，压根到不了租约判定。文档原写「返回 busy / running_elsewhere」属过度指定 | mesh-e2e.md §5 该行改写；租约「不抢活租约」由 `lease_test.go`（默认不抢 / `--takeover` 才抢 / host 感知被抢）覆盖 |
| D10 | M5 前置「扇入就绪门」：先等 A 的流里出现 B 的帧（订阅接通时 A 合成的 `mesh.peer.joined`，上限 `-FaninReadySec`，缺省 20s）再发 invoke | 订阅由 `Subscriber.Sync` 按 tick 建立；订阅接通前的窗口里 B 的 `busy=true` 不会被扇入（流不重放历史）→ 断言会随 tick 时机抖动。实测：A 05:05:53 才接上 B，而 invoke 05:05:51.7 已开始，`busy=true` 永久丢失 | mesh-e2e.md §5/§6 记录该前置与失败排查路径 |
| D11 | Web `GET /web/api/sessions` 的 `endpoint` / `ownership` 字段**未落地**（`chatWebSessionListItem` 仍只有 id/title/summary/message_count/created_at/updated_at/current），前端侧栏徽标 / 端点行 / 跨工作区分组随之未落地 | S9 只做了「⧉ 新窗口打开 + 深链 + spawn/open 端点」；子方案 P0 ① 与其后端字段是纯展示项，被挤出 S9 范围且未单列切片 | Web 子方案新增 §0.1「落地状态」标注未落地；后续 Web 侧收口切片按子方案 §5.1/§6.1 落地；**S11 已收敛**（见 §19.4） |
| D12 | `resume` 的 `running_elsewhere` 前置检查**未落地**（`/web/api/sessions/resume` 无归属判定，全仓 Go 代码无该标识） | 同 D11：归属/互斥已由网格租约（`session-<sid>`）与 `spawn` 锁内二次检查覆盖；Web 端冲突弹窗属体验项 | Web 子方案 §0.1 / FR11 标注未落地；租约语义由 `lease_test.go` 覆盖（见 D9）；**S11 已收敛**（见 §19.4） |
| D13 | 架构 §9.4 / §9.5 承诺的 `--mesh-allow-nonloopback` 与 `--mesh-journal` 两个进程级开关**未落地**（§9.7 的 flag 表却已按存在列出），`mesh/self` 也无审计开关回显 | 「文档说 A、代码做 B」：收口切片 S19 补实现而**不改文档承诺**；同时把 P9/P10/P11 三条只跑过临时脚本的治理语义机器化 | 网格方案 §11 登记「两开关已落地」；mesh-e2e.md §5 追加 M11/M12；基线 03=16（见 §27.3） |

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
| E2E 场景表 / 参数分支 / 基线正则 | `scripts/test-aicli-e2e-all.ps1:108-131 / 236-245 / 138` | 既有核验（聚合入口与基线见 `docs/e2e/debug-guide.md` §5.1） |
| E2E harness 可复用函数（`Invoke-HarnessRequest` 等） | `scripts/aicli-e2e-harness.ps1` | 既有核验（见 `docs/e2e/harness-observability.md`） |
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
| S10 | `scripts/test-aicli-debug-endpoints-e2e-mesh.ps1` | `scripts/test-aicli-e2e-all.ps1`、`e2e-assertion-baseline.json` | M1–M10（S19 追加 M11/M12） | `summary.json` + 基线 diff |
| S11 | `web_handlers_mesh_sessions.go`、`web_handlers_mesh_sessions_test.go` | `web_handlers.go`、`web/js/sessions.js`、`web/js/ui.js`、`index.html`、`style.css` | Web §10.1 + 基线门禁 | 单测输出 + 手工验收 |
| S12 | `web_handlers_mesh_realtime_test.go` | `web/js/sessions.js`、`web/js/util.js`、`web/js/sse.js` | Web §10.2「实时徽标」+ 基线门禁 | 单测输出 + 手工验收 |
| S13 | `web_handlers_mesh_polish_test.go` | `web/js/sessions.js`、`web/js/chat.js` | Web §10.2（标题后缀 / refused 文案）+ 基线门禁 | 单测输出 + 手工验收 |
| S14 | `web_handlers_session_switch_test.go` | `web_handlers.go`、`web/js/sse.js`、`web/js/sessions.js` | Web §10.2（切换事件化）+ 基线门禁 | 单测输出 + 手工验收 |
| S15 | `internal/mesh/takeover_test.go`、`commands/mesh_takeover.go` | `internal/mesh/{host,spawn,spawn_exec,cli,registry,view}.go`、`commands/{chat_mesh,web_handlers,web_handlers_mesh_sessions}.go`、`web/js/sessions.js`、`index.html` | 网格 §4.4（接管）+ Web §6.3 + 基线门禁 | 单测输出 + 双进程手工验收 |

---

## 19. S11 · Web 侧收口（一）：sessions 便捷视图 + 前端徽标/分组/开关 + resume 冲突

> 来源：Web 子方案 §0.1 未落地清单（P0 ①③④、P1 ③④⑥；偏差 **D11 / D12**）。
> 实时项（P1 ②，`mesh/events` 前端订阅 + 退避重连 + 轮询兜底）拆为 **S12**，沿用同一模板；
> P2 治理项（接管 / 收敛开关文案 / `stop` / journal 查询 / `aicli mesh` 别名 / 冲突横幅）仍留后续。

### 19.1 范围与落点

| 交付项 | 落点 | 说明 |
| --- | --- | --- |
| P0 ① 侧栏徽标 + 端点行 | `web_handlers.go`、`web_handlers_mesh_sessions.go`（新增）、`web/js/sessions.js`、`web/style.css` | `sessions` 条目新增 `workspace_path/workspace_name/session_state/ownership/conflict_count/endpoint/last_known`；响应新增 `self`/`workspaces`。全部来自同一次 `mesh.BuildView`（Web 子方案 §0.2 纪律 2） |
| P0 ③ / P1 ⑥ 打开方式开关 | `web/js/sessions.js`、`web/index.html` | `localStorage: webSessionOpenMode`（`new_window` 默认，`in_place` 可选）；Q13：迁移只在用户未显式设置时生效 |
| P0 ④ 关于页网格小节 | `web/js/ui.js`、`web/index.html` | 只读展示 `node_id` / `mesh.root` / `counts` / 建议命令；**不提供** gc / stop / spawn 按钮（§5.8） |
| P1 ③ resume 冲突（D12） | `web_handlers.go`、`web/js/sessions.js`、`web/index.html` | `running_elsewhere` / `conflict` / `force:true`（§6.3）；判定复用 `internal/mesh` 归属结果；三段式弹窗 |
| P1 ④ 跨工作区分组 | `web/js/sessions.js`、`web/style.css` | 「其他工作区（N）」可折叠分组；数据来自 `?scope=all` 合并的 peer 会话条目（§5.3） |
| **不做**（留给 S12 / P2） | — | `mesh/events` 前端订阅（S12）；冲突横幅 / 接管 / 窗口标题后缀 / resume 事件化（P2） |

### 19.2 契约增量（相对 Web 子方案 §6.2/§6.3 的三点明确 + 一处新增）

1. **`?scope=all` 合并的 peer 会话条目**：`id/title` 取自节点档案 `session` 段；`created_at/updated_at`
   用 `activated_at` 顶替（仅展示）；`message_count=0`；`ownership=peer|conflict`；`endpoint` 仅活节点给出，
   `last_known` 来自该节点档案旁挂的 binding（视图已 join）。
2. **`workspaces[]`**：`path/name/nodes` 复用 `MeshView.Workspaces`；`session_count`（本进程列表按工作区计数）
   与 `running_count`（该工作区活节点且带会话）是**同一份视图 + 本进程列表**的派生，不新增聚合路径。
3. **`session_state` 折算**：活节点占用 → `running|busy`（含 peer）；无活节点且网格可用 → `idle`；
   网格不可用或档案不可读 → `unknown`（`endpoint=null`，`last_known` 仍尽力给出）。
4. **新增字段 `conflict_count`**（int，仅 `ownership=conflict` 时非 0）：徽标「⚠ 冲突（N 个节点）」需要 N；
   Web 子方案 §6.2 未列，登记为 S11 契约增量。

### 19.3 验证

| 层 | 断言 |
| --- | --- |
| 单测 | `sessions` 扩展结构（无活节点时 `endpoint=null` / `last_known=null` 稳定）；`sessions[].endpoint` 与 `peers.nodes[].endpoint` 同源一致；`scope=all` 去重合并；`resume` 的 `running_elsewhere` 不注入队列 / `force=true` 注入 / `conflict` 拒绝 / 网格关闭退回旧语义（回归锁定） |
| 前端契约 | asset 字符串断言（沿用 `web_handlers_mesh_spawn_test.go` 风格）：徽标、分组、开关、冲突弹窗、关于页小节的关键标识符 |
| 门禁 | `go test ./cmd/aicli/commands/ ./internal/mesh/`、`go vet`、E2E-DEBUG-01/02/03 基线（MN5） |
| 手工 | `docs/aicli/web-testing.md` §2.7（「实时徽标」仍标注依赖 S12） |

**降级契约（继承 §4.7）**：网格关闭 / 根不可读时 `sessions` 仍 200，`endpoint` 全 `null`、`self=null`、
`workspaces=[]`，前端回退到无徽标的现状视图；`resume` 不做归属检查（旧语义）。

### 19.4 落地记录（2026-09-24）

| 项 | 实际 |
| --- | --- |
| 后端 | 新增 `web_handlers_mesh_sessions.go`（`buildChatWebMeshSessionIndex` / `chatWebSessionsResponse` / `chatWebResumeMeshGuard`）；`web_handlers.go` 扩展 `chatWebSessionListItem` 7 字段 + 响应 `self`/`workspaces` + `?scope=all` 合并去重 + `resume` 的 `force` 前置检查 |
| 前端 | `web/js/sessions.js`（徽标 / 端点行 / 分组 / 打开方式开关 / 冲突弹窗）、`web/js/ui.js`（关于页只读小节）、`index.html`（`#sessions-open-mode`、冲突弹窗 DOM、`#about-mesh`）、`style.css`（对应样式，复用既有设计变量） |
| 单测 | `web_handlers_mesh_sessions_test.go`：`TestChatWebMeshSessionIndexHints`、`TestHandleChatWebAPISessions_ScopeAllMergesPeers`、`TestHandleChatWebAPISessionsResume_MeshOwnershipGuard`、`TestHandleChatWebAPISessionsResume_MeshDisabledKeepsLegacySemantics`、`TestChatWebSessionsAssetHasMeshOwnershipView`（asset 契约：徽标 / 分组 / 开关 / 弹窗 / 关于页 + 令牌红线） |
| 门禁 | `go build ./...`、`go vet ./cmd/aicli/commands/ ./internal/mesh/`、`go test ./cmd/aicli/commands/ ./internal/mesh/` 全绿（本地，2026-09-24） |
| 文档 | `web-remote-api.md` 新增 §9.7（字段表 + `scope=all` + resume 归属）；`web-testing.md` 新增 §2.7.1 手工清单；Web 子方案 §0.1 状态表回填；网格方案 §11.6 回填 D11/D12 收敛 |
| 契约增量 | 见 §19.2（三点明确 + `conflict_count` 新增）；打开方式**默认值**由子方案原写的 `in_place` 改为 `new_window`（Q13：仅对未显式设置过的用户生效，老用户选择被尊重） |
| 未做（按计划） | `mesh/events` 前端订阅（S12）；接管 `--takeover` 入口（`takeover_available` 恒 `false`）、冲突横幅、窗口标题节点后缀（P2） |

---

## 20. S12 · Web 侧收口（二）：`mesh/events` 实时徽标（退避重连 + 轮询兜底）

> 来源：Web 子方案 §0.1 未落地清单（**P1 ②**）；契约 = 子方案 §5.6 实时刷新表 +
> §10.2「实时徽标」断言。前端无构建步骤，落点全在 `web/js/*`
> （**D5**：不新增 `js/mesh.js`，网格前端逻辑并入 `sessions.js`）。
> 端点本身 S7 已就绪（`web-remote-api.md` §9.4），本切片只补**消费侧**。

### 20.1 范围与落点

| 项 | 落点 | 说明 |
| --- | --- | --- |
| P1 ② 实时订阅（§5.6） | `web/js/sessions.js` | 订阅 `GET /web/api/mesh/events`：与既有 `/web/api/events` **并列的第二条 SSE**（R11：独立连接、独立退避，互不拖累） |
| 退避重连 + 续传 | `web/js/sessions.js` | 1s→2s→4s…上限 30s；重连带 `?since_seq=<last_seq>`（游标取帧首 `id:` 行 / `data.seq`） |
| 轮询兜底（降级） | `web/js/sessions.js` | SSE 不可用（旧节点 / 非回环无令牌 / 代理阻断）→ 10s 轮询同源视图；`mesh.ready` 到达即停 |
| 节流 | `web/js/sessions.js` | 事件驱动 + 200ms 合并刷新，避免高频事件把侧栏重排打成幻灯片（Q11） |
| 令牌（非回环模式） | `web/js/util.js`、`web/js/sse.js` | 共享 `webAuthToken()`：回环模式 GET/SSE 无需令牌，非回环模式 EventSource 只能带 `?token=` |
| **不做**（留给 P2） | — | 冲突详情横幅 / 接管二次确认 / resume 的 SSE 事件化 / 窗口标题节点后缀 |

### 20.2 契约增量（三点明确）

1. **帧只作刷新信号**：`joined` / `left` / `updated` / `session.changed` / `peer.event`
   一律触发一次 `GET /web/api/sessions?scope=all` 全量重算——节点增删、分组计数、忙碌翻转、
   归属变化都由同源视图派生（§6.2），前端不保存增量、不自行合并 peer 帧，不产生第二套聚合口径。
2. **`mesh.lagged` = 立即全量兜底**：跳号即丢弃增量语义（§6.4），与「流不重放历史」的既有
   语义一致；不尝试按 seq 补洞。
3. **订阅门槛与降级**：仅当 `sessions` 响应 `self` 非空（网格可用）才订阅；`--mesh=false`
   不订阅、不轮询（路由本就不注册，订阅只会制造无意义重试）；SSE 不可用期间徽标仍可用
   （10s 轮询，只是不实时），页面不报错、不提示（§4.7 降级契约）。

**红线（继承 §5.5 / M7）**：网格前端逻辑不得出现令牌原文（统一经 `util.js::webAuthToken`），
peer 令牌依旧只出现在 `mesh/spawn` 返回的 URL 里、由服务端内联。

### 20.3 验证

| 层 | 断言 |
| --- | --- |
| 前端契约 | `web_handlers_mesh_realtime_test.go`：订阅端点 / §5.6 帧类型全覆盖 / 退避与轮询常量 / 节流常量 / 订阅门槛 / 令牌红线；助手单源（`sse.js` 与 `sessions.js` 都复用 `webAuthToken`） |
| 门禁 | `go build ./...`、`go vet ./cmd/aicli/commands/ ./internal/mesh/`、`go test ./cmd/aicli/commands/ ./internal/mesh/`、`node --check`（ES 模块语法） |
| E2E | 01/02/03 聚合回归（**流本身**由 M5 `mesh/realtime-fanin` 覆盖；浏览器行为按 §10.2 保持手工 + DevTools） |
| 手工 | `web-testing.md` §2.7「实时徽标」转正 + 新增 §2.7.2（实时 / 降级 / 双流独立） |

### 20.4 落地记录（2026-09-24）

| 项 | 实际 |
| --- | --- |
| 前端 | `web/js/sessions.js`：`meshEventsURL` / `maybeStartMeshStream` / `openMeshStream` / `scheduleMeshReconnect` / `startMeshPolling` + `stopMeshPolling` / `scheduleMeshRefresh` / `handleMeshFrame`；`applyMeshView` 末尾接订阅门槛（网格可用才启动） |
| 令牌单源 | `web/js/util.js` 新增 `webAuthToken()`（sessionStorage → 页面 meta，与骨架注入的 fetch 包装同序）；`web/js/sse.js` 改为复用之（行为不变，删掉重复的取值代码） |
| 单测 | `web_handlers_mesh_realtime_test.go`：`TestChatWebSessionsAssetHasMeshRealtimeView`、`TestChatWebAssetsShareSingleWebAuthToken` |
| 门禁 | `go build ./...`、`go vet ./cmd/aicli/commands/ ./internal/mesh/`、`go test ./cmd/aicli/commands/ ./internal/mesh/` 全绿；E2E-DEBUG-01/02/03 聚合回归全绿（本地，2026-09-24） |
| 文档 | 本节 + `web-remote-api.md` §9.4 补「前端消费口径」 + `web-testing.md` §2.7/§2.7.2 + Web 子方案 §0.1 状态表回填 |
| 未做（按计划） | P2 治理项：冲突详情横幅、接管二次确认、收敛开关文案、resume 的 SSE 事件化、窗口标题节点后缀 |

---

## 21. S13 · Web 侧收口（三）：P2 对账 + 窗口标题节点后缀 + spawn `refused` 文案

> 来源：Web 子方案 §0.1 未落地清单（**P2 ①–⑤**）的一次计划↔实现对账——原表写「P2 全部未落地」，
> 实测 **① 冲突详情横幅已随 S11 落地**，该行 stale。本切片落地其中两项**纯前端**打磨
> （③ 收敛开关 `refused` 文案、⑤ 窗口标题节点后缀）；② 接管与 ④ resume SSE 事件化
> （两者都含服务端面）留 S14+。

### 21.1 对账结论（2026-09-24）

| P2 项 | 对账结论 | 依据 |
| --- | --- | --- |
| ① 冲突详情横幅（节点列表 + 心跳） | ✅ 已随 S11 落地 | `conflict` 响应带 `nodes[]`（`node_id/pid/workspace/heartbeat_at`）+ `sessions.js::showSessionConflict` 渲染 `#session-conflict-nodes` |
| ② 接管二次确认（`takeover`） | ❌ 未落地 | 仅租约原语（`internal/mesh/lease.go` 的 `AcquireOptions.Takeover`）；Web 侧恒回 `takeover_available:false` 占位 + 单测锁定 |
| ③ 收敛开关 `refused` 文案 | ❌ → 本切片落地 | 前端原本没有任何 refused code 映射；`mesh_cross_workspace_denied` 只在 CLI/Agent 面的 `mesh/call` 上触发（前端不用 `call`，§6.1） |
| ④ resume SSE 事件化 | ❌ 未落地 | `sessions.js` 仍 8×300ms 轮询；`web_handlers.go` 注释（会投递 `session_end/session_start`）与前端注释（不发布）互相矛盾——S14 先实测再定方案 |
| ⑤ 窗口标题节点后缀 | ❌ → 本切片落地 | `chat.js::updateTitle` 原本输出固定标题，窗口并排时无法分辨节点归属 |

### 21.2 范围与落点

| 项 | 落点 | 说明 |
| --- | --- | --- |
| P2 ③ `refused` 文案（§5.2 回退路径） | `web/js/sessions.js` | `SPAWN_CODE_TEXT`：spawn 失败 code → 可执行文案（`mesh_spawn_not_allowed` / `mesh_disabled` / `mesh_nonloopback_denied` / `mesh_workspace_missing` / `mesh_cross_workspace_denied` / `mesh_spawn_timeout` / `mesh_spawn_failed`）；`refused` 追加 `aicli-mesh open <session> --print-url`，失败态追加 `aicli-mesh show <session>` |
| P2 ⑤ 标题节点后缀（§7.3） | `web/js/sessions.js`、`web/js/chat.js` | 新增 `meshNodeSuffix()`（`· <工作区> · <节点短 id>`；网格不可用时空串）；`updateTitle` 拼接；`applyMeshView` 在 self 段变化时重算（否则要等下一次状态翻转才出现） |
| **不做**（留 S14+） | — | ② 接管（需 CLI `--takeover` + Web 入口 + 租约回收 + `orphaned` 提示）；④ resume SSE 事件化（需先核对 runtime 事件流） |

### 21.3 验证

| 层 | 断言 |
| --- | --- |
| 前端契约 | `web_handlers_mesh_polish_test.go`：code 映射表全量、CLI 回退 / 诊断命令、调用点带会话 id（回退命令可复制）、`meshNodeSuffix` 实现 + 降级空串 + `applyMeshView` 重算 |
| 门禁 | `go build ./...`、`go vet ./cmd/aicli/commands/ ./internal/mesh/`、`go test ./cmd/aicli/commands/ ./internal/mesh/`、`node --check`（ES 模块语法） |
| E2E | 01/02/03 聚合回归（本切片不动端点与流，回归只作基线保护） |
| 手工 | `web-testing.md` §2.7.3（标题后缀 / 双窗口辨识 / refused 文案与 CLI 回退） |

### 21.4 落地记录（2026-09-24）

| 项 | 实际 |
| --- | --- |
| 前端 | `sessions.js`：`SPAWN_CODE_TEXT` + `spawnFailureText(json, id)`；`meshNodeSuffix()` + `applyMeshView` 触发 `updateTitle()`；`chat.js::updateTitle` 拼接后缀 |
| 单测 | `web_handlers_mesh_polish_test.go`：`TestChatWebSessionsAssetHasSpawnRefusalText`、`TestChatWebTitleHasMeshNodeSuffix` |
| 文档 | 本节 + Web 子方案 §0.1（P2 五行逐项对账）+ §5.2 / §7.3 / §9 状态标注 + `web-testing.md` §2.7.3 |
| 未做（按计划） | P2 ②（接管）/ ④（resume SSE 事件化）——留 S14+ |

---

## 22. S14 · resume/new 事件化（P2 ④）：去 8×300ms 轮询

> 来源：Web 子方案 §0.1 P2 ④「resume 的 SSE 事件化」。S13 特意留下一次**实测判定**
> 才敢动：`web_handlers.go` 的 resume 注释称「注入成功后 SSE 会继续投递
> session_end/session_start/screen_refresh」，而 `sessions.js` 的注释称「CLI 侧
> resume 不发布 session_end/session_start」。两者只能有一个对。

### 22.1 实测结论（2026-09-24，代码级判定）

| 断言 | 证据 | 结论 |
| --- | --- | --- |
| `session_start`/`session_end` 是 **turn 边界事件** | 唯一发布点 `internal/chat/actor.go:2707`（run 开始）/`:2951`（run 终态）；`/resume`、`/new`、`/load` 只切换当前会话、不产生 turn | 前端注释**正确**，resume 注释**stale** |
| `/resume` 后 SSE 流里没有可订阅的完成信号 | `chatWebSSEMappings`（`web_schema.go`）无会话切换类映射；`screen_refresh` 只在 `turn_end/session_end/session_interrupted/error` 后附带（`web_handlers.go:599`） | 「订阅既有事件」方案**不成立** |
| `current_session_id` 与看门狗同源 | `web_handlers.go:984` 与看门狗都读 `currentRuntimeSessionID(session)` | 合成事件到达时列表口径已就绪，无先后竞态 |

→ 采用**服务端补发**：SSE handler 自己盯会话身份变化，合成 `session_switched`。

### 22.2 范围与落点

| 项 | 落点 | 说明 |
| --- | --- | --- |
| 合成事件 | `web_handlers.go` | `chatWebSessionWatchInterval = 250ms` 看门狗 + 纯函数 `chatWebSessionSwitchNotice(previous, current)`（同一身份 / 当前会话缺失时不通知）；载荷 `{session_id, previous_session_id}` |
| 事件文档 | `web_handlers.go::chatWebSSESchema()` | `session_switched`（SourceEvent 为空 = 服务端合成），与 `connected/heartbeat/screen_refresh` 同类 |
| 前端订阅 | `web/js/sse.js` | 监听列表 + 与 `session_start/session_end` 共用刷新分支（`loadSessions` 覆盖网格视图/缓存与技能页签/会话身份），`session_switched` 额外调 `notifySessionSwitchedCompleted()` |
| 去轮询 | `web/js/sessions.js` | `proceedResumeSession` / `createNewSession` 删除 8×300ms 轮询；改为 `armSessionSwitchFallback(...)` 单次 4s 兜底（仅覆盖 SSE 断连）+ `notifySessionSwitchedCompleted()`（解禁新建按钮、终态提示、清兜底） |
| 注释纠偏 | `web_handlers.go`（resume handler doc） | 明确「注入只是排队、切换由主循环执行、不产生 turn」，与前端注释同源 |

**不做**（留 S15）：P2 ② 接管二次确认（CLI `--takeover` + Web 入口 + 租约回收 + `orphaned` 提示）。

### 22.3 验证

| 层 | 断言 |
| --- | --- |
| 服务端单测 | `web_handlers_session_switch_test.go`：`TestChatWebSessionSwitchNotice`（纯函数口径）、`TestHandleChatWebAPIEvents_EmitsSessionSwitchedOnSwitch`（真 SSE 流：身份变化 → 事件 + 前后 id）、`TestChatWebSSESchemaDocumentsSessionSwitched` |
| 前端契约 | 同文件 `TestChatWebSessionsAssetUsesSessionSwitchedEvent`：sse.js 订阅 + 分支 + 调用点；sessions.js 兜底存在且 `pollResumed`/`pollNew`/`setTimeout(..., 300)` 残留为零 |
| 门禁 | `go build ./...`、`go vet ./cmd/aicli/commands/ ./internal/mesh/`、`go test ./cmd/aicli/commands/ ./internal/mesh/`、`node --check`（ES 模块语法） |
| E2E | 01/02/03 聚合回归（本切片不动端点与流，回归只作基线保护） |
| 手工 | `web-testing.md` §2.7.4（切换即时刷新 / 终端发起的切换也刷新 / SSE 断连兜底 / 新建按钮恢复） |

### 22.4 落地记录（2026-09-24）

| 项 | 实际 |
| --- | --- |
| 服务端 | `web_handlers.go`：`chatWebSessionWatchInterval` + `chatWebSessionSwitchNotice` + SSE handler 看门狗分支 + schema 收录 + resume 注释纠偏 |
| 前端 | `web/js/sse.js`：监听 `session_switched` + 分支复用 + `notifySessionSwitchedCompleted` 调用；`web/js/sessions.js`：删两处轮询、加 `armSessionSwitchFallback` / `notifySessionSwitchedCompleted` |
| 单测 | `web_handlers_session_switch_test.go`（4 个） |
| 文档 | 本节 + §18 索引 + Web 子方案 §0.1 / §8.3 / §10.2 回填 + `web-testing.md` §2.7.4 |
| 未做（按计划） | P2 ②（接管）——留 S15 |

---

## 23. S15 · P2 ② 接管二次确认：`--takeover` + Web 入口 + 租约回收 + `orphaned` 提示

> 来源：Web 子方案 §0.1 P2 ②「接管二次确认」。S13 对账时把它留给 S15（见 §21.1 / §22.4）。
> 契约以网格方案 §4.4 为准：**接管是显式所有权转移，旧节点绝不被杀**——它在下一次心跳
> 发现自己不再持有租约，把档案的会话段标成 `orphaned`、发一帧
> `mesh.session.changed{event:"orphaned"}`、给操作者一条 warning，进程继续运行。

### 23.1 范围与落点

| 项 | 落点 | 说明 |
| --- | --- | --- |
| 数据模型 | `internal/mesh/registry.go`、`view.go` | `SessionInfo.Orphaned/OrphanedBy`；`SessionStateOrphaned = "orphaned"`；`buildNodeView` 在活节点上把状态改写为 `orphaned`（视图与档案同源） |
| 租约回收 | `internal/mesh/host.go` | `Host.TakeoverSession(sessionID)`：唯一会抢活租约的入口（`AcquireOptions{Takeover:true}`），返回 `{OK, Reclaimed, PreviousOwnerNodeID, Reason}`；单槽位模型下先让出本进程当前会话；本地档案若已 `orphaned` 则清回正常态 |
| 旧节点感知 | `internal/mesh/host.go` | 心跳 `Renew` 返回被抢 → journal `lease.degraded{holder_node_id}` + `markSessionOrphanedLocked`（幂等）：写档案、扇入 `peer.updated` + `session.changed{orphaned}`、warning 指名接管者 |
| 拉起 | `internal/mesh/spawn.go`、`spawn_exec.go`、`cli.go` | `SpawnRequest.Takeover` 跳过两处「复用活节点」短路；`AICLI_MESH_TAKEOVER=1` 随环境下发（陈旧值一律清掉）；接管忽略会话绑定端口（旧节点还占着）；`aicli-mesh open <会话> --takeover` |
| 子进程消费 | `commands/mesh_takeover.go`、`chat_mesh.go` | 首个会话激活读 `AICLI_MESH_TAKEOVER` 并**消费**（读到即清除，后续 /resume 不再抢租约）；接管失败只降级为一条诊断 |
| Web 入口 | `web_handlers.go`、`web_handlers_mesh_sessions.go` | `POST /web/api/sessions/resume {takeover:true}`：只在 `running_elsewhere` 上生效，先回收租约再注入队列，响应 `status=taken_over` + `previous_owner_node_id`；接管失败 → `takeover_failed`（不注入、不 5xx）；`takeover_available` 由常量 `false` 改为 `true` |
| 前端 | `web/js/sessions.js`、`web/index.html` | 冲突弹窗新增「接管并切换」按钮：第一次点击切到确认态，第二次才发 `{takeover:true}`；`conflict`（≥2 节点）时不显示；`orphaned` 徽标「⚠ 已让渡」+ 接管帧 toast |

**不做**（按计划）：`conflict` 分支的自动接管——≥2 个活节点声称同一会话时仍需人工判断（网格方案 §5.7）；
`orphaned` 节点的自动退出——网格绝不杀节点（网格方案 §4.4）。

### 23.2 验证

| 层 | 断言 |
| --- | --- |
| 租约单测 | `internal/mesh/takeover_test.go`：`TestHostTakeoverSessionReclaimsLeaseAndOrphansOwner`（接管 → 租约易主 + `lease.reclaimed` → 旧节点心跳标 `orphaned` + `lease.degraded` + warning + 视图 `state=orphaned` + 幂等不复活）、`TestHostTakeoverSessionRefusals`、`TestHostTakeoverSessionClearsOwnOrphanedRecord` |
| 拉起单测 | 同文件：`TestSpawnTakeoverStartsNewNodeAndSkipsReuse`（对照组复用 / 接管拉新进程 / 环境带标记 / 不抢旧端口 / 结果指向新窗口）、`TestResolveSpawnPortTakeoverIgnoresBinding`、`TestSpawnEnvForChildTakeoverMarker`；`cli_open_test.go::TestCLIOpenTakeoverFlag` |
| 进程侧单测 | `commands/mesh_takeover_test.go`：标记一次性消费（`ConsumesMarkerOnce` / `OnlyAcceptsTruthy` / 失败降级不 panic） |
| Web 单测 | `web_handlers_mesh_sessions_test.go`：`TestHandleChatWebAPISessionsResume_Takeover`（接管成功 → `taken_over` + 租约归本进程 + 入队；`conflict` 带 takeover 仍拒绝且不碰租约；`force` 不回收租约）；`takeover_available=true`；前端契约（按钮 id / `payload.takeover = true` / `takeover_failed`） |
| 门禁 | `go build ./...`、`go test ./internal/mesh/ ./cmd/aicli/commands/`、`gofmt -l` 空、`node --check`（ES 模块语法） |
| 手工 | 双进程：A 持有会话 → `aicli-mesh open <会话> --takeover` → A 的窗口出现「已让渡」徽标 + warning，B 的 URL 可用；Web 端冲突弹窗走二次确认 |

### 23.3 落地记录（2026-09-24）

| 项 | 实际 |
| --- | --- |
| 服务端 | `host.go`（`TakeoverSession` / `markSessionOrphanedLocked` / 接管者取名）、`spawn.go`（跳过复用 + 等待时排除旧节点 + 端口规则）、`spawn_exec.go`（`AICLI_MESH_TAKEOVER`）、`cli.go`（`--takeover` + 人读提示）、`registry.go` / `view.go`（`orphaned` 字段与状态） |
| 进程侧 | `commands/mesh_takeover.go`（一次性标记 + 失败诊断）、`chat_mesh.go::syncChatMeshSession` 接管钩子 |
| Web | `web_handlers.go`（请求字段 + 接管分支）、`web_handlers_mesh_sessions.go`（`takeover_available` + 接管执行 + `orphaned` 不算 claimant）、`web/js/sessions.js`、`web/index.html` |
| 单测 | `internal/mesh/takeover_test.go`（新建）、`cli_open_test.go`（+1）、`commands/mesh_takeover_test.go`（新建）、`web_handlers_mesh_sessions_test.go`（+1 与契约扩容） |
| 施工期发现（写测试时暴露，已修） | ① `waitForLiveNode` 会把「正被顶替的旧节点」当成结果返回 → 用户拿到的是旧窗口 URL；改为排除旧节点 id。② 接管标记只清环境变量、不清内存位 → 同一进程后续每次会话切换都会再抢一次租约；改为读到即清零 |
| 未做（按计划） | `conflict` 自动接管、`orphaned` 节点自动退出——见 §23.1「不做」 |

---

## 24. S16 · `stop` 治理动作（默认关闭）：CLI `stop` + `POST /web/api/mesh/stop`

> 来源：§0.2 注里的 P2 治理项「`stop`」。与 `gc` 同一口径：**默认关闭**——是否允许被
> 停由**目标进程**用 `--mesh-allow-stop` 显式开启，CLI / Web 都绕不过。

### 24.1 范围与落点

| 面 | 落点 |
| --- | --- |
| 服务端 | `internal/mesh/stop.go`（`StopNode` / `Stop`）、`call.go` 拆出 `ResolveTargetNode`（stop 只需可定位，不要求有端点）、`process_alive_{windows,unix}.go` 增 `terminateProcess(pid)` |
| 开关 | 节点侧 `--mesh-allow-stop`（默认 false）：`mesh_flags.go` / `mesh_flags_test.go` |
| CLI | `aicli-mesh stop <节点/会话> [--force] [--wait 30s] [--json]`；退出码 stopped=0 / not_found=2 / timeout=3 / refused=6 / 其它=5 |
| Web | `POST /web/api/mesh/stop`（回环 + `X-AICLI-Token` + 目标开关），端点清单与门户 schema 同步 |
| 文档 | `docs/aicli/mesh-cli.md`（速查 / §4.12 / §5 / §6.6 / §9）、`docs/aicli/web-remote-api.md`（§7.1 / §9.8） |

### 24.2 落地记录（2026-09-24）

| 项 | 实际 |
| --- | --- |
| 两种模式 | 默认优雅——把 `/exit` 投给目标的 `/web/api/input`，让目标自己收尾（保存会话、注销档案、释放租约）并等进程消失；`--force` 才直接终止进程（不做收尾，残留档案交给 `gc` 的「可证已死」） |
| 幂等 | 「目标已不在运行」按成功处理（`mesh_stop_already_stopped`）；等不到进程消失才是退出码 3（`mesh_stop_timeout`） |
| 自停保护 | 目标就是自己 → `mesh_stop_self_refused`；跨工作区仍受 `--mesh-restrict-workspace` 收敛 |
| 审计 | journal `mesh.stop.requested` / `mesh.stop.completed`（不记令牌） |
| 单测 | `internal/mesh/stop_test.go`（新建）、`commands/web_handlers_mesh_stop_test.go`（新建）、`mesh_flags_test.go`（+1）、`call.go` 目标解析回归 |
| 门禁补记 | 「CLI / Web 都绕不过」落到**两条路**上：目标 HTTP 层在进程内按 `--mesh-allow-stop` 判（403 + `mesh_stop_not_allowed`）；不经目标 HTTP 层的本地编排（`aicli-mesh stop` / `Host.Stop` / `StopNode`）按档案能力位 `CapabilityStop` 判（`refused` + `mesh_stop_not_allowed`，退出码 6）。能力位由目标进程在 `--mesh-allow-stop=true` 时写入档案（开放集，架构 §3.1；`withLoopbackCapabilities` 之后去重）。`--force` 与 graceful **同权**——force 不是绕开关的后门，只是免掉收尾；开关关闭时唯一手段是进程外 kill（`taskkill`）。单测：`TestStopRefusedWithoutStopSwitch`（两模式 + 零请求 + 不碰进程）/ `TestHostRecordAdvertisesStopCapability`（开关 → 档案投影） |

---

## 25. S17 · `watch` 事件流（P2 治理项）：CLI `watch`（journal tail）

> 来源：§0.2 注里的 P2 治理项「journal 查询」。与 `GET /web/api/mesh/events` 的分工：
> SSE 扇入要求双方都活着，`watch` 只读 `journal/*.ndjson`——**进程全退也能复盘**。

### 25.1 范围与落点

| 面 | 落点 |
| --- | --- |
| 服务端 | `internal/mesh/watch.go`（`JournalFiles` / `CollectJournalEvents` / `WatchJournal`）——`internal/mesh` 仍只用标准库，不新增节点侧依赖 |
| CLI | `aicli-mesh watch [--since 10m] [--node ID] [--session ID] [--once] [--limit N] [--interval 500ms] [--json] [--no-color]` |
| 契约 | 回放（`--once`）给 §7.3 稳定信封 `{schema_version, events[], counts}`；实时模式逐行 NDJSON（`--json` 的唯一例外，已在文档声明）；退出码：过滤目标完全不存在 = 2、窗口内为空 = 0、Ctrl-C = 0、读 journal / 写 stdout 失败 = 5 |
| 文档 | `docs/aicli/mesh-cli.md`（§1 / §2 / §4.13 / §5 / §6.7 / §9） |

### 25.2 验证

`go test ./internal/mesh/ -run 'Watch|CollectJournal' -count=1 -v` → 13/13 PASS；
全量 `go test ./internal/mesh/ -count=1` 绿（切片提交前在「仅 watch 变更」的工作区上复跑）。

### 25.3 落地记录（2026-09-24）

| 项 | 实际 |
| --- | --- |
| 窗口与过滤 | `--since` 默认 `10m`、`0` = 磁盘上的全部（含轮转代 `<node>.ndjson.1`）；`--node` / `--session` 前缀匹配（大小写不敏感），同时给出取交集 |
| 去重与合并 | 按 `(node_id, seq)` 去重（seq 进程内单调，进程重启即新 node_id，所以「轮转代 + 活动文件」一起回放不重复）；回放按 `ts, node_id, seq` 稳定排序，人读输出每行带 `[节点 ID]` 前缀 |
| 半行与轮转 | writer 正在追加、还没有换行符的尾行**留到下一次轮询**（不消费，否则那条事件永久丢失）；轮转 / 截断按新文件从 0 重新读 |
| `--limit` | 只作用于回放，保留**最新** N 条；尾随阶段不截断 |
| 空结果语义 | 过滤目标在磁盘上完全不存在 → 2（含一次全量探测）；目标存在但窗口内没有事件 → 0 + stderr 提示（长跑进程上很常见，不该当失败） |
| 单测 | `internal/mesh/watch_test.go`（新建，13 例：合并 + since / 节点与会话过滤 / 轮转代只读一次 / limit 取最新 / follow 追加与半行 / `--once` 不尾随 / 路径不可用 fail-closed / CLI JSON 信封 / 人读前缀 / 用法错误 / 目标不存在 / 空窗口成功 / help） |
| 施工期发现 | 实时 + `--json` 若坚持套信封，就必须把事件全缓在内存里等一个永不到来的收尾 → 改为逐行 NDJSON，并在 §6.7 显式标注为信封约定的唯一例外 |

---

## 26. S18 · `aicli mesh` 别名（P2 治理项）：与独立二进制共用一套 CLI

> 来源：§0.2 注里的 P2 治理项「`aicli mesh` 别名」+ 架构 §7.5 / Q10 的结论：**做**——
> 但复用 `internal/mesh.CLI`，不写第二套实现（否则用法文本、退出码、JSON 契约会分叉）。

### 26.1 范围与落点

| 面 | 落点 |
| --- | --- |
| 别名 | `backend/cmd/aicli/commands/mesh_command.go`（新建）：`NewMeshCommand` 只做两件事——**参数透传**、**退出码透传** |
| 注册 | `backend/cmd/aicli/main.go`（`rootCmd.AddCommand(commands.NewMeshCommand())`，与 `replay` 相邻） |
| 透传机制 | `DisableFlagParsing: true`：网格旗标（`--since` / `--limit` / `--once` / `--no-color` …）的语义与 cobra 无关，在这里再声明一遍等于维护第二份真相 |
| 退出码 | 非 0 先 `runExitCleanup()` 再 `os.Exit`（与 `export.go` 的 `exportExitHook` 同惯例）：2 / 3 / 4 / 6 原样返回，cobra 的「一切皆 1」兜底路径被完全绕开 |
| 版本 | `mesh.CLI{Version: chatStatusVersion}`：`aicli mesh version` 报宿主 aicli 的构建版本（main 注入），不是 `mesh.CLIVersion` 的 `dev` 兜底 |
| 文档 | `docs/aicli/mesh-cli.md`（§1 / §2 / §12）、`docs/aicli/README.md`（条目补别名） |

### 26.2 验证

`go build ./cmd/...`、`go vet ./cmd/aicli/commands/` 通过；
`go test ./cmd/aicli/commands/ -run 'TestMeshAlias' -count=1 -v` → 6/6 PASS（2026-09-24，本地）。

### 26.3 落地记录（2026-09-24）

| 项 | 实际 |
| --- | --- |
| 不做第二套 | 别名里没有任何网格逻辑：旗标解析、渲染、JSON 信封、退出码全部来自 `internal/mesh.CLI` |
| 帮助文本 | 仍以 `aicli-mesh` 为名（同一份用法）：别名不复制、不改写用法文本；`aicli mesh --help` 打印的就是 `aicli-mesh --help` 那份（退出码 0） |
| 不是节点 | `meshNodeCommands` 只含 `chat` / `resume`，所以 `aicli mesh ...` 不写档案、不占租约、不出现在 `ls` 里 |
| 单测 | `commands/mesh_command_test.go`（新建，6 例）：`ls --json` 信封与 `schema_version` / `watch --once --since 1h --json` 旗标原样透传 / `show` 目标不存在 → 2 / 未知子命令 → 1 且 stderr 说明 / `--help` → 0 且列出子命令 / `version --json` 带宿主版本 |
| 退出码的测试手法 | 注入 `meshAliasExitHook`（生产为 `os.Exit`），与 `exportExitHook` 同一惯例——测试能断言 1 / 2 这类非 0 码而不真的结束测试进程 |
| 未做（按计划） | 别名不提供独立二进制的安装 / 出包路径（`build.ps1 -Tools aicli-mesh` 不变）；帮助文本不按调用名改写（避免第二份用法） |

---

## 27. S19 · 治理开关收口（P9/P10/P11）：`--mesh-allow-nonloopback` / `--mesh-journal` + E2E M11/M12

> 来源：架构 §9.4 / §9.5 承诺的两个进程级开关，在 §9.7 的 flag 表里已按存在列出，
> 但源码里没有（D13）；手工验证 P9/P10/P11 又表明这三条治理语义只在临时脚本里被
> 摸过、没进回归。本切片一次收口：**补实现**（保留文档承诺）+ **机器化**
> （进 E2E-DEBUG-03，断言只增不减）。

### 27.1 范围与落点

| 面 | 落点 |
| --- | --- |
| 开关 | `cmd/aicli/mesh_flags.go`：`--mesh-allow-nonloopback`（默认**关**）、`--mesh-journal`（默认**开**），与 `--mesh-restrict-workspace` / `--mesh-allow-spawn` / `--mesh-allow-stop` 同族，统一由 `applyMeshGovernanceFlags` 落状态 |
| 非回环判定 | `commands/web_handlers_mesh.go::chatWebMeshWritePathAllowed`：`chatWebRequestIsLoopback`（= 进程处于回环模式 **且** 客户端回环）不成立时只有显式开关才放行；`call` / `spawn` / `stop` 三个 handler 共用同一判定（一处口径，不各写一套） |
| 审计降级 | `internal/mesh/host.go`（`HostConfig.JournalDisabled`）→ `internal/mesh/journal.go`（`JournalOptions.Disabled`）：`Append` 静默丢弃、**seq 照常分配**（扇入与 SSE 的 seq 同源，不受影响） |
| 回显 | `mesh/self` 新增 `mesh.journal_enabled`：如实自述审计开关，消费方不必靠猜 |
| E2E | M11（`mesh/nonloopback-default-deny` + `mesh/nonloopback-cli-parity`；HTTP 侧 call 与 stop 两个端点各探一次）、M12（`mesh/journal-disabled`）——03 断言 13 → 16 |
| 文档 | mesh-e2e.md（§5/§6/§7/§8）、网格方案 §11 登记、本表 |

### 27.2 验证

- 单测：`web_handlers_mesh_nonloopback_test.go`（默认拒绝 / 逃生门放行两条路径）、
  `mesh_flags_governance_p2_test.go`（五个开关的注册、默认值与落状态）、
  `journal_disabled_test.go`（seq 仍分配、不落盘）。
- E2E：`pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-mesh.ps1`
  （M11/M12 与 M1–M10 同跑，退出码 0 才算过）。
- 基线：`pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -BaselineOnly -UpdateBaseline`
  （只校验断言名、不跑场景）→ 03 由 13 固化为 16。
- 固化验证（2026-09-24）：E2E-DEBUG-03 单跑 16/16 绿（`artifacts/mesh-e2e-m11m12-r3/`）——
  `mesh/nonloopback-default-deny`：HTTP call 与 stop 均 `403 + refused + mesh_nonloopback_denied`、
  同一节点 `/web/api/status` 仍 200；`mesh/nonloopback-cli-parity`：exit 6 + 同一原因码；
  `mesh/journal-disabled`：B4 回显 false / A true、B4 无 journal 文件、`watch --once` 无其事件、call/gc 照常。
  聚合 01 → 02 → 03 全绿（`PASS=6 FAIL=0`，`artifacts/aicli-e2e-all/20260924-205713/`，01=41 / 02=28 / 03=16）。

### 27.3 落地记录（2026-09-24）

| 项 | 实际 |
| --- | --- |
| 偏差性质 | 两个开关属「文档已承诺、代码缺失」（D13）；修法是补实现而不是改文档承诺——flag 名与默认值照 §9.7 / §10 的表 |
| 默认拒绝不是「只拦非回环客户端」 | `chatWebRequestIsLoopback` 先看**进程是否回环模式**：`--web-host 0.0.0.0` 下整机退出网格写路径，连回环客户端也拒（M11 的探针正是打这一点）；逃生门打开后令牌、逐次 `allow_write`、`--mesh-allow-stop` 的要求全部照旧 |
| 逃生门不进 E2E | M11 只锁「默认拒绝」这条红线；`--mesh-allow-nonloopback=true` 的放行路径由 Go 单测覆盖（E2E 不为图方便打开跨机开关，与 mesh-e2e.md §8 安全红线一致） |
| journal 关闭的语义边界 | 只丢审计：不写 `journal/<node>.ndjson`，`seq` 仍分配（否则扇入与 SSE 的序号会断档）；`watch --once` 退化为「只看别人的日志」且不报错；`call` / `gc` 照常（M12 逐条断言） |
| 对照断言 | M12 用 A（默认节点）的 `journal_enabled=true` 与 A 的 journal 行数 ≥1 作对照，防止「回显写死 false」也能蒙混过关 |
| 清单门禁顺带收口 | 03 的端点覆盖门禁此前把 `/web/api/mesh/stop` 留在「清单有、断言无」：M11 现在也在非回环模式下探 stop（B3 带 `--mesh-allow-stop=true` 只为越过开关检查，探针 target 指向 B3 自己，判定若被绕过也只会 `mesh_stop_self_refused`），该端点随之进 `$asserted` 而不再需要豁免 |
| 档案读取时序 | 节点档案**首写可能还没有 `endpoint` 段**（回环地址解析/落盘晚于首写，非回环监听下实测更明显）：`Wait-RecordByPid -RequireEndpoint` 等到 `endpoint.port > 0` 再交还调用方（端口仍只从档案读）；M11 探针同时带档案里的 `X-AICLI-Token`，测的是「有令牌也拒」而不是被 401 挡在门外 |
| 基线 | `scripts/e2e-assertion-baseline.json` 03：13 → 16（新增 `mesh/journal-disabled`、`mesh/nonloopback-cli-parity`、`mesh/nonloopback-default-deny`），01=40 / 02=31 未动 |
| 未做 | 非回环**放行**的端到端场景：E2E-DEBUG-02 已覆盖非回环鉴权，网格写路径的放行留给单测；将来确有需要再开场景 |

---

## 28. S20 · `new`：CLI 新建会话（`aicli-mesh new`，CLI 专用）

> 来源：运维日常缺一条「新建会话」路径——`open` 以**会话为键**（复用活节点 / 拉起既有会话），
> 要开一个**全新**会话只能手工 `aicli chat`，再自己去 `ls` 里翻端口与令牌。本切片把它补成
> `mesh.Spawn` 的第二个入口：**会话 ID 由子进程生成**，父进程按 pid 等档案读回来。

### 28.1 范围与落点

| 面 | 落点 |
| --- | --- |
| 服务端 | `internal/mesh/spawn.go`：`SpawnRequest.NewSession` + `spawnNewSession` / `spawnNewArgs` / `liveNodeForPID`；启动与就绪链路复用 `launchAndAwait` |
| CLI | `internal/mesh/cli.go`：`aicli-mesh new [--workspace PATH] [--port N] [--wait 8s] [--no-wait] [--bin PATH] [--json]`；退出码 started=0 / not_running=3 / failed=5 |
| Web | **无端点**：`POST /web/api/mesh/spawn` 仍只处理既有会话（`session_id` 必填），新会话入口只有 CLI |
| 不变量 | 没有会话 ID 可复用、也不抢租约（`spawn-<会话>.lock` 无键可建）——CLI 依旧不写档案、不写绑定、不占租约，档案由子进程自己写 |
| 文档 | `docs/aicli/mesh-cli.md`（§1 / §2 / §4.11 / §6.5 / §8 / §9 / §10 / §12）、`docs/aicli-mesh/`（README / quickstart / spawn-and-binaries / troubleshooting）、`docs/aicli/web-remote-api.md` §9.6、`docs/e2e/mesh-e2e.md` §3 |

### 28.2 验证

- 单测：`internal/mesh/spawn_new_test.go`（新建、不占租约、从不复用、拒绝 `SessionID`、
  工作区三种失败、`--no-wait`、超时、fail-closed）、`internal/mesh/cli_new_test.go`
  （请求翻译 / JSON 形状 / 默认工作区 / 退出码 / 参数错误 / `--help`）；
  `go test ./internal/mesh/ -count=1` 全绿。
- 端到端（2026-09-24，本地隔离 lab）：`new --workspace <lab>\ws --bin <child> --wait 25s --json`
  → 退出码 0，返回新会话 ID / 节点 / 端口 / 带令牌窗口 URL（约 1s 就绪）；网格目录只有子进程
  自己的档案、绑定与 `session-<sid>.lock`（**无 `spawn-*.lock`**）；同工作区连跑两次得到
  **不同**会话（从不复用）；随后 `call node.info` → 200。
- 失败面：`--workspace` 不存在 / 是文件 → 退出码 1；网格根不可用 → `mesh_disabled`；
  等不到新会话档案 → `not_running` + `mesh_spawn_timeout`（带脱敏 `log_tail`）。

### 28.3 落地记录（2026-09-24）

| 项 | 实际 |
| --- | --- |
| 就绪判定 | 按 **pid** 等「属于该 pid 且 `session_id` 非空」的 live 档案：子进程先写档案、会话建好后再补写一次，等的就是第二笔；pid 取自 `exec.Command.Start()`（无 shell 包装），与档案里的 `os.Getpid()` 同源 |
| 为什么不用 `resume <id>` | `aicli resume <id>` 对**没有存档的会话**会失败（`internal/chat.Manager.Get`）；新会话只能走 `chat`（不带 `--session`） |
| `--no-wait` 的诚实性 | 会话 ID 还没生成 → `session_id` 留空、`url` 不带 `session=`，只报 pid / 端口，`reason` 指向 `aicli-mesh ls` 自查 |
| 日志键 | `new-<UTC 时间戳>-<pid>`：两次 `new` 不共用启动日志（`open` 按会话键，天然唯一） |
| 工作区 | CLI 侧 `--workspace` 缺省 = 当前目录，且**必须已存在**（用法错误 1 当场拒绝）；Spawn 层对「目录在启动前被删」再查一次 → `mesh_workspace_missing` |
| 文档编号 | mesh-cli.md 的 §4 子命令按治理顺序插入 `new`（§4.11），`stop` / `watch` 顺延为 §4.12 / §4.13；相关交叉引用（web-remote-api.md §9.8、本计划 §24/§25 的文档行）同步更新 |
| 未做 | Web 端点（`POST /web/api/mesh/new` 之类）与前端「新建会话」按钮：本切片只补 CLI；确有需要时按 `spawnNewSession` 再包一层 handler |

---

## 附录：本文与三份基准文档的分工

| 文档 | 回答的问题 | 何时看 |
|------|-----------|--------|
| `aicli-mesh-architecture.md` | 为什么这样设计 / 契约是什么 | 施工前通读；争议时以它为准 |
| **本文** | 按什么顺序做 / 改哪些文件 / 怎么验证 / 怎么回滚 | 施工全程 |
| `aicli-micro-web-client-session-window-plan.md` | Web 侧交互与前端落点 | S5 / S9 前 |
| `docs/e2e/mesh-e2e.md` §5 | 多进程 E2E 怎么断言 | S10 前 |

> 变更记录：2026-09-24 初版（S1–S10 + 验收 / 回滚 / 锚点核验）；
> 2026-09-24 追加 §19（S11 · Web 侧收口一）与 §19.4 落地记录（sessions 便捷视图 + 前端徽标/分组/开关 + resume 冲突）。
> 2026-09-24 追加 §23（S15 · 接管二次确认：CLI `--takeover` / Web 入口 / 租约回收 / `orphaned` 提示）与 §23.3 落地记录。
> 2026-09-24 追加 §28（S20 · `new`：CLI 新建会话）与 §28.3 落地记录。
> 每完成一个切片，在 §15.3 登记实际偏差，并回填网格方案 §11.6。
