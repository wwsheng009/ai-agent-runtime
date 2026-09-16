import {
  type DispatchTeamReadiness,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";
import {
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/types/runtime";
import { useTranslation } from "react-i18next";

import { dispatchStatusPillClass } from "./format";

type DispatchTeamListProps = {
  dispatchTeamReadiness: Record<string, DispatchTeamReadiness>;
  isDispatchReadinessLoading: boolean;
  onToggleDispatchTeam: (teamId: string) => void;
  selectedDispatchTeamIds: string[];
  summaryMap: Map<string, RuntimeTeamSummaryEntry>;
  teams: RuntimeTeamRecord[];
};

export function DispatchTeamList({
  dispatchTeamReadiness,
  isDispatchReadinessLoading,
  onToggleDispatchTeam,
  selectedDispatchTeamIds,
  summaryMap,
  teams,
}: DispatchTeamListProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className="mt-3 space-y-1.5">
      {teams.length === 0 ? (
        <div className="rounded-[0.75rem] border border-dashed border-white/10 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
          {t("panels.teamsDispatch.teamSelect.empty")}
        </div>
      ) : null}
      {teams.map((team) => {
        const checked = selectedDispatchTeamIds.includes(team.id);
        const summary = summaryMap.get(team.id);
        const readiness = dispatchTeamReadiness[team.id];

        return (
          <label
            key={`dispatch-${team.id}`}
            className={cn(
              "flex cursor-pointer items-center justify-between gap-3 rounded-card border px-3 py-2.5 transition",
              checked
                ? "border-accent-gold/24 bg-accent-gold/8"
                : "border-white/8 bg-white/4 hover:border-white/14 hover:bg-white/7",
            )}
          >
            <span className="flex min-w-0 items-center gap-3">
              <input
                type="checkbox"
                checked={checked}
                onChange={() => onToggleDispatchTeam(team.id)}
                className="size-4 rounded border-white/14 bg-transparent"
              />
              <span className="min-w-0">
                <span className="block truncate app-text-13 font-semibold text-foreground">
                  {truncateIdentifier(team.id, 18)}
                </span>
                <span className="block truncate text-xs text-muted-foreground">
                  {t("panels.teamsDispatch.teamSelect.counts", {
                    tasks: String(summary?.tasks.total ?? 0),
                    teammates: String(summary?.teammates.total ?? 0),
                  })}
                </span>
                <span className="mt-0.5 block truncate app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
                  {isDispatchReadinessLoading && !readiness
                    ? t("panels.teamsDispatch.teamSelect.checkingReadiness")
                    : readiness
                      ? readiness.reason
                      : t("panels.teamsDispatch.teamSelect.readinessUnavailable")}
                </span>
              </span>
            </span>
            <span className="flex shrink-0 flex-col items-end gap-2">
              <span className="app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
                {team.status || t("panels.teamsDispatch.teamSelect.unknownStatus")}
              </span>
              <span
                className={cn(
                  dispatchStatusPillClass,
                  readiness?.executable
                    ? "border-accent-teal/24 bg-accent-teal/10 text-accent-teal"
                    : "border-white/10 bg-white/6 text-muted-foreground",
                )}
              >
                {readiness?.executable
                  ? t("panels.teamsDispatch.teamSelect.executable")
                  : t("panels.teamsDispatch.teamSelect.notReady")}
              </span>
            </span>
          </label>
        );
      })}
    </div>
  );
}
