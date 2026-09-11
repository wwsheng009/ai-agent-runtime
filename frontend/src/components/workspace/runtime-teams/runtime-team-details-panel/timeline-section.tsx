import { Badge } from "@/components/ui/badge";
import {
  describeEventPayload,
  type TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared";
import { formatRelativeTimestamp } from "@/lib/utils";

import { detailCardClass } from "./format";
import { TeamDetailsSection } from "./primitives";

type RuntimeTeamTimelineSectionProps = {
  details: TeamDetailsState;
  onToggle: () => void;
  open: boolean;
  visibleEvents: TeamDetailsState["events"];
};

export function RuntimeTeamTimelineSection({
  details,
  onToggle,
  open,
  visibleEvents,
}: RuntimeTeamTimelineSectionProps) {
  return (
    <TeamDetailsSection
      title="Team timeline"
      badge={<Badge>{details.events.length}</Badge>}
      open={open}
      onToggle={onToggle}
    >
      <div className="space-y-1.5">
        {visibleEvents.length > 0 ? (
          visibleEvents.map((event) => (
            <div
              key={`${event.seq}-${event.type}`}
              className={detailCardClass}
            >
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-semibold text-[var(--foreground)]">
                    {event.type.replaceAll(".", " / ")}
                  </div>
                  <div className="mt-0.5 truncate text-xs text-[var(--muted-foreground)]">
                    {describeEventPayload(event)}
                  </div>
                </div>
                <div className="shrink-0 text-right">
                  <div className="app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                    seq {event.seq}
                  </div>
                  <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                    {formatRelativeTimestamp(event.timestamp)}
                  </div>
                </div>
              </div>
            </div>
          ))
        ) : (
          <div className="text-sm text-[var(--muted-foreground)]">
            No team events available.
          </div>
        )}
      </div>
    </TeamDetailsSection>
  );
}
