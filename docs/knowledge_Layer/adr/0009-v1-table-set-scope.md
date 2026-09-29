# ADR-0009: v1 表集口径裁决（上限定义域、三分组与命名规范）

- **Status**: Accepted
- **Accepted**: 2026-09-29（项目 owner 授权代改并记录裁决，见 `../CHANGELOG.md` 的 2026-09-29 ADR-0009 落地条目）
- **Date**: 2026-09-29
- **Deciders**: 项目 owner（方案作者起草）
- **Gate**: `Phase1-start`
- **Reversibility**: cheap（纯文档口径 + 重跑检查脚本；无 schema 变更、无数据迁移）
- **Supersedes**: `04` §0.3 的"v1 表集 ≤ 16 张"与 §4.3 标题的同一口径；`docs/knowledge_Layer/README.md` §3 的转述；ADR-0001 §2 D7 引用该口径的数字部分（ADR-0001 的模型决策本身不取代）
- **Related**: ADR-0007 §4.1/§4.2/§4.4/§10；ADR-0001 §4.2/§4.4/§4.7；`04` §0.3（L38）、§4.3（L341–561）、R7（L1045）；`02` §8（L282–339）与 DDL（L346–1065、L2185）；`supplement/01` §1.3、`supplement/02` §2.2、`supplement/03` §3.2、`supplement/06` §6.3、`supplement/13` §13.2、`supplement/15` §15.3；`06` §9 #2/#3/#7/#19/#20 与 L125；`backend/scripts/check_knowledge_doc_invariants.go`

---

## 1. Context

### 1.1 I5 实测：声明 ≤16 vs 实际 23

**声明侧**（`04`，落地计划与验收事实源）：

- §0.3（L38）："**v1 表集 ≤ 16 张**（含 FTS 虚拟表与 migration 表），03 附录 A 的 50 张表按 P0/P1/P2 重新裁剪，类型系统/跨语言/Runtime Evidence 明确推迟。"
- §4.3（L341）标题："**v1 最小数据模型（16 张）**"；其下 14 个 `CREATE TABLE` + 1 个 `CREATE VIRTUAL TABLE`（L345–561），加上已按 ADR-0007 §4.3 迁出的 `index_jobs`（现落 `supplement/15` §15.3），合计 16 个对象。

**实际侧**（`02`，core schema 唯一事实源）：

- §8 总览块（L300–339）列 **28 个名字**；
- `02` 的 DDL 为 **22 张** `CREATE TABLE`（L346–1065），另有 1 张虚拟表 `symbol_fts`（L2185）；
- 即 `02` 侧实际 **23 个对象** = 22 表 + `symbol_fts`。

**差额的构成（两个数字各自的分母）**：`04` §4.3 的 16 = 12 个 core 概念（`workspaces`、`files`、`symbols`、`symbol_versions`、`refs`、`exploration_sessions`、`exploration_nodes`、`exploration_edges`、`context_snapshots`、`context_items`、`cache_entries`、`invalidation_events`；其中 `refs` 在 `02` 叫 `references`）+ 2 张 extension（`symbol_aliases`、`index_jobs`）+ 1 张 migrate 簿记（`schema_migrations`）+ 1 张虚表（`symbols_fts`）。也就是说，**"16" 是四个不同定义域（core 子集 / extension / 基础设施 / 虚表）的并集，而 "23" 是 02 的 v1 core 全量（含虚表）**——两个数字根本不在数同一个集合。这是 I5 反复失败的结构性原因，不是笔误。

**检查器与命令**：

- 脚本：`backend/scripts/check_knowledge_doc_invariants.go`（ADR-0007 §4.4 的 I1–I5 实现；仅标准库；退出码 0=全 PASS、1=存在 FAIL、2=运行错误）。
- 命令（默认从 `backend/` 目录运行，`-docs` 相对 cwd 解析）：

  ```text
  cd backend
  go run scripts/check_knowledge_doc_invariants.go
  # 等价显式路径：
  go run scripts/check_knowledge_doc_invariants.go -docs ../docs/knowledge_Layer
  ```

- I5 判据（脚本 L726–796）：先取 `04` §4.3 标题中"v1 最小数据模型（N 张"的 N；若无（或其次）取 §0.3 的"v1 表集 ≤ N 张"；实际数取 `02` §8【v1 core】成员数（无该分组时回退为 `02` DDL 集合）；`actual ≤ declared` 才 PASS。
- 实测记录（`06` §9 待办 #3，2026-09-29）：**当前 5/5 FAIL**；其中 **I5 = 声明 16 < 实际 23（22 表 + `symbol_fts` 虚表）**；I1 另有 6 个幽灵名 + `symbol_fts` 未列；I4 尚无三分组。同一结论见 `06` §9 #7："裁决输入已就绪（两问：虚表是否计入上限；`index_jobs` 等 extension 归属）"。

### 1.2 待裁决的两个问题（#7）

| # | 问题 | 为什么必须由 ADR 裁决，而非改数字了事 |
|---|---|---|
| ① | 上限是否含**虚拟表**（`symbol_fts`）与 **migration 表**（`schema_migrations`） | `04` §0.3 自述"含 FTS 虚拟表与 migration 表"；但已落盘实现 `backend/internal/knowledge/migrations/0001_init.sql` 头注 1 明确：`schema_migrations` 由 `internal/migrate` 统一创建，结构与 §4.3 不同、且不落在知识 schema 里。两者对"migration 表是否属于知识模型"的回答相反 |
| ② | `index_jobs` / `events` 等 **extension 表**与"类型系统/跨语言"表如何计入 | `index_jobs` 是 v1 必需但 DDL 在 `supplement/*`；`events` 已被 `events_outbox` + `consumer_offsets` 取代（v2+）；类型系统/跨语言整体推迟（`04` L564）。不先定义"上限的定义域"，这些表算不算、算进哪个数字都说不清 |

### 1.3 与 ADR-0007 的关系

ADR-0007 已 Accepted（2026-09-28）：其 D7 要求"表数上限声明必须与 DDL 实际数量自洽"（即检查器 I5），§10 明确把"`04` §4.3 'v1 ≤ 16 张'的原文口径核对与裁决"列为 `Phase1-start` 待办。本 ADR 是该待办的裁决稿，并连带回应 `06` §9 的 **#7**（I5）、**#2/#20**（6 幽灵名去向与 `02` §8 三分组）与 I3 两对命名漂移的规范名选择（见附录 A–D）。

---

## 2. Decision Drivers

| # | 判据 | 可检验形式 |
|---|---|---|
| D1 | 每个"表数"声明有**唯一、显式的定义域**，与一个机械可解析的集合一一对应 | 检查器能报出"16 在数哪个集合、23 在数哪个集合" |
| D2 | **数字不得反向裁剪模型**：不为守住上限而把已存在、已被引用的 core 表搬出唯一事实源（对偶于 ADR-0007 "不为不存在的名字建表"） | `02` 的 DDL 集合不因口径裁决而缩小 |
| D3 | 与已落盘实现和事实源分层自洽 | `02`=core 唯一源、`supplement/*`=extension 唯一源；`schema_migrations` 归 `internal/migrate` |
| D4 | 机械可检验且**不改变检查器语义**（检查器不承载主观解释） | 现有 I5 的 "declared vs【v1 core】数量" 语义即为最终语义 |
| D5 | 保留"防过度设计"护栏 | 新增 core 表必须走 ADR 评审（`04` R7）；I5 会拦截"加了表没改数字" |

---

## 3. Considered Options

| 选项 | 含义 | 后果 | 迁移成本 |
|---|---|---|---|
| **A. 维持 ≤16：把超出的表移出 core** | 上限数字不变（16）；把 `02` 的 core 从 23 收缩到 ≤16——即把 `repositories`、`commits`、`file_versions`、`calls`、`imports`、`tasks`、`task_files`、`task_symbols`、`tool_calls`、`tool_results` 中的若干表的 DDL 从 `02` 搬到 extension/deferred 区 | 为让数字成立而重建模型边界：`02` 不再是完整 core 事实源；这些表已被 `01`/`04`/`06`/实现与后续 Phase 引用，搬走会产生一批跨文件指针与新的"幽灵引用"；每个被搬走的表还要在 `supplement/*` 重新落 DDL（或变幽灵） | **高**（`02` §8+DDL 大改 + 多文档引用改写；无 DB 迁移） |
| **B. 上限按【v1 core】实际数更新（16→23）** | 只改 `04` §0.3/§4.3 与 `README.md` §3 的数字（16→23）；不动分组定义与语义 | I5 可立即绿；但"v1 表集"的定义域仍悬空：`index_jobs`/`symbol_aliases` 算不算？类型系统表算不算？下一次 extension↔core 流动又会漂移；数字是快照而非约束 | **低**（3 处文档数字） |
| **C. 上限与分组解耦：上限只约束【v1 core】** | "v1 表集上限"的定义域 = `02` §8【v1 core】= `02` 携带 DDL 的集合（22 表 + `symbol_fts` 虚表；`schema_migrations` 不计入）。数字同步为 **23**；`04` §4.3 的 16 张明确为 "P0/P1 最小子集"；extension/deferred 不计入上限，其准入由 "没有它哪个 Phase 会失败"（`06` §7）与 ADR 记录约束 | I1/I4/I5 可同时转绿且**不需要改检查器**；上限保留为 "core 变更必须同步数字" 的护栏；extension 表的增删不再牵动数字 | **低**（`04` 两处 + `README.md` §3 + `02` §8 分组；无脚本改动、无 DB 迁移） |

> 三个选项的共同前提：不新增/不复制任何 DDL（ADR-0007 D6）；涉及 DDL 的修改一律属于 `02`/`supplement/*` 而非 ADR。

---

## 4. Decision

采纳 **选项 C**（上限与分组解耦，只约束【v1 core】；数字 16→23），并做以下四项配套。

### 4.1 定义域（问题①的答案）

| 集合 | 是否计入"v1 表集上限" | 依据 |
|---|---|---|
| 【v1 core】（`02` 携带 DDL 的 22 表 + `symbol_fts` 虚表） | **计入**（当前 23） | ADR-0007 I1 已要求 core 名单含虚拟表；虚表是 FTS 检索能力的实际承载 |
| `schema_migrations`（migration 簿记） | **不计入** | `0001_init.sql` 头注 1：由 `internal/migrate` 统一创建并维护（结构不同、会与 §4.3 冲突）；它不是知识模型的表 |
| extension（`supplement/*` 的 DDL，`index_jobs` 等） | **不计入** | extension 是独立事实源；"被启用"≠"占 v1 配额" |
| deferred（`04` §4.3 推迟清单，含类型系统/跨语言/Runtime Evidence） | **不计入** | 不在 v1，天然不占配额 |

- `04` §0.3 的"含 FTS 虚拟表与 migration 表"一句，裁决落地时改写为"**含 FTS 虚拟表；不含 migration 簿记表（归 `internal/migrate`）、不含 extension/deferred**"。

### 4.2 数字与表述

- `04` §0.3 改为：**"v1 表集 ≤ 23 张（= `02` §8【v1 core】的 22 表 + `symbol_fts` 虚表；不含 extension/deferred 与 `schema_migrations`；P0/P1 最小子集见 §4.3）"**。
- `04` §4.3 标题改为**不携带 "v1 表集" 语义**的表述，例如 "P0/P1 最小数据模型（16 张；v1 core 的子集）"——其 16 从此是"最小子集"的二级数字，不再充当 `v1 表集上限` 的声明源（检查器 I5 的声明源随之只落 §0.3）。**注意**：若保留原标题措辞，脚本会先取到 16 并在 §0.3 也读到 16，I5 仍 FAIL。
- `docs/knowledge_Layer/README.md` §3 的"v1 表集：≤ 16 张（见 04 §4.3）"同步为 "≤ 23 张（见 `04` §0.3；=`02`【v1 core】）"。

### 4.3 新增 core 表的规则（问题②的延伸）

- 任何新增 core 表：先回答"没有它哪个 Phase 会失败"（`06` §7），在 ADR 中记录，并**同步 §0.3 的上限数字**；漏改由 I5 拦截。
- extension/deferred 表不占数字，但同样受"没有 Phase 会失败否则推迟"约束；**不得以"数字还有余量"为由把表塞进 core**（这正是选项 A 的错误方向）。

### 4.4 连带裁决（#2/#20 与 I3）

- 6 个幽灵名的去向：见 **附录 B**（`branches`/`events`/`language_projects` 删除；`inheritance`→`inheritance_edges`、`dependencies`→`dependency_versions` 改名指向；`index_jobs` 归 extension）。
- 28 名三分组与 `symbol_fts` 补列：见 **附录 A**；供 #20 落地的骨架见 **附录 D**。
- I3 两对漂移的规范名：见 **附录 C**（选带 DDL 的 extension 事实源名；无 DDL 的裸名不进入模型名集）。
- 连带解锁：裁决落地后，`04` §4.3 其余 15 处 DDL 的引用化（#19）即可执行；`02` §8 顶部"本块与下方 DDL 不一致"的旧注记同步删除。

---

## 5. Rationale

逐条回应 Decision Drivers，并点出被否决选项的真实缺陷（不是"不够好"）。

- **D1**：A/B 都没有回答"16 与 23 各在数什么"。C 把定义域钉死在【v1 core】上，与检查器 I5 的既有语义（declared vs core 数量）一一对应——**是定义域对齐，不是让检查器猜**；检查器一行不改。
- **D2**：选项 A 的真实缺陷是**让数字反向裁剪模型**。ADR-0007 §2 D6 与 §5 已确立"总览与 DDL 一致"的达成方式必须是**删掉不存在的名字**，而不是为不存在的名字建表；其对称命题是：**也不应为守住一个未经定义的 16 而把存在的 core 表搬走**。`repositories`/`commits`/`tasks`/`tool_calls` 等已被多份文档与后续 Phase 引用，搬走它们只会制造新的幽灵引用——与 ADR-0007 所否决的"为对齐而造表"是同一个目标错位。
- **D3**：C 与三层事实源（`02`=core、`supplement/*`=extension、`internal/migrate`=簿记）完全一致；B 的 23 缺少"不含什么"的声明；A 则与已落盘实现（`0001_init.sql` 的 14 表 + 代码懒建 FTS + migrate 自带簿记表）以及各 Phase 计划冲突。
- **D4**：I5 现有实现（`04` 声明 vs `02`【v1 core】数量）恰是 C 的语义；B 只改数字会立刻暴露"extension 算不算"的新歧义。
- **D5**：C 保留上限的护栏作用：core 是"要花钱维护的集合"，其增长需要 ADR + 数字同步（I5 拦住"加表不改数字"）；extension/deferred 继续用"Phase 存活"判据管理，不靠数字。
- **对 ADR-0007 立场的引用**：ADR-0007 §2 D6"不得为'对齐'而制造新表"与 §5"达成方式必须是删掉不存在的名字"。本 ADR 的对称原则是"**数字服务于模型，模型不服务于数字**"；A 与 ADR-0007 被否决的选项 A 是同一类错误，只是方向相反。

---

## 6. Consequences

### 6.1 Positive

- #7 的两问有明确答案；"v1 表集 ≤ N"从此只有一个定义域与一个数字（23）。
- #2/#20 可直接执行：三分组骨架（附录 A/D）满足 I1/I4；6 幽灵名一次性有去向（附录 B），不再逐次裁决。
- I3 两对漂移有规范名（附录 C），并给出"旧名不进入名集"的机械同时满足方式。
- extension 表（`index_jobs`、`symbol_aliases`、`projects` 等）不再被 16 的紧缩叙事误伤；后续 Phase 的表（`calls`/`imports`/`tasks` 等）留在 core。

### 6.2 Negative / Accepted trade-offs

- **上限数字从 16 变成 23**，失去"16 张"的紧缩叙事。主动接受：23 是 core 的事实规模；紧缩应由分组与 ADR 评审承担，而非一个不准的数字。
- **两个数字并存**（§0.3 的 23 与 §4.3 的 16）。主动接受：需要读者理解"上限 vs 最小子集"的关系，但这比三个定义域混成一个数字更可检验；§4.3 标题会显式标注"子集"。
- **core 每一次增表都要同步数字**。主动接受：这正是护栏；I5 失败是提醒而非阻碍。
- **ADR-0001 §2 D7 引用"≤16"的历史表述无法回改**（Accepted ADR 正文不可变）。主动接受：本 ADR 明确取代该数字口径，后续引用以 `04` §0.3 为准。

---

## 7. Reversal Plan

- **未落地前**（Proposed 阶段）：直接废弃本 ADR，`02`/`04`/`README.md` 零改动；重跑检查器回到 5/5 FAIL 基线。
- **已按 C 落地后回退**：git revert 四类文档改动（`04` §0.3/§4.3 标题、`README.md` §3、`02` §8 分组、以及 #19 的引用化如已执行）；重跑 `cd backend; go run scripts/check_knowledge_doc_invariants.go`，I5 重新 FAIL（16<23）作为提醒。
- **想改用 A/B**：属新 ADR 范畴（推翻 §4 口径），不在本 ADR 的回退路径内。
- **无数据迁移**：本 ADR 不改任何 schema，`knowledge.db` 表结构不动。回退成本 = 文档改动 + 一次脚本重跑。

---

## 8. Validation

命令（两条等价）：

```text
cd backend
go run scripts/check_knowledge_doc_invariants.go
```

（或 `go run scripts/check_knowledge_doc_invariants.go -docs ../docs/knowledge_Layer`；exit 0=全 PASS、1=有 FAIL、2=运行错误。）

**I1/I4/I5 全绿的精确条件**：

| # | 条件 | 机械判据 |
|---|---|---|
| I1 | `02` §8【v1 core】逐名等于 `02` 的 DDL 集合：22 表 + `symbol_fts (virtual)`，共 23 名；不得含 extension/deferred 名字，不得漏 `symbol_fts` | 两集合相等（"总览列名但 02 无 DDL"与"02 有 DDL 但总览未列"两侧均空） |
| I4 | 总览块含全部三组（【v1 core】/【extension】/【deferred】或【已推迟】）；28 个旧名要么属于恰一个组，要么以**不被解析为表名**的形式保留注记（`——` 之后的文字、`~~删除线~~` 均不进入名集）；无分组外名字、无跨组重复 | 交集为空、并集 = 块内全部被解析名字、三组齐全 |
| I5 | `04` §0.3 声明 "v1 表集 ≤ 23 张"；§4.3 标题不再匹配 "v1 最小数据模型（N 张"（否则脚本会先取到 16）；`02`【v1 core】实际 = 23 | `actual(23) ≤ declared(23)` |

**预期结果与相邻不变量**：

- 落地本 ADR（§4 的文档改动 + 附录 A/D 的分组）后：**I1/I4/I5 全绿**。
- **I3** 在按附录 C/D 落地后**同样转绿**：`inheritance`/`dependencies` 两个旧名不作为被解析的表名出现（`02` 全局名集里不再有与 `inheritance_edges`/`dependency_versions` 成对的裸名）。当前 I3 失败正是 `02` §8 把这两个旧名当条目列出所致。
- **I2 仍红**：`04` §4.3 还有 15 处 DDL；这是 #19 的范畴，而其触发条件（"I5 裁决后"）正是本 ADR——Accept 后可立即执行。
- 本 ADR 起草期间**不修改** `02`/`04`/`supplement/*`/`06`；本节条件是给落地执行者的验收单，不是起草时的既成事实。

### 8.1 落地记录（2026-09-29）

- **状态**：owner 授权代改，`Proposed` → `Accepted`；§4 文档改动 + 附录 A/D 分组 + #19 引用化已同日落盘。
- **落地改动**：`04` §0.3（声明 → ≤23）、`04` §4.3 标题（"P0/P1 最小数据模型（16 张；v1 core 的子集）"）与 15 处 DDL 引用化、`README.md` §3、`02` §8 三分组（顶部旧注记删除）。
- **检查器复跑（2026-09-29）**：`cd backend; go run scripts/check_knowledge_doc_invariants.go` = **5/5 PASS**——I1 = 23 vs 23（22 表 + `symbol_fts`）；I2 = 0 越位 DDL；I3 = 0 漂移；I4 = 三分组（23 + 1 + 2 名）；I5 = 声明 23 = 实际 23。
- **#19 例外映射（无 `02` DDL 的三项）**：`schema_migrations` → `internal/migrate`（§4.1 裁决；原 DDL 与已落盘实现冲突）；`symbol_aliases` → `supplement/01` §1.3（extension 事实源）；FTS 虚表 → `02` §73 的规范名 `symbol_fts`（旧稿名 `symbols_fts` 及列集差异的收敛见 §10）。
- **行号提示**：附录 A/B 的行号为改写前快照；`02` 三分组后行号有位移，定位以章节号为准。

---

## 9. Alternatives Rejected (and why)

| 选项/做法 | 否决理由（一句话） |
|---|---|
| A（维持 16、把超出表移出 core） | 让数字反向裁剪模型，把已被引用的 core 表搬出唯一事实源，制造新的幽灵引用（ADR-0007"不为不存在的名字建表"的对偶错误） |
| B（只把数字改成 23） | 治标：定义域仍悬空，extension↔core 下一次流动就复发；检查器绿了而语义仍无主 |
| 只改检查器（把 I5 的声明源/比较对象改掉） | 检查器不应承载裁决；文档才是口径事实源；且掩盖"16 与 23 在数不同集合"的真问题 |
| 把 `inheritance`/`dependencies` 加进 I3 allowlist | 二者不是"同一表族的合理对"，而是"旧名已被取代"；allowlist 会永久豁免未来的真实回归 |
| 让检查器解析散文状态（识别"已删除"）来做 I3 | 让机械检查依赖自然语言理解；用 `——` 后注记或删除线即可零成本满足 |
| 把 `02` 的全部 extension 表并进 v1 数字（或把 `04` DDL 全搬进 `02`） | 破坏 core/extension 分层（ADR-0007 已否决同类方案 E） |

---

## 10. Open Follow-ups

> **落地记录（2026-09-29）**：第 1/2/3 项已随本 ADR 落地并复跑验证（检查器 **5/5 PASS**，见 §8.1）；第 4/5/6 项仍为 Open。

| 项 | Gate |
|---|---|
| ✅ **已执行（2026-09-29）**：`04` §0.3（→23）、§4.3 标题（子集表述）、`README.md` §3 的落地改写；`02` §8 三分组（附录 D）与顶部旧注记删除 | `Phase1-start`（已满足） |
| ✅ **已执行（2026-09-29）**：#19：`04` §4.3 其余 15 处 DDL 引用化（I2 转绿；例外映射见 §8.1） | 本 ADR Accept 后（已满足） |
| ✅ **已验证（2026-09-29）**：I3 两对漂移的最终消解确认（附录 C）与 allowlist 维护（现含 `files/file_versions`、`symbols/symbol_versions`） | `Phase1-start`（复跑 I3 = 0 漂移） |
| `references`（`02`）vs `refs`（`04`/实现）的规范名与实现对齐；`symbol_fts`（`02` L2185/`supplement/13`）vs `symbols_fts`（`04`/实现）的收敛 | `Phase1-start`（`06` L125 已登记；两对都不在 I3 现模式 `{X, X_edges}/{X, X_versions}` 内，需单点裁决） |
| extension 组第二批名单（`symbol_aliases`、`projects`、`modules`、`project_languages`、`dependency_packages`）与 ADR-0001 的 `supplement/02` 同步（现仍是 `packages`/标量 `language` 的旧形态） | `Phase1-start` |
| 是否把 I5 声明源收敛到单处（`04` 内不再有两套数字）／扩展 I3 模式覆盖 `_outbox`、`refs/references` 等 | `Phase2-start` |

---

## 附录 A：28 名三分组建议表（#2/#20）

> 计数：**core 22 名 + `symbol_fts` 补列 = 23；extension 1 名（`index_jobs`）；deferred 5 名**。依据列给出 DDL 所在文件与行号（当前版本）或归属裁决。

| # | 名字 | 建议分组 | 依据 |
|---|---|---|---|
| 1 | workspaces | 【v1 core】 | `02` L346 |
| 2 | repositories | 【v1 core】 | `02` L360 |
| 3 | branches | 【已推迟】 | 无 DDL；ADR-0007 §4.1 删除（`repositories.current_branch` + `commits.parent_hash` 已表达） |
| 4 | commits | 【v1 core】 | `02` L385 |
| 5 | files | 【v1 core】 | `02` L407 |
| 6 | file_versions | 【v1 core】 | `02` L478 |
| 7 | symbols | 【v1 core】 | `02` L505 |
| 8 | symbol_versions | 【v1 core】 | `02` L593 |
| 9 | references | 【v1 core】 | `02` L622（注：`04`/实现用 `refs`，见 §10） |
| 10 | imports | 【v1 core】 | `02` L710 |
| 11 | calls | 【v1 core】 | `02` L666 |
| 12 | inheritance | 【已推迟】 | 无 DDL；规范名 `inheritance_edges`（`supplement/03` §3.2 L41；`04` L564 推迟 v2+） |
| 13 | dependencies | 【已推迟】 | 无 DDL；规范名 `dependency_versions`（`supplement/02` §2.2 L82；`04` L564 推迟 v2+） |
| 14 | exploration_sessions | 【v1 core】 | `02` L742 |
| 15 | exploration_nodes | 【v1 core】 | `02` L763 |
| 16 | exploration_edges | 【v1 core】 | `02` L806 |
| 17 | tasks | 【v1 core】 | `02` L853 |
| 18 | task_files | 【v1 core】 | `02` L883 |
| 19 | task_symbols | 【v1 core】 | `02` L906 |
| 20 | context_snapshots | 【v1 core】 | `02` L931 |
| 21 | context_items | 【v1 core】 | `02` L955 |
| 22 | tool_calls | 【v1 core】 | `02` L983 |
| 23 | tool_results | 【v1 core】 | `02` L1011 |
| 24 | cache_entries | 【v1 core】 | `02` L1037 |
| 25 | invalidation_events | 【v1 core】 | `02` L1065 |
| 26 | language_projects | 【已推迟】 | 无 DDL；ADR-0001 §4.7 删除，由 `project_languages`（extension，待落 `supplement/02`）取代 |
| 27 | index_jobs | 【extension】 | `supplement/15` §15.3 L83；ADR-0007 §4.1 归 extension；Phase 1 需要（调度/进度/重试） |
| 28 | events | 【已推迟】 | 无 DDL；ADR-0001 §4.7 删除；`04` L564：v2+ 由 `events_outbox`+`consumer_offsets`（`supplement/06` §6.3 L66/L82）取代；v1 用 `invalidation_events` |
| +1 | `symbol_fts` (virtual) | 【v1 core】 | `02` L2185；`06` §9 #2 要求补列（I1 必需） |

---

## 附录 B：6 幽灵名去向（ADR-0007 §4.1 的落地明细）

| 幽灵名 | 处置 | 去向 / 规范名 | 依据 |
|---|---|---|---|
| `branches` | 删除 | 不建表；能力由 `repositories.current_branch` + `commits.parent_hash` 表达 | ADR-0007 §4.1 |
| `inheritance` | 删除并改名指向 | `inheritance_edges`（extension；`04` L564 推迟 v2+） | ADR-0007 §4.1；`supplement/03` §3.2 L41 |
| `dependencies` | 删除并改名指向 | `dependency_versions`（extension；`04` L564 推迟 v2+） | ADR-0007 §4.1；`supplement/02` §2.2 L82 |
| `language_projects` | 删除 | 由 `project_languages`（+ `projects`/`modules`）取代，DDL 落 `supplement/02`（ADR-0001 后待补） | ADR-0001 §4.2/§4.7 |
| `index_jobs` | 保留，归 extension | DDL 已迁 `supplement/15` §15.3；v1 需要 | ADR-0007 §4.1/§4.3 |
| `events` | 删除 | v2+ 由 `events_outbox` + `consumer_offsets` 取代；v1 用 `invalidation_events` | ADR-0001 §4.7；`supplement/06` §6.3 L66/L82；`04` L564 |

---

## 附录 C：I3 两对漂移的规范名与依据

| 漂移对 | 规范名 | 依据 |
|---|---|---|
| {inheritance, inheritance_edges} | **`inheritance_edges`** | 唯一 DDL 在 `supplement/03` §3.2 L41（extension 事实源）；`inheritance` 无 DDL、是漂移裸名（ADR-0007 §1.1 证据 2）；`04` L564 已推迟类型系统 |
| {dependencies, dependency_versions} | **`dependency_versions`** | 唯一 DDL 在 `supplement/02` §2.2 L82；`dependencies` 无 DDL（ADR-0007 §4.1 同判）；ADR-0001 §4.4 保留 `dependency_packages`/`dependency_versions` 语义（拆分是 Phase 4 follow-up）；`04` L564 推迟依赖模型 |

统一原则：**以带 DDL 的 extension 事实源（`supplement/*`）名字为规范名；无 DDL 的裸名一律不进入模型名集**。落地注意：为同时满足 I3 与 I4，`02` §8 的 deferred 组中这两个旧名不得作为被解析的表名出现——写成 `~~inheritance~~`/`~~dependencies~~`，或以 `——` 之后注记引用即可（解析器只取 `——` 前的裸标识符）。

---

## 附录 D：建议的 `02` §8 三分组骨架（供 #20 执行）

> 仅为骨架示例；组头文字不会被解析成名字。落地后的硬约束只有两条：core 集合与 `02` DDL 逐名相等；全部名字属于且仅属于一个组。

```text
【v1 core】——本文件随后给出 DDL（22 表 + 1 虚表，共 23 名）
  workspaces, repositories, commits, files, file_versions,
  symbols, symbol_versions, references, calls, imports,
  exploration_sessions, exploration_nodes, exploration_edges,
  tasks, task_files, task_symbols,
  context_snapshots, context_items,
  tool_calls, tool_results, cache_entries, invalidation_events,
  symbol_fts (virtual)

【extension】——DDL 在 supplement/*，不在本文件；不计入 core 上限
  index_jobs
  （第二批待与 ADR-0001 落地同步：symbol_aliases、projects、modules、project_languages、dependency_packages——见 §10）

【deferred】——不在 v1（04 §4.3 推迟清单）；`——` 后为处置注记，不进入名字集
  inheritance_edges —— 规范名；旧名 ~~inheritance~~ 已删除（supplement/03 §3.2；v2+）
  dependency_versions —— 规范名；旧名 ~~dependencies~~ 已删除（supplement/02 §2.2；v2+）
  ~~branches~~ —— 已删除（repositories.current_branch + commits.parent_hash 取代）
  ~~language_projects~~ —— 已删除（ADR-0001：由 project_languages 取代，见上组）
  ~~events~~ —— 已删除（v2+ 由 events_outbox + consumer_offsets 取代；v1 用 invalidation_events）
```
