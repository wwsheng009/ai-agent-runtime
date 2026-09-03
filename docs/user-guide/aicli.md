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
aicli mcp add <名称> <URL|命令>  # 添加 MCP server
aicli mcp test <名称> <工具> [参数JSON]  # 测试工具
aicli mcp tools [名称]          # 列出工具
aicli mcp reload                # 重载配置
```

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

---

## 5. 全局参数与主题

全局参数（所有子命令通用）：

| 参数 | 说明 |
|------|------|
| `-c, --config <path>` | 配置文件路径（默认按 `$HOME/.aicli/` → `./.aicli/` → `./` → `./configs/` 顺序查找） |
| `-l, --logfile <path>` | 日志文件路径（默认取 `aicli.log.file_path` 或 `log.file_path`） |
| `--theme <name>` | 主题配色/明暗：`classic`、`focus`、`contrast`、`mono` 或 `auto`/`dark`/`light` |
| `--syntax-theme <name>` | 代码语法高亮主题（`auto` 或 Chroma 主题名） |
| `--envelope` | JSON 输出时使用统一 envelope 结构（ok/command/data） |
| `--pprof` | 启用 pprof 诊断端点（127.0.0.1 随机空闲端口） |
| `--console-host` | Windows：stdin/stdout 为 PTY/pipe 时在新 Console 窗口重启（MobaXterm/mintty 场景） |

主题优先级：**命令行 `--theme` > 环境变量 > 配置文件**。

环境变量：

| 环境变量 | 说明 |
|----------|------|
| `AICLI_THEME` | 主题名 |
| `AICLI_THEME_MODE` | 明暗模式（auto/dark/light） |
| `AICLI_THEME_SYNTAX` | 语法主题 |
| `AICLI_PPROF` | pprof 监听地址（需同时启用 --pprof） |

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
