// 由 components/workspace/artifact-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { lazy } from "react";
import { useTranslation } from "react-i18next";

export const ArtifactPanelCheckpointSurface = lazy(() =>
  import("@/components/workspace/artifact-panel-checkpoint-surface").then(
    (module) => ({
      default: module.ArtifactPanelCheckpointSurface,
    }),
  ),
);

export const ArtifactPanelPlanSurface = lazy(() =>
  import("@/components/workspace/artifact-panel-plan-surface").then((module) => ({
    default: module.ArtifactPanelPlanSurface,
  })),
);

export function ArtifactPanelCheckpointFallback() {
  const { t } = useTranslation("workspace");

  return (
    <div className="grid min-h-0 flex-1 gap-3 overflow-auto p-3">
      <div className="flex min-h-[14rem] items-center justify-center rounded-panel border border-white/8 bg-white/[0.035] px-3.5 py-2.5 text-sm text-muted-foreground">
        {t("panels.artifacts.lazySurfaces.loadingRestorePoints")}
      </div>
    </div>
  );
}

export function ArtifactPanelPlanFallback() {
  const { t } = useTranslation("workspace");

  return (
    <div className="grid min-h-0 flex-1 gap-3 overflow-auto p-3">
      <div className="flex min-h-[14rem] items-center justify-center rounded-panel border border-white/8 bg-white/[0.035] px-3.5 py-2.5 text-sm text-muted-foreground">
        {t("panels.artifacts.lazySurfaces.loadingPlanPreview")}
      </div>
    </div>
  );
}
