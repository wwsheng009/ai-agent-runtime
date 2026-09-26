# 会话 `session_20260926205017_A6z4MvC2` 运行过程分析（子代理监督链路 postmortem）

- **分析对象**：`C:\Users\vince\.aicli\chat-logs\2026\09\26\session_20260926205017_A6z4MvC2\`
- **会话**：`session_20260926205017_A6z4MvC2`（root scope 同名；进程 `aicli-5x` PID 28788，2026-09-26 20:50:17 本地启动）
- **工作区**：`E:\projects\ai\ai-agent-runtime`；provider/model：`commandgo` / `deepseek/deepseek-v4.1-flash`
- **取证时间**：2026-09-26 21:04–21:14（本地，UTC+8；下文时间戳均为 UTC，与 runtime 事件一致）
- **状态**：取证时该会话仍存活——turn 1（写分析报告）已于 13:05:55Z 结束；13:09:12Z 用户发起 turn 2「按报告实施」，13:12:58Z 仍在跑 `go test ./internal/modelrouting/`。

## TL;DR

这个回合里 3 个只读子代理（`lifecycle-scout` / `ux-scout` / `config-scout`）**实际调度、执行、结果落盘全部成功**（261.9s，3/3 succeeded，结果 9.8k–12.1k rune 均完整存在批库 SQLite 中），最终报告 `docs/analysis/commandcode-agents-design-borrowing-20260926.md`（45,647B/271 行）也已产出。但父会话在过程中经历了三重可见性/投递故障，造成明显的时间与 token 浪费：

1. **运行期“账本全空”**：批次 12:55:42.77Z 创建、12:55:43.24Z 三个任务已启动，而父会话在 12:57:06Z / 12:57:45Z / 12:59:28Z 三次 `subagent_status` 全部返回 `0 row(s) ... next_action=finalize`，`list_agents` 返回 `Listed 0 child agent(s)`。模型据此误判“子代理还在排队、父回合结束后才会启动”，放弃等待改为自行取证（12:55:43Z–12:59:59Z 又跑了 ~30 步），并在 12:59:20Z 触发 `tool_loop.exploration_stall_observed`（连续 12 个只读步）。
2. **终态投递失败**：13:00:04.63Z 批次完成，终态事件的 `mailbox_delivery_status=failed`、`mailbox_delivery_error="insert mailbox session event mirror: sqlite3: database disk image is malformed"`；监督通知行 `delivery_state=pending`（`delivered_at=NULL`），`supervision_wake_pending/wake_delivered` 无该 scope 行。父会话只靠自己 13:00:18Z 起轮询 `wait_agent` 才拿到结果。当前 `session_runtime.sqlite`（807MB）`PRAGMA quick_check` 仍报大量 ptrmap 错误。
3. **结果读取“看起来很不可读”**：`subagent_inspect_task` 默认视图对 summary 只给 **512 rune**（`MaxSnapshotResultSummaryRunes=512`）并置 `truncated=true; next_offset=512`，即使传 `max_chars=20000` 也不改变默认页；7 次 inspect 全部 `truncated=true`（`total_runes` 12,087/9,848/12,118），模型分页一次（`offset=512 limit=2000`）后放弃，转而手工核验数字。另有 `wait_agent(batch_id)` 返回“Ready:1”同时 `next_action=target_not_found` 的自相矛盾。

## 1. 会话档案与规模

| 指标 | 值 | 依据 |
|---|---|---|
| 批次 | `batch_ce9e56b661e43c4a`，background，3 任务全成功，耗时 261,865ms | `sessions/runtime/subagent_batches/session_20260926205017_A6z4MvC2.sqlite` |
| 批次窗口 | 12:55:42.766Z → 13:00:04.631Z | 同上（created_at/finished_at） |
| Turn 1 规模 | 61 次 LLM 请求、108 次工具调用、usage_total ≈ 9,053,722 tokens（provider 报告值，命中缓存居多） | `events/runtime-events.jsonl` 聚合 |
| 子代理 token | lifecycle 2,032,866；ux 1,274,238；config 1,487,827（合计 ≈ 4.79M） | `subagent_tasks.result_json.usage_total`；`subagent.completed` 事件 |
| 子代理结果 | result_json 15,212 / 13,088 / 15,070 B；summary 12,087 / 9,848 / 12,118 rune | 批库 + inspect receipt |
| 产出物 | `docs/analysis/commandcode-agents-design-borrowing-20260926.md`（271 行，45,647B，mtime 21:05:14 本地） | 文件系统 + 会话消息 seq171 |
| 观测代价 | 159 次工具调用原始输出 713,426B → 模型可见 95,201B（全部经 `tool.reduced`） | events 聚合 |

## 2. 执行时间线（turn 1）

| 时间 (UTC) | 事件 | 证据 |
|---|---|---|
| 12:50:17.996 | 会话创建；12:51:05.8 turn 1 start（prompt：抓 CommandCode agents 文档 + 对照本地实现 + 报告落 `docs/analysis`） | `session_start` / `sessions` 表 |
| 12:51:14–12:53:52 | step 1 请求 SSE `unexpected EOF`（UPSTREAM_UNAVAILABLE），196ms 后重试成功；随后 fetch 文档、ls/glob 仓库 | `llm.retry`（line 124） |
| 12:54:00–12:55:42 | 模型自读仓库与既有报告；step 7 决定派 3 个只读子代理 | tool 事件 |
| 12:55:42.732 | `spawn_subagents` 工具调用（`call_00_ujKPBN710TbuH5xQOtr25103`） | tool.requested line 1396 |
| 12:55:42.766–.805 | batch 落库；**同一瞬**登记挂起义务（`turn.suspended`，obligation_count=4，parked_at=12:55:42.801）；`subagent.batch.created` | line 1397/1398 + `turn_suspensions` 表 |
| 12:55:43.24 | 三个任务全部 started（**立即启动**，不存在“父回合结束后才调度”） | `subagent.task.started` line 1405-1407 |
| 12:55:43–12:59:59 | 父会话继续自读代码（step 8→25）；期间 12:57:06/12:57:45/12:59:28 三次 `subagent_status` 返回 0 行且 `next_action=finalize`；12:57:15 `list_agents` 返回 0；12:59:20 触发探查停滞告警（连续 12 只读步） | line 1456/1576/2747/1462/2686 |
| 12:59:41 / 12:59:48 / 13:00:04.63 | ux-scout / config-scout / lifecycle-scout 依次完成（3/3 succeeded） | `subagent.completed` line 2750/2828/3046 |
| 13:00:04.639 | 监督通知创建：`n-batch_ce9e56b661e43c4a-106a46e24811470d`，state=terminated，`delivery_state=pending`；**mailbox 投递失败**（session_runtime.sqlite malformed） | supervision.db + `subagent.batch.completed` line 3050 |
| 13:00:18.8→13:00:43.8 | 父会话 `wait_agent(id=batch_...)`：返回 Ready:1 但同时 `target_not_found` 指引 | receipt line 3416 |
| 13:00:48.2→13:01:08.6 | `wait_agent(ids=[3 children])` → Ready:3（12222ms） | receipt line 3417 |
| 13:01:13.5 | `subagent_status(include_results/include_terminal)` → 1 行 terminal，`next_seq=1` | line 3079 |
| 13:01:18.3–13:01:34.7 | 7 次 `subagent_inspect_task`：全部 `truncated=true`、`limit=512`、`next_offset=512`；`status_source=matrix_no_row`；其中一次带 `offset=512 limit=2000` 仍 truncated | receipts line 3419–3425 |
| 13:01:40–13:01:49 | `read_agent_events(child)`：30 事件，`latest_seq=8734`，“at least 200 more events remain” | line 3115 |
| 13:02:51–13:03:49 | 5 次 `append_write` 分段产出报告 | tool timeline |
| 13:03:58–13:04:09 | 3 次 `close_agent`（各 ~3.6–4.0s；返回体里带完整 child 输出） | line 3190/3193/3196 |
| 13:04:20–13:05:06 | 补 view/grep/apply_patch 收尾报告 | tool timeline |
| 13:05:55.07 | turn 1 最终答复（交付报告路径、声明未改生产代码）；工具 receipt 于同一时刻集中落盘（`tool_receipt_recorded`，13:05:55.41–55.52） | `session_messages` seq171 + events |
| 13:09:12 起 | turn 2：用户“按报告实施”；模型开始改 `agentdef`/`agent`/`toolbroker` 并跑测试，取证时仍在进行 | `session_messages` seq172+，events 尾行 13:12:58 |

## 3. 问题清单（按严重度）

### P0-1 终态投递失败：`session_runtime.sqlite` 损坏 → mailbox 镜像写入失败

- **现象**：`subagent.batch.completed` 事件携带
  `"mailbox_delivery_status": "failed"`、
  `"mailbox_delivery_error": "insert mailbox session event mirror: sqlite3: database disk image is malformed"`。
- **当前库状态**：`~/.aicli/sessions/runtime/session_runtime.sqlite`（807MB）`PRAGMA quick_check` 仍报大量
  `Tree 10 page 177315 right child: Bad ptr map entry ...` / `Rowid ... out of order`。
  其余库健康：`agent_control.sqlite`/`session_history.sqlite`/`supervision/supervision.db`/批库 `subagent_batches/<session>.sqlite` 均 `ok`。
- **代码路径**：`deliverTerminalOnce`（`backend/internal/agent/subagent_batch_coordinator.go:2179-2228`）单次投递失败即标记 failed；
  写入落点 `insert mailbox session event mirror`（`backend/internal/chat/session_runtime_store.go:3302-3320`）；
  失败只回写到事件 payload 字段（`agent_mailbox.go:223-233`），不重试、不阻断。
- **后果**：监督通知 `delivery_state` 永远 `pending`（`delivered_at=NULL`）；`supervision_wake_pending`/`supervision_wake_delivered` 无该 scope 记录。
  成功批次按设计不排 auto-wake（`supervision/projection.go:98-119` 仅 critical+action_required 排 wake），
  因此父会话的终态获知完全依赖 (a) UI display mirror（成功）与 (b) 持久 mailbox（失败）——若父回合当时已空闲，将不会被自动唤醒。
- **关联既有结论**：`docs/analysis/sqlite-wal-corruption-and-offline-compaction-20260926.md`（驱动 v0.32.0 WAL/shM 缺陷，已升 v0.35.6 + 提供离线 `aicli storage compact`）。
  本次说明**存量损坏库仍在被运行时打开并使用**，修复驱动并不自动修复旧文件。

### P0-2 运行期监督账本“全空”，且 `next_action=finalize` 主动误导

- **现象**：批次运行中（12:55:43–13:00:04）三次 `subagent_status` 结果：
  - 12:57:06 `subagent status: 0 row(s), 0 pending, 0 terminal; next_action=finalize`
  - 12:57:45 `0 row(s) ... digest: 0 critical_unresolved, 0 action_required; next_action=finalize`
  - 12:59:28 `0 row(s); next_action=finalize`
  同期 `list_agents` → `Listed 0 child agent(s).`
- **机制**：本地 batch 通道只在**终态**向 supervision 投影（`projectTerminalLifecycle` 仅由终态/取消/恢复路径调用，`subagent_batch_coordinator.go:2088-2145`；
  `supervision_execution_runs` 无该 scope 的运行行；通知行是终态事件才插入，`event_seq=1`）。
  `subagent_status` 的行来自 snapshot.Descendants（通知驱动，`toolbroker/supervision_tools.go:1042-1070`），
  于是“运行中”不可见；而 finalize 门（`supervision_tools.go:1027-1037`）本意是“工作可能仍在飞时绝不宣告可收尾”，
  在该路径下反而因“看不到运行态”而输出 `finalize`。
- **模型行为后果**：模型据空账本+空列表得出“调度排队、父回合结束后才启动”的错误结论（原文见用户贴出的过程），此后 4 分钟不再依赖子代理，改为自行补证；12:59:20 触发 `tool_loop.exploration_stall_observed`（连续 12 只读步）。
- **附带不一致**：
  - `list_agents` 走 AgentControl 注册表（`api/runtimeapi/session_runtime_support.go:1506-1550`），`spawn_subagents` 的子会话不在其中 → 永远 0；
  - `subagent_inspect_task` 对 batch 子会话永远 `status_source=matrix_no_row`（通知的 subject 是 **batch id**，不是 child session id，`supervision_tools.go:1081-1099`）。

### P1-1 `subagent_inspect_task` 默认视图 512 rune 截断，与工具描述不一致

- **现象**：7 次 inspect 全部 `truncated=true`；receipt 显示 `limit=512`、`next_offset=512`、`eof=false`，
  `total_runes` 12,087 / 9,848 / 12,118；即便 `max_chars=20000`、`sections=summary` 也一样。
  模型分页一次（`offset=512, limit=2000`）后停止，得出“接口有 512 rune 截断，无法整读”的结论。
- **机制**：summary 的默认分页固定为 `MaxSnapshotResultSummaryRunes = 512`
  （`backend/internal/supervision/agent_result.go:16-18`、`applyReadResultSummaryPage` :451-467），
  与工具描述“byte-bounded by max_chars (default 4000, clamped 256..20000)”不一致；
  512 本来是“快照行预算”，却成了整份交付物的默认视图上限。分页能力存在（H4 已修），但没有“还剩几页/建议下次调用”的 next_action 提示。

### P1-2 `wait_agent(batch_id)` 语义自相矛盾

- 一次调用同时返回：`ready_count=1`、`ready_ids=[batch_ce9e56b661e43c4a]`、`waited_ms=4314`，
  而 `next_action` 是 `target_not_found: ... If it is a dispatch batch id, take a task_id from the receipt tasks[] ... do not re-wait on the same id`；
  `agent.exists=false, status=missing`（receipt line 3416）。
  模型随后改用 3 个 child id 等待（12.2s）才拿到结果。

### P2-1 误报的 route warning（3/3 子代理）

- 三个明确写“只读调查”的任务都带
  `goal_appears_to_require_writes: read_only=true will strip all write-like tools ... set read_only=false for writer tasks ...`。
  判定启发式误报，徒增噪音（read_only 与 goal 均未出错）。

### P2-2 观测成本高

- 子代理事件流极大：`read_agent_events(child, limit=30)` 返回 `latest_seq=8734` 且“至少还有 200 条”；
  若要回溯子代理推理需 290+ 次分页。父 turn1 自身 61 次 LLM 请求 / 108 次工具调用 / ~9.05M 报告 token；
  与子代理合计约 13.8M token 量级（含缓存命中，成本口径以 provider 报告为准）。

### P2-3 首步传输抖动（轻微）

- step 1 SSE `unexpected EOF`（`UPSTREAM_UNAVAILABLE`），retry 196ms 成功；仅为记录。

## 4. 根因小结

1. **数据层**：存量 `session_runtime.sqlite` 内部损坏（已知 WAL 事故，见 2026-09-26 文档），
   运行时未 fail-closed，也未在写失败时升级为可见告警；导致 mailbox 镜像写入失败、监督通知投递永远 pending。
2. **投影层**：本地 batch 的 supervision 投影是“终态-only”，运行态既不进 `supervision_execution_runs` 也不生成通知；
   `subagent_status`/`list_agents`/`subagent_inspect_task` 的状态面向批任务运行期系统性缺失，
   并且 finalize 门在“看不到运行态”时会给出 `finalize` 建议（与设计注释的意图相反）。
3. **交互层**：512 rune 默认页 + `truncated=true` 文案 + 描述承诺 4000/20000，形成“结果不可读”的体验；
   `wait_agent(batch_id)` 的 ready/target_not_found 矛盾进一步破坏模型对工具的信任。
4. **模型侧放大**：模型在 12:57 依据空账本做出错误推断并放弃等待，重复劳动 4 分钟；
   但最终仍完成了报告（说明系统“可用”，只是协同链路不可观测、不可自动收敛）。

## 5. 建议修复（按优先级）

| 优先级 | 修复 | 落点建议 |
|---|---|---|
| P0 | 处理存量损坏的 `session_runtime.sqlite`：备份后离线恢复（`aicli storage compact --target runtime` 会识别损坏并退出码 2，仅报告不改写；需要重建/导出恢复），并让运行时在打开库前做健康/只读探测，写失败时 fail-closed 或降级为可见告警 | `docs/analysis/sqlite-wal-corruption-and-offline-compaction-20260926.md`；`internal/chat/session_runtime_store.go` |
| P0 | 终态投递失败要有重试/退避与**会话内可见**告警（不是只塞 payload 字段）；可复用 `EventSupervisionWakeDeliveryFailed` 的先例（`cmd/aicli/commands/chat_actor_host.go:764-820`） | `internal/agent/subagent_batch_coordinator.go:2179-2228` |
| P0 | 运行期可见性：本地 batch 运行期也投影 in-flight 行（批库已有 running/last_progress_at），或让 `subagent_status` 合并批库运行态；存在未终态 batch 时 finalize 门不得返回 `finalize` | `internal/agent/subagent_batch_coordinator.go`、`internal/toolbroker/supervision_tools.go:1027-1070` |
| P1 | `subagent_inspect_task` 默认页与 max_chars 对齐（或返回 `pages_remaining` + 明确的 next 调用示例）；512 仅保留为“快照行”预算 | `internal/supervision/agent_result.go:16-18,451-467` |
| P1 | `wait_agent(batch_id)` 语义收敛：要么一等公民（返回 batch 终态与 task 结果引用），要么显式拒绝；禁止 ready 与 target_not_found 并存 | `internal/toolbroker` wait 实现 |
| P1 | `list_agents` / inspect 的 matrix 行覆盖 `spawn_subagents` 子会话（注册或兜底到 batch 行/task 行） | `api/runtimeapi/session_runtime_support.go:1506-1550`、`internal/toolbroker/supervision_tools.go:1081-1099` |
| P2 | 修正 route warning 误报（只读 goal 不触发 `goal_appears_to_require_writes`） | 子代理路由告警启发式 |
| P2 | `subagent_status` 返回空行时给出“本地批任务运行态不在矩阵，可用 wait_agent/批库读取”的提示，避免模型误判 | `internal/toolbroker/supervision_tools.go:1042-1070` |

## 6. 证据索引

- 事件流：`~/.aicli/chat-logs/2026/09/26/session_20260926205017_A6z4MvC2/events/runtime-events.jsonl`
  （关键行：124 retry；1395-1407 spawn/挂起/启动；1455-1463 空账本；1576 digest 空；2686 停滞告警；2747 空账本；2750/2828/3046 子代理完成；3050 投递失败；3066/3072 wait；3079 terminal；3085-3115 inspect/read；3190-3196 close；3374-3425 tool receipt，含 `limit=512/next_offset=512/total_runes`）
- 调试日志：`.../debug/debug.log`（runtime-event render、llm-debug 请求/结束）
- HTTP dump：`.../http/001..131_*_provider_wrapper.json`
- 批库：`~/.aicli/sessions/runtime/subagent_batches/session_20260926205017_A6z4MvC2.sqlite`
  （`subagent_batches` 1 行 completed 3/3；`subagent_tasks` 3 行 succeeded；`turn_suspensions` 1 行 obligation=[batch+3 child]）
- 监督库：`~/.aicli/sessions/runtime/supervision/supervision.db`
  （`supervision_lifecycle_notifications` 1 行：`n-batch_ce9e56b661e43c4a-106a46e24811470d`，`delivery_state=pending`，`delivered_at=NULL`；`supervision_wake_pending`/`supervision_wake_delivered` 该 scope 0 行）
- 历史库：`~/.aicli/sessions/session_history.sqlite`（root session `state=active`，message_count=191；turn1 结束 seq171；turn2 起点 seq172）
- 关键代码：
  - `backend/internal/chat/session_runtime_store.go:3302-3320`（mailbox 镜像写入失败点）
  - `backend/internal/agent/subagent_batch_coordinator.go:1952-1989,2088-2145,2179-2228`（终态事件、终态-only 投影、单次投递）
  - `backend/internal/toolbroker/supervision_tools.go:1027-1070,1081-1108`（finalize 门、状态摘要、matrix_no_row）
  - `backend/internal/supervision/agent_result.go:16-18,305-319,451-495`（512 默认页、max_chars 语义）
  - `backend/internal/supervision/projection.go:98-119`（仅 critical+action_required 排 wake）
  - `backend/cmd/aicli/commands/chat_actor_host.go:451-604`（本地 batch 投影器与 wake drain）
  - `backend/internal/api/runtimeapi/session_runtime_support.go:1506-1550`（list_agents 数据源）

## 附注

- 取证时（21:14 本地）会话仍在 turn 2 中运行（PID 28788），因此本报告只对 turn 1 做完整归档，turn 2 仅有边界记录。
- 分析用临时脚本/摘录位于仓库 `.analysis-tmp/`，分析完成后已清理；如需复核可按上文路径重放。
