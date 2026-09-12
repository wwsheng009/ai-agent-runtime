// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { lazy } from "react";

export const RuntimeTeamDetailsPanel = lazy(() =>
  import("@/components/workspace/runtime-teams/runtime-team-details-panel").then(
    (module) => ({
      default: module.RuntimeTeamDetailsPanel,
    }),
  ),
);
export const RuntimeTeamDispatchPanel = lazy(() =>
  import("@/components/workspace/runtime-teams/runtime-team-dispatch-panel").then(
    (module) => ({
      default: module.RuntimeTeamDispatchPanel,
    }),
  ),
);

export function RuntimeTeamsPanelFallback({ label }: { label: string }) {
  return (
    <div className="rounded-[0.95rem] border border-[var(--border)] bg-[var(--surface-softer)] px-4 py-3 text-sm text-[var(--muted-foreground)]">
      正在加载 {label}…
    </div>
  );
}
