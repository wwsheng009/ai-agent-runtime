# Agent Memory 运行时缺口审查与修复记录

> 日期：2026-11-17  
> 范围：Agent Memory v2 的生产宿主接线、Turn Digest Outbox、Phase 1/2、读取注入与诊断面。  
> 结论：配置和 M1-M4 组件代码已存在，但实际运行数据表明 **Turn Digest 写路径没有形成有效闭环**，导致后续抽取、Consolidation、summary 和注入全部空转。

## 1. 运行时证据

### 1.1 生效配置

在仓库根、`backend/`、`itsm/itsm`、`sap/sap_network_core` 四个工作区实际加载合并后的配置均为：

```text
memory.mode=on
generation.enabled=true
generation.trigger=session_end
generation.turnDigest.enabled=true
generation.turnDigest.recoveryOnStartup=true
injection.enabled=true
injection.mode=hybrid
memory.tools.enabled=false
```

因此问题不是配置未生效。

### 1.2 多 workspace 记忆库数据

| workspace | entries | turn_digests | phase1 pending | phase2 succeeded |
|---|---:|---:|---:|---:|
| ai-agent-runtime | 0 | 0 | 1 | 7 |
| itsm/itsm | 0 | 0 | 0 | 6 |
| itsm/itsm-client | 0 | 0 | 0 | 3 |
| sap/sap_network_core | 0 | 0 | 0 | 7 |
| sap/sap_abap | 0 | 0 | 0 | 7 |
| sap/sap_abap_projects | 0 | 0 | 0 | 2 |
| sap/sap_pilot | 0 | 0 | 1 | 2 |

所有 workspace 都是同一形态：**Turn Digest 为 0、记忆记录为 0，但 Phase 2 job 持续成功**。这说明 Phase 2 只是在没有 candidate 输入时“空成功”，不能作为抽取链路健康的证据。

### 1.3 长会话状态

活动会话仍处于 `active`，例如：

- `session_20261117151543_6kAtEpDl`：2,505 条消息，workspace `itsm/itsm`
- `session_20261115220427_Qzi9PVUG`：9,738 条消息，workspace `sap/sap_network_core`
- `session_20261113135046_2BKNV4vB`：948 条消息，workspace `ai-agent-runtime`

这些会话没有正常关闭，`trigger=session_end` 不会触发 legacy `RunSession`。但更关键的是：每个 turn 都应写 Turn Digest，而实际 `memory_turn_digests` 为 0。

### 1.4 维护 ticker 与 trigger 的关系

`internal/memoryassembly/maintenance.go` 中：

- `trigger=session_end` 只关闭 `RunIdle` 的 legacy session 抽取；
- `turnDigestEnabled=true` 时仍启动 digest ticker，并每 30 分钟执行 `RunPendingTurnDigests(TriggerIdle)`。

因此，**即使 trigger 只配置 session_end，只要 digest 写入成功，维护 ticker 仍应消费并生成记忆**。当前 0 digest 说明断点在 digest 写入，而不是 trigger 或 injection。

## 2. 已确认/高置信缺口

### P0-1 Turn Digest 写入存在静默丢弃路径

关键代码：

- `internal/chat/actor.go:4126-4153`：终端 assistant message 时调用 `recordTurnDigest`
- `cmd/aicli/commands/chat_actor_host.go:1931-1965`：仅 `h.MemoryRuntime != nil` 注入 `TurnDigestSink`
- `internal/memoryruntime/turn_digest_sink.go:39-44`：`eligible` 不通过时多数原因直接 `return nil`
- `turn_digest_sink.go:47-55`：session mode 非 enabled 时直接 `return nil`

可能的静默丢弃原因包括：`MemoryRuntime` 未创建、sink 未注入、workspace mismatch、session mode、subagent/ephemeral、rollout age、`GenerationDisabled` 或 `TurnDigestEnabled=false`。

当前 `/memory status` 无法区分这些状态。

### P0-2 digest 写入 2s 预算小于 SQLite busy_timeout 5s

- `internal/chat/turn_digest_actor.go:12-20`：`context.WithTimeout(..., 2*time.Second)`
- `internal/sqliteutil/sqliteutil.go:25-26`：默认 `busy_timeout=5000ms`

`RecordTurnDigest` 的 2 秒预算同时覆盖 session identity 读取、session mode 查询、memory DB 打开/写入。多进程共享 `session_history.sqlite` 和同一 workspace memory DB 时，2 秒内超时会导致 digest 丢失，且没有重试。

### P0-3 host cleanupFns 被覆盖，memory store 不会 Close

- `cmd/aicli/commands/chat_memory_runtime.go:37,61`：`initializeMemoryRuntime` 向 `h.cleanupFns` 追加 memory store Close 与 invalidation unsubscribe
- `cmd/aicli/commands/chat_actor_host.go:1592`：随后 `host.cleanupFns = []func(){...}` 直接覆盖整个 slice

后果：memory SQLite 连接泄漏、WAL/SHM 句柄残留、关闭/重建 host 后旧连接继续占用数据库，放大锁竞争和 digest 写超时。

### P0-4 startup 恢复只扫描 digest，不恢复 legacy phase1 job

- `internal/memoryassembly/maintenance.go:104-108`：startup 只调用 `RunPendingTurnDigests(TriggerStartup)`
- `internal/memoryruntime/turn_digest.go:120-129`：只处理 pending digest session
- `Run`（可处理 `phase1_extract` pending job）只在 `trigger=idle/both` 时调用

因此 `trigger=session_end` 且进程中断时，pending phase1 job 没有恢复入口。当前 `ai-agent-runtime` 与 `sap_pilot` 各有一条 pending job。

### P0-5 phase1 job key 绑定 transcript hash，旧 job 无清理

- `internal/memoryruntime/runtime.go:195`：`jobKey = session:<id>:<transcript-hash>`
- 会话增长后 hash 变化，新 job 使用新 key，旧 pending job 永远不会匹配。
- 没有 stale/superseded job 清理逻辑。

### P0-6 无 digest 长会话只抽取最近 200 条

- `internal/memoryruntime/extraction_recovery.go:93-98`：无 digest 时 `useDigestWindows=false`
- `ChatSessionSource.ReadTranscript` 默认 `maxMessages=200`

对于 9,738 条消息的会话，最多覆盖最近 200 条，远低于“跨会话记忆”的预期。

### P0-7 runtime-server 单 workspace memory runtime

- `internal/api/runtimeapi/memory_runtime_wiring.go:136-152`：memory runtime 使用 `h.runtimeConfig.Workspace.Root` 创建一次
- `cmd/runtime-server/memory_runtime_boot.go:16-48`：进程启动时只创建一个 memory runtime
- `RecordTurnDigest` / `Run` / `RunSessionEnd` 都会用该 workspace 与 session workspace 比较

runtime-server 当前 `/api/runtime/memory/status` 报 `workspace_path=E:\projects\ai\ai-agent-runtime\backend`；其他 workspace 的会话在 workspace mismatch 时会被跳过。该模型不支持多 workspace 长期服务。

### P1-1 `/memory status` 可能给出假阳性

- `cmd/aicli/commands/chat_command_result.go:796-816`：`/memory status` 重新 `openChatProjectMemoryStore`
- `chat_memory_document.go:24-51`：状态来自独立 store，而不是实际 `MemoryRuntime`

即使 `session.LocalRuntimeHost.MemoryRuntime == nil`，仍会显示 `mode=on` 和 DB 路径。

### P1-2 Phase 2 空成功不可区分

- `internal/memoryruntime/consolidate.go:131-134`：无 active records 时直接完成 job 并返回成功
- 结果中没有 `no_input`、`promoted=0`、`summary_compiled=0` 等区分

### P1-3 缺少宿主级 E2E

现有 memory 组件测试无法覆盖：

1. 本地 host + 真实 config 下，一个 turn 是否产生 digest；
2. 多进程共享 workspace DB 时 digest 是否丢失；
3. `trigger=session_end` + active 长会话时 digest ticker 是否消费；
4. runtime-server 多 workspace 隔离；
5. pending phase1 job 在无 idle trigger 时是否恢复；
6. `/memory status` 是否反映 runtime 实际状态。

## 3. 本次修复目标

按优先级完成以下 bounded fixes：

1. 修复 `cleanupFns` 覆盖，保证 memory store 与事件订阅正确释放；
2. 提高 digest 写入可靠性：超时从 2s 提升到大于 SQLite busy_timeout，并增加一次有界重试；
3. 对 digest 静默跳过路径增加日志，至少让 workspace mismatch/session mode 可见；
4. 在 maintenance startup 阶段增加 legacy phase1 恢复入口，不受 `trigger=session_end` 限制；
5. 补回归测试并运行 memory 定向验证。

不在本次直接完成（保留为后续缺口）：

- per-workspace 单例 memory runtime 重构；
- runtime-server 多 workspace 路由重构；
- stale phase1 job 的完整 superseded 清理；
- `/memory status` 完整 runtime 诊断面（本次只记录缺口）。

## 4. 验证记录

### 4.1 本次已修复

1. **host cleanup 覆盖**
   - 新增 `appendLocalChatHostCleanup`，host 初始化改为 append 而不是覆盖 `cleanupFns`。
   - memory store Close 与 digest invalidation unsubscribe 不再被后续 cleanup 列表覆盖。
   - 回归测试：`TestAppendLocalChatHostCleanupPreservesExistingCallbacks`。

2. **digest 写入超时/重试**
   - `memoryTurnDigestWriteTimeout` 从 2s 提升到 8s，高于 SQLite `busy_timeout=5s`。
   - 对 SQLITE_BUSY 家族增加一次 200ms 退避重试；非 BUSY 错误不重试。
   - 回归测试：
     - `TestRecordTurnDigestRetriesBusyOnce`
     - `TestRecordTurnDigestDoesNotRetryNonBusyError`

3. **digest 静默跳过可见化**
   - `eligible` 拒绝时记录 `memory turn digest skipped`，workspace mismatch/subagent/ephemeral 走 warn，session mode 走 info。
   - 相关测试 `TestRecordTurnDigestEnforcesProducerEligibility` 已覆盖并显示日志原因。

4. **startup legacy phase1 恢复**
   - 新增 `Runtime.RecoverPendingPhase1`：扫描 pending `phase1_extract` job，按 terminal/idle gate 调用 `RunSession`，受 `MaxSessionsPerRun` 限制。
   - `memoryassembly.runMaintenance` 在 startup 阶段独立调用该恢复，不再依赖 `generation.trigger=idle/both`。
   - 回归测试：
     - `TestRecoverPendingPhase1ProcessesMatchingPendingJob`
     - `TestRecoverPendingPhase1RespectsIdleGate`
     - `TestPhase1JobSessionID`

5. **`/memory status` 反映实际 runtime**
   - 输出新增 `runtime=ready trigger=... generation=... digest=...` 或 `runtime=not_initialized`。
   - 不再只依赖重新打开 store 的 `mode=on` 假阳性。
   - 回归测试：`TestChatMemoryStatusReportsRuntimePresence`。

6. **既有时间依赖测试修复**
   - `TestSweepLifecycleCapsNonPermanentRecords` 不再依赖真实 wall clock；记录时间固定到测试 `now`。

7. **stale base phase1 job 清理**
   - 新增 `Store.CancelStaleBaseExtractionJobs`，在 recovery 成功后取消旧 transcript hash 的 pending base job。
   - window / byte-page job 暂不取消，因为其 key 末尾不是当前 session hash。
   - 回归测试：`TestRecoverPendingPhase1CancelsStaleBaseJob`。

8. **digest runtime stats**
   - `Runtime.DigestStats` 记录 written/skipped/failed、last skip reason、last error、last write time。
   - `/memory status` 新增 `digest_runtime` 行。
   - `/api/runtime/memory/status` 新增 `memory_runtime_present`、`memory_runtime_workspace`、`digest_runtime`。
   - 回归测试：`TestRecordTurnDigestRedactsAndTruncates`、`TestRecordTurnDigestSkipsWhenGenerationDisabled`。

9. **aicli 本地 per-workspace memory runtime 复用**
   - 新增 `sharedMemoryRuntimeRegistry`，按 workspace + memory 配置 + user + SessionManager + LLMRuntime 复用同一 runtime。
   - 引用计数：最后一个 host 释放时才停止 maintenance 并关闭 store。
   - host 不再为每个 ChatSession 创建独立 SQLite 连接和 ticker。
   - 回归测试：`TestSharedMemoryRuntimeReusesWorkspaceRuntime`。

### 4.2 已执行验证

全部通过：

```bash
go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryassembly \
  ./internal/memorypolicy ./internal/memoryredact ./internal/config ./internal/contextmgr -count=1

go test ./internal/chat -run 'TestRecordTurnDigest|TestSessionActorRecordsOneTurnDigest' -count=1
go test ./cmd/aicli/commands -run 'TestAppendLocalChatHostCleanupPreservesExistingCallbacks|TestBuildLocalChatAgent_BindsAutomaticMemoryStore|TestChatMemoryStatusReportsRuntimePresence|TestStructuredMemoryV2Commands' -count=1
go vet ./internal/chat ./internal/memoryruntime ./internal/memorystore ./internal/memoryassembly ./cmd/aicli/commands
go build ./cmd/aicli ./cmd/runtime-server
git diff --check
```

第二轮补充验证：

```bash
go test ./internal/memoryruntime -run 'TestRecordTurnDigest|TestRecoverPendingPhase1' -count=1
go test ./internal/memorystore -run 'TestCancelStaleBaseExtractionJobs|TestSweepLifecycleCapsNonPermanentRecords' -count=1
go test ./internal/api/runtimeapi -run 'TestMemoryStatusReportsRuntimeDigestStats|TestMemoryAPIRecordsLifecycleAndStatus' -count=1
go test ./cmd/aicli/commands -run 'TestChatMemoryStatusReportsRuntimePresence|TestAppendLocalChatHostCleanupPreservesExistingCallbacks|TestStructuredMemoryV2Commands' -count=1
go test ./cmd/aicli/commands -run 'TestSharedMemoryRuntimeReusesWorkspaceRuntime' -count=1
```

通过。

最终 memory 定向套件（含 API）通过：

```bash
go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryassembly \
  ./internal/memorypolicy ./internal/memoryredact ./internal/config ./internal/contextmgr \
  ./internal/api/runtimeapi -count=1

go test ./internal/api/runtimeapi -count=1
go build ./cmd/aicli ./cmd/runtime-server
go vet ./internal/chat ./internal/memoryruntime ./internal/memorystore \
  ./internal/memoryassembly ./internal/api/runtimeapi ./cmd/aicli/commands
```

> 备注：本轮验证期间有并行 Agent 正在修改 `internal/agentconfig` 等无关文件，曾导致
> `cmd/aicli/commands` / `internal/api/runtimeapi` 暂时编译失败（undefined symbol），
> 属于并发工作树状态，不是 memory 改动本身；并发编辑收敛后已重跑通过。

已知测试不稳定：

- 完整 `go test ./internal/chat -count=1` 在本机并发负载下偶发 `TestAppendEventsLockHoldBudget` 失败（`BatchLockHoldNs=0`）；单独运行时通过，记录到 `batch lock hold: total=520.3µs`。该测试是时间/负载敏感，不是本次 memory 改动的功能回归。

### 4.3 真实 ConPTY 端到端验证（2026-11-17/18）

#### 4.3.1 新发现的 P0：统一执行核未注入 TurnDigestSink

在真实 ConPTY 验证中定位到一个仅靠单元测试容易遗漏的断点：

- `buildSessionActor`（legacy actor 路径）会注入 `TurnDigestSink`；
- 但统一执行核 `aicliRuntimeDeps` 构造的 `runtimehost.Deps` 没有注入该字段；
- 结果：真实 turn 可以正常完成，`/memory status` 也能显示 `runtime=ready`，但统一执行核路径的 digest 永不写入。

修复：新增唯一装配点 `localTurnDigestSink(h)`，两条 actor 构造路径共用：

```go
// chat_actor_host.go
turnDigestSink := localTurnDigestSink(h)
```

```go
// chat_actor_executor_deps.go
TurnDigestSink: localTurnDigestSink(h),
```

回归测试：

```text
TestLocalTurnDigestSinkWiresBothActorPaths
```

#### 4.3.2 执行命令与结果

构建真实二进制后，用 Windows pseudo console 启动两个隔离会话，使用真实 provider：

```bash
go build ./cmd/aicli

python backend/scripts/memory-conpty-e2e.py \
  --provider magpie \
  --model auto/deepseek-v4.1-flash-1017 \
  --reasoning max \
  --timeout 240
```

结果：**PASS**，无 force-kill，两个会话均 clean exit。

证据目录：

```text
output/memory-conpty/20261118-072315/
```

关键产物：

```text
screen-visible.txt          第一个会话屏幕
screen-recall-visible.txt   第二个会话屏幕（含召回答案）
memory-snapshot.json        SQLite 快照
recall-evidence.json        第二会话注入/召回证据
```

#### 4.3.3 数据库证据

`memory-snapshot.json`：

```text
digests: done=2 pending=0
jobs:
  phase1_extract      succeeded  attempt=1  x2
  phase2_consolidate  succeeded  attempt=1  x1
entries:
  active    3
  candidate 3
summaries:
  active/fresh 1
```

第一个会话的 digest 在 turn 完成时写入；正常 `/exit` 触发 session-end pipeline，phase1 抽取与 phase2 consolidation 均在 host Close 等待窗口内完成。`/memory status` 显示：

```text
runtime=ready trigger=both generation=true digest=true
digest_runtime: written=1 skipped=0 failed=0
turn_digests: pending=0 done=1 ...
```

#### 4.3.4 跨会话召回证据

第二会话的 prompt **不包含**三个目标值，只要求从项目记忆回忆。真实 provider 请求中检索到：

```text
Project durable memory
MEMCONPTY-20261117
release/memconpty
make deploy-memconpty
```

第二会话最终输出：

```text
CODE=MEMCONPTY-20261117
BRANCH=release/memconpty
DEPLOY=make deploy-memconpty
MEMORY-RECALL-OK
```

`recall-evidence.json` 记录 `request_contains_injected_memory=true`，且 `finalized=true`、`clean_exit=true`。这证明闭环不是“模型猜中”，而是 memory injection 进入 provider 请求后产生召回。

#### 4.3.5 测试脚本时序修正

中途一次扩展跑出现 `digest pending` 与 entries 为空，根因不是抽取器，而是脚本在 prompt 回显阶段误判 marker，过早发送 `/exit`：

```text
[input] Agent 正在运行，slash 命令 "/exit" 未执行，也未加入消息队列
```

随后脚本强制 terminate，导致：

1. 第一个 session 在 session DB 中仍为 `active`；
2. 第一个 digest 停留在 `pending`；
3. startup recovery 只恢复 **terminal** session 的 pending digest，无法恢复该行；
4. 第二个会话没有可注入的 active memory。

脚本已改为等待 `agent.turn.finished` 与 `parent.turn.finalized`，再发送 `/memory status`，最后发送 `/exit` 并校验 clean exit。该错误不能作为 production 回归，但它暴露了“进程被强杀后 active session 的 pending digest 无启动恢复入口”的运维缺口，见 §5。

### 4.4 仍需真机重启验证

当前活动进程是 `aicli-3x.exe` / `aicli-2x.exe` 以及 `runtime-server.exe`，它们不会自动加载本次工作树改动。需要：

1. 重新构建/替换实际运行的二进制；
2. 重启对应 aicli/runtime-server；
3. 在一个 workspace 执行至少一个完整 turn；
4. 检查该 workspace 的 `memory_turn_digests` 是否新增 1 行；
5. 检查日志中是否有 `memory turn digest skipped` 或 `memory turn digest write failed`；
6. 等待 digest ticker 或 session-end/idle 触发，确认 candidate/entries 生成；
7. 执行 `/memory status`，确认出现 `runtime=ready`。

隔离真实二进制 `backend/aicli.exe` 的 ConPTY E2E 已通过；但在上述活动进程重启并完成现网 workspace 验证前，不能声称现网已加载本次修复。

## 5. 本次未修复的剩余缺口

1. **runtime-server 多 workspace 路由**
   - 当前 runtime-server 只绑定启动 workspace；其他 workspace 的 session 会在 workspace mismatch 时被跳过。

2. **Web/前端 digest runtime 展示**
   - API 和 `/memory status` 已有数据，Web memory panel 尚未展示 digest_runtime / last skip reason。

3. **window / byte-page stale job 清理**
   - 本轮只清理 base session job；window/page 级旧 job 仍需 superseded 语义。

4. **无 digest 长历史窗口化**
   - `06-test-and-acceptance.md` 明确要求历史/无 digest 会话使用“有界 transcript 抽取回退”，现有
     `TestRecoveryOversizedFallbackWithoutDigestCoverage` 钉住最近一页行为；完整窗口化会改变该契约和成本模型，需另开设计决策，不在本轮隐式变更。

5. **强杀后的 active-session pending digest 恢复**
   - 当前 startup recovery 的 `digestRecoveryEligible` 只接受 terminal session。
   - 进程被强制终止时，session 仍可能保持 `active`，其 pending digest 不会在下一进程启动时恢复。
   - 需要引入基于 lease/heartbeat 或 stale-active grace 的安全回收策略；不能简单放开所有 active session，否则可能对仍存活会话重复发起 LLM 抽取。
   - 常规 `/exit`、session-end 与 idle ticker 路径已验证正常；该缺口只涉及崩溃/强杀后的恢复。