下面继续把这套 **Go Agent + Skills 系统**升级到接近**Claude Code / Cursor / OpenDevin** 的核心能力。

这一步实现 **最关键的一层**：

**MCP (Model Context Protocol) Runtime**

Claude Code 的 skills 实际并不是本地函数，而是：

```
AI Agent
   │
   ▼
MCP Client
   │
   ▼
MCP Server
   │
   ├ filesystem
   ├ github
   ├ browser
   └ terminal
```

也就是：

```
skills = remote tool servers
```

下面给出 **完整 Go 实现框架**。

---

# 一、MCP 基本协议

MCP 是 **JSON-RPC over stdio / http**。

基本请求：

```json
{
  "jsonrpc":"2.0",
  "id":1,
  "method":"tools/list"
}
```

响应：

```json
{
  "jsonrpc":"2.0",
  "id":1,
  "result":{
    "tools":[
      {
        "name":"read_file",
        "description":"read file",
        "input_schema":{}
      }
    ]
  }
}
```

调用 tool：

```json
{
  "jsonrpc":"2.0",
  "id":2,
  "method":"tools/call",
  "params":{
    "name":"read_file",
    "arguments":{
      "path":"main.go"
    }
  }
}
```

---

# 二、MCP 数据结构

### mcp/types.go

```go
package mcp

type Request struct {

	JSONRPC string `json:"jsonrpc"`

	ID int `json:"id"`

	Method string `json:"method"`

	Params map[string]interface{} `json:"params,omitempty"`
}

type Response struct {

	JSONRPC string `json:"jsonrpc"`

	ID int `json:"id"`

	Result interface{} `json:"result,omitempty"`

	Error *Error `json:"error,omitempty"`
}

type Error struct {

	Code int `json:"code"`

	Message string `json:"message"`
}
```

---

# 三、Tool Schema

```go
package mcp

type Tool struct {

	Name string `json:"name"`

	Description string `json:"description"`

	InputSchema map[string]interface{} `json:"input_schema"`
}
```

---

# 四、MCP Client

Agent 通过 MCP Client 调用 skills。

---

### mcp/client.go

```go
package mcp

import (
	"bufio"
	"encoding/json"
	"os/exec"
)

type Client struct {

	cmd *exec.Cmd

	stdin *bufio.Writer

	stdout *bufio.Scanner

	nextID int
}

func NewClient(command string) (*Client, error) {

	cmd := exec.Command(command)

	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()

	err := cmd.Start()
	if err != nil {
		return nil, err
	}

	return &Client{
		cmd: cmd,
		stdin: bufio.NewWriter(in),
		stdout: bufio.NewScanner(out),
		nextID: 1,
	}, nil
}
```

---

# 五、发送 RPC 请求

```go
func (c *Client) send(req Request) (*Response, error) {

	data, _ := json.Marshal(req)

	c.stdin.Write(data)
	c.stdin.WriteString("\n")
	c.stdin.Flush()

	if c.stdout.Scan() {

		line := c.stdout.Bytes()

		var resp Response

		json.Unmarshal(line, &resp)

		return &resp, nil
	}

	return nil, c.stdout.Err()
}
```

---

# 六、列出 Tools

```go
func (c *Client) ListTools() ([]Tool, error) {

	req := Request{
		JSONRPC: "2.0",
		ID: c.nextID,
		Method: "tools/list",
	}

	c.nextID++

	resp, err := c.send(req)
	if err != nil {
		return nil, err
	}

	var result struct {
		Tools []Tool `json:"tools"`
	}

	data, _ := json.Marshal(resp.Result)

	json.Unmarshal(data, &result)

	return result.Tools, nil
}
```

---

# 七、调用 Tool

```go
func (c *Client) CallTool(
	name string,
	args map[string]interface{},
) (interface{}, error) {

	req := Request{
		JSONRPC: "2.0",
		ID: c.nextID,
		Method: "tools/call",
		Params: map[string]interface{}{
			"name": name,
			"arguments": args,
		},
	}

	c.nextID++

	resp, err := c.send(req)
	if err != nil {
		return nil, err
	}

	return resp.Result, nil
}
```

---

# 八、MCP Server

现在实现一个 **filesystem skill server**。

---

### server/server.go

```go
package server

import (
	"bufio"
	"encoding/json"
	"os"

	"agent/mcp"
)

type Server struct {

	tools map[string]ToolFunc
}

type ToolFunc func(map[string]interface{}) (interface{}, error)
```

---

### 注册工具

```go
func NewServer() *Server {

	return &Server{
		tools: map[string]ToolFunc{},
	}
}

func (s *Server) Register(name string, fn ToolFunc) {

	s.tools[name] = fn
}
```

---

### 处理请求

```go
func (s *Server) Handle(req mcp.Request) mcp.Response {

	switch req.Method {

	case "tools/list":

		var tools []mcp.Tool

		for name := range s.tools {

			tools = append(tools, mcp.Tool{
				Name: name,
				Description: name,
			})
		}

		return mcp.Response{
			JSONRPC:"2.0",
			ID:req.ID,
			Result:map[string]interface{}{
				"tools":tools,
			},
		}

	case "tools/call":

		name := req.Params["name"].(string)

		args := req.Params["arguments"].(map[string]interface{})

		fn := s.tools[name]

		res, err := fn(args)

		if err != nil {

			return mcp.Response{
				JSONRPC:"2.0",
				ID:req.ID,
				Error:&mcp.Error{
					Message:err.Error(),
				},
			}
		}

		return mcp.Response{
			JSONRPC:"2.0",
			ID:req.ID,
			Result:res,
		}

	}

	return mcp.Response{}
}
```

---

# 九、Server Loop

```go
func (s *Server) Run() {

	scanner := bufio.NewScanner(os.Stdin)

	for scanner.Scan() {

		line := scanner.Bytes()

		var req mcp.Request

		json.Unmarshal(line, &req)

		resp := s.Handle(req)

		data, _ := json.Marshal(resp)

		os.Stdout.Write(data)
		os.Stdout.Write([]byte("\n"))
	}
}
```

---

# 十、Filesystem Skill

```go
func ReadFile(args map[string]interface{}) (interface{}, error) {

	path := args["path"].(string)

	data, err := os.ReadFile(path)

	if err != nil {
		return nil, err
	}

	return string(data), nil
}
```

---

# 十一、Server main

```go
func main() {

	s := NewServer()

	s.Register("read_file", ReadFile)

	s.Run()
}
```

---

# 十二、Agent 使用 MCP Skill

```go
client,_ := mcp.NewClient("./filesystem_skill")

tools,_ := client.ListTools()

result,_ := client.CallTool(
    "read_file",
    map[string]interface{}{
        "path":"main.go",
    },
)

fmt.Println(result)
```

---

# 十三、最终 Agent Runtime 架构

完整结构：

```
agent
│
├─ runtime
│  ├ agent_loop.go
│  ├ planner.go
│  ├ dag_executor.go
│
├─ mcp
│  ├ client.go
│  ├ types.go
│
├─ servers
│  ├ filesystem
│  ├ git
│  ├ browser
│  └ terminal
│
└ main.go
```

---

# 十四、Claude Code 实际 Skill 架构

真实架构其实是：

```
Claude Code
    │
    ▼
MCP Client
    │
    ▼
┌───────────────┐
│ filesystem    │
│ git           │
│ github        │
│ browser       │
│ terminal      │
└───────────────┘
```

每个 skill 都是：

```
独立进程
```

好处：

* sandbox
* crash isolation
* 语言无关
* 可远程部署

---

# 十五、如果继续升级（真正 Claude Code 内核）

还缺 **3 个最关键系统**：

### 1 Workspace Context Engine

自动给 AI：

```
repo tree
recent files
git status
```

---

### 2 Code Graph（AST 索引）

AI 可以：

```
find symbol
find reference
```

---

### 3 Smart Tool Planner

LLM 会生成：

```
Plan DAG
```

例如：

```
read_file
   ↓
analyze
   ↓
write_patch
```

---

如果你愿意，我可以继续给你写 **最关键的一部分**（几乎所有 AI Coding Agent 都依赖）：

**Workspace Context Engine + Code Graph（Go 实现）**

实现后你的 Agent 将具备：

* **代码库自动理解**
* **符号搜索**
* **引用分析**
* **跨文件修改**

这一步完成后，你的 Go Agent 就基本达到：

```
Claude Code / Cursor 内核级能力
```
