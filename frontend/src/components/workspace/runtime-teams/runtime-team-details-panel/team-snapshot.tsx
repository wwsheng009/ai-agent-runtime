import {
  ActivityIcon,
  GitBranchPlusIcon,
  LoaderCircleIcon,
  UsersRoundIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import {
  getSummaryCount,
  type TeamDetailsState,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import {
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/types/runtime";

type RuntimeTeamSnapshotProps = {
  details: TeamDetailsState;
  detailsError: string | null;
  graphEdgeCount: number;
  graphMissingCount: number;
  isDetailsLoading: boolean;
  selectedSummary: RuntimeTeamSummaryEntry | undefined;
  selectedTeam: RuntimeTeamRecord;
};

export function RuntimeTeamSnapshot({
  details,
  detailsError,
  graphEdgeCount,
  graphMissingCount,
  isDetailsLoading,
  selectedSummary,
  selectedTeam,
}: RuntimeTeamSnapshotProps) {
  const { t } = useTranslation("workspace");

  return (
    <>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="app-text-10 uppercase tracking-[0.16em] text-accent-secondary">
            {t("panels.teamsPanels.details.snapshot.title")}
          </div>
          <div className="mt-1.5 truncate text-base font-semibold text-foreground">
            {selectedTeam.id}
          </div>
        </div>
        <Badge>
          {selectedTeam.status || t("panels.teamsPanels.details.statusUnknown")}
        </Badge>
      </div>

      <div className="mt-2.5 flex flex-wrap gap-1.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
        {selectedTeam.workspace_id ? (
          <span className="rounded-control border border-border bg-surface-soft px-2 py-0.5">
            {t("panels.teamsPanels.details.snapshot.workspacePill", {
              id: selectedTeam.workspace_id,
            })}
          </span>
        ) : null}
        {selectedTeam.lead_session_id ? (
          <span className="rounded-control border border-border bg-surface-soft px-2 py-0.5">
            {t("panels.teamsPanels.details.snapshot.leadPill", {
              id: truncateIdentifier(selectedTeam.lead_session_id, 18),
            })}
          </span>
        ) : null}
        {selectedTeam.strategy ? (
          <span className="rounded-control border border-border bg-surface-soft px-2 py-0.5">
            {selectedTeam.strategy}
          </span>
        ) : null}
      </div>

      <div className="mt-3 grid gap-2.5 lg:grid-cols-3">
        <div className="rounded-card border border-border bg-surface-softer px-3 py-2.5">
          <div className="flex items-center gap-2 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            <ActivityIcon size={14} />
            {t("panels.teamsPanels.details.snapshot.tasksTitle")}
          </div>
          <div className="mt-2.5 grid grid-cols-4 gap-2 text-xs text-muted-foreground">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">
                {t("panels.teamsPanels.details.snapshot.ready")}
              </div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {getSummaryCount(selectedSummary, "tasks", "ready")}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">
                {t("panels.teamsPanels.details.snapshot.running")}
              </div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {getSummaryCount(selectedSummary, "tasks", "running")}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">
                {t("panels.teamsPanels.details.snapshot.done")}
              </div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {getSummaryCount(selectedSummary, "tasks", "done")}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">
                {t("panels.teamsPanels.details.snapshot.failed")}
              </div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {getSummaryCount(selectedSummary, "tasks", "failed")}
              </div>
            </div>
          </div>
        </div>

        <div className="rounded-card border border-border bg-surface-softer px-3 py-2.5">
          <div className="flex items-center gap-2 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            <UsersRoundIcon size={14} />
            {t("panels.teamsPanels.details.snapshot.teammatesTitle")}
          </div>
          <div className="mt-2.5 flex items-end justify-between gap-3">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {t("panels.teamsPanels.details.snapshot.total")}
              </div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {selectedSummary?.teammates.total ?? 0}
              </div>
            </div>
            {selectedTeam.max_teammates ? (
              <div className="text-xs text-muted-foreground">
                {t("panels.teamsPanels.details.snapshot.cap", {
                  count: selectedTeam.max_teammates,
                })}
              </div>
            ) : null}
          </div>
        </div>

        <div className="rounded-card border border-border bg-surface-softer px-3 py-2.5">
          <div className="flex items-center gap-2 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            <GitBranchPlusIcon size={14} />
            {t("panels.teamsPanels.details.snapshot.graphTitle")}
            {isDetailsLoading ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : null}
          </div>
          <div className="mt-2.5 grid grid-cols-3 gap-2 text-xs text-muted-foreground">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">
                {t("panels.teamsPanels.details.snapshot.nodes")}
              </div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {details.graph?.count ?? 0}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">
                {t("panels.teamsPanels.details.snapshot.edges")}
              </div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {graphEdgeCount}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">
                {t("panels.teamsPanels.details.snapshot.missing")}
              </div>
              <div className="mt-1 text-sm font-semibold text-foreground">
                {graphMissingCount}
              </div>
            </div>
          </div>
        </div>
      </div>

      {detailsError ? (
        <div className="mt-3 rounded-[0.75rem] border border-accent-gold/18 bg-accent-gold/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
          {detailsError}
        </div>
      ) : null}
    </>
  );
}
