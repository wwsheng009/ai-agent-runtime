# Phase 2 认知层测量报告（W7b 测量段）

> 路径：`docs/knowledge_Layer/reports/phase2_exploration_report.md`（源码注释中简称 `reports/phase2_exploration_report.md`）。
> 对应：`06` §4 Phase 2「W7 — 验收测量与报告（A/B ≥ 20 任务 + 复算）」；报告模板：`04` §7.7；
> 口径：`04` §7.1–§7.6、`06` §9（G1–G4）。
> 硬规则：**只写已落盘、可复现的证据**；装置自检（fixture）与有界回放（真实语料演练）单独标注；
> 未执行的测量一律标「**待真实 on-mode A/B 实测**」——不编造任何 A/B 收益数字。
> 时间：2026-09-30（装置 / 演练 / 校准注释落盘日，本报告 W7b 定稿）；环境：`knowledge.mode=off`（默认）、
> `KnowledgeMode=off | signals | broad`（默认 off）、adapter=builtin、embedding=off。

## 0. 概述

W7b 交付：G1–G4 测量装置、参数校准（有界回放）与全部复算入口；真实 A/B（同一任务集
off / on 两臂、n ≥ 20）**尚未执行**，本报告不产出任何 Phase 2 收益数字。

| 段 | 状态 | 证据 / 说明 |
|---|---|---|
| 测量装置（G1–G4 复算、median + 95% CI、校准函数） | ✅ 已落盘、复跑全绿 | `knowledge/exploration_report.go`；`exploration_report_test.go` 8 用例全 PASS（§1 命令①） |
| §校准（有界回放，装置演练） | ✅ 已执行 | n=385 真实 grep/view 调用回放；建议 500、保留 800（§2） |
| G1 / G2 / G3 真实数字 | ❌ 未执行 | **待真实 on-mode A/B 实测**（§3–§5、§8） |
| G4 单元 / 装配级可逆 | ✅ 已执行 | `contextmgr` off 基线 `DeepEqual` + `agent` 装配链 off→on→off（§6） |
| G4 端到端（真实会话指标回基线） | ❌ 未执行 | **待真实 on-mode A/B 实测**（§6） |
| Phase 2 验收 | 未完成 | 不得据本报告判 Phase 2 Pass（§10） |

## 1. 测量装置与运行方式（命令）

### 1.1 装置落点

| 文件 | 职责 |
|---|---|
| `backend/internal/knowledge/exploration_report.go` | G1–G4 复算核心：`SummarizePhase2` / `MedianCI95`（order-statistic，无 RNG、可逐位复算）/ `CalibrateTokenBudget`（04 §7.6） |
| `backend/internal/knowledge/exploration_report_test.go` | 装置自检 8 用例：CI 精确值、G1 配对/未配对/零分母、G2 三类硬门槛、G3 p95 增幅、G4 未核验、确定性、校准取整 |
| `backend/internal/knowledge/exploration_report_live_test.go` | env 门控 live 入口（默认跳过）：`TestLivePhase2LedgerG2`（直读真实 `usageledger`）、`TestLivePhase2Report`（两臂样本 JSON 复算 G1–G4） |
| `backend/internal/contextmgr/knowledge_calibration_test.go` | 预算不变量 + 有界回放演练 `TestPhase2CalibrationDrill` |
| `backend/internal/contextmgr/knowledge_test.go`（W5） | G4 单元级：off 基线 `DeepEqual`、off→on→off 可逆、`knowledge_stale_item_injected` 恒 0 |
| `backend/internal/agent/agent_knowledge_test.go`（W7a） | G4 装配级：`attachKnowledgePlanner` 装配链 off→on→off 端到端可逆 |

### 1.2 复跑命令（本次全部实跑，2026-09-30；从仓库根执行）

```powershell
cd backend

# ① 装置自检（8 用例，无外部数据；§3–§6 的 fixture 数字来源）
go test -count=1 ./internal/knowledge/ -run 'TestMedianCI95|TestSummarizePhase2|TestCalibrateTokenBudget' -v

# ② 预算不变量 + W5 注入用例（含 stale 恒 0、可逆）
go test -count=1 ./internal/contextmgr/ -run 'TestKnowledge' -v

# ③ G4 装配链端到端（W7a）
go test -count=1 ./internal/agent/ -run 'TestContextManagerKnowledgeAssembly' -v

# ④ 有界回放（§2 校准数字复跑；env 门控）
#    Resolve-Path 先转绝对路径：go test 子进程工作目录是包目录，相对路径会失配
$env:KNOWLEDGE_PHASE2_SHADOW_CALLS = (Resolve-Path '..\docs\knowledge_Layer\reports\phase1_shadow_calls.jsonl').Path
go test -count=1 ./internal/contextmgr/ -run TestPhase2CalibrationDrill -v
```

live 入口（无真实数据时默认跳过；接入后只复算输入中的真实记录）：

```powershell
$env:KNOWLEDGE_PHASE2_LEDGER = "<usageledger sqlite 路径>"   # G2 直读真实 ledger
go test -count=1 ./internal/knowledge/ -run TestLivePhase2LedgerG2 -v
$env:KNOWLEDGE_PHASE2_SAMPLES_JSON = "<两臂样本 JSON 路径>"  # G1–G4 全量复算
go test -count=1 ./internal/knowledge/ -run TestLivePhase2Report -v
```

## 2. §校准 — 参数校准（04 §7.6：中位数 + 95% CI）

> 本节是**装置自检 / 有界回放**：语料真实（Phase 1 shadow 拦截的真实 grep/view 调用）、
> 路径与生产格式真实，但**不是 A/B 收益实测**；数字可随时用 §1 命令④复现。

- 语料：`reports/phase1_shadow_calls.jsonl` 回放 **n=385**（grep 243 / view 157 / skipped 15；
  跳过 = 非 grep/view、空查询或解析失败）；条目行 / 整条消息用**生产格式**
  （`knowledgeBroadLine` / `knowledgeBroadContent`）构造并测量（1 rune ≈ 1 token 上界估算）。
- 口径：`CalibrateTokenBudget` = 样本 median 的 95% CI（order-statistic）上界按粒度向上取整；
  无样本返回 0（调用方保持默认值，不得把「无数据」当「阈值为 0」）。

| 指标（rune） | median | 95% CI | p95 | max | 建议（CI 上界取整 100） |
|---|---|---|---|---|---|
| broad 单条目行成本 | 394 | [390, 398] | 424 | 472 | 400 |
| 单条目整条消息成本（header 含） | 420 | [416, 424] | 450 | 498 | **500** |
| 查询长度 | 57 | [49, 62] | 91 | 140（min 1） | — |

### 建议值 vs 保留值

| 参数 | 建议 | 保留 | 理由 |
|---|---|---|---|
| `contextmgr.DefaultKnowledgeTokens`（broad 预算） | 500 | **800** | 建议 500 仅覆盖 1 条典型消息（CI 上界 424 取整）、无余量；800 = 500 的 **1.6×**，覆盖单条典型消息（median 420 / max 498）；2 条典型行需 ≥ 813（2×394 + header 25）→ 800 下 broad 实际容纳 1 条典型条目行 + 余量。真实 A/B 未跑，上调/下调均无实测依据 → 保留。 |
| `knowledge.DefaultMinKnowledgeQueryLength` | — | **8** | 实测查询 median 57、min 1：8 是防退化下界，只拦极短查询；无上调证据（上调会削弱复用召回）。 |
| `Strategy.ReuseConfidenceFloor` | — | **0** | 无真实 on-mode 样本，无 A/B 校准数据；0 = 只信 Planner（W3 分档 0.80 / 0.90 / 0.50）。上调触发条件：真实复算出现 `unsafe_reuse_count > 0`，或 G1/G3 不达而需收紧注入面。 |

- 回写入口：真实 A/B 跑完后用 `knowledge.CalibrateTokenBudget(samples, 100)` 产出新预算并
  回写 `DefaultKnowledgeTokens`（§8 步骤 6）。
- 注释一致性：`contextmgr/knowledge.go`（`DefaultKnowledgeTokens`）、`knowledge/planner.go`
  （`DefaultMinKnowledgeQueryLength`）、`contextmgr/manager.go`（`ReuseConfidenceFloor`）三处
  W7 注释与本节引用同一演练、同一数字。

## 3. G1 — 重复工具调用次数下降 ≥ 30%（样本 ≥ 20，附 95% CI）

- **口径**（`06` §9 G1 / `04` §7.2）：同一任务集两臂配对，逐任务计算重复读取
  （`repeated_read_count`）相对下降；判据 = **median ≥ 30%**；CI 用分布无关的
  order-statistic 法（与 Phase 1 报告的 n=3 t 区间均值口径不同，不可混用）。样本不足
  （n < 20）不得判 Pass。
- **装置**：`SummarizePhase2`（配对 / 未配对 / 零分母如实计数）+ `MedianCI95`；live 入口
  `TestLivePhase2Report`（`KNOWLEDGE_PHASE2_SAMPLES_JSON`）。
- **已执行（装置自检，非实测）**：`TestSummarizePhase2G1PairedReduction` —— n=20、10→6
  （逐任务 −40%）→ median 0.40、CI [0.40, 0.40]、汇总降幅 0.40、Verdict=pass；10→9（−10%）
  → Fail；n=19 → `Sufficient=false`、Verdict=incomplete。`TestSummarizePhase2G1UnpairedAndZeroBaseline`
  —— 未配对 2、零分母 1 如实计数。以上是**装置口径自检**，不是 Phase 2 收益数字。
- **未执行**：真实任务集的配对下降与 CI → **待真实 on-mode A/B 实测**（§8 步骤 1–3）。

## 4. G2 — `unsafe_reuse_count = 0` 且 `stale_item_injected = 0`（硬门槛）

- **口径**（`04` §7.3）：复用了 stale / 低于阈值内容并注入 prompt 的次数 = 0；装置同时复算
  `knowledge_version_mismatch_count`（同为 0）。任一非零即 Fail——样本不足也不例外。
- **装置**：`SummarizePhase2.G2`（两臂累加）；live 入口 `TestLivePhase2LedgerG2` 直读真实
  `usageledger` 的 `unsafe_reuse_count` / `knowledge_version_mismatch_count` 并断言为 0
  （`stale_item_injected` 不在 ledger：它来自 `contextmgr` metadata，见下）。
- **已执行（装置自检，非实测）**：`TestSummarizePhase2G2HardGates` 三个子用例
  （unsafe / stale / version mismatch 各注入 1 次）全部 Fail 且 Violations 命中；全零 → Pass。
- **运行时保障（W5 / W6 已落盘证据，非实测数字）**：
  - W5 注入侧：进入 prompt 前二次 stale/version 过滤 + floor 二次过滤
    （`contextmgr/knowledge.go` `buildKnowledgeMessage`）；`knowledge_stale_item_injected`
    恒置 0（`applyKnowledgeMetadata`；`knowledge_test.go::TestKnowledgeStaleAndFloorFiltering`
    断言 metadata 恒 0）。
  - W6 写入侧：`ObservationSource.WriteAllowed()` fail-closed 零写入（`Record` / `process`
    双层拦截，`knowledge/exploration_recorder.go`；矩阵用例 `TestObservationSourceWriteAllowedMatrix`）；
    写入侧跨任务 floor ≥ 0.90；agent 循环置位 `KnowledgeWrite`（`agent/loop_knowledge.go`、
    `agent/loop.go`）。
  - 结论：`unsafe_reuse_count` 由 W5 过滤 + W6 门禁共同保障（单元 / 装配级）。
- **未执行**：真实 ledger 上的 G2 数字 → **待真实 on-mode A/B 实测**（§8 步骤 2；live 入口
  对真实 ledger 断言硬门槛）。

## 5. G3 — 端到端 p95 延迟增幅 ≤ 10%（会话级埋点）

- **口径**（`04` §7.4 / `06` §9 G3）：两臂会话级端到端延迟 p95 增幅 ≤ 10%；最近秩法、
  不插值（与 `baseline.go Percentiles` 同口径）。任务书写「p95 token 开销」，权威规格为
  「端到端 p95 延迟」——本报告按权威规格实现；token 侧由预算上限承载（§2），两指标不混算。
- **埋点核实**：会话级埋点已存在——`usageanalytics` 逐请求 `duration_ms`（含 session 维度
  聚合），**无需新增埋点**；装置按会话级 p95 增幅复算。
- **装置**：`SummarizePhase2.G3`（两臂各 ≥ 20 条延迟样本才 Sufficient；缺埋点 →
  `Checked=false` → Verdict=incomplete，不得判 Pass）。
- **已执行（装置自检，非实测）**：`TestSummarizePhase2G3P95Increase` —— 100→105ms（+5%）
  Pass；100→120ms（+20%）Fail；无延迟样本 → incomplete；19 条样本 → incomplete。
- **未执行**：真实两臂延迟数字 → **待真实 on-mode A/B 实测**（§8 步骤 1、3）。

## 6. G4 — 关闭 `KnowledgeMode` 后指标回到基线（可逆）

- **口径**（`06` §9 G4）：关闭 KnowledgeMode 后行为与指标回到基线（可逆）。
- **已执行（单元 / 装配级）**：
  - W5 `contextmgr`：`TestKnowledgeOffZeroCallsAndBaseline` —— off（空值 / off / 未知值）
    → planner 0 调用、零 `knowledge_*` key、messages + metadata 与基线 `DeepEqual`；
    `TestKnowledgeReversibleOnOff` —— off → on → off 可逆回基线。
  - W7a `agent` 装配链：`TestContextManagerKnowledgeAssemblyReversibleEndToEnd` —— 装配链
    （`attachKnowledgePlanner` + W5 注入）off 与「未挂知识层」基线逐字节一致，on 注入后
    可再次回基线；另有 off-by-default / 未知档位 fail-closed 用例。
- **未执行**：端到端（真实会话 on→off 的 ledger 指标回基线）→ **待真实 on-mode A/B 实测**
  （§8 步骤 1）。

## 7. 已执行 / 未执行边界

| 项 | 状态 | 证据 / 说明 |
|---|---|---|
| 测量装置（复算核心、CI、校准函数） | ✅ 已执行 | §1 命令①复跑全 PASS（8 用例） |
| 有界回放（§2 校准） | ✅ 已执行（装置演练） | §2；标注非 A/B 实测 |
| G2 运行时保障（W5 过滤 + W6 门禁） | ✅ 单元 / 装配级 | §4；`knowledge_stale_item_injected` 恒 0 |
| G4 单元 / 装配级可逆 | ✅ 已执行 | §6；`DeepEqual` 基线断言 |
| G1 / G2 / G3 真实数字 | ❌ 未执行 | **待真实 on-mode A/B 实测** |
| G4 端到端指标回基线 | ❌ 未执行 | **待真实 on-mode A/B 实测** |
| `verify_requested` 消费方接线 | ⏭ 不在本切片 | `06` §4 W7 文件落点 / 测试清单无此项（§9-3） |

## 8. §实测步骤 — 后续实测步骤清单（真实 A/B 跑完后执行）

```powershell
# 1) 真实 A/B：同一任务集两臂（n ≥ 20；on 需显式开启）
#    a. off 臂：knowledge.mode=off（默认）跑任务集，记录 ledger 与会话延迟
#    b. on  臂：knowledge.mode=on + context_knowledge_mode=signals（保守默认）跑同一任务集
# 2) G2 直读真实 ledger（硬门槛断言；任一次非零即 Fail）
cd backend
$env:KNOWLEDGE_PHASE2_LEDGER = "<usage ledger sqlite 路径>"
$env:KNOWLEDGE_PHASE2_SINCE  = "2026-10-01T00:00:00Z"   # 可选：窗口起点（RFC3339）
go test -count=1 ./internal/knowledge/ -run TestLivePhase2LedgerG2 -v

# 3) G1–G4 全量复算（样本 JSON：{"baseline":[TaskSample...],"current":[...],"g4":{...}}）
#    TaskSample 字段见 knowledge/exploration_report.go（task_id 为两臂配对键；
#    repeated_read_count 等取自 ledger；latency_ms 取自会话级埋点；
#    stale_item_injected 取自 contextmgr metadata）
$env:KNOWLEDGE_PHASE2_SAMPLES_JSON = "<phase2_samples.json>"
go test -count=1 ./internal/knowledge/ -run TestLivePhase2Report -v

# 4) 校准演练复跑（§2 数字随时可复现）
$env:KNOWLEDGE_PHASE2_SHADOW_CALLS = (Resolve-Path '..\docs\knowledge_Layer\reports\phase1_shadow_calls.jsonl').Path
go test -count=1 ./internal/contextmgr/ -run TestPhase2CalibrationDrill -v

# 5) 装置自检（无外部数据，CI 必需；§1 命令①–③）
# 6) 回写：knowledge.CalibrateTokenBudget(samples, 100) → DefaultKnowledgeTokens；
#    实测数字回写本报告 §2–§6；Phase 2 状态更新交总收口（06 §1.1 / §4、04 §5）
```

## 9. 反例与偏差登记

1. **真实反例：无**——A/B 未执行，尚无真实失败 / 退化案例。装置负例已覆盖（fixture）：
   G2 三类硬门槛违反；G1 降幅 10%（Fail）与 n=19（incomplete）；G3 +20%（Fail）与埋点缺失
   （incomplete）；G4 未核验（incomplete）。均为装置口径自检，不是 Phase 2 真实反例。
2. **A/B 未执行**：`06` §4 W7 要求真实任务集 on vs off ≥ 20 任务；本会话无真实任务量 →
   只交付装置 + 有界回放 + 复算入口，**不产出收益数字**；Phase 2 验收状态保持「未完成」。
3. **`verify_requested` 消费方不在本切片**：`06` §4 W7 的文件落点与测试清单不含该项；
   若后续切片补接线，消费方落点为 agent 循环，消费信号为 metadata `knowledge_verify_targets`
   + 事件 `context.knowledge.verify_requested`。
4. **装置与演练的边界**：§3–§6 的 fixture 数字与 §2 的演练数字均**不是** Phase 2 收益实测
   数字；本报告不做「以演练冒充实测」的结论。

## 10. 结论与回滚验证

- **装置：Pass（自检）**——G1–G4 复算可执行、确定性（同一批输入两次复算逐位一致）、live
  入口齐备、校准回写入口齐备（§1 命令①–③复跑全 PASS）。
- **Phase 2 验收：未完成**——真实 A/B（≥ 20 任务）与 G1/G2/G3 真实数字、G4 端到端可逆性
  均**待真实 on-mode A/B 实测**；**不得据本报告判 Phase 2 Pass**。
- **回滚验证**：单元 / 装配级已验证（off 与基线逐字节一致、on→off 可逆，§6）；端到端
  （真实会话指标回基线）待 §8 步骤 1 复核。

## 11. 留给总收口的输入

1. `06` §4 W7 行可标注「装置 + 有界回放 + 校准注释已落盘（2026-09-30）；真实 A/B 待跑」，
   Phase 2 状态保持「未完成（待实测）」。
2. W7b 落点：`backend/internal/knowledge/exploration_report.go` / `exploration_report_test.go`
   （8 用例）/ `exploration_report_live_test.go`（2 个 env 门控 live 入口）/
   `contextmgr/knowledge_calibration_test.go`（预算不变量 + 演练）/
   `agent/agent_knowledge_test.go`（G4 端到端）/ 本报告。
3. 校准注释已回写三处（`DefaultKnowledgeTokens` / `DefaultMinKnowledgeQueryLength` /
   `ReuseConfidenceFloor`），数值均保留；依据 = 有界回放 + 待 A/B 回写。
4. `verify_requested` 消费方与 G3 真实埋点数字登记为后续切片输入（§9）。
