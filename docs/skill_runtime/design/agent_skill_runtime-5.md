很好，我们继续把 **Golang Agent Runtime**推进到 **真正接近 Cursor / Claude Code 架构的完整 Coding Agent**。
这一部分补齐 **目前所有开源 Agent 中最核心但最难实现的 5 个模块**：

1. **Planner（任务规划）**
2. **ReAct 执行循环**
3. **Repo Map（代码库结构图）**
4. **AST Code Index（语义代码索引）**
5. **Token Budget Manager**

这些机制在：

* Claude Code
* Cursor
* OpenAI Codex CLI

里基本都是核心设计。

---

# 一、Planner（任务规划）

Coding Agent 不应该直接执行 prompt，而是先 **生成 Plan**。

结构：

```
User Prompt
      │
      ▼
Planner
      │
      ▼
Plan (steps)
      │
      ▼
Executor
```

---

## Plan 定义

`planner/plan.go`

```go
package planner

type Step struct {
	ID     int
	Action string
	Input  string
}

type Plan struct {
	Goal  string
	Steps []Step
}
```

---

## Planner

`planner/planner.go`

```go
package planner

import (
	"context"
	"encoding/json"

	"agent/llm"
)

type Planner struct {
	llm llm.Client
}

func (p *Planner) CreatePlan(ctx context.Context, prompt string) (*Plan, error) {

	req := []llm.Message{
		{
			Role: "system",
			Content: `
You are an AI coding planner.
Break the user request into steps.

Return JSON.
`,
		},
		{
			Role: "user",
			Content: prompt,
		},
	}

	resp, err := p.llm.Chat(ctx, req)
	if err != nil {
		return nil, err
	}

	var plan Plan

	err = json.Unmarshal([]byte(resp.Content), &plan)

	return &plan, err
}
```

---

# 二、ReAct Execution Loop

经典 Agent 模式：

```
Thought
Action
Observation
```

循环执行。

---

## ReAct Loop

`runtime/react_loop.go`

```go
package runtime

import (
	"context"

	"agent/llm"
	"agent/tool"
)

type ReActLoop struct {
	llm   llm.Client
	tools *tool.Executor
}

func (r *ReActLoop) Run(ctx context.Context, prompt string) (string, error) {

	messages := []llm.Message{
		{Role: "user", Content: prompt},
	}

	for i := 0; i < 10; i++ {

		resp, err := r.llm.Chat(ctx, messages)
		if err != nil {
			return "", err
		}

		if resp.ToolCall == nil {
			return resp.Content, nil
		}

		out, err := r.tools.Execute(
			ctx,
			resp.ToolCall.Name,
			resp.ToolCall.Args,
		)

		if err != nil {
			return "", err
		}

		messages = append(messages, llm.Message{
			Role: "tool",
			Content: toJSON(out),
		})

	}

	return "max steps", nil
}
```

---

# 三、Repo Map（代码库结构图）

Coding agent 需要知道整个 repo 的结构。

类似 Cursor 的 **repo map**。

---

## RepoMap 数据结构

`repo/map.go`

```go
package repo

type FileNode struct {
	Path string
}

type RepoMap struct {
	Files []FileNode
}
```

---

## 构建 Repo Map

```go
func BuildRepoMap(root string) (*RepoMap, error) {

	var files []FileNode

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {

		if info.IsDir() {
			return nil
		}

		files = append(files, FileNode{
			Path: path,
		})

		return nil
	})

	return &RepoMap{Files: files}, nil
}
```

---

## Repo Map Prompt

```go
func (r *RepoMap) Summary() string {

	var sb strings.Builder

	sb.WriteString("Repository structure:\n")

	for _, f := range r.Files {

		sb.WriteString(f.Path)
		sb.WriteString("\n")

	}

	return sb.String()
}
```

---

# 四、AST Code Index（真正智能代码搜索）

普通 embedding 不够。

必须 **解析 AST**。

---

## Go AST 解析

`index/ast.go`

```go
package index

import (
	"go/ast"
	"go/parser"
	"go/token"
)

type Symbol struct {
	Name string
	File string
}

func ParseFile(path string) ([]Symbol, error) {

	fset := token.NewFileSet()

	node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)

	if err != nil {
		return nil, err
	}

	var symbols []Symbol

	ast.Inspect(node, func(n ast.Node) bool {

		switch t := n.(type) {

		case *ast.FuncDecl:

			symbols = append(symbols, Symbol{
				Name: t.Name.Name,
				File: path,
			})

		}

		return true
	})

	return symbols, nil
}
```

---

## Symbol Index

```go
type SymbolIndex struct {
	data map[string][]Symbol
}

func (s *SymbolIndex) Add(sym Symbol) {

	s.data[sym.Name] = append(s.data[sym.Name], sym)
}
```

---

# 五、Token Budget Manager

Coding agent 最大问题：

```
context overflow
```

必须管理 token。

---

## Token Budget

`runtime/token_budget.go`

```go
package runtime

type Budget struct {
	Max int
	Used int
}

func (b *Budget) Add(tokens int) bool {

	if b.Used+tokens > b.Max {
		return false
	}

	b.Used += tokens

	return true
}
```

---

## Context Pruning

```go
func TrimContext(ctx []string, budget int) []string {

	var out []string
	total := 0

	for _, c := range ctx {

		t := estimateTokens(c)

		if total+t > budget {
			break
		}

		total += t

		out = append(out, c)

	}

	return out
}
```

---

# 六、最终 Agent Pipeline

真正 Coding Agent 的完整流程：

```
User Prompt
      │
      ▼
Planner
      │
      ▼
Plan
      │
      ▼
Repo Map
      │
      ▼
Code Index
      │
      ▼
Context Builder
      │
      ▼
Token Budget Manager
      │
      ▼
ReAct Loop
      │
      ▼
Tool Runtime
      │
      ▼
Sandbox Executor
      │
      ▼
Result
```

---

# 七、完整目录结构

最终系统：

```
agent/
 ├ cli
 ├ runtime
 │    agent.go
 │    react_loop.go
 │    token_budget.go
 │
 ├ planner
 │    planner.go
 │
 ├ skill
 │    capability.go
 │
 ├ tool
 │    registry.go
 │
 ├ memory
 │    vector_store.go
 │
 ├ index
 │    ast.go
 │
 ├ repo
 │    map.go
 │
 ├ sandbox
 │    shell.go
 │
 └ llm
      sse.go
```

总代码规模：

```
≈ 6000 行 Go
```

---

# 八、真正 Cursor / Claude Code 还有一个“终极系统”

最先进 coding agent 都有：

### **Multi-Agent Runtime**

不是一个 agent，而是：

```
planner agent
coder agent
review agent
test agent
```

例如：

```
task
  │
  ▼
planner
  │
  ▼
coder
  │
  ▼
reviewer
  │
  ▼
tester
```

---

如果你愿意，我可以 **直接给你一套真正 Cursor 级 Golang Agent 架构（约 9000 行设计）**，包括：

* Claude Code Skills Loader
* Capability Graph
* Multi-Agent Runtime
* Repo Map
* AST Code Index
* Streaming Tool Calls
* Parallel Tool Execution
* Code Patch Engine
* Git Auto Commit
* Test Runner

这套基本已经是 **完整 Coding Agent CLI 框架**。
