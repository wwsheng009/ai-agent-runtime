# aicli-mesh 网格运维 CLI

> 适用版本：内置多进程网格（`internal/mesh`）之后的 aicli 构建。
> 本文档是 `aicli-mesh` 的**权威说明**：网格方案 §7 是设计期草稿，落地后以本文为准。
> 工具默认**只读**——唯一的删除路径是 `gc --apply`，且**存活进程的档案永不删除**（规则 R3）。

## 1. 定位与前置

`aicli-mesh` 解决「一台机器上同时跑着多个 aicli 进程」时的三件事：

| 场景 | 命令 |
|------|------|
| 有哪些节点、谁活着、各自在哪个会话/工作区 | `ls` |
| 某个节点的端点、令牌提示、绑定、租约、日志尾部 | `show` |
| 拿一个可直接打开/调用的 URL（需要令牌时显式披露） | `url` |
| 让某个节点退场（默认投 `/exit` 等它收尾；`--force` 直接终止进程） | `stop`（治理动作，目标需 `--mesh-allow-stop=true`） |
| 复用活节点或拉起新节点，并给出可直接打开的窗口 URL | `open` |
| 新建一个会话（拉起新进程，返回新会话 ID 与窗口 URL） | `new` |
| 观察事件流（谁起停、谁切会话、谁调用了谁）——进程全退也能复盘 | `watch` |
| 清理已退出进程留下的档案 / 租约 / 日志 / 旧目录 | `gc`（默认 dry-run） |
| 一次性体检：目录、权限、陈旧节点、双占用、令牌可读性、日志完整性 | `doctor` |

两种等价的调用方式（**同一份实现**，参数、输出、退出码完全一致）：

- `aicli-mesh <子命令> ...` —— 独立二进制（`build.ps1 -Tools aicli-mesh` 产出，§11）；
- `aicli mesh <子命令> ...` —— `aicli` 的内置别名（§12），只部署单个二进制的场景也能用。

本文正文一律写 `aicli-mesh`；别名只是转发，把前缀换成 `aicli mesh` 同样成立。

前置条件与边界：

- 数据来源是**文件系统档案**（`nodes/` `bindings/` `leases/` `journal/`），不需要任何进程在线；
- `ls` 默认**不发网络请求**（毫秒级返回），只有 `--probe` 才做探活（§4.1）；
- 网格根目录解析顺序：`AICLI_MESH_DIR` → `AICLI_HOME/mesh` → `<用户主目录>/.aicli/mesh`；
  三者都解析不到时进入 **fail-closed** 状态：读取返回空视图、`ls`/`gc` 退出码 0（不是错误），
  `doctor` 报 1 个问题（退出码 5）。工具**不会**回退到当前目录（与 `internal/aiclipath` 的
  `./.aicli/...` 回退不同，见 `internal/mesh/paths.go` 顶部说明）；
- 令牌只在 `show` / `ls` 中以 `token_hint`（前 4 位 + `…`）出现，原文只有 `url --with-token`
  一条披露路径（§7）。

## 2. 用法总览

```text
aicli-mesh ls [-a|--all] [--json] [--probe] [--live] [--workspace PATH]... [--sort age|session|workspace]
aicli-mesh show <节点|会话> [--json] [--events N]
aicli-mesh url <节点|会话> [--with-token] [--path PATH] [--json]
aicli-mesh call <节点|会话> <op> [--args JSON] [--client-request-id ID] [--allow-write] [--timeout 130s] [--json]
aicli-mesh send <节点|会话> <prompt> [--allow-write] [--timeout 130s] [--client-request-id ID] [--json]
aicli-mesh screen <节点|会话> [--view tui|web] [--tail N] [--format json|text] [--timeout 130s] [--json]
aicli-mesh open <会话> [--port N] [--wait 8s] [--no-wait] [--takeover] [--bin PATH] [--json]
aicli-mesh new [--workspace PATH] [--port N] [--wait 8s] [--no-wait] [--bin PATH] [--json]
aicli-mesh stop <节点|会话> [--force] [--wait 30s] [--json]
aicli-mesh watch [--since 10m] [--node ID] [--session ID] [--once] [--limit N] [--interval 500ms] [--json] [--no-color]
aicli-mesh gc [--apply] [--stale-ttl 10m] [--keep-days 7] [--purge-legacy] [--prune-bindings] [--json]
aicli-mesh doctor [--json]
aicli-mesh version [--json]
```

`ls` 与 `ps` 等价（`ps` 是别名）。所有子命令都接受 `--help`（打印用法后退出码 0），
参数与位置参数可任意穿插（`show <目标> --json` 与 `show --json <目标>` 等价），
`--` 之后一律按位置参数处理。

以上命令都可用 `aicli mesh` 前缀调用（`aicli mesh watch --since 1h --once --json` ≡
`aicli-mesh watch --since 1h --once --json`，§12）——旗标与位置参数原样透传，不做二次解析。

## 3. 目标解析（`show` / `url`）

`<节点|会话>` 按固定优先级解析，**第一个命中的规则胜出**：

| 顺序 | 形式 | 例子 |
|------|------|------|
| 1 | `pid:<PID>` | `pid:8124` |
| 2 | 节点 ID 精确匹配 | `node-8124-20260924T073012Z` |
| 3 | 节点 ID 前缀 | `node-8124` |
| 4 | 会话 ID 精确匹配 | `session_20260924072950_ltYRU9tG` |
| 5 | 会话 ID 前缀 | `session_20260924072950` |

- 0 个匹配 → 退出码 2，错误信息含「目标不存在」；
- 多个匹配 → 退出码 2，**列出全部候选**（含 `pid:` 提示）并**不做猜测**；
- `show` 输出里同时给出 `pid:<PID>` 与会话 ID，便于脚本二次精确定位。

## 4. 子命令

### 4.1 `ls` — 节点列表

**默认只列在线（live）节点**——日常问的是「现在有哪些节点在跑」；要看全部档案
（含 `stale` / `stopped` / `unknown`）加 `-a` / `--all`。默认输出等宽表格（UTF-8，CJK 宽度按显示宽度对齐）：

```text
STATE  NODE                         PID    SESSION                          WORKSPACE                     ADDR                    AGE     OWN
-----  ---------------------------  -----  -------------------------------  ----------------------------  ----------------------  ------  ----
live   node-22024-20260924T013534Z  22024  session_20260924093535_4wCDwhqu  E:\projects\ai\ai-agent-run…  http://127.0.0.1:63910  8s      peer

共 2 个节点：live=1 stale=1 stopped=0 unknown=0 conflict=0（列出 1，排序 age）
已隐藏 1 个节点（默认只列在线；用 `aicli-mesh ls -a` 查看全部，counts 仍为全量口径）。
```

列含义：`STATE`（live / stale / stopped / unknown）、`NODE`、`PID`、`SESSION`、`WORKSPACE`、
`ADDR`（端点 base URL）、`AGE`（心跳年龄）、`OWN`（归属：`owner` 本节点 / `peer` 其它活节点 /
`conflict` 双占用 / `-` 无归属）。表格末尾是汇总行与根目录来源。

| 参数 | 说明 |
|------|------|
| `-a`, `--all` | 列出全部档案（`stale` / `stopped` / `unknown` 一并显示）；默认只列在线 |
| `--json` | 输出稳定 JSON（§6.1） |
| `--probe` | 对列出的节点发一次 HTTP 探活（并发 8、单请求 1s、整轮预算 3s；预算内没答复记 `skipped`，不算 `unreachable`） |
| `--live` | 显式请求默认行为（等价 `state=live` 过滤，保留兼容；与 `-a` 互斥） |
| `--workspace PATH` | 只列出该工作区的节点，可重复 |
| `--sort age\|session\|workspace` | 排序键，默认 `age`（最旧在前） |

**硬契约**：过滤（默认的「只看在线」、`-a`、`--workspace`）只裁剪 `nodes[]`，`counts` 与汇总行的
「共 N 个节点」恒为**全量口径**——过滤过的视图也不能假装网格更小，更不允许「过滤掉一半冲突」的假象（§5.4）。

### 4.2 `show` — 节点详情

```text
节点 node-22024-20260924T013534Z
  状态  live（心跳 23s 前）
  进程  pid 22024（dev，origin=cli）
  归属  peer
  端点  http://127.0.0.1:63910（web http://127.0.0.1:63910/web）（清单 http://127.0.0.1:63910/debug/endpoints）
  令牌  42ad…（required=false, mode=loopback-dev；原文用 `aicli-mesh url <目标> --with-token` 获取）
  会话  session_20260924093535_4wCDwhqu  state=running busy=false
  工作区  E:\projects\ai\ai-agent-runtime\backend（backend）
  绑定  127.0.0.1:63910 last_node=node-22024-20260924T013534Z 更新于 34m52s 前
  租约  session-session_20260924093535_4wCDwhqu owner=node-22024-... pid=22024 有效 C:\Users\vince\.aicli\mesh\leases\session-....lock
  记录  C:\Users\vince\.aicli\mesh\nodes\node-22024-20260924T013534Z.json
  日志尾部（最近 3 条）
    node.started
    session.activated
    lease.acquired
```

`--events N` 控制日志尾部条数（默认 10）。`--json` 返回 `{schema_version, node, leases[]}`
（§6.2），其中 `node` 与 `ls --json` 的 `nodes[]` 元素同构；`leases[]` 额外带
`owner_alive` / `expired`，让脚本不必自己判活。

### 4.3 `url` — 可直接使用的地址

| 参数 | 说明 |
|------|------|
| 默认 | 打印 `<web_base_url>`（不带令牌） |
| `--path PATH` | 覆盖路径，如 `--path /debug/endpoints`（相对路径自动补 `/`） |
| `--with-token` | 追加 `?token=<原文>`——**唯一**的令牌披露路径 |
| `--json` | 输出 `{schema_version, node_id, session_id, state, url, with_token, token_source}` |

目标没有端点（纯 TUI / 未开 HTTP 服务）时退出码 3（不可达），而不是「不存在」。
`--with-token` 打印的 URL 含写令牌，**不要**粘贴到日志、聊天记录或任何会被转发的地方。

### 4.4 `gc` — 清理（默认 dry-run）

不加 `--apply` 时只打印计划，一个字节都不动；`--apply` 才真正删除。
**计划在两种模式下由同一段代码生成**，因此 dry-run 与 `--apply` 的动作列表逐项一致（§12.1）。

| 动作种类 | 触发条件 |
|----------|----------|
| `node-record` | 进程已退出（`pid` 判定）**且**心跳超过 `--stale-ttl` 的陈旧档案；**存活进程永不删**（R3）。记录文件不可读 / schema 未知（无路径）时一律跳过 |
| `lease` | ①租约已过期；②持有者进程已退出且续租时间超过 `--stale-ttl` |
| `journal` | 文件 mtime 超过 `--keep-days`；对应节点档案已不存在的孤儿日志用同一窗口 |
| `binding` | 仅在 `--prune-bindings` 时参与：没有任何节点档案引用该会话，且文件超过 `--keep-days` |
| `legacy-dir` | 仅在 `--purge-legacy` 时参与：`$AICLI_HOME/web-ports`（或主目录下同名目录）存在时整棵删除（M8，会话绑定已取代端口档案） |

| 参数 | 默认 | 说明 |
|------|------|------|
| `--apply` | 关 | 真正执行删除 |
| `--stale-ttl 10m` | `10m` | 心跳/续租的陈旧阈值（最小 1s） |
| `--keep-days 7` | `7` | 日志/绑定的保留天数 |
| `--purge-legacy` | 关 | 清理旧 `web-ports/` 目录 |
| `--prune-bindings` | 关 | 清理无人引用的会话绑定 |
| `--json` | 关 | 输出 `{schema_version, dry_run, root, generated_at, stale_ttl, keep_days, purge_legacy, prune_bindings, actions[], action_count, reclaim_bytes, applied, deleted, errors[]}` |

删除失败（占用、权限）不会中断整轮：失败项进入 `errors[]`，`--apply` 结束时退出码 5。
`actions[]` 的每一项都带 `kind` / `path` / `reason`（人类可读的删除理由）。

### 4.5 `doctor` — 体检

检查项（`--json` 的 `checks[].id`）：

| id | 检查内容 | 级别 |
|----|----------|------|
| `paths` | 根目录可解析性与来源（env / home / user-home） | ok / problem |
| `permissions` | Windows 下保密性依赖用户 Profile 权限（无 POSIX 权限位，§9.6）；`AICLI_MESH_DIR` 指向 Profile 之外时告警 | ok / warn |
| `nodes` | 节点档案是否都可读（含 schema 版本） | ok / problem |
| `stale-nodes` | 已退出进程留下的档案 | warn |
| `ownership` | 同一会话是否被两个存活节点占用（§4.2） | ok / problem |
| `leases` | 租约持有者是否已退出 | warn |
| `tokens` | 声明需要令牌的节点，令牌文件是否可读 | ok / problem |
| `journal` | 日志行是否完整（截断行计数） | ok / problem |
| `journal-orphans` | 没有节点档案的孤儿日志 | warn |
| `legacy` | 旧 `web-ports/` 目录残留与新鲜度（§2.4） | warn |
| `spawn-executable` | 拉起节点会用哪个 aicli 二进制、由哪条规则选中（`AICLI_BIN` / `self` / `sibling` / `PATH`，§4.10） | ok / warn / problem |

`problems > 0` 时退出码 5（**问题**=需要人工处理；**告警**=可用 `gc` 自愈，不影响退出码）。
人类可读输出用 `[ok]` / `[告警]` / `[问题]` 前缀区分。
其中 `spawn-executable` 的 **problem** 专指 `AICLI_BIN` 指错（显式配置错误）；一台机器上
没有 aicli 二进制只是 **warn**（只读用法不受影响）。

### 4.6 `version`

```json
{
  "schema_version": 2,
  "name": "aicli-mesh",
  "version": "dev",
  "go_version": "go1.25.0",
  "os": "windows",
  "arch": "amd64",
  "mesh_root": "C:\\Users\\vince\\.aicli\\mesh",
  "mesh_root_source": "user-home",
  "record_schema_version": 2
}
```

`record_schema_version` 是**档案结构版本**（当前 2），与 `--json` 文档的 `schema_version` 分开演进：
前者描述磁盘上的节点/租约档案，后者描述本工具的输出契约。

### 4.7 `call` — 跨进程调用（op 白名单）

```text
aicli-mesh call <节点|会话> <op> [--args JSON] [--client-request-id ID] [--allow-write] [--timeout 130s] [--json]
```

把 `<op>` 连同 `--args` 发给目标进程的 `POST /web/api/mesh/call`（§5.6）。op 白名单
**固定 9 项**，不存在「任意端点转发」：

| 类别 | op | 目标端点 | 说明 |
|------|----|----------|------|
| 只读 | `node.info` | `/web/api/mesh/self` | 目标自述（默认脱敏） |
| 只读 | `status` | `/web/api/status` | 渲染器/显示状态快照 |
| 只读 | `screen` | `/web/api/screen` | 屏幕/transcript 快照（`--args '{"view":"tui","tail":40}'`） |
| 只读 | `turn` | `/web/api/turn` | turn 后验（`--args '{"id":"turn_..."}'`） |
| 只读 | `sessions.list` | `/web/api/sessions` | 会话列表 |
| 写 | `invoke` | `/web/api/invoke` | 注入 prompt 并等待 turn 结束 |
| 写 | `input` | `/web/api/input` | 异步注入 / 审批 / 提问回答 |
| 写 | `cancel` | `/web/api/input` | 折成 `{"type":"interrupt"}`（可带 `discard_pending`） |
| 写 | `sessions.resume` | `/web/api/sessions/resume` | 切到指定会话（`--args '{"session_id":"..."}'`） |

**硬规则**

1. **写操作必须显式 `--allow-write`**：没有它时 `invoke` / `input` / `cancel` /
   `sessions.resume` 一律在本地拒绝（退出码 6，`mesh_write_not_allowed`），**不发请求**；
2. `--args` 必须是**合法 JSON 对象**（按 op 白名单逐项搬运，未列出的键被丢弃）；
3. 幂等：`--client-request-id` 透传给目标端点的幂等键（仅 `invoke` 使用），
   **网格层不重复实现幂等**——重复提交同 id 时目标回放首次结果（`duplicate: true`）；
4. `--timeout` 是**等待上限**（默认 130s），超时按 `timeout` 记账而不是「目标挂了」；
5. 目标必须**开着回环控制面**（`--pprof` / `--web-port`）：纯 TUI 节点没有端点，
   返回 `not_found` + `mesh_no_endpoint`；
6. **跨工作区默认放行**（工作区是筛选维度、不是权限边界）：只有目标进程以
   `--mesh-restrict-workspace` 启动时，跨工作区的**写**调用才被拒（`refused` +
   `mesh_cross_workspace_denied`）；只读调用不受该开关影响。

### 4.8 `send` — 给目标发一轮 prompt

```text
aicli-mesh send <节点|会话> <prompt> [--allow-write] [--timeout 130s] [--client-request-id ID] [--json]
```

`call <目标> invoke --allow-write` 的固定写法：注入 prompt 并**等待该 turn 结束**，
人读输出直接打印助手正文（`assistant.content`），拿不到正文时回退打印 pretty JSON。
prompt 支持多词（`parsed.pos[1:]` 以空格拼接）。

```powershell
# A 让 B 跑一轮（B 需要 --pprof 与写令牌；令牌由 CLI 自动从档案读取）
aicli-mesh send session_20260924093535 "只回复两个字：收到" --allow-write
# → 收到
```

### 4.9 `screen` — 读目标屏幕

```text
aicli-mesh screen <节点|会话> [--view tui|web] [--tail N] [--format json|text] [--json]
```

只读（无需 `--allow-write`）。默认 `view=tui` + `format=json`（终端视口真实合成帧），
人读输出打印快照里的 `text` 段；`--tail N` 只取末尾 N 行。

### 4.10 `open` — 复用或拉起节点，拿窗口 URL

```text
aicli-mesh open <会话> [--port N] [--wait 8s] [--no-wait] [--takeover] [--bin PATH] [--json]
```

与 Web 端 `POST /web/api/mesh/spawn` 共用同一套实现（`mesh.Spawn`）。CLI 本身**不是节点**：
单飞租约的 owner 记作 `cli-<pid>`，不冒充节点身份。
会话还不存在、要**新建**一个时用 `new`（§4.11）：同一套 `mesh.Spawn` 的另一个入口。

| 开关 | 作用 |
|------|------|
| `--port N` | 首选回环端口（1-65535）；缺省先用会话绑定里的端口，再随机挑一个空闲端口 |
| `--wait 8s` | 就绪等待预算（默认 8s） |
| `--no-wait` | 只报告「已启动」，不等节点就绪 |
| `--takeover` | 跳过复用，显式回收该会话的租约（§4.4）：旧节点继续运行，下次心跳后把自己标 `orphaned`（**不会被杀**） |
| `--bin PATH` | 指定要拉起的 aicli 二进制；必须存在，且**优先于 `AICLI_BIN`** |

四态与退出码：`reused` / `started` → 0，`not_running` → 3，`failed` → 5（§5）。

**可执行文件解析顺序**（`doctor` 的 `spawn-executable` 打印实际结果与来源）：

| 顺序 | 规则 | 来源标签 |
|------|------|----------|
| 1 | `AICLI_BIN` 指向的文件（存在且不是目录） | `AICLI_BIN` |
| 2 | 当前进程自己（仅当文件名就叫 `aicli`：大小写不敏感、去掉扩展名） | `self` |
| 3 | 当前进程**同目录**下的 `aicli<扩展名>`（即 `aicli.exe`） | `sibling` |
| 4 | `PATH` 里的 `aicli` | `PATH` |
| — | 都没有 → `failed`（`mesh_spawn_bin_unavailable`） | — |

两点容易踩：

- **改名部署**：`aicli-2x.exe` 既不是 `self`（名字不叫 aicli），也不会把自己的名字当兄弟名
  ——兄弟名**硬编码**为 `aicli.exe`。要么设 `AICLI_BIN`，要么 `open --bin <路径>`，要么把
  `aicli.exe` 放在旁边。两者都**建议绝对路径**：相对路径按调用方 cwd 检查，而子进程是在
  会话工作区里启动的。
- **指错就报错**：`AICLI_BIN` 不可用（不存在 / 是目录）时**不退回** `self`/`sibling`/`PATH`，
  而是直接 `failed`（`mesh_spawn_bin_unavailable`）——「我指定了哪个二进制」不该变成猜谜，
  悄悄拉起另一个版本比报错更难排查。`--bin` 同规则：指错按用法错误（退出码 1）当场拒绝。

### 4.11 `new` — 新建会话（拉起一个新的 aicli 进程）

```text
aicli-mesh new [--workspace PATH] [--port N] [--wait 8s] [--no-wait] [--bin PATH] [--json]
```

与 `open`（§4.10）共用 `mesh.Spawn`，区别只在**会话从哪来**：`open` 处理既有会话（复用活节点，
或把进程拉到该会话上），`new` 让子进程自己生成新会话 ID（跑的是**不带** `--session` 的
`aicli chat`）。由此带来三条刻意的差异：

| 方面 | `open <会话>` | `new` |
|------|----------------|-------|
| 会话来源 | 调用方给定的会话 ID | 子进程生成，**只有返回值里有** |
| 复用 | 已有活节点直接 `reused` | **从不复用**：每次调用都该是一个新会话 |
| 单飞租约 | 复用/回收该会话的 `spawn-<会话>.lock`（§4.4） | **不涉及**：会话还不存在，没有键可抢——CLI 依旧不写档案、不写绑定、不占租约 |
| 工作区 | 从会话绑定/档案推导 | 调用方给出：`--workspace PATH`，缺省 = 当前目录 |

`--workspace` 是唯一新增开关（必须已存在且是目录，否则按用法错误退出码 1 当场拒绝）；
`--port` / `--wait` / `--no-wait` / `--bin` 与 §4.10 逐字同义，**可执行文件解析顺序**、
`AICLI_BIN` 覆盖规则、`mesh_spawn_bin_unavailable` 也完全一样（不重复列出）。`new` 不接受位置参数。

就绪判定靠 **pid**（没有会话 ID 可查）：等「新出现的、属于该 pid 的 live 档案」，且档案里的
`session_id` 非空才算数——子进程启动时先写档案、会话建好后再补写一次，等的就是第二笔；
随后从档案读回 `session_id` / `node_id` / `port`，拼出带令牌的窗口 URL。启动日志以
`new-<UTC 时间戳>-<pid>` 为键，两次 `new` 不会共用一份日志。

状态与退出码：`started` → 0，`not_running`（进程起了但迟迟没登记档案）→ 3，`failed` → 5；
四态里的 `reused` **不会出现**——那与「从不复用」矛盾。`--no-wait` 不谎报：会话 ID 尚未生成，
`session_id` 就留空，只报 pid、端口与不带 `session=` 的令牌 URL，`reason` 指向 `aicli-mesh ls` 自查。

失败面（`code` / `reason` 与 `open` 共用 §5.7 的 `SpawnResult`）：

| 情形 | `code` | 退出码 |
|------|--------|--------|
| `--workspace` 不存在 / 是文件 / 无法解析（CLI 当场拒绝） | —（用法错误） | 1 |
| 工作区目录在启动前被删（CLI 查过之后、子进程启动之前） | `mesh_workspace_missing` | 5 |
| 网格根目录不可用（fail-closed，§1） | `mesh_disabled` | 5 |
| 找不到可拉起的 aicli 二进制（`--bin` / `AICLI_BIN` 指错） | `mesh_spawn_bin_unavailable` | 5 |
| 进程起了但等不到会话档案（超时；带脱敏 `log_tail`） | `mesh_spawn_timeout` | 3 |

`--workspace` 为空且取不到当前目录时同样退出码 5（CLI 直接报错，不带 `code`）。

### 4.12 `stop` — 停止节点（治理动作，默认关闭）

```text
aicli-mesh stop <节点|会话> [--force] [--wait 30s] [--json]
```

两种模式（§5.7）：

| 模式 | 做法 | 收尾 |
|------|------|------|
| 默认（graceful） | 把 `/exit` 投给目标的 `/web/api/input`，等进程自己消失 | 目标自己保存会话、注销档案、释放租约 |
| `--force` | 直接终止进程 | **无**——残留档案由 `gc` 按「可证已死」回收 |

- **开关在被停的进程上**：目标必须显式 `--mesh-allow-stop=true`（默认**关闭**），否则一律
  `refused` + `mesh_stop_not_allowed`（退出码 6）。CLI 无法绕过——「谁能停我」由被停者决定，
  而不是由一台可能被入侵的调用方机器决定；
- **不自杀**：目标就是本进程（或调用方自己）→ `refused` + `mesh_stop_self_refused`；
  停自己用 `/exit`（会话内命令），不是网格调用；
- **幂等**：目标已不在运行（档案 `stopped`、pid 已消失）→ `status=stopped` +
  `code=mesh_stop_already_stopped`，退出码 0，重试安全；
- **等待预算**：`--wait` 默认 30s；graceful 投递成功但进程在预算内没消失 → `timeout`
  （退出码 3），提示加大 `--wait` 或改用 `--force`；
- **没有回环控制面**（目标没带 `--pprof`）时 graceful 不可用（`mesh_no_endpoint`），
  用 `--force` 直接终止进程。

```bash
# 优雅停掉某个会话所在的节点（先看它是不是真的该退）
aicli-mesh stop session_20260924093535

# 目标卡死 / 没有 --pprof：直接终止进程
aicli-mesh stop node-22024 --force
```

### 4.13 `watch` — 事件流（journal tail，不依赖节点存活）

```text
aicli-mesh watch [--since 10m] [--node ID] [--session ID] [--once] [--limit N] [--interval 500ms] [--json] [--no-color]
```

直接 tail `mesh/journal/*.ndjson`（每个进程只写自己那一个文件，§3.4），**不需要任何节点在线**：
进程全退之后仍然能复盘——这是它与 `GET /web/api/mesh/events`（SSE 扇入，需要双方都活着）的分工。

两种模式：

| 模式 | 行为 | 适用 |
|------|------|------|
| 默认（回放 + 尾随） | 先按 `--since` 回放窗口，再每 `--interval` 轮询一次新行，直到 Ctrl-C（退出码 0） | 人盯着看 / 边跑边看 |
| `--once` | 只回放窗口后退出 | 脚本、Agent、CI（不会挂住） |

- **窗口**：`--since` 默认 `10m`；`--since 0` 表示**磁盘上的全部**（含轮转代 `<node>.ndjson.1`）；
  `--limit N` 在回放时只保留**最新 N 条**（尾随阶段不截断）；
- **过滤**：`--node` / `--session` 接受 ID 或**前缀**（大小写不敏感），两者同时给出取交集。
  指定的目标在磁盘上**完全不存在** → 退出码 2；目标存在但窗口内没有事件 → 退出码 0（正常空结果，
  stderr 提示「窗口内没有事件；--since 0 可回放全部」）；
- **不消费半行**：writer 正在追加的那一行（还没有换行符）留到下一次轮询——否则那条事件会永久丢失。
  轮转（`<node>.ndjson` → `.1`）或截断会按新文件重新开始读；
- **去重**：按 `(node_id, seq)` 去重（seq 在**一个进程**内单调；进程重启 = 新 node_id），
  所以「轮转代 + 活动文件」一起回放不会出现重复行；
- **合并顺序**：回放按时间戳合并全部节点（同刻按 node_id、seq 稳定排序），人读输出每行带 `[节点 ID]`
  前缀；
- `--no-color` 是兼容开关：watch 的人读输出本来就不带颜色（与 `ls`/`gc` 的纯文本表格同一口径）；
- `--interval` 最小 `50ms`（避免忙等）；默认 `500ms`，与架构 §6.6 的 tail 轮询区间一致。

```bash
# 最近 5 分钟谁在动（回放后继续尾随，Ctrl-C 结束）
aicli-mesh watch --since 5m

# 只看某个节点的历史 + 实时
aicli-mesh watch --node node-22024 --since 0

# 只回放某个会话的事件（脚本友好：跑完就退）
aicli-mesh watch --session session_20260924093535 --once --json
```

## 5. 退出码契约

| 码 | 含义 | 触发示例 |
|----|------|----------|
| 0 | 成功 | 任何命令正常完成（空网格也算成功） |
| 1 | 用法/参数错误 | 未知子命令、未知参数、`--sort` 取值非法、缺少目标、`gc --stale-ttl` 非时长；`watch` 的位置参数、非法 `--since` / `--limit` / `--interval`（< 50ms） |
| 2 | 目标不存在或无法唯一确定 | `show nope`、`show node-100`（歧义，候选会列出）；`call` 的 `not_found`（含 `mesh_no_endpoint` / `mesh_target_stopped`）；`stop` 的 `not_found`；`watch --node/--session` 指定的目标在 journal 里完全不存在 |
| 3 | 目标不可达 | `url` 的目标没有端点（纯 TUI 节点）；`call` 的 `unreachable` / `timeout`；`stop` 的 `timeout`（进程在 `--wait` 内没消失） |
| 4 | 冲突或忙碌 | `call` 的 `busy`（目标已有 invoke 在等，HTTP 409） |
| 5 | 操作失败 | `gc --apply` 有删除失败项、`doctor` 发现问题、JSON 输出失败、`call` 的 `error`（含未知 op）；`watch` 读 journal / 写 stdout 失败、网格根目录不可用 |
| 6 | 被策略拒绝 | `call` 的 `refused`：写操作缺 `--allow-write`（`mesh_write_not_allowed`）、令牌失效（`mesh_token_stale`）、跨工作区收敛拒绝（`mesh_cross_workspace_denied`）；`stop` 的 `refused`（`mesh_stop_not_allowed` / `mesh_stop_self_refused` / `mesh_nonloopback_denied`） |

错误信息写 **stderr**，数据写 **stdout**——`aicli-mesh ls --json > x.json` 不会混入任何提示文本。
`call` 失败时 stderr 形如 `调用失败: refused（mesh_write_not_allowed）: …`，便于脚本 grep 原因码。

## 6. JSON 契约

三条稳定约定：

1. 每个文档都带 `schema_version`（当前 2）；
2. 数组字段**恒为数组**，不会出现 `null`（空网格是 `"nodes": []`）；
3. 令牌原文**从不出现**，只有 `auth.token_hint`（`url --with-token` 的 URL 除外）。

### 6.1 `ls --json`

```json
{
  "schema_version": 2,
  "generated_at": "2026-09-24T02:10:05Z",
  "root": "C:\\Users\\vince\\.aicli\\mesh",
  "counts": { "live": 1, "stale": 9, "unknown": 0, "conflict": 0 },
  "filter": { "scope": "all", "workspace": null, "state": "live" },
  "nodes": [
    {
      "node_id": "node-22024-20260924T013534Z",
      "pid": 22024,
      "kind": "chat",
      "state": "live",
      "reachability": "skipped",
      "endpoint": { "scheme": "http", "host": "127.0.0.1", "port": 63910, "loopback": true,
                    "base_url": "http://127.0.0.1:63910", "web_base_url": "http://127.0.0.1:63910/web",
                    "manifest_url": "http://127.0.0.1:63910/debug/endpoints" },
      "auth": { "mode": "loopback-dev", "required": false, "token_hint": "42ad…", "token_source": "random" },
      "session": { "id": "session_...", "title": "...", "state": "running", "busy": false },
      "workspace": { "path": "E:\\projects\\ai\\ai-agent-runtime\\backend", "name": "backend" },
      "ownership": "peer",
      "heartbeat_at": "2026-09-24T02:10:04Z",
      "age_sec": 8,
      "journal_tail": ["node.started", "session.activated", "lease.acquired"],
      "path": "C:\\Users\\vince\\.aicli\\mesh\\nodes\\node-22024-20260924T013534Z.json"
    }
  ],
  "workspaces": [ { "path": "E:\\projects\\ai\\ai-agent-runtime\\backend", "name": "backend", "nodes": 1 } ]
}
```

- `filter` 只在有过滤条件时出现：默认（只看在线）与 `--live` 回显 `"state": "live"`；`-a/--all`
  取消状态过滤，此时若也没给 `--workspace`，字段整体省略；`workspace: null` 表示「未按工作区过滤」（§5.4）。
- `counts` 恒为全量口径（§4.1 硬契约）；`reachability` 只有 `--probe` 时才可能是 `ok` / `unreachable`。
- 节点档案不可读或 schema 未知时，元素仍在 `nodes[]` 里（`state: "unknown"`）并带 `error` 字段——
  宁可展示「有个看不懂的档案」，也不静默丢弃。

### 6.2 `show --json`

```json
{ "schema_version": 2, "node": { ...与 6.1 元素同构... },
  "leases": [ { "purpose": "session", "key": "session_...", "owner_node_id": "node-...",
                "owner_pid": 22024, "expires_at": "2026-09-24T02:11:04Z", "ttl_sec": 60,
                "owner_alive": true, "expired": false, "path": "C:\\...\\leases\\session-....lock" } ] }
```

### 6.3 `doctor --json`

```json
{ "schema_version": 2, "root": "C:\\Users\\vince\\.aicli\\mesh", "source": "user-home",
  "now": "2026-09-24T02:10:13Z",
  "checks": [ { "id": "stale-nodes", "status": "warn", "detail": "9 个节点已退出但档案仍在（`aicli-mesh gc` 可清理）",
                "items": ["node-22268-...（stale，pid 22268）"] } ],
  "problems": 0, "warnings": 4 }
```

`status` ∈ `ok` / `warn` / `problem`；`items` 只在需要列举证据时出现。

### 6.4 `call` / `send` / `screen --json`

三者共用同一份调用结果文档（`schema_version` + 调用方结果视图）：

```json
{
  "schema_version": 2,
  "status": "ok",
  "node_id": "node-22024-20260924T073012Z",
  "op": "node.info",
  "elapsed_ms": 12,
  "http_status": 200,
  "attempts": 1,
  "result": { "node_id": "node-22024-20260924T073012Z", "state": "live", "...": "被调方端点的原始响应" }
}
```

- `status` ∈ `ok` / `busy` / `refused` / `not_found` / `timeout` / `unreachable` / `error`（与
  `/web/api/mesh/call` 同一套词汇，§5.6）；
- `code` 只在能给出机器可判原因时出现（`mesh_*` 前缀，见 §5 退出码表）；
- `result` 是**被调方端点的原始响应**（网格层不重写语义）；非 JSON 体折成 JSON 字符串；
- `attempts` > 1 只在「目标 401 → 重读档案重试一次」时出现；
- **`result` 里不会有令牌原文**：`node.info` 等端点默认脱敏（M7）。

### 6.5 `open` / `new` `--json`

返回 §5.7 的 `SpawnResult` 信封（与 `POST /web/api/mesh/spawn` 同一份文档）：

```json
{
  "schema_version": 2,
  "status": "started",
  "session_id": "session_20260924093535",
  "node_id": "node-22024-20260924T073012Z",
  "pid": 22024,
  "port": 55130,
  "url": "http://127.0.0.1:55130/web?token=…&session=session_20260924093535",
  "lease": "acquired",
  "origin": "cli",
  "elapsed_ms": 812
}
```

- `status` ∈ `reused` / `started` / `not_running` / `failed`（§4.10 的退出码映射）；
- 失败时带 `code`（如 `mesh_spawn_bin_unavailable`）与 `reason`；`not_running` / `failed`
  还会带脱敏后的 `log_tail`（末尾 20 行）；
- `url` 是**唯一**携带令牌原文的字段（M7：它直接交给浏览器自举）。

`new --json` 是同一形状外加 `workspace`（会话 ID 是子进程生成的、工作区是调用方给的，
脚本两个都要）：

```json
{
  "schema_version": 2,
  "workspace": "E:\\work\\proj-a",
  "status": "started",
  "session_id": "session_20260924222328_aGz9TW3R",
  "node_id": "node-25340-20260924T142328Z",
  "pid": 25340,
  "port": 64959,
  "url": "http://127.0.0.1:64959/web?token=…&session=session_20260924222328_aGz9TW3R",
  "origin": "cli",
  "elapsed_ms": 997
}
```

- `status` 只有 `started` / `not_running` / `failed`——`reused` 不会出现（§4.11）；
- `--no-wait` 时 `session_id` 为空、`url` 不带 `session=`（会话还没生成，如实留空）；
- `lease` 也不会出现：新会话没有可抢的租约（§4.11）。

### 6.6 `stop --json`

返回 §5.7 的 `StopResult` 信封（与 `POST /web/api/mesh/stop` 同一份文档）：

```json
{
  "schema_version": 2,
  "status": "stopped",
  "node_id": "node-22024-20260924T073012Z",
  "pid": 22024,
  "mode": "graceful",
  "graceful": true,
  "elapsed_ms": 1382
}
```

- `status` ∈ `stopped` / `not_found` / `refused` / `timeout` / `error`（§4.12 的退出码映射）；
- 幂等成功带 `code=mesh_stop_already_stopped` 与 `message`；失败带 `code` 与 `message`；
- `graceful=true` 只说明这次**投递**了 `/exit`，不代表对方已完成收尾——进程消失才是判据。

### 6.7 `watch --json`

回放模式（`--once`）给稳定信封（§7.3），`events` 里的每一项与 journal 文件**同构**
（`ts` / `node_id` / `seq` / `kind` / `session_id` / `detail`）：

```json
{
  "schema_version": 2,
  "events": [
    {
      "ts": "2026-09-24T09:41:12Z",
      "node_id": "node-22024-20260924T073012Z",
      "seq": 7,
      "kind": "session.activated",
      "session_id": "session_20260924093535",
      "detail": {"port": 55124}
    }
  ],
  "counts": {"events": 1, "nodes": 1}
}
```

- **实时模式（默认）不是信封，而是逐行 NDJSON**：每行一个 `JournalEntry`，来一条写一条——
  流式输出没有「收尾」，强行套信封只会把事件全缓在内存里。这是 `--json` 唯一一处
  与 §6 三条约定不同的地方（脚本请按行 `ConvertFrom-Json`）；
- `counts.nodes` 是回放窗口里出现过的节点数（`events` 为条数）；
- 窗口内为空时是 `"events": []`（不是 `null`），退出码 0。

## 7. 令牌与安全（M7）

- `ls` / `show` 只输出 `token_hint`（前 4 位 + `…`），**任何** `--json` 都不会带原文；
- `url --with-token` 是唯一的原文披露路径，且只在**本机回环**场景有意义；
- 网格目录（`nodes/` 等）在 Windows 上没有 POSIX 权限位，保密性依赖用户 Profile 权限，
  `doctor` 的 `permissions` 检查会就此给出提示（§9.6）；
- 工具不做任何网络写入：唯一的网络行为是 `ls --probe` 的 GET 探活。

## 8. 环境变量

| 变量 | 作用 |
|------|------|
| `AICLI_MESH_DIR` | 直接指定网格根目录（测试、多套网格隔离）；优先级最高 |
| `AICLI_HOME` | 共享 AICLI 主目录；网格根为 `$AICLI_HOME/mesh`，`gc --purge-legacy` 也在该目录下找 `web-ports/` |
| `AICLI_BIN` | 指定 `open` / `new` / Web 端拉起的 aicli 二进制（**建议绝对路径**）；指错时拉起直接失败（§4.10）。子节点原样继承该变量 |

## 9. 常见用法

```powershell
# 一眼看清在线节点（毫秒级，不发请求；默认只列 live）
aicli-mesh ls

# 全部档案（含已退出的 stale/stopped/unknown），排查残留与冲突
aicli-mesh ls -a

# 确认在线节点端口真的通
aicli-mesh ls --probe

# 脚本消费：活节点数
(aicli-mesh ls --json | ConvertFrom-Json).counts.live

# 从会话 ID 前缀拿到可打开的地址（不带令牌）
aicli-mesh url session_20260924093535

# 需要带令牌调用写接口时（注意：URL 含密钥，别贴到会被转发的地方）
$a = aicli-mesh url session_20260924093535 --with-token

# 复用活节点或拉起新节点，并打印可直接打开的窗口 URL（含令牌）
aicli-mesh open session_20260924093535

# 改名部署：指定要拉起的二进制（优先于 AICLI_BIN；也可 $env:AICLI_BIN = '...'）
aicli-mesh open session_20260924093535 --bin 'E:\tools\aicli-2x\aicli-2x.exe'

# 新建一个会话（子进程自己生成会话 ID；工作区缺省 = 当前目录，必须已存在）
aicli-mesh new --workspace 'E:\work\proj-a'

# 跨进程调用：先只读探一眼（无需 --allow-write）
aicli-mesh call session_20260924093535 node.info --json
aicli-mesh screen session_20260924093535 --tail 40

# 让另一个进程跑一轮 prompt（写操作必须显式允许；幂等键便于安全重试）
aicli-mesh send session_20260924093535 "只回复两个字：收到" --allow-write --client-request-id mesh-1-1

# 通用 op 形式：审批决议（写）+ 中断（写）
aicli-mesh call session_20260924093535 input --args '{"type":"approval","request_id":"req_1","allow":true}' --allow-write
aicli-mesh call session_20260924093535 cancel --allow-write

# 清理前先看计划，再执行
aicli-mesh gc
aicli-mesh gc --apply --purge-legacy --prune-bindings

# 让目标自己收尾退出（目标需 --mesh-allow-stop=true；卡死时改 --force）
aicli-mesh stop session_20260924093535

# 复盘：最近 10 分钟这台机器上发生了什么（进程全退也能看）
aicli-mesh watch

# 脚本消费：把窗口内的事件喂给 jq / ConvertFrom-Json（跑完就退）
aicli-mesh watch --since 1h --once --json

# 只盯一个节点（含历史），Ctrl-C 结束
aicli-mesh watch --node node-22024 --since 0

# 体检并让 CI 感知问题（problems > 0 → 退出码 5）
aicli-mesh doctor --json
```

## 10. 故障排查

| 现象 | 原因与处理 |
|------|------------|
| `ls` 总是空，`doctor` 报「根目录未解析」 | 没有可用的主目录（既无 `AICLI_MESH_DIR` 也无 `AICLI_HOME`/用户主目录）。设置 `AICLI_MESH_DIR` 或检查运行账户的 Profile |
| 列表里一堆 `stale` | 已退出进程的档案，属正常残留；`aicli-mesh gc --apply` 清理 |
| `show` 报歧义 | 前缀同时命中多个节点/会话，按列出的候选改用完整 ID 或 `pid:<PID>` |
| `url` 退出码 3 | 目标没有 HTTP 端点（纯 TUI 会话，未开 `--pprof`/`--web-port`） |
| `doctor` 报 `permissions` 告警 | `AICLI_MESH_DIR` 指向了用户 Profile 之外的位置；确认该目录的访问控制，或改回默认位置 |
| `gc --apply` 退出码 5 | 个别文件被占用/无权限：`errors[]` 给出具体路径与原因，其余项已删除，可直接重跑 |
| 两个进程都显示同一会话 | `doctor` 的 `ownership` 会报 problem（双占用）；先 `show` 看两边的心跳与租约，再停掉过期的一方 |
| `call` 退出码 2 + `mesh_no_endpoint` | 目标没开回环控制面（纯 TUI 节点）：让目标带 `--pprof` / `--web-port` 启动 |
| `call` 退出码 6 + `mesh_write_not_allowed` | 写 op（`invoke`/`input`/`cancel`/`sessions.resume`）缺 `--allow-write`：确认意图后显式加上 |
| `call` 退出码 6 + `mesh_token_stale` | 目标重启导致写令牌轮换：CLI 已自动重读档案重试一次，仍失败说明档案里的令牌已过期——`aicli-mesh show` 确认目标心跳，必要时重取 `url --with-token` |
| `call` 退出码 3 + `unreachable` | 目标档案还在但端口已关：目标进程已退出或未监听；`ls` 看状态、`doctor` 看 `permissions`/`stale-nodes` |
| `call` 退出码 4 + `busy` | 目标已有 invoke 在等待（单飞锁）：稍后重试，或先用 `screen` 观察它在忙什么 |
| `call` 退出码 6 + `mesh_cross_workspace_denied` | 目标开了 `--mesh-restrict-workspace`，而这是**跨工作区的写调用**：改用同工作区的节点，或让目标关掉该开关（默认关闭；只读调用不受影响） |
| `open` 拉起的是另一个版本（或旁边的 `aicli.exe`） | 改名后的二进制既不是 `self`（名字不叫 aicli）也不是 `sibling`（兄弟名硬编码 `aicli.exe`）：用 `--bin` / `AICLI_BIN` 显式指定，或先 `doctor` 看 `spawn-executable` 的实际解析结果（§4.10） |
| `doctor` 报 `spawn-executable` 为 problem | `AICLI_BIN` 指到了不存在/是目录的位置：修好或清空它。该覆盖**不会**退回 `self`/`sibling`/`PATH`（否则等于悄悄换版本） |
| `open` 退出码 5 + `mesh_spawn_bin_unavailable` | 找不到可拉起的 aicli 二进制：设 `AICLI_BIN` / `open --bin`，或从完整安装运行（与 `aicli-mesh.exe` 同目录放一个 `aicli.exe`） |
| `new` 报 `--workspace ... 不可用` | 工作区必须**已存在**：`new` 不会替你创建目录（会话要在里面落地，猜一个目录等于让返回值说谎）。先建目录，或改用 `open` 接上既有会话（§4.10 / §4.11） |
| `new` 退出码 3 + `mesh_spawn_timeout` | 进程起了但迟迟没登记新会话档案：先看 `log_tail`（子进程启动即失败最常见）；`--wait` 加大只对慢启动有效（§4.11） |
| 想接着旧会话跑，却用了 `new` | `new` 从不复用，每次都是新会话：接旧会话用 `open <会话>`（§4.10） |

## 11. 与其它组件的关系

- **单一聚合源**：`ls` / `show` 复用 `internal/mesh.BuildView`，与节点内
  `GET /web/api/mesh/self|peers` 是同一套聚合逻辑（工具与 API 不各写一份，§5.4 / §7.5）；
- **会话绑定取代端口档案**：`bindings/` 是 `web-ports/` 的替代品，旧目录由 `gc --purge-legacy` 清理（M8，§2.4）；
- **档案版本**：磁盘档案的 `schema_version` 与本文的 `--json` `schema_version` 独立演进；
  读取方对未知主版本标记为 `unknown` 且**从不改写**；
- **构建登记**：`scripts/build.ps1`（`LdflagsKind = main-version`）与 `Makefile` 的 `aicli-mesh` 目标
  负责出包；CI 门禁为 `go test ./internal/mesh/...` 加 `--help` 冒烟。

## 12. 别名：`aicli mesh`（与独立二进制同源）

架构 §7.5 / Q10 的落地：**做别名，但不写第二套实现**。`aicli mesh` 与 `aicli-mesh`
共用 `internal/mesh.CLI`——参数解析、渲染、`--json` 信封、退出码都只有一份代码；
想改行为就改 `internal/mesh/cli.go`，两边同时生效（避免「文档说 A、命令做 B」）。

```text
aicli mesh ls --json
aicli mesh show session_20260924093535
aicli mesh watch --since 5m
aicli mesh gc --apply
```

| 方面 | 行为 |
|------|------|
| 参数 | 原样透传（cobra 不解析网格旗标）：`aicli mesh watch --once --since 1h --json` 与独立二进制逐字同义 |
| 退出码 | 原样返回 0–6（§5），含 2 / 3 / 4 / 6 —— 别名**绕开** cobra 的错误路径，不会把「目标不存在」压成 1 |
| 帮助 | `aicli mesh --help` 打印的就是 `aicli-mesh --help` 那份用法（文本仍以 `aicli-mesh` 为名：两边共用一份，不复制、不改写） |
| 版本 | `aicli mesh version` 报**宿主 aicli 的构建版本**（与 `aicli` 自身版本同源，由 main 注入），不是 `aicli-mesh` 二进制的版本 |
| 节点身份 | **不是节点**：`aicli mesh ...` 不写节点档案、不占租约、不出现在 `ls` 里（唯一写盘路径仍是 `gc --apply`，以及 `open` / `new` 拉起的子进程） |
| 无参数 | `aicli mesh` 把用法打印到 stderr 并退出码 1（与 `aicli-mesh` 无参数一致） |

什么时候用哪个：装了独立二进制就两者皆可（脚本里写 `aicli-mesh` 更明确）；
只部署了单个 `aicli` 二进制（改名部署、容器镜像）时用 `aicli mesh`。
