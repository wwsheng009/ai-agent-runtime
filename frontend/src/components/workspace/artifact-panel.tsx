// 右侧栏面板宿主（PanelHost）：页签栏 + 面分发。
//
// 改造（P0-1）：页签由 `components/workspace/panel-registry.ts` 驱动，面分发改为
// 「注册表声明 → 自包含面懒加载 / 宿主自渲染面」；既有 4 个面的行为、a11y 关联与
// data-testid 保持不变，新增 files / git 两个自包含面。

import { Suspense, useId, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { ArtifactPanelArtifactSurface } from "@/components/workspace/artifact-panel/artifact-list";
import {
  ArtifactPanelCheckpointFallback,
  ArtifactPanelCheckpointSurface,
  ArtifactPanelPlanFallback,
  ArtifactPanelPlanSurface,
} from "@/components/workspace/artifact-panel/lazy-surfaces";
import { SelfContainedSurfacePanel } from "@/components/workspace/artifact-panel/surface-mount";
import {
  ArtifactPanelSurfaceTabs,
} from "@/components/workspace/artifact-panel/surface-tabs";
import { surfaceBadgeClass } from "@/components/workspace/artifact-panel/surface-tone";
import {
  type ArtifactPanelProps,
  type ArtifactPanelSurface,
} from "@/components/workspace/artifact-panel/types";
import {
  WORKSPACE_PANEL_SURFACES,
  buildSurfaceTabIds,
  type WorkspacePanelSurfaceId,
} from "@/components/workspace/panel-registry";
import { SessionUsagePanel } from "@/components/workspace/session-usage-panel";
import { Badge } from "@/components/ui/badge";
import { useRuntimeCheckpoints } from "@/hooks/workspace/use-runtime-checkpoints";
import { useRuntimePlanMode } from "@/hooks/workspace/use-runtime-plan-mode";
import {
  classifyArtifactCategory,
  formatArtifactCategory,
} from "@/lib/workspace-artifacts";

export function ArtifactPanel({
  activeSurface: controlledSurface,
  artifacts,
  isResponding = false,
  lastRuntimeEventType,
  onActiveSurfaceChange,
  onOpenArtifact,
  runtimeEventCount,
  selectedArtifactId,
  sessionId,
  workspacePath,
}: ArtifactPanelProps) {
  const { t } = useTranslation("workspace");
  const asideTitleId = useId();
  const asideDescriptionId = useId();
  const tabIdBase = useId();
  const [uncontrolledSurface, setUncontrolledSurface] =
    useState<ArtifactPanelSurface>(artifacts.length > 0 ? "artifacts" : "plan");
  // 用户显式点选页签后不再被自动回落覆盖，保证「会话用量」等页签可稳定停留。
  const [surfacePinnedByUser, setSurfacePinnedByUser] = useState(false);
  const selectSurface = (surface: ArtifactPanelSurface) => {
    setSurfacePinnedByUser(true);
    setUncontrolledSurface(surface);
    onActiveSurfaceChange?.(surface);
  };
  const activeSurface = controlledSurface ?? uncontrolledSurface;

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
  const artifactSelectionAnnouncement = selectedArtifact
    ? `${formatArtifactCategory(selectedArtifactCategory ?? "file")} selected: ${
        selectedArtifact.name
      }. Opens in dialog.`
    : "Artifact rail ready. Select an item to open it in a dialog.";

  const surfaceTabIds = useMemo(
    () =>
      Object.fromEntries(
        WORKSPACE_PANEL_SURFACES.map((spec) => [
          spec.id,
          buildSurfaceTabIds(tabIdBase, spec.id),
        ]),
      ) as Record<WorkspacePanelSurfaceId, { panelId: string; tabId: string }>,
    [tabIdBase],
  );

  // 会话依赖面（计划/还原/用量）在无会话时给出可解释的禁用原因，不白屏。
  const surfaceDisabledReasons = useMemo(() => {
    const reasons: Partial<Record<WorkspacePanelSurfaceId, string>> = {};
    for (const spec of WORKSPACE_PANEL_SURFACES) {
      if (spec.requiresSession && !sessionId) {
        // 注册表以 string 下发键名，这里按仓库惯例收窄到 i18n 键类型。
        reasons[spec.id] = t(
          (spec.disabledReasonKey ?? "panels.shell.panelTabs.disabledNoSession") as never,
        ) as string;
      }
    }
    return reasons;
  }, [sessionId, t]);

  const surfaceBadges: Partial<Record<WorkspacePanelSurfaceId, ReactNode>> = {
    plan: plan?.active ? (
      <span className={surfaceBadgeClass("plan")}>
        {t("panels.artifacts.tabs.planLive")}
      </span>
    ) : null,
    checkpoints:
      backtrackAuditEntries.length > 0 ? (
        <span className={surfaceBadgeClass("checkpoint")}>
          {backtrackAuditEntries.length}
        </span>
      ) : null,
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
        badges={surfaceBadges}
        disabledReasons={surfaceDisabledReasons}
        tabIds={surfaceTabIds}
        titleId={asideTitleId}
        trailingBadge={<Badge>{artifacts.length}</Badge>}
        onSelectSurface={selectSurface}
      />

      <div
        aria-labelledby={surfaceTabIds.artifacts.tabId}
        className="min-h-0 flex-1"
        hidden={resolvedActiveSurface !== "artifacts"}
        id={surfaceTabIds.artifacts.panelId}
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
        aria-labelledby={surfaceTabIds.plan.tabId}
        className="min-h-0 flex-1"
        hidden={resolvedActiveSurface !== "plan"}
        id={surfaceTabIds.plan.panelId}
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
        aria-labelledby={surfaceTabIds.checkpoints.tabId}
        className="min-h-0 flex-1"
        hidden={resolvedActiveSurface !== "checkpoints"}
        id={surfaceTabIds.checkpoints.panelId}
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
        aria-labelledby={surfaceTabIds.usage.tabId}
        className="min-h-0 flex-1 overflow-y-auto"
        hidden={resolvedActiveSurface !== "usage"}
        id={surfaceTabIds.usage.panelId}
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
      {WORKSPACE_PANEL_SURFACES.filter((spec) => spec.surface).map((spec) => (
        <SelfContainedSurfacePanel
          key={spec.id}
          active={resolvedActiveSurface === spec.id}
          sessionId={sessionId?.trim() ?? ""}
          spec={spec}
          tabIds={surfaceTabIds[spec.id]}
          workspacePath={workspacePath}
        />
      ))}
    </aside>
  );
}
