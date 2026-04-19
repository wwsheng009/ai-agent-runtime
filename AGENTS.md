# AGENTS.md

## Windows 命令长度限制

本仓库经常在 Windows 环境下开发。处理大补丁、长 JSON、长内联脚本、长 `powershell -Command` 或长 `cmd /c` 命令时，不要假设命令行长度是“几乎无限”的。

Windows 上常见的限制分层如下：

| 层级 | 典型限制 | 说明 |
| --- | --- | --- |
| `CreateProcessW` | `32767` 字符 | Win32 进程创建 API 的总命令行上限。包含可执行文件路径、空格、引号和所有参数。 |
| `cmd.exe` | `8191` 字符 | 通过 `cmd /c ...` 执行时最常见、也更容易先撞到的限制。很多“命令太长”问题实际卡在这里。 |
| PowerShell `-Command` | 受调用链影响 | 如果 PowerShell 是被直接启动，通常仍受 `CreateProcessW` 的 `32767` 限制；如果前面还包了一层 `cmd.exe`，则通常先受 `8191` 限制。大量引号和转义还会放大实际长度。 |
| 环境变量块 | 约 `32767` 字符 | 如果把大段内容塞进环境变量再启动子进程，也可能撞到环境块大小限制。 |

### 工程上的保守做法

上面的数字是平台层面的理论上限，不代表工程上应该逼近它们。对于需要跨 shell、跨工具桥接、或者带大量转义的场景，建议把单条内联命令控制在 `6000` 字符以内；一旦接近这个量级，就优先改成“写文件再执行”或“分块补丁”。

### 本仓库里的直接影响

- [backend/cmd/aicli/functions/shell.go](E:/projects/ai/ai-agent-runtime/backend/cmd/aicli/functions/shell.go) 在 Windows 上使用 `cmd /c` 执行命令，所以这里不能按 `32767` 预算，而应按 `8191` 这个更小的上限来设计。
- [backend/internal/background/detached.go](E:/projects/ai/ai-agent-runtime/backend/internal/background/detached.go) 采用“先写 `.cmd` runner，再启动 runner”的模式。这是处理长命令、重定向和复杂 shell 拼接的正确方向。
- [backend/internal/runtimeserver/service_api.go](E:/projects/ai/ai-agent-runtime/backend/internal/runtimeserver/service_api.go) 目前通过 `powershell -Command` 拼接重启脚本。如果这段脚本继续增长，应改为写临时 `.ps1` 文件后执行，而不是继续扩展内联 `-Command` 字符串。

### 大补丁和长脚本的规则

1. 不要在 Windows 上一次性发送超长内联补丁、超长 heredoc、超长 JSON 字符串或超长 `powershell -Command` 脚本。
2. 大文件改动优先使用“骨架 + 分块补丁”：
   先创建最小可编译骨架，再按函数、组件或 JSX 区块分多次小补丁写入。
3. 单次补丁不要逼近 `8191`。
   即使理论长度没超，转义、引号和工具包装层也可能让最终命令膨胀后失败。
4. 复杂命令优先写入临时文件再执行：
   PowerShell 写成 `.ps1`，批处理写成 `.cmd`，大 JSON/YAML 直接落文件，不要把大块文本塞进命令行参数。
5. 如果命令需要跨多层 Windows shell 传递，默认按最小上限设计。
   也就是只要链路里出现 `cmd.exe`，就优先以 `8191` 作为预算。
6. 路径很长、引号很多、转义很多时，要额外留安全余量。
   这种场景下“看起来还没超长”的命令，实际非常容易失败。
7. 遇到 `Failed to apply patch`、`The input line is too long`、`The command line is too long` 一类错误时，优先怀疑命令长度和转义膨胀，而不是先怀疑补丁语义本身。

### 推荐模式

- 前端大 TSX 文件：先落空壳，再按 section 分块写入。
- 长命令后台执行：参考 [backend/internal/background/detached.go](E:/projects/ai/ai-agent-runtime/backend/internal/background/detached.go) 的 runner 文件模式。
- 重启或启动脚本：一旦脚本长度开始膨胀，改成脚本文件，不继续叠加 `-Command` 字符串。
- 如果某个工具是否经过 `cmd.exe` 不够确定，按更保守的 `8191` 规则处理，不要赌实现细节。
