# Codex 对照与 Skill 按需加载整理

Date: 2026-03-17
Scope: Codex `skill` / `multi-agent` 分析文档，对照当前 `ai-gateway` 实现，重点聚焦 `skill` 按需加载能力

> 迁移说明（2026-03-30）：
> - 本文对照的 runtime/skill/team/toolbroker 主体实现，当前已迁至 `E:\projects\ai\ai-agent-runtime\backend`
> - 文中旧 `internal/runtime/<subpkg>` 与 `internal/api/skills/*` 路径应按独立 runtime 仓理解
> - 这份文档保留为架构对照分析，不再代表 `ai-gateway` 当前源码目录结构
> - 如需按当前仓库定位代码，请优先映射到 `backend/internal/*` 与 `backend/cmd/aicli/*`

## 结论摘要

当前系统与 Codex 的核心差异不是“有没有 skill / multi-agent”，而是两者所在层级不同：

- Codex 更偏 **agent control plane**
  - skill 是 turn 级能力目录与按需注入机制
  - multi-agent 是 thread/sub-agent 生命周期管理
- 当前系统更偏 **runtime execution plane**
  - skill 是常驻注册的可执行能力单元
  - multi-agent 是 team/task 持久化协作系统，外加一条轻量 `spawn_subagents` 路径

因此，如果要对齐 Codex，最值得补的不是重写现有 skill runtime，而是在现有 runtime 之上补一层：

1. **skill 按需暴露**
2. **skill 按需加载**
3. **轻量子代理控制面**

其中最优先的是第 2 点：**skill 按需加载**。

---

## 一、Codex 与当前系统的主要差异

### 1. Skill 载体不同

Codex：

- 以 `SKILL.md` 为中心
- 可选 `agents/openai.yaml`
- 重点是“文档 + 指令 + 使用约束”

当前系统：

- 以 `skill.yaml/skill.yml` 为中心
- 可选 `prompt.md`
- 重点是“结构化能力定义 + 路由 + 执行”

当前代码位置：

- `internal/runtime/skill/skill.go`
- `internal/runtime/skill/manifest.go`
- `internal/runtime/skill/loader.go`

### 2. Skill 使用模型不同

Codex：

- discovery 阶段扫描 roots
- turn 内只注入 skill 摘要
- 显式命中的 skill 再懒加载正文

当前系统：

- 启动时 `LoadAllWithRegistry`
- `Registry` 常驻持有全部 skill
- `Router` / `Executor` 直接复用常驻 skill 对象

当前代码位置：

- `internal/runtime/bootstrap/manager.go`
- `internal/runtime/skill/registry.go`
- `internal/runtime/skill/router.go`
- `internal/runtime/skill/executor.go`

### 3. Skill 运行目标不同

Codex 的 skill 偏：

- prompt injection
- 操作手册
- skill-aware tool use

当前系统的 skill 偏：

- LLM 执行单元
- MCP tool bridge
- workflow DAG 并发执行

当前代码位置：

- `internal/runtime/skill/executor.go`
- `internal/runtime/skill/dag.go`
- `internal/runtime/skill/embedding_router.go`

### 4. Multi-agent 主抽象不同

Codex：

- `spawn_agent`
- `send_input`
- `wait_agent`
- `close_agent`
- `resume_agent`

当前系统：

- 重型路径：`spawn_team` + `task/mailbox/path-claim/outcome`
- 轻型路径：`spawn_subagents`

当前代码位置：

- `internal/runtime/toolbroker/broker.go`
- `internal/team/*`
- `internal/runtime/agent/scheduler.go`
- `internal/runtime/agent/loop.go`
- `internal/runtime/agent/orchestrator.go`

### 5. 当前系统反而更强的点

- team/task/mailbox/path claim 持久化更完整
- blocked/handoff/replan 治理链路更完整
- workflow / MCP / embedding 已与 skill runtime 深度集成

---

## 二、当前系统已经具备的“准按需”能力

虽然当前系统还没有真正的 **skill 按需加载**，但已经有一层 **skill 按需暴露** 的雏形，主要出现在 `aicli`。

### 1. CLI 已经支持按 prompt 分析要暴露哪些 skill function

现有逻辑：

- 显式命中 skill 名或 `skill__*` 函数名
- 使用路由器根据 prompt 选择候选 skill
- 把已调用过的 skill 继续保留为暴露函数

代码位置：

- `cmd/aicli/commands/skills_integration.go`
  - `AnalyzeSkillExposure`
  - `findExplicitSkillMentions`
- `internal/runtime/chatcore/catalog.go`
  - `Select`
  - `NormalizeSkillExposureMode`

### 2. 但这还不是按需加载

当前 CLI 做的是：

1. 初始化时先把所有 skills 全部加载进 runtime
2. 为所有 skills 注册 `skill__*` function
3. 每次请求只选择一部分 function schema 暴露给模型

也就是说：

- **已实现**：按需暴露
- **未实现**：按需加载

这个差异非常关键。

---

## 三、什么叫“skill 按需加载”

建议明确区分两个概念。

### 1. 按需暴露

含义：

- 模型这一轮只看到部分 skill
- 目的是减少工具目录膨胀

当前系统：

- CLI 已部分实现

### 2. 按需加载

含义：

- 启动时不把所有 skill 的完整内容都读进运行态
- 只先构建轻量目录/摘要/索引
- 只有在下面几类场景才读取完整 skill：
  - 被显式提及
  - 被路由命中
  - 真正执行
  - 需要注入完整 prompt / workflow / permissions 时

Codex 的重点更接近这个模型。

---

## 四、当前系统距离“按需加载”的具体差距

### 1. Loader 当前是全量加载模型

`bootstrap.Manager` 在启动时直接：

- 构建 `Loader`
- `LoadAllWithRegistry`
- 将完整 skill 放入 `Registry`

代码位置：

- `internal/runtime/bootstrap/manager.go`

这意味着：

- 所有 skill manifest 会被解析
- 所有 companion `prompt.md` 会被读入
- 所有 skill 立即成为常驻对象

### 2. ManifestParser 会主动读取 `prompt.md`

当前 `ParseFile` 调用后会继续：

- `loadCompanionPrompt`

这会导致即使某个 skill 从未被使用，也会在加载期读取正文 prompt。

代码位置：

- `internal/runtime/skill/manifest.go`

### 3. Registry 当前保存的是完整 Skill，不是摘要

当前 `Registry` 持有：

- 完整 `Skill`
- keyword/pattern 索引

但它没有：

- summary/catalog entry
- lazy resolver
- load state

代码位置：

- `internal/runtime/skill/registry.go`

### 4. CLI skill function 仍是“全注册”

虽然 CLI 已经做了按需暴露，但初始化时仍然：

- 遍历全部 skills
- 为每个 skill 建立 `SkillFunction`
- 注册到 catalog

代码位置：

- `cmd/aicli/commands/skills_integration.go`

### 5. API/Agent 主线还没有 turn 级 skill 注入层

当前服务端主线是：

- route -> execute

而不是：

- turn skill exposure -> explicit injection -> lazy resolve -> execute

这正是与 Codex 的重要区别。

---

## 五、建议的目标形态

建议不要推翻现有 `Loader / Registry / Router / Executor`，而是在其上拆出两层：

1. **Skill Catalog（轻量发现层）**
2. **Skill Resolver（完整加载层）**

### 目标分层

#### A. Discovery / Catalog 层

只负责：

- 扫描 skill 文件
- 解析最小元数据
- 建立路由索引
- 建立 source/path/layer 信息
- 保存 prompt/workflow 是否存在，但不读取大正文

建议产物：

- `SkillSummary`
- `SkillCatalog`

#### B. Exposure 层

只负责：

- 根据 prompt/explicit mention/history 计算本轮可见 skills
- 生成摘要目录或 function schema
- 控制 top-k 与 exposure mode

这个层可以复用现有：

- `AnalyzeSkillExposure`
- `chatcore.Catalog.Select`

#### C. Resolver / Lazy Load 层

只负责：

- 在真正执行 skill 前
- 在需要完整 prompt 注入前
- 根据 `summary.Source.Path` 读取完整 skill

并做：

- mtime/fingerprint cache
- 失败重试
- prompt/workflow lazy hydration

#### D. Executor 层

只接受完整 skill：

- LLM default execution
- workflow execution
- MCP-backed execution

---

## 六、最小可落地方案

下面是一个尽量少改现有架构的实现方案。

### Phase 1：先把“发现”和“完整加载”拆开

建议新增：

- `SkillSummary`
- `SkillLoadOptions`
- `Loader.DiscoverAll(...)`
- `Loader.LoadFullByPath(...)`

建议行为：

- `DiscoverAll` 只读取：
  - 名称
  - 描述
  - triggers
  - tools
  - category/tags/capabilities
  - source/path/layer
  - prompt 是否存在
  - workflow 是否存在、步骤数
- 不读取 `prompt.md` 正文
- 不注入 `SystemPrompt/UserPrompt`

### Phase 2：Registry 拆成 Catalog-first

建议引入：

- `SkillCatalog`
- `LoadedSkillCache`

其中：

- `SkillCatalog` 存 `SkillSummary`
- `LoadedSkillCache` 存已解析的完整 `Skill`

这样：

- 路由器使用 `SkillCatalog` 即可工作
- 执行器在真正执行前再调用 resolver

### Phase 3：让 Executor 支持 lazy resolve

建议新增包装：

- `LazyExecutor`

输入：

- `skill name` 或 `summary`

执行前：

1. 查 `LoadedSkillCache`
2. 缓存 miss 时 `LoadFullByPath`
3. 再委托给现有 `Executor`

这样可以最大限度复用：

- `internal/runtime/skill/executor.go`

### Phase 4：先改 CLI，再改 API

CLI 是最容易落地的入口。

当前 CLI 已经具备：

- explicit mention
- route-based exposure
- top-k exposure mode

因此建议先把 CLI 改成：

1. 启动时只 discovery 全部 skill summaries
2. 注册轻量 `skill__*` function schema
3. function execute 时再 lazy load skill 正文

这样收益最大、改动最小。

### Phase 5：服务端再补 turn 级 skill exposure

服务端可后续补：

- `TurnSkillExposure`
- `ExplicitSkillResolver`
- `SkillPromptInjector`

但这属于第二阶段，不必阻塞 Phase 1~4。

---

## 七、建议修改的代码点

### 必改

- `internal/runtime/skill/manifest.go`
  - 拆出 metadata-only parse
  - companion prompt 改为懒读取

- `internal/runtime/skill/loader.go`
  - 新增 discover-only 与 full-load API

- `internal/runtime/skill/registry.go`
  - 从“完整 Skill registry”扩展为“summary + full cache”

- `cmd/aicli/commands/skills_integration.go`
  - 从“全量 SkillFunction 注册 + 暴露裁剪”改为“summary 注册 + execute 时 lazy load”

### 可选但建议改

- `internal/runtime/bootstrap/manager.go`
  - 启动期默认 discovery，而不是 full load

- `internal/runtime/skill/router.go`
  - 索引输入改为 `SkillSummary`

- `internal/runtime/skill/embedding_router.go`
  - embedding 索引基于 summary 文本构建

### 后续再改

- `internal/api/skills/handler.go`
  - 补 turn 级 exposure / injection

- `internal/runtime/agent/orchestrator.go`
  - 让 planner / route-first 主线能复用 lazy exposure

---

## 八、改造收益

### 1. 降低启动成本

- skill 多时，不必在启动期读取全部 `prompt.md`
- 大型 skill pack 更容易挂载

### 2. 降低上下文膨胀

- 模型只看到当前需要的 skill
- 减少 function catalog 与 prompt 噪音

### 3. 更接近 Codex 模型

- discovery 与 use 分离
- summary 与正文分离
- 显式命中与执行期加载分离

### 4. 更利于 profile / workspace 组合

当前系统已有：

- `skill_dir`
- `extra_skill_dirs`
- profile 级 `resolved.SkillDirs`

引入 lazy load 后，多 skill roots 的管理成本更低。

---

## 九、风险与注意事项

### 1. 不能破坏现有 workflow/MCP 执行链

当前 skill runtime 的强项就是：

- workflow
- MCP
- embedding

所以按需加载改造必须是“前置 discovery 分层”，而不是推翻 `Executor`。

### 2. inline prompt 需要兼容

当前 prompt 既可能来自：

- `skill.yaml` 内联字段
- `prompt.md`

建议统一为：

- discovery 阶段只读“是否存在 prompt 与简要元信息”
- full load 阶段再合并最终 prompt

### 3. 热加载要同步 summary cache 与 full cache

当前热加载已经会处理：

- reload skill
- embedding sync

改造后需要同时更新：

- summary catalog
- loaded cache

否则会出现：

- summary 是新版本
- full skill 还是旧版本

### 4. “按需暴露”不等于“按需加载”

这是改造中最容易混淆的点。

当前 CLI 已经有前者，但没有后者。

---

## 十、Multi-agent 侧建议

与 Codex 对齐时，multi-agent 不建议先动重型 `team` 系统，而是增加一层轻量控制面：

- `spawn_agent`
- `send_input`
- `wait_agent`
- `close_agent`
- `resume_agent`

建议做法：

- 保留 `spawn_team` 作为长任务/持久化协作层
- 保留 `spawn_subagents` 作为内部快速 fan-out
- 再新增一个对模型更友好的轻量 agent lifecycle API

这样可以形成三层：

1. **light collab**：Codex 风格 thread/sub-agent
2. **planned fan-out**：当前 `spawn_subagents`
3. **persistent team**：当前 `spawn_team`

---

## 十一、建议优先级

### P0

- 落地 `skill 按需加载`
- 先在 CLI 打通
- 保持现有 `Executor` 不变

### P1

- 服务端补 `turn` 级 skill exposure / injection
- 把当前 route-first agent chat 也接到 summary-first 模型

### P2

- 增加 Codex 风格轻量 multi-agent control plane
- 让 `spawn_team` 与 `spawn_subagents` 各归其位

---

## 最终建议

如果只做一件最值得的事，建议是：

> **把当前系统从“skill 全量加载 + 选择性暴露”升级为“skill 摘要发现 + 选择性暴露 + 执行时按需加载”。**

这是当前系统与 Codex skill 架构之间最关键、也最容易产生实际收益的一步。
