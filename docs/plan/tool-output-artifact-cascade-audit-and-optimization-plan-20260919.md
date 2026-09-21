# Tool Output Artifact 链路审计与二次读取优化方案

- 日期：2026-09-19
- 状态：P0/P1/P2/P3 与 §11 观测性 O-1～O-4 已全部实施，构建与测试全绿
- 关联修复：指针级联修复（`internal/output/gateway.go`、`internal/output/tool_result_content.go`）
- 关联文档：`docs/aicli/tool_output_contract.md`

## 实施状态（2026-09-19 更新）

| 项 | 状态 | 落点 |
| --- | --- | --- |
| P0-1 view 默认窗口字节感知 | 已实施 | `internal/toolkit/tools/view.go`（`viewDefaultLimit=400`、`viewMaxLimit=2000`、`viewOutputBudgetBytes=32KiB`；2026-09-20 起不再派生自 `output.ModelToolTextByteBudget()`） |
| P0-2 grep 字节预算硬上限 | 已实施 | `internal/toolkit/tools/grep.go`（`grepOutputBudgetBytes=32KiB`、`results_truncated`/`truncation_reason`；2026-09-20 起不再派生自 `output.ModelToolTextByteBudget()`） |
| R-1 工具自持输出预算（2026-09-20 增量） | 已实施 | 新增 `internal/toolkit/tools/tool_output_budget.go`：`view/grep/fetch/artifact_read=32KiB`、`glob/ls=16KiB`；view 另有 2000 行硬上限 |
| R-2 `skip_render_truncation` 统一契约（2026-09-20 增量） | 已实施 | 自持预算工具经 `stampToolOwnsOutput` **无条件** stamp `toolresult.MetadataSkipRenderTruncationKey`（不再依赖语义不准的 `truncated`），L4 对这类结果不再二次折叠 |
| R-3 shell 输出窗口自持（2026-09-20 增量） | 已实施 | `internal/toolkit/tools/tool_output_budget.go` 的 `ownShellOutputWindow`：`bash`（单命令/批次）与 `aicli_exec` **先归档完整 capture、再 head-only 折叠到 `shellOutputBudgetBytes=32 KiB`、最后 stamp** `skip_render_truncation`；L4 不再折叠 shell 输出（见 §0.3） |
| P1-1 Gateway 归档分级 | 已实施 | `internal/output/gateway.go`（`artifactArchiveMinBytes()`、`artifact_skipped=below_threshold`） |
| P1-2 artifact_read 默认窗口对齐上界 | 已实施 | `internal/toolkit/tools/artifact_read.go`（`artifactReadDefaultLimitBytes()` = max limit） |
| P2 截断视图续读指引 | 已实施 | `internal/output/tool_result_content.go`（`formatTruncatedToolTextForModel`） |
| P3 shell 落盘阈值常量去重 | 已实施 | `backend/cmd/aicli/functions/shell.go`、`internal/toolkit/tools/bash.go` |
| O-1 埋点与计数器/直方图 | 已实施 | `internal/observability/tool_output_artifact.go`（archive/truncation/pointer/deref 计数器 + 字节直方图） |
| O-2 Snapshot + status 暴露 | 已实施 | `tool_efficiency_snapshot.go`（`ArtifactFlow`、`L1L4GapRatio`、三个派生 flag）、`internal/api/skills/handler.go:9765` |
| O-3 view/grep/render 埋点 | 已实施 | `view.go`、`grep.go`、`tool_result_content.go`、`gateway.go`、`artifact_read.go`、`bash.go` |
| O-4 EventToolFinished metadata | 已实施 | `internal/agent/tool_runtime_events.go`（`copyToolArtifactFlowMetadata`：`output_original_bytes`、`output_model_visible_bytes`、`artifact_archived`、`artifact_skipped`、`artifact_id`）+ `runtimeobserve/projector.go` allowlist 扩展；`output_truncated_by_lines/bytes`、`pointer_notice_kind`、`deref_*` 等高基数维度由低基数计数器覆盖，事件侧按需补充 |
| §10.6 单行截断诚实化（本轮） | 已实施 | `internal/toolkit/tools/view.go`：单行 >2000 字符由静默 `...` 改为诚实标记 `…[line truncated: N more chars]`；`viewReadResult` 新增 `LongLinesTruncated`/`HiddenBytes`/`OriginalBytes`；metadata 暴露 `long_lines_truncated`/`hidden_bytes`；view 层接入 `tool_output_original_bytes{layer=l1_view}` 直方图 |
| F-4 frontend Artifact Flow 面板（本轮） | 已实施 | `frontend/src/pages/usage-analytics/artifact-flow-panel.tsx` + `getToolEfficiencySnapshot()`（`api/runtime/analytics.ts`）+ 类型（`types/runtime/analytics.ts`）+ i18n（zh-CN/en-US usage-analytics）+ `overview.tsx` 挂载 + `artifact-flow-panel.test.tsx`（10 用例） |
| F-5 micro web client 分析页 Artifact Flow（本轮） | 已实施 | `web_analysis_handlers.go` 新增 `GET /web/api/analysis/tool_efficiency`（`observability.SnapshotToolEfficiency()` 同结构体透传，先于 service==nil 检查）+ `web/js/analysis.js` `renderAnalysisEfficiency()`（四卡 + 低效信号 + 降级矩阵与 F-4 同口径）+ `web/index.html` `#analysis-efficiency` 容器 + handler 测试契约断言 |

验证口径：`go build ./...`、`go vet`、目标包 `go test` 全绿；
前端 `tsc -b`、`eslint`、i18n gate、`vite build`、`vitest run`（313 文件 / 2584 测试）全绿。

本轮增量验证（2026-09-19）：`go build ./...` 通过；`go test ./internal/observability/ ./internal/output/ ./internal/toolkit/tools/ ./internal/agent/ -count=1` 全绿。

## 0. 工具自持输出预算与 skip_render_truncation 契约（2026-09-20 增量）

L4（`internal/output/tool_result_content.go` 的 `formatTruncatedToolTextForModel`）是一刀切兜底：任何超预算文本都被 head/tail 折叠。本轮把「窗口所有权」下放到工具本身——工具用自己的预算截断、在 metadata 里给出续读/收窄指引，并 **无条件** stamp `skip_render_truncation`，L4 只对未 stamp 的结果做兜底折叠。

| 工具 | 自持预算 | 截断语义（metadata / 正文标记） | stamp |
| --- | --- | --- | --- |
| `view` | 32 KiB + 2000 行 | 行窗口 + 字节窗口；单行超长 `…[line truncated: N more chars]`；`long_lines_truncated`/`hidden_bytes` | ✅ |
| `grep` | 32 KiB | 保留完整前导匹配（绝不切中间）；`results_truncated`/`truncation_reason=byte_budget` + 收窄 next_step | ✅ |
| `artifact_read` | 32 KiB（扣除 header reserve） | 字节窗口 + `next_offset`/`artifact_eof` 续读 | ✅ |
| `glob` | 16 KiB | 保留前导路径；`truncated`/`next_action` | ✅ |
| `ls` | 16 KiB | 保留前导条目；`truncated`/`limit_hit`/`next_action`（不再用 error 表达截断） | ✅ |
| `fetch` | 32 KiB | rune 安全前缀截断 + `returned_bytes`/`truncated`/`next_action`（完整内容走 `download`） | ✅ |
| `bash` / `aicli_exec` | 32 KiB 模型窗口（executor capture 上限 256 KiB）+ 磁盘 artifact | 工具自身 head-only 折叠：正文 `[output window: showing the first N of M bytes …]`，metadata `truncated`/`output_window_bytes`/`output_window_total_bytes`；完整 capture 先入 artifact store（`artifact_id`=`artifact_source_id`，`artifact_read` 可按字节翻页），磁盘路径仍发布 | ✅ |
| 其它（`write`/`edit`/`multiedit`/`apply_patch`/`append_write`/`todos`/`web_search`/`download`/`git_worktree`/…） | 无自持预算 | — | ❌ L4 是唯一安全网 |

设计边界：只有「自持预算 + 提供续读/收窄契约」的工具才允许 stamp；没有自持预算的工具若也 stamp，L4 兜底将失效，超长输出会直接灌进上下文。

本轮验证（2026-09-20）：`go vet ./internal/toolkit/tools/` 通过；`go test -count=1 ./internal/toolkit/tools/ ./internal/output/... ./internal/toolresult/... ./internal/agent/ ./internal/observability/` 全绿（`internal/knowledge` 为既有构建破损，与本轮无关）。

## 0.1 L4 兜底折叠改为 head-only（2026-09-20 增量）

`formatTruncatedToolTextForModel` 不再做 head/tail「中间挖空」：超出 `modelToolTextByteBudget`（默认 12 KiB）的文本只保留**前 N 行**，随后是显式提示：

```
[output truncated for history safety: showing the first 118 of 600 lines; omitted 482 lines (18320 bytes) from the end]
[next step: re-issue the same call with a narrower window to read the omitted tail — view: offset=<next line, 0-based> plus a smaller limit; grep/shell: narrow the pattern, path or command output instead of repeating the identical call]
```

原因：模型无法对「文本中间的洞」分页，`artifact_read` 自身的输出在真实会话里也会被预算折叠，不能作为唯一恢复路径。因此改为「首 N 行 + 省略行数/字节数 + 下一步收窄指引」的契约；`truncationMarkerReserve` 仍按位数上界精确预留（header + head + notice ≤ budget），`firstFailureLine` 继续把被丢弃尾部里的最早错误行提升到 header。折叠提示不再承诺 `artifact_read` 分页；artifact 指针行（`Full raw output artifact_id: …`）保持不变。

验证（2026-09-20）：`go build ./...`、`go vet ./internal/output/ ./internal/toolkit/tools/` 通过；`go test -count=1 -p 2 ./internal/output/... ./internal/toolkit/tools/ ./internal/agent/ ./internal/observability/ ./internal/toolresult/` 全绿。

## 0.2 行中截断（单行超预算）的诚实提示（2026-09-20 增量）

§0.1 的 head-only 折叠在多行载荷上按行边界收尾，但保留了一个回退分支：当**首行本身**就超出 body 预算（或窗口内最后一个换行过早、不足预算一半）时，`headLinesWithinBudget` 只能返回字节前缀，窗口停在**行中**。此时按行计数渲染的通知会自相矛盾：

```
[output truncated for history safety: showing the first 9 of 9 lines; omitted 0 lines (28505 bytes) from the end]   ← 修复前（真实会话实测）
[next step: … view: offset=<next line, 0-based> …]                                                                 ← 行偏移无法恢复行中截断
```

修复：`partialLineBytes` 识别「head 不以换行结尾」，改走 `truncationMarkerPartial`，用字节口径描述损失并指向字节区间恢复：

```
[output truncated for history safety: showing 8 complete lines plus the first 11692 bytes of line 9; omitted 28308 bytes from the end]
[next step: this window stops mid-line, so a line offset cannot resume it — read the raw output pointer below by byte range (artifact_read with offset=<byte offset>) or re-issue a narrower call instead of repeating the identical one]
```

`truncationMarkerReserve` 取两种通知在上界位数下的较大者，`header + head + notice ≤ budget` 不变式不变（实测 40174 字节单行载荷 → 渲染 12287 / 预算 12288，且 shown + omitted 与原文逐字节闭合）。

### 0.2.1 同批线上实测（本仓库工作会话的真实工具调用）

| 路径 | 实测输入 | 结果 |
|---|---|---|
| `shell` | `git log --oneline -n 500` → 508 行 / 45208 字节 | 渲染为首 138 行 + `omitted 370 lines (33705 bytes) from the end` + 下一步指引；无中间挖空 |
| `shell`（按指引收窄重发） | `Select-Object -Skip 138 -First 80` | 完整拿到被省略的尾部，无二次折叠 —— 「下一步」可执行 |
| `view`（预算自持） | 461 KB 文件，`limit=2000, offset=0` | 自有 32 KiB 窗口 + `[efficiency] File continues past this window (is_truncated=true, lines_read=634) … continue with offset=634 limit<=400`；**无** L4 折叠标记（`skip_render_truncation` 生效） |
| `view`（定点续读） | `offset=632 limit=120` | 返回 633–752 行，无折叠、无指针行 —— 行偏移续读可用 |
| `artifact_read`（分页） | `offset=33000 limit=6000` | `window=[33000,39000) | next_offset=39000 | eof=false` —— 原始输出可按字节区间分页，不再是「无法分页」 |
| `shell`（单行超长） | `python -c "print('abcdefghij'*4000)"` → 40174 字节单行 | 暴露 §0.2 缺陷（`showing the first 9 of 9 lines; omitted 0 lines (28505 bytes)`），已修 |

验证（2026-09-20）：`go vet ./internal/output/ ./internal/toolkit/tools/` 通过；`go build -p 1 ./internal/... ./cmd/...` 通过（`tmp/prune_probe` 为本地未跟踪临时包，编译期 OOM 属环境内存不足）；`go test -count=1 -p 2 ./internal/output/... ./internal/toolkit/tools/ ./internal/agent/ ./internal/toolresult/` 全绿；新增 `TestFormatTruncatedToolTextForModel_PartialLineNoticeReportsByteCut` 以「8 行头 + 40000 字节单行」复现该形状，交叉核对完整行数 / 部分行字节 / 省略字节与渲染字节闭合。

---

## 0.3 shell 输出窗口自持：`bash` / `aicli_exec`（2026-09-20 增量）

§0 表格里 shell 是最后一个「只声明预算、由 L4 折叠」的工具。本轮把窗口所有权彻底下放：`bash`（单命令 + `commands` 批次）与 `aicli_exec` 的**每个返回路径**都经过 `ownShellOutputWindow`（`internal/toolkit/tools/tool_output_budget.go`，由 `Execute` 的 defer 统一收口），顺序固定为「先归档、后折叠、再 stamp」：

1. **先归档**：折叠前把**完整** capture 写入 session artifact store（`artifact_id`，归档层指标 `tool_window`）。因此 `artifact_read` 翻页拿到的是完整流，而不是模型已经看过的头部；
2. **后折叠**：正文按 `shellOutputBudgetBytes = 32 KiB` head-only 折叠（rune 边界安全，通知计入同一预算），正文标记 `[output window: showing the first N of M bytes of the captured output; omitted X bytes (L lines) …]`，metadata 记 `truncated` / `output_window_bytes` / `output_window_total_bytes`；
3. **再 stamp**：`artifact_source_id = artifact_id`（该结果就是这条记录的窗口，gateway 的 no-cascade 守卫因此不再重复归档折叠体）+ `skip_render_truncation=true` + `model_visible_budget_bytes=32 KiB`，L4 对 shell 输出不再折叠。

磁盘 artifact（`raw_output_artifact_path`，> 12 KiB 落盘）保持不变，作为第二条恢复路径。capture 上限（默认 256 KiB）语义不变：capture 仍是内存上界，模型可见窗口只由工具自己的 32 KiB 决定；capture 本身超限时仍以 capture 通知说明丢失区间。

验证（2026-09-20）：`go build ./...` 通过；`go test -count=1 ./internal/toolkit/... ./internal/output/... ./internal/toolresult/... ./internal/executor/... ./internal/agent/...` 全绿。新增 `TestShellToolsOwnTheirOutputWindow`（折叠算术 + `artifact_read(offset=32768)` 逐字节取回被省略尾部 + 渲染无 L4 折叠标记 + `l4_render` 计数零增量）、`TestShellToolsStampOwnershipOnEveryExecutePath`（真实 `echo` 单命令 / 批次 / `aicli_exec` 失败路径均带 stamp），并把 `bash`（真实 39 KiB 输出）与 `aicli_exec` 加入 `TestBudgetOwningToolsDoNotTriggerRenderLayerTruncation` 用例表。

---

## 1. 背景：artifact_read 为何被频繁调用

### 1.1 观测到的现象

在一个真实的开发会话（本仓库工作会话）中观测到以下模式：

| 序号 | 调用模式 | 结果特征 |
|---|---|---|
| 1 | `grep` → 指针行 → `artifact_read` → 指针行 → `artifact_read`… | 小结果（24~1085 字节）也被归档，解引用输出尾部再次出现指针行，形成递归级联 |
| 2 | `view` 单文件 743 行（29162 字节）→ 被截断 → `artifact_read` → 输出又贴近预算上界 | view 的行数窗口与模型字节预算错位，折叠发生在错误的层 |
| 3 | `shell` 输出 TUI 转义序列（16~57 KB）→ 截断 → `artifact_read` 分页 2~3 次才能读完全部 | 单次解引用无法覆盖完整原始输出 |
| 4 | 每次 `artifact_read` 的结果又被 gateway 归档成新 artifact | 存储膨胀，指针指向指针 |

### 1.2 本轮已修复的部分（指针级联）

修复前 `renderToolTextForModelHistory` 的行为：

```go
// 修复前
withNotice := appendToolArtifactNotice(full, notice)
if len(withNotice) <= modelToolTextByteBudget {
    return withNotice   // ← 小结果也无条件带指针行
}
```

只要 metadata 里有 `artifact_id`（gateway 对所有非空输出无条件注入），就追加指针行。后果是：
`artifact_read` 解引用一个 artifact → 输出本身又被归档为新 artifact 并追加新指针 →
模型看到的指针指向的内容又以指针结尾 → 递归级联，每次解引用膨胀 ~160 字节。

修复后语义（`tool_result_content.go` `renderToolTextForModelHistory`）：

- 完整文本 ≤ `modelToolTextByteBudget`（默认 12 KiB）且未截断时：
  - **成功的普通结果**：不再追加 record-id 指针行（消除级联的燃料）
  - **失败结果**：保留指针（兼作恢复提示，`tool_error_output_test.go` 契约）
  - **artifact_read 窗口**（metadata 带 `artifact_source_id`）：防御性保留指针，且 gateway 层跳过重复归档（`gateway.go` `isArtifactReadWindow`）
  - **path 型通知**（`raw_output_artifact_path`）：无条件保留（磁盘 artifact 可发现性）
- 超预算截断时：指针行照旧保留（这是指针存在的本意）

修复后该目标成立：**解引用一个指针永远不会产生新的指针级联**（`artifact_read.go:19-22` 注释的设计意图）。

但级联只是表层症状。审计发现 artifact_read 高频调用的根源是**三个结构性问题**，详见第 4 节。

---

## 2. Artifact 产生链路全景

### 2.1 架构总览

```
┌──────────────────────────────────────────────────────────────────┐
│                        工具执行层                                  │
│  view / grep / glob / ls / shell / write / edit / web_search …    │
└──────────────┬───────────────────────────────┬───────────────────┘
               │ 输出文本                        │ shell 类额外产物
               ▼                               ▼
┌──────────────────────────┐   ┌─────────────────────────────────┐
│ 链路 A：Gateway 会话归档   │   │ 链路 B：磁盘 shell artifact       │
│ gateway.go:130-147        │   │ bash.go:1409 ensureLargeHistory… │
│ store.Put(所有非空文本)     │   │ 阈值: ModelToolTextByteBudget()  │
│ → metadata["artifact_id"] │   │ = 12 KiB (bash.go:71-77)         │
│ → ArtifactIDs[]           │   │ → raw_output_artifact_path       │
└──────────────┬───────────┘   └────────────────┬────────────────┘
               │                               │
               ▼                               ▼
┌──────────────────────────────────────────────────────────────────┐
│ 回显层 renderToolTextForModelHistory (tool_result_content.go)      │
│  ≤ 12 KiB 未截断: 成功→无指针 / 失败→保留 / path→保留              │
│  > 12 KiB: head/tail + "Full raw output artifact_id: art_…"       │
└──────────────┬───────────────────────────────────────────────────┘
               │ 模型看到指针行
               ▼
┌──────────────────────────────────────────────────────────────────┐
│ artifact_read（toolkit/tools/artifact_read.go）                   │
│ 会话隔离校验 → 字节窗口 [offset, offset+limit)                    │
│ 默认窗口 8 KiB，上界 budget−512（artifactReadMaxLimitBytes）       │
│ eof/next_offset 分页协议                                          │
└──────────────────────────────────────────────────────────────────┘
```

### 2.2 链路 A：Gateway 会话级归档（所有工具）

**位置**：`backend/internal/output/gateway.go:130-147`

**逻辑**：

```go
if g.store != nil && strings.TrimSpace(text) != "" && !isArtifactReadWindow(result.Metadata) {
    artifactID, err := g.store.Put(ctx, artifact.Record{...})
    envelope.ArtifactIDs = append(envelope.ArtifactIDs, artifactID)
    envelope.Metadata["artifact_id"] = artifactID
}
```

**触发条件**：`store` 存在 + 输出非空 + 非 artifact_read 窗口（最后一个条件是本轮修复新增）。

**覆盖工具**：**全部工具**。`view`、`grep`、`glob`、`ls`、`shell`、`write`、`edit`、
`multiedit`、`web_search`、`web_fetch`、`todo`、`spawn_agent`、MCP 工具……所有经过
`Gateway.Process`（`agent/loop.go` 共 5 处调用点：正常执行 L2471/L2542/L2702/L2861 +
denied/soft-empty 终态 L2929/L2950）的非空文本输出。

**关键语义**：归档无条件，但**指针行只在模型可见文本被截断时**才出现在模型视野。
因此"产生 artifact" ≠ "触发 artifact_read"。小结果归档只造成存储/索引噪声，不直接
引发解引用。

### 2.3 链路 B：Shell 类磁盘 artifact（阈值触发）

**位置与逻辑**：

| 工具 | 位置 | 阈值 | 说明 |
|---|---|---|---|
| `bash` | `bash.go:862-911` + `ensureLargeHistoryOutputArtifact` (L1409-1421) | `capture.TotalBytes > ModelToolTextByteBudget()` | capture 未截断（Truncated=false）但总量超阈值时落盘 |
| `execute_shell_command` | 同 bash 底层 | 同上 | 复用同一执行器 |
| `aicli_exec` | `aicli_exec.go:245` | 同上 | 子 CLI 输出同样落盘 |
| `cmd/aicli/functions/shell.go` | CLI 函数层 | `modelHistoryArtifactThresholdBytes()` → `output.ModelToolTextByteBudget()`（L27-29） | P3 已完成：与 toolkit 侧 `bash.go:76-78` 同源，不再硬编码 |
| bash 批量模式 | `bash.go:710` | `len(batchOutput) > threshold` | 批量输出整体超阈值时归档合并输出 |

**落盘位置**：`toolctx.ShellOutputArtifactDir(ctx)` 下的 scope 目录（如
`shell-output/toolkit/git_<hash>.txt`），记录写入 `metadata["raw_output_artifact_path"]`。

**渲染语义**：path 型通知 `Full raw output artifact: <path> kind=raw_output`，
无条件追加（不受本轮"未截断则省略"规则影响——磁盘文件不在 artifact store 里，
除了这个 path 没有别的发现渠道）。

**注意**：`CaptureCombinedOutputGuarded` 自身有 256 KB capture limit（可被
`output_bytes_cap`/`disable_output_cap` 参数覆盖）；被 capture 截断的输出
（`Truncated=true`）**不会**再走 `ensureLargeHistoryOutputArtifact`（L1410 提前返回），
因为 capture 已经尽力保留了全部输出。

### 2.4 其他 artifact 相关工具

| 工具 | 与 artifact 的关系 |
|---|---|
| `artifact_read` | 消费方。按字节窗口读取 store 中的记录，带会话隔离、分页协议、防级联窗口上界 |
| `openai_image_generate` | 图片写入会话 artifact 目录（`openai_image_generate.go:26`），走磁盘目录而非 store 记录 |
| `task_output` | 后台任务输出，独立于 artifact store（指针行的 "never pass this id to task_output" 警示即源于模型混淆这两者） |
| 前端 `artifact-output-link` | 消费指针行渲染"查看完整原始输出"按钮（`frontend/src/components/workspace/message-markdown/`） |

---

## 3. 折叠发生的四个层次（现状盘点)

工具输出从产生到进入模型历史，要经过四道尺寸限制，彼此独立、互不感知：

| 层 | 限制 | 单位 | 默认值 | 截断后模型恢复路径 |
|---|---|---|---|---|
| L1 工具内部 | view 的 `viewDefaultLimit` | **行** | 2000 行 | `suggested_next_offset` + `offset/limit` 重调 view |
| L2 shell capture | `CaptureCombinedOutputGuarded.MaxBytes` | 字节 | 256 KB | 无恢复（head/middle/tail 保真压缩） |
| L3 shell 落盘 | `ensureLargeHistoryOutputArtifact` | 字节 | 12 KiB | `raw_output_artifact_path` → 模型需自行读文件（或 artifact_read 不了——这是 path 不是 store id） |
| L4 回显层 | `modelToolTextByteBudget` | 字节 | 12 KiB | `artifact_read(artifact_id=…, offset, limit)` 分页 |

**L4 兜底口径（2026-09-20 核查，`tool_result_content.go`）**：

- 默认值 `modelToolTextByteBudget = 12 * 1024`（L33，= 12288 B）；唯一改动入口
  `output.SetModelToolTextByteBudget`（L38，非正值忽略）。仓库内**无生产调用点**
  （仅 `tool_budget_l4_render_test.go` 调用），故运行时实际生效值就是 12288 B。
- 仅作用于**未声明自持窗口**的工具：声明了 `model_visible_budget_bytes` 的工具按声明值，
  被夹在 `[max(12288, 4096), 65536]`（L55/L58/L79-88）；带 `skip_render_truncation`
  的工具（`bash`/`aicli_exec`/`view`/`grep`/`glob`/`ls`/`fetch`/`artifact_read`）直接绕过 L4 折叠。
- 12288 B 是**渲染结果硬上限**：`header + head + notice ≤ budget`（L922）；存在 artifact
  指针时先扣指针（L675）。按 `truncationMarker`/`truncationMarkerPartial` 模板复算：
  `header ≈ 47-51 B`、通知预留 `≈ 375-382 B` → 模型可见正文 `≈ 11855-11866 B`（≈11.6 KiB）。

**核心错位**：L1 用行、L2-L4 用字节，且 L1 的 2000 行远超 L4 的 12 KiB
（12 KiB ≈ 150~300 行代码 / 300~600 行日志）。L1 层返回
`is_truncated=false` 的"完整窗口"，在 L4 层必然被截断。

### 3.1 各工具对 L4 预算的实际触碰概率

| 工具 | 典型输出尺寸 | 触碰 12 KiB 预算概率 | 备注 |
|---|---|---|---|
| `view`（无 limit） | 高达数百 KB | **极高** | 2000 行默认窗口是最大来源 |
| `view`（files 批量） | 受 `viewBatchDefaultLimit=200` 行/项 | 中 | 多文件合并后仍可能超 |
| `grep` | 一般小 | 低 | 大 codebase + 宽 pattern 时会超 |
| `shell` | 不定 | 中高 | TUI 程序输出转义序列时异常膨胀 |
| `go test` / 构建输出 | 中大 | 高 | 经 shell 链路 |
| `write`/`edit`/`multiedit` | 小（diff/确认信息） | 极低 | |
| `todo`/`get_goal` | 小 | 极低 | |
| MCP 外部工具 | 不定 | 中 | full-content 优先策略，见 `tool_output_contract.md` |

---

## 4. 结构性根因分析

### 根因 1（主因）：view 的行数窗口与模型字节预算脱节

`view.go:34-36`：

```go
// viewDefaultLimit is the default window size when callers omit limit.
// Large files should still be segmented with explicit offset/limit.
const viewDefaultLimit = 2000
```

问题链条：

1. 模型 `view` 一个 743 行 Go 文件，不带 limit
2. view 返回 29162 字节，`is_truncated=false`（view 认为读完了）
3. L4 回显层把它截成 head/tail + 指针行，**中间内容丢失**
4. 模型此时有两条恢复路径：
   - view 的协议：`offset=200 limit=400` 续读（但 head/tail 视图里没有"当前窗口"概念，
     模型不知道该从哪行继续，只能猜测或重读全文件）
   - artifact_read 的协议：`artifact_read(artifact_id=…, offset, limit)` 按字节分页
5. **模型几乎总是选 artifact_read**：因为 head/tail 视图已经把中间内容剪掉了，
   字节分页能精确到达丢失区间，而行数续读需要多次盲试

后果：view 精心设计的 `is_truncated` / `suggested_next_offset` / `efficiency_advisory`
（view.go:280-291）协议被架空；`view`→截断→`artifact_read` 成为高频路径。

更深一层：view 的注释（L36）说 "Large files should still be segmented with explicit
offset/limit"，但**没有机制强制或引导模型这样做**——efficiency_advisory 只在
`request.Limit >= viewEfficiencyAdvisoryThreshold`(2000) 时软提示，而模型的默认调用
恰恰不带 limit。

### 根因 2：Gateway 归档粒度与解引用需求不匹配

Gateway 把 24 字节的"未找到匹配的内容"与 57 KB 的测试输出同等对待：

- 小结果归档：永不解引用，纯存储与 Search 索引噪声
- `artifact_read` 结果归档：指针指向指针（已修复，跳过）
- 中等结果（1~12 KiB）：归档了但模型看得到全文，解引用概率低

归档本身有正当用途（前端回放、审计、Search），不应取消；问题是**归档策略与
指针策略没有对齐**——没有"多大以上的 artifact 才值得可发现"的分级。

### 根因 3：artifact_read 默认窗口偏保守，分页次数偏多

`artifact_read.go:22`：

```go
artifactReadDefaultLimitBytes = 8 * 1024
```

而单次窗口的硬上界是 `ModelToolTextByteBudget() - 512` ≈ 11.5 KiB
（`artifactReadMaxLimitBytes`，L206-213）。对于 12 KiB 上下的 artifact（shell 落盘
阈值恰好也是 12 KiB），模型用默认 8 KiB 窗口需要 2 次 `artifact_read` 才能读完全部；
第二次调用又经历一次完整的 gateway 流程（归档跳过已修复，但仍有工具往返、
contract JSON 包裹等开销）。

默认值与上界之间 3.5 KiB 的差距没有任何注释说明取舍原因；窗口协议本身
（`eof`/`next_offset`）是健壮的，问题纯粹在默认参数。

### 根因 4（放大器）：TUI 测试输出等异常输入膨胀

`cmd/aicli/commands` 的测试在非交互 shell 下输出大量 ANSI 转义序列
（观测到单条 `go test` 输出 16~57 KB），这类输出：

- 经链路 B 落盘 + 链路 A 归档，双份存储
- 解引用时按字节分页，转义序列没有语义分页点，模型需要读 2~3 页才能确认
  "只是 TUI 噪声"

这类输入是训练/评测环境的固有噪声，无法在协议层消除，但可以通过 L1 层的
字节感知（方案 P0-2）把折叠提前到 view/grep 内部，避免它们进入 L4。

---

## 5. 优化方案

### P0-1：view 默认窗口字节感知（收益最大）

**改动点**：`backend/internal/toolkit/tools/view.go`

方案 A（推荐，最小改动）：

- `viewDefaultLimit` 从 2000 行降到 400 行
- `readFile` 读满 limit 行后检查累计字节数，若已超
  `output.ModelToolTextByteBudget()` 的 70%（约 8.4 KiB，为 head/tail + 元数据留余量）
  则提前停止并把 `is_truncated=true`、`suggested_next_offset` 设为实际停止行
- 元数据新增 `byte_budget_applied: true`，与行数截断区分

方案 B（更彻底）：`readFile` 直接以字节窗口为主协议，行数仅作为可选上限。
改动面大（`viewReadResult` 结构、批量模式、目录预览都要跟着改），收益与 A 相同，
不建议第一轮做。

**效果**：view 输出几乎不再触碰 L4 截断；模型按 view 自己的
`offset/limit` 协议续读，artifact_read 调用量预期下降 60%+（按本会话观测
view→artifact_read 占解引用来源的比例估算）。

**风险**：已有测试依赖 `viewDefaultLimit=2000`（`view_test.go` 需要同步调整）；
依赖"一次 view 读整个文件"的工作流会多一次往返——但换来的是每次往返都在
预算内、无需解引用，净 token 成本下降。

### P0-2：grep/工具层字节预算硬上限

**改动点**：`backend/internal/toolkit/tools/grep.go`（及其他高方差输出工具）

- 匹配行输出累计超过 `ModelToolTextByteBudget()` 的 80% 时停止收集，
  标记 `results_truncated=true` + `next_step: "narrow the pattern or add paths/glob"`
- 与 L4 的 head/tail 截断不同，L1 截断保留**完整的前 N 条匹配**（不剪中间），
  语义上对"找东西"更有用

**效果**：消除 grep→artifact_read 路径（本会话观测中 grep 是第二大解引用来源）。

### P1-1：Gateway 归档分级

**改动点**：`backend/internal/output/gateway.go`

- 文本 < 1 KiB 且无 reducer 摘要需求：跳过 `store.Put`，metadata 记
  `artifact_skipped: "below_threshold"`
- **前提确认**：`store.Search`（gateway_test.go:79 用于全文检索）是否依赖小结果——
  审计结论是 Search 主要服务大输出的关键词回捞，小结果通常模型直接可见，
  跳过归档不损失可检索性；但需在实现时验证 `internal/artifact` 的 Search
  实现与前端回放对缺失记录的容忍度
- 阈值做成与 `modelToolTextByteBudget` 联动的常量（如 budget/12）

**效果**：存储与索引噪声显著下降；对解引用频率无直接影响（小结果本来
不会被解引用）。

### P1-2：artifact_read 默认窗口对齐上界

**改动点**：`backend/internal/toolkit/tools/artifact_read.go:22`

- `artifactReadDefaultLimitBytes` 从 8 KiB 提到 `artifactReadMaxLimitBytes()`
  （即 budget−512 ≈ 11.5 KiB）
- 保留显式 `limit` 参数覆盖能力；`artifactReadMinLimitBytes=1024` 下界不变
- 更新 `TestArtifactReadWindowStaysUnderModelBudget` 边界断言

**效果**：12 KiB 上下的 artifact 一次解引用即可读完，分页调用次数约减半。

**风险**：极低——窗口上界本来就有防级联保证（窗口 + header ≤ budget），
默认值只是从"保守"改为"贴上界"。

### P2：截断视图内的显式续读指引

**改动点**：`backend/internal/output/tool_result_content.go` `formatTruncatedToolTextForModel`

- head/tail 之间的 `[... truncated N bytes ...]` 区域改为携带解引用指引：
  `[... truncated N bytes; read via artifact_read(artifact_id=<id>, offset=<bytes>, limit=<bytes>) ...]`
- 消除"模型看到 head/tail 后走弯路（如重调同参数工具）"的路径

**依赖**：`formatTruncatedToolTextForModel` 目前不知道 artifact id（它只拿文本和
预算），需要把 id 从 `renderToolTextForModelHistory` 传下来——小规模签名调整。

### P3：shell 落盘阈值常量去重

**改动点**：`backend/cmd/aicli/functions/shell.go:24`

- 硬编码 `modelHistoryArtifactThresholdBytes = 12 * 1024` 改为引用
  `output.ModelToolTextByteBudget()`（与 toolkit 侧 `bash.go:75-77` 对齐）
- 消除"调整 budget 后 CLI 函数层不同步"的隐患

**状态（2026-09-20 核查）：已完成** —— `shell.go:27-29` 现为
`return output.ModelToolTextByteBudget()`，L400 阈值判定与 toolkit 侧同源；
仓库内已无 `12 * 1024` 的重复定义。

### 方案优先级与依赖关系

```
P0-1 (view) ─┐
P0-2 (grep) ─┼─ 独立，可并行，直接削减 artifact_read 调用量
P1-2 (窗口) ─┘
P1-1 (归档分级) ── 独立，存储侧收益
P2 (续读指引) ─── 依赖小重构（id 传递），收益中等
P3 (常量去重) ─── 独立，一行改动
```

---

## 6. 验证计划

### 6.1 单元测试

| 目标 | 测试 |
|---|---|
| P0-1 | `view_test.go`：新增"800 行文件默认窗口在预算内且 is_truncated=true + suggested_next_offset 正确"；调整依赖 2000 行默认值的既有断言 |
| P0-2 | `grep` 新增匹配数超预算截断测试（截断保留前缀完整匹配 + results_truncated 标记） |
| P1-1 | `gateway_test.go`：小结果不产生 ArtifactIDs；`artifact_read_no_cascade_test.go` 回归保持 |
| P1-2 | `artifact_read_test.go`：默认窗口 = maxLimit；`TestArtifactReadWindowStaysUnderModelBudget` 保持通过 |
| P2 | `tool_result_content_test.go`：截断视图中包含带 id 的续读指引 |
| P2 | `tool_result_content_test.go`：`TestFormatTruncatedToolTextForModel_NeverExceedsBudget` —— 预算为硬上限，覆盖 1B~64KiB 与含/不含指针两种形态 |
| P3 | `cmd/aicli/functions` 既有测试回归 |

> P2 附带的预算修正：`formatTruncatedToolTextForModel` 原先固定预留 160 B 给折叠标记，
> 而 P2 给标记追加的续读指引（约 23 B）未计入该预留，导致 12 KiB 预算下实际渲染
> 12311 B（超 23 B）。现改为由 `truncationMarkerReserve` 按标记真实长度（含指针、
> 以总字节数为省略量上界）精确预留，并把 `modelToolTextMinSegmentBytes` 的
> 每段最小值降级为"预算允许时才生效"的可用性下限，使预算成为真正的硬上限。

### 6.2 集成验证（观测指标）

在真实工作会话中统计一轮开发任务的：

- `artifact_read` 调用次数 / 总工具调用次数（基线：本会话观测约 15%+）
- 因截断产生的解引用链平均长度（基线：1~3 跳）
- 单 turn 工具输出总字节数（含解引用重复内容）

目标：artifact_read 占比降到 5% 以下；解引用链长度 ≤ 1 跳
（P1-2 后 12 KiB 级 artifact 一次读完）。

### 6.3 回归守卫

- 本轮已加的 `TestGateway_SkipsReArchivingArtifactReadWindows` /
  `TestRenderToolResultContentForModel_NoIDNoticeOnUntruncatedSuccess` 必须持续通过
- `tool_error_output_test.go` 的失败结果指针契约必须保持
- 前端 `artifact-output-patterns.ts` 对指针行的解析不受 P2 措辞调整影响
  （P2 若实施，需同步更新前端匹配 pattern 与 `splitTrailingArtifactNotice`）

---

## 7. 明确不做的事

1. **不取消 Gateway 全量归档**：前端回放、审计、Search 都依赖完整记录；
   只做小结果分级跳过（P1-1）。
2. **不把 L1 全部改成字节协议**（§5 方案 B）：改动面与收益不成比例，
   行协议对"读代码文件"场景语义更自然，字节感知只作为停止条件。
3. **不提高 L4 budget 本身**：12 KiB 是 token 成本与信息密度的平衡点，
   有独立配置入口（`SetModelToolTextByteBudget`），按会话调整即可。
4. **不在协议层处理 TUI 转义噪声**（根因 4）：属于输入侧问题，
   L1 字节感知（P0）能顺带缓解。

---

## 8. 遗留问题（超出本方案范围，需单独跟踪）

1. ~~**permission mode 恢复优先级 bug**（本会话最初发现）~~ → **已解决（2026-09-19 全量验证）**：
   原失败用例 `TestRestoreChatStateFromRuntimeSessionRestoresRouteTransparency` 现通过；
   `go test ./cmd/aicli/commands/ -count=1` 全量绿（95.3s）。原症状：`ctx.PermissionMode`
   零值 "default" 越过 session metadata 持久化的 "plan"（与 artifact 链路无关）。
2. **`cmd/aicli/commands` 测试在非交互 shell 的转义序列噪声**：建议给 TUI
   交互测试加非 TTY 环境守卫或输出重定向。
3. **`internal/chat` `TestSessionActorApproveToolResumesWithoutInMemoryWaiter`**
   全量跑偶发 5s 超时抖动，单跑稳定，疑似测试间资源竞争。

---

## 9. 实施顺序建议

1. **P1-2 + P3**（半小时级）：参数与常量对齐，零风险，立即减少分页次数
2. **P0-1**（view 字节感知）：最大收益项，测试调整面集中在 view_test.go
3. **P0-2**（grep 截断）：与 P0-1 同型，复制模式即可
4. **P1-1**（归档分级）：先验证 Search/回放对缺失小记录的容忍度再合入
5. **P2**（续读指引）：最后做，需要前端联动验证

---

## 10. Harness 层对比分析（codex-rs 与 deepseek-harness）

### 10.1 Codex（codex-rs）的做法

**关键事实：Codex 核心工具集里没有独立的 read_file 工具。**`core/src/tools/` 目录下只有
`apply_patch`、`shell`、`unified_exec`、`view_image`、`mcp_resource` 等——文件读取
完全交给 shell（`cat`/`sed`/`rg`），由 **shell 输出截断层统一兜底**：

- `exec.rs:76`：`EXEC_OUTPUT_MAX_BYTES = DEFAULT_OUTPUT_BYTES_CAP`（pty 层常量），
  stdout/stderr 各自 `truncate(max_bytes)`（L739-745），超限即截，**没有 artifact 解引用机制**
- `utils/output-truncation/src/lib.rs`：统一截断策略
  `TruncationPolicy::Bytes | Tokens`，实现为 **truncate_middle**（保头保尾剪中间），
  截断后头部追加元信息：`Warning: truncated output (original token count: N)\nTotal output lines: M`
- `unified_exec/mod.rs`：`DEFAULT_n_TOKENS = 10_000`（token 预算而非字节）
- **截断是终点，不是指针**：模型看到截断警告后，要么换更精确的 shell 命令
  （`sed -n '100,200p' file`），要么接受信息损失。没有"读取完整原始输出"的第二通道

这个设计的前提是 prompt 工程：`prompt_with_apply_patch_instructions.md` 明确指导模型
"Do not use python scripts to attempt to output larger chunks of a file"——即通过
行为约定防止模型绕过截断层，而不是提供绕过通道。

### 10.2 deepseek-harness 的做法

`packages/fs/tool-fs/src/read.ts` + `read-render.ts` 是**双层预算的行协议**：

```
READ_LIMIT        = 2000 行     （默认窗口，同时是 limit 参数的硬上界）
READ_MAX_BYTES    = 50 KiB      （字节硬上限，命中即停止收集）
READ_MAX_LINE_LENGTH = 2000 字符（单行截断，防止单行爆内存）
STREAM_MIN_SIZE   = 10 MiB      （超过则流式读取，不整文件入内存）
```

关键机制（`read-render.ts:77-89` `consumeLine`）：

- **行预算与字节预算同时生效**：收集每一行前先查 `outputBytes + bytes > maxBytes`，
  命中即标记 `truncatedByBytes` 停止——**字节预算在工具内部解决，不留给回显层**
- **仍然扫描到文件末尾**拿精确 `totalLines`（`buildWindow` 不提前 break），
  所以续读协议有精确的行号锚点
- **续读脚注内嵌在输出里**（`formatReadOutput` L152-160）：
  - 字节截断：`(Output capped. Showing lines N-M. Use offset=M+1 to continue.)`
  - 行截断：`(Showing lines N-M of T. Use offset=M+1 to continue.)`
  - 读完：`(End of file - total T lines)`
  三种状态显式区分，模型不需要猜
- **offset 越界是硬错误**（FS_NOT_FOUND），防止模型盲目递增 offset 空转

### 10.3 对比矩阵

| 维度 | ai-agent-runtime（现状） | codex-rs | deepseek-harness |
|---|---|---|---|
| 文件读取工具 | view（行协议 2000 行默认） | 无 read 工具，shell 兜底 | read（行+字节双预算） |
| 字节预算在工具内生效 | ❌ 仅在回显层（L4） | ✅ shell 截断即终点 | ✅ consumeLine 内嵌 |
| 截断后的恢复通道 | artifact_read 分页解引用 | 无（换命令重试） | 同工具 offset 续读（行号精确） |
| 截断提示 | 尾部指针行（artifact id） | 头部 warning（token 数/行数） | 尾部脚注（下一 offset 明示） |
| 单行超长防护 | ❌ 无 | ✅（middle truncate） | ✅ 2000 字符截断 |
| 超大文件流式 | ❌ 整文件读入 | 不适用 | ✅ >10MiB 流式 |
| 二次往返成本 | 高（解引用 1-3 跳） | 高（换命令盲试） | **低（一次续读定位精确）** |

### 10.4 harness 层优化建议（补充第 5 节方案）

基于对比，建议在原 P0-P3 之外增补：

**H-1：view 采用"行协议 + 字节预算内嵌"双保险（吸收 deepseek-harness）**

`readFile` 循环内加字节记账（不只是 P0-1 的 70% 停止条件）：
- 单行超过 2000 字符 → 行内截断加 `... (line truncated)` 后缀
- 累计字节超过 `ModelToolTextByteBudget() * 0.8` → 停止收集，但**继续扫描拿 totalLines**
- `is_truncated` 细分为 `truncated_by_lines` / `truncated_by_bytes`
- 脚注格式对齐 harness：`(Showing lines N-M. Use offset=M+1 to continue.)`
  与现有 `suggested_next_offset` 并存（模型读脚注，元数据供 UI）

这比原 P0-1 更进一步：原方案只降默认 limit 到 400 行，H-1 保证**任何**窗口
（包括显式 limit=2000 的调用）都在字节预算内完整返回可见部分，彻底消除
"view 层说没截断、回显层截了"的层间竞争。

**H-2：截断警告头部化（吸收 codex）**

当前指针行在输出尾部，head/tail 中间的模型注意力容易被截断内容占据。
参考 codex 把元信息放头部：

```
[view] window truncated: showing lines 21-180 of 743 (8.4KiB of 29KiB).
Continue with offset=181 or read full output via artifact_read(artifact_id=…).
```

头部一行同时给出**行协议续读**（首选）与**字节协议解引用**（兜底）两条路径，
并明确优先级——当前尾部指针行只给了 artifact_read 一条路，是模型选择
artifact_read 而非 view 续读的直接诱导因素。

**H-3：artifact_read 输出加"下一跳"脚注（吸收 harness 脚注模式）**

artifact_read 的窗口 header 已有 `eof=false next_offset=N`，但它是结构化
元数据，模型看到的是 header 行。建议对齐 harness 脚注风格，在窗口尾部追加：

```
(Showing bytes A-B of T. Use artifact_read(artifact_id=…, offset=B) to continue.)
```

消除模型从 `next_offset` 元数据换算调用参数的认知负担。

### 10.5 关键判断：现有方案的二次执行成本风险是否成立

**问题**：优化后"本来一次能处理的工作需要二次执行，成本反而增加"是否存在？

分场景评估：

| 场景 | 现状 | 方案后 | 成本变化 |
|---|---|---|---|
| view 读 500 行文件（一次够用） | view 返回 14KiB → L4 截断 → artifact_read 1-2 次 | view 窗口预算内一次返回 | **净节省**（少 1-2 次调用 + 解引用内容重复计费） |
| view 读 743 行文件（真需要全部） | view 全量 → 截断 → 解引用 2-3 跳读 29KiB | H-1: view 一次给 8.4KiB 可见部分；若模型真需要剩余 → view offset 续读 1 次（11KiB）或解引用 | **持平或节省**：解引用的 8KiB 窗口 × 3 跳 ≈ 24KiB token，vs 续读 11KiB × 1 次 |
| grep 大结果集 | 截断 → 解引用（中间匹配已丢，解引用也只能看全部） | P0-2: 前 N 条完整匹配 + 明示截断 | **节省 + 质量更高**（head-only 保留完整匹配 vs head/tail 剪中间） |
| 模型本来就要读整个大文件 | 无差别 | 无论如何都需要多跳 | 无变化（信息论下限） |

**结论：方案不会引入"一次变两次"的回退**，原因有三：

1. **字节预算内嵌（H-1）后，"view 成功且模型看全"的比例上升**，需要恢复
   路径的调用比例本身下降——恢复路径变便宜了，但更需要恢复的场景也变少了
2. **续读协议有精确锚点**（suggested_next_offset / totalLines / 脚注明示），
   现状中模型从 head/tail 猜测续读参数的盲试成本被消除
3. **解引用内容会随历史重复计费**：artifact_read 的输出进入对话历史后，
   每一轮 API 调用都重复计费这部分 token。现状的 2-3 跳解引用产生
   24-32KiB 的历史成本，方案后通常 0-1 跳，多轮会话的累积节省显著

**真正需要警惕的成本风险（补充为方案约束）**：

- ⚠️ P0-1 若只降默认 limit 而不加字节记账，显式 `limit=2000` 的调用仍会
  触发 L4 截断+解引用——所以 H-1 的字节内嵌是 P0-1 的必要补充，不是可选
- ⚠️ 续读脚注会小幅增加每次截断输出的固定开销（~100 字节），可接受
- ⚠️ H-2 头部警告行占用模型注意力头部位置，措辞需紧凑（单行 ≤ 200 字节）

### 10.6 单行截断（H-1 细节）的内容丢失风险与缓解

#### 10.6.1 关键差异：L1 截断与 L4 截断的可恢复性不同

| | L4 回显层截断（现状） | L1 工具内行截断（H-1） |
|---|---|---|
| 截断发生位置 | Gateway 渲染模型可见文本时 | view `readFile` 循环内 |
| Gateway 归档的内容 | **截断前的完整原文** | **截断后的文本** |
| artifact_read 能否恢复完整行 | ✅ 能（展示层丢失） | ❌ 不能（存储层丢失） |
| 剩余恢复手段 | 解引用 | 仅 shell 按字节偏移重读（模型难自主想到） |

即：H-1 的行内截断引入了一个**现状不存在的丢失通道**。当前系统里 view 返回的超长行
虽然会被 L4 剪掉，但完整原文始终在 artifact store 里；H-1 若直接截断，截掉的部分
彻底不可恢复。

#### 10.6.2 风险场景分级

| 场景 | 超长行常见性 | 丢失影响 |
|---|---|---|
| 源代码 | 极低（>2000 字符行本身是坏味道） | 几乎无 |
| minified JS/CSS、打包产物 | 每行超 | 无（本来不该读，截断省预算） |
| 单行 JSON / CSV 宽行 | 常见 | **有**——可能恰好截掉目标字段 |
| lock / 生成文件 | 常见 | 中——哈希、版本号可能被截 |
| base64 / 长 URL / JWT | 偶尔 | 有——截掉的正是语义载荷 |

deepseek-harness 采用同样的 2000 字符上限且无恢复通道，说明业界接受该损失；
但本仓库已有 artifact 基础设施，应该做得更好。

#### 10.6.3 缓解设计（三档，可叠加，推荐 1+3 为基线、2 为增强）

**R-1 中段截断（吸收 codex `truncate_middle_chars`）**

行内截断改为保留头尾各 ~1000 字符（或头 1400 + 尾 600）：

```
<前 1000 字符>...[line truncated, 8432 chars total]...<后 600 字符>
```

单行 JSON 的键名集中在行首、闭合结构在行尾，两端语义密度远高于中段；
纯头部截断对"行尾才是关键"的场景（闭合括号、行尾注释）全灭。

**R-2 超长行落盘恢复通道（本仓库特有优势）**

命中行内截断时，将该行**完整内容** `store.Put` 进 artifact store（复用链路 A），
行尾追加指针：

```
12345: <前1000字符>...[truncated, full line: art_xxx]...<后600字符>
```

- 触发频率低（仅超长行），不会重新引入级联（artifact_read 结果已不归档）
- 把"存储层丢失"降级回"展示层丢失"——解引用即可拿回完整行
- 实现注意：一行一个 artifact 会让 store 记录碎片化；更优做法是同一文件的所有
  被截行合并为一个 artifact 记录（每行带行号前缀），单次解引用可恢复全部截断行

**R-3 诚实标记**

- metadata：`line_truncated: true`、`max_line_length_applied: 2000`、被截行的行号列表
- 行尾提示（deepseek-harness 模式）：`... (line truncated to 2000 chars)`
- 让模型明确知道"这行不完整"，可自主决定用 shell 精确重读或解引用

#### 10.6.4 结论

- H-1 的单行截断**会**丢内容，且丢失通道是 L1 特有的（现状没有）；
- 但不截断的代价更差：一条 2MB 的 minified 行会独占整个 50KiB 窗口预算，
  把同行其他 1999 行全部挤出——deepseek-harness 设此上限正是为防这个；
- 推荐组合：**R-1（中段截断）+ R-3（诚实标记）为必选基线**，R-2（落盘恢复）
  作为 P1 增强项——它利用了本仓库已有的 artifact 基础设施，是相对两个参考
  实现的差异化优势；
- H-1 实施时必须同步更新 §6.1 测试矩阵：新增"超长行中段截断 + 指针可达 +
  metadata 标记"的用例。

---

## 11. 观测性方案：artifact 链路指标体系

### 11.1 现有基础设施盘点（本仓库已具备）

| 设施 | 位置 | 可复用点 |
|---|---|---|
| 指标注册表 | `internal/observability/metrics.go` | `IncrementCounter` / `RecordDuration`（直方图自动加 `_seconds` 后缀）/ `GetOrCreateHistogram` |
| 低基数标签纪律 | `tool_efficiency.go` 注释 + 实现 | 标签只用 reason/outcome/error_code/repeat 等泛型值，**禁止工具名进标签**，repeat 分桶 1/2/3+ |
| 结构化快照 | `tool_efficiency_snapshot.go` | `ToolEfficiencySnapshot` 聚合模式 + `deriveInefficiencyFlags` 派生信号——新指标可挂同一快照暴露到 runtime status |
| 事件目录 | `internal/runtimeobserve/known_types.go` | 封闭的 `EventToolStarted/Finished/Failed/Progress` 目录 + projector 归一化；`TestKnownEventTypeCatalogCoversLocalLoopEmits` 守卫目录完整性 |
| 既有采集点 | `toolexec/preflight.go`（preflight）、`agent/loop.go:1023-1108`（doom-loop/disposition-replay） | 埋点位置惯例：在决策点直接调用 `observability.Record*` |

**注意**：`RecordToolOutcome` 目前只有测试调用（生产路径的 outcome 归集尚未接线或经由
其他通道），artifact 指标埋点时应避免重复该问题——在 gateway/render 这两个**必经
汇聚点**埋点，而不是散落到各工具。

### 11.2 指标定义（全部低基数，遵循现有标签纪律）

#### 计数器

| 指标名 | 标签 | 含义 | 埋点位置 |
|---|---|---|---|
| `tool_output_archive_total` | `layer=gateway\|shell_disk`、`disposition=archived\|skipped_below_threshold\|skipped_read_window\|skipped_empty` | 归档决策分布。P1-1 实施后 skipped_below_threshold 直接可观测 | `gateway.go Process`、`ensureLargeHistoryOutputArtifact` |
| `tool_output_truncation_total` | `layer=l1_view\|l1_grep\|l4_render\|shell_capture`、`truncated_by=bytes\|lines` | 各层截断发生次数。**L1/L4 截断比值是层间竞争的直接证据** | view `readFile`、grep 收集循环、`formatTruncatedToolTextForModel`、capture |
| `tool_pointer_notice_total` | `kind=id\|path\|deref_hint` | 指针行/续读脚注追加次数（kind=id 即现状指针行；P2/H-2 落地后 deref_hint 可对比诱导效果） | `renderToolTextForModelHistory` |
| `tool_artifact_deref_total` | `page=first\|followup` | artifact_read 调用分布。**`page=followup`（offset>0）占比 ≈ 平均解引用跳数**，无需维护跨调用状态 | `artifact_read.go Execute` |
| `tool_artifact_deref_miss_total` | `reason=not_found\|cross_session\|bad_offset` | 解引用失败（含模型把 id 传错到 task_output 后的错误重试痕迹） | 同上 |

#### 直方图

| 指标名 | 单位 | 含义 |
|---|---|---|
| `tool_output_original_bytes` | 字节 | 工具原始输出字节数（L1 出口），按 `layer` 标签分列 |
| `tool_output_model_visible_bytes` | 字节 | 模型实际可见字节数（L4 出口）——**与上一指标的差值分布即"折叠量"** |
| `tool_artifact_deref_bytes` | 字节 | 每次解引用返回的字节数——验证 P1-2（一次读完）是否达成 |

#### 快照扩展（`ToolEfficiencySnapshot` 新增字段）

```go
type ArtifactFlowSnapshot struct {
    Archives       LabeledSnapshot `json:"archives"`        // by layer/disposition
    Truncations    LabeledSnapshot `json:"truncations"`     // by layer/truncated_by
    Deref          DerefSnapshot   `json:"deref"`           // total, followup_ratio, miss_by_reason
    L1L4GapRatio   float64         `json:"l1_l4_gap_ratio"` // 派生：L4 截断次数 / L1 声称完整次数
}
```

`deriveInefficiencyFlags` 新增派生信号（对齐现有 flag 风格）：

- `artifact_deref_heavy`：followup 占比 > 30%（解引用链普遍 >1 跳）
- `l1_l4_gap_present`：`l1_l4_gap_ratio > 5%`（工具层与回显层预算竞争未消除——
  这正是 H-1 要消灭的信号，P0-1/H-1 实施前后该 flag 的消失即是效果证据）
- `archive_skipped_majority`：skipped 占比 > 80%（P1-1 阈值过严的信号）

### 11.3 runtimeobserve 事件扩展

在既有 `EventToolFinished` 的 metadata 上追加（不新增事件类型，避免动封闭目录）：

```
output_original_bytes, output_model_visible_bytes,
output_truncated_by_lines, output_truncated_by_bytes,
artifact_archived, artifact_id, pointer_notice_kind
```

同时 artifact_read 的 finished 事件带 `deref_source_id, deref_offset, deref_bytes`。
会话回放与 session-analytics（`docs/plan/session-usage-analytics-and-agent-diagnostics-plan.md`
的查询面）即可按会话聚合，无需新事件管道。

### 11.4 埋点位置与依赖方向

```
toolkit/tools/view.go ─┐
toolkit/tools/grep.go ─┼─→ observability.Record*   （import 方向已成立：
artifact_read.go ──────┘        toolkit → observability 无环）
output/gateway.go ─────┤
output/tool_result_content.go ─┘  （output 包 import observability 需检查：
                                   目前无依赖，但 observability 无反向依赖，安全）
```

**约束**：`internal/output` 引入 `observability` 前需确认 observability 不
（直接或间接）import output——当前 observability 仅依赖标准库与自身，安全；
若未来环了，退路是在 gateway 返回的 Envelope 上加观测字段、由
`agent/loop.go` 汇聚点统一上报（loop 已 import 两侧）。

### 11.5 指标与方案效果的映射（验收即读数）

| 方案项 | 验收指标 | 期望变化 |
|---|---|---|
| P0-1/H-1 | `truncation_total{layer=l4_render}` / `truncation_total{layer=l1_view}` | L4 截断占比大幅下降，`l1_l4_gap_present` flag 消失 |
| P0-2 | `truncation_total{layer=l4_render}`（grep 来源需靠事件 metadata 的 tool_name 维度离线聚合，不入标签） | 下降 |
| P1-1 | `archive_total{disposition=skipped_below_threshold}` | 从 0 上升到真实小结果占比 |
| P1-2 | `deref_total{page=followup}` 占比 | 从 ~50% 降到 <10% |
| P2/H-2/H-3 | `deref_total` 绝对值 + `pointer_notice_total{kind}` | 解引用总量下降；deref_hint 类指针的转化率可对比 |
| §10.5 成本论证 | `output_original_bytes` vs `output_model_visible_bytes` 直方图差 | 多轮会话累计折叠字节（即重复计费风险敞口）可量化 |

### 11.6 实施顺序（独立于 §9 的功能实施，可先行）

1. **O-1（先行，零风险）**：`gateway.go` + `artifact_read.go` 埋点 + 计数器/直方图定义——
   在**改动前建立基线**，没有基线就无法证明 P0/P1 的效果
2. **O-2**：`ToolEfficiencySnapshot` 扩展 + runtime status 暴露（对齐现有 snapshot 聚合模式与测试）
3. **O-3（随功能项同步）**：view/grep/render 埋点随 P0-1/H-1/P0-2 一并落
4. **O-4**：`runtimeobserve` EventToolFinished metadata 扩展（注意
   `TestKnownEventTypeCatalogCoversLocalLoopEmits` 与 projector 归一化测试同步更新）

O-1/O-2 预计半天工作量；基线数据采集一个真实工作日后即可支撑 §9 功能项的
实施决策与验收。


## 12. 前端观察页面与 HTTP 接口（usage 页面 + aicli micro web client）

### 12.0 现状结论（先回答"是否已接入"）

**后端已经暴露了观测数据，但两个前端消费面目前都没有使用它。** 具体：

| 面 | 现状 | 证据 |
|---|---|---|
| runtime-server `/api/runtime/status` | `runtime_statusSnapshot` 已注入 `"tool_efficiency": observability.SnapshotToolEfficiency()`（handler.go:9765） | grep 实证 |
| runtime-server `/api/runtime/observe/v1/*` | capabilities/snapshot/sessions/{id}/events 四端点在 `observe_handlers.go` 全部就绪 | handler.go:984 挂载 |
| frontend `/usage` 页面（usage-analytics） | `analytics.ts` 只消费 `/api/runtime/analytics/*` 与 status 里**区块 A**（usage_analytics 健康）；`tool_efficiency` 快照**无任何消费代码**（全仓 grep `tool_efficiency|toolEfficiency` 在 frontend 下零命中） | grep 实证 |
| aicli micro web client | 计划文档（aicli-micro-web-client-plan.md）只规划了 observe 平面四端点的透传页面，**未规划 tool_efficiency/artifact 流水视图**；且 micro client 与 frontend 是两套独立 UI，数据面不同（observe 平面 vs /api/runtime/analytics） | 计划文档 grep 实证 |
| `AnalyticsToolStat`（/usage 工具统计面板数据源） | 现有字段只有 calls/failures/duration 分位/empty_results/retried_calls——**没有 artifact/truncation/deref 维度** | types/runtime/analytics.ts:261-275 |

因此 §11 的指标落地后，若不做本节的消费端工作，观测数据将只存在于 status JSON 里，"看板缺口"会继续存在。

### 12.1 数据通路设计（三条，按投入排序）

**通路 1（推荐首选，改动最小）**：`/usage` 页面读取 status 快照里的 `tool_efficiency.artifact_flow`

- `analytics.ts` 新增 `getToolEfficiencySnapshot()`：复用 `getUsageAnalyticsHealth` 的**静默降级模式**（403/网络失败/缺块一律返回 null，绝不打断主数据渲染——与 analytics.ts:212-218 注释声明的契约一致）。
- 类型侧在 `types/runtime/analytics.ts` 增加 `AnalyticsArtifactFlow`（对齐 §11.2 的 `ArtifactFlowSnapshot` 字段），用与 `normalizeUsageAnalyticsHealth` 相同的 `readCount` 防御性归一化。
- **注意**：`/api/runtime/status` 与 `/usage` 其它数据一样受 `authorizeUsageAdmin` 保护——需要 admin token header（`buildAnalyticsHeaders`），缺失时整个面板静默隐藏（与 error-patterns-panel 对 health 缺失的处理一致）。

**通路 2（后端补一个只读分析端点，供 /usage 深页用）**：

- `GET /api/runtime/analytics/artifact-flow?session=&from=&to=`，参数风格对齐 `AnalyticsToolStatsQuery`（session/tool/outcome/from/to/limit）。
- 数据源二选一（P1 决策点）：
  - 快路径：直接聚合 GlobalMetrics 计数器/直方图（进程内，重启即失忆——适合"当前状态"视图）；
  - 慢路径：聚合 usage-analytics SQLite 里 `tool_finished` 事件的 metadata 字段（§11.3 追加的 `output_original_bytes` 等）——**支持跨重启历史窗口**，与 /usage 现有按时间窗查询的语义对齐，推荐此路。
- 响应 schema 沿用 `schema_version + generated_at` 包络，避免 /usage 各面板各自发明 envelope。

**通路 3（aicli micro web client）**：observe 平面已具备透传能力，micro client 只需在分析页加一个 "Artifact Flow" 卡片，直接 GET observe `/snapshot` 并选取 `tool_efficiency` 子对象渲染。**不新造接口**——aicli 本地 in-process 模式（chat_observe_http.go）与 runtime-server 模式（observe_handlers.go）返回同构快照，一张卡片两端通用。

### 12.2 前端页面方案（/usage）

在 usage-analytics 目录新增 `artifact-flow-panel.tsx`（命名与组件结构对齐 error-patterns-panel / subagent-stats-panel 的既有模式）：

**面板内容（四块）**：

1. **归档决策分布**（`tool_output_archive_total` by disposition）：archived / skipped_below_threshold / skipped_read_window / skipped_empty 横条图，直接回答"归档是否过严/过松"。
2. **截断层位分布**（`tool_output_truncation_total` by layer）：l1_view / l1_grep / l4_render / shell_capture 四段。**核心看板信号：l1_view 与 l4_render 的比值**——P0-1/H-1 落地前后该比值的变化就是优化效果的直接可视化（对应 §11.5 验收映射）。
3. **解引用行为**（`tool_artifact_deref_total`）：first vs followup 占比环形图 + `deref_miss` 分原因小表。followup 占比 > 30% 时面板渲染警告样式（对应 §11.2 的 `artifact_deref_heavy` flag）。
4. **L1→L4 折叠量**（`tool_output_original_bytes` vs `tool_output_model_visible_bytes` 直方图均值差）：以"每工具调用平均折叠字节"单值大字呈现——这是 §10.5 成本论证（重复计费风险敞口）的最终消费指标。

**降级矩阵**（对齐 overview.tsx 现有 health 处理模式）：

| 情况 | 行为 |
|---|---|
| status 403 / 无 admin token | 整面板不渲染（非 error 横幅） |
| snapshot 缺 `tool_efficiency` 块（旧后端） | 显示"后端版本不支持"占位，不报错 |
| 数值全零 + ingested_total=0 | 显示"暂无数据"空态（对齐 error-patterns-panel 的 empty 态） |

**i18n**：新增 key 全部挂 `observability.artifactFlow.*` 命名空间，与 error-patterns-panel 的 `observability.errors.*` 平级；中英两份 locale 同步补。

**测试**：对齐现有 `observability-panels.test.tsx` 模式——normalize 函数表驱动测试（缺字段/类型错/全零）+ 面板空态/警告态快照。

### 12.3 后端接口改动清单（供 §9 实施排序引用）

| # | 改动 | 位置 | 依赖 |
|---|---|---|---|
| F-1 | status 快照 `tool_efficiency` 块扩展 `artifact_flow` 子对象 | `observability/tool_efficiency_snapshot.go` + handler.go:9765 不变（自动透传） | §11.2 O-2 |
| F-2 | `GET /api/runtime/analytics/artifact-flow` 只读端点 | `internal/api/skills/analytics_handlers.go`（对齐现有 analytics 路由组 + `authorizeUsageAdmin`） | §11.3 事件 metadata 落库 |
| F-3 | observe `/snapshot` 返回体确认含 `tool_efficiency`（若 observe projector 当前不透传则补一行映射） | `observe_handlers.go` / aicli `chat_observe_http.go` | 无 |
| F-4 | frontend：`getToolEfficiencySnapshot()` + `AnalyticsArtifactFlow` 类型 + `artifact-flow-panel.tsx` + i18n + 测试 | usage-analytics 目录 | F-1 |
| F-5 | aicli micro web client：分析页 Artifact Flow 卡片（纯透传渲染） | micro client 分析页 | F-3 |

### 12.4 与既有方案章节的关系

- **不改变 §11 的指标定义与埋点位置**——本节只是消费端，指标语义以 §11.2 为唯一权威。
- **依赖方向**：O-1/O-2（基线埋点）先行 → F-1/F-3 透传 → F-4/F-5 前端消费。前端可在基线期同步开发（用 mock 快照开发，联调等 O-2 合入）。
- **§8 遗留问题补充一条**：usage-analytics SQLite 的 `tool_finished` metadata 目前**不落库**（§11.3 的扩展字段需确认 collector→DB 的字段白名单），F-2 慢路径依赖此前置项，若白名单未开，F-2 先走快路径（进程内聚合）降级上线。

---

## 逐工具截断归属审计（per-tool truncation ownership audit）

审计目标：确认哪些工具**已经自带截断/预算**（受控工具，自己决定是否截断），
哪些工具**依赖 L4 兜底**（需要补齐）。原则：**工具自控，L4 只兜底、不做上限**。

### 分类结果

| 工具 | 现状证据 | 类别 |
| --- | --- | --- |
| `view` | 行协议默认 2000 行 + 字节预算；长行行内截断（`LongLinesTruncated`）；artifact 续读；`observability.TruncationLayerView` 指标 | **A 已自控** |
| `grep` | 结果条数/字节双预算 + 截断标记 + 续读提示 | **A 已自控** |
| `glob` | `partTruncated` 分片 + 截断标记 | **A 已自控** |
| `bash` | capture limit；`output_truncated` / `capture_limit_reached`；artifact 回退 | **A 已自控** |
| `aicli_exec` | `capture.Truncated` 判定 | **A 已自控** |
| `artifact_read` | 分页窗口（offset/limit）+ 明确契约，天然有界 | **A 已自控** |
| `sourcegraph` | 已有多处截断逻辑（粒度为条数） | **A 已自控** |
| `apply_patch` / `edit` / `multiedit` / `append_write` / `write` / `write_idempotency` | 只回 `toolresult.MutationSummary` 变更摘要，天然小输出 | **B 天然小** |
| `file_mutation_guard` | 单行守卫提示 | **B 天然小** |
| `download` | 读取上限 `maxSize = 100MB`，但 `Content` 只回**落地路径 + 字节数** | **B 天然小** |
| `fetch` | 仅 `io.LimitReader(resp.Body, f.maxSize+1)` 限制**读取**；提取后的正文**全量**进 `Content`，无截断、无 artifact 回退 | **C 需优化** |
| `ls` | 未见条目数/字节上限 | **C 待确认** |
| `web_search` | 未见条数/正文长度上限 | **C 待确认** |
| `todos` | 未见列表大小上限 | **C 待确认** |
| `openai_image_generate` | 返回体大小未设上限 | **C 待确认** |

### 改造方向（C 类统一模式）

复用 `view.go` / `bash.go` 已有模式，而不是在 L4 加压：

1. **工具内预算**：工具返回前按字节（必要时叠加条目数）预算裁剪，优先保留 head + tail。
2. **结构化截断标记**：写出 `output_truncated` / `truncated_by`（bytes|entries|lines）等字段，供模型与下游识别。
3. **artifact 落盘回退**：超预算内容写入 artifact 文件，`Content` 保留窗口 + 明确续读指令（`artifact_read`）。
4. **分层指标**：`observability.RecordToolOutputBytes` / `RecordToolOutputTruncation` 携带 layer 与 tool 名，避免"谁截断的"不可追溯。

### L4 约束（兜底而非上限）

- L4 仅当**工具未声明自控**（无截断标记 / 无 artifact 引用）且超出自身预算时才截断；
- 对已带自控标记的输出，L4 **不得二次压缩**（否则受控工具的窗口会被再次削小）；
- L4 自身截断必须记录 layer 指标，便于区分「工具截断」与「L4 兜底截断」。
