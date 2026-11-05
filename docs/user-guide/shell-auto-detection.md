# Shell 自动检测与配置

> 适用程序：`aicli`、`runtime-server` 及其内置工具执行链。
> 结论：**当前版本默认开启 shell 自动检测，不需要额外配置。**

本文说明 agent 执行本地 shell 命令时如何选择 shell、如何确认当前生效值，以及什么时候需要手动配置 `runtime.yaml` 的 `shell:` 段。

> 源码环境中的 `aicli` 命令等价于在 `backend/` 目录执行 `go run ./cmd/aicli`。已安装发行版的用户直接使用 `aicli`。

---

## 目录

1. [默认行为](#1-默认行为)
2. [快速检查当前 shell](#2-快速检查当前-shell)
3. [选择顺序与配置字段](#3-选择顺序与配置字段)
4. [启用或调整自动检测](#4-启用或调整自动检测)
5. [配置入口](#5-配置入口)
6. [故障排查](#6-故障排查)
7. [限制与注意事项](#7-限制与注意事项)

---

## 1. 默认行为

即使 `runtime.yaml` 中没有 `shell:` 段，当前版本也会按以下默认值工作：

| 字段 | 默认值 | 含义 |
|------|--------|------|
| `autoDetect` | `true` | 使用内置候选链自动寻找可用 shell |
| `strictProbe` | `true` | 所有候选都失败时报 `SHELL_UNAVAILABLE`，不静默改用其他方言 |
| `probeTimeout` | `3s` | 单个候选的哨兵探针超时 |
| `scriptDelivery` | `stdin` | 长脚本默认通过 stdin 传入，避免触发 Windows 命令行长度上限 |

Windows 的内置候选顺序为：

```text
git-bash -> msys-bash -> busybox -> pwsh -> powershell -> cmd
```

每个候选都不只是检查“文件是否存在”，还会实际启动并执行哨兵脚本。只有进程成功结束且输出包含预期哨兵值时，候选才算通过；最终选择**第一个通过的候选**。

两个容易误解的点：

- `autoDetect: true` 是缺省语义。没有写 `shell:` 段并不等于关闭自动检测。
- WSL 不在默认候选链中。需要 WSL 时必须在 `shell.prefer` 中显式写入 `wsl`，避免把未安装发行版时的 `bash.exe` 启动器误判为可用 shell。

### 1.1 怎么判断当前是否自动启用

运行：

```bash
aicli doctor shell --json
```

如果同时看到以下结果，就是**默认自动检测已生效**，不是配置缺失或未启用：

- `auto_detect: true`
- `strict_probe: true`
- `selected.source: autodetect`
- `prefer` 显示内置候选链，例如 Windows 上的 `git-bash, msys-bash, busybox, pwsh, powershell, cmd`

若当前目录下的 `runtime.yaml` 没有 `shell:` 段，说明这些值来自代码默认值。`doctor shell` 只读取和探测，不会为了“启用”而自动补写配置。

---

## 2. 快速检查当前 shell

查看当前会选中哪个 shell、所有候选的探测结果，以及配置实际写入位置：

```bash
aicli doctor shell
```

机器可读输出：

```bash
aicli doctor shell --json
```

重点字段：

| JSON 字段 | 说明 |
|-----------|------|
| `config_path` | 配置写入目标；手动编辑 YAML 时以它为准 |
| `config_layer` | 当前写入层，例如 `project` 或 `user` |
| `auto_detect` | 自动检测是否生效 |
| `prefer` | 本次实际尝试的候选链 |
| `selected.id` | 最终选中的候选 ID |
| `selected.path` | shell 可执行文件路径 |
| `selected.dialect` | 模型应使用的命令方言：`posix-full`、`posix-narrow`、`powershell` 或 `cmd` |
| `selected.source` | 来源：`autodetect`、`prefer`、`config-path` 或 `fallback` |
| `candidates[]` | 每个候选的 `pass` / `fail` / `skip`、路径、原因和探针耗时 |

强制重新探测：

```bash
aicli doctor shell --refresh
```

只验证某个候选，不修改配置：

```bash
aicli doctor shell --probe-only git-bash
aicli doctor shell --probe-only wsl
```

> `doctor shell` 只读，不会修改 `runtime.yaml`。如果 `strictProbe=true` 且没有任何候选通过，命令会返回非零退出码并报告 `SHELL_UNAVAILABLE`。

---

## 3. 选择顺序与配置字段

解析优先级固定为：

```text
shell.path > shell.prefer > 内置候选链
```

实际生成候选链的规则：

| 已配置字段 | `autoDetect` | 实际行为 |
|------------|--------------|----------|
| `path` | 任意 | 只探测 `path`，不尝试 `prefer` 或内置链 |
| 无 `path`，有 `prefer` | 任意 | 严格按 `prefer` 顺序探测，不追加内置候选 |
| 无 `path`、无 `prefer` | `true` 或未写 | 使用内置候选链自动检测 |
| 无 `path`、无 `prefer` | `false` | 候选链为空，通常无法选出 shell |

`shell:` 段支持以下字段：

| 字段 | 类型 | 说明 |
|------|------|------|
| `autoDetect` | bool | 是否启用内置候选链，缺省 `true`。仅在 `prefer` 为空时决定是否生成候选链 |
| `prefer` | string[] | 自定义候选顺序。非空时**完全替代**内置链，不会自动追加默认候选 |
| `path` | string | 强制指定一个 shell；仍必须通过探针，并且不会再尝试候选链 |
| `probeTimeout` | duration | 单个候选的探针超时，例如 `3s`、`500ms` |
| `strictProbe` | bool | 全部候选失败时是否返回错误，缺省 `true` |
| `scriptDelivery` | `stdin` / `argv` | 脚本传递方式；生产使用 `stdin`，`argv` 仅用于诊断 |
| `env` | map | 注入 shell 进程的环境变量；不允许覆盖 `PATH` |

支持的候选 ID：

| ID | 平台 | 说明 |
|----|------|------|
| `git-bash` | Windows | Git for Windows，优先使用 `bin\bash.exe` |
| `msys-bash` | Windows | MSYS2，检查 `MSYS2_ROOT`、`C:\msys64`、`C:\msys32` |
| `busybox` | Windows / Unix | 优先使用程序旁的 `vendor/busybox`，其次搜索 PATH |
| `wsl` | Windows | WSL 启动器；只会在 `prefer` 显式列出时使用 |
| `pwsh` | Windows / Unix | PowerShell 7+，从 PATH 查找 |
| `powershell` | Windows | Windows PowerShell |
| `cmd` | Windows | `cmd.exe` |
| `bash` / `zsh` / `sh` | Unix 为主 | 通用 POSIX shell 候选；Windows 下由 WSL 启动器提供的 `bash` 会被守卫规则跳过 |

`strictProbe` 的实际语义是：

- `true`：逐项尝试候选；全部失败时返回 `SHELL_UNAVAILABLE`，执行链不会悄悄改用另一种 shell 方言。
- `false`：全部失败时记录一个内部最终兜底 shell（`selected.source=fallback`），运行时仍可继续，但实际方言可能与预期不同。

建议保持 `strictProbe: true`。如果确实要在缺少 POSIX shell 的机器上继续运行，应先确认能接受 PowerShell 或 `cmd` 方言，再考虑关闭。

---

## 4. 启用或调整自动检测

### 4.1 使用默认自动检测

不需要写任何 `shell:` 配置。若希望把默认行为显式记录在文件中，可写：

```yaml
shell:
  autoDetect: true
  strictProbe: true
  probeTimeout: 3s
  scriptDelivery: stdin
```

这与完全不写 `shell:` 段的有效行为一致。不要再额外配置 `prefer`，否则内置候选链会被自定义链替换。

### 4.2 调整候选顺序

例如优先使用 PowerShell 7，再退回 Windows PowerShell 和 `cmd`：

```yaml
shell:
  autoDetect: true
  prefer: [pwsh, powershell, cmd]
```

`prefer` 是一个**完整链**，上面只尝试列出的三个候选，不会在末尾追加 `git-bash`、`msys-bash` 或 `busybox`。需要完整兜底时请自行列全：

```yaml
shell:
  autoDetect: true
  prefer: [git-bash, msys-bash, busybox, pwsh, powershell, cmd]
```

### 4.3 启用 WSL

WSL 默认不参与自动检测。确认发行版已安装并可启动后，显式加入候选链：

```yaml
shell:
  autoDetect: true
  prefer: [wsl, git-bash, pwsh, cmd]
```

建议保留后续候选。这样 WSL 启动器缺失、发行版未安装或探针失败时，仍会继续尝试下一个候选。

### 4.4 强制使用指定二进制

`path` 优先于 `prefer` 和内置链，并且只尝试这一个 shell：

```yaml
shell:
  path: 'C:\Program Files\Git\bin\bash.exe'
```

要求：

- 路径指向已知 shell 二进制：`bash`、`sh`、`zsh`、`pwsh`、`powershell`、`cmd` 或 `busybox`。
- 含目录的路径必须是绝对路径；只写 `bash` 这类裸名称时才通过 PATH 查找。
- 文件仍要通过探针。路径不存在或探针失败时，不会自动退回候选链。

### 4.5 关闭自动检测

```yaml
shell:
  autoDetect: false
```

`autoDetect: false` 的含义是**不生成内置候选链**，但不是“完全禁用 shell”。此时：

- 如果配置了 `path`，仍使用该强制路径。
- 如果配置了 `prefer`，仍按自定义链尝试。
- 如果既没有 `path` 也没有 `prefer`，候选链为空；`strictProbe=true` 时返回 `SHELL_UNAVAILABLE`。

因此，通常不需要关闭自动检测。想固定某个 shell 时优先使用 `path`，想调整优先级时使用 `prefer`。

---

## 5. 配置入口

`shell:` 段有四个等价入口，写入的是同一套配置字段。

### 5.1 手写 runtime.yaml

先执行：

```bash
aicli doctor shell --json
```

查看输出中的 `config_path` 和 `config_layer`，再编辑该文件。不要只凭目录名判断，也不要默认修改 `backend/configs/runtime.yaml`。

CLI 的合并顺序为：

```text
$HOME/.aicli/runtime.yaml       用户层（低优先级）
./.aicli/runtime.yaml           项目层（高优先级，覆盖同名字段）
```

合并按字段进行，不是整段替换。例如用户层配置了 `strictProbe: false`，项目层只写 `prefer`，则项目层的 `prefer` 会覆盖用户层同名字段，而 `strictProbe` 仍继承用户层值。删除高层字段只会重新暴露低层值，不会自动恢复代码默认值；若低层也设置了该字段，需要同时清理低层配置。

项目层路径取决于执行命令时的当前目录：

- 在仓库根执行 `aicli doctor shell`：项目层是 `./.aicli/runtime.yaml`。
- 在 `backend/` 执行 `aicli doctor shell`：项目层是 `backend/.aicli/runtime.yaml`。

因此，手工编辑前应先运行 `doctor shell --json`，以输出的 `config_path` 为准。`backend/configs/runtime.yaml` 是便携/开发配置，通常只在 `runtime-server --config backend/configs/runtime.yaml` 显式指定时读取；它不是上述 `.aicli` 合并链的隐式项目层。

### 5.2 aicli 配置 TUI

```bash
cd backend
go run ./cmd/aicli config --tui
```

已安装环境：

```bash
aicli config --tui
```

在菜单中选择「Shell 执行环境」。该页面可调整候选顺序、强制路径、自动检测开关、探针超时和脚本传递方式，也可清缓存重新探测。保存时会显示实际写入文件。

TUI 会立即更新自身进程的解析结果；已经在另一个进程中运行的 `runtime-server` 不会因外部 TUI 保存而自动重载，需重启服务或直接使用 Web 设置页保存。

### 5.3 Web 后端配置工作台

启动 `runtime-server` 后打开：

```text
http://localhost:8101/runtime/config
```

端口以实际 `--listen` 为准。选择「Shell 执行环境」：

1. 在「自动检测」中确认开关状态。
2. 在「候选顺序」中增删和排序候选 ID。
3. 点击「检测」使用当前草稿真实探测，不会保存配置。
4. 保存后，当前 `runtime-server` 进程会立即失效旧缓存并重新探测。

### 5.4 HTTP API（脚本调用）

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/runtime/config/shell` | 读取生效配置、候选矩阵和写入目标 |
| `PUT` | `/api/runtime/config/shell` | 保存 `shell.*` |
| `POST` | `/api/runtime/config/shell/probe` | 按当前或请求中的草稿重新探测，不落盘 |

请求体使用 snake_case 字段：`auto_detect`、`prefer`、`path`、`probe_timeout`、`strict_probe`、`script_delivery`、`env`。`PUT` 只更新请求中出现的字段；`prefer: []` 和 `path: ""` 可清空对应配置。`POST /probe` 接受同样字段作为草稿，未提供的字段沿用当前生效值。

非法候选、非法时长或不合法的 `shell.path` 会返回 `400`，且不会修改内存中的生效配置。

---

## 6. 故障排查

### 6.1 选中的不是我想要的 shell

先看完整矩阵：

```bash
aicli doctor shell --refresh --json
```

检查：

1. `prefer` 是否非空。非空时它会完全替代内置链。
2. `path` 是否非空。强制路径存在时，`prefer` 不会参与。
3. 目标候选是否 `pass`。PATH 中存在文件不代表它能成功执行探针。
4. `selected.source` 是 `autodetect`、`prefer`、`config-path` 还是 `fallback`。

### 6.2 报 `SHELL_UNAVAILABLE`

表示严格模式下没有任何候选通过探针。依次处理：

1. 查看 `candidates[].reason` 和 `candidates[].path`。
2. 安装至少一个真实可用的 shell，例如 Git for Windows、MSYS2、PowerShell 或 `cmd`。
3. 若使用 `path`，确认路径、引号和文件权限正确。
4. 若使用 WSL，先显式加入 `prefer`，再确认发行版能正常运行。
5. 只在确实接受方言降级时，才考虑设置 `strictProbe: false`。

### 6.3 WSL 被跳过

这是预期行为。默认链不包含 WSL，防止 `C:\Windows\System32\bash.exe` 这类启动器在未安装发行版时产生误导。显式配置：

```yaml
shell:
  prefer: [wsl, git-bash, pwsh, cmd]
```

然后验证：

```bash
aicli doctor shell --probe-only wsl
```

### 6.4 修改配置后没有生效

- 确认改的是 `doctor shell` 报告的 `config_path`。
- 确认项目层没有覆盖用户层同名字段。
- `doctor shell --refresh` 只能刷新当前命令的读取与探测结果，不能热重载另一个已运行进程。
- 已有 `runtime-server` 进程在手工修改 YAML 后通常需要重启；通过它的 Web 设置页保存则会即时更新该进程。

### 6.5 长命令在 Windows 上失败

先确认 `scriptDelivery` 是默认的 `stdin`：

```yaml
shell:
  scriptDelivery: stdin
```

`argv` 会把完整脚本拼进命令行，容易触发 `cmd.exe` 的约 8191 字符限制或 `CreateProcessW` 的 32767 字符上限，只应在诊断短脚本时使用。

---

## 7. 限制与注意事项

- **只影响本地工具执行链**：本机 `shell.*` 不会改变远端工具目标的 shell。`envd` 固定使用目标机 `/bin/sh -c`；远端 `shell` / `job_start` 使用它们自己的 `shell.program` 配置，见 [aicli-remote-tools.md](aicli-remote-tools.md)。
- **方言会影响模型写法**：选中 `git-bash` / `bash` / `zsh` 时是 `posix-full`；`busybox` 和普通 `sh` 是 `posix-narrow`；PowerShell 与 `cmd` 使用各自语法。
- **探针结果有进程内缓存**：修改配置后使用 Web/TUI 保存，或执行 `aicli doctor shell --refresh` 做诊断。新启动的 CLI / 服务进程会重新解析。
- **Win7 使用专用候选链**：Win7 构建参考 [backend/configs/runtime.win7.yaml](../../backend/configs/runtime.win7.yaml)，默认候选为 `git-bash -> busybox -> msys-bash -> powershell -> cmd`，不使用 WSL 和 PowerShell 7。
- **`shell.env` 不允许覆盖 `PATH`**：避免同一个候选 ID 因外部路径变化解析到不同程序；需要改变查找路径时请显式使用 `shell.path`。
- **高风险命令仍走审批**：自动检测只决定解释器，不绕过权限、审批或命令风险策略。

---

## 相关文档

- [aicli 使用手册](aicli.md)
- [runtime-server 使用手册](runtime-server.md)
- [aicli 安装与完整命令说明](../aicli/install.md)
- [远程工具与远端 shell 配置](aicli-remote-tools.md)

