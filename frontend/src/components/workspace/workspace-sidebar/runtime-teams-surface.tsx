// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { lazy, Suspense } from "react";

import { type RuntimeTeamRecord, type RuntimeTeamSummaryEntry } from "@/lib/runtime-api";

const RuntimeTeamsDialog = lazy(() =>
  import("@/components/workspace/runtime-teams-dialog").then((module) => ({
    default: module.RuntimeTeamsDialog,
  })),
);

type WorkspaceSidebarRuntimeTeamsSurfaceProps = {
  error: string | null;
  isLoading: boolean;
  isRefreshing?: boolean;
  onClose: () => void;
  onRefresh?: () => void;
  open: boolean;
  summaries: RuntimeTeamSummaryEntry[];
  teams: RuntimeTeamRecord[];
};

export function WorkspaceSidebarRuntimeTeamsSurface({
  error,
  isLoading,
  isRefreshing,
  onClose,
  onRefresh,
  open,
  summaries,
  teams,
}: WorkspaceSidebarRuntimeTeamsSurfaceProps) {
  return (
    open ? (
      <Suspense fallback={<RuntimeTeamsDialogFallback />}>
        <RuntimeTeamsDialog
          error={error}
          isLoading={isLoading}
          isRefreshing={isRefreshing}
          onClose={onClose}
          onRefresh={onRefresh}
          open={open}
          summaries={summaries}
          teams={teams}
        />
      </Suspense>
    ) : null
  );
}

function RuntimeTeamsDialogFallback() {
  return (
    <div className="fixed inset-0 z-[120] flex items-center justify-center bg-dialog-backdrop px-3 py-4 backdrop-blur-sm">
      <div className="rounded-panel border border-border [background:var(--dialog-bg)] px-3.5 py-2.5 text-sm text-muted-foreground shadow-[0_12px_36px_rgba(0,0,0,0.22)]">
        Loading runtime teams panel...
      </div>
    </div>
  );
}
