// 由 hooks/workspace/use-runtime-checkpoints.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type RuntimeSessionCheckpointFile,
  type RuntimeSessionCheckpointPreviewMode,
  type RuntimeSessionCheckpointPreviewResult,
  type RuntimeSessionCheckpointSummary,
} from "@/lib/runtime-api";

/** 还原执行结果（结构化）：文案由视图层按当前语言渲染。 */
export type CheckpointRestoreSummary = {
  checkpointId: string;
  mode: RuntimeSessionCheckpointPreviewMode;
  appliedPaths: number;
  conversationChanged: boolean;
};

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
