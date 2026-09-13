// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { useRuntimeTeamDispatch } from "@/components/workspace/runtime-teams/use-runtime-team-dispatch";
import {
  ackRuntimeTeamMailboxMessage,
  checkRuntimeTeamPathClaims,
  getRuntimeTeamTaskGraph,
  listRuntimeTeamEvents,
  listRuntimeTeamMailbox,
  listRuntimeTeamTasks,
  sendRuntimeTeamMailboxMessage,
  type RuntimeCheckTeamPathClaimsResponse,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import {
  createEmptyDetails,
  parsePathLines,
  sortEvents,
  sortMailbox,
  sortTasks,
  type ClaimCheckState,
  type TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared";

import { TeamsDirectoryView } from "./runtime-teams/directory-view";
import { TeamsDispatchView } from "./runtime-teams/dispatch-view";
import { TeamsSummarySection } from "./runtime-teams/summary-section";
import { type RuntimeTeamsProps, type RuntimeTeamsView } from "./runtime-teams/types";
import { useTeamDetailsLoader } from "./runtime-teams/use-team-details-loader";

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
  const [activeView, setActiveView] = useState<RuntimeTeamsView>("teams");
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

  const summaryMap = useMemo(
    () => new Map(summaries.map((summary) => [summary.team_id, summary])),
    [summaries],
  );

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
    if (teams.length === 0) {
      setActiveView("dispatch");
    }
  }, [teams.length]);

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


  const selectedTeam = teams.find((team) => team.id === selectedTeamId) ?? null;
  const selectedSummary = selectedTeam ? summaryMap.get(selectedTeam.id) : undefined;
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

  async function refreshMailboxAndEvents(teamId: string) {
    const [mailboxResponse, eventsResponse] = await Promise.all([
      listRuntimeTeamMailbox(teamId, {
        includeBroadcast: true,
        limit: 10,
      }),
      listRuntimeTeamEvents(teamId, { limit: 12 }),
    ]);

    setDetails((current) => ({
      ...current,
      mailbox: sortMailbox(mailboxResponse.messages),
      events: sortEvents(eventsResponse.events),
    }));
  }

  async function refreshSelectedTeamTasksAndEvents(teamId: string) {
    const [tasksResponse, graphResponse, eventsResponse] = await Promise.all([
      listRuntimeTeamTasks(teamId, {
        includeDependencies: true,
        includeDependents: true,
        limit: 12,
      }),
      getRuntimeTeamTaskGraph(teamId, { limit: 40 }),
      listRuntimeTeamEvents(teamId, { limit: 12 }),
    ]);

    setDetails((current) => ({
      ...current,
      tasks: sortTasks(tasksResponse.tasks),
      graph: graphResponse,
      events: sortEvents(eventsResponse.events),
    }));
  }
  async function handleSendMailboxMessage() {
    if (!selectedTeamId) {
      return;
    }
    const body = mailboxBodyDraft.trim();
    if (!body) {
      setMailboxError("mailbox body is required");
      return;
    }

    setIsSendingMailbox(true);
    setMailboxError(null);
    try {
      const response = await sendRuntimeTeamMailboxMessage(selectedTeamId, {
        body,
        from_agent: mailboxFromDraft.trim() || undefined,
        kind: mailboxKindDraft.trim() || undefined,
        task_id: mailboxTaskDraft.trim() || undefined,
        to_agent: mailboxToDraft.trim() || undefined,
      });

      await refreshMailboxAndEvents(selectedTeamId);
      setMailboxBodyDraft("");
      setMailboxTaskDraft("");

      if (response.dispatch_error?.trim()) {
        setMailboxError(`mailbox saved, but dispatch failed: ${response.dispatch_error.trim()}`);
      }
    } catch (actionError) {
      setMailboxError(
        actionError instanceof Error ? actionError.message : "failed to send mailbox message",
      );
    } finally {
      setIsSendingMailbox(false);
    }
  }

  async function handleAckMailboxMessage(messageId: string) {
    if (!selectedTeamId || !messageId) {
      return;
    }
    setAckingMessageId(messageId);
    setMailboxError(null);
    try {
      await ackRuntimeTeamMailboxMessage(selectedTeamId, messageId);
      const ackedAt = new Date().toISOString();
      setDetails((current) => ({
        ...current,
        mailbox: current.mailbox.map((message) =>
          message.id === messageId ? { ...message, acked_at: ackedAt } : message,
        ),
      }));
    } catch (actionError) {
      setMailboxError(
        actionError instanceof Error ? actionError.message : "failed to acknowledge mailbox message",
      );
    } finally {
      setAckingMessageId("");
    }
  }

  async function handleCheckPathClaims() {
    if (!selectedTeamId) {
      return;
    }
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
        await checkRuntimeTeamPathClaims(selectedTeamId, {
          readPaths,
          writePaths,
        });
      setClaimCheckState({
        ok: response.ok,
        conflicts: response.conflicts,
      });
    } catch (actionError) {
      setClaimCheckError(
        actionError instanceof Error ? actionError.message : "failed to check path claim conflicts",
      );
      setClaimCheckState(null);
    } finally {
      setIsCheckingClaims(false);
    }
  }

  return (
    <section className={cn(showHeader ? "mt-4" : "mt-0", className)}>
      {showHeader ? (
        <div className="mb-2 flex items-center justify-between">
          <div className="text-[10px] uppercase tracking-[0.16em] text-muted-foreground">
            {t("panels.teamsDispatch.summary.header")}
          </div>
        </div>
      ) : null}

      <TeamsSummarySection
        activeTeamCount={activeTeamCount}
        activeView={activeView}
        error={error}
        isLoading={isLoading}
        isRefreshing={isRefreshing}
        onRefresh={onRefresh}
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
          selectedSummary={selectedSummary}
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
