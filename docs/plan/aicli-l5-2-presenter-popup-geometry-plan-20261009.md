# L5-2 presenter popup/几何 API → surface facade 读退役（方案）

> 性质：**独立执行方案**（由 `aicli-render-l5-candidates-20261009.md` §2 升级）。
> 基线：`feat/render-p0-writer-unification` @ `398b4ca8`（L5-1 收口后；写端受认可 17 条/20 点位）。
> 触发评估（2026-10-09）：候选触发条件（需要统一 popup/几何能力提升；或继续瘦身 surface 排期）
> 尚无现场需求；经用户「继续」指令**提前立项**（设计先行；执行按分批节奏）。
> 上游：候选评估 §2、`aicli-legacy-fallback-retirement-plan-20261008.md` §4.4 D 类、
> `docs/aicli/tui-render-architecture.md`。

## 1. 现状锚点（2026-10-09 机械复核）

### 1.1 两族点位（commands 非测试）

- **popup 族 39 点位 / 17 文件**：`chat_surface_output.go` 16、`chat_composer.go` 6、
  `chat_debug.go` 3、`chat_agent_transcript.go` 2，其余 14 文件各 1
  （account/backtrack/debug_overlay/export/mcp/picker_common/resume/screen_framework/
  skill/theme/transcript_pager/usage 等）。
  方法分布：`HasActivePopup` 12、`ClearPopupForOwnerPreserveCursor` 5、
  `SetPromptEditorStatusLine` 4、`ShowPopupInputForOwner` 3、
  `BeginPopupInputForOwnerWithViewport` 2、`BeginPopupInputForOwner` 2、
  `UpdatePopupInputForHandle` 2、`ClearPopupHandlePreserveCursor` 2、
  `ShowPopupPreserveCursorForOwner` 2、其余单点（ShowPopupInputPreserveCursorForOwner、
  ShowPopupPreserveCursorForOwnerBelowPrompt、ShowPendingPastePreview、
  ClearPendingPastePreview、PromptInputMaxVisibleRows）。
- **几何族 4 点位**：`chat_interaction.go:7418/7421`（`SyncTerminalGeometry` /
  `SyncTerminalGeometryThrottled`，live paint path）、`chat_interaction.go:7501`
  （`SyncTerminalGeometry`，显式刷新）、`chat_debug_document.go:982`
  （`ActiveBandViewportSize` 读宽）。
- **门族（D3，后续，不锁本方案判据）**：`Enabled` 36、`LeaseActive` 14、`OwnedViewport` 12
  —— `if !session.Surface.Enabled() || !session.Surface.OwnedViewport() || ...` 三联 gate。
- facade 总量：`.Surface.*` = **122 处 / 29 方法**（含以上各族）。

### 1.2 关键事实（决定设计）

1. **unified 侧 popup 状态机已存在**：`ui/bottom_pane_popup_state.go` 实现
   `applyPopupShow/Begin/Update/Clear`（owner 优先级 `popupOwnerPriority`、栈恢复
   `restorePopupLayerFromStack`、`bottomFocusForPopup` 光标归属、viewport/reserved rows）；
   reducer 接线 `ui/controller_state.go:680-685`。
   surface 的 popup facade（`popupLines/popupOwner/popupInstance/popupViewport`、
   `setActivePopupStateLocked` :2650）经 `postFacadeAction`（:1298，uiPoster 非 nil 时投递
   `Show/Update/ClearPopupAction`）构成「unified 直投 + legacy 本地回落」双态。
2. **几何链**：live paint path 在 unified 下经 `surface.SyncTerminalGeometry*` 只刷新
   presenter 读取的缓存（`syncTerminalGeometry` :650：`terminal.RefreshSize()` +
   `applyLayoutWithSizeLocked`，节流 `DefaultGeometryProbeMinInterval=100ms`）；
   Resize 实际走 presenter probe（`c.primaryTerminalGeometry`，`chat_ui_actor.go:248/317`）
   →`publishGeometry`（`terminal_session_presenter.go:118`）→ AppState.Geometry；
   legacy 直报 `reportMeasuredSurfaceGeometryLocked`（:7532）仅非 unified。
3. **命令侧 seam**：`chat_surface_output.go` 的 `chatPromptOverlay`
   （beginSelectionPopup/beginModalPopupInput/renderModalPopupInput/updatePopupInput/
   clearPopupHandle/clearPopupHandlePreservePromptInput/…）是 popup 迁移主接缝；
   `HasActivePopup` 12 处为读侧门。
4. **布局宽度仍在 surface**：`currentStreamEmitWidthLocked`（:6786）先读
   `surface.ActiveBandViewportSize()`；软重排宽度的所有权迁移依赖渲染器接管布局宽度。

### 1.3 目标与完成判据

**目标**：popup/几何两族调用方不再直读 `.Surface.*`；unified 下 popup 状态所有权归
controller/AppState（已具备），几何刷新经 presenter/session 门面；surface facade 两族读退役。

**完成判据（本方案只锁两族）**：

- commands 非测试零 `.Surface.BeginPopup*/ShowPopup*/UpdatePopupInputForHandle/
  ClearPopup*/HasActivePopup/SyncTerminalGeometry*/ActiveBandViewportSize` 直读；
- unified 弹层行为等价：merged composer / body-only popup 两形态、光标归属
  （BottomFocus 派生）、popup 优先级与栈恢复、handle FIFO（begin 先分配后投递）；
- 几何节流契约（100ms）与「软宽漂移立即重排」语义不回退；
- ui/commands 全量绿 + 单写端 fence + fixture e2e 8/8 + compat 6/6。

## 2. 设计（D1/D2/D3）

### D1 popup 所有权上移（Batch B）

- 新增**会话级 popup 门面**（暂定 `ui.PopupPort`，经 session 注入 commands）：
  `Begin/Show/Update/Clear + HasActive + handle 生命周期`；语义与现 facade 一致
  （owner/viewport/belowPrompt/preserveCursor/优先级/栈）。
- **unified**：门面直接投递 `Show/Update/ClearPopupAction` 到 controller（复用
  `BottomPaneState` 状态机）；handle 分配上移（surface `nextPopupInstance` → 门面/actor，
  保持「先分配 token 后投递」FIFO 语义）。
- **legacy/compat**：门面回落 surface 本地实现（compat 真机验证前不删 legacy 路径）。
- 迁移接缝：`chatPromptOverlay` 全部方法改经门面；`HasActivePopup` 读迁为门面查询
  （unified = `BottomPaneState` 查询，legacy = surface）。
- 收尾：surface popup facade 字段与 `setActivePopupStateLocked` 降级为 legacy-only
  （compat 保留）或删除（compat 判定后）。

### D2 几何同步收编（Batch A）

- 新增**几何门面**（暂定 `ui.GeometrySyncPort`，由 `*FixedBottomSurface` 实现并注入
  coordinator）：`RequestGeometrySync(minInterval) (sizeChanged, probed bool)` +
  `ActiveBandWidth() int`；内部 = 现 `syncTerminalGeometry`（RefreshSize +
  applyLayoutWithSizeLocked + 节流）与 `ActiveBandViewportSize`。
- **决策点 D2-a（推荐 ①）**：
  - ①（本批采用）**接口注入收口直读**：commands 经注入接口调用，surface 内部实现保持
    现状（布局宽度仍归 surface）；门禁判据 = 零 `.Surface.` 直读（接口注入不算）。
  - ②（后续 L5-2b）**渲染器接管布局宽度**：ActiveBand viewport 由 AppState/渲染链提供，
    surface 布局应用退役；依赖渲染器能力提升，另行评估。
    （**已收口**：L5-2b，`ca664a79`；见 `docs/plan/aicli-l5-2b-renderer-activeband-viewport-plan-20261009.md`。）
- 迁移点：`maybeRefreshStreamGeometryLocked`（:7414-7429）改经门面；显式刷新 :7501；
  `chat_debug_document.go:982` 读宽改经门面。
- 保持：`reportMeasuredSurfaceGeometryLocked`（legacy 直报）不动；unified 下
  Resize 仍走 presenter probe → AppState.Geometry。

### D3 门族收敛（后续，不锁本方案判据）

- `Enabled/OwnedViewport/LeaseActive` 三联 gate → 会话级能力查询 helper（单点语义），
  收敛 62 点位；另行立项。

## 3. 分批

### Batch A（几何族，垂直切片）

- D2 门面（接口 + surface 实现 + coordinator 注入）+ 3 处迁移点；
- ui/commands 单测：节流契约（100ms、软漂移立即重排）、探针计数、unified/legacy 分叉；
- 门禁机械检查：几何族零直读（可加 grep 断言测试）。

### Batch B（popup 族）

- D1 门面（unified 直投 controller；handle 上移）+ `chatPromptOverlay` seam 迁移
  （16+6+3+2+14×1）+ `HasActivePopup` 读迁移；
- 行为等价测试：merged composer / body-only popup、优先级/栈、handle FIFO。

### Batch C（验证与回填）

- 两族零直读机械门禁 + ui/commands 全量 + fence + fixture e2e 8/8 + compat 6/6；
- 回填本文 §6、候选评估 §2、P0 台账/退役方案状态列。

## 4. 验收（每批适用）

- 门禁：两族零 `.Surface.` 直读（Batch B 完成后全族断言）；
- 行为：merged composer/body-only popup 两形态、光标归属、popup 优先级/栈恢复、
  几何节流 100ms 与软漂移立即重排；
- 全量：ui/commands 绿（并发 web 用例 skip 口径同 L5-1，收口后无 skip 复跑）+ fence +
  fixture 8/8 + compat 6/6。

## 5. 风险与回滚

| 风险 | 缓解 |
|---|---|
| popup 所有权交界（merged composer / body-only） | 先迁移 seam 不改语义；对照 L3-3 边界测试逐点验证 |
| handle FIFO/优先级语义回退 | token 分配上移时保持「先分配后投递」；补顺序测试 |
| 几何节流语义回退（30FPS 探针 / 软漂移） | 门面保持 minInterval 契约 + 探针计数测试 |
| legacy/compat 回落面 | 回落路径保留至 compat 真机验证；每批一提交可 revert |
| 并发工作流（web）干扰全量 | skip 口径 + 收口后无 skip 复跑 |

## 6. 执行记录

- 2026-10-09 **Batch A**（`492f3f86`；worktree 执行 + 主仓复验）：
  - D2-a ① 落地：`ui.GeometrySyncPort{RequestGeometrySync, ActiveBandWidth}`（surface 实现，
    编译期断言锁定）；coordinator `geometrySync` 注入/卸载/清空；3 处直读迁移
    （`maybeRefreshStreamGeometryLocked` 探针、`refreshActiveStreamViewportNow` 显式刷新、
    `chatDebugDocumentWidth` 读宽）；`reportMeasuredSurfaceGeometryLocked` 与 unified/legacy
    分叉语义不动；
  - 机械口径（commands 非测试）：`SyncTerminalGeometry*` **0 命中**；`ActiveBandViewportSize`
    仅剩 4 处宽+行布局读（D2-a ② 债务，`TestChatGeometryFamilyDirectReadsFrozen` 按
    file::func 白名单冻结，行号漂移免疫）；
  - 验证：gofmt 干净；`go build ./...` ok；ui 门禁 ok 1.4s；ui 全量 ok 13.7s（主仓复跑；
    首跑撞已登记环境 flake `TestTerminalSessionExecutorClaimMissReleasesStrandedInFlight`，
    隔离 ×3 绿后复跑绿）；commands 聚焦（geometry+fence+startup）ok 2.0s；worktree 侧
    ui 全量 14.7s + vet/build 干净；commands 全量（-skip web 在途用例）唯一失败为已登记
    team/streaming 环境 flake（纯基线同跑同败）；
  - 未决：D2-a ②（渲染器接管布局宽度，L5-2b）；Batch B/C 未启动。

- 2026-10-09 **Batch B**（`f2d1e6f0` + `93089990`；worktree 执行 + 主仓集成）：
  - D1 落地：`ui.PopupPort`（Begin/Show/Update/Clear + HasActive + pending paste；unified 直投
    Show/Update/ClearPopupAction 复用 BottomPaneState，legacy/compat 回落 surface；handle 分配
    上移门面边界经共享 `allocatePopupInstance`，token 单调唯一/FIFO）；`UIController.BottomPaneState()`
    轻量访问器；coordinator 同型注入（atomic.Pointer，SetSurface/Shutdown 生命周期）；
    `chatPromptOverlay` 16 处 + 其余 popup 族迁移 + HasActivePopup 12 处读迁移；
  - 机械口径（commands 非测试）：popup 状态机族 **0 直读**（接收者为 `chatSessionPopupPort(...)`
    的调用视为合规门面调用）；邻近白名单 5 处（`SetPromptEditorStatusLine`×4 /
    `PromptInputMaxVisibleRows`×1，prompt-editor/composer 组、§1.3 判据外）按 file::func::method 冻结；
  - 执行过程：子代理 run 在第三次全量等待期 stall（进度阈值）→ 父会话 `extend_deadline` +30m；
    收尾回合未再执行（run 终止）→ 父会话接管：close 后 worktree/分支被运行时清理，经对象库恢复
    （cherry-pick 2 提交；与并发工作流 WIP 零文件重叠）；
  - 验证：恢复 worktree（clean 基线 + 本批）gofmt 干净 / build ok / ui 门禁 1.5s / **ui 全量 14.5s 绿** /
    commands 聚焦（Popup|Geometry|fence|startup）2.3s 绿；主仓集成聚焦（Popup|Geometry）2.3s 绿；
    commands 全量 177.4s → 2 失败（`TestTTY_LiveLoop_LLMRetryRendersAdvancingTimerE2E`、
    `TestStreamingAssistantFinalTailTransfersExactlyOnceToNativeHistory`），隔离 ×3 全绿
    （环境 flake；后者与 Batch A 记录同源）；
  - 未决：D2-a ②（L5-2b）；Batch C 未执行；prompt-editor/composer 邻近族迁移需先立项。

- 2026-10-09 **Batch C**（验证收口，无代码变更；HEAD `60922b95` 纯净 worktree 执行）：
  - 门禁：ui writer inventory `ok` 1.5s；两族零直读冻结 + 单写端 fence `ok` 2.4s；
  - ui 全量 `ok` 14.3s；**commands 全量（无 skip）`ok` 178.4s（exit 0）**——首跑 184.2s 仅 1 例
    已登记环境 flake（`TestStreamingAssistantFinalTailTransfersExactlyOnceToNativeHistory`）；
    复验：基线（`398b4ca8`）与 HEAD 各 ×5 全绿（`TestTTY_LiveLoop_LLMRetryRendersAdvancingTimerE2E`
    同法复验绿；两者与 L5-1/L5-3 记录同源，非本批引入）；
  - fixture 真机 e2e **8/8 PASS**（exit 0；history exactly-once / 滚动可达 / replay 无 CSI 3J /
    prompt-status 单例 / Markdown 单次 / 流式采样无重复 / finalized 单次）；
  - compat 脚本（E2E-COMPAT-01）**6/6 PASS**（exit 0；无 TUI 启动 / mock 回环 / 回复落 stdout /
    优雅退出 / 无 unified 渲染字节）；
  - 回填：本文 §6、候选评估 §2、P0 台账、退役方案 §4.4/§L5/风险表。
    **L5-2 三批次收口（`492f3f86`/`f2d1e6f0`/`93089990`；收口验证 2026-10-09）。**
  - 保留项：~~D2-a ②（渲染器接管布局宽度）~~（**已收口**：L5-2b，`ca664a79`；几何族
    冻结白名单 4→0）；~~prompt-editor/composer 邻近族~~（**已收口**：L5-2c，
    `16698f11`；冻结白名单 5→0）；legacy/compat 回落面（待 compat 判定后收）。
