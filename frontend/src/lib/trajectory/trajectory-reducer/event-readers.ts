// 由 lib/trajectory/trajectory-reducer.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TrajectoryEvent } from "../types";

export function makeTrajectoryEvent(
  kind: TrajectoryEvent["kind"],
  seq: number,
  payload: Record<string, unknown>,
): TrajectoryEvent {
  return { kind, seq, payload };
}

/** runtime 生命周期事件 → 单行说明（Q4：approval/compact/session 等人类可读摘要）。 */
export function describeRuntimeEvent(
  payload: Record<string, unknown>,
): string {
  const runtimeType = readString(payload["runtime_type"]);
  const toolName = readFirstString(payload, ["tool_name", "tool"]);
  const reason = readFirstString(payload, ["reason", "error", "message"]);
  const tokenBefore = readNumber(payload["token_before"]);
  const tokenAfter = readNumber(payload["token_after"]);
  const messageCountAfter = readNumber(payload["message_count_after"]);

  switch (runtimeType) {
    case "approval_requested":
      return toolName
        ? `approval requested: ${toolName}`
        : "approval requested";
    case "approval_resolved": {
      // 后端 approvalResolvedEventPayload 用 allowed 字段（actor.go:3126）；
      // approved 仅为兼容兜底（旧数据/测试夹具）。
      const approvedValue = payload["allowed"] ?? payload["approved"];
      const verb = approvedValue === false ? "rejected" : "approved";
      return toolName ? `approval ${verb}: ${toolName}` : `approval ${verb}`;
    }
    case "session_compact_started":
      return tokenBefore !== undefined
        ? `context compaction started: ${tokenBefore} tokens`
        : "context compaction started";
    case "session_compact_completed":
      if (tokenBefore !== undefined && tokenAfter !== undefined) {
        return `context compacted: ${tokenBefore} → ${tokenAfter} tokens`;
      }
      if (messageCountAfter !== undefined) {
        return `context compacted: ${messageCountAfter} messages`;
      }
      return "context compacted";
    case "session_compact_skipped":
      return reason
        ? `context compaction skipped: ${reason}`
        : "context compaction skipped";
    case "session_compact_failed":
      return reason
        ? `context compaction failed: ${reason}`
        : "context compaction failed";
    case "session_start":
      return "session started";
    case "session_end":
      return "session ended";
    case "session_interrupted":
      return "session interrupted";
    case "context_reconciled":
      return "context reconciled";
    case "checkpoint_created":
      return "checkpoint created";
    default:
      return runtimeType || "runtime event";
  }
}

/** 从 SSE payload 提取持久化 seq（_event.sequence）。 */
export function eventSeqOf(payload: Record<string, unknown>): number {
  const envelope = payload["_event"];
  if (envelope && typeof envelope === "object") {
    const sequence = (envelope as Record<string, unknown>)["sequence"];
    if (typeof sequence === "number" && Number.isFinite(sequence)) {
      return sequence;
    }
    if (typeof sequence === "string") {
      const parsed = Number(sequence);
      if (Number.isFinite(parsed)) {
        return parsed;
      }
    }
  }
  return 0;
}

export function readString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

export function readFirstString(
  container: Record<string, unknown> | undefined | null,
  keys: string[],
): string {
  if (!container || typeof container !== "object") {
    return "";
  }
  for (const key of keys) {
    const value = readString(container[key]);
    if (value.trim()) {
      return value;
    }
  }
  return "";
}

export function readNumber(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  return undefined;
}

/** 从 chunk 载荷中提取文本 delta（对齐 workspace-thread-state.getStreamTextDelta）。 */
export function textDeltaOf(payload: Record<string, unknown>): string {
  const type = readString(payload["type"]);
  if (type && type !== "text") {
    return "";
  }
  const content = readString(payload["content"]);
  if (content) {
    return content;
  }
  const text = payload["text"];
  if (text && typeof text === "object") {
    return readString((text as Record<string, unknown>)["content"]);
  }
  return "";
}

/** 从 chunk 载荷中提取工具调用 id（对齐 getToolCallId）。 */
export function toolCallIdOf(payload: Record<string, unknown>): string {
  const toolCall = payload["tool_call"];
  if (toolCall && typeof toolCall === "object") {
    const id = readFirstString(toolCall as Record<string, unknown>, [
      "id",
      "tool_call_id",
    ]);
    if (id) {
      return id;
    }
  }
  const tool = payload["tool"];
  if (tool && typeof tool === "object") {
    return readFirstString(tool as Record<string, unknown>, [
      "id",
      "tool_call_id",
    ]);
  }
  return "";
}

/** 从 chunk 载荷中提取工具名（对齐 getToolName）。 */
export function toolNameOf(payload: Record<string, unknown>): string {
  const tool = payload["tool"];
  if (tool && typeof tool === "object") {
    const name = readString((tool as Record<string, unknown>)["name"]);
    if (name) {
      return name;
    }
  }
  const toolCall = payload["tool_call"];
  if (toolCall && typeof toolCall === "object") {
    const name = readString((toolCall as Record<string, unknown>)["name"]);
    if (name) {
      return name;
    }
  }
  return "tool";
}

export function toolErrorOf(payload: Record<string, unknown>): string {
  const tool = payload["tool"];
  if (tool && typeof tool === "object") {
    const error = readFirstString(tool as Record<string, unknown>, [
      "error",
      "error_message",
      "message",
    ]);
    if (error) {
      return error;
    }
  }
  const result = payload["result"];
  if (result && typeof result === "object") {
    return readFirstString(result as Record<string, unknown>, [
      "error",
      "error_message",
      "message",
    ]);
  }
  return "";
}

export function toolArgsSummaryOf(payload: Record<string, unknown>): string {
  const tool = payload["tool"];
  if (tool && typeof tool === "object") {
    return readFirstString(tool as Record<string, unknown>, [
      "args_summary",
      "arguments_summary",
      "args",
      "arguments",
    ]);
  }
  const toolCall = payload["tool_call"];
  if (toolCall && typeof toolCall === "object") {
    return readFirstString(toolCall as Record<string, unknown>, [
      "args_summary",
      "arguments_summary",
      "args",
      "arguments",
    ]);
  }
  return "";
}

export function toolResultSummaryOf(payload: Record<string, unknown>): string {
  const tool = payload["tool"];
  if (tool && typeof tool === "object") {
    return readFirstString(tool as Record<string, unknown>, [
      "output_summary",
      "result_summary",
      "output",
      "result",
    ]);
  }
  return readFirstString(payload, ["output_summary", "result_summary"]);
}
