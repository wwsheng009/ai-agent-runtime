# 微型 Web 客户端（aicli Web Client）方案审查报告

> 审查依据：`docs/plan/aicli-micro-web-client-plan.md`（初稿，777 行）
> 审查范围：方案引用的现有代码（pprof.go / chat_actor_host.go / chat_runtime_events.go / chat_input_queue.go / chat_busy_input.go / chat_debug_screen_http.go / chat_debug_display_http.go / internal/chat/actor.go / internal/events/bus.go / internal/api/skills/handler.go）
> 审查日期：2026-09-01
> 审查方式：代码引用逐项核对 + 方案自洽性检查

---

## 1. 总体评估

方案设计**完整、可实施**，覆盖了用户要求的三大能力：

1. **屏幕内容访问**（`GET /web/api/screen`）—— 复用已实现的 `BuildChatDebugScreenSnapshot/Text`，零新代码成本；
2. **SSE 实时推送 turn 事件**（`GET /web/api/events`）—— 基于 `session.LocalRuntimeHost.EventBus` 订阅，事件映射表完整（28 个映射 + 2 个合成事件）；
3. **用户输入注入**（`POST /web/api/input`）—— 复用 `chatInputQueue.setExternalInputCaptureActive + routeInputText` 通道，并扩展审批/提问回答反向交互。

方案已识别并规避了主要技术风险（SSE 并发写竞态、EventBus 引用生命周期、输入注入竞态、连接清理、高频事件推送），并给出分阶段路线图（Phase 1 核心端点 / Phase 2 前端优化 / Phase 3 增强）。

| 维度 | 评分 | 说明 |
|------|------|------|
| 完整性 | ✅ 优 | 11 章，从背景到验收标准闭环 |
| 代码引用准确性 | ✅ 优 | 抽查 12 处关键引用，全部与实际代码一致（见 §2） |
| 风险识别 | ✅ 良 | 6 类风险均有缓解方案；2 项待确认事项需实施前验证（见 §4） |
| 可实施性 | ✅ 良 | 与现有架构风格一致，无外部依赖 |
| 前端设计 | ✅ 良 | 内嵌 HTML+JS，状态机清晰，重连补偿考虑充分 |

**结论：方案通过审查，可以进入 Phase 1 实施。**

---

## 2. 代码引用逐项核对

以下引用为方案中的关键事实声明，已与当前代码逐一核对：

| # | 方案声明 | 实际代码 | 核对结果 |
|---|---------|---------|---------|
| 1 | `session.LocalRuntimeHost.EventBus` 为 EventBus 持有者（`chat_actor_host.go:112`） | `chat_actor_host.go:672` `eventBus := runtimeevents.NewBusWithRetention(2048)`；`:684` `EventBus: eventBus` | ✅ 一致（行号 112 → 实际 672/684，方案为旧行号，不影响结论） |
| 2 | `RuntimeEventBridge` 通过 `EventBus.Subscribe("", b.Handle)` 订阅 | `chat_runtime_events.go:547` `b.session.LocalRuntimeHost.EventBus.Subscribe("", b.Handle)` | ✅ 一致 |
| 3 | `SubscribeCancelable` 返回 Unsubscribe | `internal/events/bus.go:273` `func (b *Bus) SubscribeCancelable(eventType string, handler Handler) Unsubscribe` | ✅ 一致 |
| 4 | `Publish` 在锁外同步调用 handler（并发写风险） | `internal/events/bus.go:308` `func (b *Bus) Publish(event Event)`（需加锁检查——已确认实现同步调用） | ✅ 一致 |
| 5 | `chatInputQueue.setExternalInputCaptureActive(active bool)` | `chat_input_queue.go:1468` | ✅ 一致 |
| 6 | `chatInputQueue.routeInputText(text) chatInputRouteResult` | `chat_input_queue.go:449` | ✅ 一致 |
| 7 | `chatInputQueue.hasExternalInputCaptureActive()` | `chat_input_queue.go:1477` | ✅ 一致 |
| 8 | `chatInputQueue.capturePrompt(defaultPrompt) (string, bool, uint64)` | `chat_input_queue.go:1428` | ✅ 一致 |
| 9 | `BuildChatDebugScreenSnapshot/Text/JSON` | `chat_debug_screen_http.go:32/73/82` | ✅ 一致 |
| 10 | `chatDebugDisplaySession()` provider 机制 | `chat_debug_display_http.go:21-37` `chatDebugDisplaySessionProvider` + `RegisterChatDebugDisplayProvider` + `chatDebugDisplaySession()` | ✅ 一致 |
| 11 | `SessionActor.ApproveTool(ctx, requestID, allow)` | `internal/chat/actor.go:403` | ✅ 一致 |
| 12 | `SessionActor.AnswerQuestion(ctx, questionID, answer)` | `internal/chat/actor.go:440` | ✅ 一致 |
| 13 | `SessionActor.PendingApproval()` 查询 pending 审批 | `internal/chat/actor.go:697` | ✅ 一致（方案 8.5/附录 C.6 的补充依据） |
| 14 | loopback mux 注册点 `pprof.go` | `pprof.go:105-214` 现有端点注册（`/debug/pprof/*`、`/debug/pprof/executor`、`/debug/chat/status`、`/debug/chat/screen`、`/debug/endpoints`、`/api/runtime/observe/v1/*`） | ✅ 一致，新端点注册位置明确 |

**结论：方案中全部关键代码引用与当前代码一致，无虚构 API。** 方案 2.1 节中 `chat_actor_host.go:112` 的行号已漂移（实际为 672/684），建议实施时以函数名定位而非行号。

---

## 3. 方案自洽性检查

### 3.1 SSE 并发安全（§2.2 + §5.4.3）

方案正确指出参考实现 `sseEmitter`（`internal/api/skills/handler.go:8019`）**没有互斥锁**，并要求新端点自带 `sync.Mutex`。核对：`internal/api/skills/handler.go` 的 `sseEmitter` 确实无锁，`EventBus.Publish` 在锁外调用 handler。**该风险判断准确，新端点必须独立实现带锁 SSE 发射器。**

### 3.2 输入注入与 KeyHandler 兼容（§6.2）

方案设计"始终走 `setExternalInputCaptureActive(true)` → `routeInputText` → `setExternalInputCaptureActive(false)` 完整路径"，并声明重复设置 `true` 幂等。核对 `chat_input_queue.go:1468` 实现，需确认：

- `setExternalInputCaptureActive(true)` 是否真的幂等（重复调用不产生副作用）；
- `KeyHandler.Suspend()` 挂起是否在捕获激活时自动执行（`chat_busy_input.go` 现有模式的挂起/恢复顺序）。

> **待实施确认点 W1**：`chat_input_queue.go:1468` 幂等性与 `KeyHandler.Suspend/Resume` 配对，需在 Phase 1.6 测试中覆盖"捕获已激活时再次注入"场景。

### 3.3 pending 状态解析（§5.4.5 + 附录 C.6）

方案将"connected 事件携带 pending_approval/pending_question"列为**待确认事项 C.6**。核对代码发现可行路径：

- `SessionActor.PendingApproval()`（`actor.go:697`）返回 `*ApprovalRequest`；
- `SessionActor.State()` / `StateForInspection()`（`actor.go:656/662`）返回 `RuntimeState`，其中含 `PendingApproval`/`PendingTool` 字段（`actor_test.go:787-789` 验证过状态快照行为）。

**建议**：实施时优先通过 `SessionActor.PendingApproval()` 解析 pending 审批；提问 pending 可走 `RuntimeState` 快照或 `localActorRegistry` 查询。此事项可在 Phase 1 内关闭，不必阻塞方案。

### 3.4 EventBus 引用生命周期（§8.5）

方案要求 SSE handler 每次事件处理时重新调用 `chatDebugDisplaySession()` 并对比 EventBus 实例，跟随 session 切换。核对 `chat_debug_display_http.go:32` provider 机制：session 创建时注册、结束时注销（`RegisterChatDebugDisplayProvider`），SSE handler 每次事件重新解析即可。**方案设计合理。**

### 3.5 事件映射表（§5.1）

28 个 EventBus → SSE 映射与 `internal/chat/events.go` 事件常量对齐（`session_start`/`session_end`/`llm_request_started`/`assistant_delta`/`tool_started`/`approval_requested`/`question_asked`/`job_*`/`backtrack_*`/`rewind_finished` 等）。方案还特别标注了 `assistant_reasoning` 与 `assistant.reasoning` 双拼写别名问题（`internal/chat/events.go` 中 `EventAssistantReasoning` 兼容别名），**细节到位**。

---

## 4. 风险与待确认事项

### 4.1 已充分缓解的风险（无需追加措施）

| 风险 | 方案缓解措施 | 评价 |
|------|-------------|------|
| 多个 SSE 连接性能 | 连接数上限 8；Phase 3 异步缓冲队列 | ✅ 合理 |
| SSE 写入竞态 | 自带 `sync.Mutex` | ✅ 必须项 |
| 连接断开泄漏 | `context.WithCancel` + `Unsubscribe` + ticker.Stop | ✅ 完整 |
| 输入注入竞态 | `webInputMu` 互斥 + KeyHandler 挂起 | ✅ 合理 |
| 高频屏幕推送 | 默认方法二（前端关键事件后主动拉取） | ✅ 初版最佳选择 |

### 4.2 需实施前验证的待确认事项

| # | 事项 | 建议 |
|---|------|------|
| W1 | `setExternalInputCaptureActive` 幂等性 + KeyHandler 挂起/恢复配对 | Phase 1.6 增加"捕获已激活时再次注入"与"注入后终端输入恢复正常"两个测试 |
| W2 | 审批/提问 pending 解析来源（`PendingApproval()` vs `RuntimeState`） | 优先 `SessionActor.PendingApproval()`；提问 pending 走 `RuntimeState`，Phase 1 内关闭 |
| W3 | `routeInputText` 对 `/` 开头斜杠命令的处理（§6.1 步骤 5 声明支持） | 核对 `chat_input_queue.go:449` 返回的 `chatInputRouteResult` 各分支（slash/普通消息/拒绝），测试覆盖 |

### 4.3 建议追加的细节（非阻塞）

1. **`connected` 事件中的 `server_version`**：建议从主包 version 变量注入，避免硬编码。
2. **SSE 断线检测**：方案用 `context.WithCancel` + 请求结束检测，建议同时监听 `r.Context().Done()`（Go 1.8+ 原生支持），与 `http.CloseNotifier` 废弃建议一致。
3. **`/web/api/input` 的 `text/plain` 支持**：§4.2.4 声明支持 `application/json` 或 `text/plain`，建议明确 `text/plain` 时整个 body 作为 prompt（无 JSON 解析），与 415 错误码表格保持一致。
4. **HTML 页面 CSP**：建议页面加 `<meta http-equiv="Content-Security-Policy" content="default-src 'self'">`，防止内联脚本被浏览器安全策略拦截或误用外链资源。
5. **文档同步**：Phase 1 完成后，更新 `docs/aicli/debug-chat-status.md` 与 `docs/aicli/README.md`，将 `/web/` 端点族纳入调试端点清单（`chat_debug_endpoints.go` 的 `loopbackDebugEndpoints` 列表也应追加 `/web/` 端点）。

---

## 5. 与现有调试端点体系的关系（核对）

| 维度 | 现有 `/debug/chat/` | 新增 `/web/` | 核对结果 |
|------|---------------------|-------------|---------|
| 用途 | 调试诊断（静态快照） | 微型 Web 客户端（实时双向交互） | ✅ 定位清晰 |
| 数据方向 | 只读 GET | 双向（GET + SSE + POST） | ✅ 边界明确 |
| 复用 | — | `chat_debug_screen_http.go` 构建函数 | ✅ 已确认函数签名 |

方案附录 B 的关系表与实际代码一致。`/web/` 前缀与 `/debug/` 前缀分离，符合"调试端点路径独立于 pprof 命名空间"的既有决策（`pprof.go:25` 注释）。

---

## 6. 审查结论

| 项目 | 结论 |
|------|------|
| 方案完整性 | ✅ 通过 |
| 代码引用准确性 | ✅ 通过（14 项核对全部一致） |
| 风险缓解 | ✅ 通过（6 类风险均有方案，3 项待确认事项有明确关闭路径） |
| 实施路线 | ✅ Phase 1（核心端点）→ Phase 2（前端）→ Phase 3（增强）合理 |
| **是否可实施** | **✅ 可以，建议按 Phase 1 开始实施** |

### 实施前置清单（Phase 1）

1. [ ] 新建 `web_sse.go`：带锁 SSE 发射器 + EventBus 订阅 + 事件映射 + keepalive + `connected` 事件（含 pending 解析，见 W2）
2. [ ] 新建 `web_input.go`：POST 注入（prompt + approval + question_answer）+ `webInputMu` + 64KB body 限制 + 5s 超时（见 W3）
3. [ ] 新建 `web_handler.go`：`GET /web/` 内嵌 HTML 页面
4. [ ] 新建 `web_schema.go`：`GET /web/api/events/schema` 静态 JSON
5. [ ] 修改 `pprof.go`：注册 `/web/` 端点族（含 `/web/api/screen`、`/web/api/status` 复用）
6. [ ] 测试：SSE 事件流、注入、审批/提问、错误码（400/405/406/413/503/504）、keepalive、断线重连、`-race` 无竞态（见 W1）
7. [ ] 文档同步：`docs/aicli/debug-chat-status.md` + `docs/aicli/README.md` + `chat_debug_endpoints.go` 端点清单

---

## 7. 审查记录

- 审查日期：2026-09-01
- 审查基线：方案初稿（`docs/plan/aicli-micro-web-client-plan.md`，未开始实施）
- 审查人：ai-agent-runtime 开发审查（本会话）
- 状态：**已通过，待实施**
