// 由 components/workspace/artifact-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense, useId, useState } from "react";
import { useTranslation } from "react-i18next";

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
import { SessionUsagePanel } from "@/components/workspace/session-usage-panel";
import { useRuntimeCheckpoints } from "@/hooks/workspace/use-runtime-checkpoints";
import { useRuntimePlanMode } from "@/hooks/workspace/use-runtime-plan-mode";
import {
  classifyArtifactCategory,
  formatArtifactCategory,
} from "@/lib/workspace-artifacts";

export function ArtifactPanel({
  artifacts,
  isResponding = false,
  lastRuntimeEventType,
  runtimeEventCount,
  onOpenArtifact,
  selectedArtifactId,
  sessionId,
}: ArtifactPanelProps) {
  const { t } = useTranslation("workspace");
  const asideTitleId = useId();
  const asideDescriptionId = useId();
  const artifactSurfaceTabId = useId();
  const planSurfaceTabId = useId();
  const checkpointSurfaceTabId = useId();
  const artifactSurfacePanelId = useId();
  const planSurfacePanelId = useId();
  const checkpointSurfacePanelId = useId();
  const usageSurfaceTabId = useId();
  const usageSurfacePanelId = useId();
  const [activeSurface, setActiveSurface] = useState<ArtifactPanelSurface>(
    artifacts.length > 0 ? "artifacts" : "plan",
  );
  // 用户显式点选页签后不再被自动回落覆盖，保证「会话用量」等页签可稳定停留。
  const [surfacePinnedByUser, setSurfacePinnedByUser] = useState(false);
  const selectSurface = (surface: ArtifactPanelSurface) => {
    setSurfacePinnedByUser(true);
    setActiveSurface(surface);
  };

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
    checkpointRestoreSummary,
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

  const resolvedActiveSurface = surfacePinnedByUser
    ? activeSurface
    : artifacts.length === 0 && plan?.active
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
  const surfaceTabDisabledStates = [false, !sessionId, !sessionId, !sessionId];
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
    usagePanelId: usageSurfacePanelId,
    usageTabId: usageSurfaceTabId,
  };

  return (
    <aside
      aria-describedby={asideDescriptionId}
      aria-labelledby={asideTitleId}
      className="flex min-h-0 flex-1 flex-col overflow-hidden [background:var(--workspace-sidebar-bg)]"
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
        {t("panels.artifacts.panel.description")}
      </div>

    <ArtifactPanelSurfaceTabs
      activeSurface={resolvedActiveSurface}
      artifactCount={artifacts.length}
      backtrackCount={backtrackAuditEntries.length}
      onSelectSurface={selectSurface}
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
              checkpointRestoreSummary={checkpointRestoreSummary}
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
      <div
        aria-labelledby={usageSurfaceTabId}
        className="min-h-0 flex-1 overflow-y-auto"
        hidden={resolvedActiveSurface !== "usage"}
        id={usageSurfacePanelId}
        role="tabpanel"
      >
        {resolvedActiveSurface === "usage" && sessionId ? (
          <SessionUsagePanel
            key={sessionId}
            className="border-b-0"
            isResponding={isResponding}
            lastRuntimeEventType={lastRuntimeEventType}
            runtimeEventCount={runtimeEventCount}
            sessionId={sessionId}
          />
        ) : null}
      </div>
    </aside>
  );
}
