# AI Gateway Skills Runtime 设计方案（历史整合稿）

> 迁移说明（2026-03-30）：
> - 本文是 2025-03-07 的历史整合设计稿，用于保留当时的 monorepo 设计上下文，不代表当前仓库目录结构。
> - 文中大量 `internal/runtime/*`、`internal/api/skills/*`、`internal/mcp/*` 与 `github.com/ai-gateway/gateway/...` 示例代码，今天应分别按 `E:\projects\ai\ai-agent-runtime\backend\internal\...` 与模块路径 `github.com/wwsheng009/ai-agent-runtime/...` 理解。
> - 如需查看当前可运行实现，请优先阅读 `docs/skill_runtime/current_architecture.md`、`docs/skill_runtime/platform_runtime_roadmap.md`，以及 `E:\projects\ai\ai-agent-runtime\backend\cmd\runtime-server\main.go`。
> - 本文保留旧命名和旧代码块，是为了保留设计演进证据，不建议按本文直接执行搭建步骤。

## 一、概述

### 1.1 设计目标

基于已有的 MCP (Model Context Protocol) 实现，构建一个完整的 **Skills Runtime** 系统，使 AI Gateway 能够：

- 动态加载和管理技能（Skills）
- 智能路由用户请求到合适的 Skill
- 支持 DAG 工作流执行
- 提供语义搜索能力
- 支持多 Agent 协作

### 1.2 系统架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                        AI Gateway                                    │
├─────────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                  │
│  │   Skills    │  │   Intent    │  │   Agent     │                  │
│  │   Runtime   │──│   Router    │──│   Loop      │                  │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘                  │
│         │                │                │                          │
│  ┌──────▼──────┐  ┌──────▼──────┐  ┌──────▼──────┐                  │
│  │   Skill     │  │  Embedding  │  │    DAG      │                  │
│  │  Registry   │  │   Router    │  │  Executor   │                  │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘                  │
│         │                │                │                          │
│  ┌──────▼────────────────▼────────────────▼──────┐                  │
│  │                MCP Manager                      │                  │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐       │                  │
│  │  │ Toolkit  │ │  Git     │ │ Custom   │ ...   │                  │
│  │  │   MCP    │ │  MCP     │ │   MCP    │       │                  │
│  │  └──────────┘ └──────────┘ └──────────┘       │                  │
│  └────────────────────────────────────────────────┘                  │
└─────────────────────────────────────────────────────────────────────┘
```

### 1.3 核心概念

| 概念 | 描述 |
|------|------|
| **Skill** | 一组相关能力的封装，包含 prompt 模板、tools、workflow |
| **Capability** | Skill 提供的具体能力，可以是单个 tool 或 workflow |
| **Intent** | 用户请求的意图，用于路由到合适的 Skill |
| **Workflow** | DAG 形式的执行计划，定义多步骤任务 |
| **Observation** | Agent 执行过程中的观察记录，用于上下文记忆 |

---

## 二、已有基础设施

### 2.1 MCP Manager (已实现)

位置: `internal/mcp/manager/manager.go`

已提供能力:
- MCP 配置加载与管理
- MCP 客户端连接管理
- Tool 注册与发现
- Tool 调用执行

```go
type Manager interface {
    LoadConfig(configPath string) error
    Start(ctx context.Context) error
    Stop() error
    ListTools() []*registry.ToolInfo
    CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error)
    FindTool(toolName string) (*registry.ToolInfo, error)
    // ...
}
```

### 2.2 MCP Registry (已实现)

位置: `internal/mcp/registry/registry.go`

已提供能力:
- Tool 注册/注销
- MCP 客户端管理
- Tool 启用/禁用

### 2.3 MCP Client (已实现)

位置: `internal/mcp/client/client.go`

已提供能力:
- Stdio/SSE/WebSocket 传输
- Tool 列表获取
- Tool 调用
- Resource 管理

---

## 三、Skills Runtime 模块设计

### 3.1 目录结构

```
internal/
├── gateway/                      # [已有] AI API 代理网关 (与 runtime 并列)
│   ├── handlers/                 # HTTP 处理器
│   │   ├── pipeline/handler/handler.go  # 代理入口（替代 proxy.go）
│   │   ├── request_transformer.go
│   │   └── ...
│   ├── loadbalancer/             # 负载均衡
│   │   ├── manager.go
│   │   ├── selector.go
│   │   ├── health.go
│   │   └── ...
│   ├── middleware/               # 中间件
│   │   ├── ratelimit.go
│   │   ├── timeout.go
│   │   ├── tracing.go
│   │   └── ...
│   ├── pipeline/                 # 请求管道
│   ├── context/                  # 请求上下文
│   ├── errors/                   # 错误处理
│   └── events/                   # 事件总线
│
├── runtime/                      # [新增] Agent 运行时 (独立子系统)
│   │
│   ├── types/                    # [新增] 共享类型定义 (避免循环引用)
│   │   ├── observation.go        # Observation 执行观察记录
│   │   ├── request.go            # Request, Result 请求响应类型
│   │   ├── agent.go              # AgentConfig, AgentState, AgentResult
│   │   ├── task.go               # Task, TaskStatus, TaskDependency
│   │   ├── skill.go              # SkillMeta, ToolRef 工具引用
│   │   ├── execution.go          # ExecutionRecord, ExecutionStatus
│   │   └── errors.go             # 统一错误类型定义
│   │
│   ├── agent/                    # Agent 核心
│   │   ├── agent.go              # Agent 定义与生命周期
│   │   ├── loop.go               # ReAct 循环
│   │   ├── reflector.go          # [扩展] 自我反思机制
│   │   └── state.go              # Agent 状态管理
│   │
│   ├── skill/                    # Skill 系统
│   │   ├── skill.go              # Skill 定义
│   │   ├── manifest.go           # Skill Manifest 解析
│   │   ├── loader.go             # Skill 加载器
│   │   ├── registry.go           # Skill 注册表
│   │   ├── router.go             # Skill 路由器
│   │   ├── executor.go           # Skill 执行器
│   │   ├── mcp_adapter.go        # MCP 工具适配
│   │   └── hot_reload.go         # [扩展] 热加载系统
│   │
│   ├── orchestrator/             # 多 Agent 编排
│   │   ├── orchestrator.go       # 编排器核心
│   │   ├── coordinator.go        # 协调器
│   │   ├── dispatcher.go         # 任务分发器
│   │   └── state.go              # 全局状态管理
│   │
│   ├── memory/                   # 记忆系统
│   │   ├── memory.go             # 记忆接口
│   │   ├── short_term.go         # 短期记忆
│   │   ├── long_term.go          # 长期记忆
│   │   └── compressor.go         # 记忆压缩
│   │
│   ├── planner/                  # 计划器
│   │   ├── planner.go            # 计划器接口
│   │   ├── step.go               # 执行步骤
│   │   └── dependency.go         # 依赖分析
│   │
│   ├── executor/                 # 执行器
│   │   ├── dag_executor.go       # DAG 执行器
│   │   ├── stream_executor.go    # [扩展] 流式执行器
│   │   ├── parallel_executor.go  # [扩展] 并行执行器
│   │   └── sandbox.go            # [扩展] 沙箱执行器
│   │
│   └── context/                  # Agent 上下文
│       ├── context.go            # 上下文定义
│       └── builder.go            # 上下文构建器
│
├── mcp/                          # [已有] MCP 协议实现
│   ├── client/                   # MCP 客户端
│   ├── config/                   # MCP 配置加载
│   ├── manager/                  # MCP 管理器
│   ├── protocol/                 # MCP 协议定义
│   ├── registry/                 # MCP 工具注册
│   ├── server/                   # MCP Server
│   │   └── echo/                 # Echo Server 示例
│   └── transport/                # 传输层 (WebSocket/SSE/Stdio)
│
├── toolkit/                      # [已有] 内置工具集
│   ├── interface.go              # 工具接口定义
│   ├── registry.go               # 工具注册表
│   ├── tools/                    # 内置工具实现
│   │   ├── bash.go               # Shell 命令执行
│   │   ├── grep.go               # 文件搜索
│   │   └── ...                   # 其他工具
│   ├── adapter.go                # 工具适配器
│   └── mcp_adapter.go            # MCP 工具适配
│
├── execution/                    # [已有] 执行上下文 (DAG Executor 基础)
│   ├── builder.go                # 执行流构建
│   └── context.go                # 执行上下文
│
├── analyzer/                     # [已有] 代码分析 (Workspace Scanner 基础)
│   ├── manager.go                # 分析管理器
│   ├── job.go                    # 分析任务
│   └── transform_analyzer.go     # 转换分析
│
├── transformer/                  # [已有] 请求转换器
│   ├── transformer.go            # 转换器接口
│   ├── registry.go               # 转换器注册
│   ├── model_aware_http.go       # 模型感知 HTTP 转换
│   └── ...                       # 其他转换器
│
├── llm/                          # [新增] 统一 LLM Runtime
│   ├── runtime.go                # LLM 运行时
│   ├── provider.go               # Provider 接口
│   ├── providers/                # 各厂商实现
│   │   ├── openai.go             # OpenAI
│   │   ├── anthropic.go          # Anthropic
│   │   ├── azure.go              # Azure
│   │   └── local.go              # 本地模型
│   ├── router.go                 # 模型路由
│   └── token_budget.go           # [扩展] Token 预算管理
│
├── embedding/                    # [新增] 语义搜索
│   ├── chunk.go                  # 代码块
│   ├── index.go                  # 向量索引
│   └── search.go                 # 语义搜索
│
├── workspace/                    # [新增] 工作空间
│   ├── scanner.go                # 仓库扫描 (复用 analyzer)
│   ├── symbols.go                # 符号索引
│   └── context.go                # 上下文构建
│
└── patch/                        # [新增] 代码补丁引擎
    ├── engine.go                 # 补丁引擎核心
    ├── parser.go                 # 补丁解析
    ├── applier.go                # 补丁应用
    ├── backup.go                 # 备份管理
    └── safety.go                 # 安全规则
```

#### 3.1.1 系统架构关系

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        AI Gateway 系统                                    │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  ┌─────────────────────────────┐    ┌─────────────────────────────┐    │
│  │       gateway/              │    │        runtime/             │    │
│  │    (API 代理网关)           │    │     (Agent 运行时)          │    │
│  │                             │    │                             │    │
│  │  ┌─────────────────────┐   │    │  ┌─────────────────────┐   │    │
│  │  │ handlers/           │   │    │  │ agent/              │   │    │
│  │  │  - pipeline/handler/handler.go │   │    │  │  - agent.go         │   │    │
│  │  │  - request_trans..  │   │    │  │  - loop.go          │   │    │
│  │  └─────────────────────┘   │    │  └─────────────────────┘   │    │
│  │                             │    │                             │    │
│  │  ┌─────────────────────┐   │    │  ┌─────────────────────┐   │    │
│  │  │ loadbalancer/       │   │    │  │ skill/              │   │    │
│  │  │  - manager.go       │   │    │  │  - registry.go      │   │    │
│  │  │  - selector.go      │   │    │  │  - router.go        │   │    │
│  │  └─────────────────────┘   │    │  └─────────────────────┘   │    │
│  │                             │    │                             │    │
│  │  ┌─────────────────────┐   │    │  ┌─────────────────────┐   │    │
│  │  │ middleware/         │   │    │  │ orchestrator/       │   │    │
│  │  │  - ratelimit.go     │   │    │  │  - coordinator.go   │   │    │
│  │  │  - tracing.go       │   │    │  │  - dispatcher.go    │   │    │
│  │  └─────────────────────┘   │    │  └─────────────────────┘   │    │
│  │                             │    │                             │    │
│  │  ┌─────────────────────┐   │    │  ┌─────────────────────┐   │    │
│  │  │ pipeline/           │   │    │  │ executor/           │   │    │
│  │  └─────────────────────┘   │    │  │  - dag_executor.go  │   │    │
│  │                             │    │  │  - sandbox.go       │   │    │
│  └─────────────────────────────┘    │  └─────────────────────┘   │    │
│                                      │                             │    │
│                                      │  ┌─────────────────────┐   │    │
│                                      │  │ memory/             │   │    │
│                                      │  └─────────────────────┘   │    │
│                                      └─────────────────────────────┘    │
│                                                                          │
├─────────────────────────────────────────────────────────────────────────┤
│                          共享基础设施层                                   │
│  ┌───────────┐ ┌───────────┐ ┌───────────┐ ┌───────────┐ ┌───────────┐ │
│  │   mcp/    │ │ toolkit/  │ │   llm/    │ │ embedding/│ │ workspace/│ │
│  │  [已有]   │ │  [已有]   │ │  [新增]   │ │  [新增]   │ │  [新增]   │ │
│  └───────────┘ └───────────┘ └───────────┘ └───────────┘ └───────────┘ │
│                                                                          │
│  ┌───────────┐ ┌───────────┐ ┌───────────┐ ┌───────────┐               │
│  │execution/ │ │ analyzer/ │ │transformer│ │  patch/   │               │
│  │  [已有]   │ │  [已有]   │ │  [已有]   │ │  [新增]   │               │
│  └───────────┘ └───────────┘ └───────────┘ └───────────┘               │
└─────────────────────────────────────────────────────────────────────────┘
```

#### 3.1.2 状态标注说明

|| 标注 | 含义 |
||------|------|
|| `[已有]` | 代码库中已存在，无需新建 |
|| `[新增]` | 需要从零开始实现 |
|| `[扩展]` | 基于已有模块扩展或在新增模块中增加功能 |

#### 3.1.3 gateway 与 runtime 的关系

| 维度 | gateway/ | runtime/ |
|------|----------|----------|
| **定位** | AI API 代理网关 | Agent 运行时系统 |
| **核心职责** | 请求转发、负载均衡、流量控制 | Agent 执行、Skill 路由、任务编排 |
| **主要用户** | 上游 AI 服务调用方 | Agent 开发者/终端用户 |
| **入口** | HTTP API | Agent API / Skill API |
| **依赖** | transformer, loadbalancer | mcp, toolkit, llm, embedding |

**协作场景**：runtime 可通过 gateway 调用上游 AI 服务，实现 Agent 能力的统一出口。

#### 3.1.4 共享类型模块 (types/)

为避免循环引用，所有跨模块共享的类型定义统一放在 `runtime/types/` 中。

**types/ 目录结构**：

| 文件 | 内容 | 使用模块 |
|------|------|----------|
| `observation.go` | Observation 执行观察记录 | skill, memory, agent, orchestrator |
| `request.go` | Request, Result 请求响应类型 | skill, agent, executor |
| `agent.go` | AgentConfig, AgentState, AgentResult | agent, orchestrator |
| `task.go` | Task, TaskStatus, TaskDependency | orchestrator, planner, executor |
| `skill.go` | SkillMeta, ToolRef 工具引用 | skill, agent, orchestrator |
| `execution.go` | ExecutionRecord, ExecutionStatus | executor, agent, memory |
| `message.go` | Message, ToolCall, ToolDefinition | agent, llm, executor |
| `errors.go` | RuntimeError, ErrorCode | 所有模块 |

**关键类型定义**：

- `Observation` - 执行观察记录（被 skill, memory, orchestrator 共享）
- `Request/Result` - 请求响应类型（被 skill, agent 共享）
- `AgentConfig/AgentResult` - Agent 配置与结果（被 agent, orchestrator 共享）
- `Task/TaskStatus` - 任务定义（被 orchestrator, planner 共享）
- `Message/ToolCall` - 消息与工具调用（被 agent, executor 共享）
- `RuntimeError` - 统一错误类型（被所有模块共享）

#### 3.1.5 模块依赖关系

```
                    ┌─────────────────┐
                    │    runtime/     │
                    │     types/      │  ← 核心类型层 (无外部依赖)
                    └────────┬────────┘
                             │
         ┌───────────────────┼───────────────────┐
         │                   │                   │
         ▼                   ▼                   ▼
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   agent/    │     │   skill/    │     │orchestrator/│
│ 依赖 types  │     │ 依赖 types  │     │ 依赖 types  │
└──────┬──────┘     └──────┬──────┘     └──────┬──────┘
       │                   │                   │
       ▼                   ▼                   ▼
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│  memory/    │     │  executor/  │     │  planner/   │
│ 依赖 types  │     │ 依赖 types  │     │ 依赖 types  │
└─────────────┘     └─────────────┘     └─────────────┘
```

**依赖规则**：
- `types/` 不依赖任何 runtime 内部包，只依赖标准库
- 所有 runtime 子包都可以依赖 `types/`
- 子包之间通过 `types/` 共享类型，避免直接相互引用

### 3.2 Skill 定义

```go
// internal/runtime/skill/skill.go

package skill

import (
    "context"
    "github.com/ai-gateway/gateway/internal/runtime/types"
)

// Skill 技能定义
type Skill struct {
    // 基本信息
    Name        string `yaml:"name" json:"name"`
    Description string `yaml:"description" json:"description"`
    Version     string `yaml:"version" json:"version"`

    // 触发规则
    Triggers []Trigger `yaml:"triggers" json:"triggers"`

    // 工具列表 (引用 MCP tools)
    Tools []string `yaml:"tools" json:"tools"`

    // Prompt 模板
    SystemPrompt string `yaml:"systemPrompt" json:"systemPrompt"`
    UserPrompt   string `yaml:"userPrompt" json:"userPrompt"`

    // 工作流定义 (可选)
    Workflow *Workflow `yaml:"workflow,omitempty" json:"workflow,omitempty"`

    // 上下文注入
    Context ContextConfig `yaml:"context" json:"context"`

    // 权限要求
    Permissions []string `yaml:"permissions" json:"permissions"`

    // 自定义处理器 (用于内置 Skills)
    Handler SkillHandler `yaml:"-" json:"-"`
}

// Trigger 触发规则
type Trigger struct {
    Type    string   `yaml:"type" json:"type"`       // keyword | pattern | embedding
    Values  []string `yaml:"values" json:"values"`   // 匹配值
    Weight  float64  `yaml:"weight" json:"weight"`   // 权重
}

// Workflow 工作流定义
type Workflow struct {
    Steps []WorkflowStep `yaml:"steps" json:"steps"`
}

// WorkflowStep 工作流步骤
type WorkflowStep struct {
    ID        string                 `yaml:"id" json:"id"`
    Name      string                 `yaml:"name" json:"name"`
    Tool      string                 `yaml:"tool" json:"tool"`
    Args      map[string]interface{} `yaml:"args" json:"args"`
    DependsOn []string               `yaml:"dependsOn" json:"dependsOn"`
    Condition string                 `yaml:"condition,omitempty" json:"condition,omitempty"`
}

// ContextConfig 上下文配置
type ContextConfig struct {
    Files       []string `yaml:"files" json:"files"`
    Environment []string `yaml:"environment" json:"environment"`
    Symbols     []string `yaml:"symbols" json:"symbols"`
}

// SkillHandler 技能处理器接口
type SkillHandler interface {
    Execute(ctx context.Context, req *types.Request) (*types.Result, error)
}
```

### 3.3 Skill Manifest 格式

```yaml
# skills/git/skill.yaml
name: git
description: Git repository operations
version: 1.0.0

triggers:
  - type: keyword
    values: [git, commit, branch, diff, merge, rebase, checkout]
    weight: 1.0
  - type: pattern
    values: ["commit .* changes", "create (a )?branch", "show diff"]
    weight: 0.8

tools:
  - toolkit_run_command  # 引用 MCP tool

systemPrompt: |
  You are a Git expert assistant. You help users with Git operations.
  Always check the current git status before making changes.
  Provide clear explanations of what each command does.

context:
  files:
    - .git/config
    - .gitignore
  environment:
    - GIT_AUTHOR_NAME
    - GIT_AUTHOR_EMAIL

permissions:
  - filesystem:read
  - shell:git

# 可选: 工作流定义
workflow:
  steps:
    - id: check_status
      name: Check Git Status
      tool: toolkit_run_command
      args:
        command: git status --porcelain

    - id: show_diff
      name: Show Changes
      tool: toolkit_run_command
      args:
        command: git diff
      dependsOn: [check_status]

    - id: commit
      name: Create Commit
      tool: toolkit_run_command
      args:
        command: git commit -m "{{.CommitMessage}}"
      dependsOn: [show_diff]
```

### 3.4 Skill Registry

```go
// internal/runtime/skill/registry.go

package skill

import (
    "sync"
)

// Registry Skill 注册表
type Registry struct {
    mu     sync.RWMutex
    skills map[string]*Skill

    // 按触发类型索引
    keywordIndex  map[string][]*Skill
    patternIndex  map[string][]*Skill

    // MCP Manager 引用
    mcpManager MCPManager
}

// MCPManager MCP 管理器接口
type MCPManager interface {
    FindTool(toolName string) (ToolInfo, error)
    CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (interface{}, error)
    ListTools() []ToolInfo
}

// NewRegistry 创建注册表
func NewRegistry(mcpManager MCPManager) *Registry {
    return &Registry{
        skills:       make(map[string]*Skill),
        keywordIndex: make(map[string][]*Skill),
        patternIndex: make(map[string][]*Skill),
        mcpManager:   mcpManager,
    }
}

// Register 注册 Skill
func (r *Registry) Register(s *Skill) error {
    r.mu.Lock()
    defer r.mu.Unlock()

    // 验证 Skill
    if err := r.validate(s); err != nil {
        return err
    }

    // 存储 Skill
    r.skills[s.Name] = s

    // 构建索引
    r.buildIndex(s)

    return nil
}

// Unregister 注销 Skill
func (r *Registry) Unregister(name string) {
    r.mu.Lock()
    defer r.mu.Unlock()

    if s, ok := r.skills[name]; ok {
        r.removeFromIndex(s)
        delete(r.skills, name)
    }
}

// Get 获取 Skill
func (r *Registry) Get(name string) (*Skill, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()

    s, ok := r.skills[name]
    return s, ok
}

// List 列出所有 Skills
func (r *Registry) List() []*Skill {
    r.mu.RLock()
    defer r.mu.RUnlock()

    skills := make([]*Skill, 0, len(r.skills))
    for _, s := range r.skills {
        skills = append(skills, s)
    }
    return skills
}

// validate 验证 Skill
func (r *Registry) validate(s *Skill) error {
    // 验证工具是否存在
    for _, toolName := range s.Tools {
        if _, err := r.mcpManager.FindTool(toolName); err != nil {
            return fmt.Errorf("tool not found: %s", toolName)
        }
    }
    return nil
}

// buildIndex 构建索引
func (r *Registry) buildIndex(s *Skill) {
    for _, trigger := range s.Triggers {
        switch trigger.Type {
        case "keyword":
            for _, kw := range trigger.Values {
                r.keywordIndex[strings.ToLower(kw)] = append(
                    r.keywordIndex[strings.ToLower(kw)],
                    s,
                )
            }
        case "pattern":
            for _, pattern := range trigger.Values {
                r.patternIndex[pattern] = append(
                    r.patternIndex[pattern],
                    s,
                )
            }
        }
    }
}
```

### 3.5 Skill Router

```go
// internal/runtime/skill/router.go

package skill

import (
    "context"
    "regexp"
    "strings"
)

// Router Skill 路由器
type Router struct {
    registry *Registry

    // 可选: Embedding Router
    embeddingRouter *EmbeddingRouter
}

// RouteResult 路由结果
type RouteResult struct {
    Skill    *Skill
    Score    float64
    MatchedBy string
}

// Route 路由用户请求
func (r *Router) Route(ctx context.Context, prompt string) []*RouteResult {
    var results []*RouteResult
    promptLower := strings.ToLower(prompt)

    // 1. 关键词匹配
    for keyword, skills := range r.registry.keywordIndex {
        if strings.Contains(promptLower, keyword) {
            for _, s := range skills {
                results = append(results, &RouteResult{
                    Skill:    s,
                    Score:    1.0,
                    MatchedBy: "keyword:" + keyword,
                })
            }
        }
    }

    // 2. 正则模式匹配
    for pattern, skills := range r.registry.patternIndex {
        matched, _ := regexp.MatchString(pattern, prompt)
        if matched {
            for _, s := range skills {
                results = append(results, &RouteResult{
                    Skill:    s,
                    Score:    0.8,
                    MatchedBy: "pattern:" + pattern,
                })
            }
        }
    }

    // 3. Embedding 匹配 (如果启用)
    if r.embeddingRouter != nil {
        embResults := r.embeddingRouter.Route(ctx, prompt)
        results = append(results, embResults...)
    }

    // 去重并排序
    return r.dedupAndSort(results)
}

// EmbeddingRouter Embedding 路由器
type EmbeddingRouter struct {
    embedder Embedder
    index    *VectorIndex
}

type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
}

func (e *EmbeddingRouter) Route(ctx context.Context, prompt string) []*RouteResult {
    // 生成 prompt embedding
    emb, err := e.embedder.Embed(ctx, prompt)
    if err != nil {
        return nil
    }

    // 搜索相似 skills
    matches := e.index.Search(emb, 5)

    var results []*RouteResult
    for _, m := range matches {
        results = append(results, &RouteResult{
            Skill:    m.Skill,
            Score:    m.Score,
            MatchedBy: "embedding",
        })
    }

    return results
}
```

### 3.6 Skill Executor

```go
// internal/runtime/skill/executor.go

package skill

import (
    "context"
    "fmt"
    "sync"
)

// Executor Skill 执行器
type Executor struct {
    registry   *Registry
    mcpManager MCPManager
}

// ExecuteResult 执行结果
type ExecuteResult struct {
    SkillName   string
    Success     bool
    Output      string
    Observations []Observation
    Error       string
}

// Execute 执行 Skill
func (e *Executor) Execute(ctx context.Context, skill *Skill, req *Request) (*ExecuteResult, error) {
    result := &ExecuteResult{
        SkillName: skill.Name,
    }

    // 1. 如果有自定义处理器，直接执行
    if skill.Handler != nil {
        res, err := skill.Handler.Execute(ctx, req)
        if err != nil {
            result.Error = err.Error()
            return result, nil
        }
        result.Success = res.Success
        result.Output = res.Output
        return result, nil
    }

    // 2. 如果有工作流，执行工作流
    if skill.Workflow != nil {
        obs, output, err := e.executeWorkflow(ctx, skill, req)
        result.Observations = obs
        if err != nil {
            result.Error = err.Error()
            return result, nil
        }
        result.Success = true
        result.Output = output
        return result, nil
    }

    // 3. 默认: 构建 prompt 并调用 LLM
    return e.executeWithLLM(ctx, skill, req)
}

// executeWorkflow 执行工作流
func (e *Executor) executeWorkflow(ctx context.Context, skill *Skill, req *Request) ([]Observation, string, error) {
    var observations []Observation
    results := make(map[string]interface{})

    // 构建依赖图
    dag := e.buildDAG(skill.Workflow)

    // 拓扑排序
    order := e.topologicalSort(dag)

    // 按顺序执行
    for _, stepID := range order {
        step := e.findStep(skill.Workflow, stepID)

        // 检查依赖是否满足
        if !e.checkDependencies(step, results) {
            continue
        }

        // 准备参数
        args := e.prepareArgs(step.Args, results, req)

        // 找到对应的 MCP tool
        toolInfo, err := e.mcpManager.FindTool(step.Tool)
        if err != nil {
            return observations, "", fmt.Errorf("tool not found: %s", step.Tool)
        }

        // 调用 tool
        output, err := e.mcpManager.CallTool(
            ctx,
            toolInfo.MCPName,
            step.Tool,
            args,
        )

        obs := Observation{
            Step:    step.ID,
            Tool:    step.Tool,
            Input:   args,
            Output:  output,
            Success: err == nil,
        }
        if err != nil {
            obs.Error = err.Error()
        }
        observations = append(observations, obs)

        results[step.ID] = output
    }

    // 生成最终输出
    output := e.formatOutput(results)
    return observations, output, nil
}

// buildDAG 构建依赖图
func (e *Executor) buildDAG(workflow *Workflow) *DAG {
    dag := &DAG{
        Nodes: make(map[string]*Node),
    }

    for _, step := range workflow.Steps {
        dag.Nodes[step.ID] = &Node{
            ID:        step.ID,
            Step:      step,
            Deps:      step.DependsOn,
            Dependents: []string{},
        }
    }

    // 建立反向依赖
    for _, step := range workflow.Steps {
        for _, dep := range step.DependsOn {
            if node, ok := dag.Nodes[dep]; ok {
                node.Dependents = append(node.Dependents, step.ID)
            }
        }
    }

    return dag
}

// topologicalSort 拓扑排序
func (e *Executor) topologicalSort(dag *DAG) []string {
    var order []string
    visited := make(map[string]bool)

    var visit func(string)
    visit = func(id string) {
        if visited[id] {
            return
        }
        visited[id] = true

        if node, ok := dag.Nodes[id]; ok {
            for _, dep := range node.Deps {
                visit(dep)
            }
        }
        order = append(order, id)
    }

    for id := range dag.Nodes {
        visit(id)
    }

    return order
}

// DAG 依赖图
type DAG struct {
    Nodes map[string]*Node
}

type Node struct {
    ID         string
    Step       *WorkflowStep
    Deps       []string
    Dependents []string
}
```

---

## 四、Agent Runtime 设计

### 4.1 Agent Core

```go
// internal/runtime/agent/agent.go

package agent

import (
    "context"
    "sync"
)

// Agent AI Agent
type Agent struct {
    config      *Config
    skillRouter *skill.Router
    skillExec   *skill.Executor
    mcpManager  MCPManager
    memory      *Memory
    planner     *Planner

    mu          sync.RWMutex
    running     bool
}

// Config Agent 配置
type Config struct {
    MaxSteps       int                    `yaml:"maxSteps"`
    DefaultModel   string                 `yaml:"defaultModel"`
    SystemPrompt   string                 `yaml:"systemPrompt"`
    EnableMemory   bool                   `yaml:"enableMemory"`
    EnablePlanning bool                   `yaml:"enablePlanning"`
}

// NewAgent 创建 Agent
func NewAgent(cfg *Config, mcpManager MCPManager) *Agent {
    registry := skill.NewRegistry(mcpManager)
    router := skill.NewRouter(registry)
    executor := skill.NewExecutor(registry, mcpManager)

    return &Agent{
        config:      cfg,
        skillRouter: router,
        skillExec:   executor,
        mcpManager:  mcpManager,
        memory:      NewMemory(),
        planner:     NewPlanner(mcpManager),
    }
}

// LoadSkills 加载 Skills
func (a *Agent) LoadSkills(dir string) error {
    loader := skill.NewLoader(a.mcpManager)
    skills, err := loader.Load(dir)
    if err != nil {
        return err
    }

    for _, s := range skills {
        if err := a.skillRouter.Registry().Register(s); err != nil {
            return err
        }
    }

    return nil
}
```

### 4.2 Agent Loop (ReAct)

```go
// internal/runtime/agent/loop.go

package agent

import (
    "context"
    "fmt"
    
    "github.com/ai-gateway/gateway/internal/runtime/types"
)

// Run 执行 Agent
func (a *Agent) Run(ctx context.Context, prompt string) (*types.Result, error) {
    a.mu.Lock()
    a.running = true
    a.mu.Unlock()

    defer func() {
        a.mu.Lock()
        a.running = false
        a.mu.Unlock()
    }()

    // 1. 路由到合适的 Skill
    routes := a.skillRouter.Route(ctx, prompt)
    if len(routes) == 0 {
        return a.runDefaultLoop(ctx, prompt)
    }

    // 选择最佳匹配
    best := routes[0]

    // 2. 构建请求
    req := &types.Request{
        Prompt:  prompt,
        Context: a.buildContext(best.Skill),
        History: a.memory.Recent(5),
    }

    // 3. 执行 Skill
    result, err := a.skillExec.Execute(ctx, best.Skill, req)
    if err != nil {
        return nil, err
    }

    // 4. 记录到记忆
    if a.config.EnableMemory {
        for _, obs := range result.Observations {
            a.memory.Add(obs)
        }
    }

    return &types.Result{
        Success:      result.Success,
        Output:       result.Output,
        Skill:        result.SkillName,
        Observations: result.Observations,
    }, nil
}

// runDefaultLoop 默认 ReAct 循环
func (a *Agent) runDefaultLoop(ctx context.Context, prompt string) (*types.Result, error) {
    history := []types.Message{
        {Role: "user", Content: prompt},
    }

    var observations []types.Observation

    for step := 0; step < a.config.MaxSteps; step++ {
        // 1. LLM 推理
        response, err := a.llmCall(ctx, history)
        if err != nil {
            return nil, err
        }

        // 2. 检查是否有 Tool Call
        if len(response.ToolCalls) == 0 {
            return &types.Result{
                Success:      true,
                Output:       response.Content,
                Observations: observations,
            }, nil
        }

        // 3. 执行 Tool Calls (支持并行)
        results := a.executeToolCalls(ctx, response.ToolCalls)

        // 4. 添加观察
        for i, tc := range response.ToolCalls {
            obs := types.Observation{
                Step:    fmt.Sprintf("step_%d", step),
                Tool:    tc.Name,
                Input:   tc.Args,
                Output:  results[i],
                Success: results[i] != nil,
            }
            observations = append(observations, obs)

            if a.config.EnableMemory {
                a.memory.Add(obs)
            }
        }

        // 5. 更新历史
        history = append(history, types.Message{
            Role:      "assistant",
            Content:   response.Content,
            ToolCalls: response.ToolCalls,
        })

        for i, result := range results {
            history = append(history, types.Message{
                Role:       "tool",
                Content:    fmt.Sprintf("%v", result),
                ToolCallID: response.ToolCalls[i].ID,
            })
        }
    }

    return &types.Result{
        Success:      false,
        Output:       "Max steps reached",
        Observations: observations,
    }, nil
}
```

---

### 4.3 Planner

```go
// internal/runtime/planner/planner.go

package planner

import (
    "context"
    "encoding/json"
)

// Planner 计划器
type Planner struct {
    mcpManager MCPManager
}

// Plan 执行计划
type Plan struct {
    Goal  string
    Steps []PlanStep
}

// PlanStep 计划步骤
type PlanStep struct {
    ID          string
    Description string
    Tool        string
    Args        map[string]interface{}
    DependsOn   []string
}

// CreatePlan 创建计划
func (p *Planner) CreatePlan(ctx context.Context, prompt string, tools []ToolInfo) (*Plan, error) {
    // 构建 prompt
    systemPrompt := p.buildPlanningPrompt(tools)

    // 调用 LLM
    response, err := p.llmCall(ctx, systemPrompt, prompt)
    if err != nil {
        return nil, err
    }

    // 解析计划
    var plan Plan
    if err := json.Unmarshal([]byte(response), &plan); err != nil {
        return nil, err
    }

    return &plan, nil
}

// buildPlanningPrompt 构建计划 prompt
func (p *Planner) buildPlanningPrompt(tools []ToolInfo) string {
    toolDescriptions := make([]string, len(tools))
    for i, t := range tools {
        toolDescriptions[i] = fmt.Sprintf("- %s: %s", t.Name, t.Description)
    }

    return fmt.Sprintf(`
You are an AI planning assistant. Given a user request and available tools,
create an execution plan in JSON format.

Available tools:
%s

Respond with a JSON plan:
{
  "goal": "description of the goal",
  "steps": [
    {
      "id": "step1",
      "description": "what this step does",
      "tool": "tool_name",
      "args": {"arg1": "value1"},
      "dependsOn": []
    }
  ]
}

Rules:
1. Each step should be atomic and achievable with one tool call
2. Use dependsOn to specify dependencies between steps
3. Steps with no dependencies can be executed in parallel
`, strings.Join(toolDescriptions, "\n"))
}
```

### 4.4 Memory

```go
// internal/runtime/memory/memory.go

package memory

import (
    "sync"
    "time"
)

// Memory 记忆系统
type Memory struct {
    mu          sync.RWMutex
    observations []Observation
    maxSize     int
}

// NewMemory 创建记忆
func NewMemory() *Memory {
    return &Memory{
        observations: make([]Observation, 0),
        maxSize:     100,
    }
}

// Add 添加观察
func (m *Memory) Add(obs Observation) {
    m.mu.Lock()
    defer m.mu.Unlock()

    obs.Timestamp = time.Now()

    m.observations = append(m.observations, obs)

    // 超过最大容量，删除最旧的
    if len(m.observations) > m.maxSize {
        m.observations = m.observations[1:]
    }
}

// Recent 获取最近的观察
func (m *Memory) Recent(n int) []Observation {
    m.mu.RLock()
    defer m.mu.RUnlock()

    if n > len(m.observations) {
        n = len(m.observations)
    }

    start := len(m.observations) - n
    result := make([]Observation, n)
    copy(result, m.observations[start:])

    return result
}

// Clear 清空记忆
func (m *Memory) Clear() {
    m.mu.Lock()
    defer m.mu.Unlock()

    m.observations = make([]Observation, 0)
}

// Search 搜索记忆 (简单实现)
func (m *Memory) Search(query string) []Observation {
    m.mu.RLock()
    defer m.mu.RUnlock()

    var results []Observation
    queryLower := strings.ToLower(query)

    for _, obs := range m.observations {
        // 搜索工具名和输出
        if strings.Contains(strings.ToLower(obs.Tool), queryLower) {
            results = append(results, obs)
            continue
        }

        if output, ok := obs.Output.(string); ok {
            if strings.Contains(strings.ToLower(output), queryLower) {
                results = append(results, obs)
            }
        }
    }

    return results
}
```

---

## 五、Workspace & Embedding

### 5.1 Workspace Scanner

```go
// internal/workspace/scanner.go

package workspace

import (
    "os"
    "path/filepath"
    "strings"
)

// Workspace 工作空间
type Workspace struct {
    Root      string
    Files     map[string]*FileInfo
    Languages map[string]int
}

// FileInfo 文件信息
type FileInfo struct {
    Path     string
    Language string
    Size     int64
    Symbols  []Symbol
}

// Symbol 符号
type Symbol struct {
    Name string
    Kind string // function, type, variable, etc.
    File string
    Line int
}

// Scan 扫描工作空间
func Scan(root string) (*Workspace, error) {
    ws := &Workspace{
        Root:      root,
        Files:     make(map[string]*FileInfo),
        Languages: make(map[string]int),
    }

    err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return nil
        }

        if info.IsDir() {
            // 跳过隐藏目录和常见排除目录
            if strings.HasPrefix(info.Name(), ".") ||
               info.Name() == "node_modules" ||
               info.Name() == "vendor" {
                return filepath.SkipDir
            }
            return nil
        }

        // 检测语言
        lang := detectLanguage(path)
        if lang == "" {
            return nil
        }

        ws.Files[path] = &FileInfo{
            Path:     path,
            Language: lang,
            Size:     info.Size(),
        }
        ws.Languages[lang]++

        return nil
    })

    return ws, err
}

// detectLanguage 检测语言
func detectLanguage(path string) string {
    ext := strings.ToLower(filepath.Ext(path))
    switch ext {
    case ".go":
        return "go"
    case ".js", ".jsx":
        return "javascript"
    case ".ts", ".tsx":
        return "typescript"
    case ".py":
        return "python"
    case ".java":
        return "java"
    case ".rs":
        return "rust"
    case ".c", ".h":
        return "c"
    case ".cpp", ".hpp":
        return "cpp"
    case ".md":
        return "markdown"
    case ".yaml", ".yml":
        return "yaml"
    case ".json":
        return "json"
    default:
        return ""
    }
}
```

### 5.2 Embedding Index

```go
// internal/embedding/index.go

package embedding

import (
    "math"
    "sort"
)

// CodeChunk 代码块
type CodeChunk struct {
    ID        string
    File      string
    StartLine int
    EndLine   int
    Content   string
    Embedding []float32
    Metadata  map[string]string
}

// VectorIndex 向量索引
type VectorIndex struct {
    chunks []*CodeChunk
}

// NewVectorIndex 创建索引
func NewVectorIndex() *VectorIndex {
    return &VectorIndex{
        chunks: make([]*CodeChunk, 0),
    }
}

// Add 添加代码块
func (i *VectorIndex) Add(chunk *CodeChunk) {
    i.chunks = append(i.chunks, chunk)
}

// Search 搜索相似代码
func (i *VectorIndex) Search(query []float32, topK int) []*SearchResult {
    var results []*SearchResult

    for _, chunk := range i.chunks {
        if len(chunk.Embedding) == 0 {
            continue
        }

        score := cosineSimilarity(query, chunk.Embedding)
        results = append(results, &SearchResult{
            Chunk: chunk,
            Score: score,
        })
    }

    // 按分数排序
    sort.Slice(results, func(i, j int) bool {
        return results[i].Score > results[j].Score
    })

    // 返回 topK
    if len(results) > topK {
        results = results[:topK]
    }

    return results
}

// SearchResult 搜索结果
type SearchResult struct {
    Chunk *CodeChunk
    Score float32
}

// cosineSimilarity 余弦相似度
func cosineSimilarity(a, b []float32) float32 {
    if len(a) != len(b) {
        return 0
    }

    var dot, magA, magB float32
    for i := range a {
        dot += a[i] * b[i]
        magA += a[i] * a[i]
        magB += b[i] * b[i]
    }

    if magA == 0 || magB == 0 {
        return 0
    }

    return dot / (float32(math.Sqrt(float64(magA))) * float32(math.Sqrt(float64(magB))))
}
```

---

## 六、集成方案

### 6.1 与现有 MCP 集成

```go
// internal/runtime/skill/mcp_adapter.go

package skill

import (
    "github.com/ai-gateway/gateway/internal/mcp/manager"
    "github.com/ai-gateway/gateway/internal/mcp/registry"
)

// MCPAdapter MCP 适配器
type MCPAdapter struct {
    manager manager.Manager
}

// NewMCPAdapter 创建适配器
func NewMCPAdapter(m manager.Manager) *MCPAdapter {
    return &MCPAdapter{manager: m}
}

// FindTool 查找工具
func (a *MCPAdapter) FindTool(toolName string) (ToolInfo, error) {
    info, err := a.manager.FindTool(toolName)
    if err != nil {
        return ToolInfo{}, err
    }

    return ToolInfo{
        Name:        info.Tool.Name,
        Description: info.Tool.Description,
        MCPName:     info.MCPName,
        InputSchema: info.Tool.InputSchema,
        Enabled:     info.Enabled,
    }, nil
}

// CallTool 调用工具
func (a *MCPAdapter) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
    result, err := a.manager.CallTool(ctx, mcpName, toolName, args)
    if err != nil {
        return nil, err
    }

    // 提取文本内容
    var output strings.Builder
    for _, content := range result.Content {
        if content.Type == "text" {
            output.WriteString(content.Text)
        }
    }

    return output.String(), nil
}

// ListTools 列出工具
func (a *MCPAdapter) ListTools() []ToolInfo {
    tools := a.manager.ListTools()
    result := make([]ToolInfo, len(tools))

    for i, t := range tools {
        result[i] = ToolInfo{
            Name:        t.Tool.Name,
            Description: t.Tool.Description,
            MCPName:     t.MCPName,
            InputSchema: t.Tool.InputSchema,
            Enabled:     t.Enabled,
        }
    }

    return result
}
```

### 6.2 API 扩展

```go
// internal/api/skill_routes.go

package api

import (
    "github.com/gin-gonic/gin"
)

// SkillHandler Skill API 处理器
type SkillHandler struct {
    agent *runtime.Agent
}

// RegisterRoutes 注册路由
func (h *SkillHandler) RegisterRoutes(r *gin.RouterGroup) {
    skills := r.Group("/skills")
    {
        skills.GET("", h.ListSkills)
        skills.GET("/:name", h.GetSkill)
        skills.POST("/:name/execute", h.ExecuteSkill)
    }

    agent := r.Group("/agent")
    {
        agent.POST("/run", h.RunAgent)
        agent.GET("/memory", h.GetMemory)
        agent.DELETE("/memory", h.ClearMemory)
    }
}

// ListSkills 列出 Skills
func (h *SkillHandler) ListSkills(c *gin.Context) {
    skills := h.agent.ListSkills()
    c.JSON(200, skills)
}

// ExecuteSkill 执行 Skill
func (h *SkillHandler) ExecuteSkill(c *gin.Context) {
    name := c.Param("name")

    var req struct {
        Prompt  string                 `json:"prompt"`
        Context map[string]interface{} `json:"context"`
    }

    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(400, gin.H{"error": err.Error()})
        return
    }

    skill, ok := h.agent.GetSkill(name)
    if !ok {
        c.JSON(404, gin.H{"error": "skill not found"})
        return
    }

    result, err := h.agent.ExecuteSkill(c.Request.Context(), skill, &types.Request{
        Prompt:  req.Prompt,
        Context: req.Context,
    })

    if err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    c.JSON(200, result)
}

// RunAgent 运行 Agent
func (h *SkillHandler) RunAgent(c *gin.Context) {
    var req struct {
        Prompt string `json:"prompt"`
    }

    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(400, gin.H{"error": err.Error()})
        return
    }

    result, err := h.agent.Run(c.Request.Context(), req.Prompt)
    if err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    c.JSON(200, result)
}
```

---

## 七、配置示例

### 7.1 Skills 配置目录

```
config/
├── mcp.yaml          # MCP 配置 (已有)
└── skills/           # Skills 配置目录
    ├── git/
    │   └── skill.yaml
    ├── test/
    │   └── skill.yaml
    ├── refactor/
    │   └── skill.yaml
    └── debug/
        └── skill.yaml
```

### 7.2 Agent 配置

```yaml
# config/agent.yaml
agent:
  maxSteps: 10
  defaultModel: "claude-3-5-sonnet"
  enableMemory: true
  enablePlanning: true
  systemPrompt: |
    You are an AI coding assistant integrated with the AI Gateway.
    You have access to various tools and skills to help with coding tasks.

  skills:
    dir: "./config/skills"
    autoLoad: true
    hotReload: true

  embedding:
    enabled: true
    provider: "openai"
    model: "text-embedding-3-small"

  workspace:
    root: "."
    exclude:
      - "node_modules"
      - "vendor"
      - ".git"
```

---

## 八、实现路线图

### Phase 1: 基础框架 (1-2 周)

- [x] Skill 定义和 Manifest 解析
- [x] Skill Registry 实现
- [x] Skill Loader 实现
- [x] 基础 Router (关键词匹配)
- [x] 共享类型模块定义
- [x] 统一错误类型定义

### Phase 2: 执行引擎 (2-3 周)

- [ ] Skill Executor 实现
- [ ] Workflow DAG 执行
- [ ] Agent Loop (ReAct)
- [ ] Memory 系统
- [ ] MCP 适配器实现
- [ ] 工具调用并行执行

### Phase 3: 智能增强 (2-3 周)

- [ ] Embedding Router
- [ ] Workspace Scanner
- [ ] 代码语义搜索
- [ ] Planner 实现
- [ ] Token 预算管理

### Phase 4: 集成优化 (1-2 周)

- [ ] API 扩展
- [ ] Chat/Session 层
- [ ] 配置系统
- [ ] 配置验证器
- [ ] 热加载支持
- [ ] 性能优化

### Phase 5: 高级特性 (2-3 周)

- [ ] Multi-Agent 协作系统
- [ ] Self-Reflection 机制
- [ ] Code Patch 引擎
- [ ] Sandbox 安全执行器
- [ ] Streaming Tool Calls

### Phase 6: 基础设施 (1-2 周)

- [ ] 认证授权模块
- [ ] 错误处理和重试策略
- [ ] 速率限制
- [ ] 使用量跟踪
- [ ] 可观测性 (Metrics/Tracing/Logging)

### Phase 7: LLM 集成 (1-2 周)

- [ ] LLM Provider 实现
- [ ] OpenAI Provider
- [ ] Anthropic Provider
- [ ] Tokenizer 实现
- [ ] Gateway Client 集成
- [ ] 模型路由器

### Phase 8: 数据持久化 (1 周)

- [ ] SessionStorage 实现
- [ ] SQLite 存储
- [ ] PostgreSQL 存储
- [ ] Redis 缓存
- [ ] 数据迁移脚本

### Phase 9: 测试和文档 (2 周)

- [ ] 单元测试框架
- [ ] Mock 对象
- [ ] 集成测试用例
- [ ] 性能测试
- [ ] API 文档
- [ ] 部署文档

### Phase 10: 运维支持 (1 周)

- [ ] Docker 镜像
- [ ] Docker Compose 配置
- [ ] Kubernetes 部署
- [ ] 监控仪表板
- [ ] 告警规则
- [ ] 日志聚合

### 里程碑

| 里程碑 | 目标 | 预计时间 |
|--------|------|----------|
| M1 | 基础框架完成 | Week 2 |
| M2 | Agent 基本功能可用 | Week 5 |
| M3 | 智能路由和搜索 | Week 8 |
| M4 | 完整功能交付 | Week 10 |
| M5 | 高级特性上线 | Week 13 |
| M6 | 生产环境部署 | Week 16 |

### 团队分工

| 角色 | 职责 | 人员 |
|------|------|------|
| 架构师 | 整体架构设计、技术选型 | - |
| 后端开发 | Agent/Skill/Executor 实现 | - |
| 前端开发 | Web 界面、管理后台 | - |
| 运维工程师 | 部署、监控、告警 | - |
| 测试工程师 | 测试用例、质量保证 | - |
| 文档工程师 | API 文档、用户手册 | - |

### 技术栈

| 类别 | 技术 |
|------|------|
| 语言 | Go 1.21+ |
| Web 框架 | Gin |
| 数据库 | PostgreSQL 16, SQLite |
| 缓存 | Redis 7 |
| 消息队列 | NATS / Redis Streams |
| 监控 | Prometheus, Grafana |
| 日志 | ELK Stack / Loki |
| 追踪 | OpenTelemetry |
| 容器 | Docker, Kubernetes |
| CI/CD | GitHub Actions / GitLab CI |

### 风险和缓解措施

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| API 配额/限流 | 高 | 中 | 实现多轮重试、本地缓存提供商 |
| 成本控制 | 高 | 中 | Token 预算管理、使用量跟踪 |
| 复杂性失控 | 中 | 低 | 模块化设计、清晰接口定义 |
| 安全问题 | 高 | 低 | Sandbox、权限控制、代码审计 |
| 性能问题 | 中 | 中 | 性能测试、缓存策略、优化热点代码 |

---

## 九、总结与建议

### Phase 1: 基础框架 (1-2 周)

- [ ] Skill 定义和 Manifest 解析
- [ ] Skill Registry 实现
- [ ] Skill Loader 实现
- [ ] 基础 Router (关键词匹配)

### Phase 2: 执行引擎 (2-3 周)

- [ ] Skill Executor 实现
- [ ] Workflow DAG 执行
- [ ] Agent Loop (ReAct)
- [ ] Memory 系统

### Phase 3: 智能增强 (2-3 周)

- [ ] Embedding Router
- [ ] Workspace Scanner
- [ ] 代码语义搜索
- [ ] Planner 实现

### Phase 4: 集成优化 (1-2 周)

- [ ] API 扩展
- [ ] 配置系统
- [ ] 热加载支持
- [ ] 性能优化

---

## 九、总结

本设计方案基于已有的 MCP 实现构建 Skills Runtime，核心设计包括：

1. **Skill 系统**: 可扩展的技能定义、加载、路由和执行
2. **Agent Runtime**: ReAct 循环、记忆系统、计划器
3. **智能增强**: 语义搜索、工作空间理解
4. **与 MCP 集成**: 复用现有 MCP 工具作为 Skill 的能力来源

该设计遵循模块化、可扩展的原则，可以逐步实现和完善。

---

## 十、高级特性

### 10.1 Multi-Agent 协作系统

Multi-Agent 协作是 Claude Code/Cursor 的核心能力之一，支持多个专业 Agent 协同完成复杂任务。

```go
// internal/runtime/orchestrator/orchestrator.go

package orchestrator

import (
    "context"
    "sync"
)

// Orchestrator 多 Agent 编排器
type Orchestrator struct {
    agents      map[string]*Agent
    coordinator *Coordinator
    taskQueue   chan *Task
    results     chan *AgentResult
}

// Coordinator 协调器
type Coordinator struct {
    strategy    CoordinationStrategy
    state       *GlobalState
    dispatcher  *TaskDispatcher
}

// CoordinationStrategy 协调策略
type CoordinationStrategy string

const (
    StrategySequential   CoordinationStrategy = "sequential"    // 顺序执行
    StrategyParallel     CoordinationStrategy = "parallel"      // 并行执行
    StrategyHierarchical CoordinationStrategy = "hierarchical"  // 层级执行
    StrategyConsensus    CoordinationStrategy = "consensus"     // 共识决策
)

// GlobalState 全局状态
type GlobalState struct {
    mu            sync.RWMutex
    sharedContext map[string]interface{}
    taskProgress  map[string]*Progress
    agentOutputs  map[string][]Observation
}

// Task 任务定义
type Task struct {
    ID          string
    Type        string
    Description string
    Priority    int
    Dependencies []string
    AssignedTo  string  // 指定的 Agent
    Context     map[string]interface{}
}

// AgentResult Agent 结果
type AgentResult struct {
    TaskID    string
    AgentName string
    Output    string
    Success   bool
    Error     error
    Artifacts map[string]interface{}
}

// Dispatch 分发任务
func (o *Orchestrator) Dispatch(ctx context.Context, tasks []*Task) ([]*AgentResult, error) {
    // 1. 构建任务依赖图
    graph := o.buildTaskGraph(tasks)

    // 2. 拓扑排序获取执行顺序
    order := graph.TopologicalSort()

    // 3. 识别可并行执行的任务
    batches := o.identifyParallelBatches(order, graph)

    var allResults []*AgentResult

    // 4. 按批次执行
    for _, batch := range batches {
        results := o.executeBatch(ctx, batch)
        allResults = append(allResults, results...)
    }

    return allResults, nil
}

// executeBatch 并行执行一批任务
func (o *Orchestrator) executeBatch(ctx context.Context, tasks []*Task) []*AgentResult {
    var wg sync.WaitGroup
    results := make([]*AgentResult, len(tasks))

    for i, task := range tasks {
        wg.Add(1)
        go func(idx int, t *Task) {
            defer wg.Done()

            agent := o.selectAgent(t)
            result, err := agent.Run(ctx, t.Description)
            results[idx] = &AgentResult{
                TaskID:    t.ID,
                AgentName: agent.Name,
                Output:    result.Output,
                Success:   err == nil,
                Error:     err,
            }
        }(i, task)
    }

    wg.Wait()
    return results
}

// selectAgent 选择合适的 Agent
func (o *Orchestrator) selectAgent(task *Task) *Agent {
    if task.AssignedTo != "" {
        return o.agents[task.AssignedTo]
    }

    // 基于任务类型和能力匹配
    var bestAgent *Agent
    bestScore := 0.0

    for _, agent := range o.agents {
        score := o.computeAgentScore(agent, task)
        if score > bestScore {
            bestScore = score
            bestAgent = agent
        }
    }

    return bestAgent
}
```

#### Agent 角色定义

```yaml
# config/agents.yaml
agents:
  - name: code-analyzer
    role: |
      You are a code analysis specialist. Analyze code structure,
      identify issues, and provide detailed reports.
    capabilities:
      - code-analysis
      - ast-parsing
      - symbol-resolution
    tools:
      - toolkit_read_file
      - toolkit_grep
      - toolkit_list_files

  - name: code-writer
    role: |
      You are a code generation specialist. Write clean, efficient,
      well-documented code following best practices.
    capabilities:
      - code-generation
      - refactoring
      - test-writing
    tools:
      - toolkit_write_file
      - toolkit_edit_file

  - name: test-runner
    role: |
      You are a testing specialist. Run tests, analyze failures,
      and suggest fixes.
    capabilities:
      - test-execution
      - coverage-analysis
      - debugging
    tools:
      - toolkit_run_command
      - toolkit_read_file

  - name: reviewer
    role: |
      You are a code reviewer. Review code for quality, security,
      and performance issues.
    capabilities:
      - code-review
      - security-analysis
      - performance-review
    tools:
      - toolkit_read_file
      - toolkit_grep
```

#### 协作模式

```go
// internal/runtime/orchestrator/collaboration.go

package orchestrator

// CollaborationMode 协作模式
type CollaborationMode string

const (
    ModeReview   CollaborationMode = "review"    // 审阅模式: writer -> reviewer
    ModePair     CollaborationMode = "pair"      // 配对模式: 两个 Agent 协作
    ModeSwarm    CollaborationMode = "swarm"     // 蜂群模式: 多 Agent 并行
    ModePipeline CollaborationMode = "pipeline"  // 流水线模式: 顺序处理
    ModeDebate   CollaborationMode = "debate"    // 辩论模式: 多 Agent 共识
)

// CollaborationSession 协作会话
type CollaborationSession struct {
    ID        string
    Mode      CollaborationMode
    Agents    []*Agent
    State     *SessionState
    History   []*Message
    StartTime time.Time
}

// SessionState 会话状态
type SessionState struct {
    CurrentPhase string
    Turn         int
    Consensus    bool
    Votes        map[string]Vote
}

// RunCollaboration 运行协作
func (o *Orchestrator) RunCollaboration(ctx context.Context, mode CollaborationMode, task *Task) (*Result, error) {
    session := &CollaborationSession{
        ID:        generateID(),
        Mode:      mode,
        State:     &SessionState{Turn: 0, Votes: make(map[string]Vote)},
        StartTime: time.Now(),
    }

    switch mode {
    case ModeReview:
        return o.runReviewMode(ctx, session, task)
    case ModePair:
        return o.runPairMode(ctx, session, task)
    case ModeSwarm:
        return o.runSwarmMode(ctx, session, task)
    case ModePipeline:
        return o.runPipelineMode(ctx, session, task)
    case ModeDebate:
        return o.runDebateMode(ctx, session, task)
    default:
        return nil, fmt.Errorf("unknown collaboration mode: %s", mode)
    }
}

// runReviewMode 审阅模式: writer 编写 -> reviewer 审阅 -> 迭代修复
func (o *Orchestrator) runReviewMode(ctx context.Context, session *CollaborationSession, task *Task) (*Result, error) {
    writer := o.agents["code-writer"]
    reviewer := o.agents["reviewer"]

    maxIterations := 3
    var lastResult *Result

    for i := 0; i < maxIterations; i++ {
        // Writer 编写代码
        writeResult, err := writer.Run(ctx, task.Description)
        if err != nil {
            return nil, err
        }

        // Reviewer 审阅
        reviewTask := &Task{
            Description: fmt.Sprintf("Review the following code:\n%s", writeResult.Output),
        }
        reviewResult, err := reviewer.Run(ctx, reviewTask.Description)
        if err != nil {
            return nil, err
        }

        // 检查是否通过审阅
        if o.isApproved(reviewResult.Output) {
            return writeResult, nil
        }

        // 更新任务描述，包含审阅意见
        task.Description = fmt.Sprintf(
            "Fix the following issues:\n%s\n\nOriginal code:\n%s",
            reviewResult.Output,
            writeResult.Output,
        )
        lastResult = writeResult
    }

    return lastResult, nil
}
```

---

### 10.2 Skill 热加载系统

支持运行时动态加载、更新和卸载 Skills，无需重启服务。

```go
// internal/runtime/skill/hot_reload.go

package skill

import (
    "context"
    "path/filepath"
    "time"

    "github.com/fsnotify/fsnotify"
)

// HotReloader 热加载器
type HotReloader struct {
    registry   *Registry
    loader     *Loader
    watcher    *fsnotify.Watcher
    skillPaths map[string]string  // path -> skillName

    // 回调
    onLoad   func(skill *Skill)
    onReload func(old, new *Skill)
    onUnload func(skillName string)

    // 控制
    stopCh chan struct{}
}

// NewHotReloader 创建热加载器
func NewHotReloader(registry *Registry, loader *Loader) (*HotReloader, error) {
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        return nil, err
    }

    return &HotReloader{
        registry:   registry,
        loader:     loader,
        watcher:    watcher,
        skillPaths: make(map[string]string),
        stopCh:     make(chan struct{}),
    }, nil
}

// Watch 监控 Skill 目录
func (h *HotReloader) Watch(dir string) error {
    // 扫描目录下所有 skill.yaml
    err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return nil
        }

        if filepath.Base(path) == "skill.yaml" {
            skillDir := filepath.Dir(path)
            skill, err := h.loader.LoadFile(path)
            if err != nil {
                return nil
            }

            // 注册并记录路径
            h.registry.Register(skill)
            h.skillPaths[skillDir] = skill.Name

            // 添加监控
            h.watcher.Add(skillDir)

            if h.onLoad != nil {
                h.onLoad(skill)
            }
        }
        return nil
    })

    // 启动事件处理循环
    go h.eventLoop()

    return err
}

// eventLoop 事件循环
func (h *HotReloader) eventLoop() {
    debounce := make(map[string]time.Time)
    debounceTimer := 100 * time.Millisecond

    for {
        select {
        case <-h.stopCh:
            return

        case event, ok := <-h.watcher.Events:
            if !ok {
                return
            }

            // 防抖处理
            dir := filepath.Dir(event.Name)
            if last, ok := debounce[dir]; ok && time.Since(last) < debounceTimer {
                continue
            }
            debounce[dir] = time.Now()

            // 处理事件
            if event.Op&fsnotify.Write == fsnotify.Write ||
               event.Op&fsnotify.Create == fsnotify.Create {
                h.handleModify(dir)
            } else if event.Op&fsnotify.Remove == fsnotify.Remove {
                h.handleRemove(dir)
            }

        case err, ok := <-h.watcher.Errors:
            if !ok {
                return
            }
            log.Printf("watcher error: %v", err)
        }
    }
}

// handleModify 处理修改事件
func (h *HotReloader) handleModify(dir string) {
    skillPath := filepath.Join(dir, "skill.yaml")

    // 加载新的 Skill
    newSkill, err := h.loader.LoadFile(skillPath)
    if err != nil {
        log.Printf("failed to load skill: %v", err)
        return
    }

    // 获取旧的 Skill
    oldSkill, exists := h.registry.Get(newSkill.Name)

    // 更新注册
    h.registry.Unregister(newSkill.Name)
    h.registry.Register(newSkill)

    // 触发回调
    if exists {
        if h.onReload != nil {
            h.onReload(oldSkill, newSkill)
        }
        log.Printf("skill reloaded: %s", newSkill.Name)
    } else {
        if h.onLoad != nil {
            h.onLoad(newSkill)
        }
        log.Printf("skill loaded: %s", newSkill.Name)
    }
}

// handleRemove 处理删除事件
func (h *HotReloader) handleRemove(dir string) {
    skillName, ok := h.skillPaths[dir]
    if !ok {
        return
    }

    h.registry.Unregister(skillName)
    delete(h.skillPaths, dir)

    if h.onUnload != nil {
        h.onUnload(skillName)
    }

    log.Printf("skill unloaded: %s", skillName)
}

// Stop 停止监控
func (h *HotReloader) Stop() {
    close(h.stopCh)
    h.watcher.Close()
}
```

#### 热加载配置示例

```yaml
# config/agent.yaml
agent:
  skills:
    dir: "./config/skills"
    autoLoad: true
    hotReload:
      enabled: true
      debounce: 100ms  # 防抖时间
      watchPatterns:
        - "skill.yaml"
        - "prompts/*.md"
        - "templates/*.tmpl"
```

---

### 10.3 Token Budget Manager

智能管理 Token 预算，确保上下文不会超出模型限制，是 Claude Code/Cursor 的关键能力。

```go
// internal/llm/token_budget.go

package llm

import (
    "context"
)

// TokenBudgetManager Token 预算管理器
type TokenBudgetManager struct {
    tokenizer    Tokenizer
    maxTokens    int
    reserved     int  // 为输出预留的 Token
    strategy     BudgetStrategy
}

// Tokenizer Token 计数器接口
type Tokenizer interface {
    Count(text string) int
    CountMessages(messages []Message) int
}

// BudgetStrategy 预算策略
type BudgetStrategy string

const (
    StrategyTruncate   BudgetStrategy = "truncate"   // 截断超出部分
    StrategySummarize  BudgetStrategy = "summarize"  // 摘要压缩
    StrategyPrioritize BudgetStrategy = "prioritize" // 按优先级保留
    StrategyWindow     BudgetStrategy = "window"     // 滑动窗口
)

// BudgetAllocation Token 预算分配
type BudgetAllocation struct {
    SystemPrompt  int  // 系统提示词
    ContextFiles  int  // 上下文文件
    History       int  // 对话历史
    CurrentPrompt int  // 当前提示词
    Reserved      int  // 输出预留
}

// NewTokenBudgetManager 创建预算管理器
func NewTokenBudgetManager(tokenizer Tokenizer, maxTokens int) *TokenBudgetManager {
    return &TokenBudgetManager{
        tokenizer: tokenizer,
        maxTokens: maxTokens,
        reserved:  4096,  // 默认为输出预留 4K
        strategy:  StrategyPrioritize,
    }
}

// Allocate 分配预算
func (m *TokenBudgetManager) Allocate(ctx context.Context, req *BudgetRequest) *BudgetAllocation {
    available := m.maxTokens - m.reserved

    allocation := &BudgetAllocation{
        Reserved: m.reserved,
    }

    // 1. 系统提示词优先级最高
    systemTokens := m.tokenizer.Count(req.SystemPrompt)
    allocation.SystemPrompt = min(systemTokens, available/4)
    available -= allocation.SystemPrompt

    // 2. 当前提示词
    promptTokens := m.tokenizer.Count(req.CurrentPrompt)
    allocation.CurrentPrompt = min(promptTokens, available/4)
    available -= allocation.CurrentPrompt

    // 3. 上下文文件 (按相关度排序后截断)
    contextTokens := m.allocateContext(req.ContextFiles, available/2)
    allocation.ContextFiles = contextTokens
    available -= contextTokens

    // 4. 对话历史 (使用剩余预算)
    allocation.History = available

    return allocation
}

// allocateContext 分配上下文预算
func (m *TokenBudgetManager) allocateContext(files []*ContextFile, budget int) int {
    // 按相关度排序
    sort.Slice(files, func(i, j int) bool {
        return files[i].Relevance > files[j].Relevance
    })

    used := 0
    for _, file := range files {
        tokens := m.tokenizer.Count(file.Content)
        if used+tokens > budget {
            // 截断文件内容
            truncated := m.truncateContent(file.Content, budget-used)
            file.Content = truncated
            used += budget - used
            break
        }
        used += tokens
    }

    return used
}

// truncateContent 截断内容
func (m *TokenBudgetManager) truncateContent(content string, maxTokens int) string {
    // 简单截断 (实际应使用 tokenizer 的解码能力)
    estimated := len(content) * maxTokens / m.tokenizer.Count(content)
    if len(content) > estimated {
        return content[:estimated] + "\n... [truncated]"
    }
    return content
}

// BuildContext 构建符合预算的上下文
func (m *TokenBudgetManager) BuildContext(ctx context.Context, req *BudgetRequest) (*BuildContextResult, error) {
    allocation := m.Allocate(ctx, req)

    result := &BuildContextResult{
        Allocation: allocation,
    }

    // 构建系统提示词
    result.SystemPrompt = req.SystemPrompt

    // 构建上下文文件
    result.ContextFiles = m.selectContextFiles(req.ContextFiles, allocation.ContextFiles)

    // 构建历史 (滑动窗口)
    result.History = m.selectHistory(req.History, allocation.History)

    // 添加当前提示词
    result.CurrentPrompt = req.CurrentPrompt

    return result, nil
}

// selectHistory 选择历史消息
func (m *TokenBudgetManager) selectHistory(history []Message, budget int) []Message {
    var selected []Message
    used := 0

    // 从最新消息开始
    for i := len(history) - 1; i >= 0; i-- {
        tokens := m.tokenizer.CountMessages([]Message{history[i]})
        if used+tokens > budget {
            break
        }
        selected = append([]Message{history[i]}, selected...)
        used += tokens
    }

    return selected
}

// BudgetRequest 预算请求
type BudgetRequest struct {
    SystemPrompt  string
    ContextFiles  []*ContextFile
    History       []Message
    CurrentPrompt string
}

// ContextFile 上下文文件
type ContextFile struct {
    Path      string
    Content   string
    Relevance float64 // 相关度分数
    Language  string
}

// BuildContextResult 构建结果
type BuildContextResult struct {
    Allocation    *BudgetAllocation
    SystemPrompt  string
    ContextFiles  []*ContextFile
    History       []Message
    CurrentPrompt string
    Stats         *ContextStats
}

// ContextStats 上下文统计
type ContextStats struct {
    TotalTokens    int
    FilesIncluded  int
    FilesSkipped   int
    HistoryKept    int
    HistoryDropped int
}
```

#### 预算配置示例

```yaml
# config/agent.yaml
agent:
  tokenBudget:
    maxTokens: 200000       # 最大 Token 数
    reservedForOutput: 4096 # 输出预留
    strategy: "prioritize"

    allocation:
      systemPrompt: 25%  # 系统提示词预算比例
      contextFiles: 40%  # 上下文文件预算比例
      history: 20%       # 对话历史预算比例
      currentPrompt: 15% # 当前提示词预算比例

    contextPriority:
      - "file://.*"   # 文件内容优先
      - "symbol://.*" # 符号定义其次
      - "doc://.*"    # 文档最后
```

---

### 10.4 Streaming Tool Calls

支持流式输出和实时 Tool Call 解析，提供更好的用户体验。

```go
// internal/runtime/executor/stream_executor.go

package executor

import (
    "context"
    "encoding/json"
    "io"
    "strings"
)

// StreamExecutor 流式执行器
type StreamExecutor struct {
    agent      *Agent
    toolParser *ToolCallParser
}

// StreamResult 流式结果
type StreamResult struct {
    Type    StreamEventType
    Content string
    ToolCall *PartialToolCall
    Done    bool
    Error   error
}

// StreamEventType 流事件类型
type StreamEventType string

const (
    EventText      StreamEventType = "text"
    EventToolCall  StreamEventType = "tool_call"
    EventToolStart StreamEventType = "tool_start"
    EventToolEnd   StreamEventType = "tool_end"
    EventDone      StreamEventType = "done"
    EventError     StreamEventType = "error"
)

// PartialToolCall 部分工具调用
type PartialToolCall struct {
    ID        string
    Name      string
    Arguments string                 // 累积的参数 JSON
    Args      map[string]interface{}
    Complete  bool
}

// RunStreaming 流式执行
func (e *StreamExecutor) RunStreaming(ctx context.Context, prompt string) <-chan StreamResult {
    resultCh := make(chan StreamResult, 100)

    go func() {
        defer close(resultCh)

        // 构建请求
        req := &StreamRequest{
            Prompt:    prompt,
            Stream:    true,
            MaxTokens: e.agent.config.MaxTokens,
        }

        // 获取流式响应
        stream, err := e.agent.llmClient.Stream(ctx, req)
        if err != nil {
            resultCh <- StreamResult{Type: EventError, Error: err}
            return
        }
        defer stream.Close()

        var currentTool *PartialToolCall

        for {
            select {
            case <-ctx.Done():
                resultCh <- StreamResult{Type: EventError, Error: ctx.Err()}
                return
            default:
                chunk, err := stream.Recv()
                if err == io.EOF {
                    // 流结束
                    if currentTool != nil && currentTool.Complete {
                        e.executeToolCall(ctx, currentTool, resultCh)
                    }
                    resultCh <- StreamResult{Type: EventDone, Done: true}
                    return
                }
                if err != nil {
                    resultCh <- StreamResult{Type: EventError, Error: err}
                    return
                }

                // 处理 chunk
                for _, delta := range chunk.Deltas {
                    switch delta.Type {
                    case "text":
                        resultCh <- StreamResult{
                            Type:    EventText,
                            Content: delta.Content,
                        }
                    case "tool_call":
                        currentTool = e.processToolCallDelta(currentTool, delta, resultCh)
                    }
                }
            }
        }
    }()

    return resultCh
}

// processToolCallDelta 处理工具调用增量
func (e *StreamExecutor) processToolCallDelta(
    current *PartialToolCall,
    delta StreamDelta,
    resultCh chan<- StreamResult,
) *PartialToolCall {
    if current == nil || current.ID != delta.ToolCallID {
        // 新的工具调用开始
        if current != nil && current.Complete {
            go e.executeToolCall(context.Background(), current, resultCh)
        }

        current = &PartialToolCall{
            ID:       delta.ToolCallID,
            Name:     delta.ToolName,
            Args:     make(map[string]interface{}),
            Complete: false,
        }

        resultCh <- StreamResult{
            Type: EventToolStart,
            ToolCall: &PartialToolCall{
                ID:   delta.ToolCallID,
                Name: delta.ToolName,
            },
        }
    }

    // 累积参数
    if delta.ToolArgs != "" {
        current.Arguments += delta.ToolArgs
    }

    // 检查是否完成
    if delta.ToolCallComplete {
        current.Complete = true
        if err := json.Unmarshal([]byte(current.Arguments), &current.Args); err != nil {
            resultCh <- StreamResult{
                Type:  EventError,
                Error: fmt.Errorf("failed to parse tool arguments: %w", err),
            }
        }
    }

    return current
}

// executeToolCall 执行工具调用
func (e *StreamExecutor) executeToolCall(
    ctx context.Context,
    tc *PartialToolCall,
    resultCh chan<- StreamResult,
) {
    resultCh <- StreamResult{
        Type: EventToolCall,
        ToolCall: &PartialToolCall{
            ID:   tc.ID,
            Name: tc.Name,
            Args: tc.Args,
        },
    }

    result, err := e.agent.mcpManager.CallTool(ctx, "", tc.Name, tc.Args)

    resultCh <- StreamResult{
        Type: EventToolEnd,
        ToolCall: &PartialToolCall{
            ID:   tc.ID,
            Name: tc.Name,
        },
        Content: fmt.Sprintf("%v", result),
        Error:   err,
    }
}

// StreamDelta 流增量
type StreamDelta struct {
    Type             string
    Content          string
    ToolCallID       string
    ToolName         string
    ToolArgs         string
    ToolCallComplete bool
}

// StreamRequest 流式请求
type StreamRequest struct {
    Prompt    string
    Stream    bool
    MaxTokens int
}
```

---

### 10.5 Sandbox 安全执行器

为工具执行提供沙箱隔离，确保安全性。

```go
// internal/runtime/executor/sandbox.go

package executor

import (
    "context"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "syscall"
)

// Sandbox 沙箱执行器
type Sandbox struct {
    config      *SandboxConfig
    permissions *PermissionSet
}

// SandboxConfig 沙箱配置
type SandboxConfig struct {
    // 资源限制
    MaxMemoryMB      int           `yaml:"maxMemoryMB"`
    MaxCPUPercent    int           `yaml:"maxCPUPercent"`
    MaxFileSize      int64         `yaml:"maxFileSize"`
    MaxExecutionTime time.Duration `yaml:"maxExecutionTime"`

    // 文件系统限制
    AllowedPaths  []string `yaml:"allowedPaths"`
    DeniedPaths   []string `yaml:"deniedPaths"`
    ReadOnlyPaths []string `yaml:"readOnlyPaths"`

    // 网络限制
    AllowNetwork bool     `yaml:"allowNetwork"`
    AllowedHosts []string `yaml:"allowedHosts"`

    // 命令限制
    AllowedCommands []string `yaml:"allowedCommands"`
    DeniedCommands  []string `yaml:"deniedCommands"`
    EnvWhitelist    []string `yaml:"envWhitelist"`
}

// PermissionSet 权限集
type PermissionSet struct {
    ReadFiles    map[string]bool
    WriteFiles   map[string]bool
    ExecuteFiles map[string]bool
    Network      bool
}

// NewSandbox 创建沙箱
func NewSandbox(config *SandboxConfig) *Sandbox {
    return &Sandbox{
        config: config,
        permissions: &PermissionSet{
            ReadFiles:    make(map[string]bool),
            WriteFiles:   make(map[string]bool),
            ExecuteFiles: make(map[string]bool),
        },
    }
}

// PermissionOp 权限操作
type PermissionOp string

const (
    OpRead    PermissionOp = "read"
    OpWrite   PermissionOp = "write"
    OpExecute PermissionOp = "execute"
    OpDelete  PermissionOp = "delete"
)

// CheckPermission 检查权限
func (s *Sandbox) CheckPermission(op PermissionOp, path string) error {
    absPath, err := filepath.Abs(path)
    if err != nil {
        return err
    }

    // 检查是否在拒绝列表
    for _, denied := range s.config.DeniedPaths {
        if strings.HasPrefix(absPath, denied) {
            return fmt.Errorf("access denied: %s", path)
        }
    }

    // 检查是否在允许列表
    allowed := false
    for _, allowedPath := range s.config.AllowedPaths {
        if strings.HasPrefix(absPath, allowedPath) {
            allowed = true
            break
        }
    }

    if !allowed && len(s.config.AllowedPaths) > 0 {
        return fmt.Errorf("path not in allowed list: %s", path)
    }

    // 检查写权限
    if op == OpWrite {
        for _, ro := range s.config.ReadOnlyPaths {
            if strings.HasPrefix(absPath, ro) {
                return fmt.Errorf("path is read-only: %s", path)
            }
        }
    }

    return nil
}

// ExecuteCommand 在沙箱中执行命令
func (s *Sandbox) ExecuteCommand(ctx context.Context, cmd string, args []string, workDir string) (*ExecutionResult, error) {
    // 检查命令是否允许
    if err := s.checkCommand(cmd); err != nil {
        return nil, err
    }

    // 检查工作目录
    if err := s.CheckPermission(OpExecute, workDir); err != nil {
        return nil, err
    }

    // 创建带资源限制的命令
    execCmd := exec.CommandContext(ctx, cmd, args...)
    execCmd.Dir = workDir

    // 设置资源限制
    s.setResourceLimits(execCmd)

    // 设置环境变量 (只允许白名单)
    execCmd.Env = s.filterEnv(os.Environ())

    // 执行
    output, err := execCmd.CombinedOutput()
    if err != nil {
        return &ExecutionResult{
            Output: string(output),
            Error:  err.Error(),
        }, err
    }

    return &ExecutionResult{
        Output: string(output),
    }, nil
}

// checkCommand 检查命令权限
func (s *Sandbox) checkCommand(cmd string) error {
    // 检查拒绝列表
    for _, denied := range s.config.DeniedCommands {
        if cmd == denied || strings.HasSuffix(cmd, "/"+denied) {
            return fmt.Errorf("command denied: %s", cmd)
        }
    }

    // 如果有允许列表，检查是否在列表中
    if len(s.config.AllowedCommands) > 0 {
        allowed := false
        for _, allowedCmd := range s.config.AllowedCommands {
            if cmd == allowedCmd || strings.HasSuffix(cmd, "/"+allowedCmd) {
                allowed = true
                break
            }
        }
        if !allowed {
            return fmt.Errorf("command not allowed: %s", cmd)
        }
    }

    return nil
}

// setResourceLimits 设置资源限制
func (s *Sandbox) setResourceLimits(cmd *exec.Cmd) {
    cmd.SysProcAttr = &syscall.SysProcAttr{
        Setpgid: true,
    }
}

// filterEnv 过滤环境变量
func (s *Sandbox) filterEnv(env []string) []string {
    var filtered []string
    for _, e := range env {
        key := strings.SplitN(e, "=", 2)[0]
        for _, allowed := range s.config.EnvWhitelist {
            if key == allowed {
                filtered = append(filtered, e)
                break
            }
        }
    }
    return filtered
}

// ExecutionResult 执行结果
type ExecutionResult struct {
    Output     string
    Error      string
    ExitCode   int
    Duration   time.Duration
    MemoryUsed int64
}
```

#### 沙箱配置示例

```yaml
# config/sandbox.yaml
sandbox:
  maxMemoryMB: 512
  maxCPUPercent: 50
  maxFileSize: 10485760  # 10MB
  maxExecutionTime: 30s

  allowedPaths:
    - "/workspace"
    - "/tmp"

  deniedPaths:
    - "/etc/passwd"
    - "/etc/shadow"
    - "${HOME}/.ssh"

  readOnlyPaths:
    - "/usr/lib"
    - "/usr/share"

  allowNetwork: false

  allowedCommands:
    - "git"
    - "npm"
    - "go"
    - "python"
    - "node"

  deniedCommands:
    - "rm"
    - "sudo"
    - "su"
    - "chmod"

  envWhitelist:
    - "PATH"
    - "HOME"
    - "USER"
    - "GOPATH"
    - "NODE_PATH"
```

---

### 10.6 Self-Reflection 自我反思机制

支持 Agent 在执行过程中自我检测和修复错误。

```go
// internal/runtime/agent/reflector.go

package agent

import (
    "context"
    "encoding/json"
)

// Reflector 自我反思器
type Reflector struct {
    llmClient   LLMClient
    maxAttempts int
}

// ReflectionResult 反思结果
type ReflectionResult struct {
    HasError     bool
    ErrorType    string
    Analysis     string
    SuggestedFix string
    RetryAction  *RetryAction
}

// RetryAction 重试动作
type RetryAction struct {
    Type            string                 // "modify_args", "change_tool", "skip", "abort"
    ModifiedArgs    map[string]interface{}
    AlternativeTool string
    Reason          string
}

// Reflect 反思执行结果
func (r *Reflector) Reflect(ctx context.Context, obs *Observation) (*ReflectionResult, error) {
    if obs.Success {
        return &ReflectionResult{HasError: false}, nil
    }

    // 构建反思 prompt
    prompt := r.buildReflectionPrompt(obs)

    // 调用 LLM 分析
    response, err := r.llmClient.Call(ctx, prompt)
    if err != nil {
        return nil, err
    }

    // 解析反思结果
    var result ReflectionResult
    if err := json.Unmarshal([]byte(response), &result); err != nil {
        return nil, err
    }

    return &result, nil
}

// buildReflectionPrompt 构建反思提示词
func (r *Reflector) buildReflectionPrompt(obs *Observation) string {
    return fmt.Sprintf(`
You are a self-reflection agent. Analyze the following failed tool execution and suggest fixes.

Tool: %s
Input: %s
Error: %s
Output: %s

Analyze the error and respond in JSON format:
{
  "hasError": true,
  "errorType": "permission_denied|not_found|invalid_input|timeout|unknown",
  "analysis": "detailed analysis of what went wrong",
  "suggestedFix": "suggested fix for the issue",
  "retryAction": {
    "type": "modify_args|change_tool|skip|abort",
    "modifiedArgs": {"arg1": "new_value"},
    "alternativeTool": "tool_name_if_changing",
    "reason": "why this action is suggested"
  }
}
`, obs.Tool, toJSON(obs.Input), obs.Error, toJSON(obs.Output))
}

// SelfCorrectingExecutor 自纠正执行器
type SelfCorrectingExecutor struct {
    executor   *Executor
    reflector  *Reflector
    maxRetries int
}

// ExecuteWithRetry 带重试的执行
func (e *SelfCorrectingExecutor) ExecuteWithRetry(
    ctx context.Context,
    tool string,
    args map[string]interface{},
) (*ExecutionResult, error) {
    var lastError error

    for attempt := 0; attempt < e.maxRetries; attempt++ {
        // 执行工具
        result, err := e.executor.Execute(ctx, tool, args)
        if err == nil && result.Success {
            return result, nil
        }

        lastError = err

        // 构建观察
        obs := &Observation{
            Tool:    tool,
            Input:   args,
            Output:  result,
            Success: false,
            Error:   err.Error(),
        }

        // 反思
        reflection, rErr := e.reflector.Reflect(ctx, obs)
        if rErr != nil {
            continue
        }

        // 根据反思结果调整
        switch reflection.RetryAction.Type {
        case "modify_args":
            args = reflection.RetryAction.ModifiedArgs
        case "change_tool":
            tool = reflection.RetryAction.AlternativeTool
        case "abort":
            return nil, fmt.Errorf("aborted: %s", reflection.RetryAction.Reason)
        case "skip":
            return &ExecutionResult{
                Success: false,
                Output:  "skipped due to error: " + reflection.Analysis,
            }, nil
        }

        // 等待后重试
        time.Sleep(time.Duration(attempt+1) * time.Second)
    }

    return nil, fmt.Errorf("max retries exceeded: %w", lastError)
}

// ErrorPattern 错误模式
type ErrorPattern struct {
    Pattern   string        // 正则模式
    ErrorType string        // 错误类型
    AutoFix   AutoFixStrategy
}

// AutoFixStrategy 自动修复策略接口
type AutoFixStrategy interface {
    Fix(ctx context.Context, obs *Observation) (*RetryAction, error)
}

// CommonErrorPatterns 常见错误模式
var CommonErrorPatterns = []ErrorPattern{
    {
        Pattern:   "permission denied",
        ErrorType: "permission_denied",
        AutoFix:   &PermissionFixStrategy{},
    },
    {
        Pattern:   "no such file or directory",
        ErrorType: "not_found",
        AutoFix:   &NotFoundFixStrategy{},
    },
    {
        Pattern:   "timeout",
        ErrorType: "timeout",
        AutoFix:   &TimeoutFixStrategy{},
    },
}

// PermissionFixStrategy 权限错误修复策略
type PermissionFixStrategy struct{}

func (s *PermissionFixStrategy) Fix(ctx context.Context, obs *Observation) (*RetryAction, error) {
    return &RetryAction{
        Type:   "abort",
        Reason: "Permission issue requires user intervention",
    }, nil
}

// NotFoundFixStrategy 未找到错误修复策略
type NotFoundFixStrategy struct{}

func (s *NotFoundFixStrategy) Fix(ctx context.Context, obs *Observation) (*RetryAction, error) {
    args := obs.Input.(map[string]interface{})
    if path, ok := args["path"].(string); ok {
        // 尝试常见替代路径
        alternatives := []string{
            filepath.Join(filepath.Dir(path), strings.ToLower(filepath.Base(path))),
            filepath.Join(filepath.Dir(path), strings.ToUpper(filepath.Base(path))),
        }
        for _, alt := range alternatives {
            if _, err := os.Stat(alt); err == nil {
                args["path"] = alt
                return &RetryAction{
                    Type:         "modify_args",
                    ModifiedArgs: args,
                    Reason:       "Found alternative path",
                }, nil
            }
        }
    }

    return &RetryAction{
        Type:   "abort",
        Reason: "File or directory not found and no alternatives available",
    }, nil
}
```

---

### 10.7 Code Patch Engine

安全的代码修改引擎，支持精确的代码补丁应用。

```go
// internal/patch/engine.go

package patch

import (
    "context"
    "strings"
)

// PatchEngine 代码补丁引擎
type PatchEngine struct {
    safetyChecker *SafetyChecker
    backupManager *BackupManager
}

// Patch 补丁定义
type Patch struct {
    File       string
    Type       PatchType
    OldContent string
    NewContent string
    StartLine  int
    EndLine    int
    Confidence float64
    Reason     string
}

// PatchType 补丁类型
type PatchType string

const (
    PatchReplace PatchType = "replace" // 替换代码块
    PatchInsert  PatchType = "insert"  // 插入代码
    PatchDelete  PatchType = "delete"  // 删除代码
    PatchMove    PatchType = "move"    // 移动代码
)

// ApplyPatch 应用补丁
func (e *PatchEngine) ApplyPatch(ctx context.Context, patch *Patch) (*PatchResult, error) {
    // 1. 安全检查
    if err := e.safetyChecker.Check(ctx, patch); err != nil {
        return nil, fmt.Errorf("safety check failed: %w", err)
    }

    // 2. 备份原文件
    backup, err := e.backupManager.CreateBackup(patch.File)
    if err != nil {
        return nil, fmt.Errorf("backup failed: %w", err)
    }

    // 3. 读取文件内容
    content, err := os.ReadFile(patch.File)
    if err != nil {
        return nil, err
    }

    // 4. 应用补丁
    newContent, err := e.applyPatch(content, patch)
    if err != nil {
        e.backupManager.RestoreBackup(backup)
        return nil, fmt.Errorf("patch application failed: %w", err)
    }

    // 5. 验证语法
    if err := e.validateSyntax(patch.File, newContent); err != nil {
        e.backupManager.RestoreBackup(backup)
        return nil, fmt.Errorf("syntax validation failed: %w", err)
    }

    // 6. 写入文件
    if err := os.WriteFile(patch.File, newContent, 0644); err != nil {
        e.backupManager.RestoreBackup(backup)
        return nil, err
    }

    return &PatchResult{
        Success:      true,
        BackupPath:   backup.Path,
        LinesChanged: countLinesChanged(string(content), string(newContent)),
    }, nil
}

// applyPatch 应用补丁到内容
func (e *PatchEngine) applyPatch(content []byte, patch *Patch) ([]byte, error) {
    lines := strings.Split(string(content), "\n")

    switch patch.Type {
    case PatchReplace:
        return e.applyReplace(lines, patch)
    case PatchInsert:
        return e.applyInsert(lines, patch)
    case PatchDelete:
        return e.applyDelete(lines, patch)
    default:
        return nil, fmt.Errorf("unknown patch type: %s", patch.Type)
    }
}

// applyReplace 应用替换补丁
func (e *PatchEngine) applyReplace(lines []string, patch *Patch) ([]byte, error) {
    // 验证旧行内容匹配
    oldLines := strings.Split(patch.OldContent, "\n")
    for i, oldLine := range oldLines {
        lineNum := patch.StartLine + i - 1
        if lineNum >= len(lines) {
            return nil, fmt.Errorf("line %d out of range", lineNum+1)
        }
        if lines[lineNum] != oldLine {
            return nil, fmt.Errorf("content mismatch at line %d", lineNum+1)
        }
    }

    // 替换内容
    newLines := strings.Split(patch.NewContent, "\n")
    result := make([]string, 0)
    result = append(result, lines[:patch.StartLine-1]...)
    result = append(result, newLines...)
    result = append(result, lines[patch.EndLine:]...)

    return []byte(strings.Join(result, "\n")), nil
}

// SafetyChecker 安全检查器
type SafetyChecker struct {
    rules []SafetyRule
}

// SafetyRule 安全规则接口
type SafetyRule interface {
    Check(patch *Patch) error
}

// Check 执行安全检查
func (c *SafetyChecker) Check(ctx context.Context, patch *Patch) error {
    for _, rule := range c.rules {
        if err := rule.Check(patch); err != nil {
            return err
        }
    }
    return nil
}

// DefaultSafetyRules 默认安全规则
func DefaultSafetyRules() []SafetyRule {
    return []SafetyRule{
        &NoDeleteProtectedFilesRule{},
        &NoModifyGitDirRule{},
        &MaxChangeSizeRule{MaxLines: 100},
        &RequireReasonRule{},
    }
}

// NoDeleteProtectedFilesRule 禁止删除受保护文件
type NoDeleteProtectedFilesRule struct{}

func (r *NoDeleteProtectedFilesRule) Check(patch *Patch) error {
    protected := []string{".git", ".env", "credentials", "secrets"}
    for _, p := range protected {
        if strings.Contains(patch.File, p) {
            return fmt.Errorf("cannot modify protected file: %s", patch.File)
        }
    }
    return nil
}

// NoModifyGitDirRule 禁止修改 .git 目录
type NoModifyGitDirRule struct{}

func (r *NoModifyGitDirRule) Check(patch *Patch) error {
    if strings.Contains(patch.File, ".git/") {
        return fmt.Errorf("cannot modify .git directory")
    }
    return nil
}

// MaxChangeSizeRule 最大变更大小规则
type MaxChangeSizeRule struct {
    MaxLines int
}

func (r *MaxChangeSizeRule) Check(patch *Patch) error {
    oldLines := len(strings.Split(patch.OldContent, "\n"))
    newLines := len(strings.Split(patch.NewContent, "\n"))
    if oldLines > r.MaxLines || newLines > r.MaxLines {
        return fmt.Errorf("change too large: max %d lines allowed", r.MaxLines)
    }
    return nil
}

// RequireReasonRule 需要原因规则
type RequireReasonRule struct{}

func (r *RequireReasonRule) Check(patch *Patch) error {
    if patch.Reason == "" {
        return fmt.Errorf("patch must have a reason")
    }
    return nil
}

// BackupManager 备份管理器
type BackupManager struct {
    backupDir string
}

// CreateBackup 创建备份
func (m *BackupManager) CreateBackup(filePath string) (*Backup, error) {
    content, err := os.ReadFile(filePath)
    if err != nil {
        return nil, err
    }

    backupPath := filepath.Join(m.backupDir, fmt.Sprintf("%s.%d.bak",
        filepath.Base(filePath), time.Now().Unix()))

    if err := os.WriteFile(backupPath, content, 0644); err != nil {
        return nil, err
    }

    return &Backup{
        Path:      backupPath,
        Original:  filePath,
        Timestamp: time.Now(),
    }, nil
}

// RestoreBackup 恢复备份
func (m *BackupManager) RestoreBackup(backup *Backup) error {
    content, err := os.ReadFile(backup.Path)
    if err != nil {
        return err
    }
    return os.WriteFile(backup.Original, content, 0644)
}

// Backup 备份信息
type Backup struct {
    Path      string
    Original  string
    Timestamp time.Time
}

// PatchResult 补丁结果
type PatchResult struct {
    Success      bool
    BackupPath   string
    LinesChanged int
    Error        string
}

// ExecuteParallel 并行执行多个工具调用
func (e *ParallelExecutor) ExecuteParallel(
    ctx context.Context,
    calls []*ToolCall,
    executor ToolExecutor,
) []*ParallelResult {
    results := make([]*ParallelResult, len(calls))

    // 按优先级排序
    sorted := make([]*ToolCall, len(calls))
    copy(sorted, calls)
    sort.Slice(sorted, func(i, j int) bool {
        return sorted[i].Priority > sorted[j].Priority
    })

    // 创建 ID 映射
    idMap := make(map[string]int)
    for i, call := range calls {
        idMap[call.ID] = i
    }

    var wg sync.WaitGroup
    semaphore := make(chan struct{}, e.maxConcurrency)

    for _, call := range sorted {
        wg.Add(1)

        go func(tc *ToolCall) {
            defer wg.Done()

            // 获取信号量
            semaphore <- struct{}{}
            defer func() { <-semaphore }()

            // 限流
            e.rateLimiter.Wait(ctx)

            // 执行
            start := time.Now()
            result, err := executor.Execute(ctx, tc.Name, tc.Args)

            results[idMap[tc.ID]] = &ParallelResult{
                ID:       tc.ID,
                Result:   result,
                Error:    err,
                Duration: time.Since(start),
            }
        }(call)
    }

    wg.Wait()
    return results
}

// ExecuteWithDependencies 执行有依赖关系的工具调用
func (e *ParallelExecutor) ExecuteWithDependencies(
    ctx context.Context,
    calls []*ToolCall,
    deps map[string][]string, // callID -> dependencies
    executor ToolExecutor,
) []*ParallelResult {
    results := make([]*ParallelResult, len(calls))
    completed := make(map[string]bool)
    var completedMu sync.RWMutex

    // 创建 ID 映射
    idMap := make(map[string]int)
    for i, call := range calls {
        idMap[call.ID] = i
    }

    // 执行函数
    executeCall := func(tc *ToolCall) *ParallelResult {
        start := time.Now()
        result, err := executor.Execute(ctx, tc.Name, tc.Args)
        return &ParallelResult{
            ID:       tc.ID,
            Result:   result,
            Error:    err,
            Duration: time.Since(start),
        }
    }

    // 检查依赖是否满足
    canExecute := func(callID string) bool {
        completedMu.RLock()
        defer completedMu.RUnlock()

        for _, dep := range deps[callID] {
            if !completed[dep] {
                return false
            }
        }
        return true
    }

    // 标记完成
    markCompleted := func(callID string) {
        completedMu.Lock()
        defer completedMu.Unlock()
        completed[callID] = true
    }

    // 执行循环
    pending := make(map[string]*ToolCall)
    for _, call := range calls {
        pending[call.ID] = call
    }

    for len(pending) > 0 {
        var ready []*ToolCall

        // 找出可执行的调用
        for id, call := range pending {
            if canExecute(id) {
                ready = append(ready, call)
            }
        }

        if len(ready) == 0 {
            // 存在循环依赖或无法满足的依赖
            break
        }

        // 并行执行就绪的调用
        var wg sync.WaitGroup
        for _, call := range ready {
            wg.Add(1)
            delete(pending, call.ID)

            go func(tc *ToolCall) {
                defer wg.Done()

                result := executeCall(tc)
                results[idMap[tc.ID]] = result

                if result.Error == nil {
                    markCompleted(tc.ID)
                }
            }(call)
        }

        wg.Wait()
    }

    // 标记未执行的调用
    for id := range pending {
        results[idMap[id]] = &ParallelResult{
            ID:    id,
            Error: fmt.Errorf("dependency not satisfied"),
        }
    }

    return results
}

// RateLimiter 速率限制器
type RateLimiter struct {
    rate   int
    tokens chan struct{}
}

// NewRateLimiter 创建速率限制器
func NewRateLimiter(rate int) *RateLimiter {
    return &RateLimiter{
        rate:   rate,
        tokens: make(chan struct{}, rate),
    }
}

// Wait 等待可用令牌
func (r *RateLimiter) Wait(ctx context.Context) {
    select {
    case r.tokens <- struct{}{}:
    case <-ctx.Done():
    }
}

// ToolExecutor 工具执行器接口
type ToolExecutor interface {
    Execute(ctx context.Context, name string, args map[string]interface{}) (interface{}, error)
}
```

---

### 10.8 统一 LLM Runtime - 补充实现

#### Tokenizer 实现

```go
// internal/llm/tokenizer.go

package llm

import (
    "regexp"
    "strings"
)

// Tokenizer 实现计数器接口
type Tokenizer struct {
    strategy string // "openai" | "anthropic" | "simple"
}

// NewTokenizer 创建 Token 计数器
func NewTokenizer(strategy string) *Tokenizer {
    return &Tokenizer{strategy: strategy}
}

// Count 计算文本的 Token 数量（简化实现）
func (t *Tokenizer) Count(text string) int {
    switch t.strategy {
    case "openai":
        return t.countOpenAITokens(text)
    case "anthropic":
        return t.countAnthropicTokens(text)
    default:
        return t.countSimple(text)
    }
}

// countSimple 简单计数（按单词）
func (t *Tokenizer) countSimple(text string) int {
    words := strings.Fields(text)
    return len(words)
}

// countOpenAITokens OpenAI 近似计数
func (t *Tokenizer) countOpenAITokens(text string) int {
    // 简化实现：按字符数 / 4 估算
    return len(text) / 4
}

// countAnthropicTokens Anthropic 近似计数
func (t *Tokenizer) countAnthropicTokens(text string) int {
    // 简化实现：按字符数 / 3.5 估算
    return int(float64(len(text)) / 3.5)
}

// CountMessages 计算消息的 Token 数量
func (t *Tokenizer) CountMessages(messages []Message) int {
    var total int
    for _, msg := range messages {
        total += t.Count(msg.Content)
        // 消息元数据大约 4 tokens
        total += 4
    }
    return total
}
```

#### LLM Provider 具体实现

```go
// internal/llm/providers/openai.go

package providers

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
)

// OpenAIProvider OpenAI 提供者
type OpenAIProvider struct {
    apiKey  string
    baseURL string
    models  map[string]*ModelInfo
}

// ModelInfo 模型信息
type ModelInfo struct {
    Name            string
    MaxContext      int
    MaxOutput       int
    SupportsVision  bool
    SupportsTools   bool
    SupportsJSON    bool
}

// NewOpenAIProvider 创建 OpenAI 提供者
func NewOpenAIProvider(apiKey, baseURL string) *OpenAIProvider {
    return &OpenAIProvider{
        apiKey:  apiKey,
        baseURL: baseURL,
        models: map[string]*ModelInfo{
            "gpt-4-turbo": {
                Name:           "gpt-4-turbo",
                MaxContext:     128000,
                MaxOutput:      4096,
                SupportsVision: true,
                SupportsTools:  true,
                SupportsJSON:   true,
            },
            "gpt-4o": {
                Name:           "gpt-4o",
                MaxContext:     128000,
                MaxOutput:      16384,
                SupportsVision: true,
                SupportsTools:  true,
                SupportsJSON:   true,
            },
        },
    }
}

// Name 返回提供者名称
func (p *OpenAIProvider) Name() string {
    return "openai"
}

// Call 调用 LLM（通过 Gateway）
func (p *OpenAIProvider) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
    // 构建请求
    body := map[string]interface{}{
        "model":       req.Model,
        "messages":    req.Messages,
        "max_tokens":  req.MaxTokens,
        "temperature": req.Temperature,
    }

    if len(req.Tools) > 0 {
        body["tools"] = req.Tools
    }

    // 序列化请求
    jsonBody, err := json.Marshal(body)
    if err != nil {
        return nil, err
    }

    // 创建 HTTP 请求
    httpReq, err := http.NewRequestWithContext(
        ctx,
        "POST",
        p.baseURL+"/v1/chat/completions",
        bytes.NewReader(jsonBody),
    )
    if err != nil {
        return nil, err
    }

    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

    // 发送请求
    client := &http.Client{}
    resp, err := client.Do(httpReq)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    // 解析响应
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("API error: %s", resp.Status)
    }

    var result struct {
        ID      string `json:"id"`
        Choices []struct {
            Message *LLMResponse `json:"message"`
        } `json:"choices"`
        Usage struct {
            PromptTokens     int `json:"prompt_tokens"`
            CompletionTokens int `json:"completion_tokens"`
            TotalTokens      int `json:"total_tokens"`
        } `json:"usage"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }

    if len(result.Choices) == 0 {
        return nil, fmt.Errorf("no choices in response")
    }

    response := result.Choices[0].Message
    response.Usage = TokenUsage{
        PromptTokens:     result.Usage.PromptTokens,
        CompletionTokens: result.Usage.CompletionTokens,
        TotalTokens:      result.Usage.TotalTokens,
    }
    response.Model = req.Model

    return response, nil
}

// Stream 流式调用
func (p *OpenAIProvider) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
    // 简化实现，实际需要 SSE 处理
    ch := make(chan StreamChunk, 10)
    go func() {
        defer close(ch)
        ch <- StreamChunk{Type: "text", Content: "streaming..."}
        ch <- StreamChunk{Type: "done", Done: true}
    }()
    return ch, nil
}

// CountTokens 统计 Token
func (p *OpenAIProvider) CountTokens(text string) int {
    return NewTokenizer("openai").Count(text)
}

// GetCapabilities 获取模型能力
func (p *OpenAIProvider) GetCapabilities() *ModelCapabilities {
    return &ModelCapabilities{
        MaxContextTokens:  128000,
        MaxOutputTokens:   4096,
        SupportsVision:    true,
        SupportsTools:     true,
        SupportsStreaming: true,
        SupportsJSONMode:  true,
    }
}
```

```go
// internal/llm/providers/anthropic.go

package providers

import (
   "bytes"
"context"
"encoding/json"
"fmt"
"net/http"
"time"
)

// AnthropicProvider Anthropic 提供者
type AnthropicProvider struct {
    apiKey  string
    baseURL string
    models  map[string]*ModelInfo
}

// NewAnthropicProvider 创建 Anthropic 提供者
func NewAnthropicProvider(apiKey, baseURL string) *AnthropicProvider {
    return &AnthropicProvider{
        apiKey:  apiKey,
        baseURL: baseURL,
        models: map[string]*ModelInfo{
            "claude-3-5-sonnet": {
                Name:           "claude-3-5-sonnet",
                MaxContext:     200000,
                MaxOutput:      8192,
                SupportsVision: true,
                SupportsTools:  true,
                SupportsJSON:   true,
            },
            "claude-3-opus": {
                Name:           "claude-3-opus",
                MaxContext:     200000,
                MaxOutput:      4096,
                SupportsVision: true,
                SupportsTools:  true,
                SupportsJSON:   true,
            },
        },
    }
}

// Name 返回提供者名称
func (p *AnthropicProvider) Name() string {
    return "anthropic"
}

// Call 调用 LLM
func (p *AnthropicProvider) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
    // 转换消息格式（OpenAI -> Anthropic）
    messages := p.convertMessages(req.Messages)

    body := map[string]interface{}{
        "model":      req.Model,
        "messages":   messages,
        "max_tokens": req.MaxTokens,
    }

    if req.Temperature > 0 {
        body["temperature"] = req.Temperature
    }

    if len(req.Tools) > 0 {
        body["tools"] = p.convertTools(req.Tools)
    }

    jsonBody, _ := json.Marshal(body)

    httpReq, _ := http.NewRequestWithContext(
        ctx,
        "POST",
        p.baseURL+"/v1/messages",
        bytes.NewReader(jsonBody),
    )

    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("x-api-key", p.apiKey)
    httpReq.Header.Set("anthropic-version", "2023-06-01")

    client := &http.Client{Timeout: 60 * time.Second}
    resp, err := client.Do(httpReq)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var result map[string]interface{}
    json.NewDecoder(resp.Body).Decode(&result)

    // 解析响应
    content := result["content"].([]interface{})
    var responseText string
    var toolCalls []ToolCall

    for _, item := range content {
        block := item.(map[string]interface{})
        if block["type"] == "text" {
            responseText = block["text"].(string)
        } else if block["type"] == "tool_use" {
            tc := ToolCall{
                ID:   block["id"].(string),
                Name: block["name"].(string),
            }
            if args, ok := block["input"].(map[string]interface{}); ok {
                tc.Args = args
            }
            toolCalls = append(toolCalls, tc)
        }
    }

    return &LLMResponse{
        Content:   responseText,
        ToolCalls: toolCalls,
        Model:     req.Model,
    }, nil
}

// convertMessages 转换消息格式
func (p *AnthropicProvider) convertMessages(messages []Message) []map[string]interface{} {
    result := make([]map[string]interface{}, len(messages))
    for i, msg := range messages {
        result[i] = map[string]interface{}{
            "role":    msg.Role,
            "content": msg.Content,
        }
    }
    return result
}

// convertTools 转换工具格式
func (p *AnthropicProvider) convertTools(tools []ToolDefinition) []map[string]interface{} {
    result := make([]map[string]interface{}, len(tools))
    for i, tool := range tools {
        result[i] = map[string]interface{}{
            "name":        tool.Name,
            "description": tool.Description,
            "input_schema": tool.Parameters,
        }
    }
    return result
}

func (p *AnthropicProvider) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
    ch := make(chan StreamChunk, 10)
    go func() {
        defer close(ch)
        ch <- StreamChunk{Type: "done", Done: true}
    }()
    return ch, nil
}

func (p *AnthropicProvider) CountTokens(text string) int {
    return NewTokenizer("anthropic").Count(text)
}

func (p *AnthropicProvider) GetCapabilities() *ModelCapabilities {
    return &ModelCapabilities{
        MaxContextTokens:  200000,
        MaxOutputTokens:   4096,
        SupportsVision:    true,
        SupportsTools:     true,
        SupportsStreaming: true,
        SupportsJSONMode:  true,
    }
}
```

#### SessionStorage 具体实现

```go
// internal/runtime/chat/storage.go

package chat

import (
"context"
"time"
)

// SQLiteStorage SQLite 会话存储
type SQLiteStorage struct {
    dbPath string
}

// NewSQLiteStorage 创建 SQLite 存储
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
    // 初始化数据库
    storage := &SQLiteStorage{dbPath: dbPath}
    if err := storage.initDB(); err != nil {
        return nil, err
    }
    return storage, nil
}

// initDB 初始化数据库
func (s *SQLiteStorage) initDB() error {
    // 实现数据库连接和表创建
    // CREATE TABLE sessions (...);
    return nil
}

// Save 保存会话
func (s *SQLiteStorage) Save(ctx context.Context, session *Session) error {
    // 实现保存逻辑
    return nil
}

// Load 加载会话
func (s *SQLiteStorage) Load(ctx context.Context, sessionID string) (*Session, error) {
    // 实现加载逻辑
    return &Session{}, nil
}

// Delete 删除会话
func (s *SQLiteStorage) Delete(ctx context.Context, sessionID string) error {
    // 实现删除逻辑
    return nil
}

// List 列出用户的所有会话
func (s *SQLiteStorage) List(ctx context.Context, userID string) ([]*Session, error) {
    // 实现查询逻辑
    return []*Session{}, nil
}

// InMemoryStorage 内存会话存储（用于开发和测试）
type InMemoryStorage struct {
    sessions map[string]*Session
    mu       sync.RWMutex
}

// NewInMemoryStorage 创建内存存储
func NewInMemoryStorage() *InMemoryStorage {
    return &InMemoryStorage{
        sessions: make(map[string]*Session),
    }
}

// Save 保存会话
func (s *InMemoryStorage) Save(ctx context.Context, session *Session) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.sessions[session.ID] = session
    return nil
}

// Load 加载会话
func (s *InMemoryStorage) Load(ctx context.Context, sessionID string) (*Session, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    session, ok := s.sessions[sessionID]
    if !ok {
        return nil, fmt.Errorf("session not found: %s", sessionID)
    }
    return session, nil
}

// Delete 删除会话
func (s *InMemoryStorage) Delete(ctx context.Context, sessionID string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    delete(s.sessions, sessionID)
    return nil
}

// List 列出用户的所有会话
func (s *InMemoryStorage) List(ctx context.Context, userID string) ([]*Session, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    var result []*Session
    for _, session := range s.sessions {
        if session.UserID == userID {
            result = append(result, session)
        }
    }
    return result, nil
}
```

---

### 10.9 SessionStorage 接口定义（移动到正确位置）


支持多模型切换和统一调用接口。

```go
// internal/llm/runtime.go

package llm

import (
    "context"
)

// LLMRuntime 统一 LLM 运行时
type LLMRuntime struct {
    providers map[string]LLMProvider
    router    *ModelRouter
    config    *RuntimeConfig
}

// LLMProvider LLM 提供者接口
type LLMProvider interface {
    Name() string
    Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error)
    Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error)
    CountTokens(text string) int
    GetCapabilities() *ModelCapabilities
}

// ModelCapabilities 模型能力
type ModelCapabilities struct {
    MaxContextTokens  int
    MaxOutputTokens   int
    SupportsVision    bool
    SupportsTools     bool
    SupportsStreaming bool
    SupportsJSONMode  bool
}

// ModelRouter 模型路由器
type ModelRouter struct {
    rules []*RoutingRule
}

// RoutingRule 路由规则
type RoutingRule struct {
    Condition RoutingCondition
    Model     string
    Priority  int
}

// RoutingCondition 路由条件接口
type RoutingCondition interface {
    Match(req *LLMRequest) bool
}

// NewLLMRuntime 创建 LLM 运行时
func NewLLMRuntime(config *RuntimeConfig) *LLMRuntime {
    runtime := &LLMRuntime{
        providers: make(map[string]LLMProvider),
        router:    NewModelRouter(),
        config:    config,
    }

    runtime.registerProviders()
    return runtime
}

// registerProviders 注册提供者
func (r *LLMRuntime) registerProviders() {
    if r.config.OpenAI.Enabled {
        r.providers["openai"] = NewOpenAIProvider(r.config.OpenAI)
    }
    if r.config.Anthropic.Enabled {
        r.providers["anthropic"] = NewAnthropicProvider(r.config.Anthropic)
    }
    if r.config.Azure.Enabled {
        r.providers["azure"] = NewAzureProvider(r.config.Azure)
    }
    if r.config.Local.Enabled {
        r.providers["local"] = NewLocalProvider(r.config.Local)
    }
}

// Call 统一调用接口
func (r *LLMRuntime) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
    model := r.selectModel(req)
    provider := r.getProvider(model)
    return provider.Call(ctx, req)
}

// Stream 统一流式调用接口
func (r *LLMRuntime) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
    model := r.selectModel(req)
    provider := r.getProvider(model)
    return provider.Stream(ctx, req)
}

// selectModel 选择模型
func (r *LLMRuntime) selectModel(req *LLMRequest) string {
    // 如果请求指定了模型
    if req.Model != "" {
        return req.Model
    }

    // 使用路由规则选择
    return r.router.Route(req)
}

// LLMRequest LLM 请求
type LLMRequest struct {
    Model       string
    Messages    []Message
    Tools       []ToolDefinition
    MaxTokens   int
    Temperature float64
    Stream      bool
}

// LLMResponse LLM 响应
type LLMResponse struct {
    Content   string
    ToolCalls []ToolCall
    Usage     TokenUsage
    Model     string
}

// TokenUsage Token 使用统计
type TokenUsage struct {
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
}

// StreamChunk 流式响应块
type StreamChunk struct {
    Type    string
    Content string
    Done    bool
}
```

#### LLM 配置示例

```yaml
# config/llm.yaml
providers:
  openai:
    enabled: true
    apiKey: "${OPENAI_API_KEY}"
    models:
      - name: "gpt-4-turbo"
        maxContext: 128000
        maxOutput: 4096
      - name: "gpt-4o"
        maxContext: 128000
        maxOutput: 16384

  anthropic:
    enabled: true
    apiKey: "${ANTHROPIC_API_KEY}"
    models:
      - name: "claude-3-5-sonnet"
        maxContext: 200000
        maxOutput: 8192
      - name: "claude-3-opus"
        maxContext: 200000
        maxOutput: 4096

  local:
    enabled: false
    endpoint: "http://localhost:11434"
    models:
      - name: "codellama"
        maxContext: 16384

routing:
  defaultModel: "claude-3-5-sonnet"
  rules:
    - condition:
        type: "task"
        value: "code_review"
      model: "claude-3-opus"
    - condition:
        type: "task"
        value: "quick_fix"
      model: "gpt-4o"
    - condition:
        type: "tokens"
        max: 100000
      model: "claude-3-5-sonnet"
```

---

## 十一、缺失设计补充

### 11.1 Chat/Session 层设计

Chat 层管理用户会话，提供多轮对话上下文和会话持久化能力。

```go
// internal/runtime/chat/session.go

package chat

import (
    "context"
    "time"
    "github.com/ai-gateway/gateway/internal/runtime/types"
)

// Session 用户会话
type Session struct {
    ID          string
    UserID      string
    CreatedAt   time.Time
    UpdatedAt   time.Time
    State       SessionState
    History     []types.Message
    Context     map[string]interface{}
    Metadata    SessionMetadata
}

// SessionState 会话状态
type SessionState string

const (
    StateActive   SessionState = "active"
    StateIdle     SessionState = "idle"
    StateClosed   SessionState = "closed"
    StateArchived SessionState = "archived"
)

// SessionMetadata 会话元数据
type SessionMetadata struct {
    Tags        []string
    Title       string
    Summary     string
    TotalTurns  int
    LastAgent   string
    LastSkill   string
}

// SessionManager 会话管理器
type SessionManager struct {
    sessions    map[string]*Session
    storage     SessionStorage
    ttl         time.Duration
    maxHistory  int
}

// SessionStorage 会话存储接口
type SessionStorage interface {
    Save(ctx context.Context, session *Session) error
    Load(ctx context.Context, sessionID string) (*Session, error)
    Delete(ctx context.Context, sessionID string) error
    List(ctx context.Context, userID string) ([]*Session, error)
}

// NewSessionManager 创建会话管理器
func NewSessionManager(storage SessionStorage) *SessionManager {
    return &SessionManager{
        sessions:   make(map[string]*Session),
        storage:    storage,
        ttl:        24 * time.Hour,
        maxHistory: 100,
    }
}

// Create 创建新会话
func (m *SessionManager) Create(ctx context.Context, userID string) (*Session, error) {
    session := &Session{
        ID:        generateSessionID(),
        UserID:    userID,
        CreatedAt: time.Now(),
        UpdatedAt: time.Now(),
        State:     StateActive,
        History:   make([]types.Message, 0),
        Context:   make(map[string]interface{}),
    }

    if err := m.storage.Save(ctx, session); err != nil {
        return nil, err
    }

    m.sessions[session.ID] = session
    return session, nil
}

// Get 获取会话
func (m *SessionManager) Get(ctx context.Context, sessionID string) (*Session, error) {
    // 先从内存获取
    if session, ok := m.sessions[sessionID]; ok {
        return session, nil
    }

    // 从存储加载
    session, err := m.storage.Load(ctx, sessionID)
    if err != nil {
        return nil, err
    }

    m.sessions[sessionID] = session
    return session, nil
}

// AddMessage 添加消息到会话
func (m *SessionManager) AddMessage(ctx context.Context, sessionID string, message types.Message) error {
    session, err := m.Get(ctx, sessionID)
    if err != nil {
        return err
    }

    session.History = append(session.History, message)

    // 限制历史长度
    if len(session.History) > m.maxHistory {
        session.History = session.History[len(session.History)-m.maxHistory:]
    }

    session.UpdatedAt = time.Now()
    session.Metadata.TotalTurns++

    return m.storage.Save(ctx, session)
}

// Close 关闭会话
func (m *SessionManager) Close(ctx context.Context, sessionID string) error {
    session, err := m.Get(ctx, sessionID)
    if err != nil {
        return err
    }

    session.State = StateClosed
    session.UpdatedAt = time.Now()

    delete(m.sessions, sessionID)
    return m.storage.Save(ctx, session)
}
```

#### Chat API 接口

```go
// internal/api/chat_routes.go

package api

import (
    "context"
    "github.com/gin-gonic/gin"
    "github.com/ai-gateway/gateway/internal/runtime/chat"
)

// ChatHandler Chat API 处理器
type ChatHandler struct {
    sessionManager *chat.SessionManager
    agent          *Agent
}

// RegisterRoutes 注册路由
func (h *ChatHandler) RegisterRoutes(r *gin.RouterGroup) {
    r.POST("/chat/completions", h.ChatCompletions)
    r.POST("/chat/session", h.CreateSession)
    r.GET("/chat/session/:id", h.GetSession)
    r.POST("/chat/session/:id/messages", h.AddMessage)
    r.DELETE("/chat/session/:id", h.CloseSession)
}

// ChatCompletions 聊天补全
func (h *ChatHandler) ChatCompletions(c *gin.Context) {
    var req struct {
        SessionID string         `json:"session_id"`
        Message   types.Message  `json:"message"`
        Stream    bool           `json:"stream"`
        Options   map[string]interface{} `json:"options"`
    }

    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(400, gin.H{"error": err.Error()})
        return
    }

    ctx := c.Request.Context()

    // 获取或创建会话
    var session *chat.Session
    var err error
    if req.SessionID != "" {
        session, err = h.sessionManager.Get(ctx, req.SessionID)
        if err != nil {
            c.JSON(404, gin.H{"error": "session not found"})
            return
        }
    } else {
        // 从请求获取用户ID (从token或header)
        userID := getUserID(c)
        session, err = h.sessionManager.Create(ctx, userID)
        if err != nil {
            c.JSON(500, gin.H{"error": err.Error()})
            return
        }
    }

    // 添加用户消息到会话
    if err := h.sessionManager.AddMessage(ctx, session.ID, req.Message); err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    // 调用 Agent
    result, err := h.agent.Run(ctx, req.Message.Content, session.History)
    if err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    // 添加助手消息到会话
    assistantMsg := types.Message{
        Role:    "assistant",
        Content: result.Output,
    }
    if err := h.sessionManager.AddMessage(ctx, session.ID, assistantMsg); err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    c.JSON(200, gin.H{
        "session_id": session.ID,
        "message":    assistantMsg,
        "usage":      result.Usage,
    })
}
```

---

### 11.2 Gateway 与 Runtime 集成设计

Runtime 通过 Gateway 统一调用上游 LLM，实现模型路由、负载均衡和流量控制。

```go
// internal/runtime/llm/gateway_client.go

package llm

import (
    "context"
    "github.com/ai-gateway/gateway/internal/llm/types"
    "github.com/ai-gateway/gateway/internal/gateway/loadbalancer"
)

// GatewayClient Gateway 客户端，通过 Gateway 调用 LLM
type GatewayClient struct {
    loadBalancer *loadbalancer.Manager
    config       *GatewayClientConfig
}

// GatewayClientConfig Gateway 客户端配置
type GatewayClientConfig struct {
    ModelRouting       bool                  `yaml:"modelRouting"`
    RetryStrategy      RetryStrategy         `yaml:"retryStrategy"`
    MaxRetries         int                   `yaml:"maxRetries"`
    Timeout            time.Duration         `yaml:"timeout"`
}

// RetryStrategy 重试策略
type RetryStrategy string

const (
    RetryNone     RetryStrategy = "none"
    RetryFixed    RetryStrategy = "fixed"
    RetryExponential RetryStrategy = "exponential"
)

// NewGatewayClient 创建 Gateway 客户端
func NewGatewayClient(lb *loadbalancer.Manager, config *GatewayClientConfig) *GatewayClient {
    return &GatewayClient{
        loadBalancer: lb,
        config:       config,
    }
}

// Call 调用 LLM（通过 Gateway）
func (c *GatewayClient) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
    var lastError error

    for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
        // 1. 选择上游
        upstream, err := c.loadBalancer.Select(req.Model)
        if err != nil {
            return nil, err
        }

        // 2. 构建请求
        gwReq := c.buildGatewayRequest(*upstream, req)

        // 3. 调用 Gateway
        gwResp, err := c.callGateway(ctx, gwReq)
        if err == nil {
            return c.parseGatewayResponse(gwResp), nil
        }

        lastError = err

        // 4. 错误处理
        if !c.shouldRetry(err, attempt) {
            break
        }

        // 5. 退避等待
        c.backoff(attempt)
    }

    return nil, lastError
}

// buildGatewayRequest 构建 Gateway 请求
func (c *GatewayClient) buildGatewayRequest(upstream loadbalancer.Upstream, req *LLMRequest) *types.Request {
    return &types.Request{
        Model:       req.Model,
        Messages:    req.Messages,
        Tools:       req.Tools,
        MaxTokens:   req.MaxTokens,
        Temperature: req.Temperature,
        Stream:      req.Stream,
        Upstream:    upstream.Name,
    }
}

// Stream 流式调用
func (c *GatewayClient) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
    upstream, err := c.loadBalancer.Select(req.Model)
    if err != nil {
        return nil, err
    }

    gwReq := c.buildGatewayRequest(*upstream, req)
    ch := make(chan StreamChunk, 100)

    go func() {
        defer close(ch)
        
        stream, err := c.callGatewayStream(ctx, gwReq)
        if err != nil {
            ch <- StreamChunk{Type: "error", Content: err.Error()}
            return
        }
        
        for chunk := range stream {
            ch <- chunk
        }
    }()

    return ch, nil
}
```

#### 集成架构图

```
┌─────────────────────────────────────────────────────────────────┐
│                        AI Gateway                               │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌──────────────────┐    ┌──────────────────┐                 │
│  │  Runtime (Agent) │───▶│  Gateway Client  │                 │
│  │                  │    │                  │                 │
│  │  - Agent         │    │  - LoadBalancer  │                 │
│  │  - Skill Router  │    │  - Retry Logic   │                 │
│  │  - Executor     │    │  - Timeout       │                 │
│  └──────────────────┘    └────────┬─────────┘                 │
│                                   │                             │
│                                   ▼                             │
│  ┌─────────────────────────────────────────────────────┐        │
│  │              Gateway Core (已实现)                   │        │
│  │  ┌──────────────┐  ┌──────────────┐  ┌───────────┐  │        │
│  │  │ LoadBalancer │──│ Transformers │──│ Handlers  │  │        │
│  │  └──────────────┘  └──────────────┘  └───────────┘  │        │
│  └────────────────────┬────────────────────────────────┘        │
│                       │                                       │
│                       ▼                                       │
│  ┌─────────────────────────────────────────────────────┐        │
│  │              Upstream LLM Providers                  │        │
│  │  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌────────┐ │        │
│  │  │ OpenAI  │  │Anthropic│  │  Azure  │  │ Local  │ │        │
│  │  └─────────┘  └─────────┘  └─────────┘  └────────┘ │        │
│  └─────────────────────────────────────────────────────┘        │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

---

### 11.3 统一类型定义规范

所有共享类型定义放在 `internal/runtime/types/` 中，避免循环引用和重复定义。

```go
// internal/runtime/types/observation.go

package types

import "time"

// Observation 执行观察记录
type Observation struct {
    Step       string                 `json:"step"`
    Tool       string                 `json:"tool"`
    Input      interface{}            `json:"input"`
    Output     interface{}            `json:"output"`
    Success    bool                   `json:"success"`
    Error      string                 `json:"error,omitempty"`
    Metrics    map[string]interface{} `json:"metrics,omitempty"`
    Timestamp  time.Time              `json:"timestamp"`
}

// internal/runtime/types/request.go

package types

import (
    "context"
)

// Request 统一请求
type Request struct {
    Prompt    string                 `json:"prompt"`
    Context   map[string]interface{} `json:"context"`
    History   []Message              `json:"history"`
    Metadata  map[string]interface{} `json:"metadata"`
    Options   map[string]interface{} `json:"options"`
}

// Result 统一结果
type Result struct {
    Success      bool           `json:"success"`
    Output       string         `json:"output"`
    Skill        string         `json:"skill,omitempty"`
    Observations []Observation  `json:"observations,omitempty"`
    Usage        TokenUsage     `json:"usage,omitempty"`
    Error        string         `json:"error,omitempty"`
    Duration     Duration       `json:"duration"`
}

// internal/runtime/types/message.go

package types

import "encoding/json"

// Message 消息
type Message struct {
    Role       string       `json:"role"`
    Content    string       `json:"content"`
    ToolCalls  []ToolCall   `json:"tool_calls,omitempty"`
    ToolCallID string       `json:"tool_call_id,omitempty"`
    Metadata   Metadata     `json:"metadata,omitempty"`
}

// ToolCall 工具调用
type ToolCall struct {
    ID     string                 `json:"id"`
    Name   string                 `json:"name"`
    Args   map[string]interface{} `json:"arguments"`
}

// ToolDefinition 工具定义
type ToolDefinition struct {
    Name        string                 `json:"name"`
    Description string                 `json:"description"`
    Parameters  map[string]interface{} `json:"parameters"`
    Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// internal/runtime/types/token.go

package types

// TokenUsage Token 使用统计
type TokenUsage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
    TotalTokens      int `json:"total_tokens"`
}

// internal/runtime/types/agent.go

package types

// Duration 持续时间
type Duration struct {
    Start time.Time `json:"start"`
    End   time.Time `json:"end"`
}

// GetDuration 获取持续时间
func (d *Duration) GetDuration() time.Duration {
    return d.End.Sub(d.Start)
}

// Metadata 元数据
type Metadata map[string]interface{}

// AgentConfig Agent 配置
type AgentConfig struct {
    Name         string                 `yaml:"name"`
    Model        string                 `yaml:"model"`
    SystemPrompt string                 `yaml:"systemPrompt"`
    MaxSteps     int                    `yaml:"maxSteps"`
    EnableMemory bool                   `yaml:"enableMemory"`
    Options      map[string]interface{} `yaml:"options"`
}

// AgentState Agent 状态
type AgentState struct {
    CurrentStep int         `json:"current_step"`
    Running     bool        `json:"running"`
    Errors      []string    `json:"errors"`
}

// AgentResult Agent 结果
type AgentResult struct {
    Success      bool           `json:"success"`
    Output       string         `json:"output"`
    Steps        int            `json:"steps"`
    Observations []Observation  `json:"observations"`
    State        AgentState     `json:"state"`
}
```

---

### 11.4 可观测性设计

使用 OpenTelemetry 实现统一的 metrics、tracing 和 logging。

```go
// internal/runtime/observability/metrics.go

package observability

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics 指标
var (
    AgentExecutionsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "agent_executions_total",
            Help: "Total number of agent executions",
        },
        []string{"agent", "status"},
    )

    AgentExecutionDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "agent_execution_duration_seconds",
            Help:    "Agent execution duration in seconds",
            Buckets: prometheus.DefBuckets,
        },
        []string{"agent", "skill"},
    )

    SkillExecutionsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "skill_executions_total",
            Help: "Total number of skill executions",
        },
        []string{"skill", "status"},
    )

    ToolCallsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "tool_calls_total",
            Help: "Total number of tool calls",
        },
        []string{"tool", "mcp", "status"},
    )

    ToolCallDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "tool_call_duration_seconds",
            Help:    "Tool call duration in seconds",
            Buckets: prometheus.DefBuckets,
        },
        []string{"tool", "mcp"},
    )

    LLMTokensUsed = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_tokens_used",
            Help: "Total LLM tokens used",
        },
        []string{"model", "type"},
    )

    SessionCount = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "sessions_active",
            Help: "Number of active sessions",
        },
        []string{"state"},
    )
)

// internal/runtime/observability/tracing.go

package observability

import (
    "context"
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/codes"
    "go.opentelemetry.io/otel/trace"
)

const (
    InstrumentationName = "github.com/ai-gateway/gateway/runtime"
    TracerName         = "runtime"
)

var tracer trace.Tracer

func init() {
    tracer = otel.Tracer(TracerName)
}

// TraceAgentExecution 追踪 Agent 执行
func TraceAgentExecution(ctx context.Context, agentName, prompt string) (context.Context, trace.Span) {
    ctx, span := tracer.Start(ctx, "Agent.Run",
        trace.WithAttributes(
            attribute.String("agent.name", agentName),
            attribute.String("agent.prompt", prompt),
        ),
    )
    return ctx, span
}

// TraceSkillExecution 追踪 Skill 执行
func TraceSkillExecution(ctx context.Context, skillName string) (context.Context, trace.Span) {
    ctx, span := tracer.Start(ctx, "Skill.Execute",
        trace.WithAttributes(
            attribute.String("skill.name", skillName),
        ),
    )
    return ctx, span
}

// TraceToolCall 追踪工具调用
func TraceToolCall(ctx context.Context, toolName, mcpName string) (context.Context, trace.Span) {
    ctx, span := tracer.Start(ctx, "Tool.Call",
        trace.WithAttributes(
            attribute.String("tool.name", toolName),
            attribute.String("tool.mcp", mcpName),
        ),
    )
    return ctx, span
}

// RecordError 记录错误
func RecordError(span trace.Span, err error) {
    span.RecordError(err)
    span.SetStatus(codes.Error, err.Error())
}

// internal/runtime/observability/logging.go

package observability

import (
    "context"
    "go.uber.org/zap"
    "go.uber.org/zap/zapcore"
)

var logger *zap.Logger

func init() {
    config := zap.NewProductionConfig()
    config.EncoderConfig.TimeKey = "timestamp"
    config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
    
    var err error
    logger, err = config.Build()
    if err != nil {
        panic(err)
    }
}

// Logger 返回日志实例
func Logger() *zap.Logger {
    return logger
}

// AgentLog Agent 日志字段
func AgentLog(msg string, fields ...zap.Field) {
    logger.Info(msg, append([]zap.Field{zap.String("component", "agent")}, fields...)...)
}

// SkillLog Skill 日志字段
func SkillLog(msg string, fields ...zap.Field) {
    logger.Info(msg, append([]zap.Field{zap.String("component", "skill")}, fields...)...)
}

// ToolLog 工具日志字段
func ToolLog(msg string, fields ...zap.Field) {
    logger.Info(msg, append([]zap.Field{zap.String("component", "tool")}, fields...)...)
}

// WithLogFields 添加日志字段上下文
func WithLogFields(ctx context.Context, fields ...zap.Field) context.Context {
    // 使用 context 存储字段值
    if ctx == nil {
        ctx = context.Background()
    }
    
    // 将字段序列化到 context 中
    fieldValues := make([]interface{}, len(fields)*2)
    for i, field := range fields {
        fieldValues[i*2] = field.Key
        fieldValues[i*2+1] = field.String
    }
    
    return context.WithValue(ctx, "logFields", fieldValues)
}

// GetLogFields 从上下文获取日志字段
func GetLogFields(ctx context.Context) []zap.Field {
    if ctx == nil {
        return nil
    }
    
    values, ok := ctx.Value("logFields").([]interface{})
    if !ok {
        return nil
    }
    
    fields := make([]zap.Field, 0, len(values)/2)
    for i := 0; i < len(values); i += 2 {
        if key, ok := values[i].(string); ok {
            if value, ok := values[i+1].(string); ok {
                fields = append(fields, zap.String(key, value))
            }
        }
    }
    return fields
}
```

---

### 11.5 完整调用链示例

从用户请求到工具执行的完整流程：

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ 用户请求: "帮我查看 git 状态并提交所有更改"                                    │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 1. Chat Layer                                                              │
│    - 接收用户消息                                                           │
│    - 检查会话是否存在，不存在则创建                                         │
│    - 添加消息到会话历史                                                     │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 2. Agent Layer                                                             │
│    - 获取会话上下文和最近的历史消息                                          │
│    - 调用 Run() 方法执行 Agent                                             │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 3. Skill Router                                                            │
│    - 解析用户意图，提取关键词: "git", "commit", "更改"                        │
│    - 路由到匹配的 Skill: "git"                                             │
│    - 返回路由结果: skill=git, score=1.0, matched_by=keyword               │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 4. Skill Executor                                                          │
│    - 检查 git skill 的 workflow 定义                                       │
│    - 执行 workflow 步骤:                                                   │
│      a. check_status → toolkit_run_command("git status")                    │
│      b. show_diff → toolkit_run_command("git diff")                        │
│      c. commit → toolkit_run_command("git commit -m '...'")                │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 5. DAG Executor (Workflow 执行引擎)                                        │
│    - 构建依赖图 (check_status ← show_diff ← commit)                        │
│    - 拓扑排序执行步骤                                                       │
│    - 传递步骤间的结果                                                       │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 6. MCP Manager & Tools                                                      │
│    - 通过 MCPAdapter 调用 toolkit MCP                                      │
│    - toolkit_run_command → bash tool 执行 shell 命令                        │
│    - 返回命令输出                                                           │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 7. LLM Runtime (用于构建最终响应)                                           │
│    - 通过 Gateway Client 调用 LLM                                          │
│    - Gateway Client 使用 LoadBalancer 选择上游模型                          │
│    - 模型执行推理，生成自然语言响应                                         │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 8. 结果返回                                                                │
│    - 记录 Observations 到内存                                              │
│    - 将助手消息添加到会话历史                                              │
│    - 返回响应给用户                                                         │
│    - 更新可观测性指标 (metrics, traces, logs)                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

### 11.6 调用链路总结

完整的系统调用链路：

```
┌──────────┐    ┌──────────┐    ┌──────────────┐    ┌───────────┐
│   User   │───▶│   Chat   │───▶│    Agent     │───▶│  Runtime  │
│ Request  │    │  Layer   │    │  (ReAct)     │    │  Types    │
└──────────┘    └──────────┘    └──────────────┘    └───────────┘
                                                      │
                          ┌───────────────────────────┴─────────────┐
                          │                                                 │
          ┌───────────────▼──────────────┐  ┌───────────────────────────────┐
          │      Skill Router            │  │   Skill Executor             │
          │  (Intent → Skill Mapping)    │  │  (Workflow/Tool Execution)   │
          └───────────────┬──────────────┘  └───────────────────────────────┘
                          │                                                 │
          ┌───────────────▼──────────────┐  ┌───────────────────────────────┐
          │      DAG Executor            │  │      MCP Manager             │
          │      (Dependency Graph)      │  │    (Tool Invocation)          │
          └───────────────┬──────────────┘  └───────────────────────────────┘
                          │                                                 │
          ┌───────────────▼──────────────┐  ┌───────────────────────────────┐
          │     Gateway Client           │──│      Toolkit / Tools         │
          │  (Upstream Routing)          │  │    (Actual Execution)        │
          └───────────────┬──────────────┘  └───────────────────────────────┘
                          │
          ┌───────────────▼──────────────┐
          │     LLM Providers            │
          │  (OpenAI/Anthropic/Azure)    │
          └──────────────────────────────┘
```

---

---

### 10.10 认证授权模块

提供安全的认证和授权机制。

```go
// internal/runtime/auth/auth.go

package auth

import (
   "context"
   "crypto/hmac"
   "crypto/sha256"
   "encoding/base64"
   "fmt"
   "strings"
   "time"
)

// Authenticator 认证器接口
type Authenticator interface {
    Authenticate(ctx context.Context, token string) (*Claims, error)
    GenerateToken(ctx context.Context, userID string,ttl time.Duration) (string, error)
    ValidateToken(ctx context.Context, token string) bool
}

// Claims 认证声明
type Claims struct {
    UserID    string            `json:"user_id"`
    Username  string            `json:"username"`
    Roles     []string          `json:"roles"`
    Scopes    []string          `json:"scopes"`
    IssuedAt  time.Time         `json:"iat"`
    ExpiresAt time.Time         `json:"exp"`
    Metadata  map[string]string `json:"metadata"`
}

// JWTAuth JWT 认证实现
type JWTAuth struct {
    secretKey []byte
    issuer    string
}

// NewJWTAuth 创建 JWT 认证器
func NewJWTAuth(secretKey, issuer string) *JWTAuth {
    return &JWTAuth{
        secretKey: []byte(secretKey),
        issuer:    issuer,
    }
}

// Authenticate 验证 Token
func (a *JWTAuth) Authenticate(ctx context.Context, token string) (*Claims, error) {
    // 简化实现，实际应使用成熟的 JWT 库
    parts := strings.Split(token, ".")
    if len(parts) != 3 {
        return nil, fmt.Errorf("invalid token format")
    }

    // 验证签名
    if !a.verifySignature(parts[0], parts[1], parts[2]) {
        return nil, fmt.Errorf("invalid token signature")
    }

    // 解析 payload
    payload, err := base64.RawURLEncoding.DecodeString(parts[1])
    if err != nil {
        return nil, fmt.Errorf("invalid token payload: %w", err)
    }

    var claims Claims
    if err := json.Unmarshal(payload, &claims); err != nil {
        return nil, fmt.Errorf("invalid claims: %w", err)
    }

    // 检查过期
    if time.Now().After(claims.ExpiresAt) {
        return nil, fmt.Errorf("token expired")
    }

    return &claims, nil
}

// GenerateToken 生成 Token
func (a *JWTAuth) GenerateToken(ctx context.Context, userID string, ttl time.Duration) (string, error) {
    now := time.Now()
    claims := Claims{
        UserID:    userID,
        IssuedAt:  now,
        ExpiresAt: now.Add(ttl),
    }

    payload, _ := json.Marshal(claims)
    encodedPayload := base64.RawURLEncoding.EncodeToString(payload)

    // 生成签名
    header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
    signature := a.sign(header, encodedPayload)

    return fmt.Sprintf("%s.%s.%s", header, encodedPayload, signature), nil
}

// ValidateToken 验证 Token 有效性
func (a *JWTAuth) ValidateToken(ctx context.Context, token string) bool {
    _, err := a.Authenticate(ctx, token)
    return err == nil
}

// sign 签名
func (a *JWTAuth) sign(header, payload string) string {
    h := hmac.New(sha256.New, a.secretKey)
    h.Write([]byte(header + "." + payload))
    return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// verifySignature 验证签名
func (a *JWTAuth) verifySignature(header, payload, signature string) bool {
    expectedSig := a.sign(header, payload)
    return hmac.Equal([]byte(signature), []byte(expectedSig))
}

// Authorization 授权管理器
type Authorization struct {
    policies map[string][]Policy
}

// Policy 策略
type Policy struct {
    Resource string   `json:"resource"`
    Actions  []string `json:"actions"`
    Effect   string   `json:"effect"` // "allow" | "deny"
}

// NewAuthorization 创建授权管理器
func NewAuthorization() *Authorization {
    return &Authorization{
        policies: make(map[string][]Policy),
    }
}

// AddPolicy 添加策略
func (a *Authorization) AddPolicy(role string, policy Policy) {
    a.policies[role] = append(a.policies[role], policy)
}

// CheckPermission 检查权限
func (a *Authorization) CheckPermission(userRole, resource, action string) bool {
    policies, ok := a.policies[userRole]
    if !ok {
        return false
    }

    for _, policy := range policies {
        if a.matchResource(policy.Resource, resource) {
            for _, allowedAction := range policy.Actions {
                if allowedAction == "*" || allowedAction == action {
                    return policy.Effect == "allow"
                }
            }
        }
    }

    return false
}

// matchResource 匹配资源
func (a *Authorization) matchResource(pattern, resource string) bool {
    if pattern == "*" {
        return true
    }
    return pattern == resource
}
```

---

### 10.11 错误处理和重试策略

统一的错误分类和重试机制。

```go
// internal/runtime/errors/errors.go

package errors

import (
    "errors"
    "fmt"
    "time"
)

// ErrorCode 错误码
type ErrorCode string

const (
    // 网络错误
    ErrNetworkTimeout    ErrorCode = "NETWORK_TIMEOUT"
    ErrNetworkUnavailable ErrorCode = "NETWORK_UNAVAILABLE"

    // API 错误
    ErrAPIRateLimit     ErrorCode = "API_RATE_LIMIT"
    ErrAPIUnauthorized   ErrorCode = "API_UNAUTHORIZED"
    ErrAPINotFound      ErrorCode = "API_NOT_FOUND"
    ErrAPIBadRequest    ErrorCode = "API_BAD_REQUEST"
    ErrAPIServerError   ErrorCode = "API_SERVER_ERROR"

    // 工具错误
    ErrToolNotFound     ErrorCode = "TOOL_NOT_FOUND"
    ErrToolExecution    ErrorCode = "TOOL_EXECUTION"
    ErrToolTimeout      ErrorCode = "TOOL_TIMEOUT"

    // Agent 错误
    ErrAgentMaxSteps    ErrorCode = "AGENT_MAX_STEPS"
    ErrAgentPermission  ErrorCode = "AGENT_PERMISSION"

    // 内存错误
    ErrMemoryFull       ErrorCode = "MEMORY_FULL"

    // Workflow 错误
    ErrWorkflowCycle    ErrorCode = "WORKFLOW_CYCLE"
    ErrWorkflowStep     ErrorCode = "WORKFLOW_STEP"
)

// RuntimeError 运行时错误
type RuntimeError struct {
    Code    ErrorCode
    Message string
    Cause   error
    Context map[string]interface{}
}

// Error 实现 error 接口
func (e *RuntimeError) Error() string {
    if e.Cause != nil {
        return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
    }
    return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap 支持错误解包
func (e *RuntimeError) Unwrap() error {
    return e.Cause
}

// Wrap 包装错误
func Wrap(code ErrorCode, message string, cause error) *RuntimeError {
    return &RuntimeError{
        Code:    code,
        Message: message,
        Cause:   cause,
    }
}

// WrapWithContext 包装错误并添加上下文
func WrapWithContext(code ErrorCode, message string, cause error, ctx map[string]interface{}) *RuntimeError {
    return &RuntimeError{
        Code:    code,
        Message: message,
        Cause:   cause,
        Context: ctx,
    }
}

// Is 检查错误类型
func Is(err error, code ErrorCode) bool {
    var runtimeErr *RuntimeError
    if errors.As(err, &runtimeErr) {
        return runtimeErr.Code == code
    }
    return false
}

// Retrier 重试器
type Retrier struct {
    maxAttempts int
    strategy    RetryStrategy
    maxWait     time.Duration
}

// RetryStrategy 重试策略
type RetryStrategy interface {
    ShouldRetry(error) bool
    WaitTime(attempt int) time.Duration
}

// ExponentialBackoff 指数退避
type ExponentialBackoff struct {
    baseDelay time.Duration
    maxDelay  time.Duration
    factor    float64
}

// NewExponentialBackoff 创建指数退避策略
func NewExponentialBackoff(baseDelay, maxDelay time.Duration, factor float64) *ExponentialBackoff {
    return &ExponentialBackoff{
        baseDelay: baseDelay,
        maxDelay:  maxDelay,
        factor:    factor,
    }
}

// ShouldRetry 是否应该重试
func (s *ExponentialBackoff) ShouldRetry(err error) bool {
    var runtimeErr *RuntimeError
    if errors.As(err, &runtimeErr) {
        switch runtimeErr.Code {
        case ErrNetworkTimeout, ErrAPIServerError, ErrAPIRateLimit:
            return true
        }
    }
    return false
}

// WaitTime 计算等待时间
func (s *ExponentialBackoff) WaitTime(attempt int) time.Duration {
    delay := time.Duration(float64(s.baseDelay) * (1 << uint(attempt-1)) * s.factor)
    if delay > s.maxDelay {
        return s.maxDelay
    }
    return delay
}

// Retry 带重试的执行
func (r *Retrier) Retry(fn func() error) error {
    var lastErr error

    for attempt := 1; attempt <= r.maxAttempts; attempt++ {
        err := fn()
        if err == nil {
            return nil
        }

        lastErr = err

        // 检查是否应该重试
        if !r.strategy.ShouldRetry(err) {
            return err
        }

        // 最后一次尝试不等待
        if attempt == r.maxAttempts {
            break
        }

        // 等待重试
        waitTime := r.strategy.WaitTime(attempt)
        if waitTime > r.maxWait {
            waitTime = r.maxWait
        }
        time.Sleep(waitTime)
    }

    return fmt.Errorf("max retry attempts exceeded: %w", lastErr)
}

// NewRetrier 创建重试器
func NewRetrier(maxAttempts int, strategy RetryStrategy, maxWait time.Duration) *Retrier {
    return &Retrier{
        maxAttempts: maxAttempts,
        strategy:    strategy,
        maxWait:     maxWait,
    }
}

// 环境变量处理

// GetEnvWithDefault 获取环境变量，带默认值
func GetEnvWithDefault(key, defaultValue string) string {
    if value := os.Getenv(key); value != "" {
        return value
    }
    return defaultValue
}

// GetEnvAsInt 获取环境变量作为整数
func GetEnvAsInt(key string, defaultValue int) int {
    if value := os.Getenv(key); value != "" {
        if intValue, err := strconv.Atoi(value); err == nil {
            return intValue
        }
    }
    return defaultValue
}

// GetEnvAsDuration 获取环境变量作为时长
func GetEnvAsDuration(key string, defaultValue time.Duration) time.Duration {
    if value := os.Getenv(key); value != "" {
        if duration, err := time.ParseDuration(value); err == nil {
            return duration
        }
    }
    return defaultValue
}

// GetEnvAsBool 获取环境变量作为布尔值
func GetEnvAsBool(key string, defaultValue bool) bool {
    if value := os.Getenv(key); value != "" {
        if boolValue, err := strconv.ParseBool(value); err == nil {
            return boolValue
        }
    }
    return defaultValue
}

// 资源限制和配额管理

// RateLimiter 速率限制器（简化版）
type RateLimiter struct {
    tokens chan struct{}
    rate   int
}

// NewRateLimiter 创建速率限制器
func NewRateLimiter(rate int) *RateLimiter {
    limiter := &RateLimiter{
        tokens: make(chan struct{}, rate),
        rate:   rate,
    }
    
    // 初始填充 tokens
    for i := 0; i < rate; i++ {
        limiter.tokens <- struct{}{}
    }
    
    // 启动 token 补充
    go limiter.refill()
    
    return limiter
}

// refill 补充 tokens
func (r *RateLimiter) refill() {
    ticker := time.NewTicker(time.Second)
    defer ticker.Stop()
    
    for range ticker.C {
        select {
        case r.tokens <- struct{}{}: default:
        }
    }
}

// Wait 等待可用 token
func (r *RateLimiter) Wait(ctx context.Context) error {
    select {
    case <-r.tokens:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}

// UsageTracker 使用量跟踪器
type UsageTracker struct {
    mu          sync.RWMutex
    usage       map[string]*UserUsage
    limits      map[string]*UsageLimit
}

// UserUsage 用户使用量
type UserUsage struct {
    UserID      string
    TokenCount  int
    RequestCount int
    ResetTime   time.Time
}

// UsageLimit 使用量限制
type UsageLimit struct {
    MaxTokens   int
    MaxRequests int
    WindowDays  int
}

// NewUsageTracker 创建使用量跟踪器
func NewUsageTracker() *UsageTracker {
    return &UsageTracker{
        usage:  make(map[string]*UserUsage),
        limits: make(map[string]*UsageLimit),
    }
}

// TrackRequest 跟踪请求
func (t *UsageTracker) TrackRequest(userID string, tokens int) error {
    t.mu.Lock()
    defer t.mu.Unlock()

    usage, ok := t.usage[userID]
    if !ok {
        usage = &UserUsage{
            UserID:     userID,
            ResetTime:  time.Now().AddDate(0, 0, 7), // 7天窗口
        }
        t.usage[userID] = usage
    }

    // 检查重置
    if time.Now().After(usage.ResetTime) {
        usage.TokenCount = 0
        usage.RequestCount = 0
        usage.ResetTime = time.Now().AddDate(0, 0, 7)
    }

    // 检查限制
    limit := t.limits[userID]
    if limit != nil {
        if usage.TokenCount+tokens > limit.MaxTokens {
            return Wrap(ErrAPIRateLimit, "token limit exceeded", nil)
        }
        if usage.RequestCount+1 > limit.MaxRequests {
            return Wrap(ErrAPIRateLimit, "request limit exceeded", nil)
        }
    }

    usage.TokenCount += tokens
    usage.RequestCount++

    return nil
}

// GetUsage 获取使用量
func (t *UsageTracker) GetUsage(userID string) (*UserUsage, error) {
    t.mu.RLock()
    defer t.mu.RUnlock()

    usage, ok := t.usage[userID]
    if !ok {
        return nil, fmt.Errorf("usage not found for user: %s", userID)
    }
    return usage, nil
}

// SetLimit 设置限制
func (t *UsageTracker) SetLimit(userID string, limit *UsageLimit) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.limits[userID] = limit
}
```

---

### 10.12 配置验证和管理

```go
// internal/runtime/config/validator.go

package config

import (
   "fmt"
   "reflect"
   "regexp"
   "strconv"
)

// Validator 配置验证器
type Validator struct {
    rules []ValidationRule
}

// ValidationRule 验证规则
type ValidationRule struct {
    Field      string
    Required   bool
    Type       string
    Min        *float64
    Max        *float64
    Pattern    string
   Allowed    []string
   CustomFunc func(interface{}) error
}

// NewValidator 创建验证器
func NewValidator() *Validator {
    return &Validator{rules: make([]ValidationRule, 0)}
}

// AddRule 添加验证规则
func (v *Validator) AddRule(rule ValidationRule) {
    v.rules = append(v.rules, rule)
}

// Validate 验证配置
func (v *Validator) Validate(config interface{}) error {
    val := reflect.ValueOf(config)
    if val.Kind() == reflect.Ptr {
        val = val.Elem()
    }

    if val.Kind() != reflect.Struct {
        return fmt.Errorf("config must be a struct or pointer to struct")
    }

    for _, rule := range v.rules {
        field := val.FieldByName(rule.Field)
        if !field.IsValid() {
            return fmt.Errorf("field not found: %s", rule.Field)
        }

        // 检查必填
        if rule.Required && field.IsZero() {
            return fmt.Errorf("field %s is required", rule.Field)
        }

        // 跳过空值
        if field.IsZero() {
            continue
        }

        // 类型检查
        if rule.Type != "" {
            if err := v.checkType(field, rule.Type); err != nil {
                return fmt.Errorf("field %s: %w", rule.Field, err)
            }
        }

        // 范围检查
        if rule.Min != nil || rule.Max != nil {
            if err := v.checkRange(field, rule.Min, rule.Max); err != nil {
                return fmt.Errorf("field %s: %w", rule.Field, err)
            }
        }

        // 模式检查
        if rule.Pattern != "" {
            if err := v.checkPattern(field, rule.Pattern); err != nil {
                return fmt.Errorf("field %s: %w", rule.Field, err)
            }
        }

        // 允许值检查
        if len(rule.Allowed) > 0 {
            if err := v.checkAllowed(field, rule.Allowed); err != nil {
                return fmt.Errorf("field %s: %w", rule.Field, err)
            }
        }

        // 自定义验证
        if rule.CustomFunc != nil {
            if err := rule.CustomFunc(field.Interface()); err != nil {
                return fmt.Errorf("field %s: %w", rule.Field, err)
            }
        }
    }

    return nil
}

// checkType 检查类型
func (v *Validator) checkType(field reflect.Value, expectedType string) error {
    var actualType string
    switch field.Kind() {
    case reflect.String:
        actualType = "string"
    case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
        actualType = "int"
    case reflect.Float32, reflect.Float64:
        actualType = "float"
    case reflect.Bool:
        actualType = "bool"
    case reflect.Slice, reflect.Array:
        actualType = "array"
    case reflect.Map:
        actualType = "object"
    default:
        actualType = "unknown"
    }

    if actualType != expectedType {
        return fmt.Errorf("expected type %s, got %s", expectedType, actualType)
    }
    return nil
}

// checkRange 检查数值范围
func (v *Validator) checkRange(field reflect.Value, min, max *float64) error {
    var value float64
    switch field.Kind() {
    case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
        value = float64(field.Int())
    case reflect.Float32, reflect.Float64:
        value = field.Float()
    default:
        return fmt.Errorf("field is not numeric")
    }

    if min != nil && value < *min {
        return fmt.Errorf("value %v is less than minimum %v", value, *min)
    }
    if max != nil && value > *max {
        return fmt.Errorf("value %v is greater than maximum %v", value, *max)
    }

    return nil
}

// checkPattern 检查字符串模式
func (v *Validator) checkPattern(field reflect.Value, pattern string) error {
    if field.Kind() != reflect.String {
        return fmt.Errorf("field is not a string")
    }

    str := field.String()
    matched, err := regexp.MatchString(pattern, str)
    if err != nil {
        return fmt.Errorf("invalid pattern: %w", err)
    }

    if !matched {
        return fmt.Errorf("value %q does not match pattern %q", str, pattern)
    }

    return nil
}

// checkAllowed 检查允许值
func (v *Validator) checkAllowed(field reflect.Value, allowed []string) error {
    var str string

    switch field.Kind() {
    case reflect.String:
        str = field.String()
    case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
        str = strconv.FormatInt(field.Int(), 10)
    case reflect.Float32, reflect.Float64:
        str = strconv.FormatFloat(field.Float(), 'f', -1, 64)
    case reflect.Bool:
        str = strconv.FormatBool(field.Bool())
    default:
        return fmt.Errorf("field type not supported for allowed values check")
    }

    for _, allowedValue := range allowed {
        if str == allowedValue {
            return nil
        }
    }

    return fmt.Errorf("value %q is not in allowed list %v", str, allowed)
}
```

---

## 十二、测试策略

### 12.1 单元测试框架

```go
// internal/runtime/testing/mocks.go

package testing

import (
   "context"
   "github.com/ai-gateway/gateway/internal/runtime/types"
)

// MockMCPManager MCP 管理器 Mock
type MockMCPManager struct {
    foundTools   map[string]*ToolInfo
    toolOutputs  map[string]interface{}
    callHistory  []ToolCallRecord
}

type ToolCallRecord struct {
    MCPName  string
    ToolName string
    Args     map[string]interface{}
    Result   interface{}
}

func NewMockMCPManager() *MockMCPManager {
    return &MockMCPManager{
        foundTools:  make(map[string]*ToolInfo),
        toolOutputs: make(map[string]interface{}),
        callHistory: make([]ToolCallRecord, 0),
    }
}

func (m *MockMCPManager) FindTool(toolName string) (*ToolInfo, error) {
    if tool, ok := m.foundTools[toolName]; ok {
        return tool, nil
    }
    return nil, fmt.Errorf("tool not found: %s", toolName)
}

func (m *MockMCPManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
    record := ToolCallRecord{
        MCPName:  mcpName,
        ToolName: toolName,
        Args:     args,
    }
    
    if output, ok := m.toolOutputs[toolName]; ok {
        record.Result = output
    }
    
    m.callHistory = append(m.callHistory, record)
    
    return m.toolOutputs[toolName], nil
}

func (m *MockMCPManager) ListTools() []*ToolInfo {
    tools := make([]*ToolInfo, 0, len(m.foundTools))
    for _, tool := range m.foundTools {
        tools = append(tools, tool)
    }
    return tools
}

func (m *MockMCPManager) AddTool(toolInfo *ToolInfo, output interface{}) {
    m.foundTools[toolInfo.Name] = toolInfo
    if output != nil {
        m.toolOutputs[toolInfo.Name] = output
    }
}

func (m *MockMCPManager) GetCallHistory() []ToolCallRecord {
    return m.callHistory
}

// MockLLMClient LLM 客户端 Mock
type MockLLMClient struct {
    responses map[string]*types.Result
    calls     int
}

func NewMockLLMClient() *MockLLMClient {
    return &MockLLMClient{
        responses: make(map[string]*types.Result),
    }
}

func (m *MockLLMClient) SetResponse(prompt string, result *types.Result) {
    m.responses[prompt] = result
}

func (m *MockLLMClient) Call(ctx context.Context, prompt string) (*types.Result, error) {
    m.calls++
    
    // 精确匹配
    if result, ok := m.responses[prompt]; ok {
        return result, nil
    }
    
    // 前缀匹配
    for key, result := range m.responses {
        if strings.HasPrefix(prompt, key) {
            return result, nil
        }
    }
    
    return &types.Result{
        Success: true,
        Output:  "Mock LLM response",
    }, nil
}

func (m *MockLLMClient) GetCallCount() int {
    return m.calls
}

// 测试辅助函数

// AssertToolCalled 断言工具被调用
func AssertToolCalled(t *testing.T, mock *MockMCPManager, toolName string) bool {
    for _, record := range mock.GetCallHistory() {
        if record.ToolName == toolName {
            return true
        }
    }
    t.Errorf("Tool %s was not called", toolName)
    return false
}

// AssertToolCalledWithArgs 断言工具被调用并使用特定参数
func AssertToolCalledWithArgs(t *testing.T, mock *MockMCPManager, toolName string, args map[string]interface{}) bool {
    for _, record := range mock.GetCallHistory() {
        if record.ToolName == toolName {
            if reflect.DeepEqual(record.Args, args) {
                return true
            }
        }
    }
    t.Errorf("Tool %s was not called with expected args", toolName)
    return false
}
```

### 12.2 集成测试示例

```go
// internal/runtime/integration_test.go

package runtime_test

import (
   "context"
   "testing"
   "time"
   
   "github.com/ai-gateway/gateway/internal/runtime/agent"
   "github.com/ai-gateway/gateway/internal/runtime/skill"
   "github.com/ai-gateway/gateway/internal/runtime/testing"
)

func TestAgentEndToEnd(t *testing.T) {
    // 创建 Mock MCP Manager
    mockMCP := testing.NewMockMCPManager()
    mockMCP.AddTool(&tool.ToolInfo{
       Name: "read_file",
       Description: "Read file contents",
    }, "file content here")

    // 创建 Agent
    cfg := &agent.Config{
       MaxSteps:       5,
       DefaultModel:   "gpt-4",
       EnableMemory:   true,
       EnablePlanning: false,
       SystemPrompt:   "You are a helpful assistant",
    }

    a := agent.NewAgent(cfg, mockMCP)

    // 加载 Skills
    if err := a.LoadSkills("./test/skills"); err != nil {
       t.Fatalf("Failed to load skills: %v", err)
    }

    // 测试简单请求
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    result, err := a.Run(ctx, "Read the file README.md")
    if err != nil {
       t.Fatalf("Agent run failed: %v", err)
    }

    if !result.Success {
       t.Errorf("Agent execution should succeed")
    }

    // 验证工具调用
    if !testing.AssertToolCalled(t, mockMCP, "read_file") {
       t.Error("read_file tool should be called")
    }
}

func TestSkillWorkflow(t *testing.T) {
    // 创建测试 Skill
    testSkill := &skill.Skill{
       Name: "test_workflow",
       Triggers: []skill.Trigger{
          {
             Type:   "keyword",
             Values: []string{"test"},
             Weight: 1.0,
          },
       },
       Tools: []string{"tool_a", "tool_b"},
       Workflow: &skill.Workflow{
          Steps: []skill.WorkflowStep{
             {
                ID:   "step1",
                Tool: "tool_a",
                Args: map[string]interface{}{"arg": "value"},
             },
             {
                ID:        "step2",
                Tool:      "tool_b",
                Args:      map[string]interface{}{"arg": "value"},
                DependsOn: []string{"step1"},
             },
          },
       },
    }

    mockMCP := testing.NewMockMCPManager()
    mockMCP.AddTool(&tool.ToolInfo{Name: "tool_a"}, {"result": "a"})
    mockMCP.AddTool(&tool.ToolInfo{Name: "tool_b"}, {"result": "b"})

    registry := skill.NewRegistry(mockMCP)
    if err := registry.Register(testSkill); err != nil {
       t.Fatalf("Failed to register skill: %v", err)
    }

    executor := skill.NewExecutor(registry, mockMCP)
    req := &types.Request{
       Prompt: "test",
    }

    result, err := executor.Execute(context.Background(), testSkill, req)
    if err != nil {
       t.Fatalf("Execution failed: %v", err)
    }

    if !result.Success {
       t.Errorf("Workflow execution should succeed")
    }

    // 验证依赖顺序
    history := mockMCP.GetCallHistory()
    if len(history) < 2 {
       t.Error("Both steps should be executed")
    }

    if history[0].ToolName != "tool_a" {
       t.Error("First tool should be tool_a")
    }

    if history[1].ToolName != "tool_b" {
       t.Error("Second tool should be tool_b")
    }
}
```

### 12.3 性能测试

```go
// internal/runtime/benchmark_test.go

package runtime_test

import (
   "context"
   "testing"
   "time"
)

func BenchmarkAgentExecution(b *testing.B) {
    mockMCP := testing.NewMockMCPManager()
    mockLLM := testing.NewMockLLMClient()
    
    cfg := &agent.Config{
       MaxSteps:     3,
       DefaultModel: "gpt-4",
    }
    
    a := agent.NewAgent(cfg, mockMCP)
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
       a.Run(context.Background(), "test prompt")
    }
}

func BenchmarkSkillRouting(b *testing.B) {
    registry := skill.NewRegistry(mockMCP)
    router := skill.NewRouter(registry)
    
    // 注册 100 个 skills
    for i := 0; i < 100; i++ {
       s := &skill.Skill{
          Name: fmt.Sprintf("skill_%d", i),
          Triggers: []skill.Trigger{
             {
                Type:   "keyword",
                Values: []string{fmt.Sprintf("keyword_%d", i)},
                Weight: 1.0,
             },
          },
       }
       registry.Register(s)
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
       router.Route(context.Background(), "keyword_50")
    }
}
```

---

## 十三、部署和运维

### 13.1 Docker 部署

```dockerfile
# Dockerfile
FROM golang:1.21-alpine AS builder

WORKDIR /app

# 复制依赖文件
COPY go.* ./
RUN go mod download

# 复制源代码
COPY . .

# 构建
RUN CGO_ENABLED=0 GOOS=linux go build -o ai-gateway ./cmd/server

# 运行时镜像
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app

# 配置文件
COPY config/ ./config/

# 二进制文件
COPY --from=builder /app/ai-gateway .

# 暴露端口
EXPOSE 8080 9090

# 启动
CMD ["./ai-gateway", "--config", "./config/config.yaml"]
```

### 13.2 Docker Compose

```yaml
# docker-compose.yml
version: '3.8'

services:
  ai-gateway:
    build: .
    ports:
      - "8080:8080"  # API 端口
      - "9090:9090"  # Metrics 端口
    environment:
      - GATEWAY_CONFIG=/app/config/config.yaml
      - OPENAI_API_KEY=${OPENAI_API_KEY}
      - ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY}
    volumes:
      - ./config:/app/config
      - ./skills:/app/skills
      - ./logs:/app/logs
    depends_on:
      - postgres
      - redis

  postgres:
    image: postgres:16-alpine
    environment:
      - POSTGRES_DB=aigateway
      - POSTGRES_USER=aigateway
      - POSTGRES_PASSWORD=aigateway
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"

  redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data
    ports:
      - "6379:6379"

  prometheus:
    image: prom/prometheus
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml
      - prometheus_data:/prometheus
    ports:
      - "9091:9090"
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--storage.tsdb.path=/prometheus'

  grafana:
    image: grafana/grafana
    ports:
      - "3000:3000"
    volumes:
      - grafana_data:/var/lib/grafana

volumes:
  postgres_data:
  redis_data:
  prometheus_data:
  grafana_data:
```

### 13.3 Kubernetes 部署

```yaml
# k8s/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ai-gateway
spec:
  replicas: 3
  selector:
    matchLabels:
      app: ai-gateway
  template:
    metadata:
      labels:
        app: ai-gateway
    spec:
      containers:
      - name: ai-gateway
        image: ai-gateway:latest
        ports:
        - containerPort: 8080
          name: http
        - containerPort: 9090
          name: metrics
        env:
        - name: OPENAI_API_KEY
          valueFrom:
            secretKeyRef:
              name: ai-gateway-secrets
              key: openai-api-key
        resources:
          requests:
            memory: "512Mi"
            cpu: "500m"
          limits:
            memory: "2Gi"
            cpu: "2"
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /ready
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5
        volumeMounts:
        - name: config
          mountPath: /app/config
      volumes:
      - name: config
        configMap:
          name: ai-gateway-config
---
apiVersion: v1
kind: Service
metadata:
  name: ai-gateway
spec:
  selector:
    app: ai-gateway
  ports:
  - name: http
    port: 80
    targetPort: 8080
  - name: metrics
    port: 9090
    targetPort: 9090
  type: LoadBalancer
```

---

## 十四、监控和告警

### 14.1 Grafana 仪表板配置

```json
{
  "dashboard": {
    "title": "AI Gateway Runtime",
    "panels": [
      {
        "title": "Agent Executions",
        "targets": [
          {
            "expr": "rate(agent_executions_total[5m])"
          }
        ]
      },
      {
        "title": "Tool Call Duration",
        "targets": [
          {
            "expr": "histogram_quantile(0.95, rate(tool_call_duration_seconds_bucket[5m]))"
          }
        ]
      },
      {
        "title": "Active Sessions",
        "targets": [
          {
            "expr": "sessions_active"
          }
        ]
      },
      {
        "title": "LLM Token Usage",
        "targets": [
          {
            "expr": "rate(llm_tokens_used[5m])"
          }
        ]
      }
    ]
  }
}
```

### 14.2 告警规则

```yaml
# prometheus-alerts.yml
groups:
- name: ai-gateway-alerts
  rules:
  - alert: HighErrorRate
    expr: rate(agent_executions_total{status="error"}[5m]) > 0.1
    for: 5m
    annotations:
      summary: "High error rate in agent executions"
  
  - alert: SlowToolCalls
    expr: histogram_quantile(0.95, rate(tool_call_duration_seconds_bucket[5m])) > 30
    for: 10m
    annotations:
      summary: "Tool calls are taking too long"
  
  - alert: HighTokenUsage
    expr: rate(llm_tokens_used[1h]) > 10000
    for: 5m
    annotations:
      summary: "High LLM token usage rate"
```

---

## 十五、总结与建议

### 15.1 设计方案概览

本设计方案基于已有的 MCP 实现构建 Skills Runtime，核心设计包括：

1. **Skill 系统**: 可扩展的技能定义、加载、路由和执行
2. **Agent Runtime**: ReAct 循环、记忆系统、计划器
3. **智能增强**: 语义搜索、工作空间理解、Token 预算管理
4. **与 MCP 集成**: 复用现有 MCP 工具作为 Skill 的能力来源
5. **高级特性**: Multi-Agent 协作、热加载、流式执行、安全沙箱
6. **基础设施**: 认证授权、错误处理、监控告警、部署运维

### 15.2 已修复的问题

在本次更新中，已修复以下设计文档中的问题：

#### 代码示例修复
- ✅ 修复类型引用错误 (`skill.Request` → `types.Request`)
- ✅ 修复方法调用错误 (`RunWithHistory` → `Run`)
- ✅ 补充缺失类型定义 (`Duration`, `Metadata`)
- ✅ 完善流式调用实现
- ✅ 完善日志字段上下文实现

#### 设计补充
- ✅ 补充 Tokenizer 具体实现
- ✅ 补充 OpenAI Provider 实现
- ✅ 补充 Anthropic Provider 实现
- ✅ 补充 SessionStorage 接口和实现
- ✅ 新增认证授权模块设计
- ✅ 新增错误处理和重试策略
- ✅ 新增配置验证器设计
- ✅ 新增测试框架和 Mock 对象
- ✅ 新增部署配置（Docker/Kubernetes）
- ✅ 新增监控告警配置

#### 文档完善
- ✅ 更新实现路线图，细化 10 个阶段
- ✅ 添加里程碑和时间规划（见第九章）
- ✅ 添加团队分工建议
- ✅ 添加技术栈说明
- ✅ 添加风险和缓解措施分析

### 15.3 核心优势

| 优势 | 说明 |
|------|------|
| **架构清晰** | gateway/runtime 并行架构，职责明确 |
| **类型安全** | 统一的 types/ 模块，避免循环引用 |
| **可扩展** | 基于接口设计，易于添加新 Provider/Skill |
| **高性能** | 并行执行、缓存策略、流式处理 |
| **可观测** | 完整的 Metrics/Tracing/Logging |
| **安全可靠** | Sandbox、权限控制、重试机制 |
| **生产就绪** | 完整的部署、监控、告警方案 |

### 15.4 实施建议

#### 短期（1-2 个月）
1. 优先完成 Phase 1-3（基础框架 + 执行引擎 + 智能增强）
2. 建立基本的测试框架
3. 实现核心的 Agent/Skill 功能

#### 中期（3-4 个月）
4. 完成 Phase 4-6（集成优化 + 高级特性 + 基础设施）
5. 引入 LLM Provider 和 Gateway 集成
6. 添加认证授权和监控

#### 长期（5-6 个月）
7. 完成 Phase 7-10（持久化 + 测试 + 运维）
8. 性能优化和压测
9. 生产环境部署和稳定性建设

### 15.5 关键成功因素

1. **模块化设计**: 保持接口清晰，避免耦合
2. **增量开发**: 每个 phase 都有可交付的成果
3. **充分测试**: 单元测试 + 集成测试 + 性能测试
4. **文档同步**: 代码和文档保持同步更新
5. **监控先行**: 尽早建立监控和告警
6. **安全第一**: 从设计阶段就考虑安全性和权限控制

### 15.6 待优化项

虽然设计已经比较完整，但仍有一些方面可以在后续迭代中优化：

| 优化项 | 当前状态 | 改进方向 |
|--------|----------|----------|
| Plugin 系统 | 未设计 | 支持第三方插件扩展 |
| 事件总线 | 简单提及 | 实现完整的事件驱动架构 |
| 分布式执行 | 未提及 | 支持跨节点的 Agent 协作 |
| 上下文压缩 | 基础实现 | 更智能的记忆压缩算法 |
| 性能基准 | 未设定 | 建立性能基准和目标 |
| 竞品对比 | 未提及 | 详细的功能和性能对比 |

### 15.7 参考资源

**设计灵感来源**
- Claude Code / Cursor: Multi-Agent 协作、自我反思
- AutoGPT: 任务规划和执行
- LangChain: Chain 和 Agent 抽象
- OpenAI Tool Use: 工具调用协议

**相关标准**
- [MCP Specification](https://modelcontextprotocol.io/)
- [OpenAI API](https://platform.openai.com/docs/api-reference)
- [Anthropic Messages API](https://docs.anthropic.com/claude/reference/messages_post)
- [OpenTelemetry](https://opentelemetry.io/)

**最佳实践**
- Go 最佳实践: [Effective Go](https://go.dev/doc/effective_go)
- API 设计: [RESTful API Design](https://restfulapi.net/)
- 测试策略: [Table-Driven Tests](https://go.dev/wiki/TableDrivenTests)

---

## 附录：快速开始

### A.1 环境准备

```bash
# 安装 Go 1.21+
wget https://go.dev/dl/go1.21.0.linux-amd64.tar.gz
tar -C /usr/local -xzf go1.21.0.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin

# 克隆仓库
git clone https://github.com/wwsheng009/ai-agent-runtime.git
cd ai-agent-runtime/backend

# 安装依赖
go mod download
```

### A.2 配置文件

```yaml
# config/config.yaml
agent:
  maxSteps: 10
  defaultModel: "claude-3-5-sonnet"
  enableMemory: true
  
skills:
  dir: "./skills"
  autoLoad: true
  hotReload: true

mcp:
  configPath: "./config/mcp.yaml"

llm:
  providers:
    - name: "openai"
      apiKey: "${OPENAI_API_KEY}"
    - name: "anthropic"  
      apiKey: "${ANTHROPIC_API_KEY}"

monitoring:
  enabled: true
  metricsPort: 9090
```

### A.3 运行服务

```bash
# 构建二进制文件
go build -o ai-gateway ./cmd/server

# 运行服务
./ai-gateway --config ./config/config.yaml

# 或使用 Docker
docker-compose up -d
```

### A.4 测试 API

```bash
# 创建会话
curl -X POST http://localhost:8080/chat/session \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{}'

# 发送消息
curl -X POST http://localhost:8080/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "session_id": "xxx",
    "message": {
      "role": "user",
      "content": "帮我查看 git 状态"
    }
  }'
```

---

**文档版本**: v2.1  
**最后更新**: 2025-03-07  
**维护者**: AI Gateway Team（historical）  
**状态**: 历史设计稿，仅供对照
