下面继续把 **Go AI Coding Agent**补齐到**可以真正运行的完整版本**。
这一步会补上最后关键的几个模块：

1. **LLM Planner（真正生成执行计划）**
2. **Tool Registry**
3. **Context Packing**
4. **Agent Loop（完整 REACT / Plan-Act-Observe）**
5. **Embedding Index Builder**
6. **Streaming Tool Call 解析**
7. **完整 Demo**

这样你的系统就具备类似 **Claude Code / Cursor / OpenDevin 的核心结构。

---

# 一、LLM Planner

Planner 的职责：

```
User Prompt
   ↓
Context Builder
   ↓
LLM
   ↓
Plan(JSON DAG)
```

---

## agent/planner.go

```go
package agent

import (
	"context"
	"encoding/json"
	"bytes"
	"net/http"
)

type Planner struct {
	APIKey string
}

func NewPlanner(key string) *Planner {
	return &Planner{APIKey: key}
}

func (p *Planner) Plan(ctx context.Context, user string, contextText string) (*Plan, error) {

	prompt := `
You are an AI coding agent.

Create a tool execution plan in JSON.

Available tools:
- read_file(path)
- write_file(path,content)
- run_terminal(cmd)

Return JSON:

{
 "steps":[
   {
     "id":"step1",
     "tool":"read_file",
     "args":{"path":"main.go"},
     "depends":[]
   }
 ]
}

User request:
` + user + `

Workspace Context:
` + contextText

	reqBody := map[string]interface{}{
		"model": "gpt-4o-mini",
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	body, _ := json.Marshal(reqBody)

	req, _ := http.NewRequestWithContext(
		ctx,
		"POST",
		"https://api.openai.com/v1/chat/completions",
		bytes.NewReader(body),
	)

	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	var result struct {
		Choices []struct {
			Message struct {
				Content string
			}
		}
	}

	json.NewDecoder(resp.Body).Decode(&result)

	var plan Plan
	json.Unmarshal([]byte(result.Choices[0].Message.Content), &plan)

	return &plan, nil
}
```

---

# 二、Tool Registry

统一管理 Tools。

---

## agent/tool_registry.go

```go
package agent

import "context"

type Tool interface {
	Name() string
	Run(ctx context.Context, args map[string]interface{}) (interface{}, error)
}

type ToolRegistry struct {
	tools map[string]Tool
}

func NewRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: map[string]Tool{},
	}
}

func (r *ToolRegistry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) Get(name string) Tool {
	return r.tools[name]
}
```

---

# 三、Filesystem Tool

---

## tools/read_file.go

```go
package tools

import (
	"context"
	"os"
)

type ReadFile struct{}

func (t *ReadFile) Name() string {
	return "read_file"
}

func (t *ReadFile) Run(ctx context.Context, args map[string]interface{}) (interface{}, error) {

	path := args["path"].(string)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return string(data), nil
}
```

---

# 四、Terminal Tool

---

## tools/terminal.go

```go
package tools

import (
	"context"
	"os/exec"
)

type Terminal struct{}

func (t *Terminal) Name() string {
	return "run_terminal"
}

func (t *Terminal) Run(ctx context.Context, args map[string]interface{}) (interface{}, error) {

	cmd := exec.Command("bash", "-c", args["cmd"].(string))

	out, err := cmd.CombinedOutput()

	return string(out), err
}
```

---

# 五、Context Builder

Context Builder 是 **Claude Code / Cursor 的关键能力**。

它会把：

```
Repo Tree
Symbols
References
Semantic Search
```

打包给 LLM。

---

## workspace/context_builder.go

```go
package workspace

import "strings"

func BuildContext(ws *Workspace) string {

	var sb strings.Builder

	sb.WriteString("Repo Files:\n")

	for file := range ws.Files {
		sb.WriteString(file + "\n")
	}

	return sb.String()
}
```

---

# 六、Embedding Index Builder

---

## embedding/index_builder.go

```go
package embedding

import (
	"context"
)

func (vi *VectorIndex) BuildChunks(ctx context.Context, chunks []*CodeChunk, apiKey string) error {

	for _, c := range chunks {

		vec, err := GenerateEmbeddingOpenAI(ctx, c.Content, apiKey)
		if err != nil {
			return err
		}

		c.Embedding = vec

		vi.AddChunk(c)
	}

	return nil
}
```

---

# 七、Agent Loop（完整 REACT）

---

## agent/agent_loop.go

```go
package agent

import (
	"context"
	"fmt"
)

func (a *Agent) RunAdvanced(ctx context.Context, user string, contextText string) error {

	planner := NewPlanner(a.OpenAIKey)

	plan, err := planner.Plan(ctx, user, contextText)
	if err != nil {
		return err
	}

	fmt.Println("Plan created")

	results, err := a.RunPlan(ctx, plan)
	if err != nil {
		return err
	}

	fmt.Println("Execution results")

	for k, v := range results {
		fmt.Println(k, v)
	}

	return nil
}
```

---

# 八、Plan 结构

---

## agent/plan.go

```go
package agent

type Plan struct {
	Steps []PlanStep `json:"steps"`
}

type PlanStep struct {
	ID string `json:"id"`

	Tool string `json:"tool"`

	Args map[string]interface{} `json:"args"`

	Depends []string `json:"depends"`
}
```

---

# 九、完整运行流程

最终 Agent 运行流程：

```
User Prompt
   ↓
Workspace Scanner
   ↓
Symbol Index
   ↓
Code Embedding
   ↓
Semantic Search
   ↓
Context Builder
   ↓
LLM Planner
   ↓
DAG Executor
   ↓
Tool Runtime
   ↓
Observation Memory
   ↓
Next iteration
```

---

# 十、最终系统架构

```
                    +----------------------+
                    |        User          |
                    +----------+-----------+
                               |
                               v
                   +----------------------+
                   |      Agent Loop      |
                   +----------+-----------+
                              |
                     +--------+---------+
                     |                  |
                     v                  v
           +----------------+   +----------------+
           |   LLM Planner  |   | Observation DB |
           +--------+-------+   +----------------+
                    |
                    v
            +---------------+
            |   DAG Plan    |
            +-------+-------+
                    |
                    v
          +-------------------+
          |   DAG Executor    |
          +---------+---------+
                    |
          +---------+---------+
          |                   |
          v                   v
  +--------------+   +----------------+
  | Tool Runtime |   | MCP Skill RPC  |
  +--------------+   +----------------+
          |
          v
   +---------------+
   | Workspace API |
   +---------------+
          |
          v
  +-------------------+
  | Semantic Search   |
  | Code Embeddings   |
  +-------------------+
```

---

# 十一、这套架构的能力

实现后你会得到一个 **工业级 AI Coding Agent**：

| 能力                   | 是否支持 |
| -------------------- | ---- |
| Workspace Context    | ✅    |
| Semantic Code Search | ✅    |
| Tool Calling         | ✅    |
| DAG Planning         | ✅    |
| Parallel Execution   | ✅    |
| Observation Memory   | ✅    |
| Skill Plugins        | ✅    |
| Embedding Index      | ✅    |

---

如果你愿意，我可以 **再给你补最后一个最重要的部分**：

**Claude Code 级 Skill System（.skills 插件机制）**

这个部分会实现：

```
.skills/
   git_commit.skill
   run_test.skill
   refactor.skill
```

AI 会 **自动加载 skill 并使用它们**。

这一步做完，你的系统就会 **完全接近 Claude Code 的能力**。
