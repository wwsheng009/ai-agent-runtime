# SSE「LLM → UI 输出」缓冲审计与优化（2026-09-27）

> 适用范围：runtime-server（`backend/internal/api/runtimeapi`）+ React 工作台（`frontend/src`）。
> 结论口径：逐跳检查「LLM 流式响应 → 智能体 → SSE → 浏览器 → 像素」链路上的缓冲/节流机制，
> 并落地不改变默认语义的低风险优化。

## 1. 链路与缓冲点（审计结论）

主链路（`POST /api/agent/chat`，ReAct 开）：

| 跳 | 位置 | 机制 | 最坏迟滞 |
|---|---|---|---|
| provider SSE 解析 | `backend/internal/llm/adapter/sse.go` | bufio.Scanner 按行即出，无整包聚合 | ~0 |
| 流式聚合调用 | `backend/internal/llm/provider.go`（tee + `HandleResponse(true, streamReader)`） | 边解析边回调，body 仅旁路留档 | ~0 |
| agent 上报 | `backend/internal/agent/loop.go`（每个 chunk 直发 bus + StreamSink） | 无攒批 | ~0 |
| 落盘 | 增量帧 wire-only；其余走 `EventPersistBuffer`（25ms / 64 条） | 不阻塞 wire | ≤25ms（仅回放通道） |
| 出站合并 | `pacedFlushWriter`（16KB bufio + 50ms tick） | 每连接 ≤20 次 flush/s | ≤50ms/帧 |
| 读侧让出 | `frontend/src/api/runtime/sse.ts`（8ms 预算） | 只切分 task，不缓冲 | ≤8ms |
| 页面级提交 | `streaming-frame.ts`（120ms；结构快照 1000ms） | 只影响 thread store 副本，实时正文走 live 通道 | 视觉 ~0 |
| 打字机 | `use-typewriter.ts`（32ms 提交节拍） | 按真实时间推进、积压自适应追赶 | 首字 ≤32ms |

次要通道（`GET /api/runtime/sessions/{id}/runtime/stream`）额外经过
`EventPersistBuffer`（25ms）与 `WatchEvents` 唤醒（通知通道容量 1，兜底轮询 5s）。
正常持续事件下不会触顶；重放/补齐的拦截由渲染闸门决定（见 §4）。

## 2. 已落地优化

### 2.1 直连回合流支持 `flush_ms`（与会话运行时流同口径）

- 新增 `resolveStreamFlushInterval`（`session_runtime_stream.go`），两个 SSE 入口共用：
  缺省 50ms、显式 `0` 关闭合并（回滚面）、`>1000ms` 或非法值返回 400。
- `POST /api/agent/chat?flush_ms=0` 现在与 runtime stream 一样可关闭出站合并，
  用于对延迟敏感的本地调试/低延迟部署；默认行为不变。
- 测试：`session_runtime_stream_flush_query_test.go`。

### 2.2 非 ReAct 静态分支开流（消除「整轮缓冲」）

- 背景：此前只有 `EnableReAct` 的请求设置 `agentConfig.Options["stream"]=true`；
  ReAct 关闭（设置项可关）且 skill route 命中时，本轮模型全部生成完才由
  `streamStaticResult` 一次性下发——正是「LLM 已响应、UI 不出字」的形态。
- 改动：`handler.go` 中 `req.Stream || wantsEventStream(r)` 即开流，不再要求 ReAct。
  静态分支的增量经 runtime 通道逐段可见；回合末静态快照帧改为
  `mode=replace`（权威全文），前端按覆盖应用，不重复拼接。
- 前端：`isReplaceStreamChunk`（`lib/thread-state/shared.ts`）+ `onChunk` 覆盖分支
  （`agent-chat-turn/stream-handlers.ts`）；类型补 `mode?: string`。
- 测试：`stream-handlers.replace.test.ts`（append/replace/无去重键三态）。

### 2.3 打字机首帧立即提交

- `use-typewriter.ts` 帧循环起点回拨一个提交间隔：流开始后的第一帧即可提交
  （首字不再等满 32ms），之后仍按真实经过时间推进，节奏与上限不变。

## 3. 调优与验证

- 关掉出站合并：`curl -N 'http://127.0.0.1:8101/api/agent/chat?flush_ms=0' ...`；
  或对会话运行时流 `.../runtime/stream?flush_ms=0`。
- 观测帧到达节奏与服务端投递延迟：可复用前端探针脚本
  （读 `_event.timestamp` 与服务端时钟对比），关注 `p95` 与 `maxGap`。
- 回归门禁：`go test ./internal/api/runtimeapi`、`npx vitest run src/lib/thread-state
  src/hooks/workspace/agent-chat-turn`、`npx tsc -p tsconfig.app.json --noEmit`。

## 4. 未实施（需现场取证后再动）

1. **渲染闸门丢帧**：runtime 通道的增量在闸门关闭时被拦截/丢弃，快照刷新后整块出现。
   修复方案见 `docs/plan/frontend-sse-render-gate-recovery-plan-20260918.md`（分批、带 flag）。
2. **两通道去重抢占**：chat 直连与 runtime 通道共享 delta key「先到先 claim」；
   若较慢通道先 claim，实时帧被静默跳过。需按 `replay/live` 标记给直连通道优先级。
3. **整包缓冲地雷**：`ProviderWrapper.Chat`（`provider.go`）在 `Stream=true` 时仍
   `io.ReadAll` 整包后才回调；当前 agent 主路径走 `Call`/`CallStreamingAggregate`
   不会命中，但 legacy 调用方一旦命中即为整轮延迟。
