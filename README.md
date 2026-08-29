# ai-agent-runtime

本地优先的 AI Agent Runtime：在你自己的机器上跑 agent，而不是把工作流绑死在某个云 IDE 或单一 SaaS 上。

## 项目介绍

`ai-agent-runtime` 提供一套可本地运行、可扩展、可嵌入脚本/CI 的 Agent 运行时。默认入口是命令行工具 `aicli`：安装、登录、聊天、工具调用都能在终端完成；需要时再启用 `runtime-server` HTTP API 与 Web 控制台。

### 它解决什么问题

- 想在本地仓库里直接和代码对话、改文件、跑命令，而不是只做网页聊天。
- 希望 agent 具备可控的工具执行、权限策略、session 恢复与 headless 输出，方便脚本和 CI。
- 需要把「单 agent 对话」扩展到 skill、MCP、子 agent / team 编排，而不是只停在一次 LLM 调用。
- 可选地通过 HTTP / Web UI 管理 session、team、runtime 配置，而不是强制依赖云端控制台。

### 核心能力

| 能力 | 说明 |
|---|---|
| 交互式 Chat | `aicli` / `aicli chat`：流式对话、slash 命令、session resume、模型/provider 切换 |
| Headless Exec | `aicli exec`：JSON/JSONL 输出、退出码约定，适合管道、脚本与 CI |
| 工具与工作区 | 文件读写、搜索、shell、patch 等；支持 preflight、权限 overlay、危险操作审批 |
| Provider 接入 | OpenAI 兼容协议、Codex OAuth 等；`aicli login` 交互/非交互登录与 doctor 探活 |
| Skills / MCP | 将 skills 暴露为可调用能力；可接入 MCP server 扩展工具面 |
| 多 Agent | portable agent 定义、`spawn_agent` 子会话、team 任务编排与 outcome 契约 |
| HTTP + Web | `runtime-server` 提供 `/api/agent/chat`、`/api/runtime/*`；前端工作台管理会话与 team |

### 架构一览

```text
┌─────────────┐     ┌──────────────────┐     ┌─────────────────┐
│   aicli     │     │  runtime-server  │     │  Web 控制台     │
│ chat/exec   │     │  HTTP / SSE API  │◄────│  React + Vite   │
└──────┬──────┘     └────────┬─────────┘     └─────────────────┘
       │                     │
       └──────────┬──────────┘
                  ▼
        backend/internal/*
        agent · chat · toolkit · policy
        skills · team · provider · storage
```

- **默认路径**：本机安装 `aicli` → 配置 provider → 在仓库目录里聊天/执行。
- **可选路径**：启动 `runtime-server` + frontend，用浏览器管理会话、team 与 runtime 配置。
- **实现语言**：后端 Go（`backend/`），前端 TypeScript/React（`frontend/`）。

### 设计原则

1. **Local-first**：凭证、session、工具执行默认落在本机；HTTP/Web 是增强，不是门槛。
2. **CLI 优先**：最短路径始终是 `aicli`，文档与安装脚本围绕「装上就能聊」收口。
3. **契约清晰**：工具结果、exec 输出、team outcome、权限模式都尽量有稳定约定，便于自动化。
4. **可组合扩展**：skills、MCP、agent definition、permission overlay 分层叠加，而不是写死单一 agent。

### 适用场景

- 本地开发助手：读仓库、改代码、跑测试、排查问题
- 自动化脚本 / CI：`aicli exec` 做 code review、摘要、批处理
- 多 agent 协作实验：子 agent 并行、team 任务分派与结果汇总
- 自托管控制台：企业内部或本机部署 runtime-server + Web UI

最短上手路径：

```text
安装 aicli → aicli init --global → aicli login → aicli 进入 chat
```

细节以文档为准（根 README 只保留可跑通的入口）：

| 你想做什么 | 文档 |
|---|---|
| 首次安装 checklist + 成功信号 | [docs/aicli/quickstart.md](./docs/aicli/quickstart.md) |
| 安装/配置/slash/session/MCP/卸载 | [docs/aicli/install.md](./docs/aicli/install.md) |
| 卡住了（空 providers / 401 / PATH） | [docs/aicli/faq.md](./docs/aicli/faq.md) |
| 全部 aicli 文档 | [docs/aicli/README.md](./docs/aicli/README.md) |
| 运行时 / skills / multi-agent | [docs/README.md](./docs/README.md) |

---

## 5 分钟上手

按顺序做完，就能开始对话。

### 1) 安装

**Linux / macOS**

```bash
curl -fsSL https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.sh | bash
```

默认安装到 `~/.local/bin`（需已在 `PATH` 中）。

**Windows（PowerShell）**

```powershell
iwr -useb https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.ps1 | iex
```

默认安装到 `%LOCALAPPDATA%\Programs\aicli`，并写入当前用户 `PATH`。  
**装完后请新开一个终端**，再验证：

```powershell
Get-Command aicli
aicli version
```

**源码安装**（需要 [Go 1.24+](https://go.dev/dl/) 与 Make）

```bash
git clone https://github.com/wwsheng009/ai-agent-runtime.git
cd ai-agent-runtime
make install-aicli   # 安装到 $GOBIN 或 $(go env GOPATH)/bin
```

网络受限、无法跑安装脚本时，也可从 [GitHub Releases](https://github.com/wwsheng009/ai-agent-runtime/releases) 下载对应平台二进制，放到已在 `PATH` 的目录。

**成功信号**

```bash
aicli version
```

能打印版本号即可。

### 2) 初始化配置

多数人只需要用户级配置，不必先手改 YAML：

```bash
aicli init --global
```

**成功信号**

```bash
aicli config
```

能读到配置路径（通常是 `~/.aicli/config.yaml`）。

> 配置查找顺序、字段说明、环境变量 / `.env`：见 [install.md](./docs/aicli/install.md)。

### 3) 登录 Provider

**推荐：无参数交互登录**

```bash
aicli login
```

不带任何 flag 时，会进入交互向导，按提示逐步输入：

- Provider 名称（新建，或从已有列表里选）
- 登录协议（如 `openai`、`codex-oauth`）
- Base URL
- API key（终端隐藏输入，不进 shell 历史）
- 是否设为默认 provider 等

登录会校验 models endpoint，并把凭证写回配置。chat 内也可用 `/login`，逻辑相同。

**可选：一条命令非交互登录**（脚本 / CI）

```bash
# 建议 API key 走环境变量，避免写进 shell 历史
export OPENAI_API_KEY=sk-...

aicli login \
  --provider openai \
  --protocol openai \
  --base-url https://api.openai.com \
  --api-key "$OPENAI_API_KEY" \
  --set-default
```

**Windows PowerShell（非交互）**

```powershell
$env:OPENAI_API_KEY = 'sk-...'
aicli login `
  --provider openai `
  --protocol openai `
  --base-url https://api.openai.com `
  --api-key $env:OPENAI_API_KEY `
  --set-default
```

**成功信号**

```bash
aicli provider list
aicli doctor provider
# 可选：指定模型再探活
aicli doctor provider --provider openai --model gpt-4.1
```

能看到 provider，且 doctor 不报致命错误即可。

其他场景（本地兼容网关、Codex OAuth、DeepSeek 等）见 [quickstart.md](./docs/aicli/quickstart.md) 与 [install.md](./docs/aicli/install.md)。

### 4) 开始使用

```bash
# 默认进入交互式 chat（与 aicli chat 等价）
aicli
```

进 chat 后建议先：

```text
/model status
/help
```

然后直接提问，例如：

```text
用三句话介绍当前目录这个项目是做什么的
```

**成功信号**：模型能回复；`/model status` 显示当前 provider/model。

退出：`/exit` 或 `Ctrl+C`。

---

## 第一次真正“用起来”的 3 个例子

```bash
# 1) 交互：在仓库根目录打开 chat，让 agent 帮你看代码
cd /path/to/your/repo
aicli
# 然后输入：总结这个仓库的目录结构，并指出入口命令

# 2) 非交互：一条命令拿结果（脚本 / 管道友好）
aicli chat --no-interactive --message "用 bullet 总结当前目录 README 的要点"

# 3) Headless / CI 风格
aicli exec "解释 backend/cmd/aicli 的职责"
```

进阶：

| 需求 | 入口 |
|---|---|
| JSON/JSONL、resume、CI 退出码 | [docs/aicli/exec.md](./docs/aicli/exec.md) |
| portable agent / `spawn_agent.agent_type` | [docs/aicli/agents.md](./docs/aicli/agents.md) |
| skills 怎么暴露与路由 | [docs/skill_runtime/aicli_skills_usage.md](./docs/skill_runtime/aicli_skills_usage.md) |
| 图片生成 `aicli image` / `/image` | [docs/aicli/tool_image_generate.md](./docs/aicli/tool_image_generate.md) |

---

## 卡住了？先跑这三连

```bash
aicli config
aicli doctor provider
aicli doctor provider --provider openai --model gpt-4.1
```

| 现象 | 优先看 |
|---|---|
| 找不到 `aicli`（尤其 Windows） | 新开终端；`Get-Command aicli`；[faq.md](./docs/aicli/faq.md) 的 PATH 段 |
| `providers` 为空 / 没配置 | 是否做过 `init --global` 与 `login` |
| login 校验 models 失败 | base-url / key / 网络；[faq.md § login](./docs/aicli/faq.md) |
| HTTP 401 | key 与 provider 是否匹配 |
| chat 里 `/model` 切不过去 | [faq.md](./docs/aicli/faq.md) |

完整排错清单：[docs/aicli/faq.md](./docs/aicli/faq.md)。

---

## 常用命令速查

```bash
# 配置与诊断
aicli init --global
aicli config
aicli config --models
aicli doctor provider
aicli login   # 无参数：交互向导
# aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key "$OPENAI_API_KEY" --set-default

# 聊天
aicli
aicli chat --provider openai --model gpt-4.1
aicli chat --resume latest
aicli chat --no-interactive --message "summarize this repo"

# 测试 / 管道 / headless
aicli test --provider openai --message "Hello"
echo "Hello" | aicli pipe --model gpt-4.1
aicli exec "解释这段代码"

# MCP / 图片
aicli mcp --help
aicli image "帮我生成一张海边日落照片"
```

chat 内常用 slash（完整列表见 [install.md](./docs/aicli/install.md)）：

```text
/help
/model status | /model <name>
/login ...
/status
/stream on|off
/exit
```

---

## 运行 runtime-server / 前端（可选）

仅当你需要 HTTP 服务或 Web 控制台时：

```bash
# 后端
cd backend
go run ./cmd/runtime-server serve --listen 127.0.0.1:8101
# POST /api/agent/chat、/api/runtime/*、GET /healthz
```

```bash
# 前端（默认 http://0.0.0.0:5193，代理到 127.0.0.1:8101）
cd frontend
pnpm install
pnpm dev
```

前端环境变量见 [`frontend/.env.example`](./frontend/.env.example)。

---

## 项目结构（简表）

```text
ai-agent-runtime/
├── backend/                 # Go：aicli、runtime-server、agent/llm/toolkit/...
├── frontend/                # React + TS 控制台
├── docs/aicli/              # 新用户与运维文档（quickstart / install / faq / exec / agents）
├── scripts/install-aicli.*  # 跨平台安装脚本
├── .github/workflows/       # tag 触发的 aicli Release
├── Makefile
├── MIGRATION.md
└── README.md
```

---

## 文档导航

- [aicli 快速开始](./docs/aicli/quickstart.md) — 安装 → init → login → chat
- [aicli 安装与配置](./docs/aicli/install.md) — 配置顺序、login、slash、session、MCP、卸载
- [aicli 常见问题](./docs/aicli/faq.md)
- [aicli headless exec](./docs/aicli/exec.md)
- [aicli agents](./docs/aicli/agents.md)
- [aicli 图片生成](./docs/aicli/tool_image_generate.md)
- [aicli 文档目录](./docs/aicli/README.md)
- [开发规范](./docs/development-guidelines.md)
- [项目文档目录](./docs/README.md)
- [Roadmap](./docs/roadmap.md)
- [迁移指南](./MIGRATION.md)

---

## 维护者附录

发版（tag 触发跨平台 Release）：

```bash
git tag v0.4.5
git push origin v0.4.5
# .github/workflows/release-aicli.yml → 6 平台 GitHub Release（aicli + runtime-server 制品）
```

支持的 tag：`v*`、`aicli-v*`。含 `-rc` / `-beta` / `-alpha` 的 tag 会标为 prerelease。
