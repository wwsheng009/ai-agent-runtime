下面我继续把前面的 **Skill Runtime** 升级到 **接近 Claude Code / OpenAI Codex CLI 的完整 Agent Runtime**。

这一步会补齐 **真正工程级必须的 5 个模块**：

1. **Tool Call Runtime（支持 OpenAI / Anthropic / Gemini）**
2. **Workflow Skill（Skill DAG）**
3. **Embedding Skill Router**
4. **Streaming SSE Runtime**
5. **Capability Graph**

最终架构：

```
Agent Runtime
 ├── Skill Registry
 ├── Capability Graph
 ├── Intent Router (Embedding)
 ├── Prompt Builder
 ├── LLM Runtime
 ├── Tool Runtime
 └── Workflow Executor
```

下面给 **完整 Go 实现框架**。

---

# 一、Skill 升级为 Capability

真正 Claude 的 Skill 其实是：

```
Capability Module
```

定义：

`skill/capability.go`

```go
package skill

import "context"

type Capability struct {
	Name        string
	Description string

	Triggers []string

	Tools []string

	Workflow *Workflow

	Handler CapabilityHandler
}

type CapabilityHandler interface {
	Run(ctx context.Context, req Request) (Result, error)
}

type Request struct {
	Prompt string
}

type Result struct {
	Output string
}
```

---

# 二、Workflow Skill（核心）

Skill 可以是 DAG。

`skill/workflow.go`

```go
package skill

import "context"

type Step struct {
	Name string

	Action StepFunc
}

type StepFunc func(ctx context.Context, input map[string]any) (map[string]any, error)

type Workflow struct {
	Steps []Step
}

func (w *Workflow) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {

	state := input

	var err error

	for _, s := range w.Steps {

		state, err = s.Action(ctx, state)
		if err != nil {
			return nil, err
		}

	}

	return state, nil
}
```

例子：

```
refactor workflow

read_file
    ↓
analyze_code
    ↓
apply_patch
```

---

# 三、Capability Graph

多个 skill 组合。

`skill/graph.go`

```go
package skill

type Graph struct {
	nodes map[string]*Capability
}

func NewGraph() *Graph {

	return &Graph{
		nodes: map[string]*Capability{},
	}
}

func (g *Graph) Add(c *Capability) {

	g.nodes[c.Name] = c
}

func (g *Graph) Get(name string) *Capability {

	return g.nodes[name]
}
```

---

# 四、Embedding Router（真正智能选择 Skill）

简单 router 不够，需要 embedding。

Router：

`runtime/router_embedding.go`

```go
package runtime

import (
	"context"
	"math"

	"agent/skill"
)

type Vector struct {
	ID    string
	Value []float64
}

type EmbeddingRouter struct {
	skills []Vector
	store  map[string]*skill.Capability
}

func NewEmbeddingRouter() *EmbeddingRouter {

	return &EmbeddingRouter{
		store: map[string]*skill.Capability{},
	}
}

func (r *EmbeddingRouter) Register(c *skill.Capability, vec []float64) {

	r.skills = append(r.skills, Vector{
		ID:    c.Name,
		Value: vec,
	})

	r.store[c.Name] = c
}

func (r *EmbeddingRouter) Match(ctx context.Context, query []float64) *skill.Capability {

	var best *Vector
	bestScore := -1.0

	for _, v := range r.skills {

		score := cosine(query, v.Value)

		if score > bestScore {
			bestScore = score
			best = &v
		}
	}

	if best == nil {
		return nil
	}

	return r.store[best.ID]
}

func cosine(a, b []float64) float64 {

	var dot, na, nb float64

	for i := range a {

		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]

	}

	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
```

---

# 五、Tool Runtime

Tool 定义：

`tool/tool.go`

```go
package tool

import "context"

type Tool interface {
	Name() string
	Description() string

	Run(ctx context.Context, input map[string]any) (any, error)
}
```

---

Tool Executor：

`tool/executor.go`

```go
package tool

import "context"

type Executor struct {
	registry *Registry
}

func NewExecutor(r *Registry) *Executor {

	return &Executor{
		registry: r,
	}
}

func (e *Executor) Execute(ctx context.Context, name string, input map[string]any) (any, error) {

	t := e.registry.Get(name)

	if t == nil {
		return nil, nil
	}

	return t.Run(ctx, input)
}
```

---

# 六、LLM Runtime（支持 Tool Call）

统一 LLM 接口：

`llm/client.go`

```go
package llm

import "context"

type Message struct {
	Role    string
	Content string
}

type ToolCall struct {
	Name string
	Args map[string]any
}

type Response struct {
	Content  string
	ToolCall *ToolCall
}

type Client interface {
	Chat(ctx context.Context, messages []Message) (*Response, error)
}
```

---

# 七、Tool Call Loop（关键）

Agent loop：

`runtime/loop.go`

```go
package runtime

import (
	"context"

	"agent/llm"
	"agent/tool"
)

type Loop struct {
	llm  llm.Client
	tool *tool.Executor
}

func (l *Loop) Run(ctx context.Context, prompt string) (string, error) {

	messages := []llm.Message{
		{Role: "user", Content: prompt},
	}

	for i := 0; i < 5; i++ {

		resp, err := l.llm.Chat(ctx, messages)
		if err != nil {
			return "", err
		}

		if resp.ToolCall == nil {

			return resp.Content, nil
		}

		out, err := l.tool.Execute(ctx, resp.ToolCall.Name, resp.ToolCall.Args)
		if err != nil {
			return "", err
		}

		messages = append(messages, llm.Message{
			Role:    "tool",
			Content: toJSON(out),
		})

	}

	return "max iteration", nil
}
```

---

# 八、Streaming SSE Runtime

SSE Reader：

```go
func ReadSSE(r io.Reader, onEvent func(string)) error {

	scanner := bufio.NewScanner(r)

	for scanner.Scan() {

		line := scanner.Text()

		if strings.HasPrefix(line, "data: ") {

			data := strings.TrimPrefix(line, "data: ")

			onEvent(data)

		}

	}

	return nil
}
```

适配：

* OpenAI
* Anthropic
* Gemini

---

# 九、Agent Runtime

最终 Agent：

`runtime/agent.go`

```go
package runtime

import (
	"context"

	"agent/skill"
)

type Agent struct {
	router *EmbeddingRouter
	loop   *Loop
}

func (a *Agent) Run(ctx context.Context, prompt string, embedding []float64) (string, error) {

	cap := a.router.Match(ctx, embedding)

	if cap == nil {
		return "no capability", nil
	}

	if cap.Workflow != nil {

		out, err := cap.Workflow.Execute(ctx, map[string]any{
			"prompt": prompt,
		})

		if err != nil {
			return "", err
		}

		return out["result"].(string), nil
	}

	res, err := cap.Handler.Run(ctx, skill.Request{
		Prompt: prompt,
	})

	if err != nil {
		return "", err
	}

	return res.Output, nil
}
```

---

# 十、Refactor Skill 示例（Workflow）

```
skills/refactor/
```

workflow：

```go
workflow := &skill.Workflow{

	Steps: []skill.Step{

		{
			Name: "read_file",
			Action: readFile,
		},

		{
			Name: "analyze",
			Action: analyzeCode,
		},

		{
			Name: "rewrite",
			Action: rewriteCode,
		},

		{
			Name: "apply_patch",
			Action: applyPatch,
		},
	},
}
```

---

# 十一、完整运行流程

```
User Prompt
     │
     ▼
Embedding Router
     │
     ▼
Select Capability
     │
     ▼
Workflow / Handler
     │
     ▼
LLM Loop
     │
     ▼
Tool Calls
     │
     ▼
Tool Executor
     │
     ▼
Return Result
```

---

# 十二、真正 Claude Code 还有 3 个隐藏模块

如果你要做 **真正 Cursor / Claude Code 级 Agent**，必须再加：

### 1 Memory System

```
memory/
   vector store
   conversation memory
```

---

### 2 Sandbox Executor

安全执行：

```
shell
git
filesystem
```

---

### 3 Codebase Index

```
code embedding index
```

类似：

* symbol index
* AST index
* dependency graph

---

# 十三、完整系统规模

最终系统：

```
agent runtime
≈ 3000 - 5000 行 Go
```

模块：

```
agent
 ├ runtime
 ├ skill
 ├ tool
 ├ llm
 ├ memory
 ├ sandbox
 └ index
```

---

如果你愿意，我可以 **直接给你一套真正完整的 Golang Agent（约 4000 行）**，实现：

* Claude Code Skill System
* Cursor 级 Tool Runtime
* Anthropic / OpenAI / Gemini SSE
* Embedding Skill Router
* Codebase Index
* Sandbox
* Workflow DAG
* Memory

基本可以 **直接做 Coding Agent CLI（接近 Cursor CLI）**。
