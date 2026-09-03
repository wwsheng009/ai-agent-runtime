# 在 Windows 7 上使用 aicli

Windows 7 必须使用单独的 **Win7 amd64 兼容包**。普通 Windows Release
由 Go 1.24 编译，而 Go 1.21 起已不再支持 Windows 7；普通包可能在启动时
直接崩溃（常见为 `Exception 0xc0000005 PC=0x0`）。

## 1. 前提

- Windows 7 SP1 64 位（当前不提供 32 位构建）。
- 建议安装 Windows 7 最后的 SHA-2、根证书和 TLS 相关更新。
- Provider 必须能从该机器访问；企业代理需要同时配置系统或进程代理环境变量。
- 不需要安装 Go、Node.js、MSYS2 或 `winpty`。兼容包使用 Go 1.21.4、
  `CGO_ENABLED=0` 构建。

## 2. 下载和安装

从项目 Release 下载名字包含以下片段的压缩包：

```text
aicli-win7-<版本>-windows-amd64.zip
```

不要下载普通的 `aicli-<版本>-windows-amd64.zip` 代替它。

解压到不含特殊权限限制的目录，例如：

```text
C:\Tools\aicli\
  aicli.exe
  aicli-console.exe
  runtime-server.exe
  configs\
    runtime.win7.yaml
```

`runtime-server.exe` 是 Win7 兼容的 runtime server（Go 1.21.4 构建，使用
`--runtime-server` 远程模式或独立启动时使用；本地默认 `local` runtime
不需要它）。

先在 `cmd.exe` 中验证：

```bat
cd /d C:\Tools\aicli
aicli.exe version
```

可以通过“控制面板 → 系统 → 高级系统设置 → 环境变量”把
`C:\Tools\aicli` 加到用户 `PATH`。Windows 7 的旧版 `setx` 可能截断过长
的 `PATH`，因此不建议盲目执行 `setx PATH "%PATH%;..."`。

## 3. 首次配置

在原生 `cmd.exe` 中执行：

```bat
cd /d C:\Tools\aicli
aicli.exe init --global
aicli.exe login --provider openai --protocol openai --base-url https://api.openai.com --set-default
aicli.exe provider list
aicli.exe doctor provider
```

`login` 缺少 `--api-key` 时会交互式提示输入，避免把密钥直接留在命令历史。
也可以只在当前窗口设置环境变量后再登录：

```bat
set OPENAI_API_KEY=sk-替换为实际密钥
aicli.exe login --provider openai --protocol openai --base-url https://api.openai.com --api-key "%OPENAI_API_KEY%" --set-default
set OPENAI_API_KEY=
```

默认配置保存在：

```text
%USERPROFILE%\.aicli\config.win7.yaml
```

Win7 兼容版不会默认读取普通版的 `config.yaml`。兼容包内还包含独立的
runtime 配置：

```text
C:\Tools\aicli\configs\runtime.win7.yaml
```

该配置以及 Win7 分支的代码级回退值都会把会话数据库设为：

```text
%USERPROFILE%\.aicli\sessions\session_history_win7.sqlite
```

普通版继续使用
`%USERPROFILE%\.aicli\sessions\session_history.sqlite`。两者的 `-wal` /
`-shm` 文件也因此完全分离，避免多进程 WAL 锁竞争：`aicli` 持有主库的
`-wal`/`-shm` 写锁，`runtime-server` 改为读取每 30 秒从主库刷新的只读
副本（`session_history_win7_replica.sqlite`，配置见
`configs\runtime.win7.yaml`），两者并发运行互不阻塞。代价是两个版本的
会话历史默认互不可见。

如果要把普通版历史一次性复制给 Win7 版，必须先正常停止所有正在访问原
数据库的 `aicli` 和 `runtime-server` 进程；不要在 WAL 写入期间只复制主
`.sqlite` 文件。更安全的方式是使用应用的导出/导入能力或 SQLite
backup/snapshot。

首次看到 `.env file not found` 警告不影响使用；`.env` 是可选项。

## 4. 推荐启动方式

### 原生 cmd.exe

Windows 7 的 conhost 不支持现代 VT/ConPTY。最稳妥的交互方式是：

```bat
aicli.exe chat --compat-mode
```

`--compat-mode` 默认等价于 `--input-mode auto`：先使用 Windows 原生
`ReadConsoleW` cooked 输入，让 conhost/中文输入法负责拼音组合、候选词选择和
提交，再以 UTF-16 接收最终文本；这条路径不依赖当前控制台代码页。只有系统
Unicode 行输入不可用时才回退到程序自带的逐键编辑器。

也可以显式指定：

```bat
rem 推荐：保留 Win7 中文 IME 组合和候选窗
aicli.exe chat --compat-mode --input-mode system

rem 仅在系统输入路径异常、且主要需要 Backspace/Delete 时尝试
aicli.exe chat --compat-mode --input-mode custom
```

`custom` 模式直接消费 `ReadConsoleInputW` 键盘事件，适合作为编辑键兼容回退；
它无法保证所有 Win7 第三方输入法的组合态和候选窗行为，因此中文输入优先使用
默认的 `auto` 或显式 `system`。

如果确认当前终端输入、退格和中文显示都正常，也可以直接运行：

```bat
aicli.exe
```

完全避开交互式编辑器的单次调用：

```bat
aicli.exe chat --no-interactive --prompt "请介绍一下你自己"
```

### MobaXterm、mintty 或 Git Bash

这些终端通常把原生 Windows 程序连接到 pipe，而不是 Win7 Console。
推荐运行同目录的启动器，它会新建原生 conhost 窗口：

```text
./aicli-console.exe chat --compat-mode
```

如果主程序已改名，使用启动器专用的 `--target` 指定可执行文件：

```bat
aicli-console.exe --target "C:\Tools\aicli\aicli-win7.exe" chat --compat-mode
```

也支持等号形式：

```bat
aicli-console.exe --target="C:\Tools\aicli\aicli-win7.exe" version
```

`--target` 只由启动器解析，不会传给主程序；其他参数保持原顺序转发。
目标查找优先级为：

1. 命令行 `--target`
2. 环境变量 `AICLI_CONSOLE_TARGET`
3. 启动器同目录的 `aicli.exe`
4. `PATH` 中的 `aicli.exe`

相对目标路径以当前工作目录为基准，因此长期配置推荐使用绝对路径。如果必须
把字面量 `--target` 传给主程序，可将它放在参数分隔符 `--` 之后。

等价方式：

```text
./aicli.exe --console-host chat --compat-mode
```

如果必须留在当前终端标签页，并且终端环境已自带完整的 winpty，可尝试：

```text
winpty ./aicli.exe chat --compat-mode
```

不要只复制一个 `winpty.exe`。它通常还需要同版本的 `winpty-agent.exe`、
`winpty.dll`，MSYS2 构建还依赖 `msys-2.0.dll`。Git for Windows 自带的
`winpty` 应直接从 Git Bash 使用；原生 `cmd.exe` 不需要它。

## 5. 功能范围

- chat、provider 登录、session、常用本地工具可使用。
- 默认 `local` runtime 不需要另行启动 `runtime-server.exe`。
- `runtime-server.exe` 提供 Web API（`/healthz`、`/api/...`）与 command API；
  Win7 客户端 `aicli.exe chat --runtime-server <host:port>` 可连接本机或
  其他机器上的 server。win7 兼容包未内嵌完整前端页面（`win7compat`
  构建使用占位 `dist/`），`/` 返回运行时信息，功能通过 API 与 aicli 使用。
- Win7 runtime server 默认查找 `config.win7.yaml`，并使用
  `configs\runtime.win7.yaml`。若要与普通版 server 同机并行运行，还应使用
  不同端口和 PID 文件，例如：

  ```bat
  runtime-server.exe start --listen 127.0.0.1:8102 --pid-file logs\runtime-server-win7.pid
  ```

- `win7compat` 构建当前禁用 MCP 集成；需要 MCP 时应在受支持的新系统上运行。
- 搜索工具找不到外部 `rg.exe` 时会回退到内置扫描器。若自行提供 ripgrep，
  也必须确认该 ripgrep 版本能在 Windows 7 上启动。

## 6. 已知限制

Win7 兼容包在功能、性能与更新节奏上有以下明确限制：

| 限制 | 说明 |
|---|---|
| 工具链冻结 | 固定使用 **Go 1.21.4** 编译。Go 1.21 是官方支持 Windows 7 的最后一个版本，且 Go 1.21.5+ 因 `GetSystemTimePreciseAsFileTime` 回归在 Win7 上无法启动（golang/go#64622），因此不能跟随主线升级 Go 版本 |
| 依赖冻结 | 依赖图独立维护在 `go.win7.mod`，例如 JSON Schema 校验使用 `santhosh-tekuri/jsonschema/v5`（`google/jsonschema-go` 要求 go ≥ 1.23，Win7 构建不可用）；SQLite 驱动为 `ncruces/go-sqlite3 v0.22.0`（纯 Go + wasm，`CGO_ENABLED=0`） |
| 仅 amd64 | 当前不提供 32 位（386）构建，也未提供 Linux/macOS 的 Win7 等价物 |
| 功能裁剪 | `win7compat` build tag 裁剪了要求更高 Go 版本的特性，主要是 **MCP 集成**；需要 MCP 时请在受支持的新系统上运行 |
| Web UI 受限 | win7 兼容包未内嵌完整前端页面（占位 `dist/`）；`runtime-server` 的 `/` 返回运行时信息，功能通过 Web API 与命令行使用 |
| 会话历史隔离 | Win7 版使用独立的会话数据库（`session_history_win7.sqlite`），与普通版（`session_history.sqlite`）默认互不可见；`runtime-server` 读取 30 秒刷新的只读副本，改动不会立即出现在副本中 |
| 无自动更新 | Win7 版只随 `win7-*` tag 发布，不会跟随主线 `v*` 发布自动更新；需手动下载新 Release 覆盖 |
| 终端兼容 | Win7 conhost 不支持现代 VT/ConPTY；交互式输入依赖 `--compat-mode`（`ReadConsoleW`/`ReadConsoleInputW` 路径），第三方终端（MobaXterm、mintty、Git Bash）需配合 `aicli-console.exe` 启动器 |
| 系统依赖 | 依赖 Win7 SP1 及最后的 SHA-2/根证书/TLS 更新；未打补丁的系统可能出现 x509/TLS 连接失败 |
| 性能 | wasm 版 SQLite 与内置扫描器（无外部 `rg.exe` 时）性能低于现代系统；大数据量任务建议在受支持的系统上运行 |

## 7. 常见故障

| 现象 | 处理 |
|---|---|
| 启动即 `0xc0000005 PC=0x0` | 下载了普通 Go 1.24 Windows 包；改用文件名含 `win7` 的兼容包 |
| “不是有效的 Win32 应用程序” | 确认系统是 64 位，并使用 `windows-amd64` 包 |
| 退格、Delete 或方向键异常 | 改用原生 `cmd.exe`，先用 `chat --compat-mode`；若系统行编辑仍异常，再试 `--input-mode custom`；在 mintty/MobaXterm 中用 `aicli-console.exe` |
| 中文输入法没有组合态/候选窗，或中文无法提交 | 使用 `chat --compat-mode --input-mode system`（默认 `auto` 已优先走该路径）；不要强制 `custom` |
| 中文输出乱码 | 优先使用 `aicli-console.exe`，并选择支持中文的控制台字体；`chcp 65001` 只影响字节流输入输出，不是 `ReadConsoleW` 中文输入的前提 |
| 主程序已改名或提示找不到 `aicli.exe` | 使用 `aicli-console.exe --target "主程序完整路径" ...`，或设置 `AICLI_CONSOLE_TARGET` |
| `x509`、TLS 或连接失败 | 更新根证书和系统补丁，检查系统时间、代理、防火墙及 Provider 地址 |
| 找不到配置或 provider 为空 | 执行 `aicli.exe init --global`，再执行 `login`；用 `aicli.exe config --no-tui` 查看实际配置路径 |

日志默认位于：

```text
%USERPROFILE%\.aicli\logs\aicli.log
```
