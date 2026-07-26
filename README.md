# ai-agent-runtime

本地优先的 AI Agent Runtime：用命令行 `aicli` 完成安装、登录、聊天与工具调用；需要时再启用 HTTP 服务与 Web 控制台。

最短路径：

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

### 3) 登录 Provider（最小示例）

只有 OpenAI 兼容 API Key 时，复制下面这一条即可：

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

**Windows PowerShell**

```powershell
$env:OPENAI_API_KEY = 'sk-...'
aicli login `
  --provider openai `
  --protocol openai `
  --base-url https://api.openai.com `
  --api-key $env:OPENAI_API_KEY `
  --set-default
```

登录会校验 models endpoint，并把凭证写回配置。

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
aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key "$OPENAI_API_KEY" --set-default

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
├── MIGRATION.md             # 从 ai-gateway 拆分的迁移说明
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
- [项目文档目录](./docs/README.md)
- [Roadmap](./docs/roadmap.md)
- [迁移指南（ai-gateway）](./MIGRATION.md)

---

## 维护者附录

发版（tag 触发跨平台 Release）：

```bash
git tag v0.2.0
git push origin v0.2.0
# .github/workflows/release-aicli.yml → 6 平台 GitHub Release
```

支持的 tag：`v*`、`aicli-v*`。含 `-rc` / `-beta` / `-alpha` 的 tag 会标为 prerelease。

与 [`ai-gateway`](https://github.com/wwsheng009/ai-gateway) 的关系：本仓库负责 `aicli`、agent/runtime HTTP API 与 multi-agent runtime；网关能力留在 `ai-gateway`。细节见 [MIGRATION.md](./MIGRATION.md)。
