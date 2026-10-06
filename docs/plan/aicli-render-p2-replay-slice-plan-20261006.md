# P2 replay 切片执行方案（S1–S5）：删 armed 销毁式重放，settle-only 恢复

> 依据：`docs/architecture/aicli-tui-renderer-architecture-design.md` §7.4 第 2 条（replay/settle 族）、
> `docs/plan/aicli-render-p2-recon-20261006.md` §1（三路只读侦察 + 可删性表 + 切片建议）。
> 状态：**S1 已完成（`59ac603d`）**；S2 待启动（本文件随切片推进回填证据与提交锚点）。

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

## 2. S2：删 `ProvenScrollbackEpoch` / `HistoryScrollbackReconciled`

- epoch 推进已内联进 `ReplaceTranscriptAction`（S1）；删除 action、reducer 分支、
  `reconcileScrollback`/`recordProvenScrollbackReplacement`、`ProvenScrollbackEpoch` 字段、
  `HistoryProjectionRecovered` 的延迟消费调用与 executor 的 `HistoryScrollbackReconciled` 回执。
- `TerminalEpoch` 保留（语义化）：plan memo 输入 + stale-callback 栅栏 + 诊断。
- 删除/改写 `history_effect_queue_test.go` reconcile 用例、`scrollback_reconcile_barrier_test.go` 等。

## 3. S3：删 success-mode 背压 + `ScrollbackReset` 结果/计数/3J 写路径

- executor `armRecoveryBackoff` 的 reset 分支（第 3 条）删除；保留 failed 限速与
  "obligation pending + generation 不变" 分支。
- `TerminalTransactionPlan.resetScrollback`、`forceScrollbackReset` 分支、
  `ScrollbackResetCount`/`LastScrollbackResetReason`/`terminalScrollbackResetReason`、
  `TerminalTransactionResult.ScrollbackReset` 与全部 `3J` 写点删除。
- 保留：失败限速（439 arms/0 engages 事故防线）、settle 事务、半写整族复位。

## 4. S4：诊断字段 / debug composer / guard 清理

- `ScrollbackReplayArmed`（state/diagnostics/debug JSON/status 行）、debug composer
  `composeTerminalViewportScrollbackReconciliationPlan`（若 S3 后无引用）、
  `clearScrollbackReplayAuthorization`、相关唤醒列表项删除。

## 5. S5：目标语义用例（新增）

1. 装载后追加交付不重发（旧内容在 scrollback 保留、新内容按序 append）；
2. settle 后从最后已证明行续写无空洞（交付流连续、无重复行）；
3. 语义代后旧 token 的 ack/fail 不可复活（已有用例保留并扩展到装载场景）；
4. 全交互路径 `3J` 计数恒 0（含 /resume、/backtrack、失败恢复）；
5. 真机 e2e：装载路径 marker exactly-once、无清屏闪烁。

## 6. 回滚

每个切片独立提交；回滚 = `git revert <切片提交>`。S1 若发现 settle-only 装载在真机出现
锚点空洞/重复，可整体 revert 回销毁式重放（行为变更集中在 S1 一个提交）。
