# L5-2c：prompt-editor 门面（邻近族收口）实施计划

> 性质：L5-2 保留项（prompt-editor/composer 邻近族）**提前立项** + 执行方案（单批收口）。
> 依据：`docs/plan/aicli-l5-2-presenter-popup-geometry-plan-20261009.md` §6 保留项——
> 5 点邻近白名单「迁移需先立项 prompt facade（D1 范围外）」。
> 基线：`feat/render-p0-writer-unification` @ `f88b367c`。
> 硬规则：迁移一处 → 从门禁白名单减一；unified 与 legacy 行为等价由测试钉住。

## 1. 现状锚点（已核实）

- 冻结白名单 5 点（`chat_popup_family_freeze_test.go` 邻近族，全在 `commands/chat_composer.go`）：
  - `(*chatComposerController) Close / onChange / onComplete / setStatusLine` ::
    `session.Surface.SetPromptEditorStatusLine`（4 处，`:111/:172/:260/:283`）；
  - `func chatComposerMaxVisibleRows` :: `session.Surface.PromptInputMaxVisibleRows`（1 处，`:147`）。
- surface 侧语义（unified 通道已具备，缺的只是会话门面入口）：
  - `SetPromptEditorStatusLine`（`ui/fixed_bottom_surface.go:1729`）已是 facade：
    `postFacadeAction(SetPromptEditorStatusAction)`（unified → reducer `controller_state.go:658`
    写 `Bottom.PromptEditorStatusLine`），失败回落 `setPromptEditorStatusLineImpl`
    （sanitize + reflow，legacy）；
  - `PromptInputMaxVisibleRows`（`:698`）：terminal 探针 + 预算（legacy 权威）；
  - **渲染层已有 unified 等价投影**：`BottomPanePolicyForGeometry(bottom, geometry).PromptMaxVisibleRows`
    （`bottom_pane_layout_policy.go:30-101`，渲染器同源纯函数）。
- 控制器投影面：`UIController.BottomPaneState()` / `Geometry()`（`controller.go:856/1034`，
  短锁只读，不可重入）。
- 接线范式（Batch B 同构）：`chat_interaction.go:659-670`（SetSurface 注入）/
  `:5615-5617`（Shutdown 清空）原子指针；`chat_ui_actor.go:98-107` 状态源闭包（actor 存活判定）。

## 2. 设计（D1）

**门面** `ui.PromptEditorPort`：

- `SetStatusLine(line string) bool`：unified → `postFacadeAction(SetPromptEditorStatusAction{Line})`
  （与 surface facade 同投递）；否则 `setPromptEditorStatusLineImpl`（legacy 原语义）；
- `MaxVisibleRows() int`：unified（stateSource ok）→
  `BottomPanePolicyForGeometry(bottom, geometry).PromptMaxVisibleRows`（渲染器同源投影）；
  legacy → `surface.promptInputMaxVisibleRowsImpl()`（原探针/预算语义；公开方法保留、主体下沉
  impl，冻结扫描按公开方法名不受 impl 影响）；无 surface → `ChatComposerMaxVisibleRows`
  （与现 disabled/nil 口径一致）。

**接线**：

- coordinator `promptEditorPort atomic.Pointer[ui.PromptEditorPort]`（SetSurface 注入 /
  Shutdown 清空，镜像 popupPort）；
- 状态源 `promptEditorState() (BottomPaneState, GeometryState, bool)`：actor 存活 →
  `(actor.BottomPaneState(), actor.Geometry(), true)`；否则 ok=false（legacy 回落）；
- `commands/chat_prompt_port.go`：`chatSessionPromptPort(session)` 解析
  （coordinator 注入 → legacy 构造 → no-op），始终非 nil。

**迁移点**：`chat_composer.go` 5 处（上表）→ 全部经 `chatSessionPromptPort`；行为等价
（unified/legacy 分叉不变，投递/回落路径同源）。

**门禁**：邻近白名单 `want` 清空（5→0）；消息更新为「已迁移至 chatSessionPromptPort /
ui.PromptEditorPort（L5-2c）；新增直读即违规」。

## 3. 分批

- **Batch A（本批，单批收口）**：门面 + 接线 + 5 点迁移 + 冻结白名单清零 + 行为等价测试；
- 验收：gofmt/build；`TestChatPopupFamilyDirectReadsFrozen`（邻近 0）；新增 ui/commands
  门面测试；ui/commands 聚焦 + ui 全量；主仓集成聚焦。

## 4. 验收

- 门禁：邻近白名单 0；popup 族维持 0；
- 行为：legacy 回落（状态行写入/清除、预算原语义）；unified 投影（policy 等价、
  surface 不变更）；投递被拒回落；
- 接线：SetSurface/Shutdown 注入/清空；无 coordinator legacy 构造；
- 全量：ui/commands 聚焦绿 + ui 全量绿（主仓）。

## 5. 风险与回滚

| 风险 | 缓解 |
|---|---|
| unified 投影与 legacy 预算语义漂移 | policy 为渲染器同源纯函数 + 等价测试钉住分叉 |
| 状态行投递语义回退 | 复用 `postFacadeAction` 原通道；legacy impl 原样保留 |
| 门禁误伤（impl 方法名匹配） | 冻结扫描按公开方法名；impl 后缀不匹配 |
| 回滚 | 单批一提交，可独立 revert |

## 6. 执行记录

- 2026-10-09 **Batch A（单批收口）**（worktree `aicli/agent/l5-2c` @ `0c8bb08f`；主仓
  cherry-pick `16698f11`）：
  - 落地：`ui.PromptEditorPort`（SetStatusLine/MaxVisibleRows）+ `commands/chatSessionPromptPort`
    解析（coordinator 注入 → legacy 构造 → no-op）+ coordinator 接线（`promptEditorPort`
    原子指针，SetSurface/Shutdown）+ `promptEditorState()` 状态源（BottomPaneState +
    GeometryState）；surface `PromptInputMaxVisibleRows` 主体下沉
    `promptInputMaxVisibleRowsImpl`（公开方法保留，冻结扫描按公开方法名不受影响）；
  - 迁移：`chat_composer.go` 5 点（Close / onChange / onComplete / setStatusLine /
    chatComposerMaxVisibleRows）全部经门面；冻结门禁邻近白名单 **5 → 0**
    （`TestChatPopupFamilyDirectReadsFrozen`）；
  - 验证：gofmt 干净 / build ok；worktree 聚焦（ui 1.3s + commands 2.6s）绿、
    **ui 全量 13.7s 绿**；主仓集成聚焦（ui 1.2s + commands 3.5s）绿、主仓 ui 全量绿；
  - 语义：unified 状态行直投 `SetPromptEditorStatusAction`（reducer 权威）、预算走渲染器
    同源投影（`BottomPanePolicyForGeometry`，随 ActiveBand 占行收缩/几何封顶）；
    legacy 回落 surface impl（原探针/预算语义）；投递被拒回落一致；
  - 保留项：无（本项收口）。L5-2 全链（Batch A/B/C + L5-2c）至此完成。
