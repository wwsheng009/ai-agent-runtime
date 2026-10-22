# 测试、指标与验收

> 状态：验收基线（阈值在 M0 校准）  
> 原则：先建立基线，再设定阈值；所有数字必须能由当前代码路径、测试或真实会话证据复现。  
> 本文件中的“暂定阈值”在 M0 基线完成后由 owner 校准。

## 1. 验证目标

Agent Memory v2 的验证必须同时满足：

1. **行为正确**：读取、注入、引用、删除、迁移正确；
2. **默认安全**：`off` 无 v2 自动副作用，scope 不穿透；
3. **质量可接受**：自动抽取不制造大量噪声、不污染偏好；
4. **性能有界**：不阻塞对话，不破坏 prompt cache；
5. **可运维**：任务、错误、审计、导出、回滚可用；
6. **跨平台**：标准构建和 Windows 7 兼容构建均通过受影响包的验证。

## 2. 测试分层

```text
单元测试
  ├─ memorystore schema / hash / dedupe / retrieval
  ├─ redaction / citation parser / token budget
  └─ job lease / retry / watermark

集成测试
  ├─ notes.jsonl 迁移
  ├─ contextmgr 注入顺序
  ├─ canonical transcript → extraction
  ├─ phase2 → summary → context build
  └─ API / CLI / Web settings

回归测试
  ├─ mode=off 基线
  ├─ prompt-cache 前缀稳定
  ├─ compact / resume
  ├─ 现有 /memory 与 harness/memory
  └─ SQLite 并发/锁

E2E / live
  ├─ aicli chat
  ├─ runtime-server + Web
  ├─ ACP 入口
  ├─ background/idle 触发
  └─ Windows 7 构建
```

## 3. 后端测试矩阵

### 3.1 存储与迁移

| 场景 | 预期 |
| --- | --- |
| 空目录首次打开 | 创建 DB；`off` 且无手工访问时不创建 v2 DB |
| `off` + 手工 `/memory add` | 保留现有手工 memorystore 行为，不要求 v2 自动能力 |
| `notes.jsonl` 正常迁移 | 记录数量、文本、tags、manual 来源一致 |
| retention_class | permanent 不因 maxUnusedDays 自动过期；ephemeral 更短 |
| 重复迁移 | 不重复插入 |
| 损坏行 | 跳过并计数，不影响其他行 |
| JSONL 写入失败/只读 | 迁移失败可诊断，不丢原文件 |
| DB 打开失败 | 读取降级，不阻断 Agent |
| DB 被另一进程写 | busy_timeout/重试生效，无死锁 |
| 关闭后再次打开 | schema 版本正确，数据可读 |

建议测试：

- `TestMemoryStore_CreateSchema`
- `TestMemoryStore_MigrateNotesIdempotent`
- `TestMemoryStore_CorruptJSONL`
- `TestMemoryStore_OpenFailureDegrades`
- `TestMemoryStore_WALConcurrentReaders`
- `TestMemoryStore_OffKeepsManualMemory`
- `TestMemoryStore_ProjectKeyCanonicalization`
- `TestMemoryRetentionClassAndPinnedIndependent`

### 3.2 记录与检索

| 场景 | 预期 |
| --- | --- |
| content hash 去重 | 同 scope/kind/text 不重复 active |
| manual vs derived | manual 不被自动覆盖 |
| supersede | 旧记录保留历史但不注入 |
| tombstone | 删除后检索/注入均不可见 |
| 删除后重新抽取 | suppression 生效，不复活 |
| correction | 新 revision 生效，旧记录 superseded，审计事件存在 |
| pinned manual | 自动 Consolidation 不 supersede/merge/delete |
| summary revision | `memory_summary_items` 与 summary revision 一致 |
| expired | 过期记录不注入 |
| scope 过滤 | project/user/profile 不串数据 |
| 标签过滤 | AND 语义 |
| 空 query | 按最近更新时间 |
| 关键词匹配 | 命中 text/tags |
| 零结果 | 返回过滤/分词命中诊断，不只返回空 |
| FTS 不可用 | LIKE fallback 结果语义一致 |
| token budget | 不超预算；至少允许一条截断策略需固定 |
| usage 排序 | cited 只增加被引用记录 |

### 3.3 上下文注入与 prompt cache

必须覆盖现有 `contextmgr` 约束：

- 记忆只出现在 raw history 之后；
- system/profile/ledger 前缀不被改写；
- `project_memory` stage 已存在时不重复追加；
- active-turn replay 时抑制；
- `off` 时 v2 不新增/不改变消息列表；手工 memory 与基线一致；
- summary 切换只发生在新 turn；
- tool surface 不变；
- compact 后仍在动态尾部；
- resume 后不重复注入旧 memory block。

建议复用/新增：

- `TestBuildProjectMemoryKeepsDynamicTail`
- `TestBuildDoesNotPrependProjectMemory`
- `TestBuildProjectMemorySuppressedOnActiveReplay`
- `TestBuildProjectMemorySnapshotStableAcrossSteps`
- `TestBuildMemoryOffByteIdentical`
- `TestBuildSummaryRevisionOnlyChangesAtTurnBoundary`

### 3.4 Phase 1 抽取

| 场景 | 预期 |
| --- | --- |
| 未启用的 session | 不入选 |
| 新鲜 session | 未过 idle 阈值不入选 |
| 已处理且无更新 | 不重复 |
| source 更新 | 可重新抽取，生成新 revision |
| 子 agent | 默认排除 |
| 跨 workspace | 拒绝 |
| LLM 返回非 JSON | 失败，不落库 |
| 字段缺失/过长 | 校验失败 |
| secret | 整条丢弃并记录规则名 |
| 证据缺失 | 不落 candidate |
| 任务 lease 过期 | 另一个 worker 可接管 |
| 重试耗尽 | failed，记录错误，不无限循环 |

### 3.5 Phase 2 Consolidation

| 场景 | 预期 |
| --- | --- |
| 无 candidate | 不生成新 summary |
| 重复记录 | 合并 sources，不重复注入 |
| 冲突记录 | 新记录 supersede 旧记录，旧记录不可注入 |
| 用户 correction | 优先于自动记录 |
| summary 生成失败 | 保留旧 revision |
| summary 超预算 | 截断/压缩并记录 |
| manual note | 不被自动删除 |
| reset | 记录与汇总清理，但源会话不动 |
| 两个进程同时 Consolidate | 单写者锁生效 |
| 汇总使用的记录 | 由 `memory_summary_items` 可重建 |

### 3.6 引用与 usage

| 场景 | 预期 |
| --- | --- |
| 无 citation | 不增加 cited 计数 |
| 合法 citation | 增加 cited，记录 session/turn |
| 非法/不存在 id | 记录错误指标，不阻断回答 |
| 本 turn 未注入/未读取的 id | 记为 invalid，不增加 usage |
| citation 在输出流中 | 不泄漏到最终可见正文 |
| 记忆被删除 | 旧 citation 保留审计，但后续不注入 |
| 读取工具使用 | 记录 read，不等同于 cited |
| 用户说“忘掉” | 只有显式命令/受权管理 API 可删除；不产生自动删除 |
| 用户说“记住” | 工具可用时写 manual；工具不可用时提示命令 |

### 3.7 API / CLI

- `/api/runtime/harness/memory` 兼容行为不变；
- v2 接口参数校验、上限、错误码；
- workspace 绑定；
- delete/reset 确认；
- `/memory add/list/search/status` 不回归；
- `/memory show/why/why-not/correct/forget/export` 有正/负向测试；
- 子 agent 无写权限。
- delete/restore/reset/session mode 需要授权，不能只凭 workspace_path。

### 3.8 安全测试

必须明确覆盖：

1. 跨项目 SQL 查询无法返回其他 project_key；
2. user scope 未开启时不可见；
3. deleted/tombstone 不可注入；
4. secret 不进入 DB、日志、事件、导出；
5. 记忆文本中的“忽略以上指令”不会被当作 system 指令；
6. memory 内容不能触发工具/权限;
7. 记忆导入（未来）不能执行其中命令；
8. 导出文件只包含授权 scope；
9. 日志不打印原文 secret；
10. 删除操作有审计且不可默默恢复。

补充测试：

- `TestMemorySuppressionPreventsRecreation`
- `TestMemoryCorrectionCreatesRevisionAndEvent`
- `TestMemorySummaryItemsMatchRevision`
- `TestMemorySessionModeDisabledSkipsExtraction`
- `TestMemorySessionModePollutedSkipsGeneration`
- `TestMemoryDeleteRequiresAuthorization`
- `TestMemoryRestoreRejectsHardDeletedRecord`
- `TestMemorySummaryHybridBudgetSplit`
- `TestMemorySummaryModeWithoutToolsStillSelfContained`
- `TestMemorySummaryActivateRollback`
- `TestMemorySummaryActivateRejectsPrunedInputs`
- `TestMemoryImportValidatesAndDeduplicates`
- `TestMemoryImportedRecordsStartUnverified`
- `TestMemoryCitationRequiresTurnEvidence`
- `TestMemoryPromptIncludesDecisionBoundary`
- `TestMemorySummaryFormatAndFallback`
- `TestMemoryStaleNotInjected`
- `TestMemoryStaleWarnOnlyLowRisk`
- `TestMemorySourceChangeMarksStale`
- `TestMemorySourceMissingBecomesStale`
- `TestMemorySummaryWatermarkMismatchRebuilds`
- `TestMemoryRevalidationRestoresFresh`
- `TestMemoryStaleSummaryFallback`
- `TestMemoryWhyShowsFreshness`
- `TestMemoryUserScopeRequiresOptIn`
- `TestMemoryPromotionRequiresEligibility`
- `TestMemoryBudgetClampedByPromptBudget`
- `TestMemoryRecallTraceRedactsQuery`
- `TestMemoryZeroResultDiagnostics`
- `TestMemorySessionContextAuthorization`
- `TestMemoryWorkingDigestBound`（v1.1）
- `TestMemoryHandoffClaimOnce`（v1.1）
- `TestMemoryTieredBriefBudget`（v1.1）
- `TestMemoryAccessWeightedRetention`（v1.1）
- `TestMemoryRRFExplain`（v1.1）
- `TestMemoryProfilePromotionRequiresEvidence`（v1.1）
- `TestMemoryProfileForgetPreventsResurrection`（v1.1）
- `TestSessionTranscriptReaderRejectsCrossWorkspace`

## 4. 前端测试

- Settings memory section 加载、空态、错误态；
- 搜索、添加、删除、二次确认；
- 来源/scope/usage 展示；
- 非当前 workspace 不可见；
- reset 后状态刷新；
- job 状态展示；
- 中文/英文 i18n；
- 大列表分页/截断，不一次渲染过多记录。

命令：

```bash
cd frontend
pnpm test
pnpm build
```

## 5. 端到端测试

复用现有 harness：

- `scripts/test-aicli-tools-e2e.ps1`
- `scripts/test-aicli-resume-history-e2e.ps1`
- `scripts/test-aicli-windows-terminal-e2e.ps1`
- `scripts/test-runtime-server-browser-e2e.ps1`
- `scripts/aicli-e2e-harness.ps1`

建议新增：

```text
scripts/test-agent-memory-e2e.ps1
```

覆盖：

1. `aicli` 手动 `/memory add`；
2. 结束会话后 shadow 生成 candidate；
3. 手动 consolidate；
4. 新会话注入 summary；
5. 引用后 usage 增加；
6. `forget` 后新会话不再出现；
7. Windows 路径含空格/中文；
8. SQLite busy 时仍可对话；
9. prompt cache 前缀不变（用 debug 日志 hash 或专用测试）。

## 6. 指标与观测

### 6.1 功能指标

| 指标 | 说明 |
| --- | --- |
| `memory_extract_claimed` | 领取的会话任务数 |
| `memory_extract_succeeded` | 成功抽取数 |
| `memory_extract_invalid_json` | 结构化输出失败数 |
| `memory_extract_skipped` | 因 idle/scope/开关跳过 |
| `memory_candidates_created` | 新 candidate 数 |
| `memory_records_active` | active 记录数 |
| `memory_duplicates_merged` | 去重合并数 |
| `memory_superseded` | 冲突替代数 |
| `memory_summary_revision` | 当前摘要版本 |
| `memory_injection_count` | 注入记录数 |
| `memory_injection_tokens` | 注入 token |
| `memory_citations` | 被引用次数 |
| `memory_reads` | 工具读取次数 |
| `memory_deleted` | 用户删除数 |
| `memory_cross_scope_denied` | 越界访问拦截数 |
| `memory_redaction_drops` | 脱敏丢弃数 |
| `memory_freshness_status_total` | fresh/stale/unverified 数量 |
| `memory_revalidate_total` | 回源校验成功/失败 |
| `memory_stale_summary_blocked_total` | stale summary 被阻止注入次数 |

### 6.2 性能指标

| 指标 | 目标 |
| --- | --- |
| 正常对话额外延迟 | 0（读取只做本地短查询） |
| 注入 token p95 | <= 配置预算；默认 <= 600 |
| 记忆检索 p95 | 暂定 < 20ms（本地 SQLite） |
| Phase 1 单会话耗时 | 仅后台；有上限 |
| Phase 2 单项目耗时 | 有 lease/超时；不阻塞聊天 |
| DB 大小增长 | 由 retention + maxRecords 约束 |
| WAL/busy 等待 | 不造成 runtime-server health 死锁 |

### 6.3 质量指标（shadow 阶段）

建议在 M5 用真实标注集评估：

- 抽取真阳性率；
- 假阳性率；
- 遗漏率；
- 重复率；
- 用户 correction 比例；
- citation 命中率；
- 注入后用户负面反馈率；
- 过期/矛盾记忆数量；
- stale/unverified 记录比例、revalidation 成功/失败；
- stale summary 被阻止注入次数；
- secret/敏感数据命中数（目标必须为 0）。

**暂定门禁（M0 后校准）：**

- 自动抽取结果中被人工/用户判定为明显错误的记录 < 10%；
- secret 泄漏 = 0；
- 跨项目泄漏 = 0；
- `mode=off` 行为差异 = 0；
- prompt prefix 变化 = 0（非 memory stage）；
- 注入 token 超预算 = 0；
- 删除后再次注入 = 0；
- 同一 source 重复提取 = 0。

> 这些不是最终产品阈值；不得在没有 shadow 数据时把它们写成硬编码常量。

## 7. 回归命令

```bash
cd backend

go test ./internal/memorystore ./internal/memoryruntime \
  ./internal/contextmgr ./internal/agent ./internal/chat \
  ./internal/api/runtimeapi ./internal/tools -count=1

go test ./internal/compactruntime ./internal/artifact ./internal/factledger -count=1

go test ./... -count=1
go vet ./...
```

前端：

```bash
cd frontend
pnpm test
pnpm build
```

Windows 7（受影响包）：

```bash
cd backend
GOFLAGS="-modfile=go.win7.mod" go test -tags win7compat \
  ./internal/memorystore ./internal/memoryruntime ./internal/contextmgr \
  ./internal/agent ./internal/chat ./internal/api/runtimeapi -count=1
```

并发：

```bash
cd backend
go test -race ./internal/memorystore ./internal/memoryruntime ./internal/contextmgr -count=1
```

## 8. 阶段验收标准（Definition of Done）

> 每个阶段必须同时满足功能、回归、安全、观测、回滚五类条件；未满足不得进入下一阶段。  
> 下列阈值是 v1 初始门禁，M0 建立基线后由 owner 校准；不得在没有 shadow 数据时把暂定值写成不可配置常量。

### 8.0 通用门禁（所有阶段）

- [ ] 相关 Go 包 `go test -count=1` 通过，全量 `go test ./...` 与 `go vet ./...` 通过；
- [ ] 触及前端时 `pnpm test`、`pnpm build` 通过；
- [ ] `git diff --check` 通过，文档链接、代码围栏和表格校验通过；
- [ ] `mode=off` 与基线的 prompt 顺序、prompt hash、消息列表、工具面和 `/memory` 输出一致；
- [ ] 无 secret 进入 DB、日志、事件、trace、导出；跨项目/跨 scope 泄漏为 0；
- [ ] 每个新增开关都有回滚路径和失败降级测试；
- [ ] 本阶段涉及的文档、配置示例和运维说明同步更新。

**证据要求：** 测试命令与输出、基线 diff、迁移报告、安全检查结果、回滚记录必须可复现；口头确认或“看起来可用”不算验收。

### 8.1 M0：基线与契约

**退出条件：**

- [ ] `07 §8.1` 中阻塞 M1 的决策已确认，并标记为 ADR/配置契约；
- [ ] `mode=off` 基线快照已记录：prompt 顺序/hash、`/memory` 输出、harness memory API 响应、context stage、token 估算、notes 数量；
- [ ] feature flag、默认 `off`、手工 notes 独立开关已定义；
- [ ] 失败矩阵覆盖 DB 不存在/打开失败/无 summary/无笔记/迁移损坏/FTS 不可用/stale summary/source hash 变化/跨 workspace；
- [ ] v1.1 扩展点已冻结：v1 不建 `memory_handoffs`、不写 RRF trace 列、不启用 access-weighted retention/profile 晋升；
- [ ] M1 的 migration/rollback 方案已评审。

**量化门槛：** 基线差异 = 0；`off` 下 v2 自动 DB 创建/模型调用/工具注册/注入 = 0。

**证据：** 基线文件或测试断言、决策记录、失败矩阵测试、回滚步骤。

### 8.2 M1：SQLite 存储与读取门面

**退出条件：**

- [ ] schema/migration 可重复执行；重复迁移不重复插入；
- [ ] `notes.jsonl` 迁移前后记录数、文本、tags、manual 来源一致；损坏行有计数且不阻塞其他行；
- [ ] 旧 `/memory`、`/api/runtime/harness/memory`、Settings append/search 不回归；
- [ ] `off` 不创建 v2 DB；`on` 只读，不自动抽取；
- [ ] scope 隔离、tombstone/suppression、revision/event、summary revision/items schema 契约测试通过；
- [ ] `freshness_status`/`last_verified_at`/`revalidate_after` 持久化正确；stale 默认不注入；
- [ ] `valid_until` 到期按 expired 处理；
- [ ] 注入仅在 dynamic tail；`project_memory` 不重复追加；compact/resume 后不重复注入；
- [ ] memory budget 被总 prompt 预算 clamp；单 turn 最多一个 memory block。

**量化门槛：** notes 迁移丢失 = 0；跨 scope 泄漏 = 0；prompt 前缀变化 = 0；本地检索 p95 暂定 < 20ms（M0 校准）。

**证据：** 迁移报告、基线 diff、scope/删除/freshness/cache 测试、SQLite busy 测试、回滚到 `notes` 的记录。

### 8.3 M2：Phase 1 会话提取

**退出条件：**

- [ ] 自动抽取只写 `candidate`，不写 `active`；`shadow` 只记录、不注入；
- [ ] session 选择符合 workspace、mode、idle、watermark、subagent 排除规则；
- [ ] job 具备幂等 key、lease、接管、退避、重试上限和失败可观测性；
- [ ] 非 JSON、字段缺失/过长、无 source、secret 命中均不落库；
- [ ] 跨 workspace session 拒绝；工具原始大输出不直接进入抽取；
- [ ] 新 candidate 初始化 freshness/TTL；同一 source 变化时旧记录标记 stale；
- [ ] 对话路径无额外同步模型调用，抽取失败不影响正常回答。

**量化门槛：** secret 落库 = 0；跨项目 source = 0；同一 source 重复提取 = 0；`off` 模型调用 = 0。

**证据：** 至少 3-5 个会话的 shadow 记录（M5 再扩展到 10-20 个）、candidate 抽样、source 变化/stale 测试、失败任务列表、lease 接管测试、脱敏测试。

### 8.4 M3：Consolidation、摘要与引用

**退出条件：**

- [ ] hash 去重、source 合并、supersede、correction 优先级正确；
- [ ] manual/pinned 不被自动 supersede/merge/delete；
- [ ] summary revision 与 `memory_summary_items` 完全一致；可从 DB 重建；
- [ ] summary 生成失败保留旧 revision；activate/rollback 有授权和事件记录；
- [ ] summary 注入前通过 `input_watermark + memory_summary_items freshness` 门禁；
- [ ] summary 不一致/含 stale item 时重建；失败回退 fresh records/notes，不注入旧 summary；
- [ ] summary/hybrid/notes 的 token 拆分符合预算；summary 超限可截断/压缩；
- [ ] citation 只接受本 turn 注入或读取过的 id；非法 citation 不增加 usage、不阻断回答；
- [ ] 删除/纠正后写 suppression，下一轮不再注入，且不会被后续抽取复活。

**量化门槛：** summary 注入超预算 = 0；prompt 前缀变化 = 0；删除后再次注入 = 0；citation 伪造导致的 usage 增长 = 0；summary 长度 p95 不超过配置预算。

**证据：** 去重/冲突/回滚测试、summary 重建报告、summary watermark/stale 测试、citation 正负向测试、删除复活测试、token 预算报告。

### 8.5 M4：工具、API 与 UI

**退出条件：**

- [ ] `memory_search`、`memory_read`、`memory_session_context` 只读且受 scope/kind/tag 限制；
- [ ] `memory_add`/`memory_correct` 只在用户明确请求时写入；`memory_forget` 默认关闭；
- [ ] delete/restore/reset/session mode 使用管理员/本机授权，不能只凭 `workspace_path`；
- [ ] revalidate/freshness sweep 有管理员授权、事件记录和失败重试；
- [ ] API 分页、参数上限、workspace 绑定、错误码和兼容接口测试通过；
- [ ] Web/TUI 正确展示来源、scope、freshness、usage、job、recall trace、why/why-not；删除二次确认；
- [ ] 非当前 workspace 不可见；子 agent 无写权限；错误不泄漏 secret。

**量化门槛：** 未授权 destructive API 成功 = 0；跨 workspace 可见记录 = 0；前端 build/test 失败 = 0。

**证据：** CLI/Web E2E、权限负向测试、接口兼容测试、前端截图或测试报告、导出脱敏检查。

### 8.6 M5：Shadow 评估与逐项目上线

**退出条件：**

- [ ] shadow 运行覆盖 10-20 个真实项目会话，至少一个完整工作日/多个 idle 周期；
- [ ] 完成人工标注，统计真阳性、假阳性、遗漏、重复、correction、citation 和敏感数据命中；
- [ ] 至少一个真实项目完成 `on` + `notes`，再切 `hybrid`；每次只改一个维度；
- [ ] 完成 `mode=off`、`notes`、summary revision 回滚演练；
- [ ] 监控、告警、导出、备份、迁移手册和用户说明已更新；
- [ ] owner 明确签署上线结论。

**量化门槛（初始门禁，M0/M5 校准）：**

| 指标 | 门槛 |
| --- | --- |
| 自动抽取明显错误率 | < 10% |
| secret 泄漏 | = 0 |
| 跨项目泄漏 | = 0 |
| `off` 行为差异 | = 0 |
| 非 memory stage 的 prompt 前缀变化 | = 0 |
| 注入 token 超预算 | = 0 |
| 删除后再次注入 | = 0 |
| 同一 source 重复提取 | = 0 |
| stale summary 注入 | = 0 |
| stale/unverified 记录默认注入 | = 0 |
| SQLite busy 导致 health 死锁 | = 0 |

### 8.7 v1.1 能力门禁（不阻塞 v1）

- [ ] `memory_handoffs` claim-once 在单事务内完成，TTL 到期不再注入；
- [ ] handoff 只带 source refs，不复制完整会话正文，不携带工具授权；
- [ ] tiered brief 先扣安全框架/pinned/rules/footer，再按 L0/L1/L2 分配预算；
- [ ] access-weighted retention 不改变 semantic/procedural 的自动衰减规则；
- [ ] RRF explain 记录 stream/rank/fusion，不保存完整 query 或记忆正文；
- [ ] cross-project profile 需要多项目证据或用户确认，forget 后不复活；
- [ ] 每个 v1.1 能力都有独立开关、回滚路径和 v1 基线对照。

### 8.8 验收证据包

每个阶段结束必须提交：

1. 测试命令、退出码和失败重跑记录；
2. 基线 diff 或 prompt hash 对比；
3. 迁移/重建/回滚报告；
4. 安全检查结果（secret、跨 scope、未授权 API）；
5. 观测指标和原始数据位置；
6. 未关闭风险和 owner 签字。

## 9. 不应作为验收证据的内容

- 只看“能生成摘要”，不看来源和错误率；
- 只跑单元测试，不跑 prompt 顺序和 canonical transcript 回归；
- 只在一个空项目验证，不验证中文路径/空格/Windows；
- 只看平均延迟，不看 SQLite 锁和 health 死锁；
- 只看注入成功，不看删除后是否仍被注入；
- 只看模型回答“看起来对”，不看引用和证据来源。
