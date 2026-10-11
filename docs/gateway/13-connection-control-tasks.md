# 13 · 连接控制面实现任务清单

> 上游：[12-connection-control.md](./12-connection-control.md)（设计）、[02-architecture.md](./02-architecture.md)、[06-implementation-plan.md](./06-implementation-plan.md)
> 本文把 [12](./12-connection-control.md) 拆成**文件级、可勾选**的实现任务：改动点 + 测试点 + 验收。
> 路径均基于当前仓库真实结构。

---

## 0. 交付物总览

| 工作流 | 目标 | 主要落点 |
|--------|------|----------|
| WS1 | 共享连接控制接口 | `backend/internal/gateway/agent/`（新增包） |
| WS2 | aicli 宿主接线（TUI 命令 + 启动） | `backend/cmd/aicli/commands/`、`backend/cmd/aicli/pprof.go` |
| WS3 | runtime-server 宿主接线（API + 配置） | `backend/internal/api/runtimeapi/`、`backend/internal/runtimeserver/` |
| WS4 | 前端入口 | `frontend/src/api/runtime/`、`frontend/src/components/workspace/settings/` |
| WS5 | 集成/E2E 与文档 | `scripts/`、`docs/gateway/` |

> 依赖顺序：WS1 →（WS2 ∥ WS3）→ WS4 → WS5。

---

## WS1 · 共享连接控制接口（`internal/gateway/agent`）

| # | 文件 | 改动 | 测试 |
|---|------|------|------|
| 1.1 | `internal/gateway/agent/state.go`（新） | 定义状态枚举 `disabled/disconnected/connecting/connected/reconnecting/disconnecting/error` 与状态迁移表 | `state_test.go`：合法/非法迁移 |
| 1.2 | `internal/gateway/agent/status.go`（新） | `Status` 结构：`state/node_id/gateway_url/console_url/connected_at/last_heartbeat/last_error/reconnect_count/sessions/mode(operator_disconnected)`；`Status()` 只读快照 | `status_test.go`：字段与 JSON 形状（golden） |
| 1.3 | `internal/gateway/agent/control.go`（新） | `Controller` 接口 + 实现：`Connect/Disconnect/Reconnect/Status/Configure`；`operator_disconnected` 抑制自动重连；认证失败不重连 | `control_test.go`：幂等、并发串行化、断开抑制重连、`auth_invalid` 不重连 |
| 1.4 | `internal/gateway/agent/config.go`（新） | `Config{Enabled,URL,Token,Name,AutoConnect,Routes}`；`token_hint` 派生（只暴露前 4 位） | `config_test.go`：`token_hint` 不泄露原文 |
| 1.5 | `internal/gateway/agent/agent.go`（新/骨架） | 出站连接、握手、心跳、注册（供 [03](./03-protocol.md) 协议实现）；被 `control.go` 驱动 | `agent_test.go`：内存环回最小连接/断开 |

**验收**：`Controller` 可被两宿主以相同方式驱动；`Status` 在两宿主输出一致。

---

## WS2 · aicli 宿主接线

| # | 文件 | 改动 | 测试 |
|---|------|------|------|
| 2.1 | `backend/cmd/aicli/commands/chat_gateway.go`（新） | `/gateway` 命令实现：`status/connect/disconnect/reconnect/url/enable/disable/help`；输出见 [12](./12-connection-control.md) §4.2 | `chat_gateway_test.go`：各子命令、缺配置报错、错误态文案 |
| 2.2 | `backend/cmd/aicli/commands/chat_slash_command_catalog.go` | 注册 `/gateway` 条目（帮助/别名/参数说明） | 既有 catalog 测试回归（补全列表含 `/gateway`） |
| 2.3 | `backend/cmd/aicli/commands/chat_slash_argument_completion.go` | `/gateway` 子命令补全（`status/connect/...`） | 补全测试：子命令候选正确 |
| 2.4 | `backend/cmd/aicli/commands/chat_busy_command_policy.go` | 忙时策略：`/gateway status|url` 允许忙时（只读）；`connect|disconnect|enable|disable|reconnect` 非忙时 | 忙时策略测试（I/S 档） |
| 2.5 | `backend/cmd/aicli/commands/chat_input_queue.go` | `chatSlashCommandQueueSafe` 归类：`/gateway status|url` 可排队安全；写类不可静默延迟 | 队列安全测试 |
| 2.6 | `backend/cmd/aicli/commands/agent_stdio_slash.go` | 无头/ACP 仅暴露 `status`（其余过滤） | ACP 命令列表测试 |
| 2.7 | `backend/cmd/aicli/commands/chat_actor_host.go` | 宿主持有 `agent.Controller`；web server 就绪后按配置自动 `Connect`（对齐 hook 接线风格，见 `chat_actor_host.go:2541`） | 宿主接线测试：配置启用→自动连接；未配置→`disabled` |
| 2.8 | `backend/cmd/aicli/pprof.go` | 回环 web server 启动后触发 agent 自动连接；启动日志 `Info: gateway <state> node=<id> console=<url>`（stderr） | 启动日志测试（无 TTY 可捕获） |
| 2.9 | `backend/cmd/aicli/commands/chat_options.go`（或 flags） | `--gateway/--gateway-token/--gateway-name/--gateway=false` 解析并写入配置层 | flag 解析测试 |

**验收**：TUI 内 `/gateway` 全子命令可用；`disconnect` 不改配置；`disable` 写配置且重启不自动连。

---

## WS3 · runtime-server 宿主接线

| # | 文件 | 改动 | 测试 |
|---|------|------|------|
| 3.1 | `backend/internal/api/runtimeapi/gateway_handlers.go`（新） | `GetGatewayStatus` / `ConnectGateway` / `DisconnectGateway` / `ReconnectGateway` / `GetGatewayConfig` / `UpdateGatewayConfig`；错误统一信封；`token` 不回传 | `gateway_handlers_test.go`：状态/连断/配置、令牌脱敏、非本机写被拒 |
| 3.2 | `backend/internal/api/runtimeapi/handler.go` | `registerGatewayRoutes(runtimeRouter)`，注册 `/gateway/*`（对齐 `/config/agent/max-steps` 风格，`handler.go:980`） | 路由注册测试（路径+方法） |
| 3.3 | `backend/internal/runtimeserver/gateway_config.go`（新） | 配置段 `gateway` 的读写与持久化（复用现有配置文档服务/写入锁）；`auto_connect` 语义 | 配置读写/分层合并测试 |
| 3.4 | `backend/internal/runtimeserver/service_api.go`（或 app 装配） | 启动时按配置创建并 `Connect` `agent.Controller`；`shutdown` 时 `Disconnect` | 启动/停止装配测试 |
| 3.5 | `backend/cmd/runtime-server/main.go` | 注入 `agent.Controller` 到 handler；`--gateway*` flag 透传 | 启动测试（配置启用→自动连接；未配置→disabled） |

**验收**：`/api/runtime/gateway/*` 可用；写操作返回更新后状态；`disconnect` 不改配置；`auto_connect=false` 持久化。

---

## WS4 · 前端入口

| # | 文件 | 改动 | 测试 |
|---|------|------|------|
| 4.1 | `frontend/src/api/runtime/gateway.ts`（新） | API client：`getGatewayStatus/connectGateway/disconnectGateway/reconnectGateway/getGatewayConfig/updateGatewayConfig`（对齐 `api/runtime/config.ts` 风格） | `gateway.test.ts`：URL/方法/错误信封 |
| 4.2 | `frontend/src/api/runtime/index.ts` | 导出 gateway 模块 | 类型检查 |
| 4.3 | `frontend/src/components/workspace/settings/gateway-settings-page.tsx`（新） | 设置页：状态卡 + 连接/断开/重连按钮 + 配置（url/令牌/名称/自动连接）；过渡态禁用；`error` 态显示 `reason/hint` | `gateway-settings-page.test.tsx` |
| 4.4 | `frontend/src/components/workspace/settings/settings-dialog.tsx` | `SettingsSectionId` 增加 `"gateway"`；sections 数组与 render switch 增加分支（见 `settings-dialog.tsx:33/128/251`） | `settings-dialog` 回归 |
| 4.5 | 侧栏/状态栏指示灯（可选） | 网关连接状态指示（绿/黄/红），点击打开设置页 | 组件测试 |
| 4.6 | i18n | `settings` 命名空间补 gateway 文案 | i18n 校验脚本 |

**验收**：前端可查看状态、连/断/重连、保存配置；过渡态按钮禁用；令牌输入掩码且不回显。

---

## WS5 · 集成 / E2E 与文档

| # | 项 | 说明 |
|---|----|------|
| 5.1 | 集成测试 | 本地起 `httptest` 网关 + 真实 `agent`：`connect → REGISTER → disconnect → DEREGISTER → 状态回退` |
| 5.2 | E2E | 真实 `aicli` 进程：`/gateway disconnect` 后网关侧 node 转 offline；`/gateway connect` 后恢复 |
| 5.3 | runtime-server E2E | 前端 Playwright：设置页连接/断开/重连；断言状态与按钮禁用 |
| 5.4 | 回归 | 未配置网关时 `aicli`/`runtime-server` 行为与基线逐字一致 |
| 5.5 | 文档 | 更新 [12](./12-connection-control.md) 落地状态；必要时补 `docs/aicli/` 命令说明 |

---

## 关键实现约束（避免踩坑）

1. **运行态 vs 持久化**：`disconnect` 只置 `operator_disconnected`，**绝不写配置**；只有 `enable/disable` 与 `auto_connect` 写配置。测试必须钉住这条。
2. **认证失败不重连**：`auth_invalid` → `error` 且停止重连（防风暴）；UI/TUI 提示重新签发令牌。
3. **令牌脱敏**：控制 API 与前端只暴露 `token_hint`；配置写入 `token` 省略=保持原值。
4. **幂等与串行**：`Connect/Disconnect/Reconnect` 内部互斥；重复点击/重复调用不产生多条隧道。
5. **本地 owner only**：控制面仅 TUI 或本机 runtime-server（回环/admin token）；网关侧无反向强制连接入口。
6. **降级**：网关不可用时本地功能零影响；命令/接口给出明确提示而非报错崩溃。
7. **忙时安全**：只读子命令（`status/url`）允许忙时；写类子命令不得被队列静默延迟。

---

## 建议的提交/PR 序列

1. **PR1（WS1）**：`internal/gateway/agent` 控制接口 + 状态机 + 单测（无宿主接线）。
2. **PR2（WS2）**：aicli `/gateway` 命令 + 自动连接 + 忙时/补全/ACP + 单测。
3. **PR3（WS3）**：runtime-server `/api/runtime/gateway/*` + 配置持久化 + 装配 + 单测。
4. **PR4（WS4）**：前端 api client + Gateway 设置页 + 单测。
5. **PR5（WS5）**：集成/E2E + 文档收口。

每个 PR 的 DoD：单测通过、`go test ./...`（受影响包）、`tsc`/`vitest`（前端）、未配置网关时行为不变。

---

## 完成判据（Definition of Done）

- [ ] `aicli TUI`：`/gateway` 全子命令可用，`disconnect` 抑制自动重连且不改配置，`disable` 持久化。
- [ ] `runtime-server`：`/api/runtime/gateway/*` 可用，前端设置页可操作，令牌不回传。
- [ ] 认证失败进入 `error` 且不重连，提示可操作。
- [ ] `connect/disconnect/reconnect` 幂等；并发不产生多隧道。
- [ ] 控制面仅本机 owner 可触发。
- [ ] 未配置网关时，两宿主行为与基线一致（回归通过）。
- [ ] 集成/E2E 覆盖"连→注册→断→注销→重连→恢复"。
