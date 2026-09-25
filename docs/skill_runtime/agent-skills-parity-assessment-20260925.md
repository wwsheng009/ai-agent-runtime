# Agent Skills 标准对照评估与实施计划（2026-09-25）

> 对照对象：Command Code 文档《Agent Skills》(https://commandcode.ai/docs/skills) 所述 Agent Skills 开放标准。
> 评估范围：`ai-agent-runtime` 仓库现有 skill 机制（发现优先级 / `SKILL.md` 解析 / 占位符替换 / shell 注入 / CLI / Web / 生命周期）。
> 结论已由独立子代理逐条反证核查（见附录 B 检索清单）。

**路径口径**：本文撰写时的现状路径为 `backend/internal/api/skills`（包名 `skills`）。该包名与职责不符（它承载整个 runtime HTTP API，而不只是 skills），本次实施将其重命名为 `backend/internal/api/runtimeapi`（包名 `runtimeapi`），方案见 §6。文中的 `internal/api/skills` 均指重命名前的口径。

**实施进展（2026-09-25）**：§6 包名调整已完成（`git mv` + 包声明 + 全仓引用），`go build ./...` 与相关包测试编译通过；§2/附录 A 中出现的 `internal/api/skills` 为历史口径，当前实际路径为 `internal/api/runtimeapi`。

---

## 1. 结论摘要

1. 本仓库不是"没有 skills 机制"，而是**已经有一套比文档更重的 skill runtime**：Codex 风格 `SKILL.md` 已是一等公民，另有 legacy `skill.yaml`（handler/workflow/triggers/MCP tools）形态共存。
2. 对照 Agent Skills 标准，真正的缺口只有 5 类：
   - **标准可选 frontmatter 字段**完全不解析（`license`/`compatibility`/`allowed-tools`/`argument-hint`/`when_to_use`/`disable-model-invocation`/`user-invocable`/`arguments`/`model`/`effort` 等，静默忽略）；
   - **参数与占位符替换**不存在（`$ARGUMENTS`/`$ARGUMENTS[N]`/命名参数/`${…_SKILL_DIR}` 全仓 0 命中）；
   - **发现根与开关细节**不齐（无显式 `~/.agents/skills` 用户根、无 `--skill`/`--no-skills`、无同名遮蔽诊断）；
   - **CLI 安装面**弱（只有本地 `aicli skill install`，无 `add <owner/repo>`/`list`/`remove`）；
   - **per-skill 启停**未持久化（只有全局 `skills_runtime.enabled` 门 + profile 名单过滤）。
3. **正文动态 shell 注入（`` !`cmd` ``）完全未实现**；结合本仓库已有的审批/权限/信任体系，建议**不实现**（详见 §5.6）。
4. 渐进式披露（progressive disclosure）语义**已具备**：常驻 catalog 只含 name+description+source locator，纪律块要求模型自己读全文（`internal/skill/catalog_render.go:316-325`）。
5. 实施优先级（按收益/风险）：**P2 占位符与参数 > P1 标准字段 > P3 发现对齐与遮蔽诊断 > P5 per-skill 启停 > P4 CLI add/list/remove > P6（不做）**。

---

## 2. 现状盘点

### 2.1 发现（roots / 优先级 / 扫描）

| 事项 | 事实 | 位置 |
|---|---|---|
| 根目录 | `<anchor>/skills`、anchor 各级祖先 `.agents/skills`、`~/.aicli/skills`、`~/.aicli/agents/skills`、`<config_dir>/skills` | `internal/skill/roots.go:92,86-88,97-98,101-103` |
| 配置/CLI 附加根 | `skills_runtime.skill_dir / skill_dirs / extra_skill_dirs`；`aicli chat|exec --skills-dir`（可重复） | `internal/agentconfig/config.go:844-846`、`cmd/aicli/commands/chat_command.go:121`、`exec_common_flags.go:59` |
| 实际装配顺序 | config → CLI `--skills-dir` → Codex 兼容发现 → cwd 祖先 `.agents/skills` → plugin 根 | `cmd/aicli/commands/skills_integration.go:1173-1213` |
| 扫描规则 | BFS、深度上限 6、每根 2000 目录、跳过隐藏项；`scripts/ references/ assets/ agents/` 不当作 skill | `internal/skill/scan.go:11-12`、`manifest.go:502-515` |
| 冲突优先级 | 目录顺序先者优先（`sourceRankForPath` 返回目录索引） | `internal/skill/hot_reload.go:623-646` |
| 注册语义 | `skills` + `skillsByPath` 双索引；同 path 覆盖；legacy×legacy 同名后者丢弃；Codex 同名可共存、name 索引先到先得 | `internal/skill/registry.go:99-129` |
| 缺失能力 | 无 `~/.agents/skills` 显式用户根（仅在 cwd 位于 home 下时被祖先遍历间接覆盖）；无 `--skill`/`--no-skills`；无重复名/shadow 报告 | 反证核查见附录 B |

### 2.2 SKILL.md 解析

| 事项 | 事实 | 位置 |
|---|---|---|
| 入口判定 | 文件名 `SKILL.md`（大小写不敏感）走 Codex 解析；否则按 legacy `skill.yaml` | `internal/skill/codex_model.go:228-230`、`manifest.go:480-499` |
| frontmatter 切分 | 首行必须是 `---`，到下一个 `---` 为止；CRLF 归一化 | `internal/skill/codex_manifest.go:145-181` |
| 已解析字段 | `name`、`description`、`metadata.short-description`（name 缺省回退目录名；name≤64、description≤1024） | `internal/skill/codex_manifest.go:13-19,93-141` |
| 伴生元数据 | 同目录 `agents/openai.yaml`：interface / dependencies / policy（含 `allow_implicit_invocation`） | `internal/skill/codex_manifest.go:21-52,183-269` |
| 未解析字段 | `license`、`compatibility`、`metadata.*`（除 short-description）、`allowed-tools`、`disallowed-tools`、`argument-hint`、`when_to_use`、`disable-model-invocation`、`user-invocable`、`arguments`、`model`、`effort`；YAML 非严格模式，未知键**静默忽略** | 反证核查见附录 B |
| 校验强度 | 仅长度校验；不校验 name 字符集，也不要求目录名与 name 一致 | `codex_manifest.go:99-124` |

### 2.3 目录 → 模型上下文（渐进披露）

| 事项 | 事实 | 位置 |
|---|---|---|
| catalog 条目 | `name + description + scope + source locator`（别名模式下为 short path + roots 表） | `internal/skill/catalog_render.go:26-35,84-124` |
| 渲染 | intro → skill roots → available skills → 纪律块；字符预算默认 8000（可配 `catalog_budget_chars`），超预算逐级降级 | `catalog_render.go:126-133,220-288` |
| 纪律块 | 要求：命中后先完整读 `SKILL.md`；references/scripts/assets 相对技能目录解析；不要让子代理代读 skill 指令 | `catalog_render.go:316-325` |
| 注入点（API） | `buildSkillExposureMessages` → 系统消息；`buildSkillCatalogMessages` 常驻目录 | `internal/api/skills/handler.go:8229-8300` |
| 注入点（TUI `/skill`） | catalog 文本 + ProgramGuide（或文档模式正文）拼进本回合 pin guide | `cmd/aicli/commands/chat_skill_turn.go:135-206` |
| 文档模式 | `execution_mode: document` 或 Codex 技能在 `skills_runtime.document_mode=auto` 时把正文注入上下文；默认 off | `internal/skill/skill.go:139-167`、`agentconfig/config.go:876-881` |

### 2.4 调用与执行

| 链路 | 机制 | 位置 |
|---|---|---|
| aicli chat | skill 暴露为 `skill__<name>` function；每轮 = 显式提及 + Router 候选 + 历史回补 | `cmd/aicli/commands/skills_integration.go` |
| TUI `/skill` | 解析 skill 函数 → pin 工具面 + guide → 作为普通用户消息提交（回合制） | `chat_skill_turn.go:93-116,135-206` |
| TUI `/skills` | 目录选择器（编号/名称）→ 输入 prompt → 转 `/skill` | `chat_skills_command.go:22-115` |
| API | `POST /api/agent/chat` + `expose_skills`（强制 ReAct / 未知 skill 400 fail loudly） | `docs/skill_runtime/skill_loading_and_interaction_20260918.md` §4.3 |
| 执行 | legacy：Handler → Workflow → executeDefault（LLM 子调用）；model 模式：ProgramGuide + 工具循环 | `internal/skill/executor.go` |
| 参数模板（仅 legacy workflow args） | `{{prompt}}`/`{{context.x}}`/`{{options.x}}`/`{{metadata.x}}`/`{{results.step}}`，递归渲染 string/map/slice | `internal/skill/executor.go:30,220,1493-1618` |

### 2.5 生命周期 / 诊断 / 治理

| 能力 | 事实 | 位置 |
|---|---|---|
| 热重载 | fsnotify 监听多根 + debounce；事件 `skill_added/updated/removed/error`；`/skills/hot-reload/{start,stop,reload,stats}` | `internal/skill/hot_reload.go`、`internal/api/skills/handler.go:853-856` |
| 注册表 API | `GET/POST /skills`、`GET/PUT/DELETE /skills/{name}`、`/skills/search`、`/skills/stats`、`/skills/validate`、`/skills/import|export`、`/skills/batch` | `internal/api/skills/handler.go:839-857,1089-1092` |
| 治理开关 | `read_only / disable_import / disable_persist / disable_reload_ops / disable_hot_reload_ops` + admin_token | `internal/agentconfig/config.go:850-857` |
| 诊断 | 发现错误 `errors[]`；依赖缺失 `unavailable[]`（missing_tools，含恢复引导）；**无重复名/shadow 报告** | `codex_list.go:25-47`、`internal/skill/unavailable.go:12-35` |
| per-skill 启停 | 无；`CodexSkillMetadata.Enabled` 恒 true 且不参与过滤；全局门 `skills_runtime.enabled`；profile 名单过滤 `SetNameFilter` | `codex_model.go:135`、`loader.go:312-321`、`skills_integration.go:952-954` |
| Web UI | skills 页（catalog/detail/hot-reload/stats）+ workspace 技能面板 + composer skill 对话框（只读浏览 + 热重载操作） | `frontend/src/pages/skills/*`、`hooks/use-runtime-skill-catalog.ts` |

### 2.6 CLI

| 命令 | 能力 | 位置 |
|---|---|---|
| `aicli skill install [name]`（alias `skills`） | 本地目录安装到 `codex|aicli|workspace` 目标根；`--source-dir/--target-dir/--dry-run/--force` | `cmd/aicli/commands/skill.go:41-85` |
| `aicli chat/exec --skills-dir/--skills-top-k/--skills-mode/--skills-debug` | 附加根、暴露 top-k、暴露模式、路由调试 | `chat_command.go:121-125`、`exec_common_flags.go:59-62` |
| 缺失 | `add <owner/repo>[@branch] [-g] [-s name]`、`list [--debug]`、`remove`；`--skill`（单根）/`--no-skills` | 反证核查见附录 B |

---

## 3. 与 Agent Skills 标准逐项对照

| 标准能力 | 现状 | 判定 |
|---|---|---|
| 目录/文件形态、`references|scripts|assets`、分组嵌套 | 已支持（深度 6；资源目录剪枝） | ✅ 对齐 |
| `name` / `description` 必填与长度限制 | 已支持（64 / 1024） | ✅ 对齐 |
| `license`、`compatibility`、`metadata`、`allowed-tools`、`disallowed-tools`、`argument-hint`、`when_to_use`、`disable-model-invocation`、`user-invocable`、`arguments`、`model`、`effort` | 全部不解析、静默忽略 | ❌ 缺 |
| 目录名 = name、name 字符集校验 | 未校验 | ❌ 缺 |
| `$ARGUMENTS` / `$ARGUMENTS[N]` / `${N}` / 命名参数 `${SKILL_DIR}` `${PROJECT_DIR}` `${SESSION_ID}` `${EFFORT}`（+ CLAUDE_* 别名） | 不存在 | ❌ 缺 |
| 正文动态 shell 注入（`` !`cmd` `` / ```` ```! ````） | 不存在（`internal/skill` 无 `os/exec`） | ❌ 缺（建议不做） |
| 发现：`.agents/skills` 项目级 + 用户级、≤10 层且到 home 停 | 项目级祖先遍历（到盘根，不因 home 停）；用户级 `.agents/skills` 无显式根 | ⚠️ 部分 |
| 额外位置（settings `skills` 数组 / `--skill` / `--no-skills`） | 由 `extra_skill_dirs` + `--skills-dir` 承担；无 `--skill`/`--no-skills` | ⚠️ 部分 |
| 同名遮蔽告警（duplicate names） | 无 | ❌ 缺 |
| `/skills` 浏览、`/skill` 调用 | 已有（TUI + Web） | ✅（形态不同） |
| 启停持久化（disabledSkills） | 无 per-skill 持久化 | ❌ 缺 |
| `skills add/list/remove` | 仅本地 `install` | ⚠️ 部分 |
| 热重载、治理、权限/审批、HTTP API、usage/quota | 明显超出标准 | ✅ 超出 |

---

## 4. 风险与约束（实施前必读）

1. **两种格式并存**：legacy `skill.yaml` 要求 `triggers`（`manifest.go:298-300`），Codex `SKILL.md` 跳过该校验（`manifest.go:293-295`）。任何标准字段实现都必须**限定在 Codex 路径**，否则会破坏 legacy。
2. **`internal/api/skills` 是巨型聚合包**：204 个文件、`handler.go` 约 472KB，且 `cmd/aicli`、`internal/runtimeserver`、`pkg/skillsapi` 都依赖其导出面。重命名必须一次性、可编译验证（见 §6）。
3. **冲突语义不统一**：初始注册（`registry.go:99-129`）与热重载（`hot_reload.go:623-646`）对同名/同 path 的取舍不完全一致。做遮蔽诊断时必须先统一口径，否则诊断结果会与真实可用集合不一致。
4. **占位符替换是"不可逆注入"**：替换结果若参与再次展开会形成注入面；必须约定"单趟替换、插入文本不再解析"。
5. **`settings.json` 是外部工具口径**：本仓库配置是 YAML（`configs/config.yaml` → `skills_runtime.*`），不要为对齐文档引入第二套配置载体。

---

## 5. 实施计划

优先级：**P2 > P1 > P3 > P5 > P4 >（P6 不做）**。每阶段独立可交付、可回滚，且必须保持 `go build ./...` 与既有测试通过。

### 5.1 P2 参数与占位符替换（最高收益）

**目标**：让 `SKILL.md` 正文与 guide 支持标准占位符，且不引入二次展开。

- 新增 `internal/skill/substitute.go`：
  - 支持 `$ARGUMENTS`、`$ARGUMENTS[N]`、`${N}`、声明的 `$name`（来自 frontmatter `arguments`）、`${SKILL_DIR}`、`${PROJECT_DIR}`、`${SESSION_ID}`、`${EFFORT}` 及 `CLAUDE_*` 兼容别名；
  - **单趟**扫描替换，插入文本不再解析；未识别的 `${...}` 原样保留；
  - 提供 `Substitute(text string, ctx SubstitutionContext) (string, SubstitutionReport)`，报告未解析 token 以便调试。
- 接线点：
  - TUI：`cmd/aicli/commands/chat_skill_turn.go`（`/skill <name> <args>` 解析 args → `resolveSkillTurnPin` 渲染 guide/正文）；
  - API：`internal/api/skills/handler.go` 的 `buildSkillExposureMessages`（请求体新增 `skill_args`，缺失时按空处理）；
  - catalog 描述里的 `argument-hint`（依赖 P1）用于前端提示。
- 灰度：`skills_runtime.argument_substitution`（默认 on，`off` 时保持现状）。
- 验收：单测覆盖单趟替换、未识别 token、别名、`$ARGUMENTS` 缺失、CLI 端到端（TUI pin guide 含替换结果）、API 端到端。

### 5.2 P1 标准 frontmatter 字段（低风险）

**目标**：Codex `SKILL.md` 兼容 Agent Skills 可选字段，并让其中"生效类"字段真正生效。

- `internal/skill/codex_manifest.go`：扩展 `codexSkillFrontmatter`（`license`/`compatibility`/`metadata.*`/`allowed-tools`/`disallowed-tools`/`argument-hint`/`when_to_use`/`disable-model-invocation`/`user-invocable`/`arguments`/`model`/`effort`），映射到 `CodexSkillMetadata`（新字段，JSON 化供 API/前端读取）；保持"未知键忽略"。
- 生效点：
  - `user-invocable=false` → 不进 `/skills` 菜单与 function 面；
  - `disable-model-invocation=true` → 不进 catalog / 不被隐式路由命中（显式 `/skill` 仍可用）；
  - `when_to_use` → 追加进 catalog 行；`argument-hint` → 透出到列表/选择器；
  - `allowed-tools`/`disallowed-tools` → 与既有工具策略求交集（`internal/policy` + `skills_integration` 的 `ProgramTools`），缺工具时沿用 `unavailable` 诊断语义；
  - `model`/`effort` → 记录并在 model 模式执行时作为建议值（不强制覆盖会话模型，避免惊喜）。
- 校验：name 字符集（小写字母/数字/连字符，不以下划线或连字符开头）与目录名一致性，先做**告警**（`errors[]` 之外新增 `warnings[]`），稳定后再考虑升级为错误。
- 验收：`codex_manifest_test.go` 扩展；`codex_list` 输出含新字段与 warnings；`/skills` 菜单过滤生效的测试。

### 5.3 P3 发现对齐与遮蔽诊断

- `internal/skill/roots.go`：显式追加 `~/.agents/skills`（user scope），与 `~/.aicli/skills` 并存、稳定去重。
- CLI：`chat_command.go` / `exec_common_flags.go` 增加 `--skill <dir>`（可重复，等价 `--skills-dir`）与 `--no-skills`（跳过自动发现，显式 `--skill` 仍生效）。
- 遮蔽诊断：在 discovery/registry 层收集同名冲突（name → 多个 path/scope），统一 registry 与 hot-reload 的优先级口径，并在 `/skills/list` 响应与 `aicli skill list --debug` 输出 `shadowed[]`。
- 不做：`settings.json` 的 `skills` 数组（用 `extra_skill_dirs` 表达即可，文档说明映射）。

### 5.4 P5 per-skill 启停

- 配置：`skills_runtime.disabled_skills: []string`（兼容读取外部 `disabledSkills`）；在 `loader.SetNameFilter` 统一生效（天然覆盖热重载与 profile 过滤）。
- 交互：TUI `/skills` 选择器 Enter 切换启停；Web skills 详情页开关；写回复用 config document 写路径（`/skills/config/write`）。
- 优先级：`profile allow/deny` 与 `disabled_skills` 的交集语义需在文档中固化（建议 deny 优先）。
- 验收：单测（filter 生效、热重载后仍生效）+ 配置写回测试。

### 5.5 P4 CLI add/list/remove

- `aicli skill add <owner/repo>[@branch] [-s <name>] [-g] [--force] [--dry-run]`：GitHub tarball 拉取 + 解包 + 校验（含 `SKILL.md`，名称与目录一致）；
- `aicli skill list [--debug]`：展示 roots、scope、不合法条目、遮蔽与 unavailable；
- `aicli skill remove <name> [-g] [--force]`：删除对应目录（默认仅删 `.agents/skills/<name>`）。
- 风险：供应链（默认 dry-run 提示、`--force` 才覆盖、记录来源 repo/commit 到 `metadata.source`）。

### 5.6 P6 正文 shell 注入（建议不做）

理由：既有能力已覆盖需求（catalog 指引模型自行读取正文 + 受控 shell 工具执行，全程走策略/审批/审计），而正文内联执行等于"读文件即可执行命令"，把不可信 skill 内容直接提升为命令执行入口，与 `internal/policy`、foldertrust、审批复用等既有防线冲突。

若未来确需实现，最低要求：默认关闭的 `skills_runtime.allow_skill_shell_substitution`、强制走既有工具策略与审批、输出字节上限、仅 trusted workspace、禁止嵌套替换、审计事件。

---

## 6. 包名调整：`internal/api/skills` → `internal/api/runtimeapi`（已完成）

**问题**：该包承载整个 runtime HTTP API（agent chat、session、team、profile、MCP admin、fs、git、skills、checkpoint、usage 等），"skills" 命名会误导后续维护者，属早期规划遗留。

**方案**：

1. `git mv backend/internal/api/skills backend/internal/api/runtimeapi`；
2. 包声明 `package skills` → `package runtimeapi`（204 个文件，机械替换）；
3. 全仓 Go 引用更新：`internal/api/skills` → `internal/api/runtimeapi`；调用方别名 `skillsapi`/`skillshandler` 统一为 `runtimeapi`（现有 22 个导入全部带别名，替换无歧义）；
4. 保留 `pkg/skillsapi`（对外 typed client）不改名——它是 skill API 客户端，命名仍准确；
5. 同步 `docs/skill_runtime/*` 当前文档；`docs/plan/*`、`docs/working/*` 等历史文档保留旧路径（历史记录不改写）；
6. 验证：`go build ./...`、`go vet ./internal/api/runtimeapi`、`go test ./internal/api/runtimeapi ./cmd/... ./internal/runtimeserver ./pkg/skillsapi -count=1`。

**注意**：不改包内类型名（如 `Handler`、`Server`），只改包名/路径，降低 diff 噪声与冲突面。

**执行结果（2026-09-25）**：
- 204 个文件的 `package skills` → `package runtimeapi`；21 个带别名的调用方去掉 `skillsapi`/`skillshandler` 别名并统一为 `runtimeapi`；
- 全仓 Go 文件内 `internal/api/skills` 路径引用（含注释与守卫测试路径字面量）已归零；
- `go build ./...` 通过；`go test ./internal/api/runtimeapi ./pkg/skillsapi ./internal/runtimeserver ./cmd/runtime-server ./cmd/aicli/commands -run ZZZ_NoSuchTest -count=1`（仅编译测试二进制）全部 ok；
- `pkg/skillsapi`（对外 typed client）与 HTTP 路由/类型名保持不变。

---

## 7. 验收与证据

- 现状结论证据：见 §2 各表 file:line；"不存在"类结论的检索清单见附录 B。
- 每阶段验收：新增/修改单测 + `go build ./...` + 相关包 `go test -count=1`。
- 端到端（P2）：TUI `/skill <name> <args>` 实际回合内 guide 文本含替换结果；`POST /api/agent/chat` 带 `expose_skills`+`skill_args` 时系统消息含替换结果。

---

## 8. 实施进度记录

| 阶段 | 状态 | 备注 |
|---|---|---|
| 文档落地 | 已完成 | 本文件 |
| §6 包名调整 | 已完成 | `internal/api/skills` → `internal/api/runtimeapi`，见 §6 执行结果 |
| P2 占位符与参数 | 已完成 | `internal/skill/substitute.go` + TUI `/skill` + API `skill_args` + 灰度开关，含单测 |
| P1 标准字段 | 已完成（生效面） | frontmatter 全字段解析 + `user-invocable` / `disable-model-invocation` / `when_to_use` 生效点 + `~/.agents/skills` 遮蔽与规范警告；`allowed-tools` 与 `model`/`effort` 仅解析透出，尚未接入工具策略与执行建议 |
| P3 发现对齐与遮蔽诊断 | 已完成（核心） | `~/.agents/skills` 用户根、`--skill`/`--no-skills`、同名遮蔽与规范 warning 进入 `codex_list` 响应 |
| P5 per-skill 启停 | 已完成（配置/热重载/API/TUI 命令） | `skills_runtime.disabled_skills` + `~/.agents/skills`；三处 loader 过滤器权威点同源；`config/document` 写入热生效；TUI `/skills disable\|enable <name>` 写配置 + 运行面热刷新；剩余为 picker 内 Enter 启停与 Web 详情页开关按钮（同一链路的 UI 糖） |
| P4 CLI add/list/remove | 已完成 | `aicli skill list [--debug]`、`skill remove <name> [-g]`、`skill add <owner/repo>[@ref]`（tarball + 校验 + 来源记录 + 路径穿越防护） |
| P6 正文 shell 注入 | 不做 | 见 §5.6 |

### 本轮实施记录（2026-09-25）

- 代码改动集中在：`internal/skill/{substitute.go,substitute_test.go,codex_standard_fields_test.go,codex_manifest.go,codex_model.go,codex_discovery.go,catalog_render.go,registry.go,roots.go,skill.go,summary.go}`、`internal/agentconfig/config.go`、`internal/api/runtimeapi/{handler.go,codex_list.go,skill_exposure_context_test.go}`、`cmd/aicli/commands/{chat_skill_turn.go,chat_skills_command.go,chat_skill_picker.go,skills_integration.go,chat.go,chat_options.go,chat_setup.go,chat_profile.go,chat_command.go,exec_common_flags.go,exec_run.go,exec_options.go}`。
- 验证命令与结果：
  - `go build ./...`：通过；
  - `go test ./internal/skill -count=1`：通过（含新增 `SubstituteSkillText` / 标准字段 / 遮蔽告警 / 用户级根用例）；
  - `go test ./internal/agentconfig -count=1`：通过；
  - `go test ./internal/api/runtimeapi -run 'TestBuildSkillExposureMessages|TestCodexList|TestCatalog' -count=1`：通过（含 `skill_args` 替换断言）；
  - `go test ./cmd/aicli/commands -run 'TestResolveConfiguredSkillDirs|TestSkill|TestChatSkill|TestBuildFunctionCatalog' -count=1`：通过。
- 已知外部干扰：工作区当时存在另一会话的并发改动（`internal/mcp/auth`、`internal/chat/planstore`、`cmd/aicli/commands` 的 MCP/plan-review 相关文件）。本轮的构建/测试结论以重新执行后的结果为准；`gofmt -w` 曾在 `cmd/aicli/commands` 产生一批纯格式噪音，已按"归一化后与 HEAD 相同即视为纯格式"逐个回滚（9 个文件），其余保留的是并发会话的语义改动。

### 第二轮实施记录（2026-09-25，P5 + P4）

- P5（per-skill 启停）：
  - 配置：`SkillsRuntimeConfig.DisabledSkills`（兼容 `disabledSkills`）+ `DisabledSkillNames()` 归一化（去空白/去重/保序）；
  - 过滤器：`internal/profileinput.WithDisabledSkills(base, disabled)`，语义固化为 **deny 优先**（禁用覆盖 profile allow），空名单时原样返回 base（未配置不改行为）；
  - 生效点（三处同源）：`cmd/aicli chat` 本地 host、runtimeapi profile registry 路径、`cmd/runtime-server` 启动 bootstrap；
  - 热重载：`bootstrap.Manager.ApplySkillNameFilter` 先 `SetNameFilter` 再 `registry.Clear()` + 全量重新发现，保证"禁用/解禁都立即生效"；`skills_runtime.disabled_skills` 已加入 `hotReloadSkillsRuntimePrefixes`，因此 `PUT /api/runtime/config/document` 写回即可热生效（Web 的开关按钮只是这一条链路的 UI 糖）；
  - 可见性：`codex_list` 响应新增 `disabled_skills`（缓存命中路径实时读取）。
- P4（CLI）：
  - `aicli skill list [--debug] [--cwd]`：展示发现结果、roots/scope、加载错误与标准告警；
  - `aicli skill remove <name> [-g] [--dir] [--dry-run] [--force]`：默认只动工作区 `.agents/skills/<name>`，frontmatter name 与目录名不一致时要求 `--force`；
  - `aicli skill add <owner/repo>[@ref] [-s name] [--path] [-g] [--dir] [--dry-run] [--force]`：codeload tarball 下载（64 MiB 上限）、解包拒绝路径穿越/链接条目、自动定位含 `SKILL.md` 的目录、名称一致性校验、覆盖保护、写入 `.aicli-source.json` 记录 repo/ref/path/时间。
- 验证命令与结果：
  - `go build ./...`：通过；
  - `go test ./internal/skill ./internal/agentconfig ./internal/profileinput ./internal/bootstrap ./internal/runtimeserver -count=1`：通过；
  - `go test ./internal/api/runtimeapi -run 'TestBuildSkillExposureMessages|TestCodexList|TestCatalog|TestSkill' -count=1`：通过；
  - `go test ./cmd/aicli/commands -run 'TestRunSkill|TestParseSkillAddSource|TestExtractSkillTarGz|TestLocateSkillDirInArchive|TestResolveConfiguredSkillDirs|TestSkill|TestChatSkill|TestBuildFunctionCatalog' -count=1`：通过。
- 仍未做：TUI `/skills` picker 内的 Enter 启停与 Web 详情页开关（两者都是"写 config document + 热重载"这一既有链路的 UI 入口；文本命令 `/skills disable|enable` 已在第三轮补齐）；P1 的 `allowed-tools` 策略求交与 `model`/`effort` 执行建议。

### 第三轮实施记录（2026-09-25，P5 交互面）

- 配置写回：`agentconfig.UpdateSkillsDisabledSkills(configPath, names)`（新文件 `skills_persistence.go`）。
  - 走统一配置文件写事务（与 provider / chat / theme 共用写锁），空名单删除键、非空统一写
    snake_case `disabled_skills` 并清理兼容 key `disabledSkills`；分层配置下按 key 路由到拥有层；
    空更新不会为删键而新造 `skills_runtime` 节点。
- TUI 交互：`/skills disable <name>` / `/skills enable <name>`（别名 `off`/`on`、`stop`/`start`），
  统一渲染通道与 legacy stdout 两条路径都接。
  - 校验：停用要求 skill 在当前函数面内（否则提示"未找到"），解禁要求确实处于停用态；
    名字大小写不敏感，落盘写规范名。
  - 语义：**没有可写配置文件路径时直接报错**，不做内存-only 的假开关。
- 运行面热刷新（新函数 `refreshSkillsRuntimeBinding`）：
  1. `manager.ApplySkillNameFilter(profile 选择 ∩ disabled_skills)` → loader 过滤器 + registry 重建；
  2. 用新的 summaries 就地更新同一个 binding（保持 catalog/session 引用有效），
     `PruneSkillFunctionsExcept` 撤销 stale 的 `skill__<name>` 函数与 catalog entry——
     否则停用后仍能被 `/skills` 选中并执行；
  3. 全部技能停用时同样走撤销分支（`len(summaries)==0` 的 reuse 路径），不会留下旧函数面。
  - 配套新增 `functions.FunctionRegistry.Unregister` 与 `binding.mcpRuntime`（刷新复用同一 MCP 通道）。
- 测试：`skills_persistence_test.go`（归一化/清理/不新造节点/路径必填）、
  `chat_skill_toggle_test.go`（停用-解禁闭环：registry+catalog 撤销与恢复、配置落盘与清理、
  未知名称与重复操作提示、结构化 `/skills disable` 路由）。
- 迁移债基线：legacy stdout 回退路径新增 1 个 `fmt.Println`（结果输出），
  `TestChatInteractiveDirectWriterInventory` 的 `chat_skills_command.go/handleSkillsMenuCommand`
  计数由 10 调整为 11，并在基线条目上注明原因；统一渲染通道那条路径不写 stdout。
- 验证命令与结果：
  - `go build ./...`：通过；
  - `go test ./internal/agentconfig ./internal/skill ./internal/profileinput ./internal/bootstrap ./internal/runtimeserver -count=1`：通过；
  - `go test ./cmd/aicli/commands -run 'TestChatInteractiveDirectWriterInventory|TestRunSkillToggleCommand|TestExecuteStructuredSkillsMenuCommand|TestParseSkillToggleQuery|TestRunSkill|TestInitSkillFunctions' -count=1`：通过；
  - 全包 `go test ./cmd/aicli/commands -count=1` 观察到一个与 skill 无关的顺序敏感用例
    `TestStreamingAssistantFinalTailTransfersExactlyOnceToNativeHistory` 失败（单独运行与和
    本轮新增用例同跑均通过），判断为并发会话在 streaming/history 区域的既有抖动，未做改动。

---

## 附录 A：关键文件索引（路径为当前口径，§6 完成后前缀改为 `internal/api/runtimeapi`）

| 主题 | 文件 |
|---|---|
| 根目录/发现 | `backend/internal/skill/roots.go`、`codex_discovery.go`、`codex_manifest.go` |
| 解析/加载 | `backend/internal/skill/manifest.go`、`loader.go`、`hydrate.go` |
| 注册表/冲突 | `backend/internal/skill/registry.go` |
| catalog 渲染/纪律块 | `backend/internal/skill/catalog_render.go` |
| 路由/隐式调用 | `backend/internal/skill/router.go`、`embedding_router.go`、`implicit_invocation.go` |
| 执行器/参数模板 | `backend/internal/skill/executor.go` |
| 热重载 | `backend/internal/skill/hot_reload.go` |
| API 注入点 | `backend/internal/api/skills/handler.go`（`buildSkillExposureMessages` / `buildSkillCatalogMessages`） |
| TUI /skill | `backend/cmd/aicli/commands/chat_skill_turn.go`、`chat_skills_command.go` |
| 配置 | `backend/internal/agentconfig/config.go`（`SkillsRuntimeConfig`） |

## 附录 B：反证核查检索清单（"不存在"即证据）

- 路径：`backend/internal/skill`、`backend/cmd/aicli`（含 `commands`）、`backend`（全包）、仓库根（含 docs/frontend/configs）。
- 关键词：`$ARGUMENTS` / `ARGUMENTS[` / `CLAUDECODE_SKILL_DIR` 系列 / `COMMANDCODE_SKILL_DIR` / `CLAUDE_SKILL_DIR` / `CLAUDE_PROJECT_DIR` / `SKILL_DIR` / `${0}`；`os/exec` / `exec.Command` / `CombinedOutput`；`` !` `` / ` ```! ` / `ShellBlock` / `executeInline` / `inlineShell` / `bangCommand`；`text/template` / `template.New` / `ExpandEnv`；`allowed-tools` / `disallowed-tools` / `argument-hint` / `when_to_use` / `disable-model-invocation` / `user-invocable` / `license` / `compatibility` / `effort`；`settings.json` / `no-skills`；`disabledSkills` / `disabled_skills` / `SetSkillEnabled`；`skill list` / `skill remove` / `skill add`。
- 结论：除 `$1`（markdown 正则，`cmd/aicli/formatter/markdown.go:280,299`）、`SKILLS_RUNTIME_SKILL_DIR`（配置环境变量名）、legacy `version` 字段外均无命中。
