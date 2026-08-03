# aicli TUI 渲染简化：实施指导（施工契约）

> 上位文档：`docs/plan/aicli-tui-owned-render-simplification-plan.md`（what/why，方案）
> 评审文档：`docs/analysis/aicli-tui-owned-render-simplification-plan-review.md`（缺口 G1-G8）
> 本文档：**how + 约束 + 验收**。回答"每一步做什么、不许做什么、做到什么程度算完成"。
> 并行专项：`docs/plan/aicli-tui-scene-presenter-convergence-design.md`（C0-C4：AICLI_SCENE_PRESENTER 双跑收敛 → 单一 Scene 渲染，本指导 Phase 2/6 引用）
> 状态：**起草（待评审通过后执行）**
> 适用范围：`backend/cmd/aicli/ui/` + `backend/cmd/aicli/commands/` 的渲染相关改动

---

## 0. 使用方式（先读这里）

本文档不是又一份方案，而是**逐条可对照检查的施工契约**。每个实施提交前，必须逐条过一遍：

1. **§2 铁律**：本次改动是否违反任何一条？（违反 = 立即停手，改设计，不许带着违规提交）
2. **§4 阶段清单**：本次改动属于哪个阶段的哪个任务？（不属于任何已批准任务 = 不许动）
3. **§5 提交纪律**：测试是否先于实现更新？提交粒度是否满足？
4. **§6 门禁命令**：本地是否全绿？
5. **§8 红线**：是否触发了必须回滚的红线？

任何"顺手重构"、任何"先改代码再补测试"、任何"新增第 N 套账本"都不在本契约允许范围内。

---

## 1. 总目标与范围

### 1.1 目标（一句话）

把"**一份数组三份所有权 + 提交→失效→猜测→补偿**"简化为"**两处物理所有者（committed scrollback / mutable tail） + 单一提交器（AppendNewRows / ScrollExistingRows）+ 单调 committedBoundary**"，并最终让 **Scene/historyCell 语义源成为唯一权威**（AICLI_SCENE_PRESENTER 从 flag 变为默认）。

### 1.2 范围边界

| 在范围内 | 不在范围内（只读，禁止改动） |
|---|---|
| historyWindow / handoff / softOutput / committedBoundary 相关（D1-D9、N1-N5） | prompt / composer / popup / status 行布局逻辑 |
| 两个专项失败测试 + vt.Screen 语义断言工具（N6） | ScreenLease（DEC 1049）主流程 |
| `historyCell` 迁移（user-echo 已过 → assistant/tool/supplement） | `chat_runtime_events.go` 事件桥的业务逻辑（只允许动 Scene presenter flag 的切换点） |
| FramePump 调度收敛（L4，Phase 6） | 输入事件系统、SSE 消费、会话持久化 |
| `/debug` 观测字段（committedBoundary/mode/mutableTail） | terminal write lock、批量 flush 机制 |

---

## 2. 铁律（不可违反的不变量）

每一条都是对当前 bug 根因的直接禁止。**新增违反铁律的代码 = 提交被拒**。

| # | 铁律 | 违反的判定方式（代码审查时对照） |
|---|---|---|
| IR-1 | **一行文本的字节流出现次数 ≤ 1（跨提交原子性）**：任何已进入 scrollback 的行，不得因滚动/恢复/重绘再次出现在输出流中 | `vt.Screen` 回放 + 语义行 ID 记账断言（Phase 0 落地后成为 hard gate） |
| IR-2 | **不得新增任何"账本/补偿"状态**：禁止新增 handoffFrontier 类、headroom 类、reserve-debt 类、字符级 prefix-len 类字段。需要对齐时只用 `committedBoundary`（行级单调）或 Scene cell `Revision` | grep 新增 struct 字段：`*Frontier*`、`*Headroom*`、`*Debt*`、`*PrefixLen*` |
| IR-3 | **单一提交器**：所有向 scrollback/终端的文本写入必须收敛到 `AppendNewRows`（新内容）与 `ScrollExistingRows`（纯平移）。禁止新增 `insertHistoryLines*` / `writeSurfaceOutputText*` 类直写调用点 | grep 生产代码中的 `\r\n` 构造点与 `WriteString` 调用，逐一归属到两个原语 |
| IR-4 | **ScreenModel 是唯一渲染状态所有者**：surface 上不得存在与模型重复的行缓存（`historyWindow` 是 Phase 3 前的过渡态，转换完成后删除）；softOutput 只允许存在于 mutable tail 内（INV-S5） | /debug 对比 ScreenModel front 与 surface 行缓存的一致性 |
| IR-5 | **几何变化 = 模型平移**：band 出现/消失、bottom 增减一律走 `ScrollExistingRows`（screen_model.go:103-137 已有 ScrollUp/Down/ScrollRegionUp/Down，只缺接线）。禁止用文本提交模拟滚动（方案 D2 的删除目标） | grep `commitExcessHistoryToScrollbackLocked` 调用点：Phase 1 后只允许剩 0 个 |
| IR-6 | **resize/reflow 允许重发，禁止增量补偿**：重建代价已知且可封顶（Codex 式 source replay）；任何"补偿性文本提交"都是 bug | 评审 diff：出现"重发文本修复空白"的提交 = 违规 |
| IR-7 | **语义先于字节**：测试断言以"语义行出现次数"为准，字节计数降级为辅助诊断。禁止用改断言的方式掩盖实现缺陷（语义澄清须按方案 §4.2 规则书面记录） | 测试 diff 审查 |
| IR-8 | **不扩大范围**：改动必须属于当前阶段的任务清单。prompt/popup/status/lease 相关文件（除非清单点名）只读 | 提交 diff 的文件清单 vs 阶段任务文件白名单 |
| IR-9 | **flag 是唯一开关**：Scene presenter 权威切换只能通过 `AICLI_SCENE_PRESENTER`（chat_runtime_events.go:196-206，默认关，双跑对照）。禁止在阶段 0-4 悄悄把权威切走 | 审查默认值/环境变量读取点 |
| IR-10 | **cursor 是状态不是序列**：不得新增 cursorHide/cursorShow/move 操作序列；光标位置在 flush 末尾作为状态更新（对齐 Codex `last_known_cursor_pos`） | grep 新增 `\x1b[?25` 序列 |

---

## 3. 现状勘误（实施前必须更新的既有认知）

以下事实与方案 §9 复核记录不一致，**实施者不得按过期信息行动**：

| # | 过期认知（方案 §9） | 实测现状（2026-08-03） | 对实施的影响 |
|---|---|---|---|
| E1 | "ScreenModel 现有 API 无 ScrollUp/ScrollDown，N1 为真实新增" | `screen_model.go:103/112/122/137` 已有 `ScrollUp/ScrollDown/ScrollRegionUp/ScrollRegionDown`，且有 `screen_model_scroll_test.go` | **N1 不是新增，是接线**：阶段 1 只做调用点切换 + 语义核对 |
| E2 | Scene 是"未接线的未来" | `AICLI_SCENE_PRESENTER` flag + 双跑模式已生产落地（`chat_runtime_events.go:196-206`）；`historyCell` 接口 + `cellIdentity`（ID/Seq/CauseID）+ `DisplayLines(width)` 已落地（`chat_history_cell.go:39-55`），P4.1 user-echo 已路由 | 阶段 2-3 的"Scene 接线"实际是"打开开关 + 完成剩余 block 迁移"，不是从零开始 |
| E3 | 阶段 0 只需改两个测试 | ui 包另有 3 个 `zz_` 前缀诊断测试（`zz_diag_duplicate_probe_test.go`、`zz_probe_reconcile_test.go`、`zz_dump_stream_test.go`），`chat_*` 侧有 20+ 个 blank/parity/identity/invariant 类测试（`chat_interaction_live_vs_replay_blank_test.go` 25KB、`chat_interaction_midstream_blank_test.go` 22KB、`chat_surface_reserve_scroll_invariant_test.go` 15.6KB 等） | 阶段 0 必须盘点这批测试的断言语义，逐个标注"保留/改写/删除"；`zz_` 文件在阶段 5 删除 |
| E4 | CI hard gate 覆盖渲染 | `release-aicli.yml` validate job **不含 `cmd/aicli/ui`**，仅 soft gate 覆盖 | 阶段 0 一并补 hard gate（§6 门禁） |
| E5 | —（新增） | `chat_runtime_events_test.go` 223KB 是 commands 包最大测试文件，内含大量 parity 断言 | 任何影响 Scene presenter 双跑的改动，必须同时跑通该文件，否则 flag 打开即回归 |

---

## 4. 阶段执行计划（入口 → 任务 → 出口）

> 每阶段独立提交、独立可回滚。**阶段之间不得并行**（R6 half-mode 风险）。方案 §7 的阶段 0-5 保留，此处补齐每阶段的入口/出口与任务分解，并新增 Phase 6（L3/L4 收敛，来自评审报告 G2/G3）。

### Phase 0：测试语义改造（先行，单独提交）

**入口**：无（首个提交）。
**任务**：

1. 扩展 `vt.Screen` 保留滚出行（N6）：`ui/vt/screen.go` 的 `index()`/`reverseIndex()` 当前丢弃/清空滚出行（`fixed_bottom_surface_band_restore_duplicate_test.go:25` 注释自述 "A vt.Screen cannot see this"）。扩展后回放可断言 scrollback 序列。
2. 建立**语义行 ID 记账断言工具**（评审 G7/G8）：每个测试可声明"行 X 在字节流中出现 ≤ N 次"；同时暴露进程级重复计数（INV-S1 计数器）供 `/debug` 与 CI 断言。
3. 改写两个专项测试为语义断言（方案 §4.3）：
   - `TestFixedBottomSurface_BandShrinkRestoreNeverReplaysHandedOffHistory`
   - `TestBottomReserveShrinkRestoresHistoryWithoutBlanktingTop`
4. **盘点并标注**（E3）：`fixed_bottom_surface_*` 的 6 处 cap/headroom 断言（`fixed_bottom_surface_history_window_test.go`）、`chat_interaction_live_vs_replay_blank_test.go`、`chat_interaction_midstream_blank_test.go`、`chat_surface_reserve_scroll_invariant_test.go` 的断言语义，输出"保留/改写/删除"清单（写进本阶段提交说明）。
5. CI 补强（E4）：`release-aicli.yml` validate job 增加 `go test ./cmd/aicli/ui/... -count=1`（windows-only 文件由 build tag 排除）。

**出口（通过标准）**：

- 两专项测试仍失败（或部分转黄），但失败原因已符合 §4.2 语义（不再是被陈旧断言误伤）；
- 语义行 ID 断言工具可独立运行：`go test ./cmd/aicli/ui/ -run TestSemanticLineLedger -v` 绿；
- `zz_` 文件清单已列入阶段 5 删除计划。

### Phase 1：几何变化去文本提交

**入口**：Phase 0 已提交。
**任务**：

1. `applyOwnedViewportGeometryLocked`（fixed_bottom_surface.go:2971/2999/3028）：bottom 增长从 `commitExcessHistoryToScrollbackLocked` 改为 `ScrollExistingRows` 模型平移 + 终端滚动序列（R1：行数以**物理行**计算，复用 `expandHistoryLinesLocked` 思路；R4：不依赖 RI/SD 拉回，统一"模型平移 + diff 重画恢复行"）。
2. 删除 D2/D6：`CommitRange(1, regionBottom)` 对已知滚动的覆盖补偿（`appendOwnedDirectPaintLocked` 尾部）。
3. 阶段 1 结尾（提交前）：grep 确认 `commitExcessHistoryToScrollbackLocked` 调用点归零（IR-5）。

**出口**：测试 2 绿；测试 1 的 `@3354` 类重复消失（只剩 diff 猜测类，Phase 2 处理）。

### Phase 2：单调提交器 AppendNewRows

**入口**：Phase 1 已提交。
**任务**：

1. `AppendNewRows` 成为唯一文本写入入口：直写路径（`appendOwnedDirectPaintLocked`）、全帧路径（`renderOwnedViewportLocked`）、reflow 路径（`RewriteSoftOutputTail` owned 分支）全部收敛；删除 D3/D4/D5（Invalidate 补偿、直写路径独立 frontier 计算与窄区变体、正常路径 Invalidate 重建补偿）。
2. 在**双跑对照确认 parity 之后**，把 `AICLI_SCENE_PRESENTER` 对完整块可见行的权威切换纳入本阶段验证面（跑通 E5 的 `chat_runtime_events_test.go` 全量）。双跑门禁化（C0）与完整块强权威（C1）见 `aicli-tui-scene-presenter-convergence-design.md`；**C1 必须排在本阶段（Phase 1-4）全部完成后执行**（mismatch 即错误会把旧路径重复渲染 bug 暴露成红）。
3. 提交前核对 IR-3：grep 生产代码中所有 `\r\n` 文本构造点，逐一归属 `AppendNewRows` / `ScrollExistingRows`。

**出口**：测试 1 绿（`@1749` 类 diff 重复消失）；两专项测试全绿。

### Phase 3：删除双保留与 trim

**入口**：Phase 2 已提交。
**任务**：

1. D1：`keepForRestore = visible + historyWindowHeadroom`（:4195）删除——可恢复性由"模型持有全部 mutable 行"保证。
2. D8/D9：legacy reserve debt、`historyWindow → mutableRows` 整体 commit 语义；删除 `historyWindowMaxLines=400` 安全网（超可见容量即整体 commit，方案 §9-3）。
3. **同步迁移测试**（方案 §9-4）：`fixed_bottom_surface_history_window_test.go` 的 6 处 cap/headroom 断言删除，改写为 `committedBoundary` 单调 + `mutableRows` 有界断言。
4. **historyCell 迁移推进**（E2）：assistant/tool/supplement block 路由到 `historyCell.DisplayLines(width)`，替换 `Formatter.Format` 的独立渲染调用点（评审 G2 的"双渲染调用点"收敛第一步）。

**出口**：`go test ./cmd/aicli/ui/... -count=1` 全绿；`/debug` 显示 `committedBoundary` 单调。

### Phase 4：组帧收缩

**入口**：Phase 3 已提交。
**任务**：

1. D7：`composedPlanLocked`（`fixed_bottom_surface_snapshot.go:156`）从"无条件全量历史组帧"改为只消费 `mutableTail`（boundary 之后）。
2. N5：Frame/Scrollback 分支清晰化；`frameMode` 双路径并存允许保留到本阶段结束（R6），但调用点已全部收敛。

**出口**：全量测试 + `/debug` 观测（paint trace 每帧行数 = mutableTail 行数）；流式输出下 CPU/GC 可观测下降（`active_stream_benchmark_test.go` 对照）。

### Phase 5：清理

**入口**：Phase 4 已提交。
**任务**：

1. 删除 `HandoffFrontier.TrimPrefix/Clamp`（handoff_frontier.go:48/60）、legacy reserve debt 残留、未用导出符号（`go vet` + 未用符号扫描）。
2. 删除 3 个 `zz_` 诊断测试文件（E3）。
3. 文档同步：`aicli-tui-single-scroll-channel-plan.md` 标记为被本方案取代；更新本方案 §9 的 E1-E5 勘误。

**出口**：`go vet ./cmd/aicli/ui/...` + 全量测试干净；diff 只增不删的符号清单为 0。

### Phase 6（评审新增）：L3/L4 收敛

> 来源：评审报告 G2（coordinator 五副本 + 三套账本）、G3（四时钟调度）。方案原 §7 不含此阶段——**本阶段才是"历史消息重复渲染"在用户侧的最后闭环**。若不做，问题仍在。
> **本阶段与 Scene presenter 收敛 C2（mutable tail Scene 化）合并执行**，完整定义见 `aicli-tui-scene-presenter-convergence-design.md`；执行顺序：owned Phase 0 → C0 → Phase 1-4 → C1 → Phase 5 → C2+Phase 6 → C3 → C4。

**入口**：Phase 5 已提交；`AICLI_SCENE_PRESENTER` 默认开启观察 ≥ 1 个发布周期。
**任务**：

1. **L3-1 删除字符级账本**：`streamEnqueuedPrefixLen` / `streamRenderedPrefixLen` 删除，改由 Scene cell `Revision` 对齐（`chat_interaction.go` 中同步 `streamLastFinalDivergence` 等）。
2. **L3-2 收敛双渲染调用点**：`commitActiveStableScrollbackLocked`（:4314）的 live band 稳定前缀提交与历史 replay 统一从 `historyCell.DisplayLines(width)` 取源；删除 `writeSurfaceOutputTextLocked`（:1172）直写路径（若 Phase 3 未完成）。
3. **L3-3 单真相源**：`streamBuffer` / `ActiveStreamController` 缓冲不再作为渲染源（只保留为背压/节流），渲染源 = Scene 快照。
4. **L4-1 调度收敛**：`FrameKeyActiveFrame/DynamicStatus/Prompt/StableCommit` 四个 key 收敛为单一帧调度（dirty 分类保留，时钟唯一）；`scheduleActiveStableCommitLocked` / `drainActiveStableCommitCatchUpLocked` 并入统一帧循环。
5. **L4-2 事件流**：input / render-intent / SSE 输出 / lease 事件合并为单一事件队列（仿 Codex `TuiEventStream`），reducer 单一入口。

**出口**：全量 `go test ./...` 绿；`chat_runtime_events_test.go`（223KB）全绿；双跑 parity 断言零偏差；`/debug` 显示渲染源 = Scene revision（非 buffer 副本）。

---

## 5. 提交纪律（每个提交必须满足）

1. **测试先行**：每个任务先写/改语义断言（Phase 0 工具之上），再动实现。提交里实现与断言同 commit，禁止"先实现后补测试"。
2. **单阶段单提交**：一个提交只属于一个阶段的一个任务。混合两个任务 = 拆开再提交。
3. **提交信息模板**：`render(owned): P<n>-<task> <动词> <对象>`，如 `render(owned): P1-1 replace commitExcess with ScrollExistingRows in applyOwnedViewportGeometryLocked`。提交说明必须附：阶段、铁律自检结果、E1-E5 勘误引用。
4. **文件白名单**：提交 diff 的文件必须全部落在当前阶段任务的文件清单内（§1.2 范围边界 + 阶段任务）。范围外文件出现在 diff = 提交被拒。
5. **禁止携带**：格式化无关代码、重构未点名符号、修改注释语言风格。

---

## 6. 回归门禁（本地提交前与 CI 命令）

```powershell
# 本地快速门禁（每提交前）
go build ./cmd/aicli/...
go vet ./cmd/aicli/ui/... ./cmd/aicli/commands/...
go test ./cmd/aicli/ui/... -count=1          # 阶段 0 后必须全绿（或按阶段出口标注的例外）
go test ./cmd/aicli/commands/ -run 'TestFixedBottomSurface|TestBottomReserve|TestChatInteraction|TestChatRuntimeEvents' -count=1

# 语义断言专项
go test ./cmd/aicli/ui/ -run TestSemanticLineLedger -count=1 -v

# 双跑 parity（E5，Phase 2 后每次涉及 presenter 的提交）
go test ./cmd/aicli/commands/ -run TestChatRuntimeEvents -count=1

# 基准对照（Phase 4 前后）
go test ./cmd/aicli/ui/ -run '^$' -bench 'ActiveStream|Compose' -benchmem
```

CI：阶段 0 提交时同步把 `go test ./cmd/aicli/ui/... -count=1` 加入 `release-aicli.yml` validate job（E4，hard gate）。软门禁 `verify-release.ps1` 保持 `go test ./... -count=1` 不变。

## 7. 禁止清单（Do NOT）

| # | 禁止 | 原因（对应根因） |
|---|---|---|
| DN-1 | 新增任何 `*Frontier*` / `*Headroom*` / `*Debt*` / `*PrefixLen*` 状态字段 | IR-2：账本 = 重复渲染的机制来源 |
| DN-2 | 新增 `zz_` 前缀测试文件，或延长既有 `zz_` 文件寿命 | 诊断探测是病症不是治疗；Phase 5 删除 |
| DN-3 | 拷贝 `historyWindow` 数据到新结构（迁移 = 替换，不是并行拷贝） | 五副本问题的产生方式 |
| DN-4 | 修改 `chat_runtime_events.go` 中非 flag 切换点的业务逻辑 | 事件桥是独立子系统，超出本契约范围 |
| DN-5 | 删除/绕过 `renderengine.WithTerminalWriteLock` | 单一 writer 是全局门禁，动它 = 并发回归 |
| DN-6 | 用字节计数断言"无重复"作为新测试的通过条件 | §4.2 语义已裁定：字节计数只做辅助诊断 |
| DN-7 | 在 Phase 6 之前合并/删除 `stableCommitQueue` 之外的调度器 | 四时钟收敛是 L4 专项，提前动 = 无法归因 |
| DN-8 | 以"性能优化"为名跳过阶段顺序（如提前删 headroom） | 每阶段依赖前一阶段出口，跳步 = half-mode 回归 |
| DN-9 | 修改 `AICLI_SCENE_PRESENTER` 默认值 | flag 切换是 Phase 6 入口决策，需发布周期观察 |

## 8. 风险红线（触发即停下，回滚到上一阶段提交）

| 红线 | 触发条件 | 动作 |
|---|---|---|
| RZ-1 | 两个专项测试在任一阶段出口未达到该阶段通过标准 | 不回滚代码，先查语义断言是否正确（IR-7），确认断言正确则回滚实现 |
| RZ-2 | `vt.Screen` 回放出现"语义行重复计数 > 1"而实现自称已收敛 | 立即停止本阶段，回滚到上一绿提交 |
| RZ-3 | 打开 `AICLI_SCENE_PRESENTER` 后 parity 断言偏差（`chat_runtime_events_test.go` 失败） | flag 默认回关，修复后重开 |
| RZ-4 | 任一提交 diff 包含范围外文件（§1.2） | 该提交作废，拆分重提 |
| RZ-5 | 阶段 1 后 grep 仍发现 `commitExcessHistoryToScrollbackLocked` 调用点 | 视为 IR-5 违反，禁止进入阶段 2 |

## 9. 完成定义（DoD，整个迁移的验收）

1. `go test ./... -count=1` 全绿（含 ui 包 hard gate）。
2. 两个专项测试以语义断言通过；`/debug` 显示 `committedBoundary` 单调、paint trace 只含 mutable tail。
3. grep 验证：无 `commitExcessHistoryToScrollbackLocked` 调用点；无 `streamEnqueuedPrefixLen`/`streamRenderedPrefixLen`；无 `zz_` 文件；`historyWindow`/`historyWindowHeadroom`/`historyWindowMaxLines` 符号不存在。
4. `AICLI_SCENE_PRESENTER` 开启时，渲染源为 Scene 快照（/debug 可见 revision 单调），`chat_runtime_events_test.go` 全绿。
5. 人工验收（scripts/validate-multi-agent-real-terminal.ps1 模式）：Windows Terminal 下 band 出现/消失、400+ 行连续滚动、shrink/grow、resize/reflow 无重复、无顶部空白、光标位置正确。
6. 文档同步完成（E1-E5 勘误入库，single-scroll-channel 方案标记取代）。

## 10. 执行顺序备忘（给实施者/后续 agent）

```
owned Phase 0（语义先行，单独提交）→ C0（双跑门禁化）
→ Phase 1（平移替代文本提交）→ Phase 2（单调提交器）→ Phase 3（删双保留，historyCell 推进）→ Phase 4（组帧收缩）
→ C1（完整块强权威，mismatch 即错误）
→ Phase 5（清理）
→ C2 + Phase 6（mutable tail Scene 化 + L3 单真相源 + L4 单时钟单事件流，合并执行）
→ C3（flag 默认开启，观察 ≥ 1 发布周期）→ C4（删 flag 删旧路径）
```

> C0-C4 定义见 `aicli-tui-scene-presenter-convergence-design.md`。任何阶段不得跳序；任何提交不得违反 §2 铁律与 §7 禁止清单。
