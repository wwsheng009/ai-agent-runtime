# aicli resume 后输入 prompt 不进入 LLM 请求交互：架构分析

> 日期：2026-08-31
> 范围：`backend/cmd/aicli/commands` 交互主循环与 actor 执行链路
> 关联：`docs/analysis/aicli-resume-recovery-backoff-postmortem.md`（上一轮 busy loop + 输入区冻结的复盘）

## 1. 问题现象

用户通过 `aicli resume <session_id>` 恢复会话后：

- 输入区（TUI composer）**能正常显示与输入**（上一轮修复的"输入区冻结"已解决）；
- 但输入用户 prompt 并回车后，**没有进入 LLM 请求交互**：没有模型请求、没有流式输出、也没有错误提示，界面停留在原地。

一句话定性：**输入被提交了，但请求从未发出——prompt 在提交链路中被无界等待吞掉，而不是被显式拒绝。**

## 2. 输入提交链路（resume 后实际走的路径）

resume 启动路径（`chat_setup.go:444-446`）：

```go
session.ActorFirstReady = true
session.ChatExecutor = newAICLIActorChatExecutor()
```

即 resume 后的会话走 **actor 运行时执行器**（`aicliActorChatExecutor`），而不是 legacy 直连执行器（`aicliSharedChatExecutor`）。这是理解问题的前提。

用户输入后的调用链：

```
runChatLoop (chat.go:1185)
├─ prepareInteractiveRead (chat_team_drain.go:106)
│  └─ waitForInteractivePromptReady (chat_team_drain.go:145)
│     └─ interactiveTeamPending → waitForTeamTerminal   ← 拦截点 A
├─ chatInteractiveReadLine (chat_input_queue.go:1055)
│  └─ composer.ReadLine → InputBox.ReadWithHistoryPromptWithHooks
└─ sendMessage (chat_send.go:12)
   ├─ ensureChatExecutor (chat_core.go:114)             ← 拦截点 B
   └─ executor.Execute (chat_actor_executor.go:114)
      ├─ chatActorForSession → SessionHub.GetOrCreate   ← warmup 等待
      ├─ acquireActorTurnGate                           ← 拦截点 C1
      ├─ waitForAICLIActorReady                         ← 拦截点 C2（最可疑）
      └─ submitAICLIActorPrompt                         ← 真正发起 LLM 请求
```

## 3. 三个拦截点与证据

### 拦截点 A：`prepareInteractiveRead` → `waitForTeamTerminal`（chat_team_drain.go:145-173）

```go
func waitForInteractivePromptReady(session *ChatSession) error {
	...
	pending := interactiveTeamPending(session)   // 检查 AmbientRunMeta 绑定的 team 是否 active
	if !pending {
		return nil
	}
	...
	if err := session.LocalRuntimeHost.waitForTeamTerminal(ctx, teamID); err != nil {
		return err
	}
	...
}
```

- resume 后如果 runtime session 的 `AmbientRunMeta.Team` 指向一个**仍为 active 的 team**（上次进程被杀/崩溃前遗留），`interactiveTeamPending` 返回 true，`waitForTeamTerminal` **阻塞等待该 team 终止**。
- 若该 team 是孤儿（没有活着的 lead 驱动它结束），等待不会收敛——prompt 不显示或主循环反复 continue，输入永远到不了 `chatInteractiveReadLine`。
- 影响：这是"输入前"的拦截。若用户能输入，说明此点已通过（team 已收敛）；若连 prompt 都不显示，则是此点卡住。

### 拦截点 B：`sendMessage` → `ensureChatExecutor`（chat_core.go:114-131）

```go
if session.ChatExecutor == nil {
	if session.ActorFirstReady && session.LocalRuntimeHost != nil {
		session.ChatExecutor = newAICLIActorChatExecutor()
	} else {
		return nil, fmt.Errorf("chat executor is not initialized")
	}
}
```

- 若 resume 路径未设置 `ActorFirstReady` 或 `LocalRuntimeHost` 为 nil，则 `sendMessage` 直接返回错误，输入被丢弃。
- resume 正常路径已设置（`chat_setup.go:444`），此点概率较低，但属于"输入被吞但无 LLM 请求"的候选之一。

### 拦截点 C：actor 执行器内的无界等待（最可疑）

`aicliActorChatExecutor.Execute`（chat_actor_executor.go:114-142）：

```go
actor, err := chatActorForSession(ctx, session)          // GetOrCreate，带 warmup 等待
...
releaseTurn, err := session.LocalRuntimeHost.acquireActorTurnGate(ctx, session.RuntimeSession.ID)
...
if err := waitForAICLIActorReady(ctx, actor); err != nil {   // ← 无限轮询
	return "", err
}
```

`waitForAICLIActorReady`（chat_actor_executor.go:72-88）：

```go
func waitForAICLIActorReady(ctx context.Context, actor *runtimechat.SessionActor) error {
	ticker := time.NewTicker(aicliActorReadyPollInterval)
	defer ticker.Stop()
	for {
		if state, ok := actor.StateSummary(); !ok || !state.Busy() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
```

- 该函数**没有超时**：只要 actor 保持 busy，就无限轮询，唯一退出路径是 ctx 取消（用户 Ctrl+C）。
- resume 场景下 `SessionHub.GetOrCreate(sessionID)` 可能拿到**携带上一进程遗留 busy 状态**的 actor（上一轮 turn 未收敛、executor 恢复 backoff 循环仍在跑、或 turn gate 被占），此时用户 prompt 卡在此处：**没有 LLM 请求、没有错误、UI 无提示**——与用户描述完全吻合。
- `acquireActorTurnGate` 同样可能阻塞等待上一轮残留的 turn gate 释放。

## 4. 架构问题定性

三层职责不清导致"resume 后输入无响应"，根因是**状态权威分裂 + 无界等待 + resume 收敛不完整**：

### 4.1 状态权威分裂（核心）

CLI 侧 `ChatSession`/`Interaction`（`IsReady()` 状态）与运行时侧 `SessionActor`（`StateSummary().Busy()`）是**两个独立的状态源**：

- `prepareInteractiveRead` 只检查 Interaction 与 team 状态；
- `waitForAICLIActorReady` 只检查 actor busy 状态；
- 二者之间没有统一的"当前是否可以接受用户输入"门控。

resume 后 CLI 认为会话就绪（能输入），但 actor 认为自身忙（不能收 prompt），矛盾在提交时才暴露——表现为无响应。

### 4.2 无界等待，无降级路径

三个等待点（`waitForTeamTerminal`、`acquireActorTurnGate`、`waitForAICLIActorReady`）都没有"等待超时 → 反馈用户 → 降级/排队"的路径。用户输入被吞后：

- 没有错误提示（不是 error，只是阻塞）；
- 没有排队（composer 模式下 `session.InputQueue = nil`，排队机制不可用）；
- 唯一出路是用户 Ctrl+C。

### 4.3 resume 收敛不完整

resume 恢复了历史消息（`loadResumeCanonicalHistory`）与 ChatSession，但**没有收敛 actor 的运行时状态**：

- 遗留 turn 未 cancel / 未标记完成；
- turn gate 未释放；
- 遗留 active team 未终止。

`GetOrCreate` 只是拿到 actor，不负责清理上一进程的遗留状态，导致"CLI 已恢复、actor 未恢复"的错位。

## 5. 修复建议（按优先级）

1. **`waitForAICLIActorReady` 增加超时与降级**：等待超时（建议 15-30s）后返回带诊断的错误，UI 明确提示"actor 仍忙，可 Ctrl+C 中断或等待"，而不是无限轮询；`aicliActorReadyPollInterval` 轮询期间同时输出诊断事件（busy 原因、generation、恢复状态）。
2. **resume 时收敛 actor 状态**：在 resume 路径显式 cancel/重置遗留 turn、释放 turn gate、终止孤儿 team（复用上一轮 postmortem 中的恢复义务模型，把"恢复"定义为 actor 侧的义务而非仅 CLI 侧），保证 `GetOrCreate` 后 actor 不 busy。
3. **统一输入门控**：`prepareInteractiveRead` 与 `sendMessage` 共享同一个"actor 就绪"检查；`Interaction.IsReady()` 与 `actor.StateSummary()` 对齐，未就绪时**明确提示**而非静默吞输入。
4. **`waitForTeamTerminal` 超时降级**：孤儿 team 等待超过阈值后放弃等待并进入输入循环（或提示用户 `/team` 清理），避免输入前无限阻塞。
5. **错误路径可见性**：所有拦截点失败时统一走 `renderChatTurnRecoveryHintForError` 风格的可见提示 + 日志（`--debug` 下输出 `[aicli-diag]` 标记），杜绝"无反馈吞输入"。

## 6. 验证方式（复现与回归）

- 复现：`aicli resume <id> --yolo --debug`，输入 prompt，观察是否出现 LLM 请求（`--debug` 下的请求日志 / pprof 端点 `/debug/pprof/goroutine` 确认 goroutine 卡在 `waitForAICLIActorReady` 轮询）。
- 回归：正常 resume 后首次 prompt 应在阈值内发出请求；期间按 Ctrl+C 可中断并回到输入循环。
- 单测：为 `waitForAICLIActorReady` 增加"busy 超时返回错误"用例；为 resume 收敛路径增加"遗留 busy actor 被复位"用例。
