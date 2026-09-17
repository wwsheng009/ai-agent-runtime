# 子 Agent 调度机制异常分析报告

- 日期：2026-09-17
- 对照方案：`supervision-parent-child-control-optimization-plan-20260917.md`
- 被检对象：`~/.aicli`（`C:\Users\vince\.aicli`）下最近若干会话的执行历史、日志、supervision SQLite 库，以及 `backend/` 对应实现
- 性质：**只读分析**，本报告不修改任何生产代码

---

## 0. 结论摘要（先看这里）

| # | 现象 | 证据 | 判定 |
|---|------|------|------|
| S1 | `supervision_actions` 全库 **0 行**，但同期存在 **11 条 supervision notification** | 库表计数 | **控制环从未闭环一次**（确认） |
| S2 | `supervision_wake_pending` 有 **2 行永久未被认领**（`claimed_at IS NULL`、`claimed_by=''`） | 库表逐行导出 | **wake 被写入但从未被 drain**（确认） |
| S3 | 11 条通知全部 `allowed=[]`、`auto=''`、`recommended=cancel` | 通知字段导出 | **建议动作不在允许集合内 → 自相矛盾，无法执行**（确认） |
| S4 | 子 agent 失败仍发 `subagent.completed` 并关闭会话，无重试 | `backend/internal/agent/scheduler.go:454-465` | **失败被语义掩盖**（确认，高风险） |
| S5 | wake 消费只在 `EventSessionEnd` / 进度检查点被触发，**没有周期性 drain** | `chat_actor_host.go:902`、`chat_actor_progress_check.go` | **触发面过窄 → 陈旧 pending 泄漏**（高置信） |

**核心判断：子 agent 的「派发/执行」链路基本正常，坏的是「回流/监管（supervision）链路」。**
错误与异常确实大量存在，但监管机制对它们 **零反应**——这才是「看起来有不少错误与异常」却没有任何自动处置动作的根本原因。

---

## 1. 方案要求 vs. 实际实现

方案（doc 6.5）要求子 agent 生命周期闭合为：

```
子 agent 终态 → notification → 预算/策略裁决 → supervision_action → 唤醒父 agent
```

实际情况：

```
子 agent 终态 → notification ✅（11 条已落库）
              → 唤醒入队 ✅（pending 已落库）
              → drain ❌（2 行永久未认领）
              → 策略裁决 ⚠️（allowed 为空）
              → supervision_action ❌（全库 0 行）
```

链路在 **第 3 环** 断掉，而不是在产生端。这也解释了为什么 DB 里能看到完整的通知与 pending 记录，却没有任何动作记录。

---

## 2. 证据来源与采样范围

| 来源 | 具体位置 | 用途 |
| --- | --- | --- |
| supervision 数据库 | `~/.aicli/supervision.db`（SQLite） | 通知、唤醒、动作、outbox 的权威状态 |
| 会话库 | `~/.aicli/*.db` 中 `agent_run` / `agent_control_*` 相关表 | 子 agent 运行记录、父子关系 |
| 运行日志 | `~/.aicli/logs/`（约 13MB，最新 2026-09-17 16:01） | 调度失败现场、错误堆栈 |
| 会话目录 | `~/.aicli/sessions/` | 会话元数据、事件流 |
| 实现代码 | `backend/internal/supervision/`、`backend/internal/agent/scheduler.go`、`backend/cmd/aicli/commands/chat_actor_host.go` | 交叉验证「机制是否被接线」 |

采样命令（已执行，产物落盘于临时目录，可复现）：

```powershell
# 表级概览
sqlite3 "...\supervision.db" ".tables"
# 行数统计
#   schema_migrations=2  supervision_actions=0  supervision_completion_outbox=0
#   supervision_wake_pending=2（claimed_at 均为 NULL）
```

---

## 3. 观测事实（症状清单）

以下五条全部来自实际数据，彼此独立、可复核。

### 事实 1：`supervision_actions` 全库 0 行，但通知有 11 条

```
supervision_notifications      : 11 行   ← 产生端正常工作
supervision_wake_pending       :  2 行   ← 唤醒队列有堆积
supervision_actions            :  0 行   ← 消费端一个动作都没执行
supervision_completion_outbox  :  0 行
```

产生端健康、消费端全空，是最典型的「链路中间断裂」形态。

### 事实 2：11 条通知的投递目标全部指向同一个父会话

```
root_scope_id            = session_20260916063943_Sl7ZV2kN
target_parent_session_id = session_20260916063943_Sl7ZV2kN
target_parent_team_id    = (NULL)
subject_kind             = 'agent_session'
subject_id               = session_20260916064642_IBIVijAd
                           session_20260916064643_rR8Iegba
                           session_20260916064649_q56WB5IG
                           ... (共 11 条)
```

父会话 ID 解析正确、`subject_kind` 正确、`subject_id` 能对上真实子会话 —— **寻址逻辑没问题**，问题不在「找不到收件人」。

### 事实 3：通知的候选动作集为空，却推荐了一个不在集合里的动作

```
allowed = []
auto    = ''
rec     = cancel
```

`allowed`（允许自动执行的动词白名单）为空，意味着策略层认为**没有任何动作可以自动执行**；但同时又给出 `rec=cancel` 作为推荐动作。推荐值与白名单自相矛盾 —— 消费者即使被唤醒，也会因为「推荐动作不在允许集合内」而只能降级为人工确认或直接丢弃，永远落不到 `supervision_actions`。

### 事实 4：唤醒队列有 2 条陈旧未认领记录

```
supervision_wake_pending: 2 行
  claimed_at = NULL
  claimed_by = ''
```

时间戳停在 `2026-09-15T22:5x`，此后没有任何 claim/purge。这是「唤醒被写入但从未被取走」的直接物证。

### 事实 5：子 agent 失败路径的状态语义错误

`backend/internal/agent/scheduler.go:454-465`：子 agent 执行失败时，调度器**仍然**发出 `subagent.completed` 事件并关闭该会话。上层因此认为「子任务成功完成」，与事实相反。

后果：
- 父 agent 不会重试，也不会升级为 supervision 事件；
- 失败被静默吞掉，只在日志里留痕；
- 这正是「看起来有不少错误与异常，但主流程不报错」的来源。

### 事实 6：父子作用域全部塌缩到同一个 root

11 条通知的 `root_scope_id` 与 `target_parent_session_id` **全部等于同一个值**
`session_20260916063943_Sl7ZV2kN`，且 `target_parent_team_id` 恒为 `None`。
说明定位「该通知由哪个父级消费」的信息在该批次里退化为常量，
一旦该父会话已结束，通知便永远无人认领——这正是事实 1 中 2 条 pending 长期挂起、
11 条通知状态停在未处理的原因之一。
