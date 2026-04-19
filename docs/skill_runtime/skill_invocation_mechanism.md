# Skill Runtime 中 AI 调用 Skill 的机制

## 问题定义

在当前仓库中，AI 调用 skill 并不是单一机制，而是两条不同的链路：

1. `aicli chat` 的 `skills-as-functions` 链路
2. `skills API / agent chat` 的 `route-first` 链路

如果只看 `skill.yaml`，会很容易产生两个疑问：

- AI 是怎么知道某个 skill 存在的
- `prompt` 是在哪里真正被用到的

这篇文档专门回答这两个问题。

## 先给结论

### 1. 在 `aicli chat` 中

模型不是直接读取 `skill.yaml`。

它感知 skill 的方式是：

- runtime 先加载 skills
- 每个 skill 被包装成一个 `skill__*` function
- function schema 被放进本轮模型请求
- 模型从这些 schema 中选择要不要发起 tool/function call

因此，模型看到的是：

- function `name`
- function `description`
- function `parameters`

而不是 manifest 文件本身。

### 2. 在 `POST /api/agent/chat` 中

后端通常会先 route skill，再决定是否调用 LLM。

也就是说，这条链路里很多时候是：

- 后端知道有哪些 skill
- 后端判断当前 prompt 命中哪个 skill
- 命中后直接执行 skill
- 没命中才 fallback 到 LLM

所以这里不一定存在“模型先感知 skill，再主动调用”的过程。

## Skill 元数据分别被谁使用

`skill.yaml` 中的字段，不是都给模型看的。

### 路由阶段使用

- `triggers`

主要作用：

- keyword route
- pattern route
- embedding route

这些字段主要给 `Router` 用，不会原样发送给模型。

### 模型感知阶段使用

- `name`
- `description`
- `category`
- `capabilities`
- `tags`
- `tools`

这些字段会被拼进 `SkillFunction.Description()`，用于生成 function/tool schema，发送给模型。

### 执行阶段使用

- `systemPrompt`
- `userPrompt`
- 可选 companion `prompt.md`
- `workflow`
- `workflow.steps[].args`

这些字段主要由 `Executor` 在真正执行 skill 时消费。

## 链路一：`aicli chat` 如何让模型感知并调用 skill

这是当前最直接的“AI 主动调用 skill”机制。

### 整体链路

```text
user prompt
-> analyze skill exposure
-> select candidate skills
-> build function schemas
-> send schemas to model
-> model returns tool_call(skill__xxx, args)
-> FunctionRegistry.ExecuteFunction
-> SkillFunction.Execute
-> skill.Executor.Execute
-> workflow / MCP / LLM
```

## 1. skill 被注册成 function

`aicli chat` 启动时会初始化 `FunctionRegistry`，然后调用 `initSkillFunctions()`。

这一阶段会：

- 加载 skills runtime
- 遍历当前 registry 中的 skill
- 把每个 skill 注册成一个 `SkillFunction`

对应代码：

- `cmd/aicli/commands/chat.go`
- `cmd/aicli/commands/skills_integration.go`

结果是：

- `run_shell_command` 会变成 `skill__run_shell_command`
- `view_file_content` 会变成 `skill__view_file_content`

## 2. 本轮 prompt 决定暴露哪些 skill

并不是每轮都把所有 skill schema 全量发给模型。

当前机制是 route-first：

- 先分析当前 prompt
- 用 `Router.Route()` 选出候选 skill
- 再按 `skills-top-k`、`skills-mode` 决定最终暴露哪些 skills

这一步里，`prompt` 的第一层用途出现了：

- `prompt` 用来做 route
- `triggers` 在这里参与 keyword / pattern / embedding 匹配

因此：

- `prompt` 先被用来“选 skill”
- 还没有真正进入 skill 执行

## 3. skill 被翻译成 function schema 发给模型

`SkillFunction` 会暴露三件事：

- `Name()`
- `Description()`
- `Parameters()`

其中：

- `Description()` 会把 skill 的 `name / description / category / capabilities / tags / tools` 组织成自然语言描述
- `Parameters()` 会暴露统一 schema：
  - `prompt`
  - `context`
  - `options`

然后 `buildRequestFunctionSchemas()` 会把这些 schema 交给协议适配 builder：

- OpenAI / Gemini
- Anthropic
- Codex

最后被放进模型请求中的 `tools` / `functions` 字段。

这就是模型“知道 skill 存在”的根本原因：

- 不是读 manifest
- 而是读 runtime 暴露出来的 function schema

## 4. 模型返回 tool call

当模型认为某个 skill 合适时，会返回：

- `tool_calls`
- 或协议对应的 function call 结构

`aicli` 收到响应后：

- 提取 `tool_calls`
- 转成内部 `functions.ToolCall`
- 交给 `FunctionRegistry.ExecuteFunction()`

此时才真正进入 skill 执行。

## 5. `SkillFunction.Execute()` 如何使用 `prompt`

这一步里，`prompt` 的第二层用途出现了。

`SkillFunction.Execute()` 会：

- 从 tool call 参数里取出 `prompt`
- 构造 `types.Request`
- 把 `context / options / history / metadata` 一起放进去
- 再调用 `Executor.Execute()`

因此，在 `aicli` 模式下：

- 第一次 `prompt` 用于 route 和暴露控制
- 第二次 `prompt` 作为 skill 执行参数真正传入 runtime

## 6. `Executor` 如何消费 `prompt`

### 默认 LLM skill

如果 skill 没有 workflow，而是走默认 LLM 模式：

- 如果 skill 目录下存在 `prompt.md`，runtime 会先尝试把它装载进 skill prompt
- 当 `prompt.md` 使用 `System` / `User` 分段时，会映射到 `systemPrompt / userPrompt`
- 如果 `prompt.md` 没有显式分段，当前默认整体作为 `SystemPrompt`
- `systemPrompt` 会进入 system message
- `userPrompt` 如果定义了，优先使用它
- 否则使用 `req.Prompt`

这意味着：

- skill 可以固定自己的 user prompt
- 也可以直接使用调用时传入的 prompt

### workflow skill

如果 skill 是 workflow skill，则不会简单把 prompt 直接拼进去，而是通过模板替换使用。

当前支持：

- `{{prompt}}`
- `{{context.*}}`
- `{{options.*}}`
- `{{metadata.*}}`
- `{{results.*}}`

例如内置 skill `run_shell_command`：

```yaml
workflow:
  steps:
    - id: run_command
      tool: bash
      args:
        command: "{{prompt}}"
```

这里调用时传入的 `prompt`，最终会被替换成 bash 的 `command` 参数。

所以你如果只看 manifest 里没有直接出现具体 prompt 内容，这是正常的：

- prompt 是运行时注入的
- 不会预先写死在 skill 文件里

## 链路二：`skills API / agent chat` 的 route-first 机制

这一条链和 `aicli chat` 明显不同。

### 整体链路

```text
user prompt
-> handler builds session/history/context
-> router.Route(prompt)
-> if matched: execute skill directly
-> else: llm fallback
```

## 1. `AgentChat` 先构造上下文

`POST /api/agent/chat`（兼容：`POST /api/agent/chat`）收到请求后，会先构造：

- session
- history
- workspace context
- context pack
- usage scope

然后根据请求和 runtime config 决定是否开启 route-first / planner-first。

## 2. 后端先 route，而不是模型先感知

如果启用了 routing：

- handler 会先取最后一条用户消息
- 调 `routeCandidates()`
- 内部走 `Router.Route(prompt)`

这里，`prompt` 的第一层用途同样是路由。

但是和 `aicli` 不同的是：

- 当前 route 发生在后端
- 模型并没有先看到 skill schema

## 3. 命中 skill 就直接执行

`Agent` 的 `runWithPreparedRoutes()` 逻辑非常直接：

- 有候选 skill 就取最佳候选
- 直接 `executeSkillMode()`
- 没命中才进入默认模式

而在更高一层 `Orchestrate()` 中：

- route 命中且 agent 结果可用，则返回 `agent_route`
- 否则才走 `llm_fallback`

这意味着 `skills API / agent chat` 的主线不是“LLM tool selection”，而是“backend route selection”。

## 4. 这里模型什么时候才参与

只有两种主要情况：

1. 没有匹配到 skill，进入 `llm_fallback`
2. 命中了 skill，但该 skill 本身是默认 LLM skill，`Executor` 内部再去调用 `LLMRuntime`

因此在这条链上，模型未必知道“我现在在调用一个 skill”。

很多时候模型只是：

- 被 fallback 调用
- 或作为某个 skill 的内部执行引擎

## 两条链路的本质区别

| 维度 | `aicli chat` | `skills API / agent chat` |
| --- | --- | --- |
| skill 是否暴露给模型 | 是 | 不一定 |
| 模型是否主动选择 skill | 是 | 通常不是 |
| 主要机制 | function/tool call | route-first orchestration |
| prompt 的第一层用途 | skill 暴露筛选 | backend route |
| prompt 的第二层用途 | function 参数传入 skill | runtime request 传入 skill |
| skill 未命中时 | 仍可保留普通 tools 或内置 functions | fallback 到 LLM |

## 为什么你会觉得“没看到 prompt 被使用”

这是因为 `prompt` 分散在多个阶段使用，不会只在一个地方出现。

### 阶段 1：候选 skill 筛选

- 用于 route
- 这里读的是 `triggers`

### 阶段 2：function 参数

- 模型发起 `skill__xxx(prompt=...)`
- 这里 prompt 是调用参数

### 阶段 3：Executor 执行

- 普通 LLM skill：进入 `userPrompt / req.Prompt`
- workflow skill：进入 `{{prompt}}` 模板替换

所以如果只盯着 manifest 文件，会误以为 prompt 没有被用。
实际上 prompt 是在运行时被分阶段消费的。

## 当前机制的局限

### 1. `aicli` 与 `AgentChat` 不是同一条调度链

目前：

- `aicli` 更接近 skills-as-functions
- `AgentChat` 更接近 route-first orchestration

二者共享 skill runtime 内核，但对模型的暴露方式不同。

### 2. `trigger` 和 function description 是两套信号

当前设计里：

- `trigger` 给路由器
- `description/schema` 给模型

这两套信号还没有完全统一成单一的“面向 AI 的能力描述”。

### 3. 模型并不会直接学习完整 skill 语义

模型实际看到的只是：

- function name
- description
- parameters

因此 skill 的可发现性强弱，很大程度上取决于：

- route 是否先把它暴露出来
- description 是否足够清晰
- `skills-top-k` / `skills-mode` 是否合适

### 4. `permissions` 已开始影响实际执行

当前 runtime 已不再把 `skill.permissions` 仅当作静态元数据：

- `Executor` 会在真正执行前检查请求是否带有所需权限
- `aicli` 作为本地可信宿主，默认以通配权限运行
- `skills API` 可通过受信请求或显式权限 header 透传权限集合

因此现在的状态是：

- `permissions` 已经进入执行链
- 但更细粒度的 sandbox / policy 仍然可以继续增强

## 建议阅读

- `docs/skill_runtime/aicli_skills_usage.md`
- `docs/skill_runtime/current_architecture.md`
- `docs/skill_runtime/README.md`

如果要继续收口这套机制，后续最自然的方向是：

- 把 `route signal` 和 `AI-visible descriptor` 统一成一套 capability 模型
- 让 `aicli` 和 `AgentChat` 在“技能可见性”上共享更多语义，而不是只共享 runtime 内核
