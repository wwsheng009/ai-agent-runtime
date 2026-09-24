# Profile 场景化上下文裁剪方案（Profile Presets & Context Pruning）

> 状态：待实施（2026-09-24 制定；同日第二轮深化：**配置域化可行性 / 特性开关与审批偏好 / 前端配置 UI**，见「第二部分」§8-§13；同日第三轮深化：**热切换（运行时 `/profile` 切换 + 缓存失效矩阵）**，见「第三部分」§14-§21；同日第四轮审查：**生命周期闭环（创建/修改/前端配置/使用切换）+ 项目级 profile 信任门控**，见「第四部分」§22-§26 与补遗 G1-G7）
> 范围：backend（`internal/profile`、`internal/profileinput`、`internal/skill`、`internal/mcp`、`internal/agentconfig`、`internal/chat`、`internal/api/skills`、`cmd/aicli`）+ **frontend（Profiles 页、编辑器与 `/profile` 命令，§10/§17）** + docs。
> 关联文档：
> - **实施方案**：`docs/plan/profile-scenario-implementation-plan-20260924.md`（批次执行顺序 / V 表门禁 / DoD / 验收与回滚——本文档负责"做什么"，实施方案负责"怎么做"）
> - 现有设计：`docs/multi-agents/profile/profile_system_implementation.md`、`profile_workspace_agent_design.md`、`aicli_profile_loading_flow.md`
> - 相邻能力：`docs/plan/mcp-tool-enable-disable-plan.md`（MCP 工具级启停）、`docs/plan/skills-exposure-implementation-and-test-plan.md`（skills 暴露链路）

## 0. 目标（用户诉求）

当前 agent 的 tools / skills / MCP 默认全量启用，单次请求携带大量工具 schema 与技能目录，token 上下文压力大。希望引入 **profile 场景预设**：按场景（编码 / 审查 / 文档 / 轻量问答）加载不同的 tools / skills / prompt，直接减小发往 LLM 的请求体积。

目标拆解：

1. **裁剪是真的省 token**：被 profile 排除的工具不出现在请求 `tools` 数组中（而非仅执行时拦截）；技能不进暴露候选；prompt 可精简。
2. **开箱即用**：提供内置场景模板与脚手架，创建可用 profile ≤ 2 条命令。
3. **可发现、可管理**：`aicli profile` 命令组（list / show / validate / create）与生效反馈。
4. **向后兼容**：不指定 profile 时行为与现状完全一致。
5. **多入口一致**：CLI chat / exec / agent stdio / runtime-server `/api/agent/chat` 共用同一解析层（沿用既有架构边界，不在 cmd 层重复实现）。

## 1. 现状与关键证据（file:line）

**本方案不是从零建设。** `internal/profile` 解析层与四条消费链路已经存在且带测试，主要缺口在"管理面、粒度、反馈"。

### 1.1 解析层（已完成）

| 模块 | 现状 | 证据 |
|------|------|------|
| Profile 域模型 | `ProfileSpec{profile, runtime, providers, mcp, skills, tools, agents}`；`ToolPolicySpec{allowlist, denylist, read_only, sandbox}` 已可用 | `backend/internal/profile/spec.go:4-63` |
| 目录约定 | root 下 `profile.yaml` / `runtime.yaml` / `mcp.yaml` / `skills/` / `agents/<id>/{agent.yaml, prompts/{system,role,tools}.md, tools/policy.yaml, skills/, workspace/}` | `backend/internal/profile/paths.go:41-80` |
| 解析与四层 merge | profile inline → agent inline → agent.yaml → workspace.yaml → tools/policy.yaml；输出 `ResolvedAgent` | `backend/internal/profile/resolver.go:45-120` |
| 引用解析规则 | 注册名 → config root；像路径 → 直接规范化；否则 `<defaultRoot>/<name>` | `backend/internal/profile/registry.go:45-69` |
| 全局配置 | `ProfilesConfig{root, default_profile, items{name:{root}}}`（`DEFAULT_PROFILE`/`PROFILES_ROOT` 可被 env 覆盖） | `backend/internal/agentconfig/config.go:762-771`；`registry_config.go:6-18` |

### 1.2 消费链路（已完成，但缺粒度）

| 链路 | 现状 | 证据 |
|------|------|------|
| CLI 入口 | `aicli chat --profile`、`aicli exec --profile`、`aicli agent stdio --profile` 均已接通；默认值取 `cfg.Profiles.DefaultProfile` | `chat_command.go:84`、`chat_profile.go:57-109`、`exec_common_flags.go:15`、`exec_run.go:111`、`agent.go:90` |
| **工具裁剪（真正省 token）** | profile 的 ToolPolicy → `session.ToolPolicy` → `FunctionCatalog.SetToolPolicy` → `Catalog.Select` 在构建请求时按 `AllowsDefinition` **跳过被排除工具的 schema** | `chat_setup.go:260-266`；`chatcore/catalog.go:210-222`；`policy/tool_policy.go:328-330`；`function_catalog.go:284-311` |
| 执行时拦截 | 同一 policy 在调用期兜底（deny / read_only / untrusted MCP 写操作） | `policy/tool_policy.go:80-91,161-205` |
| Prompt 注入 | profile 的 prompts（system/role/tools）组合 + context notes → **整体替换** `session.SystemPromptText` | `profileinput/inputs.go:59-66,96-98`；`chat_setup.go:255` |
| Skills 作用域 | profile 级 skill dirs（workspace > agent > profile > global）；exposure mode（auto/prefer/only）+ TopK 路由（默认 5）控制每轮暴露的 skill 函数 | `resolver.go:59-64`；`skills_integration.go:27,107-120` |
| MCP 作用域 | profile 解析出 MCP 配置路径（workspace mcp.yaml > profile mcp.yaml > global）；server 端 profile 拥有独立 MCPAdapter | `resolver.go:58`；`internal/api/skills/handler.go:1825-1826` |
| Server / API | `/api/agent/chat` 支持 `profile` 字段，含 prompt 自动路由（`routeProfileForPrompt`）；session metadata 记录 ProfileRef/Name/Agent/Root；checkpoint 记录 profile 资源溯源 | `handler.go:1695-1703,3870-3904` |
| Session | CLI 侧把 ProfileReference/Name/Agent/Root 写入会话 | `chat_setup.go:248-255` |

### 1.3 缺口证据

| 缺口 | 证据 |
|------|------|
| 无 profile 管理命令（list/show/validate/create 均不存在） | `cmd/aicli` 全量 grep 仅见 `--profile` flag 与 `{"profile","--profile"}` 参数提示（`command_invoke.go:809`），无子命令注册 |
| 无脚手架 | `aicli init` 只生成 starter 配置（`init.go:22-32`），不生成 profile |
| 内置模板缺失 | `examples/profiles/` 仅一个 `coding` 样例，且内容极简（只有 `role.md`，无 tools/skills/mcp 声明） |
| Skills 粒度不足 | `SkillsSpec` 为空壳（仅 inline extras），不能按 skill 名 allow/deny，只能整目录替换 | `spec.go:38-41` |
| MCP 粒度不足 | `MCPSpec` 仅有 `merge_strategy` + extras，不能声明"使用全局 mcp.yaml 中的哪几个 server"，只能整文件替换 | `spec.go:32-36` |
| Prompt 只有替换模式 | 组合文本整体替换 system prompt，无"基础 prompt + 场景追加"模式 | `chat_setup.go:255` |
| 无量化反馈 | 无命令/启动摘要展示"暴露 N 工具 / M 技能 / 估算 token"（exposure debug 深藏） | `skills_integration.go:1757`（debug 专用） |
| 无会话内切换 | profile 仅启动时决定；`/profile` slash 命令不存在 |

## 2. 差距分析与需求优先级

### P0（必须）

- **FR-1 管理命令组**：`aicli profile list | show | validate | create`。
- **FR-2 内置场景模板**：`coding` / `review` / `minimal` / `docs`，`profile create --template` 一键生成；`review`、`minimal` 必须产生显著更小的工具面。
- **FR-3 Skills 粒度裁剪**：profile 支持按 skill 名 `allowlist/denylist`，与目录替换正交叠加，deny 优先。
- **FR-4 MCP server 级选择**：profile 支持 `use_servers/exclude_servers` 引用全局 mcp.yaml 的 server 子集；被排除 server 不连接、不注册工具。
- **FR-5 量化与生效反馈**：`profile show` 与 chat 启动摘要输出工具/技能/MCP 计数与 schema token 估算；profile 解析失败保持显式报错（不静默回退全量）。
- **FR-6 优先级确定性**：`--profile` flag > `config.default_profile`（可被 `DEFAULT_PROFILE` env 覆盖）> 无 profile；与 `--agent`、CLI 覆盖的关系文档化并加测试锁定。

### P1（应该）

- **FR-7 Prompt 追加模式**：显式 `prompts.mode: append` 时，profile 文本叠加在内置基础 system prompt 之后（默认仍为 replace，行为不变）。
- **FR-8 会话内切换**：chat 内 `/profile <ref>`，重建工具/技能/prompt；MCP 来源变化时提示重启生效。
- **FR-9 子 agent 继承**：spawn 子会话默认继承父 profile 的裁剪策略（agentdef 显式声明可覆盖）。
- **FR-10 exec 元数据**：exec JSON 输出包含生效 profile 与工具面摘要，便于 CI 断言。

### P2（可选）

- **FR-11 CLI 场景自动路由**：`--profile auto`，复用 server 端 `routeProfileForPrompt` 思路并允许配置映射规则。
- **FR-12 Web 集成**：runtime-server 暴露 profile 只读列表/详情 API；frontend 设置页展示（需产品确认；注意与"subagent 路由难度档位 profile"命名区分）。
- **FR-13 统计聚合**：usage ledger 按 profile 维度聚合 token 消耗对比。
- **FR-14 项目级绑定**：workspace `.aicli/profile` 文件声明项目默认 profile。

### 非功能需求

- **NFR-1 向后兼容**：无 profile 时行为零变化。
- **NFR-2 分层复用**：新字段只扩展 `internal/profile` 的 spec/merge/validate；消费点复用 `ResolvedAgent` 契约；不在 cmd 层重复合并逻辑。
- **NFR-3 安全收窄**：profile 只能收窄、不能放宽安全基线（denylist 优先于 allowlist；`BlockUntrustedMCP`/`BlockRemoteWrites` 等不可被 profile 关闭）。
- **NFR-4 性能**：profile 加载不增加 chat 启动可感知延迟（守住现有 `startupTiming.mark("profile")`）。

## 3. 设计决策

### D1 复用既有解析层，只做"扩展 spec + 新消费点"

所有新增声明扩展 `internal/profile/spec.go`，经 `merge.go` 分层合并、`validate.go` 校验后进入 `ResolvedAgent`；CLI/API/背景 runtime 三面共用。**不引入第二套 profile 方言**（这是现有文档 `profile_system_implementation.md` §1 的既定架构边界，必须守住）。

### D2 Skills 粒度裁剪（FR-3）

```yaml
# profile.yaml / agent.yaml / workspace.yaml 均可声明，逐层 merge
skills:
  allowlist: [code-review, testing]   # 空 = 不限制
  denylist:  [browser-automation]     # 优先级高于 allowlist
```

- 语义分层：**目录决定"可发现集合"，allow/deny 决定"可暴露集合"**。被 deny 的 skill：不进 exposure 候选、不进技能目录清单、不可通过 slash 调用、不可被模型 skill 函数调用。
- 过滤点：skill 注册构建处（`cmd/aicli/commands/skills_integration.go` 的 registry 构建路径）与 server 侧对应路径（`internal/api/skills`）。确切函数在 Batch 0 定位后固化。
- 名字匹配：精确名（大小写不敏感）；不做通配符，避免引入第二套匹配语义（如后续需要，复用 `policy` 包既有匹配工具）。

### D3 MCP server 级选择（FR-4）

```yaml
mcp:
  use_servers:     [chrome-devtools]   # 白名单：只连接列出的 server（空 = 全部）
  exclude_servers: [playwright]        # 黑名单：排除（优先于白名单）
```

- 叠加关系：**文件替换优先**（profile/workspace `mcp.yaml` 决定 server 来源），`use/exclude` 在有效来源之上再做子集过滤。两者正交，可同时使用。
- 被排除的 server：不建立连接、不注册工具、不出现在管理面板"已连接"列表（可出现在"已配置但被 profile 排除"视图，便于排障——show 命令体现）。
- 与 `mcp-tool-enable-disable-plan.md` 的关系：那份方案是"连接后工具级启停"；本方案是"连接前 server 级过滤"。二者正交，文档需写明优先级：profile 过滤 > 配置 enabled/disabled > 工具级 UserEnabled。

### D4 Prompt 追加模式（FR-7）

```yaml
prompts:
  mode: replace   # 默认；replace = 现状（组合文本整体替换 system prompt）
  # mode: append  # 组合文本追加在内置基础 prompt（agentguidance 等）之后
```

- 默认 `replace`，保证零行为变化；只有显式 `append` 才叠加。
- 叠加顺序：内置基础 prompt → profile 组合文本（system → role → tools 段序保持 `LoadPromptLayers` 的 LayerBase/LayerDeveloper 语义）→ profile context notes。
- 实现点：`internal/profileinput/inputs.go` 组合函数 + `chat_setup.go:255` 注入点 + server 侧 `handler.go:1873` 对应分支。

### D5 `aicli profile` 命令组（FR-1/FR-2）

| 子命令 | 行为 |
|--------|------|
| `profile list` | 枚举三来源：① config `profiles.items` 注册项 ② default root 下含 `profile.yaml` 的子目录 ③ 显式路径；标注哪个是当前默认生效项、哪个来自 env |
| `profile show <ref> [--agent X]` | 解析后展示：provider/model/agent、工具面（allow/deny 原始声明 + 最终允许数量 + 被排除清单）、skills（目录 + allow/deny + exposure mode/topk）、MCP server（来源文件 + use/exclude 后实际连接清单）、prompt（来源文件 + mode）、所有解析出的文件路径 |
| `profile validate <ref>` | 校验：profile.yaml 语法与必填项；allowlist/denylist 引用的工具名是否存在（内置清单来源见 Batch 0）；skill 名/目录存在性；mcp server 引用存在性；prompt 文件可读；merge 冲突（如 allow 与 deny 同名） |
| `profile create <name> --template <coding\|review\|minimal\|docs> [--root <dir>] [--use] [--agent <id>]` | 从内置模板生成目录树；`--use` 写入 config `profiles.items[name].root`（走现有 config 写锁与分层写路由），可选 `--set-default` 写 `default_profile` |

- 命令注册对齐现有方式（`cmd/aicli/commands/` 下新增 `profile*.go` 并挂到 root command）。
- 内置模板随二进制分发：`backend/internal/profile/templates/<name>/`（go:embed），不依赖仓库 checkout；`examples/profiles/` 保留为参考样例，与模板同源由测试保证一致。
- `aicli init` 增加可选 `--with-profiles`（默认关闭，不改变现有行为）。

### D6 量化与生效反馈（FR-5）

- 新增轻量估算：`schema JSON 字节数 / 4`（明示为"估算"），放 `internal/profile/estimate.go`，供 show 与启动摘要共用。
- chat 启动摘要：profile 生效时输出一行（工具数/技能数/MCP server 数/估算 token），走现有 status/notice 通道（`session.Interaction.RefreshStatus`），并受现有 quiet 模式抑制。
- 失败语义：profile 引用解析失败保持现有报错路径（`ResolveRef` 返回 error，不静默回退全量）；新增"配置了 default_profile 但目录缺失"的显式告警。

### D7 会话内切换（FR-8，P1）

- `/profile <ref>` slash 命令：重建 ToolPolicy（复用 `BaseToolPolicy` + `applyChatPermissionsOverlay` 机制）、prompt、skill dirs 与 exposure 绑定；更新 session metadata 并发布 runtime event。
- MCP 来源变化（profile 指向不同 mcp.yaml 或 use/exclude 变化）**不在会话内热替换**：给出"重启生效"提示（保底降级），热替换作为后续独立议题（涉及 MCP adapter 生命周期与进行中回合一致性）。
- 切换后对当前回合边界生效，避免同回合工具前缀变更破坏 prompt cache。

### D8 子 agent 继承（FR-9，P1）

- 核实现状（Batch 0）：spawn 路径是否已把 `session.ToolPolicy` 传入子会话。
- 目标语义：未声明 `agents.<id>.tools` 时，子会话默认继承父 profile 的裁剪策略；显式声明可覆盖但**不可放宽**安全基线（NFR-3）。
- 覆盖链：父 profile policy → 子 agentdef binding → 子会话；在 `agentdef.BuildBinding` / spawn 装配处落地。

### D9 exec 元数据（FR-10，P1）

- `aicli exec` 的 JSON/JSONL 输出增加 `profile` 字段（ref/name/agent/tool 数/skill 数），便于 CI 断言实际生效的裁剪；不改变默认文本输出形状。

### D10 优先级规则固化（FR-6）

```
--profile flag  >  DEFAULT_PROFILE env（经 config.default_profile）  >  无 profile（全量）
--agent flag    >  profile.default_agent  >  "default"
profile tool policy 与 CLI 工具开关（如有）叠加时：deny 恒优先
```

以表驱动测试锁定（`chat_profile_test.go` 扩展）。

### D11 命名区分

"runtime profile"（本方案）与 "subagent 路由难度档位 profile"（`AICLISubagentRouteProfile`，见 `agentconfig`）是两个概念。代码符号不重命名（避免无谓 churn），但**文档、CLI 帮助、Web 文案**必须区分：前者叫 `profile`，后者在用户可见文案中用 "route profile / 路由档位"。

## 4. 实施批次与文件清单

### Batch 0 — 核实 spike（0.5 天，先于编码）

1. **MCP 工具过滤链路核实**：catalog 层已确认按策略过滤 builtin 条目（`chatcore/catalog.go:210-222`）；需核实 ① MCP 工具进入 CLI chat function registry 的确切注册函数 ② agent 层 tool surface（`internal/agent/tool_list.go` 的 `ShouldList` 路径）是否同样受 ToolPolicy 过滤，覆盖 exec/headless 与子 agent 路径。若缺口存在，纳入 Batch 1。
2. **skill 注册构建点定位**：`skills_integration.go` 中 skill registry 构建函数（allow/deny 插入点），及 server 侧对应点。
3. **内置工具名清单权威来源**：供 `profile validate` 使用（优先复用 catalog 构建入口，避免手工清单漂移）。
4. **spawn 子会话 ToolPolicy 传递现状**（D8 前置）。

产出：结论回填本文档附录（file:line），作为 Batch 1 的输入。

### Batch 1 — spec 扩展 + 三处过滤落地（P0）

- `backend/internal/profile/spec.go`：`SkillsSpec{Allowlist,Denylist}`、`MCPSpec{UseServers,ExcludeServers}`、新增 `PromptsSpec{Mode}`。
- `backend/internal/profile/merge.go`：三层 merge 规则（profile/agent/workspace 同名集合取并集；deny 恒优先）。
- `backend/internal/profile/validate.go`：mode 枚举、集合去重与冲突检查。
- `backend/internal/profileinput/inputs.go`：`ResolvedToolPolicy` 传递不变；prompt mode 进入 `ComposeSystemPrompt`/注入点。
- skills 过滤：`cmd/aicli/commands/skills_integration.go`（Batch 0 定位点）+ `internal/api/skills` 对应路径。
- mcp 过滤：chat bootstrap（`chat_bootstrap.go`/`chat_setup.go` 消费 `MCPConfigPath` 处）与 server 侧 profile runtime state（`handler.go:1825` 附近），按 `use/exclude` 过滤 server 集合后再连接。
- prompt mode：`chat_setup.go:255`、`handler.go:1873` 分支按 mode 选择替换或追加。
- 测试：spec/merge/validate 单测；skills deny 后不进 exposure/目录/调用；mcp 过滤后不连接不注册；prompt append 与 replace 两态。

### Batch 2 — `aicli profile` 命令组 + 模板（P0）

- 新增 `backend/cmd/aicli/commands/profile.go`、`profile_list.go`、`profile_show.go`、`profile_validate.go`、`profile_create.go`；root command 注册。
- `backend/internal/profile/templates/{coding,review,minimal,docs}/`（go:embed）+ `templates.go` 渲染（name/description 注入）。
- `backend/internal/profile/estimate.go`：schema token 估算。
- 模板内容要点：
  - `review`：`tools.allowlist` 只读集（read/grep/glob 等）+ `read_only: true`；无 MCP；精简 role prompt；
  - `minimal`：最小工具集（如仅 read/grep）；`skills.denylist: ["*"]` 等价开关或空目录；最短 prompt；
  - `docs`：文件读写 + 搜索；docs 类 skill allowlist；deny shell 类；
  - `coding`：全工具基线（对齐 examples/profiles/coding，补 tools 声明与 README）。
- 测试：四个子命令路径（含错误分支）；模板渲染快照；`examples/profiles` 与模板一致性测试。

### Batch 3 — 反馈、exec 元数据与文档（P0/P1）

- chat 启动摘要（status 通道 + quiet 抑制）；`profile show` 复用同一估算输出。
- `exec_run.go` 输出结构增加 profile 元数据（D9）。
- 文档：新增 `docs/aicli/profiles.md`（使用指南：概念、目录约定、四个模板、优先级规则、validate 语义、与 mcp 工具级启停的关系）；更新 `docs/aicli/install.md`、`docs/README.md` 索引；`docs/multi-agents/profile/profile_system_implementation.md` 追加"现状与后续"注记。

### Batch 4 — 会话内 `/profile` 切换（P1）

- slash 命令注册与实现（对齐现有 chat slash 命令表）；重建 ToolPolicy/prompt/skills；MCP 变更提示重启；session metadata + runtime event；`chat_profile_test.go` 扩展。

### Batch 5 — 子 agent 继承（P1）

- 按 Batch 0 结论在 spawn 装配处落地继承语义；新增测试：子会话工具面 ⊆ 父 profile 允许集（除非显式声明且不放宽基线）。

### Batch 6 — P2（按需排期）

- `--profile auto` 自动路由（映射规则配置化，复用 server 端 `routeProfileForPrompt` 思路）；
- runtime-server 只读 API + frontend 展示；
- usage ledger 按 profile 聚合；
- workspace `.aicli/profile` 项目级绑定。

## 5. 验收与度量

1. **裁剪量化（以实测基线为准）**：`aicli profile show minimal` 与 `review` 的工具 schema 数量 ≤ 全量基线的 40%；请求 artifact（`~/.aicli/chat-logs/*_request_*.json`）验证被排除工具确实不在 `tools` 数组中。
2. **可用性**：`aicli profile create review --use` → `aicli chat` 两条命令即可使用场景化裁剪。
3. **校验能力**：`profile validate` 能检出——allowlist 引用不存在的工具名、skill 名/目录不存在、mcp server 引用不存在、prompt mode 非法/文件不可读、allow 与 deny 同名冲突。
4. **回归**：无 profile 场景全部现有测试通过、行为零变化；重点回归面：`cmd/aicli/...`、`internal/profile/...`、`internal/profileinput/...`、`internal/api/skills/...`、`internal/chatcore/...`。
5. **多入口一致**：chat / exec / agent stdio 对同一 profile 解析出的工具面与 prompt 一致（表驱动测试）；server 侧 `/api/agent/chat` 同语义（可后置到 P2 集成测试）。

## 6. 风险与兼容性

| # | 风险 | 缓解 |
|---|------|------|
| R1 | **MCP 工具过滤层级差异**：catalog 层已确认过滤，但 agent 层 tool surface（`tool_list.go`）与子 agent 路径未完全核实；若缺口存在，"省 token"对 MCP/headless 场景不成立 | Batch 0 先核实；缺口纳入 Batch 1 一并补齐并加断言测试 |
| R2 | **prompt append 叠加顺序**：与内置基础 prompt（agentguidance/capability 等段）的顺序若不稳定，会破坏 prompt cache 或语义 | 固定叠加顺序为"内置基础 → profile 组合（system→role→tools）→ context notes"，并加顺序断言测试；默认 replace 保证现状不动 |
| R3 | **会话内切换的 MCP 热替换复杂度**：adapter 生命周期与进行中回合一致性成本高 | P1 保底降级：MCP 变更提示重启生效；热替换独立议题 |
| R4 | **与 MCP 工具级启停方案的交互**：两套过滤叠加可能让用户困惑"为什么工具没出现" | 明确优先级（profile server 过滤 > 配置 enabled > 工具级 UserEnabled）；`profile show` 展示被排除项与排除来源 |
| R5 | **模板与 examples 漂移** | 同源测试：模板渲染产物与 `examples/profiles` 一致性断言 |
| R6 | **工具名清单漂移**：validate 的内置工具名来源若手工维护会过期 | Batch 0 选择复用 catalog 构建入口作为权威来源；测试锁定 |
| R7 | **默认全量不变**：本方案不引入"精简默认"，用户仍可能不建 profile 而继续全量 | 通过 `profile show` 的量化对比、文档引导与 `init --with-profiles` 降低采用门槛；是否改默认值列入开放问题 |

## 7. 开放问题（待拍板）

1. **默认行为**：`aicli chat` 不带参数时是否默认套用精简 profile？（本方案建议：保持全量，仅通过 `create --set-default` 显式设置，确保零破坏。）
2. **token 估算口径**：`字节数/4` 粗估即可，还是接入 usage ledger 精确计量（成本高、P2）？（建议先粗估并在输出中标注。）
3. **Web UI 排期**：P2 的 runtime-server 只读 API + frontend 展示是否本期需要？（涉及与"路由档位 profile"的文案区分工作量。）
4. **模板集合**：`coding/review/minimal/docs` 四个是否够？是否需要 `web-debug`（chrome-devtools MCP 场景）？

## 附录 A — Batch 0 核实结论（待回填）

| 核实项 | 结论 | 证据 |
|--------|------|------|
| MCP 工具进入 CLI function registry 的注册函数 | 待核实 | — |
| agent 层 tool surface 是否受 ToolPolicy 过滤（exec/子 agent） | 待核实 | — |
| skill 注册构建点（allow/deny 插入点） | 待核实 | — |
| 内置工具名清单权威来源 | 待核实 | — |
| spawn 子会话 ToolPolicy 传递现状 | **API 侧曾缺失继承（Batch 5 已修复）**：`sessionAgentController.Spawn` 只写父子/根/深度等上下文，从不复制父会话 profile 绑定，子 actor 构建期 `profileState == nil`，工具面可宽于已收窄的父策略（违反 FR-9）；本地侧天然继承（与父共用同一 `ChatSession`/`apiAgent`，`applyLocalChildAgentdefToolPolicy` 再收窄）。修复：`sessionmeta.CopyProfileBinding` 单点快照 + API/本地两个 spawn 装配点调用；子策略 = 父 profile 允许集 ∩ 子 agentdef 声明（`DeriveChild`，只收窄），快照语义不追溯已存在子代理 | `internal/api/skills/session_runtime_support.go:604-611`（快照点）、`:4176-4202`（agentdef 叠加层 `DeriveChild`）、`cmd/aicli/commands/chat_actor_registry.go:715-722`、`cmd/aicli/commands/chat_actor_host.go:1591-1596`；测试 `internal/api/skills/session_profile_inheritance_test.go`（5 例）、`internal/sessionmeta/profile_binding_copy_test.go`（4 例）、`cmd/aicli/commands/chat_profile_child_inheritance_test.go`（1 例） |

## 附录 B — 目录与文件参考

```text
<profile root>/
  profile.yaml            # name / description / default_agent / tools / skills / mcp / providers
  runtime.yaml            # profile 级 runtime 覆盖
  mcp.yaml                # profile 级 MCP server 定义（可被 use/exclude 再过滤）
  skills/                 # profile 级 skill 目录
  agents/<id>/
    agent.yaml            # provider / model / permission_mode / sandbox / tools / skills
    prompts/{system,role,tools}.md
    tools/policy.yaml     # 工具策略（allowlist/denylist/read_only/sandbox）
    skills/               # agent 级 skill 目录
    workspace/            # workspace.yaml / mcp.yaml / skills/（最高优先层）
```

现有样例：`examples/profiles/coding/`（仅 profile.yaml + agents/{default,explore}，含 role.md；缺 tools/skills/mcp 声明，Batch 2 补齐为完整参考）。

---

# 第二部分：深化设计（2026-09-24 第二轮）

> 本轮回答三个深化命题：
> ① **配置域化**：profile 是否应做成"管理特定版本 config.yaml、覆盖现有配置一部分"的配置域层？
> ② **特性配置**：功能开关（是否启用某一项功能）与审批方式偏好如何建模？
> ③ **前端 UI**：`/frontend` 如何提供 profile 配置 UI 与操作便利性（MCP server list / skill list / tools list / agents 启用）？
>
> **结论先行**：
> ① **可行**——且仓库已有约八成基建（分层合并、层溯源、层感知写回均已实施；profile spec 已预留 `runtime.overrides` / `mcp.merge_strategy` 扩展点但未接线）。不是新造系统，是"接线 + 覆盖白名单 + UI 呈现"。
> ② **合理**——但必须按"已有配置开关 / 工具面裁剪 / 会话偏好"三类分别建模；**禁止发明假开关**（每个开关必须有单一权威生效点与断言测试）。
> ③ **可复用现有"配置域编辑器"框架**——前端已有 17 个域的编辑器基建、MCP 完整管理 UI（含工具级启停）、skills 页、会话级权限切换控件；增量集中在"Profiles 页 + 清单化选择器 + 来源/影响反馈"。

## 8. 深化命题一：profile 作为"配置域层"的可行性

### 8.1 命题拆解与判定

用户原话："配置域化，比如使用 profile 来管理特定版本的 config.yaml……profile 类似于更细化的配置，可以覆盖一部分的现有配置。"

拆成三个可判定的子命题：

| 子命题 | 判定 | 理由 |
|--------|------|------|
| P1 技术上可行（有合并语义与层模型可复用） | ✅ 可行 | 见 §8.2：合并函数、层栈、溯源、写回规则全部现成 |
| P2 与现有配置体系不冲突（不产生第二套方言） | ✅ 可行，但须定义覆盖白名单与优先级 | 见 §8.3/§8.4：复用 YAML + 现有合并语义；安全域禁止覆盖 |
| P3 产品上合理（用户可理解、可排障） | ⚠️ 有条件成立 | 必须配套"来源溯源 + 校验 + 影响预览"，否则"配置为什么没生效"会成为主要支持成本，见 §8.5 |

### 8.2 现成基建盘点（为什么不是从零建设）

| # | 能力 | 现状 | 证据（file:line） | 对 profile 的意义 |
|---|------|------|-------------------|-------------------|
| 1 | 稀疏覆盖合并语义 | 已实施，经预设层验证 | `agentconfig/preset.go:371`（`MergeConfigYAML`）、`:440`（`mergeMergeMaps`）；语义注释 `:366-370` | profile overrides 复用同一语义（map 递归 / 标量·slice 覆盖 / 未写键不 shadow），保证"只写差异键" |
| 2 | config.yaml 分层合并 | P0/P1 已实施 | `docs/plan/layered-config-merge-design-20260917.md` 状态行；`ConfigLayerStack()`（`runtimeserver/config_external_reload.go:261`） | profile 层只需回答"插到层栈哪一层 / 还是走会话级 overlay" |
| 3 | runtime.yaml 分层 | P2-7 已实施 | `runtimeserver/runtime_config_layers.go:13-19`（`RuntimeConfigLayerStack()`） | profile 目录下的 `runtime.yaml` 未来可同样获得层语义 |
| 4 | 层溯源已暴露到 HTTP API | 已实施 | `runtimeserver/config_document.go:83`（`doc.Origins`）、`:343-351`（`doc.Layers`）、`:364-371`（`impact.PathLayers`）；断言 `config_document_layered_test.go:95-100`、`:191-196`（`Origins["providers.items.openai.base_url"]=="user"` 等） | UI 可直接渲染"该键来自哪层"，profile 覆盖只需新增一个 `profile` 来源标记，**无需新建溯源机制** |
| 5 | 层感知写回规则 | 已定义 | `layered-config-merge-design-20260917.md` §7（R1 读哪层写哪层 / R2 按层分摊 / R3 删除即写 null / R4 保留字面量 / R5 目标统一） | profile 编辑写回沿用同一套规则（写 profile.yaml，不写全局文件） |
| 6 | profile 已预留覆盖扩展点（未接线） | 字段已解析、**零消费方** | `profile/spec.go:28`（`RuntimeSpec.Overrides`）、`:34`（`MCPSpec.MergeStrategy`）；仅解析断言 `profile/spec_test.go:31/37`；全仓 grep 无消费点 | 实施成本是"接线 + 语义定义"，**不是从零设计**；同时也说明：不接线它就是一个"看起来能配、实际无效"的陷阱字段，必须一并处理 |
| 7 | 会话装配注入点 | 已存在 | `chat_profile.go:57`（`resolveChatProfileState(cfg, opts)`）、`:15-30`（`chatProfileState` 已含 `PermissionMode`/`RuntimeConfig`/`MCPConfig`/`SkillDirs`） | 覆盖结果有明确的注入位置；`RuntimeConfigPath()`/`MCPConfigPath()`（`:36-48`）已是"整文件替换"的现成通道 |
| 8 | 审批偏好通道 | 已存在（未宣传） | `chat_profile.go:23-25`（`PermissionMode` 注释：仅 CLI 未显式指定时应用） | 命题二的"审批偏好"已有 50%：只差文档化、UI 暴露与其它审批维度聚合 |

### 8.3 覆盖语义设计（D12-D15）

**D12 三种覆盖模式并存，按"省心 → 精确"梯度，默认推荐 B：**

| 模式 | 声明方式 | 语义 | 适用场景 | 状态 |
|------|----------|------|----------|------|
| A 整文件替换 | profile 目录放 `runtime.yaml` / `mcp.yaml` | 完全替换全局文件 | 场景差异极大、愿意整份维护 | ✅ 已实现（`ResolvedAgent.RuntimeConfig`/`MCPConfig`，`resolved.go:63-64`） |
| B 稀疏覆盖 | `runtime.overrides:` 键树（+ `mcp.use_servers/exclude_servers`） | 复用 `MergeConfigYAML` 语义，只写差异键 | **大多数场景（推荐默认）** | ⛔ 字段已存在（`spec.go:28`）但未接线 |
| C 引用合并 | `runtime.base: <path>` + `overrides` | 以指定文件为 base 再叠加 overrides | 复用既有配置片段 | ⛔ 未定义（P2 可选） |

**D13 覆盖生效位置（会话级 overlay，而非全局文件层栈）：**

- 推荐 **M2 会话级 overlay**：`config 正常分层加载（L0-L5 现状不动） → profile 解析 → 对 cfg 做一次内存合并（复用 `MergeConfigYAML` 语义） → 会话装配使用合并视图`。
  - 优先级（低 → 高）：内置预设 < 便携默认 < 用户级 < 项目级 < **profile overrides** < 显式 CLI flag / 进程环境变量。
  - 理由：a) profile 是"当前会话的显式选择"，应高于背景文件层；b) 命令行/env 仍最高，保持"flag 可临时覆盖一切"的直觉；c) 不动全局层栈 ⇒ 不影响现有写回目标选择（layered-config §7）与热加载归因，风险面最小。
- 备选 **M1 文件层插入**（P2，若未来要"profile 级 runtime-server 进程配置"）：把 profile overrides 作为层栈一层。代价：`default_profile` 来自 config 自身 → 两阶段加载（鸡生蛋），且污染写回/热加载归因。**本方案不推荐**。
- 实现落点：`chatProfileState` 增加 `ConfigOverlay`（已合并视图 + 覆盖键清单 + origins），`chat_setup.go` 装配处消费；server 侧 `/api/agent/chat` 同语义。
- **落地状态（2026-09-24，V27 已回填）**：CLI 与 aicli 进程内 web 会话两路径经 `applyProfileStateToChatSession` 单点消费；server 侧（`internal/api/skills`）配置消费面为 `Handler.aicliConfig` 快照，**请求级已接线**（`profileRuntimeState.ConfigOverlay` + `skillsRuntimeConfigFor` → AgentChat 的 catalog/exposure 注入，`handler.go:1962`/`:2419`），**会话级 routing 三件套为余项**（actor 构建/spawn 期读取，需 actor 缓存失效矩阵配套，登记为 Batch 12 前置）。白名单边界：`skills_runtime.*` 是**字段级**白名单（`overrides.go:63-70`，6 键），`catalog_budget_chars`/`document_mode`/`discipline_block` **不在放行集内**——前端 Overrides 卡片不得展示这些键的编辑入口（R9 假开关陷阱）。

**D14 覆盖白名单（安全硬约束）：**

| 类别 | 键域 | 判定 |
|------|------|------|
| ✅ 允许覆盖（场景相关、低风险） | `aicli.chat.*`（默认模型/推理档/流式）、`skills_runtime.*`（目录/曝光模式/topk；**V11 修正**：挂**配置根** `agentconfig/config.go:42`，不是 `aicli.*` 子键）、`aicli.log.*`、`aicli.retry.*`、`aicli.timeout.*`、`aicli.balance.*`、`aicli.theme.*`、`aicli.model_cards.*`、`aicli.mcp.config_file`、`aicli.subagents.*`（仅 `routing`；**V8**：无 `enabled` 字段）、`aicli.teams.*`（同上）、`aicli.main_agent.*`、`providers.default_provider` / `model_aliases`（已有 spec 字段） | 白名单放行 |
| ⛔ 禁止覆盖（安全/信任边界） | `skills_runtime.admin_token`、`auth` 相关、`providers.*` 的密钥与端点类键、foldertrust 状态、`runtime.mode` / `runtime.server_url`（进程级拓扑） | 校验器**按键前缀拒绝**并报错 |
| ⚠️ 待定（需单独评审） | `providers.items.*` 的非密钥字段（base_url/model 列表） | 列入开放问题；`skills_runtime.enabled` 已于 Batch 9 定案（V11：真实开关、单一权威生效点 `skills_integration.go:944` → **纳入白名单**） |

- 实现方式：`profile/validate.go` 增加覆盖键白名单校验（`profile validate` 与运行时解析双重执行），**不靠文档约定**。
- 原则复述（与第一部分一致）：profile 只能收窄安全基线；覆盖白名单是同一原则在"数据面"的投影。

**D15 写回与编辑边界：**

- 编辑 profile 覆盖 → **只写** `<profile root>/profile.yaml`（或 profile 级 runtime.yaml），永不写全局 config.yaml；
- 编辑全局配置（现有 `/api/runtime/config/document`）→ 沿用层感知写回（R1-R5）；profile 层不参与该写回的目标选择；
- 禁止把"解析后的合并视图"当作写回内容（layered-config R2 的教训对 profile 同样成立）。

### 8.4 与现有配置体系的关系（冲突分析）

结论：**不冲突**，但必须遵守三条纪律：

1. **一个键只有一个权威来源层**：profile 覆盖键与全局键同名时，以 profile 层为准，origins 标记为 `profile`（复用现有 origins 结构，不新增格式）。
2. **不引入新语法**：复用 YAML 与现有合并函数（`MergeConfigYAML`/`mergeMergeMaps`），不引入第二套方言（与第一部分设计原则一致）。
3. **特殊值语义与 layered-config §4 完全对齐**，UI 必须显式呈现，否则必然误操作：
   - `{}` ≠ 清空（语义为"本层没写任何键"→ 回落低层）；
   - `null` = 显式屏蔽（删除该键，回到"未配置"）；
   - slice（列表）= **整表替换**，不做元素级合并。

### 8.5 P3 条件（产品合理性）的具体要求

"配置域化"只有在配套以下能力时才成立，否则用户无法理解生效结果：

- **来源可见**：每个生效键可回答"来自哪层"（复用 origins，UI 用徽标呈现）；
- **影响可预览**：改覆盖前可预览"哪些键会变化、影响哪些会话行为"（复用 `POST /config/document/preview` 与 `impact.PathLayers` 模式）；
- **非法可拦截**：白名单外的键、拼错的键、类型不符的键在保存时即报错（不是静默忽略）；
- **可回退**：覆盖可一键清空（回到全局配置），并有 diff 视图。

## 9. 深化命题二：特性开关与审批偏好

### 9.1 建模原则：开关必须分类，禁止发明"假开关"

用户诉求："profile 不但是几个配置，还需要能进行特性配置，比如是否启用某一项功能开关，比如审批方式的偏好设置。"

仓库现状给出的教训：**一个开关只有在"有单一权威生效点"时才是真开关**。因此按生效点把"特性"分三类建模：

| 类别 | 定义 | 建模方式 | 为什么不能混用 |
|------|------|----------|----------------|
| **T1 已有配置开关** | config.yaml 中已存在的 `enabled` 类字段 | profile 覆盖该键（D12 模式 B） | 重新发明一个同名开关会造成双权威（配置一个值、工具面另一个值），必然漂移 |
| **T2 能力裁剪型**（无配置开关） | 能力由"工具是否存在"驱动（如 spawn_agent / spawn_team） | profile 工具面 allow/deny（**唯一真实生效点**） | 新增 `subagents.enabled` 之类的开关，若工具面不联动，就是假开关——UI 上关了、模型仍能调用 |
| **T3 会话偏好型** | 每会话可变的偏好（权限模式、推理档、模型） | profile 提供**默认值**，会话内可覆盖（`requested_*` vs `effective_*` 机制） | 若做成硬开关，会破坏"会话内可切换"的既有能力（composer 控件、slash 命令） |

### 9.2 开关目录（Feature Flags Catalog，首批）

| 特性 | 类别 | 权威生效点（file:line） | profile 声明方式 | 备注 |
|------|------|--------------------------|------------------|------|
| Skills 运行时总开关 | T1 | `skills_runtime.enabled`（`agentconfig/config.go:811`，env `SKILLS_RUNTIME_ENABLED`；**配置根**，`config.go:42`） | 覆盖 `skills_runtime.enabled`（**V11 修正**：根级路径；旧稿写的 `aicli.skills_runtime.enabled` 是 dormant 假开关，Batch 7 已修正白名单） | ✅ 已接线（Batch 7 白名单 + Batch 9 断言 `overrides_catalog_test.go`）；生效点 `skills_integration.go:944` 早退 → **profile 级 skill 目录同样被 gate** |
| Skills 曝光模式 / TopK | T1 | `aicli_skill_exposure_mode` / `_top_k`（`config.go:815-816`） | 覆盖 + 已有 profile 级 exposure 绑定（第一部分 FR） | 与第一部分 FR-4 同一消费点 |
| 日志开关 | T1 | `log.enabled`（`config.go:550`） | 覆盖 `aicli.log.enabled` | 低风险 |
| Model cards | T1 | `model_cards.enabled`（`config.go:644`） | 覆盖 | 低风险 |
| 主 Agent 动态 provider/model 切换 | T1 | `aicli.main_agent`（`config.go:540`，注释明确"默认关闭"） | 覆盖 `aicli.main_agent.*` | 场景开关典型：review profile 可关 |
| 子代理（spawn_agent / spawn_subagents） | T2 | 工具面：`toolbroker/spawn_agent_permission.go` + 工具可见性（`toolkit/listable.go` 等） | `tools.denylist: [spawn_agent, spawn_subagents]` | ⛔ 不新增 config 开关；**V8 已核实**：`AICLISubagentsConfig` 只有 `routing`（`config.go:650-653`），**无 `enabled` 语义** → T2 只能纯工具面建模；review 模板 denylist 已落地（Batch 9） |
| 团队（spawn_team / runtime teams） | T2 | 同上（团队工具 + teams 配置节） | `tools.denylist: [spawn_team]` | **V8 已核实**：`AICLITeamsConfig` 只有 `routing`（`config.go:655-659`），无 `enabled` → 纯工具面建模；review 模板 denylist 已落地（Batch 9） |
| MCP server 级启停 | T1（既有） | `mcp.yaml` server `enabled` + `mcp/admin` Service（已完成，见 `docs/plan/mcp-management-ui-plan.md`） | profile 用 `mcp.use_servers/exclude_servers` **选择视图**（不写 mcp.yaml） | 优先级：profile 过滤 > mcp.yaml enabled > 工具级 enabled（第一部分 R4 已定） |
| MCP 工具级启停 | T1（既有） | `mcp.yaml` `tools.<name>.enabled`（`mcp/config/types.go`）+ 注册表 `EnableTool/DisableTool` | 不在 profile 重复实现；profile 编辑器内**联动展示**（见 §10） | 前端对话框已实现批量启停（`mcp-tools-dialog.tsx:94-110`，实测已接通） |
| Plan mode / 工具面其它能力 | T2 | 工具可见性 + `planmode` 状态 | `tools.denylist` | 与 plan mode 的交互列入开放问题 |
| 审批：权限模式默认 | T3 | `permission_mode`（`sessionmeta/sessionmeta.go:35`；`requested_/effective_` `:46-47`） | profile/agent 级 `permission_mode` 默认（Batch 9 修正：**profile 路径此前丢失默认值**，现由 `profileAgentPermissionMode` 走 agentdef 权威解析填充，`chat_profile.go:162-200`） | 仅当 CLI 未显式 `--permission-mode/--yolo` 时应用——保持"显式优先"（`chat_profile_test.go:170-178` 锁定）；profile 不得默认 `bypass_permissions`（D16，`agentdef/build.go:125-135`） |
| 审批：会话内切换 | T3 | `composer-permission-mode-control.tsx` + `/permission-mode` 类命令 | 不改（profile 只提供默认值） | 会话内切换已有，profile 不应抢占 |
| 审批：supervision 审批（多代理） | T3 | `supervision.Config`（`agentconfig/config.go:43`；键含 `wake_max_*` / `turn_end_check` / `progress_check_interval` / `approval_terminal_guard` / `message_semantics_v2`，`internal/supervision/config.go:18-121`） | **不纳入 profile 覆盖目录**（V9 结论） | 已可配置，但属**宿主级 supervision 运行时预算/灰度**（唤醒配额、巡检间隔、终态守卫），不是"审批偏好档位"：profile 的审批语义由 `permission_mode` 默认（T3/D16/D17）+ 工具 denylist（T2）承担。纳入白名单会造出"能调 supervision、却与审批决策无关"的假开关（R9） |
| runtime-server 策略（auth/mutation/usage/governance policy） | ⚠️ 进程级 | `handler.go:826-831`（四组 policy API） | **不纳入 profile**（进程级，非会话级） | 列入开放问题：若用户要"团队共享审批策略"，那是 runtime-server 配置域的议题 |

### 9.3 审批偏好设计（D16-D17）

**D16 审批偏好 = "默认值 + 收窄约束"，不做硬开关：**

- profile/agent 声明 `permission_mode`（已有通道）：作为会话默认，会话内可被用户切换（T3 语义）。
- 硬约束：**profile 不得把默认放宽到 `bypass_permissions`**（`chat_permission_mode.go:14-24` 的四个档位中最高危档）。校验规则：profile 默认值 ∈ {default, accept_edits, plan}；若用户确需 bypass，走显式 CLI flag（`--yolo`）并在 UI 标注风险。
- 理由：profile 常随仓库/团队分发（项目级绑定），若允许默认 bypass，等于"打开别人的仓库就自动免审批"——与 foldertrust 门禁的信任模型直接冲突。

**D17 审批偏好的可发现性（UI 侧）：**

- 会话顶部（composer）已有权限模式控件 → profile 生效时显示"默认来自 profile X"的来源标注；
- `profile show` / UI 详情显示：默认权限模式、实际生效模式（`effective_`）、是否被会话覆盖。

### 9.4 反模式清单（明确不做）

1. ❌ 新增 `subagents.enabled` / `teams.enabled` 而不联动工具面（假开关）；
2. ❌ 在 profile 里重复实现 MCP 工具级启停（与 `mcp.yaml` 双写、双权威）；
3. ❌ 把 runtime-server 的进程级 policy（auth/mutation/usage/governance）塞进 profile（作用域错位）；
4. ❌ 用 profile 默认值强制覆盖用户在会话内显式选择的权限模式/模型（破坏 T3 语义）；
5. ❌ 为"看起来有用"的字段（如 dormant 的 `mcp.merge_strategy`）先建 UI 再想语义——先定语义再接线（见附录 C 待核实项）。

## 10. 深化命题三：前端配置 UI 设计

### 10.1 现状资产盘点（可直接复用的部分）

| 资产 | 位置 | 可复用性 |
|------|------|----------|
| 配置页与域编辑器框架 | `frontend/src/pages/runtime-config-page.tsx` → `components/workspace/settings/backend-config-settings-page/`（17 个 mode：providers/agentRouting/providerGroups/networkProxy/auth/routing/rateLimit/resourceManager/providerQueue/concurrency/retry/monitor/websocket/circuitBreaker/transformer/**mcp**/source，见 `mode-registry.ts:7-115`） | ⭐ 直接复用：新增 `profiles` mode 即可挂进同一菜单与壳 |
| 域对话框壳 | `settings/config-domain-dialog.tsx`（`ConfigDomainDialog`，`max-w-5xl`，eyebrow/title/description/footer 插槽） | ⭐ profile 编辑器可直接用同一壳 |
| 通用设置组件族 | `settings/settings-section.tsx`、`settings-toggle-card.tsx`、`settings-field-card.tsx`、`settings-choice-card.tsx`、`settings-empty-state.tsx`、`settings-dialog.tsx`、`settings-notice-card.tsx` 等 | ⭐ 卡片/开关/空态/对话框全部现成，避免新造视觉语言 |
| 反馈组件 | `settings/impact-panel.tsx`（影响面）、`settings/validation-panel.tsx`（校验）、`settings/preview-section.tsx`（预览）、`settings/unsaved-bar.tsx`（未保存）、`settings/runtime-config-diff.ts`（diff） | ⭐ "改前预览 / 校验 / 影响 / diff"四件套现成 |
| MCP 管理 UI | `sections/modes/mcp.tsx`（列表/CRUD/启停/热重载）、`mcp-form.tsx`、`mcp-tools-dialog.tsx`（**工具级勾选 + 批量启停，已接通** `setRuntimeMcpToolEnabled`/`setRuntimeMcpToolsEnabled`）、`api/runtime/mcp.ts` | ⭐ MCP server list 与 tools list 诉求**已满足**；profile 侧只需"选择视图" |
| Skills 管理 UI | `pages/skills-page.tsx`、`hooks/use-skills-catalog.ts`、`hooks/use-skills-market.ts`、`api/runtime/skills.ts` | ⭐ skill list 诉求已满足；profile 侧只需"选择视图" |
| 权限模式控件 | `components/workspace/composer-permission-mode-control.tsx`（会话内切换 + 测试） | ⭐ 审批偏好 UI 的既有入口，profile 只加"默认值来源"标注 |
| agents / teams 可视化 | `session-agents-panel.tsx`、`session-agents-tree.tsx`、`runtime-teams.tsx`、`api/runtime/agents.ts` | ◐ 可复用展示组件；"按 profile 启用/禁用 agents"的编辑视图需新增 |
| 配置 API 客户端 | `api/runtime/config.ts`（document GET/PUT/preview/agent-route-preview/max-steps/service） | ⭐ 新 profile API 按同一风格扩展 |
| admin token | `lib/admin-token.ts` | ⭐ 写操作鉴权沿用 |

**结论**：前端不是"从零做配置 UI"，而是"在既有配置域编辑器上加一个 profiles mode + 三类选择器（tools/skills/mcp）+ 覆盖域编辑器"。

### 10.2 信息架构（建议）

```
Settings（现有设置入口）
├── …（现有：chat / appearance / harness / notification / workspace / about）
└── Profiles（新增，或作为 runtime/config 下的一个 mode）
    ├── 列表视图
    │   ├── 卡片：name / description / 来源徽标（config.items | default root | env | 显式路径）
    │   ├── 当前生效项高亮（default_profile / --profile / 会话绑定）
    │   └── 操作：新建（模板向导 / 复制 / 从会话固化，§23 G1）/ 重命名 / 移动 / 删除（引用检查，§23 G2）
    │       / 导出 / 导入（§23 G5）/ 设为默认 / 应用到当前会话（§23 G3，两动作文案互斥）
    └── 编辑器视图（ConfigDomainDialog 壳，左侧卡片导航 + 右侧表单）
        ├── 1 基本（name / description / default_agent）
        ├── 2 工具面（allow/deny 勾选 + 生效预览 + 估算 token）
        ├── 3 Skills（目录 + allow/deny + 曝光模式/TopK）
        ├── 4 MCP（server 勾选 use/exclude + 工具级状态只读联动）
        ├── 5 Prompts（system/role/tools 文件 + replace/append + 预览）
        ├── 6 Agents（列表 + 启用/禁用 + 默认 agent）
        ├── 7 覆盖域 Overrides（键级编辑 + 白名单校验 + 来源徽标）
        ├── 8 偏好与审批（permission_mode 默认 / reasoning_effort / 模型）
        └── 9 校验与影响（validate + impact + diff + 保存）
```

### 10.3 编辑器卡片细则（对应用户诉求点）

| 卡片 | 用户诉求 | 数据源（API） | 关键交互 |
|------|----------|---------------|----------|
| 2 工具面 | tools list | `/api/runtime/capabilities`（`handler.go:802`，**内容待核实**是否含内置工具全清单）+ `/mcps/{name}/tools` | 按来源分组（内置/MCP/skill）勾选；allow 与 deny 互斥校验；实时显示"生效 N 个工具 / 估算 token / 被排除清单"（对齐第一部分 FR-5） |
| 3 Skills | skill list | `/skills`（`handler.go:796`）、`/skills/list`（`:797`） | 目录多选 + allow/deny 勾选 + 曝光模式（auto/topk/all）下拉；显示 TopK 值 |
| 4 MCP | mcp server list | `/mcps`（`:876`）、`/mcps/{name}/tools`（`:882`） | 勾选"本 profile 使用/排除的 server"；工具级状态**只读展示**（改动跳转到现有 MCP 面板，避免双权威）；显示排除后剩余工具数 |
| 5 Prompts | prompt 管理 | 文件浏览复用 `fs-*` API；预览用 `/debug/prompt-layout`（`:844`） | 文件选择器 + replace/append 单选 + 叠加预览（render 后效果） |
| 6 Agents | 是否启用 agents | agents 清单 API（**待核实**：`api/runtime/agents.ts` 现有端点是否可直接列 profile 级 agent） | 列表 + 启用开关（映射到 profile `agents` map / 工具面 denylist 提示）+ 默认 agent 选择 |
| 7 覆盖域 | 配置域化 | `/config/document`（读全局视图 + origins）+ 新 profile 读写 API | 键级编辑；**来源徽标**（profile/global/默认）；白名单外键置灰并说明；特殊值提示（`{}`/null/slice 语义，§8.4） |
| 8 偏好与审批 | 审批偏好 | 现有 permission mode API | permission_mode 默认选择（`bypass_permissions` 置灰，见 D16）；显示"会话可覆盖"提示 |

### 10.4 关键交互原则

1. **改前预览、改后可回退**：任何覆盖编辑先走 preview（复用 `preview-section` 模式与 `runtime-config-diff.ts`），保存后提供 diff 与"清空覆盖"。
2. **来源徽标无处不在**：生效工具数、每个覆盖键、权限模式默认值都要能回答"来自哪里"。
3. **校验前置**：白名单、allow/deny 冲突、skill/mcp 引用存在性在**输入时**提示，保存时强校验（对齐 `validation-panel` 模式）。
4. **不做双写**：MCP 工具级、mcp.yaml 内容、全局 config.yaml 的编辑入口保持唯一（跳转现有面板），profile 只做"选择/覆盖视图"。
5. **量化反馈**：工具面卡片常驻"估算 token / 工具数"（第一部分 FR-5 的估算函数前后端共用口径，避免两套数字）。

### 10.5 后端 API 扩展清单（新增，风格对齐现有 `/api/runtime/*`）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/runtime/profiles` | 列表：三来源枚举 + 当前生效标注 + 解析状态（ok/error） |
| GET | `/api/runtime/profiles/{ref}` | 解析后视图：工具面（生效/排除/来源）、skills、mcp（use/exclude 后实际连接清单）、prompts（路径+mode）、agents、overrides（含 origins）、估算 token |
| PUT | `/api/runtime/profiles/{ref}` | 写回 profile.yaml（白名单校验 + 原子写 + 冲突检测）；**不触碰全局 config.yaml** |
| POST | `/api/runtime/profiles/{ref}/validate` | 校验：语法/引用存在性/白名单/冲突（对齐 `profile validate`，CLI 与 UI 共用实现） |
| POST | `/api/runtime/profiles/{ref}/preview` | 预览解析结果与影响（对齐 `config/document/preview` 模式） |
| POST | `/api/runtime/profiles` | 创建：模板 / 复制 / 从会话固化（三模式合一，§23 G1/D24） |
| POST | `/api/runtime/profiles/{ref}/duplicate` | 复制到目标层（内置 profile 只读，复制即"另存为"，§23 G1） |
| POST | `/api/runtime/profiles/{ref}/rename` | 重命名（同层；返回旧/新 ref 与引用更新情况，§23 G2） |
| POST | `/api/runtime/profiles/{ref}/move` | 层级移动（user↔project；同层或跨层冲突拒绝，§23 G2） |
| DELETE | `/api/runtime/profiles/{ref}` | 删除（引用检查；`?force=true` 处理 default 引用，§23 G2/D25） |
| GET | `/api/runtime/profiles/{ref}/references` | 只读：列出四类引用（删除/移动前展示，§23 G2） |
| POST | `/api/runtime/profiles/{ref}/default` | 设为默认（写 config `profiles.default_profile`，只影响**新会话**，§23 G3/D26） |
| POST | `/api/runtime/profiles/{ref}/apply` | 应用到当前会话（= 第三部分 `set_profile`，下一 turn 生效，§23 G3/D26） |
| POST | `/api/runtime/profiles/{ref}/export` | 导出（目录/zip；不支持单文件内联格式，§23 G5/D28）——**Batch 13 slice 2 已落地（D32/D33）** |
| POST | `/api/runtime/profiles/import` | 导入（先 validate、绝不自动激活、显示路径清单，§23 G5/D28）——**Batch 13 slice 2 已落地（D32/D33）**；**CLI 入口已接线（slice 3）**，TUI/前端入口待接线 |

- 写操作沿用 admin token 鉴权与原子写工具；错误码风格对齐现有 handler。
- 复用优先：`{ref}` 解析直接调 `internal/profile` 的 registry/resolver，**不新建解析逻辑**。
- **本表已按第四轮补遗同步（修复内部不一致）**：原 `activate` 端点按 D26 拆分为 `default`（新会话）/ `apply`（当前会话）两个端点；创建/修改/分享端点来自 §23 G1-G5，实施批次见 §24 Batch 13。

### 10.6 用户诉求 → 落地方式 对照表

| 用户原话诉求 | 现状 | 落地方式 |
|--------------|------|----------|
| "mcp server list" | ✅ 已有（`mcp.tsx`） | profile 编辑器内嵌"勾选视图"（use/exclude），不复制 CRUD |
| "skill list" | ✅ 已有（`skills-page.tsx`） | profile 编辑器内嵌"勾选视图"（allow/deny + 目录） |
| "tools list" | ◐ MCP 工具有（含启停）；内置工具清单 UI **待新增** | 工具面卡片（数据源 `/capabilities`，待核实内容） |
| "是否启用 agents" | ◐ 有展示（session-agents / teams 面板），无 profile 级编辑 | Agents 卡片：启用开关 → 落到 profile `agents` map 与工具面 denylist（T2 语义） |
| "特性配置（功能开关）" | ◐ 部分已有配置开关 | §9.2 开关目录 + 覆盖白名单 |
| "审批方式偏好" | ◐ 已有 permission_mode 通道与会话控件 | §9.3 D16/D17：默认值 + 收窄约束 + 来源标注 |
| "配置域化（覆盖一部分现有配置）" | ◐ 基建齐全、扩展点 dormant | §8 D12-D15：会话级 overlay + 白名单 + 溯源 UI |
| "创建 / 复制 / 从会话固化 profile"（第四轮补全） | ❌ 原方案仅"模板新建"一行、无固化入口 | §23 G1：三入口一语义（D24 差分固化）+ §10.2 列表操作 |
| "分享 / 跨项目复用 profile"（第四轮补全） | ❌ 无设计 | §23 G5：export/import（D28 安全纪律）+ 项目级随仓库分发 |

## 11. 实施批次（第二轮新增，接第一部分 Batch 0-6）

### Batch 7 — 配置覆盖接线（P0/P1）

1. **语义先行**：定义 `runtime.overrides` 的键路径语法、合并语义（复用 `MergeConfigYAML`）、特殊值（`{}`/null/slice）行为，写进 spec 文档与 `profile validate` 校验器（白名单，§8.3 D14）。
2. **会话级 overlay**：`chatProfileState` 增加 `ConfigOverlay`（合并视图 + 覆盖键清单 + origins）；`chat_setup.go` 装配点消费；server 侧 `/api/agent/chat` 同语义（复用 `resolveChatProfileState` 路径）。
3. **origins 标记**：覆盖键 origins 标记为 `profile`（复用现有结构，不新增格式）。
4. **测试**：覆盖生效（改 model/skills_runtime/chat 偏好）、白名单拒绝、null 屏蔽、slice 整表替换、未写键回落、无 profile 时逐字节零变化。
5. **dormant 字段处置**：`mcp.merge_strategy` 要么定义语义接线、要么从 spec 移除并在 validate 报"未知字段"（避免"配了不生效"陷阱）。

### Batch 8 — 前端 Profiles 页与 API（P1）

1. 后端：`/api/runtime/profiles`（list/get/put/validate/preview/default/apply，§10.5；原 `activate` 已按 D26 拆分），解析复用 `internal/profile`，写回走原子写 + admin token；第四轮增补的 create/生命周期端点（§23 G1-G2）并入本批次实现。
2. 前端：`profiles` mode（挂 `mode-registry.ts`）+ 编辑器 9 卡片（§10.2/10.3），复用 `ConfigDomainDialog`、`settings-*`、`impact-panel`、`validation-panel`、`preview-section`、`runtime-config-diff`。
3. 选择器数据源对接：`/capabilities`（工具）、`/skills`（技能）、`/mcps` + `/mcps/{name}/tools`（MCP，工具级只读联动）。
4. i18n（zh/en，对齐 `runtime-config` 命名空间风格）+ vitest（编辑器纯函数单测 + 关键交互测试）。

### Batch 9 — 特性开关与审批聚合（P1/P2）

1. T1 开关：开关目录（§9.2）逐项接线到覆盖白名单，每项配"权威生效点"断言测试。
2. T2 能力裁剪：提供"禁用子代理/团队"的**模板 denylist**（`spawn_agent`/`spawn_subagents`/`spawn_team`），不新增配置开关。
3. T3 审批：profile 默认 `permission_mode`（已有）+ `bypass_permissions` 禁止校验（D16）+ UI 来源标注（D17）。
4. 待核实项（附录 C）完成后，决定 supervision 审批与 `skills_runtime.enabled` 是否纳入目录。

> 第四轮补遗 **Batch 13-14（§24）扩展本批次范围**：Profiles 页实现（Batch 8）增补创建向导（§23 G1）、生命周期操作与引用检查（§23 G2）、`default`/`apply` 双按钮（§23 G3）、导入导出（§23 G5）、未信任徽标（§23 G6）。

> **落地状态（2026-09-24）**：✅ 已完成——① **T1 开关**：`internal/profile/overrides_catalog_test.go` 双重断言（`ValidateOverrides` 通过 + `ApplyConfigOverlayYAML` 后 `*Config` 字段真实翻转：`skills_runtime.enabled`/曝光 mode+top_k/`aicli.log.enabled`/`aicli.model_cards.enabled`/`aicli.main_agent.routing.enabled`）；V11 修正 `skills_runtime.*` 为**根级**路径（旧 `aicli.skills_runtime.*` 属 dormant 假开关）。② **T2 能力裁剪**：`templates/review/profile.yaml` 增 `tools.denylist: [spawn_agent, spawn_subagents, spawn_team]`（注释说明 allowlist 裁不掉 runtime-owned 工具），断言 `TestRenderedReviewTemplateDeniesDelegationTools`（denylist 命中 + `AllowTool` 报错 + `todos` 仍可用）。③ **T3 审批**：D16 单一权威断言 `TestBuildBinding_ProfileCannotDefaultToBypassPermissions`；D17 来源标注（`profile show` 新增 `permission_mode`/`permission_mode_source`）；**修复 profile 路径默认权限模式静默丢失**（`chat_profile.go` `profileAgentPermissionMode` 复用 agentdef 权威解析，与 `--agent` 路径行为对齐）。④ V8/V9/V11 已回填；决策：supervision 审批**不纳入**覆盖目录（V9：宿主级运行时预算/灰度，非审批偏好档位），`skills_runtime.enabled` **纳入**（V11）。

## 12. 风险与兼容性（第二轮新增）

| # | 风险 | 缓解 |
|---|------|------|
| R8 | **覆盖语义误用**：用户把 `{}` 当清空、把 slice 当追加，产生"改了没生效/删不掉" | UI 三处显式提示（§8.4 三条语义）+ 校验器拦截歧义写法 + `profile show` 展示最终值 |
| R9 | **dormant 字段陷阱**：`runtime.overrides` / `mcp.merge_strategy` 已在 spec 中，用户以为能配 | Batch 7 先接线或移除；接线前 UI 不得展示这两个字段的编辑入口 |
| R10 | **双权威漂移**：profile 里做 MCP 工具级启停 / 新增 `subagents.enabled` 等第二套开关 | §9.4 反模式清单 + 单一入口原则（UI 跳转现有面板）+ 代码评审检查项 |
| R11 | **profile 放宽安全基线**：默认 `bypass_permissions`、覆盖密钥/端点类键 | D14 白名单校验 + D16 禁止 bypass + 项目级 profile 受 foldertrust 门禁约束（第一部分已有） |
| R12 | **UI 复杂度**（9 卡片）导致采用率低 | 渐进披露：基础视图（基本/工具面/Skills/MCP）默认展开；高级视图（Prompts/Agents/Overrides/偏好）折叠；模板向导生成可用 profile（对齐第一部分 `profile create`） |
| R13 | **前后端估算口径不一致**（工具面 token 估算两套数字） | 估算函数单点实现（`internal/profile/estimate.go`），前端只消费 API 返回值 |

## 13. 开放问题（第二轮新增，接第一部分 §7）

5. **`/api/runtime/capabilities` 内容**：是否返回内置工具全清单与定义（决定工具面卡片数据源；若否，需新增只读端点）。
6. **agents 清单端点**：`api/runtime/agents.ts` 现有端点能否直接列"可被 profile 启用的 agent"（待核实）。
7. **`subagents` / `teams` 配置节内部字段**：是否已有 enabled 语义（决定 T2 是否纯工具面建模）。→ **已定案（V8）**：两者仅有 `routing`，**无 `enabled`** → T2 纯工具面（denylist）建模。
8. **supervision 审批**：是否已有可配置档位（决定是否纳入审批偏好目录；无则不做）。→ **已定案（V9）**：有可配置项但属宿主级 supervision 运行时预算/灰度（`supervision.Config`），**不纳入** profile 覆盖目录。
9. **白名单边界**：`skills_runtime.enabled`、`providers.items.*` 非密钥字段是否允许覆盖。→ **部分定案（V11）**：`skills_runtime.enabled` 纳入（**根级**路径）；`providers.items.*` 非密钥字段仍待评审。
10. **与 `--config` 的交互**：显式 `--config`（单文件短路语义）与 profile overrides 同时出现时的行为定义。
11. **`mcp.merge_strategy`**：定义语义（merge/replace）或删除字段。
12. **`WorkspaceSpec`**：spec 中已定义（`profile/spec.go:51-55`）但消费状态待核实，决定是否纳入本期。

## 附录 C — 第二轮核实清单（待回填）

| 核实项 | 结论 | 证据 |
|--------|------|------|
| `/api/runtime/capabilities` 返回内容（是否含内置工具清单/定义） | ✅ 已核实（Batch 8 前置，2026-09-24）：**不含内置工具全清单**——`descriptors = skillRegistry.CapabilityDescriptors()`（skill 能力描述）+ `agentCapabilityDescriptor()`（agent 单条），零内置工具条目；会话级"生效工具"另有 `GET /sessions/{id}/runtime/tools`（tool surface 快照）→ 工具面卡片数据源改由 profiles get 视图提供（`tools.allow/deny/effective`） | `handler.go:1568-1586`、`internal/skill/capability.go:87-100`、`session_runtime_handlers.go:484-536` |
| agents 清单端点（`api/runtime/agents.ts` 对应后端路由与语义） | ✅ 已核实（Batch 8 前置，2026-09-24）：**不是 profile 级 agent 清单**——`api/runtime/agents.ts` 对应 `GET /api/runtime/agent-control/agents`（运行中子代理身份图，`agent_control_agent_handlers.go:23-69`），不能列 profile/agentdef 定义 → Agents 卡片数据源 = profiles get 视图的 `agents` 字段（profile spec `agents` map；agentdef 目录展示如需，复用 `internal/agentdef.Discover` 另行增补） | `frontend/src/api/runtime/agents.ts:1-35`、`handler.go:963`、`internal/agentdef` |
| `AICLISubagentsConfig` / `AICLITeamsConfig` 内部字段（是否已有 enabled） | ✅ 已核实（Batch 9，2026-09-24）：**无 `enabled` 语义**——两者仅有 `routing` 子节（团队在 routing 缺失时回落到 subagents.routing）→ T2 只能纯工具面（denylist）建模，**不新增配置开关** | `config.go:650-653`、`config.go:655-659` |
| supervision 审批可配置性（档位/超时/策略） | ✅ 已核实（Batch 9，2026-09-24）：**已有可配置项，但属宿主级 supervision 运行时预算/灰度**（唤醒配额 `wake_max_*`、`turn_end_check`、`progress_check_interval`、`approval_terminal_guard`、`message_semantics_v2`），不是"审批偏好档位" → **不纳入 profile 覆盖目录**（纳入会造出与审批决策无关的假开关） | `agentconfig/config.go:43`、`internal/supervision/config.go:18-121` |
| `WorkspaceSpec` 消费状态 | ✅ 已核实（Batch 7 前置，2026-09-24）：**已消费**——`resolver.go:36` 经 `LoadWorkspace`（`loader.go:35`）把 `spec.go:73-78` 作为 `workspace.file` 层参与 provider/model/tool-policy 解析；按 Q12 本期后置 | `profile/spec.go:73-78`、`profile/resolver.go:36`、`profile/loader.go:35` |
| `skills_runtime.enabled=false` 时 profile 级 skill 目录的行为 | ✅ 已核实（Batch 9，2026-09-24）：**一并关闭**——`initSkillFunctionsWithManager` 在 `enabled=false`（或 `SkillsRuntime==nil`）时于解析 skill 目录**之前**早退，profile 级 `ResolvedSkillDirs` 不再被扫描 → 开关真实、单一权威生效点；键路径为**配置根** `skills_runtime.*`（`config.go:42`） | `cmd/aicli/commands/skills_integration.go:944-951`、`agentconfig/config.go:42` |
| `mcp.merge_strategy` 语义（定义或删除） | ✅ 已核实（Batch 7 前置，2026-09-24）：零生产消费点（全仓仅 `spec_test.go` 解析测试）→ 按 Q11 **从 spec 移除**，validate 报未知字段（不留 dormant） | `profile/spec.go:39` |
| server 侧 `/api/agent/chat` 的 `runtime.overrides` 生效点（V27） | ✅ 已核实（Batch 7 server 半程，2026-09-24）：消费面 = `Handler.aicliConfig` 快照 → `runtimeSkillsConfig()`/`subagentRoutingConfig()`/`teamRoutingConfig()`/`mainAgentRoutingConfig()`/`defaultReasoningEffort()`；**请求级已接线**（skills catalog/exposure 注入），routing 三件套为**会话级余项**（登记 Batch 12 前置） | `internal/api/skills/handler.go:164/450/460/468/476/496/1962/2419/8225`、`profile_support.go`、`profile_config_overlay_test.go` |

---

# 第三部分：热切换设计（Runtime Profile Hot-switch，2026-09-24 第三轮）

> 用户诉求（原文要点）：**在 frontend 与 aicli TUI 中增加 `/profile` 主命令与二级子命令，让用户在运行时切换 profile；切换后请求中的 `tools`、`system prompt` 随之变化；由此导致的请求缓存失效是可接受的。**
>
> 本部分回答：**怎么切**（命令面）、**切了之后什么必须失效**（失效矩阵）、**怎么保证不是假开关**（不变量与断言）。

## 14. 命题拆解与判定

### 14.1 结论先行

热切换**不需要新机制**：仓库已有一套完整的"运行时切换 + 缓存失效"基建，本方案只是把 profile 接上去。

| 需要的能力 | 现有先例（同语义、已验证） | 证据 |
|---|---|---|
| 运行时切换且"下一 turn 生效" | `/model`、`/provider` 会话内切换 | `chat_model_switch.go:171`（`applyRuntimeModelSwitch`）、`:238`（`applyRuntimeProviderSwitch`），注释明确"从下一个 turn 生效" |
| 切换后清跨轮工具面缓存 | 两处显式调用 | `chat_model_switch.go:216`、`:268`（`resetStableSharedToolSurface`） |
| 跨会话/持久化层工具面失效 | MCP 目录变化即自动失效 | `internal/chat/hub.go:128`（`SessionHub.InvalidateStableToolSurfaces`）、`session_runtime_store.go:555/2400`（内存/SQLite 实现） |
| 在途 turn 不被切换撕裂 | turn 冻结面保留前缀 | `session_runtime_store.go:556`："无在途 turn 的会话同时清空 turn 冻结面；**运行中的 turn 保留前缀，下一轮重建**" |
| 会话级持久化与层感知写回 | `/routing ... --to session\|workspace\|config` | `chat_slash_command_catalog.go:159`；`chat_routing_layers.go` |
| 前端注入会话命令 | `POST /api/runtime/sessions/{id}/runtime/commands` 已有 7 种命令类型 | `session_runtime_handlers.go:830/889`（`submit_prompt`/`continue`/`approve_tool`/`answer_question`/`interrupt`/`rewind_to`/`compact`） |
| 前端命令面（含二级候选） | composer 内置命令 + popupSelect | `frontend/src/lib/composer-builtin-commands.ts:27-66`（`/model`、`/skill` 即"宿主目录驱动的二级候选"） |
| 会话内身份持久化 | profile 键已存在 | `sessionmeta.go:20-23`（`profile_ref`/`profile_name`/`profile_agent`/`profile_root`） |

### 14.2 现状缺口（这才是要补的）

| 缺口 | 证据 | 影响 |
|---|---|---|
| **profile 只在启动期解析一次**，运行期无任何重解析入口 | `resolveChatProfileState` 仅 4 个调用点，全在启动路径：`chat.go:581`、`agent_stdio.go:685`、`exec_resume.go:98`、`exec_run.go:261`；命令目录无 `/profile`（grep `handleProfileCommand`/`"/profile"` 零命中） | 运行期无法换 profile |
| **system prompt 被会话冻结**，换 profile 不会自动改 prompt | `sessionmeta.SystemPromptFrozen`（`sessionmeta.go:63-69`）："Provider prompt caching requires the instruction head (messages[0]) to stay byte-identical for the whole session… captured once and reused"；读写点 `chat_actor_host.go:2179`/`:2189`/`:2269` | 不显式清锚点 = **假开关**（改了 profile 但 prompt 不变） |
| **profile 默认只在启动期应用到 CLI options** | `applyProfileDefaultsToChatOptions`（`chat_profile.go:169-186`）带 `ProviderChanged`/`ModelChanged`/`PermissionModeChanged` 守卫；测试 `chat_profile_test.go:170-177` 断言显式 `bypass_permissions` 不被 profile 默认覆盖 | 运行期需复用同一守卫语义，避免切换降级用户显式选择 |
| 无切换报告（用户看不到"变了什么"） | 无对应结构 | 用户无法确认 tools/prompt 真的换了 |

## 15. 失效矩阵（Hot-switch Invalidation Matrix）

### 15.1 请求构成与缓存对象

一次请求的"可变前缀"由四层组成，每层各有独立的缓存与失效点：

```
messages[0] system prompt ── 会话冻结锚点（SystemPromptFrozen）  ← ①
tools[]                   ── 稳定工具面（跨轮）+ turn 冻结面      ← ②③
skills 暴露候选           ── 技能目录/暴露链路                    ← ④
provider/model 元数据     ── session/provider 状态                ← ⑤
```

provider 侧 prompt cache（Anthropic `cache_control`，`internal/types/anthropic/types.go:133-134/226-229`）以 **messages[0] 字节一致**为前提——这正是 `SystemPromptFrozen` 存在的理由（`sessionmeta.go:63-69`）。**切换 profile 必然改变该前缀，因此必然导致一次 provider 缓存未命中；用户已明确接受此成本**（见 §16）。

### 15.2 失效矩阵表

| # | 对象 | 存储位置 | 现有失效机制 | 切换时动作 | 证据 |
|---|---|---|---|---|---|
| ① | 系统提示冻结锚点 | `sessionmeta.SystemPromptFrozen`（会话 context） | 无（设计上"一次冻结、永不改"） | **显式删除锚点**，下一 turn 重组新 prompt | `sessionmeta.go:63-69`、`chat_actor_host.go:2179-2193`、`:2269-2276` |
| ② | 稳定工具面（跨轮） | `session.stableSharedToolSelection` + 持久化 `stableToolSurfaceJSON` | `resetStableSharedToolSurface`（进程内单会话）、`SessionActor.InvalidateStableToolSurface`、`SessionHub.InvalidateStableToolSurfaces` | 本会话立即失效；必要时走 hub/持久化层 | `chat_tool_surface_stability.go:9-47`、`internal/chat/hub.go:128-141`、`session_runtime_store.go:555-557`/`:2400-2412` |
| ③ | turn 冻结面 | `RuntimeState.FrozenTurnTools` / `FrozenTurnToolsSet` | 随 ② 一起清（**仅无在途 turn 时**） | 有在途 turn 时**保留**，切到下轮生效 | `session_runtime_store.go:556`、`turn_tool_surface_snapshot_test.go:340-348` |
| ④ | skills 暴露候选 | skills runtime / 暴露链路 | 目录变化即重建 | profile `skill_dirs` 变化时重建（复用 §8 设计） | §8.2、`skills-exposure-implementation-and-test-plan.md` |
| ⑤ | provider/model/permission 元数据 | `session.Provider/Model/...` + sessionmeta | `/model`、`/provider` 的既有落地路径 | 仅在用户未显式覆盖时应用 profile 默认 | `chat_model_switch.go:212-226`、`chat_profile.go:173-181` |
| ⑥ | context token 计数 | `session.ContextWindowTokenCount` | `/model`/`/provider` 均置 0 | 置 0 | `chat_model_switch.go:215`、`:267` |
| ⑦ | 运行时配置缓存 | `chatRuntimeConfigCache`（键=路径+mtime / 层指纹） | 自动（键不匹配即重载） | **无需动作**；路径或指纹变化自然未命中 | `chat_runtime_config_cache.go:22-31`、`:143-154` |
| ⑧ | 环境上下文块 | `sessionmeta.EnvironmentContextBlock` | 会话冻结（与 profile 无关） | **保留**（不得因切换而重写） | `sessionmeta.go:51-54` |
| ⑨ | MCP 目录 | MCP manager + 生命周期观察者 | `mcp.*` 事件 → 异步失效全部会话 | profile 切 `mcp.yaml` 时触发重连；失效自动发生 | `chat_mcp_surface_invalidation.go:13-36` |
| ⑩ | sessionmeta profile 身份 | `profile_ref`/`profile_name`/`profile_agent`/`profile_root` | 已有键 + `syncRuntimeSessionFromChat` | 写入新值并 sync | `sessionmeta.go:20-23`、`chat_session.go:863` |

### 15.3 设计决策（D18-D23）

**D18 — 切换语义 = "下一 turn 边界生效"，turn 内绝不打断。**
沿用 `/model`/`/provider` 的既有语义（`chat_model_switch.go:219` 注释"从下一个 turn 生效"）。在途 turn 保留其冻结前缀（③），使请求前缀在同一 turn 内保持自洽——这是防"半新半旧"的第一道闸。

**D19 — 切换即失效，且必须"一次到位"。**
四项失效动作（①删除锚点、②重置工具面、③按需清 turn 面、⑥清零 token 计数）**在同一个切换事务内全部执行**，不允许出现"prompt 已换但工具面还是旧的"这类中间态。切换实现放在单一函数 `applyRuntimeProfileSwitch`，失效动作不出函数。

**D20 — 复用三套既有失效 API，不新增机制。**
① 进程内单会话：`resetStableSharedToolSurface`（`chat_tool_surface_stability.go:41`）；
② 跨会话/持久化：`SessionHub.InvalidateStableToolSurfaces`（`hub.go:128`）→ `SessionActor.InvalidateStableToolSurface` → `RuntimeStore.InvalidateStableToolSurfaces`（内存 `:555` / SQLite `:2400`）。
若目标 actor 句柄可得，优先单会话路径（精确、代价低）；句柄不可得时退化为 hub 全量失效（正确性优先）。

**D21 — 双入口、单执行核心。**
TUI `/profile use <name>` 与 Web `POST /runtime/commands {type:"set_profile", profile:"<name>"}` **都落到同一个 `applyRuntimeProfileSwitch`**（`cmd/aicli/commands/chat_profile_switch.go`）。命令解析层只做参数解析与呈现，不做第二套切换逻辑——与 §8 的"一个键一个权威层"同一条纪律。

**D22 — 持久化默认"仅当前会话"，跨会话写回必须显式。**
`/profile use X` 默认只改当前会话（含 sessionmeta 身份，⑩），resume 时沿用；写回默认 profile 需 `/profile save [--to session|workspace|config] [--yes]`，层语义与 `/routing save` 完全对齐（`chat_slash_command_catalog.go:159`，config 层需 `--yes` 二次确认）。

**D23 — 切换必须返回结构化报告（Switch Report）。**
结构固定：`{from, to, changed: {tools_added/tools_removed, skills_added/removed, mcp_added/removed, prompt_changed: bool, provider_changed, model_changed, permission_mode_changed}, effective_at: "next_turn", cache_notice: "..."}`。TUI 与前端渲染**同一个投影**，避免两套文案漂移。

**D30 — 切换不隐式改写 provider/model/permission：只报告，不落地。**
三项各有既有的权威切换路径（`/provider`、`/model`、权限覆盖层 `chat_permissions_overlay.go`），且都携带各自的缓存/协议/工具面语义（`chat_model_switch.go:212-222`、`:257-272`）。若在 profile 切换里再改一次，等于新增第二套切换路径（违反 D20/D21 的"不新增机制、单执行核心"），并可能在 turn 中途改变协议面。落地口径：切换报告 `changed.provider_changed/model_changed/permission_mode_changed` **恒为 false**，差异以 `warnings` 明示"需显式 `/provider`、`/model` 应用"；会话当前选择永不被 profile 默认值覆盖（A5 断言）。启动路径的"仅未显式覆盖时应用默认"（`chat_profile.go:169-186`）保持不变——那是会话尚未开始时的绑定，不是热切换。

## 16. 请求缓存影响（用户已接受，但要"付得明白"）

### 16.1 影响是什么

| 缓存 | 触发条件 | 切换后的代价 |
|---|---|---|
| Provider prompt cache（Anthropic `cache_control`） | messages[0] 前缀字节一致 | 切换后**首次请求必然未命中**，按全价计费一次输入 token；后续请求按新前缀重新缓存 |
| 本地跨轮工具面缓存（②） | 会话内工具面稳定 | 失效后重建一次（进程内，无计费成本） |
| 运行时配置缓存（⑦） | 路径+mtime 命中 | 路径不变则无影响；换 `runtime.yaml` 时重载一次（本地 IO） |
| 本地上下文计数（⑥） | 会话累计 | 清零后重新累计（仅影响进度条/阈值判断） |

### 16.2 设计约束（把"可接受"变成"可控"）

1. **不做切换节流，但做切换告知**：切换报告（D23）必须包含 `cache_notice`，TUI 与前端明示"下一轮起生效；请求缓存将重建（首轮输入计费增加）"。
2. **同一 turn 内禁止二次切换**：在途 turn 落地前再次收到切换请求 → 覆盖 pending 值（合并为最后一次），不排队多次失效（避免连环 miss）。
3. **切换即失效必须完整**：若只删 prompt 锚点而忘了工具面，会出现"prompt 新 + tools 旧"的请求——这既不是旧 profile 也不是新 profile，比缓存 miss 更糟。这是 §18 断言 A2/A3 的存在理由。
4. **不做"缓存保真"式折衷**：不提供"保留旧 prompt 直到 turn 结束再换"之类的半吊子选项——语义简单性优先于缓存收益（用户已明确接受成本）。

## 17. 命令面设计

### 17.1 TUI：`/profile` 主命令 + 二级子命令

命令目录（`chat_slash_command_catalog.go`）新增一条 spec，`Group: session`，风格对齐 `/routing` 与 `/supervision`：

```
/profile [status]                                   # 当前 profile：引用、来源（builtin|user|project）、生效摘要
/profile list                                       # 可用 profile 列表（内置/用户/项目分层 + 当前标记）
/profile show <name>                                # 只读预览：该 profile 将带来什么（不切换）
/profile diff [<name>]                              # 与当前对比：tools/skills/mcp/prompt 变化摘要
/profile use <name>                                 # 热切换（下一 turn 生效；含 cache_notice）
/profile pick                                       # 交互选择器（对齐 /model 的 popup picker，Interactive: true）
/profile reload                                     # 重新解析当前 profile（磁盘编辑 profile.yaml 后）
/profile off                                        # 回到无 profile 基线（等价启动时不带 --profile）
/profile save [--to session|workspace|config] [--yes]   # 持久化默认 profile（层语义同 /routing save）
/profile create <name> [--template coding|review|minimal|docs] [--to user|project]   # 模板新建（§23 G4）
/profile duplicate <ref> <name> [--to user|project]                                  # 复制（内置只读，复制即另存为）
/profile save-as <name> [--to user|project]                                          # 从当前会话固化（§23 G1/D24）
/profile edit [<ref>]                                                                # 打印 profile.yaml 路径并尝试 $EDITOR
/profile rename <ref> <new-name>                                                     # 重命名（同层）
/profile move <ref> --to user|project                                                # 层级移动
/profile delete <ref> [--force]                                                      # 删除（引用检查 + 确认）
/profile export [<ref>] [--output <file>]                                            # 导出（§23 G5）
/profile help
```

> `create` … `export` 八个生命周期子命令为**第四轮 G4 增补**（D27：TUI 内全生命周期可达，复杂编辑仍在前端）；行为细则与失败模式见 §23 G4。

命令 spec 草案（对齐 `chatSlashCommandSpec`，`chat_slash_command_catalog.go:19-31`）：

```go
{
    Name:        "/profile",
    Usage:       "/profile [status|list|show <name>|diff [<name>]|use <name>|pick|reload|off|save [--to session|workspace|config] [--yes]|create <name> [--template <t>] [--to user|project]|duplicate <ref> <name>|save-as <name>|edit [<ref>]|rename <ref> <new-name>|move <ref> --to user|project|delete <ref> [--force]|export [<ref>] [--output <file>]]",
    Summary:     "查看或运行时切换 profile（tools / skills / MCP / prompt 场景预设）",
    Group:       string(chatSlashCommandGroupSession),
    AcceptsArgs: true,
    Interactive: true,
    Args: []chatSlashCommandArgSpec{
        {Token: "status",  Summary: "当前 profile 与生效来源"},
        {Token: "list",    Summary: "列出可用 profile（内置/用户/项目）"},
        {Token: "show",    Summary: "只读预览指定 profile"},
        {Token: "diff",    Summary: "与当前 profile 的差异摘要"},
        {Token: "use",     Summary: "热切换到指定 profile（下一轮生效）"},
        {Token: "pick",    Summary: "交互选择 profile"},
        {Token: "reload",  Summary: "重新从磁盘解析当前 profile"},
        {Token: "off",     Summary: "回到无 profile 基线"},
        {Token: "save",    Summary: "持久化默认 profile 到指定层"},
        {Token: "create",  Summary: "模板新建 profile"},
        {Token: "duplicate", Summary: "复制现有 profile（内置只读，复制即另存为）"},
        {Token: "save-as", Summary: "把当前会话与基线的差分固化为新 profile（D24）"},
        {Token: "edit",    Summary: "打印 profile.yaml 路径并尝试 $EDITOR"},
        {Token: "rename",  Summary: "重命名 profile（同层）"},
        {Token: "move",    Summary: "层级移动 profile（user↔project）"},
        {Token: "delete",  Summary: "删除 profile（引用检查 + 确认）"},
        {Token: "export",  Summary: "导出 profile（目录/zip）"},
        {Token: "--template", Summary: "模板名（coding|review|minimal|docs）"},
        {Token: "--output", Summary: "导出目标路径"},
        {Token: "--force", Summary: "删除时处理 default 引用（同时清空 default）"},
        {Token: "--to",    Summary: "层选择（save: session|workspace|config；create/move: user|project）"},
        {Token: "--yes",   Summary: "config 层写入的二次确认"},
    },
},
```

**免费获得的配套设施**（无需额外开发）：二级补全（`chat_slash_completion.go`/`chat_slash_argument_completion.go` 读取同一 catalog）、`/help` 条目（`chat_slash_help.go`）、picker 交互（复用 `/model` 的 `runtimeModelPickerState` 渲染/翻页/过滤框架，`chat_model_switch.go:297-393`）。

### 17.2 命令面行为细则

| 子命令 | 行为 | 失败模式（一律"解析不了就报错"，不猜） |
|---|---|---|
| `use <name>` | 解析→切换→打印 Switch Report | 未知 profile：报错且**状态零改动**（§18 A6） |
| `use`（无参） | 报错并提示 `pick` | 不隐式选第一个 |
| `diff` | 只读：解析目标 profile 并与当前投影对比 | 目标不存在：报错 |
| `show` | 只读预览，**绝不**产生任何失效 | — |
| `reload` | 重解析当前 `profile_ref`；文件已删/损坏 → 报错并**保留旧状态** | 不静默降级为"无 profile" |
| `off` | 清空 profile 绑定 + 完整失效（① ② ③ ⑥）+ 身份键清除 | 无 profile 时：幂等提示，不报错 |
| `save` | 写回默认 profile 到目标层；config 层需 `--yes` | 与 `/routing save` 同一守卫 |

> `create` / `duplicate` / `save-as` / `edit` / `rename` / `move` / `delete` / `export` 的行为细则见 §23 G4（同一"解析不了就报错"纪律，本节不重复；例如 `save-as` 无差分时明确提示且不生成空 profile）。

### 17.3 Web / 前端命令面

**后端**：`SubmitSessionRuntimeCommand` 的 dispatch switch（`session_runtime_handlers.go:889`）新增分支：

```go
case "set_profile", "profile":
    // req.Profile：目标引用；空字符串 = 回到基线（等价 /profile off）
    // 复用 §17.1 同一 applyRuntimeProfileSwitch（= §23 G3 的 POST /profiles/{ref}/apply）；返回 Switch Report
```

请求结构 `sessionRuntimeCommandRequest`（`session_runtime_handlers.go:40-50`）新增字段 `Profile string \`json:"profile,omitempty"\``，与既有 `RequestID`/`TurnID` 风格一致。**不新增路由**——复用 `POST /sessions/{id}/runtime/commands`（`handler.go:912`），前端零新协议。

**前端 composer**：`buildComposerBuiltinCommands`（`composer-builtin-commands.ts:27-66`）新增：

```ts
{
  name: "profile",
  kind: "popupSelect",           // 与 /model、/skill 同型：宿主目录驱动的二级候选
  descriptionKey: "composer.builtin.profile.description",
  argumentHintKey: "composer.builtin.profile.argumentHint",
  options: profileOptions.length > 0 ? profileOptions : undefined,
}
```

约束（沿用该文件既有纪律，`composer-builtin-commands.ts:3-12`）：
- **只注册"当前确实可执行"的命令**：`profileOptions` 为空（后端未暴露目录 API）时，命令仍可提交但只走"无候选"路径；若后端完全没有 `set_profile` 支持，则该命令不注册；
- 参数解析"解析不了就报错"：`/profile use <name>` 的 name 为空 → 失败，不降级为 pick。

**前端接线点**（三处，均有先例）：
1. 命令执行器：`use-composer-command-executor.ts` 增加 `profile` 分支 → 调 `setSessionProfile()`（新函数，放 `api/runtime/sessions.ts`，风格对齐 `approveSessionTool`/`interruptSessionTurn`）；
2. 候选目录：新增 `api/runtime/profiles.ts`（`GET /api/runtime/profiles`），经 `use-composer-command-surface.ts` 注入 `profileOptions`（与 `modelOptions`/`skillOptions` 同一注入方式）；
3. 会话详情展示：当前 profile 徽标（数据源 `sessionmeta.profile_name`，会话详情已有 routing 区块可作 UI 先例 `session-detail-routing/`）。

### 17.4 与既有命令的边界

| 相邻命令 | 分工 | 不做什么 |
|---|---|---|
| `/model`、`/provider` | 只换模型/provider | **不因 profile 切换而被 profile 默认覆盖**（显式选择优先，§18 A5） |
| `/routing` | 难度路由（provider/model/reasoning） | profile 切换不重置 routing 覆盖；`/profile diff` 只报告不修改 |
| `/permission` | 会话权限模式 | profile 的 `permission_mode` 仅作为**默认值**（用户未显式设置时），不降级显式值（`chat_profile_test.go:170-177`） |
| `/agents`、`/supervision` | 子代理/监督 | 切换**不追溯**已存在子代理；新子代理继承新 profile（与 §8 D8 一致） |

## 18. 后端执行核心与不变量

### 18.1 `applyRuntimeProfileSwitch`（新文件 `cmd/aicli/commands/chat_profile_switch.go`）

```
applyRuntimeProfileSwitch(session *ChatSession, ref string) (*ProfileSwitchReport, error)

1. 解析阶段（只读，失败即返回，零状态改动）
   - registry := profilesys.NewRegistryFromProfilesConfig(cfg.Profiles)     // 同 chat_profile.go:73-76
   - resolved, err := profilesys.ResolveRef(registry, ref, ResolveOptions{...}) // 同 chat_profile.go:77-82
   - inputs, err := runtimeprofileinput.BuildResolvedAgentInputs(...)        // 同 chat_profile.go:87-90
   - 空 ref（off）跳过解析，resolved=nil

2. 应用阶段（会话级）
   - session.ProfileState = 新 chatProfileState{Reference, Resolved, PromptText, ToolPolicy, ...}
   - provider/model/permission 默认：复用 applyProfileDefaultsToChatOptions 的守卫语义
     （仅在 ProviderChanged/ModelChanged/PermissionModeChanged 为假时应用）        // chat_profile.go:173-181
   - 子代理/团队：以 tools.denylist 表达（§9 T2），不新增开关

3. 失效阶段（D19 一次到位；四项全部执行）
   - ① 删除 system prompt 锚点：sessionmeta.Delete(ctx, sessionmeta.SystemPromptFrozen)
   - ② resetStableSharedToolSurface(session)                              // chat_tool_surface_stability.go:41
      + actor/持久化层：SessionActor.InvalidateStableToolSurface（句柄可得时）
        否则 SessionHub.InvalidateStableToolSurfaces(ctx)                   // hub.go:128
   - ③ turn 冻结面：由 ② 的存储层实现按"在途保留、空闲清空"处理            // session_runtime_store.go:556
   - ⑥ session.ContextWindowTokenCount = 0

4. 身份与持久化阶段
   - sessionmeta.Set(ctx, ProfileRef/ProfileName/ProfileAgent/ProfileRoot, ...) // sessionmeta.go:20-23
   - syncRuntimeSessionFromChat(session)                                     // chat_session.go:863

5. 报告阶段
   - 组装 ProfileSwitchReport（D23），diff 计算基于旧/新 profileState 投影
```

**落地收敛（Batch 10 实施，2026-09-24）**：阶段 2 的 provider/model/permission 默认应用收敛为"只报告不落地"（见 D30）；阶段 3 的 turn 冻结面**不做显式清理**——存储层 `session_runtime_store.go:555-590` 在 `CurrentTurnID != ""` 时天然保留 `FrozenTurnTools`，显式清理反而会破坏在途前缀。

### 18.2 turn 边界落地（在途 turn 的处理）

- **空闲**：命令路径直接执行 1-5，返回 `effective_at: "next_turn"`。
- **在途 turn**：应用与失效**仍立即执行**（会话状态是下一轮的事），但 ② 的存储层实现天然保护在途前缀（`session_runtime_store.go:556`）；① 的锚点删除**只影响下一次 compose**，在途 turn 的 head 已在 turn 开始时冻结。
  - **核实项 2 结论（Batch 10 实施，2026-09-24）：采用"立即删除"，不引入 `pending_profile_switch`。** 依据：① 锚点（`sessionmeta.SystemPromptFrozen`）只在 compose 时被读取，而 compose 的调用点都在 run 起点——`buildLocalChatAgent`（actor 构建，`chat_actor_host.go:1848`）与 `localChatPrepareRunHook`（每 run 一次的 prepare 钩子，`:2119-2145`）；② 在途 turn 的 head 已 materialize 进活体 agent 的 `cfg.SystemPrompt`，删锚点不会回写它；③ 工具面由存储层在途保留（`session_runtime_store.go:555-590`，`turn_tool_surface_snapshot_test.go:340-348` 已锁定）；④ `pending_profile_switch` 需要新增 turn 终止回调，属新增机制（违反 D20）。
  - **A3 覆盖分层**：本层断言切换不触碰在途消息窗口与路由（`TestProfileSwitch_InFlightTurnPrefixStable`）；`FrozenTurnTools` 的在途保留由存储层测试覆盖；端到端由 Batch 11a 的 TUI 剧本覆盖。

### 18.3 不变量与断言测试（禁止假开关）

| # | 不变量 | 断言测试（建议名） |
|---|---|---|
| A1 | 切换后请求 `tools[]` 与新 profile 声明一致（被排除工具**不出现在请求中**，而非仅执行拦截） | `TestProfileSwitch_ToolSurfaceMatchesDeclaredPolicy` |
| A2 | 切换后**首个 turn** 的 system prompt 字节 = 新 profile 组合结果（锚点已替换，而非复用旧值） | `TestProfileSwitch_SystemPromptRebuiltFromNewProfile` |
| A3 | 在途 turn 的冻结前缀不变（切前切后同一 turn 的 tools/prompt 一致） | `TestProfileSwitch_InFlightTurnPrefixStable` |
| A4 | `/profile use X` 后 resume，`profile_ref` 仍为 X | `TestProfileSwitch_IdentityPersistsAcrossResume` |
| A5 | profile 未声明 provider/model 时，当前 provider/model 不变；用户显式 `/model` 选择不被 profile 默认覆盖 | `TestProfileSwitch_ExplicitSelectionWins` |
| A6 | 切换失败（未知 profile / 解析错误）时，prompt 锚点、工具面、sessionmeta **全部不变**（原子性） | `TestProfileSwitch_FailureLeavesStateUntouched` |
| A7 | `/profile off` 后 prompt/tools 回到无 profile 基线且锚点重建 | `TestProfileCommandOffRestoresBaseline`（命令层；失效核心复用 Batch 10） |
| A8 | 同一 turn 内连续两次切换只产生一次失效（合并为最后一次） | `TestProfileSwitch_RapidSwitchCoalesces` |

**Batch 10 落地状态（2026-09-24）**：A1-A6 已实现并全绿——`cmd/aicli/commands/chat_profile_switch_test.go`（`TestProfileSwitch_ToolSurfaceMatchesDeclaredPolicy` / `_SystemPromptRebuiltFromNewProfile`（含 A4 的持久化半程）/ `_InFlightTurnPrefixStable` / `_ExplicitSelectionWins` / `_FailureLeavesStateUntouched`）。
**Batch 11a 落地状态（2026-09-24）**：A4 的 resume 展示半程（`TestProfileSwitch_IdentityPersistsAcrossResume`）、A7（`TestProfileCommandOffRestoresBaseline`）、A8（`TestProfileSwitch_RapidSwitchCoalesces`）全绿——A1-A8 至此全部有断言；反证：临时停用 `chat_session.go` restore 钩子 → A4 测试立即失败（`got ref="" name=""`），恢复后通过；TUI 剧本 e2e（真实主循环 + stdin 注入 + vt.Screen）`TestTTY_LiveLoop_ProfileUseSwitchesNextTurnSurface`（use → 下一轮生效面切换 / off → 回落基线 / Switch Report 渲染）；全包回归 `go test ./cmd/aicli/commands -count=1` 全绿（139.721s）。

## 19. 实施批次（第三轮新增，接 Batch 7-9）

### Batch 10 — 后端热切换执行核心（P0，约 1.5 天）

| 动作 | 文件 |
|---|---|
| 新增 `applyRuntimeProfileSwitch` + `ProfileSwitchReport` | `cmd/aicli/commands/chat_profile_switch.go`（新） |
| 失效接线（锚点删除、工具面重置、token 清零） | 同上 + 复用 `chat_tool_surface_stability.go`/`internal/chat/hub.go` |
| sessionmeta 身份更新与 sync | 复用 `chat_session.go:863` |
| 断言 A1-A8 | `chat_profile_switch_test.go`（新） |
| 核实项：actor 句柄可得性、turn 生命周期（附录 D 1/2） | — |

**出口条件**：A1/A2/A3/A6 四条全绿；`/profile` 尚不可用（无命令面），仅通过 Go 测试驱动。

> **落地状态（2026-09-24）**：✅ 已完成——`chat_profile_switch.go`（五阶段 + `ProfileSwitchReport`）、`applyProfileStateToChatSession`（单一权威投影，启动路径复用）、`chat_profile_switch_test.go`（A1/A2/A3/A4 持久化半程/A5/A6 全绿）；出口条件达成（`go test ./cmd/aicli/commands/ -count=1` 140.0s 全绿）。附录 D 第 1/2/6 项已回填；新增 **D30**（provider/model/permission 只报告不落地）。详见实施方案变更记录（Batch 10）。

### Batch 11 — TUI `/profile` 命令面（P0，约 1 天）

| 动作 | 文件 |
|---|---|
| catalog spec 注册 | `chat_slash_command_catalog.go` |
| handler（status/list/show/diff/use/pick/reload/off/save） | `chat_profile_command.go`（新） |
| picker（复用 runtimeModelPicker 框架） | 同上 |
| 补全/帮助自动生效验证 | `chat_slash_completion_test.go` 增例 |
| Switch Report 文本渲染（与 Web 共用投影） | `chat_profile_command.go` |

**出口条件**：TUI 内 `/profile use X` → 下一轮请求 tools/prompt 确实变化（手工 + 断言 A1/A2）。

### Batch 12 — Web 命令 + 前端接线（P1，约 1.5 天）

| 动作 | 文件 |
|---|---|
| `set_profile` 命令分支 + `Profile` 字段 | `session_runtime_handlers.go:40/889` |
| 目录 API `GET /api/runtime/profiles` | `handler.go` 路由表 + 新 handler |
| `api/runtime/profiles.ts` + `setSessionProfile` | `frontend/src/api/runtime/` |
| composer 内置命令 + executor 分支 + 候选注入 | `composer-builtin-commands.ts`、`use-composer-command-executor.ts`、`use-composer-command-surface.ts` |
| 会话详情 profile 徽标 | `components/workspace/session-detail-*`（复用 routing 区块风格） |

**出口条件**：前端 `/profile` 可选择、可切换、可看到 Switch Report；切换后下一轮请求断言通过。

> 第四轮补遗 **Batch 13-14（§24）扩展本批次范围**：`/profile` 生命周期子命令（§23 G4）并入 Batch 11 命令面；`apply` 端点（§23 G3）与本节 `set_profile` 为**同一执行核心**，不重复实现；前端导入导出入口并入 Batch 12。

## 20. 风险与兼容性（第三轮新增，接 §12）

| # | 风险 | 等级 | 缓解 |
|---|---|---|---|
| R14 | provider prompt cache 失效成本（首轮全价输入） | 低（**用户已接受**） | Switch Report 明示 `cache_notice`；切换不做节流、不做半吊子保真 |
| R15 | turn 前缀撕裂（prompt 新 + tools 旧，或反过来） | 高 | D19 一次到位失效；A2/A3 断言；在途 turn 保留冻结面 |
| R16 | 子代理/团队与父会话 profile 漂移 | 中 | 不追溯已存在子代理；新子代理继承新 profile；`/profile status` 显示绑定关系 |
| R17 | 切换 `mcp.yaml` 导致重连抖动/工具迟到 | 中 | 复用 MCP 生命周期失效链（`chat_mcp_surface_invalidation.go`）；报告注明"工具将在连接完成后可用" |
| R18 | resume 后 profile 漂移（profile 文件已改/已删） | 中 | `profile_ref` 持久化 + 解析失败时警告并保留会话（不崩、不静默降级）；`/profile reload` 显式重解析 |
| R19 | profile 默认覆盖用户显式选择（provider/model/permission） | 中 | 复用 `applyProfileDefaultsToChatOptions` 守卫语义 + A5 断言 |
| R20 | 前端在旧后端上显示 `/profile`（后端无 `set_profile`） | 低 | "只注册确实可执行命令"纪律：能力探测失败则不注册/禁用并说明 |

## 21. 开放问题（第三轮新增，接 §13）

| # | 问题 | 建议 |
|---|---|---|
| 13 | 在途 turn 时切换是"立即落地+存储层保护"还是"pending 到 turn 边界"？ | 以附录 D 核实项 2 的 turn 生命周期为准；验收线是 A3 |
| 14 | `/profile use` 是否默认写入 sessionmeta 身份？ | 是（会话级持久化）；跨会话默认值必须 `save` |
| 15 | Web `set_profile` 是否需要二次确认（切到高危 profile，如含 denylist 收缩的）？ | 不需要（profile 只能收窄安全基线，§9；危险方向本就被禁止） |
| 16 | 目录 API 形态：独立 `/api/runtime/profiles` 还是并入 `/capabilities`？ | 独立端点（前端 composer 候选与 Profiles 页共用；`/capabilities` 语义已饱和） |
| 17 | 切换是否计入 usage/审计（`/usage` 面板）？ | 建议记录到会话事件（便于排查"哪一轮换了 profile"），不进入 token 统计 |
| 18 | exec/ACP headless 是否暴露运行期切换？ | 不暴露（headless 是单轮语义，profile 保持启动期解析；`agent_stdio.go:685` 现状即正确） |

## 附录 D — 第三轮核实清单（待回填）

| # | 核实项 | 为什么影响设计 | 核实方式 |
|---|---|---|---|
| 1 | `SessionActor.InvalidateStableToolSurface` 能否从命令路径直接取得句柄（vs 必须走 `SessionHub` 全量失效） | 决定 D20 的精确失效路径与代价 | 读 `internal/chat/hub.go` actor 注册表 API + `chat_actor_host.go` 持有关系 |
| 2 | `FrozenTurnTools` 在 turn 终止时是否自动清空；actor 重建是否可能发生在 turn 中途 | 决定 §18.2 采用"立即删除锚点"还是"pending 落地" | 读 `internal/chat/session_actor*.go` turn 生命周期 + `turn_tool_surface_snapshot_test.go` |
| 3 | Web 命令路径的 actor 句柄来源（`chatWebSession()` 与 `SubmitSessionRuntimeCommand` 的关系） | 决定 `set_profile` 分支能否精确失效单会话 | 读 `session_runtime_handlers.go` 与 `chat_actor_host.go` 的 host/session 查找 |
| 4 | 前端 `use-composer-command-executor.ts` 现有分支结构（`/model` 如何调 API、popupSelect 如何回填） | 决定 `/profile` 接线的具体落点 | 读 `frontend/src/hooks/workspace/composer/use-composer-command-executor.ts` |
| 5 | `/routing save --to session` 的会话层持久化实现 | `--save` 直接复用，避免第二套写回 | 读 `chat_routing_layers.go` / `chat_routing_command.go` |
| 6 | 切换后重组 prompt 时 `<environment_context>` 块是否仍与冻结值字节一致（⑧保留但 ①重建） | 防止"重建 prompt"意外改写环境块，破坏 ⑧ 的冻结语义 | 读 `buildLocalChatSystemPrompt`（`chat_actor_host.go:2278+`）组合顺序 |
| 7 | `chatWebSession()` 单例假设在 runtime-server 多会话下的适用性 | 决定前端多会话并存的失效范围 | 读 `chat_mcp_surface_invalidation.go:40-48` 与 host 单例定义 |

**回填结论（Batch 10 + Batch 12 实施，2026-09-24；对应实施方案 §3.1 的 V13/V14/V18/V26 与 V15/V16/V19）**：

| # | 结论 | 证据 |
|---|---|---|
| 1 | **句柄可得**：`session.LocalRuntimeHost.SessionHub.Get(sessionID)` 直接返回 `*SessionActor`，命中即走精确单会话失效；未命中退化为 hub 全量失效 | `internal/chat/hub.go:77-103`、`:128-154`（hub 内部对每个 actor 调 `InvalidateStableToolSurface`，`:149`）；落地：`chat_profile_switch.go invalidateChatStableToolSurface`（报告 `tool_surface_scope`） |
| 2 | **立即删除锚点**（不引入 `pending_profile_switch`）：锚点只在 compose 时被读取，compose 调用点都在 run 起点；在途 turn 的 head 已 materialize 进活体 agent 的 `cfg.SystemPrompt`；存储层在 `CurrentTurnID != ""` 时保留 `FrozenTurnTools` | `chat_actor_host.go:1848`（actor 构建）、`:2119-2145`（每 run 一次的 prepare 钩子）；`session_runtime_store.go:555-590`；`turn_tool_surface_snapshot.go:134-172`；在途标注用 `SessionActor.RunInFlight()`（`actor.go:844`） |
| 6 | **环境块保留**：切换只删 `SystemPromptFrozen` 锚点，`sessionmeta.EnvironmentContextBlock` 不被触碰，下一次 compose 复用同一冻结值（⑧保留、①重建） | `sessionmeta.go:51-54`；`chat_profile_switch.go clearFrozenChatSystemPromptAnchor` |
| 3 | **server 路径不经 `chatWebSession()`**：`SubmitSessionRuntimeCommand` 走 `sessionManager` + `peekSessionHub()`（只读探测，不懒加载）；`set_profile` 分支先于 hub 解析处理，actor 句柄 `hub.Get(sessionID)` 精确可得；无活体 actor 时下一次 `GetOrCreate` 天然取新面（无需 hub 全量失效） | `internal/api/skills/session_runtime_handlers.go:876-894`；`agent_control_runtime_state.go:104-110`；`session_profile_switch.go:329-377` |
| 4 | **分支落点**：`switch (command.key)`；`runModel` = "解析 → API → 提示"样板，`runProfile` 同构；候选注入读能力广告 `sessionSwitch` 决定注册（R20），选中值经既有 `runCommand(key, value)` 通路回填 | `frontend/src/hooks/workspace/composer/use-composer-command-executor.ts:264-304/306+/503-511`；`use-composer-command-surface.ts:142-151/228` |
| 5 | **命令面已可用（Batch 11a）**：`/profile save --to session / workspace / config` 已注册（`chat_profile_command.go`）；写回实现细节（复用既有会话层存储、不引入第二套）随 Batch 13（E7）逐行复核 | 见实施方案变更记录（Batch 11a）① |
| 7 | **单例不适用**：`chatWebSession()` 是 aicli 进程内 web 会话单例（调用点全部在 `cmd/aicli/commands`）；runtime-server 按 sessionID 精确失效、多会话互不影响 → 不引入单例 | `cmd/aicli/commands/web_handlers.go:35-37`；`chat_mcp_surface_invalidation.go:40-48`（aicli 侧单例通道）；`session_profile_switch.go:329-377` |

---

# 第四部分：生命周期闭环审查与补遗（2026-09-24 第四轮）

> 审查问题：**方案是否覆盖"创建 → 修改 → 前端配置 → 使用/切换"的完整业务闭环？**
> 方法：以用户生命周期动作为行、以文档现有覆盖为列，逐项判定；凡判定为 ◐/❌ 的，在本部分给出补遗设计（G1-G7）。

## 22. 审查结论：生命周期 × 覆盖矩阵

| # | 生命周期动作 | 现有覆盖 | 判定 |
|---|---|---|---|
| 1 | **发现**：列出可用 profile | FR-1 `list`；§17.1 `/profile list`；§10.2 列表视图 | ✅ |
| 2 | **创建**：模板新建 | FR-2 + Batch 2（`aicli profile create --template`，CLI 侧 ✅）；前端仅 §10.2 一行"新建（模板向导）"，无细则、无 API | ◐ 前端缺口 → **已补** §23 G1（向导细则 + 创建 API） |
| 3 | **创建**：复制现有 profile | §10.2 一行"复制" | ❌ 无 API、无语义（复制到哪层？内置只读如何另存？）→ **已补** §23 G1/D24 |
| 4 | **创建**：从当前会话固化（"把现在调好的这套存成 profile"） | 无 | ❌ **最大缺口**：这是用户创建 profile 最自然的入口 → **已补** §23 G1/D24（差分固化） |
| 5 | **修改**：卡片编辑（工具/skills/mcp/prompts/agents/覆盖域/审批） | §10.2/10.3 九卡片 + §10.4 交互原则 | ✅ |
| 6 | **修改**：重命名 / 层级移动（user↔project） | 无 | ❌ → **已补** §23 G2（rename/move） |
| 7 | **修改**：删除 | §10.2 一行"删除" | ❌ 无引用完整性检查、无 API → **已补** §23 G2/D25 |
| 8 | **校验**：语法/引用/白名单/冲突 | FR-2 + §10.5 validate ✅ | ✅ |
| 9 | **使用**：启动 `--profile` | 已有实现（§1.1） | ✅ |
| 10 | **使用**：会话内切换（TUI/前端） | 第三部分 §14-§21 | ✅ |
| 11 | **使用**：设为默认（影响新会话） | §10.5 `activate` 端点把"设为默认"与"应用到会话"混装在一个端点 | ◐ 语义混装，需拆分 → **已修复**：§10.5 拆为 `default`/`apply`（§23 G3/D26） |
| 12 | **反馈**：量化/生效报告 | FR-5 + D23 Switch Report | ✅ |
| 13 | **分享**：导出 / 导入 / 项目级随仓库分发 | §9 D16 提到"项目级 profile 可随仓库分发"，但无导出/导入设计 | ❌ → **已补** §23 G5/D28 |
| 14 | **安全**：项目级 profile 的信任门控 | **无**（见 G6：`internal/profile` 零 trust 引用） | ❌ **安全缺口** → **已补** §23 G6/D29 |
| 15 | **验收**：端到端剧本（创建→用→改→分享） | 各批次有出口条件，但无跨阶段 E2E 剧本 | ❌ → **已补** §23 G7（E2E-1~7） |
| 16 | **TUI 可达性**：在 TUI 内完成创建/保存 | §17.1 仅查看+切换（status/list/show/diff/use/pick/reload/off/save） | ◐ 缺 create/duplicate/save-as/edit → **已补** §23 G4/D27 |

**结论**：**"使用/切换"闭环已完整（第三部分）；"创建/修改/分享/安全"存在 5 个实质缺口**（第 3、4、6/7、13、14 行），第 2、11、16 行为部分缺口。以下 G1-G7 逐项补遗，并**已回填正文**（§10.2 列表操作、§10.5 API 清单、§17.1/§17.2 命令面、§10.6 对照表、§11/§19 批次范围），本文档内部一致性已修复。

## 23. 补遗设计（G1-G7）

### G1 — 创建闭环：三种入口，一个语义（D24）

用户创建 profile 的三种现实路径，全部要支持：

| 入口 | 场景 | 载体 | 语义 |
|---|---|---|---|
| **模板新建** | "我要一个 review 场景" | 前端向导 / `aicli profile create --template review` / TUI `/profile create` | 从 FR-2 内置模板生成骨架（含注释），落到 user 层（默认）或 project 层 |
| **复制（duplicate）** | "在现有基础上改" | 前端列表"复制" / `aicli profile duplicate <ref> <new-name>` | 深拷贝现有 profile 全部文件到目标层；**内置 profile 只读，复制即"另存为"** |
| **从会话固化（save-as）** | "把现在这套调好的存下来" | TUI `/profile save-as <name>` / 前端"从当前会话创建" | 把当前会话**实际生效面与基线的差分**固化为新 profile |

**D24（save-as 的核心语义决策）**：固化的是**与基线的差分**，不是全量快照。
- 理由：全量快照会随基线演进腐化（基线新增工具不会进入快照 → 用户以为"没生效"）；差分则天然继承基线变化。
- 固化内容：工具面（`allowlist`/`denylist` 的实际差分）、skills（allow/deny 差分 + 目录）、MCP（`use_servers`/`exclude_servers`）、`permission_mode`（仅当与默认不同）、prompts（**不固化**——会话 prompt 可能来自临时上下文，固化会污染；UI 明示"prompt 未包含，可在编辑器中补充"）。
- 固化后引导（闭环关键）：**"立即使用（应用到当前会话）/ 设为默认 / 稍后"** 三选一，创建即有去处。

**G1 的 API 补遗**（原 §10.5 的 6 个端点不含创建，**已回填至 §10.5**）：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/runtime/profiles` | 创建：`{name, layer, template?|from_ref?, from_session?}`；模板/复制/固化三模式合一入口 |
| POST | `/api/runtime/profiles/{ref}/duplicate` | 复制（等价于 POST 的 `from_ref` 模式，保留独立端点便于 UI 直接调用） |

### G2 — 修改补全：重命名 / 移动 / 删除 + 引用完整性（D25）

**D25（删除保护）**：删除前必须做**引用完整性检查**，四类引用：

| 引用源 | 检查方式 | 处理 |
|---|---|---|
| `config.profiles.default_profile` | 读全局配置 | **阻止删除**，提示"先改默认 profile 或使用 `--force`（同时清空 default）" |
| 活跃会话 `profile_ref`（`sessionmeta`） | 会话注册表 | 允许删除，但列出受影响会话；这些会话 resume 时按 R18 降级（警告 + 保持会话不崩） |
| 其他 profile 的 agent 引用 | profile `agents` 声明 | 警告（悬空引用），不阻止 |
| 项目级 profile 的工作区文件 | 文件系统 | 删除即删除工作区文件（需二次确认，展示将删除的路径清单） |

**删除语义**：硬删 + 二次确认 + 路径清单（不做回收站——引入第二套状态源，违背"不引入第二套方言"纪律；前端在确认框中展示 diff）。

**API 补遗**：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/runtime/profiles/{ref}/rename` | 重命名（同层）；返回旧/新 ref 与引用更新情况 |
| POST | `/api/runtime/profiles/{ref}/move` | 层级移动（user↔project）；同层拒绝；跨层冲突（目标已存在）拒绝 |
| DELETE | `/api/runtime/profiles/{ref}` | 删除；响应体含引用检查结果；`?force=true` 处理 default 引用 |
| GET | `/api/runtime/profiles/{ref}/references` | 只读：列出所有引用（供 UI 在删除/移动前展示） |

### G3 — 使用闭环补全：拆开"设为默认"与"应用到会话"（D26）

原 §10.5 的 `activate` 端点混装两种语义（**已按本节拆分回填修复**），前端无法给出准确文案（"设为默认"与"立即切换"对用户是两件事）：

| 方法 | 路径 | 语义 | 生效范围 |
|---|---|---|---|
| POST | `/api/runtime/profiles/{ref}/default` | 设为默认 profile | **新会话**（写 `config.profiles.default_profile`，走层感知写回） |
| POST | `/api/runtime/profiles/{ref}/apply` | 应用到**指定会话**（`session_id` 必填，D31：不推断"当前会话"） | **该会话下一 turn**（= 第三部分 `set_profile`，同一执行核心 D21） |

**D26**：两按钮在 UI 上并列且文案互斥说明——"新会话默认（不影响当前）" vs "立即切换（当前会话，下一轮生效）"。创建完成引导（G1）复用同一对动作，不新增语义。

**D31（apply 的会话参数化，Batch 13 slice 1 补）**：HTTP `apply` **必须**显式携带 `session_id`，服务端**不推断**"当前会话"——设置页（`/runtime-config`）等无会话上下文的调用方没有"当前会话"可推断，猜测等于把别的会话切走。缺参 = 400 + 可执行提示；未知会话 = **404**（客户端语义，与 `handler.go` 会话读取口径一致，不落 500）。**A12 的 apply 作用域**（只动显式指定的那一个会话；`default` 与其它会话零变化）由 `TestRuntimeProfilesAPI_ApplyWiresSessionSwitchCore` 钉住；会话内立即切换仍走 composer `/profile`（D21 同一执行核心，不新增第二套语义）。

### G4 — TUI 可达性补全：`/profile` 全生命周期子命令（D27）

**D27（TUI 分工原则）**：TUI 内**全生命周期可达**（用户不该为了建 profile 被迫离开 TUI），但**复杂编辑仍在前端**——TUI 提供"最小闭环 + 路径指引"。

§17.1 已增补以下子命令（本节定义行为细则与失败模式）：

```
/profile create <name> [--template coding|review|minimal|docs] [--to user|project]   # 模板新建
/profile duplicate <ref> <name> [--to user|project]                                  # 复制
/profile save-as <name> [--to user|project]                                          # 从当前会话固化（G1/D24）
/profile edit [<ref>]                                                                # 打印 profile.yaml 路径并尝试 $EDITOR；无编辑器时给出手工路径
/profile rename <ref> <new-name>
/profile move <ref> --to user|project
/profile delete <ref> [--force]                                                      # 删除（引用检查 + 确认）
/profile export [<ref>] [--output <file>]                                            # 导出（G5）
```

行为细则（延续 §17.2"解析不了就报错"）：
- `create` 的 name 非法（含路径分隔符/已存在/保留名）→ 报错，不自动改名；
- `save-as` 无差分（当前会话与基线完全一致）→ 明确提示"当前无差异，无需固化"，**不生成空 profile**；
- `edit` 在无 `$EDITOR` 环境（如纯 ACP）→ 只打印路径，不报错；
- `delete` 命中 default 引用 → 阻止并提示 `--force`（`--force` 同时清空 default 并在报告中明示）。

### G5 — 分享闭环：导出 / 导入 / 项目级分发（D28）

| 动作 | CLI | 前端 | 说明 |
|---|---|---|---|
| 导出 | `aicli profile export <ref> --output <dir\|zip>` | Profiles 页"导出"按钮 | 导出该 profile 目录的全部文件（含 agents/prompts/skills） |
| 导入 | `aicli profile import <path> [--to user\|project] [--name <name>]` | "导入"按钮（文件选择） | 目标层默认 user；project 层需显式选择 |
| 项目级分发 | （无需命令） | — | 提交 `.aicli/profiles/` 随仓库分发，是零成本的分享方式 |

**D28（导入安全纪律）**：
1. 导入**必须**先跑同一 `validate`（语法/引用/白名单/冲突），失败即拒绝；
2. 导入**绝不自动激活**（不写 default、不切换会话），导入后落在列表并提示"已导入，点击使用"；
3. 导入显示**将写入的路径清单**（与删除的路径清单同一投影）；
4. 导入的 profile 在**未信任工作区**加载时受 G6 门控（与本地创建的 profile 同一标准，不给导入开后门）。

### G6 — 信任与安全闭环：项目级 profile 的内容门控（D29，安全缺口）

**现状证据（本次审查发现）**：
- `internal/profile` 全包 grep `trust` **零命中**——profile 解析完全不查信任；
- 但四个入口都在 profile 解析**之前**解析信任（`chat.go:568-571` 注释"resolve folder trust before profile/plugin discovery so project-scope plugins/hooks/MCP are gated consistently"；`agent_stdio.go:682-683`、`exec_run.go:259-260`、`exec_resume.go:95-96`）——即**信任机制存在，但 profile 没接上**；
- `chat_setup.go:255`：`session.SystemPromptText = profileState.PromptText`——profile 的 prompts 是**整体替换 system prompt**的自由文本。

**风险**：clone 一个未信任仓库 → 其 `.aicli/profiles/*/prompts/*.md` 直接注入 system prompt = **仓库驱动的提示注入面**（与 §9 D16 拒绝 `bypass_permissions` 同一类问题，但更隐蔽）。

**D29（按资源类型分级门控）**：项目级 profile 在**未信任工作区**加载时：

| 资源类型 | 性质 | 未信任时行为 |
|---|---|---|
| tools / skills / mcp 裁剪声明 | 只会**收窄**能力（NFR-3） | **正常生效**（收窄是安全的） |
| `permission_mode` 默认 | 只允许 default/accept_edits/plan（D16 已禁止 bypass） | 正常生效 |
| `overrides`（§8 白名单域） | 白名单已排除安全域（D14） | 正常生效 |
| **prompts（system/role/tools）** | **自由文本，注入 system prompt** | **不应用**，并在 `/profile status`、启动摘要、Switch Report 中显式警告"内容因工作区未信任而未应用" |
| **agents 定义中的 prompts** | 同上 | 同上（agent 的工具策略部分可生效） |

- 信任后重载：`/profile reload` 或重新启动即恢复完整应用（复用 `chat_folder_trust.go:186-196` 的"重新解析使门控翻转"先例）；
- 前端 Profiles 页对未信任工作区的 profile 显示"部分内容未应用"徽标 + 一键信任入口（复用 foldertrust 既有 UI 通道）。

### G7 — 端到端验收剧本（跨阶段闭环）

| # | 剧本 | 覆盖阶段 | 通过标准 |
|---|---|---|---|
| E2E-1 | 前端：新建 → 模板 `review` → 校验 → "立即切换" | 创建→校验→切换 | 下一轮请求 `tools[]` 与模板声明一致；Switch Report 显示变更项（A1/A2） |
| E2E-2 | TUI：调好工具面 → `/profile save-as my-review` → `/profile use my-review` → 退出重开 `--profile my-review` | 固化→切换→持久化 | 重开后工具面与固化时一致；`profile.yaml` 仅含差分（D24） |
| E2E-3 | 前端：改 deny 列表 → preview → 保存 → diff → 切换 | 修改→校验→切换 | 保存后 diff 与实际文件一致；切换后断言生效 |
| E2E-4 | 跨目录分享：`export` → 另一工作区 `import` → 使用 | 分享→使用 | 导入后 validate 通过、未自动激活、使用后工具面一致 |
| E2E-5 | 删除保护：删除被 `default_profile` 引用的 profile | 修改→引用完整性 | 被阻止（无 `--force`）；`--force` 后 default 清空且报告明示 |
| E2E-6 | 未信任仓库：untrusted 工作区加载项目 profile | 安全门控 | 裁剪生效、prompts 未应用、警告出现（D29） |
| E2E-7 | resume 漂移：会话绑定 X，X 被删除后 resume | 使用→容错 | 警告 + 会话可用（不崩、不静默降级，R18） |

## 24. 补遗实施批次（Batch 13-14）

| Batch | 内容 | 优先级 | 依赖 | 出口条件 |
|---|---|---|---|---|
| **13** | **创建/编辑/分享闭环**：`POST /profiles`（模板/复制/固化三模式，G1）、`rename`/`move`/`DELETE`/`references`（G2）、`default`/`apply` 拆分（G3）、TUI 命令面增补（G4）、export/import（G5） | P1 | Batch 10-12（第三部分执行核心） | E2E-1/2/3/4/5 通过；A9-A12 断言通过 |
| **14** | **信任门控 + 安全验收**：D29 分级门控落地（`internal/profile` 接入 foldertrust）、未信任警告面（TUI/前端/报告）、E2E-6/7 | **P0（安全）** | Batch 13 | E2E-6/7 通过；A13-A14 断言通过 |

**新增断言（延续 A1-A8 防假开关纪律）**：

| # | 断言 | 防的假开关 |
|---|---|---|
| A9 | `save-as` 产物仅含与基线的差分；无差分时报错不产空 profile | 固化语义漂移（R21） |
| A10 | 删除被 `default_profile` 引用的 profile 在无 `--force` 时必失败；`--force` 后 default 必被清空 | 悬空引用（R22） |
| A11 | 导入的 profile 在任何情况下不改变当前会话 profile 与 default | 导入自动激活 |
| A12 | `apply` 端点只影响当前会话（default 不变）；`default` 端点只影响新会话（当前会话不变） | D26 语义混装回归 |
| A13 | 未信任工作区加载项目 profile：tools 裁剪生效 **且** prompts 未注入（对 `SystemPromptText` 断言不含 profile 文本） | **D29 假门控** |
| A14 | 信任后 `reload` 使 prompts 恢复注入（同一会话内翻转） | D29 单向门控 |

## 25. 补遗风险（R21-R24）

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| R21 | **固化语义漂移**：`save-as` 若做成全量快照，基线演进后 profile 静默过时 | 用户以为"没生效"，排障成本高 | D24 差分语义 + A9 断言 + `/profile diff` 可视化差分 |
| R22 | **删除/移动造成引用悬空**：default、活跃会话、agent 引用指向不存在的 profile | resume 失败或静默降级 | D25 四类引用检查 + A10 断言 + R18 降级路径兜底 |
| R23 | **导入供应链**：恶意 profile 包（prompts 注入 / 越权声明） | 提示注入、权限越界 | D28（validate + 不自动激活 + 路径清单）+ D29（信任门控）+ 白名单校验拒绝越界声明 |
| R24 | **TUI 与前端双写冲突**：同一 profile 在两个入口同时编辑 | 后写覆盖 | 复用 §10.5 既有"原子写 + mtime 冲突检测"；冲突时拒绝并提示 reload（不合并、不静默覆盖） |

## 26. 补遗开放问题（19-22）

| # | 问题 | 影响面 | 倾向 |
|---|---|---|---|
| 19 | `save-as` 差分的口径：工具面按"绝对列表差分"还是"语义差分"（如 `denylist` 差分 vs 全量列表） | D24 实现 | 倾向按**声明式字段逐个差分**（与 `profile.yaml` 字段一一对应，可读、可 diff） |
| 20 | 删除是硬删还是软删（`.trash/`） | D25 实现 | 倾向硬删 + 二次确认（不引入第二套状态源，见 D25 理由） |
| 21 | 导出包格式：目录 / zip / 单文件（含内联 agents） | G5 实现 | 倾向**目录或 zip**（单文件内联会引入第二套 profile 方言，违背纪律） |
| 22 | 未信任工作区是否提供"一键信任并重载"（G6 的 UI 闭环） | D29 UX | 倾向提供（复用 foldertrust 既有 UI 通道），但信任动作仍需显式确认 |

## 附录 E — 第四轮核实清单（待回填）

> 第四部分的设计引用了以下尚未逐一核实的事实；实施 Batch 13-14 前需回填验证（延续附录 B/D 的"先核实再实施"纪律）。

| # | 待核实 | 为什么重要 | 建议核实方式 |
|---|---|---|---|
| 1 | `foldertrust.Resolution` 的消费方式：是否已有"按资源类型门控"的先例（如插件/MCP 目录如何用 trust 结果） | D29 分级门控要复用既有模式而非新造 | 读 `internal/foldertrust` 与 plugin/MCP 目录发现处对 `Resolution` 的使用 |
| 2 | `.aicli/profiles` 目录发现是否已被其他路径间接门控（本次 grep `internal/profile` 零命中，但可能在上层调用处） | D29 的"缺口"结论需确认不是重复门控 | 全仓 grep `profiles` + `foldertrust` 交叉点；跑未信任目录加载实验 |
| 3 | 前端 `mcp-form.tsx` 的 CRUD 表单模式可否直接复用于 profile 模板向导 | ✅ 已核实（Batch 8 前置，2026-09-24）：**可复用**——`mcp-form.tsx`（470 行）= 草稿类型 + 纯函数文本↔请求映射 + 表单组件 + 独立测试（`mcp-form.test.ts`），且独立于 config document 草稿（提交时调 `/api/runtime/mcps`，父面板负责反馈/刷新）→ profile 创建向导按同一结构实现（`ProfileDraft` + `createProfileDraft`/`buildProfileCreateRequest` + 测试） | 读 `frontend/src/components/workspace/settings/backend-config-settings-page/sections/modes/mcp-form.tsx`（470 行）+ `mcp-form.test.ts` |
| 4 | `aicli init` 的模板生成机制（`init.go`）能否复用为 profile 脚手架 | FR-2/Batch 2 与 G1 模板共用一套模板定义 | 读 `backend/cmd/aicli/commands/init.go` |
| 5 | 会话 `profile_ref` 在 server 侧的写入点（`/api/agent/chat` 路径） | G2 删除引用检查必须覆盖 server 创建的会话 | grep `ProfileRef`/`profile_ref` 在 `internal/chat` 与 `internal/api` 的写入点 |
| 6 | 原子写在 `profile.yaml` 上的适用性（§10.5 已假设） | G2 rename/move/delete 与 R24 冲突检测依赖它 | 定位既有原子写工具与其在配置文件写入处的用法 |
| 7 | `/profile save --to session` 的会话层存储位置（附录 D 第 5 项的姊妹项） | G1 `save-as` 需要区分"会话层覆盖"与"落盘 profile" | 读 `sessionmeta` 与 runtime store 中 profile 相关字段 |

---

## 附录 D32/D33：Batch 13 slice 2 回填（2026-09-24）——export/import 落地契约

**落地范围**：执行核心 `internal/profile/transfer.go`（收集/打包/读包/物化，API 与 CLI 共用）+ 两个端点（`POST /api/runtime/profiles/{ref}/export`、`POST /api/runtime/profiles/import`）+ CLI 子命令 `aicli profile export/import`（slice 3）。TUI `/profile export`、前端按钮**尚未接线**（TUI 的 `export` 子命令当前是"声明但未接线"状态）。

**包格式（Q21 落地）**：zip 条目名 = profile 根相对路径（`profile.yaml`、`agents/...`、`skills/...`），导入原样物化、不重排、不改写内容。**不支持单文件内联**（会引入第二套 profile 方言）。

**有界传输（R23）**：`BundleMaxFiles=256`、`BundleMaxFileBytes=2MiB`、`BundleMaxTotalBytes=8MiB`（未压缩）；请求体（压缩）上限 8MiB。导出跳过符号链接与 `*.tmp`（原子写残留）；导入拒绝符号链接条目、目录条目仅做路径检查。

**D32（导入命名契约）**：包内 `profile.yaml` 的 `name` 是**权威**；显式 `name` 参数必须与之一致（不一致 → 400，提示"改名请导入后用 rename"）——**导入不静默改写 profile.yaml**（避免"目录名 vs 声明名"两套状态源）；包内无 `name` 时必须显式传 `name`。注意不能拿 `validation.ProfileName` 当声明名：它是 resolved 名，对没写 `name` 的包会退化成临时目录名。

**D33（导入落位契约）**：物化到层根下 `.import-*` 临时目录 → 跑**同一个** `ValidateProfileReference` → 失败即拒绝（层根不留痕，含临时目录）→ 成功 `os.Rename` **原子落位**（列表随即以 `source=root` 可见）。同名目标 **409 不覆盖**；`dry_run=true` 只预演（返回路径清单但不落盘）；**绝不自动激活**（不写 default、不碰会话、不写宿主配置——测试断言宿主 config.yaml 不存在/字节不变）；响应含 `paths` 清单（与删除端点同一投影）与 `activated:false`。

**测试锚点**：`internal/profile/transfer_test.go`（路径清洗表、读包安全、收集/打包往返、临时文件与符号链接排除、物化逃逸拒绝）；`internal/api/skills/profiles_transfer_handlers_test.go`（导出→导入往返 + 列表可见 + 不激活 + 冲突不覆盖 + dry_run + 命名契约 + 五类恶意包拒绝且磁盘不留痕 + 解压成功但 validate 失败拒绝）。测试通过 `HOME/USERPROFILE` 重定向把 `layer=user` 的层根指到临时目录，避免污染真实用户目录。

**slice 3（CLI）补充**：`aicli profile export <profile> [--out <path>] [--dry-run]`、`aicli profile import <path> [--to user|project] [--name <name>] [--dry-run]`。两处实现细节：① 设计表的 `--output <dir|zip>` 与既有 `--output`（输出格式 text|json）重名，CLI 用 `--out`（缺省 `./<profile>.zip`，指向已存在目录则写 `<目录>/<profile>.zip`）；② `--dry-run` 的临时目录放系统临时区、**不创建层根**（预演不该在用户仓库里留下 `.aicli/` 空目录），真实导入仍在层根内建临时目录以保证 `os.Rename` 同盘原子落位。另外把「层根在哪」的规则收敛到 `internal/profile.LayerRoot`（API 与 CLI 共用一份，API 侧只留包内别名），导入命名契约也收敛为 `internal/profile.ResolveBundleProfileName`。

**遗留（Batch 14 及后续）**：① G6/D29 信任门控——未信任工作区里导入的 profile 与本地创建同标准（本项目 profile 解析仍未接 foldertrust，Batch 14 处理）；② 导出/导入的 TUI/前端入口（CLI 与 API 已接线）；③ `from_session`（G1/D24）仍是显式 501。
