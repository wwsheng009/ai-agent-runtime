// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { RefreshCcwIcon } from "lucide-react";
import { type Dispatch, type SetStateAction } from "react";

import { Badge } from "@/components/ui/badge";
import { type RuntimeTeamRecord } from "@/lib/runtime-api";
import { truncateIdentifier } from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";

import { type RuntimeTeamsView } from "./types";

type TeamsSummarySectionProps = {
  activeTeamCount: number;
  activeView: RuntimeTeamsView;
  error: string | null;
  isLoading: boolean;
  isRefreshing?: boolean;
  onRefresh?: () => void;
  selectedTeam: RuntimeTeamRecord | null;
  setActiveView: Dispatch<SetStateAction<RuntimeTeamsView>>;
  teams: RuntimeTeamRecord[];
};

export function TeamsSummarySection({
  activeTeamCount,
  activeView,
  error,
  isLoading,
  isRefreshing,
  onRefresh,
  selectedTeam,
  setActiveView,
  teams,
}: TeamsSummarySectionProps) {
  return (
    <>
      <div className="mb-3 flex flex-col gap-3 rounded-[0.95rem] border border-border bg-surface-softer px-3.5 py-3 xl:flex-row xl:items-center xl:justify-between">
        <div className="flex flex-wrap gap-2">
          <Badge>{teams.length} teams</Badge>
          <Badge>{activeTeamCount} active</Badge>
          {selectedTeam ? (
            <Badge>{truncateIdentifier(selectedTeam.id, 18)}</Badge>
          ) : null}
          {activeView === "dispatch" ? (
            <Badge className="border-accent-primary-border bg-accent-primary-soft text-accent-primary">
              dispatch view
            </Badge>
          ) : null}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={() => setActiveView("teams")}
            className={cn(
              "rounded-[0.65rem] border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              activeView === "teams"
                ? "border-accent-secondary-border bg-accent-secondary-soft text-accent-secondary"
                : "border-border bg-surface-soft text-muted-foreground hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground",
            )}
          >
            Teams
          </button>
          <button
            type="button"
            onClick={() => setActiveView("dispatch")}
            className={cn(
              "rounded-[0.65rem] border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              activeView === "dispatch"
                ? "border-accent-primary-border bg-accent-primary-soft text-accent-primary"
                : "border-border bg-surface-soft text-muted-foreground hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground",
            )}
          >
            Dispatch
          </button>
          {onRefresh ? (
            <button
              type="button"
              onClick={onRefresh}
              className="inline-flex items-center justify-center rounded-[0.65rem] border border-border bg-surface-soft p-1.5 text-muted-foreground transition hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground"
              aria-label="Refresh runtime teams"
            >
              <RefreshCcwIcon
                size={14}
                className={cn(isRefreshing ? "animate-spin" : "")}
              />
            </button>
          ) : null}
        </div>
      </div>

      {error ? (
        <div className="rounded-[0.9rem] border border-[#f0c77b]/18 bg-[#f0c77b]/8 px-3.5 py-3 text-sm leading-6 text-muted-foreground">
          {error}
        </div>
      ) : null}

      {isLoading && teams.length === 0 ? (
        <div className="rounded-[0.9rem] border border-white/8 bg-white/4 px-3.5 py-3.5 text-sm text-muted-foreground">
          Loading runtime teams...
        </div>
      ) : null}

      {!isLoading && teams.length === 0 && !error ? (
        <div className="rounded-[0.9rem] border border-dashed border-white/10 px-3.5 py-3.5 text-sm text-muted-foreground">
          No runtime teams available.
        </div>
      ) : null}
    </>
  );
}
