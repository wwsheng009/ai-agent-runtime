# ai-agent-runtime

通用 Multi-Agent 执行运行时，从 [ai-gateway](https://github.com/ai-gateway/gateway) 项目中提取的独立子系统。

## 项目结构

```
ai-agent-runtime/
├── backend/                  # Go 后端（module: github.com/ai-gateway/ai-agent-runtime）
│   ├── go.mod
│   ├── cmd/
│   │   ├── server/           # HTTP/gRPC 服务入口
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

## 快速开始

### 后端

```bash
cd backend
go mod tidy
go build ./...
go test ./...
```

### 前端

```bash
cd frontend
pnpm install
pnpm dev
```

## 与 ai-gateway 的关系

本模块从 ai-gateway 中提取，作为独立的 agent 运行时库使用。ai-gateway 通过以下方式接入：

```go
// ai-gateway/go.mod
require github.com/ai-gateway/ai-agent-runtime v0.1.0

// 开发期间使用本地路径
replace github.com/ai-gateway/ai-agent-runtime => ../ai-agent-runtime/backend
```

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
