# Codex（E:\projects\ai\codex）Skill 加载与执行机制分析

> 对象：`E:\projects\ai\codex`（Rust workspace `codex-rs`），2026-09-18 工作区快照。
> 结论均附 `文件:行号`；未逐行核实的推测已明确标注。

---

## 0. 一句话结论

Codex 的 skill **没有"执行器"**：skill 是一份按 `SKILL.md` 组织的**指令文档**。
"加载"= 多源目录发现 + front matter 校验 + 不可变快照/缓存 + 文件 watcher；
"执行"= **按需把 SKILL.md 正文注入当轮会话**（user 角色 `<skill>` 片段），模型依据 developer 角色里的 catalog 与 `How to use skills` 规则，用**既有工具**（shell/apply_patch/MCP…）自行照做。
CLI 里没有"调用某个 skill 接口"这条路径，也没有确定性 workflow 执行——这与本仓库（ai-agent-runtime）的 `skill.Executor`（Handler/Workflow/model 工具循环）是根本性差异。

---

## 1. 加载机制

### 1.1 数据模型与文件约定

| 项 | 事实 | 位置 |
|---|---|---|
| 入口文件 | 每个技能一个目录，入口 `SKILL.md`（常量 `SKILLS_FILENAME="SKILL.md"`） | `core-skills/src/loader.rs:137` |
| 目录约定 | `.agents/skills/<name>/SKILL.md`、`skills/...`；`skills` 目录名 + `.agents` 包装目录 | `loader.rs:138-141`；测试构造 `core/tests/suite/skills.rs:26-47` |
| front matter | `name`（必填）、`description`、`metadata.short-description` | `loader.rs:62-76` |
| 元数据文件 | `agents/openai.yaml`：`interface`（display_name/short_description/icon/brand_color/default_prompt）、`dependencies.tools[]`、`policy` | `loader.rs:78-128` |
| policy | `allow_implicit_invocation`（默认 true）、`products`（产品限制） | `loader.rs:111-117`；`model.rs:29-49` |
| 长度上限 | name 64 / qualified 128 / description、short-description、default_prompt 各 1024；依赖字段亦有上限 | `loader.rs:142-150` |
| 作用域 | `SkillScope`: User / Repo / System / Admin；另带 `plugin_id` 溯源 | `model.rs:14-26`；`core/src/skills.rs:70-75` |
| 重名 | `namespace::SkillNamespaceResolver` 做命名空间/限定名解析 | `core-skills/src/lib.rs:9`、`loader.rs:45` |

### 1.2 发现与来源

- **服务中心**：`SkillsService` 持有 `codex_home`、运行时 `extra_roots`（RwLock）、双缓存（按 cwd / 按 config-key）、全局并发信号量 `MAX_CONCURRENT_ROOT_SCANS = 8`（跨 cwd 共享，防 I/O fanout 放大）。见 `core-skills/src/service.rs:59-106`。
- **来源合流**：
  - 配置层栈（`ConfigLayerStack`）+ 有效技能根（`PluginSkillRoot`，可带插件加载期解析的快照 `PluginSkillSnapshots`）→ `SkillsLoadInput`（`service.rs:32-65`）；
  - 运行时额外根 `extra_roots`（`set_extra_roots` 后清缓存，`service.rs:108-117`）；
  - **bundled/system 技能**：`install_system_skills` 落到 `skills/.system`；`bundled.enabled=false` 时 `uninstall_system_skills` 清理（`service.rs:98-104`、`core-skills/src/system.rs:1-8`）。
- **扫描实现**：`loader` 的 `discovery` 模块（hidden 目录策略、symlink 策略、并发发现、元数据先行的 `SkillMetadataDiscovery`）、`root_loader`（插件根快照）、`namespace` 解析器（`loader.rs:1-45,60`）。
- **环境/远端**：`loader/environment.rs` 支持 environment-owned skill（`EnvironmentSkillSnapshot{Outcome}`）；`remote.rs` 支持远端技能读取；`snapshot_for_config(input, fs: Option<Arc<dyn ExecutorFileSystem>>)` 允许按执行环境文件系统加载（`service.rs:131-136`）。
- **产品过滤**：`filter_skill_load_outcome_for_product` + `SkillMetadata::matches_product_restriction_for_product`。

### 1.3 快照、缓存与失效

- 产出不可变的 `HostSkillsSnapshot` / `SkillLoadOutcome{skills, errors, disabled_paths, skill_roots, file_systems_by_skill_path, implicit_skills_by_scripts_dir, implicit_skills_by_doc_path}`（`model.rs:91-101`）。
- 缓存键按**有效配置**（roots + skill config rules）而非仅 cwd，避免 role/session 局部覆盖串味（`service.rs:119-140`）。
- 每轮使用 turn 级快照：`turn_context.turn_skills.snapshot`（`core/src/skills.rs:56-57`、`core/src/session/turn.rs:591`）。
- **热更新**：`app-server/src/skills_watcher.rs` 用 `FileWatcher` 订阅技能目录，节流 **10s**（测试 50ms，`skills_watcher.rs:24-27`），变更后推送 `SkillsChangedNotification`；支持 `register_runtime_extra_roots` 与按 thread 配置/环境注册监听（`skills_watcher.rs:29-90`）。
- **对外 API（app-server v2）**：`skills/list`、`skills/config/write`、`skills/extraRoots/set`、`pluginSkill/read`、`SkillsChangedNotification`（协议 schema：`app-server-protocol/schema/{json,typescript}/v2/*Skill*`）。

### 1.4 配置与启停

- `SkillsConfig{bundled.enabled, include_instructions, config[]}`；`SkillConfig{path|name, enabled}` 以路径或名字选择器控制启停（`config/src/skills_config.rs:12-49`）。
- `config_rules::resolve_disabled_skill_paths` 产出 `disabled_paths`；`SkillLoadOutcome::is_skill_enabled` 供后续选择/注入过滤（`model.rs:103-118`）。
- `include_instructions` 决定是否注入 `### How to use skills` 说明块（`available_skills_instructions.rs:26-44`）。

### 1.5 模型可见的 catalog（render + 预算）

- **预算**：默认 **8000 字符**，或上下文窗口的 **2%**（取其一实现：`DEFAULT_SKILL_METADATA_CHAR_BUDGET=8_000`、`SKILL_METADATA_CONTEXT_WINDOW_PERCENT=2`，`render.rs:18-19`）。
- 预算不足时**降级顺序**：先按描述截断（带 `"..."` 后缀与 100 字符阈值告警），仍超则**移除全部描述**只留名字，并给出告警文案（`SKILL_DESCRIPTION_TRUNCATED_WARNING*`、`SKILL_DESCRIPTIONS_REMOVED_WARNING_PREFIX`，`render.rs:20-27`）。
- 两种呈现形态：**绝对路径**（无 roots 表）或 **短路径 + `### Skill roots` 别名表**（`SKILLS_INTRO_WITH_ALIASES`，`render.rs:28-29,47-79`）。
- 结构：`## Skills` → intro →（可选）`### Skill roots` → `### Available skills`（每行 name + description + 定位符）→（可选）`### How to use skills`（`render.rs:65-79`）。
- 渲染报告 `SkillRenderReport{total/included/omitted/truncated_*}` + OTEL 指标 `THREAD_SKILLS_ENABLED_TOTAL/KEPT_TOTAL/TRUNCATED/DESCRIPTION_TRUNCATED_CHARS`（`render.rs:10-24,113-128`）。
- **注入形态**：`AvailableSkillsInstructions` 是 **developer 角色**的 contextual fragment（带 `SKILLS_INSTRUCTIONS_OPEN_TAG/CLOSE_TAG` 标记），在会话装配时生成（`core/src/context/available_skills_instructions.rs:47-62`；调用点 `core/src/session/mod.rs:3249-3257`，是否带用法说明由 `model_info.include_skills_usage_instructions` 决定）。

---

## 2. 执行机制

### 2.1 触发与选择（显式）

`collect_explicit_skill_mentions` 汇总三类显式信号（`ext/skills/src/selection.rs:21-80`、core 版 `core-skills/src/injection.rs:146+`）：

1. **结构化选择**：`UserInput::Skill{name, path}`（编辑器/客户端选择技能）；
2. **mention**：`UserInput::Mention{path}` 且路径以 `skill://` 开头；
3. **文本提及**：消息里的 `$skill-name`（`TOOL_MENTION_SIGIL` 解析，经 `extract_tool_mentions`）。

规则：结构化命中会把同名"纯文本提及"**拉黑**（`blocked_plain_names`），避免一次调用被算两次；路径匹配支持 `main_prompt` / 资源 id / display_path 三种形态；结果去重保序（`seen`）。

### 2.2 注入正文 —— "执行"的本体

- `build_skill_injections(mentioned_skills, loaded_skills, otel, analytics, tracking)` 对每个被提及的技能：
  - 选择**该技能所属的文件系统**（`SkillLoadOutcome::file_system_for_skill`，environment/远端技能用其自己的 FS；否则 `LOCAL_FS`）；
  - 读取 `SKILL.md` **全文**（`fs.read_file_text`）；
  - 成功 → `SkillInjection{name, path, contents}` 并记 `InvocationType::Explicit`；
  - 失败 → 警告 `Failed to load skill {name} at {path}: ...`（`core-skills/src/injection.rs:71-124`）。
- 渲染为 **user 角色**片段：`<skill><name>…</name><path>…</path>正文</skill>`（`core-skills/src/skill_instructions.rs:22-40`）。
- **回合装配序列**（`core/src/session/turn.rs:591-687`）：
  1. 取 turn 快照 `turn_context.turn_skills.snapshot.outcome()`；
  2. `collect_explicit_skill_mentions`（含 connector slug 计数防歧义）；
  3. `maybe_prompt_and_install_mcp_dependencies`：技能在 `agents/openai.yaml` 声明的 MCP 工具依赖，若缺失会弹确认（`Install` / `Continue anyway`，仅 first-party originator），见 `core/src/mcp_skill_dependencies.rs:30-44`；
  4. `build_skill_injections` → 警告事件逐条上报；
  5. `skill_items` → 从技能正文里**解析 connector/app id**（`collect_explicit_app_ids_from_skill_items`）→ 自动启用对应 connector；
  6. 与扩展注入去重：若扩展已通过 WorldState 提供 host 技能目录（`HostSkillsCatalogInWorldState`），core 用 `InjectedHostSkillPrompts.contains_path` 过滤，避免同一 `SKILL.md` 注入两份（`turn.rs:613-615,675-684`）；
  7. 合并 plugin / extension 注入项，随本轮输入下发。

### 2.3 隐式调用（"读/跑即调用"）

- 索引构建：`build_implicit_skill_path_indexes` → `implicit_skills_by_scripts_dir` 与 `implicit_skills_by_doc_path`（`model.rs:99-100`）。
- 判定：`detect_implicit_skill_invocation_for_command(outcome, command, workdir)` —— 当 agent 执行的命令落在技能的 `scripts/` 目录，或访问了技能的 `SKILL.md` 路径时，视为**隐式调用**；`allow_implicit_invocation=false` 的技能被排除（`model.rs:108-118`、`core/src/skills.rs:50-62`）。
- 效果（`core/src/skills.rs:63-124`）：
  - turn 级去重（`implicit_invocation_seen_skills`，键 `scope:path:name`）；
  - 通知所有扩展 `skill_invocation_contributors().on_skill_invocation(...)`；
  - OTEL `codex.skill.injected{invoke_type=implicit}`；
  - analytics `SkillInvocation{InvocationType::Implicit}`。
- **扩展钩子契约**：`SkillInvocationInput{session_store, thread_store, turn_store, turn_id, skill_resource, kind}`；`SkillInvocationKind::Explicit | Implicit`（`ext/extension-api/src/contributors/skill_invocation.rs:1-26`）。

### 2.4 工具面（orchestrator / 远端技能）

- `ext/skills` 注册命名空间工具 **`skills.list` / `skills.read`**（`SKILLS_NAMESPACE="skills"`，`ext/skills/src/tools/mod.rs:30-55`）。
- authority 模型：`skills.list` 以 `{"authority":{"kind":"orchestrator"}}` 查询，返回 package 与 `main_resource`；`skills.read` 按**同一 authority+package** 读取资源；`skill://` 标识**不是**文件系统路径（`tools/mod.rs:66-110`）。
- `SkillProvider` trait：`list/read/search`；三种实现：**Host**（本地/快照）、**Executor**（执行环境能力发现）、**Orchestrator**（MCP resources，`CODEX_APPS_MCP_SERVER_NAME`）；要求"某 provider 列出的资源必须由同一 provider/authority 读取"，禁止退化为本地路径（`ext/skills/src/provider.rs:55-69`）。
- 模型指引（`core-skills/src/render.rs:30-63`）明确规定：
  - trigger 规则（`$Name` 或描述匹配即必须使用；多提及=全用；**不跨轮携带**）；
  - progressive disclosure：主 agent 必须**自己完整读** `SKILL.md`（不许委派 subagent 读/总结技能说明）；引用文件按同一机制解析；只按需读 references/；有 scripts 优先运行/打补丁而非重打代码；
  - 协调、context hygiene 与 fallback 规范。

### 2.5 权限与审批

- 技能**本身不是可执行实体**，所以没有"技能级审批"；相关控制点是：
  - 依赖安装确认弹窗（`skill_mcp_dependency_install`：Install / Continue anyway，`mcp_skill_dependencies.rs:30-33`）；
  - 技能挂载的 MCP 工具走既有审批策略（测试 `core/tests/suite/skill_approval.rs`）；
  - `SkillPolicy.products` 目前**只解析存储**，源码注释明确"尚未在选择/注入中强制"（`model.rs:55-57`）。

### 2.6 端到端测试契约（行为证据）

`core/tests/suite/skills.rs:49+`：
- 在仓库写 `.agents/skills/demo/SKILL.md`（front matter name/description + 正文）；
- 用户输入 `"please use $demo"` + 结构化 `UserInput::Skill{name:"demo", path:…}`；
- 断言：mock SSE 收到的请求里包含技能注入（`user_turn_includes_skill_instructions`）。
→ 印证"执行=注入 + 模型照做"，而非运行时调用某个执行接口。

---

## 3. 与本仓库（ai-agent-runtime）的对照

| 维度 | Codex | ai-agent-runtime（现状） |
|---|---|---|
| 技能本体 | `SKILL.md` 指令文档（无执行器） | `skill.yaml`/`SKILL.md` + `Executor`（Handler → Workflow → executeDefault/model 工具循环） |
| 模型可见面 | developer catalog 片段 + user `<skill>` 正文注入（+`skills.list/read` 工具） | 路由/函数 schema（aicli）、ProgramGuide（web `/skill`、model 模式） |
| 触发 | `$name` / 结构化选择（显式）；读/跑即隐式（观测） | 路由 top-k、显式 mention、`/skill`（TUI 直执或回合化后模型驱动） |
| 执行 | 模型按说明调用既有工具 | 运行时执行器 + 工具循环（确定性 auto / 模型 model） |
| 预算治理 | 上下文 2% 或 8000 字符，降级截断 + 告警 | ProgramGuide/目录无预算上限（仅在提示词层面） |
| 热更新 | FileWatcher + 10s 节流 + `SkillsChanged` 通知 + extra roots API | 后端 fsnotify HotReload；前端目录仅挂载时拉一次（存在滞后） |
| 依赖 | `agents/openai.yaml` 声明 MCP 工具依赖，缺失时引导安装 | `skill.Tools`/`ProgramTools` 映射函数目录，默认只 pin 命中项 |
| 隐式调用观测 | 有（path 索引 + 去重 + 扩展钩子 + 遥测） | 无对应概念（路由命中即调用） |

---

## 4. 值得借鉴与风险提示

**可借鉴**
1. **catalog 与正文分离**：常驻上下文只放 name+description+定位符，正文按需注入——token 成本可控。
2. **progressive disclosure 规则写进提示词**并明确"主 agent 自己读，不得委派 subagent"——避免技能语义在转发中失真。
3. **预算与降级策略**：2% 上下文 + 截断阈值 + 用户可见告警，工程上比"超预算静默失败"稳健。
4. **隐式调用闭环**：path 索引 + turn 级去重 + 扩展钩子 + 遥测，能观测"模型实际照做了没有"。
5. **authority 边界**：远端/orchestrator 资源必须经同一 provider 读取，杜绝"资源冒充本地路径"。

**风险/边界**
1. `policy.products` 只解析未强制（源码 TODO）。
2. 隐式调用依赖路径匹配，存在误报/漏报可能（尤其脚本被复制或包一层 wrapper 时）。
3. 目录规模大时扫描/监听成本（并发 8 + 双层缓存 + 10s 节流缓解，但仍是全局信号量）。
4. 技能正文全文注入依赖模型自觉"完整读完"，大 `SKILL.md` 会挤占上下文（与 2% 预算只约束 catalog、不约束正文）。

---

## 5. 关键文件索引

| 主题 | 文件 |
|---|---|
| 加载实现（loader/发现/解析/预算） | `codex-rs/core-skills/src/{loader.rs,render.rs,model.rs}` |
| 快照/缓存/extra roots/system 技能 | `codex-rs/core-skills/src/{service.rs,system.rs}` |
| 注入与显式提及 | `codex-rs/core-skills/src/{injection.rs,skill_instructions.rs}` |
| core 侧适配与隐式调用 | `codex-rs/core/src/skills.rs`、`core/src/mcp_skill_dependencies.rs` |
| 回合装配 | `codex-rs/core/src/session/turn.rs:591-687`、`core/src/session/mod.rs:3249-3257` |
| developer catalog 片段 | `codex-rs/core/src/context/available_skills_instructions.rs` |
| 扩展（catalog/provider/工具/选择） | `codex-rs/ext/skills/src/{catalog.rs,provider.rs,selection.rs,tools/*,world_state.rs}` |
| 扩展 invocation 钩子 | `codex-rs/ext/extension-api/src/contributors/skill_invocation.rs` |
| 热更新与 API | `codex-rs/app-server/src/skills_watcher.rs`、`codex-rs/app-server-protocol/schema/{json,typescript}/v2/*Skill*` |
| 配置 | `codex-rs/config/src/skills_config.rs`、`codex-rs/core-skills/src/config_rules.rs` |
| 行为测试 | `codex-rs/core/tests/suite/{skills.rs,skill_approval.rs}`、`codex-rs/app-server/tests/suite/v2/{skills_list.rs,executor_skills.rs,host_skills.rs}` |
