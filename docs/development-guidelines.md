# 开发规范

本文档规定 `ai-agent-runtime` 仓库的日常开发、验证、分支、提交、文档和兼容性要求。目标是让改动可审查、可验证、可回滚，并避免标准构建与 Windows 7 兼容构建相互污染。

> 核心原则：**多动手少空想，多实测少猜测。** 结论应有代码、测试、构建产物或运行结果作为证据。

## 1. 适用范围与优先级

本规范适用于仓库中的后端、前端、脚本、工作流和文档改动。

规则发生冲突时，按以下优先级执行：

1. 安全、数据完整性和用户明确要求；
2. 仓库根目录及更近目录中的 `AGENTS.md`；
3. 本文档；
4. 模块已有惯例和历史实现。

## 2. 仓库结构

| 路径 | 说明 |
| --- | --- |
| `backend/` | Go 后端、`aicli`、`runtime-server`、存储与 Agent Runtime |
| `frontend/` | React、TypeScript、Vite 前端 |
| `scripts/` | 安装、打包、发布验证和端到端测试脚本 |
| `docs/` | 用户文档、开发规范、设计、分析和实施计划 |
| `.github/workflows/` | 发布及 Windows 7 兼容构建工作流 |
| `.agents/` | 系统 Agent 与 Skill 定义 |
| `examples/` | 示例配置和 Profile |

新增文件前应先确认其所属模块，不要把临时调试文件、生成物、日志或本地二进制提交到源码目录。

## 3. 开发环境

### 3.1 后端

标准后端模块以 `backend/go.mod` 为准：

- Go：`1.24.0`
- 推荐 toolchain：`go1.24.2`
- 默认构建：无 `win7compat` tag

```bash
cd backend
go version
go mod download
go test ./...
```

### 3.2 前端

前端依赖和命令以 `frontend/package.json`、`frontend/pnpm-lock.yaml` 为准。使用 pnpm，不要混用 npm 或 yarn 改写 lockfile。

```bash
cd frontend
pnpm install --frozen-lockfile
pnpm dev
```

### 3.3 Windows 命令约束

本仓库经常在 Windows 上开发。不要把大段补丁、JSON、脚本或多层转义内容塞入单条命令：

- 经过 `cmd.exe` 的命令按 `8191` 字符上限保守设计；
- 长脚本写入 `.ps1` 或 `.cmd` 后执行；
- 大文件采用“骨架 + 分块修改”；
- 不使用 PowerShell 不支持的 Bash heredoc；
- 路径、引号和转义较多时预留额外余量。

详细约束见仓库根目录 `AGENTS.md`。

## 4. 分支规范

### 4.1 长期分支职责

Win7 兼容代码与构建资产已全部合入 `main`，以 `main` 为唯一事实来源，不设并行适配分支：

| 分支 | 职责 | 构建隔离 | 应承载的改动 |
| --- | --- | --- | --- |
| `main` | 标准系统主线 + Windows 7 兼容 | `backend/go.mod`（Go 1.24）/ `backend/go.win7.mod`（Go 1.21.4、`-tags win7compat`、Windows 7 amd64） | 通用功能、通用修复、现代系统实现、标准发布，以及 Win7 专用 API 回退、旧控制台兼容、依赖降级、配置隔离、适配测试与 Win7 发布 |
| `feat/win7-go120-compat`（历史遗留） | 原 Win7 适配分支，代码已全部合入 `main`，两分支 Win7 资产零差异；仅保留用于历史发布与追溯 | — | 不再承载新改动 |

边界要求：

- `main` 是通用功能和现代系统实现的事实来源，不应为了兼容 Win7 而整体降级 Go 版本、依赖或现代系统能力。
- Win7 适配代码通过技术隔离手段直接维护在 `main` 上：`go.win7.mod` 独立依赖图 + `win7compat` build tag 成对隔离 + 专用配置（`runtime.win7.yaml` 等）。
- Win7 新增提交必须能说明具体适配目标，例如旧 Win32 API、控制台/PTY、TLS、进程管理、文件系统、SQLite、Go 1.20/1.21 或依赖兼容。
- 与操作系统无关的功能和缺陷修复直接进入 `main`，维护一份通用实现即可。
- Win7 专用实现必须隔离在 `//go:build win7compat` 变体中，不得进入标准构建热路径；可复用的抽象或通用修复应单独提取。
- 完整的 Win7 编译与验证步骤见 [docs/aicli/windows7-build.md](aicli/windows7-build.md)。

### 4.2 分支同步策略

Win7 适配已并入 `main`，不再存在需要双向同步的并行适配分支：

1. 通用功能、协议、数据模型和跨平台修复直接合入 `main`，Win7 兼容性由 `win7compat` 构建验证兜底。
2. 新增 Win7 适配提交直接进入 `main`，与通用改动同处一个可审查流程；提交主题应体现具体兼容目标。
3. 在 `main` 上修改 Win7 配置、路径、console、session、SQLite、依赖或 workflow 后，必须同时验证标准构建和 Win7 构建（见 [第 8 节](#8-windows-7-兼容规范)）。
4. 历史遗留的 `feat/win7-go120-compat` 分支不再开发新功能；如仍用于追溯 Win7 历史发布，仅在确需时执行单向 merge，且不得成为新适配的落点。

### 4.3 分支命名

建议使用以下格式：

```text
feat/<topic>
fix/<topic>
refactor/<topic>
docs/<topic>
test/<topic>
chore/<topic>
```

分支应聚焦单一目标。跨模块的大改动应拆成可独立验证、可独立审查的阶段。

历史 Win7 适配分支 `feat/win7-go120-compat` 的代码已合入 `main`，不再继续使用；新的 Win7 适配直接提交到 `main` 并带 `win7compat` build tag，提交主题应体现具体兼容目标，而不是再创建含义不清的平行 Win7 长期分支。

### 4.4 开始修改前

```bash
git status --short --branch
git fetch --prune origin
git log -1 --oneline
```

如果工作区已有修改：

- 先确认修改来源和归属；
- 不覆盖、不重置、不清理他人的未提交工作；
- 新改动应尽量避开无关文件；
- 无法隔离时，先保存补丁、stash 或使用独立 worktree，并明确记录。

## 5. 代码修改原则

1. **先复现，再修改。** Bug 修复应先定位可观察症状和最小复现路径。
2. **最小充分改动。** 不把无关重构、格式化或依赖升级混入功能修复。
3. **保持契约。** 修改 API、事件、配置、持久化格式或工具结果时，必须检查生产者、消费者、恢复路径和兼容层。
4. **错误必须可诊断。** 错误信息应包含必要上下文，如操作、路径、资源或阶段，但不得泄漏密钥和敏感内容。
5. **并发必须有生命周期。** 新 goroutine、channel、锁和后台任务必须说明退出、取消、超时和关闭行为。
6. **持久化必须考虑并发。** SQLite 改动要检查连接生命周期、busy timeout、事务边界、幂等性和多实例访问。
7. **生成物不入库。** 除非是明确维护的 fixture 或发布资产，否则不提交 `dist/`、日志、数据库、临时脚本、截图和本地二进制。

## 6. Go 后端规范

### 6.1 格式与组织

- 所有修改过的 Go 文件必须执行 `gofmt`。
- import 由 `gofmt`/`goimports` 风格维护，不手工制造分组差异。
- package 名使用简短小写名称；导出标识符必须有清晰语义。
- 优先返回错误，不在库代码中 `panic`；只有不可恢复的启动不变量才能在入口层失败退出。
- 使用 `%w` 包装需要上层判断的错误，并通过 `errors.Is` / `errors.As` 匹配。
- `context.Context` 放在参数首位，不存入长期对象；取消和 deadline 应沿调用链传播。

### 6.2 并发与资源

- 启动 goroutine 前明确谁负责取消和等待。
- 不允许用无界 goroutine、队列或缓存掩盖背压。
- 等待多个任务时，不能只依赖忽略 context 的 `Wait`；必须保证调用有界返回。
- `Close` 应尽量幂等；关闭后调用的行为需要测试。
- 文件、响应体、数据库连接和临时资源应在成功获取后立即安排释放。

### 6.3 测试

- 测试文件与实现放在同一 package 或明确的外部测试 package。
- 测试名使用 `Test<Subject>_<Scenario>` 或项目既有风格。
- 优先表驱动测试；失败信息包含输入、实际值和期望值。
- 避免依赖固定 sleep；优先 channel、condition、fake clock 或 eventually 断言。
- 修复回归必须添加能在修复前失败、修复后通过的测试。

常用命令：

```bash
cd backend

# 定向验证，开发过程中优先执行
go test ./internal/<package> -count=1

# 全量后端测试
go test ./...

# 静态检查
go vet ./...

# 构建全部 package
go build ./...
```

涉及竞态、并发队列、生命周期或共享状态时，按平台能力补充：

```bash
go test -race ./path/to/package
```

`-race` 不支持所有交叉编译目标；无法执行时应在变更说明中明确。

### 6.4 依赖管理

- 仅在确实改变依赖时运行 `go mod tidy`。
- 提交前检查 `go.mod` / `go.sum` diff，避免无关升级或降级。
- 标准依赖图和 Win7 依赖图必须分别维护，不能用标准 `go mod tidy` 意外覆盖 `go.win7.*`。

## 7. 前端规范

- 使用 TypeScript，避免无理由引入 `any`。
- 组件保持职责单一；状态、数据获取和展示逻辑按现有模块边界拆分。
- React effect 必须处理清理、依赖和异步竞态。
- 用户可见错误应提供可操作信息；内部上下文、密钥和原始敏感 payload 不应直接渲染。
- 修改交互、流式渲染、滚动、恢复或事件排序时，应补 reducer/component 测试；涉及真实浏览器行为时补 Playwright 测试。

常用命令：

```bash
cd frontend
pnpm lint
pnpm test
pnpm build
```

涉及浏览器端到端行为时：

```bash
pnpm test:e2e
```

不应默认更新 snapshot 或放宽断言来“修复”失败；先确认产品语义和根因。

## 8. Windows 7 兼容规范

### 8.1 代码边界

Windows 7 适配代码直接维护在 `main`，以 `win7compat` build tag 隔离。适配实现不得降低标准实现能力，标准构建和 Win7 构建均可独立构建、测试和发布。

改动归属按下表判断：

| 改动类型 | 落点 | 示例 |
| --- | --- | --- |
| 跨平台接口、通用协议和通用缺陷修复 | `main`（通用实现） | 通用 session 契约、与 OS 无关的并发修复 |
| 现代系统默认实现 | `main`（`!win7compat` 变体） | 现代 Windows API、当前 Go 工具链实现 |
| Windows 7 专用实现与回退 | `main`（`win7compat` 变体） | 缺失 Win32 API 的替代实现、旧 Console/PTY 处理 |
| Go 1.21 或兼容依赖调整 | `main`（`go.win7.mod` 依赖图） | 替代 Go 1.22+ API、依赖降级、兼容 shim |
| Win7 配置、会话、打包与验证 | `main` | `runtime.win7.yaml`、独立 session DB、Win7 workflow |

推荐实现方式：

- 通用接口和平台无关逻辑放在通用实现（`!win7compat`），Win7 只增加或替换平台实现。
- 优先使用 `//go:build win7compat` 与 `//go:build !win7compat` 成对隔离实现，避免在通用热路径中散布大量运行时版本判断。
- 文件名可以表达 Win7 语义，但真正的编译隔离必须依赖 build constraint；`_win7.go` 不是 Go 内建的 GOOS 后缀。
- 禁止在 Win7 路径中引入 Go 1.21+ 标准库 API、要求更高 Go 版本的依赖，或 Windows 7 不提供的系统 API。
- Win7 适配不得通过删除标准版功能来实现。必要时定义共同接口，并分别维护标准实现和 Win7 回退实现。
- 共享的 Win7 构建基础设施（workflow、`go.win7.mod`）维护在 `main`，但平台实现差异必须隔离在 `win7compat` build tag 变体中，不得进入标准构建热路径。

### 8.2 构建隔离

Win7 构建使用：

```text
Go 1.21.4
GOFLAGS=-modfile=go.win7.mod
-tags win7compat
CGO_ENABLED=0
```

相关资产包括：

- `backend/go.win7.mod`
- `backend/go.win7.sum`
- `backend/configs/runtime.win7.yaml`
- `backend/internal/aiclipaths/profile_win7.go`
- `.github/workflows/build-aicli-win7.yml`
- `docs/aicli/windows7.md`
- `docs/aicli/windows7-build.md`（完整的编译、测试与打包步骤）
- `scripts/build.ps1`（统一构建入口：`-Target windows|win7|both` + `-Tools aicli,aicli-console,runtime-server,ssh-client,sftp-client,ssh-keygen`）
- `scripts/build-aicli-win7.ps1` / `scripts/build-runtime-server-win7.ps1` / `scripts/build-ssh-sftp-clients-win7.ps1`（向后兼容薄壳，转发到 `build.ps1`，本地可重复构建）

标准构建必须继续使用 `backend/go.mod`，且不能因为 Win7 兼容代码改变默认 Profile。

### 8.3 Profile 默认值

| 构建 | 配置 | CLI 配置 | Runtime 配置 | Session 数据库 |
| --- | --- | --- | --- | --- |
| 标准 | `config.yaml` | `aicli.yaml` | `runtime.yaml` | `session_history.sqlite` |
| Win7 | `config.win7.yaml` | `aicli.win7.yaml` | `runtime.win7.yaml` | `session_history_win7.sqlite` |

新增默认路径时，应同时检查标准和 Win7 profile，防止两个构建写入同一持久化文件。

### 8.4 验证命令

在安装 Go 1.21.4 的环境中执行：

```bash
cd backend
GOFLAGS="-modfile=go.win7.mod" go test -tags win7compat \
  ./internal/aiclipaths ./internal/agentconfig ./internal/config \
  ./internal/chat ./cmd/runtime-server -count=1

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
GOFLAGS="-modfile=go.win7.mod" \
go build -tags win7compat -trimpath -o dist/aicli-win7.exe ./cmd/aicli
```

在 PowerShell 中使用：

```powershell
$env:GOTOOLCHAIN = 'go1.21.4'
$env:GOFLAGS = '-modfile=go.win7.mod'
$env:CGO_ENABLED = '0'
go test -tags win7compat ./internal/aiclipaths ./internal/agentconfig ./internal/config ./internal/chat ./cmd/runtime-server -count=1
```

若修改了 Win7 配置、路径、console、session、SQLite、依赖或 workflow，必须同时验证标准构建和 Win7 构建。

在提交或发布 Win7 相关改动前，检查改动没有混入无关功能，且与标准构建互不影响：

```bash
cd backend
GOFLAGS="-modfile=go.win7.mod" go test -tags win7compat \
  ./internal/aiclipaths ./internal/agentconfig ./internal/config \
  ./internal/chat ./cmd/runtime-server -count=1
go test ./... -count=1   # 标准依赖图回归
```

检查结果应满足：

- 改动包含预期的适配提交，`git diff --check` 无空白错误；
- 标准版和 Win7 版均通过各自的最低验证；
- 差异仅包含 Win7 适配及其必要测试、配置、依赖、工作流和文档；
- Win7 产物确实由 Go 1.21.4、`go.win7.mod` 和 `win7compat` 构建（`go version -m` 首行为 `go1.21.4`）。

## 9. API、配置与持久化兼容

### 9.1 API 与事件

修改 HTTP、SSE、runtime event、tool result 或 team outcome 时：

- 明确字段的必填、可选、默认值和错误状态；
- 保持旧客户端可解析，删除或改名字段需有迁移方案；
- 同步更新服务端、客户端、恢复/重放逻辑和文档；
- 测试成功、失败、取消、超时、重复投递和恢复场景。

当前 HTTP 路由以 `backend/internal/api/skills/handler.go` 为事实来源。

### 9.2 配置

- 新配置项必须定义默认值和零值语义。
- 环境变量、YAML、CLI flag 和热加载路径的优先级必须明确。
- 示例配置不得包含真实凭证。
- 修改 runtime 配置结构时，同步检查配置快照、文档生成、测试和 Win7 配置。

### 9.3 数据迁移

持久化 schema 或文件路径变化应满足：

- 老数据可继续读取或提供显式迁移；
- 迁移可重复执行并具有失败恢复策略；
- 不静默丢弃未知字段、未完成任务或历史记录；
- 对并发打开、升级中断和回滚做测试。

## 10. 文档规范

- 用户行为变化必须同步更新用户文档；内部契约变化更新对应设计或 API 文档。
- 当前事实与历史方案要明确区分；过期内容标记“历史”或迁入 `docs/working/` / `docs/analysis/`。
- 文档中的命令应从仓库根目录或明确工作目录执行，路径使用仓库相对路径。
- 不写本机绝对路径、用户名、token、内部 URL 或不可复现的环境假设。
- Markdown 标题逐级递进，代码块标注语言，链接使用相对路径。
- 大量自动备份文件不得作为正常文档提交。

## 11. 验证矩阵

根据改动范围选择最低充分验证。不能执行的项目必须在交付说明中说明原因。

| 改动范围 | 最低验证 |
| --- | --- |
| 仅文档 | 检查链接、路径、命令和 `git diff --check` |
| 单个 Go package | `gofmt` + 定向 `go test` |
| 跨后端模块 | 相关 package 测试 + `go test ./...` + `go vet ./...` |
| 并发/共享状态 | 定向测试 + `-race`（环境支持时） |
| 前端逻辑 | `pnpm lint` + `pnpm test` + `pnpm build` |
| 浏览器交互 | 上述前端检查 + 相关 Playwright 测试 |
| runtime-server 打包 | `pwsh scripts/package-runtime-server.ps1 ...` |
| 发布链路 | `pwsh scripts/verify-release.ps1 ...` 或等价 CI gate |
| Win7 相关 | 标准验证 + Go 1.21/`go.win7.mod`/`win7compat` 定向验证 |

测试失败时不得只报告“失败”；应区分：

- 本次改动引入的失败；
- 可稳定复现的既有失败；
- 环境、网络、端口、文件锁或工具链问题；
- 超时但尚未判定根因的情况。

## 12. 提交规范

### 12.1 提交内容

- 一个提交表达一个完整意图。
- 不混入无关格式化、生成物、调试日志或个人配置。
- 提交前检查：

```bash
git status --short
git diff --check
git diff --cached --stat
git diff --cached
```

### 12.2 提交信息

采用 Conventional Commits 风格：

```text
type(scope): subject
```

常用类型：

- `feat`：新增功能
- `fix`：缺陷修复
- `refactor`：不改变外部行为的重构
- `perf`：性能优化
- `test`：测试改动
- `docs`：文档改动
- `build`：构建或依赖
- `ci`：CI/workflow
- `chore`：维护性工作
- `revert`：回滚

示例：

```text
fix(chat): bound session shutdown wait
docs(dev): add repository development guidelines
ci(win7): verify Go 1.20 profile defaults
```

subject 使用祈使式或清晰的结果描述，不加句号。重大不兼容改动使用 `!`，并在正文中说明迁移方式。

### 12.3 禁止事项

未经明确授权，不执行：

- `git reset --hard`
- `git clean -fd`
- 强制推送共享分支
- 改写已发布 tag
- 删除或覆盖来源不明的未提交修改
- 通过跳过测试、放宽断言或隐藏错误制造“通过”结果

## 13. 代码评审清单

提交评审前确认：

- [ ] 改动目标和非目标明确；
- [ ] diff 中没有无关文件和生成物；
- [ ] 新行为有测试，修复有回归测试；
- [ ] context、超时、取消、goroutine 和资源关闭路径已检查；
- [ ] API、事件、配置和持久化兼容性已检查；
- [ ] 改动进入了职责正确的实现（通用实现或 `win7compat` 变体）；
- [ ] Win7 专用代码已隔离在 `win7compat` build tag 变体中，标准构建热路径未被破坏；
- [ ] 标准构建与 Win7 构建边界未被破坏；
- [ ] 用户可见变化已更新文档；
- [ ] 已运行与改动范围相匹配的验证；
- [ ] 已记录未运行检查、已知风险和后续事项；
- [ ] 没有凭证、内部地址、用户数据或敏感日志进入提交。

## 14. 发布规范

- 标准版本从 `main` 创建 `v*` 或 `aicli-v*` tag。
- Windows 7 适配版本从 `main` 创建 `win7-*` tag。
- `.github/workflows/release-aicli.yml` 负责标准发布；其中的 Win7 构建检查或共享制品不能替代 `win7compat` 构建的正式差异验证。
- `.github/workflows/build-aicli-win7.yml` 响应 `win7-*` tag 或手动触发，正式 Win7 tag 直接指向 `main` 上的目标提交。
- 创建 tag 前应确认工作区干净、本地与远端同步、目标 commit 正确、必要 gate 已通过。
- 完整的 Win7 编译、验证与打包步骤见 [docs/aicli/windows7-build.md](aicli/windows7-build.md)。

标准版本：

```bash
git switch main
git status --short --branch
git fetch --prune origin
git merge-base --is-ancestor origin/main HEAD
git tag vX.Y.Z
git push origin vX.Y.Z
```

Win7 版本：

```bash
git switch main
git status --short --branch
git fetch --prune origin
git merge-base --is-ancestor origin/main HEAD
# 提交前验证 win7compat 构建（GOFLAGS=-modfile=go.win7.mod -tags win7compat，见 windows7-build.md）
git tag win7-vX.Y.Z
git push origin win7-vX.Y.Z
```

## 15. 交付说明模板

完成开发任务时，建议按以下结构说明：

```text
变更：
- 修改了什么
- 为什么这样修改

验证：
- 运行的命令及结果

风险与限制：
- 未运行的检查及原因
- 兼容性、迁移或后续事项

工作区：
- 是否仍有不属于本任务的未提交修改
```

规范的目标不是增加流程负担，而是让每次改动都有清晰边界、可复现证据和安全的演进路径。
