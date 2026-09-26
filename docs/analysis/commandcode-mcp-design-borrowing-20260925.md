# CommandCode MCP 文档设计借鉴分析

- **日期**：2026-09-25
- **来源**：<https://commandcode.ai/docs/mcp>（Command Code "MCP Servers" 文档全文）
- **对照对象**：本仓库 `aicli` 的 MCP 实现（`backend/internal/mcp/**`、`backend/cmd/aicli/commands/mcp.go`、`chat_mcp_command.go`、`docs/aicli/install.md`）
- **结论口径**：区分「值得借鉴（缺口）」与「已有且更完整（不要回退）」，每条建议都给出代码落点。

---

## 1. TL;DR：可借鉴清单

| # | 主题 | CommandCode 做法 | aicli 现状 | 建议 | 优先级 |
|---|------|------------------|------------|------|--------|
| 1 | 环境变量插值 | `${VAR}` / `${VAR:-default}` / `$${}` 转义；未设置且无默认值 → **报错并指名变量**；作用于 http headers 与 stdio env，运行时解析 | `os.ExpandEnv` 只展开 URL/Command/Env 值（`loader.go:128-147`）；缺失变量→**静默空串**；`headers:` 字段无插值 | 实现带默认值/转义的插值器，扩展到 headers/args，缺失即报错指名变量 | **P0** |
| 2 | `--auth oauth` 死参数 | 无此问题（OAuth 自动检测） | `--auth` 声明后从未被消费（`mcp.go:32/85/315/734`，`runMCPAddCommand` 不读 `AuthType`） | 二选一：实现（接入第 5 项）或移除并文档说明 | **P0** |
| 3 | CLI 参数语义 | `cmd mcp add <name> -- <command> <args...>`；flag 必须在 name 之前；传输按目标推断 | `add <名称> <URL\|命令>`，默认 `--transport sse`；帮助示例 `... -- npx chrome-devtools-mcp@latest` 按现行解析会把 server 命名为 `npx` | 明确 `--` 约定（name 在 `--` 前，命令在 `--` 后），URL→streamable/命令→stdio 自动推断，修正示例 | **P0** |
| 4 | CLI `--env` | `mcp add --env KEY=VALUE`（stdio） | CLI `add` 无 `--env`（`/mcp add` 有，`chat_mcp_command.go:23`） | 补齐 `--env`，两入口行为对齐 | **P0** |
| 5 | MCP OAuth | OAuth 2.0 + PKCE，自动检测 401、浏览器授权、token 存储 `~/.commandcode/mcp-tokens.json` 自动刷新；`mcp auth / --status / --list / --clear` | `internal/mcp` 下无任何 OAuth 代码（`oauth` 零命中）；仅有 provider 登录 OAuth（`agentconfig/auth_store.go`、`providerops/login.go`） | 新增 MCP auth 子系统 + `aicli mcp auth *` + `/mcp` 认证状态 | **P1** |
| 6 | 配置 scope | `local`（私有/项目）> `project`（`.mcp.json`，可提交）> `user`（全局），**同名逐级覆盖**；写时 `--scope` 显式选择；提交文件剥离 OAuth client secret | 发现式**单文件**「命中即用」：`./.aicli/mcp.yaml` > `~/.aicli/mcp.yaml` > 向上搜索 > `configs/mcp.yaml`（`install.md:534-551`）；`.aicli/` 整体 gitignore（`.gitignore:17`），无共享项目层、无同名叠加 | 引入三级 scope + 同名覆盖 + 项目层秘密剥离；至少支持「全局基础 + 项目增量」 | **P1** |
| 7 | 跨 agent 导入 | `/import claude\|codex\|cursor\|opencode\|gemini` 一键迁移 MCP/skills/agents/memory | 无此类能力；`aicli import` 是会话 JSON 导入（`import.go:97-116`）；但 `foldertrust` 已识别 `.mcp.json`/`.cursor/mcp.json`（`foldertrust/configs.go:79-97`） | 增加 `aicli mcp import --from ...`（dry-run/JSON/冲突策略） | **P1** |
| 8 | 机器可读配置 | `mcp add-json '<json>'`（`type` 为 `transport` 别名）；文档给出可复制的 JSON | 无 `add-json`/`get --json`；配置文件本身已支持 JSON/YAML（`loader.go:37-57`） | 增加 `add-json` 与 `mcp get <name> [--json]`（导出单 server 配置） | P2 |
| 9 | 会话内 `/mcp` | 交互式菜单：颜色状态（连接/已认证/**需认证**/错误）+ 每 server 工具数 + 动作（connect/auth/remove） | 文本面板已有：状态标记 ●/◐/○、工具数、trust、最近连接、错误（`chat_mcp_command.go:145-169/243-249`） | 增量做 picker/动作菜单；补「需认证」状态（依赖第 5 项） | P2 |
| 10 | 文档 IA | 1 分钟 quickstart（5 步）→ 命令行参考表 → 常见 server recipes → 症状式排错 | 文档详尽但分散（`docs/mcp/`、`docs/aicli/install.md`、`docs/user-guide/`） | 补 `docs/mcp/quickstart.md` + 症状→命令排错表 + 复制即用 recipes | P2 |

> 已有且**明显强于** CommandCode、不应回退的能力：trust level（`local/trusted_remote/untrusted_remote`）、工具级启停、health check（tools/resources/toolArgs）、`test`/`test-server`/`tools` 子命令、stdio stderr 诊断、Windows 进程树治理、`mcp__<server>__<tool>` 规范名与重名隔离、ACP 客户端下发 `mcpServers`（merge/local/client/off）、profile 分层与 MCP diff、热重载与三端（CLI `/mcp`/console/web）共用同一 Service。

---

## 2. CommandCode 文档的设计要点提炼

按「接入路径 / 配置模型 / 认证 / 会话内管理 / 迁移 / 文档结构」六组归纳（均来自文档原文行为，不含推测）：

1. **接入路径**
   - 两种 transport 一等公民：`--transport http <name> <url>` 与默认 stdio `mcp add <name> -- <command> <args...>`。
   - 复杂配置可用 `add-json`（JSON 可直接在机器间复制）。
   - 约 1 分钟 Quickstart：加 server → 需要时授权 → `mcp list`/`mcp get` 验证 → 自然语言调用 → 可选 JSON/导入路径。
2. **配置模型**
   - `mcpServers` 映射；字段 `transport|type`、`url`、`headers`、`env`、`enabled`。
   - 三级 scope 与优先级：`local`（`~/.commandcode/projects/<slug>/mcp.json`，默认）> `project`（项目根 `.mcp.json`，**入库共享**）> `user`（`~/.commandcode/mcp.json`）。
   - **同名 server 逐级覆盖**，而不是整文件二选一。
   - 环境变量插值：`${VAR}`、`${VAR:-default}`、`$${NAME}` 转义；未设置且无默认值 → 该 server 启动失败并**指名变量**；作用于 http headers 与 stdio env。
   - 写 `project` scope 时 OAuth client secret 自动剥离到 `~/.commandcode/mcp-tokens.json`。
3. **认证**
   - OAuth 2.0 Authorization Code + PKCE；对需要认证的远端自动检测并拉起浏览器授权；token 本地安全存储、自动刷新；刷新失败提示重新 `mcp auth`。
   - ACL 面：`mcp auth <server>`、`--status`、`--list`、`--clear`；`add-json` 可配 `oauth.clientId`/`callbackPort`，支持 `--client-secret`。
   - 文档明确「浏览器未自动打开时手动复制 URL」「OAuth 同时适用于 HTTP 与 SSE」。
4. **会话内管理**
   - `/mcp` 交互菜单：连接状态色（绿=已连接、青=已认证、黄=需认证、红=错误）、每 server 工具数、动作（连接/认证/移除）。
   - 工具命名 `mcp__<server>__<tool>`，并强调「用户只需自然语言描述，无需记工具名」。
5. **迁移兼容**
   - `/import` 从 Claude Code / Codex / Cursor / OpenCode / Gemini CLI 导入 MCP、skills、agents、自定义命令与 memory；无参数时扫描全部来源。
6. **文档结构**
   - 表格化命令参考（命令/说明/示例）、常见 server recipes（Notion、GitHub、Playwright、Sentry、OpenAI Docs、Context7、Figma、Chrome DevTools）、三段式排错（连不上 / 认证问题 / 工具不出现，每条给具体命令）。
   - 明确约束「所有 flag 必须在 server 名之前」「`--` 分隔 name 与命令」。

---

## 3. aicli 现状盘点（代码证据）

| 维度 | 现状 | 证据 |
|------|------|------|
| CLI 子命令 | `add / remove / list / status / enable / disable / tools / test / test-server / reload`（无 `get`/`add-json`/`auth`/`import`） | `backend/cmd/aicli/commands/mcp.go:44-161` |
| `add` flags | `--transport`（**默认 sse**）、`--description`、`--command`、`--header`、`--auth`（未消费）；无 `--env`/`--scope` | `mcp.go:81-85`、`mcp.go:315`、`mcp.go:517-556` |
| 传输层 | stdio / sse / websocket / streamable，别名归一化（`http`→`streamable`、`ws`→`websocket`） | `admin/configfile.go:66-87`、`config/types.go:195-200` |
| 配置格式 | YAML 为主、JSON 兼容；`mcpServers` + `global`（健康检查/超时默认） | `config/loader.go:37-57`、`config/types.go:19-22` |
| 配置解析 | 单文件「发现链」：session/profile 显式 > `./.aicli/mcp.yaml` > `~/.aicli/mcp.yaml` > 向上搜索 > exe 目录 > `configs/mcp.yaml`；写路径命中即写，全不存在落 `~/.aicli/mcp.yaml` | `docs/aicli/install.md:534-551`、`mcp.go:576-584` |
| 插值 | `os.ExpandEnv`，仅 URL/Command/Env 值；无默认值/转义语法，无缺失报错；headers 写入时镜像成 `HEADER_*` env（间接获得一层展开，两条路径语义不一致） | `config/loader.go:128-147`、`admin/configfile.go:266-313` |
| 认证 | `internal/mcp` 无 OAuth/token store；provider 侧有 OAuth（device-code 形态）可作参考实现风格 | `oauth` 在 `backend/internal/mcp` 零命中；`agentconfig/auth_store.go`、`providerops/login.go`、`chat_login_command.go:22-31` |
| 治理 | trust level、工具级启停、health check、`maxParallelCalls`、timeout/retry、workingDir | `config/types.go:10-16/91-153` |
| 工具命名 | `mcp__<server>__<tool>` + portable 名 + shadow 名消歧 | `registry/registry.go:231/344` |
| `/mcp` | list/status/add/remove/enable/disable/reload；文本面板含 tool count、trust、最近连接、LastError；无交互动作菜单 | `chat_mcp_command.go:15-29/111-169/243-249` |
| 三端一致 | CLI、chat `/mcp`、console/微型 Web 共用 `mcp/admin.Service`（读→改→校验→原子写→热重载） | `admin/configfile.go:1-5`、`plan/mcp-management-ui-plan.md:32-35` |
| ACP 集成 | 客户端 `session/new|load` 可下发 `mcpServers`，merge 策略 `merge/local/client/off`，会话级与进程级 manager 按「客户端优先」合并 | `agent.go:131`、`manager/merged.go:13-23`、`scripts/acp_e2e_mcp_*.go` |
| Profile 集成 | profile 目录可带 `mcp.yaml`，`/profile show|validate|diff` 汇总 MCP 增删 | `chat_profile_command.go:581/647`、`profile_validate.go:194`、`chat_profile.go:199` |
| 迁移 | 无跨 agent MCP 导入；`aicli import` 只做会话 JSON 回灌 | `import.go:97-116` |
| 信任门 | `foldertrust` 已把 `mcp.yaml/.json/.mcp.json/.cursor/mcp.json/configs/mcp.yaml` 识别为项目级 MCP 配置并纳入 gate | `internal/foldertrust/configs.go:79-97` |

---

## 4. 重点借鉴项：设计映射与落点

### 4.1 【P0】环境变量插值升级为「严格模式」

- **CommandCode 语义**：`${VAR}`、`${VAR:-default}`、`$${NAME}` 转义；未设置且无默认值 → server 启动失败、错误信息指名变量；headers 与 stdio env 均支持；运行时解析（不落盘明文）。
- **aicli 现状**：`loader.go:128-147` 对 URL/Command/Env 值调用 `os.ExpandEnv`。三个缺口：
  1. 未设置变量 → 空串，`Authorization: Bearer ${TOKEN}` 会静默发成 `Bearer `（难排查）；
  2. 无 `:-default` 与 `$${}` 转义语法；
  3. `headers:` 字段不在展开范围（仅 CLI `--header` 经 `HEADER_*` env 镜像间接展开，两条配置路径行为不一致）。
- **建议**：
  - 新增 `internal/mcp/config/expand.go`：解析 `${NAME}` / `${NAME:-default}` / `$${...}`，返回收集到的缺失变量列表；不引入 shell 全语法，只实现文档承诺的子集。
  - 展开范围扩展为 `url / command / args / env 值 / headers 值`；缺失且无默认值时返回聚合错误：`MCP "x" 引用未设置的环境变量 TOKEN`（一次列全，便于修复）。
  - 与 `ApplyDefaults` 一样，同时供「文件加载」与「内存/ACP 下发配置」两条入口复用，避免两套语义。
- **落点**：`backend/internal/mcp/config/loader.go`、`config/types.go`、`admin/configfile.go`。
- **风险**：`$${}` 转义可保护历史字面量；建议仅对新增语法严格，`$VAR` 旧式展开保留兼容（或按文档标注 deprecated）。

### 4.2 【P0】`--auth oauth` 死参数处置

- **现状**：`mcp.go:85` 暴露 `--auth`，`mcpAddCommandOptions.AuthType` 被赋值（`mcp.go:734`）但 `runMCPAddCommand` 从不读取，属"声明即承诺但无行为"。
- **建议**：按 4.4 实现 OAuth 前，先二选一：
  - 移除该 flag（并把「远端需要认证时如何配置」写进 `mcp add --help` 与 FAQ）；或
  - 保留但显式报错 `--auth oauth 尚未实现，请先配置 headers 方式`，避免静默吞掉用户意图。
- **落点**：`backend/cmd/aicli/commands/mcp.go`。
- **判据**：仓库内应无「定义了但从不消费的 CLI flag」——可直接纳入 help 回归测试（`help_docs_regression_test.go` 同族）。

### 4.3 【P0】`add` 参数语义与传输推断对齐 CommandCode

- **CommandCode 约定**：`mcp add <name> [flags] -- <command> <args...>`；flag 必须先于 name；`--transport http` 显式声明远端。
- **aicli 现状**：`add <名称> <URL|命令>`，`--transport` 默认 `sse`。两个具体问题：
  1. 帮助示例 `aicli mcp add --transport stdio -- npx chrome-devtools-mcp@latest`（`mcp.go:74`）按当前解析会把 server 命名为 `npx`、命令变成 `chrome-devtools-mcp@latest`，用户照抄即错；
  2. CLI 不推断传输类型，而 `/mcp add` 已经能做「URL→streamable、`ws(s)→websocket`」（`chat_mcp_command.go:19-25`），两个入口行为不一致。
- **建议**：
  - 用 `cmd.ArgsLenAtDash()` 区分：`--` 之前的位置参数 = name（可省略，缺省自动取名或报错），`--` 之后 = stdio command+args；保留现有 `--command/--arg` 写法兼容。
  - 未显式 `--transport` 时按 target 推断（URL→streamable、`ws/wss`→websocket、其余→stdio），并把 `sse` 从默认值降级为显式选项；对既有脚本，推断结果与显式 `sse`+URL 的语义差异需在 changelog 标注。
  - 同步修正帮助示例为 `aicli mcp add chrome-devtools -- npx -y chrome-devtools-mcp@latest`，并补 `--env KEY=VALUE`。
- **落点**：`mcp.go`（Args/Flags/Run）、`admin/configfile.go`（Upsert 已支持 Env）、`docs/aicli/install.md`、`docs/user-guide/aicli.md`。
- **测试**：`mcp_add_transport_test.go` 增加 `--` 场景（name 在 `--` 前 / 缺 name / `ws://` 推断）。

### 4.4 【P1】MCP OAuth 2.0 + PKCE + token 生命周期

- **CommandCode 语义**：远端需要认证 → 自动检测（401 挑战）→ 浏览器授权（PKCE，Authorization Code）→ token 存 `mcp-tokens.json` → 自动刷新；提供 `auth`/`--status`/`--list`/`--clear`；`oauth.clientId`/`callbackPort` 可配；提交型配置文件剥离 secret。
- **aicli 现状**：`internal/mcp` 完全没有 OAuth；provider 登录侧已有 OAuth token 存取（`agentconfig/auth_store.go`，字段含 access/refresh/id token）与 device-code 轮询经验（`providerops/login.go`、`chat_login_command.go:22-31`），可复用**存储与刷新范式**，但 MCP 需要本机回调 + PKCE，形态不同。
- **建议最小闭环**：
  1. `internal/mcp/auth`：discovery（`WWW-Authenticate` / RFC 9728 resource metadata，失败则提示手动配置）→ 动态或手工 clientId → PKCE + 本机 loopback 回调（`--callback-port`，默认随机）→ token 持久化 `~/.aicli/mcp-tokens.json`（0600；Windows 走 ACL/用户目录语义）→ 过期前刷新，刷新失败标记 `requires_auth`。
  2. transport 层把 401/403 归一为 `MCPStatus{RequiresAuth:true}` 而不是泛化 `LastError`（`transport/streamable.go`、`manager` 状态聚合），`mcp status`/`/mcp`/web 面板统一展示「需认证」。
  3. CLI 面：`aicli mcp auth <name>`、`--status`、`--list`、`--clear`；`/mcp auth <name>` 复用同一 Service（`admin.Service` 已是三端共用，扩展点清晰）。
  4. 与 trust 模型交互：OAuth 只解决"能不能连"，不绕过 `foldertrust` 的项目级 MCP gate（`foldertrust/configs.go:79-97`）；默认 `untrusted_remote` 的 server 不应因完成 OAuth 而自动提权。
- **落点**：新包 `internal/mcp/auth`、`transport/streamable.go`、`manager`、`admin`、`mcp.go`、`chat_mcp_command.go`、`web_mcp_handlers.go`。
- **风险**：Windows 7 兼容构建（本仓库有 `*_win7` 分支）——OAuth 网络/证书栈不可用时必须优雅降级为"配置 headers 手动认证"路径；浏览器打开失败时打印可复制的 URL（对齐 CommandCode 文档行为）。

### 4.5 【P1】三级 scope 与「同名覆盖 + 秘密剥离」

- **CommandCode 语义**：`local > project > user` 三级，**同名 server 逐级覆盖**；`--scope` 决定写入目标；`project`（可提交）写入时自动剥离 OAuth client secret。
- **aicli 现状**：单文件「发现链、命中即用」（`install.md:534-551`）。带来两个真实限制：
  1. 无法「用户级放公共 server + 项目级加增量 server」——项目文件一旦存在，用户文件整体失效；
  2. `.aicli/` 被整体 gitignore（`.gitignore:17`），团队没有「可提交共享」的 MCP 配置层。
- **建议（分两步，避免一次性推翻发现链）**：
  - **Step 1（低风险）**：保留发现链决定"主文件"，但把解析结果改为**按 name 合并**：低优先级文件提供基础项，高优先级文件同名覆盖；冲突不静默，`mcp list` 标注来源文件与 shadow 关系。这样无需新增 scope 即可解决"全局基础 + 项目增量"。
  - **Step 2（可选）**：新增显式 `--scope user|local|project`：`user = ~/.aicli/mcp.yaml`、`local = ~/.aicli/projects/<slug>/mcp.yaml`（私有）、`project = 项目根 .aicli/mcp.json`（需在 `.gitignore` 中开豁免，或采用 `.mcp.json` 兼容名）。写入 `project` 时：
    - 检测 secrets（`Authorization`/`api-key`/`token` 等 header、env 值）→ 改写为 `${VAR}` 引用并给出提示，或拒绝写入并要求用 `${VAR}`/`--env`；
    - OAuth token 永远只落用户级 token store，不进入项目文件（对齐 CommandCode 行为）。
- **落点**：`internal/aiclipaths`（已有 profile/项目路径解析）、`internal/mcp/config`（合并器）、`admin.Service`（写入时 scope 校验与秘密剥离）、`docs/aicli/install.md` 解析表。
- **风险**：发现链有测试与文档承诺（`mcp_config_resolution_test.go`、`install.md`），Step 1 需明确"合并"与既有"命中即用"的差异并更新文档；Windows 路径/大小写与 `foldertrust` 组合要加回归。

### 4.6 【P1】跨 agent MCP 配置导入

- **CommandCode 语义**：`/import` 无参扫全部来源，或 `claude|codex|cursor|opencode|gemini` 定向导入；导入内容含 MCP、skills、agents、命令与 memory。
- **aicli 现状**：无 MCP 迁移能力；`foldertrust` 已经知道要检查 `.mcp.json`、`.cursor/mcp.json`（`configs.go:79-97`），说明"外部格式存在"已在视野内，但没有导入管道。
- **建议**：实现 `aicli mcp import --from claude|codex|cursor|gemini|opencode [--scope] [--dry-run] [--json] [--on-conflict skip|overwrite|rename]`：
  - Claude Code：`.mcp.json`、`~/.claude.json` 的 `mcpServers` —— 与 aicli 的 JSON schema 天然同构（`type: http|sse|stdio` → `streamable|sse|stdio`），映射成本最低，建议首个支持；
  - Cursor：`.cursor/mcp.json`（同上 schema）；Codex：`~/.codex/config.toml` 的 `[mcp_servers.*]`（TOML 需解析 `command/args/env/url/http_headers`）；Gemini CLI / OpenCode：各自 settings 的 `mcpServers`。
  - `--dry-run` 输出"将导入 N 个、冲突 M 个、跳过 K 个"的结构化摘要；导入同样执行 4.5 的秘密策略（`${VAR}` 化或落用户级）。
- **落点**：新包 `internal/mcp/importers/<vendor>.go`（纯解析、可单测）+ `commands/mcp.go` / `chat_mcp_command.go` 入口 + 文档。
- **价值**：这是"本地优先、可迁移"定位的直接兑现，也是获客路径（用户已有 Claude/Cursor 配置时零成本切换）。

### 4.7 【P2】`add-json` / `get`（机器可读单 server 配置）

- **CommandCode 语义**：`mcp add-json <name> '<json>'` 便于脚本/跨机复制；`mcp get <name>` 查看单 server 详情。
- **aicli 现状**：加载器已支持 JSON 文件（`loader.go:37-57`），`admin.UpsertRequest` 也已是 JSON 结构（`configfile.go:50-64`），缺的只是 CLI 表层：`add-json` 与 `get`（`status` 已含部分信息，但没有"导出可复制配置"的语义）。
- **建议**：`aicli mcp add-json <name> '<json>'`（接受 `type` 别名，复用 `NormalizeTransportType`）+ `aicli mcp get <name> [--json]`（输出单 server 的规范化配置，含来源文件），与 `--output json`/envelope 约定一致（`mcpOutputFormat` 已有）。

### 4.8 【P2】`/mcp` 交互菜单与「需认证」状态色

- **CommandCode 语义**：绿色=已连接、青色=已认证、黄色=需认证、红色=错误；每 server 工具数；动作 connect/auth/remove。
- **aicli 现状**：文本面板已输出状态标记、tool count、trust、最近连接与错误（`chat_mcp_command.go:145-169/243-249`），但没有可交互动作，也没有"需认证"独立状态（当前会落进泛化 `LastError`）。
- **建议**：在 4.4 引入 `RequiresAuth` 状态后，复用 chat 现有 picker 基建（`chat_model_picker.go` 一族）为 `/mcp` 增加选择器：列出 server → 动作（状态查看 / 认证 / 启停 / 移除）；TUI 用颜色语义，非 TTY 保持现有纯文本输出（向后兼容）。

### 4.9 【P2】文档信息架构（可直接照搬的部分）

- 借 CommandCode 的三段式：
  1. **≤1 分钟 quickstart**：`docs/mcp/quickstart.md`（add → 验证 → 自然语言调用），链接到现有 chrome-devtools 等 recipes；
  2. **命令参考表**：现有 `install.md` 的 MCP 子命令概览已接近，可补"示例"列与 flag 顺序约束（`--` 约定、flag 必须在 name 前）；
  3. **症状式排错表**：连不上 / 认证失败 / 工具不出现 三类，每类给具体命令（`aicli mcp get`、`test-server --show-stderr`、`/mcp status`）——aicli 的诊断能力（stderr 尾部、health check）比原文更强，只差"按症状索引"。
- 另可补 GitHub/Notion/Sentry/Context7/Figma 的复制即用配置 recipes；仓库已有 `docs/mcp/chrome-devtools.md` 的写法可作模板。

---

## 5. 不建议照搬 / 需要保留的差异

| 项 | CommandCode | 建议 |
|----|-------------|------|
| 默认 transport | stdio 为默认 | 不盲目改默认：改为**按 target 推断**（4.3）并保留显式覆盖；直接改默认会破坏既有 `--transport` 省略的脚本 |
| flag 顺序严格性 | 强制 flag 在 name 前 | 保留 cobra 宽松解析，仅文档化 + 帮助示例正确化，避免破坏现状 |
| 主配置格式 | `.mcp.json`（JSON）为主 | 保持 YAML 主格式（含 global/healthCheck 等扩展字段），把 `.mcp.json` 作为导入/共享兼容层 |
| scope 覆盖语义 | local > project > user 同名覆盖 | 采用"同名覆盖"思想，但**基于现有发现链**渐进改造（4.5 Step 1），一次性切三 scope 会冲击 `mcp_config_resolution_test.go` 与 `foldertrust` 门 |
| 配置能力 | 文档只覆盖基础字段 | **不回退** aicli 已有：trust level、工具级启停、health check、并行度、retry、stderr 诊断、ACP 下发、profile 分层——这些应继续作为对外差异点 |
| 个人配置目录 | `~/.commandcode/*` | aicli 已有 `~/.aicli` 约定与多平台路径解析（`internal/aiclipaths`），沿用，不新造目录 |

---

## 6. 建议落地顺序（最小切片）

1. **M1（正确性与一致性，约 1-2 天）**：4.1 插值器 + 4.2 死参数处置 + 4.3 `--`/推断/`--env`/示例修正 + 4.9 中的命令参考表与 flag 约束。全部是低风险改动，且是后续 scope/秘密剥离的前置。
2. **M2（认证能力）**：4.4 OAuth（先 streamable HTTP，再 sse）+ `mcp auth` 子命令 + 401→`RequiresAuth` 状态贯通三端；win7 降级路径与手动 header 兜底同时交付。
3. **M3（配置模型）**：4.5 Step 1 同名合并 → Step 2 scope 与秘密剥离；更新 `install.md` 解析表与回归测试。
4. **M4（生态与体验）**：4.6 导入（Claude/Cursor 先行）→ 4.7 `add-json`/`get` → 4.8 `/mcp` 菜单 → 4.9 recipes 扩充。

**验收建议**：
- M1 用单测覆盖插值语法矩阵（缺省值/转义/缺失报错）与 `--` 解析矩阵；
- M2 需一个可复现的 OAuth 测试 server（或 mock discovery + 本地回调）端到端脚本，参照 `scripts/acp_e2e_mcp_*.go` 的 e2e 风格；
- M3/M4 各补一条"用户级基础 + 项目级增量"与"从 Claude 配置导入"的端到端用例。

---

## 7. 参考

- CommandCode MCP 文档：<https://commandcode.ai/docs/mcp>
- aicli 关键代码：`backend/cmd/aicli/commands/mcp.go`、`chat_mcp_command.go`、`backend/internal/mcp/{config,admin,registry,manager,transport}`、`backend/internal/foldertrust/configs.go`、`backend/internal/aiclipaths`
- aicli 关键文档：`docs/aicli/install.md`（§MCP 子命令概览 / 配置解析表）、`docs/mcp/README.md`、`docs/plan/mcp-management-ui-plan.md`

---

## 8. 实施备注（M1 落地时的两处设计修正，2026-09-25）

落地过程中实测发现原方案（4.1 计划"缺失即让配置加载失败"）会带来两个真实问题，已按更贴近 CommandCode 语义的方式修正：

1. **缺失变量按 server 隔离，而不是让整份配置失败**
   - `ExpandEnv` 不再返回错误，而是把缺失项写入 `MCPConfig.EnvError`（`yaml:"-"`，仅内存态，不落盘）；
   - `manager.StartAsync` 对带 `EnvError` 的 server 跳过连接，并把错误写入 `MCPStatus.LastError`，`aicli mcp list/status`、`/mcp`、Web 面板均可看到；
   - 其它 server、`mcp add/enable/disable/remove` 等管理操作完全不受影响。这正是 CommandCode 文档
     「A variable that is not set and has no default **stops that server** with an error naming the variable」的语义。
2. **配置文件保持原始 `${VAR}`，展开只在运行时入口发生**
   - 若在 `Loader.Load()` 就地展开，`mcp add/enable/remove` 的"读-改-写回"会把展开结果（真实密钥或空串）固化进配置文件；
     实测已复现：`HEADER_Authorization: 'Bearer '` 被写回，引用丢失；
   - 现在 `Loader.Load()` 不展开；仅 `manager.LoadConfig` 与 `config.CloneWithDefaults`（内存 / ACP 下发配置）调用 `ExpandEnv`，
     并有回归测试 `TestServiceAddKeepsRawEnvRefsOnDisk` 守住"管理写入不丢引用、不落密钥"的契约。

M1 交付清单：

- `internal/mcp/config/expand.go`：`${VAR}` / `${VAR:-default}` / `$${}` 转义 + 历史 `$VAR` 兼容（args/headers 仅显式语法）；
- `internal/mcp/config/{loader,clone,types}.go`、`internal/mcp/manager/manager.go`：展开时机与按 server 隔离；
- `cmd/aicli/commands/mcp.go`：`--` 语义、传输推断、`--env`、`--auth` 显式报错、帮助示例修正、list/status 展示「最近错误」；
- `internal/mcp/admin/configfile.go`：`--env` / UpsertRequest.Env 由整体替换改为增量合并（与 headers 行为一致）；
- 测试：`config/expand_test.go`、`admin/configfile_env_test.go`、`manager/manager_env_error_test.go`、`commands/mcp_add_args_test.go`；
- 文档：`docs/aicli/install.md`、`docs/user-guide/aicli.md`。

后续里程碑不变：M2 OAuth → M3 scope/秘密剥离 → M4 跨 agent 导入与 `/mcp` 交互菜单。

---

## 9. M2 实施备注（OAuth 2.0 + PKCE + 令牌生命周期，2026-09-25）

对齐 §4.4 的落地结果：

**新增 `internal/mcp/auth` 包**

- `store.go`：令牌存 `~/.aicli/mcp-tokens.json`（`AICLI_MCP_TOKENS_FILE` 可覆盖），临时文件 + 原子重命名写入（0600、目录 0700）；
  同名 server 的 `serverUrl` 不一致时视为未登录，避免把旧站令牌发给新站。
- `discovery.go`：探测 MCP 端点 401 的 `WWW-Authenticate`（含 `resource_metadata` / `scope`）→ RFC 9728 受保护资源 metadata
  → RFC 8414 / OIDC 授权服务器 metadata；允许 `auth.authorizationServer` 覆盖，失败信息带 `headers` 手动兜底提示。
- `pkce.go`：S256 PKCE（RFC 7636 测试向量已覆盖）+ state。
- `flow.go`：Authorization Code 流程，本机 `127.0.0.1:<port>/callback` 回调；无 `clientId` 时 RFC 7591 动态注册；
  浏览器打开失败或 `--no-browser` 时打印可复制 URL，并支持手动粘贴回调 URL / 裸 code。
- `session.go`：`AccessToken`（临近过期自动刷新 + 落盘）、`ForceRefresh`（401/403 后强制刷新）、`NeedsAuth` 状态与
  `SessionStatus` 快照（不含令牌明文）；实现 `config.AccessTokenProvider`。

**传输与状态贯通**

- `transport`：新增 `AccessTokenProvider` 与 `oauthRoundTripper` 行为——按需注入 `Authorization: Bearer`，
  静态 `headers` 里的 Authorization 优先（手动兜底不被覆盖）；401/403 时缓冲请求体、强制刷新并重试一次，
  刷新失败则保留原始 401（由上层标记「需认证」）。
- `manager`：oauth server 在启动时建立会话；未登录 → 隔离为 `RequiresAuth` 且 `LastError` 给出
  `aicli mcp auth <名称>` 提示；连接失败时若会话处于需认证状态则回写状态；连接成功清空标记。
- `MCPStatus.RequiresAuth` 贯通 CLI（`list`/`status` 显示「需认证」）、chat `/mcp`（`!` 标记 + 错误行）、
  微型 Web 面板（黄色「需认证」徽标 + 执行提示）；runtime-server/Web API 的 JSON 自动带 `requiresAuth`。

**CLI**

- `aicli mcp add ... --auth oauth [--oauth-client-id|--oauth-client-secret|--oauth-scope|--oauth-callback-port|--oauth-auth-server]`；
  `--auth none` 清除认证；配置仅支持远程 streamable/sse（stdio/websocket 报可行动错误）。
- `aicli mcp auth <名称>`（`--no-browser`、`--scope`、`--client-id/--client-secret`、`--callback-port`、`--auth-server`、`--timeout`）、
  `aicli mcp auth --status|--list|--clear [--all]`、`aicli mcp logout <名称> [--all]`；`--list` 仅输出元数据，绝不打印令牌明文。
- win7compat 构建：不自动打开浏览器（始终打印授权链接），其余逻辑与主构建一致。

**验证**

- 单测：PKCE 向量、store 权限/原子写/URL 不匹配、discovery（挑战解析、候选地址、失败兜底）、
  transport（注入/静态头优先/401 刷新重试/刷新失败保留 401/provider 报错）、manager（未登录隔离、有令牌注入 provider、损坏令牌文件隔离）、
  config（`auth: oauth` 简写与结构化解析、stdio/未知类型/端口校验、运行时字段不落盘）、CLI（`--auth` 参数矩阵、oauth 落盘、`--list` 不含密钥）。
- 真实二进制端到端：`go run ./scripts/mcp_oauth_e2e.go` —— 本地 mock 授权服务器 + 受保护 MCP 端点，
  覆盖「未登录→需认证 / `--no-browser` 授权（PKCE 校验通过）/ 动态注册 / 令牌落盘且清单无明文 /
  带令牌连接成功（tools/list） / `logout` 回到需认证」。

**与 M3/M4 的边界**

- `project` scope 的 client secret 剥离（§4.5）留待 M3；当前 CLI 已在 help 中提示 `--oauth-client-secret` 会明文写入配置文件。
- `/mcp` 交互菜单与 TUI 状态色（§4.8）留待 M4，本次仅贯通状态字段与文本/徽标展示。

---

## 10. M3 实施备注（配置模型：同名合并 + scope + 秘密剥离，2026-09-25）

对齐 §4.5 的 Step 1 / Step 2 落地结果：

**Step 1：分层加载与同名覆盖**

- `internal/mcp/config/layered.go`：
  - `DiscoverSources(explicit)` 把发现链解析为「低→高」候选（default/upward < user < project）；
    `explicit` 为真实覆盖路径时只返回该文件（保持脚本语义），为约定路径（如 `configs/mcp.yaml`）时仍走发现链。
  - `LoadLayered` 加载所有已存在文件并按名合并：**同名整体覆盖**（不做字段级合并，语义可预测），
    `global` 取最高优先级文件；`ServerOrigin` 记录胜出层级与 `Shadowed`（被覆盖的低优先级定义）。
  - 容错：低优先级文件损坏 → 跳过 + `Warnings`；最高优先级（生效）文件损坏 → 直接报错，避免「配置静默消失」。
  - `Loader.Load` 与分层加载共用 `loadConfigFile`/`unmarshalConfig`，两套入口不会漂移。
- `internal/mcp/manager`：新增可选能力 `LayeredConfigLoader`（`LoadConfigEffective`）与 `ConfigOriginReporter`
  （`MCPConfigOrigins`/`MCPConfigWarnings`），`mergedManager` 做并集转发；`MCPStatus` 增加
  `configSource`/`configPath`/`shadowedSources` 字段，由 `mcpStatusLocked` 从 origins 注入。
- 贯通三端：CLI `mcp list`/`status` 打印「来源/覆盖」，chat `/mcp` 面板同字段，微型 Web 面板新增来源行；
  runtime-server 启动日志记录分层告警；`/web/api/mcps`、`/api/runtime/mcps` 随 JSON 自动带出来源字段。
- 入口切换：`ensureMCPManager`（CLI 全部状态类命令）、`mcp test-server`、chat（`prepareChatMCPManager` +
  微型 Web 管理服务，新增 `explicitOverride` 透传）、runtime-server（`loadRuntimeMCPManagerConfig`）。
  显式 `--config-file` / `MCP_CONFIG_FILE` / session 绑定路径仍精确加载单文件。

**Step 2：`--scope` 与项目级秘密剥离**

- `--scope user|local|project`（`aicli mcp add`）：
  - `user` → `~/.aicli/mcp.yaml`；`local` → `~/.aicli/projects/<slug>/mcp.yaml`（`aiclipaths.LocalMCPConfigPath`，
    slug 规则与 planstore 一致，退化为空时用路径哈希兜底）；`project` → `<cwd>/.aicli/mcp.yaml`。
  - `local` 同时被加入发现链最高优先级的候选（§4.5 的 local > project > user 语义）——只写不读会让
    `--scope local` 变成「写了不生效」，这一处是 e2e 里补上 local 用例时发现的真问题。
- 项目级秘密剥离：`scanSensitiveFindings` 检查 headers/env（key 命中 authorization/api-key/token/secret/password/cookie 等）
  与 `auth.clientSecret`；取值非空且不含 `${...}` 引用即视为明文 → **拒绝写入**并给出两条可行动路径
  （改 `${VAR}` 引用，或 `--scope user|local`）。选择「拒绝」而非自动改写：自动改写需要猜变量名，容易生成半成品。
- 写入目标语义修正：`enable`/`disable`/`remove` 通过 `locateMCPServerConfigFile` 定位「实际定义该 server 的文件」，
  避免分层后改错层级；`add` 时若同名已存在于其它层级，打印 `注意: ... 将在其之上覆盖` 而不静默覆盖。
- 安全边界：`--scope project` 只约束写盘内容，OAuth 令牌始终只落用户级 token store（M2），不进入项目文件。

**验证**

- 单测：`config`（合并/遮蔽/损坏容错/来源排序/显式覆盖）、`manager`（合并加载 + 状态来源 + 显式覆盖 + 告警）、
  `aiclipaths`（slug 归一化 / local 路径稳定性与哈希兜底）、`commands`（scope 路径矩阵、明文凭证矩阵、
  项目级写入端到端、定义文件定位、list/chat 来源渲染）。
- 真实二进制端到端：`go run ./scripts/mcp_scope_e2e.go` —— 隔离 HOME + 临时项目，
  覆盖「用户级基础 + 项目级覆盖 / 来源与 shadow 展示 / JSON 字段 / 项目级拒绝明文凭证并保留 `${VAR}` /
  删除项目级后用户级重新生效 / disable 写回定义文件」。

**与后续里程碑的边界**

- `project` 层当前为 `./.aicli/mcp.yaml`（沿用既有发现链的第 1 层）；`.mcp.json` 兼容名与仓库级 `.gitignore`
  豁免只做提示，不自动修改用户仓库——完整的外部格式互操作留给 M4 的 importers。
- `mcp get`（导出单 server 的可复制配置）与 `--scope` 的交互留给 M4 §4.7。

---

## 11. M4 实施备注（跨 agent 导入 + 单 server 导出，2026-09-25）

对齐 §4.6 / §4.7 的落地结果；§4.8（`/mcp` 交互菜单）另行说明。

**新增 `internal/mcp/importers` 包（纯解析，可单测）**

- `Import(vendor, Options{Root,Home,Names})` 返回按来源分组的 `Result`；来源：`claude` / `cursor` / `gemini` /
  `opencode` / `codex` / `all`。候选文件按「用户级 → 项目级」排序，后者覆盖前者并在告警里记录覆盖关系。
- JSON 解析兼容 `mcpServers`（Claude/Cursor/Gemini/OpenCode）与 `servers`（VS Code 风格）两种容器键，
  以及 `~/.claude.json` 的 `projects.<path>.mcpServers`；`env: ["K=V"]` 数组形态也接受。
- 映射规则集中在 `mapServerEntry`：`http/streamable*` → `streamable`、`sse` → `sse`、`ws/websocket` → `websocket`、
  `stdio/local` → `stdio`；缺 `type` 按 `url`/`command` 推断；`headers/http_headers` → `headers`；
  `timeout`/`timeout_sec`/`startup_timeout_ms`/`tool_timeout_sec` → 秒；`enabled`/`disabled` 各自保留语义；
  `oauth: true | {...}` / `scopes: [...]` → `auth`。
- **未知字段与不支持的构造只告警、不失败**：`knownEntryKeys` 之外记 `忽略未映射字段`；Codex TOML 的数组表
  与嵌套子表记 `不支持…（已跳过）`。单个来源文件解析失败只影响该文件（`ScannedFile.Err`），其余来源照常。
- `codex.go` 自带 TOML 子集解析器（仓库无 TOML 依赖）：表头（含引号键 `"odd.name"`）、基本/字面字符串、
  数字、布尔、跨行字符串数组、内联表 `env = { K = "v" }`；行尾注释与引号内的 `#` 都正确处理。

**CLI：`mcp import` / `mcp get` / `mcp add-json`（`cmd/aicli/commands/mcp_import.go`）**

- `mcp import`：`--from`、`--scope`（默认 `local`）、`--dry-run`、`--on-conflict skip|overwrite|rename`、
  `--on-secrets mask|reject|keep`、`--only`；文本摘要 + `--output json --envelope` 双形态。
- **dry-run 零副作用**：规划阶段改用只读的 `loadMCPTargetFile`（文件不存在返回空配置），不再调用会
  `EnsureFile` 的 `admin.LoadFile` —— 这一点是 e2e 断言「--dry-run 不创建文件」时发现的真问题。
- 冲突处理：`skip` 不动；`rename` 生成 `<name>-imported(-N)`；`overwrite` 走 `Service.Update`
  （`admin.Add` 对已存在名字直接报错，实测踩到过）。
- 秘密策略（仅 `--scope project` 生效，因为 user/local 是个人配置）：`mask`（默认）把明文改写为 `${VAR}`，
  变量名形如 `CODEX_REMOTE_X_API_KEY`，**相同明文复用同一个变量名**，摘要里列出需要设置的环境变量；
  `reject` 跳过该 server 并标记原因；`keep` 原样写入（兼容显式选择）。
- `mcp get <name> [--json]`：输出分层合并后**生效**的那份配置与 `configSource/configPath`；
  `--json` 的 `.config` 片段可直接喂回 `add-json`（e2e 里做了往返验证）。
- `mcp add-json <name> <JSON>`：接受 `type` 别名、`headers/http_headers`、`timeoutSeconds`、`maxParallelCalls`、
  `enabled/disabled`，以及 `get --json` 的 `.config` 包装；未声明 `type` 时按 `url`/`command` 推断
  （否则 admin 会把纯 url 请求当 stdio 并要求 command）。同样受 `--scope` 与项目级秘密剥离约束。

**验证**

- 单测：`importers`（Claude 映射与 projects 段、项目级覆盖用户级、Codex TOML 含跨行数组与不支持构造、
  非法来源/缺失文件、`all` 与 `--only` 过滤、坏文件隔离）；`commands`（dry-run 无副作用、local 层落盘并生效、
  project 层 mask/reject、冲突三策略、`--only` 未命中、秘密变量复用、add-json 解析与类型推断、
  项目级拒绝明文、`get --json` → `add-json` 往返）。
- 真实二进制端到端：`go run ./scripts/mcp_import_e2e.go` —— 隔离 HOME + 临时项目，覆盖五条断言链
  （dry-run 不落盘 / local 层生效并显示来源 / project 层 ${VAR} 化并提示变量名 / get→add-json 往返 /
  非法输入报错且不改文件）。

**§4.8 `/mcp` 交互菜单（已接入）**

复用 chat 的 lease-bound picker 基建（与 /model、/skills、/export 同一套
`AcquireAlternateScreen` + UI-actor 屏障语义）：

- 入口：bare `/mcp` 与 `/mcp select|pick|menu|choose` 请求选择器；`/mcp list` 及所有显式子命令保持纯文本。
- 两层列表：① server（标题行直接复用 `/mcp list` 的投影 —— 状态标记、transport、工具数、trust、
  分层来源与同名覆盖链；`SearchText` 为 server 名，可即时过滤）→ ② 动作
  （查看状态 / 启用|停用（按当前状态切换标签）/ 热重载全部 / 移除（二次确认） / 返回上一层）。
- 单一写路径：动作不新开写通道，统一转交 `/mcp <sub> <name>` 文本执行通道，
  因此 `--json`、项目级秘密剥离、热刷新（`refreshChatMCPTools`）等既有语义自动保持一致。
- 屏障语义：新增 `ui.OpenMCPPicker` / `ui.CloseMCPPicker`（`ClassBarrier`）与 `MCPPickerState`
  （只记租约所有权）；`LeaseReleased` 兜底清理状态。动作只在租约释放、主呈现器恢复之后执行。
- 降级：非交互 / 非 ANSI TTY / `--output json` / 有关键帧时不进选择器，`/mcp` 回到列表面板。
- 缺口（有意保留）：chat 侧暂无「认证」动作 —— OAuth 登录仍是 CLI（`aicli mcp auth login <name>`）
  与微型 Web 面板的职责，`chatMCPService` 未暴露 auth 能力；后续若把 auth 抽象进服务接口，可在动作列表里补一项。

验证：`cmd/aicli/ui` 的屏障生命周期与屏障类别断言（陈旧租约 Open 不生效、不匹配 Close 不清理、
`LeaseReleased` 兜底清理）；`cmd/aicli/commands` 的入口判定、无表面降级（含 `/mcp select` 不得报未知子命令）、
动作映射（启停标签随状态切换、移除需确认、取消项不带命令）、以及 server 行复用 list 投影。
真实 TTY 下的按键交互沿用既有 picker 基建，未新增终端读写路径。

---

## 11. M4 实施备注（跨 agent 导入 + 单 server 导出，2026-09-25）

对齐 §4.6 / §4.7 的落地结果；§4.8（`/mcp` 交互菜单）另行说明。

**新增 `internal/mcp/importers` 包（纯解析，可单测）**

- `Import(vendor, Options{Root,Home,Names})` 返回按来源分组的 `Result`；来源：`claude` / `cursor` / `gemini` /
  `opencode` / `codex` / `all`。候选文件按「用户级 → 项目级」排序，后者覆盖前者并在告警里记录覆盖关系。
- JSON 解析兼容 `mcpServers`（Claude/Cursor/Gemini/OpenCode）与 `servers`（VS Code 风格）两种容器键，
  以及 `~/.claude.json` 的 `projects.<path>.mcpServers`；`env: ["K=V"]` 数组形态也接受。
- 映射规则集中在 `mapServerEntry`：`http/streamable*` → `streamable`、`sse` → `sse`、`ws/websocket` → `websocket`、
  `stdio/local` → `stdio`；缺 `type` 按 `url`/`command` 推断；`headers/http_headers` → `headers`；
  `timeout`/`timeout_sec`/`startup_timeout_ms`/`tool_timeout_sec` → 秒；`enabled`/`disabled` 各自保留语义；
  `oauth: true | {...}` / `scopes: [...]` → `auth`。
- **未知字段与不支持的构造只告警、不失败**：`knownEntryKeys` 之外记 `忽略未映射字段`；Codex TOML 的数组表
  与嵌套子表记 `不支持…（已跳过）`。单个来源文件解析失败只影响该文件（`ScannedFile.Err`），其余来源照常。
- `codex.go` 自带 TOML 子集解析器（仓库无 TOML 依赖）：表头（含引号键 `"odd.name"`）、基本/字面字符串、
  数字、布尔、跨行字符串数组、内联表 `env = { K = "v" }`；行尾注释与引号内的 `#` 都正确处理。

**CLI：`mcp import` / `mcp get` / `mcp add-json`（`cmd/aicli/commands/mcp_import.go`）**

- `mcp import`：`--from`、`--scope`（默认 `local`）、`--dry-run`、`--on-conflict skip|overwrite|rename`、
  `--on-secrets mask|reject|keep`、`--only`；文本摘要 + `--output json --envelope` 双形态。
- **dry-run 零副作用**：规划阶段改用只读的 `loadMCPTargetFile`（文件不存在返回空配置），不再调用会
  `EnsureFile` 的 `admin.LoadFile` —— 这一点是 e2e 断言「--dry-run 不创建文件」时发现的真问题。
- 冲突处理：`skip` 不动；`rename` 生成 `<name>-imported(-N)`；`overwrite` 走 `Service.Update`
  （`admin.Add` 对已存在名字直接报错，实测踩到过）。
- 秘密策略（仅 `--scope project` 生效，因为 user/local 是个人配置）：`mask`（默认）把明文改写为 `${VAR}`，
  变量名形如 `CODEX_REMOTE_X_API_KEY`，**相同明文复用同一个变量名**，摘要里列出需要设置的环境变量；
  `reject` 跳过该 server 并标记原因；`keep` 原样写入（兼容显式选择）。
- `mcp get <name> [--json]`：输出分层合并后**生效**的那份配置与 `configSource/configPath`；
  `--json` 的 `.config` 片段可直接喂回 `add-json`（e2e 里做了往返验证）。
- `mcp add-json <name> <JSON>`：接受 `type` 别名、`headers/http_headers`、`timeoutSeconds`、`maxParallelCalls`、
  `enabled/disabled`，以及 `get --json` 的 `.config` 包装；未声明 `type` 时按 `url`/`command` 推断
  （否则 admin 会把纯 url 请求当 stdio 并要求 command）。同样受 `--scope` 与项目级秘密剥离约束。

**验证**

- 单测：`importers`（Claude 映射与 projects 段、项目级覆盖用户级、Codex TOML 含跨行数组与不支持构造、
  非法来源/缺失文件、`all` 与 `--only` 过滤、坏文件隔离）；`commands`（dry-run 无副作用、local 层落盘并生效、
  project 层 mask/reject、冲突三策略、`--only` 未命中、秘密变量复用、add-json 解析与类型推断、
  项目级拒绝明文、`get --json` → `add-json` 往返）。
- 真实二进制端到端：`go run ./scripts/mcp_import_e2e.go` —— 隔离 HOME + 临时项目，覆盖五条断言链
  （dry-run 不落盘 / local 层生效并显示来源 / project 层 ${VAR} 化并提示变量名 / get→add-json 往返 /
  非法输入报错且不改文件）。

**§4.8 `/mcp` 交互菜单的现状**

文本面板已具备状态标记、工具数、trust、来源/覆盖与「需认证」提示（M2/M3 已交付），
但**可交互选择器（动作：状态 / 认证 / 启停 / 移除）尚未实现**：`chat_*_picker.go` 一族正在被并行的
会话改造，此刻接入会与在制品冲突。下一步单独切片：先在 picker 稳定后接入，非 TTY 保持现有纯文本输出。
