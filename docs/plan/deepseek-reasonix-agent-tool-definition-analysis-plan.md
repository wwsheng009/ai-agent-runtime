# DeepSeek-Reasonix Agent 工具定义借鉴分析与落地方案

## 1. 文档信息

| 项目 | 内容 |
| --- | --- |
| 分析对象 | `E:\projects\ai\DeepSeek-Reasonix` 与 `E:\projects\ai\ai-agent-runtime` |
| 参考项目版本 | `DeepSeek-Reasonix` HEAD `ed00eccf3` |
| 当前项目版本 | `ai-agent-runtime` HEAD `a54fa5e`；分析时保留工作树中用户已有改动 |
| 分析日期 | 2026-08-11 |
| 输出目标 | 提炼可借鉴的工具契约、Schema 可靠性、执行编排和 Agent-native 工具设计，并形成可分阶段实施方案 |
| 非目标 | 不复制 Reasonix 的全部内置工具，不替换当前权限引擎，不要求一次性重写现有工具实现 |

本文是架构分析和实施计划，不代表本次已经修改运行时代码。文件引用以仓库相对路径为主；参考项目路径使用绝对路径，便于复核。

## 2. 结论摘要

### 2.1 总体判断

Reasonix 的主要优势不在于工具数量，而在于把“工具身份、Schema、能力属性、真实执行目标、授权、证据和结果”组织成一条可审计的契约链：

```text
工具定义 -> canonical Schema -> Registry 身份解析 -> 真实目标授权
-> hook/lease/preflight -> 执行 -> 结构化结果/证据 -> 有界 transcript 输出
```

当前项目已经具备较强的运行时能力：结构化 `ToolResult`、OutputGateway、权限和 Sandbox、多 Agent 控制面、工具面 fingerprint、搜索投影、MCP 并发限制以及 session 稳定快照。这些能力不应被 Reasonix 的较小基础接口替换。

当前最需要借鉴的是四个边界：

1. **Schema 边界（P0）**：外部 MCP Schema 应在注册时 canonicalize、编译验证和隔离，不能让一个坏 Schema 破坏整个 provider 请求。
2. **身份边界（P0）**：MCP 工具必须拥有带 server namespace 的 canonical 名称；同名短别名只能在唯一时解析，歧义必须 fail closed。
3. **定义/执行边界（P1）**：以强类型 descriptor 作为单一事实源，FunctionCatalog、MCP、Broker、runtime-server 仅负责投影和适配。
4. **调度边界（P1/P2）**：已知只读调用可以按连续片段并行；写入、代理解析、证据顺序和失败依赖必须保留屏障，并按原始调用顺序回填结果。

### 2.2 优先级建议

| 优先级 | 建议 | 直接收益 |
| --- | --- | --- |
| P0 | 统一外部 Schema canonicalize/validate/quarantine | 降低 provider 400/422、请求整体失败和难定位问题 |
| P0 | MCP canonical namespace、alias 歧义拒绝、来源诊断 | 消除同名工具静默覆盖和错误执行风险 |
| P0 | contract snapshot 与 provider 错误索引回溯测试 | 防止工具契约漂移，缩短排障时间 |
| P1 | `ToolDescriptor/ToolTraits` 与唯一 Registry | 消除多事实源，统一 policy、schema、执行身份 |
| P1 | resolved target pipeline | 让代理工具的授权、hook、证据针对真实目标 |
| P1 | 分段并行和 mutation dependency barrier | 提升只读吞吐，同时保持写入顺序与失败安全 |
| P2 | 稳定 subagent result ref、结构化 ask/plan | 降低长结果丢失，改善 Agent 编排体验 |

### 2.3 不应照搬的内容

- 不应把当前结构化输出退回 Reasonix 的单一字符串结果；应保留 `output_kind`、binary/MIME、metadata、diagnostic 和 OutputGateway。
- 不应替换当前 capability scope、hook patched-args revalidation、approval mode、sandbox 等权限能力；Reasonix 的 `ReadOnly()` 只能补充基础契约，不能成为完整授权模型。
- 不应复制 Reasonix 的约 20 个内置工具实现。应优先迁移契约和编排模式，复用当前 `ToolBroker`、multi-agent、goal、skill 等能力。
- 不应把每个扩展点都拆成微接口；优先使用强类型 traits，少量真正可选的能力再使用接口，避免类型断言泛滥。
- 不应机械照搬 Reasonix 当前 `ResolvedCall.Commit` 的调用时序。当前代码分析显示其实际调用点靠近普通 permission gate 之前；本项目应重新定义“解析是否有副作用”和“授权后提交”的顺序。

## 3. 分析范围与方法

### 3.1 覆盖范围

本次比较覆盖以下维度：

- 基础 Tool 接口和结果协议；
- Registry、MCP 生命周期、命名和 alias 解析；
- Schema canonicalization、JSON Schema 验证、provider dialect 适配；
- provider 可见工具面与 host context 可见性；
- 权限、hook、preflight、workspace lease、证据和 mutation 分类；
- 批量工具调用、并行调度、失败依赖和输出顺序；
- Agent-native 工具，包括问答、任务委派、计划、能力代理和 subagent 结果读取；
- contract 测试、fingerprint、诊断和迁移风险。

### 3.2 证据来源

Reasonix 重点文件：

- `E:\projects\ai\DeepSeek-Reasonix\internal\tool\tool.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\tool\contract.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\tool\resolved.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\tool\blocked.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\tool\progress.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\tool\shell_execution.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\provider\schema_canonicalize.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\provider\schema_validate.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\provider\schema_dialect.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\provider\schema_error.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\agent\execute_one.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\agent\execute_batch.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\agent\ask.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\agent\task.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\agent\parallel_tasks.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\agent\fleet.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\agent\subagent_result.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\agent\usecapability.go`
- `E:\projects\ai\DeepSeek-Reasonix\internal\agent\submit_plan.go`
- `E:\projects\ai\DeepSeek-Reasonix\docs\TOOL_CONTRACT.zh-CN.md`

当前项目重点文件：

- `backend/internal/toolkit/interface.go`、`registry.go`
- `backend/internal/tools/manager.go`、`agent_adapter.go`
- `backend/internal/mcp/registry/registry.go`、`manager/manager.go`
- `backend/internal/aiclitools/capability.go`、`registry.go`、`mcp_adapter.go`、`function_adapter.go`
- `backend/internal/toolbroker/broker.go`、`team_tools.go`
- `backend/internal/types/message.go`、`metadata_helpers.go`
- `backend/internal/policy/taxonomy.go`
- `backend/internal/agent/loop.go`、`tool_list.go`、`tool_surface_binding.go`、`tool_parallel_scheduler.go`
- `backend/cmd/aicli/commands/function_catalog.go`、`chat_tool_definitions.go`
- `docs/plan/aicli-tool-capability-convergence-plan.md`

## 4. 两项目调用链总览

### 4.1 Reasonix

```text
编译期 Builtins()/插件工具
        |
        v
每次运行的 tool.Registry
  - canonical Schema
  - stable name
  - MCP namespace/alias
  - schemaRev
        |
        +--> Schemas() ------------------> provider tool contract
        +--> SchemasForContext() --------> host/context diagnostics
        +--> ResolveCall() --------------> exact/unique alias/ambiguous
                                      |
                                      v
             ResolvedCall / permission / hook / lease / evidence
                                      |
                                      v
                       ExecuteDetailed/ImageTool/Execute
                                      |
                                      v
                         bounded model output + raw output
```

基础接口在 `internal/tool/tool.go:21`：`Name`、`Description`、`Schema`、`Execute`、`ReadOnly`。Registry 在 Add 时 canonicalize Schema，在调用时不重复读取可变 Schema。`ContextualTool` 只影响 host 投影，执行阶段仍必须 fail closed。

### 4.2 当前项目

当前项目存在多条并行事实源和适配链：

```text
toolkit.Tool
   -> toolkit.Registry
   -> tools.Manager + MCP protocol.Tool
   -> tools.AgentAdapter / skill.ToolInfo
   -> types.ToolDefinition
   -> FunctionCatalog / provider-specific function schema

runtime-owned Agent tools
   -> ToolBroker.Definitions()/DefinitionsForContext()
   -> ReActLoop execution

aicli cross-path capabilities
   -> aiclitools.Registry
   -> FunctionFromCapability / CapabilityMCPManager
   -> shared / actor / runtime-server projections
```

基础接口位于 `backend/internal/toolkit/interface.go:12`，结果位于 `:39`。统一工具管理器在 `backend/internal/tools/manager.go:73` 和 `:132`；Broker 定义与执行仍集中在 `backend/internal/toolbroker/broker.go:130`、`:992`、`:1068`。跨 executor 的能力收敛已在 `docs/plan/aicli-tool-capability-convergence-plan.md` 中规划，新增方案应作为该计划的工具契约增强，而不是另起一套 registry。

## 5. Reasonix 工具定义设计分析

### 5.1 基础 Tool 契约：小接口加可选能力

Reasonix 的基础接口刻意保持很小，但把最关键的安全属性放进基础契约：

```go
type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage
    Execute(context.Context, json.RawMessage) (string, error)
    ReadOnly() bool
}
```

其设计价值有四点：

1. **模型输入和执行输入同构**：Schema 和 `json.RawMessage` 都是 JSON，避免在 map、interface、provider JSON 之间来回转换造成类型损失。
2. **副作用是显式契约**：并行调度、计划模式和审计不必通过工具名称猜测。
3. **复杂能力按需扩展**：`Previewer`、`ImageTool`、`PlanModeClassifier`、`DetailedExecutor`、`ContextualTool`、MCP metadata 等通过可选接口增加能力，不污染所有工具实现。
4. **主循环只依赖协议**：内置工具、插件工具、MCP 工具都能进入同一个 Registry 和执行管道。

可借鉴的不是“必须完全采用字符串返回”，而是“把最小安全契约固定下来，并把扩展能力分层”。本项目应把 `ReadOnly`、mutation、network、retry 等属性从松散 metadata 提升到强类型 descriptor/traits；`ToolResult` 继续保留当前结构化能力。

### 5.2 Registry：运行时集合、稳定身份和 revision

`internal/tool/tool.go:282` 的 Registry 具有以下特征：

- 编译期 built-in 集合与每次运行启用的 Registry 分离；
- `Add`（`:299`）只对 Schema canonicalize 一次，避免每轮请求重新调用插件 Schema；
- provider schema 按稳定名称排序；
- `schemaRev`（`:528`）随着工具集合变化递增，可用于 token 估算和缓存失效；
- MCP 可以按 prefix remove/suspend/resume，处理断线、异步握手和 session 禁用竞态；
- `ResolveCall`（`:422`）精确名称优先，portable alias 只有唯一候选才允许执行，多候选直接返回候选列表。

这里最值得移植的是“稳定 canonical identity + 明确 revision + fail-closed resolution”。当前项目的 session 工具面 fingerprint 已经能描述某次暴露面，下一步应让 fingerprint 来源于唯一 Registry 的 canonical descriptor，而不是多个 adapter 之后再拼接。

### 5.3 MCP 命名与代理解析

Reasonix 将 MCP 工具统一成 `mcp__<server>__<tool>`：

- provider 看到稳定且可逆的名称；
- server-local 原始工具名由 `MCPMetadata` 保留；
- 诊断可以指出 package、server、raw name 和 capability ID；
- `use_capability` 等代理工具使用 `ResolvedCall`（`internal/tool/resolved.go:11`）区分：
  - `DisplayName`：provider transcript 中的代理名；
  - `TargetName`/`Target`：真实执行目标；
  - `Args`：解析后的真实参数；
  - `ReadOnly`/`CapabilityID`：真实安全属性和审计身份；
  - `SkipExecute`/`Unavailable`/`Result`：只解析、拒绝或不可用结果；
  - `Commit`：解析后状态提交回调。

这使 permission、hook、evidence、mutation observation 针对真实目标，而 transcript 仍保留模型调用的代理名。当前项目已有 `search_tool` 投影和 `ToolBroker` 代理等类似需求，应采用同样的“显示身份与执行身份分离”思路，但重新设计 commit 的副作用时序。

### 5.4 Schema 可靠性：先规范化，再编译验证，再隔离

Reasonix 在 `internal/provider` 形成了完整链路：

1. `CanonicalizeSchema`（`schema_canonicalize.go:10`）处理空 Schema、`null`、遗漏根 `type`、缺失 `properties`、不合法的 `required: true`，并稳定排序对象和 required 数组。
2. `ValidateToolSchema`（`schema_validate.go:17`）用 JSON Schema 编译器验证，禁止 `$ref` loader 访问本地文件或网络，避免外部 Schema 在校验时产生越权 I/O。
3. 无效 MCP 工具被隔离，不让单个坏工具导致整次 provider 请求失败。
4. `NormalizeLegacyTupleItemsForDraft202012`（`schema_dialect.go:34`）只在目标 provider 明确使用 2020-12 方言时转换 legacy tuple，避免把 provider-specific 修复污染通用缓存。
5. `AnnotateToolSchemaError`（`schema_error.go:17`）将 provider 的“第 N 个 tool function Schema 错误”映射回工具名、MCP server 和原始工具名。

这组设计直接对应当前项目的 P0 风险。当前 `normalizeParameters` 和 `chat_tool_definitions.go` 主要做浅层 object/default 修复，调用参数的 required/type 校验不能替代 Schema 编译验证；必须把 Schema 处理前移到外部工具进入 unified registry 的边界。

### 5.5 执行管道：真实目标在安全检查前确定

Reasonix `internal/agent/execute_one.go` 的单调用流程大致为：

```text
canonical/alias 解析
-> stale-anchor / 重复调用 guard
-> before 扩展与 proxy resolve
-> ContextualTool gate
-> Plan/Delivery/mutation gate
-> recovery + permission
-> workspace lease / parent write claim / checkpoint barrier
-> PreToolUse hook
-> progress/evidence/jobs 等 context 注入
-> ExecuteDetailed / ImageTool / Execute
-> after hook、blocked/execution outcome 分类
-> evidence receipt、mutation observation
-> bounded model output，必要时保存 raw full output
```

值得借鉴的边界是：解析、授权、执行不可混在一个“按名称直接调用”的函数里；代理解析后的 target 必须重新做 plan mode、mutation 和权限判断；被宿主拒绝的 `BlockedError` 要和执行失败区分。

### 5.6 批处理和并行调度

`internal/agent/execute_batch.go` 的策略比当前项目更细：

- 先按 provider 顺序发出 dispatch；
- 连续、已知、只读工具分段并行，最大并发 8；
- writer、未知工具、代理工具、证据顺序敏感工具保持串行；
- 结果按原始 tool-call 顺序回填；
- 取消或预算耗尽时仍为所有调用补齐结果，保持 provider 消息配对完整；
- writer 执行后刷新后续 writer preview，避免审批基于旧 diff；
- 前一个 mutation 失败后，后续 mutation/verification 标为 dependency not-run，只读诊断仍可运行；
- `complete_step` 等状态工具强制串行。

当前 `backend/internal/agent/tool_parallel_scheduler.go:28` 已有安全的 MCP-only 并行，但要求整批全部满足条件，存在 hook/custom permission 时整体回退串行。Reasonix 的“连续只读片段 + writer barrier + 顺序回填”可作为 P1/P2 增强方向。

### 5.7 Agent-native 工具的契约设计

Reasonix 将 Agent 编排工具视为一等工具，而不是把所有行为塞进一个大而宽的 `task` Schema。

#### `ask`

`internal/agent/ask.go` 对问题和选项有严格边界：问题数 1 至 4、选项数 2 至 4、label 不重复，支持推荐项和多选。无头模式明确返回“这是模型假设，不是用户回答”；用户 dismiss 被解释为停止，不允许模型擅自选择。这个设计把交互不确定性显式传回模型，适合借鉴到当前 `ask_user_question`。

#### `read_only_task` 与 `task`

Reasonix 把只读委派和可写委派拆成不同工具：

- `read_only_task` 的 Schema 不暴露 background、继续 transcript、writer 等危险选项，可在 Plan mode 安全运行；
- `task` 要求声明写路径，profile/tool allowlist 只能求交，调用参数不能扩大父级权限；
- subagent reference 稳定，可继续同一子会话。

这比一个 `task(read_only=true, background=true, ...)` 更容易让 Schema 表达安全边界，也更容易做 policy 和并发预检。

#### `parallel_tasks` 与 `fleet`

- `parallel_tasks` 限制输入最多 64 个任务，分配前校验，任务有独立状态和事件，预览预算公平分配；
- `fleet` 支持 2 至 64 项 DAG、`depends_on`、`fail_fast`、background；并发 writer 必须声明不重叠 write paths；图或路径冲突在启动任何任务前预检失败。

可借鉴点是“先验证图和资源声明，再启动执行”，而不是启动后再依赖运行时错误恢复。

#### `read_subagent_result`

该工具通过稳定 ref 分页读取完整结果：验证 UTF-8 byte offset 边界，限制在当前 conversation lineage 和 workspace，避免把多个 Agent 结果一次塞进固定 tool-result 字段导致截断或上下文污染。当前项目已有 mailbox/events 和 task output，应补充同样的稳定引用、分页、归属校验。

#### `submit_plan` 与 `use_capability`

- `submit_plan` 将 objective、assumptions、non-goals、steps、depends_on、acceptance、verification、risk 结构化，并区分 `verified_files` 与 `candidate_files`；
- `use_capability` 使用固定 Schema 的 lazy MCP proxy：list 不启动 server，inspect 可读 cache，call 才按需连接；真实目标在授权、hook、证据之前解析。

这两种工具分别代表“计划是结构化工件”和“动态能力目录不应直接膨胀 provider Schema”两个值得借鉴的原则。

## 6. 当前项目现状与已有优势

### 6.1 基础接口和结果协议

当前 `backend/internal/toolkit/interface.go:12` 的 Tool 接口包含 `Name`、`Description`、`Version`、`Parameters`、`Execute`、`CanDirectCall`；`ToolResult`（`:39`）支持：

- text / structured / binary / empty 的 `OutputKind`；
- `Content`、binary `Data`、MIME type；
- `Metadata` 和工具级诊断；
- 结构化结果与外层 Go `error`。

这比 Reasonix 的 string-only 基础返回更适合当前图片、下载、MCP output schema 和 OutputGateway 场景，应作为目标架构的保留项。需要解决的是三套成功/失败信号可能不一致，以及 `Parameters()` 返回可变 map、Registry 不做不可变快照的问题。

`CanDirectCall` 当前在 `backend/internal/toolkit/registry.go:92` 的两个分支最终都调用同一 `tool.Execute`，语义基本空转，不能作为副作用或安全属性。建议在 descriptor 迁移中明确区分“调用通道”与“能力 traits”。

### 6.2 工具面稳定性、投影和诊断

当前项目已有一组成熟能力，实施时应作为基线保留：

- Agent turn/session 的工具面 snapshot；
- `ToolDefinitionsFingerprint`（`backend/internal/agent/tool_surface_binding.go:14`）和 eligibility key；
- 大 catalog 的 `search_tool` 投影（`backend/internal/agent/tool_list.go:19`），被投影工具仍可搜索和调用；
- tool schema token 预算、annotation compaction；
- `ToolResult`、`toolprotocol`、OutputGateway 的 structured diagnostic 和 output source；
- `should_list`、`list_when`、`DefinitionsForContext` 的动态可见性；
- tool surface metadata、fingerprint 和 effective surface 的运行时诊断。

Reasonix 的 `Schemas()` 与 `SchemasForContext()` 分离，可以帮助当前项目把这套能力命名得更清楚：provider contract 应稳定，host contextual projection 可以变化，执行时还要再次检查可用性。

### 6.3 权限、preflight 与 multi-agent 控制面

当前 `backend/internal/policy` 已具备较完整的权限基础：capability scope、hook/callback patched args hard revalidation、approval mode、sandbox，以及 taxonomy 中的 `tool_kind`、`read_only`、`mutates_fs`、`requires_net`、`supports_parallel`、`retry_class`、`empty_replay_cache`。

当前 `backend/internal/agent` 还具备 required/type 参数预检、path auto-heal、terminal failure circuit、empty-result negative cache；`ToolBroker` 提供 spawn、team、approval bridge、mailbox/events、worktree apply/discard 和 supervision。Reasonix 的价值是把这些属性纳入统一契约和调度，而不是替换这些实现。

### 6.4 已出现的收敛起点

`backend/internal/aiclitools` 已经提供一个较小的 cross-path 能力抽象：

- `Capability` 包含 Name、Description、Parameters、Metadata、Exposure 和 Execute；
- `Registry` 支持按 `shared`、`actor`、`runtime_server` 过滤；
- `FunctionFromCapability` 和 `CapabilityMCPManager` 将同一能力投影到 FunctionCatalog 与 MCPManager；
- `backend/internal/goal/capability.go` 已用它收敛 `get_goal/update_goal`。

它说明当前项目可以采用“先 adapter 化、再统一 descriptor”的渐进路线。缺口是它尚未表达强类型安全 traits、Schema revision、canonical JSON、来源身份、alias 冲突和稳定 contract snapshot。

## 7. 差距与风险矩阵

| 维度 | Reasonix 做法 | 当前项目现状 | 风险 | 建议优先级 |
| --- | --- | --- | --- | --- |
| 基础副作用契约 | `Tool.ReadOnly()` 固定在基础接口 | 主要依赖 metadata 和 `knownToolTaxonomy`；缺失时有名称 heuristic | 并行、plan、权限可能使用不同判断 | P1 |
| Schema 表示 | `json.RawMessage`，Add 时 canonicalize | `map[string]interface{}` 多次转换，外部 Schema 只做浅层修复 | 类型丢失、map 可变、provider 400 | P0 |
| Schema 验证 | JSON Schema compile、无外部 loader、坏工具隔离 | 尚未发现统一 compile/quarantine；调用参数校验不等于 Schema 验证 | 一个坏 MCP 工具拖垮整个请求 | P0 |
| Schema 方言 | provider-specific legacy tuple 转换 | 各路径自行归一化 | 不同 provider 对同一 Schema 处理不一致 | P1 |
| MCP 命名 | `mcp__server__tool` canonical name | registry key 为 `${mcp}_${tool}`；manager 按短名遍历 | 同名工具静默选择首个，调用身份不稳定 | P0 |
| Alias 解析 | 精确名优先，唯一 alias 才执行，歧义返回候选 | `FindTool` 返回首个同名结果，`seen[name]` 静默丢弃后续 | 错误 server 被调用且难以审计 | P0 |
| Registry 事实源 | 每次运行单一 Registry，带 revision | toolkit Registry、MCP Registry、Manager、AgentAdapter、Broker、FunctionCatalog、ToolBroker 并存 | schema/metadata/执行身份漂移 | P1 |
| provider contract 与 context projection | `Schemas` 和 `SchemasForContext` 分离 | 已有 snapshot、should_list、search projection，但概念分散 | 提示可见与执行可用不一致 | P1 |
| 解析与执行 | `ResolvedCall` 先得到真实目标，再统一授权/hook/evidence | 多路径按 name 直接查找和执行；aiclitools 只覆盖部分能力 | proxy 的真实副作用可能漏过 policy | P1 |
| 阻断语义 | `BlockedError` 与执行 error 区分 | `ToolResult.Success`、`ToolResult.Error`、外层 error 并存 | UI、loop guard、重试对失败分类不一致 | P1 |
| 批处理 | 连续只读段并行，writer/dependency barrier，顺序回填 | 仅整批 MCP-only 并行；不满足任一条件即整批串行 | 安全但吞吐和局部诊断能力不足 | P1/P2 |
| preview 一致性 | writer 后刷新后续 preview | 当前已有部分 preview/permission 流程，但缺少统一批处理屏障 | 审批 diff 过时 | P2 |
| subagent 结果 | 稳定 ref、分页、lineage/workspace 校验 | 已有 task output、events、mailbox，但结果读取契约不统一 | 长结果截断或串线 | P2 |
| contract 测试 | built-in 文档、Schema、read-only、输出策略锁定 | 有 surface parity/fingerprint 测试，但定义分散 | 新工具可见但 schema/metadata 漂移 | P0/P1 |
| provider 错误诊断 | 第 N 个工具错误映射回 identity/server/raw name | 尚未发现同等通用回溯 | 400/422 只能看到 provider index | P0 |

### 7.1 P0：MCP 同名冲突是确定性安全问题

当前 `backend/internal/mcp/registry/registry.go:232` 的内部 key 虽然包含 MCP 名称，但 `backend/internal/mcp/manager/manager.go:333` 的 `FindTool(toolName)` 遍历工具并返回第一个匹配项；`backend/internal/tools/manager.go:73` 又使用 `seen[info.Tool.Name]` 去重。结合 `ListTools` 的排序，同名工具可能由字典序靠前的 server 获胜，且没有歧义错误。

`backend/internal/mcp/registry/registry.go:83` 的 `ListTools` 先按工具名、再按 MCP 名排序，`FindTool` 在该列表上取第一个匹配，因此同名短名最终落在 MCP 名最小的 server 上；`tools.Manager.ListTools` 随后再次静默去重。

这不是纯粹的命名风格问题：模型调用的名字、授权的 server、执行的 server 和日志中的来源可能不一致。必须先引入 canonical namespace，再提供显式 alias 兼容期；alias 多候选时返回可行动的候选列表并拒绝执行。

### 7.2 P0：外部 Schema 的浅层修复不够

当前 `backend/internal/tools/manager.go:605`、`backend/internal/agent/loop.go:3131` 和 `backend/cmd/aicli/commands/chat_tool_definitions.go` 主要补 `type=object`、`properties` 和 `additionalProperties=false`。这可以修复一部分常见空 Schema，但无法发现：

- 非法 `required` 类型；
- 无法解析或越权的 `$ref`；
- 非法组合关键字、错误数组 tuple 形式和 provider dialect 差异；
- 根节点不是 object、Schema JSON 本身多值或损坏。

参数 preflight 只针对一次调用参数，不等价于 Schema 编译验证。应在 MCP/外部能力注册边界做一次 canonicalize + compile；验证失败的工具进入 quarantine，并把来源、原始 Schema hash 和错误原因纳入诊断。

### 7.3 P1：多事实源导致工具契约漂移

当前同时存在 `toolkit.Registry`、MCP registry、`tools.Manager`、`AgentAdapter`、`skill.MCPManager`、`ToolBroker.Definitions`、`aicliFunctionCatalog`、`types.ToolDefinition` 和 `aiclitools.Registry`。每次转换都可能改变：

- 参数 Schema 的 map 类型和默认字段；
- metadata 的 key 和来源；
- MCP server/tool 身份；
- 是否可并行、是否只读、是否可在 plan mode 执行；
- 输出类型和错误分类。

应以 descriptor 作为唯一事实源，适配器只做“协议投影”，不要在每个 adapter 重新推断工具属性。

## 8. 值得借鉴的目标架构

### 8.1 强类型 descriptor 与 traits

建议在 `backend/internal/tools` 或新的共享 capability 包中定义与当前项目兼容的最小模型：

```go
type ToolDescriptor struct {
    ID          ToolID
    Name        string
    Description string
    Version     string
    Schema      json.RawMessage
    Traits      ToolTraits
    Source      ToolSource
}

type ToolTraits struct {
    Kind             ToolKind
    ReadOnly         bool
    MutatesFS        bool
    RequiresNetwork  bool
    ParallelSafe     bool
    RetryClass       RetryClass
    EmptyReplayCache bool
    PlanMode         PlanModeDisposition
}

type Tool interface {
    Descriptor() ToolDescriptor
    Execute(context.Context, json.RawMessage) (*toolkit.ToolResult, error)
}
```

实施注意：

- `Schema` 使用 canonical `json.RawMessage`；旧 `Parameters() map[string]interface{}` 工具通过 adapter 包装，不能一次性修改现有约 27 个 toolkit 工具文件（含支持文件）。
- `ToolTraits` 是 policy、scheduler、surface 和 contract test 的共同输入；`DefinitionMetadata()` 仅作为迁移期兼容读取，不再是长期事实源。
- `Source` 至少包含 built-in、toolkit、MCP package/server/raw name、broker、skill、runtime-server 和 capability ID。
- `PlanModeDisposition` 不能由 `ReadOnly` 推导；例如 `complete_step` 可能只读但不应在 planning 阶段运行。
- `RetryClass` 和 `EmptyReplayCache` 保留当前项目已有 taxonomy 语义，避免重复发明一套重试策略。

### 8.2 唯一 CapabilityRegistry，多个协议投影

目标调用关系：

```text
CapabilityRegistry
   |
   +-- ProviderProjection     -> types.ToolDefinition / OpenAI / Anthropic
   +-- FunctionProjection     -> aicli FunctionCatalog
   +-- MCPProjection          -> skill.ToolInfo / MCPManager
   +-- BrokerProjection       -> ToolBroker runtime-owned tools
   +-- RuntimeServerProjection-> remote contract
   +-- DiagnosticProjection   -> surface/fingerprint/contract snapshot
```

Registry 必须提供：

1. `Register`：trim/校验 ID，canonicalize 和 compile Schema，记录来源和 revision；
2. `ResolveExact`：只按 canonical ID 查找；
3. `ResolveAlias`：只返回唯一候选，多候选带候选列表并拒绝执行；
4. `Definitions(snapshot)`：返回稳定排序、不可变 Schema bytes 的 provider 合约；
5. `DefinitionsForContext(ctx)`：返回 host contextual projection，不能改变 provider cache lane 的身份；
6. `Revision`/`Fingerprint`：工具集合变化、Schema 变化、metadata 变化均可观测；
7. `Quarantined`：坏 Schema、重复 canonical ID、非法来源保留诊断但不进入 provider 请求。

Registry 的锁边界应遵循 Reasonix `ContractEntries` 的经验：先在锁内抓取工具指针和 canonical bytes，释放锁后调用可能触发 lazy MCP 的方法，避免 Registry lock 与连接/交换锁形成 AB-BA 死锁。

### 8.3 canonical identity、alias 和执行身份

建议 MCP canonical name：

```text
mcp__<portable-server>__<portable-tool>
```

并在 descriptor 中保留：

```text
Source.Server       原始 server 名
Source.RawToolName  server-local 原始工具名
Source.VisibleName  skill/用户可读短名
Source.CapabilityID 稳定审计 ID
CanonicalName       当前 provider 可见、可执行名称
Aliases             仅供 host 解析，不自动注入 provider schema
```

解析规则：

- canonical exact match 优先；
- alias 只有一个候选才允许；
- alias 多候选返回 `ambiguous_tool`，列出 canonical 候选和 server；
- alias 不得改变 descriptor 的安全属性；
- provider transcript 记录 `DisplayName`，授权、hook、evidence、mutation 记录 `TargetName` 和 `CapabilityID`。

迁移期间可接受旧短名，但必须：记录 deprecation、只在唯一时解析、同名时 fail closed，并在日志/结果中提示 canonical 名称。

### 8.4 Schema 生命周期

建议 Schema 进入 Registry 后经过以下状态：

```text
raw external schema
    -> parse single JSON value
    -> canonicalize (generic)
    -> provider dialect projection (per request/provider)
    -> compile validate with no external loader
    -> accepted | quarantined
    -> immutable snapshot + hash
```

规则：

- 空值、`null`、缺失 root type 的 object Schema可按兼容策略修复；根类型明确非 object 时 quarantine；
- `$ref` 默认只允许内置 metaschema，禁止访问文件和网络；
- 无效工具不影响同一请求的其他工具；
- provider 400/422 如果包含工具序号，映射到 descriptor identity、MCP server、raw name 和 Schema hash；
- generic canonical bytes 与 provider dialect bytes 分开缓存，不能用 provider-specific 修复污染跨 provider cache；
- Schema hash 加入 tool surface fingerprint，修复 Schema 后旧 session 的 surface 变化可观测。

### 8.5 Resolve、Authorize、Execute 分离

建议把当前 Agent 的调用管道显式建模为：

```text
ResolveCall(display name, args)
  -> ResolvedTarget(canonical name, descriptor, normalized args)
  -> Context/availability gate
  -> Policy + permission + hook revalidation
  -> Mutation/lease/checkpoint/evidence preparation
  -> Execute target
  -> NormalizeOutcome(blocked | failed | success)
  -> OutputGateway + transcript projection
```

约束：

- Resolve 不应默认产生不可逆副作用；lazy MCP connect 若需要主机状态改变，应单独标记并走 host authorization；
- patched args 在 permission engine 后必须重新按 descriptor Schema 验证；
- 真实 target 的 traits 必须覆盖 proxy 声明的默认 traits；
- `Blocked`、`NotRun/DependencySkipped`、`ExecutionFailed`、`Succeeded` 使用稳定 outcome kind，避免只靠 `Success` 与 error 双重推断；
- provider 输出只携带有界内容，完整输出放在 run-scoped artifact 或 result ref 中。

## 9. 分阶段实施方案

### Phase 0：契约盘点和测试基线（低风险）

**目标**：不改变用户可见行为，先建立所有工具定义的可比较快照。

**工作项**：

1. 从 toolkit Registry、MCP Manager、Broker、FunctionCatalog、aiclitools Registry 和 runtime-server 收集工具定义，输出统一诊断表：canonical/legacy name、来源、Schema hash、metadata、path exposure、是否执行器可用。
2. 盘点所有工具的 `read_only`、`mutates_fs`、`requires_net`、`supports_parallel`、`retry_class` 和 plan-mode 语义，列出“metadata 缺失但名称 heuristic 生效”的条目。
3. 建立 contract snapshot 测试：名称、description、canonical Schema、来源、traits、输出策略；测试使用稳定排序和 JSON canonical bytes，避免 map 插入顺序造成误报。
4. 建立跨路径 parity 测试：同一 capability 在 shared、actor、runtime-server 的暴露、Schema 和执行结果 metadata 必须有明确差异说明。
5. 记录当前 MCP 同名工具、空 Schema、非法 Schema、重复 alias 和 provider 400/422 的现状样本。

**验收**：

- 所有当前内置、Broker、MCP fixture、skill 和 aiclitools capability 都能生成快照；
- snapshot 变化必须在 PR 中显示具体工具和字段；
- 不修改旧 provider 请求的工具顺序和名称；
- 测试能明确区分“定义不可见”“定义可见但执行不可用”“执行失败”。

### Phase 1：P0 Schema 可靠性与 MCP 身份

**目标**：先处理会造成整体请求失败或错误工具执行的风险。

#### 1.1 建立统一 Schema package

建议新增 `backend/internal/toolschema`（名称可按现有包边界调整），提供：

```go
Canonicalize(raw json.RawMessage) (canonical json.RawMessage, warnings []Warning, err error)
Validate(canonical json.RawMessage) error
ProjectForProvider(canonical json.RawMessage, dialect ProviderDialect) json.RawMessage
```

实现参考 Reasonix `internal/provider/schema_canonicalize.go`、`schema_validate.go`、`schema_dialect.go`，但以当前项目 provider adapter 的方言能力为准。所有 MCP/外部工具只在进入 registry 时 canonicalize + validate 一次；内置工具也通过同一路径生成 snapshot。

#### 1.2 quarantine 和诊断

Registry 注册失败不应 panic，也不应让其他工具消失：

- `QuarantinedTool` 记录 source、server、raw name、schema hash、error、first seen time；
- provider projection 默认排除 quarantine；
- `list_mcp_resources` 或 debug diagnostics 可列出隔离原因；
- provider index error 用当前 request 的有序 descriptor 列表映射回具体来源。

#### 1.3 MCP canonical namespace

改造顺序：

1. 在 MCP registry/manager 内部先生成 `mcp__server__tool` canonical ID，不立即移除旧短名；
2. `ToolInfo`、AgentAdapter、tools.Manager 和 ToolBroker adapter 全部携带 canonical name 与 source metadata；
3. provider 默认只暴露 canonical name；
4. 旧短名进入 host alias resolver，唯一候选兼容，多个候选返回歧义错误；
5. 为同名 fixture、server 名含特殊字符、tool 名含特殊字符、包名冲突补测试；
6. 最后再决定是否删除旧短名 provider 兼容，不能让日志或回放依赖短名。

**Phase 1 验收**：

- 任一非法外部 Schema 不会让同批有效工具消失；
- `$ref` 校验不会访问本地文件或网络；
- 两个 server 注册同名工具时，provider 能同时看到两个 canonical names；
- 短 alias 歧义时调用不执行任何 server，并返回候选列表；
- provider 报 tool index 错误时诊断包含 canonical name、MCP server、raw name 和 Schema hash。

### Phase 2：强类型 descriptor 与 Registry 单一事实源

**目标**：用 adapter 渐进收敛多条定义路径，不一次重写所有工具。

#### 2.1 先包旧接口

新增 `LegacyToolkitAdapter`：

```text
toolkit.Tool
  -> clone Parameters map
  -> toolschema.Canonicalize/Validate
  -> infer/explicit ToolTraits
  -> ToolDescriptor + Execute adapter
```

旧 `ToolResult` 保留，adapter 负责把 `Success/Error/OutputKind` 正规化成统一 outcome。对于暂时无法确定的 `MutatesFS`、`ParallelSafe`、`PlanMode`，必须使用保守值并产生 diagnostics，而不是静默乐观。

#### 2.2 Broker 拆定义与 handler

`ToolBroker` 目前约 3969 行，`Definitions` 和执行分发集中在 `broker.go`，且部分 spawn-agent 定义依赖 TeamStore 分支重复构造。建议按工具拆成：

```text
toolbroker/definitions/ask.go
toolbroker/definitions/task.go
toolbroker/definitions/team.go
toolbroker/definitions/plan.go
toolbroker/handlers/ask.go
toolbroker/handlers/task.go
...
```

每个文件导出 descriptor + handler；Broker 只负责生命周期依赖注入、路由和结果适配。迁移期间保留 `Broker.Definitions()`，改为从 descriptor 集合生成，避免一次改动所有调用方。

#### 2.3 aiclitools 与统一 Registry 对齐

将 `aiclitools.Capability` 的 `Parameters map` 替换为 canonical Schema，同时兼容旧字段；`Exposure` 继续作为 projection filter；增加 `Traits`、`Source`、`Aliases`、`Revision`。`goal` 作为首个完整 capability 迁移样板，随后再迁移低耦合 Broker 工具和 runtime-server 工具。

**Phase 2 验收**：

- 新增工具只需注册一次 descriptor，至少能生成 shared/actor/runtime-server 的 projection；
- FunctionCatalog、MCPManager、Broker 和 provider request 的名称、Schema hash、traits 可追溯到同一个 descriptor ID；
- 旧工具测试无需大面积重写，Legacy adapter 通过 parity tests；
- Registry revision/fingerprint 在工具新增、删除、Schema/metadata 变更时稳定变化。

### Phase 3：Resolved target、context gate 和统一执行结果

**目标**：让 proxy、contextual tool 和多 executor 共享同一安全顺序。

#### 3.1 引入 resolved call

在当前 `types.ToolCall` 上增加 host-local 的 resolved metadata，或在 Agent 内部引入独立结构：

```go
type ResolvedToolCall struct {
    DisplayName    string
    TargetName     string
    CapabilityID   string
    Descriptor     ToolDescriptor
    Args           json.RawMessage
    SkipExecute    bool
    Unavailable    bool
    UnavailableWhy string
}
```

执行顺序必须固定为：

1. exact/alias/canonical resolve；
2. 读取真实 descriptor 和 context availability；
3. policy capability scope、permission、approval、sandbox；
4. patched args 再次 Schema/type/path 校验；
5. hook、workspace claim、checkpoint、evidence；
6. handler execute；
7. `Blocked`、`DependencySkipped`、`Failed`、`Succeeded` 统一结果分类；
8. OutputGateway 生成 provider-visible bounded output。

`ContextualTool` 的借鉴方式是：provider contract 可以保持稳定，host projection 可按 turn/context 过滤，但执行前必须再次检查当前 session、executor、MCP enabled、server authorization 和 permission scope。stale transcript 不得绕过 gate。

#### 3.2 统一结果 envelope

当前 `ToolResult` 继续作为 payload，但增加内部 outcome envelope：

```text
status: success | blocked | failed | not_run | unavailable
reason: stable machine code
display_output: bounded text/structured content
raw_ref: optional artifact/result reference
source/capability_id/target_name
```

provider 消息只取 display output；debug、event、UI 使用完整 metadata。这样可保留现有 binary/MIME，同时让 loop guard、重试、依赖屏障和前端对“拒绝”和“失败”有一致解释。

**Phase 3 验收**：

- 代理工具的 permission、hook、evidence、mutation 记录真实目标；
- context 切换或 MCP disable 后，旧 transcript 调用在执行前 fail closed；
- patched args 无法绕过 descriptor Schema、path 或 capability scope；
- blocked、not-run、execution-failed 在事件和 provider tool result 中可区分；
- output truncation 不丢失 structured/binary metadata，完整结果可通过 ref 或 artifact 追溯。

### Phase 4：分段并行、依赖屏障和 preview 一致性

**目标**：在现有安全 permission/preflight 的前提下提高只读吞吐。

#### 4.1 调度规则

把当前 `buildParallelToolBatchPlan` 的整批判定扩展为分段计划：

- 连续已知、canonical、`ReadOnly && ParallelSafe` 且同一 MCP 并发限制允许的调用组成并行段；
- unknown、ambiguous、proxy、writer、shell、Broker state tool、evidence-order-sensitive tool 单独串行；
- 每个并行调用仍执行 hard constraints 和 final policy revalidation；
- 结果、event、tool message 按原始 call index 回填，不能按完成先后改变 provider 顺序；
- 全局并发和 per-server `MaxParallelCalls` 同时生效；
- 任一 mutation 失败后，后续 mutation/verification 标记 `dependency_not_run`，只读诊断仍可执行；
- writer 成功或可能改变工作区后，刷新后续 writer preview；
- cancel、budget stop、session close 时为每个尚未执行调用补齐 not-run result。

#### 4.2 迁移策略

先将调度器输入从 `ToolInfo` 切换为 `ToolDescriptor`，保留现有“有 hook/custom permission 则整体串行”的开关；当 descriptor traits 和 hard revalidation 覆盖率达到验收标准后，再允许“有 hook 但只读段仍可并行”的配置。

**Phase 4 验收**：

- 混合批次中的只读调用并行，写调用仍按 provider 顺序执行；
- 结果顺序、证据 receipt 顺序、UI card 顺序稳定；
- MCP per-server limit 不被全局并发覆盖；
- mutation 失败不会启动后续写入或验证，但不会阻塞安全的只读诊断；
- 并行路径与串行路径的 policy/hook/preflight/metadata 结果一致。

### Phase 5：Agent-native 工具增强

**目标**：在契约和执行边界稳定后，再增强模型编排体验。

建议顺序：

1. `ask_user_question`：结构化 question/options/recommended/multi-select/dismiss/headless assumption，沿用当前 approval bridge；
2. `read_only_task` 与写型 `task` 分离 Schema，写型任务声明 write paths，allowlist 只求交；
3. `read_subagent_result(ref, offset, limit)`：分页、UTF-8 边界、lineage/workspace 校验；
4. `submit_plan`：objective、assumptions、non-goals、steps、depends_on、acceptance、verification、risk；
5. `parallel_tasks/fleet`：输入上限、DAG/path 冲突预检、fail-fast 和公平 preview budget；
6. lazy `use_capability`：list/inspect 不启动 MCP，call 才连接，固定 provider Schema。

**Phase 5 验收**：

- 用户 dismiss 不会被模型隐式解释为选择某个选项；
- 只读任务在 plan mode 可运行，写型任务需要明确权限和路径；
- 多 Agent 结果不会因固定 tool-result 长度而丢失，越权 ref 被拒绝；
- DAG/path 冲突在任何子任务启动前失败；
- 动态 MCP inventory 不改变固定 proxy Schema 或 provider cache key。

## 10. 文件级改造建议

| 阶段 | 首选文件/目录 | 建议动作 | 兼容要求 |
| --- | --- | --- | --- |
| P0 | `backend/internal/toolschema/`（建议新包） | canonicalize、compile validate、dialect projection、schema hash、quarantine 类型 | 不改变现有 toolkit.Tool 接口 |
| P0 | `backend/internal/mcp/registry/registry.go` | 增加 canonical MCP identity、source metadata、duplicate/alias index | 保留内部 key 兼容读取，禁止静默覆盖 |
| P0 | `backend/internal/mcp/manager/manager.go` | `FindTool` 改为 exact canonical；新增 unique alias resolve 和 ambiguity error | 旧短名仅 host 兼容，不进入默认 provider surface |
| P0 | `backend/internal/tools/manager.go`、`agent_adapter.go` | 消除 `seen[name]` 首个胜出逻辑，统一 schema adapter 和来源字段 | local toolkit priority 规则保留并显式记录 |
| P0 | `backend/internal/agent/loop.go`、provider adapters | 统一 Schema 入口和 provider index 错误回溯 | 保持现有 surface snapshot/fingerprint 语义 |
| P0/P1 | `backend/internal/agent/*_test.go`、`backend/internal/tools/*_test.go` | contract snapshot、MCP collision、invalid schema、provider error mapping | fixture 必须覆盖坏工具隔离 |
| P1 | `backend/internal/tools/descriptor.go` 或 capability 包 | 定义 `ToolDescriptor/ToolTraits/ToolSource/Outcome` | Legacy adapter 先行，禁止一次迁移全部工具 |
| P1 | `backend/internal/aiclitools/*` | Capability 使用 descriptor、traits、source、revision；保留 Exposure projection | `goal` 作为 pilot |
| P1 | `backend/internal/toolbroker/` | Definitions 拆为 descriptor + handler，Broker 保留路由 facade | 不改变现有 tool name 和结果字段 |
| P1 | `backend/internal/agent`、`internal/policy` | resolved target pipeline，真实 target revalidation，blocked/not-run outcome | 复用现有 permission engine 和 OutputGateway |
| P1/P2 | `backend/internal/agent/tool_parallel_scheduler.go`、`execute` 相关文件 | 从整批并行改为连续只读段、dependency barrier、顺序回填 | 默认保守开关，支持回退串行 |
| P2 | `backend/internal/toolbroker`、`backend/internal/agent` | stable subagent result ref、分页、DAG/path preflight、structured ask/plan | 与现有 AgentControl/mailbox 共存 |
| 文档 | `docs/plan/aicli-tool-capability-convergence-plan.md` | 增补本方案的 Schema/identity/registry 约束和阶段依赖 | 保持其 shared/actor/runtime-server 收敛方向 |

### 10.1 不建议立即改动的文件

- 不要先改所有 `backend/internal/toolkit/tools/*` 实现签名；先用 adapter 证明 descriptor parity。
- 不要先重写 `ToolBroker` 的业务 handler；先把 Definitions 从 Broker 大文件投影成 descriptor，再逐步拆 handler。
- 不要先删除 `FunctionCatalog`、`MCPManager` 或 runtime-server adapter；它们在迁移期仍是协议边界和回滚点。
- 不要在 P0 顺带改变 `search_tool` threshold、session snapshot 或 provider tool order，这些属于独立兼容面。

## 11. 测试矩阵与验收标准

### 11.1 Schema 测试

| 场景 | 断言 |
| --- | --- |
| 空字节、`null`、`{}`、缺 root `type` | canonical 为合法 object Schema，带 properties |
| root type 为 string/array/数字 | 注册失败并 quarantine，不进入 provider 请求 |
| `required: true`、数组包含重复/非字符串 | 按兼容规则修复或拒绝，结果可诊断 |
| properties、`$defs`、definitions 嵌套 Schema | canonical 稳定，字段排序不受 map 插入顺序影响 |
| `$ref` 指向 file/http | compile 失败，不产生文件/网络访问 |
| legacy tuple + provider dialect | 仅目标 provider projection 改变，通用 canonical bytes 不漂移 |
| 一个坏工具和多个好工具同批 | 好工具仍生成 provider definitions |
| provider 返回 tool index 400/422 | 映射 canonical name、source server/raw name、schema hash |

### 11.2 身份、alias 和生命周期测试

| 场景 | 断言 |
| --- | --- |
| 两个 MCP server 同名 tool | 两个 canonical names 均存在，无静默覆盖 |
| exact canonical 与 alias 同时命中 | exact canonical 优先 |
| alias 唯一 | 返回唯一 target，target traits 保持不变 |
| alias 多候选 | 返回 ambiguity + 候选列表，零 server 调用 |
| server disconnect/reconnect | namespace remove/suspend/resume 不把旧工具复活到禁用 session |
| provider name 特殊字符 | portable name 稳定区分，原始名称可通过 source metadata 追溯 |
| stale transcript/context disable | execute 前再次 gate，返回 unavailable/blocked |

### 11.3 执行与结果测试

| 场景 | 断言 |
| --- | --- |
| proxy 指向 writer | permission/hook/evidence 使用真实 target，而非 proxy 名 |
| permission patched args | patched args 经 Schema 和 hard policy revalidation |
| host blocked | outcome 为 blocked/not executed，loop guard 和 UI 不当成 execution failure |
| handler 返回 binary/structured | OutputGateway 保留 kind/MIME/metadata，模型文本有界 |
| context availability 变更 | provider surface 可稳定但执行 fail closed |
| execute panic/外层 error 与 ToolResult.Error 不一致 | 统一 outcome，不出现双重成功 |

### 11.4 并行和依赖测试

| 场景 | 断言 |
| --- | --- |
| 连续只读调用 | 并行执行，结果按原始 index 回填 |
| 只读 + writer 混合 | 只读段并行，writer 串行，不越过 provider 顺序 |
| 不同 MCP server limit | 各 server gate 生效，全局上限也生效 |
| writer 失败后 writer/verification | 标记 dependency not-run，不启动执行 |
| writer 失败后 read-only diagnosis | 仍可运行 |
| writer 前后 preview | 后续 preview 反映最新 workspace state |
| cancel/budget stop | 每个调用有完整 tool/result 对，未执行项有稳定 not-run reason |

### 11.5 Agent-native 工具测试

- `ask` 的问题/选项边界、重复 label、recommended、多选、dismiss、headless assumption；
- `read_only_task` Schema 不暴露写型/后台参数，Plan mode 可运行；
- `task` 的 write path、profile/allowlist 求交和 continuation reference；
- `parallel_tasks/fleet` 的数量上限、DAG 环检测、depends_on、重叠路径和 fail-fast；
- `read_subagent_result` 的 lineage/workspace、UTF-8 byte offset、分页和结果过期；
- `submit_plan` 的 verified/candidate files 和 acceptance/verification/risk 完整性；
- `use_capability` 的 list/inspect 不启动 server、call lazy connect、固定 Schema 和真实 target authorization。

## 12. 观测、兼容、迁移与回滚

### 12.1 需要记录的诊断字段

每次 provider request 和 tool execution 建议保留：

```text
tool_surface_fingerprint
registry_revision
descriptor_id / capability_id
display_name / canonical_name / target_name
source_kind / mcp_server / raw_tool_name
schema_hash / schema_validation_status
resolution_kind: exact | alias | ambiguous | unavailable
outcome_kind: success | blocked | failed | not_run
parallel_segment / dependency_reason
```

完整原始 Schema 和输出不直接塞进模型上下文；使用 hash、bounded excerpt 和 run-scoped artifact/ref。敏感参数仍遵守当前 redaction 和 permission logging 规则。

### 12.2 兼容开关

建议按独立开关逐步启用：

1. `tool_schema_validation`：shadow -> quarantine -> enforce；
2. `mcp_canonical_names`：diagnostic-only -> dual lookup -> provider canonical-only；
3. `tool_descriptor_registry`：shadow fingerprint -> adapter execution -> default registry；
4. `resolved_target_pipeline`：仅 proxy -> 所有 MCP -> 所有 tool；
5. `segmented_parallel_tools`：off -> read-only fixtures -> production default。

每个开关都应记录生效值和 registry revision，便于重放和回滚。

### 12.3 回滚原则

- Schema quarantine 误判时，可切换为 shadow 或仅隔离指定 source，不回滚其他工具定义；
- canonical name 兼容期保留旧 alias，但不能恢复“同名首个胜出”；
- descriptor adapter 出问题时回退到旧 Manager/Broker，contract snapshot 继续运行暴露差异；
- 分段并行出现时序问题时关闭开关回到当前整批串行/MCP-only 策略；
- 不使用破坏性 git 回滚覆盖工作树中的用户改动，所有迁移通过独立提交和开关控制。

## 13. 推荐启动顺序与预期收益

### 13.1 推荐启动顺序

1. **Phase 0**：先建 contract inventory、parity 和 snapshot 测试，为后续所有阶段提供回归基线；
2. **Phase 1**：先做 Schema canonicalize/validate/quarantine 和 MCP canonical namespace，因为它们风险最高且独立于大重构；
3. **Phase 2**：以 `goal` 为 pilot 引入 `ToolDescriptor/ToolTraits`，再通过 adapter 覆盖旧 toolkit 和 Broker 工具；
4. **Phase 3**：在 descriptor 稳定后接入 resolved target pipeline 和统一 outcome；
5. **Phase 4**：在 permission/hook/preflight parity 测试通过后逐步放开分段并行；
6. **Phase 5**：最后做 Agent-native 工具增强，避免在新契约未稳定时同时扩大编排面。

### 13.2 预期收益

- 外部 MCP 坏 Schema 不再拖垮整次 provider 请求，400/422 可快速定位到来源 server 和原始工具；
- 同名 MCP 工具不再静默覆盖或误执行，模型调用、授权、执行和审计使用同一 canonical 身份；
- shared、actor、runtime-server 三条路径的工具定义、Schema 和安全 traits 收敛到单一事实源，新增工具只声明一次；
- 代理工具、contextual projection、权限 patched args 和 mutation 分类获得统一顺序，减少 stale transcript 绕过；
- 只读调用吞吐提升，同时保留 writer 顺序、失败依赖屏障、preview 一致性和每调用结果回填；
- subagent、plan、ask 等编排工具获得结构化契约，降低长结果丢失和越权引用风险。

### 13.3 复核说明

本方案基于 2026-08-11 两个仓库的源码快照：

- Reasonix HEAD `ed00eccf3`；
- 当前项目 HEAD `a54fa5e`，并保留工作树中用户已有改动；
- 关键行号在文档撰写时已核对，代码改动后行号可能偏移，应以文件和函数名复核；
- 初始分析阶段仅新增本文档；随后已按第 15 章实施首轮 P0 优化并完成定向、静态、竞态和全量回归验证。

## 14. 最终判断

Reasonix 的工具定义体系值得借鉴的是“契约先行、身份稳定、Schema 可靠、执行可审计”的工程方法，而不是它的具体工具集。当前项目的结构化结果、权限引擎、工具面稳定性和多 Agent 控制面已经更强，因此正确做法是在保留这些能力的前提下，先补齐 P0 的 Schema 与 MCP 身份边界，再以 descriptor 收敛多事实源，最后用分段并行和 Agent-native 契约增强编排体验。

## 15. 首轮实施与验证结果

### 15.1 本轮实际完成范围

本轮落地的是 Phase 1 中可独立实施、能直接降低 provider 整体失败和 MCP 误路由风险的部分，并补充了 Toolkit Schema 快照。没有宣称完成全文所有 Phase。

1. 新增 `backend/internal/toolschema`，统一完成 Schema 深拷贝、canonicalize、编译验证、canonical JSON 和 SHA-256 hash。缺失根 `type`、缺失 `properties`、缺失 `additionalProperties`、错误的 `required: true` 和 `dependentRequired` 可按兼容规则修复；显式非 object 根类型拒绝。
2. Schema 遍历按 JSON Schema 关键字位置执行，保留名为 `required`/`dependentRequired` 的普通属性，也不改写 `enum`、`default`、`examples` 中的实例数据。
3. 未声明 `$schema` 时按 draft-07 编译，兼容 legacy tuple `items: [...]`；本地 `$defs/$ref` 可用，外部文件和网络 `$ref` 通过禁用 loader 拒绝。
4. MCP Registry 在注册边界 canonicalize + validate。坏工具进入 quarantine，不进入 callable inventory；同批有效工具继续加载。诊断保留 server、raw tool name、canonical name、Schema hash 和错误。
5. MCP 生命周期新增 `mcp.tool.quarantined`，`mcp.tools.loaded` 同时记录 advertised、loaded、quarantined 数量。
6. Registry 内部 key 改为 `server + NUL + raw tool`，消除旧 `${mcp}_${tool}` 拼接碰撞。注册、查询、列举返回隔离 Schema 快照，调用方不能修改 Registry 内部 canonical Schema。
7. MCP canonical identity 使用 `mcp__<portable-server>__<portable-tool>`。合法名称保持原样；需要替换特殊字符时追加稳定 FNV-1a 短哈希，避免 `server.name` 与 `server_name` 归一化碰撞，原始名称继续保存在 metadata。
8. 短名只有唯一候选时兼容；多个 server 的同名短名返回带排序候选的 `AmbiguousToolError`，不调用任何 server。exact canonical identity 优先。
9. callable name 投影同时处理“两个 raw name 相同”和“某个 raw name 等于另一工具 canonical name”两类冲突，避免生成可见但会误路由的工具名。
10. Manager、`tools.Manager`、Skill MCP adapter 和 Catalog gateway 使用同一 callable-name 规则，并在执行前把 canonical name 还原成 server-local raw name。已登记但 disabled 的工具不能通过显式 server 调用绕过。
11. Toolkit Registry 注册时只读取一次 `Parameters()`，保存 canonical Schema 快照并维护 `SchemaRevision()`；Tool schema、MCP adapter、Toolkit MCP server 和统一 tools Manager 的投影均读取深拷贝快照。
12. Agent loop 与 CLI tool definition 的旧浅层 Schema 修复改为复用 `toolschema.Canonicalize`，减少相同规则在不同路径漂移。

### 15.2 兼容策略

- 本轮没有立刻把所有唯一 MCP 工具的 provider-visible name 改成 canonical name。唯一 raw 短名仍保持旧名称；只有同名或 raw/canonical 交叉冲突时强制投影 canonical name。这样先消除确定性误执行，同时避免一次性破坏既有 session、transcript 和 allowlist。
- 显式 `mcpName + raw toolName` 的旧调用仍由 MCP Manager 做最终准入；尚未进入 inventory 的 raw 调用保持旧兼容。canonical 名必须成功解析，已登记但 disabled 的 raw 工具必须 fail closed。
- 特殊字符归一化不是从 provider name 反解原名，而是使用可读片段和稳定哈希避免碰撞，再通过 `mcp_name`、`mcp_raw_tool_name` metadata 做无损追溯。
- Toolkit 与 MCP 的 Schema 都在注册时形成快照；返回给投影层的是深拷贝，调用者的后续 map 修改不会改变 Registry 契约。

### 15.3 验证结果

以下命令在 `backend` 目录执行：

```powershell
go test -count=1 ./internal/toolschema ./internal/toolkit ./internal/mcp/registry ./internal/mcp/manager ./internal/tools ./internal/skill ./internal/mcp/catalog
go vet ./internal/toolschema ./internal/toolkit ./internal/mcp/registry ./internal/mcp/manager ./internal/tools ./internal/skill ./internal/mcp/catalog ./internal/agent ./cmd/aicli/commands
go test -race -count=1 ./internal/toolschema ./internal/toolkit ./internal/mcp/registry ./internal/mcp/manager ./internal/tools ./internal/skill ./internal/mcp/catalog
go test -count=1 ./...
go mod tidy -diff
git diff --check
```

结果：

- Schema、Toolkit、MCP Registry/Manager、tools、skill、catalog 定向测试全部通过；
- Agent 和 CLI 关键路径测试通过；
- 受影响包 `go vet` 通过；
- 受影响共享路径的 race 测试全部通过；
- `go mod tidy -diff` 无输出，依赖声明已整洁；
- `git diff --check` 通过；
- 后端全量回归除 `internal/background` 外全部通过，本轮所有修改包均通过。

`internal/background` 不在本次改动范围内。全量测试中有两个既有时序用例失败：

```text
TestWatchdogReclaimsStuckScheduledJob
TestReadOutputQueueDiagnostics
```

单独重复验证显示它们均为非确定性波动：watchdog 用例 5 次中 4 次通过、1 次失败；queue diagnostics 用例 5 次中 3 次通过、2 次失败。失败分别表现为作业被过早标记 `scheduler stuck`，以及预期队列诊断数 1、实际为 2。两者不引用本轮工具定义代码，工作树中也没有修改 `internal/background`。

### 15.4 独立模型审查实测（opencode.ai / deepseek-v4-flash / max）

用户指定使用 `opencode.ai` provider、`deepseek-v4-flash` 模型、`max` reasoning effort 对本轮改动做独立审查。实际执行命令（`backend` 工作目录外，仓库根目录）：

```powershell
aicli exec review --uncommitted --provider opencode.ai --model deepseek-v4-flash --reasoning-effort max --max-tokens 10000 --enable-tools --permission-mode plan --fail-fast --timeout 4m -c docs\plan\.aicli-review-opencode.yaml
```

实测结论：请求能到达 provider，模型确实读取了代码（日志出现多轮 `view`、`shell`），但多次运行均未产出正文：

```text
reasoning_only_empty_reply: finish_reason=length: reasoning_present=true: content_empty=true: tool_calls=0
```

已排除的影响因素：

- 非流式与流式均复现；
- `max_tokens` 10000 与本地调高到 32768 均复现；
- `reasoning_effort=high` 对照同样复现，说明不是 `max` 独有；
- `aicli test --provider opencode.ai --model deepseek-v4-flash --message hi --max-tokens 100 --output text` 在 120 秒内未返回正文；
- 第一次无工具运行报 `[TOOL_NOT_REGISTERED] fetch / openai_image_generate / bash / view`，是 CLI actor host 在 `--disable-tools` 下仍 DiscoverOnly 扫描 skill 的既有初始化路径问题；改用 `--enable-tools --permission-mode plan` 可读文件并保持只读权限。

因此，本轮无法从该 provider/模型组合获得可信的第三方审查结论；失败证据本身作为兼容性发现记录，后续可在 provider 修复后或改用 `deepseek-v4-pro` 后补跑。
该临时审查配置 `docs\plan\.aicli-review-opencode.yaml` 已在本轮结束后删除。

### 15.5 审查补充修复与回归验证

本地审查在 15.1-15.3 的基础上又发现并修复了两个身份边界缺口：

1. `ResolveToolForMCP` 之前先按 raw 名精确匹配，再匹配 canonical。同一个 MCP 内如果某个 raw 工具名恰好等于另一工具的 canonical 名（例如 raw `mcp__docs__search` 与 `search` 共存），显式 server 调用会命中 raw 别名，把本应执行 `search` 的 canonical 调用路由到错误工具。现已改为 canonical exact 优先、raw 名兼容在后，并让 Manager 与 Skill adapter 通过共享 `ExecutionLookupName` 保留 canonical-shaped raw 工具自身的可执行身份。
2. callable 投影只处理重名和 raw/canonical 遮蔽，唯一但包含空格、点号或超过 64 字符的 MCP raw 名仍会直接暴露给 OpenAI 兼容 provider，存在工具名校验失败风险。现统一强制投影为 `[A-Za-z0-9_-]{1,64}` 的 canonical 名，超长名使用稳定 SHA-256 截断后缀；原始名称继续保存在 metadata。

新增回归测试：

```text
TestResolveToolForMCPPrefersCanonicalIdentityOverRawAlias
TestCallableToolNamesCanonicalizeProviderUnsafeAndLongNames
TestMCPAdapter_CallToolWithMetaPrefersCanonicalIdentityOverRawAlias
TestMCPAdapter_CallToolWithMetaKeepsCanonicalShapedRawToolAddressable
```

本轮验证命令与结果：

```powershell
go test -count=1 ./internal/toolschema ./internal/toolkit ./internal/mcp/registry ./internal/mcp/manager ./internal/tools ./internal/skill ./internal/mcp/catalog ./internal/agent ./cmd/aicli/commands
go vet ./internal/toolschema ./internal/toolkit ./internal/mcp/registry ./internal/mcp/manager ./internal/tools ./internal/skill ./internal/mcp/catalog ./internal/agent ./cmd/aicli/commands
go test -race -count=1 ./internal/toolschema ./internal/toolkit ./internal/mcp/registry ./internal/mcp/manager ./internal/tools ./internal/skill ./internal/mcp/catalog
go test -count=1 ./...
go mod tidy -diff
git diff --check
```

- 定向、Agent、CLI、vet、race、tidy 全部通过；
- 全量回归除 `internal/background` 外全部通过（本轮未修改该包）；
- CLI 全量并行时出现的 `TestChatRuntimeEventBridge_CompleteResponseMatchesTranscriptRenderer` spinner ANSI 帧差异，独立 `-count=5` 复测全部通过，判定为并发测试输出竞争；
- `internal/background` 两个已知时序用例本次复测仍偶发失败：watchdog `-count=5` 失败 1 次，queue diagnostics `-count=5` 失败 2 次；全量运行时 queue diagnostics 再次失败，watchdog 通过。

### 15.6 尚未实施的原方案项目

以下项目仍属于后续 Phase，不能视为本轮已完成：

1. provider 方言投影与 generic/provider-specific Schema 双缓存；
2. provider 400/422 中 tool index 到 canonical identity、server/raw name、Schema hash 的自动回溯注释；
3. 跨 Toolkit、MCP、Broker、FunctionCatalog、runtime-server 的统一强类型 `ToolDescriptor/ToolTraits` 单一事实源；
4. 完整的 `Resolve -> Authorize -> Revalidate -> Execute -> NormalizeOutcome` 真实目标管道；
5. Schema hash 纳入所有 session surface fingerprint 和跨路径 contract snapshot；
6. 连续只读段并行、writer barrier、依赖 not-run 和顺序结果回填；
7. `ask`、`task`、`parallel_tasks/fleet`、`submit_plan`、`use_capability` 等 Agent-native 工具增强；
8. shadow/enforce feature flags、alias deprecation telemetry、quarantine first-seen 持久诊断与统一 debug UI。
