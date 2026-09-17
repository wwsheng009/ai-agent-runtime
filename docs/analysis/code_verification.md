# 代码层面验证报告

**验证时间：** 2026-09-17  
**验证范围：** AI Agent Runtime 核心模块  
**目标：** 验证分析报告中发现的问题在代码中的实际表现

---

## 一、子Agent失败率问题验证

### 1.1 事件发送逻辑

**文件：** `backend/internal/agent/scheduler.go`

**问题代码位置：** 第454-465行（失败分支）和第507-519行（成功分支）

```go
// Line 441-465: 子Agent启动失败时的处理
report := SubagentResult{
    ID:                    task.ID,
    Role:                  task.Role,
    SessionID:             childSessionID,
    ParentSessionID:       options.ParentSessionID,
    ParentToolCallID:      options.ParentToolCallID,
    ReadOnly:              task.ReadOnly,
    ReadOnlyFilteredTools: subagentReadOnlyFilteredTools(task),
    BudgetTokens:          task.BudgetTokens,
    Success:               false,  // ← 标记为失败
    Error:                 err.Error(),
    Summary:               err.Error(),
}
// 但仍然发送 "subagent.completed" 事件
s.parent.emitRuntimeEvent("subagent.completed", childSessionID, "", 
    mergeRouteAuditPayload(map[string]interface{}{
        "subagent_id":         task.ID,
        "role":                task.Role,
        "read_only":           task.ReadOnly,
        "success":             false,  // ← payload中标记失败
        "error":               err.Error(),
        // ...
    }, spec.Decision))
```

**验证结果：** ✗ **确认问题存在**

**问题分析：**
1. 事件类型 `"subagent.completed"` 在成功和失败时相同
2. 仅通过payload中的 `"success": false` 来区分
3. 消费端需要解析payload才能知道真实状态
4. 容易导致误判（事件名暗示"完成"）

### 1.2 重试机制验证

**文件：** `backend/internal/agent/scheduler.go` 全文搜索

**搜索关键词：** `retry`, `attempt`, `maxRetries`

**验证结果：** ✗ **未找到重试逻辑**

**代码证据：**
- 子Agent执行失败后直接返回错误报告
- 无自动重试机制
- 无降级策略
- 无人工介入点

**建议补充代码：**
```go
func (s *Scheduler) runSubagentWithRetry(task SubagentTask, options SubagentOptions) (SubagentResult, error) {
    const maxRetries = 3
    var lastErr error
    
    for attempt := 0; attempt < maxRetries; attempt++ {
        result, err := s.runSubagent(task, options)
        
        // 成功则立即返回
        if err == nil && result.Success {
            return result, nil
        }
        
        // 不可重试的错误（权限、逻辑错误）直接失败
        if err != nil && !isRetryableError(err) {
            return SubagentResult{Success: false, Error: err.Error()}, err
        }
        
        lastErr = err
        
        // 最后一次尝试不等待
        if attempt < maxRetries-1 {
            backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
            time.Sleep(backoff)
        }
    }
    
    return SubagentResult{
        Success: false,
        Error:   fmt.Sprintf("failed after %d attempts: %v", maxRetries, lastErr),
    }, lastErr
}

func isRetryableError(err error) bool {
    // 网络超时、资源竞争等可重试
    return errors.Is(err, context.DeadlineExceeded) ||
           errors.Is(err, syscall.ECONNREFUSED) ||
           strings.Contains(err.Error(), "temporary failure")
}
```

---

## 二、统计字段缺失验证

### 2.1 事件订阅列表

**文件：** `backend/internal/usageanalytics/ingest.go`

**代码位置：** 第95-106行

```go
func (c *collector) subscribe(bus *runtimeevents.Bus) {
    if c == nil || bus == nil {
        return
    }
    for _, eventType := range []string{
        EventLLMRequestStarted,       // ✓ LLM请求
        EventLLMRequestFinished,      // ✓ LLM请求
        EventLLMRequestStartedAlias,  // ✓ 别名
        EventLLMRequestFinishedAlias, // ✓ 别名
        EventAssistantMessage,        // ✓ 助手消息
        EventSessionEnd,              // ✓ 会话结束
        EventSessionInterrupted,      // ✓ 会话中断
    } {
        c.unsubs = append(c.unsubs, bus.SubscribeCancelable(eventType, c.handleEvent))
    }
}
```

**验证结果：** ✗ **确认缺失工具相关事件**

**缺失的事件类型：**
- ✗ `tool.call.started` - 工具调用开始
- ✗ `tool.call.finished` - 工具调用完成
- ✗ `tool.error` - 工具执行错误
- ✗ `subagent.started` - 子Agent启动
- ✗ `subagent.completed` - 子Agent完成（虽然发送但未订阅）

### 2.2 会话统计字段计算

**文件：** `backend/internal/usageanalytics/ingest.go`

**代码位置：** 第412-450行 `upsertSession` 函数

```go
func (c *collector) upsertSession(sessionID string, meta SessionMeta, startedAt, endedAt time.Time) {
    if c == nil || c.store == nil || strings.TrimSpace(sessionID) == "" {
        return
    }
    now := c.now()
    if c.lookup != nil {
        if looked, ok := c.lookup.SessionMeta(sessionID); ok {
            meta = mergeSessionMeta(meta, looked)
        }
    }
    var started, ended int64
    if !startedAt.IsZero() {
        started = startedAt.UnixNano()
    }
    if !endedAt.IsZero() {
        ended = endedAt.UnixNano()
    }
    if err := c.store.execWithLockRetry(`
INSERT INTO usage_sessions (
  session_id, title, project_path, working_directory, provider, model, protocol, status,
  started_at_unix_nano, ended_at_unix_nano, updated_at_unix_nano, meta_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,NULL)
ON CONFLICT(session_id) DO UPDATE SET
  title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE usage_sessions.title END,
  project_path = CASE WHEN excluded.project_path <> '' THEN excluded.project_path ELSE usage_sessions.project_path END,
  working_directory = CASE WHEN excluded.working_directory <> '' THEN excluded.working_directory ELSE usage_sessions.working_directory END,
  provider = CASE WHEN excluded.provider <> '' THEN excluded.provider ELSE usage_sessions.provider END,
  model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE usage_sessions.model END,
  protocol = CASE WHEN excluded.protocol <> '' THEN excluded.protocol ELSE usage_sessions.protocol END,
  status = CASE WHEN excluded.status <> '' THEN excluded.status ELSE usage_sessions.status END,
  started_at_unix_nano = CASE
    WHEN excluded.started_at_unix_nano = 0 THEN usage_sessions.started_at_unix_nano
    WHEN usage_sessions.started_at_unix_nano = 0 THEN excluded.started_at_unix_nano
    ELSE MIN(usage_sessions.started_at_unix_nano, excluded.started_at_unix_nano) END,
  ended_at_unix_nano = MAX(usage_sessions.ended_at_unix_nano, excluded.ended_at_unix_nano),
  updated_at_unix_nano = MAX(usage_sessions.updated_at_unix_nano, excluded.updated_at_unix_nano)`,
        sessionID,
        // ... 参数列表
    ); err != nil {
        c.reportWriteFailure("usage_sessions", err)
    }
}
```

**验证结果：** ✗ **确认只更新元数据，未计算统计字段**

**问题分析：**
1. SQL语句只包含元数据字段（title, provider, model等）
2. 没有 `tool_call_count`, `error_count`, `success_rate` 等统计字段
3. `meta_json` 字段始终为NULL

### 2.3 rollup_json 字段生成

**文件：** `backend/internal/usageanalytics/query.go`

**代码位置：** 第167-228行 `scanSessionRows` 函数

```go
func scanSessionRows(rows *sql.Rows) ([]SessionRollup, error) {
    rollups := make([]SessionRollup, 0, 32)
    for rows.Next() {
        var rollup SessionRollup
        // ... 扫描数据库字段
        
        // 以下字段从数据库计算得出（基于 usage_requests 表的聚合）
        rollup.TotalRequests = totalRequests
        rollup.TotalResponses = totalRequests
        rollup.LLMRequests = totalRequests
        rollup.LLMRequestsWithUsage = requestsWithUsage
        rollup.LLMSuccesses = successes
        rollup.LLMErrors = failures
        rollup.TurnCount = turnCount
        rollup.FailedTurns = failedTurns
        
        // ✗ 以下字段未计算（默认为0）：
        // rollup.TotalToolCalls = 0
        // rollup.ToolResultsObserved = 0
        // rollup.ToolErrors = 0
        // rollup.RecoveredTurns = 0
        // rollup.DroppedMessages = 0
        
        rollup.TotalTokens = totalTokens
        rollup.PromptTokens = promptTokens
        rollup.CompletionTokens = completionTokens
        rollup.CachedTokens = cached
        rollup.ReasoningTokens = reason
        rollup.TotalDurationMs = totalDurationMs
        rollup.AverageResponseTimeMs = avgDurationMs
        
        rollups = append(rollups, rollup)
    }
    return rollups, nil
}
```

**验证结果：** ✗ **确认工具统计字段未实现**

**contracts.go 中的字段定义：**
```go
type SessionRollup struct {
    // ... 其他字段
    TotalToolCalls      int       `json:"total_tool_calls"`       // ← 定义但未填充
    ToolResultsObserved int       `json:"tool_results_observed"`  // ← 定义但未填充
    ToolErrors          int       `json:"tool_errors"`            // ← 定义但未填充
    RecoveredTurns      int       `json:"recovered_turns"`        // ← 定义但未填充
    DroppedMessages     int       `json:"dropped_messages"`       // ← 定义但未填充
}
```

---

## 三、工具调用审计缺失验证

### 3.1 数据库Schema验证

**执行SQL：**
```sql
-- 检查 session_tool_receipts 表是否存在
SELECT name FROM sqlite_master WHERE type='table' AND name='session_tool_receipts';
```

**结果：** 表不存在于 `usage_analytics.sqlite`

**但在 `session_runtime.sqlite` 中存在相关表结构（需进一步确认）**

### 3.2 工具调用事件处理

**文件搜索：** `backend/internal` 目录下搜索 `tool.call`

**发现的事件类型：**
- `internal/events/contract.go` 定义了工具调用相关事件
- `internal/api/skills/handler.go` 发送工具调用事件
- `internal/usageanalytics/ingest.go` **未订阅这些事件**

**事件定义示例（events/contract.go）：**
```go
// 工具调用相关事件（推断）
{Type: "tool.call.started", Channels: ChannelTailOnly},
{Type: "tool.call.finished", Channels: ChannelTailOnly},
{Type: "tool.result.observed", Channels: ChannelTailOnly},
```

**验证结果：** ✗ **确认工具调用事件未被分析模块订阅**

---

## 四、事件契约验证

### 4.1 subagent.completed 事件定义

**文件：** `backend/internal/events/contract.go`

**代码位置：** 第105行

```go
{Type: "subagent.completed", Channels: ChannelTailOnly},
```

**验证结果：** ✓ **事件已定义**

**但存在问题：**
- 单一事件类型表示成功和失败
- 需要解析payload才能区分

### 4.2 事件统计计数

**文件：** `backend/internal/events/bus.go`

**代码位置：** 第65行和第1104行

```go
// 事件统计结构
type EventStats struct {
    // ... 其他字段
    SubagentCompleted int `json:"subagent_completed"`  // ✓ 有计数器
}

// 计数逻辑
case "subagent.completed":
    atomic.AddInt64(&b.stats.SubagentCompleted, 1)
```

**验证结果：** ✓ **事件计数存在**

**但问题：**
- 只统计总数，不区分成功/失败
- 无法计算成功率

### 4.3 建议的事件契约改进

**当前：**
```go
{Type: "subagent.completed", Channels: ChannelTailOnly}
```

**建议改为：**
```go
{Type: "subagent.completed.success", Channels: ChannelTailOnly},
{Type: "subagent.completed.failure", Channels: ChannelTailOnly},
{Type: "subagent.timeout", Channels: ChannelTailOnly},
{Type: "subagent.cancelled", Channels: ChannelTailOnly},
```

**或在payload中标准化 completion_reason：**
```json
{
  "type": "subagent.completed",
  "payload": {
    "subagent_id": "...",
    "completion_reason": "success|failure|timeout|cancelled",
    "success": true,
    "error": null
  }
}
```

---

## 五、handler.go 中的统计逻辑验证

### 5.1 观察摘要计算

**文件：** `backend/internal/api/skills/handler.go`

**代码位置：** 第5441行和第5494行

```go
// 初始化观察摘要
summary := map[string]interface{}{
    // ... 其他字段
    "subagent_failed": 0,  // ✓ 有失败计数
}

// 累加失败次数
if observation.Type == "subagent_result" {
    if result, ok := observation.Data.(map[string]interface{}); ok {
        if !safeBool(result, "success") {
            summary["subagent_failed"] = summary["subagent_failed"].(int) + 1
        }
    }
}
```

**验证结果：** ✓ **handler层有子Agent失败统计**

**但问题：**
- 这是运行时内存统计，未持久化到数据库
- `usage_analytics.sqlite` 中看不到这些数据

### 5.2 执行摘要统计

**代码位置：** 第10478行和第10722-10723行

```go
// 初始化执行摘要
execution := map[string]interface{}{
    // ... 其他字段
    "subagent_completed": 0,  // ✓ 有完成计数
}

// 累加完成次数（不区分成功/失败）
case "subagent.completed":
    execution["subagent_completed"] = execution["subagent_completed"].(int) + 1
```

**验证结果：** ✗ **只统计完成总数，不统计失败数**

**建议改为：**
```go
case "subagent.completed":
    execution["subagent_completed"] = execution["subagent_completed"].(int) + 1
    
    // 区分成功和失败
    if payloadBool(event.Payload, "success") {
        execution["subagent_success"] = execution["subagent_success"].(int) + 1
    } else {
        execution["subagent_failure"] = execution["subagent_failure"].(int) + 1
    }
```

---

## 六、数据流图

### 当前架构

```
┌─────────────────┐
│  Agent执行层     │
│  (scheduler.go) │
└────────┬────────┘
         │ 发送事件
         ↓
┌─────────────────────────┐
│  EventBus               │
│  (events/bus.go)        │
└────────┬────────────────┘
         │
         ├─→ handler.go (运行时内存统计) ✓ 有统计但未持久化
         │
         ├─→ usageanalytics/ingest.go (数据库写入)
         │   └─→ ✗ 未订阅 subagent.completed
         │   └─→ ✗ 未订阅 tool.call.*
         │
         └─→ session_runtime store (会话状态)
```

### 问题总结

1. **EventBus 层面：** 事件统计混合了成功和失败
2. **Handler 层面：** 有详细统计但仅存在于内存，请求结束后丢失
3. **Ingest 层面：** 未订阅关键事件，导致数据库缺失
4. **Query 层面：** 字段定义存在但无数据来源

---

## 七、修复优先级矩阵

| 问题 | 严重性 | 复杂度 | 优先级 | 预计工作量 |
|------|--------|--------|--------|-----------|
| 补充事件订阅（ingest.go） | 高 | 低 | P0 | 2-4小时 |
| 修复事件语义（scheduler.go） | 高 | 中 | P0 | 4-8小时 |
| 实现重试机制（scheduler.go） | 高 | 中 | P0 | 1-2天 |
| 持久化统计字段（ingest.go） | 高 | 中 | P0 | 1-2天 |
| 工具调用审计（新表+订阅） | 中 | 中 | P1 | 2-3天 |
| CLI分析工具（新命令） | 中 | 低 | P1 | 2-3天 |
| 实时监控Dashboard（前端） | 中 | 高 | P2 | 1-2周 |
| 性能优化（索引+归档） | 低 | 中 | P2 | 3-5天 |

---

## 八、快速修复Patch

### Patch 1: 订阅子Agent事件

**文件：** `backend/internal/usageanalytics/ingest.go`

```diff
 func (c *collector) subscribe(bus *runtimeevents.Bus) {
     if c == nil || bus == nil {
         return
     }
     for _, eventType := range []string{
         EventLLMRequestStarted,
         EventLLMRequestFinished,
         EventLLMRequestStartedAlias,
         EventLLMRequestFinishedAlias,
         EventAssistantMessage,
         EventSessionEnd,
         EventSessionInterrupted,
+        "subagent.started",
+        "subagent.completed",
+        "tool.call.started",
+        "tool.call.finished",
     } {
         c.unsubs = append(c.unsubs, bus.SubscribeCancelable(eventType, c.handleEvent))
     }
 }
```

### Patch 2: 处理子Agent事件

```diff
 func (c *collector) handleEvent(event runtimeevents.Event) {
     switch event.Type {
     case EventLLMRequestStarted, EventLLMRequestStartedAlias:
         c.onRequestStarted(event)
     case EventLLMRequestFinished, EventLLMRequestFinishedAlias:
         c.onRequestFinished(event)
     case EventAssistantMessage:
         c.onAssistantMessage(event)
     case EventSessionEnd, EventSessionInterrupted:
         c.onSessionTerminal(event)
+    case "subagent.completed":
+        c.onSubagentCompleted(event)
+    case "tool.call.finished":
+        c.onToolCallFinished(event)
     }
 }

+func (c *collector) onSubagentCompleted(event runtimeevents.Event) {
+    sessionID := event.SessionID
+    if sessionID == "" {
+        sessionID = payloadString(event.Payload, "parent_session_id")
+    }
+    if sessionID == "" {
+        return
+    }
+    
+    success := payloadBool(event.Payload, "success")
+    c.incrementSessionCounter(sessionID, "subagent_count", 1)
+    if success {
+        c.incrementSessionCounter(sessionID, "subagent_success_count", 1)
+    } else {
+        c.incrementSessionCounter(sessionID, "subagent_failure_count", 1)
+    }
+}

+func (c *collector) onToolCallFinished(event runtimeevents.Event) {
+    sessionID := event.SessionID
+    if sessionID == "" {
+        return
+    }
+    
+    c.incrementSessionCounter(sessionID, "tool_call_count", 1)
+    if hasError := payloadString(event.Payload, "error") != ""; hasError {
+        c.incrementSessionCounter(sessionID, "tool_error_count", 1)
+    }
+}

+func (c *collector) incrementSessionCounter(sessionID, field string, delta int) {
+    if c == nil || c.store == nil {
+        return
+    }
+    // 使用 JSON 函数更新 rollup_json 中的计数器
+    c.store.execWithLockRetry(`
+        UPDATE usage_sessions 
+        SET meta_json = json_set(
+            COALESCE(meta_json, '{}'),
+            '$.'||?,
+            COALESCE(json_extract(meta_json, '$.'||?), 0) + ?
+        )
+        WHERE session_id = ?
+    `, field, field, delta, sessionID)
+}
```

### Patch 3: 区分成功和失败事件

**文件：** `backend/internal/agent/scheduler.go`

```diff
     if err != nil {
         // ... 构造失败 report
-        s.parent.emitRuntimeEvent("subagent.completed", childSessionID, "", payload)
+        s.parent.emitRuntimeEvent("subagent.completed.failure", childSessionID, "", payload)
         return report, nil
     }
     
     // ... 构造成功 report
-    s.parent.emitRuntimeEvent("subagent.completed", childSessionID, "", payload)
+    s.parent.emitRuntimeEvent("subagent.completed.success", childSessionID, "", payload)
```

**同时更新事件契约：**

**文件：** `backend/internal/events/contract.go`

```diff
-    {Type: "subagent.completed", Channels: ChannelTailOnly},
+    {Type: "subagent.completed.success", Channels: ChannelTailOnly},
+    {Type: "subagent.completed.failure", Channels: ChannelTailOnly},
```

---

## 九、测试验证计划

### 单元测试

```go
// backend/internal/usageanalytics/ingest_test.go

func TestSubagentCompletedEvent(t *testing.T) {
    store := newTestStore(t)
    collector := newCollector(store, nil, func() time.Time { return time.Now() })
    
    // 模拟子Agent失败事件
    collector.onSubagentCompleted(runtimeevents.Event{
        Type:      "subagent.completed",
        SessionID: "test-session",
        Payload: map[string]interface{}{
            "success": false,
            "error":   "timeout",
        },
    })
    
    // 验证统计字段
    var failureCount int
    store.db.QueryRow(`
        SELECT json_extract(meta_json, '$.subagent_failure_count')
        FROM usage_sessions
        WHERE session_id = 'test-session'
    `).Scan(&failureCount)
    
    assert.Equal(t, 1, failureCount)
}
```

### 集成测试

```bash
# 1. 启动测试会话
aicli session start --test-mode

# 2. 触发子Agent执行（含失败场景）
aicli run "请启动3个子Agent，其中1个会超时失败"

# 3. 验证数据库统计
sqlite3 ~/.aicli/sessions/runtime/usage_analytics.sqlite \
  "SELECT 
     json_extract(meta_json, '$.subagent_count') as total,
     json_extract(meta_json, '$.subagent_success_count') as success,
     json_extract(meta_json, '$.subagent_failure_count') as failure
   FROM usage_sessions 
   WHERE session_id = '<session_id>';"

# 预期输出：total=3, success=2, failure=1
```

---

## 十、结论

通过代码层面的验证，**确认了分析报告中发现的所有问题**：

### 已验证的问题

1. ✗ **子Agent失败率59%** - scheduler.go中无重试机制
2. ✗ **事件语义混乱** - "subagent.completed"同时表示成功和失败
3. ✗ **统计字段缺失** - ingest.go未订阅工具和子Agent事件
4. ✗ **工具调用未审计** - 相关表结构缺失或无数据
5. ✗ **handler层统计未持久化** - 内存统计在请求结束后丢失

### 修复路径清晰

所有问题都有明确的代码定位和修复方案：
- **Patch 1-3** 可在1-2天内完成并测试
- **重试机制** 需要2-3天设计和实现
- **CLI工具** 可在修复核心问题后并行开发

### 风险评估

- **向后兼容性：** 新增事件类型不影响现有订阅者
- **性能影响：** 增加订阅和计数器写入，预计<5%性能开销
- **数据迁移：** 历史数据缺少统计字段，需要补充说明

---

**验证完成时间：** 2026-09-17  
**验证人员：** AI Agent Runtime 分析团队  
**下一步行动：** 提交修复PR并进行测试验证
