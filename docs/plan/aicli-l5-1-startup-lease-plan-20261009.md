# L5-1 启动期租约收编方案（fullscreen/pager/debug raw 退役）

> 性质：**独立执行方案**（由 `aicli-render-l5-candidates-20261009.md` §1 升级）。
> 基线：`feat/render-p0-writer-unification` @ `4f31ac80`（L5-3 三批次收口；写端基线
> 25 条/28 点位 = 受认可 21/24 + 债务 4/4）。
> 上游：候选评估 §1（现状锚点）、`aicli-legacy-fallback-retirement-plan-20261008.md` §4.4/§6、
> `docs/aicli/tui-render-architecture.md`。
> 触发评估（2026-10-09）：候选文档的触发条件（启动期 picker 需纳入统一渲染/租约；或启动期
> 裸写引发实际冲突/需求）**尚无现场证据**；本方案为**提前立项**（用户授权），动机：
> 写端归一的最后一块、收益可直接量化（受认可 24→20）、且不改变产品行为面
> （仅收敛启动选择器的写入通道与生命周期实现）。

## 1. 现状锚点（2026-10-09 逐条复核）

### 1.1 三个 raw 分支（唯一的 lease 缺失路径）

- `ui/fullscreen_list.go:423-455`（`fullScreenListLifecycle.enter/close`）：`!leaseManaged`
  时自写 `\x1b[?1049h`、`\x1b[r`、`\x1b[?25l`、`\x1b[2J`、`\x1b[H` 与退出序列；
- `ui/transcript_pager.go:582-599`（`transcriptPagerLifecycle`）：同型 raw enter/close；
- `ui/debug_overlay.go:110-127`（`debugOverlayLifecycle`）：同型 raw enter/close。

### 1.2 无租约入口仅剩 3 处（启动/登录期）

- `commands/chat.go:1008` `selectProviderFullScreen` → `ui.SelectFullScreenList`（:1025）；
- `commands/chat.go:1202` `selectModelFullScreen` → `ui.SelectFullScreenList`（:1218）；
- `commands/login.go:260` `cliLoginPrompter.PromptSelect` → `ui.SelectFullScreenList`（:278）。
- 调用链：`prepareChatRuntimeState`（`chat_bootstrap.go:157`）早于 shell 引导
  （`bootstrapChatSessionShell`），此时无 surface/presenter/transport；`aicli login`
  为独立命令，全程无 chat 会话。
- 机械核实：`ui.SelectFullScreenList` 的非测试调用者**恰为上述 3 处**；
  `ui.RunTranscriptPager`/`ui.RunDebugOverlay` 的裸版本**不存在**（仅 WithLease 变体）。

### 1.3 写端基线关联（受认可 4 条目/4 点位，`ui/writer_inventory_test.go:105-108`）

- `debug_overlay.go:RunDebugOverlayWithLease`（os.Std* 1）；
- `fullscreen_list.go:SelectFullScreenList`（os.Std* 1）；
- `fullscreen_list.go:SelectFullScreenListWithLease`（os.Std* 1）；
- `transcript_pager.go:RunTranscriptPagerWithLease`（os.Std* 1）。
- 机制：三个 WithLease 入口把 `os.Stdout` 作为 raw writer 回落参数传入
  （`transcript_pager.go:522` 同型）；`SelectFullScreenList` 是裸入口本体。
  扫描器口径 = ui 包内 os.Stdout/os.Stderr 触碰（os.Stdin 不计数，见 README「写端门禁」）。

### 1.4 会话内租约链（已收口，作为本方案的复用底座）

- 租约入口 `ui/screen_lease.go:52`（`AcquireAlternateScreenWait`，≤2s 预算）；
- 租约 transport 接口 `ui/screen_lease.go:148-153`（`AlternateScreenLeaseTransport`：
  Enter/Write/Exit/RequestPrimaryRecovery）；生产实现 = `TerminalSessionPresenter`；
- 会话内 picker 获取模式：`commands/chat_picker_common.go:119/136/152`（WithLease + UI actor
  barrier）；`resumeFullScreenTerminal(session) = session.Layout.Terminal()`（:436）。
- `FixedBottomSurface`：`Enable()`（caps.Interactive+ANSI+ScrollRegion+高度，
  `canEnableLocked` :2880）、`Disable()`（:446，nil 安全、含租约在途 teardown 语义）、
  `postFacadeAction`（:1298，无 uiPoster 时 false 回落同步，**无 actor 时安全**）。

## 2. 设计：启动期租约 transport（D1/D2/D3）

### D1 `ui.StartupAlternateScreenTransport`（terminal-backed，一次性）

- 实现 `AlternateScreenLeaseTransport`：
  - `EnterAlternateScreen`：经 `terminal` 写 `?1049h + \x1b[r + ?25l + 2J + H`（与现 raw 分支同字节）；
  - `WriteAlternateScreen`：写帧字节（与 lease 语义一致）；
  - `ExitAlternateScreen`：写 `?25h + \x1b[r + ?1049l`（与现 raw close 同字节）；
  - `RequestPrimaryRecovery`：no-op（启动期无 primary 保留内容，无 repaint 需求）。
- 字节经 `Terminal` 的既有受认可出口（driver/emitControl）组合，不新增 os.Std* 触碰。

### D2 `ui.RunStartupFullScreenList`（获取 → 渲染 → 释放，fail-closed）

- 流程：`NewFixedBottomSurface(terminal)` → `SetPhysicalWritesEnabled(false)` →
  `SetAlternateScreenLeaseTransport(startupTransport)` → `Enable()` →
  `AcquireAlternateScreenWait(ctx, req, DefaultAlternateScreenWaitBudget)` →
  `SelectFullScreenListWithLease(...)` → `lease.Release(ctx)` → `surface.Disable()`（defer）。
- 失败面（全部回落 numbered，不产生半开副屏）：
  - `CanUseFullScreenList` 不成立（非 ANSI / 非 TTY / 高度不足）；
  - `Enable()` false（caps 不满足 / zellij）；
  - 租约忙（预算耗尽）或 transport 不可用。
- 交接规则：启动 surface 为**一次性对象**（picker 结束后 Release+Disable 丢弃）；
  会话 shell 照常自建 surface/presenter，两者不共享状态；`postFacadeAction` 无 actor
  时安全（§1.4 证据）。

### D3 `leaseManaged` 恒真化（raw 分支与回落参数退役）

- `ui.SelectFullScreenList`（裸入口）**删除**（3 处调用者全部改走 D2）；
- 三个 WithLease 入口去掉 `os.Stdout` 回落参数：lease 缺失/非活跃 → `ErrFullScreenUnavailable`
  （fail-closed），不再回落 raw；
- 三个 lifecycle 删除 `leaseManaged` 字段与 `!leaseManaged` 分支（enter/close 仅保留
  stdin raw 恢复职责）；内部 `selectFullScreenListWithLeaseState` / `runTranscriptPagerWithLease`
  / overlay 同型简化为 lease 必需；测试 seam 改为显式测试 transport。
- 预期：受认可条目 4 条/4 点位退役（24→20），`uiSanctionedConsoleWriterCeiling` 同步下调至 20。

## 3. 分批

### Batch A（ui 层：transport + 恒真化 + 基线摘除）

- 新增 D1/D2；删除 `SelectFullScreenList` 裸入口与三个 lifecycle 的 raw 分支/字段；
  WithLease 入口去掉 os.Stdout 回落参数；`uiSanctionedConsoleWriterCeiling` 24→20
  （writer_inventory 条目摘除）；ui 包测试迁移（裸入口引用改 lease/测试 transport）。

### Batch B（commands 层：三处接线 + 回落测试）

- `selectProviderFullScreen`/`selectModelFullScreen`/`cliLoginPrompter.PromptSelect` 改走
  D2；任何失败保持既有 numbered 回落；
- 回归测试：lease 可用走 fullscreen、不可用走 numbered、Esc 取消语义不变。

### Batch C（验证与回填）

- commands/ui 全量绿 + fixture e2e 8/8 + compat 脚本 6/6 + 单写端 fence；
- 启动 provider/model/login 选择器真机人工项（含 Win7/compat 走 numbered）；
- 回填本文 §6、候选评估 §1 状态、P0 台账 §4/退役方案 §4 状态列。

## 4. 验收（每批适用）

- 门禁：`TestUIInteractiveDirectWriterInventory` 精确匹配；受认可 24→20（ceiling 同步下调，
  债务组不变）；
- ui/commands 全量绿；`TestUnifiedSessionSinglePhysicalWriterFence` PASS；
- 真机：fixture e2e 8/8 保持；compat 脚本 6/6 保持；启动选择器人工项通过；
- 行为面：无 TTY / 无 ANSI / 高度不足 / 租约忙 → numbered（与现状一致）。

## 5. 风险与回滚

| 风险 | 缓解 |
|---|---|
| 启动期 surface 与后续 shell surface 状态串扰 | 一次性对象 + Release+Disable + 无共享字段 |
| 租约 enter/exit 字节与现 raw 分支不一致导致闪烁/残留 | 同字节序列 + fixture e2e 与真机人工复核 |
| `Enable()` 在部分终端失败导致选择器降级为 numbered | 与现状 fallback 一致（fail-closed）；保留诊断输出 |
| 测试依赖裸入口/回落参数 | Batch A 内迁移到 lease 或测试 transport |
| 回滚 | 每批一提交；Batch A 可独立 revert（ui 层）；Batch B 回退三处接线即恢复现状 |

## 6. 执行记录（待填）
