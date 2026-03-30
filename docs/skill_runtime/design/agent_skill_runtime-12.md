下面继续把这套 **Go AI Agent + Skills + MCP** 升级到真正接近**Claude Code / Cursor** 的 **核心能力模块**：

这一步实现 **Workspace Context Engine + Code Graph**。

这是 **AI Coding Agent 最关键的能力**，作用是让 AI 自动理解整个代码仓库，而不是只看单个文件。

整体结构：

```
Workspace
   │
   ├── Repo Scanner
   ├── File Index
   ├── Symbol Index
   ├── Reference Graph
   └── Embedding Search
```

---

# 一、Workspace Engine 结构

```go
package workspace

type Workspace struct {

	Root string

	Files map[string]*File

	Symbols map[string]*Symbol

	References map[string][]string
}

type File struct {

	Path string

	Language string

	Content string

	Symbols []string
}

type Symbol struct {

	Name string

	File string

	Line int

	Kind string
}
```

---

# 二、扫描代码仓库

扫描整个 repo。

### scanner.go

```go
package workspace

import (
	"os"
	"path/filepath"
)

func Scan(root string) (*Workspace, error) {

	ws := &Workspace{
		Root: root,
		Files: map[string]*File{},
		Symbols: map[string]*Symbol{},
		References: map[string][]string{},
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {

		if info.IsDir() {
			return nil
		}

		if !isCodeFile(path) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		ws.Files[path] = &File{
			Path: path,
			Content: string(data),
		}

		return nil
	})

	return ws, err
}
```

---

### 文件类型判断

```go
func isCodeFile(path string) bool {

	switch filepath.Ext(path) {

	case ".go",".js",".ts",".py",".java":
		return true
	}

	return false
}
```

---

# 三、构建 Symbol Index（Go AST）

解析 Go 文件。

### symbol_parser.go

```go
package workspace

import (
	"go/ast"
	"go/parser"
	"go/token"
)

func ParseGoSymbols(ws *Workspace) error {

	for _, file := range ws.Files {

		if filepath.Ext(file.Path) != ".go" {
			continue
		}

		fset := token.NewFileSet()

		node, err := parser.ParseFile(
			fset,
			file.Path,
			file.Content,
			parser.ParseComments,
		)

		if err != nil {
			continue
		}

		ast.Inspect(node, func(n ast.Node) bool {

			switch v := n.(type) {

			case *ast.FuncDecl:

				name := v.Name.Name

				ws.Symbols[name] = &Symbol{
					Name: name,
					File: file.Path,
					Kind: "function",
				}

			case *ast.TypeSpec:

				name := v.Name.Name

				ws.Symbols[name] = &Symbol{
					Name: name,
					File: file.Path,
					Kind: "type",
				}

			}

			return true
		})
	}

	return nil
}
```

---

# 四、Reference Graph

找到 symbol 被引用的位置。

### reference.go

```go
package workspace

import (
	"go/ast"
	"go/parser"
	"go/token"
)

func BuildReferences(ws *Workspace) {

	for _, file := range ws.Files {

		if filepath.Ext(file.Path) != ".go" {
			continue
		}

		fset := token.NewFileSet()

		node, err := parser.ParseFile(
			fset,
			file.Path,
			file.Content,
			0,
		)

		if err != nil {
			continue
		}

		ast.Inspect(node, func(n ast.Node) bool {

			ident, ok := n.(*ast.Ident)

			if !ok {
				return true
			}

			name := ident.Name

			if _, ok := ws.Symbols[name]; ok {

				ws.References[name] = append(
					ws.References[name],
					file.Path,
				)
			}

			return true
		})
	}
}
```

---

# 五、Repo Tree

让 AI 知道项目结构。

```go
type RepoTree struct {

	Path string

	Children []*RepoTree
}
```

---

### build_tree.go

```go
package workspace

import (
	"os"
	"path/filepath"
)

func BuildTree(root string) (*RepoTree, error) {

	node := &RepoTree{
		Path: root,
	}

	files, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	for _, f := range files {

		p := filepath.Join(root, f.Name())

		if f.IsDir() {

			child, _ := BuildTree(p)

			node.Children = append(node.Children, child)

		} else {

			node.Children = append(node.Children, &RepoTree{
				Path: p,
			})
		}
	}

	return node, nil
}
```

---

# 六、Workspace Context Builder

AI 每次调用前生成 context。

### context_builder.go

```go
package workspace

import (
	"strings"
)

func BuildContext(ws *Workspace) string {

	var sb strings.Builder

	sb.WriteString("Workspace Files:\n")

	for path := range ws.Files {

		sb.WriteString(path)
		sb.WriteString("\n")
	}

	sb.WriteString("\nSymbols:\n")

	for name, sym := range ws.Symbols {

		sb.WriteString(name)
		sb.WriteString(" -> ")
		sb.WriteString(sym.File)
		sb.WriteString("\n")
	}

	return sb.String()
}
```

---

# 七、Symbol Search Tool

AI 可以查 symbol。

```go
func (ws *Workspace) FindSymbol(name string) *Symbol {

	if s, ok := ws.Symbols[name]; ok {
		return s
	}

	return nil
}
```

---

# 八、Reference Search Tool

```go
func (ws *Workspace) FindReferences(name string) []string {

	return ws.References[name]
}
```

---

# 九、Workspace MCP Skill

现在把 workspace 暴露成 skill。

### tools.go

```go
func FindSymbolTool(ws *Workspace) func(map[string]interface{}) (interface{}, error) {

	return func(args map[string]interface{}) (interface{}, error) {

		name := args["name"].(string)

		return ws.FindSymbol(name), nil
	}
}
```

---

# 十、AI Coding Agent Workflow

现在 AI 可以：

```
User: fix bug in http client
```

Agent：

```
1 scan workspace
2 find symbol httpClient
3 read file
4 analyze code
5 patch
```

执行流程：

```
Agent
 │
 ▼
Workspace Index
 │
 ▼
Symbol Search
 │
 ▼
Read File
 │
 ▼
Patch
```

---

# 十一、最终 Agent 架构

```
agent
│
├── runtime
│   ├ agent_loop.go
│   ├ planner.go
│   ├ dag_executor.go
│
├── mcp
│
├── workspace
│   ├ scanner.go
│   ├ symbol_parser.go
│   ├ reference.go
│   ├ tree.go
│   └ context_builder.go
│
├── skills
│   ├ filesystem
│   ├ git
│   ├ terminal
│
└ main.go
```

---

# 十二、Claude Code / Cursor 真正的核心能力

完整 Coding Agent 实际有 **五层能力**：

```
1 LLM
2 Tool Runtime
3 Skills / MCP
4 Workspace Context
5 Code Graph
```

再往上就是：

```
Autonomous Coding Loop
```

---

# 十三、如果继续升级（最关键一步）

目前系统还缺 **最强的一层**：

**Code Embedding + Semantic Search**

这是：

* Cursor
* Sourcegraph Cody
* OpenDevin

最核心能力。

实现后 AI 可以：

```
semantic search code
```

而不是简单 symbol search。

例如：

```
"where http request retry logic implemented"
```

AI 也能找到代码。

---

如果你愿意，我可以继续给你补 **最后一块最核心系统**：

**Code Embedding Engine（Go 完整实现）**

包含：

* 代码 chunk
* embedding
* vector index
* semantic search
* context packing

做完后你的系统基本就是：

**一个完整 Go 版 Claude Code / Cursor 内核。**
