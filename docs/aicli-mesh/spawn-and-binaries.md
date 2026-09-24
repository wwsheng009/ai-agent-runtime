# 拉起节点与可执行文件解析（`open` / `new` / `spawn` 专题）

> 适用：`aicli-mesh open` / `aicli-mesh new`（CLI）与 `POST /web/api/mesh/spawn`（Web）——
> 三者共用 `mesh.Spawn`。
> 本文回答两个最容易踩的问题：**拉起的到底是哪个 aicli 二进制**、**改名部署怎么办**。

## 1. `open` 做什么

在**会话的工作区**里复用活节点或拉起新进程，并返回可直接打开的窗口 URL：

```text
aicli-mesh open <会话> [--port N] [--wait 8s] [--no-wait] [--takeover] [--bin PATH] [--json]
```

| 开关 | 作用 |
|------|------|
| `--port N` | 首选回环端口（1–65535）；缺省先用会话绑定里的端口，再随机挑空闲端口 |
| `--wait 8s` | 就绪等待预算（默认 8s） |
| `--no-wait` | 只报告「已启动」，不等节点就绪 |
| `--takeover` | 跳过复用，显式回收该会话的租约；旧节点**不会被杀**，下次心跳把自己标 `orphaned` |
| `--bin PATH` | 指定要拉起的 aicli 二进制；必须存在，且**优先于 `AICLI_BIN`** |

拉起的子进程命令行（固定）：

```text
<解析出的 aicli>  resume <会话>  --pprof  --web-host 127.0.0.1  [--web-port N]  [--web-token <令牌>]
```

四态与退出码：`reused` / `started` → 0，`not_running` → 3，`failed` → 5。
CLI 本身**不是节点**：单飞租约的 owner 记作 `cli-<pid>`，不写自己的节点档案。

### 1.1 `new`：新建会话（同一套 Spawn，只是没有会话 ID）

会话还不存在时用 `new`——它在 `--workspace` 里拉起 `aicli chat`（**不带** `--session`），
由子进程生成新会话 ID，父进程再从节点档案里读回来：

```text
aicli-mesh new [--workspace PATH] [--port N] [--wait 8s] [--no-wait] [--bin PATH] [--json]
```

```text
<解析出的 aicli>  chat  --pprof  --web-host 127.0.0.1  [--web-port N]  [--web-token <令牌>]
```

| 方面 | 行为 |
|------|------|
| 工作区 | `--workspace`，缺省 = 当前目录；必须已存在（CLI 当场校验，退出码 1） |
| 复用 / 租约 | **都没有**：会话还不存在，没有键可抢——每次调用都产生一个新会话 |
| 就绪判定 | 按 **pid** 等「属于该 pid、且 `session_id` 非空」的 live 档案（`open` 是按会话查） |
| 退出码 | `started` → 0，`not_running`（超时）→ 3，`failed` → 5；不会出现 `reused` |

二进制解析顺序（§2）、`AICLI_BIN` / `--bin` 的**不回退**规则、`doctor` 诊断（§5）与 `open` 完全一致。

## 2. 可执行文件解析顺序（硬契约）

| 顺序 | 规则 | 来源标签 |
|------|------|----------|
| 1 | `open --bin PATH` 指定的文件（存在且不是目录） | `--bin` |
| 2 | `AICLI_BIN` 指向的文件 | `AICLI_BIN` |
| 3 | 当前进程自己（**仅当**文件名就叫 `aicli`：大小写不敏感、去掉扩展名） | `self` |
| 4 | 当前进程**同目录**下的 `aicli<扩展名>`（即 `aicli.exe`，名字**硬编码**） | `sibling` |
| 5 | `PATH` 里的 `aicli` | `PATH` |
| — | 都没有 → 拉起失败（`mesh_spawn_bin_unavailable`） | — |

两条**不回退**规则：

- `AICLI_BIN` 是**权威**：设了但不可用（不存在 / 是目录 / 不可读）→ 直接失败，
  **绝不**退回 `self`/`sibling`/`PATH`。理由：「我指定了哪个二进制」不该变成猜谜——
  悄悄拉起另一个版本比报错更难排查；
- `--bin` 同规则：路径不可用时在 CLI 解析阶段当场拒绝（**用法错误，退出码 1**，
  不发请求、不进入 Spawn）。

`--bin` 与 `AICLI_BIN` 都只在**拉起**时生效；`ls` / `show` / `call` 等读命令与它们无关。

## 3. 改名部署（最容易踩的坑）

`aicli-2x.exe` 既不是 `self`（名字不叫 aicli），也不会把自己的名字当兄弟名——
兄弟名**硬编码**为 `aicli.exe`。实测：在只有 `aicli-2x.exe` 的环境里不带覆盖运行 `doctor`，
解析会落到 `PATH` 并**命中机器上另一个 aicli**（来源 `PATH`）——这是本专题存在的直接原因。

三种正确做法（任选）：

```powershell
# A. 环境变量（推荐放进启动脚本；子节点原样继承）
$env:AICLI_BIN = 'E:\tools\aicli-2x\aicli-2x.exe'
aicli-mesh open session_20260924093535

# B. 单次显式指定（优先于 AICLI_BIN）
aicli-mesh open session_20260924093535 --bin 'E:\tools\aicli-2x\aicli-2x.exe'

# C. 在 aicli-2x.exe 旁边放一个 aicli.exe（sibling 规则命中）
```

**建议绝对路径**：相对路径按**调用方 cwd** 检查，而子进程是在**会话工作区**里启动的——
两者通常不是同一个目录。

## 4. 失败语义（大声报错，不静默换版本）

| 场景 | 结果 |
|------|------|
| 解析不到任何二进制 | `status=failed` + `code=mesh_spawn_bin_unavailable` + `reason`，退出码 5 |
| `AICLI_BIN` 指错 | 同上（`reason` 说明原因：找不到文件 / 是目录 / 权限错误） |
| `open --bin` 指错 | CLI 用法错误，退出码 1，**不进入 Spawn** |
| `new --workspace` 指到不存在/非目录 | CLI 用法错误，退出码 1，**不进入 Spawn** |
| 子进程起来了但没就绪（含 `new` 等不到新会话档案） | `status=not_running` + `code=mesh_spawn_timeout` + `log_tail`（末尾 20 行，已脱敏），退出码 3 |
| 会话工作区不存在 | `code=mesh_workspace_missing` |
| 网格根不可用（fail-closed） | `code=mesh_disabled` |
| Web 端被开关关闭 | `403` + `mesh_spawn_not_allowed`（`--mesh-allow-spawn=false`；**默认开启**） |

`open --json` 失败时带 `code` 与 `reason`；`not_running` / `failed` 还会带 `log_tail`（末尾 20 行）。
前端按 `code` 分支（不解析散文 `reason`），并在出现 `mesh_spawn_bin_unavailable` 时提示
「诊断：`aicli-mesh doctor`」。

## 5. 诊断：doctor 的 `spawn-executable`

```powershell
aicli-mesh doctor --json | ConvertFrom-Json |
    Select-Object -ExpandProperty checks |
    Where-Object id -eq 'spawn-executable' | Format-List
```

| status | 含义 | 处理 |
|--------|------|------|
| `ok` | 解析到可用二进制 | 无需动作；`detail` 写明路径与来源（`--bin` / `AICLI_BIN` / `self` / `sibling` / `PATH`） |
| `warn` | 一台机器上找不到 aicli 二进制 | 只读用法不受影响；需要 `open` / `new` 时再配置 |
| `problem` | **`AICLI_BIN` 指错**（显式配置错误） | 修好或清空该变量；它不会退回其它候选 |

`doctor` 的 `problems > 0` → 退出码 5，可直接用于 CI 门禁。

## 6. 本地复现（隔离实验）

```powershell
$lab = Join-Path $env:TEMP 'mesh-bin-lab'; New-Item -ItemType Directory -Force $lab | Out-Null
$env:AICLI_MESH_DIR = $lab

# 1) 指错就失败（不会退回 self/sibling/PATH）
$env:AICLI_BIN = Join-Path $lab 'nope.exe'
aicli-mesh doctor            # → [问题] spawn-executable ...；退出码 5
aicli-mesh open session_demo # → failed + mesh_spawn_bin_unavailable；退出码 5
Remove-Item Env:AICLI_BIN

# 2) 看当前解析结果与来源
aicli-mesh doctor            # → [ok] ... 使用 <path>（来源 self / sibling / PATH）

# 3) --bin 指错：用法错误，不进 Spawn
aicli-mesh open session_demo --bin (Join-Path $lab 'nope.exe')   # → 退出码 1
```

## 7. 容易踩的坑（清单）

1. **改名部署**：见 §3；先跑 `doctor` 看 `spawn-executable` 的实际解析结果，再排查别的；
2. **相对路径**：`--bin` / `AICLI_BIN` 都建议绝对路径（子进程在会话工作区里启动）；
3. **期望「自动回退」**：`AICLI_BIN` 指错时不会回退——这是**故意的**，修配置而不是让它猜；
4. **复用判断依赖档案端点**：活节点已服务该会话时 `open` 直接返回它的 URL（`reused`）；
   要强制换新进程用 `--takeover`；
5. **多套安装**：机器上多个 aicli 时，`PATH` 命中哪一个由环境决定——用 `doctor` 固化预期，别靠猜；
6. **把 `new` 当成 `open`**：`new` 从不复用、也不接管，每次都是**新会话**；接旧会话用 `open <会话>`。
