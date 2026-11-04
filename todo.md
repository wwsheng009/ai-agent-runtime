# TODO

> 基线：`8af08c82`（2026-11-04）
>
> 依据：
> - `docs/plan/parent-turn-finalization-and-late-result-hardening-plan-20261103.md` §10 / §11
> - `docs/plan/implementation-execution-plan-20261103.md` §10
>
> 口径：主链路 P0 已完成。本文件只列**尚未完成**的事项，不把已完成能力、可选增强或已接受边界重新算成缺口。
>
> 当前剩余：**5 项**，其中真正的功能缺口 **1 项**（P1-1）；其余为兼容决策、验收、观测和测试债。
>
> 维护规则：新增 TODO 必须来自方案文档的未完成项、真机验收失败或明确的线上风险；新增能力前先更新设计文档，不直接扩 TODO。

## 总览

| ID | 项目 | 类型 | 优先级 | 状态 |
| --- | --- | --- | --- | --- |
| P1-1 | ACP 非终局响应 / continuation 协议 | 协议 + 功能 | P1 | 未开始，需协议决策 |
| P1-3 | 无 identity 的 legacy / approval wake 收口 | 兼容 + 策略 | P1/P2 | 需决策 |
| V-1 | `job_start(finalization_policy="required")` 真机 ConPTY 验收 | 验收 | P0 验收 | 未执行 |
| P2-1 | terminal output drain 的版本化吸收/丢弃指标 | 可观测性 | P2 | 部分完成 |
| P2-2 | Windows/PTY 与高负载 timing 测试稳定性 | 测试债 | P2 | 已知环境问题 |

---

## P1-1 ACP 非终局响应 / continuation 协议

状态：未开始。当前安全依赖是“ACP 不接自动 wake consumer”，不是协议本身已经表达 parked turn。

现状：

- ACP `PromptResponse{StopReason: EndTurn}` 仍只表示本次同步 request 结束；
- 逻辑 turn 可以处于 `parked`，但 ACP transport 没有“挂起但未终局”的响应语义；
- late child/job result 不会产生第二个 final response，因为 ACP 当前没有接入自动 resume；
- 一旦未来把 ACP 接到 wake consumer，现有传输语义不足以表达同一 turn 的 continuation。

完成标准：

- [ ] 做出协议决策：新增非终局响应，或明确“EndTurn 后由显式 resume request 继续同一 turn/episode”；
- [ ] 保证同一逻辑 turn 不会产生两个 ACP final response；
- [ ] continuation 必须携带或可反推 `turn_id` / `episode_id`，不能只按 session/actor idle 路由；
- [ ] 增加 ACP 回归：late settlement 只通知、不二次 final；显式 resume 只续跑匹配 turn；
- [ ] 把最终决策写入主方案 §9.1 和 §11。

相关代码：

- `backend/cmd/aicli/commands/agent_stdio.go`
- `backend/cmd/aicli/commands/agent_stdio_bridge.go`
- `backend/internal/supervision/resume_admission.go`

---

## P1-3 无 identity 的 legacy / approval wake 收口

状态：需决策。不是当前主链路故障，但语义仍是“按父状态放行”的兼容路径。

现状：

```text
policy 为空 + 非 terminal/settlement
  parent finalized/canceled -> NotifyOnly
  parent busy               -> RetryAdmission
  parent idle/unknown       -> ResumeLegacy
```

具体表现：

- approval projection 的 wake 目前没有 `turn_id` / `episode_id` / policy；
- 它依靠父状态防线避免 finalized 后重新开模型，但不进入 same parked turn + settlement claim 语义；
- 旧 wake 行可能既无 turn 也无 policy，无法证明它属于哪个 episode。

完成标准：

- [ ] 明确 approval 的产品语义：必须自动唤醒父 turn，还是只能通知并由用户/控制面显式跟进；
- [ ] 对无 turn/episode 的非终态 wake 明确默认值，不再依赖“idle 即允许”；
- [ ] 若保留 legacy 行为，增加显式兼容开关和冻结/过期条件；
- [ ] 增加测试：finalized parent、旧无 identity 行、duplicate approval、episode mismatch；
- [ ] 更新 §5.4 判定矩阵和 §8.1 旧记录兼容说明。

相关代码：

- `backend/internal/supervision/approval_projection.go`
- `backend/internal/supervision/projection.go`
- `backend/internal/supervision/resume_admission.go`

---

## V-1 `job_start(finalization_policy="required")` 真机 ConPTY 验收

状态：未执行。公共能力、单测和宿主接线已完成；仍需真实客户端/PTY 证据。

验收场景 A：required job

```text
job_start(finalization_policy="required")
  -> parent.turn.parked
  -> job 执行并 terminal
  -> turn.resumed(phase=started, same turn_id + episode_id)
  -> parent.turn.finalized(same turn_id + episode_id)
```

验收场景 B：detached 对照

```text
job_start(finalization_policy="detached")
  -> parent.turn.finalized（早于 job terminal）
  -> job terminal 只产生 notification
  -> resume_claims = 0
  -> 无第二个 session_start / final response
```

完成标准：

- [ ] 使用真实 ConPTY 和真实 `job_start` 工具调用，不使用 fake manager；
- [ ] 断言 job policy、parent turn/episode、parked/finalized/resumed 事件顺序；
- [ ] 断言 detached 对照不会自动 resume；
- [ ] 将 `verification-summary.json`、日志路径和结论写入主方案 §11.9；
- [ ] 如果失败，只修真实缺口，不扩大验收范围。

参考：

- `output/conpty-real-e2e/` 已有父 turn / subagent 验收证据格式
- `cmd/aicli/commands/chat_actor_background_supervision_test.go`

---

## P2-1 terminal output drain 的版本化吸收/丢弃指标

状态：部分完成。`output_drained_at`、`late_output_count`、`late_output_discarded` 已落地，但还不是完整的版本化 drain 账本。

当前已有：

- terminal transition 写入 `output_drained_at`；
- terminal 后到达的 output 累计 `late_output_count` / `late_output_discarded`；
- `TaskOutputResult` 暴露上述字段；
- late output 不重开 obligation，不追加 output event。

仍未完成：

- [ ] 明确每个输出块/结果的版本边界，区分 terminal 前吸收、terminal 时 drain、terminal 后丢弃；
- [ ] 对 batch task 与 background job 使用一致的版本化吸收/丢弃口径；
- [ ] 增加跨 run/replay 的断言，避免只看 manager 内存计数；
- [ ] 将 drain 账本暴露到 supervision metrics/state，而不只挂在单个 `TaskOutputResult`；
- [ ] 明确版本冲突时是丢弃、告警还是阻塞，不允许静默覆盖稳定结果。

相关代码：

- `backend/internal/background/observation.go`
- `backend/internal/background/manager.go`
- `backend/internal/background/output_drain_test.go`
- `backend/internal/background/types.go`

---

## P2-2 Windows/PTY 与高负载 timing 测试稳定性

状态：已知环境问题，不代表功能回归。

已知项：

- `internal/background` 的部分 timing 测试在高负载并行时偶发超时；`-p 1` 串行通过；
- `TestOpenTerminalPTYSurvivesCallerContextCancel` 受当前 Windows/PTY 环境影响；
- 相关失败应与 admission / finalization 逻辑失败分开归因。

完成标准：

- [ ] CI 对受影响包固定串行或提高明确的时间预算；
- [ ] PTY 用例提供明确的 skip 条件，不能无条件吞掉真实失败；
- [ ] 增加环境诊断输出（PTY 可用性、并发负载、超时阶段）；
- [ ] 在方案文档中记录哪些失败是环境性，哪些是逻辑回归。

---

## 已接受边界（不是 TODO）

- 嵌套 child 的 resume prompt 由 durable partial result capsule 重建，不重放未持久化的逐步 tool transcript；child 必须从 workspace / artifact / durable 结果恢复现场。
- 旧 job 没有 origin 时按 detached / notification-only 处理，不猜测父 turn。
- detached job 完成后只产生 durable notification，不自动开启第二个模型 turn。
- 不全局关闭 auto wake，也不无条件等待所有后台作业。
- 当前 ACP 安全依赖“未接自动 wake”；该边界由 P1-1 决定是否改变。

## 已完成基线（不要再加入 TODO）

- P0 identity / policy / Resume Admission / parent finalization gate；
- terminal/settlement/background job 的 admission 收口；
- evidence-aware stale resume claim recovery（`recovered_submitted` / `recovered_unsubmitted` / `recovery_pending`）、claim lease heartbeat、retention；
- FINALIZING durable phase、finalization sequence gate、self-check；
- required background job 的公共能力、resolver 与宿主接线；
- 嵌套 child self-park 与跨重启 batch resume；
- parked/resume observability、progress-only digest、`turn.resumed` phase；
- required / detached 真机父 turn 与 subagent 证据（required job 真机验收除外）。
