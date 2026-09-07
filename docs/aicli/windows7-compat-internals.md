# Windows 7 兼容降级机制内部说明（win7compat）

> 本文面向需要理解或维护 Win7 兼容构建实现细节的开发者，系统性盘点
> 仓库中所有针对 Windows 7 的兼容处理及其工作原理。
>
> 面向最终用户的安装/使用指南见 [windows7.md](windows7.md)；面向开发者的
> 编译/打包/CI 流程见 [windows7-build.md](windows7-build.md)。本文聚焦
> **代码层面的兼容机制**：构建隔离、Go 版本 Polyfill、配置/路径、功能裁剪、
> 控制台终端运行时、服务控制与 SQLite 兼容。

## 1. 总览

Win7 兼容不是单一开关，而是贯穿 **构建 → 依赖 → 配置 → 功能 → 终端运行时 →
服务控制** 六个层次的一整套降级策略。核心约束来自两个硬事实：

1. **工具链**：Go 1.21 是官方支持 Windows 7 的最后一个版本，且 Go 1.21.5+
   因 `GetSystemTimePreciseAsFileTime` 回归（golang/go#64622）在 Win7 上无法
   启动，因此 Win7 构建**冻结在 Go 1.21.4**。
2. **终端**：Win7 conhost **不支持** `ENABLE_VIRTUAL_TERMINAL_PROCESSING`
   （VT）与现代 ConPTY（Win10 1903+），交互式终端只能走经典 Win32 控制台
   API（`ReadConsoleW` / `ReadConsoleInputW` / `WriteConsoleW`）。

次要约束包括：部分依赖要求 go≥1.23（MCP SDK、jsonschema-go）无法在 Go
1.21 下编译；`CGO_ENABLED=0` 禁用外部链接；裁剪版 Win7 工控机可能没有
PowerShell。

代码隔离依赖 `//go:build win7compat`（兼容实现）与 `//go:build !win7compat`
（主线实现）**成对出现**。二者都只影响 Win7 目标，主线（go 1.25）构建
不受影响。Go 1.20 时代的版本限定 Polyfill 文件（`//go:build go1.20 && !go1.21`）
已随工具链冻结在 Go 1.21.4 全部删除（min/max 为语言内建，context 系列为
标准库自带，见 §3）。

## 2. 构建与工具链层

统一构建入口为 `scripts/build.ps1`，`-Target win7` 时启用整套降级参数
（三个 `build-*-win7.ps1` 只是转发 wrapper）：

| 机制 | win7 值 | 说明 |
| --- | --- | --- |
| Go 工具链 | `GOTOOLCHAIN=go1.21.4` | 最后支持 Win7 的 Go 1.21 补丁版本 |
| 依赖图 | `GOFLAGS=-modfile=go.win7.mod`（+`go.win7.sum`） | 与主 `go.mod`（go 1.25.0）隔离，依赖版本整体回退 |
| CGO | `CGO_ENABLED=0` | 纯 Go 构建，无外部链接依赖 |
| build tag | `-tags win7compat` | 切换全部兼容/裁剪实现 |
| 平台 | `windows/amd64` | 仅 64 位，不提供 386 |
| 产物 | `<tool>-win7.exe` + `.sha256` | 与主线 `<tool>.exe` 区分 |

依赖回退（`go.win7.mod` vs `go.mod`）关键项：

- 移除：`modelcontextprotocol/go-sdk`（要求 go≥1.23 → Win7 构建整体禁用 MCP）、
  `google/jsonschema-go`（要求 go≥1.23 → 换 `santhosh-tekuri/jsonschema/v5`）
- 降级：`ncruces/go-sqlite3` 0.32 → **0.22.0**、`pkg/sftp` 1.13.11 → 1.13.5、
  `x/crypto` 0.55 → 0.33、`x/sys` 0.47 → 0.30、`wazero` 1.11 → 1.8.2、
  `chroma` 2.20 → 2.15 等

> **注意**：`go.win7.mod` 必须用 `go mod tidy -modfile=go.win7.mod` 独立维护，
> 不能直接 `go mod tidy`（会误改标准依赖图）。

## 3. Go 语言版本兼容层

Win7 工具链 Go 1.21.4 已包含 Go 1.21 的全部语言与标准库能力：`min`/`max`
为语言内建，`context.WithoutCancel` / `context.WithTimeoutCause` /
`context.Cause` 为标准库函数。Go 1.20 时代的 Polyfill 文件（6 个
`compat_go120.go`、`internal/agent/compat_context.go` 手工实现、
`internal/api/skills` 的 `builtinMin/builtinMax` 与 `withoutCancel` 包装）
因此**全部删除**，双工具链共用同一份实现，无行为分叉。保留的项如下：

| 保留项 | 现状 | build tag |
| --- | --- | --- |
| `internal/agent/compat_context.go` | `agentWithoutCancel`/`agentWithTimeoutCause`/`agentContextCause` 直接委托标准库（由原 `compat_context_go121.go` 合并而来，行为测试保留） | 无 tag |
| `internal/team/task_execution_context.go` | `DetachedTaskExecutionContext` 直接调 `context.WithoutCancel`（保留 nil→Background 防护） | 无 tag |
| `internal/toolschema/validate_go120.go` | 使用 `santhosh-tekuri/jsonschema/v5`（`jsonschema-go` 需 go≥1.23）；`validate.go` 为对侧 | `win7compat`（已去掉多余的 go1.20 分支） |

> 删除 Polyfill 后，受影响的 6 个包内 `min`/`max` 调用点直接解析为语言
> 内建；`internal/api/skills` 的 `builtinMin(parsed, 1000)` 改为内建
> `min(parsed, 1000)`、`withoutCancel(requestCtx)` 改为
> `context.WithoutCancel(requestCtx)`。

## 4. 配置与路径层

`internal/aiclipaths/` 用 build profile 提供两类文件名集合：

- `profile_standard.go`（`!win7compat`，buildProfile=`main`）
- `profile_win7.go`（`win7compat`，buildProfile=`win7`）

**用户可见配置与主线完全统一**（这是 `ae4141ab` 起的有意设计，避免 UI 写回
`config.yaml` 而 Win7 版读 `config.win7.yaml` 造成读写不一致）：

| 配置项 | 主线（main） | Win7（win7compat） |
| --- | --- | --- |
| 用户 CLI 配置 | `config.yaml` / `aicli.yaml` | **`config.yaml` / `aicli.yaml`（共享）** |
| 运行时配置 | `runtime.yaml` | `runtime.win7.yaml`（专属名） |
| 会话库默认名 | `session_history.sqlite` | `session_history.sqlite`（profile 默认值） |

配套逻辑：

- `internal/agentconfig/bootstrap.go`：配置搜索顺序由 profile 名驱动，win7
  专属名优先、标准名 `config.yaml` 兜底，使 Win7 二进制能发现标准布局配置。
- `cmd/runtime-server/main.go` `runtimeServerConfigSearchNames()`：同样做
  专属名优先 + 标准名 fallback；`cmd/runtime-server/config_path_win7_test.go`
  验证 Win7 构建**只**搜索标准 `config.yaml`，且 legacy `config.win7.yaml`
  文件不会覆盖标准配置。
- 会话库默认名虽然统一为 `session_history.sqlite`，但 Win7 运行时配置
  `runtime.win7.yaml` 显式覆盖为 Win7 专属库（见第 8 节）。

## 5. 功能裁剪层（MCP / JSON Schema）

`win7compat` build tag 下，依赖 go≥1.23 的库被整体裁剪为**空壳实现**，
但保留与主线一致的导出面，调用方（chat 工具注册、skills 集成等）无需改动：

| 被裁剪功能 | 空壳/替代 | 机制 |
| --- | --- | --- |
| MCP 注册表 | `internal/mcp/registry/registry_win7compat.go` | `Registry` 空壳 + 简化 `CanonicalToolName`（不截断长名；注释注明差异仅在 MCP 不可达路径上无影响） |
| MCP 管理器 | `internal/mcp/manager/manager_win7compat.go` | Manager 接口形状完整保留，方法全部空实现或返回 `"MCP is not supported in the Windows 7 compatible build"` |
| MCP 适配器 | `internal/skill/mcp_adapter_win7compat.go` | `MCPAdapter` 全方法 stub（FindTool/CallTool/ListTools…） |
| MCP transport/server | `internal/mcp/transport/websocket.go` 等 | `//go:build !win7compat` 直接排除 |
| JSON Schema 编译器 | `internal/toolschema/validate_go120.go` | 换用 `santhosh-tekuri/jsonschema/v5`（唯一兼容 Go 1.21.4 的维护中编译器），保持"拒绝外部引用"等行为一致 |

> MCP 在 Win7 兼容构建中**整体禁用**；需要 MCP 时应在受支持的新系统上运行。

## 6. 控制台与终端运行时层（核心）

Win7 conhost 不支持 VT/ANSI，交互式终端必须走经典 Win32 控制台 API。这是
兼容处理最密集的领域，分**输出**与**输入**两条线，另有渲染/surface 降级与
原生 Console 启动器。

### 6.1 输出：UTF-8 代码页切换

Go 程序始终以 UTF-8 字节写 stdout，而 Win7 conhost 默认按 OEM 代码页
（中文系统为 936/GBK）解码，无 VT 时中文输出会乱码。解决：

- `internal/winconsole/console_utf8_windows.go` `EnsureConsoleUTF8Output()`
- `cmd/aicli/ui/terminal_driver_windows.go` `platformEnsureConsoleUTF8Output()`

逻辑：`GetConsoleMode` 检测无 `ENABLE_VIRTUAL_TERMINAL_PROCESSING` → 调用
`SetConsoleOutputCP(65001)`，返回恢复函数供 defer 在退出时还原原代码页
（避免污染同一 console 上后续命令的显示）。支持 VT 的控制台、管道/文件
重定向、非 Windows 平台均为空操作。配套的
`platformTerminalSupportsANSI()` / `platformEnableVirtualTerminalProcessing()`
用于探测并尝试开启 VT。

### 6.2 输入：legacy 控制台行读取（三档策略）

`cmd/aicli/commands/chat_legacy_console_line.go` 定义
`chatConsoleLineInputMode`（disabled/auto/system/custom），由
`--compat-mode` 强制进入、`--input-mode auto|system|custom` 选择：

- **system**（`chat_system_console_editor_windows.go`）：直接调 `ReadConsoleW`
  原生 cooked 路径，保留 `ENABLE_LINE_INPUT` / `ENABLE_ECHO_INPUT`，让 conhost
  承担 **IME 组合、候选词与提交**（Win7 中文输入法关键路径）。
- **custom**（`chat_legacy_console_editor_windows.go`）：`ReadConsoleInputW`
  直接读按键记录（VK 码 + UnicodeChar，与输出代码页无关）+ `WriteConsoleW`
  （UTF-16 渲染，避开 65001 字节解码问题）+ `SetConsoleCursorPosition`。
  解决两个传统 conhost 行编辑问题：UTF-8 代码页下中文按字节退格成乱码；
  Delete 键（VK_DELETE）不进入行缓冲。还处理老 conhost 把物理 Backspace
  翻译成 VK_LEFT 的兼容（用 scan code `0x0E` 救回）。
- **auto**（默认）：优先 system，系统 Unicode 行读取不可用时回退 custom，
  再回退 buffered。
- `chat_legacy_console_editor_other.go`：非 Windows 平台直接 buffered 回退。

### 6.3 渲染 / surface 降级

无 ANSI → 无 TUI：

- `--compat-mode` 不走 TUI，用原生控制台行输入。
- `chat_surface_output.go`：无 surface（Win7 无 VT / headless / 后台服务）
  时用户输入仍注入 Scene 数据面，`/web/api/screen` 的 messages 不缺失
  role=user 条目（web 客户端可渲染用户 prompt）。
- `chat_debug_screen_http.go`：Win7 降级形态下 `/web/api/screen` 内容源四级
  优先级：surface → bridge Scene → 会话 transcript 兜底。

### 6.4 原生 Console 启动器（aicli-console）

`cmd/aicli-console/main.go` + `internal/consolehost/consolehost_windows.go`：
从 pipe-backed 终端（MobaXterm/mintty/Cygwin）启动时 `CREATE_NEW_CONSOLE`
创建真 conhost 窗口。关键实现细节：

- **故意绕过 `os/exec`**，直接 `CreateProcessW`：Go 的 os.StartProcess 总会
  设置 `STARTF_USESTDHANDLES`，把 MobaXterm 的 pipe（或 NUL）传给子进程而
  使 `CREATE_NEW_CONSOLE` 失效；置空 `STARTF_USESTDHANDLES` 让 Windows 从新
  console 的 `CONIN$`/`CONOUT$` 初始化标准句柄（注释明确 "including on
  Windows 7"）。
- `platformHasConsole()`：stdin 与 stdout 均为 console handle 时判定为原生
  控制台（此时保留当前 console，不新建）。
- 从 `cmd.exe` / PowerShell（真 console）启动时保持当前 console。

## 7. 服务控制与进程层（面向裁剪版 Win7）

针对无 PowerShell 或 PowerShell 被裁剪/禁用的 Win7 工控机，进程控制全部改为
Win32 API 直连：

- `internal/runtimeserver/service_control.go`：进程存活探测改用 `OpenProcess`
  （此前用 `powershell.exe -Command "if (Get-Process ...)"`，在裁剪 Win7 上
  恒失败，导致 `start` 在 serve 已正常写入 PID 文件时仍空转 30s 误报）。
- `processImagePath`：Windows 用 `QueryFullProcessImageNameW`（Vista+，Win7
  可用，`PROCESS_QUERY_LIMITED_INFORMATION` 权限）。
- stop 身份核验 `looksLikeRuntimeServerProcess`：识别
  `runtime-server-win7-*.exe` 等命名，端口被非服务进程接管时拒绝自动终止。
- `internal/agentcontrol/registry_service.go`：SQLite 共享连接池注释专门提到
  "Win7-compatible SQLite driver" 同进程多连接池协调。

## 8. 会话与 SQLite 兼容（read-replica）

Win7 构建使用 `ncruces/go-sqlite3 v0.22.0`（纯 Go + wazero，CGO=0）——
与主线的 v0.32.0 **同一驱动主版本线**。历史教训（commit `18395eb9`）：旧
Win7 构建曾用 Go 1.20.14 + go-sqlite3 v0.8.3，其 no-shm WAL 实现把写入者
串行化到单一 OS 锁，第二个 aicli 实例访问同一会话库即
`database is locked` 死锁；**v0.22.0 增加 proper shared memory
（`shm_windows.go`），使 WAL 多进程并发在 Win7 上可用**。

会话库采用 **read-replica 模式**（`backend/configs/runtime.win7.yaml`）：

```yaml
sessions:
  backend: sqlite
  storePath: session_history_win7_replica.sqlite   # runtime-server 读的私有副本
  replicaSource: session_history_win7.sqlite       # master（aicli 持有 WAL/-shm 锁）
  replicaSyncInterval: 30s                          # 每 30s 从 master 同步并热切换
```

机制：`aicli` 写 master（`session_history_win7.sqlite`）并持有其 `-wal`/`-shm`
锁；`runtime-server` 直接读 master 会被锁阻塞（`会话存储查询超时`），因此改为
读每 30s 从 master 同步的私有副本并热切换。`cmd/runtime-server/main.go` 中
`ReplicaSource` 为空时回退到 `aiclipaths.DefaultSessionHistoryFileName`。
主线 `runtime.yaml` 使用同一模式（`session_history.sqlite` / `_replica`）。

## 9. 构建验证与测试矩阵

`scripts/build.ps1` 在 win7 target 下用 `-tags win7compat -mod=readonly`
运行专项兼容测试（默认启测试时）：

- Win7 配置与运行时测试：`./internal/chat ./cmd/runtime-server` 等
  `commonSuite`
- agent context 兼容测试：`./internal/agent` 定向
  `TestAgentWithoutCancel|TestAgentWithTimeoutCause|TestComputeAvailableToolsDoesNotExposePolicyDeniedSpawnSubagents`
- aicli 配置测试：`./cmd/aicli/commands` 定向
  `Test(GetMCPConfigPath|ResolveGlobalRuntimeConfigPath|RunInitCommand|InitCommandHelp)`
- **交叉编译 PE 校验**：控制台兼容测试
  `Test(ReadChatSessionLine|ParseChatCommandOptionsConsoleInputMode|DecodeSystemConsoleLine|ReadSystemConsoleUTF16Line|LegacyConsoleDispatchAcceptsCommittedIMEProcessCharacter)`
  以 `-c` 交叉编译出 windows/amd64 测试二进制并 `Test-PEExecutable` 校验
  （宿主非 Win7 时只能交叉编译验证）
- 原生 Console 启动器测试：`./cmd/aicli-console`
- winconsole 包测试：`./internal/winconsole/...`（ssh-*/sftp 工具组）
- ssh-keygen 测试：`./cmd/ssh-keygen`

开发约束（`windows7-build.md` §6 相同）：修改 Win7 配置、路径、console、
session、SQLite、依赖或 workflow 时，必须同时通过标准构建和 Win7 构建的验证。

## 10. 已知不一致与维护注意事项

1. **会话库默认名 vs 显式配置**：`internal/aiclipaths/profile_win7.go` 将
   `defaultSessionHistoryFileName` 设为 `session_history.sqlite`（注释称"统一
   使用主库，使前端 Web 控制台按工作目录分组展示所有会话"），但
   `backend/configs/runtime.win7.yaml` 显式覆盖为
   `session_history_win7.sqlite` / `_replica.sqlite`（隔离 + read-replica）。
   实际生效以**显式配置**（`runtime.win7.yaml`）为准；两处描述存在出入，维护
   会话库或 Web 控制台会话分组时应先对齐意图。
2. **`go.win7.mod` 必须独立维护**（`go mod tidy -modfile=go.win7.mod`），
   禁止不带 `-modfile` 直接 tidy。
3. **`-tags win7compat` 不可遗漏**：漏掉时 `profile_win7.go` 等兼容实现不参与
   编译，产物实为标准实现，Win7 上启动即 `0xc0000005 PC=0x0`。
4. MCP 在 Win7 构建中为预期缺失（非 bug），见第 5 节。

## 附录：关键文件清单

| 层 | 文件 |
| --- | --- |
| 构建 | `scripts/build.ps1`、`scripts/build-{aicli,runtime-server,ssh-sftp-clients}-win7.ps1`、`backend/go.win7.mod`/`.sum`、`.github/workflows/build-aicli-win7.yml` |
| Go 兼容（1.20 polyfill 已随 1.21.4 清理） | `internal/agent/compat_context.go`（stdlib 委托）、`internal/team/task_execution_context.go`、`internal/toolschema/validate_go120.go`（`win7compat` tag） |
| 配置/路径 | `internal/aiclipaths/profile_{win7,standard}.go`、`internal/agentconfig/bootstrap.go`、`cmd/runtime-server/main.go`、`backend/configs/runtime.win7.yaml` |
| MCP 裁剪 | `internal/mcp/{registry/registry_win7compat.go, manager/manager_win7compat.go, transport/*}`、`internal/skill/mcp_adapter_win7compat.go` |
| 控制台 | `internal/winconsole/console_utf8_windows.go`、`cmd/aicli/ui/terminal_driver_windows.go`、`cmd/aicli/commands/chat_legacy_console_{line,editor_windows}.go`、`chat_system_console_editor_windows.go`、`cmd/aicli-console/main.go`、`internal/consolehost/consolehost_windows.go` |
| 渲染降级 | `cmd/aicli/commands/chat_surface_output.go`、`chat_debug_screen_http.go` |
| 服务控制 | `internal/runtimeserver/service_control.go`、`internal/agentcontrol/registry_service.go` |
