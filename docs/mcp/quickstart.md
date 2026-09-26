# MCP 快速开始

面向第一次接入 MCP（Model Context Protocol）的用户：**先三步跑通，再按症状查命令**。
完整子命令、配置文件解析顺序与安全约定见 [docs/aicli/install.md](../aicli/install.md#mcp-服务器)；
本目录另有 [chrome-devtools.md](chrome-devtools.md) 这类具体 server 的深度教程。

---

## 1. 一分钟跑通

### 第 1 步：加一个 server（≈20 秒）

三种形态，挑一个（命令可在任意项目目录执行）：

```bash
# ① 远端 HTTP（streamable）——多数托管服务是这种形态
aicli mcp add context7 https://mcp.context7.com/mcp

# ② 远端 + OAuth——服务要求浏览器授权时
aicli mcp add notion https://mcp.notion.com/mcp --auth oauth --oauth-scope read

# ③ 本地 stdio——名称在 -- 之前，命令与其参数在 -- 之后（推荐写法）
aicli mcp add chrome-devtools -- npx -y chrome-devtools-mcp@latest
```

> 未显式指定 `--transport` 时按目标推断：`http(s)://` → `streamable`、`ws(s)://` → `websocket`、
> `--` 之后的本地命令 → `stdio`；推断结果会在添加输出里提示。
> flag 建议写在名称之前，本地命令写在 `--` 之后；`--command` 是兼容写法。

### 第 2 步：验证（≈15 秒）

```bash
aicli mcp list                          # 状态标记：● 已连接 / ◐ 已启用未连接 / ! 需认证 / ○ 已停用
aicli mcp test-server context7          # 真实建连并列出工具
aicli mcp test-server my-stdio --show-stderr   # stdio 起不来时：附子进程 stderr 尾部
aicli mcp tools context7                # 只看工具清单（含工具级启停状态）
```

`aicli mcp list` 每行会带 `来源: <层级> (<文件>)`；被更高优先级同名定义盖住时额外带
`覆盖: <层级> (<文件>)`，用来解释「我改了用户级为什么没生效」。

### 第 3 步：用起来

```bash
aicli mcp auth notion                   # ② 形态：浏览器完成授权（令牌存 ~/.aicli/mcp-tokens.json）
aicli chat                              # 会话里直接用自然语言描述任务，工具名形如 mcp__<server>__<tool>
```

chat 内可用的 `/mcp` 命令：

```text
/mcp                    交互菜单：选 server → 动作（查看状态 / 启用停用 / 移除 / 热重载）
/mcp select             同上（别名 pick / menu / choose）
/mcp list               纯文本列表（脚本口径，不开菜单）
/mcp status <name>      单 server 详情：配置、来源、最近错误与工具状态
/mcp auth               查看各 OAuth server 的授权状态
/mcp auth <name>        发起/继续授权（给出授权链接；浏览器回调已到达时直接完成）
/mcp auth <name> <回调URL|code>   回调页面打不开时，粘贴完成授权
/mcp auth <name> --clear          清除该 server 的令牌
/mcp enable|disable <name>   启停并热重载
/mcp remove <name>      删除并热重载
/mcp reload             重新加载配置并重连
/mcp help               会话内帮助
```

修改配置后不必重启：chat 内用 `/mcp reload`，命令行用 `aicli mcp reload`。
选择器里同样可以完成授权：选中 OAuth server → 「认证 / 重新认证 / 完成授权」（按当前状态变化），
需要清除令牌时选「清除授权」（二次确认）。

---

## 2. 命令参考表

> 全局通用：`-C/--config-file <路径>` 指定 MCP 配置文件；查询类子命令支持 `--output json`
> （简写 `-j`）与 `--envelope`（统一 `ok/command/data` 结构，适合脚本消费）。

| 子命令 | 用途 | 常用示例 |
|---|---|---|
| `add` | 新增 URL / stdio server | `aicli mcp add --transport sse legacy https://example.com/sse`<br>`aicli mcp add local-fs --command npx -y @modelcontextprotocol/server-filesystem /data` |
| `add-json` | 用一段 JSON 新增/更新（脚本、跨机复制） | `aicli mcp add-json team '{"url":"https://team.example.com/mcp"}' --scope project` |
| `get` | 查看/导出单个 server 的规范化配置（含来源） | `aicli mcp get context7 --json \| jq -c .config` |
| `import` | 从其它 agent 工具导入 | `aicli mcp import --dry-run`<br>`aicli mcp import --from codex --scope project` |
| `list` | 列出全部 server 与来源/覆盖 | `aicli mcp list --output json` |
| `status` | 单个 server 的运行状态 | `aicli mcp status context7` |
| `tools` | 列出某 server 的工具 | `aicli mcp tools chrome-devtools` |
| `test` | 真实调用一次工具（端到端验证） | `aicli mcp test chrome-devtools list_pages '{}'` |
| `test-server` | 只验证连接 | `aicli mcp test-server my-stdio --show-stderr` |
| `enable` / `disable` | 启停（写在**定义该 server 的文件**上） | `aicli mcp disable chrome-devtools` |
| `remove` | 删除定义并热重载 | `aicli mcp remove chrome-devtools` |
| `reload` | 重新加载配置并重连 | `aicli mcp reload` |
| `auth` / `logout` | OAuth 登录、状态、清理 | `aicli mcp auth --status`<br>`aicli mcp auth notion --no-browser`<br>`aicli mcp logout notion` |

写入层级 `--scope`（`add` / `add-json` / `import` 通用）：

| `--scope` | 落盘位置 | 用途 |
|---|---|---|
| `user`（默认） | `~/.aicli/mcp.yaml` | 个人全局 |
| `local` | `~/.aicli/projects/<项目标识>/mcp.yaml` | 项目私有，不进版本库 |
| `project` | `<项目>/.aicli/mcp.yaml` | 可提交共享；**拒绝明文凭证**，须写成 `${VAR}` |

`enable` / `disable` / `remove` 不接受 `--scope`：它们直接改「实际定义该 server 的那个文件」
（删掉项目级定义后，用户级同名定义会自动重新生效）。

---

## 3. 复制即用 recipes

四种形态覆盖绝大多数托管/本地 server；把 URL 或命令换成目标 server 的即可。

### ① 远端、无需凭证（Context7）

```bash
aicli mcp add context7 https://mcp.context7.com/mcp
aicli mcp test-server context7
```

### ② 远端、OAuth（Notion 等）

```bash
aicli mcp add notion https://mcp.notion.com/mcp --auth oauth --oauth-scope read
aicli mcp auth notion                  # 浏览器授权
aicli mcp auth notion --no-browser     # 无桌面 / Win7：打印链接，手动粘贴回调 URL 或 code
aicli mcp auth --status                # 查看各 server 授权状态（不打印令牌明文）
```

**在 chat 里同样可以授权**（不必切到终端）：`/mcp auth notion` 起流程并打印授权链接，
浏览器回调到达后再执行一次 `/mcp auth notion` 即完成；回调页面打不开时，把地址栏里的完整
回调 URL（或 code）粘贴回来：`/mcp auth notion <回调URL 或 code>`。
两边共用同一套 PKCE 流程与 `~/.aicli/mcp-tokens.json`，谁先完成都生效。

未登录或刷新失败时该 server 显示为「需认证」（`!`），不影响其它 server；
`401/403` 会用 refresh token 自动刷新并重试一次。`websocket` / `stdio` 暂不支持自动 OAuth，请用 `headers` 配置静态凭证。

### ③ 远端、静态令牌（header + 环境变量）

个人使用（默认 `user` 层，允许明文，但仍建议用变量）：

```bash
# PowerShell: $env:MY_TOKEN = "...";  Bash/Zsh: export MY_TOKEN=...
aicli mcp add my-remote https://mcp.example.com/mcp --header "Authorization: Bearer ${MY_TOKEN}"
```

团队共享（`project` 层会做秘密剥离：明文凭证直接拒绝）：

```bash
aicli mcp add team-remote https://team.example.com/mcp --scope project \
  --header "Authorization: Bearer ${TEAM_TOKEN}"
# 提示：若 .gitignore 忽略了 .aicli/，请追加豁免 !.aicli/mcp.yaml
```

配置文件里始终保留 `${VAR}` 字面量，展开只发生在运行时连接前；引用未设置时会隔离该 server
并在 `list/status` 的「最近错误」里指名变量。语法：`${NAME}` 严格、`${NAME:-default}` 带默认值、`$${NAME}` 表字面量。

### ④ 本地 stdio（可带 env / args）

```bash
# 浏览器自动化（Google 官方；连接已打开的 Chrome/Edge 见 chrome-devtools.md）
aicli mcp add chrome-devtools -- npx -y chrome-devtools-mcp@latest

# 文件系统访问，限定根目录由 --env 传入
aicli mcp add local-fs --command npx -y @modelcontextprotocol/server-filesystem --env FS_ROOT=/data
```

### ⑤ 从其它 agent 工具一键迁移

```bash
aicli mcp import --dry-run                       # 先预览：将导入 N、改名 M、跳过 K（不写任何文件）
aicli mcp import --from claude --scope user      # 从 ~/.claude.json / .mcp.json 导入到个人全局
aicli mcp import --from codex --scope project    # 从 ~/.codex/config.toml 的 [mcp_servers.*] 导入
aicli mcp import --on-conflict rename --only context7
```

`--from` 支持 `claude` / `cursor` / `gemini` / `opencode` / `codex` / `all`（默认）。
写 `--scope project` 时默认对凭证脱敏（`--on-secrets reject` 可直接拒绝，`keep` 保持原样）。

### ⑥ 直接写配置文件（等价于 add）

```yaml
# ./.aicli/mcp.yaml 或 ~/.aicli/mcp.yaml
mcpServers:
  context7:
    type: streamable
    url: https://mcp.context7.com/mcp
  local-fs:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
    env:
      FS_ROOT: /data
    enabled: false          # 先写配置，稍后 aicli mcp enable local-fs
```

写完用 `aicli mcp list` 复核来源，或 chat 内 `/mcp reload` 热重载。
同级多个文件同名时按 `configs/mcp.yaml` < `~/.aicli/mcp.yaml`（user）< `./.aicli/mcp.yaml`（project）
< `~/.aicli/projects/<项目>/mcp.yaml`（local）**整体覆盖**（不做字段级合并）。

---

## 4. 症状 → 命令（排错索引）

先跑左列命令拿到证据，再按「常见原因」处理。命令都在 `aicli mcp` 下。

| 症状 | 先跑 | 常见原因与处理 |
|---|---|---|
| 一直是 `◐ 已启用未连接` / 连接失败 | `aicli mcp status <名称>`<br>`aicli mcp test-server <名称> --show-stderr` | URL/传输类型不匹配（`/sse` 端点需 `--transport sse`）；本地 stdio 子进程启动失败（看 stderr 尾部）；网络/代理问题 |
| `! 需认证`（401/403） | `aicli mcp auth --status`<br>`aicli mcp auth <名称>`<br>chat 内 `/mcp auth <名称>` | 未授权：完成 OAuth（chat 内可分段完成：起流程 → 粘贴回调 URL/code）；自动 OAuth 不支持的 transport 用 `--header`/`--env` 配静态凭证；刷新失败会回到「需认证」 |
| 工具不出现 / 数量为 0 | `aicli mcp tools <名称>`<br>`aicli mcp test <名称> <工具> '{}'` | server 未连接（先看状态）；工具被**工具级启停**关闭（微型 Web / console 的工具弹窗，`tools` 的 `configured_enabled` 字段）；部分 server 延迟注册工具 |
| server 被隔离，提示「引用未设置的环境变量」 | `aicli mcp status <名称>`<br>`echo $env:MY_TOKEN`（PowerShell） | `${NAME}` 是严格语义：未设置即隔离该 server（不影响其它 server）。设置变量后 `aicli mcp reload`；或改用 `${NAME:-default}` |
| 改了配置却不生效 | `aicli mcp list`<br>`aicli mcp get <名称> --json` | 被更高优先级同名定义覆盖：看 `覆盖:` 行与 `configSource`；`enable/disable/remove` 作用在定义处，删项目级定义后用户级自动生效 |
| 配置写不进去（`--scope project` 报错） | `aicli mcp list` | 项目层做秘密剥离：把明文改成 `${VAR}` 或改用 `--scope user/local`；错误信息本身会给出这两条建议 |
| 会话里改了配置没立刻生效 | `/mcp status <名称>`（chat 内） | 管理动作会热重载；若直接改了配置文件，`/mcp reload` 或下一回合生效 |
| chat 里 `/mcp` 没有交互菜单 | `/mcp list` | 非 TTY / `--output json` / 有运行中关键帧时按设计降级为文本列表；用 `/mcp list|status` 或 CLI 完成同样操作 |
| Windows 下 `npx` 类 stdio 启动失败 | `aicli mcp test-server <名称> --show-stderr` | `.cmd`/`.bat` 包装器的启动问题；可尝试把命令写成 `cmd /c npx ...`，或改用包装脚本 |
| 想确认「到底加载了哪个文件」 | `aicli mcp list --output json \| jq '.[0]'`<br>`aicli mcp list --output json --envelope \| jq '.data[0]'` | 看 `configSource` / `configPath` / `shadowedSources`（不带 `--envelope` 时顶层就是 server 数组）；`--config-file` 指定的真实覆盖路径会关闭分层合并 |

---

## 5. 生效时机

| 动作 | 生效范围 |
|---|---|
| `aicli mcp enable/disable/remove/add/add-json/import` | 写入配置文件 + 热重载该 MCP 服务 |
| `aicli mcp reload` / chat `/mcp reload` | 重新读取分层配置并重连（配置文件被外部工具改动后用） |
| 直接编辑配置文件 | 下一次 `reload`，或新会话启动时 |
| chat 会话内工具面 | 管理动作会触发会话内刷新；进行中的回合可能仍持有旧快照，下一回合生效 |
| 完成 OAuth 授权（`/mcp auth <名称> <回调URL\|code>`） | 立即热重载并刷新工具面；无需手动 `/mcp reload` |
| runtime-server / 微型 Web / console | 三端与 CLI 共用同一份分层配置与管理服务，状态与来源展示一致（Web 侧见 [web-remote-api.md](../aicli/web-remote-api.md) 的 `/api/runtime/mcps`） |

---

## 6. 相关文档

- [docs/aicli/install.md](../aicli/install.md#mcp-服务器) — 完整子命令、`--scope` 与秘密剥离、OAuth、`mcp import|get|add-json` 细节
- [docs/user-guide/aicli.md](../user-guide/aicli.md) — 使用手册（MCP 章节：接入、分层、迁移、chat 内 `/mcp`）
- [docs/mcp/chrome-devtools.md](chrome-devtools.md) — 浏览器自动化 server 的深度示例（attach / launch / browser-url 三种模式 + 安全须知）
- [docs/mcp/mcp-tool-llm-integration.md](mcp-tool-llm-integration.md) — 工具如何以原生 `tools` 暴露给 LLM（命名规范、重名隔离、工具级启停）
- [docs/mcp/README.md](README.md) — 本目录索引
- [docs/analysis/commandcode-mcp-design-borrowing-20260925.md](../analysis/commandcode-mcp-design-borrowing-20260925.md) — 本套能力（分层 scope、OAuth、导入导出、`/mcp` 菜单）的设计来源与落地记录
