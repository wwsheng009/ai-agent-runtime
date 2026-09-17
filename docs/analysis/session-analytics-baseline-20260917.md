# 会话分析可复现基线（2026-09-17，实施后）

> 生成方式：`pwsh -File backend/scripts/usage-analytics-baseline.ps1`（同 `-Json`）
> 对照：实施计划 `docs/plan/session-analytics-subagent-reliability-implementation-plan.md` §0.4（实施前基线）
> 说明：本文件是"数据可信性"的回归基线；下次改动后重跑同一脚本并按本文件逐行对比。

## 1. 命令

```powershell
pwsh -NoProfile -File backend/scripts/usage-analytics-baseline.ps1
pwsh -NoProfile -File backend/scripts/usage-analytics-baseline.ps1 -Json
pwsh -NoProfile -File backend/scripts/usage-analytics-archive-legacy.ps1        # 幂等；-Restore 可回滚
```

## 2. 分析库（`~/.aicli/sessions/runtime/usage_analytics.sqlite`）

| 表 | 实施前（§0.4） | 实施后 |
| --- | ---: | ---: |
| `usage_requests` | 0 | 0（待首个新会话写入） |
| `usage_sessions` | 0 | 0（待首个新会话写入） |
| `usage_tool_calls` | 不存在 | 0（schema v2 已建立） |
| `usage_subagents` | 不存在 | 0（schema v2 已建立） |
| `usage_turns` | 不存在 | 0（schema v2 已建立） |
| `analytics_sessions` | 4131（死表） | 0（已归档） |
| `analytics_turns` | 1891（死表） | 0（已归档） |
| `analytics_llm_requests` | 35129（死表） | 0（已归档） |
| `analytics_legacy_sessions` | — | 4131 |
| `analytics_legacy_turns` | — | 1891 |
| `analytics_legacy_llm_requests` | — | 35129 |

归档备份：`~/.aicli/sessions/runtime/backup/20260917-121922/`（三张表 JSON 全量导出）。
`user_version = 2`（`Store.Open` 可写路径写入；只读路径不迁移，符合 §4 批次 1.1 约定）。

> `usage_*` 仍为 0 行是**预期**：本机历史会话产生于采集链路修复之前；判定标准是
> 「完成一个真实会话后 > 0」（计划 §1.1 P0-1、附录 A.2）。

## 3. 会话库（`~/.aicli/sessions/runtime/session_runtime.sqlite`）

| 指标 | 实施前（§0.4） | 实施后 |
| --- | ---: | ---: |
| `session_events` 总数 | 16062 | 16062（历史数据不变） |
| `tool.requested` / `tool.completed` | 0 | 0（历史数据）；**新回合将落盘为 `tool_started`/`tool_finished`（前端回放契约名）** |
| `tool_receipt_recorded` | 1 | 1（历史数据） |
| `session_tool_receipts` | 0 | 0（历史数据）；新回合完成后由终局回执补齐写入 |

事件类型 Top-N（实施后，全量 16062）：

| 类型 | 数量 |
| --- | ---: |
| session_start | 3799 |
| session_end | 3741 |
| session_compact_skipped | 3569 |
| assistant_message | 2815 |
| session_interrupted | 490 |
| mailbox_received | 350 |
| subagent.completed | 271 |
| session_compact_started | 234 |
| context_reconciled | 225 |
| session_compact_completed | 225 |

## 4. 子代理完成事件字段分布（历史 271 行）

| 字段 | 分布 |
| --- | --- |
| `success` | `true`=59、`false`=186 |
| `status` | `idle`=59、`failed`=186、`stopped`=26 |

历史行的双口径（§0.4 G4）在读取侧按 §5.2 映射表归一：`success` 优先、缺失时按
`status` 回退；两者皆缺的行走 `unknown` 单列，不计入失败。**新事件**由两个生产者
（`agent/scheduler`、`api/skills` + `aicli` 的 agent-controller 镜像）经
`usageanalytics.NormalizeSubagentCompletionPayload` 补齐 `success`/`status`/
`completion_reason`/`failure_category`/`attempt`/`max_attempts`/`retry_advice`。

## 5. 判定口径（回归检查表）

1. `usage_tool_calls` / `usage_subagents` / `usage_turns` 三表存在（`user_version=2`）。
2. 一次带工具调用的真实回合后：`session_tool_receipts` ≥ 1 且与 `tool.finished` 数量一致（允许 ±1 在途）。
3. 一次带子代理的真实回合后：`usage_subagents` 出现行，且 `success`/`status` 同时存在。
4. 失败工具的 `usage_tool_calls.outcome=failed` 且 `error_code`/`retryable`/`duration_ms` 至少一项非空。
5. `aicli stats doctor` 的提示列表（G1/G2/G3/G4）应随数据写入逐步消失。
