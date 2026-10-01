# code.* 工具面缺口评审与修复计划（2026-09-30）

> 范围：`code_search` / `code_inspect` / `code_navigate` / `code_references` / `code_callers` 五个工具及其降级、门控、shadow 档与文档口径。
> 方法：真实会话内约 30 次工具调用（正常 / 零命中 / 非法参数 / limit 截断 / 限定名 / 反斜杠路径 / 不存在符号 / kind 过滤 / 重复读取）+ 只读子代理实现走查（file:line 证据）。
> 约束：评审轮全程只读；本文档保存后进入修复轮。相关口径：`04` §4.6、`06` §4 Phase 3、ADR-0004。

## 1. 总体结论

主链路可用：统一信封（`source/confidence/version/range/truncated/next_cursor/explanation/degraded`）结构稳定；索引不可用/零命中/查询失败均能降级到 grep/view（Degrade-Not-Fail 成立）；参数校验与只读元数据一致。

但存在 **P0 级静默错误风险**（引用索引漏报且无陈旧度信号）、**shadow 档语义泄漏**，以及若干"文档承诺 ≠ 实现"的语义缺口。

## 2. 缺口清单（按严重度）

### P0 — 可能静默出错

1. **引用索引漏报 + 无陈旧度信号（实测）**
   - `code_callers(EvaluatePlan)` / `code_references(EvaluatePlan)` 返回 5 条全部在 `planner_test.go`，漏掉生产调用点 `backend/internal/knowledge/planner.go:213`（`return EvaluatePlan(in, nodes, opts.Config), nil`，已用 view 复核存在）。
   - `classifyStoreError` 同样漏掉 `planner.go:211`；而 `planner.go` 其余调用点（204/209/249/259/290/294/336/347/358）均可查到。
   - 信封无 `snapshot_ts/staleness_seconds/completeness`，模型无法察觉结果不完整 → 影响面分析可能得出"没有生产调用者"的错误结论（ADR-0004 §1.2/D1 明令防范）。
   - **2026-10-01 结案**：漏报不可复现于当前代码（提取层 / 全新索引 / 增量重写 / 生产库四处实证绑定正常）——现场为陈旧索引快照；仍活着的索引侧缺陷（接口方法声明被误抽成 `kind=call`，`planner.go:136/142`）已由 `builtin/4` 修复。详见 CHANGELOG「收口轮」。
2. **ADR-0004「陈旧索引分级」整体未落地（走查）**
   - 无 staleness/snapshot/completeness 字段（`code_common.go:108-120`）；注册仅单一 `code_tools off|on`（`knowledge/config.go:28-33`、`manager.go:445-448`）；无描述追加、无 `knowledge.tools.stale_reader` 逃生舱；`RegisterGroup` 不存在。
   - `06` Phase 3 登记注记与 CHANGELOG 未登记该缺口。
   - **2026-10-01 结案**：信封三字段 + 三档分级注册 + 两逃生舱 + 描述变体全部落地（`ListTools` 惰性重评估 + 执行期硬守卫；`RegisterGroup` 证据有误，改用 `Registry.Register/Unregister`）。见 CHANGELOG「收口轮（三）」。
3. **shadow 档语义泄漏（走查）**
   - 唯一 `ModeShadow` 判断在 `code_search.go:101`；`code_inspect` / `code_navigate` / `code_references` / `code_callers` / `view --symbol` 在 `mode=shadow + code_tools=on` 时仍返回 `source=index`，污染灰度对比。
4. **引用精确率（实测）**：`kind=call` 结果混入 `t.Fatalf("EvaluatePlan() = ...")` 字符串与接口方法声明行；同名符号（如 `Plan`）跨符号混合，"排除同名噪音"被高估。

### P1 — 语义 / 文档承诺不符

5. **on 档"低置信（<0.5）补一次 grep 并合并"未实现**：仅 `len(hits)==0` 时补 grep（`code_search.go:109-115`）；`codeSourceIndexGrep`、`codeExploreBelow` 为死代码。
6. **fallback 丢截断信号、丢过滤条件（实测）**：`fallbackEnvelope` 只搬 Content（`code_common.go:239-253`），grep/view 的 truncated 元数据被丢弃；`code_search(lang=python)` 零命中后 fallback 静默忽略 lang；`path_prefix` 保留但输出路径变为相对前缀形式（索引为工作区相对），路径口径不一致。
7. **code_inspect 索引路径不报截断**：`limit` 裁剪正文后 `truncated` 恒 false；view 错误被 `result, _ :=` 吞掉（`code_inspect.go:104-108`）；重复读取时 `results.content` 为 view 的 `unchanged` 去重提示，信封仍 `truncated=false`。
8. **mode=off + code_tools=on 仍注册 5 个工具**（`manager.go:446-448` 只看 code_tools），与 ADR §4.4"mode=off 不注册"不符。

### P2 — 正确性 / 易用性

9. **kind 不校验（实测）**：`kind=bogus` / `kind=type` 静默零命中 → grep fallback，fallback 无法按 kind 过滤且无提示。
10. **限定名不支持（实测）**：`symbol="knowledge.Plan"` 全部 fallback；`code_search("knowledge.Plan")` 返回无关命中却标 `confidence=0.9, degraded=false`。
11. **精确名排序弱（实测）**：`code_search("PlanInput")` 13 条中类型本体排第 8，子串命中靠前；无 exact-name 加权。
12. **Windows 路径归一化不一致（实测）**：`code_navigate(members, "backend\\internal\\knowledge\\planner.go")` 返回 0 符号、`source=index`、`degraded=false`；同一反斜杠前缀在 `code_search` 正常。
13. **fallback confidence 语义混乱（实测）**：search/inspect/navigate-definition 的 fallback=0；references/callers 对不存在符号=0.8、对存在但无引用=1。
14. **其他（走查）**：`code_inspect(file_path)` 被标 `degraded=true`（正常路径）；`navigate members` 的 limit 先于精确过滤、truncated 口径不可靠；只读句柄缓存永不失效（`code_index_resolver.go:29-33/91-106`）。

### P3 — 细节

- 空 direction 落到 members 再报"需要 file_path"，错误信息误导；definition 方向写无意义的 `truncated`。
- explanation 的"命中 N 条"是返回数而非总数（limit=3 时实际 19 条，仅 `truncated=true` 提示）。
- `codeParamInt` 不接受字符串数字（`"limit":"20"` 静默用默认值）。
- `grep` 的 Phase 3 分工句在 BaseTool 描述里（`grep.go:655-656`），被 `GrepTool.Description()` 覆盖后模型不可见（`grep.go:668`）；`view` 未反向指向 `code_inspect`（闭环单边）。
- `ToolResult.Metadata` 只带 5 个字段，version/range/next_cursor/explanation 不进 metadata。

## 3. 已确认良好（勿回退）

1. 信封 8 字段齐全、JSON tag/omitempty 合理；`source` 取值语义清晰。
2. `codeClampLimit` 语义正确（≤0 取默认、>max 夹取并报 truncated）；search 20/100、refs 50/200、符号候选 12。
3. fallback 三件套完整：`source=fallback`、`degraded=true`、`fallback.tool/reason/output` + 稳定 explanation。
4. `code_search` shadow 分支本身正确；on 档零命中确实补一次 grep（仅"合并"未实现）。
5. 参数校验干净：inspect 双缺、navigate 三方向必填与非法 direction、空 query 走 schema 校验。
6. `code_inspect` 符号读取含完整 range + 带行号正文（EvaluatePlan 83 行完整返回）。
7. 解析器门控（mode=off / 库不存在 / 打开失败 / workspace 未登记 → 不可用）；注册门控 off 不注册、on 注册 5 个。
8. 5 个工具只读元数据一致；工具描述均声明"索引增强不替换、不可用降级、符号级 vs 文本分工"。

## 4. 修复计划（本轮范围）

按"低风险、可测试、不改变对外信封主结构"原则选取：

| # | 修复项 | 落点 | 验收 |
|---|---|---|---|
| F1 | shadow 统一：5 工具 + `view --symbol` 在 shadow 档一律返回 grep/view 结果（候选照算、仅内部记录） | `code_common.go`（统一 resolveIndex 返回 shadow 标志）+ 各工具 | 新增 shadow 用例：inspect/navigate/references/callers 在 shadow 下 `source=fallback` |
| F2 | kind 闭集校验；fallback explanation 明示"按文本近似，不保证 kind" | `code_references.go`、`code_callers.go`、`code_common.go` | 非法 kind 返回参数错误；callers fallback 提示语义丢失 |
| F3 | fallback 截断信号透传（grep/view metadata.truncated → env.Truncated）；fallback explanation 附过滤降级说明（如 lang 被忽略） | `code_common.go`、`code_search.go` | 新增断言：fallback 时 truncated 与 grep 一致 |
| F4 | inspect 截断与错误处理：正文被 view 截断时置 `env.Truncated`；view 失败进 explanation | `code_inspect.go` | limit 截断用例 |
| F5 | navigate 细节：路径分隔符归一化（反斜杠）；空 direction 报"direction 必填"；definition 不写 truncated；members 精确过滤后再判 truncated | `code_navigate.go` | 反斜杠路径 members 用例；空 direction 用例 |
| F6 | mode=off 不注册 code.*；resolver 只读句柄按 stat 失效重建 | `tools/manager.go`、`code_index_resolver.go` | 门控用例；替换 DB 后重开用例（可选） |
| F7 | grep 模型可见描述并入 Phase 3 分工句；view 描述补指向 code_inspect | `grep.go`、`view.go` | 描述字符串断言 |
| F8 | 低置信补量：on 档 `confidence < 0.5`（无 hits 或低相关）补一次 grep，返回 `source=index+grep` 合并结果 | `code_search.go`、`code_common.go` | 新增合并用例 |

**明确不在本轮**（登记为后续）：ADR-0004 陈旧度分级整体落地（staleness 字段/分级注册/逃生舱）；FTS exact-name 加权与限定名（`knowledge.Plan`）支持；引用索引漏报的根因（需索引侧重构 + 全量重建验证）。

## 5. 验证方式

- `go build ./...`；`go test -count=1 ./internal/toolkit/ ./internal/tools/ ./internal/knowledge/ ./internal/config/`。
- 新增/更新用例集中在 `code_tools_test.go`、`code_index_resolver_test.go`，按"工具 × 分支"覆盖 shadow、kind、truncation、路径归一化。
- 修复完成后在真实会话复测五个工具的关键路径（含 shadow 与 fallback），并把结果登记回 `06` Phase 3 登记注记与 CHANGELOG。

## 6. 修复落地记录（2026-09-30）

本轮已实施（对应 §4 的 F1–F8）：

| 修复 | 落点 | 验证 |
|---|---|---|
| F1 shadow 统一（5 工具 + `view --symbol` 不再泄漏索引结果） | `code_inspect.go`、`code_navigate.go`、`code_references.go`（共享 `runCodeRefsQuery`）、`view.go` | `TestCodeToolsShadowModeCoversAllTools` |
| F2 kind 闭集校验 + fallback 语义标注 | `code_references.go`、`code_common.go` | `TestCodeReferencesRejectsInvalidKind` |
| F3 fallback 截断透传（grep/view → `env.Truncated`）+ lang 过滤说明 | `code_common.go`（`fallbackEnvelope`/`codeResultTruncated`）、`code_search.go` | 现有 fallback 用例 + 代码复核 |
| F4 inspect 截断与读取失败标记 | `code_inspect.go` | `TestCodeInspectLimitMarksTruncated` |
| F5 navigate 路径归一化 / 空 direction / definition truncated / members 截断口径 | `code_navigate.go`、`code_common.go`（`normalizeCodePath`） | `TestCodeNavigateMembersNormalizesWindowsPath`、`TestCodeNavigateRequiresDirection` |
| F6 `mode=off` 不注册 code.*；只读句柄按 size/mtime 失效重建 | `tools/manager.go`、`code_index_resolver.go` | `TestRegisterBuiltinToolkitToolsGatesCodeTools`（新增 mode=off 断言） |
| F7 grep 模型可见描述并入 Phase 3 分工句；view 指向 code_inspect | `grep.go`、`view.go` | 描述字符串人工复核 |
| F8 on 档低相关命中补一次 grep（`source=index+grep`） | `code_search.go`、`code_common.go` | `TestCodeSearchLowConfidenceSupplement` |

验证命令与结果：

- `go build ./...` → OK
- `go test -count=1 ./internal/toolkit/... ./internal/tools/...` → `toolkit` ok / `toolkit/tools` ok（36s）/ `tools` ok
- `go test -count=1 ./internal/knowledge/... ./internal/config/... ./internal/agent/... ./internal/contextmgr/...` → 全 ok
- `go vet ./internal/toolkit/tools/ ./internal/tools/` → OK

仍未落地（后续轮次）：

1. ~~**ADR-0004 陈旧度分级**~~ **（2026-10-01 结案）**：信封 `snapshot_ts` / `staleness_seconds` / `completeness`、按陈旧度分级注册（writer 全开 / reader ≤60s 全开 / ≤900s 仅定义类 / 过旧不注册）、description 追加、`knowledge.tools.stale_reader=off` + `knowledge.tools.enabled=false` 逃生舱全部落地；`RegisterGroup` 不存在（ADR 证据 1 有误），复用既有条件注册 + `Registry.Register/Unregister`，`ListTools` 惰性重评估 + 执行期硬守卫替代事件推送。见 CHANGELOG「收口轮（三）」。
2. ~~**引用索引漏报根因**~~ **（2026-10-01 结案）**：现场为陈旧索引快照（当前代码/全新索引/增量重写/生产库均绑定正常）；索引侧修复 = `builtin/4` 接口方法声明守卫（`planner.go:136/142` 的伪调用点）+ `AdapterVersion` 升级强制全量重建；`Fatalf` 字符串误报已于 Phase 4 `insideStringOrComment` 修复。见 CHANGELOG「收口轮」。
3. ~~**FTS exact-name 加权与限定名（`knowledge.Plan`）查询支持**~~ **（2026-10-01 结案）**：跨任务路径改加权检索（exact > prefix > contains）+ 限定名/路径符号归一化；未新建 FTS 表（LIKE + CASE + `escapeLike`），`EvaluatePlan` 判定语义不变。见 CHANGELOG「收口轮（续）」。
4. ~~**`code_inspect` 的 view 去重提示**（`unchanged: ...`）作为 `content` 返回的语义~~ **（2026-10-01 结案）**：已由修复轮 2 落地（`inspectDedupHit` → `content_omitted=view_dedup` + explanation 说明，`TestCodeInspectMarksViewDedupStub`）。

## 7. 修复轮 2（2026-10-01）

承接 §6，继续收敛工具层语义缺口（不改索引/知识层）：

| 修复 | 落点 | 验证 |
|---|---|---|
| 限定名（`a.b`）尾段精确解析并前置匹配符号；命中完整限定名时 `confidence=1.0` | `code_search.go`（`withQualifiedTailHits`） | `TestCodeSearchQualifiedNameResolvesTail` |
| FTS 命中按"精确名 > 前缀 > 包含"稳定重排（`PlanInput` 本体先于子串命中） | `code_search.go`（`orderCodeSearchHits`） | `TestOrderCodeSearchHitsExactFirst` |
| explanation 改为"返回 N 条"+ limit 截断说明；限定名命中时跳过低相关补量 | `code_search.go` | 用例断言 + 代码复核 |
| view 去重命中显式标注 `content_omitted=view_dedup` 并说明 | `code_inspect.go`（`inspectDedupHit`） | `TestCodeInspectMarksViewDedupStub` |

验证：`go build ./...` OK；`go test -count=1 ./internal/toolkit/tools/ ./internal/tools/` 全绿（toolkit/tools 46s）。

**2026-10-01 收口**：ADR-0004 陈旧度分级、引用索引漏报根因、§6.3 加权检索三项全部结案（见上）；`codeParamInt` 字符串数字已补齐（`case string` + `TestCodeParamIntAcceptsNumericStrings`，13 子例）。
