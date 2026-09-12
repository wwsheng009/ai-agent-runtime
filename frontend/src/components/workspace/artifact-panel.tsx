// 由 components/workspace/artifact-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense, useId, useState } from "react";

import { ArtifactPanelArtifactSurface } from "@/components/workspace/artifact-panel/artifact-list";
import {
  ArtifactPanelCheckpointFallback,
  ArtifactPanelCheckpointSurface,
  ArtifactPanelPlanFallback,
  ArtifactPanelPlanSurface,
} from "@/components/workspace/artifact-panel/lazy-surfaces";
import { ArtifactPanelSurfaceTabs } from "@/components/workspace/artifact-panel/surface-tabs";
import {
  type ArtifactPanelProps,
  type ArtifactPanelSurface,
  type ArtifactPanelSurfaceTabIds,
} from "@/components/workspace/artifact-panel/types";
import { useRuntimeCheckpoints } from "@/hooks/workspace/use-runtime-checkpoints";
import { useRuntimePlanMode } from "@/hooks/workspace/use-runtime-plan-mode";
import {
  classifyArtifactCategory,
  formatArtifactCategory,
} from "@/lib/workspace-artifacts";

export function ArtifactPanel({
  artifacts,
  lastRuntimeEventType,
  runtimeEventCount,
  onOpenArtifact,
  selectedArtifactId,
  sessionId,
}: ArtifactPanelProps) {
  const asideTitleId = useId();
  const asideDescriptionId = useId();
  const artifactSurfaceTabId = useId();
  const planSurfaceTabId = useId();
  const checkpointSurfaceTabId = useId();
  const artifactSurfacePanelId = useId();
  const planSurfacePanelId = useId();
  const checkpointSurfacePanelId = useId();
  const [activeSurface, setActiveSurface] = useState<ArtifactPanelSurface>(
    artifacts.length > 0 ? "artifacts" : "plan",
  );

  const {
    backtrackAuditEntries,
    backtrackAuditError,
    backtrackAuditLoading,
    checkpointConversationSummary,
    checkpointDetailsError,
    checkpointDetailsLoadingId,
    checkpointFileCode,
    checkpointFiles,
    checkpointPreview,
    checkpointPreviewFiles,
    checkpointProvenance,
    checkpointProvenanceSummary,
    checkpointRestoreError,
    checkpointRestoreNotice,
    checkpointRestorePendingId,
    checkpoints,
    checkpointsError,
    checkpointsLoading,
    onRestoreCheckpoint,
    onSelectCheckpoint,
    onSelectCheckpointFile,
    selectedCheckpoint,
    selectedCheckpointFilePath,
    selectedCheckpointId,
  } = useRuntimeCheckpoints({
    lastRuntimeEventType,
    runtimeEventCount,
    sessionId,
  });

  const {
    canSubmitDecision,
    notesDraft,
    onNotesDraftChange,
    plan,
    planActionPending,
    planError,
    planLoading,
    planStatusLabel,
    reloadPlan,
    submitDecision,
  } = useRuntimePlanMode({
    lastRuntimeEventType,
    runtimeEventCount,
    sessionId,
  });

  const resolvedActiveSurface =
    artifacts.length === 0 && plan?.active
      ? "plan"
      : artifacts.length === 0 && checkpoints.length > 0 && !plan?.active
        ? "checkpoints"
        : activeSurface;
  const evidenceArtifacts = artifacts.filter(
    (artifact) => classifyArtifactCategory(artifact) === "evidence",
  );
  const outputArtifacts = artifacts.filter(
    (artifact) => classifyArtifactCategory(artifact) === "file",
  );
  const orderedArtifacts = [...evidenceArtifacts, ...outputArtifacts];
  const selectedArtifact =
    artifacts.find((artifact) => artifact.id === selectedArtifactId) ?? null;
  const selectedArtifactCategory = selectedArtifact
    ? classifyArtifactCategory(selectedArtifact)
    : null;
  const surfaceTabDisabledStates = [false, !sessionId, !sessionId];
  const artifactSelectionAnnouncement = selectedArtifact
    ? `${formatArtifactCategory(selectedArtifactCategory ?? "file")} selected: ${
        selectedArtifact.name
      }. Opens in dialog.`
    : "Artifact rail ready. Select an item to open it in a dialog.";

  const artifactPanelTabIds: ArtifactPanelSurfaceTabIds = {
    artifactPanelId: artifactSurfacePanelId,
    artifactTabId: artifactSurfaceTabId,
    checkpointPanelId: checkpointSurfacePanelId,
    checkpointTabId: checkpointSurfaceTabId,
    planPanelId: planSurfacePanelId,
    planTabId: planSurfaceTabId,
  };

  return (
    <aside
      aria-describedby={asideDescriptionId}
      aria-labelledby={asideTitleId}
      className="hidden h-full min-h-0 flex-col overflow-hidden border-l border-white/8 [background:var(--workspace-sidebar-bg)] xl:flex"
    >
      <div
        key={selectedArtifactId ?? "none"}
        aria-atomic="true"
        aria-live="polite"
        className="sr-only"
        role="status"
      >
        {artifactSelectionAnnouncement}
      </div>
      <div className="sr-only" id={asideDescriptionId}>
        Workspace artifacts, plan preview, restore points, and backtrack audit for
        the current thread.
      </div>

    <ArtifactPanelSurfaceTabs
      activeSurface={resolvedActiveSurface}
      artifactCount={artifacts.length}
      backtrackCount={backtrackAuditEntries.length}
      onSelectSurface={setActiveSurface}
      planIsActive={Boolean(plan?.active)}
      sessionId={sessionId}
      surfaceTabDisabledStates={surfaceTabDisabledStates}
      tabIds={artifactPanelTabIds}
      titleId={asideTitleId}
    />

      <div
        aria-labelledby={artifactSurfaceTabId}
        className="min-h-0 flex-1"
        hidden={resolvedActiveSurface !== "artifacts"}
        id={artifactSurfacePanelId}
        role="tabpanel"
      >
        <ArtifactPanelArtifactSurface
          artifacts={artifacts}
          orderedArtifacts={orderedArtifacts}
          onOpenArtifact={onOpenArtifact}
          selectedArtifactId={selectedArtifactId}
        />
      </div>
      <div
        aria-labelledby={planSurfaceTabId}
        className="min-h-0 flex-1"
        hidden={resolvedActiveSurface !== "plan"}
        id={planSurfacePanelId}
        role="tabpanel"
      >
        {resolvedActiveSurface === "plan" ? (
          <Suspense fallback={<ArtifactPanelPlanFallback />}>
            <ArtifactPanelPlanSurface
              canSubmitDecision={canSubmitDecision}
              notesDraft={notesDraft}
              onNotesDraftChange={onNotesDraftChange}
              onReload={() => {
                void reloadPlan();
              }}
              onSubmitDecision={(decision) => {
                void submitDecision(decision);
              }}
              plan={plan}
              planActionPending={planActionPending}
              planError={planError}
              planLoading={planLoading}
              planStatusLabel={planStatusLabel}
              sessionId={sessionId}
            />
          </Suspense>
        ) : null}
      </div>
      <div
        aria-labelledby={checkpointSurfaceTabId}
        className="min-h-0 flex-1"
        hidden={resolvedActiveSurface !== "checkpoints"}
        id={checkpointSurfacePanelId}
        role="tabpanel"
      >
        {resolvedActiveSurface === "checkpoints" ? (
          <Suspense fallback={<ArtifactPanelCheckpointFallback />}>
            <ArtifactPanelCheckpointSurface
              backtrackAuditEntries={backtrackAuditEntries}
              backtrackAuditError={backtrackAuditError}
              backtrackAuditLoading={backtrackAuditLoading}
              checkpointConversationSummary={checkpointConversationSummary}
              checkpointDetailsError={checkpointDetailsError}
              checkpointDetailsLoadingId={checkpointDetailsLoadingId}
              checkpointFileCode={checkpointFileCode}
              checkpointFiles={checkpointFiles}
              checkpointPreview={checkpointPreview}
              checkpointPreviewFiles={checkpointPreviewFiles}
              checkpointProvenance={checkpointProvenance}
              checkpointProvenanceSummary={checkpointProvenanceSummary}
              checkpointRestoreError={checkpointRestoreError}
              checkpointRestoreNotice={checkpointRestoreNotice}
              checkpointRestorePendingId={checkpointRestorePendingId}
              checkpoints={checkpoints}
              checkpointsError={checkpointsError}
              checkpointsLoading={checkpointsLoading}
              onRestoreCheckpoint={onRestoreCheckpoint}
              onSelectCheckpoint={onSelectCheckpoint}
              onSelectCheckpointFile={onSelectCheckpointFile}
              selectedCheckpoint={selectedCheckpoint}
              selectedCheckpointFilePath={selectedCheckpointFilePath}
              selectedCheckpointId={selectedCheckpointId}
              sessionId={sessionId}
            />
          </Suspense>
        ) : null}
      </div>
    </aside>
  );
}
