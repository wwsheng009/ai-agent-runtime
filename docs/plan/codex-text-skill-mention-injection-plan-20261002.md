# 方案：借鉴 Codex 文本类 Skill 使用方式（`$` mention + 多技能回合注入）

> 状态：**方案 v1.3（已拍板，待实施；Q1 于 2026-10-02 按跨协议稳定性重审修订）**
> 审查报告：`docs/plan/codex-text-skill-mention-injection-plan-20261002-review.md`
> 决策记录：见 §8（2026-10-02 全部决议；Q1 由 system 改为 user；Q11/Q12 相对建议小幅加严）
> 日期：2026-10-02
> 作者：主会话（基于 `E:\projects\ai\codex` 代码走查 + 本仓库现状取证）
> 关联文档：
> - `docs/analysis/codex-skill-loading-and-execution-analysis-20260918.md`（Codex 加载/执行机制分析）
> - `docs/plan/tui-skill-turn-invocation-plan.md`（现状 `/skill` 回合 pin，已实施）
> - `docs/plan/skill-model-driven-invocation-plan.md`（模型驱动调用，已实施 P0/P1/P2）
> - `docs/plan/skills-exposure-implementation-and-test-plan.md`（技能暴露策略，SK-1/SK-2/SK-7）
> - `docs/skill_runtime/current_architecture.md`、`docs/skill_runtime/aicli_skills_usage.md`

---

## 0. 摘要（TL;DR）

Codex 对**文本类 skill**（SKILL.md 说明型，无执行器）的使用方式是"**目录常驻 + `$` 提及 + 正文注入**"：
技能不是工具函数；用户在 composer 里用 `$skill-name` 提及（可多个），提交时 core 把所有被提及 skill 的
`SKILL.md` 正文作为**当回合上下文片段**整体注入，模型按纪律自行用常规工具执行。

本仓库现状是"**skill 即函数**"（`skill__<name>`）+ `/skill` 单技能回合 pin。本方案建议为文本类 skill
**增量引入** 一套 Codex 风格路径（不改 handler/workflow 技能语义）：

| 能力 | Codex | 本仓库现状 | 本方案 |
|---|---|---|---|
| 技能可见性 | 常驻 catalog（developer 片段 + 预算降级） | pin 回合的 guide 内含 catalog（`buildSkillCatalogText`） | 增加"会话常驻 catalog"开关，复用现有渲染与预算 |
| 触发语法 | `$skill-name`（可多个，`$` 弹出补全） | `/skill <name>`（一次一个）、prompt 提及（exposure 路由） | 新增 `$name` 解析 + composer `$` 补全；`/skill` 保留 |
| 多技能 | 多提多注，同一回合全部注入，纪律约束顺序 | 单技能 pin；多技能需分回合或靠路由 | 多提及 → 按目录顺序去重后全部注入（默认上限 4） |
| 注入内容 | SKILL.md 正文（`<skill>` 片段，user 角色） | `skill__X` 函数调用返回"已加载技能指令"文本 | 文本类 skill 直接注入正文，跳过函数间接层 |
| 执行 | 模型用常规工具照做 | 同（默认路径），另有 handler/workflow 桥 | 文本类对齐 Codex；handler/workflow 不变 |
| 函数面 | 无 | `skill__*` 注册并可被 pin/暴露 | 交互路径对文本类默认不再暴露函数（兼容期双路径可灰度） |

---

## 1. 背景与目标

### 1.1 为什么借鉴

1. **当前函数形态带来额外一跳**：模型要先把 `skill__<name>` 当工具调用一次，运行时再把正文塞回工具结果；
   对纯说明型技能这是冗余动作，且描述里不得不写"不要再重读 SKILL.md/不要重复调用"之类的约束。
2. **单技能 pin 限制**：`/skill` 一次只 pin 一个技能（`SendSkillTurnRequest.SkillName` 单值、
   `session.pendingSkillTurn` 单槽），组合任务（如"按品牌规范写一份 docx"）需要两个技能同时在场。
3. **Codex 的渐进披露 + 多技能纪律已被验证**：常驻目录（轻量）+ 提及才注入正文（重），配合
   "multiple mentions mean use them all / minimal set + 说明顺序 / 不跨回合携带"的纪律文案，
   上下文成本与选择确定性都更可控。
4. 本仓库已有可复用的地基：`@` 文件补全、slash 补全弹层、catalog 渲染与预算、异步能力面挂载等待、
   turn 级消息通道（`TurnSystemMessages` 的 prompt-only 语义）与非持久 system-reminder user 通道
   （`NewSystemReminderMessage`）——改造是"接线"而非"造轮子"；角色策略见 §4.12。

### 1.2 目标

- **G1**：文本类 skill 支持 `$skill-name` 提及（composer 补全 + 纯文本解析），一个回合可提及**多个** skill。
- **G2**：被提及 skill 的正文（或 ProgramGuide，按文档模式判定）在本回合注入，顺序、去重、预算、失败降级
  与 Codex 语义对齐；模型无需函数调用即可获得指令。
- **G3**：技能目录对模型常驻（可开关），携带多技能纪律块；预算与降级复用现有 `RenderSkillCatalog`。
- **G4**：`/skill`、`/skills`、`/call`、handler/workflow 技能、headless/JSON 投影全部**零回归**。
- **G5**：可观测（turn metadata + `skills.invoked` 事件扩展）、可灰度（配置开关）、可回滚。

### 1.3 非目标

- 不引入 Codex 的执行环境/云技能/`skills.read` 资源读取体系（本仓库技能目录均在本地文件系统）。
- 不改 handler/workflow 技能的确定性直执行与技能桥语义（`/skill --direct`、`options.execution=bridge` 不动）。
- 不做技能市场、权限、配额（沿用 `skills_runtime` 既有治理）。
- 不在本期实现"技能间依赖/组合编排"（多技能=并列注入，由模型按纪律协调）。
- 不做 `[@name](skill://path)` 链接式提及的完整语法（P2 可选，先支持纯 `$name`）。

---

## 2. Codex 参考实现（已核实，附行号）

### 2.1 使用链路

| 环节 | Codex 行为 | 代码位置 |
|---|---|---|
| 常驻目录 | `## Skills` + intro + `### Available skills`（name+desc+定位符）+ `### How to use skills`，developer 角色片段，预算 8000 字符 / 上下文 2%，超限先截描述再去描述、条目永不消失 | `ext/skills/src/catalog_prompt.rs:81-106`；`skills/src/render.rs`（经由 09-18 分析文档 §1.5） |
| 提及语法 | sigil `$`；`$name` 与 `[$name](skill://path)` 两种；`tool_kind_for_path` 区分 skill/app/mcp/plugin | `skills/src/mentions.rs:41,79-86,43-55` |
| TUI 补全 | `$` 直接打开技能列表（菜单提示 "press $ to open this list directly"）；弹窗模糊匹配 display_name/name，显示 `[Skill]`/tags；选中插入 `$skill_name` 并绑定 path | `tui/src/chatwidget/skills.rs:25-27,33`；`tui/src/bottom_pane/chat_composer.rs:4222-4229`；`tui/src/bottom_pane/skill_popup.rs` |
| 提交解析 | mention binding + 文本 `$name` → `UserInput::Skill { name, path }`（先 binding 后文本，按 path 去重） | `tui/src/chatwidget/input_submission.rs:304-331` |
| 多技能选择 | 结构化输入优先；文本提及**按技能发现顺序**遍历；rank by path 优先、纯名需唯一且无 connector 冲突；禁用/歧义/失效跳过；路径去重 | `skills/src/selection.rs:42-109,164-196` |
| 注入 | 逐技能读完整 `SKILL.md`，每个技能一个 `<skill><name>…</name><path>…</path>正文</skill>` 片段（user 角色，content kind=`skills.selected_skill_instructions`），同一回合全部注入；agent-plugin 技能超限截断 + warning | `ext/skills/src/host_prompt.rs:69-108`；`ext/skills/src/fragments.rs:76-110`；`core/src/session/turn.rs:1063-1097` |
| 多技能纪律 | "…**Multiple mentions mean use them all.** Do not carry skills across turns unless re-mentioned."；"If multiple skills apply, **choose the minimal set** … and **state the order** you'll use them." | `ext/skills/src/catalog_prompt.rs:8,17` |
| 依赖联动 | 被提及 skill 声明的 MCP 依赖自动安装/提示 | `core/src/session/turn.rs:988-1006`；`core/src/mcp_skill_dependencies.rs` |
| 观测 | 每个被注入 skill 上报 `codex.skill.injected`（status ok/error，invoke_type=explicit） | `core/src/skills.rs:38-119` |

### 2.2 与我们模型的差异（关键）

- Codex：**技能没有函数/没有执行器**，正文即指令；模型用既有 shell/apply_patch/MCP 工具照做。
- 本仓库：技能以 `skill__<name>` 注册进函数目录，走工具调用管线；默认路径是把技能指令注入主循环，
  handler/workflow 技能另有桥执行。因此本方案是"**为文本类技能开一条与函数路径并行、且默认优先的提及注入路径**"，
  而不是替换整个技能运行时。

---

## 3. 现状盘点（本仓库，附行号）

| 能力 | 现状 | 位置 |
|---|---|---|
| 技能注册 | 每个技能 = `SkillFunction`（`skill__<name>`，prompt 必填），注册进 FunctionCatalog | `backend/cmd/aicli/commands/skills_integration.go:321,422-441,1006+` |
| 常驻性 | 技能函数**不常驻**：`skillToolSurface.ListTools` 故意不列出，按需解析执行 | `backend/cmd/aicli/commands/chat_skill_tool_surface.go:26-39,55-60` |
| 显式调用 | `/skill <name> <args>` → 一次性 pin（guide system 消息 + skill 函数/程序叠加 + allowed/disallowed 收窄），`--direct` 保留直执 | `chat_skill_picker.go:187`；`chat_skill_turn.go:62-104,148-249` |
| 回合通道 | actor 路径 `SubmitPromptOption.TurnSystemMessages`（当前单条 guide）；chatcore 路径把 guide append 进请求历史 | `chat_actor_executor.go:271-278`；`chat_core.go:194-197` |
| catalog | `buildSkillCatalogText`（name+description+定位符，预算/纪律块可配），目前**只在 pin 回合**注入 | `chat_skill_turn.go:420-460`；配置 `catalog_budget_chars`/`discipline_block`（`agentconfig/config.go:921-926`） |
| 异步能力面 | 首帧后后台 discover + await 时挂载（attach）；斜杠命令需先 await（本次修复 `09fc19b2`） | `chat_capabilities_async.go`；`command_invoke.go:resolveDirectCallableFunctionName` |
| 提及补全 | `@` 路径补全（Tab，`onComplete` 通道）已实现；slash 补全使用 `ownedPopupBelowPrompt` 弹层模式 | `chat_mention_completion.go:159-215`；`chat_composer.go:214-219`；`chat_slash_completion_controller.go:331-373` |
| 技能选择器 | `/skills` 全屏选择器 + per-skill 启停（写 `skills_runtime.disabled_skills`） | `chat_skill_picker.go:15-186`；`skills_persistence.go` |
| 文档模式 | `document_mode`（auto/off）、`argument_substitution`、`IsDocumentModeEnabled`、`skillUsesDefaultExecution` | `agentconfig/config.go:915-930`；`skills_integration.go:670-675` |
| 文本类判定 | `Handler == nil && !HasWorkflow()`（说明型）；文档模式可再区分 | `skills_integration.go:667-675`；`internal/skill` 对应实现 |


---

## 4. 方案设计

### 4.1 范围：文本类 skill 的判定

- **文本类（本方案生效范围）**：`skill.Handler == nil && !skill.HasWorkflow()`（即现有
  `skillUsesDefaultExecution`，`skills_integration.go:667-675`）；其中 `IsDocumentModeEnabled(cfg.DocumentModeAuto())`
  的技能按文档模式处理。文档模式开关沿用 `skills_runtime.document_mode`（auto/off，默认 off）。
- **非文本类（handler/workflow/桥）**：行为不变——仍由 `/skill` pin + `skill__` 函数 + 技能桥执行；
  被 `$` 提及时 P0 忽略并记 debug（见 4.6 D3）。
- 文本类技能**仍保留** `skill__` 注册与 `/call` 能力（API/兼容/调试），但交互式默认路径改为提及注入（P3 默认隐藏函数面，可回退）。

### 4.2 触发语法与解析

- **sigil**：`$`；名称字符集 `[A-Za-z0-9_-]`（与 Codex 一致），大小写不敏感匹配技能名。
- **忽略常见环境变量**：`$HOME`、`$PATH`、`$PWD`、`$USER`、`$TEMP`、`$env` 等维护一份小名单，
  命中即不视为 mention（对齐 Codex `is_common_env_var`）。
- **未知名称不报错**：`$foo` 若不在技能目录中，视为普通文本（不注入、不弹错误）。
- **只解析已知技能**：候选集 = 已加载且未禁用（`skills_runtime.disabled_skills`）的文本类技能；
  候选之外的 `$name` 一律忽略 → 天然规避 `$100`、`$var` 误判。
- **顺序**：按 **catalog 顺序**（技能发现顺序，稳定排序），而非文本出现顺序；保证跨回合/跨会话前缀稳定。
- **去重**：同回合内同名或同 canonical path 只注入一次；与 `/skill` pin 的 skill 去重（复用 pin 不重复注入）。
- **歧义**：同名多技能（不同层/插件）且无法用 path 唯一化时，跳过并记 `ambiguous` 诊断（不猜测）。
- **上限**：`mention_multi_limit`（默认 4）；超出的提及按 catalog 顺序截断并给一条汇总提示。
- **大小写策略（已拍板 Q9）**：默认"大小写不敏感"以贴合本仓库既有 `findExplicitSkillMentions`
  （`skills_integration.go:209-260`）行为；与 Codex 文本精确匹配（`skills/src/selection.rs:164-196`）
  属**有意偏差**，需专门单测固化并在用户文档说明。
- **绑定优先（P2）**：composer 绑定产生的 `$name` 带 path，解析时 path 命中优先；path 失败时
  阻断同名文本兜底（对齐 Codex `blocked_plain_names`，`skills/src/selection.rs:62,171-173`）。
- **环境变量形态**：除 Codex env 名单外补充 PowerShell 常见形态（`$env:FOO`、`$null`、`$_`），
  纯数字（`$100`、`$1`）一律不作为 mention。
- **路径归一化**：注入文本与 fingerprint 使用的技能路径统一 `/` 分隔（`\`→`/`），保证
  Windows/跨会话逐字节稳定（对齐 Codex `normalize_skill_path`，`skills/src/mentions.rs:75-77`）。
- **P2 可选**：`[$name](skill://path)` 链接式提及（精确 path 优先）；代码块/行内代码内不解析；
  mention 在 composer 内高亮。

### 4.3 TUI：`$` 技能补全

新增 `backend/cmd/aicli/commands/chat_skill_mention_completion.go`（与 `chat_mention_completion.go` 并列）：

| 项 | 设计 |
|---|---|
| 触发 | 光标前 token 匹配 `\$[A-Za-z0-9_-]*$`；`@`/`/` 逻辑互不抢占（`/` 仅行首、`@` 仅路径、`$` 仅技能） |
| 候选 | 已挂载 catalog 的文本类技能（name/display name/tags），禁用名单过滤；复用 `chatSkillCatalogEntries` 数据源 |
| 唯一命中 | 直接补全为 `$<name> `（尾随空格），保留其余输入 |
| 多命中 | 弹层（复用 `showOwnedPopupBelowPrompt`，owner=`skill_mention_completion`；参照 `chat_slash_completion_controller.go:331-373`），↑/↓ + Tab/Enter 选择，Esc 关闭；最多 40 行 |
| 装载中 | 能力面未 attach：不弹层（或状态行提示"技能装载中"）；解析侧由 `awaitChatCapabilitiesForTurn` 兜底 |
| 门控 | 复用 `canOpenChatSkillPicker` 的 lease/popup/run 检查，避免与全屏选择器、busy turn 冲突 |
| 插入绑定 | 记录 `skill name → path`（会话内 map），提交时若 path 可用则优先按 path 解析（对齐 Codex structured-first 语义） |

### 4.4 回合装配：多技能正文注入

新增 `backend/cmd/aicli/commands/chat_skill_mentions.go`：

```
collectSkillMentionNames(prompt string, known map[string]string) []mention   // 词法：$name / 链接式，忽略 env
resolveMentionedTextSkills(session, mentions) (*skillMentionSelection, diag) // await 挂载 → 按目录顺序解析/去重/歧义/禁用/上限
buildSkillMentionFragments(session, selection) ([]runtimetypes.Message, diag) // 正文/截断/预算/失败降级
```

**注入形态**（建议 D2：直接正文，Codex 对齐）：

```text
## Skill: brand-guidelines
<skill name="brand-guidelines" path="/…/.agents/skills/brand-guidelines/SKILL.md">
<SKILL.md 正文（经 SubstituteSkillText 展开 ${SKILL_DIR} 等，$ARGUMENTS 为空）>
</skill>
```

- 每个技能一条 **user 角色片段**（Q1 重审修订）：`<skill name="…" path="…">正文</skill>`；pin guide
  （若有）在前、mention 正文在后，名称稳定排序；实现复用现有非持久 user 通道
  `NewSystemReminderMessage`（`internal/agent/system_reminder.go:106-114`，注释明确 "Role remains user
  so providers that only allow system at turn start still accept it"）。
- **参数占位（已拍板 Q7）**：mention 路径下 `$ARGUMENTS` 置空；仅当正文包含 `$ARGUMENTS`/`${ARGUMENTS}`
  时，片段头部加一行"（未提供显式参数）"，避免模型臆造参数。
- **角色选择（Q1 重审修订，跨协议稳定性）**：**回合级内容一律 user 角色**，不再用 system。证据：
  - Anthropic 适配器把**非 leading** system 改写为 user（`llm/adapter/anthropic.go:140-147`）；
  - Gemini 适配器把 system 映射为 **model**（`llm/adapter/gemini.go:597-608`），system 注入会变成"模型发言"；
  - OpenAI 兼容回退把 system 合并进首条 user（`internal/skill/executor.go:1378-1452`）；
  - Codex/Responses 只把 leading system 提升为 instructions，非 leading system 的角色语义随上游而异
    （`llm/adapter/codex.go:601-632`）。
  结论：user 角色在全部协议中**不重写、不重定位**；system 只允许用于"leading 前缀/顶层 instructions"。
- **不注入** ProgramGuide+函数加载提示（文本类没有可执行程序，避免多一跳与重复正文）；
  workflow 步骤信息如需保留，等 P2 评估后再决定是否附加精简列表。

**注入点**：

| 路径 | 位置 | 变更 |
|---|---|---|
| actor（交互主路径） | `chat_actor_executor.go:271-278` | mention fragments 走 user 角色非持久通道（新增 `TurnUserContextMessages`，或复用 system-reminder，kind=`skill_instructions`）；pin guide 保持现状并去重 |
| 共享 chatcore | `chat_core.go:194-197` | 同一通道 append user 片段进请求历史（与现有 guide 逻辑并列） |
| headless/JSON | 同 chatcore | 默认生效；`/skill` 投影不变 |

**只对用户发起回合注入**：`ContinuationPrompt == ""` 且非系统生成输入（goal continuation、guardian、
subagent bootstrap、team 自动唤醒）——对齐 Codex 对 guardian 输入跳过 mention 的做法。

**预算与失败**：

| 项 | 默认 | 行为 |
|---|---|---|
| 单技能上限 | 32768 字符 | 截断 + attention 提示行 + debug 记 `truncated` |
| 回合总量 | 65536 字符 | 按 catalog 顺序保留到上限，其余记 `limit_skipped` + 提示 |
| 技能数 | 4 | 超出按 catalog 顺序取前 N |
| 读取失败 | - | 该技能跳过；UI/状态行警告 + `skill_inject_skipped` 诊断；**不向模型注入失败桩**（对齐 Codex），模型如需可用文件工具兜底 |
| 全部失败 | - | 无注入，回合照常执行；若文本类函数面仍开启，模型可回退调用 |

**幂等与可见性（修订 H1）**：回合内 set 去重；跨回合**不主动重复注入**（纪律："do not carry across
turns unless re-mentioned"）。注意本仓库 turn 级注入（`TurnSystemMessages` / 回合 system 消息）是
**prompt-only、不落盘**（`internal/agent/loop.go:688-695`，标记 `Durable=false`，落盘时经
`DurableMessagesForPersist` 剥除，`internal/agent/system_reminder.go:389-410`）：
- 后续回合默认看不到上一回合注入的正文 → 天然满足"不跨回合携带"，无需跨回合去重记录；
- 用户再次提及 → 再次注入（与 Codex 一致）；
- 若未来希望正文进入持久历史（例如 resume 后继续同一任务），需单独提案并评估上下文成本。

### 4.5 常驻技能目录（catalog）

- 复用 `buildSkillCatalogText`（`chat_skill_turn.go:420-460`）与 `internal/skill.RenderSkillCatalog`
  （预算默认 min(8000 字符, 上下文 2%)，降级：截描述 → 去描述，条目永不消失）。
- 新增 `skills_runtime.catalog_resident`（默认 off，P1 灰度）：与 pin 回合的 guide catalog 去重
  （catalog 已常驻时，pin guide 只保留 ProgramGuide）。
- **注入位置（修订 H2 + Q1 重审）**：常驻 catalog 落在**会话级稳定前缀**——紧跟 leading system 的第一条
  **user 上下文片段**（`<system-reminder kind="skills_catalog">` 包裹），fingerprint 不变时逐字节稳定；
  **不使用 leading system 承载 catalog**（Gemini 会映射为 model 角色，造成指令角色反转）；禁止每回合追加
  到历史尾部（击穿前缀缓存、挤压 preflight）。重建触发条件仅限技能快照 fingerprint 变化或配置变更。
- **路径与排序稳定性**：条目按规范化名称排序；路径统一 `/`；渲染与 fingerprint 复用
  `internal/skill` 现有实现（已有 `FingerprintStableAcrossPermutation` 测试）。
- **纪律块**在现有 SK-2 文案上扩展（对齐 Codex `catalog_prompt.rs:8,17`）：

```
### How to use skills
- 用户点名（$SkillName 或纯文本）或任务与描述明显匹配时必须使用；**多个提及表示全部都要用**。
- 若多个技能适用：选覆盖请求的最小集合，并用一句话说明使用顺序。
- 技能正文只在被点名的回合注入；**不要跨回合携带**，除非用户再次提及。
- 注入的正文已在本回合上下文中：**不要再用文件工具重读 SKILL.md**。
- **不要把技能指令的阅读、总结或解释委派给子代理**（子代理可以执行技能授权的任务工作）。
- 未在目录中的名字不要臆造；读取失败时简短说明并继续。
```

### 4.6 与 `skill__` 函数 / `/skill` pin / `/call` 的关系

| 场景 | P0 行为 | P3（默认切换后） |
|---|---|---|
| 纯文本提及 `$a $b`（无 /skill） | 注入 a、b 正文（若为文本类） | 同；文本类函数不再进交互工具面 |
| `/skill a <args>` | 现状 pin（guide + 函数/程序叠加）；prompt 中额外提及 `$b` → 追加 b 注入；a 不重复 | 同 |
| `/call skill__a <args>` | 不变（显式函数路径永远可用） | 同（含文本类，便于调试/脚本） |
| handler/workflow 技能被 `$` 提及 | 忽略 + debug（D3）；用户可用 `/skill` 或 `/call` | 可选：自动转 pin 语义 |
| 文本类 skill 的函数暴露 | 保留（exposure 路由仍可命中） | 交互请求面不再暴露；`/call`、API、exec 保留 |
| headless/JSON/远程 invoke | 解析/注入生效；投影文本不变 | 同 |

- **D3 建议**：P0 只对文本类生效；P3 再评估"提及非文本类技能 → 自动转 pin（等价 /skill）"，避免早期语义混淆。
- **依赖联动（已拍板 Q11）**：P0 不检查、不安装；P1 增加"技能声明工具/依赖不可用"的回合提示（不阻断、
  不安装）；自动安装不在本方案范围，另立提案。
- **防双份正文**：若文本类函数在 P3 仍偶发暴露，函数 `Execute` 对"本回合已注入该技能"返回
  一行短提示（"指令已在本回合上下文中提供"），不再重复正文（幂等桩，可选）。

### 4.7 配置与灰度

建议在 `SkillsRuntimeConfig`（`agentconfig/config.go:880+`）新增：

```yaml
skills_runtime:
  mention_injection: auto           # off|auto|on；P0 首发 off，通过 Rollout Gates 后默认 auto
  mention_multi_limit: 4            # 单回合最多注入的文本技能数
  mention_inject_max_chars: 32768   # 单技能正文上限（超出截断+告警）
  mention_inject_total_chars: 65536 # 回合注入总量上限
  catalog_resident: false           # 常驻目录（P1 灰度后再默认开启）
```

- 语义对齐现有风格：`off/auto/on` 解析复用 `SkillsRuntimeConfig` 字符串开关惯例（参照 `document_mode`）。
- CLI 覆盖（可选）：`--skills-mention=off|auto|on`、`--skills-catalog-resident`。
- **灰度门槛（Rollout Gates，审查补充）**：`off → auto` 需同时满足：① `$mention` 解析零误报（固定语料集）；
  ② 注入不引发 preflight 压缩（§4.11 回归全绿）；③ 上下文增量 P95 ≤ 8KB（单技能典型场景）；
  ④ provider 前缀缓存命中率相对基线下降 ≤ 2%；⑤ `/skill`、headless、JSON 投影零回归。
  `catalog_resident` 默认切换另需：前缀稳定测试全绿 + 固定开销 ≤ 2K tokens。
- **默认值节奏（已拍板）**：P0 首发 `mention_injection=off`；通过上述 Gates 后默认 `auto`；
  `catalog_resident` 同节奏。预算先固定 4 / 32KB / 64KB，动态比例（上下文 2%）留 P3 评估。
- 回滚：任一开关置 off → 回到现状（函数 + `/skill` pin），无数据迁移、无持久化格式变化；
  常驻 catalog 只是回合消息，不落盘为会话配置。

### 4.8 安全与边界

1. 仅解析**已加载且启用**的技能；未知 `$xxx` 不注入、不报错。
2. 提及不触发任何执行/网络/写操作——只影响本回合上下文（与 `/skill` 相同的信任级）。
3. 系统生成输入（guardian、goal continuation、subagent 引导、team 自动唤醒）不解析 mention。
4. 预算三重兜底（技能数/单技能/总量）+ 配置值钳制（0/负数回退默认），防止上下文被技能正文打爆。
5. 不改变工具策略：mention 不扩权、不触发 allowed/disallowed 收窄（收窄仍只由 `/skill` pin 的技能声明触发）。
6. 正文注入带显式边界标记（`<skill name=… path=…>`），便于模型与审计区分"用户消息/技能正文/系统消息"。
7. **信任边界（修订 H4）**：项目级技能目录按既有 folder-trust / profile 策略加载；未信任项目不得因 mention
   注入获得额外内容通道。P0 明确"沿用现有技能加载结果（未加载则不注入）"；是否在未信任项目**禁用 mention
   注入**已拍板 Q12**：P1 与 folder-trust 联动——未信任项目默认禁用 mention 注入
   （显式 `mention_injection=on` 可覆盖）。
8. **子代理边界**：纪律块必须包含 Codex 原文的 "Do not delegate reading, summarizing, or interpreting
   skill instructions to a subagent"（本仓库多代理运行时的现实风险），并作为 P0 文案验收项。

### 4.9 生命周期与会话行为

- **时机**：回合装配前（`ensureChatExecutor` / 能力面 attach 完成后）解析与读取，失败不阻断回合启动。
- **持久化**：注入是 **prompt-only 回合级内容**（`Durable=false`，落盘时剥除，见 §4.4）；不进会话持久
  历史、不写稳定工具面。
- **resume/重启**：持久历史中本就没有注入正文（prompt-only）；catalog 常驻按配置与 fingerprint 重新生成；
  不重放已消费的 mention。
  若产品上需要"resume 后同一任务继续"，需另行提案（把技能正文改为可落盘片段），本方案不做。
- **热加载**：技能目录变更后（现有 hot reload / watcher），下一次解析使用新快照；
  catalog fingerprint 变化时，下一回合刷新常驻消息。
- **子代理**：子会话使用同一文本类规则（自身用户 turn）；系统注入的父级上下文不解析 mention。

### 4.10 观测与调试

- **mentioned vs injected 两级口径（修订 H3）**（对齐 Codex `codex.skill.injected` 的 status=ok/error）：

  | 事件/字段 | 含义 |
  |---|---|
  | `skill_mentions` | 解析命中（含未注入） |
  | `skill_injected[] {name, chars, truncated}` | 实际注入成功 |
  | `skill_inject_skipped[] {name, reason}` | ambiguous/disabled/limit/read_error/system_input |
  | `skills.invoked{invoke_type=mention}` | 仅对实际注入发出；与 pin/函数路径去重，避免双计数 |

- 日志与调试：`skill mention injected n=%d chars=%d limit=%d`；`/skills debug` 输出候选/排序/预算裁剪/失败原因
  （复用 `formatSkillExposureDebug` 风格）。
- 指标（可选）：注入技能数直方图、截断次数、注入字符数、mention 命中率（提及但未命中技能的比例）。

### 4.11 与 preflight / auto-compact / 预算的顺序契约（修订 H1，新增）

本仓库回合请求会经过 prompt preflight（`internal/agent/loop.go:5784 enforcePromptPreflightWithTools`），
且 turn system 消息在 preflight 之前就已并入请求历史（`loop.go:691`）。若注入正文不设本地预算，
可能把请求推过阈值、触发整段 active-turn 压缩甚至 preflight 失败——**注入不得成为压缩的触发器**。

契约（按序执行）：
1. 回合装配阶段先做**本地注入预算裁决**（技能数/单技能/总量三重上限 + 配置钳制）；
2. 超预算按序降级：截断 → 丢弃后续技能（记 `limit_skipped`）→ 全部放弃（并可选回退函数路径）；
3. 注入后的预估 token 必须落在 preflight 可用余量内（复用 `resolvePromptBudget` 的 active-turn 预算，
   预留 ≥20% 余量），否则继续降级；
4. preflight/auto-compact 只处理"既有历史 + 用户输入"的预算，不因注入而压缩历史；若仍触发压缩，
   视为裁决计算错误，以回归测试覆盖（§6.1）；
5. 常驻 catalog 不走回合尾部注入（见 §4.5），避免每回合改写前缀与挤压 preflight。

### 4.12 跨协议角色稳定性矩阵（Q1 重审依据，新增）

| 协议 / 适配器 | leading system | 非 leading system | user |
|---|---|---|---|
| OpenAI Chat 兼容 | system | 各家不一（部分仅接受首条） | user（稳定） |
| Anthropic（`llm/adapter/anthropic.go:118-154`） | → 顶层 `system` | → **user**（重写） | user（稳定） |
| Gemini（`llm/adapter/gemini.go:597-608`） | → **model** | → **model**（角色反转） | user（稳定） |
| Codex/Responses（`llm/adapter/codex.go:601-632`） | → 顶层 `instructions` | 留在 input，角色语义随上游 | user/developer（稳定） |
| system-role 回退（`internal/skill/executor.go:1378-1452`） | 合并进首条 user | 合并进首条 user | user（稳定） |

**规则**：① 回合级注入（技能正文、reminder、guide 类）一律 **user**；② 只有"会话 leading 前缀"允许
system/developer，且只承载真正的系统提示；③ 新增内容必须通过协议转换稳定性测试（§6.1）后放行。


---

## 5. 分阶段实施计划

### P0 — 提及解析 + 多技能正文注入（核心链路，默认 off）

**范围**：文本类 skill 的 `$name` 词法解析、解析排序/去重/歧义/禁用/上限、正文读取与注入装配、
turn metadata 与事件；配置键与默认值（`mention_injection=off` 首发，评审后再切 auto）。

| # | 文件 | 变更 |
|---|---|---|
| 1 | `backend/cmd/aicli/commands/chat_skill_mentions.go`（新增） | `collectSkillMentionNames` / `resolveMentionedTextSkills` / `buildSkillMentionFragments`；复用 `awaitChatCapabilitiesForTurn`（能力面未挂载先 await，参考 `resolveDirectCallableFunctionName` 的修复 `09fc19b2`） |
| 2 | `backend/cmd/aicli/commands/chat_actor_executor.go` + `internal/agent` | mention fragments 走 **user 角色非持久通道**（新增 `TurnUserContextMessages`，或复用 system-reminder kind=`skill_instructions`）；与 pin guide 去重、稳定排序（§4.12 约束） |
| 3 | `backend/cmd/aicli/commands/chat_core.go` | 共享 chatcore 路径经同一 user 通道 append 片段进请求历史（与 194-197 现有 guide 逻辑并列） |
| 4 | `backend/cmd/aicli/commands/skills_integration.go` | 抽出 `IsTextSkillFunction(fn)`（`Handler==nil && !HasWorkflow()`）与读取复用 `resolvedTurnSkill()`；不改变现有注册 |
| 5 | `backend/internal/agentconfig/config.go` | 新增 `mention_injection` / `mention_multi_limit` / `mention_inject_max_chars` / `mention_inject_total_chars` 字段 + 默认值解析（沿用 `document_mode` 风格） |
| 6 | `backend/cmd/aicli/commands/chat_skill_mentions_test.go`（新增） | 词法（env var 忽略、未知名忽略、大小写、连字符/下划线）、解析（顺序/去重/歧义/禁用/上限）、注入（正文、截断、总量、失败降级、去重 pin） |
| 7 | `backend/internal/agentconfig/config_test.go` / `preset_test.go` | 新键默认值与显式覆盖（off/auto/on、0/负数钳制） |
| 8 | `docs/skill_runtime/aicli_skills_usage.md` | 记录新配置与语义（默认 off）+ mention 纪律段（多提及/不跨回合/禁子代理转述/无需重读 SKILL.md） |
| 9 | `backend/cmd/aicli/commands/chat_skill_mentions.go` | 解析/注入**不得依赖函数曝光面**：直接读 `binding.skillFunctions`，保证 P3 隐藏文本类函数后仍可解析 |
| 10 | `backend/cmd/aicli/commands/chat_skill_turn.go` + `internal/agent/loop.go`（读契约） | 落地 §4.11：注入预算本地裁决先于 preflight；新增"注入不触发历史压缩"回归测试 |
| 11 | `backend/cmd/aicli/commands/chat_skill_mentions_test.go` | 补充 fuzz（token 解析）、golden（片段格式/Windows 路径归一化）、subagent/team 系统输入负例（与第 6 行共用文件） |

**验收**：
1. flag=off：全量现有测试不变，请求逐字节不新增任何 skill 片段；
2. flag=on：新会话首个消息 `$alpha 和 $beta ...` → 请求历史包含两条 `<skill>` 片段（catalog 顺序），
   且 `skill__*` 工具面**不因此变化**；`/skill alpha` 回合不重复注入 alpha；
3. 歧义/禁用/超限/读取失败四类用例都有明确降级行为与 metadata 记录；
4. `go build ./...`、`go test ./cmd/aicli/commands/ ./internal/agentconfig/ ./internal/skill/` 全绿。
5. 注入后不触发 preflight 历史压缩（回归：注入接近预算余量时按 §4.11 降级，而非压缩历史）；
6. 无 mention 回合请求逐字节不变；有 mention 回合仅新增片段部分（不重写历史前缀）。

**回滚**：配置置 off 即回现状；无持久化/协议变更。

### P1 — 常驻 catalog + 多技能纪律 + 预算收敛

**范围**：`catalog_resident` 开关；catalog 注入时机与去重（pin 回合不重复）；纪律块扩展；
预算与降级复用 `RenderSkillCatalog`；KV 前缀稳定性验证。

| # | 文件 | 变更 |
|---|---|---|
| 1 | `backend/cmd/aicli/commands/chat_skill_turn.go` | 抽出 `buildResidentSkillCatalogMessage(session)`（复用 `buildSkillCatalogText`；**固定前缀 user 片段**，kind=`skills_catalog`，Durable=false）；pin 时若 catalog 已常驻则 guide 只保留 ProgramGuide |
| 2 | `backend/cmd/aicli/commands/chat_skill_mentions.go` | catalog 注入与 mention 注入的排序/去重策略统一（catalog 在前，技能正文在后） |
| 3 | `backend/internal/skill/catalog_render.go` | 仅当纪律块文案需扩展时新增变量；预算/降级逻辑不改 |
| 4 | `backend/cmd/aicli/commands/chat_skill_turn_test.go` 等 | 新增：常驻注入一次、fingerprint 不变不重复、pin 去重、预算降级 |
| 5 | 配置 | `catalog_resident` 默认 off → 灰度 auto → 默认 on（按数据） |
| 6 | `backend/cmd/aicli/commands/chat_skill_mentions.go` | 已拍板 Q11：声明依赖/工具不可用时输出回合提示（不阻断、不安装） |
| 7 | `backend/cmd/aicli/commands/*`（folder-trust 接线） | 已拍板 Q12：未信任项目默认禁用 mention 注入（显式 on 覆盖），与 folder-trust/profile 状态联动 |

**验收**：常驻 catalog 打开后，无 mention 的普通回合上下文仅 +1 条稳定片段（fingerprint 稳定、
不随发现顺序抖动）；**连续 N 个回合的请求前缀逐字节不变**（catalog 位于稳定前缀，不随回合尾部漂移）；
pin 回合无重复 catalog；预算超限按"截描述→去描述"降级且技能条目不消失。

### P2 — TUI `$` 补全

**范围**：composer 输入 `$` 弹出技能补全，选择插入 `$name ` 并绑定 path；与 `/`、`@` 补全互不抢占；
装载中/禁用/门控行为明确。

| # | 文件 | 变更 |
|---|---|---|
| 1 | `backend/cmd/aicli/commands/chat_skill_mention_completion.go`（新增） | token 解析（`$` + 名称字符集、env 名单过滤）、候选（catalog 文本类技能）、唯一命中直接补全、多命中返回候选列表 |
| 2 | `backend/cmd/aicli/commands/chat_composer.go` | `onComplete` 增加 `$` 分支（在 `@` 之后、slash 之前/后按优先级），复用现有 `LineEditorReplacement` 契约 |
| 3 | `backend/cmd/aicli/commands/chat_skill_mention_popup.go`（新增，或复用 slash popup 控制器） | 弹层渲染（`showOwnedPopupBelowPrompt`，owner=`skill_mention_completion`）、↑/↓/Tab/Enter/Esc |
| 4 | `backend/cmd/aicli/commands/chat_skill_mention_completion_test.go`（新增） | token/候选/唯一命中/多命中/门控/ENV 排除；快照式弹层渲染断言 |
| 5 | `docs/aicli/interactive-mode.md` | 新增 `$` 用法与示例 |

**验收**：TUI 中 `$` 弹层与 slash 弹层互斥不冲突；选中后文本为 `$name `；禁用技能不出现在候选；
busy turn / 全屏选择器 lease 期间不弹层；无 TUI 场景解析路径不受影响。

### P3 — 默认切换与函数面收敛

**范围**：`mention_injection` 默认 auto/on；交互式请求面对**文本类**技能不再暴露 `skill__` 函数
（保留 `/call`、`/skill --direct`、API/exec 与 handler/workflow 技能）；观测数据复盘与文档收尾。

| # | 文件 | 变更 |
|---|---|---|
| 1 | `backend/cmd/aicli/commands/function_catalog.go` | 交互请求选择时过滤文本类 skill 函数（新增 `mention_hide_text_skill_functions` 开关，默认 on 可回退）；`SelectStableSessionFunctions` 同步 |
| 2 | `backend/cmd/aicli/commands/skills_integration.go` | 暴露分析（`AnalyzeSkillExposure`）对文本类返回空/不参与路由（避免函数面与注入面双路径） |
| 3 | `backend/cmd/aicli/commands/chat_skill_mentions.go` | 幂等桩：若文本类函数仍被调用且本回合已注入 → 函数返回"指令已在本回合上下文中"短提示（可选） |
| 4 | 文档 | `docs/skill_runtime/aicli_skills_usage.md`、`docs/plan` 状态更新；`/skills` 帮助文案补充 `$` |
| 5 | 观测复盘 | 注入量/截断率/上下文增幅/模型行为抽样（是否按纪律顺序执行、是否仍误读 SKILL.md） |

**验收**：文本类技能的交互 prompt 不再出现 `skill__*` 调用；`/call skill__x` 仍可用；
handler/workflow 技能零回归；无 mention 的旧会话行为与现状一致（除常驻 catalog 的稳定前缀）。

---

## 6. 测试计划

### 6.1 单元测试（P0/P1 为主）

| 用例 | 断言 |
|---|---|
| `$alpha $beta` 解析 | 命中 2 个；顺序按 catalog；同名去重 |
| `$HOME`/`$PATH`/`$100` | 不视为 mention |
| `$unknown` | 忽略、不报错、无注入 |
| 大小写/连字符/下划线 | 匹配正确；边界字符（中英文标点、行尾）正确 |
| 歧义（同名多技能） | 跳过 + `ambiguous` 诊断 |
| 禁用技能 | 不注入（即使被提及） |
| 上限 | N+1 个提及 → 注入前 N（catalog 顺序）+ 汇总提示 |
| 正文截断 | 超单技能上限 → 截断 + warning；总量超限 → 按序保留 |
| 读取失败 | 不阻断回合；诊断/提示可断言 |
| `/skill a` + `$a` | 只注入一次（pin 优先） |
| 系统生成输入 | 不解析 mention |
| 能力面未挂载 | 先 await 再解析；挂载失败 → 降级不注入且不报错（与 `09fc19b2` 同语义） |
| catalog 常驻 | 只注入一次；fingerprint 不变不重复；预算降级顺序正确 |
| KV 稳定性 | 同技能集合不同发现顺序 → 注入文本逐字节一致（复用 `catalog_render_test.go` 断言） |
| token 解析 fuzz（随机 `$`/标点/中文/超长输入） | 不 panic、不误匹配、耗时有界 |
| 片段格式 golden（含 Windows 路径归一化） | 逐字节稳定（`\`→`/`）；跨平台一致 |
| preflight 交互（§4.11） | 注入不触发历史压缩/preflight 失败；超预算按降级顺序执行 |
| 常驻 catalog 前缀稳定 | 连续 N 回合请求前缀逐字节不变（fingerprint 未变时）；变化仅在下一次重建 |
| 子代理/team 系统输入负例 | 不解析 mention（goal continuation/guardian/subagent 引导） |
| 函数面隐藏后的前向兼容（P3 预埋） | 文本类 `skill__` 被过滤后，`$name` 解析/注入链仍生效 |
| 协议转换稳定性（§4.12） | Anthropic 非 leading system→user、Gemini system→model 等路径下，user 片段内容/顺序保留；回合级注入不得使用 system |

### 6.2 集成测试（请求级）

1. 新会话首条消息 `$a $b` → 抓取请求：包含 2 条技能片段、无 `skill__` 调用、无 pin guide 重复；
2. `/skill a ...` 且文本含 `$b` → guide 1 条 + b 片段 1 条，a 不重复；
3. flag=off → 请求与现状逐字节一致（回归基线）；
4. headless（`aicli exec`）/runapi invoke → 同样注入，命令投影不变；
5. auto-compact 触发后 → 注入内容按历史压缩规则处置，不破坏后续回合。

### 6.3 手工/E2E

- TUI：`$` 弹层、Tab 补全、`$a $b` 一回合两技能（观察模型是否按纪律声明顺序）、禁用技能不可见；
- 回归：`/skills` 选择器、`/skill --direct`、`/call`、图片/生成类既有回合不变；
- 观测：turn metadata 与 `skills.invoked{invoke_type=mention}` 可在 debug 端点/日志核对。


---

## 7. 风险与缓解

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| R1 | 技能正文大 / 多技能 → 上下文膨胀 | 成本、压缩频繁、模型注意力稀释 | 三重预算（技能数 4 / 单技能 32KB / 总量 64KB）+ 截断告警；catalog 独立预算 8000 字符；灰度观测 |
| R2 | 函数路径与注入路径双份正文 | 重复 token、模型困惑 | 同回合去重；pin 优先；P3 收敛文本类函数面；可选幂等桩 |
| R3 | `$` 误判（shell 变量、价格、占位符） | 无（若只匹配已知技能名则天然安全） | 已知技能候选集 + env 名单 + 未知忽略 + 大小写不敏感精确匹配 |
| R4 | 常驻 catalog 击穿 KV 前缀缓存 | 缓存命中率下降 | 稳定排序 + fingerprint（复用 render 测试）；发现顺序变化不触发重注入；变更只在快照变化后 |
| R5 | 模型仍用文件工具重读 SKILL.md | 双份正文 | 纪律块明确"正文已注入，无需重读"；P3 观测复盘，必要时在正文头部加一行提示 |
| R6 | 与 `/skill` pin 的 allowed/disallowed 语义混淆 | 工具面收窄误用 | 明确：mention 不触发收窄；混合回合收窄只由 pin 技能声明决定（测试固化） |
| R7 | 系统生成输入被误注入 | 安全/噪声 | 只对用户发起回合注入；guardian/continuation/subagent 跳过（对齐 Codex） |
| R8 | headless/远程行为意外变化 | 自动化脚本受影响 | P0 默认 off；灰度 auto；请求级集成测试与现状基线对比 |
| R9 | 热加载窗口读到半更新技能 | 读取失败/内容不一致 | 沿用 bootstrap 快照语义；单技能失败跳过 + 诊断 |
| R10 | 歧义技能被静默忽略，用户以为已加载 | 体验 | 汇总提示（"2 个技能未注入：歧义/禁用"）+ `/skills debug` 可见 |
| R11 | 未信任项目的技能内容经 mention 注入 | 内容信任边界 | 沿用既有加载/信任策略（未加载则不注入）；评审 Q12 决定是否在未信任项目禁用 |
| R12 | 注入把请求推过 preflight 预算 | 触发压缩/preflight 失败 | §4.11 顺序契约 + 回归测试；超预算只降级注入，不压缩历史 |
| R13 | 回合装配同步读取 N 个技能文件 | 主循环卡顿 | 复用 `resolvedTurnSkill()` 解析缓存；N≤4；warm 装配目标 ≤20ms，超限按序降级 |
| R14 | 用 system 角色承载回合级注入 | Anthropic→user、Gemini→model 角色反转，指令语义漂移 | §4.12 规则：回合级一律 user；协议矩阵测试纳入 P0 验收 |

---

## 8. 决策记录（已拍板，2026-10-02）

| # | 问题 | 已拍板决策 |
|---|---|---|
| Q1 | 注入消息角色：system vs user（Codex 原文） | **user 角色（2026-10-02 重审修订）：回合级片段走 user 非持久通道；catalog 走固定前缀 user 片段；只有 leading 前缀用 system/developer。依据 §4.12 协议矩阵** |
| Q2 | 非文本类技能被 `$` 提及：忽略（本方案）还是自动转 pin | 忽略 + 提示；P3 再评估自动 pin |
| Q3 | 是否支持链接式提及 `[$name](skill://path)` | P2 可选；P0 只支持 `$name` |
| Q4 | 常驻 catalog 注入时机：会话首回合一次 / 每回合 fingerprint 变化才注入 / resume 重放 | 首回合 + fingerprint 变化时刷新；resume 重放一次 |
| Q5 | P3 是否隐藏文本类 `skill__` 函数面 | 默认隐藏（配置可回退），保留 `/call`、API、exec |
| Q6 | 上限默认值与动态化（4 / 32KB / 64KB vs 上下文 2%） | 先用固定默认，灰度后按数据调；动态比例留 P3 |
| Q7 | mention 路径下 `$ARGUMENTS` 留空 | 留空 + 正文头部一行"未提供显式参数"提示（可选） |
| Q8 | 技能间编排（a 的输出喂给 b） | 不做，由模型按纪律协调顺序；后续如有需求另立方案 |
| Q9 | 大小写策略：大小写不敏感 vs Codex 精确匹配 | **不敏感（贴合现状）+ 单测固化 + 文档说明** |
| Q10 | 常驻 catalog 注入位置：稳定系统前缀 vs 每回合尾部 | **稳定前缀（缓存与 preflight 双重要求）** |
| Q11 | 技能声明的 MCP/工具依赖是否检查/安装 | **P0 不检查、不安装；P1 输出"依赖不可用"回合提示（不阻断）；自动安装另立方案** |
| Q12 | 未信任项目是否禁用 mention 注入 | **P1 与 folder-trust 联动：未信任默认禁用（显式 `on` 覆盖）；P0 沿用现有加载结果** |

> **拍板说明**：除 Q11/Q12 相对建议小幅加严外，其余全部按"建议默认"采纳；实现顺序、验收与
> 灰度门槛以 §5 / §4.7 为准。即日进入 P0（默认 off、零回归起步）。

---

## 附录 A：Codex 参考代码索引（`E:\projects\ai\codex`）

| 机制 | 位置 |
|---|---|
| `$` sigil / `[$name](skill://path)` 语法 | `codex-rs/skills/src/mentions.rs:41,79-86` |
| 多提及选择（结构化优先、目录顺序、歧义/禁用规则） | `codex-rs/skills/src/selection.rs:42-109,118-196` |
| TUI `$` 打开列表 / 技能菜单 | `codex-rs/tui/src/chatwidget/skills.rs:25-58` |
| 补全插入 `$name` + path 绑定 | `codex-rs/tui/src/bottom_pane/chat_composer.rs:4222-4229` |
| 提交转 `UserInput::Skill` | `codex-rs/tui/src/chatwidget/input_submission.rs:304-331` |
| 逐技能读取正文 + `<skill>` 片段 | `codex-rs/ext/skills/src/host_prompt.rs:69-108`；`fragments.rs:76-110` |
| 回合注入（fragments → input items） | `codex-rs/core/src/session/turn.rs:1063-1097` |
| 常驻 catalog + 纪律文案 | `codex-rs/ext/skills/src/catalog_prompt.rs:8,17,81-106` |
| 观测量 | `codex-rs/core/src/skills.rs:38-119` |

## 附录 B：本仓库改造索引（计划）

| 模块 | 文件 | 阶段 |
|---|---|---|
| 提及解析/解析/注入 | `backend/cmd/aicli/commands/chat_skill_mentions.go`（新增） | P0 |
| actor 注入接线 | `backend/cmd/aicli/commands/chat_actor_executor.go:271-278` | P0 |
| chatcore 注入接线 | `backend/cmd/aicli/commands/chat_core.go:194-197` | P0 |
| 文本类判定复用 | `backend/cmd/aicli/commands/skills_integration.go:667-675`、`resolvedTurnSkill()` | P0 |
| 配置键 | `backend/internal/agentconfig/config.go:880+`（SkillsRuntimeConfig） | P0 |
| 常驻 catalog | `backend/cmd/aicli/commands/chat_skill_turn.go:420-460` | P1 |
| `$` 补全 | `chat_skill_mention_completion.go`（新增）+ `chat_composer.go:214-219` | P2 |
| 弹层 | `chat_slash_completion_controller.go:331-373` 模式复用 | P2 |
| 函数面收敛 | `function_catalog.go`（Select*）、`skills_integration.go`（AnalyzeSkillExposure） | P3 |
| 文档 | `docs/skill_runtime/aicli_skills_usage.md`、`docs/aicli/interactive-mode.md` | P0/P2 |

## 附录 C：配置示例

```yaml
skills_runtime:
  enabled: true
  document_mode: auto
  catalog_budget_chars: 8000
  discipline_block: true
  argument_substitution: "on"
  # 本方案新增
  mention_injection: auto            # off|auto|on
  mention_multi_limit: 4
  mention_inject_max_chars: 32768
  mention_inject_total_chars: 65536
  catalog_resident: false            # P1 灰度后再默认开启
```

## 附录 D：与既有方案的关系

- **不改** `/skill` 回合 pin（`tui-skill-turn-invocation-plan.md`）——它是 handler/workflow 技能与显式调用的主干；
  本方案为文本类技能增加默认更轻的提及路径，两者共用去重与预算。
- **不改** 技能即函数（`skill-model-driven-invocation-plan.md`）——`/call`、API、exec、调试路径继续可用；
  P3 仅对交互式请求面收敛文本类函数。
- 与 `codex-skill-loading-and-execution-analysis-20260918.md` 的差异：本方案只借"文本类技能的使用方式"
  （catalog + mention + 注入），不引入执行环境/云技能/`skills.read` 资源体系。
