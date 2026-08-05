# aicli Quickstart

最短路径：

```text
安装 aicli → aicli init --global → aicli login → aicli 进入 chat
```

更完整的安装、配置字段、slash commands、session/resume、MCP、卸载说明见 [install.md](./install.md)。常见报错与排查见 [faq.md](./faq.md)。仓库总入口见 [根 README](../../README.md)。

## 0. 首次安装 checklist（含成功信号）

装完二进制后，按下面步骤即可进入可用状态（与根 README「5 分钟上手」一致）：

| 步骤 | 命令 | 目的 | 成功信号 |
|---|---|---|---|
| 0. 安装 | 安装脚本 / Release / `make install-aicli` | 得到可执行的 `aicli` | `aicli version` 打印版本号 |
| 1. 初始化配置 | `aicli init --global` | 写入用户级 `~/.aicli/config.yaml` | `aicli config` 能读到配置路径 |
| 2. 登录 provider | `aicli login ... --set-default`（见下） | 校验 models endpoint 并写回凭证/默认 provider | `aicli provider list` 可见该 provider；`aicli doctor provider` 不报致命错误 |
| 3. 开始使用 | `aicli` | 进入默认交互式 chat | 模型能回复；`/model status` 显示当前 provider/model |

可选加固检查：

```bash
aicli doctor provider
aicli doctor provider --provider openai --model gpt-4.1
aicli config --models
```

失败时优先看 [faq.md](./faq.md)（空 providers、login models 校验、HTTP 401、Windows PATH）。

## 1. 安装

**Linux / macOS**

```bash
curl -fsSL https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.sh | bash
```

默认安装到 `~/.local/bin`（需已在 `PATH` 中）。

**Windows (PowerShell)**

```powershell
iwr -useb https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.ps1 | iex
```

默认安装到 `%LOCALAPPDATA%\Programs\aicli`，并写入当前用户 `PATH`。  
**装完后请新开一个终端**，再验证：

```powershell
Get-Command aicli
aicli version
```

**源码编译**（需要 [Go 1.24+](https://go.dev/dl/) 与 Make）

```bash
git clone https://github.com/wwsheng009/ai-agent-runtime.git
cd ai-agent-runtime
make install-aicli   # 安装到 $GOBIN 或 $(go env GOPATH)/bin
```

网络受限时，也可从 [GitHub Releases](https://github.com/wwsheng009/ai-agent-runtime/releases) 下载对应平台二进制，放到已在 `PATH` 的目录。

**成功信号**

```bash
aicli version
```

能打印版本号即可。

## 2. 初始化配置

推荐首次使用写用户级配置（不必先手改 YAML）：

```bash
aicli init --global
```

**成功信号**

```bash
aicli config
```

能读到配置路径（通常是 `~/.aicli/config.yaml`）。

默认配置查找顺序（首个存在即采用）：

1. `-c/--config <path>`
2. `$HOME/.aicli/config.yaml`（用户级，推荐）
3. `./.aicli/config.yaml`
4. `./aicli.yaml`
5. `./configs/config.yaml`

没有配置时，启动某些命令也可能自动创建 starter；显式 `aicli init` 更清晰。字段与环境变量说明见 [install.md](./install.md)。

## 3. 登录 Provider

### 最小示例（OpenAI 兼容 API Key）

建议 API key 走环境变量，避免写进 shell 历史：

```bash
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

### 其他常见场景

```bash
# 本地 OpenAI 兼容服务
aicli login \
  --provider local \
  --protocol openai \
  --base-url http://127.0.0.1:4000 \
  --models-path /v1/models

# Codex OAuth
aicli login \
  --provider codex \
  --protocol codex-oauth \
  --base-url https://api.openai.com \
  --auth-ref codex \
  --set-default
```

登录成功后也可再查：

```bash
aicli config
aicli config --models
```

chat 内也可以用 `/login`，与 `aicli login` 共用同一套逻辑。更多 provider 写法见 [install.md](./install.md)。

## 4. 开始聊天

```bash
# 默认进入交互式 chat（与 aicli chat 等价）
aicli

# 启动时指定 provider / model
aicli --provider openai --model gpt-4.1
aicli chat --provider openai --model gpt-4.1 --reasoning-effort medium
```

进入 chat 后建议先：

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

其他常用 slash：

```text
/model gpt-4.1
/model --provider openai --model gpt-4.1
/stream on
/status
/exit
```

完整 slash 列表见 [install.md](./install.md)。

## 5. 第一次真正“用起来”的 3 个例子

```bash
# 1) 交互：在仓库根目录打开 chat，让 agent 帮你看代码
cd /path/to/your/repo
aicli --prompt "总结这个仓库的目录结构，并指出入口命令"
# 启动消息完成后仍停留在交互界面，可继续输入

# 2) 非交互：一条命令拿结果（脚本 / 管道友好）
aicli chat --no-interactive --prompt "用 bullet 总结当前目录 README 的要点"

# 3) Headless / CI 风格
aicli exec "解释 backend/cmd/aicli 的职责"
```

## 6. 卡住了？先跑这三连

```bash
aicli config
aicli doctor provider
aicli doctor provider --provider openai --model gpt-4.1
```

| 现象 | 优先看 |
|---|---|
| 找不到 `aicli`（尤其 Windows） | 新开终端；`Get-Command aicli`；[faq.md](./faq.md) 的 PATH 段 |
| `providers` 为空 / 没配置 | 是否做过 `init --global` 与 `login` |
| login 校验 models 失败 | base-url / key / 网络；[faq.md § login](./faq.md) |
| HTTP 401 | key 与 provider 是否匹配 |
| chat 里 `/model` 切不过去 | [faq.md](./faq.md) |

完整排错清单：[faq.md](./faq.md)。

## 7. 常用后续命令

```bash
# 诊断
aicli doctor provider
aicli doctor provider --provider openai --model gpt-4.1

# 非交互 / CI
aicli chat --no-interactive --prompt "summarize this repo"
aicli exec "解释这段代码的作用"

# 配置查看
aicli config --provider openai
aicli config --models --output json
```

## 下一步

| 需求 | 文档 |
|---|---|
| 安装细节、配置字段、slash、session、MCP、卸载 | [install.md](./install.md) |
| 常见问题与排错 | [faq.md](./faq.md) |
| headless `exec` / CI | [exec.md](./exec.md) |
| portable agent definition | [agents.md](./agents.md) |
| 图片生成 `aicli image` / `/image` | [tool_image_generate.md](./tool_image_generate.md) |
| skills 暴露与路由 | [../skill_runtime/aicli_skills_usage.md](../skill_runtime/aicli_skills_usage.md) |
| 仓库总入口 | [根 README](../../README.md) |
