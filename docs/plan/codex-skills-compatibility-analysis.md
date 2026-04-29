# Codex Skills 加载与解析机制对照分析

状态: **analysis**

范围:

- `E:\projects\ai\codex\codex-rs`
- `E:\projects\ai\ai-agent-runtime`

## 结论先行

两边的 skills 机制不是“同一模型的不同实现”，而是两套不同抽象：

- `codex` 侧是 **文档型 skill**，核心载体是 `SKILL.md`，再配套 `agents/openai.yaml`、`scripts/`、`references/`、`assets/`。
- 当前仓库是 **结构化 runtime skill**，核心载体是 `skill.yaml` + `prompt.md`，再配套 `workflow`、`context`、`permissions`、`tools`、`hot reload`、`registry/router/executor`。

因此，如果目标是“兼容并对齐 Codex skills 机制”，正确方向不是把现有实现直接改名成 `SKILL.md`，而是增加一层 **Codex 兼容/转换层**，并在 registry、解析、发现、注入和 API 语义上做结构性调整。

## 1. Codex skills 机制

### 1.1 目录和文件模型

Codex 的 skill 目录结构是明确的 progressive disclosure：

- `SKILL.md` 是必需入口，前置 frontmatter 只负责触发判断和最小元数据。
- `agents/openai.yaml` 是推荐的 UI/展示层元数据。
- `scripts/`、`references/`、`assets/` 是按需加载或用于输出的 bundled resources。

参考:

- [`E:\projects\ai\codex\codex-rs\skills\src\assets\samples\skill-creator\SKILL.md`](E:\projects\ai\codex\codex-rs\skills\src\assets\samples\skill-creator\SKILL.md)
- [`E:\projects\ai\codex\codex-rs\skills\src\assets\samples\openai-docs\agents\openai.yaml`](E:\projects\ai\codex\codex-rs\skills\src\assets\samples\openai-docs\agents\openai.yaml)

### 1.2 解析模型

`core-skills/src/loader.rs` 显示 Codex 的解析链路是：

- 先读 `SKILL.md`
- 只解析 YAML frontmatter
- frontmatter 只认 `name`、`description` 和 `metadata.short-description`
- body 的 Markdown 不参与触发判断，但会在 skill 被触发后作为内容载入
- 再可选加载 `agents/openai.yaml`，补充 `interface`、`dependencies`、`policy`

关键文件:

- [`loader.rs`](E:\projects\ai\codex\codex-rs\core-skills\src\loader.rs)
- [`model.rs`](E:\projects\ai\codex\codex-rs\core-skills\src\model.rs)

### 1.3 发现机制

Codex 的 root discovery 不是“给一组目录就扫一组目录”，而是有分层和优先级的：

- 从 config layer stack 里提取 project/user/system/admin 相关 root
- 再补充 repo 内 `.agents/skills`、用户目录 `~/.aicli/skills` / `~/.aicli/agents/skills`、系统缓存目录等
- 还会把插件或额外 roots 纳入同一 discovery 流程
- 最后按 path 去重，再按 scope 和 name 做稳定排序

对应逻辑位于：

- [`loader.rs`](E:\projects\ai\codex\codex-rs\core-skills\src\loader.rs)
- [`manager.rs`](E:\projects\ai\codex\codex-rs\core-skills\src\manager.rs)

### 1.4 注入与 invocation

Codex 把 skills 分成两类使用方式：

- **explicit invocation**: 用户在输入里显式点名，或者通过结构化 `UserInput::Skill { name, path }` 选择
- **implicit invocation**: 通过命令/文档路径等规则，推断是否应该自动启用某个 skill

它的 explicit 选择不是简单的“字符串包含 skill 名”，而是：

- 先按 path 精确匹配
- 再按 name / mention 处理
- 对重复 name 做歧义约束
- 对 disabled / policy 限制做过滤

相关实现：

- [`injection.rs`](E:\projects\ai\codex\codex-rs\core-skills\src\injection.rs)
- [`invocation_utils.rs`](E:\projects\ai\codex\codex-rs\core-skills\src\invocation_utils.rs)

### 1.5 API 语义

Codex 的协议侧不是单一“列出所有 skills”的平面列表，而是：

- `skills/list` 支持按多个 cwd 查询
- 支持 `force_reload`
- 支持 per-cwd extra roots
- 返回 `cwd -> skills -> errors` 的分组结果
- 另有 `SkillsChangedNotification` 用作失效通知

协议定义位于：

- [`v2.rs`](E:\projects\ai\codex\codex-rs\app-server-protocol\src\protocol\v2.rs)

## 2. 当前仓库的 skills 机制

### 2.1 文件模型

当前仓库是 YAML 驱动的 runtime skill：

- `skill.yaml` 是主清单
- `prompt.md` 是 companion prompt
- `skill.go` 里的 `Skill` 结构体同时包含 `triggers`、`workflow`、`context`、`permissions`、`handler`
- `LoadFileFull` 和 `DiscoverFile` 体现了“摘要/完整体”两个模式，但二者仍然围绕 `skill.yaml` 展开

关键文件：

- [`backend/internal/skill/skill.go`](E:\projects\ai\ai-agent-runtime\backend\internal\skill\skill.go)
- [`backend/internal/skill/manifest.go`](E:\projects\ai\ai-agent-runtime\backend\internal\skill\manifest.go)
- [`backend/internal/skill/loader.go`](E:\projects\ai\ai-agent-runtime\backend\internal\skill\loader.go)

### 2.2 解析与摘要

当前仓库的 manifest parser 做的是“整份清单解析 + companion prompt 加载”：

- `ParseFile` 直接读 `skill.yaml`
- `ParseSummaryFile` 读轻量摘要，用于 discovery-only 场景
- `ParseDir` / `ParseSummaryDir` 递归扫描 `.yaml` / `.yml`
- `prompt.md` 通过 `parsePromptMarkdown()` 解释成 `systemPrompt` / `userPrompt`
- `SaveFile` 会反向写出 `skill.yaml` 和 `prompt.md`

关键点：

- `prompt.md` 不是 Codex 那种 “SKILL.md body”
- 当前实现把 prompt 分成 `# System` / `# User` 两块，而不是 frontmatter + body
- 解析失败多数时候是 `fmt.Printf` 或 warning 记录，不是结构化 outcome 收集

参考:

- [`manifest.go`](E:\projects\ai\ai-agent-runtime\backend\internal\skill\manifest.go)
- [`loader.go`](E:\projects\ai\ai-agent-runtime\backend\internal\skill\loader.go)

### 2.3 registry / hydrate / hot reload

当前仓库的 skills 核心运行时更像“注册表 + 路由器 + 执行器”：

- `Registry` 用 `map[string]*Skill` 存 skill，主键是 `Name`
- `Register` / `Unregister` / `Get` / `List` 都围绕 name 唯一性
- `deduplicateSkillsByName()` 会直接把同名 skill 合并掉
- `HydrateSkillWithRegistry()` 通过 `skill.yaml` + `prompt.md` 恢复完整 skill
- `HotReload` 监听 `.yaml` / `.yml` 变更，按文件名重建注册表

这套模型的强项是：

- 运行时执行路径清楚
- workflow / tools / context / permissions 都是内建能力
- 支持热更新和 embedding router 同步

但它的身份模型是 name-based，不是 path/scope-based。

参考:

- [`registry.go`](E:\projects\ai\ai-agent-runtime\backend\internal\skill\registry.go)
- [`hydrate.go`](E:\projects\ai\ai-agent-runtime\backend\internal\skill\hydrate.go)
- [`hot_reload.go`](E:\projects\ai\ai-agent-runtime\backend\internal\skill\hot_reload.go)

### 2.4 bootstrap 与 API

`bootstrap/manager.go` 负责把 loader、registry、hot reload、embedding router 组装起来：

- `DiscoverOnly=true` 时只发现轻量 summary
- 否则加载完整 skill 并注册
- `skillDirs` 是外部传入的配置结果，而不是 Codex 那种从配置层和工作目录推导出的 root 集合

`api/skills/handler.go` 暴露的是偏管理后台风格的接口：

- `ListSkills` 返回全局 skills 列表
- `ReloadSkills` 手动重载
- `SearchSkills` 做关键词 / 模式 / embedding 搜索
- `ExportSkills` / `ImportSkills` 管理技能配置

`aicli` 侧则把 skill summary 变成可调用函数：

- 先 `DiscoverOnly` 初始化 runtime
- 再把每个 summary 注册成 `skill__*` 函数
- `AnalyzeSkillExposure()` 通过 prompt 文本、历史调用和路由器决定暴露哪些 skill function

参考:

- [`backend/internal/bootstrap/manager.go`](E:\projects\ai\ai-agent-runtime\backend\internal\bootstrap\manager.go)
- [`backend/internal/api/skills/handler.go`](E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\handler.go)
- [`backend/cmd/aicli/commands/skills_integration.go`](E:\projects\ai\ai-agent-runtime\backend\cmd\aicli\commands\skills_integration.go)

## 3. 关键差异对照

| 维度 | Codex | 当前仓库 | 结论 |
| --- | --- | --- | --- |
| 主文件格式 | `SKILL.md` + frontmatter | `skill.yaml` + `prompt.md` | 不是同一种清单格式，不能只改文件名 |
| 展示层元数据 | `agents/openai.yaml` | 无独立展示层文件 | 需要新增兼容解析和生成器 |
| 根发现 | project/user/system/admin 分层 discovery | 手工配置 `skillDirs` / CLI 传入 | 需要补 codex 风格 root 推导 |
| 身份模型 | path/scope 为主，name 只是显示字段 | name 唯一，registry 直接去重 | 必须改 registry 身份模型 |
| 摘要/完整体 | metadata 先行，body/resources 按需加载 | summary 和 full 都围绕 YAML manifest | 需要拆出 Codex 风格 discovery/hydrate 层 |
| 显式调用 | `UserInput::Skill { name, path }` + mention 解析 | prompt 文本扫描 + function 名 | 需要补 path-based 精确选择 |
| 隐式调用 | 命令/文档路径推断 | embedding router + prompt 文本路由 | 需要新增 implicit invocation 语义 |
| 错误模型 | `SkillLoadOutcome.errors` 结构化收集 | 多处 warning/printf，部分路径直接失败 | 需要改成结构化 outcome |
| API 形态 | `skills/list` 按 cwd 分组，支持 force reload / extra roots | `ListSkills` 全局平铺列表 | 需要做协议/接口兼容层 |
| 热更新语义 | 文件变更通知 + 失效后重拉 | fsnotify + registry 热重载 | 需要补“通知 + 失效刷新”语义 |
| bundle 资源 | `scripts/` / `references/` / `assets/` | `workflow` / `context` / `prompt.md` | 资源模型不同，需单独适配 |

### 3.1 为什么不能直接“改名兼容”

如果只把当前实现里的 `skill.yaml` 改成 `SKILL.md`，问题不会消失，反而会变成：

- frontmatter 只保留最小触发信息，当前 manifest 里的 `workflow`、`permissions`、`context`、`triggers` 没有自然落点
- Codex 的 `openai.yaml` 是展示层元数据，不是运行时主清单
- 当前 registry 的 name 唯一化会破坏 Codex 允许存在的多 root、同名、同 scope 组合
- 当前 `prompt.md` 的 System/User 分段逻辑不能直接映射 Codex 的 Markdown body + references 体系
- 当前 prompt 路由依赖 embedding / function exposure，不等同于 Codex 的 explicit / implicit invocation 规则

所以更合理的目标是：

1. 保留当前 runtime 作为 legacy 兼容路径
2. 新增 Codex 风格 adapter 作为并行入口
3. 最后再把两边收敛到统一的内部抽象

## 4. 如果要对齐 Codex，需要作哪些调整

### 4.1 必须新增的兼容层

1. **增加 `SKILL.md` frontmatter 解析**
   - 只解析 `name`、`description`、`metadata.short-description`
   - body 作为真正的 skill 内容，而不是触发字段
   - frontmatter 缺失或格式错误时，按 Codex 风格返回结构化错误

2. **增加 `agents/openai.yaml` 解析**
   - 读取 `interface.display_name`、`short_description`、`default_prompt`
   - 读取 `dependencies.tools`、`policy.allow_implicit_invocation`、`policy.products`
   - 这层应该是展示/补充层，不应覆盖 `SKILL.md` 的主语义

3. **增加 Codex 风格 discovery root 推导**
   - 支持 project / user / system / admin 的层级根发现
   - 支持 repo 内 `.agents/skills`
   - 支持 `~/.aicli/skills`、`~/.aicli/agents/skills` 兼容路径
   - 支持额外 roots，并保留 scope 优先级和去重规则

4. **把 registry 身份模型从 `name` 改成 `path + scope` 为主**
   - 允许同名 skill 并存
   - `name` 只能做展示字段或二级索引
   - `Get/Unregister/InvalidateLoadedSkill` 之类接口需要支持 path 索引
   - 选择/注入时必须能回到原始 `SKILL.md` 路径

5. **把错误收集改成结构化 outcome**
   - discovery 阶段要返回 `skills + errors`
   - 不要只靠 `fmt.Printf` 或日志 warning
   - 这样 `skills/list`、热更新、UI 诊断都能看到完整失败面

6. **补 per-skill filesystem 绑定**
   - Codex 会把 skill path 映射回具体的 file system，再按该 file system 读取 body / resources
   - 当前仓库基本默认本地 `os.ReadFile` / `os.Stat`
   - 如果要兼容远程挂载、插件 roots 或多文件系统来源，这层映射不能省

### 4.2 解析与资源模型要重构的地方

1. **把 `prompt.md` 角色降级为 legacy**
   - Codex 语义里，真正的内容主体在 `SKILL.md` body
   - 当前 `prompt.md` 的 `# System` / `# User` 分块可以作为兼容输入
   - 但不应继续作为唯一主语义

2. **补 bundled resources 解析**
   - `scripts/`：可执行资源，供 skill 调用或 patch 辅助
   - `references/`：按需加载的知识/规范
   - `assets/`：输出物资源，不应该默认进入 prompt 上下文
   - 当前 `workflow/context/permissions` 不能直接替代这些目录语义

3. **引入 explicit / implicit invocation 的统一选择器**
   - explicit: path 精确匹配优先，其次 name 解析
   - implicit: 基于命令、文件路径、文档路径等规则推断
   - 需要维护 name counts、path counts、blocked names、scope 过滤
   - 这比现在的 prompt 文本扫描和 embedding 路由更接近 Codex

4. **补 budgeted metadata rendering**
   - Codex 会按 context budget 截断 metadata
   - 当前仓库 `ListSkills`/`DiscoverAllWithRegistry` 是全量返回
   - 如果要在前端或模型上下文里对齐 Codex，需要增加类似 `build_available_skills()` 的预算渲染器

### 4.3 API / 协议层调整

1. **增加或兼容 `skills/list` 语义**
   - 按 cwd 维度返回数据
   - 支持 `force_reload`
   - 支持 `per_cwd_extra_user_roots`
   - 返回 `errors`，不要只返回平铺列表

2. **增加 skills 失效通知**
   - 文件变更后发通知
   - 客户端按当前参数重新拉取
   - 热更新不只是“改 registry”，还要“通知调用方刷新”

3. **把当前 `ListSkills` / `SearchSkills` / `ReloadSkills` 分出兼容与新语义**
   - 旧接口继续服务当前 runtime
   - 新接口承接 Codex 语义
   - 不要把现有 API 行为直接改掉，否则会回归现有功能

### 4.4 当前实现里需要特别注意的风险点

1. `cache_by_cwd` 只按 cwd 缓存，不能表达 codex 式的 root 组合和 extra roots。
2. `dedupeSkillsByName()` 会抹掉同名 skill，和 Codex 的 path/scope 语义冲突。
3. `findExplicitSkillMentions()` 依赖 prompt 文本和函数名，缺少 path 精确匹配和歧义处理。
4. `LoadFileFull()` 依赖 `prompt.md`，和 Codex 的 `SKILL.md` body 加载模型不一致。
5. `hot_reload.go` 的重载单元是 `skill.yaml`，而不是 `SKILL.md + openai.yaml + bundled resources` 这一整组文件。

### 4.5 审核补充：完全兼容 Codex 加载还必须覆盖的细节

现有报告已经抓住主干，但如果目标是“完全兼容 Codex 风格的 skills 加载”，还需要把以下边界行为纳入设计和测试。

1. **独立引入 Codex skill 领域模型**
   - 新增 `CodexSkillMetadata` / `CodexSkillLoadOutcome` / `CodexSkillError`，不要直接复用当前 `Skill`。
   - 字段至少包括 `name`、`description`、`short_description`、`interface`、`dependencies`、`policy`、`path_to_skills_md`、`scope`、`enabled`。
   - 当前 `Skill` 可以作为 legacy runtime execution model，Codex model 再通过 adapter 转换成可执行 skill。

2. **精确复刻 `SKILL.md` frontmatter 规则**
   - `description` 必须非空，最大 1024 字符。
   - `name` frontmatter 缺失时，Codex loader 会用父目录名兜底；最终 name 必须非空，最大 64 字符。
   - `metadata.short-description` 最大 1024 字符。
   - 所有这些单行字段都需要做 whitespace collapse，避免换行污染模型可见摘要。
   - `SKILL.md` body 不在 discovery 阶段展开，只在 explicit injection / hydrate 阶段读取。

3. **精确复刻 `agents/openai.yaml` fail-open 行为**
   - 文件不存在、不是普通文件、读取失败、YAML 无效或字段非法时，不能阻断 `SKILL.md` 加载。
   - `interface.icon_small` / `icon_large` 只能是 `assets/` 下的相对路径，不能接受绝对路径或 `..`。
   - `interface.brand_color` 只接受 `#RRGGBB`。
   - `dependencies.tools` 只在 `type` 和 `value` 都有效时进入结果。
   - `policy.allow_implicit_invocation` 默认等价于允许隐式调用。
   - `policy.products` 需要参与产品过滤，否则会加载到不该暴露的 skill。

4. **补全 Codex root discovery 的优先级和来源**
   - project config folder 下的 `skills` 是 repo scope。
   - repo 工作目录链路中的 `.agents/skills` 也是 repo scope，且需要从 project root 到 cwd 逐级发现。
   - `~/.aicli/skills` 是 user-installed skills 路径。
   - `~/.aicli/agents/skills` 是 alternate user skills 路径。
   - `./.agents/skills/.system` 是 embedded system skills cache。
   - 系统配置层的 `skills` 归入 admin scope。
   - 插件 roots 和 `per_cwd_extra_user_roots` 应按 user scope 进入同一加载结果。

5. **补全扫描、去重和排序细节**
   - 只把文件名精确为 `SKILL.md` 的文件作为 Codex skill 入口。
   - 跳过隐藏目录/隐藏文件。
   - 扫描深度需要有上限，Codex 当前是 `MAX_SCAN_DEPTH = 6`。
   - 每个 root 扫描目录数需要有上限，Codex 当前是 `MAX_SKILLS_DIRS_PER_ROOT = 2000`。
   - repo/user/admin scope 跟随 symlink 目录，system scope 不跟随。
   - path 要 canonicalize 后作为 identity。
   - 加载结果按 path 去重，而不是按 name 去重。
   - loader 结果排序是 repo -> user -> system -> admin；模型 prompt metadata 渲染排序是 system -> admin -> repo -> user。

6. **补全配置启停规则**
   - 支持 `skills.config` 里的 name selector 和 path selector。
   - disabled/enabled 规则只应从 user layer 和 session flags 读取。
   - 后出现的规则覆盖先出现的规则。
   - `SkillLoadOutcome` 需要保留 `disabled_paths`，列表 API 返回 enabled 状态，但 disabled skill 不应参与 explicit/implicit 注入。

7. **补全 bundled system skills 行为**
   - 启用 bundled skills 时，需要安装/刷新 embedded system skills 到 `./.agents/skills/.system`。
   - 关闭 bundled skills 时，需要清理或排除 system root。
   - 这不是普通 `skillDirs[0] == system` 能表达的行为。

8. **补全插件命名空间行为**
   - Codex 会根据 plugin skill path 推导 namespace，并把 name 变成 `namespace:name`。
   - 当前 registry 的 `buildSkillFunctionName()` 只做函数名清洗，不能代替 plugin namespace。
   - 兼容层需要把命名空间保留到 metadata、mention、API 和日志里。

9. **补全缓存键**
   - Codex 同时有按 cwd 的缓存和按 effective skill config 的缓存。
   - config cache key 至少应包含 root path、scope rank、skill config rules。
   - 如果只按 cwd 缓存，会让 session-local skill overrides、extra roots、bundled 开关互相污染。

10. **补全注入内容与 metrics**
    - explicit injection 读取的是完整 `SKILL.md` 内容，不是只读 body，也不是读取 `prompt.md`。
    - structured `UserInput::Skill { name, path }` 必须优先按 path 解析。
    - markdown linked mention、`skill://` path、普通 `$skill-name` 都需要处理。
    - 普通 name mention 只有在 skill name 唯一且不与 connector slug 冲突时才可解析。
    - 注入成功/失败要有 warning 和观测指标，失败不应直接中断本轮。

11. **补全 implicit invocation**
    - 需要维护 `scripts/` 目录索引和 `SKILL.md` 文档路径索引。
    - 执行 `python/bash/node/... scripts/foo.*` 这类命令时，能反推出对应 skill。
    - 读取 `SKILL.md` 时，能反推出对应 skill。
    - `allow_implicit_invocation=false` 或 disabled 的 skill 必须排除。

12. **补全协议兼容**
    - `skills/list` 响应应按 cwd 分组返回 `skills + errors`。
    - skill metadata 需要暴露 `scope`、`enabled`、`path`、`interface`、`dependencies`。
    - 文件变化只发 invalidation notification，由客户端带原参数重新 `skills/list`。
    - 当前管理型 REST API 可以保留，但不能作为 Codex 协议的唯一实现。

13. **补全测试矩阵**
    - frontmatter 缺失、description 缺失、name 缺失兜底、字段超长。
    - `openai.yaml` 不存在、非法、图标路径越界、brand color 非法、dependencies 缺字段。
    - 同名不同 path、同 path 多 root、不同 scope 排序。
    - user/session enable-disable 覆盖。
    - bundled skills 开关。
    - symlink 跟随和 scan limit。
    - explicit path mention、ambiguous name mention、connector slug 冲突。
    - implicit script run / doc read。
    - `skills/list` force reload、extra roots、errors 返回。

### 4.6 完全兼容时的推荐改造包

建议把改造拆成 6 个可验收的工程包，而不是一次性改掉现有 runtime。

1. **`internal/skill/codexmodel`**
   - 定义 Codex 风格 metadata、scope、interface、dependencies、policy、load outcome。
   - 保持和当前 `Skill` 解耦。

2. **`internal/skill/codexloader`**
   - 实现 `SKILL.md` / `agents/openai.yaml` 解析。
   - 实现 root discovery、scan、dedupe、config rules、bundled system skills。
   - 返回 `CodexSkillLoadOutcome`。

3. **`internal/skill/codexregistry` 或扩展现有 registry**
   - 支持 path/scope identity。
   - 支持 name index、path index、enabled index 和 loaded cache。
   - 保留 legacy `Get(name)`，但新增 path-aware API。

4. **`internal/skill/codexinvoke`**
   - 实现 explicit mention 解析和 implicit command detection。
   - 实现 `SKILL.md` 注入内容读取。
   - 处理 warning、metrics 和 disabled filtering。

5. **`internal/api/skills` 协议适配层**
   - 新增 Codex-compatible `skills/list`。
   - 新增 skills changed invalidation。
   - 保留当前 runtime 管理 API，不把旧接口硬改成新语义。

6. **legacy adapter**
   - 允许 `skill.yaml` 继续被当前 executor 使用。
   - 可选把 Codex skill 转换成当前 `SkillSummary`，用于现有 router/executor。
   - 不要求 `skill.yaml` 反向完全表达 Codex bundled resources。

## 5. 建议的迁移顺序

### Phase 1: 兼容读取

- 新增 `SKILL.md` + frontmatter 解析器
- 新增 `agents/openai.yaml` 解析器
- 先把 Codex skill 适配成当前 `Skill` / `SkillSummary`
- 旧的 `skill.yaml` / `prompt.md` 保持不变

### Phase 2: 发现与身份

- 把 root discovery 改成 scope-aware
- 把 registry 改成 path/scope 主键
- 引入结构化 errors / warnings
- 让同名 skill 能并存，不再默认 name 唯一

### Phase 3: 调用语义

- 增加 explicit/implicit invocation selector
- 增加脚本 / 文档路径索引
- 把 prompt 扫描、embedding 路由和 Codex mention 语义分开

### Phase 4: 协议与 UI 对齐

- 兼容 `skills/list`
- 增加失效通知
- 做 budgeted metadata rendering
- 再决定是否收敛当前 `ListSkills` / `SearchSkills` / `ReloadSkills` 的旧语义

### 5.1 当前实现进展

本轮已经把“扫描机制本身”补齐到了更接近 Codex 的状态，但默认技能目录来源仍保持当前仓库的配置驱动语义，避免把本机用户目录里的真实技能树自动拖入测试和普通运行：

- `ManifestParser.ParseDir` / `ParseSummaryDir` 已改成 BFS 扫描。
- 单 root 扫描现在受 `MAX_SCAN_DEPTH = 6` 和 `MAX_SKILLS_DIRS_PER_ROOT = 2000` 约束。
- 隐藏目录会跳过，`SKILL.md` 仍然是 Codex 入口文件，`agents/openai.yaml` 只作为 companion metadata。
- symlink 目录和普通目录的扫描规则已经统一到共享扫描器里，`system/.system` root 默认不跟随 symlink。
- `DiscoverCodexCompatibleSkillDirs` 已接入默认技能目录链路，`resolveConfiguredSkillDirs`、`resolvedExtraSkillDirs`、`resolvedExtraSkillDirsForHotReload` 都会合并 Codex 风格 roots。
- `discoverCodexCompatibleSkillDirs(anchor, configFile, homeDir, codexHome)` 已拆成可测试内部入口，单测不再依赖真实 `HOME` / `USERPROFILE`。
- 新增 `DiscoverCodexSkillLoadOutcome(anchor, configFile, extraRoots)`，会按 Codex 风格 `SKILL.md` 扫描并返回 `skills + errors`。
- 新增 `POST /api/runtime/skills/list`，返回按 `cwd` 分组的 Codex 风格发现结果，包含 `roots`、`skills`、`errors` 和 `force_reload` 字段。
- 新增统一技能变更事件 `skills.changed`，`CreateSkill` / `UpdateSkill` / `DeleteSkill` / `BatchCreateSkills` / `ImportSkills` / `ReloadSkills` / `StartHotReload` / `StopHotReload` / `ReloadHotReload` 以及 hot reload 回调都会发布，`/api/runtime/events` 可以按 `event_type=skills.changed` 直接查询。
- 相关回归测试已经通过：`go test -p 1 -vet=off ./internal/skill` 以及 `go test -p 1 -vet=off ./cmd/aicli/commands ./cmd/runtime-server ./internal/bootstrap ./internal/runtimeserver`。

## 6. 最终判断

如果只看“能不能列出 skill、能不能热重载、能不能执行”，当前仓库已经有一套完整 runtime。

但如果看“是否与 Codex skills 机制兼容”，当前仓库和 Codex 的差距主要在：

- 文件格式
- discovery 语义
- 身份模型
- explicit / implicit invocation
- 结构化错误和失效通知
- bundle 资源体系

因此，推荐把目标定义为：

> **在保留现有 `skill.yaml` 运行时的前提下，新增一条 Codex 风格的 skills 兼容链路。**

这样可以避免破坏现有 runtime，又能逐步向 Codex 的 `SKILL.md + openai.yaml + progressive disclosure` 模型靠拢。
