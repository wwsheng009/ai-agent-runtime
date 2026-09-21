# 术语表（GLOSSARY）

> 本文件是 `docs/knowledge_Layer/` 的**术语唯一事实源**（见 [`README.md`](README.md) §3）。
> 其他文档必须使用本表的规范名；出现同义词时以本表为准。
>
> **建立于 2026-09-20。** 覆盖范围：现有文档中**已被定义或反复使用**的术语。
> 尚未覆盖的术语请在引用处先补定义，再回填本表。
> 每条术语给出"事实源"列——定义以该处为准，本表只做索引与归一。

---

## 1. 文档与决策

| 术语 | 规范名 | 定义 | 事实源 |
|---|---|---|---|
| 架构意图 | Architecture Intent | 原则、分层、目标、非目标；**不含** schema 与阈值 | `01` |
| core schema | Core Schema | `knowledge.db` 的基础表与 DDL | `02` |
| extension schema | Extension Schema | 在 core 之上追加/修改的表；必须以 `ALTER` 显式应用 | `03` / `supplement/*` |
| 落地计划与验收 | Delivery Plan | Phase 划分、验收指标、风险登记 | `04` |
| 决策记录 | ADR | 一个决策的上下文、选项、结论、代价、回退路径 | `adr/*.md` |
| 变更历史 | Changelog | 文档与决策的变更记录 | `CHANGELOG.md` |
| "第 5 份并列设计文档" | — | **反模式**：在本目录新增与 `01`/`02`/`03`/`04` 并列的设计文档。新内容应归入 `supplement/` 或 `adr/` | `README.md` §7 |
| 单一事实源 | Single Source of Truth | 某类内容只允许有一处可写；其他文档只引用 | `README.md` §3 |

### 1.1 ADR 字段

| 字段 | 取值 | 含义 |
|---|---|---|
| `Status` | `Proposed` / `Accepted` / `Rejected` / `Superseded by ADR-YYYY` / `Deprecated` | **只有项目 owner 能把 `Proposed` 改为 `Accepted`**；`Accepted` 之后正文不可改 |
| `Reversibility` | `cheap` / `moderate` / `expensive` | 回退成本分级。`cheap` 现在决定；`expensive` 只在**无部署数据**时允许现在决定 |
| `Gate` | `Phase0-baseline` / `Phase1-shadow` / `Phase1-start` / `PhaseN-start` / `none` | 该 ADR 必须在哪个时点之前被 Accept，防止决策永久悬空。**阈值类 Gate 必须可达成**：需 shadow 对比数据的阈值用 `Phase1-shadow`，不能用 `Phase0-baseline`（2026-09-21 增补） |
| `Supersedes` | ADR 号或文档锚点 | 本 ADR 取代的对象 |
| `Deciders` | 角色 | 有权 Accept 的人；当前为"项目 owner" |
| Decided-by gate | — | 即 `Gate` 字段的机制名 | `adr/README.md` §4 |

---

## 2. 运行形态与配置

| 术语 | 定义 | 事实源 |
|---|---|---|
| knowledge 层 / 代码知识层 | 进程内代码索引与检索库；**不是**独立服务、不新增端口 | `README.md` §3、`04` 附录 B B8 |
| `knowledge.mode` | `off` / `shadow` / `on`。**`off` 时不得改变任何现有行为** | `README.md` §7、`supplement/05` §2.4 |
| shadow 模式 | 索引照常构建，但**不向模型暴露** `code.*`；只采集差异率用于校准 | `supplement/05` §1.4、ADR-0003 |
| owner | 单写者仲裁者；`owner.json` + 心跳 + PID 校验 | `supplement/05` §1.3、`04` §4.7 |
| reader / writer | writer（owner）可写；reader 以只读方式打开，读到的是**上次 writer 提交的快照** | `supplement/05` §1.3 |
| staleness | reader 快照与当前时间的差；以 `staleness_seconds` 暴露给模型 | ADR-0004 §4.2 |
| `snapshot_ts` | 快照对应的提交时间戳 | ADR-0004 §4.2 |
| `knowledge.lsp.mode` | `off` / `self`。**v1 只有这两个值**（`external` 未实现） | ADR-0002 §4 |
| select 配置项 | ACP `session/set_config_option` 中**不受能力门控**的选项类型 | ADR-0002 证据 3 |
| boolean 配置项 | 受 `clientCapabilities.session.configOptions.boolean` 门控的选项类型（无该能力则被过滤） | ADR-0002 证据 3 |
| `external_preferred` | **已废弃**。原 `supplement/05` §2.3 的 LSP 默认策略；ACP v1 无 LSP 能力位，**无法探测** | ADR-0002 |
| 逃生舱 | 关闭知识层的配置开关（`knowledge.mode=off`、`knowledge.tools.enabled=false` 等） | ADR-0004 §4.4 |

---

## 3. 项目模型

| 术语 | 定义 | 事实源 |
|---|---|---|
| Workspace | 用户打开 / ACP root 的目录 | `supplement/05` §3.1 |
| Project | 可独立构建 / 依赖管理的**最小单元** | `supplement/05` §3.1、ADR-0001 |
| Module | Project 内部的可选边界（`go.work` 的 module、pnpm workspace 的 package） | `supplement/05` §3.1 |
| multi-kind project | 同一 root 同时是多种 kind（如 `go-module` + `node-package`） | ADR-0001 D2 |
| `project_languages` | Project 的语言集合；**取代** `02` 的幽灵表 `language_projects` | ADR-0001 §4.2 |
| `project_evidence` | 语言/类型断言的逐条证据，用于解释 confidence | `supplement/05` §3.1 |
| confidence | 对某断言的置信度；必须有公式与证据，不得只给示例值 | `04` §4.4、`04` 附录 B B14 |
| stable_key | 符号的稳定标识（`H(rel_path + kind)` 类）；跨重命名保持 | `03` §1.2、`04` §4.4 |

---

## 4. 检索与工具面

| 术语 | 定义 | 事实源 |
|---|---|---|
| `code.*` | 知识层暴露给模型的工具族（`code.search` / `code.find_symbol` / `code.find_refs` / `code.callers` / `code.impact`） | `04` §4.6 |
| 定义类查询 | `code.find_symbol` / `code.search`。陈旧时失败**显式**（模型会回退 `grep`） | ADR-0004 §1.2 |
| 关系类查询 | `code.find_refs` / `code.callers` / `code.impact`。陈旧时失败**静默且不可逆** | ADR-0004 §1.2 |
| `S_fresh` / `S_max` | 陈旧度分档边界（**初始值** 60s / 15min，须由 Phase 0 校准） | ADR-0004 §4.1 |
| `completeness` | `full` / `partial` / `fallback`，随每个 `code.*` 结果返回 | ADR-0004 §4.2 |
| shadow 差异率 | 被拦截的 `grep`/`view` 调用与索引侧候选之间的差异度量 | ADR-0003 |
| baseline / candidate | `G`（被拦截调用实际返回的条目）/ `K`（索引侧候选条目） | ADR-0003 §4.1 |
| `coverage` | `overlap_n / baseline_n` | ADR-0003 §4.2 |
| `economy` | `candidate_tokens / baseline_tokens` | ADR-0003 §4.2 |
| `usable` | `coverage >= α AND economy <= 1.0` | ADR-0003 §4.2 |
| α | coverage 门槛。**由 Phase 0 产出**，不得现在写死 | ADR-0003 §4.2、`04` §7.6 |
| M1–M4 | 调用级可用率 / 覆盖度 / 经济性 / token 收益 | ADR-0003 §4.5 |
| 零结果调用 | `baseline_n == 0` 的被拦截调用；**不计入** M1–M3 分母，单独计数 | ADR-0003 §4.5 |
| 索引不可用降级 | 知识层不可用（`mode=off`）时回到 `grep`/`view` | `04` 附录 B |
| 能力受限降级 | 知识层**可用**但某项能力不可用（如陈旧 reader 下关系类查询不注册） | `04` 附录 B、ADR-0004 |

---

## 5. 进程与协议

| 术语 | 定义 | 事实源 |
|---|---|---|
| Job Object | Windows 内核对象，可容纳进程树；`TerminateJobObject` 一次收全树 | ADR-0005 |
| `KILL_ON_JOB_CLOSE` | `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`。宿主进程消失时由**内核**收树，不依赖用户态代码运行 | ADR-0005 §4.2 |
| `processGuard` | `internal/executor` 的进程树守卫：Job Object 为主，`taskkill /T` 为降级 | ADR-0005 §4.1 |
| `treeGuard` | `internal/mcp/transport` 的守卫，语义是"随连接生死"，**不适合**长驻 LSP | ADR-0005 §5 |
| `rep.Mode` | 收树模式：`job` / `taskkill` / `direct` | ADR-0005 §4.3 |
| detach | 故意脱离 Job Object / 进程组的长寿进程语义 | `internal/executor/detach.go`、ADR-0005 §4.4 |
| `positionEncoding` | LSP `initialize` 协商的位置编码。**未协商时协议默认 `utf-16`**（是默认值，不是常量） | ADR-0006 §4.2 |
| canonical position | 内部唯一位置表示：0-based 行 + 行内 **UTF-8 字节**列 + 半开区间 `[start, end)` | ADR-0006 §4.1 |
| `document_version` | 文档版本；结果版本不匹配则**丢弃** | ADR-0006 §4.6 |

---

## 6. Schema 治理

| 术语 | 定义 | 事实源 |
|---|---|---|
| 幽灵表 | 出现在总览清单中但**没有 DDL** 的表名 | ADR-0007 §1.2 |
| 命名漂移 | 同一概念在不同文档中用了不同表名（如 `inheritance` vs `03.inheritance_edges`） | ADR-0007 §1.2 |
| DDL 越位 | DDL 出现在 `02`/`03` 之外（如 `04`） | ADR-0007 §1.2 |
| 文档不变量 I1–I5 | 总览↔DDL 集合相等 / DDL 落点 / 无命名漂移 / 分组互斥完备 / 表数自洽 | ADR-0007 §4.4 |
| 三层分组 | `02` §8 总览必须分为 **【v1 core】/【extension】/【deferred】**，禁止混列 | ADR-0007 §4.2 |

---

## 7. 仓库内的相邻概念（易混用，明确边界）

| 术语 | 归属 | 与知识层的关系 | 事实源 |
|---|---|---|---|
| `memorystore` | 长期笔记（人工维护） | 三层记忆之一 | `README.md` §3 |
| `factledger` | 事实账本 | 三层记忆之一；权威性高于压缩后的散文 | `README.md` §3 |
| exploration memory | 任务工作集（自动维护） | 三层记忆之一 | `README.md` §3 |
| `contextmgr` / `contextpack` | 上下文管理 | 知识层**扩展**它们，不新建第二套上下文系统 | `04` 附录 B B11 |
| `internal/events` / `runtimeevents` | 事件总线 | **复用**，不新建 outbox（`events_outbox` 推迟） | `04` 附录 B B18 |
| `internal/artifact` | 工具输出归档 | 复用；知识层的表只存元数据 | `04` 附录 B |
| `winconsole` | Windows UTF-8 代码页工具 | **与进程树生命周期无关**（`supplement/05` §9.5 曾把两者捆在一个问题里） | ADR-0005 §4.5 |
| `internal/policy` / `fsscope` | 权限与路径范围 | `code.*` 走既有 capability，不新建权限模型 | `supplement/05` §2.3 |
| `internal/goal` | 目标 / turn | `tasks` 表映射到 goal/turn，不新建平行概念 | `04` 附录 B B13 |
