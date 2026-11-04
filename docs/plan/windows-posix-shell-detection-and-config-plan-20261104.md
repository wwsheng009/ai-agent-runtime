# Windows 上使用 POSIX Shell 的方案：自动探测 + 配置控制 + doctor/TUI/Web 四入口

- 状态：Implemented（P0/P1/P2 代码已落地；附录 B 中环境依赖项仍未实测，见 §17）
- 日期：2026-11-04
- 适用范围：`aicli`（CLI / TUI / Web 控制台）、`runtime-server`、Win7 兼容构建
- 相关文档：[windows7-compat-internals.md](../aicli/windows7-compat-internals.md)、[windows7.md](../aicli/windows7.md)、[exec.md](../aicli/exec.md)

---

## 0. 结论摘要

1. **要的不是"Linux"，是"POSIX 语义层"。** WSL 提供另一个内核，进程/文件系统/路径/工具链全部跨界；MSYS2（Git Bash）与 busybox-w32 是**原生 Windows 进程**却提供 `sh` 语义，才是能和 Windows 共存的那一层。
2. 新增 `runtime.yaml` 的 `shell:` 段，由**配置驱动**（`prefer` 候选顺序 + `path` 强制指定 + `autoDetect` 开关），而不是硬编码平台判断。
3. 探测必须用**哨兵探针**而不是 `exec.LookPath`：实测 `C:\Windows\System32\bash.exe`（WSL 桩）在未安装发行版时返回 `exit=1`、stdout 0 字节，`LookPath("bash")` 会把它选中，导致命令"静默成功但什么也没做"。
4. 四个入口统一读写同一份配置：`aicli doctor shell`（诊断）、`aicli config --tui`（终端交互）、`GET/PUT /api/runtime/config/shell`（Web 与前端设置页）、`runtime.yaml`（手写）。
5. 顺带修复一个现存缺陷：`internal/background/detached.go` 的 `detachedRunnerHostAvailable()` 只认 PowerShell，导致"装了 Git Bash 但没有 PowerShell"的机器（正是 §7 Win7 裁剪工控机）白降级为进程内监管。
6. **shell 不是在所有地方只解析一次。**`background.Config.Shell` 这个注入点已经存在，但 `DefaultUserShell()` 还在 detached runner、PTY 会话、提示词、工具预检、`aicli exec`、remotetools 共 7 类位置被独立调用。配置只注入 manager 会造成"前台 bash、超时转后台变 PowerShell"这类漂移。**必须引入进程级单一 resolver**（§16）。
7. **remote tools（envd 后端）完全不受本方案影响**：远端 shell 工具硬编码 `/bin/sh -c`，`ResolvedShell` 对 envd 主动置零，且 envd 后端根本不注册 `job_*` 工具。远端本来就已是 POSIX，没有收益也没有风险（§16.3）。

---

## 1. 问题与实测证据

### 1.1 现状

| 位置 | 现状 |
| --- | --- |
| `internal/executor/shell_detect.go:211` `defaultWindowsShell()` | 只按 `pwsh → powershell → cmd` 顺序 `LookPath`，完全不考虑 Git Bash / busybox |
| `internal/executor/shell_detect.go:99` `DeriveExecArgs()` | 长脚本走 PowerShell `-EncodedCommand`（阈值 7000），sh 类只有 `-c` |
| `internal/background/detached.go:80` `defaultDetachedRunnerHostAvailable()` | Windows 上只检查 `pwsh` / `powershell`，缺失即放弃 detached |
| `internal/background/detached.go:727` | `runnerPath` 扩展名由 `runtime.GOOS` 决定，与"实际用哪种 shell"解耦 |

`Shell` / `ShellType` 抽象本身是完整的（`bash|zsh|sh|powershell|pwsh|cmd`），缺口只在**选择策略**与**验证**。

### 1.2 实测数据（Windows 11 26200，2026-11-04）

启动开销，各 10 次取均值：

| Shell | 单次启动 | 说明 |
| --- | --- | --- |
| MSYS bash 5.3（PortableGit） | **444 ms** | 参考基线 |
| powershell 5.1 | 1150 ms | 2.6× |
| pwsh 7.x | 1420 ms | 3.2× |

语义差异：

| 行为 | MSYS bash | powershell |
| --- | --- | --- |
| `exit 3` 透传 | `3` ✅ | 折叠成 1（需 `WrapPowerShellExitCode` 补丁） |
| 命令不存在 | `127` ✅ | 需额外判断 |
| 36 KB 脚本 | `bash -s < file` 正常 ✅ | 必须 `-EncodedCommand` + 7000 阈值 |
| 输出编码 | 默认 UTF-8 | 跟随 GBK 代码页，需 `[Console]::OutputEncoding` 加固 |

GNU 工具覆盖（Git Bash `/usr/bin`）：`find/sort/sed/awk/grep/tar/diff/xargs` 齐全；`curl` 命中 `System32\curl.exe`；`rsync` 缺失。

### 1.3 三点实测事实（方案据此设计）

**事实 1：`LookPath("bash")` 会撞上 WSL 桩。**

```
where bash.exe →
  C:\Windows\System32\bash.exe                     ← WSL 启动器（PATH 最前）
  C:\Users\<u>\AppData\Local\Microsoft\WindowsApps\bash.exe
  C:\Users\<u>\...\PortableGit\...\bin\bash.exe    ← 真正的 POSIX bash
  C:\Users\<u>\...\PortableGit\...\usr\bin\bash.exe
```

实测 `C:\Windows\System32\bash.exe -c "echo hello"` → `exit=1`，stdout **0 字节**，stderr 16 字节本地化提示。命令没有执行，也没有可用错误信息。**结论：任何"探测"都必须真跑一条哨兵命令并校验 stdout。**

**事实 2：`cmd //c` 的双斜杠约定不能被全局关闭。**

MSYS 会把 `/c` 当路径改写，调 `cmd.exe` 必须写 `cmd //c`。实测 `MSYS2_ARG_CONV_EXCL='*'` 会连带禁用 `//c → /c` 的修正，`cmd` 于是进入交互模式并挂住。**结论：不要 blanket-exclude 参数转换；改为一律使用正斜杠路径 + 逐参数处理。**

**事实 3：反斜杠在 shell 里是转义符。**

未加引号的 `printf '%s' C:\Users\vince\x` 输出 `C:Usersvincex`；加引号才保留。**结论：脚本层路径规范统一为 `C:/Users/...` 正斜杠形式（MSYS 与 Windows API 都接受，实测 `cd "C:/Users"` 成功），并在提示词里明确要求。**

---

## 2. 目标与非目标

### 2.1 目标

1. Windows 上默认优先使用 POSIX shell（Git Bash / busybox），使模型可以用一套 `sh` 语法写命令。
2. 探测**可验证**：任何候选都必须通过哨兵探针才算可用；失败给出可读原因。
3. 选择**可配置**：候选顺序、强制路径、探测超时、方言、环境加固全部由 `runtime.yaml` 的 `shell:` 段控制。
4. 四个入口配置同源：`doctor shell` 读、`config --tui` 写、Web API 读写、`runtime.yaml` 手写；写入落在**可写层**。
5. 保留完整降级链：POSIX 不可用时退回 PowerShell，再退回 cmd，行为与今天一致。
6. Win7 兼容构建可用：内置 busybox 兜底，不依赖用户安装任何东西。

### 2.2 非目标

- 不把 WSL 作为默认 shell（见 §3.3）。若要支持，作为 `os_sandbox` 的可选后端，与本方案的 `shell:` 段解耦。
- 不统一 POSIX 与 PowerShell 的语法差异（不做命令翻译层）。方言由探测结果决定并注入提示词。
- 不改变 `exec` 工具的审批、沙箱、输出采集逻辑，只替换"用哪个 shell 二进制执行"。
- 不引入新的 shell 二进制分发（除 busybox 之外不打包 shell）。

---

## 3. 分层模型

```
┌────────────────────────────────────────────────────────────┐
│ 4. 表现层  doctor shell │ config --tui │ Web 设置页 │ yaml  │
├────────────────────────────────────────────────────────────┤
│ 3. 方言层  posix-full / posix-narrow / powershell / cmd     │
│            → 决定提示词片段与长脚本传递方式                  │
├────────────────────────────────────────────────────────────┤
│ 2. 选择层  显式 path > prefer 链 > 内置缺省链               │
│            每个候选先探针验证，再落选/入选                   │
├────────────────────────────────────────────────────────────┤
│ 1. 执行层  argv -c 短命令 / stdin 长脚本 / Job Object 终止   │
└────────────────────────────────────────────────────────────┘
```

### 3.1 候选标识（稳定 ID）

配置与 UI 都用这组 ID，不暴露内部 `ShellType`：

| ID | ShellType | 方言 | 典型路径 |
| --- | --- | --- | --- |
| `git-bash` | bash | posix-full | `<Git>\bin\bash.exe` |
| `msys-bash` | bash | posix-full | `C:\msys64\usr\bin\bash.exe` |
| `busybox` | sh | posix-narrow | `<exe目录>\vendor\busybox\busybox.exe` |
| `wsl` | bash | posix-full | `%SystemRoot%\System32\bash.exe`（需显式启用） |
| `pwsh` | pwsh | powershell | `pwsh.exe` |
| `powershell` | powershell | powershell | `powershell.exe` |
| `cmd` | cmd | cmd | `cmd.exe` |
| `sh` / `bash` / `zsh` | sh/bash/zsh | posix-full | Unix 平台 `$SHELL` 等 |

### 3.2 内置缺省链

```
Windows:  git-bash → msys-bash → busybox → pwsh → powershell → cmd
Unix:     $SHELL → zsh → bash → /bin/sh          （保持不变）
```

`wsl` **不在**缺省链里，只有用户在 `prefer` 中显式写出才会被尝试（理由见 §3.3）。

### 3.3 为什么 WSL 不作为默认

| 维度 | WSL | MSYS / busybox |
| --- | --- | --- |
| 进程边界 | 看不到也管不了 Windows 进程，`taskkill /T`、Job Object 全部失效 | 原生进程，`attachProcessTree` 直接可用 |
| 文件系统 | `/mnt/c` 跨 VM，慢；`chmod` 语义为模拟 | 直接访问 `C:\`，零拷贝 |
| 路径 | `/mnt/c/...` 与 `C:\...` 两套 | 一套（正斜杠） |
| 工具链 | Windows 的 go/node/msvc 不在里面 | 直接调 `.exe` |
| 平台要求 | Win10 1903+，**Win7 无** | 有 Win7 版本 |
| 后台任务 | 不能在 WSL 里托管 Windows 侧 detached runner | 可以 |

WSL 适合"用户显式选择的项目级沙箱"，与现有 `internal/executor/os_sandbox*.go` 同级，而不是 agent 的默认执行环境。

---

## 4. 配置模型

### 4.1 `runtime.yaml` 新增段

```yaml
shell:
  # autoDetect=false 时只接受 path 或 prefer 中显式列出的候选，
  # 不做内置缺省链回退；purpose 是让"锁定某个 shell"的团队环境可复现。
  autoDetect: true

  # 候选顺序（稳定 ID，见 §3.1）。留空 = 内置缺省链。
  # 显式写出时**只**按此顺序尝试，不追加缺省链尾部（可复现优先）。
  prefer: [git-bash, busybox, powershell]

  # 强制指定二进制路径（最高优先级）。设置后跳过 prefer 的路径解析，
  # 但仍做哨兵探针；探针失败按 strictProbe 处理。
  path: ""

  # 单次探针超时。3s 足够覆盖 PowerShell 冷启动（实测 1.15-1.42s）。
  probeTimeout: 3s

  # 探针失败即视为不可用。设为 false 仅用于排障（会记录 warning），
  # 生产环境不建议关闭。
  strictProbe: true

  # 长脚本传递方式：
  #   stdin  —— sh 类用 `-s` + stdin（推荐）；PowerShell 暂仍用 -EncodedCommand
  #   argv   —— 一律拼进命令行（仅排障用，会撞 8191/32767 限制）
  scriptDelivery: stdin

  # 注入到 shell 进程的环境变量（键值对，覆盖同名变量）。
  # 注意：NO_COLOR/TERM/PAGER/GIT_PAGER/GH_PAGER/CLICOLOR 已由
  # internal/executor 的 NeutralizeConsoleStylingEnv 在 background 路径强制设置，
  # 这里不要重复实现；本段只用于补充它没覆盖的项（如 LANG/LC_ALL）。
  env:
    LANG: C.UTF-8
    LC_ALL: C.UTF-8
```

字段命名沿用 `runtime.yaml` 主段的 camelCase（见 `background:` / `agent:`）。`knowledge:` 用的 snake_case 属历史遗留，不在本段重复。

### 4.2 缺省值即"行为零变化"审查

| 字段 | 缺省 | 缺省时的行为差异 |
| --- | --- | --- |
| `autoDetect` | `true` | **有变化**：Windows 上会优先选中 Git Bash（这是本方案的目的），需在 release note 明示 |
| `prefer` | 空 | 走内置缺省链 |
| `path` | 空 | 不做强制 |
| `probeTimeout` | `3s` | 从不探测 → 探测一次，增加一次启动开销（约 0.4-1.4s，仅进程内首次） |
| `strictProbe` | `true` | 从不校验 → 校验 |
| `scriptDelivery` | `stdin` | sh 类的长脚本从 argv 改为 stdin；PowerShell 暂保持 `-EncodedCommand` 不变（附录 B 第 6 项） |
| `env` | 见上 | 新增 `PAGER/GIT_PAGER/TERM/NO_COLOR`，属于预期的行为改善 |

若某项要保持旧行为，可设 `autoDetect: false` + `prefer: [powershell, cmd]`，得到与今天等价的链。

### 4.3 配置分层与写入位置

现有层栈（`internal/agentconfig/config_layers.go`）：

```
legacy(1) → legacy(2) → user ~/.aicli/runtime.yaml → project ./.aicli/runtime.yaml
                                                       ↑ 优先级最高
```

关键约束（务必写进实现注释）：

- `backend/configs/runtime.yaml` 与发行包 `configs/runtime.yaml` 属于 **portable 层，只读**，`RuntimeConfigWriteTarget()` 永不返回它。配置默认值示例只能写在文档里，不能靠改这个文件生效。
- 新键（`shell.*`）在层栈中不存在时，写入落到**可写层的最高存在层**（通常是用户级，必要时新建文件）。
- 写入走 `MergedConfigDocument.ApplyDocumentPathChange("shell.prefer", [...]string, target)`，与 `agent.maxSteps` 同一条路径，天然获得：
  - 单键粒度 upsert，保留其余键、注释与顺序
  - 覆盖整段读-改-写的写锁（`agentconfig.LockConfigFileWrite`）
  - 可写/只读层归属判定（`WriteLayerKindFor` 供 UI 显示"将写入哪个文件"）

> 待实测项：`ApplyDocumentPathChange` 的 `value` 是 `interface{}`，切片经 `yaml.Marshal` 序列化应为块序列（`- git-bash`）。实施时补一条"写入切片 → 重新加载 → 深比较相等"的往返单测（`shell.prefer` 是首个切片值，此前只有标量先例）。

---

## 5. 探测算法

### 5.1 流程

```
1. 解析配置 ── shell.path 非空 ──→ 候选 = [path]（最高优先，标记 source=config）
                        │
                        └─→ 候选 = resolve(prefer 或 缺省链)

2. 对每个候选（按序）
     a. 定位二进制（LookPath / 已知安装目录 / 发行包随附）
     b. 排除已知陷阱（见 5.3）
     c. 跑哨兵探针（见 5.2），超时 probeTimeout
     d. 探针通过 → 选中，记录 path/version/source，结束
        探针失败 → 记录 reason，继续下一个

3. 全部失败
     strictProbe=true  → 返回错误（exec 工具报 SHELL_UNAVAILABLE，不静默降级）
     strictProbe=false → 回落到平台最终兜底（Windows: cmd）并记录 warning
```

### 5.2 哨兵探针

单一探针要同时验证三件事：能启动、能执行、stdout 通路正确。

```go
// 期望 stdout 恰为哨兵（TrimSpace 后相等），且 exit code 为 0。
const shellProbeSentinel = "__aicli_shell_ok__"

// sh 类：  <path> --noprofile --norc -c 'printf %s __aicli_shell_ok__; exit 0'
// PS 类：  <path> -NoProfile -NonInteractive -Command 'Write-Output __aicli_shell_ok__'
// cmd：   <path> /c 'echo __aicli_shell_ok__'
```

判据：

| 结果 | 判定 |
| --- | --- |
| exit 0 且 stdout 含哨兵 | 可用 |
| exit≠0 | 不可用，reason = 首行 stderr（截断 200 字节，去除本地化乱码） |
| exit 0 但 stdout 为空 | **不可用**（WSL 桩即此类，实测 exit=1/空输出两种形态都出现过，必须同时覆盖） |
| 超时 | 不可用，reason = `probe timeout after <d>`

补充：版本探测（`--version` 首行）作为可选信息，不影响可用性判定 —— 部分裁剪环境 `--version` 会因缺少动态库失败但 shell 本身可用。

### 5.3 已知陷阱的排除规则

| 陷阱 | 排除方式 |
| --- | --- |
| `%SystemRoot%\System32\bash.exe`（WSL 启动器） | 归入 `wsl` 候选，**仅在 `prefer` 显式包含 `wsl` 时尝试**；否则直接跳过，避免撞桩 |
| `%LOCALAPPDATA%\Microsoft\WindowsApps\bash.exe` | 同上（实测返回 `E_ACCESSDENIED` 类提示） |
| `System32\find.exe` / `sort.exe` 抢占 GNU 同名工具 | bash 从 `bin\bash.exe` 启动时 `/usr/bin` 位于 PATH 前部，天然优先；**不**手工重排 PATH |
| `cmd` 需要 `//c` | 在 `DeriveExecArgs` 内固定生成 `//c`（sh 类上下文）或 `/c`（直接 `CreateProcess` 上下文），不在调用点判断 |
| 反斜杠路径 | 提示词与内部拼接统一使用正斜杠；不依赖 `MSYS2_ARG_CONV_EXCL` |

### 5.4 定位规则

```
git-bash:
  %ProgramFiles%\Git\bin\bash.exe
  %ProgramFiles(x86)%\Git\bin\bash.exe
  %LOCALAPPDATA%\Programs\Git\bin\bash.exe
  from `git --exec-path`            → 去掉尾部 /mingw64/libexec/git-core 再拼 /bin/bash.exe
  PATH 中的 bash.exe（探针通过才算）

msys-bash:
  C:\msys64\usr\bin\bash.exe
  %MSYS2_ROOT%\usr\bin\bash.exe

busybox:
  <executable dir>\vendor\busybox\busybox.exe     （随发行包分发，§12）
  PATH 中的 busybox.exe

pwsh:        PATH
powershell:  %SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe
cmd:         %SystemRoot%\System32\cmd.exe
```

优先 `bin\bash.exe` 而不是 `usr\bin\bash.exe`：前者是 MSYS launcher，会正确初始化 `/usr/bin` 与 `MSYSTEM`，使 GNU 工具在 PATH 内可用；两者都支持 `--noprofile --norc`。

### 5.5 缓存

- 进程内 `sync.Once` 级 memo，键为 `(GOOS, 配置快照指纹)`；配置热更新后失效。
- **不落盘缓存**：PATH 与安装状态会随用户操作变化，落盘缓存会把一次误判固化。
- `doctor shell --refresh` / `POST .../shell/probe` 强制重探。

---

## 6. 方言与执行策略

### 6.1 方言档位

| 方言 | 判定 | 提示词片段要点 |
| --- | --- | --- |
| `posix-full` | bash / zsh | 可用 bash 数组、`[[ ]]`、`pipefail`；路径一律正斜杠 |
| `posix-narrow` | busybox ash / dash | 仅 POSIX sh；无 `[[ ]]`、无数组、无 `pipefail`；coreutils 需经 busybox applet |
| `powershell` | pwsh / powershell | PowerShell 语法；`$LASTEXITCODE` 语义；输出编码注意事项 |
| `cmd` | cmd | 批处理语法；无管道退出码透传 |

提示词注入点复用现有 `Shell.Metadata()`（已产出 `shell_display`/`shell_type`/`shell_path`），新增 `shell_dialect` 字段，供 harness 拼接对应片段。

### 6.2 长脚本

```go
// sh 类（bash / ash）—— 缺省路径
cmd := exec.CommandContext(ctx, shell.Path, "--noprofile", "--norc", "-s")
cmd.Stdin = strings.NewReader(script)

// PowerShell —— 暂保持现状：-EncodedCommand（见附录 B 第 6 项）
// 验证 `-Command -` 的多行 stdin 行为与退出码配合之后，再切换。
```

`scriptDelivery: stdin` 对 sh 类立即生效；PowerShell 在附录 B 第 6 项实测通过前仍走 `-EncodedCommand`，避免把一个未验证的传递方式当成缺省。收益仍然成立：sh 类彻底绕开 Windows 命令行长度限制，不再需要 7000 阈值这类经验值。

### 6.3 进程树终止

**不因换 shell 而放松。** bash 的 `kill -TERM -- -$pgid` 只覆盖 MSYS 进程；原生 Windows 子进程必须继续走 `attachProcessTree`（Win32 Job Object）+ `taskkill /T`。`detached.go` 现有的 `attachProcessTree` / `terminateProcess` 保持原样。

### 6.4 后台 detached runner

修复点：扩展名与启动方式由 **shell 类型**决定，而不是 `runtime.GOOS`。

| 选中 shell | runner 文件 | 启动方式 | 可用性判定 |
| --- | --- | --- | --- |
| PS 类 | `<log>.ps1` | `pwsh|powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File` | 现有实现不变 |
| POSIX 类 | `<log>.sh` | `<bash> --noprofile --norc <path>` | **新增**：Windows 上也能跑 `.sh` runner |
| 都没有 | — | 退回进程内监管 | `detachedRunnerHostAvailable()=false` |

`defaultDetachedRunnerHostAvailable()` 改为"存在任一探针通过的 shell 即 true"，而不是硬编码查 PowerShell。这条修复让"Git Bash 有、PowerShell 无"的机器重新获得断连存活能力。

`.sh` runner 在 Windows 上的写法沿用 `writeUnixDetachedRunner` 的结构（heartbeat 后台循环 + 状态码写文件），但需注意：

- heartbeat 循环用 `while kill -0 $$` 在 MSYS 下对自身 PID 有效，保留；
- 日志重定向 `>> <log> 2>&1` 保持；
- 退出码写 `<status>` 用 `printf '%s'` 而非 `echo`，避免尾部换行影响 `readDetachedExitCode` 的 `Atoi`。

---

## 7. 代码落点清单

| # | 文件 | 改动 |
| --- | --- | --- |
| 1 | `backend/internal/config/manager.go` | `RuntimeConfig` 新增 `Shell ShellConfig \`yaml:"shell" json:"shell"\``；新增 `ShellConfig` 结构 + `Validate()`（`probeTimeout>0`、`scriptDelivery` 枚举、`prefer` 只允许已知 ID） |
| 2 | `backend/internal/executor/shell_detect.go` | 新增 `ShellTypeBusybox`（或复用 `sh` + `Dialect` 字段）；`Shell` 增 `ID`/`Dialect`/`Source`/`Version` 字段；`Metadata()` 增 `shell_dialect`/`shell_source` |
| 3 | `backend/internal/executor/shell_resolve.go`（新） | 候选链解析 + `probeShell()` + memo 缓存 + `ResolveShell(cfg ShellConfig) (Shell, []CandidateReport, error)` |
| 4 | `backend/internal/executor/shell_detect.go` `DeriveExecArgs` | sh 类增加 `--noprofile --norc -s`（stdin）变体；cmd 固定 `//c` |
| 5 | `backend/internal/background/detached.go` | `defaultDetachedRunnerHostAvailable` 改用 shell 解析结果；`runnerPath` 扩展名与 `startDetachedRunner` 分支按 `shell.Dialect` 而非 `GOOS` |
| 6 | `backend/cmd/aicli/commands/doctor_shell.go`（新） | `aicli doctor shell`，仿 `doctor_search.go` 的 resolver+probe+report 三段式 |
| 7 | `backend/cmd/aicli/commands/doctor.go` | 注册 `cmd.AddCommand(newDoctorShellCommand())` 并在 `Long` 的子命令清单里补一行 |
| 8 | `backend/internal/api/runtimeapi/shell_settings.go`（新） | `GET/PUT /api/runtime/config/shell`、`POST .../shell/probe`，仿 `approval_explain_settings.go` |
| 9 | `backend/internal/runtimeserver/runtime_shell_settings.go`（新） | persister/reader，仿 `runtime_agent_max_steps.go`（`ApplyDocumentPathChange("shell.*")`） |
| 10 | `backend/cmd/aicli/commands/config_tui.go` | 主菜单加 `Shell 执行环境`：显示当前 + 候选矩阵 + 调整 `prefer` 顺序 |
| 11 | `frontend/src/api/runtime/config.ts` + `types/runtime/config.ts` | 三个新函数与响应类型 |
| 12 | `frontend/src/components/workspace/settings/runtime-shell-domain-editor.tsx`（新）+ `runtime-shell-domain-utils.ts`（新） | 仿 `runtime-concurrency-domain-editor.tsx` |
| 13 | `.../backend-config-settings-page/domains/shell.ts`（新）+ `lazy-editors.ts` + `sections/modes/shell.tsx` | 域注册，仿 `domains/concurrency.ts` |
| 14 | `frontend/src/i18n/resources/{zh-CN,en-US}/runtime-config` | 新增 shell 段文案 |
| 15 | `backend/configs/runtime.win7.yaml` | 显式 `shell.prefer: [busybox, git-bash, powershell, cmd]`（Win7 拒绝 `wsl` 与 `pwsh`） |

---

## 8. `aicli doctor shell`

### 8.1 定位

与 `doctor search`（rg 后端与 builtin 回退）、`doctor hooks`（配置来源与可执行性）同族：**只读诊断，不落盘**。

### 8.2 输出（text）

```
$ aicli doctor shell

shell resolution
  config:        C:\Users\vince\.aicli\runtime.yaml   (user layer, writable)
  autoDetect:    true
  prefer:        git-bash, busybox, pwsh, powershell, cmd
  selected:      git-bash  (bash 5.3.15)
  path:          C:\Program Files\Git\bin\bash.exe
  dialect:       posix-full
  probe:         PASS (52 ms)

candidates
  ID          STATUS  PATH                                              REASON
  git-bash    PASS    C:\Program Files\Git\bin\bash.exe
  msys-bash   SKIP    -                                                 not found
  busybox     SKIP    -                                                 bundled binary absent
  wsl         SKIP    C:\Windows\System32\bash.exe                       not in prefer (guard: WSL stub)
  pwsh        PASS    C:\Program Files\PowerShell\7\pwsh.exe
  powershell  PASS    C:\Windows\System32\WindowsPowerShell\v1.0\...
  cmd         PASS    C:\Windows\System32\cmd.exe

notes
- git-bash 为当前生效 shell；PowerShell 系列仅作为降级候选。
- 长脚本走 stdin（bash -s），不进入命令行长度限制。
```

### 8.3 输出（json）

```json
{
  "config_path": "C:\\Users\\vince\\.aicli\\runtime.yaml",
  "config_layer": "user",
  "writable": true,
  "auto_detect": true,
  "prefer": ["git-bash", "busybox", "pwsh", "powershell", "cmd"],
  "selected": {
    "id": "git-bash", "path": "C:\\Program Files\\Git\\bin\\bash.exe",
    "version": "5.3.15", "dialect": "posix-full", "source": "autodetect",
    "probe_ms": 52
  },
  "candidates": [
    {"id": "wsl", "status": "skip", "path": "C:\\Windows\\System32\\bash.exe",
     "reason": "not in prefer (guard: WSL stub)"}
  ],
  "findings": ["git-bash 为当前生效 shell", "wsl 候选被守卫规则跳过"]
}
```

### 8.4 标志

| Flag | 说明 |
| --- | --- |
| `--json` | JSON 输出（沿用 `resolveStructuredOutputOptions`） |
| `--refresh` | 跳过 memo，强制重探 |
| `--probe-only <id>` | 只探某个候选，用于排障（如 `--probe-only wsl`） |

退出码：全部候选失败且 `strictProbe=true` → 非 0（让 CI 与安装脚本能判定）；否则 0。

---

## 9. HTTP API

| Method | Path | 语义 |
| --- | --- | --- |
| `GET` | `/api/runtime/config/shell` | 返回生效值 + 候选矩阵 + 配置来源与写入目标 |
| `PUT` | `/api/runtime/config/shell` | 保存 `prefer`/`path`/`autoDetect`/`probeTimeout`/`strictProbe`/`scriptDelivery`/`env`；非法值 400 且**不改内存** |
| `POST` | `/api/runtime/config/shell/probe` | 强制重探并返回新矩阵（对应设置页"检测"按钮），不改配置 |

响应体（GET/PUT 同构）：

```json
{
  "effective": {
    "id": "git-bash", "path": "...", "version": "5.3.15",
    "dialect": "posix-full", "probe_ms": 52
  },
  "auto_detect": true,
  "prefer": ["git-bash", "busybox", "pwsh", "powershell", "cmd"],
  "path": "",
  "probe_timeout": "3s",
  "strict_probe": true,
  "script_delivery": "stdin",
  "candidates": [
    {"id": "cmd", "status": "pass", "path": "C:\\Windows\\System32\\cmd.exe"},
    {"id": "wsl", "status": "skip", "reason": "not in prefer (guard: WSL stub)"}
  ],
  "config_path": "C:\\Users\\vince\\.aicli\\runtime.yaml",
  "writable": true,
  "updated": false
}
```

实现要点（照抄 `agent.maxSteps` 的成熟做法）：

1. 先改 `RuntimeManager` 内存快照（`manager.Update(&next)`），失败的写法一律在内存更新之后回滚。
2. 再 `LoadMergedRuntimeConfigDocument()` + `ApplyDocumentPathChange("shell.prefer", ...)` 逐键落盘。
3. 层栈不可用时退回单文件写入（`manager.GetFilePath()`），保持旧行为可用。
4. **逐个键**调用 `ApplyDocumentPathChange`，不要整段替换 `shell:` —— 否则会吃掉用户手写的注释。

---

## 10. `aicli config --tui`

现有 TUI（`config_tui.go`，1213 行）主菜单围绕 provider 展开。新增一个与 provider 平级的分区，复用既有 `promptBoolUpdate` / `promptIntUpdate` / `notice` / `pause` / `reload` 原语：

```
AICLI 配置
  1) Providers
  2) Shell 执行环境          ← 新增
  3) Chat 偏好
  q) 退出

Shell 执行环境
  当前生效:   git-bash (bash 5.3.15)   [posix-full]
  配置文件:   C:\Users\vince\.aicli\runtime.yaml  (可写)

  候选状态:
    [PASS] git-bash     C:\Program Files\Git\bin\bash.exe
    [FAIL] busybox      未找到（发行包未附带 vendor\busybox\busybox.exe）
    [SKIP] wsl          不在 prefer 列表中
    [PASS] powershell   C:\Windows\System32\WindowsPowerShell\v1.0\...

  1) 调整候选顺序        2) 填写强制路径        3) 开关自动探测
  4) 重新探测            5) 修改探针超时        b) 返回
```

约束：

- TUI **只写可写层**，并在保存前显示目标文件路径，避免用户以为改到了仓库里的 portable 层。
- 调整顺序用"输入逗号分隔的 ID 序列"而不是上下键列表：与现有 TUI 的纯 stdin/stdout 风格一致（无 TUI 框架依赖），也便于测试。
- 保存后立即 `reload()` 并使 shell memo 失效，下次 `exec` 即生效。

---

## 11. Web 前端

### 11.1 数据流

前端设置页编辑的是**整份 runtime 配置文档草稿**（`useConfigCore` 的 `draftParsed`），保存走通用 document API；`shell` 段作为其中一个域参与：

```
PUT /api/runtime/config/document         ← 通用保存（已有）
  body.document.shell = { prefer: [...], autoDetect: true, ... }

POST /api/runtime/config/shell/probe    ← 只读探测（新），"检测"按钮用
GET  /api/runtime/config/shell          ← 展示生效值与写入目标（新）
```

### 11.2 新增文件（严格仿 concurrency 域）

| 文件 | 职责 |
| --- | --- |
| `settings/runtime-shell-domain-utils.ts` | `getRuntimeShellConfig(value)` → summary；`readText` 容错 |
| `settings/runtime-shell-domain-editor.tsx` | 展示当前生效值 + 候选表 + 顺序调整 + 检测按钮 |
| `backend-config-settings-page/domains/shell.ts` | `createShellDomain(core)`：把编辑结果 `setConfigValueAtPath(draft, ["shell"], {...})` |
| `backend-config-settings-page/sections/modes/shell.tsx` | 该域的 UI 区块 |
| `backend-config-settings-page/lazy-editors.ts` | 注册 `RuntimeShellDomainEditor`（`lazy()`） |

UI 要点：

- 候选表每行显示 `ID / 状态 / 路径 / 原因`，"检测"按钮触发 `probe` 接口后原地刷新（不写配置）。
- 状态色沿用设置页现有语义：PASS 用正向色、FAIL 用警示色、SKIP 用中性色；不引入新配色。
- `prefer` 编辑用可拖拽标签列表或上下移动按钮，最终序列化为字符串数组。
- 生效值与"即将保存的草稿"要视觉区分，避免用户把未保存的顺序当成已生效。
- i18n：`runtime-config` 命名空间新增 `shell.*` 键（zh-CN / en-US 同步）。

---

## 12. Win7 与分发策略

### 12.1 现实约束

- Git for Windows **2.46.2 是最后一个支持 Win7/8 的版本**（2.47.0 起要求 Win8.1+），官方 release note 与 FAQ 均已确认。因此不能假定 Win7 机器上有可用的 Git Bash。
- Win7 兼容构建（`Go 1.21.4`、`CGO_ENABLED=0`）已经承诺"不需要安装 Go、Node.js、MSYS2 或 winpty"。引入 shell 依赖不能破坏这条承诺。

### 12.2 内置 busybox 方案

随 Win7 兼容包分发 `vendor\busybox\busybox.exe`（busybox-w32，单文件约 1 MB，原生 Win32，支持 Win7，自带 ash + ~300 applet）。这是唯一"零安装、可复现、覆盖 Win7"的 POSIX 层。

需要处理的细节：

1. **applet 可见性**：`busybox sh -c` 里的 `ls/grep/sed` 不自动在 PATH 中。两个选项：
   - 启动时 `busybox --install -s <exeDir>\vendor\busybox\bin`，把该目录置于 PATH 首位；
   - 或依赖编译期的 `FEATURE_SH_STANDALONE`（shell 内直接调 applet）。
   > 待实测：确认所用 busybox-w32 构建是否开启 standalone。若未开启，采用 `--install -s`，并在测得的方案里固化目录布局。
2. **方言降级**：`posix-narrow`，提示词必须去掉 bash 扩展，否则模型写出 `[[ ]]` 会直接失败。
3. **不支持的能力**：`feat` 之外无 rsync、无完整 `sed -i` 语义差异需实测；`busybox sed -i` 可用但备份后缀行为与 GNU 有差异。

### 12.3 分层策略

| 机器类型 | 缺省链 | 说明 |
| --- | --- | --- |
| Win10/11 开发机 | `git-bash → busybox → pwsh → powershell → cmd` | 用户大概率已装 Git |
| Win10/11 无 Git | `busybox → pwsh → powershell → cmd` | 需检测发行包是否附带 busybox |
| Win7 兼容包 | `busybox → git-bash → powershell → cmd` | 显式在 `runtime.win7.yaml` 配好；拒绝 `wsl`/`pwsh` |
| Win7 裁剪机（无 PowerShell） | `busybox → cmd` | 依赖 §6.4 的 runner 修复才能保住 detached |

---

## 13. 安全约束

1. **`shell.path` 必须校验二进制名**属于已知 shell 集合（`bash`/`sh`/`pwsh`/`powershell`/`cmd`/`busybox`），拒绝任意可执行文件。否则配置一项即可把 agent 变成任意程序启动器（`path: evil.exe`）。
2. **探针不得执行用户可控内容**：哨兵脚本是编译期常量，不拼接任何配置值。
3. **不因配置而放宽审批**：`shell.*` 只影响执行环境，不进入审批决策。`exec` 的 `--yolo`/沙箱策略不变。
4. **`env` 键值校验**：只允许字母数字下划线键名；值不允许含换行（防注入脚本）。禁止覆盖 `PATH`（避免把 `/usr/bin` 顺序搞乱）。
5. **探测不跟随符号链接到不可信目录**：仅接受绝对路径与已知安装根下的路径。

---

## 14. 实施阶段与验收

### P0 — 探测与配置骨架（可独立上线）

- [x] `ShellConfig` + `Validate`，`runtime.yaml` 支持 `shell:` 段
- [x] `ResolveShell` + 哨兵探针 + `wsl` 守卫 + memo
- [x] `doctor shell`（text/json/`--refresh`）
- [x] `DeriveExecArgs` 增加 stdin 变体

**验收**

| 场景 | 期望 |
| --- | --- |
| 有 Git Bash | `doctor shell` 选中 `git-bash`，`exec 'echo hi'` 输出 `hi` |
| 只有 WSL 桩、无发行版 | 不选中 `wsl`，reason 明确；最终落到 `powershell` |
| `prefer: [powershell, git-bash]` | 选中 `powershell`（顺序生效） |
| `path` 指向不存在的文件 | 非严格模式给出 warning 并继续；严格模式非 0 退出 |
| 36 KB 脚本 | `exec` 正常，无命令行长度错误 |

### P1 — 四个入口打通配置

- [x] `GET/PUT /api/runtime/config/shell` + `probe`
- [x] `config --tui` 新增 Shell 分区
- [x] 前端 settings shell 域 + i18n
- [x] `runtime.win7.yaml` 显式链

**验收**

| 场景 | 期望 |
| --- | --- |
| Web 设置页调整顺序并保存 | 落盘到可写层；`runtime.yaml` 只变 `shell.prefer` 一行（注释保留） |
| TUI 保存 | 目标文件路径可见；保存后 `doctor shell` 立即反映 |
| 三处入口连续修改 | 无丢更新（写锁覆盖读-改-写） |
| 非法 `prefer` ID | API 400 且内存不变；TUI 提示重填 |

### P2 — detached runner 与 Win7

- [x] `.sh` runner 在 Windows 上可用（含 heartbeat/status 语义）
- [x] `detachedRunnerHostAvailable` 改用解析结果
- [x] `runtime.win7.yaml` 缺省链
- [ ] **Win7 发布包内置 busybox-w32**：属于打包/发布环节，本轮未改动制品清单；在该项落地前，Win7 裁剪机仍依赖自带 Git Bash 2.46.2 或 PowerShell 5.1 之一存在。

**验收**

| 场景 | 期望 |
| --- | --- |
| 仅 Git Bash、无 PowerShell | 后台任务可 detached，进程存活可跨重启收养 |
| busybox 作为唯一 shell | `doctor shell` 选中 `busybox`；方言为 `posix-narrow`；提示词不含 bash 扩展 |
| Win7 裁剪机 | 不因缺 PowerShell 而失去后台能力 |

---

## 15. 风险与回滚

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 双方言导致 skill/示例文档漂移 | 同一份 skill 在某些机器失败 | 规定 POSIX 为 canonical 方言；PowerShell 仅作降级；skill 校验脚本按方言标注 |
| MSYS fork 开销 | 大量短命令（如循环 50 次 `git`）比原生慢 | 实测单次 444 ms 是含进程创建；批量场景鼓励用内建工具（grep/glob/read）而非 shell 循环 |
| `autoDetect` 缺省变更 | 升级后行为变化 | release note 明示；提供 `autoDetect: false` + 等价 `prefer` 的一行回滚 |
| busybox applet 不在 PATH | `posix-narrow` 下命令找不到 | `--install -s` 固化 shim 目录；`doctor shell` 把该目录纳入报告 |
| 探针把慢机器误判为不可用 | 冷启动 > 3s 的低配机 | `probeTimeout` 可配；失败 reason 显示实际耗时，便于定位 |
| 写入落到只读层 | 配置看似保存但不生效 | 保存前用 `WriteLayerKindFor` 显示目标文件；portable 层永不写入 |

**整体回滚**：`shell:\n  autoDetect: false\n  prefer: [powershell, cmd]` 一行即回到当前行为；代码层面 `ResolveShell` 可通过 `ShellConfig` 零值短路到旧 `defaultWindowsShell()`。

---

## 16. 与既有 job-first 架构、remote tools 的关系

> 本节为 2026-11-04 代码审计补充，结论会改变 §7 的实施要求，实施前必读。

### 16.1 现状：两层 job 与多处独立解析

**第一层：工具层 job-first（每个 shell 调用都是一个 background job）**

`internal/toolkit/tools/bash.go:354` 的注释是硬约束：

> Every shell call is a job. There is no direct process fallback.

`executeJobFirst` → `shelljob.Execute` → `background.Manager`；等待超时后命令被 **promoted**，返回 `job_id` 与 `next_action: job_output job_id=...`，进程不被杀。所以"自动转入 job"不是可选项，而是唯一执行路径。

**第二层：执行层 Job Object（Windows 进程作业对象）**

`internal/executor/process_guard_windows.go:33-46`：`CreateJobObject` + `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | BREAKAWAY_OK`，随后 `AssignProcessToJobObject`。每条命令一棵树一个 job，超时/取消用 `TerminateJobObject` 一次杀全树；`BREAKAWAY_OK` 专门为 `DetachAllowed` 的 `CREATE_BREAKAWAY_FROM_JOB` 留出逃生口。

**关键事实：shell 不在工具层选，在 background 层选。**

`shelljob.Request`（`internal/shelljob/runtime.go:64-74`）只有 `Command/Workdir/WaitTimeout/OutputLimit`，**没有 Shell 字段**。真正解析发生在：

```go
// internal/background/manager.go:3508-3517
shell := cfg.Shell                                   // ← 已有注入点
if strings.TrimSpace(shell.Path) == "" {
    shell = runtimeexecutor.DefaultUserShell()       // ← 局部兜底
}
```

好消息是 `background.Config.Shell` 字段**已经存在**（`manager.go:66-69`，注释 "Shell overrides the shell used to execute jobs"）。坏消息是它只覆盖了这一处。

`DefaultUserShell()` 的现存调用点（排除 `.tmp-verify` 快照）：

| 位置 | 用途 | 是否受 `background.Config.Shell` 覆盖 |
| --- | --- | --- |
| `background/manager.go:3512` | 前台 job 实际执行 | ✅ 是（有兜底） |
| `background/detached.go:727` | detached runner 脚本与扩展名 | ❌ 否 |
| `background/interactive.go:714` | PTY / `write_stdin` 交互会话 | ❌ 否 |
| `toolkit/tools/bash.go:860/1036/1227/1569/2071/2114/2791` | 预检、元数据、`cmd` 特判 | ❌ 否 |
| `cmd/aicli/commands/command.go:1591` | `aicli exec` 路径 | ❌ 否 |
| `prompt/environment_context.go:86/113` | 提示词里的 shell 描述 | ❌ 否 |
| `remotetools/shell_runtime.go:23`（经 `resolveShell("")`） | remotetools 本地后端 | ❌ 否 |

### 16.2 对本方案的实质影响

**影响 1（最危险）：promoted 之后换了 shell。**
命令等待超时 → 转 detached runner（`detached.go`），而 runner 独立走 `DefaultUserShell()`。若前台因配置选中 `git-bash`、runner 却按 `DefaultUserShell()` 得到 `powershell`，**同一条命令在"没超时"和"超时转后台"两种情况下被两种语言解释**——引号、变量展开、路径分隔符全部改变，且失败模式极难复现。

**影响 2：提示词与实际执行漂移。**
`prompt/environment_context.go` 告诉模型"你在 PowerShell 上"，实际跑 bash（或反之）。这是文档 §6.1 方言注入要解决的核心问题，但前提是**所有调用点共用一次解析结果**。

**影响 3：预检与执行不一致。**
`bash.go:2071` 与 `command.go:1769` 用 `Type == ShellTypeCmd` 做分支判断；若解析点不同，预检按 cmd 走、执行按 bash 走。

**结论**：配置注入 `background.Config.Shell` 只是必要条件，不充分。必须在 §7 之上加一条硬性要求：

> **引入进程级单一 resolver。**`DefaultUserShell()` 降级为 resolver 的内部兜底实现，不再被业务代码直接调用。所有 §16.1 表中的调用点改为从注入的 resolver 取值。这是"改一处、到处一致"的唯一做法。

**Job Object 与 MSYS 的交互（需实测）**：`platformGuard` 在 `cmd.Start()` 之后才 attach，MSYS 进程被 assign 后其 fork 出的子进程默认继承 job，`TerminateJobObject` 理论上仍能整树杀掉——但这属于必须实测的项（附录 B 第 7 项），Cygwin/MSYS 的 fork 路径与 Job Object 在历史上有过兼容问题。

### 16.3 remote tools：零影响，但有两处值得顺手修

**结论：本地 `shell:` 配置对 envd 远端工具完全不生效，这是设计意图，不是缺陷。**

三处证据：

```go
// 1) shell 工具在 envd 后端硬编码 POSIX sh
// internal/remotetools/envd_process.go:111-117
be.client.Run(ctx, envdclient.StartRequest{
    Process: envdclient.ProcessConfig{
        Cmd:  "/bin/sh",
        Args: []string{"-c", command},
        Cwd:  cwd,
    },
})

// 2) ResolvedShell 对 envd 后端主动置零
// internal/remotetools/config.go:639-649
if c.Backend == BackendLocal {
    c.ResolvedShell = shell                    // 只有本地后端解析
} else {
    // envd executes on the target through /bin/sh; the local bridge's shell
    // selection must not leak into that backend's tool description.
    c.ResolvedShell = runtimeexecutor.Shell{}
}

// 3) envd 后端根本没有 job_* 工具
// internal/remotetools/server.go:100-109 → buildEnvDRegistry
```

推论：

| 问题 | 答案 |
| --- | --- |
| 远端会因为本方案换 shell 吗？ | 不会。远端本来就是 `/bin/sh -c`，已天然 POSIX |
| 远端有"自动转 job"吗？ | 没有。`buildEnvDRegistry` 只注册 file/edit/patch/process/pty，不含 `job_start/job_output/job_cancel/job_list` |
| 远端会受 Job Object 影响吗？ | 不会。远端进程不在本地 Job Object 里 |
| 本方案对远端有收益吗？ | 没有直接收益，也没有风险；远端已经不存在 PowerShell 兼容问题 |

**值得顺手修的两处：**

1. **envd 的方言提示走了 `default` 分支。**`remotetools/shell_runtime.go:139-153` 的 `shellSyntaxHint` 按 `Shell.Type` 分支；envd 后端 `ResolvedShell` 是零值（`Type == ""`），落到 `default: "Use the command syntax of the selected shell."`。而 envd 明确是 `/bin/sh`，应当返回 POSIX hint。这是低风险、明确收益的小修：让 envd 后端在 `shellSyntaxHint` 里显式按 `posix` 处理，而不是靠零值落到模糊提示。

2. **不要改 `DefaultUserShell()` 的优先级。**`remotetools/shell_runtime.go:20-31` 的 `resolveShell("")` 直接调用 `DefaultUserShell()`。若为了本方案把 `DefaultUserShell()` 的 Windows 优先级改成 git-bash 优先，remotetools 的**本地后端**会跟着静默改变 shell，而这条路径与 agent 主链无关、也没有被 §14 的验收覆盖。所以：

> `DefaultUserShell()` 保持今天的语义不变；新增独立的 `ResolveShell(cfg ShellConfig)` 供 agent 主链使用。两者互不影响，回滚互不牵连。

### 16.4 对 §7 落点清单的增补

| # | 文件 | 改动 |
| --- | --- | --- |
| 16 | `backend/internal/executor/shell_resolve.go` | 增加**进程级 resolver 单例**（`sync.Once` + 配置指纹失效），提供 `Resolve(cfg)` / `Current()`；`DefaultUserShell()` 保留为内部兜底 |
| 17 | `background/manager.go` / `detached.go` / `interactive.go` | 三处改为从 resolver 取值；`detached.go` 的 runner 扩展名与启动器按 resolver 结果决定（与 §6.4 合并实施） |
| 18 | `toolkit/tools/bash.go`（7 处）/ `cmd/aicli/commands/command.go:1591` / `prompt/environment_context.go:86,113` | 改为从 resolver 取值，杜绝"预检一种 shell、执行另一种 shell" |
| 19 | `remotetools/shell_runtime.go` `shellSyntaxHint` | 为 envd 后端补 POSIX 分支，不再落到 `default` |

**验收补充**（并入 §14 P0）：

| 场景 | 期望 |
| --- | --- |
| 配置 `prefer: [git-bash]` 后跑一条必然超时的命令 | 前台 bash、promoted 后的 detached runner 也是 bash；日志里两段输出的引号/变量行为一致 |
| 同一命令分别走前台与 `write_stdin` 交互会话 | 两者 shell 一致（`interactive.go` 已改用 resolver） |
| 提示词中的 shell 描述与实际执行 | 一致（`environment_context.go` 已改用 resolver） |
| envd 远端工具 | 行为零变化；`shellSyntaxHint` 从模糊提示变为 POSIX 提示 |

## 17. 实施记录（2026-11-04）

### 17.1 提交与范围

| 提交 | 范围 |
| --- | --- |
| `724a914d` | P0 + P1：`ShellConfig`/`ResolveShell`、进程级 resolver、哨兵探针与 WSL 桩守卫、7 类调用点收口、stdin 长脚本、`doctor shell`、TUI、Web API、前端 shell 域与 i18n |
| `07ba1b89` | P2：detached runner 按方言选型、Win7 显式候选链、bootstrap 边界修复、附录 B 实测用例与文档回填 |

### 17.2 与方案的一处实现差异（重要）

方案 §16.4 写的是 `sync.Once` 单例。实现改成了**带配置指纹的锁保护 memo**（`ConfigureShell` → 指纹变化则失效；`InvalidateShellResolution` 供写入路径主动失效）。原因：`runtime.yaml` 的 `shell:` 段在 Web/TUI 里可热更新，`sync.Once` 无法让一次保存立即生效。同时增加 `configured` 标志：**只有真实启动入口安装过配置才启用新探测链**，否则 `CurrentShell()` 仍返回 `DefaultUserShell()`（`Source=legacy`），保证库级调用方和既有单测的行为不被改变。

### 17.3 安装点边界（实测发现的缺陷与修复）

`ConfigureShell` 是进程级全局写入，只能出现在启动入口，不能出现在请求/会话路径的 helper 里。实测到的两类污染：

| 位置 | 现象 | 修复 |
| --- | --- | --- |
| `runtimeapi/handler.go` 的 `getBackgroundManager` | 每个 HTTP 请求都安装一次；同进程单测里 PowerShell guidance 断言拿到 bash | 删除，保留 `cmd/runtime-server/main.go` 的启动期安装 |
| `cmd/aicli/commands/chat_actor_host.go` 的 `buildLocalChatBackgroundManager` | 该函数被单测直接调用，装出的新方言泄漏到整个测试进程：`TestComposeLocalChatSystemPrompt_IncludesWorkspaceGuidance` 找不到 `Select-Object -First 200`，`cmd /c exit 0` 被 MSYS 改写 `/c` 导致进入交互模式 | 新增导出 `commands.InstallRuntimeShellResolver(cfg)`，只在 `cmd/aicli/main.go` 的 `PersistentPreRunE` 里调用；加 `TestBuildLocalChatBackgroundManagerDoesNotInstallGlobalShellResolver` 钉住约束 |

最终安装点只有三处：`cmd/aicli/main.go`、`cmd/runtime-server/main.go`、`config_tui_shell.go`（保存后立即生效，属用户显式动作）。

### 17.4 P2 detached runner 的实现要点

- 宿主判定与 runner 生成共用一次结果：`detachedRunnerHost()` 返回 `(shell, posixHost)`，`launchDetachedShell` 同时用它决定扩展名（`.sh` / `.ps1`）、脚本内容和启动进程，杜绝"生成 `.sh` 却交给 PowerShell"。
- Windows 上 `CurrentShell()` 是 POSIX 时还要 `os.Stat(shell.Path)` 且非目录，防止"配置里强制指向已卸载的 bash"生成永远起不来的 runner；失败则回落 `DefaultUserShell()` 的 `.ps1`。
- `writeUnixDetachedRunner` 在 Windows 上把所有嵌入路径统一 `filepath.ToSlash`（`cd` 是内建命令，不走 CreateProcess，`C:\...` 依赖 MSYS 转换器猜）。
- busybox-w32 的 `sh` 是 applet：`Prefix: ["sh"]` 既要进 runner 内部的 `DeriveExecArgs`，也要进**宿主进程**的 argv（`busybox.exe sh runner.sh`），见 `posixRunnerHostArgv`。
- `detachedRunnerHostAvailable()` 不再只认 PowerShell：存在任一磁盘上真实可用的 POSIX shell 即返回 true，覆盖 §7 里"PowerShell 被裁剪掉"的 Win7 工控机。

### 17.5 验证结果

- `go build ./...` 通过；`go vet` 在改动涉及的所有包通过（全仓仅剩既有 `internal/pty/pty_windows.go:162` 的 `unsafe.Pointer` 告警，与本方案无关）。
- 后端全量回归（`-count=1`）：`internal/background`、`internal/executor`、`internal/config`、`internal/agentconfig`、`internal/runtimeserver`、`internal/toolcore/shellexec`、`internal/prompt`、`internal/api/runtimeapi`、`internal/toolkit/tools`、`cmd/aicli/commands` 共 10 个包全部 PASS。
- 前端：`tsc -b`、i18n 一致性校验、eslint（新增文件）、`vitest run src/components/workspace/settings`（45 个测试文件 / 272 项）全部通过。
- `go run ./cmd/aicli doctor shell --json` 实测选中 `git-bash`（`C:\Program Files\Git\bin\bash.exe`，`5.2.21`，`posix-full`，探针 168 ms），候选矩阵完整。
- 附录 B 第 5/7/8 项已用端到端实测回填；第 2/3/4/6 项仍为未实测，按"未验证不得按已确认处理"保留标注。

---

## 附录 A：实测环境

```
OS:            Windows 11 26200
Git Bash:      2.55.0.windows.3 / bash 5.3.15 (x86_64-pc-cygwin) / MINGW64_NT-10.0-26200
PowerShell:    5.1 (System32\WindowsPowerShell\v1.0) / pwsh 7 (C:\Program Files\PowerShell\7)
路径探测:      where bash.exe → 4 条（System32 的 WSL 桩位于最前）
```

## 附录 B：本方案中标注为"待实测"的项（2026-11-04 回填）

| # | 项 | 状态 | 实测结论 / 未测原因 |
| --- | --- | --- | --- |
| 1 | `ApplyDocumentPathChange` 写入切片值（`shell.prefer`）的序列化形态与往返一致性 | ✅ 已实测 | `TestApplyDocumentPathChangeWritesShellSliceValue` 确认 slice 序列化为块序列且往返一致。**同时发现真实缺陷**：`applyDocumentChanges` 把层文件解码成 `map[string]interface{}` 后整体 `yaml.Marshal`，必然丢失注释与键顺序。因此配置写入改走 `runtimeserver.persistShellSettings` 的 `yaml.Node` 单键 upsert，并由 `TestPersistShellSettingsPreservesCommentsAndOrder` 固化。 |
| 2 | busybox-w32 是否启用 `FEATURE_SH_STANDALONE`（决定是否需 `--install -s`） | ❌ 未实测 | 参考机未安装 busybox-w32。**不据此下结论**：实现里按 applet 语义固定 `Prefix: ["sh"]`，并用 `TestPosixRunnerHostArgvKeepsShellPrefix` 覆盖 argv 形状；是否需 standalone 安装仍待实机确认。 |
| 3 | `busybox sed -i` 与 GNU `sed -i` 的备份后缀差异 | ❌ 未实测 | 同上，缺样本。文档与提示词不宣称两者等价；`posix-narrow` 方言只禁用 bash 扩展，不声称工具链完全一致。 |
| 4 | Win7 实机上 Git Bash 2.46.2 与 busybox 并存时的候选排序 | ❌ 未实测 | 无 Win7 实机。`runtime.win7.yaml` 已给出显式链 `git-bash → busybox → msys-bash → powershell → cmd`，但顺序需在实机复验；配置文件里的排序是**设计意图**，不是实测结论。 |
| 5 | `.sh` runner 在 Windows 下的 `kill -0 $$` 心跳可用性 | ✅ 已实测 | `TestLaunchDetachedShellWithPosixHost` 在 Win11 + Git Bash 上真实跑完 detached 生命周期：runner 写出可被 `strconv.Atoi` 解析的数字心跳时间戳，并把退出码 `7` 回写 status 文件。`TestWriteDetachedRunnerShContentForPosixHost` 另固化脚本内容。 |
| 6 | `powershell -Command -` / `pwsh -Command -` 读取多行 stdin 脚本 | ❌ 未实测 | 按方案保持保守缺省：PowerShell 长脚本仍走 `-EncodedCommand`，`SupportsStdinScript()` 只对 POSIX 返回 true。切换前必须单独实测 `$LASTEXITCODE` 与 `WrapPowerShellExitCode` 尾声的配合。 |
| 7 | **MSYS bash 在 Windows Job Object 内的行为** | ✅ 已实测 | 新增 `TestMSYSBashDescendantsAreInsideWindowsJobObject`（Windows-only）：让 Git Bash fork 一个持续写文件的子进程，用 `QueryInformationJobObject(JobObjectBasicProcessIdList)` 直接观察到 job 内 ≥2 个 PID；取消后 `TerminationReport.Mode == "job_object"`、`TreeKill == true`、`Killed >= 2`，且 liveness 文件停止增长 → **fork 出来的子进程确实继承 job，`TerminateJobObject` 一次杀净，未观察到孤儿残留**。历史上的 Cygwin/MSYS 兼容顾虑在本机 Git Bash 5.2.21 上未复现。 |
| 8 | **bash 场景下 `CREATE_BREAKAWAY_FROM_JOB` 逃生与 `taskkill /T` 收尾** | ⚠️ 部分实测 | 已实测：`startDetachedProcess(bash.exe, [runner.sh])` 能启动并完成 detached 生命周期（第 5 项）；`TestDetachedPosixRunnerTerminateKillsWholeTree` 验证 `taskkill /T /F` 后 MSYS 后代停止写文件，无孤儿。**注意**：该场景里 runner 由 manager 进程直接启动，并不位于 per-command job 内，因此 `JOB_OBJECT_LIMIT_BREAKAWAY_OK` 的"从 per-command job 脱离"这一半**未构造场景验证**；`startDetachedProcess` 本身也未设置 `CREATE_BREAKAWAY_FROM_JOB`。另实测到 `taskkill /T /F` 在 MSYS 树上可能返回 `exit status 255`，但重试即报"没有找到进程"（树已被杀净），所以生产侧以 `waitProcessGone` 为权威判定，不采信退出码。 |

> 结论口径：标 ✅ 的项有可执行测试作为证据；标 ⚠️ 的项只覆盖了一半场景；标 ❌ 的项仍是开放问题，不得在文档、提示词或缺省配置里按已确认处理。
