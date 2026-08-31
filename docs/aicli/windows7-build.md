# 从源码编译 Windows 7 兼容版 aicli

> 本文面向需要自行编译 Win7 兼容包的开发者。最终用户无需编译，直接下载
> Release 中文件名包含 `win7` 的压缩包并参阅 [windows7.md](windows7.md)。

## 1. 为什么需要单独的构建

普通 Release 由 Go 1.24 编译，而 Go 1.21 起官方已不再支持 Windows 7；
且主线代码会调用 Windows 7 不存在的系统 API（如 Windows 10 1903+ 才有的
ConPTY），普通包在 Win7 上启动即崩溃（`Exception 0xc0000005 PC=0x0`）。

因此 Win7 兼容包使用独立的构建配方：

- 工具链固定 **Go 1.20.14**（最后一个官方支持 Windows 7 的 Go 版本）；
- 依赖图走 **`go.win7.mod`**（降级依赖，独立维护，独立 `go.win7.sum`）；
- 编译时带 **`-tags win7compat`**，启用 `//go:build win7compat` 的兼容实现
  并裁剪要求更高 Go 版本的特性（如 MCP 集成）；
- **`CGO_ENABLED=0`**，避免链接到 Win7 上没有的运行时依赖。

## 2. 构建隔离一览

| 维度 | 标准构建 | Win7 构建 |
| --- | --- | --- |
| 代码分支 | `main` | `main`（兼容代码已全部合入 main，**无需切分支**） |
| 工具链 | Go 1.24（`go.mod` 声明） | Go 1.20.14（`GOTOOLCHAIN` 固定） |
| 依赖图 | `backend/go.mod` + `go.sum` | `backend/go.win7.mod` + `go.win7.sum` |
| build tag | 无 | `-tags win7compat` |
| CGO | 允许 | `CGO_ENABLED=0` |
| 默认 CLI 配置 | `config.yaml` / `aicli.yaml` | `config.win7.yaml` / `aicli.win7.yaml` |
| 默认 runtime 配置 | `runtime.yaml` | `runtime.win7.yaml` |
| 会话数据库 | `session_history.sqlite` | `session_history_win7.sqlite` |

代码隔离依赖 `//go:build win7compat` 与 `//go:build !win7compat` 成对出现，
例如：

- `backend/internal/aiclipaths/profile_win7.go` / `profile_standard.go`
- `backend/internal/mcp/manager/manager_win7compat.go` / `manager.go`
- `backend/internal/mcp/registry/registry_win7compat.go` / `registry.go`
- `backend/internal/skill/mcp_adapter_win7compat.go` / `mcp_adapter.go`

## 3. 前置条件

1. **源码**：检出 `main` 分支（win7 构建资产 `backend/go.win7.mod`、
   `backend/go.win7.sum`、`backend/configs/runtime.win7.yaml`、
   `.github/workflows/build-aicli-win7.yml` 均在 main 上）。
2. **Go 工具链**：任选其一
   - 本机装有 Go 1.21+（如 1.24），用 `GOTOOLCHAIN=go1.20.14` 让 go 命令
     自动下载并使用 Go 1.20.14（推荐，无需手动安装）；
   - 或手动安装 Go 1.20.14，并保证 `go version` 输出为 `go1.20.14`。
   > Go 1.20 的 go 命令本身不认识 `GOTOOLCHAIN`，该变量由启动的 go 1.21+
   > 解释后再切换到 1.20.14 执行构建，所以 `GOTOOLCHAIN` 写法在 1.21+ 主机上有效。
3. **网络**：首次构建需下载 go 1.20.14 工具链与 `go.win7.mod` 的依赖
   （国内网络建议先配置 `GOPROXY`）。

## 4. 编译

以下命令在 `backend/` 目录下执行。三个产物对应 CI workflow 中的三个命令：

| 产物 | 命令路径 | 用途 |
| --- | --- | --- |
| `aicli-win7.exe` | `./cmd/aicli` | 主 CLI |
| `aicli-console-win7.exe` | `./cmd/aicli-console` | 控制台前端 |
| `runtime-server-win7.exe` | `./cmd/runtime-server` | Win7 兼容 runtime server |

### 4.1 bash（Git Bash / WSL / Linux CI）

```bash
# 主 CLI（可注入版本号，与 CI 一致）
GOTOOLCHAIN=go1.20.14 GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
GOFLAGS="-modfile=go.win7.mod" \
go build -tags win7compat -trimpath \
  -ldflags "-s -w -X main.version=win7-dev -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o dist/aicli-win7.exe ./cmd/aicli

# 控制台前端
GOTOOLCHAIN=go1.20.14 GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
GOFLAGS="-modfile=go.win7.mod" \
go build -tags win7compat -trimpath -o dist/aicli-console-win7.exe ./cmd/aicli-console

# runtime server（版本注入走 internal/buildinfo，与主线 release workflow 一致）
GOTOOLCHAIN=go1.20.14 GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
GOFLAGS="-modfile=go.win7.mod" \
go build -tags win7compat -trimpath \
  -ldflags "-s -w -X github.com/wwsheng009/ai-agent-runtime/internal/buildinfo.version=win7-dev -X github.com/wwsheng009/ai-agent-runtime/internal/buildinfo.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o dist/runtime-server-win7.exe ./cmd/runtime-server
```

### 4.2 PowerShell

```powershell
$env:GOTOOLCHAIN = 'go1.20.14'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
$env:GOFLAGS = '-modfile=go.win7.mod'

go build -tags win7compat -trimpath -o dist/aicli-win7.exe ./cmd/aicli
go build -tags win7compat -trimpath -o dist/aicli-console-win7.exe ./cmd/aicli-console
go build -tags win7compat -trimpath -o dist/runtime-server-win7.exe ./cmd/runtime-server
```

> **注意**：`-tags win7compat` 必不可少。漏掉它时 `profile_win7.go`、
> `*_win7compat.go` 等兼容实现不会参与编译，产物实际走 `!win7compat`
> 标准实现，不是真正的 Win7 兼容版。

### 4.3 使用 scripts 下的构建程序

仓库提供了两个可重复执行的 PowerShell 构建程序。它们不依赖当前工作目录，
会自动定位仓库根目录，并在进程范围内设置 Go 1.20.14、`go.win7.mod`、
`win7compat`、`windows/amd64` 和 `CGO_ENABLED=0`：

```powershell
# 构建主 CLI 和原生 Console 启动器
pwsh -File ./scripts/build-aicli-win7.ps1 -Version win7-dev

# 单独构建 runtime server
pwsh -File ./scripts/build-runtime-server-win7.ps1 -Version win7-dev
```

`pwsh` 也可以替换为 Windows PowerShell 5.1 的 `powershell.exe`；脚本会把
Win7 专用 Go 环境限制在自身进程内，不会污染调用者的环境变量。脚本中的普通
回归测试在宿主平台执行，只有 Windows-only console 测试和最终产物使用
`windows/amd64` 交叉编译。

两个程序默认把产物写入 `backend/dist/`，并为每个 exe 生成 `.sha256` 校验文件；
构建完成前还会检查 PE 的 `MZ` 魔数和 `go version -m` 的 `go1.20.14`。
默认会执行对应的 Win7 回归测试；只想快速编译时可加 `-SkipTests`，依赖图已经
确认过且希望跳过 `go list` / `go mod verify` 时再加 `-SkipDependencyCheck`。
例如：

```powershell
pwsh -File ./scripts/build-aicli-win7.ps1 `
  -Version win7-v1.2.3 -OutputDir dist/win7 -SkipTests
pwsh -File ./scripts/build-runtime-server-win7.ps1 `
  -Version win7-v1.2.3 -OutputDir dist/win7 -SkipTests
```

runtime-server 程序默认临时使用 `backend/internal/webui/dist/placeholder.txt`，
防止把本地生成的现代前端误嵌入 Win7 产物；编译结束（包括失败）会恢复原目录。
确实需要保留现有前端时可显式传入 `-KeepEmbeddedWebUI`。

## 5. 验证产物

```bash
# 1) 工具链确认：首行必须为 go1.20.14
go version -m dist/aicli-win7.exe | sed -n '1,4p'

# 2) PE 头魔数：必须是 MZ（说明是有效 Windows 可执行文件）
head -c 2 dist/aicli-win7.exe

# 3) 校验和归档
sha256sum dist/aicli-win7.exe dist/aicli-console-win7.exe dist/runtime-server-win7.exe
```

在真正的 Windows 7 SP1 x64 机器（或虚拟机）上做启动冒烟：

```cmd
aicli-win7.exe version
aicli-win7.exe chat --help
```

启动即 `0xc0000005 PC=0x0` 通常意味着产物不是用 Go 1.20 + `win7compat`
构建（例如拿普通包或遗漏了 build tag）。

## 6. 测试

```bash
cd backend
GOFLAGS="-modfile=go.win7.mod" go test -tags win7compat \
  ./internal/aiclipaths ./internal/agentconfig ./internal/config \
  ./internal/chat ./cmd/runtime-server -count=1
```

修改了 Win7 配置、路径、console、session、SQLite、依赖或 workflow 时，
必须同时通过标准构建和 Win7 构建的验证（`GOFLAGS` 缺省即标准依赖图）。

> 注意：`go test -modfile=go.win7.mod` 等管理操作不能直接用 `go mod tidy`
> 覆盖 `go.win7.*`；标准依赖图和 Win7 依赖图必须分别维护。

## 7. 打包（可选，与 CI 一致）

Release 包结构与 `build-aicli-win7.yml` 的 Package 步骤保持一致：

```bash
cd backend
VERSION=win7-dev
BASENAME="aicli-${VERSION}-windows-amd64"
mkdir -p dist/release/configs
cp dist/aicli-win7.exe dist/release/aicli.exe
cp dist/aicli-console-win7.exe dist/release/aicli-console.exe
cp dist/runtime-server-win7.exe dist/release/runtime-server.exe
cp configs/runtime.win7.yaml dist/release/configs/runtime.win7.yaml
cp ../docs/aicli/windows7.md dist/release/WINDOWS7-README.md
cd dist/release && zip -9 -r "../${BASENAME}.zip" ./* && cd ..
sha256sum "${BASENAME}.zip"
```

解压布局（与 [windows7.md](windows7.md) 的安装说明一致）：

```text
aicli.exe
aicli-console.exe
runtime-server.exe
configs\
  runtime.win7.yaml
```

## 8. 常见问题

| 现象 | 原因与处理 |
| --- | --- |
| 启动即 `0xc0000005 PC=0x0` | 产物不是 Go 1.20 + `win7compat` 构建：确认 `-tags win7compat` 与 `-modfile=go.win7.mod` 都在，`go version -m` 首行是 `go1.20.14` |
| 编译报错依赖版本冲突 / 找不到模块 | 漏了 `GOFLAGS="-modfile=go.win7.mod"`，走的是标准依赖图；或 `go.win7.mod` 与 `go.win7.sum` 不同步（用 `go mod tidy -modfile=go.win7.mod` 单独维护） |
| 编译报 Go 1.21+ API | main 上新代码使用了 Win7 构建禁止的 API。按 `go.win7.mod` 的依赖约束降级实现，并保证标准构建不受影响 |
| 误用 `go mod tidy` 后 `go.win7.*` 被改 | 独立维护：`go mod tidy -modfile=go.win7.mod`，且不要在没有 `-modfile` 的情况下 tidy |
| Win7 上 MCP 功能缺失 | 预期行为：`win7compat` 构建当前裁剪 MCP 集成，需 MCP 时在受支持的新系统上运行（见 windows7.md 功能范围） |

## 9. 与 CI 的关系

`.github/workflows/build-aicli-win7.yml`（在 main 分支上）完成同样的工作：

- 推送 `win7-*` 格式的 tag 时自动编译并发布 Release（`win7-v1.2.3` 等）；
- 也可在 Actions 页面手动触发（`workflow_dispatch`），只产出构建工件不发布；
- 步骤：Setup Go 1.20.14 → 单元测试 → 三个命令编译（含版本注入）→
  PE/工具链校验 → 打包 zip + sha256 → 上传工件 / 发布 Release。

本地编译出现与 CI 不一致的结果时，以 CI 的 `GO_VERSION` / `MODFILE` /
`BUILD_TAGS` / `CGO_ENABLED` 环境变量为准逐项核对。
