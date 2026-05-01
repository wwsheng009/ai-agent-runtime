# Skills 暴露链路实施与测试方案

状态: **implemented**

实施结果:

- 共享投影层已落到 `backend/internal/skill/exposure_projection.go`，`tool_surface` 与 `skill_exposure` 的 metadata 组装改为共享实现。
- `skills/config/write` 已接入 `skills` 路由，且 `config/document` 的保存路径也会发布 `skills.changed`，统一触发 codex skills cache 失效。
- `skill_exposure` 现在携带计数型 telemetry，`skills/list` 响应带有 `cache_hit`，`skills.changed` 事件带有 `codex_list_cache_version`，方便观察缓存和暴露状态。
- `backend/go test ./...` 已通过。

范围:

- `backend/internal/agent`
- `backend/internal/chatcore`
- `backend/internal/skill`
- `backend/internal/api/skills`
- `backend/internal/llm`
- `backend/internal/agentconfig/config.go`

## 背景

当前系统已经具备三条相邻但没有完全统一的链路：

1. `skills/list` / `skills.changed` 这类对外发现协议。
2. `CapabilityDescriptors()` / `Catalog.Select()` 这类统一能力与工具选择逻辑。
3. `LLMRequest.Tools` -> provider adapter -> 实际模型请求的工具注入逻辑。

现状的问题不是“没有 skills”，而是以下三点没有闭环：

- 技能暴露策略还没有真正接到运行时工具选择。
- skills 发现接口缺少缓存与失效闭环。
- skills / capability / tool 三种视图还没有统一投影层。

本方案的目标不是重写整套技能系统，而是把这三条链路收敛成一条可配置、可缓存、可测试、可回归的路径。

## 目标

1. 让技能暴露策略真正进入运行时，而不是只存在于配置字段或局部 helper。
2. 让 `skills/list` 在没有配置、没有 mcp manager、没有额外 roots 的情况下都能稳定返回空结果或局部结果，不因缺省配置失败。
3. 让技能、能力、工具三种视图使用同一份基础数据，减少重复拼装和排序抖动。
4. 让 Codex 兼容能力继续向前推进，但不破坏当前 runtime 的 legacy 行为。
5. 让所有关键行为都能通过单元测试和集成测试覆盖。

## 优先级划分

### P0

- 接通技能暴露策略到运行时工具选择。
- 为 `skills/list` 增加缓存与失效机制。
- 保证空配置、空 manager、空 roots 时返回稳定的空结果。

### P1

- 统一技能 / capability / tool 的排序和投影。
- 收敛 prompt/request builder 的职责，避免把 skills 列表直接塞进 prompt。
- 为 LLM 请求构造增加可观察的调试字段。

### P2

- 补齐 Codex 风格的技能配置写入或等价能力。
- 强化 telemetry、diagnostic event 与回归测试覆盖。

## 实施方案

### 1. 接通技能暴露策略到运行时工具选择

#### 1.1 当前问题

当前代码里已经存在技能暴露相关配置：

- `AICLISkillExposureTopK`
- `AICLISkillExposureMode`

但从现有链路看，这些配置并没有稳定进入 turn 级工具选择入口。结果是：

- 技能的可见性仍然主要依赖局部搜索或固定工具列表。
- 没有明确的“模型可见技能预算”。
- `Catalog.Select()` 目前更多是纯选择器，没有作为主路径统一接入。

#### 1.2 改动建议

1. 在 `backend/internal/agent/loop.go` 的工具收集入口建立统一的技能暴露策略对象。
2. 在工具选择阶段显式读取 `AICLISkillExposureMode` 和 `AICLISkillExposureTopK`。
3. 将技能选择从“先搜工具再回退所有工具”改成“先按暴露策略筛，再按 goal 精排，再按 policy 过滤”。
4. 保留现有 `toolWhitelist` 和 `ToolExecutionPolicy` 的约束，但把它们放在最终裁剪层。
5. 当没有可暴露技能时，仍然保留 builtin tools / MCP meta tools 的稳定行为，不让空 skills 变成错误。

#### 1.3 推荐实现顺序

1. 先在 `chatcore.Catalog.Select()` 上接入 `ExposureMode + ExposedSkills + ToolPolicy` 的组合参数。
2. 再从 `agentconfig` 读取暴露策略，构造 `ExposedSkills` 集合。
3. 再把 `computeAvailableTools()` 改成先做 exposure，再做搜索，再做回退。
4. 最后把结果写入 request metadata，方便调试与回放。

#### 1.4 验收标准

- 配置存在时，技能暴露结果会稳定影响 turn 级工具列表。
- 配置不存在时，系统仍能运行，且不会抛出空配置错误。
- 同一输入在同一配置下得到稳定的技能工具顺序。

#### 1.5 测试项

- `Catalog.Select()` 在 `auto / prefer / only` 下的行为测试。
- `AICLISkillExposureTopK` 不同取值的裁剪测试。
- `toolWhitelist` 与 `ToolExecutionPolicy` 的交集测试。
- 空 skills / 空 MCP manager / 空 capability catalog 的回退测试。
- 同一 prompt 重复运行时的 deterministic output 测试。

### 2. 为 `skills/list` 增加缓存与失效闭环

#### 2.1 当前问题

`backend/internal/api/skills/codex_list.go` 当前每次都重新计算：

- cwd 归一化
- configFile 上溯解析
- roots 组合
- `DiscoverCodexSkillLoadOutcome(...)` 文件树扫描

这在技能数量少时问题不大，但在大仓库、多个 cwd、额外 roots、频繁刷新时会明显放大 IO 成本。

#### 2.2 改动建议

1. 增加一个轻量的 skills discovery cache，键至少包含：
   - `cwd`
   - `configFile`
   - `extraRoots`
   - `perCwdExtraUserRoots`
   - `forceReload`
   - bundled skills 开关
   - config layer 影响项
2. `forceReload=true` 时跳过缓存或强制刷新。
3. `skills.changed` 事件触发相关 cache 失效。
4. `skills/config/write`、技能热重载、插件装载/卸载、配置文件 reload 也要触发失效。
5. 缓存粒度要控制在 discovery 结果级别，不要缓存到“全局唯一一份”，否则不同 cwd 会串。

#### 2.3 建议放置位置

- 缓存管理可以放在 `backend/internal/skill` 或 `backend/internal/api/skills` 的私有 helper 中。
- 如果后续要扩展到更多入口，建议统一放到 `backend/internal/skill`，上层只负责调用。

#### 2.4 验收标准

- 同样参数连续调用 `skills/list` 时，第二次命中缓存。
- `forceReload=true` 时必须重新扫描磁盘。
- `skills.changed` 后下一个请求能看到新结果。
- 空配置场景仍可返回空 `results`，不报错。

#### 2.5 测试项

- `skills/list` 正常返回 grouped results 的测试。
- `forceReload=false` 命中缓存的测试。
- `forceReload=true` 重新扫描的测试。
- `skills.changed` 触发失效的测试。
- 多 cwd、多 extra roots、per-cwd extra roots 的组合测试。
- 路径不存在、非法路径、非目录路径时的错误收集测试。

### 3. 统一技能 / capability / tool 的排序和投影

#### 3.1 当前问题

当前系统里至少有三份“技能视图”：

- `SkillSummary` / `CodexSkillMetadata`
- `capability.Descriptor`
- `types.ToolDefinition`

它们分别服务于发现、编排和模型请求，但目前排序和字段投影逻辑分散在多个地方，容易出现：

- map 遍历导致的顺序抖动
- 同一技能在不同层看到的字段不一致
- debug / log / api 返回内容不统一

#### 3.2 改动建议

1. 建一个单一的 skill exposure projection helper。
2. 统一排序规则，至少保证：
   - scope / layer 优先级
   - path 其次
   - name 最后
3. 对外 API 使用结构化投影，而不是各自拼 map。
4. 对模型请求使用最小 schema，只包含真正要给 LLM 的字段。

#### 3.3 推荐排序规则

- Codex 风格列表：`scope -> path -> name`
- Capability 列表：`kind -> source -> name`
- Tool 列表：`policy -> goal score -> name`

#### 3.4 验收标准

- 同样的输入多次执行，返回顺序稳定。
- UI、API、调试日志的技能名称顺序一致。
- 不同层的字段投影没有互相污染。

#### 3.5 测试项

- `Registry.List()` / `ListSummaries()` / `CapabilityDescriptors()` 的顺序稳定性测试。
- `skills/list` 返回顺序稳定性测试。
- `Catalog.Select()` 的 schema 顺序稳定性测试。
- 同一组数据在多次运行下的快照对比测试。

### 4. 收敛 prompt/request builder 的职责

#### 4.1 当前问题

当前 `buildRequest()` 只负责 prompt / history / metadata 组装，这本身是正确的。风险在于后续如果把 skills 列表、capability 列表或者工具目录继续塞进 system prompt，就会出现：

- prompt 膨胀
- 预算污染
- 发现层和推理层耦合

#### 4.2 改动建议

1. 保持 `buildRequest()` 只负责构造对话请求，不直接拼技能目录。
2. 所有可调用技能都应该通过 `Tools` 注入。
3. 仅在必要时向 metadata 注入技能暴露摘要，用于 debug / trace / replay。
4. 对于显式 skill 调用，继续使用 `skill` 输入项或等价结构，而不是只依赖文本 marker。

#### 4.3 验收标准

- prompt 中不出现大段 skills 列表。
- 选中的工具在 `Tools` 中可见。
- `SystemPrompt` 不承担技能发现职责。

#### 4.4 测试项

- request builder 不泄漏 skills 列表的测试。
- `Tools` 注入后的 provider request 结构测试。
- 显式 skill 调用与隐式 skill 调用的分离测试。

### 5. 补齐 Codex 风格兼容能力

#### 5.1 当前问题

当前系统已有：

- `skills/list`
- `skills.changed`
- `capabilities`

但 Codex 风格协议里还包含：

- `skills/config/write`
- 更明确的 per-cwd skills 发现 / 失效语义

如果目标是兼容 Codex 客户端，这里还需要继续补齐。

#### 5.2 改动建议

1. 明确 `skills/config/write` 是不是要做成 HTTP 路由或内部 API。
2. 如果要做，保持它和 `skills.changed` 的事件闭环一致。
3. 返回值和错误语义要和 Codex 保持接近，不要做成纯管理后台风格。

#### 5.3 验收标准

- 配置写入后，skills 发现会刷新。
- 对应事件可以被上层消费。
- 不破坏现有管理后台接口。

#### 5.4 测试项

- 允许写入 / 禁止写入的权限测试。
- `skills/config/write` 后的失效测试。
- path / name 二选一参数校验测试。

### 6. 增强 telemetry 和调试能力

#### 6.1 当前问题

目前虽然有不少 debug 能力，但对技能暴露这条链路来说，还不够集中：

- 看不到某次 turn 最终暴露了哪些 skill/tool
- 看不到裁剪发生在哪一层
- 看不到缓存命中率

#### 6.2 改动建议

1. 在 turn 级 metadata 中记录：
   - 暴露模式
   - topK
   - 最终技能数
   - 缓存命中情况
   - 被裁剪的技能数量
2. 在 `skills/list` 响应或相关 event 中记录：
   - roots 数量
   - group 数量
   - errors 数量
3. 对关键路径增加可观测日志，但控制日志量，避免把完整 body 打爆。

#### 6.3 测试项

- 调试事件字段完整性测试。
- trace / metadata 包含 exposure 摘要测试。
- 日志不会输出完整大 body 的测试。

## 测试方案

### 1. 单元测试

优先覆盖以下模块：

- `backend/internal/chatcore`
- `backend/internal/skill`
- `backend/internal/agent`
- `backend/internal/api/skills`
- `backend/internal/llm`

建议重点新增或补强的测试：

1. `Catalog.Select()` 的 `auto / prefer / only` 行为。
2. `Catalog.Select()` 对 `ToolPolicy` 的过滤。
3. `Registry.List()` / `ListSummaries()` / `CapabilityDescriptors()` 的顺序稳定。
4. `skills/list` 的 cwd / extra roots / per-cwd extra roots 组合。
5. `skills/list` 的缓存命中与 `forceReload` 行为。
6. `skills.changed` 事件触发后的失效。
7. `buildRequest()` 不把 skills 列表拼进 prompt。
8. `ProviderWrapper.convertTools()` 与 `CodexAdapter.BuildRequest()` 的 tools 透传。

### 2. 集成测试

建议补 3 条集成路径：

1. **技能发现路径**
   - 准备一个临时工作区
   - 放入 Codex 风格 skill 和 legacy skill
   - 调 `skills/list`
   - 验证 grouped results、roots、errors、顺序、forceReload

2. **运行时选择路径**
   - 准备 builtin tools + MCP tools + skill catalog
   - 给一个目标 prompt
   - 验证最终 `Tools` 列表只包含允许暴露的项
   - 验证 policy / whitelist 生效

3. **失效恢复路径**
   - 先调用一次 `skills/list`
   - 再发布 `skills.changed`
   - 再调用同样参数
   - 验证结果变化被刷新

### 3. 回归测试

建议把以下行为列为固定回归项：

- 空 mcp manager 时不崩溃，返回空工具或空列表。
- 空 skill 配置时不崩溃，返回空 skills 发现结果。
- 目录不存在时，错误被收集而不是整个请求失败。
- 同一个输入多次执行返回相同顺序。
- Codex / OpenAI / Anthropic 不同协议下 tools 仍然能正确落到各自适配器。

### 4. 手工验证

建议至少做一次完整手工验证：

1. 启动 runtime。
2. 用一个包含 skills 的 workspace 调 `skills/list`。
3. 修改 skill 文件并触发 `skills.changed`。
4. 再次调 `skills/list`，确认结果变化。
5. 发起一次 agent turn，确认最终 tools 列表与技能暴露策略一致。
6. 切换无配置环境，确认空结果可用。

## 推荐实施顺序

### 第一阶段

- 接通暴露策略到 runtime tool 选择。
- 为 `skills/list` 加缓存与失效。
- 补空配置 / 空 manager / 空 roots 的容错测试。

### 第二阶段

- 统一排序与投影。
- 收敛 prompt/request builder。
- 增加调试 metadata。

### 第三阶段

- 补 Codex 风格兼容能力。
- 补 telemetry 和回归测试。

## 风险与回滚

### 风险

1. 暴露策略接通后，可能让某些场景下可用工具数量变少。
2. 缓存引入后，可能出现失效不及时导致的旧结果。
3. 排序统一后，可能影响已有测试快照或前端展示顺序。
4. Codex 兼容补齐后，可能影响当前管理后台 API 的约定。

### 回滚策略

1. 每个阶段独立提交，避免一次性大改。
2. 缓存层支持开关或直接绕过。
3. 暴露策略支持回退到 `auto + 现有搜索逻辑`。
4. 关键路径保留日志和测试，方便快速比对前后差异。

## 交付标准

完成时应满足以下条件：

- 主要优化点有明确实现。
- 单元测试覆盖核心行为。
- 集成测试覆盖发现、失效、选择、回退链路。
- 无配置场景和空 manager 场景均能稳定运行。
- 文档、接口和实现语义一致。
