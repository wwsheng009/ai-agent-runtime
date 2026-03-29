# ai-agent-runtime 迁移文档

> 从 `ai-gateway` 提取 multi-agent 执行子系统为独立项目。

---

## 1. 目标项目目录结构

```
ai-agent-runtime/
├── backend/                        # Go 后端（独立 Go module）
│   ├── go.mod                      # module github.com/ai-gateway/ai-agent-runtime
│   ├── go.sum
│   ├── cmd/
│   │   ├── server/                 # HTTP/WebSocket API 服务入口
│   │   │   └── main.go
│   │   └── aicli/                  # 命令行工具（从 ai-gateway/cmd/aicli 迁移）
│   │       └── main.go
│   └── internal/
│       ├── agent/                  # ← internal/runtime/agent
│       │   ├── agent.go
│       │   ├── loop.go
│       │   ├── orchestrator.go
│       │   ├── planner.go
│       │   ├── scheduler.go
│       │   ├── tool_broker.go
│       │   ├── tool_policy.go
│       │   ├── permission_engine.go
│       │   ├── hook_manager.go
│       │   ├── message_builder.go
│       │   ├── prompt_builder.go
│       │   ├── role_defaults.go
│       │   ├── subagent_plan.go
│       │   ├── approved_tool.go
│       │   └── checkpoint_manager.go
│       │
│       ├── team/                   # ← internal/team
│       │
│       ├── llm/                    # ← internal/runtime/llm
│       │   ├── adapter/
│       │   └── balancer.go         # 新增：ProviderBalancer 接口（解耦 loadbalancer）
│       │
│       ├── skill/                  # ← internal/runtime/skill
│       ├── toolkit/                # ← internal/toolkit
│       │   └── tools/
│       │
│       ├── mcp/                    # ← internal/mcp
│       │   ├── protocol/
│       │   ├── transport/
│       │   ├── client/
│       │   ├── server/
│       │   ├── registry/
│       │   ├── catalog/
│       │   ├── config/
│       │   └── manager/
│       │
│       ├── chat/                   # ← internal/runtime/chat
│       ├── chatcore/               # ← internal/runtime/chatcore
│       ├── workspace/              # ← internal/runtime/workspace
│       ├── contextpack/            # ← internal/runtime/contextpack
│       │
│       ├── executor/               # ← internal/runtime/executor
│       ├── embedding/              # ← internal/runtime/embedding
│       ├── memory/                 # ← internal/runtime/memory
│       ├── artifact/               # ← internal/runtime/artifact
│       ├── checkpoint/             # ← internal/runtime/checkpoint
│       ├── migrate/                # ← internal/runtime/migrate
│       │
│       ├── background/             # ← internal/runtime/background
│       ├── hooks/                  # ← internal/runtime/hooks
│       ├── events/                 # ← internal/runtime/events
│       ├── observability/          # ← internal/runtime/observability
│       │
│       ├── capability/             # ← internal/runtime/capability
│       ├── policy/                 # ← internal/runtime/policy
│       ├── toolbroker/             # ← internal/runtime/toolbroker
│       ├── output/                 # ← internal/runtime/output
│       ├── contextmgr/             # ← internal/runtime/contextmgr
│       ├── prompt/                 # ← internal/runtime/prompt
│       ├── profileinput/           # ← internal/runtime/profileinput
│       ├── tools/                  # ← internal/runtime/tools
│       │
│       ├── config/                 # 新增：AgentConfig（从 ai-gateway/internal/config 裁剪）
│       │   └── config.go
│       │
│       ├── types/                  # ← internal/runtime/types
│       ├── errors/                 # ← internal/runtime/errors
│       └── pkg/
│           └── logger/             # ← internal/pkg/logger
│
├── frontend/                       # 前端项目（React + TypeScript）
│   ├── package.json
│   ├── vite.config.ts
│   ├── tsconfig.json
│   ├── public/
│   └── src/
│       ├── App.tsx
│       ├── main.tsx
│       ├── api/
│       ├── components/
│       ├── pages/
│       ├── hooks/
│       ├── store/
│       ├── types/
│       └── utils/
│
├── docs/                           # 项目文档
│   ├── architecture.md
│   ├── api.md
│   └── getting-started.md
│
├── configs/                        # 示例配置文件
│   └── config.example.yaml
│
├── scripts/                        # 构建、部署脚本
│
├── .github/
│   └── workflows/
│
├── MIGRATION.md                    # 本文档
├── README.md
├── .gitignore
└── Makefile
```

---

## 2. 来源映射表

| 目标路径（backend/internal/） | 来源路径（ai-gateway/） | 耦合等级 |
|---|---|---|
| `types/` | `internal/runtime/types` | 无耦合，直接迁移 |
| `errors/` | `internal/runtime/errors` | 无耦合，直接迁移 |
| `output/` | `internal/runtime/output` | 无耦合，直接迁移 |
| `events/` | `internal/runtime/events` | 无耦合，直接迁移 |
| `capability/` | `internal/runtime/capability` | 无耦合，直接迁移 |
| `policy/` | `internal/runtime/policy` | 无耦合，直接迁移 |
| `memory/` | `internal/runtime/memory` | 无耦合，直接迁移 |
| `artifact/` | `internal/runtime/artifact` | 无耦合，直接迁移 |
| `checkpoint/` | `internal/runtime/checkpoint` | 无耦合，直接迁移 |
| `executor/` | `internal/runtime/executor` | 无耦合，直接迁移 |
| `hooks/` | `internal/runtime/hooks` | 无耦合，直接迁移 |
| `contextmgr/` | `internal/runtime/contextmgr` | 无耦合，直接迁移 |
| `prompt/` | `internal/runtime/prompt` | 无耦合，直接迁移 |
| `background/` | `internal/runtime/background` | 无耦合，直接迁移 |
| `observability/` | `internal/runtime/observability` | 无耦合，直接迁移 |
| `embedding/` | `internal/runtime/embedding` | 无耦合，直接迁移 |
| `migrate/` | `internal/runtime/migrate` | 无耦合，直接迁移 |
| `contextpack/` | `internal/runtime/contextpack` | 无耦合，直接迁移 |
| `profileinput/` | `internal/runtime/profileinput` | 无耦合，直接迁移 |
| `toolbroker/` | `internal/runtime/toolbroker` | 无耦合，直接迁移 |
| `tools/` | `internal/runtime/tools` | 无耦合，直接迁移 |
| `mcp/` | `internal/mcp` | 自洽，直接迁移 |
| `pkg/logger/` | `internal/pkg/logger` | 无耦合，直接迁移 |
| `workspace/` | `internal/runtime/workspace` | 轻耦合（依赖 agent 类型，需接口化）|
| `chatcore/` | `internal/runtime/chatcore` | 轻耦合 |
| `llm/` | `internal/runtime/llm` | **强耦合**：依赖 `gateway/loadbalancer` + `config` |
| `skill/` | `internal/runtime/skill` | 中耦合：依赖 llm 解耦后可迁移 |
| `agent/` | `internal/runtime/agent` | 中耦合：依赖 llm、skill、team |
| `team/` | `internal/team` | 中耦合：依赖 toolkit、mcp |
| `toolkit/` | `internal/toolkit` | **强耦合**：依赖 `config.Config`、`profile`、`loadbalancer` |
| `chat/` | `internal/runtime/chat` | 中耦合：依赖上述所有 |
| `config/` | 新建 | 从 `internal/config` 裁剪出 AgentConfig |

---

## 3. 关键解耦方案

### 3.1 LLM 包解耦（`llm/` → `gateway/loadbalancer`）

**问题：** `internal/runtime/llm` 直接依赖 `gateway/loadbalancer` 和 `internal/config`。

**方案：** 定义 `ProviderBalancer` 接口，网关侧提供实现并注入。

```go
// backend/internal/llm/balancer.go（新文件）
package llm

type ProviderBalancer interface {
    SelectProvider(ctx context.Context, model string) (*ProviderEndpoint, error)
    ReportSuccess(endpoint *ProviderEndpoint)
    ReportFailure(endpoint *ProviderEndpoint, err error)
}

type ProviderEndpoint struct {
    Name    string
    BaseURL string
    APIKey  string
    Weight  int
}
```

在 `ai-gateway` 侧创建适配器：
```go
// ai-gateway: internal/adapter/llm_balancer.go
type GatewayBalancer struct {
    lb *loadbalancer.Manager
}
func (g *GatewayBalancer) SelectProvider(...) { ... }
```

### 3.2 Toolkit 包解耦（`toolkit/` → `config.Config`）

**问题：** `toolkit` 依赖整个 `internal/config.Config`（含 Server、DB、Redis 等无关字段）。

**方案：** 新建 `AgentConfig` 只包含 agent 运行所需字段。

```go
// backend/internal/config/config.go（新建）
package config

type AgentConfig struct {
    Providers  ProvidersConfig  // LLM provider 配置
    MCP        MCPConfig        // MCP server 配置
    Toolkit    ToolkitConfig    // 工具执行配置
    Log        LogConfig        // 日志配置
    Workspace  WorkspaceConfig  // 工作区配置
}
```

在 `ai-gateway` 侧提供转换函数：
```go
func ToAgentConfig(c *config.Config) *agentconfig.AgentConfig { ... }
```

### 3.3 Logger 包

`internal/pkg/logger` 是 zap 的薄封装，无外部项目依赖，直接迁移到 `backend/internal/pkg/logger/`，只需更新 import 路径。

---

## 4. Import 路径替换规则

复制文件后，批量执行以下替换：

| 旧路径前缀 | 新路径前缀 |
|---|---|
| `github.com/ai-gateway/gateway/internal/runtime/` | `github.com/ai-gateway/ai-agent-runtime/backend/internal/` |
| `github.com/ai-gateway/gateway/internal/mcp/` | `github.com/ai-gateway/ai-agent-runtime/backend/internal/mcp/` |
| `github.com/ai-gateway/gateway/internal/team` | `github.com/ai-gateway/ai-agent-runtime/backend/internal/team` |
| `github.com/ai-gateway/gateway/internal/toolkit` | `github.com/ai-gateway/ai-agent-runtime/backend/internal/toolkit` |
| `github.com/ai-gateway/gateway/internal/pkg/logger` | `github.com/ai-gateway/ai-agent-runtime/backend/internal/pkg/logger` |

---

## 5. 迁移执行步骤

### Phase 1：建立目录骨架

```bash
cd E:/projects/ai/ai-agent-runtime
git init

# 创建目录结构
mkdir -p backend/cmd/server backend/cmd/aicli
mkdir -p backend/internal
mkdir -p frontend/src
mkdir -p docs configs scripts .github/workflows

# 初始化 Go module
cd backend
go mod init github.com/ai-gateway/ai-agent-runtime
```

### Phase 2：复制零耦合包

```bash
SRC="E:/projects/ai/ai-gateway"
DST="E:/projects/ai/ai-agent-runtime/backend/internal"

# 零耦合包（直接复制）
for pkg in types errors output events capability policy memory artifact \
           checkpoint executor hooks contextmgr prompt background \
           observability embedding migrate contextpack profileinput \
           toolbroker tools; do
  cp -r "$SRC/internal/runtime/$pkg" "$DST/$pkg"
done

# MCP
cp -r "$SRC/internal/mcp" "$DST/mcp"

# logger
mkdir -p "$DST/pkg"
cp -r "$SRC/internal/pkg/logger" "$DST/pkg/logger"
```

### Phase 3：批量替换 import 路径

```bash
DST="E:/projects/ai/ai-agent-runtime/backend/internal"

find "$DST" -name '*.go' -exec sed -i \
  -e 's|github.com/ai-gateway/gateway/internal/runtime/|github.com/ai-gateway/ai-agent-runtime/backend/internal/|g' \
  -e 's|github.com/ai-gateway/gateway/internal/mcp/|github.com/ai-gateway/ai-agent-runtime/backend/internal/mcp/|g' \
  -e 's|github.com/ai-gateway/gateway/internal/pkg/|github.com/ai-gateway/ai-agent-runtime/backend/internal/pkg/|g' \
  -e 's|github.com/ai-gateway/gateway/internal/team|github.com/ai-gateway/ai-agent-runtime/backend/internal/team|g' \
  -e 's|github.com/ai-gateway/gateway/internal/toolkit|github.com/ai-gateway/ai-agent-runtime/backend/internal/toolkit|g' \
  {} \;

cd E:/projects/ai/ai-agent-runtime/backend
go mod tidy
go test ./internal/types/... ./internal/errors/... ./internal/executor/...
```

### Phase 4：创建 AgentConfig，解耦 toolkit

1. 新建 `backend/internal/config/config.go`，定义 `AgentConfig`
2. 复制 `internal/toolkit/` → `backend/internal/toolkit/`
3. 替换 `*config.Config` 引用为 `*agentconfig.AgentConfig`
4. 删除对 `internal/profile`、`internal/gateway/loadbalancer` 的引用

### Phase 5：创建 ProviderBalancer 接口，解耦 llm

1. 新建 `backend/internal/llm/balancer.go`，定义接口
2. 复制 `internal/runtime/llm/` → `backend/internal/llm/`
3. 将 `loadbalancer.Manager` 调用替换为 `ProviderBalancer` 接口

### Phase 6：迁移 skill、agent、team、chat

依赖顺序：`skill → agent → team → chat`

```bash
for pkg in skill agent team chat chatcore workspace contextpack; do
  cp -r "$SRC/internal/runtime/$pkg" "$DST/$pkg" 2>/dev/null || \
  cp -r "$SRC/internal/$pkg" "$DST/$pkg"
done
# 再次执行 Phase 3 的 import 替换
```

### Phase 7：更新 ai-gateway 引用

```go
// ai-gateway/go.mod
require github.com/ai-gateway/ai-agent-runtime v0.1.0

// 开发期间使用本地路径
replace github.com/ai-gateway/ai-agent-runtime => ../ai-agent-runtime/backend
```

---

## 6. 不迁移的包（保留在 ai-gateway）

| 包 | 原因 |
|---|---|
| `internal/gateway/loadbalancer` | 网关核心，提供 `GatewayBalancer` 实现注入到新模块 |
| `internal/config` | 网关全局配置，裁剪出 `AgentConfig` 子集给新模块使用 |
| `internal/profile` | 用户画像，属于网关业务层 |
| `internal/runtime/bootstrap` | 网关启动引导，强依赖网关基础设施 |
| `internal/runtime/e2e` | 依赖完整网关环境的端到端测试 |
| `internal/runtime/examples` | 示例代码，可视情况迁移 |
| `internal/model` | 数据库 ORM 模型，属于网关持久层 |
| `internal/repository` | 数据访问层，属于网关持久层 |
| `internal/session` | 会话管理，依赖网关数据库 |
| `internal/taskqueue` | 任务队列，依赖网关基础设施 |

---

## 7. 风险与注意事项

| 风险 | 说明 | 应对 |
|---|---|---|
| workspace ↔ agent 循环依赖 | workspace 导入 agent 类型，agent 也导入 workspace | 将共享类型下沉到 `types/` |
| sqlite3 CGO 依赖 | `migrate`、`artifact` 使用 cgo sqlite3 | 新模块保留，或抽象为存储接口 |
| e2e 测试环境 | `runtime/e2e` 依赖完整网关 | 不迁移，新模块单独建集成测试 |
| vendor 目录 | 原项目有 vendor/ | 新模块用 `go mod tidy` 重新生成，不复制 vendor |

---

## 8. 验收标准

- [ ] `backend/` 下 `go build ./...` 无错误
- [ ] `backend/` 下 `go test ./...` 全部通过
- [ ] `backend/internal/` 无对 `github.com/ai-gateway/gateway/internal/gateway/` 的 import
- [ ] `backend/internal/` 无对 `github.com/ai-gateway/gateway/internal/config` 的 import
- [ ] `backend/internal/` 无对 `github.com/ai-gateway/gateway/internal/profile` 的 import
- [ ] `ai-gateway` 引用新模块后 `go build ./...` 无错误
- [ ] `ProviderBalancer` 接口有 mock 实现供单元测试使用
- [ ] `README.md` 包含快速上手示例
