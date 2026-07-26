// Package toolprotocol defines the stable wire contract for tool identity,
// capability taxonomy, execution results, errors, and progress notifications.
//
// Design goals (Iteration C1):
//   - toolkit implements tools; toolbroker routes session/policy tools
//   - agent/SSE/CLI consume a single progress + result shape
//   - existing toolkit.ToolResult / toolresult.Diagnostic stay authoritative
//     for execution; this package is the portable wire view + progress channel
//
// Progress channel (live-only):
//   - Event type: EventTypeProgress = "tool.progress"
//   - Tools opt in via Report(ctx, Progress{...}) when a Reporter is bound
//   - Agent binds Reporter on sequential / parallel / approved tool paths
//   - Progress is NOT persisted to the session event store by default
//     (high frequency); ListRuntimeEvents (bus query) and aicli live bridge see it
//   - Session SSE (StreamSessionRuntimeEvents) defaults to durable store only;
//     opt-in live bus fan-out via ?live=1 (or include_live_progress=1) forwards
//     tool.progress with live=true without persisting it
//   - tool.completed payloads may include nested protocol_result (EventMap)
//
// Non-goals for C1:
//   - full marketplace / plugin packaging (C2)
//   - ACP stdio host (C3)
//   - doom-loop / tool search productization (C4)
package toolprotocol
