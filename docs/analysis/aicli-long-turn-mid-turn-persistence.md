# 长 turn 中途增量落库（turn 级持久化解耦）

日期：2026-09-14
状态：已实现并验证（默认开启）

## 1. 现象（现场证据）

会话 `session_20260914160651_7ytOWV8b`：

- `%USERPROFILE%\.aicli\sessions\session_history.sqlite` 的 `session_messages` 停在
  seq 377 / 16:33:43，而该 turn 一直跑到 17:19（约 46 分钟）。
- 期间只有 runtime events / HTTP / 调试文件在增长，**权威会话存储长时间停在
  turn 开始时的状态**。
- 后果：长 turn 运行中 `/resume`、会话列表、其他进程读库都看不到本 turn 已经
  产生的内容；进程崩溃/被杀时，这一整段已提交内容全部丢失。

这与会话 JSON 镜像（`chat-logs/.../*.json`）无关，问题在**权威库**。

## 2. 根因

durable 历史的落库时机只有 turn 收尾：

- ReAct 循环内部在每个提交点（assistant 文本、tool 结果、预算/压缩改写）调用
  `PersistHistory`（`internal/agent/loop.go` 的 `persistBuilderHistory`），但那个
  闭包只做 `session.ReplaceHistory(durable)` —— 改的是**内存里的会话对象**。
- 真正写库是 `SessionActor.persistSession`，在此之前只被 post-turn sync /
  中断 / 审批等离散路径调用；长 turn 期间没有任何周期性写库。

也就是说：**提交点（commit）和落库点（persist）之间没有连线**，长 turn 里
commit 再多次也不会触发持久化。

## 3. 方案

把「durable 历史已提交」变成一个显式信号，由宿主决定怎么落库：

1. `internal/agent/loop.go`：`LoopReActConfig` 新增
   `OnHistoryCheckpoint func(ctx, messages)`，在 `RunWithSession` /
   `ContinueWithSession` 的 `PersistHistory` 闭包内、`ReplaceHistory` 之后调用。
   契约：尽力而为、只读 messages、不计入 turn 结果、实现方自带节流。
2. `internal/chat/actor.go`：
   - `SessionActorConfig.CheckpointInterval`（0 → `DefaultSessionCheckpointInterval`
     = 15s；负数显式禁用）。
   - `checkpointSessionHistory`：按间隔节流调用 `persistSession`（复用既有
     run 归属校验、`sessionPersistMu`、broker alias 合并、SQLite 防回退口径），
     失败只上报事件、并回退节流戳让下一个提交点立即重试。
   - 失败上报 `session.checkpoint_persist_error`（`errSessionRunSuperseded` /
     context 取消不报，避免正常收尾路径制造噪声）。
3. `cmd/aicli/commands/chat_actor_host.go`：CLI 显式装配，并支持
   `AICLI_SESSION_CHECKPOINT_INTERVAL`（Go duration，如 `5s`；`off` / `0s` /
   `disable` 关闭；非法值回退默认，避免漏写单位静默关掉落库）。
4. `internal/runtimeobserve/known_types.go`：登记 `session.checkpoint_persist_error`，
   否则落库失败会被统计成 `unknown_events_dropped`，把真实故障伪装成目录缺口。

## 4. 语义与边界

- **turn 收尾的 post-turn sync 仍是最终权威**：中途落库是增量 best-effort，
  不改变任何 turn 结果、不参与错误传播。
- 落库粒度 = **durable 提交点**（assistant 消息提交、tool 结果、压缩/预算改写），
  节流 15s；单个 LLM 调用长时间流式输出期间若不产生提交点，库上仍是上一个
  提交点状态（提交点语义决定的，不是遗漏）。
- `chat-logs/*.json` 镜像**未改**：它是调试/展示镜像，仍按既有节奏刷新；权威
  记录是 SQLite。
- 写放大：长 turn 内至多 1 次/15s 全行更新（SQLite，含 WAL），与 turn 时长无关。

## 5. 验证

新增回归（`go test ./internal/chat/ ./internal/agent/ ./internal/runtimeobserve/`）：

| 测试 | 断言 |
| --- | --- |
| `TestSessionActorCheckpointWritesMidTurnHistoryWithThrottle` | 提交点写库、窗口内不重复写、窗口后继续写、写入的是提交后的完整历史 |
| `TestSessionActorCheckpointFailureIsIsolatedAndObservable` | 失败不外抛、上报事件、节流戳回退后立即重试 |
| `TestSessionActorCheckpointIntervalSemantics` | 0 → 默认、负数禁用（且不挂回调） |
| `TestSessionActorCheckpointLoopConfigWiresCallback` | 回调已挂载且不改写共享 loopConfig |
| `TestSessionActorCheckpointPersistsWhileTurnStillRunning` | **端到端**：LLM 调用被阻塞（turn 未结束）时，权威存储已能读到本 turn 已提交历史 |
| `TestReActLoop_NotifiesHistoryCheckpointAfterDurableCommit` | 循环在每个 durable 提交点通知宿主，且不带 system 消息 |

并发：`go test -race ./internal/chat/ -run Checkpoint` 通过。

现场核验（长 turn 运行中）：

```sql
-- %USERPROFILE%\.aicli\sessions\session_history.sqlite
SELECT MAX(seq), MAX(created_at) FROM session_messages WHERE session_id = '<session_id>';
```

长 turn 运行中该值应随提交点前进（秒级～15s 级），而不是停在 turn 开始时刻。
