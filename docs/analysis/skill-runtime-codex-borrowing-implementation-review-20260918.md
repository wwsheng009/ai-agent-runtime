# Skill 运行时优化方案（Codex 借鉴）实施审查

> 审查对象：`docs/plan/skill-runtime-codex-borrowing-optimization-plan.md`（SK-1 ~ SK-10、§3 阶段声明、§4 验收、§8 实施状态）
> 审查日期：2026-09-18
> 审查方式：以代码/测试实证为准，逐项核对"计划声称"与"实际落地"；关键结论均给出 `file:line` 或可复现命令。
> 注意：工作区含大量其它未提交改动（mcp UI、SSE 渲染、executor detach 等），本报告只对 skill 方案相关项定性。

---

## 0. 结论摘要

| SK | 计划阶段 | 审查判定 | 一句话结论 |
|---|---|---|---|
| SK-1 预算化 catalog | Phase 1 | **已实施，未达标** | 渲染链路与降级代码齐全，但**预算在常见场景完全不生效**（D1，实测 8000 预算渲染出 26,471 字符） |
| SK-2 使用纪律块 | Phase 1 | **已实施（基本达标）** | 五类条目齐备、两种 locator 形态、受 `discipline_block` 开关控制；`/skills debug` 展示未落地（轻微） |
| SK-3 隐式调用观测 | Phase 2 | **部分实施** | 仅接在 skills API `/execute` 缝隙；主 agent / TUI / 文档模式均不产出事件（D2） |
| SK-4 缺失依赖可见 | Phase 2 | **未实施** | `missing tool` 仍软跳过（`loader.go:238`），无 unavailable 分组/点名引导/统计口径 |
| SK-5 口径统一 | Phase 2 | **未实施** | `RenderSkillCatalog` 无 list/search/stats 复用；report 未进入列表契约 |
| SK-6 product/scope 强制 | Phase 3 | **未实施** | `products` 仅解析（`codex_manifest.go:256`）；registry 同名静默忽略仍在（`registry.go:94-96`） |
| SK-7 文档模式 | Phase 1 | **已实施，部分达标** | 识别/注入/函数跳过/灰度开关齐备；**依赖校验豁免未实现**（D4），观测 e2e 断链（D2） |
| SK-8 跨回合去重/复用 | Phase 2 | **弱增量/大部分为既有** | 排序稳定与同回合去重已在；缺注入块指纹快照与三回合实测记录 |
| SK-9 资源读取边界 | Phase 3 | **部分实施** | 文案层给了相对路径/别名规则；无 `LocatorKind(file\|resource)` 契约与边界单测 |
| SK-10 指标与灰度 | Phase 3 | **部分实施** | 三开关已存在（命名漂移）；**RenderReport 被调用点丢弃**，无指标与观测面 |

**三个最高优先级问题**

1. **D1（SK-1）预算形同虚设**：`trimLinesToBudget` 用"单行长度 vs 总预算"判断是否裁剪，多条目、每条描述都短于预算时**一条都不裁、无告警**。实测：120 条 × 150 字符描述、预算 8000 → 输出 **26,471 字符**（3.3×），`truncated=0 omitted=0 warnings=0`。这直接违背 SK-1 的立项目的与验收口径，且现有测试未覆盖该分支。
2. **D2（SK-3/SK-7 联动）观测只覆盖一个缝隙**：隐式调用索引只在 `POST /api/runtime/skills/{name}/execute` 设置（`handler.go:1261-1263`）；`internal/agent/agent.go:163` 与 `cmd/aicli/commands/skills_integration.go:878` 创建的 Executor 均未设置索引；文档模式技能既不进 Executor 又被索引显式排除（`implicit_invocation.go:89`）。因此 SK-7 验收第 5 条"SKILL.md-only 技能 → 模型跑脚本 → SK-3 记录隐式调用"**当前不可能通过**。
3. **D3（SK-10/SK-1）渲染报告被丢弃**：两处唯一生产调用点都是 `body, _ := RenderSkillCatalogWithOptions(...)`（`handler.go:7869`、`chat_skill_turn.go:376`），`RenderReport` 无任何落点；指标、诊断面板、灰度组合测试全部未实现。

---

## 1. D1 详述：catalog 预算不生效（SK-1，最高优先级）

**代码路径**：`backend/internal/skill/catalog_render.go:207-254`（`trimLinesToBudget`）

```go
total := 0
for _, l := range lines { total += len(l) + 1 }
if total <= budget { return lines }        // 总预算判断
// 降级 1：截描述
for i := range lines {
    if len(lines[i]) <= budget { break }   // ← 单行长度 vs 总预算：恒为 true，立即 break
    ...
}
```

**实测（临时探针，已清理）**：

| 场景 | 预算 | 实际输出 | truncated | omitted | warnings |
|---|---|---|---|---|---|
| 120 技能 × 150 字符描述（单条远小于预算） | 8000 | **26,471 字符** | 0 | 0 | 0 |
| 1 条 20,000 字符描述 | 8000 | 2,571 字符 | 1 | 0 | 0 |

**影响**：SK-1 的两条验收均未达成——"预算边界单测（含 0/极小预算、全部截断、超大描述）"只覆盖了极小预算（≤400 触发省描述分支）与正常路径；"catalog 体积对比基线"无任何记录。多技能真实目录（10+ 技能）是本方案的**核心目标场景**，恰好落在此缺陷里。

**修复方向**：改为对行序列做累计预算裁剪（按行累加，超出后先截当前行描述到阈值、再逐步省略后续行的描述；或按 Codex 的"逐条分配预算"策略），并补三条测试：多条目总和超预算、单条超长、0/极小预算；同时把 `RenderReport` 接到调用点。

**附带偏差**：`DefaultCatalogBudget(ctxWindowTokens)` 的"min(8000, 上下文 2%)"只被测试引用；生产调用点用 `cfg.CatalogBudget()`（纯字符配置，默认 8000），即"上下文 2%"路径为死代码。

---

## 2. D2 详述：SK-3 观测面与 SK-7 验收断链

**已接线（唯一）**：`handler.go:1260-1263`（`ExecuteSkill` → `SetImplicitInvocationIndex` → `executor.Execute` → `publishSkillInvokedEvents`，`handler.go:1274/8946`）。

**未接线（实测 grep 全仓 `SetImplicitInvocationIndex` 仅 1 处）**：

| 执行宿主 | 证据 | 后果 |
|---|---|---|
| 主 agent 管线 | `internal/agent/agent.go:163` 创建 Executor，未设索引 | agent chat 中经 `skillExec` 执行的技能无 `skills.invoked` |
| TUI/aicli 技能桥 | `cmd/aicli/commands/skills_integration.go:878` 创建 Executor，未设索引 | TUI `/skill` 回合无事件 |
| 文档模式（SK-7） | `handler.go:7841`、`chat_skill_turn.go:158`、`skills_integration.go:904` 走注入分支，不进 Executor；且 `implicit_invocation.go:89` 把 document 技能排除出索引 | SK-7 验收第 5 条的"SK-3 记录隐式调用"**结构性不成立** |

**前端**：`grep skills.invoked|implicit_invocations` 在 `frontend/src` 无命中——"前端轨迹/诊断展示"（SK-3 落点）未接线。
**e2e**：SK-3 验收的"事件序列 e2e（跑技能脚本 → 事件出现且只出现一次）"只有 executor 级集成测试 + 发布单测（`implicit_invocation_test.go:248`、`skill_invoked_event_test.go`），没有 HTTP 级 e2e。

**结论**：SK-3 的"判定内核 + 事件契约"已落地且质量可用，但作为"回答模型到底照做没有"的产品能力，目前只在独立 `/execute` 接口上可见；§8.1 中"仅 skills API 宿主接线"的描述准确，但方案正文 SK-7 的验收链未同步修订。

---

## 3. 其余缺陷与偏差

> 本节编号沿用 §0 / §8.2 的全局编号（D1~D7），不代表出现顺序。

### D4（SK-7）依赖校验豁免未实现
`loader.go:321-329` 与 `registry.go:337-346` 的工具校验**没有** `IsDocumentMode` 分支；`loader.registerSkills`（`loader.go:231-242`）在 `ErrToolNotRegistered` 时软跳过。对"无工具绑定"的自动识别技能无实际影响，但显式 `execution_mode: document` 且声明 `Tools` 依赖的技能，在工具缺失时仍会被静默跳过——与计划的"依赖校验豁免"（SK-7 行为对照表）不符。`document_mode_test.go` 也只测了 `IsDocumentMode*`，无 loader/registry 层豁免测试。

### D5（SK-1/SK-8）验收证据缺失
- 无 catalog before/after 基线（docs 全库检索仅命中方案自身的验收描述）。
- SK-8 的"注入块指纹（顺序/内容稳定性）"无快照测试；同回合去重靠 `buildSkillExposureMessages` 的 `seen` map（`handler.go:7817-7832`，既有/半新增），三回合手工实测记录不存在。

### D3（SK-10）指标与观测面缺位
- 指标（catalog 字符/估算 token、截断次数、注入技能数、隐式调用数、去重命中、缓存命中）无埋点；`RenderReport` 被两个生产调用点丢弃（`handler.go:7869`、`chat_skill_turn.go:376`）。
- 诊断面板/技能页无"最近一次 catalog 渲染报告"字段；unavailable 列表（依赖 SK-4）同样缺席。

### D6（SK-10）命名漂移与开关测试缺口
- 计划命名 `skills.catalog_budget`，实际配置键 `catalog_budget_chars`（`agentconfig/config.go:774`）；`document_mode` / `discipline_block` 与计划一致，默认值：document off、discipline 开、budget 8000。
- 无开关组合单测（仅 `TestRenderSkillCatalog_DisciplineBlockPresent` 覆盖"开"，未覆盖显式关闭路径）。

### D7（SK-4/SK-5/SK-6）三项整体未实施
- SK-4：全仓无 `UnavailableSkills`/`MissingTools` 结构；`loader_softfail_test.go` 明确断言"跳过缺工具技能"仍是现行语义；列表仍由独立 discovery 输出（`codex_list.go:100`），"列表可见 ↔ 可执行集合"矛盾原样保留。
- SK-5：`RenderSkillCatalog*` 仅 2 个消费点（注入），list/search/stats 未复用；`codex_list.go` 的 groups/errors 是既有能力（§7.2 已确认），无 report 字段与一致性测试。
- SK-6：`codex_manifest.go:256-260` 仅收集 `policy.products`，无过滤点；`registry.go:94-96` 两个非 Codex 同名注册仍 `return nil` 静默忽略。

---

## 4. 验收命令实测（2026-09-18）

| 命令 | 结果 |
|---|---|
| `go build ./cmd/aicli ./cmd/runtime-server` | ✅ exit 0 |
| `go test ./internal/skill/... ./internal/api/skills/... ./cmd/aicli/commands/... -count=1`（方案 §4 后端口径） | ✅ 全绿（2.2s / 25.3s / 101.3s） |
| `npx tsc --noEmit` | ✅ exit 0 |
| `npm run lint:i18n` | ✅ scanned=820, violations=0 |
| `npx vitest run src/api/runtime/skills.test.ts src/lib/composer-skill-options.test.ts` | ✅ 18 passed |
| `npx vitest run`（全量） | ⚠️ 301 文件通过 / **4 文件 10 用例失败**，其中 **1 文件属 skills 相关**：`src/lib/composer-builtin-commands.test.ts`（新增第 5 个内置命令 `skill`（`composer-builtin-commands.ts:58-62`）后，测试清单仍断言 4 个 key（test:22-27）而失败）。其余 3 个失败文件（`session-usage-panel.test.tsx`、`workspace-directory-manage-dialog.test.tsx`、`directory-group-actions.test.tsx`）与本方案无调用关系 |

**环境备注（可复现记录）**：14:24:45 首次执行方案 §4 后端命令时失败，原因是同一工作区中**并行未提交工作流**的中间态：`internal/chat/runtime_store_hardening.go`（untracked，14:24:39 写入，未使用 `time` import）与 `internal/chat/session_runtime_store.go:1445`（调用不存在的 `notifyDrops`）。该工作流于 14:4x 自行修复后，复跑全绿。此失败与 skill 方案实现无关，但说明**在工作区存在并行写入者时，§4 验收命令的结果需要复跑确认**。

**D1 复现命令**（临时探针已清理）：`backend/.scratch/budget_probe`——120 条 `SkillCatalogEntry`（描述各 150 字符）+ `RenderSkillCatalog(entries, CatalogBudget{Characters: 8000})` → `body_chars=26471, truncated=0, omitted=0, warnings=0`。

---

## 5. 整改清单（建议顺序）

| 优先级 | 项 | 动作 | 关联 |
|---|---|---|---|
| P0 | D1 catalog 预算 | 重写 `trimLinesToBudget` 为"行序列累计预算"裁剪；补测：多条目总和超预算、单条超长、0/极小预算、`Included==Total`；把 `RenderReport` 接到调用点 | SK-1/§4 |
| P0 | 前端 skill 相关红项 | 同步 `composer-builtin-commands.test.ts` 的清单断言（含 `skill`），确认 `npm run lint:i18n`/tsc 后归零 | §4 前端门禁 |
| P1 | D2 观测面 | 明确 SK-3 的目标缝隙：若要求覆盖主 agent/TUI/文档模式，需在 agent 工具执行层（而非仅 skill Executor）加 hook，并从索引中放行 document 技能；否则修订 SK-7 验收第 5 条与 §8.1 口径 | SK-3/SK-7 |
| P1 | D3 指标与观测面 | `RenderReport` 落点（日志/指标/技能页字段）；SK-10 六项指标埋点；诊断面板展示最近一次渲染报告 | SK-10 |
| P1 | D4 依赖豁免 | loader/registry 校验处对 `IsDocumentModeEnabled` 短路；补 loader 级测试 | SK-7 |
| P2 | D5 基线/指纹 | 记录 catalog before/after（字符数与估算 token）；补注入块顺序/内容快照测试与三回合实测 | SK-1/SK-8 |
| P2 | D7 未实施项 | 按 Phase 2/3 推进 SK-4（unavailable 分组+点名引导）、SK-5（list/search/stats 复用 render）、SK-6（product 过滤与同名告警） | SK-4/5/6 |
| P3 | D6 命名/测试口径 | `catalog_budget_chars` 与计划命名对齐或在计划中回写；补 `discipline_block=false` 用例 | SK-10 |

---

## 6. 审查方法说明

- 判定以"代码是否真正支撑验收口径"为准，不以"存在同名函数/文件"为准；每个 SK 的结论均有 `file:line` 或可复现命令支撑。
- 未修改任何实现代码；仅新增本报告并校准方案文档状态（`skill-runtime-codex-borrowing-optimization-plan.md` 头部状态、§3 阶段状态、§8.2）。
- SK-3 由本轮实现者自审 + 复核：判定内核、去重、事件契约、`/execute` 发布与测试经复核有效；其缺口（D2）已按同一口径计入。

---

## 7. 修复记录（2026-09-18，review 后）

### 7.1 已修复

| 编号 | 修复内容 | 证据 |
|---|---|---|
| D1 | `trimLinesToBudget` 重写为**累计行预算**裁剪：前缀/纪律块计入同一预算（SK-10 口径），降级顺序=先截描述到 100 rune、再从尾部省略描述；技能条目永不消失；rune 安全截断；新增 `RenderReport.BudgetChars/BodyChars/Degraded()` 与"预算仍超"可见告警；渲染不再改写调用方 entries（可重入/确定性） | `catalog_render.go:118-260`；新增 6 个测试（多条目预算、单条超长、CJK rune 安全、0 预算回退、纪律块显式关闭、确定性）；`go test ./internal/skill/` 全绿 |
| D1 基线 | 120 技能 × 150 字符描述、预算 8000：**before 26,471 字符 / 0 降级 / 0 告警 → after BodyChars=7,979（估算 ≈1,994 tokens）/ truncated=120 / omitted=119 / 1 条可见告警**；单条 20,000 字符描述：body=2,376、truncated=1；纪律块关闭：body=7,944 | 临时探针（已清理）；对照测试 `TestRenderSkillCatalog_MultiEntryBudgetEnforced` |
| D4 | 文档模式依赖校验豁免：`Registry.validate` 与 `Loader.CheckSkill` 对 `IsDocumentMode()` 技能跳过工具可用性校验；`SkillSummary.ToSkillStub` 补传 `ExecutionMode`（stub 路径同样豁免） | `registry.go`、`loader.go`、`summary.go`；新增 3 个测试（registerSkills / registerSummaryStubs / CheckSkill）；非文档技能缺工具仍按原语义跳过 |
| D3（部分） | 两处生产调用点不再丢弃 `RenderReport`：降级时以结构化日志落点（`skill catalog degraded to fit budget` + budget/body/truncated/omitted） | `handler.go:7869-7875`、`chat_skill_turn.go:375-380` |
| D6 | `discipline_block=false` 显式关闭路径补测；命名漂移（`catalog_budget_chars`）留待计划回写（见 7.2） | `TestRenderSkillCatalogWithOptions_DisciplineBlockDisabled` |
| D2（后端闭环） | 会话宿主观测面扩展：`Executor.RefreshImplicitInvocationIndex()` 新增；**chat actor 技能回合**执行前刷新索引并对命中发布 `skills.invoked`（payload `session_id`，事件保持总线级）；**aicli/TUI 技能桥**刷新索引 + 结构化日志；**主循环观测**（`internal/agent/skill_invocation_observer.go` 新）：索引 `IncludeDocumentMode` 涵盖文档模式技能，`read_file SKILL.md`→`doc_path`、`run_shell_command` 落 `scripts/`→`scripts_dir`，turn 级去重后经共享 agent bus 发事件，补齐 SK-7 验收第 5 条链路 | `executor.go`、`implicit_invocation.go`、`actor.go`、`skills_integration.go`、`skill_invocation_observer.go`、`loop.go`；新增 4 个单测（刷新入口、载荷契约、文档模式索引口径、主循环判定+turn 去重） |
| SK-4（D7 第一项） | 缺失依赖不再静默丢弃：`UnavailableSkill{name,path,scope,missing_tools,reason,message}` + `Registry.RecordUnavailable/UnavailableSkills/LookupUnavailable`；loader 软跳过时登记；list/search/stats 加性返回 unavailable 分组；按名解析（GET/execute/expose）命中 unavailable 返回 409 `SKILL_UNAVAILABLE` + 引导 | `unavailable.go`（新）、`skill_unavailable.go`（新）、`registry.go`、`loader.go`、`handler.go`、`codex_list.go`、`codes.go`；新增/定向 14 个测试全 PASS |
| SK-5（D7 第二项） | 口径统一：`catalog` 投影以 `buildCatalogProjectionFromSummaries` 为唯一实现（复用 `BuildCatalogEntries + RenderSkillCatalogWithOptions`），加性挂到 `POST /skills/list`、`GET /skills`、`/skills/stats`；字段含 budget/body/degraded/truncated/omitted/fingerprint；Codex list 缓存命中路径实时重算 | `codex_list_catalog.go`（新）、`codex_list.go`、`codex_list_cache.go`、`handler.go`；6 个测试（codex→summary 映射、投影一致性×2、空输入省略、注册表前置条件、指纹快照） |
| SK-3 远程观测补口 | `skills.invoked` 纳入 runtimeobserve v1 白名单（远程 invoke 实测暴露：事件只走总线，观测面不可见）：专用低敏投影（name/scope/kind/basis/tool + has_path + step；路径与正文不导出），总线级事件把载荷 session_id/trace_id 提升到 correlation，`?session_id=` 查询即可命中；已知类型目录同步登记，不再计 filtered_by_type | `runtimeobserve/{model.go,projector.go,known_types.go}`；5 个新测试（低敏投影与会话提升、事件会话优先、空载荷仍投影、目录不变量、collector 端到端会话过滤+防串场） |
| SK-3 远程观测补口（二）：TUI 发布侧 | 新会话实测两段暴露：①TUI 技能桥因历史前提（"本地无共享事件总线"）只打日志不发事件；②补上发布器后 `/skill` 回合仍无事件——屏幕证据显示该回合由**程序工具**（`bash`）完成，根本不经过 `SkillFunction.Execute`。最终设计：显式调用在 **pin 解析（回合派发）点** 发布（kind=explicit/basis=mention，name/scope/path 取 hydrated 技能定义，缺失退化为去前缀函数名）；`SkillFunction.Execute` 继续发布隐式命中与点名函数的显式调用；同回合去重键含 kind（pin 事件与随后点名同一技能只发一条，隐式命中不受抑制），`sendMessage` 入口按回合重置；事件保持总线级、session_id 取 `session.RuntimeSession.ID`；无 host/EventBus 时退化为结构化日志 | `cmd/aicli/commands/{chat.go,chat_send.go,chat_skill_turn.go,skills_integration.go}`；`skills_invocation_observation_test.go` 4 测试（总线级载荷契约、无发布器安全退化、派发点发布+回合内去重+跨回合重置、发布器→collector→按会话查询跨层闭环）；**live 验证（2026-09-18，session_20260918154843_H7vFgX4H）**：远程 `POST /web/api/invoke` 执行 `/skill run_shell_command echo OBS_OK` → `GET /api/runtime/observe/v1/events?session_id=…` 命中 1 条 `skills.invoked{kind:explicit, basis:mention, name:run_shell_command, scope:system, has_path:true}`（该回合模型实际用 `bash` 程序工具完成，派发点仍正确记录；同回合无重复事件，去重生效） |
| D8（本批复核新发现，高） | **catalog 注入配置在运行时恒为 nil**：runtime-server 唯一注入点 `SetAICLIConfig(cfg)` 的快照函数 `cloneAICLIRoutingConfig` 只拷 AICLI 路由字段、**丢弃 `SkillsRuntime`**，而 catalog 注入（SK-1/SK-2）与 `catalog_budget_chars`/`discipline_block`/`document_mode` 都经 `runtimeSkillsConfig()` 读它——即 Phase 1 的注入与三个灰度开关在 server 侧从未生效且无告警。修复：快照保留 `SkillsRuntime` | `handler.go:cloneAICLIRoutingConfig`；回归测试 `handler_aicli_config_test.go`（保留 + 缺省 nil 两条） |
| D9（本批复核新发现，中） | `BuildCatalogEntries` 对 `Source==nil` 的摘要 `panic`（程序化注册/summary stub 可达；修复 D8 后 catalog 注入才真正每回合渲染，路径会被放大） | `catalog_render.go:BuildCatalogEntries` 判空降级（路径/别名置空）+ `TestBuildCatalogEntries_NilSourceDoesNotPanic` |
| SK-8 验收缺口（D5 第二项） | 补"排序稳定=快照"：同一技能集合任意发现顺序渲染逐字节一致 + sha256 指纹一致（prompt cache 前缀安全） | `TestRenderSkillCatalog_FingerprintStableAcrossPermutation` |
| 前端红项 | `composer-builtin-commands.test.ts` 同步第 5 个内置命令 `skill`（kind=popupSelect） | `npx vitest run src/lib/composer-builtin-commands.test.ts` → 10 passed |

### 7.2 仍未闭环（有意保留，附理由）

| 项 | 状态 | 理由 / 后续 |
|---|---|---|
| D2 残留：前端消费 `skills.invoked` | 未做 | 后端四类宿主均已发事件且可经 runtime events 调试端点观察；前端轨迹/诊断展示属 SK-10 观测面 |
| D3 残留：指标/诊断面板 | 未做 | 当前仅结构化日志；unavailable 列表已进 list/search/stats（SK-4），"最近一次渲染报告"面板属 SK-10 |
| D6：配置键命名 | 未处理 | 实际键 `catalog_budget_chars`；改键名是 breaking 变更，建议方案文档回写而非改代码 |
| D7：SK-5/SK-6 | 未实施 | SK-4 已实施（见 §7.1）；SK-5（catalog/list/search/stats 口径统一）与 SK-6（product 门控）仍为 Phase 2/3 规划项 |

### 7.3 本批验证命令

- `go build ./...`（backend）→ exit 0
- `go test ./internal/skill/... ./internal/api/skills/... ./internal/chat/... ./cmd/aicli/commands/... -count=1` → **全绿**（2.4s / 25.8s / 20.2s / 96.3s；含本批新增的预算、豁免、刷新与载荷测试）
- `npx vitest run src/lib/composer-builtin-commands.test.ts` → 10 passed
- `npx tsc --noEmit` → exit 0；`npm run lint:i18n` → scanned=820 / violations=0
- 前端全量剩余红项：3 个文件（`session-usage-panel.test.tsx`、`workspace-directory-manage-dialog.test.tsx`、`directory-group-actions.test.tsx`）经复跑确认与 skill 方案无关（目录管理/用量面板并行工作流）；skills 相关红项（`composer-builtin-commands`）已清零
