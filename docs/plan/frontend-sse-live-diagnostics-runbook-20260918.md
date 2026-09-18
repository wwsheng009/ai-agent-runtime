# 快速诊断脚本

> 2026-09-18 补充：先做下面的「帧级核验」——不需要运行脚本，就能把现场判定为三种分支之一（见修复方案 v2 §0.3）；脚本部分用于进一步读取前端状态。

## 0. 帧级核验（免脚本，先做）

1. **看 `/runtime` 快照的 `active_turn`**
   DevTools → Network → 筛选 `runtime` → 打开 `GET /api/runtime/sessions/session_20260918081157_TZduqX8h/runtime` 的最新响应：
   - `active_turn` 非空 → 分支 C（前端认领/刷新链路问题）；
   - `active_turn: null` → 继续第 2 步。
2. **看 SSE 帧上的来源标记与补齐边界**
   Network → 找到 `/api/runtime/stream?...after=...` 请求 → EventStream 面板：
   - 帧 payload 带 `replay:true`（经 EventStore 投递）或 `live:true`（总线直投 live-only）；
   - 注释行 `: resumed from=A to=B`：`B` 是本次补齐追平的最高 seq；
   - **seq ≤ B 的 delta 属回放补齐**（分支 A：页面不更新是正确行为，缺陷在自愈与面板误报）；
   - **seq > B 或 `live:true` 且持续到达**（分支 B：服务端仍在产新输出，需前端推断身份）。
3. **交叉核对时间戳与来源进程**
   - 帧 payload 的 `timestamp` 是否新鲜（陈旧 → 倾向 A）；
   - 是否有另一个浏览器标签 / aicli TUI / 另一个 server 实例在驱动这个会话（有 → 倾向 B：`active_turn` 只登记本进程的 web 回合）。

判定后的修复面：A → 批次 1（口径与自愈）；B → 批次 2（推断与认领）；C → 按脚本核对 React 状态与 fetch 失败。

## 1. 浏览器 Console 脚本

将以下代码粘贴到浏览器开发者工具的 Console 中执行，获取详细诊断信息：

```javascript
// ============================================
// 前端会话渲染诊断脚本
// ============================================

(function diagnoseSessionRender() {
  console.log('%c=== 会话渲染诊断 ===', 'color: #38bdf8; font-size: 16px; font-weight: bold');
  
  // 1. 检查 live-diagnostics store
  console.log('\n%c[1] Live Diagnostics Store 状态', 'color: #6ee7b7; font-weight: bold');
  
  try {
    // 尝试从 window 获取当前会话 ID（可能需要调整）
    const currentSessionId = window.location.pathname.match(/\/sessions\/([^\/]+)/)?.[1];
    console.log('当前会话 ID:', currentSessionId || '未找到');
    
    // 检查 live-diagnostics 模块是否可访问
    console.log('提示：需要在前端代码中暴露诊断接口');
    console.log('建议在 session-detail-network.tsx 中临时添加：');
    console.log('window.__DEBUG_LIVE_DIAGNOSTICS__ = { getLiveDiagnosticsSnapshot }');
    
  } catch (error) {
    console.error('无法访问诊断数据:', error);
  }
  
  // 2. 检查 API 请求
  console.log('\n%c[2] 检查最近的 API 请求', 'color: #6ee7b7; font-weight: bold');
  console.log('请在 Network 面板中筛选以下请求：');
  console.log('- /api/runtime/sessions/{id}/runtime  → 查看 active_turn 字段');
  console.log('- /api/runtime/stream?after=X         → 查看事件流');
  
  // 3. React DevTools 检查
  console.log('\n%c[3] React 组件状态检查', 'color: #6ee7b7; font-weight: bold');
  
  if (window.__REACT_DEVTOOLS_GLOBAL_HOOK__) {
    console.log('✓ React DevTools 已安装');
    console.log('\n在 React DevTools 中查找以下 hooks：');
    console.log('- useWorkspaceLive → 查看 liveTurnId, resumedTurnId, renderLiveDeltas');
    console.log('- useResumedSessionTurn → 查看 adoptable, serverTurnId, occupiedByLocalTurn');
    console.log('- useSessionRuntimeState → 查看 activeTurn');
  } else {
    console.warn('✗ React DevTools 未安装，建议安装后重试');
  }
  
  // 4. 存储检查
  console.log('\n%c[4] 本地存储检查', 'color: #6ee7b7; font-weight: bold');
  
  try {
    // 检查可能导致状态残留的存储
    const keys = Object.keys(localStorage).filter(k => 
      k.includes('session') || k.includes('thread') || k.includes('turn')
    );
    if (keys.length > 0) {
      console.log('发现相关 localStorage 键:', keys);
    } else {
      console.log('未发现相关 localStorage 键');
    }
  } catch (error) {
    console.error('无法访问 localStorage:', error);
  }
  
  // 5. 性能标记检查
  console.log('\n%c[5] 性能标记', 'color: #6ee7b7; font-weight: bold');
  
  try {
    const perfEntries = performance.getEntriesByType('mark').filter(e => 
      e.name.includes('session') || e.name.includes('stream')
    );
    if (perfEntries.length > 0) {
      console.log('发现性能标记:', perfEntries.map(e => e.name));
    }
  } catch (error) {
    console.log('无性能标记数据');
  }
  
  // 6. 生成诊断指令
  console.log('\n%c[6] 下一步操作', 'color: #e9a568; font-weight: bold');
  console.log(`
📋 手动检查清单：

1️⃣ 检查 /runtime 快照：
   Network 面板 → 筛选 "runtime" → 找到最新的 GET 请求
   查看响应中的 active_turn 字段：
   - 如果为 null：说明服务端认为回合已结束
   - 如果有值：记录 turn_id

2️⃣ 检查 SSE 事件流：
   诊断面板「网络详情 → 直连回合 → 最近帧」
   查看事件中的 turn_id 是否与步骤 1 的 active_turn.turn_id 一致

3️⃣ 检查渲染闸门：
   诊断面板「网络详情 → 渲染闸门」
   - 在途回合：应该显示 turn_id 或「无」
   - 渲染闸门：应该显示「开」
   - 如果显示「关」，检查上面两个字段

4️⃣ 检查线程消息：
   在 Console 执行以下代码查看末尾消息：
   
   // 复制下面的代码执行
   (() => {
     const msgList = document.querySelector('[data-testid*="message"]')?.parentElement;
     if (msgList) {
       console.log('消息列表已找到，检查最后一条消息的 DOM 属性');
       const lastMsg = msgList.lastElementChild;
       console.log('最后一条消息:', lastMsg);
     } else {
       console.warn('未找到消息列表 DOM');
     }
   })();

5️⃣ 如果以上都正常，可能是游标对齐问题：
   检查 /runtime/stream 请求的 after 参数
   与当前会话的最大事件 seq 是否匹配
  `);
  
  console.log('\n%c✅ 诊断脚本执行完成', 'color: #38bdf8; font-size: 14px; font-weight: bold');
  console.log('请按照上述清单逐项检查，并将结果反馈给开发团队\n');
  
})();
```

## 如何使用

1. **打开浏览器开发者工具**
   - Chrome/Edge: `F12` 或 `Ctrl+Shift+I`
   - 选择 `Console` 标签页

2. **粘贴并执行脚本**
   - 将上面的整段代码复制
   - 粘贴到 Console 中
   - 按 `Enter` 执行

3. **按照输出的指令逐项检查**
   - 脚本会打印彩色的分步指南
   - 按顺序完成每个检查项
   - 记录发现的异常

---

## 高级调试：添加临时诊断钩子

如果需要实时监控状态变化，可以在代码中临时添加以下调试钩子：

### 方法 1：在 use-workspace-live.ts 中添加日志

```typescript
// 在 frontend/src/hooks/workspace/use-workspace-live.ts 的第 145 行后添加：

useEffect(() => {
  console.log('[DEBUG] 渲染闸门状态变化:', {
    renderLiveDeltas,
    liveTurnId,
    resumedTurnId,
    localTurnId,
    localResponding,
    resumedTurnActive,
    sessionId,
    timestamp: new Date().toISOString()
  });
}, [renderLiveDeltas, liveTurnId, resumedTurnId, localTurnId, 
    localResponding, resumedTurnActive, sessionId]);
```

### 方法 2：在 use-resumed-session-turn.ts 中添加日志

```typescript
// 在 frontend/src/hooks/workspace/use-resumed-session-turn.ts 的第 105 行后添加：

useEffect(() => {
  console.log('[DEBUG] 续传认领状态:', {
    adoptable,
    serverTurnId,
    localTurnId,
    occupiedByLocalTurn,
    normalizedSessionId,
    activeTurn,
    timestamp: new Date().toISOString()
  });
}, [adoptable, serverTurnId, localTurnId, occupiedByLocalTurn, 
    normalizedSessionId, activeTurn]);
```

### 方法 3：在 resumed-turn.ts 中添加日志

```typescript
// 在 frontend/src/lib/thread-state/resumed-turn.ts 的 adoptResumedTurnInThread 函数开头添加：

export function adoptResumedTurnInThread(
  thread: Thread,
  turnId: string | null | undefined,
): Thread {
  const normalized = normalizeTurnId(turnId);
  
  console.log('[DEBUG] 尝试认领续传回合:', {
    turnId: normalized,
    threadId: thread.id,
    messageCount: thread.messages.length,
    lastMessage: thread.messages[thread.messages.length - 1]
  });
  
  if (!normalized || thread.messages.length === 0) {
    console.log('[DEBUG] 认领失败：回合 ID 为空或线程无消息');
    return thread;
  }
  
  // ... 原有代码
  
  // 在每个 return 前添加对应的日志
}
```

---

## 自动化检测方案

如果问题频繁出现，可以添加持续监控：

```typescript
// 在 frontend/src/hooks/workspace/use-workspace-live.ts 中添加：

useEffect(() => {
  // 检测「闸门关闭但有增量到达」的异常
  const checkInterval = setInterval(() => {
    const snapshot = getLiveDiagnosticsSnapshot(sessionId ?? '');
    
    if (!renderLiveDeltas && 
        snapshot.counters.blockedDeltas > 0 &&
        snapshot.runtime.events > 0) {
      console.error('[异常检测] 渲染闸门关闭但有增量被拦截:', {
        blockedDeltas: snapshot.counters.blockedDeltas,
        unownedTurns: snapshot.counters.unownedTurns,
        snapshotRefreshes: snapshot.counters.snapshotRefreshes,
        liveTurnId,
        resumedTurnId,
        localTurnId,
        renderLiveDeltas,
        timestamp: new Date().toISOString()
      });
      
      // 可选：自动触发一次强制刷新
      if (snapshot.counters.unownedTurns > 3) {
        console.warn('[自动修复] 触发强制快照刷新');
        refreshRuntimeState?.();
      }
    }
  }, 5000);
  
  return () => clearInterval(checkInterval);
}, [renderLiveDeltas, sessionId, liveTurnId, resumedTurnId, 
    localTurnId, refreshRuntimeState]);
```

---

## 预期输出示例

正常情况下，日志应该显示：

```
[DEBUG] 渲染闸门状态变化: {
  renderLiveDeltas: true,
  liveTurnId: "turn-abc123",
  resumedTurnId: "turn-abc123",
  localTurnId: null,
  localResponding: false,
  resumedTurnActive: true,
  sessionId: "sess-xyz789"
}
```

异常情况（闸门关闭）：

```
[DEBUG] 渲染闸门状态变化: {
  renderLiveDeltas: false,        ← 闸门关闭
  liveTurnId: null,                ← 无回合身份
  resumedTurnId: null,             ← 续传未认领
  localTurnId: null,
  localResponding: false,
  resumedTurnActive: false,
  sessionId: "sess-xyz789"
}

[DEBUG] 续传认领状态: {
  adoptable: false,                ← 不可认领
  serverTurnId: "",                ← 快照无 active_turn
  localTurnId: null,
  occupiedByLocalTurn: false,
  activeTurn: null                 ← 关键：快照为空
}
```

这会明确指向根因 A：快照中没有 `active_turn`。
