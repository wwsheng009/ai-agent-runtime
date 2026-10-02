# LSP 在 Agent Runtime 中的接入与使用（文档索引）

> 最后更新：2026-09-21
> 定位：**参考实现分析 + 落地实施方案**，服务于 `docs/knowledge_Layer` 已评审的 LSP 规格。
> 事实源边界：本目录 **不定义** core schema、不定义表结构、不新增第 5 份并列设计文档。

---

## 1. 为什么有这个目录

`docs/knowledge_Layer` 已经把 LSP 相关的**规格与决策**写完了：

| 事实源 | 内容 | 位置 |
| --- | --- | --- |
| `supplement/05_runtime_integration_project_detection_and_lsp.md` §10 | LSP 工程化规格（生命周期、能力协商、位置编码、诊断表列） | §10.1（原 `03` §5.1）、§10.2（原 §5.2）、§10.3（原 §5.3）；原写 `03` §5.4 的坐标实为 `03` §6.4（见 `supplement/06_cache_consistency.md` §6.4） |
| `adr/0002-acp-lsp-ownership.md` | ACP 下 LSP 归属与能力面（谁 spawn、谁拥有、能力如何声明） | ADR-0002，Gate `Phase4-start` |
| `adr/0006-lsp-position-encoding-boundary.md` | 位置编码转换边界与缓存键 | ADR-0006，Gate `Phase4-start` |
| `supplement/05_runtime_integration_project_detection_and_lsp.md` | 知识层 × Runtime 集成、项目类型感知与 LSP 接入 | supplement/05 |

这些文档回答的是 **"应该是什么"（规范）**。它们不回答 **"具体怎么写出来"（工程实现）**，也没有一份可对照的**参考实现**。

本目录补齐这一段：

- 第 1 份：分析 **crush** 项目（一个已经把 LSP 深度接进 AI agent 的 Go 实现）**具体怎么做的**，给出可复用的模式。
- 第 2 份：把这些模式**映射**到 ai-agent-runtime 的既有架构与 knowledge_Layer 规格上，给出分层设计。
- 第 3 份：拆成可执行任务，给出验收条件，并对齐 `04_completeness_review_and_optimized_plan.md`。

---

## 2. 目录清单

| 文件 | 内容 | 读者 |
| --- | --- | --- |
| [`01-crush-lsp-usage-analysis.md`](./01-crush-lsp-usage-analysis.md) | crush 的 LSP 接入全貌：进程生命周期、能力协商、Agent 工具面（diagnostics / edit / references / view / write / lsp_restart）、提示词注入、诊断回灌闭环 | 想了解参考实现的工程师 |
| [`02-runtime-lsp-integration-design.md`](./02-runtime-lsp-integration-design.md) | 在 ai-agent-runtime 中落地同类能力的分层设计：LSP Manager、会话归属、工具面、边界转换、缓存、可观测性 | 架构 / 实现负责人 |
| [`03-implementation-plan-and-acceptance.md`](./03-implementation-plan-and-acceptance.md) | 任务拆分、依赖顺序、验收清单、与 knowledge_Layer `04` 的对齐与回灌点 | 排期 / 验收 |

---

## 3. 与 knowledge_Layer 的边界（必须先读）

本目录**遵守** `docs/knowledge_Layer/README.md` §7 的约束，并显式声明以下边界：

1. **不新增第 5 份并列设计文档。**
   本目录不是并列设计文档，是**下游参考实现分析与落地方案**。任何与规格冲突的内容，一律以 `knowledge_Layer` 为准，并通过 ADR 或 `supplement/*` 回灌，不在本目录"私自定稿"。

2. **不复制 DDL。**
   本目录不出现 `CREATE TABLE`。涉及 `lsp_diagnostics` / `lsp_servers` 等列与语义时，**只引用** `supplement/05_runtime_integration_project_detection_and_lsp.md` §10.2 与 ADR-0006 的结论，不复写定义。

3. **不写无基线的硬编码阈值。**
   本文中出现的所有数值型参数（超时、并发上限、缓存条目数等）都标注为**待基线标定**，并给出标定方法，不直接当成结论。

4. **归属问题以 ADR-0002 为准。**
   "LSP 进程由谁拥有 / 由谁 spawn / 能力如何声明" 这类问题，本文只做**实现层面的展开**，不做与 ADR-0002 相反的设计。ADR-0002 状态为 Proposed、Gate 为 `Phase4-start`，因此本文的实现工作同样**不得早于 Phase 4**。

5. **位置编码问题以 ADR-0006 为准。**
   "在哪个边界做 UTF-16 ↔ 字节偏移转换、缓存键怎么构成" 以 ADR-0006 的结论（含规则 "4.4 转换只发生在边界 (D3)"）为准，本文只描述该规则在代码里的落点。

---

## 4. 状态与前置条件

| 项 | 状态 |
| --- | --- |
| knowledge_Layer 评审 | 已评审完成 |
| Phase 0 | **未开始** |
| ADR-0002 / ADR-0006 | Proposed，Gate = `Phase4-start` |
| 本目录文档 | 已完成（参考分析 + 实施方案） |
| 代码实现（runtime 侧 W1–W7） | 已落地：`backend/internal/lsp/`（编码边界 / 传输 / 客户端池 / 归属路由 / 内联渲染）+ `backend/internal/tools/lsp_bridge.go`（`lsp_servers` / `lsp_diagnostics` / 编辑结果尾部追加）；配置入口 `lsp.*`（默认 `enabled=false`） |
| TUI 用户侧入口（`/lsp` 命令族） | 已落地：`backend/cmd/aicli/commands/chat_lsp_command.go`（`status`/`list`/`servers`/`diagnostics <file>`/`restart [name]`/`start [name]`），复用 `internal/tools` 的 LSP 管理面（状态/诊断/手动启停）；只读长文档按 alt-screen 框架 §5.1 进备用屏 `ScreenDocument`（`lsp.status`/`lsp.diagnostics`，超内联预算且能力满足时才开屏，短输出/无副屏降级内联），忙时走副屏通道（S），`restart`/`start` 忙时排队到回合结束后 |
| **语义通道（code_* 侧）** | 已落地（2026-10-01 收口轮十/十一）：`backend/internal/knowledge/lsp/`（进程 + ADR-0002 §4.4 锁 + `Status()` 观测）+ `backend/internal/knowledge/adapter_lsp*.go`（definition / references / **documentSymbol** / **workspace/symbol**）。能力面接到 `code_navigate`（按位置查定义 + **members**）、`code_references` / `code_callers` / `code_navigate refs`、`code_search`（`workspace/symbol` 同名消歧，`source=index+lsp`）。配置入口 **`knowledge.lsp.*`**（默认 `enabled=false` / `mode=off`），与 `lsp.*` 是两处独立开关。**单实例（收口轮十一）**：诊断池启用时语义通道**借用**池的 gopls 进程（不 spawn / 不持锁 / 不关停），同一种语言在一个进程里只有一个 server 实例——判据是内存（gopls 常驻数百 MB，历史崩溃根因即整机内存耗尽），决策与代价见 `docs/knowledge_Layer/adr/0002-acp-lsp-ownership.md` §4.4.1；`/lsp restart gopls` 会同时影响两条链路。诊断池关闭时语义通道自建 + 持锁（跨进程去重仍由 §4.4 的锁负责）。模块根支持从 workspace 根向下最多 3 层探测 `go.mod`（monorepo 场景），同深度多模块时降级。观测：`lsp_servers` 的语义通道分节 + `/lsp semantic` 子命令（标注 `shared=诊断池`，每个稳定 reason token 均有可行动提示）。详见 `docs/knowledge_Layer/CHANGELOG.md` 收口轮（十）（十一） |
| 观测优化轮（2026-10-01，真实基线驱动） | 已落地：空诊断确认窗口与冷启动短等待、重启预算滑动窗口 + 崩溃 stderr 归因、降级提示去重、`tool_call_id`/`turn_id`/指纹埋点补齐、fallback 分母与追加字节拆分、文档 LRU+didClose、增量 didChange、多 server 独立预算、rune 预算与空块精简、诊断事件去重、closure 率可计算；**后续轮**：降级原因细分类（缺二进制/崩溃/传输关闭/取消）、空发布假 clean 护栏、路径级冷路径快速失败（热连接上的冷路径不再付满预算）、`first_publish_ms` 观测+基线行+状态面展示、崩溃预算耗尽的结构化原因（分类与 note 可行动）。记录见 `docs/plan/lsp-observability-and-analysis-plan-20260929.md` §5.8 |
| W8 持久化 | 未动 schema：`lsp_servers` / `lsp_diagnostics` 表属 `knowledge_Layer`，随 `Phase4-start` 建表后接入（本目录不加表/列） |

> 验收映射（本次实现）：A1/A2/A3/A5/A7/A8/A10/A11 已有自动化用例，入口 `go test ./internal/lsp/... ./internal/tools/...`；
> A4 受上游建表 gating（无新表/新列）；A6/A9 的默认值与多 server 优先级裁定需真实 server 基线与规格回流（§7），尚未在代码中固化为结论。
> 真机冒烟：`TestRealRustAnalyzerRoundTrip`（rust-analyzer；未安装或 `-short` 时自动跳过）覆盖 spawn → initialize 握手 → didOpen/didChange/didSave → 诊断等待 → 追加式渲染全链路。

**结论：本目录文档可以先行交付与评审，但其描述的代码实现必须以 `Phase4-start` 为最早起点。**
在 Phase 0–3 期间，本文档的唯一用途是：让后续实现者不必重新做一遍调研。

---

## 5. 阅读路径建议

- **只关心"crush 怎么做的"** → 直接读 `01-crush-lsp-usage-analysis.md`。
- **要排期做实现** → 读 `02-runtime-lsp-integration-design.md` §2（分层）+ `03-implementation-plan-and-acceptance.md`。
- **要评审是否越界** → 读本文件 §3，再核对 `adr/0002`、`adr/0006`。
