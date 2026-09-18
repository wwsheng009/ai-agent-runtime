# Skill 运行时优化方案（借鉴 Codex 的加载与执行机制）

状态: **部分实施（2026-09-18 审查 + 修复后）** — SK-1/2/3/7 有落地代码；审查发现 D1（预算不生效）/D4（文档模式豁免缺失）/D3（报告被丢弃）已修复，D2 观测面已扩展 chat actor 与 TUI（文档模式观测留待决策）；SK-4/5/6 未实施。详见 §8 与 `docs/analysis/skill-runtime-codex-borrowing-implementation-review-20260918.md`
日期: 2026-09-18
关联文档:
- `docs/analysis/codex-skill-loading-and-execution-analysis-20260918.md`（Codex 侧取证：逐条 `文件:行号`）
- `docs/plan/codex-skills-compatibility-analysis.md`（既有：加载/解析/发现/API 对照分析）
- `docs/plan/skill-model-driven-invocation-plan.md`（既有：模型驱动执行 P0-P2 已实施，P1b 待实施）
- `docs/skill_runtime/skill_loading_and_interaction_20260918.md`（本仓库现状与实测口径）

---

## 0. 结论先行

Codex 的核心不是"执行 skill"，而是**三层暴露 + 按需加载**：
**catalog（常驻、极简）→ 正文（按需注入/读取）→ 工具面（远端资源走专用工具）**。
本仓库当前是"结构化 runtime skill + 执行器"，机制更重，但在**上下文成本治理**与**调用可观测性**上有明显缺口。

本方案只做取长补短，**不改**我们的执行器语义（Handler/Workflow/model 工具循环全部保留），也不引入 Codex 的 MCP-resource authority 体系。

### 已存在、不在本方案内的能力（避免重复建设）

| 能力 | 现状锚点 |
|---|---|
| Codex 兼容目录发现 / frontmatter / openai.yaml policy 解析 | `backend/internal/skill/codex_discovery.go:19`、`codex_manifest.go:38-52`、`codex_model.go:64-68` |
| Codex 兼容 list API（`cwds` / `extra_roots` / `per_cwd_extra_user_roots` / `force_reload`） | `backend/internal/api/skills/codex_list.go:16-83,217-242` |
| `skills.changed` 事件（热重载启停 / 增删改 / 配置写入发布） | `handler.go:89` `skillsChangedEventType`、`publishSkillsChangedEvent`（`handler.go:1039-1041`、`config_document_handlers.go:126-128`） |
| 热重载生命周期（start/stop/reload/stats） | `backend/internal/skill/hot_reload.go:67+`、`pkg/skillsapi/client.go:3677-3721` |
| 工具结果截断 | `backend/internal/skill/executor.go:824` `truncateSkillToolResult` |
| 扫描规模保护（目录上限 + 告警） | `backend/internal/skill/scan.go:58-164`（`maxSkillDirsPerRoot`） |
| 会话级稳定工具面 + 回合级 pin（TUI `/skill` 回合化） | `cmd/aicli/commands/chat_tool_surface_stability.go:9-29`、`chat_skill_turn.go` |

---

## 1. 借鉴原则（来自 Codex 取证）

1. **catalog 与正文分离**：常驻上下文只放 name + description + 定位符；正文按需读取/注入。
2. **预算 + 降级 + 告警**：目录预算默认 `min(8000 字符, 上下文 2%)`；不足时**先截描述、再删描述，但技能不消失**，并给出可见告警（`core-skills/src/render.rs:18-27`）。
3. **使用纪律写进提示词**：触发规则（点名/描述匹配必须用、不跨轮携带）、主 agent 必须**自己完整读** `SKILL.md`（不得委派 subagent 读/总结）、按需读 references、优先跑 scripts（`render.rs:30-63`）。
4. **隐式调用是观测问题**：按 path 索引（scripts 目录 / `SKILL.md` 路径）判定"模型确实照做了"，turn 级去重 + 事件/遥测（`core/src/skills.rs:50-124`）。
5. **依赖缺失要可引导**：Codex 对 `agents/openai.yaml` 声明的 MCP 依赖给 `Install / Continue anyway` 引导（`core/src/mcp_skill_dependencies.rs:30-44`），而不是静默丢弃技能。

---

## 2. 优化项

### SK-1（P0）技能目录（catalog）与正文分离 + 元数据预算

**问题**：当前 web `/skill` 回合直接注入 `ProgramGuide` 全文（`internal/skill/executor.go:729`），aicli 目录注入亦无预算；大 skill/多 skill 时上下文不可控，且没有任何截断告警。Codex 把"目录"与"正文"分开并给目录上预算。

**设计**
1. 新增 catalog 投影：`SkillCatalogEntry{Name, ShortDescription, Description, Scope, SourceLayer, SourceDir, LocatorKind(file|resource), Locator}` + `RenderSkillCatalog(entries, budget) (body string, report RenderReport)`。
2. 预算默认 `min(8000 chars, 2% 上下文窗口)`（后者由宿主 token 预算换算；无上下文信息时用字符预算）。降级顺序与 Codex 一致：截 `Description`（阈值告警）→ 去 `Description` 只留 name+short（技能必须全量可见）。
3. `RenderReport{Total, Included, TruncatedDescriptionCount, TruncatedChars, OmittedDescriptions bool, Warnings []string}`，告警进会话可见消息（web 走既有 notice；aicli 走 debug/告警行）。
4. 正文（ProgramGuide）**单独设上限**：超限时截断并附 `[skill guide truncated: read full document at <path>]`，避免"目录省了、正文又爆"。（Codex 正文天然按需读，我们注入全文，属自保措施。）
5. 注入点收敛到同一实现，供 web `expose_skills` 回合、aicli 目录注入、`/api/runtime/skills/list|search|stats` 复用（见 SK-5）。

**落点**：`backend/internal/skill/catalog_render.go`（新）、`exposure_projection.go`、`summary.go`；`cmd/aicli/commands/skills_integration.go`、`chat_skill_turn.go`；`internal/api/skills/handler.go`。

**验收**：预算边界单测（含 0/极小预算、全部截断、超大描述）；catalog 体积对比基线（before/after 字符数与估算 token）记录进文档；技能条目不因预算消失（断言 `Included == Total`）。

---

### SK-2（P0）注入"技能使用纪律"块（progressive disclosure）

**问题**：我们只给模型"有哪些 skill / 程序清单"，没有 Codex 那套使用纪律；实践中模型容易只读摘要就动手、把技能说明转给 subagent、或跨轮沿用技能。

**设计**：catalog 之后追加 `### How to use skills`（随 web 回合与 aicli 目录注入），条目：
- 触发：点名（`/skill <name>` 或 mention）**或**任务与描述明显匹配时必须使用；多提及=全部使用；**不跨轮携带**；
- 读取：主 agent 必须**自己完整读** skill 文档（我们为 `skill.yaml`/`SKILL.md`/`prompt.md` 的组合），读到 EOF；**不得委派 subagent 读/总结技能说明**（subagent 只做任务工作）；
- 引用：`references/`、`scripts/`、`assets/` 按需，不深链、不加载无关资源；有脚本优先运行/改脚本而非重打代码；
- 协调：多技能取最小集合并声明顺序；跳过明显适用的技能要说明；
- 兜底：读不到/不适用时简要说明并降级继续。

**落点**：`backend/internal/skill/guide.go`（新，与 `ProgramGuide` 同包但分文件）；消费点同 SK-1。

**验收**：文本快照测试（两种形态：绝对路径 / 别名表）；`/skills debug` 可见（复用现有 debug 输出）。

---

### SK-3（P1）隐式调用检测与观测

**问题**：我们已经解析 `allow_implicit_invocation`（`codex_model.go:64-68`、`codex_manifest.go:49-52`），但没有"模型确实用了这个技能"的判定；无法回答"路由/注入之后模型到底照做没有"。

**设计**
1. 载入期为 Outcome 建两个索引：`byScriptsDir`、`byDocPath`（一次构建，随 registry 失效重建）。
2. 判定时机：工具执行器/文件读取路径命中索引（命令落在技能 `scripts/`；读取 `SKILL.md`/`skill.yaml`）→ 判定隐式调用。
3. turn 级去重（键 `scope:path:name`）；`allow_implicit_invocation=false` 排除。
4. 产出 `skills.invoked` runtime event（复用 `runtimeevents` 总线，字段 `name/scope/path/kind=implicit|explicit/plugin_id(可选)`），并在 usage/诊断面板可见；**仅观测，不参与权限与计费**。

**落点**：`backend/internal/skill/implicit_invocation.go`（新）、`executor.go`（工具执行后钩子）、`internal/api/skills/handler.go`（事件发布）、前端轨迹/诊断展示。

**验收**：单测（路径匹配、去重、策略排除、非技能路径不误报）；事件序列 e2e（跑技能脚本 → 事件出现且只出现一次）。

---

### SK-4（P1）缺失依赖不再静默丢弃

**状态：✅ 已实施（2026-09-18）** — `Registry.UnavailableSkill`（name/path/scope/missing_tools/reason/message）+ `RecordUnavailable/UnavailableSkills/LookupUnavailable`；loader 软跳过时登记；`list/search/stats` 加性返回 unavailable 分组；GET/execute/expose 点名 unavailable 技能返回 409 `SKILL_UNAVAILABLE` + 安装/启用引导。契约与测试见审查报告 §7.1。

**问题**：注册期对"tool 缺失"是**跳过该 skill**（`loader_softfail_test.go:43` `SkipsSkillsWithMissingTools`），即它不会被路由/执行；但列表接口走的是**独立 discovery**（`codex_list.go:100` `DiscoverCodexSkillLoadOutcome`），仍会把它列出来 —— **"列表可见"与"可执行集合"不一致，且没有任何诊断或恢复引导**。Codex 的做法是保留技能、把缺失依赖变成可安装提示。

**设计**
1. 注册结果增加 `UnavailableSkills[]{Name, Path, MissingTools[], Reason}`，目录/列表返回该分组（技能仍可见，带 "unavailable" 标记）。
2. 回合/turn 开始时若显式点名了 unavailable 技能 → 返回可操作提示（缺哪些工具、如何启用/安装），对齐 Codex 的 `Install / Continue anyway` 语义（我们落在提示 + 引导，不自动安装）。
3. `list/search/stats` 统计口径区分 `available / unavailable / disabled`。

**落点**：`internal/skill/loader.go`、`registry.go`、`summary.go`；`internal/api/skills/handler.go`、`codex_list.go`；前端技能页与 composer 提示。

**验收**：单测（missing tools 不再从列表消失；点名时的提示文案）；契约测试（列表/搜索包含 unavailable 分组）。

---

### SK-5（P1）catalog 渲染与 list/search/stats 口径统一

**状态：◐ 主体已实施（2026-09-18）** — `catalog` 投影以单一实现 `buildCatalogProjectionFromSummaries` 计算（复用 `BuildCatalogEntries + RenderSkillCatalogWithOptions`，与注入**同一实现**），加性挂到 `POST /skills/list`（Codex discovery 口径）、`GET /skills` 与 `/skills/stats`（注册表口径）；字段含 `entry_count/budget_chars/body_chars/degraded/truncated_descriptions/omitted_descriptions/fingerprint`；Codex list 缓存命中路径实时重算（预算/灰度属运行时配置）。剩余：search 响应未挂投影（结果子集，语义待定）与 `exposure_projection.go` 收口。

**问题**：注入用的 catalog、`/api/runtime/skills` 列表、`search`、`stats` 各自组装字段，预算/降级策略无法共享，前端展示与模型所见可能不一致。

**设计**：以 SK-1 的 `RenderSkillCatalog` 为**单一实现**，四处复用；`/api/runtime/skills/list` 保持既有 Codex 兼容契约（`cwds/extra_roots/force_reload` 已存在），补齐"分组 + errors + report"输出，便于前端一次拿到与模型一致的视图。

**落点**：`internal/api/skills/codex_list.go`、`handler.go`、`exposure_projection.go`。

**验收**：一致性测试（同一快照下 catalog 注入文本 == list 渲染文本的裁剪前后关系）；`codex_list_test.go` 扩展 report/errors 断言。

---

### SK-6（P2，可选）产品门控与 scope 优先级强制

**背景**：Codex 自身 `policy.products` **只解析未强制**（源码 TODO），我们也同样只解析。若产品线多，建议我们先行：在**选择/注入**处按 product 过滤，并定义同名技能优先级 `repo > user > system`。

**现状核实（2026-09-18）**：`deduplicateSkillsByName` 在本仓库**已不存在**（`docs/plan/codex-skills-compatibility-analysis.md` 的相关描述已过时）。实际语义分两层：
- **discovery 层已有 scope 优先级**：`codex_discovery.go:159-172` `codexScopeRank`（repo=0 > user=1 > system=2 > admin=3 > unknown=4），并在 `:144-155` 按 rank 排序；
- **Registry 层是"按路径共存 + name 索引偏好 legacy"**：`registry.go:74-124` 以 `skillIdentityPath` 为主键；同名不同路径时 Codex 技能只在 name 未被占用时占位（`:108-118`），legacy 优先；两个非 Codex 同名注册会被**静默忽略**（`:92-96` `return nil`，无告警）。
→ SK-6 需要对齐的是"**两层同名的口径一致性**"，而不是新增一个不存在的去重函数。

**落点**：`internal/skill/registry.go`、`router.go`、`exposure_projection.go`。

**验收**：单测（product 不匹配不可见；同名优先级稳定且可解释）。

---

### SK-7（P0）文档模式：兼容"本体是指令文档、无执行器"的 skill

**需求**：用户明确要求兼容 Codex 的"指令文档、无执行器"形态——skill 不经过 `Executor`（Handler → Workflow → executeDefault 一律不跑），只把文档注入上下文，模型用**既有工具**（bash/apply_patch/MCP…）自行照做。

**识别（两种来源）**
1. 显式：`skill.yaml` 声明 `execution_mode: document`（新增枚举值；`auto`/`model` 语义不变）。
2. 自动：Codex 兼容技能（只有 `SKILL.md`，无 handler/workflow、无可绑定工具）与纯 companion-prompt 技能 → 默认 document 模式，**不再** `HydrateSkill` 后落 `executeDefault`。

**行为对照**
| 能力 | document 模式（新增，并列） | runtime 模式（现状不变） |
|---|---|---|
| 上下文 | catalog + 纪律块 + 按需注入文档正文（对齐 Codex） | ProgramGuide/程序清单 |
| 执行 | **无**：模型直接调用既有工具 | `skill__<name>` 函数 → Executor |
| 函数面 | 不注册 `skill__<name>`（除非显式 `/skill --direct` 要求） | 注册并按策略暴露 |
| 依赖校验 | **豁免**：文档里提到的工具名只是指引，不参与 available/unavailable（SK-4 不适用于本模式） | 维持 SK-4 |
| 观测 | 依赖 SK-3 的隐式调用判定（读文档/跑 `scripts/` 即算调用） | 显式调用 + 审计 |
| 审批 | 脚本执行走既有 bash/MCP 权限策略，无技能级审批 | 同左 |

**兼容与迁移**：document 是**并列模式**，不替换默认路径；配置开关 `skills.document_mode=auto|off`（auto=按上述识别；off=保持现状），`/skill <name>` 在 document 模式下走"回合注入文档"分支（与既有 TUI 回合化一致）。

**现状核实（2026-09-18，决定自动识别的边界）**：Codex 兼容技能当前实际路径是——
1. discovery stub：`CodexSkillMetadata.ToSkill(loadBody=false)` → `Source.DiscoveryOnly=true`，body 未加载（`codex_model.go:172-178`）；
2. 执行前 hydrate：`hydrate.go:36-57` 仅对 `DiscoveryOnly` 生效，Codex 走 `Source.MetadataPath`/`codexMetadataPathForSkillPath`，`ParseFile` 以 `loadBody=true` 把 `SKILL.md` 正文写入 **`Skill.Body`**（`codex_manifest.go:59-76,134-136`），并加载 `agents/openai.yaml` 的 interface/dependencies/policy（`:137-140`）；
3. 执行：`executor.go:151-156` `resolveExecutableSkill` → 无 handler/workflow 时落 `executeDefault`，把 `Body` 当 system prompt 发起**一次独立的技能 LLM 调用**（而非主 agent 回合注入）。
→ 所以 SK-7 的自动识别条件应限定为 **`Format==Codex` 且无 handler、无 workflow、无工具绑定**；命中者从"独立子调用"切换为"主 agent 文档注入"属**行为变更**，必须由 `skills.document_mode=auto|off` 控制并附迁移说明。

**落点**：`internal/skill/skill.go`（模式字段）、`manifest.go`/`codex_manifest.go`（识别与默认值）、`loader.go`/`registry.go`（跳过工具校验与函数注册）、`internal/api/skills/handler.go`（`expose_skills` 注入正文而非 skill 函数）、`cmd/aicli/commands/`（目录与 `/skill` 分支）。

**验收**：单测（document 技能不产生 `skill__` 函数、不因缺工具被跳过、缺 handler 不报错）；e2e（`SKILL.md`-only 技能：`/skill <name>` → 回合注入文档 → 模型跑技能内脚本 → SK-3 记录隐式调用）。

---

### SK-8（P1）跨回合去重与复用（对齐 Codex 现状 + 我们的增量）

**Codex 现状（已取证，见 §7 证据表）**
- 根去重：同 path root 去重（first wins，`loader.rs:517-520`）；技能按 `path_to_skills_md` 去重，scope 优先级 **Repo > User > System**（`root_loader.rs:116-158`）。
- 提及去重：`seen` 集合 + 结构化提及屏蔽同名纯文本（`selection.rs:21-80`、测试 `injection_tests.rs:216`）。
- 注入去重：扩展已注入的 host skill 正文不重复注入（`InjectedHostSkillPrompts.contains_path`，`turn.rs:613-615,675-684`）。
- 隐式调用去重：**turn 级** `implicit_invocation_seen_skills`（键 `scope:path:name`，`core/src/skills.rs:79-89`）。
- **跨回合没有正文缓存/复用**：策略是"每回合按提及重注入"+ 提示词纪律 *"Do not carry skills across turns unless re-mentioned"*（`render.rs:48`）。
- 快照缓存：cwd/config 双缓存 + `force_reload` 绕过（`service.rs:180-186`）；插件根快照缓存（`root_loader.rs:73-86`）。

**我们的增量**
1. **回合级去重（对齐）**：同一回合内 catalog/正文/隐式事件共用同一身份键（`scope:source_layer:name` 或 path），重复提及只注入/计数一次。
2. **跨回合不累积 + 按需重注入**：不把上一回合的正文留在 history；显式点名或描述命中才重注入（与 SK-1 常驻 catalog 互补）。
3. **provider prompt cache 友好**：注入块（catalog → 纪律块 → 正文）必须**顺序稳定、内容确定、带稳定标记**；排序规则固定为 scope 优先级 → name，避免每次微变击穿 provider 前缀缓存（这是跨回合"便宜地重注入"的关键）。**现状核实**：我们的回合级注入本来就是**追加在持久 history 之后**（`internal/agent/loop.go:571-574`），且持久化时 `stripSystemMessages`（`:337`）不会把注入写回 history；另有 `PromptCacheEpoch` + `prompt_cache_epoch_break` 生成代际（`:155-157,799-806`）——SK-8 只需保持"追加 + 稳定顺序"这两条不变量。
4. **快照/正文缓存**：复用既有 hot-reload 快照与 `skills.changed` 失效通道；正文读取可按 `路径+mtime+size` 缓存，失效由 watcher 驱动。**现状核实**：hydrate 缓存已是 `路径 + mtime + size` 三要素（`hydrate.go:10-28,110-129`，测试 `hydrate_test.go:57-64` 验证 mtime 变化即失效），直接复用即可，无需另建缓存。
5. **不做"已读即跳过"**：不因"上一回合用过"就省略注入（Codex 也不这么做）；便宜化只走内容级缓存，不改语义。

**落点**：`internal/skill/exposure_projection.go`、`cmd/aicli/commands/skills_integration.go`、`chat_skill_turn.go`、`internal/api/skills/handler.go`、`hot_reload.go`（缓存失效接线）。

**验收**：单测（同回合重复提及只注入一次；跨回合未提及不注入；排序稳定=快照测试）；手工（连续三回合调用同一 skill，记录注入体积与缓存命中）。

---

### SK-9（P2）技能资源读取与边界（references / scripts / assets）

**问题**：Codex 的 progressive disclosure 依赖"模型能按同一机制读到 `references/`、`scripts/`、`assets/`"；我们目前没有技能资源的读取约定与边界说明，文档里写相对路径时模型行为不确定。

**设计**：明确本地技能以"路径披露 + 既有文件工具"读取（catalog/正文里给出可解析的定位符）；若未来引入远端/环境技能，再按同一 authority 提供专用读工具（对齐 Codex `skills.read`，本方案不实现）。同时规定：相对路径以技能目录为基准、不得跨技能目录越权读取隐藏文件。

**落点**：`internal/skill/catalog_render.go`（定位符契约）、`guide.go`（读取规则文案）。

**验收**：单测（定位符生成/别名展开）；手工（模型按文档相对路径找到脚本并执行）。

---

### SK-10（P2）指标与灰度开关

**设计**：① 指标：catalog 字符/估算 token、截断次数、注入技能数、隐式调用数、去重命中数、快照缓存命中；② 灰度：`skills.catalog_budget`、`skills.document_mode`、`skills.discipline_block` 三个配置开关，默认值经过 Phase 1 基线后确定；③ 观测面：技能页/诊断面板展示最近一次 catalog 渲染报告与 unavailable 列表。

**落点**：`internal/skill/`（指标埋点）、`internal/api/skills/handler.go`（报告字段）、前端技能页。

**验收**：指标出现在既有 runtime events/诊断输出；三个开关的组合行为有单测（默认/显式关闭）。

---

## 3. 分阶段实施

| 阶段 | 内容 | 产出 |
|---|---|---|
| **Phase 1（P0）** | SK-1 + SK-2 + SK-7 | 预算化 catalog + 纪律块 + **文档模式（无执行器）兼容**；上下文体积基线对比 |
| **Phase 2（P1）** | SK-3 + SK-4 + SK-5 + SK-8 | 隐式调用事件、不可用技能可见、口径统一、**跨回合去重/复用** |
| **Phase 3（P2）** | SK-6 + SK-9 + SK-10 | product/scope 强制、资源读取边界、指标与灰度开关 |

每阶段独立可发布；SK-1/SK-2 不改变执行语义，风险最低。**SK-7 改变的是"哪些技能走执行器"**，必须与 `skills.document_mode` 灰度开关同批上线。

> **Phase 1 状态：✅ 已实施，D1/D4/D8 已修复（2026-09-18）** — SK-1 预算修复后 120 技能场景 BodyChars=7,979（预算 8000，修复前 26,471）；SK-7 依赖校验豁免已补；SK-7 观测链已由主循环 hook 补齐（`IncludeDocumentMode` 索引；见 §8.1）；**D8**：修复 `SetAICLIConfig` 快照丢弃 `SkillsRuntime` 导致 catalog 注入与三个灰度开关在 server 侧恒不生效的问题——现在配置真正到达注入点；见 §8.2 与审查报告。
>
> **Phase 2 状态：部分** — SK-3 已落地并扩展到 chat actor（事件）、TUI（刷新索引 + 总线事件发布，替换原先的日志-only 落点）与主循环（含文档模式，事件走共享 bus），且 `skills.invoked` 已进入 runtimeobserve v1 白名单（远程按会话可查，见审查报告 §7.1）；SK-4 已实施（unavailable 可见性 + 可操作引导）；SK-5 核心已实施（list/registry list/stats 携带与注入同源的 catalog 投影）；SK-8 具备排序稳定、指纹快照与同回合去重。
>
> **Phase 3 状态：未实施** — SK-9 仅文案层部分；SK-6/SK-10 未落地（SK-10 三个灰度开关已存在，但指标与观测面未做）。

---

## 4. 验证与回归

- 后端：`go test ./internal/skill/... ./internal/api/skills/... ./cmd/aicli/commands/... -count=1`；`go build ./cmd/aicli ./cmd/runtime-server`。
- 前端：`npx vitest run`（skills 相关）、`npx tsc --noEmit`、`npm run lint:i18n`。
- 手工（web + TUI 各一次）：
  1) 同一技能目录（≥10 个技能、含超长描述）观察 catalog 注入体积与告警；
  2) `/skill <name> <args>` 回合内模型是否按纪律自己读文档、按需取 references；
  3) 改 `SKILL.md` 后 `skills.changed` → 前端列表刷新（既有能力回归）；
  4) 制造 missing tool 技能，验证其仍可见且点名时有引导提示。
  5) 放置一个 `SKILL.md`-only 技能（无 handler/workflow/工具绑定），验证 document 模式：不注册 `skill__<name>`、`/skill` 注入文档、模型用既有工具跑通脚本、SK-3 记录调用。
  6) 同一 skill 连续三回合调用：第 2/3 回合验证"按需重注入 + 顺序稳定 + 无 history 累积"。
- 记录 before/after：catalog 字符数、估算 token、技能条数（必须相等）、注入块指纹（顺序/内容稳定性）。

---

## 5. 风险与取舍

| 风险 | 缓解 |
|---|---|
| 预算裁剪改变模型可见性 | 严格沿用 Codex 降级顺序：**技能永不消失**，只截/去描述，并给可见告警 |
| 纪律块自身占用 token | 纳入同一预算上限；文案固定、可快照测试 |
| 隐式调用误报/漏报（脚本被 wrapper 包裹） | 定位为**观测信号**，不用于权限/计费；事件标注判定依据（path kind） |
| unavailable 技能参与路由 | 明确 `available` 集合才是路由/注入输入；unavailable 仅用于展示与点名提示 |
| 与既有 P1b（`skill__<name>` 函数级派发）冲突 | 本方案不改函数面；SK-2 纪律块对两条派发路径都成立 |
| document 模式改变既有 Codex 技能行为（原本 hydrate 后走 executeDefault） | 灰度开关 `skills.document_mode=auto|off` + 行为变更说明；先自动识别"纯文档技能"，再考虑扩大范围 |
| 注入块不稳定击穿 provider 前缀缓存（跨回合重注入变贵） | SK-8 强约束：顺序/内容/标记稳定，排序固定 scope→name；验收含"注入块指纹"对比 |
| document 技能声明了工具名但实际不可用 | 文档模式豁免工具校验，但 SK-4 的 unavailable 列表仍如实展示（仅展示，不阻断） |

## 6. 非目标

- 不强制单一入口：保留 `skill.yaml` + companion prompt + `SKILL.md` 兼容层，**新增** document 模式与之并列（SK-7）。
- 不替换默认执行器路径：runtime 技能（Handler/Workflow/model）语义与优先级不变。
- 不实现 MCP resource authority / orchestrator `skills.list|read` 工具面（除非引入远端技能源）。
- 不做跨回合"已读即跳过"的语义裁剪（SK-8 只做内容级缓存与去重，不改"每回合按需重注入"）。

---

## 7. 完整性审查记录（2026-09-18）

### 7.1 本轮审查发现并补齐的缺口

| 缺口 | 补入项 |
|---|---|
| 未覆盖"指令文档、无执行器"形态（用户明确要求兼容） | **SK-7**（P0，文档模式，并列于 runtime 模式） |
| 未回答"多回合调用同一 skill 的去重/复用" | **SK-8**（P1，含 Codex 取证与我们的增量） |
| 未定义 references/scripts/assets 的读取与边界 | **SK-9**（P2） |
| 未定义指标、灰度开关与回滚手段 | **SK-10**（P2）；SK-7/SK-8 各自带开关与基线 |
| 未明确注入顺序稳定性对 provider 缓存的影响 | 写入 SK-1/SK-8 约束与验收 |
| 未明确 document 模式与 SK-4 依赖校验的关系 | SK-7 "依赖校验豁免"行 + §5 风险行 |

### 7.2 审查中确认"已有、无需建设"的能力（避免重复）

详见 §0 清单；补充本轮核对结论：`skills.changed` 事件覆盖热重载启停/增删改/配置写入；`codex_list.go` 已支持 `cwds/extra_roots/per_cwd_extra_user_roots/force_reload`；扫描有目录上限与告警；工具结果有截断；TUI 已有会话级稳定工具面与回合级 pin。

### 7.3 Codex 去重 / 缓存能力证据表（供 SK-8 对照）

| 机制 | 粒度 | 证据 |
|---|---|---|
| 技能根按 path 去重（first wins） | 加载期 | `core-skills/src/loader.rs:517-520` |
| 技能按 `path_to_skills_md` 去重 + scope 优先级 Repo>User>System | 加载期 | `core-skills/src/root_loader.rs:116-158`；测试 `loader_tests.rs:2507` |
| 显式提及去重（`seen`）+ 结构化提及屏蔽同名纯文本 | 回合输入 | `ext/skills/src/selection.rs:21-80`；测试 `injection_tests.rs:216` |
| 扩展已注入的 host skill 正文不再由 core 重复注入 | 回合注入 | `core/src/session/turn.rs:613-615,675-684`（`InjectedHostSkillPrompts`） |
| host 目录不由 core 与扩展双份注入 | 回合注入 | `HostSkillsCatalogInWorldState`（`core-skills/src/injection.rs:42-48`） |
| 隐式调用去重（键 `scope:path:name`） | **回合级** | `core/src/skills.rs:79-89` |
| 跨回合正文复用 | **无（刻意不做）** | 提示词纪律 "Do not carry skills across turns unless re-mentioned"（`render.rs:48`），每回合按提及重注入 |
| 技能快照缓存 + `force_reload` 绕过 | 加载期 | `core-skills/src/service.rs:180-186` |
| 插件根快照缓存 | 加载期 | `core-skills/src/root_loader.rs:73-86` |
| 名称歧义计数（精确/小写） | 提及解析 | `core-skills/src/mention_counts.rs:8-24` |

### 7.4 核实结论（2026-09-18，四项已完成）

| # | 待确认项 | 核实结论 | 对方案的影响 |
|---|---|---|---|
| 1 | 同名合并口径与 scope 优先级 | `deduplicateSkillsByName` **已不存在**（旧分析文档过时）。语义分两层：discovery 层已有 `codexScopeRank`（repo>user>system>admin>unknown，`codex_discovery.go:144-172`）；**Registry 层**以 `skillIdentityPath` 为主键、同名不同路径时 Codex 仅补位、legacy 优先，两个非 Codex 同名注册**静默忽略**（`registry.go:74-124`，无告警） | SK-6 改为"两层口径对齐"：discovery 排序已是 Codex 语义，Registry 需补 scope 意识与同名冲突告警（至少 debug 日志） |
| 2 | hydrate 路径与 SK-7 边界 | Codex 技能当前链路：discovery stub（`DiscoveryOnly=true`，body 未载）→ hydrate 以 `loadBody=true` 把 `SKILL.md` 正文写入 **`Skill.Body`**，并加载 `agents/openai.yaml` → 无 handler/workflow 时 `executeDefault` 用 `Body` 作 system prompt 发起**独立技能 LLM 调用**（`codex_model.go:172-178`、`hydrate.go:36-57`、`codex_manifest.go:59-76,134-140`、`executor.go:151-156`） | SK-7 自动识别条件锁定为 **`Format==Codex` ∧ 无 handler ∧ 无 workflow ∧ 无工具绑定**；该切换是**行为变更**（子调用 → 主 agent 注入），必须灰度 + 迁移说明 |
| 3 | provider 前缀缓存约束 | 我们侧：回合注入**追加在持久 history 之后**（`loop.go:571-574`），持久化时 `stripSystemMessages`（`:337`），另有 `PromptCacheEpoch`/`prompt_cache_epoch_break` 代际机制（`:155-157,799-806`）。Codex 侧：catalog 为 developer 片段、正文为 user 片段，均带稳定 type markers（`available_skills_instructions.rs:48-58`、`skill_instructions.rs:24-40`）。**两边都未见 provider 专用的 cache_control/断点标记** | SK-8 落为两条不变量：**只追加（尾部）+ 顺序稳定**，且不写回 history；缓存收益需实测（记录 cache 命中/等效 token，见 §4） |
| 4 | 前端展示位 | 技能页目录：`frontend/src/pages/skills/{catalog,detail,hot-reload,stats}.tsx`；来源标签已有 `describeSkillSource`（用 `source.layer/dir/path`，`shared.ts:31-47`），列表副标题 `skillSubtitle`（`:49-58`）。**命名冲突**：`shared.ts:17-28` 已把 `"unavailable"` 用于**端点/API 不可用**分类（403/404/405/501/503） | SK-4/SK-7 新增状态字段不得复用 `unavailable`，建议 `state: "available"\|"missing_tools"\|"document"`，展示位 = catalog 列表项来源行 + detail 页 |

**残留待实测（不阻塞设计）**：provider 侧前缀缓存对我们"每回合尾部追加注入块"的实际命中率与成本收益，需在 Phase 1 用真实 provider 采样（对比同一会话连续三回合的 cached/uncached token）。

---

## 8. 实施状态

### 8.1 SK-3（隐式调用检测与观测）✅ 已实施并验证（2026-09-18）

| 项 | 落点 | 说明 |
|---|---|---|
| 索引与判定 | `backend/internal/skill/implicit_invocation.go`（新） | `BuildImplicitInvocationIndex` 建 `byScriptsDir`/`byDocPath`；`DetectImplicitInvocations` 对 shell 类工具（cwd/命令路径回溯父目录）与读文件类工具（SKILL.md/skill.yaml 精确匹配）判定命中 |
| 策略与去重 | 同文件 | 仅 `Format==Codex` 且非 document 模式参与；`allow_implicit_invocation=false` 排除；`DedupeInvocations` 键 `scope:path:name`，`Path` 用技能锚点（根目录/文档路径）保证同技能多脚本归并为一条 |
| 执行钩子 | `backend/internal/skill/executor.go` | `Executor.SetImplicitInvocationIndex` + `executeDefault` 工具循环内 `collectImplicitInvocations`，经 `attachImplicitInvocations` 写入 `ExecuteResult.ImplicitInvocations` |
| 事件发布 | `backend/internal/api/skills/handler.go` | `ExecuteSkill` 内 `publishSkillInvokedEvents`：每条命中发一条 `skills.invoked`（字段 `name/scope/path/kind/basis/tool` + `session_id`）；`Event.SessionID` 刻意留空，与 `skills.changed` 同为总线级事件，不进 A 通道 |
| 会话宿主钩子（2026-09-18 补） | `internal/chat/actor.go`、`cmd/aicli/commands/skills_integration.go` | chat actor 技能回合执行前 `RefreshImplicitInvocationIndex` 并对命中发布 `skills.invoked`；TUI 技能桥刷新索引 + 结构化日志（无共享总线） |
| 主循环观测（2026-09-18 补，含文档模式） | `internal/agent/skill_invocation_observer.go`（新）、`loop.go:act` 调用点 | 索引口径 `IncludeDocumentMode`：`read_file SKILL.md` → `doc_path`、`run_shell_command` 落 `scripts/`/根目录 → `scripts_dir`；turn 级去重（`scope:path:name`）后经 agent 总线（与宿主共享）发 `skills.invoked`，补齐"正文注入 + 主循环执行脚本"链路 |
| 契约 | `kind` ∈ `implicit\|explicit`（计划口径），`basis` ∈ `scripts_dir\|doc_path\|mention`（诊断细分）；`NewExplicitInvocation` 供点名路径标记 |

**验证（本次实测）**

- `go test ./internal/skill/... -count=1` ✅（含 SK-3 单测 12 例 + `executeDefault` 工具循环集成：两脚本命中同一技能→去重为一条、非技能路径不误报）
- `go test ./internal/api/skills/... -count=1` ✅（含 `skills.invoked` 发布：去重、字段、显式/隐式透传、空输入无事件）
- `go vet ./internal/skill/... ./internal/api/skills/...` ✅；`go build ./...` ✅

**修复后状态（2026-09-18）**

- 观测面已从 `/execute` 缝隙扩展为四类宿主：skills API `/execute`（既有）、chat actor 技能回合（事件）、TUI 技能桥（刷新索引 + 结构化日志）、主循环工具面（含文档模式技能，事件走共享 agent bus）。SK-7 验收第 5 条（document 技能 → SK-3 记录）**结构性成立**：主循环观测索引显式包含文档模式技能（`BuildImplicitInvocationIndexWithOptions{IncludeDocumentMode:true}`），`read_file SKILL.md` / `scripts` 命令均可归因。
- 仍缺：前端轨迹/诊断面板消费 `skills.invoked`（事件已在总线，可先经 runtime events 调试端点观察）——归入 SK-10 观测面。
- 灰度开关当前是编译期常量 `skillsInvokedEnabled`；SK-10 再收敛为运行时配置。

### 8.2 实施审查结论（2026-09-18）

完整审查见 `docs/analysis/skill-runtime-codex-borrowing-implementation-review-20260918.md`（含逐条 `file:line` 证据与可复现命令）。核心结论：

| 编号 | 问题 | 严重度 | 状态（2026-09-18 修复后） |
|---|---|---|---|
| D1 | SK-1 catalog 预算不生效：`trimLinesToBudget` 逐行长度与总预算比较，多条目场景零裁剪零告警（实测 120×150 字符 → 26,471 字符 / 预算 8000）；"上下文 2%" 路径为死代码 | 高 | ✅ 已修复：累计行预算（前缀+纪律块计入），降级/告警/可重入，6 个新测试；同场景 26,471 → 7,979 |
| D2 | SK-3 观测仅 `/execute` 缝隙；主 agent/TUI/文档模式未接线，SK-7 观测验收断链 | 高 | ✅ 后端闭环：chat actor 事件、TUI 刷新索引/日志、主循环观测（`IncludeDocumentMode`，含文档模式 SKILL.md/scripts 归因，turn 级去重）均接线；前端消费归 SK-10 |
| D3 | SK-10 `RenderReport` 被调用点丢弃（`handler.go:7869`、`chat_skill_turn.go:376`），无指标与观测面 | 中 | ◐ 部分：降级时结构化日志已接（budget/body/truncated/omitted）；指标与诊断面板未做 |
| D4 | SK-7 依赖校验豁免未实现（`loader.go:321-329`、`registry.go:337-346` 无 document 分支） | 中 | ✅ 已修复：registry/loader 双豁免 + `ToSkillStub` 传递 ExecutionMode + 3 个测试 |
| D5 | SK-1 无 before/after 基线；SK-8 无注入块指纹快照与三回合实测记录 | 中 | ◐ 部分：before/after 基线已入审查报告 §7.1；SK-8 指纹快照已补（`TestRenderSkillCatalog_FingerprintStableAcrossPermutation`）；三回合实测属手工验收待执行 |
| D6 | SK-10 命名漂移（计划 `skills.catalog_budget` vs 实际 `catalog_budget_chars`）；开关组合测试缺"显式关闭"路径 | 低 | ◐ 部分：显式关闭路径已补测；键名漂移建议回写文档（改键为 breaking 变更） |
| D7 | SK-4/SK-5/SK-6 整体未实施（Phase 2/3 剩余项） | 按阶段 | ◐ 部分：SK-4 已实施（unavailable 可见性 + 409 引导，14 测试）；SK-5 核心已实施（list catalog 投影与注入同源）；SK-6 未实施（需产品门控决策） |
| D8 | catalog 注入配置在 server 侧恒为 nil：`cloneAICLIRoutingConfig` 丢弃 `SkillsRuntime`，SK-1/SK-2 注入与 `catalog_budget_chars`/`discipline_block`/`document_mode` 开关从未生效 | 高 | ✅ 已修复：快照保留 `SkillsRuntime` + 2 条回归测试；修复后 config 真源（`normalizeSkillsRuntimeConfig` 的默认值）实际到达 `buildSkillExposureMessages` |
| D9 | `BuildCatalogEntries` 对 `Source==nil` 的摘要 panic（程序化注册可达；D8 修复后注入真正每回合渲染，风险放大） | 中 | ✅ 已修复：路径/别名判空降级 + `TestBuildCatalogEntries_NilSourceDoesNotPanic` |

§8.1 中 SK-3 已交付部分（判定内核、去重、事件契约、web `/execute` 发布、单测）经复核**保持有效**；其"未纳入本批次"清单按上述 D2 修订。
