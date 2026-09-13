// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { CompassIcon, LoaderCircleIcon } from "lucide-react";
import { Link } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { cn } from "@/lib/utils";
import { type Dispatch, type SetStateAction, useMemo } from "react";
import { type TFunction } from "i18next";

import { summarizeSidebarSessions } from "@/components/workspace/workspace-sidebar-shared";
import { type RuntimeSessionsSummary } from "@/hooks/workspace/use-runtime-sessions-data";
import { type RuntimeTeamRecord } from "@/lib/runtime-api";

import { SidebarSection } from "./section-shell";
import {
  type SidebarSectionId,
  type SidebarSectionState,
  type SidebarThread,
} from "./types";

type WorkspaceSidebarRuntimeSectionProps = {
  onCloseMobile?: () => void;
  openSections: SidebarSectionState;
  runtimeSessionsError: string | null;
  runtimeSessionsLoading: boolean;
  runtimeSessionsRefreshing?: boolean;
  runtimeSessionsSummary: RuntimeSessionsSummary;
  runtimeTeams: RuntimeTeamRecord[];
  setRuntimeTeamsDialogOpen: Dispatch<SetStateAction<boolean>>;
  t: TFunction<"workspace">;
  threads: SidebarThread[];
  toggleSection: (section: SidebarSectionId) => void;
};

export function WorkspaceSidebarRuntimeSection({
  onCloseMobile,
  openSections,
  runtimeSessionsError,
  runtimeSessionsLoading,
  runtimeSessionsRefreshing,
  runtimeSessionsSummary,
  runtimeTeams,
  setRuntimeTeamsDialogOpen,
  t,
  threads,
  toggleSection,
}: WorkspaceSidebarRuntimeSectionProps) {
  const sessionRailSummary = useMemo(
    () => summarizeSidebarSessions(threads),
    [threads],
  );
  const liveTeamCount = runtimeTeams.filter(
    (team) => (team.status || "").trim().toLowerCase() === "active",
  ).length;
  const hasRuntimeStats =
    runtimeSessionsSummary.totalCount > 0 ||
    runtimeSessionsSummary.recoverableCount > 0 ||
    sessionRailSummary.pendingCount > 0 ||
    runtimeSessionsLoading ||
    Boolean(runtimeSessionsRefreshing);

  return (
      <SidebarSection
        id="runtime"
        icon={CompassIcon}
        iconClassName="text-accent-secondary"
        title={t("sidebar.sections.runtime")}
        count={
          liveTeamCount > 0 ? (
            <Badge>{t("sidebar.active", { count: liveTeamCount })}</Badge>
          ) : runtimeSessionsSummary.totalCount > 0 ? (
            <Badge>
              {t("sidebar.runtimeStats.sessions", {
                count: runtimeSessionsSummary.totalCount,
              })}
            </Badge>
          ) : undefined
        }
        isOpen={openSections.runtime}
        onToggle={toggleSection}
      >
        <section className="rounded-panel border border-border bg-surface-softer p-3">
          {hasRuntimeStats ? (
            <div className="flex flex-wrap gap-1.5 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              {runtimeSessionsSummary.totalCount > 0 ? (
                <span className="rounded-control border border-border bg-surface-soft px-2 py-0.5">
                  {t("sidebar.runtimeStats.sessions", {
                    count: runtimeSessionsSummary.totalCount,
                  })}
                </span>
              ) : null}
              {runtimeSessionsSummary.recoverableCount > 0 ? (
                <span className="rounded-control border border-border bg-surface-soft px-2 py-0.5">
                  {t("sidebar.runtimeStats.recoverable", {
                    count: runtimeSessionsSummary.recoverableCount,
                  })}
                </span>
              ) : null}
              {sessionRailSummary.pendingCount > 0 ? (
                <span className="rounded-control border border-border bg-surface-soft px-2 py-0.5">
                  {t("sidebar.runtimeStats.pending", {
                    count: sessionRailSummary.pendingCount,
                  })}
                </span>
              ) : null}
            {runtimeSessionsLoading || runtimeSessionsRefreshing ? (
              <span className="inline-flex items-center gap-1.5 rounded-control border border-border bg-surface-soft px-2 py-0.5">
                <LoaderCircleIcon size={12} className="animate-spin" />
                {t("sidebar.runtimeStats.syncing")}
              </span>
            ) : null}
            </div>
          ) : null}

        {runtimeSessionsError ? (
          <div className="mt-3 rounded-card border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
            {runtimeSessionsError}
          </div>
        ) : null}

        {runtimeTeams.length > 0 ? (
          <div className="mt-3 space-y-1.5">
            {runtimeTeams.slice(0, 4).map((team) => (
              <div
                key={team.id}
                className="flex items-center justify-between rounded-[0.75rem] border border-border bg-surface-solid px-2.5 py-2"
              >
                <div className="truncate app-text-13 text-foreground">
                  {team.id}
                </div>
                <span className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                  {team.status || t("sidebar.unknown")}
                </span>
              </div>
            ))}
          </div>
        ) : null}

        <Button
          variant="secondary"
          size="sm"
          className="mt-3 w-full"
          onClick={() => setRuntimeTeamsDialogOpen(true)}
        >
          {t("sidebar.openRuntimeTeamDetails")}
        </Button>
        <Link
          to="/runtime/config"
          onClick={onCloseMobile}
          className={cn(
            buttonVariants({ variant: "secondary", size: "sm" }),
            "mt-2 w-full",
          )}
        >
          {t("sidebar.backendConfigPage")}
        </Link>
      </section>
    </SidebarSection>
  );
}
