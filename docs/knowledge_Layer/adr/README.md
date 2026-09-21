# ADR 索引与流程（决策唯一事实源）

> 最后更新：2026-09-20
> 本目录是 `docs/knowledge_Layer/` 内**决策的唯一事实源**（见 `../README.md` §3）。
> 任何推翻既有设计的改动，必须先在本目录新增/更新 ADR，再改文档与代码。

---

## 1. 为什么需要本目录

`04` 附录 B 与 `supplement/05` §9 曾把"待裁决问题"写成散文条目。这是反模式：

| 反模式 | 后果 |
|---|---|
| 决策写在散文里 | 无法区分"已定/未定/被推翻"；新人重开已决之事 |
| 无状态字段 | 不知道某决定是否仍然生效 |
| 无 gate | "待裁决"永久悬空，直到阻塞某 Phase 才被想起 |
| 无被拒方案记录 | 后人重复提出同一个已被否决的方案 |
| 决策/指标/风险混在一起 | 指标阈值被当成架构决策，或反之 |

本目录按标准 ADR 实践修正这些问题。

---

## 2. 状态生命周期

```text
Proposed ──(owner 接受)──→ Accepted ──(被新 ADR 取代)──→ Superseded by ADR-XXXX
    │                          │
    │                          └──(不再适用)──→ Deprecated
    └──(owner 否决)──→ Rejected
```

- **只有项目 owner 能把 Proposed 改为 Accepted。** 本目录中的 ADR 由方案作者起草，默认为 `Proposed`。
- Accepted 之后 ADR **不可修改正文**；要改就写新 ADR 并在 `Supersedes` 字段引用旧号。
- 状态变更必须同时更新本 README 的索引表。

---

## 3. 可逆性分级（决定"现在定"还是"等证据"）

| 级别 | 含义 | 处理方式 |
|---|---|---|
| **cheap** | 改配置默认值 / 加开关即可回退 | 现在决定，用保守默认 |
| **moderate** | 需改代码但无持久化影响 | 现在决定，写清回退路径 |
| **expensive** | 涉及持久化 schema / 对外协议 | **只有在无部署数据时才允许现在决定**；否则等 Phase 0 证据 |

> **关键前提**：本方案 **Phase 0 尚未开始**，`knowledge.db` 在任何机器上都不存在。
> 因此 schema 类决策当前的迁移成本为 **零**。这是采纳"干净模型"而不是"向后兼容模型"的决定性理由——
> 现在清理是免费的，上线后清理是 10–100 倍成本。

---

## 4. Decided-by gate（防止悬空）

每个 ADR 必须声明 gate，取值之一：

| Gate | 含义 |
|---|---|
| `Phase0-baseline` | 必须在 Phase 0 基线跑完、拿到数据后才能定阈值部分。**仅适用于 Phase 0 能产出的数据**（`mode=off` 下的 token / 延迟 / 索引构建成本） |
| `Phase1-shadow` | 必须在 **Phase 1 shadow 实测**后才能定阈值部分。适用于**需要 shadow 对比数据**的阈值（如 ADR-0003 的 α）——Phase 0 为 `mode=off`，**结构上产不出**该类数据（2026-09-21 新增，见 ADR-0003 §10） |
| `Phase1-start` | 阻塞 Phase 1 开工，必须在此之前 Accepted |
| `PhaseN-start` | 阻塞对应 Phase |
| `none` | 无阻塞，可随时定 |

---

## 5. 索引表

| ADR | 标题 | 状态 | 可逆性 | Gate | 取代 |
|---|---|---|---|---|---|
| [0001](0001-project-module-language-schema.md) | Project/Module/Language 模型收敛 | Proposed | expensive（但当前零迁移成本） | **Phase1-start** | `02` 的 `language_projects` |
| [0002](0002-acp-lsp-ownership.md) | ACP 下 LSP 归属与能力面 | Proposed | cheap | Phase4-start | `supplement/05` §2.3 的 `external_preferred` |
| [0003](0003-exploration-attribution-metrics.md) | 探索归因与 shadow 差异率度量 | Proposed | cheap（仅测量） | **Phase1-shadow**（阈值）/ Phase1-start（口径） | `supplement/05` §9.3 |
| [0004](0004-stale-index-tool-surface.md) | 陈旧索引下的 `code.*` 工具面 | Proposed | cheap | Phase2-start | `supplement/05` §9.4 |

> **Gate 可达性修订（2026-09-21）**：`0004` §10 的 `S_fresh` / `S_max` 取值 Gate 由 `Phase0-baseline` 改为 **`Phase2-start`**——二者是 **reader 观测到的陈旧度**阈值，需要索引 + 多进程仲裁 + 心跳数据，Phase 0（`mode=off`、无索引、无 reader）**结构上产不出**。注意：`0004` 的 Accept **本来就不被该值阻塞**（§4.1 已声明 60s / 15min 为初始值），此处仅修正 Gate 指向。
| [0005](0005-windows-child-process-lifecycle.md) | Windows 子进程树生命周期与复用既有 process guard | Proposed | moderate | Phase4-start | `supplement/05` §9.5 |
| [0006](0006-lsp-position-encoding-boundary.md) | LSP 位置编码转换边界与缓存键 | Proposed | moderate | Phase4-start | 澄清并补齐 `03` §5.3 |
| [0007](0007-phantom-tables-and-doc-invariants.md) | 幽灵表清理与文档不变量 | Proposed | cheap | Phase1-start | `02` §8 的 6 个无 DDL 表名；`04` L546 的 `index_jobs` DDL 落点 |

> **落盘状态（2026-09-20）**：`0000`（模板）与 `0001`–`0007` 均已落盘，**全部为 `Proposed`**，等待 owner 按 §2 逐个 Accept。
> **注意**：ADR **不得复制 DDL**（§7）。ADR-0007 §4.3 要求把 `04` 中的 `CREATE TABLE index_jobs` 迁移到 extension schema；
> 该迁移在 ADR-0007 被 Accept 之前**不执行**，但已在 `04` 相应位置与本节标注为待办。

---

## 6. 模板

见 [`0000-template.md`](0000-template.md)。

---

## 7. 与其他文档的关系

```text
01 架构意图 ──┐
02 core schema ──┼──→ adr/*.md（决策） ──→ 03 supplement/*（扩展 schema） ──→ 04（落地计划/验收）
03 extension ──┘
```

- ADR 不复制 DDL。ADR 说"改什么、为什么、代价是什么"，DDL 的唯一事实源仍是 `02`（core）与 `03`/`supplement`（extension）。
- **DDL 规则的精确边界（2026-09-20 补充）**
  - ADR **不得复制** `02`/`03` 中**已存在**的 DDL——那是同一对象的第二处定义，必然漂移。
  - ADR **可以**给出**新增对象**的**拟议 DDL**（该对象尚无事实源，不存在"第二处"）；一旦 ADR 被 Accept，DDL 必须落到 `02`（core）或 `03`/`supplement/*`（extension），ADR 改为引用。
  - 本规则**只约束 `knowledge.db`**。其他库（如 usage ledger）的 schema 事实源是其自身代码——例如 `backend/internal/usageledger/sqlite_store.go` 的 `init()` 语句列表——不适用本规则。
- ADR 不写验收阈值。阈值属于 `04` §7，且必须由 Phase 0 基线校准。

---

## 8. 待办：把 `04` 附录 B 的 18 条矛盾点也转成 ADR

`04` 附录 B 目前是散文列表。**同一套反模式**。建议在 Phase 1 开工前：

1. 逐条判定：是"决策"还是"文档笔误"还是"风险"。
2. 决策 → 转 ADR；笔误 → 直接改文档并在 `CHANGELOG.md` 记录；风险 → 留在 `04` §6。
3. 附录 B 最终只保留一个指向本目录的指针表。

在完成之前，附录 B 不构成决策依据。

**已完成的映射（部分）**

| 附录 B 条目 | 对应 ADR | 状态 |
|---|---|---|
| B3（阈值是否写死） | [0003](0003-exploration-attribution-metrics.md) | 口径已定，阈值待 `Phase1-shadow`（2026-09-21 修订，原为 `Phase0-baseline`） |
| B6（项目模型重叠） | [0001](0001-project-module-language-schema.md) | Proposed |
| B15（schema 应用顺序） | 待转 ADR（与 [0007](0007-phantom-tables-and-doc-invariants.md) 的 I2 相关） | 未开始 |
