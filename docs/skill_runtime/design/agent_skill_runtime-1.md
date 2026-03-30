在 **Golang AI Agent / Coding Agent** 开发中，如果要实现类似 **Claude Code 的 `skills` 机制**，本质上是在做一套 **可动态加载的能力系统（Capability System）**，它通常包含：

* Skill **描述（metadata）**
* Skill **触发规则（intent / routing）**
* Skill **执行器（tool / workflow）**
* Skill **上下文注入（prompt / memory / env）**

也可以理解为：

```
Agent Runtime
   │
   ├── Skills Registry
   │       ├── GitSkill
   │       ├── TestSkill
   │       ├── RefactorSkill
   │       └── SearchSkill
   │
   ├── Intent Router
   │
   ├── Tool Runtime
   │
   └── Prompt Context Builder
```

我从 **系统架构角度**讲完整方案（不是简单插件系统）。

---

# 一、Claude Code 的 Skills 本质

Claude Code 的 skills 本质是：

```
Prompt Capability + Tool Capability + Workflow
```

每个 skill 实际包含：

```
Skill
 ├── manifest.yaml
 ├── prompt.md
 ├── tools/
 └── workflow.go
```

例如：

```
skills/
   git/
      skill.yaml
      prompt.md
      tools.go
   test/
      skill.yaml
      prompt.md
```

---

# 二、Skill Manifest 设计

核心是 **Skill 描述文件**

建议：

```yaml
name: git
description: git repository operations

triggers:
  - git
  - commit
  - branch
  - rebase

tools:
  - git_status
  - git_commit
  - git_diff

context:
  files:
    - .git/config
  env:
    - GIT_AUTHOR_NAME
```

Golang struct

```go
type SkillManifest struct {
    Name        string   `yaml:"name"`
    Description string   `yaml:"description"`

    Triggers []string `yaml:"triggers"`

    Tools []string `yaml:"tools"`

    Context struct {
        Files []string `yaml:"files"`
        Env   []string `yaml:"env"`
    } `yaml:"context"`
}
```

---

# 三、Skill Runtime 架构

核心运行时：

```
                ┌──────────────┐
User Prompt ───►│ Intent Router│
                └──────┬───────┘
                       │
                ┌──────▼──────┐
                │ Skill Engine│
                └──────┬──────┘
                       │
         ┌─────────────┼─────────────┐
         │             │             │
     Git Skill     Test Skill     Code Skill
         │             │             │
      Tool Call     Tool Call     Tool Call
```

组件：

```
AgentRuntime
 ├── SkillRegistry
 ├── IntentRouter
 ├── ToolExecutor
 └── ContextBuilder
```

---

# 四、Skill Registry（加载 Skills）

```go
type Skill struct {
    Manifest SkillManifest
    Handler  SkillHandler
}

type SkillHandler interface {
    Execute(ctx context.Context, req SkillRequest) (SkillResult, error)
}
```

Registry

```go
type SkillRegistry struct {
    skills map[string]*Skill
}

func NewSkillRegistry() *SkillRegistry {
    return &SkillRegistry{
        skills: make(map[string]*Skill),
    }
}

func (r *SkillRegistry) Register(skill *Skill) {
    r.skills[skill.Manifest.Name] = skill
}
```

---

# 五、自动加载 Skills（类似 Claude）

扫描目录：

```
~/.agent/skills
```

代码：

```go
func LoadSkills(dir string, registry *SkillRegistry) error {

    entries, err := os.ReadDir(dir)
    if err != nil {
        return err
    }

    for _, e := range entries {

        skillDir := filepath.Join(dir, e.Name())
        manifestPath := filepath.Join(skillDir, "skill.yaml")

        data, err := os.ReadFile(manifestPath)
        if err != nil {
            continue
        }

        var m SkillManifest
        yaml.Unmarshal(data, &m)

        skill := &Skill{
            Manifest: m,
        }

        registry.Register(skill)
    }

    return nil
}
```

---

# 六、Intent Router（选择 Skill）

用户输入：

```
"commit current changes"
```

Router：

```
commit → git skill
```

简单实现：

```go
type IntentRouter struct {
    registry *SkillRegistry
}

func (r *IntentRouter) Match(prompt string) *Skill {

    for _, skill := range r.registry.skills {

        for _, t := range skill.Manifest.Triggers {

            if strings.Contains(prompt, t) {
                return skill
            }
        }
    }

    return nil
}
```

更高级方案：

### 1 embedding routing

```
prompt embedding
      ↓
skill embedding
      ↓
vector search
```

### 2 LLM routing

```
You are skill router.

Skills:
- git
- test
- refactor

Choose best skill.
```

---

# 七、Skill Tool System

Skill 可以提供 tools

例如：

```
git_status
git_diff
git_commit
```

Tool interface：

```go
type Tool interface {
    Name() string
    Description() string

    Execute(ctx context.Context, input map[string]any) (any, error)
}
```

Tool Registry

```go
type ToolRegistry struct {
    tools map[string]Tool
}
```

Skill 注册 tool

```go
registry.RegisterTool(&GitStatusTool{})
```

---

# 八、Skill Prompt 注入

Claude Code skills 还有一个关键：

**prompt augmentation**

例如：

```
system prompt:

You are git expert.

Commands available:
- git_status
- git_commit
```

代码：

```go
func BuildSkillPrompt(skill *Skill, userPrompt string) string {

    return fmt.Sprintf(`
Skill: %s

Description: %s

User Request:
%s
`, skill.Manifest.Name, skill.Manifest.Description, userPrompt)
}
```

---

# 九、完整运行流程

完整 pipeline：

```
User Prompt
     │
     ▼
Intent Router
     │
     ▼
Skill Selected
     │
     ▼
Context Builder
     │
     ▼
Prompt Builder
     │
     ▼
LLM Call
     │
     ▼
Tool Call
     │
     ▼
Tool Executor
     │
     ▼
Return Result
```

---

# 十、进阶架构（Claude级别）

真正 Claude Code skill 系统通常包含：

### 1 Skill 生命周期

```
install
enable
disable
update
```

---

### 2 Skill 权限

例如：

```
permissions:
  - filesystem
  - git
  - shell
```

---

### 3 Skill Memory

```
skill memory
```

例如：

```
test skill
remember last test command
```

---

### 4 Skill Workflow

一个 skill 可以是 workflow

```
refactor skill

1 read file
2 analyze
3 rewrite
4 apply patch
```

---

# 十一、推荐 Golang 最终架构

我最推荐的结构：

```
agent/

  runtime/
      agent.go
      router.go
      context.go

  skill/
      registry.go
      loader.go
      skill.go

  tool/
      registry.go
      tool.go

  skills/
      git/
         manifest.yaml
         skill.go
      test/
         manifest.yaml

  llm/
      openai.go
      anthropic.go
```

---

# 十二、一个 Claude Code 级设计（核心思想）

核心思想是：

```
Skill = Capability Module
```

包含：

```
Capability
 ├── Intent
 ├── Prompt
 ├── Tools
 ├── Workflow
 └── Context
```

Agent Runtime：

```
Agent = Router + Capability Engine
```

---

# 十三、业界主流 Agent 框架做法

类似设计：

| 框架               | 技术           |
| ---------------- | ------------ |
| LangChain        | Tool + Agent |
| AutoGPT          | Plugin       |
| OpenAI Codex CLI | Tool + Plan  |
| Claude Code      | Skills       |

---

# 十四、真正工程级 Skill 系统（我建议）

如果你做 **Go AI coding agent**，建议直接设计成：

```
Capability Graph
```

而不是简单 plugin：

```
Capability DAG
```

例如：

```
refactor
   │
   ├─ read_file
   ├─ analyze_code
   └─ apply_patch
```

这样 skill 可以组合。

---

# 十五、如果你愿意，我可以给你一套 **完整 Claude Code Skill Runtime（Golang版）**

包括：

* Skill Loader
* Intent Router（embedding）
* Tool Call Runtime
* SSE streaming
* Workflow Skill
* Capability Graph
* Prompt Injection
* Sandbox

完整代码规模大约：

```
3000 行 Go
```

可以直接做 **Claude Code / Cursor 级 Agent**。
