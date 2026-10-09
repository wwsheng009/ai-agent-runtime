# L5-2b：渲染链接管 ActiveBand 视口（D2-a ②）实施计划

> 性质：L5-2 保留项 D2-a ② 的**评估 + 单批收口**方案。
> 依据：`docs/plan/aicli-l5-2-presenter-popup-geometry-plan-20261009.md` §2 D2-a ②
> 「渲染器接管布局宽度：ActiveBand viewport 由 AppState/渲染链提供」。
> 基线：`feat/render-p0-writer-unification` @ `a0e2bea3`。
> 硬规则：迁移一处 → 从门禁白名单减一；unified/legacy 行为等价由测试钉住。

## 1. 现状锚点（已核实）

- 冻结白名单 4 点（`chat_geometry_family_freeze_test.go`，全在 `chat_interaction.go`，
  均为 `surface.ActiveBandViewportSize()` 的「宽+行」读）：
  - `commitActiveStableScrollbackLocked`（:6420，`plainStableScrollbackCut` 用宽+行）；
  - `currentStreamEmitWidthLocked`（:6826，取宽，回落 `GetTerminalWidth` → 80）；
  - `syncActiveStreamViewportLocked`（:7399，`SetViewport`，回落终端探针）；
  - `refreshActiveStreamViewportLocked`（:7611，`Resize` 覆写，回落终端探针）。
- surface 侧语义：`ActiveBandViewportSize()`（`ui/fixed_bottom_surface.go:1513`）=
  `(terminal.Width(), ActiveBandRows(terminal.Height()))`——**纯终端几何缓存读**，
  无 enabled 门控；`ActiveBandRows` 是导出的高度→行数预算纯函数。
- 渲染链已有等价投影：`UIController.Geometry()`（presenter probe → Resize action →
  `AppState.Geometry` 权威）；`ActiveBandRows(geometry.Height)` 与 surface 同函数。
- 评估结论（本批范围）：
  - **视口值源接管可行且低风险**（4 点迁移 + 白名单清零即 ② 收口判据）；
  - `applyLayout`（`syncTerminalGeometry` 内）在 unified 下仍承重（`ownedViewport=true`
    时 `applyOwnedViewportGeometryLocked` 更新渲染后端几何，`Enable()` :387），
    **物理退役不在本批**；本批后 surface 不再作为 commands 侧流式视口的宽度权威。

## 2. 设计（D1）

**门面** `ui.ActiveBandViewportPort`：

- `ViewportSize() (width, rows int)`：
  - unified（stateSource ok 且 `Width>0 && Height>0`）→
    `(geometry.Width, ActiveBandRows(geometry.Height))`（渲染链投影）；
  - legacy → `surface.activeBandViewportSizeImpl()`（终端缓存原语义）；
  - 无来源 → `(0, ActiveBandMinRows)`（与现 nil surface 口径一致）；
- `Unified() bool`：渲染链几何源可用（供调用点保留 legacy 的 enabled/探针分叉语义）。

**接线**（L5-2c 同构）：

- coordinator `activeBandViewportPort atomic.Pointer[ui.ActiveBandViewportPort]`
  （SetSurface 注入 / Shutdown 清空）；
- 状态源 `activeBandGeometry() (GeometryState, bool)`：actor 存活 → `actor.Geometry()`；
- 解析 `c.activeBandViewport()`：注入端口 → legacy 构造（`c.surface`）→ no-op；
- surface `ActiveBandViewportSize` 主体下沉 `activeBandViewportSizeImpl`（公开方法保留）。

**迁移点**（4 处，保持既有回落分叉）：

1. site 1：`width, rows := c.activeBandViewport().ViewportSize()`（原读点已由
   `surfaceOutputActiveLocked()` 门控）；
2. site 2：`if width, _ := ...ViewportSize(); width > 0 { return width }`（回落不变）；
3. site 3：`viewport.Unified() || (c.surface != nil && c.surface.Enabled())` 时取端口，
   否则终端探针（与旧 enabled 分叉等价）；
4. site 4：同上覆写形状（`w > 0` / `r > 0` 条件保留）。

**门禁**：几何族 `ActiveBandViewportSize` 白名单 **4 → 0**；`SyncTerminalGeometry*` 维持 0。

## 3. 分批

- **Batch A（本批，单批收口）**：门面 + 接线 + 4 点迁移 + 冻结白名单清零 +
  行为等价测试（unified 投影 / legacy 回落 / 无效几何回落 / no-op）。

## 4. 验收

- 门禁：几何族白名单 0（含 SyncTerminalGeometry* 维持 0）；
- 行为：unified 投影 = `geometry.Width + ActiveBandRows(geometry.Height)`；
  legacy = surface 终端缓存（原语义）；几何未测量（0 宽/高）回落 surface/探针；
- 接线：SetSurface/Shutdown 注入/清空；无 coordinator 时 legacy 构造；
- 全量：ui/commands 聚焦绿 + ui 全量绿（主仓）。

## 5. 风险与回滚

| 风险 | 缓解 |
|---|---|
| unified 几何与 surface 终端缓存瞬态不一致（resize 竞态） | 渲染链为 unified 权威（方案 §2 ②）；稳态同源物理尺寸 |
| legacy enabled/探针分叉语义漂移 | 调用点保留 `Unified() \|\| Enabled()` 分叉 + 等价测试 |
| 门禁误伤（新方法名/impl 后缀） | 新方法名 `ViewportSize` 不在冻结集；port 经 impl 回落 |
| 回滚 | 单批一提交，可独立 revert |

## 6. 执行记录

- 2026-10-09 **Batch A（单批收口）**（worktree `aicli/agent/l5-2b` @ `ca664a79`；
  主仓 cherry-pick 同 sha）：
  - 落地：`ui.ActiveBandViewportPort{ViewportSize, Unified}` + coordinator 接线
    （`activeBandViewportPort` 原子指针，SetSurface/Shutdown）+ `activeBandGeometry()`
    状态源（`actor.Geometry()`）；surface `ActiveBandViewportSize` 主体下沉
    `activeBandViewportSizeImpl`（公开方法保留，冻结扫描按公开方法名不受影响）；
  - 迁移：`chat_interaction.go` 4 点（commitActiveStableScrollbackLocked /
    currentStreamEmitWidthLocked / syncActiveStreamViewportLocked /
    refreshActiveStreamViewportLocked）全部经 `c.activeBandViewport()`；几何族冻结
    白名单 **4 → 0**（`TestChatGeometryFamilyDirectReadsFrozen`）；
  - 验证：gofmt 干净 / build ok；worktree 聚焦（ui 1.2s + commands 11.6s，含流式族）
    绿、**ui 全量 14.9s 绿**；主仓集成聚焦（ui 1.4s + commands 11.6s）绿、主仓
    ui 全量绿；
  - 语义：unified 视口 = 渲染链几何投影（`geometry.Width +
    ActiveBandRows(geometry.Height)`）；legacy = surface 终端缓存（原语义）；几何
    未测量（0 宽/高或 ok=false）回落 surface/探针；`applyLayout`/owned-viewport
    应用仍在渲染后端承重，不属本批（见 §1 评估结论）；
  - 保留项：无（D2-a ② 收口）。L5-2 全链（Batch A/B/C + L5-2b + L5-2c）至此完成。
