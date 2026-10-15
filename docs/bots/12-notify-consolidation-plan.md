# 12 · notify 中台收敛：三宿主迁移方案（S4b）

> 上游：[06-target-architecture.md](./06-target-architecture.md) §1 D1（notify 中台 = 唯一权威/SSOT）、
> D4（绞杀者模式：先抽内核保行为，再迁三宿主）、§4.2 N2/N3/N8；策略链语义见
> [10-presence-signals-design.md](./10-presence-signals-design.md) §3.2/§3.3（在场线已落地）。
> 状态：**方案设计（待评审）** ｜ 日期：2026-10-15 ｜ 前置：S4a 已落地（`aabb4777`，
> `notify.Presence` + Dispatcher 门禁）。
> 维护约定：本文是 S4b **切片细节的唯一权威**；04 状态行/06 决策不动，切片完成时回写一行。

## 1. 背景与目标

D4 绞杀者模式第一步（抽内核）已完成：`internal/notify` 已有 kind 登记表、
优先级/免打扰/合并原语（policy.go）、Dispatcher（D6 择通道）、三级偏好（prefs）、
投递账本（ledger）、在场门（S4a presence.go）。

**第二步（本文）**：把三宿主仍各自实现的通知策略路径收敛到内核——宿主退化为
「信号上报者 + 通道适配器」。SSOT 依据（06 D1）：策略只能有一处实现，否则必然漂移。

护栏：gateway 现有全量单测（行为不变）；每切片独立可灰度、可回滚。

## 2. 现状差距矩阵（代码核实 2026-10-15）

| 策略环节 | 内核 | gateway | runtime-server | aicli |
|---|---|---|---|---|
| 择通道（D6：high 扇出/normal 主通道/low 收件箱） | ✅ Dispatcher | ✅ 已走（push.go dispatch） | ❌ IM 投影自定路由（connector_bootstrap.go:105/124 → `NotifyAttentionScoped`） | ❌ 连接器直投 `Service.NotifySession/NotifyAttention`（04 P1） |
| 三级偏好 | ✅ prefs | ✅ 已接 | ❌ | ❌ |
| 在场门 | ✅ S4a | ◐ 双门并存（见 §3 D1） | ❌（sessionViewers 未接内核） | ❌（InteractiveUI 未接内核） |
| 跨通道去重账本 | ✅ Ledger | ❌ **`NewDispatcher(nil,…)` 未接线**（push.go:226） | ❌ | ❌ |
| 静默时段/合并窗口 | ✅ 原语（QuietRange/InQuietHours/Coalesce） | ◐ 调度逻辑仍在 pushNotifier（batch/timer/flush） | ❌ | ❌ |
| 薄适配层 | — | ◐ notify_policy.go（纯别名，可退役） | — | — |

关键代码事实（设计约束的来源）：

- `pushNotifier.dispatch` 每次调用新建 Dispatcher（channels=WebPush+Ntfy、
  SetPrimary("webpush")、SetPreferences、S4a SetPresence）——构造可上提为字段；
- **账本为 nil 不是疏漏**：现 DedupKey = `note.Tag` = `nodeID|sessionID`，同一会话
  先后两种 attention（如 approval→error）会共享同键，接账本会**吞掉第二个事件**——
  键语义必须先解决（§3 D2）；
- 双门竞态：`observe` 决定投递并写入 seen 快照**之后**、`Dispatch` 执行**之前**，
  若在场恰好续期（心跳到达），内核门会抑制该事件且 seen 已记 → **通知丢失**，
  补推不触发（毫秒级窗口，真实存在）。

## 3. 关键设计决定

### D1 双门收敛：保留双门 + Suppressed 回滚 seen

`observe` 的 seen 前置检查是**语义门**（它同时决定"不记 seen → 切走补推"）；
内核 S4a 门是**防御门**（兜住未经 observe 前置检查的生产者）。二者并存，但必须
消除 §2 的竞态：gateway 的 dispatch 循环收到 `Receipt.Suppressed=true` 时，从
`p.seen` 删除该键 → 下一次 observe 视作新事件重投（自愈）。语义等价性：
被抑制的事件永远不落在"已投递"状态，与"跳过且不记 seen"逐字一致。

### D2 账本键语义：agent 去重键优先，合成键回退

跨路径统一去重的前提是键跨路径一致。优先采用 `wire.Notify.DedupKey`（协议已支持，
notify.go/dedup 语义已在线上验证）；agent 未提供时回退合成键
`nodeID|sessionID|attention`（等价于现 seen 键+事件，绝不会吞同类后续事件）。
落地需要把 NOTIFY 的 DedupKey 透传到 gateway 侧事件面（SessionDigest 增量字段或
attention_changed_at 复用，切片内定）。

### D3 静默/合并下沉：pushNotifier 保留调度骨架，策略判定进内核

quietHours 判定与 Coalesce 原语已在内核；G1 只把**调用位置**迁入 Dispatcher 策略链
（新增内核环节：`SetQuietHours/SetCoalesceWindow`），pushNotifier 退化为通道装配
与事件生产。批次 timer 属调度实现，留宿主（不进内核）。

### D4 IM 投影统一：三宿主的 IM 出站都变成 notify.Channel

`internal/notify/channels/im.go` 已有雏形；runtime-server 的 `NotifyAttentionScoped`
与 aicli 的 `Service.NotifySession/NotifyAttention` 适配为 Channel 实现注入各自的
Dispatcher，替换自定路由。绑定反查仍是宿主职责（内核不知道绑定模型）。

## 4. 实施切片（每片独立验证/提交/可回滚）

| 切片 | 内容 | 主要落点 | 测试 | 回退 |
|---|---|---|---|---|
| **G1** 双门收敛 | ✅ 2026-10-15 落地：`Receipt.Suppressed` 条件回滚 seen（值匹配才删，不覆盖并发更新）；`presence` 字段放宽为 `notify.Presence` 接口（`flipPresence` 测试注入确定性复现竞态窗口）；Dispatcher 构造上提（`ensureDispatcher` 惰性缓存，p.mu 下并发安全）。验证：竞态用例 ×3 全绿（回滚+条件删除）、gateway 全包、`-race` 通过 | `cmd/gateway/push.go` | 竞态用例（observe→续期→dispatch 不丢通知）；既有 push/presence 全包 | 单点 revert |
| **G2** 账本接线 | ✅ 2026-10-15 落地：`MemoryLedger.Release`（内核新增）；账本实例上提为字段；键 = `tag|attention`；**生命周期镜像 seen 快照**（换值释放旧键 + 清除释放消失键，清理必须遍历替换**前**的旧快照）。设计修正：现有单测 `TestPushNotifierObserve` 抓住"clear→re-arm 同值被持久键吞"的回归——纯 `(node, session, attention)` 键不含代次，D2 据此修正为键生命周期随 seen；agent NOTIFY DedupKey 跨路径透传仍留后续（registry 摘要不含该字段）。验证：notify+gateway 全包、G2 用例 ×2、`-race` | `cmd/gateway/push.go`、`internal/notify/ledger.go` | 跨路径同键只投一次；键冲突用例（approval→error 不得吞）；re-arm 重投回归 | 传回 nil |
| **G3** 静默/合并下沉 | 内核策略链新增 quietHours/coalesce 环节；pushNotifier 剪枝；notify_policy.go 退役 | `internal/notify/dispatcher.go`、`cmd/gateway/{push,notify_policy}.go` | 既有 gateway 全包（行为不变护栏）+ 内核新环节单测 | 保留别名文件恢复 |
| **R1** runtime-server 接入 | IM 投影改走内核 Dispatcher（§3 D4）；sessionViewers 作为 Presence 注入 | `cmd/runtime-server/connector_bootstrap.go`、`internal/api/runtimeapi/session_viewers.go` | 既有 connector_command_attention_test 语义不变 | 绑定回原 hook |
| **R2** aicli 接入 | 连接器出站走内核；InteractiveUI 信号复用为 Presence（本地面用） | `internal/connectors/service.go`、`cmd/aicli/commands/chat_gateway_host.go` | 既有 NotifySession/NotifyAttention 单测 | 同上 |
| **验收** | 真机：三宿主各触发同一会话 attention → 恰一次推送（跨路径去重生效）；在场抑制/补推回归 10 号文档 §5 六条 | — | 生产网关 + 本地双宿主 | — |

**排序与错峰**：G1→G2→G3（gateway 自洽，可先行）；R1 压在另一条线的活跃编辑区
（`connector_bootstrap.go`/`internal/connectors` 为其 QQ/连接器切片工作面）——
**R1 必须与该线提交节奏错峰**，建议等其 WIP 收口后启动；R2 随后。

## 5. 风险与回退

| 风险 | 缓解 |
|---|---|
| 双门合并引入丢通知（§2 竞态） | G1 的 Suppressed 回滚 seen；竞态用例固化 |
| 账本键设计错误吞同类事件 | D2 合成键回退绝不吞；G2 用例专测 approval→error 序列 |
| 行为漂移 | 每切片以 gateway 全量单测为护栏；真机回归 10 号 §5 六条 |
| 与另一条线冲突 | R1 错峰为**显式前置**；G1–G3 冲突面仅 push.go/dispatcher.go（本线持续持有） |
| 回滚 | 每宿主独立开关/单点 revert：G2 传回 nil、G3 恢复别名、R1/R2 绑定回原 hook |

## 6. 与既有文档的分工

- **06 §1 D1/D4**：决策原文，不动；S4b 完成时 04 的「notify 策略内核 ◐」转 ✅；
- **10 号**：在场信号线权威（S1–S3 + S4a 已收口）；本文只引用其语义，不复制；
- **本文（12）**：S4b 切片细节唯一权威；G1–G3/R1/R2 逐片回写 ✅ 与证据（同 10 号惯例）。
