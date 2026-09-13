// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { lazy } from "react";

export const SettingsDialog = lazy(() =>
  import("@/components/workspace/settings/settings-dialog").then((module) => ({
    default: module.SettingsDialog,
  })),
);
export const ArtifactDetailDialog = lazy(() =>
  import("@/components/workspace/artifact-detail-dialog").then((module) => ({
    default: module.ArtifactDetailDialog,
  })),
);
export const ArtifactPanel = lazy(() =>
  import("@/components/workspace/artifact-panel").then((module) => ({
    default: module.ArtifactPanel,
  })),
);
export const TrajectoryView = lazy(() =>
  import("@/components/workspace/trajectory/trajectory-view").then((module) => ({
    default: module.TrajectoryView,
  })),
);

export function SettingsDialogFallback({ message }: { message: string }) {
  return (
    <div className="fixed inset-0 z-[120] flex items-center justify-center bg-dialog-backdrop px-3 py-4 backdrop-blur-sm">
      <div className="rounded-[0.9rem] border border-border [background:var(--dialog-bg)] px-3.5 py-2.5 text-sm text-muted-foreground shadow-[0_12px_36px_rgba(0,0,0,0.22)]">
        {message}
      </div>
    </div>
  );
}

export function ArtifactPanelFallback({ message }: { message: string }) {
  return (
    <aside className="hidden h-full min-h-0 flex-col overflow-hidden border-l border-white/8 [background:var(--workspace-sidebar-bg)] xl:flex">
      <div className="flex h-full items-center justify-center px-4 text-sm text-muted-foreground">
        {message}
      </div>
    </aside>
  );
}

export function ArtifactDialogFallback({ message }: { message: string }) {
  return (
    <div className="fixed inset-0 z-[130] flex items-center justify-center bg-dialog-backdrop px-3 py-4 backdrop-blur-sm">
      <div className="rounded-[0.9rem] border border-border [background:var(--dialog-bg)] px-3.5 py-2.5 text-sm text-muted-foreground shadow-[0_12px_36px_rgba(0,0,0,0.22)]">
        {message}
      </div>
    </div>
  );
}
