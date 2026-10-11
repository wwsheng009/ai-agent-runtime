# 实施状态（P0）

> 依据：[`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md) §10 统一路线图。
> 状态：**P0 已完成并通过编译/单测**；P1 起未开始。

---

## 1. 已交付（P0）

| 步骤 | 交付 | 文件 | 验证 |
|---|---|---|---|
| P0-1 | 类型/校验/错误码 | `backend/internal/schedule/types.go`、`errors.go` | ✅ 单测 |
| P0-2 | 领域层 `after`/`at`/`every` 创建 + 发生点计算 | `backend/internal/schedule/domain.go` | ✅ 单测（含 XOR、未来性、最小间隔、错过推进） |
| P0-3 | SQLite store + `migrate` + `state_version` CAS + owner 列 | `backend/internal/schedule/store.go` | ✅ 单测 |
| P0-4 | `Service`（Create/List/Catalog/Get/Delete/Update）+ 单写者队列 | `backend/internal/schedule/service.go` | ✅ 单测（含冲突/未命中） |
| P0-5 | `cmd/scheduled` 前台守护进程 + 单定时器运行时（投递 + 重试退避）+ mesh 单实例租约 | `backend/cmd/scheduled/main.go`、`internal/schedule/runtime.go` | ✅ 单测（假时钟 + 记录型投递器） |
| P0-6 | `schedule_*` 四工具 + 参数审计表 + 定义门控 | `internal/toolbroker/schedule_tools.go`、`broker.go`、`broker_arg_audit.go` | ✅ 单测（往返 + 门控 + 错误码） |
| P0-7 | `ScheduleConfig` + 默认值 + `WithDefaults` | `internal/config/manager.go` | ✅ 配置测试 |
| P0-8 | aicli 宿主接线（`host.Schedule` → `broker.Schedule`） | `cmd/aicli/commands/chat_actor_host.go`、`chat_schedule_host.go` | ✅ `go build ./...` |

**门控语义**：`schedule_*` 工具只在 `Broker.Schedule != nil` 时进入 `Definitions()`；aicli 宿主在 `cfg.Schedule.Enabled` 时创建服务，因此默认**不改变现有工具面**。

## 2. 与设计的差异（P0 范围内）

| 设计 | P0 实现 | 说明 |
|---|---|---|
| `runMeta.source="schedule"` | 用提醒固定文本（`[SCHEDULE REMINDER]`）标识来源 | 本项目 `team.RunMeta` 无通用 `source` 字段 |
| 投递回执 messageId | 生成稳定合成 id（`schedule:<id>:<ms>`） | delegate 的 HTTP 响应未回传真实消息 id；P1 可改为从响应解析 |
| 持久 mailbox 兜底 | 未实现（P0 仅 delegate） | 归 P2（D1） |
| 子 Agent 拒绝 | 未实现 | 归 P1（§8.2） |
| daily/weekly/cron | 未实现（返回 unsupported） | 归 P1 |

## 3. 验证命令

```
cd backend
go build ./...
go test ./internal/schedule/... ./internal/toolbroker/... ./internal/config/...
go vet ./internal/schedule/... ./internal/toolbroker/ ./cmd/aicli/commands/ ./cmd/scheduled/...
```

## 4. 未完成（后续阶段）

- **P1**：`daily/weekly/cron` + DST；`schedule_update` 的 clock-kind 变更；子 Agent 拒绝；归档准入；投递历史表读写与分页；HTTP API（`internal/api/runtimeapi/schedule_handlers.go`）；`schedule.changed` 事件。
- **P2**：持久 mailbox 兜底 + 重试/死信计数；服务化安装（Windows Service / systemd）；`healthz`/`status`。
- **P3/P4**：E1 frontend、E2 micro web、E3 TUI。
- **P5**：内嵌执行。
- **P0-8 补**：runtime-server 侧（`internal/api/runtimeapi`）的 broker 接线（当前仅 aicli 宿主接了）。
