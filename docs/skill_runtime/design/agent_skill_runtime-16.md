下面继续把 **Claude-Code 风格的 Skill 系统**完整实现出来。
这一层是整个 AI Agent 中 **最关键的一层能力**：

它允许 AI **动态加载能力（skills）**，而不是写死在程序里。

类似：

* Claude Code
* Cursor
* OpenDevin

这些系统的核心能力就是 **Skill / Tool Plugin Runtime**。

---

# 一、Skill 系统设计

目标：

```text
.skills/
    run_tests.skill
    fix_bug.skill
    commit_git.skill
    refactor.skill
```

Skill 文件描述：

```json
{
  "name": "run_tests",
  "description": "Run project tests",
  "tool": "run_terminal",
  "args": {
    "cmd": "go test ./..."
  }
}
```

AI 可以：

```
User: 修复 bug
AI: 
 1. run_tests
 2. analyze failure
 3. edit code
```

---

# 二、Skill 数据结构

### agent/skill.go

```go
package agent

type Skill struct {

	Name string `json:"name"`

	Description string `json:"description"`

	Tool string `json:"tool"`

	Args map[string]interface{} `json:"args"`
}
```

---

# 三、Skill Loader

自动加载 `.skills` 目录。

### agent/skill_loader.go

```go
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func LoadSkills(dir string) ([]Skill, error) {

	var skills []Skill

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {

		if filepath.Ext(path) == ".skill" {

			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			var skill Skill

			err = json.Unmarshal(data, &skill)
			if err != nil {
				return err
			}

			skills = append(skills, skill)

		}

		return nil
	})

	return skills, err
}
```

---

# 四、Skill Registry

Skill 与 Tool Registry 连接。

### agent/skill_registry.go

```go
package agent

type SkillRegistry struct {
	skills map[string]Skill
}

func NewSkillRegistry() *SkillRegistry {

	return &SkillRegistry{
		skills: map[string]Skill{},
	}
}

func (r *SkillRegistry) Register(s Skill) {

	r.skills[s.Name] = s

}

func (r *SkillRegistry) Get(name string) (Skill, bool) {

	s, ok := r.skills[name]

	return s, ok
}
```

---

# 五、Skill Executor

Skill 实际调用 Tool。

### agent/skill_executor.go

```go
package agent

import (
	"context"
)

func (a *Agent) ExecuteSkill(ctx context.Context, skillName string) (interface{}, error) {

	skill, ok := a.SkillRegistry.Get(skillName)

	if !ok {
		return nil, nil
	}

	tool := a.Tools.Get(skill.Tool)

	if tool == nil {
		return nil, nil
	}

	return tool.Run(ctx, skill.Args)

}
```

---

# 六、Agent 初始化 Skill

### agent/agent.go

新增字段：

```go
SkillRegistry *SkillRegistry
Tools *ToolRegistry
```

初始化：

```go
func NewAgent(key string) *Agent {

	agent := &Agent{
		OpenAIKey: key,
		VectorIndex: embedding.NewVectorIndex(),
		Memory: NewMemory(),
		MCPClients: map[string]*mcp.Client{},
		SkillRegistry: NewSkillRegistry(),
		Tools: NewRegistry(),
	}

	return agent
}
```

---

# 七、加载 Skill

在 main.go 中：

```go
skills, _ := agent.LoadSkills(".skills")

for _, s := range skills {
    aiAgent.SkillRegistry.Register(s)
}
```

---

# 八、Planner 支持 Skill

Planner prompt：

```text
Available Skills:

run_tests
fix_bug
commit_git
refactor
```

示例 LLM 输出：

```json
{
 "steps":[
   {
     "id":"step1",
     "tool":"skill",
     "args":{"name":"run_tests"},
     "depends":[]
   }
 ]
}
```

---

# 九、Executor 扩展

### dag_executor.go

```go
if step.Tool == "skill" {

	name := step.Args["name"].(string)

	out, err := a.ExecuteSkill(ctx, name)

	results[step.ID] = out

	continue
}
```

---

# 十、示例 Skill

### .skills/run_tests.skill

```json
{
  "name": "run_tests",
  "description": "Run go tests",
  "tool": "run_terminal",
  "args": {
    "cmd": "go test ./..."
  }
}
```

---

### .skills/git_commit.skill

```json
{
  "name": "git_commit",
  "description": "Commit code",
  "tool": "run_terminal",
  "args": {
    "cmd": "git add . && git commit -m \"ai fix\""
  }
}
```

---

# 十一、Skill + Tool 执行流程

完整执行链：

```
User Prompt
    ↓
LLM Planner
    ↓
Skill Plan
    ↓
DAG Executor
    ↓
Skill Runtime
    ↓
Tool Runtime
    ↓
System Execution
```

---

# 十二、Skill 系统能力

实现后 AI 就拥有：

| 能力        | 支持 |
| --------- | -- |
| 动态能力加载    | ✅  |
| AI 自主选择能力 | ✅  |
| 技能组合      | ✅  |
| 技能 DAG    | ✅  |
| 技能热更新     | ✅  |

---

# 十三、最终 AI Agent 架构

```
                        +----------------+
                        |     USER       |
                        +--------+-------+
                                 |
                                 v
                         +-------+-------+
                         |    AGENT LOOP |
                         +-------+-------+
                                 |
                      +----------+-----------+
                      |                      |
                      v                      v
             +---------------+      +---------------+
             |   LLM Planner |      | Observation DB|
             +-------+-------+      +---------------+
                     |
                     v
                +----+----+
                |   PLAN   |
                +----+----+
                     |
                     v
              +------+------+
              | DAG Executor |
              +------+------+
                     |
             +-------+--------+
             |                |
             v                v
      +-------------+   +-------------+
      | Skill Engine |   | Tool Engine|
      +------+------+
             |
             v
        +----+----+
        |  SYSTEM |
        +---------+
```

---

# 十四、下一步（真正的 Claude Code 级能力）

如果继续升级，可以增加 **三个非常关键的系统**：

### 1️⃣ Code Graph（代码知识图谱）

```
Function
  ↓
Call Graph
  ↓
Dependency Graph
```

AI 可以理解整个 repo。

---

### 2️⃣ Long Context Memory

类似：

```
Agent Memory
   ├─ Observations
   ├─ Plans
   ├─ Fix history
```

AI 可以持续学习。

---

### 3️⃣ Multi-Agent 协作

```
Planner Agent
Coder Agent
Tester Agent
Reviewer Agent
```

---

如果你愿意，我可以 **继续往下写一个更完整的部分**：

**“Claude Code 级 Codebase Semantic Engine”**

这个系统能让 AI：

```
理解整个代码库
自动定位 bug
自动重构
跨文件修改
```

也是 **Cursor / Devin 的核心秘密之一**。
