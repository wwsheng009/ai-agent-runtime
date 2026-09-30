# UI actor 卡顿与 tool.completed 丢弃修复记录（2026-09-30）

> 现场：`session_20260927073805_QbWBceF5`（pid 15536，endpoint 127.0.0.1:52977）
> 症状：TUI 已提交正文自 09:50 起冻结；模型/工具持续工作（`model_items` 12664→12797+）
> 状态：代码修复 + 回归测试 + 基准已落地；线上旧进程需重建/重启后生效

## 1. 症状与证据链

1. `render_output.last_sequence = primary_committed = 14062`、`projection.frame = 14064`、
   history `acked=116086` 在 10:06→10:38 多次采样完全不变；`plan_incomplete=true`、
   `mutable_cells` 16→31，`app_state.active_cell=12603 (mutable)`。
2. TUI 帧尾部停在 `• Running edit file_path=itsm-backend/capability/…`，
   状态行 `Analyzing (45m 11s)`；而 journal/`/web/api/screen` 已是最新内容。
3. `debug.log`：`09:50:10.789 render suppressed reason="UI actor mailbox stalled; event dropped"
   type="tool.completed"`。step 66 edit（`call_00_Bn7cpaz4TkjR5kDT8lY58413`）在 journal 中
   只有 `tool.requested` / `tool.reduced`，**没有 `tool.completed`**。
4. 机制：`canonicalHistoryCommitFrontier`（`ui/history_effect_planner.go:137`）规定
   “第一个 mutable cell 是 barrier，后面的 cell 都不能越过”。一条终态丢失 = tool-chain
   cell 永远 mutable = 已提交显示永久冻结。该轮另有无流事件丢弃 31 条
   （dynamic_status 14 / tool.reduced 5 / tool.requested 5 / tool.completed 4 / llm.* 3）。

## 2. 性能归因（现场 profile + goroutine 采样）

- `plan-max-ms` 20.6s → 75.5s，`plan-count` ~9/min；`ui_actor.post_wait p95 7.4s / max 17.8s`，
  `pending` 长期贴顶 256/257、`dropped` 2.3k+；reasoning delta 丢弃 6.1 万条。
- goroutine 采样连续 8 轮捕获 UI actor 位于
  `reflect.DeepEqual`（`controller_state.go:946`）：每个权威 Scene 快照都要
  ① `NewTranscriptState` 全量深克隆（`cloneTranscriptCell` → `Presentation.Clone`），
  ② `transcriptReplacementOnlyUpdatesActive` 对全部 cell 做 `reflect.DeepEqual`
  （含 Document 树）。两者都是 O(全部 cell × 内容)，8k cell 会话下单快照即数百 ms
  到数秒，256 槽邮箱被拖垮，事件桥在 5s 预算（`uiActionPostBudget`）后开始丢事件。
- 另一处放大：plan memo 用**总 cell 数**做围栏，忙碌 turn 每追加一个 *mutable* 尾部
  cell 就全量重规划（基准 723ms/op @2000 cells）。

## 3. 修复

### 3.1 终态事件不再静默丢弃（`cmd/aicli/commands/chat_runtime_events.go`）

- 新增 `uiActorPostMustNotDrop`：复用既有分级契约（`eventIsCritical`，enforce 模式下
  `classifyChatRuntimeEvent == eventClassCritical`），把 `tool.completed` /
  `tool_finished` / `llm.request.finished` / `assistant_message` / `session_end` /
  审批提问终态等一并纳入“不丢”集合（off/observe 模式保持回滚语义）。
- `postRuntimeEventToUIActorWithEpoch`：必须不丢的事件在邮箱拥塞时**重新武装有界等待**
  （日志 `critical event remains pending (type=…)`），不再 5s 后丢弃；epoch 退休也放行
  （与 subagent lifecycle 同语义）。
- `handleQueuedEvent`：stale-epoch 守卫与“actor 关闭时走 legacy 兜底”都改用同一判定，
  保证 critical 事件在任一出口都不会静默消失。

### 3.2 快照热路径去反射与增量克隆（`cmd/aicli/ui/`）

- `transcriptReplacementOnlyUpdatesActive`：`reflect.DeepEqual` → 
  `transcriptCellUnchangedForActiveOnly`（ID + Revision + static metadata + Source；
  Scene 事务契约保证 Revision 是变更围栏，Presentation 变化走
  `SetThemeContextAction` 的重排路径）。
- 新增 `newTranscriptStateFromSnapshot`：只深克隆变化后缀，未变前缀按值复用；
  仅 `ReplaceTranscriptAction` 的 fallback 路径使用（`controller_state.go:512`）。

### 3.3 plan memo 围栏改为 finalized 计数（`ui/history_effect_planner.go`）

- 新增 `transcriptFinalizedCellCount`，memo 比较 finalized cell 数而不是总 cell 数：
  追加 mutable 尾部 cell 不再触发全量重规划；finalized 变化仍由 finalized-prefix
  指纹 + finalized 计数双重保证失效。

## 4. 基准（2000 finalized cells + mutable 尾部，Windows Xeon E5-2686，本机含活跃会话）

| 基准 | 修复前 | 修复后 |
|---|---|---|
| Mutable-tail 快照 reduce（命中 memo） | 225 ms/op · 4.5 MB · 14,654 allocs | 2.2–18 ms/op · 385 KB · **14 allocs**（机器负载下抖动；allocs 稳定） |
| 追加 mutable 尾部快照 | 723 ms/op · 18.7 MB · 43,692 allocs | **13.9 ms/op · 7 allocs**（≈52×） |
| activeOnly 快照 | — | 48 µs/op · 2 allocs |

## 5. 回归测试

- `cmd/aicli/commands/chat_runtime_events_critical_delivery_test.go`
  - `TestCriticalToolCompletedPostSurvivesStalledMailbox`（邮箱满 → 不丢弃 → 排空后送达）
  - `TestCriticalToolCompletedPostSurvivesStaleRunEpoch`
  - `TestNonCriticalEventStillDropsAfterBoundedWait`（对照：非 critical 仍有界降级）
- `cmd/aicli/ui/transcript_snapshot_incremental_test.go`
  - 前缀复用 / 变化后缀深克隆（改 snapshot 不回灌）/ 不同 SceneID 全量重建 /
    廉价比较器覆盖 Source、metadata、Revision 变化。
- `cmd/aicli/ui/history_memo_mutable_append_test.go`
  - 追加 mutable cell 不使 memo 失效；追加 finalized cell 必须失效。
- `ui/history_prepend_replan_bench_test.go` 适配 finalized 计数语义。

验证命令：

```powershell
cd backend
go build ./...
go vet ./cmd/aicli/ui/ ./cmd/aicli/commands/
go test ./cmd/aicli/ui/ -run 'TestTranscriptPlanMemo|TestNewTranscriptStateFromSnapshot|TestTranscriptCellUnchanged|TestTranscriptReplacementOnlyUpdatesActive' -count 1
go test ./cmd/aicli/commands/ -run 'TestCriticalToolCompletedPostSurvives|TestNonCriticalEventStillDrops|TestStreamingRuntimeEventPostDropsAfterBoundedWaitWhenMailboxStalled' -count 1
go test ./cmd/aicli/ui/ -run '^$' -bench 'BenchmarkReplaceTranscript' -benchtime 30x
```

## 6. 已知残留与后续

- 线上 pid 15536 跑的是修复前二进制；需重建/重启（或 `resume` 重建 Scene）才能生效，
  且**已被丢弃的那条终态不会自愈**，旧会话恢复后需从历史重建显示。
- `LayoutTranscript`/`LayoutRows` 仍是 O(全部行) 全量构建（8k cell / 23.8 万行），
  finalized cell 真正变化时的全量规划不可避免；后续可做增量行缓存（按 cell Revision 复用前缀）。
- 基线中已存在的 flaky/环境失败（干净代码同样失败，与本次改动无关）：
  `TestKeyHandlerStart_DoesNotPollSessionInputWhileSuspended`、
  `TestNextInteractiveKeyReadsFromPipeBackedStdin`、
  `TestTerminalSessionExecutorFailedReconciliationArmsBackoff`、
  `TestChatRuntimeEvents_DoesNotRestorePromptUntilInteractionReady`（高负载下偶发）。
