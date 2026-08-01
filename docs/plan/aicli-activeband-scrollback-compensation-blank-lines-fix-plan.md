# aicli ActiveBand 补偿空行污染历史消息流修复方案

状态: **生产路径已切 owned viewport（P5.2b/P5.3-S5）；补偿空行根因已消除，遗留字段仅服务 legacy 回退；跨模块长期收敛已转入统一架构计划**

优先级: **P1（核心交互体验与历史显示正确性）**

创建日期: **2026-07-30**

更新时间: **2026-07-31**

关联方案:

- `docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`（长期 single-owner、Scene、gap/handoff 与 legacy 删除基线）
- `docs/plan/aicli-tui-p5-owned-viewport-design.md`（当前 owned viewport/P5.6 实施真相）
- `docs/plan/aicli-tui-render-data-plane-codex-migration-plan.md`
- `docs/plan/aicli-ui-rendering-implementation-review.md`

关联实现:

- `backend/cmd/aicli/ui/fixed_bottom_surface.go`
- `backend/cmd/aicli/ui/active_stream.go`
- `backend/cmd/aicli/commands/chat_interaction.go`
- `backend/cmd/aicli/ui/viewport/`

> 本文是 ActiveBand/底部预留区域补偿空行问题的专项历史与验收文档，不替代 P5 owned viewport 总体设计。§1–§5 保留问题发现和方案演进；P5 文档记录当前生产实现与 P5.6 行为；统一架构文档承接 raw/direct output、全局 boundary policy、Scene/Presenter、fullscreen lease 和 legacy renderer 删除。本文早期进度日志是当时快照，不能覆盖顶部状态和 2026-07-31 的状态对账。

---

## 1. 问题摘要

在交互式 `aicli chat` 中，当 ActiveBand、prompt、popup、动态状态等底部预留区域发生增长或
收缩时，`FixedBottomSurface` 会通过终端滚动指令补偿布局。输出区域已被历史消息填满时，增长
补偿会把屏幕顶部的历史行向上滚出当前可见区域；随后收缩补偿使用 `CSI Ps T` 将输出区域向下
滚动，但该指令无法恢复已经滚走的历史，只能在区域顶部插入空行。

用户可见结果包括：

- 屏幕上的历史消息之间或历史区域顶部凭空出现多行空白；
- 较旧的可见历史被挤出，原位置被补偿空行占据；
- 长回复结束、工具状态结束、ActiveBand 突然收缩时空行最明显；
- 多次流式提交、popup/status 切换或终端 resize 后，现象可能重复出现；
- 会话数据本身通常没有这些空消息，问题主要发生在终端物理屏幕/scrollback 渲染层。

该问题不是 Markdown 段落间距或消息数据重复写入的普通空行问题，而是立即模式终端渲染下的
**历史所有权与不可逆滚动问题**。

---

## 2. 已确认的复现与证据

### 2.1 最小复现

仓库已有一个明确标记为 known、still-open defect 的测试：

```text
backend/cmd/aicli/ui/fixed_bottom_surface_compensation_top_bug_test.go
TestBottomReserveShrinkCompensationDrawsBlanksAtTop
```

复现场景：

1. 终端大小为 20×6；
2. 输出区域填满 `L1..L5`；
3. 底部预留从 1 行增长到 3 行；
4. 底部预留再从 3 行收缩到 1 行；
5. 收缩补偿后，屏幕顶部两行为空，`L1/L2` 已不可恢复。

2026-07-30 实际运行输出：

```text
01|
02|
03|L3
04|L4
05|L5
06|
```

注意：该测试当前断言的是“错误行为仍存在”，所以测试显示 `PASS` 不代表缺陷已修复。生产修复
完成时必须反转断言：顶部不得出现补偿空行，历史必须保持锚定。

### 2.2 旧路径中的关键代码

底部预留增长：

```text
backend/cmd/aicli/ui/fixed_bottom_surface.go
appendOutputScrollUpForBottomReserveGrowthSequence
```

其行为是限定输出滚动区域、移动到区域底部并写入若干换行，使历史整体向上滚动。

底部预留收缩：

```text
backend/cmd/aicli/ui/fixed_bottom_surface.go
appendOutputScrollDownForBottomReserveShrinkSequence
terminalScrollDownSequence
```

其行为是发送 `CSI Ps T`，在滚动区域顶部插入空行并把当前内容下移。它不能从终端原生
scrollback 中取回增长阶段已经滚走的历史。

补偿状态机：

```text
scrollCompensatedRows
pendingScrollDownRows
outputCursorOnBlankRow
outputScrollDebtRows
```

核心布局逻辑位于 `appendApplyLayoutSequenceWithSizeLocked`，写入前的债务清算位于
`writeOutput`、`flushPendingOutputScrollDownLocked` 和 `flushOutputScrollDebtLocked`。

### 2.3 正确模型已有影子证明

`backend/cmd/aicli/ui/viewport/` 已有 owned viewport 的双缓冲和合成基础，其中：

```text
TestCompose_GrowShrinkKeepsHistoryAnchored
```

使用与缺陷相同的 reserve `1 → 3 → 1` 场景，能够在收缩后恢复 `L1..L5`，不会在顶部产生
补偿空行。该证明目前只存在于 `viewport` 影子/测试路径，尚未接管生产 `FixedBottomSurface`。

`FixedBottomSurface.historyWindow` 当前也只捕获有界历史；代码明确说明它尚未用于生产重渲染。

---

## 3. 根因分析

### 3.1 根因：把不可逆历史滚动当作可逆布局操作

当前实现隐含假设：

```text
底部区域增长 N 行：输出向上滚 N 行
底部区域收缩 N 行：输出向下滚 N 行
二者可以互相抵消
```

实际终端语义是：

```text
向上滚动：顶部历史可能离开应用可控制的滚动区域，进入原生 scrollback
向下滚动：只能在当前区域顶部插入空白，不能恢复已滚走的历史像素
```

所以 `scrollCompensatedRows` 只记录了几何变化量，却没有保留被滚走的历史内容。应用没有足够
状态执行真正的逆操作。

### 3.2 放大因素：stable commit 与 ActiveBand 收缩不是单一事务

`chat_interaction.go` 的 stable commit 顺序为：

```text
写 stable 行到 scrollback
→ CommitStablePrefix
→ 重新绘制/收缩 ActiveBand
```

该顺序用于避免先隐藏 ActiveBand 内容产生可见空洞，但形成了短暂的双重所有权：同一批内容已写入
历史时仍保留在旧高度 ActiveBand 中。历史写入和 ActiveBand 释放是两个独立 surface 操作，后者
可能按旧 reserve 产生 shrink compensation。

这属于跨组件的逻辑事务不原子，不是普通 Go 数据竞争。

### 3.3 放大因素：finalize 保留 ActiveBand 到最终历史写入之后

assistant finalize 会先保留 ActiveBand，写入 residual/full scrollback，随后在 reset 阶段清除
ActiveBand。长回复结束时，满高度 ActiveBand 可能一次释放 6–14 行，从而触发较大的向下补偿，
使顶部空白非常明显。

### 3.4 次要风险

- resize 路径中，surface geometry、soft tail rewrite、stable queue rebuild 和 ActiveStream resize
  分阶段发生，可能短暂混用新旧几何；
- 绕过 `FixedBottomSurface.WriteOutput` 的直接 stdout 路径可能遗留 `pendingScrollDownRows` 或
  `outputScrollDebtRows`，把布局债务附着到下一次历史写入；
- ActiveBand、popup、prompt notice、dynamic status 共享 bottom reserve，任一高度振荡都可能触发
  同类补偿，不应只针对 assistant 文本做局部修补。

---

## 4. 修复目标与非目标

### 4.1 必须达成的目标

1. ActiveBand/bottom reserve 发生任意 `grow → shrink` 后，屏幕顶部不得出现补偿制造的空行；
2. 增长时暂时不可见的最近历史，在收缩后能够从应用拥有的历史模型中重新显示；
3. 历史与 ActiveBand/prompt 之间不得产生额外空洞；
4. live stream、finalize 后屏幕和一次性 replay 的消息行与间距保持一致；
5. stable 内容从可变 viewport 迁移为历史时只发生一次，不双画、不漏画、不重复提交；
6. prompt、popup、tool progress、status、resize 与 assistant stream 共用同一布局真相；
7. 非交互、JSON、管道输出行为不改变；
8. 对不支持所需终端能力的环境保留明确、可测试的降级路径。

### 4.2 非目标

- 不通过修改 Markdown 渲染器掩盖终端补偿空行；
- 不把空行简单裁掉，因为真实 Markdown 块间距和消息间距必须保留；
- 不把“关闭 ActiveBand”作为最终修复；
- 不在本专项中改造成全屏 alt-screen TUI；
- 不承诺重排已经完全交给终端原生 scrollback 的无限历史，只保证应用拥有窗口内的正确性。

---

## 5. 方案决策

### 5.1 采用 owned viewport，停止依赖对称滚动补偿

生产修复必须与 `aicli-tui-p5-owned-viewport-design.md` 的 P5.2b/P5.3 对齐：

- 应用拥有最近历史和底部区域的逻辑行；
- 将最近历史、ActiveBand、prompt、popup、status 合成为完整 viewport back buffer；
- 与 front buffer 做 diff 后统一刷新终端；
- bottom reserve 增长时只改变可见历史窗口，较旧行仍保留在 history model；
- bottom reserve 收缩时重新合成并恢复这些历史行，而不是发送 `CSI Ps T` 制造空白；
- 最终历史仅通过一个光标中性的 `insertHistoryLines(rows)` 原语交给原生 scrollback。

### 5.2 必须同时接入历史窗口与底部合成

只把 ActiveBand 改成 buffer、但历史仍完全依赖原生 scrollback，不能解决“收缩时拿什么填回顶部”的
问题。因此以下两项必须作为同一生产切换交付：

1. bottom pane 统一走 owned composition；
2. 最近历史窗口成为生产渲染输入，而不只是旁路记录。

### 5.3 不接受的局部修补

以下方案已知不能作为最终修复：

- **直接删除收缩 `CSI T`**：会在历史和 prompt/ActiveBand 之间留下大空洞；
- **用清行替代 `CSI T`**：无法恢复历史，并破坏布局中性和 live/replay parity；
- **只修改补偿计数**：行数即使计算准确，也无法取回已经滚走的内容；
- **只固定 ActiveBand 高度**：可以减少流中振荡，但首次增长和最终收缩仍有不可逆边界；
- **只调整 stable commit 顺序**：先清 band 会重新引入可见空洞，先写历史仍保留旧 reserve；需要
  原子的 viewport/history 交接，而不是交换两个非原子调用的顺序；
- **从最终字符串删除连续空行**：补偿空行不在消息数据内，同时会误删合法 Markdown 间距。

---

## 6. 分阶段实施计划

### Phase 0：冻结缺陷契约与观测基线

状态: **done（核心契约已冻结；其余覆盖并入 P5 回归）**

任务：

- [x] 确认最小 grow/shrink 复现；
- [x] 确认旧测试当前刻画的是 buggy output；
- [x] 确认 `viewport.Compose` 能证明正确行为；
- [x] 新增面向真实 chat surface 的“满屏历史 + ActiveBand grow/shrink”正向回归
      （`TestBottomReserveShrinkRestoresHistoryWithoutBlankingTop`）；
- [ ] 覆盖短终端、24/40/48 行终端和 ActiveBand 最大高度（后续增强）；
- [ ] 保存修复前 ANSI trace、VT screen dump 和历史标记序列作为基线；
- [ ] 为 repeated grow/shrink 增加测试，确认空行不会按周期累积。

退出条件：

- 测试能同时观察屏幕顶部历史保留、底部邻接和 prompt 位置；
- 旧路径在测试中稳定复现缺陷；
- 测试不会把合法 Markdown 空行误判为补偿空行。

### Phase 1：定义生产 owned viewport 状态与边界

状态: **done（对齐 P5.2b/P5.3 设计落地）**

任务：

- [ ] 明确 `historyWindow` 的行模型、样式模型、部分行续写和宽字符语义；
- [ ] 定义 viewport frame 输入：history、ActiveBand、popup、notice、prompt、status；
- [ ] 定义可变行、soft committed 行、不可逆 history 行的所有权状态；
- [ ] 定义唯一历史交接 API `insertHistoryLines(rows)` 的输入、光标和滚动语义；
- [ ] 明确 resize 时哪些行可重新排版，哪些行已不可逆；
- [ ] 明确 Windows ConPTY、常规 ANSI terminal 和无 ScrollRegion 能力环境的降级策略；
- [ ] 评审锁顺序：coordinator lock、surface lock、terminal write lock 不得形成新死锁。

退出条件：

- 同一逻辑行在任意时刻只属于一个生产渲染层；
- history 交接前后 viewport frame 和终端光标状态有可验证的不变量；
- 不再需要以 `grow N` 与 `shrink N` 对称来推断历史位置。

### Phase 2：接通 history window 与 viewport composition

状态: **done（生产默认 owned；legacy 仅能力回退）**

任务：

- [x] 将已有 `viewport.Backend`/`Compose` 接入 `FixedBottomSurface` 生产渲染；
- [x] 将 `historyWindow` 从记录用途升级为合成器的历史输入；
- [x] bottom pane 所有元素统一生成结构化行并进入同一 frame；
- [x] 确保 style、CJK 宽度、截断、NO_COLOR 和超链接安全语义不回退；
- [x] 同一终端写锁内提交 frame diff，避免旧立即模式逐段写产生中间状态；
- [ ] 为能力不足终端实现显式降级，并记录降级模式下的已知限制（P5.7 收尾）。

退出条件：

- `SetActiveBandStyled`、prompt/status/popup 更新不再通过滚动历史来腾行；
- reserve grow/shrink 通过重新 Compose 恢复历史；
- 生产路径能够通过 grow/shrink 历史锚定测试。

### Phase 3：原子化 stable/final 历史交接

状态: **partial / transferred（owned handoff 与 P5.6 已落地；stable/final 的统一 Scene transaction 继续由统一架构计划 P4–P7 收敛）**

任务：

- [ ] 把 stable prefix 的“WriteOutput 后 CommitStablePrefix”改为单一所有权迁移事务；
- [ ] 把 finalize residual 写入和 ActiveBand 清除改为单一 frame/history 交接；
- [ ] 保留 source offset、transcript block 和 soft-tail 的一致性；
- [ ] 验证 timer drain、catch-up、cancel、interrupt、tool→assistant 切换不重放；
- [ ] 确保每个 history cell 只调用一次 `insertHistoryLines`；
- [ ] 删除或隔离不再需要的补偿分支，禁止新路径回落到旧 `WriteOutput + shrink SD` 组合。

退出条件：

- stable/final 内容不在 ActiveBand 和 history 中同时物理绘制；
- finalize 无闪洞、无顶部空白、无消息重复；
- transcript/source offset 与屏幕历史逐行一致。

### Phase 4：resize、直接输出和降级路径收口

状态: **partial / transferred（P5.5 resize/reflow 已落地；direct output、fullscreen 与降级路径继续由统一架构计划 P0–P3/P7 收敛）**

任务：

- [x] owned 路径 geometry / soft-tail / band 更新走同一 Compose frame；
- [ ] 完成 `beginDirectInteractiveOutput + fmt.Print*` 全量审计；目前仅部分 command/runtime writer 已 surface-aware，剩余项转入统一架构计划 P0–P2；
- [x] history/resume 在首个消息写入前不再背负旧布局债务（owned SettleOutputDebt 纯 recompose）；
- [ ] 校验 popup、slash command panel、login/model/skill 选择器退出后的历史锚定；
- [ ] 为终端 resize 往返、窄宽切换、Windows ConPTY 增加真实终端或录制验证。

退出条件：

- resize 不混用新旧 bottom reserve 几何；
- 受控交互路径没有遗留 `pendingScrollDownRows` 的调用点；
- 降级路径行为和限制有测试、日志或诊断信息。

### Phase 5：删除旧补偿状态机并完成生产验收

状态: **partial（owned 路径已 no-op；字段仍保留给 legacy 回退，P5.7 再物理删除）**

任务：

- [x] owned 路径不再读写/依赖 `scrollCompensatedRows` 做布局；
- [x] owned 路径不再积累/flush `pendingScrollDownRows`；
- [ ] 删除新路径不再需要的 `outputCursorOnBlankRow` / `outputScrollDebtRows` 推断；
- [ ] 反转 `TestBottomReserveShrinkCompensationDrawsBlanksAtTop`，改为历史不得丢失；
- [ ] 更新 P5 总体设计和相关 implementation review；
- [ ] 完成 Windows Terminal/ConPTY 与至少一种非 Windows ANSI terminal 的人工验收；
- [ ] 观察一版后决定是否移除旧路径/killswitch。

退出条件：

- 本文第 8 节完成标准全部满足；
- 不再有生产路径依赖 grow/shrink 对称补偿维护 ActiveBand 布局；
- 旧缺陷测试从“刻画错误”正式变成“阻止回归”。

---

## 7. 测试矩阵

| 场景 | 必须断言 | 测试层级 | 状态 |
| --- | --- | --- | --- |
| reserve `1→3→1`，历史填满屏幕 | 顶部历史恢复，无补偿空行 | `ui/vt` + surface | 已有旧缺陷刻画；生产回归待补 |
| ActiveBand `0→6→0` | 历史与 prompt 位置匹配无 band 基线 | commands screen VT | 部分已有，需增加顶部检查 |
| ActiveBand `0→14→0` | 不丢顶部历史、不出现 14 行空白 | commands screen VT | 待补 |
| repeated grow/shrink | 空行不随周期累计 | commands screen VT | 待补 |
| stable prefix 多次提交 | 无双画、无顶部空白、band 尾连续 | coordinator + surface | 部分已有，需增强 |
| finalize residual | 历史只提交一次，清 band 无闪洞 | coordinator + surface | 部分已有，需增强 |
| Markdown 多块/合法空行 | 与 one-shot replay 行一致 | live-vs-replay | 已有，需与顶部保留联合 |
| tool progress→completed | 历史锚定，tool cell 不重复 | screen VT | 待补 |
| popup/status/prompt notice 增减 | 任意 reserve 变化不滚走 owned 历史 | surface | 待补 |
| resize 40→80→40 | owned 窗口内历史可重排且不丢行 | viewport + real terminal | 待补 |
| CJK/emoji/宽字符 | 行宽、续列、清除均正确 | viewport/vt | 基础已有，生产组合待补 |
| history/resume | 首行不吸收旧布局债务 | commands | 已有部分 invariant |
| direct command output | 无 pending debt 泄漏到下一消息 | commands | 部分已有，调用点审计待做 |
| NO_COLOR/16/256/TrueColor | 样式变化不影响布局与行所有权 | ui integration | 待补 |
| 非交互/JSON/pipe | 字节输出无行为变化 | commands | 待回归 |
| Windows ConPTY | 无顶部空白、无错位、无光标残留 | 真实终端 | 待验证 |

建议所有关键 screen 测试同时检查三类不变量，而不是只检查底部 gap：

1. **顶部保留**：预置的 oldest visible marker 仍存在或能在收缩后恢复；
2. **底部邻接**：最后历史行到 ActiveBand/prompt 的间距等于 replay 基线；
3. **唯一所有权**：每个 marker 在屏幕/历史交接中出现预期次数，不重复、不丢失。

---

## 8. 完成标准（Definition of Done）

只有同时满足以下条件，本文状态才能改为 `completed`：

- [ ] 生产 `aicli chat` 默认路径使用 owned viewport 合成最近历史和底部区域；
- [ ] 最小复现中的 `L1..L5` 在 grow/shrink 后全部恢复；
- [ ] known bug 测试已反转，不再断言错误行为；
- [ ] ActiveBand 最大高度、多轮 stable commit、finalize 和 repeated cycles 均无顶部补偿空行；
- [ ] 现有无底部空洞、layout neutral、live-vs-replay 测试全部通过；
- [ ] history/resume/direct output 不携带旧布局债务；
- [ ] resize 和至少两类终端环境完成验证；
- [ ] `go test ./cmd/aicli/ui/...` 通过；
- [ ] `go test ./cmd/aicli/commands/...` 通过；
- [ ] 对相关 package 完成 `go vet`/构建验证；
- [ ] P5 总体计划和实施审查文档状态同步；
- [ ] 旧补偿路径已删除，或有明确的降级边界、killswitch 和后续删除期限。

特别规则：

> `TestBottomReserveShrinkCompensationDrawsBlanksAtTop` 在旧断言下通过，不能作为修复完成证据。
> 必须以反转后的历史保留断言和生产 surface 集成测试为准。

---

## 9. 风险、回退与诊断

### 9.1 主要风险

- 生产 scrollback 所有权切换范围大，可能影响 prompt 光标、行编辑器和异步输出；
- history window 与 soft tail/transcript source 若存在两个真相，可能出现重复历史或 resize 重放；
- CJK 宽字符、样式 span、部分行续写可能导致 front/back diff 不一致；
- 原生 scrollback 和 owned viewport 的交接边界处理不当可能吞掉或重复一行；
- 锁粒度扩大后可能造成输入延迟、死锁或 30 FPS 活动帧抖动；
- 各终端对 scroll region、cursor save/restore、同步更新支持不同。

### 9.2 回退策略

- 生产切换初期保留环境 killswitch，仅用于紧急回退；
- killswitch 关闭 owned viewport 时必须输出可诊断模式信息，并明确会回到已知旧缺陷路径；
- 不用长期双写两个生产 renderer；允许 shadow compare，但终端只能由一个 renderer 提交；
- 每个阶段保持小切片，先增加状态/合成，再切换生产所有权，最后删除旧字段；
- 若 history 交接出现重复/漏行，优先回退该切片，不用恢复“清空代替 SD”的失败探针。

### 9.3 建议诊断字段

开发或 debug 模式建议记录：

```text
terminal width/height
owned viewport enabled/degraded
history window line count
visible history range
bottom pane row count
active band row count
stable source emitted/enqueued/committed offsets
history insert sequence/id
frame generation
resize generation
```

日志不得包含敏感对话全文；测试可使用 marker/source offset 验证所有权。

---

## 10. 进度看板

| ID | 工作项 | 状态 | 依赖 | 完成证据/备注 |
| --- | --- | --- | --- | --- |
| AB-00 | 根因确认与专项文档 | completed | 无 | 2026-07-30 完成代码、测试和 P5 文档交叉分析 |
| AB-01 | 最小缺陷复现 | completed | 无 | 原 buggy test 已完成历史使命并在生产切换后反转 |
| AB-02 | owned Compose 修复证明 | completed | 无 | `TestCompose_GrowShrinkKeepsHistoryAnchored` 通过并已接生产 |
| AB-03 | 真实 surface 满屏顶部保留回归 | completed | AB-01 | `TestBottomReserveShrinkRestoresHistoryWithoutBlankingTop` 锁定生产行为 |
| AB-04 | owned viewport 生产状态设计 | completed | AB-02 | 已由 P5.2b/P5.3 设计和实现承接 |
| AB-05 | history window 接入生产 Compose | completed | AB-04 | owned history 与 Compose 已进入默认生产路径 |
| AB-06 | bottom pane 全量结构化合成 | completed | AB-04 | ActiveBand/prompt/popup/status 由 owned frame 合成 |
| AB-07 | stable prefix 原子历史交接 | transferred | AB-05, AB-06 | 现有 P5 行为保留；统一 Scene transaction 转入长期计划 P4–P7 |
| AB-08 | finalize 原子历史交接 | transferred | AB-05, AB-06 | mutable/final replace-commit 转入长期计划 P4–P7 |
| AB-09 | resize/soft-tail 收口 | completed-p5 | AB-05 | P5.5 已完成；semantic cell/source truth 的进一步收敛见长期计划 P7 |
| AB-10 | direct output 路径审计 | transferred | AB-06 | 转入长期计划 P0–P2，当前不能视为已完成 |
| AB-11 | 旧补偿/legacy renderer 删除 | transferred | AB-07..AB-10 | 转入长期计划 P8，删除前必须有单一 owner 替代路径 |
| AB-12 | 自动化全量验证 | transferred | AB-11 | P5 回归已存在；全局 invariant/race/PTY 验收转入长期计划 P9 |
| AB-13 | 真实终端验收 | transferred | AB-12 | Windows ConPTY + 非 Windows PTY 转入长期计划 P9 |
| AB-14 | 旧缺陷反转与文档收尾 | partial | AB-13 | 缺陷测试已反转；全局文档/legacy 收尾尚未完成 |

状态枚举：

- `not started`：未开始；
- `in progress`：正在实施；
- `blocked`：被依赖、设计或环境阻塞；
- `completed-shadow`：影子/测试实现完成，但未接生产；
- `completed-p5`：P5 专项范围完成，但统一 Scene/single-owner 仍有后续；
- `transferred`：剩余工作已映射到统一架构计划，不再在本文重复维护；
- `partial`：已有部分证据，但尚未满足完整退出条件；
- `completed`：完成且具备验收证据。

---

## 11. 进度更新规则

每次开始或完成一个切片时，必须更新以下内容：

1. 文档顶部 `状态` 与 `更新时间`；
2. 第 6 节对应 Phase 状态和任务勾选；
3. 第 10 节进度看板的状态、依赖和证据；
4. 第 12 节追加一条按日期排序的变更记录；
5. 如调整架构决策，同步第 5 节，并注明替代了哪项旧决策；
6. 如发现新回归，同步第 7 节测试矩阵和第 9 节风险；
7. 证据必须包含测试名、命令结果、screen dump/golden 或人工终端记录之一；
8. 不允许仅以“代码已合并”标记完成，必须满足对应 Phase 退出条件。

---

## 12. 进度日志

### 2026-07-30：创建专项跟踪方案

已完成：

- 定位立即模式 grow/shrink 补偿的不可逆历史问题；
- 确认 stable commit 和 finalize 顺序会放大 reserve 收缩；
- 运行 known defect 测试并得到顶部两行空白、`L1/L2` 丢失的结果；
- 运行现有 layout-neutral/live-vs-replay 相关测试，确认它们通过但没有消除满屏顶部缺陷；
- 运行 `viewport/TestCompose_GrowShrinkKeepsHistoryAnchored`，确认 owned composition 能恢复历史；
- 确认 `historyWindow` 与 viewport backend 尚未接管生产 surface；
- 建立 AB-00 至 AB-14 工作项和完成门禁。

当日判断（历史快照，已被 2026-07-31 状态对账取代）：

- 根因置信度：**高，已有代码注释、最小 VT 复现和 P5 设计交叉证明**；
- 当时生产修复状态：**未开始**；
- 当时下一步：**AB-03，增加真实 chat/surface 满屏历史顶部保留回归，然后进入 AB-04 生产状态设计评审**。

### 2026-07-30：TTY 仿真测试审计

已确认当前仓库存在两层 TTY/终端字节流验证，但还没有发现直接启动真实 OS PTY/Windows ConPTY 的集成测试：

- `backend/cmd/aicli/ui/vt/screen.go`：共享 VT 屏幕模型，逐字节回放 surface 实际输出，支持 `DECSTBM`、`SD (CSI T)`、LF/IND/RI、CUP、光标保存恢复、擦除、SGR 和宽字符；
- `backend/cmd/aicli/commands/chat_interaction_screen_test.go`：用 `screenVT` 包装 `vt.Screen`，把固定底部 surface 捕获的 ANSI 输出重新喂回仿真屏幕，再检查用户可见行；
- `fixed_bottom_surface_compensation_top_bug_test.go`：直接用 VT 模型复现 grow `1→3`、shrink `3→1`，确认顶部补偿空行；
- `viewport/compose_test.go`：用 VT 模型验证 owned viewport 在相同场景能恢复历史；
- `chat_surface_reserve_scroll_invariant_test.go`、`chat_interaction_midstream_blank_test.go`、`chat_interaction_live_vs_replay_blank_test.go`：覆盖真实 surface 输出字节流的回放、ActiveBand 邻接和 live/replay 布局约束，但部分测试主要关注底部 gap，尚未全部覆盖满屏历史顶部保留。

已运行并通过：

```text
go test ./cmd/aicli/ui/vt ./cmd/aicli/ui/viewport -count=1
go test ./cmd/aicli/ui -run "TestBottomReserveShrinkCompensationDrawsBlanksAtTop|TestFixedBottomSurface_ActiveBandGrowthAndReleaseScrollSymmetrically|TestFixedBottomSurface_ReleasedActiveBandScrollsOutputBackDown" -count=1
go test ./cmd/aicli/commands -run "TestFixedBottomSurface_ActiveBandIsLayoutNeutral|TestFixedBottomSurface_MidStreamBandGrowthKeepsScrollbackAdjacent|TestFixedBottomSurface_StableCommitThenBandShrinkKeepsAdjacency|TestChatInteractionCoordinator_LiveStreamScreenLayoutParityWithReplay|TestFixedBottomSurface_EOSFusionLeavesNoBlankGap" -count=1
```

这些通过结果不能证明缺陷消失：其中 known defect 测试仍然刻画当前错误行为，其他测试主要证明底部布局、邻接和 live/replay 约束没有回归。

当前结论：

- **TTY 字节流仿真确认问题：是**；
- **真实 OS PTY/ConPTY 确认问题：目前没有现成测试证据**；
- 下一项测试工作应是 AB-03：在现有 `screenVT` 基础上增加真实 chat/surface 的“满屏历史顶部保留”失败回归，再决定是否补充 Windows ConPTY/真实终端录制验收。

### 2026-07-31：与 P5.6 和统一长期架构对账

当前状态：

- production 已默认使用 owned viewport；原 `CSI T` grow/shrink 顶部空行缺陷已由 `TestBottomReserveShrinkRestoresHistoryWithoutBlankingTop` 反转并锁定；
- P5.6 已在 `835386e` 中确认 tool cell 内稠密、独立 final tool/event cell 间单空行，Running 仅在 ActiveBand live redraw；
- 本文原 AB-04–AB-06 已由 P5 实现完成，AB-09 在 P5 专项范围完成；
- AB-07/AB-08 的统一 stable/final transaction、AB-10 direct output 审计、AB-11 legacy 删除及 AB-12/AB-13 全局验收，已转入 `aicli-tui-unified-render-architecture-refactor-plan.md`；
- 2026-07-30 日志中“生产修复未开始”等结论只代表当时快照，不再代表当前生产状态。
