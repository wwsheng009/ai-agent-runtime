# aicli × Windows `.cmd` 垫片 MCP 兼容性缺陷分析与修复方案

- 文档日期：2026-09-21
- 影响组件：aicli（`E:\projects\ai-agent-runtime`，本次实测版本 `aicli version dev`）MCP stdio 客户端
- 触发环境：Windows 10 22H2（10.0.19045）+ `command: npx`（`.cmd` 垫片）+ argv 中含空格的参数
- 主要受害者示例：`chrome-devtools-mcp@1.9.0` 的 `--autoConnect --userDataDir "C:\...\Edge\User Data"` 官方推荐用法
- 结论：**是 aicli 侧的真实兼容性缺陷（P0，需改代码）**；另有 1 项诊断可观测性缺陷（P1，强烈建议同批修）
- 状态：根因已定位到函数级并完成"复现 → 修复机制验证"闭环；本文档给出补丁草案、回归测试与验收标准

---

## 1. 摘要（TL;DR）

| 编号 | 问题 | 定性 | 证据强度 |
|---|---|---|---|
| **B1** | `resolveStdioCommand` 用 `cmd.exe /c <script> <args...>` 包装 `.cmd/.bat` 垫片；当 argv 中**存在第二个含空格的参数**时，cmd.exe 走"旧引号剥离规则"，命令被打断，MCP server 根本没启动 | aicli 代码缺陷（P0） | 直接复现 + 打印 cmd 原始报错 |
| **B2** | 进程启动/握手失败时，错误信息只有 `连接 MCP Server 失败: calling "initialize": EOF`，子进程 stderr 被丢弃，用户无法自查 | aicli 可观测性缺陷（P1） | aicli.log 全窗口无 stderr 记录 |

B1 造成的实际后果：**Windows 上任何"经 npx/uvx/pnpm/yarn 或自定义 .bat/.cmd 启动 + 参数含空格"的 MCP server 都必然连不上**，且报错毫无指向性。含空格参数极其常见（Chrome/Edge user data dir、日志路径、workspace 路径、JSON 参数、甚至用户名带空格的 `C:\Users\John Doe\...`）。

B1 与具体 MCP server 无关：同一台机器、同一个 `chrome-devtools-mcp@1.9.0`，手动运行/Node 包装器运行均成功，仅 aicli 直连失败。

---

## 2. 现象与证据链

### 2.1 实测矩阵（均为本次在同一台机器上的真实结果）

| # | 启动方式 | 关键参数（含空格？） | 结果 | 说明 |
|---|---|---|---|---|
| E1 | PowerShell 手动 | `--autoConnect --userDataDir "C:\...\Edge\User Data"`（是） | ✅ 成功，日志 `Chrome DevTools MCP Server connected` | 排除"server 本身有问题" |
| E2 | aicli 直连（`command: npx`） | 同上（是） | ❌ `calling "initialize": EOF`（复现 3 次） | 本缺陷主现场 |
| E3 | aicli 直连 | `--wsEndpoint ws://ip:port/devtools/browser/<uuid>`（否） | ✅ connected，29 tools | 无空格 → 恰好幸免 |
| E4 | aicli 直连 | `--wsEndpoint ...` + `--logFile "C:\...\Temp\mcp aicli ws.log"`（是） | ❌ `calling "initialize": EOF` | **与 autoConnect 无关**，纯空格触发 |
| E5 | aicli 直连 | `--wsEndpoint ...` + `--logFile "C:\...\Temp\mcp-aicli-ws.log"`（否，对照组） | ✅ connected，29 tools | 与 E4 仅差一个空格 |
| E6 | Go 复刻 aicli 的 spawn（`cmd.exe /c "C:\Program Files\nodejs\npx.cmd" ... --userDataDir "C:\...\User Data"`） | （是） | ❌ stderr：`'C:\Program' is not recognized as an internal or external command` | **直接拿到 cmd.exe 的原始报错** |
| E7 | Go 修复版 spawn（`cmd.exe /d /s /c "<完整命令行>"`，原样下发） | （是） | ✅ `initialize` 返回 `serverInfo.name=chrome_devtools v1.9.0`；日志 `connected` | **修复机制已验证** |
| E8 | Node 包装器（`node wrapper.js` → `spawn('npx', args, {shell:true})`） | （是） | ✅ connected，29 tools | 现网临时规避方案 |

### 2.2 关键原始输出（E6 / E7）

E6（现状，失败）：

```
exec.Command argv = ["cmd.exe" "/c" "C:\\Program Files\\nodejs\\npx.cmd" "-y" "chrome-devtools-mcp@latest"
                     "--autoConnect" "--userDataDir" "C:\\Users\\wwsheng\\AppData\\Local\\Microsoft\\Edge\\User Data" ...]
stderr: 'C:\Program' is not recognized as an internal or external command, operable program or batch file.
MCP --logFile 未生成
wait err: exit status 1
```

E7（修复方案，成功）：

```
cmd.exe /d /s /c ""C:\Program Files\nodejs\npx.cmd" -y chrome-devtools-mcp@latest --autoConnect
                --userDataDir "C:\...\User Data" --logFile "..." ..."
initialize 响应: {"result":{"protocolVersion":"2024-11-05",...,"serverInfo":{"name":"chrome_devtools","version":"1.9.0"}},"jsonrpc":"2.0","id":1}
MCP 日志: Starting Chrome DevTools MCP Server v1.9.0 / Chrome DevTools MCP Server connected
```

### 2.3 已排除的假设（避免重复排查）

| 假设 | 结论 | 证据 |
|---|---|---|
| 参数被 aicli 拆分/丢引号 | ❌ 不成立 | 用 `command: node` 探针转储 `process.argv`，含空格路径完整、cwd 正确 |
| 子进程环境变量被裁剪 | ❌ 不成立 | 完整 env 对比：子进程比父 shell **多** 15 个变量（aicli 注入的各类 API Key），无缺失 |
| aicli 的 stdio JSON-RPC 通道本身有问题 | ❌ 不成立 | 同通道下 `--wsEndpoint` 配置 29 个工具全可用，真实 `list_pages` 返回浏览器页面 |
| `--autoConnect` 不被 aicli 支持 | ⚠️ 表象 | 实为 B1 的连带结果：autoConnect 场景几乎必然带 `--userDataDir "…有空格…"`，从而命中 B1 |
| 第三方 server 版本/安装问题 | ❌ 不成立 | 同版本 server 手动、Node 包装器、修复版 spawn 三种方式全部成功 |
---

## 3. 根因分析（函数级）

### 3.1 相关代码

`E:\projects\ai-agent-runtime\backend\internal\mcp\transport\stdio_command.go:17-41`（aicli 源码）：

```go
func resolveStdioCommand(command string, args []string) (string, []string) {
	...
	target := trimmed
	if filepath.Ext(target) == "" {
		resolved, err := exec.LookPath(target)   // npx → C:\Program Files\nodejs\npx.cmd
		if err != nil { return command, args }
		target = resolved
	}
	if !isWindowsBatchShim(target) { return target, args }
	wrapped := make([]string, 0, len(args)+2)
	wrapped = append(wrapped, "/c", target)      // ← 缺陷点：cmd.exe /c <script> <args...>
	wrapped = append(wrapped, args...)
	return "cmd.exe", wrapped
}
```

调用点：`backend/internal/mcp/transport/transport.go:123-126`（`ToMCPSdkTransport`）
→ `backend/internal/mcp/transport/stdio_tree.go:171-183`：

```go
func newStdioCommandGuard(ctx context.Context, command string, args []string) (*exec.Cmd, *executor.ProcessGuard, error) {
	cmd := exec.CommandContext(ctx, command, args...)   // ← SysProcAttr 在此之后由 guard 填充
	guard := executor.NewProcessGuard()
	if err := guard.Bind(cmd); err != nil { ... }        // Windows: 只是 OR 上 HideWindow / CREATE_NEW_PROCESS_GROUP
	...
}
```

### 3.2 机制：cmd.exe 的两条引号规则

`cmd /?` 中明确定义：当命令行里出现引号时，

1. **Rule 1（保留引号）** 需同时满足：无 `/S`、整个命令行**恰好只有 2 个引号**、两个引号之间没有 `&<>()@^|`、两引号之间是有效可执行文件路径。
2. **Rule 2（旧行为，剥离首尾引号）**：不满足 Rule 1 时，cmd 会**剥掉第一个引号和最后一个引号**，其余文本按普通命令行重新解析。

Go 的 `os/exec` 会把每个"含空格的参数"用双引号包裹，于是：

| 场景 | 生成的命令行 | 引号数 | cmd 行为 |
|---|---|---|---|
| 仅脚本路径含空格（如 `--wsEndpoint` 无空格参数） | `cmd.exe /c "C:\Program Files\nodejs\npx.cmd" -y ... --wsEndpoint ws://...` | 2 | Rule 1 → 正常执行 ✅ |
| 脚本路径 + 任一含空格参数（如 `--userDataDir "…\Edge\User Data"`） | `cmd.exe /c "C:\Program Files\nodejs\npx.cmd" -y ... --userDataDir "C:\…\Edge\User Data" …` | 4 | Rule 2 → 剥离首尾引号 → 首个 token 变成 `C:\Program` → `'C:\Program' is not recognized as an internal or external command` ❌ |

因此：**只要 argv 里有两个及以上含空格的元素（脚本自身路径常常就是第一个），stdio MCP server 100% 起不来**。这解释了几分钟前看似"诡异"的全部现象：

- `--wsEndpoint ws://…`（无空格）碰巧只有 2 个引号 → 侥幸能连；
- `--autoConnect --userDataDir "C:\…\User Data"`（官方推荐用法）、任何 `--logFile "…a b.log"`、任何 JSON 参数带空格 → 必然失败；
- 报错表现为 `calling "initialize": EOF`（因为 cmd 直接退出，MCP 进程从未启动，连它自己的 `--logFile` 都不会生成——实测日志文件"未生成"正是关键旁证）。

### 3.3 为什么这是 aicli 的缺陷而不是第三方的问题

| 判定项 | 证据 |
|---|---|
| 同版本 server 手动可连 | E1：PowerShell 直接运行，`connected` ✅ |
| Node 包装器（`shell:true`，即 cmd `/d /s /c "单条命令行"`）可连 | E8 / E7 ✅ |
| Go 复刻 aicli 的 `cmd.exe /c` 逐参数写法必失败 | E6 ❌，且拿到 cmd 原始报错 |
| 失败与 server 逻辑无关 | E4/E5：同一 `--wsEndpoint` 连接，仅日志参数多一个空格即从 ✅ 变 ❌ |
| 影响所有 `.cmd/.bat` 启动器 | `isWindowsBatchShim` 覆盖 `.cmd/.bat`（npx/uvx/pnpm/yarn/自定义 bat 一视同仁） |

---

## 4. 影响面

1. **平台**：仅 Windows（`resolveStdioCommand` 只在 `runtime.GOOS == "windows"` 时包装；Unix 直接用 execve，无此问题）。
2. **触发条件**（满足任一即中招）：
   - `command` 是 `.cmd/.bat` 垫片（`npx`、`npm`、`pnpm`、`yarn`、`uvx`、第三方 `xxx.cmd`），**且**
   - `args` 中除脚本路径外还有 ≥1 个含**空格**（或 `& | < > ^ ( )` 等 cmd 元字符）的元素。
3. **典型受害者**：
   - `chrome-devtools-mcp`：`--autoConnect --userDataDir "C:\Users\<用户>\AppData\Local\Microsoft\Edge\User Data"`（Chrome/Edge 144+ 官方推荐的浏览器内开关流程）；
   - 任何带 `--logFile "…logs dir…"`、`--workspace "D:\My Projects\x"`、`--blockedUrlPattern "foo bar"` 的 server；
   - **用户名含空格**的 Windows 账户（`C:\Users\John Doe\…`）：即使用户不显式传路径，`npx` 自身的缓存/配置路径也常带空格 → 随机性极强；
   - 通过 `.aicli/mcp.yaml` 下发 JSON 参数（`{"path": "C:\Program Files\…"}`）的 server。
4. **非确定性风险**：同一份 `mcp.yaml` 在 A 机器可用、B 机器报 EOF（取决于安装路径/用户目录是否含空格），排查成本极高；且当前错误信息完全掩盖真实原因（见 B2）。
5. **安全性副作用**：现状依赖 cmd.exe 的隐式引号规则；若改为规范写法，应同时启用 `/d`（跳过注册表 `AutoRun`，避免被本机 AutoRun 脚本注入命令）。
---

## 5. 修复方案

### 5.1 P0 — 修正 `.cmd/.bat` 垫片的包装方式（已验证机制）

**核心原则**：交给 `cmd.exe` 的命令必须是**一条完整的命令行字符串**，并用 `cmd.exe /d /s /c "<整条命令>"` 形式下发；同时**必须绕过 Go 的 argv 引号拼装**（Go 用 MSVCRT 的 `\"` 转义，cmd.exe 不认），改由 `syscall.SysProcAttr.CmdLine` 原样指定。

> `/d` 跳过注册表 AutoRun（安全/确定性）；`/s` 统一"剥掉首尾引号"的行为，等价于 Node `child_process` / `cross-spawn` 的标准做法（本次 E7 已实测通过）。

**改动点 1：`backend/internal/mcp/transport/stdio_command.go`**

```go
// stdioCommand 描述最终要执行的 stdio 子进程。
type stdioCommand struct {
	Path string   // 交给 exec.Command 的程序
	Args []string // 交给 exec.Command 的参数（诊断/日志用）
	// RawCmdLine 非空时，调用方必须写入 syscall.SysProcAttr.CmdLine 原样下发，
	// 不能让 os/exec 重新拼装（Go 的 \" 转义是 MSVCRT 规则，cmd.exe 不识别）。
	RawCmdLine string
}

func resolveStdioCommand(command string, args []string) stdioCommand {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" || runtime.GOOS != "windows" {
		return stdioCommand{Path: command, Args: args}
	}
	target := trimmed
	if filepath.Ext(target) == "" {
		resolved, err := exec.LookPath(target)
		if err != nil {
			return stdioCommand{Path: command, Args: args} // 解析失败保留原样
		}
		target = resolved
	}
	if !isWindowsBatchShim(target) {
		return stdioCommand{Path: target, Args: args}
	}
	inner := quoteForCmd(target)
	for _, a := range args {
		inner += " " + quoteForCmd(a)
	}
	return stdioCommand{
		Path:       "cmd.exe",
		Args:       []string{"/d", "/s", "/c", inner},
		RawCmdLine: `cmd.exe /d /s /c "` + inner + `"`,
	}
}

// quoteForCmd 按 cmd.exe 规则引用单个参数（详见 §5.4 风险提示）。
func quoteForCmd(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"&|<>^()%!") {
		return s
	}
	return `"` + s + `"`
}
```

**改动点 2：`backend/internal/mcp/transport/stdio_tree.go:171`**

```go
func newStdioCommandGuard(ctx context.Context, rc stdioCommand) (*exec.Cmd, *executor.ProcessGuard, error) {
	cmd := exec.CommandContext(ctx, rc.Path, rc.Args...)
	if rc.RawCmdLine != "" {
		applyRawCmdLine(cmd, rc.RawCmdLine) // 必须在 guard.Bind 之前
	}
	guard := executor.NewProcessGuard()
	if err := guard.Bind(cmd); err != nil { // Windows 侧只 OR 上 HideWindow / CREATE_NEW_PROCESS_GROUP，不会覆盖 CmdLine
		guard.Close()
		return cmd, nil, err
	}
	...
}
```

**改动点 3：新增带构建标签的小工具**（沿用仓库现有 `*_windows.go` / `*_other.go` 惯例）

```go
//go:build windows
// stdio_cmdline_windows.go
func applyRawCmdLine(cmd *exec.Cmd, raw string) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = raw
}

//go:build !windows
// stdio_cmdline_other.go
func applyRawCmdLine(cmd *exec.Cmd, raw string) {}
```

**改动点 4**：`transport.go:123-126` 适配新返回值（`resolveStdioCommand` → `newStdioCommandGuard(ctx, rc)`）；`client.go:192` 的 "Server command" 日志建议保留 `Args` 拼装形式，便于用户对照。

### 5.2 P0 备选/加固方案（可选）

- **方案 C（推荐作为后续加固）**：对已知垫片做"直连解释器"解析，彻底绕开 cmd.exe。例如 npm 生成的 `npx.cmd` 结构固定（内部调用 `"%dp0%\node.exe" "%dp0%\node_modules\npm\bin\npx-cli.js" %*`），可在 `resolveStdioCommand` 中识别并直接返回 `node.exe npx-cli.js …`。好处：零引号问题、启动更快；代价：需按启动器维护少量模式，遇到未知 `.cmd` 仍回落到方案 A。
- 不建议的做法：自行用 `cmd.exe /c` + Go 默认 argv（现状）、或把参数拼成字符串后交给 `shell: true` 之外的自研转义（引号/`%`/`!` 展开易出错）。

### 5.3 P1 — 诊断可观测性（强烈建议与 P0 同批）

现状：进程根本没起来时，用户只看到 `连接 MCP Server 失败: calling "initialize": EOF`（`aicli.log` 中亦无子进程 stderr），真实原因 `'C:\Program' is not recognized…` 被丢弃。

建议：
1. 在 stdio 启动处为子进程 stderr 挂一个**有界环形缓冲（建议 8–16 KB）**，与既有 stderr 输出并行（tee）。
2. `initialize` 失败或进程在握手前退出时，错误信息追加：`exit code`、是否在 N 秒内退出、stderr 尾部 N 行。示例期望输出：

```
连接 MCP Server 失败: 子进程在 initialize 前退出（exit code=1）
stderr: 'C:\Program' is not recognized as an internal or external command, operable program or batch file.
```

3. `aicli mcp test-server <name>` 增加 `--show-stderr`（或默认输出尾部 20 行），并在 `--output json` 中提供 `stderrTail` 字段。
4. 落地时注意：go-sdk 的 `CommandTransport` 是否接管 `cmd.Stderr`（若接管，需在构造 Transport 之前/之后按 SDK 约定注入 Buffer），本项需在实现时确认一次。

### 5.4 风险提示（实现时务必覆盖）

- `quoteForCmd` 的"简单加引号"版本对 `"`、`%`、`!`、`^`、`&` 等字符不够健壮：cmd 会在引号内做 `%VAR%` 展开，`!VAR!` 在延迟展开开启时展开。建议直接移植 **cross-spawn 的 `escape.argument`/`escape.command`**（MIT，Node 生态广泛验证）并配表驱动测试；对无法安全引用的参数（含 `"` 等）给出明确报错而不是静默失败。
- `SysProcAttr.CmdLine` 只在 Windows 存在 → 必须用构建标签隔离（改动点 3）。
- 顺序：`applyRawCmdLine` 必须在 `guard.Bind` 之前或之后皆可（Bind 只 OR 标志位），但**不可**在 Bind 之后整体替换 `cmd.SysProcAttr`（会丢 `HideWindow`/`CREATE_NEW_PROCESS_GROUP`）。
- 回归风险面：所有 Windows 下 `.cmd/.bat` 类 MCP server（含企业内自研 bat 启动器）都走这条新路径，建议灰度/自测覆盖。

### 5.5 P2 — 回归测试建议

1. **单元测试**（`backend/internal/mcp/transport/stdio_command_test.go` 现有用例需同步更新签名）：
   - `.cmd` + 仅普通参数 → 期望 `/d /s /c` + `RawCmdLine` 含 `"<script>"`；
   - `.cmd` + 含空格参数（`--userDataDir C:\Users\a b\User Data`）→ 断言 `RawCmdLine` 中该参数仍是被引号包裹的**单个**整体；
   - `.exe` / 非垫片 → 不产生 `RawCmdLine`（行为不变）。
2. **Windows 端到端测试**（新增 `stdio_shim_space_windows_test.go`，与 `stdio_tree_windows_test.go` 同风格）：
   - 用 `t.TempDir()` 造一个含空格的目录，写入 `fake-launcher.cmd`（内容 `@echo off` + 把 `%*` 写入文件，或直接 `node dump-argv.js %*`）；
   - 通过 `newStdioCommandGuard` 真实启动，参数里带含空格项，断言子进程收到的 argv 与预期逐字节一致；
   - 该用例在当前代码上**必定失败**、在 P0 补丁后通过——正是本次缺陷的回归护栏。
3. 可选：在 CI（windows-latest）上跑上述 e2e；Linux/macOS 分支不受影响。
---

## 6. 影响期规避方案（不改 aicli 代码）

| 方案 | 做法 | 适用性 |
|---|---|---|
| 规避 1（推荐） | 用 `.exe` 或 `node` 作为 `command`，由包装脚本内部再拉起目标 server（Node 的 `shell:true` 走 `cmd /d /s /c "整条命令"`，引号处理正确） | 任何 npx 类 server；本项目已落地 |
| 规避 2 | 消除参数中的空格：user data dir 换到 `C:\EdgeDebugProfile`；日志文件放无空格路径 | 临时应急 |
| 规避 3 | 自研 MCP 启动器时不要用 `.cmd/.bat`，改成 `.exe` / node 入口 | 内研 server |
| ❌ 反模式 | 自己在 `command` 里写 `cmd.exe /c …`（aicli 仍会二次包装，同样触发 Rule 2） | —— |

参考实现（本项目现网，已验证 29 tools + 真实页面调用）：

```yaml
# E:\projects\itsm\.aicli\mcp.yaml
    command: node
    args:
      - E:\projects\itsm\.aicli\edge-mcp-wrapper.js
```

```js
// .aicli/edge-mcp-wrapper.js（要点）
// 1) 读 %LOCALAPPDATA%\Microsoft\Edge\User Data\DevToolsActivePort（第1行=端口，第2行=浏览器级 WS 路径）
// 2) spawn('npx', ['-y','chrome-devtools-mcp@latest','--wsEndpoint',`ws://127.0.0.1:${port}${wsPath}`, …], { stdio:'inherit', shell:true })
```

> 该方案同时解决另一个现实问题：Edge/Chrome 用"浏览器内开关"开启调试后，**端口与浏览器 UUID 每次重启都会变**，包装器每次启动实时读取，无需改配置。

---

## 7. 本项目当前状态（`E:\projects\itsm`）

- `.aicli/mcp.yaml` 已切换为**规避 1** 形态：`command: node` + `.aicli/edge-mcp-wrapper.js`；实测 `connected: true, toolCount: 29`，`list_pages` 能取到真实标签页。
- 待 aicli 修复 B1（+ B2）后，可回归浏览器开关 + 官方推荐形态：
  `npx -y chrome-devtools-mcp@latest --autoConnect --userDataDir "C:\Users\wwsheng\AppData\Local\Microsoft\Edge\User Data"`，届时包装器可下线（或保留以兼容 UUID 变动场景）。
- `.dev/` 目录已被 `.gitignore:289` 忽略 → 本文档为本地资料，不会入库；如需评审请转存到 `docs/`（itsm 仓库）或 aicli 仓库的 `docs/mcp/`。

---

## 8. 验收标准（修复 PR 的 Definition of Done）

1. **功能**：Windows 下 `command: npx` + 含空格参数（`--userDataDir`、`--logFile` 等）配置，`aicli mcp status` 返回 `connected: true`；
2. **回归**：`TestResolveStdioCommand*` 全部通过并新增含空格用例；新增的 Windows e2e 用例在旧代码上失败、新代码上通过；
3. **诊断**：失败时错误信息包含子进程 `exit code` + stderr 尾部（不再只有 EOF）；
4. **安全**：包装带 `/d`（跳过 AutoRun）；不引入新的命令注入面（含 `& | ^ % !` 参数有明确处理或拒绝）；
5. **自测命令**（可直接复用本次复现配置）：

```powershell
# 期望（修复后）：connected = true, toolCount = 29
aicli mcp -C "$env:TEMP\diag-ws.yaml" status --output json
# 期望（修复后）：错误信息里能看到子进程 stderr 尾部
aicli mcp -C "$env:TEMP\diag-autoc.yaml" status --output json
```

### 8.1 验收实测结果（2026-09-21，`e41fed46` 之后本机复跑）

| # | 标准 | 结果 | 关键证据 |
|---|------|------|----------|
| 1 | 功能：`npx` + 含空格参数 → `connected: true` | ✅ | `diag-ws.yaml` → `connected:true, toolCount:29`（`--wsEndpoint ws://127.0.0.1:9222/devtools/browser/...`） |
| 2 | 回归：既有/新增测试全绿 | ✅ | `go test ./cmd/aicli/commands/ ./internal/mcp/...` 全部通过；`-tags win7compat` 下 `manager`/`transport` 编译通过（全树 win7compat 构建受离线缓存缺 `jsonschema/v5` 限制，与本改动无关） |
| 3 | 诊断：失败含 `exit code` + stderr 尾部 | ✅ | `diag-env`：`子进程已退出（PID 10444，exit code = 3）` + `stderr 尾部（共 19 字节）：[env-probe] dumped` |
| 4 | 安全：包装带 `/d` | ✅ | 运行期采样：`cmd.exe /d /s /c "npx -y chrome-devtools-mcp@latest --autoConnect --userDataDir C:\...\Edge\User Data ..."`（整条命令行在引号内，含空格路径未被截断） |
| 5 | 自测命令 | ✅（含一处预期变化） | `diag-ws` / `diag-space` → `connected:true, toolCount:29`；`diag-autoc` 修复后**直接连通**（原「期望失败」场景随缺陷修复消失） |

补充观测：

- 成功场景的 stderr 亦可主动查看：`test-server --show-stderr`（2026-09-21 追加，见 §11.6）对 `diag-stderr` 输出 518 字节 stderr 尾部（DEP0190 警告 + 内容安全提示），JSON 字段 `stderr_tail`。
- 每次运行结束无残留 `cmd.exe` / `node` 子进程（Job Object 回收正常）；机器上另有 18:33/18:37 两批历史遗留进程（修复前早期试验产物，非本轮产生）。

---

## 9. 附录

### 9.1 环境与版本

| 项 | 值 |
|---|---|
| OS | Windows 10 22H2（10.0.19045） |
| aicli | `aicli version dev`（`E:\bins\aicli.exe`），源码 `E:\projects\ai-agent-runtime` |
| Chrome DevTools MCP | `chrome-devtools-mcp@1.9.0` |
| Node / npm / Go | v24.15.0 / 11.12.1 / go1.25.5 windows/amd64 |
| 浏览器 | Edge 153.0.4234.48（153 目录），`edge://inspect/#remote-debugging` 已开启 |

### 9.2 复现与验证脚本

> 已归档：`E:\projects\itsm\.dev\aicli\repro\`（下表同名文件；`%TEMP%` 下另有运行副本）。

| 文件 | 用途 |
|---|---|
| `diag-autoc.yaml` | 失败用例：`npx … --autoConnect --userDataDir "<含空格>"` |
| `diag-ws.yaml` | 成功对照：`npx … --wsEndpoint ws://…`（无空格） |
| `diag-space.yaml` | ★ 决定性对照：同 `--wsEndpoint` + `--logFile "…mcp aicli ws.log"`（含空格）→ 失败 |
| `probe-cmdspawn/main.go` | Go 复刻 aicli 的 `cmd.exe /c` 逐参数 spawn → 打印 cmd 原始报错 |
| `probe-fix/main.go` | A/B 对照：现状 vs `/d /s /c "整条命令行"`（RawCmdLine），含 `initialize` 握手验证 |
| `aicli-argv-probe.js` + `diag-argv.yaml` | 转储 aicli 传给子进程的 argv（证明参数未被拆分） |
| `aicli-env-probe.js` + `diag-env-stderr.yaml` | 完整 env 对比（证明环境未被裁剪） |
| `aicli-stderr-probe.js` | 包装器方式捕获 MCP stderr（证明包装后 autoConnect 可用） |

关键命令：

```powershell
# 复现（期望失败）
aicli mcp -C "$env:TEMP\diag-space.yaml" status --output json
# Go 复刻 + 修复对照
cd $env:TEMP\probe-cmdspawn; go run main.go
cd $env:TEMP\probe-fix;      go run main.go
```

### 9.3 原始证据片段

```
[E4 现网 aicli 直连，含空格 --logFile]
{"name":"diag-space",...,"connected":false,"lastError":"连接 MCP Server 失败: calling \"initialize\": EOF"}

[E5 同配置无空格 → 成功]
{"name":"diag-ws",...,"connected":true,"toolCount":29,"lastConnect":"2026-09-21T19:06:40+08:00"}

[E6 Go 复刻 aicli spawn]
exec.Command argv = ["cmd.exe" "/c" "C:\\Program Files\\nodejs\\npx.cmd" "-y" "chrome-devtools-mcp@latest" "--autoConnect" ...]
STDERR: 'C:\Program' is not recognized as an internal or external command, operable program or batch file.
MCP --logFile 未生成 / wait err: exit status 1

[E7 修复版 spawn]
initialize 响应: {"result":{"protocolVersion":"2024-11-05","capabilities":{"logging":{},"tools":{"listChanged":true}},
                "serverInfo":{"name":"chrome_devtools","title":"Chrome DevTools MCP server","version":"1.9.0"}},"jsonrpc":"2.0","id":1}
MCP 日志: ... Starting Chrome DevTools MCP Server v1.9.0 / Chrome DevTools MCP Server connected
```

### 9.4 一句话给评审

> aicli 在 Windows 上用 `cmd.exe /c <script> <args...>` 包装 `.cmd` 垫片，未按 cmd 的引号规则组装整条命令行；一旦参数含空格（如 chrome-devtools-mcp 官方 `--autoConnect --userDataDir` 流程），cmd 走"剥离首尾引号"的旧规则，子进程根本无法启动，而错误只暴露为 `initialize: EOF`。修复方式是改为 `cmd.exe /d /s /c "<整条命令行>"` 并经 `SysProcAttr.CmdLine` 原样下发（已实测验证），同时补齐 stderr 诊断与 Windows e2e 回归。
---

## 10. 代码链路取证（aicli 源码只读审查，2026-09-21）

> 证据来源：Go 源码逐行审查（子代理）+ 本文 §2/§3 的实测。复现材料已归档到 `.dev/aicli/repro/`（`%TEMP%` 下另有同名副本）。

### 10.1 `aicli mcp status` → spawn 的完整调用链

```
commands.mcp.go:371-379  runMCPStatusCommand
└─ mcp.go:208-231        ensureMCPManager() → MCPManager.Start(ctx)
   ├─ manager.go:219-224 Start = StartAsync + WaitReady（并发上限 8，manager.go:120）
   ├─ manager.go:300     connectMCP → manager.go:334 createClient
   │   └─ manager.go:509-513 connectClient：WithTimeout(resolveConnectTimeout)
   │        └─ 超时优先级：server.timeout > global.connectTimeout > 10s 默认（manager.go:982-985）
   │        └─ client/client.go:195-215 Connect → initialize
   │             └─ 失败包装 `连接 MCP Server 失败: %w`（client.go:207）
   └─ transport/transport.go:123-126 ToMCPSdkTransport
        ├─ resolveStdioCommand        (stdio_command.go:17-41)
        └─ newStdioCommandGuard       (stdio_tree.go:171-188) → exec.CommandContext + Job Object 守卫
```

配置来源：`config/types.go:93-113`（`command|args|env|workingDir|timeout`）；cwd/env 装配：`transport.go:136-162`。

### 10.2 与本文结论的互证与补充

| 项 | 代码证据 | 与本文关系 |
|---|---|---|
| `.cmd/.bat` 垫片被 `cmd.exe /c` 包装，且**未做任何引号适配** | `stdio_command.go:17-41`（本次修复目标） | 与 §3 一致 |
| Go 官方点名该例外：cmd.exe/批处理使用**不同**的反引号算法，建议自行引用并写入 `SysProcAttr.CmdLine` | Go 1.25.5 `os/exec/exec.go:390-397` | 支持 §5.1 修复方式 |
| `calling "initialize": EOF` 来自"读循环结束"分支（无 `connection closed` 前缀）→ **对端先消失**，不是本地 ctx 取消 | go-sdk `mcp/transport.go:180-199` | 支持 §3.2 判断 |
| aicli 的关闭顺序是"**先失败、后收树**"（SDK Close → Job Terminate），不会抢在 initialize 前关 stdin | `stdio_tree.go:97-122, 179-187`；`process_guard_windows.go:32-65` | 排除"aicli 主动关闭导致 EOF" |
| 4–8s 失败**不是** aicli 超时（默认 10s，本仓 1m/2m） | `config/loader.go:98-100`；`manager.go:121-123,467-478` | 排除超时假设 |
| `env: {}` → `cmd.Env = nil`（完整继承）；`cwd` 取 `workingDir` 否则 `os.Getwd()` | `transport.go:136-162` | 与 §2.3 的实测一致（排除环境差异） |
| `--wsEndpoint` 只是第三方 server 的 CLI 参数，aicli 只需透传 | `transport.go:73-84`（MCP-over-WS 传输是另一回事） | ⚠️ 注意：`ws://127.0.0.1:9222/devtools/browser/<uuid>` 是 **CDP 端点**，不能配成 `type: websocket` |

### 10.3 对 P1 的补充实现建议

1. `client.go:202-207` 的失败路径应补充：**退出码/signal、是否在握手前退出、stderr 尾部**（现有 `mcp.client.session.connect_failed` 事件只带 `error` 字符串，无法区分"对端退出"与"本地取消"）。
2. 可选：把 stdio server stderr 落盘到 `.aicli/logs/mcp-<name>.stderr.log`（对第三方 server 取证价值极高，本例可直接看到 `'C:\Program' is not recognized…`）。
3. 可选逃生舱：当用户在 YAML 里显式写 `command: cmd.exe` + `args: ["/c", ...]` 时**不再二次包装**（当前会再包一层，导致 `/c cmd.exe /c …`）。

### 10.4 保留意见（落地时需注意）

- 子代理审查所用的 go-sdk 为**本机 module cache 中的 v0.8.0**（`go.mod` 声明 v1.4.0，本机网络不通无法核对）→ SDK 内部行号可能随版本漂移；但错误字符串形态与实测完全一致。
- §5.1 的修复机制已用 Go 复刻实证（E7），**尚未在 aicli 源码中落地并跑通真实客户端**；第 8 节的验收标准即为落地后的必测项。

---

## 11. 实施记录（ai-agent-runtime 落地，2026-09-21）

> 本文档已归档到本仓库 `docs/plan/`，作为该缺陷的**权威实施文档**；本节记录实际落地内容、与前述方案的差异、验证证据与未落地项。

### 11.1 落地范围

| 级别 | 状态 | 说明 |
|---|---|---|
| P0（`.cmd/.bat` 垫片命令行） | ✅ 已落地 | 含 `\\`/`"`/`%`/`&` 等元字符的转义，与 cross-spawn 语义对齐 |
| P1.1 stderr 有界环形缓冲 | ✅ 已落地 | 16 KB 上限，并发安全，`Write` 永不阻塞子进程 |
| P1.2 错误信息 / 生命周期事件补充诊断 | ✅ 已落地 | 用户可见错误 + `mcp.client.session.connect_failed.stderr_tail` + `aicli.log` |
| P1.3 `aicli mcp test-server --show-stderr` | ⏸ 未落地 | 需要 `manager → registry → client` 接口扩展（win7compat 双实现），单独立项更稳，见 §11.5 |
| P2 单测 / Windows e2e 回归护栏 | ✅ 已落地 | 见 §11.4，含真实 `cmd.exe → .cmd → node` 的 MCP 握手用例 |

### 11.2 代码改动清单

| 文件 | 改动 |
|---|---|
| `backend/internal/mcp/transport/stdio_command.go` | 新增 `stdioCommand{Path,Args,RawCmdLine}`；`resolveStdioCommand` 返回该结构并产出 `cmd.exe /d /s /c "<整条命令行>"`；新增 `escapeCmdCommand` / `escapeCmdArgument` / `doubleBackslashesBeforeQuotes` / `doubleTrailingBackslashes`（cross-spawn 算法移植，含 `node_modules\.bin\*.cmd` 的双重转义判定） |
| `backend/internal/mcp/transport/stdio_cmdline_windows.go`（新） | `applyRawCmdLine`：把 RawCmdLine 写入 `syscall.SysProcAttr.CmdLine`，等价 Node `windowsVerbatimArguments=true`；只改 CmdLine 字段，保留 `Bind` 设置的 `HideWindow` / `CREATE_NEW_PROCESS_GROUP` |
| `backend/internal/mcp/transport/stdio_cmdline_other.go`（新） | 非 Windows 空实现（构建标签隔离） |
| `backend/internal/mcp/transport/stdio_tree.go` | `newStdioCommandGuard(ctx, rc stdioCommand)`：`exec.CommandContext` 后、`guard.Bind` 前调用 `applyRawCmdLine` |
| `backend/internal/mcp/transport/transport.go` | 调用点适配；为子进程挂接 stderr 环形缓冲（`cmd.Stderr = buf`）；`StdioTransport` 新增 `stderr` 字段 |
| `backend/internal/mcp/transport/stdio_stderr.go`（新） | `stderrTailBuffer`（有界环形缓冲）、`StderrDiagnosticsProvider`、`StderrDiagnosticsOf`、`EnrichConnectError`、`formatStderrDiagnostics`（尾部 20 行 / 8 KB，含截断提示） |
| `backend/internal/mcp/transport/stdio_process_status_{windows,other}.go`（新） | 无副作用进程状态探测：Windows 用 `OpenProcess + GetExitCodeProcess`（不重复 `Wait`），Unix 用 `kill(pid,0)` |
| `backend/internal/mcp/client/client.go` | `initialize` 失败时用 `transport.StderrDiagnosticsOf(t)` 增强错误信息，写入 `stderr_tail` 生命周期字段并 `logger.Errorf` 落盘 |
| `backend/internal/mcp/transport/stdio_command_test.go` | 适配新签名 + 转义矩阵用例 |
| `backend/internal/mcp/transport/stdio_stderr_test.go`（新） | 环形缓冲、尾行截取、错误增强用例 |
| `backend/internal/mcp/transport/stdio_shim_space_windows_test.go`（新） | 含空格目录/文件名垫片的 argv 逐字节回归护栏 + 反向护栏（证明旧实现必失败） |
| `backend/internal/mcp/transport/stdio_shim_e2e_windows_test.go`（新） | 真实 `cmd.exe → .cmd → node` 的完整 MCP 握手（initialize + tools/list）与失败场景 stderr 诊断 |

### 11.3 与前述方案的差异（实现时确认）

1. **`quoteForCmd` 采用 cross-spawn 全量算法**，而非文档 §5.1 草案里的简化版：`%`、`!`、`&`、`^`、`(`、`)` 等元字符按 `^` 转义，`"` 先按 qntm 规则补 `\` 再由 `^"` 包裹。简化版在含 `%`/`&` 的参数上不可靠。
2. **go-sdk `CommandTransport` 不接管 `Stderr`**（`mcp@v1.4.0/cmd.go:29-47` 只 `StdoutPipe`/`StdinPipe`），`Stderr == nil` 时 os/exec 会接到 null device —— 正是"只剩 EOF"的直接原因。因此 `cmd.Stderr = <环形缓冲>` 是安全且必要的接入点，无需 SDK 约定。
3. **退出码探测不走 `cmd.Wait`**（Wait 由 SDK 的 `pipeRWC.Close` 调用，重复 Wait 会报错）：Windows 用 `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION) + GetExitCodeProcess`；`ERROR_INVALID_PARAMETER` 说明进程对象已回收，此时输出"退出码不可得"而不是伪造 0。
4. 失败路径额外做了**最长 300 ms 的 stderr 收尾等待**，避免"进程刚退出、拷贝 goroutine 尚未落盘"导致的诊断缺失。
5. 新增文件按仓库既有约定做了构建标签隔离：`stdio_stderr*.go` 为 `!win7compat`，cmdline/状态探测为 `windows` / `!windows`。

### 11.4 验证证据（本机 Windows 10 22H2 + go1.25.5，离线 `GOPROXY=off`）

```powershell
# 1) 编译与静态检查
go build ./internal/mcp/...                       # exit 0
go vet ./internal/mcp/transport/ ./internal/mcp/client/   # exit 0

# 2) 传输层全部用例（含新增 e2e）
go test ./internal/mcp/transport/ -count=1        # ok ... 19.2s

# 3) 新增关键用例
go test ./internal/mcp/transport/ -run 'TestStdioShim' -v
#   --- PASS: TestStdioShimFullHandshakeThroughSpacedPath (0.93s)
#   --- PASS: TestStdioShimFailureSurfacesStderrTail (0.48s)

# 4) mcp 全目录回归
go test ./internal/mcp/... -count=1               # 全部 ok（admin/catalog/client/config/manager/protocol/registry/transport）
```

关键用例断言的实际内容：

- `TestStdioShimFullHandshakeThroughSpacedPath`：`C:\...\mcp server dir\launcher shim.cmd`（目录与文件名都含空格）+ 参数 `--userDataDir "C:\Users\demo user\Edge\User Data"` → 真实 `cmd.exe` 启动 → 子进程 argv 与配置**逐字节一致** → MCP `initialize` 成功 → `tools/list` 返回 `echo_ping` → Job Object 绑定成功（`AttachErr == ""`）。
- `TestWindowsShimWithSpacedArgsDeliversExactArgv`：参数矩阵 `含空格路径` / `%PATH%`（保持字面量）/ `a&b` / `say "hi"` / 结尾反斜杠 全部逐字节还原。
- `TestWindowsShimArgvWrappingTruncatesSpacedPath`：反向护栏 —— 旧写法（`cmd.exe /c <script>` 交给 os/exec 拼装）实测报 `'C:\...\dir' is not recognized as an internal or external command`，证明回归护栏有效。
- `TestStdioShimFailureSurfacesStderrTail`：启动即失败时用户可见错误为

  ```
  calling "initialize": EOF
  [stdio 子进程诊断]
  子进程已退出（PID 1596，exit code = 3）：通常说明启动命令本身失败（如路径不存在、引号被截断、缺少依赖）
  stderr 尾部（共 68 字节）：
  'C:\Program' is not recognized as an internal or external command
  ```

### 11.5 未落地项与后续建议

1. ~~**P1.3 `--show-stderr` / JSON `stderrTail`**~~ → ✅ **已落地（2026-09-21，见 §11.6）**：采用「可选能力接口 + 管理器快照留存」，未扩大 `client.Client` / `Manager` 主接口，`win7compat` 与测试替身零破坏。
2. ~~**`aicli` 二进制级验收**~~ → ✅ **已完成（2026-09-21，见 §8.1）**：`go build ./cmd/aicli` 可执行（默认构建依赖已齐备），§8 全部复跑通过；仅 `-tags win7compat` 全树构建仍受离线缓存缺 `jsonschema/v5` 限制（与本改动无关，涉及包 `manager`/`transport` 已单独验证通过）。
3. **方案 C（直连解释器解析 `npx.cmd`）** 仍建议作为后续加固，可彻底绕开 cmd.exe 层；当前 P0 已消除缺陷。
4. CI 建议新增 `windows-latest` 上运行 `go test ./internal/mcp/transport/ -run 'TestStdioShim|TestWindowsShim'`。

### 11.6 追加落地：`mcp test-server --show-stderr`（2026-09-21）

**动机**：P1 只覆盖「连接失败」的错误信息；成功与排查场景还需要主动查看子进程 stderr（如 `chrome-devtools-mcp` 启动横幅、Node 弃用警告）。

**实现要点**：

| 文件 | 内容 |
|------|------|
| `internal/mcp/transport/stdio_stderr.go` | 新增 `StderrDisplayProvider`（`StderrTailForDisplay()`）；渲染抽公共 `stderrDiagnostics(failureHint bool)`，归因提示（「通常说明启动命令本身失败…」）仅保留在失败路径 |
| `internal/mcp/client/client.go` | 新增可选接口 `StderrDiagnosticsClient` 与 `StderrDiagnosticsOf` 探测；`mcpClient` 记录 `activeTransport` 供读取 |
| `internal/mcp/manager/stderr_diagnostics.go` | 新增可选接口 `StderrDiagnosticsProvider`（不进入 `Manager` 主接口） |
| `internal/mcp/manager/manager.go` | `lastStderr` 快照：建连失败在 `Close()` 前留存；成功 / `Stop` / `ReloadConfig` 清理；查询优先在线客户端 |
| `internal/mcp/manager/manager_win7compat.go`、`merged.go` | 禁用实现返回空串；merged 先 primary 后 secondary |
| `cmd/aicli/commands/mcp.go` | `test-server` 增 `--show-stderr`；JSON 增 `stderr_tail`；text 增 `stderr 诊断:` 段落；失败时补印 `LastError` |
| 测试 | `mcp_stderr_flag_test.go`（4 例）、`manager_stderr_diagnostics_test.go`（4 例）、`stdio_stderr_test.go`（展示文案断言） |

**实测**：

```text
# 失败场景（diag-env：node 立即退出）
$ aicli mcp -C %TEMP%\diag-env-stderr.yaml test-server diag-env --show-stderr
  ❌ 连接失败
  错误: 连接 MCP Server 失败: calling "initialize": EOF
[stdio 子进程诊断]
子进程已退出（PID 15400，exit code = 3）
stderr 尾部（共 19 字节）：
[env-probe] dumped

stderr 诊断:
─────────────────────────────────────────
（同上诊断文本；该段落由 --show-stderr 独立渲染，成败皆可用）

# 成功场景（diag-stderr：chrome-devtools-mcp）
$ aicli mcp -C %TEMP%\diag-env-stderr.yaml test-server diag-stderr --show-stderr --output json
... "success":true, "stderr_tail":"[stdio 子进程诊断]\n子进程 PID 416 仍在运行\nstderr 尾部（共 518 字节）..."
```

### 11.7 回切官方形态与二进制部署验收（2026-09-21）

**背景**：§6 的规避形态（`command: node` + `edge-mcp-wrapper.js`）只为绕开 P0 缺陷；修复进入 `main`（`e41fed46`、`635ba1bd`）后回切官方推荐形态，并在真实项目上复验。

**编译与部署**：

| 项 | 值 |
|---|---|
| 源码 | `main@635ba1bd`（工作区干净） |
| 构建 | `cd backend && go build -o aicli-new.exe ./cmd/aicli`（55,587,328 B，约 75s） |
| 部署 | `E:\bins\aicli.exe`（PATH 唯一入口，`where aicli` 仅此一处） |
| 旧二进制备份 | `E:\bins\aicli.exe.bak-20260921-213341` |

**配置回切**（`E:\projects\itsm\.aicli\mcp.yaml`，本地 gitignored 配置）：

```yaml
    command: npx
    args:
      - -y
      - chrome-devtools-mcp@latest
      - --autoConnect
      - --userDataDir
      - C:\Users\wwsheng\AppData\Local\Microsoft\Edge\User Data
      - --logFile
      - E:\projects\itsm\.aicli\logs\chrome-devtools-mcp.log
      - --no-usage-statistics
      - --no-performance-crux
```

规避形态备份：`.aicli/backups/mcp.yaml.workaround-20260921-213009.yaml`（`edge-mcp-wrapper.js` 同步备份）；wrapper 原文件暂留 `E:\projects\itsm\.aicli\` 备查。

**验收**（新二进制 + 官方形态，argv 含空格 `--userDataDir`）：

```text
$ aicli mcp test-server chrome-devtools --show-stderr
  ✅ 已连接 / 工具数量: 29        exit=0
  stderr 诊断: 子进程 PID 11036 仍在运行；stderr 尾部（共 256 字节）

$ aicli mcp test-server chrome-devtools --output json
  {"status":{"connected":true,"toolCount":29,...},"success":true}
```

`--logFile` 落盘正常：`Starting Chrome DevTools MCP Server v1.9.0` → `Chrome DevTools MCP Server connected`（后随 `Shutting down (stdin end)`，即 test-server 主动收尾）。

**注意**：回切前已启动的 aicli 会话仍运行旧二进制（Windows 允许重命名运行中的可执行文件，已加载进程不受影响），需重启会话以启用修复。
