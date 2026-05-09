# aicli 安装与配置

`aicli` 是 `ai-agent-runtime` 提供的命令行工具。当前默认入口是 chat，同时支持 provider 登录、session/resume、slash commands、tools/skills、shell/background、MCP、配置查看、端点测试、上下文测试和管道模式。

直接运行 `aicli` 会默认进入交互式 chat 模式；`aicli chat` 仍然是显式且等价的入口。

本文档涵盖：

- [一、安装](#一安装)
- [二、配置](#二配置)
- [三、常用命令](#三常用命令)
- [四、卸载](#四卸载)

---

## 一、安装

### 方式 1：一键安装脚本（推荐）

从 [GitHub Release](https://github.com/wwsheng009/ai-agent-runtime/releases) 下载预编译二进制并自动放入用户 PATH。

**Linux / macOS**

```bash
curl -fsSL https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.sh | bash
```

默认安装到 `~/.local/bin`。可用环境变量覆盖：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `AICLI_VERSION` | `latest` | 指定版本 tag，如 `v0.1.0` |
| `AICLI_INSTALL_DIR` | `$HOME/.local/bin` | 安装目录 |
| `AICLI_REPO` | `wwsheng009/ai-agent-runtime` | 源仓库 |

示例：

```bash
AICLI_VERSION=v0.1.0 AICLI_INSTALL_DIR=$HOME/bin bash install-aicli.sh
```

**Windows (PowerShell)**

```powershell
iwr -useb https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.ps1 | iex
```

默认安装到 `%LOCALAPPDATA%\Programs\aicli`，并自动追加到当前用户 PATH（新开终端生效）。可用环境变量：

```powershell
$env:AICLI_VERSION = 'v0.1.0'
$env:AICLI_INSTALL_DIR = "$env:USERPROFILE\bin"
iwr -useb https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.ps1 | iex
```

脚本会自动识别 `amd64` / `arm64` 架构，并校验 `sha256`。

### 方式 2：源码编译安装

```bash
git clone https://github.com/wwsheng009/ai-agent-runtime.git
cd ai-agent-runtime
make install-aicli   # 调用 go install，安装到 $GOBIN（默认 $(go env GOPATH)/bin）
```

可选参数：

```bash
# 注入版本号（默认 dev）
make install-aicli VERSION=v0.1.0

# 自定义安装目录
GOBIN=$HOME/bin make install-aicli
```

仅本地编译不安装：

```bash
make aicli           # 产出 ./aicli 可执行文件
```

### 方式 3：本地模块内 go install

```bash
git clone https://github.com/wwsheng009/ai-agent-runtime.git
cd ai-agent-runtime/backend
go install ./cmd/aicli
```

说明：当前 Go module 位于 `backend/go.mod`，因此远程 `go install github.com/wwsheng009/ai-agent-runtime/backend/cmd/aicli@latest` 不是推荐路径；源码安装请优先使用 `make install-aicli` 或在 `backend` 目录执行 `go install ./cmd/aicli`。

### 验证安装

```bash
aicli version
# 输出：
#   AI CLI version: v0.1.0
#   Build time:     2026-04-27T15:26:06Z
```

---

## 二、配置

`aicli` 启动时按以下顺序查找配置文件，**首个存在即采用**：

| 优先级 | 路径 | 用途 |
|---|---|---|
| 显式覆盖 | `-c/--config <path>` | 命令行显式指定（最高） |
| 1 | `$HOME/.aicli/config.yaml` | 用户级全局配置 |
| 2 | `./.aicli/config.yaml` | 项目级配置（cwd 下 `.aicli/`） |
| 3 | `./aicli.yaml` | 项目级单文件配置 |
| 4 | `./configs/config.yaml` | 旧版默认（向后兼容） |

四个默认候选位置都不存在时，当前 `aicli` 会在当前工作目录创建 starter 配置 `./.aicli/config.yaml`，而不是以完全空配置启动。starter 中默认开启 `aicli.chat.stream: true`，并保留空的 `providers.items`，方便后续通过 `aicli login` 或手工编辑补 provider。

注意：`./configs/config.yaml` 是相对当前工作目录解析的路径。仓库示例配置实际位于 `backend/configs/config.yaml`，只有从 `backend` 目录运行时才会被默认候选命中；从仓库根运行时请使用 `-c backend/configs/config.yaml` 或创建项目级 `./.aicli/config.yaml`。

### 初始化 starter 配置

如果你还没有配置文件，可以先让 `aicli` 自动生成一个最小 starter 配置：

```bash
# 在当前工作目录生成 `./.aicli/config.yaml`
aicli init

# 在用户目录生成 ~/.aicli/config.yaml
aicli init --global

# 也可以显式指定目标路径
aicli init --config ~/.aicli/config.yaml

# 以 JSON 输出初始化结果
aicli init --json
aicli init --output json
```

说明：

- `aicli init` 默认生成的是项目级 `./.aicli/config.yaml`
- `aicli init --global` 等价于 `aicli init --config ~/.aicli/config.yaml`
- 如果你希望优先使用仓库内配置，建议保持默认本地初始化
- 如果你希望保存个人默认值到用户目录，可以使用 `--global`
- `aicli init` 不会覆盖已有配置；JSON 输出包含 `config_path`、`created`、`already_exists` 和 `message`

### 最小配置示例

你也可以直接用 `aicli init --global` 生成一个最小骨架，然后把下面这些字段补进去。

把以下内容存为 `~/.aicli/config.yaml`：

```yaml
providers:
  default_provider: nvidia
  items:
    nvidia:
      api_key: ${NVIDIA_API_KEYS:-}
      base_url: ${NVIDIA_BASE_URL:-https://integrate.api.nvidia.com}
      api_path: ""
      forward_url: /v1/chat/completions
      protocol: openai
      default_model: z-ai/glm-5.1
      enabled: true
      supported_models:
        - z-ai/glm-5.1

aicli:
  chat:
    default_provider: nvidia
    default_model: z-ai/glm-5.1
    reasoning_effort: medium
    stream: true
  log:
    file_path: ${AICLI_LOG_FILE_PATH:-~/.aicli/logs/aicli.log}
```

完整字段示例见 [`backend/configs/config.yaml`](../../backend/configs/config.yaml)。

`aicli.chat` 偏好优先级：

1. 命令行 flag，例如 `--provider`、`--model`、`--reasoning-effort`、`--stream`
2. 已加载 session 的 provider/model/reasoning/stream metadata
3. `aicli.chat.default_provider`、`default_model`、`reasoning_effort`、`stream`
4. 交互式选择结果
5. provider 的默认模型

`/model`、`/stream`、`/s`、`/normal` 等 chat 内命令会同步更新当前 session；在具备可写配置路径时，也会把相关偏好写回 `aicli.chat`。

### 环境变量

配置中 `${VAR:-default}` 语法支持从环境变量注入。常见 API key 变量：

```bash
export NVIDIA_API_KEYS=nvapi-xxxxx
export DEEPSEEK_API_KEY=sk-xxxxx
export BIGMODEL_API_KEYS=xxxxx
export GEMINI_API_KEY=xxxxx
```

支持自动加载 `.env` 文件，搜索顺序：

1. `$HOME/.aicli/.env`
2. `./.aicli/.env`
3. `./.env`
4. `./configs/.env`

`.env` 的候选位置由 `config.yaml` 候选位置的所在目录派生，仍然是首个存在文件生效。

---

## 三、常用命令

```bash
# 列出当前 providers / provider_groups
aicli config
aicli config --provider nvidia
aicli config --groups
aicli config --models
aicli config --output json

# 端点测试
aicli test --model gpt-4 --message "Hello"
aicli test --provider nvidia --message "测试"
aicli test --stream

# 上下文窗口测试
aicli context --model glm-4.7
aicli context --provider nvidia --model gpt-4
aicli context --model gpt-4 --step 5000

# 管道 / JSON 模式
echo "Hello" | aicli pipe --model gpt-4 --timeout 120

# MCP 子命令
aicli mcp --help

# 登录或更新 provider，并校验 models endpoint 后写回 config.yaml
aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key sk-... --set-default
aicli login --provider local --protocol openai --base-url http://127.0.0.1:4000 --models-path /v1/models
aicli login --provider codex --protocol codex-oauth --base-url https://api.openai.com --auth-ref codex --set-default
aicli login --provider openai --base-url https://new.example.com --dry-run --json

# 交互式聊天（默认）
aicli
aicli --provider CODEX_04 --model gpt-5.4-mini

# 显式进入 chat（与直接运行 aicli 等价）
aicli chat --provider CODEX_04 --model gpt-5.4-mini

# 非交互 chat / session 恢复 / 图片输入
aicli chat --no-interactive --message "summarize this repo"
aicli chat --resume latest
aicli chat --list-sessions --session-state active --session-provider CODEX_04 --session-query runtime --session-limit 20
aicli chat --image ./screenshot.png --message "describe this screenshot"

# chat 中查看当前请求会暴露哪些 functions / skills
/functions 帮我生成一张图片

# 直接调用内置 tool（适合图片生成这类不依赖模型 tool-choice 的场景）
/call openai_image_generate {"prompt":"帮我生成一张海边日落照片"}
/tool openai_image_generate {"prompt":"帮我生成一张海边日落照片"}

# 直接调用 skill（会路由到 skill__imagegen）
/skill imagegen 帮我生成一张海边日落照片

# 显式指定配置
aicli -c ./mycfg.yaml config

# 全局选项
aicli --logfile ./aicli.log config
aicli --theme contrast config
aicli --envelope --output json config
```

完整子命令列表：

```bash
aicli --help
```

### chat 常用启动参数

| 类别 | 参数 | 说明 |
|---|---|---|
| provider/model | `--provider`、`--model`、`--reasoning-effort` | 指定本轮 chat 的 provider、模型和 reasoning effort |
| 非交互 | `--message`、`--no-interactive`、`--request-timeout` | 一次性发送消息并退出，适合脚本 |
| session | `--session`、`--resume`、`--list-sessions` | 加载指定 session、恢复最近 session 或列出历史 |
| session 过滤 | `--session-state`、`--session-provider`、`--session-model`、`--session-query`、`--session-limit` | 筛选可恢复 session |
| skills/tools | `--skills-dir`、`--skills-mode`、`--skills-debug`、`--tools-debug` | 控制 skills 暴露、路由和调试输出 |
| 权限 | `--permission-mode`、`--approval-reuse`、`--yolo` | 控制命令/编辑审批策略 |
| 多模态 | `--image/-i` | 为下一条消息附加图片 |

当前启动时不再自动弹出历史会话选择菜单；默认创建新会话。恢复历史会话请使用 `--resume`、`--session`、`/resume`、`/sessions` 或 `/load`。

### MCP 子命令概览

`aicli mcp` 支持常用管理动作：

- `add`
- `remove`
- `list`
- `status`
- `enable`
- `disable`
- `tools`
- `test`
- `test-server`
- `reload`

常用参数包括 `--config-file/-C`、`--transport`、`--header`、`--auth` 等；完整参数以 `aicli mcp --help` 和各子命令 `--help` 为准。

### chat 内置斜杠命令补充

进入交互式聊天后（无论是直接运行 `aicli` 还是显式执行 `aicli chat`），还支持直接在聊天输入中执行命令：

| 命令 | 用途 |
|---|---|
| `/help`、`/?` | 显示 slash 命令帮助；帮助内容由当前 catalog 渲染 |
| `/exit`、`/quit`、`/q` | 退出聊天 |
| `/clear`、`/cls` | 清空当前会话历史 |
| `/new` | 创建新会话 |
| `/session` | 显示当前会话信息 |
| `/status` | 显示当前会话状态 |
| `/debug` | 显示当前会话调试信息 |
| `/title <title>` | 更新当前会话标题 |
| `/history`、`/h` | 显示当前会话历史 |
| `/stream [on|off|toggle|status]` | 查看或切换流式输出 |
| `/s` | 开启流式输出，等价 `/stream on` |
| `/normal`、`/n` | 关闭流式输出，等价 `/stream off` |
| `/model [name|status|clear-reasoning|--provider ...]` | 查看或切换 provider/model/reasoning_effort |
| `/login [provider|--provider ...]` | 在 chat 内新增或更新 provider 登录凭证，并可刷新/切换当前模型 |
| `/compact [auto|local|remote]` | 手动触发会话压缩 |
| `/image [path|clear]` | 查看、添加或清空图片附件 |
| `/queue [status|clear]` | 查看或清空排队输入 |
| `/permission-mode [default|accept_edits|plan|bypass_permissions]`、`/mode` | 查看或切换权限模式 |
| `/approval-reuse [off|session_readonly_shell|team_readonly_shell]` | 查看或切换审批复用策略 |
| `/yolo` | 切换到 `bypass_permissions` |
| `/functions <prompt>` | 预览当前 prompt 会暴露哪些 builtin tools / skill functions |
| `/function <name>` | 查看单个 function 描述 |
| `/call <name> [args-json]` | 直接执行指定 function |
| `/tool <name> [args-json]` | `/call` 别名，便于直接执行 tool |
| `/skill <name> <prompt>` | 直接执行指定 skill，并把后面的文本作为 `prompt` |
| `/skills [query]` | 列出并选择执行 skill |
| `/sessions` | 列出或筛选可恢复会话 |
| `/load <session-id>` | 加载指定会话 |
| `/resume [latest|<session-id>]` | 恢复最近会话或指定会话；无参数时显示可恢复会话选择器 |
| `/agents [panel|pick|target|send|followup]` | 查看 agent tree、选择默认 agent target、向 child agent 投递消息或 follow-up |
| `/timeline [team|active] [limit] [filter=<text>]` | 查看 active team 或指定 team 的持久事件时间线 |
| `/collab [follow] [target|selected|parent|all] [limit] [filter=<text>] [timeout=10s]` | 查看 parent/child/team teammate 的 mailbox/collab 时间线 |
| `/shell <command>`、`/cmd <command>` | 执行 shell 命令并把输出分享给 AI |
| `!<command>` | `/shell` 快捷形式 |

说明：

- `/call` / `/tool` 适合直接执行 `openai_image_generate` 这类内置工具。
- `/skill imagegen ...` 会直接调用 `skill__imagegen`，由 skill 工作流转发到 `/v1/images/generations` provider。
- `/model` 支持 `status`、`clear-reasoning`、`--provider/-p`、`--model/-m`、`--reasoning-effort/-r`；切换后会刷新 provider、adapter、BaseURL、HTTP client、function builder、logger 和 runtime session metadata。
- `/login` 与 `aicli login` 共用 provider 登录逻辑，支持 API key、Codex OAuth、`--models-path`、`--default-model`、`--set-default`、`--dry-run` 和 JSON 输出。
- `/stream`、`/s`、`/normal` 会更新当前会话，并在可写配置存在时写回 `aicli.chat.stream`。
- `/resume latest` 会跳过当前正在使用的 runtime session 和只有 system prompt 的启动占位 session；交互式选择器只显示相对更新时间与清理后的标题，不再把 session id、provider 和 session file 路径塞进候选行。
- `spawn_team auto_start=true` 之后应使用 `wait_team` 等待持久 `team.completed` / `team.summary`；`wait_agent` / `read_agent_events` 面向 `spawn_agent` child session，不应拿 team member id 当 child session id。
- `/shell` / `/cmd` 支持 `--output-bytes-cap <bytes>` 与 `--disable-output-cap`；默认使用检测到的用户 shell。危险命令仍会进入确认/权限流程。
- builtin `execute_shell_command` function 支持 `command`、`workdir`、`output_bytes_cap`、`disable_output_cap`；Windows PowerShell/pwsh 下不要把 POSIX-only 命令如 `head` 当默认可用命令。
- background toolbroker 能力包括 `background_task` 和 `task_output`；HTTP 观测入口见 `docs/skill_runtime/runtime_operations_api.md` 的 Background Jobs 章节。
- 当 `aicli.mcp.auto_connect=false` 且 `config_file` 不存在时，chat 会跳过 MCP 初始化，不再为缺失的默认 `configs/mcp.yaml` 打印 warning。

---

## 四、卸载

**通过 Makefile 安装的**：

```bash
make uninstall-aicli
```

**通过安装脚本安装的（Linux / macOS）**：

```bash
rm -f "$HOME/.local/bin/aicli"
# 或自定义路径
rm -f "$AICLI_INSTALL_DIR/aicli"
```

**Windows**：

```powershell
Remove-Item "$env:LOCALAPPDATA\Programs\aicli\aicli.exe"
# 如需从 PATH 中移除，可手动编辑用户环境变量
```

---

## 五、相关链接

- [GitHub Releases](https://github.com/wwsheng009/ai-agent-runtime/releases)
- [Release workflow 源码](../../.github/workflows/release-aicli.yml)
- [完整配置示例](../../backend/configs/config.yaml)
- [项目主 README](../../README.md)
