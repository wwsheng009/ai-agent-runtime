# 存储写入路径与数据重复分析（2026-11-06）

> 分析对象：`~/.aicli/sessions/` 与 `~/.aicli/data/schedule/` 下的 9 个 SQLite 库。
> 基线数据：2026-11-06 完成 7 天前数据清理与离线压缩后实测（11.2 GB → 5.2 GB）。
> 方法：`dbstat` 逐表/逐索引体积 + 全量行数 + 跨库主键比对 + 写入点代码定位。原始命令见第 10 节。

## 1. 结论

- 5.2 GB 中约 **2.2 GB（42%）是可消除的重复或纯日志**，不影响会话回放与工具配对。
- 最大单点是 `session_messages.preview_json`：148,424 / 148,558 行与 `payload_json` **逐字节相同**，白占 **703 MB**。
- 第二大点是 `agent_control_agent_wake_events`：958,993 行中 **958,731 行是 `upsert`**，表 255 MB + 索引 242 MB，其中每行重复约 200 B 身份字段。
- 第三大点是 `usage_tool_calls.record_json`：653 MB 里约 **71% 是工具输出正文**，而同一份输出已存在于 `session_tool_receipts.message_json`。
- `cache_requests` 与 `usage_requests` 有 93,901 个相同 `llm_request_id`（99.99% 重合），同一 LLM 请求被两套子系统各写一份，约 105 MB 纯重复。
- 库数量可以 **9 → 4**：`session_history`、`session_runtime`（含 cells）、`telemetry`（analytics + background + agent_control + team + schedule）、`artifacts`。

## 2. 实测基线

| 库 | 文件大小 | 主要占用（dbstat） |
|---|---:|---|
| history | 1721.8 MB | `session_messages` 1561.3（payload 729.5 + preview 703.1）、`session_prompt_messages` 141.0 |
| analytics | 1190.2 MB | `usage_tool_calls` 811.3、`usage_requests` 243.3、legacy 表 22.3、索引约 45 |
| runtime | 1026.2 MB | `session_tool_receipts` 479.2、`session_events` 346.3、`cache_requests` 126.7、索引约 36 |
| background | 663.9 MB | `background_job_events` 627.6（其中 output 435.4）、索引 32.6 |
| agent_control | 498.2 MB | `wake_events` 表 255.4、**索引 241.9**、`agents` 0.07 |
| artifacts | 210.7 MB | `artifacts` 118.3（content 97.6）、`memory_entries` 73.3 |
| cells | 36.7 MB | `session_cells` 35.3 |
| team | 0.2 MB | 仅 1 个 team，其余表空 |
| schedule | 0.1 MB | 1 个 task |

行数快照：`session_messages` 148,558；`session_events` 465,985；`session_tool_receipts` 69,664；`cache_requests` 93,914；`usage_tool_calls` 154,395；`usage_requests` 124,751；`wake_events` 958,993；`background_job_events` 360,649；`memory_entries` 54,502。

## 3. 写入路径地图

| 表 | 写入模块 | 写入节奏 |
|---|---|---|
| `history.session_messages` | `internal/chat/sqlite_storage.go:2422` | 每条消息 1 行，同时写 `payload_json` 与 `preview_json` |
| `history.session_prompt_messages` | `internal/chat/sqlite_storage.go` | 每次 prompt 投影 1 组 |
| `cells.session_cells` | `internal/chat/session_cell_store.go` | 每次 cell 修订 1 行 |
| `runtime.session_events` | `internal/chat/session_runtime_store.go`、`session_runtime_store_batch.go` | 每个流式增量/事件 1 行 |
| `runtime.session_tool_receipts` | `internal/chat/session_runtime_store.go` | 每个工具结果 1 行 |
| `runtime.cache_requests` | `internal/chat/session_runtime_store_cache.go` | 每个 LLM 请求 1 行 |
| `analytics.usage_requests` | `internal/usageanalytics/ingest.go` | 同一 LLM 请求再写 1 行 |
| `analytics.usage_tool_calls` | `internal/usageanalytics/ingest_v2.go` | 每个工具调用再写 1 行（含输出预览） |
| `agent_control.wake_events` | `internal/agentcontrol/global_agent_store.go` | 每次 agent 状态 upsert 1 行 |
| `background.background_job_events` | `internal/background/store.go` | 每个 stdout/stderr 分片 1 行 |
| `artifacts.artifacts` | `internal/artifact/store.go` | 大工具结果归档 1 行 |
| `artifacts.memory_entries` | `internal/artifact/store.go` | 每个记忆条目 1 行 |

## 4. 引用型 / 记录型 / 重复型

| 库.表 | 分类 | 被谁引用 | 处理建议 |
|---|---|---|---|
| `history.sessions` | 引用型 | 全部子表、purge 候选 | 保留，作为唯一会话注册表 |
| `history.session_messages` | 引用型 | `seq` 回放、`identity_hash` 回溯 | 保留；`preview_json` 去重 |
| `history.session_prompt_messages` | 派生引用型 | prompt 重放/前缀校验 | 可由 messages 重建，压缩或延迟物化 |
| `cells.session_cells` | 引用型 | `cell_id + revision` | 保留，并入 runtime |
| `runtime.session_tool_receipts` | 引用型 | `tool_call_id` 与消息配对 | 保留，作为工具结果唯一权威 |
| `runtime.session_events` | 记录型（部分引用） | `session_id + seq` 续传 | 按类型分级保留 |
| `runtime.cache_requests` | 重复型 | `llm_request_id` | 与 `usage_requests` 二选一 |
| `runtime.runtime_tool_surface_content` | 引用型 | `fingerprint` 内容寻址 | 保留，已是正确去重模式 |
| `analytics.usage_*` | 记录型 | 仅分析查询 | 只存指标，不存正文 |
| `analytics.legacy_*` | 死数据 | 无 `FROM` 引用 | 直接删表 |
| `artifacts.artifacts` | 引用型 | `artifact_id` | 保留，独立库便于备份 |
| `artifacts.memory_entries` | 引用型 | `source_hash` 去重 | 保留 |
| `artifacts.checkpoints` | 引用型 | `history_hash` | 保留 |
| `agent_control.agents` | 引用型 | `agent_id` 主状态 | 保留，作为 agent 唯一状态表 |
| `agent_control.wake_events` | 记录型 | 无外部引用 | 只记状态跃迁，或 24h 过期 |
| `agent_control.global_mailbox_records` | 引用型 | `message_id` | 保留 |
| `background.jobs` | 引用型 | `job_id` | 保留 |
| `background.job_events` | 记录型 | 无外部引用 | 正文落文件，DB 只留尾部 |
| `team.*` | 引用型 + 结构重复 | team/teammate id | 并入 agent_control |
| `schedule.*` | 引用型 | `schedule_id` | 并入 runtime |

## 5. 重复实测

| 重复项 | 实测证据 | 可回收 |
|---|---|---:|
| `session_messages.preview_json` | 148,424 / 148,558 行 `preview_json = payload_json`，合计 735.2 MB | ~703 MB |
| `wake_events` upsert 日志 | 958,731 / 958,993 为 `upsert`；平均 200.3 B 身份字段/行 | ~490 MB |
| `usage_tool_calls.record_json` 输出正文 | 抽样 20,000 行：`output` 49.6% + `protocol_result` 12.6% + `render_output` 9.1% | ~460 MB |
| `cache_requests` vs `usage_requests` | 93,901 个相同 `llm_request_id`；104.8 MB vs 104.2 MB | ~105 MB |
| `session_events` 流式增量 | `assistant.reasoning` 145.5 MB + `assistant_delta` 53.9 MB | ~150 MB（短保留） |
| `background_job_events.output` | 156,184 条 / 435.4 MB；日志目录仅 190 个文件 3.4 MB | ~300 MB（截断） |
| `analytics.legacy_*` | 35,129 + 4,131 + 1,891 行，无读取点 | ~25 MB |

补充：工具结果与 artifacts 的重复很小。909 个 `tool_call_id` 同时有 receipt 与 artifact，receipt 16.8 MB / artifact 11.8 MB；`artifact_id` 只出现在 1,756 个工具调用中。主要重复不在归档层，而在“回执 / 分析 / 事件”三份投影。

## 6. 优化方案

### 6.1 写入逻辑

1. **单一事实源**：LLM 请求只写 `cache_requests`（或只写 `usage_requests`），另一侧只保留标量指标与外键；工具结果只写 `session_tool_receipts`，analytics 只存 `tool_call_id + outcome + duration + summary + artifact_id`。
2. **preview 只在必要时写**：`sqlite_storage.go:2417` 处当 `preview == payload` 时写 `NULL`。读路径 `sqlite_storage.go:2786` 已用 `len(preview)==0` 回退到完整 payload，且等长场景字节消耗不变。
3. **agent 状态表化**：`wake_events` 的 `upsert` 改为更新 `agent_control_agents` 行；追加日志只保留 `spawn_reserved / active / stale / closed / reclaimed` 等跃迁。
4. **后台输出落文件**：`background_job_events.output` 追加到 `background_logs/<job>.log`，DB 只存 offset/hash/最后 N KB。
5. **流式事件分级**：`assistant.reasoning`、`assistant_delta` 保留 24–48h（用于断线续传），最终 `assistant_message` 与 receipts 长期保留。
6. **索引瘦身**：`wake_events` 6 个索引 → 2 个（`agent_id,id`、`created_at`）；`usage_tool_calls` 6 个索引 → 3–4 个。
7. **删除 legacy**：`analytics_legacy_llm_requests / turns / sessions` 迁移完成后 drop。

### 6.2 保留策略

| 数据 | 建议保留 |
|---|---|
| 会话消息、receipts、artifacts、memory、checkpoints | 跟随会话 TTL（当前 30d） |
| `session_events` 增量类 | 24–48h |
| `session_events` 生命周期类 | 跟随会话 TTL |
| `background_job_events` | 7d 或每 job 上限 |
| `wake_events` | 24h（或只留跃迁） |
| `cache_requests` / `usage_requests` | 30d，二选一 |

### 6.3 库合并（9 → 4）

| 目标库 | 并入 | 理由 |
|---|---|---|
| `session_history` | history | 权威数据，独立备份/恢复 |
| `session_runtime` | runtime + cells + schedule | 同一会话生命周期，写入频率相近 |
| `telemetry` | analytics + background + agent_control + team | 均为追加型遥测，统一按龄裁剪 |
| `artifacts` | artifacts | 内容寻址大对象，独立 VACUUM/备份 |

分两步走：先合并 `team_store → agent_control`（同构表直接复用）、`schedule → runtime`；再评估 `analytics + background + agent_control` 合并后的写锁竞争。若竞争明显，保留 `analytics` 独立，目标退化为 5 库。

## 7. 落地计划

| 阶段 | 内容 | 预期收益 |
|---|---|---|
| P0 | `preview_json` 去重 + legacy 表删除 | ~730 MB |
| P1 | `wake_events` 状态化 + 索引瘦身 | ~490 MB |
| P2 | analytics 去正文、请求单一事实源 | ~560 MB |
| P3 | 流式事件与后台输出分级保留 | ~450 MB |
| P4 | 库合并（team/schedule → 现有库） | 运维复杂度下降 |

每阶段独立可回滚：P0/P2 只改写入投影，P1/P3 需要一次迁移脚本，P4 需要停机窗口。

## 8. 验收指标

- 7 天稳定运行后总占用 ≤ 3.2 GB（当前 5.2 GB）。
- 会话回放、`identity_hash` 回溯、工具配对、resume 续传测试全绿。
- `prune-sessions --older-than 7d` 在无 TTL 阻塞时能命中预期候选。
- 单库写入 p99 不高于当前基线；`compact` 时长不增加。

## 9. 风险

- `preview_json` 置空后，页面预算不足时会回退读完整 payload；需确认 `HistoryPageBytes` 边界测试覆盖。
- `wake_events` 若被外部审计依赖，改为跃迁日志会丢失心跳粒度，需要先确认消费方。
- 库合并会放大单库写锁竞争；SQLite WAL 仍只有一个写者，合并前应做并发压测。

## 10. 附录：实测方法

```bash
# 表/索引体积
sqlite3.exe <db> "select name, round(sum(pgsize)/1048576.0,1) from dbstat group by name order by 2 desc;"

# preview 重复
sqlite3.exe session_history.sqlite "select count(*) from session_messages where preview_json = payload_json;"

# 跨库主键重合（python）
# cache_requests.llm_request_id ∩ usage_requests.llm_request_id = 93,901
# session_tool_receipts.tool_call_id ∩ usage_tool_calls.tool_call_id = 69,662
```
