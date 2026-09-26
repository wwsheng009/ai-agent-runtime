# aicli 使用手册

> 对应程序：`cmd/aicli`（`aicli` / `aicli.exe`）
> 作用：AI Agent 运行时的主 CLI——交互式 Chat、Headless Exec、配置、Provider 登录与系统管理。

---

## 目录

1. [概述](#1-概述)
2. [快速上手](#2-快速上手)
3. [子命令总览](#3-子命令总览)
4. [常用子命令详解](#4-常用子命令详解)
5. [全局参数与主题](#5-全局参数与主题)
6. [配置文件与数据目录](#6-配置文件与数据目录)
7. [退出码与日志](#7-退出码与日志)
8. [相关文档](#8-相关文档)

---

## 1. 概述

`aicli` 是 `ai-agent-runtime` 的核心命令行界面。不带子命令启动时进入 **Chat 交互模式**，也支持 `exec` 无头执行、配置/Provider 管理、MCP 与插件管理、技能管理等多种子命令。

主要能力：

- **交互式 Chat**：流式对话、slash 命令、会话恢复、模型切换、shell/后台工具
- **Headless Exec**：`aicli exec`，JSON/JSONL 输出，适合脚本与 CI
- **Provider 接入**：OpenAI 兼容协议、Codex OAuth；`aicli login` 交互/非交互登录
- **配置管理**：`aicli config`（含 TUI）、`aicli init`、`aicli doctor` 诊断
- **技能 / MCP / 插件**：skill 安装、MCP server 管理、本地 plugin 信任管理
- **多 Agent**：`aicli agent`（子会话 / stdio）、`aicli exec` 计划子代理执行

---

## 2. 快速上手

```bash
# 首次使用：初始化全局配置（会引导填写 provider 等）
aicli init --global

# 登录 provider
aicli login

# 非交互式登录（指定 provider 与 API key）
aicli login -p <provider-name> --api-key <key>

# 直接进入交互式聊天
aicli

# 无头执行一句话任务（输出 JSON）
aicli exec "列出当前目录的文件" --json

# 恢复指定会话
aicli chat --session <session-id>
```

> 详细安装见 [docs/aicli/install.md](../aicli/install.md)，首次使用流程见 [docs/aicli/quickstart.md](../aicli/quickstart.md)。

---

## 3. 子命令总览

| 子命令 | 说明 | 典型用法 |
|--------|------|----------|
| `chat` | 交互式聊天（默认模式） | `aicli chat` / `aicli` |
| `exec` | 无头执行任务 | `aicli exec [OPTIONS] [PROMPT]` |
| `exec resume` | 恢复 exec 任务 | `aicli exec resume [SESSION_ID] [PROMPT]` |
| `exec review` | 对变更做 review | `aicli exec review [指令]` |
| `resume` | 恢复聊天会话 | `aicli resume [SESSION_ID]` |
| `agent` | 子 Agent 管理（stdio 等） | `aicli agent` / `aicli agent stdio` |
| `config` | 查看/修改配置 | `aicli config`（TUI）/ `--json` |
| `init` | 初始化配置 | `aicli init --global` |
| `login` | Provider 登录 | `aicli login` |
| `provider` | Provider 管理 | `aicli provider list` / `provider show <name>` |
| `doctor` | 环境诊断 | `aicli doctor` |
| `balance` | 账户余额查询 | `aicli balance` |
| `skill` | 技能管理 | `aicli skill` / `aicli skill install [name]` |
| `plugin` | 本地插件信任管理 | `aicli plugin install <source-dir>` / `plugin list` |
| `image` | 图片生成 | `aicli image [prompt]` |
| `mcp` | MCP server 管理 | `aicli mcp add/list/remove/enable/disable/...` |
| `test` | 测试 Provider 端点 | `aicli test -p <provider> -m <model>` |
| `context` | 测试上下文窗口与最大输出 | `aicli context` |
| `replay` | 回放会话记录 | `aicli replay <file>` |
| `version` | 版本信息 | `aicli version` |
| `uninstall` | 卸载 | `aicli uninstall` |

---

## 4. 常用子命令详解

### 4.1 `aicli` / `aicli chat` —— 交互式聊天

```bash
aicli                       # 进入 chat
aicli chat --session <id>   # 恢复会话
aicli chat --model <model>  # 指定模型
```

Chat 内支持的 slash 命令（部分示例，以 `/help` 内为准）：

```text
/help          显示帮助
/new           新会话
/model         切换模型
/session       查看/切换会话
/theme         切换主题
/exit /quit    退出
```

### 4.2 `aicli exec` —— 无头执行

```bash
# 简单执行
aicli exec "你的任务描述"

# JSON 输出（结构化结果）
aicli exec "任务描述" --json

# 恢复上次中断的 exec
aicli exec resume <session-id> "继续指令"

# 计划模式执行（先规划、审批后执行）
aicli exec --permission-mode plan "复杂任务"

# 快捷模式（跳过审批，等价于 --permission-mode bypass_permissions）
aicli exec --yolo "复杂任务"
```

> 详细用法与输出格式见 [docs/aicli/exec.md](../aicli/exec.md)。
>
> plan 模式的完整说明（模式选择、`/plan` 命令、模型裁决与评审闭环、计划工件归档、HTTP API 与未实现清单）见 [docs/aicli/plan-mode.md](../aicli/plan-mode.md)。

### 4.3 `aicli config` / `init` / `doctor`

```bash
aicli config              # 交互式 TUI（默认）
aicli config --no-tui     # 传统摘要输出
aicli config --json       # JSON 输出
aicli config -p <provider>  # 只查看指定 provider
aicli doctor              # 环境与连通性诊断
aicli doctor provider     # 诊断 provider
```

### 4.4 `aicli provider` / `login` / `balance`

```bash
aicli login                       # 交互式登录
aicli login --provider <name> --api-key <api-key>   # 非交互（示例）
aicli provider list               # 列出已配置 provider
aicli provider show <name>        # 查看 provider 详情
aicli provider set-default <name> # 设为默认
aicli balance                     # 查询账户余额
```

### 4.5 `aicli mcp` —— MCP 管理

```bash
aicli mcp list                  # 列出已配置 MCP
aicli mcp add <名称> <URL|命令>  # 添加 MCP server（缺省按目标推断传输类型）
aicli mcp add <名称> -- <命令> [参数...]  # stdio：名称在 -- 之前，命令在 -- 之后
aicli mcp auth <名称>           # OAuth 登录（浏览器 + 本地回调，PKCE）
aicli mcp auth --status         # 查看各 server 授权状态
aicli mcp logout <名称>          # 清除本地 OAuth 令牌
aicli mcp test <名称> <工具> [参数JSON]  # 测试工具
aicli mcp tools [名称]          # 列出工具
aicli mcp reload                # 重载配置
```

支持的传输类型：`stdio`、`sse`、`websocket`、`streamable`（Streamable HTTP，MCP 2025-03-26 规范，推荐）。
未显式指定 `--transport` 时会按目标推断：`http(s)://` → `streamable`、`ws(s)://` → `websocket`、本地命令 → `stdio`；
`--env KEY=VALUE` 可重复传入，`--header "Key: Value"` 会镜像为 `HEADER_*` 环境变量。

```bash
# stdio（推荐写法）
aicli mcp add chrome-devtools -- npx -y chrome-devtools-mcp@latest

# Streamable HTTP（显式指定亦可）
aicli mcp add --transport streamable chrome-mcp http://127.0.0.1:12306/mcp

# 传统 SSE（端点形如 /sse 时显式指定）
aicli mcp add --transport sse legacy-sse https://example.com/sse
```

配置值支持环境变量引用：`${NAME}` 严格（未设置时该 server 被隔离并在 `mcp list/status` 的「最近错误」里指名变量）、
`${NAME:-default}` 带默认值、`$${NAME}` 表示字面量；配置文件始终保留原始引用，仅在运行时连接前展开。

需要 OAuth 的远端服务：

```bash
aicli mcp add notion https://mcp.notion.com/mcp --auth oauth --oauth-scope read
aicli mcp auth notion                 # 浏览器完成授权；令牌存 ~/.aicli/mcp-tokens.json
aicli mcp auth notion --no-browser    # 无桌面环境：打印链接，手动粘贴回调 URL/code
```

未登录时该 server 显示为「需认证」（`mcp list/status`、`/mcp`、微型 Web 面板），401/403 会自动刷新令牌并重试一次；
刷新失败即回到「需认证」。websocket / stdio 暂不支持自动 OAuth，请用 `headers` 配置静态凭证。

配置分层：`configs/mcp.yaml`（默认）< `~/.aicli/mcp.yaml`（个人全局）< `./.aicli/mcp.yaml`（项目级）< `~/.aicli/projects/<项目>/mcp.yaml`（local，项目私有）
会**按名合并**——低优先级提供基础 server，高优先级同名 server 整体覆盖。`mcp list` 会标注 `来源:` 与 `覆盖:`，避免「改了用户级却被项目级盖掉」的困惑。

```bash
aicli mcp add my-tools https://example.com/mcp            # 默认写用户级
aicli mcp add team-tools https://team.example.com/mcp --scope project   # 写 ./.aicli/mcp.yaml（可提交共享）
aicli mcp add private-tool https://x.example.com/mcp --scope local      # 写 ~/.aicli/projects/<项目>/mcp.yaml（私有）
```

`--scope project` 拒绝明文凭证（`Authorization`/`api-key`/`token` 等）：请写成 `${VAR}`，例如
`--header "Authorization: Bearer ${TEAM_TOKEN}"`；`enable/disable/remove` 会作用在定义该 server 的那个文件上。

从 Claude / Cursor / Gemini / OpenCode / Codex 迁移：

```bash
aicli mcp import --dry-run                    # 先看会导入什么（不写文件）
aicli mcp import --from claude --scope user   # 从 Claude 配置导入到个人全局
aicli mcp add-json my-server '{"url":"https://example.com/mcp"}'   # 或直接用一段 JSON 添加
aicli mcp get my-server --json                # 导出单 server 配置（可直接复制到别的机器）
```

查看某个 MCP 当前暴露的工具：CLI 用 `aicli mcp tools <名称>`；微型 Web（`aicli chat --web`）与 console 设置页的 MCP 列表里都有「工具」按钮，
分别读取 `GET /web/api/mcps/{name}/tools`（微 Web）与 `GET /api/runtime/mcps/{name}/tools`（runtime-server），
返回 `{name,count,tools:[{name,description,enabled,inputSchema}]}`；未启用 / 未连接时返回空列表，由前端渲染空态。

### 4.6 `aicli agent` —— 子 Agent

```bash
aicli agent         # 查看 agent 帮助
aicli agent stdio   # stdio 模式（供上层编排调用）
```

### 4.7 `aicli skill` / `plugin` / `image`

```bash
aicli skill list                 # 列出已安装技能
aicli skill install <name>       # 安装技能
aicli plugin list                # 列出插件与信任状态
aicli plugin trust <name>        # 信任某插件
aicli image "一只在月球上散步的猫"  # 图片生成
```

### 4.8 子 Agent 执行看门狗与监督通知（本地模式）

本地 Chat 会话 spawn 出来的子 agent 受轻量执行看门狗巡检：执行 deadline、进度停滞、审批超时都会投影为监督通知，并进入父会话的 preflight 摘要与唤醒提醒。

- **默认 `observe`**：只提醒，不打断仍在运行的子 agent（与 runtime-server 的 `enforce` 默认值不同，避免本地长跑任务被误杀）。
- **切到 `enforce`**：设置 `AICLI_EXECUTION_SUPERVISOR_MODE=enforce`，行为与 API 一致——到期先 interrupt，超过 cancel grace（15s）仍未结束则标记 orphaned。
- **阈值与 API 同源**：执行 deadline 30m、进度停滞 5m、审批超时 1h、扫描周期 5s、store 故障退避 2m。
- **前提**：本地会话启用了 durable supervision control plane（SQLite store）；未启用时不构建看门狗，`spawn` 行为与未接入时完全一致。

查看与收敛（`/debug supervision ...`）：

```text
/debug supervision watchdog                     # 看门狗状态：接线/循环/mode/阈值/最近决策/未终态 run
/debug supervision list [--all]                 # 未决监督通知（含版本号与 action_required）
/debug supervision ack <id> --note <text>       # 确认通知（必带审计说明）
/debug supervision defer <id> --until 30m       # 延后注入 preflight
/debug supervision resolve <id> --state closed  # 收敛 resolution 状态
/debug supervision control <id> --action cancel --reason <text> [--cascade target|descendants]
                                                # 对通知标的执行 durable 控制动作
                                                # （cancel|close|cancel_subtree|retry|reassign）
```

`control` 与模型侧 `subagent_control`、HTTP 宿主共用同一实现：动作按
request → accept → execute 落 durable action 行（`action_id` 可在输出中复核），
并受 `--expected-version` CAS 保护。每个动作会为同一标的写入解析通知并推进版本，
连续操作前请先重新 `list` 并使用最新的 `<id>` / `--expected-version`；沿用旧行会被
拒绝（`action conflict: state changed`），不会静默重放。

模型侧同一能力由四个 `subagent_*` 工具承载：`subagent_status`（台账汇总，
`include_digest=true` 时输出原 `supervision_snapshot` 的摘要）、
`subagent_inspect_task`（单标的深查，`include_status=false` 时即原 `read_agent_result`）、
`subagent_ack_lifecycle`（决定通知：acknowledge / defer / resolve）与
`subagent_control`（控制动作）。旧名 `supervision_snapshot` / `supervision_descendants` /
`read_agent_result` / `ack_lifecycle` / `control_descendant` 仍可调用（已存提示词与历史
指针不会断），但不再出现在工具列表中。

---

## 5. 全局参数与主题

全局参数（所有子命令通用）：

| 参数 | 说明 |
|------|------|
| `-c, --config <path>` | 配置文件路径（默认按 `./.aicli/config.yaml` → `$HOME/.aicli/config.yaml` → `./aicli.yaml` → `./config.yaml` → `./configs/config.yaml` 顺序查找，首个存在即采用） |
| `-l, --logfile <path>` | 日志文件路径（默认取 `aicli.log.file_path` 或 `log.file_path`） |
| `--theme <name>` | 主题配色/明暗：`classic`、`focus`、`contrast`、`mono` 或 `auto`/`dark`/`light` |
| `--syntax-theme <name>` | 代码语法高亮主题（`auto` 或 Chroma 主题名） |
| `--envelope` | JSON 输出时使用统一 envelope 结构（ok/command/data） |
| `--pprof` | 启用 pprof 诊断端点（127.0.0.1；新会话随机空闲端口，`resume <session-id>` 复用该会话上次的端口） |
| `--web-port <port>` | 指定 loopback 服务器（Web 客户端 / `/debug` 端点共用）监听端口（1-65535）；等价 `AICLI_PPROF=127.0.0.1:<port>` 且优先级更高，越界直接报错退出；显式指定会覆盖该会话已保存的粘性端口 |
| `--web-token <token>` | 预设 Web 写令牌（默认每进程随机；未指定时也可用 `AICLI_WEB_TOKEN`；详见 [aicli-tui-remote.md](aicli-tui-remote.md) 第 2.2 节） |
| `--console-host` | Windows：stdin/stdout 为 PTY/pipe 时在新 Console 窗口重启（MobaXterm/mintty 场景） |

主题优先级：**命令行 `--theme` > 环境变量 > 配置文件**。

环境变量：

| 环境变量 | 说明 |
|----------|------|
| `AICLI_THEME` | 主题名 |
| `AICLI_THEME_MODE` | 明暗模式（auto/dark/light） |
| `AICLI_THEME_SYNTAX` | 语法主题 |
| `AICLI_PPROF` | loopback 服务器监听地址（Web 客户端 / `/debug` 端点）；非空即启用、无需 `--pprof`，可带自定义 host；`--web-port` 优先于它 |
| `AICLI_MESH_DIR` | 网格根目录（节点档案 `nodes/`、会话绑定 `bindings/`、租约 `leases/`、网格日志 `journal/`；默认 `$HOME/.aicli/mesh/`）；设空/未设用默认值，测试或多环境隔离时可覆盖。会话粘性端口存于 `mesh/bindings/<session-id>.json`，旧的 `AICLI_WEB_PORTS_DIR` 不再被读取 |
| `AICLI_WEB_TOKEN` | 预设 Web 写令牌（等价 `--web-token`，flag 优先；≥16 位 URL 安全字符） |
| `AICLI_EXECUTION_SUPERVISOR_MODE` | 本地子 Agent 看门狗模式：`observe`（默认，仅提醒）或 `enforce`（interrupt + cancel grace，见 §4.8） |
| `AICLI_RUN_STALL_TIMEOUT` | run 无进展看门狗阈值：**默认关闭**（`0`），长任务/自动化自然执行到结束；显式设为 Go duration（如 `30m`）才启用，触发会以 `context.Canceled` 中止整个 run（`off`/`0`/`disable` 关闭） |

---

## 6. 配置文件与数据目录

| 路径 | 用途 |
|------|------|
| `~/.aicli/config.yaml` | aicli 全局配置（默认配置文件名） |
| `~/.aicli/aicli.yaml` | aicli CLI 配置（若启用双文件布局） |
| `~/.aicli/sessions/session_history.sqlite` | 会话历史数据库 |
| `~/.aicli/logs/` | 日志目录 |

> Windows 上默认配置目录随构建 profile 而定，可用 `aicli doctor` 查看实际路径。

---

## 7. 退出码与日志

- 正常退出：`0`
- 参数/配置错误：非零（具体见各子命令）
- exec 类任务失败时返回非零退出码，便于 CI 判断

日志默认写入文件（不污染交互/管道输出）；`-l` 可显式指定路径。

---

## 8. 相关文档

| 主题 | 文档 |
|------|------|
| 安装与配置 | [docs/aicli/install.md](../aicli/install.md) |
| 快速上手 | [docs/aicli/quickstart.md](../aicli/quickstart.md) |
| FAQ | [docs/aicli/faq.md](../aicli/faq.md) |
| exec 无头执行 | [docs/aicli/exec.md](../aicli/exec.md) |
| portable agents | [docs/aicli/agents.md](../aicli/agents.md) |
| 图片生成 | [docs/aicli/tool_image_generate.md](../aicli/tool_image_generate.md) |
| Win7 兼容 | [docs/aicli/windows7.md](../aicli/windows7.md)、[windows7-build.md](../aicli/windows7-build.md) |
| aicli-console 启动器 | [aicli-console.md](aicli-console.md) |
