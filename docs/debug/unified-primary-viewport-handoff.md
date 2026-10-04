# 交接：UnifiedPrimaryViewport 用例失败（活跃 cell 溢出头部挤掉 history 尾部）

**目标**：让下面这个用例通过，并保证不掩盖真实 bug。

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./cmd/aicli/commands/ -count=1 -run '^TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning$'
```

**当前状态**：单独运行即**确定性失败**（不是配对时序问题）。整包
`go test ./cmd/aicli/commands/ -count=1` 当前仅剩这 1 个失败（172s）。

## 一、症状（逐行对照，width=52 height=15，OutputBottomRow=6）

```
计划 rows 1-6  = ["", "history assistant 5", "", "history user 6", "", "history assistant 6"]
计划 rows 7-12 = band: "active reasoning line 03".."08"
实际 rows 1-6  = ["", "history assistant 6", "─── reasoning ───", "────", "line 01", "line 02"]
实际 rows 7-12 = band: "active reasoning line 03".."08"   ← 与计划一致
```

断言失败点：`chat_history_reconcile_test.go:568`
`primary history viewport is missing "history user 6"`。

计划取自 `LayoutAppScreen(state)`（完整 transcript）；rows 7-12 两边一致，说明
band 部分正确，分歧只在 rows 1-6。

## 二、已确认的机制（插桩过，勿再重复验证）

presenter 热路径**故意清空 transcript 再构图**，viewport 内的 transcript 行改由
HistoryCommit 投递填充，**不在帧里**——所以帧的 rows 1-6 恒为 `Owner:gap/Text:""`，
这是**设计**不是陈旧帧：

```go
// backend/cmd/aicli/ui/terminal_session.go:153-161
func composeTerminalViewportFramePlan(state AppState) TerminalFramePlan {
	state.Transcript = TranscriptState{}
	return ComposeTerminalFramePlan(state)
}
```

帧构造链：`app_render_frame.go:46 ComposeAppRenderFrame` → `LayoutAppScreen`
→ `terminal_session.go:64 ComposeTerminalFramePlan` / `:134 composeTerminalViewportTransactionPlan`。

因此 rows 3-6 实际屏幕上的 `─── reasoning ───` 与 `line 01/02` **只能来自
HistoryCommit**。即：**活跃 reasoning cell 的溢出头部被序列化进 transcript，
并作为 history 插入 viewport**，占掉 4 行，正好挤掉 `history user 6`（计划 rows 3-4）
与 `history assistant 5`（计划 row 2）。

设计意图：活跃 cell 在 viewport 内应只占 band 的 6 行（rows 7-12）；溢出部分应
进 **scrollback**，而不是插入 viewport 顶部占掉已 finalize 的 history 尾部。

**嫌疑落点**：活跃 cell 的 overflow handoff（引入本用例的提交 `672ccdc2`
"feat(ui): close unified render cutover with active cell overflow handoff"）
在 viewport 模式下的**插入位置/容量计算**。

## 三、下一步（很具体）

1. 在 HistoryCommit 发射点插桩：打印每个 commit 的 `CellID`、行数、以及
   `terminalHistoryInsertionANSI`（`terminal_session.go:1566`）的 `inserted` 行数
   与 `capacity`。
2. 判定：reasoning cell 的溢出头部是**何时、以多大容量**被列入 transcript 的。
3. 对照 `terminalRetainHistoryTailRows`（`:1471`）/ `terminalAppendHistoryTailRows`
   （`:1481`）的容量与保留策略，确认 viewport 顶部被谁占用。

## 四、已踩过的坑（勿重复）

| 方向 | 结论 |
|---|---|
| 打印 `plan.Frame.Rows[i].Text` 全空串 | **插桩假象**。文本在 `RenderRows`（结构化 `render.Line`）里，不在 `Rows`。据此外推的"陈旧帧"结论不成立 |
| 打印 `Rows[i]` 全字段确认 rows 1-6 恒为 gap | 这是设计（见上），不是陈旧帧 |
| presenter 写的是陈旧帧 / band 高度判据不同 | 均被上一条否定 |

## 五、仓库约定（本会话已遵守，新会话请延续）

- 提交信息中文，格式 `type(scope): 标题` + 正文说明"证据 → 结论"
- **不接受未经测试验证的改动**：本会话曾实现并验证 actor 的 ambient 保留修复
  （误清 6→0、`internal/chat` 全量通过），因无法由任何测试验证收益、且改变
  `a.state.AmbientRunMeta` 语义影响 `turn_tool_surface_snapshot.go:126` 的
  permissionMode 回退（无测试覆盖），**已回退**
- 插桩必须带 env 开关（`AA_DIAG_*`），收尾时 `git checkout` 清除并确认工作树干净
- 验证口径：
  - `go test ./internal/chat/... -count=1` → PASS（31s）
  - `go test ./cmd/aicli/commands/ -count=1` → 1 个失败（172s）

## 六、相关背景（已修复，勿回退）

- `LateReasoningBarrier...` 已修（`65a7d19c`）
- `TestAICLIChatActorExecutor_AutoStartTeamMarksBaseSessionRunningUntilSettled`
  已修（`e5653ae1`，`teammateHold` channel 门控，配对 3/3 PASS）
- 完整诊断史：`docs/debug/auto-start-team-ambient-run-meta.md`
