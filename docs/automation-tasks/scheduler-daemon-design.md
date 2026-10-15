# 调度守护进程（scheduled daemon）设计

> 问题：计划任务（Schedule）应**会话级独立**——不依赖某个 aicli/runtime-server 进程常驻。需要**独立守护进程**长驻处理定时（Windows 服务 / Linux systemd）；aicli / runtime-server 只做控制、创建、查看。
> 结论：新增 **`cmd/scheduled`** 守护进程（三平面拆分：控制面 / 调度面 / 执行面），复用 `internal/mesh` 租约做单实例，投递经 runtime-server 的会话命令端点 + 持久 mailbox 兜底。
> 本文修订 [`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md) §6 的"进程内单定时器"假设。

> **v1.1 修订（2026-10-14，联盟式调度）**：本文的"调度面 = 独立守护进程"泛化为"**调度面 = mesh 可抢占角色**"——daemon 仍是默认常驻成员与第一优先级，但 `runtime-server`（可选 `aicli`）也可按配置参与领导权竞争；投递路由扩展为"就近优先 L1→L5"（本进程 hub → 目标节点 `mesh.Call` → HTTP delegate → embedded → mailbox）。协调机制、风险与落地切片见 [`mesh-scheduler-federation-design.md`](./mesh-scheduler-federation-design.md)。本文其余内容（子命令、服务化、存储、mailbox 语义）不变。

---

## 1. 问题与动机

原方案（主方案 §6.2）把定时器放在**宿主进程内**（aicli / runtime-server 的 `ScheduleRuntime`），并靠"启动扫描 + 重启补发"。缺陷：

1. **宿主不运行 = 任务不触发**。用户关掉 aicli、没开 runtime-server，提醒就永远不会到点。
2. **会话级不独立**：任务虽绑定 session，但其**调度**却依赖承载该会话的进程活着。
3. **多实例竞争**：多个 aicli/runtime-server 各自持定时器，需要额外的 owner 选举。

→ 正确模型：**调度与执行分离**。一个**长驻守护进程**统一管理所有任务的触发；宿主负责控制面与执行。

---

## 2. 进程模型（三平面）

```
┌───────────────────────────┐        ┌──────────────────────────────┐
│ 控制面 (aicli / runtime-server) │        │ 调度面 (cmd/scheduled 守护进程) │
│  create/list/update/delete  │ 写库    │  长驻 · 单实例 · 持 timer       │
│  CLI + HTTP /api/runtime/…  │ ───────▶│  读到期 → 投递 → 推进(CAS)      │
└─────────────▲─────────────┘  schedule.sqlite └───────────┬──────────┘
              │                                            │ 投递
              │                                    ┌───────▼────────┐
              │                                    │ 执行面           │
              └──────────── HTTP 会话命令 ◀────────│ runtime-server   │
                        (submit_prompt)            │ (GetOrCreate +   │
                                                   │  SubmitPromptAsync)│
                                                   └──────────────────┘
```

| 平面 | 进程 | 职责 |
|---|---|---|
| 控制面 | `aicli` / `runtime-server` | 创建/查看/编辑/删除任务；写共享 store；**不持定时器** |
| **调度面** | **`cmd/scheduled`（新）** | 长驻；单实例；到点计算与投递；跨重启；重试/死信 |
| 执行面 | `runtime-server`（会话 actor） | 收到投递的 user 消息后跑一次 turn |

> **"会话级独立"的含义**：任务的**调度**不依赖任何会话活跃、任何交互进程存活；守护进程常驻即可触发。**执行**默认交给 runtime-server（`delegate`），若需**完全无人值守**，daemon 可切换 `embedded` 模式**自己执行**（见 [`embedded-execution-design.md`](./embedded-execution-design.md)）；两者都不可用时投递落持久 mailbox（§6）。

---

## 3. 守护进程 `cmd/scheduled`

### 3.1 子命令

| 命令 | 语义 |
|---|---|
| `scheduled run` | 前台运行（开发 / 容器 / 调试） |
| `scheduled install` | 安装为系统服务（Windows Service / systemd unit） |
| `scheduled uninstall` | 卸载服务 |
| `scheduled start` / `stop` / `status` | 服务控制 |
| `scheduled doctor` | 自检（store 可达、租约、runtime-server 连通性、待触发数） |

> 实现状态：`run` / `doctor` / `install` / `uninstall` / `start` / `stop` / `status` 已实现
> （Windows Service；非 Windows 的服务安装显式提示 systemd，见 §3.2）。单实例租约已加**续租 +
> 失租停止**（续租丢失即停止调度，避免双实例双投递）。`doctor` 只读，不取租约。

### 3.2 平台集成

| 平台 | 机制 | 实现 |
|---|---|---|
| Windows | **Windows Service**（SCM） | `golang.org/x/sys/windows/svc`；服务名 `ai-agent-runtime-scheduled`；`install` 用 `sc create`/SCM API，`--start=auto` |
| Linux | **systemd** unit | `aicli-scheduled.service`（`Type=simple`、`Restart=always`、`After=network.target`）；`install` 写 `/etc/systemd/system/` + `systemctl enable --now` |
| macOS（可选） | launchd plist | `~/Library/LaunchAgents/` |
| 容器 | 前台进程 | 无服务包装，直接 `scheduled run` |

> **新增能力**：项目当前**没有**任何服务化基础设施（`grep windows/svc|systemd` 为空），这是全新模块。可参考 `cmd/runtime-server` 的生命周期（信号处理、优雅关闭）。

### 3.3 生命周期

- 启动：加载配置 → 打开 store（`sqliteutil` WAL 基线）→ **获取调度租约** → 启动单定时器 + 立即扫描一次 → 暴露本机 `healthz/status`（仅 loopback）。
- 停止：SIGTERM/服务 stop → 取消定时器 → 排空进行中的投递 → 释放租约 → 关库。

---

## 4. 单实例与接管

复用 `internal/mesh`（`lease.go`）：

```go
outcome := mesh.Acquire(paths, "schedule-scheduler", "global", owner, mesh.AcquireOptions{TTL: 30 * time.Second})
// 持租约者才执行定时与投递；续租 mesh.Renew(...)；退出 mesh.Release(...)
```

- **只有持租约的 daemon 触发任务**；多实例并存时其余实例待命（租约到期自动接管，`mesh.Takeover` 语义）。
- 心跳/续租复用 mesh，不新造。

> **v1.1 补充**：① 租约参与者不再限于 daemon——`participants`（默认 `[daemon, runtime-server]`）按序即优先级，让位语义为"先停驱动、后放租约"；② 宿主默认 **fail-closed standby**（mesh 降级不参战），daemon 保留可配置的 `degraded: standalone`；③ driver 租约 TTL 显式 30s（mesh 默认 90s）；④ mesh `Renew` 的已知竞态（M1）与 Epoch 建议（M2）由联盟方案 §12.3 跟踪。均见 [`mesh-scheduler-federation-design.md`](./mesh-scheduler-federation-design.md) §3.2/§12。

---

## 5. 存储与变更通知

- daemon 与控制面**共享** `schedule.sqlite`（WAL + `busy_timeout` + 单连接池，`sqliteutil.OpenFile` 基线；见 [`data-storage-design.md`](./data-storage-design.md)）。
- 控制面写任务行；daemon 读到期行、写回执/推进（`state_version` CAS）。
- **变更通知**：SQLite 无 pub/sub，daemon 采用：
  - 主：**短周期轮询** `SELECT MIN(scheduled_at) FROM schedule_tasks WHERE status='active'`（1s 级，成本极低）；
  - 辅（可选）：控制面写库后向 daemon 的本机端点发一次"重算"提示，或写 `schedule_notify` 表，daemon 命中即立即重算。

---

## 6. 投递（关键路径）

daemon 到点后必须把提醒送进**原会话**。**三级兜底**：

| 通道 | 机制 | 前提 | 取舍 |
|---|---|---|---|
| **A. HTTP 直投（delegate）** | `POST /api/runtime/sessions/{id}/runtime/commands`（`submit_prompt`，`handler.go:1089`）→ runtime-server `GetOrCreate` 重建 actor → `SubmitPromptAsync` | runtime-server 在线 | 立即执行；失败可重试 |
| **B. 内嵌执行（embedded）** | daemon 内嵌执行栈，取 **session 租约**后 `hub.GetOrCreate` + `SubmitPromptAsync` | daemon 能读 config/凭据/会话库 | **完全无人值守**；见 [`embedded-execution-design.md`](./embedded-execution-design.md) |
| **C. 持久 mailbox** | 写会话持久 mailbox 的 `trigger_turn=true` 消息（`chat/trigger_turn_drain.go`、`agentcontrol` global mailbox） | 宿主下次运行/drain | 最终一致；宿主不在也能落库 |

**推荐链**：`A → B → C`——HTTP 直投成功即确认；失败且启用 `embedded` 则自执行；仍不可用（拿不到 session 租约/未启用）则写持久 mailbox，宿主下次 drain 时消费。效果：
- 宿主在线 → 立即触发（体验最好）；
- 宿主离线但允许内嵌 → daemon 自己跑（无人值守）；
- 都不可用 → 消息不丢，恢复后补发（最终投递）。

**回执语义**（与主方案 §6.3 一致）：确认 = 通道 A 返回 2xx **或** B 提交被 actor 接受 **或** C 落库成功；随后写回执 + 推进目标（同一事务）。**不等待** turn 完成。

**重试/死信**：A/B 失败按退避（30s/1m/2m…上限 10m）重试；达到 `MaxDeliveryAttempts`（默认 5）后转 `inactive` 并记 `delivery_failed`（避免空转）。

---

## 7. 与控制面 / 运维的接口

| 面 | 接口 |
|---|---|
| CLI | `aicli schedule list/create/update/delete`（走控制面 HTTP） |
| HTTP（用户/前端） | `/api/runtime/schedule/*`（catalog/list/history/update/delete） |
| daemon 运维（仅 loopback） | `GET /healthz`、`GET /status`（租约、下次触发、待投递、store 可达） |
| 事件 | `schedule.changed`（控制面写库后广播）；daemon 不面向用户 |

---

## 8. 与既有方案的关系（需同步修订）

| 文档/章节 | 原假设 | 修订 |
|---|---|---|
| 主方案 §6.1/§6.2 | 宿主进程内单定时器 + 启动扫描 | 改为 **daemon 内单定时器**；宿主不持定时器 |
| 主方案 §6.2 多实例 | 单实例假设 | 由 **mesh 租约**保证单实例；daemon 是唯一调度者 |
| 主方案 §6.3 投递 | `SessionHub.GetOrCreate` + `SubmitPromptAsync`（**进程内**） | 改为**跨进程**：HTTP 直投 + 持久 mailbox 兜底 |
| `data-storage-design.md` §6 | owner 列指向宿主实例 | `owner_instance_id` = **daemon 实例**；租约归 daemon |
| 主方案 §6.2 wake | P2 复用 supervision wake | **不再需要**（daemon 自长驻）；supervision wake 与本能力解耦 |

---

## 9. 分期

> **与主方案映射**：D0 → 主方案 **P0**（前台 daemon）；D1–D3 → 主方案 **P2**（守护进程服务化）。

| 阶段 | 交付 | 验收 |
|---|---|---|
| D0 | `cmd/scheduled run`（前台）+ mesh 单实例租约 + 共享 store + 轮询 + HTTP 直投 | 两个 daemon 只有一个触发；宿主在线时到点即触发 |
| D1 | 持久 mailbox 兜底 + 重试/死信 | 宿主离线时消息落库，宿主恢复后补发；达上限转 inactive |
| D2 | `install/uninstall/start/stop/status`（Windows Service + systemd）+ `doctor` | 服务随系统启动；重启机器后任务仍触发 |
| D3 | 本机 `/healthz`、`/status` + 观测指标 | 运维可查租约/下次触发/待投递 |

---

## 10. 风险与开放问题

1. **执行依赖宿主（默认）**：`delegate` 模式下 daemon 只调度；runtime-server 长期不在线则任务只能落库。**由 `embedded` 模式消除**（daemon 内嵌执行栈自跑 turn，见 [`embedded-execution-design.md`](./embedded-execution-design.md)）；代价是 daemon 体积/依赖增大与无人值守审批约束。
2. **Windows Service 权限**：投递/落库需与宿主同用户或共享数据目录权限。
3. **时钟/休眠**：Windows 休眠、容器暂停后，timer 漂移 → 依赖"启动扫描 + 到期即补发最近一次"（已有语义）。
4. **多用户/多实例共享目录**：租约与 store 需在同一 mesh root 下，避免跨用户看不到彼此。
5. **服务安装需管理员**：`install` 需提权，文档需说明。
