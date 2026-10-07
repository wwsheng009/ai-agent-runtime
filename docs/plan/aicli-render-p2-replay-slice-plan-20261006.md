# P2 replay 切片执行方案（S1–S5）：删 armed 销毁式重放，settle-only 恢复

> 依据：`docs/architecture/aicli-tui-renderer-architecture-design.md` §7.4 第 2 条（replay/settle 族）、
> `docs/plan/aicli-render-p2-recon-20261006.md` §1（三路只读侦察 + 可删性表 + 切片建议）。
> 状态：**S1–S5 全部完成（`59ac603d`/`9a205572`/`9e7383c1`/`7ade9732`/`473da400`）**。本切片收尾：目标语义用例与真机 e2e 装载/追加阶段均已落地。

## 0. 目标语义与行为变更（产品口径）

目标（审计 §6 P2）：可见窗口 W 行 + 溢出按行序 append 进 native scrollback；任何 mutable 内容
不得提前入 scrollback；resize 只重画窗口；**不承诺 scrollback 可改写**。

由此产生的用户可见行为变更（已由设计文档确认，随本切片落地）：

1. `/resume`、`/load`、启动恢复、`/backtrack`：**不再清屏（`\x1b[3J`）重放**。装载的转录内容
   以 append 方式进入 scrollback（在已有内容之后），可见窗口由帧计划重画。
2. backtrack 改历史后，已进入 scrollback 的旧行**物理保留**（append-only 修正：新版本追加，
   旧版本不擦除）。
3. `settle` 族保持原语义（原地隔离未证明区间、永不重发、从最后已证明行续写），成为唯一恢复路径。
4. `TerminalEpoch` 语义降级为**装载代**（stale-callback 栅栏），不再代表物理清屏代。

## 1. S1：生产可达销毁计划 → 恒 settle（本切片第一步）

### 1.1 设计（Design B：装载只重证明；ledger 保持权威）

> 设计修正（实施中发现）：曾考虑"装载换 ledger/推进语义 epoch"（Design A）。**否决**：
> native scrollback 是 append-only，换 ledger 会让同一身份的已交付内容重新可铸 →
> 在 scrollback 里重复追加（违反 exactly-once）。装载必须保留交付记录做去重。

- **executor**（`terminal_session_executor.go` `terminalHistoryRecoveryPlan`）：删除
  `snapshot.scrollbackReplayArmed` 分支，恢复计划恒为
  `composeTerminalViewportTransactionPlan + SettleHistoryProjection=true`。销毁式 composer
  `composeTerminalViewportScrollbackReconciliationPlan` 保留（debug/测试面，S4 清理）。
- **reducer**（`controller_state.go` `ReplaceTranscriptAction` 的 `a.ArmScrollbackReplay` 分支）：
  只做 `invalidateTranscriptPlanMemo()`（从源重证明，覆盖 no-op 安装）。**不**arm、**不**推进
  epoch、**不**换 ledger、**不**强制 ProjectionUnknown。
- **settle 触发**：替换若丢弃/修改了已交付 cell，既有 `transcriptReplacementInvalidatesAckedHistory`
  路径照常置 `ProjectionUnknown + ReconciliationRequired` → 一次 settle（重画视口、原地隔离
  未决、续接 handoff）。完全不相交的装载（如新终端启动恢复）无需 settle，内容按普通
  handoff 顺序进入 scrollback。
- **append-only 去重**：`hasTerminalRecordForSource` 以（cell, revision, source range, fragment）
  为身份 —— 同身份内容（重复装载同一修订）绝不重铸；新身份（新修订/新 cell）追加。
- **旧 token 栅栏**：in-flight 记录保留；settle 把未决条目隔离（quarantined）后，迟到
  ack/fail 不可复活；已交付记录保持权威（其物理字节已落 scrollback，不得重发）。
- **物理面不变**：settle 事务不写 `3J`、不 bump 物理 reset 计数；`terminal_session.go` 的
  settle 分支保持"保留 tail 锚点"语义。装载路径 `ScrollbackResetCount` 增量恒 0。

### 1.2 测试改写（先红后绿）

- ui：
  - 新增 `transcript_load_reproof_test.go`：装载不 arm/不推进 epoch/不退休 ledger；
    失效替换 → settle 义务；no-op 安装重证明；`historyPendingCount` helper 迁入。
  - `scrollback_replay_executor_test.go`：改写为"装载期间在途交付不被扰动、全程无 3J、
    在途记录保留、装载内容按序到达"。
  - `scrollback_replay_resume_blank_test.go`：改写为 append-only 三阶段（正常交付 /
    同修订重复装载零重发 / 新修订只追加），断言 `3J` 恒 0、epoch 恒 0。
  - 删除 `scrollback_replay_grant_test.go`、`scrollback_reconcile_barrier_test.go`
    （授权族/物理屏障的生产路径已不存在；`reconcileScrollback` 单测保留在
    `history_effect_queue_test.go`）。
  - `controller_state_resize_replan_test.go`：precondition 改为"装载不得 arm"。
- commands：
  - `chat_scrollback_replay_grant_test.go`：装载不 arm/不推进 epoch；canonical seed
    只对新增 unit 重证明（同历史重放不重规划、新 unit 必须被规划）。
  - `chat_resume_history_geometry_test.go`：precondition 改为"装载不得 arm"。
  - `chat_history_reconcile_test.go`：`\x1b[3J` 期望 1/1/2 → 全 0（append-only）。

### 1.3 验收（已达成，`59ac603d`）

- `go test ./cmd/aicli/ui -count=1` → **ok 104.5s**；`./cmd/aicli/commands -count=1` → **ok 167.8s**；
- `go vet` 双包干净；`go build ./...` 干净；gofmt 干净；
- 真机 e2e（`scripts/test-aicli-windows-terminal-e2e.ps1`）**PASS**：72 行 history
  exactly-once、增量归档尾部推进、prompt/status 各一次、Markdown 无源语法；
- 新负断言（ui `transcript_load_reproof_test.go` / `scrollback_replay_resume_blank_test.go`）：
  装载路径 `\x1b[3J` 与 `ScrollbackResetCount` 恒 0、语义 epoch 不因装载推进、同修订重复
  装载零重发、新修订只追加；
- 净删 175 行（15 文件，+328/−503；含 2 个旧授权族测试文件删除）。

## 2. S2：删 `ProvenScrollbackEpoch` / `HistoryScrollbackReconciled`（已完成，`9a205572`）

- 删除 `HistoryScrollbackReconciled` action（含 class/wake 名单/actionClassString）、reducer
  分支、executor 回执；删除 `reconcileScrollback`/`recordProvenScrollbackReplacement`/
  `ProvenScrollbackEpoch` 字段、`HistoryProjectionRecovered` 的延迟消费调用；
  删除已无调用方的 `resetActiveHistoryProgressForTerminalEpoch`、`armScrollbackReplay`、
  `clearScrollbackReplayAuthorization`。
- `TerminalEpoch` 保留（当前无生产推进点；plan memo 输入 + 诊断），S5 再评估去留。
- 测试：删除 2 个 reconcile 单测；`TestHistoryEffectsReducer_ScrollbackReconciliation...`
  改写为 `TestHistoryEffectsReducer_FailedHandoffSettlesWithoutRetiringLedger`（settle 原地
  隔离 + 旧回调不可复活 + epoch 不推进）；`history_planning_budget_test.go` 白盒改
  `NewHistoryCommitLedger + invalidateTranscriptPlanMemo`；action 表/唤醒测试同步。
- 验收（已达成）：`go test ./cmd/aicli/ui` **ok 113.7s**；`./cmd/aicli/commands`
  **ok 172.1s**；`go vet`/`go build ./...`/gofmt 干净；真机 e2e PASS（72 行 exactly-once）。
  净删 212 行（13 文件，+64/−276）。

## 3. S3：删 success-mode 背压 + `ScrollbackReset` 结果/计数/3J 写路径（已完成，`9e7383c1`）

- executor `armRecoveryBackoff` 的 reset 分支（第 3 条）删除；保留 failed 限速与
  "obligation pending + generation 不变" 分支（case 2 的 retry window/budget 语义保留，
  作为非收敛 obligation 的速率上限；命名清理归 S4）。
- 删除：`TerminalTransactionPlan.resetScrollback`、`ComposeScrollbackReconciliationPlanForDebug`、
  `composeTerminalViewportScrollbackReconciliationPlan`、`forceScrollbackReset` 全部分支、
  `ScrollbackResetCount`/`LastScrollbackResetReason`/`terminalScrollbackResetReason`/
  `terminalResetScrollbackANSI`（`\x1b[r\x1b[0m\x1b[H\x1b[2J\x1b[3J\x1b[H` 唯一生产写点）、
  `TerminalTransactionResult.ScrollbackReset`、executor diag 的 `ScrollbackReset`/
  `ScrollbackResetsInWindow`、debug HTTP/document 对应字段。
- 保留：失败限速、settle 事务、半写整族复位、`TerminalEpoch` 语义栅栏（无生产写者）。
- 验收（已达成）：`go test ./cmd/aicli/ui` **ok 115.3s**；`./cmd/aicli/commands`
  **ok 177.9s（3395 pass）**；`go vet`/`go build ./...`/gofmt 干净；真机 e2e PASS
  （72 行 exactly-once）。净删 285 行（17 文件，+84/−369）。

## 4. S4：诊断字段 / debug composer / guard 清理（已完成，`7ade9732`）

- `ScrollbackReplayArmed` 全表面删除：`HistoryEffectQueueState`/`HistoryEffectDiagnostics`
  字段、`terminalSessionScheduleSnapshot`/`terminalSessionControllerSnapshot` 镜像、
  `/debug/chat/status` JSON（`scrollback_replay_armed`）、debug document
  `scrollback-replay-armed` 读数、`chat_resume_progress` 的武装门。
- `ReplaceTranscriptAction.ArmScrollbackReplay` **保留**：它是装载替换的"从源重证明"
  标记（触发 memo 失效 + re-proof），与物理重放无关；注释已按 append-only 语义改写。
- `scrollbackReset*` → `recoveryBackoff*` 命名清理：`terminalRecoveryBackoff[Yield]`/
  `terminalRecoveryRetryWindow`/`terminalRecoveryMaxRetries`/`recoveryBackoffActive`/
  `recoveryBackoffSuccessMode`/`recordRecoveryBackoff`/`lastRecovery*`；guard 语义改写为
  "限速非收敛的 source-backed recovery 重证"，"reset+replay" 时代注释全量重写。
- 验收（已达成）：`go test ./cmd/aicli/ui` **ok 117.7s**；`./cmd/aicli/commands`
  **ok 181.5s**；`go vet`/`go build ./...`/gofmt 干净；真机 e2e PASS（72 行 exactly-once）。
  净删 96 行（22 文件，+183/−279）。

## 5. S5：目标语义用例（已完成，`473da400`）

新增 `history_append_only_semantics_test.go`：

1. `TestSessionLoadThenAppendDeliversOnlyNewRows`：装载替换（ArmScrollbackReplay）不重发
   已交付前缀，随后普通追加按序交付新行；逐行 exactly-once、物理顺序单调、无 `3J`、
   epoch 不推进、无恢复义务。
2. `TestSettleAfterPartialWriteContinuesRemainingRowsExactlyOnce`：首笔短写失败 → settle
   原地隔离（部分写入行最多一次），其余行按序恰好一次续写；settle 后同源不再重铸。
3. `TestHistoryEffectsReducer_LoadAfterFailedHandoffCannotResurrectOldToken`：在途交付期间
   装载只重证明（不换 ledger、不推进 epoch、不重铸同源 token）；随后旧 token 的
   fail/settle/迟到 ack 均不可复活它；settle 后再次装载亦不重铸。

新增 `scrollback_clear_fence_test.go`（S5-4 源码栅栏）：

- 递归扫描 `ui` 树 + `commands` 树的生产 `.go`，禁止任何 `[3J` 字节序列回归
  （`/resume`、`/backtrack`、失败恢复等全部交互路径在 S3 后已无物理清屏写点）。

真机 e2e（S5-5）：

- 夹具 `aicli-render-fixture` 新增两阶段：装载替换（同 Scene + 装载标记）与装载后追加
  （第 073 行）；`TerminalSession` 写出经 3J 计数包装，末尾打印 `AICLI-E2E-CLEAR-3J=<n>`。
- 脚本断言：73 行历史 + markdown + prompt/status 全部 exactly-once；装载与追加后
  最老/最新行仍可达；`AICLI-E2E-CLEAR-3J=0`。
- 结果：**PASS**（"session-load replay and post-load append delivered without CSI 3J
  scrollback clear"）。

验收（已达成）：`go test ./cmd/aicli/ui` **ok 117.2s**；`./cmd/aicli/commands`
**ok 175.3s**；`go vet`/`go build ./...`/gofmt 干净；真机 e2e PASS。新增 403 行（4 文件）。

## 6. 回滚

每个切片独立提交；回滚 = `git revert <切片提交>`。S1 若发现 settle-only 装载在真机出现
锚点空洞/重复，可整体 revert 回销毁式重放（行为变更集中在 S1 一个提交）。
