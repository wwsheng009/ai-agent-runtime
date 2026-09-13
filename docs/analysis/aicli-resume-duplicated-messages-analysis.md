# `aicli resume` 渲染重复内容 — 根因分析与修复

- 复现命令：`aicli resume session_20260913200137_dKcr5jw1 --pprof --debug --yolo`
- 结论：**不是渲染层缺陷**。会话存储的 canonical 记录里已经存在重复的 assistant 行，
  resume 只是忠实回放；重复由存储层写入侧的追加起点计算（长度算术）产生。

## 1. 真机证据

会话库：`C:\Users\vince\.aicli\sessions\session_history.sqlite`（表 `session_messages`）。

修复前该会话的 canonical 原始行（`session_messages`，未做任何归一化读取）：

```
 seq role      message_id                      备注
   1 system    msg_7716825f...                 环境上下文
   2 user      msg_504d3c4f...                 "ls"
   3 tool      msg_e75e22e0...                 目录输出        <- 第 1 轮的 assistant(tool_call) 行缺失
   4 assistant msg_d04a853b...                 回答 A
   5 assistant msg_d04a853b...                 回答 A          <- 重复行（同 id，相邻 seq）
   6 user      msg_9d19f4ab...                 "pwd"
   7 assistant msg_b3f58542...                 工具调用消息
   8 tool      msg_37789b57...                 pwd 输出
   9 assistant msg_5cbd7352...                 回答 B
  10 assistant msg_5cbd7352...                 回答 B          <- 重复行（同 id，相邻 seq）
```

两处症状同时出现：**已存消息被重复插入**，以及**尚未落库的消息被跳过**（seq 3 的
assistant 工具调用行从未写入）。

## 2. 根因

`SQLiteSessionStorage.updateSessionTx`（`backend/internal/chat/sqlite_storage.go`）用长度算术
决定 canonical 追加起点：

```go
appendFrom := len(session.History)
if appendOnly { appendFrom = len(promptRows) }
else if declaredDelta := session.CanonicalMessageCount - count; declaredDelta > 0 { appendFrom = len(history) - declaredDelta }
```

调用方交回的 history 与 canonical 并非逐条一致：

- agent loop 在持久化前剥离 system 消息（`internal/agent/loop.go` 的 `PersistHistory`：
  `session.ReplaceHistory(stripSystemMessages(messages))`），于是 `len(history)` 比 canonical 少；
- CLI 侧镜像的是 prompt 投影，可能比 `session_messages` 多一条（投影领先）；
- 退出同步与 actor 持久化可能交叉。

两者一旦漂移，`len(history)-declaredDelta` 就会落在已经入库的消息上，造成重复插入；
反过来也会跳过真正缺失的行。真实会话数据与该推导完全一致（上表）。

## 3. 修复

### 3.1 写入侧：按消息身份对齐追加起点（根治）

`canonicalAppendStartTx` / `identityAlignedAppendStart`：从 history 最新一条向前走，
停在第一条"canonical 中已存在（同 message_id 且同内容）"的消息上，只追加其后的尾部；
找不到锚点时退回原有启发式（压缩重写、整段替换等场景行为不变）。
边界：同 id 但内容不同（重试改写）不算已存在，必须追加，不能被静默丢弃。

### 3.2 读取侧：归一化已损坏的历史数据（自愈）

已经写坏的库里重复行仍在（不在用户不知情时删除数据）。所有把行物化为消息的读路径
统一折叠"相邻且完全同身份"的重复行：`loadPromptMessages`（resume 渲染）、
`loadCanonicalMessagesTx`（投影重建，随后由同事务写回，持久修复投影）、
`GetMessagePage`（历史分页）、`StreamMessages`。
仅折叠 "同 message_id + 同角色 + 同内容 + 同工具调用" 的行；无 id 的行永不折叠。

## 4. 验证

- 单元测试（`backend/internal/chat/sqlite_storage_canonical_append_test.go`）：
  5 个用例覆盖身份对齐追加、已存尾部不重复、同内容不同 id 合法重复、读路径折叠 +
  投影持久修复、重试改写不丢失。回归价值已验证：把修复 stash 掉后用例失败
  （`assistant(tool_call)` 行被跳过）。
- 真机会话副本端到端：`Load` → 9 行、`GetMessagePage` 10 行 → 8 行（折叠 2 条重复）、
  `StreamMessages` 8 行；追加一条新消息走写入路径后，canonical 只新增 1 行（seq 11），
  投影被重建为 11 行且无重复。
- `go build ./...`、`go vet ./internal/chat/`、
  `go test ./internal/chat/... ./internal/agent/... ./cmd/aicli/commands/...` 全部通过。
- 流式路径与各 readers 共用同一判定 `duplicateMessageIdentity`：同 id 的重试改写既不被
  折叠、也不会在 `StreamMessages` 中丢失；回归用例同时断言渲染视图与流式视图。

## 5. 遗留说明

`session_messages` 中历史遗留的重复行不会被自动删除（避免后台静默删改用户数据），
`Total`/`message_count` 仍为原始行数，但不再参与任何渲染；后续写入会把 prompt 投影
重建为干净版本。若需要物理清理旧库，可作为独立的离线维护动作另行实现。
