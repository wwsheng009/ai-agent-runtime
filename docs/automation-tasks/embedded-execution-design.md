# 内嵌执行（Embedded Execution）设计

> 目标：让调度守护进程 `cmd/scheduled` 在**无人值守**下**自己执行**会话 turn，不依赖 runtime-server 在线。
> 定位：[`scheduler-daemon-design.md`](./scheduler-daemon-design.md) 的**可选执行模式**（默认 `delegate`；本设计给出 `embedded`/`auto`）。
> 结论：**执行栈当前由 `runtimeapi.Handler` 拥有**（`session_runtime_support.go:4502` 惰性建 `chat.NewBoundedSessionHub`）；内嵌执行有两条路径——**A 复用 Handler 作库**（快）、**B 抽取 `internal/runtimehost.Executor`**（净）。推荐 A 先落地、B 作后续收敛。所有权由 **mesh 调度租约 + session 租约**双重仲裁。

---

## 1. 目标与三种执行模式

| 模式 | 行为 | 前提 |
|---|---|---|
| `delegate`（默认） | daemon 经 HTTP 让 runtime-server 执行 | runtime-server 在线 |
| `embedded` | daemon 内嵌执行栈，自己跑 turn | daemon 能读 config/凭据/会话库 |
| `auto` | 先 `delegate`，不可达则 `embedded`；再失败落 mailbox | 两者都可用 |

目的：`embedded` 解决"宿主长期不在线 → 提醒只能落库不能执行"的缺口，实现**完全无人值守**。

---

## 2. 现状：执行栈在哪里（核验）

- **会话执行栈由 `runtimeapi.Handler` 拥有**：`h.getSessionHub()` 惰性创建 `chat.NewBoundedSessionHub(factory)`（`internal/api/runtimeapi/session_runtime_support.go:4502`）；actor 工厂在其中装配 provider/tools/profile。
- `internal/bootstrap.Manager` 提供 `Registry()` / `Loader()` / `TeamStore()`（`internal/bootstrap/manager.go:649-685`）。
- `cmd/runtime-server` 的装配：`bootstrapManager := runtimebootstrap.NewManager(...)` → `handler := runtimeapi.NewHandler(registry, loader, mcpAdapter)` → `handler.Set*` → `handler.RegisterRoutes(router)` → HTTP 监听（`main.go:1076-1309`）。

**含义**：内嵌执行 = 在 daemon 内构造同一套 hub + actor 工厂 + provider + tools，但**不启动 HTTP**。

---

## 3. 两条实现路径

### 路径 A：复用 `runtimeapi.Handler` 作为库（推荐先落地）
- daemon 按 `runtime-server/main.go` 的方式构造 `bootstrap.Manager` + `runtimeapi.NewHandler(...)` + 必要的 `Set*`；
- **不调用** `RegisterRoutes` / 不 `ListenAndServe`；直接调用 Handler 的会话执行方法（或 `getSessionHub()` 暴露的 hub）。
- 优点：改动最小、能力最全（工具/MCP/skills/provider/事件持久化全部就绪）。
- 代价：daemon 链接整个 API 层（二进制增大；runtime-server 本就如此）。

### 路径 B：抽取 `internal/runtimehost.Executor`（后续收敛）
- 把 hub + actor 工厂 + 事件持久化 + provider 配置 + 工具注册抽成 `runtimehost.Executor`；
- `runtimeapi.Handler` 改为**持有** `*runtimehost.Executor`（而非自建 hub）；
- `cmd/runtime-server` 与 `cmd/scheduled` 共用 `Executor`，HTTP/UI 只是它之上的一层。
- 优点：层次干净、daemon 不背 HTTP 包；代价：真实重构（触及 `runtimeapi` 大量会话路径）。

**推荐**：D3 用 A 落地 `embedded`，作为 B 的过渡与验证；B 在独立重构批次做。

---

## 4. 组件与依赖

| 需要 | 不需要 |
|---|---|
| 配置加载（`internal/config`） | HTTP 路由/监听（`RegisterRoutes`/`ListenAndServe`） |
| `bootstrap.Manager`（Registry/Loader/TeamStore） | `internal/webui`（前端产物） |
| provider 配置与凭据（`BuildRuntimeProviderConfigs`） | MCP 管理 UI / config UI |
| 会话存储（`chat` sqlite storage） | gateway / console |
| `chat.SessionHub` + `SessionActor` + agent loop | — |
| 工具注册（`toolkit`/`toolbroker`，按 profile 裁剪） | — |
| 事件持久化（`internal/events`） | — |

---

## 5. 所有权仲裁（关键：避免双执行者）

两把锁，**缺一不可**：

1. **调度租约（全局）**：`mesh.Acquire(paths, "schedule-scheduler", "global", …)` —— 只有一个 daemon 触发（见 daemon 文档 §4）。
2. **会话租约（per-session）**：`internal/mesh` 的 session lease（`mesh/host.go:441 acquireSessionLeaseLocked`、`:579 TakeoverSession`）—— 决定**谁执行该会话**。

规则：

| 会话状态 | daemon 行为 |
|---|---|
| 被其他宿主（aicli/runtime-server）持有 | **只投递**（HTTP 或持久 mailbox），**不执行** |
| 空闲/无人持有 | daemon **获取 session 租约后 embedded 执行**，执行完释放 |

这样 daemon 与交互会话**永不并发跑同一会话**；用户正在聊天的会话，daemon 只排队不抢。

---

## 6. 执行流程（embedded）

```
到期
 └─ mode=delegate → POST …/runtime/commands(submit_prompt)
      成功 → 写回执 + 推进
      失败 → mode=embedded? 转下；否则落 mailbox
 └─ mode=embedded:
      1. mesh.Acquire session lease（拿不到 → 落 mailbox，稍后重试）
      2. hub.GetOrCreate(sessionID)            // 冷会话重建
      3. runMeta{source:"schedule"}; SubmitPromptAsync(text, runMeta)
      4. 等待：SubmitPrompt（同步）或轮询 actor 终态，受 maxTurnDuration 约束
      5. 释放 session 租约
      6. 写回执 + 推进目标（同一事务）
 └─ 失败/超时 → 退避重试；达 MaxDeliveryAttempts → inactive + delivery_failed
```

> **回执语义**：`delegate` 确认 = HTTP 2xx；`embedded` 确认 = 提交被 actor 接受（与主方案 §6.3 方案 A 一致）。是否等 turn 完成由 `maxTurnDuration` 决定是否记 `turn_timeout`（不回滚回执）。

---

## 7. 资源与安全

| 项 | 约束 |
|---|---|
| 并发 | `maxConcurrentExecutions`（默认 1）——daemon 串行执行，避免抢占用户机器 |
| 时长 | `maxTurnDuration`（默认 10m）——超时中断并记事件 |
| 工具面 | 用**专用 profile**（如 `scheduled`）限定工具面/权限（呼应 [`tool-surface-inventory-and-naming-review.md`](./tool-surface-inventory-and-naming-review.md) 的工具分组；只读或受限工具组） |
| 工作目录 | 按会话 workspace 绑定；缺失时用配置默认 |
| 凭据 | 从同一 config/凭据存储读取（服务账户需有权限） |
| 权限模式 | 建议 `dont_ask`/受限，避免服务态弹出审批（无人应答） |
| 网络 | 仅需 provider API 出站；不需入站 |

**审批语义**：无人值守下，审批类工具必须被 profile/策略**预先拒绝或降级**（不能挂起等待）。这一点必须在 `scheduled` profile 里显式配置。

---

## 8. 配置

```yaml
schedule:
  execution:
    mode: auto                 # delegate | embedded | auto
    runtimeServerURL: http://127.0.0.1:PORT   # delegate 用
    profile: scheduled         # embedded 用的受限 profile
    maxConcurrentExecutions: 1
    maxTurnDuration: 10m
    fallbackToMailbox: true    # 拿不到会话租约/执行失败时落持久 mailbox
```

---

## 9. 与既有方案的关系（需同步）

| 文档/章节 | 修订 |
|---|---|
| [`scheduler-daemon-design.md`](./scheduler-daemon-design.md) §2 | 三平面 → **执行面可选**：delegate（宿主执行）或 embedded（daemon 执行） |
| 同上 §6 | 投递链：`delegate → embedded → mailbox`（三级兜底） |
| 同上 §10 风险 1 | "执行仍依赖宿主" → 由 `embedded` 模式消除 |
| 主方案 §6.3 | 补 embedded 注入入口（daemon 内 `hub.GetOrCreate` + `SubmitPromptAsync`） |
| 主方案 §6.2 | 明确 daemon 可选内嵌执行；调度租约 + 会话租约双重互斥 |

---

## 10. 分期

> **与主方案映射**：E0–E3 → 主方案 **P5**（内嵌执行）；E3（路径 B 重构）为主方案 P5 的可选收敛项。

| 阶段 | 交付 | 验收 |
|---|---|---|
| E0 | 路径 A：daemon 构造 `bootstrap.Manager` + `runtimeapi.Handler`（不启 HTTP）；`mode` 配置 | daemon 能对空闲会话执行一次提醒 turn |
| E1 | session 租约仲裁 + `scheduled` profile（受限工具面/无审批） | 用户正在聊天的会话，daemon 不并发执行（只排队） |
| E2 | 并发/时长上限 + 观测（执行数、超时、失败原因） | 超时中断并留痕；并发不超上限 |
| E3 | 路径 B：抽取 `internal/runtimehost.Executor`，runtime-server 与 scheduled 共用（可选） | 两宿主共用同一执行核心 |

---

## 11. 风险

1. **二进制/依赖膨胀**：路径 A 让 daemon 链接整个 API 层；用 `-tags` 或路径 B 收敛。
2. **双执行者竞态**：由调度租约 + session 租约双重互斥缓解；仍须在集成测试覆盖。
3. **凭据/权限**：daemon 以服务账户运行，需读 config/凭据与会话库；文档须说明提权与权限。
4. **无人值守审批**：必须在 profile 预置拒绝/降级，否则挂起。
5. **资源竞争**：daemon 执行会占用 CPU/网络；并发默认 1，并提供上限。
6. **行为差异**：embedded 与 runtime-server 的执行路径必须同源（路径 A 天然同源；路径 B 需保证）。
