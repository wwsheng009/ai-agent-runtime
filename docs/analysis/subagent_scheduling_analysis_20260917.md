# 子 Agent 调度与"唤醒-控制"闭环异常分析报告（基于 ~/.aicli 最近会话）

- 分析日期：2026-09-17
- 对照方案：`supervision-parent-child-control-optimization-plan-20260917.md`
- 数据来源：`C:\Users\vince\.aicli`（sessions / logs / supervision SQLite）
- 代码基线：`E:\projects\ai\ai-agent-runtime\backend`
- 分析性质：**只读分析，不修改任何生产代码**
- 结论：**子 Agent 的"唤醒-控制"闭环整体未生效**。子 Agent 能被正常 spawn 并运行，派发/执行链路基本正常；坏的是**回流 / 监管链路**——父侧监督控制链路（lifecycle inbox → wake_pending → ActionService）全程空转，导致子 Agent 的失败被静默吞掉、父 Agent 永远收不到需要处理的子 Agent 事件。
- 严重级别：**高**（监管对被观测到的大量错误 / 异常零反应）

> 说明：本报告已合并此前同日产出的两份重复分析（`subagent_scheduling_analysis_20260917.md` 与 `subagent-scheduling-bug-analysis-20260917.md`），结论、证据、假设与建议均已去重归一，重复文件已删除，仅保留本文件。

## 1. 结论摘要

| 编号 | 观测结论 | 证据类型 | 判定 |
| --- | --- | --- | --- |
| S1 | `supervision_actions` 全库 **0 行**，但同期存在 **11 条 supervision notification** | 库表计数 | **控制环从未闭环一次**（确认） |
| S2 | `supervision_wake_pending` 有 **2 行永久未被认领**（`claimed_at IS NULL`、`claimed_by = ''`） | 库表计数 | wake 记录被写入，但**从未被消费**（确认） |
| S3 | 11 条通知全部 `allowed = []`、`auto = ''`、`recommended = cancel` | 通知内容解析 | **动作策略解析为空**，无任何动作被允许执行（确认） |
| S4 | 子 Agent 失败仍上报 `subagent.completed` 并关闭会话，无重试 | 代码 + 运行日志 | 失败被**伪装成成功完成**（确认） |
| S5 | wake 消费只挂在 `EventSessionEnd` / 进度检查点上，**没有周期性 drain** | 代码定位 | 唤醒通路缺少驱动源（确认） |

核心判断：**子 agent 的派发与执行链路基本正常，坏掉的是回流 / 监管链路**；监管系统对被观测到的大量错误与异常**零反应**。

## 2. 方案要求 vs 实际实现

| 维度 | 方案（`supervision-parent-child-control-optimization-plan-20260917.md`）要求 | 实际实现 | 差距证据 |
| --- | --- | --- | --- |
| 生命周期事件回流 | 子 Agent 生命周期事件写入父会话可消费的 inbox，供父侧监督 | 事件**有**产生（11 条 notification 落库），但作用域全部塌缩到同一个 root 父会话 | S1 / 事实 2、事实 6 |
| 唤醒驱动 | wake 记录由**周期性 drain** 认领并派发，保证"事件必被处理" | `wake_pending` 无周期性消费，认领字段长期为空 | S2 / S5 |
| 动作决策 | ActionService 依据策略解析出**非空**可执行动作并落库 `supervision_actions` | 动作集合恒为空（`allowed=[]`），`supervision_actions` 始终 0 行 | S3 / S1 |
| 失败语义 | 子 Agent 失败应产生失败语义（重试 / 升级 / 至少告警父 Agent） | 失败一律以 `subagent.completed` 收尾并关闭会话，无重试 | S4 |
| 可观测性 | 控制环是否闭环应可自证（动作数、认领数、投递目标可核对） | 无闭环计数，只能事后靠库表反查 | S1 / S2 / S3 |
