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

## 剩余缺口（独立问题，风险更高）

`chat_history_reconcile_test.go:568`
（`TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning`，
报 `primary history viewport is missing "history user 6"`）。

定位：presenter 的**计划**正确（`app_screen_layout.go` 的 `bottom.Rows` 含
`history user 6`），但**实际写屏**的那一份行序不同——是 presenter 与
`LayoutAppScreen` 计划脱节，不是布局计算错误。建议独立一轮处理。

## 验证口径

```powershell
go test ./internal/chat/... -count=1            # PASS（31s）
go test ./cmd/aicli/commands/ -count=1          # 1 个失败（172s）
# 配对复现 3 次                                    # 3/3 PASS
```
