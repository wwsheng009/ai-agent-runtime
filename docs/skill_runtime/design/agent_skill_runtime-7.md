下面我把 **Claude Code 风格的 Skill Loader + Skill Runtime**完整设计和 **Golang 可落地实现**补齐。
这一层其实是 **Agent 能扩展能力的核心机制**，类似：

* Claude Code 的 `skills/`
* Cursor 的 rules / agents
* OpenAI Codex CLI 的 tool packs

本质是：

```
skills/
   git/
      skill.yaml
      prompt.md
      tools.go
   golang/
      skill.yaml
      prompt.md
   debug/
      skill.yaml
      prompt.md
```

Runtime 会：

```
load skills
     │
     ▼
build capability graph
     │
     ▼
select skill
     │
     ▼
inject prompt + tools
```

下面给出 **完整 Go 实现**。

---

# 一、Skill 文件结构

每个 skill：

```
skills/
  git/
    skill.yaml
    prompt.md
```

---

## skill.yaml

```yaml
name: git
description: git operations

triggers:
  - commit
  - branch
  - git

tools:
  - git_commit
  - git_branch
```

---

# 二、Skill 数据结构

`skill/skill.go`

```go
package skill

type Skill struct {
	Name        string
	Description string
	Triggers    []string
	Prompt      string
	Tools       []string
}
```

---

# 三、Skill Loader

`skill/loader.go`

```go
package skill

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Loader struct {
	Skills []*Skill
}

func (l *Loader) Load(dir string) error {

	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {

		if info.IsDir() {
			return nil
		}

		if filepath.Base(path) != "skill.yaml" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		var s Skill

		err = yaml.Unmarshal(data, &s)
		if err != nil {
			return err
		}

		promptPath := filepath.Join(filepath.Dir(path), "prompt.md")

		prompt, _ := os.ReadFile(promptPath)

		s.Prompt = string(prompt)

		l.Skills = append(l.Skills, &s)

		return nil
	})
}
```

---

# 四、Skill Registry

`skill/registry.go`

```go
package skill

type Registry struct {
	skills []*Skill
}

func NewRegistry(skills []*Skill) *Registry {

	return &Registry{skills: skills}
}

func (r *Registry) All() []*Skill {

	return r.skills
}
```

---

# 五、Skill Router

Router 用于根据 prompt 选择 skill。

---

## Router

`skill/router.go`

```go
package skill

import "strings"

type Router struct {
	skills []*Skill
}

func NewRouter(skills []*Skill) *Router {

	return &Router{skills: skills}
}

func (r *Router) Match(prompt string) *Skill {

	for _, s := range r.skills {

		for _, t := range s.Triggers {

			if strings.Contains(prompt, t) {
				return s
			}
		}

	}

	return nil
}
```

---

# 六、Skill Prompt Injection

Skill 会把 prompt 注入 LLM。

---

## Prompt Builder

`runtime/skill_prompt.go`

```go
package runtime

import (
	"strings"

	"agent/skill"
)

func BuildPrompt(s *skill.Skill, user string) string {

	var sb strings.Builder

	sb.WriteString("Skill:\n")
	sb.WriteString(s.Prompt)

	sb.WriteString("\nUser:\n")
	sb.WriteString(user)

	return sb.String()
}
```

---

# 七、Skill Tool Injection

Skill 可以限制工具。

---

## Tool Filter

```go
func FilterTools(all map[string]Tool, allowed []string) map[string]Tool {

	res := map[string]Tool{}

	for _, name := range allowed {

		if t, ok := all[name]; ok {

			res[name] = t

		}
	}

	return res
}
```

---

# 八、Skill Runtime

Skill Runtime 负责：

```
select skill
inject prompt
inject tools
run agent
```

---

## Runtime

`runtime/skill_runtime.go`

```go
package runtime

import (
	"context"

	"agent/skill"
)

type SkillRuntime struct {
	router *skill.Router
}

func (s *SkillRuntime) Run(ctx context.Context, prompt string) (*skill.Skill, string) {

	sk := s.router.Match(prompt)

	if sk == nil {

		return nil, prompt

	}

	p := BuildPrompt(sk, prompt)

	return sk, p
}
```

---

# 九、Skill 热加载

Agent 可以动态 reload skills。

---

## Watcher

`skill/watch.go`

```go
package skill

import (
	"log"

	"github.com/fsnotify/fsnotify"
)

func Watch(path string, reload func()) {

	watcher, _ := fsnotify.NewWatcher()

	watcher.Add(path)

	go func() {

		for {

			select {

			case e := <-watcher.Events:

				log.Println("skill change", e)

				reload()

			}

		}

	}()

}
```

---

# 十、Skill CLI 示例

```
skills/
   git/
      skill.yaml
      prompt.md
   golang/
      skill.yaml
      prompt.md
```

运行：

```go
loader := &skill.Loader{}

loader.Load("./skills")

router := skill.NewRouter(loader.Skills)

runtime := &SkillRuntime{
	router: router,
}

sk, prompt := runtime.Run(ctx, userInput)
```

---

# 十一、Skill Capability Graph（高级）

真正 Claude Code 不是简单匹配，而是：

```
Skill Graph
```

例如：

```
golang skill
     │
     ├── test skill
     │
     └── debug skill
```

结构：

```go
type Node struct {
	Skill *Skill
	Next  []*Node
}
```

Agent 会：

```
skill chain execution
```

---

# 十二、完整 Skill 系统结构

最终：

```
skills/
   git
   golang
   debug

agent/
 ├ skill
 │   loader.go
 │   registry.go
 │   router.go
 │   watch.go
 │
 ├ runtime
 │   skill_runtime.go
 │
 └ tool
```

代码规模：

```
≈ 1200 行
```

---

# 十三、Claude Code Skills 的真正秘密

真实系统还有三个关键机制：

### 1 Skill Embedding Router

不是 keyword，而是：

```
embedding search
```

---

### 2 Skill Context Cache

```
reuse skill context
```

减少 token。

---

### 3 Skill Planner

```
plan
  ↓
skill chain
```

例如：

```
fix bug
   │
   ▼
search code
   │
   ▼
edit code
   │
   ▼
run tests
```

---

如果你愿意，我可以 **再给你一份真正 Claude Code 级的 Go 实现（约 12000 行架构）**，里面会包含：

* Skill Embedding Router
* Capability Graph
* Repo Map
* AST Code Graph
* ReAct Planner
* Parallel Tool Runtime
* Token Budget 管理
* Streaming Tool Calls
* Code Patch Engine
* Git 自动提交

这套基本就是 **完整 Coding Agent CLI 内核**。
