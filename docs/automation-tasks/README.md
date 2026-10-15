# 自动化任务（Schedule）设计与实施方案

> 目录：`docs/automation-tasks/`
> 参考系统：`E:\projects\ai\deepseek-harness`（下文简称 **dsh**）的 Schedule 子系统
> 目标系统：本项目 `ai-agent-runtime`（Go，模块根 `backend/`）
> 状态：分析方案（待评审），尚未实施

---

## 1. 术语澄清（本项目内的三个"自动化"概念）

本项目同时存在三种容易混淆的"自动化"，本方案只针对第一种：

| 概念 | dsh 对应 | 本项目现状 | 工具命名 | 语义 |
|---|---|---|---|---|
| **自动化任务 / Schedule** | `packages/schedule`（`schedule` 服务 + `tool-schedule` + `ui-schedule`） | **缺失**（本方案新增） | `schedule_*`（`id` 参数） | 到点后在**原会话**投递一条提醒/后续消息；支持一次性、固定速率、每日、每周、cron |
| **后台作业 / Jobs** | `packages/jobs`（`jobs` / `jobs-local` / `tool-jobs`） | 已有：`backend/internal/background` | 现状 `background_task`/`task_*`；**建议 canonical 改 `job_*`**（`job_id` 参数），`task_*` 保留兼容别名 | 长时进程的托管：启动、排队、监控、取消、恢复；不绑定"到点触发"语义 |
| **工作流 / Workflow** | `packages/workflow`（`workflow` / `tool-workflow` / `workflow-ptc`） | 已有：`internal/team` / `internal/teamsupervisor` / `internal/subagentbatch` | `spawn_team` / `read_task_spec` 等（`task_*` 归团队） | 多 Agent 编排、任务依赖、团队协作 |

结论：**"自动化任务"在本项目（以及 dsh 源码/文档中）唯一指向 Schedule**，不应与后台作业或工作流混用同一命名空间。本方案采用独立的 `schedule_*` 工具族与独立存储/运行时。**注意 `task_*` 词根当前被三个域重载**（作业 / 团队 / 子代理），故建议后台作业改用 `job_*`、`task_*` 词根归还团队——详见 [`background-jobs-tool-surface-and-profile-governance.md`](./background-jobs-tool-surface-and-profile-governance.md) §2。

---

## 2. 结论摘要（TL;DR）

1. **可复用本项目的既有骨架**：新增 `internal/schedule` 领域层，复用现有 SQLite 存储、`internal/mesh` 租约、工具注册与会话投递通道（`internal/background` 的 CAS/状态机原则）。
2. **三平面架构（2026-10-14 v1.1 修订：调度面角色化）**：**调度面 = mesh 可抢占角色**——daemon / runtime-server（可选 aicli）竞争 `schedule-scheduler` 租约，持租约者持定时器；daemon 仍是默认常驻成员与第一优先级；**控制面 = aicli/runtime-server**（create/list/update/delete，不持定时器）；**执行面 = 就近承接**（会话宿主进程内 → 目标节点 → HTTP delegate → embedded → mailbox）。任务不再要求"记得部署守护进程"这一前提；详见 [`mesh-scheduler-federation-design.md`](./mesh-scheduler-federation-design.md)。
3. **六种规则**：`after`、`at`、`every`、`daily`、`weekly`、`cron`（五字段 Vixie 方言），时间语义按 dsh 口径：DST 缺口跳过、重叠取较早时点、停机只补发最近一次、cron 创建时规范化。
4. **投递（就近优先，五级）**：L1 本进程 hub（`SubmitPromptAsync`）→ L2 目标节点（`mesh.Call`）→ L3 HTTP `delegate`（`submit_prompt` → 宿主执行）→ L4 `embedded`（内嵌执行栈自跑 turn，取 session 租约）→ L5 `mailbox`（持久 `trigger_turn` 兜底）。见 [`mesh-scheduler-federation-design.md`](./mesh-scheduler-federation-design.md) §3.3。
5. **一致性**：单写者/CAS（对齐 `docs/background-job-lifecycle.md` 的 `state_version` 约定），`update` 采用完整记录 compare-and-set。
6. **边界**：任务绑定创建会话；被委派子 Agent 的会话拒绝 `create/update`；会话归档需先停止活动任务。默认 `delegate` 需宿主在线；`embedded` 模式可**完全无人值守**（代价：daemon 体积/依赖增大、审批需预置拒绝）。

---

## 3. 文档清单

| 文档 | 内容 |
|---|---|
| [`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md) | 参考系统机制分析、本项目差距、目标架构、数据模型、时间语义、运行时、工具层、权限、Web、**统一路线图（P0–P6）**与验收 |
| [`completeness-review-and-integration.md`](./completeness-review-and-integration.md) | **方案完整性审查**：后端组件清单、四端入口设计（frontend React / micro web / aicli TUI / gateway console）、组件计数、与 workflow/后台作业的配合、缺口补丁 |
| [`data-storage-design.md`](./data-storage-design.md) | **数据存储设计**：SQLite 位置/schema/migration/并发基线/CAS/保留/多实例租约/备份恢复/与其他存储边界 |
| [`frontend-business-chain.md`](./frontend-business-chain.md) | **前端业务链条完整性**：14 项业务场景 × 四端覆盖矩阵、创建/查看/保存/删除/历史/实时闭环、缺口与交付顺序 |
| [`review-of-design-docs.md`](./review-of-design-docs.md) | **设计文档评审**：逐条核验架构假设，2 个阻断级错误（投递机制/会话确认）、5 个重要缺口、逐文档修订清单 |
| [`background-jobs-tool-surface-and-profile-governance.md`](./background-jobs-tool-surface-and-profile-governance.md) | **后台作业工具面与 Profile 治理**：`task_*`→`job_*` 命名区分与描述规范、补 `job_list`/`job_search`、工具分组（tool group）、`background.tools.group` 配置门控、MCP 单入口桥接、profile 收窄 |
| [`tool-surface-inventory-and-naming-review.md`](./tool-surface-inventory-and-naming-review.md) | **工具面梳理与命名规范（全局）**：全量工具清单（作业/计划任务/团队/子代理/文件/监督）、命名问题诊断（`task_*` 三重载）、`job_*`/`team_*`/`subagent_*` 映射表、分批迁移策略 |
| [`scheduler-daemon-design.md`](./scheduler-daemon-design.md) | **调度守护进程设计**：三平面拆分、`cmd/scheduled`、Windows 服务 / systemd、`internal/mesh` 单实例租约、跨进程投递（HTTP + 持久 mailbox）、运维端点与分期 |
| [`embedded-execution-design.md`](./embedded-execution-design.md) | **内嵌执行设计**：delegate/embedded/auto 三模式、复用 `runtimeapi.Handler` 作库（路径 A）vs 抽取 `internal/runtimehost.Executor`（路径 B）、session 租约仲裁、受限 profile、资源/审批约束与分期 |
| [`runtimehost-executor-extraction-plan.md`](./runtimehost-executor-extraction-plan.md) | **执行核抽取分析与方案（路径 B）**：现状核验（Handler 拥有 hub/actor 工厂）、耦合点清单、`internal/runtimehost.Executor` 目标架构与接口草案、E3.0–E3.4 分阶段迁移、风险/测试/验收、开放问题；含两个阻塞缺陷（会话租约域不一致、`mode` 未接线） |
| [`mesh-scheduler-federation-design.md`](./mesh-scheduler-federation-design.md) | **调度面 mesh 化（联盟式调度，v1.1 待评审）**：事故复盘（daemon 不在岗 + store schema v3/v5 不兼容 + 空 URL delegate）、mesh 原语与 bots 多实例先例盘点、driver/deliverer/executor 角色模型、就近投递 L1–L5、能力位/版本握手/任务级 claim/misfire 四机制、S0–S5 切片、配置与接口草案、联动修订清单；**§12 完整性审查**：交付账本（唯一性）、五层多实例状态机、mesh 缺口 M1–M10（含 Renew 竞态与 fencing epoch） |
| [`automation-job-generality-design.md`](./automation-job-generality-design.md) | **自动化作业通用化设计**：`target` 执行上下文（session/workspace/headless）脱离会话绑定、Job=trigger+goal+plan+context+policy+state、作业级执行租约、headless 执行与结果回投、自然语言→规划→执行、J0–J4 分期 |
| [`aicli-executor-reuse-design.md`](./aicli-executor-reuse-design.md) | **aicli 复用执行核（E3.4）详细设计**：两处 actor 工厂逐项差异核验（D1–D8）、`Deps` 差异映射、E3.4-0/a–c 分阶段、契约测试、开放问题 O1–O3；**完整性审查 G1–G7（含阻断级租约模式修正）与架构变更影响面** |
| [`consistency-review.md`](./consistency-review.md) | **文档一致性复核**：链接/编号/术语/分期四类检查、17 处不一致修复清单、各轨分期编号（P/D/E/J/B/G）对齐 |
| [`implementation-status.md`](./implementation-status.md) | **实施状态（P0）**：已交付文件映射、与设计的差异、验证命令、后续阶段 |
| 本文件 | 术语澄清、结论摘要、索引 |

---

## 4. 与现有文档的关系

- [`../background-job-lifecycle.md`](../background-job-lifecycle.md) —— 后台任务生命周期；本方案的**存储与所有权原则**（一任务一 owner、CAS、终态不可逆、显式恢复）直接沿用其结论。
- [`../plan/background-jobs-p0-plan-20260928.md`](../plan/background-jobs-p0-plan-20260928.md)、[`../plan/background-jobs-interactive-terminal-codex-alignment-plan-20261011.md`](../plan/background-jobs-interactive-terminal-codex-alignment-plan-20261011.md) —— 后台任务实施计划，可作为本方案实施节奏的参照。
- [`../plan/frontend-deepseek-harness-gap-list.md`](../plan/frontend-deepseek-harness-gap-list.md) —— 本项目与 dsh 的能力差距清单，本方案是其中"自动化任务"一项的落地。
