import { Badge } from "@/components/ui/badge";
import {
  describeEventPayload,
  type TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared";
import { formatRelativeTimestamp } from "@/lib/utils";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("workspace");

  return (
    <TeamDetailsSection
      title={t("panels.teamsPanels.details.timeline.title")}
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
                  <div className="truncate app-text-13 font-semibold text-foreground">
                    {event.type.replaceAll(".", " / ")}
                  </div>
                  <div className="mt-0.5 truncate text-xs text-muted-foreground">
                    {describeEventPayload(event)}
                  </div>
                </div>
                <div className="shrink-0 text-right">
                  <div className="app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
                    {t("panels.teamsPanels.details.timeline.seq", {
                      seq: event.seq,
                    })}
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
            {t("panels.teamsPanels.details.timeline.empty")}
          </div>
        )}
      </div>
    </TeamDetailsSection>
  );
}
