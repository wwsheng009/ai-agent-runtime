// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { lazy } from "react";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("workspace");

  return (
    <div className="rounded-panel-lg border border-border bg-surface-softer px-4 py-3 text-sm text-muted-foreground">
      {t("panels.teamsPanels.fallback.loading", { label })}
    </div>
  );
}
