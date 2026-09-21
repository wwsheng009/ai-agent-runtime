# 多 Agent 持久化生命周期与结果取回加固方案

更新时间: 2026-09-20
状态: proposed（待评审；本文件只记录取证结论与方案，未修改任何代码）
适用仓库: `E:\projects\ai\ai-agent-runtime`
证据基线: 2026-09-20 工作树 + 一次真实 background 批次端到端取证（`batch_42d0a52d2a24e401`）。引用行号会随代码漂移，实施前需按符号名复核。
审查方法: 一次真实 `spawn_subagents` background 批次（3 个 hard 只读分析任务）端到端取证 + 三份 durable 存储只读快照 + 只读代码走查（grep/view）+ 与 `docs/plan/` 既有 118 份方案的覆盖比对。

---

## 1. 文档定位

本文件聚焦 multi-agent 的**持久化生命周期完整性与结果取回可靠性**，即「子代理已经产出了东西之后，父代理能不能可靠地拿到、系统能不能诚实地记账」。

它与 `docs/plan/multi-agent-execution-optimization-plan.md`（下称「主计划」）的关系是**增量而非重复**：主计划管的是执行协作语义（等待、唤醒、审批、配额、回收、可观测性、协作引导），本文件管的是同一批机制**在持久层与取回层上的缺口**。凡是主计划已覆盖的条目，本文件只做差异标注，不重开方案。

本文件的结论全部来自一次**真实批次的第一手取证**，而非静态推演。这次取证的核心发现是：

> 批次终态报告 `failed`（completed=1 / failed=2），但两个「失败」任务的 `result_json.summary` 里**已经存有完整交付物**（6817 / 6113 字符）。失败原因是只读策略拒绝了复合 shell 命令——一个与任务目标无关的执行细节。父代理若只读状态、不读正文，会把已经完成的活判为失败并重派。

这条链路串起了本文件的三个问题轴：

1. **结果取回**（H1–H5）：正文取不出来、大输出没有分页、聚合没有去重。
2. **持久记录完整性**（H6–H10）：运行期批计数器撒谎、子会话绑定到终态才写、任务级超时/进度字段全空、失败判定掩盖已完成交付物。
3. **隔离与边界**（H11–H15）：worktree 孤儿泄漏、并发无全局上限、子代理可自提权限、apply 无冲突检测。

---

## 2. 相关文档

| 文档 | 与本文件的关系 |
| --- | --- |
| `docs/plan/multi-agent-execution-optimization-plan.md` | 主计划（N1–N11 / P0–P2）；本文件的 H6–H10 与其 N10、P1-5、P2-9 有部分交叠，见 §6 |
| `docs/plan/subagent-readonly-boundary-transparency-plan-20260917.md` | 只读边界可读化（M3/M4/M6，2026-09-17 已实施）；本文件 H10 是其**未闭环的剩余层** |
| `docs/plan/supervision-parent-child-control-optimization-plan-20260917.md` | `read_agent_result` 工具的定义来源（改动 2）与 worktree 守卫 P1-3；本文件 H1、H11 与其直接相关 |
| `docs/plan/spawn-subagents-async-supervisor-plan.md` | background 批次的原始设计（数据模型、生命周期、超时恢复）；本文件 H6–H9 是其数据模型的落地偏差 |
| `docs/plan/tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md` | 大输出归档与分页既有方案；本文件 H4 标注其未覆盖面 |
| `docs/plan/sse-live-event-channel-optimization-plan.md` | 事件字节分布与增量双写去重；本文件 H2 的上游 |
| `docs/plan/runtime-store-append-hotpath-optimization-plan-20260918.md` | 事件裁剪（2048/256）与写热路径；本文件 H2 的保留策略来源 |
| `docs/plan/llm-tool-authorization-policy-convergence-plan.md` | 权限模式基础语义；本文件 H13 的权限天花板缺口不在其范围 |
| `docs/plan/multi-agent-production-readiness-plan.md` | 生产就绪门禁；本文件 H11 的 worktree 孤儿不在其「工作树」语义内 |
| `docs/plan/multi-agent-real-terminal-validation-runbook.md` | 真实验证 runbook；§8 的验收脚本建议在此扩展 |

---

## 3. 事实基线与取证方法

### 3.1 取证批次

| 项 | 值 |
| --- | --- |
| batch_id | `batch_42d0a52d2a24e401` |
| 派发方式 | `spawn_subagents`（background 模式） |
| 任务数 / 难度 | 3 个 / 全部 hard，全部 `read_only=true` |
| 终态 | `failed`（completed=1 / failed=2），elapsed 230047 ms |
| 失败分类 | `error_class='policy'`，错误串 `read-only policy blocks compound shell command; submit one command per shell.commands entry` |
| 关键反讽 | 两个 failed 任务的 `result_json.summary` 分别含 **6817 / 6113 字符的完整交付物** |

三个子任务的原始报告已落盘留证（UTF-8，含精确 file:line）：

- `output/multiagent-review/opt-dispatch.md`（14,354 B）
- `output/multiagent-review/opt-isolation.md`（11,078 B）
- `output/multiagent-review/opt-result.md`（9,191 B）
- 汇总：`output/multiagent-review/OPTIMIZATION-SUGGESTIONS.md`（9,850 B）

### 3.2 三份 durable 存储快照（2026-09-20 只读查得）

| 存储 | 路径 | 规模 | 关键观察 |
| --- | --- | --- | --- |
| 批次库 | `~/.aicli/sessions/runtime/subagent_batches/session_20260920083941_wHAOpqH5.sqlite` | 表 `subagent_batches` / `subagent_tasks` | 运行期 `queued_count=3, running_count=0` 而三行 task 均为 `running`；`child_session_id` 终态才有值；`task_deadline` / `last_progress_at` 全 NULL |
| 会话运行时库 | `backend/data/runtime/session_runtime.sqlite` | **907 MB** | `session_events` 562,192 行 / 载荷 255.4 MB；`assistant.reasoning` 251,498 行/113.6 MB + `assistant_delta` 225,232 行/79.3 MB ≈ **75% 字节是流式增量** |
| Agent 控制库 | `~/.aicli/sessions/runtime/agent_control.sqlite` | **541.2 MB** | `agent_control_agent_wake_events` **1,239,083 行**（closed 1,127,304 / active 111,490 / stale 289），跨度 2026-08-22 → 09-20；而 `agent_control_agents` 仅 **924 行** |

### 3.3 运行期可观测性的实测结论

在批次运行期间（终态之前）对同一批次做的解析尝试全部失败：

| 尝试 | 结果 |
| --- | --- |
| `wait_agent(id=<task_id>)` | `status: missing, exists: false` |
| `read_agent_events(id=<task_id>)` | 返回 0 事件 |
| 直接读 durable 库 | 只有批级计数与 `running` 状态，无子会话绑定、无进度、无超时 |

即：**父代理在子代理运行期间，通过工具面和持久层都拿不到任何可解析的进度锚点**。这与主计划 P1-5「子 agent 进度可观测」已实施的四项（前端下钻、`read_agent_events` 过滤视图、父流节流镜像、前端 inline 审批）并不矛盾——那些解决的是**父会话已知子会话身份之后**的可视化，而本文件 H7 指出的是**运行期拿不到这个身份**。

### 3.4 关键参数

| 参数 | 默认值 | 证据 | 备注 |
| --- | --- | --- | --- |
| `agents.maxThreads` | 6（`0` 回退默认；`-1` 显式不限） | 主计划 §3.2 | 只管 spawn 闸门，见 H12 |
| `SubagentSchedulerConfig.MaxConcurrent` | 4 | `backend/internal/agent/scheduler.go:159-163` | **未从配置面透传**，见 H12 |
| `read_agent_result` summary 预算 | 512 runes | `backend/internal/supervision/agent_result.go:18,315` | 见 H1、H4 |
| 事件裁剪 | 2048 / 256 条 | `runtime-store-append-hotpath-optimization-plan-20260918.md:21,60` | 条数级，非字节级，见 H2 |
| 终态 agent 记录 GC | 30 天 / `AICLI_REGISTRY_RETENTION` | 主计划 §10（:842） | 不覆盖 active 行，见 H3 |

---

## 4. 问题清单（H1–H15）

编号采用 `H`（hardening）前缀，与主计划的 `N1–N11` 区分，避免交叉引用歧义。

| 编号 | 优先级 | 问题 | 归属优化项 | 覆盖审计 |
| --- | --- | --- | --- | --- |
| H1 | P0 | `read_agent_result` 按当前契约无法返回正文 | P0-1 | 部分覆盖 |
| H2 | P1 | `session_events` 无载荷级裁剪，单库 907 MB | P1-2 | 部分覆盖 |
| H3 | P1 | wake 事件表失控：1.24M 行 / 541 MB | P1-2 | 部分覆盖 |
| H4 | P1 | 大输出归档与分页能力不一致，父级摘要超预算整条丢弃 | P0-1 | 部分覆盖 |
| H5 | P2 | 聚合缺语义去重与冲突消解 | P2-1 | 未覆盖 |
| H6 | P1 | 运行期批计数器与任务行状态互相矛盾 | P1-1 | 部分覆盖 |
| H7 | P1 | `child_session_id` 到终态才写入 → 运行期无法解析子会话 | P1-1 | 部分覆盖 |
| H8 | P1 | 任务级 `task_deadline` / `last_progress_at` 全为 NULL | P1-1 | 部分覆盖 |
| H9 | P2 | 运行期 `result_summary` 存成空字节字面量 `b''` | P0-2 | 未覆盖 |
| H10 | P0 | 策略拒绝使**已完成**任务被判 failed，交付物被状态掩盖 | P0-2 | 部分覆盖 |
| H11 | P1 | worktree 孤儿目录泄漏（已实证） | P1-3 | 未覆盖 |
| H12 | P1 | 子代理并发无全局上限（4×N） | P1-4 | 部分覆盖 |
| H13 | P1 | 子代理请求更高权限会被接受 | P1-4 | 未覆盖 |
| H14 | P1 | worktree apply 无冲突检测 | P1-3 | 未覆盖 |
| H15 | P2 | 隔离层回收缺统一对账 | P1-3 | 部分覆盖 |

### 4.1 结果取回轴

#### H1（P0）：`read_agent_result` 按当前契约无法返回正文

- **实测**：`read_agent_result(id=..., sections=["summary"])` 只返回 `Structured output summary: kind=structured size=N`，**正文丢失**；换 `sections=["output"]` 直接报错 `unsupported section output (want summary|findings|changes|artifacts|errors|usage)`。
- **机制**：`read_agent_result` 被打上 `output_kind=structured`（`backend/internal/toolbroker/supervision_tools.go:698`、`cache_safe_summary.go:12-21`），但 `backend/internal/output/tool_result_content.go:379-390` 的**协作结果白名单漏了它**——同族的 `wait_agent` / `read_agent_events` 都在白名单内。section 词表见 `backend/internal/supervision/agent_result.go:183-188,215-247`；summary 被截断到 512 runes（`agent_result.go:18,315`）。
- **影响**：这是「获取子 agent 输出」这条主路径上的**硬阻断**。工具存在、工具返回成功、正文不在返回里——模型无法通过任何参数组合拿到子代理的实质产出。H10 的交付物因此更难被父代理发现。
- **既有覆盖**：`supervision-parent-child-control-optimization-plan-20260917.md:216` 定义该工具（P0）、`:358` 定义预算（snapshot ≤512 rune / 工具 ≤4000 字符）、`:452` 记录 M3 实施（有界 sections、source/next_action）。既有文档只定义了**有界契约**，未记录白名单漏项导致的**正文整体丢失**，无对应修复与回归条目。

#### H2（P1）：`session_events` 无载荷级裁剪

- **实测**：`backend/data/runtime/session_runtime.sqlite` 907 MB；`session_events` 562,192 行 / 载荷 255.4 MB；其中 `assistant.reasoning` 251,498 行/113.6 MB、`assistant_delta` 225,232 行/79.3 MB，合计约 **75% 字节是流式增量**。
- **既有覆盖**：`sse-live-event-channel-optimization-plan.md:68-86`（全库字节分布）、`:220-228`（增量只落一处，预估存储 −45~52%）；`runtime-store-append-hotpath-optimization-plan-20260918.md:21,60`（事件裁剪 2048/256）；`session-usage-analytics-and-agent-diagnostics-plan.md:155,302`（按条数裁剪 2048 条）。
- **差异**：既有方案已定位「增量双写是字节主因」并给出去重方案与**条数级** retention；但没有**载荷字节级**上限——条数裁剪挡不住单条大载荷；本轮 907 MB / 562,192 行的实测规模也未被任何文档复测确认。

#### H3（P1）：wake 事件表失控

- **实测**：`~/.aicli/sessions/runtime/agent_control.sqlite` 541.2 MB；`agent_control_agent_wake_events` 1,239,083 行（closed 1,127,304 / active 111,490 / stale 289），时间跨度 2026-08-22 → 2026-09-20；对照 `agent_control_agents` 仅 924 行——**事件行是实体行的 1341 倍**。
- **既有覆盖**：主计划 `:842` 已实施 `PurgeTerminalAgentRecords`（终态保留与 GC，默认 30 天 / `AICLI_REGISTRY_RETENTION`）、`:844`（无变化刷新不再追加 wake 事件）、`:847`（新增 `idx_agent_control_agent_wake_created_at`）；`spawn-subagents-async-supervisor-plan.md:397`（wake 用 idempotency key 去重）。
- **差异**：既有方案收敛了**写放大**并 GC **终态行**，但存量 1.13M closed 行仍在 30 天窗口内；**active 行的 wake 事件无上限**这一点在 `:842` 被承认却无方案；没有更短窗口 / 压缩 / 分区措施。

#### H4（P1）：大输出归档与分页能力不一致

- **机制**：`artifact_read` 具备 offset/limit/eof 分页（`backend/internal/toolbroker/artifact_read.go:138-230`）；`read_agent_result` **无分页**（`backend/internal/supervision/agent_result.go:352-358,395-406`），artifacts 段只列 ref、不说明可解引用；gateway 只归档大工具输出（`backend/internal/output/gateway.go:141-176`），**子代理最终文本默认不归档**；父级摘要超预算**整条丢弃**且无回退指针（`backend/internal/agent/subagent_parent_summary.go:29-60`）。
- **既有覆盖**：`tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md:14`（P1-1 Gateway 归档分级、`artifactArchiveMinBytes()`）、`:15,106-108`（P1-2 `artifact_read` 分页）、`:178-181,334-347`（回显层 12 KiB 预算 + 解引用指针）。
- **差异**：上述四处（`read_agent_result` 无分页、artifacts 段无解引用指引、子代理终文本不归档、父级摘要整条丢弃无回退指针）在该文档中**均无条目**。

#### H5（P2）：聚合缺语义去重与冲突消解

- **机制**：仅 `wait_agent` 有 JSON 形状级去重（`backend/internal/toolbroker/types.go:495`）；父级摘要为逐条拼接 + 预算截断 + `Omitted` 计数，**无跨子代理语义去重、无冲突消解、无稳定排序**。
- **既有覆盖**：最接近的只有投递层幂等（`spawn-subagents-async-supervisor-plan.md:397`、主计划 `:424-426` 的 wake claim 去重合并）。全 `docs/plan` 检索「冲突消解 / 语义去重 / 稳定排序」**无命中**。

### 4.2 持久记录完整性轴

> 数据来源：`~/.aicli/sessions/runtime/subagent_batches/session_20260920083941_wHAOpqH5.sqlite`，表 `subagent_batches` / `subagent_tasks`。

#### H6（P1）：运行期批计数器与任务行状态互相矛盾

- **实测**：运行中快照为 batch `status=running, queued_count=3, running_count=0`，但三行 `subagent_tasks.status` 全是 `running`。计数与明细**直接互相否定**。
- **附带**：`subagent_tasks.updated_at` 冻结在启动时刻，而批级 `updated_at` / `heartbeat_at` 持续推进——说明任务行在运行期**根本没有被回写**。
- **影响**：任何基于批计数的进度判断（父代理、CLI 面板、前端）在运行期都会显示「0 个在跑」。
- **既有覆盖**：`spawn-subagents-async-supervisor-plan.md:176`（§4.3 数据模型定义 `queued_count`/`running_count` 计数器列）；`supervision-business-supervision-implementation-plan.md:191`（§3.1 父侧 N/M 由 `ListTasks` + `subagentbatch.Counts(tasks)` 从 task 行派生——即设计意图本就是**以 task 行为准**）。
- **差异**：计数器只被定义为 batch 行上的**独立列**，没有「必须与 task 行一致 / 随状态转换同步更新 / 读侧一律以 task 行为准」的约束；§11.1 与 §14 均无计数一致性测试或验收项，运行期漂移无对应条目。

#### H7（P1）：`child_session_id` 到终态才写入

- **实测**：运行期三行 task 的 `child_session_id` 为空字符串；终态后才填入 `subagent_opt-dispatch_6bb17ea9c6c549b398f31414d93758eb` 这类值。
- **实测后果**：运行期用 task id 调 `wait_agent` → `status: missing, exists:false`；`read_agent_events` → 0 事件。父代理在子代理运行期间**完全看不到进度**。
- **影响**：这是「background 批次可监督」这一设计承诺的落地缺口。它解释了为什么运行期只能靠「等」，也说明主计划 P1-5 的四项可视化能力在 background 批次下**缺少可用的身份入口**。
- **既有覆盖**：`supervision-parent-child-control-optimization-plan-20260917.md:485`（§3 偏差 5：「`SubagentTaskRecord.ChildSessionID` 原无生产写入者：终态落结果时补一次 best-effort CAS」）、`:452`（M3 记录「结果写入补齐 ChildSessionID 列」）；对照 `supervision-parent-child-control-analysis-20260917.md:92,94`（R1-1、R1-3）。
- **差异**：既有方案把「**终态补写**」当作已闭环，未要求运行期即可由 task_id 解析子会话；也未定义 `wait_agent(task_id)` 运行期 `missing` / `read_agent_events` 0 events 的降级语义与验收（R1 只承诺「进度可见」，而进度写回出厂默认关闭）。

#### H8（P1）：任务级超时与进度字段全为 NULL

- **实测**：`subagent_tasks.task_deadline` 全为 NULL（而 `subagent_batches.batch_deadline` 有值）；`last_progress_at` 全为 NULL。
- **代码线索**：`backend/internal/agent/subagent_batch_coordinator.go` 存在 `taskProgressWritebackEnabled`（约 1213-1215 行），但实测未见落库效果。
- **影响**：单任务卡死无法被识别，也无法对「哪个任务拖慢了整批」做归因。
- **既有覆盖**：`spawn-subagents-async-supervisor-plan.md:192,194`（§4.3 定义 `task_deadline` / `last_progress_at`）、`:372-378`（§6.3 三层 deadline）；`supervision-parent-child-control-optimization-plan-20260917.md:481`（§3 偏差 1 **断言四列「写入正常」、仅读投影丢弃**——**与实测直接矛盾**）、`:450`（M1 进度写回）、`:495`（§4 默认值 `supervision.task_progress_interval=0` ＝关闭不写）。
- **差异**：`last_progress_at` 全 NULL 可由「写回开关出厂 0」解释（但读侧需区分「未启用」与「无进度」）；而 `task_deadline` 全 NULL 说明**该列根本没有生产者**，既有偏差清单反而断言写入正常，且没有「任务级 deadline 缺失时回退 `batch_deadline`」的规则。

#### H9（P2）：运行期 `result_summary` 存成空字节字面量 `b''`

- **实测**：批记录运行期 `result_summary` 的值为字符串 `"b''"`（Python bytes 字面量的误序列化形态），终态才写成 JSON。
- **影响**：运行期读该字段的任何消费方（含 `read_agent_result` 的 summary 路径）会拿到无意义内容；提示写入路径存在 bytes/str 混用。
- **既有覆盖**：最接近的是 `supervision-parent-child-control-optimization-plan-20260917.md:233`（「核对子会话完成路径是否已把有界结果写入 batch task `ResultSummary`/`ArtifactRef`…避免父侧读到空结果」）与 `spawn-subagents-async-supervisor-plan.md:195`（§4.3 采用 `result_summary_ref` 新模型）。
- **差异**：二者都指向「避免读到空结果」，但**没有 `b''` 误序列化这一具体缺陷的条目**，也无 bytes/str 混用的回归用例。

#### H10（P0）：策略拒绝使**已完成**任务被判 failed

- **实测**：批次终态 `failed`（completed=1 / failed=2），两个失败任务 `error_class='policy'`，错误为 `read-only policy blocks compound shell command; submit one command per shell.commands entry`。**但这两个任务的 `result_json.summary` 里已有完整分析交付物（6817 / 6113 字符）**。
- **关键判断**：失败原因是**与任务目标无关的执行细节**（一条被拒的 shell 命令），而任务的实质产出已经生成并落库。
- **与既有方案的关系**：主计划 **N10** 已记录「只读子代理的管道复合命令被 policy 整体拒绝，批次失败且无恢复指引」，`subagent-readonly-boundary-transparency-plan-20260917.md` 已实施拒绝消息可读化（`[TOOL_DENIED:ERR_READONLY_*]` + boundary/rule/fix 三段式、shell 描述预告、拒绝计数熔断）。**本文件新增的是下一层**：拒绝消息已经可读（实测错误串确实给出了 fix），但——
  1. 拒绝被**升级为任务级终态失败**，而非「降级为一次工具错误 + 继续」；
  2. 失败判定**不看已产出的结果**，交付物被状态掩盖；
  3. 父代理若按状态重派，会**重复消耗**已完成的工作（本批次 230 秒 × 2）；
  4. 与 H1 叠加后，父代理即使想核实正文也取不出来。
- **审计确认（逐字）**：N10 全文（`multi-agent-execution-optimization-plan.md:178-184`）与归口（`:124`，P1-7/P2-12）**均未出现「结果已生成 / 交付物 / partial result 保留」的语义**，只讲到「机械失败 + 无 next_action + 无重试降级 + 父代理事后才发现」。唯一相邻的 `partially_completed`（`spawn-subagents-async-supervisor-plan.md:175`）与 §10 Phase 3「根据 partial failure 继续规划」（`:544`）是**batch 级**语义，未绑定「task 级 failed 但结果可用」。
- **影响**：这是「多 agent 协作产出」主路径上的**正确性缺陷**，不是体验问题。

### 4.3 隔离与边界轴

#### H11（P1）：worktree 孤儿目录泄漏（已实证）

- **实测**：`E:\projects\ai\ai-agent-runtime\.aicli\agent-worktrees\resume-redraw-fix` 目录存在（2026-09-14），但 `git worktree list` **不包含**它——该目录已不被 git 管理，成为孤儿。
- **代码佐证**：`backend/cmd/aicli/commands/chat_actor_registry.go:1215` 注释「Keep worktree after completion so parent can apply/discard explicitly. close_agent still cleans remaining worktrees.」；清理实现 `backend/internal/isolation/worktree/worktree.go:172-186`（`worktree remove --force` + `branch -D`）。
- **既有覆盖**：`supervision-parent-child-control-optimization-plan-20260917.md` §4.3 P1-3（261-266 行，`close_agent` 的 `worktree_pending` 守卫）与 §10 遗留问题（506 行，**自述 P1-3 未实施**）。全 `docs/` 检索 `agent-worktrees` 仅命中 `docs/config/aicli-config-load-save.md:11`（目录列举），**无 prune / 周期清扫方案**。

#### H12（P1）：子代理并发无全局上限

- **机制**：`SubagentSchedulerConfig.MaxConcurrent` 默认 4（`backend/internal/agent/scheduler.go:159-163`），但配置面**只透传 `Routing`、未透传并发上限**（`backend/cmd/aicli/commands/chat_actor_host.go:1742-1745`）→ 每个 background batch 各自起 worker，实际并发为 **4×N**；队列为无限排队，无队列超时 / 优先级 / 背压。
- **既有覆盖**：主计划 P2-8（477-510 行）与 N8（163-166 行）已定义全局 spawn 配额 `agents.maxThreads`（默认 6、`-1` 显式不限，**已实施**）；`multi-agent-framework-codex-comparison-plan.md` §5.5（347-360 行）提出 root/team/subagent 共用同一计数。
- **差异**：既有全局计数只覆盖 **spawn 闸门**；`MaxConcurrent` 的配置透传缺失无人认领，且 P2-8 明确「不引入排队语义（保持快速失败）」——即**无队列超时/优先级/背压方案**。

#### H13（P1）：子代理请求更高权限会被接受

- **机制（已核验）**：`backend/internal/toolbroker/spawn_agent_permission.go:65-98` 规则 5——子代理请求**高于**父级的 `permission_mode` 会被**保留**，仅追加 `permission_mode_escalated_from_parent` 告警（常量见同文件 14-27 行）。只有 `bypass_permissions` 父级会向下钉住（`spawnAgentPermissionPinnedToParent`，102-110 行），`plan` 父级把子级钉回 `plan`。
- **schema 暴露（已核实属实）**：`permission_mode` 确实出现在模型可见的 spawn 工具 schema 中——`backend/internal/toolbroker/broker.go:362` 定义该参数，enum 为 `default|accept_edits|plan|bypass_permissions`，描述含 pinned-to-parent 语义。
- **既有覆盖**：`llm-tool-authorization-policy-convergence-plan.md` §3.1（36-45 行）只描述 4 种 mode 的基础语义，§2.2（26-32 行）非目标未涉及父-子权限天花板；`subagent-readonly-boundary-transparency-plan-20260917.md:47` 的非目标语境限于 `read_only` 边界；`task-difficulty-model-routing-plan.md:2883` 反而将子代理显式使用 `bypass_permissions` 视为**预期能力**。→ **权限天花板在 `docs/plan/` 中无对应治理方案**。

#### H14（P1）：worktree apply 无冲突检测

- **机制**：apply 走 `git checkout <branch> -- <paths>`（`backend/internal/isolation/worktree/worktree.go:217-258`），**不检测冲突**、可能直接覆盖主树同名文件；范围外的未跟踪文件不在合入集合内。入口见 `backend/cmd/aicli/commands/chat_actor_registry.go:777-808`。
- **既有覆盖**：`supervision-parent-child-control-analysis-20260917.md:152,155` 只记录 apply/discard 契约（completion 不 auto-apply，父须先 review）；`grok-harness-productization-implementation-plan.md:110-113` 只描述 apply/discard 工具面、`:385` 只提 path claims 协同。全 `docs/` 检索「冲突检测」仅命中 `workspace-right-panel-file-browser-and-git-diff-plan.md:62`（文件编辑器场景，无关）。→ **无方案**。

#### H15（P2）：隔离层回收缺统一对账

- **机制**：检索 `prune` / `agent-worktrees` 未发现任何「清理 / 回收 worktree」的定期扫描；session / artifact 侧有 prune（`chat_debug.go:239,275`、`chat_agent_cleanup.go:40`），worktree 层没有对应机制。
- **既有覆盖**：主计划 P2-9（514-547 行）及实施记录（759、825-840 行）已有 agent registry ↔ session 的 Reconciler / `SweepAgentQuotaReclaim` / `agent.reclaimed` / `/agents cleanup`，但范围**仅为 agent/session 配额**。
- **差异**：统一对账未覆盖隔离层——`.aicli/agent-worktrees` 目录与 git worktree 注册表无任何定期扫描 / 清扫 / 对账（与 H11 同源缺口的两面）。

---

## 5. 优化详案

每项包含：问题、方案、验收、风险。优先级定义沿用主计划口径（P0 = 主路径阻断，P1 = 正确性/资源风险，P2 = 质量改进）。

### P0-1 结果取回契约修复（H1 + H4）

**问题**：`read_agent_result` 返回成功但正文不在返回里；`sections=["output"]` 被拒；无分页；artifacts 段无解引用指引；子代理终文本默认不归档；父级摘要超预算整条丢弃。

**方案**

1. **白名单补齐（最小修复）**：在 `backend/internal/output/tool_result_content.go:379-390` 的协作结果白名单中补入 `read_agent_result`，与 `wait_agent` / `read_agent_events` 同口径。这是 H1 的**根因修复**，改动面最小。
2. **section 词表容错**：`backend/internal/supervision/agent_result.go:183-188` 增加 `output` 作为 `summary` 的别名；错误串在列出合法词表之外，追加 `next_action`（指明「用 `sections=["summary"]` 或 `artifact_read`」），对齐主计划 P1-7 的协作引导口径。
3. **分页对齐**：为 `read_agent_result` 增加 `offset` / `limit`，返回 `eof` 标志，语义与 `artifact_read`（`backend/internal/toolbroker/artifact_read.go:138-230`）一致；summary 的 512 runes 截断保留为**默认视图**，不再作为唯一出口。
4. **artifacts 段可解引用**：`agent_result.go:352-358,395-406` 的每条 ref 附带 `artifact_read(id=...)` 指引文案。
5. **终文本归档**：子代理最终文本超过 `artifactArchiveMinBytes()` 阈值时进入 gateway 归档（复用 `backend/internal/output/gateway.go:141-176` 的分级），返回指针而非丢弃。
6. **父级摘要不整条丢弃**：`backend/internal/agent/subagent_parent_summary.go:29-60` 的超预算分支改为「截断 + 指针 + `Omitted` 可展开」，禁止无回退路径的丢弃。

**验收**

- 单测：对 `read_agent_result` 的渲染断言**正文非空**（防止白名单再次漏项）。
- 单测：`sections=["output"]` 返回与 `sections=["summary"]` 等价内容 + `next_action`。
- e2e：跑一个 background 批次，父代理**仅通过工具面**取回 6000 字符级交付物（复现 `batch_42d0a52d2a24e401` 的两个 6817 / 6113 字符结果）。

**风险**：正文回填会放大父上下文。缓解：保留既有 512 rune / 4000 字符预算作为默认视图，正文走显式 `sections` 或分页；归档阈值与 `artifact_read` 同源，避免两套预算漂移。

### P0-2 交付物保全与失败语义（H10 + H9）

**问题**：任务已产出完整交付物，却因一条被拒的 shell 命令被判 `failed`；运行期 `result_summary` 写入 `b''` 伪值；父代理若按状态行事会丢弃并重派已完成的工作。

**方案**

1. **失败判定看结果**：任务终态判定引入「结果存在性」维度——只要 `result_json` / `result_summary` 非空，终态不得为裸 `failed`，改为携带结果的终态（`failed_with_result`，或复用既有 `partially_completed` 语义，见 `spawn-subagents-async-supervisor-plan.md:175`），并在批计数中单列。
2. **策略拒绝降级**：`error_class='policy'` 默认**降级为工具级错误**，不升级为任务级终态；仅当拒绝计数达到既有熔断阈值（`subagent-readonly-boundary-transparency-plan-20260917.md` 的 M6 hard stop = 5 次）才终态。这与「拒绝消息已可读、已给出 fix」的现状配套——既然 fix 可执行，就不该在第一次拒绝时判死。
3. **父级摘要上浮**：渲染 `failed` 任务时，若存在可用结果，输出「⚠ 已产出交付物；失败原因：<error>」+ 结果正文或指针，并显式提示**不要重派**。
4. **修 `b''` 伪值（H9）**：修 `result_summary` 写入路径的 bytes/str 混用；运行期无值一律写 `NULL`，禁止伪值入库；读侧对 `NULL` 显示「进行中」而非空串。
5. **绑定 partial 语义**：把 `spawn-subagents-async-supervisor-plan.md` §10 Phase 3「根据 partial failure 继续规划」（`:544`）从 **batch 级**下沉到 **task 级**，明确「task failed 但结果可用」时父代理的合法动作集合（取用 / 追加 / 不重派）。

**验收**

- 故障注入：只读子代理先产出结果、后触发复合命令拒绝 → 断言终态携带结果、父摘要可见、`error_class` 保留可诊断性。
- 断言父代理**不会**重派同一任务（用既有 `deny_streak` / `requires_write` 事件面校验）。
- 回归：`result_summary` 运行期读值为 `NULL` 或合法 JSON，`b''` 不再出现。

**风险**：终态枚举扩展会影响前端与 CLI 渲染（`frontend/src` trajectory、`chat_debug` 面板）。缓解：未知枚举按 `failed` 渲染保持向后兼容；本项与 P0-1 必须**同批实施**，否则「状态说失败、正文取不出」的组合仍然成立。

### P1-1 批记录运行期一致性（H6 + H7 + H8）

**问题**：运行期 `queued_count=3, running_count=0` 与三行 task `running` 互相否定；`child_session_id` 终态才写；`task_deadline` / `last_progress_at` 全 NULL。

**方案**

1. **单一事实源**：读侧一律由 task 行派生计数（复用 `supervision-business-supervision-implementation-plan.md:191` 已有的 `ListTasks` + `subagentbatch.Counts(tasks)` 做法），或让 batch 计数器与 task 状态转换**同事务**更新。二者择一，不允许双写漂移。
2. **运行期写 `child_session_id`**：子会话创建成功后立即 CAS 写回 task 行；终态仅做校正。这修正 `supervision-parent-child-control-optimization-plan-20260917.md:485` 把「终态补写」当闭环的偏差。
3. **运行期解析降级语义**：`wait_agent` / `read_agent_events` 接收 task_id 时，若绑定尚未就绪，返回显式状态（如 `pending_binding` + `next_action`），而非 `missing / exists:false`。
4. **补 `task_deadline` 生产者**：建批时按 batch 分摊写入，或在缺失时由读侧回退 `batch_deadline` 并标注来源；同时修正既有文档 `supervision-parent-child-control-optimization-plan-20260917.md:481` §3 偏差 1「四列写入正常」的错误断言。
5. **进度写回语义显式化**：`supervision.task_progress_interval=0`（出厂关闭，见同文档 `:495`）需在读侧体现为「未启用」，与「无进度」区分开。

**验收**

- 运行期快照断言 `running_count == count(tasks where status='running')`。
- 运行期用 task_id 可解析到子会话并读到 ≥1 条事件。
- 断言 `task_deadline` 非 NULL（或读侧有显式回退标注）。

**风险**：读侧改为派生会改变查询形态。缓解：单批 task 行数有限（本例 3 行），开销可忽略；跨进程并发写用 CAS。

### P1-2 存储保留与载荷裁剪（H2 + H3）

**问题**：`session_runtime.sqlite` 907 MB（75% 字节是流式增量）；`agent_control.sqlite` 541 MB / wake 事件 1.24M 行，是实体行（924）的 1341 倍。

**方案**

1. **载荷字节级上限（H2 的核心增量）**：`session_events` 增加单条 + 单会话累计字节上限，超限即归档为 artifact 并留指针；**保留**既有条数裁剪（2048 / 256）不变。
2. **增量双写收敛**：按 `sse-live-event-channel-optimization-plan.md:220-228` 的「增量只落一处」方案落地（预估存储 −45~52%）。
3. **wake 事件治理（H3）**：
   - active 行设**事件上限**（每 agent 保留最近 K 条），补齐既有文档承认但未解决的缺口（主计划 `:842`）；
   - closed 行缩短保留窗口或引入按天分区 / 压缩；
   - 补一次性存量清理（1.13M closed 行）。
4. **可观测**：表大小 / 行数 / 最老行时间纳入既有 `/debug` 面板，使保留策略可验证。

**验收**

- `agent_control.sqlite` 清理后显著回落，且 active agent 的唤醒语义不丢失。
- `session_runtime.sqlite` 增量写入量下降 ≥45%（对照既有预估）。
- 裁剪后会话回放/审计仍可从 artifact 指针还原。

**风险**：裁剪影响回放与审计。缓解：默认保守、可配置；归档走 artifact 保证可追溯；先 observe 后 enforce（与 P2-9 同风格）。

### P1-3 隔离回收与对账（H11 + H15 + H14）

**问题**：`.aicli/agent-worktrees/resume-redraw-fix` 已是孤儿目录（`git worktree list` 不含）；worktree 层无任何周期清扫；apply 无冲突检测。

**方案**

1. **先补既有 P1-3**：实施 `supervision-parent-child-control-optimization-plan-20260917.md` §4.3 P1-3 的 `worktree_pending` 守卫（该文档 §10 自述未实施）。
2. **worktree 对账（H11 + H15）**：新增对账任务，比对三处事实源——`.aicli/agent-worktrees/*` 目录、`git worktree list`、agent registry 行；孤儿目录 → `git worktree prune` + 目录清理 + 告警。纳入既有 P2-9 Reconciler（与 `SweepAgentQuotaReclaim` 同族），默认 observe、显式 enforce。
3. **apply 冲突检测（H14）**：`backend/internal/isolation/worktree/worktree.go:217-258` 的 `git checkout <branch> -- <paths>` 之前先做预检（`git diff --quiet` 或 merge-tree），冲突时**拒绝**并返回可操作错误 + `next_action`；同时显式列出「范围外未跟踪文件不在合入集合内」。

**验收**

- 构造孤儿目录 → 对账任务可回收，且不误删活跃 worktree。
- 构造主树同名文件冲突 → apply 拒绝并给出 `next_action`。
- 单测覆盖 `worktree_pending` 守卫。

**风险**：对账误删活跃 worktree。缓解：必须 registry + `git worktree list` 双重确认；默认 observe。

### P1-4 并发上限与权限天花板（H12 + H13）

**问题**：`MaxConcurrent=4` 未透传配置面 → 实际 4×N 并发，队列无背压；子代理请求高于父级的 `permission_mode` 会被接受。

**方案**

1. **透传并发上限（H12）**：把 `SubagentSchedulerConfig.MaxConcurrent` 接入配置面（`backend/cmd/aicli/commands/chat_actor_host.go:1742-1745` 目前只透传 `Routing`），并提供与 `agents.maxThreads` 的显式关系（同一计数或明确乘法上限），消除 4×N。
2. **可选背压**：新增队列深度上限与排队超时，作为**可选**语义；明确其与主计划 P2-8「不引入排队语义、保持快速失败」的关系——默认行为不变，背压需显式开启。
3. **权限天花板（H13）**：子代理请求高于父级时，默认**拒绝并钉住父级**（与 `bypass_permissions` / `plan` 父级的既有钉住行为一致），保留显式 opt-in 开关；`permission_mode_escalated_from_parent` 由「仅告警」升级为「默认阻断 + 告警」。同步 `backend/internal/toolbroker/broker.go:362` 的 schema 描述，并修订 `task-difficulty-model-routing-plan.md:2883` 中「子代理显式使用 `bypass_permissions` 是预期能力」的表述。

**验收**

- 配置 `MaxConcurrent=2` 并起 3 个 batch → 断言全局并发 ≤2。
- 父级 `accept_edits` 派 `bypass_permissions` 子代理 → 默认被拒并附 `next_action`。
- 单测覆盖 `spawn_agent_permission.go` 规则 5 的新默认分支。

**风险**：权限收紧是**行为变更**，可能破坏依赖提权的既有流程。缓解：提供 opt-in 开关 + 迁移说明；在实施记录中标注行为变更；灰度期保留告警-only 模式。

### P2-1 聚合质量（H5）

**问题**：父级摘要为逐条拼接 + 预算截断，无跨子代理语义去重、无冲突消解、无稳定排序。

**方案**

1. **语义去重**：按结论指纹（去重键）合并重复结论，保留来源标注。
2. **冲突消解**：同一问题出现不同结论时**显式并列 + 标注来源**，不自动裁决。
3. **稳定排序**：按 task 声明顺序而非完成顺序输出，保证可复现。
4. **`Omitted` 可展开**：计数改为可展开指针（与 P0-1 的归档指针同源）。

**验收**：注入两个结论重叠的子代理 → 断言摘要去重且保留来源；注入结论冲突的两个子代理 → 断言并列呈现且不自动裁决。

**风险**：去重可能误合并不同结论。缓解：仅做指纹级精确去重；冲突永不自动裁决。

---

## 6. 与既有方案的关系（覆盖审计）

对 `docs/plan/` 全部 118 份方案做了覆盖比对，结论如下。**判据**：若既有文档只定义了契约/数据模型，而未记录该缺陷、未给出修复或验收项，则记为「部分覆盖」或「未覆盖」，本文件负责补齐该层。

| 发现 | 结论 | 主要既有文档 | 本文件新增的部分 |
| --- | --- | --- | --- |
| H1 | 部分覆盖 | `supervision-parent-child-control-optimization-plan-20260917.md:216,358,452`；`tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md:16` | 白名单漏项导致**正文整体丢失**这一 P0 缺陷、`sections=["output"]` 被拒、分页与解引用指引 |
| H2 | 部分覆盖 | `sse-live-event-channel-optimization-plan.md:68-86,220-228`；`runtime-store-append-hotpath-optimization-plan-20260918.md:21,60` | **载荷字节级**上限（条数裁剪挡不住大载荷）、907 MB 实测规模复测 |
| H3 | 部分覆盖 | 主计划 `:842,844,847`；`spawn-subagents-async-supervisor-plan.md:397` | **active 行 wake 事件上限**、1.13M closed 存量清理、更短窗口/分区 |
| H4 | 部分覆盖 | `tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md:14,15,106-108,178-181,334-347` | `read_agent_result` 无分页、artifacts 段无解引用、终文本不归档、父级摘要整条丢弃无回退指针 |
| H5 | **未覆盖** | 仅投递层幂等（`spawn-subagents-async-supervisor-plan.md:397`） | 语义去重、冲突消解、稳定排序（全 `docs/plan` 无命中） |
| H6 | 部分覆盖 | `spawn-subagents-async-supervisor-plan.md:176`；`supervision-business-supervision-implementation-plan.md:191` | 计数一致性约束、运行期漂移的测试/验收项 |
| H7 | 部分覆盖 | `supervision-parent-child-control-optimization-plan-20260917.md:485,452`；`supervision-parent-child-control-analysis-20260917.md:92,94` | 要求**运行期**可解析（既有方案把「终态补写」当闭环）、`missing` 降级语义 |
| H8 | 部分覆盖 | `spawn-subagents-async-supervisor-plan.md:192,194,372-378`；`supervision-parent-child-control-optimization-plan-20260917.md:481,450,495` | `task_deadline` 无生产者、修正「四列写入正常」的错误断言、deadline 回退规则 |
| H9 | **未覆盖** | `supervision-parent-child-control-optimization-plan-20260917.md:233`；`spawn-subagents-async-supervisor-plan.md:195` | `b''` 误序列化缺陷与回归用例 |
| H10 | 部分覆盖 | 主计划 N10（`:178-184`，归口 `:124`）；`subagent-readonly-boundary-transparency-plan-20260917.md:66-69,131` | **交付物已生成却被 failed 掩盖**、失败判定须看结果、父代理不得重派 |
| H11 | **未覆盖** | `supervision-parent-child-control-optimization-plan-20260917.md` §4.3 P1-3（`:261-266`）、§10 遗留（`:506`，自述未实施） | worktree **孤儿目录**周期清扫与对账 |
| H12 | 部分覆盖 | 主计划 P2-8（`:477-510`）、N8（`:163-166`）；`multi-agent-framework-codex-comparison-plan.md:347-360` | `MaxConcurrent` 配置透传缺失（4×N）、队列背压 |
| H13 | **未覆盖** | `llm-tool-authorization-policy-convergence-plan.md:36-45,26-32`；`task-difficulty-model-routing-plan.md:2883` | 父-子**权限天花板**（既有文档反而视提权为预期能力） |
| H14 | **未覆盖** | `supervision-parent-child-control-analysis-20260917.md:152,155`；`grok-harness-productization-implementation-plan.md:110-113,385` | apply 的**冲突检测**（全 `docs/plan` 无命中） |
| H15 | 部分覆盖 | 主计划 P2-9（`:514-547`）、实施记录（`:759,825-840`） | 对账范围**未覆盖隔离层**（worktree 目录与 git 注册表） |

**统计**：未覆盖 5 项（H5、H9、H11、H13、H14）、部分覆盖 10 项（H1、H2、H3、H4、H6、H7、H8、H10、H12、H15）、完全已覆盖 **0** 项。→ 本文件不重复既有方案，但 15 项中**无一项**被既有文档完整闭环，说明这条链路（产出之后如何可靠取回与诚实记账）确实是既有方案的空白带。

**两条需要立即纠正的既有文档错误**

1. `supervision-parent-child-control-optimization-plan-20260917.md:481` §3 偏差 1 断言 `SubagentTaskRecord` 四列「写入正常、仅读投影丢弃」——实测 `task_deadline` 与 `last_progress_at` **全为 NULL**，该断言与事实矛盾（H8）。
2. `task-difficulty-model-routing-plan.md:2883` 将子代理显式使用 `bypass_permissions` 视为预期能力——与「父-子权限天花板」的安全预期冲突（H13）。

---

## 7. 实施路线图

排序原则：先修「取回」与「保全」（P0-1 / P0-2 必须同批），再修「记账」与「存储」，最后做「隔离」与「质量」。P0 两项不落地时，其余项的价值都会被「状态说失败、正文取不出」的组合抵消。

| 阶段 | 内容 | 覆盖发现 | 前置依赖 | 建议规模 |
| --- | --- | --- | --- | --- |
| Phase 0 | **取回 + 保全**：白名单补齐、section 别名、分页、artifacts 解引用、终文本归档、父摘要不整条丢弃；失败判定看结果、策略拒绝降级、`b''` 修复、partial 语义下沉 | H1、H4、H10、H9 | 无 | 2 个可独立验证的批次 |
| Phase 1 | **记账一致性**：计数单一事实源、运行期写 `child_session_id`、task_id 解析降级语义、补 `task_deadline` 生产者、修正既有文档错误断言 | H6、H7、H8 | Phase 0（父摘要需能显示进度） | 1 个批次 |
| Phase 2 | **存储治理**：载荷字节级上限、增量双写收敛、wake 事件上限与存量清理、可观测指标 | H2、H3 | 无（可与 Phase 1 并行） | 2 个批次 |
| Phase 3 | **隔离与边界**：既有 P1-3 守卫、worktree 对账、apply 冲突检测、并发上限透传、权限天花板 | H11、H15、H14、H12、H13 | 无（独立轴） | 2–3 个批次 |
| Phase 4 | **聚合质量**：语义去重、冲突消解、稳定排序、`Omitted` 可展开 | H5 | Phase 0（依赖归档指针） | 1 个批次 |

**跨阶段约束**

- Phase 0 的 P0-1 与 P0-2 必须**同一批次**实施与验收：只修其一，都会留下「可发现但不可取回」或「可取回但状态骗人」的半闭环。
- Phase 3 的权限收紧（H13）是**行为变更**，需单独标注并给迁移说明，不与其它项混批。
- 所有存储保留策略（Phase 2）与隔离对账（Phase 3）统一采用既有 P2-9 的 **observe → enforce** 两段式，避免误删。

---

## 8. 测试计划

### 8.1 单元测试

| 目标 | 断言 |
| --- | --- |
| H1 白名单回归 | 对 `read_agent_result` 的渲染结果断言**正文非空**（防止白名单再次漏项）；`sections=["output"]` 与 `sections=["summary"]` 等价 |
| H1 分页 | `offset` / `limit` / `eof` 语义与 `artifact_read` 一致 |
| H4 父摘要 | 超预算时产出「截断 + 指针」，**不得**整条丢弃 |
| H9 伪值 | 运行期 `result_summary` 为 `NULL` 或合法 JSON，`b''` 不再出现 |
| H10 失败判定 | 结果非空 + `error_class='policy'` → 终态携带结果，非裸 `failed` |
| H6 计数一致性 | `running_count == count(tasks where status='running')` |
| H8 deadline | `task_deadline` 非 NULL，或读侧有显式回退标注 |
| H13 权限天花板 | 子代理请求高于父级 → 默认拒绝并钉住父级 |
| H14 冲突检测 | 主树冲突时 apply 拒绝并返回 `next_action` |

### 8.2 故障注入与 e2e

1. **复现批次**：重跑 `batch_42d0a52d2a24e401` 的同构场景（3 个只读 hard 任务 + 复合命令），断言：终态**不是** `failed`、父代理能取回 6000 字符级交付物、不触发重派。
2. **孤儿 worktree 回收**：手工造 `.aicli/agent-worktrees/<orphan>` → 对账任务回收且不误删活跃 worktree。
3. **并发上限**：`MaxConcurrent=2` + 3 个 batch → 全局并发 ≤2。
4. **运行期可观测**：批次运行中用 task_id 调 `wait_agent` / `read_agent_events` → 可解析、≥1 事件。

> **执行记录（2026-09-20 补做，全部为可重复命令，不再依赖手工 runbook）**
>
> | 项 | 覆盖 | 命令 | 结果 |
> | --- | --- | --- | --- |
> | 1 复现批次 | 新增 `backend/internal/agent/subagent_batch_acceptance_test.go::TestBatchPolicyRefusalKeepsDeliverableAndDoesNotRedispatch`：3 个只读 hard 任务、其中 2 个策略拒绝（复合 shell 命令）、6000 字符级交付物，走**真实协调器 + SQLite 批存储 + `BuildReadResultPayload`** | `go test ./internal/agent -run TestBatchPolicyRefusalKeepsDeliverableAndDoesNotRedispatch -count=1` | PASS |
> | 2 孤儿 worktree 回收 | 既有 `internal/isolation/worktree/reconcile_test.go`：`TestReconcileWorktreesReclaimsOrphanDir` / `KeepsRegisteredWorktree` / `KeepsGitRegisteredUnmanaged` / `PrunesStaleRegistration` / `MissingBaseDirIsEmpty`（造孤儿目录 → 回收且不误删活跃与未托管 worktree） | `go test ./internal/isolation/worktree -run ReconcileWorktrees -count=1` | PASS（5/5） |
> | 3 并发上限 | 既有 `SubagentConcurrencyLimiter` 全局准入用例（进程级 ≤ 上限语义） | `go test ./internal/agent -run GlobalConcurrency -count=1` | PASS |
> | 4 运行期可观测 | 新增 `subagent_batch_acceptance_test.go::TestBatchRunningTaskExposesProgressAnchor`：运行期 `child_session_id` 已绑定（H7）、`task_deadline` / `started_at` / `last_progress_at` 非空（H8）、父侧进度投影由 task 行派生为 1/1/0/0/0（H6） | `go test ./internal/agent -run TestBatchRunningTaskExposesProgressAnchor -count=1` | PASS |
>
> **与原文的偏差（按实施结果更正）**
>
> - 第 1 项原写「断言终态**不是** `failed`」。P0-2 选择的是**保留诚实失败 + `failed_with_result` 子计数 + 读出口 `result_available` / `do_not_retry` 指引**：任务确实没有完成被要求的工作，把批次改判 `completed` 反而掩盖真实失败。验收因此改为断言「失败不再掩盖交付物，且不触发重派」（终态 `failed`，`completed=1 / failed=2 / failed_with_result=2`，worker 只派发一次）。
> - 第 4 项原写「用 task_id 调 `wait_agent` / `read_agent_events`」。这两个工具的解析依赖 H7 的 `child_session_id` 绑定，工具层行为由 `internal/toolbroker` / `internal/supervision` 既有用例覆盖；本项在宿主级只验收**可解析锚点**（运行中的 task 行 + 派生投影），避免与工具层用例重复。
> - 第 4 项的「≥1 事件」在宿主级落为 `last_progress_at` 非空与派生投影中的运行中任务明细（`RunningTasks[0]` 携带 `task_id` / `child_session_id` / `state` / `last_progress_at`），事件本身仍在子会话事件流中。

### 8.3 存储治理验证

- `agent_control.sqlite`：清理前后体积/行数对照，active 唤醒语义不丢失。
- `session_runtime.sqlite`：增量写入量下降 ≥45%（对照 `sse-live-event-channel-optimization-plan.md:220-228` 的预估）。
- 裁剪后可追溯性：随机抽样若干被裁剪事件，确认能通过 artifact 指针还原。

> **基线测量（2026-09-20，只读；脚本 `output/multiagent-review/db_inventory.py`，`mode=ro` 打开，不写库）**
>
> | 库 | 体积 | 目标表 | 行数 | 载荷 |
> | --- | --- | --- | --- | --- |
> | `agent_control.sqlite` | 580.5 MB | `agent_control_agent_wake_events` | 1,266,733 | 无载荷列：窄行 + 索引，≈459 B/行 |
> | `session_runtime.sqlite` | 65.3 MB | `session_events` | 17,255 | `payload_json` 合计 20.4 MB，均值 1,183 B，最大 231,037 B，**超 256 KiB 上限 0 行** |
> | `subagent_batches/*.sqlite` | 29.3 MB（635 个分片文件） | — | — | 按会话分片，单文件最大 `session_20260917155815_3vE4qX2F.sqlite` |
>
> **结论与偏差**
>
> 1. `agent_control.sqlite` 的回收杠杆确实是 wake 行（1,266,733 行占 580.5 MB 的绝大部分），与 H3 取证（1.24M 行 / 541 MB）同量级。候选计算与 enforce 删除路径已实现并有单测覆盖（§10.1 H3），但**对本机真实库的 enforce 运行尚未执行**：它删除存量行，属破坏性操作，需显式授权后按 §8.4 runbook 执行。
> 2. **载荷上限对当前语料零影响**：现网 17,255 条 `session_events` 全部低于 256 KiB 上限（最大 231,037 B），P1-2 的载荷裁剪只对未来的大载荷写入生效。因此「增量写入量下降 ≥45%」在本机语料上**不可测**——该数字源自 `sse-live-event-channel-optimization-plan.md:220-228` 的大载荷流量预估，不是当前数据的实测值。此项按「机制已就位、量级待大载荷流量验证」记录，不伪造对照数字。
> 3. 第 3 条（裁剪后可追溯性）与 §10.5-1 一致：`internal/chat` 写入路径上无 artifact store 可达，裁剪只留 stub 指针；且当前语料无被裁剪行，**无样本可抽**，故该项以「不可执行 + 原因」结案而非留空。

> **回收实测（2026-09-20；对 `agent_control.sqlite` 的副本执行真实 `PruneAgentWakeEvents`，线上库全程只读）**
>
> 命令：`python output/multiagent-review/prune_measure.py`（副本由 SQLite backup API 生成；Go 探针 `backend/tmp/prune_probe`，属 gitignore 的证据产物）
>
> | 阶段 | wake 行数 | closed/stale | active | 文件 |
> | --- | --- | --- | --- | --- |
> | 副本初始 | 1,270,982 | 1,159,473 | 111,509 | 582.6 MB |
> | observe 一遍 | 1,270,982（**0 删除**） | — | — | — |
> | enforce 一遍（有界） | 1,250,982（−20,000） | 1,149,473 | 101,509 | 630.2 MB（含 WAL；freelist 1,684 页） |
>
> - observe 输出 `closed_candidates=4096 / overflow_candidates=4096`，`closed_deleted=0 / overflow_deleted=0`——**观察模式确实零写入**，与 `retention_test.go` 的契约一致。
> - enforce 一遍按宿主语义的边界（`batch_limit=5000`、`max_batches=2`，两半各自计批）删除 `closed=10000 + overflow=10000 = 20000` 行、`batches=4`；行数与删除量**逐行吻合**（1,270,982 − 20,000 = 1,250,982）。
> - `agent_control_agents` 936 行前后不变：身份表未被回收波及；active 侧那 10,000 行删除正是「每 agent 只保留最新 K 条非终态唤醒」的上限收敛，不是误删在跑会话的唤醒。
> - **体积口径**：删除后文件反而变大（582.6 → 630.2 MB），因为改动先落在 WAL，且 DELETE 不归还页。要真正缩容必须 checkpoint + `VACUUM`——「清理前后体积对照」只看删除后的文件大小会得出反向结论。
> - **回收不是一遍完成的**：closed/stale 存量 1,159,473 行，一遍只删 10,000 行（有界设计）。按此速率清空存量需 ~116 遍，runbook 必须写成「重复有界遍数直到候选归零」，而不是「跑一次对账」。首次尝试用一次性 500 遍全量清扫，15 分钟内未收敛且无增量输出，已放弃该写法——有界 + 可观测才是可运维形态。

### 8.4 建议扩展的既有脚本

`docs/plan/multi-agent-real-terminal-validation-runbook.md` 的验收脚本应扩展 8.2 的四项；`session-analytics-runbook.md` 可承载 8.3 的存储对照。

---

## 9. 风险与回滚

| 风险 | 影响 | 缓解 | 回滚 |
| --- | --- | --- | --- |
| 正文回填放大父上下文 | 父代理上下文被挤占 | 保留 512 rune / 4000 字符默认视图；正文走显式 `sections` 或分页；归档阈值与 `artifact_read` 同源 | 关闭 `sections` 扩展，仅保留白名单修复 |
| 终态枚举扩展破坏渲染 | 前端 / CLI 显示异常 | 未知枚举按 `failed` 渲染；同步 `frontend/src` trajectory 与 CLI 面板 | 回退枚举，改为在 `error` 字段携带结果标记 |
| 策略拒绝降级导致「该停不停」 | 只读子代理反复撞墙 | 复用既有 M6 熔断阈值（连续 5 次硬停），不新增阈值 | 恢复「首次拒绝即终态」开关 |
| 权限收紧破坏既有提权流程 | 依赖 `bypass_permissions` 的流程失败 | opt-in 开关 + 迁移说明 + 灰度期告警-only | 开关回退到旧行为 |
| 存储裁剪影响回放/审计 | 历史会话不可完整回放 | 默认保守、可配置；归档走 artifact 保证可追溯；observe → enforce | 关闭裁剪，恢复全量保留 |
| worktree 对账误删活跃目录 | 丢失未 apply 的工作 | registry + `git worktree list` 双重确认；默认 observe | 关闭 enforce，仅告警 |

**全局回滚原则**：本文件所有项均为**增量开关**，默认行为与现状保持一致；任何一项出现回归，均可在不改动其它项的前提下单独关闭。

---

## 10. 实施记录

**状态：已实施（2026-09-20 回填）。** H1–H15 的修复已在本工作树落地并通过定向测试；§8.2 的真实终端 e2e 与 §8.3 的存储体量对照**尚未执行**，故 §8 的验收项仍为待办。

状态口径对齐主计划 §10：**已实施** = 代码与定向测试均在本工作树落地；**部分** = 主路径已落地，周边（真实验证 / 文档 / 可视化）待补。

### 10.1 落地情况

| 项 | 状态 | 代码落点 | 定向测试 |
| --- | --- | --- | --- |
| P0-1 结果取回契约（H1 + H4） | 已实施 | 协作结果白名单补齐 `read_agent_result`（注释即 H1 回归说明）：`backend/internal/output/tool_result_content.go:379-395`；section 词表 / 正文渲染 / 分页：`backend/internal/supervision/agent_result.go`、`backend/internal/toolbroker/supervision_tools.go`；结果面：`backend/internal/runtimeserver/supervision_results.go`；父级摘要「截断 + 指针 + 解引用动作」：`backend/internal/agent/subagent_parent_summary.go:110-147` | `go test ./internal/supervision -count=1` |
| P0-2 交付物保全与失败语义（H10 + H9） | 已实施 | 运行期 `result_summary` 空值落 NULL 而非空字节字面量：`backend/internal/subagentbatch/sqlite_store.go:526-529`；终态携带交付物与 `error_class` 归口：`backend/internal/agent/subagent_batch_coordinator.go:1353,1588,1871`、`backend/internal/supervision/agent_result.go` | `go test ./internal/subagentbatch -count=1`；`go test ./internal/agent -run 'Batch\|Policy\|Result' -count=1` |
| P1-1 批记录运行期一致性（H6 + H7 + H8） | 已实施 | 计数器与任务行一致性：`backend/internal/subagentbatch/state.go`、`types.go`；任务级 `task_deadline` / `last_progress_at` 写侧回填：`backend/internal/agent/subagent_batch_coordinator.go:481`、`backend/internal/agent/scheduler.go`；`child_session_id` 早期绑定（终态仅兜底）：`subagent_batch_coordinator.go:1412-1422,1564-1573` | `go test ./internal/subagentbatch -count=1`；`go test ./internal/agent -run 'TaskProgress' -count=1` |
| P1-2 存储保留与载荷裁剪（H2 + H3） | 已实施（含 1 处方案偏差，见 10.5） | 载荷字节级上限 + 裁剪 stub（默认 256 KiB，`payload_truncated` / `payload_bytes` / `payload_cap` 标记）：`backend/internal/chat/session_event_payload_cap.go`；wake 表 closed 窗口 + 每 agent active 上限：`backend/internal/agentcontrol/retention.go:326`（`PruneAgentWakeEvents`）；对账报告字段与双宿主挂钩：`backend/internal/agentcontrol/reconcile.go`、`reconciler.go`、`backend/cmd/aicli/commands/chat_actor_reconcile.go`、`backend/internal/api/skills/agent_registry_reconcile.go` | `go test ./internal/agentcontrol -count=1`；`go test ./internal/chat -count=1`；`go test ./cmd/aicli/commands -run 'TestLocalRegistryReconcile' -count=1` |
| P1-3 隔离回收与对账（H11 + H15 + H14） | 已实施 | 三方对账（目录 / `git worktree list` / registry，默认 observe）：`backend/internal/isolation/worktree/reconcile.go:128`；apply 冲突预检 + `ApplyConflictError`（`next_action`）：`backend/internal/isolation/worktree/worktree.go:219-226` | `go test ./internal/isolation/worktree -count=1`（含 `TestApplyRefusesMainTreeConflicts`） |
| P1-4 并发上限与权限天花板（H12 + H13） | 已实施 | 进程级准入 `SubagentConcurrencyLimiter`（nil = 不限，配置面驱动）：`backend/internal/agent/scheduler.go:116,218,224-241`；配置透传：`backend/internal/config/agents_normalize.go`、`manager.go`；提权默认阻断并钉住父级：`backend/internal/toolbroker/spawn_agent_permission.go:30` | `go test ./internal/agent -run 'GlobalConcurrency' -count=1`；`go test ./internal/toolbroker -run 'SpawnAgentPermission' -count=1` |
| P2-1 聚合质量（H5） | 已实施 | 结论分组去重 + 冲突检测 + 计数（`Deduplicated` / `Conflicts`）：`backend/internal/agent/subagent_parent_summary.go:30-57` | `go test ./internal/agent -run 'ParentSummary' -count=1` |

### 10.2 本轮定向验证（命令与结果）

| 命令 | 结果 |
| --- | --- |
| `gofmt -l <本轮改动文件>` | 无输出 |
| `go build ./...` | 通过 |
| `go test ./internal/agentcontrol/ -count=1` | ok（含本轮新增 `TestPruneAgentWakeEventsObserveReportsAndEnforceDeletes`、`TestReconcilerFoldsWakePruneIntoReport`） |
| `go test ./internal/api/skills/ -count=1` | ok |
| `go test ./internal/supervision/ -count=1` | ok |
| `go test ./internal/subagentbatch/ -count=1` | ok |
| `go test ./internal/agent -run 'Batch\|Policy\|Result' -count=1` | ok |
| `go test ./internal/agent -run 'TaskProgress\|GlobalConcurrency\|ParentSummary' -count=1` | ok |
| `go test ./internal/toolbroker -run 'SpawnAgentPermission' -count=1` | ok |
| `go test ./internal/isolation/worktree/ -count=1` | ok |
| `go test ./internal/chat/ -count=1` | ok（首次运行的 `TestSQLiteSessionStorageOpenFailsWhenLockPersists` 受 sqlite 锁重试时序影响为**偶发**，立即复跑即通过） |
| `go test ./cmd/aicli/commands/ -count=1` | ok（100.6s；此前 `TestChatSlashCommandCatalogMatchesHandleCommandRoutes` 因新增目录项 `/agent` 未登记到期望表而失败，已随本轮补齐） |
| `go test ./cmd/aicli/commands/ -run 'TestLocalRegistryReconcile' -count=1` | ok（对账 summary 含 `wake_prune_mode=enforce`） |
| `go test ./internal/agent -run 'TestBatchPolicyRefusalKeepsDeliverableAndDoesNotRedispatch\|TestBatchRunningTaskExposesProgressAnchor' -count=1` | ok（**§8.2 验收轮**：§8.2-1 / §8.2-4 新增宿主级用例；`gofmt -l` 无输出） |
| `go test ./internal/isolation/worktree -run 'ReconcileWorktrees' -count=1` | ok（**§8.2 验收轮**：5/5，§8.2-2 孤儿回收对照） |
| `python output/multiagent-review/db_inventory.py` | ok（**§8.3 基线**：`agent_control.sqlite` 580.5 MB / wake 1,266,733 行；`session_runtime.sqlite` 65.3 MB / `session_events` 17,255 行，超 256 KiB 上限 0 行） |
| `python output/multiagent-review/prune_measure.py` | ok（**§8.3 回收实测（副本）**：observe 0 删除；enforce 一遍 −20,000 行；agents 936→936 不变；结果 `output/multiagent-review/prune-measurement.json`） |

**与既有失败的区分**：`cmd/runtime-server`（`TestResolveRuntimeMCPConfigResolutionFallsBackToUserLevel`）与 `internal/aiclipaths`（`TestResolveMCPConfigPathExpandsTildeAndFallsBackToPortableDefault`）在本轮改动前即为红，属环境相关既有失败，不计入本次回归。

**已于 2026-09-21 修复**：两者的“祖先链上没有 `mcp.yaml`”前提在开发机上不成立 —— `%TEMP%` 位于用户主目录之下，向上搜索会按 `docs/aicli/install.md` 的层级（每级先 `.aicli/mcp.yaml`，再 `configs/mcp.yaml`）命中真实的 `~/.aicli/mcp.yaml`，属规定行为而非解析器缺陷。修法：把 portable 兜底断言拆为 `TestResolveMCPConfigPathFallsBackToPortableDefault`（带祖先链前提守卫，干净环境仍断言）、无环境依赖的 `TestResolveMCPConfigPathExpandsTildeToUserHome`，并把 user-fallback 规则抽为 `applyMCPUserFallback` 由 `TestApplyMCPUserFallbackRewritesConventionDefaultOnly` 做确定性覆盖；同时修掉 `scripts/build.ps1` 的 `-ApiBaseUrl` 空串绑定 bug（`[AllowEmptyString()]`），使 `-BuildFrontend` 恢复可用。

**重新打包与上线（2026-09-21 11:25–11:31）**：`pwsh -File ./scripts/build.ps1 -Tools runtime-server -Target windows -BuildFrontend` 端到端跑通（pnpm install → `tsc -b` → `vite build` → `go test ./cmd/runtime-server ./internal/webui` → 暂存内嵌前端 → `go build` → 还原 `backend/internal/webui/dist`），`Invoke-FrontendBuild` 另补了 Node 堆下限（仅在调用者未自带 `--max-old-space-size` 时注入 4096，构建后还原），避免 `tsc -b` 再次触顶（历史 exit 134）。产物 `backend/dist/runtime-server.exe`（33,978,368 B，sha256 `a5e7fd180806c024222e6851de3b2f961c4cb460acbad227af05758f418e9725`）已原地替换 `backend/runtime-server.exe` 并 `start`（pid 16340，`127.0.0.1:58314` + pprof 58315；旧件备份 `backend/runtime-server.exe.bak-20260921-1127`）。线上校验：`/` 与 `frontend/dist/index.html` 逐字节一致，`/assets/events-live-ZfgGaa-j.js` 200 / 44,097 B 且含 O(n) 修复标记 `normalizedSessionId`，`/api/runtime/sessions` 与 `/api/runtime/health` 均 200。

### 10.3 本次取证的产物（已落盘，可作为实施后的对照基线）

| 产物 | 路径 | 用途 |
| --- | --- | --- |
| 汇总建议 | `output/multiagent-review/OPTIMIZATION-SUGGESTIONS.md`（9,850 B） | 17 章节、P0/P1/P2 分级的原始汇总 |
| 调度轴报告 | `output/multiagent-review/opt-dispatch.md`（14,354 B） | 任务派发与批记录证据 |
| 隔离轴报告 | `output/multiagent-review/opt-isolation.md`（11,078 B） | worktree / 并发 / 权限证据 |
| 取回轴报告 | `output/multiagent-review/opt-result.md`（9,191 B） | 结果取回与归档证据 |

### 10.4 取证批次留证（2026-09-20 复核）

- `batch_42d0a52d2a24e401`：3 个只读 hard 任务，终态 `failed`（completed=1 / failed=2），elapsed 230047 ms。
- 批次库：`~/.aicli/sessions/runtime/subagent_batches/session_20260920083941_wHAOpqH5.sqlite`。
- 该批次的监督通知已 ack 收敛（`critical_unresolved: 0`），相关子会话已 `close_agent`。
- 复核读回（只读探针，已随本轮删除）：`subagent_batches.result_summary` 持久类型为 blob 且内容是**合法 BatchSummary JSON**——`b''` 是**运行期写侧**的伪值（已按 P0-2 修 NULL 守卫）；`subagent_tasks.task_deadline` / `last_progress_at` 在该历史批次中仍为 NULL（修复前写入的数据，不回填）。

### 10.5 方案偏差与文档更正

1. **H2 未接 artifact 指针**：`internal/chat` 的写入路径上无 artifact store 可达（`artifact.Store` 挂在 `SessionActor` / `output.Gateway`，不在 runtime store 依赖内），故按最小修复采用「行保留 + 有界 stub（含 `payload_truncated` / `payload_bytes` / `payload_cap` + 头部预览）」而非归档解引用。**§8.3「裁剪后可追溯性：通过 artifact 指针还原」因此不成立**，只能区分「被裁剪」与「空载荷」。
2. **两项既有文档错误已按 10.3 旧注修正**：`supervision-parent-child-control-optimization-plan-20260917.md:481`（「四列写入正常」更正为仅 `started_at`/`finished_at` 成立）与 `task-difficulty-model-routing-plan.md:2883`（提权由「预期能力」更正为「默认阻断 + 显式 opt-in」）。
3. **H3 的宿主挂钩是本轮补齐的最后缺口**：`PruneAgentWakeEvents` 此前无生产调用者，对账只按 30 天身份窗口清理 wake 行；现由 CLI 与 API 双宿主在对账 `Purge:` 钩子中执行，并把 `wake_prune_mode` / `wake_prune_candidates` 折入对账报告与 summary。

### 10.6 剩余项

1. §8.3 存储体量对照：**基线测量与副本回收实测均已完成**（见 §8.3 执行记录）。剩余可选动作：①对**线上库**执行 enforce 回收——机制与量级已在副本上验证（observe 零写入、enforce 有界删除、身份表不变），线上执行属破坏性操作，须按 §8.4 runbook 重复有界遍数并在 checkpoint / `VACUUM` 后再对照体积；②「增量写入量下降 ≥45%」需大载荷流量样本，当前语料全量低于 256 KiB 上限故不可测。§8.2 四项已于 2026-09-20 补做完成（见 §8.2 执行记录）。
2. 裁剪 stub 的 artifact 解引用（10.5-1）留待 `internal/chat` 写入路径接入 artifact store 后补齐。
3. 历史库中的 NULL / `b''` 存量行不回填，仅在后续写入路径生效。
