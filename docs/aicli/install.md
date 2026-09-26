# aicli 安装与配置

`aicli` 是 `ai-agent-runtime` 提供的命令行工具。当前默认入口是 chat，同时支持 provider 登录、session/resume、slash commands、tools/skills、shell/background、MCP、配置查看、端点测试、上下文测试和管道模式。

直接运行 `aicli` 会默认进入交互式 chat 模式；`aicli chat` 仍然是显式且等价的入口。

本文档涵盖：

- [零、第一次使用（推荐路径）](#零第一次使用推荐路径)
- [一、安装](#一安装)
- [二、配置](#二配置)
- [三、常用命令](#三常用命令)
  - [MCP / skill / plugin / agent 概览](#mcp-子命令概览)
- [四、常见问题（FAQ）](#四常见问题faq)
- [五、卸载](#五卸载)
- [六、相关链接](#六相关链接)

最短上手路径见 [quickstart.md](./quickstart.md)；独立排错清单见 [faq.md](./faq.md)。

---

## 零、第一次使用（推荐路径）

如果你只是想尽快用上 `aicli`，也可直接看独立文档 [quickstart.md](./quickstart.md)。下面是同一条最短路径的摘要：

```text
安装 aicli
  → 初始化/确认配置（~/.aicli/config.yaml）
  → aicli login 登录 provider
  → aicli 进入 chat，用 /model 切换模型
```

**首次安装 checklist**（细节与成功信号见 [quickstart.md](./quickstart.md#0-首次安装-checklist含成功信号)）：

| 步骤 | 命令 | 成功信号 |
|---|---|---|
| 0. 安装 | 安装脚本 / Release / `make install-aicli` | `aicli version` 打印版本号 |
| 1. 初始化 | `aicli init --global` | `aicli config` 能读到配置路径 |
| 2. 登录 | `aicli login ... --set-default` | `aicli provider list` 可见 provider；`aicli doctor provider` 不报致命错误 |
| 3. 使用 | `aicli` | 模型能回复；`/model status` 显示当前 provider/model |

```bash
# 1) 安装后验证
aicli version

# 2) 初始化用户级配置（推荐）
aicli init --global
aicli config   # 成功信号：能读到配置路径

# 3) 登录 provider（会校验 models endpoint 并写回 config）
# 建议 API key 走环境变量，避免写进 shell 历史
export OPENAI_API_KEY=sk-...
aicli login \
  --provider openai \
  --protocol openai \
  --base-url https://api.openai.com \
  --api-key "$OPENAI_API_KEY" \
  --set-default

# 4) 进入 chat
aicli
# 在 chat 内：
#   /model status
#   /model gpt-4.1
#   /help
```

**Windows PowerShell 登录示例**

```powershell
$env:OPENAI_API_KEY = 'sk-...'
aicli login `
  --provider openai `
  --protocol openai `
  --base-url https://api.openai.com `
  --api-key $env:OPENAI_API_KEY `
  --set-default
```

登录后建议先检查（成功信号）：

```bash
aicli provider list
aicli doctor provider
aicli doctor provider --provider openai --model gpt-4.1
aicli config --models
```

也可以在 chat 内执行 `/login`，与 `aicli login` 共用同一套登录逻辑。

更细的安装方式、配置字段、slash 命令和卸载说明见下文各章节。

---

## 一、安装

> Windows 7 需要使用 Go 1.21.4 构建的独立兼容包，不能使用普通 Windows
> Release。下载、终端选择和故障处理见
> [《在 Windows 7 上使用 aicli》](./windows7.md)。

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

Release 归档同时包含经过 SHA-256 校验的固定版本 ripgrep，安装在 `codex-path/rg`（Windows 为 `codex-path/rg.exe`）。结构化 `grep` / `glob` 按以下顺序解析搜索后端：

1. `AICLI_RG_PATH` 指定的可执行文件。
2. `aicli` 同目录下的 `codex-path/rg`。
3. `aicli` 相邻目录或 `resources/rg`。
4. 系统 `PATH` 中的 `rg`。
5. toolkit 内置扫描器。

可通过以下命令检查实际使用的路径、来源、版本和回退状态：

```bash
aicli doctor search
aicli doctor search --json
```

源码编译和 `go install` 不会自动下载额外二进制；它们默认使用系统 `PATH` 中的 `rg`，找不到时回退 builtin。需要固定自定义路径时设置 `AICLI_RG_PATH`。

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
| 1 | `./.aicli/config.yaml` | 项目级配置（cwd 下 `.aicli/`；**优先于用户级**） |
| 2 | `$HOME/.aicli/config.yaml` | 用户级全局配置 |
| 3 | `./aicli.yaml` | 项目级单文件配置（遗留） |
| 4 | `./config.yaml` | 遗留散落文件 |
| 5 | `./configs/config.yaml` | 便携默认（向后兼容） |

默认是**单文件语义**（`AICLI_CONFIG_MERGE` 未开启）：只加载首个命中的文件，低层不补键；需要跨层合并时设
`AICLI_CONFIG_MERGE=on`（低→高逐层合并，高层只覆盖自己显式写的键，显式 `null` 删除下层键），`dry-run` 只预览不改变行为。

所有默认候选位置都不存在时，当前 `aicli` 会优先创建用户级 starter 配置 `$HOME/.aicli/config.yaml`（这是**创建**落点，
不是读取优先级）；如果用户目录不可用，则回退到当前工作目录的 `./.aicli/config.yaml`。starter 中默认开启
`aicli.chat.stream: true`，并保留空的 `providers.items`，方便后续通过 `aicli login` 或手工编辑补 provider。

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
    terminal_title:
      enabled: true
      animations: true
      # activity/project are defaults (icon only; no Ready/Working prose).
      # Optional Codex-aligned items: state, model, thread, git-branch (or branch), app-name.
      items: [activity, project]
  # 双轴主题：mode=明暗，name=配色（也可用环境变量 AICLI_THEME_MODE / AICLI_THEME）
  theme:
    mode: auto          # auto | dark | light
    name: focus         # classic | focus | contrast | mono
  log:
    file_path: ${AICLI_LOG_FILE_PATH:-~/.aicli/logs/aicli.log}
```

完整字段示例见 [`backend/configs/config.yaml`](../../backend/configs/config.yaml)。

### 自定义上游请求 Header

可以在 `providers.headers` 中声明所有 provider 共用的请求 Header，也可以在 `providers.items.<name>.headers` 中为单个 provider 覆盖或补充。Header 名称按大小写不敏感方式匹配；同名时 provider 级配置优先。配置值同样支持 `${VAR}` 和 `${VAR:-default}` 环境变量展开。

```yaml
providers:
  headers:
    X-Upstream-Client: ${UPSTREAM_CLIENT_ID:-aicli}
    X-Upstream-Route: default
  items:
    private_upstream:
      enabled: true
      protocol: openai
      base_url: https://llm.example.com
      api_key: ${PRIVATE_UPSTREAM_API_KEY}
      default_model: example-model
      headers:
        x-upstream-route: private
        X-Upstream-Token: ${PRIVATE_UPSTREAM_TOKEN}
```

以上配置对普通聊天、`pipe`、`test`、`context`、模型列表校验、runtime-server 调用以及图片生成请求生效。最终发送的 `X-Upstream-Route` 为 `private`，同时保留全局的 `X-Upstream-Client` 和 provider 专属的 `X-Upstream-Token`。

`aicli.chat` 偏好优先级：

1. 命令行 flag，例如 `--provider`、`--model`、`--reasoning-effort`、`--stream`
2. 已加载 session 的 provider/model/reasoning/stream metadata
3. `aicli.chat.default_provider`、`default_model`、`reasoning_effort`、`stream`
4. 交互式选择结果
5. provider 的默认模型

`/model`、`/stream`、`/s`、`/normal` 等 chat 内命令会同步更新当前 session；在具备可写配置路径时，也会把相关偏好写回 `aicli.chat`。

交互式 `aicli chat` 默认会通过 OSC 0 更新终端窗口或标签标题。默认 `items` 为 `activity` + `project`：`activity` 只显示工作中的状态图标/spinner（空闲时不显示），需要用户操作时显示 `[ ! ] Action Required`；`project` 显示当前目录名。可选 `state` 才会追加 `Ready` / `Waiting` / `Working` 等文字标签（默认不启用，避免图标与描述同时出现）。`items` 还支持 `model`、`thread`、`git-branch` 和 `app-name`；设置 `animations: false` 可保留状态图标但关闭动画，设置 `enabled: false` 可完全关闭标题更新。非 TTY、非交互模式、JSON 输出以及不支持 ANSI 标题的终端会自动跳过。

### 环境变量

配置中 `${VAR:-default}` 语法支持从环境变量注入。常见 API key 变量：

```bash
export OPENAI_API_KEY=sk-xxxxx
export NVIDIA_API_KEYS=nvapi-xxxxx
export DEEPSEEK_API_KEY=sk-xxxxx
export BIGMODEL_API_KEYS=xxxxx
export GEMINI_API_KEY=xxxxx
```

Windows PowerShell：

```powershell
$env:OPENAI_API_KEY = 'sk-xxxxx'
$env:NVIDIA_API_KEYS = 'nvapi-xxxxx'
$env:DEEPSEEK_API_KEY = 'sk-xxxxx'
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

# skill 安装到目标工具目录（codex|aicli|workspace）
aicli skill install
aicli skill install aicli --target codex --dry-run

# 本地 plugin 安装 / 信任 / 启用（无 marketplace）
aicli plugin install ./my-plugin
aicli plugin list
aicli plugin trust my-plugin

# ACP 子集宿主（stdin/stdout NDJSON；详见 agents.md）
aicli agent stdio --provider openai --model gpt-4o

# 登录或更新 provider，并校验 models endpoint 后写回 config.yaml
aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key sk-... --set-default
aicli login --provider local --protocol openai --base-url http://127.0.0.1:4000 --models-path /v1/models
aicli login --provider codex --protocol codex-oauth --base-url https://api.openai.com --auth-ref codex --set-default
aicli login --provider openai --base-url https://new.example.com --dry-run --json

# 诊断 provider 调用链（可复现矩阵；不是裸 `aicli doctor`）
aicli doctor provider
aicli doctor provider --provider openai --model gpt-4.1
aicli doctor subagent-route --role writer --difficulty hard

# 交互式聊天（默认）
aicli
aicli --provider CODEX_04 --model gpt-5.4-mini
aicli --prompt "检查当前项目"             # 自动提交一次，完成后继续留在交互界面

# 显式进入 chat（与直接运行 aicli 等价）
aicli chat --provider CODEX_04 --model gpt-5.4-mini

# 非交互 chat / session 恢复 / 图片输入
aicli chat --no-interactive --prompt "summarize this repo"
aicli resume                              # 顶层恢复当前工作目录的最近会话（等价 aicli chat --resume）
aicli resume --cwd=false                  # 跨工作目录恢复最近会话
aicli resume session_xxx                  # 顶层加载指定会话（等价 aicli chat --session）
aicli chat --resume                       # 兼容写法：恢复当前工作目录的最近会话
aicli chat --session session_xxx          # 兼容写法：加载指定会话
aicli chat --list-sessions --session-state active --session-provider CODEX_04 --session-query runtime --session-limit 20
aicli chat --image ./screenshot.png --prompt "describe this screenshot"

# 会话导出（chat 内 /export 的顶层等价入口；失败返回非零退出码）
aicli export                                    # 最近一次会话 → 完整 JSON
aicli export latest --trace                     # Markdown + 工具调用与结果
aicli export latest --body --output ./session.md
aicli export session_xxx --full --dir ./exports

# 会话导入（export --full 的反向入口；默认沿用原 ID 与用户，冲突不覆盖）
aicli import ./exports/session_xxx_full.json
aicli import ./session.json --new-id             # ID 已存在时生成新 ID 并存
aicli import ./session.json --user alice         # 覆盖会话归属
aicli import ./session.json --dry-run --json     # 只预检，输出机器可读摘要

# chat 中查看当前请求会暴露哪些 functions / skills
/functions 帮我生成一张图片

# 独立图片生成子命令（直接复用 openai_image_generate）
aicli image "帮我生成一张海边日落照片"
aicli image --provider SENSENOVA_IMAGE --model sensenova-u1-fast "生成一张海报"

# 直接调用内置 tool（适合图片生成这类不依赖模型 tool-choice 的场景）
/call openai_image_generate 帮我生成一张海边日落照片
/call openai_image_generate {"prompt":"帮我生成一张海边日落照片"}
/tool openai_image_generate 帮我生成一张海边日落照片
/tool openai_image_generate {"prompt":"帮我生成一张海边日落照片"}

# 直接调用 skill（会路由到 skill__imagegen）
/skill imagegen 帮我生成一张海边日落照片

# 显式指定配置
aicli -c ./mycfg.yaml config

# 全局选项
aicli --logfile ./aicli.log config
aicli --theme contrast config          # 切换配色
aicli --theme dark config              # 仅切换明暗
# 环境变量（优先级: --theme > AICLI_THEME/AICLI_THEME_MODE > 配置）
# AICLI_THEME=contrast AICLI_THEME_MODE=light aicli config
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
| 启动消息 | `--prompt`、`-M/--message` | 初始化和历史恢复后自动提交一次；`--message` 是兼容别名；两者同时使用时值必须相同 |
| 非交互 | `--no-interactive`、`--request-timeout` | 启动消息完成后退出，适合脚本 |
| session | `--session`、`--resume`、`--list-sessions` | 加载指定 session、恢复最近 session 或列出历史 |
| session 过滤 | `--cwd`、`--session-state`、`--session-provider`、`--session-model`、`--session-query`、`--session-limit` | 默认按当前工作目录筛选；使用 `--cwd=false` 查看全部目录，或叠加其他筛选条件 |
| skills/tools | `--skills-dir`、`--skills-mode`、`--skills-debug`、`--tools-debug` | 控制 skills 暴露、路由和调试输出 |
| 权限 | `--permission-mode`、`--approval-reuse`、`--yolo` | 控制命令/编辑审批策略 |
| 多模态 | `--image/-i` | 为下一条消息附加图片 |

当前启动时不再自动弹出历史会话选择菜单；默认创建新会话。恢复历史会话请使用 `--resume`、`--session`、`/resume`、`/sessions` 或 `/load`。

### 会话存储与长会话内存上限

持久会话默认使用 SQLite。完整 canonical transcript 追加写入 `session_messages`，运行时只加载有界的 `session_prompt_messages` 投影；compact 只替换 prompt projection，不覆盖 compact 前的 canonical transcript。会话列表只读取 metadata，历史接口使用 `before_seq` 游标按页向前读取，因此恢复和列表不会随完整历史长度线性占用内存。

```yaml
sessions:
  backend: sqlite
  # 相对路径以 sessions.dir 为基准；留空时使用 session_history.sqlite
  storePath: session_history.sqlite
  maxHistory: 128
  hotHistoryBytes: 2097152
  maxHotMessageBytes: 131072
  historyPageMessages: 100
  historyPageBytes: 4194304
  maxInlineMessageBytes: 524288
  sqliteCacheKiB: 2048
  busyTimeout: 5s
```

- 单条 canonical 消息超过 `maxInlineMessageBytes` 时，正文按内容哈希写入 `session-artifacts/<session-id>/`，SQLite 保存路径、大小、校验值和有界预览。
- SQLite 使用单连接、WAL、`synchronous=NORMAL`、文件临时表、禁用 mmap 和小页缓存；关闭存储时执行 WAL truncate checkpoint，新数据库启用 incremental auto-vacuum。
- 首次切换到 SQLite 时会流式导入 sessions 目录中的旧 JSON 会话。旧 JSON 默认保留作为回滚源，不会自动删除。
- `resume latest`、会话选择和 slash 补全按 100 条 metadata/preview 分页读取；清理和 idle 归档按最多 128 条一批执行，避免会话文件或过期会话总数抬高峰值内存。
- `/export --full` 从 canonical JSON 流式写出；新写入的外置 canonical 消息会边读取边校验 SHA-256，不需要把整条大 artifact 读入 Go 内存。导出先写同目录临时文件，成功后再发布，失败不会留下半文件或覆盖旧目标。
- 需要兼容旧格式时可显式配置 `sessions.backend: file`；该模式仍会按旧 JSON 文件读写，不具备 SQLite canonical transcript、artifact 外置和游标分页的完整能力。
- `GET /api/runtime/sessions/{id}/history?limit=100&before_seq=<cursor>` 返回 `first_seq`、`last_seq`、`next_before_seq` 和 `has_more`；继续加载旧历史时传回 `next_before_seq`，不要一次请求完整会话。
- runtime HTTP debug artifacts 每个会话最多保留 256 个 JSON 文件且总量最多 64 MiB，超限时自动删除最旧文件；单次 debug raw body 捕获固定上限为 256 KiB，并保留首尾片段和原始字节数。

### MCP 子命令概览

`aicli mcp` 支持常用管理动作：

> 只想尽快跑通？见 [docs/mcp/quickstart.md](../mcp/quickstart.md)（一分钟 quickstart + 复制即用 recipes + 症状式排错表）。

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
- `auth`
- `logout`

常用参数包括 `--config-file/-C`、`--transport`、`--header`、`--env` 等；完整参数以 `aicli mcp --help` 和各子命令 `--help` 为准。

`aicli mcp add` 的目标与传输类型：

- 未显式指定 `--transport` 时按目标推断：`http(s)://` → `streamable`；`ws(s)://` → `websocket`；本地命令 → `stdio`。推断结果会在添加输出里提示，可用 `--transport` 显式覆盖。
- stdio 推荐写法：名称在 `--` 之前，命令与其参数在 `--` 之后，例如
  `aicli mcp add chrome-devtools -- npx -y chrome-devtools-mcp@latest`；
  `--command <cmd>` 是保留的兼容写法。
- `--env KEY=VALUE` 与 `--header "Key: Value"` 均可重复；header 会镜像为 `HEADER_*` 环境变量（与 console / 微型 Web 面板同一约定）。
- `--auth oauth` 启用 OAuth（`--oauth-scope` / `--oauth-client-id` / `--oauth-callback-port` 详见下文「MCP OAuth」）；`--auth none` 清除认证；
  服务只接受静态凭证时用 `--header` / `--env` 传入。

MCP 配置中的环境变量插值（作用于 `url` / `command` / `env` 与 `args` / `headers`）：

- `${NAME}`：严格语义；变量未设置时该 server 会记录 `EnvError`（内容形如「引用未设置的环境变量: NAME」），变量已设置但为空串视为合法。
- `${NAME:-default}`：变量未设置或为空时使用默认值；`$${NAME}` 展开为字面量 `${NAME}`。
- 历史 `$NAME` 语法仅在 `url` / `command` / `env` 上按旧行为展开（未设置→空串）；`args` / `headers` 只识别 `${...}` 显式语法，避免误伤 `$1`、`cost$5` 这类字面量。
- **配置文件始终保留原始 `${NAME}` 字面量**：展开只发生在运行时入口（manager 建立连接时）。因此 `mcp add/enable/disable/remove` 等管理操作不会把展开后的真实值（或空串）写回文件，也不会把密钥固化进配置。
- 缺变量的 server 会被隔离：不参与连接，但会在 `aicli mcp list` / `aicli mcp status <名称>` 的「最近错误」、`/mcp` 与 Web 面板的错误列中显示；其它 server 与管理操作不受影响。

MCP OAuth（`--auth oauth` + `aicli mcp auth`）：

- 启用：`aicli mcp add <名称> <URL> --auth oauth [--oauth-scope read ...] [--oauth-client-id ...] [--oauth-callback-port 3344]`；
  也可直接写配置：`auth: oauth` 或结构化 `auth: {type: oauth, clientId: ..., scopes: [...], callbackPort: ...}`；`--auth none` 清除认证。
- 登录：`aicli mcp auth <名称>`（浏览器 + 本地回调，Authorization Code + PKCE S256；未配置 `clientId` 时尝试动态客户端注册 RFC 7591）。
  浏览器打不开/无桌面环境时用 `aicli mcp auth <名称> --no-browser`：复制打印的授权 URL 手动访问，再把回调 URL（或 code）粘贴回终端。
- 查看与清理：`aicli mcp auth --status`、`aicli mcp auth --list`（只输出元数据，绝不打印令牌明文）、
  `aicli mcp auth --clear <名称>` / `aicli mcp auth --clear --all`，或 `aicli mcp logout <名称> [--all]`。
- chat 内同口径可用：`/mcp auth`（状态）、`/mcp auth <名称>`（发起，无桌面时加 `--no-browser`）、
  `/mcp auth <名称> <回调URL|code>`（回调页面打不开时粘贴完成）、`/mcp auth <名称> --clear`。
  chat 不能阻塞终端读 stdin，因此把流程拆成多个用户回合：起流程打印授权链接，浏览器回调到达后再执行
  一次 `/mcp auth <名称>` 即兑换；或直接粘贴回调 URL/code。与 CLI 共用同一套 PKCE 流程与 `~/.aicli/mcp-tokens.json`，
  完成后自动热重载并刷新会话工具面；选择器的「认证 / 重新认证 / 完成授权 / 清除授权」动作走同一文本通道。
- 令牌存 `~/.aicli/mcp-tokens.json`（写入为 0600 且原子替换，目录 0700），可用 `AICLI_MCP_TOKENS_FILE` 覆盖路径；
  server URL 变化时同名旧令牌会被忽略，避免把旧站令牌发给新站。
- 401/403 时会用 refresh_token 自动刷新并重试一次；未登录或刷新失败时该 server 被隔离为「需认证」
  （`aicli mcp list` / `aicli mcp status`、`/mcp`、微型 Web 面板均可见），不影响其它 server。
- 目前支持 streamable 与 sse；websocket / stdio 请用 `headers` 配置静态凭证。Win7 兼容构建不会自动打开浏览器，始终打印授权链接。
- 安全提示：`--oauth-client-secret` 会明文写入配置文件（公共客户端留空即可）。

配置分层与写入层级（`--scope`）：

- **同名覆盖**：解析链上的所有已存在文件按「低 → 高」合并，低优先级提供基础项，高优先级**整体覆盖**同名 server（不做字段级合并）。
  层级顺序（低→高）：`configs/mcp.yaml`（向上搜索 / 默认） < `~/.aicli/mcp.yaml`（user） < `./.aicli/mcp.yaml`（project）
  < `~/.aicli/projects/<项目标识>/mcp.yaml`（local，项目私有，优先级最高）。
  `--config-file` / `MCP_CONFIG_FILE` 指定**真实覆盖**路径时退化为「只加载该文件」，不合并。
- 合并结果可见：`aicli mcp list` / `mcp status` 会打印 `来源: <层级> (<文件>)`；被覆盖的低优先级定义打印 `覆盖: <层级> (<文件>)`。
  `--output json` 的 `configSource` / `configPath` / `shadowedSources` 字段、chat `/mcp` 面板与微型 Web 面板同源展示。
- 写入目标：`aicli mcp add ... --scope user|local|project`
  - `user`（默认）→ `~/.aicli/mcp.yaml`
  - `local` → `~/.aicli/projects/<项目标识>/mcp.yaml`（项目私有，不进版本库）
  - `project` → `<项目>/.aicli/mcp.yaml`（可提交共享）
  `enable` / `disable` / `remove` 不需要 `--scope`：它们会作用在**实际定义该 server 的文件**上（删除项目级定义后，用户级同名定义自动重新生效）。
- **项目级秘密剥离**：`--scope project` 拒绝明文凭证（`Authorization`/`api-key`/`token`/`secret`/`password`/`cookie` 等 header 或 env 值、`auth.clientSecret`）。
  请改写成 `${VAR}` 引用（例如 `--header "Authorization=Bearer ${MY_TOKEN}"`），或改用 `--scope user` / `--scope local`。
  写入项目级时会提示：「若仓库 `.gitignore` 忽略了 `.aicli/`，请追加豁免 `!.aicli/mcp.yaml`」。
- 低优先级文件损坏时：跳过该层并打印告警（`警告: 已跳过 ...`，runtime-server 记入日志），不会让整次加载失败；最高优先级文件损坏仍然直接报错。

从其它 agent 工具导入（`aicli mcp import`）：

| 来源 (--from) | 扫描的文件 |
|---|---|
| `claude` | `~/.claude.json`（含 `projects.<path>.mcpServers`）、`<项目>/.mcp.json` |
| `cursor` | `~/.cursor/mcp.json`、`<项目>/.cursor/mcp.json` |
| `gemini` | `~/.gemini/settings.json`、`<项目>/.gemini/settings.json` |
| `opencode` | `~/.config/opencode/opencode.json`、`<项目>/opencode.json` |
| `codex` | `~/.codex/config.toml` 的 `[mcp_servers.*]`（TOML 子集解析） |
| `all`（默认） | 以上全部；同一来源内项目级覆盖用户级 |

```bash
aicli mcp import --dry-run                      # 预览：将导入 N、改名 M、跳过 K
aicli mcp import --from claude --scope user     # 导入到个人全局
aicli mcp import --from codex --scope project   # 导入到项目配置（明文凭证会被 ${VAR} 化）
aicli mcp import --on-conflict rename --only context7
```

| 参数 | 说明 |
|---|---|
| `--scope` | `user` / `local`（默认）/ `project`，语义见上文 |
| `--dry-run` | 只输出计划，**不写任何文件**（也不会创建目标文件） |
| `--on-conflict` | `skip`（默认）/ `overwrite` / `rename`（改名 `<name>-imported`） |
| `--on-secrets` | `mask`（默认，仅 `--scope project` 生效）/ `reject` / `keep` |
| `--only` | 只导入指定 server（可重复） |

映射规则：`http`→`streamable`、`sse`→`sse`、`ws/websocket`→`websocket`、`stdio`→`stdio`；缺 `type` 时按 `url`/`command` 推断；
`headers/http_headers` 映射到 `headers`，`env` 保留（`${VAR}` 引用原样保留）；`startup_timeout_ms` 换算为秒；
`enabled: false` 保留为 aicli 的 `enabled: false`，`disabled: true` 保留 MCP 官方兼容形态；
未映射的字段与不支持的 TOML 构造（数组表、嵌套子表）会出现在导入摘要的告警里，不会让整份导入失败。

单 server 导出与 JSON 直填（脚本/跨机复制）：

```bash
aicli mcp get context7                       # 文本：类型/URL/命令/Headers/Env/来源
aicli mcp get context7 --json | jq -c .config   # 拿到可直接复制的 JSON
aicli mcp add-json context7 '{"type":"http","url":"https://mcp.context7.com/mcp","headers":{"Authorization":"Bearer ${CONTEXT7_TOKEN}"}}'
aicli mcp add-json local-fs '{"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/data"]}'
aicli mcp add-json notion '@./notion.json'   # 整份 JSON 放文件里（避免 shell 引号地狱）
aicli mcp get context7 --json | jq -c .config | aicli mcp add-json context7 -   # 从管道读
```

`add-json` 接受 `type` 别名（`http`/`sse`/`ws`/`stdio`）与 `get --json` 的 `.config` 片段，
同样受 `--scope` 与项目级秘密剥离约束；`mcp get` 显示的是**分层合并后**生效的那一份配置（含 `configSource`）。
未映射的字段会在输出末尾以「提示: 忽略未映射字段 …」列出，避免把 `headers` 写成 `header` 时静默丢配置。

整份 JSON 文件（多 server）用 `mcp import --from json`，与厂商来源共用同一套冲突/秘密策略：

```bash
aicli mcp import --from json ./.mcp.json --dry-run          # 先看计划
aicli mcp import --from json ./team-mcp.json --scope project --on-conflict rename
```

文件形态（顶层）支持四种：`{"mcpServers":{...}}`（Claude/Cursor/aicli 同构）、`{"servers":{...}}`（VS Code 风格）、
单 server 对象（`{"name":"notion","url":"..."}`，也接受 `mcp get --json` 的 `{name, config}` 导出）、
以及对象数组（每项自带 `name`）。路径可以写成位置参数或 `--file`（两者都给时必须一致）。
只想加一个 server 时用 `add-json`；容器形态（`mcpServers`）交给 `import`，`add-json` 会拒绝并给出这条命令。

chat 里的交互菜单（TUI）：

```
/mcp            # 打开选择器：选择 server → 选择动作
/mcp select     # 同上（别名 pick / menu / choose）
/mcp list       # 始终是纯文本列表（脚本口径，不开选择器）
/mcp add-json notion '{"url":"https://mcp.notion.com/mcp"}'   # 也可 /mcp add-json notion @./notion.json
```

选择器的第一层列出全部 MCP（行首状态标记 `●` 已连接 / `!` 需认证 / `◐` 已启用未连接 / `○` 已停用，
详情行带 endpoint、分层来源与同名覆盖链），支持输入即时过滤；第二层是动作：

| 动作 | 等价命令 |
|---|---|
| 查看状态 | `/mcp status <name>` |
| 启用 / 停用 | `/mcp enable｜disable <name>` |
| 热重载全部 MCP | `/mcp reload` |
| 移除 | `/mcp remove <name>`（二次确认） |

动作在选择器**释放备用屏之后**才执行，且复用同一套 `/mcp` 文本通道（唯一写路径）；
非交互、非 ANSI TTY、`--output json` 或运行中有关键帧时，`/mcp` 自动降级为纯文本列表面板。
OAuth 认证目前仍在 CLI（`aicli mcp auth login <name>`）与微型 Web 面板里完成，chat 选择器暂不提供「认证」动作。

MCP 配置文件解析顺序（chat 会话、`aicli mcp *`、console / 微型 Web 面板、runtime-server 共用同一套）：

| 优先级 | 路径 | 说明 |
|---|---|---|
| 0 | session / profile 显式指定，或 `config_file` 指向**非约定路径** | `--profile`、session 级覆盖，或 `aicli.mcp.config_file` / `MCP_CONFIG_FILE` 写成自定义路径时直接胜出 |
| 1 | `~/.aicli/projects/<项目标识>/mcp.yaml` | **local 层**：个人为本项目追加的私有配置（`--scope local` 写入，不进版本库） |
| 2 | `./.aicli/mcp.yaml` | **工作区级 / project 层**（cwd 下的 `.aicli/`，该目录默认在 `.gitignore`） |
| 3 | `~/.aicli/mcp.yaml` | user 层 |
| 4 | 从 cwd 逐级向上搜索 | 每级先 `.aicli/mcp.yaml`，再 `configs/mcp.yaml` |
| 5 | 可执行文件目录逐级向上搜索 | 覆盖从无关目录启动的场景 |
| 6 | `configs/mcp.yaml` | 兜底（`config_file` 为约定值时返回该字面路径） |

`aicli.mcp.config_file` **未设置（或为空）不等于“未配置”**：解析器按“发现”语义依序查找上表 1→5 层，
local 层（若存在）、工作区 `./.aicli/mcp.yaml`、用户 `~/.aicli/mcp.yaml`、向上搜索命中即用，无需用户先写 `config_file`。
只有磁盘上**任何候选都不存在**时才回到“未配置”（空路径，chat 静默跳过，`aicli mcp list` 报无服务器）；
第 6 行的字面兜底仅适用于 `config_file` 显式写成约定值（如 `configs/mcp.yaml`）的场景。
`MCP_CONFIG_FILE` 环境变量优先于 YAML 值（见 `agentconfig.EffectiveAICLIMCPConfigFile`）。

**加载**不再是“命中即用”，而是把上表里**所有已存在**的文件按 6→1 的「低 → 高」顺序合并（同名整体覆盖，见上文「配置分层」）；
1 号位（local 层）优先级最高，其次是 project，再是 user / upward / default；被覆盖的定义会在 `mcp list` 中以 `来源` / `覆盖` 两行显式说明。
`config_file` 写成真实覆盖路径（非约定值）时不合并，只加载该文件。

`aicli mcp add` / `/mcp add` 的**写入**路径默认与上表一致：命中哪个文件就写哪个；若全部不存在，则创建 `~/.aicli/mcp.yaml`（runtime-server 同样落到用户级，避免在任意工作目录生成 `configs/mcp.yaml`）。
显式 `--scope user|local|project` 时改为强制写入对应层级（`project` 会拒绝明文凭证）。`enable` / `disable` / `remove` 始终作用在定义该 server 的文件上。

### skill 安装概览

`aicli skill`（别名 `skills`）把 Codex 风格 skill 目录（含 `SKILL.md`）安装到目标工具的 skills 根目录：

```bash
aicli skill install                         # 默认安装内置 aicli skill
aicli skill install aicli --target codex
aicli skill install aicli --target aicli
aicli skill install aicli --target workspace
aicli skill install aicli --source-dir .\.agents\skills --dry-run --output json
```

| 参数 | 说明 |
|---|---|
| `--target` | `codex` / `aicli` / `workspace`；被 `--target-dir` 覆盖 |
| `--target-dir` | 显式 skills 根目录；最终落到 `<target-dir>/<name>` |
| `--source-dir` | 单个 skill 目录，或包含多个 skill 子目录的根 |
| `--dry-run` / `--force` | 预览或覆盖已存在目标 |

chat 内 skills **暴露 / 路由**（默认启用、top-k、exec 假阴性）见 [skill_runtime/aicli_skills_usage.md](../skill_runtime/aicli_skills_usage.md)。  
角色 agent 与 skill 内 `agents/openai.yaml` 的区别见 [agents.md](./agents.md)。

### plugin 本地包概览

`aicli plugin`（别名 `plugins`）管理**本地** plugin 包，不做 marketplace：

```bash
aicli plugin install ./my-plugin
aicli plugin install ./my-plugin --trust
aicli plugin list
aicli plugin trust my-plugin
aicli plugin enable my-plugin
aicli plugin disable my-plugin
aicli plugin untrust my-plugin
```

要点：

- plugin 是带 `plugin.yaml` 的目录，可贡献 `skills/`、`agents/`、hooks、MCP
- 默认安装到 `~/.aicli/plugins`（或 `$AICLI_HOME/plugins`）
- **新安装默认 untrusted**：不会向 runtime 贡献，直到 `aicli plugin trust <name>`
- 已信任后仍可用 `enable` / `disable` 控制是否贡献

skills 暴露见 [skill_runtime/aicli_skills_usage.md](../skill_runtime/aicli_skills_usage.md)；agent 定义层见 [agents.md](./agents.md)。

### agent ACP 宿主概览

`aicli agent stdio` 以 Agent Client Protocol (ACP) **子集**在 stdin/stdout 上服务（JSON-RPC 2.0 over NDJSON）。  
这与 `aicli chat --agent`（Portable AgentDefinition / profile 角色）不是同一入口：

| 入口 | 用途 |
|---|---|
| `aicli chat --agent <name>` | 交互/非交互 chat 绑定角色 def |
| `aicli agent stdio` | 外部 IDE/客户端协议宿主；stdin 是协议流，不是 prompt 文本 |
| `aicli exec` | headless 工具代理（CI/脚本） |

支持方法：`initialize`、`session/new`、`session/prompt`、`session/cancel`、`session/load`（`loadSession=true`，回放后返回空对象 `{}`；返回 `null` 会导致 Zed 反序列化失败）。  
默认 `--ephemeral`：`session/load` 仅能 reattach 本进程 `session/new` 的 id；跨进程恢复需 `--session-dir`。

```bash
aicli agent stdio --provider openai --model gpt-4o
aicli agent stdio --profile default --permission-mode default
aicli agent stdio --yolo --enable-tools
aicli agent stdio --session-dir ~/.aicli/sessions
```

角色 / permission / profile 概念见 [agents.md](./agents.md#9-acp-宿主-aicli-agent-stdio)（运行 profile 见 [profiles.md](./profiles.md)）；headless 输出契约见 [exec.md](./exec.md)。

### chat 内置斜杠命令补充

进入交互式聊天后（无论是直接运行 `aicli` 还是显式执行 `aicli chat`），还支持直接在聊天输入中执行命令：

| 命令 | 用途 |
|---|---|
| `/help`、`/?` | 显示 slash 命令帮助；帮助内容由当前 catalog 渲染 |
| `/hotkeys [reload]` | 显示**当前生效**的快捷键（含用户覆盖与配置告警）、固定快捷键清单与 `keybindings.json` 路径；`reload` 重新读取用户按键配置 |
| `/exit`、`/quit`、`/q` | 退出聊天 |
| `/clear`、`/cls` | 清空当前会话历史 |
| `/new` | 创建新会话 |
| `/session` | 显示当前会话信息 |
| `/status` | 显示当前会话状态 |
| `/debug [on|off|status|display|routing|export|zip]` | 控制会话 debug 模式；`routing` 显示 subagent difficulty routing 摘要，`display` 显示当前会话调试信息，`export/zip` 打包会话日志与 artifacts |
| `/title <title>`、`/rename <title>` | 更新当前会话标题 |
| `/history`、`/h` | 显示当前会话历史 |
| `/stream [on|off|toggle|status]` | 查看或切换流式输出 |
| `/s` | 开启流式输出，等价 `/stream on` |
| `/normal`、`/n` | 关闭流式输出，等价 `/stream off` |
| `/theme [mode\|palette\|list\|status\|preview\|select]` | 查看或切换终端主题（明暗 auto/dark/light + 配色 classic/focus/contrast/mono） |
| `/model [name|status|clear-reasoning|--provider ...]` | 查看或切换 provider/model/reasoning_effort |
| `/login [provider|--provider ...]` | 在 chat 内新增或更新 provider 登录凭证，并可刷新/切换当前模型 |
| `/account [provider] [show\|detect] [--save] [--no-refresh] [--json] [--timeout 15s]` | 查看或刷新「当前（或指定）provider」的账户余额/订阅额度；`--save` 才写回 `config.yaml` |
| `/accounts [refresh\|display] [--wait] [--enabled-only] [--no-refresh] [--json] [--timeout 15s]` | 全部 provider 的余额总览：默认提交后台刷新并立刻渲染缓存快照，`refresh` 只提交、`display` 只看缓存（零网络）、`--wait` 才是阻塞拉取；备用屏内按 `r` 刷新显示 |
| `/compact [auto|local|remote]` | 手动触发会话压缩 |
| `/attach [path|clear]` | 查看、添加或清空待发送图片附件 |
| `/image [prompt] [--provider <name>] [--model <name>] [--path auto\|api\|codex_native]` | 调用 `openai_image_generate` 生成图片，行为与 `aicli image` 对齐 |
| `/queue [status|clear]` | 查看或清空排队输入 |
| `/permission-mode [default|accept_edits|plan|bypass_permissions]`、`/mode` | 查看或切换权限模式 |
| `/approval-reuse [off|session_readonly_shell|team_readonly_shell]` | 查看或切换审批复用策略 |
| `/yolo` | 切换到 `bypass_permissions` |
| `/functions <prompt>` | 预览当前 prompt 会暴露哪些 builtin tools / skill functions |
| `/function <name>` | 查看单个 function 描述 |
| `/call <name> [args-json]` | 直接执行指定 function；`openai_image_generate` 可直接把后续文本作为 `prompt` |
| `/tool <name> [args-json]` | `/call` 别名；`openai_image_generate` 可直接把后续文本作为 `prompt` |
| `/skill [--direct] <name> <prompt>` | 默认提交 skill 回合（注入程序说明，由模型自选程序）；`--direct` 直接执行并把后面的文本作为 `prompt` |
| `/skills [query]` | 列出并选择执行 skill |
| `/mcp [list\|status <name>\|add <name> <url> [options]\|enable\|disable\|remove <name>\|reload\|help]` | 管理 MCP Server（列表/新增/启停/删除/热重载），与 `aicli mcp`、console 与微型 Web 面板共用同一份配置与实现 |
| `/sessions` | 列出或筛选可恢复会话 |
| `/load <session-id>` | 加载指定会话 |
| `/resume [latest|<session-id>]` | 恢复最近会话或指定会话；无参数时显示可恢复会话选择器 |
| `/export [current|latest|<session-id>] [--full|--body|--tools|--trace]` | 导出当前或历史会话；完整 JSON 保留 tool_calls、tool 结果和 metadata，正文模式输出 Markdown，`--tools`/`--trace` 在 Markdown 中附带工具调用（名称+输入参数 / 输入+输出结果） |
| `/agents [panel|pick|target|send|followup|routing]` | 查看 agent tree、选择默认 agent target、向 child agent 投递消息或 follow-up；`/agents routing test` 可 dry-run 子 agent 路由 |
| `/timeline [team|active] [limit] [filter=<text>]` | 查看 active team 或指定 team 的持久事件时间线 |
| `/collab [follow] [target|selected|parent|all] [limit] [filter=<text>] [timeout=10s]` | 查看 parent/child/team teammate 的 mailbox/collab 时间线 |
| `/shell <command>`、`/cmd <command>` | 执行 shell 命令并把输出分享给 AI |
| `!<command>` | `/shell` 快捷形式 |

说明：

- `/call` / `/tool` 适合直接执行 `openai_image_generate` 这类内置工具；例如 `/call openai_image_generate 生成图片` 会自动转换为 `{"prompt":"生成图片"}`。
- `/skill imagegen ...` 会直接调用 `skill__imagegen`，由 skill 工作流转发到 `/v1/images/generations` provider。
- `/model` 支持 `status`、`clear-reasoning`、`--provider/-p`、`--model/-m`、`--reasoning-effort/-r`；切换后会刷新 provider、adapter、BaseURL、HTTP client、function builder、logger 和 runtime session metadata。
- `/mcp` 直接管理当前 MCP 配置（优先级：`./.aicli/mcp.yaml` > `~/.aicli/mcp.yaml` > 显式配置 > 向上搜索），写操作落盘并热重载、重连；成功后会把最新 MCP 工具重新注册进当前会话。
  - 新增 URL 传输：`/mcp add chrome-mcp http://127.0.0.1:12306/mcp --header "Authorization: Bearer x" --env API_KEY=1 --trust trusted_remote`（http(s)→streamable，ws(s)→websocket，可用 `--type sse|websocket|streamable` 覆盖）。
  - 新增 stdio 传输：`/mcp add local-fs --command npx --arg -y --arg @modelcontextprotocol/server-filesystem --disabled`。
  - `--header` 会映射为 `HEADER_*` 环境变量（与 console / 微型 Web 面板同一约定）；`/mcp status <name>` 查看连接状态、工具数与最近错误。
- `/login` 与 `aicli login` 共用 provider 登录逻辑，支持 API key、Codex OAuth、`--models-path`、`--default-model`、`--set-default`、`--dry-run` 和 JSON 输出。
- `/account [provider] [show|detect] [--save] [--no-refresh] [--json] [--timeout <dur>]` 与 `aicli balance` 共用同一套站点类型探测与额度换算：缺省（或 `refresh`）实时拉取当前（或指定）provider 并刷新底部状态栏（仅当目标是当前生效 provider 时才写回会话快照，避免后台周期刷新覆盖新值），`--save` 才把快照写回 `config.yaml`；`show`/`--no-refresh` 只读展示缓存快照，`detect` 只探测站点类型。
- `/accounts [refresh|display] [--wait] [--enabled-only] [--no-refresh] [--json] [--timeout <dur>]` 是全部 provider 的总览，默认不再阻塞：提交一个后台刷新任务后立刻渲染缓存快照（状态行显示「后台刷新中」）；`refresh` 只提交并返回一行确认，`display`（`status`/`--no-refresh` 同义）只看缓存、零网络，`--wait` 回到「拉完再渲染」的旧语义，`--json` 默认隐含 `--wait`（`display` 时只序列化缓存）。同一会话同时只允许一个在飞刷新任务（重复提交复用同一任务），结果写入会话缓存供后续 `display` 复用；会话退出时取消在飞任务且不发布半成品结果，后台刷新绝不写 `config.yaml`。
- `/accounts` 的备用屏是**可刷新**的：屏内按 `r`（或 `R`）提交（或复用）一次后台刷新并立刻重投影缓存快照，页脚常驻 `r 刷新显示` 提示；任务完成时屏幕会自动从「后台刷新中」翻到「已刷新」并带上新余额，不需要退出屏幕再敲一次命令。屏内刷新沿用打开屏幕那一刻的 `--enabled-only` / `--timeout` 参数，提交失败（例如过滤后没有目标）只在状态行显示 `刷新未提交` + 原因，缓存表格照常显示。`/account` 单账户屏、`/usage`、`/debug`、`/web` 仍是静态快照屏（没有 `r` 键）。
- 交互式 TUI 会把当前 provider 的账户余额显示在底部状态栏，并在启动后立即刷新一次，随后按 `aicli.balance.refresh_interval` 定时刷新。对于未声明 `site_type` 的 `openai` 协议 provider（例如直连 DeepSeek 网关），TUI 会在首个刷新周期内探测并识别站点类型（`deepseek` / `sub2api` / `new-api`）后拉取余额；探测结果与余额仅保存在会话本地，不改写配置文件。刷新失败时保留最后一次成功值；探测到不支持账户查询的 upstream 则仅探测一次，不再每轮重试探测。
- `/stream`、`/s`、`/normal` 会更新当前会话，并在可写配置存在时写回 `aicli.chat.stream`。
- 交互式快捷键支持用户覆盖：配置文件为 `$AICLI_HOME/keybindings.json`（未设置时 `~/.aicli/keybindings.json`），只写要改的 action，未提及的保持默认；绑定值可为字符串或字符串数组，空数组 `[]` 表示禁用。当前可重映射动作：`app.permission.cycle`（默认 `shift+tab`、`alt+m`）与 `app.transcript.pager`（默认 `ctrl+t`）。文件缺失、JSON 损坏、未知 action、无法解析的按键都只告警并回退默认，不会导致启动失败；改完执行 `/hotkeys reload` 生效，`/hotkeys` 查看当前生效表。示例：`{"app.permission.cycle": ["alt+c"]}`。
- 权限模式循环键（默认 `shift+tab`，Windows 终端可用 `alt+m`）按 `default → accept_edits → plan → bypass_permissions → default` 顺序切换；进入 `bypass_permissions` 仍需二次确认，弹层/选择器持有输入时按键不会抢占。
- 大段粘贴默认折叠为输入框占位符（提交时仍发送全文）；设置 `aicli.chat.collapse_pasted_text: false` 可关闭折叠、原样显示粘贴内容。
- `@` 路径引用支持 `Tab` 补全：唯一命中直接补全（目录补 `/`），多命中先补公共前缀并在状态行给出「匹配 N 项」与示例，无命中也会消费按键（不触发 `Tab` 的 plan mode 切换）。补全是**路径引用**语义，提交时原样发送，内容由模型按需读取。
- 剪贴板图片可直接加成附件：`alt+v`（可重映射动作 `app.attach.clipboard_image`）或 `/attach paste` 读取剪贴板位图，落盘为临时 PNG 后进入待发送附件（Windows 读 CF_DIB/CF_DIBV5，macOS 用 `osascript`，Linux 需 `wl-paste`/`xclip`）；`/hotkeys` 的「剪贴板图片」行会如实说明当前平台是否可用与原因，不可用时用 `/attach <path>`。
- `ctrl+v` 读不到剪贴板文本时（例如剪贴板里只有图片）会按 `alt+v` 的同一套语义读图并插入 `[Image #N]` 令牌；没有图片时完全静默（只有明确按 `alt+v` 才提示错误）。剪贴板同时有文本和图片时仍优先粘贴文本。
- 粘贴图片**路径**会自动成为附件：整段粘贴就是**一个或多个**存在的图片文件路径时生效（Windows Terminal 把复制的图片文件转成路径、资源管理器拖拽路径都算，多选即多个路径），落附件后按顺序插入 `[Image #N]` 令牌；校验全有或全无，任一不是可用图片就整体按文本粘贴。一次最多 8 张，夹在句子里的路径保持原样。注意 WT 自己处理 `Ctrl+V`/右键，位图请用 `alt+v`。
- 发送前图片处理：长边超过 `1568px` 等比缩小（含透明通道保 PNG，其余转 JPEG），超过 `32MB` 直接跳过；提示里会写明前后尺寸与体积，绝不静默发送原图。上限可在配置文件里改：`aicli.chat.max_image_dimension`（像素；`0` 或负数 = 关闭压缩/不缩放）与 `aicli.chat.max_image_mb`（MB；`<=0` = 用默认 32MB）；环境变量 `AICLI_IMAGE_MAX_DIMENSION` / `AICLI_IMAGE_MAX_MB` 优先于配置，都未提供时用内置默认。
- 图片附件带可见令牌 `[Image #N]`：`alt+v` 在光标处插入、`/attach <path>` 在命令执行后写回草稿；**删掉令牌即不再发送该图**（只约束令牌引入的附件，ACP/Web 等其它来源不受影响）。
- 审批面板在工具名后会先给一行通俗解释（`[说明] 动作 / 目标 / 影响`），再列原因、风险等级、上下文与参数摘要；解释是启发式规则、不调用模型，识别不出时不会输出任何猜测。按 `[3]` 可展开完整参数。
- 交互模式的完整说明（按键分层与重映射、终端能力矩阵、粘贴与附件语义、输入所有权、排障）见 [interactive-mode.md](./interactive-mode.md)。
- `/theme` 支持双轴主题：明暗（`auto|dark|light`）与配色（`classic|focus|contrast|mono`）。会立即切换当前终端主题，并在可写配置存在时写回 `aicli.theme.name`（配色）与 `aicli.theme.mode`（明暗）。无参数时交互选择；`list`/`status`/`preview` 只读（`list`/`preview` 带角色色样例）；可写 `/theme dark`、`/theme focus`、`/theme light contrast` 等。配色别名：`default`/`balanced`→focus，`high-contrast`→contrast，`minimal`→mono。启动优先级：`--theme` > `AICLI_THEME`/`AICLI_THEME_MODE` > 配置文件。
- `/resume` 会打开按最后更新时间倒序排列的全屏历史会话选择器，默认仅显示 `workspace_path` 与当前工作目录一致的历史会话；`/resume --cwd` 可显式声明相同行为。不再把候选项挤在聊天输入框上方的小弹层中。使用方向键或 `j`/`k` 移动，`PgUp`/`PgDn` 翻页，`Home`/`End` 跳到首尾，`/` 搜索，回车恢复，`Esc` 或 `q` 取消。当前会话和只有 system prompt 的启动占位 session 不会出现在列表中；不支持 ANSI/TTY 的环境自动回退到编号输入列表。
- `/resume latest` 直接恢复最近的其他可恢复会话。全屏选择器显示最后更新时间（绝对时间与相对时间）、会话轮次、消息数、清理后的标题和选中会话摘要；session id、protocol、provider 和 model 只进入搜索索引，不占用候选行。轮次按持久化的 user 消息数统计，消息数包含 system、user、assistant 和 tool 消息。
- chat 内的 `/sessions` 不显示当前会话和启动占位会话；`aicli chat --list-sessions` / `aicli resume --list-sessions` 的独立完整列表显示最后更新时间、轮次和消息数，并保留 session id、状态、protocol、provider、model 等诊断信息。会话列表和最近会话恢复默认按当前工作目录过滤，传入 `--cwd=false` 才会查看全部目录。CLI 入口上，`aicli resume` 默认恢复当前工作目录最近的可恢复会话，`aicli resume <session-id>` 仍可直接加载指定会话；`aicli --resume` 会经默认 chat 参数改写为 `aicli chat --resume`。
- 退出交互式 TUI 后，终端会显示 `aicli resume <session-id>`，便于下次继续当前会话；临时会话以及尚未落盘的空会话不会显示无效的恢复命令。
- 交互式 `aicli resume` / `aicli chat --resume` / 会话内 `/resume` 恢复后**停在等待输入状态**：上一进程遗留的团队执行会被停放为 `paused`（保留可恢复的团队壳，任务标记 cancelled），不会在启动阶段重新拉起 team lifecycle loop 继续执行，也不会在首屏渲染前 drain supervision auto-wake；启动信息行会提示 `Resume:` 停放说明。headless / `--output json` 语义不变，遗留团队仍跑到终态。
- `/export` 无参数时会弹出选择器；`--full` 生成完整 JSON，`--body` 只导出用户/助手正文，`--tools` 在 Markdown 中附带工具调用名称与输入参数，`--trace` 再附带按 `tool_call_id` 配对的输出结果（单个输出超过 32 KB 时截断并标记，完整内容用 `--full`）；可用 `--output <path>` 或 `--dir <dir>` 指定输出位置。
- `aicli export [current|latest|<session-id>] [--full|--body|--tools|--trace] [--format <fmt>] [--output <path>|--dir <dir>] [--session-dir <dir>] [--user <id>]` 是 `/export` 的顶层等价入口，复用同一套导出实现与格式语义，适合脚本与 CI：目标缺省为 `latest`（顶层命令没有「当前会话」上下文，`current` 也落到 `latest`，实际导出的会话 ID 会打印在摘要里）；格式来源（`--full`/`--body`/`--tools`/`--trace`/`--format`/裸格式词）互相冲突时直接报参数错误，不会静默取最后一个。退出码：`0` 成功、`1` 参数错误、`2` 确定性错误（会话不存在、会话存储不可读、输出发布失败）——chat 内的 `/export` 出错仍返回 `0`，脚本请改用顶层命令判断成败。
- `aicli import <file> [--session-dir <dir>] [--user <id>] [--new-id] [--dry-run] [--output text|json]` 是 `aicli export --full` 的反向入口：只接受 `--full` 导出的 JSON（`--body`/`--tools`/`--trace` 的 Markdown 投影会被拒绝）。默认沿用文件里的 `session.id` 与 `userId`；同 ID 会话已存在时默认报错且**绝不覆盖**（SQLite 的 Save 对已存在 ID 是 upsert，放过就等于静默覆盖原会话），加 `--new-id` 生成新 ID 并存，`--user <id>` 可改写归属。文件里的 `session.id` 若不可寻址（含路径分隔符、首尾空白或 `<nil>` 占位值，存储层读取时会先做规范化，落库后无法按原样读回、只能手动清理）同样默认按参数错误拒绝，`--new-id` 可改用新 ID 导入。落库语义：`createdAt` 保留、`updatedAt` 刷新为导入时间（导入的会话会排在 resume 列表顶部）、丢弃 `expiresAt`、`headOffset` 归零、缺失的 `message_id`/`turn_id` 补齐，复用的重复 `message_id` 重新铸造（读取路径会折叠相邻同内容消息，重复身份等于静默少消息），工具链身份不完整（`tool_calls` 缺 ID 或 `tool` 消息缺 `tool_call_id`）只告警不拒绝；并在 `metadata.context` 写入 `imported_from`/`imported_at`（改名时另有 `import_original_session_id`），原有 context 键（如 `workspace_path`）保持不变，导入的会话因此仍回到原来的工作目录分组；写入后读回校验 canonical 消息条数与文件一致。退出码与 `export` 对齐：`0` 成功、`1` 参数错误、`2` 确定性错误（文件不可读或格式不支持、会话已存在、写库或读回校验失败）。完整用法、导出文件结构、JSON 摘要字段与排障见 [session-export-import.md](./session-export-import.md)。
- `/debug export` / `/debug zip` 会把 `/debug display` 中“会话文件与目录”部分的 session file、chat/debug log、http/shell/images artifacts（兼容旧目录名 runtime-http/local-shell/generated-images）打包为 zip，并附带 `manifest.json`。SQLite 模式在同一读事务中生成只含当前 session 的一致性快照，包含已提交 WAL 内容但不会泄露其他会话，并同时打包当前会话引用的 canonical artifacts。
- `spawn_team auto_start=true` 之后应使用 `wait_team` 等待持久 `team.completed` / `team.summary`；`wait_agent` / `read_agent_events` 面向 `spawn_agent` child session，不应拿 team member id 当 child session id。
- `/shell` / `/cmd` 支持 `--output-bytes-cap <bytes>` 与 `--disable-output-cap`；默认使用检测到的用户 shell。危险命令仍会进入确认/权限流程。
- builtin `execute_shell_command` function 支持 `command`、`workdir`、`output_bytes_cap`、`disable_output_cap`；Windows PowerShell/pwsh 下不要把 POSIX-only 命令如 `head` 当默认可用命令。
- background toolbroker 能力包括 `background_task` 和 `task_output`；HTTP 观测入口见 `docs/skill_runtime/runtime_operations_api.md` 的 Background Jobs 章节。
- shell / background：进程正常结束但 exit≠0 是内容结果，不是工具崩溃。前台 bash 返回 `Success:true` + `exit_code`；background job 状态为 `completed` 并保留 `exit_code`（可选 `non_zero_exit`），仅启动失败、超时、取消、权限/健康检查等硬失败才是 `failed`/`timed_out`/`cancelled` 并带 `error_code`。
- MCP 默认启动即连（后台异步并行建连，已移除 `auto_connect` 开关）：`config_file` 解析到实际存在的配置时，chat 加载并连接其中 `enabled: true` 的 server；配置缺失时静默跳过，不再为缺失的默认 `configs/mcp.yaml` 打印 warning。单个 server 是否参与连接由 `mcpServers.<name>.enabled` 控制（缺省启用）。

账户余额定时刷新间隔默认为 1 分钟，可在配置文件中修改：

```yaml
aicli:
  balance:
    refresh_interval: 1m
```

也可以通过环境变量 `AICLI_BALANCE_REFRESH_INTERVAL` 覆盖，例如 `30s`、`2m`。该定时任务只在交互式 TUI 会话中运行；`aicli balance --refresh [--save]` 的手动刷新与保存语义保持不变。

### 子 agent difficulty routing

`aicli` 支持通过 `aicli.subagents.routing` 按子任务难度为子 agent 选择 provider、model 和 `reasoning_effort`。主会话模型仍由 `/model` 与 `aicli.chat` 控制；difficulty routing 只作用于子 agent 执行面。

最小配置示例：

```yaml
aicli:
  subagents:
    routing:
      enabled: false
      default_difficulty: normal
      inherit_parent_when_missing: true
      validate_model_capabilities: true
      unsupported_reasoning_policy: ignore
      max_expert_concurrency: 1
```

开启后可配置不同难度的 route：

```yaml
aicli:
  subagents:
    routing:
      enabled: true
      default_difficulty: normal
      allow_explicit_provider_override: false
      allow_explicit_model_override: true
      allow_explicit_reasoning_override: false
      unsupported_reasoning_policy: downgrade
      levels:
        easy:
          provider: local_fast
          model: gpt-5.4-mini
          reasoning_effort: low
        hard:
          provider: strong_remote
          model: gpt-5.4
          reasoning_effort: high
      roles:
        verifier:
          hard:
            provider: audit_model
            model: gpt-5.4
            reasoning_effort: medium
```

Team 默认沿用 `aicli.subagents.routing`。如果 Team task 需要与普通子 Agent 使用不同的 provider/model，可增加独立配置：

```yaml
aicli:
  teams:
    routing:
      enabled: true
      default_difficulty: normal
      inherit_parent_when_missing: true
      validate_model_capabilities: true
      levels:
        easy:
          provider: local_fast
          model: gpt-5.4-mini
        normal:
          provider: balanced_remote
          model: gpt-5.4
        hard:
          provider: strong_remote
          model: gpt-5.4
          reasoning_effort: high
        expert:
          provider: audit_model
          model: gpt-5.4
          reasoning_effort: high
```

#### 显式难度与启发式提升

子 Agent 显式声明 `difficulty` 时，历史上会直接短路启发式。默认（`enforce`）改为：显式声明仍可被高风险信号**提升**，且提升是单调的——只抬高 rank，绝不会把显式声明的 `expert` 降回 `hard`。

```yaml
aicli:
  subagents:
    routing:
      # enforce（默认）| warn | off
      promote_explicit_difficulty: enforce
      heuristics:
        disabled: false
        # 追加语义：内置词表始终生效，这里只做补充
        promote_keywords: ["kafka", "分库分表"]
        promote_keywords_combo: ["压测", "容量规划"]
```

- 三态：`enforce` 真正提升；`warn` 不提升、只把「本该提升」写进 `route_warnings`（先用它观测误报再决定是否 enforce）；`off` 回到历史行为，且不产生任何提升告警。`off` / `warn` 即回滚开关，无需改代码或降级版本。
- 高信号词（`promote_keywords`）：单命中即升 `hard`。内置词表同时覆盖中英文（`security` / `权限` / `migration` / `迁移` / `协议` …）。
- 弱信号词（`promote_keywords_combo`）：需 ≥2 命中，或 1 命中 + 非只读 `writer`，用于压制「保持风格一致性」这类误报。
- 英文词条带词形归一：同词干的词尾变体都算命中（`migrate` / `migrating` / `migrated` / `migrations` → 词表里的 `migration`，`encrypt` → `encryption`）。只按固定后缀表剥离一次、不做模糊匹配，并有词长 ≥5、词干 ≥6 两条护栏压制误报；中文与多词条目仍为子串匹配。命中记录的是词表条目本身，`route_warnings` 的格式与调优方式不变。
- 角色规则独立于词表：`verifier` 与非只读 `writer` 至少升到 `normal`，`heuristics.disabled` 不会关闭它。

在 runtime-server Web 配置页的“Agent 难度路由”中可维护这两套配置。保存后，新创建的子 Agent 和 Team task 会立即使用新策略；已经运行中的任务不会被重新路由。

可观测入口：

- `aicli doctor subagent-route --role writer --difficulty hard`：不调用模型，只输出最终 route decision。未显式传 `--parent-*` 时，会优先使用 `aicli.chat.default_provider/default_model/reasoning_effort` 作为 parent 默认值，再回退 provider 默认配置。
- `/debug routing`：在 chat 内查看当前 routing 配置摘要。
- `/agents routing test --role writer --difficulty hard`：在 chat 内基于当前会话 parent provider/model/reasoning 做 route dry-run。该命令支持 `--provider`、`--model`、`--reasoning-effort` 值补全。
- `subagent.started` / `subagent.completed` runtime event、`subagent_start` / `subagent_stop` hook payload、AgentControl mailbox/display mirror 会携带 `difficulty`、`difficulty_source`、`difficulty_rationale`、`route_provider`、`route_model`、`route_reasoning_effort`、`route_source`、`route_warnings`、`fallback_used`、`fallback_reason` 和使用量字段。
- `validate_model_capabilities: true` 时，routing 会校验已声明 capability 的 route model。若 route model 明确不支持，会优先 fallback 到 parent provider/model 并记录 `model_unsupported`、`model_fallback_parent` 和 `fallback_reason`；无法 fallback 时返回错误。未声明能力目录的 provider 不会被强制拒绝。
- 提升证据链：`route_warnings` 会写 `difficulty_promoted_over_explicit`（显式难度被提升）、`difficulty_promotion_warn_only`（`warn` 模式下的观测）、以及 `difficulty_promoted_by_keyword:<词>` / `difficulty_promoted_by_role:<role>`（命中证据，最多 3 条），因此「为什么被升档」在审计里自解释，误报可按词回溯调优。
- `subagent_route_resolved` 审计事件额外携带 `expert_limit`：`unlimited` 或十进制上限，用于确认 expert 并发闸门是否真的生效。`aicli doctor subagent-route` 输出的 `preflight.promote_explicit_difficulty` / `preflight.expert_limit` / `preflight.config_warnings` 可在不调用模型的前提下确认三态与词表是否被正确解析。
- `spawn_agent` / `spawn_subagents` 的结果元数据带 `route_receipt`（整批 ≤8 行、≤1 KB，无路由信息时不产生该字段）：父 Agent 无需额外查询即可看到每个子 Agent 的实际落点与告警数。
- `unsupported_reasoning_policy` 控制模型不支持 route `reasoning_effort` 时的行为：`ignore` 清空并 warning，`downgrade` 降到已支持的较低档位，`fail` / `reject` 直接拒绝该 route。默认是 `ignore`。

tool 参数边界：

- `spawn_subagents` 支持 `difficulty`、`difficulty_rationale`、`provider`、`model`、`reasoning_effort`；`thinking_effort` 是 `reasoning_effort` 的兼容别名。routing enabled 时，provider/model/reasoning 最终仍由本地 routing policy 授权；未授权 override 会被忽略或记录 warning。
- `spawn_agent` 支持同样的 route hints，并会把最终 route 写入 child session context 和 AgentControl durable graph。routing disabled 时只保留 legacy `model` override，不会因为新增字段切换 provider 或 reasoning。
- planner 生成的 `PlanStep.difficulty` / `difficulty_rationale` 会复制到建议的 subagent task；hard/expert writer 必须带只读 verifier 依赖，且 verifier 难度至少为 hard。
- `spawn_team.tasks[].difficulty` 与 `difficulty_rationale` 会进入 Team task 路由决策、planner/audit、dispatch/mailbox 和 runtime event。若未配置 `aicli.teams.routing`，Team 沿用 `aicli.subagents.routing`。

---

## 四、常见问题（FAQ）

完整排错清单已拆到独立文档 [faq.md](./faq.md)。本节只保留入口索引：

| 问题 | 文档 |
|------|------|
| 找不到配置 / `providers` 为空 | [faq.md §1](./faq.md#1-找不到配置--providers-为空) |
| `aicli login` models endpoint 校验失败 | [faq.md §2](./faq.md#2-aicli-login-校验-models-endpoint-失败) |
| chat 里 `/model` 切换失败 | [faq.md §3](./faq.md#3-chat-里-model-切换失败) |
| HTTP 401 / Invalid API key | [faq.md §4](./faq.md#4-http-401--invalid-api-key) |
| Windows 安装后找不到 `aicli` | [faq.md §5](./faq.md#5-windows-安装后找不到-aicli) |
| 临时使用另一份配置 | [faq.md §6](./faq.md#6-临时使用另一份配置) |
| 日志路径 | [faq.md §7](./faq.md#7-日志在哪里) |
| 仓库示例配置读不到 | [faq.md §8](./faq.md#8-仓库里的示例配置为什么读不到) |

快速自检：

```bash
aicli config
aicli doctor provider
aicli doctor provider --provider openai --model gpt-4.1
```

最短上手见 [quickstart.md](./quickstart.md)。

---

## 五、卸载

如需删除 aicli 在本机写入的配置与运行数据，可先预览，再确认删除：

```bash
aicli uninstall --dry-run
aicli uninstall --yes
```

`aicli uninstall` 默认删除用户目录下的 `~/.aicli`，以及当前工作目录树中的所有 `.aicli` 目录。这些目录中可能包含 `config.yaml`、`.env`、`auth.json`、sessions、chat-logs、logs、skills、MCP 配置等文件。可用 `--user-only` 或 `--local-only` 限定删除范围，也可用 `--output json` 获取结构化结果。

该命令只清理配置与数据目录，不删除 `aicli` 可执行文件本身。删除可执行文件请按安装方式使用下面的方法。

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

## 六、相关链接

- [quickstart.md](./quickstart.md)
- [faq.md](./faq.md)
- [exec.md](./exec.md)
- [agents.md](./agents.md)（含 `aicli chat --agent` 与 `aicli agent stdio`）
- [profiles.md](./profiles.md)（运行 profile：`profile.yaml` + `agents/`、四模板、优先级、validate 语义）
- [tool_image_generate.md](./tool_image_generate.md)
- [skill_runtime/aicli_skills_usage.md](../skill_runtime/aicli_skills_usage.md)
- [GitHub Releases](https://github.com/wwsheng009/ai-agent-runtime/releases)
- [Release workflow 源码](../../.github/workflows/release-aicli.yml)
- [完整配置示例](../../backend/configs/config.yaml)
- [项目主 README](../../README.md)
