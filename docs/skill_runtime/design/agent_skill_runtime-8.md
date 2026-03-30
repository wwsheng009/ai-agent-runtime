下面我给你一套 **真正可以落地的 Claude Code / Cursor / Copilot CLI 类似的 Skill Runtime 完整实现**。
目标是实现：

**AI Agent → 自动加载 Skills → Tool 调用 → 执行 → 返回结果**

架构包含：

```
ai-agent
 ├─ runtime
 │   ├─ skill_loader.go
 │   ├─ skill_registry.go
 │   ├─ skill_executor.go
 │   ├─ skill_manifest.go
 │   └─ prompt_injector.go
 │
 ├─ skills
 │   ├─ fs
 │   │   ├─ skill.yaml
 │   │   └─ tool.go
 │   │
 │   └─ git
 │       ├─ skill.yaml
 │       └─ tool.go
 │
 └─ main.go
```

---

# 一、Skill Manifest 设计（Claude Code同款）

每个 skill 一个 `skill.yaml`

```
skills/fs/skill.yaml
```

```yaml
name: filesystem
description: Access and manipulate files

tools:
  - name: read_file
    description: Read file content
    parameters:
      type: object
      properties:
        path:
          type: string
      required: [path]

  - name: write_file
    description: Write file content
    parameters:
      type: object
      properties:
        path:
          type: string
        content:
          type: string
      required: [path, content]
```

---

# 二、Skill Manifest 解析

```
runtime/skill_manifest.go
```

```go
package runtime

import (
	"os"

	"gopkg.in/yaml.v3"
)

type SkillManifest struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description"`
	Tools       []ToolSchema  `yaml:"tools"`
}

type ToolSchema struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Parameters  map[string]interface{} `yaml:"parameters"`
}

func LoadManifest(path string) (*SkillManifest, error) {

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var m SkillManifest
	err = yaml.Unmarshal(data, &m)

	return &m, err
}
```

---

# 三、Skill Registry

负责管理所有 tool。

```
runtime/skill_registry.go
```

```go
package runtime

import "context"

type ToolFunc func(ctx context.Context, args map[string]interface{}) (any, error)

type Tool struct {
	Schema ToolSchema
	Run    ToolFunc
}

type Registry struct {
	tools map[string]*Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools: map[string]*Tool{},
	}
}

func (r *Registry) Register(schema ToolSchema, fn ToolFunc) {

	r.tools[schema.Name] = &Tool{
		Schema: schema,
		Run:    fn,
	}
}

func (r *Registry) Get(name string) (*Tool, bool) {

	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) ListSchemas() []ToolSchema {

	var s []ToolSchema

	for _, t := range r.tools {
		s = append(s, t.Schema)
	}

	return s
}
```

---

# 四、Skill Loader

扫描 skills 目录并加载。

```
runtime/skill_loader.go
```

```go
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
)

type Loader struct {
	Registry *Registry
}

func NewLoader(r *Registry) *Loader {
	return &Loader{Registry: r}
}

func (l *Loader) LoadSkills(root string) error {

	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	for _, e := range entries {

		if !e.IsDir() {
			continue
		}

		skillPath := filepath.Join(root, e.Name())
		manifest := filepath.Join(skillPath, "skill.yaml")

		if _, err := os.Stat(manifest); err != nil {
			continue
		}

		m, err := LoadManifest(manifest)
		if err != nil {
			return err
		}

		fmt.Println("load skill:", m.Name)

		err = l.loadTools(m)
		if err != nil {
			return err
		}
	}

	return nil
}

func (l *Loader) loadTools(m *SkillManifest) error {

	for _, tool := range m.Tools {

		fn, ok := builtinTools[tool.Name]
		if !ok {
			return fmt.Errorf("tool impl missing: %s", tool.Name)
		}

		l.Registry.Register(tool, fn)
	}

	return nil
}
```

---

# 五、Tool 实现

```
runtime/tools_builtin.go
```

```go
package runtime

import (
	"context"
	"os"
)

var builtinTools = map[string]ToolFunc{

	"read_file": func(ctx context.Context, args map[string]interface{}) (any, error) {

		path := args["path"].(string)

		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}

		return string(data), nil
	},

	"write_file": func(ctx context.Context, args map[string]interface{}) (any, error) {

		path := args["path"].(string)
		content := args["content"].(string)

		err := os.WriteFile(path, []byte(content), 0644)

		return "ok", err
	},
}
```

---

# 六、Tool Executor

负责执行 AI 请求的 tool。

```
runtime/skill_executor.go
```

```go
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
)

type ToolCall struct {
	Name      string
	Arguments json.RawMessage
}

type Executor struct {
	Registry *Registry
}

func NewExecutor(r *Registry) *Executor {
	return &Executor{Registry: r}
}

func (e *Executor) Execute(ctx context.Context, call ToolCall) (any, error) {

	tool, ok := e.Registry.Get(call.Name)
	if !ok {
		return nil, fmt.Errorf("tool not found")
	}

	var args map[string]interface{}

	err := json.Unmarshal(call.Arguments, &args)
	if err != nil {
		return nil, err
	}

	return tool.Run(ctx, args)
}
```

---

# 七、Prompt 自动注入 Tools

让 AI 知道有哪些 tools。

```
runtime/prompt_injector.go
```

```go
package runtime

import (
	"encoding/json"
)

func BuildToolPrompt(reg *Registry) string {

	schemas := reg.ListSchemas()

	data, _ := json.MarshalIndent(schemas, "", "  ")

	return `
You can use the following tools:

` + string(data) + `

When you want to call a tool respond:

{
 "tool_call": {
   "name": "tool_name",
   "arguments": {...}
 }
}
`
}
```

---

# 八、AI Tool Call 解析（OpenAI / Claude 通用）

```
runtime/ai_parser.go
```

```go
package runtime

import (
	"encoding/json"
)

type AIResponse struct {
	ToolCall *ToolCall `json:"tool_call"`
	Text     string    `json:"text"`
}

func ParseAIResponse(data []byte) (*AIResponse, error) {

	var r AIResponse
	err := json.Unmarshal(data, &r)

	return &r, err
}
```

---

# 九、完整运行示例

```
main.go
```

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"agent/runtime"
)

func main() {

	reg := runtime.NewRegistry()

	loader := runtime.NewLoader(reg)

	err := loader.LoadSkills("./skills")
	if err != nil {
		panic(err)
	}

	exec := runtime.NewExecutor(reg)

	// 模拟 AI 返回

	ai := `{
	  "tool_call": {
	    "name": "read_file",
	    "arguments": {
	      "path": "test.txt"
	    }
	  }
	}`

	resp, _ := runtime.ParseAIResponse([]byte(ai))

	if resp.ToolCall != nil {

		out, err := exec.Execute(context.Background(), *resp.ToolCall)

		if err != nil {
			panic(err)
		}

		j, _ := json.MarshalIndent(out, "", " ")

		fmt.Println(string(j))
	}
}
```

---

# 十、运行流程

完整流程：

```
启动
 ↓
加载 skills/*
 ↓
解析 skill.yaml
 ↓
注册 Tool
 ↓
生成 Tool Prompt
 ↓
发送给 AI
 ↓
AI 返回 ToolCall
 ↓
解析 ToolCall
 ↓
执行 Tool
 ↓
结果返回 AI
```

---

# 十一、Claude Code / Cursor 的真实 Skill 架构

真实结构其实是：

```
Agent Runtime
   │
   ├── Skill Loader
   │
   ├── Tool Registry
   │
   ├── Prompt Builder
   │
   ├── Tool Router
   │
   └── Sandbox Executor
```

每个 Skill 可以提供：

```
skill/
   skill.yaml
   tools.go
   prompt.md
   examples.md
```

Claude Code 实际还支持：

```
MCP server skill
Shell skill
Filesystem skill
Git skill
Browser skill
```

---

# 十二、如果做 **工业级 Claude Code Runtime**

需要再加 **5个核心模块**：

### 1 Skill 热加载

```
fsnotify
```

自动 reload skill。

---

### 2 Tool Sandbox

防止 AI 执行危险操作

```
seccomp
container
wasm
```

---

### 3 权限系统

```
tool permission

read_file -> allow
delete_file -> confirm
```

---

### 4 Skill Prompt DSL

```
skill:
  system_prompt: |
     You are a git expert
```

自动注入 prompt。

---

### 5 Skill Planning

AI 不直接 tool call：

```
plan
  step1
  step2
```

再执行。

---

# 十三、真正 Claude Code 的 Skill 运行流程

实际是：

```
User
 ↓
Agent
 ↓
Planner
 ↓
Skill Selection
 ↓
Tool Call
 ↓
Executor
 ↓
Observation
 ↓
Next Step
```

就是 **ReAct Agent Loop**。

---

如果你愿意，我可以 **继续给你补完整工业级实现**：

包括：

* **Claude Code 完整 Agent Loop**
* **Tool Planner**
* **多 Skill 自动选择**
* **MCP Skill**
* **SSE Tool Streaming**
* **OpenAI / Claude / Gemini 统一 Tool Call Runtime**

并给你一套 **3000 行级别 Go Agent Framework（可以直接做 Cursor/Claude CLI）**。
