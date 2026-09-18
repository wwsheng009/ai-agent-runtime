# 前端会话渲染问题分析报告

> **修订说明（2026-09-18 完整性审查）**
>
> 本报告的早期版本把「快照 `active_turn=null`」等同于「回合已结束」。经后端契约核验，需要更正与补充：
>
> 1. **`replay:true` ≠ 历史**：store 侧帧（含连接建立后新落库的行）一律 `replay:true`；真正的「补齐 vs 新增」分界是注释帧 `: resumed from=A to=B`（`backend/internal/api/skills/session_runtime_stream.go:350-358`），前端目前把该注释当 keepalive 丢弃（`frontend/src/api/runtime/sse.ts:271-274`）。
> 2. **`active_turn` 是进程内注册表**（`backend/internal/api/skills/session_active_turn.go:49-57`）：跨进程/跨实例的活跃回合永远不会出现在 `/runtime` 快照里——这类情况无法靠反复刷新快照自愈。
> 3. **根因 A 需拆分为 A（回放补齐，非故障）与 B（跨进程活跃回合，需前端推断身份）**；两者判定证据与修复面不同，见修复方案 v2 §0.3。
> 4. 修复思路以 `../plan/frontend-sse-render-gate-recovery-plan-20260918.md`（v2）为准；本文件保留现象与排查记录。

## 问题现象

从诊断面板显示的数据：
- **渲染闸门：关**
- **被拦增量：100**
- **未认领回合：5**
- **快照刷新：5**
- SSE 有帧活动（最近帧：tool_finished/tool_started）
- 但页面没有更新

## 根因分析

### 1. 渲染闸门逻辑

位置：`frontend/src/hooks/workspace/use-workspace-live.ts:143-144`

```typescript
const renderLiveDeltas = 
  (localResponding || resumedTurnActive) && Boolean(liveTurnId);
```

**闸门打开的条件（必须同时满足）：**
- `localResponding` 本地回合在跑 **或** `resumedTurnActive` 续传回合活跃
- `liveTurnId` 有在途回合身份

### 2. 续传回合认领逻辑

位置：`frontend/src/hooks/workspace/use-resumed-session-turn.ts:100-104`

```typescript
const serverTurnId = normalizedSessionId 
  ? (activeTurn?.turnId ?? "").trim() 
  : "";
const occupiedByLocalTurn = Boolean(localTurnId?.trim()) || localResponding;
const adoptable = Boolean(serverTurnId) && !occupiedByLocalTurn;
```

**续传认领条件（必须同时满足）：**
- `serverTurnId` 非空（来自 `/runtime` 快照的 `active_turn`）
- `!occupiedByLocalTurn` 本地没有在途回合

### 3. 未认领回合自愈机制

位置：`frontend/src/hooks/workspace/use-workspace-live.ts:220-223`

```typescript
if (!liveTurnId) {
  if (turnId && deltaKind) {
    handleUnownedTurn(turnId);  // 触发快照刷新
  }
}
```

当检测到「本地无回合身份但事件带回合ID」时，自动刷新 `/runtime` 快照。

---

## 问题诊断

从诊断数据可以推断：

1. **SSE 帧正常到达**（最近帧有 tool_finished/tool_started）
2. **自愈机制已触发 5 次**（快照刷新：5）
3. **但续传回合始终未被认领**（未认领回合：5，渲染闸门：关）
4. **导致 100 个增量帧被拦截**（被拦增量：100）

### 可能的根因（优先级排序）

#### 根因 A：快照返回的 `active_turn` 为空
**症状匹配度：★★★★★**

```
/runtime API 返回 active_turn: null
→ serverTurnId 为空
→ adoptable = false
→ resumedTurnId = null
→ liveTurnId = null
→ renderLiveDeltas = false
→ 闸门关闭
```

**验证方法：**
- 检查浏览器开发者工具 Network 面板
- 查看 `/api/runtime/sessions/{id}/runtime` 的响应
- 确认 `active_turn` 字段是否存在且非空

**可能原因：**
- 服务端回合已经结束（正常完成/错误/取消）
- 但 SSE 仍在重放历史事件（游标未对齐）
- 或者是不同回合的事件（turnId 不匹配）

---

#### 根因 B：本地回合身份残留
**症状匹配度：★★★☆☆**

```
localResponding 没有正确清空
→ occupiedByLocalTurn = true
→ adoptable = false
→ 续传回合无法认领
```

**验证方法：**
- 在诊断面板查看「渲染闸门」区块
- 确认 `localResponding` 状态
- 检查「在途回合」字段是否有残留

---

#### 根因 C：线程末尾不可认领
**症状匹配度：★★☆☆☆**

位置：`frontend/src/lib/thread-state/resumed-turn.ts:80-95`

续传认领只在以下条件同时满足时才写入线程：
- 线程末尾是 assistant 消息
- 该消息没有 `runtimeTurnId`（未被其他回合占用）
- 该消息不是 System prompt 基础设施行
- 未定稿（`streaming !== true && streaming !== false && !interrupted`）

**验证方法：**
- 检查消息列表最后一条消息的属性
- 确认是否满足认领条件

---

#### 根因 D：会话 ID 不匹配
**症状匹配度：★☆☆☆☆**

快照拉取的会话 ID 与当前选中会话不一致。

**验证方法：**
- 确认诊断面板显示的会话 ID
- 确认 URL 路由中的会话 ID
- 检查 `/runtime` API 请求的会话 ID

---

## 排查步骤

### 第一步：确认快照内容（根因 A）

打开浏览器开发者工具：

1. **Network 面板**
   - 筛选 `runtime` 请求
   - 找到 `/api/runtime/sessions/{sessionId}/runtime` 的最近一次请求
   - 查看 Response：
     ```json
     {
       "state": { ... },
       "active_turn": {
         "turn_id": "turn-xxx",  // 这个字段是否存在？
         "created_at": "..."
       }
     }
     ```

2. **Console 面板**
   - 如果响应中 `active_turn` 为 `null`，说明服务端认为回合已结束
   - 但 SSE 仍在推送该回合的事件，说明：
     - 游标对齐问题（`/runtime/stream?after=X` 的游标过旧）
     - 或者是历史回放（窗口回放与实时流重复）

### 第二步：确认本地回合状态（根因 B）

在诊断面板「网络详情 → 渲染闸门」区块查看：
- **在途回合**：应该显示回合 ID 或「无」
- **本地直连回合** (`liveTurnId`)：是否有残留？
- **续传回合** (`resumedTurnId`)：是否为空？
- **本地响应中** (`localResponding`)：应该为 false

如果 `localResponding` 为 true，检查：
- `frontend/src/hooks/workspace/use-workspace-agent-chat-turn.ts`
- 确认 chat 请求是否正确结束并清理状态

### 第三步：确认线程末尾消息（根因 C）

在 Console 输入：
```javascript
// 获取当前线程
const thread = window.__REACT_DEVTOOLS_GLOBAL_HOOK__?.renderers?.get(1)?.getCurrentFiber()
// 或直接查看消息列表最后一条

// 检查末尾消息属性
const lastMessage = thread.messages[thread.messages.length - 1]
console.log({
  role: lastMessage.role,
  runtimeTurnId: lastMessage.runtimeTurnId,
  streaming: lastMessage.streaming,
  interrupted: lastMessage.interrupted
})
```

期望结果：
- `role: "assistant"`
- `runtimeTurnId: undefined` 或为空
- `streaming: undefined` 或 `true`（不能是 `false`）
- `interrupted: false` 或 undefined

### 第四步：确认 SSE 事件内容

在诊断面板「网络详情 → 直连回合 → 最近帧」查看：
- 事件是否带 `turn_id`？
- 该 `turn_id` 与快照的 `active_turn.turn_id` 是否一致？

---

## 修复方向

### 方向 1：游标对齐问题（最可能）

如果确认是「快照显示回合已结束，但 SSE 仍在推历史事件」：

**问题：** `/runtime/stream?after=X` 的游标 X 过旧，导致已结束回合的事件被重放。

**修复：**
- 检查 `frontend/src/hooks/workspace/use-session-runtime-stream.ts:200`
- 确认 `getReplayCursor` 返回的游标是否正确
- 可能需要在回合结束时强制推进游标

### 方向 2：快照刷新时机问题

如果自愈机制触发了 5 次但都失败：

**问题：** 快照刷新的节流窗口（3秒）可能导致回合已结束但仍在节流期内。

**修复：**
- 调整 `UNOWNED_TURN_REFRESH_MS`（当前 3000ms）
- 或者在回合结束事件到达时立即清理状态

### 方向 3：回合结束信号缺失

如果服务端回合结束了，但前端没有收到终止信号：

**修复：**
- 检查 `applyRuntimeEventToThread` 是否正确处理 `turn.finished` 等终止事件
- 确保终止事件会清空 `streaming` 标记

---

## 临时解决方案（用户操作）

如果用户遇到此问题，可以尝试：

1. **刷新页面**（F5）
   - 重新拉取快照和历史
   - 游标重新对齐

2. **切换到其他会话再切回来**
   - 触发会话切换的快照刷新
   - 清理旧的订阅状态

3. **停止当前回合**
   - 点击「停止」按钮
   - 手动终止服务端回合

---

## 代码位置索引

| 组件 | 文件 | 行号 | 说明 |
|------|------|------|------|
| 渲染闸门逻辑 | `use-workspace-live.ts` | 143-144 | renderLiveDeltas 计算 |
| 续传认领逻辑 | `use-resumed-session-turn.ts` | 100-104 | adoptable 判定 |
| 认领写入逻辑 | `resumed-turn.ts` | 61-103 | adoptResumedTurnInThread |
| 未认领检测 | `use-workspace-live.ts` | 220-223 | handleUnownedTurn |
| 快照刷新 | `use-workspace-live.ts` | 176-178 | refreshRuntimeState |
| 被拦增量记账 | `use-workspace-live.ts` | 228-233 | reportBlockedDelta |
| 诊断面板 | `session-detail-network.tsx` | 188+ | SessionDetailNetworkSection |
