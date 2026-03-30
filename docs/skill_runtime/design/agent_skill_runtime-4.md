下面我继续把前面的 **Golang Agent Skill Runtime**推进到 **真正接近生产级 Coding Agent（Cursor / Claude Code 级）**。

这一部分补齐 **最关键但前面没展开的 4 个核心系统**：

1. **Memory / Vector Store（技能选择 + 上下文记忆）**
2. **Codebase Index（代码库语义检索）**
3. **Sandbox Executor（安全执行 shell / git / file）**
4. **统一 SSE LLM Runtime（OpenAI / Anthropic / Gemini）**

涉及到的系统类似：

* Claude Code
* OpenAI Codex CLI
* Cursor

最终系统结构：

```
Agent Runtime
 ├── Router
 ├── Capability Graph
 ├── Workflow Engine
 ├── Tool Runtime
 ├── LLM Runtime (SSE)
 ├── Memory
 ├── Code Index
 └── Sandbox
```

---

# 一、Memory System（Agent 记忆）

Agent 必须有 **长期记忆 + 会话记忆**。

目录：

```
memory/
   store.go
   vector.go
```

## Vector 定义

`memory/vector.go`

```go
package memory

type Vector struct {
	ID    string
	Value []float64
	Meta  map[string]string
}
```

---

## Vector Store

`memory/store.go`

```go
package memory

import "math"

type Store struct {
	data []Vector
}

func NewStore() *Store {
	return &Store{}
}

func (s *Store) Add(v Vector) {

	s.data = append(s.data, v)
}

func (s *Store) Search(query []float64, k int) []Vector {

	type pair struct {
		v     Vector
		score float64
	}

	var res []pair

	for _, v := range s.data {

		score := cosine(query, v.Value)

		res = append(res, pair{v, score})
	}

	sort.Slice(res, func(i, j int) bool {
		return res[i].score > res[j].score
	})

	var out []Vector

	for i := 0; i < k && i < len(res); i++ {
		out = append(out, res[i].v)
	}

	return out
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

# 二、Codebase Index（代码库语义索引）

Coding agent 的关键能力。

目录：

```
index/
   index.go
   parser.go
```

---

## Code Index

`index/index.go`

```go
package index

import (
	"io/fs"
	"os"
	"path/filepath"

	"agent/memory"
)

type CodeIndex struct {
	store *memory.Store
}

func New() *CodeIndex {

	return &CodeIndex{
		store: memory.NewStore(),
	}
}

func (c *CodeIndex) Build(root string) error {

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {

		if d.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		vec := embed(string(data))

		c.store.Add(memory.Vector{
			ID: path,
			Value: vec,
		})

		return nil
	})
}
```

---

## Code Search

```go
func (c *CodeIndex) Search(query string) []string {

	vec := embed(query)

	res := c.store.Search(vec, 5)

	var out []string

	for _, v := range res {
		out = append(out, v.ID)
	}

	return out
}
```

---

# 三、Sandbox Executor（安全执行）

Coding agent 必须限制系统权限。

目录：

```
sandbox/
   shell.go
   fs.go
```

---

## Shell Executor

`sandbox/shell.go`

```go
package sandbox

import (
	"context"
	"os/exec"
)

type Shell struct{}

func (s *Shell) Run(ctx context.Context, cmd string) (string, error) {

	c := exec.CommandContext(ctx, "bash", "-c", cmd)

	out, err := c.CombinedOutput()

	return string(out), err
}
```

---

## File System Tool

`sandbox/fs.go`

```go
package sandbox

import "os"

func ReadFile(path string) (string, error) {

	data, err := os.ReadFile(path)

	return string(data), err
}

func WriteFile(path, content string) error {

	return os.WriteFile(path, []byte(content), 0644)
}
```

---

# 四、统一 SSE LLM Runtime

Coding agent 的 LLM 调用必须支持 streaming。

目录：

```
llm/
   sse.go
   anthropic.go
   openai.go
```

---

## SSE Reader

`llm/sse.go`

```go
package llm

import (
	"bufio"
	"io"
	"strings"
)

func ReadSSE(r io.Reader, fn func(string)) error {

	scanner := bufio.NewScanner(r)

	for scanner.Scan() {

		line := scanner.Text()

		if strings.HasPrefix(line, "data: ") {

			data := strings.TrimPrefix(line, "data: ")

			if data == "[DONE]" {
				break
			}

			fn(data)

		}

	}

	return nil
}
```

---

# 五、Anthropic SSE 解析

类似 **Claude Code**。

`llm/anthropic.go`

```go
type ClaudeEvent struct {
	Type string `json:"type"`
	Delta struct {
		Text string `json:"text"`
	} `json:"delta"`
}
```

解析：

```go
func ParseClaude(data string) string {

	var e ClaudeEvent

	json.Unmarshal([]byte(data), &e)

	return e.Delta.Text
}
```

---

# 六、OpenAI SSE 解析

类似 **Codex CLI**。

```go
type OpenAIChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}
```

---

# 七、Agent Prompt Builder（关键）

Coding agent prompt 需要：

```
system
tools
context
user
```

实现：

`runtime/prompt_builder.go`

```go
package runtime

import "strings"

type PromptBuilder struct {
}

func (p *PromptBuilder) Build(ctx []string, user string) string {

	var sb strings.Builder

	sb.WriteString("Context:\n")

	for _, c := range ctx {
		sb.WriteString(c)
		sb.WriteString("\n")
	}

	sb.WriteString("\nUser:\n")
	sb.WriteString(user)

	return sb.String()
}
```

---

# 八、完整 Agent Pipeline

最终运行流程：

```
User Prompt
     │
     ▼
Embedding Router
     │
     ▼
Capability Selected
     │
     ▼
Codebase Index Search
     │
     ▼
Context Builder
     │
     ▼
Prompt Builder
     │
     ▼
LLM Streaming
     │
     ▼
Tool Calls
     │
     ▼
Sandbox Executor
     │
     ▼
Return Result
```

---

# 九、Coding Agent CLI 示例

最终 CLI：

```go
func main() {

	agent := runtime.NewAgent()

	ctx := context.Background()

	for {

		var prompt string

		fmt.Print("> ")
		fmt.Scanln(&prompt)

		out, err := agent.Run(ctx, prompt)

		if err != nil {
			fmt.Println(err)
			continue
		}

		fmt.Println(out)
	}
}
```

---

# 十、完整系统规模

完整 Coding Agent：

```
≈ 4000 行 Go
```

模块：

```
agent/
 ├ runtime
 ├ skill
 ├ tool
 ├ memory
 ├ index
 ├ sandbox
 ├ llm
 └ cli
```

---

# 十一、真正 Cursor / Claude Code 还有两个隐藏核心

如果你要做到 **行业最先进 Agent**，还需要：

### 1 AST Code Index

不是普通 embedding：

```
symbol index
type index
dependency graph
```

---

### 2 Planner

高级 Agent 会先 **生成执行计划**

```
Plan
  ↓
Execute
```

例如：

```
1 search code
2 modify file
3 run tests
```

---

如果你需要，我可以 **再给你一套真正完整的 Golang Coding Agent 架构（约 6000 行设计）**，里面会包含：

* Claude Code Skills 完整机制
* Capability Graph
* Planner（ReAct + Tool Planning）
* AST Code Index
* Repo Map（Cursor 同款）
* 并发 Tool Runtime
* Token Budget 管理
* Streaming Tool Calls

基本已经是 **Cursor CLI / Claude Code 级架构**。
