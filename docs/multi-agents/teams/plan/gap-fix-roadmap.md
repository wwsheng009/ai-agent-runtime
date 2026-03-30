# Team 系统缺口修复路线图

**制定日期**: 2026-03-15
**基于**: comprehensive-implementation-review.md
**当前完成度**: 85%
**目标完成度**: 100%

---

## 优先级分级

### P0 - 关键缺口（立即修复）
无 - 所有核心功能已实现

### P1 - 高优先级（1-2周内完成）
1. Background Task 工具化
2. Context Compaction 机制

### P2 - 中优先级（1-2月内完成）
3. Hook Engine 完整支持
4. Checkpoint/Rewind 对话模式

### P3 - 低优先级（3-6月内完成）
5. Context Manager L4/L5 层
6. Subagents 自动路由

---

## Phase 1: Background Task 工具化（1-2周）

### 目标
让 agent 能够通过工具创建和查询后台任务

### 任务清单

#### 1.1 BackgroundTask 工具实现
- [ ] 定义工具接口 `internal/runtime/toolbroker/background_task.go`
- [ ] 实现 `BackgroundTask` 工具
  - 参数: `command`, `timeout`, `working_dir`
  - 返回: `task_id`, `status`
- [ ] 集成到 ToolBroker
- [ ] 添加权限控制（默认需要审批）

**预计工作量**: 2-3天

#### 1.2 TaskOutput 工具实现
- [ ] 定义工具接口 `internal/runtime/toolbroker/task_output.go`
- [ ] 实现 `TaskOutput` 工具
  - 参数: `task_id`, `offset`, `limit`, `follow`
  - 返回: `output`, `status`, `exit_code`
- [ ] 支持流式输出（follow 模式）
- [ ] 集成到 ToolBroker

**预计工作量**: 2-3天

#### 1.3 后台任务持久化
- [ ] 设计数据表 `background_tasks`
- [ ] 实现输出持久化（ring buffer + append-only log）
- [ ] 实现任务状态管理（running, completed, failed, timeout）
- [ ] 添加任务清理机制（会话结束后保留 24 小时）

**预计工作量**: 3-4天

#### 1.4 测试和文档
- [ ] 单元测试（覆盖率 > 80%）
- [ ] 集成测试（端到端场景）
- [ ] 工具使用文档
- [ ] 示例代码

**预计工作量**: 2天

**Phase 1 总工作量**: 9-12天

---

## Phase 2: Context Compaction（2-3周）

### 目标
实现智能上下文压缩，避免长会话失焦

### 任务清单

#### 2.1 Compaction 基础设施
- [ ] 定义 Compaction 接口 `internal/runtime/context/compaction.go`
- [ ] 实现 CompactionManager
- [ ] 设计触发策略（token 阈值、turn 数量）
- [ ] 集成到 Agent Loop

**预计工作量**: 3-4天

#### 2.2 四种摘要类型实现
- [ ] DecisionSummary - 决策摘要
  - 提取��键决策点
  - 记录决策理由
- [ ] OpenQuestions - 未解决问题
  - 识别待解决问题
  - 跟踪问题状态
- [ ] WorkCompleted - 已完成工作
  - 总结完成的任务
  - 记录产出物
- [ ] ArtifactIndex - 产物索引
  - 索引创建的文件
  - 记录修改历史

**预计工作量**: 5-6天

#### 2.3 摘要生成策略
- [ ] 实现基于 LLM 的摘要生成
- [ ] 实现基于��则的摘要生成（备选）
- [ ] 优化 prompt 设计
- [ ] 添加摘要质量评估

**预计工作量**: 3-4天

#### 2.4 测试和优化
- [ ] 单元测试
- [ ] 长会话测试（100+ turns）
- [ ] 性能优化（减少 API 调用）
- [ ] 文档和示例

**预计工作量**: 3-4天

**Phase 2 总工作量**: 14-18天

---

## Phase 3: Hook Engine 完整支持（3-4周）

### 目标
实现完整的生命周期 hook 支持

### 任务清单

#### 3.1 Hook 基础设施
- [ ] 定义 Hook 接口 `internal/runtime/hooks/hook.go`
- [ ] 实现 HookManager
- [ ] 支持 Shell hook
- [ ] 支持 HTTP hook
- [ ] 实现 hook 配置加载

**预计工作量**: 4-5天

#### 3.2 生命周期事件支持
- [ ] PreToolUse - 工具调用前
- [ ] PostToolUse - 工具调用后
- [ ] SessionStart - 会话开始
- [ ] UserPromptSubmit - 用户提交 prompt
- [ ] PermissionRequest - 权限请求
- [ ] SubagentStart/Stop - 子代理启动/停止

**预计工作量**: 6-8天

#### 3.3 Hook 决策支持
- [ ] continue - 继续执行
- [ ] block - 阻止执行
- [ ] modify - 修改输入
- [ ] notify - 发送通知
- [ ] attach_context - 附加上下文

**预计工作量**: 3-4天

#### 3.4 集成和测试
- [ ] 集成到 Permission Engine
- [ ] 集成到 Agent Loop
- [ ] 单元测试
- [ ] 集成测试
- [ ] 文档和示例

**预计工作量**: 4-5天

**Phase 3 总工作量**: 17-22天

---

## Phase 4: Checkpoint/Rewind 对话模式（3-4周）

### 目标
支持对话历史的回滚和恢复

### 任务清单

#### 4.1 对话状态快照
- [ ] 设计对话状态结构
- [ ] 实现对话状态序列化
- [ ] 实现对话状态持久化
- [ ] 支持增量快照（减少存储）

**预计工作量**: 4-5天

#### 4.2 对话恢复机制
- [ ] 实现对话历史回滚
- [ ] 实现消息删除和重建
- [ ] 处理工具调用结果的回滚
- [ ] 处理副作用的清理

**预计工作量**: 5-6天

#### 4.3 三种恢复模式
- [ ] code - 仅恢复代码
- [ ] conversation - 仅恢复对话
- [ ] both - 同时恢复代码和对话

**预计工作量**: 3-4天

#### 4.4 UI 和测试
- [ ] 实现 Rewind 命令
- [ ] 添加 checkpoint 列表查看
- [ ] 单元测试
- [ ] 集成测试
- [ ] 文档和示例

**预计工作量**: 4-5天

**Phase 4 总工作量**: 16-20天

---

## Phase 5: Context Manager L4/L5（长期）

### 目标
实现可检索知识层和团队共享层

### 任务清单（概要）

#### 5.1 L4 - 可检索知识层
- [ ] 文件索引系统
- [ ] 代码符号索引
- [ ] 向量检索（可选）
- [ ] 智能检索策略

**预计工作量**: 3-4周

#### 5.2 L5 - 团队共享层
- [ ] Task graph 概要生成
- [ ] Teammate 摘要生成
- [ ] Mailbox 关键结论提取
- [ ] 团队知识库构建

**预计工作量**: 2-3周

**Phase 5 总工作量**: 5-7周

---

## Phase 6: Subagents 自动路由（长期）

### 目标
实现智能的 subagent 选择和路由

### 任务清单（概要）

#### 6.1 Profile 外置
- [ ] 设计 AgentProfile 配置格式
- [ ] 实现 profile 加载器
- [ ] 内置 3 个 profile（explore, planner, executor）
- [ ] 支持自定义 profile

**预计工作量**: 2-3周

#### 6.2 自动路由
- [ ] 实现 task classifier
- [ ] 设计路由策略
- [ ] 实现路由决策引擎
- [ ] 添加路由监控和调优

**预计工作量**: 3-4周

**Phase 6 总工作量**: 5-7周

---

## 时间线总览

```
Week 1-2:   Phase 1 - Background Task 工具化
Week 3-5:   Phase 2 - Context Compaction
Week 6-9:   Phase 3 - Hook Engine
Week 10-13: Phase 4 - Checkpoint/Rewind 对话模式
Month 4-5:  Phase 5 - Context Manager L4/L5
Month 5-6:  Phase 6 - Subagents 自动路由
```

---

## 里程碑

### M1: 核心工具完善（Week 2）
- ✅ Background Task 工具化完成
- 目标: agent 可以创建和查询后台任务

### M2: 长会话支持（Week 5）
- ✅ Context Compaction 完成
- 目标: 支持 100+ turns 的长会话不失焦

### M3: 自动化能力（Week 9）
- ✅ Hook Engine 完成
- 目标: 支持自动化测试和格式化

### M4: 时间旅行（Week 13）
- ✅ Checkpoint/Rewind 对话模式完成
- 目标: 支持对话历史的回滚和恢复

### M5: 智能上下文（Month 5）
- ✅ Context Manager L4/L5 完成
- 目标: 智能检索和团队知识共享

### M6: 智能路由（Month 6）
- ✅ Subagents 自动路由完成
- 目标: 自动选择最合适的 subagent

---

## 资源需求

### 人力
- 1 名全职开发者（Phase 1-4）
- 0.5 名开发者（Phase 5-6，可并行其他工作）

### 时间
- 短期（3 个月）: Phase 1-4
- 长期（6 个月）: Phase 1-6

### 风险
- Context Compaction 的摘要质量依赖 LLM 能力
- Hook Engine 的性能影响需要优化
- Checkpoint/Rewind 的副作用清理可能复杂

---

## 验收标准

### Phase 1
- [ ] agent 可以通过工具创建后台任务
- [ ] agent 可以查询后台任务输出
- [ ] 输出持久化到文件系统
- [ ] 测试覆盖率 > 80%

### Phase 2
- [ ] 长会话（100+ turns）不会失焦
- [ ] context 大小保持在合理范围
- [ ] 摘要质量高（人工评估）
- [ ] 测试覆盖率 > 80%

### Phase 3
- [ ] 支持 PreToolUse 和 PostToolUse 事件
- [ ] 支持 Shell hook 和 HTTP hook
- [ ] 支持 continue/block/modify 决策
- [ ] 测试覆盖率 > 80%

### Phase 4
- [ ] 支持 code/conversation/both 三种恢复模式
- [ ] 对话历史可以正确回滚
- [ ] 副作用可以正确清理
- [ ] 测试覆盖率 > 80%

---

## 下一步行动

1. **立即开始**: Phase 1 - Background Task 工具化
2. **准备工作**: 设计 Context Compaction 的摘要策略
3. **技术调研**: 调研 Hook Engine 的最佳实践
4. **原型验证**: 验证 Checkpoint/Rewind 的可行性

---

**更新日期**: 2026-03-15
**下次审查**: 每个 Phase 完成后
