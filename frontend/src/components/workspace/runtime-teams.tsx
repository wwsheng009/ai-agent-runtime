// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import { useRuntimeTeamDispatch } from "@/components/workspace/runtime-teams/use-runtime-team-dispatch";
import {
  ackRuntimeTeamMailboxMessage,
  checkRuntimeTeamPathClaims,
  sendRuntimeTeamMailboxMessage,
  type RuntimeCheckTeamPathClaimsResponse,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import {
  createEmptyDetails,
  parsePathLines,
  type ClaimCheckState,
  type TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared";

import { TeamsDirectoryView } from "./runtime-teams/directory-view";
import { TeamsDispatchView } from "./runtime-teams/dispatch-view";
import { RuntimeTeamRunsView } from "./runtime-teams/runtime-team-runs-view";
import { TeamsSummarySection } from "./runtime-teams/summary-section";
import { type RuntimeTeamsProps, type RuntimeTeamsView } from "./runtime-teams/types";
import { useTeamRuns } from "./runtime-teams/use-team-runs";
import { useTeamDetailsActions } from "./runtime-teams/use-team-details-actions";
import { useTeamDetailsLoader } from "./runtime-teams/use-team-details-loader";
import { useTeamDetailsPolling } from "./runtime-teams/use-team-details-polling";

export function RuntimeTeams({
  className,
  error,
  isLoading,
  isRefreshing,
  onRefresh,
  showHeader = true,
  summaries,
  teams,
}: RuntimeTeamsProps) {
  const { t } = useTranslation("workspace");
  const [searchParams, setSearchParams] = useSearchParams();
  const requestedView = searchParams.get("team_view");
  const activeView: RuntimeTeamsView =
    requestedView === "teams" ||
    requestedView === "runs" ||
    requestedView === "dispatch"
      ? requestedView
      : searchParams.has("team_run_id")
        ? "runs"
        : teams.length === 0
          ? "dispatch"
          : "teams";
  const [selectedTeamId, setSelectedTeamId] = useState("");
  const [details, setDetails] = useState<TeamDetailsState>(createEmptyDetails);
  const [detailsError, setDetailsError] = useState<string | null>(null);
  const [isDetailsLoading, setIsDetailsLoading] = useState(false);
  const [ackingMessageId, setAckingMessageId] = useState("");
  const [isSendingMailbox, setIsSendingMailbox] = useState(false);
  const [mailboxError, setMailboxError] = useState<string | null>(null);
  const [mailboxBodyDraft, setMailboxBodyDraft] = useState("");
  const [mailboxFromDraft, setMailboxFromDraft] = useState("lead");
  const [mailboxToDraft, setMailboxToDraft] = useState("*");
  const [mailboxKindDraft, setMailboxKindDraft] = useState("info");
  const [mailboxTaskDraft, setMailboxTaskDraft] = useState("");
  const [readPathDraft, setReadPathDraft] = useState("");
  const [writePathDraft, setWritePathDraft] = useState("");
  const [isCheckingClaims, setIsCheckingClaims] = useState(false);
  const [claimCheckError, setClaimCheckError] = useState<string | null>(null);
  const [claimCheckState, setClaimCheckState] = useState<ClaimCheckState>(null);
  const selectedTeamIdRef = useRef(selectedTeamId);
  selectedTeamIdRef.current = selectedTeamId;

  function setActiveView(view: RuntimeTeamsView) {
    setSearchParams((current) => {
      const next = new URLSearchParams(current);
      next.set("team_view", view);
      return next;
    });
  }
  const {
    refreshMailboxAndEvents,
    refreshSelectedTeamTasksAndEvents,
  } = useTeamDetailsActions({
    details,
    selectedTeamIdRef,
    setDetails,
    setDetailsError,
  });
  const {
    capabilities: teamRunCapabilities,
    error: teamRunsError,
    isLoading: isTeamRunsLoading,
    refresh: refreshTeamRuns,
    runs: teamRuns,
    updateRun: handleRunUpdated,
  } = useTeamRuns();

  const summaryMap = useMemo(
    () => new Map(summaries.map((summary) => [summary.team_id, summary])),
    [summaries],
  );
  const selectedTeam = teams.find((team) => team.id === selectedTeamId) ?? null;
  const selectedSummary = selectedTeam ? summaryMap.get(selectedTeam.id) : undefined;

  const activeRunCount = teamRuns.filter(
    (run) => run.status.trim().toLowerCase() === "active",
  ).length;

  function handleRefresh() {
    onRefresh?.();
    void refreshTeamRuns();
  }

  function handleOpenLegacyTeam(teamId: string) {
    if (!teams.some((team) => team.id === teamId)) {
      return;
    }
    setSelectedTeamId(teamId);
    setActiveView("teams");
  }

  useEffect(() => {
    if (teams.length === 0) {
      setSelectedTeamId("");
      return;
    }
    if (!teams.some((team) => team.id === selectedTeamId)) {
      setSelectedTeamId(teams[0].id);
    }
  }, [selectedTeamId, teams]);

  useEffect(() => {
    if (
      teams.length === 0 &&
      !searchParams.has("team_view") &&
      !searchParams.has("team_run_id")
    ) {
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          next.set("team_view", "dispatch");
          return next;
        },
        { replace: true },
      );
    }
  }, [searchParams, setSearchParams, teams.length]);

  useTeamDetailsLoader({
    selectedTeamId,
    setClaimCheckError,
    setClaimCheckState,
    setDetails,
    setDetailsError,
    setIsDetailsLoading,
    setMailboxBodyDraft,
    setMailboxError,
    setMailboxFromDraft,
    setMailboxKindDraft,
    setMailboxTaskDraft,
    setMailboxToDraft,
    setReadPathDraft,
    setWritePathDraft,
  });

  useTeamDetailsPolling({
    eventCursor: details.eventCursor,
    isLoading: isDetailsLoading,
    selectedTeamId,
    setDetails,
    setDetailsError,
    summaryState: details.summaryState,
    teamStatus: selectedTeam?.status,
  });

  const activeTeamCount = teams.filter(
    (team) => (team.status || "").trim().toLowerCase() === "active",
  ).length;
  const visibleTasks = details.tasks.slice(0, 6);
  const visibleTeammates = details.teammates.slice(0, 5);
  const visibleEvents = details.events.slice(0, 8);
  const visibleMailbox = details.mailbox.slice(0, 6);
  const visiblePathClaims = details.pathClaims.slice(0, 6);
  const graphEdgeCount = details.graph?.edge_count ?? 0;
  const graphMissingCount = details.graph?.missing_dependencies?.length ?? 0;
  const {
    dispatchMonitor,
    dispatchMonitorCounts,
    dispatchMonitorError,
    dispatchTaskDeliverablesDraft,
    dispatchTaskError,
    dispatchTaskGoalDraft,
    dispatchTaskInputsDraft,
    dispatchTaskPriorityDraft,
    dispatchTaskResults,
    dispatchTaskTitleDraft,
    dispatchTeamReadiness,
    dispatchTemplateMode,
    isDispatchMonitorLoading,
    isDispatchReadinessLoading,
    isDispatchingTask,
    isProvisioningDispatch,
    onDispatchTaskDeliverablesDraftChange: setDispatchTaskDeliverablesDraft,
    onDispatchTaskGoalDraftChange: setDispatchTaskGoalDraft,
    onDispatchTaskInputsDraftChange: setDispatchTaskInputsDraft,
    onDispatchTaskPriorityDraftChange: setDispatchTaskPriorityDraft,
    onDispatchTaskTitleDraftChange: setDispatchTaskTitleDraft,
    onDispatchTaskToTeams: handleDispatchTaskToTeams,
    onDispatchTemplateModeChange: setDispatchTemplateMode,
    onProvisionStrategyDraftChange: setProvisionStrategyDraft,
    onProvisionTeamCountDraftChange: setProvisionTeamCountDraft,
    onProvisionTeammateNamePrefixDraftChange: setProvisionTeammateNamePrefixDraft,
    onProvisionTeammateProfileDraftChange: setProvisionTeammateProfileDraft,
    onProvisionTeamsAndDispatch: handleProvisionTeamsAndDispatch,
    onProvisionUserPrefixDraftChange: setProvisionUserPrefixDraft,
    onProvisionWorkspaceDraftChange: setProvisionWorkspaceDraft,
    onRefreshDispatchMonitor: handleRefreshDispatchMonitor,
    onToggleDispatchTeam: toggleDispatchTeam,
    provisionStrategyDraft,
    provisionTeamCountDraft,
    provisionTeammateNamePrefixDraft,
    provisionTeammateProfileDraft,
    provisionUserPrefixDraft,
    provisionWorkspaceDraft,
    selectedDispatchTeamIds,
  } = useRuntimeTeamDispatch({
    onRefresh,
    onRefreshSelectedTeamTasksAndEvents: refreshSelectedTeamTasksAndEvents,
    selectedTeamId,
    selectedTeamWorkspaceId: selectedTeam?.workspace_id,
    teams,
  });

  async function handleSendMailboxMessage() {
    if (!selectedTeamId) {
      return;
    }
    const teamId = selectedTeamId;
    const body = mailboxBodyDraft.trim();
    if (!body) {
      setMailboxError("mailbox body is required");
      return;
    }

    setIsSendingMailbox(true);
    setMailboxError(null);
    try {
      const response = await sendRuntimeTeamMailboxMessage(teamId, {
        body,
        from_agent: mailboxFromDraft.trim() || undefined,
        kind: mailboxKindDraft.trim() || undefined,
        task_id: mailboxTaskDraft.trim() || undefined,
        to_agent: mailboxToDraft.trim() || undefined,
      });

      if (selectedTeamIdRef.current !== teamId) {
        return;
      }
      setMailboxBodyDraft("");
      setMailboxTaskDraft("");
      const refreshError = await refreshMailboxAndEvents(teamId);
      if (selectedTeamIdRef.current !== teamId) {
        return;
      }

      if (response.dispatch_error?.trim()) {
        setMailboxError(`mailbox saved, but dispatch failed: ${response.dispatch_error.trim()}`);
      } else if (refreshError) {
        setMailboxError(`mailbox saved, but ${refreshError}`);
      }
    } catch (actionError) {
      if (selectedTeamIdRef.current === teamId) {
        setMailboxError(
          actionError instanceof Error
            ? actionError.message
            : "failed to send mailbox message",
        );
      }
    } finally {
      setIsSendingMailbox(false);
    }
  }

  async function handleAckMailboxMessage(messageId: string) {
    if (!selectedTeamId || !messageId) {
      return;
    }
    const teamId = selectedTeamId;
    setAckingMessageId(messageId);
    setMailboxError(null);
    try {
      await ackRuntimeTeamMailboxMessage(teamId, messageId);
      if (selectedTeamIdRef.current !== teamId) {
        return;
      }
      const refreshError = await refreshMailboxAndEvents(teamId);
      if (selectedTeamIdRef.current === teamId && refreshError) {
        setMailboxError(`mailbox message acknowledged, but ${refreshError}`);
      }
    } catch (actionError) {
      if (selectedTeamIdRef.current === teamId) {
        setMailboxError(
          actionError instanceof Error
            ? actionError.message
            : "failed to acknowledge mailbox message",
        );
      }
    } finally {
      setAckingMessageId("");
    }
  }

  async function handleCheckPathClaims() {
    if (!selectedTeamId) {
      return;
    }
    const teamId = selectedTeamId;
    const readPaths = parsePathLines(readPathDraft);
    const writePaths = parsePathLines(writePathDraft);
    if (readPaths.length === 0 && writePaths.length === 0) {
      setClaimCheckError("enter at least one read or write path");
      setClaimCheckState(null);
      return;
    }

    setIsCheckingClaims(true);
    setClaimCheckError(null);
    try {
      const response: RuntimeCheckTeamPathClaimsResponse =
        await checkRuntimeTeamPathClaims(teamId, {
          readPaths,
          writePaths,
        });
      if (selectedTeamIdRef.current !== teamId) {
        return;
      }
      setClaimCheckState({
        ok: response.ok,
        conflicts: response.conflicts,
      });
    } catch (actionError) {
      if (selectedTeamIdRef.current === teamId) {
        setClaimCheckError(
          actionError instanceof Error
            ? actionError.message
            : "failed to check path claim conflicts",
        );
        setClaimCheckState(null);
      }
    } finally {
      setIsCheckingClaims(false);
    }
  }

  return (
    <section className={cn(showHeader ? "mt-4" : "mt-0", className)}>
      {showHeader ? (
        <div className="mb-2 flex items-center justify-between">
          <div className="app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
            {t("panels.teamsDispatch.summary.header")}
          </div>
        </div>
      ) : null}

      <TeamsSummarySection
        activeTeamCount={activeTeamCount}
        activeRunCount={activeRunCount}
        activeView={activeView}
        capabilities={teamRunCapabilities}
        error={error}
        isLoading={isLoading}
        isRefreshing={isRefreshing}
        onRefresh={handleRefresh}
        runCount={teamRuns.length}
        selectedTeam={selectedTeam}
        setActiveView={setActiveView}
        teams={teams}
      />

      {activeView === "dispatch" ? (
        <TeamsDispatchView
          dispatchMonitor={dispatchMonitor}
          dispatchMonitorCounts={dispatchMonitorCounts}
          dispatchMonitorError={dispatchMonitorError}
          dispatchTaskDeliverablesDraft={dispatchTaskDeliverablesDraft}
          dispatchTaskError={dispatchTaskError}
          dispatchTaskGoalDraft={dispatchTaskGoalDraft}
          dispatchTaskInputsDraft={dispatchTaskInputsDraft}
          dispatchTaskPriorityDraft={dispatchTaskPriorityDraft}
          dispatchTaskResults={dispatchTaskResults}
          dispatchTaskTitleDraft={dispatchTaskTitleDraft}
          dispatchTeamReadiness={dispatchTeamReadiness}
          dispatchTemplateMode={dispatchTemplateMode}
          handleDispatchTaskToTeams={handleDispatchTaskToTeams}
          handleProvisionTeamsAndDispatch={handleProvisionTeamsAndDispatch}
          handleRefreshDispatchMonitor={handleRefreshDispatchMonitor}
          isDispatchMonitorLoading={isDispatchMonitorLoading}
          isDispatchReadinessLoading={isDispatchReadinessLoading}
          isDispatchingTask={isDispatchingTask}
          isProvisioningDispatch={isProvisioningDispatch}
          provisionStrategyDraft={provisionStrategyDraft}
          provisionTeamCountDraft={provisionTeamCountDraft}
          provisionTeammateNamePrefixDraft={provisionTeammateNamePrefixDraft}
          provisionTeammateProfileDraft={provisionTeammateProfileDraft}
          provisionUserPrefixDraft={provisionUserPrefixDraft}
          provisionWorkspaceDraft={provisionWorkspaceDraft}
          selectedDispatchTeamIds={selectedDispatchTeamIds}
          setDispatchTaskDeliverablesDraft={setDispatchTaskDeliverablesDraft}
          setDispatchTaskGoalDraft={setDispatchTaskGoalDraft}
          setDispatchTaskInputsDraft={setDispatchTaskInputsDraft}
          setDispatchTaskPriorityDraft={setDispatchTaskPriorityDraft}
          setDispatchTaskTitleDraft={setDispatchTaskTitleDraft}
          setDispatchTemplateMode={setDispatchTemplateMode}
          setProvisionStrategyDraft={setProvisionStrategyDraft}
          setProvisionTeamCountDraft={setProvisionTeamCountDraft}
          setProvisionTeammateNamePrefixDraft={setProvisionTeammateNamePrefixDraft}
          setProvisionTeammateProfileDraft={setProvisionTeammateProfileDraft}
          setProvisionUserPrefixDraft={setProvisionUserPrefixDraft}
          setProvisionWorkspaceDraft={setProvisionWorkspaceDraft}
          summaryMap={summaryMap}
          teams={teams}
          toggleDispatchTeam={toggleDispatchTeam}
        />
      ) : activeView === "runs" ? (
        <RuntimeTeamRunsView
          capabilities={teamRunCapabilities}
          error={teamRunsError}
          isLoading={isTeamRunsLoading}
          onOpenLegacyTeam={handleOpenLegacyTeam}
          onRetry={() => void refreshTeamRuns()}
          onRunUpdated={handleRunUpdated}
          runs={teamRuns}
        />
      ) : (
        <TeamsDirectoryView
          ackingMessageId={ackingMessageId}
          claimCheckError={claimCheckError}
          claimCheckState={claimCheckState}
          details={details}
          detailsError={detailsError}
          graphEdgeCount={graphEdgeCount}
          graphMissingCount={graphMissingCount}
          handleAckMailboxMessage={handleAckMailboxMessage}
          handleCheckPathClaims={handleCheckPathClaims}
          handleSendMailboxMessage={handleSendMailboxMessage}
          isCheckingClaims={isCheckingClaims}
          isDetailsLoading={isDetailsLoading}
          isSendingMailbox={isSendingMailbox}
          mailboxBodyDraft={mailboxBodyDraft}
          mailboxError={mailboxError}
          mailboxFromDraft={mailboxFromDraft}
          mailboxKindDraft={mailboxKindDraft}
          mailboxTaskDraft={mailboxTaskDraft}
          mailboxToDraft={mailboxToDraft}
          readPathDraft={readPathDraft}
          selectedSummary={details.liveSummary ?? selectedSummary}
          selectedTeam={selectedTeam}
          selectedTeamId={selectedTeamId}
          setMailboxBodyDraft={setMailboxBodyDraft}
          setMailboxFromDraft={setMailboxFromDraft}
          setMailboxKindDraft={setMailboxKindDraft}
          setMailboxTaskDraft={setMailboxTaskDraft}
          setMailboxToDraft={setMailboxToDraft}
          setReadPathDraft={setReadPathDraft}
          setSelectedTeamId={setSelectedTeamId}
          setWritePathDraft={setWritePathDraft}
          summaryMap={summaryMap}
          teams={teams}
          visibleEvents={visibleEvents}
          visibleMailbox={visibleMailbox}
          visiblePathClaims={visiblePathClaims}
          visibleTasks={visibleTasks}
          visibleTeammates={visibleTeammates}
          writePathDraft={writePathDraft}
        />
      )}
    </section>
  );
}
