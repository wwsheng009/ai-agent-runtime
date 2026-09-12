// 由 components/workspace/artifact-panel-checkpoint-surface.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect, useState } from "react";

import {
  isCheckpointDetailLoading,
  pickInitialBacktrackAuditId,
  resolveCheckpointFileEntries,
} from "@/components/workspace/artifact-panel-shared";

import { ArtifactPanelBacktrackAuditSection } from "./artifact-panel-checkpoint-surface/backtrack-audit-section";
import { ArtifactPanelCheckpointDetailSection } from "./artifact-panel-checkpoint-surface/checkpoint-detail-section";
import { ArtifactPanelCheckpointsSection } from "./artifact-panel-checkpoint-surface/checkpoints-section";
import {
  type ArtifactPanelCheckpointSurfaceProps,
} from "./artifact-panel-checkpoint-surface/types";

export function ArtifactPanelCheckpointSurface({
  backtrackAuditEntries = [],
  backtrackAuditError = null,
  backtrackAuditLoading = false,
  checkpointConversationSummary,
  checkpointDetailsError,
  checkpointDetailsLoadingId,
  checkpointFileCode,
  checkpointFiles,
  checkpointPreview,
  checkpointPreviewFiles,
  checkpointProvenance,
  checkpointProvenanceSummary,
  checkpointRestoreError = null,
  checkpointRestoreNotice = null,
  checkpointRestorePendingId = "",
  checkpoints,
  checkpointsError,
  checkpointsLoading,
  onRestoreCheckpoint,
  onSelectCheckpoint,
  onSelectCheckpointFile,
  selectedCheckpoint,
  selectedCheckpointFilePath,
  selectedCheckpointId,
  sessionId,
}: ArtifactPanelCheckpointSurfaceProps) {
  const [selectedAuditId, setSelectedAuditId] = useState<string | null>(null);

  useEffect(() => {
    // P0-2 机械搬迁：保留原「审计条目变化即同步校正当前选中项」语义。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSelectedAuditId((current) =>
      pickInitialBacktrackAuditId(backtrackAuditEntries, current),
    );
  }, [backtrackAuditEntries]);


  const checkpointFilesForSelection = resolveCheckpointFileEntries(
    checkpointPreviewFiles,
    checkpointFiles,
  );
  const checkpointDetailLoading = isCheckpointDetailLoading(
    selectedCheckpoint,
    checkpointDetailsLoadingId,
  );

  return (
    <div className="grid min-h-0 flex-1 gap-2.5 overflow-auto p-2.5">
      <ArtifactPanelBacktrackAuditSection
        backtrackAuditEntries={backtrackAuditEntries}
        backtrackAuditError={backtrackAuditError}
        backtrackAuditLoading={backtrackAuditLoading}
        onSelectAuditId={setSelectedAuditId}
        selectedAuditId={selectedAuditId}
        sessionId={sessionId}
      />

      <ArtifactPanelCheckpointsSection
        checkpoints={checkpoints}
        checkpointsError={checkpointsError}
        checkpointsLoading={checkpointsLoading}
        onSelectCheckpoint={onSelectCheckpoint}
        selectedCheckpointId={selectedCheckpointId}
        sessionId={sessionId}
      />

      <ArtifactPanelCheckpointDetailSection
        checkpointConversationSummary={checkpointConversationSummary}
        checkpointDetailLoading={checkpointDetailLoading}
        checkpointDetailsError={checkpointDetailsError}
        checkpointFileCode={checkpointFileCode}
        checkpointFilesForSelection={checkpointFilesForSelection}
        checkpointPreview={checkpointPreview}
        checkpointProvenance={checkpointProvenance}
        checkpointProvenanceSummary={checkpointProvenanceSummary}
        checkpointRestoreError={checkpointRestoreError}
        checkpointRestoreNotice={checkpointRestoreNotice}
        checkpointRestorePendingId={checkpointRestorePendingId}
        onRestoreCheckpoint={onRestoreCheckpoint}
        onSelectCheckpointFile={onSelectCheckpointFile}
        selectedCheckpoint={selectedCheckpoint}
        selectedCheckpointFilePath={selectedCheckpointFilePath}
      />
    </div>
  );
}
