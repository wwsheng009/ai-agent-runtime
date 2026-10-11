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
