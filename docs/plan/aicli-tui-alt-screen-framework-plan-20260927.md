# aicli TUI 统一副屏框架计划（主屏 → 命令 → 副屏 → Esc → 主屏）

- 状态：待评审（v1.1：§10 开放决策点已按 ADR 转正为**已确认决策** D-A…D-F；v1.0：按最佳实践补齐非目标、契约与不变量、错误分类、回滚与开关、可观测性、风险登记、测试矩阵与量化验收；v0.2 已并入两个只读盘点子任务结论）
- 日期：2026-09-27
- 版本历史：v0.1 初稿 → v0.2 盘点落地（命令通道 + 副屏基础设施）→ v1.0 工程化补强 → v1.1 §10 决策转正（本版）
- 目标流程：**主屏 → 输入/执行命令 → 副屏（alternate screen）→ Esc → 主屏**
- 一句话摘要：把现有十余处各自实现的副屏收编为**一个 `openChatScreen` 框架 + 一份 55 条命令输出类别清单 + 一条统一 Esc 契约**，分 6 个可独立合并、可开关回滚的批次落地；§10 的六条决策（D-A…D-F）已连同理由、验证与回退口径确认为定论。
- 关联文档：
  - `docs/plan/aicli-tui-busy-slash-command-execution-plan-20260927.md`（忙时命令分档与执行闸门）
  - `docs/plan/esc-interrupt-priority-and-loop-robustness-plan-20260918.md`（Esc 语义）
  - **上位规范**：`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`（Scene / single screen owner / 事务式 frame / fullscreen lease）
  - `docs/plan/aicli-tui-transcript-overlay-renderer-mode-plan.md`（primary/alternate 所有权边界、lease 释放后的 retained-state repaint）
  - `docs/plan/aicli-tui-p5-owned-viewport-design.md`（只读历史故障样本，禁止作为新实现依据）
  - `docs/plan/aicli-terminal-e2e-methodology.md`（L3 进程内 VT e2e 方法，§7.1 引用）

---

## 1. 背景与问题

当前 chat TUI 的命令输出存在**三条互不相同的通道**，副屏能力被拆散在十余个命令实现里：

1. **主屏内联命令单元格**：`CommandResult{Blocks: ...}` → `renderChatCommandResult` → `Interaction.RenderCommandDocument`（`backend/cmd/aicli/commands/chat_command_output.go:14-32`、`chat_interaction.go:4619-4655`）。`/help` 走这条（`chat_unified_command_gate.go:24-26`）。
2. **副屏（ScreenLease / alternate screen）**：`CommandResult` 上的十余个 `Open*Screen` / `Open*Picker` 效果字段驱动，每个命令各自调用 `session.Surface.AcquireAlternateScreen(...)` 并自建渲染循环。
3. **legacy 通道**：未迁移命令的 stdout 直写（`writeChatCommandResultPlain`、`NewStdoutCommandTextWriter`）。

由此产生的问题：

- **入口不统一**：副屏的准入没有统一标准，"为什么 `/help` 在主屏、`/usage` 在副屏"无法用一条规则回答。
- **实现重复**：每个副屏各自处理取租约、渲染循环、Esc/取消、错误降级、返回主屏；差异散落在十余个文件（见 §2.2）。
- **Esc 语义分散**：主屏与副屏、只读页与多阶段 picker 的 Esc 行为各自实现，缺少统一契约（见 §2.3）。
- **扩展成本高**：新增一个"内容较长、需要滚动/选择"的命令时，需要新加 `Open*` 字段 + 消费点 + 专属文件。

本计划的目标是把副屏能力收敛成**一个框架 + 一份命令分档清单 + 一条统一 Esc 契约**。

---

## 2. 现状（事实与证据）

### 2.1 命令输出的三条通道

| 通道 | 入口 | 落点 | 证据 |
| --- | --- | --- | --- |
| 主屏内联 | `CommandResult.Blocks` | `RenderCommandDocument` → 保留命令单元格（主屏消息流） | `chat_command_output.go:20-28`、`chat_interaction.go:4615-4655` |
| 副屏 | `CommandResult.Open*` 效果字段 | `AcquireAlternateScreen` + 各自渲染循环 | `chat_command_result.go:127-190+`、§2.2 表 |
| legacy | 非交互/未迁移 | stdout 直写 | `chat_command_output.go:31`、`chat_command_text_writer.go:17` |

### 2.2 现有副屏接入点（各自为政）

已确认的 `AcquireAlternateScreen` 调用点（来自本轮 grep，逐文件证据）：

| 文件 | 调用点 | 形态 |
| --- | --- | --- |
| `chat_usage_screen.go` | `:87` | 只读文档/表格分页 |
| `chat_account_screen.go` | `:212` | 只读文档分页（与 /usage 同契约，见 `:21`、`:79` 注释） |
| `chat_debug_overlay.go` | `:36` | 只读文档覆盖层 |
| `chat_web_command.go` | `:217` | 复用 debug overlay 的只读页 |
| `chat_transcript_pager.go` | `:31` | 只读 transcript 分页器（Ctrl+T 与 `/history`） |
| `chat_backtrack_select.go` | `:194`、`:468` | 交互选择（回溯） |
| `chat_model_picker.go` | `:247`/`:303` 附近 | 多阶段交互选择 |
| `chat_theme_picker.go` | `:45` | 实时预览 + 选择 |
| `chat_skill_picker.go` | `:133` | 选择器 |
| `chat_export_picker.go` | `:75`、`:168` | 选择器 |
| `chat_mcp_picker.go` | `:261` | 选择器 |
| `chat_resume_command.go` | `:202`、`:399-405` | 选择器 |
| `chat_picker_common.go` | `:115-195` | 公共 picker stage 封装（`chatPickerOpen/Stage/Close`） |
| `chat_routing_panel.go` | `:57` 附近 | 只读面板 |

对应的 `CommandResult` 效果字段包括但不限于：`OpenTranscript`、`OpenDebugOverlay`、`OpenWebEndpointsScreen`、`OpenUsageScreen`、`OpenAccountScreen`、`OpenAccountsScreen`、`OpenResumePicker`、`OpenBacktrackPicker`、`OpenModelPicker`、`OpenThemePicker`、`OpenSkillPicker`、`OpenExportPicker`、`OpenMCPPicker`（字段定义见 `chat_command_result.go:127-190+`，调用点见各命令文件）。

> 注：已有 `chatPickerOpen/Stage/Close`（`chat_picker_common.go:174-195`）是最接近"框架"的现有设施，但只覆盖 picker，不覆盖只读文档页，也没有统一的 Esc 契约。

### 2.3 Esc 语义分散（现状）

| 场景 | 现状 Esc 行为 | 证据 |
| --- | --- | --- |
| 主屏空闲 + 空输入 | 打开回溯 picker（副屏） | `inputbox_editor.go:1313-1319` → `chat.go:1699-1702` → `chat_backtrack_select.go:24-41` |
| 主屏空闲 + 有草稿 | 不丢草稿，键被吞 | `inputbox_editor.go:1304-1319` |
| 主屏忙时 | 中断当前回合（"已中断 - ESC 取消当前操作"） | `chat_composer.go:543-548` → `chat_busy_input.go:115-123`、`:204-213` |
| 副屏内 | 逐屏实现：`fullscreen_list.go:513-514`、`transcript_pager.go:737,752` 等 | 各副屏文件 |

结论：**Esc 没有统一契约**（副屏内"返回上一级 vs 关闭回主屏"、"是否可中断"都由各实现自行决定），而用户期望的是统一流程「副屏内 Esc → 回到主屏」。

### 2.4 现有副屏的三族渲染器与两套租约生命周期（盘点结论）

**三族渲染器**（全部已有，统一框架只做收编，不新增渲染路径）：

| 族 | 原语 | 形态与键位证据 |
| --- | --- | --- |
| A | `ui.SelectFullScreenListWithLease`（`ui/fullscreen_list.go:162`） | 可搜索列表 / 多阶段 picker；返回 `FullScreenListResult{Index,Cancelled,DeleteRequested,Text}`（`:88-96`）；取消键 Esc/q/interrupt/EOF（`:513-514`、`:533-534`）；`x/X/Delete` → 删除请求（`:535-566`）；自由文本 Esc/q 取消（`:576-591`） |
| B | `ui.RunDebugOverlayWithLease`（`ui/debug_overlay.go:66`） | 静态文档 + 滚动；q/Q/Esc/Enter/Ctrl+C/Ctrl+D 一律直接关闭（`:261-267`） |
| C | `ui.RunTranscriptPagerWithLease`（调用点 `chat_transcript_pager.go:59`） | actor AppState 绑定的分页器 |

**两套租约生命周期**（统一框架要归并的对象）：

1. **共享封装**：`chatPickerOpen` / `chatPickerStage` / `chatPickerStageResult` / `chatPickerFreeTextStage` / `chatPickerClose`（`chat_picker_common.go:111-205`，含就绪门 `chatPickerSurfaceReady :39-55` 与错误哨兵 `:27-37`）；使用者为 `/model`、`/provider`、`/routing panel`。
2. **内联重复**：`chat_theme_picker.go:45`、`chat_skill_picker.go:133`、`chat_export_picker.go:75`、`chat_mcp_picker.go:261`、`chat_resume_command.go:202`、`chat_theme_command.go:577`、`chat_backtrack_select.go:194` 各自 `AcquireAlternateScreen` + post `ui.Open*Picker` + `waitUIActorIdleBounded` + release/Close，与共享封装高度重合。

**释放屏障契约（必须原样保留）**：post Close → release → `waitUIActorIdleBounded` → 才允许 mutate Scene（`chat_backtrack_select.go:236-244`、`chat_transcript_pager.go:52-57`、`chat_picker_common.go:195-205`）。

**A 族与 B 族的契约差异（统一框架必须消除）**：A 族在取租约前后 post `Open*Picker`/`Close*Picker` 动作并等 actor barrier；B 族不 post，只依赖 `LeaseAcquired` 屏障 + `waitUIActorIdleBounded`。二者应统一到同一 barrier 契约（建议全部显式 post "screen opened/closed" 动作）。

### 2.5 命令通道事实源盘点（55 条命令，只读子任务结论）

- **三轨并存**：(a) 结构化 `CommandResult` 主屏内联单元格；(b) 各命令自实现的副屏/picker（§2.4）；(c) legacy stdout 直写——`/help` → `printChatCommandOutput`（`chat_slash_help.go:68`）、`/exit` → `printDirectInteractiveOutput`（`command.go:449`）、`/sessions`、`/functions`、`/timeline`、`/collab` 等仍走 `printChatCommandOutput`。
- **忙时副屏只有 5 条白名单**：`/todos`、`/history`、`/usage`、`/debug`(display)、`/web`(endpoints)（`chat_busy_screen_exec.go:18-35`，要求 `Mode==Screen && Effect==Read`；等待预算 `chatBusyScreenWaitBudget=2s` `:15-16`）。registry 声明为 `screen` 的 `/model`、`/provider`、`/theme select`、`/skills`、`/mcp`、`/agents`、`/routing panel`、`/profile pick`、`/export` 在忙时全部降级排队。
- **形态分裂**：`/model`、`/provider`、`/theme`、`/skills`、`/export`、`/resume`、`/backtrack` 的只读变体内联、交互变体副屏，没有统一的"只读列表也可翻页"回退。
- **事实源分裂**：`catalog.BusyPolicy` 与 `runtimeRegistry.Mode` 两处并存，行为变更易漂移。

---

## 3. 目标与非目标

### 3.1 目标（验收口径）

| # | 目标 | 验收口径 |
| --- | --- | --- |
| G1 | 统一副屏框架：一个入口 `openChatScreen(session, spec)`，统一负责租约获取/释放、渲染循环、Esc/取消、错误降级、主屏恢复 | 全部副屏命令共用该入口；框架文件之外 `AcquireAlternateScreen` 调用点为 0（守卫测试，A2） |
| G2 | 命令分档清单（§5）：每条命令明确归属「主屏内联 / 副屏只读 / 副屏交互 / 主屏副作用」 | 单一事实源声明输出类别；未声明即守卫测试失败（A1） |
| G3 | 统一 Esc 契约：副屏内 Esc = 关闭副屏返回主屏；多阶段在副屏内推进；Esc 永不退出进程；主屏 Esc 语义不变（空闲回溯 / 忙时中断） | 键序列测试 + L2 e2e 逐命令断言（T5/T6） |
| G4 | 迁移后**行为不变性或显式变更**逐条可测 | 每条命令的字符级差异书面说明，0 条未解释（A4） |
| G5 | 可回滚：框架与命令迁移均可在不 revert 代码的情况下按开关回退 | 批次 0 起提供 `AICLI_CHAT_SCREEN_FRAMEWORK=legacy`（§6.1），回退演练通过（A8） |
| G6 | 可观测：关键生命周期事件 + 计数器可在 debug 日志与 `/debug display` 中查看 | 事件表落地（§8.1）、计数器可见（§8.2） |

### 3.2 非目标（本计划明确不做）

- 不改动上位渲染架构（Scene / single screen owner / 事务式 frame / fullscreen lease），不新增第二条 alt-screen 渲染路径。
- 不引入副屏嵌套（D-D，已确认，见 §10）；`ScreenStages` 在租约内推进，不重新取租约。
- 不改动 plain/JSON 投影的既有字段与语义（只允许新增字段；废弃字段走 §6.2 弃用窗口）。
- 不重构 `ui` 包既有原语内部实现（`SelectFullScreenList` / `RunDebugOverlay` / `RunTranscriptPager` 只做编排复用）。
- 不做主题、布局、键位重映射等体验改版；不改动上表之外命令的输出通道。
- 不改变 `commands` → `ui` 的单向依赖（`ui` 不得反向依赖 `commands`）。

### 3.3 假设与依赖

- A1：`chatPickerOpen/Close`（`chat_picker_common.go:174-205`）的屏障顺序可无损复用：post Close → release → `waitUIActorIdleBounded` → 才 mutate Scene。
- A2：`waitUIActorIdleBounded`（`chat_ui_actor.go:1257` 起）超时返回 false，调用方必须 fail-closed（不得继续进入 legacy 写）。
- A3：忙时闸门的 2s 等待预算（`chat_busy_screen_exec.go:15-16`）与能力门（`:37-53`）语义可被框架内聚而不放宽（§4.4）。
- A4：`CommandResult` 旧 `Open*` 字段可做纯映射兼容（新增 `Screen` 字段与旧字段并存，不破坏既有消费点与 JSON 投影）。
- 依赖：上位渲染计划与忙时命令计划的既定契约先行；本计划不与其并行改动同一契约（冲突时以上位规范为准）。

---

## 4. 统一副屏框架设计

### 4.1 核心抽象

```go
// commands 包内（示意）
type ScreenKind int

const (
    ScreenDocument ScreenKind = iota // 只读文档/表格（可滚动）
    ScreenList                       // 列表选择
    ScreenStages                     // 多阶段选择（provider→model→reasoning）
)

type ScreenSpec struct {
    ID    string          // 稳定标识：日志 / 审计 / 测试锚点
    Title string          // 副屏标题（交给 ui.FullscreenRequest）
    Kind  ScreenKind
    Doc   render.Document // ScreenDocument 的静态内容
    Rows  []ScreenRow     // ScreenList 的行（含搜索/分组元数据）
    Stages []ScreenStage  // ScreenStages 的有序阶段
    Esc   EscPolicy       // TopLevelClose（默认，一次 Esc 回主屏）| StepBack（仅显式启用）
}
```

设计要点：

- **单一效果字段**：`CommandResult` 新增 `Screen *ScreenSpec`，逐步替代十余个 `Open*` 字段（保留旧字段作为兼容映射层，迁移完成后删除）。
- **单一入口**：`openChatScreen` 内部只做四件事：
  1. `AcquireAlternateScreen`（沿用现有 `Surface` 契约与忙时等待预算）；
  2. 按 `Kind` 走统一渲染循环（复用 `ui.SelectFullScreenList` / 现有文档分页实现）；
  3. Esc/取消统一收敛为 `EscPolicy`；
  4. `Release` 后恢复主屏，并按需提交一条结果单元格。
- **生命周期实现**：`chatScreenOpen` / `chatScreenClose` 建立在现有 `chatPickerOpen` / `chatPickerClose`（`chat_picker_common.go:170-205`）之上，保留屏障顺序（post Close → release → `waitUIActorIdleBounded` → 才 mutate Scene），A/B 族统一收敛到该契约。
- **结果回流**：选择/确认的结果经调用方回调在**租约释放后**应用（延续现有 picker 契约：`chat_command_result.go:43-57` 注释里"mutation only after alternate-screen ownership has been released"）。

### 4.2 契约细则（所有权 / 并发 / 重入 / 超时 / panic 安全）

| 维度 | 契约 | 违反时行为 |
| --- | --- | --- |
| 所有权 | `ScreenSpec` 及其 `Doc/Rows/Stages` 是一次性快照，在 `openChatScreen` 前构建完毕；租约存续期不得读写共享可变状态 | 防御性断言 + `aicli.chat.screen.violation` 事件；fail-closed 关闭副屏 |
| 并发 | 同一时刻至多一个副屏（单一 ScreenOwner）；`open` / `close` 只从命令派发路径（主 goroutine）发起 | 已有租约 → `ErrScreenLeaseBusy` → 等待预算 → 超时内联降级 |
| 重入 | 不可重入、不可嵌套（D-D，已确认，见 §10）；框架内部禁止再取租约 | 立即返回 `ErrScreenNested`，不排队、不阻塞 |
| 超时 | 租约等待预算 2s（沿用 `chatBusyScreenWaitBudget`，`chat_busy_screen_exec.go:15-16`）；`waitUIActorIdleBounded` 有界等待；框架内无任何无界阻塞 | 超时即降级：内联提示 + `degrade` 事件（reason 可区分 busy/unavailable） |
| 幂等 | `chatScreenClose` 可被 `defer` 重复调用；`ScreenLease.Release` 幂等（`screen_lease.go:200-220`） | 无副作用，重复调用不报错 |
| defer 保证 | 无论正常、取消、错误还是 panic 展开，close 序列（post Close → release → 等 actor idle）都由 `defer` 保证执行；禁止裸 return 跳过 | 由 I2/I9 测试覆盖；违反按 P1 缺陷处理 |
| 取消 | Esc/取消是正常结果（非错误），与确认路径共用同一条 close 序列 | 取消不得跳过 release（T5） |

### 4.3 生命周期（主屏 → 命令 → 副屏 → Esc → 主屏）

```
主屏(composer)
  │ 用户输入命令 / 快捷键
  ▼
命令派发（统一 gate）
  │ result.Screen != nil
  ▼
openChatScreen(spec)
  ├─ acquire ScreenLease ──(busy: 等待预算, 超时则拒绝并内联提示)
  ├─ suspend primary presenter → 渲染副屏
  ├─ 交互循环（滚动/搜索/选择/预览）
  └─ Esc/取消/确认
        │
        ▼
  release lease → 恢复主屏 present →（可选）提交结果单元格
```

### 4.4 与忙时执行闸门的关系

忙时副屏命令已经有一层闸门：`chat_busy_screen_exec.go:79-101`（`SetAlternateScreenWaitBudget`）与 `chat_busy_command_exec.go:114-118`（按 `Open*` 字段分派）。统一框架应把这段"等待预算 + 拒绝降级"逻辑内聚到 `openChatScreen`，让忙时/空闲共用一条代码路径。

### 4.5 失败与边界（错误分类 + 降级矩阵）

| 错误 | 触发 | 框架行为 | 用户可见 |
| --- | --- | --- | --- |
| `ErrScreenLeaseBusy` | 已有租约在途 | 在等待预算内轮询；超时降级 | 内联提示「当前无法打开全屏视图，已内联显示」 |
| `ErrFullScreenUnavailable` | 非 TTY / 未启用全屏 / 无 OwnedViewport | 直接降级为内联文档单元格 | 静默降级（不刷错误） |
| `ErrScreenNested`（新增） | 重入 / 嵌套打开 | 拒绝：不排队、不再取租约 | 内联降级 + debug 事件 |
| 渲染 / 屏障错误 | 终端写失败、`waitUIActorIdleBounded` 超时 | 关副屏 → release → 内联报错（fail-closed） | 一行错误 + 文档内联 |
| plain / JSON / 非交互 | 非交互投影 | `Screen` 退化为 plain 文档（与旧 `Open*` fallback 一致，`chat_command_result.go:139-146`） | 无副屏 |

交互路径禁止落回 legacy stdout 直写（I6）；降级一律走主屏内联命令单元格。非交互/JSON 继续走既有 plain 投影，不新增旁路。

### 4.6 与上位渲染规范的约束（必须遵守）

- 服从 `aicli-tui-unified-render-architecture-refactor-plan.md`：任何时刻**只有一个 ScreenOwner** 写 TTY；副屏必须经 `ScreenLease`（或同语义租约）取得所有权，不得引入第二条渲染路径。
- 租约释放 = primary presenter 恢复并做完整 retained-state repaint，**composer 草稿不丢**（`transcript-overlay` 计划 §0.1 已确立的契约）。
- 副屏输入必须带 lease fence：陈旧租约的延迟按键由 reducer 忽略，框架不得绕过 actor 直接读 stdin。
- 禁止拉回已废弃机制（`historyWindow` / whole-screen owned viewport / 几何驱动 handoff，见 P5 最终 disposition）。

### 4.7 不变量清单（Invariants，违反即 fail-closed）

| # | 不变量 | 强制手段（测试 / 守卫） |
| --- | --- | --- |
| I1 | 任意时刻只有一个 ScreenOwner 写 TTY；副屏写入必须持有有效租约（lease fence） | 守卫测试：`AcquireAlternateScreen` 仅允许出现在框架文件；陈旧按键由 reducer 忽略（既有契约） |
| I2 | 释放顺序恒为 post Close → release → `waitUIActorIdleBounded` → 才 mutate Scene | 契约测试（沿用 `chat_ui_actor_surface_test.go` 的 barrier 测试设施）+ 框架生命周期测试 T1 |
| I3 | 选择/确认的副作用只在租约释放后应用 | 回调断言测试 T7（`chat_command_result.go:43-57` 既有契约不回退） |
| I4 | Esc 永不退出进程；副屏内 Esc 不触发主屏回溯、不中断回合；主屏 Esc 语义不变 | 键序列测试 T5/T6 + L2 e2e |
| I5 | release 后 primary 完整 repaint（retained state），composer 草稿不丢 | 生命周期测试 T2 + 忙时草稿回归 T6 |
| I6 | 交互路径 fail-closed：统一路径不落 legacy stdout 直写，降级只走主屏内联单元格 | 守卫测试（统一 gate 路径不得调 `printChatCommandOutput`），指标 A3 |
| I7 | 忙时/空闲共用同一 `openChatScreen`；等待预算与能力门只有一份实现 | 代码守卫（单实现）+ 忙时测试 T10 |
| I8 | 框架不读 stdin、不直接写 TTY；输入一律经既有 reducer / actor 通道 | 守卫测试（框架文件无 `os.Stdin`/`os.Stdout` 引用）+ 评审清单（§6.3） |
| I9 | 无副屏嵌套；`ScreenStages` 在租约内推进，不重新取租约 | 重入测试 T8（`ErrScreenNested`） |

---

## 5. 命令分档清单（副屏准入标准）

### 5.1 准入标准（本次统一口径）

| 判据 | 去向 |
| --- | --- |
| 输出为短确认/错误/单行状态（≤ 3 行） | 主屏内联命令单元格 |
| 输出为需要滚动/搜索/分页的长文档（超过半屏） | 副屏只读页（`ScreenDocument`） |
| 需要列表选择、多阶段确认、实时预览 | 副屏交互页（`ScreenList` / `ScreenStages`） |
| 纯控制/副作用命令（`/exit`、`/clear`、`/compact` 等） | 主屏执行 + 结果单元格 |
| 需要与后续对话并排滚动阅读的参考卡 | 主屏内联（D-A 同判据：不独占键盘、需并排阅读） |

原则：**副屏只承载"独占键盘 + 需要翻页/选择"的内容**；其余一律留在主屏，避免为了统一而滥用副屏。

### 5.2 命令分档清单（盘点落地）

**判据回顾**：≤3 行短输出 → 主屏内联；需要滚动/搜索/分页的长文档 → 副屏只读（`ScreenDocument`）；需要选择/多阶段/实时预览 → 副屏交互（`ScreenList` / `ScreenStages`）；纯副作用/确认 → 主屏执行 + 结果单元格。

**（1）已是副屏 → 收编到统一框架（行为保持）**

| 命令 | 现状形态 | 目标 |
| --- | --- | --- |
| `/usage`、`/debug display`、`/web endpoints`、`/account`、`/accounts` | B 族只读 viewer | `ScreenDocument` |
| `/history`、Ctrl+T | C 族分页器 | `ScreenDocument` |
| `/todos` | 只读面板（忙时白名单） | `ScreenDocument` |
| `/resume`、`/backtrack`、`/model`、`/provider`、`/theme select`、`/skills`、`/export`、`/mcp`、`/routing panel`、`/profile pick` | A 族 picker / 多阶段 | `ScreenList` / `ScreenStages` |
| `/agents panel`、`/agent`（transcript 视图） | A 族 / 内联详情 | `ScreenDocument` / `ScreenList` |

**（2）建议迁入副屏（只读长文档，本次新增能力）**

| 命令 | 现状 | 迁入理由（盘点证据） |
| --- | --- | --- |
| `/help` | 主屏内联（`chat_unified_command_gate.go:24-26`）＋ legacy `printChatCommandOutput`（`chat_slash_help.go:68`） | ≈55 条命令 ×1 行 + shell 段 ≈ **80+ 行**，当前一次性打屏 |
| `/status` | 内联多段报告 | 多段只读快照 |
| `/sessions`、`/plans detail`、`/functions` | legacy `printChatCommandOutput` | 条目随数量线性增长 |
| `/timeline`、`/collab` | legacy / 内联 | 带 `limit`/`filter` 的事件流 |
| `/agents panel [full]`、`/agent transcript` | 内联 | `full` 为完整详情（`limit` 默认 200、上限 2000），天然分页 |
| `/hotkeys` | 内联 | 静态键位表，典型分页器 |
| `/model`、`/provider`、`/theme list`、`/skills list`、`/mcp list`、`/profile list`、`/export` 的**只读 list 变体** | 内联 | 消除"只读内联 / 交互副屏"同命令形态分裂 |

**（3）保持主屏（短输出 / 副作用 / 提示）**

`/exit`、`/clear`、`/compact`、`/retry`、`/queue`（裸）、`/image`、`/attach`、`/cmd`(`!`)、`/trust`、`/add-dir`、`/approval-reuse`、`/grants`、`/permission-mode` set、`/plan`、`/supervision`、`/reasoning*`、`/stream`、`/fast`、`/s`、`/normal`、`/login` 及其余短确认/错误回执。

**（4）单一事实源与守卫（防漂移）**

输出类别在命令目录的**单一事实源**中声明，并在批次 5 与 `catalog.BusyPolicy`、`runtimeRegistry.Mode` 合并为一份定义（消除 §2.5 的"事实源分裂"）：

| 类别 | 含义 | 允许的效果字段 |
| --- | --- | --- |
| `inline` | 主屏内联单元格 | `Blocks` |
| `screen-document` | 副屏只读页 | `Screen{Kind: ScreenDocument}` |
| `screen-interactive` | 副屏交互页 | `Screen{Kind: ScreenList / ScreenStages}` |
| `side-effect` | 主屏执行 + 结果单元格 | 无 `Screen` 效果 |

守卫规则：

1. 目录中的全部 55 条命令（含别名归并）必须命中且仅命中一个类别；未声明即守卫测试失败（T11）。
2. `Screen` 效果与声明类别不一致（如 `side-effect` 命令返回 `Screen`）即守卫测试失败。
3. 子命令级覆盖（如 `/queue clear`、`/plans … reopen`）继承父命令类别；确需例外必须显式覆盖并附对应测试，否则按父类别断言。
4. 新增命令必须同时提交类别声明与至少一条 Esc / 生命周期断言，否则评审不通过（§6.3）。上表为盘点结论，作为首版分类基线。

---

## 6. 迁移批次、开关与回滚

目标：**每一步都可独立合并、可回滚、行为差异可测**；框架先进场，命令分批收编。

| 批次 | 内容 | 验收 |
| --- | --- | --- |
| 0 框架落地 | 新增 `chat_screen_framework.go`（`ScreenSpec{ID,Title,Kind,Doc,Rows,Stages,Esc}` + `openChatScreen` + `EscPolicy{TopLevelClose(默认),StepBack}`）；统一生命周期 `chatScreenOpen/Close`，内部复用 `chatPickerOpen/Close` 的屏障顺序（post Close → release → `waitUIActorIdleBounded` → mutate），并给 B 族补上显式 open/close 动作；`CommandResult.Screen` 与旧 `Open*` 字段并存、旧字段映射进新框架（行为不变）；忙时能力门 + 2s 等待预算内聚进框架 | 现有测试全绿；新增生命周期 + Esc 契约测试 |
| 1 只读页收编（B/C 族） | `/usage`、`/account`、`/accounts`、`/debug display`、`/web endpoints`、`/todos`、`/history` + Ctrl+T → `ScreenDocument`；统一 A/B 族屏障契约 | 每命令一条"命令 → 副屏 → Esc → 主屏"断言；输出字符级差异逐条说明 |
| 2 交互页收编（A 族） | `/resume`、`/backtrack`、`/model`、`/provider`、`/theme`、`/skills`、`/export`、`/mcp`、`/routing panel`、`/profile pick`、`/agents panel` → `ScreenList` / `ScreenStages`；§2.4 第 2 条列出的 7 个内联租约实现改走统一封装并删除重复代码 | 多阶段 Esc 一次回主屏（D-B，已确认；搜索态例外见 §10.2）；结果应用仍在租约释放后（不回归既有契约） |
| 3 长文档迁入（新增能力） | `/help`、`/status`、`/sessions`、`/plans detail`、`/functions`、`/timeline`、`/collab`、`/hotkeys` 及只读 list 变体 → `ScreenDocument`（§5.2（2））；同时把 legacy stdout 直写（§2.5 三轨之 (c)）收编进 `CommandResult` | 上述命令全部经统一 gate；统一交互路径上 `printChatCommandOutput` 直写归零 |
| 4 忙时闸门扩展 | 白名单从 5 条（§2.5）扩展到全部 `Effect==Read` 的 `ScreenDocument` 命令；保持 fail-closed 能力门 + 等待预算 + 超时降级提示 | 忙时/空闲两条路径共用 `openChatScreen`；降级行为有测试 |
| 5 清理与守卫 | 删除旧 `Open*` 字段；`catalog.BusyPolicy` 与 `runtimeRegistry.Mode` 合并为单一事实源；落清单守卫测试（每条命令必须声明输出类别）；更新 `/help` 文案（副屏命令标注「Esc 返回」） | 全量 `go build ./cmd/aicli/...` + `go test ./cmd/aicli/...` |

**框架落位**：放在 `commands` 包（`chat_screen_framework.go`），`ui` 包只保留通用原语（`ScreenLease`、`SelectFullScreenList`、文档分页），保持 `ui` 不反向依赖 `commands`。

**防漂移守卫**：新增一条清单守卫测试——命令目录（`buildChatSlashHelpLines` 的数据源）中的每条命令必须声明输出类别（主屏内联 / 副屏只读 / 副屏交互 / 副作用），未声明即测试失败；防止未来新增命令再次出现"通道不明"。

**批次 DoD（每批合并前必须满足，缺一不可）**：

1. `go build ./cmd/aicli/...` + `go test ./cmd/aicli/...` 全绿；框架与 actor 边界包加跑 `-race`。
2. 本批触及的不变量（I1–I9）均有对应测试且通过；新增守卫测试**先于实现落地**（测试先行，先红后绿）。
3. 行为差异逐条书面说明（字符级 diff / 键序差异），0 条未解释。
4. 本批引入的行为都可通过 §6.1 开关退回上一批次行为，且回退路径有测试。
5. 本批路径的 debug 事件与计数器可用（§8.1 / §8.2），且在 `/debug display` 可见。

### 6.1 特性开关与灰度（Kill Switch）

| 开关 | 取值 | 作用 | 默认 |
| --- | --- | --- | --- |
| `AICLI_CHAT_SCREEN_FRAMEWORK`（新增，框架级） | `unified` / `legacy` | `legacy` = 副屏命令走批次 0 之前的旧实现（经兼容映射层），出问题一键回退 | `unified`（批次 0 起生效） |
| `AICLI_CHAT_BUSY_COMMAND`（既有，`chat_busy_command_policy.go:42-45`） | 启用 / 显式关闭 | 与本框架正交：忙时执行闸门总闸；显式关闭时忙时行为回退首批白名单 | 保持忙时计划 D14 语义 |
| `AICLI_CHAT_RUNTIME_INTERACTION`（既有，忙时计划 §335） | `auto` / `readonly` / `off` | 分层收窄忙时可执行档；`off` = 全部排队 | `auto` |

- 开关读取集中在框架入口一处；未知值按默认 `unified` 处理并记 debug 事件（`reason=unknown_env`）。
- `legacy` 分支只服务迁移窗口：批次 5 完成后删除分支与开关（届时 `legacy` 变为无效值并记 warning）。

### 6.2 回滚策略（Rollback）

| 层面 | 手段 | 验证 |
| --- | --- | --- |
| 命令级 | `AICLI_CHAT_SCREEN_FRAMEWORK=legacy` 重启会话 → 全部副屏命令回旧实现 | 回退演练 A8：抽 `/usage`、`/model`、`/help` 对比批次前录制的键序与输出 |
| 批次级 | 每个批次独立 PR；回滚 = revert 单个 PR（无数据库/状态迁移，无残留） | PR 模板必填「本批开关回退路径」 |
| 契约级 | 旧 `Open*` 字段在批次 5 前不删除；`Screen` 为纯增量字段 | 兼容映射测试 + A2 守卫防新增旁路 |
| 数据 | 无持久化 schema 变更；会话历史不受影响 | 无需数据回滚 |

### 6.3 变更评审清单（PR Checklist）

- [ ] 新副屏命令只声明 `Screen`，未新增 `Open*` 字段；未直接调用 `AcquireAlternateScreen`。
- [ ] 未在框架内读 stdin / 未直写 TTY；租约释放顺序未被改动（I1/I2/I8）。
- [ ] 命令类别已在单一事实源声明（§5.2(4)），并附 Esc / 生命周期断言。
- [ ] 新增/变更的 debug 事件只含 ID/枚举/耗时，无内容正文与凭据（§8.1）。
- [ ] 开关回退路径已验证（§6.1）。

---

## 7. 测试与验收

### 7.1 测试分层

| 层 | 手段 | 覆盖 | 运行位置 |
| --- | --- | --- | --- |
| L0 单元 | Go table tests：Spec 校验、EscPolicy 解析、错误映射、类别守卫 | 框架纯逻辑 + 命令目录守卫 | CI |
| L1 契约/集成 | 现有测试设施：`chat_busy_screen_gate_test.go`（`ui.FixedBottomSurface` 门）、`chat_ui_actor_surface_test.go`（barrier/lease 测试）；固定 surface + 租约记录器 | 生命周期、租约边界、失败降级、defer/panic | CI |
| L2 进程内 VT e2e | `os.Pipe + VT 重建` 的进程内交互 e2e（`aicli-terminal-e2e-methodology.md` L3，fake executor，CI 友好） | "主屏 → 命令 → 副屏 → Esc → 主屏" 逐命令 + 草稿保留 | CI |
| L3 真机 | Windows Terminal live / fixture e2e（同方法论 L1/L2） | 真实终端观感与兼容性 | 发布前 / 人工 |

### 7.2 必测矩阵（失败即阻断合并）

| T# | 场景 | 断言 |
| --- | --- | --- |
| T1 | 框架打开/关闭生命周期（`ScreenDocument` / `ScreenList` / `ScreenStages` 各一条） | 顺序被观测：post Close → release → actor idle → 才 mutate Scene |
| T2 | 逐命令「主屏 → 副屏 → Esc → 主屏」（L2） | 副屏退出后 primary repaint 完成、无残留帧、无重复单元格 |
| T3 | 租约忙 + 预算耗尽 | 不进入副屏；内联降级提示；无 legacy 输出 |
| T4 | 非 TTY / JSON / 非交互 | `Screen` 退化为 plain 文档；JSON schema 不变 |
| T5 | Esc 契约（非搜索态，逐命令） | 一次 Esc 关副屏回主屏；不触发回溯、不中断回合、不退出进程 |
| T5b | Esc 契约（搜索态与多阶段） | 搜索态先清查询 → 退出搜索 → 再 Esc 关闭（`fullscreen_list.go:478-499`、footer `:843-847`）；多阶段在任一阶段一次 Esc 回主屏 |
| T6 | 主屏忙时 Esc（D-C） | 中断语义保留；副屏内 Esc 不触达中断路径 |
| T6a | 忙时 Esc 草稿保留（D-C 修复） | Esc 中断后草稿仍在；下次 capture 的 `InitialText` 与中断前一致 |
| T7 | 结果应用时机 | 副作用只发生在 release 之后（回调断言） |
| T8 | 重入 / 嵌套 | 返回 `ErrScreenNested`；不排队、不取第二个租约 |
| T9 | panic / defer 展开 | close 序列仍执行；租约无泄漏 |
| T10 | 忙时白名单扩展（批次 4） | 全部 `Effect==Read` 的 `ScreenDocument` 命令可用；不可用时有降级提示 |
| T11 | 类别守卫 | 未声明类别 / 效果与类别不符的新命令导致测试失败 |
| T12 | 重复开合压力 | N 次开合无 goroutine / 租约 / fd 泄漏，无状态残留 |

### 7.3 非功能测试与预算

- `go test -race`：框架、actor 边界与生命周期相关包必跑（CI 门禁）。
- 泄漏检查：重复开合循环（T12）断言 goroutine 数有界、`FixedBottomSurface` 租约计数归零；`go.uber.org/goleak` 已在 `backend/go.sum`，可在本包单测按需启用。
- 键序列表驱动 + fuzz：Esc / q / Ctrl+C / EOF 在只读、自由文本、多阶段各形态下不得 panic、不得跳过 release。
- 性能预算（记录基线；不达标需写明原因，不静默忽略）：副屏首帧延迟、release → primary repaint 延迟、N 次开合无线性内存增长。
- 兼容矩阵：`xterm-256color`、tmux、SSH、winpty / Windows legacy console、非 TTY，逐项对应 §4.5 降级行为。

### 7.4 验收指标（Definition of Done，量化）

| A# | 指标 | 目标 |
| --- | --- | --- |
| A1 | 副屏命令经统一入口比例 | 100%（守卫测试） |
| A2 | 框架文件之外 `AcquireAlternateScreen` 调用点 | 0（守卫测试） |
| A3 | 统一交互路径 legacy stdout 直写 | 0（批次 3 后，守卫测试） |
| A4 | 迁移命令行为差异 | 逐条书面说明，0 条未解释 |
| A5 | 内联重复租约实现 | 删除 ≥7 处（§2.4 第 2 条），且无新增重复 |
| A6 | 每条副屏命令的 Esc 生命周期断言 | 100% 覆盖（L2） |
| A7 | 忙时只读 `ScreenDocument` 命令可用率 | 全部（白名单 = 全部 `Effect==Read`），降级 100% 有提示 |
| A8 | 回退演练 | `legacy` 开关回退后与批次 0 前行为一致（演练记录入库） |

---

## 8. 可观测性与运维

### 8.1 事件（debug 日志，`logpkg`）

| 事件 | 触发 | 字段（禁止记录内容正文与凭据） |
| --- | --- | --- |
| `aicli.chat.screen.open` | 租约获取成功、首帧前 | `screen_id`、`kind`、`lease_id`、`wait_ms`、`trigger`（command / busy / hotkey） |
| `aicli.chat.screen.close` | close 序列完成 | `screen_id`、`lease_id`、`result`（esc / confirm / cancel / error）、`held_ms`、`repaint_ms` |
| `aicli.chat.screen.degrade` | 能力门或租约超时导致内联降级 | `screen_id`、`reason`（busy / unavailable / nested / unknown_env / …） |
| `aicli.chat.screen.error` | 渲染、屏障或 close 错误 | `screen_id`、`lease_id`、`stage`（acquire / render / barrier / close）、`err` |
| `aicli.chat.screen.violation` | 不变量防御断言命中 | `invariant`（I1–I9）、`screen_id`、`action`（fail-closed 处置） |

- 脱敏：只允许 ID、枚举、耗时、尺寸；`/account`、`/debug display`、transcript 等正文与凭据一律不入日志（复用 `internal/pkg/logger` 的 debug 通道与既有日志级别开关）。
- 事件命名与忙时计划的 `aicli.chat.runtime_interaction` 审计事件保持同一前缀风格，便于统一采集。

### 8.2 计数器与诊断

- 计数器（进程内原子计数）：打开数、关闭数（按 result）、租约等待超时数、降级数（按 reason）、错误数（按 stage）、平均持有时间、repaint 耗时分布。
- 暴露：`/debug display` 新增 "Alt-screen framework" 只读区块，复用现有 `chat_debug_document.go` 文档构建器（组件风格见 `chat_debug_components.go`）；不新增独立命令，避免扩大命令面。
- 计数器只在 debug 视图呈现，不进入 JSON 事件流，避免影响既有输出契约（§3.2）。

### 8.3 故障定位 Runbook

| 症状 | 先看 | 处置 |
| --- | --- | --- |
| 副屏打不开（自动内联） | `degrade` 事件 reason + 计数器 | busy → 稍后重试；unavailable → 检查终端能力（tmux / winpty / 非 TTY）；unknown_env → 检查开关拼写 |
| 退出副屏后主屏花屏 / 残留 | `close` 事件 `repaint_ms` + `violation` 事件 | 置 `AICLI_CHAT_SCREEN_FRAMEWORK=legacy`；保留 debug 日志与复现键序 |
| 副屏内无响应 | `error` 事件 stage=barrier | 确认 actor 是否阻塞；用既有 Ctrl+C 退出路径恢复 |
| 忙时命令无响应 | `degrade` reason + busy 计数器 | 检查 `AICLI_CHAT_*` 档位与白名单；必要时 `AICLI_CHAT_RUNTIME_INTERACTION=off` 全排队 |

## 9. 风险登记表

| R# | 风险 | 触发信号 | 影响 | 缓解 / 回退 |
| --- | --- | --- | --- | --- |
| R1 | 生命周期归并引入 repaint 顺序回归 | 花屏 / 残留帧；I2、T1 测试失败 | 高 | 批次 0 先落契约与守卫测试；框架开关回退；每批独立 PR |
| R2 | Esc 行为变更（一次回主屏）与用户预期冲突 | 用户反馈；文档/footer 文案不一致 | 中 | `/help` 与 footer 统一标注「Esc 返回」；发布说明；`StepBack` 仅显式启用 |
| R3 | 输出迁移破坏既有快照或下游解析 | 快照测试大面积失败 | 中 | 字符级 diff 逐条审阅（A4）；JSON schema 不变；必要时按命令保留内联 |
| R4 | 忙时白名单扩展造成租约争用 | 租约等待超时计数上升 | 中 | 保持 2s 预算 + fail-closed 降级；`AICLI_CHAT_RUNTIME_INTERACTION` 灰度收窄 |
| R5 | 命令类别事实源迁移漂移 | T11 守卫测试失败 | 中 | 单一事实源 + 守卫测试（§5.2(4)）；迁移期双读校验 |
| R6 | 旧 `Open*` 字段残留形成双通道 | A2 守卫（框架外调用点）失败 | 中 | 批次 5 删除；迁移期映射层集中一处 |
| R7 | 兼容矩阵外环境（tmux / winpty）异常 | 兼容矩阵测试失败或用户反馈 | 中 | 能力门 fail-closed + 内联降级（§4.5）；runbook（§8.3） |
| R8 | panic / 错误路径跳过 release 造成 TTY 锁死 | `violation` 事件；后续无法开副屏 | 高 | defer 保证（§4.2）+ T8/T9 测试 + runbook 回退开关 |

## 10. 决策记录（ADR，已确认）

> 本节 6 条决策均已**确认**（Accepted），随本计划批准生效；要改结论，按 §10.7 追加 `Superseded by …` 记录，不允许静默改行为。正文中的 `D-x` 均指本节。

### 10.0 决策索引

| # | 决策（结论） | 状态 | 生效批次 | 主要验证 | 回退口径 |
| --- | --- | --- | --- | --- | --- |
| D-A | `/help` 迁移到副屏只读文档页 | 已确认 | 批次 3 | T1/T2 + 字符级 diff | 框架开关回退；或 catalog override 保留内联 |
| D-B | 副屏内 Esc 默认一次回主屏（`TopLevelClose`）；搜索态为唯一例外 | 已确认 | 批次 0 契约、1–2 落地 | T5 / T5b | `StepBack` 不启用；例外需白名单登记 |
| D-C | 忙时 Esc 保留中断，并修复草稿被清空（对齐空闲态语义） | 已确认 | 批次 0（独立提交） | T6 / T6a | 单独 revert 该提交，不影响框架 |
| D-D | 禁止副屏嵌套（阶段在单租约内推进），`ErrScreenNested` | 已确认 | 批次 0 | T8 + 守卫 A2 | 无开关；新用例另立 ADR |
| D-E | `Screen` 与旧 `Open*` 并存至批次 5 再删除 | 已确认 | 批次 0–5 | A2 + 映射穷尽测试 | 批次 0–4 天然可回退；批次 5 单点 revert |
| D-F | 降级一律主屏内联单元格，不落 legacy stdout | 已确认 | 批次 0 | T3 + degrade 指标 A3 | 降级路径本身即回退 |

### 10.1 D-A：`/help` 从主屏内联迁移到副屏只读页

- **决策**：确认迁移（`ScreenDocument`），随批次 3 落地；55 条命令目录与 Shell 段整体进入可滚动页。
- **背景/证据**：现状 `/help` → `printChatCommandOutput`（`chat_slash_help.go:67-68`），输出约 67 行 = 2 行表头 + 55 条非隐藏命令 + Shell 段 9 行（`chat_slash_help.go:15-65`；目录条数 `chat_slash_command_catalog.go`），静态、查完即走、无需与后续对话并排阅读。
- **理由**：符合 §5.1 准入（超过半屏的静态文档 → 副屏只读）；与 `/usage`、`/accounts` 等只读页同契约；主屏消息流不再被长文档淹没；顺带收编 legacy 直写（§2.5(c)）。
- **备选与否决**：(a) 保持内联——否决：与 §5.1 判据自相矛盾，且继续保留 legacy 通道；(b) 主屏内折叠/分页——否决：在主屏再造一套分页，与框架重复，违反 G1。
- **验证**：T1/T2 键序（`/help` → 副屏 → Esc → 主屏）；输出与现状**字符级 diff**（仅承载通道变化、内容一致）；degrade 场景归 D-F。
- **回退口径**：`AICLI_CHAT_SCREEN_FRAMEWORK=legacy` 回退旧内联路径（旧路径保留至批次 5）；如需长期内联，登记为 catalog override 白名单项。
- **影响清单**：§5.2(2)、§6 批次 3、legacy 直写收编、`/help` 文案补「Esc 返回」。

### 10.2 D-B：副屏内 Esc = 一次回主屏（默认 `TopLevelClose`）

- **决策**：`EscPolicy` 默认 `TopLevelClose`：一次 Esc 关闭副屏、释放租约、回主屏。**唯一例外：列表搜索态**——Esc 先清空查询词，查询为空时退出搜索态，退出后的下一次 Esc 才关闭（现状即如此）。`StepBack`（逐级返回）保留为扩展点，首期任何命令不得启用。
- **背景/证据**：现状三族副屏 Esc 均直接关闭（A 族 `fullscreen_list.go:513-514`；B 族 `debug_overlay.go:261-267`）；搜索态例外见 `fullscreen_list.go:478-499`（Esc 清查询/退出搜索）与 footer 文案 `:843-847`。
- **理由**：默认值 = 现状行为（A4 行为不变性）；单层租约（D-D）下 Esc 逃逸路径最短、最可预期；搜索态例外保护正在输入的筛选词，且不引入屏内阶段栈。
- **备选与否决**：(a) 逐级返回——否决：现状无此语义，需屏内阶段栈与每级取消定义；A 族多阶段本就在单租约内推进（`chat_model_picker.go:247-318` 复用同一租约），逐级返回会改变完成率与键位，且与目标流程冲突；(b) 双 Esc（首 Esc 逐级、二 Esc 关闭）——否决：状态记忆不可测、不可观测，易出"吞 Esc"缺陷（R8 类）。
- **验证**：T5（逐命令一次 Esc 回主屏、租约释放、primary repaint 完整）；T5b（搜索态键序；多阶段任一阶段一次回主屏）。
- **回退口径**：`StepBack` 与例外状态位默认关闭；现场若需逐级，先补测试再走 §10.7 变更。
- **影响清单**：§4.1 `EscPolicy`、§4.7 I4、§5.2 清单、T5/T5b；**§11 待复核项 1 由本决策关闭**。

### 10.3 D-C：主屏忙时 Esc = 保留中断 + 修复草稿被清空

- **决策**：保留"忙时 Esc 中断当前回合"；同时修复普通忙时输入行（`trackPrompt=true` 且非 approval/question）的草稿丢失——`chatBusyComposerCapture.onCancel(snapshot)` 暂存快照，`PreserveDraft()` 先按快照恢复输入再清屏，使中断后草稿仍可继续编辑。审批/提问行语义不变（其主草稿本就 parked 保护，`chat_composer.go:431-460`）。
- **背景/证据（已核实的不一致）**：忙时 `onCancel` 恒返回 true（`chat_composer.go:543-548`）→ 编辑器随后 `onChange("")` 清空语义草稿（`ui/inputbox_editor.go:1304-1308`）→ 中断路径的 `PreserveDraft()` 已无草稿可保；空闲态同一编辑器明确要求 "Keep typed drafts untouched so Esc never silently discards composer text"（`ui/inputbox_editor.go:1313-1320`）。
- **理由**：取消操作不应销毁用户输入（readline/zsh 通用约定）；同一 TUI 内空闲/忙时行为必须一致；代码注释已声明意图，本例属**bug 修复**而非口味变更；改动面最小。
- **备选与否决**：(a) 保持现状——否决：用户输入不可恢复，违反既有注释契约；(b) 只清草稿不中断——否决：违反 Esc 中断契约（`esc-interrupt-priority-and-loop-robustness-plan-20260918.md`）；(c) 改编辑器公共路径（所有 `OnCancel` 后不再 `onChange("")`）——否决：modal/merged/agent-panel/selection 四类 composer 依赖该清理语义（`chat_composer.go:627-632`、`:760-765`、`:882` 起、`chat_runtime_selection_nav.go:173` 起），冲击面过大。
- **验证**：新增 T6a（忙时输入半截草稿 → Esc → 断言回合已中断 + 草稿仍在 + 下次 capture `InitialText` 一致）；T6（中断语义 + 副屏内 Esc 不触达中断路径）；回归 `chat_composer_test.go:301-342`（跨 prompt 切换/回合结束的草稿保留）。
- **回退口径**：本修复作为批次 0 内**独立提交**，可单独 revert（框架开关不控制它，PR 说明中单列）。
- **影响清单**：`chat_composer.go:543-548`、`chat_busy_input.go:115-122` 注释、T6/T6a、§4.7 I5。

### 10.4 D-D：禁止副屏嵌套

- **决策**：一层副屏；跨阶段用 `ScreenStages` 在同一租约内推进；副屏内再次请求开屏 → 立即 `ErrScreenNested`（不排队、不阻塞）。
- **背景/证据**：`ScreenLease` 为单所有者独占凭据（`ui/screen_lease.go:110-132`）；§2.4 全部调用点都假设"进入前无租约"；现有 picker 已证明单租约多阶段可行（`chat_model_picker.go:247-318`）。
- **理由**：alt-screen 是进程级单例资源；嵌套需要租约栈 + 多层回退 repaint 协议，收益低、TTY 锁死风险高（R8）；与上位规范 single screen owner 一致。
- **备选与否决**：(a) 允许嵌套——否决：无真实用例；panic/取消路径的释放顺序难以证明。
- **验证**：T8（副屏内请求开屏 → `ErrScreenNested`，原租约与屏内容不受损）；守卫 A2（框架外 `AcquireAlternateScreen` 计数 = 0）。
- **回退口径**：无开关（结构性约束）；未来出现真实需求时另立 ADR + 租约栈设计。
- **影响清单**：§4.2 重入行、§4.7 I9、§5.2 多阶段命令编排。

### 10.5 D-E：`Screen` 与旧 `Open*` 并存至批次 5

- **决策**：批次 0 引入 `CommandResult.Screen`，旧 `Open*` 字段保留并**集中映射**进新框架（单一映射函数、行为不变）；批次 5 删除旧字段。
- **理由**：扩张-收缩（expand/contract）迁移，逐批可独立回退（G5/R6）；避免一次性重写十余个消费点导致不可二分定位的回归；JSON/plain 投影在此期间保持兼容（§3.2）。
- **备选与否决**：(a) 批次 0 直接删旧字段——否决：破坏兼容投影与回退能力，任一批出问题只能整体 revert。
- **验证**：A2 守卫（框架外调用点 = 0）；映射穷尽测试（`chat_command_result.go:127-199` 每个旧字段至少一条映射用例）；JSON 金样本不变。
- **回退口径**：批次 0–4 天然可回退（旧字段仍在）；批次 5 的删除是单点 revert（恢复字段定义与消费点）。
- **影响清单**：`chat_command_result.go:127-199`、§6 批次 0/5、A2、R6。

### 10.6 D-F：降级形态 = 主屏内联文档单元格

- **决策**：能力不足（`CanUseFullScreenList` / `OwnedViewport` 不可用）、租约忙（2s 预算超时）或框架内部错误时，一律降级为主屏内联文档单元格 + 原因提示；**绝不落 legacy stdout 直写，绝不静默吞掉输出**。
- **理由**：fail-closed 单通道（I6）；legacy 直写绕过 Scene/事务式 frame 契约，无结构化上下文、不可测试，是三轨分裂的历史根源（§2.1、`chat_command_output.go:31`、`chat_command_text_writer.go:17`）；内联降级用户仍可完整阅读内容（文档 → Blocks）。
- **备选与否决**：(a) legacy stdout 直写——否决：复活旁路通道，观测/测试/回滚全部失效；(b) 静默忽略——否决：用户无反馈，违反 I6。
- **验证**：T3（能力关闭 → 输出内联 + `degrade` 事件含 reason + 退出码/stderr 不受影响）；A3 计数；超长文档截断提示的有界测试。
- **回退口径**：降级路径本身即最终回退；更深一层即 legacy，已否决。
- **影响清单**：§4.5 降级矩阵、§4.7 I6、§8.1 事件、runbook §8.3。

### 10.7 决策一致性检查与变更流程

- **依赖关系**：D-B 以 D-D 为前提（单租约使"一次 Esc 回主屏"无歧义）；D-E 是 D-A/D-B/D-F 的落地方式（批次 0 并存、批次 5 收敛）；D-F 与 D-B 组合后，降级态（内联单元格）不参与 Esc 路由；D-C 与 D-B 互不耦合（分别覆盖主屏忙时与副屏内），由 T6/T6a 分别锁定。
- **一致性**：D-A 与 D-C 无冲突（通道不同）；D-A × D-F = 副屏不可用时 `/help` 内联展示全部内容。
- **变更流程**：任何决策要改，先在本节追加 `Superseded by <新决策>`（保留原文），再更新受影响正文与测试；禁止静默改行为。

## 11. 附录：证据来源与待复核

- 证据来源：§2.4 / §2.5 / §5.2 来自两个只读盘点子任务（命令通道盘点、副屏基础设施盘点），关键行号已随文标注。
- 待复核（不阻塞批次 0-2 动工）：
  1. ~~`fullscreen_list.go` 搜索态下 Esc 行为~~ —— **已关闭（D-B 已确认）**：搜索态 Esc 先清查询、查询为空时退出搜索，退出搜索后的下一次 Esc 才关闭副屏（`fullscreen_list.go:478-499`；footer 文案 `:843-847` 与此一致）；该例外已固定进 T5b。
  2. `ui.RunTranscriptPagerWithLease` 的 Esc/q/翻页键位未展开。
  3. MCP / Account（单、全）/ Transcript 三处的 `CommandResult` 字段 → 打开函数调度行未逐一取到（打开函数入口已确认）。
  4. `OpenDebugOverlay` 字段定义行号未取到（消费点 `chat_debug_overlay.go:36/52` 已确认）。
  5. `ScreenDocument` 的文档分页复用路径（`RunDebugOverlayWithLease` vs 现有文档分页器）——批次 0 定稿前确认，决定框架内部编排哪条原语。
  6. tmux / winpty 下 `CanUseFullScreenList` 与 `OwnedViewport` 的实测行为（兼容矩阵，§7.3）。
  7. `ErrFullScreenUnavailable` 在 unified surface 下的实际返回路径（无 presenter / disabled 分支）——批次 0 前确认，用于 T4 投影一致性断言。

### 11.1 术语表

| 术语 | 含义 |
| --- | --- |
| ScreenLease | `ui.ScreenLease`：alternate screen 独占所有权凭据；持有期内 primary 不得 flush 字节（`screen_lease.go:110-132`） |
| ScreenOwner | 任一时刻唯一被允许写 TTY 的组件（上位渲染规范） |
| retained state / repaint | 主屏在租约期的保留状态；release 后据此完整重绘（草稿不丢） |
| lease fence | 用 lease id 校验输入/写入，拒绝陈旧租约的延迟事件 |
| fail-closed | 出错或能力不足时降级为主屏内联，绝不改走 legacy 旁路、绝不静默吞错 |
