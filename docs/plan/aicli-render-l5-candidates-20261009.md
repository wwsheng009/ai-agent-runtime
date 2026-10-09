# L5 候选与跟踪项立项评估（render 写端归一后续）

> 性质：**立项评估**，不是执行方案。登记三项 L5 候选与一项跟踪项的现状锚点、
> 前置、完成判据、触发条件与规模；任何一项在触发条件满足后，从本文升级为
> 独立完整方案文档（一项目一文档）。
> 基线：`feat/render-p0-writer-unification` @ `7a73df0f`（L4 完成；写端基线
> 25 条/28 点位 = 受认可 21/24 + 债务 4/4；主链 D0→L1→L2→L3-1→L3-2→L3-3→L4 全部完成）。
> 上游：`docs/plan/aicli-legacy-fallback-retirement-plan-20261008.md` §4.4/§5、
> `docs/plan/aicli-render-p0-writer-unification-ledger.md` §4、
> `docs/architecture/aicli-tui-renderer-architecture-design.md` §7.5、
> `docs/aicli/tui-render-architecture.md`。
> 口径：现状锚点为 2026-10-09 逐条复核；行号随代码漂移，执行时以符号名 + `rg` 复核为准。
> 状态：**未排期、未开工**；本文不改变任何生产行为、门禁与基线。

## 0. 触发式立项原则

- 四项均无排期；仅在触发条件（下表）满足时开工。
- 开工前先升级为独立方案文档：现状锚点 → 分批切片 → 验收标准 → 风险与回滚
  （沿用本仓库惯例，形态参考退役方案 §5 的各批记录）。
- 每项完成判据统一包含：目标包全量绿（`./cmd/aicli/ui`、`./cmd/aicli/commands`）+
  写端基线只降不升（两组 ceiling 只降）+ 相关真机场景 PASS（含 compat 场景，退役方案 §6）。

| 项 | 一句话 | 触发条件（满足其一即立项） | 规模 |
|---|---|---|---|
| L5-1 | 启动期租约 → fullscreen/pager/debug raw 收编（**已收口**：Batch A/B/C；写端受认可 24→20） | 启动期 picker 需纳入统一渲染/租约；或启动期裸写引发实际冲突/需求 | 中（约 3–6 提交） |
| L5-2 | presenter popup/几何 API → surface facade 读退役（**已收口**：Batch A/B/C + L5-2c 邻近族；两族零直读门禁 + fixture 8/8 + compat 6/6） | 需要统一 popup/几何能力提升；或继续瘦身 surface 排期 | 中大（设计 + 分批迁移） |
| L5-3 | legacy 命令处理器 → CommandResult 批量迁移（已执行：Batch A/B/C 完成，三批次收口） | 排期「命令通道收口」批次（收益最直接：删硬门禁） | 大（逐命令小刀） |
| T-1 | session 侧 DEC 2026 同步帧包裹 | 真机 tearing 证据（唯一门槛） | 小（约 1–2 提交 + 真机验证） |

## 1. L5-1 启动期租约 → fullscreen/pager/debug raw 收编

**现状锚点（2026-10-09 复核）**

- raw 分支 = 三个 lifecycle 的 `!leaseManaged` 兜底：`ui/fullscreen_list.go:423-455`
  （`fullScreenListLifecycle.enter/close`）、`ui/transcript_pager.go:582-599`
  （`transcriptPagerLifecycle`）、`ui/debug_overlay.go:110-127`（`debugOverlayLifecycle`）；
  lease 已管理时 `enter/close` 直接 no-op，不写任何序列。
- 会话内 picker 已全部走租约：`commands/chat_screen_framework.go:720/724`、
  `commands/chat_picker_common.go:119/136/152`；租约入口 `ui/screen_lease.go:52`
  （`AcquireAlternateScreenWait`，默认 ≤2s 等待预算）。
- 无租约入口仅剩 3 处（启动/登录期，surface 未 enable 或 transport 未建立）：
  `commands/chat.go:1025`（选择 Provider）、`commands/chat.go:1218`（选择 Model）、
  `commands/login.go:278`（登录账号/凭据选择）。
- 写端基线关联（受认可组 4 条目/4 点位）：`debug_overlay.go RunDebugOverlayWithLease`、
  `fullscreen_list.go SelectFullScreenList`、`fullscreen_list.go SelectFullScreenListWithLease`、
  `transcript_pager.go RunTranscriptPagerWithLease`。

**目标**：给 3 处接租约 transport（或把 surface enable/transport 建立提前到启动选择之前），
使 `leaseManaged` 恒真；删除三个 `!leaseManaged` raw 分支。

**完成判据**

- 三个 lifecycle 不存在裸写路径；3 处启动/登录入口全部 lease-managed；
- 写端基线预期退役上述 4 条目/4 点位（受认可 24→20；执行时以扫描器实测为准）；
- ui 全量绿 + 真机全屏/分页/overlay 行为不变（含取消/Esc 恢复路径）。

**前置**：**启动期租约设计**——presenter attach 之前的 transport 语义（谁持有、何时交接、
fail-closed 规则）；这是本项唯一的真正设计工作。

**风险**：启动期与 unified presenter 的租约交接时序（重复进入/退出副屏、恢复序列丢失）；
`beginChatInputWait` 等启动看门狗语义需保持。

**规模**：中等（约 3–6 提交：设计 1 + 三处接线/清理 + 测试迁移与真机）。

**执行记录（2026-10-09）**：已升级为独立方案文档
[aicli-l5-1-startup-lease-plan-20261009.md](aicli-l5-1-startup-lease-plan-20261009.md)；
触发条件尚未满足，经用户授权**提前立项**（动机：写端基线受认可 24→20、启动期零裸写收尾）。
Batch A/B/C 已收口（`dec13b68`/`3dca6215`）：D1/D2/D3 落地、三处启动选择器接线、
裸入口与 raw 分支退役、fail-closed 回归测试；验收 = ui 门禁/全量绿 + commands 全量
（-skip 并发 web 在途用例）176.4s 绿 + fixture 真机 8/8 + compat 6/6。

## 2. L5-2 presenter popup/几何 API → surface facade 读退役

**现状锚点（2026-10-09 复核）**

- 点名两族读：popup 输入 `commands/chat_surface_output.go:239/311`
  （`Surface.BeginPopupInputForOwnerWithViewport`）；几何 `commands/chat_interaction.go:7402-7420`
  （`Surface.SyncTerminalGeometry` 等）。
- facade 调用面总量（commands 非测试文件 `.Surface.*` 机械统计）：**122 处 / 29 个方法**
  （含 popup、lease、几何、output 各族）——分批迁移的总盘子。
- `FixedBottomSurface` 现为「语义 facade + 几何/lease 桥」（约 40 个生产方法、700–900 行承重，
  见 `docs/aicli/tui-render-architecture.md`）。

**目标**：presenter 侧补齐 popup 输入所有权与几何同步 API → 迁移调用方 → 删除 surface facade
**读**方法；facade 进一步瘦身。

**完成判据**

- popup/几何两族零 `.Surface.` 直读（先这两族，再按「读 vs 写」分类推进其余）；
- unified 弹层/软重排行为等价（含底部 prompt 行所有权契约不回退——L3-3 已收敛过的边界）；
- ui/commands 全量绿。

**前置**：**presenter API 设计**（popup 所有权语义、几何事件来源与节流契约）。

**风险**：popup 与底部 prompt 行的所有权交界（merged composer / body-only popup 两形态）；
几何探测节流（`DefaultGeometryProbeMinInterval`）语义不得回退。

**规模**：中大（设计 + 分批迁移；建议先 popup+几何两族小刀，再逐步收其余）。

**执行记录（2026-10-09）**：已升级为独立方案文档
[aicli-l5-2-presenter-popup-geometry-plan-20261009.md](aicli-l5-2-presenter-popup-geometry-plan-20261009.md)；
经用户「继续」指令**提前立项**（设计先行）；Batch A（几何族：D2 门面 + 3 处迁移）启动执行。
**执行记录（2026-10-09 续）**：Batch A（`492f3f86`/`8e85961c`）与 Batch B（`f2d1e6f0`/`93089990`）
均完成（两族零直读机械门禁 + 隔离/集成验证）；Batch C（全量收口 + 回填）待执行。
**执行记录（2026-10-09 收口）**：Batch C 验证四件套通过——ui 全量 14.3s / commands 无 skip 全量
178.4s（exit 0）/ fixture 真机 e2e 8/8 / compat 6/6；**L5-2 三批次收口**。
**执行记录（2026-10-09 邻近族收口）**：L5-2c 立项并单批收口（prompt-editor 门面
`ui.PromptEditorPort` + composer 5 点迁移 + 冻结白名单 5→0；`16698f11`）。

## 3. L5-3 legacy 命令处理器 → CommandResult 批量迁移（删硬门禁）

**现状锚点（2026-10-09 复核）**

- 硬门禁三件套：
  - `commands/chat_unified_command_gate.go:18-50`：`dispatchUnmigratedUnifiedChatCommand`
    （default 拒绝：「尚未迁移到统一渲染命令通道，已在 interactive TTY 中禁用」）+
    `rejectUnmigratedUnifiedChatCommand`（非分派入口保护）；
  - `commands/command.go:45-51`：unified 下不允许回落 `handleCommand`；
  - `commands/chat_command_result.go:880-901`：`unifiedInteractiveLegacyCommandFence`
    （/backtrack、/rewind、/resume 残余变体）+ `commands/command_invoke.go:30/122`（/call、/skill）。
- 已迁移面（机械统计）：`chat_command_result.go` 中 `commandMatches(cmdLower, …)` 认领的
  命令名 **63 个（含别名）**；门禁自身只保留 `/exit`（「再见！」）与 `/help`。
- 剩余集可由门禁 default 分支在运行时实测枚举（执行时以实测为准）。

**目标**：剩余 legacy 处理器逐批迁到 typed `CommandResult`/UI action → 删除三件套与
`handleCommand` 回落分支 → legacy 命令处理器全量退役。

**完成判据**

- 门禁/fence 删除后，unified 下任意命令不可能触达 legacy 直写/模态读；
- console/plain 模式（M2/M3）行为不变；compat 真机场景 PASS；
- commands/ui 全量绿。

**前置**：每命令的**语义交互模型**（picker/确认流范式已有大量先例可复用）。

**风险**：剩余多为复杂流（checkpoint 应用、picker 变体、带删除/新建的选择器）；
逐命令迁移期长，需保持「迁移一刀一提交、可 revert」纪律。

**规模**：最大（多项独立小刀；建议从门禁 default 实测集合按复杂度分批）。

**执行记录（2026-10-09）**：已升级为独立方案文档
[aicli-l5-3-command-channel-closure-plan-20261009.md](aicli-l5-3-command-channel-closure-plan-20261009.md)；
Batch A 已执行（`f5c2286d`）：机械枚举 72 个目录命令后，唯一缺口 `/normal`（全名漏接）
修复 + 回归测试 4 子例。Batch B 已执行（`f396f2d9`）：/rewind 别名语义收编 + /resume
typed 降级 + 迁移 fence 全量删除（13 files，+100/−130）。Batch C 已执行（`2a125651`）：
硬门禁删除 + /exit 结构化 + 未知命令两档回落 + 目录覆盖机械守卫（72 名称），
L5-3 三批次收口完成；compat 场景已脚本化并全绿
（`scripts/test-aicli-compat-mode-e2e.ps1`，E2E-COMPAT-01，6/6）。

## 4. 跟踪项：session 侧 DEC 2026 同步帧包裹（默认不上路）

**现状锚点（2026-10-09 复核，零漂移）**

- 帧原子性现状 = 唯一物理 writer `TerminalSession` 的「单飞 executor + 每帧一次 `Write` +
  写锁串行」，**无** emulator 级 2026 包裹。
- 残留资产仅 1 处：`ui/terminal_driver.go:21-25` 能力位 `SynchronizedOutput`
  （`:110` 赋值：`ansi && vt`），**无消费者**。
- 零发射者复核：`\x1b[?2026h/l`、`SetTerminalSynchronizedFrames`、`AICLI_DISABLE_SYNC_UPDATE`
  在非测试代码中均为 0 引用。

**触发条件（唯一门槛）**：真机 tearing 证据（录屏/字节捕获）。

**若触发的实现约束**

- 必须**经 session writer/事务**发射 `\x1b[?2026h/l`（禁止裸 stdout——这正是 legacy 版被
  退役的原因）；
- 与「单帧一次 `Write`」的原子提交语义对齐（包裹覆盖一帧的提交边界）；
- 真机验证：撕裂消失 + 单写端栅栏（`TestUnifiedSessionSinglePhysicalWriterFence`）保持 PASS。

**规模**：小（约 1–2 提交 + 真机验证）；未触发则保持现状，无任何动作。

## 5. 与现有文档的关系 / 升级路径

- 本文是退役方案 §5「L5 观察项」与「跟踪项」的**展开评估**；触发式立项，不改变主链
  「全部完成」状态。
- 升级路径：某触发条件满足 → 从本文对应章节生成独立方案文档（含分批切片与验收）→ 执行 →
  回填退役方案/P0 台账（沿用 L1–L4 的记录格式）。
- 关联约束：写端基线 ceiling（受认可 24 / 债务 4）与 compat 真机场景为所有后续批次共用验收面；
  门禁语义（sanctioned 零新增 / debt 只降不升）见 `backend/cmd/aicli/ui/README.md`「写端门禁」。
