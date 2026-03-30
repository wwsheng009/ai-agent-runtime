# Team7 设计文档实现状态分析报告

> 基于 `docs/multi-agents/teams/design/team7.md`，聚焦 mailbox receipts、`ReadMailboxDigest` 和 `RunMeta`。

**分析日期**: 2026-03-15
**更新日期**: 2026-03-15
**设计文档**: `docs/multi-agents/teams/design/team7.md`

---

## 总体结论

Team7 设计里的三个核心点已全部实现：

- `RunMeta`: 已实现传播和持久化
- `ReadMailboxDigest`: 已实��� agent tool
- mailbox receipts: 已实现独立追踪机制

**综合完成度：100%** ✅

---

## 1. RunMeta ✅

### 已实现

- `RunMeta` / `TeamRunMeta` 类型：`internal/team/run_meta.go:3`
- context 注入和读取：`internal/team/context.go:219`, `internal/team/context.go:227`
- actor submit 时保存/传递 run meta：`internal/runtime/chat/actor.go:436`, `internal/runtime/chat/actor.go:882`, `internal/runtime/chat/actor.go:1070`, `internal/runtime/chat/actor.go:1119`
- teammate runner / lead planner 也会带 run meta：`internal/team/teammate_runner.go:67`, `internal/team/lead_planner.go:63`
- **持久化到 SQLite**：`internal/runtime/chat/session_runtime_store.go:206`, `session_runtime_store.go:252`, `session_runtime_store.go:294-297`, `session_runtime_store.go:336`, `session_runtime_store.go:343`
- **数据库 migration**：`internal/runtime/chat/session_runtime_store.go:521-525`
- **持久化测试**：`internal/runtime/chat/session_runtime_store_test.go:17-47`, `session_runtime_store_test.go:49-122`

### 关键实现

```go
// RuntimeState 包含 CurrentRunMeta
type RuntimeState struct {
    CurrentRunMeta *team.RunMeta `json:”current_run_meta,omitempty”`
    // ...
}

// 持久化到数据库
if state.CurrentRunMeta != nil {
    payload, err := json.Marshal(state.CurrentRunMeta)
    // INSERT/UPDATE current_run_meta_json
}
```

## 2. ReadMailboxDigest ✅

### 已实现

- `MailboxService.BuildDigest()` 服务层：`internal/team/mailbox.go:90`
- `TeammateRunner` 自动注入 digest 到任务 prompt：`internal/team/teammate_runner.go:61`
- **Agent 工具定义**：`internal/runtime/toolbroker/broker.go:160-175`
- **Agent 工具处理逻辑**：`internal/runtime/toolbroker/broker.go:539-580`
- **工具测试**：`internal/runtime/toolbroker/broker_team_test.go:134-139`

### 关键实现

```go
// 工具定义
{
    Name: ToolReadMailboxDigest,
    Description: “Read unread mailbox context for the current teammate, including broadcast messages.”,
    Parameters: {
        “team_id”: “Optional team id override”,
        “limit”: “Max messages to include”
    }
}

// 工具执行
case ToolReadMailboxDigest:
    teamID, agentID, _, err := resolveTeamScope(ctx, sessionID, request.TeamID)
    digest, err := team.NewMailboxService(b.TeamStore).BuildDigest(ctx, teamID, agentID, limit)
    return ReadMailboxDigestResult{...}
```

## 3. mailbox receipts ✅

### 已实现

- **数据库表**：`internal/team/sqlite_store.go:1496-1505`
- **AckMail 写入 receipts**：`internal/team/sqlite_store.go:1100-1104`
- **ListMail 查询 receipts**：`internal/team/sqlite_store.go:998-1001`
- **ListMailReceipts 查询**：`internal/team/sqlite_store.go:1119-1122`
- 支持每个 agent 独立已读追踪
- 支持 `globalMailReceiptAgent = “*”` 用于广播消息

### 关键实现

```sql
CREATE TABLE IF NOT EXISTS team_mailbox_receipts (
    message_id TEXT NOT NULL,
    team_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    acked_at TEXT NOT NULL,
    PRIMARY KEY (team_id, message_id, agent_id)
);

-- ListMail 的 UnreadOnly 过滤
NOT EXISTS (
    SELECT 1 FROM team_mailbox_receipts receipts
    WHERE receipts.team_id = team_mailbox_messages.team_id
      AND receipts.message_id = team_mailbox_messages.id
      AND receipts.agent_id IN (?, ?)  -- agent_id 和 “*”
)
```

### 影响

- ✅ 广播消息可以按 agent 独立已读
- ✅ 每个 agent 有独立的已读状态追踪
- ✅ 不再依赖单一 `acked_at` 字段

---

## 实现状态总结

| 设计点 | 状态 | 实现位置 |
|--------|------|----------|
| RunMeta 传播 | ✅ 完成 | `internal/runtime/chat/actor.go:436` |
| RunMeta 持久化 | ✅ 完成 | `internal/runtime/chat/session_runtime_store.go:294-297` |
| RunMeta migration | ✅ 完成 | `internal/runtime/chat/session_runtime_store.go:521-525` |
| ReadMailboxDigest 服务层 | ✅ 完成 | `internal/team/mailbox.go:90` |
| ReadMailboxDigest 工具 | ✅ 完成 | `internal/runtime/toolbroker/broker.go:539-580` |
| mailbox receipts 表 | ✅ 完成 | `internal/team/sqlite_store.go:1496-1505` |
| receipts 写入 | ✅ 完成 | `internal/team/sqlite_store.go:1100-1104` |
| receipts 查询 | ✅ 完成 | `internal/team/sqlite_store.go:998-1001` |

---

## 结论

Team7 的三个核心点已全部落地：

1. ✅ **RunMeta** - 完整的传播、持久化和 migration 支持
2. ✅ **ReadMailboxDigest** - 服务层和 agent 工具都已实现
3. ✅ **mailbox receipts** - 独立追踪机制完整实现

所有功能均已验证并通过测试，系统具备完整的团队协作和消息追踪能力。

---

*报告生成时间: 2026-03-15*
*更新时间: 2026-03-15*
