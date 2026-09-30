# ADR-0008: grep 通道探索归因采用 file-level 覆盖口径

- **Status**: Proposed
- **Date**: 2026-09-29
- **Deciders**: 方案作者（起草，Proposed）/ 项目 owner（Accept 或 Reject）
- **Gate**: `Phase1-shadow`（数据前提 2026-09-29 已满足，见 §1）
- **Reversibility**: cheap（仅测量：新列可空 / `DEFAULT 0`，撤销 = 停止读写新列，无数据迁移）
- **Supersedes**: 部分取代 [ADR-0003](0003-exploration-attribution-metrics.md) §4.2 中 **grep 通道**的 coverage 口径；
  ADR-0003 仍为 Accepted，**view 通道口径与 §4.2 的 economy/usable 公式不变**。
- **Related**: `04` §5 Phase 1 / §7.6；[`reports/phase1_shadow_report.md`](../reports/phase1_shadow_report.md)
  §4.1 / §4.3 / §5；代码 `backend/internal/knowledge/shadow.go`、`backend/internal/usageledger/sqlite_store.go`

---

## 1. Context

事实与证据（全部可复核）：

1. **现行口径（ADR-0003 §4.2，Accepted）**：`coverage := overlap_n / baseline_n`（行级）；
   `economy := candidate_tokens / baseline_tokens`；`usable := (coverage ≥ α) AND (economy ≤ 1.0)`（§4.2）。
2. **ADR-0003 §6.2 已预告**该口径对"索引答案更好但不重合"的调用会低估，并写明
   "**这是本 ADR 已知的最大度量偏差，必须写进 Phase 0 报告**"；§10 把
   "`coverage` 低估问题的**抽样核对报告**"列为 `Phase1-shadow` 待办。
3. **2026-09-29 实测**（`Phase1-shadow` 真实调用重放：本仓库 400 条真实 `grep`/`view`，
   见 `reports/phase1_shadow_report.md`，复算命令同报告 §6）：

   | 指标 | grep 通道 | view 通道 |
   |---|---|---|
   | 分母（`baseline_n > 0`） | 213 | 157 |
   | 行级 M2 覆盖度 | **3.79 %** | 51.76 % |
   | 行级 coverage p50 / p90 | 0 / 0.080 | 0.733 / 1.0 |
   | 行级 M1（α=0.8） | **0.47 %** | 48.41 % |
   | **file-level 覆盖（同一批数据）** | mean **31.83 %**、p50 0、p90 **100 %** | — |

   file-level 分母 213，`usable@0.8 = 26.76 %`，`answerable_rate = 49.38 %`（`candidate_n > 0`），
   grep 通道 M4 = 63.00 %。
4. **候选并非为空**：49.38 % 的 grep 调用有非空候选；p90 文件级完全命中。
   行级 ≈ 0 的主因是口径：baseline 是**文本命中的所有行（含上下文行）**，
   候选是**符号定义行 + 已解析引用点**（refs 仅覆盖 call/import 类）——两集合的行号天然错位。
5. **§10 待办的完成度**：本次以**全量重放**（400 条，非抽样）量化了低估偏差，
   并同时给出候选来源与文件级对照；`04` §5 与报告已落盘。本 ADR 视为对该待办项的回应。

## 2. Decision Drivers

按重要性排序，均可检验：

- **D1 有效性**：判据必须贴近"索引能否帮到 agent"，且不被上下文行/文本行噪声主导。
- **D2 可现场测量**：live 数据必须**直接可算**——不得依赖离线重放重建（`exploration_attribution`
  只存聚合值，live 阶段没有原始输出）。
- **D3 通道可比性**：view 通道口径已验证可用（M1=48.4 %）；grep 需要各自合理的口径，
  且 M1 必须**按通道报告**。
- **D4 数据连续性**：不得改变历史行语义；追加列取默认值时的既有聚合结果与改动前一致
  （ADR-0003 §4.1 的 D3 三条检验）。
- **D5 成本与可逆性**：优先 additive、无迁移；撤销成本接近零。
- **D6 兼容性**：不推翻 ADR-0003 的表结构决策；口径变更经新 ADR（本文件）。

## 3. Considered Options

| 选项 | 描述 | 优点 | 代价 |
|---|---|---|---|
| **A（推荐）** | grep 主判据改 **file-level**（新增 `baseline_files_n` / `overlap_files_n` 两列）；行级列保留为诊断 | 判据与"找到相关文件"直接相关；live 可算（D2）；历史行语义不变（D4） | 比行级宽松，需行级诊断防"返回整个文件"；追加两列 |
| B | 把现有 `coverage` 列**改为** file-level 语义（不加列） | 零 schema 变更 | 历史行语义混杂（新旧口径无法区分），违反 D4；无法回溯 |
| C | 保持行级，仅把"`candidate_n > 0`"加入门槛 | 改动最小 | 可被"返回整个文件/空壳候选"钻空子（ADR-0003 §9 已否决"只要非空"）；不反映定位质量 |
| D | 维持现状（行级）仅调 α / 门槛 | 零改动 | 实测表明 α∈[0.1, 0.8] 对 grep M1 影响可忽略（恒 ≈0.5 %），等于放弃 grep 通道的度量能力 |
| E | 把 per-file / per-symbol 归因层提前到 Phase 1 | 信息最完整 | 超出测量范畴，schema 与工作量更大；ADR-0003 §10 已将其列为 `Phase2-start` |

## 4. Decision

**采用选项 A，并吸收 C 的诊断项**：

1. grep 通道的"可用"判据改用 **file-level** 覆盖：
   `file_coverage := overlap_files_n / baseline_files_n`，`usable := (file_coverage ≥ α) AND (economy ≤ 1.0)`。
2. 行级 `coverage`（`overlap_n / baseline_n`）与 `answerable_rate`（`candidate_n > 0` 占比）
   作为**诊断指标**同时报告——不判 Pass/Fail，用于防止"返回整个文件"式退化。
3. **view 通道口径不变**（行级区间覆盖）；M1 及门槛**按通道分别报告**。
4. **α 与门槛数值不在本 ADR 决定**：仍由 `04` §7.6 的校准流程（中位数 + 95 % CI）决定；
   本 ADR 只裁定"用哪个量来算"。当前实测建议阈值见报告 §4.4（暂不写死）。
5. 新增列以 additive 迁移落到 `usageledger` 的 `statements`（该库 schema 的事实源是其代码，
   按 `adr/README.md` §7 的边界，不属 `knowledge.db` DDL 规则）。

## 5. Rationale

- 对 **D1**：file-level 直接回答"索引是否指向了 agent 实际需要的文件"；行级额外要求
  "行号也一致"，而索引答案（定义 + 已解析引用）**本来就不以逐行重合为目标**——
   §1.4 的实测把这一点量化了（行级 0.47 % vs 文件级 usable 26.76 %）。
- 对 **D2**：文件集合在观察时即可取得（两侧 path 集合），可在 writer 侧计算并落两列；
  行级的 0 分无法在 live 阶段"事后修复"，因为它由口径产生而非数据缺失。
- 对 **D3**：view 的区间语义与符号 span 天然对齐（M1=48.4 %），强行统一口径会
  要么放宽 view、要么冤枉 grep；按通道报告更诚实。
- 对 **D4**：两列为追加列、`NULL`/`DEFAULT 0`，历史行聚合不变——满足 ADR-0003 §4.1 D3 三条检验。
- 对 **D5/D6**：撤销 = 停止使用新列（无迁移）；本 ADR 不推翻 ADR-0003 的表结构与
  economy/usable 公式，只替换 grep 的覆盖量。
- 被否决选项的真实缺陷（不是"不够好"）：
  - **B**：同一列两套语义，Phase 1 期间两种口径的样本会混在一起，且无法识别哪些行是哪种；
  - **C**：ADR-0003 §9 已明确否决"只要非空"（可被返回整文件钻空子），本次实测也无法
    用该判据区分"命中文件"与"全库返回"；
  - **D**：在 α∈[0.1, 0.8] 区间 grep M1 恒 ≈0.5 %，该通道将永远 Fail 且无法归因；
  - **E**：per-file 归因层需要新表与新写入路径，属于 Phase 2 的"重复探索"问题域，
    用它来救度量口径是范围外扩张。

## 6. Consequences

### 6.1 Positive

- grep 通道获得**可测且可归因**的判据；Phase 1 验收不再被已预告的口径偏差永久阻塞。
- 两条通道各自的口径与其语义匹配（grep=文件定位，view=区间读取）。
- 诊断项（行级 coverage、answerable_rate、token 经济性）保留，"更精炼的答案"不会被误判，
  也不给"返回整文件"留后门。

### 6.2 Negative / Accepted trade-offs

- **file-level 比行级宽松**：同文件不同行也算命中。主动接受：以行级诊断 + 人工抽查
  （Phase 1 验收样本 ≥ 30 条）补偿；真正的行级问答应由 `Phase2-start` 的 per-file 归因层回答。
- **新增两列**：`exploration_attribution` 宽度 +2；接受。
- **历史数据（2026-09-29 之前）不带 file-level 值**：新列默认 0，历史行只用于行级诊断，
  不参与 file-level M1。接受：该表投入生产前的数据仅用于本次校准。

## 7. Reversal Plan

- 回退到行级口径：停止写入/读取两列即可（`coverage` 列未动），**无数据迁移**；
- 如需彻底移除：按 `usageledger` 自身迁移规则追加 drop（或保留空列），成本 ≈ 0。

## 8. Validation

- 用 `reports/phase1_shadow_calls.jsonl` 复算：file-level 指标须与报告 §4.3 **逐位一致**
  （±1e-9；复算入口同报告 §6）。
- D4 可检验：新列加入后，对历史行/全表 `SUM`/`COUNT`/`AVG` 的结果与改动前一致。
- 诊断联动：report 同时输出 file-level 与行级，若两者差异缩小到 < 2 倍，
  说明候选映射已接近文本检索语义，可再评估是否合并口径（列入 §10）。
- Phase 1 验收：三入口 live 数据须写入新列（接入验证项），M1 按通道报告。

## 9. Alternatives Rejected (and why)

| 方案 | 否决理由（一句话） |
|---|---|
| B 改列语义 | 历史行口径混杂、无法区分版本，违反 D4。 |
| C 只要非空 | ADR-0003 §9 已否决"只要非空"（返回整文件即可骗过）。 |
| D 维持行级调阈值 | 实测 α∈[0.1, 0.8] 恒 ≈0.5 %，该通道将永久 Fail 且不可归因。 |
| E 提前 per-file 层 | 属 Phase 2 范围（重复探索），用于救度量口径是范围外扩张。 |

## 10. Open Follow-ups

| 项 | Gate |
|---|---|
| α 与 Phase 1 门槛数值（按通道校准，`04` §7.6） | `Phase1-shadow`（数据已具备） |
| 新列实现（additive 迁移 + writer/reader + 测试） | 本 ADR Accept 后 |
| 三入口 live 数据写入新列（接入验证） | Phase 1 验收前 |
| per-file / per-symbol 归因层（回答"命中文件里是否真的读了那一行"） | `Phase2-start`（ADR-0003 §10 原项） |
| grep 候选映射继续收敛（`literal=true`、glob+path 双约束、未解析标识符 refs 扩展） | 工程项，不阻塞本 ADR |
| file-level 与行级差异收敛后是否合并口径 | `Phase2-start` |
