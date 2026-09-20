# Low-Token Multi-Language AI Agent Harness 设计方案

> 版本：v1.0  
> 日期：2026-09-20  
> 定位：面向 AI Coding Agent / Agent Runtime / Agent Harness 的代码智能、上下文编译、多轮探索缓存与多语言支持架构

---

## 1. 摘要

当前 AI Coding Agent 的一个核心问题是：LLM 大量 token 消耗在项目探索，而不是消耗在真正的代码理解、推理和修改上。

典型流程是：

```text
LLM
 ↓
list files
 ↓
grep
 ↓
read file
 ↓
grep
 ↓
read another file
 ↓
LLM
 ↓
继续搜索
```

多轮会话尤其严重，因为 Agent 经常重复探索已经探索过的项目。

本方案的核心目标不是简单减少 Tool Call，而是：

> **把“代码探索”从 LLM 的重复行为，升级为由 Agent Harness 管理的 Code Intelligence + Knowledge Cache + Context Compiler。**

核心设计原则：

1. Incremental：只处理发生变化的代码。
2. Lazy：只深入分析 Agent 当前任务需要的代码。
3. Cached：已经探索过的内容尽量复用。
4. Versioned：知识与代码版本绑定，避免使用过期上下文。
5. Language-Agnostic：核心 Harness 不绑定具体编程语言。
6. LLM Focused：LLM 负责理解、推理和决策，不负责反复发现项目结构。
7. Small Core：语言能力通过 LSP、Tree-sitter 和少量 Custom Adapter 提供，避免 Harness 膨胀。

---

# 2. 问题定义

## 2.1 当前 Agent 的主要 Token 消耗

假设一次任务：

```text
用户：
修改 Agent Tool Timeout。
```

传统 Agent 可能执行：

```text
ls
find
grep timeout
grep Agent
read agent.go
grep execute
read executor.go
grep config
read config.go
grep tests
read executor_test.go
```

这些工具输出进入 LLM Context 后，会产生大量 token 消耗。

真正需要 LLM 理解的可能只有：

```text
Agent.run()
ToolExecutor.execute()
ToolConfig.Timeout
executor_test.go
```

因此问题可以抽象为：

```text
Token Cost =
    Exploration Cost
  + Reasoning Cost
  + Generation Cost
```

当前很多 Agent：

```text
Exploration Cost >> Reasoning Cost
```

目标是：

```text
Exploration Cost << Reasoning Cost
```

---

# 3. 核心架构

整体架构：

```text
                         User
                          │
                          ▼
                 ┌────────────────┐
                 │  Task Analyzer │
                 └───────┬────────┘
                         │
                         ▼
                ┌──────────────────┐
                │ Context Planner  │
                └────────┬─────────┘
                         │
          ┌──────────────┼──────────────┐
          ▼              ▼              ▼
     Task Memory    Project Memory    Cache
          │              │              │
          └──────────────┼──────────────┘
                         ▼
              ┌─────────────────────┐
              │ Code Intelligence   │
              │                     │
              │ File Index          │
              │ Symbol Index        │
              │ Reference Index     │
              │ Call Graph          │
              │ Type Graph          │
              │ Dependency Graph    │
              │ Semantic Index      │
              └──────────┬──────────┘
                         │
                         ▼
              ┌─────────────────────┐
              │ Context Compiler    │
              └──────────┬──────────┘
                         │
                         ▼
                      ┌───────┐
                      │  LLM  │
                      └───┬───┘
                          │
                    Action / Tool
                          │
                          ▼
              ┌─────────────────────┐
              │ Tool Executor       │
              └──────────┬──────────┘
                         │
                         ▼
              ┌─────────────────────┐
              │ Observation         │
              │ Compressor          │
              └──────────┬──────────┘
                         │
                         ▼
              ┌─────────────────────┐
              │ Knowledge Update    │
              └─────────────────────┘
```

---

# 4. 最核心的设计：Code Knowledge Runtime

建议将代码智能能力抽象为独立模块：

```text
Code Knowledge Runtime
```

它不是 IDE，也不是完整编译器，而是：

> 面向 Agent 的、按需构建和增量维护的项目代码知识缓存。

主要职责：

```text
Project Discovery
File Index
Symbol Index
Reference Index
Call Graph
Dependency Graph
Language Detection
Incremental Update
Cache Invalidation
Exploration Memory
Context Retrieval
```

---

# 5. Universal Code Model（UCM）

为了支持 Go、Java、Python、TypeScript、Rust、C/C++、ABAP 等不同语言，核心不能直接绑定某种语言。

建立 Universal Code Model：

```text
Project
 ├── Module
 ├── Package
 ├── File
 ├── Namespace
 ├── Type
 ├── Class
 ├── Interface
 ├── Function
 ├── Method
 ├── Variable
 ├── Constant
 ├── Import
 ├── Reference
 ├── Call
 ├── Inheritance
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

统一模型：

```text
Type: Agent
 └── Field: runtime : Runtime

Method: Agent.Run
 └── Call: Runtime.Execute
```

Java、Python、TypeScript 可以映射到相同的 Universal Model。

---

# 6. Universal Model 不强行统一所有语言

不同语言有独特语义，因此采用：

```text
Common Model
+
Language Extension
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
    "calls": ["Runtime.Execute"]
  },
  "language_specific": {
    "receiver": "*Agent",
    "goroutine": false,
    "defer": false
  }
}
```

ABAP 可以拥有：

```text
ABAP Extension
 ├── REPORT
 ├── FUNCTION MODULE
 ├── CLASS
 ├── METHOD
 ├── FORM
 ├── PERFORM
 ├── DDIC
 ├── CDS
 ├── BAdI
 ├── Enhancement
 └── Transaction
```

这样 ABAP 特殊能力不会污染核心。

---

# 7. Language Adapter 设计

不要为每种语言完整重写一个分析器。

Language Adapter 采用三种来源：

```text
Language Adapter
 ├── LSP Adapter
 ├── Tree-sitter / Parser Adapter
 └── Custom Adapter
```

## 7.1 LSP Adapter

优先复用现有语言服务器能力：

```text
Go          → gopls
Rust        → rust-analyzer
Python      → pyright / pylsp
TypeScript  → tsserver
Java        → Eclipse JDT LS
C/C++       → clangd
```

典型能力：

```text
definition
references
workspace symbol
type
diagnostics
implementation
```

Harness 不重新实现这些能力。

## 7.2 Tree-sitter / Parser Adapter

用于：

```text
AST
syntax
symbol
function/class structure
imports
快速索引
```

Tree-sitter 负责语法结构，而不是完整语义分析。

## 7.3 Custom Adapter

只用于标准 LSP / parser 无法表达的重要领域。

例如 ABAP：

```text
ABAP Adapter
 ├── ABAP Parser
 ├── DDIC
 ├── CDS
 ├── Function Module
 ├── BAdI
 ├── Enhancement
 └── SAP-specific dependency
```

---

# 8. 语言支持分级

不需要一开始支持所有语言。

建议：

```text
Tier 0
Text / Regex

Tier 1
Syntax / AST

Tier 2
Symbol Intelligence

Tier 3
Semantic Intelligence

Tier 4
Runtime Intelligence
```

示例：

| Language | Syntax | Symbol | Semantic | Runtime |
|---|---:|---:|---:|---:|
| Go | ✓ | ✓ | ✓ | ✓ |
| Java | ✓ | ✓ | ✓ | ✓ |
| TypeScript | ✓ | ✓ | ✓ | ✓ |
| Python | ✓ | ✓ | △ | ✓ |
| Rust | ✓ | ✓ | ✓ | ✓ |
| C/C++ | ✓ | ✓ | ✓ | ✓ |
| ABAP | ✓ | ✓ | Custom | SAP Runtime |
| Shell | ✓ | △ | △ | ✓ |

---

# 9. 动态语言与不确定性

Python、JavaScript 等动态语言不一定能静态确定调用目标。

因此 Graph 不应只有：

```text
Call → Target
```

而应支持：

```text
Call
 ├── resolved_target
 ├── candidate_targets
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

这样 Context Planner 可以根据 confidence 决定是否进一步探索。

---

# 10. Code Intelligence API

不要给 LLM 大量底层工具。

建议把：

```text
grep
find
cat
list
```

隐藏在 Code Intelligence 层。

LLM 看到的工具可以控制在：

```text
code.search
code.inspect
code.navigate
code.trace
code.impact
code.diff
```

## code.search

```json
{
  "query": "timeout",
  "scope": "agent"
}
```

## code.inspect

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

## code.trace

```json
{
  "from": "HTTPHandler",
  "to": "Database"
}
```

## code.impact

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

# 11. Hierarchical Code Context

不要把整个项目放进 Context。

采用：

```text
Level 0: Project
    ↓
Level 1: Module
    ↓
Level 2: File
    ↓
Level 3: Symbol
    ↓
Level 4: Code
```

例如：

```text
Project
 ↓
agent module
 ↓
Agent
 ↓
ToolExecutor
 ↓
timeout
```

最后只读取：

```text
ToolExecutor.execute()
ToolConfig
相关测试
```

---

# 12. Exploration Memory

多轮会话必须保存已经探索的知识。

例如：

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

第二轮用户说：

```text
把 timeout 改成 60 秒
```

无需重新搜索。

---

# 13. Exploration Graph

把 Agent 的探索过程记录为图：

```text
Conversation
      │
      ▼
Task
      │
      ▼
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

新的任务先检查已有探索结果。

---

# 14. Context Planner

Context Planner 决定：

```text
是否需要重新探索？
需要哪些知识？
读取哪些代码？
哪些历史内容可以复用？
```

例如：

```text
已有知识：
TimeoutConfig
Agent.run
ToolExecutor.execute

confidence = 0.94

需要探索？
NO
```

而：

```text
用户：
为什么 Agent 在生产环境偶尔死锁？

confidence = 0.32

需要探索？
YES
```

再启动针对性 Code Intelligence。

---

# 15. Context Compiler

建议把 Context Compiler 作为整个系统的核心。

输入：

```text
User Task
+
Project Knowledge
+
Task History
+
Exploration Graph
+
Git Changes
+
Code Intelligence
```

输出：

```text
LLM Context
```

也就是说：

```text
Repository
   ↓
Code Knowledge
   ↓
Context Compiler
   ↓
LLM
```

LLM 不直接决定“项目里还有哪些代码需要探索”。

---

# 16. Context 分层

建议：

```text
L0 System Context
  Agent rules
  Tool definitions

L1 Project Context
  Project architecture
  Project map

L2 Task Context
  Relevant symbols
  Exploration graph
  Constraints

L3 Working Context
  Current code
  Current tool results
  Current reasoning
```

原则：

> L1/L2 尽量结构化，L3 才承载高价值代码文本。

---

# 17. Tool Output Compression

原始：

```text
grep -R "timeout" .
```

可能产生几百行结果。

不要直接放入 LLM。

采用：

```text
Raw Tool Output
      ↓
Observation Compressor
      ↓
Structured Observation
      ↓
Knowledge Store
```

例如：

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

---

# 18. Symbol 级代码读取

不要只提供：

```text
read_file(path, start_line, end_line)
```

增加：

```text
read_symbol("ToolExecutor.execute")
```

返回：

```text
signature
implementation
dependencies
callers
callees
related tests
```

这会显著降低无关代码进入 Context 的概率。

---

# 19. 文件修改同步

这是系统必须具备的能力。

总体：

```text
                   File Change
                       │
            ┌──────────┴──────────┐
            │                     │
        Agent Edit           External Edit
            │                     │
            └──────────┬──────────┘
                       ▼
                Change Manager
                       │
                       ▼
              Incremental Indexer
                       │
             ┌─────────┴─────────┐
             ▼                   ▼
       Symbol Update       Graph Update
             │                   │
             └─────────┬─────────┘
                       ▼
                Cache Invalidate
```

---

# 20. Agent 修改文件

Agent 自己修改文件时，不需要等待 watcher。

直接：

```text
Agent
 ↓
Edit File
 ↓
Index Update
 ↓
Context Update
```

同时 filesystem watcher 作为保险机制，用于捕获：

```text
VSCode
IntelliJ
vim
git checkout
git pull
branch switch
merge
rebase
```

---

# 21. 文件版本

每个代码知识节点带版本：

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

修改后：

```text
abc123
   ↓
def456
```

对应 Symbol：

```text
status = dirty
```

而不是立即重建整个项目。

---

# 22. Fresh / Stale / Dirty / Unknown

建议知识节点支持：

```text
Fresh
Stale
Dirty
Unknown
```

例如：

```text
文件修改
 ↓
Symbol = Dirty
 ↓
重新解析
 ↓
Symbol = Fresh
```

如果依赖发生变化：

```text
A → B → C

C 修改

B/A = potentially affected
```

不必马上深度分析 A/B。

---

# 23. 增量索引

假设：

```text
A.go → B.go → C.go
```

C.go 修改：

```text
重新 parse C.go
      ↓
更新 C symbols
      ↓
更新 references
      ↓
标记 B/A potentially affected
```

只有真正需要时才重新计算深层语义。

---

# 24. 两级 Index

推荐：

```text
Source Code
      │
 ┌────┴─────┐
 │          │
Fast Index  Deep Index
 │          │
<100ms      async
 │          │
AST         Call Graph
Symbols     Type Graph
Imports     References
```

用户编辑代码后：

```text
Fast Index
```

优先立即更新。

后台再：

```text
Deep Index
```

这样不会阻塞 Agent。

---

# 25. Debounce

IDE 每输入一个字符都可能产生 file changed。

因此：

```text
0ms      修改
50ms     修改
100ms    修改
150ms    修改
...
500ms    停止

        ↓

     Parse once
```

建议：

```text
300~1000ms debounce
```

具体值可配置。

---

# 26. Lazy Index

不要项目启动时把所有东西全部分析。

例如：

```text
Project
├── frontend
├── backend
├── worker
├── legacy
└── tests
```

用户问：

```text
修改 backend timeout
```

只深入：

```text
backend
 ↓
相关 module
 ↓
相关 symbol
 ↓
相关 graph
```

其他模块保持：

```text
UNINDEXED
```

---

# 27. Index 状态

建议：

```text
UNINDEXED
    ↓
LIGHT
    ↓
DEEP
```

LIGHT：

```text
AST
Symbols
Imports
```

DEEP：

```text
References
Types
Call Graph
Semantic
```

文件变化：

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

# 28. Context Cache 失效

代码变化后不能只更新 Code Index。

还要检查：

```text
Project Summary
Module Summary
Symbol Summary
Exploration Cache
Task Context
Semantic Retrieval Cache
```

因此需要统一：

```text
Cache Invalidation Manager
```

---

# 29. 版本化 Context

例如：

```text
TimeoutConfig@18
ToolExecutor@32
Agent.run@51
```

TaskContext：

```text
requires:
TimeoutConfig@18
ToolExecutor@32
Agent.run@51
```

TimeoutConfig 更新：

```text
TimeoutConfig@19
```

Context Compiler 判断：

```text
required @18
current @19

→ stale
→ refresh
```

---

# 30. Git Integration

Index 记录：

```text
indexed_commit
```

例如：

```text
Index:
AAAAAA

Current:
BBBBBB
working tree modified
```

通过：

```text
git diff AAAAAA..BBBBBB
```

找出变化文件。

只更新：

```text
changed files
changed symbols
affected graph
```

---

# 31. Code Knowledge Storage

建议数据库逻辑模型：

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

---

# 32. 推荐的 Symbol 数据模型

```text
symbols
--------------------------------
id
project_id
file_id
parent_id
name
qualified_name
kind
language
start_line
end_line
signature
visibility
file_hash
index_version
status
confidence
created_at
updated_at
```

---

# 33. Exploration Node

```text
exploration_nodes
--------------------------------
id
session_id
task_id
node_type
target
file_id
symbol_id
summary
confidence
source
version
created_at
last_used_at
```

例如：

```text
node_type = SYMBOL
target = ToolExecutor.execute
confidence = 0.96
source = AST + LSP
version = executor.go@def456
```

---

# 34. Exploration Edge

```text
exploration_edges
--------------------------------
from_node
to_node
relation
confidence
source
```

关系：

```text
CALLS
REFERENCES
IMPORTS
IMPLEMENTS
EXTENDS
DEPENDS_ON
CONTAINS
RELATED_TO
```

---

# 35. Code Graph

最终形成：

```text
                     Project
                        │
                     Module
                        │
                       File
                        │
                     Symbol
                        │
        ┌───────────────┼───────────────┐
        │               │               │
      CALLS         REFERENCES      IMPLEMENTS
        │               │               │
        ▼               ▼               ▼
     Symbol          Symbol          Symbol
```

这已经从传统：

```text
Code Search
```

升级为：

```text
Code Knowledge Graph
```

---

# 36. Semantic Search

不要让 Vector DB 成为唯一搜索方案。

采用：

```text
                 Code Retrieval
                       │
       ┌───────────────┼───────────────┐
       ▼               ▼               ▼
   Symbol Search    Graph Search   Semantic Search
       │               │               │
       └───────────────┼───────────────┘
                       ▼
                 Rank / Merge
                       ▼
                  LLM Context
```

例如：

```text
Agent.run
```

Symbol Search 更合适。

```text
谁调用 Agent.run？
```

Graph Search 更合适。

```text
处理 tool timeout 的代码
```

Semantic Search 更合适。

---

# 37. Agent Runtime Loop

推荐：

```text
User
 ↓
Task Analyzer
 ↓
Context Planner
 ↓
Knowledge Retrieval
 ↓
Context Compiler
 ↓
LLM
 ↓
Action
 ↓
Tool Executor
 ↓
Observation
 ↓
Observation Compressor
 ↓
Knowledge Update
 ↓
Context Planner
```

而不是：

```text
LLM
 ↓
Tool
 ↓
LLM
 ↓
Tool
```

---

# 38. Token 优化目标

传统：

```text
Exploration 50~70%
Reasoning   20%
Generation  10%
```

目标：

```text
Exploration 10~20%
Reasoning   50%
Generation  30%
```

具体比例应通过 telemetry 实测，不应写死。

---

# 39. Telemetry

必须记录：

```text
task_id
session_id

input_tokens
output_tokens

tool_calls
tool_tokens

exploration_tokens
reasoning_tokens
generation_tokens

cache_hit
cache_miss

index_hit
index_miss

files_read
symbols_read

context_size
context_reuse

latency
```

核心指标：

```text
Exploration Token Ratio
Repeated Exploration Ratio
Context Cache Hit Rate
Code Retrieval Precision
Tokens Per Task
Tokens Per Successful Change
```

---

# 40. Cache 层级

建议：

```text
L1 Process Cache
        ↓
L2 Session Cache
        ↓
L3 Project Knowledge Cache
        ↓
L4 Persistent Knowledge DB
        ↓
Source Repository
```

例如：

```text
当前 Task
 ↓
Session
 ↓
Project
 ↓
Database
```

越近越快。

---

# 41. Cache Key

可以采用：

```text
repo_hash
+
commit
+
working_tree_hash
+
query
+
language
+
scope
```

例如：

```text
repo=abc123
commit=8f31ab
query="Agent tool execution architecture"
```

第一次：

```text
LLM exploration
→ 20000 tokens
```

之后：

```text
cache hit
→ 几百 tokens
```

实际效果需要通过 telemetry 验证。

---

# 42. Project Map

项目第一次接入时，可以生成轻量 Project Map：

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

它作为 Project Context 的轻量入口。

但不要把整个 Project Map 永久塞进 LLM Context。

---

# 43. 推荐的核心模块

代码结构可以设计成：

```text
agent-harness/
│
├── runtime/
│   ├── agent_loop
│   ├── task_manager
│   └── tool_runtime
│
├── context/
│   ├── planner
│   ├── compiler
│   ├── compressor
│   └── cache
│
├── knowledge/
│   ├── project
│   ├── task
│   ├── exploration
│   └── memory
│
├── code/
│   ├── index
│   ├── graph
│   ├── retrieval
│   ├── change
│   └── invalidation
│
├── language/
│   ├── detector
│   ├── lsp
│   ├── parser
│   └── adapters
│
├── storage/
│   ├── sqlite/postgres
│   ├── cache
│   └── vector
│
└── tools/
    ├── code_search
    ├── code_inspect
    ├── code_trace
    ├── code_impact
    └── code_diff
```

---

# 44. 不建议的架构

## 44.1 每种语言自己实现完整 Code Engine

不要：

```text
Go Engine
Java Engine
Python Engine
TS Engine
Rust Engine
ABAP Engine
...
```

这会导致项目指数级膨胀。

## 44.2 只使用 Vector DB

Vector Search 无法替代：

```text
definition
reference
call graph
type resolution
```

## 44.3 每次任务重新扫描项目

这是当前 Token 浪费的主要来源之一。

## 44.4 所有代码进入 Context

Context 越大不代表 Agent 越聪明。

## 44.5 文件变化就重建整个 Index

应该增量更新。

---

# 45. 推荐的最终架构

```text
                         ┌───────────────┐
                         │     User      │
                         └───────┬───────┘
                                 │
                                 ▼
                    ┌─────────────────────┐
                    │    Agent Runtime    │
                    └──────────┬──────────┘
                               │
                               ▼
                    ┌─────────────────────┐
                    │    Task Analyzer    │
                    └──────────┬──────────┘
                               │
                               ▼
                    ┌─────────────────────┐
                    │   Context Planner   │
                    └──────────┬──────────┘
                               │
             ┌─────────────────┼─────────────────┐
             ▼                 ▼                 ▼
        Task Memory      Project Knowledge     Cache
             │                 │                 │
             └─────────────────┼─────────────────┘
                               ▼
                    ┌─────────────────────┐
                    │  Code Intelligence  │
                    └──────────┬──────────┘
                               │
              ┌────────────────┼────────────────┐
              ▼                ▼                ▼
          File Index       Symbol Index       Graph
              │                │                │
              └────────────────┼────────────────┘
                               │
                    ┌─────────────────────┐
                    │ Universal Code Model│
                    └──────────┬──────────┘
                               │
             ┌─────────────────┼─────────────────┐
             ▼                 ▼                 ▼
            LSP          Tree-sitter        Custom Adapter
             │                 │                 │
       Go/Java/...           AST               ABAP
                               │
                               ▼
                         Source Files
                               ▲
                               │
                       Change Manager
                               ▲
                  ┌────────────┼────────────┐
                  │            │            │
                Agent         IDE          Git
                 Edit        Changes       Changes
```

---

# 46. 分阶段实施路线

## Phase 0：Telemetry

先统计：

```text
每个 Task
用了多少 token
调用多少 Tool
读了多少文件
重复读取多少文件
```

目标：建立基线。

---

## Phase 1：Exploration Cache

实现：

```text
exploration_nodes
exploration_edges
task_context
```

优先解决：

> 多轮重复探索。

---

## Phase 2：Project Map + File Index

实现：

```text
File Index
Language Detection
Project Map
Light Symbol Index
```

---

## Phase 3：统一 Code API

实现：

```text
code.search
code.inspect
code.navigate
code.trace
code.impact
```

把：

```text
grep
find
cat
```

隐藏在内部。

---

## Phase 4：Language Bridge

优先：

```text
Tree-sitter
LSP
```

而不是自己实现各语言解析器。

---

## Phase 5：Incremental Index

实现：

```text
File Watcher
Git Diff
Agent Edit Events
Change Manager
Dirty/Fresh
Dependency Invalidation
```

---

## Phase 6：Context Compiler

实现：

```text
Task
+
Project Knowledge
+
Exploration Graph
+
Code Graph
+
Git Changes
+
Cache
```

生成最小必要 Context。

---

## Phase 7：Semantic Retrieval

最后再增加：

```text
Embedding
Vector Search
Reranking
```

---

## Phase 8：Language-specific Intelligence

最后才逐步增加：

```text
ABAP
Java
Go
Python
TypeScript
Rust
...
```

并保持 Adapter 与 Core 隔离。

---

# 47. ABAP 的特殊设计

考虑到 ABAP 与普通语言差异很大，不建议强行映射所有概念。

建议：

```text
Universal Code Model
        │
        └── ABAP Extension
              │
              ├── Program / REPORT
              ├── Function Module
              ├── Class
              ├── Method
              ├── FORM
              ├── PERFORM
              ├── DDIC Table
              ├── CDS
              ├── BAdI
              ├── Enhancement
              ├── Transaction
              └── SAP Runtime Object
```

这样以后如果继续发展 ABAP Runtime / ABAP Compiler，也可以复用这一层 Code Knowledge Model。

---

# 48. 安全与可靠性

Code Intelligence 不应直接决定修改结果。

推荐：

```text
Code Intelligence
      ↓
Evidence
      ↓
Context Compiler
      ↓
LLM
      ↓
Proposed Change
      ↓
Diff
      ↓
Validation
      ↓
Apply
```

LLM 看到的内容应尽可能带：

```text
source
version
file
line
confidence
```

避免旧 Context 导致错误修改。

---

# 49. 关键原则总结

整个方案可以浓缩成：

```text
                 DON'T

LLM
 ↓
grep
 ↓
cat
 ↓
grep
 ↓
cat
 ↓
LLM
```

变成：

```text
                 DO

Source
  ↓
Incremental Code Knowledge
  ↓
Universal Code Model
  ↓
Exploration Cache
  ↓
Context Compiler
  ↓
LLM
```

而语言支持：

```text
                 Core
                  │
          Universal Code Model
                  │
       ┌──────────┼──────────┐
       │          │          │
      LSP     Tree-sitter  Custom
       │          │          │
   Go/Java/...    AST       ABAP
```

代码变化：

```text
Agent / IDE / Git
       ↓
Change Manager
       ↓
Incremental Index
       ↓
Dependency Invalidation
       ↓
Context Cache Invalidation
```

---

# 50. 最终目标

最终 Agent Harness 不应该成为：

> 一个会调用 grep/cat/read_file 的 LLM。

而应该成为：

> **一个拥有项目级、版本化、增量更新的 Code Knowledge Runtime，并通过 Context Compiler 向 LLM 提供当前任务真正需要的最小高价值上下文。**

核心能力：

```text
Project Knowledge
       +
Code Intelligence
       +
Exploration Memory
       +
Incremental Index
       +
Language Adapter
       +
Context Compiler
       +
Cache
       =
Low-Token Agent Harness
```

其中最关键的创新点不是某个具体 Parser，而是：

```text
                    Code Knowledge
                          │
                          ▼
                 Context Compiler
                          │
                          ▼
                         LLM
```

让 LLM 从“项目探索者”变成“项目推理者”。

---

# 51. 推荐 MVP

如果资源有限，第一版只实现：

```text
1. File Index
2. Language Detection
3. Symbol Index
4. Exploration Cache
5. File Change Detection
6. Context Cache
7. Context Compiler
8. code.search
9. code.inspect
10. code.trace
```

暂时不要实现：

```text
复杂 Vector DB
完整 Call Graph
复杂 Type Graph
所有语言深度支持
完整 Runtime Analysis
```

先证明一个核心指标：

```text
同一个多轮任务：

第一次探索：N tokens

第二轮探索：
目标 < 20% N

第三轮探索：
目标 < 10% N
```

再逐步增加 semantic intelligence。

---

# 52. 结论

本方案最终形成三个边界：

```text
┌──────────────────────────────────────────┐
│              Agent Runtime               │
│                                          │
│ Task / Planning / Tool / Memory / LLM   │
└────────────────────┬─────────────────────┘
                     │
                     │ stable interface
                     ▼
┌──────────────────────────────────────────┐
│         Code Knowledge Runtime            │
│                                          │
│ Index / Graph / Cache / Context Compiler │
└────────────────────┬─────────────────────┘
                     │
                     │ language bridge
                     ▼
┌──────────────────────────────────────────┐
│            Language Ecosystem             │
│                                          │
│ LSP / Tree-sitter / Compiler / Custom    │
│ Go / Java / Python / TS / ABAP / ...    │
└──────────────────────────────────────────┘
```

这三个边界可以让 Agent Runtime 保持稳定，而语言数量、Code Intelligence 能力和具体解析技术可以独立演进。

最终目标不是：

> “让 LLM 看更多代码”。

而是：

> **让 LLM 每次只看到最相关、最新、可验证的代码。**
