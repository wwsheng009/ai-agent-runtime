# SSE Live 渲染闸门设计问题深度分析

> **补遗（2026-09-18 完整性审查）**：本文件提出的「方案 A：放宽渲染闸门（按 SSE 活动开闸）」已被**否决**——`/runtime/stream` 的初始 dump / 断点补齐同样产生帧与字节，按活动开闸会误渲染历史回放；且只开闸不引入回合身份仍无写入目标（找不到 streaming 助手消息、也不满足补建占位的双侧身份条件）。修正后的完整设计（帧分类 + 活跃证据 + 认领/补放 + 自愈有界 + 诊断口径）见 `../plan/frontend-sse-render-gate-recovery-plan-20260918.md` v2 §2/§3；本文件保留设计层分析与方案对比记录。

## 问题本质

**核心矛盾：SSE 有帧活动，但页面不更新，因为渲染闸门关闭。**

从诊断数据：
- 渲染闸门：**关**
- 被拦增量：**100**
- 未认领回合：**5**
- 快照刷新：**5**
- SSE 最近帧：`tool_finished`, `tool_started`（帧活动正常）

## 当前设计的逻辑链

### 1. 渲染闸门决策（`use-workspace-live.ts:143-144`）

```typescript
const renderLiveDeltas = 
  (localResponding || resumedTurnActive) && Boolean(liveTurnId);
```

**闸门打开条件（AND 关系）：**
1. 本地回合在跑 **或** 续传回合活跃
2. **有在途回合身份** (`liveTurnId` 非空)

### 2. 回合身份来源

```typescript
const liveTurnId = localTurnId ?? resumedTurnId;
```

- `localTurnId`：本地 `/api/agent/chat` POST 请求拥有的回合
- `resumedTurnId`：从 `/api/runtime/sessions/{id}/runtime` 快照的 `active_turn` 认领的回合

### 3. 续传认领条件（`use-resumed-session-turn.ts:104`）

```typescript
const adoptable = Boolean(serverTurnId) && !occupiedByLocalTurn;
```

其中 `serverTurnId` 来自快照的 `active_turn.turn_id`。

### 4. 增量应用逻辑（`use-session-runtime-stream.ts:372-374`）

```typescript
const turnMatches = matchesActiveTurn(activeTurn, eventTurn);
let shouldApplyLiveDelta = false;
if (renderLiveDeltasRef.current && deltaKind && turnMatches) {
  // 只有闸门开 + 回合匹配才应用增量
}
```

### 5. 回合匹配语义（`deltas.ts:339-349`）

```typescript
export function matchesActiveTurn(
  activeTurnId: string | undefined,
  eventTurnId: string | undefined,
): boolean {
  const active = activeTurnId?.trim() ?? "";
  const eventTurn = eventTurnId?.trim() ?? "";
  // 关键：「未知」不等于「其他 turn」
  if (!active || !eventTurn) {
    return true;  // 任一侧无身份都放行
  }
  return active === eventTurn;  // 两侧都有时才严格比对
}
```

---

## 设计缺陷分析

### 缺陷 1：闸门是「单向阀」而非「自适应开关」

**现状：**
- 闸门开启：需要外部状态（本地请求或快照续传）
- 闸门关闭：一旦外部状态丢失，闸门永久关闭
- SSE 帧活动：**无法自动打开闸门**

**问题场景：**

```
1. 本地回合结束 → localTurnId 清空
2. 快照刷新拿到 active_turn = null（回合已结束）
3. 但 SSE 仍在推送该回合的历史事件（游标过旧）
4. liveTurnId = null → 闸门关闭
5. 100 条增量到达 → 全部被拦截
6. 自愈机制触发 5 次 → 快照仍然 active_turn = null
7. 闸门永久关闭，页面不再更新
```

**根本原因：**
渲染闸门依赖「回合身份」，而回合身份只能从外部获取（本地请求或快照）。当外部状态失效但 SSE 仍有活动时，没有机制从 SSE 事件中提取回合身份来重新打开闸门。

---

### 缺陷 2：`matchesActiveTurn` 的宽松语义与闸门的严格判定不一致

**`matchesActiveTurn` 的设计理念：**
> "未知不等于其他 turn" —— 任一侧无身份都放行

这是为了兼容：
- Provider 未暴露身份
- 后端 `loop.go` 只在 `turnID != ""` 时注入 `turn_id`
- 避免真后端的增量在 hook 层被整条丢弃

**但是：**

```typescript
// use-session-runtime-stream.ts:374
if (renderLiveDeltasRef.current && deltaKind && turnMatches) {
  shouldApplyLiveDelta = true;
}
```

即使 `turnMatches = true`（因为双方都无身份时放行），但 `renderLiveDeltasRef.current = false`（闸门关闭），增量仍然被拦截！

**矛盾：**
- `matchesActiveTurn` 说："这条增量可以写入当前消息"（宽松放行）
- 但闸门说："没有回合身份就不允许任何增量渲染"（严格拒绝）

结果：**增量通过了回合匹配，但被闸门拦截**。

---

### 缺陷 3：自愈机制的死循环

**「未认领回合」检测逻辑（`use-workspace-live.ts:220-223`）：**

```typescript
if (!liveTurnId) {  // 本地无回合身份
  if (turnId && deltaKind) {  // 但事件带回合 ID
    handleUnownedTurn(turnId);  // 触发快照刷新
  }
}
```

**自愈流程：**

```
检测到未认领回合 → 刷新快照
                  ↓
快照返回 active_turn = null（回合已结束）
                  ↓
续传认领失败 → liveTurnId 仍为 null
                  ↓
下一帧增量到达 → 再次触发「未认领回合」
                  ↓
再次刷新快照 → 仍然 active_turn = null
                  ↓
死循环（诊断数据：5 次触发，5 次刷新，0 次成功）
```

**根本问题：**
自愈机制假设「事件有 turn_id 但本地无身份 = 快照过期」，但实际情况可能是：
- **游标对齐问题**：回合已结束，但 SSE 仍在重放历史
- **时序错位**：快照已收敛为空，但历史事件仍在队列中

---

## 理想设计：自适应渲染闸门

### 设计原则

1. **SSE 活动优先**
   - 有帧活动 = 有内容需要渲染
   - 闸门应该默认打开，除非明确检测到冲突

2. **回合身份是「对齐锁」而非「准入门槛」**
   - 有身份时：严格对齐，防止串台
   - 无身份时：放行渲染，避免丢失内容

3. **自愈应该是「修复」而非「循环检测」**
   - 检测到问题 → 尝试修复
   - 修复失败 → 降级策略（如无身份渲染）
   - 而不是无限重试同一个失败的操作

---

## 解决方案设计

### 方案 A：放宽渲染闸门（最小改动）

**核心思路：**
既然 `matchesActiveTurn` 允许「未知」放行，闸门也应该允许「无身份但有活动」的情况。

**实现：**

```typescript
// use-workspace-live.ts
const renderLiveDeltas = 
  (localResponding || resumedTurnActive) && Boolean(liveTurnId)
  || hasRecentSSEActivity();  // 新增：SSE 活动自动打开闸门

function hasRecentSSEActivity(): boolean {
  const snapshot = getLiveDiagnosticsSnapshot(sessionId ?? '');
  const now = Date.now();
  // 近 5 秒内有事件到达 = 活动中
  return snapshot.runtime.lastEventAt !== null && 
         (now - snapshot.runtime.lastEventAt) < 5000;
}
```

**优点：**
- 改动最小（只改一行）
- 立即解决「SSE 有活动但页面不更新」的问题
- 保持现有的回合对齐逻辑

**缺点：**
- 可能导致不同回合的事件串台（如果没有回合身份保护）
- 需要严格依赖 `matchesActiveTurn` 的防护

---

### 方案 B：从事件中提取回合身份（推荐）

**核心思路：**
既然事件带 `turn_id`，为什么不直接用它作为临时回合身份？

**实现：**

```typescript
// use-workspace-live.ts:133
const liveTurnId = localTurnId ?? resumedTurnId ?? inferredTurnId;

// 新增：从最近的 SSE 事件推断回合身份
const [inferredTurnId, setInferredTurnId] = useState<string | null>(null);

useEffect(() => {
  // 只在「本地和续传都无身份」时才推断
  if (localTurnId || resumedTurnId) {
    setInferredTurnId(null);
    return;
  }
  
  // 从诊断快照获取最近的事件 turn_id
  const snapshot = getLiveDiagnosticsSnapshot(sessionId ?? '');
  const recentFrames = snapshot.runtime.frames;
  
  for (const frame of recentFrames) {
    if (frame.kind === 'event' && frame.name.startsWith('assistant')) {
      // 从事件 payload 提取 turn_id
      // 需要访问实际的事件对象，诊断快照只有摘要
      // 这里需要在 use-session-runtime-stream 的 onEvent 回调中设置
    }
  }
}, [localTurnId, resumedTurnId, sessionId]);
```

**在 `use-session-runtime-stream.ts` 中：**

```typescript
// 在 onEvent 回调中（第 346 行）
onRuntimeEvent: (event) => {
  const turnId = getRuntimeEventTurnId(event);
  const deltaKind = getRuntimeDeltaKind(event.type);
  
  // 新增：如果本地无回合身份，但事件带 turn_id，推断为当前回合
  if (!liveTurnId && turnId && deltaKind) {
    // 通过回调设置推断的回合身份
    onInferTurnId?.(turnId);
  }
  
  // ... 原有逻辑
}
```

**配合修改闸门逻辑：**

```typescript
const renderLiveDeltas = 
  (localResponding || resumedTurnActive || Boolean(inferredTurnId)) && 
  Boolean(liveTurnId);
```

**优点：**
- 充分利用事件中的回合信息
- 保持回合对齐的安全性
- 自动恢复「回合身份丢失但事件仍在推送」的情况

**缺点：**
- 需要新增状态管理
- 推断的身份可能不准确（如果事件来自不同回合）

---

### 方案 C：改进自愈机制（补充方案）

**核心思路：**
检测到自愈失败后，不再无限重试，而是采用降级策略。

**实现：**

```typescript
// use-workspace-live.ts
const [failedRefreshCount, setFailedRefreshCount] = useState(0);

const handleUnownedTurn = useCallback((turnId: string) => {
  // ... 原有节流逻辑
  
  reportUnownedTurn(sessionId, turnId);
  
  if (refreshRuntimeState) {
    refreshRuntimeState();
    reportSnapshotRefresh(sessionId);
    
    // 新增：记录刷新次数
    setFailedRefreshCount(prev => prev + 1);
    
    // 刷新 3 次后仍失败 → 启用推断模式
    if (failedRefreshCount >= 3) {
      console.warn('[自愈降级] 快照刷新失败 3 次，启用事件推断模式');
      setInferredTurnId(turnId);
      setFailedRefreshCount(0);  // 重置计数
    }
  }
}, [refreshRuntimeState, sessionId, failedRefreshCount]);

// 当续传成功认领时，清除失败计数
useEffect(() => {
  if (resumedTurnId) {
    setFailedRefreshCount(0);
  }
}, [resumedTurnId]);
```

**优点：**
- 避免自愈死循环
- 提供降级策略
- 保持主流程的严格性

**缺点：**
- 逻辑更复杂
- 需要额外的状态管理

---

### 方案 D：游标对齐修复（根本解决）

**核心思路：**
如果问题是「回合已结束但 SSE 仍在重放历史」，应该在检测到这种情况时主动推进游标。

**实现：**

```typescript
// use-session-runtime-stream.ts
onEvent: (event) => {
  const turnId = getRuntimeEventTurnId(event);
  const deltaKind = getRuntimeDeltaKind(event.type);
  
  // 新增：检测「历史回合的增量」
  if (turnId && deltaKind && !liveTurnId) {
    // 本地无回合身份，但事件带回合 ID
    // 可能是历史事件重放
    
    // 从快照获取当前活跃回合
    const activeTurn = sessionActiveTurnRef.current;
    
    if (!activeTurn || activeTurn.turnId !== turnId) {
      // 事件的回合与快照不一致（或快照无活跃回合）
      // 这是历史事件，应该跳过而不是渲染
      console.warn('[游标对齐] 检测到历史回合的增量事件', {
        eventTurnId: turnId,
        activeTurnId: activeTurn?.turnId,
        eventSeq: event.seq
      });
      
      // 不推送到渲染队列，只推进游标
      // 避免 handleUnownedTurn 被触发
      return;
    }
  }
  
  // ... 原有逻辑
}
```

**配合后端优化：**

在 `/api/runtime/stream` 端点检测到客户端游标远远落后于当前活跃回合时，主动跳过中间的历史事件：

```go
// backend handler.go
if cursor < latestSeq - threshold && activeTurn != nil {
  // 客户端游标严重滞后，跳到活跃回合的起点
  cursor = activeTurn.StartSeq
}
```

**优点：**
- 从根本上解决游标对齐问题
- 避免历史事件污染当前渲染
- 减少自愈触发次数

**缺点：**
- 需要前后端配合
- 可能丢失部分历史事件（但这些事件不应该被实时渲染）

---

## 推荐实施路径

### 短期（立即修复）

**方案 A + 方案 C 组合：**

1. 放宽渲染闸门，允许「SSE 活动」自动打开
2. 改进自愈机制，3 次失败后启用降级策略
3. 添加详细的日志，监控实际发生的情况

**预期效果：**
- 立即解决「页面不更新」的问题
- 保持回合对齐的基本安全性
- 为长期方案收集数据

### 中期（优化设计）

**方案 B：从事件推断回合身份**

1. 在 `use-workspace-live` 中新增 `inferredTurnId` 状态
2. 在 `use-session-runtime-stream` 的 `onEvent` 中提取回合 ID
3. 闸门逻辑同时检查推断的身份

**预期效果：**
- 充分利用事件信息
- 减少对快照刷新的依赖
- 提高渲染的鲁棒性

### 长期（架构改进）

**方案 D：游标对齐优化**

1. 前端检测历史事件并跳过
2. 后端优化 `/runtime/stream` 的游标逻辑
3. 在回合结束时主动推进客户端游标

**预期效果：**
- 从根本上解决问题
- 减少不必要的事件传输
- 提升整体性能

---

## 测试用例

### 测试 1：回合结束后的历史重放

```typescript
test('should not block rendering when active_turn is null but events arrive', () => {
  // 1. 模拟本地回合结束
  localTurnId = null;
  
  // 2. 模拟快照返回 active_turn = null
  activeTurn = null;
  
  // 3. 模拟 SSE 推送历史事件
  const event = createDeltaEvent({ turn_id: 'turn-123' });
  
  // 4. 预期：渲染闸门应该打开（方案 A）
  //    或者：推断出 turn_id = 'turn-123'（方案 B）
  expect(renderLiveDeltas).toBe(true);
});
```

### 测试 2：自愈机制降级

```typescript
test('should fallback to inferred mode after 3 failed refreshes', () => {
  // 1. 触发 3 次未认领回合检测
  handleUnownedTurn('turn-123');
  handleUnownedTurn('turn-123');
  handleUnownedTurn('turn-123');
  
  // 2. 预期：第 4 次应该启用推断模式
  expect(inferredTurnId).toBe('turn-123');
  expect(renderLiveDeltas).toBe(true);
});
```

### 测试 3：历史事件过滤

```typescript
test('should skip historical events from ended turns', () => {
  // 1. 当前活跃回合：turn-456
  activeTurn = { turnId: 'turn-456' };
  
  // 2. 收到旧回合的增量事件：turn-123
  const event = createDeltaEvent({ turn_id: 'turn-123' });
  
  // 3. 预期：事件被跳过，不触发渲染
  expect(shouldApplyLiveDelta).toBe(false);
  expect(handleUnownedTurn).not.toHaveBeenCalled();
});
```

---

## 总结

**当前设计的核心问题：**
渲染闸门过于依赖外部状态（本地请求或快照续传），无法从 SSE 活动本身推断应该打开闸门。

**根本原因：**
设计假设「有回合身份才渲染」是安全的，但实际上「有 SSE 活动就应该渲染」才是正确的语义。回合身份应该是「对齐锁」而非「准入门槛」。

**推荐方案：**
- **短期**：放宽闸门 + 改进自愈（立即修复）
- **中期**：从事件推断回合身份（提高鲁棒性）
- **长期**：优化游标对齐（根本解决）

这样的分层方案既能立即解决用户痛点，又能逐步改进架构设计，避免技术债累积。
