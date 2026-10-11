# 实施状态（P0 + P1 + P2）

> 依据：[`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md) §10 统一路线图。
> 状态：**P0、P1、P2 已完成并分批提交**；P3（E1 frontend）起未开始。

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

- **P2 余项**：`DrainPendingMessages` 尚未接进宿主启动路径（当前由 daemon
  暴露能力，宿主激活会话时调用即可补上）；pending 消息的死信上限。
- **P3**：E1 frontend（`/runtime/schedule` 页 + panel + api/hook/i18n）。
- **P4**：E2 micro web + E3 TUI。
- **P5**：内嵌执行（E0–E3）。
- **P6**：E4 gateway、观测收敛、作业终态→提醒桥。
