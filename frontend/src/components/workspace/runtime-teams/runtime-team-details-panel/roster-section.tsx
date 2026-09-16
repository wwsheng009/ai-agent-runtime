import { Badge } from "@/components/ui/badge";
import {
  statusTone,
  type TeamDetailsState,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("workspace");

  return (
    <TeamDetailsSection
      title={t("panels.teamsPanels.details.roster.title")}
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
                  <div className="truncate app-text-13 font-semibold text-foreground">
                    {teammate.name || truncateIdentifier(teammate.id, 18)}
                  </div>
                  <div className="truncate text-xs text-muted-foreground">
                    {teammate.profile || teammate.id}
                  </div>
                </div>
                <span
                  className={cn(
                    detailStatusPillClass,
                    statusTone(teammate.state),
                  )}
                >
                  {teammate.state ||
                    t("panels.teamsPanels.details.statusUnknown")}
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
          <div className="text-sm text-muted-foreground">
            {t("panels.teamsPanels.details.roster.empty")}
          </div>
        )}
      </div>
    </TeamDetailsSection>
  );
}
