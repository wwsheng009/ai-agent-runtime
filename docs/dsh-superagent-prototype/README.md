# dsh-superagent-prototype 借鉴与利用分析（面向 ai-agent-runtime）

> 生成日期：2026-11-01
> 分析对象：`E:\projects\ai\dsh-superagent-prototype`（下称 **SAP**）——DSH（DeepSeek Harness）bundle，TypeScript，面向单个 GitHub 仓库（可选飞书）的主动式值守 agent；`docs/000–008` 为规格文档，`src/core` 为纯域实现。
> 对照对象：本仓库 **ai-agent-runtime**（下称 **AAR**）——runtime-server / aicli / gateway 三宿主，connectors(bots) / background jobs / supervision / subagent 批次 / skills runtime / goal / profile 权限栈。
> 性质：**借鉴与利用清单 + 落点建议（本文档未实施任何代码改动）**；AAR 现状按本仓库现有文档口径归纳，并经 2026-11-01 二次复查做代码级抽查（见 §10；其余未抽查项在实施前仍需按 HEAD 代码核实）。
> 前置结论：SAP 与 AAR 不共享代码与运行时（SAP 依赖 DSH/cordis，AAR 是 Go 单机 runtime）。因此本文讨论三类可用性：**整包直接利用**、**设计/协议借鉴**、**在 AAR 既有设施上落地**。
> 配套实施方案：`implementation-plan.md`（S0–S9 切片、接口草案、验收与决策清单；2026-11-01 整理）。

---

## 0. 摘要（TL;DR）

1. **SAP 最值钱的不是代码规模，而是两套协议**：
   - ① 入站事件的**授权分诊协议**：确定性 Gate + 准入表（接收范围 × 名单 × 是否点名）+ `explicit/inferred` 强度 + `off/observe/auto` 接受模式 + Mandate（Policy 版本/依据/范围）与恢复前重校验；
   - ② 工作项的**生命周期协议**：Ticket 阶段表（谁在等谁）、Assignment/Report（`progress|waiting|ready|blocked|failed|dropped`）/Attention（结构化单问题）、handback/ownership（谁来决定一条人消息）、resume/stop/retry/pause、输入"已读/未读"语义。
2. **AAR 已有一流的可靠性与控制面设施**（background CAS+租约+终态吸收、subagent durable 批次、supervision 通知收敛、worktree 隔离、connectors/bots 专题、skills runtime）。SAP 的价值在于**补齐"主动式/半主动式 agent 的授权与分诊语义"**，而不是替换 AAR 现有设施。
3. **三条可执行路线**：
   - **路线 A（零改动利用）**：把 SAP 作为独立工具部署（专用于一个 GitHub 仓库的值守），用其现成的面板/巡检/渠道适配；AAR 侧不改代码。前置条件与限制见 §5。
   - **路线 B（推荐主体）**：把 SAP 的协议逐项映射到 AAR 现有设施上做增量（见 §3 清单与 §4 矩阵）。
   - **路线 C（长期）**：若产品方向要做"主动式工作平台"，以 SAP 域模型为蓝本设计 AAR 的 work-item 域（Ticket/Mandate/Stage），执行面复用 AAR 的 session/subagent/background 栈。
4. **只做三件的优先级（价值排序；实施节奏见 §8，B1 先以记录态落地）**：`B1` 准入表 + observe 模式（覆盖所有"非人类直接指令"的自动化入口）；`B2` 控制台/监督面采用"谁在等谁"的阶段语义；`B3` 出站消息幂等键 + `gone/retryable` 契约（connectors 通道发送）。
5. **明确不借**：SAP 的单文件 SQLite/无 GC 存储管理、DSH 宿主耦合（preset/tmux/进程级 PATH/danger-full-access）、面板具体实现、研究原型的代码风格（SAP README 自陈"研究代码、工程实践不一致"）。

---

## 1. 分析范围与证据

- **SAP 侧已核对**（2026-11-01）：`docs/000–008`，以及 `src/core/{engine,ports,model,stage}.ts`、`src/sources/kind.ts`、`src/app/{effects,http,runtime,outbox,sessions}.ts`、`src/executor/dsh.ts`、`cordis.patch.yml` 等关键协议面。
- **已发现的少量文档/代码漂移**（引用其协议时以代码为准）：
  - `DomainEvents` 实际含 `trigger.followup`（`src/core/ports.ts:100`），`docs/007` 的正文列表未提；
  - `DecisionKind` 保留旧版本兼容值 `reopen`，`MandateBasis` 保留 `listed/authority` 遗留分支（代码注释标明为兼容旧记录）。
- **AAR 侧口径**：`docs/README.md`、`docs/bots/README.md`、`docs/skill_runtime/README.md`、`docs/background-job-lifecycle.md`、`docs/ANALYSIS-sqlite-lock-problem.md`、`docs/analysis/deepseek-harness-vs-aar-agent-system.md`、`docs/analysis/dsh-goal-domain-vs-aar-goal-20261017.md`、`docs/product/{folder-trust,project-permissions,harness-settings}.md`（列名）等现有文档；另见 §10 二次复查的 AAR 代码级抽查记录（2026-11-01）。

---

## 2. 结论总表（借鉴/利用面一览）

| # | SAP 机制 | 可用性 | AAR 最近落点 | 优先级 |
|---|---------|--------|--------------|--------|
| B1 | 授权分诊协议（Gate / 准入表 / 强度 / observe-auto / Mandate 重校验） | 设计借鉴（SAP 独有性最强） | connectors/bots 入站 + 权限栈（folder-trust / D29 信任分级门控） | **高** |
| B2 | 工单生命周期协议（Ticket/Stage/Report/Attention/handback/read-tracking） | 设计借鉴 | goal / session / supervision / 控制台与通知文案 | **高** |
| B3 | 出站回执（Outbox：幂等 id、按会话保序、`gone` vs 可重试、dropped 留档） | 协议借鉴（可直接写测试） | connectors 通道发送 + notify 中台 | **高** |
| B4 | durable 副作用队列（Effects：kind/key 合并、ordered、`Later`、3 连败健康） | 增量改进 | background jobs + batch terminal outbox | 中高 |
| B5 | Source 连接器契约（schema+fields / scope / policy part / health claim / threadContext） | 设计借鉴 | connectors 专题（schema 驱动配置方向已一致） | 中高 |
| B6 | 工作项独立 Session+目录 + 槽位纪律（人工额外槽位、prepare 命令、30 分钟后台策略） | 部分已有，补语义 | worktree isolation / sessions / subagentbatch / background | 中 |
| B7 | Prompt/技能版本化覆写（base revision、占位符校验、Default updated、总大小上限） | 设计借鉴 | skills runtime / profile / prompts | 中 |
| B8 | 模型面预算 + noWait 守卫（主循环禁等） | 测试与纪律借鉴 | toolbroker / progressive disclosure / turn budget | 中 |
| B9 | docs-as-spec 测试护栏（文档索引完整性、类注释、模型面漂移测试） | 工程纪律（低成本） | docs/ + CI | 中 |
| B10 | 运维自检状态码（0/1/2/3）与巡检提示词范式 | 运维范式 | runtime-server ops / scripts | 低-中 |
| B11 | Health 纪律（每条目唯一写者、3 连败转 error、成功即清除） | 纪律借鉴 | runtime health / supervision | 低-中 |
| B12 | 注意力预算 UX 与文案原则（少说、结构化单问、健康不占位） | 规范借鉴 | notify 中台 / console 文案 | 中 |

---

## 3. 逐项借鉴清单

每项给出：SAP 设计 → AAR 现状（按现有文档口径）→ 落点建议 → 验收线索。SAP 证据均在 `E:\projects\ai\dsh-superagent-prototype` 下，AAR 证据均在本仓库 `docs/` 下。

### B1. 授权分诊协议：Gate + 准入表 + 强度 + observe/auto + Mandate（优先级：高）

**SAP 设计**（`docs/002-authorization.md`，`src/policy.ts`）：

- 两条路径分离：Source 侧 **Gate→Eval**（决定是否开工）与 **Subscription 扇出**（延续已有工作；绕 Gate、不授予新权限）；
- 三条强度：`explicit`（名单内的人点名/私信、带 Ticket 的 Thread 上的人跟进、准入表 explicit 格）/ `inferred`（表内 inferred 格、名单 bot）/ `not admitted`（其他 bot、默认格、`off` 模式下所有推断）；
- **准入表**：行 = 接收范围（飞书群/其他群/私信；GitHub issues/pulls/comments/ci），三格 = {名单内未点名, 名单外点名, 名单外未点名}，默认 `inferred/ignore/ignore`；仅"接收全部消息"的范围提供未点名格；
- **接受模式** `off / observe(默认) / auto`：observe 下正评估只记 "would accept"，不执行；切 auto 需要人工审查后再开；
- **Mandate**：accept 时按不可变凭据落库（强度 + 规则集 + Policy 版本 + 依据 basis + 范围）；恢复结束的工单前按当前 Policy **重校验**（规则仍在、模式仍是 auto、发送者仍在名单）；只有三种操作改变 Mandate：accept 创建、人工 stop 撤销、重校验失败撤销；策略变更只**告知**（policy-changed Input），不立刻吊销；
- **跟进所有权（handback/ownership）**：人消息落到有工单的 Thread → 归该工单的 worker 决定；worker 可 handback 一次；全部 owner 交还/未接手才回 Eval；主 Session 在 owner 存在时只能 stop。

**AAR 现状**（经二次复查代码抽查）：connectors 已落地为代码包（`backend/internal/connectors`：service/dispatch/queue/store、跨宿主 `ChannelKey` 去重、四家 provider；多实例派发 R1/R2a/R3 见 `dispatch.go`；docs/bots 13 记 S1–S4 已落地），notify 中台已存在（`backend/internal/notify`：presence/dispatcher/kinds/channels）。但授权语义仍偏"宿主/绑定层"（会话绑定、偏好、路由），权限面集中在 folder-trust / project-permissions / profile 的信任分级门控（执行期生效）。**缺口**：缺少"谁让 agent 开工、以什么强度、能否被事后追溯"的**入站分诊域**。

**落点建议**：
1. 给 connectors 的入站事件加一层"分诊元数据"：`strength ∈ {explicit, inferred}`、`scope`（接收范围 row id）、`policy_version`、`basis`——先只做**记录**（observe 语义），不动执行路径；
2. 将"允许名单"从通道配置中独立为 Policy part（每通道自己的 allowed/bots 列表 + 收件范围行），与现有 folder-trust/D29 门控对接：分诊结果作为**输入**，最终授权仍由现有权限栈裁决——两层都过才执行；
3. 参考 Mandate 的"凭据 + 版本 + 重校验"形态，把"一次授权"从隐式会话绑定升级为显式记录（复用 AAR 已有的 CAS/状态版本基建）。

**验收线索**：新增分诊记录可在审计/控制台查询；对未点名消息在默认模式下只产生 "would-run" 记录、零副作用；`stop` 语义可回答"这条消息为什么没有触发工作"。

### B2. 工单生命周期协议：Stage / Report / Attention / 输入已读（优先级：高）

**SAP 设计**（`docs/001`、`docs/003`，`src/core/{model,stage}.ts`）：

- **阶段表**（第一匹配胜出）：`person/delivery` → `person/answer` → `person/review` → `person/blocked|failed` → `working/queued` → `idle` → `working`，另加 `ended`；语义是"**在等谁**"，不是内部状态码；
- **Report 状态机**：`progress|waiting|ready|blocked|failed|dropped`；`blocked/failed` 后自动输入被扣留，直到人的新消息到达；`ready` + 有 PR = "待审阅"；
- **Attention**：结构化**单个**具体问题（带来源消息定位），问题替换时撤回未发送的旧答复；
- **输入已读语义**：Input 被 Session 带入 step 且随后有 Report 或 turn 结束才记"已读"；工单结束时未读、未回、未 handback 的消息回到 Eval——支撑"消息不会静默丢失"；
- **resume/stop/retry/pause**：人消息可沿用原授权唤醒已结束工单；人工 stop 不可逆；pause 期间 Eval 超时顺延、源继续记录。

**AAR 现状**（经二次复查代码抽查）：goal/iterate_goal 有终态与 stalled 门控（`backend/internal/goal/continuation.go`）、`submit_report` 结构化回执（`backend/internal/agent/iterate_goal.go` 等）、supervision 有通知收敛与 CAS 控制动作（`backend/internal/supervision/`：`ExpectedVersion`）；background jobs 有终态吸收；但**控制台/通知**缺一套统一的"这条工作为什么停在现在、在等谁"的分类口径。

**落点建议**：
1. 在现有 goal/session/工作项投影上增加一个纯展示层"阶段分类函数"（照抄 SAP 表，本地化标签：等输入送达 / 等答复 / 等审阅 / 受阻 / 排队 / 空闲 / 进行中 / 已结束）——**零执行改动**，控制台、IM 通知、巡检共用；
2. 把"结构化单问题"固化为 supervision/ask_user_question 的推荐形态（杜绝长报告里藏问题）；
3. 对"未读消息在工单结束时去向"做一次专项审查：AAR 的 mailbox/terminal outbox 已 durable，重点补"未读→回退到分诊（B1）或明确丢弃"的显式规则，避免静默吞消息。

**验收线索**：控制台/通知里每个终止或等待中的工作项都能被阶段函数唯一分类；构造"消息注入但未被执行即结束"用例，断言其去向可追溯（已读/回退/丢弃三选一且可见）。

### B3. 出站回执协议：幂等 + 保序 + gone vs retryable（优先级：高）

**SAP 设计**（`src/app/outbox.ts`，`docs/008` §5）：

- 每条消息一个**稳定 id**，作为渠道幂等键（GitHub 隐藏标记 / 飞书 idempotency key / 自定义 CLI `--key`）；同一 id 永不二次入队；
- 同一 Ticket 到同一会话的**保序**：后面的消息等前面的（直到前车连续失败 3 次并升级为系统问题才放行）；
- 渠道结果二分：`sent` / `gone`（永久不可达：会话删除/锁定/找不到 → 留档 `dropped`，含原因与所属工单，永不再排队）；其余异常抛错 → 指数退避（5s → 300s）；
- Source 关闭时消息**等待**而非丢弃；撤回=取消队列行。

**AAR 现状**（经二次复查代码抽查）：已有通道实例级去重键（`channel_key.go`："同一通道实例不重复投递"）、入站去重（`inbound_dedupe_key*`）与投递账本去重（`RecordSubscriptionDelivery(..., dedupKey)` 等）、失败回复重放（`failed_reply_test.go`）、投递指标（`delivery_metrics.go`）；notify 中台与 background 终态机制已存在。**缺口缩小为**：出站**用户消息**（`SendOutbound`/`EnqueueOutbound`）未见消息级幂等键与"永久失败 vs 可重试"分类的代码证据（抽查口径，需逐行复核），以及 dropped 留档的统一规范。

**落点建议**：
1. 在已存在的 `backend/internal/connectors/api` 契约包与 provider 实现中，把 `SendResult ∈ {sent, gone, retryable(error)}` 与"幂等键 = 消息稳定 id（本系统生成，非通道消息号）"固化为硬规则 + 契约测试；
2. 复用 background/terminal outbox 的既有重试与终态吸收，不新造队列；
3. `dropped`（永久失败）在控制台/通知可见，且与 B1 的"回退分诊"语义衔接（用户可感知"这条回复为什么发不出去"）。

**验收线索**：同一消息重复投递只发一次（渠道侧可验证）；会话不可达时记 `gone` 且不再重试；可重试失败按退避重排且不产生乱序。

### B4. durable 副作用队列纪律：Effects 的四条可借规则（优先级：中高）

**SAP 设计**（`src/app/effects.ts`，`docs/007`）：

1. **先在事务内入队、提交后才执行**（副作用行与业务变更同事务，崩溃不丢）；
2. **`(kind, key)` 唯一并合并**：重复入队保留队列位置、替换 payload 并使 due 立即（幂等合并而非堆积）；
3. **per-kind 纪律**：并发上限、有序种类（relay/notice 保序，失败阻塞同 kind 后续）、`Later`（"现在不行"不计失败，如暂停/槽位满）、按 kind 独立健康位（3 连败才转 error，delivery 例外因失败已落在工单上）；
4. **drain 保留**：停止调度但保留待执行行，下次 start 继续。

**AAR 现状**（经二次复查代码抽查）：background jobs 已实现更重的机制（CAS/`state_version`、owner 实例 + 租约、终态不可覆盖、进程树 reaper、显式 requeue、默认不自动恢复；`backend/internal/background/`）；subagent 批次有 durable 终态 outbox 与有界重试/退避（`backend/internal/agent/subagent_batch_coordinator.go`：`BatchTerminalNotification`/`TerminalDeliveryAttempts`/`TerminalDeliveryRetryDelay`）。**SAP 可增量补充的点**：kind/key 合并语义、"有序种类"的显式声明、`Later` 这种"非失败型延迟"的显式分类、以及"3 连败才升级为对外可见问题"的阈值纪律。

**落点建议**：把上述四点作为 background/outbox 的**语义检查单**逐条对照现有实现，能对齐的对齐（尤其 `ordered` 与 `Later` 分类），不要照抄其 SQLite 行模型与调度器（AAR 的更强，见 §6）。

**验收线索**：新增对照表（kind → limit/ordered/retry 类/健康转 error 阈值）；暂停/恢复测试断言"暂停期不失败、恢复后不丢不乱"。

### B5. Source 连接器契约：一份可复用的"接入协议"（优先级：中高）

**SAP 设计**（`src/sources/kind.ts` + `docs/008-cookbook.md` 完整教程）：

一个 Source kind 用一个声明式对象描述全部接入面：

- `schema`（zod，存配置校验 + 默认值）与 `fields`（**直接生成设置表单**：switch/select/number/text/rows/fact，`advanced` 折叠高级项）——前端零定制；
- `scope(input, config)`：监听判定 + `observed`（是否收到未点名消息）+ `row`（准入表行 id）；**同一函数在 Mandate 重校验时复用**（只允许依赖 meta 与配置）；
- `policy` part：`schema` + `fields` + `explicit` 说明 + `rows()`（准入表行与可用格）+ 可选 `bots` + 可选 `sync`（成员名册同步，失败进 `sync:<id>` 健康位、1 分钟起退避）；
- `channel`：`allowed(address, config)`（目标是否仍可写）、`send`（**幂等**，返回 `sent|gone`）、`link`（面板链接）；
- `threadContext`：`messageId/read(thread|root|parent)/images/image`，给模型评估一个**有界上下文窗口**；
- `health: string[]`：注册时 claim，**唯一写者**，冲突拒绝、`sync:` 保留；
- `environment`：声明执行环境变量（key + 双语描述）+ `values`（按工单来源注入，**不含凭据**）；
- 运行循环纪律：`ready → configured() → config() 每轮重读 → cursor 只在全部覆盖项落库后推进 → withSignal/abort 立即退出 → report 只写已声明健康位`；KV 键由 `KV` 注册表统一声明（测试禁止散落字面量）。

**AAR 现状**（经二次复查代码抽查）：契约包与四家 provider 已实现（`backend/internal/connectors/api/schema.go`；`providers/{feishu,qqbot,telegram,wechat}`），"Connector + Binding + JSON Schema 驱动表单、三前端共用字段定义"的方向与 SAP 一致（`docs/bots/README.md` 结论 4）。差距在**契约的完整度**：SAP 把"接收范围→准入行→回复地址→健康位→环境变量"串成闭环，而 AAR 当前 schema 主要覆盖"配置渲染 + 基础 provider 能力"。

**落点建议**：把 SAP 的 kind 对象作为已存在的 `backend/internal/connectors/api` 与四家 provider 的**对标清单**，逐项确认：scope/重校验同源、准入表行、channel `allowed`、发送幂等键与 gone 语义、健康位唯一 claim、KV 键注册表。**不建议**照抄 TypeScript 类型形态，建议照抄"字段集的完备性"。

**验收线索**：新增一个通道只需一份 schema + provider 实现即可出现在三端配置页（无前端改动）；契约测试矩阵覆盖 scope/重校验/channel/health 四组行为（SAP 侧对应 `tests/sources.spec.ts`、`registry.spec.ts`）。

### B6. 工作项隔离与槽位纪律（优先级：中）

**SAP 设计**（`docs/003`）：

- 每 Ticket 一个 worker Session（`worker-<id>`）+ 一个独立目录（worktree），互不串扰；
- **worker 槽位** = 并发上限，另加 **1 个"人消息/状态查询"额外槽位**——新工作永远不能挡住"人继续已有工单"；
- prepare 命令一次执行（10 分钟超时、失败记工单）+ 工单级环境变量（`DSH_SUPERAGENT_*`，不含凭据）；
- 后台工作规则：turn 结束仍有作业/子代理 → 保持槽位；30 分钟上限（shell 杀、子代理放行）；handoff 给人 → 立即释放槽位；Assignment 结束 → 杀作业、丢子代理唤醒；
- 重启恢复：运行中 Assignment 带"恢复说明"重启（最旧优先）、已报告且无新输入保持 Session。

**AAR 现状**：worktree isolation / fsscope / sessions / subagentbatch / turn budget 已具备大部分能力；background 有 owner/租约语义。**可借的是调度纪律**（人工额外槽位、恢复说明文案、handoff 即释放），以及"准备命令 + 环境变量契约"这种**给运维的表达方式**。

**落点建议**：① 校验 AAR 是否已有"人工唤醒的优先级通道"（若没有，参照额外槽位设计）；② 把"重启恢复说明（检查目录/git/远端后再重复外部动作）"作为恢复提示词的固定段落；③ 环境变量注册表复用 B5 的 environment 声明（与 connectors 共用）。

**验收线索**：满负载时人工追问仍能推进（专项用例）；重启后恢复提示语逐字一致、外部动作不重复。

### B7. Prompt/技能版本化覆写协议（优先级：中）

**SAP 设计**（`docs/004`）：分区装配（共享/角色 → 团队段 → 工作段 → 环境 → 策略，策略段每次请求重生成）；覆写记录 **base 修订**；保存命名基修订，冲突拒绝且不丢草稿；模板保留**占位符校验**；总大小上限、未知键拒绝、与默认相同的覆写自动删除；上游默认变更时面板标 **Default updated**，提供"保留我的文本/用新默认"；会话每个模型请求读最新版本并记录所用修订。

**AAR 现状**：skills runtime 已有 system/external/runtime 三层来源与持久化、治理/配额；profile 有配置体系。**缺口**：提示/技能文本（尤其团队定制段落）缺统一的"基线修订 + 冲突拒绝 + 上游默认变更提示"协议。

**落点建议**：给 AAR 的 profile 提示覆写/团队技能引入 `baseRevision` 与占位符校验（可直接复用现有版本化/CAS 基建），并按 SAP 的"同默认即删除"规则防止垃圾覆写堆积；"Default updated" 提示接入控制台。

**验收线索**：并行编辑冲突被显式拒绝且草稿保留；升级后旧覆写可见 diff 与两种处置；未保留覆写的段落自动跟随新默认。

### B8. 模型面预算 + noWait 守卫（优先级：中）

**SAP 设计**（`docs/007` §Presets、`tests/model-surface.spec.ts`）：主 Session 工具面（14 个）与 Executor 工具面（6 个）有**逐字契约测试**与**每角色大小预算**；主 Session 明确"**不等待**"——守卫拒绝 `sleep`（命令位）、shell 超时 > 30s、job wait > 5s；结果一律 push 到达。

**AAR 现状**：toolbroker / progressive tool disclosure / tool schema baseline / turn budget 已有同类方向（`docs/tools/*`）。**可借**：①"主循环禁等"的机制化守卫（AAR 的 supervision 让位是同类思想，可延伸为工具级校验）；②surface 预算测试作为工具面的回归护栏（AAR 已有 tool-schema-baseline 文档，可升级为自动断言）。

**验收线索**：主循环路径新增"阻塞型等待"调用会被测试或运行时守卫拒绝；工具面变更触发预算断言。

### B9. docs-as-spec 测试护栏（优先级：中，低成本）

**SAP 设计**：编号文档只依赖前序文档；`tests/docs.spec.ts` 强制"每篇只链接更早文档、本地链接可解析、模块表与测试表**完整列出**所有模块与测试"；`class-docs.spec.ts` 强制每个类有 JSDoc；`model-surface.spec.ts` 防模型面漂移。

**AAR 现状**（经二次复查）：已有同类先例——`backend/internal/driftguard` 把"设计文档条目 ↔ 代码调用"锁成测试（单文档级，`driftguard.go` 注释指向 `docs/plan/tool-layer-io-seam-design-20261028.md`）；docs/ 已分专题且 `docs/README.md` 有索引。**缺口**：目录级索引完整性与相对链接有效性无自动校验；代码注释规范未机械化。

**落点建议**：把 driftguard 模式从"单文档 ↔ 单模块"扩展到"docs 目录级"：校验 `docs/README.md` 主节列表与目录一一对应、专题 README 存在、相对链接无断链（CI 可见）；可选：把关键契约文档与代码常量做互查（AAR 已有 tool schema baseline 与 driftguard 先例）。**成本低、防文档腐化收益高。**

**验收线索**：故意删除某专题 README 或改坏链接时校验失败。

### B10. 运维自检状态码与巡检范式（优先级：低-中）

**SAP 设计**（`docs/006`）：`status/start/restart` 四类退出码（0 正常 / 1 不运行或 runtime 错 / 2 部件错且重启无用 / 3 等待 setup）；`--respect-stop` 保留人工停止；小时巡检提示词模板（0 静默、2/3 不重启只报告、1 才重启）；日志保留策略。

**AAR 现状**：runtime-server ops、scripts/install-*、health/巡检脚本已有；**可借**："退出码即语义"的自检契约与"巡检提示词直接可用"的交付方式（把运维手册变成可粘贴的 agent 任务）。

**验收线索**：巡检脚本的退出码语义写进运维文档并由测试锁定。

### B11. Health 纪律（优先级：低-中）

**SAP 设计**（`src/infra/health.ts`）：每个健康条目**注册时唯一 claim**（冲突与保留名拒绝）；固定文案（setupPending/sourceOff/pollFailures…）；连续 3 次失败才转 `error`、任一成功即清除；错误是"可能需要对人的行动"的信号，而不是一次性网络抖动。

**落点建议**：把"条目唯一写者 + 阈值转 error + 成功即清除"写成 AAR health 平台的契约测试；与 B1/B2 协作：分诊率、未读回退数等指标可进入同一健康面。

### B12. 注意力预算 UX/文案原则（优先级：中）

**SAP 设计**（`docs/000`、`docs/005`）："用最少的人注意力换最多价值"；面板**健康不占空间、只列需要人的**；每行标签回答"在等谁、需要什么"；通知 = "人必须行动或能用结果"；聊天消息默认一句话、硬上限 100 词；单问句；阶段定义互斥且可检查。

**AAR 现状**：notify 中台（优先级/静默/合并/presence 策略）已有同样的问题域。**可借**：把 SAP 的原则变成本仓库的**文案与通知复查清单**（例如：每个新通知都要回答"不等它会损失什么"），并让控制台/IM 通知复用 B2 的阶段标签。

**验收线索**：通知模板评审清单化；健康状态零占位规则在控制台走查中可验证。

---

## 4. 落点映射矩阵（若在 AAR 上落地，SAP 机制对应到哪）

| SAP 机制 | AAR 既有设施（最近邻） | 复用方式 | 主要缺口/动作 |
|---|---|---|---|
| Source / SourceKind | connectors（`api/schema.go` + 四 provider，bots 专题） | 契约对标（B5） | scope/准入行/health claim/幂等发送契约 |
| Trigger / Thread / Correlation | 事件总线 + 事件订阅路由（13/14 号文档） | 概念映射 | "不可变事件 + 稳定 id" 与去重键规范的统一 |
| Gate / 准入表 / Mandate | folder-trust / project-permissions / D29 信任分级门控 | 叠加：分诊记录 → 既有权裁 | 入站分诊域（B1） |
| Ticket / Stage / Attention | goal / iterate_goal / supervision | 展示层阶段函数 + 结构化单问 | 阶段语义统一（B2） |
| Assignment / Report | subagent run / submit_report / batch | 映射（已有回执契约） | Report 状态机与"未读回退"规则对齐 |
| Effects（副作用队列） | background jobs（CAS+租约+reaper） | 语义检查单（B4） | kind/key 合并、ordered、Later 分类 |
| Outbox（出站回执） | connectors 发送（ChannelKey/投递账本已有）+ 批次终态 outbox | 契约借鉴（B3） | 消息级幂等键、gone/retryable 二分 |
| worker Session + 目录 | worktree isolation / sessions / subagentbatch | 部分已有（B6） | 人工额外槽位、恢复说明、env 契约 |
| Prompts 版本化 | skills runtime / profile | 协议借鉴（B7） | baseRevision/占位符/Default updated |
| model-surface / noWait | toolbroker / progressive disclosure / turn budget | 护栏借鉴（B8） | 预算断言、主循环禁等守卫 |
| docs-as-spec | docs/ + driftguard 先例 | 模式扩展（B9） | 目录级索引/链接自动校验 |
| health/status/巡检 | runtime health / ops scripts | 范式借鉴（B10/B11） | 退出码语义、唯一写者、阈值 |
| 面板原则 | console / notify 中台 | 规范借鉴（B12） | 文案清单、阶段标签 |

---

## 5. 路线 A：把 SAP 整包"直接利用"（零改动）

**适用场景**：团队需要一个"现成可跑"的仓库值守 agent（修 bug、跟进 review、答疑、开 PR），且不打算先改 AAR；或想先观察这类 agent 的真实行为，再决定 AAR 上要补什么。

**做法**：按 SAP `docs/006-deployment.md` 部署——专用 DSH profile（`superagent`）+ 专用 agent GitHub 账号（gh-agent 包装）+ 可选飞书 bot（lark-cli 包装）；配置仓库、Policy（允许名单/准入表/规则）、Execution（worker 目录与 prepare 命令）；用其内置面板监督；可选接入小时巡检脚本。

**能直接"利用"的成品（AAR 侧可直接借用或对照）**：

- GitHub / 飞书两套 Source 适配语义（通知、订阅 Issue/PR、CI、评论、贴图读取）；
- 工单/授权/面板/审计全链路（Trigger→Decision→Ticket→Assignment→Report）；
- 运维范例：状态码、巡检提示词、面板"只显示需要人"的交互。

**前置条件与限制**（README / docs/006 明确）：

- 研究原型，"可能仍有 bug、工程实践不一致"；非官方 DeepSeek 产品；
- 依赖 Node 22.19+/DSH 0.2.1 环境、tmux、独立 OS 账号才谈得上隔离（预设为 `danger-full-access`，改进程级 PATH/git 配置）；
- 仅 GitHub.com（不支持 GHES）；记录只增不删（无 GC）；飞书可选、默认禁用；
- 许可证 MIT：**借鉴设计无额外义务；复制代码须保留版权声明**。

**决策建议**：若目标只是"让某个仓库有一个值守 agent"，路线 A 是零成本试点；若目标是"把这类能力做进 AAR 产品"，路线 A 只作行为样机，主线走路线 B/C。

---

## 6. 明确不借鉴（避免过度移植）

| SAP 侧 | 不借理由 |
|---|---|
| 单文件 SQLite（`orchestrator.sqlite`）+ 无删除/无 GC 的存储管理 | AAR 已是多库 + CAS + 租约 + reaper 的更成熟形态；且本仓库经历过 SQLite 多进程锁问题（`docs/ANALYSIS-sqlite-lock-problem.md`），不应引入弱于现状的模型 |
| DSH 宿主耦合层：profile/preset、`cordis.patch.yml`、tmux 服务脚本、进程级 PATH/git 全局修改 | AAR 有自己的宿主体系（aicli/gateway/runtime-server）与权限栈；宿主耦合不可迁移 |
| 面板具体实现（React + DSH client primitives） | 只借 UX/文案原则（B12），不借组件与路由 |
| 原型代码风格与工程实践 | SAP README 自陈"研究代码、工程实践不一致"；借鉴清单限定在协议与纪律层 |
| 已明显弱于 AAR 的部分：无跨实例租约、无写者栅栏、无监督收敛、无 durable 批次恢复 | 重复实现会倒退；只做语义对齐核查 |

---

## 7. 风险与实施前置

1. **文档—代码漂移**：SAP 文档与实现已有少量出入（§1 所列），任何"照文档抄协议"都应以代码复核为准（本文已给出两处偏差）。
2. **语义跨宿主失真**：SAP 的协议是围绕 DSH Session/Source 抽象写的；移植到 AAR 时须保留其"**core 纯域 + ports 适配**"边界（`src/core/ports.ts` 的六端口形态），否则会退化成"又一个权限开关"。
3. **两层授权叠加复杂度**：B1 建议"分诊输入 + 既有权裁"两层，需先定义两者的优先级与审计字段，避免权限语义分叉（与 folder-trust/D29 文档一并评审）。
4. **产品边界**：路线 C 是产品级投入（work-item 域 + 控制台 + 面板），需单独立项评审；本文只做映射，不做承诺。
5. **许可合规**：MIT 引用/复制保留版权；SAP 中引用的第三方（gh/lark-cli 等）各自许可需另核。
6. **复查口径**：AAR 现状已于 2026-11-01 二次复查做代码级抽查（§10）；仍未抽查的项（如 B6 的"人工唤醒优先级通道"是否存在、B3 的 provider 级永久失败分类）在实施前需按 HEAD 代码复核。

---

## 8. 建议落地顺序（含验收口径）

**短期（1：低成本、独立收益）**

1. `B9` docs 索引/链接自动校验脚本（CI）——验收：故意破坏可捕获；
2. `B3` connectors 发送契约（幂等键 + gone/retryable）写入契约包与测试——验收：重复投递/不可达/可重试用例；
3. `B2` 展示层阶段函数接入控制台/通知/巡检——验收：阶段唯一分类 + "未读回退"用例；
4. `B12` 通知/文案复查清单化——验收：通知模板评审走查记录。

**中期（2：需要少量改造）**

5. `B1` 入站分诊域（记录态先行：strength/scope/policy_version/basis + observe 语义），再评审是否接入执行路径；
6. `B4` Effects 语义检查单 vs background/outbox（kind/key 合并、ordered、Later、3 连败阈值）；
7. `B5` connectors 契约补全（scope/重校验同源、准入行、health claim、KV 注册表）；
8. `B6/B7/B8` 分项评估：人工额外槽位、恢复说明、prompt baseRevision、模型面预算断言。

**长期（3：产品级）**

9. 路线 C：work-item 域（Ticket/Mandate/Stage）+ 面板原则，复用 AAR 执行面；先出设计文档再立项。

**建议的下一步动作**：已整理为同目录 `implementation-plan.md`（S0–S9 切片，含 `B1→S5`、`B3→S2` 的接口草案与验收矩阵）；下一步按 S0 核实 → 评审（决策 D1–D6）→ 逐切片实施，延续本仓库"方案 → 评审 → 实施切片"的既有流程。

---

## 9. 参考

**SAP（`E:\projects\ai\dsh-superagent-prototype`）**

- `docs/000-philosophy.md`（注意力哲学）、`docs/001-domain.md`（领域模型）、`docs/002-authorization.md`（授权）、`docs/003-execution.md`（执行/恢复）、`docs/004-prompts.md`（提示版本化）、`docs/005-panel.md`（面板原则）、`docs/006-deployment.md`（部署/运维/API）、`docs/007-architecture.md`（模块索引）、`docs/008-cookbook.md`（Source 接入教程）；
- `src/core/{engine,ports,model,stage}.ts`、`src/sources/kind.ts`、`src/app/{effects,outbox,http}.ts`、`src/executor/dsh.ts`。

**AAR（本仓库）**

- `docs/README.md`（文档总入口）；
- `docs/bots/README.md` 及 01–14 号文档（connectors/notify/订阅路由）；
- `docs/background-job-lifecycle.md`（background 生命周期与 CAS/租约）；
- `docs/analysis/deepseek-harness-vs-aar-agent-system.md`、`docs/analysis/dsh-goal-domain-vs-aar-goal-20261017.md`（既有 DSH 类借鉴先例与对照方法）；
- `docs/skill_runtime/README.md`（skills runtime 分层与治理）；
- `docs/product/{folder-trust,project-permissions,harness-settings}.md`（权限/信任面）；
- `docs/ANALYSIS-sqlite-lock-problem.md`（存储锁定教训，§6 依据）；
- `docs/analysis/commandcode-*-borrowing-*.md`（本仓库借鉴分析文档的既有格式先例）。

---

## 10. 二次复查记录（2026-11-01）

**方法**：① 通读报告全文（结构、表格、优先级一致性）；② 对 AAR 做代码级抽查（目录/文件存在性 + 关键字检索，非全量审读）；③ 校验报告引用的文档/路径存在性；④ 复核 SAP 侧关键结论（仍以代码为准）。

**已确认（抽查范围内）**：

| 项 | 证据 |
|---|---|
| connectors 已落地（含契约包与四家 provider） | `backend/internal/connectors/{service,dispatch,queue,store}.go`、`api/schema.go`、`providers/{feishu,qqbot,telegram,wechat}` |
| 多实例派发 R1/R2a/R3 已实现 | `backend/internal/connectors/dispatch.go` 头注释（指向 `docs/bots/09 §7.2`） |
| 跨宿主通道去重键 | `backend/internal/connectors/channel_key.go`（"同一通道实例不重复投递"） |
| notify 中台存在 | `backend/internal/notify/{dispatcher,presence,kinds}.go`、`channels/` |
| background CAS/租约 | `backend/internal/background/{store,manager,observation}*.go`（`state_version`/lease） |
| supervision CAS 控制动作 | `backend/internal/supervision/{action_service,execution_store}.go`（`ExpectedVersion`） |
| subagent 批次终态投递 | `backend/internal/agent/subagent_batch_coordinator.go`（`BatchTerminalNotification`、有界重试/退避）与 `batch_lifecycle.go` |
| goal stalled 门控 / 结构化回执 | `backend/internal/goal/continuation.go`；`backend/internal/agent/{iterate_goal,loop}.go` |
| worktree 隔离 | `backend/internal/isolation/worktree/worktree.go` |
| turn budget | `backend/internal/agent/turn_budget_*.go` |
| driftguard（文档↔代码漂移锁）先例 | `backend/internal/driftguard/{driftguard,driftguard_test}.go` |
| 报告引用的文档/脚本路径均存在 | `docs/bots/01–14`、`docs/product/*`、`docs/analysis/commandcode-*-borrowing-*.md`、`docs/ANALYSIS-sqlite-lock-problem.md`、`docs/skill_runtime/README.md` 等 |

**本次复查修正的表述**（已改入正文）：

1. `B1/B2/B3/B4/B5/B9` 的"AAR 现状"由文档口径升级为**代码抽查口径**并补充证据路径；
2. `B3` 缺口收窄：由"跨通道发送语义尚无规范"改为"已有通道实例去重、入站/账本去重、失败回复重放；缺**出站消息级幂等键与永久失败分类**（待逐行复核）"；
3. `B5` 由"建议新建契约包"改为"`backend/internal/connectors/api` **已存在**，属对照现有实现补全字段集"；
4. `B9` 由"无自动校验先例"改为"已有 `driftguard`（单文档级）先例，缺口是**目录级**索引/链接校验"；
5. `B4` 补充 subagent 批次 durable 终态 outbox 的代码证据；
6. `§0-4` 与 `§8` 的"优先级 vs 实施节奏"差异补充说明（B1 价值最高、先落记录态）。

**复查结论**：三条主结论不变——SAP 的价值在授权分诊与生命周期协议；AAR 执行面更强、不引其存储/宿主耦合；最优先 `B1/B2/B3`。其中 `B3` 的**落地起点比初稿判断更高**（部分机制已存在，重点转为补齐消息级幂等、失败分类与留档规范）。

**遗留待核实（实施前）**：`B6` 的"人工唤醒优先级通道"是否存在；`B3` 的 provider 级"永久失败"识别（如飞书撤回/群解散、Telegram 不可达）是否已有等价实现；`B2` 的"未读消息回退"在 mailbox/connectors 的实际行为。
