# dsh-superagent-prototype 借鉴落地实施方案（AAR）

> 状态：方案（待评审）｜日期：2026-11-01｜未开工
> 依据：`docs/dsh-superagent-prototype/README.md`（分析报告；B1–B12 清单、§8 顺序、§10 二次复查）
> 范围：B1–B12 中可落地项（分切片）；路线 C（work-item 产品域）只做立项评估
> 估算口径：1 人日 ≈ 6h 有效工作，含测试与文档；估算为区间值，评审后细化
> 关系：SAP 不参与 AAR 运行时，本方案全部为"在 AAR 既有设施上落地"；不引入 SAP 的存储/宿主/面板实现

---

## 0. 目标与非目标

**目标（对应分析报告结论，按价值排序）**

1. **B1 授权分诊**：入站事件记录"谁、以什么强度、在什么范围、依据什么"；先落**记录态（observe）**，零执行副作用，评审后再决定是否接入执行路径；
2. **B2 阶段语义**：统一"工作为什么停在现在、在等谁"的分类口径，控制台/IM 通知/巡检共用；
3. **B3 出站发送契约**：出站**用户消息级**幂等键 + `gone/permanent` 与可重试的显式二分 + 失败留档（在既有"失败快照/重发重放"机制上补齐语义与可见性，见 S2）；
4. **B9/B12**：docs 目录级校验（低成本护栏）、通知/文案复查清单；
5. **B4/B5/B6/B7/B8**：对照表 + 分项评估，能对齐的对齐，缺项单独立项；
6. **S9**：路线 C 立项评估文档（不排期实施）。

**非目标**

- 不迁移/不依赖 SAP 的 SQLite 行模型、DSH preset/tmux/进程级环境修改、面板实现；
- 不改变 background/subagent/supervision 的既有语义（仅按 B4 对照后做 additive 增补）；
- 不做路线 C 的产品级开发（仅设计草案）；
- 不把 SAP 当上游依赖（无运行时耦合）。

---

## 1. 实施原则（沿用本仓库既有纪律）

1. **先核实后动手**：每个切片先做"按 HEAD 代码核实"（分析报告 README §10 的遗留项见 S0），核实记录作为切片交付物的一部分；
2. **additive、默认零行为变化**：新增字段/接口/能力位一律向后兼容；S2 的分类**只新增元数据与留档**，不改动既有调用方的重试行为（provider 未实现分类 → 默认 `retryable`）；
3. **复用既有设施**：background CAS/租约、subagent 批次终态 outbox、notify 账本、connectors 去重账本——只做语义对齐与缺口增补，不新造队列/账本；
4. **契约先行**：接口与错误分类先落 `backend/internal/connectors/api` + 契约测试，再改 provider 与 Service；
5. **切片化交付**：每片含"改动点 + 验收 + 状态回填"；完成后回填本文件 §7 状态表，并同步 README（分析报告）相关条目。

---

## 2. 里程碑总览（切片级）

| 切片 | 内容 | 对应 | 依赖 | 估算 | 状态 |
|---|---|---|---|---|---|
| **S0** | 前置核实（§10 遗留项 + 落地锚点确认） | — | — | 0.5–1 人日 | 待开始 |
| **S1** | docs 目录级校验（索引/链接） | B9 | 无 | 1 人日 | 待开始 |
| **S2** | 出站发送契约（幂等 + gone/retryable + 留档） | B3 | S0（provider 永久失败现状） | 3–5 人日 | 待开始 |
| **S3** | 阶段语义展示层（统一阶段函数） | B2 | S0（未读回退现状） | 2–3 人日 | 待开始 |
| **S4** | 通知/文案复查清单 | B12 | 无 | 1 人日 | 待开始 |
| **S5** | 分诊记录（observe 态） | B1 | S0、决策 D2/D3 | 3–5 人日 | 待开始 |
| **S6** | effects 纪律对照（kind/key、ordered、Later、阈值） | B4 | 无 | 1–2 人日 | 待开始 |
| **S7** | connectors 契约补全（scope/准入行/health） | B5 | S5 设计口径 | 3–5 人日 | 待开始 |
| **S8** | 分项评估：B6 人工优先通道/恢复说明；B7 提示 baseRevision；B8 模型面预算与守卫 | B6/B7/B8 | S0 | 各 0.5–1 人日（评估） | 待开始 |
| **S9** | 路线 C 立项评估文档（work-item 域草案） | 路线 C | S2/S3/S5 结论 | 2–3 人日 | 待开始 |

**合计**：S0–S7 + S9 ≈ **17–26 人日**（区间之和；不含 S8 各项的实施、不含 S5 执行接入的后续切片）。

### 2.1 未排期项（明确缓办，避免遗漏）

- **B10（运维自检状态码/巡检范式）**：AAR 已有 runtime-server ops、healthz/巡检与发布脚本；借鉴收益低于本轮切片，缓办（S8 评估时顺带复核）；
- **B11（Health 纪律）**：AAR 已有 supervision/health 面；"条目唯一写者 + 3 连败阈值"留待 health 面统一改造时评估；
- **B5 的 `environment`/`threadContext` 字段**：S7 对照表已标"后续项"（AAR 无模型评估环时的取舍）；
- **B12 的面板视觉部分**：本轮只做通知/文案清单（S4），控制台视觉改造不排期。

---

## 3. 切片明细

### S0. 前置核实（0.5–1 人日）

对分析报告 §10 的遗留项按 HEAD 代码逐条给出 file:line 结论，产出《核实记录》（追加到本文件附录 B）：

- [ ] **B6 人工唤醒优先通道**：aicli/runtime-server 是否存在"人消息/状态查询可用额外槽位"的等价机制（线索：`backend/internal/connectors/runner_preference.go`、goal 续跑让位、session 队列优先级）；
- [ ] **B3 provider 级永久失败**：飞书（`providers/feishu`，撤回/群解散/被踢）、Telegram（`providers/telegram`，chat 不存在/被封）是否已有等价识别；`delivery_metrics` 现有 outcome 取值枚举；
- [ ] **B2 未读消息回退**：mailbox（agentcontrol）与批次 obligation resolver 对"注入后未被执行"的实际去向；
- [ ] **S5 落点确认**：connectors 入站处理主链（`service.go` Handle/队列/派发）与审计账本（`store.go`、`logs.go`）的可扩展点；
- [ ] **S7 现状**：connectors `Policy.AllowedUsers`、`NotifyPolicy`、`runner_preference` 与 SAP `scope/policy rows` 的字段对应关系。

**验收**：每项一条结论 + 证据路径；结论影响 S2/S3/S5/S7 的设计取舍。

### S1. docs 目录级校验（B9）

- **目标**：把 `backend/internal/driftguard` 的"文档 ↔ 代码"模式从单文档级扩到 **docs 目录级**。
- **交付**：
  - 新增校验脚本（建议 `scripts/check-docs-index.mjs`，Node 可用；或 Go test 形式挂到 CI）：校验 `docs/README.md` 主节列表 ↔ `docs/` 目录一一对应（允许显式白名单，如 `analysis/`）、每个专题目录存在 `README.md`、markdown 相对链接可解析；
  - 接入 CI：建议新增 `docs-check.yml`（或并入既有 `debug-gate-gotest.yml` 的 gate 流程）；`scripts/verify-release.ps1` 作为发布前兜底。
- **接口**：脚本退出码 0/1；失败输出"断链/缺 README/目录未登记"清单。
- **验收**：故意删除某专题 README 或改坏一个相对链接 → 校验失败；恢复后通过。
- **估算**：1 人日。
### S2. 出站发送契约（B3，3–5 人日）

- **目标**：出站用户消息具备"消息级幂等 + 永久失败/可重试二分 + 失败留档"三条硬规则。
- **现状锚点（已核实）**：`api` 包当前只有 Schema/校验/脱敏契约；`OutboundMessage` 目前仅 `{Text, Media}`（`types.go:296`，无 id 字段，新增属 additive）；发送链为 `Service.SendOutbound`（直发，返回 `error`）、`Service.EnqueueOutbound`（`connector|chat` 串行队列，失败仅记日志）；四个 provider 各自实现 `Provider.Send(ctx, cfg, chat, msgs) error`；`delivery_metrics` 以 `recordConnectorDelivery(connectorID, form, outcome)` 计指标；**已有"失败快照 + 重发意图命中重放"**（微信 errcode -2 恢复路径：`service.go` `tryReplayFailedReply`，切片 2f）——S2 在其上扩展，不另起机制。
- **接口草案（`backend/internal/connectors/api`，新增）**：

  ```go
  // SendOutcome 是出站投递的终态分类。
  type SendOutcome string
  const (
      SendSent      SendOutcome = "sent"      // 已送达（或可安全认定）
      SendGone      SendOutcome = "gone"      // 永久不可达：会话删除/锁定/被踢/目标不存在
      SendRetryable SendOutcome = "retryable" // 其余失败：按既有队列/退避重试
  )
  type SendResult struct { Outcome SendOutcome; MessageID string; Reason string }

  // SendClassifier 是 provider 可选能力（未实现 = 全部 retryable，零行为变化）。
  type SendClassifier interface { ClassifySendError(err error) SendOutcome }
  ```

  - **幂等键**：`OutboundMessage` 增加 `IdempotencyKey`（本系统生成的消息稳定 id，非通道消息号）；provider 发送时携带（飞书原生 uuid/幂等键；Telegram 等无原生幂等的通道，采用"发送前账本认领 + 有界重试 + 窗口去重"策略——**D1 决策项**；与既有失败快照复用同一消息标识，由 D1 定稿）。
  - **留档**：`gone` 结果写入 connectors 失败留档（复用 `logs.go` 审计 + 在 REST/控制台可见的失败条目；如需要可新增 `failed outbound` 视图）。
- **改动点**：`api/schema.go`（或新文件 `api/send.go`）、`service.go`（SendOutbound 返回 `SendResult`；保留旧 `error` 包装以兼容调用方）、`providers/{feishu,qqbot,telegram,wechat}`（实现分类）、`delivery_metrics.go`（新增 outcome 枚举）。
- **测试**：
  - 契约矩阵：sent/gone/retryable 三态 × 四 provider（httptest 假服务）；
  - e2e：重启重投同一消息不重复（复用 `e2e_restart_test.go` 模式）；
  - 负例：`gone` 不再重试且在留档可见。
- **验收**：同一消息重复投递只发一次（自动测试）；`gone` 不重试；可重试失败沿用既有重放/退避路径且不产生乱序；失败留档（`gone` 条目）在 REST/日志可见。
- **依赖**：S0（provider 永久失败现状）。

### S3. 阶段语义展示层（B2，2–3 人日）

- **目标**：为工作项（会话/目标/批次/工单视图）给出唯一"在等谁"阶段，控制台、IM 通知、巡检复用同一函数与 i18n key。
- **设计草案**：

  ```go
  // 纯展示层分类（无副作用，输入为只读投影）。
  type WorkStage string
  const (
      StagePersonDelivery WorkStage = "person/delivery" // 输入未送达/投递失败
      StagePersonAnswer   WorkStage = "person/answer"   // 有待回答的问题
      StagePersonReview   WorkStage = "person/review"   // 结果待审阅
      StagePersonBlocked  WorkStage = "person/blocked"  // 受阻（等人工解锁）
      StagePersonFailed   WorkStage = "person/failed"
      StageWorkingQueued  WorkStage = "working/queued"
      StageIdle           WorkStage = "idle"
      StageWorking        WorkStage = "working"
      StageEnded          WorkStage = "ended"
  )
  func StageOf(view WorkItemView) WorkStage { /* 第一匹配胜出，同 README §3-B2 表 */ }
  ```

- **改动点**：`backend/internal/api/runtimeapi`（或共享视图包，**决策 D4**）新增 `StageOf` 与视图字段；`frontend/src/pages`（标签渲染）；`backend/internal/notify`（通知文案引用 stage key）；巡检脚本可消费同一枚举。
- **原则**：只读展示，不改状态机；阶段定义互斥且可检查（对应 B12 的"精确定义"原则）；**与现有 `supervision.ExecutionStatus`（`supervision/snapshot.go`）的关系在 D4 一并定稿**（做映射而非再造第二套状态词表）。
- **验收**：对每个可构造场景阶段唯一分类；"消息注入但未被执行即结束"的专项用例（依赖 S0 结论）给出可追溯去向。
- **依赖**：S0（未读回退现状）。

### S4. 通知/文案复查清单（B12，1 人日）

- **交付**：独立的《通知与文案复查清单》文档（建议放本目录，如 `notify-copy-checklist.md`），内容：
  - 每条通知必须回答"不等它会损失什么"；健康状态零占位；标签说清"在等谁、需要什么"；
  - 与 S3 的阶段 key 对照；
  - 评审流程：新增通知模板时按清单走查并留档。
- **验收**：新通知模板评审记录可查；控制台健康状态零占位走查通过。
- **估算**：1 人日。

### S5. 分诊记录（B1 记录态，3–5 人日）

- **目标**：connectors 入站事件记录"谁、以什么强度、在什么范围、依据什么、结论是什么"；**零执行副作用**（默认只记录不改变处理路径）。
- **设计草案**：

  ```go
  // connectors 侧新增（示例；字段名评审时定稿）
  type IngressDisposition struct {
      EventID       string    // 入站去重键（复用 inboundDedupeKey）
      ConnectorID   string
      Chat          string
      Sender        string    // 平台用户 id
      Strength      string    // explicit | inferred | ignore
      Row           string    // 接收范围行（connector/chat 或 provider 范围）
      Basis         string    // addressed | followup | admission | bot
      Verdict       string    // admit | observe | ignore
      PolicyVersion string
      At            time.Time
  }
  ```

- **规则初版**（记录态，不执行）：
  - 现有 `Policy.AllowedUsers` 命中且点名/私信 → `explicit/admit`；
  - 其余人类消息 → `observe`（记 "would-run"，包括未点名、未在名单）；
  - 已知 bot/自动化账号 → `ignore` 并记录原因。
- **落点**：connectors 入站主链（`Service.HandleInbound`，`service.go:868`）与审计账本（`store.go`/`logs.go` 扩展或独立 disposition 表——**决策 D2**）；视图经 REST/控制台可查。
- **后续（另切片，评审后）**：与执行路径接入（observe→auto 的显式开关）、恢复/重校验语义、与 folder-trust/D29 的叠加优先级（**决策 D3**）。
- **验收**：默认模式下分诊**零副作用**（对既有行为无 diff）；审计可回答"这条消息为什么（没有）触发工作"。
- **依赖**：S0、决策 D2/D3。
### S6. effects 纪律对照（B4，1–2 人日）

- **目标**：把 SAP Effects 的四条纪律对照到 background/subagent 终态 outbox 的现状，输出差距清单与小改项。
- **交付**：
  - 对照表（kind/key 合并、ordered 有序、`Later` 非失败延迟、3 连败转 error 阈值）×（background jobs / subagent 终态 outbox / notify 投递账本）；
  - 能对齐的 additive 小改（如 ordered 语义显式化）；不能对齐的写明理由（AAR 已有更强机制：CAS/租约/终态吸收）。
- **改动点**：以文档对照为主；如需改代码，限于 `backend/internal/background`、`backend/internal/agent/subagent_batch_coordinator.go` 的小增量。
- **验收**：对照表评审通过；新增断言覆盖"暂停期不失败、恢复后不丢不乱"（如适用）。

### S7. connectors 契约补全（B5，3–5 人日）

- **目标**：以 SAP `SourceKind` 字段集为对标清单，补全 AAR connectors 的接入契约（对照现有实现，非新建）。
- **对照与动作**：

  | SAP 字段 | AAR 现状（已核实） | 动作 |
  |---|---|---|
  | `schema/fields`（配置渲染） | `api.ProviderSchema` + 三前端共用 Schema | 已对齐；补齐字段类型与动作（`ActionSpec`）覆盖度 |
  | `scope`（监听 + observed + row） | `Policy.AllowedUsers`、`NotifyPolicy`、connectors Policy | 新增"接收范围/observed"记录并与 S5 分诊行对应 |
  | `channel.allowed`（目标可写判定） | 无统一判定（provider 内部各自处理） | 评估新增可选能力接口 |
  | `health` 唯一 claim | connectors 日志/指标存在，无统一 health 契约 | 评估新增 connectors health/diagnosis 视图（与既有 ops 对齐） |
  | `environment`（执行变量注册） | 无（非本轮重点） | 记录为后续项 |
  | `threadContext`（评估窗口） | 无（AAR 无模型评估环） | 记录为后续项（若做 B1 执行接入再评估） |

- **验收**：每条"动作"项落一条配置→入站判定→审计的链路用例；已对齐项给出证据。
- **依赖**：S5 设计口径。

### S8. 分项评估：B6 / B7 / B8（各 0.5–1 人日评估，实施另立）

- **B6（隔离与槽位纪律）**：核实"人工唤醒优先通道"现状（S0 已列）；若缺，参照 SAP"1 个额外槽位"提出设计（评估输出，不含实施）；
- **B7（提示/技能版本化）**：评估在 skills runtime/profile 上引入 `baseRevision + 冲突拒绝 + 占位符校验 + Default updated` 协议的最小切片；
- **B8（模型面预算与 noWait）**：把 tool-schema baseline 升级为**自动断言**；评估"主循环禁等守卫"（sleep/长等待）的落点与既有 turn budget/supervision 让位的兼容性。

### S9. 路线 C 立项评估（2–3 人日）

- **交付**：`work-item 域设计草案`（本目录或 `docs/analysis/`）：Ticket/Mandate/Stage 在 AAR 的映射（session/goal/permission/supervision），范围、边界、与现有会话模型的关系、产品问题清单；
- **明确**：只立项评审，不排期实施；是否需要该域由产品决策。

---

## 4. 测试与验收策略

- **复用既有**：`go test ./...`、`go vet`、`-race`；e2e 脚本（`scripts/aicli-e2e-harness.ps1`、`scripts/verify-micro-web-*.mjs`）；connectors 契约测试与 `e2e_restart_test.go` 模式。
- **新增（按切片）**：
  - S2：三态契约矩阵（sent/gone/retryable × 四 provider 假服务）+ 重启重投不重复 + 留档可见；
  - S3：`StageOf` 单测（每阶段可构造、互斥、唯一）+ "未读回退"专项；
  - S5：分诊记录"零副作用"断言（既有入站用例跑通 + 分诊记录生成）+ 审计查询；
  - S1：docs 校验负路径（删 README/断链 → 失败）。
- **验收矩阵**：每切片交付"能力 × 场景 × 断言"表（参考 `docs/bots/08-operations-and-acceptance.md` 的矩阵格式），评审通过方可回填状态。
- **回归护栏**：涉及 connectors 的改动必须保持"未配置/未实现时不改变既有行为"（additive 默认零变化）为红线的回归测试。
---

## 5. 风险与依赖

| # | 风险/依赖 | 说明与缓解 |
|---|---|---|
| R1 | SAP 文档—代码漂移 | 已记录两处（`trigger.followup`、`reopen/listed` 遗留）；涉及协议细节一律以 SAP 代码复核为准 |
| R2 | 两层授权叠加复杂度 | S5 只做记录态；执行接入必须先把"分诊结论 ↔ 既有权限栈（folder-trust/D29）"的优先级、审计字段写清楚（决策 D3） |
| R3 | 幂等键在无原生幂等通道的落地 | Telegram/QQ/微信无平台幂等键：需在 D1 选定"账本认领 + 有界重试 + 窗口去重"策略并写清边界 |
| R4 | 语义跨宿主失真 | 移植时保留"先契约后实现"顺序；provider 未实现分类时默认 `retryable`，不改变既有行为 |
| R5 | 展示层与执行层脱节 | S3 只读投影，禁止反向写状态；任何"阶段→动作"耦合留到后续切片单独评审 |
| R6 | 范围蔓延 | S8/S9 只出评估/草案；实施需另立项；不把"顺带做的改造"混入 S1–S7 |
| R7 | 与既有专题重复 | S7 与 `docs/bots/*` 后续计划可能重叠：开工前先与 bots 专题维护者对齐范围（避免双写） |

**外部依赖**：无（不依赖 DSH/SAP 运行时；不新增第三方库——S1 校验脚本用 Node/Go 既有工具链）。

---

## 6. 排期建议（单线、1 人主责 + 评审）

| 周 | 切片 | 说明 |
|---|---|---|
| W1 | S0 → S1 → S4 | 核实先行；两个低成本切片先交付收益 |
| W2 | S2（含评审 D1） | 契约 + 四 provider + 测试 |
| W3 | S3（评审 D4）+ S6 | 阶段语义与对照表 |
| W4 | S5 记录态（评审 D2/D3） | 分诊记录 + 审计视图 |
| W5 | S7（3–5 天）+ S8 评估（择项启动） | 若 S7 溢出则 S8 顺延至 W6；S8/S9 为文档产出，可与评审并行 |
| W6 | S9 + 阶段收口 | 立项评估文档 + 状态回填/复盘 |

> 排期为参考：S2/S5/S7 依赖评审决策，决策未按时给出则顺延；每切片独立可交付，不做大爆炸式合并。

---

## 7. 状态回填与文档维护

- 每切片完成后，在本文件 §2 表与本节追加"实现状态"（沿用 `docs/bots/04-migration-roadmap.md` 的 ✅/◐/待开始 + 位置 格式）：

| 切片 | 状态 | 位置/证据 |
|---|---|---|
| S0 | 待开始 | — |
| S1 | 待开始 | — |
| S2 | 待开始 | — |
| S3 | 待开始 | — |
| S4 | 待开始 | — |
| S5 | 待开始 | — |
| S6 | 待开始 | — |
| S7 | 待开始 | — |
| S8 | 待开始 | — |
| S9 | 待开始 | — |

- 维护规则：① 分析报告（`README.md`）新增条目时，本方案同步增/改切片；② 切片完成后同步 `docs/README.md` 索引描述；③ 若引入新的设计文档（如 S9 草案），挂到本目录并在本文件链接。

---

## 8. 决策清单（需评审拍板）

| ID | 决策点 | 选项（建议） | 影响切片 |
|---|---|---|---|
| D1 | 幂等键与无原生幂等通道的策略 | 在既有失败快照/重放机制（`tryReplayFailedReply`）上统一消息标识：系统 id + 账本认领 + 有界重试（建议）；或仅账本窗口去重 | S2 |
| D2 | 分诊记录存储位置 | connectors 自有账本（建议，随通道走）；或 sessionmeta/审计账本 | S5 |
| D3 | 分诊结论与 folder-trust/D29 的优先级 | 分诊=输入、权裁=最终（两层都过，建议）；或分诊可直接放行 | S5、后续执行接入 |
| D4 | `StageOf` 归属 | `runtimeapi`（建议，快速接入控制台）；或共享视图包 | S3 |
| D5 | S7 与 bots 专题的范围边界 | 先对齐再开工（建议）；或独立推进 | S7 |
| D6 | S8 各项是否本轮启动 | 只评估（建议）；或择一立即实施 | S8 |

---

## 9. 附录

### 附录 A：落地锚点表（2026-11-01 抽查）

| 锚点 | 路径 | 用途（切片） |
|---|---|---|
| 连接器契约包 | `backend/internal/connectors/api/schema.go` | S2/S7 |
| 连接器核心 | `backend/internal/connectors/{service,queue,store,dispatch,channel_key,inbound_dedupe_key*}.go` | S2/S5/S7 |
| Provider 实现 | `backend/internal/connectors/providers/{feishu,qqbot,telegram,wechat}` | S2 |
| 投递指标 | `backend/internal/connectors/delivery_metrics.go` | S2 |
| 通知中台 | `backend/internal/notify/{dispatcher,presence,kinds}.go`、`channels/` | S3/S4 |
| 控制面 API | `backend/internal/api/runtimeapi/` | S3/S5 视图 |
| 前端页面 | `frontend/src/pages`、`frontend/src/api` | S3 |
| 后台作业 | `backend/internal/background/{store,manager}.go`（`state_version`/lease） | S6 |
| 批次终态 outbox | `backend/internal/agent/subagent_batch_coordinator.go` | S6 |
| 监督面 | `backend/internal/supervision/{action_service,execution_store}.go`（`ExpectedVersion`） | S3 |
| 文档↔代码漂移先例 | `backend/internal/driftguard/{driftguard,driftguard_test}.go` | S1 |
| e2e/校验脚本 | `scripts/aicli-e2e-harness.ps1`、`scripts/verify-micro-web-*.mjs`、`scripts/verify-release.ps1` | 各切片验收 |

### 附录 B：S0 核实记录（待填）

> 按 §3-S0 清单逐条填写：结论 + file:line + 对设计的影响。

- [ ] B6 人工唤醒优先通道：
- [ ] B3 provider 永久失败现状：
- [ ] B2 未读消息回退：
- [ ] S5 入站主链与账本落点：
- [ ] S7 字段对应关系：

---

## 10. 方案审查记录（2026-11-01）

**方法**：全文重读（一致性、估算、依赖、可执行性）+ 关键声明按 HEAD 代码复核（`connectors/{api,service,types,delivery_metrics}`、`supervision/snapshot.go`、`.github/workflows`）+ 与 README（分析报告）B1–B12 覆盖度核对。

**审查修正（已改入正文）**：

1. **估算口径修正**：S0–S7+S9 区间和实际为 **17–26 人日**（原写 15–24，算术不符）；
2. **S0–S7 遗漏项登记**：新增 **§2.1 未排期项**，显式缓办 `B10/B11` 及 `B5` 的 `environment/threadContext`、`B12` 视觉部分，避免"分析报告有、方案无"的覆盖缺口；
3. **S2 现状锚点补全**：`OutboundMessage` 实为 `{Text, Media}`（`types.go:296`）；补充"已有失败快照+重发重放（`tryReplayFailedReply`，切片 2f）"，S2 定位为**在其上扩展**而非另起机制；D1 采择基于该既有机制；
4. **S2 行为边界澄清**：分类只新增元数据与留档，不改动既有调用方重试行为（默认 `retryable` 不等于新增自动重试）；
5. **S5 锚点修正**：入站主链函数名为 `Service.HandleInbound`（`service.go:868`）；
6. **S3 对齐项**：与既有 `supervision.ExecutionStatus`（`snapshot.go`）的映射关系纳入 D4 定稿，禁止再造第二套状态词表；
7. **S1 接入点具化**：CI 建议新增 `docs-check.yml` 或并入 `debug-gate-gotest.yml`；`verify-release.ps1` 兜底；
8. **排期修正**：W5 标注 S7 溢出时 S8 顺延至 W6（原表一周内塞入 4.5–8 天工作量）。

**审查确认（无需修改）**：切片依赖链（S0→S2/S3/S5/S7）、决策 D1–D6 与切片映射、additive 原则、复用 background/subagent/notify 既有设施、无外部依赖（不引 SAP 运行时/存储）。

**结论**：方案可执行；开工前置条件为 **D1–D6 评审** 与 **S0 核实**（附录 B 回填）。审查后未发现需要调整优先级或增删切片的问题；`B10/B11` 等缓办项已显式登记。
