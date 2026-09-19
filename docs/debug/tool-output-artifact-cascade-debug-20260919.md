# Tool Output → Artifact 级联审计与优化 调试文档

> 对应计划：`docs/plan/tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md`
> 日期：2026-09-19 ｜ 验证环境：Windows + pwsh ｜ 状态：P0-1/P0-2/P1-1/P1-2/P2/P3/O-1/O-2/O-4 全部实施并 E2E 验证通过

## 1. 问题域速查

工具原始输出（L4 raw）进入模型上下文（L1 visible）前经过三层治理，调试时按层定位：

```
进程 stdout/stderr
  └─ [executor capture 层] internal/executor/output_capture.go
       · DefaultRetainedOutputBytes = 256*1024（head 2/3 + tail 1/3 + marker reserve 192B）
       · 超限时保留首尾窗口，中间以 marker 标注
  └─ [gateway 治理层] internal/output/gateway.go
       · artifactArchiveMinBytes() = modelToolTextByteBudget/12（下限 256B）
       · 成功 + 低于阈值 → artifact_skipped=below_threshold（不归档）
       · 失败结果永远保留指针（恢复契约 §6.1）
       · artifact_read 窗口不重复归档（no-cascade，artifact_source_id 元数据）
  └─ [模型可见文本] internal/output/tool_result_content.go
       · formatTruncatedToolTextForModel：head/tail 截断 + 内嵌续读指引
       · truncationMarkerReserve 精确预留，budget 为硬上限
```

## 2. 关键常量与文件索引

| 常量/函数 | 位置 | 值/语义 |
|---|---|---|
| `DefaultRetainedOutputBytes` | internal/executor/output_capture.go:18 | 256KiB capture 上限 |
| `captureOutputMarkerReserve` | internal/executor/output_capture.go:21 | 192B |
| `artifactArchiveMinBytes()` | internal/output/gateway.go:321 | budget/12，下限 256B（P1-1 分层阈值） |
| `viewDefaultLimit=400` | internal/toolkit/tools/view.go | view 默认窗口行数（P0-1） |
| `viewByteBudgetReserveRatio=0.7` | internal/toolkit/tools/view.go | view 输出预算预留 |
| `grepByteBudgetReserveRatio=0.8` | internal/toolkit/tools/grep.go | grep 预算预留（P0-2） |
| `artifactReadDefaultLimitBytes()` | internal/toolkit/tools/artifact_read.go | ≤12KiB artifact 一次读完（P1-2） |
| `isArtifactReadWindow` / `artifactSourceIDMetadataKey` | internal/output/gateway.go:40 | 解引用不级联守卫 |
| O-1 计数器 | internal/observability/tool_output_artifact.go | `tool_output_archive_total`（layer/disposition）、`tool_output_truncation_total`、`tool_pointer_notice_total`、`tool_artifact_deref_total`（page=first\|followup）、`tool_artifact_deref_miss_total`；直方图 `tool_output_original_bytes`/`tool_output_model_visible_bytes`/`tool_artifact_deref_bytes` |
| O-2 快照 | internal/observability/tool_efficiency_snapshot.go | `ArtifactFlow`、`L1L4GapRatio`、flag：`artifact_deref_heavy`/`l1_l4_gap_present`/`archive_skipped_majority`；暴露于 internal/api/skills/handler.go:9765 |
| O-4 元数据透传 | internal/agent/tool_runtime_events.go `copyToolArtifactFlowMetadata` + internal/runtimeobserve/projector.go:131 allowlist | `output_original_bytes`/`output_model_visible_bytes`/`artifact_archived`/`artifact_skipped`/`artifact_id` |

## 3. E2E 驱动方法（aicli chat headless）

```powershell
# 1) 启动常驻 TUI（headless 避免交互选择器阻塞）
E:\projects\ai\ai-agent-runtime\bin\aicli.exe chat --headless --yolo --web-port 8103 --pprof --debug `
  --log-dir E:\projects\ai\ai-agent-runtime\.chat-logs-headless

# 2) 读写令牌（写操作需 X-AICLI-Token；只读端点免鉴权）
$tok = (Invoke-RestMethod "http://127.0.0.1:8103/web/api/token").token

# 3) 同步远程 invoke（注入 prompt → 等 turn 结束）
$hdr = @{ "X-AICLI-Token" = $tok; "Content-Type" = "application/json" }
$body = @{ prompt = "..." } | ConvertTo-Json
Invoke-RestMethod -Uri "http://127.0.0.1:8103/web/api/invoke" -Method Post -Headers $hdr -Body $body -TimeoutSec 420
```

响应关键字段：`status`（completed）、`usage.context_tokens`（可观测 L1 增长）、`screen.lines`（turn 全过程，含 `[tool]` 行）。

## 4. E2E 验证记录（2026-09-19，session_20260919100006_3H6CMKLl）

### 4.1 大输出截断 → artifact 指针 → 续读（P2/P1-2 主链路）✓

让 agent 生成 307500B 文本文件并 `Get-Content` 直接 stdout，观察到的工具结果：

- 头部元数据：`Total output lines: 3750` / `Total output bytes: 307500`
- 折叠标记：`[output truncated for history safety: omitted 247354 bytes from the middle]`
- 内嵌续读指引：`read via artifact_read(artifact_id=art_8eaa3d01b36a44e89581bf871c742a97, offset=<bytes>, limit=<bytes>)`
- 尾部独立指针行：`Full raw output artifact_id: art_8eaa3d01b36a44e89581bf871c742a97 size=262386 kind=text; ...`
- `artifact_read(offset=300000, limit=200)` 越界读：安静返回 `window=[262386,262386) | eof=true` 空窗口（语义：eof 标记而非报错，size 元数据可信）

### 4.2 小结果跳过归档（P1-1）✓

同会话中 `Measure-Object -Line`（几十字节输出）无任何 `Full raw output artifact_id` 指针行 → `artifact_skipped=below_threshold` 路径生效。

### 4.3 解引用不级联 ✓

artifact_read 的窗口结果未产生新的 artifact 指针（no-cascade 守卫生效，代码级契约见 `artifact_read_no_cascade_test.go`、gateway.go L143/L172）。

### 4.4 单测回归 ✓

```
ok  internal/output            0.536s
ok  internal/toolkit/tools    17.039s
ok  cmd/aicli/functions        2.086s
```

## 5. 调试陷阱与根因笔记

### 5.1 "307500 vs 262386" 字节数差异（易误判为归档溢出 bug）

**现象**：截断视图头部写 307500B，artifact 标注 size=262386B。

**根因（分层，非 bug）**：
- 307500 = 进程 stdout 真实字节数（executor 如实记录到元数据）
- 262386 = executor capture 层保留窗口 = head(2/3×262144=174762) + tail(87382) + marker reserve(192)
- 差值 45114B 是 capture 层折叠的中间部分，由 `[output truncated for history safety: omitted 247354 bytes from the middle]` 显式标注（omitted 计数含折叠标记自身开销）

**判断口诀**：`artifact size ≤ Total output bytes` 恒成立；两者差 = capture 折叠 + marker 开销。只有当 artifact size **无故大于** 262144+marker 时才需要怀疑归档层问题。

### 5.2 越界 offset 读返回空窗口而非报错

`artifact_read(offset > size)` 返回 `eof=true` + 空 window（`window=[size,size)`），不抛错。这是有意的幂等语义：模型凭指针续读时按 `eof=true` 收敛即可，调试时不要把空窗口误判为 artifact 丢失。

### 5.3 headless 模式守卫面

`--headless` 时四个交互选择器全部短路：provider/model×2/reasoning/stream（chat_preferences.go、chat_options.go L321/L372），folder trust 亦跳过。若 headless 启动卡死，先查是否残留交互分支（`ensureProcessFolderTrust(opts.TrustGrant, !opts.NoInteractive && !opts.Headless)`）。

### 5.4 Windows/pwsh 环境

- 相对路径执行 `..\bin\aicli.exe` 会报「术语不会被识别」，必须用绝对路径
- 大输出查看用 `2>&1 | Select-Object -Last N` 防截断
- 后台进程用 `background_task` + tcp probe，状态用 `task_output`

## 6. 观测与验证命令速查

```powershell
# 归档/截断计数器（O-1）在 observability 包注册，快照暴露于 skills handler（O-2）：
#   GET /api/skills 下的 tool_efficiency 快照 → ArtifactFlow / L1L4GapRatio / flags
# O-4：runtime 事件 payload 校验 allowlist（internal/runtimeobserve/projector.go:131）
#   期望字段：output_original_bytes, output_model_visible_bytes, artifact_archived, artifact_id, artifact_skipped

# L1/L4 gap 判定（验收断言 4）：l1_l4_gap_present 应为 false
#   gap = output_original_bytes 与 output_model_visible_bytes 的差异率 > 5% 才置位
# artifact_deref_heavy：followup 页占比 > 30% 才置位（正常链路应为 false）

# 快速冒烟：单测覆盖全部契约
go test ./internal/output/ ./internal/toolkit/tools/ ./cmd/aicli/functions/ -count=1
```

## 7. 遗留观察项（非阻塞）

1. capture 层折叠计数（omitted bytes）与归档 size 的差值目前需人工对账；O-1 直方图已覆盖两端口，后续可加差值指标。
2. `web/api/invoke` 的 `screen.lines` 中 tool 行只展示摘要（668ms/865ms 等），原始工具结果需看会话存储或 runtime 事件流。

## 8. headless 模式下随机端口的获取（--web-port 未指定时）

### 8.1 随机端口的分配条件（重要前提）

`cmd/aicli/pprof.go:117-131 resolveLoopbackServerAddr` 的优先级链：

1. `--web-port <n>`（显式，越界直接报错）
2. `AICLI_PPROF` 环境变量（原样作为地址）
3. `--pprof` 或 `--debug` → 监听 `127.0.0.1:0`，**由 OS 分配随机空闲端口**
4. 全部未设置 → 返回空串，**根本不启动 loopback 服务器**

⚠️ 因此 `--headless` 单独使用时没有任何远程端点；`--headless` 至少要搭配 `--debug`（或 `--pprof`、`AICLI_PPROF`）才会进入随机端口路径。

### 8.2 获取随机端口的三种方式

**方式 1：解析 stderr 启动行（推荐，进程自描述）**

随机端口分配后 main.go:109-119 会向 stderr 打印全部端点 URL：

```
Info: pprof endpoint enabled: http://127.0.0.1:51994/debug/pprof/
Info: chat render status endpoint: http://127.0.0.1:51994/debug/chat/status (JSON; ?format=text for plain text)
Info: chat screen content endpoint: http://127.0.0.1:51994/debug/chat/screen (JSON; ?format=text for plain text)
Info: chat debug endpoints list: http://127.0.0.1:51994/debug/endpoints (JSON; ?format=text for plain text)
Info: chat web client / remote invoke endpoint: http://127.0.0.1:51994/web/ (POST http://127.0.0.1:51994/web/api/invoke)
Info: web write token (X-AICLI-Token): <32hex> (POST /web/api/* 必需)
```

PowerShell 提取示例：

```powershell
$stderr = & E:\...\bin\aicli.exe chat --headless --debug ... 2>&1   # 或后台任务的 output
$port = [regex]::Match(($stderr -join "`n"), 'http://127\.0\.0\.1:(\d+)/debug/pprof/').Groups[1].Value
```

**方式 2：按 PID 反查监听端口（进程已在跑、stderr 已丢失时）**

```powershell
$pid = (Get-Process aicli).Id
Get-NetTCPConnection -OwningProcess $pid -State Listen |
  Select-Object -ExpandProperty LocalPort   # 仅一个 127.0.0.1 监听时即端口
```

（等价 `netstat -ano | findstr "<pid>"`；实测验证：PID 10320 → 51994）

**方式 3：预检启动行落盘**——`--log-dir` 落的是会话 JSON 日志（不含端口信息），端口只走 stderr，不要在日志文件里找。

### 8.3 随机端口下的令牌

令牌同样默认每进程随机（`/web/api/token` 响应 `source: random`，重启轮换）。随机端口场景下令牌也只在启动行出现一次；要固定令牌用 `--web-token <t>` 或 `AICLI_WEB_TOKEN`（`source: explicit`，重启不轮换）。

### 8.4 实测记录（2026-09-19）

`aicli chat --headless --yolo --debug`（无 --web-port）→ OS 分配 51994 → `GET /web/api/token` 返回 `source=random, len=32` → `GET /debug/chat/status` 返回 `available=true, session=session_20260919101556_wtwpcwM9`。启动行完整内容见后台任务 stderr（job_ref_37b2c0967572 输出）。


## 案例三：`chat --headless` 启动报 `Error: provider 'gemini_local' not found`（exit 1）

日期：2026-09-19 ｜ 严重度：中（chat 命令完全不可用）｜ 状态：已修复

### 现象

```text
PS> aicli chat --headless --yolo --debug
Info: pprof endpoint enabled: http://127.0.0.1:51484/debug/pprof/
...（loopback 端点全部正常打印）
Error: provider 'gemini_local' not found     ← 进程 exit 1
```

### 结论（先答用户问题）

**这个错误确实导致服务启动失败**。loopback 调试服务器先启动（所以能看到 5 个 Info 端点行），随后 provider 解析硬失败，进程以 exit 1 退出——服务端口虽然短暂监听过，但 chat 会话永远建立不起来，属启动失败。

### 根因链（三层叠加）

1. **配置引用了不存在的 provider**：`~\.aicli\.env` 写了 `PROVIDERS_DEFAULT=gemini_local`，
   config.yaml 的 `providers.default_provider: ${PROVIDERS_DEFAULT:-nvidia}` 被替换为
   `gemini_local`。但该名字只存在于 `concurrency.per_provider_limits`（残留的限流配置），
   不在 `providers.items` 里 → `resolveProviderExecutionContext` 硬报错
   （`cmd/aicli/commands/provider_context.go:96`）。

2. **godotenv 不覆盖已存在环境变量**（`backend/cmd/aicli/main.go:43-49`）：进程启动时若 shell
   已 export `PROVIDERS_DEFAULT`，`.env` 里的同名变量**不会生效**。本次排障中先改了 `.env`
   又误判"改了没用"，实际是 shell 环境变量一直在赢。**清环境变量优先于改 .env**。

3. **（健壮性缺陷，已修）失效偏好不降级**：`chat_preferences.go:97` 原来在 session 存储的
   provider 已被删除、protocol 兜底也失败时，仍无条件返回失效名字，跳过了
   workspace → config → default 的整条降级链。已在本次修复中改为继续降级
   （session 偏好只在其指向的 provider 仍可用时生效）。

### 排查路径（关键证据与排除法）

| 检查点 | 命令 | 结果 |
|---|---|---|
| config 里是否有 gemini_local | providers.items 键名扫描 | 只有 `concurrency.per_provider_limits` 残留 |
| shell 环境变量 | `[Environment]::GetEnvironmentVariable('PROVIDERS_DEFAULT')` | `gemini_local`（父进程继承，非 User/Machine 级） |
| `.env`（godotenv 加载点） | `~\.aicli\.env` | `PROVIDERS_DEFAULT=gemini_local` ← 元凶 |
| workspace 偏好 | `~\.aicli\workspace\<hash>\chat-prefs.yaml` | `beta`（无关） |
| session 存储偏好 | session_history.sqlite `metadata_json` | `unsee` / `opencode.ai`（无关） |

### 修复与验证

1. **用户配置修复**：`~\.aicli\.env` 的 `PROVIDERS_DEFAULT=gemini_local` → `nvidia`。
2. **代码健壮性修复**：`chat_preferences.go` 失效 session provider 降级（见上），
   `go build ./...` + `go test ./cmd/aicli/commands/ -run "Chat|Provider"` 全绿，
   二进制已重编译进 `bin/aicli.exe`。
3. **验证**：清掉 shell 环境变量后 `aicli chat --headless --yolo --debug --no-interactive`
   exit 0，无 provider 错误；`--provider nvidia` 显式指定也正常（banner 正确显示 nvidia）。

### 经验法则

- aicli 的 provider 解析优先级：`--provider` flag > shell 环境变量（同名时压过 .env）>
  `.env` > session 持久化偏好 > workspace chat-prefs > config `aicli.chat.default_provider` >
  `providers.default_provider`。排查时按此顺序自上而下排除。
- `godotenv.Load` 的标准语义是"不覆盖已存在的环境变量"，改 `.env` 前先确认 shell 里没有同名变量。
- config 里删 provider 时，记得同步清理 `concurrency.per_provider_limits`、auth store、
  分组引用和 `.env` 里的派生变量——残留引用会在启动解析期以
  `provider 'xxx' not found` 形式暴露。
