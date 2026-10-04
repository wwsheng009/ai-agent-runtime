# 缺口交接：auto-start team 的 `AmbientRunMeta` 丢失

状态：**两个根因均已确诊；第一个曾有可用修复但已回退（见 §3），第二个未修**。
基线 `5b5fe77c`，整包 2 个存量失败。

## 复现（4 秒，勿用整包）

```powershell
cd backend
go test ./cmd/aicli/commands/ -count=1 -run '^(TestAICLIChatActorExecutor_AutoStartTeamPublishesSingleTerminalEvents|TestAICLIChatActorExecutor_AutoStartTeamMarksBaseSessionRunningUntilSettled)$'
```

单跑目标用例 **PASS**；与前者配对才 **FAIL**。失败断言在
`chat_local_orchestration_integration_test.go:1003`，唯一失败字段是
`AmbientRunMeta == nil`（`Status`、`CurrentRunMeta` 均正确）。

## 1. 根因一：actor 整行覆盖抹掉 ambient（真实 bug）

```
写者 A — syncAmbientTeamLifecycleState (chat_team_drain.go:333)   ← 全仓唯一生产写入方
  case pending:              state.AmbientRunMeta = currentRunMetaForSession()  :362
  case !pending && != nil:   state.AmbientRunMeta = nil                          :366

写者 B — SessionActor.updateState (internal/chat/actor.go:3763, SaveState 在 :3795)
  next := a.state.Clone()
  ↑ a.state 由 loadState(:3533) 在**构造时**载入，早于 ambient 绑定写入
  ⇒ 其 AmbientRunMeta 恒为 nil，每次保存都整行覆盖把它抹掉
```

插桩实测（`InMemoryRuntimeStore.SaveState`，`AA_SAVE`）：40 次落库中
**6 次 `in=false stored=true`**，调用链 `actor.go:3795 <- :3829 <- :3008`
与 `actor.go:3795 <- :1092 <- :1005`。

### 修法与回退记录

已实现并验证过一个修复：`updateState` 落库前 `LoadState` 取回当前
`AmbientRunMeta` 保留。实测效果：

- actor 的 6 次误清 **6 → 0**（40 次落库全部来自 drain）
- `internal/chat` 全量通过
- `cmd/aicli/commands` 整包失败面不变（仍 2 个）
- **但不修复目标用例**（见根因二）

**已回退**，理由：收益无法由任何测试验证，代价是三项未验证影响——
(a) 每次 `updateState` 增加一次 `LoadState`（热路径，SQLite 后端为额外 SELECT）；
(b) `a.state.AmbientRunMeta` 语义改变，影响 `turn_tool_surface_snapshot.go:126`
的 permissionMode 回退（无测试覆盖）；(c) 无法证明修复了任何可观测行为。

若重做，建议一并补一个直接覆盖「actor 保存后 ambient 仍在」的单元测试，
否则不宜合入。

## 2. 根因二：team 在断言前已 settle（目标用例的真正失败原因）

对 `syncAmbientTeamLifecycleState` 的 switch 插桩（`AA_CLEAR`），配对运行实测：

```
每个 base session:
  调用 10~13 次, binding 为 nil 0 次, pending=false 1~4 次, hadAmbient=true 9 次
  会触发清除的 (!pending && hadAmbient) = 恰好 1 次
```

要点：

- **`binding` 从未为 nil** —— 排除了「绑定解析瞬时失败触发清除」的猜测
- 清除恰好发生 **1 次**，且是该 session 的最后一次落库
- 即：team 在断言之前就已 settle，drain 执行了**合法**的清除

而用例的前提是「`Execute` 返回时 team 仍 pending」。该前提在配对运行中不成立——
这正是它单跑通过、配对失败的原因。已排除的假设：

| 假设 | 证伪方式 | 结果 |
|---|---|---|
| binding 解析返回 nil | `AA_DIAG` / `AA_CLEAR` | 全程非 nil |
| `Pending` 误报 false | `AA_DIAG` | pending=true 为主，仅收尾转 false |
| `currentRunMetaForSession` 返回 nil | `AA_DIAG2` | **0 行**，从未进入 return 分支 |
| `LoadState` aliasing | 读实现 | 返回 `state.Clone()`，深拷贝 |
| store 会合并该字段 | 读实现 | 只对工具面 reuse-or-clone，不碰 ambient |

### 下一步需要判定（未做）

需要在 `Execute` 返回点与前述那次清除之间打时间戳，判定：

- **(a) 测试假设过时**：mock planner 完成太快，配对运行时被预热，
  team 在 `Execute` 返回前就结束 ⇒ 应修测试（给它一个确定性同步点），
  但**改测试有掩盖真 bug 的风险，须先确认生产行为正确**
- **(b) 生产真 bug**：drain 的清除条件 `!pending` 过宽——`Pending()` 在
  「team 已注册但尚未置为 pending」时也会返回 false，从而误清。
  若属实，修法是让清除额外要求 team 确为终态
  （`team.IsTerminalTeamStatus(record.Status)`，同 `shouldPropagateTeamRunMeta`
  的既有口径），而非仅凭 `!pending`

## 3. 同批次另一缺口（未修，风险更高）

`chat_history_reconcile_test.go:568`
（`TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning`，
报 `primary history viewport is missing "history user 6"`）。

定位：presenter 的**计划**正确（`app_screen_layout.go` 的 `bottom.Rows` 含
`history user 6`），但**实际写屏**的那一份行序不同——是 presenter 与
`LayoutAppScreen` 计划脱节，不是布局计算错误。建议独立一轮处理。

## 附：本轮验证口径

```powershell
go test ./internal/chat/... -count=1                 # PASS（32s）
go test ./cmd/aicli/commands/ -count=1               # 2 个已知失败，174s
```

整包失败面自 `65a7d19c` 起未扩大：始终是上述两个用例。
