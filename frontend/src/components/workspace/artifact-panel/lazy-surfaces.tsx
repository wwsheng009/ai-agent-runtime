// 由 components/workspace/artifact-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { lazy } from "react";

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
  return (
    <div className="grid min-h-0 flex-1 gap-3 overflow-auto p-3">
      <div className="flex min-h-[14rem] items-center justify-center rounded-panel border border-white/8 bg-white/[0.035] px-3.5 py-2.5 text-sm text-muted-foreground">
        正在加载 restore points…
      </div>
    </div>
  );
}

export function ArtifactPanelPlanFallback() {
  return (
    <div className="grid min-h-0 flex-1 gap-3 overflow-auto p-3">
      <div className="flex min-h-[14rem] items-center justify-center rounded-panel border border-white/8 bg-white/[0.035] px-3.5 py-2.5 text-sm text-muted-foreground">
        正在加载 plan preview…
      </div>
    </div>
  );
}
