import {
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

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
              "w-full rounded-[0.8rem] border px-3 py-2.5 text-left transition",
              isActive
                ? "border-[#8fd0c6]/30 bg-[#8fd0c6]/10 shadow-[0_0_0_1px_rgba(143,208,198,0.12)]"
                : "border-white/8 bg-white/4 hover:border-white/14 hover:bg-white/7",
            )}
          >
            <div className="flex items-center justify-between gap-3">
              <div className="truncate text-base font-semibold text-[var(--foreground)]">
                {truncateIdentifier(team.id, 16)}
              </div>
              <span className="shrink-0 app-text-11 uppercase tracking-[0.15em] text-[var(--muted-foreground)]">
                {team.status || "unknown"}
              </span>
            </div>
            <div className="mt-1.5 flex flex-wrap gap-1.5 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              <span>{summary?.tasks.total ?? 0} tasks</span>
              <span>{summary?.teammates.total ?? 0} teammates</span>
              {team.strategy ? <span>{team.strategy}</span> : null}
            </div>
          </button>
        );
      })}
    </div>
  );
}
