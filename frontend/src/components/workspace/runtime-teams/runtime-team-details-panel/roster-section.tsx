import { Badge } from "@/components/ui/badge";
import {
  statusTone,
  type TeamDetailsState,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";

import {
  detailCardClass,
  detailMetaPillClass,
  detailStatusPillClass,
} from "./format";
import { TeamDetailsSection } from "./primitives";

type RuntimeTeamRosterSectionProps = {
  details: TeamDetailsState;
  onToggle: () => void;
  open: boolean;
  visibleTeammates: TeamDetailsState["teammates"];
};

export function RuntimeTeamRosterSection({
  details,
  onToggle,
  open,
  visibleTeammates,
}: RuntimeTeamRosterSectionProps) {
  return (
    <TeamDetailsSection
      title="Teammate roster"
      badge={<Badge>{details.teammates.length}</Badge>}
      open={open}
      onToggle={onToggle}
    >
      <div className="space-y-1.5">
        {visibleTeammates.length > 0 ? (
          visibleTeammates.map((teammate) => (
            <div
              key={teammate.id}
              className={detailCardClass}
            >
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-semibold text-[var(--foreground)]">
                    {teammate.name || truncateIdentifier(teammate.id, 18)}
                  </div>
                  <div className="truncate text-xs text-[var(--muted-foreground)]">
                    {teammate.profile || teammate.id}
                  </div>
                </div>
                <span
                  className={cn(
                    detailStatusPillClass,
                    statusTone(teammate.state),
                  )}
                >
                  {teammate.state || "unknown"}
                </span>
              </div>
              {teammate.capabilities && teammate.capabilities.length > 0 ? (
                <div className="mt-1.5 flex flex-wrap gap-1.5">
                  {teammate.capabilities.slice(0, 3).map((capability) => (
                    <span
                      key={capability}
                      className={detailMetaPillClass}
                    >
                      {capability}
                    </span>
                  ))}
                </div>
              ) : null}
            </div>
          ))
        ) : (
          <div className="text-sm text-[var(--muted-foreground)]">
            No teammates registered.
          </div>
        )}
      </div>
    </TeamDetailsSection>
  );
}
