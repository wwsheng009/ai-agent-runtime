import {
  RuntimeApiError,
  getSessionLeaseConflictTitle,
} from "@/api/runtime/shared";
import type { AgentChatStreamChunkPayload } from "@/types/runtime";
import type { ChatTurnRuntimeState } from "./turn-state";

export type FrameScheduler = {
  flush: () => void;
  schedule: () => void;
};

/**
 * 租约冲突的 SSE error 帧 → 与 CLI 路径完全一致的短标题。文案单一真源在
 * api/runtime/shared，这里只把帧内的 lease 上下文投影成它认识的错误体。
 */
export function getFrameLeaseConflictTitle(
  payload: Record<string, unknown>,
): string | undefined {
  if (payload.error_type !== "session_lease_conflict") {
    return undefined;
  }
  const lease =
    asRecord(payload.lease) ?? asRecord(asRecord(payload.context)?.lease);
  const ownerKind =
    typeof lease?.owner_kind === "string" ? lease.owner_kind : "";
  return getSessionLeaseConflictTitle(
    new RuntimeApiError(409, {
      code: "SESSION_LEASE_CONFLICT",
      context: { lease: { owner_kind: ownerKind } },
    }),
  );
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object"
    ? (value as Record<string, unknown>)
    : null;
}

export function chatStreamIDOf(
  payload: AgentChatStreamChunkPayload,
): string {
  return typeof payload.stream_id === "string" ? payload.stream_id.trim() : "";
}

export function ensureTextStreamStart(
  turnState: ChatTurnRuntimeState,
  streamID: string,
) {
  if (!streamID || streamID in turnState.streamTextStart) {
    return;
  }
  turnState.streamTextStart[streamID] = turnState.streamedText.length;
}

export function ensureReasoningStreamStart(
  turnState: ChatTurnRuntimeState,
  streamID: string,
) {
  if (!streamID || streamID in turnState.streamReasoningStart) {
    return;
  }
  turnState.streamReasoningStart[streamID] = {
    text: turnState.reasoningText.length,
    block: turnState.reasoningBlocks.length,
  };
}
