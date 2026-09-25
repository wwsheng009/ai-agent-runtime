# aicli-mesh 排障手册

> 权威参考：[../aicli/mesh-cli.md](../aicli/mesh-cli.md) §5（退出码）/ §10（现象表）；
> HTTP 侧错误码见 [../aicli/web-remote-api.md](../aicli/web-remote-api.md) §9。
> 本页把两者收敛成「先看哪里、再看什么、怎么处置」。

## 1. 三步诊断法

```powershell
aicli-mesh ls -a               # 1) 现状：有哪些节点/谁在线、谁残留（-a 含 stale/stopped）、ADDR 是否为空
aicli-mesh doctor              # 2) 体检：problems/warnings 定位到具体检查项
aicli-mesh show <目标>         # 3) 细节：心跳/绑定/租约/日志尾部 + 令牌原文与 /web?token=… 打开地址
aicli-mesh watch --since 10m   # 4) 时间线：谁起停、谁接管、谁调用了谁（进程全退也能看）
```

**先分清三类失败**，处理方式完全不同：

| 类别 | 特征 | 处置方向 |
|------|------|----------|
| 根目录/档案类 | `ls` 空、`doctor` 报 `paths`/`nodes` | 环境变量与权限（§3） |
| 目标类 | 退出码 2/3/4 | 目标不存在 / 不可达 / 忙（§2） |
| 策略类 | 退出码 6，`mesh_*` 拒绝码 | 开关与显式授权（§4） |

## 2. 退出码契约（0–6）

| 码 | 含义 | 典型触发 |
|----|------|----------|
| 0 | 成功 | 任何命令正常完成（空网格也是成功） |
| 1 | 用法/参数错误 | 未知子命令/参数、缺少目标、`--sort` 非法、`watch` 位置参数、`--bin` 不可用 |
| 2 | 目标不存在或无法唯一确定 | `show nope`、歧义（候选会列出）、`call` 的 `not_found`（含 `mesh_no_endpoint`）、`stop` 的 `not_found`、`watch` 指定的目标在 journal 里完全不存在 |
| 3 | 目标不可达 | `url` 目标无端点（纯 TUI）；`call` 的 `unreachable`/`timeout`；`stop` 的 `timeout`；`open` / `new` 的 `not_running` |
| 4 | 冲突或忙碌 | `call` 的 `busy`（目标已有 invoke 在等，HTTP 409） |
| 5 | 操作失败 | `gc --apply` 有删除失败项、`doctor` 发现问题、`open` / `new` 的 `failed`、JSON 输出失败、`call` 的 `error` |
| 6 | 被策略拒绝 | `call` 的 `refused`、`stop` 的 `refused`（见 §4） |

约定：错误信息写 **stderr**，数据写 **stdout**——`aicli-mesh ls --json > x.json` 不会混入提示文本；
`call` 失败时 stderr 形如 `调用失败: refused（mesh_write_not_allowed）: …`，便于脚本 grep 原因码。

## 3. doctor 检查项 → 处置

| id | 级别 | 处置 |
|----|------|------|
| `paths` | ok / problem | 根目录解析不到：设置 `AICLI_MESH_DIR` 或检查运行账户主目录（fail-closed 下读取给空视图） |
| `permissions` | ok / warn | `AICLI_MESH_DIR` 指向用户 Profile 之外：确认该目录访问控制，或改回默认位置 |
| `nodes` | ok / problem | 档案不可读 / schema 未知：不要手工改档案，先备份再让 `gc` 处理 |
| `stale-nodes` | warn | 已退出进程的残留档案：`aicli-mesh gc --apply` |
| `ownership` | ok / problem | 同一会话被两个活节点占用：`show` 看两边心跳与租约，再停掉过期的一方（或 `--takeover`） |
| `leases` | warn | 租约持有者已退出：`gc --apply` 回收 |
| `tokens` | ok / problem | 声明需要令牌的节点令牌文件不可读：检查档案与令牌文件权限 |
| `journal` | ok / problem | 日志有截断行：磁盘已满 / 写入中断，检查磁盘与进程日志 |
| `journal-orphans` | warn | 无节点档案的孤儿日志：`gc --apply`（按 `--keep-days` 窗口） |
| `legacy` | warn | 旧 `web-ports/` 残留：`gc --apply --purge-legacy` |
| `spawn-executable` | ok / warn / problem | `problem`＝`AICLI_BIN` 指错（**显式配置错误**，不会回退）；`warn`＝机器上没有 aicli 二进制。详见 [spawn-and-binaries.md](./spawn-and-binaries.md) §5 |

## 4. `mesh_*` 错误码字典

### 4.1 跨进程调用（`call` / `send` / `screen`）

| 码 | 退出码 | 含义与处置 |
|----|--------|------------|
| `mesh_write_not_allowed` | 6 | 写 op 缺 `--allow-write`：确认意图后显式加上（CLI 本地拒绝，不发请求） |
| `mesh_nonloopback_denied` | 6 | 非回环调用：跨机一律拒绝（本机 127.0.0.1 才行）。目标以非回环地址监听时**整机**退出网格写路径（连回环客户端也拒）；逃生门 `--mesh-allow-nonloopback=true` 开在**目标进程**上、默认关 |
| `mesh_cross_workspace_denied` | 6 | 目标开了 `--mesh-restrict-workspace` 且这是**跨工作区写调用**：改用同工作区节点，或让目标关掉该开关（只读不受影响） |
| `mesh_token_stale` | 6 | 目标重启导致写令牌轮换：CLI 已自动重读档案重试一次仍失败——`show` 确认目标心跳并直接取回新令牌（或 `url --with-token`） |
| `mesh_no_endpoint` | 2 | 目标没开回环控制面（纯 TUI）：让目标带 `--pprof` / `--web-port` 启动 |
| `mesh_target_stopped` | 2 | 目标档案已标 `stopped`：它已退出，改选活节点 |
| `mesh_target_ambiguous` | 2 | 目标引用命中多个：改用完整 ID 或 `pid:<PID>` |
| `mesh_target_not_found` | 2 | 目标不存在 |
| `mesh_target_mismatch` | 2 | 被调方发现 `target` 指向别的节点（防串线自检）：核对目标 ID |
| `mesh_unknown_op` | 5 | op 不在白名单 9 项内：查 mesh-cli.md §4.7 的表 |
| `mesh_schema_unsupported` | 6 | 目标档案主版本比本工具新：调用方本地直接判 `refused`（档案不可用，**不是**操作本身出错）：升级工具，不要降级改写档案 |
| `mesh_upstream_error` | 5 | 目标端点内部错误：看目标的 stderr / journal；被调方其它状态会按其 HTTP 折算（403→6、409→4、404→2、504→3） |
| `mesh_body_too_large` | 5 | 请求体超 1 MiB：拆小 `--args` |
| `mesh_disabled` | 6 | 目标网格已关闭（`--mesh=false`）：调用面返回 403 `refused`；拉起面见 §4.2 |
| `mesh_stream_unavailable` | — | Web SSE 扇入关闭或订阅被拒（HTTP 503；客户端上限 429） |

### 4.2 拉起（`open` / `new` / `POST /web/api/mesh/spawn`）

| 码 | 退出码 | 含义与处置 |
|----|--------|------------|
| `mesh_spawn_bin_unavailable` | 5 | 找不到要拉起的 aicli 二进制：设 `AICLI_BIN` / `--bin`，或与 `aicli-mesh.exe` 同目录放 `aicli.exe` |
| `mesh_spawn_timeout` | 3 | 子进程起了但就绪超时：看 `log_tail`，加大 `--wait` |
| `mesh_spawn_failed` | 5 | 拉起失败（看 `reason` / `log_tail`） |
| `mesh_spawn_not_allowed` | 6（HTTP 403） | 目标进程 `--mesh-allow-spawn=false`（**默认开启**）：需要时显式打开 |
| `mesh_workspace_missing` | 5 | 工作区不存在：既有会话（`open`）看绑定/档案，新会话（`new`）看 `--workspace`——它必须**已存在**（CLI 会当场拒绝不存在的目录，退出码 1） |
| `mesh_disabled` | 5 | 网格根不可用 / `--mesh=false`（fail-closed，`failed`）；HTTP spawn 侧为 403 `refused` |

### 4.3 停止（`stop` / `POST /web/api/mesh/stop`）

| 码 | 退出码 | 含义与处置 |
|----|--------|------------|
| `mesh_stop_not_allowed` | 6 | 目标进程未开停止开关（`--mesh-allow-stop=true`）：CLI 无法绕过 |
| `mesh_stop_self_refused` | 6 | 目标是本进程/调用方：停自己用 `/exit` |
| `mesh_stop_timeout` | 3 | graceful 投递成功但进程没消失：加大 `--wait` 或 `--force` |
| `mesh_stop_already_stopped` | 0 | 幂等成功：目标已不在运行，重试安全 |
| `mesh_stop_bad_mode` | — | `mode` 不是 `graceful`/`force`（HTTP 400） |
| `mesh_stop_force_failed` | — | `--force` 终止进程失败（权限/句柄） |
| `mesh_nonloopback_denied` | 6 | 非回环调用：跨机一律拒绝 |

## 5. 现象表（高频故障）

| 现象 | 原因与处理 |
|------|------------|
| `ls` 总是空，`doctor` 报根目录未解析 | 没有可用主目录：设置 `AICLI_MESH_DIR`，或检查运行账户的 Profile |
| `ls` 空但 `ls -a` 有 `stale`/`stopped` 档案 | 正常：默认只列在线；残留档案用 `aicli-mesh gc --apply` 回收 |
| `ls -a` 列表里一堆 `stale` | 已退出进程的正常残留：`aicli-mesh gc --apply` |
| `show` 报歧义 | 前缀命中多个目标：按列出的候选改用完整 ID 或 `pid:<PID>` |
| `url` 退出码 3 | 目标没有 HTTP 端点（纯 TUI，未开 `--pprof`/`--web-port`） |
| 两个进程都显示同一会话（`conflict`） | `doctor` 的 `ownership` 报 problem：`show` 看两边心跳与租约，再停掉过期的一方；这是**设计要暴露**的状态，不是 bug |
| `call` 退出码 3 + `unreachable` | 档案还在但端口已关：目标已退出或未监听（`ls -a` 看状态、`doctor` 看 `stale-nodes`） |
| `call` 退出码 4 + `busy` | 目标已有 invoke 在等待（单飞锁）：等结束再重试；**不要**换 `client_request_id` 重发 |
| `open` 拉起的是另一个版本（或旁边的 `aicli.exe`） | 改名二进制既不是 `self` 也不是 `sibling`：用 `--bin` / `AICLI_BIN`，先看 `doctor` 的 `spawn-executable` |
| `open` 退出码 5 + `mesh_spawn_bin_unavailable` | 找不到可拉起的 aicli：设 `AICLI_BIN` / `--bin`，或与 `aicli-mesh.exe` 同目录放 `aicli.exe` |
| `open` 的 `reused` 不是我想要的（想强制换新进程） | 用 `--takeover`：回收租约后拉起新节点；旧节点继续运行并把自己标 `orphaned`（不会被杀） |
| `new` 报 `--workspace ... 不可用` | 工作区必须已存在（CLI 当场拒绝，退出码 1）：先建目录；要接**既有**会话改用 `open <会话>` |
| `new` 每次都开一个新会话（不复用） | 这是设计：`new` 没有会话 ID 可复用、也不抢租约；接旧会话用 `open <会话>` |
| `stop` 一律 `refused` | 目标没开 `--mesh-allow-stop=true`（默认关闭）：开关在被停进程上，CLI 不能绕过 |
| `watch` 回放为空 / journal 文件不增长 | 目标进程开了 `--mesh-journal=false`（默认开）：启动 stderr 有 Info 行、`GET /web/api/mesh/self` 回显 `mesh.journal_enabled=false`；需要审计就让目标去掉该开关（其余功能照常） |
| `watch` 提示「目标不存在」 | `--node`/`--session` 指定的目标在 journal 里**完全没有**出现过（退出码 2）；改成存在的前缀或先 `ls` |
| `doctor` 报 `spawn-executable` 为 problem | `AICLI_BIN` 指到了不存在/是目录的位置：修好或清空它（**不会**退回其它候选） |
| Web 端「在新窗口打开」失败且提示诊断命令 | spawn 失败：按返回的 `code` 查 §4.2，并跑一次 `aicli-mesh doctor` |

## 6. 复现与证据（隔离实验）

```powershell
# 把实验关进临时目录，不碰真实网格
$lab = Join-Path $env:TEMP ('mesh-lab-' + (Get-Random))
New-Item -ItemType Directory -Force $lab | Out-Null
$env:AICLI_MESH_DIR = $lab

aicli-mesh version --json      # 确认 root 与 root_source
aicli-mesh ls -a --json        # 空网格：counts 全 0、nodes 为 []（不是 null）
aicli-mesh doctor --json       # 起步基线：只有 paths 等基础检查
```

需要真实多进程复现时，按 [../e2e/mesh-e2e.md](../e2e/mesh-e2e.md) §4 的两进程步骤与
`scripts/test-aicli-debug-endpoints-e2e-mesh.ps1`（E2E-DEBUG-03，M1–M12 断言）走一遍——
它覆盖了发现、CLI/HTTP 同源、租约互斥、跨进程 invoke、扇入实时性、崩溃对账、令牌不泄露、
非回环默认拒绝（M11）与 journal 降级（M12）。
