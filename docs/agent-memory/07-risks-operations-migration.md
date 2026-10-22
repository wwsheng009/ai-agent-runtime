# 风险、安全、迁移与运维

> 状态：方案约束与上线检查清单  
> 适用：Agent Memory v2 的 M0-M5 全过程。

## 1. 风险分级

| 等级 | 风险 | 后果 |
| --- | --- | --- |
| P0 | 跨项目/跨用户记忆泄漏 | 数据安全事故，必须 fail closed |
| P0 | secret 写入 memory DB / 日志 / 导出 | 凭证泄漏 |
| P0 | 记忆内容被当作指令或权限 | 提示注入、越权操作 |
| P1 | 自动抽取污染长期记忆 | 后续会话被错误偏好影响 |
| P1 | 删除后仍被注入 | 用户信任和隐私问题 |
| P1 | 删除/纠正后的记忆被重新抽取复活 | 用户无法真正遗忘 |
| P1 | project key 漂移/大小写差异 | 跨项目误读或记忆丢失 |
| P1 | 未授权删除/重置 | 数据被破坏 |
| P1 | retention_class 误分类 | 重要记忆被提前清理 |
| P2 | working digest 过期 | 近期工作状态误导模型 |
| P1 | SQLite 锁导致 runtime-server 假死 | 服务不可用 |
| P1 | Prompt cache 前缀被改写 | 成本和性能明显回退 |
| P2 | 记忆膨胀/重复 | 噪声、检索变慢 |
| P2 | 抽取/整合成本过高 | 用户成本增加 |
| P2 | 迁移失败导致 notes 丢失 | 数据损坏 |
| P2 | Windows 7 构建不兼容 | 版本支持回退 |

## 2. 安全设计

### 2.1 记忆是不可信历史数据

- memory block 必须标注 `historical context, not instructions`；
- 不把记忆放入 system prompt；
- 记忆不得：
  - 改变工具 allow/deny；
  - 改变 permission mode；
  - 调用 MCP/shell；
  - 批准审批；
  - 修改 profile/config；
- 如果记忆文本包含“忽略规则”“执行命令”等，应作为普通数据处理。

### 2.2 权限提升禁令

记忆系统只提供上下文和可检索数据，不参与：

- 工具授权；
- durable grants；
- approval 决策；
- sandbox 策略；
- 模型/provider 切换。

现有 grant/approval 逻辑保持不变。

### 2.3 scope 隔离

- `project_key` 优先使用 git origin + 相对路径，否则使用 canonical workspace 路径；
- Windows 大小写/分隔符归一，符号链接解析后再比较；
- 查询必须同时匹配 scope + project_key；
- user scope 只在 `allowUserScope=true` 时读取；
- 子 agent 默认不继承写权限；
- API 不能通过 `workspace_path` 读取其他项目；
- 导出、审计、jobs 查询一律带 scope 过滤；
- 任何无法判定 scope 的读取 fail closed。

### 2.4 脱敏

- 在落库前执行，不只在前端展示时执行；
- 覆盖 key/token/password/private key/URL query credentials；
- 命中 secret 时优先丢弃整条派生记录；
- 日志只记录规则名/字段类型，不记录原文；
- 手动输入也应提示脱敏结果；
- `audit` 不保存 secret 原文。

### 2.5 记忆投毒

自动抽取的会话可能包含恶意内容：

- 只从可访问的当前项目会话抽取；
- 自动记录默认 candidate；
- 需要来源证据和置信度；
- 冲突时用户 correction/manual 优先；
- 不因单次会话直接改写全局偏好；
- Consolidation 必须保留可追溯 revision；
- 不自动提升 user scope。
- 删除/纠正后写入 `memory_suppressions`，抽取和 Consolidation 必须跳过；只有显式 restore 才可恢复。

### 2.6 手工记忆与 v2 模式隔离

- `memory.mode=off` 不等于关闭既有 `/memory` 手工能力；
- 手工 notes 由 `manual.enabled` 和现有 memorystore 兼容面管理；
- 手工 notes 不因 v2 开关切换被删除；
- 自动记录不得静默覆盖 manual 记录；
- v2 自动化和手工操作必须在 UI 上分别标识，避免用户误以为关闭了生成就等于删除已有记忆。

## 3. 技术风险与缓解

### 3.1 SQLite 锁与 WAL

风险：

- 项目已经出现过 `session_history.sqlite` 共享导致的 busy/lock 和 health 死锁；
- 记忆抽取/Consolidation 若与聊天共用连接或长事务，会放大问题。

缓解：

- memory.db 独立文件，不复用 `session_history.sqlite`；
- 使用 `internal/sqliteutil` 的 WAL + busy_timeout + 单连接基线；
- 写事务短小，不在事务内做 LLM 调用；
- 读路径使用短查询/只读快照；
- Phase 2 单写者锁，lease 带超时；
- health 检查不得无界等待 memory DB；
- Windows 7 走同一驱动基线，新增测试。

### 3.2 Prompt cache

风险：

- 记忆插入 system/profile/history 前面会破坏 provider prompt cache；
- 每轮 summary 变化导致前缀抖动；
- 重复注入产生多个 memory block。

缓解：

- 保留 `contextmgr` 的动态尾部；
- stage 名保持 `project_memory`；
- 每个逻辑 turn 只注入一次；
- summary 在新的 turn 边界切换；
- `activeTurnHasReplay` 时抑制；
- 加前缀稳定性测试和 `[llm-debug]` 观测。

### 3.2.1 模式与依赖矩阵

实现前必须固化：

- `off/shadow/on` 对抽取、Consolidation、注入、工具、手工 notes 的逐项行为；
- `summary` 模式下只读工具是否开启；
- `hybrid` 的 summary/records token 拆分；
- session mode 的默认值和持久化位置；
- 工具关闭时 summary 必须自足。

任何“看起来开着但实际无检索/无删除”的组合都必须在状态面警告。

### 3.3 上下文膨胀

风险：

- 自动记忆过多、摘要过长、每条会话都注入。

缓解：

- summary 默认 600-800 tokens；
- maxItems、maxRecords 有硬上限；
- 默认 summary-first，细节按需读取；
- 低置信度不注入；
- 过期/未使用淘汰；
- 禁止把完整 transcript 注入。

### 3.4 模型输出不稳定

风险：

- 抽取返回 markdown/自然语言，破坏 JSON 解析；
- 模型编造来源或偏好。

缓解：

- 严格 JSON schema；
- 校验失败重试有限次；
- 无证据不落库；
- 候选和 active 分离；
- 确定性 fallback；
- 抽取结果按 confidence 门槛过滤；
- 质量评测使用真实样本，不靠 prompt 直觉。

### 3.5 成本与延迟

风险：

- 每次会话结束调用大模型；
- Consolidation 频繁触发；
- 读路径同步调用模型。

缓解：

- 异步 job；
- idle/session-end 批处理；
- 每轮会话数/记录数上限；
- 低 reasoning；
- 读路径零模型调用；
- Consolidation 有节流和 watermark；
- 可通过 provider/model capability 配置内部模型。

### 3.6 记忆质量退化

风险：

- 错误偏好被长期放大；
- 旧决策覆盖新决策；
- 自引用造成回音室；
- source 已变化但记忆仍被当作当前事实；
- 旧 summary 与当前记录集不一致。

缓解：

- manual/correction 优先；
- source evidence 和 confidence；
- use_count 衰减；
- 可选 retention 和人工 review；
- 禁止自动把 assistant 建议直接当 preference；
- 定期抽样评估；
- 支持“为什么记住这条”的 `why` 视图；
- freshness_status/TTL/source hash 校验；
- summary input watermark + items freshness 门禁；
- 高风险事实只作为 pointer，回答前回源读取。

## 4. 迁移方案

### 4.1 迁移对象

- `<workspace>/.aicli/memory/notes.jsonl`
- profile memory 目录（只读引用，v1 不迁移）
- 旧 `.aicli` 本地数据

### 4.2 迁移步骤

```text
1. mode=off，备份 notes.jsonl
2. 创建 memory.db/schema
3. 逐行读取 notes.jsonl
4. 规范化 text/tags/source/timestamps
5. content hash 去重
6. 插入 manual active records
7. 记录迁移批次和跳过/损坏计数
8. 保留 notes.jsonl，不自动删除
9. 读路径优先 SQLite；失败时回退只读 JSONL
10. 运行 M1 验收
```

### 4.3 迁移要求

- 幂等：重复执行不复制记录；
- 可观测：记录文件行数、成功数、损坏数、跳过数；
- 可回滚：原始 notes.jsonl 保留；
- 不改变 ID 语义？如果旧 ID 可用则保留，否则生成新 ID 并在 metadata 记录旧 ID；
- 不把旧 note 的 `session_id` 丢失；
- 不把损坏行静默丢弃而不计数。

### 4.4 降级

- 新代码可读旧 schema：fail closed 或只读；
- 旧代码不应读取新 DB 并在不知 schema 的情况下写入；
- 不允许自动删除新表来“回滚”；
- 回滚到旧版本时，至少保留 notes.jsonl 和 memory.db 原文件；
- 升级/降级都要有 smoke test。

## 5. 运维

### 5.1 状态检查

提供：

```text
/memory status
/api/runtime/memory/status
/memory jobs
```

显示：

- mode；
- scope；
- DB path；
- schema version；
- active/candidate/superseded/deleted 数量；
- fresh/stale/unverified 数量；
- summary revision / generated_at；
- summary freshness / input watermark 是否匹配；
- 最近一次 freshness sweep / revalidation；
- 最近一次 recall 的候选/过滤/选中数、token 预算与 trace id；
- 最近一次 extraction/consolidation；
- 队列长度、失败数、lease；
- 最近错误（已脱敏）。

### 5.2 日志

建议日志字段：

- event/type；
- scope/project hash；
- job id；
- session id（可哈希）；
- record count；
- error class；
- redaction rule；
- duration。

禁止：

- 原文 secret；
- 完整记忆文本（默认只记长度/id）；
- 其他项目路径。

### 5.3 指标与告警

- job 连续失败；
- lease 长时间不释放；
- DB 大小超阈值；
- summary revision 长时间不更新；
- 注入 token 超预算；
- cross-scope denied 异常升高；
- 用户删除/纠正比例异常升高；
- suppression 命中后仍被重新创建；
- stale summary 被注入；
- stale 记录比例异常升高；
- source changed 后长期未失效；
- freshness sweep 连续失败；
- unauthorized destructive API 调用；
- manual notes 被自动记录覆盖；
- memory DB busy 超时。

### 5.3.1 Recall trace 与 token 观测

- 采样记录 `memory_recall_traces`：过滤原因、排序、预算、丢弃项；
- 禁止保存完整 query/记忆正文，只保留 hash 和脱敏 preview；
- 观测 `memory_injection_tokens` 与总 prompt 预算的比值；
- 当 memory budget 饱和时记录 `memory_budget_saturated`；
- `/memory why` 查看 provenance，`why-not` 查看预算/过滤原因；
- `why` 必须展示 freshness、last_verified_at、revalidate_after、stale_reason；
- 相关阈值和默认值见 `09-memory-horizons-recall-and-budget.md`。

### 5.3.2 Freshness sweep

- v1 先做读取时惰性校验 + 管理端手动 sweep；
- v1.1 再启用周期性 sweep；
- 每批受 `revalidateBatchSize` 限制，不阻塞对话；
- source 可访问且 hash/revision 未变：更新 `last_verified_at`，保持 `fresh`；
- source 已变化：标记 `stale`，写 `memory_events`，按配置入队重新抽取；
- source 不可访问：按 kind 标记 `stale` 或 `expired`，不得误标 `fresh`；
- sweep 失败必须可观察、可重试，不得把失败当作 fresh。

### 5.4 备份与导出

- 备份：关闭写入或使用 SQLite backup API，复制 `memory.db` + WAL；
- 导出：
  - JSONL（完整结构，便于迁移）；
  - Markdown（`memory_summary.md`、`MEMORY.md`）；
- 导入：仅支持明确 schema/版本，先校验、去重、再写入；
- 导出包含 freshness_status/last_verified_at/revalidate_after/stale_reason；
- 导出不包含 secret、跨 scope、被删除原文；
- 导出文件明确标记版本和生成时间。

### 5.5 重置

`/memory reset` 或 API：

- 二次确认；
- 保留源 session transcript；
- 删除/标记 memory records、summaries、jobs；
- 可选保留 audit；
- 重建空 store；
- 不能影响 grants、sessions、knowledge、artifacts。

## 6. 上线检查清单

- [ ] `mode=off` 下 v2 自动行为为零，手工 memory 与基线一致；
- [ ] 迁移幂等，notes 未丢失；
- [ ] project scope 隔离测试通过；
- [ ] user scope 默认关闭；
- [ ] secret/跨项目泄漏测试为 0；
- [ ] 删除后不再注入；
- [ ] 删除/纠正后不会被下次抽取复活；
- [ ] summary revision 可通过 `memory_summary_items` 重建；
- [ ] summary 注入前通过 input watermark + items freshness 门禁；
- [ ] stale/unverified 记录默认不注入；
- [ ] source hash/revision 变化能标记 stale；
- [ ] 高风险事实只作为 pointer，具备回源读取路径；
- [ ] freshness sweep/revalidate 有授权、审计和失败重试；
- [ ] retention_class 与 pinned 语义独立且经过淘汰测试；
- [ ] working digest（v1.1）不替代 long-term summary；
- [ ] project key 在 Windows 大小写/分隔符/符号链接下稳定；
- [ ] `off` 下手工 notes 与 v2 mode 语义清晰且不回归；
- [ ] destructive API 有管理员/本机授权；
- [ ] 注入只出现在 dynamic tail；
- [ ] memory budget 已按总 prompt 预算 clamp；
- [ ] recall trace 不含 secret/完整正文；
- [ ] 每 turn 最多一次 memory block；
- [ ] tool surface 不因 memory 变化；
- [ ] compact/resume 后不重复注入；
- [ ] SQLite 锁和 health 测试通过；
- [ ] Windows 7 受影响包测试通过；
- [ ] `/memory` 旧命令不回归；
- [ ] Web 面板不回归；
- [ ] shadow 指标已评审；
- [ ] 回滚演练完成；
- [ ] 运维/迁移/用户文档已更新。

### 6.1 v1.1 采纳前检查（不阻塞 v1）

- [ ] `memory_handoffs` 为独立表，claim-once 在单事务内完成，TTL 到期不再注入；
- [ ] handoff 只带 source refs，不复制完整会话正文，不携带工具授权；
- [ ] tiered brief 先扣安全框架/pinned/rules/footer，再按 L0/L1/L2 分配预算；
- [ ] 省略项只留下 path/id 指针，不静默丢弃；
- [ ] access-weighted retention 只影响淘汰/轻量排序，semantic/procedural 不自动衰减；
- [ ] RRF explain 记录 stream/rank/fusion，不保存完整 query 或记忆正文；
- [ ] cross-project profile 需要多项目证据或用户确认，forget 后不会复活；
- [ ] 上述能力均有独立开关、回滚路径和 v1 基线对照。

## 7. 回滚策略

| 层级 | 操作 |
| --- | --- |
| 注入 | 切 `injection.mode=notes` 或 `mode=off` |
| 生成 | `generation.enabled=false` |
| Consolidation | `consolidation.enabled=false` |
| Freshness sweep | `freshness.enabled=false`；保留已有 stale 标记 |
| 工具 | `tools.enabled=false` |
| 迁移 | 保留 notes.jsonl，不删除 DB |
| 代码 | 回退到 M1/M0；schema 只前滚 |
| 数据 | 从导出 JSONL/备份恢复 |

回滚时禁止：

- 自动删除用户 manual notes；
- 自动删除 audit；
- 自动执行 schema downgrade；
- 让旧进程写入未知的新 schema。

## 8. 开放问题与默认决策

### 8.1 M0 默认决策（按最佳实践确认，M0 转 ADR）

| # | 问题 | 建议默认 | 理由 |
| --- | --- | --- | --- |
| 1 | 默认注入模式 | M1 用 `notes`，M3 后切 `summary`；无检索工具时 `summary` 必须自足 | 先保持旧行为，再逐项目切换；避免 summary 依赖不存在的工具 |
| 2 | v1 是否提供 user scope | v1 仅 `project`；user scope 默认关闭 | 降低跨项目泄漏和迁移复杂度 |
| 3 | `memory_forget` 是否允许 Agent 调用 | 默认 `false`；仅用户命令/管理 API | 删除是高影响操作，不能由模型自行触发 |
| 4 | FTS5 在 Windows 7 不可用 | FTS5 始终可选，自动回退 LIKE | 不把平台能力当作功能门禁 |
| 5 | citation block 流式/导出 | 统一使用可剥离的 metadata 标记，前端/导出共用剥离器 | 避免用户复制到内部 ID 或渲染残片 |
| 6 | 抽取/Consolidation 模型 | 复用当前 provider，低 reasoning，允许配置覆盖 | 不引入第二套凭据和路由 |
| 7 | manual note 的冻结语义 | `retention_class=permanent`，是否 `pinned` 由用户显式选择 | 保留用户删除/编辑权，不把 manual 变成不可变 |
| 8 | 跨机器项目标识 | 优先规范化 git origin；无 origin 时回退 canonical path hash | 兼顾 clone 与本地项目 |
| 9 | 是否导入外部 `MEMORY.md` | v1 不做；后续只导入显式格式并标记 source | 避免不可信内容绕过抽取和脱敏 |
| 10 | memory 命中是否进 usage analytics | 进本地 usage，不默认上报远端 | 保留可观测性，同时控制隐私边界 |
| 11 | session mode 存储 | 独立表，按 session id 持久化，不塞 metadata | 需要跨 resume 保留并支持审计 |
| 12 | 无工具时是否允许 `summary` 默认 | 允许，但状态面标记 `no_retrieval_tools`，并限制预算 | 避免把“无工具”误判为不可用 |
| 13 | stale 记录如何处理 | 默认 `exclude`；per-kind TTL；高风险事实回源 | 不让已知过时信息静默注入 |

以上默认值作为 M0 实现基线，M0 必须转为 ADR/配置契约；若后续修改，必须同步更新本文档和测试。未落地前，代码实现仍必须保留开关和回退路径。

原始问题清单（与上表一一对应）：

1. 默认注入模式是 `summary` 还是 `hybrid`？
2. v1 是否提供 user scope，还是只做 project？
3. `memory_forget` 是否允许 Agent 工具调用？
4. FTS5 在 Windows 7 目标构建中的可用性？
5. citation block 如何在流式渲染、复制、导出中统一剥离？
6. extraction/consolidation 是否使用当前 provider 模型，还是独立轻量模型？
7. manual note 是否需要“永不自动合并/永不删除”标记？
8. 项目记忆是否需要跨机器导出/导入的稳定项目标识？
9. 是否允许从其他 Agent 的 MEMORY.md 导入（类似 Codex external memory import）？
10. 是否需要把 memory 命中纳入现有 usage analytics？
11. session memory mode 存 metadata 还是独立表？是否需要跨 resume 保留？
12. `summary` 模式在只读工具关闭时是否仍允许作为默认？

这些问题必须在 M0 或对应阶段开始前完成决策，不能由实现者临时猜测。

## 9. 结论

Agent Memory v2 的最大风险不是“模型不会总结”，而是：

- scope 穿透；
- secret 泄漏；
- 记忆污染；
- 注入位置破坏 prompt cache；
- 数据库锁影响运行时；
- 删除语义不可靠。

因此实施顺序必须是：**存储与隔离 → 可观测写入 → 摘要与引用 → UI/工具 → shadow 评估 → 渐进启用**。

闭环审查结果见 [08-completeness-review.md](./08-completeness-review.md)。
