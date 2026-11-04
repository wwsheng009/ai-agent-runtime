# TODO

> 基线：`a82093fa`（2026-11-04）
>
> 依据：
> - `docs/plan/parent-turn-finalization-and-late-result-hardening-plan-20261103.md` §10 / §11
> - `docs/plan/implementation-execution-plan-20261103.md` §10
>
> 口径：主链路 P0 已完成。本文件只列**尚未完成**的事项，不把已完成能力、可选增强或已接受边界重新算成缺口。
>
> 当前剩余：**0 项**，真正的功能缺口 **0 项**；本轮跟踪的 P0/P1/P2 项已全部收敛。
>
> 维护规则：新增 TODO 必须来自方案文档的未完成项、真机验收失败或明确的线上风险；新增能力前先更新设计文档，不直接扩 TODO。

## 总览

| ID | 项目 | 类型 | 优先级 | 状态 |
| --- | --- | --- | --- | --- |

当前无未完成项。

---

## 已接受边界（不是 TODO）

- 嵌套 child 的 resume prompt 由 durable partial result capsule 重建，不重放未持久化的逐步 tool transcript；child 必须从 workspace / artifact / durable 结果恢复现场。
- 旧 job 没有 origin 时按 detached / notification-only 处理，不猜测父 turn。
- detached job 完成后只产生 durable notification，不自动开启第二个模型 turn。
- 不全局关闭 auto wake，也不无条件等待所有后台作业。
- ACP 明确保持“不接自动 wake”，由显式 prompt + `_meta` continuation 契约承担恢复；该边界不再作为待决策项。

## 已完成基线（不要再加入 TODO）

- P0 identity / policy / Resume Admission / parent finalization gate；
- terminal/settlement/background job 的 admission 收口；
- evidence-aware stale resume claim recovery（`recovered_submitted` / `recovered_unsubmitted` / `recovery_pending`）、claim lease heartbeat、retention；
- ACP explicit continuation contract：`EndTurn` 保持 request-scoped，`_meta.ai-agent-runtime` 暴露 parked/final/turn/episode，显式 prompt 续跑同一 turn/episode；
- approval identity / attention-wake 收口：approval wake 必须携带 parent turn+episode；缺失身份、finalized parent、episode mismatch 均不再按 idle 自动续跑；
- required job 真机 ConPTY 验收：required -> park -> job terminal -> resume same turn/episode -> finalize；detached 对照 notification-only、resume_claims=0；
- versioned output drain ledger：background job / batch task 统一 version/phase/absorbed/drained/late/conflict policy，跨 store reload 持久化并接入 supervision metrics；
- Windows/PTY 与 timing 稳定性：shell 同源 echo fixture、有界诊断轮询、launch deadline 归入 timed_out、`make test-background` 串行入口；
- FINALIZING durable phase、finalization sequence gate、self-check；
- required background job 的公共能力、resolver 与宿主接线；
- 嵌套 child self-park 与跨重启 batch resume；
- parked/resume observability、progress-only digest、`turn.resumed` phase；
- required / detached 真机父 turn 与 subagent 证据（required job 真机验收除外）。
