# Patch Proposal：Turn Digest 运行时 Outbox（记忆写路径恢复）

> 状态：**Phase 1 core + 窗口化读取 + per-window stale + L0→L1→L2 rollup + canonical range invalidation + idle/session-end 调度 + L1 retention/manual purge + SQLite reopen/abrupt-exit/hard-kill E2E 已实现**；真实终端关闭与 shadow 待补
> 日期：2026-11-11
> 关联基线：`03-target-architecture.md`、`04-data-model-and-api.md`、`05-implementation-plan.md`、`06-test-and-acceptance.md`、`13-memory-injection-and-learning-update-plan.md`
> 结论：**先落地“Turn Digest as Outbox”，不以 digest 替代 canonical transcript；Digest 只负责运行时收集、持久化待处理标记和崩溃恢复。**
> 说明：本文是变更提案，不是新的并列设计基线。决策已于 2026-11-11 确认并完成 Phase 1 core 实现。

---

## 0. 结论摘要

当前自动记忆的写路径只在 `SessionManager.Close` 时触发。正常 `/exit` 可以工作，但以下场景不可靠：

- 直接关闭终端窗口；
- 进程被强制结束；
- 崩溃；
- 长会话长期不关闭；
- 只配置 `generation.trigger=session_end`，没有 idle 兜底。

与此同时，memory 设计明确要求：

- **turn 结束不做 LLM 抽取**；
- 自动抽取在 **session-end / idle / both / manual** 执行；
- 读取 canonical transcript 作为证据源；
- 候选必须有 source、hash/revision、审计事件；
- job 必须幂等、可重试、有 watermark。

因此本提案不把 digest 当作“第二份 transcript”，而是增加一个**运行时 Turn Digest Outbox**：

```text
turn end（assistant_message）
  → 写一条轻量 TurnDigest（用户提问 + 最终 assistant 输出 + turn/message 引用）
  → 不调用 LLM，不阻塞用户

session-end / idle / startup recovery
  → 扫描未处理 TurnDigest
  → 调用现有 RunSession，读取 canonical transcript 进行抽取
  → 成功后标记 digest 已处理
  → 按保留策略清理 digest
```

推荐先实现：

1. `SessionActorConfig` 增加运行时 `TurnDigestSink`；
2. `memory_turn_digests` 持久化表（schema v3）；
3. session-end / idle / startup 的 pending digest recovery；
4. 现有 `ChatSessionSource.ReadTranscript` 不变，继续作为 source of truth；
5. 增加测试与可观测性，确认崩溃恢复、幂等和清理。

不在第一阶段：

- 用 digest 直接替代 canonical transcript 做抽取；
- per-turn source revision / stale 语义重构；
- 新的公共 HTTP API；
- 改变读路径注入位置或 prompt cache 契约。

---

## 1. 背景与问题

### 1.1 当前实现

运行时 turn 终态发布：

`internal/chat/actor.go:3722-3754`

```go
if publishTerminal && result != nil {
    messagePayload := map[string]interface{}{
        "turn_id": turnID,
        "content": result.Output,
        "mode":    "snapshot",
    }
    ...
    a.publish(runtimeevents.Event{
        Type:      EventAssistantMessage,
        SessionID: a.id,
        TraceID:   resultTraceID(result, turnID),
        Payload:   messagePayload,
    })
}
```

`startSessionRun` 同时持有：

- `prompt`：本 turn 的用户提问；
- `session`：canonical session；
- `turnID`；
- `result.Output`：最终 assistant 输出；
- `result.Usage`、`result.Steps`、`success`、`interrupted`、`status`。

现有记忆抽取入口：

`cmd/aicli/commands/chat_memory_runtime.go:114-137`

```go
func (h *localChatRuntimeHost) triggerMemorySessionEnd(sessionID string) {
    ...
    _, _ = h.MemoryRuntime.RunSession(ctx, memoryruntime.TriggerSessionEnd, sessionID)
}
```

它只挂在 `SessionManager.Close` 上：

`cmd/aicli/commands/chat_setup.go:716-723`

### 1.2 问题

1. **终端直接关闭时，close hook 不保证执行**，记忆抽取可能永久丢失；
2. **长会话不结束**时，`trigger=session_end` 永远不抽取；
3. 设计文档已警告只配 `session_end` 的风险：

   `13-memory-injection-and-learning-update-plan.md:1202-1206`

   > gateway 长会话可能永远不会 session_end；只配 session_end 会导致永不抽取。

4. 现有 `runtime-events.jsonl` / HTTP artifacts 是调试产物，不能作为可靠的运行时写路径；
5. 单纯依赖 `memory_jobs` 的 pending 记录不够，因为需要保留“哪个 turn 已经结束、用户问什么、最终回答是什么”的上下文，才能在重启后恢复。

### 1.3 需求

- 运行时收集，不做事后日志反推；
- 每 turn 一条，不按 LLM step 收集；
- turn 结束只写轻量数据，不跑 LLM；
- session-end / idle / startup 统一消费；
- 抽取仍以 canonical transcript 为证据源；
- 崩溃、终端关闭后可恢复；
- 不重复抽取，不误伤已有候选；
- 可脱敏、可审计、可清理；
- 不改变读路径和 prompt cache 契约。

---

## 2. 设计约束

本提案必须满足现有 memory 方案：

| 约束 | 来源 | 本提案处理 |
|---|---|---|
| turn-end 不做自动抽取，只做轻量触发/usage | `13` §4.3 | 只写 digest，不跑 LLM |
| 自动抽取在 session-end / idle / both / manual | `03` §7.1、`04` §6 | session-end/idle/recovery 消费 |
| 默认推荐 `both` | `04` §6、`13` §12.5 | 推荐配置改为 `both` |
| 默认从 canonical transcript 读取 | `05` §4 M2 | Phase 1 保持 `ChatSessionSource` 不变 |
| 候选必须有 source、hash/revision、事件 | `04` §3.1/§3.2 | digest 保存 turn/message 引用，抽取仍产出 canonical source |
| 读写解耦，读路径不等待写路径 | `03` §3、`13` §2 | digest 写入不进入读路径 |
| job 幂等、lease、retry、watermark | `04` §3.4、`05` §4 | 复用 `memory_jobs`；digest 增加 watermark |
| 不默认记录完整原始会话到记忆库 | `README` §8 | digest 只存有界、脱敏文本或引用 |
| 变更 schema/scope/删除语义必须先更新文档和测试 | `README` §7 | 本文是提案；通过后回写 `03/04/05/06/13` |

---

## 3. 方案比较

### 3.1 方案 A：只入队 `memory_jobs`

做法：

- turn 结束只写一个 pending `phase1_extract` job；
- 不保存 turn 的用户提问和最终回答；
- session-end/idle 时 worker 读 canonical transcript。

优点：

- 最小改动；
- 完全复用现有 job/lease/watermark；
- 不引入新表。

缺点：

- 无法快速回答“这个 pending 对应哪个 turn、哪个用户问题、哪个最终回答”；
- 若 canonical transcript 被清理或压缩，恢复线索弱；
- 难以做 per-turn 诊断和清理；
- 终端关闭后，pending job 可能仍在，但缺少恢复状态的可读证据。

结论：适合作为降级方案，不作为首选。

### 3.2 方案 B：Turn Digest Outbox（推荐）

做法：

- turn 结束写一条 `memory_turn_digests`；
- 保存 turn/message 引用、用户提问、最终 assistant 输出摘要；
- 抽取仍读 canonical transcript；
- digest 用于 pending 恢复、审计、清理和指标。

优点：

- 运行时收集，符合需求；
- 不改变 source of truth；
- 崩溃/终端关闭可恢复；
- 可按 turn 清理、诊断、统计；
- 不要求改 `MarkSessionSourceChanged` 语义。

缺点：

- 新增表和迁移；
- 需要处理脱敏、保留、session 删除；
- digest 与 canonical transcript 的一致性需要测试。

结论：**Phase 1 推荐方案。**

### 3.3 方案 C：Digest 作为抽取输入

做法：

- 抽取时直接使用 digest 中的用户提问和最终回答；
- 不读 canonical transcript，或只读命中证据。

优点：

- 抽取输入更小；
- 更聚焦 turn 最终结论；
- 减少 tool output 噪声。

缺点：

- 破坏当前 `source_message_ids` / `source_hash` / `source_revision` 语义；
- `MarkSessionSourceChanged` 会把旧候选误标 stale；
- 需要 per-turn source identity、backtrack/compaction 失效规则；
- 需要新 schema 和 source adapter；
- 设计文档需要大幅回写。

结论：作为 **Phase 2 可选方案**，不在本次实现。

### 3.4 方案 D：HTTP / chat-logs 事后回放

不采纳：

- 不是运行时收集；
- HTTP 日志按 LLM step，不是按 turn；
- SSE 需要解析；
- body 可能截断；
- 已有 256 文件 / 64MB 调试保留上限；
- 属于调试证据，不是记忆事实源。

---

## 4. Phase 1：运行时 Turn Digest 收集

### 4.1 挂点

在 `internal/chat/actor.go` 的 `assistant_message` 终态处增加 sink 调用。

`SessionActorConfig` 增加：

```go
// TurnDigestSink 由宿主注入；actor 在 turn 终态窗口写入一条轻量 digest。
// 建议先写 digest 再发布 assistant_message；sink 失败只记录告警，
// 不影响 assistant_message / session_end 发布。
TurnDigestSink TurnDigestSink
```

定义：

```go
type TurnDigest struct {
    SessionID         string
    TurnID            string
    TraceID           string
    Prompt            string
    AssistantOutput   string
    Outcome           string // success | interrupted | error | continuation
    Steps             int
    Usage             *types.TokenUsage
    AssistantStreamID string
    UserMessageID     string
    AssistantMessageID string
}

type TurnDigestSink interface {
    RecordTurnDigest(ctx context.Context, digest TurnDigest) error
}
```

调用位置：

`internal/chat/actor.go:3726-3754` 的终态窗口，使用与 `assistant_message` 发布相同的 `publishTerminal` 条件。建议顺序：

```text
persist session
  → RecordTurnDigest（失败不阻断）
  → publish assistant_message
  → publish session_end
```

如果实现为发布后写入，则必须接受“assistant_message 已落盘、digest 未落盘”的窗口，并在 startup recovery 中把 durable assistant_message 事件作为兜底线索。

```go
if publishTerminal && result != nil && a.turnDigestSink != nil {
    digest := TurnDigest{
        SessionID:       a.id,
        TurnID:          turnID,
        TraceID:         resultTraceID(result, turnID),
        Prompt:          prompt,
        AssistantOutput: result.Output,
        Outcome:         turnDigestOutcome(success, interrupted, status),
        Steps:           resultSteps(result),
        Usage:           result.Usage,
        AssistantStreamID: result.AssistantStreamID,
    }
    // 使用独立短超时 context，避免 turn 取消导致 digest 写入丢失。
    digestCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    if err := a.turnDigestSink.RecordTurnDigest(digestCtx, digest); err != nil {
        logpkg.Warnf("memory turn digest write failed: session=%s turn=%s: %v", a.id, turnID, err)
    }
    cancel()
}
```

注意：

- 只在 `publishTerminal` 为 true 时写，避免被 superseded run 覆盖；
- 使用独立 context，不跟随 turn cancel；
- sink 不得阻塞 turn 终态超过短超时；
- sink 失败不能影响 assistant_message / session_end；
- 只写一次，按 `session_id + turn_id` 幂等。

### 4.2 用户提问与消息 ID

`startSessionRun` 已有 `prompt`：

`internal/chat/actor.go:3125`

但 canonical message ID 可能尚未生成。建议：

1. 用户消息已通过 `preparedPrompt` 写入 session：
   `internal/chat/actor.go:1211-1223`
2. 在 `persistSession` 后，从 `session.History` 中取最后一条 user message 的 `types.MessageID(...)`；
3. assistant 最终消息同理，在 `persistSession` 后取该 turn 的 assistant message ID；
4. 若取不到，允许 `UserMessageID` / `AssistantMessageID` 为空，保留 `turn_id` 和文本，抽取时由 canonical transcript 重新解析；
5. 不要伪造 canonical ID；digest 中的 `turn_id` 是稳定关联键。

### 4.3 写入内容

推荐必填：

```text
session_id
project_key
turn_id
trace_id
outcome
steps
usage_json
created_at
```

推荐条件写入：

```text
user_text           // 有界、脱敏；默认 8KB 上限
assistant_text      // 有界、脱敏；默认 16KB 上限
user_message_id
assistant_message_id
assistant_stream_id
metadata_json       // model/provider/retry/tool refs 等
```

不写入：

- 完整 tool output；
- HTTP request/response；
- reasoning 全量；
- 图片 base64；
- 未经脱敏的 secret。

文本脱敏：

- 复用 `internal/memoryredact`；
- prompt 和 assistant text 在写入前 `Redact`；
- 命中 secret 时记录规则名，不写原文；
- 若产品选择“只存引用”，可配置 `storeText=false`，只保留 message IDs。

### 4.4 中断、续跑、子代理

| 场景 | 处理 |
|---|---|
| normal success | `outcome=success`，assistant_text 为最终输出 |
| interrupted | `outcome=interrupted`，保留 partial output；抽取默认只消费 success，或按配置消费 partial |
| error | `outcome=error`，记录 error class；默认不抽取 |
| continuation / auto turn | `Prompt` 可能为空；标记 `outcome=continuation`，抽取时使用 canonical 最后 user message |
| subagent | 默认 `includeSubagents=false` 时不写；开启时记录 `parent_session_id` / `agent_type`，由抽取过滤 |
| empty output（reasoning-only） | 写 digest 但不作为抽取输入；记录 `outcome=empty` |

### 4.5 幂等

- 唯一键：`(project_key, session_id, turn_id)`；
- 使用 `INSERT ... ON CONFLICT ... DO UPDATE` 更新最终状态；
- 重复 turn / resume / retry 不产生重复 digest；
- 若同一 turn 的输出被修正，更新 text/outcome，保留 `updated_at`；
- digest 写入与 assistant_message 发布同一终态窗口，避免中间 step 写入。

---

## 5. 数据模型

### 5.1 `memory_turn_digests`

建议放在 memory.db，作为 memory 写路径的 outbox 表，schema migration `v3`。

```sql
CREATE TABLE IF NOT EXISTS memory_turn_digests (
    digest_seq      INTEGER PRIMARY KEY AUTOINCREMENT,
    id              TEXT NOT NULL UNIQUE,
    scope           TEXT NOT NULL DEFAULT 'project',
    project_key     TEXT NOT NULL,
    session_id      TEXT NOT NULL,
    turn_id         TEXT NOT NULL,
    trace_id        TEXT,
    parent_session_id TEXT,
    agent_type      TEXT,
    user_message_id TEXT,
    assistant_message_id TEXT,
    message_seq_end INTEGER NOT NULL DEFAULT 0,
    user_text       TEXT NOT NULL DEFAULT '',
    assistant_text  TEXT NOT NULL DEFAULT '',
    text_truncated  INTEGER NOT NULL DEFAULT 0,
    outcome         TEXT NOT NULL DEFAULT 'success', -- success | interrupted | error | continuation | empty
    steps           INTEGER NOT NULL DEFAULT 0,
    usage_json      TEXT NOT NULL DEFAULT '{}',
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    digest_hash     TEXT NOT NULL DEFAULT '',
    digest_revision INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'pending', -- pending | processing | done | failed | skipped | deferred | invalidated
    process_job_id  TEXT,
    last_error      TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    processed_at    TEXT,
    UNIQUE(project_key, session_id, turn_id)
);

CREATE INDEX IF NOT EXISTS idx_memory_turn_digests_pending
  ON memory_turn_digests(project_key, session_id, status, digest_seq);

CREATE INDEX IF NOT EXISTS idx_memory_turn_digests_session
  ON memory_turn_digests(session_id, digest_seq);

CREATE INDEX IF NOT EXISTS idx_memory_turn_digests_created
  ON memory_turn_digests(created_at);
```

说明：

- `digest_seq` 是全局单调递增序号；处理时按 session 过滤并用 `MAX(digest_seq)` 作为水位；
- `digest_hash` / `digest_revision` 在 Phase 1 只作审计/诊断，不参与现有 `MarkSessionSourceChanged` 的 transcript stale 判定；
- `message_seq_end` 记录 turn 结束时的 canonical message 序号，供窗口化读取确定 `[start, end]`；
- `user_text` / `assistant_text` 有界、脱敏；可以通过配置改为只存 message ID；
- `status=deferred` 保留给 Phase 1.5 窗口化读取；`status=invalidated` 用于 backtrack/compaction/删除等显式失效；
- `process_job_id` 便于把 digest 与具体 `memory_jobs` 关联；
- 不保存 tool output、HTTP artifact 或 reasoning 全量。

### 5.2 迁移与版本

- `memorystore` schema migration version 3/4/5：`memory_v3_turn_digests` + `memory_v4_turn_digest_message_seq` + `memory_v5_window_source_identity`；
- `SchemaVersion()` 增至 5；
- `04-data-model-and-api.md` §3 增加表定义；
- `05-implementation-plan.md` M2 增加施工项；
- `06-test-and-acceptance.md` 增加迁移/幂等/恢复测试；
- 回滚：代码关闭 digest 功能后忽略表；迁移为 additive，不删除旧表。

### 5.3 清理策略

- 成功抽取：`status=done`，写入 `processed_at`；
- TTL（建议 `turnDigest.retentionDays=7`）后删除 done 行；
- failed 行保留到 `max_attempts` 耗尽，并暴露指标；
- `deferred` 行不按普通 retention 删除；留给 Phase 1.5 窗口化读取重置；
- session 删除：memory DB 与 session DB 分离，无法依赖 FK 级联；由应用层删除或标记 `invalidated`，避免恢复已删除会话；
- backtrack / compaction：将该 turn 或受影响 turn 标记 `invalidated`；
- 清理动作必须幂等、可审计，写入诊断指标或独立审计；不要写 `memory_events`（该表的 `memory_id` 必须关联 memory entry）。

---

## 6. 处理流程

### 6.1 Session-end

`triggerMemorySessionEnd` 执行顺序调整为：

```text
SessionManager.Close
  → TriggerMemorySessionEnd
  → RunSession(TriggerSessionEnd, sessionID)
  → 成功后 MarkTurnDigestsProcessed(sessionID, watermark)
  → consolidation
```

注意：

- 如果 `RunSession` 因 job 已 succeeded 而跳过，也应把同一个 transcript watermark 对应的 digest 标记 done；
- 如果 `RunSession` 失败，digest 保持 pending/failed，等待 retry；
- 标记 processed 必须在校验 transcript hash/水位之后，避免新 turn 被误标。

### 6.2 Idle

`RunMemoryIdleSweep` 增加 pending digest 发现：

1. `ListSessions` 仍是 source of truth 的 session 选择；
2. 对每个 eligible session，检查是否存在 pending digest；
3. 如果有 pending digest，调用 `RunSession`；
4. 成功后按 watermark 标记 digest；
5. 如果 transcript 没有变化且 job 已成功，只标记 digest done，不重复调 LLM。

### 6.3 Startup recovery

这是解决“终端关闭/崩溃”的关键，但需要明确触发语义。

建议新增：

```yaml
memory:
  generation:
    trigger: both
    turnDigest:
      enabled: true
      recoveryOnStartup: true
      retentionDays: 7
```

启动时：

```text
构造 MemoryRuntime
  → 扫描 status=pending 的 digest（按 project/session）
  → 仅对 closed / stale / orphaned session 或 recoveryOnStartup 允许的 session 执行
  → RunSession → 标记 processed
```

注意：

- 不要对当前正在活跃的 session 在启动时强行抽取；
- 需要 session state / heartbeat / last activity 判断；
- 如果 `generation.trigger=session_end`，startup recovery 仍可作为“崩溃恢复”而不是“常规 generation trigger”，但必须在文档中明确；
- 推荐配置 `trigger: both`，使 idle 与 session-end 都有常规兜底。

### 6.4 `/new`、`/resume`、compaction 边界

Phase 1 至少：

- `/new` / `/resume` 前调用 pending digest flush 或把旧 session 标记为待 recovery；
- compaction 后若 canonical transcript 改变，将受影响 digest 标记 `invalidated` 或更新 watermark；
- 不要求在这些边界同步跑 LLM；可以只写/更新 outbox，由 worker 异步处理。

### 6.5 并发与幂等

- 同一 session 的 digest 处理串行化（session-scoped lock 或 job claim）；
- `memory_jobs` 唯一键 + digest watermark；
- session-end 与 idle 同时触发时，只有一个 worker 执行；
- 成功后用 `processed_at` 和 job watermark 双重标记；
- 失败退避、重试上限、错误可见；
- 新 turn 在抽取期间追加时，digest 保持 pending，下一次处理；不要清理未处理行。

---

## 7. 与现有 memory 设计的契合性

| 设计点 | Phase 1 处理 | 结论 |
|---|---|---|
| turn-end 不跑 LLM | 只写 digest | 契合 |
| 抽取在 session-end/idle/both/manual | digest 只做恢复/触发；RunSession 仍按原触发 | 契合 |
| canonical transcript 作为 source | Phase 1 不改 `ChatSessionSource` | 契合 |
| source_message_ids / source_hash | 抽取仍写 canonical source；digest 只存引用 | 契合 |
| 读路径不等待写路径 | digest 写入不在 context build 路径 | 契合 |
| candidate-only + Consolidation | digest 不直接写 active | 契合 |
| job 幂等/lease/watermark | 复用 memory_jobs；digest 增加水位 | 需小扩展 |
| 隐私/redaction | digest 写入前 Redact + TTL | 需新增实现 |
| schema/API 变更流程 | 新表走 migration v3 + 文档回写 | 需走流程 |
| `/new`/`/resume`/compaction 边界 | Phase 1 至少标记/恢复，不保证同步 flush | 部分契合，需补边界 |
| 只用 session_end 的可靠性 | startup recovery 补足；推荐 both | 需明确配置语义 |

### 7.1 关键结论

**Phase 1 可以把 digest 定义为 outbox/trigger，而不是 transcript；这样不会破坏当前 memory 的 source、stale、revision 和证据链。**

如果直接让 digest 作为抽取输入，则会与以下现有实现冲突：

- `ChatSessionSource.ReadTranscript` 的 canonical 语义；
- `MarkSessionSourceChanged` 的全 session hash/revision 比较；
- `memory_sources.source_hash/source_revision`；
- backtrack/compaction 后的 source 失效；
- `validateCandidates` 对 source_message_ids 的校验。

因此 Phase 2 必须先改设计，不得在 Phase 1 隐式引入。

---

## 8. Phase 2（可选）：Digest 作为抽取输入

如果后续实测证明“整段 transcript 抽取成本过高/噪声过大”，再评估 Phase 2。

### 8.1 需要的新设计

1. `DigestSource` 实现 `memoryruntime.SessionSource` 或新增 `ReadDigestTranscript`；
2. digest transcript 使用稳定的 synthetic/canonical message ID；
3. `Transcript.Hash` 改为 digest set watermark hash；
4. `source_hash` / `source_revision` 改为 per-turn / per-digest；
5. `MarkSessionSourceChanged` 拆分为：
   - 全 session transcript 变化；
   - 单 turn digest 变化；
   - compaction/backtrack 失效；
6. 清理 digest 前确保 source refs 已持久化；
7. 增加配置与迁移；
8. 更新 `03/04/05/06/13` 的 source 语义和测试。

### 8.2 不建议在 Phase 1 做的原因

- 会把“追加新 turn”误判为旧候选 stale；
- 会改变 `memory_sources` 的可追溯语义；
- 会让 compaction/backtrack 的失效规则更复杂；
- 需要新的 source adapter 和 transcript contract；
- 当前最关键的问题是“抽取丢失”，不是“抽取输入太大”。

---

## 9. 代码落点

| 区域 | 文件 | 变更 |
|---|---|---|
| Actor 配置 | `internal/chat/actor.go` | 增加 `TurnDigestSink` / `TurnDigest`；在 `assistant_message` 终态调用 |
| Actor 终态 | `internal/chat/actor.go:3722-3754` | 捕获 `turnID`、`prompt`、`result.Output`、`success/interrupted/status` |
| Chat host | `cmd/aicli/commands/chat_actor_host.go` | 注入 sink；启动恢复；关闭时处理 pending |
| Chat memory runtime | `cmd/aicli/commands/chat_memory_runtime.go` | 构造 digest writer；session-end 后标记 processed；idle/startup 扫描 |
| Runtime API host | `internal/api/runtimeapi/memory_runtime_wiring.go` | runtime-server 侧构造同一 sink；startup recovery |
| Memory store | `internal/memorystore/v2_schema.go` | migration v3：`memory_turn_digests` |
| Memory store | 新文件或 `v2_*.go` | CRUD、watermark、retention、session delete |
| Memory runtime | `internal/memoryruntime/*` | pending digest 查询/处理后标记；Phase 1 不改 `ChatSessionSource` |
| 状态/指标 | `internal/memorystore/v2_metrics.go`、API status | pending/done/failed 计数、写入失败、recovery 次数 |
| 文档 | `03/04/05/06/13` | 评审通过后回写 |

### 9.1 推荐的接口分层

- `internal/chat` 只定义 `TurnDigest` 和 `TurnDigestSink`，不 import `memoryruntime`；
- `cmd/aicli/commands` 或 `internal/memoryruntime` 提供 adapter，实现 sink；
- `internal/memorystore` 提供 `RecordTurnDigest`、`ListPendingTurnDigests`、`MarkTurnDigestsProcessed`、`PruneTurnDigests`；
- `internal/memoryruntime` 提供 pending session 发现和 `RunSession` 调用，不把 digest 直接塞进 `Transcript`；
- Phase 1 不改 `ChatSessionSource.ReadTranscript`。

### 9.2 写入失败策略

- sink 失败只告警，不阻断 turn；
- 可通过 event log / 下一轮 fallback 重建；
- 如果 memory mode=off，sink 不注册，零开销；
- 如果 generation disabled，sink 默认不写；手工恢复/诊断场景应通过显式配置开启，不隐式绕过用户开关。

---

## 10. 配置建议

```yaml
memory:
  mode: on
  generation:
    enabled: true
    trigger: both          # 推荐：session_end + idle
    turnDigest:
      enabled: true
      storeText: true      # false 时只存 message IDs + metadata
      maxUserChars: 8192
      maxAssistantChars: 16384
      recoveryOnStartup: true
      retentionDays: 7
      maxPendingPerSession: 200
```

说明：

- `trigger: both` 是现有推荐默认，不应只配 `session_end`；
- `turnDigest.enabled` 默认随 `generation.enabled`；
- `storeText=false` 适合高隐私模式，只存 ID/水位；
- `recoveryOnStartup` 是新的恢复语义，需在 04/05 中定义；
- `maxPendingPerSession` 防止异常会话堆积；
- 所有新字段必须有默认值和校验。

---

## 11. 测试与验收

### 11.1 单元/集成测试

- `TestTurnDigestWrittenOncePerTurn`：每个 turn 一条，不按 LLM step；
- `TestTurnDigestNotWrittenForIntermediateSteps`；
- `TestTurnDigestIdempotentOnRetry`：同 session/turn 不重复；
- `TestTurnDigestRedactsSecrets`；
- `TestTurnDigestTextBounds`；
- `TestTurnDigestSessionEndMarksProcessed`；
- `TestTurnDigestIdleRecovery`；
- `TestTurnDigestStartupRecovery`；
- `TestTurnDigestDoesNotDuplicateCandidate`：session-end + idle 并发；
- `TestTurnDigestFailedExtractionRetries`；
- `TestTurnDigestPrunedAfterSuccess`；
- `TestTurnDigestInvalidatedOnSessionDelete`；
- `TestTurnDigestInvalidatedOnBacktrack`；
- `TestTurnDigestOffModeNoWrite`；
- `TestTurnDigestTriggerSessionEndRecovery`；
- `TestTurnDigestTriggerBoth`。

### 11.2 回归/验收

- `off`：不建 digest 表、不写、不跑 LLM；
- `shadow`：写 digest，抽取候选，不注入；
- `on`：写 digest，session-end/idle/startup 抽取，下一 turn 可读；
- 终端关闭/进程崩溃模拟：重启后 pending digest 可处理；
- session-end 与 idle 同时触发：同一 source 只生成一个 job/candidate；
- source 变化/backtrack/compaction：不误复活、不误 stale；
- secret 落库 = 0；
- digest 文本有界、可清理；
- 指标可见：pending/done/failed/recovered。

### 11.3 证据包

- 3-5 个真实会话的 digest 样本（脱敏）；
- 一次“assistant_message 后进程退出、重启恢复”的证据；
- 一次“session-end + idle 并发”的 job 证据；
- 一次 secret 命中拒绝证据；
- 清理前后行数与字节数。

---

## 12. 文档回写计划

2026-11-11 决策确认后，已按以下顺序回写基线文档（代码待实现）：

| 文档 | 回写内容 |
|---|---|
| `README.md` | 在“变更提案”区登记本文；不改变基线文档数量 |
| `03-target-architecture.md` §7.1 | 增加 turn-end outbox；明确“只记录、不抽取”；推荐 `both` |
| `04-data-model-and-api.md` §3 | 增加 `memory_turn_digests` 表、索引、迁移 v3、retention、状态 |
| `04` §6 | 增加 `generation.turnDigest` 配置、默认值、校验 |
| `05-implementation-plan.md` M2 | 增加 actor sink、outbox、recovery、retention、测试、文件落点 |
| `06-test-and-acceptance.md` §3.4/§8.3 | 增加 digest 写入/恢复/幂等/清理/脱敏/并发测试 |
| `13-memory-injection-and-learning-update-plan.md` §4.3/§4.9/§12.5 | 明确 turn-end 只写 digest；session-end/idle/startup 消费；边界与恢复 |
| `07-risks-operations-migration.md` | 增加 digest 隐私、重复数据、清理失败、startup recovery 风险 |
| `08-completeness-review.md` | 登记“终端关闭/崩溃恢复”闭环修补记录 |

### 12.1 回写顺序

1. 先改 `04` schema/配置（事实源）；
2. 再改 `03` 触发/架构；
3. 改 `13` 写路径/边界；
4. 改 `05` 施工与 `06` 验收；
5. 最后改 `07/08` 风险与审查；
6. 更新 `README` 索引。

### 12.2 回写原则

- `04` 仍是 schema/API 事实源；
- `03` 只描述架构与触发边界；
- `05/06` 只在施工与验收层登记；
- `13` 保留读/写路径闭环语义；
- 本文保留决策记录与多 turn 优化细节，作为实现参考。

---

## 13. 风险与回滚

### 13.1 风险

| 风险 | 等级 | 缓解 |
|---|---|---|
| digest 变成第二份敏感数据 | P1 | 默认有界 + redaction；支持只存 ID；TTL |
| digest 与 canonical transcript 不一致 | P1 | Phase 1 不把 digest 当 source；抽取仍读 transcript |
| session-end/idle 重复抽取 | P1 | job 唯一键 + digest watermark + processed_at |
| 旧候选被误标 stale | P1 | Phase 1 不改 `MarkSessionSourceChanged`；只做 outbox |
| startup recovery 抽取活跃 session | P2 | 只处理 closed/stale/orphaned；可配置关闭 |
| session 删除后 digest 复活 | P1 | 应用层级联删除或标记 invalidated |
| digest 写入阻塞 turn 终态 | P2 | 短超时 + 异步/有界 writer；失败只告警 |
| migration v3 失败 | P1 | additive migration；失败回退旧版本；不破坏 v1/v2 表 |
| 文本截断导致质量下降 | P2 | 保留 message ID；需要时回查 canonical |

### 13.2 回滚

- 配置 `turnDigest.enabled=false`：停止写入/处理；
- 代码回退后忽略 `memory_turn_digests`，不影响旧表；
- 不删除 v3 表；仅停止使用；
- 已生成的 memory entries 仍可通过既有流程清理/失效；
- 删除表需单独 migration，不在回滚路径自动执行。

---

## 14. 已确认设计决策

以下决策于 2026-11-11 确认，作为 Phase 1 的实现基线：

| # | 决策项 | 已确认结论 |
|---|---|---|
| 1 | digest 存放位置 | **放 `memory.db`**；digest 属于 memory 写路径 outbox，与 `memory_jobs`、retention、metrics 同生命周期。session DB 只保留 canonical transcript。session 删除由应用层清理 digest。 |
| 2 | `storeText` 默认值 | **默认 `true`，但有界 + 脱敏**：只存 user text 与最终 assistant text，不存完整 transcript/tool output/reasoning 全量；高隐私部署可设 `false`，只存 message IDs。 |
| 3 | startup recovery | **独立于 `generation.trigger`**，由 `turnDigest.recoveryOnStartup` 控制，默认 `true`；只处理 closed/stale/orphaned session，不抽取活跃会话。`trigger` 继续只表达 idle/session_end/both/manual。 |
| 4 | interrupted/error turn | **只自动抽取 `outcome=success` 且 assistant text 非空的 turn**；interrupted/error 保留 digest 用于诊断/人工处理，不自动进入 LLM 抽取。 |
| 5 | session-end 与 idle 优先级 | **session-end 立即触发；idle 只做兜底；同一 session 串行化**。两者共用 `phase1_extract` job 与 digest watermark，避免重复 candidate。 |
| 6 | `/new`、`/resume`、compaction | **不同步跑 LLM**；只写/更新 digest 并唤醒后台 worker。compaction 后把受影响 digest 标记 `invalidated` 或更新 watermark。 |
| 7 | diagnostics API | **Phase 1 不新增公共 API**；扩展现有 memory status/metrics（pending/done/failed/recovered），必要时加内部 CLI/debug 入口。 |
| 8 | digest-as-source | **Phase 2 再评估**；必须先完成 per-turn source identity、stale 规则、backtrack/compaction 失效设计。 |

### 14.1 决策后的默认配置

```yaml
memory:
  mode: on
  generation:
    enabled: true
    trigger: both
    turnDigest:
      enabled: true
      storeText: true
      maxUserChars: 8192
      maxAssistantChars: 16384
      recoveryOnStartup: true
      retentionDays: 7
      maxPendingPerSession: 200
```

### 14.2 仍未改变的原则

- `off`：零写入、零 LLM、零建表；
- `shadow`：写 digest、抽取 candidate，不注入；
- `on`：写 digest、抽取、下一 turn 可注入；
- 读路径不等待 digest 写入或抽取；
- digest 不是第二份 transcript，Phase 1 的抽取 source 仍是 canonical transcript。

---

## 15. 多 Turn 会话优化

### 15.1 问题

一个会话有大量 turn 时，不能简单“把全部 digest 一次性交给 LLM”：

- 当前 `MaxSourceMessages` 默认 200，`ChatSessionSource.ReadTranscript` 只读取最近一页；
- 单次抽取超过 `MaxSourceBytes`（默认 512KB）会被截断；
- digest 表和 pending 队列会无限增长；
- session-end 一次抽取可能超时/超预算；
- 分多次抽取又可能误触发现有 `MarkSessionSourceChanged` 的 session 级 hash/revision stale 规则。

因此多 turn 优化必须分两层：

1. **小会话**：保持在 session-end/idle 做一次有界抽取；
2. **大会话**：按 turn digest 分批/窗口抽取，不能一次性读完整 transcript。

### 15.2 分批（Batching）

建议新增：

```yaml
turnDigest:
  maxTurnsPerBatch: 20
  maxBytesPerBatch: 131072
  maxDigestsPerRun: 200
  extractEveryNTurns: 50
  idleExtractMinutes: 30
  windowTurns: 100
  windowSummaryEnabled: true
```

规则：

- 按 `digest_seq` 升序取 pending digest；
- 每批最多 `maxTurnsPerBatch` 个 turn 或 `maxBytesPerBatch` 字节；
- 每次运行最多处理 `maxDigestsPerRun` 个；
- 成功一批后只标记该批 watermark 之前的 digest 为 done；
- 剩余 pending 留到下一次 idle/session-end/startup；
- 不能因为只处理了最近一页就把全部 digest 标记 done。

### 15.3 窗口化 transcript 读取（基础实现已完成）

当前 `ChatSessionSource.ReadTranscript` 只能读最近一页；已新增：

```go
ReadTranscriptWindow(ctx, session, startSeq, endSeq, maxMessages) (Transcript, error)
```

基础实现已提供 `ReadTranscriptWindow` 与窗口 job：

- 只读取指定 seq/turn 窗口；
- 仍以 canonical transcript 为 source of truth；
- 每个窗口生成独立的 `phase1_extract` job；
- `job_key` 使用 `session_id + window_start_seq + window_end_seq + window_hash`；
- `memory_sources` 记录该窗口的 source message IDs；
- `memory_sources.source_window` 记录窗口 identity（例如 `1-4`）；
- `MarkWindowSourceChanged` 只比较同窗口 source；
- session-wide `MarkSessionSourceChanged` 跳过带 `source_window` 的 source，避免跨窗口误标 stale。

窗口 reader 只读取指定 `[startSeq, endSeq]`；窗口大小受 `maxTurnsPerBatch` / `maxBytesPerBatch` 约束。

### 15.4 增量抽取

建议为每个 session 维护：

```text
last_processed_digest_seq
last_processed_window_end_seq
```

触发策略：

- session-end：按 `maxDigestsPerRun / maxTurnsPerBatch` 做有界 drain，一次 session-end 最多推进该预算内的 batch；
- idle：独立 digest ticker 使用 `idleExtractMinutes`；active session 在 pending 达到 `extractEveryNTurns` 时提前处理，否则等 `minIdleHours`；consolidation/diagnostics 使用独立 30m maintenance ticker；
- startup recovery：只处理 pending 且 session 已 closed/stale/orphaned；
- 没有新 digest 时直接跳过，不调用 LLM。

这样长会话不会等到最后才学习，也不会每次 idle 都重跑全量。

### 15.5 分层汇总（Rollup，已实现）

大量 turn 不能长期保留全部 L0 digest。建议三层：

| 层级 | 内容 | 生命周期 |
|---|---|---|
| L0 | 每 turn digest（用户提问 + 最终回答 + 引用） | 短保留，抽取成功后清理 |
| L1 | 每 N turn 的窗口摘要（window summary） | 中保留，供 session 内/跨会话回顾 |
| L2 | session/project 级 Consolidation 摘要 | 长期，进入 memory summary |

已新增 session-scoped 表 `memory_turn_windows`（migration v6）：

```text
memory_turn_windows
  session_id
  project_key
  window_index
  start_digest_seq
  end_digest_seq
  start_message_seq
  end_message_seq
  turn_count
  summary_text
  summary_hash
  summary_revision
  source_hash
  status
  created_at
  processed_at
```

流程：

1. L0 抽取完成后 `rollup_status=pending`，不会因 extraction done 被 retention 清理；
2. 每 `windowTurns` 个 done digest（或 backlog 排空后的 partial tail）生成 L1 window summary；
3. L1 存储成功后生成一条 `source_kind=turn_window` 的 L2 seed candidate，再由现有 Consolidation 晋升并进入 L2 summary；
4. 只有 L1 与 L2 seed candidate 都成功后，才通过 `rollup_status=done` 推进 L0 水位；
5. L1/L2 seed 生成失败时保留 L0；配置关闭 window summary 时显式写 `rollup_status=skipped`。

实现锚点：

- `memory_turn_digests.rollup_status / rollup_window_id / rolled_up_at`
- `memory_turn_windows`
- `memoryruntime.RollupTurnDigests`
- `memoryruntime.WindowSummarizer` / `LLMWindowSummarizer`
- `memorystore.MarkTurnDigestsRolledUp`

### 15.6 Backpressure 与优先级

- `maxPendingPerSession` 是**软上限**：达到阈值时优先触发 batch/rollup，不是静默丢弃 turn；
- backlog 超过 `maxDigestsPerRun` 时，按 oldest-first 处理，保证历史覆盖；
- 如果产品更重视“最近上下文”，可以增加 recent-first 模式，但必须保证旧 digest 最终仍可处理；
- 暴露 `pending_digests`、`oldest_pending_age`、`backlog_batches` 指标；
- 不允许因为 backlog 大而把未处理 digest 标成 done。

### 15.7 清理

- L0 digest：只有 `rollup_status=done/skipped` 后才按 `retentionDays` 清理；failed/pending 保留；
- L1 window summary：按独立 `windowRetentionDays`（默认 30）清理 `done/invalidated`；`pending/processing/failed` 保留；
- L2 project memory：按现有 memory retention 管理；
- session 删除：应用层清理 L0/L1，L2 memory entries 走既有删除/失效流程；
- compaction：只改 prompt projection，canonical transcript 不变，因此不失效 canonical 派生 L0/L1/memory；
- backtrack：用 `canonical_cutoff_seq` + `canonical_message_count_before` + `canonical_cutoff_at` + tombstone IDs 精确失效受影响 digest/window/memory；事件时间防止误伤截断后复用 seq 的新行；旧事件缺少范围时回退 session 级保守失效。

### 15.8 多 turn 测试

- 500 turn / 5000 turn 会话的 batch 处理；
- 只读最近 200 条消息时，旧 pending digest 不被误标 done；
- window reader 的 source IDs 与 `memory_sources` 一致；
- 新增窗口不会把旧窗口候选误标 stale；
- backlog 超过上限时按 oldest-first 分批；
- L0 → L1 → L2 汇总成功后才清理 L0；
- session 删除/compaction 后不恢复已失效窗口；
- idle 增量抽取不重复调用 LLM。

### 15.9 结论

**Phase 1 应先实现 outbox、batching 和 backlog 保护；窗口化 transcript 读取作为 Phase 1.5 的必要补强。**
在窗口化读取完成前，大会话只能保证“最近窗口可抽取”，不能标记所有 turn 已处理。Phase 2 的 digest-as-source 可作为窗口读取成本过高时的替代方案。

---

## 16. 实施顺序建议

### Step 1：设计确认

- 本文决策已确认；
- `04/03/13` 事实源条款已回写；
- `05/06/07/08` 已登记施工、验收与风险；
- 下一步从 Step 2 开始代码实现。

### Step 2：schema + store

- 状态：已完成；
- migration v3；
- `memory_turn_digests` CRUD；
- retention/session delete；
- metrics。

### Step 3：runtime writer

- 状态：已完成；
- `SessionActorConfig.TurnDigestSink`；
- `assistant_message` 终态写入；
- redaction/bounds；
- CLI/runtime-server host 注入。

### Step 4：recovery

- 状态：已完成（含窗口化读取、rollup 与 per-window stale）；
- session-end 标记 processed；
- idle pending sweep；
- startup recovery；
- 并发/幂等；
- 状态/指标。

### Step 5：验收

- 状态：Phase 1 单元/集成测试已通过；SQLite canonical store reopen、abrupt-exit 与 hard-kill E2E 已补；真实终端关闭与 shadow 待补；
- 单元/集成/E2E；
- 崩溃/终端关闭模拟；
- shadow 样本；
- 文档回写与 README 更新。

### 16.1 实现状态（2026-11-11）

已完成：

- `memory_turn_digests` migration v3、CRUD、pending/deferred/done 生命周期、retention；
- `SessionActorConfig.TurnDigestSink` 与 `assistant_message` 终态写入；
- `memoryruntime.Runtime.RecordTurnDigest`（脱敏、限长、usage/metadata、幂等）；
- session-end / idle ticker / startup recovery 的 pending digest 消费；
- 大会话 `ReadTranscriptWindow` + `RunSessionWindow` 基础窗口 job；
- `CanonicalCount > MaxSourceMessages` 时按 digest batch 计算 `[startSeq,endSeq]`，读取 canonical window；
- 窗口 source hash/job key 独立；`memory_sources.source_window` + `MarkWindowSourceChanged` 完成 per-window stale；
- `off` / generation-disabled 零写入；
- CLI 与 runtime-server host 注入；
- memory status 增加 `turn_digests` 计数；
- memory status 增加 `turn_windows` 计数；
- runtime-server/web/ACP 显式删除 session 时 best-effort 清理 digest；
- compaction 事件带 `canonical_transcript_changed=false` 时不再失效 canonical 派生记忆；旧事件仍回退保守失效；
- backtrack finish payload 增加 `canonical_cutoff_seq` / `canonical_message_count_before`；store 只失效受影响 message range 的 L0 digest、L1 window 与 memory；
- legacy backtrack 事件若缺少 cutoff 且 removed IDs 被 tombstone cap，回退 session 级保守失效；
- `memory_turn_windows` migration v6 与 L1 生命周期；
- L0 extraction done 后保持 `rollup_status=pending`，rollup 成功前 retention 不删除；
- `windowTurns` 分组、backlog 排空后 partial tail finalize、L1 失败保留 L0；
- session-end 按 `maxDigestsPerRun / maxTurnsPerBatch` 有界 drain；
- digest ticker 使用 `idleExtractMinutes`；active session 以 `extractEveryNTurns` 为提前提取阈值，否则等 `minIdleHours`；
- consolidation/diagnostics 使用独立 30m maintenance ticker，避免 `idleExtractMinutes` 改变 maintenance 频率；
- L1 独立 `windowRetentionDays` retention，接入 digest ticker；
- L1 手动 purge API (`/api/runtime/memory/turn-windows/purge`) 与 `/memory purge-windows --yes (--session <id> | --all)`；释放 L0 rollup 水位，不删除 L2 memory；
- L1 summary 生成 `source_kind=turn_window` 的 L2 seed candidate，由现有 Consolidation 晋升并进入 summary；
- window summary 关闭时显式写 `rollup_status=skipped`；
- L1 source 变化会 stale 旧 turn-window candidate，避免 L2 复用旧摘要；
- E2E：`TestE2ESQLiteCanonicalRestartRecovery` 使用真实 SQLite canonical transcript，模拟 store 重启后恢复 pending digest、窗口抽取、L0→L1 rollup 与 Consolidation/L2；
- E2E：`TestE2ESQLiteAbruptProcessExitRecovery` 在子进程写完 digest 后 `os.Exit(0)`，不关闭 session/memory store；父进程 reopen SQLite 文件后完成恢复；
- E2E：`TestE2ESQLiteHardKillRecovery` 在子进程提交 session/digest 后由父进程执行 hard kill（Windows TerminateProcess / POSIX SIGKILL 等价），随后 reopen 并恢复；
- store/runtime/actor/config 单元测试。

仍待实现：

- 真实终端关闭 E2E 与 shadow 证据包。

---

## 17. 最终结论

- **需求合理**：turn 结束时收集“用户提问 + 最终 assistant 输出”是运行时就能获得的稳定信号；
- **不能把 digest 直接作为第二份 transcript**：否则会破坏当前 memory 的 source、hash/revision、stale 和证据链；
- **最契合当前设计的做法**：Turn Digest 作为 **outbox/trigger**，抽取仍读 canonical transcript；
- **Phase 1 的目标**：解决终端关闭/崩溃导致的抽取丢失，并提供可清理、可审计的运行时待处理记录；
- **多 turn 会话**：按 digest seq 分批、窗口化读取 canonical transcript、L0→L1→L2 rollup；只读最近一页时不得把更早 pending digest 标 done；
- **Phase 2 的目标**：只有在实测证明需要时，才把 digest 升级为抽取输入，并先完成 per-turn source identity 设计。

> 本提案不改变当前 memory 读路径；所有变更默认由 `memory.mode` / `generation.turnDigest.enabled` 门控，`off` 模式零写入、零 LLM。

---

## 18. 相关代码锚点

- `internal/chat/actor.go:197`：`SessionActorConfig`
- `internal/chat/actor.go:3125`：`startSessionRun`（prompt）
- `internal/chat/actor.go:3722-3754`：`assistant_message` 终态
- `internal/chat/turn_digest.go`：`TurnDigest` / `TurnDigestSink`
- `internal/chat/turn_digest_actor.go`：终态 digest 写入与消息 ID 解析
- `internal/memoryruntime/chat_source.go:45`：`ChatSessionSource.ReadTranscript`
- `internal/memoryruntime/chat_source.go`：`ReadTranscriptWindow`
- `internal/memoryruntime/runtime.go:152`：Phase-1 job key
- `internal/memoryruntime/runtime.go`：`RunSessionWindow`
- `internal/memoryruntime/turn_digest.go`：digest recorder / pending recovery / windowed batch / fallback defer
- `internal/memorystore/v2_runtime.go:517`：`MarkSessionSourceChanged`
- `internal/memorystore/v2_runtime.go`：`MarkWindowSourceChanged`
- `internal/memorystore/v2_schema.go`：`memory_sources` / `memory_jobs`
- `internal/memorystore/v2_turn_digest.go`：`memory_turn_digests` store
- `cmd/aicli/commands/chat_memory_runtime.go:114`：session-end trigger
- `cmd/aicli/commands/chat_setup.go:716`：SessionManager.Close 调用点
- `cmd/aicli/commands/chat_actor_host.go:355`：host.Close / asyncWG
- `docs/agent-memory/13-memory-injection-and-learning-update-plan.md:483-490`：turn-end 不抽取
- `docs/agent-memory/13-memory-injection-and-learning-update-plan.md:1202-1206`：只配 session_end 的风险
