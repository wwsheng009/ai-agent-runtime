// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import { statusTone, truncateIdentifier } from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";

import { detailsCardClass, detailsPanelClass, detailsPillClass } from "./format";
import { type TeamDetailsPanelProps } from "./types";

type TeamDetailsPanelRosterProps = Pick<
  TeamDetailsPanelProps,
  | "visibleTeammates"
>;

export function TeamDetailsPanelRoster({
  visibleTeammates,
}: TeamDetailsPanelRosterProps) {
  return (
    <div className={detailsPanelClass}>
      <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-muted-foreground">
        Teammate roster
      </div>
      <div className="mt-3 space-y-2">
        {visibleTeammates.length > 0 ? (
          visibleTeammates.map((teammate) => (
            <div key={teammate.id} className={detailsCardClass}>
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-sm font-semibold text-foreground">
                    {teammate.name || truncateIdentifier(teammate.id, 18)}
                  </div>
                  <div className="truncate text-xs text-muted-foreground">
                    {teammate.profile || teammate.id}
                  </div>
                </div>
                <span
                  className={cn(
                    detailsPillClass,
                    statusTone(teammate.state),
                  )}
                >
                  {teammate.state || "unknown"}
                </span>
              </div>
              {teammate.capabilities && teammate.capabilities.length > 0 ? (
                <div className="mt-2 flex flex-wrap gap-1.5">
                  {teammate.capabilities.slice(0, 3).map((capability) => (
                    <span
                      key={capability}
                      className="rounded-[0.65rem] bg-white/7 px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground"
                    >
                      {capability}
                    </span>
                  ))}
                </div>
              ) : null}
            </div>
          ))
        ) : (
          <div className="text-sm text-muted-foreground">
            No teammates registered.
          </div>
        )}
      </div>
    </div>

  );
}
