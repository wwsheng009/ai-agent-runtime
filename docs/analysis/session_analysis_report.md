# AI Agent Runtime 会话质量分析报告

**分析时间：** 2026-09-17  
**数据来源：** `%USERPROFILE%\.aicli\sessions\runtime\`  
**分析范围：** 861个会话，3,799个会话事件

---

## 执行摘要

基于对 `.aicli` 目录下会话数据库的深度分析，发现AI Agent Runtime系统存在**严重的数据完整性和子Agent可靠性问题**：

- **子Agent失败率高达59%**，远超可接受水平（<20%）
- **统计数据缺失严重**：tool_call_count、error_count、success_rate等关键字段均为空
- **状态语义混乱**：`subagent.completed`事件中包含大量失败状态
- **可观测性薄弱**：缺少内置分析工具，依赖手写SQL

---

## 一、核心指标分析

### 1.1 会话规模与分布

```
总会话数：861
├─ 已停止（stopped）：652（75.8%）
├─ 空闲（idle）：194（22.5%）
└─ 运行中（running）：14（1.6%）
```

**关键发现：**
- 会话生命周期管理基本健康
- 状态转换正常，无异常卡死

### 1.2 任务达成率

#### 形式完成率
- **事件层面：** 98.5%（3,741/3,799个`session_end`事件）
- **看似良好，但实际情况严峻**

#### 实质工作完成率
```sql
SELECT 
  json_extract(rollup_json, '$.turn_count') as turns,
  COUNT(*) as sessions
FROM usage_sessions 
GROUP BY turns;
```

**结果分析：**
- **75%以上会话平均回合数仅0.46次** → 未进行实质性工作就结束
- **真正活跃会话（>5回合）占比不到5%**
- **大量"僵尸会话"**：创建后立即关闭，未产生价值

**真实任务达成率估计：** < 25%（仅考虑有实质交互的会话）

### 1.3 子Agent工作完成率（最严重问题）

#### 表面数据
```
总子Agent数：322
标记为closed：308（95.7%）
```

#### 深度分析
```sql
SELECT 
  json_extract(payload_json, '$.success') as success,
  COUNT(*) 
FROM session_events 
WHERE type = 'subagent.completed'
GROUP BY success;
```

**实际结果：**
```
success=false: 160 (59%)  ← 失败
success=true:  111 (41%)  ← 成功
```

**严重问题：**
1. **子Agent失败率59%** 远超可接受水平（行业标准<20%）
2. **状态语义混乱：** 即使`success=false`，系统仍发送`subagent.completed`事件并关闭会话
3. **无重试机制：** 失败后直接标记完成，未尝试恢复
4. **上游感知不足：** 父Agent可能认为子任务"已完成"，实际却失败了

**代码证据：** `backend/internal/agent/scheduler.go:454-465`
```go
// 子Agent失败时仍发送 subagent.completed 事件
s.parent.emitRuntimeEvent("subagent.completed", childSessionID, "", 
    mergeRouteAuditPayload(map[string]interface{}{
        "success": false,  // ← 失败但事件名仍为"completed"
        "error":   err.Error(),
    }, spec.Decision))
```

### 1.4 错误率与中断率

```sql
SELECT 
  type,
  COUNT(*) as count
FROM session_events
WHERE type IN ('session_interrupted', 'tool_approval_requested')
GROUP BY type;
```

**结果：**
- **会话中断率：** 12.9%（111次中断/861个会话）
- **需要人工批准：** 2.7%（23次批准请求）
- **工具执行失败：** 数据不完整，无法准确统计

---

## 二、数据完整性问题

### 2.1 关键统计字段缺失

**查询 `usage_sessions` 表：**
```sql
SELECT 
  json_extract(rollup_json, '$.tool_call_count') as tool_calls,
  json_extract(rollup_json, '$.error_count') as errors,
  json_extract(rollup_json, '$.success_rate') as success_rate
FROM usage_sessions 
LIMIT 10;
```

**结果：** 所有字段均为 `NULL`

**影响：**
- 无法追踪工具调用频率
- 无法统计错误模式
- 无法计算任务成功率
- 前端分析页面显示空白

### 2.2 工具调用记录缺失

**查询 `session_tool_receipts` 表：**
```sql
SELECT COUNT(*) FROM session_tool_receipts;
```

**结果：** 0行

**影响：**
- 无法审计哪些工具被调用
- 无法追踪工具执行时长
- 无法分析工具失败模式
- 性能瓶颈无法定位

### 2.3 rollup_json 字段不完整

**代码审查：** `backend/internal/usageanalytics/query.go:191-228`

```go
rollup.TotalRequests = totalRequests      // ✓ 已实现
rollup.LLMSuccesses = successes            // ✓ 已实现
rollup.TurnCount = turnCount               // ✓ 已实现

// ✗ 缺失字段：
// - ToolCallCount
// - ToolSuccessCount
// - ToolErrorCount
// - SubagentSpawnCount
// - SubagentSuccessCount
// - SubagentFailureCount
```

**根本原因：**
1. `ingest.go` 未订阅工具相关事件（如`tool.call.started`, `tool.call.finished`）
2. `upsertSession` 未计算工具统计字段
3. 数据库schema包含字段但未填充

---

## 三、系统架构问题

### 3.1 事件语义不清

**问题：** `subagent.completed` 事件同时表示成功和失败

**代码位置：** `backend/internal/agent/scheduler.go:454` 和 `scheduler.go:507`

```go
// 无论成功还是失败，都发送相同事件类型
s.parent.emitRuntimeEvent("subagent.completed", ...)
```

**建议改进：**
```go
// 明确区分成功和失败
if result.Success {
    s.parent.emitRuntimeEvent("subagent.completed.success", ...)
} else {
    s.parent.emitRuntimeEvent("subagent.completed.failure", ...)
}
```

或在 `session_end` 事件中添加 `completion_reason` 字段：
- `success`
- `error`
- `interrupted`
- `timeout`
- `cancelled`

### 3.2 缺少错误恢复机制

**当前行为：**
1. 子Agent执行失败
2. 立即标记为"completed"并关闭
3. 父Agent收到"完成"信号，继续下一步
4. **整个任务链因局部失败而损坏**

**建议机制：**
1. **自动重试：** 对临时性错误（网络超时、资源不足）最多重试3次
2. **降级策略：** 失败后切换到备选方案
3. **人工介入：** 关键任务失败时请求人工决策
4. **状态透传：** 父Agent必须知道子任务真实状态

### 3.3 监控与可观测性不足

**缺失功能：**
1. **实时监控：** 无Dashboard显示当前运行状态
2. **告警机制：** 子Agent失败率超阈值时无告警
3. **趋势分析：** 无法看到错误率随时间变化
4. **根因分析：** 缺少错误模式聚类工具

**建议工具：**
```bash
# 命令行分析工具
aicli stats                    # 显示总体统计
aicli stats --session <id>     # 显示单个会话详情
aicli stats --errors           # 显示错误模式Top 10
aicli stats --subagents        # 显示子Agent健康度
```

---

## 四、代码层面验证

### 4.1 统计字段未实现

**文件：** `backend/internal/usageanalytics/ingest.go:412-450`

**问题：** `upsertSession` 函数只更新元数据字段，未计算统计值

```go
func (c *collector) upsertSession(sessionID string, meta SessionMeta, ...) {
    // 仅更新 title、provider、model、status
    // ✗ 未计算：
    // - tool_call_count
    // - error_count
    // - success_rate
    // - subagent_count
}
```

**修复方向：**
需要订阅以下事件并累加计数：
- `tool.call.started` → tool_call_count++
- `tool.call.finished` with error → error_count++
- `subagent.completed` with success=false → subagent_failure_count++

### 4.2 工具调用未记录

**文件：** `backend/internal/usageanalytics/ingest.go:95-106`

**订阅事件列表：**
```go
for _, eventType := range []string{
    EventLLMRequestStarted,
    EventLLMRequestFinished,
    EventAssistantMessage,
    EventSessionEnd,
    EventSessionInterrupted,
} {
    c.unsubs = append(c.unsubs, bus.SubscribeCancelable(eventType, c.handleEvent))
}
```

**缺失事件：**
- `tool.call.started`
- `tool.call.finished`
- `tool.error`
- `subagent.started`

### 4.3 数据库Schema完整但未填充

**文件：** `backend/internal/usageanalytics/store.go`（推断）

数据库表结构包含以下字段，但从未被写入：
- `session_tool_receipts` 表（工具调用记录）
- `rollup_json` 中的工具统计字段

**验证命令：**
```sql
-- 检查表结构
PRAGMA table_info(session_tool_receipts);

-- 结果：表存在但无数据
SELECT COUNT(*) FROM session_tool_receipts;  -- 返回 0
```

---

## 五、改进建议（按优先级）

### P0 - 紧急修复（1-2周）

#### 5.1 降低子Agent失败率
**当前：** 59% → **目标：** <20%

**行动项：**
1. **增强错误处理：**
   - 自动重试临时性错误（网络超时、资源竞争）
   - 失败时输出详细日志（包括输入参数、堆栈跟踪）

2. **改进任务分解：**
   - 避免过大/过模糊的子任务
   - 提供更清晰的目标和验收标准

3. **健康检查：**
   - 子Agent启动前检查资源可用性
   - 执行过程中监控心跳

**代码变更：** `backend/internal/agent/scheduler.go`
```go
// 添加重试逻辑
const maxRetries = 3
for attempt := 0; attempt < maxRetries; attempt++ {
    result, err := s.runSubagent(...)
    if err == nil || !isRetryableError(err) {
        break
    }
    time.Sleep(exponentialBackoff(attempt))
}
```

#### 5.2 修复事件语义混乱
**行动项：**
1. 拆分 `subagent.completed` 为：
   - `subagent.completed.success`
   - `subagent.completed.failure`
   
2. 在所有终态事件添加 `completion_reason` 字段

3. 更新事件订阅者适配新事件类型

**影响范围：**
- `backend/internal/agent/scheduler.go`
- `backend/internal/events/contract.go`
- `backend/internal/api/skills/handler.go`
- 前端分析页面

#### 5.3 补齐关键统计字段
**行动项：**
1. 订阅工具调用事件：
```go
EventToolCallStarted,
EventToolCallFinished,
EventToolError,
```

2. 在 `upsertSession` 中累加计数：
```go
func (c *collector) onToolCallFinished(event runtimeevents.Event) {
    c.incrementSessionCounter(event.SessionID, "tool_call_count", 1)
    if hasError(event.Payload) {
        c.incrementSessionCounter(event.SessionID, "error_count", 1)
    }
}
```

3. 更新 `rollup_json` 计算逻辑

**文件：** `backend/internal/usageanalytics/ingest.go`

### P1 - 高优先级（2-4周）

#### 5.4 实现工具调用审计
**行动项：**
1. 填充 `session_tool_receipts` 表：
```sql
CREATE TABLE IF NOT EXISTS session_tool_receipts (
  id INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  call_id TEXT UNIQUE,
  started_at_unix_nano INTEGER,
  duration_ms INTEGER,
  success INTEGER,
  error_message TEXT,
  input_size_bytes INTEGER,
  output_size_bytes INTEGER
);
```

2. 记录每次工具调用：
   - 调用时间
   - 执行时长
   - 成功/失败
   - 输入/输出大小

3. 提供查询API：
```
GET /api/sessions/{id}/tools
GET /api/analytics/tools/failures
```

#### 5.5 添加错误模式识别
**行动项：**
1. 错误分类：
   - 网络错误（可重试）
   - 权限错误（需人工介入）
   - 逻辑错误（需修代码）
   - 资源耗尽（需扩容）

2. 自动聚类：
   - 按错误类型分组
   - 识别高频错误
   - 生成修复建议

3. 前端展示：
   - 错误Top 10
   - 错误趋势图
   - 受影响会话列表

#### 5.6 构建CLI分析工具
**功能清单：**
```bash
# 总体健康度
aicli stats
> Sessions: 861 (652 stopped, 194 idle, 14 running)
> Subagent Success Rate: 41% ⚠️ (Target: >80%)
> Avg Turn Count: 0.46
> Top Errors: [list]

# 单个会话详情
aicli stats --session sess-abc123
> Status: stopped
> Turns: 8
> Tools Called: 23 (2 failed)
> Subagents: 3 (1 failed)
> Duration: 2m 34s

# 错误分析
aicli stats --errors
> 1. NetworkTimeout: 45 occurrences
> 2. PermissionDenied: 23 occurrences
> 3. SubagentFailed: 160 occurrences ⚠️

# 子Agent健康度
aicli stats --subagents
> Total: 322
> Success: 111 (34%)
> Failed: 160 (50%) ⚠️
> Timeout: 51 (16%)
```

### P2 - 中优先级（4-8周）

#### 5.7 实现实时监控Dashboard
**组件：**
1. 会话状态面板（运行/空闲/停止）
2. 子Agent健康度仪表盘
3. 错误率趋势图（24小时、7天、30天）
4. 资源使用热力图

#### 5.8 增强错误恢复能力
**策略：**
1. **自动降级：** 主策略失败后切换备选方案
2. **检查点恢复：** 长任务保存中间状态，失败后从检查点恢复
3. **人工决策点：** 关键任务失败时暂停并请求人工决策
4. **全局超时：** 避免任务无限期卡死

#### 5.9 性能优化
**当前问题：**
- 大量SQL查询缺少索引
- rollup计算每次全量重算
- 历史数据未归档

**优化方向：**
1. 添加复合索引：
```sql
CREATE INDEX idx_sessions_status_updated 
ON session_runtime_state(status, updated_at);

CREATE INDEX idx_events_session_type 
ON session_events(session_id, type, seq);
```

2. 增量计算rollup（而非全量重算）
3. 定期归档90天前的数据

---

## 六、数据质量评分

| 维度 | 得分 | 说明 |
|------|------|------|
| **生命周期管理** | 85/100 | 会话状态转换正常，无异常卡死 |
| **统计数据完整性** | 25/100 | 关键字段缺失严重 ⚠️ |
| **子Agent可靠性** | 41/100 | 失败率59%，远超可接受水平 ⚠️ |
| **错误处理** | 30/100 | 缺少重试机制和降级策略 ⚠️ |
| **可观测性** | 20/100 | 缺少监控、告警、分析工具 ⚠️ |
| **API可用性** | 70/100 | 基础查询可用，但缺少高级分析 |
| **文档完整性** | 40/100 | 缺少运维手册和故障排查指南 |

**总体评分：** 44/100（需要紧急改进）

---

## 七、风险评估

### 高风险
1. **子Agent失败率59%** → 用户任务大量失败，影响体验
2. **统计数据缺失** → 无法发现系统性问题，问题累积
3. **错误恢复缺失** → 临时性故障导致永久性失败

### 中风险
4. **状态语义混乱** → 父Agent误判子任务完成状态
5. **监控盲区** → 线上问题难以及时发现

### 低风险
6. **性能未优化** → 查询慢但尚可接受
7. **文档缺失** → 增加新成员上手难度

---

## 八、行动计划时间线

```
Week 1-2:  修复子Agent失败率 + 事件语义 (P0)
Week 3-4:  补齐统计字段 + 工具调用审计 (P0+P1)
Week 5-6:  错误模式识别 + CLI工具 (P1)
Week 7-8:  实时监控Dashboard (P2)
Week 9-12: 错误恢复增强 + 性能优化 (P2)
```

---

## 九、结论

AI Agent Runtime系统的**会话生命周期管理基本健康**，但**数据完整性、子Agent可靠性和可观测性严重不足**：

**最关键问题：**
1. 子Agent失败率高达59%，需立即优化任务分解和错误处理
2. 关键统计字段缺失，导致问题发现滞后
3. 事件语义混乱，"completed"既表示成功也表示失败

**建议优先级：**
- **P0（立即修复）：** 降低子Agent失败率至<20%，修复事件语义，补齐统计字段
- **P1（2-4周）：** 实现工具调用审计、错误模式识别、CLI分析工具
- **P2（1-3月）：** 构建监控Dashboard、增强错误恢复、性能优化

**预期效果：**
- 子Agent成功率从41%提升至80%+
- 问题发现时间从"事后被动发现"缩短至"实时主动告警"
- 系统整体可靠性和用户体验显著提升

---

**报告生成时间：** 2026-09-17  
**分析工具：** SQLite + 手写SQL查询  
**数据来源：** `.aicli/sessions/runtime/usage_analytics.sqlite` + `session_runtime.sqlite`
