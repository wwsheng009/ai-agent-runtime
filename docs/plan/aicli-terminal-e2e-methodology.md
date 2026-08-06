# aicli Terminal E2E 测试方法与观测手段

状态：**active / 可重复执行**（2026-08-06 实机验证通过）

上位规范：`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`

实施契约：`docs/plan/aicli-tui-owned-render-simplification-implementation-guide.md`

适用范围：`backend/cmd/aicli/` 的 interactive 渲染路径（native scrollback + owned viewport）在真实 Windows 终端宿主上的验收。

## 0. 本文定位

本文回答两个问题：

1. **怎么跑**：本仓库现有的三层 aicli e2e 各是什么、各自的前置条件、命令、耗时与适用场景。
2. **怎么看**：每次 e2e 运行的产物（manifest、dump、日志）里哪些字段是观测证据、怎么解读、哪些字段过不了就算失败。

三层与验收目标的关系：

| 层 | 名称 | 是否真实 provider | 是否真实终端 | 用途 |
| --- | --- | --- | --- | --- |
| L1 | opencode live E2E | 是 | 是（Windows Terminal） | 最终实机验收门禁 |
| L2 | fixture render E2E | 否 | 是（Windows Terminal） | 不耗 API 的 scrollback 回归验证 |
| L3 | 进程内交互 e2e | 否（fake executor） | 否（os.Pipe + VT 重建） | CI 友好的快速回归 |

L1 与 L2 都依赖交互式 Windows 桌面会话（`EnumWindows` + UI Automation 枚举 `CASCADIA` 窗口），**不能在无头/服务会话跑**；L3 无此限制。

## 1. 前置条件（L1 / L2 通用）

1. Windows NT 交互式桌面会话（`$env:OS` 必须为 `Windows_NT`，当前登录会话内执行）。
2. Windows Terminal 已安装，`wt.exe` 在 PATH（通常位于 `C:\Users\<user>\AppData\Local\Microsoft\WindowsApps\wt.exe`）。
3. Go toolchain 可用（脚本内部会执行 `go build`）。
4. L1 额外需要 opencode 凭证，二选一（按优先级）：
   - 环境变量 `OPENCODE_API_KEY`；
   - `~/.local/share/opencode/auth.json` 中的 `opencode-go` 或 `deepseek` 条目（脚本按此顺序查找 `key` 字段）。
5. L1 会真实消耗 provider 配额（默认 `deepseek-v4-flash`、`reasoning_effort=max`，单次约 40 行 marker 输出）。

## 2. L1：真实 provider + Windows Terminal live E2E

脚本：`scripts/test-aicli-opencode-windows-terminal-e2e.ps1`

```powershell
cd E:\projects\ai\ai-agent-runtime
pwsh -NoProfile -File scripts/test-aicli-opencode-windows-terminal-e2e.ps1 -TimeoutSeconds 300
```

参数：

| 参数 | 默认 | 范围 | 说明 |
| --- | --- | --- | --- |
| `-TimeoutSeconds` | 300 | 30–900 | 从启动 runner 到完成整轮验证的总预算 |
| `-StartupTimeoutSeconds` | 60 | 10–180 | 等待模型输出首个 marker 的预算 |
| `-KeepWindow` | off | - | 保留真实 Windows Terminal 窗口供人工观察，不自动关闭 |

执行流程：

1. `go build` 产出 `output\aicli-terminal-e2e\aicli-live-e2e.exe`。
2. 生成唯一 `runID`，构造要求模型输出 **40 个严格递增 marker 行** 的测试 prompt，并注入唯一的 `reasoningSentinel` 文本。
3. 写 `config.yaml`（`opencode.ai/zen/go`、`deepseek-v4-flash`、`reasoning_effort=max`、`stream=true`、`yolo=true`、`tools=disabled`）与 `run-chat.ps1` runner；API key 只经 `credential.bridge` 文件注入进程环境，**永不进入命令行或 dump**。
4. 在真实 Windows Terminal 新窗口（标题 `aicli-opencode-e2e-<runID前12位>`）内执行 `aicli chat --prompt ...`。
5. 周期性用 UI Automation 捕获窗口的 `DocumentRange`（整个 scrollback）与 `GetVisibleRanges()`（当前视口），直到 40 个 marker 全部可见且渲染稳定（连续 3 次采样 SHA256 一致）。
6. 运行全部断言（见 §5），向终端发送 `/exit`，等待 runner 退出并确认 `runner_exit_code = 0`。
7. 全部证据写入 `output\aicli-terminal-e2e\opencode-wt-<runID>\`，并对 `.txt/.log/.json/.jsonl/.yaml/.ps1/.cmd` 做 `[REDACTED]` 清洗（防止 key 落盘）。

## 3. L2：fixture 版 Windows Terminal E2E（无需真实 provider）

脚本：`scripts/test-aicli-windows-terminal-e2e.ps1`

```powershell
cd E:\projects\ai\ai-agent-runtime
pwsh -NoProfile -File scripts/test-aicli-windows-terminal-e2e.ps1 -TimeoutSeconds 45
```

参数：`-TimeoutSeconds`（默认 45，fixture 保持窗口的时长）、`-KeepWindow`。

执行流程：

1. `go build` 产出 `output\aicli-terminal-e2e\aicli-render-fixture.exe`（源码在 `backend/cmd/aicli-render-fixture`）。
2. 通过 `wt.exe -w new --size "100,24"` 打开真实终端，fixture 以 `AICLI_RENDER_FIXTURE_HOLD_MS` / `AICLI_RENDER_FIXTURE_RUN_ID` 环境变量控制时长与窗口标识。
3. fixture 渲染 **72 条 history 行**（`AICLI-E2E-HISTORY-000` .. `AICLI-E2E-HISTORY-071`）+ prompt/status marker（`AICLI-E2E-PROMPT-VIEWPORT`、`AICLI-E2E-STATUS-VIEWPORT`）+ Markdown marker（`AICLI-E2E-MARKDOWN-HEADING/BOLD/CODE`）。
4. 与 L1 相同的 UI Automation 机制采样与断言（见 §5）。

适用场景：每次渲染改动后的快速 scrollback 回归，不消耗 API、无网络依赖；跑通后再上 L1。

## 4. L3：进程内交互 e2e（go test）

文件：`backend/cmd/aicli/commands/chat_tty_live_loop_test.go`

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./cmd/aicli/... -count=1
```

可选强化：

```powershell
go test -race ./cmd/aicli/ui ./cmd/aicli/commands -count=1
go vet ./cmd/aicli/ui ./cmd/aicli/commands
```

> 注意：Go module 根在 `backend/`，必须在 `backend` 目录下执行，否则报 `directory prefix cmd\aicli does not contain main module`。

机制（本机 ConPTY 不可用，`CreateProcess` 挂 `PSEUDOCONSOLE` attribute 会 `0xC0000142 STATUS_DLL_INIT_FAILED`，已废弃 `chat_tty_conpty*`）：

- `os.Pipe` 替换 `os.Stdin`，向真实 chat 主循环注入脚本化输入（`waitReady` 步骤先轮询会话回到 Ready 再发下一行，模拟真实排队语义）；
- `runChatLoop` 真实主循环（`prepareInteractiveRead` → 逐行读取 → slash 命令处理 → 退出）；
- `captureSurfaceStdout` 捕获真实渲染字节流（VT 序列）；
- `ui/vt.Screen` 把字节流重建为"用户看到的屏幕"并断言。

覆盖场景：普通多轮对话、未知命令错误渲染、`/clear` 确认流（取消+确认）、`/help` 帮助列表、`!` 真实 shell 子进程执行、输入回显与 assistant 渲染。

## 5. 断言清单与观测手段

### 5.1 观测手段（L1/L2 共用）

- **UI Automation 文本模型**：内嵌 C# helper（脚本内 `Add-Type`）`EnumWindows` 过滤 `CASCADIA` 类名窗口，按标题精确匹配本次运行窗口（标题带 runID，避免误抓其他终端）。
  - `TextPattern.DocumentRange.GetText(-1)` → **整个 scrollback**（finalized history 所在）；
  - `TextPattern.GetVisibleRanges()` → **当前视口**（active 渲染所在）。
- **稳定快照**：渲染稳定后连续采集 3 次，`document_sha256` 与 `visible_sha256` 必须一致，否则视为仍在抖动、不通过。
- **退出观测**：按 executable path + run ID 定位窗口，由独立 helper 经 `AttachConsole + WriteConsoleInputW` 发送 `/exit`，再读取 runner 的退出码；`forced cleanup` 应为 0（即未走杀进程兜底）。

### 5.2 L1 断言清单（真实 provider）

| 断言 | 通过条件 | 失败含义 |
| --- | --- | --- |
| markers | 40/40 各恰好出现一次 | 有重画/重复提交 |
| marker 顺序 | 40 个 marker 下标严格递增 | 历史乱序 |
| scrollback 边界 | marker01 在 `DocumentRange` 中、不在 `VisibleRanges` | 早期历史没进 native scrollback（或视口断言失效） |
| 最新 tail | marker40 在 `VisibleRanges` 中 | 最新内容不可见 |
| reasoning sentinel | 唯一 reasoning summary 完整出现且位于 marker01 之前，仅一次 | reasoning 位置/去重异常 |
| raw 协议标签 | 无 `assistant.reasoning` / `llm.request.started` / `llm.request.finished` 裸文本 | 协议事件泄漏进渲染 |
| blank-line gaps | 异常空白行间隔数 = 0 | planner 丢弃/插入非语义空行 |
| UIA 稳定 | 3/3 快照 SHA256 稳定 | 渲染抖动或后台线程在写 |
| 退出 | `/exit` 已发送且已确认，`runner_exit_code = 0`，`forced cleanup = 0` | 退出路径或清理兜底异常 |

### 5.3 L2 断言清单（fixture）

- 72 个 `AICLI-E2E-HISTORY-xxx` 各出现一次（increment history exactly-once）；
- 最旧行移出可见视口但仍留在 `DocumentRange`（scrollback 语义）；
- 最新行可见；
- Markdown marker 在宿主 `DocumentRange` 中只出现渲染后的 heading/emphasis/code 内容，**不泄漏 raw Markdown 标记**。

### 5.4 产物解读

每次 L1/L2 运行产出目录 `output\aicli-terminal-e2e\opencode-wt-<runID>\`（或 `aicli-render-fixture` 对应目录）：

| 文件 | 内容 | 用途 |
| --- | --- | --- |
| `manifest.json` | 全部断言结果、窗口/PID/标题、provider/model 元数据、UIA 快照 SHA256、退出码 | 验收证据（报告时引用此文件） |
| `uia-document-full.txt` | 完整 scrollback dump | 人工核查历史内容 |
| `uia-visible-ranges.txt` | 当前视口 dump | 人工核查 active 渲染 |
| `sessions\` | 本次 chat session 数据 | 排查会话状态 |
| `chat-logs\` | chat 日志 | 排查事件序列 |
| `aicli.log` | aicli 进程日志 | 排查运行期错误 |
| `config.yaml` / `run-chat.ps1` / `credential.bridge` | 运行配置与凭证桥 | key 不落 dump；credential.bridge 运行后清理 |

报告格式参考（与真实运行输出一致）：

```
PASS: real Windows Terminal + UI Automation live E2E completed.
Markers: 40/40 present exactly once
Scrollback: marker01 full=True visible=False | marker40 visible=True
UIA evidence: stable=True samples=3 document_sha256=<hash> visible_sha256=<hash>
Ordering: reasoning_before_marker01=True | marker_indices_strictly_increasing=True
Raw protocol labels: assistant.reasoning=False | llm.request.started=False | llm.request.finished=False
Exit: /exit sent=True confirmed=True | runner_exit_code=0
Manifest: output\aicli-terminal-e2e\opencode-wt-<runID>\manifest.json
```

## 6. 排障

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `This E2E requires an interactive Windows desktop` | 非交互/服务会话 | 改为本地登录的 Console 会话执行 |
| `OpenCode credential unavailable...` | 无 `OPENCODE_API_KEY` 且 auth.json 无 `opencode-go`/`deepseek` | 配置凭证后重跑 |
| `failed to build ...` | Go 构建失败 | 先跑 L3 确认代码可测 |
| 找不到窗口/超时 | 标题匹配失败或窗口被占用 | 关闭多余 WT 窗口；确认 `wt.exe` 在 PATH |
| UIA 快照不稳定 | 渲染在抖动（如 scrollback 重排） | 复现并对照 L3 定位；按实施契约的 sticky-top/continuity 规则检查 |
| marker 缺失或重复 | planner 重放或丢行 | 对照 `history_effect_planner.go` 的 FragmentID/SourceRange 归属 |
| `cmd\aicli does not contain main module` | 在仓库根执行 go test | `cd backend` 后再跑 |

## 7. 执行顺序建议

1. 日常/提交前：L3（`go test ./cmd/aicli/... -count=1`，必要时加 `-race`）。
2. 渲染改动后：L2（fixture，30–60 秒，不耗 API）。
3. 发布/验收前：L1（真实 provider，3–5 分钟），把 `manifest.json` 路径写进验收记录。

三者不是替代关系：L3 证明渲染逻辑正确，L2 证明 native scrollback 在真实宿主上的语义，L1 证明真实事件流 + 真实终端的端到端行为。
