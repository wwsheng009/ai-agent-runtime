// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type SessionRuntimeEvent } from "@/types/runtime";

export type RuntimeDeltaKind = "text" | "reasoning" | "image";

/**
 * Coordinates the two live delivery paths used by the workspace:
 * `/api/agent/chat` SSE and the session runtime stream. Both paths carry the
 * same provider delta, so claiming its stable stream identity before applying
 * it makes delivery order irrelevant.
 */
export type RuntimeDeltaCoordinator = {
  beginTurn: (turnId: string) => void;
  endTurn: (turnId?: string) => void;
  isTurnActive: (turnId?: string) => boolean;
  claim: (key: string) => boolean;
};

const MAX_RUNTIME_DELTA_KEYS = 512;

export function createRuntimeDeltaCoordinator(): RuntimeDeltaCoordinator {
  const seenKeys = new Set<string>();
  let activeTurnId = "";

  return {
    beginTurn(turnId: string) {
      activeTurnId = turnId.trim();
      seenKeys.clear();
    },
    endTurn(turnId?: string) {
      const normalized = turnId?.trim() ?? "";
      if (!normalized || normalized === activeTurnId) {
        activeTurnId = "";
      }
    },
    isTurnActive(turnId?: string) {
      if (!activeTurnId) {
        return false;
      }
      const normalized = turnId?.trim() ?? "";
      return !normalized || normalized === activeTurnId;
    },
    claim(key: string) {
      const normalized = key.trim();
      if (!normalized) {
        // Legacy providers may not expose identity. Callers can still
        // suppress those payloads while a direct request stream is active.
        return true;
      }
      if (seenKeys.has(normalized)) {
        return false;
      }
      seenKeys.add(normalized);
      while (seenKeys.size > MAX_RUNTIME_DELTA_KEYS) {
        const oldest = seenKeys.values().next().value as string | undefined;
        if (oldest === undefined) {
          break;
        }
        seenKeys.delete(oldest);
      }
      return true;
    },
  };
}

function readDeltaIdentityValue(
  payload: Record<string, unknown> | undefined,
  keys: string[],
): string {
  if (!payload) {
    return "";
  }
  for (const key of keys) {
    const value = payload[key];
    if (
      (typeof value === "string" || typeof value === "number") &&
      String(value).trim()
    ) {
      return String(value).trim();
    }
  }
  return "";
}

/**
 * Return the cross-channel identity for one text/reasoning/image delta.
 * EventStore `_event.sequence` is deliberately not used: it is assigned by
 * each transport independently, while provider stream_id + sequence is shared.
 */
export function getRuntimeDeltaKey(
  payload: Record<string, unknown> | undefined,
  kind: RuntimeDeltaKind,
): string {
  const streamId = readDeltaIdentityValue(payload, ["stream_id", "streamId"]);
  const sequence = readDeltaIdentityValue(payload, [
    "sequence",
    "stream_sequence",
    "streamSequence",
  ]);
  if (!streamId || !sequence) {
    return "";
  }
  const turnId = readDeltaIdentityValue(payload, ["turn_id", "turnId", "turn"]);
  return ["runtime-delta", turnId, streamId, kind, sequence].join("|");
}

export function getRuntimeDeltaKind(
  eventType: string,
): RuntimeDeltaKind | null {
  switch (eventType) {
    case "assistant_delta":
      return "text";
    case "assistant_reasoning":
    case "assistant.reasoning":
    case "assistant.reasoning_delta":
      return "reasoning";
    case "assistant.image_progress":
      return "image";
    default:
      return null;
  }
}

export function getRuntimeDeltaKeyFromEvent(
  event: SessionRuntimeEvent,
): string {
  const kind = getRuntimeDeltaKind(event.type);
  return kind ? getRuntimeDeltaKey(event.payload, kind) : "";
}
