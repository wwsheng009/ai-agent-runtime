可以，而且最像 Claude Code 的做法，不是做“几个 agent 在一个公共会话里互相聊天”，而是做一个 **Supervisor 驱动的 agent loop**：主代理把子任务通过一个特殊工具 `spawn_subagents` 交给调度器，调度器在 Go 里起 goroutine 跑子代理；每个子代理都有**独立上下文、独立工具白名单、独立预算**，最后只把**摘要**回传给父代理。Anthropic 公开文档也基本落在这个方向：Claude Agent SDK 复用了 Claude Code 的 tools、agent loop 和 context management，但官方 SDK 目前是 Python / TypeScript；同时官方 Go SDK 已经提供了 Messages API、手写 tool loop 示例，以及 `BetaToolRunner` 这种自动单代理工具循环，所以在 Go 里更现实的路线是“**官方 Go SDK + 你自己的多代理 supervisor**”。 ([Claude开发平台][1])

Claude 风格的关键不在“多开几个模型请求”，而在这三个约束同时成立：
主代理按“接收任务 → 决策 → 调工具 → 回填结果 → 继续循环”运行；子代理启动时用**全新会话**，不继承父代理的历史对话；父代理只收到子代理的**最终结果摘要**，而不是完整 transcript。官方文档明确写了这一点，而且 Anthropic 的多代理研究文章也强调，subagent 的价值在于**并行探索 + 独立上下文窗口 + 压缩回传**。 ([Claude开发平台][2])

我建议你在 Go 里直接做下面这个拓扑：

1. **Root supervisor**
   负责目标拆解、预算分配、子代理并发控制、冲突解决。

2. **Read-only subagents**
   并行做搜索、读文件、跑只读命令、查日志、查测试失败。

3. **Single writer agent**
   只有一个代理真正写文件；其他代理只提建议或生成 patch，避免并发改同一代码树。

4. **Verifier agents**
   并行跑测试、lint、build、benchmark，结果回传给 root。

5. **Tool broker / sandbox**
   所有工具都经过白名单、路径限制、网络限制、审计。

6. **Context compactor / memory**
   长会话自动压缩历史，只保留高信号摘要。

这套形状和 Claude 文档里的 hooks、subagent、compaction、permissions 思路是对齐的：官方 SDK 里有 `SubagentStart` / `SubagentStop`、`PreToolUse`、`PostToolUse`、`PreCompact` 这些钩子；在 Go 里你自己做一个事件总线就行。 ([Claude开发平台][2])

## 最小可落地机制

先把 `spawn_subagents` 设计成一个“普通工具”，由模型自己决定何时调用：

```json
{
  "name": "spawn_subagents",
  "description": "Spawn isolated subagents for parallel subtasks. Use only when tasks are independent.",
  "input_schema": {
    "type": "object",
    "properties": {
      "agents": {
        "type": "array",
        "items": {
          "type": "object",
          "properties": {
            "name": { "type": "string" },
            "goal": { "type": "string" },
            "tools": {
              "type": "array",
              "items": { "type": "string" }
            },
            "max_turns": { "type": "integer" },
            "budget_usd": { "type": "number" }
          },
          "required": ["name", "goal", "tools"]
        }
      }
    },
    "required": ["agents"]
  }
}
```

然后你的 Go 调度器拦截这个工具调用，不把它发给外部系统，而是在本地做三件事：

* 校验深度、预算、最大并发
* 为每个子代理创建**新会话**
* 把每个子代理的**最终摘要**拼成 `tool_result` 回给父代理

这一步就是“像 Claude Code”的核心。

## Go 骨架

下面这段骨架我按可直接扩展的形状写，重点是 **父子上下文隔离** 和 **摘要回传**：

```go
package multiagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role    Role
	Name    string
	Content string
}

type Task struct {
	ID          string
	Goal        string
	Constraints []string
}

type ToolCall struct {
	Name  string
	Input map[string]any
}

type SpawnSpec struct {
	Name      string
	Goal      string
	Tools     []string
	MaxTurns  int
	BudgetUSD float64
}

type Decision struct {
	FinalAnswer string
	ToolCalls   []ToolCall
	Spawn       []SpawnSpec
}

type AgentRequest struct {
	AgentName string
	Task      Task
	Messages  []Message
	ToolNames []string
	MaxTurns  int
}

type AgentResponse struct {
	Decision Decision
}

type Model interface {
	Decide(ctx context.Context, req AgentRequest) (AgentResponse, error)
}

type Tool interface {
	Name() string
	Run(ctx context.Context, input map[string]any) (string, error)
}

type ToolRegistry struct {
	m map[string]Tool
}

func NewToolRegistry(ts ...Tool) *ToolRegistry {
	m := make(map[string]Tool, len(ts))
	for _, t := range ts {
		m[t.Name()] = t
	}
	return &ToolRegistry{m: m}
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	t, ok := r.m[name]
	return t, ok
}

type AgentResult struct {
	AgentName string
	Summary   string
	Children  []AgentResult
}

type Supervisor struct {
	model         Model
	tools         *ToolRegistry
	maxConcurrent int
	maxDepth      int
}

func NewSupervisor(model Model, tools *ToolRegistry, maxConcurrent, maxDepth int) *Supervisor {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	return &Supervisor{
		model:         model,
		tools:         tools,
		maxConcurrent: maxConcurrent,
		maxDepth:      maxDepth,
	}
}

func (s *Supervisor) Run(ctx context.Context, root Task) (AgentResult, error) {
	return s.runAgent(ctx, "root", root, []string{
		"search_repo", "read_file", "run_tests", "spawn_subagents",
	}, 0, 8)
}

func (s *Supervisor) runAgent(
	ctx context.Context,
	name string,
	task Task,
	allowedTools []string,
	depth int,
	maxTurns int,
) (AgentResult, error) {
	if depth > s.maxDepth {
		return AgentResult{}, errors.New("max subagent depth exceeded")
	}

	msgs := []Message{
		{
			Role: RoleSystem,
			Content: `You are a focused coding agent.
Use subagents only for independent, parallelizable subtasks.
Return concise summaries.`,
		},
		{Role: RoleUser, Content: task.Goal},
	}

	var children []AgentResult

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := s.model.Decide(ctx, AgentRequest{
			AgentName: name,
			Task:      task,
			Messages:  msgs,
			ToolNames: allowedTools,
			MaxTurns:  maxTurns,
		})
		if err != nil {
			return AgentResult{}, err
		}

		d := resp.Decision

		if d.FinalAnswer != "" && len(d.ToolCalls) == 0 && len(d.Spawn) == 0 {
			return AgentResult{
				AgentName: name,
				Summary:   d.FinalAnswer,
				Children:  children,
			}, nil
		}

		for _, tc := range d.ToolCalls {
			if tc.Name == "spawn_subagents" {
				// 也可以统一走 Tool.Run；这里单独展开只是为了看清机制
				continue
			}
			out, err := s.execTool(ctx, tc, allowedTools)
			if err != nil {
				out = "tool error: " + err.Error()
			}
			msgs = append(msgs,
				Message{Role: RoleAssistant, Content: "calling tool " + tc.Name},
				Message{Role: RoleTool, Name: tc.Name, Content: out},
			)
		}

		if len(d.Spawn) > 0 {
			results, err := s.runChildren(ctx, depth+1, d.Spawn)
			if err != nil {
				return AgentResult{}, err
			}
			children = append(children, results...)

			// 关键点：父代理只拿到摘要，不拿完整 transcript
			for _, child := range results {
				msgs = append(msgs, Message{
					Role:    RoleTool,
					Name:    "subagent_result",
					Content: fmt.Sprintf("subagent=%s summary=%s", child.AgentName, child.Summary),
				})
			}
		}

		msgs = compact(msgs)
	}

	return AgentResult{}, errors.New("max turns exceeded")
}

func (s *Supervisor) runChildren(
	ctx context.Context,
	depth int,
	specs []SpawnSpec,
) ([]AgentResult, error) {
	sem := make(chan struct{}, s.maxConcurrent)
	out := make([]AgentResult, len(specs))
	errCh := make(chan error, len(specs))
	var wg sync.WaitGroup

	for i, spec := range specs {
		i, spec := i, spec
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			res, err := s.runAgent(
				ctx,
				spec.Name,
				Task{ID: fmt.Sprintf("child-%d", i), Goal: spec.Goal},
				spec.Tools,
				depth,
				defaultInt(spec.MaxTurns, 5),
			)
			if err != nil {
				errCh <- err
				return
			}
			out[i] = res
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Supervisor) execTool(ctx context.Context, tc ToolCall, allow []string) (string, error) {
	if !contains(allow, tc.Name) {
		return "", fmt.Errorf("tool %q not allowed", tc.Name)
	}
	t, ok := s.tools.Get(tc.Name)
	if !ok {
		return "", fmt.Errorf("tool %q not registered", tc.Name)
	}
	return t.Run(ctx, tc.Input)
}

func compact(msgs []Message) []Message {
	// 生产环境里这里要做：
	// 1. 清理大工具输出
	// 2. 只保留最近 N 轮
	// 3. 把关键决策写入 scratchpad / memory
	return msgs
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func defaultInt(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}
```

这个骨架背后的设计点只有两个最重要：

* **subagent 是 fresh session，不共享父会话历史**
* **父代理只吸收 child summary，而不是 child transcript**

这正是官方文档强调的上下文控制方式。 ([Claude开发平台][2])

## 接 Anthropic 官方 Go SDK 的方式

在 Anthropic 侧，你可以分两层接：

### 第一层：先做单代理 tool loop

官方 Go SDK README 已经给了两种路线：
一种是自己写 `for { client.Messages.New(...) -> 执行 tool -> 把 tool_result 追加回 messages }` 的循环；另一种是直接用 `client.Beta.Messages.NewToolRunner(...)` 自动跑完单代理工具循环。前者更适合你后面接多代理 supervisor，后者更适合先快速验证单代理工具链。 ([GitHub][3])

你的 `Model.Decide(...)` 里可以做这三步：

1. `messages` 转成 Anthropic 的 `MessageParam`
2. `allowedTools` 转成 `Tools`
3. 遍历返回内容，解析：

   * 普通 `tool_use` → 本地执行工具
   * `spawn_subagents` → 交给 Go supervisor
   * 纯文本 → `FinalAnswer`

也就是说，**多代理不要塞进 SDK 里做**，而是放在 SDK 外面一层做 orchestration。

### 第二层：把 subagent 当作“特殊工具”

这是最稳的建模方式。对模型而言，`spawn_subagents` 只是一个工具；对你的程序而言，它其实是：

```go
type SpawnSubagentsTool struct {
	supervisor *Supervisor
}

func (t *SpawnSubagentsTool) Name() string { return "spawn_subagents" }

func (t *SpawnSubagentsTool) Run(ctx context.Context, input map[string]any) (string, error) {
	specs := decodeSpawnSpecs(input)
	results, err := t.supervisor.runChildren(ctx, 1, specs)
	if err != nil {
		return "", err
	}
	return marshalChildSummaries(results), nil
}
```

这样你就把“模型推理”和“系统调度”解耦了。

## 真正像 Claude Code 的几个细节

### 1) 不要让多个 agent 同时写代码

Claude 风格的并发更适合**读、搜、测、比对**，不适合多个子代理同时改同一工作区。实践上最稳的是：

* 子代理并发搜代码、跑测试、查日志
* 父代理综合结果后下发给一个 writer
* verifier 再并发跑 build/test/lint

这能避开 patch 冲突和脏工作区问题。

### 2) 工具按需加载，不要一口气全塞给模型

Anthropic 的 tool search 文档明确指出，工具定义会快速吃掉上下文；在多 server 场景里，工具定义可能先占掉约 55K tokens，tool search 通常能把这部分降低 85% 以上，而且工具数超过 30–50 个后，模型挑工具的准确率会明显下降。文档也明确说了，你可以自己实现 **client-side tool search**。在 Go 里，这非常适合做成“工具索引 + BM25 / embedding 检索 + 动态注入 3–5 个相关工具”。 ([Claude开发平台][4])

### 3) 长链工具调用用“代码编排”而不是每步都回模型

Anthropic 的 programmatic tool calling 文档说明，这类模式的核心收益就是**减少多工具工作流的 round-trip 和 token 消耗**：先在代码里循环/过滤/聚合，最后只把高信号结果给模型。即使你暂时不用 Anthropic 托管的 code execution，这个思想在 Go 里也一样成立：把复杂 API 编排下沉到本地 runner 或 sandbox worker。 ([Claude开发平台][5])

### 4) 一定要做 sandbox

Claude Code 的公开安全文章把边界说得很清楚：要想让 agent 更自治，又不把环境敞开，至少要有**文件系统隔离**和**网络隔离**两层边界。Go 里最常见的做法是：

* 文件系统：只挂载 repo workspace / tmp / cache
* 网络：默认 deny，只放行 Git、包管理镜像、内部 API 白名单
* 命令白名单：先允许 `grep`, `find`, `go test`, `git diff` 这类只读/低风险命令
* 写操作：必须经过 policy 和审计日志

这一步对“像 Claude Code”非常重要，因为一旦子代理能跑 bash，就会遇到 prompt injection 和权限扩散问题。 ([Anthropic][6])

### 5) 一开始就加预算、深度和 turn 限制

官方 agent loop 文档把 `max_turns`、`max_budget_usd`、`SubagentStart` / `SubagentStop`、`PreToolUse`、`PostToolUse` 这些控制点都放在比较核心的位置。你在 Go 里最好一开始就有：

* `maxDepth`
* `maxConcurrent`
* `maxTurnsPerAgent`
* `budgetPerAgent`
* `globalBudget`
* `preTool` / `postTool` hook
* `onSubagentStart` / `onSubagentStop` event

否则多代理非常容易失控。 ([Claude开发平台][2])

## 我会怎么分三期做

### 第 1 期：单代理先跑通

目标是先把最小循环跑出来：

* `Messages.New(...)`
* tool schema
* 本地工具执行
* tool result 回填
* streaming / log / trace

这一步用官方 Go SDK 的手写 tool loop 或 `BetaToolRunner` 都行。 ([GitHub][3])

### 第 2 期：加 `spawn_subagents`

只允许 read-only 子代理：

* search / grep / read_file / run_tests / query_logs
* 父代理只拿摘要
* 加 semaphore 控并发
* 加 turn / budget / depth limit

### 第 3 期：加 writer + verifier + compaction

这时再上：

* 单 writer agent
* patch queue
* verifier fan-out
* transcript compaction
* scratchpad / memory
* tool search

Anthropic 的 context engineering 文章也把 compaction、memory / note-taking、sub-agent architecture 视为长任务里最关键的三类上下文治理手段。 ([Anthropic][7])

## 一句结论

用 Go 实现“像 Claude Code 的 multi-agent/sub-agents”，最稳的方式是：

**Anthropic 官方 Go SDK 负责单代理的 Messages / tool loop；你自己的 Supervisor 负责 `spawn_subagents`、上下文隔离、预算控制、并发调度和安全沙箱。**

先把它做成 **“主代理 + 特殊 spawn 工具 + 独立子代理 fresh sessions + 只回传摘要”**，你就已经抓住最核心的机制了。

[1]: https://platform.claude.com/docs/en/agent-sdk/overview "Agent SDK overview - Claude API Docs"
[2]: https://platform.claude.com/docs/en/agent-sdk/agent-loop "How the agent loop works - Claude API Docs"
[3]: https://github.com/anthropics/anthropic-sdk-go "GitHub - anthropics/anthropic-sdk-go: Access to Anthropic's safety-first language model APIs via Go · GitHub"
[4]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool "Tool search tool - Claude API Docs"
[5]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/programmatic-tool-calling "Programmatic tool calling - Claude API Docs"
[6]: https://www.anthropic.com/engineering/claude-code-sandboxing "Making Claude Code more secure and autonomous with sandboxing \ Anthropic"
[7]: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents "Effective context engineering for AI agents \ Anthropic"
