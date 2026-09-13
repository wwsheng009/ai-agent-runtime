// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import { Badge } from "@/components/ui/badge";
import { getSummaryCount, truncateIdentifier } from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";
import { ActivityIcon, GitBranchPlusIcon, LoaderCircleIcon, UsersRoundIcon } from "lucide-react";

import { detailsCardClass } from "./format";
import { type TeamDetailsPanelProps } from "./types";

type TeamDetailsPanelSnapshotProps = Pick<
  TeamDetailsPanelProps,
  | "details"
  | "detailsError"
  | "graphEdgeCount"
  | "graphMissingCount"
  | "isDetailsLoading"
  | "selectedSummary"
  | "selectedTeam"
>;

export function TeamDetailsPanelSnapshot({
  details,
  detailsError,
  graphEdgeCount,
  graphMissingCount,
  isDetailsLoading,
  selectedSummary,
  selectedTeam,
}: TeamDetailsPanelSnapshotProps) {
  return (
    <>
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="text-sm font-semibold text-foreground">
            Team snapshot
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {selectedTeam.id}
          </div>
        </div>
        <Badge>{selectedTeam.status || "unknown"}</Badge>
      </div>

      <div className="mt-3 grid gap-2 sm:grid-cols-2 xl:grid-cols-1">
        <div className={detailsCardClass}>
          <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-muted-foreground">
            <ActivityIcon size={14} />
            Tasks
          </div>
          <div className="mt-3 grid grid-cols-4 gap-2 text-xs text-muted-foreground">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">Ready</div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {getSummaryCount(selectedSummary, "tasks", "ready")}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">Running</div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {getSummaryCount(selectedSummary, "tasks", "running")}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">Done</div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {getSummaryCount(selectedSummary, "tasks", "done")}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">Failed</div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {getSummaryCount(selectedSummary, "tasks", "failed")}
              </div>
            </div>
          </div>
        </div>

        <div className={detailsCardClass}>
          <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-muted-foreground">
            <UsersRoundIcon size={14} />
            Teammates
          </div>
          <div className="mt-3 flex items-end justify-between gap-3">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                Total
              </div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {selectedSummary?.teammates.total ?? 0}
              </div>
            </div>
            {selectedTeam.max_teammates ? (
              <div className="text-xs text-muted-foreground">
                cap {selectedTeam.max_teammates}
              </div>
            ) : null}
          </div>
        </div>
      </div>

      <div className={cn("mt-3", detailsCardClass)}>
        <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-muted-foreground">
          <GitBranchPlusIcon size={14} />
          Task Graph
          {isDetailsLoading ? (
            <LoaderCircleIcon size={14} className="animate-spin" />
          ) : null}
        </div>
        <div className="mt-3 grid grid-cols-3 gap-2 text-xs text-muted-foreground">
          <div>
            <div className="app-text-10 uppercase tracking-[0.14em]">Nodes</div>
            <div className="mt-1 text-sm font-semibold text-foreground">
              {details.graph?.count ?? 0}
            </div>
          </div>
          <div>
            <div className="app-text-10 uppercase tracking-[0.14em]">Edges</div>
            <div className="mt-1 text-sm font-semibold text-foreground">
              {graphEdgeCount}
            </div>
          </div>
          <div>
            <div className="app-text-10 uppercase tracking-[0.14em]">Missing</div>
            <div className="mt-1 text-sm font-semibold text-foreground">
              {graphMissingCount}
            </div>
          </div>
        </div>
      </div>

      <div className="mt-3 space-y-1.5 text-sm text-muted-foreground">
        {selectedTeam.workspace_id ? (
          <div>workspace: {selectedTeam.workspace_id}</div>
        ) : null}
        {selectedTeam.lead_session_id ? (
          <div>lead session: {truncateIdentifier(selectedTeam.lead_session_id, 18)}</div>
        ) : null}
        {selectedTeam.strategy ? <div>strategy: {selectedTeam.strategy}</div> : null}
      </div>

      {detailsError ? (
        <div className="mt-3 rounded-card border border-accent-gold/18 bg-accent-gold/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
          {detailsError}
        </div>
      ) : null}
    </>
  );
}
