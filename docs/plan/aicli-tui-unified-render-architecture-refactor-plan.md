# aicli TUI 统一 Scene、单屏所有者与事务式渲染长期重构设计

状态：**in progress（长期架构基线；P0/P1/P3 已有部分实施，终局 Scene 架构仍待迁移）**

更新时间：**2026-08-01**

适用范围：`backend/cmd/aicli/commands`、`backend/cmd/aicli/ui` 及所有在 chat interactive 生命周期内产生可见输出的 runtime/tool/diagnostic 组件。

关联文档（按职责分层）：

- 当前 owned viewport 实施基线：`docs/plan/aicli-tui-p5-owned-viewport-design.md`；
- 渲染面/数据面迁移母计划：`docs/plan/aicli-tui-render-data-plane-codex-migration-plan.md`；
- ActiveBand/scroll compensation 专项历史：`docs/plan/aicli-activeband-scrollback-compensation-blank-lines-fix-plan.md`；
- 内容、样式、Markdown、Diff 与结构化渲染 IR：`docs/plan/aicli-ui-ux-rendering-codex-reference-plan.md`；
- 早期 UI 组件化与交互阶段规划：`docs/plan/aicli-ui-refactor-codex-inspired-plan.md`；
- 2026-07-29 内容渲染实施审查：`docs/plan/aicli-ui-rendering-implementation-review.md`；
- Phase 0 渲染路径和组件清点：`docs/plan/aicli-ui-rendering-phase0-inventory.md`。

> 本文不是 `/debug display` 的局部修复说明，也不否定已有 P1–P5 工作。P5 文档继续作为 owned viewport、ActiveBand、history handoff 和 P5.6 gap 行为的当前实施真相；UI/UX reference 与 implementation review 继续作为内容渲染 IR、主题和安全边界的真相。本文在这些成果之上定义跨模块的长期终局：物理屏幕所有权、Scene 数据模型、事件与 frame 事务、history/cell/gap 规则、fullscreen 生命周期、scrollback handoff 以及旧路径删除计划。若历史文档中的过渡方案与本文强不变量冲突，以“同一交互会话只有一个物理屏幕所有者”为最终收敛方向。

---

## 实施状态（2026-08-01）

本文的终局 `TuiScene -> Layout/Compose -> TuiPresenter` 尚未整体落地；以下状态仅记录已验证的实施切片，不能将过渡 adapter 误读为终局 single-writer 已完成。

| 阶段 | 当前状态 | 已验证证据 | 仍待完成 |
| --- | --- | --- | --- |
| P0 旁路审计 | **进行中** | `TestChatInteractiveDirectWriterInventory` 以 AST 扫描 `commands/chat*.go` 与 `command.go`，固化 **155 个 grouped debt entries / 550 个 call sites**（491 `fmt.Print*`、49 `fmt.Fprint*(os.Std*)`、1 `io.WriteString(os.Std*)`、9 `ui.WriteTerminal*`）；新增 group 或 count 变化默认使测试失败，并报告实际行号。`/debug display`、`/status`、`/load`、`/goal`、`/memory`、`/stream` 与 `/title`/`/rename` 成功路径已有 raw-stdout/structured producer fence 及 atomic command-cell 回归；`TestChatSystemOutputWriter_ActiveTurnMirrorSurvivesOwnedViewportRepaint` 覆盖 active-turn mirror 在 status/ActiveBand 重绘后仍只出现一次。`functions/builder.go` 的遗留 function-call parser 已删除全部 direct writer，并有 source fence 与 malformed-call 语义测试。 | 每条 chat/command debt entry 标注 owned-safe/plain-only/startup-shutdown/待迁移 owner；补 owner/frame trace；迁移后逐项从基线删除。 |
| P1 CommandResult | **部分完成** | `/debug display`、`/status`、成功 `/load <session-id>`、`/goal`（status/clear/pause/resume/complete/set）、`/memory`（status/add/list/search）、`/stream`（status/toggle/set）与成功 `/title`/`/rename` 已以结构化 `CommandResult` 进入单个 command cell（另有 `chat_title_document.go`）；handler 禁止 direct terminal writer 的测试已覆盖全部新式 producer。`/goal <objective>` 在确认 cell 提交后经 `CommandResult.SendObjective` 走正常 send pipeline 触发 AI 目标请求；`/stream` toggle 复用既有 `persistStreamCommandPreference`；title mutation 与成功确认 cell 共用 side-effect boundary。参数错误、持久化/store 错误、`--json` 变体与 nil session 仍走 legacy 路径，以使错误在全部输出模式中可见；成功 `/load` 在确认 cell 后通过 `CommandResult.ReplayHistory` 逐消息回放历史。 | 迁移 `/skills`（selection/modal 交互，留待 order-3 桶）及其余 slash command，删除 `beginDirectInteractiveOutput` 作为通用命令协议。 |
| P3 ScreenLease | **首批完成** | resume、backtrack、theme fullscreen picker 已通过 `ScreenLease` 取得 alternate screen；主屏 flush suspend、release full repaint、DEC 1049 事务边界均有测试。 | 将 lease 上移到最终 presenter API，补 signal safety 与完整失败注入矩阵。 |
| P4–P9 Scene 终局 | **未开始整体切换** | owned viewport、front/back diff、history window、ActiveBand 与部分 reflow/handoff 能力已存在。 | SceneController、semantic cell/revision、BoundaryPolicy、统一 runtime/tool event writer、legacy 删除及 PTY/ConPTY 验收。 |

P0 inventory 是迁移债务账本和 regression fence，**不是**对 raw terminal writer 的授权；它刻意按 file/function/kind/count 分组，避免普通源码行号移动造成基线漂移，同时保留实际行号用于失败诊断。旧的 [Phase 0 inventory](./aicli-ui-rendering-phase0-inventory.md) 仅是内容渲染时期的抽样记录，不是这个 owned-interactive 门禁的第二份 allowlist。

---

## 0. 执行摘要

当前 TUI 已经具备 owned viewport、front/back diff、ActiveBand、prompt、popup、history window 等 retained-mode 能力，但生产路径中仍长期保留 immediate-mode 输出：slash command、runtime diagnostics、tool builder 或 library 直接调用 `fmt.Print*`、`os.Stdout`、`os.Stderr` 或 terminal primitive。两类路径写入同一个主终端，却只在字节层共享锁，不共享 history、layout、cursor、front buffer、handoff frontier 和 frame generation。

这导致的症状不是彼此独立的偶发缺陷，而是同一个所有权缺失问题的不同表现：

- 历史消息重复追加、finalize 后再次绘制；
- raw 输出出现在旧消息、ActiveBand 或 prompt 中间；
- 下一帧覆盖不在 Scene 中的文字；
- user/assistant/event/tool/supplement/reasoning block 的 gap 不一致；
- ActiveBand grow/shrink 后永久留下空洞；
- fullscreen 使用 `Disable()/Enable()` 后 retained history 和 UI state 丢失；
- resize/reflow、native scrollback handoff 和 soft output 各自维护“已输出”状态，真相源分裂；
- 旧 immediate renderer 与新 retained renderer 同时存在，修复一条路径后其他入口仍可复现。

长期目标不是把所有 UI 状态塞进一个无边界的大对象，而是建立以下架构约束：

```text
多个业务生产者
  -> 一个有序 RenderEvent 队列
  -> 一个权威 TuiScene
  -> 一个 Layout/Compose 流程
  -> 一个 TuiPresenter
  -> 一个物理 TerminalWriter
```

允许多个逻辑 layer；不允许多个 physical screen writer。允许 JSON、pipe、noninteractive 使用 plain renderer；但一个会话生命周期只能选择一种 renderer mode。允许 fullscreen 使用 alternate screen；但必须通过排他的 screen lease 获取物理终端所有权，不能销毁主 Scene。

本文的核心决策为：

1. `TuiScene` 是交互 UI 的唯一状态真相，终端屏幕和 Backend front buffer 都不是业务数据源；
2. 所有永久 transcript 输出都以有稳定身份的 semantic cell 存储，显示行由 `DisplayLines(width)` 派生；
3. cell 内部默认稠密，cell 之间的 gap 由单一 boundary policy 计算，gap 不由调用点自行拼接空行；
4. mutable cell 的 update/finalize 是 replace/commit transaction，不得通过“再 append 一份 final 文本”完成；
5. 每个逻辑 block 通过一次 Scene transaction 提交，每个 frame 通过一次 presenter transaction flush；
6. 只有持有 screen ownership 的 presenter 能输出 ANSI 和终端字节；
7. fullscreen 使用 alternate-screen lease；lease 期间主 Scene 可继续更新但暂停主屏 flush，释放后 invalidation + full repaint；
8. handoff frontier 单调前进，同一个 cell/revision/row range 最多向 native scrollback 交接一次；
9. replay 与 live 使用相同的 cell sequence、boundary policy、layout 和 presenter；
10. 迁移完成后删除 production legacy renderer、gap 布尔补偿状态机和 fullscreen `Disable()/Enable()` 暂停路径。

---

## 1. 当前问题与根因

### 1.1 当前的两条主输出路径

正确的 retained 路径：

```text
Chat/Event/Command Result
  -> chatInteractionCoordinator / scene adapter
  -> FixedBottomSurface.WriteOutput
  -> historyWindow / retained state
  -> viewport.Compose
  -> Backend front/back diff
  -> Terminal
```

高风险 immediate 路径：

```text
Handler/Library/Runtime
  -> fmt.Print* / os.Stdout / os.Stderr / direct terminal write
  -> 当前物理光标
  -> Terminal
```

raw 路径不会同步：

- retained history；
- Backend front buffer；
- 当前 Scene revision；
- layout 与 bottom reserve；
- cursor intent；
- soft/mutable cell revision；
- native scrollback handoff frontier。

因此 terminal mutex 只能防止字节交叉，不能使两个 renderer 对屏幕状态达成一致。任何一次 status tick、prompt repaint、ActiveBand resize、popup 开关或 terminal resize，都可能让 retained presenter 覆盖、移动或重复 raw 输出。

### 1.2 已确认的具体架构风险

1. `FixedBottomSurface.WriteOutput` 会将 owned output 追加到 `historyWindow` 并重新渲染 viewport；raw stdout 不经过该流程。
2. viewport Backend 使用 front/back diff；任何旁路终端 mutation 都会使 front buffer 与物理屏幕失真。
3. `chatInteractionCoordinator.writeRowsLocked` 已证明完整 block 必须一次多行写入；逐行释放 surface lock 会允许 ActiveBand/status 尺寸变化插入永久空洞。
4. `completeBlockOutput` 和多个 `gapFor*` helper 仍体现“根据前一次调用推断 gap”的历史模型；长期应被 cell boundary policy 取代。
5. `Disable()` 会清空 popup、composer、prompt、ActiveBand、status、Backend 和 retained history；它是 destructive teardown，不是 fullscreen suspend。
6. 当前 history window 同时承担可见历史、restore headroom、soft rewrite 和 native scrollback handoff，cell identity 与 row identity 不完整，容易出现二次提交或错误 suffix replace。
7. runtime/tool 的部分 writer 已 surface-aware，但仍存在直接 stdout/stderr 的 active-turn diagnostics，说明输出契约尚未在模块边界强制执行。

### 1.3 当前代码锚点

以下路径是实施审计和首批迁移的主要入口；行号可能随实现变化，评审时以符号名为准：

| 关注点 | 当前代码锚点 | 长期归属 |
| --- | --- | --- |
| surface/Layout/InputBox 初始化 | `backend/cmd/aicli/commands/chat_setup.go` | renderer mode bootstrap |
| chat transcript 路由 | `backend/cmd/aicli/commands/chat_transcript_renderer.go` | RenderEvent adapter |
| 完整 block 原子写入与现有 gap helper | `backend/cmd/aicli/commands/chat_interaction.go` | Scene transaction + BoundaryPolicy |
| owned history、soft output、handoff、Disable | `backend/cmd/aicli/ui/fixed_bottom_surface.go` | Scene/Presenter/Handoff 分拆 |
| viewport 合成 | `backend/cmd/aicli/ui/viewport/compose.go` | Compositor |
| front/back diff | `backend/cmd/aicli/ui/viewport/backend.go` | TuiPresenter |
| 过渡期 surface-aware direct output | `backend/cmd/aicli/commands/chat_surface_output.go` | CommandResult adapter |
| `/debug display` raw 输出 | `backend/cmd/aicli/commands/chat_debug.go` | 首批 CommandResult 迁移 |
| active-turn tool diagnostics | `backend/cmd/aicli/functions/builder.go` | logger/RenderEventWriter |
| alternate screen frame | `backend/cmd/aicli/ui/fullscreen_list.go` | fullscreen presenter + lease |
| fullscreen 调用者 | `backend/cmd/aicli/commands/chat_resume_command.go`、`backend/cmd/aicli/commands/chat_backtrack_select.go`、`backend/cmd/aicli/commands/chat_theme_command.go` | 统一 ScreenLease |

### 1.4 症状到根因的映射

| 症状 | 直接原因 | 深层根因 |
| --- | --- | --- |
| `/debug display` 嵌入消息流中间 | handler 直接写当前物理光标 | command 没有返回 Scene mutation；主屏有多个 writer |
| 下一帧覆盖命令输出 | Backend front 不包含 raw 文本 | front buffer 与物理屏幕分裂 |
| final assistant/tool 重复绘制 | mutable 内容与 final 内容分别 append | 缺少稳定 cell ID、revision 和原子 finalize |
| event/tool block gap 时有时无 | 调用点拼空行或读取历史布尔标志 | gap 不是 boundary domain model |
| ActiveBand 变化后历史出现洞 | 多行 block 分次输出或 bottom reserve 中途改变 | logical block/frame 不是事务 |
| resize 后行序、换行或重复异常 | 以旧 display rows 推断 source | semantic cell 不是唯一真相源 |
| fullscreen 返回后历史丢失 | 使用 `Disable()/Enable()` | 没有 screen lease 与 suspend/resume 状态机 |
| scrollback 重复/漏行 | retained rows 和 handed-off rows 缺少稳定交接记录 | handoff frontier/record 不完整 |

### 1.5 为什么不能继续做局部补丁

只把 `/debug` 改成 surface-aware block 可以修复当前命令，但不能阻止新的 handler、library 或 runtime callback 再次旁路。继续增加 `lastXXX`、`alreadyPrinted`、`promptAfterXXX` 或 scroll compensation 布尔字段，会把终端物理状态反向变成业务真相，导致状态组合指数增长。

长期方案必须先确立所有权和数据模型，再迁移入口，最后删除旧模型；不能长期让两个 production renderer 双写并以视觉结果“碰巧一致”作为正确性标准。

---

## 2. 目标、非目标与成功定义

### 2.1 长期目标

- 交互会话中所有可见内容都可追溯到一个 Scene node/cell 或 overlay state；
- 同一时刻只有一个组件拥有主终端/alternate screen 的物理写权限；
- history、mutable turn、ActiveBand、status、prompt、popup 的职责和生命周期清晰；
- live、replay、resume、resize 使用同一 cell/layout/boundary 逻辑；
- block 不重复、不覆盖、不乱序，gap 可由规则表确定；
- fullscreen 不销毁主 Scene，退出后可从权威状态完整恢复；
- scrollback handoff 可证明 exactly-once，retained tail 可安全 reflow；
- 迁移后能删除旧 immediate production path 和补偿状态机，而不是永久兼容。

### 2.2 非目标

- 不改变 JSON schema、pipe 输出和独立非 chat CLI command 的 plain-output 语义；
- 不要求应用无限期保留所有已交给 native scrollback 的 display rows；
- 不将 popup、status、prompt 写进 transcript；
- 不以第三方 TUI 框架替换全部现有代码；可以复用现有 viewport、render、VT 和 terminal abstraction；
- 不在一个阶段内大爆炸式重写所有 command 和 UI 组件；采用可验证的小切片迁移；
- 不通过全局重定向 `os.Stdout` 掩盖违规调用，最终仍需在 API 和 CI 层消除旁路。

### 2.3 成功定义

完成态必须同时满足：

1. owned interactive 模式下，production 代码不存在未授权终端写入；
2. 一个逻辑 cell 在 live、finalize、replay、resize、handoff 后仍保持稳定身份；
3. gap 由一处纯函数/规则表生成，业务调用者不插入语义空行；
4. fullscreen、popup、prompt、status 更新不改变 transcript 序列；
5. terminal partial write、resize storm、cancel/panic 后可 invalidation 并从 Scene 恢复；
6. 真实 Windows ConPTY 与至少一种非 Windows ANSI terminal 通过验收；
7. legacy production renderer 和旧补偿状态机有明确删除证据。

---

## 3. 设计原则与强不变量

以下不变量属于架构门禁；实现阶段不得以“当前视觉正常”为理由绕过。

### 3.1 所有权不变量

- **INV-OWNER-01**：owned interactive 会话中，只有当前 `ScreenOwner` 可以调用 terminal byte/ANSI primitive。
- **INV-OWNER-02**：主屏 presenter 与 fullscreen presenter 不能同时持有 screen lease。
- **INV-OWNER-03**：terminal write lock 只在 owner 内部用于 frame 字节原子性，不能作为多 renderer 共存协议。
- **INV-OWNER-04**：plain、JSON、owned renderer mode 在 session 初始化时选定；进入 owned 生命周期后不能临时降级为 raw stdout，再恢复 owned。

### 3.2 Scene 与 history 不变量

- **INV-SCENE-01**：`TuiScene` 是交互 UI 的唯一权威状态；物理屏幕、VT snapshot、Backend front 都是派生缓存。
- **INV-SCENE-02**：每个 transcript cell 有不可复用的 `CellID` 和单调递增 `Sequence`。
- **INV-SCENE-03**：mutable update 必须携带匹配的 `CellID` 与更大的 `Revision`；旧 revision 不得覆盖新 revision。
- **INV-SCENE-04**：finalize 是同一 cell 的状态迁移，不是 append 新 cell。
- **INV-SCENE-05**：prompt、status、popup、ActiveBand 不属于 transcript，不改变 transcript sequence 或 boundary state。

### 3.3 block 与 gap 不变量

- **INV-GAP-01**：cell 内没有隐式首尾空行；内容本身的空行属于 cell source。
- **INV-GAP-02**：独立 top-level transcript cells 之间最多一个语义 gap。
- **INV-GAP-03**：gap 由 `BoundaryPolicy(prev, next)` 生成，不能由 renderer 调用点逐行推断。
- **INV-GAP-04**：mutable update、ActiveBand redraw、resize、replay 和 handoff 不改变既有 cell boundary。
- **INV-GAP-05**：空 block、被过滤事件和无可见内容的 update 不推进 boundary state。

### 3.4 frame 与失败恢复不变量

- **INV-FRAME-01**：一个 Scene transaction 要么完整应用，要么不应用；不能提交半个 tool/event block。
- **INV-FRAME-02**：一次 presenter frame 只能基于一个不可变 Scene snapshot 和一个 terminal size generation。
- **INV-FRAME-03**：front buffer 只在 terminal flush 全部成功后更新。
- **INV-FRAME-04**：partial/failed write 后 front buffer 必须失效，下一次使用 full repaint；不得继续基于未知物理屏幕做 diff。
- **INV-FRAME-05**：frame 完成后的物理 cursor 必须等于 snapshot 中的 `CursorIntent`。

### 3.5 scrollback handoff 不变量

- **INV-HANDOFF-01**：handoff frontier 单调前进，不能回退到已交给 native scrollback 的 range。
- **INV-HANDOFF-02**：同一 `CellID + Revision + DisplayRange + LayoutGeneration` 最多 handoff 一次。
- **INV-HANDOFF-03**：只有 committed、不可再 mutation 的内容可以 handoff。
- **INV-HANDOFF-04**：handoff 不产生新 cell、不改变 gap，也不能再次走普通 append 路径。
- **INV-HANDOFF-05**：resize 只 reflow retained tail；已进入 native scrollback 的物理行视为不可重排。

---

## 4. 目标架构与职责边界

### 4.1 总体数据流

```text
 User Input   Chat Runtime   Tool Runtime   Slash Command   Diagnostics
     │             │              │               │              │
     └─────────────┴──────────────┴───────────────┴──────────────┘
                                   │
                          RenderEvent / CommandResult
                                   │
                         bounded ordered UI queue
                                   │
                            SceneController
                     validate -> reduce -> transaction
                                   │
                                TuiScene
     ┌──────────────────────────────────────────────────────────────┐
     │ Transcript Cells / Mutable Cells / Handoff State            │
     │ ActiveBand / Status / Prompt+Editor / Popup Stack            │
     │ Cursor Intent / Theme / Dimensions / Generations            │
     └──────────────────────────────────────────────────────────────┘
                                   │ snapshot
                              LayoutEngine
                    semantic cells -> width-aware rows
                                   │
                               Compositor
                 history + bottom layers + overlays + cursor
                                   │ Frame
                              TuiPresenter
                   back buffer -> diff -> atomic terminal flush
                                   │
                    ScreenLease + TerminalWriter (single owner)
                                   │
                               Physical TTY
```

### 4.2 组件职责

| 组件 | 负责 | 不负责 |
| --- | --- | --- |
| `SceneController` | 串行消费事件、校验 revision、事务式修改 Scene | ANSI、终端坐标、直接输出 |
| `TuiScene` | semantic cells、overlay state、生命周期与 generation | 终端 I/O、diff 算法 |
| `LayoutEngine` | 按 width/theme 将 source 转为 display rows，测量各 layer | 修改 Scene、写终端 |
| `Compositor` | 将可见 history、ActiveBand、status、prompt、popup 合成为 frame | 业务事件排序、stdout |
| `TuiPresenter` | front/back、diff、cursor、同步更新、flush、失败失效 | 业务 cell gap 决策 |
| `ScrollbackHandoff` | 选择 eligible committed range、记录 exactly-once frontier | mutable cell、prompt/popup |
| `ScreenLeaseManager` | primary/alternate 所有权与 suspend/resume | 清空 Scene |
| `PlainRenderer` | JSON/noninteractive/pipe 的顺序输出 | owned interactive 主屏 |

### 4.3 renderer mode

```go
type RendererMode uint8

const (
    RendererOwnedInteractive RendererMode = iota
    RendererPlain
    RendererJSON
)
```

选择规则：

- interactive + ANSI/terminal capability 满足：`RendererOwnedInteractive`；
- pipe、`NoInteractive` 或 capability 不满足：`RendererPlain`；
- JSON output：`RendererJSON`。

每种 mode 使用各自完整 pipeline。禁止同一 session 中以 `fmt.Print*` 临时穿透 owned presenter。若 capability 在运行期失效，应执行明确的 controlled teardown/degrade：先停止事件接收或切换到可重建状态，不能让两个 renderer 重叠工作。

### 4.4 建议的包边界

可在现有目录中渐进拆分，命名仅为建议：

```text
ui/scene/        semantic state、cell、boundary、event reducer
ui/layout/       width-aware DisplayLines 与 pane measurement
ui/viewport/     frame compose 与 front/back backend
ui/presenter/    terminal flush、cursor、frame generation
ui/screenlease/  primary/alternate ownership
commands/        CommandResult 与 scene adapter，不含 ANSI/stdout
```

重构期间可保留 `FixedBottomSurface` 作为 facade，但其内部职责必须逐步委托给上述组件；最终 facade 不再同时维护业务状态、terminal mutation 和补偿状态机。

---

## 5. Scene 数据模型

### 5.1 顶层 Scene

```go
type TuiScene struct {
    Revision        uint64
    EventSequence   uint64
    Transcript      TranscriptState
    ActiveBand      ActiveBandState
    Status          StatusState
    Prompt          PromptState
    Popups          PopupStack
    Cursor          CursorIntent
    Theme           ThemeState
    Terminal        TerminalState
    Fullscreen      FullscreenState
}
```

职责边界：

- `Transcript` 是持久语义历史；
- `ActiveBand` 表示当前运行中、允许高频更新但尚未提交到 transcript 的活动区域；
- `Status` 是瞬时状态，不参与历史顺序；
- `Prompt`/editor 是输入状态，不参与 transcript；
- `Popups` 是 overlay stack，不修改下层数据；
- `Cursor` 是 frame 输出意图，不从物理 cursor 反推；
- `Terminal` 只保存 width/height/capability/generation，不保存业务文本。

### 5.2 Transcript cell

```go
type CellID uint64

type TranscriptCell struct {
    ID          CellID
    Sequence    uint64
    Kind        CellKind
    Source      RichDocument
    Revision    uint64
    Phase       CellPhase
    Boundary    BoundaryClass
    Provenance  Provenance
    CreatedAt   time.Time
    FinalizedAt *time.Time
}

type CellPhase uint8

const (
    CellMutable CellPhase = iota
    CellCommitted
    CellPartiallyHandedOff
    CellHandedOff
)
```

`Kind` 至少区分：

- user；
- assistant；
- tool chain；
- runtime event；
- supplement/reasoning；
- system/notice/warning；
- command result；
- diagnostic（仅显式允许进入交互 transcript 的诊断）。

`Source` 保存语义内容或 render document，而不是终端换行后的字符串数组。`DisplayLines(width, theme)` 是派生函数。ANSI 不应成为 source truth；样式应尽可能保存为 span/role，最终由 presenter 编码。

### 5.3 稳定身份与 revision

- 创建 cell 时分配 `CellID`，整个 live → final → replay/persist 映射期间不变；
- `Sequence` 只在创建 top-level cell 时增加，update/finalize 不增加；
- 每次 mutable update 增加 `Revision`；过期 update 可被安全丢弃；
- finalization 将 `CellMutable` 转为 `CellCommitted`，并在同一 transaction 中移出 ActiveBand 或替换 mutable projection；
- 不允许用文本相等判断“是否已经输出”，文本相同不代表相同 cell，文本不同也不代表需要新 append。

### 5.4 ActiveBand 的边界

ActiveBand 只承载仍在运行、需要频繁重绘且不应进入不可变历史的状态，例如：

- assistant streaming 尚未稳定的 tail；
- running tool chain；
- 当前 turn 的进度、elapsed 或 spinner；
- 等待授权、等待子任务等可变状态。

ActiveBand 更新：

- 不增加 transcript sequence；
- 不改变 committed cell boundary；
- 不触发 native scrollback handoff；
- finalize 时通过一个 transaction 转换/合并为 committed cell，不能先 append final 再清 ActiveBand。

### 5.5 Popup、status 与 prompt

- popup 只改变 overlay stack；关闭后下层 frame 从 Scene 重新合成；
- status 可以被 coalesce，只保留最新 revision；
- prompt/editor 保存 source、selection、viewport 和 cursor intent；
- 三者都不贡献 transcript gap，不参与 history replay，不得写入 `historyWindow`。

---

## 6. RenderEvent、排序与 Scene transaction

### 6.1 事件模型

业务层不得表达“移动光标、清行、打印空行”，只能表达 UI 意图：

```go
type RenderEvent interface {
    EventID() uint64
    Source() EventSource
}

type AppendCell struct { Cell TranscriptCell }
type UpdateCell struct { ID CellID; Revision uint64; Source RichDocument }
type FinalizeCell struct { ID CellID; Revision uint64; Source RichDocument }
type RemoveMutableCell struct { ID CellID; Revision uint64 }
type UpdateActiveBand struct { Revision uint64; Model ActiveBandModel }
type UpdateStatus struct { Revision uint64; Model StatusModel }
type UpdatePrompt struct { Revision uint64; Model PromptModel }
type PushPopup struct { Popup PopupModel }
type PopPopup struct { PopupID string }
type Resize struct { Width, Height int; Generation uint64 }
type EnterFullscreen struct { Request FullscreenRequest }
type ExitFullscreen struct { LeaseID uint64 }
```

事件可以来自多个 goroutine，但进入 UI queue 后必须获得全局消费顺序。跨 source 有业务因果关系时使用 parent event/turn/tool sequence，而不是依赖 goroutine 调度时间。

### 6.2 transaction 生命周期

每次消费一个事件或一个明确的事件 batch：

```text
Validate
  -> Reduce to candidate Scene
  -> Check invariants
  -> Commit Scene revision
  -> Produce immutable snapshot
  -> Layout/Compose
  -> Presenter flush
  -> Commit front/handoff metadata
```

如果事件涉及“final assistant + final tool chain + clear ActiveBand + update status”，应允许作为一个 `SceneTransaction` 批量提交，确保用户永远看不到中间状态。

```go
type SceneTransaction struct {
    Cause      EventID
    Mutations  []SceneMutation
    Flush      FlushPolicy
}
```

`FlushPolicy` 可区分 immediate、coalescable 和 no-primary-flush-during-lease，但不能绕过 Scene。

### 6.3 排序规则

1. 用户提交产生 user cell 后，才开始对应 assistant turn；
2. 同一 turn 内依据 runtime event sequence 排序，不以完成回调到达时间重新排序；
3. mutable update 只更新自己的 `CellID`，不能插入另一个 committed cell 前面；
4. async system/diagnostic event 若无父 sequence，作为独立 top-level cell 在被 SceneController 接收时分配 sequence；
5. status/progress 不使用 transcript sequence；
6. replay 读取持久化顺序并重建相同 cell sequence；不得通过“逐行模拟 live 输出”恢复历史。

### 6.4 队列与背压

事件队列必须 bounded，但不同事件采用不同策略：

| 事件 | 队列满时策略 |
| --- | --- |
| committed cell append/finalize | 不得丢；阻塞、持久化或触发受控故障 |
| user input/prompt submit | 不得静默丢失 |
| mutable streaming update | 可按 `CellID` 合并，只保留最新 revision |
| status/spinner/elapsed | 可 coalesce，只保留最新状态 |
| resize | 可 coalesce，只保留最新 generation |
| popup push/pop、lease acquire/release | 不得重排或丢失 |

背压处理不能回退到 raw stdout。

---

## 7. 统一 block boundary 与 gap policy

### 7.1 基本模型

Gap 是两个 top-level transcript cell 之间的语义边界，不是某个 cell 尾部保存的空字符串，也不是“上一次是否打印过完整 block”的全局布尔值。

```go
type BoundaryClass uint8

const (
    BoundaryDense BoundaryClass = iota
    BoundaryNormal
    BoundarySection
)

func ResolveGap(prev, next TranscriptCell) GapRows
```

实现初期 `GapRows` 只允许 `0` 或 `1`。若未来确需 section divider，应引入显式 separator cell/style，不能把多个不可追踪空行塞进 history。

### 7.2 规范化规则

- cell source 不保存由 renderer 添加的 leading/trailing blank row；
- markdown/code/preformatted source 中用户实际提供的内部空行必须保留；
- renderer 对 source 做行尾规范化，但不得用 `TrimSpace` 破坏代码块内部空白；
- gap 在 layout/compose 阶段由相邻 cell metadata 派生；
- handoff 时 gap 与后继 cell 的归属必须明确，建议作为 boundary row 归属于后继 cell 的 display projection，或使用独立 `BoundaryRecord`，二者选一后全局一致。

### 7.3 规则表

| 前一项 | 后一项 | gap | 说明 |
| --- | --- | ---: | --- |
| 无 | 任意首 cell | 0 | transcript 不以空行开头 |
| user | assistant | 1 | 独立 top-level 对话块 |
| assistant | user | 1 | turn 边界 |
| 任意 committed top-level | 独立 command/system/notice | 1 | 最多一个语义 gap |
| 同一 tool-chain cell 内的 tool events | 下一 tool event | 0 | cell 内稠密 |
| 独立 final tool/event cell | 下一独立 final cell | 1 | top-level boundary |
| supplement/reasoning 属于同一 assistant cell | 同 cell 后续 section | 由 cell 内 layout 决定，默认 0 | 不读取全局 gap 状态 |
| mutable cell revision N | revision N+1 | 不适用 | replace，不创建边界 |
| mutable cell | 同 ID finalization | 不新增 | replace/commit transaction |
| ActiveBand/status/prompt/popup | 任意 transcript cell | 不参与 | overlay 不能改变 transcript gap |
| filtered/empty event | 任意 | 不参与 | 不推进 boundary |
| replay cell | replay next cell | 与 live 相同 | 禁止 replay 特例 |
| handoff range | retained next cell | 保持原 boundary | handoff 不重新计算业务顺序 |

若产品最终希望某些组合稠密，只修改这一规则表及其测试，不在不同 renderer 中增加 `gapForTool`、`gapForAsyncLine` 等分支。

### 7.4 gap 所有权

必须选择并固定一种实现：

**推荐方案：派生 boundary row。** Scene 只保存 semantic cells，`LayoutTranscript` 遍历相邻 cell 时插入 gap row，并为该 row 标记稳定的 boundary key：

```text
BoundaryKey = (PrevCellID, NextCellID, PolicyVersion)
```

这样可以：

- resize 时稳定重算；
- mutable update 不重复添加空行；
- handoff 时以 boundary key 判断是否已提交；
- 测试可以直接断言 cell sequence 与 boundary sequence。

禁止同时在 cell source 尾部和 boundary policy 中保存 gap，否则必然出现双空行。

---

## 8. History、mutable cell 与 exactly-once correctness

### 8.1 Cell 生命周期

```text
Created
  -> Mutable (optional, revision 1..N)
  -> Finalizing
  -> Committed
  -> Retained Tail
  -> Partially Handed Off (仅超大 cell/分片场景)
  -> Handed Off
```

一般 cell 应在完整 committed 后按 cell 边界交接。超大单 cell 若必须分片，分片必须具有稳定身份：

```go
type DisplaySliceID struct {
    CellID           CellID
    CellRevision     uint64
    LayoutGeneration uint64
    StartSourceUnit  uint64
    EndSourceUnit    uint64
}
```

不应只用“当前 historyWindow 前 N 行”作为唯一交接身份，因为 resize 后 display row 数量可能改变。

### 8.2 Finalization 规则

Finalization 必须在一个 transaction 内完成：

```text
validate expected revision
  -> replace mutable source with final source
  -> set phase committed
  -> clear corresponding ActiveBand projection
  -> recompute boundary/layout
  -> compose one frame
  -> flush
```

禁止以下模式：

```text
append final text
clear running text
猜测旧文本是否已出现
按 suffix 字符串替换失败后 reset history
```

当 final source 与 latest mutable source 相同时，仍执行状态迁移，但 display diff 可以为空；不能据此再 append 一份。

### 8.3 Handoff frontier

建议保存结构化 handoff 状态：

```go
type HandoffState struct {
    Generation uint64
    Frontier   HandoffCursor
    Records    Ring[HandoffRecord]
}

type HandoffRecord struct {
    Token       uint64
    CellID      CellID
    Revision    uint64
    SourceRange SourceRange
    RowCount    int
    Width       int
    Frame       uint64
}
```

流程：

1. 从 Scene snapshot 选择 committed 且超出 retained headroom 的最老 range；
2. 生成唯一 handoff token；
3. presenter 在拥有主屏时执行 cursor-neutral history insertion；
4. terminal flush 全部成功后提交 record 和 frontier；
5. 失败则不推进 frontier，front buffer invalid；
6. 重试使用同一 token/range，防止上层重新创建 append；
7. handed-off range 不再参与普通 viewport history append。

### 8.4 Retained tail 与 source truth

应用需要保留足够的 semantic cells/source range 用于：

- 当前 viewport 展示；
- bottom reserve grow/shrink 后恢复；
- resize reflow；
- mutable tail finalization；
- fullscreen 返回后的 full repaint。

物理 terminal scrollback 只保存不可变历史的展示副本，不是恢复 Scene 的数据库。session persistence/replay 必须来自聊天数据模型或 semantic cells，而不是读取 terminal 或 Backend front。

### 8.5 Soft output 收敛

现有 soft output/suffix rewrite 应收敛为 mutable cell update：

- `WriteSoftTrackedOutput` 对应 `UpdateCell(CellID, Revision, Source)`；
- stable prefix 可作为同一 cell 的 committed source range，或在明确边界后拆为已 committed cell；
- replacement 失败不应清空全部 history；revision/source-range 校验失败时记录 invariant violation，并从权威 Scene full repaint；
- 不再用文本 suffix 是否匹配来决定 history 所有权。

---

## 9. Layout、Compose 与事务式 Frame

### 9.1 Layout 输入与输出

Layout 输入必须是同一代 snapshot：

```go
type LayoutInput struct {
    SceneRevision     uint64
    TerminalWidth     int
    TerminalHeight    int
    ResizeGeneration  uint64
    ThemeGeneration   uint64
}
```

输出至少包括：

- retained transcript display rows；
- ActiveBand rows；
- status rows；
- prompt/editor rows及 cursor；
- popup overlay rectangles；
- handoff candidates；
- frame generation 与 snapshot identity。

任何组件不得在 layout 中读取实时可变 Scene；否则一个 frame 可能混用不同 revision。

### 9.2 Compose 层级

建议固定合成次序：

```text
1. base background
2. retained transcript viewport
3. ActiveBand
4. status
5. prompt/editor
6. popup/modal overlay stack
7. cursor intent
```

popup 可以遮盖下层，但关闭 popup 后必须从同一 Scene snapshot 重画下层，不得依赖“恢复之前终端字符”。

### 9.3 Presenter frame transaction

```text
Acquire primary screen ownership
  -> verify lease/generation
  -> build back buffer
  -> diff against known-valid front
  -> emit synchronized terminal update
  -> verify full write
  -> commit front buffer + frame generation
  -> release write section
```

若 front invalid，则跳过 diff，执行 full repaint。full repaint 仍然只能从 Scene/Layout/Compose 生成。

frame flush 期间不允许其他 terminal writer 插入字节。Scene event 可以继续入队，但必须在下一 frame 消费，不能修改正在提交的 snapshot。

### 9.4 Cursor 与 scroll region

- cursor 位置由 `CursorIntent` 决定；
- presenter 是唯一允许 Save/Restore/MoveTo/SetScrollRegion 的组件；
- frame 结束时验证 cursor 是否在合法 prompt/editor cell；
- history handoff primitive 必须 cursor-neutral，或在同一 frame transaction 中恢复确定位置；
- 业务 handler 不知道 terminal row/column，也不能调用 BeginOutput 来猜测光标状态。

---

## 10. 终端写入策略与输出接口

### 10.1 单一物理 writer

所有 ANSI 和 terminal bytes 必须集中到 presenter/terminal sink：

```go
type TerminalSink interface {
    FlushFrame(ctx context.Context, owner ScreenOwner, frame EncodedFrame) error
}
```

`ScreenOwner` 由 lease manager 发放，不能由业务代码构造。terminal lock 位于 `FlushFrame` 内部，业务层不直接获取。

### 10.2 合法的 raw/plain 输出范围

raw/plain renderer 在以下场景合法：

- JSON output；
- noninteractive；
- stdout 是 pipe/file；
- owned surface 启用前的启动失败；
- owned surface完成 destructive shutdown 后的最终退出阶段；
- 独立、未进入 chat TUI 生命周期的 CLI subcommand。

禁止的是：owned interactive 生命周期中任意业务代码绕过 Scene/presenter 写同一主 terminal。

### 10.3 Slash command 契约

目标接口：

```go
type CommandResult struct {
    Blocks  []RenderBlock
    Popup   *PopupModel
    Action  CommandAction
    Notice  *StatusModel
    // ReplayHistory: 仅 /load 类带加载副作用的命令使用；owned interactive
    // 提交确认 cell 后按逐消息 replay 渲染器回放历史，plain/JSON 投影忽略。
    ReplayHistory bool
}

type ChatCommand interface {
    Execute(context.Context, CommandInput) (CommandResult, error)
}
```

command handler：

- 可以读取 session/runtime state；
- 返回结构化结果或 action；
- 不调用 `fmt.Print*`、`os.Stdout`、terminal primitive；
- 不决定 gap、光标和物理换行；
- 多段 debug/status 文档作为一个或多个明确的 top-level block 原子提交。

`/debug display` 是首批迁移样例，但不能成为特例。现有 surface-aware direct output 可作为过渡 adapter，长期仍应返回 `CommandResult`。

`/load` 是副作用型样例：成功输出 = 确认文档（atomic command cell）+ 历史回放（逐消息 cell）。回放不得并入命令文档，否则会破坏 cell 边界与 replay 语义（见 INV-GAP-04 / §8.1）；dispatch 在确认 cell 提交后按 `ReplayHistory` 触发回放，回放渲染器自行选择 surface 或 plain 输出路径。

### 10.4 Runtime/tool/diagnostic 契约

对于接受 `io.Writer` 的组件，提供 Scene-aware adapter：

```go
type RenderEventWriter struct {
    CellID CellID
    Post   func(RenderEvent) error
}
```

adapter 负责：

- UTF-8/行片段聚合；
- 限流与大小上限；
- 将可见输出映射为 mutable/diagnostic event；
- 在 close/finalize 时提交完整 block；
- 不直接触碰 terminal。

纯开发日志写结构化 logger 或文件，不默认进入 transcript。stderr 不能被视为 owned terminal 的“另一条安全通道”；若 stdout/stderr 指向同一 TTY，二者同样受 screen ownership 约束。

### 10.5 审计和 CI 门禁

建立 owned-interactive 范围的旁路审计：

- 静态扫描 `fmt.Print*`、`log.Print*`、`os.Stdout`、`os.Stderr`、`Terminal.*` direct calls；当前 chat/command 的 P0 AST gate 已覆盖其中可直接归属的 `fmt`、`os`、`io` 和 `ui.WriteTerminal*` 调用；
- debt inventory 的每项必须注明 owned-safe、plain-only、startup/shutdown 或待迁移原因与 owner；基线不是允许新增旁路的 allowlist；
- 新 command handler 测试使用 forbidden writer，发现直接输出立即失败；
- debug/test build 记录 terminal sink owner、frame 和调用点；
- P0 分类完成后将 audit 扩展到 `log.Print*`、terminal primitive 和 active-turn producer，并在 CI 中运行。

不建议长期依赖全局替换 `os.Stdout`，因为它会隐藏错误依赖、影响非交互路径和第三方库；API 收敛与静态门禁才是最终方案。

---

## 11. Fullscreen 与 Screen Lease 生命周期

### 11.1 状态机

```text
PrimaryActive
  -> LeaseAcquiring
  -> PrimarySuspended
  -> AlternateActive
  -> AlternateReleasing
  -> PrimaryInvalidated
  -> PrimaryFullRepaint
  -> PrimaryActive
```

`Disable()` 属于：

```text
PrimaryActive -> ShuttingDown -> Disabled
```

两者语义完全不同，不能复用。

### 11.2 Lease API

```go
type ScreenLease interface {
    ID() uint64
    Mode() ScreenMode
    Release(context.Context) error
}

func (p *TuiPresenter) AcquireAlternateScreen(
    ctx context.Context,
    req FullscreenRequest,
) (ScreenLease, error)
```

Acquire：

1. 等待当前 primary frame 完成；
2. 标记 primary flush suspended；
3. 保留完整 Scene、front buffer metadata 和事件队列；
4. 在同一个 terminal ownership transaction 中进入 alternate screen；
5. 将物理写权限交给 fullscreen presenter。

Lease active 期间：

- 主 SceneController 继续处理不可丢事件；
- primary presenter 不 flush；
- status/resize/mutable update 可合并；
- committed transcript event 必须保留；
- fullscreen presenter 只能写 alternate screen；
- 禁止后台 stdout 绕过 lease。

Release：

1. fullscreen presenter 停止提交；
2. 退出 alternate screen 并恢复基础 terminal mode；
3. primary front buffer 标记 invalid；
4. 处理最新 resize generation；
5. 从最新 Scene snapshot full repaint；
6. 恢复 primary flush。

### 11.3 异常安全

- cancel、error、panic 必须通过 `defer`/guard 释放 lease；
- release 幂等，同一 lease 只能恢复一次；
- acquire 失败不能留下 `PrimarySuspended`；
- alternate exit 失败时执行 best-effort terminal reset，并把 presenter 标记为 degraded；
- 测试注入 acquire/enter/render/exit/repaint 每个阶段的失败；
- process signal handler 只做安全终端恢复，不尝试在 signal context 中重建复杂 Scene。

### 11.4 迁移对象

所有 fullscreen 入口统一使用 lease，包括但不限于：

- resume session picker；
- backtrack selector；
- theme selector；
- 未来 model/tool/agent fullscreen picker。

迁移完成后，禁止用 `FixedBottomSurface.Disable()/Enable()` 实现临时 modal/fullscreen。

### 11.5 实施状态（2026-07-31）

首批 foundation 已落地：

- 新增 `ScreenLease` 接口与 `FixedBottomSurface.AcquireAlternateScreen(ctx, FullscreenRequest) (ScreenLease, error)`：
  - 单租约不变量：同一时刻只允许一个 alternate lease，重复获取返回 `ErrScreenLeaseBusy`；
  - `Release` 幂等；lease 实例持有唯一 id；
  - acquire 失败不留下悬挂状态；`Disable()`（进程 teardown）会清掉 lease 且不再向 alternate screen 写入；
- 主 presenter flush 抑制：lease 生效期间所有主屏写入路径（owned 帧、legacy layout/status/popup/prompt/active band、光标移动、scroll debt flush、`WriteOutput` 的终端写）全部短路，retained state 照常更新；
- Release 全量重绘：使 double-buffer 失效后从最新 retained scene 完整 recompose（owned 模式），legacy 模式走 `Enable()` 同款 paint 序列；
- 三个 fullscreen 入口（resume session picker、backtrack selector、theme selector）已从 `Disable()/Enable()` 迁移到 lease；`Disable()/Enable()` 仅保留给进程级 shutdown（`chat_setup` cleanup）；
- DEC 1049 transport 已收进 lease 的同一个 terminal ownership transaction：
  - `AcquireAlternateScreen` 在持有 terminal write lock 的临界区内依次写入 `\x1b[?1049h` / `\x1b[r` / `\x1b[?25l` / `\x1b[2J` / `\x1b[H` 并原子地标记 lease 活跃，主 presenter 无法在“进入 alternate screen”与“primary flush suspended”之间插入任何字节；
  - enter 写失败时在同一锁内 best-effort 回滚 exit 序列，不留下 `PrimarySuspended`，surface 立即可重新 acquire；
  - `Release` 在同一锁内先写 exit 序列（`\x1b[?25h` / `\x1b[r` / `\x1b[?1049l`）再全量重绘 primary，exit→repaint 对终端原子；exit 写失败仍继续重绘并向上返回错误；
  - picker 通过新增 `SelectFullScreenListWithLease` 感知 lease：list 跳过自身的 DEC 1049 序列（避免双写 alternate buffer），只保留 stdin raw-mode 处理；未持有 lease 的调用路径（`SelectFullScreenList`）行为不变；
  - 序列写入使用 `writeLeaseSequencesLocked`（锁内直写），不复用 `writeFullScreenSequences`（后者经 `WriteTerminalText` 会再次获取不可重入的 terminal write lock）；
  - `alternateWriter` 字段允许测试注入字节 sink 断言 enter/exit 边界；testMode 下默认 `io.Discard`，不污染真实终端；
- 回归测试：`screen_lease_test.go` 覆盖 lifecycle、单租约、flush 抑制、release 重绘、teardown 安全，以及新增的 enter/exit 序列边界断言、enter 失败回滚、exit 失败仍重绘三个失败注入用例。

lease 已拥有完整的 alternate-screen 所有权契约（enter/suspend、exit/repaint 各在同一 transaction）。后续阶段应把 lease 上移到 TuiPresenter（§11.2 的最终 API），并补齐 signal-handler 安全恢复（signal 上下文只做终端恢复、不重建 Scene）与 acquire/enter/render/exit/repaint 每一阶段的系统化失败注入。

---

## 12. Resize、Reflow 与 Native Scrollback Handoff

### 12.1 Resize 处理

resize 是 Scene event，不是任意组件直接调用 terminal update：

```text
OS resize signal
  -> coalesced Resize(width, height, generation)
  -> update TerminalState
  -> invalidate layout cache
  -> reflow retained semantic cells
  -> recompute bottom panes and cursor
  -> full/diff frame
```

同一 frame 只使用一个 width/height generation。resize storm 可 coalesce，但最终 generation 必须被绘制。

### 12.2 Reflow 规则

- 使用 cell source 和 style span 重新生成 display rows；
- 不使用旧 physical rows 拼接新 source；
- retained tail 和 mutable cells可 reflow；
- 已 handoff 到 native scrollback 的历史不尝试重写；
- 宽字符、emoji、combining marks、ANSI style continuity 必须由共享 layout/render 库处理；
- 设置 retained source/display cap，防止极端 resize 导致无界 CPU 和内存开销；
- cap 不能破坏 cell identity、handoff frontier 或重复输出。

### 12.3 Handoff 时机

handoff 只能在以下条件同时满足时执行：

- primary screen lease active；
- terminal size generation 稳定；
- range 对应 committed revision；
- range 已离开可重绘/restore headroom；
- 当前无 fullscreen lease 切换；
- presenter 可以在一个 frame transaction 中完成 insert + redraw + cursor restore。

ActiveBand grow/shrink 时，优先从 retained headroom 恢复，不回退到已 handoff range。若 headroom 不足，允许可预测地减少可见历史，但不能伪造或重复 native scrollback。

### 12.4 Scrollback 与 Scene 的关系

```text
Session/semantic history      权威、可 replay
Retained Scene tail           权威、可 reflow/repaint
Native terminal scrollback    已交接 display 副本、不可重写
Presenter front buffer        缓存、可随时 invalid
Physical screen               输出目标、永不作为数据源
```

这五者必须在代码类型和命名上明确区分，禁止统一称为 `history` 后依赖调用者猜测其语义。

---

## 13. 并发、锁与调度

### 13.1 单 UI owner

推荐使用单 SceneController/UI goroutine 串行执行：

- event reduce；
- transaction commit；
- snapshot generation；
- layout/compose 调度；
- presenter frame sequencing；
- lease state transition。

runtime、tool、network、timer goroutine 只投递 event。这样可将大量跨字段 mutex 不变量收敛为事件顺序不变量。

若因为现有结构暂时必须保留 mutex，仍应保持锁顺序：

```text
Scene state lock
  -> snapshot release
  -> presenter state lock
  -> terminal write lock
```

不得在 terminal write lock 内调用可能回调业务层、等待队列或获取 Scene lock 的代码。

### 13.2 Frame 调度

- user submit、cell append/finalize、popup push/pop：要求及时 frame；
- streaming mutable update：在低延迟窗口内合并，例如一帧一次；
- status/spinner：低优先级 coalesce；
- resize：优先于普通 diff，通常触发 full layout；
- fullscreen lease transition：高优先级 barrier；
- handoff：只在 committed scene/frame barrier 上运行。

### 13.3 防止饥饿

持续 streaming 不得让 command result、user input 或 lease release 永久等待。队列调度需要区分：

- durable ordering lane；
- coalescable visual lane；
- control/barrier lane。

但最终 Scene commit 仍形成一个全局 revision 顺序，不能让不同 lane 各自直接写屏。

### 13.4 错误策略

- 事件校验失败：记录 invariant violation，不写 terminal；
- 过期 mutable revision：安全丢弃并计数；
- presenter write 失败：front invalid，停止 incremental diff；
- Scene 无法恢复的内部错误：受控退出 owned mode 并执行一次 terminal cleanup，不能在未知屏幕上继续两个 renderer；
- 输出过大：在 domain 层生成明确 truncation cell/marker，不在 writer 中静默截断导致 block 半提交。

---

## 14. 分阶段实施计划

迁移采用“先建立约束与 adapter，再迁移入口，再切所有权，最后删除旧路径”的顺序。任何阶段都不允许两个 production renderer 对同一 terminal 双写；shadow compare 只能比较内存 frame，不能双提交。

### 14.1 阶段总览

| 阶段 | 目标 | 关键交付物 | 依赖 | 退出条件 |
| --- | --- | --- | --- | --- |
| P0 | 建立旁路写入清单与门禁 | writer inventory、allowlist、CI audit、PTY 复现 | 无 | owned 生命周期所有 direct writer 有 owner/迁移项 |
| P1 | 统一 slash command 输出 | `CommandResult`、command adapter、迁移高风险命令 | P0 | command handler 不直接 stdout/terminal |
| P2 | 清理 runtime/tool diagnostics | `RenderEventWriter`、logger 分流、active-turn writer 迁移 | P0 | active turn 无 raw stdout/stderr |
| P3 | 引入 screen lease | lease manager、fullscreen suspend/resume、异常恢复 | P0 | fullscreen 不再 Disable/Enable surface |
| P4 | 拆分 Scene/Layout/Presenter | SceneController、snapshot、presenter facade | P0–P3 | 单一 Scene revision 驱动 frame |
| P5 | 统一 semantic cell | stable ID/revision、mutable finalize、replay adapter | P4 | live/replay/finalize 同一 cell 模型 |
| P6 | 统一 boundary/gap | `BoundaryPolicy`、规则表测试、删除 gap helper | P5 | gap 只由 policy 生成 |
| P7 | 完成 reflow/handoff | source-aware layout、handoff record/frontier | P4–P6 | resize/handoff exactly-once |
| P8 | 删除 legacy 与补偿状态机 | 删除旧 renderer、raw adapter、旧 flags | P1–P7 | production 只有一个 owner/presenter |
| P9 | 全量验收与性能收口 | property、VT、PTY/ConPTY、benchmark、runbook | P8 | 满足第 19 节全部验收标准 |

### 14.2 P0：直接输出审计与安全网（进行中）

已完成的安全网：

- `TestChatInteractiveDirectWriterInventory` 对 `commands/chat*.go` 和 `command.go` 做 AST 扫描，覆盖 `fmt.Print*`、`fmt.Fprint*(os.Stdout/os.Stderr)`、直接 `os.Std*.Write*`、`io.WriteString(os.Std*)` 及 `ui.WriteTerminal*`；
- 当前基线为 155 个 file/function/kind 分组、550 个 call site；新增分组或调用次数变化均默认失败，失败信息包含实际行号；
- `/debug display`、`/status`、`/load` 与 `/title`/`/rename` 成功路径已验证不写 raw stdout、各作为一个 atomic command cell 提交，并在 prompt/status/ActiveBand repaint 与 resize recompose 后不重复或丢失；参数错误与加载/同步失败保留 legacy 报错路径（错误 message 需在所有模式下可见）。
- `cmd/aicli/functions/builder.go` 已删除 legacy tool-call parsing 的 `fmt.Print*` / `os.Stderr` diagnostics；解析层静默保留 raw/incomplete call，由上层决定结构化诊断。`TestFunctionCallBuilder_HasNoDirectTerminalWriter` 阻止该库重新取得 terminal sink。

剩余工作项：

- 对 tool/runtime active-turn direct output 建立并发复现；
- 增加 owner/frame debug trace；
- 为每个现有 debt entry 补 owned-safe、plain-only、startup/shutdown 或待迁移分类和 owner；
- 按分类迁移入口并从 inventory 删除，而不是扩充 baseline。

本阶段不大规模重写，仅阻止问题继续增长。

### 14.3 P1：CommandResult 收敛

优先顺序：

1. `/debug`、`/status`、`/load`、`/title`，建立结构化命令样板（`/debug display`、`/status`、`/load`、`/title`/`/rename` 均已迁移；`/load` 确认 cell 采用 `chat_load_document.go`，历史回放经 `CommandResult.ReplayHistory` 在提交后触发）；
2. `/goal`、`/memory`、`/stream`、`/skills` 等文档型输出（前三者已迁移，`/skills` 的 selection/modal 仍待迁移）；
3. `/resume`、`/backtrack`、`/theme` 等带 modal/fullscreen action 的命令；
4. 其余 slash command；
5. 删除 `beginDirectInteractiveOutput` 作为通用 command 生命周期协议，仅保留必要的过渡 facade。

每个 command result 作为明确 block transaction 进入 Scene。测试必须在 command 后触发 prompt/status repaint，不能只捕获 stdout 字符串。

### 14.4 P2：Runtime/tool diagnostics 收敛

- 将 builder/parser warning 改为 logger 或 diagnostic render event；
- 将 tool output mirror 替换为 cell-aware writer；
- 区分用户可见 tool result、运行进度与开发日志；
- 为 streaming fragment 建立 accumulator 和 revision；
- stdout/stderr 指向 TTY 时统一受 Scene ownership 管理。

### 14.5 P3：Fullscreen lease

- 实现 lease state machine 和异常安全 guard；
- 迁移所有 fullscreen selector；
- lease active 时模拟后台 chat/tool/status/resize 事件；
- release 后强制 front invalidation + full repaint；
- 删除 fullscreen 路径中的 surface Disable/Enable。

### 14.6 P4–P6：核心模型切换

- 将 `FixedBottomSurface` 变为 facade，内部委托 SceneController/Layout/Presenter；
- 为现有 history output 生成稳定 cell identity；
- 将 ActiveBand running/final 合并为 cell lifecycle；
- replay/resume 直接建立 cell sequence；
- 用 `BoundaryPolicy` 替换 `completeBlockOutput`/`gapFor*` 推断；
- 设置 feature flag 只选择旧或新 presenter，禁止双写；可 shadow compose 比较 frame。

### 14.7 P7：Resize/Reflow/Handoff

- `DisplayLines(width)` 从 semantic source 派生；
- retained tail 在 resize 时重排；
- 建立 source range handoff record；
- frame 成功后才推进 frontier；
- 覆盖 terminal shrink/grow、wide char、长 tool cell、popup/fullscreen 并发；
- 删除依赖旧 display row suffix 的 soft rewrite。

### 14.8 P8–P9：删除与验收

- 删除 production immediate renderer 和所有无主 terminal write；
- 删除旧 scroll compensation/gap flags；
- 保留 plain/JSON 独立 renderer；
- 完成真实终端矩阵、性能基线和故障注入；
- 移除临时 killswitch 前至少经过一个稳定发布周期；若保留 emergency flag，必须是“整个 session 选择 renderer mode”，不能运行期混用。

---

## 15. 删除与收敛清单

迁移完成后逐项确认，不允许只标记 deprecated 后长期保留可达路径。

### 15.1 输出入口

- [ ] owned interactive command handler 中的 `fmt.Print*`；
- [ ] owned interactive runtime/tool 中的 `os.Stdout`/`os.Stderr` direct write；
- [ ] 业务层 direct terminal cursor/scroll-region/clear-line 操作；
- [ ] 将 `BeginOutput` 当作 raw 输出许可的调用方式；
- [ ] 同一 session 在 retained/plain renderer 之间临时切换。

### 15.2 Gap 与 block 状态

- [ ] `completeBlockOutput` 作为全局 gap truth；
- [ ] `gapFor*` 分散 helper；
- [ ] cell source 首尾人为拼接的空行；
- [ ] replay 特有的 gap 推断；
- [ ] ActiveBand/status 更新对 committed gap state 的修改。

### 15.3 History 与 soft output

- [ ] 以纯文本 suffix 匹配作为 cell ownership；
- [ ] finalize 时 append 第二份 final 内容；
- [ ] replacement 失败即清空全部 owned history；
- [ ] retained history 与 native handoff 各自维护不可关联的行计数；
- [ ] 从 Backend/terminal screen 反推业务历史。

### 15.4 Fullscreen 与生命周期

- [ ] fullscreen 前 `Disable()`、退出后 `Enable()`；
- [ ] alternate screen active 时 primary presenter flush；
- [ ] fullscreen renderer 绕过 screen owner；
- [ ] lease release 后继续使用旧 front buffer 做增量 diff。

### 15.5 旧补偿状态机

在 owned architecture 完全接管后，评估并删除或限制到 plain legacy-only：

- [ ] `scrollCompensatedRows`；
- [ ] `pendingScrollDownRows`；
- [ ] `outputCursorOnBlankRow`；
- [ ] `outputScrollDebtRows`；
- [ ] 与旧 immediate scroll region 绑定的补偿分支；
- [ ] production legacy renderer 可达入口。

### 15.6 Markdown / ActiveBand / 历史消息统一管理现状（2026-08-01 排查结论）

三者**已有统一管理，但统一在「单一引擎 + 单一 writer + 单一 coordinator」，不是「单一 Scene」**；用户观察到的"markdown 单独渲染、与工具事件流覆盖"对应的是仍存在的 raw stdout 旁路与双渲染调用点，而非无管理状态。

已统一的部分：

- **单一 markdown 引擎**：`ui/markdown`（goldmark AST + Chroma → `render.Document`）是唯一内容渲染真源；ActiveBand 直播（`ActiveStreamController.activeDocumentLocked` → `markdown.Render(ActiveBandBodyOptions)`）与历史回放/scrollback（`Formatter.Format` → `AssistantBodyOptions`）共享 `ApplyAssistantBodyContract`（Hyperlinks=false、TableMode=Auto），并有 `TestAssistantBodyEngines_SharedContractParity` 等 parity 测试保证 blank/plain 一致（active_stream.go L458-466、formatter/markdown.go L174-176、assistant_options.go L12-20）。
- **单一 surface 写入入口**：owned 路径下所有内容经 `FixedBottomSurface.WriteOutput` → `appendHistoryWindowLocked` → `renderOwnedViewportLocked`（historyWindow + ActiveBand 一帧合成）；`WriteSoftTrackedOutput` 单独保留 assistant soft 尾巴供 reflow。
- **单一 coordinator 互斥入口**（chat_interaction.go）：assistant 流 `paintActiveStreamLocked`、tool 流 `syncAgentStageActiveBandLocked`、稳定前缀滚动提交 `commitActiveStableScrollbackLocked`（用 `session.Formatter.Format` 的 rows delta，与历史 replay 同构）、band 帧 `publishActiveStreamFrameLocked` → `SetActiveBandStyled`。
- **band 所有权规则已显式化**：`streamingActive || reasoningActive` 时 tool 事件不碰 band（L4931-4932）；assistant 内容总是覆盖 stale tool cell（L3992-3995）；tool 结束 → `Cancel` + `ClearActiveBand`；直播与持久历史共用 `aicliTranscriptRenderer` 与 `Formatter`。

仍存在的分裂点（P4–P9 收敛对象）：

- **双渲染调用点**：live band 用 `markdown.Render(ActiveBandBodyOptions)`（自带 Highlighter、`HideHighlightFallback=true`），scrollback 与历史用 `Formatter.Format`（`AssistantBodyOptions`）——靠 contract + parity 测试对齐，不是同一代码路径；收敛方向是以 `Formatter.FormatDocument` 为结构化真源、band 只做 highlighter/holdback 差异化。
- **raw stdout 旁路**：P0 inventory 155 组/552 个 call site 直接写终端，绕过 surface 所有权——"下一帧覆盖不在 Scene 中的文字 / raw 输出出现在 ActiveBand 中间"的真源即此（§1.1、§1.4）。
- **historyWindow 仍是 `[]string`**，无 cell/row identity；gap 由 `completeBlockOutput`/`gapFor*` 按前一次调用推断（历史模型），未切到 `BoundaryPolicy`（INV-GAP-03 未实现）。

---

## 16. 测试策略与矩阵

### 16.1 测试分层

1. **纯模型单元测试**：cell lifecycle、revision、boundary、queue coalesce；
2. **Layout/Compose golden**：不同 width/height/theme 下的 rows 和 cursor；
3. **Presenter/VT 测试**：执行 ANSI stream 后重建物理屏幕，验证与 frame 一致；
4. **属性/不变量测试**：随机事件序列、resize、update/finalize、popup/fullscreen；
5. **命令集成测试**：command result 与下一帧 repaint；
6. **并发/race 测试**：runtime event、timer、input、lease 同时发生；
7. **PTY/ConPTY 测试**：真实 terminal semantics；
8. **人工验收**：Windows Terminal、ConPTY、常见 ANSI terminal、tmux/zellij 可支持模式；
9. **性能测试**：长 session、resize storm、高频 streaming、超长 tool result。

### 16.2 核心测试矩阵

| 场景 | 必须断言 |
| --- | --- |
| user → assistant streaming → final | 同一 assistant `CellID`；final 后只出现一次 |
| running tool → multiple events → completed | cell 内稠密；finalize 不追加副本 |
| tool final cell → next event cell | 恰好一个 policy gap |
| `/debug display` 后 status tick/prompt repaint | debug block 顺序不变、不覆盖、不消失 |
| command 输出多行时 ActiveBand grow | block 行连续，无永久空洞 |
| popup open/close | transcript sequence、handoff frontier 不变 |
| fullscreen 期间到达 assistant/tool event | alternate screen 不被污染；release 后事件可见 |
| fullscreen cancel/panic | lease 释放；主屏 full repaint；Scene 不丢失 |
| width 120→60→120 | retained source 不变；可见 rows 可逆重算范围内一致 |
| height grow/shrink | 不重复 handoff；prompt/cursor 始终合法 |
| partial terminal write | front invalid；下一次 full repaint |
| 过期 mutable revision | 新 revision 保留；不回滚 |
| 空/filtered event | 不产生 cell 或 gap |
| replay 与 live 同一 transcript | cell/boundary sequence 相同 |
| native handoff 重试 | 同一 range 最多成功一次 |
| stdout/stderr 违规写 | CI/test guard 失败并报告调用点 |

### 16.3 属性测试建议

随机生成事件序列并持续验证：

```text
CellID 唯一
Sequence 严格递增
Revision 单调
Committed cell 不再 mutation
GapRows ∈ {0,1}
No orphan boundary
Handoff frontier 单调
No handed-off range re-appended
At most one active screen lease
Front valid => VT(screen) == presenter front
Cursor within terminal bounds
```

可以针对每个随机序列在任意位置插入 resize、popup、status update、fullscreen acquire/release 和 write failure，以发现手工案例难覆盖的组合。

### 16.4 真实终端验收

至少覆盖：

- Windows ConPTY / Windows Terminal；
- 非 Windows ANSI terminal；
- terminal height 很小、width 很窄；
- CJK、emoji、combining character；
- 快速连续 resize；
- 长时间 streaming；
- shell-like tool 大量输出；
- fullscreen 进入/退出和 Ctrl+C；
- terminal capability 不满足时的 plain-mode 选择。

录制时应使用 marker/cell ID 校验，不仅依赖截图肉眼观察。

---

## 17. 可观测性与诊断

### 17.1 建议字段

开发/debug 模式记录结构化、无敏感正文的元数据：

```text
renderer_mode
scene_revision / event_sequence
frame_generation / resize_generation
typed_event / transaction_id
cell_id / cell_kind / cell_revision / cell_phase
transcript_cell_count / retained_cell_count
boundary_key / boundary_gap_rows
active_band_revision / status_revision / prompt_revision
screen_owner / lease_id / lease_state
front_valid / full_repaint_reason
handoff_token / handoff_frontier / handed_off_rows
queue_depth / coalesced_updates / dropped_stale_revisions
terminal_width / terminal_height
flush_bytes / diff_cells / frame_latency
```

不得默认记录完整用户对话、tool secret、环境变量或未经脱敏的 command output。测试可使用 synthetic marker 和 CellID。

### 17.2 关键计数器

- `tui_unowned_terminal_write_total`：目标长期为 0；
- `tui_full_repaint_total{reason}`；
- `tui_stale_cell_update_total`；
- `tui_invariant_violation_total{type}`；
- `tui_handoff_retry_total`；
- `tui_handoff_duplicate_prevented_total`；
- `tui_scene_queue_depth`；
- `tui_frame_latency_ms`；
- `tui_frame_diff_cells`；
- `tui_screen_lease_active`。

### 17.3 Debug snapshot

`/debug display` 最终应读取 Scene/Presenter 的只读 snapshot，并以普通 `CommandResult` 显示，例如：

- renderer mode；
- current owner/lease；
- Scene/frame/resize generation；
- transcript cell 数量及 phase 汇总；
- retained/handoff frontier；
- active band/status/prompt/popup 摘要；
- front buffer 是否有效及上次 invalidation 原因。

读取 debug 状态不能修改 Scene，也不能通过 raw stdout 输出。

---

## 18. 风险与回退策略

### 18.1 主要风险

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| Scene 模型与现有 event 语义不一致 | 丢事件或错误合并 | 先 adapter/shadow model，建立 sequence/revision 测试 |
| handoff exactly-once 实现错误 | 原生 scrollback 重复/漏行 | token/record、成功后推进 frontier、故障注入 |
| 大量 semantic history 占用内存 | 长 session 内存增长 | retained source cap、持久层 replay、不可变旧 range 释放 |
| full repaint 过多 | 闪烁和性能下降 | 只在 invalid/resize/lease release 使用，正常路径 diff |
| queue backpressure | UI 延迟或 runtime 阻塞 | durable/coalescable 分类、指标与限流 |
| Windows/Unix ANSI 差异 | 某平台 cursor/scroll 异常 | VT + ConPTY + PTY 双层测试 |
| 迁移期新旧逻辑交叉 | 更难定位的双写 | session-level feature flag，只选一个 presenter |
| fullscreen 异常退出 | terminal mode 残留 | 幂等 lease guard、best-effort reset、signal cleanup |

### 18.2 回退原则

- 每个阶段保留小粒度 feature flag；
- flag 必须在 session 创建时选择完整 renderer pipeline；
- 不允许在同一 session 中“新 presenter 出错后直接继续 raw 输出”；
- 允许内存 shadow compose 比较新旧 frame，但只能有一个 renderer 写 terminal；
- handoff/front buffer 异常时，优先 invalidation + Scene full repaint；
- 若某阶段必须回退，回退该阶段的所有权切换，不恢复已经证明错误的混合双写；
- emergency plain-mode 回退要执行明确 teardown，并提示功能降级，不能假装 retained history 可继续。

### 18.3 发布节奏

建议：

1. 测试/开发环境默认启用；
2. opt-in canary，收集无正文指标；
3. interactive owned-capable terminal 默认启用，保留 session-level killswitch；
4. 一轮稳定发布后删除旧 production path；
5. 再一轮稳定发布后删除迁移 adapter 和过期 feature flag。

---

## 19. 最终验收标准

### 19.1 架构验收

- [ ] `TuiScene` 是 transcript 与所有 bottom/overlay state 的唯一权威来源；
- [ ] SceneController 串行提交 mutation，frame 使用不可变 snapshot；
- [ ] owned interactive 模式只有一个 terminal owner；
- [ ] presenter 之外没有可达的业务 terminal primitive；
- [ ] command/runtime/tool 输出都通过结构化 event/result；
- [ ] fullscreen 通过 screen lease，不销毁主 Scene；
- [ ] plain/JSON renderer 与 owned renderer 生命周期完全分离。

### 19.2 History 和 gap 验收

- [ ] 所有 top-level cell 有稳定 ID/sequence/revision/phase；
- [ ] mutable finalization 不 append 副本；
- [ ] replay/live 使用同一 cell/boundary pipeline；
- [ ] gap 只有一个权威 policy；
- [ ] cell 内部稠密，独立 cell 之间符合规则表且最多一个 gap；
- [ ] ActiveBand/status/prompt/popup 不影响 transcript gap；
- [ ] handoff frontier 单调且 exactly-once。

### 19.3 渲染和生命周期验收

- [ ] command block 在任意下一帧后位置和内容稳定；
- [ ] resize/reflow 不重复或丢失 retained cell；
- [ ] partial write 后能 full repaint 恢复；
- [ ] fullscreen 期间事件不丢失、不污染 alternate screen；
- [ ] release 后从最新 Scene full repaint；
- [ ] cursor、scroll region 和 front buffer 强不变量通过；
- [ ] 旧 compensation/gap/legacy production path 已删除或仅限明确 plain-only 代码。

### 19.4 质量门禁

- [ ] model/layout/presenter/unit/property 测试通过；
- [ ] race 测试通过；
- [ ] commands/ui 全量 Go tests 通过；
- [ ] Windows ConPTY 与非 Windows PTY 验收通过；
- [ ] 长 session、长 tool output、resize storm 性能达到基线；
- [ ] 无正文 telemetry 中 `unowned_terminal_write_total == 0`；
- [ ] 文档、runbook、故障回退流程同步更新。

---

## 20. 架构决策记录

### ADR-01：保留多个逻辑 layer，但只允许一个物理 writer

**决定**：history、ActiveBand、status、prompt、popup 继续分层；统一通过 compositor/presenter 提交。

**原因**：职责分层有利于状态管理；冲突来自它们或业务组件直接竞争 terminal，而不是“层”本身。

### ADR-02：Scene 是真相源，terminal 不是

**决定**：所有恢复、reflow、fullscreen return 均从 Scene 生成。

**原因**：terminal scrollback 和 physical cursor 不可可靠查询，也无法表达 cell identity/revision。

### ADR-03：Gap 是 boundary policy，不是输出字符串

**决定**：gap 由相邻 semantic cells 纯函数生成。

**原因**：可统一 live/replay/resize/handoff，并消除调用顺序布尔状态。

### ADR-04：Finalization 是 replace/commit transaction

**决定**：mutable 与 final 共享 CellID，final 不创建第二份 history cell。

**原因**：从数据模型上消除重复绘制，而不是用文本比对补救。

### ADR-05：Fullscreen 使用 lease，不使用 Disable/Enable

**决定**：alternate screen 临时独占 terminal；主 Scene 暂停 flush 但继续存在。

**原因**：Disable 是 destructive teardown，会清空 retained state。

### ADR-06：不长期双写两个 production renderer

**决定**：feature flag 在 session 层选择 renderer；shadow compare 只比较内存结果。

**原因**：双写本身就是当前问题根因，无法作为稳定迁移方案。

### ADR-07：Native scrollback 是不可变副本

**决定**：semantic history 与 retained tail 保持权威，handoff 后物理行不再 reflow。

**原因**：兼顾原生 scrollback 用户体验、可重绘 viewport 和有限内存。

---

## 21. 建议的首个实施切片

为了以最小风险验证本文架构，建议第一个实现切片只完成以下闭环：

1. 定义最小 `CommandResult{Blocks}` 和 command-to-scene adapter；
2. 将 `/debug display` 从 raw stdout 迁移为一个原子 command cell；
3. 为 cell 分配稳定 `CellID/Sequence`；
4. 使用最小 `BoundaryPolicy` 计算与前一 cell 的 0/1 gap；
5. 测试执行 `/debug display` 后依次触发 status update、prompt repaint、ActiveBand grow/shrink 和 resize；
6. 断言 debug cell 只出现一次、顺序不变、gap 符合 policy、Backend/VT 最终 frame 一致；
7. 增加 command handler direct stdout 禁止测试，防止新增旁路。

该切片不要求先完成全部 Scene 拆分，但其 API 和测试必须符合最终模型，避免再次形成只能服务 `/debug` 的临时方案。

随后第二个切片应迁移 `functions/builder.go` 等 active-turn diagnostics，第三个切片引入 fullscreen lease。完成这三个切片后，再进入 Scene/Layout/Presenter 的核心拆分，可显著降低重构期间双写和状态丢失风险。

---

## 22. 文档维护规则

每次开始或完成一个阶段时，必须同步：

1. 文档顶部状态和更新时间；
2. 第 14 节阶段表的状态、依赖和退出证据；
3. 第 15 节删除清单；
4. 第 16 节新增或反转的回归测试；
5. 第 19 节验收项；
6. 若改变不变量或模型，新增/更新 ADR，并说明替代关系；
7. 证据至少包含测试名、命令结果、VT/golden、PTY/ConPTY 记录之一；
8. 不以“代码已合并”作为完成标准，必须证明对应所有权和不变量成立。
