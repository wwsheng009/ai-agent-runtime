# CHANGELOG — Code Knowledge Runtime（知识层）

> 本文件是 `docs/knowledge_Layer/` 的**变更历史唯一事实源**（见 [`README.md`](README.md) §3）。
> 变更流程见 [`README.md`](README.md) §6。
> 本目录在 2026-09-20 之前没有变更记录；下列为首批条目。

---

## 2026-10-01 — 收口轮（六）：跨任务（per-workspace）复用可达性修复 + M5 测量阻塞点定位

### Changed

- `knowledge/planner.go`：`storePlanner.Plan` 在**会话/任务工作集无可复用项**时回退到跨任务检索（04 §4.4 第三条路径 + C8「per-task 写入、per-workspace 复用」），按跨任务阈值评估（≥0.90 直接复用 / 0.50–0.90 待验证 + 强制验证）；失败/无命中保留原判定（Degrade-Not-Fail），会话命中时不改变既有语义（不多打一次 store）。
- 既有契约测试随新语义更新（`TestPlannerLookupShape`：首次查询形状不变 + 追加一次回退查询）；`fakePlanReader` 增加查询序列记录（additive）。

### Verified

- 5 例新测试（回退命中 / 会话命中不触发 / 低于硬下限保留原判定 / 回退失败静默 / 真实 store 端到端：旧会话记忆被新会话召回）+ `knowledge` 包 planner 用例全绿。
- 真机（新二进制，`%TEMP%\kb_closeout_e2e\ws`）：**全新会话** + 含路径查询（`internal/knowledge/planner.go …`）→ `context_snapshots` 新增 `injected=1, mode=signals, stale_filtered=0`（跨任务复用注入）；对照组：自然语言查询（目录前缀 + 符号名 + 中文）→ **零注入**。

### Notes（M5 测量阻塞点，登记）

- 跨任务回退的实际命中取决于 `explorationLookupKeys` 的键提取：查询为**纯路径/限定名**时可命中（exact > prefix > contains），但"目录前缀 + 符号名 + 中文"的典型 NL 任务键退化为无意义尾段（如"中查找"）→ 命中 ≈ 0；会话内复用（会话工作集，含 query 哈希节点）不受影响。
- 结论：§7.2 的"上下文 token / 任务 ↓ ≥ 25%"（真实 NL 任务集 A/B）在当前键提取下**仍不可测**——需先扩展键提取（或按 Phase 7 走 FTS/语义检索）再跑测量轮；本轮已把任务集采样（82 条真实任务）、筛选口径与 A/B 工作区（`%TEMP%\p6_ab\ws`，4127 文件，索引就绪）准备好，供下一轮直接复用。

---

## 2026-10-01 — 收口轮（五）：知识层"不可用"状态面统一 + `knowledge migrate`（P0 现场收尾）

### Changed

- `internal/knowledge`：新增 `UnavailableStatus`（"已配置但不可用"载荷：mode=配置值、enabled=false、degraded_reason 带 `index_unavailable` 前缀 + 原始原因）与 `LockRecordPID`（诊断口径：锁超龄时仍能读出占用者 pid；仲裁口径 `LockHolderPID` 语义不变）。
- `cmd/aicli`：`ChatSession.KnowledgeError` 记录接入失败原因（TUI/ACP 两条路径同口径）；`/web/api/knowledge/status` 在 Knowledge=nil 且原因非空时返回上述降级载荷，不再退化成 mode=off；`aicli knowledge status` 失败时同样打印该载荷 + `knowledge migrate` 指引。
- 新增 `aicli knowledge migrate`（`--workspace/--json/--no-index/--timeout`）：显式做一次 writer 打开（迁移 schema + 默认等待索引重建）；写锁被占时给出持有者 pid 与处置建议（不抢占）。

### Verified

- 单测：`internal/knowledge`（UnavailableStatus / LockRecordPID）+ `cmd/aicli/commands`（web 降级载荷、migrate off/owner 路径、CLI 契约）全绿。
- 真机（新二进制 `aicli-p0.exe`，工作区 `E:\projects\ai\ai-agent-runtime`）：
  - `knowledge status`：`mode on` + `degraded index_unavailable: … schema v3 …` + migrate 提示（此前为裸错误 + usage dump）；
  - `knowledge migrate`：`无法迁移：锁文件仍记录 pid 39272（锁已超龄但无法接管…）`（此前为无指引的原始错误）；
  - 全新工作区 `knowledge migrate --no-index`：owner + `schema v4` + 行数汇总，exit 0；
  - `/web/api/knowledge/status`：`mode=on, enabled=false, degraded_reason=index_unavailable: … schema v3 …`（此前 `mode=off` 最小载荷）。

### Notes

- 现场发现并登记：Windows 下锁文件被老进程打开时 `os.Remove` 因共享冲突失败 → 新二进制无法接管（降级 reader），且锁超 `maxLockAge=2h` 被判"陈旧"——`LockHolderPID` 返回 0 与"锁仍被占用"并存；指引文案改用 `LockRecordPID` 兜底。`maxLockAge` 与长会话寿命的关系留作后续观察项（本轮不改仲裁语义）。

---

## 2026-10-01 — 收口轮（四）：`codeParamInt` 字符串数字（登记项收尾）

### Changed

- `backend/internal/toolkit/tools/code_common.go`：`codeParamInt` 增 `case string`（`strconv.Atoi` + TrimSpace）——模型常把数字参数写成字符串（`"20"` / `" 12 "`），此前会**静默回落默认值**；小数/非法字符串仍回退（不引入浮点截断语义）。

### Verified

- 新增 `TestCodeParamIntAcceptsNumericStrings`（13 子例：int/int64/float64/json.Number/数字串/带空白/负数/非法/小数/空串/bool/缺键/nil）；`go test ./internal/toolkit/tools/ -count=1` 全绿（33.8s）。

### Notes

- 评审修复轮 2 的两项遗留（本项 + `code_inspect` view 去重提示）至此全部收尾；修复计划 §6/§7 清单已无未落地项。

---

## 2026-10-01 — 收口轮（三）：ADR-0004 陈旧索引下的 code.* 工具面落地（§7.1 结案）

### Added / Changed

- **信封三字段**（ADR §4.2/D3）：`code.*` 全部结果携带 `snapshot_ts`（unix 秒，0=无快照）、`staleness_seconds`（reader 实际值、writer 恒 0）、`completeness ∈ {full, partial, fallback}`；无 `omitempty`（任意模式都必须出现），metadata 同步三字段；`truncated` → `partial`，`source=fallback` → `fallback`。
- **分级注册**（ADR §4.1）：`CodeStaleFreshSeconds=60` / `CodeStaleMaxSeconds=900` 收敛为常量（D7）；`CodeTierForSnapshot`——writer 或逃生舱关闭分级 → `all`；reader 按 S 落 `all|definitions|none`（边界含；`snapshotTS<=0` 即从未成功索引 → `none`，fail closed）。staleness 来源 = `knowledge.Store.Stats.IndexedAt`（与 status.go 同源，向上取整防截断越档）；writer 判定复用 knowledge 写锁仲裁（新增只读 `knowledge.LockHolderPID`，不引入第二套判活口径）；`Stats` 读取失败按不可用处理（工具自身降级）。
- **动态切换（不重启会话）**：`tools.Manager.ListTools()` 前按最新陈旧度重评估并 `Registry.Register/Unregister` 切换分组；执行期再判一次（注册与执行之间的陈旧度漂移窗口）——关系类在非 `all` 档、定义类在 `none` 档 → 显式 `fallback(stale_index)`，绝不返回可能静默漏报的索引结果（D1）。
- **逃生舱**（ADR §4.4）：`knowledge.tools.stale_reader=off`（reader 也按 writer 策略注册）、`knowledge.tools.enabled=false`（索引照跑、工具面不注册）；配置校验与测试落地（`knowledge/config.go` + `config/manager.go`）。
- **描述变体**（ADR §4.3）：中等陈旧档定义类工具的 description 追加陈旧提示；JSON schema 恒定（D4，schema 对比测试）。

### Verified

- 新增测试：三档注册 / 信封三字段（含 fallback 与 truncated 映射）/ 两逃生舱 / 描述变体 / schema 恒定 / 动态切换 / 边界（60s、900s、无快照）；`go build ./...` 与 `./internal/tools/ ./internal/toolkit/tools/ ./internal/config/ ./internal/knowledge/` 全绿（子代理 worktree + 主仓集成两轮）。

### Notes

- **偏差登记**：① ADR 证据 1 假设的 `RegisterGroup` 不存在（`apply_patch_test.go` 里只是字符串夹具），改为复用 `tools/manager.go` 既有条件注册 + `Registry.Register/Unregister`；② ADR §4.1 的"heartbeat 变化后推送切换"未做事件推送，改为 `ListTools` 惰性重评估 + 执行期硬守卫（语义等价：切换在下一个工具面读取点生效）；③ `code_navigate` 含 references 方向，按**关系类**保守处理（中等陈旧不注册）。
- 集成说明：主仓 `tools/manager.go` 另有并行会话未提交改动（rg 别名），集成时保留其工作树改动，提交仅含 ADR-0004 变更。

---

## 2026-10-01 — 收口轮（续）：跨任务探索候选加权检索 + 限定名归一化（§6.3 结案）

### Changed

- `backend/internal/knowledge/store_sqlite_exploration_read.go`：跨任务路径（TaskID/SessionID 均为空）的 `Target` 从**精确匹配**改为**加权检索**（exact=0 > prefix=1 > contains=2，权重优先，其后沿用 `last_used_at DESC（NULL 在后）→ created_at DESC → id ASC`，SQL LIMIT 在排序后生效）；任务/会话路径保持 `n.target` 精确过滤与既有排序不变。
- 新增 `explorationLookupKeys` 归一化（规则全文注释在代码内）：全名与末段都参与匹配——`path#symbol`（三键）、路径（全名 + basename）、限定名（全名 + 末段点分名，`knowledge.Plan` → `Plan`）、裸名；末段在首个空白处截断（自然语言里嵌入符号名可命中）；末段命中代码扩展名时不提取；`LOWER()` 双侧折叠 + `escapeLike` 转义 `%`/`_`/`\`。
- `exploration.go` / `planner.go` / `store.go`：DTO/接口/Planner 注释对齐新语义；`EvaluatePlan` 阈值/版本/stale 口径零改动。
- **未新建 FTS 表**（复用 LIKE + CASE；避免迁移与新索引维护面）。

### Verified

- 新增 8 组行为测试（`store_sqlite_exploration_weight_test.go`：归一化键集合 / 三档权重压过时间 / 限定名与自然语言嵌入 / 路径符号与反斜杠 / 无命中与通配符转义 / 排序稳定与 Limit / 任务会话语义不变）+ Planner 真库端到端；`go test ./internal/knowledge/ -count=1` 全绿（子代理复跑 44.5s / 主仓集成复跑 33.3s）；`go build ./...` + `go vet` OK。
- 既有跨任务用例期望按新语义修正（`pkg/a.go` 现在同时精确命中文件节点、前缀命中 `pkg/a.go#Foo` 符号节点）。

### Notes

- 边界（登记）：长句只把**裸符号名**嵌在句中（无点号/路径可提取末段）仍不命中——属 token 化/FTS 桥接的后续空间；本轮按登记项范围（exact-name 加权 + 限定名）实现。

---

## 2026-10-01 — 收口轮：P0 引用索引漏报结案（根因 + builtin/4 索引侧修复）

### Findings（根因）

- 评审 P0「引用索引漏报（`EvaluatePlan` 生产调用点缺失）」**在当前代码不可复现**，三处实证：
  - 提取层：`planner.go:211/213` 的 return 行调用（复合字面量内 / 多值返回）被 builtin 提取器正确抽出（探针实测）；
  - 全链路：全新索引 + 3 轮增量重写后 `FindRefs` 全部绑定（`TestReferenceBindingStableAcrossIncrementalRewrites`）；
  - 生产库（`.aicli/knowledge/knowledge.db`，原始只读直查）：`planner.go:213 → EvaluatePlan`、`211 → classifyStoreError` 均已绑定。
- 结论：评审现场是**陈旧索引快照**（当时库尚未重建），其不可察觉正是 ADR-0004「陈旧度信号」缺失的后果——该分级属本轮收口另一笔欠账（进行中）。
- **仍活着的索引侧缺陷（本轮已修）**：接口方法声明行被误抽成 `kind=call` 引用——`planner.go:136`（`Planner` 接口）/ `142`（`ExplorationNodeReader` 接口）实证；这些"伪调用点"会污染 `code_callers`。

### Changed

- `backend/internal/knowledge/adapter_builtin.go`：新增 `interfaceMethodDeclPattern`（无关键字方法声明整行：Go/TS 接口体、抽象方法；要求 `name(params)` 后**必须有返回类型子句**——裸调用 `compute(x)` 不匹配），在调用抽取前整行跳过。
- `backend/internal/knowledge/version.go`：`AdapterVersion` `builtin/3 → builtin/4`（提取语义变化必须触发全量重建；同时让 builtin/3 写出的旧引用表自然失效——这是"陈旧索引现场"的根治手段）。

### Verified

- 新增 3 例回归（`refs_regression_test.go`）：接口声明不成为引用（Go/TS，含同名真调用正例）；return 行调用必抽出（复合字面量/多值返回）；增量重写 3 轮后绑定稳定 + 接口声明行不入引用表。
- `go test ./internal/knowledge/ -count=1` 全绿（含 golden set 精度/召回门槛）。

### Notes

- 遗留（登记）：Java/C# 风格的 `Type Name(...);` 抽象方法声明未纳入守卫——文本层无法与 `System.out.println(x);` 这类真调用可靠区分，贸然排除会掉召回；由 LSP/tree-sitter 语义通道承担。
- 评审 item 4 的 `Fatalf` 字符串误报已在 Phase 4 `insideStringOrComment` 修复（本轮复核仍成立）。

---

## 2026-10-01 — Phase 6 切片 7：验收门槛可复现化 + 验收报告（Phase 6 收口）

### Added

- `knowledge/acceptance_phase6_test.go`：G1/G2 硬门槛（表内 `context_items.stale=1` 行数 = 0、条目版本与快照版本不一致 = 0；注入候选混入 `#pending3`/空版本/低置信作反证）+ 快照写入时延（真库 n=50，**p50 540 µs / p95 563 µs**，门槛 50 ms）。
- `contextmgr/acceptance_phase6_test.go`：注入侧硬门槛（被过滤条目的 target 不出现在 prompt）+ off 可逆（与"无知识层"基线消息逐条一致、零 knowledge metadata）+ 对抗性内容（闭合标签恒 1 个、`</data` 中性化、属性引号转义）+ 真库 E2E（注入 → 快照落库 → 压缩只留计数/版本痕迹）。
- `knowledge.ContextSnapshotReader`：快照审计读接口（`*sqliteStore` 满足、reader 角色可用），供跨包复算 G1。
- `reports/phase6_context_compiler_report.md`：Phase 6 验收报告（04 §7.7 模板）——8 项门槛 7 项 Pass，收益 A/B 登记为待测量轮。

### Verified

- 6 例新门槛用例全绿；缓存 p95 门槛沿用切片 2 的真库实测（hit 0.54 ms / miss 1.07 ms）；全量回归（build + 6 包）绿。

### Notes

- **唯一未闭合项**：收益指标 A/B（token 下降 ≥ 25% / 任务成功率不降，任务集须来自真实 `usageledger` 采样）+ 端到端 p95 增幅 ≤ 10%；建议与 Phase 5 的真实会话 E2E 方法合并跑（见报告 §4.1/§4.4）。
- **Phase 6 切片计划收口**：原 8 片中，切片 5（防注入/信任/冲突接线）的实现已在切片 1/3/4 完成，其验证并入本切片——实际落地 7 片，全部完成。

---

## 2026-10-01 — Phase 6 切片 6：Observation Compressor 与 compactruntime 合并（knowledge 压缩模板）

### Changed

- `compactruntime/local.go`：
  - 新增 `isTransientCompactStage`（staged ∧ ¬durable ∧ ¬compaction）：knowledge / recall / workspace 这类**逐轮重生成**的瞬态注入，**不得**进入摘要输入、**不得**作为 retention 单元跨压缩保留——旧正文被写进摘要或原样留存后，会在知识版本漂移后成为 **stale 注入向量**（Phase 6 硬门槛 `stale_item_injected=0`）；
  - `compactionSummaryHistory` 两个 phase 都先剔除瞬态注入（保留 durable 阶段与 `compaction` 投影；mid-turn 原有的 staged-user 规则不变）；
  - `buildLocalRetentionUnits` 跳过瞬态注入（与既有的 compaction 投影跳过同构）；
  - `isDurableCompactStage` 增**禁止事项注释**：不得把 `knowledge` 加进 durable 集合（写明原因与替代方案）。
- `contextmgr/compact.go`（确定性摘要回退路径）：新增 **knowledge 压缩模板**——`knowledge` 阶段消息只产出一行有界痕迹（`N items / mode / version`，预算 400、最多 2 条），**绝不携带注入正文**；摘要中单列 "Knowledge injections (re-derived each turn; body not carried)" 小节。`isDurablePromptStage` 同步加禁止事项注释。
- `contextmgr/knowledge.go`：注入消息 metadata 增 `knowledge_version`（供压缩痕迹与转录审计）。
- 未新增压缩器：全部复用 `compactruntime` / `contextmgr` 既有压缩机制（04 §5 交付 3 / 06 §5.3）。

### Verified

- `compactruntime` 全包（2 例新测试：knowledge 非 durable 集合口径钉住；瞬态注入不进摘要输入/不进 retention 单元，durable 与压缩投影不受影响）+ `contextmgr` 全包（1 例新测试：摘要含计数/版本痕迹、不含正文、不进 durable 小节）绿。

### Notes

- **接受的行为取舍**：mid-turn 压缩后，本轮注入的 knowledge 块会从工作历史移除（不跨压缩携带），模型在本轮剩余部分依赖摘要痕迹；下一次 build 会按当前版本重新注入。这是"正确性优先于连续性"的选择——携带未重新校验的旧知识块会直接违反 `stale_item_injected=0`。

---

## 2026-10-01 — Phase 6 切片 5：context_snapshots / context_items 可解释性落库

### Added

- 迁移 `0004_context_items_explainability.sql`（additive）：`context_items` 增 `version` / `confidence` / `tier` / `provisional` / `explanation`——配合 0001 已有的 `source` / `trust` / `reason` / `stale`，满足「每个 item 有 source/version/trust/reason/stale」的验收要求。
- `knowledge/context_snapshot.go`：语义镜像 + 记录器——
  - **只记注入条目**：stale / 低置信 / 超预算 / 被覆盖只进 `budget_json` 的 dropped 计数；因此 `context_items.stale=1` 的行数就是 `stale_item_injected`（表内口径，04 §7.3），表里出现 stale=1 行即代表注入违规；
  - 确定性主键（内容哈希派生快照 id、`(snapshot, ref, item_type)` 派生条目 id）+ `ON CONFLICT DO NOTHING` → 重复记录同一次编译幂等；
  - `ContextRecorder`：Degrade-Not-Fail——无条目 / 无 store 静默跳过；真实失败返回 error 交调用方记 metadata；**reader 角色首次 `ErrReadOnlyStore` 后粘性停用**（不重试、不再产生失败噪声）。
- `knowledge/store_sqlite_context.go`：真库读写（单事务写快照+条目，不出现「有快照没条目」中间态；`ContextSnapshotsBySession` / `ContextItemsBySnapshot` 读路径 reader 可用）。
- contextmgr：`Manager.KnowledgeRecorder` + 注入成功后记录（metadata：`knowledge_snapshot_recorded` / `knowledge_snapshot_error` / `knowledge_version`）；版本观测提升到编译入口（缓存与快照共用一次观测）。
- agent：`attachKnowledgePlanner` 在 store 满足 `ContextSnapshotStore` 时装配记录器。

### Verified

- knowledge 全包（4 例新测试：映射确定性与四件套 / 记录器跳过·失败·只读粘性停用 / 真库往返 + 幂等 / reader 只读与会话隔离）绿；contextmgr 全包（3 例新测试：只记注入条目 / 失败降级 / off 零写入）绿。

### Notes

- 迁移编号：本切片新增的是 **0004**（0002 / 0003 已被 `file_soft_delete` / `workspace_adapter_version` 占用）。

---

## 2026-10-01 — Phase 6 切片 4：contextpack knowledge.Provider（只读视图）

### Added

- `contextpack/knowledge_provider.go`：只读知识层 provider（04 §5 文件落点）——
  - 契约：不写库 / 不记录探索 / 不触发索引；Planner=nil、空或短查询、Degraded 计划、无注入条目一律 `(nil, nil)`（pack 与未装配时逐字节一致）；
  - 结构化字段（items / dropped / tiers / stale_item_injected / tokens）供程序化消费与审计；Planner 错误经 Builder 变成 `_warnings`（可见不致命）；
  - `digest_block`：`RenderDataBlock` 产出的 data block（≤600 rune；超界退化为计数块——**绝不给半个块**），供任何 prompt 路径安全取用；
  - 预算 / 置信度下限与 contextmgr 同口径（`DefaultKnowledgePackBudget = 800`）。
- `contextpack/context_pack.go`：`Reduce` 增加 knowledge 归约（count / reason / digest）；digest **不截断**（截断会留下未闭合块，破坏 03 §14.5 包裹规则）。
- `knowledge/compiler.go`：tier 规则单一来源 `CompiledItemTier`（hot/warm + 0.90 阈值常量 `CompiledTierHotConfidence`）；contextmgr 的 `knowledgeTier` 改为委托（切片 3 代码收敛）。
- `handler.go`：`buildContextPack` 在 `knowledge.mode=on` 时装配 provider；`contextPackKnowledgeLayer` 门控——shadow 只保鲜索引（ModeShadow 契约）、off / 未激活零装配。

### Verified

- `internal/contextpack` 全包（5 例新测试：跳过条件 / 只读视图与 digest / 降级与错误 / 预算与下限 / Reduce 保块）绿；runtimeapi 门控用例（on / shadow / off 三态）绿。

### Notes

- 发现 `internal/contextpack/contextpack/` 是**无人引用的重复副本**（与 `internal/contextpack` 同名同内容，全仓无 import）；本切片未改动，建议单独清理（避免后续误改副本）。

---

## 2026-10-01 — Phase 6 切片 3：contextmgr 接线（编译 + data block + compile 缓存 + tier 映射）

### Changed

- `contextmgr/knowledge.go`：知识层注入改走 Context Compiler——
  - 注入前统一 `knowledge.CompilePlan`（信任/冲突/stale/置信度下限/token 预算/可解释性）；本地 `knowledgeItemStale` 删除，收敛到 `knowledge.IsReuseItemStale` 规范判据；
  - 渲染统一为 data block（03 §14.5 规则 2/4）：broad = 每条目一块（块头给来源/版本/理由，块体给 target/confidence/verify + 摘要）；signals = 单个 digest 块（措辞不变，仍是「只给信号、不给明细」）；
  - `Manager.KnowledgeCache`（新字段）：Planner 可提供版本观测（`*knowledge.Layer`）时走 compile 层缓存，命中不重编译；不可观测/观测失败/缓存故障一律降级直算；
  - 条目按 tier 映射 hot/warm/cold（04 §5 交付 2）：高置信直接复用 → hot；Verify/Provisional → warm；未注入（stale/低置信/超预算/被覆盖）→ cold（仅审计）；`knowledge_items` 增加 trust/tier/tokens，新增 `knowledge_budget_filtered` / `knowledge_overridden_filtered` / `knowledge_cache_hit` 元数据；
  - off 档零调用零注入不变（既有 off 可逆测试 + 新缓存场景下继续钉住）。
- `contextmgr/manager.go`：`LayerPlan` 增加 `knowledge` 层（name/description/sources/max_tokens/mode），描述 tier 映射。
- `agent/agent.go`：`attachKnowledgePlanner` 在 `*knowledge.Layer` 的 store 满足 `CompileCacheStore` 时装配 `KnowledgeCache`（reader 角色只读会被降级直算）。
- `knowledge/compiler.go`：`DefaultCompileItemOverhead` 24 → 320——预算必须覆盖**渲染后**的 data block（块头 + 块体脚手架），否则整条消息可超预算（切片 3 实测暴露：969 > 800）。
- 测试：`TestKnowledgeBroadInjectionShapeAndEvents` 断言更新为 data block 格式；`knowledge_calibration_test.go` 校准路径改用编译渲染（预算不变量对**新生产格式**成立）；新增 3 例（tier/信任元数据、缓存命中与降级、LayerPlan knowledge 层 + 缓存下 off 可逆）。

### Verified

- `internal/contextmgr` + `internal/agent` 全包绿；`go build ./...` 绿。
- 预算不变量：典型上界条目块 ≤ 800（`TestKnowledgeBroadBudgetCoversTypicalUpperBoundLine`，新生产格式）。

---

## 2026-10-01 — Phase 6 切片 2：compile 层缓存（`cache_entries`）

### Added

- `knowledge/cache.go`：
  - `CompileCacheStore` 窄接口（Get / Put / Delete / PurgeExpired 四方法；`*sqliteStore` 直接满足，未实现该接口的 Store 假体自动退化为「无缓存」）；
  - `CompileCacheKey`：确定性 sha256 键 = workspace + 计划输入（task/session/query/scope/write）+ 编译策略（mode/budget/floor）+ **知识版本** + **编译器版本**（`CompileCacheVersion`；编译语义变化时递增即全量失效）；
  - `CompileCache.Do`：命中直接返回（`CacheHit=true`）；未命中执行编译并回填；**任何缓存故障（读/写失败、载荷损坏、版本不符、过期、reader 角色只读）都降级为直算**（Degrade-Not-Fail）并计入 Errors；版本不符/过期条目尽力清理；
  - `CompileCacheMetrics`：命中/未命中/错误计数 + 命中率 + 两侧时延 p50/p95（有界样本 512，最近邻取法）。
- `knowledge/store_sqlite_cache.go`：`cache_entries` 的 SQLite 读写——`GetCacheEntry` / `PutCacheEntry`（ON CONFLICT upsert）/ `DeleteCacheEntry`（幂等）/ `PurgeExpiredCacheEntries`（NULL 过期 = 永不过期）；写路径走 `execWrite`，reader 角色硬失败 `ErrReadOnlyStore`。
- `knowledge/cache_test.go`：8 例测试（键确定性与 11 维敏感 / 命中不重编译 / 四类故障降级 / 版本与过期守卫 / 分位口径 / 真库往返（upsert·删除幂等·过期清理·reader 只读拒绝）/ 门槛复现 / 校验边界）。

### Verified

- **门槛复现**（04 §5 Phase 6；真库 + 真编译，200 次）：`hits=199 misses=1`，**hit p95 = 0.54ms**（门槛 < 50ms）、**miss p95 = 1.07ms**（门槛 < 200ms）、hit_rate = 0.995（04 §7.2 要求 compile 层 ≥ 50%）。
- `internal/knowledge` 全包 + `go build ./...` 全绿。

### Notes

- 缓存键含知识版本：Phase 5 的 `#pendingN` 未稳定 token 会自然改变键——索引落后期间不会复用旧载荷（与 fail-closed 口径一致）。
- TTL 默认 15 分钟仅兜底回收（正确性由版本键承担）；`PurgeExpiredCacheEntries` 待切片 8 接入 GC 周期。

---

## 2026-10-01 — Phase 6 切片 1：Context Compiler 编译语义内核（`knowledge/compiler.go`）

### Added

- `knowledge/compiler.go`：Phase 6 的语义内核（纯函数、无 IO、可复算）——
  - 信任等级闭集（03 §14.3：SYSTEM/TRUSTED_TOOL/CODE_INTELLIGENCE/UNTRUSTED_TOOL/USER_CONTENT/CODE_COMMENT/GENERATED）+ `context_items.trust` 落库映射（high|medium|low|untrusted；未知等级 fail closed 到 untrusted）+ `Injectable()`（不可信工具输出永不注入）；
  - 来源闭集与冲突优先级（03 §14.4；权重直接复用 04 §4.4 `SourceWeight` 闭集）+ `ResolveConflicts`（同目标同类型只保留最高优先级，其余进 dropped 且原因可查；独立通道 memory/artifact/fact 不参与该序）；
  - `CompilePlan`：复用项 → `CompiledItem`（item_type/ref_id/source/trust/version/confidence/reason/stale/tokens/explanation，即 `context_items` 的语义镜像）；stale（空版本 / `#pendingN` / 版本不匹配 / 版本未知）绝不进入可注入集合；置信度下限过滤；预算截断按确定性排序（confidence↓ → tokens↑ → ref_id↑）；
  - `RenderDataBlock`：03 §14.5 规则 2/4 的 data block 包裹（来源 + 版本入块头；内容中的 `</data` 中性化；属性转义）；
  - `IsReuseItemStale`：复用项 stale 的**规范判据**（与 contextmgr 注入前第二道防线同语义；切片 3 收敛到同一实现）。
- `knowledge/compiler_test.go`：8 例测试（信任映射 / 优先级序 / stale 判据 / 过滤与丢弃原因 / 可解释性字段 / 预算确定性 / 冲突解决 / data block 与转义）。

### Notes

- **文档不一致登记**（以 04 为准）：supplement 14 §14.4 的冲突序写作「LSP > 适配器 > Tree-sitter > FTS5 > Regex」，而 04 §4.4 的 `SourceWeight` 闭集里 Regex(0.55) > FTS(0.40)。本实现按 04（落地口径事实源）执行，`compiler.go` 头注已说明。
- Phase 6 切片计划（8 片）：①编译内核（本切片）②compile 层缓存（`cache_entries`；命中 p95 < 50ms / 未命中 < 200ms）③`contextmgr` LayerPlan knowledge 层 + 注入接线（off 可逆）④`contextpack` `knowledge.Provider`（只读）⑤防注入 / 信任等级 / 冲突解决接线 ⑥`context_snapshots`/`items` 写入与可解释性 ⑦Observation Compressor 复用 `compactruntime` ⑧验收门槛可复现化 + E2E。

---

## 2026-10-01 — watcher 删除事件缺口修复 + `watch=on` 真机时延验证（登记③ 收口）

### Fixed

- **fsnotify 源漏掉文件删除**（`change_watch.go`）：事件白名单是 `Write|Create|Rename`，`Remove` 被排除——文件删除永远不入队，删除传播只能等到下一个 turn 边界（git/scan 校正源），与 `watch=on` 的「一次事件」承诺不符。下游本就支持（`IndexPaths` 对「磁盘已不存在」的路径走软删除，见 `TestIndexPathsSoftDeletesMissingFile`），修复即把 `Remove` 纳入入队集合；目录删除由后缀预筛挡掉，`s.dirs` 记账保持原语义（文件路径不在其中，是空操作）。
- 新增回归用例 `TestWatchSourceSoftDeletesExternalRemovalWhileIdle`（watcher 用例 4 → 5）。

### Verified

- 真机（新二进制 + 临时工作区 + `watch:on`，**会话全程零 turn**）：外部新建 372ms / 修改 348ms / **删除 392ms** 被事件路径吸收（`last_job.kind=incremental`）。修复前对照：删除 15s 超时（连续两次、不同文件），修复后 392ms。
- 判据说明：软删除**不推进** `indexed_at`（该字段取活跃文件的最新索引时间），删除场景应以 `last_job.id` / `files` 计数为准。
- `internal/knowledge` 全包 + `go build ./...` 全绿。

### Notes

- 登记③ 收口：`watch=on` 的「外部变更 → 事件路径」时延已在真实进程验证（新建/修改/删除同口径）；「下一个 turn 直接命中索引」沿用既有 E2E 口径，未再跑 LLM 轮次。
- 新观测（登记④，**非缺陷**，保守设计的代价）：owner 被**强杀**后，同工作区新进程若落在「锁不可删 / 持有者 pid 判活」窗口，会降级为 reader 并在**整个进程生命周期内不再重试接管**（`acquireOwnership` 仅在 `Layer.Open` 调用一次）；`degraded_reason` 显式标注 `read-only: store is owned by pid N`。恢复路径：重启进程，或等 `maxLockAge`（2h）后由下一进程接管。建议 Phase 6 评估「reader 退避重试接管」。

---

## 2026-10-01 — Phase 5 登记① 收口：知识层状态端点（`/web/api/knowledge`）

### Added

- `GET /web/api/knowledge[/status]`（knowledge.status.v1）：聊天会话 web 面首次暴露知识层状态快照。数据源与 `aicli knowledge status`、runtime-server `GET /api/runtime/knowledge/status` **同源同形**（直接返回同一 `knowledge.StatusReport`：mode/role/owner_pid/watch/gc/lock_wait/last_job/staleness/degraded_reason 等）；mode=off / 无会话返回 200 + mode=off 最小载荷（不是 404）；未知子路径 404、非 GET 405；只读——不触发索引、不写库，owner 与 reader 行为一致。
- 接线：`web_schema.go`（路径常量）+ `pprof.go`（路由注册，含 `/` 变体）+ `chat_debug_endpoints.go`（清单登记，只认清单的脚本可发现）。

### Verified

- 6 例新测试（mode=off / 真实 Layer / 裸路径等价 / 无会话 / 405 / 404）+ `cmd/aicli/commands` 全绿 + `go build ./...` OK。
- 真实进程实测（新二进制 + 临时工作区 + `watch: on`）：`mode=on`、`role=owner`、**`watch.active=true, dirs=1`**、`files/symbols/refs` 与 `last_job(kind=light,status=done)` 齐全、`degraded_reason` 空；裸路径等价；405/404 正确；`/debug/endpoints` 可发现。
- reader 降级实测（同工作区第二进程）：`role=reader` + `owner_pid` 指向 owner + 同一索引读数（`files=1/symbols=1/refs=2`）+ `staleness_ms` 可见。
- 关闭 Phase 5 真实会话 E2E 报告登记①（知识层状态面不可读）；登记③的「watch 状态可观测」随之落地（`watch=on` 时 `active=true` 可直接读）。

### Notes

- 仍遗留：`watch=on` 的**端到端时延压缩**（外部变更从"一个 turn"变为"一次事件"）未在真实会话验证（需以 `watch: on` 重启会话）；on-mode A/B（≥20 任务）与端到端 p95 统计门槛仍待测量轮。
- 登记②（`/web/api/turn` usage 在 reasoning 回合读数不自洽）仍未动。

---

## 2026-10-01 — Phase 5 真实会话远程 E2E（交付 1/3 的生产路径验证）

### Verified

- 会话 `session_20261001102354_qsfZLi0z`（runtime-server `:49747`；`knowledge.mode=on` + `code_tools=on`，`watch` 默认 off；真实仓库 5142 文件 / 5.5 万符号，同期另有 4 个会话在改同一工作区）经 `POST /web/api/invoke` 驱动 7 轮；**三方证据逐轮对齐**（模型自述 ↔ 工具结果信封 `session_history.sqlite` ↔ 库内状态 `knowledge.db` 只读查询）：
  - A1 只读工具面 `source=index`（`code_search` / `code_inspect`；`ObserveVersion` 行 271–298）；
  - A2 **edit hook 端到端**：模型 `write` 落盘 → 索引自动 +1 文件/符号（无需显式 reindex）→ `code_search.source=index`；
  - A3 **判定点校正（变更源 2）**：会话外写盘 → 首查 `source=fallback`（`degraded=true` + `reason=no_index_hit`，grep 兜底仍给出正确路径/行号）→ turn 结束后入库 → 同查询翻转为 `source=index`；
  - A4 删除传播：删后首查 1 命中（≤1 turn 残留）→ 下一轮 0 命中 + 库内软删除（`files_active` 回到 5013）；
  - A5 时延观测：索引命中回合 4.1–8.3s/轮；fallback 回合 87.1s（13×）。
- 报告（含逐轮证据表与复现方式）：[`reports/phase5_real_session_e2e_report.md`](reports/phase5_real_session_e2e_report.md)。

### Notes

- 登记：① 知识层状态面（含 `StatusReport.Watch`）未经 web/observe 暴露，真实会话无法直读 mode/watch/queue；② `/web/api/turn` 的 usage 在含 reasoning 的回合出现不自洽读数（待核，本报告只用墙钟与库事实）；③ `watch=on` 场景未在本轮验证（需重启 runtime-server）；④ on-mode A/B（≥20 任务）与端到端 p95 统计门槛仍未做——三项统计门槛仍以 `acceptance_phase5_test.go` 进程内复现为准。
- 两枚探针文件已在验证后删除并完成软删除传播；工作树未留残余。

---

## 2026-10-01 — Phase 5 切片 8 实施：交付 3 收尾（未稳定版本 token 不得参与复用判定）

### Changed

- `backend/internal/knowledge/confidence.go`：`CompareKnowledgeVersion` 新增未稳定口径——任一侧带 `#pendingN`（外部变更刚被发现、索引尚未追上）一律按 `VersionStatusUnknown` 处理，**即使两侧字符串相等**。理由：pending token 的含义是"索引落后于磁盘"，它不钉住任何内容状态；两侧都是 `#pending3` 并不表示"同一份知识"，只表示"当时都有 3 个待处理变更"。相等即放行会让不稳定快照被当作可复用知识（fail open）。
- 新增导出 `knowledge.IsVersionUnstable(version)`：复用判定与注入前过滤共用同一口径。
- `backend/internal/contextmgr/knowledge.go`：`knowledgeItemStale` 同步拒绝未稳定 token（注入前最后一道防线，Phase 6 的 `stale_item_injected=0` 口径）。

### Verified

- `knowledge/confidence_test.go`：版本比较表新增 3 例（current/stored/both pending → unknown），复用门表新增 1 例（两侧同为 pending → explore/不可用/Stale，reason=version_unknown）。
- `contextmgr/knowledge_test.go`：`TestKnowledgeStaleAndFloorFiltering` 增加 `wv1#pending3` + reason=ok 的条目，stale 过滤计数 2 → 3（未稳定条目不得进 prompt）。
- `internal/knowledge` + `internal/contextmgr` 全包通过；`go build ./...` OK。

### Notes

- 这是交付 3"版本向量参与复用判定"的最后一块：索引侧（代次化版本缓存 + 判定点校正 + `#pending` 标记）→ 判定侧（本切片 fail-closed）→ 注入侧（context item 过滤）三处口径一致。
- Phase 5 实现侧至此收口；剩余仅"真实会话级 E2E 与端到端 p95（on-mode A/B）"测量轮。

---

## 2026-10-01 — Phase 5 切片 7 实施：交付 1 第三类变更源（fsnotify 可选源）

### Changed

- `backend/internal/knowledge/change_watch.go`（新）：`watchSource` 把工作区文件系统事件翻译成工作区相对路径并交给**同一个变更队列**（debounce + 串行增量仍由队列负责，本文件只负责"发现"）。
  - 只监听目录；**事件按索引口径过滤**（`resolveIndexTargets`：内置忽略集 + `.gitignore` + 非代码后缀），知识库自己的 `.aicli/*.db(-wal/-shm)` 写入不会形成回环；另有"代码后缀预筛"，避免每个事件都去读 `.gitignore`。
  - 遍历剪枝：内置忽略集 + 隐藏目录不监听；新建目录补监听；目录数上限 `maxWatchDirs=2048`，超限显式降级并记录原因（不静默半监听）。
  - 启动失败（权限/配额/队列不可用）只记录原因，绝不让 Activation/Open 失败（Degrade-Not-Fail）。
- `backend/internal/knowledge/config.go`：新增 `knowledge.watch`（off|on，默认 off）+ `ParseWatch` / `WatchEnabled` / `Normalize`/`Validate` 接入；`backend/internal/config/manager.go` 的 `ValidateKnowledgeConfig` 同步（未知值在加载期拒绝）。
- `backend/internal/knowledge/knowledge.go`：`Open` 在 owner + shadow|on + `watch=on` 时启动监听；`Close` 先停 watcher、再停队列、最后关库；`WatchStatus()` 提供 Active/Dirs/DegradedReason。
- `backend/internal/knowledge/status.go`：`StatusReport.Watch`（开启却没生效时必须给出原因）。
- 配置模板：`configs/runtime.yaml` / `runtime.win7.yaml` 补 `watch` 注释模板（**默认不开启**——watch 是系统级资源，默认开启会改变既有部署的资源画像）。

### Verified

- `knowledge/change_watch_test.go` 4 例（真实 fsnotify 事件）：空闲期新建/修改代码文件被即时索引（0.70s 内）；被忽略路径（node_modules / .aicli / 非代码后缀 / `.db-wal`）不入索引；默认 off 时无监听且不即时索引；配额耗尽显式降级（`cap reached`）。
- `internal/knowledge` 全包 `-race` ok（84.3s）；`internal/config` ok；`go build ./...` OK。

### Notes

- 定位：edit hook 覆盖"agent 自己改的文件"，外部校正（git/stat）覆盖"判定点能看到的落后"，watcher 覆盖**空闲期**——最坏延迟从"一个 turn"降到"一次事件"。
- 剩余：版本向量参与 context item（交付 3 剩余）、真实会话级 E2E（on-mode A/B 与端到端 p95）。

---

## 2026-10-01 — Phase 5 切片 6 实施：R12 第三段（库损坏时留证重建 + 重建期降级）

### Changed

- `backend/internal/knowledge/recovery.go`（新）：`isCorruptStoreError`（驱动码 `CORRUPT`/`NOTADB` 含扩展码优先、消息模式兜底；**版本不匹配不属于损坏**）、`quarantineCorruptStore`（主库 + `-wal`/`-shm` 副文件一起改名留证，时间戳后缀、绝不覆盖历史证据、绝不删除）、`openStoreWithRecovery`（仅 owner 角色对损坏错误做一次"留证 → 重建 → 重开"）。
- `backend/internal/knowledge/knowledge.go`：`Open` 在 `OpenStore` 失败且判定为损坏时走自愈路径，把留证路径记在 Layer 上（`recoveredFrom`）；其它错误（版本不匹配 / 权限 / 锁）原样返回。
- `backend/internal/knowledge/status.go`：`StatusReport.StoreRecoveredFrom` 暴露留证路径，`DegradedReason` 说明"库已重建、索引为空、等待重新索引"——零值必须能被解释，否则会被误读成"这个工作区没有代码"。
- 语义：`knowledge.db` 是派生数据（随时可由工作区重建），所以损坏时不让用户卡在"打不开"；重建期索引为空 → 判定与注入按既有 degraded 语义降级（绝不把空索引当成"没有知识"以外的含义）。reader 没有写权限，只报错、把处置权交还 owner。

### Verified

- `knowledge/recovery_test.go` 4 例：损坏库 → owner 打开成功、损坏文件留证且内容原样、重建后可重新索引（Stats.Files > 0）、状态面给出留证路径 + 降级原因；**版本不匹配绝不留证改名**（`ErrSchemaNewer` 仍拒绝、原库原样保留、未来迁移行仍在）；只读路径不动文件；`isCorruptStoreError` 表驱动 6 例（含"版本不匹配/旧库提示不是损坏"的负例）。

### Notes

- 同时修复切片 5 的验收用例在**全量包跑**下的偶发失败：`TestAcceptanceCheckoutStaleDetection50Samples` 原先只认"`#pending` 保守"这一条路径，但队列 worker 可能恰好在判定前把 checkout 后的内容异步吸收——那是**正确**判定（索引与磁盘一致）。现在断言真正的不变量：checkout 后**绝不能返回 checkout 前那个稳定版本**（保守路径或"版本已随内容变化"的吸收路径都可），并在每轮判定前排空队列消除遗留 debounce 定时器；50/50 走保守路径。

---

## 2026-10-01 — Phase 5 切片 5 实施：验收门槛可复现化（三项统计口径）

### Changed

- `backend/internal/knowledge/acceptance_phase5_test.go`（新）：把 04 §5 Phase 5 的三项验收门槛从"待测量轮"变成确定性自动化用例——
  - **连续编辑 100 次后增量与全量 diff = 0**：100 次编辑（改内容 / 跨文件引用 / 加文件 / 删文件 / 重命名）全部经变更队列 → 定向增量落地后，与"同一终态磁盘状态的一次全量重建"做 **ID 级**逐行对照（复用 `projectWorkspaceIndex` / `diffProjections`）。
  - **外部 `git checkout` 后 stale 判定正确率 100%（样本 ≥ 50）**：50 轮"未提交改写 → 索引追上（必须稳定，0 误报）→ `git checkout --` 外部写盘（必须立即 `#pending` 保守）"，逐轮计数。
  - **索引写不阻塞会话读**：写侧持续跑定向增量（每条都经 `execWrite` → 锁等待采样），读侧并发 `FindSymbols` / `Stats`；断言读延迟 p95 < 50ms、写路径锁等待 p95 < 50ms、锁重试失败 0 次（"双实例只读降级"由既有 activation/owner 用例覆盖，不重复）。

### Verified

- 三项用例全绿（`-count=1`）：100 次编辑后 diff = 0；checkout 检出 50/50、误报 0；实测读延迟 p50 ≈ 0.55ms / p95 ≈ 1.09ms（2068 次采样），写侧锁等待 p95 = 0ms（单写者稳态）、重试失败 0。
- 口径：前两项是确定性判定（与机器性能无关）；第三项是**本机进程内**近似（单 workspace、写连接 + 只读连接并发），阈值取文档原值 50ms。

### Notes

- 剩余：fsnotify 可选源（交付 1 第三类）、R12 第三段（DB 损坏时重命名重建 + 降级 off）、真实会话级 E2E（on-mode A/B 与端到端 p95）。

---

## 2026-10-01 — Phase 5 切片 4 实施：迁移版本拒绝（交付 6）+ GC（交付 4）

### 交付 6：schema 迁移版本拒绝（新 DB 不被旧代码打开）

风险 R12 的第二半（"版本拒绝"）此前只做了一半：旧库（schema 落后）由 reader
显式拒绝、由 owner 迁移；但**新库**（schema 高于本二进制）两条路径都会放行——
旧代码按旧列集读写新结构，静默给错比失败更糟。

### Changed

- `backend/internal/migrate/migrate.go`：`Apply` 在**所有 store 的公共收口点**上拒绝"库版本高于本二进制"（`ErrSchemaNewer`，消息带两侧版本号与处置建议）；新增 `LatestVersion`（顺序无关取最大值）与 `maxVersion`。六个 store（knowledge / agentcontrol / artifact / chat / team / supervision / subagentbatch）一起受益，不需要各自加检查。
- `backend/internal/knowledge/store_sqlite.go`：`verifyInitialized`（只读路径）补上"新于本二进制"分支，包 `migrate.ErrSchemaNewer` 并给出可操作提示（"用更新的二进制打开这个 workspace"）。
- `backend/configs/runtime.yaml`：补 GC 两个旋钮的注释模板（`max_db_size_mb` / `gc_retention_days`）。

### 交付 4：GC（软删除行物理清理）

### Changed

- `backend/internal/knowledge/store_sqlite_gc.go`（新）：`GCDeleted(ctx, workspaceID, cutoff)` —— 在单事务内**先显式删 symbols**（`symbols_fts` 的同步靠 `symbols` 上的 AFTER DELETE 触发器，而 SQLite 的外键级联动作**不触发**触发器；只删 files 会留下"搜得到、查不到"的幽灵符号），再删 files（级联清掉 refs / symbol_versions / symbol_aliases）；refs 行数在级联前先数（SQLite 不报告级联行数）。返回 `GCReport`（Files / Symbols / Refs / BytesBefore / BytesAfter）。只读 store 返回 `ErrReadOnlyStore`。
- `backend/internal/knowledge/gc.go`（新）：保留期（`DefaultGCRetentionDays = 30`，`Config.GCRetention()` 可配）、触发口径（`shouldAutoGC`：超过 `max_db_size_mb` 且冷却窗口 `DefaultGCInterval = 10min` 已过）、`Layer.RunGC`（显式入口，owner 专属；reader/off 返回零报告且不报错）、`Layer.maybeAutoGC`（判定点顺带检查，失败只记录不传播——GC 是维护动作，不是复用判定的正确性前提）、`GCStats` 状态摘要。
- `backend/internal/knowledge/{store.go,knowledge.go,status.go,change_sync.go,config.go}`：`Store` 接口加 `GCDeleted`；Layer 加 GC 状态与测试注入口（`gcSizeFn`）；`StatusReport.GC` 暴露摘要；`ObserveVersion`（判定点）在采样前顺带检查库大小；`DefaultMaxDBSizeMB` 200 → **512**（04 §7.4 校准值：本仓库实测 313.9 MiB，200 的初值一开始就压着上限）。

### Verified

- 交付 6：`internal/migrate` 新增 `TestLatestVersion` / `TestApplyRefusesNewerSchema`（含 `errors.Is(ErrSchemaNewer)` 与"消息带两侧版本号"断言、以及"已知到 v3 的二进制可正常打开"的正例）/ `TestApplyIsIdempotent`；`knowledge` 新增 `TestOpenStoreRefusesNewerSchema`（写入与只读两条路径都拒绝 + 可操作提示）。
- 交付 4：`knowledge/gc_test.go` 6 例 —— 过期软删除文件被物理清理（Files=1、Symbols>0、Refs>0；FileByPath 找不到、符号不可见、存活文件的符号保留）；保留期内不清理（并钉住 `GCRetention()` 的缺省/覆盖/`<=0` 回落口径）；`shouldAutoGC` 表驱动 6 例；判定点触发（超软上限 → 真清理，冷却窗口内不重复触发）；只读 store 硬失败；reader 层 no-op。**GC 不改变工作区版本**（版本只由未软删除文件构成），因此判定点触发不会污染版本判定。
- `go build ./...` OK；`internal/knowledge`/`migrate`/`agent`/`toolkit`/`tools`/`toolctx`/`contextmgr`/`runtimeapi`/`subagentbatch`/`artifact`/`supervision`/`agentcontrol`/`cmd/runtime-server` 全绿；GC 用例 `-race` 通过。

### Notes

- 交付 4 刻意**不做 VACUUM**：删行后 SQLite 原地复用空闲页（文件不缩但不再增长），而 VACUUM 要独占锁并重写全库，绝不能出现在判定点热路径上。需要收缩文件的运维动作仍可手动 `sqlite3 <db> VACUUM`；`GCReport` 的 BytesBefore/After 如实反映"文件是否缩了"。
- 交付 6 的拒绝口径是**双向**的：落后 → reader 拒绝 / owner 迁移；超前 → 两条路径都拒绝。损坏时"重命名重建 + 降级 off"（R12 的第三段）仍待做。
- 剩余：fsnotify 可选源（交付 1 第三类）、版本向量参与 context item（交付 3 剩余）、三项统计验收（编辑 100 次 diff=0 / checkout 后 stale 100% ≥50 样本 / 锁等待 p95）。

## 2026-10-01 — 索引范围收口：.gitignore 过滤接入（G11/R11 的 .gitignore 层）

### Changed

- `backend/internal/knowledge/gitignore.go`（新）：git 语义的 .gitignore 解析与匹配——按目录层级叠加（root→深，最后命中胜出）、`!` 取反、`#` 注释、`\` 转义、尾部 `/` 仅匹配目录、前导 `/` 锚定、无 `/` 模式按 basename 任意深度匹配、`*`/`?`/`[]` 不跨 `/`、`**/` / `/**/` / `/**` 三种整段形态；单文件读失败 / 规则编译失败降级为"无该规则"，不阻断索引。
- `backend/internal/knowledge/indexer.go`：`collectIndexableFiles` 在 WalkDir 中按目录栈应用 .gitignore（内置 `ignoreDirs` + 隐藏目录仍是第一道防线，且不被取反规则重新包含）；`IndexPaths`/`resolveIndexTargets` 与全量同口径，拒绝被内置目录 / 隐藏目录 / .gitignore 排除的显式路径（计入 Errors）；新增忽略规则后，已入库文件由下一次全量对账软删除。
- `backend/internal/knowledge/config.go`：新增 `knowledge.index.use_gitignore`（默认 true）；关闭后回到"内置忽略集 + 隐藏目录"旧口径（回滚逃生舱）。

### Verified

- 新增 7 例测试：规则语义表（basename/锚定/目录/`**`/转义/字符类/CRLF）、嵌套优先级与目录通配取反链（`dir/*` + `!dir/` + `!dir/**`）、collect 集成（node_modules 与隐藏目录保持生效）、开关关闭口径、全量软删除新增忽略文件、IndexPaths 拒绝被忽略路径。
- `go build ./...` OK；`internal/knowledge` 全包测试通过（含全部既有用例）。

### Notes

- 范围边界：只读取工作区内的 .gitignore；`.git/info/exclude` 与用户全局 excludesFile 不参与（前者需要先定位仓库根，后者是机器级配置）。非 git 工作区同样生效。
- 内置忽略集与 .gitignore 是**叠加**关系：node_modules / vendor / dist 等即使未被 .gitignore 声明也会被剪枝，且不能被 `!` 规则重新包含。

## 2026-10-01 — Phase 5 切片 3 实施：变更源 2（git + stat 外部校正）

edit hook 只覆盖"工具写盘"；shell/exec 写盘、外部进程写盘、git checkout/pull/reset
都会绕过它。本切片补齐**外部变更校正源**，并把校正挂在"判定点"上：版本采样前先校正，
让 stale 判定建立在校正后的状态上。

### Changed

- `backend/internal/knowledge/change_git.go`（新）：`GitChangeSource` —— HEAD 移动（`rev-parse` + `diff --name-only old..new`）与工作树状态（`status --porcelain -z`，含未跟踪新文件、删除、重命名两侧）。**按转移报告**（只报新条目/状态码变化）：持续 modified 的文件不会每轮重复上报，避免版本判定永远带 pending。只读契约：`GIT_OPTIONAL_LOCKS=0`（不抢锁、不刷新 index）；非仓库/git 不可用 → `Skipped`（不是错误）；路径统一工作区相对并丢弃工作区外路径（工作区是仓库子目录时）。
- `backend/internal/knowledge/change_scan.go`（新）：`ScanIndexedFileStats` —— 把 store 的已索引文件与磁盘现状按 **size + mtime_ns** 比对（与索引器 `stageFile` 的廉价预筛同口径），磁盘已删 → `Missing`（交索引器软删除）。这是 `git checkout -- <file>`（还原到 HEAD、status 干净、HEAD 未动）这类变化的**唯一发现者**，也是非 git 工作区的兜底。
- `backend/internal/knowledge/change_sync.go`（新）：`Layer.SyncExternalChanges`（三源合并 → 队列；owner + shadow|on 门控；失败/节流降级，永不阻断）与 `Layer.ObserveVersion`（判定点：校正 → 发现变更则失效版本缓存 + 本次 token 附 `#pendingN` 未稳定标记 → 旧知识立即不可复用，fail-closed）。**分源节流**：stat 校正每次判定都跑（廉价、正确性靠它），git 校正按 2s 节流（只补"未跟踪新文件"这一 stat 看不见的维度）。校正结果先按索引器同口径过滤（工作区 + `codeExtensions`）——知识层自己的 `.aicli/*.db`、锁文件不会被当成变化喂给队列。
- `backend/internal/knowledge/{knowledge,planner,change_queue}.go`：版本缓存（`versionCache`）新增**索引代次**（`ChangeQueue.Generation`，每次成功的定向增量 +1）：TTL 内但代次已变 → 强制重采样。修掉"失效后又被写回旧版本"的竞态（索引落地与采样并发时，旧版本曾能被缓存压住 30s）。`Plan` 的版本采样改走 `ObserveVersion`；队列 `OnResult` 仍失效缓存（双保险）。**并修复队列饥饿缺陷**：`Mark` 原实现即使路径已在待办集合里也发 wake 信号 → debounce 被反复重置，反复标记同一路径（校正源每轮重标仍然落后的文件正是这种模式）会让 worker 永远等不到开工时机；改为"只有真正新增待办才唤醒"（切片 1 遗留缺陷，本次由校正源测试暴露）。
- `backend/internal/knowledge/change_scan.go` + `change_sync.go`：git 侧路径再过一道**"索引是否真落后"**过滤（`StatScanReport.Fresh`）——工作树相对 HEAD 持续 modified 的文件在索引已跟上后不得再入队，否则版本判定会永远带 `#pending`（git 状态转移与索引追上之间存在一个 ≤2s 的窗口，曾让校正源"误报"自己已处理过的文件）。
- `backend/internal/agent/{loop,tool_exec_middleware}.go`：**turn 边界触发**——`run()` 在 turn 开始（首轮 LLM 请求与 Plan 之前）跑一次校正，让本 turn 的复用判定看到校正后的版本；`knowledgeLayerForAgent` 取 `context_knowledge_layer` 的 `*knowledge.Layer`；未挂载知识层是 no-op（off/reader 零副作用）。

### Verified

- 新增 10 例测试：git 源 3 例（真实临时仓库：修改/未跟踪/删除三态 + HEAD 移动 + 工作区外过滤；非仓库跳过）；stat 源 1 例（外部改写 size+mtime、磁盘删除）；Layer 级 5 例（外部改写 → 队列 → 版本立即变保守 → 索引追上后稳定且不同；**`git checkout --` 还原**场景（git 侧断言干净，只有 stat 能发现）；**持续 modified 但索引已跟上不得产生 pending**（跨 2s 节流窗口连续采样）；git 仅节流、stat 不受限；nil/reader no-op）；agent 侧 1 例（turn 边界确实触发；未挂载不触发）。
- `go build ./...` OK；`internal/knowledge`/`agent`/`toolkit`/`tools`/`toolctx`/`contextmgr`/`runtimeapi`/`cmd/runtime-server` 全绿。

### Notes

- 校正**成本**：stat 校正 ≈ O(已索引文件) 次 stat（每次判定点执行，通常 <30ms）；git 校正 ≈ 1–2 次 git 进程（2s 节流）。版本采样本身仍有 30s TTL 缓存，但校正不受 TTL 限制——正确性优先。
- 验收门槛"外部 git checkout 后 stale 判定正确率 100%"的**机制**已具备并被测试覆盖；50 样本 × 连续 checkout 的统计实测留待测量轮（连同"编辑 100 次 diff=0"与锁等待 p95）。
- 仍待实施：fsnotify 可选源（交付 1 的第三类，用于空闲期外部变更的即时发现；当前由"判定点 + turn 边界"覆盖）、GC（交付 4）、迁移版本拒绝（交付 6）、版本向量参与 context item（交付 3 的剩余部分）。

---

## 2026-10-01 — Phase 5 切片 2 实施：edit hook 全链接线（工具 → ctx → 队列）

切片 1 交付了"入口 + 队列 + 执行端"但无人调用；本切片把**编辑类工具**接上：
落盘成功 → 会话知识层 `MarkChanged` → 串行队列 → debounce 定向增量。
链路按"工具只认 ctx、装配方注入句柄"分层，off/reader/非会话路径零副作用。

### Changed

- `backend/internal/toolctx/context.go`：新增 `WithFileChangeNotifier` / `FileChangeNotifierFromContext`（会话级变更接收方，nil 不注入）。
- `backend/internal/toolkit/tools/change_notify.go`（新）+ `{write,edit,multiedit,append_write,apply_patch,download}.go`：落盘**成功**后报告被写路径（失败不报告；apply_patch 报告全部 mutated paths）。shell/exec 类工具不标记（无法可靠判定写盘；由后续 git diff 校正源兜底）。
- `backend/internal/agent/{tool_exec_middleware,loop,approved_tool}.go`：`knowledgeChangeNotifierForAgent` 从 `context_knowledge_layer` 取 `knowledge.ChangeNotifier`，在 `toolCallContext` 与 `approvedToolCallContext` 两条工具执行路径上注入；未挂载知识层不注入（off 基线零副作用）。
- `backend/internal/knowledge/{knowledge,activation}.go`：变更队列所有权从 `Activation` 下移到 `Layer`（agent 侧只持有 Layer；同 workspace 多 Activation 共享一个串行 worker，不会两个 worker 抢同一 store）；`Activation.MarkChanged/ChangeQueue` 保留为转发；新增 `knowledge.ChangeNotifier` 接口（`*Layer` / `*Activation` 都实现）。
- `backend/internal/knowledge/change_queue.go`：`Mark` 过滤工作区**外**的绝对路径（download 写到工作区外不制造 Errors 噪声；直接调 `IndexPaths` 仍显式计入 Errors）。
- `backend/internal/api/runtimeapi/handler.go` + `backend/cmd/aicli/commands/chat_actor_host.go`：**shadow 档改为发布 `context_knowledge_layer`（仍不设 `context_knowledge_mode`）**——句柄是编辑标记的接收方，只让索引保鲜，不注入 prompt（`contextmgr` 在 mode=off 时零调用零注入，`ModeShadow` 契约不变；已有测试 `TestContextManagerKnowledgeAssemblyOffWithPlannerZeroCalls` 钉住该前提）。

### Verified

- 新增 12 例测试：工具侧 5 例（write/edit/multiedit/apply_patch/append_write 报告路径、失败不报告、无接收方零副作用、helper no-op、**端到端**：编辑工具 → `MarkChanged` → 队列 → debounce 增量 → store 新旧符号正确替换）；agent 侧 2 例（两条 ctx 路径绑定接收方 + 未挂载/类型不符不绑定）；装配侧 2 例更新（aicli / runtimeapi 的 shadow 断言改为"发布句柄但不设 mode"）；toolctx/queue 过滤随既有用例覆盖。
- `go build ./...` OK；`internal/knowledge`（全量）/`agent`/`toolkit`/`tools`/`toolctx`/`contextmgr`/`runtimeapi`/`cmd/runtime-server` 全绿；`cmd/aicli/commands` 目标用例 PASS（该包为既知红，见 Phase 4 注记）。

### Notes

- **变更源 1 完成**（agent edit hook：主、同步标记）；变更源 2（git diff 校正）与 3（fsnotify 可选）仍待实施——shell/exec 写盘、外部进程写盘、`git checkout` 都靠它们兜底。
- 真实会话 E2E（模型实际调用 write → 索引在 debounce 后刷新）未跑：链路各段已有测试（工具报告 / ctx 绑定 / 装配发布 / 队列增量），端到端会话级验证留待测量轮。

---

## 2026-10-01 — Phase 5 切片 1 实施：定向增量（IndexPaths）+ 串行变更队列

04 §5 Phase 5 的**执行端**先落地：变更源（edit hook / git diff / fsnotify）只负责
"标记哪些文件变了"，队列与定向增量负责把它变成索引更新。切片 1 覆盖交付 1 的
edit hook 入口、交付 2（debounce + 串行队列）与交付 5（增量 vs 全量等价性测试）。

### Changed

- `backend/internal/knowledge/indexer.go`：**定向增量 `IndexPaths`**（Phase 5 交付 1/2 的执行端）——抽取 `stageFile`/`writePendingFiles` 单文件管线供全量与增量共用；不遍历工作区、不做全量删除对账、不写 adapter 版本；路径越界/后缀不可索引计入 Errors（不静默）；磁盘已删路径走软删除（与全量对账同语义）；adapter 版本不一致且开启 full_rebuild 时**整体跳过**（绝不写出新旧 adapter 混装库）。新增 `IndexJobKindIncremental`，状态面可区分全量/增量运行。
- `backend/internal/knowledge/change_queue.go`：**串行变更队列**（Phase 5 交付 1/2）——`Mark`（非阻塞、幂等、去重）→ debounce（默认 300ms）→ 单 worker 串行 `IndexPaths`（`runMu` 保证至多一个写事务在跑）；`MaxBatch`（默认 256）防单 job 拉长；`Close` 幂等并等待当前 job；nil store/未开启时全链路 no-op。`Activation.MarkChanged` 是其 edit hook 入口（owner + shadow|on 才入队，与 `Recorder` 同门控）。
- `backend/internal/knowledge/indexer.go`（**缺陷修复，等价性测试捕获**）：`loadKnownSymbols` 新增"排除本轮重写文件"参数——旧实现把**被重写文件的旧符号**也留在名字表里，同一名字（旧+新）触发"歧义即不绑定"，引用静默丢 `to_symbol_id`；增量与全量因此不等价，且**全量重建对"文件改后仍定义同名符号"的自遮蔽同样中招**（既有缺陷，非 Phase 5 引入）。修复后 `TestIncrementalMatchesFullRebuild` 逐行（含 ID）等价。

### Verified

- Phase 5 定向增量与队列测试 13 例（`internal/knowledge`，含 `-race` 复跑）：`IndexPaths` 只刷新被标记文件（旧符号消失/新符号入库/未标记文件零重写/**自遮蔽引用必须绑定新符号**）、软删除（行保留 + active 列表剔除）、越界与后缀拒绝计数、adapter 版本变化整体跳过（旧索引零写入）、**增量 vs 全量 ID 级等价性**（改 2/加 1/删 1、分两批标记、双 store 同根比较）；队列去重合并、debounce 后台自动执行、8 goroutine 并发标记不丢不并发写、Close 幂等且停止入队、nil store no-op、`Activation.MarkChanged` 端到端（owner+shadow 触发增量，nil Activation no-op）。
- `go build ./...` OK；`internal/knowledge`（含既有用例）与相关包全绿；`-race` 复跑队列/增量用例通过。

### Notes

- **尚未接线**：agent 编辑工具的调用点（write/edit/apply_patch 落盘后调用 `Activation.MarkChanged`）与 git diff / fsnotify 两类变更源仍是下一步；本切片只交付"入口 + 队列 + 执行端"。
- **登记发现（跨 workspace 名字表）**：`loadKnownSymbols` 读的是 store 内**全部**符号（`FindSymbols` 无 workspace 过滤）。生产上 store 与 workspace 一一对应（DB 在 `<workspace>/.aicli/`），故当前不可达；但"一个 store 多 workspace"（共享 DBPath）时会出现跨 workspace 同名歧义/误绑定。已登记，待 Phase 5 的 owner 仲裁/多工作区议题一并处理。
- **等价性口径**：ID 级等价成立的前提是"引用方也被标记"（变更源现实语义）；未标记引用方的旧引用会暂时悬空（`to_symbol_id` 指向已被替换的旧符号），由下一次标记或全量刷新——该边界在 `TestIndexPathsRefreshesOnlyMarkedFiles` 中显式断言。

---

## 2026-10-01 — 修复：code_search 限定名/精确名排序 + code_inspect 去重语义（修复轮 2）

承接前一条修复轮，继续收敛工具层语义缺口（不改索引/知识层）。

### Changed

- `code_search`：限定名查询（`a.b` / `a.b.c`）先做尾段精确符号解析，把限定前缀匹配的符号前置；命中完整限定名时 `confidence=1.0`（修复 `knowledge.Plan` 这类查询返回无关命中）。FTS 命中按"精确名 > 前缀 > 包含"稳定重排（修复 `PlanInput` 本体排在 `SessionSubscriptionPlanInput` 等子串命中之后）。
- `code_search`：explanation 改为"返回 N 条"，`limit` 截断时注明命中总数可能更多；限定名精确命中时跳过低相关补量。
- `code_inspect`：view 的 unchanged-window 去重命中时，结果显式标注 `content_omitted=view_dedup` 并在 explanation 说明（避免把 stub 提示当符号正文）。

### Verified

- 新增 3 例测试（限定名尾段解析、精确名排序、去重标注）；`go build ./...` OK；`internal/toolkit/tools` 与 `internal/tools` 全绿。

---

## 2026-09-30 — 修复：code.* 工具面缺口修复轮（评审 + 修复）

对 code.* 工具面做只读评审（真实会话 30+ 次调用 + 实现走查），并修复其中可低风险落地的一批缺口（完整评审清单与后续项见 `docs/plan/code-tools-gap-review-and-fix-plan-20260930.md`）。

### Changed

- **shadow 档统一**：`code_inspect` / `code_navigate`（definition/members）/ `code_references` / `code_callers` 与 `view --symbol` 在 `mode=shadow` 时不再返回索引结果（候选照算、返回 grep/view，`fallback.reason=shadow_mode`）；此前仅 `code_search` 判断。
- **`code_references`**：`kind` 做闭集校验（reference|call|import|implement），非法值返回参数错误；引用类 fallback 的 explanation 标注"文本近似、不保证 kind 过滤"。
- **fallback 截断透传**：`fallbackEnvelope` 读取 grep（`truncated`/`results_truncated`）与 view（`is_truncated`）元数据并写入 `env.Truncated`；各调用点改为 `env.Truncated || clamped`，不再覆盖。
- **`code_search`**：fallback 标注 lang 过滤未生效；on 档低相关命中（无任何命中与查询同名/包含）补一次 grep 作为补充证据（`source=index+grep`，`fallback.reason=low_confidence_supplement`）。
- **`code_inspect`**：`limit` 裁剪或 view 截断时置 `truncated=true`；view 读取失败时置 `degraded=true` 并在 explanation 附错误。
- **`code_navigate`**：file_path 反斜杠归一化为正斜杠（修复 Windows 下 members 静默 0 命中）；空 direction 明确报错；definition 不再写无意义的 truncated；members 先放大候选窗口、过滤目标文件后再按 limit 截断（截断口径修正）。
- **注册门控与解析器**：`mode=off` 时即使 `code_tools=on` 也不注册 code.*（ADR-0004 §4.4 全局硬闸）；只读句柄按 db size/mtime 变化失效重开（不再永久指向旧快照）。
- **描述分工**：`grep` 模型可见 `Description()` 并入 Phase 3 分工句（此前分工句被覆盖、模型不可见）；`view` 描述指向 `code_inspect`。

### Verified

- 新增/更新测试：shadow 全工具覆盖、非法 kind、反斜杠路径、空 direction、inspect 截断、低相关补量、注册门控 mode=off 硬闸；`go build ./...` OK；`toolkit` / `toolkit/tools` / `tools` / `knowledge` / `config` / `agent` / `contextmgr` 全绿；`go vet ./internal/toolkit/tools/ ./internal/tools/` 通过。

### Notes

- 未落地（登记后续）：ADR-0004 陈旧度分级（staleness/snapshot 字段、分级注册、逃生舱）；引用索引漏报（`EvaluatePlan` 生产调用点缺失）根因与索引侧修复；FTS exact-name 加权与限定名（`knowledge.Plan`）查询支持。

---

## 2026-09-30 — Phase 4 实施：Adapter SPI 与可选 LSP

Phase 4（`06` §4 Phase 4 / `04` §5 Phase 4）实现落地：`LanguageAdapter` SPI 扩展（能力声明）、builtin 通道显式化、tree-sitter 可选通道（未接入→降级）、进程外 LSP 语义通道（锁 / 上限 / 崩溃 / 超时 / 僵尸回收 + 位置编码边界）、adapter/parser 版本参与身份与全量重建、离线与降级矩阵。**默认行为零变化**（adapter 缺省 builtin、`lsp.enabled=false`）。不 git commit。

### Changed

- `backend/internal/knowledge/adapter.go`（新增）：SPI 扩展（`Version` / `Detect` / `Capabilities`）+ `AdapterCapabilities`（definition|references|callers|types|tests）+ `AdapterKind`（builtin|treesitter|lsp）+ `SelectIndexAdapter`（不可用即降级 builtin，且给出可观测 Reason）+ `SemanticAdapter`（02 §44）。
- `backend/internal/knowledge/adapter_builtin.go`：接口与产物类型迁至 adapter.go；补 `Version/Detect/Capabilities`；`SignatureHashFor(sig, adapterVersion)` 参数化（builtin 仍走 `AdapterVersion` 常量，字节级等价）。
- `backend/internal/knowledge/adapter_treesitter.go`（新增）：可选通道，`Available()=false`（v1 未接入语法），能力面全 false；被选中时降级并记录原因。
- `backend/internal/knowledge/adapter_lsp.go`（新增）：进程外语义适配器（definition / references），复用 `internal/lsp` 的 canonical 位置边界（ADR-0006 §4.4）；不可用 / 崩溃 / 超时统一 `ErrSemanticUnavailable`（不阻断）。
- `backend/internal/knowledge/lsp/`（新增子包）：`manager.go` 进程管理——ADR-0002 §4.4 锁文件 `<workspace>/.aicli/knowledge/lsp/<lang>-<sha256(root)[0:12]>.lock`（活进程持锁→**不 spawn**，直接降级；过期→接管；Close 释放）；`max_processes` / `memory_limit_mb` 超限→回收 + 降级；崩溃检测走 `client.Done()`；`proc_windows.go` / `proc_other.go`（进程存活与内存探测）。
- `backend/internal/lsp/spec.go`：`SpawnProcess` 接入 `internal/executor.ProcessGuard`（ADR-0005 §4.1：Windows Job Object + KILL_ON_JOB_CLOSE / Unix Setpgid；绑定失败可观测；`Kill` 改走 `guard.Terminate`）；`DialResult` 增 `Guard` 字段。
- `backend/internal/lsp/client.go`：新增 `Call(ctx, method, params)`（知识层语义查询复用既有传输 / 生命周期 / 编码协商，不新写协议栈）。
- `backend/internal/knowledge/indexer.go`：adapter 由配置选择（替换硬编码 `builtinAdapter{}`）；`resolveRefs` 的 source/confidence 由 adapter 推导；adapter 版本与库内记录不一致且开关开启 → 全量重建（`inspectFile(force)`）；`IndexResult` 增 adapter / 降级 / 全量重建字段。
- `backend/internal/knowledge/store.go` / `store_sqlite.go` / `migrations/0003_workspace_adapter_version.sql`（新增）：`workspaces.adapter_version` 读写。
- `backend/internal/knowledge/config.go`：`knowledge.adapter`、`knowledge.index.full_rebuild_on_adapter_change`、`knowledge.lsp.{enabled,mode,max_processes,memory_limit_mb,startup_timeout,request_timeout}`（mode 仅 off|self；`external` 按 ADR-0002 §4.3 拒绝）。
- `backend/internal/knowledge/version_hash.go`：工作区知识版本改用库内 adapter 版本（未记录时回落常量，既有库的版本值逐字节不变）。
- `backend/internal/knowledge/adapter_lsp_factory.go`（新增）：语义通道生产构造入口 `NewSemanticAdapterForWorkspace`（ADR-0002 §4.1 双重门控；v1 仅 Go 模块），返回稳定降级 reason；`adapter_lsp.go` 首查惰性 `Ensure`（受 `startup_timeout` 约束）+ `Close`（释放锁与子进程）。
- `backend/internal/toolkit/tools/{code_common,code_references}.go`：**语义通道接入工具面**——`code_references` / `code_callers` / `code_navigate(refs)` 在语义可用时优先返回编译器级引用集合（`source=lsp`）；失败 / 零命中 / kind 不支持自动回落索引路径；位置口径转换集中一处（索引 1-based ↔ ADR-0006 canonical 0-based）、声明自身剔除；`kind=call` 用行内 `name(` 启发式过滤（候选集来自语义通道，confidence 回到 0.90 口径）。
- `backend/internal/toolkit/tools/code_navigate.go`：**definition 语义面按位置入口**——`direction=definition` 支持 `file_path`+`line`（`col` 可选，缺省按行内标识符有界尝试），语义通道优先、索引反查兜底（该行引用 → 目标符号；否则所在符号并明确标注）、最后退化为读取该行。
- `backend/internal/knowledge/adapter_builtin.go`：引用抽取新增字符串/注释守卫 `insideStringOrComment`——`t.Fatalf("Activate(owner): %v", err)` 这类**字符串里的"调用形"文本不再被抽成引用**（live 测量定位到的索引误报源，占测试文件引用的全部噪声样本）。
- `backend/cmd/aicli/commands/{agent_stdio_config_option,chat_actor_host,chat}.go` + `internal/acp/types.go`：**ACP `knowledge.lsp.mode` select 接线**（ADR-0002 §4.2 的宿主侧交付）——id `knowledge.lsp.mode`、值域 `off|self`（select 不受 boolean 能力门控）、category `_knowledge`；**下发门控**：仅当本会话有知识层且 `lsp.enabled=true`（逃生舱不得被会话打开）；切换为**会话级覆盖**（`ChatSession.KnowledgeLSPModeOverride`，绝不写回全局配置），在 `buildLocalChatAgent` 构造**运行时配置副本**时落地——共享配置与其它会话/子代理不受影响，语义适配器缓存按生效值区分，下一个 turn 生效；未启用知识层的会话显式拒绝（非静默降级）。
- `backend/internal/tools/code_index_resolver.go`：进程级语义适配器缓存（key=root|enabled|mode；只缓存成功构造）+ `CodeIndexHandle.{Root,Semantic}` 注入。

### Verified

- 新增测试 17 例：SPI/选择/降级/全量重建/版本敏感 8 例；LSP 语义通道与进程管理 8 例（UTF-16→canonical 位置转换、崩溃/超时/不可用降级、锁活持/过期接管/释放、客户端复用、内存上限回收、非法进程上限）；builtin 测试文件抽取定点 1 例。
- 工具面接线测试 12 例：语义优先（含声明剔除与 1-based↔0-based 转换断言）、语义失败回落索引、`kind=import` 绕过语义、`kind=call` 启发式过滤、门控与缓存（未启用 / 非 Go 模块 → nil）、构造门控（4 段）、**按位置查定义 5 例**（语义优先 / 显式 col / 索引反查 / 所在符号 / 缺 line 报错）、**字符串注释守卫 1 例**（真调用保留、字符串/原始字符串/注释/转义引号四类文本排除、列偏移断言）。
- ACP 选项接线测试 5 例（`cmd/aicli/commands`）：下发门控（无知识层 / `lsp.enabled=false` 逃生舱不下发、select 类型与 `_knowledge` 分类、值域恰为 off|self）、会话覆盖优先与非法覆盖 fail closed、`session/set_config_option` 端到端（切换成功 + 响应回读 + `external` 拒绝且状态不变）、未启用知识层显式拒绝、运行时配置副本落地（nil/空/同值/非法原样返回，不同值返回副本且共享配置零改写）。
- golden set：go/parser 编译级真值 **1765 条**（门槛 ≥200）；**builtin definition precision=1.0000 / recall=0.8510**（function 99.5% / type 100% / method 99.8% / variable 68.8% / constant 16.1%——块内常量按"轻索引"口径不入索引）。
- LSP live（gopls v0.23.0 + 真实 `backend/` 模块）：**definition precision=0.9241–0.9750 / recall=0.9125–0.9750**（多次实测区间：80 查询，73–78 命中）——达到 Phase 4 门槛（≥0.90 / ≥0.85）；**references precision（代理口径）=1.0000 / recall（裁决后）=1.0000**（40 符号；索引基线 40 条，覆盖 33、索引误绑定 7、索引误报 0、语义漏报 0）——**双门槛达标**。
- 口径修正记录：上一轮 references 的 0.7674 系**两处口径问题**——① 裁决把"该位置无定义返回"误判为语义漏报（实为索引在字符串字面量里的误报）；② 索引本身确实把 `t.Fatalf("Activate(owner): %v", err)` 这类文本抽成了引用。本轮修正裁决并加 `insideStringOrComment` 守卫后，误报清零（false_positives=0）、语义漏报清零（semantic_misses=0）。
- 回归：`go build ./...` OK；`knowledge` / `knowledge/lsp` / `lsp` / `config` / `tools` / `toolkit` / `runtimeapi` / `cmd/*` 全绿；`cmd/contractgen` 漂移按测试指引重生成（`frontend/src/types/runtime/event-contract.ts`）。

### Notes

- 默认值零变化：adapter 缺省 builtin；`lsp.enabled=false` 时任何入口都不起 LSP；全部开关关闭时行为与 Phase 3 逐字节一致。
- **前置 ADR-0002 / 0005 / 0006 已于 2026-09-30 由项目 owner 授权代改并 Accept**（"按最佳实践确认"，先例 ADR-0004 / 0008 / 0009）；证据即本条目的实现与验证记录。
- 登记偏差与遗留：① tree-sitter 通道仅登记（未接入语法依赖）；② ~~LSP 语义通道未接入工具面~~ → **已接入**（引用类三工具 + `code_navigate` 按位置查定义；位置口径转换集中一处）；按名字的 definition 仍走索引（索引 definition 实测 P=1.0000）；③ ~~ACP `knowledge.lsp.mode` select 未接线~~ → **已接线**（ADR-0002 §4.2 宿主侧交付完成）；会话覆盖仅存内存、未进 chat-prefs 持久化（重启回配置默认）——与"绝不隐式改用户配置"取向一致，是否需要持久化待产品裁定；④ 内存探测用 `tasklist` 解析（Windows），未接 Job Object 记账；⑤ golden 真值以 go/parser 生成（等价人工标注的可验证真值），测试文件按查询面口径（`is_test=0`）排除；⑥ ~~references recall 门槛未达标~~ → **已达标（1.0000）**，原 0.7674 为裁决口径错误 + 索引字符串误报（已修，见上"口径修正记录"）；⑦ live 测量需**独占运行**：与其它包测试并发时出现过一次全空样本（semantic/builtin 均 0，疑资源竞争下 gopls 未就绪），复跑稳定通过——CI 中应串行执行 live 套件；**异常终止会遗留 gopls 进程**（本轮实测残留 1.5 GB + 0.4 GB 两个，导致后续 `go test` 编译期 OOM）——live 套件应带进程清理，CI 侧建议在 job 结束回收 gopls。
- 已知红（与本轮改动无关）：`cmd/aicli/commands` 包级 FAIL（无 `--- FAIL` 行，测试二进制在并行批次中途被 `os.Exit` 型路径提前终止）。2026-09-30 复核：`-skip 'TestACPSessionMCPStdioEndToEnd|TestMCPHelperServerProcess'` 仍复现，故早前"helper 进程 os.Exit"的定位不完整（精确用例待定，`-v` 日志止于 `TestProfileCommand*` 批次之后）；本轮新增 5 例在该包**全量运行**中全部 PASS（日志可见），失败与本轮改动无关。（`cmd/contractgen` 的漂移为上一轮遗留，已修。）

---

## 2026-09-30 — Phase 3 实施：Code API 与工具面收敛

Phase 3（`06` §4 Phase 3 / `04` §5 Phase 3）实现落地：5 个 `code.*` 工具（注册名 `code_search` / `code_inspect` / `code_navigate` / `code_references` / `code_callers`——provider 函数名约束不接受 `.`，`code.*` 为设计文档的概念命名）、统一返回结构、降级协议（fallback 到 grep/view）、`view --symbol`、工具描述分工指引。默认 off（`knowledge.code_tools=on` 灰度开启；关闭即回滚到纯 grep/view 基线）。收益类验收（探索 token ↓≥40%、fallback 触发率 ≤30%、工具调用总数不增加）留待测量轮；本轮不 git commit。

### Changed

- `backend/internal/knowledge/config.go`：新增 `knowledge.code_tools`（off|on，默认 off）+ `ParseCodeTools` / `CodeToolsEnabled` / `Validate` 接入；`backend/internal/config/manager.go` 的 `ValidateKnowledgeConfig` 同步（未知值在加载期拒绝）。
- 新增 `backend/internal/toolkit/tools/code_common.go`：统一返回结构（`source` / `confidence` / `version` / `range` / `truncated` / `next_cursor` / `explanation` / `degraded`）、`CodeIndex` 窄读接口与 `CodeIndexResolver`、符号/引用结果形状、fallback 包装（`source=fallback` + `fallback.tool/reason/output`）。
- 新增 5 个工具文件：`code_search.go`（symbols_fts；on 档零命中补一次 grep；shadow 档算候选但返回 grep 结果）、`code_inspect.go`（按符号读取，正文经 view）、`code_navigate.go`（definition / members / refs）、`code_references.go`（按符号身份的引用，含 name-only 回退）、`code_callers.go`（kind=call 子集）。
- `backend/internal/toolkit/tools/view.go`：可选 `symbol` 参数（索引命中按符号范围读取；无索引且有 file_path 时按行范围读取并附 note，否则明确报错并提示 grep）。
- `backend/internal/tools/code_index_resolver.go`：按 workspace 懒加载只读索引（`OpenStore(readOnly=true)` 不参与 owner 仲裁；进程内按 db 路径缓存；ctx workspace root 优先）；`manager.go` 注册门控 + 给 view 注入 resolver。
- 配置启用：`backend/configs/runtime.yaml` 与 `backend/configs/runtime.win7.yaml` 显式写入 `knowledge.code_tools: on`（随附配置启用；两档取值一致，避免"代码默认 off、随附配置漏配"的静默降级）；项目层 `.aicli/runtime.yaml` 同步开启。
- 工具描述更新：`grep` / `view` / 5 个 code.* 明确“符号级问题优先 code.*；文本/配置/日志用 grep”的分工。
- 偏差（登记）：① 工具名用下划线而非 `code.`（provider 函数名约束）；② `configs/model_cards.yaml` 无工具面清单（模型能力卡），系统提示的实际载体是工具描述，未改该文件；③ `cmd/toolkit-mcp-server` 无 workspace/knowledge 上下文，未注册 code.*；④ `version` 字段留空（版本向量落地于 Phase 5）。

### Verified

- 单测：`code_tools_test.go` 10 例（索引路径 / 无索引 fallback / shadow 档 / view --symbol 两口径 / 统一结构字段）、`code_index_resolver_test.go` 3 例（注册门控 off|on、解析器可用性、ctx workspace）、`internal/config` 1 例；`go build ./...` OK；`knowledge` / `contextmgr` / `config` / `tools` / `toolkit` 全绿（另修 `TestPlannerLookupShape` 以匹配会话级读取口径）。
- 配置侧：新增 `TestShippedRuntimeConfigsEnableCodeTools`（随附两档配置必须开启 code_tools，且与 `TestWin7RuntimeConfigUsesSharedSessionDatabase` 的全量等价断言一致）。
- E2E（真实会话，`knowledge.mode=on` + `code_tools=on`，独立 owner 进程 + 远程 invoke）：模型实际调用 `code_search` → `code_callers`（runtime-events 的 `tool_name` 事件为证），工具结果信封含 `"source":"index"`；回答给出索引口径事实（`planLookupQuery` 定义于 `planner.go:247`、调用点 `planner.go:209`）；`/exit` 后锁文件释放、进程正常退出。
- E2E 复核（配置启用后，`code_navigate`）：工具面含 5 个 code.*；模型调用 `code_navigate(direction=members)`，结果信封 `"source":"index"`、`"confidence":1`，回答给出 planner.go 的 22 个符号（类别 + 行范围）；runtime-events `tool_name=code_navigate`。

### Notes

- 默认值零变化：`code_tools` 缺省 off 时不注册任何 code.*（与改动前逐字节一致）；本仓库 `.aicli/runtime.yaml` 已显式开启用于灰度验证。
- 遗留：收益类验收（token ↓≥40% / fallback ≤30% / 调用总数不增加）与 M3 判定待测量轮；`next_cursor` v1 恒空（不分页）。

---

## 2026-09-30 — 修复：会话级召回口径 + 退出路径知识层锁释放

真实会话实测暴露两处缺口并修复（Phase 2 W3/W4/W6 相关）：普通 chat 会话的 `TaskID` 是会话级回退（`loop.go` `taskID := sessionID`），而探索记忆按"宿主无任务语义 → task_id 为空"落库（W1 DTO 回退口径）；两条口径不一致使 Planner 用 `task_id = sessionID` 精确匹配永零命中，跨任务路径又要求 target 精确匹配（自然语言查询同样零命中）→ 真实会话召回恒空。另一处：`cmd/aicli/commands` 退出清理注册表是单槽位，chat 三次 `registerExitCleanup` 后只剩最后一个（调试面注销），`/exit` 后 `knowledge.db.lock` 不释放，只能等下一次进程做陈旧锁抢占（最长 2h reader 降级窗口）。

### Changed

- `backend/internal/knowledge/exploration.go` / `store_sqlite_exploration_read.go`：`ExplorationNodeQuery` 新增 `SessionID`；`LookupExplorationNodes` 增加"会话级工作集"读取路径（`s.session_id = ? AND COALESCE(s.task_id,'') = ''`；任务行仍走 TaskID 路径）。
- `backend/internal/knowledge/planner.go`：`PlanInput` 新增 `SessionID`；新增 `planLookupQuery` 三条路径选择（任务 → 会话 → 跨任务精确 target）；同工作集作用域判定放宽为 TaskID 或 SessionID 任一非空。
- `backend/internal/contextmgr/knowledge.go`：接线时把会话级回退（TaskID == SessionID）折叠为 task_id 为空 + SessionID，避免用会话 id 匹配从未落库的 task_id 行。
- `backend/cmd/aicli/commands/output.go`：退出清理注册表由单槽位改为列表（后登记先执行、幂等），修复 `/exit` 后知识层锁与会话资源不释放。

### Verified

- 单测：`go test -count=1 ./internal/knowledge/ ./internal/contextmgr/ ./internal/agent/` 全绿（新增 3 用例：会话级读过滤 / Planner 路径选择 / contextmgr 接线口径）；`go build` OK。
- 真实 E2E（修复版二进制，独立 owner 进程 + 远程 invoke）：第 1 轮 grep/view → 探索记忆落库（file 节点 conf=1.0、use_count=2）；第 2 轮同会话自然语言查询 → **注入命中**：请求体含 `Exploration memory signals: 3 reusable item(s); verify_required=0`，运行时事件 `context.knowledge.injected`（count=3, mode=signals）；`/exit` 后锁文件被移除、进程正常退出。
- 存量失败（与本次改动无关，回退本次 `output.go` 后对照复现）：`cmd/aicli/commands` 的 `TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning` 与 `TestLateReasoningBarrierWithholdsLongAssistantFromNativeHistory`。

### Notes

- 未改任何默认值：注入档位仍由 `knowledge.mode`（on → signals）驱动，`KnowledgeMode` 默认 off 的可逆性不变。
- 未 git commit；`internal/knowledge/**` 与 `internal/contextmgr/knowledge.go` 为未跟踪文件，按现状留待统一提交。

---

## 2026-09-30 — W1 补登（探索记忆持久化层）

W1（Phase 2 工作流）**补登**：Phase 2 总收口核验如实报告 W1 表行未见 ✅、W1 小节无状态块、本 CHANGELOG 无 W1 独立条目（见下方「Phase 2 完成」条目 Notes）；本轮按 `06` §4 W1 小节规划原文逐项复核（文件 / 函数 / 测试）后补登。本轮为**文档补登**：不改代码、不 git commit、不触碰 `reports/` 与 `adr/*`。

### Changed

- `06` §4 Phase 2：W1 表行 → ✅ **已完成（2026-09-30；补登日 2026-09-30）** + 验证摘要；W1 小节补「状态 / 落地 / 验证记录 / 登记注记（补登说明）」。
- 实现落点（父会话已完成，本轮仅核验与登记）：新增 `backend/internal/knowledge/{exploration.go,store_sqlite_exploration.go,store_sqlite_exploration_read.go,version_hash.go}`；修改 `backend/internal/knowledge/store.go`（仅追加 6 个接口方法，+32 行、0 删改）；零迁移（未新增 0003）。

### Verified

- 登记轮独立复跑：`gofmt -l` 7 文件 clean；`go test -count=1 ./internal/knowledge/...` **ok 1.990s**；W1 定向 18 用例全 PASS（0.518s）：枚举闭集 / ID 稳定 / scope 校验 / `WorkspaceVersion` 对文件哈希·adapter·schema 敏感且同输入幂等 / 写入幂等与计数 / `knowledge_version` 必填 / reader 写 `ErrReadOnlyStore` / `ON DELETE CASCADE` / workspace 隔离 / 读过滤·limit 默认·排序稳定 / latest 排序。
- DoD ①–⑤ 逐项核验通过：零迁移可读写三表；同 target 幂等（`use_count+1`、`last_used_at` 刷新）；节点 `knowledge_version` 非空；reader 写返回 `ErrReadOnlyStore`；`go test ./internal/knowledge/...` 全绿。

### Notes

- **证据来源与日期口径**：证据 = `06` §4 W1 规划条目 + 代码检视（`exploration.go` / `store_sqlite_exploration.go` / `store_sqlite_exploration_read.go` / `version_hash.go` / `store.go` diff）+ 登记轮测试复跑。W1 七文件均未提交（`git status` untracked、`git log --all` 对相关路径无记录），完成日期取文件 mtime（2026-09-30 06:43–06:50），与 Phase 2 其他工作流同日；**补登日 2026-09-30**。
- 证据不足项：无（W1 规划 5 个文件落点 + 3 个测试文件逐项对号，均查有实现与测试）。
- 去重核验：补登前检索 `06` / 本 CHANGELOG 无 W1 完成条目（无重复登记）；对 `06` / 本 CHANGELOG 的编辑均先重读目标行。

---

## 2026-09-30 — Phase 2 完成（认知层激活，W1–W7）

Phase 2（Exploration Memory + Context Planner）文档总收口：W1–W7 实现全部落地并登记（W7 分 W7a 激活装配 / W7b 测量段两切片）；`06` §1.1 / §4 与 `04` §5 状态位更新为「实现完成（2026-09-30）」。本轮为**文档收口**：不改代码、不 git commit、不触碰 `reports/` 与 `adr/*`。

### Changed

- `06` §4 Phase 2：W7 表行 → ✅ **已完成（2026-09-30，两切片验证摘要 + 遗留指向）**；W7 小节补「状态 / 落地 / 验证记录 / 登记注记」（W7a / W7b 两段）。
- `06` §1.1：Phase 2 状态行 → **实现完成（2026-09-30）** + 验证摘要（复跑数据 + 有界演练 n=385 + 遗留指向）；`06` §0 一句话状态 / §1.2 尾注同步状态位（「待开工」→「实现完成」，仅状态位）。
- `04` §5：Phase 2 状态由「未开始」→ **实现完成（2026-09-30）**（W1–W7 全覆盖 + 遗留项；交付 / 验收门槛 / 回滚原文未改）。
- 实现落点（父会话已完成，本轮仅登记）：W7a 激活装配（`contextmgr.KnowledgeModeForLayerMode` + agent / runtimeapi / aicli 装配链，默认 off 零行为变化）；W7b 测量段（`backend/internal/knowledge/exploration_report.go` 装置 + `CalibrateTokenBudget`、`backend/internal/contextmgr/knowledge_calibration_test.go`）。

### Verified

- W7a 复跑：`knowledge` 3.8s / `contextmgr` 1.0s / `agent` 17.5s / `runtimeapi` 51.8s 全 ok；`go build ./...` OK；默认 off 零行为变化。
- W7b 复跑：`knowledge` 1.79s / `contextmgr` 0.41s 全 ok；有界演练 n=385：median≈420、95% CI [416,424]、p95 450、max 498 → 建议预算 500、保留 `DefaultKnowledgeTokens=800`（=1.6× 余量）。

### Notes

- **遗留（登记）**：① 真实 on-mode A/B ≥20 任务实测待跑（报告落点 `reports/phase2_exploration_report.md`，由并行会话维护）；② `verify_requested` 消费方未接线（W7 规格外缺口）；③ `broad` 档阈值校准留后续；④ `06` §9 #21 已确认为预存、#22 flaky（均不阻塞）。
- **W1–W7 表行核验（如实报告）**：W0 / W2–W6 表行 ✅ 且小节齐备；**W1 表行未见 ✅、W1 小节无状态块、本 CHANGELOG 无 W1 独立条目**（与「W1–W6 已登记 ✅」口径不符）——未伪造补登，留父会话裁决。
- 去重核验：登记前检索 `06` / 本 CHANGELOG，无 W7 / Phase 2 完成条目（无重复登记）；对 `06` / `04` 的编辑均先重读目标行。

---

## 2026-09-30 — W6 完成（多 Agent 写入门禁）

W6（Phase 2 工作流）完成：探索记忆写入侧落地来源门禁（未标注 / 只读子代理 / 只读会话 fail-closed 零写入）与任务作用域隔离（`task_id` 空时仅 `confidence ≥ 0.90` 落库）；`agent` 循环置位 `KnowledgeWrite` 写意图；runtimeapi / aicli 把子代理 / 只读标志与 `active_team_task_id` 透传到观察链。本轮为**文档登记**：不改代码、不 git commit。

### Changed

- `06` §4 Phase 2：W6 表行 → ✅ **已完成（2026-09-30，含验证摘要）**；W6 小节补「状态 / 落地 / 验证记录」与「登记注记（偏差与缺口）」。
- `06` §9：#21 条目更新为「**确认预存**（HEAD 干净 worktree 复现，与 W6 无关）」（#22 保持原状）。
- 实现落点（父会话已完成，本轮仅登记）：修改 `backend/internal/knowledge/exploration_recorder.go`（`ObservationSource` 枚举 + `WriteAllowed()` + `ObservationSourceFor`；`Record` / `process` 双层 fail-closed 门禁；`applyCrossTaskWriteFloor` 写入侧 ≥0.90；`ObservedCall.TaskID → exploration_sessions.task_id`）与 `backend/internal/knowledge/shadow.go`（`ObservedCall` 增 `TaskID` / `Source`，additive）；新增 `backend/internal/agent/loop_knowledge.go`（`knowledgeWriteIntent` / `agentIsReadOnly`）+ 修改 `backend/internal/agent/loop.go`（置位 `KnowledgeWrite`）；编排透传：runtimeapi `session_runtime_support.go`、aicli `chat_actor_host.go`（子代理 / 只读标志 + `active_team_task_id`）。
- 测试（父会话已完成）：新增 `backend/internal/knowledge/exploration_scope_test.go`、`backend/internal/api/runtimeapi/subagent_knowledge_write_guard_test.go`、`backend/internal/agent/loop_knowledge_test.go`；既有接线测试随签名更新。

### Verified

- 父会话独立复跑：`gofmt` clean；`go vet` 5 包通过；`go test -count=1`：`knowledge` 3.6s / `agent` 15.7s / `contextmgr` 0.93s 全 ok；`go build ./...` OK。子代理另跑：`runtimeapi` 全量 ok（40.9s）。
- 门禁矩阵：未标注 `""` → 不写｜`main_session` → 写｜`subagent` → 写（per-task）｜`subagent_read_only` → 不写｜`read_only_session` → 不写；`task_id` 空时仅 ≥0.90 落库。
- **#21 结论更新**：`TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning` 在 HEAD 干净 worktree 复现同样失败 → **确认预存**（与 W6 无关）；#22 单跑通过（flaky 原状）。

### Notes

- **偏差/缺口（登记）**：① 读侧 ≥0.90 已由 W3/W4 落地（W6 只补写入侧硬拦 + 作用域隔离）；② 只读标志既有（`SubagentTask.ReadOnly` / `ToolExecutionPolicy.ReadOnly`，仅透传、未新增枚举）；③ `KnowledgeWrite` 为启发式（未知 → 按写语义），留 W7 校准（`04` §7.6）。
- **缺口（W7）**：运行时装配默认 off；`verify_requested` 消费方未接线；A/B 测量与 G1–G4 复算。
- 与并行写者核验：登记前检索 `06` / 本 CHANGELOG 无 W6 完成条目（无重复登记）；对 `06` 与 CHANGELOG 的编辑均先重读目标行。

---

## 2026-09-30 — W5 完成（contextmgr 集成）

W5（Phase 2 工作流）完成：`contextmgr` 新增 `KnowledgeMode`（off/signals/broad，默认 off、未知值 fail closed）与 `Strategy` 知识旋钮（`MinKnowledgeQueryLength` / `ReuseConfidenceFloor`），把 W4 `knowledge.Planner` 的 `Reuse` 项按档位注入上下文（二次 stale/version + floor 过滤、`signals` 摘要 / `broad` 条目、token 预算截断、metadata 与事件）；`off` 零调用零注入且与基线逐字节一致（G4 可逆主承载）。本轮为**文档登记**：不改代码、不 git commit。

### Changed

- `06` §4 Phase 2：W5 表行 → ✅ **已完成（2026-09-30，含验证摘要）**；W5 小节补「状态 / 落地 / 验证记录」与「登记注记（偏差与缺口）」。
- 实现落点（父会话已完成，本轮仅登记）：新增 `backend/internal/contextmgr/knowledge.go`（档位归一化 fail-closed；`buildKnowledgeMessage`：off 短路、查询长度短路、同任务/跨任务 scope、`KnowledgeWrite` 透传；二次 stale/version 过滤 + `ReuseConfidenceFloor`；`signals` 摘要 / `broad` 条目渲染与 `DefaultKnowledgeTokens=800` 预算截断；`knowledge_items` / `knowledge_verify_targets` metadata；`knowledge_stale_item_injected` 恒 0）；修改 `backend/internal/contextmgr/manager.go`（+134/-18：`Strategy` 三旋钮默认 off、`Manager.Knowledge knowledge.Planner`、`BuildInput.KnowledgeWrite`、Build 注入 append-only + 活动回合回放抑制、`context.knowledge.injected` / `verify_requested` / `degraded` 事件、off 零新增 key）；新增 `backend/internal/contextmgr/knowledge_test.go`（9 用例 + `var _ knowledge.Planner = (*knowledge.Layer)(nil)` 编译期钉齐）；修改 `backend/internal/contextmgr/manager_test.go`（追加 `TestStrategyKnowledgeDefaultsAndOverrides`，+31 行）。

### Verified

- `gofmt` clean；`go vet`（contextmgr / knowledge）通过；`go build ./...` OK。
- `go test -count=1`：`contextmgr` 0.42s / `knowledge` 1.82s 全 ok（父会话；登记轮独立复跑 0.405s / 1.852s）。
- 四态证据：off（calls=0、与基线 `DeepEqual`、零 `knowledge_*` key、开→关可逆）；Reuse（`signals` 摘要 / `broad` 条目含 reason/version/confidence）；Verify/Provisional（`knowledge_verify_targets` + `context.knowledge.verify_requested` 事件）；Degraded（零注入 + `context.knowledge.degraded` + `knowledge_degraded=true`）；`knowledge_stale_item_injected` 恒 0。
- 子代理另跑：`agent` ok；`runtimeapi` 一处 flaky（并行写者 parked-turn 测试，单跑通过，与 W5 无关）。

### Notes

- **偏差/缺口（登记）**：① 档位命名收口为 `off | signals | broad`（W5 小节原「`on` 下」表述由三档取代；`04` §4.5 即此三档）；② `Manager.Knowledge` 落为 `knowledge.Planner` 接口（`04` §7.2 原文 `*knowledge.Planner`），`knowledge_test.go` 编译期钉齐 `*knowledge.Layer` 可直接注入；③ broad 预算用常量 `DefaultKnowledgeTokens=800`（1 rune ≈ 1 token 上界；未新增 `Budget` 字段），`context_items` 落库留 Phase 6。
- **缺口**：W6 写入门禁未落地（`BuildInput.KnowledgeWrite` 已透传、调用方未置位）；运行时装配默认 off（W7 激活切片注入 `*knowledge.Layer`）；`verify_requested` 消费方（agent 循环 grep/view 验证读取）未接线；预算 / 阈值 / 长度参数校准留 W7。
- 与并行写者核验：登记前检索 `06` / 本 CHANGELOG 无 W5 完成条目（无重复登记）；对 `06` 与 CHANGELOG 的编辑均先重读目标行。

---

## 2026-09-30 — W4 完成（Planner）

W4（Phase 2 工作流）完成：`knowledge.Planner` 落地——`Plan{Reuse, Explore, Degraded, Reason}`、稳定 Reason token、纯函数 `EvaluatePlan` 决策矩阵与 store 注入版 `Planner`（Degrade-Not-Fail），`Layer.Plan` 层入口（nil/off 不 panic、只读）。本轮为**文档登记**：不改代码、不 git commit。

### Changed

- `06` §4 Phase 2：W4 表行 → ✅ **已完成（2026-09-30，含验证摘要）**；W4 小节补「状态 / 落地 / 验证记录」与「登记注记（偏差与缺口）」。
- 实现落点（父会话已完成，本轮仅登记）：新增 `backend/internal/knowledge/planner.go`（`PlanInput` / `Plan` / `ReuseItem` / `ExploreItem`；稳定 Reason token；纯函数 `EvaluatePlan`：`<8 rune` 零 store 调用、无候选 Explore、同任务 `<0.90` Reuse+Verify / `≥0.90` Reuse、写 `write_verify`、跨任务 `cross_task_verify`、版本不匹配或未知 Explore、TTL 滞后 `version_observation_lag`；`Planner` 接口 + `NewPlanner`（窄读接口注入；store nil/错/超时/取消 → `Degraded` + `error=nil`）；`versionCache` 30s TTL）；修改 `backend/internal/knowledge/knowledge.go`（`Layer.Plan`：nil/off/无 store → 空 Plan + `disabled`，不 panic；只读 workspace；Current 空时采样版本）；新增 `backend/internal/knowledge/planner_test.go`（决策矩阵 19 用例 + 其余 12 个测试函数，共 13 个 `Test*`）。

### Verified

- `gofmt -l internal/knowledge/` clean；`go vet ./internal/knowledge/` 通过。
- `go test -count=1 ./internal/knowledge/` **ok**（父会话 2.168s；登记轮独立复跑 2.281s）；`-v` 实跑：决策矩阵 19/19 子测试 PASS、`planner_test.go` 13 个测试函数全 PASS。

### Notes

- **偏差/缺口（登记）**：① `MinKnowledgeQueryLength` 落 `planner.go`（`DefaultMinKnowledgeQueryLength=8` rune，`PlanInput.MinQueryLength` 可覆盖），归 W5 `Strategy` 正式收口；② 版本采样缓存放 Layer 级（`versionCache` 30s TTL），`ObservedAt` 留给 W3 Gate 做 TTL 滞后兜底；③ `Planner` 接口保留 `error` 返回位，实现永不 error（Degrade-Not-Fail）。
- **缺口（W5 注入口径）**：只注入 `Reuse`；`Verify` / `Provisional` 需 metadata `reason` + 验证读取；`Degraded` 零注入；W5 二次 stale/version 过滤（为 Phase 6 `stale_item_injected=0` 预留）。
- 与并行写者核验：登记前检索 `06` / 本 CHANGELOG 无 W4 条目（无重复登记）；测试计数以登记轮 `-v` 实跑为准（矩阵 19 子测试 / 13 个 `Test*`，父会话口径 20/12 按实跑修正）。

---

## 2026-09-30 — W3 完成（Reuse Gate）

W3（Phase 2 工作流）完成：`04` §4.4 的 confidence 可执行定义与 Reuse Gate 阈值判定落地（纯函数、无 IO、可复算），`knowledge.planner.*` 保守默认阈值与 YAML 注释模板就位。本轮为**文档登记**：不改代码、不 git commit。

### Changed

- `06` §4 Phase 2：W3 表行 → ✅ **已完成（2026-09-30，含验证摘要）**；W3 小节补「状态 / 落地 / 验证记录」与「登记注记（偏差与缺口）」。
- 实现落点（父会话已完成，本轮仅登记）：新增 `backend/internal/knowledge/confidence.go`（`SourceWeight` 闭集 / `AgreementFactor` / `StalenessPenalty` / `AmbiguityPenalty` / `ComputeConfidence` / `CompareKnowledgeVersion` / `EvaluateReuseGate`：`explore` / `reuse_verify` / `reuse` + `Verify` 强制条件（写 / 跨任务 ∨ conf < 0.90 ∨ TTL 滞后）；`ExplorationNode.GateInput` 供 W4）；新增 `confidence_test.go`（表驱动 21 用例）；修改 `backend/internal/knowledge/config.go`（`PlannerConfig` 规格 4 键 + additive `explore_below=0.50`、`Normalize` / `Validate`、`ReuseFloor`、`Config.Planner`）；修改 `backend/configs/{runtime.yaml,runtime.win7.yaml}`（`knowledge.planner.*` 注释模板，缺省即代码值）。

### Verified

- `gofmt` clean；`go vet`（knowledge / config）通过；`go build ./...` OK。
- `go test -count=1`：`knowledge` 2.13s / `config` 1.25s **全 ok**；`confidence_test.go` 21 用例全 PASS；登记轮抽检 `-run 'Confidence|Reuse|Planner|Staleness|SourceWeight|CompareKnowledgeVersion'`（knowledge）复跑 **ok（0.185s）**。

### Notes

- **过程记录**：`TestPlannerConfigYAMLRoundTrip` 初跑失败（fixture 未注入 workspace）→ 一行修复 `WithWorkspace("ws")`，复跑全绿。
- **偏差/缺口（登记）**：① 新增 `explore_below=0.50` 键（规格 4 键装不下 <0.50 下界，additive）；② 未加加载期校验（与 Alpha 一致，`Open` 时校验）；③ 文档状态（`06` §1.1 / `04` §5）留 W7 验收后更新。缺口：W4 需自带 `VersionObservation`（含 `ObservedAt`）才能吃到 TTL 兜底；写入侧 confidence 是否改用 `ComputeConfidence` 留 W6/W7；W5 需把 `Provisional` 写 `context_items.reason`；W7 用 `Usable` 口径复算 `unsafe_reuse_count=0`。
- 与并行写者核验：登记前检索 `06` / 本 CHANGELOG 均无 W3 条目（无重复登记）。

---

## 2026-09-30 — W2 完成（Recorder 自动采集 + 三入口接线）

W2（Phase 2 工作流）完成：`OnToolObserved` 只读钩子接上探索记忆采集器，`grep` / `view` 的最终结果异步写入 `exploration_sessions/nodes/edges`，三入口（runtimeapi / aicli TUI+ACP / runtime-server）全部接线。本轮为**文档登记**：不改代码、不 git commit。

### Changed

- `06` §4 Phase 2：W2 表行 → ✅ **已完成（2026-09-30，含验证摘要）**；W2 小节补「状态 / 落地 / 验证记录」与「登记注记（偏差与缺口）」；「现状核对」同步（通道条目更新、缺口 ① 关闭、W2 缺口交叉引用）。
- 实现落点（父会话已完成，本轮仅登记）：新增 `backend/internal/knowledge/exploration_recorder.go`（异步有界采集：grep/view → session + query/file 节点 + `derived_from` 边；query 仅落 `query_hash`；confidence file 1.0 / query 0.9；`knowledge_version` TTL 30s；同 target 去重交 store 主键；队列满丢弃；nil-safe）；`activation.go` 增 `Recorder()`（仅 `mode=shadow|on` 且 owner 非 nil；off/reader nil）；三入口接线（runtimeapi `session_runtime_support.go:4575/4854` + `handler.go:746`；aicli TUI+ACP 共用 `chat_actor_host.go:1720/2638`，ACP 见 `agent_stdio.go:988`；runtime-server `main.go:1321` + `knowledge_boot.go:94`）。
- 测试（父会话已完成）：4 新增——`exploration_recorder_test.go`、runtimeapi `exploration_wiring_test.go`、aicli/commands `exploration_wiring_test.go`、runtime-server `knowledge_recorder_wiring_test.go`；1 修正——aicli/commands `chat_shadow_wiring_test.go`（无账本时 hook 仍须接线）。

### Verified

- `gofmt` clean；`go vet` 四包通过。
- `go test -count=1`：`knowledge` 3.2s / `runtimeapi` 39.9s / `runtime-server` 0.75s **全 ok**；`-run 'Exploration|Shadow|ActivationRecorder'`（knowledge + aicli/commands）**全 PASS**。

### Notes

- **偏差/缺口（登记）**：① `OnToolObserved` 无 task/turn 上下文 → session 级回退（`task_id` 为空，不编造任务语义）；② 无跨调用批量（异步有界 + 队列满丢弃）；③ 计划中的 `loop_observe_test` 扩展未做；④ Phase 级文档状态（`06` §1.1、`04` §5）留 W7 验收后更新；⑤ 缺口：W6 写入门禁未落地、`knowledge_version` TTL 滞后（缓存 ≤30s，stale 兜底判定在 W3）、Recorder 显式装配仅 runtime-server（aicli 经 `session.Knowledge.Recorder()`）。
- 与并行写者核验：W2 文件集与 `AgentSessionObligations` 接线无重叠；`04` §5 Phase 2 未列 W 清单，按登记口径**不动**（Phase 2 状态仍为「未开始」，Phase 级更新留 W7）。

---

## 2026-09-30 — W0 完成（ADR-0008 收口）：新列 + 三入口 live 写入

W0（Phase 1 遗留项，非 Phase 2 交付）完成：`exploration_attribution` 的 file-level 两列已落地，三入口 live 写入已接线，ADR-0008 §10 两项 Open 关闭。本轮为**文档登记**：不改代码、不 git commit。

### Changed

- `adr/0008-grep-coverage-file-level.md` §8.1：原"仍未完成"改为**已落地（2026-09-30）**并新增「落地记录（2026-09-30）」；§10："新列实现"与"三入口 live 写入"两行标 ✅ **已完成（2026-09-30）**（其余 follow-up 保留）。
- `06` §4 Phase 2 开工规划：W0 表行 → ✅ **已完成（2026-09-30，含验证摘要）**；「现状核对」缺口 ② 标记关闭；W0 小节补状态 / 落地 / 验证记录。
- `06` §9 待办表新增 **#21**（viewport 测试确定性失败，疑似预存）与 **#22**（team autostart 测试波动）。
- `adr/README.md`：`0008` 索引行补"落地完成（2026-09-30：新列 + 三入口 live 写入）"备注。
- 实现落点（父会话已完成，本轮仅登记）：`entity` 两列 `BaselineFilesN` / `OverlapFilesN`；`usageledger` DDL + 旧库 `ALTER` 补齐 + 读写；`knowledge/shadow.go` file-level 落列与 `usable` 重算；`attribution.go` file-level 复算族；三入口接线（runtimeapi `handler.go:734`、aicli `chat_actor_host.go:1707/2614`、runtime-server `main.go:1317`）。

### Verified

- `gofmt -w` 修复 4 文件后 **clean**；`go vet` 六包通过。
- `go test -count=1`：`usageledger` 11.9s / `knowledge` 2.0s / `runtimeapi` 41.6s / `runtime-server` 0.5s **全 ok**；`cmd/aicli/commands -run Shadow` **全 PASS**。

### Notes

- **范围外失败 2 条**（登记于 `06` §9 #21/#22，不阻塞 W0）：① `TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning` 确定性失败（viewport 缺 `history user 6`；测试文件自 2026-09-24 未变、域不相关，疑似预存）；② `TestAICLIChatActorExecutor_AutoStartTeamMarksBaseSessionRunningUntilSettled` 波动（全量跑失败 / 单跑通过）。

---

## 2026-09-29 — Phase 2 开工规划落档（Exploration Memory + Context Planner）

`06` §4 Phase 2 追加子节 **「Phase 2 开工规划（2026-09-29）」**：工作流 W0–W7（按依赖排序，每条含目标 / 文件落点 / 测试清单 / 验收映射 G1–G4 / 前置依赖 / 风险）、第一步最小切片 S1（标注"可在下一轮直接开工"）、与 ADR-0008 配套项（新列 + live 写入）的先后关系。

### Changed

- `06` §4 Phase 2：追加开工规划子节；Phase 2「状态」行：`门禁已解除，待开工` → **`规划完成（2026-09-29），待开工`**（并指向规划子节与最小首片 S1）。
- `06` §1.1 Phase 2 行同步为 **规划完成（2026-09-29），待开工**，指向 §4 Phase 2 规划子节。

### Notes

- 本轮为**只读核对 + 文档落档**：不写业务代码、不改 `04` / `adr/*`、不 git commit。
- 与并行写者核验：落档前检索 `docs/knowledge_Layer/` 无既有 Phase 2 开工规划（无重复/覆盖）；对 `06` 的编辑均先重读目标行。
- 规划发现（缺口，已写入 `06` 规划子节"现状核对"）：`exploration_sessions/nodes/edges` 建表已在 `knowledge/migrations/0001_init.sql`（但无 Go 读写代码）；`contextmgr` 无 `KnowledgeMode` / `Knowledge` 字段；工作区级 `knowledge_version` 无生成器；ADR-0008 的 `baseline_files_n` / `overlap_files_n` 新列与三入口 live 写入仍未实现（列为 W0，建议先于 W2 收口）。
- 首片 S1 = W1 前半（探索记忆存储层 + `WorkspaceVersion` + 测试），零迁移、可在下一轮直接开工。
- 旁注（非 Phase 2 阻塞，待复核）：`backend/internal/knowledge/config.go` 的 `DefaultMaxDBSizeMB = 200` 与 `04` §7.4 校准值 512MB 不一致（Phase 5 GC 触发点）。

---

## 2026-09-29 — ADR-0004 Accept（陈旧索引下 `code.*` 工具面）：P2 门禁解除

owner 授权代改并记录裁决（先例：ADR-0008 / 0009）：ADR-0004 由 `Proposed` 改为 **`Accepted`**。

### Changed

- `adr/0004-stale-index-tool-surface.md`：`Status: Proposed` → `Accepted`，加注"2026-09-29，项目 owner 授权代改（先例：0008/0009）"；§10 追加接受记录；**正文其余不动**（Accepted 后不可改正文）。
- `adr/README.md`：§5 索引表 `0004` 行（Proposed → **Accepted**）+ 新增"接受与落地状态（2026-09-29）"段落；2026-09-28 段落的剩余 Proposed 列表尾注同步。
- `06` §0 一句话状态 / §1.1 Phase 2 行（未开始 → **门禁已解除，待开工**）/ §1.2 阻塞清单 / §3 ADR 表 / §4 Phase 2 前置与状态；`README.md` §0 当前阶段 / §2 文档状态表 / §4 进度表 Phase 2 行同步。
- `supplement/05` §9 表第 4 行、`GLOSSARY.md` §4 的 `S_fresh` / `S_max` 条目（"须由 Phase 0 校准" → Gate = `Phase2-start`，补齐 2026-09-21 Gate 修订）同步。

### Notes

- 关键决策（陈旧索引下 `code.*` 工具面口径）：按陈旧度**分级注册**——新鲜全开；中等只开定义类 `code.find_symbol` / `code.search`；过旧全关；关系类 `code.find_refs` / `code.callers` / `code.impact` 陈旧时**不注册**（杜绝静默错误）。schema 恒定、描述可变；三个逃生舱开关。
- `S_fresh`（60s）/ `S_max`（15min）阈值：**Gate = `Phase2-start`**（2026-09-21 由 `Phase0-baseline` 修订），初值**不阻塞 Accept**；§10 其余跟进项同 Gate。
- **P2 门禁解除**（ADR-0008 §8.1 的"P2 进入条件 = ADR-0004 Accept"已满足），Phase 2 待开工；未开工事实不变（`04` §5 Phase 2 状态仍为"未开始"，本轮未改）。

---

## 2026-09-29 — ADR-0008 裁决落地（file-level 口径）+ α 定稿与 Phase 1 主门槛复核通过

owner 授权代改并记录裁决：ADR-0008 采纳**选项 A**——grep 通道主判据 = **file-level 覆盖**（行级保留为诊断；view 口径不变）。

### Changed

- `adr/0008-grep-coverage-file-level.md`：`Proposed` → **`Accepted`**；新增 §8.1 落地与阈值定稿、§10 行状态更新；`adr/README.md` 索引与说明同步；`adr/0003` §10 的"α 与 Phase 1 门槛数值"待办标记完成。
- `reports/phase1_shadow_report.md` §1 / §4.4 / §5.1：口径裁决依据（file-level mean 31.83 %、p90 100 %、usable@0.8 26.76 %、answerable 49.38 %；行级 0.47 % 仅作诊断）、α 定稿与 Phase 1 复核结论落稿。
- `06` §1.1 / §1.3 / §3 ADR 表 / §4 Phase 1 状态行、`04` §5 Phase 1 状态、`README.md` §4 进度表与说明同步。
- config：`knowledge.shadow.alpha` 字段已存在且默认即建议值 0.8，**无需改动**。

### Verified

- **α 定稿 = 0.8**；**Phase 1 门槛 = 合并 M1 ≥ 0.31**（95 % CI 下界，α=0.8）。
- **Phase 1 主门槛复核 = Pass**：grep file-level `usable@0.8 = 26.76 %`（n=213）、view `M1 = 48.41 %`（n=157）、合并 **M1 = 133/370 = 35.95 % ≥ 0.31**；复算：报告 §6 第 2 步（`TestPhase1ShadowReplay`，`KNOWLEDGE_SHADOW_ALPHA=0.8`）。
- **P2 进入条件 = ADR-0004 Accept**；ADR-0008 的"新列实现 + 三入口 live 写入新列"仍为 Open。

---

## 2026-09-29 — ADR-0009 落地（v1 表集口径，Accepted）：不变量检查器 5/5 PASS

ADR-0009 由 `Proposed` 改为 `Accepted`（项目 owner 授权代改，先例见 2026-09-28 条目），并完成 §4 落地与 `06` §9 #19。

### Changed

- `04` §0.3：v1 表集声明 `≤16` → **`≤23`**（= `02` §8【v1 core】的 22 表 + `symbol_fts` 虚表；不含 `schema_migrations` / extension / deferred）。
- `04` §4.3 标题 → "P0/P1 最小数据模型（16 张；v1 core 的子集）"；其余 15 处 DDL（14 表 + FTS 虚表，含索引）**引用化**，`04` 彻底无 DDL（#19）。例外映射（无 `02` DDL 的三项）：`schema_migrations` → `internal/migrate`、`symbol_aliases` → `supplement/01` §1.3、FTS 虚表 → `02` §73 的规范名 `symbol_fts`（旧稿名 `symbols_fts`）。
- `02` §8：三分组落地——【v1 core】23 名 /【extension】1 名（`index_jobs`）/【已推迟】5 名（`branches`/`inheritance`/`dependencies`/`language_projects`/`events`；删除名以删除线或注记出现，不进入名字集合）；删除"本块与下方 DDL 不一致"旧注记。
- `README.md` §3：v1 表集转述同步为 ≤23（见 `04` §0.3）；`adr/README.md`：`0009` 状态同步为 **Accepted**。
- `06` §9：#2 / #3 / #7 / #19 / #20 标记完成并补结果；相邻待办仅剩 #5。
- ADR-0009 §8.1 / §10：追加落地记录（含例外映射与行号提示）。

### Verified

- `cd backend; go run scripts/check_knowledge_doc_invariants.go` = **5/5 PASS**：I1（23 = 23）、I2（0 越位 DDL）、I3（0 漂移）、I4（三分组 23+1+2）、I5（声明 23 = 实际 23）。

---

## 2026-09-29 — ADR-0009 起草（v1 表集口径裁决，Proposed）

回应 ADR-0007 §10 的 `Phase1-start` 待办（I5：`04` 声明 ≤16 vs `02` 实际 23）与 `06` §9 待办 #2/#7/#20。

### Added

- ADR [`adr/0009-v1-table-set-scope.md`](adr/0009-v1-table-set-scope.md)（**Proposed**）：
  推荐 **选项 C——上限与分组解耦，`v1 表集上限` 只约束【v1 core】**（当前 23 = 22 表 + `symbol_fts`；
  extension/deferred 与 `schema_migrations` 不计入）；附 28 名三分组建议（22 core + `symbol_fts` 补列 /
  1 extension / 5 deferred）、6 幽灵名去向与 I3 两对漂移的规范名（`inheritance_edges`、`dependency_versions`）。
  等待 owner 裁决。

### Notes

- 裁决前不动事实源：本轮未修改 `02`/`04`/`supplement/*`/`06`；`adr/README.md` 索引表同步登记 `0009 = Proposed`。

---

## 2026-09-29 — Phase1-shadow 实测 v1（真实调用重放）与候选映射修复

执行 `Phase1-shadow` Gate（ADR-0003 §10）：需要 shadow 数据校准 α 并复算 M1。
此前仓库只有 `exploration_attribution` 的写入路径：既无读取/聚合工具，也无无头测量入口。

### Added

- `usageledger.ListExplorationAttribution`：按时间读取归因行（NULL coverage/economy 原样还原）。
- `knowledge.SummarizeAttribution` / `CalibrateShadowAlpha`：M1–M4（mean/p50/p90 +
  按 tool/source/project 分组），usable 用传入 α 重算；同批数据重复复算逐位一致。
- 实测入口 `knowledge.TestPhase1ShadowReplay`（env-gated，CI 跳过）：真实调用重放 →
  生产 `ShadowObserver` → 真实 `RunIndex` 索引 → 真实 `usageledger` 落库 → 复算报告。
- 调用集提取脚本 `backend/scripts/extract-shadow-calls.mjs` 与调用集
  `reports/phase1_shadow_calls.jsonl`（400 条真实 grep/view：235/165）。
- `knowledge.Config.Alpha`（yaml `alpha`）+ `Activation.Config()`；`ShadowObserverFor`
  把配置 α 传入观察器；`runtime.yaml` 增加说明（缺省 0.8 仍是联调初值）。
- 报告 `reports/phase1_shadow_report.md`。
- ADR 提案 [`adr/0008-grep-coverage-file-level.md`](adr/0008-grep-coverage-file-level.md)
  （**Proposed**）：grep 通道主判据改 file-level 覆盖（新增两列），行级保留为诊断；
  依据报告 §4.1/§4.3 的实测反差（行级 M1=0.47 % vs 文件级 usable@0.8=26.76 %），
  并回应 ADR-0003 §10 的"抽样核对报告"待办。等待 owner 裁决。
- **live 接入验证（首个入口，2026-09-29）**：`aicli chat`（cmd+tui 宿主）以 `mode=shadow`
  跑真实会话（隔离 workspace + 临时 runtime.yaml/账本 DSN）——`knowledge.db` 落 2 文件 /
  3 符号，`ledger.db` 落 2 条 `exploration_attribution`（grep coverage=0.667、view=0.600）；
  新增 `TestLiveLedgerVerify`（env 门控）走生产 reader 复算 M1–M4（小样本 M1=0 %、
  M2=63.33 %、M4=74.34 %）。同日 **ACP 入口验证通过**（`aicli acp` + Node NDJSON JSON-RPC
  客户端：initialize → session/new → session/prompt；grep/view `tool_call` 后账本追加 2 条
  同值行）与 **runtime-server 入口验证通过**（HTTP：会话创建 → `permission-mode`
  `bypass_permissions`+confirm → `runtime/commands submit_prompt`；grep/view 并行执行后
  `DONE`，账本再追加 2 条同值行）——**live 验证 3/3**。注意：HTTP 会话默认模型来自
  `runtime.yaml` `agent.defaultModel`（内建 `claude-3-5-sonnet` 无 provider 声明会 fail-fast）。
- **抽样补齐 §5 未覆盖度量（2026-09-29）**：新增 env 门控 live 抽样
  `internal/knowledge/live_sampling_test.go`——content_hash 一致率 **261/261 = 100 %**
  （263 抽样；2 例为索引后被修改的预期不对称），同进程 4 写者锁等待
  `samples=0 / p95=0 / retry_failures=0`（240 文件 ×4、13.0 s 全部成功）。
  过程中修复 `index_jobs.id` 同纳秒碰撞（`jobs.go` 追加进程内原子序号 `indexJobSeq`），
  回归 `TestStartIndexJobIDsAreUnique`（8 并发 job 全成功且 ID 互异）。
- **性能阈值校准（04 §7.6 / 报告 §4.7，2026-09-29）**：等规模样本 n=3（原工作树 +
  2 个 4989 文件语料副本）全量索引——耗时中位数 **292.4 s**、95 % CI [241.8, 356.5] s；
  DB 中位数 313.9 MiB（CI [313.2, 314.3] MiB）；**`04` §7.4「首次全量索引（本仓库规模）」
  由未校准占位 ≤120 s 校准为 ≤ 360 s**（CI 上界取整）；二次增量（全量跳过）0.7–0.9 ms/文件。
  跨仓库抽验（module cache 异源语料 ×2：`x/net@v0.57.0` 742 文件 32.5 s、
  `gin@v1.12.0` 98 文件 3.81 s；**38.9–43.8 ms/文件**）与本仓库 58.6 ms/文件同量级；
  GitHub 直连被网络阻塞，故用本机 module cache 取样。
- **多 `paths` 逐项作用域（2026-09-29，报告 §5 第 2 项）**：grep shadow 映射支持 rg
  多根语义——`shadow_scope.go` 前缀集合 OR + `shadow.go` 的 `grepPathScopes` /
  `parseGrepBaselineMulti` / `prefixScopePathMulti`（逐行归属某作用域、否则回退首项），
  4 个回归测试（含观察器端到端 union 用例）；提取端 `extract-shadow-calls.mjs` 不再
  截取 `paths` 首项而是保留整组。对 v1 复算无影响（现有 400 条调用集中 `paths`=0，
  240 条为单 `path`），修复面向未来重放与 live 会话。
- **单文件增量实测（`04` §7.4 最后一行落数，2026-09-29）**：新增 env 门控
  `live_incremental_test.go`（20 样本、逐次 `indexed=1` 断言）；干净复测 job 墙钟
  p50 4.15 s / p95 4.35 s、"全量跳过"基线 4.05 s（遍历≈0.81 ms/文件）→
  **marginal p50 108 ms / p95 302 ms**（受干扰首测 p95 687 ms）；对照「p95 < 50 ms」
  **Fail**，须 Phase 5 增量触发（fsnotify）或重定口径。
- **文档不变量检查落地（06 §9 #3，2026-09-29）**：新增 `backend/scripts/check_knowledge_doc_invariants.go`
  （ADR-0007 §4.4 的 I1–I5，stdlib、`go run`、失败 exit≠0、含正向 fixture 自测）。
  首次实测 **5/5 FAIL**（即 #2/#19/#20 尚未执行的事实）：I1 六幽灵名
  （`branches`/`dependencies`/`events`/`index_jobs`/`inheritance`/`language_projects`）
  + `symbol_fts` 未列入；I2 `04` 内 15 处越位 DDL；I3 两对命名漂移
  `{inheritance, inheritance_edges}`/`{dependencies, dependency_versions}`；
  I4 `02` §8 尚无三分组；**I5 声明 ≤16 vs 实际 23**（22 表 + 虚表）→ #7 裁决输入就绪；
  已登记 #20「`02` §8 三分组落地」。

### Fixed（重放实测暴露的 shadow 缺陷）

- 绝对 path / file_path 未折叠为 workspace 相对路径 → 候选恒为空（`relativizeWorkspacePath`）。
- grep 输出路径未按作用域目录补前缀（rg 以 path 为根输出相对路径）→ (path,line) 永不相交
  （`parseGrepBaseline(output, scopeBase)`）。
- regex pattern 直接检索 FTS → 零命中；改为交替拆分 + 字面 token 并集去重（≤6 token，`shadowPatternTokens`）。
- refs 候选未接入且 `FindRefs` 未回填 `path` → 使用点行不可达；新增 `Reference.Path` +
  观察器 refs 候选通道（可选接口 `refCandidateIndex`），并把批量 `patterns` 还原为多次检索
  （`grepPatternList` / `collectShadowTokens` + 提取脚本数组还原）。
- 双作用域只取其一（`path`+`glob` 同时给出时只取 `firstNonEmpty` 的第一个，真实样本
  144/243 条）→ `newScopeFilterSpec` 让 path 与 glob 为 AND，baseline 前缀仍只由 path 决定。

### Findings

- **M1=20.81 %（n=370）**：view 通道 48.4 %（可用），grep 行级 **0.47 %**（M2 3.79 %）；
  同一批数据的 file-level 对照：grep mean **31.83 %**、p90 100 %、usable@0.8 26.76 %、
  answerable **49.38 %**——量化证明瓶颈是行级口径（ADR-0003 §6.2 偏差），需新 ADR 裁决。
- 索引侧复测（4989 文件）：324.9 s / 313.5 MiB，对照 `04` §7.4 初值仍 Fail（同 Phase 0 结论）。
- α 中位数校准=0.10，行级分布双峰（α∈[0.1,0.8] 对 M1 影响可忽略），本次**不写死阈值**。

---

## 2026-09-29 — Phase 1 交付 1/3 收口（Java/C++ 粗符号 + 文件软删除）与 shadow 口径修正

起因：`06` 实施状态核查发现两处交付缺口与一处口径漂移——交付 1 要求"补 Java / Rust / C++
粗符号"但 Java/C++ 未实现；交付 3 要求"删除文件标记 `deleted_at`"但索引只做 `content_hash`
增量、不对账删除；`04`/`06` 声称 shadow 对比写 `invalidation_events`，而该表 reason 闭集是
变更源事件（`04` §4.3），ADR-0003 也未定义 shadow 写该表。

### Added

- **Java 粗符号**（`adapter_builtin.go` 的 `javaSymbolPatterns`）：class / interface / enum /
  record + 带修饰符的方法/构造器（支持 `@Annotation` 前缀与 `public` 可见性判定）；
  `default -> handle();` 等 switch 箭头与字段初始化不误收。`.java` 此前只有文件行、没有符号行。
- **C/C++ 粗符号**（`cppSymbolPatterns`，注册 `cpp` 与 `c`）：class / struct / union / enum +
  具名函数（含 `Foo::bar` 限定、`const` / `noexcept` / 尾置返回类型）；`statementKeywords`
  守卫排除 `return compute(x);` 这类语句。已知折损："最令人烦恼的解析"式变量构造可能误收
  （代码注释已标注，由 Phase 4 深索引收敛）。
- **`migrations/0002_file_soft_delete.sql`**：`files.deleted_at` 列 + `(workspace_id, deleted_at)`
  索引；既有 v1 库打开时自动升级（含升级路径测试）。
- **Store 接口**：`ListActiveFiles` / `MarkFilesDeleted`（单事务：`files.deleted_at` +
  `index_state=stale` + 其 `symbols.deleted_at` 同步标记；幂等；reader 返回 `ErrReadOnlyStore`）。
- **reader 版本守卫**：`verifyInitialized` 在 schema 版本落后于本二进制时显式报错
  （提示以 writer 打开一次完成迁移），避免旧库半可用。
- **`RunIndex` 删除对账**：完整遍历（`!Truncated`）后把"库内登记、磁盘缺失"的文件软删除，
  新增 `IndexResult.Deleted` 计数；文件恢复时 `UpsertFile` 清空标记、`ReplaceSymbols`
  重建符号（复活）。`truncated` 时绝不对账，避免把未遍历到的文件误标为删除。
- **读路径过滤**：FindSymbols / FindRefs / Search / searchLike / symbolIDsByName / Stats
  全部排除 `f.deleted_at IS NOT NULL` 的文件及其符号/引用。
- **测试**：`adapter_java_cpp_test.go`、`indexer_soft_delete_test.go`、`store_test.go` 的
  `TestMarkFilesDeletedIsIdempotent` / `TestSoftDeleteMigrationUpgradesV1Store`。

### Changed

- **`04` §5 Phase 1 交付 4 / `06` §4 Phase 1 交付 4**：shadow 对比只写
  `exploration_attribution`，明确**不写 `invalidation_events`**（口径修正）；
  `store.go` 的 `RecordInvalidation` 注释同步。
- **`06` §1.1/§4/§5、`04` §5、`README.md` §1/§4**：交付 1–6 状态、实际文件落点与
  配置段落点校正（原预测的 `indexer_light.go` / `query.go` / `knowledge_test.go` 未单独
  落盘；`knowledge:` 段实际只在 `runtime.yaml` / `runtime.win7.yaml`）。

### Notes

- `KnowledgeVersion`（磁盘契约版本，DB 文件名的一部分）保持 1：0002 是增量迁移，不改变
  `stable_key` 身份语义；`schema_migrations` 版本随之前进到 2。
- 仍未收敛：`Phase1-shadow` 实测（α 校准 + M1 复算）；Phase 1 尚未验收。

---

## 2026-09-28 — Phase 1 交付 5（`knowledge.status` CLI / HTTP 状态面）

起因：关闭 `06` §4 Phase 1 交付 5 —— 把索引状态、文件数 / 符号数、DB 大小、最近 job、
锁等待 p95 暴露为统一只读状态面（CLI `aicli knowledge status` + HTTP
`GET /api/runtime/knowledge/status`），为 `Phase1-shadow` 实测与日常诊断提供观测入口。

### Added

- **`backend/internal/knowledge/status.go`（`StatusReport` / `Layer.Status` / `OwnerPID` / `lookupWorkspace`）** ——
  统一只读载荷：mode / role / owner pid / workspace / db 路径与大小 / schema 版本 /
  files / symbols / refs / indexed_at + staleness / index_running / last_job / lock_wait /
  degraded_reason。不触发索引、不写库（`lookupWorkspace` 走 `FindWorkspace`，reader 也可用）；
  `degraded_reason` 覆盖「未索引 / reader 降级 / 上次索引失败」，零值不静默。
  `activation.go` 增加 `Activation.Status` / `IndexRunning` / `Stats`（合并本进程后台索引生命周期）。
- **`backend/internal/knowledge/jobs.go`** —— `index_jobs` 运行账本：`StartIndexJob` /
  `UpdateIndexJob` / `FinishIndexJob` / `LatestIndexJob`（ADR-0007 §4.3 / `04` §4.1 的
  「先写表再执行」）；错误摘要截断到 512B、空串落 NULL。`indexer.go` 的 `RunIndex` 接入：
  先落 running，每 256 个文件上报进度，结束落 done/failed + files_total/done + 时长
  （终态上报用 `context.WithoutCancel` + 5s 超时——ctx 取消常伴随索引失败，「失败」恰是最该落库的事实）。
- **`backend/internal/knowledge/lockwait.go`** —— 写路径锁等待采样（有界 128 环 + 最近秩分位，
  `Samples` / `P50MS` / `P95MS` / `MaxMS` / `RetryFailures`）；进程内、无持久化，p95 为近似分位。
- **`backend/internal/sqliteutil`（`RetryLockedCtxObserved`）** —— 与 `RetryLockedCtx` 同语义，
  额外把单次调用累计退避上报给观察器；`RetryLockedCtx` 改为 nil-observer 包装。
  `store_sqlite.execWrite` 接线，并在重试耗尽（最终锁错误）时记 `observeFailure`。
- **HTTP 面（`runtimeapi/knowledge_handlers.go`）** —— `GET /api/runtime/knowledge/status`：
  未接线 / mode=off 返回 200 + off 载荷（404/503 会迫使调用方猜状态）；内部错误 500 统一错误体；
  路由注册于 `handler.go`，句柄经 `SetKnowledgeActivation` 注入（runtime-server 启动装配）。
- **CLI 面（`cmd/aicli/commands/knowledge.go`）** —— `aicli knowledge status [--workspace] [--json] [--timeout]`：
  工作区锚点与三入口同源（flag → runtime.yaml workspace.root → cwd）；`SkipInitialIndex`
  保证查询不触发索引；默认人类可读快照，`--json` 与 HTTP 载荷同形；mode=off 时打印显式启用提示。
- **测试**：`knowledge/status_test.go`（行数/job/锁等待/reader 降级/off 载荷/owner→reader）、
  `sqliteutil/lock_wait_observe_test.go`（无等待不上报 / 累计 / 非锁错误 / 取消仍上报）、
  `runtimeapi/knowledge_status_handler_test.go`（off / nil handler / 接线载荷 / nil 重置）、
  `cmd/aicli/commands/knowledge_status_test.go`（字节格式化 / 文本面字段 / off 提示 / flag 契约）。

### Changed

- **`knowledge/config.go`**：新增 `StorePathFor`（与 `Open` 落点逐字节一致，状态面不打开 store 也能报路径）。
- **`cmd/runtime-server/main.go`**：`handler.SetKnowledgeActivation(knowledgeActivation)`（与 shadow 观察器同源）。
- **`06` §1.1 / §4 Phase 1、`04` §5 Phase 1、`README` 阶段行**：交付 5 由「未开始」改为
  「已完成」，并登记落点与下一步（`Phase1-shadow` 实测：α 校准 + M1 复算）。

### Notes

- 锁等待口径：样本来自**本进程写路径**每次锁冲突的累计退避；进程重启清零（「当前进程经历过的
  锁竞争」才是诊断所需语义）。单写者拓扑下稳态应接近全零；非零即提示并发写者或长事务。
- 状态面契约：**只读**（不触发索引、不写库）、**nil-safe**（off / 未接线返回 off 载荷而非错误）、
  **可解释**（degraded_reason）。验收口径「锁等待 p95 < 50ms」待 `Phase1-shadow` 实测。
- 仍未收敛：`Phase1-shadow` 实测（α 校准 + M1 复算）——交付 5 是其观测入口。

---

## 2026-09-28 — Phase 1 交付 4（shadow 拦截 `grep` / `view`）

起因：关闭 `06` §4 Phase 1 交付 4 —— `mode=shadow` 下在既有 `grep` / `view` 执行路径上
拦截并旁路对比（ADR-0003 §4.3 / §4.4），使 `exploration_attribution` 开始产生数据、
M1 可复算。

### Added

- **`backend/internal/knowledge/shadow.go`（新增 `ShadowObserver` / `ShadowObserverFor` / `ShadowConfig` / `ShadowIndex` / `AttributionSink` / `ObservedCall`）** ——
  只读索引侧候选（`Search` / `FindSymbols`）与工具实际输出（baseline）逐调用对比，
  计算 `baseline_n` / `candidate_n` / `overlap_n` / `coverage` / `economy` / `usable`
  （ADR-0003 §4.2 口径；零结果 `coverage` 落 NULL，§4.5）；落库只写
  `query_hash = sha256(tool + "\x00" + pattern + "\x00" + scope)` 与
  `project_id = ProjectIDForWorkspace(ws)`（哈希短键，不含绝对路径，§4.6）。
  `DefaultShadowAlpha = 0.8` 仅为联调初值，**不得**作为验收门槛（§4.2 / §10）。
- **`usageledger.AppendExplorationAttribution`**（`sqlite_store.go`）—— 18 列插入，
  浮点为 NULL 语义；**不写 `token_usage_history`**（D3 不变量，由测试钉住）。
- **`agent.LoopReActConfig.OnToolObserved`**（`internal/agent/loop.go`）—— 在 MCP 分支
  与并行批次出口上报工具最终结果；hook 为 nil 时零行为变化。
- **三入口接线**：aicli cmd/tui（`applyLocalChatToolObservation`，`chat_actor_host.go`）、
  aicli acp（`attachSessionKnowledge` 挂 `ChatSession.Knowledge`，`agent_stdio.go`）、
  runtime-server（`Handler.SetKnowledgeShadow` + `applyAPISessionToolObservation`）。
- **测试**：`knowledge/shadow_test.go`（grep 覆盖率 / 零结果落库 / view 区间覆盖 /
  全覆盖可用 / 跳过与 mode=off / sink 错误语义 + 工厂与 project_id 契约）、
  `usageledger/sqlite_store_exploration_attribution_write_test.go`（回读 + 零基线 NULL +
  不污染 `token_usage_history`）、`agent/loop_observe_test.go`、
  `runtimeapi/knowledge_shadow_wiring_test.go`。

### Changed

- **acp 引用计数修正**（`agent_stdio.go`）：`attachSessionKnowledge` 与 TUI 同口径把句柄挂到
  `ChatSession.Knowledge`；`closeSessionLocked` 仅在句柄**未**挂在 chat 上时释放，
  避免与 `finalizeChatSession` 双释放导致 refs 少计。
- **`06` §1.1 / §4 Phase 1、`04` §5 Phase 1、`README` 阶段行**：交付 4 由「未开始」改为
  「已完成」，并登记落点与下一步（交付 5 → `Phase1-shadow` 实测）。

### Notes

- 观察器契约：**尽力而为**（落库失败只 debug，不冒泡为 turn 失败）、**只读**（不改工具
  结果）、`mode=off` / 未接线时 hook 为 nil，与无知识层逐字节一致。
- 仍未收敛：**交付 5**（`knowledge.status`）；`Phase1-shadow` 实测（α 校准 + M1 复算）。

---

## 2026-09-28 — 补记：ADR 0001/0003/0007 裁决为 Accepted、Phase 0 文档治理（#8 / #9）与 `index_jobs` DDL 迁移（#1）

起因：Phase 0 文档治理收尾与 Phase 1 门禁解除。owner 授权代改 ADR 状态并记录裁决；随后按
ADR-0007 §4.3 补做 `index_jobs` DDL 迁移。Phase 1 开工见下一条。

### Changed

- **ADR 裁决**：`adr/0001`、`adr/0003`（**仅口径**；§10 的 α 阈值仍受 `Phase1-shadow` 约束）、`adr/0007` 由 `Proposed` → `Accepted`（2026-09-28）；三项头部加 `Accepted` 记录行，`adr/README.md` §4 状态表 / §5 说明同步。**Phase 1 的 `Phase1-start` 门禁解除**。ADR 决策正文未改动（Accept 前已完成的 #11 / #16 修订见 2026-09-21 条目）。
- **#8 归档**：`00_Code_Intelligence_Project_Knowledge_Layer.md` 以 `git mv` 移入 `archive/`，文首加"归档说明"（仅供追溯，不作事实源）；`README.md` §1、`04` 评审对象行、`06` §9 同步。
- **#9a `03` 拆分**：`03_agent_harness_supplement.md` 拆为 `supplement/01`–`16`（正文逐段搬迁、章节号沿用原编号；`03` §5 并入既有 `supplement/05` §10），`03` 重写为**拆分索引 + 历史引用映射**（`03 §5.4(L674)` 更正为 `§6.4`）。引用同步：`README.md`、`04`、`06`、`adr/README.md`、`adr/0006`、`docs/lsp/*`（4 个文件）。
- **#9b `01` 边界**：`01` 文首加 **§0 定位与事实源边界**（逐节指认权威落点；正文未删减）；逐节删减登记为 **#18**（Phase 1 开工前）。
- **#1 `index_jobs` 迁移**：`04` §4.3 的 `CREATE TABLE index_jobs` + 索引**逐字节**迁至 [`supplement/15_change_management.md`](supplement/15_change_management.md) §15.3（extension schema）；`04` 只留用途 / 验收指标 / 引用（ADR-0007 §4.3）。

### Notes

- 本轮为**文档结构治理**：不改变设计决策、不改变 DDL 语义、不改动 `Accepted` ADR 的决策正文。
- 仍未收敛（`06` §9）：**#2** `02` §8 三分组、**#3** I1–I5 不变量脚本、**#19** `04` §4.3 其余 15 张表 DDL 的引用化（依赖 **#7** 对 "v1 ≤ 16 张" 口径的裁决）、**#18** `01` 逐节删减。

---

## 2026-09-28 — Phase 1 交付 6（接入/激活）+ 探索归因 9 指标暴露

起因：Phase 1 门禁（ADR-0001 / 0007 / 0003 口径）已解除，开工。本次关闭两项
此前"只有文档、没有代码"的交付：`06` §4 Phase 1 交付 6「接入（激活）」与
`06` §5.2 / 附录 A 步骤 6「`usageanalytics` 暴露 9 个探索归因指标」。

### Added

- **`backend/internal/knowledge/activation.go`（新增 `Activation` / `Activate` / `ActivationOptions`）** ——
  三个入口共用的"接入"原语：打开 store → 判角色（owner/reader）→ owner 在后台跑首次全量索引。
  `mode=off` 时在任何磁盘操作之前返回 `(nil, nil)`；`Close` 取消并**等待**后台索引退出（索引持写事务，
  先关 store 会撞锁）。`Activation` 所有方法 nil-safe，接入方无需分支。
- **`backend/cmd/aicli/commands/chat_knowledge.go`（新增）** —— `cmd/aicli`（chat/tui）与
  `cmd/aicli agent stdio`（ACP）共用：按 workspace 引用计数的接入表 + 进程级一次性释放。
  一个 ACP 宿主进程可服务多个 workspace，故不能"进程级单例 + 首次 Close 释放"。
- **`backend/cmd/runtime-server/knowledge_boot.go`（新增）** —— `bootRuntimeServerKnowledge`：
  workspace 锚点 = `runtime.yaml` 的 `workspace.root`（相对配置文件解析）→ 进程 cwd；
  两者皆空则 warn + 降级为 off；`mode=off` 时不调用 `Activate`（零副作用）。

### Changed

- **`backend/cmd/aicli/commands/chat.go`** —— `ChatSession` 新增 `Knowledge *knowledge.Activation`；
  `HandleChat` 在 exit-cleanup 注册处接入 `attachChatKnowledge` + `releaseAllChatKnowledge`。
- **`backend/cmd/aicli/commands/chat_setup.go`** —— `buildChatFinalCleanup` 在 finalize 会话后
  归还引用。
- **`backend/cmd/aicli/commands/agent_stdio.go`** —— `acpHostSession` 新增 `knowledge` /
  `knowledgeWorkspace`；`session/new` 在记录 workspace 之后接入；`closeSessionLocked` 归还引用。
- **`backend/cmd/runtime-server/main.go`** —— `runtimeServerApp` 新增 `knowledge` 字段，启动阶段
  （workspace 解析后、对外服务前）调用 `bootRuntimeServerKnowledge`；`close()` 最后释放知识层。
- **`backend/internal/api/runtimeapi/usage_ledger_group.go`** —— `usageLedgerProfileGroup` 增加
  9 个探索归因字段（**全部 `omitempty`**），`aggregateUsageLedgerByProfile` 按组求和。
- **`backend/pkg/skillsapi/client.go`** —— `UsageLedgerRecord` 增加 9 字段；新增
  `UsageLedgerProfileGroup`；`GetUsageLedgerResponse` 增加 `group_by` / `groups` / `grouped_total`
  （`omitempty`）；`GetUsageLedgerParams` 增加 `GroupBy`，客户端在非空时才发 `group_by` 查询参数。

### Notes

- **`mode=off` 硬不变量**：三入口在 off 下不建库、不建锁文件、不起 goroutine，行为与"无知识层"
  逐字节一致；9 个归因指标在 off 下恒为 0 且因 `omitempty` 不出现在任何响应里。
- **刻意偏离 `06` §4 Phase 1 交付 6 的措辞**：计划写"向 `internal/background` 注册
  `knowledge.index.initial` 任务"，但 `internal/background.Manager` 只有 shell 作业通道
  （`SubmitShell`），进程内任务无公开注册口。把索引塞进 shell 作业会多起一个进程并重复打开同一个
  store，反而破坏单写者不变量。因此首次全量索引由 `Activate` 内部后台 goroutine 承担
  （同样不阻塞启动 / turn，Close 可取消并等待退出）。
- **`06` §5.2 的路径近似**：该表把"暴露 9 指标"的落点写成 `internal/usageanalytics/*`，但该包不读
  `usageledger`；ledger 的实际读取与聚合面在 `internal/api/runtimeapi`（`GetUsageLedger`）。
  本次按实际落点实现，`06` 属索引文档、不构成决策依据。
- 验证：`go build ./...` 干净；`go test ./internal/knowledge/ ./internal/usageledger/
  ./internal/api/runtimeapi/ ./pkg/skillsapi/ -count=1` 全绿。

---

## 2026-09-28 — Phase 0 交付 7 落地：`exploration_attribution` 建表

起因：核对"Phase 0 是否真的可验收"时发现，`04` §5 / `06` §4 的 Phase 0 交付 7
（`exploration_attribution` 表 + 2 索引，2026-09-21 由 §9.2 #15 补入归属）**只有文档、没有代码**：
`usageledger` 的 `init()` statements 里既无建表也无索引，
Phase 0 验收门槛"可被 `sqliteutil.OpenFileCtx` 打开且重复 init 幂等"此前无法通过。

### Changed

- **`backend/internal/usageledger/sqlite_store.go`** —— `init()` 的 statements 追加
  `CREATE TABLE IF NOT EXISTS exploration_attribution`（18 列，与 ADR-0003 §4.1 逐列对齐）
  与两个索引 `idx_exploration_attribution_created_at` / `idx_exploration_attribution_tool_time`。
  复用既有 `IF NOT EXISTS` 幂等语义（ADR-0003 D7），**只建表与索引，不产生数据**。
- **`backend/internal/usageledger/sqlite_store_exploration_attribution_test.go`**（新增）——
  4 个用例：①列集与两索引齐全且表为空；②同一 DSN 反复 init 幂等、`sqlite_master` 只登记 1 次；
  ③可被 `sqliteutil.OpenFileCtx` 打开（验收门槛原文）；④D3 非污染——重新 init 后
  `token_usage_history` 的行数与 token 聚合逐字节不变。

### Notes

- **`mode=off` 行为零变化**：只新增空表与索引，没有任何写入路径，ledger 记录与聚合结果与改动前一致。
- 验证：`gofmt`/`go vet` 干净；`go test ./internal/usageledger/ ./internal/knowledge/ -count=1` 全绿。
- 仍未落地（不属本次范围）：`06` §5.2 / 附录 A 步骤 6 提到的"`usageanalytics` 暴露 9 个新指标"
  与 §9 文档治理尾项 #8 / #9；Phase 1 及之后仍受 ADR-0001 / 0003 / 0007 门禁约束。
  （**2026-09-28 更新**：9 指标已在本文件"Phase 1 交付 6（接入/激活）+ 探索归因 9 指标暴露"条目中落地；#8 / #9 与 `index_jobs` 迁移已落地；ADR-0001 / 0003 / 0007 已 Accept、门禁解除——见顶部"补记"条目。）

---

## 2026-09-21 — 修复规划缺口（#10–#16 关闭；修复中另发现并修复 #17）

起因：owner 指示"针对缺口进行修复"。上一条（核查发现）登记的 7 项缺口**已全部修复**；
修复过程中又发现**同类缺陷 1 项**（#17，ADR-0004 陈旧度阈值 Gate），一并修复。
`06` §9.1 保留为发现记录，新增 §9.2 为修复记录。

### Changed

- **`adr/0003-exploration-attribution-metrics.md`**（仍 `Proposed`，修订就地标注日期）
  - 头部 `Gate`：#11 —— 阈值 Gate `Phase0-baseline` → **`Phase1-shadow`**。
  - §4.1：#16 —— D3 由字面"一列不改、一行不变"改写为**三条可检验形式**（不新增行 / 不改变既有聚合 / 不改变历史行语义），并注明 Phase 0 已实现的 9 列 `ADD COLUMN … DEFAULT 0` **满足**该实质要求。
  - §8 验证表"不污染"行同步改写。
  - §1.2 / §2 D4 / §4.2 / §5 / §6.1 / §10：α 的产出点由"Phase 0 产出"改为"Phase 1 shadow 实测产出"；§10 增"Gate 变更说明"。
- **`04_completeness_review_and_optimized_plan.md`**
  - §5 Phase 0：#15 —— 新增交付 7 `exploration_attribution` 表（只建表与埋点骨架，不产生数据）；验收门槛补"重复 init 幂等"。
  - §5 Phase 1：#10 —— 新增交付 6「接入（激活）」（runtime-server / aicli cmd+tui / aicli acp）；#13 —— 交付 4 由 `code.search` 改为**拦截既有 `grep`/`view`**；#14 —— 验收门槛改为**主门槛（M1）+ 诊断指标**。
  - §7.6：#11 —— 校准流程改为"Phase 0 出基线与警告 / Phase 1 shadow 出阈值"。
- **`06_implementation_index_and_guidance.md`**
  - §1.2 / §3 / §4 Phase 0 / §4 Phase 1 / §4.2 / §5.1 / §5.2 同步上述修订；新增 §9.2 修复记录表。
- **`reports/phase0_baseline_report.md`**：#12 —— 新增 §7（ADR-0003 §6.2 强制内容）：`coverage` 低估警告 + 抽样核对状态（Phase 0 无 shadow 数据，顺延至 `Phase1-shadow`）+ Phase 1 执行清单。
- **`README.md`**：顶部阶段行与 §1 状态表更新；§2 门禁清单加入 `0003`；§7 新增"阈值类 Gate 必须可达成"。
- **`adr/0004-stale-index-tool-surface.md`**：#17 —— `S_fresh` / `S_max` 的 Gate 由 `Phase0-baseline` 改为 **`Phase2-start`**；§4.1 “由 Phase 0 校准”同步修订。
- **Gate 词汇表登记处**（新增 `Phase1-shadow` 取值）：`GLOSSARY.md` §… 字段表、`adr/0000-template.md`、`adr/README.md` §4 Gate 表 / §5 索引表 / §8 B3 行、`supplement/05` §9.3 表。

### 修复性质（供 owner 复核）

| 类别 | 项 | 说明 |
|---|---|---|
| 文档一致性修复（**不改变设计决策**） | #10 #12 #13 #15 | 把已存在于 `supplement/05` 或 ADR-0003 的内容补上 Phase 归属，或对齐两套口径 |
| 修订 `Proposed` ADR 正文 | #11 #14 #16 #17 | `0003` / `0004` 仍为 `Proposed`，按 `adr/README.md` 在 Accept 前修订属正常流程；改动已就地标注日期，owner 可在 Accept 时一并复核 |

### Notes

- **#11 是本批的关键**：原 Gate 结构性不可达（Phase 0 是 `mode=off`，产不出 shadow 对比数据），
  会让 ADR-0003 永远无法 Accept、Phase 1 被自锁。修订后 **ADR-0003 的 Accept 不再被阈值阻塞**。
- **#17 是同一根因的第二个实例**：凡是“需要索引 / shadow / reader 观测数据”的阈值，都不能挂在
  `Phase0-baseline`。已在 `GLOSSARY.md` 的 `Gate` 定义里写明这条判据，防止再犯。
- **#16 未改任何代码**：Phase 0 已实现的 `ADD COLUMN`（请求级粒度、历史行取 `DEFAULT 0`）本就满足
  D3 的实质要求，冲突只在字面；修订方向是**把 D3 写成它本来的意思**，不是放宽它。
- 仍未解决但已登记的相邻项：`04` §5 Phase 1 的"≤ 120s / ≤ 200MB"初值已被实测击穿
  （146.9s / 247.5 MiB），Gate 是 `04` §7.4 评审，**不属于本次 7 项缺口**。

---

## 2026-09-21 — 核查发现 7 项 Phase 1 规划缺口（未改任何事实源；同日已修复，见上条）

起因：复核"`knowledge.Open` 未接入 aicli → shadow 为 no-op"与"5 条 ADR 仍 Proposed"两条状态时，
追问"这是否只是未实现"。结论：**不是**——其中 7 项属于**计划自身的缺口**（任务写在规格或 ADR 里，
却没有 Phase 归属；或两个事实源对同一验收给了不同口径），已登记为 `06` §9.1 #10–#16。

### Changed

- `docs/knowledge_Layer/06_implementation_index_and_guidance.md` — 新增 §9.1"规划缺口"表（#10–#16）；
  §1.2 阻塞清单同步标注（ADR-0003 阈值门禁不可达 + Phase 1 规划缺口 7 项）。

### Findings（摘要）

| # | 缺口 | 证据 |
|---|---|---|
| 10 | Phase 1 缺"激活"交付项：`knowledge.Open` 接入 aicli / runtime-server 只在 `supplement/05` §2/§8，`04`/`06` 的 Phase 1 交付与文件落点均无 | `supplement/05` L107/L124/L510/L576-578 |
| 11 | ADR-0003 阈值门禁 `Phase0-baseline` 结构性不可达（α 需 shadow 数据，而 Phase 0 无 shadow） | `adr/0003` §10、§4.2 |
| 12 | ADR-0003 §6.2 强制内容（coverage 低估警告 + 抽样核对）未写进 Phase 0 报告 | `adr/0003` §6.2 vs 报告全文 |
| 13 | Phase 1 shadow 以 `code.search` 定义，但 `code.search` 是 Phase 3 交付 | `04` §5 Phase 1 交付 4 vs Phase 3 交付 1 |
| 14 | Phase 1 验收两套口径：`04` "top-10 差异率 < 15%" vs ADR-0003 §4.5 M1 主门槛 | `04` §5 Phase 1 验收 vs `adr/0003` §4.5 |
| 15 | `exploration_attribution` 表仅存在于 ADR-0003，无 Phase 归属（M1 由它计算） | `adr/0003` §4.1；`04`/`06` 全文无 |
| 16 | ADR-0003 D3 字面"一列不改"与已实现 Phase 0（`ADD COLUMN` 9 列）冲突 | `sqlite_store.go` L236-244/L262-287 |

### Notes

- **本次只登记缺口，未修改 `04` / `adr/*` 等事实源**——按 `README.md` §6 变更流程，
  涉及验收口径与决策的改动须先经 owner / 评审裁决。
- #10 与 #11 互为因果：激活项无归属 → Phase 0 拿不到 shadow 数据 → ADR-0003 阈值门禁无法满足
  → Phase 1 被自锁。**这两项应作为同一个问题裁决。**

---

## 2026-09-21 — Phase 0 状态定稿：核心 5 交付完成

### Changed

- `docs/knowledge_Layer/06_implementation_index_and_guidance.md` — Phase 0 状态定稿为"核心 5 交付已完成"
  （非全面封版：文档治理尾项 §9 条目 8 / 9 仍挂起）。添加 Phase 1 进入条件与校准建议
  （3 个仓库样本：首次全量 ≤ 0.6ms×refs 且 ≤ 300s；DB ≤ 1KiB×refs 且 ≤ 300MB）。
- `docs/knowledge_Layer/reports/phase0_baseline_report.md` — 状态由"部分完成"→"完成"；
  §6 标题与交付 3 判定表更新为"完成（A/B 延期）"。

### Notes

- Phase 0 工程验收已就绪：5/5 交付落地 + ledger 可复算 + `TestPhase0TaskBaselineSummarize` PASS。
- 进入 Phase 1 的两个硬门禁仍未满足：ADR-0001 + ADR-0007 需 owner Accept；ADR-0003 阈值
  待 `04` §7.6 校准落稿。

---

## 2026-09-21 — Phase 0 任务侧基线（mode=off）完成

起因：`usageledger` 接入 aicli（`chat_cache_local.go`）后，`reports/phase0_baseline_report.md`
§6 的"任务侧"子项得以执行。

### Added

- `backend/internal/knowledge/baseline_run_test.go` — `TestPhase0TaskBaselineSummarize`：
  从 `gateway.db token_usage_history` 读取 7 条 `llm_runtime` 记录，调用
  `BaselineReport.Summarize` + `Percentiles`，产出 mode=off 任务侧基线。

### Changed

- `docs/knowledge_Layer/reports/phase0_baseline_report.md` — §6 回填 5 个真实任务的
  基线结果（7 LLM 请求 / 150,768 tokens / p50=6,000ms / p95=12,600ms）；标题与状态
  更新为"任务侧 mode=off 完成"。
- `docs/knowledge_Layer/06_implementation_index_and_guidance.md` — §4 Phase 0 交付 3
  状态更新：任务侧 5 个真实任务已跑，A/B（off vs shadow）延期至 Phase 1。

### Results

| 指标 | 值 |
|---|---|
| 样本 | 7 LLM 请求 / 5 真实任务 |
| TotalTokens | 150,768 |
| SuccessfulTasks | 7 |
| FailedTasks | 0 |
| ExplorationTokenShare | 0.000000（mode=off） |
| ToolCallsPerTask | 0.00 |
| RepeatedReadPerTask | 0.00 |
| Latency p50 | 6,000 ms |
| Latency p95 | 12,600 ms |
| SafetyViolations | [] |

### Notes

- `shadow` A/B 延期：`cmd/aicli/` 未接入 `knowledge.Open`，`mode=shadow` 在 aicli 为
  no-op，待 Phase 1 知识层接入后补跑。

---

## 2026-09-21 — 补上 LSP 落地文档的反向引用

起因：`docs/lsp/`（2026-09-21 建立）已在自身 README 声明"服务于 `docs/knowledge_Layer` 已评审的 LSP 规格"，但本目录**没有任何反向指针**，事实源与落地方案文档脱节（缺口）。

| 文件 | 改动 |
|---|---|
| [`README.md`](README.md) | §1 表格后增加"跨目录入口"说明；§3 单一事实源声明表新增 LSP 落地方案行 |
| [`06_implementation_index_and_guidance.md`](06_implementation_index_and_guidance.md) | §2 文档地图新增 `../lsp/` 行 |
| [`adr/0002-acp-lsp-ownership.md`](adr/0002-acp-lsp-ownership.md) | §10 Open Follow-ups 新增一条（Gate = `Phase4-start`） |
| [`adr/0006-lsp-position-encoding-boundary.md`](adr/0006-lsp-position-encoding-boundary.md) | §10 Open Follow-ups 表后新增"实现侧落点"引用，重申本文件仍是编码转换边界与缓存键的唯一事实源 |
| [`supplement/05_runtime_integration_project_detection_and_lsp.md`](supplement/05_runtime_integration_project_detection_and_lsp.md) | §9 已裁决问题表前新增 `../../lsp/` 实现侧入口与边界声明（不新增散文条目，仅引用） |

本次改动**不新增 schema、不改 DDL、不改任何决策**，仅建立引用关系。

---

## 2026-09-20 — Phase 0 续：轻索引收回到"顶层声明"（builtin/3）与相邻计划交叉评审

起因：builtin/2 的首次诚实基线显示仍有 **5080 行符号身份被合并**（1093 个文件）。一次性诊断
（对全仓跑一遍 `Extract` 并按 `stable_key` 分组）推翻了初版归因：合并行里 **4295 行（84.5%）是
函数体内的局部变量**（`var output bytes.Buffer`、`let container: HTMLDivElement;`）——适配器把
Deep Index 的内容放进了 Light Index，直接违反 `04` §2 的 Lazy 原则（"v1 默认只索引文件 +
**顶层符号** + imports；方法体、局部变量、类型关系不索引"）。

### Changed

- `backend/internal/knowledge/adapter_builtin.go` — 新增 `topLevelOnly(language)` / `isIndented(line)`：
  go / typescript / javascript 的声明规则只接受**顶格**声明（Python 早已用 `^def`/`^class` 锚定；
  Rust 的 impl 方法依赖缩进作用域，不适用）。缩进行仍照常产出**调用点引用**。
- `backend/internal/knowledge/version.go` — `AdapterVersion` `builtin/2` → `builtin/3`。
- `docs/knowledge_Layer/reports/phase0_baseline_report.md` — §2/§3 回填实测数字；§4.4 重写为真实
  归因（附 kind / 语言分布）；新增 §4.5（残余 732 行的归因）、§4.6（refs 口径变化）。
- `docs/knowledge_Layer/06_implementation_index_and_guidance.md` — §8 修订 `cache_entries` 一行
  （与 `llm-cache-analytics` 计划是**命名撞车**，不是同一张表）；Phase 0 交付 5 标记完成。

### Added

- `backend/internal/knowledge/adapter_scope_test.go` — 顶层/局部作用域回归用例。
- `docs/knowledge_Layer/reports/phase0_cross_review.md` — Phase 0 交付 5：与 5 份相邻计划 +
  `02`/`03` schema 事实源的交叉核对（6 条动作项，其中 A6 是 Phase 1 硬门禁）。

### Fixed

- **局部变量被当成符号**：builtin/2 下同目录同名同签名的局部变量塌缩成同一身份，本仓库实测
  1093 个文件 / 5080 行被合并。builtin/3 后降到 **229 个文件 / 732 行**（−85.6%）。
- **缩进声明行吞掉调用点**：builtin/2 对 `var x = f(...)` 这类行先按声明匹配再 `continue`，
  行内真实调用 `f(...)` 丢失；builtin/3 让这些行落进调用点扫描，refs 反而 +2.2%
  （372286 → 380657）。两处同源：都在把轻索引从错误的作用域收回正确的作用域。

### Deviations / 已知限制（需评审）

- `symbolNamespace` 用"文件所在目录"表达包/模块边界：对 Go（包=目录）正确，对 TS/JS/Python
  （模块=文件）过粗，残余 732 行合并即由此产生（`scripts/*.go` 的 build-tag 隔离多 main 程序同理）。
  收敛方案（Go=目录、TS/JS/Python=文件、build-tag 文件加文件名分量）列入 Phase 1，
  届时需再升一次 `AdapterVersion` 并重跑基线。
- 冷启动样本 n=3（本仓库 + gin + prometheus），`06` Phase 1 的 120s / 200MB 初值按报告 §5
  建议改为**每 ref 口径**（≤ 0.6ms × refs、≤ 1 KiB × refs），定稿需 `04` §7.4 评审。

### 外部仓库对照（同日追加，报告 §2.4）

`git clone` 在本机网络下只有 ~13 KB/s（7 分钟仅 5 MB），改用 codeload tarball + `tar -xzf`；
两个外部样本的测量命令与 §1 完全相同，仓库为 gin `3b08cd72`（99 文件）、prometheus `1d6fe378`（1010 文件）。

| 指标 | gin | prometheus | 本仓库 |
|---|---|---|---|
| 首次全量 | 2.66s | 47.3s | 146.9s |
| 每文件 | 26.9ms | 46.9ms | 38.1ms |
| 每 ref | 0.269ms | 0.359ms | 0.386ms |
| DB 每 ref | 701 B | 647 B | 682 B |
| 合并身份 | 1.06% | 1.24% | 1.68% |

结论：**"每文件"不是稳定口径，"每 ref"才是**（跨仓库 ±20% 内），Phase 1 门槛应按 refs 表达。

### 开启 usage_ledger（任务侧基线前置）

报告 §6 步骤 1 要求"跑任务前必须打开 usage_ledger"。修改 `backend/configs/config.yaml` 与
`config.runtime.snapshot.yaml` 的 env 默认 `SKILLS_RUNTIME_USAGE_LEDGER_ENABLED:-false`→`:-true`
（仍可 `SKILLS_RUNTIME_USAGE_LEDGER_ENABLED=false` 关）；代码默认仍为 `false` 不变。bench 证据
（`internal/usageledger/sqlite_store_bench_test.go`）：单写 **3.09ms/op**、并发 0 丢失、
444 bytes/记录（100k ≈ 42 MiB）——3 ms ≪ 一次 LLM 轮次，故默认开启不增可感知延迟。
同时在 `~/.aicli/.env` 追加 `SKILLS_RUNTIME_USAGE_LEDGER_ENABLED=true` 作为运行时开关
（`~/.aicli/config.yaml:13595` 的 `${...:-false}` 订阅优先，重启 aicli 后生效）。

---

## 2026-09-20 — Phase 0 开工：知识层骨架落地与 `stable_key` 身份缺陷修复

起因：`backend/internal/knowledge/` 存在一份**未提交且无法编译**的半成品（而 `06` §4 仍写"Phase 0 未开始"）。
本次把它补成可编译、可测量的 Phase 0/1 骨架，并在首次真机基线上发现一个会让索引**静默丢掉三分之一文件**的缺陷。

### Added

- `backend/internal/knowledge/baseline.go`、`baseline_test.go` — `04` §7.7 基线报告的数据面（token 构成、重复读取、p50/p95 复算）。
- `backend/internal/knowledge/baseline_index_test.go` — 索引侧基线测量入口（`KNOWLEDGE_BASELINE_REPO=<path>` 开启，默认 skip）。
- `backend/internal/knowledge/telemetry.go`、`telemetry_test.go` — 9 个探索归因字段的采集器（并发安全 `Recorder`）。
- `backend/internal/config/knowledge_config_test.go` — `ValidateKnowledgeConfig`（实现在 `manager.go`，
  复用 `knowledge.DefaultConfig()` / `ParseMode`）的用例：mode 三值解析、负数限额拒绝。
- `backend/internal/usageledger/sqlite_store_knowledge_metrics_test.go` — 9 字段 round-trip 与**旧 13 列库升级**用例。
- `docs/knowledge_Layer/reports/phase0_baseline_report.md` — Phase 0 基线报告（索引侧已实测；任务侧待跑）。

### Changed

- `backend/internal/usageledger/sqlite_store.go` — 新增 9 列，并用 `PRAGMA table_info` 做幂等 `ALTER TABLE` 补列（旧库兼容）。
- `backend/internal/model/entity/token_usage_history.go` — 9 个 `omitempty` 归因字段。
- `backend/configs/runtime.yaml`、`runtime.win7.yaml` — 新增 `knowledge:` 段，默认 `mode: "off"`。
- `backend/internal/knowledge/adapter_builtin.go` — `stable_key` 补上 `namespace` 分量（`symbolNamespace`）。
- `backend/internal/knowledge/store_sqlite.go` — `ReplaceSymbols` 容忍 `stable_key` 冲突：保留身份行、改挂宿主，并记录 `adapter_conflict`。
- `backend/internal/knowledge/version.go` — `AdapterVersion` `builtin/1` → `builtin/2`（让 builtin/1 写出的错误身份表自然失效）。

### Fixed

- **`stable_key` 缺 `namespace`（`04` §4.4 的 `normalize(namespace_or_package_or_module)` 被传成空串）**：本仓库 3860 个候选文件里有
  **1365 个（35%）**的符号写入被 `idx_symbols_stable` 唯一约束拒绝，`ReplaceSymbols` 整份失败、`refs` 一并丢失，
  且 `RunIndex` 只把它计入 `errors`，从表面看像是"解析失败"。修复后 `errors=0`，符号数 22348 → 60899。

### Deviations（偏离设计文档，需评审）

- `04` §4.4 歧义规则要求"stable_key 冲突时**使该符号 `confidence` 降级**"，但 v1 DDL 的 `symbols` 表**没有 `confidence` 列**，
  因此只能落地为"记录 `adapter_conflict` 事件 + 保持身份行唯一"。是否加列待 Phase 1 决定。
- 冲突时采用 **last-writer-wins**（身份不变、`file_id` 改挂最近写入的文件），而不是丢弃后写入者：
  丢行会让符号从库里消失，LWW 只换宿主，`refs` 指向的 `symbol_id` 不受影响。

---

## 2026-09-20 — 决策层建立与文档一致性修复

本次变更的起因是一次文档完整性核查，发现三类缺陷：

1. `README.md` 声明 `adr/` 为"决策唯一事实源"，但该目录**不存在**；
2. `adr/` 建成后其索引表列了 7 个 ADR，其中 6 个**从未落盘**（悬空链接）；
3. `02` §8 的总览清单（28 个名字）与其 `CREATE TABLE` 区块（22 张）**不一致**，且 `04` 承载了本不属于它的 DDL。

### Added

- **`adr/` 决策记录目录**（此前仅在 `README.md` §1 中声明为"待创建"）：
  - `adr/README.md` — ADR 索引与流程（状态生命周期、可逆性分级、Decided-by gate、索引表）
  - `adr/0000-template.md` — ADR 模板
  - `adr/0001-project-module-language-schema.md` — Project/Module/Language 模型收敛
  - `adr/0002-acp-lsp-ownership.md` — ACP 下 LSP 归属与能力面
  - `adr/0003-exploration-attribution-metrics.md` — 探索归因与 shadow 差异率度量
  - `adr/0004-stale-index-tool-surface.md` — 陈旧索引下的 `code.*` 工具面
  - `adr/0005-windows-child-process-lifecycle.md` — Windows 子进程树生命周期与复用既有 process guard
  - `adr/0006-lsp-position-encoding-boundary.md` — LSP 位置编码转换边界与缓存键
  - `adr/0007-phantom-tables-and-doc-invariants.md` — 幽灵表清理与文档不变量
- **`GLOSSARY.md`** — 术语表（此前声明为"待创建"；本次建立，覆盖现有文档中已定义或反复使用的术语）
- **`CHANGELOG.md`** — 本文件

### Changed

- **`README.md`**
  - §1 文档状态表：`adr/` 由“待创建”改为“已建立（`0000` + `0001`–`0007`，全部 `Proposed`）”；
    `GLOSSARY.md` / `CHANGELOG.md` 由“待创建”改为“已建立（2026-09-20）”；
    新增 `supplement/` 行（**部分建立**，当前仅 `05`），`03` 行状态细化为“待拆分（`supplement/05` 已建立）”。
  - §2 实施者阅读顺序：新增“先读 `adr/`”，并声明 `04` 附录 B **不是**决策依据；
    `supplement/01` 引用改为 `03` §1（标注“待拆分为 `supplement/01`”），`02` 的提示改为“待补应用顺序声明”标注。
  - §3：已裁决关键决策的指针由"`04` §3.2 与附录 B"改为 `adr/`。
  - §7 关键约束：DDL 约束补充"extension schema 例外"与"`04` 不得含 `CREATE TABLE`"。

- **`supplement/05_runtime_integration_project_detection_and_lsp.md`**
  - §2.3：LSP 默认策略由 `external_preferred` 改为 `off`，并加 ADR-0002 指针与废弃理由
    （ACP v1 能力面中不存在任何 LSP 能力位，"先探测 editor 是否提供 LSP"在 v1 无对象可探测）。
  - §2.4 入口对照表：同步该默认值。
  - §3.1：Project/Module 收敛的表述改为指向 ADR-0001（对应 `04` 附录 B 的 B6）。
  - §9：由"待裁决问题（需 ADR）"改为"已裁决问题（ADR 索引）"，含 5 项裁决映射表
    与 2 项新增 ADR；原始提问移入 §9.1 保留备查。

- **`adr/README.md`**
  - §5 索引表：`0005`/`0006` 标题与文件对齐；`0007` 的"取代"列补齐。
  - §5：新增落盘状态与"ADR 不得复制 DDL"说明。
  - §7：明确 DDL 规则的**精确边界**（不得复制已存在 DDL；新增对象可给拟议 DDL；
    本规则只约束 `knowledge.db`，其他库以自身代码为事实源）。
  - §8：新增 `04` 附录 B → ADR 的已完成映射（B3 → 0003；B6 → 0001；B15 待转）。

- **`04_completeness_review_and_optimized_plan.md`**
  - 附录 B：加注"本附录是散文列表，不构成决策依据"，指针改指 `adr/`，记录已完成映射。
  - 第 10 节 `index_jobs`：加注 **DDL 越位**（ADR-0007 §4.3），待 ADR 被 Accept 后迁移到 extension schema。

- **`02_agent_harness_technical_design_spec_sqlite.md`**
  - §8 数据模型总览：加注"清单 28 名 vs DDL 22 张"，列出 6 个无 DDL 的名字
    （`branches`/`inheritance`/`dependencies`/`language_projects`/`index_jobs`/`events`）
    与对应 ADR，并说明 ADR-0007 要求的三分组重构。
  - 文件头：加注"**待补 schema 应用顺序声明**"（`04` 附录 B 的 B15）。

- **`03_agent_harness_supplement.md`**
  - §5.3 位置编码规则：加注 ADR-0006 指出的 6 项缺口（协商、canonical 定义、
    非 BMP/组合字符、BOM/CRLF、`document_version` 语义升级、`lsp_servers` 补列）。
  - §5.2 `lsp_servers`：加注待补 `position_encoding` 列（ADR-0006 §4.3）。

### Notes

- **本批新增的 7 个 ADR 全部为 `Proposed`**，需 owner 按 `adr/README.md` §2 逐个 Accept。
  `Accepted` 之后 ADR 正文不可修改，要改需新写 ADR 并在 `Supersedes` 引用旧号。
- **凡 ADR 要求改动其他文档正文的，本次一律只加指针/标注，不执行正文改动**，
  以避免在决策被接受前产生既成事实。受此约束的待办：
  - `04` L546 的 `index_jobs` DDL 迁移（ADR-0007 §4.3）
  - `02` §8 的三分组重构与不变量脚本（ADR-0007 §4.2/§4.4）
  - `03` §5.3 规则改写与 `lsp_servers` 补列（ADR-0006 §4.1–§4.3）
- **唯一例外**是 `supplement/05` §2.3 的 `external_preferred`：该表述所依赖的能力面
  在 `backend/internal/acp/types.go` 中**不存在**，属**事实错误**而非设计分歧，故直接修正。
- **仍未执行（既有建议）**：`00_Code_Intelligence_Project_Knowledge_Layer.md` 移入 `archive/`
  （`README.md` §1 已标注，涉及链接改写，未在本批处理）。**2026-09-28 已执行**：`git mv` → `archive/` + 文首归档说明，见顶部"补记"条目与 `06` §9.3。
- **仍未解决（已知矛盾，待裁决）**：`04` §4.3 声明的 "v1 表集 ≤ 16 张" 与 `02` 实际的
  22 张 core DDL 疑为不自洽。ADR-0007 的不变量 **I5** 专门检测此项，由检查结果裁决，ADR 未预设结论。

- **同日补记（第二轮一致性核查）**：`02` 文件头的"待补 schema 应用顺序声明"标注，
  在首批编辑中因 `multiedit` 部分失败（失败项 `old_string` 起于 `> 适用：AI Co...`）
  而**未实际落盘**；本次已补写于 `02` 顶部，`CHANGELOG` 原记录描述的是预期状态，
  现与磁盘一致。同轮另修正 `README.md` §1 中 `GLOSSARY.md` / `CHANGELOG.md` 的
  "待创建"陈旧状态、新增 `supplement/` 行、并修正 §2 对 `supplement/01`（尚未落盘）的引用。

- **新增 `06_implementation_index_and_guidance.md`（方案实施索引与指引）**：以实施者视角重组本目录——
  §3 ADR 门禁表（含 `Gate`、阻塞 Phase、状态）、§4 分阶段实施指引（Phase 0–8，每项含目标 / 前置 ADR /
  交付 / 文件落点 / 验收 / 回滚）、§5 文件落点索引（新增 / 修改 / 明确不新增）、§6 验收与度量入口、
  §7 变更与治理流程、§8 边界、§9 已知阻塞与待办、附录 A Phase 0 首日清单。
  本文定位为**索引 / 指引**：不复制 DDL、不复制阈值、不复制 ADR 正文，冲突时以事实源原文为准。
  同步更新 `README.md` §1 状态表、§2 实施者阅读顺序、§7 元文档例外，以及 `04` 附录 C.1 文件清单。
