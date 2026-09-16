# SSE live 事件通道优化与增强方案（runtime live event channel）

> 状态：**方案阶段（未实施）**。本文只做盘点、定量取证与分批设计，不含代码改动。
> 例外：§9 为已闭环缺陷的复盘（含已落地的代码改动与验证证据），归档于此作为同族回归证据。
> 日期：2026-09-16（本地 +08:00）
> 适用版本：当前仓库 `E:\projects\ai\ai-agent-runtime`（Go module：`backend`，前端 `frontend`）
> 取证基线：`backend/data/runtime/session_runtime.sqlite`（只读采样，见附录 A）
> 关联文档：
> - `docs/plan/ui-event-bridge-drop-hardening.md`（CLI 侧事件桥分级/合并/丢弃加固；本方案 Batch 3 的合帧语义与其对齐）
> - `docs/plan/workspace-chat-streaming-realtime-rendering-plan.md`（前端逐字渲染与提交合帧）
> - `docs/plan/ui-event-bridge-drop-hardening.md` §6.5（web SSE 丢帧暴露——本文 Batch 3 承接）
> - `backend/internal/api/skills/runtime_event_delivery.go`（四通道分类与计数，P0-2 批次 20 落地）

---

## 0. 结论摘要

1. **live 通道本身（`/runtime/stream`）实现质量高**：分页 dump、watch 唤醒、双层 flush、10s 查询限时、指数退避、15s keepalive、慢消费不阻塞 Publish 等都已到位，注释里还记录了历史回归的取证过程。本方案**不建议重写传输层**。
2. **真正的成本与风险在「同一事实被四条通道重复交付、且各自手写白名单」**：
   - 同一份运行时事实经 A（会话事件库）/ B（live-only 旁路）/ C（chat SSE 帧桥）/ D（回合末尾巴）四条独立路径抵达前端，每条各持一份手写名单（`runtime_event_delivery.go:12-38` 自述）。
   - 结果是**构造性双写**：实测全库 payload（38.2MB，快照时点）中约 72% 的字节属于四组成对结构；严格去重可回收约 34%，计入终稿快照家族约 48%（口径与算式见 §2.1 末）。
   - 前端只能靠 `deltaCoordinator` claim 去重、靠 `renderLiveDeltas` 猜测回放/实时——把服务端冗余转移成了客户端算力。
3. **刚修复的 `todo_snapshot` 缺失是本族问题的实例**（生产者视角的字段白名单漏字段），不是孤例；只要「多份手写名单」还在，同类丢字段会继续发生。
4. **本次建议的落地顺序**：Batch 1（去重持久化 + provenance 条件化 + 统一游标）→ Batch 2（事件契约单一真源 + 代码生成 + 门禁）→ Batch 3（服务端合帧/latest-wins/`retry:`/连接指标）→ Batch 4（replay 标记与续传摘要）。

---

## 1. 现状盘点

### 1.1 两条并发 live 传输

| 传输 | 端点 | 实现 | 游标 | 关键机制 |
|---|---|---|---|---|
| 会话运行时流 | `GET /api/runtime/sessions/{id}/runtime/stream` | `backend/internal/api/skills/session_runtime_stream.go` | EventStore 行 `seq`（`after=` 查询参数） | 分页 dump（500/页）、`WatchEvents` 唤醒、500ms 兜底轮询、`live=1` 旁路、15s keepalive、退避重试 200ms→5s、16KB bufio + 双层 flush |
| 直连回合流 | `POST /api/agent/chat` | `backend/internal/api/skills/handler.go`（`sseEmitter` / `writeSSEEventWithEnvelope`） | emitter 连接内 `sequence`（envelope `_event.sequence`） | 每帧 `Emit` → 可选 `persist`（trajectory emitter）→ 写 wire |

前端把两者接线到同一 thread 状态：共享 `deltaCoordinator` 实例在 `workspace-page.tsx:41` 创建、`:126`/`:299` 分发两路（runtime 侧 `use-workspace-live.ts:103/130`；chat 侧 `use-workspace-agent-chat-turn.ts:408` → `stream-handlers.ts:173/208/235`），共享 claim 避免同一段增量渲染两次。

### 1.2 四条交付通道（同一事实的多路径投递）

| 通道 | 判定点 | 内容 | 落盘 |
|---|---|---|---|
| A 会话事件库 | `isPersistedRuntimeEventType`（`handler.go:3924-3942`）/ `shouldPersistRuntimeSessionEvent`（`handler.go:3945-3950`） | 15 类：`tool.requested`/`tool.completed`、`context.profile.injected`、`recall.performed`、`checkpoint_created`、审批、compact 系列、session 生命周期、`agent.reclaimed`、增量打字机四类 | ✅ |
| B live-only 旁路 | `isSessionLiveOnlyRuntimeEvent`（`session_runtime_stream.go:358-365`）、订阅与转发 `session_runtime_stream.go:307-349` | `tool.progress`、`subagent.progress` | ❌ |
| C chat SSE 桥 | `live_tool_stream.go` + 回合末补帧（`handler.go:1928-1948`） | 工具生命周期实时建行 | ✅（经 trajectory emitter 逐帧落盘） |
| D 回合末尾巴 | `buildObservedToolEventPayloadsWithLive`（`handler.go:8010-8017`） | 子代理批量事件（`subagent.*`） | ✅ |

`DeliveryChannelsFor`（`runtime_event_delivery.go:55-77`）是唯一的「类型 → 通道」汇总视图；但**判定源仍是四份独立名单**，函数只是把它们读出来。

### 1.3 帧格式与游标

- 帧头：`event: <name>` + `data: <json>`；**不写 SSE `id:` 行**（`writeSSEEventWithEnvelope`，`handler.go:8510-8520`）。
- 信封：`wrapSSEData` 在载荷外层加 `_event: {name, schema_version: "skill_runtime.sse.v1", timestamp, sequence?}`（`handler.go:8537-8547`）。
- `sseEmitter.Emit`（`handler.go:8472-8478`）：先 `e.sequence++`，若 `persist` 成功返回 `seq > 0` 则用持久化 seq 覆盖。**persist 失败/未配置时该帧携带的是连接内计数器**——与相邻帧的 EventStore seq 不在同一空间，存在非单调风险（见 §3 P0-3）。
- runtime stream 侧帧不带 `_event` 信封，`seq` 由 `ListEvents` 注入到 `payload.seq`（`runtimeEventSeq`，`session_runtime_stream.go:291-300`）。

### 1.4 前端消费

- 自研 `fetch` 流解析器（非 `EventSource`）：`frontend/src/api/runtime/sse.ts:125-203`，只识别 `:`/`event:`/`data:`；8ms 让出预算（`:32`）。
- 连接管理：`use-session-runtime-stream.ts` 重连循环（`:297-330`）、`after = max(本地已消费 seq, 轨迹已回放 seq)`（`:307-310`）、连续 3 次失败才降级（`:28`）、120ms 提交合帧。
- 事件折叠：`lib/thread-state/events.ts` + `events-live.ts`（`chat.sse.*` 桥接帧应用与 `resolveBridgeToolPayload` 归一化）。
- 轨迹恢复：`lib/trajectory/recovery.ts` 自带 `RUNTIME_EVENT_TYPES` 白名单（`:95`）与「observation 是 tool_end 重复来源」的专门判定（`isToolObservation`，`:197-206`；注释 `:172-196`）。

---

## 2. 量化体检（只读采样，2026-09-16）

### 2.1 全库分布

`session_events` 共 **77,066 行 / 38,243,520 字节** payload。按字节占比前 10：

| 类型 | 行数 | 字节 | 占比 | 备注 |
|---|---:|---:|---:|---|
| `assistant_message` | 1,427 | 6,174,595 | 16.1% | 终稿快照（bus 侧完整文本） |
| `chat.sse.chunk` | 25,274 | 6,066,115 | 15.9% | 正文增量（chat SSE 侧） |
| `assistant_delta` | 13,971 | 5,111,405 | 13.4% | 正文增量（bus 侧） |
| `assistant.reasoning` | 8,904 | 4,177,601 | 10.9% | 推理增量（bus 侧） |
| `chat.sse.reasoning` | 17,253 | 4,014,472 | 10.5% | 推理增量（chat SSE 侧） |
| `chat.sse.done` | 54 | 2,823,749 | 7.4% | 回合结束帧（内嵌完整 result payload） |
| `chat.sse.result` | 54 | 2,734,748 | 7.2% | 回合结果帧（与 done 同体量） |
| `chat.sse.tool_end` | 364 | 1,533,277 | 4.0% | 工具完成帧（含完整 arguments/output） |
| `chat.sse.observation` | 325 | 1,018,491 | 2.7% | 工具观测帧（与 tool_end 同源） |
| `agent.reclaimed` | 1,461 | 859,830 | 2.2% | 子代理回收 |

> **口径注（§0「重复」主张的算式）**：四组成对结构 = `chat.sse.chunk`↔`assistant_delta`（11.18MB）、`chat.sse.reasoning`↔`assistant.reasoning`（8.19MB）、`chat.sse.done`/`chat.sse.result`（5.56MB）、`chat.sse.tool_end`↔`chat.sse.observation`（2.55MB），合计 27.48MB ≈ 全库 71.9%；严格去重（每组只保留一侧）可回收 12.88MB ≈ 33.7%；若把终稿快照家族（`assistant_message` 6.17MB 与 `done`/`result` 内嵌 result 5.56MB 互为同文）计入，可回收 ≈ 18.44MB ≈ 48.2%。
> **复跑注记（2026-09-16 14:5x，附录 A 脚本）**：全库已增长至 81,785 行 / 40.82MB，Top10 次序随新会话漂移（`chat.sse.chunk` 升至第 1）；§2.2 单会话样本 2,302 行 / 2,642,069 字节仍逐字节可复现。

### 2.2 单会话样本（行数最多的会话）

`session_20260916133052_DktyYFb3`：**2,302 行 / 2,642,069 字节**。

| 类型 | 行数 | 字节 | 占比 |
|---|---:|---:|---:|
| `chat.sse.tool_end` | 87 | 560,895 | 21.2% |
| `chat.sse.done` | 1 | 488,603 | 18.5% |
| `chat.sse.result` | 1 | 485,749 | 18.4% |
| `chat.sse.observation` | 87 | 381,848 | 14.5% |
| `assistant_delta` | 833 | 301,780 | 11.4% |
| `chat.sse.chunk` | 833 | 248,130 | 9.4% |
| `assistant.reasoning` | 229 | 103,581 | 3.9% |
| `chat.sse.reasoning` | 229 | 67,774 | 2.6% |
| 其余（orchestration/route） | 2 | 3,709 | 0.1% |

**该会话 99% 的字节属于四组「成对重复」**：

| 重复族 | 两侧 | 合计字节 | 占会话 |
|---|---|---:|---:|
| 终稿结果 | `chat.sse.result` ↔ `chat.sse.done` | 974,352 | 36.9% |
| 工具观测 | `chat.sse.tool_end` ↔ `chat.sse.observation` | 942,743 | 35.7% |
| 正文增量 | `assistant_delta` ↔ `chat.sse.chunk` | 549,910 | 20.8% |
| 推理增量 | `assistant.reasoning` ↔ `chat.sse.reasoning` | 171,355 | 6.5% |

### 2.3 双写抽样实证

同一段推理，`seq` 相邻、两条独立行：

```
assistant.reasoning   seq=93641  len=450  {"llm_request_id":"llm_req_df0a969e-…","logical_turn_id":"trace_be2f7909-…", …}
chat.sse.reasoning    seq=93642  len=289  {"content":".","index":29602,"metadata":{…},"reasoning":{"content":…}}
```

生产者侧成因（代码可复核）：

- 增量：`handler.go:7765-7776` 对 `EventTypeReasoning/ToolCall/...` **先 `Emit(streamEventName(type), payload)` 再 `Emit("chunk", payload)`**；两条帧各触发一次 trajectory `persist`（`trajectory_events.go:27-50`）。同时 agent loop 的 bus 事件（`assistant_delta` / `assistant.reasoning`）又走 A 通道落盘（`handler.go:3915`）。
- 终稿：`handler.go:1944-1948` 连续 `Emit("result", resultPayload)` 与 `Emit("done", {…, result: …})`，两帧都落盘。
- 观测：`handler.go:1936-1940` 先发 `tool_end`（观测转写），再发 `observation`（同一批 `resultPayload["observations"]`）；前端 `recovery.ts:197-206`（`isToolObservation`）已专门写逻辑把 observation 当作「重复来源」处理。

### 2.4 wire-only 开销：provenance

`buildSessionRuntimeEventView`（`session_runtime_event_view.go:7-18`）对**每条事件**无条件计算并下发 8 键 `provenance`（`handler.go:10763-10773`），绝大多数为零值：

```json
"provenance":{"profile_context_injected":0,"recall_with_source_refs":0,"profile_resource_refs":[],
"profile_resource_kinds":{},"profile_resource_count":0,"profile_memory_count":0,
"profile_notes_count":0,"profile_resource_labels":[]}
```

- 单帧约 **215 字节**（估算值，JSON 键名+零值）。
- 2,302 帧的 dump ≈ **495KB 纯噪音**（≈ payload 的 19%）。
- 同一函数还被分页端点复用（`session_runtime_handlers.go:698`、`:735`），即每次首屏「窗口拉取 + stream dump」两份都付。
- 该字段不落盘，纯属每次传输的重复计算与重复编码。

### 2.5 下游消费者盘点（停写/去重的影响面）

> 核对方法：全仓 grep + 只读子代理逐条核对（2026-09-16）。目的是回答「谁在读 `chat.sse.*`」，作为 Batch 1 停写增量的影响面清单。

**后端（Go）**

| 消费者 | 读取口径 | 停写影响 |
|---|---|---|
| `backend/internal/api/skills/trajectory_events.go:16` | 唯一生产构造点（`chat.sse.` 前缀常量） | — |
| `backend/internal/chat/session_runtime_store.go` | 按 `session_id`/`seq` 读 `session_events`，不按类型过滤 | 无（只是行数变少） |
| `backend/pkg/skillsapi/client.go` | 仅定义 `StreamEnvelopeMeta.Sequence` 字段，全仓未见读取点 | 无（见 §8 问题 3） |

**前端（消费「从 store 回放的 `chat.sse.*` 行」的模块）**

| 模块 | 用途 | 停写增量后 |
|---|---|---|
| `lib/trajectory/recovery.ts`（`:95` 白名单、`:197-206` observation 去重） | 恢复链路唯一入闸：EventStore 帧 → 轨迹 push，含 `skip(seq)` 游标推进 | 判据已被 bus 侧增量覆盖（`recovery.ts:77-83,134-136`）；风险集中在投影等价性 |
| `lib/trajectory/history-fallback.ts` | `isTrajectoryContentEvent` 判定「会话是否有内容帧」→ 决定历史兜底 | 无（bus 侧增量计入内容帧） |
| `lib/trajectory/export.ts`（`chatSseEventToExportEntry:127-146`） | 把 `chat.sse.*` 转 JSONL 导出行 | **有**：导出产物少掉该前缀行 → 需等价性用例（§7） |
| `lib/trajectory/export-session.ts`（`:74-84`） | 全量导出：分页拉 `chat.sse.*` + 同一转换；无内容帧回退历史投影 | 同上 |
| `lib/trajectory/session-history.ts` | 反向依赖：仅在**没有**内容帧时投影历史为降级轨迹 | 无 |
| `hooks/workspace/use-trajectory-recovery.ts`（`:199-220`） | 驱动方：分页拉取 + 重放进 store | 无（随 recovery 判据） |

**结论**：停写面的新增验证需求只有 2 个导出模块（JSONL 产物对照）；其余 4 个模块由「内容帧判据已覆盖 bus 侧」与「恢复以 bus 侧增量为主」兜住。

---

## 3. 问题清单

### P0 · 结构类

**P0-1 四通道 + 多份手写白名单，字段丢失属结构性问题**

- 后端 A/B/C/D 判定分散在 `handler.go:3924-3950`、`session_runtime_stream.go:353-365`、`live_tool_stream.go`、`handler.go:8010-8017`；`DeliveryChannelsFor` 只是读取者，不是定义者。
- 前端另有 `trajectory/recovery.ts:95`（`RUNTIME_EVENT_TYPES`）、`deltas.getRuntimeDeltaKind`、`events.ts` 分支。
- 实例：`todo_snapshot` 未进入 `protocol_result`（生产者白名单只读顶层 `metadata["todos"]`，真实路径嵌在 `metadata["tool_metadata"]`）。修复见 `backend/internal/agent/tool_runtime_events.go`（本轮已改，未提交）。
- 后果：新增事件类型或结构化字段时，任一处名单漂移的表现都是「前端没反应」，与「本来就没有事件」不可区分（`runtime_event_delivery` 的 `dropped_known/dropped_unknown` 已能兜住一半，但不覆盖生产者侧字段丢弃）。

**P0-2 构造性双写，dump 体积与帧数≈2×**

见 §2.2/§2.3。代价链路：写放大（每增量一次 SQLite insert，`handler.go:3915`）→ store 体积 → dump 时长与首屏解析量 → 轨迹恢复分页次数。前端 `deltaCoordinator` 去重只是把冗余从渲染层挪走，SQLite IO 与网络字节仍在付。

**P0-3 两个游标空间 + 无 SSE `id:` 行**

- runtime stream 游标 = EventStore 行 seq；chat SSE 游标 = emitter 连接内 sequence。
- `sseEmitter.Emit`（`handler.go:8472-8478`）在 persist 失败时保留连接内计数器：重连后该计数器从 1 重新开始，与已持久化 seq（可达 9 万+）混用同一字段 `_event.sequence`。
- 帧头无 `id:`，标准 `Last-Event-ID` 续传不可用；前端 `recovery.ts:5-9` 专门做两个空间的换算。
- 附带收益：补 `id:` 后，未来若把解析器换成 `EventSource` 可白拿断点续传。

### P1 · 性能与成本类

**P1-1 provenance 恒定开销** —— 每帧 ~215B、每次 dump 与每次分页拉取都付（§2.4）。

**P1-2 服务端零合帧** —— 2,302 行 = 2,302 帧 = 2,302 次 JSON marshal。CLI 侧已有成熟的流式合并（`backend/cmd/aicli/commands/chat_runtime_events.go:299-316`：`chatStreamCoalescePendingLimit/ByteLimit`、latest-wins、`streamCoalescedFromKey`），Web 侧未对齐。

**P1-3 live-only 通道慢消费静默丢帧** —— `session_runtime_stream.go:317-335`：cap 64 的 channel，满即丢（`default:` 分支），前端只表现为「进度不动」；无 latest-wins、无丢弃计数回传（`recordRuntimeEventDeliveryLiveForwarded` 只记成功转发量）。

**P1-4 大块单行落盘** —— `chat.sse.done` 单行 488KB、`chat.sse.tool_end` 平均 ~6.4KB/行（87 行 560KB）。单行过大影响 SQLite 页利用与读取放大，dump 首包也更慢。

**P1-5 连接级指标缺失** —— 现有 `SnapshotRuntimeEventDelivery`（`runtime_event_delivery.go:159-191`）只有 A 通道丢弃与 B 通道转发计数；缺活跃连接数、每连接帧数/字节、dump 耗时/页数、keepalive 次数、重试次数。

### P2 · 体验与增强类

**P2-1 回放帧与实时帧不可区分** —— 帧上没有 `replay: true`；前端只能用 `renderLiveDeltas`/`isResponding` 猜（渲染闸门 `use-session-runtime-stream.ts:366`，选项 `:49-50`；`isResponding` 在 `use-workspace-live.ts:98/131-132` 与 `workspace-page.tsx:300`）。

**P2-2 无 `retry:` 帧、keepalive 固定 15s** —— 重连退避完全由前端硬编码（`:28` 的 3 次阈值与循环内退避）；代理 idle timeout 与 keepalive 周期无协商余地。

**P2-3 无续传摘要** —— 断点续传时没有 `: resumed from=N to=M dropped=K` 一类注释帧，用户与排障都无法判断是否丢过窗口。

**P2-4 `tail=N` 缺失** —— 服务端只有 `after`，没有「只要最后 N 条」；首屏依赖前端 tail-first 窗口 + `getReplayCursor` 规避全量 dump（`use-session-runtime-stream.ts:64-79`），属于客户端绕行。

---

## 4. 优化与增强方案

### Batch 1 · 去重与瘦身（低成本高收益，建议先做）

| 落点 | 内容 | 预期收益 |
|---|---|---|
| `backend/internal/api/skills/session_runtime_event_view.go` | provenance 条件化：非 provenance 承载类型（按类型白名单，如 `context.profile.injected`/`recall.performed`）不计算、不下发；承载类型零值字段省略 | dump 字节 −15~20%；省每帧一次聚合调用 |
| `backend/internal/api/skills/handler.go`（trajectory emitter 的 persist 入口）、`trajectory_events.go` | 增量只落一处：保留 bus 侧 `assistant_delta`/`assistant.reasoning` 作唯一回放源，`chat.sse.chunk`/`chat.sse.reasoning` **只走 wire 不落盘** | store 体积 −10~12%（全局约 10MB） |
| `handler.go:1944-1948` | `result` 与 `done` 只落一份（建议保留 `result`；`done` 帧去掉内嵌 result 大字段，仅保结束元信息） | 单会话 −37%（约 0.97MB / 2.64MB） |
| `handler.go:1936-1940` | `observation` 与 `tool_end` 同源时不落盘（或只落 `observation`，`tool_end` 走 wire 补全） | 单会话 −14.5% |
| `handler.go:8510-8520` | 所有帧补 `id: <seq>` 行；`persist` 失败时**不写 `id:`**（宁缺勿假），并保留 `_event.sequence` 兼容 | 游标语义可校验；为 `EventSource` 化留路 |

> 合计预估（按 §2.2 单会话自证数据复算）：store 体积 −45~52%（2.64MB → 约 1.28~1.46MB，取决于保留 `tool_end` 还是 `observation`）；行数/帧数 −50%（2,302 → 约 1,152）；provenance 去噪合 payload 的 ~19%（wire 口径 ~16%）。帧数进一步下探需叠加 Batch 3 合帧（目标 <500 帧，以实测校准）。

> **实施状态（2026-09-16，PR-1 代码已落地，未提交）**：写入侧四项已实现——`chatSSEFrameIsWireOnly`（chunk/reasoning/同源 observation 只走 wire）、`trimChatSSEEventPayloadForStore`（`done` 落盘裁 `result`）、`writeSSEEventFrame`（`id:` 仅在持久化 seq 时下发）、`summarizeRuntimeEventProvenanceIfBearing`（provenance 条件化 + 零值省略）。回归护栏：`trajectory_write_amplification_test.go`（合成回合 wire=15,471B/17 帧 → 落盘 4,495B/3 行，行 −82% / 字节 −71%，门槛 ≥50% / ≥45%）、`session_runtime_event_view_test.go`、`trajectory_events_test.go`（wire-only 帧不带 seq/id）、`export-dedup.test.ts`（新旧导出差异清单）。单会话复算（保留 `tool_end` 口径）：行 −49.9%（2,302 → 1,153）、字节 −40.6%（2.64MB → 1.57MB）。存量数据按前置条件 1 选项 (a) 保留不动；导出差异已固化（新导出不再含 chunk/reasoning/observation 行与 `done.result`，如需导出保留助手正文须独立 PR 纳入 bus 侧内容事件）。

**风险与配套**：轨迹内容帧判据 `isTrajectoryContentEvent`（`frontend/src/lib/trajectory/recovery.ts:134-136`）= `chat.sse.*` ∪ `ASSISTANT_RUNTIME_EVENT_TYPES`，而后者已包含 `assistant_delta`/`assistant_reasoning`/`assistant.reasoning`（`recovery.ts:77-83`），因此**「会话是否有内容帧」的判定不受去重影响**。真正需要验证的是轨迹重放对 bus 侧增量事件的投影路径（turn 归属、reasoning 分块、与终稿的顺序）是否与 `chat.sse.*` 等价——用 `history-fallback.test.ts`（`:75-82` 已覆盖内容帧判定）与 `trajectory-recovery` 用例锁住。

> **前置条件（PR-1 开工前定稿）**
> 1. **存量数据处置**：本次去重只作用于**新写入**；现有 81,785 行 / 40.82MB 中约 34%（≈14MB）为历史冗余。选项：(a) 保留不动（读路径已容忍双份，`recovery.ts` 本就按 observation 去重）；(b) 一次性 compaction（离线 SQL 删除 `chat.sse.chunk`/`chat.sse.reasoning` 与重复 `done`/`observation` 行，须先备份并复算 §2 口径）；(c) 只对超过 N 天的会话做 compaction。**建议 (a) 先行**，Batch 1 上线观察一个发布周期后再按 (c) 收敛；任何删除动作独立 PR + 备份 + 复核。
> 2. **导出等价性**：`export.ts` / `export-session.ts` 的 JSONL 产物会因停写而少掉 `chat.sse.*` 行 → 先补「新旧导出对照」用例（§7），再合入停写改动。
> 3. **前端适配清单**：`id:`/`retry:`/`replay`/`coalesced_from` 在现有解析器（`sse.ts:164-196` 仅认 `:`/`event:`/`data:`）下**静默忽略、不报错**（向后兼容 ✓）；真正启用需前端新增解析，随 Batch 3/4 各自带用例。
> 4. **验收基线**：写放大基线与 ttfB 基线在 PR-1 步骤 1 一次性补齐（§7）。

### Batch 2 · 事件契约单一真源

| 落点 | 内容 |
|---|---|
| `backend/internal/events/`（新增 `contract.go`） | 声明式注册表：`type → {持久化, liveOnly, chatBridge, tailOnly, provenanceBearing, viewFields}`；`isPersistedRuntimeEventType`、`isSessionLiveOnlyRuntimeEvent`、`DeliveryChannelsFor`、`sessionLiveOnlyRuntimeEventTypes` 全部改为从注册表派生 |
| `backend/internal/events/contract_test.go` | 门禁：注册表与 `runtimeobserve` 已知类型目录双向一致；新增类型未登记即失败 |
| 代码生成（`go generate` 或 `make contract`） | 由注册表生成 `frontend/src/types/runtime/event-contract.ts`：类型联合 + 通道表；前端 `trajectory/recovery.ts:95`、`deltas.getRuntimeDeltaKind`、`thread-state/events.ts` 改为引用生成物 |
| `backend/internal/agent/tool_runtime_events_test.go` | 生产者契约测试：对每类带结构化输出的工具，断言 envelope 结构字段必须出现在 `protocol_result`（把 `todo_snapshot` 这类 bug 挡在 CI） |

### Batch 3 · 传输增强

| 落点 | 内容 | 对齐参考 |
|---|---|---|
| `session_runtime_stream.go`（`sendEvents`/`drainEvents`） | 相邻增量行服务端合帧：同类型、同 turn、连续 seq 合并为一帧，带 `coalesced_from` / `coalesced_count`；帧数 −80%+ | `backend/cmd/aicli/commands/chat_runtime_events.go:299-316`、`streamCoalescedFromKey` |
| `session_runtime_stream.go:307-349` | B 通道由「满了就丢」改 **latest-wins**（按 `tool_call_id`/`agent` 归并保留最新值）；丢弃数经 keepalive 注释帧回传（`: drops since=… n=…`） | `backend/cmd/aicli/commands/chat_runtime_events.go:57-65`（`pendingStreams` 合并） |
| `session_runtime_stream.go` 写帧处 | 新增 `retry: <ms>` 帧；keepalive 周期可配（默认 15s 不变） | SSE 规范 |
| `session_runtime_stream.go:41-49` | 支持 `tail=N`（只发最后 N 条，配 `after` 互斥语义） | 现有 `after`/`poll_ms` 风格 |
| `runtime_event_delivery.go` | 连接级指标并入快照：`active_connections`、`frames_sent`、`bytes_sent`、`dump_duration_ms`、`dump_pages`、`dropped_live`、`retry_count` | 既有 `/runtime/status` 键，不新增端点 |

### Batch 4 · 体验收口

| 落点 | 内容 |
|---|---|
| `session_runtime_stream.go` + `buildSessionRuntimeEventView` | 回放帧标记 `replay: true`（初始 dump 与断点补齐的帧），实时帧不带；前端可据此替代 `isResponding` 猜测 |
| 同上 | 断点续传起始帧：`: resumed from=N to=M`（含跨窗口跨度） |
| `handler.go`（emitter） | 收敛为单一 session 级 seq：`persist` 不可用时不再回退连接内计数器，改为该帧不带 `id:`/`sequence`；`chat.sse.*` 与 runtime stream 共用 EventStore seq |

---

## 5. 建议的首个落地切片（PR-1）

范围：**Batch 1 全量 + Batch 2 的注册表雏形**。理由：收益最大、耦合面最小，且能把「去重」与「契约」两件事一次锁住。

1. 先写回归测试锁现状：回放语义（tail-first 建连游标）、双通道去重（`deltaCoordinator` claim）、轨迹恢复（内容帧判据）、**导出等价（`export.ts`/`export-session.ts` 的 JSONL 产物）**四条链路。
2. provenance 条件化（纯服务端，无契约变更）。
3. 去重落盘：`chat.sse.chunk/reasoning` 不落盘；`done` 去内嵌 result；`observation` 与 `tool_end` 二选一落盘。
4. 同步改轨迹内容帧判据并跑 `history-fallback`/`trajectory-recovery` 用例。
5. 补 `id: <seq>` 行 + persist 失败不发假游标。

提交边界建议：1 个 PR 只做「测试锁现状」，1 个 PR 做 2+5，1 个 PR 做 3+4（涉及回放判据，独立评审）。

---

## 6. 风险与回滚

| 风险 | 影响 | 缓解 |
|---|---|---|
| 去重落盘后轨迹回放降级 | 刷新后轨迹行与在线时不一致 | 内容帧判定已被 bus 侧事件覆盖（`recovery.ts:77-83,134-136`），风险集中在**投影等价性**：补「同一 turn 分别由 `chat.sse.*` 与 bus 增量重放，产物一致」的对照用例；`chat.sse.*` 帧在 wire 上保持不变（实发实例见 §9） |
| `id:` 行与既有 `_event.sequence` 并存导致游标双源 | 前端可能误用 | 前端优先 `payload.seq`（现状），`id:` 只作新增能力；阶段 2 再切换 |
| B 通道 latest-wins 改变进度帧语义 | 前端进度条跳变 | 保留 `coalesced_count`，前端可选择展示「已合并 N 帧」 |
| 合帧改变帧序假设 | 轨迹排序错乱 | 合帧仅限同类型、同 turn、**连续 seq**；`coalesced_from` 保留区间下界（沿用 CLI 既有语义） |
| 指标新增字段 | 无（只增字段） | 沿用 P0-2「只增不改」约定 |

回滚面：Batch 1 的三项落盘改动都可用「恢复 persist 调用」单点回滚；provenance 为纯展示字段。

**存量数据与灰度（Batch 1–4）**

| 批次 | 灰度/开关 | 回滚面 |
|---|---|---|
| Batch 1 | 无开关（写入侧裁剪）；合入前跑「影子对比」：同一 mock 回合分别以旧/新写入，断言回放投影一致 | 恢复 persist 调用（三处单点） |
| Batch 2 | 注册表先只读派生（不改行为）；生成物入库 + CI 校验 | 恢复手写名单、删除生成物 |
| Batch 3 | 合帧/latest-wins 按连接可配（默认关 → 灰度开）；`tail=N` 独立开关 | 关开关即回旧路径 |
| Batch 4 | `replay: true` 为纯新增字段；seq 收敛保留兼容期（前端优先 `payload.seq` 不变） | 字段级回退 |

---

## 7. 验收与测试计划

| 层 | 用例 | 覆盖 |
|---|---|---|
| Go 单测 | `session_runtime_stream_test.go`（新增合帧/`tail=N`/`retry:`/latest-wins 丢弃计数） | Batch 3 |
| Go 单测 | `runtime_event_delivery_test.go`（新增连接级指标） | Batch 3 |
| Go 门禁 | `events/contract_test.go`（注册表 ↔ 已知类型目录双向一致） | Batch 2 |
| Go 契约 | `tool_runtime_events_test.go`（结构化字段必须进 `protocol_result`） | Batch 2 |
| Go 集成 | 写放大基线：同一 mock 回合（833 增量 + 87 工具）落盘行数断言下降 ≥50%、字节断言下降 ≥45%（复算见 §4 Batch 1 预估）——**已落地**：`trajectory_write_amplification_test.go` | Batch 1 |
| Go 单测 | `session_runtime_event_view_test.go`：provenance 条件化（承载类型保留、非承载类型省略、零值字段收敛）——**已落地** | Batch 1 |
| 前端单测 | `lib/thread-state/runtime-events.test.ts`、`lib/thread-state/chat-sse-bridge.test.ts`、`lib/trajectory/history-fallback.test.ts`（去重后行为不变；实测 45 用例通过） | Batch 1 |
| 前端单测 | 新增导出对照用例：同一事件序列分别以「旧写入（含 `chat.sse.chunk/reasoning/observation`）」与「新写入（去重后）」导出 JSONL，断言共有行逐字段等价并产出显式差异清单——**已落地**：`lib/trajectory/export-dedup.test.ts` | Batch 1 |
| 前端单测 | `lib/trajectory/history-fallback.test.ts`、`use-session-runtime-stream-recovery.test.tsx` | Batch 1/4 |
| 前端单测 | 解析器兼容用例：`id:`/`retry:` 行不报错、不影响既有帧解析——**已存在**：`api/runtime/sse.test.ts`（「忽略 `id:`/`retry:` 行」用例，实测 177 用例通过） | Batch 1/3 |
| E2E | 首屏：`after=0` 场景 dump 帧数/字节与 ttfB 对比（帧数/字节基线见 §2.2；ttfB 基线需在 PR-1 步骤 1 补测） | Batch 1/3 |

---

## 8. 开放问题（待决策）

1. **回放唯一真源选哪边**：保留 bus 侧 `assistant_delta`/`assistant.reasoning`（语义稳定、有 turn 归属），还是保留 `chat.sse.*`（贴近 wire 形状、前端已有解析）？本文倾向前者。
2. **`observation` 与 `tool_end` 谁落盘**：若未来需要「观测级」增量重放，`observation` 更完整；若只服务 UI 工具行，`tool_end` 足够。
3. **emitter 本地 sequence 是否需要兼容期**：外部消费者是否依赖 `_event.sequence` 的连续性？**取证（2026-09-16）**：`pkg/skillsapi/client.go` 仅定义 `StreamEnvelopeMeta.Sequence` 字段、全仓未见读取点 → 降级为低风险；切换为单一 session seq（Batch 4）前再全仓确认一次即可。
4. **`tail=N` 与 tail-first 窗口是否合并**：前端已有窗口机制，服务端加 `tail=N` 后可考虑下线前端窗口拉取，减少一次全量分页。
5. **`schema_version` 策略**：Batch 1 不升版（未改字段语义，仅减少冗余行）；Batch 3 合帧新增 `coalesced_from` 等字段时升 `skill_runtime.sse.v2`，并保留 v1 兼容期。
6. **代码生成管线落点**：Makefile 当前无 `generate`/`contract` 目标、`package.json` 无 codegen 脚本 → Batch 2 需新增目标 + CI 校验（防生成物与注册表漂移）。
7. **附录 A 脚本落点**：`.tmp/sse-live-audit.py` 位于 gitignored 目录、仓库内不可复跑 → 移入 `scripts/` 并提交（见附录 A 注）。
8. **可选增强是否纳入**：HTTP 压缩（gzip/br）、订阅类型过滤、dump 序列化缓存；建议 Batch 3 后按实测收益评估，不阻塞主干。
9. **导出是否保留助手正文**：去重后 JSONL 只含 `chat.sse.*`（`tool_end`/`result`/`done`），`chunk`/`reasoning` 行与 `done.result` 不再出现（差异清单见 `lib/trajectory/export-dedup.test.ts`）。若要求导出文件与轨迹视图同样保留正文，需在 export 侧纳入 bus 侧 `assistant_delta`/`assistant.reasoning`（或改为导出轨迹投影），独立 PR 评估。

> 状态：1–2 已随 PR-1 按本文倾向落地（回放唯一真源取 bus 侧 `assistant_delta`/`assistant.reasoning`；`tool_end` 落盘、同源 `observation` 只走 wire）；3–9 随对应批次开工前给出结论。

---

## 9. 复盘：刷新续传打字机停摆（缺陷已修复并验证，2026-09-16）

> 本节归档一个**已闭环**的实发缺陷。它不在本方案的交付范围内，但与本方案同族（同一事实多路径投影 + 消费者侧手写判定口径），且正好命中 §6 风险表「投影等价性」一行，故作为回归证据与约束来源记在这里。

### 9.1 现象

在途回合中刷新页面：增量帧持续到达（服务端、连接、解析均正常），但 DOM 正文**停在刷新前的字符数不再增长**；直到回合收尾（定稿写入或历史重放）才整块蹦出。观感即「打字机停摆」。

### 9.2 根因：形状等价导致的错误认领

恢复链路 = 历史同步投影 + live 增量续写。`adoptResumedTurnInThread`（`frontend/src/lib/thread-state/resumed-turn.ts`）负责把服务端在途回合认领到线程末尾那条「尚未定稿的助手消息」，使增量续写同一行。

判定用的是**形状**而非身份（`role === "assistant"` + 无 `runtimeTurnId` + 未置 `streaming`），而历史投影出的**提示基础设施行**（System prompt / 系统提示词卡片）恰好同形：

| 字段 | System prompt 行 | 未定稿助手消息 | 是否相同 |
|---|---|---|---|
| `role` | `assistant` | `assistant` | 相同 |
| `runtimeTurnId` | 无 | 无 | 相同 |
| `streaming` | 未置 | 未置 | 相同 |
| 渲染口径 | **折叠标题**（不渲染 segments 正文） | segments 正文 | **不同** |

后果：整轮增量写进一张永不显示正文的卡片——payload 侧正文涨到约 8k 字符，DOM 侧该卡片稳定在 16 字符（折叠标题文本）。

### 9.3 修复与回归用例

- `resumed-turn.ts` 认领条件新增 `isSystemPromptMessage(tail)` 排除（判定源 `frontend/src/lib/chat-view/project.ts`：`label === "system"` 或 `author === "System context"`）。跳过它后，首帧增量按既有回退路径补建 `turn-<turnId>-assistant` 占位（`frontend/src/lib/thread-state/live-assistant-target.ts` 的 `createInFlightAssistantTarget`），增量落在可见气泡上；已认领同回合的幂等分支不变。
- `frontend/src/lib/thread-state/resumed-turn.test.ts` 新增用例「提示基础设施行不被认领」，覆盖 `label:"system"` 与 `author:"System context"` 两种形态。

### 9.4 验证证据（生产代码状态，无调试埋点）

浏览器探针 `frontend/.tmp/refresh-resume-e2e2.mjs`（gitignored，纯 DOM 断言），verdict：

```json
{"beforeReloadLen":429,"afterMountLen":0,"afterWatchLen":5897,"grewAfterReload":true,
 "bubblesAfter":[{"id":"msg_64396c1bc9e14a6c8c6cbdb11674be56","len":16,"head":"系统提示词system系统提示词"},
                 {"id":"turn-746aea6d-fe16-413f-83f9-279aefaea40c-assistant","len":5881,"head":"Runtime streamstreaming第 13 行：stub 增量输出，用于刷新续传 E2E。"}]}
```

即刷新瞬间 DOM 归零，随后增量持续落到可见气泡（`turn-<turnId>-assistant`，5881 字符）上增长；System prompt 卡片保持 16 字符、未被认领。

门禁（同一状态下）：`npx tsc -b` 0 错；`npm run test` 264 files / 2111 tests 全通过；`npm run lint` 0 error 且 i18n / no-backups / max-lines / message-tokens 四项门禁 OK。

### 9.5 沉淀的约束

1. **「是否可续写」的口径必须全链路一致**：`hasConversationRows`、`isSystemPromptMessage`、`isLiveAssistantMessage`、`adoptResumedTurnInThread` 四处判定必须同步；未来新增任何「基础设施行」（工具卡、审批卡等）时逐一核对，不能只靠形状判定。
2. **与 Batch 4 互补**：若 wire 上携带「本帧属于哪个 turn / 哪段 segment 的续传」显式标记（P2-3 的续传摘要 + replay 标记），这类「靠形状猜目标行」的判定即可退化为身份匹配，从根上消除本族缺陷。
3. **回归夹具**：上述 E2E 探针可复用于任何「刷新后增量不落可见气泡」的回归。

---

## 附录 A · 取证方法（可复现）

脚本：`.tmp/sse-live-audit.py`（只读，`mode=ro` 打开 `backend/data/runtime/session_runtime.sqlite`）。

> **落点待办（可复现性）**：`.tmp/` 被 `.gitignore:68` 忽略，仓库内无法复跑 → 建议移入 `scripts/`（沿用现有 `analyze-*.mjs`/`analyze-*.ps1` 先例）并随文档提交；迁移时保留 `--db` 参数与只读模式。

```sql
-- 按会话排行
select session_id, count(*) c from session_events group by session_id order by c desc limit 8;
-- 单会话类型分布（行数 + payload 字节）
select type, count(*) c, sum(length(cast(payload_json as blob))) b
from session_events where session_id = ? group by type order by b desc;
-- 双写抽样：同类取 seq 相邻两条对照
select seq, payload_json from session_events
where session_id=? and type='assistant.reasoning' order by seq desc limit 2;
```

口径说明：字节数为 `length(cast(payload_json as blob))` 之和，不含 SQLite 行开销与索引；provenance 单帧 215B 为 JSON 字面量估算，未计入 payload 列（该字段不落盘）。
