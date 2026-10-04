# auto-start team 的 `AmbientRunMeta` 断言缺口

**状态：已修复（`e5653ae1`，测试侧）。** 整包失败面 2 → 1。

## 结论先行

该用例的失败**不是生产 bug**，而是测试自身的时序假设：

- drain 的清除路径**只在 team 真正终态时触发**（插桩实测 `teamStatus="done" terminal=true`）
- 失败时 team 在 `Execute` **仍在运行期间**就结束了，清除比 `Execute` 返回早 85ms
- 等测试断言时，ambient 已被**正确**清掉

## 复现（4 秒）

```powershell
cd backend
go test ./cmd/aicli/commands/ -count=1 -run '^(TestAICLIChatActorExecutor_AutoStartTeamPublishesSingleTerminalEvents|TestAICLIChatActorExecutor_AutoStartTeamMarksBaseSessionRunningUntilSettled)$'
```

修复前：单跑目标用例 PASS，与前者配对 **必 FAIL**。修复后配对 3/3 PASS。

## 决定性证据（时间戳插桩）

在 `chat_team_drain.go` 的 switch 与 `Execute` 返回点同时打时间戳：

```
失败 session:
  AA_CLEAR     t=12:49:10.993256  pending=false  teamStatus=done  terminal=true
  AA_EXEC_RET  t=12:49:11.078214
  ⇒ 清除早于 Execute 返回 85ms

通过的 session（对照）:
  AA_EXEC_RET  t=12:49:10.618104
  AA_CLEAR     t=12:49:10.660092
  ⇒ 清除晚于 Execute 返回 42ms
```

两次清除都是 `teamStatus=done`。**同一个用例，成败只取决于调度时序。**

## 修法

`autoStartLocalOrchestrationProvider` 增加 `teammateHold` channel 门控：teammate
在产出 task 结论前阻塞直到测试放行（保留 `ctx.Done()` 取消路径）。用例在断言
之后 `release()`，使「team 仍 pending」成为**确定状态**而非时序假设。

与仓库既有的 `interruptibleAutoStartLocalOrchestrationProvider` 用
`started`/`canceled` channel 同步的做法一致。`teammateDelay` 保留不动。

被替换掉的机制是 `teammateDelay: 120ms`（provider 里的 `time.Sleep`）：它隐含
「`Execute` 耗时 < 120ms」的假设，配对运行时该假设不成立。

## 已排除的假设（全部经插桩证伪，非推断）

| 假设 | 证伪方式 | 结果 |
|---|---|---|
| binding 解析瞬时返回 nil | `AA_DIAG` / `AA_CLEAR` | 全程非 nil |
| `Pending()` 误报 false | `AA_DIAG` | pending=true 为主，仅收尾转 false |
| drain 提前清除（生产 bug） | `AA_CLEAR` 带 teamStatus | 仅 `done`/terminal=true 时清除，合法 |
| `currentRunMetaForSession` 返回 nil | `AA_DIAG2` | **0 行**，从未进入 return 分支 |
| `LoadState` aliasing | 读实现 | 返回 `state.Clone()`，深拷贝 |
| store 会合并该字段 | 读实现 | 只对工具面 reuse-or-clone，不碰 ambient |

## 同批次遗留：另一真实 bug（未修，已回退）

`internal/chat` 的 `SessionActor.updateState`（`actor.go:3763`，SaveState 在
`:3795`）会**整行覆盖** runtime state，而 `a.state` 由 `loadState`(:3533) 在
**构造时**载入，早于 ambient 绑定写入 ⇒ 其 `AmbientRunMeta` 恒为 nil，每次
保存都把 drain 刚写入的绑定抹掉。

插桩实测：40 次落库中 **6 次 `in=false stored=true`**，调用链
`actor.go:3795 <- :3829 <- :3008` 与 `actor.go:3795 <- :1092 <- :1005`。

**这是真实 bug，但不是本用例的失败原因**（它只造成一个短暂的读取窗口，drain
随后会重新写入）。曾实现修复（`updateState` 落库前 `LoadState` 取回 ambient
保留），实测误清 6 → 0、`internal/chat` 全量通过、整包失败面不变，但**已回退**：

- 收益无法由任何测试验证（不修复任何用例）
- 代价：热路径每次多一次 `LoadState`（SQLite 后端为额外 SELECT）
- 副作用：`a.state.AmbientRunMeta` 语义改变，影响
  `turn_tool_surface_snapshot.go:126` 的 permissionMode 回退，**无测试覆盖**

若重做，**先补一个直接覆盖「actor 保存后 ambient meta 仍在」的单元测试**，
否则不宜合入。

## 剩余缺口：活跃 cell 溢出内容被插入 viewport，挤掉 history 尾部（未修）

`chat_history_reconcile_test.go:568`
（`TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning`）

**单独运行即确定性失败**（非配对时序问题）。逐行对照（width=52 height=15，
`OutputBottomRow=6`）：

```
计划 rows 1-6  = ["", "history assistant 5", "", "history user 6", "", "history assistant 6"]
计划 rows 7-12 = band: "active reasoning line 03".."08"
实际 rows 1-6  = ["", "history assistant 6", "─── reasoning ───", "────", "line 01", "line 02"]
实际 rows 7-12 = band: "active reasoning line 03".."08"   ← 与计划一致
```

### 机制（已插桩确认）

presenter 热路径**故意清空 transcript 再构图**，viewport 里的 transcript 行
改由 HistoryCommit 投递填充，不在帧里：

```go
// terminal_session.go:153-161
// composeTerminalViewportFramePlan omits finalized transcript cells from the
// mutable primary-frame projection. In unified mode those rows are delivered
// exclusively by tokenized HistoryCommit effects and retained by the terminal
// itself; ...
func composeTerminalViewportFramePlan(state AppState) TerminalFramePlan {
	state.Transcript = TranscriptState{}
	return ComposeTerminalFramePlan(state)
}
```

插桩实测（`FlushTransaction` 打印 `Rows[i]` 全字段）：presenter 的**每一帧**
rows 1-6 都是 `Owner:gap / Text:""`，rows 7-12 为 band —— 与上述设计一致，
**不是陈旧帧**。

所以真正的分歧在**投递侧**：实际屏幕 rows 3-6 装的 `─── reasoning ───` 与
`line 01/02` 只能来自 HistoryCommit，即**活跃 cell 的溢出头部被序列化进
transcript 并作为 history 插入 viewport**，占掉 4 行，正好挤掉 `history user 6`
（计划 rows 3-4）与 `history assistant 5`（计划 row 2）。rows 7-12 由帧的 band
提供，两边一致。

即：轨迹显示 reasoning 块共占 10 行（rows 3-12），而设计上活跃 cell 在 viewport
内应只占 band 的 6 行（rows 7-12）。溢出头部应进 scrollback，而不是插入 viewport
顶部占掉已 finalize 的 history 尾部。嫌疑集中在活跃 cell 的 overflow handoff
（引入本用例的提交 `672ccdc2` 的主题）在 viewport 模式下的插入位置/容量计算。

### 已验证无效的方向（勿重复）

- 打印 `plan.Frame.Rows[i].Text` 得到全空串——文本在 `RenderRows`（结构化
  `render.Line`）里，不在 `Rows`。那次"全空"是插桩假象，不是发现。
- 打印 `Rows[i]` 全字段后确认 rows 1-6 恒为 `gap`：这是设计，不是陈旧帧。

本轮未做代码修改。

## 验证口径

```powershell
go test ./internal/chat/... -count=1            # PASS（31s）
go test ./cmd/aicli/commands/ -count=1          # 1 个失败（172s）
# 配对复现 3 次                                    # 3/3 PASS
```
