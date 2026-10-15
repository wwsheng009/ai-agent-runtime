# 调度面 mesh 化：联盟式调度与宿主承接设计（Federation）

> 目录：`docs/automation-tasks/`
> 前置文档：[`scheduler-daemon-design.md`](./scheduler-daemon-design.md)、[`embedded-execution-design.md`](./embedded-execution-design.md)、[`runtimehost-executor-extraction-plan.md`](./runtimehost-executor-extraction-plan.md)、[`aicli-executor-reuse-design.md`](./aicli-executor-reuse-design.md)、[`../bots/09-multi-instance-binding.md`](../bots/09-multi-instance-binding.md)
> 状态：方案 v1.1（待评审；含 §12 完整性审查修订） ｜ 日期：2026-10-14
> 一句话结论：把"调度面 = 独立守护进程"泛化为"**调度面 = mesh 可抢占角色**"——任一具备 `scheduler` 能力的在线节点（daemon / runtime-server / aicli）持 mesh 租约成为调度者，投递按"就近优先"路由，daemon 收缩为默认常驻成员与无宿主场景的兜底。

---

## 1. 背景：一次真实事故的复盘

### 1.1 事故经过

| 时间（本地） | 事件 |
|---|---|
| 21:38:42 | 会话 `session_20261014140934_bgvm9eLR`（微信接入）中模型调用 `schedule_create(after_seconds=300)`，创建任务 `schedule-4753cce5-…`，`scheduled_at=21:43:42` |
| 21:43:42 | 到期时间已过，**没有任何投递发生** |
| 事后 | 任务仍 `status=active`，`last_scheduled_at/last_delivered_at=NULL`，无 pending 兜底、无 job_runs；`schedule.sqlite-wal` 的最后写入时间停在创建时刻 |

### 1.2 三层根因（逐层暴露）

1. **直接原因：调度者不在岗。** 唯一持定时器的 `cmd/scheduled` 守护进程没有运行：无进程、无 Windows 服务、8919 状态端口无监听、mesh 中无 `schedule-scheduler` 租约。宿主侧（runtime-server / aicli）按设计只做控制面——`getScheduleService` 只挂载 drain 运行时（会话激活时补投 pending），**不持定时器**——于是到点无人扫描。
2. **二级原因：版本不兼容且静默。** 现有 `backend\scheduled.exe`（2026-10-13 构建）只支持 schedule store schema v3，而共享库已被新版宿主迁移到 **v5**（v4=J0–J4 作业字段、v5=created_by_session_id + job_runs）。`scheduled.exe doctor` 实测：`schema is newer than this binary … refusing to open` → 即使手动启动也会 fail-closed 退出。
3. **三级风险：默认路由打不通。** 工作区配置 `.aicli/runtime.yaml` 只写了 `schedule.enabled: true`，未写 `execution.*` → 默认 `mode=delegate` 且 `runtimeServerURL` 为空。即使 daemon 在位，delegate 也必然失败，只能落 mailbox（下次会话激活才补投，非准时）。

### 1.3 事故暴露的架构问题

| # | 问题 | 说明 |
|---|---|---|
| P1 | **可用性单点**：调度是"专用进程 + 手工运维" | "宿主不持定时器"的原始决策（避免多实例重复投递、避免任务依赖交互进程）把触发能力锁进了另一个需要用户记得部署的进程里；而宿主机（runtime-server / aicli）恰恰是最常驻、最不容易被忘记的进程 |
| P2 | **可验证性缺失**：没有任何角色回答"现在谁在调度 / 能不能跑" | 创建时、UI 里、日志里都没有"调度者在岗 + 版本兼容"的信号，失败对用户完全静默 |
| P3 | **路由僵化**：只有 HTTP delegate + mailbox 两条路 | 宿主明明在线（会话 actor 就在本机），却因 URL 未配置走不通；跨机与同机没有分层 |

---

## 2. 现状盘点：可复用资产（本方案不新造轮子）

### 2.1 mesh 原语与 schedule 需求的对应关系

| mesh 能力（代码位置） | 现成语义 | 对 schedule 的用途 |
|---|---|---|
| `mesh.Host` / `HostConfig`（`backend/internal/mesh/host.go`） | 节点档案：`kind`、`capabilities[]`、`version/build_time`、endpoint、心跳（默认 30s，TTL 90s）；`HasCapability()` 帮助函数；`CapabilityStop` 是"能力位先于行为"的先例 | 调度者候选池与能力位：新增 `scheduler` 位即可发现"谁有资格持定时器" |
| `mesh.Lease`（`lease.go`）：Acquire / Renew / Release / ReadLease，TTL 30s | 单实例租约；`cmd/scheduled/lease.go` 已在用（purpose=`schedule-scheduler`，key=`global`；失租即停；被他人持有时待命退出；mesh 根不可用则带警告降级） | 调度领导权：把"只有 daemon 抢"改为"任何 `scheduler` 节点抢" |
| `mesh.Binding`（`binding.go`）+ `BuildView`（`view.go`） | 会话偏好记录（`LastNodeID`/workspace）；视图聚合 `ownership=owner/peer/conflict/none` 与 `orphaned` 会话状态 | 会话亲和路由：投递前知道"该会话现在挂在哪个进程"，以及绑定节点是否掉线 |
| `mesh.Call`（`call.go`）：9 项 op 白名单（`node.info`/`status`/`screen`/`turn`/`sessions.list`/`invoke`/`input`/`cancel`/`sessions.resume`），写操作显式 `allow_write`，跨工作区拒绝，loopback+token 鉴权；`CallCodeSchemaTooNew` 已有"目标档案 schema 不兼容即早失败"的先例 | 跨节点投递/触发：同机多进程之间的执行路由与安全约束全部现成 |
| `mesh.Spawn` / `aicli-mesh` CLI | 在正确 workspace 拉起某会话的节点 | 目标会话所在宿主人去楼空时，按需拉起（可选增强） |

### 2.2 先例：bots 多实例派发已经跑通同构问题

`docs/bots/09-multi-instance-binding.md` 把"谁承接一个 turn"完整解过一遍，且已于 2026-10-14 落地：

- 节点能力位：aicli = `["mesh","runner"]`（`backend/cmd/aicli/mesh_bootstrap.go`），runtime-server = `["mesh","runner","poller"]`（`backend/cmd/runtime-server/mesh_bootstrap.go`，P2 #8 已入网写档）；
- Runner 路由：**R1 绑定优先**（有 binding → 绑定节点执行）→ **R2a 绑定节点掉线 → 接管 failover**（切片 C：接管节点继续服务，成功后回写 `binding.NodeID` 并提示一次）→ **R3 无绑定按 `runner_preference` 选举**（默认 runtime-server 优先 → aicli → round-robin）；
- 唯一性与幂等：poller 归属（owners + 心跳）保证同一 connector 只有一个轮询者；`claimed` 键做入站去重；派发体携带 `message_id` 供 runner 二次校验；
- 崩溃自愈：poller 崩溃 90s 后任一宿主按规则接管；runner 崩溃按 R2a/R2b 处理；state 共享走文件系统。

**结论：schedule 联盟化不需要发明新机制，只需要复刻这套"能力位 + 绑定亲和 + 选举 + 心跳接管 + 幂等"的模式，并补齐本方案 §4 的四个缺口。**

### 2.3 schedule 现状：哪些用了、哪些空着

| 组件 | 现状 | 差距 |
|---|---|---|
| 存储 `internal/schedule/store.go` | schema v5；`schedule_tasks` 已有 `owner_instance_id` / `lease_expires_at` 两列与索引 `idx_schedule_lease` | 两列**从未被 UPDATE 消费**（只在建表/INSERT 默认值出现）——任务级 claim 是预留但未落地 |
| 调度运行时 `internal/schedule/runtime.go` | 精确 timer（按最近目标排定）、启动扫描、模式 `delegate/embedded/auto`、内嵌执行（E0/E1）、mailbox 兜底、结果回投创建者、job_runs 历史、metrics | 只能被 daemon "整进程独占"启动一次；无法被多个宿主共同持有 |
| 领导权 `cmd/scheduled/lease.go` | mesh 租约 + 续租 + 失租停止 + 降级警告 | 逻辑写在 `cmd/scheduled`（main 包）里，其他宿主无法复用；参与者集合固定为"daemon 单方" |
| 投递 `runtime.go` + `runtimeapi/schedule_host.go` | ① HTTP delegate（`submit_prompt` → runtime-server 会话 actor）；② 进程内 `scheduleSessionDeliverer`（`SubmitPromptAsync`，目前只用于 drain）；③ mailbox pending | delegate 是唯一"主动"路径且依赖 `runtimeServerURL` 配置；"宿主在线但本机即可投递"的就近路径没有被主动扫描使用 |
| 执行核 | `runtimehost.Executor` 已抽取（E3.3）；daemon 用 `handler.Executor()` 复用同一核；aicli 侧有 E3.4 复用设计 | 宿主（runtime-server 已具备；aicli 按 E3.4 接入）完全可以承接执行，但没有"谁来调度"的角色层 |
| 观测 | daemon `/status`（127.0.0.1:8919）暴露 metrics；`Runtime.Metrics()` | 没有"当前调度者是谁、租约何时到期、创建时是否有调度者在岗"的任何信号 |

---

## 3. 目标形态：联盟式调度（Federation）

### 3.1 角色模型：driver / deliverer / executor 三层解耦

把"调度"拆成三个可独立归属的角色，任何一层都不再绑定某个专属进程：

```
┌────────────────────────────────────────────────────────────────┐
│ ① driver（谁持定时器）                                          │
│    持 mesh 租约（capability=scheduler）的节点，跑 schedule.Runtime │
│    候选（按优先级）：daemon > runtime-server > aicli(opt-in)      │
│    到点 → 生成投递批次 → 交给 deliverer                           │
├────────────────────────────────────────────────────────────────┤
│ ② deliverer（怎么送到）                                          │
│    L1 本进程 session hub：SubmitPromptAsync（零网络，最优）      │
│    L2 目标宿主节点：binding/view → mesh.Call（同机跨进程）        │
│    L3 HTTP delegate：runtime-serverURL（跨机/远端，保留）        │
│    L4 embedded 自执行：空闲会话；活跃会话由 session 租约保护不抢占  │
│    L5 mailbox pending：兜底（现状语义不变）                       │
├────────────────────────────────────────────────────────────────┤
│ ③ executor（谁来跑 turn）                                        │
│    session 目标：会话宿主进程内执行（actor 本就在那）             │
│    headless/workspace：任一具备 runtimehost.Executor 的宿主       │
│    无人值守：dont_ask 权限模式（现状不变）                        │
└────────────────────────────────────────────────────────────────┘
```

与现状的本质差别：今天"driver = 独立进程"是硬绑定；本方案"driver = 持租约的角色"——**进程只是角色的载体**。

### 3.2 领导权：mesh 租约角色化

- **租约**：沿用 `cmd/scheduled/lease.go` 的语义（purpose=`schedule-scheduler`，key=`global`，TTL 30s，续租 TTL/3，失租即停，避免双投递），但把它从 `cmd/scheduled`（main 包）抽成 `internal/schedule`（或新包 `internal/schedulehost`）的可复用 `Leader` 组件。
- **参与者与优先级**：配置 `schedule.driver.participants`（默认 `[daemon, runtime-server]`，aicli 为 opt-in）。两种落地形态：
  - v1（先到先得）：只做"参与者资格过滤"——非参与者不抢租约；谁先抢到谁当值。daemon 服务化常驻后天然长期在位。
  - v2（优先级让位，推荐终态）：租约记录携带优先级/权重；低优先级持有者周期检查视图，若存在"更高优先级且心跳稳定"的候选则主动 Release 转入 standby，高优先级节点下一轮接管。防抖：候选需持续在线 ≥ 稳定窗口（如 60s）才触发让位。
- **接管与空窗**：leader 崩溃 → TTL 30s 内任何合格节点 Acquire 成功接管，接管后立即 `RequestDrive()`（Runtime 启动即扫描 overdue，语义与今天"启动补发"一致）。
- **降级策略（关键决策）**：mesh 根不可用时，
  - daemon：保持现状"带警告、无互斥运行"（单守护进程场景可接受）；
  - 宿主（runtime-server / aicli）：**默认 standby（fail-closed）**，不参战——不能确定是否有其他调度者时宁可不动，由配置 `schedule.driver.degraded: standalone` 显式打开单机自跑。理由：多宿主共存的机器上，"无互斥自跑"意味着双投递风险。
- **触发时机**：Runtime 本就是精确 timer（按最近目标排定 + `maxTimerDelay=1h` 分段；不是轮询），另加两路唤醒：`schedule.changed` 事件 → `RequestDrive()`（已有事件通道），以及 leader 易主时立即 drive。

### 3.3 投递路由：就近优先（L1→L5）

对 `target_kind=session` 的到期批次，driver 按序尝试：

1. **L1 本进程 hub**：driver 就是会话宿主时，直接走 `scheduleSessionDeliverer`（`SubmitPromptAsync`，`runtimeapi/schedule_host.go` 已有实现，只是目前只挂给 drain）——零网络、零配置、被 actor 接受即回执；
2. **L2 目标宿主节点**：经 mesh 视图（binding / ownership）解析该会话的在线宿主，`mesh.Call` 投递。建议新增白名单 op `schedule.deliver`（write=true，走既有 token/loopback/跨工作区纪律），携带幂等键 `client_request_id = occurrence_key`（= `schedule_id + occurrence_at`，§12.1；mesh 层原样透传，由目标端点/接收侧幂等去重——与 `invoke` 同约定）；**前置校验与降级**：归属 `conflict`（§12.2.2）、跨工作区（M6）、无回环 endpoint（M7）时跳过 L2 进入下一级，不得盲投；
3. **L3 HTTP delegate**：`runtimeServerURL` 配置存在且可达时使用（跨机/远端场景保留，不再是同机唯一路径）；
4. **L4 embedded 自执行**：以上都失败时按既有 E0/E1 语义自跑 turn（先经 session 租约仲裁，活跃会话绝不抢占；`dont_ask` 权限预置拒绝）；
5. **L5 mailbox**：仍失败则落持久 pending（`fallbackToMailbox=true` 默认），宿主恢复/会话激活时由既有 `drainSchedulePending` 补投。

`headless/workspace` 目标不经过 L1–L3，直接找 executor（§3.4）或由 daemon 处理。

### 3.4 执行承接：宿主完全有能力

- **session 目标**：执行体就是会话宿主的 actor——L1/L2 投递后由宿主正常跑 turn，与用户发一条消息的路径完全同源（工具面、权限、上下文都一致）。这消灭了"daemon 必须绕 HTTP 才能触达会话"的绕路。
- **headless/workspace 目标**：`runtimehost.Executor` 抽取（E3.3）后，daemon 已通过 `handler.Executor()` 复用与 runtime-server 同一执行核（`cmd/scheduled/executor.go`）；runtime-server 天然具备；aicli 侧按 `aicli-executor-reuse-design.md`（E3.4，D1–D8 差异映射）逐步接入。执行承接对三者是同一份契约（`TurnRequest` + fail-closed 权限），差异只在"谁愿意承接"（配置 + 能力位）。
- **结果回投**：headless 作业结果回投创建者会话（`deliverJobResult`）同样走 §3.3 的就近路由，天然支持"创建者在别的宿主"。

### 3.5 与"三平面"既有结论的关系（修订表述）

`README.md §2` 与 `scheduler-daemon-design.md` 的原表述是：

> 调度面 = 独立守护进程 `cmd/scheduled`（长驻、单实例、持定时器）；控制面 = aicli/runtime-server（不持定时器）。

本方案将其修订为（不推翻，只泛化）：

> **控制面不变**（create/list/update/delete + 会话兜底 drain）；
> **调度面 = 持 mesh 租约的可抢占角色**，daemon 是**默认常驻成员与第一优先级**，runtime-server（可选 aicli）是**在岗替补**；
> **执行面 = 就近承接**（会话宿主进程内 / 任一具备执行核的宿主 / daemon 内嵌），session 租约仲裁继续保证"活跃会话不被抢占"。

原设计否掉"宿主内嵌定时器"的三个理由（宿主不运行=不触发 / 多实例竞争 / 双投递），在本方案中分别由 ①"联盟内任一成员在岗即可触发 + daemon 兜底"、②"mesh 租约单实例"、③"任务级 claim + 幂等键"解决——这正是 mesh 与 bots 多实例经验带来的新条件。

---

## 4. 必须补齐的四个机制

联盟化把"单进程串行"变成"多节点竞争"，以下四项缺一不可，否则今天的问题会以"重复投递 / 静默不投 / 版本踩踏"的形式回来。

### 4.1 能力位与在岗可见性（解决 P2 静默）

- **能力位**：`mesh` 节点档案新增 `scheduler`（建议带 store 兼容标记，见 §4.2）。默认：
  - daemon：`["mesh","scheduler"]`（其 mesh Host 目前未入网，需补 `mesh.NewHost` 入网写档，或至少写驻留标记）；
  - runtime-server：`["mesh","runner","poller","scheduler"]`（新增位；由配置开关决定是否真的参战）；
  - aicli：opt-in 时加 `scheduler`。
- **创建时校验**：`schedule_create` / HTTP create 在写库前做一次"在岗检查"——`ReadLease(schedule-scheduler/global)` + 存活节点里是否存在带 `scheduler` 能力位的进程。结果三态：
  - `driver_online`：正常返回；
  - `driver_absent`：任务照常创建（存储与控制面独立），但回执与 UI 明确提示"**当前无调度者在岗，任务不会准时触发；请启动 scheduled 或保持 runtime-server 运行**"；
  - `driver_incompatible`：存在调度者但 store 版本不兼容（§4.2）→ 明确报"调度者版本过旧，请升级"。
- **在岗面板**：`/schedule` 命令与 web「任务」页显示调度者身份（node_id/kind）、租约到期时间、最近一次驱动时间；daemon `/status`（8919）扩展同样的字段，统一口径。

### 4.2 schema / 版本握手（解决本次二级根因）

本次 `scheduled.exe`（v3）打不开 store（v5）时只在 stderr 吐一行后退出，外界毫无感知。补齐三处：

1. **节点档案携带兼容信息**：`HostConfig` 已有 `Version/BuildTime`，能力位再加 store 兼容档（如 `scheduler:store-v5` 或独立的 `schedule_store_schema` 字段扩展——优先复用能力位字符串，避免动 mesh 记录 schema）。这样"谁在岗、能否跑当前 store"从视图一眼可判；
2. **选主前校验**：持有租约者发现自己理解不了 store（`migrate` 拒绝）→ 立即释放租约并写"不兼容"信号（退出码 + 档案能力位），让合格节点接管，而不是占着租约空转；
3. **创建/查看时校验**：§4.1 的 `driver_incompatible` 态；同时 `doctor`（daemon）与 `/schedule`（宿主）都要显式展示 store schema 与二进制支持档。

参考先例：mesh call 对节点档案已有 `CallCodeSchemaTooNew` 的"版本不兼容早失败"处理（`call.go`），schedule 侧照抄这种显式失败哲学即可。

### 4.3 任务级 claim（防双投递）

`schedule_tasks` 的 `owner_instance_id` / `lease_expires_at` 两列与 `idx_schedule_lease` 索引已经预留，但**没有任何代码写过它们**——把预留转正：

- **CAS claim**：driver 处理某任务前先做一次原子 UPDATE（`WHERE id=? AND (owner_instance_id IS NULL OR lease_expires_at < now OR owner_instance_id=?self)`）写入自己 + 短租约（如 60s，处理期间续期）；抢失败说明别的节点正在处理 → 跳过；
- **覆盖窗口**：leader 易主、网络分区、时钟漂移下，靠 claim 把"至多一次"收敛到单节点；claim 过期（崩溃）后由新 leader 重试（v1.1 修订：仅 claim + `message_id` 不足以承诺唯一投递，窗口的闭合依赖 §12.1 的"交付账本 + 接收侧去重"，否则按 at-least-once 语义表述）；
- **不采用"mesh 任务级租约"作为主方案**：任务级并发控制在 store（SQLite 单写域 + 现成索引）内完成最可靠，且 mesh 根不可用时不至于连 claim 都做不了；mesh 租约只负责**领导权**（粗粒度）。这条分工要写进 `runtimehost-executor-extraction-plan.md` §2.5a 的"锁域"说明旁边（leadership=mesh 文件租约；会话执行=session_runtime.sqlite 租约；任务=schedule.sqlite CAS）。

### 4.4 misfire 语义与观测

- **misfire 策略显式化**：现状是"启动即补发所有 overdue"。联盟化后明确为：
  - `after` / `at`：逾期即补发（可配置宽限窗口 `misfire_grace`，超窗跳过或仅记 inactive，二选一由配置决定，默认补发）；
  - `every` / `daily` / `weekly` / `cron`：沿用既有口径"停机只补发最近一次"，不追补全部历史周期；
  - claim 保证补发动作幂等，接管风暴（多次易主）不会放大投递次数。
- **观测**：复用 `Runtime.Metrics()`（交付/失败/内嵌计数），增加 driver 身份与租约字段，随 §4.1 面板一起暴露；事件通道可增发 `schedule.driver.changed`（与 `schedule.changed` 同族，B 通道 live-only）。

---

## 5. 方案对比与取舍

| 方案 | 内容 | 优点 | 问题 | 结论 |
|---|---|---|---|---|
| A. 维持现状 + 服务化 | 只把 `scheduled.exe` 装成 Windows 服务 / systemd，不改编排 | 改动最小 | "另一个可执行文件 + 另一份配置"的运维面仍在；本次事故（未安装 + 版本漂移）会复发；同机在线宿主仍然使不上劲 | 仅作为 S0 应急，不作为终态 |
| B. 宿主裸接管定时器 | runtime-server / aicli 各自起定时器，不做选举 | 实现直观 | 多进程重复投递；谁在调度不可知；用户界面的 aicli 频繁开关导致抖动 | 否决 |
| C. 联盟式调度（本方案） | mesh 租约选主 + 就近投递 + 任务级 claim + 版本握手；daemon 为默认常驻成员 | 消除调度单点运维；复用 bots 已验证模式；同机零配置直达；跨机仍可 delegate；失败可解释 | 需要四个补丁机制、占用一些 mesh/存储改造 | **推荐** |
| D. 全量 delegate | 宿主统一暴露 HTTP 端点，daemon 只调度 | 职责清晰 | 同机跨进程还要过网络层；daemon 不在岗时依然无人调度；不解决 P1/P2 | 作为 C 的 L3 分支保留 |

**关键决策记录（ADR 简表）**

| # | 决策 | 理由 |
|---|---|---|
| D1 | 调度领导权 = mesh 可抢占角色，而非进程身份 | 消除"部署了才有调度"的单点；单一持有者语义不变 |
| D2 | 投递就近优先（L1→L5），HTTP delegate 降为跨机分支 | 消灭 `runtimeServerURL` 空值这类配置坑；同机最短路径 |
| D3 | 任务级 claim 放 store CAS，mesh 租约只做领导权 | store 是权威且单写域；mesh 不可用时 claim 仍可用 |
| D4 | 宿主默认**不**在 mesh 降级时自跑（fail-closed standby） | 不能判定唯一性时宁可不动，宁可晚投不可双投；daemon 保持可配置降级 |
| D5 | daemon 保留且优先级最高；`participants` 默认为 `[daemon, runtime-server]` | 长期无人值守场景（headless/workspace 作业）仍以 daemon 最稳；aicli 频繁开关，opt-in |
| D6 | misfire 沿用既有口径（周期类只补最近一次；一次性逾期补发） | 与 `README.md`/既有实现语义一致，避免行为迁移面扩大 |

---

## 6. 落地切片（S0–S5）

> 原则：S0 可立即上线止血（不依赖本方案评审）；S1–S4 按顺序，每片独立可验收；S5 可选。

### S0 应急止血（当天可做）✅ 已落地（2026-10-15）

| 项 | 内容 |
|---|---|
| 动作 | ① `go build -o scheduled.exe ./cmd/scheduled` 重建（支持 schema v5）；② 以前台或 Windows 服务方式运行（`scheduled run` / `install+start`）；③ `doctor` 确认 `store_ok=true`、`due_count` 符合预期 |
| 配置 | `.aicli/runtime.yaml` 的 `schedule.execution` 显式化：`mode: auto`（daemon 自跑，不依赖 URL）或 `runtimeServerURL: http://127.0.0.1:8101`（delegate 到在跑的 runtime-server） |
| 验收 | 逾期任务被立即补投；`/schedule` 能看到任务转 inactive（一次性） |
| 说明 | 纯运维动作，不改代码；用于立即恢复能力，同时为 S1+ 争取时间 |

> **落地状态（2026-10-15）**：① `scheduled.exe` 支持当前 schema（v7，含交付账本/claim/attempts）；② 服务化安装代码就绪并修正了一个真实陷阱——**服务默认以 LocalSystem 运行**，`~` 会解析到系统 profile，install 现在把安装者解析出的绝对路径（`--store` / `--log-file` / `--config`）烘焙进服务命令行，避免“服务在跑但读写另一份库/日志”；③ **运行期日志落盘**：daemon 新增 `--log-file`（默认 `~/.aicli/logs/scheduled.log`，带 UTC 时间戳，服务模式 stderr 不可见的缺口已补，实测落盘）；runtime-server 日志归一为专属 `~/.aicli/logs/runtime-server.log`（纠正了配置模板把默认指向共享 `gateway.log` 导致的“日志混写 + start 失败诊断看不到日志”，serve 与 `start` 诊断同路径，实测生效）；④ `schedule.execution` 显式化（`mode: auto` + `runtimeServerURL`，`doctor` 实测 `store_ok=true`、`due_count` 正确、`runtime_server_ok=true`）。边界：SCM 注册/启停需管理员执行 `scheduled.exe install`。

### S1 领导权组件化 + runtime-server 参与（小）✅ 已落地（2026-10-14）

| 项 | 内容 |
|---|---|
| 改动 | 把 `cmd/scheduled/lease.go` + `Runtime.Start/Stop` 的装配抽成 `internal/schedule`（或 `internal/schedulehost`）的 `Leader`（`LeaderOptions` 见 §8）；+ driver 正式状态机（candidate/standby/leader/draining/quiesced + degraded/incompatible，§12.2.1）与"先停驱动、后放租约"的让位顺序；+ daemon 以**无 endpoint** 的 node record 入网（`capabilities=["mesh","scheduler"]`，M4）；+ driver 租约显式 TTL=30s（mesh 默认 90s，M3） |
| 接线 | runtime-server `serve`：`schedule.enabled=true` 且配置允许时加入 participants（能力位 `scheduler`）；daemon 改用同一组件（行为不变） |
| 验收 | 双进程实验：daemon + runtime-server 同时在线只有一个持租约；杀掉持有者，另一个 30s 内接管并补发 overdue；`aicli-mesh ls` 能看到 daemon 节点与 `scheduler` 能力位；让位时无并行驱动窗口；两者都不在线时行为与今天一致（无调度） |
| 风险 | 领导权在宿主内的生命周期要挂到进程信号/优雅关闭上，避免退出不释放（TTL 兜底） |

### S2 就近投递（小）✅ 已落地（2026-10-14/15）

| 项 | 内容 |
|---|---|
| 改动 | 新增组合式 deliverer（`LocalFirstDeliverer`）：L1 本进程 hub（复用 `scheduleSessionDeliverer`）→ L2 binding/view 解析目标节点 + `mesh.Call`（op `schedule.deliver`，write，幂等键 `occurrence_key`；前置校验见 §3.3-2）→ L3 HTTP delegate → L4 embedded → L5 mailbox；+ 接收侧账本校验（按 occurrence_key 去重，§12.1） |
| 接线 | runtime-server 的 handler 把"本进程 deliverer"暴露给 Leader；aicli 宿主同构（其 session hub/actor 注册表已有） |
| 验收 | 在 runtime-server 托管的会话上创建提醒：不配置 `runtimeServerURL` 也能准时投递；宿主在别的工作区/本机其他进程时走 L2（跨工作区按策略跳过）；`conflict`/无 endpoint 目标不盲投；目标节点掉线时按 L3/L4/L5 逐级降级且不丢消息；在"投递成功、commit 前"杀 leader → 接管后不产生重复提示（账本命中） |
| 风险 | mesh.Call 的写操作纪律（token/跨工作区拒绝/回环限制）必须复用，不能新开口子 |

### S3 能力位 + 在岗检查 + 版本握手（小）✅ 已落地（2026-10-14）

| 项 | 内容 |
|---|---|
| 改动 | ① 能力位 `scheduler`（词表常量）+ store 兼容档 `scheduler:store-v5`（M5）；daemon/runtime-server/aicli 档案更新；② create 三态检查（§4.1）；③ 选主前/持租约时自检 store 兼容，不兼容即释放并标记；④ 幽灵 leader 检测：租约持有者在档案中非 live → 告警/复核（M8 只读半边） |
| 验收 | 构造"无调度者在岗"场景：create 回执带明确警告；构造"旧版调度者"场景：`driver_incompatible` 可见；`/schedule` 与 daemon `/status` 字段一致 |
| 风险 | 能力位字符串要有词表常量（参照 `internal/connectors/hostid.go` 的 `MeshCapability*` 做法），避免散落魔法字符串 |

> **落地状态（2026-10-14，S3）**：① 能力位 `scheduler` + store 兼容档 **`scheduler:store-v<N>`**（`schedulehost.SchedulerCapability`，N 由 `schedule.SupportedSchemaVersion()` 派生、不再手写 v5）；daemon 与 runtime-server（配置参战时）均已携带，aicli opt-in 留待 aicli 接入 Leader（E3.4）。② 创建三态检查 `schedulehost.ProbePresence`：无租约 → absent；持有者不在档案 / 非 live / 无调度位 → absent+**ghost**（幽灵 leader，M8 只读半边）；在岗但未声明兼容档或档位低于当前 store → incompatible；否则 online。`schedule_create` 工具回执与 HTTP `/schedule/create` 回执注入 `presence` 快照 + `view.notice`（非在岗/不兼容时给出"不会准时触发 + 修复动作"，在线无噪音、不拦创建）；`/schedule` 目录头部与 daemon `/status.presence` 同字段口径（实测一致）。③ `Leader.StoreCompatCheck`（`schedulehost.StoreCompatCheck(store.SchemaVersion)`）：选主前每轮 + 持租约续租前自检，store 超出本二进制支持档 → 释放租约 + `StateIncompatible`，让合格节点接管；恢复后自动回候选。实机验收（2026-10-14）：runtime-server（新档，携带 v6 标记并持租约）时 create 回执 `driver_online`（store_schema=v6、driver_store_schema=v6）；daemon 前台 `/status` presence 同口径输出、standby 行为正常；旧档（无标记）调度者在岗 → `driver_incompatible` 由单测钉住。 |

### S4 交付账本 + 持久化 attempts + group claim + 死信状态（中）✅ 已落地（2026-10-14）

| 项 | 内容 |
|---|---|
| 改动 | ① **交付账本** `schedule_delivery_intents`（occurrence_key 唯一索引，intent→delivered；接收侧去重，§12.1）；② 任务级 claim（`ClaimTask/RenewClaim` CAS）**升级为组级 claim**：按 `groupFor` 的 KindEvery 合批语义，同一会话同窗任务一次事务 claim；③ attempts 持久化（`attempt_count/last_attempt_at`）与死信状态显式化；④ misfire 配置与行为（§4.4）；⑤ 驱动前复核租约（M1/M2 的兜底）；⑥ metrics/状态扩展（driver 身份、claim 冲突、接管次数、账本对账） |
| 验收 | 模拟 leader 易主/双 leader（人为过期租约）：同一 occurrence 至多一次进入会话（账本命中）；claim 冲突有计数可观测；失主后 attempts 不归零、死信状态可查；周期任务停机 1 天后只补最近一次；every 合批不被拆成多条 |
| 风险 | 账本与 delivery history/pending 三重语义要对齐（pending 保留"失败证据"）；claim 与退避重试的交互要过一遍 Runtime 现有分支（failedUntil/attempts/groupFor） |

> **落地状态（2026-10-14，S4a）**：① store **v7**：`attempt_count` / `last_attempt_at` / `dead_lettered_at` 三列（`TaskAttemptStore`：`RecordTaskAttempt` 保留首次死信留痕、`ClearTaskAttempts` 成功即清；簿记写不推进 state_version）。② **组级 claim 转正**（§4.3，D3）：`TaskClaimer`（一次事务内多行 CAS，未持有/本人/租约过期 < now 可抢；部分成功是正常结果）+ Runtime 集成——drive 前 claim，抢不到 → 延后 15s 重扫 + `ClaimConflictsTotal` 计数；内嵌/headless 长执行前续期到 `embeddedTurnTimeout + 60s`；投递成功释放并清 attempts。③ leader 易主：`hydrateAttempts` 从持久层恢复 attempts/退避（接管不风暴重试）。实机 E2E（2026-10-14）：runtime-server（store v7）对无宿主会话投递失败 → `attempt_count=1`/`last_attempt_at` 落库、claim `owner_instance_id`=节点 id 且可查、pending 兜底消息存在；测试数据已清理。**S4b（同日）**：`schedule.misfireGrace` 显式配置（0=总是补发兼容现状；>0 时一次性任务超窗不投递、直接转 inactive、计 `MisfireSkippedTotal`；周期任务不适用——单测钉住“停机只补最近一次”）；`Leader.Takeovers` 接管计数（首次空租约不计入；从他节点/过期残租上位 +1，随 `/status.driver` 暴露）；daemon `/status.stats` 账本对账（active/inactive/pending/delivery intents，实测与库一致）；metrics 新增 `claimConflictsTotal`/`misfireSkippedTotal`。M2（epoch fencing）已落地（2026-10-15，见 §12.3 M2）。

### S5 可选增强

- aicli 参与 driver（opt-in，含优先级让位 v2）；
- 跨节点 headless 执行路由：按 workspace 亲和 / profile 匹配 / 负载选择 executor 节点；
- `mesh.Spawn` 联动：目标会话宿主完全离线时，按需在正确 workspace 拉起节点（代价与治理见 bots 文档同款纪律）。

> **落地状态（2026-10-15，S5a）**：① **aicli 参与 driver（opt-in）**：`schedule.driver.participants` 含 `aicli` 时，于**会话门控安装点**启动 `schedulehost.Leader`（`cmd/aicli/commands/chat_schedule_driver.go`，与会话宿主挂载解耦）——复用本地宿主 service，远端 runtime-server 执行器模式下自带 store/service（退出关闭）；投递 L1 当前会话注入（账本守卫）→ L2 mesh → L3 HTTP；不装配内嵌执行核（E3.4 另行设计）；参战时经 `mesh.Host.AddCapabilities` 运行期宣告 `scheduler` + store 兼容位（补上 S3 遗留的 aicli 能力位）。② **优先级让位 v2**：`schedulehost.SchedulerKindPriority`（daemon 1 > runtime-server 2 > aicli 3；未知=0 保守）+ `HigherPrioritySchedulerOnline`（live + 带调度能力位）+ `LeaderOptions.DeferToHigherPriority`——存在更高优先级在岗者时不抢租约，持租约期间其上线即主动释放（不等 TTL）。实机（headless aicli）：驱动 `joined as node-… (priority=3)` → `candidate → leader (lease acquired)`。**驱动与会话宿主生命周期解耦**（`chat_setup.go` 门控安装点启动、会话 teardown 无条件停止；自带 store/service），使"开着 aicli 就有调度"从会话建立即成立，不受交互式 TUI 异步能力面挂载时序影响（实测该挂载可能到首个 turn 才发生）。实机验证矩阵（2026-10-15）：无更高优先级在岗 → aicli `candidate → leader (lease acquired)`（租约 owner=aicli、epoch 3，顺带再次验证 M2 回收递增）；runtime-server 在岗 → `deferring to higher-priority scheduler (runtime-server)` → standby 不抢；单测覆盖优先级词表/在岗判定/不抢/主动让位/能力位幂等。**未启动**：跨节点 headless 执行路由、`mesh.Spawn` 联动。

---

## 7. 配置草案（`.aicli/runtime.yaml` / runtime.yaml）

```yaml
schedule:
  enabled: true

  driver:                      # 新增：调度领导权
    participants: [daemon, runtime-server]   # 有资格竞争的角色，按序即优先级（aicli 需显式加入）
    lease_ttl: 30s             # mesh 默认 90s；driver 显式 30s（接管更快，M3）
    stable_window: 60s         # v2 优先级让位的防抖窗口
    degraded: standby          # standby（默认，fail-closed）| standalone（无 mesh 时单机自跑）
    sweep_interval: 60s        # 兜底驱动（防时钟跳变/休眠漏拍；精确 timer 之外的安全网）

  execution:
    mode: auto                 # delegate | embedded | auto（默认 auto：先就近/委托，不行再内嵌）
    runtimeServerURL: ""       # 仅跨机/远端需要；同机就近投递不依赖它
    profile: ""                # 内嵌/headless 的受限 profile（沿用现状）
    fallbackToMailbox: true

  misfire:
    catch_up_once: true        # 一次性任务逾期补发
    grace: 30m                 # 超出宽限窗口的一次性任务视为过期（仅记录/转 inactive）
```

兼容性：以上字段全部可选，缺省时行为 = "daemon 可独立运行 + 宿主不参战"，与现状一致，可灰度打开。

---

## 8. 接口草案（Go 伪代码）

```go
// ---- internal/schedule（或 internal/schedulehost）----

// DriverIdentity 描述一个候选/在岗调度者。
type DriverIdentity struct {
    NodeID   string // mesh node id；daemon 可使用 "scheduled-<pid>"
    Kind     string // daemon | runtime-server | aicli
    Priority int    // participants 顺序映射
}

// LeaderOptions 是"以调度者身份运行"的全部依赖（与 daemon 现有装配一一对应）。
type LeaderOptions struct {
    Store     schedule.Store
    Service   *schedule.Service
    Deliverer schedule.Deliverer      // 组合式：LocalFirstDeliverer
    Executor  schedule.SessionExecutor
    Headless  schedule.HeadlessExecutor
    Arbiter   schedule.LeaseArbiter
    Mode      string                  // delegate | embedded | auto
    Identity  DriverIdentity
    Logf      func(format string, args ...any)
}

// Leader 持有 mesh 租约并运行 schedule.Runtime；失租/退出时安全停止。
type Leader struct{ /* ... */ }

func NewLeader(opts LeaderOptions) (*Leader, error)
func (l *Leader) Start(ctx context.Context) // Acquire 成功则 runtime.Start；失败进 standby（监听接管时机）
func (l *Leader) Stop(ctx context.Context)
func (l *Leader) Status() LeaderStatus      // 供 /status 与面板：身份/租约到期/最近驱动/metrics

// ---- store 扩展（把预留列转正）----

// ClaimTask 以 CAS 抢占任务：仅当无主/租约过期/本人持有才成功。
func (s *SQLiteStore) ClaimTask(id, owner string, ttl time.Duration, now time.Time) (bool, error)
func (s *SQLiteStore) RenewClaim(id, owner string, ttl time.Duration, now time.Time) (bool, error)
func (s *SQLiteStore) ReleaseClaim(id, owner string) error

// ---- 交付账本（v1.1，§12.1）----

// OccurrenceKey = schedule_id + occurrence_at（唯一索引），跨 leader 去重。
type DeliveryIntent struct {
    OccurrenceKey  string
    ScheduleID     string
    Seq            int64
    State          string    // intent | delivered
    Owner          string    // driver node id
    LeaseExpiresAt time.Time
}

func (s *SQLiteStore) BeginDelivery(intent DeliveryIntent, now time.Time) (bool, error) // INSERT OR IGNORE：撞键=false
func (s *SQLiteStore) MarkDelivered(occurrenceKey string, now time.Time) error
func (s *SQLiteStore) LookupDelivery(occurrenceKey string) (DeliveryIntent, bool, error) // 接收侧投递前查

// ---- deliverer 组合 ----

type LocalFirstDeliverer struct {
    Local  schedule.Deliverer // 本进程 hub（SubmitPromptAsync）
    Peer   schedule.Deliverer // mesh.Call("schedule.deliver")
    HTTP   schedule.Deliverer // 既有 HTTPDeliverer
    // 之后由 Runtime 依次回退：embedded → mailbox
}
```

mesh 侧改动：
- `HostConfig.Capabilities` 增加 `scheduler`（词表常量集中定义）；
- `call.go` op 白名单增加 `schedule.deliver`（write=true；端点建议 `/web/api/mesh/schedule-deliver` 或并入 `invoke` 语义，二选一在实现时定）；
- 节点档案的 `Process` 段已有 version/build_time，配合能力位 `scheduler:store-<n>` 完成 §4.2 握手。
- **（v1.1）** `Lease` 增加 `Epoch`（fencing token）与 `Renew` 进入 `.claim` 锁内重读校验的修复（M1/M2，见 §12.3）——**✅ 均已落地（M1 2026-10-14；M2 2026-10-15）**；任务/组 claim 为最终裁决、每轮 drive 前复核租约继续作为纵深防线（S4）。

---

## 9. 测试与验收计划

| 层级 | 用例 | 判据 |
|---|---|---|
| 单元 | `Leader` 的租约竞争/失租停止/standby 接管（fake clock + 内存 store） | 同一时刻仅一个持租约；失租后 runtime 停止无可疑写 |
| 单元 | `ClaimTask` CAS（无主/过期/他人持有/本人续期四态） | 至多一个成功；过期后可被接管 |
| 集成（双进程） | daemon + runtime-server 同时在线：创建 5s 后提醒 | 只投一次；杀掉持有者 ≤30s 内接管并补发 |
| 集成（就近） | 不配置 `runtimeServerURL`，runtime-server 托管会话 | L1 准时投递（消灭配置坑） |
| 集成（failover） | 目标宿主进程崩溃（模拟 R2a 场景） | 提醒按 L2→L4→L5 降级路径可达且不重复 |
| 集成（版本） | 旧二进制（v3）与新 store（v5）共存 | `driver_incompatible` 显式可见；不静默 |
| 集成（misfire） | 停机 1 小时后恢复：一次性 + 周期任务各一 | 一次性补发；周期只补最近一次 |
| UX | 无任何调度者在岗时 `schedule_create` | 回执/UI 明确警告（与本次事故场景一一对应） |

现有资产可复用：`cmd/scheduled/lease_test.go`、`executor_integration_test.go`、`embedded_integration_test.go`、`internal/schedule/runtime_test.go`、mesh `two_hosts_test.go`、bots `service_dispatch_test`（R1/R2a/R3 四例）为路由测试提供同构模板。

---

## 10. 风险与开放问题

| # | 风险/问题 | 缓解 / 待决 |
|---|---|---|
| R1 | 双 leader（租约时钟漂移、mesh 文件系统分区）重复投递 | 任务/组 claim + 交付账本 occurrence_key 去重（§12.1）；claim 冲突计数可观测（S4） |
| R2 | 宿主频繁开关导致领导权抖动、接管补发风暴 | `participants` 默认不含 aicli；v2 优先级让位加稳定窗口；接管后扫描天然幂等（R1 兜底） |
| R3 | leadership lease（mesh 文件）与 session lease（DB）、任务 claim（store）三套锁域的心智负担 | 明确分层并写入文档：leadership=粗粒度唯一性；session 租约=执行不抢占；task claim=任务并发（写进 `runtimehost-executor-extraction-plan.md` §2.5a 旁注） |
| R4 | runtime-server 承接 driver 后，其"控制面不持定时器"的既有承诺需修订 | 属于本方案的显式修订（§3.5）；代码注释与文档同步改 |
| R5 | mesh 记录 schema 兼容档的载体（能力位字符串 vs 新增字段） | 倾向能力位字符串（零 schema 变更）；实现前在 `internal/mesh` 词表常量化 |
| R6 | 跨机（多台机器）调度 | 明确非目标 v1：mesh 目前是本机文件态（`~/.aicli/mesh`）；跨机走 L3 delegate 或后续共享 store 方案 |
| R7 | 宿主进程内跑 headless 作业的用户预期（突然消耗 token/资源） | 沿用 fail-closed 权限与 profile 收窄（现状）；`participants`/`mode` 由配置显式打开；观测面板可见 |

**开放问题（评审时定）**：① daemon 是否必须补 mesh 入网（或仅写驻留标记）；② `schedule.deliver` 独立 op 还是并入 `invoke`；③ aicli 参与 driver 的默认值（建议 v1 默认关闭）；④ 一次性任务宽限窗口默认值（30m?）。

---

## 11. 联动修订清单（2026-10-14 铺设完成）

> 除"代码注释"行随 S0/S1 实施提交外，其余修订均已落笔。

| 文档 | 修订点 | 状态 |
|---|---|---|
| `docs/automation-tasks/README.md` | §2 第 2 条、§3 文档清单：把"调度面 = 独立守护进程"改为"调度面 = mesh 可抢占角色（daemon 为默认常驻成员）"；登记本文件 | ✅ 已落笔（§2 第 2 条 + §3 清单） |
| `docs/automation-tasks/scheduler-daemon-design.md` | §三平面表、§4 租约小节：补充"角色化"与参与者优先级；daemon 降级语义与新 `degraded` 策略对齐 | ✅ 已落笔（顶部 + §4 注记） |
| `docs/automation-tasks/embedded-execution-design.md` | delegate/embedded 之外补"就近投递 L1/L2"在路由中的位置；确认 embedded 仍是最后的执行兜底 | ✅ 已落笔（§1 路由注记） |
| `docs/automation-tasks/runtimehost-executor-extraction-plan.md` | §2.5a 锁域说明补 leadership lease 与 task claim 两层；E1 结论不变 | ✅ 已落笔（三锁域补记） |
| `docs/automation-tasks/analysis-and-implementation-plan.md` | §6.2 原"宿主内嵌定时器被否"处加修订注记：以 mesh 选主 + claim 解决当初的三个反对理由 | ✅ 已落笔（§6.2 补记） |
| `docs/bots/09-multi-instance-binding.md` | 交叉引用：schedule 复用其 R1/R2a/R3 路由模式（实现侧可共享 `DecideRoute` 思路） | ✅ 已落笔（§7.2 复用注记） |
| `docs/automation-tasks/implementation-status.md` | 追加 S0（重建/服务化）与 S1–S4 状态行 | ✅ 已落笔（新增"调度面 mesh 化"章节） |
| 代码注释 | `cmd/scheduled/main.go` 顶部"后续阶段"注释、`cmd/aicli/commands/chat_schedule_command.go` 提示语、`.aicli/runtime.yaml` schedule 段注释（引号内的旧口径）同步更新 | ⏳ 随 S0/S1 实施提交（仅注释，不改逻辑） |
| `docs/plan/aicli-mesh-architecture.md` | §3.3/§4.4 追加"修订说明（2026-10-14）"：续约/回收覆盖竞态（M1）与 Epoch/fencing 建议（M2） | ✅ 已落笔（随本审查） |

---

## 12. 完整性审查记录（v1.1，2026-10-14）

> 方法：对照 `backend/internal/mesh`（lease / host / registry / call / binding / view）与 `backend/internal/schedule`（runtime / store / service / metrics）逐项核验本方案 §3–§6 的机制假设。
> 结论：**骨架成立、无需推翻**；但两处需要实质补强——①"唯一性保证"目前只能做到 at-least-once，方案的"claim + message_id 兜底"表述不完整；②多实例状态机没有正式定义。mesh 侧另有 1 个正确性竞态（M1）与若干可观测/策略缺口。

### 12.1 运行唯一性：补"交付账本"（S4 升级）

先说结论：**"claim + message_id 兜底"（§4.3 原文）不足以承诺唯一投递。** 五个代码事实：

| # | 事实（代码证据） | 后果 |
|---|---|---|
| U1 | 交付顺序是"**先投递、后 commit**"：`runtime.go` drive 中 `Deliverer.Deliver(...)` 成功才走 `commit`；commit 用 `UpdateCAS(state_version)` | leader 在"投递成功但 commit 前"崩溃/失租 → 新 leader 重新扫描到同一 occurrence 再投一次 |
| U2 | commit 的 CAS 失败（控制面并发编辑）只记日志（`applied=false` 分支） | 已送达但未推进 → 下次扫描再投 |
| U3 | 投递载荷**已经携带去重键**：单条 = `[SCHEDULE REMINDER] + schedule_id_json + occurrence_at`（`domain.go:153`）；批次 = `reminders_json[]` 每项含 `schedule_id + occurrence_at`（`runtime.go:650`） | 接收侧按 `(schedule_id, occurrence_at)` 去重的材料是现成的；缺的是"谁去重、在哪落库" |
| U4 | `schedule_delivery_history` 只在成功后由驱动侧写入 | 失主窗口内，接收侧无法自证"这条是不是重复" |
| U5 | attempts/退避只在进程内存（`r.attempts` / `r.failedUntil`）；`metrics.go` 注释承诺"达上限转 inactive 留痕"，但 `StatusInactive` 唯一写入点是 commit 的正常完成分支 | 失主即断记忆；"死信"没有持久状态，且注释与实现不一致 |

**修订（S4 从"任务级 claim"升级为"交付账本"）：**

1. 新增 `schedule_delivery_intents`（推荐新表；也可扩展 `schedule_pending_messages`，但 pending 语义应保持"失败证据"）：以 `occurrence_key = schedule_id + occurrence_at` 建**唯一索引**，状态 `intent → delivered`，带 owner/租约；驱动在**投递前**写入 intent（撞键 = 已有在途/已完成 → 跳过或对账）；
2. **接收侧去重**：L1（本进程 hub）与 L2（`schedule.deliver` 端点）在 `SubmitPromptAsync` 前按 occurrence_key 查账本：已 delivered 直接回执，避免重复入 actor；
3. commit 成功后把 intent 标 `delivered`（与 delivery history 同批）；崩溃恢复 = intent 与 history 对账后收敛；
4. 明确降级选项：若评审决定"提醒类容忍极端窗口重复"（产品上通常可接受），则保留 at-least-once 并在文档与 UI 语义里写明，**不得宣称唯一**；
5. **attempts 持久化**：`attempt_count / last_attempt_at`（任务行列或 attempts 表）；死信策略显式化（转 `inactive` + reason，或保持 active 无限退避），并把 `metrics.go` 注释与实现对齐。

> 唯一性分层口径（建议写入文档术语表）：
> - **领导权唯一**：mesh 租约（尽力而为，M1/M2 有竞态与无 epoch 的缺口）；
> - **任务并发唯一**：store CAS claim（权威，单 SQLite 写域）；
> - **投递唯一**：occurrence_key 账本 + 接收侧去重（新增，补 U1–U4 窗口）；
> - 三层缺一不可，最终效果 = effectively-once（在账本 + 去重落地的前提下）。

> **落地状态（2026-10-14/15，S2a + S2b）**：交付账本（store v6 `schedule_delivery_intents`）+ 接收侧去重（L1 守卫 `GuardedDeliverer`、L3 命令端点 `occurrences`、**L2 aicli 端点**）+ `LocalFirstDeliverer`（L1/L2/L3 组合）全部落地；`occurrence_key` 统一毫秒截断（与 store `instantLayout` 对齐）。
> S2b 实机（2026-10-15）新增两个由 E2E 抓出的关键修复：
> ① **L1 宿主前置闸门**——driver 只能为"本进程确实托管"的会话走 L1（已有活 actor 或会话在本进程库可加载），否则降级 L2；没有这道闸门曾把外部 TUI 会话的提醒投进 runtime-server 的幽灵 actor（被"接受"但宿主不可见、账本/历史照常推进）。
> ② **控制面变更唤醒**——`Service.SetDriverWaker`（创建/删除后唤醒 `RequestDrive`）+ Runtime 空闲兜底扫描（`idleSweepInterval`）：此前空闲期创建的任务要等下一次 drive 才可见（实机复现）。
> 另：aicli 接收侧的账本解析经**进程级注册位**（`processScheduleService`）解耦"早期会话快照 LocalRuntimeHost 恒为 nil"问题（异步能力面装载前 web 处理器拿到的对象）。实机验收：TUI 会话经 `mesh.call.received op=schedule.deliver` 收到提醒并落账；host 死亡时 L2 连接拒绝 → 自动降级 L5 mailbox 不丢消息。组级 claim 仍留 S4。

### 12.2 多实例状态模型（五层状态表）

原方案只在 §3.2 散点提到"待命/在岗"，缺少可实现的正式状态机。审查补齐如下。

| 层 | 状态集合 | 现状来源（代码） | 缺口 |
|---|---|---|---|
| 节点档案 | `live / stale / stopped / unknown` | `mesh/registry.go`（NodeState）；心跳 30s / TTL 90s（`host.go`） | 已在；daemon 不写档（M4） |
| 租约 | `acquired / held / contended / degraded / unreadable`（`LeaseOutcome`）+ `fresh / expired / stale`（`Stale()`） | `mesh/lease.go` | ✅ M1/M2 已修（Renew 锁内重读校验；`Lease.Epoch` fencing token） |
| 调度者 | **本方案新增**：`candidate / standby / leader / draining / quiesced`，旁路态 `degraded / incompatible` | 目前仅文字描述 | 需正式状态机（下方 12.2.1） |
| 任务执行 | `active / claimed / delivering / failed-backoff / dead-letter / inactive`（claim 等与现有 active/inactive **正交**） | 现有仅 `active/inactive`；`owner_instance_id`/`lease_expires_at` 列闲置 | claim 语义 + attempts 持久化（§12.1） |
| 会话归属 | `owner / peer / conflict / none` × `running / busy / idle / unknown / orphaned` | `mesh/view.go` | `conflict` 的路由处置未定义（12.2.2） |

#### 12.2.1 调度者（driver）状态机

| 状态 | 进入条件 | 行为 | 退出 |
|---|---|---|---|
| `candidate` | 进程启动且角色在 `participants` 内 | 尝试 `mesh.Acquire` | 成功→`leader`；被持→`standby`；mesh 不可用且未开 standalone→`degraded` |
| `standby` | Acquire 失败（他人持有）或让位中 | 周期探测（读租约 + 持有者档案存活），**不驱动** | 租约过期/持有者消失→抢入 `leader`；配置关闭→`quiesced` |
| `leader` | Acquire 成功 | 跑 `Runtime`；**每轮 drive 前复核租约归属**；续租线程在跑 | 失租→`standby`；退出信号→`draining`；store 不兼容→`incompatible` |
| `draining` | 收到终止/让位信号 | 停止接新批次，等待进行中 drive 结束（`Runtime.Stop` 已具备），随后释放租约 | →`standby`（让位）或 `quiesced`（退出） |
| `degraded` | mesh 根不可用且 `degraded: standalone` | 单机自跑（默认仅 daemon 允许进入；宿主默认不进入，见 D4） | mesh 恢复→`candidate` |
| `incompatible` | store schema 超出本二进制支持档 | **立即释放租约**、写明确日志与能力位标记，不占用领导权 | 升级二进制→`candidate` |

补充约束：**让位 = 先停驱动、后释放租约**（顺序颠倒会让两个 leader 短暂并驱）；`standby` 的探测周期建议 = `lease_ttl/3`。

#### 12.2.2 `conflict` 归属的处置（新决策点）

`view.go` 的 `ownership=conflict`（多个存活节点声明同一会话）在 L2 路由中必须特殊处理：**不得盲投**——若 driver 自身是冲突方之一走 L1；否则跳过 L2 直接 L3/L4/L5，并在 `/schedule` 面板提示"会话归属冲突，投递走兜底路径"。

### 12.3 mesh 缺口与优化清单（M1–M10）

> 按严重度排序；每条给出代码证据、影响与建议归属（mesh 改动 / schedule 层自足）。

| # | 级别 | 缺口 | 证据与影响 | 建议 |
|---|---|---|---|---|
| M1 | **高·正确性** | `Renew` 与 reclaim 存在 clobber 竞态 | `Acquire` 用 `.claim` 文件串行化"read-decide-rename-create"（`lease.go` 注释明确该序列非原子）；而 `Renew` 是 `ReadLease → writeLeaseFile`（temp + rename **无条件覆盖**），不取 claim。时序：A 读到自持的旧租约 → B 判定 A stale（TTL 越界/PID 假死）reclaim+create(B) → A 的 rename 覆盖 B 的新租约 → A、B 同时自认持有 | Renew 必须在 claim 锁内重读校验（OwnedBy 且未被接管）后再写；或引入 epoch（M2）让接管可被检测。schedule 层缓解：任务 claim CAS 为最终裁决 + 每轮 drive 前复核租约。**✅ 已修复（2026-10-14，随 S1）：`Renew` 进入 `.claim` 锁内重读校验；回归测试 `TestRenewAfterTakeoverNeverClobbers`** |
| M2 | **高·能力** | 租约无 epoch / fencing token | `Lease` 结构（`lease.go:68`）只有 AcquiredAt/RenewedAt/TTLSec；下游无法证明"我来自当前持有者" | 建议加 `Epoch`（acquire/reclaim 时取 max(已读)+1）；任务 claim 可携带 epoch 作栅栏。若暂不动 mesh schema：把"投递前复核租约"列为 S4 硬性步骤。**✅ 已落地（2026-10-15）**：`Lease.Epoch`（加性字段，`schema_version` 不变）——新建=1、接管/回收=旧值+1、续约保持、legacy 缺字段→0 且按"不可判定"处理；`Leader` 每次续约校验 epoch（失配即让位）、`LeaderStatus.Epoch` + `doctor` + `presence.lease_epoch` 暴露；回归测试 `TestAcquireAssignsMonotonicEpoch` / `TestAcquireLegacyLeaseEpoch` / `TestLeaderCountsTakeoverFromForeignLease`（epoch 1→2）；实机：epoch 1 → 强杀（不释放）→ 回收接管 → epoch 2 |
| M3 | 中·口径 | TTL 表述易混 | mesh 默认 TTL 90s（`DefaultLeaseTTL`）；`cmd/scheduled` 显式用 30s；本方案 §3.2 写的是后者 | driver lease 显式 30s（接管快）；文档与配置注释不得写"沿用 mesh 默认" |
| M4 | 中·可观测 | daemon 不写 node record | daemon 只有租约文件（`newSchedulerLease` 只 Acquire），mesh 视图里不可见、能力位无法表达 | S1 增补：daemon 写**无 endpoint** 的 node record（`capabilities=["mesh","scheduler"]`；mesh 支持无 endpoint 记录，只是不可被 call） |
| M5 | 中·兼容 | 版本/兼容档载体未定 | 本次事故（旧 exe v3 vs store v5）证明"能力位需带兼容信息" | 倾向能力位字符串 `scheduler:store-v5`（零 mesh schema 变更）；沿用 `CallCodeSchemaTooNew` 的早失败哲学 |
| M6 | 中·策略 | `mesh.Call` 跨工作区拒绝 | `CallCodeCrossWorkspace` 是既有闸门；目标会话在别的 workspace 时 L2 会被拒 | 显式决策：`schedule.deliver` 是否允许跨工作区（若允许需审计与 source 标记）；否则 L2 仅同 workspace，跨区自动降级 L3/L4/L5 |
| M7 | 中·能力边界 | Call 只支持带 loopback endpoint 的节点 | 无 `--web` 的 aicli 不可被调用（`ResolveCallTarget` 已做早失败） | 文档承诺"L2 不可达 ≠ 失败"；fallback 链是有意的（且 leader==owner 时 L1 覆盖常见场景） |
| M8 | 中·健壮性 | "幽灵 leader" | `host.go`：连续写失败超过 `maxWriteFailures=3` 后节点**停止写档但仍在运行** → 档案显示 stale/unknown，租约却仍被其续租 | driver 循环监测自身档案写健康：连续失败主动转 `degraded` 并放弃 leadership；档案恢复后再竞争 |
| M9 | 低·可观测 | 无"当前 leader"一等查询 | 现状只能手工 `ReadLease`（doctor 是唯一消费者） | mesh view/CLI 增 leases 汇总或 `scheduler status`；供 §4.1 create 校验、面板、CLI 复用 |
| M10 | 低·范围 | 跨机不可用 | mesh 根是本机文件态（`~/.aicli/mesh`） | 维持非目标：跨机走 L3 HTTP 或后续共享 store 方案 |

### 12.4 对 S0–S5 的修订汇总

| 切片 | 修订 |
|---|---|
| S1 | + daemon 入网写档（M4）；+ driver 状态机（§12.2.1）与"停驱动→放租约"的让位顺序；+ driver lease 显式 TTL=30s（M3） |
| S2 | + L2 前置校验（`conflict` 跳过、跨工作区策略 M6、无 endpoint 跳过 M7）；+ `schedule.deliver` 幂等键 = `occurrence_key`；+ 接收侧账本校验（§12.1-2） |
| S3 | + 能力位携带 store schema（M5）；+ "幽灵 leader"检测：租约持有者在档案中非 live 时告警/复核（M8 的只读半边） |
| S4 | 升级为"**交付账本 + 持久化 attempts + group claim + 死信状态**"（§12.1）；group claim 需与 `groupFor` 的 `KindEvery` 合批语义对齐（同一会话同窗的 every 任务一次事务 claim，避免批次被拆分） |
| S5 | 不变 |

### 12.5 新增开放问题

1. 提醒类业务对"极端窗口重复"的容忍度——决定账本复杂度（§12.1 的降级选项是否可接受）。
2. `schedule.deliver` 跨工作区策略（M6）。
3. 账本形态：新表 `schedule_delivery_intents` vs 扩展 `schedule_pending_messages`（推荐新表；pending 保持"失败证据"语义）。
4. "幽灵 leader"是否必须主动退位（M8），还是靠任务 claim + 驱动前复核即可兜底。
5. `Epoch` 是否进 mesh `Lease` schema（影响 mesh 记录兼容面，M2）。**已决（2026-10-15）**：进 `Lease` 作加性字段（`json:"epoch,omitempty"`），`schema_version` 保持 2；旧读者忽略、新读者把缺字段读作 0（不可判定）。

### 12.6 审查结论

- §3–§5 的角色模型、就近路由与切片方向成立，**不需推翻**；
- 唯一性表述从"claim + message_id"升级为"**交付账本（occurrence_key 唯一索引 + 接收侧去重 + 持久 attempts）**"后，才可对外承诺唯一投递；否则只能承诺 at-least-once + 可观测的重复窗口；
- 多实例状态机（§12.2）、group claim、幽灵 leader、daemon 入网四项应并入 S1–S4 的验收标准；
- mesh 的 M1（Renew 竞态）与 M2（epoch）属 mesh 自身正确性/能力增强：可随本方案 S1 顺带落地，也可单独立项，但**在 S4 之前必须有一处兜底**（任务 claim 权威 + 每轮投递前复核租约）。

---

## 附录 A：事故证据链（2026-10-14）

| 证据 | 内容 |
|---|---|
| store 记录 | `schedule-4753cce5-8e7e-4cc1-ba25-f9a13021a793`；`session_id=session_20261014140934_bgvm9eLR`；`kind=after/300s`；`scheduled_at=13:43:42.656Z`；`status=active`；`last_scheduled_at/last_delivered_at/owner_instance_id=NULL` |
| 无副产物 | `schedule_pending_messages` 无行、`schedule_job_runs` 无行；WAL 最后写入 = 创建时刻（之后无任何进程写库） |
| 进程面 | 无 `scheduled` 进程；无 Windows 服务/计划任务；8919 无监听；`backend\scheduled.exe doctor` → `"schema is newer than this binary: database schema v5 is newer than this binary (v3); refusing to open"` |
| 配置面 | `.aicli/runtime.yaml` 仅 `schedule.enabled: true`；`execution.mode` 缺省=delegate、`runtimeServerURL` 空 |
| 会话面 | 会话由 `runtime-server.exe serve`（PID 19352，:8101，wechat connector）托管；宿主侧 `drainSchedulePending` 只在会话激活时消费 pending——没有 pending 可消费 |

## 附录 B：相关既有结论索引

- 三平面拆分与 daemon 设计：`scheduler-daemon-design.md` §三平面、§4（mesh 租约）、§6（投递兜底）；
- delegate/embedded/auto 与 session 租约仲裁：`embedded-execution-design.md`、`runtimehost-executor-extraction-plan.md`（E0/E1/E3）；
- 执行核复用：E3.3（daemon 路径 A，已落地）、E3.4（aicli，设计完成待接入）；
- 作业通用化（goal/plan/target/headless 与结果回投）：`automation-job-generality-design.md`（J0–J4，已落地）；
- 多实例承接先例与纪律：`docs/bots/09-multi-instance-binding.md`（R1/R2a/R3、poller 唯一性、claimed 幂等、failover 已落地）。
