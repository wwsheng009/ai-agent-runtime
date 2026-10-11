# 12 · 连接控制面（手动连接 / 断开）

> 上游：[02-architecture.md](./02-architecture.md)、[04-security.md](./04-security.md)、[08-notifications.md](./08-notifications.md)
> 实现任务清单（文件级改动点 + 测试点）见 [13-connection-control-tasks.md](./13-connection-control-tasks.md)。
> 需求：除了**进程启动时按配置自动注册**，还需要**手动控制**：
> - `aicli TUI`：新增命令手动**连接 / 断开**（以及重连、查看状态）；
> - `runtime-server`：在**前端**暴露入口，控制是否断开注册 / 手动注册连接。
> 本文定义统一的连接状态机、两种宿主的控制面（TUI 命令 + 前端入口）、语义与优先级、安全约束。

---

## 1. 目标与原则

| 目标 | 说明 |
|------|------|
| 自动 + 手动并存 | 配置可自动注册；用户可随时手动连/断，无需改配置或重启 |
| 语义清晰 | **运行态开关**（本次进程有效）与**持久化开关**（写配置）分开，不互相偷偷改写 |
| 可观测 | 任何时刻可查看状态：state / node_id / 网关地址 / 最近心跳 / 最近错误 / 重连次数 |
| 安全 | 只能由**本机 owner**（TUI 或本机 runtime-server 控制面）触发；网关侧不能反向强制连接 |
| 降级 | 未配置网关时命令给出明确提示；网关不可用时本地功能不受影响 |

---

## 2. 连接状态机

`internal/gateway/agent` 维护单一状态机，两个宿主共用：

```text
                 ┌─────────── disable（持久化 enabled=false）───────────┐
                 ▼                                                      │
   disabled ──enable──▶ disconnected ──connect──▶ connecting ──ok──▶ connected
       ▲                     ▲                        │                 │
       │                     │                        │ fail            │ 断线/心跳超时
       │                     │                        ▼                 ▼
       │                  disconnect ◀──────────── error ───────▶ reconnecting
       │                     │                                          │
       └────── disable ──────┴──────────────────────────────────────────┘
```

| 状态 | 含义 | 自动重连 |
|------|------|----------|
| `disabled` | 配置/持久开关关闭，或本进程未启用网关 | 否 |
| `disconnected` | 已启用但当前未连接（含手动断开） | 取决于是否手动断开 |
| `connecting` | 正在建立连接 / 注册 | — |
| `connected` | 已注册且心跳正常 | 断线自动重连 |
| `reconnecting` | 断线后自动重连中（退避） | 是 |
| `disconnecting` | 正在 `DEREGISTER` 并关闭 | — |
| `error` | 连接/认证失败（如 `auth_invalid`） | 认证类**不重连**；网络类退避重连 |

**关键区分**：

- **手动断开** = 设置 `operator_disconnected=true` → 抑制自动重连，直到手动 `connect` 或进程重启（或配置变更）。
- **认证失败**（`auth_invalid`，令牌撤销/无效）→ 进入 `error` 且**不重连**（避免风暴），提示重新签发令牌。
- **网络失败** → 进入 `reconnecting`，指数退避（见 [03](./03-protocol.md) §6）。

---

## 3. 自动 vs 手动：优先级与持久化

| 层级 | 载体 | 作用 |
|------|------|------|
| 持久化开关 | 配置 `gateway.enabled`（+ `--gateway=false` flag / `AICLI_GATEWAY_URL`） | 决定进程启动时是否自动连接 |
| 运行态覆盖 | `operator_disconnected` / 手动 `connect` | 本次进程内覆盖持久开关，**不回写配置** |
| 显式持久命令 | `/gateway disable` / `/gateway enable`（TUI）、前端"保存配置" | 明确写配置，影响下次启动 |

**优先级**：`运行态覆盖` > `持久化开关`。规则：

1. 启动：若 `gateway.enabled && url && token` → 自动 `connect`；否则 `disabled`。
2. 手动 `disconnect`：进入 `disconnected` 且抑制自动重连；**不改配置**。
3. 手动 `connect`：清除抑制，立即连接；若缺 url/token → 明确报错（提示如何配置）。
4. `disable`（持久）：写 `gateway.enabled=false`，并断开；下次启动不再自动连接。
5. `enable`（持久）：写 `gateway.enabled=true`；若已有 url/token 则立即尝试连接。
6. 进程重启：运行态覆盖不保留（回到按配置启动），除非用户用了 `disable`。

> 这样"断开一下"不会永久改变行为；要永久关闭必须显式 `disable`。避免"手动断开后忘记为什么不再注册"。

---

## 4. aicli TUI：`/gateway` 命令

### 4.1 子命令

| 命令 | 行为 |
|------|------|
| `/gateway` 或 `/gateway status` | 显示连接状态（见 §4.2） |
| `/gateway connect` | 手动连接（缺 url/token 时报错并提示配置方式） |
| `/gateway disconnect` | 手动断开（运行态，抑制自动重连；不改配置） |
| `/gateway reconnect` | 断开后立即重连（等价 disconnect + connect） |
| `/gateway url` | 打印网关注册地址 / 控制台访问地址（含 node_id） |
| `/gateway enable` | 持久启用（写 `gateway.enabled=true`）并尝试连接 |
| `/gateway disable` | 持久关闭（写 `gateway.enabled=false`）并断开 |
| `/gateway help` | 用法说明 |

可选：`/gateway token <tok>` 临时注入令牌（不落盘、仅本次运行，用于排障）——默认不提供，避免令牌进命令历史；如提供需显著提示。

### 4.2 状态输出（示例）

```text
gateway: connected
  node_id     node-aicli-9f3a
  gateway     wss://gw.example.com
  console     https://gw.example.com/n/node-aicli-9f3a/web/
  connected   2026-10-11T10:00:00Z（已 12m）
  heartbeat   3s 前
  sessions    2（busy 1 / attention 1）
  reconnects  0
  mode        auto（配置启用；未手动断开）
```

错误态示例：

```text
gateway: error
  reason   auth_invalid（设备令牌无效/已撤销；不会自动重连）
  hint     在网关重新签发令牌后执行 /gateway connect 或更新配置
```

### 4.3 集成点（落地）

| 项 | 位置 |
|----|------|
| 命令目录 / 帮助 | `backend/cmd/aicli/commands/chat_slash_command_catalog.go`（新增 `/gateway` 条目与参数补全） |
| 命令分发 | 与现有命令同源的分发器（按 name 分发，`/mesh`、`/mcp` 同形） |
| 忙时策略 | `chat_input_queue.go` 的 busy policy：`/gateway status|url` 允许忙时（只读）；`connect|disconnect|enable|disable` 建议非忙时或明确允许 |
| 无头/ACP | `agent_stdio_slash.go`：只暴露可无头执行的子命令（如 `status`），其余过滤 |
| 状态来源 | `internal/gateway/agent.Status()`（两宿主共用） |
| 启动日志 | web server 就绪后打印一行：`Info: gateway <state> node=<id> console=<url>`（stderr，便于无 TTY 捕获） |

### 4.4 CLI 对等（可选）

为脚本/CI 提供对等入口：`aicli gateway status|connect|disconnect|reconnect|enable|disable`。
说明：CLI 子命令作用于**独立的一次性进程**，无法控制已在运行的 TUI；其价值是脚本化配置与排障（例如 `aicli gateway status` 读取配置并试连）。**TUI 内的 `/gateway` 才是控制运行中会话的正确入口。**

---

## 5. aicli 微前端（可选增强）

`aicli` 的微前端（`/web/`）已有"关于/状态"页签。可选加一个 **Gateway 状态卡**：

- 显示 state / node_id / console URL / 最近错误；
- 提供"断开/重连"按钮（写操作，走 `X-AICLI-Token`）；
- 与 TUI 共享同一 agent 状态。

> 非必需（用户明确要求的是 TUI 命令 + runtime-server 前端）；列为可选，保持一致体验。

---

## 6. runtime-server：API + 前端入口

### 6.1 控制 API（`/api/runtime/gateway/*`）

沿用 runtime-server 现有 `handler.RegisterRoutes` 风格（同 `/api/runtime/config/agent/max-steps`）。

| 方法 | 路径 | 用途 | 认证 |
|------|------|------|------|
| `GET` | `/api/runtime/gateway/status` | 状态（state/node_id/url/console_url/heartbeat/last_error/reconnects/sessions/mode） | 回环或 admin token |
| `POST` | `/api/runtime/gateway/connect` | 手动连接（可带 `{url?, token?}` 临时覆盖） | 同上（写） |
| `POST` | `/api/runtime/gateway/disconnect` | 手动断开（运行态，抑制自动重连） | 同上（写） |
| `POST` | `/api/runtime/gateway/reconnect` | 重连 | 同上（写） |
| `GET` | `/api/runtime/gateway/config` | 读取配置（`url`/`name`/`auto_connect`/`token_hint`，**不回传 token 原文**） | 同上 |
| `PUT` | `/api/runtime/gateway/config` | 保存配置（`url`/`name`/`token`/`auto_connect`），`token` 省略=保持原值 | 同上（写） |

约定：

- 写操作返回**更新后的 status**，前端据此刷新；
- 错误沿用统一信封 `{"error":{"code","message"}}`；`token` 永不回传明文（只给 `token_hint`）；
- 运行态 `disconnect` 不改配置；`auto_connect=false` 等价于持久 `disable`（写配置）；
- 控制 API 仅本机可用（回环放行 / admin token），与现有配置写入口同级（见 [04](./04-security.md) §7）。

### 6.2 前端入口

在 `frontend/src/components/workspace/settings/settings-dialog.tsx` 的 `activeSection` 分派中新增 **"Gateway"** 设置页（`gateway-settings-page.tsx`）：

```text
┌─ 设置 › Gateway ───────────────────────────────────────────┐
│ 状态: ● connected    node-aicli-runtime-…                  │
│ 控制台: https://gw.example.com/n/<node>/     [打开]        │
│ 最近心跳: 3s 前    重连: 0    会话: 5                       │
│                                                            │
│ [连接] [断开] [重连]                                       │
│                                                            │
│ 网关地址   [ wss://gw.example.com            ]             │
│ 设备令牌   [ ••••••••（已设置）        ] [更换]             │
│ 节点名称   [ build-server                   ]             │
│ [x] 启动时自动连接（auto_connect）                         │
│                                   [保存配置]               │
└────────────────────────────────────────────────────────────┘
```

要点：

- 按钮在过渡态（`connecting`/`disconnecting`）禁用，避免重复点击；
- `error` 态显示 `reason` 与 `hint`（如 `auth_invalid` → 提示重新签发令牌）；
- `disconnect` 是运行态按钮（不写配置）；`auto_connect` 复选框才是持久化开关；
- 复用现有设置组件（`settings-section.tsx` / `settings-toggle-card.tsx` / `settings-field-card.tsx`）；
- 可选：在顶部状态栏/侧栏显示一个网关连接指示灯（绿/黄/红）。

### 6.3 前端 API 客户端

`frontend/src/api/modules/gateway.ts`（对齐现有 `api/modules/*` 风格）：
`getGatewayStatus` / `connectGateway` / `disconnectGateway` / `reconnectGateway` / `getGatewayConfig` / `updateGatewayConfig`。

---

## 7. 共享 agent 控制接口

`internal/gateway/agent` 暴露统一控制面，两个宿主各自接线：

```go
type Controller interface {
    Connect(ctx context.Context) error       // 手动连接（清除 operator_disconnected）
    Disconnect(ctx context.Context) error    // 手动断开（置 operator_disconnected）
    Reconnect(ctx context.Context) error
    Status() Status                          // state/node_id/url/heartbeat/last_error/reconnects/sessions
    Configure(cfg Config) error              // url/name/token/auto_connect（写配置由宿主负责）
}
```

- `aicli`：TUI 命令调用该接口；状态经现有渲染面输出。
- `runtime-server`：`/api/runtime/gateway/*` 调用该接口；配置读写复用 `internal/runtimeserver` 的配置文档服务。
- 线程安全：`Connect`/`Disconnect`/`Reconnect` 串行化（内部互斥），重复调用幂等。

---

## 8. 安全

| 风险 | 缓解 |
|------|------|
| 谁可以触发连/断 | 仅**本机 owner**：TUI 命令、或 runtime-server 回环/admin token 控制 API；网关侧无反向"强制连接"入口 |
| 令牌泄露 | 控制 API 不回传 token 明文；`/gateway token` 不默认提供；配置写按现有敏感项处理（env/密钥管理优先） |
| 误操作 | `disconnect` 只影响运行态、可一键 `connect` 恢复；`disable` 才是持久化且需显式确认 |
| 重连风暴 | 手动断开置 `operator_disconnected` 抑制自动重连；认证失败不重连 |
| 审计 | `connect`/`disconnect`/`enable`/`disable`/`reconnect` 记录本地日志；网关侧记录 `tunnel.connected`/`disconnected` |

---

## 9. 验收

- [ ] 配置 `gateway.enabled=true` 时启动自动注册；`false` 时不连接。
- [ ] `/gateway status` 显示 state/node_id/console/心跳/错误；`/gateway connect|disconnect|reconnect` 生效。
- [ ] 手动 `disconnect` 后**不自动重连**；`connect` 后恢复；**不修改配置文件**。
- [ ] `/gateway disable` 写配置并断开；重启后不再自动连接；`enable` 恢复。
- [ ] 令牌无效时进入 `error` 且不重连，并给出可操作提示。
- [ ] runtime-server 前端 Gateway 页可查看状态、连接/断开/重连、保存配置；过渡态按钮禁用。
- [ ] 控制 API 不回传 token 明文；非本机/无 token 的写请求被拒绝。
- [ ] 未配置网关时，TUI 命令与前端入口给出明确提示，本地功能不受影响。
- [ ] `connect`/`disconnect` 幂等；并发调用不产生多条隧道。
