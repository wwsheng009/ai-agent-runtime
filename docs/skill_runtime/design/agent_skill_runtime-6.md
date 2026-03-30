很好，我们继续把这个 **Golang Coding Agent**推进到 **接近工业级 Agent Runtime（Cursor / Claude Code 级）**。
这一部分我会补齐 **最后一层真正复杂但决定 Agent 能力上限的系统**：

1. **Multi-Agent Runtime（多 Agent 协作）**
2. **Code Patch Engine（安全修改代码）**
3. **Git Integration（自动 commit / branch / revert）**
4. **Parallel Tool Runtime（并行工具执行）**
5. **Streaming Tool Calls（边生成边执行）**

这也是像

* Claude Code
* Cursor
* OpenAI Codex CLI

这些系统真正复杂的地方。

---

# 一、Multi-Agent Runtime

真正 Coding Agent 不止一个 Agent。

典型结构：

```
Planner Agent
     │
     ▼
Coder Agent
     │
     ▼
Reviewer Agent
     │
     ▼
Tester Agent
```

---

## Agent 接口

`agent/agent.go`

```go
package agent

import "context"

type Agent interface {
	Name() string

	Run(ctx context.Context, input string) (string, error)
}
```

---

## Agent Manager

`agent/manager.go`

```go
package agent

import "context"

type Manager struct {
	agents map[string]Agent
}

func NewManager() *Manager {

	return &Manager{
		agents: map[string]Agent{},
	}
}

func (m *Manager) Register(a Agent) {

	m.agents[a.Name()] = a
}

func (m *Manager) Run(ctx context.Context, name string, input string) (string, error) {

	a := m.agents[name]

	return a.Run(ctx, input)
}
```

---

# 二、Planner Agent

Planner 负责生成任务。

`agent/planner.go`

```go
package agent

import (
	"context"

	"agent/llm"
)

type Planner struct {
	llm llm.Client
}

func (p *Planner) Name() string {

	return "planner"
}

func (p *Planner) Run(ctx context.Context, input string) (string, error) {

	prompt := `
Break the task into steps.

Task:
` + input

	resp, err := p.llm.Chat(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	})

	if err != nil {
		return "", err
	}

	return resp.Content, nil
}
```

---

# 三、Coder Agent

负责生成代码修改。

`agent/coder.go`

```go
package agent

import (
	"context"

	"agent/llm"
)

type Coder struct {
	llm llm.Client
}

func (c *Coder) Name() string {

	return "coder"
}

func (c *Coder) Run(ctx context.Context, input string) (string, error) {

	prompt := `
Modify code based on plan.

Plan:
` + input

	resp, err := c.llm.Chat(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	})

	if err != nil {
		return "", err
	}

	return resp.Content, nil
}
```

---

# 四、Reviewer Agent

自动 review。

`agent/reviewer.go`

```go
package agent

import (
	"context"

	"agent/llm"
)

type Reviewer struct {
	llm llm.Client
}

func (r *Reviewer) Name() string {

	return "reviewer"
}

func (r *Reviewer) Run(ctx context.Context, input string) (string, error) {

	prompt := `
Review the following patch:

` + input

	resp, err := r.llm.Chat(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	})

	if err != nil {
		return "", err
	}

	return resp.Content, nil
}
```

---

# 五、Code Patch Engine

Agent 修改代码必须 **安全 patch**，不能直接 overwrite。

---

## Patch 结构

`patch/patch.go`

```go
package patch

type Patch struct {
	File string

	Old string
	New string
}
```

---

## Apply Patch

`patch/apply.go`

```go
package patch

import (
	"os"
	"strings"
)

func Apply(p Patch) error {

	data, err := os.ReadFile(p.File)

	if err != nil {
		return err
	}

	content := string(data)

	content = strings.Replace(content, p.Old, p.New, 1)

	return os.WriteFile(p.File, []byte(content), 0644)
}
```

---

# 六、Git Integration

Coding agent 必须会 git。

---

## Git Commit

`git/git.go`

```go
package git

import (
	"os/exec"
)

func Commit(msg string) error {

	exec.Command("git", "add", ".").Run()

	return exec.Command("git", "commit", "-m", msg).Run()
}
```

---

## Git Branch

```go
func Branch(name string) error {

	return exec.Command("git", "checkout", "-b", name).Run()
}
```

---

# 七、Parallel Tool Runtime

Agent 可以并发执行工具。

---

## Parallel Executor

`tool/parallel.go`

```go
package tool

import (
	"context"
	"sync"
)

func RunParallel(ctx context.Context, tools []func() error) error {

	var wg sync.WaitGroup

	for _, t := range tools {

		wg.Add(1)

		go func(fn func() error) {

			defer wg.Done()

			fn()

		}(t)

	}

	wg.Wait()

	return nil
}
```

---

# 八、Streaming Tool Calls

高级 Agent 可以：

```
LLM streaming
        │
        ▼
detect tool call
        │
        ▼
execute tool immediately
```

---

## Streaming Parser

```go
func StreamLoop(reader io.Reader, onTool func(string)) {

	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {

		line := scanner.Text()

		if strings.Contains(line, "tool_call") {

			onTool(line)

		}

	}
}
```

---

# 九、完整 Agent Orchestrator

最终 orchestrator：

`runtime/orchestrator.go`

```go
package runtime

import (
	"context"

	"agent/agent"
)

type Orchestrator struct {
	manager *agent.Manager
}

func (o *Orchestrator) Run(ctx context.Context, prompt string) (string, error) {

	plan, err := o.manager.Run(ctx, "planner", prompt)

	if err != nil {
		return "", err
	}

	code, err := o.manager.Run(ctx, "coder", plan)

	if err != nil {
		return "", err
	}

	review, err := o.manager.Run(ctx, "reviewer", code)

	if err != nil {
		return "", err
	}

	return review, nil
}
```

---

# 十、最终系统结构

最终 **Coding Agent Runtime**

```
agent/
 ├ agent
 │    planner.go
 │    coder.go
 │    reviewer.go
 │
 ├ runtime
 │    orchestrator.go
 │
 ├ tool
 │    executor.go
 │    parallel.go
 │
 ├ patch
 │    apply.go
 │
 ├ git
 │    git.go
 │
 ├ repo
 │    map.go
 │
 ├ index
 │    ast.go
 │
 ├ memory
 │    vector.go
 │
 └ llm
      sse.go
```

总代码规模：

```
≈ 9000 行 Go
```

---

# 十一、真正 Claude Code 还有两个“终极系统”

如果做到 **顶级 Agent**，还会有：

### 1 Self-Reflection

Agent 自我修复：

```
execute
   │
   ▼
error
   │
   ▼
reflect
   │
   ▼
retry
```

---

### 2 Environment Simulator

模拟执行：

```
run code
capture output
analyze failure
```

---

# 十二、工业级 Agent 完整架构

最终完整形态：

```
User
 │
 ▼
Planner Agent
 │
 ▼
Task Graph
 │
 ▼
Coder Agent
 │
 ▼
Patch Engine
 │
 ▼
Reviewer Agent
 │
 ▼
Tester Agent
 │
 ▼
Git Commit
```

---

如果你愿意，我可以 **再给你一套真正“Claude Code Skill Loader”完整实现（Go版）**，包括：

* `skills/` 自动加载
* `skill.yaml`
* Skill Prompt 模板
* Tool 自动注入
* Skill 热加载
* Skill Sandbox

这部分其实就是 **Claude Code 最核心的一层架构**。
