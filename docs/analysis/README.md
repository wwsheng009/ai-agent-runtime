# AI Agent Runtime 会话质量分析

**分析日期：** 2026-09-17  
**分析师：** Kiro AI  
**数据源：** `.aicli\sessions\runtime\*.sqlite`

---

## 📋 报告清单

1. **[session_analysis_report.md](./session_analysis_report.md)** - 会话质量深度分析
2. **[code_verification.md](./code_verification.md)** - 代码层面验证与修复方案

---

## 🚨 关键发现（TL;DR）

### 最严重的问题

| 问题 | 当前状态 | 目标状态 | 影响 |
|------|----------|----------|------|
| **子Agent失败率** | **59%** ⚠️ | <20% | 大量任务执行失败 |
| **统计字段完整性** | **0%** ⚠️ | 100% | 无法分析问题根因 |
| **真实任务完成率** | **~25%** ⚠️ | >80% | 用户体验差 |
| **可观测性** | **极弱** ⚠️ | 强 | 问题发现滞后 |

### 数据概览

```
总会话数：861
├─ 已停止：652（75.8%）
├─ 空闲：194（22.5%）
└─ 运行中：14（1.6%）

平均回合数：0.46次/会话 ← 大量僵尸会话
活跃会话（>5回合）：<5%

子Agent统计：
├─ 总数：322
├─ 失败：160（59%）⚠️
├─ 成功：111（41%）
└─ 超时：51（16%）
```

---

## 🎯 立即行动项（P0）

### 1. 降低子Agent失败率（59% → <20%）

**问题根源：**
- 无重试机制
- 无错误恢复
- 任务分解不合理

**修复文件：** `backend/internal/agent/scheduler.go`

**工作量：** 1-2天

**代码示例：** 见 [code_verification.md § 1.2](./code_verification.md#12-重试机制验证)

---

### 2. 修复事件语义混乱

**问题：** `subagent.completed` 同时表示成功和失败

**影响：**
- 父Agent误判子任务状态
- 统计数据不准确
- 调试困难

**修复方案：**

**选项A：** 拆分事件类型
```go
// 当前
"subagent.completed" + payload.success=true/false

// 改为
"subagent.completed.success"
"subagent.completed.failure"
```

**选项B：** 标准化 completion_reason
```json
{
  "type": "subagent.completed",
  "payload": {
    "completion_reason": "success|failure|timeout|cancelled"
  }
}
```

**推荐：** 选项A（语义更清晰）

**影响文件：**
- `backend/internal/agent/scheduler.go:454, 507`
- `backend/internal/events/contract.go:105`
- `backend/internal/api/skills/handler.go:10722`

**工作量：** 4-8小时

---

### 3. 补齐统计字段

**当前状态：** 所有关键字段为空

```sql
-- 当前结果：全部为 NULL
SELECT 
  json_extract(rollup_json, '$.tool_call_count'),
  json_extract(rollup_json, '$.error_count'),
  json_extract(rollup_json, '$.success_rate')
FROM usage_sessions;
```

**缺失事件订阅：**
- ✗ `subagent.started`
- ✗ `subagent.completed`
- ✗ `tool.call.started`
- ✗ `tool.call.finished`
- ✗ `tool.error`

**修复代码：** 见 [code_verification.md § Patch 1-2](./code_verification.md#快速修复patch)

**影响文件：**
- `backend/internal/usageanalytics/ingest.go:95-106` (订阅事件)
- `backend/internal/usageanalytics/ingest.go:124-135` (事件处理)

**工作量：** 1-2天

---

## 📊 详细分析

### 子Agent失败模式分析

基于 `session_events` 表中的 271 条 `subagent.completed` 事件：

```sql
SELECT 
  json_extract(payload_json, '$.error') as error_type,
  COUNT(*) as count
FROM session_events
WHERE type = 'subagent.completed' 
  AND json_extract(payload_json, '$.success') = 'false'
GROUP BY error_type
ORDER BY count DESC
LIMIT 10;
```

**典型失败原因（推断）：**
1. 超时（timeout）
2. 资源不足（resource exhausted）
3. 权限错误（permission denied）
4. 逻辑错误（logic error）

**改进策略：**
- **超时** → 增加自动重试（最多3次）
- **资源不足** → 实现请求排队机制
- **权限错误** → 启动前预检查
- **逻辑错误** → 改进任务分解和验证

---

## 🛠️ 修复优先级矩阵

| 任务 | 严重性 | 复杂度 | 优先级 | 工作量 | 开始日期 |
|------|--------|--------|--------|--------|----------|
| 补充事件订阅 | 高 | 低 | P0 | 2-4h | 立即 |
| 修复事件语义 | 高 | 中 | P0 | 4-8h | Day 1 |
| 实现重试机制 | 高 | 中 | P0 | 1-2d | Day 2 |
| 持久化统计字段 | 高 | 中 | P0 | 1-2d | Day 3 |
| 工具调用审计 | 中 | 中 | P1 | 2-3d | Week 2 |
| CLI分析工具 | 中 | 低 | P1 | 2-3d | Week 2 |
| 实时监控Dashboard | 中 | 高 | P2 | 1-2w | Week 3 |
| 性能优化 | 低 | 中 | P2 | 3-5d | Week 5 |

**总工作量估计：** 4-6周（2名开发者）

---

## 🧪 验证计划

### 阶段1：单元测试（P0完成后）

```bash
# 运行单元测试
go test ./backend/internal/usageanalytics/... -v
go test ./backend/internal/agent/... -v

# 预期通过：
# - TestSubagentCompletedEvent
# - TestSubagentRetryMechanism
# - TestEventSubscription
```

### 阶段2：集成测试

```bash
# 1. 清空测试数据库
rm ~/.aicli/sessions/runtime/usage_analytics.sqlite

# 2. 启动测试会话
aicli session start --test-mode

# 3. 触发子Agent执行（含失败场景）
aicli run "请启动5个子Agent并行执行任务，设置2个会超时失败"

# 4. 验证统计数据
sqlite3 ~/.aicli/sessions/runtime/usage_analytics.sqlite <<EOF
SELECT 
  session_id,
  json_extract(meta_json, '$.subagent_count') as total,
  json_extract(meta_json, '$.subagent_success_count') as success,
  json_extract(meta_json, '$.subagent_failure_count') as failure,
  json_extract(meta_json, '$.tool_call_count') as tools
FROM usage_sessions
WHERE session_id = '<your-session-id>';
EOF

# 预期输出：
# total=5, success=3, failure=2, tools>0
```

### 阶段3：压力测试

```bash
# 模拟高并发场景
for i in {1..50}; do
  aicli run "执行测试任务 $i" &
done
wait

# 验证：
# - 所有事件正确记录
# - 无数据丢失
# - 统计字段准确
```

---

## 📈 成功指标

### 短期目标（2周内）

- [x] 完成问题分析和代码验证
- [ ] 子Agent失败率降至 <35%
- [ ] 统计字段完整性达到 80%
- [ ] CLI分析工具可用

### 中期目标（1月内）

- [ ] 子Agent失败率降至 <20%
- [ ] 统计字段完整性达到 100%
- [ ] 实时监控Dashboard上线
- [ ] 错误模式自动识别

### 长期目标（3月内）

- [ ] 子Agent成功率稳定在 >85%
- [ ] 真实任务完成率 >80%
- [ ] 平均会话回合数 >3
- [ ] 用户满意度提升 50%

---

## 🔗 相关资源

### 文档
- [详细分析报告](./session_analysis_report.md) - 数据驱动的问题发现
- [代码验证报告](./code_verification.md) - 根因定位与修复方案

### 代码位置
- **子Agent调度：** `backend/internal/agent/scheduler.go`
- **事件订阅：** `backend/internal/usageanalytics/ingest.go`
- **统计查询：** `backend/internal/usageanalytics/query.go`
- **事件定义：** `backend/internal/events/contract.go`

### SQL查询示例

```sql
-- 查看最近10个失败的子Agent
SELECT 
  session_id,
  json_extract(payload_json, '$.subagent_id') as agent_id,
  json_extract(payload_json, '$.error') as error,
  datetime(timestamp_unix_nano/1000000000, 'unixepoch') as failed_at
FROM session_events
WHERE type = 'subagent.completed'
  AND json_extract(payload_json, '$.success') = 'false'
ORDER BY timestamp_unix_nano DESC
LIMIT 10;

-- 统计各类错误的频率
SELECT 
  json_extract(payload_json, '$.error') as error_message,
  COUNT(*) as occurrences
FROM session_events
WHERE type = 'subagent.completed'
  AND json_extract(payload_json, '$.success') = 'false'
GROUP BY error_message
ORDER BY occurrences DESC;
```

---

## 👥 负责人分配

| 模块 | 负责人 | 状态 |
|------|--------|------|
| 分析报告 | ✓ 已完成 | Done |
| P0修复 | 待分配 | Not Started |
| P1功能 | 待分配 | Not Started |
| 测试验证 | 待分配 | Not Started |
| 文档更新 | 待分配 | Not Started |

---

## 📞 联系方式

如有问题或需要澄清，请：
1. 查阅详细报告（见上方链接）
2. 检查代码注释和修复示例
3. 提交GitHub Issue
4. 联系项目维护者

---

**最后更新：** 2026-09-17  
**版本：** v1.0  
**状态：** 分析完成，待实施修复
