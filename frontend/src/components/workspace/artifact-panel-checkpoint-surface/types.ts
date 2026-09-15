// 由 components/workspace/artifact-panel-checkpoint-surface.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import type {
  RuntimeSessionBacktrackTombstone,
  RuntimeSessionCheckpointFile,
  RuntimeSessionCheckpointPreviewMode,
  RuntimeSessionCheckpointPreviewFile,
  RuntimeSessionCheckpointPreviewResult,
  RuntimeSessionCheckpointSummary,
} from "@/lib/runtime-api";
import type { CheckpointRestoreSummary } from "@/hooks/workspace/use-runtime-checkpoints";

export type CheckpointFileCode = {
  code: string;
  language: string;
  title: string;
};

export type ConversationSummaryMessage = {
  content: string;
  role: string;
};

export type CheckpointFileSelection =
  | RuntimeSessionCheckpointFile
  | RuntimeSessionCheckpointPreviewFile;

export type { CheckpointRestoreSummary };

export type ArtifactPanelCheckpointSurfaceProps = {
  backtrackAuditEntries?: RuntimeSessionBacktrackTombstone[];
  backtrackAuditError?: string | null;
  backtrackAuditLoading?: boolean;
  checkpointConversationSummary: ConversationSummaryMessage[];
  checkpointDetailsError: string | null;
  checkpointDetailsLoadingId: string;
  checkpointFileCode: CheckpointFileCode;
  checkpointFiles: RuntimeSessionCheckpointFile[];
  checkpointPreview?: RuntimeSessionCheckpointPreviewResult;
  checkpointPreviewFiles: RuntimeSessionCheckpointPreviewFile[];
  checkpointProvenance: string[];
  checkpointProvenanceSummary: string[];
  checkpointRestoreError?: string | null;
  checkpointRestoreSummary?: CheckpointRestoreSummary | null;
  checkpointRestorePendingId?: string;
  checkpoints: RuntimeSessionCheckpointSummary[];
  checkpointsError: string | null;
  checkpointsLoading: boolean;
  onRestoreCheckpoint?: (mode?: RuntimeSessionCheckpointPreviewMode) => void;
  onSelectCheckpoint: (checkpointId: string) => void;
  onSelectCheckpointFile: (filePath: string) => void;
  selectedCheckpoint: RuntimeSessionCheckpointSummary | null;
  selectedCheckpointFilePath: string | null;
  selectedCheckpointId: string | null;
  sessionId?: string;
};
