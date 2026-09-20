有，而且这是目前 **AI Coding Agent / LLM Agent Harness** 非常典型的成本问题。

你描述的现象本质上是：

> **LLM 把大量上下文窗口和 token 消耗在“重新认识项目”上，而不是消耗在真正的推理和修改代码上。**

尤其是多轮会话，如果每一轮都重新：

```text
ls
→ find
→ grep
→ cat
→ 搜索 symbol
→ 查看调用关系
→ 再 grep
→ 再读文件
→ ...
```

那么 Agent 的成本会非常高，而且随着项目变大，效果会越来越差。

我建议你的 Agent Harness 不要把“代码探索”完全交给 LLM，而是增加一个独立的 **Code Intelligence / Project Knowledge Layer**。

---

# 一、核心思路：不要让 LLM 每次重新探索项目

可以把整体架构从：

```text
User
  ↓
LLM
  ↓
Tool
  ├── list files
  ├── grep
  ├── read file
  ├── search
  └── ...
  ↓
LLM
```

升级成：

```text
                         ┌─────────────────────┐
                         │       User          │
                         └──────────┬──────────┘
                                    ↓
                         ┌─────────────────────┐
                         │    Agent Harness   │
                         └──────────┬──────────┘
                                    ↓
                    ┌─────────────────────────────┐
                    │     Context / Memory        │
                    │                             │
                    │ Conversation Memory         │
                    │ Project Memory              │
                    │ Exploration Cache           │
                    │ Task Memory                 │
                    └──────────────┬──────────────┘
                                   ↓
                    ┌─────────────────────────────┐
                    │   Code Intelligence Layer   │
                    │                             │
                    │ AST                         │
                    │ Symbol Index                 │
                    │ Reference Index              │
                    │ Call Graph                   │
                    │ Import Graph                 │
                    │ File Structure               │
                    │ Semantic Index               │
                    │ Dependency Graph             │
                    └──────────────┬──────────────┘
                                   ↓
                    ┌─────────────────────────────┐
                    │       LLM Agent              │
                    └──────────────┬──────────────┘
                                   ↓
                    ┌─────────────────────────────┐
                    │       Tool Layer             │
                    └─────────────────────────────┘
```

这里最关键的是：

> **LLM 不应该负责“发现项目结构”，LLM 应该负责“理解和推理”。**

---

# 二、最重要的优化：建立 Project Knowledge

例如第一次进入一个项目：

```text
project/
├── cmd/
├── internal/
│   ├── agent/
│   ├── runtime/
│   ├── tools/
│   └── memory/
├── pkg/
├── tests/
└── go.mod
```

传统 Agent 会：

```text
LLM:
  ls

LLM:
  ls internal

LLM:
  grep Agent

LLM:
  cat agent.go

LLM:
  grep Runtime

LLM:
  cat runtime.go
```

然后这些内容全部进入 context。

这是非常浪费的。

---

# 三、可以建立一个 Project Index

第一次扫描项目的时候建立：

```text
ProjectIndex
│
├── FileIndex
│
├── SymbolIndex
│
├── ReferenceIndex
│
├── CallGraph
│
├── ImportGraph
│
├── TypeIndex
│
├── APIIndex
│
├── TestIndex
│
└── SemanticIndex
```

例如：

```json
{
  "symbol": "Agent.run",
  "file": "internal/agent/agent.go",
  "line": 128,
  "type": "method",
  "receiver": "Agent",
  "calls": [
    "Planner.plan",
    "ToolExecutor.execute",
    "Memory.store"
  ],
  "called_by": [
    "AgentLoop.loop"
  ]
}
```

以后用户问：

> Agent 执行 Tool 的流程在哪里？

不需要：

```text
grep Agent
grep Tool
cat agent.go
grep execute
cat executor.go
...
```

直接：

```text
Symbol Search:
Agent.run
      ↓
calls
      ↓
ToolExecutor.execute
      ↓
calls
      ↓
Tool.execute
```

LLM 只需要读取真正相关的几个代码片段。

---

# 四、把代码探索变成“查询”，而不是“工具调用”

这是我认为你的 Harness 最值得改的地方。

传统：

```text
search_file()
read_file()
grep()
list_dir()
```

实际上这些都是**底层 primitive tools**。

可以在上面再增加一层：

```text
Code Intelligence API
```

例如：

```text
find_symbol("Agent.run")

find_references("Agent.run")

find_callers("ToolExecutor.execute")

find_callees("Agent.run")

find_implementations("Tool")

find_type("Agent")

find_tests("Agent.run")

find_related_code("Agent.run")
```

这样 LLM 不需要自己进行十几步探索。

例如用户：

> 帮我修改 Agent 的 tool timeout。

Agent 可以直接：

```text
find_symbol("Agent")
find_related_code("timeout")
find_callers(...)
find_tests(...)
```

而不是：

```text
grep timeout
grep agent
grep tool
cat xxx
cat xxx
...
```

---

# 五、进一步：建立 Project Map

这是非常值得做的。

第一次扫描项目生成一个：

```text
PROJECT.md
```

或者内部结构：

```json
{
  "project": {
    "language": ["Go", "TypeScript"],
    "framework": ["..."],
    "entrypoints": [],
    "modules": []
  }
}
```

例如：

```markdown
# Project Map

## Architecture

cmd/
  application entrypoints

internal/agent/
  Agent orchestration

internal/tools/
  Tool execution

internal/memory/
  Memory subsystem

internal/runtime/
  Runtime implementation

## Important Symbols

Agent
Agent.run
AgentLoop
ToolExecutor
MemoryManager

## Dependencies

Agent
 ├── Planner
 ├── ToolExecutor
 └── Memory

ToolExecutor
 ├── ToolRegistry
 └── ToolRuntime
```

这个东西不要每次重新生成。

---

# 六、但是 PROJECT.md 还不够

如果项目有：

```text
5000 files
10000 symbols
```

把整个 Project Map 放进 LLM context 也很浪费。

所以建议：

```text
Project Knowledge
       │
       ├── Project Summary
       │
       ├── Module Summary
       │
       ├── Symbol Index
       │
       ├── Dependency Graph
       │
       └── Semantic Index
```

采用**分层加载**。

---

# 七、Hierarchical Context 是关键

不要：

```text
把整个项目塞给 LLM
```

而是：

```text
Level 0
Project
   ↓
Level 1
Module
   ↓
Level 2
File
   ↓
Level 3
Symbol
   ↓
Level 4
Code
```

例如用户：

> 修改 Agent Tool Timeout。

第一阶段只需要：

```text
Project
 ↓
agent module
 ↓
Agent
 ↓
ToolExecutor
 ↓
timeout symbol
```

最后才读取：

```go
func (e *ToolExecutor) Execute(...)
```

也就是说：

> **先缩小搜索空间，再读取代码。**

这个对 token 消耗影响非常大。

---

# 八、多轮会话最重要：Exploration Memory

你现在的问题里，我认为这个甚至比代码索引更重要。

例如：

第一轮：

```text
用户：
分析 Agent timeout 问题
```

Agent 探索：

```text
Agent.run
ToolExecutor.execute
ToolRegistry
TimeoutConfig
```

第二轮：

```text
用户：
那把 timeout 改成 30 秒
```

传统 Agent：

```text
重新搜索 Agent
重新搜索 timeout
重新找 ToolExecutor
重新读代码
```

这完全没必要。

应该保存：

```text
TaskContext

Explored:
  Agent.run
  ToolExecutor.execute
  TimeoutConfig

Relevant Files:
  agent.go
  executor.go
  config.go

Relevant Symbols:
  Agent.run
  ToolExecutor.execute
  TimeoutConfig.Timeout

Known Architecture:
  Agent
    ↓
  ToolExecutor
    ↓
  ToolRuntime

Unresolved:
  tests for timeout
```

第二轮直接复用。

---

# 九、甚至可以建立 Exploration Graph

这个非常适合你自己的 Agent Harness。

例如：

```text
Conversation
      │
      ↓
Task
      │
      ↓
Exploration Graph
      │
      ├── Agent.run
      │     ├── Planner
      │     └── ToolExecutor
      │
      ├── ToolExecutor.execute
      │     ├── timeout
      │     └── retry
      │
      └── TimeoutConfig
```

下一轮：

```text
User:
把 timeout 改成 60 秒
```

Harness 判断：

```text
是否已经探索过？

YES

→ 不调用搜索工具
→ 使用 Exploration Graph
→ 直接读取相关 symbol
```

---

# 十、Tool Output 也要压缩

另外一个巨大的 token 黑洞是：

```text
grep
```

比如：

```text
grep -R "timeout" .
```

可能返回：

```text
500 lines
```

然后全部塞给 LLM。

这是非常危险的。

应该让 Tool 本身返回：

```json
{
  "query": "timeout",
  "total_matches": 183,
  "relevant_matches": [
    {
      "file": "internal/agent/executor.go",
      "line": 127,
      "symbol": "ToolExecutor.execute"
    },
    {
      "file": "internal/config/config.go",
      "line": 42,
      "symbol": "TimeoutConfig"
    }
  ],
  "truncated": true
}
```

而不是：

```text
file1 line 1
file1 line 2
file1 line 3
...
file183 line...
```

---

# 十一、代码读取也应该从“行”升级到“Symbol”

传统：

```text
read_file(
  path,
  start_line,
  end_line
)
```

建议增加：

```text
read_symbol(
  "ToolExecutor.execute"
)
```

返回：

```text
ToolExecutor.execute

signature
implementation
dependencies
callers
callees
related tests
```

而不是：

```text
executor.go 1~500
```

---

# 十二、Context Compression

还有一个非常重要的机制：

```text
Raw Tool Output
       ↓
Observation
       ↓
Compression
       ↓
Memory
```

例如原始：

```text
2000 tokens
```

压缩成：

```text
Agent.run() 位于 internal/agent/agent.go:128

执行流程：

Agent.run
 → Planner.plan
 → ToolExecutor.execute
 → Memory.store

ToolExecutor.execute 位于 internal/tools/executor.go:87

timeout 来自 ToolConfig.Timeout。
```

可能只需要：

```text
100 tokens
```

然后把 raw output 丢掉。

---

# 十三、建议把 Context 分成 4 层

你的 Harness 可以设计成：

```text
┌────────────────────────────────────┐
│ L0 System Context                  │
│ Agent rules / tool definitions     │
└────────────────────────────────────┘

┌────────────────────────────────────┐
│ L1 Project Context                 │
│ Project architecture              │
│ Project map                       │
└────────────────────────────────────┘

┌────────────────────────────────────┐
│ L2 Task Context                    │
│ Current task                      │
│ Relevant symbols                  │
│ Exploration graph                 │
└────────────────────────────────────┘

┌────────────────────────────────────┐
│ L3 Working Context                 │
│ Current code                      │
│ Tool results                      │
│ Current reasoning                 │
└────────────────────────────────────┘
```

最重要的是：

> **只有 L3 高频进入 LLM，L1/L2 应该尽量结构化，而不是全部展开成自然语言。**

---

# 十四、甚至可以做“Context Virtual Memory”

这个概念我很推荐你采用。

类似操作系统：

```text
Disk
 ↓
Index
 ↓
Cache
 ↓
RAM
```

映射到 Agent：

```text
Repository
 ↓
Project Knowledge
 ↓
Exploration Cache
 ↓
LLM Context
```

即：

```text
Repository
    ↓
Code Index
    ↓
Knowledge Store
    ↓
Context Retrieval
    ↓
LLM
```

LLM Context 就像 RAM。

**不是所有项目代码都应该放进 RAM。**

---

# 十五、可以加入 Context Cache

例如：

```text
cache key:

repo_hash
+
branch
+
commit
+
query
```

例如：

```text
repo: abc123
query: "Agent tool execution architecture"
```

第一次：

```text
LLM exploration
↓
20000 tokens
```

第二次：

```text
cache hit
↓
300 tokens
```

---

# 十六、Git Commit 可以成为 Cache Invalidation

这个很好解决代码变化问题。

例如：

```text
repository
commit = 8f31ab
```

建立：

```text
ProjectIndex(8f31ab)
```

如果：

```text
commit = 91aa32
```

不要整个项目重新索引。

只更新：

```text
changed files
changed symbols
affected references
affected call graph
```

即：

```text
Incremental Indexing
```

---

# 十七、非常适合你这个 Agent 的数据库设计

如果你的 Harness 本身已经有数据库，可以直接做：

```text
projects
repositories
commits

files
symbols
references
imports

call_graph
type_graph
dependency_graph

project_summaries
module_summaries
symbol_summaries

exploration_sessions
exploration_nodes
exploration_edges

task_context
task_files
task_symbols

tool_calls
tool_results

context_cache
semantic_cache
```

例如：

```text
exploration_nodes

id
session_id
type
target
file
symbol
summary
created_at
last_used_at
```

---

# 十八、Agent Loop 也应该改变

传统：

```text
while (!done) {

    LLM

    if tool_call:
        execute_tool()

}
```

建议：

```text
User
 ↓
Task Analyzer
 ↓
Context Planner
 ↓
Knowledge Retrieval
 ↓
LLM
 ↓
Action
 ↓
Tool
 ↓
Observation
 ↓
Observation Compressor
 ↓
Knowledge Update
 ↓
LLM
```

重点增加两个组件：

```text
Context Planner
Observation Compressor
```

---

# 十九、Context Planner 可以决定“是否需要探索”

这是非常关键的。

例如：

```text
User:
把 timeout 改成 30 秒
```

Context Planner：

```text
已有知识：

TimeoutConfig
Agent.run
ToolExecutor.execute

confidence = 0.94

需要探索？
NO
```

直接执行。

如果：

```text
User:
为什么 Agent 在生产环境偶尔死锁？
```

那么：

```text
confidence = 0.32

需要探索？
YES
```

再去：

```text
find locks
find goroutines
find channels
find mutex
find callers
```

这样可以显著减少无意义搜索。

---

# 二十、可以引入 Confidence

例如每个知识节点：

```json
{
  "symbol": "ToolExecutor.execute",
  "confidence": 0.96,
  "source": "AST",
  "last_verified_commit": "8f31ab"
}
```

然后：

```text
confidence > 0.9
    ↓
直接使用

0.6 ~ 0.9
    ↓
局部验证

< 0.6
    ↓
重新探索
```

这个机制非常适合解决：

> Agent 到底什么时候应该重新搜索？

---

# 二十一、不要让 Vector DB 成为唯一方案

很多 Agent 第一反应：

> 把代码 embedding 到向量数据库。

我不建议只这么做。

代码搜索应该是：

```text
             Code Retrieval
                  │
       ┌──────────┼──────────┐
       ↓          ↓          ↓
   Symbol       Graph      Semantic
   Search       Search      Search
       │          │          │
       └──────────┼──────────┘
                  ↓
             Rank / Merge
                  ↓
              LLM Context
```

因为：

```text
"Agent.run"
```

这种查询：

**Symbol Index > Vector Search**

而：

```text
"处理 tool timeout 的代码"
```

这种查询：

**Semantic Search > Symbol Search**

而：

```text
"谁调用 Agent.run"
```

应该：

**Reference / Call Graph > Vector Search**

---

# 二十二、我建议你的 Harness 最终形成这个架构

```text
                         USER
                           │
                           ▼
                  ┌────────────────┐
                  │ Task Analyzer  │
                  └───────┬────────┘
                          │
                          ▼
                 ┌──────────────────┐
                 │ Context Planner  │
                 └────────┬─────────┘
                          │
             ┌────────────┼────────────┐
             ▼            ▼            ▼
        Task Memory   Project Memory  Cache
             │            │            │
             └────────────┼────────────┘
                          ▼
                ┌───────────────────┐
                │ Code Intelligence  │
                │                   │
                │ AST               │
                │ Symbol Index      │
                │ Reference Index   │
                │ Call Graph        │
                │ Type Graph        │
                │ Semantic Index    │
                └─────────┬─────────┘
                          │
                          ▼
                ┌───────────────────┐
                │ Context Builder   │
                └─────────┬─────────┘
                          │
                          ▼
                     ┌─────────┐
                     │   LLM   │
                     └────┬────┘
                          │
                    tool / answer
                          │
                          ▼
                ┌───────────────────┐
                │ Tool Executor     │
                └─────────┬─────────┘
                          │
                          ▼
                ┌───────────────────┐
                │ Observation       │
                │ Compressor        │
                └─────────┬─────────┘
                          │
                          ▼
                ┌───────────────────┐
                │ Knowledge Update  │
                └───────────────────┘
```

---

# 二十三、最值得优先实现的不是全部功能

如果你现在已经有一个 Agent Harness，我建议不要一下子做完整 Code Intelligence。

按这个顺序做：

### Phase 1：Exploration Cache

首先解决：

> **多轮会话重复探索**

保存：

```text
file
symbol
query
search result
summary
relevant code
```

收益最大，而且实现简单。

---

### Phase 2：Project Map

启动项目时生成：

```text
project structure
modules
important symbols
entrypoints
dependencies
```

---

### Phase 3：Symbol Index

支持：

```text
find_symbol
find_references
find_definition
find_implementation
```

这一阶段会明显减少：

```text
grep
cat
find
```

---

### Phase 4：Call Graph / Dependency Graph

支持：

```text
caller
callee
dependency
impact
```

---

### Phase 5：Context Compression

所有：

```text
Tool Output
```

不要直接进入 LLM。

统一：

```text
raw
 ↓
compress
 ↓
structured observation
 ↓
context
```

---

### Phase 6：Semantic Retrieval

最后再加：

```text
embedding
vector search
reranking
```

而不是一开始就 Vector DB。

---

# 二十四、最终的目标应该不是“减少 Tool Call”

这个区别非常重要。

真正应该优化的是：

```text
                    Token
                      │
         ┌────────────┼────────────┐
         │            │            │
       Explore      Reason       Generate
         │            │            │
      50~70%         20%          10%
```

逐渐变成：

```text
                    Token
                      │
         ┌────────────┼────────────┐
         │            │            │
       Explore      Reason       Generate
         │            │            │
       10~20%         50%         30%
```

也就是说：

> **不是让 Agent 少用工具，而是让工具变得“更聪明”。**

从：

```text
LLM → grep → LLM → cat → LLM → grep → LLM
```

变成：

```text
LLM
 ↓
Code Intelligence
 ↓
一次拿到结构化结果
 ↓
LLM
```

这才是 Harness 层真正应该解决的问题。

---

## 如果你准备继续做这个 Agent Harness

结合你之前在做的 **AI Agent Runtime / Codex Server / Mint** 这一类项目，我会建议进一步把它抽象成一个独立的：

```text
Agent Knowledge Runtime
```

核心不是普通的 Memory，而是：

```text
                    Agent Knowledge Runtime
                              │
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
 Project Knowledge       Task Knowledge       Conversation
        │                     │                     │
   Code Index            Exploration Graph      Memory
   Symbol Graph          Task Graph             Summary
   Call Graph            Changed Files          Decisions
   AST                    Relevant Symbols       Constraints
        │                     │                     │
        └─────────────────────┼─────────────────────┘
                              │
                     Context Compiler
                              │
                              ▼
                             LLM
```

这里还有一个很有价值的概念：

> **Context Compiler**

不要让 LLM 自己决定“我要读哪些代码、读多少代码、以前看过什么”。

而是 Harness 根据：

```text
User Task
+
Project Knowledge
+
Task History
+
Exploration Cache
+
Git Changes
```

自动编译出：

```text
LLM Context
```

这会比单纯增加几个 `search_code()` 工具有效得多。

如果你愿意把你**现在 Agent Harness 的架构、tool 定义以及一段真实的多轮调用日志**贴出来，我可以直接按你的现有设计，给你重新设计一套 **“低 Token Code Agent Harness / Context Compiler”架构**，包括数据结构、Agent Loop、缓存策略、Code Index、Exploration Graph，以及具体哪些 Tool 应该删掉/合并。



这恰恰是设计 **Agent Harness 的 Code Intelligence Layer** 时最关键的问题之一。

我的建议是：

> **不要让 Agent Harness 针对 Java、Go、Python、ABAP、TypeScript 分别实现一套代码理解系统。**
>
> 应该设计成 **Language Adapter + Universal Code Model（统一代码中间表示）**。

也就是把“语言差异”隔离在底层。

---

# 1. 总体架构

可以设计成：

```text
                         Agent Harness
                              │
                              ▼
                     ┌─────────────────┐
                     │ Context Compiler│
                     └────────┬────────┘
                              │
                              ▼
                  ┌───────────────────────┐
                  │ Universal Code Model  │
                  │       UCM             │
                  └───────────┬───────────┘
                              │
       ┌──────────────┬───────┼────────┬──────────────┐
       ▼              ▼       ▼        ▼              ▼
   Go Adapter      Java     Python   TypeScript     ABAP
       │              │       │        │              │
   Parser/AST      Parser   Parser   Parser        ABAP Parser
       │              │       │        │              │
       └──────────────┴───────┴────────┴──────────────┘
                              │
                              ▼
                        Source Code
```

LLM **尽量不要直接面对语言差异**。

---

# 2. 最重要的是定义一个 Universal Code Model

例如不管代码是什么语言，都转换成统一的：

```text
Project
  │
  ├── Module
  │
  ├── File
  │
  ├── Namespace
  │
  ├── Package
  │
  ├── Type
  │
  ├── Class
  │
  ├── Interface
  │
  ├── Function
  │
  ├── Method
  │
  ├── Variable
  │
  ├── Constant
  │
  ├── Import
  │
  ├── Reference
  │
  ├── Call
  │
  ├── Inheritance
  │
  └── Dependency
```

例如 Go：

```go
type Agent struct {
    runtime Runtime
}

func (a *Agent) Run(ctx context.Context) error {
    return a.runtime.Execute(ctx)
}
```

统一模型可以表示成：

```text
Type
 └── Agent
      │
      └── Field
           └── runtime : Runtime

Method
 └── Agent.Run
      │
      └── Call
           └── Runtime.Execute
```

---

# 3. Java 也转换成同样的模型

```java
class Agent {

    private Runtime runtime;

    public void run(Context ctx) {
        runtime.execute(ctx);
    }
}
```

仍然得到：

```text
Class
 └── Agent
      │
      ├── Field
      │    └── runtime : Runtime
      │
      └── Method
           └── run()
                │
                └── Call
                     └── Runtime.execute()
```

因此上面的 Agent Tool 可以统一：

```text
find_symbol("Agent")
find_method("Agent.run")
find_callers("Agent.run")
find_callees("Agent.run")
find_references("Runtime")
find_implementations("Runtime")
```

不需要让 LLM 知道：

```text
这是 Go
这是 Java
这是 Python
```

---

# 4. 但不能追求 100% 统一

这里非常重要。

不同语言之间有大量无法完全统一的概念。

例如：

### Go

```text
package
interface
struct
goroutine
channel
```

### Java

```text
package
class
interface
annotation
generic
thread
```

### Python

```text
module
class
decorator
dynamic attribute
metaclass
```

### JavaScript

```text
module
prototype
closure
dynamic object
promise
```

### ABAP

```text
REPORT
FUNCTION MODULE
CLASS
METHOD
FORM
PERFORM
DDIC
CDS
BAdI
Enhancement
Transaction
Internal Table
```

所以 UCM 应该采用：

```text
Common Model
+
Language Extension
```

而不是：

```text
所有语言强行统一
```

---

# 5. 我会设计成两层 Schema

例如：

```text
Universal Layer
────────────────────────

Symbol
Reference
Call
Type
Function
Method
Dependency
Module
File


Language Extension
────────────────────────

GoExtension
JavaExtension
PythonExtension
TypeScriptExtension
ABAPExtension
```

例如：

```json
{
  "symbol": "Agent.Run",
  "kind": "method",
  "language": "go",

  "universal": {
    "owner": "Agent",
    "returns": "error",
    "calls": [
      "Runtime.Execute"
    ]
  },

  "language_specific": {
    "receiver": "*Agent",
    "goroutine": false,
    "defer": false
  }
}
```

这样通用 Agent 可以使用：

```text
calls
references
definition
implementation
dependencies
```

而需要语言特性时再查询：

```text
go.goroutine
java.annotation
python.decorator
abap.ddic
```

---

# 6. Parser 层也不要自己写

现在已经有比较成熟的跨语言解析方案。

一个非常适合 Agent Harness 的思路是：

```text
Tree-sitter
```

它可以作为第一层统一 parser。

大致：

```text
Go       ─┐
Java     ─┤
Python   ─┤
Rust     ─┤
JS/TS    ─┤──→ Tree-sitter → AST
C/C++    ─┤
C#       ─┤
...      ─┘
```

然后：

```text
AST
 ↓
Language Adapter
 ↓
UCM
```

但这里要注意：

> **Tree-sitter 解决的是“语法解析”，不是完整的代码语义分析。**

所以不要直接把 Tree-sitter AST 当你的 Code Intelligence。

---

# 7. 再增加 Semantic Adapter

最终：

```text
Source
  ↓
Parser
  ↓
AST
  ↓
Language Semantic Adapter
  ↓
UCM
```

例如：

```text
Go Adapter
├── AST
├── Type Resolution
├── Import Resolution
├── Interface Resolution
├── Method Resolution
└── Call Resolution
```

Java：

```text
Java Adapter
├── AST
├── Type Resolution
├── Generic Resolution
├── Annotation
├── Inheritance
└── Call Resolution
```

Python：

```text
Python Adapter
├── AST
├── Import Resolution
├── Decorator
├── Class
├── Dynamic Reference
└── Call Resolution
```

---

# 8. 还有一个非常重要的问题：动态语言

这是你设计的时候必须考虑的。

例如：

```python
obj.execute()
```

静态分析可能无法确定：

```text
obj = ?
```

所以：

```text
Java / Go
```

通常可以得到比较准确的：

```text
Call Graph
```

而：

```text
Python / JavaScript
```

可能只能得到：

```text
Candidate Call Graph
```

因此 UCM 里面最好不是：

```text
call → target
```

而是：

```text
Call
 │
 ├── resolved_target
 │
 ├── candidate_targets
 │
 └── confidence
```

例如：

```json
{
  "call": "obj.execute()",
  "target": null,
  "candidates": [
    "Foo.execute",
    "Bar.execute"
  ],
  "confidence": 0.63
}
```

这和我前面说的 **Confidence** 就连接起来了。

---

# 9. 这样 Agent 就可以跨语言工作

例如用户问：

> 找一下这个 API 从 HTTP 请求到数据库的调用链。

项目可能是：

```text
React
   ↓
TypeScript
   ↓
Java
   ↓
Python
   ↓
PostgreSQL
```

传统 Agent：

```text
grep
grep
grep
cat
grep
...
```

你的 Code Intelligence 可以：

```text
HTTP Endpoint
     ↓
TypeScript Function
     ↓
REST API
     ↓
Java Controller
     ↓
Java Service
     ↓
Python RPC
     ↓
Python Function
     ↓
SQL
```

然后只把：

```text
关键路径
+
关键代码片段
```

交给 LLM。

---

# 10. 我甚至建议加入 Runtime Evidence

因为静态分析永远不可能完全解决动态语言。

所以可以：

```text
Static Analysis
      +
Runtime Analysis
      +
Git History
      +
LLM Semantic Analysis
```

例如：

```text
Static:
A → B → C

Runtime:
A → B → D

Git:
最近修改了 B → D

LLM:
D 很可能是实际实现
```

最终：

```text
Code Intelligence
```

不再只是 AST。

---

# 11. 最终可以形成一个“代码知识图谱”

例如：

```text
                     ┌──────────────┐
                     │   Project    │
                     └──────┬───────┘
                            │
                         contains
                            │
                            ▼
                       ┌─────────┐
                       │ Module  │
                       └────┬────┘
                            │
                         contains
                            │
                            ▼
                       ┌─────────┐
                       │ Symbol  │
                       └────┬────┘
                            │
             ┌──────────────┼──────────────┐
             │              │              │
           calls        references     implements
             │              │              │
             ▼              ▼              ▼
          Symbol         Symbol          Symbol
```

这实际上已经不是：

> Code Search

而是：

> **Code Knowledge Graph**

---

# 12. Agent Tool 就可以非常少

最终我反而建议你不要给 LLM 几十个代码工具。

可能只需要：

```text
code.search
code.inspect
code.navigate
code.impact
code.trace
code.diff
```

例如：

### code.search

```json
{
  "query": "timeout",
  "scope": "agent"
}
```

### code.inspect

```json
{
  "symbol": "Agent.run",
  "include": [
    "definition",
    "calls",
    "references"
  ]
}
```

### code.trace

```json
{
  "from": "HTTPHandler",
  "to": "Database"
}
```

### code.impact

```json
{
  "symbol": "TimeoutConfig"
}
```

返回：

```text
affected symbols
affected files
affected tests
```

---

# 13. 语言支持可以分级

不要一开始就试图支持 30 种语言。

可以设计：

```text
Tier 0
纯文本
    ↓
grep / regex

Tier 1
语法解析
    ↓
Tree-sitter

Tier 2
Symbol Intelligence
    ↓
definition/reference

Tier 3
Semantic Intelligence
    ↓
type/call/inheritance

Tier 4
Runtime Intelligence
    ↓
dynamic behavior
```

例如：

| Language   | Syntax | Symbol | Semantic |     Runtime |
| ---------- | -----: | -----: | -------: | ----------: |
| Go         |      ✓ |      ✓ |        ✓ |           ✓ |
| Java       |      ✓ |      ✓ |        ✓ |           ✓ |
| TypeScript |      ✓ |      ✓ |        ✓ |           ✓ |
| Python     |      ✓ |      ✓ |        △ |           ✓ |
| Rust       |      ✓ |      ✓ |        ✓ |           ✓ |
| C/C++      |      ✓ |      ✓ |        ✓ |           ✓ |
| ABAP       |      ✓ |      ✓ |       自建 | SAP Runtime |
| Shell      |      ✓ |      △ |        △ |           ✓ |

这样你的 Harness **不会因为支持新语言而重写核心架构**。

---

# 14. 更进一步：语言只是一个 Adapter

最终你的架构应该是：

```text
                 Agent Harness
                       │
                Context Compiler
                       │
                Universal Code API
                       │
        ┌──────────────┼───────────────┐
        │              │               │
   Code Graph      Symbol DB       Semantic DB
        │              │               │
        └──────────────┼───────────────┘
                       │
                 Language Layer
                       │
       ┌───────┬───────┼───────┬───────┐
       │       │       │       │       │
      Go     Java   Python    TS     ABAP
       │       │       │       │       │
    Parser  Parser  Parser   Parser  Parser
```

**Agent Harness 核心完全不关心语言。**

只有：

```text
Language Adapter
```

关心：

```text
这个语言如何解析
这个语言如何找 symbol
这个语言如何 resolve type
这个语言如何找 reference
这个语言如何构建 call graph
```

---

## 15. 这也能很好解决你最开始的 Token 问题

最终 LLM 不再看到：

```text
5000 个文件
10000 次 grep
100000 行代码
```

而看到：

```text
Task:
修改 Agent timeout

Relevant Knowledge:

Agent.run
  ↓
ToolExecutor.execute
  ↓
ToolConfig.timeout

Relevant Files:
executor.go
config.go

Relevant Tests:
executor_test.go

Recent Changes:
config.go changed 2 commits ago

Confidence:
0.94
```

然后只读取：

```text
Agent.run()
ToolExecutor.execute()
ToolConfig
相关测试
```

**这才是多语言 Agent Harness 最合理的方向。**

而且你前面提到的 **ABAP Runtime / ABAP Compiler** 方向其实也可以直接复用这个设计：ABAP 不需要污染 Universal Code Model，只需要做一个 `ABAP Language Adapter`，再额外加入 `DDIC / CDS / Function Module / BAdI / Enhancement / Transaction` 等 ABAP-specific graph。这样未来甚至可以让同一个 Agent 同时理解 **Go + Java + ABAP + SQL + TypeScript** 项目。


对，这两个问题其实决定了这个架构最后会不会“越做越重”。

我会把设计原则调整成一句话：

> **不要维护一个“完整理解整个项目”的巨大系统，而是维护一个“增量、按需、可失效”的代码知识缓存。**

这样既能及时同步文件变化，也不会因为支持 20 种语言而把 Agent Harness 做成一个庞大的 IDE。

---

# 1. 文件修改同步：不要重新扫描项目

假设 Agent 修改：

```text
internal/agent/agent.go
```

不要：

```text
修改文件
 ↓
重新扫描整个项目
 ↓
重新建立 AST
 ↓
重新建立 Symbol
 ↓
重新建立 Call Graph
```

应该：

```text
文件变化
   ↓
File Watcher
   ↓
计算 File Hash
   ↓
判断是否真的变化
   ↓
只解析这个文件
   ↓
更新这个文件对应的 Index
   ↓
更新受影响的 Graph
   ↓
Invalidate Cache
```

也就是：

```text
                 File Change
                     │
                     ▼
                Change Detector
                     │
                     ▼
                Changed Files
                     │
                     ▼
             Incremental Parser
                     │
                     ▼
               Changed Symbols
                     │
              ┌──────┴──────┐
              ▼             ▼
        Symbol Index     Dependency
              │             │
              └──────┬──────┘
                     ▼
                 Cache Invalidate
```

---

# 2. 最重要的是“变化传播”，而不是全量更新

例如：

```text
A.go
 └── Agent.run()

B.go
 └── ToolExecutor.execute()

C.go
 └── TimeoutConfig
```

关系：

```text
A → B → C
```

如果用户修改：

```text
C.go
```

你只需要：

```text
重新 parse C.go
      ↓
更新 TimeoutConfig
      ↓
找 references
      ↓
标记 B / A 为 potentially affected
```

不一定马上重新分析 A、B。

可以采用：

```text
Fresh
Stale
Dirty
Unknown
```

四种状态。

---

# 3. 建议每个知识节点都带版本

例如：

```json
{
  "symbol": "ToolExecutor.execute",
  "file": "executor.go",
  "file_hash": "abc123",
  "commit": "8f1234",
  "index_version": 17,
  "status": "fresh"
}
```

文件修改：

```text
executor.go
hash abc123
        ↓
hash def456
```

马上：

```text
ToolExecutor.execute
status = dirty
```

而不是马上把整个知识库重建。

---

# 4. Agent 修改文件时，可以直接更新 Index

这个非常重要。

因为你的 Agent 自己就知道：

```text
EditTool
```

修改了：

```text
executor.go
```

所以没必要再等 filesystem watcher。

可以：

```text
Agent
 ↓
Edit File
 ↓
Index Update
 ↓
Context Update
```

而 filesystem watcher 作为第二道保险：

```text
External IDE
VSCode
IntelliJ
vim
git checkout
git pull
```

这些修改由 watcher 捕获。

最终：

```text
               File Change
                    │
          ┌─────────┴─────────┐
          │                   │
     Agent Edit          External Edit
          │                   │
          └─────────┬─────────┘
                    ▼
             Change Manager
                    │
                    ▼
          Incremental Indexer
```

---

# 5. Git 也可以作为第三层同步机制

例如：

```text
Agent edit
IDE edit
git checkout
git pull
branch switch
merge
rebase
```

都可能改变项目。

因此启动 Agent 时：

```text
current HEAD
working tree
index state
```

都检查。

例如：

```text
Index:
commit = AAAAA

Current:
commit = BBBBB
working tree = modified
```

那么：

```text
Index Manager
 ↓
git diff AAAAA..BBBBB
 ↓
只重新处理 changed files
```

---

# 6. 甚至不需要实时解析所有修改

这里可以进一步优化。

例如用户正在编辑：

```text
foo.go
```

IDE 每敲一个字符都会触发：

```text
file changed
```

如果你每次都：

```text
parse
AST
symbol
graph
```

反而浪费 CPU。

所以可以 debounce：

```text
0ms      修改
50ms     修改
100ms    修改
150ms    修改
...
500ms    停止修改

             ↓

         Parse once
```

例如：

```text
debounce = 300~1000ms
```

---

# 7. 更进一步：两级 Index

这是我比较推荐你的设计。

```text
              Source Code
                   │
          ┌────────┴─────────┐
          │                  │
      Fast Index          Deep Index
          │                  │
       <100ms              async
          │                  │
       syntax             semantic
       symbols            call graph
       imports            type graph
```

文件刚修改：

```text
立即更新：

AST
Symbol
Line
Function
Class
Import
```

后台再更新：

```text
Call Graph
Type Resolution
Reference
Semantic Index
```

这样 Agent 不会因为索引更新阻塞用户操作。

---

# 8. 第二个问题：多语言 Adapter 确实可能导致项目膨胀

如果你采用：

```text
GoAdapter
JavaAdapter
PythonAdapter
RustAdapter
CAdapter
CppAdapter
TypeScriptAdapter
...
```

每个 Adapter 都自己实现：

```text
Parser
AST
Symbol
Reference
Type
Call
Dependency
...
```

最后很容易变成：

```text
20 languages
×
10 intelligence features
=
200 implementations
```

这确实不可维护。

所以这里我建议你**不要自己实现语言 Parser**。

---

# 9. Adapter 应该非常薄

推荐：

```text
Language Adapter
       │
       ├── detect
       ├── parse
       ├── symbols
       ├── references
       └── language-specific semantics
```

核心能力全部放在：

```text
Core Engine
```

例如：

```text
                    Core Engine
                        │
      ┌─────────────────┼─────────────────┐
      │                 │                 │
    Index            Graph             Cache
      │                 │                 │
      └─────────────────┼─────────────────┘
                        │
                 Language Adapter
                        │
          ┌─────────────┼──────────────┐
          │             │              │
       Tree-sitter   LSP/Compiler   Custom
```

---

# 10. 我尤其建议：优先利用 LSP

这是降低多语言开发成本的关键。

很多语言本身已经有：

```text
Language Server
```

例如：

```text
Go       → gopls
Rust     → rust-analyzer
Python   → pyright / pylsp
TypeScript → tsserver
Java     → Eclipse JDT LS
C/C++    → clangd
```

这些工具已经实现了大量：

```text
definition
reference
symbol
type
diagnostics
workspace symbol
```

你没必要重新造轮子。

---

# 11. 所以你的 Language Adapter 可以只有三种模式

```text
Language Adapter
      │
      ├── Parser Adapter
      │
      ├── LSP Adapter
      │
      └── Custom Adapter
```

### 第一种：Parser Adapter

适合：

```text
结构分析
symbol
AST
快速索引
```

---

### 第二种：LSP Adapter

适合：

```text
definition
reference
type
diagnostic
workspace symbol
```

这是最值得优先利用的。

---

### 第三种：Custom Adapter

只给：

```text
ABAP
特殊 DSL
特殊框架
```

使用。

例如你前面研究的 ABAP：

```text
ABAP Adapter
    │
    ├── ABAP Parser
    ├── DDIC
    ├── CDS
    ├── Function Module
    ├── BAdI
    ├── Enhancement
    └── SAP-specific dependency
```

这样 ABAP 的特殊逻辑不会污染核心。

---

# 12. 最终你可能只需要几十 KB 的 Adapter 代码

理想状态：

```text
core/
  index/
  graph/
  cache/
  context/
  invalidation/
  retrieval/

adapters/
  lsp/
    go
    java
    python
    typescript
    rust

  parser/
    tree-sitter

  custom/
    abap
```

而不是：

```text
go-engine/
java-engine/
python-engine/
typescript-engine/
...
```

---

# 13. 更进一步：不要一开始索引所有东西

这是解决“项目膨胀”的另一个关键。

比如项目：

```text
2 million lines
```

不要启动时做：

```text
AST
+
Symbol
+
Reference
+
Call Graph
+
Embedding
+
Summary
```

全部生成。

启动阶段只做：

```text
Project Detection
      ↓
File Index
      ↓
Language Detection
      ↓
Lightweight Symbol Index
```

例如：

```text
启动：
300ms ~ 几秒
```

然后：

```text
Agent 真正需要某个模块
       ↓
Lazy Index
       ↓
Deep Analysis
```

---

# 14. 也就是说：Lazy Code Intelligence

例如项目：

```text
frontend/
backend/
worker/
legacy/
tests/
docs/
```

用户问：

> 修改 backend 的 timeout。

不要分析：

```text
frontend
worker
legacy
docs
```

只分析：

```text
backend
```

然后：

```text
backend
 ↓
相关 module
 ↓
相关 symbol
 ↓
相关 call graph
```

---

# 15. 我会把 Index 分成三种状态

非常实用：

```text
UNINDEXED
     ↓
LIGHT
     ↓
DEEP
```

例如：

```text
foo.go

UNINDEXED
 ↓
发现是 Go
 ↓
LIGHT
 ├── AST
 ├── symbols
 └── imports

Agent 真正查询
 ↓
DEEP
 ├── references
 ├── types
 ├── call graph
 └── semantic
```

如果文件修改：

```text
DEEP
 ↓
DIRTY
 ↓
LIGHT
 ↓
DEEP
```

---

# 16. 这样你的整个 Agent Harness 会非常轻

我建议最终架构变成：

```text
                      Agent
                        │
                        ▼
                Context Compiler
                        │
                        ▼
                 Code Intelligence
                        │
        ┌───────────────┼────────────────┐
        │               │                │
     File Index      Symbol Index      Graph
        │               │                │
        └───────────────┼────────────────┘
                        │
                 Language Runtime
                        │
           ┌────────────┼────────────┐
           │            │            │
          LSP       Tree-sitter    Custom
           │            │            │
       Go/Java/...      AST         ABAP
                        │
                        ▼
                  Source Files
                        ▲
                        │
                  Change Manager
                        ▲
             ┌──────────┴─────────┐
             │                    │
          Agent Edit           IDE/Git
```

---

# 17. 甚至可以把“同步”完全设计成事件驱动

例如：

```json id="v1u0tg"
{
  "event": "file.changed",
  "path": "internal/agent/executor.go",
  "old_hash": "abc",
  "new_hash": "def",
  "source": "agent"
}
```

进入：

```text
Change Manager
```

然后：

```text
file.changed
      ↓
invalidate file
      ↓
parse
      ↓
update symbols
      ↓
update references
      ↓
mark affected graph nodes
      ↓
invalidate context cache
```

---

# 18. Context Cache 也必须跟着失效

这是非常容易遗漏的。

假设之前 LLM 已经得到：

```text
Agent.run
 ↓
ToolExecutor.execute
 ↓
TimeoutConfig
```

然后用户修改：

```text
TimeoutConfig
```

那么：

```text
Code Index
```

更新还不够。

必须同时：

```text
invalidate:

Project Summary
Module Summary
Symbol Summary
Exploration Cache
Task Context
Semantic Retrieval Cache
```

否则 Agent 会拿着旧知识继续工作。

---

# 19. 可以采用“依赖版本”解决这个问题

例如：

```text
TimeoutConfig
version = 18
```

那么：

```text
TaskContext
requires:

TimeoutConfig@18
ToolExecutor@32
Agent.run@51
```

文件变化：

```text
TimeoutConfig@19
```

Context Compiler 一看：

```text
TaskContext requires TimeoutConfig@18
current = @19

→ stale
```

自动重新获取。

这比粗暴地：

```text
任何文件修改
→ 清空所有 cache
```

高效得多。

---

# 20. 所以我现在反而不建议你做“大而全的代码分析器”

你原来的担心是对的：

> 不同语言 + 实时同步 + AST + Graph + Semantic + Vector，很容易把 Agent Harness 做成一个庞然大物。

我的建议是把它控制在：

```text
                    Agent Harness
                         │
                ┌────────┴────────┐
                │                 │
           Agent Runtime      Code Intelligence
                                  │
                     ┌────────────┼────────────┐
                     │            │            │
                   Index        Cache        Change
                     │                         │
                     └────────────┬────────────┘
                                  │
                           Language Bridge
                                  │
                     ┌────────────┼────────────┐
                     │            │            │
                    LSP      Tree-sitter    Custom
```

其中 **Core 只负责协议、缓存、版本、索引、Graph、Context**。

语言差异尽量交给：

```text
LSP
+
Tree-sitter
+
少量 Custom Adapter
```

---

## 21. 如果让我给这个系统定一个核心设计原则

我会定成这五条：

```text
① Incremental
   只处理变化的文件

② Lazy
   不主动分析 Agent 不需要的代码

③ Cached
   已经探索过的内容不重复探索

④ Versioned
   所有知识和 Context 都绑定代码版本

⑤ Language-Agnostic
   Harness 不理解具体语言，
   Language Adapter 才理解
```

这样你最开始提出的两个问题其实可以统一解决：

```text
                Code Repository
                       │
                       ▼
                Change Manager
                       │
               Incremental Update
                       │
                       ▼
              Universal Code Index
                       │
           ┌───────────┴───────────┐
           │                       │
      Language Adapter        Version Manager
           │                       │
           └───────────┬───────────┘
                       ▼
                Knowledge Cache
                       │
                       ▼
                Context Compiler
                       │
                       ▼
                      LLM
```

**核心不是建立一个“全项目永久索引”，而是建立一个“随着 Agent 工作逐渐形成、随着代码变化局部失效”的 Code Knowledge Cache。**

这会比较适合你现在做的 AI Agent Runtime：**Harness 保持小，Code Intelligence 可以独立成一个服务/模块，语言适配器甚至可以进程外运行**。这样以后增加 ABAP、Java、Go、Python，不需要不断膨胀主 Agent Runtime。

已经把前面讨论的内容整合成一份完整的方案文档，重点覆盖：

* Low-Token Agent Harness 总体架构
* Project Knowledge / Code Knowledge Runtime
* Universal Code Model（UCM）
* 多语言 Adapter / LSP / Tree-sitter / Custom Adapter
* Go / Java / Python / TypeScript / ABAP 等多语言策略
* Exploration Cache / Exploration Graph
* Context Planner / Context Compiler
* 增量代码索引与文件变更同步
* Agent / IDE / Git 三类代码变更处理
* Fresh / Dirty / Stale / Unknown 版本机制
* Code Graph / Symbol Index / Reference Index
* Tool Output 压缩
* Context Cache 与 Cache Invalidation
* Lazy Index / Fast Index / Deep Index
* Semantic Search 与 Vector DB 的定位
* Token 优化指标与 Telemetry
* 数据库模型
* Agent Runtime Loop
* ABAP 特殊适配层
* MVP 实施路线
* 后续演进路线

**下载：**

[下载完整方案文档：Low-Token Multi-Language AI Agent Harness](sandbox:/mnt/data/low_token_multilanguage_ai_agent_harness_design.md)

这份方案的核心架构可以概括成：

```text
                    Agent Runtime
                         │
                         ▼
                 Context Planner
                         │
                         ▼
              Code Knowledge Runtime
                         │
        ┌────────────────┼────────────────┐
        │                │                │
     File Index      Symbol Index      Code Graph
        │                │                │
        └────────────────┼────────────────┘
                         │
                 Universal Code Model
                         │
          ┌──────────────┼──────────────┐
          │              │              │
         LSP        Tree-sitter      Custom
          │              │              │
      Go/Java/...        AST          ABAP
                         │
                         ▼
                    Source Code

                         │
                         ▼
                  Context Compiler
                         │
                         ▼
                        LLM
```

我建议后续如果真正开始实现，可以把这个方案继续拆成 **《Agent Harness 技术设计详细规格》**：具体到 PostgreSQL 表结构、UCM JSON Schema、事件模型、Index API、Language Adapter SPI、Context Compiler 算法，以及 Go 项目目录和接口定义，这样就可以直接进入编码阶段。

已完成，并将上一版架构方案进一步拆成了可以直接用于研发的 **《Agent Harness 技术设计详细规格》**。

文档重点已经从“架构思想”进入到“工程实现规格”，包括：

* SQLite 完整数据库设计
* `files / symbols / references / calls / exploration / context / cache` 等表结构
* Universal Code Model（UCM）
* Language Adapter SPI
* LSP / Tree-sitter / Custom Adapter
* File Watcher + Agent Edit + Git 三种变更同步机制
* Incremental Index / Fast Index / Deep Index
* Fresh / Dirty / Stale / Unknown 状态
* Exploration Cache / Exploration Graph
* Context Planner
* Context Compiler
* Observation Compressor
* Code Intelligence API
* `code.search / inspect / navigate / trace / impact / diff`
* Cache Dependency / Invalidation
* SQLite WAL、事务、GC、Migration
* FTS5 搜索
* 多语言和跨语言项目
* ABAP Adapter 演进路线
* Token Budget 和 Anti-Repetition
* Telemetry / KPI
* 测试方案
* MVP 分阶段实施路线
* Go Interface / SPI 示例
* 后续从 SQLite 向 PostgreSQL / Vector DB 演进的边界

### 下载

[下载《Agent Harness 技术设计详细规格》Markdown](sandbox:/mnt/data/agent_harness_technical_design_spec_sqlite.md)

这一版的核心已经可以作为后续真正实现 `Agent Runtime + Code Knowledge Runtime + Context Compiler` 的技术基线。
