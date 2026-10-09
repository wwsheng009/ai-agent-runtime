# D3 副屏能力单点收敛方案（Enabled/OwnedViewport/LeaseActive 三联 gate）

> 性质：**独立方案**（由 `aicli-l5-2-presenter-popup-geometry-plan-20261009.md` §2 D3 引出，
> 原文「`Enabled/OwnedViewport/LeaseActive` 三联 gate → 会话级能力查询 helper（单点语义），
> 收敛 62 点位；另行立项」）。
> 基线：`feat/render-p0-writer-unification`（Batch A 落地于 `48027017`）。
> 范围：commands 生产代码中 `Surface.Enabled/OwnedViewport/LeaseActive` 直读面的语义收敛；
> **不改任何 gate 判定结果**（行为等价优先，fail-closed 顺序保持）。

## 1. 现状锚点（2026-10-09 机械枚举）

全量：27 个生产文件 / **60 个点位**（三联方法直接调用；计划文本口径 62，含测试面差异，
以本文复核为准）。按语义分三族：

### A 族——三联 gate（12 处内联，完全相同四联表达式）

`!Surface.Enabled() || !Surface.OwnedViewport() || Surface.LeaseActive() || Popup.HasActivePopup()`

- 10 个 `canOpenChatXxx`：`chat_account_screen.go:87`、`chat_backtrack_select.go:155`、
  `chat_debug_overlay.go:17`、`chat_export_picker.go:23`、`chat_mcp_picker.go:38`、
  `chat_resume_command.go:197`、`chat_skill_picker.go:20`、`chat_theme_picker.go:22`、
  `chat_transcript_pager.go:16`、`chat_usage_screen.go:56`；
- 框架 helper：`chat_picker_common.go:47`（`chatPickerSurfaceReady`）、
  `chat_screen_framework.go:258`（`chatScreenCapability`）。
- 各站点在 gate 前后另有自身前置（`NoInteractive/JSONOutput/Interaction/Surface` 非空、
  `RuntimeEventBridge` 空闲、`CanUseFullScreenList` 终端能力等）——**不收敛**，保留原位。

### B 族——单读（租约/繁忙语义）

- `chat_busy_input.go:19`：`Surface != nil && Surface.LeaseActive()`（忙时输入判定）；
- `chat_screen_framework.go:305`：`Surface.LeaseActive()`（degrade 原因 `busy` 标签）。

### C 族——输出面活跃（`Enabled()` 读，语义 ≠ 副屏 gate）

- `chat_interaction.go` 12 处（writer 归属 :911、prompt 可见 :5690/:5694、几何/刷新
  :7406/:7460/:7495/:7550/:7618 等）、`chat_ui_actor.go:1378`、`chat_surface_output.go`、
  `chat_composer.go`、debug/completion 散点；
- 语义是「统一渲染面正在承载主输出」，收敛需先定义输出面单点（见 §2 D3），
  且与 `surfaceOutputActiveLocked` 等既有内部判定对齐后再动，**不锁本方案判据**。

## 2. 设计

### D1 副屏能力单点（A 族，已落地）

```go
func chatSurfaceScreenGate(session *ChatSession) bool // chat_screen_capability.go
```

= `Enabled ∧ OwnedViewport ∧ ¬LeaseActive ∧ ¬PopupActive`（nil 安全）。
各站点改调 `if !chatSurfaceScreenGate(session) { return false }`，自身前置保持。

**机械门禁**（`chat_screen_gate_freeze_test.go`）：`OwnedViewport()` 生产直读面冻结为
**全仓 1 处**（helper 本体）；内联组合形态零容忍；白名单按「文件 :: 函数」精确匹配。

### D2 租约/繁忙单点（B 族，Batch B 待办）

- 新增 `chatSurfaceLeased(session) bool`（`Surface != nil && Surface.LeaseActive()`）；
- `chat_screen_framework.go:305` 与 `chat_busy_input.go:19` 迁移；
- 门禁扩展：`LeaseActive()` 生产直读冻结为 {helper, 迁移后调用点} 白名单。
- **协调**：`chat_busy_input.go` 当前处于并发 WIP（web/resume 工作流），迁移须等其
  工作区收口后执行（或经 cherry-pick 冲突预检）。

### D3 输出面活跃单点（C 族，Batch C 待办，先设计后迁移）

- 先输出语义清单（12+ 散点逐点归类：writer 归属 / prompt 可见 / 几何刷新 / 调试），
  确认哪些是同一谓词、哪些必须保留独立读；
- 单点候选：`chatSurfaceOutputActive(session)`（会话级「统一渲染面承载主输出」）；
- 逐点迁移 + 机械门禁（`Surface.Enabled()` 白名单收敛）；
- 本批不锁本方案判据（若语义无法单点化，保留并登记）。

## 3. 分批与验收

| 批次 | 范围 | 状态 |
|---|---|---|
| Batch A | A 族 12 处 → `chatSurfaceScreenGate` + OwnedViewport 门禁 | **已执行（`8d4a4d45`/`48027017`）** |
| Batch B | B 族 2 处 → `chatSurfaceLeased` + LeaseActive 门禁扩展 | **已执行（`124967ca`/`bda38888`）** |
| Batch C | C 族逐点语义清单 → 输出面单点（可选/不锁判据） | **已执行（`7211cf70`/`f9f178d6` + `bda38888` 收尾）**：语义清单完成（`surfaceOutputActiveLocked` 既有单点）+ coordinator 链式裸读 8 处全部收敛（含原剩余 `chat_ui_actor.go:1378`）；`TestChatSurfaceOutputEnabledReadsFrozen` 白名单收敛为单点本体；非链式 `Enabled()` 散点（其他接收者/语义）按 §1 登记保留，不属本单点语义 |

每批验收：目标族聚焦绿 + `go vet` + commands 全量（仅剩已登记 flake）+ gofmt 空 +
机械门禁绿；一刀一提交，文档同步。

## 4. 风险

- **行为等价**：单点只收敛完全相同的表达式；各站点前置与 fail-closed 顺序不动
  （已用 12 处「逐字节相同块」替换 + 门禁钉住）；
- **并发协调**：B 族目标文件（`chat_busy_input.go`）在途 WIP；A 族 12 文件与 WIP 零重叠；
- **门禁计数**：每次迁移同步更新冻结白名单与本文档（L4 基线同款纪律）。

## 5. 执行记录

- 2026-10-09 Batch A（`8d4a4d45`，主仓 `48027017`）：单点 helper + 12 处迁移
  （14 files，+156/−24）+ `TestChatScreenGateTripleReadsFrozen` 机械门禁。
  验证：gofmt 空 / build 绿 / vet 绿 / picker·screen 族 + 门禁聚焦绿（3.0s）/
  commands 全量 186.7s 仅剩已登记环境 flake（`StreamingAssistantFinalTail…`，
  隔离 ×2 绿）/ 主仓复验绿。
- 2026-10-09 Batch C（部分，`7211cf70`，主仓 `f9f178d6`）：C 族语义清单（逐点归类）
  + coordinator 7 处裸读收敛至既有单点 `surfaceOutputActiveLocked()`（writer 归属
  `:911`、viewport 分叉 `:7406/:7618`、探针块 `:7460/:7550`、行同步 `:7495`、
  prompt 谓词 `:5690`）+ `TestChatSurfaceOutputEnabledReadsFrozen` 链式门禁
  （白名单含剩余 1 处 `chat_ui_actor.go:1378`，待 WIP 收口后迁移）。
  验证：gofmt 空 / build 绿 / vet 绿 / 冻结门禁 + coordinator·stream 族聚焦绿（3.5s）/
  commands 全量 184.5s 仅剩已登记 flake（`AutoStartTeam…` 隔离 ×3、`StreamingAssistantFinalTail…`
  隔离 ×2 全绿）/ 主仓复验绿。
- 2026-10-09 Batch B + C 收尾（`124967ca`，主仓 `bda38888`，web WIP 收口后）：
  `chatSurfaceLeased` 单点（`chatBusyScreenActiveForSession` + `chatScreenCapability`
  busy 标签 2 处）+ 输出面剩余 1 处（`chat_ui_actor.go:1378`）迁移至
  `surfaceOutputActiveLocked`；双冻结门禁收敛（LeaseActive 白名单 = 2 单点；
  链式 `c.surface.Enabled()` 白名单 = 仅单点本体）。
  验证：gofmt 空 / build 绿 / vet 绿 / 聚焦绿（2.0s）/ commands 全量 183.2s 仅剩已登记
  flake（`AutoStartTeam…`/`TTY_LiveLoop_LLMRetry…` 隔离 ×2 全绿）/ 主仓复验绿。
  **D3 三批次（A/B/C）至此收口。**
