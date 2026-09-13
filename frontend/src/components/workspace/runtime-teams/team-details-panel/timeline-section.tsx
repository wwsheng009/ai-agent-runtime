// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import { describeEventPayload, prettyEventType } from "@/components/workspace/runtime-teams/shared";
import { formatRelativeTimestamp } from "@/lib/utils";

import { detailsCardClass, detailsPanelClass } from "./format";
import { type TeamDetailsPanelProps } from "./types";

type TeamDetailsPanelTimelineProps = Pick<
  TeamDetailsPanelProps,
  | "visibleEvents"
>;

export function TeamDetailsPanelTimeline({
  visibleEvents,
}: TeamDetailsPanelTimelineProps) {
  return (
    <div className={detailsPanelClass}>
      <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-muted-foreground">
        Team timeline
      </div>
      <div className="mt-3 space-y-2">
        {visibleEvents.length > 0 ? (
          visibleEvents.map((event) => (
            <div key={`${event.seq}-${event.type}`} className={detailsCardClass}>
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-sm font-semibold text-foreground">
                    {prettyEventType(event.type)}
                  </div>
                  <div className="mt-1 truncate text-xs text-muted-foreground">
                    {describeEventPayload(event)}
                  </div>
                </div>
                <div className="shrink-0 text-right">
                  <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                    seq {event.seq}
                  </div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    {formatRelativeTimestamp(event.timestamp)}
                  </div>
                </div>
              </div>
            </div>
          ))
        ) : (
          <div className="text-sm text-muted-foreground">
            No team events available.
          </div>
        )}
      </div>
    </div>
  );
}
