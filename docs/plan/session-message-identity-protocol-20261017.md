# 会话消息身份架构重构方案（Message Identity Protocol）

> 创建日期：2026-10-17
> 状态：立项评审中（未实施）
> 触发缺陷：2026-10-17 实弹三连测（sessions `qslOfySy` / `ptamDn2v` / `crFqYFTy`）——同一会话第二轮工具调用在 canonical 转录中丢失 assistant(tool_calls) + tool 行、final assistant 继承上一轮 turn_id
> 关联修复（防御层，已提交）：`c8f223a5` / `c8dfaed5` / `b6829450`
> 验收门（已建成）：`backend/internal/chat/actor_repeat_tool_turn_test.go`（工作区）——actor 级两轮重复 tool payload 场景，转绿即闭环

---

## 1. 问题陈述

### 1.1 表面缺陷

同一会话内，第二轮 run 的工具结果与第一轮**逐字节相同**（同查询同结果池）时：
canonical 转录（`session_messages`）丢失第二轮的 `assistant(tool_calls)` 与 `tool` 行
（应 +3 行实 +1 行），且第二轮 final assistant 的 `turn_id` 被第一轮的身份覆盖。
后果：前端回放丢工具证据、turn 归属错乱、跨 turn 审计断链。

### 1.2 缺陷的四次投影（同一架构问题的不同表现）

| # | 投影 | 时间线 | 修补 |
|---|---|---|---|
| 1 | 继承穿透：substance 相同 → 继承旧 id | 本轮实测 | `c8f223a5`（user turn 截断） |
| 2 | 截断条件被 developer 请求作用域行破坏（index 偏移） | 本轮实测 | `c8dfaed5`（substance 队列） |
| 3 | user2 已被中途 checkpoint 落库 → user-only 边界穿透 | 本轮实测 | `b6829450`（连续背书前缀） |
| 4 | **ReplaceHistory 序列正确铸新后，身份仍被其它通道重写回旧值** | 本轮实测（打点证实每次调用正确、最终 durable 仍错） | **无补丁可修——这是通道覆盖问题** |

### 1.3 既有事故记录（同类问题的历史补丁，见代码内注释）

- 2026-09-18 plan-mode 状态被旧快照覆盖（persist 时合并）
- 2026-10-04 MCP 覆盖被旧快照抹掉（persist 时合并）
- 2026-10-11 context anchor 污染（run 级 vs 步级 usage 口径）
- 2026-10-13 压缩替换被 canonical 重建复活（HistoryReplaced 一次性标志）
- 2026-10-14 turn_id 空间分裂（StampMessageTurnID 补章）
- 2026-09-15 session_messages 重复行（独立 session-dedupe 工具事后清理）

**模式**：全部是「多写者 + 内容对齐」架构下的覆盖/重复/归属错误，每次以更精细的
启发式补丁收窄，但复杂度单调上升，未消除根因。

---

## 2. 根因分析（架构层）

### 2.1 缺陷一：身份是「事后推导」而非「创建时授予」

现状：message_id/turn_id 在消息**创建时**不保证存在，靠 persist 时按
`MessageSubstanceKey`（role+content+toolcall 签名，**无时间/轮次维度**）反推继承。

- 后果：内容相同的消息在任何反推算法下**原理上不可区分**；每个反推特例都需要一个补丁。
- 证据：`types/message_identity_inherit.go`（3 种匹配策略并存）、
  `chat/sqlite_storage.go` 的 `identityAlignedAppendStart`（identity+substance+配额+连续前缀四重启发式）。

### 2.2 缺陷二：一个 session 三条互不知情的写通道

| 通道 | 写法 | 触发点 |
|---|---|---|
| ReAct loop | `session.ReplaceHistory(durable)`（整幅替换） | 每步 PersistHistory |
| actor | `session.AddMessage(...)` + `persistSession`（loadSession 副本上操作） | 审批/回执/pending 路径 |
| store 回写 | `applyStoredPromptProjection(session, projection)`（Update 后整幅替换） | 每次 persist 成功后 |

三条通道都能**整幅替换 History**，互不感知对方的存在与写入时序。后写者覆盖先写者；
「previous=1 截短」「铸好的 id 又变旧」均为其症状。实测打点：loop 的 ReplaceHistory
序列 7 次全部正确，最终 durable 仍错——覆盖发生在通道之间。

### 2.3 缺陷三：storage 对齐靠内容启发式而非身份协议

`canonicalAppendStartTx` 需要同时维持 identity 匹配、substance 配额、连续前缀、
legacy delta 四套启发式的正确性。任何一套的漏洞都是**静默数据丢失**（无报错、
只有事后取证才能发现）。

### 2.4 复杂度正反馈

每个启发式补丁扩大「对齐规则」的状态空间，使下一个补丁更难写对——本轮三次返工
（cut 条件两次重写、扫描/分配两版）即为实证。**继续打补丁的边际成本已高于重构。**

---

## 3. 目标协议（Message Identity Protocol）

三条不变量，全部以「创建时」为锚点：

### INV-1：身份创建时授予（Grant-at-Creation）

- 每条消息在其**创建构造器**处获得最终 message_id；tool 结果消息在
  `MessageBuilder.AppendToolResults` 处由 builder 注入（builder 持有本 run 的
  turn_id + 单调序号，id = `msg_<uuid>` 一次性铸造，不依赖内容）；
- turn_id 由 run 边界显式传入（actor 拥有 turn 生命周期，创建 user 消息时授予，
  非 user 消息继承创建时已知的本 run turn_id）；
- **禁止**任何通道在创建之后写 message_id/turn_id（`StampMessageTurnID` 仅允许在
  创建时同帧调用）。

### INV-2：单一写者（Single-Writer）

- 一个 run 生命周期内，**loop 持有的 session 对象是唯一真身**；
- actor 的 pending/审批路径需要在 run 中改历史时，必须通过 loop 暴露的受控方法
  （或事件），**不得** `loadSession` 后在副本上操作再整幅 persist；
- store 回写（applyStoredPromptProjection）只允许在「确认无并发 run」时执行，
  run 进行中一律跳过（现 post-turn persist 已满足，需用断言钉死）。

### INV-3：storage 按 id 对齐（Align-by-ID）

- `canonicalAppendStartTx` 仅做：扫描 incoming history 中**已带 id** 的行 →
  这些行必然已存储（或按 identity_hash 点查确认）→ 锚点 = 最后一条已存储行之后；
- substance 匹配/配额/连续前缀/legacy delta 全部**退役**（保留在 migration 分支
  用于存量无 id 行，新行必须有 id）；
- 无 id 行（存量数据）走一次性 backfill（identity_hash 列已具备，P0-3 已建）。

---

## 4. 改造范围与分层

| 层 | 文件 | 改动 |
|---|---|---|
| 协议层 | `types/message_builder.go` | `AppendToolResults`/`AppendAssistantAction` 接受 identity provider（run turn_id + 序号），创建即铸 id |
| 协议层 | `types/message_identity_inherit.go` | 退役继承路径（保留 exported 函数为 deprecated shim，内部 no-op + 断言日志），先冻结再删 |
| 会话层 | `chat/session.go` | `ReplaceHistory` 移除继承逻辑 → 只做「带 id 验证」：发现 id 缺失即 fail-loud（开发态）/结构化事件（生产态） |
| 会话层 | `chat/actor.go` | pending/审批路径改走 loop 受控方法；`recordPendingToolCall` 等不再 loadSession 副本写历史 |
| 存储层 | `chat/sqlite_storage.go` | `canonicalAppendStartTx` 简化为 id 对齐；启发式退役至 legacy 分支（存量兼容） |
| 存量迁移 | 一次性 | 无 id 历史行 backfill（复用 identity_hash 快路径），迁移后 substance 启发式可删 |

### 4.1 明确不做（Non-Goals）

- 不改 sqlite schema（复用现有列）；
- 不改前端消费契约（`sources`/`quality_signals`/`cancelled_queries` 的提升与卡片不变）；
- 不动 web_search 工具面；
- 不追求一次性删除全部启发式：分两阶段（先 fail-loud 观测 1-2 周，再物理删除）。

---

## 5. 实施阶段

| 阶段 | 内容 | 验收 |
|---|---|---|
| **P0 验收门固化** | `actor_repeat_tool_turn_test.go` 入库为 ignored 测试（`t.Skip` + 显式 env 解锁），CI 记录现状 | 测试可稳定复现缺陷 |
| **P1 创建时授予** | builder/构造器铸 id；actor 传入 run turn_id | 新增单测：任意重复内容 → id 恒异；本轮实测链路（sqlite 取证脚本）复跑 |
| **P2 单写者收敛** | actor pending 路径改受控方法；run 中 loadSession-persist 加断言 | P0 测试转绿；既有 actor 回归全绿 |
| **P3 对齐简化** | storage 改 id 对齐 + fail-loud 观测 | 无 id 行为 0（新写入）；存量 backfill 完成后删除启发式 |
| **P4 清理** | 删 `message_identity_inherit.go` 与 legacy 分支；session-dedupe 工具退役评估 | 相关死代码 0；文档同步 |

预估：P1+P2 约 2–3 天（含回归），P3+P4 约 1–2 天 + 一周观测窗。

---

## 6. 风险与对策

| 风险 | 对策 |
|---|---|
| 存量会话（无 id 行）在 P3 后对齐失败 | backfill 迁移先行；legacy 分支保留到 backfill 完成率 100% |
| actor 并发 WIP 冲突 | 本方案启动前置条件：`chat_actor_host.go` / `goal_*` WIP 收敛；P2 是主要冲突面 |
| 隐性行为依赖 substance 继承（如 resume/replay 场景） | P3 fail-loud 观测窗内收集全部触发点，逐个显式迁移后再删 |
| 回归面大（chat 包 36s 全量） | 每阶段独立提交 + 四包回归；P0 验收门 + sqlite 取证脚本双通道验证 |

---

## 7. 本方案与既有修复的关系

三个已提交防御修复（`c8f223a5`/`c8dfaed5`/`b6829450`）**不回滚**：它们收窄的继承越界
在协议落地后依然是无害性冗余（P4 随 `message_identity_inherit.go` 一并退役）。
本方案落地后，同日实测缺陷的修复从「三层启发式防御」收敛为「零启发式、单协议」。
