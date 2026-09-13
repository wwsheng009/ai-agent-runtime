// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { cn } from "@/lib/utils";

import { truncateIdentifier } from "../shared";

import { consolePillClass } from "./console-styles";
import type { DispatchConsoleProps } from "./types";

type DispatchTeamSelectorProps = Pick<
  DispatchConsoleProps,
  | "dispatchTeamReadiness"
  | "isDispatchReadinessLoading"
  | "onToggleDispatchTeam"
  | "selectedDispatchTeamIds"
  | "summaryMap"
  | "teams"
>;

export function DispatchTeamSelector({
  dispatchTeamReadiness,
  isDispatchReadinessLoading,
  onToggleDispatchTeam,
  selectedDispatchTeamIds,
  summaryMap,
  teams,
}: DispatchTeamSelectorProps) {
  return (
    <div className="mt-3 space-y-1.5">
      {teams.length === 0 ? (
        <div className="rounded-[0.8rem] border border-dashed border-white/10 px-3 py-2.5 text-sm text-muted-foreground">
          No existing teams yet. Use the provision action above to create runnable teams and fan out the next task.
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
              "flex cursor-pointer items-center justify-between gap-3 rounded-[0.8rem] border px-3 py-2.5 transition",
              checked
                ? "border-[#f0c77b]/24 bg-[#f0c77b]/8"
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
                <span className="block truncate text-sm font-semibold text-foreground">
                  {truncateIdentifier(team.id, 18)}
                </span>
                <span className="block truncate text-xs text-muted-foreground">
                  {summary?.tasks.total ?? 0} tasks · {summary?.teammates.total ?? 0} teammates
                </span>
                <span className="mt-1 block truncate app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
                  {isDispatchReadinessLoading && !readiness
                    ? "checking executability..."
                    : readiness
                      ? readiness.reason
                      : "readiness unavailable"}
                </span>
              </span>
            </span>
            <span className="flex shrink-0 flex-col items-end gap-2">
              <span className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
                {team.status || "unknown"}
              </span>
              <span
                className={cn(
                  consolePillClass,
                  readiness?.executable
                    ? "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                    : "border-white/10 bg-white/6 text-muted-foreground",
                )}
              >
                {readiness?.executable ? "executable" : "not ready"}
              </span>
            </span>
          </label>
        );
      })}
    </div>
  );
}
