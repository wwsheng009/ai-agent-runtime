// 由 hooks/workspace/use-runtime-checkpoints.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type ShouldReloadBacktrackAuditOptions,
  type ShouldReloadRuntimeCheckpointsOptions,
} from "./types";

export function buildRuntimeEventReloadKey(
  lastRuntimeEventType?: string,
  runtimeEventCount?: number,
) {
  if (!lastRuntimeEventType) {
    return "";
  }

  return `${lastRuntimeEventType}:${runtimeEventCount ?? 0}`;
}

export function shouldReloadRuntimeCheckpoints({
  lastHandledEventKey,
  lastRuntimeEventKey,
  lastRuntimeEventType,
  loadedCheckpointSessionId,
  sessionId,
}: ShouldReloadRuntimeCheckpointsOptions) {
  if (!sessionId) {
    return false;
  }

  if (loadedCheckpointSessionId !== sessionId) {
    return true;
  }

  if (
    lastRuntimeEventType !== "checkpoint_created" &&
    lastRuntimeEventType !== "backtrack_finished" &&
    lastRuntimeEventType !== "rewind_finished"
  ) {
    return false;
  }

  if (!lastRuntimeEventKey) {
    return false;
  }

  return lastRuntimeEventKey !== lastHandledEventKey;
}

export function shouldReloadBacktrackAudit({
  lastHandledEventKey,
  lastRuntimeEventKey,
  lastRuntimeEventType,
  loadedAuditSessionId,
  sessionId,
}: ShouldReloadBacktrackAuditOptions) {
  if (!sessionId) {
    return false;
  }

  if (loadedAuditSessionId !== sessionId) {
    return true;
  }

  if (
    lastRuntimeEventType !== "backtrack_finished" &&
    lastRuntimeEventType !== "rewind_finished"
  ) {
    return false;
  }

  if (!lastRuntimeEventKey) {
    return false;
  }

  return lastRuntimeEventKey !== lastHandledEventKey;
}
