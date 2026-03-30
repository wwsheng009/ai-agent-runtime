先校正一个前提：**现在已有官方 Go client SDK**，但 **Claude Code 风格的 Agent SDK 公开文档仍然是 Python / TypeScript**。所以如果你说的是“没有官方 Go Agent SDK”，那正确路线不是等 SDK，而是直接基于 **Claude 的 REST Messages API** 或官方 Go client，自建一层 **agent runtime / tool loop / subagent scheduler / context manager**。而且 Messages API 本身是**无状态**的，你每轮都要重发历史；这件事天然把 context window 管理推成了**系统设计问题**，而不是 prompt 小技巧。([Claude开发平台][1])

从 Anthropic 公开材料反推，它的设计意图其实很清楚：第一，别先上复杂框架，先用 API 和简单可组合模式搭底座；第二，把 agent 理解成“**LLM 在 loop 里自主调工具**”；第三，把“prompt engineering”升级成“**context engineering**”，也就是每一轮都要决定**什么该进 context，什么不该进**。Anthropic 还明确说了：在一个 session 里，系统提示、工具定义、历史消息、工具输入和工具输出都会持续累积；上下文接近上限时，需要做 compaction，总结旧历史，保留最近交换和关键决策。([Anthropic][2])

所以在 Go 里，我会把它做成一个 **“Agent OS / Context OS”**，而不是一个“聊天机器人外面套几个 goroutine”。最稳的分层是这样：

```text
User / API
   |
Root Agent Runtime
   |---- Scheduler / Supervisor ---- Subagents
   |
Context Manager  <---->  Session Store / Checkpoints
   |
Output Gateway / Tool Broker / MCP Gateway
   |
Sandbox Executor
   |
Artifact Store (blob/files) + SQLite FTS5 (index/search)
   |
Model Adapter (Claude Messages API / other model APIs)
```

这个分层和 Anthropic 公开出来的 agent loop、subagents、hooks、long-running harness 思路是一致的：主循环负责决策，hooks 在模型上下文之外运行，subagent 用来隔离上下文和并行探索，长任务则靠外部环境与检查点跨多个 context window 延续。([Claude开发平台][3])

## 1. Go 里真正该先实现的不是“多 agent”，而是这 5 个核心组件

**第一是 Model Adapter。**
它只做一件事：把你内部的 `SessionState -> MessageRequest`，发到 `/v1/messages`，再把返回解析成 `text / tool_use / stop_reason / usage`。这一层不要混进调度和记忆逻辑，保持可替换。Anthropic 自己也建议先直接用 LLM API，而不是先沉迷框架。([Claude开发平台][4])

**第二是 Context Manager。**
它负责“当前轮到底给模型看什么”。Anthropic 把 context engineering 定义为对有限 token 预算做持续策展；并且明确提醒上下文是有限资源，随着 token 增长会出现 recall 下降和“context rot”。所以这层不能只是一个 `[]Message append`。它应该维护一个**工作集**：系统提示、当前计划、未解决问题、最近少量对话、当前任务相关的 artifact 摘要、必要的工具定义。其余内容都要被压缩、外置或检索式召回。([Anthropic][5])

**第三是 Output Gateway。**
这是你问题里最关键的点。Anthropic 在工具使用文档里明确提到：工具结果在送回 Claude 之前可以被检查、修改；这正是你在 Go 里要复制的能力。不要让 Playwright snapshot、`git log`、`go test -json` 原文直接进入消息历史。先经过 gateway，变成一个小的 envelope：`brief summary + structured fields + artifact refs + recall hints`，再塞回 context。([Claude开发平台][6])

**第四是 Artifact Store。**
我赞成你用 SQLite FTS5，但要加一句：**FTS5 负责索引，不负责吞掉所有原始大对象。** 文本型原始输出进 SQLite 或 sidecar text 文件再建 FTS；二进制和超大文本进 blob/file store，只在 SQLite 里存 metadata、snippet、hash、路径、tool、session、task、timestamp。Anthropic 在 context engineering 文章里提倡的也是“只保留轻量引用，运行时按需加载”。([Anthropic][5])

**第五是 Scheduler / Supervisor。**
subagent 不应该是“几个 agent 在群聊里互相说话”，而应该是 supervisor 生成子任务包，起独立 session 跑子代理，再把**结构化报告**收回来。Anthropic 对 subagents 的定义就是“单独 agent 实例”，用于**上下文隔离、并行分析、专门指令**；它们不是共享一坨大上下文。([Claude开发平台][7])

## 2. 你的 “Context Mode” 方向是对的，但我会把它再系统化一层

我会把 Context Manager 做成 5 个动作：

**Admission control：任何工具输出默认都不能直接进上下文。**
Anthropic 对传统 tool calling 的批评非常直接：10MB 日志、跨表查询结果、海量中间数据会污染 context，把重要信息挤出去。你的系统应该默认把工具结果视为“外部数据”，先走 reducer，再决定是否有极小的一部分值得进入模型。([Anthropic][8])

**Reducer：每个工具都要有“确定性摘要器”。**
Playwright 不该把整份 DOM snapshot 扔给模型，而应该抽取：失败步骤、选择器、console error、network error、可见文本 top-N、截图路径。`git log` 不该全文回传，而应该提取：commit 数、作者分布、触达目录、最近异常提交、可疑回滚。`go test -json` 不该全量回传，而应该提取：失败用例、首个 stack trace、panic 位置、相关包。这样可以做到你说的“**不额外调用 LLM**”。Anthropic 的 programmatic tool calling / code execution 思路，本质也是把中间数据处理留在执行环境里，只把真正有用的结果暴露给模型。([Anthropic][8])

**Paging / Recall：需要时再 page-in。**
FTS5 很适合做第一层 recall。模型问“把 checkout 失败时的 console error 给我”时，不是把整个 artifact 重新塞回来，而是用 `artifact_id + tool=playwright + bm25(query)` 找 top-k 片段，只把相关 snippet page-in。Anthropic 对“just in time context”强调的就是这种做法：保留引用、动态装载。([Anthropic][5])

**Compaction：把旧上下文从 transcript 变成 ledger。**
Anthropic 的 compaction 是“旧历史总结，保留最近交换和关键决策”。在你的 Go 运行时里，旧消息不该继续作为真相来源；真相来源应是一个**decision log / task log / artifact refs**。我建议把旧轮次压成三类结构：`decisions[]`、`open_questions[]`、`artifacts[]`。这样你重启 session 时，不是回放几千行 transcript，而是从 ledger 恢复。([Claude开发平台][3])

**Checkpoint / Resume：跨 context window 延续。**
Anthropic 针对长任务的做法是 initializer agent 先写环境脚手架和 progress 文件，后续 coding agent 每个 session 只做增量工作并留下结构化更新。你完全可以在 Go 里照着做：每个 session 结束时，落盘 `plan.json`、`decision_log.ndjson`、`open_tasks.json`、`artifact_manifest.json`。下个 session 读这些，而不是读完整历史。([Anthropic][9])

## 3. 主 agent 如何指挥 subagent：不要“聊天协同”，要“任务包协同”

Anthropic 公开的多代理材料里，主模式是一个 lead agent 先规划，再起并行 agent 去搜或验证，最后回主代理综合；而且他们明确说，这种模式的价值是**把详细搜索上下文隔离在 sub-agent 内部，lead agent 只负责综合**。这点很关键。([Anthropic][10])

所以我建议主代理和子代理之间只交换两样东西：

1. **任务包**
   `goal / success criteria / allowed tools / input artifact refs / max turns / budget / output schema`

2. **结构化回执**
   `status / summary / findings[] / evidence artifact refs[] / next actions / blockers`

主代理千万不要把整个父会话 transcript 传给子代理；子代理也不要把完整 transcript 传回来。Anthropic 的 subagents 文档强调的就是“独立实例 + 隔离上下文 + 平行分析”。([Claude开发平台][7])

协同机制上，我推荐三条硬规则：

**规则一：many readers, single writer。**
多个子代理可以并发读、搜、测、比对，但真正改代码或改计划的 writer 最好只有一个。这样不会出现两个 child 同时修改工作区、同时生成 patch 的冲突。

**规则二：child-child 不直接对话。**
所有协同都经由父代理或共享 artifact/blackboard。直接 child-child 聊天会让上下文拓扑失控，debug 也会非常痛苦。

**规则三：父代理只消费 child report，不消费 child transcript。**
这条是上下文不爆炸的生命线。

Anthropic 的 hook 设计也很值得抄：`PreToolUse`、`PostToolUse`、`SubagentStart/SubagentStop`、`PreCompact`。在 Go 里你把它做成 event bus 即可，这些事件都应发生在模型上下文之外，不消耗 token。([Claude开发平台][3])

## 4. MCP 这里我会纠正你一句：问题很大，但不完全是“协议缺陷”

你说“第三方 MCP 工具响应通过 JSON-RPC 直接发模型，无法拦截冗余数据”，这个说法**方向对，但表述太绝对**。MCP 的 transport 确实是 JSON-RPC over stdio / HTTP；`tools/call` 返回 `CallToolResult`，而模型看到的是客户端后续构造的 `tool_result`。但 **MCP 规范本身并没有强制你把工具结果原样喂给模型**，甚至明确写了“协议不规定特定的用户交互模型，具体实现可自行决定”。此外，task-augmented `tools/call` 还允许先返回 task metadata，真正结果稍后通过 `tasks/result` 拿；host app 还可以先给模型一个 immediate response。换句话说，**拦截点在客户端，不在协议外面。**([Model Context Protocol][11])

所以我会把问题定义成：
**不是 MCP 不能拦，而是很多 MCP client 采用了“直通式 tool_result”架构。** 这会把协议层返回和模型上下文耦死，造成 token 浪费。Anthropic 自己后来推动 Tool Search Tool 和 code execution with MCP，本质也是在把这个耦合拆开：工具先按需发现，中间数据先在执行环境处理，只有必要信息进入模型。([Anthropic][8])

因此你的 Go 实现里，MCP 一定要放在 **MCP Gateway** 后面，而不是直接暴露给模型：

* `list_tools` 结果进本地工具索引，不全量进 prompt
* `search_tools(query)` 只把命中的 3-5 个工具 schema 暴露给模型
* `call_tool` 结果先入 gateway，转成 envelope，再决定是否形成 `tool_result`
* 长任务用 task mode，先回一条短状态，再异步拿结果
* 原始 `CallToolResult.content` 默认入 artifact store，不入 context

这其实正对应 Anthropic 的 Tool Search Tool：大工具库或多 MCP server 时，先做搜索，不要把全量工具定义前置到 context；官方甚至明确给出 defer loading 整个 MCP server 的做法。([Anthropic][8])

## 5. “操作系统类比”是好类比，但我会改成这个版本

你的比喻很好，不过我会把映射关系调得更精确一点：

* **Context window = working set / L1 活跃工作集**
* **Artifact store + FTS5 = page file + file system + inverted index**
* **Reducer / compactor = log compaction + summarizing serializer**
* **Recall / page-in = page fault handler**
* **Admission policy = memory allocator**
* **TTL / LRU / priority eviction = cache manager**
* **Checkpoint files = process snapshot**

不太像的地方是：真正的虚拟内存 page-in 是精确字节回填，而 LLM 的 “page-in” 仍然要经过**语义选择**和 **token 预算**，不是把整页搬回来就行。所以它更像“**受预算约束的语义虚拟内存**”。Anthropic 对 context engineering 的定义，本来也不是“尽量塞满”，而是“在有限 attention budget 下，挑最小且最高信号的 token 集合”。([Anthropic][5])

## 6. 一个纯 Go 的最小骨架

下面这个骨架不是 SDK 克隆，而是你真正需要的运行时轮廓：

```go
type Runtime struct {
	Model   ModelClient
	CtxMgr  ContextManager
	Broker  ToolBroker
	Gateway OutputGateway
	Store   ArtifactStore
	Sched   Scheduler
}

type ToolEnvelope struct {
	Brief        string
	Structured   map[string]any
	ArtifactRefs []string
	RecallHints  []string
}

func (r *Runtime) Step(ctx context.Context, s *Session) error {
	req, err := r.CtxMgr.BuildRequest(s)
	if err != nil { return err }

	if r.CtxMgr.ShouldCompact(req) {
		if err := r.CtxMgr.Compact(s); err != nil { return err }
		req, err = r.CtxMgr.BuildRequest(s)
		if err != nil { return err }
	}

	resp, err := r.Model.CreateMessage(ctx, req)
	if err != nil { return err }

	for _, tu := range resp.ToolUses {
		if tu.Name == "spawn_subagents" {
			reports, err := r.Sched.Run(ctx, s, tu)
			if err != nil { return err }
			if err := r.CtxMgr.AdmitChildReports(s, reports); err != nil { return err }
			continue
		}

		raw, err := r.Broker.Exec(ctx, s, tu)          // sandbox / MCP / local tools
		if err != nil { raw = NewErrorRawResult(err) }

		artifactRefs, err := r.Store.Put(ctx, s, raw)  // raw stdout/stderr/files/snaps go outside context
		if err != nil { return err }

		env, err := r.Gateway.Reduce(ctx, tu, raw, artifactRefs) // deterministic summary
		if err != nil { return err }

		if err := r.CtxMgr.AdmitToolEnvelope(s, env); err != nil { return err }
	}

	if resp.FinalText != "" {
		return r.CtxMgr.AdmitAssistantText(s, resp.FinalText)
	}
	return nil
}
```

这里最关键的不是 `Scheduler.Run`，而是 `Gateway.Reduce` 和 `CtxMgr.AdmitToolEnvelope`。前者决定原始数据如何外置，后者决定哪些 token 值得进入模型。Anthropic 的公开材料其实一直在强调这两件事：工具中间结果不要污染 context，旧历史要能被 compact，subagent 只应回传压缩后的结果。([Anthropic][8])

## 7. 你列出的 5 个判断，我的结论是

**1）“工具调用冗余数据会迅速填满 context，导致遗忘和重启会话”**
这个判断基本成立。Anthropic 明确把“大中间结果污染 context”列为传统 tool calling 的核心问题之一。([Anthropic][8])

**2）“Context Mode：沙盒执行，只返回摘要，完整数据存 SQLite FTS5，按需检索，无额外 LLM 调用”**
这是很好的方向，而且和 Anthropic 的 “just in time context”、programmatic tool calling、code execution with MCP 非常同路。唯一补充是：FTS5 负责索引，原始大对象最好独立存储。([Anthropic][5])

**3）“MCP 协议缺陷：第三方工具响应无法拦截”**
我会改写成：**很多 MCP 客户端实现存在直通式设计缺陷；MCP 协议本身并不强制直通。** 你完全可以加 gateway、task mode、immediate response、tool search、result reduction。([Model Context Protocol][12])

**4）“Context 管理像虚拟内存，自动精简像垃圾回收”**
这个类比很有启发，但工程上更接近“working set 管理 + page-in/page-out + log compaction + checkpoint”。([Anthropic][5])

**5）“更大的机会在 Agent 基础设施”**
我同意。Anthropic 自己的公开材料已经把 context engineering、tool infrastructure、multi-agent coordination、sandboxing、long-running harness 都当成能力边界，而不只是模型调用边界。([Claude开发平台][3])

一句话收束：**在 Go 里别去模仿 Claude Code 的外观，要复刻它背后的操作系统结构。** 主 agent 不是“最聪明的那个模型”，而是“调度器 + context pager + output gateway”的前台；subagent 不是群聊成员，而是被派出去干活、带着小上下文回来交结构化报告的 worker。这样做，multi-agent 和 context window 才会一起变稳。

[1]: https://platform.claude.com/docs/en/api/client-sdks "Client SDKs - Claude API Docs"
[2]: https://www.anthropic.com/engineering/building-effective-agents "Building Effective AI Agents \ Anthropic"
[3]: https://platform.claude.com/docs/en/agent-sdk/agent-loop "How the agent loop works - Claude API Docs"
[4]: https://platform.claude.com/docs/en/api/overview "API Overview - Claude API Docs"
[5]: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents "Effective context engineering for AI agents \ Anthropic"
[6]: https://platform.claude.com/docs/zh-CN/agents-and-tools/tool-use/implement-tool-use "如何实现工具使用 - Claude API Docs"
[7]: https://platform.claude.com/docs/en/agent-sdk/subagents "Subagents in the SDK - Claude API Docs"
[8]: https://www.anthropic.com/engineering/advanced-tool-use "Introducing advanced tool use on the Claude Developer Platform \ Anthropic"
[9]: https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents "Effective harnesses for long-running agents \ Anthropic"
[10]: https://www.anthropic.com/engineering/multi-agent-research-system "How we built our multi-agent research system \ Anthropic"
[11]: https://modelcontextprotocol.io/specification/2025-06-18/basic/transports "Transports - Model Context Protocol"
[12]: https://modelcontextprotocol.io/specification/2025-06-18/server/tools "Tools - Model Context Protocol"
