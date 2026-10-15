# 13 · 事件订阅与通知路由设计（会话事件 → bot 的消息投递层）

> 上游：[06-target-architecture.md](./06-target-architecture.md) §1 D1/D4/D6/D9（notify 中台 SSOT、绞杀者、偏好三级、语义登记）、
> [09-multi-instance-binding.md](./09-multi-instance-binding.md) §7.4（发送收口在 poller）/§7.4.1（D5 通知开关）、
> [10-presence-signals-design.md](./10-presence-signals-design.md)（在场抑制/补推）、
> [12-notify-consolidation-plan.md](./12-notify-consolidation-plan.md)（S4b 内核迁移切片，本文不重复其内容）。
> 状态：**方案设计（待评审）** ｜ 日期：2026-10-15 ｜
> 维护约定：本文是「事件订阅模型 + 路由求解 + 内容出站（正文副本）」的**唯一权威**；
> 12 号继续作为 notify 内核迁移切片的唯一权威；切片完成时回写一行状态与证据。

## 0. 本文要解决的问题（缺口陈述，代码核实 2026-10-15）

现有出站能力（bot 侧能收到什么）：

| # | 链路 | 触发 | 投递内容 | 正文？ | 代码锚点 |
|---|------|------|----------|--------|----------|
| 1 | IM 入站回复 | **某条 IM 消息**触发的回合 | 终态 assistant 输出，**只回发起 chat** | ✅ | `internal/connectors/service.go:946 → 1045`（`provider.Send(ctx, cfg, msg.Chat, result.Reply)`） |
| 1b | 同上·流式变体（runtime-server + aicli） | 同上回合生命周期内 | delta 写入占位消息再 Final | ✅ | `cmd/runtime-server/connector_bootstrap.go:302-309`、`internal/api/runtimeapi/connector_host.go:83-173`；**aicli 已接 sink**（2026-10-15 P1 流式增量，`internal/connectors/delta_stream.go` + `cmd/aicli/commands/connector_host.go`） |
| 2 | 注意力出站（FR-8） | approval / question / turn finished | 固定文案通知 | ❌ | `internal/connectors/attention.go:26-30,303-346` |
| 3 | 管理面测试消息 | 手动 | 手动文本 | — | `internal/connectors/restapi/connectors.go:599` |

**缺口一：正文副本只覆盖 IM 入站回合。** 唯一带正文的出站是回复路径，它是「IM 消息 → 回复该消息」的同步副产物；
本地来源（TUI 直输 / 微前端 / API 提交）的回合**没有任何正文出站通道**——绑定会话的 bot 默认连
「回合完成」通知都收不到（绑定会话 completion 受 `notify.enabled` 门控、缺省 false，`types.go:129-137`）。
即：TUI 里问 agent，QQ bot 侧看不到 agent 的回答。用户期望的「输出副本传递给 bot」在 IM 来源成立、本地来源缺失。

**缺口二：事件订阅没有一等模型。** D5（09 §7.4.1）只覆盖 4 类注意力 kind
（`approval|question|completion|error`）+ `scope: sessions|all`，语义挂在「注意力通知」上：

- 「哪些会话的事件」只能选「绑定的会话」或「全部会话」，**没有多会话显式选择器**（如运维 bot 订阅 A/B/C 三个会话的 turn 事件）；
- 「接收哪些类型」只有注意力 4 类，**正文类（assistant_message）、回合生命周期类（turn.started/suspended）、工作类（subagent/team）不可订阅**；
- 「一个事件 → 多个 bot」「多个会话 → 一个 bot（聚合订阅）」没有模型与配置面；
- 订阅求值散在 `SubscribeAttention` 的过滤循环里（`attention.go:228-278`），加新事件类型 = 改出站代码，不是加配置。

本文补齐：**声明式订阅模型（谁订阅什么）+ 路由求解（事件命中哪些订阅、发到哪些 chat）+ 正文出站形态（content）**，
并给出与 D5 / notify 内核（12 号）/ mesh / 在场的接线方式。**不重写**：事件→文案的映射、D5 过滤、发送收口、
内核策略链（去重/静默/合并/在场/偏好）均复用现有实现。

## 1. 现有资产盘点（禁止重复造轮子）

| 资产 | 现状 | 本文如何复用 |
|------|------|--------------|
| 事件契约 | `internal/events/contract.go`（落盘/通道归类）、`internal/chat/events.go`、`internal/runtimeobserve/known_types.go` | 事件词表以这三处为枚举来源；新增可订阅类型先登记（06 D9） |
| 注意力出站单一实现点 | `internal/connectors/attention.go`：订阅三类型 → `AttentionNotice` → D5 过滤 → 有界队列 → `NotifyAttentionScoped` + mesh Forward | 保留为 **notice 形态**的一条投影；订阅匹配器在它**之前**求值，取代其内部过滤循环（行为兼容见 §4） |
| D5 策略 | `Policy.Notify{enabled,kinds,scope}` + `AcceptsNotice`（绑定会话 approval/question 恒收；completion 随开关；跨会话需 enabled+kinds+scope=all） | 作为**订阅模型的特例**做向后兼容映射（§4.1）；语义不动 |
| notify 内核 | `internal/notify/{kinds,ledger,dispatcher,presence,prefs}.go` + `channels/im.go`（语义登记/跨通道去重账本/优先级择通道/静默/合并/在场） | 路由求解的输出（NotifyRequest）交由 Dispatcher 策略链；12 §3 D4 的「IM 出口 = Channel」是本文下游 |
| 发送收口 | 09 §7.4：**事件可发生在任何节点，发送永远收口在 connector 的 poller 节点** | 订阅求值不改变收口；跨宿主事件走 Forward/InletAttention（scope=all） |
| 失败重放 | `service.go:1311-1348` 失败回复账本 + 重放 | 正文投递复用同一失败回执/重放语义 |
| 在场/静默 | 10 号 presence 门 + 内核 `Muted`/`Suppressed` 回执语义 | 订阅命中后仍过 Dispatcher 策略链（TUI 前台在看 → 抑制/补推） |

结论：**不需要新中台**。需要的是（a）事件词表扩展到可订阅的通用类型、（b）每 bot 的声明式订阅存储与求值、
（c）content 形态的投递组装。「怎么投」已由 12 号负责收敛。

## 2. 架构总览：五层一条链

```
[事件源]  宿主运行时事件总线（runtimeapi bus / aicli host.EventBus / gateway 投影 hook）
   │      事件：assistant_message、agent.turn.finished、approval_requested、question_asked、
   │            session_end、turn.suspended/resumed、interrupted、subagent.completed …（06 D9 登记）
   │  ① 归一化：EventDescriptor 查表（family/type、可订阅性、默认优先级、默认形态、去重身份）
   ▼
[订阅层]  SubscriptionRegistry：per-bot 声明式订阅（存储 = connector Policy）
   │      订阅 = {sessions 选择器, types 集合, deliver 形态, stream, target, enabled}
   │  ② 匹配：event × 全部订阅 → 命中集（会话选择器求解，见 §3.5）
   ▼
[路由层]  TargetResolver：命中订阅 × 会话 → 目标 chat 集合
   │      （绑定反查；D4 发送收口在 poller，见 09 §7.4；显式 target 直投）
   │  ③ 组装：NotifyRequest{kind, delivery_form, payload 投影, targets, dedup_key, priority}
   ▼
[策略层]  notify.Dispatcher（12 §3 D4 的迁移目标；本文按 S3 接线的下游）
   │      去重账本（跨通道）→ 在场门 → 静默门 → 合并门 → 偏好/优先级择通道
   ▼
[通道层]  channels/im（生产 = Service；Provider 队列/节流/失败账本/重放）
```

> 图中只画**订阅平面**；**回复（reply）平面**是一条独立的定向投递边（消息触发的因果应答，§4.6），
> 不经过订阅求值——绑定会话的回复语义与观察者订阅语义不得混同。

### 2.1 入口/出口全景与闭环矩阵（完整性审查基线）

**入口（谁能让会话产生回合）**

| # | 入口 | 路径（现状） | 回合？ | 输入可见性 |
|---|------|--------------|--------|-----------|
| E1 | IM 文本消息 | poller/webhook → per-chat 队列 → `Backend.Handle` → session turn（`service.go:946`） | 是 | TUI 投影（IM 输入显示）+ agent |
| E2 | IM 命令 | 入站拦截（不计 turn，`service.go:1580+`） | 否 | IM 回执 |
| E3 | TUI 直输 | SubmitUserInput → session turn | 是 | TUI |
| E4 | Web/微前端/React | submit_prompt → session turn | 是 | web |
| E5 | API/程序化（team/subagent/脚本） | submit_prompt → session turn | 是 | 调用方 |
| E6 | 系统唤醒（goal auto-continue、托管 resume、子代理完成回灌） | 内部提交 → session turn | 是 | 无（TUI 可见事件） |
| E7 | mesh 跨节点派发（R1） | poller → runner `DispatchTurn` → `Backend.Handle`（runner 侧落 turn） | 是 | runner 侧 TUI（若有）+ IM |
| E8 | 管理面（测试消息 / failover 提示） | 直发，无回合 | 否 | —— |

**出口（会话/回合产出到哪里）**

| # | 出口 | 覆盖入口 | 受订阅配置控制？ | 现状 |
|---|------|----------|------------------|------|
| X1 | reply（发起 chat 的因果应答） | E1/E7 | 否（D8，§4.6） | ✅ `service.go:1046/1151` |
| X2 | 命令回执 | E2 | 否 | ✅ `service.go:1607` |
| X3 | 注意力通知（approval/question 恒开；completion 随开关） | 全部回合会话 | D5/订阅 | ✅ FR-8 |
| X4 | 失败回执 + 重放 | E1/E7 | 否 | ✅ 失败账本（`service.go:1311-1348`） |
| X5 | failover / 系统提示 | 系统 | 否 | ✅ |
| X6 | **订阅投递**（notice/content/digest） | E1–E7（按来源与订阅） | **是** | ◻ S1 起 |
| X7 | mesh `scope=all` 转发 | 全部会话（广播意愿） | 是 | ✅（fire-and-forget） |

**闭环结论**：

- E1/E7 → X1 已闭环（IM 对话闭环）；E2 无回合、不会误入 X6；
- **E3–E6 → X6 缺失 = 缺口一**，S1 以 content 形态补上（E6 系统回合同为本地来源，镜像语义一致，噪音由订阅开关与预算控制）；
- X1/X2 与 X6 的互斥靠 L3、顺序靠 per-chat 队列（§3.6/§3.7）；X3/X6 形态重叠属显式配置（L2：(kind, form) 不同 = 不同投递）；
- 多 bot 扇出 = X6 按 bot 独立求值（§3.5）；**跨节点定向投递是 S3 前置缺口**（§4.4 注记）。

分层纪律（与 06 D1/D4、12 §3 D4 一致）：

1. **订阅只声明"要什么"**（事件类型、会话选择器、投递形态、目标），不声明"怎么投"——
   优先级/静默/合并/在场/去重全部在 Dispatcher 策略链（SSOT）。
2. **匹配与目标求解在宿主/连接器侧**：绑定模型（IM chat ↔ SessionRef）只有 connectors 层知道，
   内核不懂绑定（12 §3 D4 已定）；内核只收 `NotifyRequest`（内核形态为 `notify.Event`）。
3. **未登记事件不可订阅**：词表扩展必须先落 06 D9 的语义登记表 + 本文件对照表，三宿主不得私造类型。

## 3. 领域模型

### 3.1 事件词表（可订阅类型；登记后方可配置）

| family | type（事件名） | 语义 | 默认优先级 | 默认形态 | 现状 |
|--------|----------------|------|------------|----------|------|
| conversation | `assistant_message` | 回合终态正文（`payload.content`） | normal | **content** | ✅ 事件在总线（`chat/actor.go:3340`）；**出站未接（缺口一）** |
| conversation | `user_message` | 用户输入正文（IM 投影/本地直输） | low | content | 事件存在（TUI 投影用）；出站未定义（默认关） |
| conversation | `assistant_delta` | 流式增量 | low | stream | ✅ S4 落地：仅 `deliver=stream` 订阅可消费（其余形态不匹配 delta 事件，默认不可订阅） |
| turn | `agent.turn.started` / `agent.turn.finished` | 回合开始/收尾 | low / normal | notice | finished 已接（completion 文案）；started 新增登记 |
| turn | `turn.suspended` / `turn.resumed` | 托管挂起/恢复（§6.12） | normal | notice | 事件在总线；出站未接 |
| turn | `session_start` / `session_end` / `session.interrupted` | 会话生命周期/用户中止 | normal | notice | 事件在总线；cancelled 类出站未定义 |
| interaction | `approval_requested` / `question_asked` | 审批/提问（命令回环） | **high** | notice | ✅ 已接（D5：绑定会话恒收） |
| work | `subagent.completed` / `subagent.batch.completed` | 子代理/批次收尾 | normal | digest | ✅ 2026-10-15：仅 digest 形态可订阅（词表 + 聚合投递落地）；`team.*`/`supervision.*` 待事件面登记 |
| work | `tool.requested` / `tool.completed` / `tool.denied`（别名 `tool.started`/`tool_finished`） | 工具调用生命周期 | low | digest | ✅ 2026-10-15：高频，**仅聚合形态**（notice 订阅不生效）；窗口内与 completion 同批聚合、按 kind 分列 |
| system | `error` / `connector.health` / `budget.exceeded` | 失败与限额 | high | notice | 部分词表已登记（`error`）；健康类随 S2 补 |

登记落点：`internal/notify/kinds.go` 的 kind 语义表扩展为「family/type → priority → 默认受众/形态」，
与 `connectors.AttentionKind*`、`runtimeobserve` 事件名保持词汇表对照（06 D9）。

### 3.2 事件信封与去重身份

```
EventEnvelope{
  family, type        // 词表命中；未登记 → 不可订阅（宿主日志记降级）
  session_id, node_id // 会话与归属宿主（绑定反查 + 在场定位键）
  turn_id, trace_id   // 回合关联（同一回合的通知可聚合/去重）
  source_host_kind    // aicli | runtime-server | gateway（诊断与转发回环防护）
  ts, payload         // 原始事件载荷（投递时按形态做投影，见 §3.3）
}
```

去重身份 `identity`：显式业务 id（`request_id`/`question_id`/`message_id`）> `turn_id` > 事件时间戳
（对齐现有 `attentionIdentity`，`attention.go:350-358`）。**去重键规范见 §3.6**——12 §3 D2 已定「键语义先解决再接账本」，
本文据此把键从「文本拼接」升级为命名空间键。

### 3.3 投递形态（delivery form）

| 形态 | 语义 | 载荷 | 现状/切片 |
|------|------|------|-----------|
| `notice` | 通知文案（人读的高层状态） | 模板文本（如 "✅ 会话回合已完成"） | 现状即此（FR-8） |
| `content` | **正文副本**（assistant 输出原文） | `payload.content`（截断/分片见 §7） | 缺口一；**S1 落地** |
| `stream` | 占位消息 + 编辑（打字机） | delta 流（节流合并 ≥1s） | ✅ 2026-10-15：`SubscribeStreamMirror` 共享实现（占位 + 节流编辑 + 终态正文最终编辑；不支持流式的 provider 退化为一次性 content 投递） |
| `digest` | 聚合摘要（窗口内多事件合并为一条） | 摘要模板 + 计数 | ✅ 2026-10-15：completion 聚合（默认 30s 窗口、计数 + 会话标签，经 `NotifyBroadcast` 投递）+ **工作类聚合**（工具/子代理，与 completion 同批按 kind 分列）；approval/question/error 恒即时（安全优先） |

### 3.4 订阅模型（Subscription）

```yaml
subscription:
  id: ops-watch              # 稳定 id（去重键/诊断/审计用）
  enabled: true
  sessions:                  # 会话选择器（三选一或组合）
    mode: ids                # bound（默认）| ids | all
    ids: [session_a, session_b]   # mode=ids 时的显式列表（多会话 → 1 bot）
  types: [assistant_message, "turn.*", approval, question]   # 类型或族通配（"*" 需显式批准）
  deliver: content           # notice | content | digest | stream（stream 仅 delta 类）
  target: bound_chats        # bound_chats（默认）| explicit（配 chats 列表）
  stream: false              # 预留：deliver=content 时可选流式（未落地；当前用 deliver=stream）
  priority_override: ""      # 可选；空=按词表默认
```

会话选择器语义：

| mode | 含义 | 典型场景 |
|------|------|----------|
| `bound`（默认） | 仅「绑定到本 bot 的会话」的事件 | 单会话绑定（现状）；D5 `scope=sessions` |
| `ids` | 显式会话列表（无需绑定） | **多会话 → 1 个运维/值班 bot**（订阅 A/B/C 的 turn 与审批） |
| `all` | 宿主内全部会话 | 广播（D5 `scope=all`；沿用启用告警） |

### 3.5 匹配与路由求解（确定性顺序）

```
for each bot in enabled connectors:
  for each sub in bot.policy.events.subscriptions (enabled):
    if not TypeMatch(ev.type, sub.types):          continue   # 族通配展开
    if not SessionMatch(ev.session, sub, bindings): continue  # bound / ids / all
    targets = ResolveTargets(sub, ev.session, bindings)       # bound_chats / explicit
    if targets empty: continue
    emit NotifyRequest{kind, form, payload=Project(ev, sub.deliver), targets,
                       dedup_key=L1Key(ev),                       # 事件级键（不含订阅/目标）
                       merge_key=L2Key(ev, bot, chat, kind, form), # 目标级合并键
                       priority}
```

- **多 bot 扇出**：事件对全部 enabled bot **独立求值**——命中多个即各自投递（各自凭证、收口在各自 poller），
  互不影响，与 D5「任一连接器接收即出站」一致；
- **同 bot 内多订阅命中同一 chat** 时按 (kind, form) 合并为一次投递（L2，§3.6）——`subscription_id` 不进键；
- **多个会话可命中同一订阅**（`mode=ids`），聚合语义由 digest 形态承载（S4），v1 逐事件投递；
- **命令回环类（approval/question）的 notice 必须携带 id**（`request_id`/`question_id` 进文案与去重键），
  否则 IM 侧无法 `/approve`/`/answer`——沿用现有模板（`attention.go:310-334`）。

### 3.6 去重机制（三层，键与职责不同）

**L1 事件级（内核账本；跨通道/跨宿主/重复投递）**

```
key = evt|<family>|<kind>|<node>|<session>|<type>|<identity>
例：evt|conversation|content|aicli:node-11588-20261015T083743Z|session_2026…|assistant_message|turn_abc123
```

- `identity` 优先级 = 显式业务 id（`request_id`/`question_id`/`message_id`）> `turn_id` > 事件时间戳
  （对齐现有 `attentionIdentity`）；
- 认领一次 → 同一事件不再二次入策略链；mesh 转发回环、总线重放、重复订阅都由它兜底；
- 12 §3 D2 的教训：identity 必须带回合/请求身份——**绝不吞同类后续事件**（不同回合不同键）；
- 认领/回执/释放语义沿用 `notify.MemoryLedger`（`ledger.go:44-48`；`Suppressed` 回滚补推见 12 G1）。

**L2 目标级合并（路由层；同事件同 chat 至多一条）**

```
merge = (L1 key, connector_id, chat, kind, form)
```

- 同一 bot 的多个订阅命中同一 chat 且 (kind, form) 相同 → 合并为**一次**投递；
- **`subscription_id` 只进 ConnectorLog（审计/诊断），不进去重键**——键含 subscription_id 会让
  「多订阅命中」双发（反面用例进 S2 回归）；
- (kind, form) 不同 → 视为**不同投递**（订阅者显式配置了两种信息，如 content + completion notice，不互相吞并）。

**L3 与回复路径互斥（§4.2/§4.6）**：IM 回合来源标记 → content 订阅 v1 跳过整个回合。

三层关系：L1 决定「事件是否还值得投」，L2 决定「同目标合并成几条」，L3 决定「哪些订阅不参与」。

### 3.7 不变量（实现与评审门禁）

1. **不重复**：同一事件对同一 `(connector, chat, kind, form)` 至多一条（L1 账本 + L2 合并）；
   IM 入站回合的正文不双发（L3 / §4.2 来源标记）。
2. **不放大**：流式/工作类必须合并（stream 节流、digest 窗口）；禁止逐 token / 逐 tool 事件直投。
3. **顺序**：reply/命令回执走 per-chat 串行队列（`internal/connectors/queue.go`，现状）；
   **订阅投递从 S1 起必须复用同一 per-chat 队列**（新增"出站投递入队"入口，串行键 = connector+chat）——
   现状 `NotifySession`/`SendOutbound` 是直发、不经队列（`service.go:751/788`），若不改则 reply 与订阅投递之间
   **无顺序保证**。回归用例：同 chat 连续回合的 reply→content 保序。
4. **幂等**：重放/重订阅/跨宿主转发重复到达时账本短路（L1 键含 identity）。
5. **可降级**：平台限制（长度/频率）触发截断/合并/丢弃时记录 ConnectorLog（不静默吞）。
6. **默认安静**：content/stream 形态默认关闭；未登记类型不可订阅。

## 4. 与现有机制的关系（演进，不重写）

### 4.1 D5 向后兼容映射

D5 的 `policy.notify` 是订阅模型在注意力类上的特例，**字段保留、语义不动**，内部映射为隐式订阅：

| D5 字段 | 订阅模型等价 | 说明 |
|---------|--------------|------|
| `notify.enabled` | `events.subscriptions[notify].enabled` | 缺省 false = 依赖「绑定会话 approval/question 恒收」的现状语义 |
| `notify.kinds` | `types`（attention 词表 4 值） | 旧值 `completion` ↔ `agent.turn.finished` 的 notice 形态 |
| `notify.scope=sessions` | `sessions.mode=bound` | 现状默认 |
| `notify.scope=all` | `sessions.mode=all` | 广播语义 + 启用告警不变 |

迁移策略（S2 ✅ 2026-10-15 落地）：**双读单写**——读路径先取 `policy.events`（`Configured()` =
enabled 且有订阅）；未配置时**逐字回退** legacy（`notify` 的 AcceptsNotice 语义与 `content_mirror` 开关，
上表映射由求解层等价实现）；写路径已全链落地（storefile/storejson/REST/前端类型）。
旧字段保留一个发布周期后在 06 §1 变更决策下移除；前端图形控件 ✅ React 连接器对话框
（S2b，2026-10-15）；aicli 微前端与 TUI 保持只读（当前无策略编辑面）。

### 4.2 IM 入站回合的正文去重（缺口一实现的关键）

IM 入站回合的正文已经由回复路径投给发起 chat（`service.go:1045`）；content 订阅若照发会**双收**。定案：

- 宿主在 `Backend.Handle` 执行期间登记「**该会话存在 IM 回合进行中**」（`session → (connector, chat)`；
  会话内回合由 actor 串行化，无交错风险）；
- content 订阅求值时：命中此状态的订阅**跳过整个回合**（v1 保守，等价「本地来源才镜像」）；
- S2 可细化到「跳过发起 chat、其余绑定 chat 仍收副本」（更完整的会话可见性，需用 §3.6 的键保证幂等）；
- 跨宿主（R1 远程派发）由 runner 侧登记（派发回合在 runner 上执行，登记点一致）；
  runtime-server 的两条 IM 来源（本地入站 `Backend.Handle` / runner `DispatchTurn`）经
  `ConnectorTurnRequest.RegisterActivity` 在**会话解析后**登记——auto-create 场景下必须用
  解析出的最终会话 id（S1b ✅ 2026-10-15，`cmd/runtime-server/connector_bootstrap.go`）。
- 回复平面与订阅平面的完整划分（为什么回复不走订阅？）见 §4.6。

### 4.3 与 notify 内核（12 号）的接线

12 §3 D4 已定「三宿主的 IM 出站都变成 `notify.Channel`，绑定反查仍是宿主职责」。本文的路由求解直接产出
`notify.Event` 语义（kind/priority/dedup_key/target/session），**S3 起**把连接器出站从「直投 `NotifyAttentionScoped`」
切到「宿主 Dispatcher + `channels/im`」，从而获得账本去重/静默/合并/在场/偏好（G1–G3 已在内核落地）。
S1/S2 期间出站仍走现有 Service 路径（行为等价），S3 一次性切换（与 12 的错峰约束一致：R1 须与其活跃工作面错峰）。
- 接线细节（S3 决策点）：内核 `notify.Event.Target` 是**单值**。多 bot/多 chat 扇出建议**按目标展开为多条投递**
  （每条键 = L1+L2 合成 `…|connector|chat`，账本按目标判重），而不是用一个事件级键让通道内部扇出——
  后者会让账本按事件短路、失去目标粒度（L2 合并键正是为逐目标判重设计的）。
  通道侧（`channels/im.go`）届时不再自行做 D5 过滤（过滤已在订阅求值时完成），只负责发送。

### 4.4 发送收口与 mesh（不变）

- 发送永远收口在 connector 的 poller 节点（09 §7.4）；`mode=all` 的跨宿主事件沿用
  `SubscribeAttention` 的 Forward / `InletAttention`（`attention.go:112-159,264-268`），对端按本地订阅再过滤；
- 订阅求值发生在**每个看到事件的宿主**：本地事件本地求值；转发事件由对端求值（幂等靠 §3.6 键）。
- **跨节点定向投递（现状缺口，S3 前置）**：订阅命中多个 bot 时，各 bot 的发送收口在其 poller 节点（09 §7.4）。
  若事件宿主不是该 bot 的 poller，现状只有 `scope=all` 的 Forward 广播（fire-and-forget，对端再过滤）——
  **没有"定向转发给持有该 bot 的节点"的通道**。短期约束：单实例部署，或事件宿主=poller（现状部署满足）；
  S3 需补定向投递（按 bot 的 poller 归属经 mesh peers 转发），否则跨实例 fan-out 不可靠（入开放问题）。

### 4.5 在场（10 号）与投递形态的关系

- notice/content 都过 Dispatcher 在场门：TUI 前台正在看该会话时抑制 + 离开补推（10 §3.3 语义）；
- content 的补推是「一条完整文本」，不需要 delta 重放；stream 形态在抑制期直接降级为 content（S4 定案）。

### 4.6 绑定会话的双平面语义：回复是专用通道，订阅是配置通道

IM-bot ↔ Session 的绑定有两层不同语义，本设计**不得混同**：

| 平面 | 触发 | 通道 | 受订阅配置影响？ | 语义 |
|------|------|------|------------------|------|
| **回复 reply** | 某 chat 的消息触发的回合 | `BackendResult.Reply` → `provider.Send(msg.Chat)`（`service.go:1046`） | **否**（不读 subscriptions、不过静默/合并/在场裁剪） | 因果应答：必须送达、保持会话序、落在平台被动回复窗口（QQ 被动回复/微信 `context_token`） |
| **订阅 subscription** | 任意会话事件 | 路由求解 → Dispatcher → 通道 | 是 | 事件触发的投递（通知/正文副本/摘要）：观察者或广播语义 |

定案（D8）：

- **回复路径保持专用、不并入订阅平面**：并入订阅平面会让免打扰/合并/在场裁剪「应答」——
  用户提问却因静默不答，属于对话闭环破坏；且应答依赖平台的被动回复窗口/上下文令牌（订阅广播不具备）。
- **命令回环（approval/question 的 notice）同理恒开**（D5 既有语义），不因订阅开关或静默被静音。
- 两平面**共享传输设施**（Provider 队列/节流/失败账本/重放/发送收口在 poller），但决策链独立。
- 绑定会话想收「本地来源回合的正文」= 给绑定 chat 加 `content` 订阅（S1a，缺口一修复点）；
  IM 来源回合由 L3 互斥（§4.2）交回回复路径——同一文本对同一 chat 永远只有一条（L1+L2 兜底）。
- 支持「多 bot 同收」的边界由此清晰：**哪些 bot 收得到回复取决于是谁在对话**（reply 跟随发起 chat），
  而**哪些 bot 收得到事件取决于订阅配置**——两者独立配置、独立审计。

## 5. 关键设计决定（Decision Records）

| # | 决定 | 理由与边界 |
|---|------|-----------|
| D1 | **订阅是一等公民**：声明式存储（每 bot）、统一求值点、词表登记门禁 | 「加事件类型 = 改出站代码」不可持续；D5 已暴露该问题（4 类写死） |
| D2 | **content 形态默认关**，每订阅显式开启；开启时 ConnectorLog 记风险提示（对齐 `scope=all` 告警） | 邮箱级打扰不可默认；QQ 主动消息有配额（见 §7.3） |
| D3 | **IM 回合正文去重**：v1 跳过整个 IM 回合（保守），S2 可细化为跳过发起 chat | 避免与回复路径双发；会话串行保证判定无竞态 |
| D4 | **发送收口/白名单/命令回环语义全部不变**：订阅只影响「发什么」，不影响「谁能用」「谁来发」 | 09 §7.4/§8 是安全边界，不在本设计内松动 |
| D5 | **未登记事件不可订阅**；族通配（`turn.*`）白名单化 | 06 D9 契约优先；防止订阅面失控与语义漂移 |
| D6 | **去重键命名空间化**（§3.6），先解决键语义再走账本 | 12 §3 D2 的教训：键错吞事件 |
| D7 | 正文截断/分片由**投递层统一做**（不是订阅者各自做） | 平台差异（Telegram 4096 / QQ 分片 / 飞书卡片）收敛在 Provider |
| D8 | **回复平面与订阅平面分离**：reply/命令回环不走订阅配置与策略裁剪（不读 subscriptions、不过静默/合并/在场）；两平面只共享传输设施（§4.6） | 应答必须送达且不受免打扰裁剪；订阅是广播/观察语义；绑定特殊性（被动回复窗口/context_token）落在 reply 侧 |

## 6. 配置模型（bot 侧）

### 6.1 形态（connector Policy 扩展；Schema 驱动，三端零改动）

```yaml
connectors:
  - id: qq-ops
    policy:
      # 旧字段（D5，兼容保留；语义见 §4.1 映射，一个发布周期后退役）
      notify:
        enabled: false
        kinds: [approval, question, completion, error]
        scope: sessions
      # 新字段（S2 起）：声明式订阅
      events:
        enabled: true
        subscriptions:
          - id: main-chat                 # 绑定会话的正文副本（缺口一）
            sessions: { mode: bound }
            types: [assistant_message, agent.turn.finished]
            deliver: content
            stream: false
          - id: ops-watch                 # 多会话 → 1 个值班 bot
            sessions: { mode: ids, ids: [session_a, session_b, session_c] }
            types: ["turn.*", approval, question]
            deliver: notice
```

Schema 字段（`internal/connectors/api`）：`sessions.mode`(enum: bound|ids|all)、`sessions.ids`(multi)、
`types`(multi, 词表校验)、`deliver`(enum: notice|content|digest|stream)、`stream`(bool)、`enabled`(bool)。

> **状态**：S2 已落地 `policy.events`（`enabled` + `subscriptions[]`）；`notify` 与
> `content_mirror` 保留为 **legacy 回退**（events 未配置时逐字生效，双读单写）。
> 订阅字段：`id`/`sessions{mode,ids}`/`types`/`deliver` + per-sub `enabled` ✅（S4 部分，
> 2026-10-15；缺省启用、false 显式停用）；`digest` ✅（completion 聚合）与 `stream` ✅
>（S4，2026-10-15：占位 + 节流编辑 + 退化，仅 delta 类）已落地；`content + stream:true`
> 变体未落地（用 `deliver: stream` 表达）。前端图形控件 ✅ React 对话框（S2b；deliver
> 选项含 digest/stream）。

### 6.2 默认值矩阵

| 项 | 默认 | 依据 |
|----|------|------|
| IM 回合回复（reply，回复发起 chat） | **恒开（专用通道，不读 subscriptions）** | 因果应答必须送达、落在平台被动回复窗口（§4.6） |
| 绑定会话 approval/question（notice） | **恒开**（不受任何开关） | 命令回环（D5 既有语义） |
| 绑定会话 completion（notice） | 关 | 2026-10-14 静音收口（每条回复尾随「✅ 已完成」是噪音） |
| `content`（正文副本，任意来源） | **关** | D2：零打扰默认；开启需显式 |
| `stream` / `digest` | 关 | 显式订阅才开启（2026-10-15 均已落地） |
| 未配置 `events` 且未配置 `notify` | 等价现状（仅审批/提问回环必达） | 向后兼容 |

### 6.3 场景示例

1. **单会话绑定（默认 + 正文）**：`main-chat` 订阅 `bound` + `assistant_message` → TUI 输入回合的正文也到 bot；
   IM 回合仍走回复路径（去重后不双发）。
2. **多会话 → 1 运维 bot**：`ops-watch` 订阅 3 个会话的 `turn.*` + 审批/提问 → 值班群看板（notice 形态，高频类后续走 digest）。
3. **1 会话 → 多 bot**：主 bot 绑定会话（回复闭环）+ 观察者 bot 订阅 `agent.turn.finished`（notice，不绑定）。
   注意：观察者身份只约束**出站订阅**；该 chat 若发消息，入站仍按既有 D2 语义（未绑定 → auto-create 新会话）——
   "纯只读"不是本设计提供的强约束（如需，另立策略，不在 13 号范围）。

## 7. 投递语义与治理

### 7.1 形态处理

- `notice`：模板文案（复用 `attention.go` 现有模板；新增类型补模板并进词表）。
  **跨会话订阅（ids/all）的文案必须带会话标识**（短 id 或标题）——否则运维无法区分「哪个会话」待审批/已完成（S2 落地）。
- `content`：原文投递；超长由**投递层统一截断/分片**（D7），代码块围栏按平台能力降级（QQ/微信纯文本优先）。
  附件（`MEDIA:`/`IMAGE:` 等指令与结构化 Media）经 provider 解析后与正文同发——**富媒体透传**
  （2026-10-15 收口：流式与非流式同口径）。
- `digest`：窗口（如 30s）内同订阅多事件合并为一条（标题 + 计数 + 可选摘要行）。
  工作类批按 kind 分列（`（回合 ×a，工具 ×b，子代理 ×c）`，2026-10-15）；
  纯 completion 批保持既有「N 个会话回合已收尾」口径（存量行为不变）。
- `stream`：占位消息 + 编辑 ✅ 2026-10-15（`SubscribeStreamMirror` 共享实现：节流 ≥1s、
  终态正文做最终编辑、provider 不支持流式退化一次性 content 投递；审计/指标见 §7.4；
  抑制期降级与在场门待 S3 接线）。流式中间态**不泄漏媒体指令**（截断未闭合块/剥离已闭合块），
  终态解析指令补发附件（Telegram/飞书 2026-10-15 对齐，QQ 既有）。

### 7.2 抑制与合并（内核）

content 定为 `normal` 优先级：过在场门（TUI 前台在看 → 抑制 + 离开补推，10 §3.3）、静默门（免打扰窗口丢弃）、
合并门（同 kind 攒批）。approval/question 恒 `high`（穿透静默/合并）。turn 类 notice 允许被合并为「N 个回合已收尾」。

### 7.3 平台约束与限流

- **QQ 主动消息**（非被动回复窗口）有频次/权限限制——`content` 默认关的直接原因之一；发送失败落失败账本
  （`service.go:1311-1348`），由用户显式重放或等窗口（不自动重试轰炸）。
- Telegram 4096 字符分片、飞书卡片/文本降级、微信占位降级——全部收敛在 Provider/投递层（D7）。
- 单 bot 出站速率沿用连接器队列/节流（`internal/connectors/queue.go`、`throttle.go`），订阅不绕过。

### 7.4 可观测（D10）

`connectors_*` / `notify_*` 指标；ConnectorLog 出站条目带 `subscription_id` + `dedup_key` + `direction=out`，
失败即记录（对齐 `recordFailedReply` 现有字段）。
审计面：`content`/`stream` 类订阅与 `mode=all` 的启停、每次投递的 `subscription_id` 落 ConnectorLog（system/info），
保证「内容可见性 = 显式订阅 + 可追溯」（安全边界依赖默认关，不依赖隐式信任）。
✅ 2026-10-15：内容镜像投递审计落地（`Service.RecordSubscriptionDelivery`；ConnectorLog
direction=out，含命中 subscription id 与去重身份；legacy 命中记 `legacy-content`；入队失败不审计）。
✅ 2026-10-15（追加）：attention 成功投递审计落地（`Service.RecordAttentionDelivery`，
按目标逐连接器记 kind/session/命中订阅 id；恒开给 `bound-command-loop`、legacy 给
`legacy-notify`；**投递失败不审计**）。`mode=all` 启停审计待 S3。
✅ 2026-10-15（追加）：投递面**指标**落地——`connectors_deliveries_total{connector,form,outcome}`
覆盖 content/notice/digest × sent/failed（成功在 `Record*Delivery` 归一计数、失败在投递点
计数）；`connectors_digest_merged_total{connector}` 记被聚合吸收的事件数（「不放大」读数）。
digest 聚合广播补成功投递审计（`Service.RecordAttentionDigestDelivery`：Summary 记
`digest=N` 批内计数——此前聚合条目在 ConnectorLog 无痕）。高基数维度（会话/订阅 id）只进
日志，指标 label 保持有界。
✅ 2026-10-15（追加）：stream 形态投递审计 + 指标（`Service.RecordStreamDelivery`：
ConnectorLog direction=out，含 subscription/dedup；provider 不支持流式时 Summary 标
`degraded=content`；失败计 `stream/failed`）。

## 8. 实施切片（每片独立验证/提交/可回滚）

| 切片 | 内容 | 落点 | 测试（可执行） | 回退 |
|------|------|------|----------------|------|
| **S1a** ✅ 2026-10-15 落地 | 订阅 `assistant_message`（含 `assistant.message` 别名）→ 绑定反查 → content **经 per-chat 队列**投递（§3.7#3）；L3 IM 回合登记（`TurnActivityTracker`，本地入站与 mesh runner 派发共用 `Handle`）；最小开关 `policy.content_mirror`（缺省关） | `internal/connectors/content_mirror.go`（新）+ `service.go`（`EnqueueOutbound`）+ `attention.go`（`release`）+ `types/storefile/storejson/restapi/frontend types`（开关全链）+ `cmd/aicli/commands/connector_host.go`（装配/登记） | 全绿：`content_mirror_test.go`（开关/会话匹配/L3/去重/别名/失败释放）、`service_enqueue_outbound_test.go`（保序/校验）、`connector_turn_tracker_test.go`（嵌套/并发）；connectors 全包 + commands 定向 + `-race` | 单文件 revert + 开关缺省关 |
| **S1b** ✅ 2026-10-15 落地 | 同款订阅装配（事件源换 `runtimeapi.Handler.SubscribeConnectorSessionOutput`）；两条 IM 来源经 `ConnectorTurnRequest.RegisterActivity` 在会话解析后登记（L3 覆盖 auto-create）；投递同走 `EnqueueOutbound` | `cmd/runtime-server/connector_bootstrap.go` + `internal/api/runtimeapi/connector_host.go`（订阅面/登记钩子）+ L3 登记器提为共享 `internal/connectors/turn_activity.go`（aicli 收编同一实现） | 全绿：`connector_content_mirror_test.go`（backend/runner 两条路径登记且在提交窗口内有效）、`connector_session_output_test.go`（双事件名/退订/防御）、`turn_activity_test.go`（嵌套/并发）；connectors 全包 + runtime-server/gateway 全包 + aicli 定向 + `-race` | 单点 revert（`events`/`content_mirror` 缺省即旧路径） |
| **S2** ✅ 2026-10-15 落地 | `Policy.events`（`enabled`+`subscriptions[]`：会话选择器 `bound\|ids\|all`、types 族通配、deliver `notice\|content`）+ 双读单写（events 未配置回退 notify/content_mirror 逐字语义）+ 跨会话通知文案带会话标识（§7.1）+ 绑定反查改 SessionID 口径 | `internal/connectors/subscriptions.go`（新：词表/匹配/求解）+ `attention.go`/`content_mirror.go`（求值点切换）+ `types/storefile/storejson/restapi/frontend types`（全链） | 全绿：`subscriptions_test.go`（词表/通配/选择器/归一化/双读）、`attention_subscription_test.go`（ids 跨会话+文案标识、绑定 approval 恒开）、`content_mirror_test.go`（声明式+legacy 并存/形态过滤）、storefile/storejson/restapi 回环；connectors 全包 + aicli/runtime-server/gateway 定向 + `-race` | `events` 缺省即旧路径（单点 revert） |
| **S3** 内核接线 | 连接器出站经 Dispatcher + `channels/im`（12 §3 D4 落地）；键切 §3.6 | 三宿主 bootstrap + `internal/notify` | 跨通道去重用例（同键只投一次）；静默/在场/合并回归；**与 12 R1 错峰** | Dispatcher 传 nil = 现路径 |
| **S4** ✅ 2026-10-15 落地（S4 收口） | ✅ 单条订阅 `enabled`（显式停用/缺省启用，全链含 React 勾选）+ ✅ 内容镜像投递审计 + ✅ **attention 成功投递审计**（§7.4：逐连接器记 kind/session/subscription；失败不审计）+ ✅ **投递面指标**（§7.4：`connectors_deliveries_total{connector,form,outcome}` 三形态×sent/failed + `connectors_digest_merged_total`；digest 聚合广播补成功审计 `digest=N`）+ ✅ **digest 聚合**（completion：默认 30s 窗口按 (connector, subscription) 聚合为一条广播，计数+会话标签；交互类恒即时；`NotifyBroadcast`/`NotifyAttentionBroadcast` 投递口，未装配时退化即时）+ ✅ **工作类聚合**（工具/子代理事件入词表（含 `tool_finished`/`tool.started` 别名）；**仅 digest 形态**——notice 订阅显式拒绝；与 completion 同批按 kind 分列；身份去重 tool_call_id/subagent_id；两宿主订阅面含工作类事件）+ ✅ **stream 形态**（`SubscribeStreamMirror`：占位 + 节流编辑 ≥1s + 终态正文最终编辑 + chat 重绑先收尾旧流 + provider 不支持流式退化一次性 content 投递（经 per-chat 队列）；L3 同 content 镜像；审计/指标含 degraded 标注；两宿主接线 + 前端 deliver 选项）+ ✅ **富媒体透传**（流式会话 Update/Final 与普通 Send 同口径：中间态截断未闭合块/剥离指令（不泄漏），终态解析文本指令 + 结构化 Media 补发附件；媒体失败降级文本提示；Telegram/飞书 2026-10-15 实装，QQ 既有） | `internal/connectors/{types,subscriptions,content_mirror,stream_mirror,service,attention,delivery_metrics}.go` + `internal/connectors/providers/{telegram,feishu}`（流式媒体对齐）+ `internal/api/runtimeapi/connector_host.go`（增量/工作类事件面）+ `internal/observability/metrics.go` + 三存储/REST/前端 types+`subscription-editor` + 两宿主接线 | 全绿：订阅停用跳过（content/notice）、双面审计（内容和 attention：declarative/恒开/legacy 伪 id、失败不审计）、指标（三形态 sent/failed + digest merged + 空 label 无样本 + digest 广播审计/失败不审计）、digest e2e（3 会话合并 1 条 + approval 即时 + 无二次广播）、**work digest e2e**（5 事件同批 1 条广播：`回合 ×1/工具 ×3/子代理 ×1` + 同身份去重 + notice 拒绝 + 词表别名/族通配）、stream（占位 1 次 + 节流合并 + 终态正文权威 + 退化一次性投递 + L3 跳过 + chat 重绑收尾旧流 + 失败不审计/计 failed + 显式订阅门 + 审计/指标）、**stream media**（Update 未闭合/已闭合/指令行三形态不泄漏 + Final 指令附件上传与结构化 Media 直传 + 失败降级且正文不受影响）、NotifyBroadcast（去重/空参/部分失败）、storefile/storejson/restapi 回环、React 停用用例；connectors 全树（含 providers/stores/restapi）+ observability + runtimeapi + 两宿主全包 + `-race`（4 连跑）+ 前端 vitest（394 文件/3295 用例）+ i18n/词条校验 | 逐项开关（单条 `enabled:false` 即停用；digest/stream 仅显式配置时生效；工作类仅 digest；指标/媒体解析为旁路读数与 provider 内解析，移除即回旧） |

验收（S1 级）：真机 QQ 单聊——TUI 直输一个只读问题，bot 收到**一条**与 TUI 相同的正文副本；
随后同会话从 QQ 发消息，回复**只出现一次**（无副本+回复双发）；`content` 关闭时 bot 静默。

## 9. 开放问题（评审焦点）

| # | 问题 | 备选与倾向 |
|---|------|-----------|
| 1 | `content` 默认开关 | 本文倾向**默认关**（零打扰 + QQ 主动消息配额）；若产品语义定为「绑定 = 双端可见」，可默认开但需先做配额评估 |
| 2 | `mode=ids` 的会话输入形态 | 列表手填（v1）vs UI 选择器（S2）；会话被删除/改名的悬空订阅 → 忽略 + ConnectorLog 告警 |
| 3 | digest 的摘要生成 | 模板（v1，计数+标题）vs LLM 摘要（成本/延迟，另切片决策） |
| 4 | 事件词表版本化 | 事件删除/改名时存量订阅 → 忽略未知类型 + 首投告警（不静默失败） |
| 5 | 跨宿主账本共享 | 12 §5 遗留：`MemoryLedger` 进程内；S3 视需要持久化/共享（scope=all 跨宿主幂等依赖它）；与 #8 共同决定多实例投递语义 |
| 6 | 噪音预算 | 每 bot 订阅数上限、每会话每小时 content 条数上限（超限合并/丢弃并记录） |
| 7 | stream 与平台能力矩阵 | ✅ 部分（2026-10-15）：能力探测 = `StreamingProvider` 类型断言 + `StartStreaming` 失败 → 退化一次性投递（不硬编码）；Telegram/QQ 已实现（QQ 群/无被动上下文返回委托会话）；飞书卡片流式留 provider 侧 |
| 8 | 跨节点定向投递 | 按 bot 的 poller 归属定位转发目标（复用 mesh peers/归属账本）；未落地前限定「单实例或事件宿主=poller」（§4.4） |

## 10. 与既有文档的分工

- **06 号**：D1/D4/D6/D9 决策原文，不动；本文是其「订阅侧」落点的展开。
- **09 号**：§7.4 发送收口 / §7.4.1 D5 开关 / §8 安全白名单——**引用不改**；D5 是本文模型的特例（§4.1 映射）。
- **12 号**：notify 内核迁移（G1–G3/R1/R2）唯一权威；本文只定义接线点（§4.3）与 S3 切片，不重复其切片细节。
- **10 号**：在场信号语义（抑制/补推）——引用不复制。
- **本文（13）**：事件词表 / 订阅模型 / 路由求解 / 内容形态（content）的唯一权威；
  S1–S4 逐片回写 ✅ 与证据（同 10/12 号惯例）；决策类变更先改 06 §1 再回写本文。

## 11. 完整性审查记录（2026-10-15）

审查维度：入口（E1–E8）× 出口（X1–X7）闭环矩阵（§2.1）、架构分层与发送所有权、去重/顺序/失败/安全/可观测。

| # | 发现 | 级别 | 处置 |
|---|------|------|------|
| F1 | 入口×出口矩阵缺全景：TUI/Web/API/系统/派发入口与各出口的覆盖关系散落 | 结构 | 已补 §2.1（含闭环结论） |
| F2 | 顺序不变量与实现不符：订阅投递现状不经 per-chat 队列，reply↔订阅无顺序保证 | 一致性（中） | 已改 §3.7#3：S1 起订阅投递复用 per-chat 队列 + 保序回归用例 |
| F3 | 跨节点定向投递未定义（事件宿主 ≠ bot poller 时无定向通道，仅 `scope=all` 广播） | 架构（高，多实例阻塞项） | 已注 §4.4；列开放问题 #8；S3 前置 |
| F4 | S3 内核接线目标粒度未定（`Event.Target` 单值 vs 多目标扇出） | 集成（中） | 已注 §4.3（按目标展开、键=L1+L2 合成、通道不再自行过滤） |
| F5 | 「观察者 bot」易被误解为强制只读（入站仍 auto-create） | 语义（低） | 已改 §6.3#3 澄清 |
| F6 | 跨会话订阅的通知文案缺会话标识（多会话下无法区分来源） | 业务闭环（中） | §7.1 补要求；S2 落地 |
| F7 | 订阅/内容出站缺审计面（可见性边界仅靠默认关 + 日志） | 安全（低） | §7.4 增审计要求（`subscription_id`/启停 system 记录） |

未发现阻塞性缺口：缺口一（E3–E6 → X6）已有 S1 闭环路径；D5 兼容映射与 D8 平面分离经映射表核对无冲突；
多实例下的两项遗留（跨节点定向投递、跨宿主账本共享）为既有平台级缺口，已显式化并挂 S3 门禁。
