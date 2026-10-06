# aicli 渲染差距收敛实施方案（G1–G12，2026-10-06）

> 性质：**实施方案**（批次 A–D 的切片、测试先行、验收、台账）。本文是
> `docs/architecture/aicli-tui-renderer-architecture-design.md` §7.5（G1–G12 差距扫描）的执行文档。
> 基线：`feat/render-p0-writer-unification` @ `d6315e78`（文档）；代码基线 `d47147b1`（扫描时点）。
> 关联：`aicli-render-p1-state-convergence-plan.md`（P1-3 §3.7 保留决策）、
> `aicli-render-p1-1-step4-planning-incremental-plan.md`、`aicli-render-p2-recon-20261006.md`（切片）、
> `aicli-render-remaining-defect-ledger-20261006.md`（A1–A5 主线）、写端清单门禁
> （`ui/writer_inventory_test.go`、`commands/chat_command_result_test.go`）。
> 硬规则：**迁移一处 → 从 §9 台账划掉一处**；测试先行；一刀一提交；每刀 `gofmt` +
> 目标包门禁 + 相关用例；跨批不得混合提交。

## 0. 摘要

- 覆盖 §7.5 全部 G1–G12；分四批：**A 写端闭合收尾（P0）**、**B 轮询面复核与登记（P1-3）**、
  **C 交付账语义收紧（P2 前置）**、**D 口径与一致性小修**。
- 默认决策对齐：P1-3 §3.7 已把队列等待/有界兜底类 1ms 轮询列为**保留项**；B 批只做
  "复核 + 可选事件化 + 全量登记"，不推倒既有决策。
- 非目标：P2 Slice 1 本体（停铸 active，见 P2 切片计划）、P1-1 Stage 1 编码（见子计划）、
  P3 增量编码；本方案不替代剩余缺陷台账 A1–A5 主线。

## 1. 范围与原则

| 项 | 内容 |
|---|---|
| 覆盖 | §7.5 G1–G12（写端盲区/1ms 轮询/交付账缺口/镜像补登/口径不一致） |
| 交付物 | 门禁增强、逐项收敛提交、保留项登记表、钉测试、文档回填 |
| 原则 | 最小行为反转；先钉测试后改实现；每刀独立可回滚；白名单=迁移债务台账而非授权 |
| 验证基座 | `go test ./cmd/aicli/ui ./cmd/aicli/commands`（含定向 `-race`）+ 真机 e2e 脚本 |

## 2. 批次总览

| 批次 | 覆盖 | 目标 | 依赖 | 相对规模 | 回滚面 |
|---|---|---|---|---|---|
| **A 写端闭合收尾**（P0） | G2/G3/G9/G11 | 门禁增强（递归+盲区）+ 12 项逐项收敛/登记 | 无 | 1–1.5 天 | 每项 1 刀，独立 revert |
| **B 轮询面复核与登记**（P1-3） | G1/G7 | 保留决策清单化 + 可选 `WaitIdleTimeout` 事件化 + 1ms 站点清零或登记 | 无（与 A 并行） | 0.5 天（+0.5 可选） | 单提交 revert |
| **C 交付账语义收紧**（P2 前置） | G4/G5 | claimed×presentation 漂移显式化；skipRows 证明去二义 | 建议 B 后（settle 面不交叉） | 1–1.5 天 | 每刀 1 提交 |
| **D 口径与一致性小修** | G10/G12/G6 残余/G8 残余 | 口径统一、文档注明、镜像映射 | 无 | 0.5 天 | 单提交 revert |

依赖图：`A0 → A1* → A2`；`B0 → B1? → B2`；`C1 → C2 →（P2 Slice 1）`；`D1/D2/D3/D4` 任意时点。
A 与 B 文件面无交集，可并行；C 与 B 的建议顺序仅为避免 settle/轮询同批交叉审查。

## 3. 批次 A：写端闭合收尾（P0）

### A0 门禁增强（测试先行，1 刀）

现状（扫描实证）：

- ui 门禁 `writer_inventory_test.go:143-244`：glob 仅 `uiDir/*.go`（**非递归**），漏 `ui/renderengine` 等子包；
- commands 门禁 `chat_command_result_test.go:1000`（glob :1217，仅 `chat*.go` + `command.go`）；
- 两个扫描器只遍历 `FuncDecl` 体 → 盲区：**包级 var 函数字面量、结构体字面量、非 arg0 的 `os.Std*`**。

步骤：

1. 新增盲区扫描器（AST：`ValueSpec`/`FuncLit`/`CompositeLit`/非 arg0 实参）并先红：
   `ui/writer_inventory_blindspot_test.go` + commands 对应用例；输出新增命中基线；
2. ui 门禁改递归 glob（含 `renderengine`）；commands 门禁扩 glob 与盲区规则；
3. 台账分组更新（新增条目；白名单语义不变）。

验收：`go test ./cmd/aicli/ui ./cmd/aicli/commands -run 'DirectWriterInventory|WriterInventory'` 绿；
基线包含 G2 全部新命中。回滚：测试文件独立提交，可单 revert。

### A1 逐项收敛（每项 1 刀）

| # | 位置 | 处置 | 钉测试/证据 | 风险 |
|---|---|---|---|---|
| 1 | `ui/renderengine/terminal_lock.go:72-76`（syncFrames DEC2026 直写 os.Stdout） | 首选**登记 + 冻结**（legacy-only；unified 不开启，随 legacy surface 退役删除）；二选注入 sink/经 session 事务 | 递归门禁命中基线；`syncFramesEnabled` 唯一开启点核对 | 低（legacy 面） |
| 2 | `ui/terminal_output.go:18-21`（包级 var os.Stdout） | 保持兼容 sink 语义；**修正注释**（删除/改写 `SetLegacyBinding` 声明，标注"legacy 适配器退役后删除"）+ 登记 | 盲区扫描命中；注释不再声称不存在的实现 | 低 |
| 3 | `commands/chat_notification.go:613`、`chat_notification_sound.go:170`（`raw: os.Stdout` 结构体字面量） | 改经 **session 旁路接口**（S14）；submit 失败路径 OSC/BEL 不得绕过 | 新钉测试：失败路径经旁路记录、stdout 零字节 | 中（旁路接口只增不改） |
| 4 | `commands/chat_legacy_console_editor_windows.go:60-66`（包级闭包写 stderr） | 收编 `NotifyChatDiagnostic` 或函数内化并登记 | 盲区扫描命中消失/入台账 | 低 |
| 5 | `commands/chat_tool_executor.go:95,117`（`io.Writer(os.Stdout)` 转换/参数） | 显式 allowlist 登记（unified 已 `io.Discard`，legacy 直写） | 台账条目 | 低 |
| 6 | `commands/chat_setup.go:108/139/233/268` + `printChatSessionInfoRow/Line` 调用点、`chat_selection_output.go:129`、`chat.go:1073`（G3 stderr 边缘） | 交互期统一 `NotifyChatDiagnostic`/会话 writer；非交互保留 | stderr 路由测试；交互期 stdout/stderr 零字节 | 中（面广，逐文件小刀） |
| 7 | `ui/statusbar.go:197-252`（G9 legacy `StatusBar.Render` 直写） | 加物理栅栏（`physicalWritesEnabled`）或标记 fenced-dead 并从 `layout.go:58` 构造面移除 | unified 会话零字节断言 | 低-中（生产已不可达） |
| 8 | `commands/chat_command_text_writer.go:48`、`chat_ui_actor.go:149/281`、`exec_event_processor.go:190`、`chat_pipe_console_line.go`、`chat_profile_lifecycle_ops.go:274`、`chat_model_command.go:558…`/`chat_model_switch.go:392…` | 分类：**登记**（白名单/test-only/子进程 stdio/非 TUI exec）或**收敛**（裸 ANSI 路径） | 台账条目 + 逐项注释 | 低 |

> 说明：第 8 行为扫描报告"新增缺口"中不涉及交互期主链的余项；处置以"可审计"为目标，
> 不为了零命中而改变非 TUI 语义（pipe/exec/子进程保留并登记）。

### A2 验收（批次收口）

- 单写端断言扩展：`TestUnifiedSessionSinglePhysicalWriterFence` 驱动控制序列/直写输出/命令输出/诊断，
  断言全部落在同一物理 writer、进程 stdout/stderr 零字节；
- 递归 + 盲区门禁 0 越界（白名单外）；
- 真机 e2e：`scripts/test-aicli-windows-terminal-e2e.ps1`；
- 回滚：逐项独立提交；接口变更仅限新增旁路方法。

## 4. 批次 B：轮询面复核与登记（P1-3 收尾）

> 背景：P1-3 §3.7 已将残余 1ms 轮询分为"**已消除**"（actor idle 路径：controller waiter /
> executor 票据栅栏 / bridge 三态 ack / 两个 dispatch flusher）与"**保留**"（队列等待或有界超时兜底）。
> 本批把保留决策清单化，并给出可选事件化路径；不推倒既有决策。

### B0 保留项登记表（文档 + 注释，1 刀）

产出统一表格（回填设计文档 §7.5 G1/G7）：位置、形态、理由、触发条件、诊断计数、复核期限。

| 站点 | 形态 | 分类 | 依据 |
|---|---|---|---|
| `controller.go:852`（`WaitIdleTimeout` 内部） | 1ms sleep | 有界超时兜底（保留） | 经 close drain（`chat_ui_actor.go:1014`）、legacy 有界辅助（`chat_runtime_events.go:4787`、`chat_surface_output.go:538`）、`waitUIActorIdleBounded` 5s（生产 7 处）触达 |
| `chat_runtime_events.go:1981/2003/2597` | 1ms | 队列等待（保留） | backlog/deferred/字节预算等待，P1-3 §3.7 |
| `chat_runtime_events.go:3030` | 1ms | 防御回退（保留） | 无 accepted ticket 的 mailbox 满防御；补覆盖测试 |
| `chat_runtime_events.go:1840` | 5ms | backlog worker 重试（保留） | 单点重试循环 |
| `terminal_session_executor.go:1037` | 10ms | 同代 backoff 让出（保留） | 防紧循环，随 P2 backoff 删除 |
| `screen_lease.go:82-89` | 10ms | lease 等待（保留） | alternate screen 互斥 |
| 平台轮询（Windows ESC/剪贴板/overlay 等） | 20–100ms | 平台 I/O（保留） | 平台限定，登记即可 |

### B1 `WaitIdleTimeout` 事件化（可选，1 刀，测试先行）

- 决策点：**默认保留**；若执行：复用 `appliedTicket/visibleTicket` 水位 + waiter 注册
  （`WaitActionApplied` 机制），超时用单次 `time.Timer`，删除内部 1ms sleep；语义不变
  （effect 派发计入未空闲；批交付含 `delivering=false`）。
- 钉测试：`TestWaitIdleTimeoutEventDriven`（无 Sleep、水位唤醒、超时、close-drain 竞态、
  waiter 自摘除不丢成功）；既有 `TestWaitControllerAcceptedAppliedWaitsForPriorAccepts` 不回归。
- 回滚：单提交 revert。

### B2 残余 1ms 站点清零核对

- `chat_runtime_events.go:3030` 防御回退：补 accepted-ticket 路径覆盖测试；无 ticket 场景保留并注释。
- 验收：生产代码 1ms `time.Sleep` 命中数 = 0（或仅 §B0 表内条目，且带注释与测试）。

## 5. 批次 C：交付账语义收紧（P2 前置）

### C1 claimed × presentation 漂移（G4）

现状（扫描实证）：

- `rebasePendingHistoryEffects`（`history_effect_planner.go:1640-1662`）：只对
  `Queued 且 identity 失效` 的 token 立即 invalidate；**claimed（`WriteCursor` 指向）且 identity 仍有效、
  presentation 改变**的 token 既不 rebase 也不即时 invalidate；
- `rebasePending` 显式跳过 `WriteCursor`（`history_effect_queue.go:560-567`）；
- 收敛路径：generation 失配 → executor Deferred（`terminal_session.go:946`、executor :1190-1191）→
  `deferInFlight` 释放游标 → 再 rebase 收敛。

步骤（最小行为反转）：

1. 钉测试复现全链（resize/theme/defer × claimed），先绿钉**现状收敛**；
2. 显式化：对 claimed 且载荷/世代不一致的 token 增加**显式 Deferred 触发**（或"诊断计数 + 注释"，
   视测试面选择最小反转——若显式 Deferred 破坏既有用例则退为计数 + 注释）；
3. 反例测试：claim 后载荷不得被替换（S6→S7→S8 不变式）。

验收：resize/theme/defer 场景无"静默跳过"（或显式计数）；`history_resume_full_coverage_test.go`、
`history_planning_budget_test.go` 绿。

### C2 skipRows 二义性（G5）

现状：`activeAckedRenderedPrefixRows`（`history_effect_planner.go:335-367`）返回 0 二义
（"无已交付前缀"与"前缀行不等价"）；finalize 由 `finalizedActiveCorrectionTouchesAckedPrefix`
置 `ProjectionUnknown` 兜底（`controller_state.go:594-597`）。

步骤：返回值改 `(rows int, proved bool)`（或显式 unknown 标记）；调用点（:218-230）区分：
无前缀 → `skipRows=0` 正常；前缀不等价 → 保持当前兜底并新增测试钉住。回归
`history_planning_budget_test.go` / resume 全量。

### C3 与 P2 主线衔接

C1/C2 完成后启动 **P2 Slice 1（停铸 active）**——见 `aicli-render-p2-recon-20261006.md` §3
（两刀制：最小行为反转 + 死代码保留 + 回滚面控制）；本方案不重复其内容。

## 6. 批次 D：口径与一致性小修

| # | 项 | 处置 | 钉测试/验收 |
|---|---|---|---|
| D1 | G12：`app_layout.StatusRows`（nil 时 0）与 row plan 恒预留 1 行口径不一（`app_layout.go:110` vs `bottom_pane_row_plan.go:121`） | 统一口径（推荐：物理行恒预留 1 行，`app_layout` 对齐；或反向收敛并注明） | 钉测试：nil 状态仍恒 1 行 |
| D2 | G10：web/TUI statusbar 段集合不一致（web 缺 state/goal/model/provider/fast；goal/fast 未说明） | **先文档注明**（goal/fast 的有意性）；web 侧对齐另评 | 文档回填 §10.1；web 用例不回归 |
| D3 | G6 残余映射：M4 legacy `FixedBottomSurface.lastWidth/lastHeight`；M5 viewport 双表示；M1/M2/M3/M7/M8/M9 镜像组；M6 terminalEpoch 双份 | M4 并入 A 的 fenced-dead；M5 随 P1-2b 残余小刀；其余随 P1/P2 字段清理；M6 随 P2 `TerminalEpoch` 语义化 | 每项登记 §4 矩阵状态更新 |
| D4 | G8 残余：§10 补登项（编辑器状态行/队列指示/band 顶距/fullscreen 族/主链 defer） | 对照补测试（窄屏/popup 覆盖/resize） | 相关区域测试绿 |

## 7. 顺序与规模

```
A0 ──► A1(逐项 1..8) ──► A2            （P0 收口，先门禁后台账）
B0 ──► B1?(可选) ──► B2                （与 A 并行）
D1/D2/D4（任意时点）        D3 随 A/P1/P2
C1 ──► C2 ──► P2 Slice 1 ──►（P2 其余切片）
```

| 批次 | 相对规模 | 提交数（估） | 关键前置 |
|---|---|---|---|
| A | 1–1.5 天 | 3–10 | A0 门禁先落地 |
| B | 0.5 天（B1 +0.5） | 1–3 | 无 |
| C | 1–1.5 天 | 2–4 | 建议 B 后（settle 面不交叉） |
| D | 0.5 天 | 2–4 | 无 |

## 8. 验收总表

| 批次 | 关键验收 | 命令（示例） | 风险 |
|---|---|---|---|
| A | 递归+盲区门禁 0 越界；单写端断言；真机 e2e | `go test ./cmd/aicli/ui ./cmd/aicli/commands -run 'DirectWriterInventory|WriterInventory|SinglePhysicalWriter'`；`scripts/test-aicli-windows-terminal-e2e.ps1` | 中（stderr 面广，逐文件小刀） |
| B | 1ms 站点清零或全部登记；事件化语义不变 | `go test ./cmd/aicli/ui ./cmd/aicli/commands -run 'WaitController|WaitIdle'`（含 `-race` 定向） | 低 |
| C | claimed 漂移显式化；skipRows 去二义；resume/budget 用例绿 | `go test ./cmd/aicli/ui -run 'History|Resume|PlanningBudget|Transcript'` | 中（交付账语义面） |
| D | 口径统一/文档注明；区域测试绿 | `go test ./cmd/aicli/ui -run 'Layout|Region|Status'` | 低 |

## 9. 实施台账（每刀更新）

| ID | 项 | 状态 | 提交 | 备注 |
|---|---|---|---|---|
| A0 | 门禁增强（递归 + 盲区扫描器） | pending | — | 先红后绿；基线更新 |
| A1-1 | renderengine/terminal_lock.go 直写 | pending | — | 登记+冻结（首选） |
| A1-2 | terminal_output.go 包级 var + 注释漂移（G11） | pending | — | 注释修正 + 登记 |
| A1-3 | notification OSC/BEL 旁路化 | pending | — | 失败路径测试 |
| A1-4 | legacy console editor stderr 闭包 | pending | — | 收编或登记 |
| A1-5 | tool_executor stdout 转换登记 | pending | — | allowlist |
| A1-6 | stderr 边缘收口（G3） | pending | — | chat_setup 等 |
| A1-7 | legacy StatusBar 栅栏（G9） | pending | — | 或 fenced-dead |
| A1-8 | 余项分类登记/收敛 | pending | — | 见 A1 表 #8 |
| A2 | 单写端断言扩展 + e2e | pending | — | 批次收口 |
| B0 | 保留项登记表回填 §7.5 | pending | — | 清单化 |
| B1 | WaitIdleTimeout 事件化（可选） | pending | — | 默认保留 |
| B2 | 1ms 站点清零核对 | pending | — | 仅剩表内条目 |
| C1 | claimed×presentation 显式化 | pending | — | 先绿钉现状 |
| C2 | skipRows 去二义 | pending | — | `(rows, proved)` |
| D1 | StatusRows 口径统一 | pending | — | G12 |
| D2 | web/TUI statusbar 注明 | pending | — | G10 |
| D3 | G6 残余映射登记 | pending | — | 随 P1/P2 |
| D4 | §10 补登项测试对照 | pending | — | G8 残余 |

## 10. 附录：扫描出处与关联证据

- 差距扫描：设计文档 §7.5（G1–G12）；四路报告要点——
  状态矩阵 14 项核验 + 9 组未登记镜像（M1–M9）+ 生产轮询清单（P1–P11）；
  写端 12 项新增缺口；handoff 逐条判定（G4/G5）；区域模型 8 类差异（G8/G10/G12）。
- 保留决策：P1 收敛计划 §3.7（1ms 轮询消除核查）与 §3.3（有界超时保留清单）。
- 写端门禁：`ui/writer_inventory_test.go`（"migration-debt ledger, not an authorization"）；
  `commands/chat_command_result_test.go:1000`。
- P2 切片：`aicli-render-p2-recon-20261006.md` §1–§3；P1-1 子计划 §1.6。
