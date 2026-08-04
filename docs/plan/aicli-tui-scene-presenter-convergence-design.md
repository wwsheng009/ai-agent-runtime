# aicli Scene presenter 双跑收敛设计：AICLI_SCENE_PRESENTER → 单一 Scene 渲染

> 原上位：`docs/plan/aicli-tui-owned-render-simplification-implementation-guide.md`
> 关联：`aicli-tui-unified-render-architecture-refactor-plan.md`（母计划 P3 切换）、`aicli-tui-owned-render-simplification-plan.md`（L2 简化）、`aicli-tui-transcript-overlay-renderer-mode-plan.md`（primary/alternate renderer 与 transcript pager 实施）
> 状态：**superseded（C0-C4 有效目标已合并进 unified plan 与 owned implementation guide Phase 1-6；本文只保留历史设计记录，不得独立执行）**
> 日期：2026-08-03

> **维护说明**：2026-08-03 评审确认，先完成旧 L2 `committedBoundary/ScrollExistingRows` 再做 Scene C2 会制造第二套中间状态，且“发布周期后再建立单事件流”的顺序错误。新的规范顺序是先建立 UI actor/AppState，再引入 Presenter effect/ack 和 tokenized handoff，随后收敛 streaming，最后删除 flag/旧 renderer。执行以新版 implementation guide 为准。

---

## 0. 一句话结论

`AICLI_SCENE_PRESENTER` 不是"终局开关"，而是**收敛过程本身**：它当前只把"完整块"的可见行以 Scene 投影为权威（且是"内容一致才采用"的保守模式），mutable tail（流式 band）仍是旧路径。收敛 = 按 C0→C4 五步把 Scene 权威域从"完整块"逐步扩大为"全部可见内容 + scrollback 提交源"，最后 **flag 本身无意义并删除**。整个收敛期间任一时刻可关回旧路径（已有测试证明输出等价）。

---

## 1. 现状：双跑机制的精确语义（代码实测，2026-08-03）

### 1.1 两个状态

| 状态 | 行为 | 代码 |
|---|---|---|
| flag 关（默认） | 可见输出**完全保持旧路径**（coordinator `writeRowsLocked` 等），Scene 只做**只读旁路对照**（`checkTextParity` 探针，不改变任何输出行为） | `chat_runtime_events.go:196-206`（`scenePresenterModeFromEnv`：未设置/空/未知值一律关闭，回退安全）、`:1063-1113` |
| flag=1 | **完整块**可见行由 Scene 投影驱动：`writeRowsLocked` 的每个完整块行 = Scene 对应 cell 组的 `LayoutTranscript` 行（含跨块 gap 空行）+ 样式 chrome（user `"> "`、system ErrorIcon） | `sceneBlockSource`（`:1167-1245`） |

### 1.2 sceneBlockSource 的消费规则（理解收敛的关键）

- **整 cell 提交语义**：入参块行（剥 chrome 与前导 gap 空行）必须与**某个未消费分组**的行完全一致，才消费该分组并用 Scene 行输出；
- **内容对应校验失败 → 回退旧行、不消费、不推进**（流式残差/部分提交/快照滞后时）；闭包返回空，调用方走旧路径并靠探针报 mismatch；
- **nextGroup 单调推进**：保证块与 Scene cell 一一对应，不重复消费。

**推论**：flag=1 的当前实现本质是"**确认等价后才采用 Scene 行**"——它不可能在不等价时产生错误输出（最坏回退旧路径）。这是收敛可以安全推进的结构基础。

### 1.3 双跑对照探针（checkTextParity）

- coordinator 每个完整块提交后调用：旧路径实际写出行 vs Scene 快照 `RenderText` 对应片段，逐行对照；
- 统计：`textParityBlocks / textParityMatched / textParityMissed / textParityLastErr`（`:132-137`），供 `/debug` 审计段展示（`:1247-1254`）；
- user 前导 gap 由 prompt 重绘输出（不在块行内），对照时忽略；gap 行不参与 chrome。

### 1.4 既有测试面（收敛的安全网）

| 测试 | 固化内容 |
|---|---|
| `TestPresenterSwitch_SceneModeOutputMatchesLegacy` | flag 开/关输出**逐行完全一致**（可回退性证明） |
| `TestPresenterSwitch_SceneModeOutputIsSceneProjection` | 强断言可见输出就是 Scene 投影（防退化） |
| `TestPresenterSwitch_EnvParsing` | 取值解析：未设置/空/未知一律关（回退安全） |
| `TestRenderLayer_TextParity_EventStreamVsLegacyCoordinator` | 完整链路（事件流→编码器→Scene→RenderText）与旧路径逐行一致 |
| `TestRenderLayer_TextParity_ToolChainRenderText` | tool 链 gap 结构 + 内容行一致 |
| `chat_runtime_events_test.go`（223KB） | 事件桥全量行为 |

### 1.5 当前覆盖边界（缺口）

| 内容域 | Scene 权威 | 说明 |
|---|---|---|
| 完整块（user/assistant/tool/system finalized） | ✅（flag=1） | 整 cell 提交 |
| 流式残差 / 部分提交 | ❌ 回退旧路径 | Scene 无对应分组时旧行输出 |
| **mutable tail（流式 band：assistant delta、reasoning delta、tool-running 动态区）** | ❌ 完全旧路径 | 最大缺口 |
| scrollback 提交源（历史行入 native scrollback） | ❌ 旧路径 | 属 owned 方案 L2 域 |
| gap 语义 | ✅（LayoutTranscript） | 但旧路径 gap 计算仍存在 |

---

## 2. 收敛目标

从"**完整块 Scene 化 + 双跑审计**"演进到：

1. **全部可见内容**（完整块 + mutable tail）以 Scene 为唯一权威；
2. **scrollback 提交源** = `historyCell.DisplayLines(width)`（与 owned 方案合并）；
3. 旧路径渲染代码删除，**flag 删除**（`AICLI_SCENE_PRESENTER` 不再存在）。

---

## 3. 收敛阶段（C0 → C4）

> 每个阶段独立提交、可回滚；与 owned 方案阶段（`implementation-guide.md` §4 的 Phase 0-6）穿插执行，依赖排序见 §4。

### C0：双跑门禁化（旁路审计 → CI 硬门禁）

**目的**：把"探针统计"变成"回归门禁"，让双跑偏差在提交时失败而不是上线后观察。

任务：

1. 新增断言测试：固定会话序列（覆盖 user/assistant/tool/system/error/reasoning + 流式 + gap）跑完后，`textParityMatched == textParityBlocks && textParityMissed == 0`（在 `chat_runtime_events_test.go` 同包）。
2. CI 矩阵扩展：validate job 同时跑 `AICLI_SCENE_PRESENTER` 空值与 `=1` 两组（复用 `TestPresenterSwitch_*` 的 `t.Setenv` 模式；`scenePresenterModeFromEnv` 每次构造 bridge 时读取，无需进程级隔离——**实施前需确认 bridge 构造点**，见 §5 风险 R-C1）。
3. `/debug` 审计段展示 textParity 统计（确认 `textParityStats` 已接入展示，未接入则补）。

**出口**：parity 门禁全绿；flag=1 组与空值组全量测试一致通过。

### C1：完整块强权威（去掉"内容一致才采用"的保守回退）

**目的**：完整块从"确认等价才用 Scene 行"升级为"**必须**由 Scene 投影输出"，mismatch 即错误。

任务：

1. `sceneBlockSource` 找不到分组时：从"回退旧行 + 探针报 mismatch"改为"**panic 式失败**（测试态）/ 保留旧行 + 硬错误计数（生产态）"——生产态不崩，但 `textParityMissed > 0` 使 CI 失败，迫使修复而非掩盖。
2. 补齐所有 finalized cell 类型的 Scene 投影覆盖（tool chain、system error、supplement、reasoning final），逐一跑 parity 门禁。
3. 旧路径 gap 计算删除，gap 唯一来源 = `LayoutTranscript`（含 user 前导 gap 的归属统一）。

**前置**：owned 方案 Phase 1-4 完成（几何/提交器/双保留/组帧），否则旧路径自身的重复渲染 bug 会让"mismatch 即错误"天天红。

**出口**：flag=1 下完整块 100% 由 Scene 投影；`textParityMissed == 0` 连续 N 个提交保持。

### C2：mutable tail Scene 化（最大工程）

**目的**：流式 band（assistant delta、reasoning delta、tool-running）从 ActiveStream 缓冲渲染改为 Scene mutable cell 投影。

任务：

1. **流式 cell 化**：assistant/reasoning 流式 delta 进 `ChangeSet` upsert → Scene mutable cell（`scene.CellMutation` 的 `AppendCell/UpdateCell` 已支持），Layout 派生 band 行；
2. **增量投影**：每次 delta 只对受影响 cell 重新 Layout（确认 `LayoutTranscript` 是否支持 cell 级增量；不支持则先做"脏 cell 集 + 全量 Layout 但只 diff 脏区"的过渡）；
3. **band 差异化渲染**：band 行不再整段重画，`ScreenModel` diff 只发 delta；
4. 与 owned 方案 Phase 6 L3-2 合并：`commitActiveStableScrollbackLocked`（:4314）的 live band 稳定前缀提交源 = `historyCell.DisplayLines(width)`。

**出口**：flag=1 下可见内容 100% Scene 权威（含流式）；双跑探针对 mutable tail 的对照统计归零（mutable tail 不再走旧路径，探针无旧行可比——探针职责收窄为"仅审计历史完整块"或删除）。

### C3：默认开启 + 观察

任务：

1. `scenePresenterModeFromEnv` 默认值改为开（环境变量仅用于 opt-out 回退）；
2. 观察 ≥ 1 个发布周期：parity 统计、真实终端人工验收（`validate-multi-agent-real-terminal.ps1` 模式）、无回退报告。

**出口**：发布周期内 `textParityMissed == 0` 且无用户可见偏差报告。

### C4：删除旧路径与 flag

任务：

1. 旧路径完整块渲染（`writeRowsLocked` 的旧 cell source 分支、`Formatter.Format` 独立渲染调用点、gap 旧计算）删除；
2. `AICLI_SCENE_PRESENTER` flag 及 `sceneBlockSource` 的"回退旧行"分支删除（此时无旧路径可回退）；
3. `checkTextParity` 探针决定去留：保留为审计（对比对象 = 编码器输出而非旧路径）或删除；
4. 与 owned 方案 Phase 5 清理合并：`zz_` 文件、`HandoffFrontier.TrimPrefix/Clamp`、legacy reserve debt、未用符号一并清理。

**出口**：grep 确认 `AICLI_SCENE_PRESENTER`、`scenePresenterMode`、`blockSourceFn` 回退分支、`writeRowsLocked` 旧源分支均不存在；全量测试绿。

---

## 4. 与 owned-render-simplification 的合并时间线

两线共享"语义行不重复"目标：owned 线管 **L2（scrollback 三权分裂）**，Scene 线管 **L1/L3（语义源 + 完整块/流式权威）**。推荐执行顺序：

```
owned Phase 0（语义行 ID 记账工具）        ← 一切断言的前提
   ↓
C0（双跑门禁化）                            ← 在语义工具之上建 parity 门禁
   ↓
owned Phase 1-4（几何平移 → 单调提交器 → 删双保留 → 组帧收缩）
   ↓
C1（完整块强权威）                          ← 前置：owned Phase 1-4 已消除旧路径重复渲染
   ↓
owned Phase 5（清理）
   ↓
C2 + owned Phase 6（mutable tail Scene 化 + L3 单真相源 + L4 单时钟单事件流）  ← 合并执行
   ↓
C3（默认开）→ C4（删 flag 删旧路径）
```

**为什么 C1 必须排在 owned Phase 1-4 之后**：C1 的"mismatch 即错误"会把旧路径的重复渲染/空白 bug 全部暴露成红。先由 owned 线修掉（几何平移、单调提交器、删双保留），C1 才能稳定通过。反过来，owned 线的 Phase 0（语义断言）又是 C0 的前提。

---

## 5. 风险与回退

| ID | 风险 | 对策 |
|---|---|---|
| R-C1 | `scenePresenterMode` 在 bridge 构造时读取（`:218`），CI 双组测试可能因包级/进程级共享导致 env 串扰 | 实施 C0 前确认 bridge 构造点与测试隔离方式；必要时把 mode 改为可注入（构造参数），env 只做默认值来源 |
| R-C2 | C1 的"mismatch 即错误"误伤合法差异（chrome/gap 归属边界） | 语义澄清按 IR-7 书面记录；先扩大 parity 测试覆盖面再收紧 |
| R-C3 | C2 增量投影性能（每 delta 全量 Layout） | 过渡方案：脏 cell 集 + 全量 Layout + diff 脏区；基准对照 `active_stream_benchmark_test.go` |
| R-C4 | C2 期间 ActiveStream 与 Scene 双缓冲并存（五副本问题的临时放大） | C2 分两步：先"Scene 为源、ActiveStream 只做节流"，后"ActiveStream 缓冲删除"（对齐实施指导 L3-3） |
| R-C5 | 默认开启后（C3）用户环境出现旧路径未暴露的差异 | env 强制 opt-out 保留至 C4 完成；`/debug` parity 统计作为诊断第一入口 |

**回退原则**：C0-C2 任一阶段红 → flag 关回（零成本，输出等价有测试证明）；C3 后 → env 置空强制回退；C4 完成后无回退路径（旧路径已删除，此时 Scene 是唯一实现）。

---

## 6. 完成定义（与 implementation-guide §9 合并后追加）

1. grep 验证：`AICLI_SCENE_PRESENTER` / `scenePresenterMode` / `blockSourceFn` 回退分支 / `writeRowsLocked` 旧源分支均不存在；
2. 流式 band 渲染源 = Scene mutable cell（/debug 可见 revision 单调 + 无缓冲副本）；
3. scrollback 提交源 = `historyCell.DisplayLines(width)`（与 owned 方案 DoD 第 3 条合并）；
4. 全量测试绿，parity 统计从"双跑对照"演化为"编码器输出审计"（或删除）。
