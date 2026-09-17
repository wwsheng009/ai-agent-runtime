# AI Agent Runtime 会话分析报告

**分析时间：** 2026-09-17  
**数据来源：** `%USERPROFILE%\.aicli\sessions\runtime`  
**分析方法：** SQLite数据库查询 + 统计分析

---

## 执行摘要

本报告基于对 `.aicli` 会话数据库的深度分析，发现当前AI Agent Runtime系统在**子Agent成功率、数据收集完整性和可观测性**方面存在严重问题。虽然系统基础功能运行正常，但**59%的子Agent失败率**和**关键统计字段缺失**严重影响了系统的实用性和可靠性。

### 关键发现

- ✅ 会话生命周期管理基本完善（98.5%完成率）
- ⚠️ 大部分会话未进行实质性工作（75%会话回合数为0）
- ❌ **子Agent实际成功率仅37%**（远低于95.7%的表面完成率）
- ❌ 工具调用、错误统计等核心指标缺失
- ❌ 缺少内置分析工具和错误追踪机制

---

## 一、总体会话统计

### 1.1 会话规模

**数据来源：** `session_runtime_state` 表

| 指标 | 数值 | 占比 |
|------|------|------|
| 总会话数 | 861 | 100% |
| 已停止 (stopped) | 653 | 75.8% |
| 空闲状态 (idle) | 194 | 22.5% |
| 运行中 (running) | 14 | 1.6% |

### 1.2 活跃度分析

**数据来源：** `analytics_sessions` 表

| 指标 | 数值 |
|------|------|
| 分析会话总数 | 4,131 |
| 总回合数 | 1,891 |
| 平均每会话回合数 | 0.46 |
| 有效回合会话占比 | ~25% |

**关键洞察：**
- 75%以上的会话回合数为0，表明大量会话在未进行实质性工作前就结束
- 仅约5%的会话进行了超过5个回合的交互
- 活跃会话的回合数范围：6-22次

---

## 二、会话事件分析

### 2.1 事件类型统计

**数据来源：** `session_events` 表 (共计事件类型20种)

| 事件类型 | 次数 | 说明 |
|---------|------|------|
| session_start | 3,799 | 会话启动 |
| session_end | 3,741 | 会话结束 |
| session_compact_skipped | 3,569 | 上下文压缩跳过 |
| assistant_message | 2,815 | 助手消息 |
| session_interrupted | 490 | **会话中断** |
| mailbox_received | 350 | 收到消息 |
| subagent.completed | 271 | **子Agent完成** |
| session_compact_started | 234 | 开始压缩 |
| session_compact_completed | 225 | 压缩完成 |
| approval_requested | 104 | 请求批准 |
| approval_resolved | 100 | 批准已处理 |

### 2.2 关键比率

- **会话完成率：** 98.5% (3,741 / 3,799)
- **会话中断率：** 12.9% (490 / 3,799)
- **需要人工批准：** 2.7% (104 / 3,799)
- **上下文压缩执行率：** 63.1% (225 / (234+3,569))

---

## 三、子Agent工作完成率分析 ⚠️

### 3.1 表面统计

**数据来源：** `agent_control_agents` 表（不含root会话）

| 状态 | 数量 | 占比 |
|------|------|------|
| 总子Agent数 | 322 | 100% |
| closed（已关闭） | 308 | **95.7%** |
| stale（失效） | 7 | 2.2% |
| active（活跃） | 7 | 2.2% |

**表面结论：** 95.7%的子Agent成功完成工作

### 3.2 深度分析：真实成功率

**数据来源：** `session_events` 表中的 `subagent.completed` 事件（样本：271个事件）

通过分析271个 `subagent.completed` 事件的payload中的status字段：

| 完成状态 | 数量 | 占比 |
|---------|------|------|
| **failed（失败）** | 159 | **59%** |
| **idle（成功）** | 100 | **37%** |
| stopped（提前终止） | 12 | 4% |

### 3.3 关键发现 🔴

**状态语义混乱：**
- `subagent.completed` 事件表示"子Agent已处理完毕"，但其中59%的payload.status为`failed`
- 这些失败的子Agent仍然被父会话关闭并在 `agent_control_agents` 表中标记为 `closed`
- **95.7%的"完成率"是误导性指标，真实成功率仅37%**

**影响：**
- 父会话无法准确判断子Agent是否真正完成了任务
- 失败的子Agent被当作成功关闭，导致任务链断裂
- 缺少失败原因的详细记录和分析

---

## 四、错误率分析

### 4.1 会话级错误

**已识别的错误指标：**

| 错误类型 | 发生率 | 说明 |
|---------|--------|------|
| 会话中断 | 12.9% | 490次中断 / 3,799次启动 |
| 需要人工批准 | 2.7% | 可能因权限或安全问题 |
| 子Agent失败 | 59% | 见上节详细分析 |

**缺失的错误指标：**
- `analytics_sessions.rollup_json` 中的 `error_count` 字段为空
- 无法统计工具调用失败次数
- 无法追踪特定错误类型的分布

### 4.2 工具调用分析 ❌

**严重问题：工具调用数据缺失**

- `session_tool_receipts` 表：**0条记录**
- `analytics_sessions.rollup_json.tool_call_count`：**全部为空**
- `session_events` 表中 `tool_name` 非空记录：**仅1条（shell工具）**

**结论：** 当前系统未正确记录工具调用详情，无法分析：
- 哪些工具最常用
- 工具成功/失败率
- 工具执行时长
- 工具错误模式

---

## 五、数据质量评估

### 5.1 数据完整性检查

| 数据字段 | 状态 | 影响 |
|---------|------|------|
| `session_runtime_state.*` | ✅ 完整 | 会话状态可追踪 |
| `session_events.*` | ✅ 完整 | 事件流可分析 |
| `analytics_sessions.turn_count` | ⚠️ 部分空值 | 部分会话统计缺失 |
| `analytics_sessions.tool_call_count` | ❌ 全部空值 | **无法分析工具使用** |
| `analytics_sessions.error_count` | ❌ 全部空值 | **无法统计错误** |
| `analytics_sessions.success_rate` | ❌ 全部空值 | **无法评估成功率** |
| `session_tool_receipts.*` | ❌ 表为空 | **工具调用记录缺失** |
| `analytics_turns.fact_json` | ❌ 无有效数据 | **回合级分析不可用** |

### 5.2 数据库表结构

**已实现的表：**
1. `session_runtime_state` - 会话运行时状态 ✅
2. `session_events` - 会话事件流 ✅
3. `session_tool_receipts` - 工具调用回执（**空表** ❌）
4. `agent_control_agents` - 子Agent控制 ✅
5. `analytics_sessions` - 会话分析汇总（**关键字段空值** ⚠️）
6. `analytics_turns` - 回合分析（**无有效数据** ❌）
7. `background.sqlite` - 后台任务
8. `team_store.sqlite` - 团队协作
9. `subagent_batches.sqlite` - 子Agent批次

---

## 六、本地Agent工具功能完善度评估

### 6.1 当前功能状态

| 功能模块 | 实现状态 | 评分 |
|---------|---------|------|
| 会话生命周期管理 | ✅ 完整 | A |
| 子Agent创建与控制 | ✅ 基本完整 | B |
| 事件流记录 | ✅ 完整 | A |
| 工具调用记录 | ❌ **缺失** | F |
| 错误追踪与分析 | ❌ **严重不足** | D |
| 性能监控 | ⚠️ 部分实现 | C |
| 数据分析工具 | ❌ **缺失** | F |
| 成功率评估 | ❌ **不准确** | D |

**总体评分：C-**

### 6.2 严重缺陷清单

#### 1. 数据收集不完整 🔴

**问题：**
- `tool_call_count`、`error_count`、`success_rate` 等关键指标未计算
- 工具调用详情未记录到 `session_tool_receipts`
- 回合级事实数据 (`analytics_turns.fact_json`) 为空

**影响：**
- 无法评估系统实际运行质量
- 无法识别高频错误模式
- 无法优化工具使用策略

**修复优先级：** P0（最高）

#### 2. 状态语义混乱 🔴

**问题：**
- `subagent.completed` 事件中包含 `status=failed` 的记录
- 缺少明确的"任务成功" vs "任务失败"区分
- `session_end` 缺少结束原因分类

**影响：**
- 95.7%的"完成率"具有误导性
- 父会话无法准确判断子Agent工作质量
- 失败链追踪困难

**修复优先级：** P0（最高）

#### 3. 分析工具缺失 🟡

**问题：**
- 需要手写SQL才能获取基础统计信息
- 没有CLI命令查看会话历史和统计（如 `aicli stats`）
- 缺少错误模式识别和聚类分析
- 没有性能异常告警机制

**影响：**
- 开发和运维效率低
- 问题发现滞后
- 用户无法自助诊断

**修复优先级：** P1（高）

#### 4. 子Agent失败率过高 🔴

**问题：**
- 59%的子Agent以失败状态完成
- 缺少失败原因的详细分类
- 没有自动重试机制
- 任务分解策略可能不当

**影响：**
- 多Agent协作不可靠
- 用户体验差
- 系统实用性受限

**修复优先级：** P0（最高）

#### 5. 监控指标缺失 🟡

**问题：**
- 缺少工具执行时长统计
- 缺少内存/CPU使用率监控
- 缺少并发会话数监控
- 缺少token使用量统计

**影响：**
- 性能瓶颈难以定位
- 资源优化缺乏依据
- 成本控制困难

**修复优先级：** P1（高）

---

## 七、改进建议

### 7.1 立即修复（P0优先级）

#### 1. 修复数据收集缺失

**目标：** 确保所有关键统计字段正确计算并持久化

**实施步骤：**

```typescript
// 在 analytics_sessions.rollup_json 中正确计算：
{
  "turn_count": 10,
  "tool_call_count": 25,      // ← 需要实现
  "error_count": 2,            // ← 需要实现
  "success_rate": 0.92,        // ← 需要实现
  "avg_turn_duration_ms": 1500,
  "total_tokens": 15000
}

// 确保 session_tool_receipts 表正确记录每次工具调用
INSERT INTO session_tool_receipts (
  session_id, tool_name, call_id, 
  status, duration_ms, error_message,
  input_size, output_size
) VALUES (...);
```

**验证方法：**
```sql
-- 检查字段是否不再为空
SELECT COUNT(*) FROM analytics_sessions 
WHERE json_extract(rollup_json, '$.tool_call_count') IS NOT NULL;

-- 检查工具调用记录
SELECT COUNT(*) FROM session_tool_receipts;
```

#### 2. 明确区分成功与失败

**目标：** 让状态语义清晰明确

**实施步骤：**

1. 修改事件定义：
   - 保留 `subagent.completed` 用于"子Agent已处理"
   - 添加 `subagent.succeeded` 表示真正成功
   - 添加 `subagent.failed` 表示明确失败

2. 修改 `agent_control_agents.status` 枚举：
   ```typescript
   type AgentStatus = 
     | 'active'
     | 'idle'
     | 'completed_success'  // ← 新增
     | 'completed_failure'  // ← 新增
     | 'cancelled'
     | 'stale';
   ```

3. 为 `session_end` 添加 `completion_reason` 字段：
   ```typescript
   type CompletionReason = 
     | 'success'
     | 'error'
     | 'interrupted'
     | 'timeout'
     | 'user_cancelled';
   ```

#### 3. 降低子Agent失败率

**目标：** 将失败率从59%降至20%以下

**分析失败原因：**
```sql
-- 需要在代码中添加失败原因记录
SELECT 
  json_extract(payload_json, '$.failure_reason') as reason,
  COUNT(*) as count
FROM session_events
WHERE type = 'subagent.completed' 
  AND json_extract(payload_json, '$.status') = 'failed'
GROUP BY reason
ORDER BY count DESC;
```

**可能的改进策略：**
- 改进任务分解逻辑（避免任务过大或依赖不清）
- 添加子Agent超时检测和自动重试（最多3次）
- 提供更清晰的上下文和约束条件
- 实现子Agent健康检查和早期失败检测

### 7.2 短期优化（P1优先级）

#### 1. 实现CLI分析命令

```bash
# 查看会话统计
aicli stats

# 查看最近10个会话
aicli sessions --recent 10

# 查看特定会话详情
aicli session <session_id>

# 查看子Agent成功率
aicli agents --success-rate

# 查看工具使用统计
aicli tools --stats

# 查看错误分布
aicli errors --top 20
```

#### 2. 添加错误模式识别

**目标：** 自动识别和聚类常见错误

```typescript
interface ErrorPattern {
  pattern_id: string;
  error_type: string;
  frequency: number;
  affected_tools: string[];
  first_seen: Date;
  last_seen: Date;
  sample_sessions: string[];
}

// 实现错误聚类算法
function detectErrorPatterns(
  sessions: Session[]
): ErrorPattern[] {
  // 基于错误消息相似度聚类
  // 识别重复出现的错误模式
}
```

#### 3. 实现性能监控

```typescript
interface PerformanceMetrics {
  session_id: string;
  avg_turn_latency_ms: number;
  p95_turn_latency_ms: number;
  total_tokens: number;
  tool_call_count: number;
  cache_hit_rate: number;
  memory_peak_mb: number;
}
```

### 7.3 中期增强（P2优先级）

#### 1. 实现子Agent质量评分

```typescript
interface SubagentQualityScore {
  agent_id: string;
  task_completion_rate: number;     // 0-1
  output_quality_score: number;     // 0-1
  efficiency_score: number;         // 0-1
  error_recovery_rate: number;      // 0-1
  overall_score: number;            // 0-1
}
```

#### 2. 添加可视化Dashboard

- 实时会话监控面板
- 错误趋势图表
- 子Agent成功率热力图
- 工具使用分布饼图
- 性能时间序列图

#### 3. 实现告警机制

```typescript
interface Alert {
  level: 'info' | 'warning' | 'critical';
  type: 'error_spike' | 'performance_degradation' | 'high_failure_rate';
  message: string;
  affected_sessions: string[];
  recommendation: string;
}
```

---

## 八、代码核实清单

为了验证本报告的分析结论，需要检查以下代码位置：

### 8.1 数据收集逻辑

**需要检查的文件：**
- `src/analytics/session-analytics.ts` - 会话统计计算逻辑
- `src/analytics/rollup-builder.ts` - rollup_json构建逻辑
- `src/tools/tool-executor.ts` - 工具调用记录逻辑

**验证点：**
```typescript
// 是否正确计算 tool_call_count?
rollup.tool_call_count = /* ... */;

// 是否正确计算 error_count?
rollup.error_count = /* ... */;

// 是否正确写入 session_tool_receipts?
await db.insert('session_tool_receipts', {
  tool_name: toolName,
  status: result.status,
  // ...
});
```

### 8.2 子Agent状态管理

**需要检查的文件：**
- `src/agents/subagent-coordinator.ts` - 子Agent协调器
- `src/agents/agent-lifecycle.ts` - Agent生命周期管理
- `src/events/subagent-events.ts` - 子Agent事件定义

**验证点：**
```typescript
// subagent.completed 事件的触发条件
function emitSubagentCompleted(agent: Agent) {
  // 为什么 status=failed 也触发 completed 事件？
  if (agent.status === 'failed') {
    // 应该触发 subagent.failed 事件？
  }
}

// agent_control_agents 的 status 更新逻辑
function updateAgentStatus(agentId: string, status: string) {
  // 是否区分 completed_success 和 completed_failure?
}
```

### 8.3 错误处理和记录

**需要检查的文件：**
- `src/error/error-handler.ts` - 错误处理器
- `src/analytics/error-tracker.ts` - 错误追踪器（如果存在）

**验证点：**
```typescript
// 错误是否被正确记录到 analytics_sessions.error_count?
function handleError(error: Error, context: Context) {
  // 是否更新了 error_count?
  // 是否记录了错误详情?
}
```

---

## 九、结论与建议优先级

### 9.1 系统健康度评分

| 维度 | 评分 | 说明 |
|------|------|------|
| 基础功能 | B+ | 会话管理和事件记录基本完善 |
| 可靠性 | D | 子Agent失败率59%不可接受 |
| 可观测性 | D | 关键统计数据缺失 |
| 可维护性 | C | 缺少内置分析工具 |
| 数据质量 | D | 多个关键字段为空值 |
| **总体评分** | **C-** | **需要立即改进** |

### 9.2 行动计划优先级

**第一阶段（P0 - 立即修复）：**
1. ✅ 修复 `tool_call_count`、`error_count`、`success_rate` 计算逻辑
2. ✅ 实现 `session_tool_receipts` 正确记录
3. ✅ 明确区分子Agent成功/失败状态
4. ✅ 分析并降低子Agent失败率（目标：<20%）

**第二阶段（P1 - 1-2周内）：**
1. 实现 `aicli stats` 等CLI分析命令
2. 添加错误模式识别和聚类
3. 实现基础性能监控和告警

**第三阶段（P2 - 1个月内）：**
1. 添加子Agent质量评分机制
2. 实现可视化Dashboard
3. 添加自动化测试和回归检测

### 9.3 预期改进效果

**修复后的预期指标：**
- 子Agent成功率：37% → **80%+**
- 数据完整性：~40% → **95%+**
- 错误可追踪性：弱 → **强**
- 问题发现时间：数天 → **实时**
- 用户满意度：低 → **中高**

---

## 附录

### A. 数据库Schema摘要

```sql
-- session_runtime_state
CREATE TABLE session_runtime_state (
  session_id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  current_turn_id TEXT,
  updated_at TEXT NOT NULL
);

-- session_events
CREATE TABLE session_events (
  id INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  tool_name TEXT,
  payload_json BLOB NOT NULL,
  created_at TEXT NOT NULL
);

-- agent_control_agents
CREATE TABLE agent_control_agents (
  agent_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL
);

-- analytics_sessions
CREATE TABLE analytics_sessions (
  session_id TEXT PRIMARY KEY,
  rollup_json BLOB NOT NULL,
  updated_at TEXT NOT NULL
);
```

### B. SQL查询示例

```sql
-- 查看最近活跃会话
SELECT session_id, status, updated_at 
FROM session_runtime_state 
ORDER BY updated_at DESC 
LIMIT 20;

-- 查看子Agent成功率
SELECT 
  COUNT(*) as total,
  SUM(CASE WHEN json_extract(payload_json, '$.status') = 'idle' THEN 1 ELSE 0 END) as success,
  SUM(CASE WHEN json_extract(payload_json, '$.status') = 'failed' THEN 1 ELSE 0 END) as failed
FROM session_events
WHERE type = 'subagent.completed';

-- 查看事件类型分布
SELECT type, COUNT(*) as count 
FROM session_events 
GROUP BY type 
ORDER BY count DESC;
```

### C. 参考资料

- SQLite数据库位置：`%USERPROFILE%\.aicli\sessions\runtime\`
- 主要数据库文件：
  - `session_runtime.sqlite` - 会话运行时
  - `agent_control.sqlite` - Agent控制
  - `usage_analytics.sqlite` - 使用分析
  - `background.sqlite` - 后台任务

---

**报告生成：** 2026-09-17  
**分析工具：** SQLite3 + 自定义SQL查询  
**数据时间范围：** 2026-07-28 至 2026-09-17
