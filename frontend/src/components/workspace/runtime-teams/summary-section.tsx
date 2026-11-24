// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { RefreshCcwIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import {
  isRuntimeTeamProductCapabilitySupported,
  type RuntimeTeamProductCapabilities,
  type RuntimeTeamRecord,
} from "@/lib/runtime-api";
import { truncateIdentifier } from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

import { type RuntimeTeamsView } from "./types";

type TeamsSummarySectionProps = {
  activeTeamCount: number;
  activeRunCount: number;
  activeView: RuntimeTeamsView;
  capabilities: RuntimeTeamProductCapabilities;
  error: string | null;
  isLoading: boolean;
  isRefreshing?: boolean;
  onRefresh?: () => void;
  runCount: number;
  selectedTeam: RuntimeTeamRecord | null;
  setActiveView: (view: RuntimeTeamsView) => void;
  teams: RuntimeTeamRecord[];
};

export function TeamsSummarySection({
  activeTeamCount,
  activeRunCount,
  activeView,
  capabilities,
  error,
  isLoading,
  isRefreshing,
  onRefresh,
  runCount,
  selectedTeam,
  setActiveView,
  teams,
}: TeamsSummarySectionProps) {
  const { t } = useTranslation("workspace");
  const studioDraftCapability = isRuntimeTeamProductCapabilitySupported(
    capabilities,
    "run_draft_create",
  )
    ? "supported"
    : capabilities.run_draft_create === "unsupported"
      ? "unsupported"
      : "unknown";

  return (
    <>
      <div className="mb-3 flex flex-col gap-3 rounded-panel-lg border border-border bg-surface-softer px-3.5 py-3 xl:flex-row xl:items-center xl:justify-between">
        <div className="flex flex-wrap gap-2">
          <Badge>
            {t("panels.teamsPanels.summary.teamsBadge", { count: teams.length })}
          </Badge>
          <Badge>
            {t("panels.teamsPanels.summary.activeBadge", {
              count: activeTeamCount,
            })}
          </Badge>
          <Badge>
            {t("panels.teamsPanels.summary.runsBadge", { count: runCount })}
          </Badge>
          {activeRunCount > 0 ? (
            <Badge>
              {t("panels.teamsPanels.summary.activeRunsBadge", {
                count: activeRunCount,
              })}
            </Badge>
          ) : null}
          {selectedTeam ? (
            <Badge>{truncateIdentifier(selectedTeam.id, 18)}</Badge>
          ) : null}
          {activeView === "dispatch" ? (
            <Badge className="border-accent-primary-border bg-accent-primary-soft text-accent-primary">
              {t("panels.teamsPanels.summary.dispatchView")}
            </Badge>
          ) : null}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={() => setActiveView("teams")}
            className={cn(
              "rounded-control border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              activeView === "teams"
                ? "border-accent-secondary-border bg-accent-secondary-soft text-accent-secondary"
                : "border-border bg-surface-soft text-muted-foreground hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground",
            )}
          >
            {t("panels.teamsPanels.summary.teamsTab")}
          </button>
          <button
            type="button"
            onClick={() => setActiveView("runs")}
            className={cn(
              "rounded-control border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              activeView === "runs"
                ? "border-accent-teal/30 bg-accent-teal/10 text-accent-teal"
                : "border-border bg-surface-soft text-muted-foreground hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground",
            )}
          >
            {t("panels.teamsPanels.summary.runsTab")}
          </button>
          <button
            type="button"
            onClick={() => setActiveView("dispatch")}
            className={cn(
              "rounded-control border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              activeView === "dispatch"
                ? "border-accent-primary-border bg-accent-primary-soft text-accent-primary"
                : "border-border bg-surface-soft text-muted-foreground hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground",
            )}
          >
            {t("panels.teamsPanels.summary.dispatchTab")}
          </button>
          {onRefresh ? (
            <button
              type="button"
              onClick={onRefresh}
              className="inline-flex items-center justify-center rounded-control border border-border bg-surface-soft p-1.5 text-muted-foreground transition hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground"
              aria-label={t("sidebar.refreshRuntimeTeams")}
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
        <div className="rounded-panel border border-accent-gold/18 bg-accent-gold/8 px-3.5 py-3 text-sm leading-6 text-muted-foreground">
          {error}
        </div>
      ) : null}

      <div
        className="mb-3 rounded-panel border border-border bg-surface-soft px-3.5 py-2.5 text-sm leading-6 text-muted-foreground"
        role="note"
      >
        <p>{t("panels.teamsPanels.summary.modeBoundary")}</p>
        <p className="mt-1 text-xs text-muted-foreground/80">
          {studioDraftCapability === "supported"
            ? t("panels.teamsPanels.summary.studioDraftSupported")
            : studioDraftCapability === "unsupported"
              ? t("panels.teamsPanels.summary.studioDraftUnsupported")
              : t("panels.teamsPanels.summary.studioDraftUnknown")}
        </p>
      </div>

      {isLoading && teams.length === 0 ? (
        <div className="rounded-panel border border-white/8 bg-white/4 px-3.5 py-3.5 text-sm text-muted-foreground">
          {t("panels.teamsPanels.summary.loading")}
        </div>
      ) : null}

      {!isLoading && teams.length === 0 && !error ? (
        <div className="rounded-panel border border-dashed border-white/10 px-3.5 py-3.5 text-sm text-muted-foreground">
          {t("panels.teamsPanels.summary.empty")}
        </div>
      ) : null}
    </>
  );
}
