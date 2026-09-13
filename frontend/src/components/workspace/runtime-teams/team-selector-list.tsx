import {
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

import { truncateIdentifier } from "./shared";

type TeamSelectorListProps = {
  onSelectTeam: (teamId: string) => void;
  selectedTeamId: string;
  summaryMap: Map<string, RuntimeTeamSummaryEntry>;
  teams: RuntimeTeamRecord[];
};

export function TeamSelectorList({
  onSelectTeam,
  selectedTeamId,
  summaryMap,
  teams,
}: TeamSelectorListProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className="space-y-1.5">
      {teams.map((team) => {
        const summary = summaryMap.get(team.id);
        const isActive = team.id === selectedTeamId;
        return (
          <button
            key={team.id}
            type="button"
            onClick={() => onSelectTeam(team.id)}
            className={cn(
              "w-full rounded-card border px-3 py-2.5 text-left transition",
              isActive
                ? "border-accent-teal/30 bg-accent-teal/10 shadow-[0_0_0_1px_rgba(143,208,198,0.12)]"
                : "border-white/8 bg-white/4 hover:border-white/14 hover:bg-white/7",
            )}
          >
            <div className="flex items-center justify-between gap-3">
              <div className="truncate text-base font-semibold text-foreground">
                {truncateIdentifier(team.id, 16)}
              </div>
              <span className="shrink-0 app-text-11 uppercase tracking-[0.15em] text-muted-foreground">
                {team.status || t("panels.teamsPanels.details.statusUnknown")}
              </span>
            </div>
            <div className="mt-1.5 flex flex-wrap gap-1.5 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              <span>
                {t("panels.teamsPanels.teams.tasksCount", {
                  count: summary?.tasks.total ?? 0,
                })}
              </span>
              <span>
                {t("panels.teamsPanels.teams.teammatesCount", {
                  count: summary?.teammates.total ?? 0,
                })}
              </span>
              {team.strategy ? <span>{team.strategy}</span> : null}
            </div>
          </button>
        );
      })}
    </div>
  );
}
