# ai-agent-runtime

通用 Multi-Agent 执行运行时，现已作为独立服务与 CLI 仓库维护，承载 `aicli`、`/api/agent`、`/api/skills` 等 agent / skills 能力。

## 项目结构

```
ai-agent-runtime/
├── backend/                  # Go 后端（module: github.com/wwsheng009/ai-agent-runtime）
│   ├── go.mod
│   ├── cmd/
│   │   ├── runtime-server/   # Skills / Agent HTTP 服务入口
│   │   ├── skillsapi-demo/   # Skills API 客户端示例
│   │   └── aicli/            # 命令行工具
│   └── internal/
│       ├── agent/            # 核心：orchestrator、ReAct loop、planner、scheduler
│       ├── team/             # 多 agent 团队协作
│       ├── llm/              # LLM provider 抽象（ProviderBalancer 接口）
│       ├── skill/            # 技能路由、DAG、embedding router
│       ├── toolkit/          # 工具实现集合
│       ├── mcp/              # MCP 协议（protocol/transport/client/server/registry）
│       ├── chat/             # 对话 actor
│       ├── chatcore/         # 对话核心逻辑
│       ├── workspace/        # 工作区上下文
│       ├── executor/         # 并行执行器、沙盒
│       ├── memory/           # 记忆管理
│       ├── checkpoint/       # 状态持久化
│       ├── artifact/         # checkpoint 文件存储
│       ├── embedding/        # embedding 模型抽象
│       ├── types/            # 共享类型定义
│       ├── errors/           # 错误类型
│       └── pkg/logger/       # 日志工具（zap 封装）
│
├── frontend/                 # React + TypeScript 前端控制台
│   └── src/
│
├── docs/                     # 文档
├── configs/                  # 配置示例
├── scripts/                  # 构建/部署脚本
├── Makefile
├── MIGRATION.md              # 迁移指南
└── README.md
```

当前后端 module 路径为 `github.com/wwsheng009/ai-agent-runtime`。

## 快速开始

### 后端

```bash
cd backend
go mod tidy
go build ./...
go test ./...
go run ./cmd/runtime-server --listen 127.0.0.1:8081
```

启动后，runtime 服务会独立承载：

- `POST /api/agent/chat`
- `/api/skills/*`
- `GET /healthz`

### 前端

```bash
cd frontend
pnpm install
pnpm dev
```

## 与 ai-gateway 的关系

`ai-agent-runtime` 已从 `ai-gateway` 中独立出来。当前约定是：

- `ai-agent-runtime` 负责 `aicli`、agent/skills HTTP API、多 agent runtime
- `ai-gateway` 只保留网关与代理能力，不再暴露 `/api/agent`、`/api/skills`

详细迁移步骤见 [MIGRATION.md](./MIGRATION.md)。

## 核心解耦接口

### ProviderBalancer

`llm` 包依赖此接口而非直接依赖网关 loadbalancer：

```go
type ProviderBalancer interface {
    SelectProvider(ctx context.Context, model string, opts BalancerOptions) (*ProviderEndpoint, error)
    ReportResult(ctx context.Context, endpoint *ProviderEndpoint, success bool, latency time.Duration)
}
```

网关侧实现 `GatewayBalancer` 并在初始化时注入。
