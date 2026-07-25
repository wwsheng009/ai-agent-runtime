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

四个默认候选位置都不存在时，当前 `aicli` 会优先创建用户级 starter 配置 `$HOME/.aicli/config.yaml`，从而与默认查找顺序保持一致；如果用户目录不可用，则回退到当前工作目录的 `./.aicli/config.yaml`。starter 中默认开启 `aicli.chat.stream: true`，并保留空的 `providers.items`，方便后续通过 `aicli login` 或手工编辑补 provider。

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
| 非交互 | `--message`、`--no-interactive`、`--request-timeout` | 一次性发送消息并退出，适合脚本 |
| session | `--session`、`--resume`、`--list-sessions` | 加载指定 session、恢复最近 session 或列出历史 |
| session 过滤 | `--session-state`、`--session-provider`、`--session-model`、`--session-query`、`--session-limit` | 筛选可恢复 session |
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
| `/debug [on|off|status|display|routing|export|zip]` | 控制会话 debug 模式；`routing` 显示 subagent difficulty routing 摘要，`display` 显示当前会话调试信息，`export/zip` 打包会话日志与 artifacts |
| `/title <title>`、`/rename <title>` | 更新当前会话标题 |
| `/history`、`/h` | 显示当前会话历史 |
| `/stream [on|off|toggle|status]` | 查看或切换流式输出 |
| `/s` | 开启流式输出，等价 `/stream on` |
| `/normal`、`/n` | 关闭流式输出，等价 `/stream off` |
| `/theme [mode\|palette\|list\|status\|preview\|select]` | 查看或切换终端主题（明暗 auto/dark/light + 配色 classic/focus/contrast/mono） |
| `/model [name|status|clear-reasoning|--provider ...]` | 查看或切换 provider/model/reasoning_effort |
| `/login [provider|--provider ...]` | 在 chat 内新增或更新 provider 登录凭证，并可刷新/切换当前模型 |
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
| `/skill <name> <prompt>` | 直接执行指定 skill，并把后面的文本作为 `prompt` |
| `/skills [query]` | 列出并选择执行 skill |
| `/sessions` | 列出或筛选可恢复会话 |
| `/load <session-id>` | 加载指定会话 |
| `/resume [latest|<session-id>]` | 恢复最近会话或指定会话；无参数时显示可恢复会话选择器 |
| `/export [current|latest|<session-id>] [--full|--body]` | 导出当前或历史会话；完整 JSON 保留 tool_calls、tool 结果和 metadata，正文模式输出 Markdown |
| `/agents [panel|pick|target|send|followup|routing]` | 查看 agent tree、选择默认 agent target、向 child agent 投递消息或 follow-up；`/agents routing test` 可 dry-run 子 agent 路由 |
| `/timeline [team|active] [limit] [filter=<text>]` | 查看 active team 或指定 team 的持久事件时间线 |
| `/collab [follow] [target|selected|parent|all] [limit] [filter=<text>] [timeout=10s]` | 查看 parent/child/team teammate 的 mailbox/collab 时间线 |
| `/shell <command>`、`/cmd <command>` | 执行 shell 命令并把输出分享给 AI |
| `!<command>` | `/shell` 快捷形式 |

说明：

- `/call` / `/tool` 适合直接执行 `openai_image_generate` 这类内置工具；例如 `/call openai_image_generate 生成图片` 会自动转换为 `{"prompt":"生成图片"}`。
- `/skill imagegen ...` 会直接调用 `skill__imagegen`，由 skill 工作流转发到 `/v1/images/generations` provider。
- `/model` 支持 `status`、`clear-reasoning`、`--provider/-p`、`--model/-m`、`--reasoning-effort/-r`；切换后会刷新 provider、adapter、BaseURL、HTTP client、function builder、logger 和 runtime session metadata。
- `/login` 与 `aicli login` 共用 provider 登录逻辑，支持 API key、Codex OAuth、`--models-path`、`--default-model`、`--set-default`、`--dry-run` 和 JSON 输出。
- `/stream`、`/s`、`/normal` 会更新当前会话，并在可写配置存在时写回 `aicli.chat.stream`。
- `/theme` 支持双轴主题：明暗（`auto|dark|light`）与配色（`classic|focus|contrast|mono`）。会立即切换当前终端主题，并在可写配置存在时写回 `aicli.theme.name`（配色）与 `aicli.theme.mode`（明暗）。无参数时交互选择；`list`/`status`/`preview` 只读（`list`/`preview` 带角色色样例）；可写 `/theme dark`、`/theme focus`、`/theme light contrast` 等。配色别名：`default`/`balanced`→focus，`high-contrast`→contrast，`minimal`→mono。启动优先级：`--theme` > `AICLI_THEME`/`AICLI_THEME_MODE` > 配置文件。
- `/resume` 会打开按最后更新时间倒序排列的全屏历史会话选择器，不再把候选项挤在聊天输入框上方的小弹层中。使用方向键或 `j`/`k` 移动，`PgUp`/`PgDn` 翻页，`Home`/`End` 跳到首尾，`/` 搜索，回车恢复，`Esc` 或 `q` 取消。当前会话和只有 system prompt 的启动占位 session 不会出现在列表中；不支持 ANSI/TTY 的环境自动回退到编号输入列表。
- `/resume latest` 直接恢复最近的其他可恢复会话。全屏选择器显示最后更新时间（绝对时间与相对时间）、会话轮次、消息数、清理后的标题和选中会话摘要；session id、protocol、provider 和 model 只进入搜索索引，不占用候选行。轮次按持久化的 user 消息数统计，消息数包含 system、user、assistant 和 tool 消息。
- chat 内的 `/sessions` 不显示当前会话和启动占位会话；`aicli chat --list-sessions` 的独立完整列表显示最后更新时间、轮次和消息数，并保留 session id、状态、protocol、provider、model 等诊断信息。
- `/export` 无参数时会弹出选择器；`--full` 生成完整 JSON，`--body` 只导出用户/助手正文；可用 `--output <path>` 或 `--dir <dir>` 指定输出位置。
- `/debug export` / `/debug zip` 会把 `/debug display` 中“会话文件与目录”部分的 session file、chat/debug log、runtime-http/local-shell/generated-images artifacts 打包为 zip，并附带 `manifest.json`。SQLite 模式在同一读事务中生成只含当前 session 的一致性快照，包含已提交 WAL 内容但不会泄露其他会话，并同时打包当前会话引用的 canonical artifacts。
- `spawn_team auto_start=true` 之后应使用 `wait_team` 等待持久 `team.completed` / `team.summary`；`wait_agent` / `read_agent_events` 面向 `spawn_agent` child session，不应拿 team member id 当 child session id。
- `/shell` / `/cmd` 支持 `--output-bytes-cap <bytes>` 与 `--disable-output-cap`；默认使用检测到的用户 shell。危险命令仍会进入确认/权限流程。
- builtin `execute_shell_command` function 支持 `command`、`workdir`、`output_bytes_cap`、`disable_output_cap`；Windows PowerShell/pwsh 下不要把 POSIX-only 命令如 `head` 当默认可用命令。
- background toolbroker 能力包括 `background_task` 和 `task_output`；HTTP 观测入口见 `docs/skill_runtime/runtime_operations_api.md` 的 Background Jobs 章节。
- shell / background：进程正常结束但 exit≠0 是内容结果，不是工具崩溃。前台 bash 返回 `Success:true` + `exit_code`；background job 状态为 `completed` 并保留 `exit_code`（可选 `non_zero_exit`），仅启动失败、超时、取消、权限/健康检查等硬失败才是 `failed`/`timed_out`/`cancelled` 并带 `error_code`。
- 当 `aicli.mcp.auto_connect=false` 且 `config_file` 不存在时，chat 会跳过 MCP 初始化，不再为缺失的默认 `configs/mcp.yaml` 打印 warning。

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

在 runtime-server Web 配置页的“Agent 难度路由”中可维护这两套配置。保存后，新创建的子 Agent 和 Team task 会立即使用新策略；已经运行中的任务不会被重新路由。

可观测入口：

- `aicli doctor subagent-route --role writer --difficulty hard`：不调用模型，只输出最终 route decision。未显式传 `--parent-*` 时，会优先使用 `aicli.chat.default_provider/default_model/reasoning_effort` 作为 parent 默认值，再回退 provider 默认配置。
- `/debug routing`：在 chat 内查看当前 routing 配置摘要。
- `/agents routing test --role writer --difficulty hard`：在 chat 内基于当前会话 parent provider/model/reasoning 做 route dry-run。该命令支持 `--provider`、`--model`、`--reasoning-effort` 值补全。
- `subagent.started` / `subagent.completed` runtime event、`subagent_start` / `subagent_stop` hook payload、AgentControl mailbox/display mirror 会携带 `difficulty`、`difficulty_source`、`difficulty_rationale`、`route_provider`、`route_model`、`route_reasoning_effort`、`route_source`、`route_warnings`、`fallback_used`、`fallback_reason` 和使用量字段。
- `validate_model_capabilities: true` 时，routing 会校验已声明 capability 的 route model。若 route model 明确不支持，会优先 fallback 到 parent provider/model 并记录 `model_unsupported`、`model_fallback_parent` 和 `fallback_reason`；无法 fallback 时返回错误。未声明能力目录的 provider 不会被强制拒绝。
- `unsupported_reasoning_policy` 控制模型不支持 route `reasoning_effort` 时的行为：`ignore` 清空并 warning，`downgrade` 降到已支持的较低档位，`fail` / `reject` 直接拒绝该 route。默认是 `ignore`。

tool 参数边界：

- `spawn_subagents` 支持 `difficulty`、`difficulty_rationale`、`provider`、`model`、`reasoning_effort`；`thinking_effort` 是 `reasoning_effort` 的兼容别名。routing enabled 时，provider/model/reasoning 最终仍由本地 routing policy 授权；未授权 override 会被忽略或记录 warning。
- `spawn_agent` 支持同样的 route hints，并会把最终 route 写入 child session context 和 AgentControl durable graph。routing disabled 时只保留 legacy `model` override，不会因为新增字段切换 provider 或 reasoning。
- planner 生成的 `PlanStep.difficulty` / `difficulty_rationale` 会复制到建议的 subagent task；hard/expert writer 必须带只读 verifier 依赖，且 verifier 难度至少为 hard。
- `spawn_team.tasks[].difficulty` 与 `difficulty_rationale` 会进入 Team task 路由决策、planner/audit、dispatch/mailbox 和 runtime event。若未配置 `aicli.teams.routing`，Team 沿用 `aicli.subagents.routing`。

---

## 四、卸载

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

## 五、相关链接

- [GitHub Releases](https://github.com/wwsheng009/ai-agent-runtime/releases)
- [Release workflow 源码](../../.github/workflows/release-aicli.yml)
- [完整配置示例](../../backend/configs/config.yaml)
- [项目主 README](../../README.md)
