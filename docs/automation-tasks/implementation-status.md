# 实施状态（P0–P4 + P5 机制；主链路完整）

> 依据：[`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md) §10 统一路线图。
> 状态：**P0–P4 全量 + 全局页 + 四缺口修复 已提交**；P5 机制层就绪（E0 完整执行栈待路径 B 重构）；P6 为增强项。

---

## 1. 提交清单

| 提交 | 内容 |
|---|---|
| `2eb91237` | P0：领域层/存储/工具/调度守护进程 + 文档 |
| `0f66cd57` | P0-8：runtime-server 侧接线 + gofmt |
| `84c206ce` | P1a：时区/DST + daily/weekly |
| `4115a9ae` | P1e：HTTP API（catalog/list/update/delete） |
| `48294340` | P1b/c：cron 解析/规范化/发生点 + 工具与 API |
| `ded5de5b` | P1d：投递历史读写与分页 |
| `cdf46221` | P1f：子 Agent 拒绝 + 归档准入 |
| `aea74ee3` | P2：守护进程服务化（D1–D3） |
| `c82faf1a` | P3：E1 frontend 自动化任务面板 |
| `930f5415` | P4：E2 micro web 任务页签 + E3 TUI /schedule |
| `ed3f6e0f` | 缺口修复：drain 接线 / schedule.changed / 搜索地平线 / 死锁 |
| `e668a252` | 全局页 /runtime/schedule（P3 余项收敛） |
| `3a518144` | P6 观测收敛：metrics + daemon /status 暴露 |

## 2. 已交付

### P0
- `internal/schedule`：types/errors/domain/store/service/runtime。
- `cmd/scheduled`：前台守护进程（mesh 单实例租约）。
- `toolbroker`：`schedule_*` 四工具（`Broker.Schedule != nil` 才进工具面）+ 审计表。
- `config`：`ScheduleConfig`/`ScheduleExecutionConfig`（默认 disabled/delegate）。
- 宿主：aicli（`chat_schedule_host.go`）+ runtime-server（`getScheduleService`）两侧接线。

### P1
- **P1a** `timezone.go`：`ParseTimeZone`/`parseLocalTime`（拒绝 24:00/闰秒）、
  `localInstant`（DST 缺口不存在、重叠取较早）、`NormalizeWeekdays`、
  `CreateDaily/CreateWeekly` + 发生点解析（回溯最近一次 + 次日起找未来）。
- **P1b/c** `cron.go`：五字段 Vixie 解析、规范化（整段均匀步长 a-b/n、星号步长、
  连续段合并、dow 全域 `*`、0/7 统一 0）、DOM/DOW 语义、`CreateCron` +
  `resolveCronOccurrence`（同日多次发生）；存储表达式非规范化即拒绝打开；
  工具与 HTTP API 暴露 cron。
- **P1d** 投递历史：`AppendDeliveryRecord`（seq 单调）、`ListHistoryPage`
  （追加序逆序 + 排他游标）、`Service.History`（limit 1..100、未知游标
  `delivery_cursor_not_found`、`NextBefore`）；runtime 投递提交同批写历史表；
  `GET /api/runtime/schedule/history`。
- **P1e** HTTP API：`/api/runtime/schedule/{catalog,list,history,update,delete}`。
- **P1f** 权限边界：`create/update` 对 `agent_depth>0` 拒绝（`subagent_session`）；
  `ArchiveSession`/`BatchArchiveSessions` 归档准入（活动任务阻止归档）。

### P2（D1–D3）
- **D1** 持久兜底：`schedule_pending_messages` 表（migration v2）；投递失败即落
  pending（`FallbackToMailbox` 可关）；`DrainPendingMessages` 供宿主恢复后消费。
- **D2** 服务化：Windows Service（SCM：`install/uninstall/start/stop/status`，
  服务名 `ai-agent-runtime-scheduled`，StartType=Automatic）；非 Windows 显式
  提示 systemd（见设计文档）。
- **D3** 运维端点：`127.0.0.1:8919` 的 `/healthz` 与 `/status`。

### P3（E1 frontend）
- `types/runtime/schedule.ts` + `api/runtime/schedule.ts`：类型与 REST 客户端
  （catalog/list/history/update/delete）。
- `hooks/workspace/use-schedule-tasks.ts`：目录数据 hook（事件延迟刷新、active
  兜底轮询、失败保留最近已知、删除/保存统一入口）。
- `components/workspace/schedule-panel.tsx`：右侧栏**自包含面**（活动/未运行分区、
  行内编辑草稿——仅钟表规则可改时间、完整 `expected` CAS 保存、冲突保留草稿、
  删除二次确认）+ `schedule-panel-shared.ts` 纯函数（6 例单测）。
- `panel-registry.ts`：注册 `schedule` 面（CalendarClockIcon / artifact tone /
  content 宽度 / requiresSession=false）。
- i18n：zh-CN 与 en-US 的 `panels-schedule` 模块 + `shell.panelTabs.schedule`。

### P4（E2 micro web + E3 TUI）
- **E2**：`web/js/schedule.js`（任务目录模块：进入即重拉、两段式删除确认、esc
  转义）+ `index.html` 页签/面板 + `ui.js`/`app.js` 接线 + `style.css`（全部走
  已定义 token）+ `scripts/verify-micro-web-schedule.mjs`（静态契约 + 迷你 DOM
  行为验证全过）。
- **E3**：TUI `/schedule [all|active|inactive]` 只读目录命令
  （`chat_schedule_command.go`，数据面 = `session.LocalRuntimeHost.Schedule`
  in-process store，与 `schedule_*` 工具同源）；`command.go` 路由 + legacy 出口、
  C12 inline/read 注册、slash 补全条目与一致性测试同步。

### P5（内嵌执行：机制就绪）
- `internal/schedule/executor.go`：`SessionExecutor` + `LeaseArbiter` 接缝。
- `runtime.tryEmbedded`：delegate 失败 → Arbiter 取 **session 排他租约**（拿不到
  = 活跃会话，绝不抢占，退回 pending mailbox）→ 拿到才自跑 turn（10m 上限）→
  成功即 commit 推进任务。
- `cmd/scheduled`：`daemonSessionArbiter`（mesh `purpose=session` /
  `key=session-<sid>`，与 `mesh.Host` 约定一致；windows/other 双构建）。
- 测试：租约授予→内嵌执行并 commit；租约被持有→绝不抢占并退回 pending；
  未注入 Executor→恒走兜底。

## 3. 与设计的差异（诚实记录）

| 设计 | 实现 | 说明 |
|---|---|---|
| `runMeta.source="schedule"` | `[SCHEDULE REMINDER]` 固定文本 | `team.RunMeta` 无通用 source 字段 |
| 回执 messageId | 合成 id `schedule:<id>:<ms>` | delegate HTTP 响应未回传消息 id |
| `schedule.changed` 事件 | 未实现 | P3 随 frontend 交付补 |
| 归档 stop 语义（stop 删除活动行） | 仅拒绝归档，未提供"停止任务再归档"的原子 stop | P3 随 UI 交付补 |
| cron 400 年搜索地平线 | 800 天（date loop）| P1 简化；规范化/语义不变 |
| 归档准入读取路径 | 每次归档读一次 catalog | 未做缓存，量级小 |

## 4. 验证

```
cd backend
go build ./internal/schedule/... ./internal/toolbroker/... ./internal/api/runtimeapi/... ./internal/config/... ./cmd/scheduled/... ./cmd/aicli/commands/   ✅
go test ./internal/schedule/... ./internal/toolbroker/ ./internal/config/   ✅
gofmt -l（相关文件）  ✅
go vet（相关包）  ✅
```

> `internal/pty` 当前构建失败来自他人未提交 WIP（`write_stdin`/PTY 交互终端），
> 已用 `git stash` 验证与本改动无关。

## 5. 未完成（后续阶段）

- **P5 余项（E0 完整执行栈）**：daemon 注入真正的 `SessionExecutor` 需要
  `bootstrap.Manager` + `runtimeapi.Handler` 栈，但 aicli 会话执行强耦合
  per-session 状态（`ChatSession`/session store/workspace/profile）——无会话记录的
  daemon 无法低成本构造 actor（诚实评估见 embedded-execution-design.md §3）。
  可行路径：路径 B 抽取 `internal/runtimehost.Executor`（独立重构批次），或
  daemon 在投递前确认 runtime-server 可达并仅依赖 delegate。**机制已全部就位**：
  注入 Executor 即启用，`mode=delegate` 默认行为不变。
  路径 B 的分析与分阶段方案见
  [`runtimehost-executor-extraction-plan.md`](./runtimehost-executor-extraction-plan.md)。

### P7（人工创建入口）
- 参考调研：deepseek-harness 的"新建"按钮只开新会话让模型创建（其管理页
  刻意无创建表单）；本项目的 P7 是对该做法的扩展——提供真正的人工创建。
- `schedule.Origin`（model/manual）+ migration v3（`origin` 列 DEFAULT 'model'，
  未知/空值 scan 回落 model，旧库零迁移）；`Service.Create` 增加 origin 参数。
- HTTP `POST /api/runtime/schedule/create`（origin=manual）：会话绑定 + 恰好一个
  选择器，校验复用服务层与模型工具完全相同的管线（title/prompt/DST/cron）。
- 前端面板/全局页"新建任务"表单：六种规则全覆盖（after/every 分钟数、at
  datetime-local、daily/weekly/cron 时间+时区+星期集合），人工任务行内
  "人工"徽章；i18n zh/en 同步。
- 测试：`TestTaskOriginPersistence`（manual/model round-trip + 旧行回落）、
  `TestCreateScheduleTask_ManualOrigin`/`_ReusesServiceValidation`（HTTP 层）。

### P8（命名契约执行——B1–B4 全量落地）
`docs/automation-tasks/tool-surface-inventory-and-naming-review.md` §4/§5/§7 的
迁移批次全部完成：
- **B1**：后台作业归一为 `job_*`（`job_start/output/cancel/monitor/control/input`），
  新增 `job_list`/`job_search` 读取面（ListJobs 客户端侧过滤）；旧名
  `background_task`/`task_*`/`write_stdin`/`task_input` 保留为可调用别名
  （normalizeToolName 映射）。
- **B2**：团队域归一为 `team_*`（`team_spawn/wait/send/mailbox/task_spec/
  task_context/task_report/task_block`）。**关键修复**：`applyObservedTaskOutcome`
  与 complete_task 结果观察的匹配表补入新名——旧名匹配导致 teammate 的
  `team_task_report` 观察被丢弃、任务终态从不落地（FailedAutoStart 集成测试
  3/3 复现钉住）；lead-planner 摘要提示与 teammate 提示同步改名。
- **B3**：子代理域归一为 `subagent_*`（`subagent_spawn/list/send/followup/
  input/approve/wait/events/close/resume/apply_worktree/discard_worktree`）。
- **B4**：计划模式归一为 `plan_enter/plan_exit`（`plan_review` 不变）。
- **契约测试**：`tool_naming_contract_test.go` 四断言——广告面全部匹配
  `<domain>_<verb>`（域封闭集合 + 动词封闭集合 + 核心原语白名单）、全部旧名
  别名仍可 normalize、广告面不含任何旧名、canonical 名是 normalize 不动点。
  新增工具将被测试拦截，防止命名再次漂移。
- 深度策略/只读策略/capability 表/taxonomy/listable/doom-loop 豁免与
  轮询软刹车（`subagent_wait` 等）、本地宿主系统提示、深度上限 deny 表
  （subagent_spawn/team_spawn 双名）全部同步。
- 已知边界：`agentcontrol.WorkflowSpawnTeam = "spawn_team"` 是**持久化 workflow
  标签**（DB 行值），不是工具名，保持不变；`/s` 斜杠候选与 catalog 等失败的
  项属 P4 调度命令交付的既有问题，与命名契约无关。

### C3 结论（不做自动化桥）
作业终态已有 supervision 投影 + wake 投递（`projectLocalBackgroundJobTerminal`），
模型在下一 turn 可见结果——再自动创建 `at` 提醒会引入双投递，且违反"创建始终
由模型完成"的产品原则（daemon 无会话创建权限）。**以文档结论代替实现**。

### P6（观测收敛）
- `internal/schedule/metrics.go`：原子计数器
  （deliveredTotal / deliveryFailedTotal / embeddedTotal / embeddedSkippedTotal /
  retriesScheduled / deadLetterTotal）+ `MetricsSnapshot` 只读快照。
- `runtime` 埋点：投递成功（delegate/内嵌）、内嵌被租约仲裁跳过、退避重排、
  死信（达 MaxDeliveryAttempts）；`Metrics()` 访问器。
- daemon `/status`（127.0.0.1:8919）携带 metrics 快照。
- 口径：`deliveryFailedTotal` 只计"未被内嵌接住的失败"——内嵌接住的失败直接
  转化为 embeddedTotal+deliveredTotal。
- **P6 余项（增强）**：E4 gateway console 只读面；metrics 并入宿主 Prometheus
  端点（可复用 `Runtime.Metrics()`）。

---

## 调度面 mesh 化（v1.1 方案，2026-10-14）

> 方案：[`mesh-scheduler-federation-design.md`](./mesh-scheduler-federation-design.md)（含 §12 完整性审查：交付账本/五层状态机/mesh 缺口 M1–M10）。
> 状态：**S0–S4 + M1/M2 已落地（2026-10-14/15）；S5a（aicli opt-in driver + 优先级让位 v2）已落地（2026-10-15）；S5 其余项待排期**。

| 切片 | 内容 | 状态 |
|---|---|---|
| S0 | 重建 `scheduled.exe`（支持 schema v5）+ 服务化安装 + `schedule.execution` 配置显式化 | ✅ 已落地（2026-10-15）。① `scheduled.exe` 支持当前 schema（v7）；② 服务化 `install/uninstall/start/stop/status` 就绪，且 **install 把安装者解析的绝对路径（`--store`/`--log-file`/`--config`）烘焙进服务命令行**——服务默认以 LocalSystem 运行时不再读写系统 profile 下的错误库；③ 运行期日志落盘：daemon 默认 `~/.aicli/logs/scheduled.log`（含 `--log-file` 覆盖与 UTC 时间戳，服务模式 stderr 不可见的缺口已补）；runtime-server 日志归一为专属 `~/.aicli/logs/runtime-server.log`（模板默认的共享 gateway.log 被纠正，serve 与 `start` 失败诊断同路径，实测生效）；④ `schedule.execution` 显式化（`mode: auto` + `runtimeServerURL`，`doctor` 实测 `store_ok=true`/`due_count` 正确/`runtime_server_ok=true`）。边界：SCM 注册与启停需**管理员**执行 `scheduled.exe install`（本机当前 shell 非管理员，仅验证到只读 `status` 的错误路径与参数烘焙逻辑） |
| S1 | `Leader` 组件化（mesh 租约角色化）+ runtime-server 参与 + daemon 入网 + driver 状态机 | ✅ 已落地（2026-10-14：`internal/schedulehost` + daemon 重构 + runtime-server 接线；双进程实测单持租约、杀掉持有者后 10s 内接管、`aicli-mesh` 可见 `scheduler` 能力位） |
| S2 | 就近投递 L1–L5（含接收侧账本校验与 L2 前置校验） | ✅ 已落地（2026-10-14/15）。S2a：交付账本 store v6 + L1/L3 接收侧去重 + `LocalFirstDeliverer` L1/L3 组合 + occurrence 毫秒规范化。S2b：`mesh.Call` op `schedule.deliver` + aicli 接收端点（账本守卫）+ L2 目标解析/前置校验 + **L1 宿主前置闸门**（非宿主进程不得为外部会话建幽灵 actor）+ **控制面变更唤醒**（`SetDriverWaker` + 空闲兜底扫描）+ 账本注册位解耦早期会话快照。实机 E2E：TUI 会话经 L2 收到提醒并落账；host 死亡时按 L2→L5 降级进 mailbox 不丢消息。**2026-10-15 ConPTY 复测抓出共享库误判**：会话库为所有宿主共享，`manager.Get` 可加载 ≠ 本进程托管——aicli 托管的活跃会话被旧 L1 判真，幽灵 actor 吞掉提醒且静默 commit（无 turn、无日志、TUI 不可见）。修复：`scheduleLocalSessionHosted` 在共享库分支增加 **mesh binding 归属校验**（`binding.last_node_id` ≠ 本节点 → 拒绝，降级 L2；binding 缺失保守沿用旧判定）。修复后复测：`local(L1): session … is not hosted` → `delivered via peer(L2)` → aicli `mesh.call.received op=schedule.deliver` → turn 起注入帧（模型亲证五轮对照） |
| S3 | 能力位 + 在岗检查 + schema 握手 + 幽灵 leader 检测 | ✅ 已落地（2026-10-14）。① 能力位 `scheduler` + store 兼容档 `scheduler:store-v<N>`（N 随 `schedule.SupportedSchemaVersion()` 派生，当前 v7；daemon 与 runtime-server 参战时携带）。② `schedulehost.ProbePresence` 三态（driver_online/absent/incompatible）+ **幽灵 leader 检测**（持有者不在档案/非 live/无调度位 → absent+ghost）；create 回执（工具 + HTTP）注入 `view.notice`（非在岗/不兼容不拦创建、给出"不会准时触发 + 修复动作"；在线无噪音）；`/schedule` 面板头部与 daemon `/status.presence` 同字段口径。③ `Leader.StoreCompatCheck`：选主前每轮 + 持租约续租前自检，store 超出二进制支持档 → 释放租约 + `StateIncompatible` 让位。实机：runtime-server 持租约时 create 回执 `driver_online`（store v6/v6）；daemon 前台 `/status` presence 字段一致且 standby 正常。边界：aicli 作为调度者的 opt-in 能力位已由 S5a 补上（运行期 `AddCapabilities` 宣告 scheduler 位） |
| S4 | 交付账本 + 持久 attempts + group claim + 死信状态 | ✅ 已落地（2026-10-14，S4a+S4b）。① store v7：`attempt_count`/`last_attempt_at`/`dead_lettered_at`（簿记写不推进 state_version）。② **组级 claim 转正**（§4.3/D3）：`TaskClaimer` 一次事务多行 CAS（`owner_instance_id`/`lease_expires_at`：未持有/本人/租约过期可抢）+ Runtime 集成（drive 前 claim；抢不到 → 延后 15s 重扫 + `ClaimConflictsTotal`；长执行前续期到 `embeddedTurnTimeout+60s`；成功释放 + 清 attempts）。③ leader 易主经 `hydrateAttempts` 恢复退避（不风暴重试）。实机：runtime-server（store v7）投递失败 → attempt_count=1 落库、claim owner=节点 id、pending 兜底，清理无残留。S4b：`schedule.misfireGrace` 显式配置（0=总是补发；>0 时一次性任务超窗不投递、转 inactive、计 `misfireSkippedTotal`；周期任务沿用“只补最近一次”，单测钉住）、`Leader.Takeovers` 接管计数、daemon `/status.stats` 账本对账（active/inactive/pending/ledger 条目，实测一致）。M2（epoch fencing）已落地（2026-10-15，见 M1/M2 行） |
| S5 | 可选增强：aicli 参与 driver（opt-in，含优先级让位 v2）/ 跨节点 headless 路由 / `mesh.Spawn` 联动 | 🔶 部分落地（2026-10-15，S5a）：**aicli 参与调度领导权（opt-in）+ 优先级让位 v2**。① aicli 在 `schedule.driver.participants` 含 `aicli` 时，于**会话门控安装点**启动 `schedulehost.Leader`（与异步宿主挂载解耦；复用本地宿主 service；远端 runtime-server 执行器模式下自带 store/service；L1 当前会话注入 → L2 mesh → L3 HTTP；不装配内嵌执行核）；② 参战即经 `mesh.Host.AddCapabilities` 运行期宣告 `scheduler` + store 兼容位（节点档案先写、参战决策后做的时序）；③ `DeferToHigherPriority`（daemon > runtime-server > aicli）：存在更高优先级在岗者时不抢租约、上线即主动让位（不等 TTL）。单测：优先级词表 / 在岗判定 / 不抢 / 主动让位 / `AddCapabilities` 幂等。实机（headless aicli）：`schedule-driver: joined as node-… (priority=3)` → `candidate → leader (lease acquired)`；**驱动与会话宿主生命周期解耦**（在门控安装点启动，不等交互式 TUI 的异步能力面挂载——后者实测可能到首个 turn 才发生）。实机验证矩阵（2026-10-15）：无更高优先级在岗 → aicli `candidate → leader (lease acquired)`（租约 owner=aicli、epoch 3）；runtime-server 在岗 → `deferring to higher-priority scheduler (runtime-server)` → standby 不抢。**未启动**：跨节点 headless 执行路由、`mesh.Spawn` 联动 |
| M1/M2 | mesh 租约：`Renew` 进 `.claim` 锁 + `Epoch`（fencing） | ✅ 全部落地。M1（2026-10-14，随 S1，含回归测试）；M2（2026-10-15）：`Lease.Epoch` fencing token（新建=1、接管=旧值+1、续约保持、legacy 0 兼容；加性字段、schema 不变）+ `Leader` 续约期 epoch 校验（失配即让位，legacy 不误判）+ `LeaderStatus.Epoch`/`doctor`/`presence.lease_epoch`/`/status` 暴露。实机：epoch 1 → 强杀（不释放）→ 回收接管 → epoch 2 |
