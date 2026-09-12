// 由 hooks/workspace/use-runtime-checkpoints.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type RuntimeSessionCheckpointFile,
  type RuntimeSessionCheckpointPreviewResult,
  type RuntimeSessionCheckpointSummary,
} from "@/lib/runtime-api";

export type UseRuntimeCheckpointsOptions = {
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  sessionId?: string;
};

export type ShouldReloadRuntimeCheckpointsOptions = {
  lastHandledEventKey?: string;
  lastRuntimeEventKey?: string;
  lastRuntimeEventType?: string;
  loadedCheckpointSessionId: string;
  sessionId?: string;
};

export type ShouldReloadBacktrackAuditOptions = {
  lastHandledEventKey?: string;
  lastRuntimeEventKey?: string;
  lastRuntimeEventType?: string;
  loadedAuditSessionId: string;
  sessionId?: string;
};

export type ResolveCheckpointDetailStateOptions = {
  checkpointFiles: RuntimeSessionCheckpointFile[];
  checkpointPreview?: RuntimeSessionCheckpointPreviewResult;
  selectedCheckpoint?: RuntimeSessionCheckpointSummary | null;
  selectedCheckpointFilePath: string | null;
};
