# ai-agent-runtime

通用 Multi-Agent 执行运行时，提供：

- **`aicli`** —— 命令行工具，对接 AI Gateway，支持配置查看、端点测试、上下文测试、MCP、管道模式等
- **`runtime-server`** —— Skills / Agent HTTP 服务（`/api/agent`、`/api/runtime`、`/healthz`）
- **`frontend/`** —— React + TypeScript 控制台

后端 module：`github.com/wwsheng009/ai-agent-runtime`

---

## 快速开始

### 安装 aicli

**Linux / macOS**

```bash
curl -fsSL https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.sh | bash
```

**Windows (PowerShell)**

```powershell
iwr -useb https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.ps1 | iex
```

**源码编译**

```bash
git clone https://github.com/wwsheng009/ai-agent-runtime.git
cd ai-agent-runtime
make install-aicli
```

📖 **完整安装与配置说明**：[docs/aicli/install.md](./docs/aicli/install.md)

涵盖：一键脚本 / `make install-aicli` / `go install` 三种安装方式、配置文件查找顺序、最小配置示例、环境变量、常用命令、卸载等。

### 验证

```bash
aicli version
aicli config
```

---

## 运行 runtime-server / 前端（可选）

仅当你需要 HTTP 服务或 Web 控制台时才用到。

```bash
# 后端 HTTP 服务
cd backend
go run ./cmd/runtime-server --listen 127.0.0.1:8101
# 提供 POST /api/agent/chat、/api/runtime/*、GET /healthz
```

```bash
# 前端控制台（默认监听 http://0.0.0.0:5193，代理到 127.0.0.1:8101）
cd frontend
pnpm install
pnpm dev
```

前端环境变量见 [`frontend/.env.example`](./frontend/.env.example)。

---

## 项目结构

```
ai-agent-runtime/
├── backend/                # Go 后端（module: github.com/wwsheng009/ai-agent-runtime）
│   ├── cmd/
│   │   ├── aicli/          # 命令行工具
│   │   ├── runtime-server/ # Agent / Skills HTTP 服务
│   │   ├── echo-mcp-server/
│   │   ├── toolkit-mcp-server/
│   │   └── skillsapi-demo/
│   ├── configs/            # 配置示例
│   └── internal/           # agent / llm / toolkit / mcp / skill / ...
├── frontend/               # React + TS 控制台
├── docs/
│   └── aicli/              # aicli 安装与配置文档
├── scripts/
│   ├── install-aicli.sh    # Linux / macOS 安装脚本
│   └── install-aicli.ps1   # Windows 安装脚本
├── .github/workflows/
│   └── release-aicli.yml   # tag 触发的跨平台 Release
├── Makefile
├── MIGRATION.md            # 从 ai-gateway 拆分的迁移指南
└── README.md
```

---

## 发布新版本（维护者）

```bash
git tag v0.2.0
git push origin v0.2.0
# .github/workflows/release-aicli.yml 自动编译 6 平台并发布 GitHub Release
```

支持的 tag 模式：`v*`、`aicli-v*`。包含 `-rc / -beta / -alpha` 的 tag 会被自动标记为 prerelease。

---

## 与 ai-gateway 的关系

`ai-agent-runtime` 已从 [`ai-gateway`](https://github.com/wwsheng009/ai-gateway) 中独立：

- `ai-agent-runtime` 负责 `aicli`、agent/runtime HTTP API、多 agent runtime
- `ai-gateway` 仅保留网关与代理能力

迁移细节见 [MIGRATION.md](./MIGRATION.md)。

---

## 文档导航

- [aicli 安装与配置](./docs/aicli/install.md)
- [项目文档目录](./docs/README.md)
- [迁移指南](./MIGRATION.md)
- [Roadmap](./docs/roadmap.md)
