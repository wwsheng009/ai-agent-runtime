# 缺口交接：auto-start team 的 `AmbientRunMeta` 丢失

状态：**成因已确诊（lost update），未修复**。基线 `65a7d19c`，整包 2 个存量失败。

## 复现（4 秒，勿用整包）

```powershell
cd backend
go test ./cmd/aicli/commands/ -count=1 -run '^(TestAICLIChatActorExecutor_AutoStartTeamPublishesSingleTerminalEvents|TestAICLIChatActorExecutor_AutoStartTeamMarksBaseSessionRunningUntilSettled)$'
```

单跑目标用例 **PASS**；与前者配对才 **FAIL**。症状只集中在
`chat_local_orchestration_integration_test.go:1003`：

```
expected base session runtime state to stay idle with ambient team metadata
while team is pending, got &{… Status:idle CurrentTurnID: SuspendedTurnID:
CurrentCheckpointID: CurrentRunMeta:<nil> AmbientRunMeta:<nil> …}
```

`Status` / `CurrentRunMeta` 都正确，**唯一失败字段是 `AmbientRunMeta == nil`**。

## 确诊过程（每一步都由插桩证伪，非推断）

在 `InMemoryRuntimeStore.SaveState` 加临时日志后，一次复现得到 40 次落库，其中
**6 次是覆盖写**：

```
AA_SAVE in=false stored=true <- actor.go:3795 <- actor.go:3829 <- actor.go:3008
AA_SAVE in=false stored=true <- actor.go:3795 <- actor.go:1092 <- actor.go:1005
AA_SAVE in=false stored=true <- chat_team_drain.go:368 <- chat_actor_host.go:3807
```

`in=false stored=true` 的含义是：**本次写入要把已存储的 ambient meta 抹成 nil**。

### 两个写者争同一行

```
写者 A — syncAmbientTeamLifecycleState (cmd/aicli/commands/chat_team_drain.go:333)
  LoadState → case pending: state.AmbientRunMeta = currentRunMetaForSession(…)  :362
            → case !pending && != nil: state.AmbientRunMeta = nil                :366
            → SaveState
  ↑ 全仓唯一一处生产写入 AmbientRunMeta

写者 B — internal/chat Actor.mutateState (backend/internal/chat/actor.go:3791-3795)
  next := a.state.Clone()   // a.state 自初始化起就没有 AmbientRunMeta
  mutate(next); a.stateStore.SaveState(ctx, next.Clone())
  ↑ 整行覆盖 ⇒ 每次都把 ambient 写成 nil
```

`internal/chat/actor.go` 全文只设 `CurrentRunMeta`、从无 `AmbientRunMeta`。
actor 的 state 从未装载过该字段，所以它**必然**在每次保存时清空它。

### 已排除的假设（都做过插桩证伪）

| 假设 | 证伪方式 | 结果 |
|---|---|---|
| `resolvedInteractiveTeamBinding` 返回 nil | `AA_DIAG` 全程 23 次 | `binding="team-auto"` 始终非 nil |
| `interactiveTeamPendingByTeamID` 误报 false | 同上 | `pending=true` 全程，仅收尾转 false |
| `currentRunMetaForSession` 返回 nil | `AA_DIAG2` | **0 行**，从未进入该 return 分支 |
| `LoadState` 返回内部指针（aliasing） | 读实现 | 返回 `state.Clone()`，深拷贝，无别名 |
| `cloneRuntimeStateForInMemoryStore` 会合并该字段 | 读实现 | 只对**工具面**做 reuse-or-clone；`AmbientRunMeta` 直接取调用方值 |

## 为什么只在整包失败

能否通过取决于**最后一次落库是 A 还是 B**。单跑时交错顺序恰好以 A 收尾 → 通过；
跑过其它 auto-start team 流程后顺序改变，最后一次变成 B → 失败。这也解释了
「凡是跑过 auto-start team 流程的用例都会触发后续用例失败」。

## 修复方向（需设计决策，非一行补丁）

难点在于**无法区分 nil 的两种语义**：
- actor 的 nil =「我不知道这个字段」→ 应保留旧值
- `chat_team_drain.go:366` 的 nil =「请清除」→ 必须生效

所以单纯在 `cloneRuntimeStateForInMemoryStore` 里做「nil 则保留 previous」
会**破坏 `:366` 的主动清除路径**。需要给清除一个显式信号（哨兵/专用方法），
或改所有权模型。

### 推荐做法：让 actor 在保存时向提供方取当前值

仓内已有同构先例——`chat_actor_host.go:1811` 的 `TriggerTurnRunMeta` 钩子：
actor 在写入 run meta 前向宿主询问，而不是持有可能陈旧的值。

按同一模式给 actor 注入一个 `AmbientRunMeta` 提供方，在
`mutateState` 落库前取值，即可让 actor 永不写入陈旧 nil，`A` 保持为唯一真实来源。

代价：跨 `internal/chat` 与 `cmd/aicli/commands` 的接线改动，
`actor.go` 有 4 处 `SaveState` 调用点（:3554 / :3557 / :3597 / :3795）需一并核对。

### 修完必须验

```powershell
go test ./internal/chat/... -count=1
go test ./cmd/aicli/... -count=1            # 整包，确认存量失败仍是 2 个
```

另需确认 `chat_team_drain.go:366` 的主动清除在 team 收尾后仍生效
（`:414` 有依赖 `state.AmbientRunMeta` 的读取方）。

## 同批次另一缺口

`chat_history_reconcile_test.go:568`
（`UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning`，
报 `primary history viewport is missing "history user 6"`）。

定位：presenter 的**计划**正确（`app_screen_layout.go` `bottom.Rows` 含
`history user 6`），但**实际写屏**的那一份行序不同 —— 是 presenter 与
`LayoutAppScreen` 计划脱节，不是布局计算错误。风险高于本缺口，建议排在其后。