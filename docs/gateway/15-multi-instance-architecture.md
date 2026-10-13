# 15 · 多实例架构与实例间通讯：现状、边界与改进建议

> 上游：[10-runbook.md](./10-runbook.md) §13（主备切换）、[08-notifications.md](./08-notifications.md) §10.2（双活待办）
> 关联：[docs/bots/](../bots/README.md)（IM 连接器方案，其部署约束由本文界定）

## 1. 问题界定

"gateway 之间的通讯"在本项目里有两种可能含义，**结论完全不同**，先分开：

| 含义 | 现状 | 结论 |
|------|------|------|
| A. 多个 gateway 实例之间的协调（主备/双活/水平扩展） | **不存在实例间协议**；主备靠"共享数据目录快照 + agent 自动重连"实现 | 见 §4：**近期不需要新增协议**，需要的是"单实例约束 + 共享协调抽象" |
| B. gateway ↔ agent 的隧道通讯 | 已有完整协议（WS 多路复用、心跳、GOAWAY、fencing） | 见 §5：仅两处值得顺手加固（快照原子写、连接器流量分级） |

本文以 A 为主，并回答"IM 连接器落地是否要求先改网关间架构"——**答案：不要求；但需要明确并校验部署约束。**

## 2. 现状盘点（含证据）

| 能力 | 状态 | 证据 |
|------|------|------|
| 优雅下线广播 GOAWAY | ✅ | `cmd/gateway/main.go:186-188,736-739`（SIGINT/SIGTERM → GOAWAY + 快照落盘） |
| 注册表快照持久化（防抖） | ✅ | `internal/gateway/tunnel/persist.go:16-26,44-51` |
| 快照恢复为 stale 条目 | ✅ | `persist.go:108-114`；runbook §13.2/§13.3 |
| 稳定 node id（agent 侧） | ✅ | runbook §13.4（`~/.aicli/gateway-node-id`） |
| 主备演练（真实进程） | ✅ | `backend/.tmp/ha-e2e/failover_e2e.ps1`（runbook §13.4） |
| **实例间通讯协议（gossip/leader/etc）** | ❌ 无 | `cmd/gateway` 无 instance-id/锁/租约（grep `flock|instance-id` 无命中） |
| **数据目录写租约 / 防双写** | ❌ 无 | 快照为 `os.WriteFile`（`persist.go:90`，**非原子**：无 temp+rename） |
| **presence / 推送去重 / 连接器状态** | ⚠️ 进程内存 | `cmd/gateway/presence.go`、`push.go:24-43`（`seen` map）；08 §10.2 已记录 |
| **/n/、/s/ 反代与 node 归属** | ⚠️ 依赖"该实例持有该 node 隧道" | `main.go:701+`（反代直接查本进程注册表） |
| 双活（active/active） | ❌ 待办 | 08-notifications.md §10.2（需共享存储 + 节点归属路由 + SSE 广播层） |

**关键事实**：当前架构是**"单写者"模型**——同一时刻只有一个实例应处于活跃状态；standby 只读快照。
该模型在主备下成立，但它是一个**部署约定，而非代码强制**（无锁、无实例身份），存在 split-brain 风险：
两个实例同时活跃会互相覆盖 `registry.json`、重复推送通知、重复消费轮询（对 IM 是致命的：Telegram
`getUpdates` 双消费者会互相抢消息并 409）。

## 3. 需求分析：什么时候才真的需要"实例间通讯"

| 目标形态 | 需要什么 | 是否需要实例间协议 |
|----------|----------|--------------------|
| 单实例（当前线上） | 无 | ❌ |
| 主备热切换（当前已支持） | 共享数据目录 + 部署纪律（只跑一个 active） | ❌（但需要**防双写保护**） |
| 连接器与网关同机、单实例运行 | 单实例约束 + 启动校验 | ❌ |
| 双活/水平扩展 | ① node 归属路由（/n/、/gw/agent 必须落到持有隧道的实例）② 共享协调状态（presence、推送去重、轮询所有权、连接器账本）③ 通知广播或"只投递自己拥有的目标" | ⚠️ **仍然不需要 P2P 协议**；需要**共享协调层 + 归属路由** |
| 跨机房容灾 | 以上 + 数据同步/一致性策略 | 同上（协调层换成 etcd/Redis/DB） |

**最佳实践判断**：为"实例间通讯"引入自研 gossip/raft 是**过度设计**。本项目的正确路径是
**"共享协调状态（Lease/Registry）+ 归属路由（LB/edge 或查询接口）"**，即把"谁拥有什么"外置为可查询状态，
而不是让实例互相对话。

## 4. 结论（直接回答）

1. **不需要**为了 IM 连接器去改"gateway 之间的通讯协议"；
2. **需要**三项"非协议"改进，按优先级：
   - **P0（部署约束 + 防护，零架构改动）**：单实例约束显式化 —— `--instance-id` 标识 + 数据目录写租约
     （flock，standby 只读/或启动即拒绝）+ 快照原子写（temp+rename）；webhook 由 LB 粘性路由到 active；
     连接器启动时校验"本实例是否为唯一 active"。
   - **P1（抽象 + 单机实现）**：把"共享状态"抽成接口 `internal/cluster`
     （`Lease` / `Ownership` / `DedupLedger` / `PubSub`），默认提供**单进程内存实现**（行为与现在一致），
     为将来的 Redis/Postgres 实现留位；`presence`、推送去重、连接器轮询所有权、连接器账本统一走该接口。
   - **P2（真双活时才做）**：分布式实现 + **归属路由**：网关暴露 `GET /api/cluster/owners`（或写入共享 KV），
     由前置 edge/LB 按 node 归属把 `/n/{node}`、`/gw/agent`、webhook 路由到持有实例；通知侧改为
     "每个实例只投递自己拥有的目标" 或订阅共享事件流。
3. **不建议**：自研实例间消息总线、raft 选主、跨实例直连隧道——收益低、风险高，且与现有单进程哲学冲突。

### 4.1 为什么 P0 的"防双写"必须做（与 IM 连接器强相关）

| 连接器能力 | 双实例同时活跃时的后果 | P0 防护 |
|------------|------------------------|---------|
| webhook 入站 | 平台重试/双投 → 重复触发 turn（有去重账本可挡，但账本是进程内存） | LB 粘性 + P1 共享账本 |
| 轮询/WS（Telegram/QQ/微信） | **双消费者抢消息**（409/丢消息） | 轮询所有权（P1）；P0 靠单实例约束 |
| IM↔session 绑定与游标 | 两实例各写一份，会话错乱 | P1 共享 store |
| 通知去重（DedupKey） | 同一提醒双响（等于回到 G2 的老问题） | P1 共享账本 |
| `/n/` 反代 | 落错实例 → 502/404 | P2 归属路由（P0 单 active 时天然正确） |

## 5. gateway ↔ agent 侧值得顺手加固的两点

1. **快照原子写**：`os.WriteFile` → `temp + fsync + rename`（`persist.go:90`）。
   当前崩溃在写入中途会留下截断 JSON；`LoadSnapshot` 容错为"从空开始"，等于静默丢注册表快照。
2. **隧道流量分级/背压**：连接器上线后，网关同时承载"agent 隧道 + SSE + 连接器出站"。
   现有 SSE hub 是 drop-oldest（`cmd/gateway/main.go:53-59`，合理），但连接器出站尚无背压模型——
   按 [docs/bots/05](../bots/05-review-and-hardening.md) §2.3 的有界队列与丢弃策略执行即可，无需改隧道协议。

## 6. 目标形态（若将来做双活）

```text
                    ┌────────── 前置 LB / edge（按 ownership 路由）──────────┐
console ─────────▶  │                                                        │
                    └───────┬───────────────────────────┬────────────────────┘
                            ▼                           ▼
                  gateway-1 (active)             gateway-2 (active)
                    │  └── node A,B 隧道             │  └── node C 隧道
                    │                                │
                    └────────┬───────────────────────┘
                             ▼
                共享协调层（Postgres / Redis / etcd）
                · Lease：主备/轮询所有者
                · Ownership：node→instance 映射（供 LB 查询）
                · DedupLedger：通知/入站去重
                · PubSub（可选）：跨实例事件广播
```

> 服务器上已有 PostgreSQL（`postgresql@16-main`），**优先复用**它做协调层，避免引入 Redis 新依赖。

## 7. 分级落地清单

| 级别 | 项 | 落点 | 验收 |
|------|----|------|------|
| P0 | 快照原子写（temp+rename+fsync） | `internal/gateway/tunnel/persist.go` | 注入写中断不产生截断文件；单测 |
| P0 | `--instance-id` + 数据目录写租约（flock）；standby 只读启动 | `cmd/gateway/main.go` | 双实例同数据目录：第二个实例启动即拒绝并给出明确提示 |
| P0 | 连接器单实例约束与启动校验（`connectors.enabled` 仅在 active 实例） | `cmd/gateway/connectors.go`（待建） | 双实例同时开连接器 → 拒绝/告警 |
| P1 | `internal/cluster` 抽象（Lease/Ownership/DedupLedger/PubSub）+ 内存实现 | 新包 | 现有行为不变（回归全绿）；接口有内存实现与契约测试 |
| P1 | presence / 推送去重 / 连接器账本改走 cluster 接口 | `cmd/gateway`、`internal/notify`、`internal/connectors` | 单实例行为不变；切换实现只改装配 |
| P2 | Postgres 协调实现 + `GET /api/cluster/owners` + LB/edge 归属路由 | 新包 + edge 配置 | 两实例各持一段 node，反代与 webhook 正确落点；跨实例通知不重复 |

## 8. 核实（本文断言 ↔ 代码）

| # | 断言 | 证据 | 结论 |
|---|------|------|------|
| C1 | 主备靠 GOAWAY + 快照 + agent 重连，无实例间协议 | `main.go:186-188,736-739`；runbook §13 | ✅ 无协议 |
| C2 | 无实例身份/无锁 | grep `flock|instance-id|InstanceID`（cmd/gateway）无命中 | ✅ 缺口确认 |
| C3 | 快照写入非原子 | `persist.go:88-90`（MkdirAll + WriteFile） | ✅ 需加固 |
| C4 | presence/推送去重为进程内存 | `cmd/gateway/presence.go:17-32`；`cmd/gateway/push.go:24-43` | ✅ 双活阻塞项 |
| C5 | 反代依赖本进程注册表 | `main.go:701+`；`tunnel.Registry` 进程内 | ✅ 归属路由是 P2 前提 |
| C6 | 双活被显式列为待办 | `docs/gateway/08-notifications.md` §10.2 | ✅ 已有记录 |

## 9. 与 IM 连接器方案的关系（结论）

- **不阻塞**：连接器可在当前主备架构下先落地——把"连接器只在 active 实例运行"写成部署约束 +
  启动校验（P0 第 3 项），webhook 由 LB 粘性路由；
- **天然兼容**：`internal/connectors` 的 `Store` 接口与 `Ledger` 从设计上就允许替换为共享实现，
  `internal/notify` 的 `DedupLedger` 同理（见 [docs/bots/06](../bots/06-target-architecture.md) §2.2/§2.6）；
- **顺序建议**：P0（原子写 + 租约 + 单实例校验）→ 连接器 P0 → 需要扩容时再做 P1/P2。
