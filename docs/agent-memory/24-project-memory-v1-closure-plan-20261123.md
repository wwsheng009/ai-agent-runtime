# Project Memory v1 落地闭环与发布计划（2026-11-23）

> 状态：当前发布计划 / 执行基线
> 参考设计：`references/AI_Agent_Memory_Engine_Design.md` v1.0
> 现行契约：`03-target-architecture.md`、`04-data-model-and-api.md`、`06-test-and-acceptance.md`
> 当前状态证据：`19-implementation-status-20261123.md`、`20/21/22-*-evidence-20261123.md`、`23-g0-dogfood-read-only-diagnosis-20261123.md`
> 历史施工计划：`05-implementation-plan.md`、`16-ai-memory-engine-implementation-plan-20261122.md`

## 0. 决策摘要

1. 参考设计是**长期 Memory Engine 路线图**，不是要求一次交付全部能力的单一版本验收清单。
2. 当前产品目标收敛为 **Project Memory v1**：项目级、可审计、可检索、可注入、可纠正的 Agent Memory。
3. v1 只承诺一条 normal path：

```text
可信 project/workspace
  -> canonical turn capture
  -> durable capture receipt
  -> 一个 extraction job
  -> 有界 transcript 读取
  -> redaction / scope / source validation
  -> candidate
  -> deterministic consolidation
  -> active memory_entries
  -> FTS/LIKE SearchRecords
  -> 有界 project_memory 注入
  -> citation/usage
  -> correct / forget / rollback
```

4. v1 不包含 Dreaming/Patch、Episode、Entity/Relation、Semantic/Vector/RRF、跨项目 profile promotion、独立 Core Memory 事实层和完整运营平台。这些能力不进入当前 release gate。
5. 当前代码已经有可运行的 Memory v2 主干，但**不能宣布整体闭环**：G0-5 真实恢复未执行，G1 三端同一 corpus 证据不足，G2 运营面与多 workspace 已补代码级证据但改动未提交、发布复现未完成，G4/G5 未实施。代码存在不等于发布门禁通过。
6. 实施必须按 P0 → P1 → P2 → P3 单线推进。P3 通过即发布 v1，并停止当前实施线；后续能力另开 RFC，不并入 v1 主闭环。

本文不把 `15/16` 的历史缺口清单继续扩展成新工作包。`15` 是 2026-11-22 的参考设计审查快照；FTS5、版本/回滚和 scope 基础已有后续提交，施工前应以 `19/21/22`、当前代码和实时测试为准。`16` 当前有未提交的 G2-4 增量，不能直接当作稳定发布证据。

---

## 1. 参考设计如何收敛

### 1.1 设计域与 v1 映射

| 参考设计域 | v1 处理 | 不进入 v1 的部分 |
|---|---|---|
| §3 Capture / Write | canonical receipt、异步 extraction、source/provenance、candidate → active | 多抽取路径、复杂 event bus |
| §4 Memory Types / Tiers | 现有 project kinds：preference、decision、fact、workflow、pitfall、reference、open_question、correction | working/episodic/reflective/procedural 独立层 |
| §5 Write Pipeline | 规则过滤、结构化抽取、脱敏、source 校验、确定性 Consolidation | Salience 六因子评分、Entity Resolution |
| §6 Retrieval | FTS5/BM25 + LIKE fallback + 可解释单路排序 | Semantic、Entity、Temporal Graph、RRF、rerank、vector |
| §7 Context Builder | stable `project_memory` + 有界 `project_memory_recall`，硬 token budget | 独立 Core Memory、tiered brief、working digest |
| §8 Dreaming | 不实现；只保留审计化 correction/rollback | Replay/Analyze/Represent/Patch 审核管线 |
| §9 SQLite | SQLite 唯一事实源；FTS 是派生索引 | 向量索引、图数据库 |
| §10 API / Tools | 最小 records/search/status/lifecycle 控制面 | memory_dream、复杂运营 API、完整 Web 管理平台 |
| §11 Job System | 一个 extraction job；lease/retry/idempotency/恢复 | 多类 Dream/Embed/Wiki/Verify 作业 |
| §12 Lifecycle | revision/event、tombstone/suppression、record rollback | 完整时态图、复杂有效期查询 |
| §13 Project Memory | 项目决策、约束、工作流、坑、已验证结论 | 全量源码/工具输出长期存储 |
| §14 Security | project scope、fail-closed、secret redaction、admin 授权、审计 | 完整 tenant/user/agent/task scope 产品化 |
| §15 Evaluation | 确定性门禁 corpus + 小规模 dogfood + 基本指标 | 完整 Recall@K/MRR/成本评测体系 |
| §16 Roadmap | 只取 Phase 1 的 Project Memory 子集 | Phase 2–5 全部 |

### 1.2 参考设计 MVP 条目的处理

参考设计 §19 的 10 条 MVP 标准，v1 不是全部照单接收：

| 参考设计条目 | v1 判定 |
|---|---|
| 显式保存并在新会话召回 | 必须通过 |
| Task Completion Episode | 延期；不属于 v1 |
| Semantic + 精确标识符检索 | v1 只做 FTS/BM25 + LIKE；语义检索延期 |
| Project scope isolation | 必须通过 |
| 事实更新不丢历史，current/historical query | v1 必须有 record revision/rollback；完整历史查询延期 |
| 硬 token budget | 必须通过 |
| Dreaming reviewable Patch / rollback | v1 只做 record rollback；Dreaming 延期 |
| 外部 prompt-injection 内容不能成为 trusted instruction | v1 必须有基本 trust 边界与负向测试 |
| inspect / correct / delete | 必须通过 |
| 指标暴露 extraction/retrieval/cost/latency | v1 必须暴露最小指标；完整质量评测延期 |

结论：**v1 是参考设计 Phase 1 的一个可交付子集，不是“参考设计已全部完成”。**

---

## 2. Project Memory v1 范围

### 2.1 产品承诺

在同一个可信 project/workspace 内，用户可以：

1. 显式记住一条项目事实；
2. 让系统从 canonical turn 自动提取候选项目记忆；
3. 在后续会话中检索到同一条记忆；
4. 以有界、可解释的方式注入到上下文；
5. 看到 citation/usage，并检查来源；
6. 纠正、遗忘、回滚一条记忆；
7. 在 job 失败、进程重启、数据库暂时不可用时保持可诊断、可恢复，且不阻塞 Agent 主流程。

### 2.2 v1 必须完成

- 单一 project/workspace scope；
- canonical transcript 为消息事实源；
- durable capture receipt；
- 一条异步 extraction 路径；
- candidate → active 晋升门禁；
- source/provenance；
- secret redaction；
- deterministic consolidation（不依赖 Dreaming）；
- FTS5 + LIKE fallback；
- 有界检索和注入；
- citation/usage；
- correction / forget / rollback；
- tombstone + suppression，删除后不复活；
- job lease/retry/idempotency；
- 失败、重启、数据库不可用时的降级；
- HTTP / CLI / Agent Tool 共用同一 Store/Retrieval 逻辑；
- 基本审计与 scope 负向测试。

### 2.3 v1 明确暂缓

以下能力冻结，不新增表、API、worker 或产品承诺：

- G4 Episode / Dreaming / Patch / Reflective memory；
- G5 Entity / Alias / Relation / Temporal graph / RRF / rerank / vector / embedding；
- user/profile/tenant/agent/task scope 的产品化；
- cross-project profile promotion；
- handoff / session continuation package；
- tiered brief / L0-L2 多层展开；
- 独立 `memory_core` 作为第二套长期事实系统；
- 多种并行 extraction 路径；
- 复杂 Web 管理平台；
- 新增“更多 memory 类型”的扩展；
- 以 Dreaming 自动重写 active memory。

已有 schema 中的未来表、已有 `memory_core`/episode/patch/dream 代码、已有管理 API 可以保留为**冻结的兼容/实验代码**，但不能用来证明 v1 闭环，也不能继续扩展。

---

## 3. 唯一 vertical slice

### 3.1 主链路

```text
[1] trusted project identity
      |
[2] canonical turn commit
      |
[3] durable capture receipt
      |
[4] memory materialization
      |
[5] one extraction job
      |
[6] bounded canonical transcript read
      |
[7] redaction + scope + source validation
      |
[8] candidate
      |
[9] deterministic consolidation
      |
[10] active memory_entries
      |
[11] unified SearchRecords (FTS5/LIKE)
      |
[12] bounded project_memory injection
      |
[13] citation / usage
      |
[14] correct / forget / rollback
```

### 3.2 链路不变量

1. **事实源不变量**：canonical transcript 是消息事实源；memory DB 不保存完整 transcript。
2. **单一正常路径**：receipt-backed 新回合只由一个 capture worker / 一个 extraction job 处理；legacy session replay 只处理历史兼容数据。
3. **来源不变量**：每条 active 自动记忆至少有一个 `memory_sources`；没有 source 的候选不得晋升。
4. **晋升不变量**：自动抽取只能写 candidate；只有确定性 Consolidation 通过 scope/source/freshness/suppression 检查后才可 active。
5. **删除不变量**：delete 产生 tombstone + suppression；后续抽取不得复活，检索/注入不得可见。
6. **隔离不变量**：所有读取同时匹配 tenant/project/scope；缺上下文 fail closed；跨 project 泄漏为 0。
7. **预算不变量**：注入只发生在 context 动态尾部；同一 turn 最多一组 memory；总 token 不超配置预算。
8. **降级不变量**：memory store 不可用、FTS 不可用、summary 缺失、job 失败都不阻塞普通 Agent 请求。
9. **引用不变量**：citation 只接受本 turn 注入或本 turn 工具读取过的 id；非法 citation 不增加 usage。
10. **控制面不变量**：HTTP、CLI、tool 使用同一 query/selection/授权语义，不各自实现一套检索。

### 3.3 receipt / digest / window 的 v1 口径

- `capture receipt` 是 canonical → memory 的 durable handoff，保留；
- `memory_turn_digests` 和 `memory_turn_windows` 只能作为内部 materialization/progress、兼容和恢复设施；
- 它们是派生对象，不是第二份 transcript、不是用户可见的长期记忆、不是新的产品对象；
- v1 不再新增第三套 receipt/digest/window/phase 名称；
- 大 session、崩溃恢复和 backlog 需要它们时，只按恢复路径使用，不作为普通链路的前置产品逻辑。---

## 4. 数据与接口冻结

### 4.1 v1 数据对象

v1 只承诺以下对象。其他表可以存在，但不属于发布路径。

| 对象 | v1 角色 | 约束 |
|---|---|---|
| `memory_entries` | 长期记忆事实 | active/candidate/superseded/deleted/expired；必须带 scope/project/source |
| `memory_sources` | 证据关联 | active 自动记录必须至少一条 source |
| `memory_events` | 审计事件 | create/update/correct/supersede/delete/rollback/usage 可追溯 |
| `memory_versions` | 变更历史 | 记录级 revision；支持 rollback |
| `memory_jobs` | 异步工作项 | 一个 extraction 类型；lease/retry/idempotency |
| `memory_suppressions` | 防复活 | delete/forget/correct 后阻止相同内容重新生成 |
| `memory_usage` | citation/use 反馈 | 只记录注入、读取、引用等可验证事件 |
| `memory_entries_fts` | 派生检索索引 | 可从 `memory_entries` 重建，不是事实源 |
| `memory_summaries` | 可选物化视图 | 只在启用 summary 时使用；不是第二事实源 |

冻结、不作为 v1 发布路径的对象：

```text
memory_core
memory_episodes
memory_patches
memory_dream_runs
memory_entities
memory_relations
memory_handoffs
```

已有未来表/代码可以保留，但 v1 不新增其 API、worker、迁移和使用场景。

### 4.2 最小公开接口

v1 的 release 控制面只固定以下语义；路径可沿用现有 `/api/runtime/memory/...`：

| 能力 | 最小接口 |
|---|---|
| 状态 | `GET /memory/status` |
| 显式记住 | `POST /memory/records` |
| 列表/检索 | `GET /memory/records?query=...`（内部统一走 `SearchRecords`） |
| 读取 | `GET /memory/records/{id}` |
| 来源 | `GET /memory/records/{id}/sources`（管理员/本机） |
| 纠正 | `PATCH /memory/records/{id}` |
| 遗忘 | `DELETE /memory/records/{id}`（管理员/本机，逻辑删除） |
| 恢复 | `POST /memory/records/{id}/restore`（仅 tombstone） |
| 历史 | `GET /memory/records/{id}/versions` |
| 回滚 | `POST /memory/records/{id}/rollback`（管理员/本机，显式 revision） |
| 手动修复 | `POST /memory/extract` 或 `POST /memory/consolidate`（仅运维/管理员，不属于普通用户主路径） |

CLI 至少保留：

```text
/memory status
/memory add <text>
/memory list [n]
/memory search <query>
/memory show <id>
/memory correct <id> <text>
/memory forget <id>
/memory versions <id>
/memory rollback <id> <revision>
```

Agent Tool 至少保留：

```text
memory_search
memory_read
memory_add          # 仅在用户明确要求记住时
memory_correct      # 仅在用户明确纠正时
```

`memory_forget` 默认不暴露给 Agent；只能由用户命令或管理 API 触发。`memory_dream_run`、Core 管理、episode/patch API 等全部冻结。

### 4.3 统一实现边界

HTTP handler、CLI、Agent Tool 必须调用同一套：

- `Store.SearchRecords`；
- `Store.GetRecord` / `SelectForInjectV2`；
- `memoryRequestContext` 的 scope/授权解析；
- citation/usage 解析；
- redaction 与 suppression 检查。

不得在某个入口单独拼接 SQL、单独实现排序、单独绕过 scope 或单独忽略 citation allowlist。

### 4.4 Scope 冻结

v1 release 配置只允许：

```text
scope = project
project_key = canonical workspace identity
```

- `tenant_id/owner_id/agent_id/task_id` 字段可以保留，作为内部上下文和未来扩展点；
- v1 不开启 user/profile/agent/task scope 的产品行为；
- 不新增 scope grant、cross-project promotion、profile import；
- 缺 project key、tenant 不匹配、workspace 不可 canonicalize 时 fail closed；
- 同一 workspace 的不同路径写法必须落到同一 key；
- 跨 workspace 负向测试是 release gate，不得只依赖前端参数过滤。

### 4.5 配置与模式冻结

v1 只保留三种模式：

```text
off     不自动打开/写入/注入 v2；手工路径按现有兼容行为
shadow  允许 capture/extract/consolidation，但不注入；用于质量观察
on      允许 capture + retrieval + bounded injection
```

发布 profile 固定为：

- `scope=project`；
- `generation.trigger=both` 或等价的 `idle + session_end`，但两者必须入同一个 capture worker/job，不能形成两条抽取链；
- 注入默认使用 `notes`；如果发布方选择 `summary`/`hybrid`，必须额外通过 summary gate，不能把未验证模式作为默认；
- `retention.enabled=false` 起步，v1 只要求显式 delete/forget；自动 retention 属于 v1.1；
- `tools` 只开启只读搜索/读取和显式 add/correct；
- 所有未列入 release profile 的开关保持关闭。

---

## 5. 分阶段实施

### P0：冻结边界与基线

**目标：** 把“继续扩展”改成“有固定发布目标”。

工作项：

1. 在独立 worktree 或已固定提交上施工；不得在 164 项并发改动的主工作区直接混入发布证据。
2. 记录实时基线：`git rev-parse HEAD`、`git status --short`、memory 包测试、API/CLI 定向测试、`mode=off` 的 prompt hash/消息顺序。
3. 固化 v1 scope：project only、notes 注入、一个 extraction job、无 G4/G5。
4. 固化最小对象/API/CLI/tool 清单；把其余接口标记为 experimental/admin，不作为验收项。
5. 建立 release evidence 模板（建议：`25-project-memory-v1-release-evidence-YYYYMMDD.md`，P3 再创建）。
6. 明确 G4/G5、Core、user/profile scope 的冻结边界；禁止新增 schema migration、worker、public API。
7. 建立 P0-P3 的通过/不通过矩阵；任何新发现先归类到 gate，不能直接变成新功能。

**退出条件：**

- 发布 profile、scope、数据对象、API 清单和冻结能力已签字；
- `mode=off` 与基线差异为 0；
- 没有新的 G4/G5、scope、vector、Dreaming 表或接口进入 v1；
- 当前工作区已知阻塞被记录为“单独处理”，没有混入 memory release；
- 后续每个变更都能映射到 P1/P2/P3 的一个 gate。

**本阶段禁止：** 以“顺手补齐”为理由新增功能。

### P1：显式记忆闭环

**目标：** 证明用户显式保存的项目事实能持久化、检索、注入、引用，并能纠正/遗忘/回滚。

工作项：

1. 用手工 add 建立同一 corpus：一条 decision、一条 fact、一条 pitfall。
2. 验证 HTTP、CLI、Agent Tool 对同一 corpus 返回相同的 memory id 和检索结果。
3. 在新会话中触发一次 bounded `project_memory` 注入；验证记录 id、scope、revision、token 数量。
4. 验证 citation block 被解析并写入 usage；非法 citation 不增加 usage。
5. 验证 correct 产生新 revision、旧记录 superseded、来源保留。
6. 验证 forget 产生 tombstone + suppression；搜索/注入立即不可见。
7. 验证 rollback 恢复到指定 revision；回滚后旧/新状态与事件一致。
8. 验证跨 workspace、缺 scope、private 记录的负向访问。
9. 验证 `mode=off` 不因手工路径产生 v2 自动副作用。

**建议测试：**

```text
TestProjectMemoryV1OffBaseline
TestProjectMemoryV1ManualLifecycle
TestProjectMemoryV1SameCorpusAcrossSurfaces
TestProjectMemoryV1InjectionBudgetAndCitation
TestProjectMemoryV1ScopeAndPrivateNegative
```

**退出条件：**

- 一条手工事实从 add 到新会话注入完整通过；
- 三端同 corpus 返回同一 id/同一排序；
- 删除后检索、注入、后续抽取均为 0；
- rollback 后历史和新状态都可验证；
- token 超预算 = 0；跨 workspace 泄漏 = 0；
- P1 未通过前，不开始自动抽取。

### P2：自动记录闭环

**目标：** 证明 canonical turn 可以自动变成 candidate，再经 Consolidation 变成 active，并在下一会话被召回。

工作项：

1. 固定 canonical transcript fixture，覆盖 user 明确事实、项目 decision、失败/pitfall、一次性噪声。
2. 验证 turn commit 写入 durable receipt；重复 turn/重复 worker 不产生重复 job。
3. 验证唯一 extraction job 读取 receipt 固定的有界 range；抽取前后校验 generation/revision/range/hash。
4. 验证 redaction、source required、scope required、suppression 检查。
5. 验证 LLM/结构化输出失败、空输出、非 JSON、字段超长时：job 失败可观测、可重试，不写 active。
6. 验证 candidate 不直接进入注入；deterministic Consolidation 才可晋升为 active。
7. 验证一个 source 重复抽取不产生重复 active；相同内容去重，冲突按 revision/优先级处理。
8. 验证 job lease、心跳、超时、接管、退避、最大重试、重启恢复。
9. 验证 legacy session/digest 路径只处理无 receipt 的历史数据；receipt-backed 新回合不会被 legacy 路径重复消费。
10. 验证下一会话注入新 active 记录，并产生 citation/usage。
11. 验证 `mode=off` 模型调用数为 0；`shadow` 只生成、不注入。

**建议测试：**

```text
TestProjectMemoryV1CaptureToActive
TestProjectMemoryV1OneExtractionPath
TestProjectMemoryV1IdempotentReplay
TestProjectMemoryV1LeaseRetryRestart
TestProjectMemoryV1SourceRedactionAndSuppression
TestProjectMemoryV1CandidateDoesNotInject
TestProjectMemoryV1NextSessionRecall
```

**退出条件：**

- 一条合成事实从 canonical turn 到下一会话注入完整通过；
- 同一 receipt/turn/range 重复执行不重复写 candidate/active；
- 失败 job 有错误、attempt、lease、可重试状态，且重启后可恢复；
- 无 source、跨 scope、secret、stale/删除内容均不落 active；
- legacy 与 receipt 路径不重复消费；
- 抽取失败不影响普通回答；
- P2 未通过前，不开始 dogfood 或发布。

### P3：发布验收与稳定化

**目标：** 在隔离环境和小规模 dogfood 中证明 v1 可交付，并冻结发布。

工作项：

1. 备份真实 `.aicli/memory/memory.db`；先只读诊断，不直接 reset/migrate/清理。
2. 对历史 pending digest/job 做分类：完成、跳过、dead-letter、明确失败原因；不得用空 phase2 成功冒充闭环。
3. 在隔离项目或复制库上执行一次受控恢复演练；记录恢复前后计数。
4. 运行 HTTP/CLI/tool 同一 corpus 的三端验收；记录返回 id、排序、token、usage。
5. 覆盖进程重启、worker hard-kill、数据库不可用、DB corruption、FTS 不可用、busy/lock、权限拒绝。
6. 覆盖 secret、跨 project、跨 workspace、未授权 destructive API、citation 伪造的负向测试。
7. 记录最小指标：capture success、extraction success、candidate → active、recall hit、injection tokens、job retry、cross-scope leak、secret leak。
8. 更新 `19`/`README` 状态和 release evidence；未关闭问题只允许是非主链路后续项。
9. 冻结发布：只修 gate 阻断项，不再新增功能。

**退出条件：**

- 五个 release gate（§6）全部通过；
- 在隔离提交或固定 worktree 上可复现；
- 真实/复制 dogfood 库有完整写入、检索、注入、usage 证据；
- 关键负向测试全部通过；
- 未关闭问题清单不含主链路阻断项；
- owner 签署“Project Memory v1 可交付”，而不是“完整 Memory Engine 已完成”。

---

## 6. Release Gate

### Gate 1：Write

一条用户事实或一条合成项目事实可以从：

```text
capture / add
  -> persist
  -> candidate 或 manual active
  -> source + event
```

验证：

- 自动抽取只写 candidate；
- 手工 add 可 active；
- 无 source 不 active；
- secret/scope/suppression 拒绝；
- 重复写不重复。

### Gate 2：Read

同一 workspace 的下一会话可以通过统一 `SearchRecords` 检索到同一条记录：

- HTTP/CLI/tool 返回同一 id；
- FTS5 可用时使用 FTS/BM25；
- FTS5 不可用时 LIKE fallback 结果语义一致；
- 精确路径、错误码、函数名、版本号可命中；
- 不返回跨 project 记录。

### Gate 3：Injection

- 只注入 active + fresh + allowed scope；
- 注入在 dynamic tail，system/profile 前缀不变；
- 每 turn 最多一组 memory；
- 总 token 不超预算；
- 注入内容标记为历史数据，不作为指令；
- memory 不可用/为空/无 FTS 时降级而不阻塞。

### Gate 4：Lifecycle

- correct 写新 revision，旧记录 superseded；
- forget 写 tombstone + suppression，后续检索/注入/抽取不可见；
- rollback 恢复到指定 revision，事件可审计；
- job 失败可查看、可重试、可恢复；
- 进程重启后 pending job/digest 不丢失、不重复。

### Gate 5：Security

- 跨 project/workspace 泄漏 = 0；
- secret 落库 = 0；
- 未授权 destructive API 成功 = 0；
- citation 伪造导致 usage 增长 = 0；
- DB 不可用不阻塞 Agent；
- private/tombstone 记录普通读取失败；
- 子 agent/team 默认无写权限。

**发布判定：** 五个 gate 全部通过，且有可复现证据；任一 gate 失败，不发布 v1，但不能通过新增功能来“绕过”失败项。---

## 7. 后续版本：必须另开 RFC

v1 关闭后，不自动进入“继续实施”。下列能力分别作为独立版本/独立 RFC，拥有独立目标、数据模型、验收和回滚，不并入 v1 主闭环：

| 版本 | 候选范围 | 进入条件 |
|---|---|---|
| v1.1 | summary/core 物化视图、retention、handoff、user scope 的探索性实现 | v1 发布；有独立 schema/API/验收；不能改变 v1 事实源 |
| v2 | semantic / entity / temporal / RRF / vector / rerank | v1 FTS 质量基线稳定；有可重复评测 corpus；有独立降级策略 |
| v3 | Episode / Dreaming / Patch / Reflective / negative learning | v1 记录级回滚与审计稳定；有 patch 审核/回滚设计 |
| v4 | Adaptive memory / utility ranking / cross-project profile | v1-v3 均有真实使用证据；有用户确认和跨 scope 安全门禁 |

每个后续版本的门禁必须回答：

1. 它解决的真实用户问题是什么？
2. 它是否新增事实源？如果新增，为什么现有 `memory_entries` 不够？
3. 它是否改变 scope、注入、token 或删除语义？
4. 它的回滚和降级是什么？
5. 它如何证明不会让 v1 的五个 gate 退化？

没有上述答案，不进入实施。

---

## 8. 当前状态与首批动作

### 8.1 当前状态口径

以 `19`、`20`、`21`、`22` 和当前代码为准：

| 范围 | 当前状态 | 对 v1 的影响 |
|---|---|---|
| 手工 add/search/注入 | 代码和部分测试已有 | P1 仍需三端同 corpus 和生命周期证据 |
| capture receipt / extraction | receipt-first 路由已有 | P2 仍需幂等、lease、重启和单一链路证据 |
| FTS5 / SearchRecords | FTS、统一检索、有界候选已有 | Gate 2 仍需 HTTP/CLI/tool 同 corpus 证据 |
| project scope | v24 scope 基础、v30 purge/capture tenant、G2 收尾测试与多 workspace 负向测试已有 | v1 project scope 代码级收尾（未提交）；完整 multi-tenant same-project 仍留作后续 RFC |
| record versions / rollback | G3 代码级主体已有 | Gate 4 仍需与 correction/delete/rollback 的端到端组合证据 |
| Core / summary | Core 部分实现、summary 部分实现 | 不纳入 v1 硬门禁；现有代码冻结，不再扩展 |
| G0-5 dogfood | 2026-11-24 一致快照：副本 v23→v32 migration、123 digest 精确分类（already_queued 9 / missing_receipt 17 / not_orphaned 97 / eligible 0）、107 failed job operator 分类、8/8 deadline repair、operator 动作模拟和 dead-letter→digest close 恢复操作已完成；真实库未迁移/修复 | P3 仍需真实库 raw 备份与受控执行、17 条 legacy 提取、64 条 requeue 执行验证、新会话注入与 busy/lock/corruption 演练 |
| G4/G5 | 未实施 | 冻结，不进入 v1 |
| Live model E2E | `magpie` / `baidu-qianfan/deepseek-v4.1-flash` / `max` 的内存核心链路已在隔离 worktree 连续两次通过 `-race` 真实调用 | 这是 P2 的合成数据证据；HTTP/CLI/tool 同 corpus、dogfood 与 Gate 1–5 仍须完成 |
| 当前工作区 | 并发改动多；`16` 有未提交增量 | 发布施工必须使用隔离 worktree/固定提交 |

**2026-11-23 增量证据（G1 检索接线）：**

- HTTP `/api/runtime/memory/records` 支持重复 `status`/`kind`/`tag` 与逗号分隔值；
- CLI 本地、结构化命令和 runtime-server 远端命令统一支持 `--kind`/`--tag`；
- FTS5 `listRecordsFTS` 现在应用 `Statuses`、`Kinds`、`Tags`，不再由 FTS 路径绕过过滤；
- `TestMemoryCrossSurfaceSameCorpus` 用同一临时 corpus 验证 HTTP、CLI、`memory_search` tool 返回同一记录集合；
- `TestMemoryAPIMultiValueKindTagFilters` 与 `TestStructuredMemoryListAndSearchUseMultiValueKindTagFilters` 覆盖重复值、逗号值和 FTS 查询过滤。

这只推进 G1 的接线与三端一致性，不代表 G1 的全部质量语料/性能/多 workspace 证据已经关闭。

**2026-11-23 增量证据（G1/G2 稳定性）：**

- `memory_turn_digests` insert 补齐 `processed_at` 值，修复 `37 values for 38 columns`；
- `TestMemoryJobOperationalQueriesAreTenantScoped`、`TestMemorySummaryOperationalQueriesAreTenantScoped` 与 `TestMemoryTurnOperationalQueriesAreTenantScoped` 通过；
- `TestMemoryAuditAndExportAreTenantScoped` 通过，legacy 空 tenant 事件按关联记录的 tenant fail closed；
- `TestMemoryImportIsTenantScoped` 通过，import 固定写入当前 tenant；跨 tenant 同 ID 导入 fail closed；
- migration v30 为 `memory_session_purges`/`memory_turn_capture_generations` 增加 tenant 身份；`TestMemorySessionPurgeIsTenantScoped` 通过；
- `TestMemoryWorkspaceRegistryKeepsRecordCorporaIsolated` 通过，两个 workspace 的 records corpus 不互见；
- `TestMemoryWorkspaceStatusKeepsDigestAndJobBacklogsIsolated` 通过，两个 workspace 的 digest/job backlog 不互见；
- `go test ./internal/memorystore -count=1` 在当前工作区通过（并发改动仍可能继续变化）；
- G2 收尾已补：status、job get/claim/reclaim/heartbeat/complete/fail/operator/deadline、correction/suppression、source invalidation、version/as-of/rollback、core/core usage、metrics 读取补 tenant；`TestMemoryG2*` 五组负向测试通过；
- 全仓 project_key 审计已分类：v1 主路径无未分类裸查询；无 tenant 列内部表与 v1.1/G4/G5 冻结路径列入 `22` §5.3 豁免；
- `Mode=off` 独立证据已补：runtime 零模型调用、HTTP 零建库、CLI 零建库（`22` §6）；
- `TestMemoryV30UpgradePreservesLegacyPurgeAndCaptureIdentity` 通过：v29 legacy 行升级后保留 `tenant_id=''`，同 project/session 与同 receipt 可由另一 tenant 共存；
- G2 按 v1 project scope 代码级收尾；提交与隔离复现后才算发布关闭；同 project 的 multi-tenant same-project 扩展不纳入 v1。
- G0-5 副本 recovery：schema v29，replay candidates 116（already_queued 9 / missing_receipt 17 / not_orphaned 90 / eligible 0），deadline repair 8/8 applied on copy；未 replay、未迁移真实库。

本文写作时观测到：

```text
HEAD = b5c4537e
git status --short = 164 项
docs/agent-memory/16-ai-memory-engine-implementation-plan-20261122.md 有未提交修改
```

这些只是写作时快照，不是发布基线。P0 必须重新记录，不得假设工作区干净。

### 8.2 首批动作（按顺序）

1. **建立隔离工作区**：从当前可构建提交创建独立 worktree；不要把 team/workspace/supervision/前端并发改动混入 memory release。
2. **执行 P0 基线**：记录 `mode=off` 行为、memory 包测试、API/CLI/tool 定向结果；如果 API 被无关的 `internal/supervision` 签名漂移阻塞，记录为独立 blocker，不得把旧通过结果当作当前通过。
3. **冻结 release profile 和证据模板**：先确定 project-only、notes 注入、一个 extraction job、无 G4/G5。
4. **执行 P1**：手工闭环不通过，不进入 P2。
5. **执行 P2**：自动闭环不通过，不进入 P3。
6. **执行 P3**：dogfood、负向测试、恢复演练、文档和签字；通过后发布 v1。
7. **停止**：发布后不继续把 G4/G5 塞进同一实施线；新能力另开 RFC。

### 8.3 当前最先不要做的事

- 不要先补 G4 Dreaming/Patch；
- 不要先补 G5 Entity/RRF/Vector；
- 不要先扩展 user/profile/tenant/agent/task scope；
- 不要先做新的 Web 管理平台；
- 不要为了“结构完整”新增 migration v27+；
- 不要把 `memory_core` 变成第二个事实源；
- 不要把 `phase2_consolidate succeeded` 当作写入闭环证据；
- 不要用局部包测试、空 SQLite fixture 或一次合成 E2E 代替发布证据。

---

## 9. 停止规则与闭环声明

### 9.1 停止规则

在 P0-P3 期间：

1. 任何新需求先分类为：v1 gate 阻断项、已知风险、v1.1+、丢弃。
2. 只有 v1 gate 阻断项可以进入当前实现；其余进入 backlog。
3. 不新增 scope、retrieval channel、memory type、Dreaming/Patch 表、Core 功能或运营 API。
4. 不新增 schema migration；如必须新增，只能是为关闭已有 gate 的最小修复，并单独评审/回滚。
5. 每个阶段最多一个 in-progress 工作包；阶段退出必须有证据。
6. 不把“代码存在”“局部测试通过”“演示成功一次”写成“闭环”。
7. P3 发布后，当前实施线关闭；后续能力不以“持续实施”方式继续。

### 9.2 何时可以宣布闭环

只有同时满足以下条件，才可以对外说：

> **Project Memory v1 已闭环并可交付。**

1. Gate 1–5 全部通过；
2. 在隔离 commit/worktree 上可复现；
3. 有 HTTP/CLI/tool 同一 corpus 证据；
4. 有自动 capture → candidate → active → 下一会话注入证据；
5. 有 delete/correct/rollback/retry/restart 证据；
6. 有跨 project/secret/未授权/DB 不可用负向证据；
7. 真实或复制的 dogfood 库完成受控恢复，未关闭项不含主链路阻断；
8. `19`/`README` 更新为“v1 可交付”，并链接 release evidence；
9. 明确声明：这不是“参考设计全部实现”，v2/v3/v4 仍待独立立项。

### 9.3 不应作为闭环证据

- 只有 `memory_entries` 表和接口，没有 injection/citation；
- 只有临时 SQLite fixture，没有 workspace/restart/backlog；
- 只有 summary 文本，没有 source/items/gate；
- 只有 `phase2_consolidate` 成功，没有 candidate/active 增量；
- 只有单端 API 通过，没有 CLI/tool 同 corpus；
- 只有“没有报错”，没有负向测试；
- 只有 0 条记录的空库；
- 只有旧提交/旧 worktree 的通过结果；
- 只有参考设计 Phase 1–5 的表格，没有 release gate。

---

## 附录 A：最小验证命令

在 `backend/` 目录、隔离 worktree 中执行；命令结果必须带退出码。

```bash
cd backend

go test ./internal/memorystore ./internal/memoryruntime \
  ./internal/memoryassembly ./internal/config -count=1

go test ./internal/api/runtimeapi -run 'TestMemory' -count=1

go test ./cmd/aicli/commands \
  -run 'TestMemory|TestStructuredMemory|TestChatMemory' -count=1

go test ./internal/toolkit/tools -run 'TestMemory' -count=1

go test ./internal/memorystore \
  -run 'TestSearchRecords|TestMemoryFTS|TestMemoryScope|TestRecordVersion|TestRecordRollback' \
  -count=1
```

发布前再执行：

```bash
go test ./internal/memorystore ./internal/memoryruntime \
  ./internal/memoryassembly ./internal/api/runtimeapi \
  ./cmd/aicli/commands ./internal/toolkit/tools -count=1

git diff --check
```

如果某个包被非 memory 并发改动阻塞：

- 记录包名、错误、当前 HEAD、是否可复现；
- 不要用旧结果替代；
- 不要把“阻塞”写成“通过”；
- 在单独提交/单独 worktree 修复后再纳入证据。

## 附录 B：Release Evidence 模板

P3 创建 `25-project-memory-v1-release-evidence-YYYYMMDD.md`，至少包含：

```text
1. release commit / worktree
2. release profile 与配置快照
3. mode=off 基线 diff
4. Gate 1–5 结果表
5. HTTP/CLI/tool 同 corpus 输出
6. 自动 capture -> active -> next-session injection 证据
7. delete/correct/rollback/retry/restart 证据
8. scope/secret/未授权/DB 不可用负向结果
9. dogfood 备份、dry-run、恢复前后计数
10. 指标原始数据位置
11. 未关闭问题与非阻断分类
12. owner 签字与发布结论
```

## 附录 C：Draft backlog（不进入当前实施）

```text
- v1.1: summary/core as materialized view
- v1.1: retention / purge / handoff / tiered brief
- v1.1: user scope exploratory RFC
- v2: semantic + entity + temporal + RRF + vector
- v3: Episode + Dreaming + Patch + reflective learning
- v4: adaptive memory + cross-project profile
```

这些条目只是记录，不是当前承诺，也不创建新的 G 系列阶段。

---

## 结论

当前项目已经有可运行的 Memory v2 代码基础，但“代码基础”不等于“完整产品闭环”。本计划的目的不是再列一遍 G0-G5 缺口，而是：

1. 把参考设计收敛为 **Project Memory v1**；
2. 把 v1 限定为一条可验证的 project-scope 主链路；
3. 用 P0-P3 和五个 release gate 决定何时发布；
4. 把 Dreaming、Episode、Hybrid Retrieval、Entity/Relation、Vector、独立 Core、跨项目 profile 等全部移出当前 release；
5. 在 P3 通过后停止当前实施线，后续能力另开 RFC。

在 Gate 1–5 全部通过之前，正确结论仍是：**当前不能宣布 Agent Memory 整体闭环。**
