import {
  RefreshCcwIcon,
} from "lucide-react";
import { lazy, Suspense, useEffect, useMemo, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { useRuntimeTeamDispatch } from "@/components/workspace/runtime-teams/use-runtime-team-dispatch";
import {
  ackRuntimeTeamMailboxMessage,
  checkRuntimeTeamPathClaims,
  getRuntimeTeamFinalSummary,
  getRuntimeTeamTaskGraph,
  listRuntimeTeamEvents,
  listRuntimeTeamMailbox,
  listRuntimeTeamPathClaims,
  listRuntimeTeamTasks,
  listRuntimeTeamTeammates,
  sendRuntimeTeamMailboxMessage,
  type RuntimeCheckTeamPathClaimsResponse,
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import {
  type ClaimCheckState,
  createEmptyDetails,
  parsePathLines,
  sortEvents,
  sortMailbox,
  sortPathClaims,
  sortTasks,
  sortTeammates,
  type TeamDetailsState,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";

const RuntimeTeamDetailsPanel = lazy(() =>
  import("@/components/workspace/runtime-teams/runtime-team-details-panel").then(
    (module) => ({
      default: module.RuntimeTeamDetailsPanel,
    }),
  ),
);
const RuntimeTeamDispatchPanel = lazy(() =>
  import("@/components/workspace/runtime-teams/runtime-team-dispatch-panel").then(
    (module) => ({
      default: module.RuntimeTeamDispatchPanel,
    }),
  ),
);

type RuntimeTeamsProps = {
  className?: string;
  error: string | null;
  isLoading: boolean;
  isRefreshing?: boolean;
  onRefresh?: () => void;
  showHeader?: boolean;
  summaries: RuntimeTeamSummaryEntry[];
  teams: RuntimeTeamRecord[];
};

type RuntimeTeamsView = "teams" | "dispatch";

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

  useEffect(() => {
    if (!selectedTeamId) {
      setDetails(createEmptyDetails());
      setDetailsError(null);
      setMailboxError(null);
      setClaimCheckError(null);
      setClaimCheckState(null);
      setMailboxBodyDraft("");
      setMailboxFromDraft("lead");
      setMailboxToDraft("*");
      setMailboxKindDraft("info");
      setMailboxTaskDraft("");
      return;
    }

    setMailboxError(null);
    setClaimCheckError(null);
    setClaimCheckState(null);
    setMailboxBodyDraft("");
    setMailboxFromDraft("lead");
    setMailboxToDraft("*");
    setMailboxKindDraft("info");
    setMailboxTaskDraft("");
    setReadPathDraft("");
    setWritePathDraft("");

    let cancelled = false;
    setIsDetailsLoading(true);
    setDetailsError(null);

    void (async () => {
      try {
        const [
          summaryResult,
          teammatesResult,
          tasksResult,
          graphResult,
          eventsResult,
          mailboxResult,
          pathClaimsResult,
        ] = await Promise.allSettled([
            getRuntimeTeamFinalSummary(selectedTeamId),
            listRuntimeTeamTeammates(selectedTeamId, { limit: 8 }),
            listRuntimeTeamTasks(selectedTeamId, {
              includeDependencies: true,
              includeDependents: true,
              limit: 12,
            }),
            getRuntimeTeamTaskGraph(selectedTeamId, { limit: 40 }),
            listRuntimeTeamEvents(selectedTeamId, { limit: 12 }),
            listRuntimeTeamMailbox(selectedTeamId, {
              includeBroadcast: true,
              limit: 10,
            }),
            listRuntimeTeamPathClaims(selectedTeamId, {
              activeOnly: true,
              limit: 10,
            }),
          ] as const);

        if (cancelled) {
          return;
        }

        const nextDetails = createEmptyDetails();
        const partialFailures: string[] = [];

        if (summaryResult.status === "fulfilled") {
          nextDetails.finalSummary = summaryResult.value.summary?.trim() || "";
        } else {
          partialFailures.push("final summary");
        }

        if (teammatesResult.status === "fulfilled") {
          nextDetails.teammates = sortTeammates(teammatesResult.value.teammates);
        } else {
          partialFailures.push("teammates");
        }

        if (tasksResult.status === "fulfilled") {
          nextDetails.tasks = sortTasks(tasksResult.value.tasks);
        } else {
          partialFailures.push("tasks");
        }

        if (graphResult.status === "fulfilled") {
          nextDetails.graph = graphResult.value;
        } else {
          partialFailures.push("task graph");
        }

        if (eventsResult.status === "fulfilled") {
          nextDetails.events = sortEvents(eventsResult.value.events);
        } else {
          partialFailures.push("events");
        }

        if (mailboxResult.status === "fulfilled") {
          nextDetails.mailbox = sortMailbox(mailboxResult.value.messages);
        } else {
          partialFailures.push("mailbox");
        }

        if (pathClaimsResult.status === "fulfilled") {
          nextDetails.pathClaims = sortPathClaims(pathClaimsResult.value.claims);
        } else {
          partialFailures.push("path claims");
        }

        setDetails(nextDetails);
        setDetailsError(
          partialFailures.length > 0
            ? `partially loaded team details: ${partialFailures.join(", ")}`
            : null,
        );
      } catch (fetchError) {
        if (cancelled) {
          return;
        }
        const message =
          fetchError instanceof Error
            ? fetchError.message
            : "failed to load runtime team details";
        setDetails(createEmptyDetails());
        setDetailsError(message);
      } finally {
        if (!cancelled) {
          setIsDetailsLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [selectedTeamId]);

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
          <div className="text-[10px] uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
            Runtime Teams
          </div>
        </div>
      ) : null}

      <div className="mb-3 flex flex-col gap-3 rounded-[0.95rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3.5 py-3 xl:flex-row xl:items-center xl:justify-between">
        <div className="flex flex-wrap gap-2">
          <Badge>{teams.length} teams</Badge>
          <Badge>{activeTeamCount} active</Badge>
          {selectedTeam ? (
            <Badge>{truncateIdentifier(selectedTeam.id, 18)}</Badge>
          ) : null}
          {activeView === "dispatch" ? (
            <Badge className="border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--accent-primary)]">
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
                ? "border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)] text-[var(--accent-secondary)]"
                : "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--muted-foreground)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft-hover)] hover:text-[var(--foreground)]",
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
                ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--accent-primary)]"
                : "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--muted-foreground)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft-hover)] hover:text-[var(--foreground)]",
            )}
          >
            Dispatch
          </button>
          {onRefresh ? (
            <button
              type="button"
              onClick={onRefresh}
              className="inline-flex items-center justify-center rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] p-1.5 text-[var(--muted-foreground)] transition hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft-hover)] hover:text-[var(--foreground)]"
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
        <div className="rounded-[0.9rem] border border-[#f0c77b]/18 bg-[#f0c77b]/8 px-3.5 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
          {error}
        </div>
      ) : null}

      {isLoading && teams.length === 0 ? (
        <div className="rounded-[0.9rem] border border-white/8 bg-white/4 px-3.5 py-3.5 text-sm text-[var(--muted-foreground)]">
          Loading runtime teams...
        </div>
      ) : null}

      {!isLoading && teams.length === 0 && !error ? (
        <div className="rounded-[0.9rem] border border-dashed border-white/10 px-3.5 py-3.5 text-sm text-[var(--muted-foreground)]">
          No runtime teams available.
        </div>
      ) : null}

      {activeView === "dispatch" ? (
        <Suspense fallback={<RuntimeTeamsPanelFallback label="dispatch view" />}>
          <RuntimeTeamDispatchPanel
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
            isDispatchMonitorLoading={isDispatchMonitorLoading}
            isDispatchReadinessLoading={isDispatchReadinessLoading}
            isDispatchingTask={isDispatchingTask}
            isProvisioningDispatch={isProvisioningDispatch}
            onDispatchTaskDeliverablesDraftChange={setDispatchTaskDeliverablesDraft}
            onDispatchTaskGoalDraftChange={setDispatchTaskGoalDraft}
            onDispatchTaskInputsDraftChange={setDispatchTaskInputsDraft}
            onDispatchTaskPriorityDraftChange={setDispatchTaskPriorityDraft}
            onDispatchTaskTitleDraftChange={setDispatchTaskTitleDraft}
            onDispatchTaskToTeams={() => void handleDispatchTaskToTeams()}
            onDispatchTemplateModeChange={setDispatchTemplateMode}
            onProvisionStrategyDraftChange={setProvisionStrategyDraft}
            onProvisionTeamCountDraftChange={setProvisionTeamCountDraft}
            onProvisionTeammateNamePrefixDraftChange={setProvisionTeammateNamePrefixDraft}
            onProvisionTeammateProfileDraftChange={setProvisionTeammateProfileDraft}
            onProvisionTeamsAndDispatch={() => void handleProvisionTeamsAndDispatch()}
            onProvisionUserPrefixDraftChange={setProvisionUserPrefixDraft}
            onProvisionWorkspaceDraftChange={setProvisionWorkspaceDraft}
            onRefreshDispatchMonitor={() => void handleRefreshDispatchMonitor()}
            onToggleDispatchTeam={toggleDispatchTeam}
            provisionStrategyDraft={provisionStrategyDraft}
            provisionTeamCountDraft={provisionTeamCountDraft}
            provisionTeammateNamePrefixDraft={provisionTeammateNamePrefixDraft}
            provisionTeammateProfileDraft={provisionTeammateProfileDraft}
            provisionUserPrefixDraft={provisionUserPrefixDraft}
            provisionWorkspaceDraft={provisionWorkspaceDraft}
            selectedDispatchTeamIds={selectedDispatchTeamIds}
            summaryMap={summaryMap}
            teams={teams}
          />
        </Suspense>
      ) : (
        <div className="grid gap-3 xl:grid-cols-[18rem_minmax(0,1fr)]">
          <aside className="rounded-[0.95rem] border border-[var(--border)] bg-[var(--surface-softer)] p-2.5">
            <div className="mb-2.5 flex items-center justify-between gap-3 px-0.5">
              <div>
                <div className="text-[10px] uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                  Team directory
                </div>
                <div className="mt-0.5 text-sm font-semibold text-[var(--foreground)]">
                  Select a team
                </div>
              </div>
              <Badge>{teams.length}</Badge>
            </div>

            {teams.length > 0 ? (
              <div className="space-y-1.5 xl:max-h-[calc(100vh-18rem)] xl:overflow-y-auto xl:pr-1">
                {teams.map((team) => {
                  const summary = summaryMap.get(team.id);
                  const isActive = team.id === selectedTeamId;
                  return (
                    <button
                      key={team.id}
                      type="button"
                      onClick={() => setSelectedTeamId(team.id)}
                      className={cn(
                        "w-full rounded-[0.8rem] border px-3 py-2.5 text-left transition",
                        isActive
                          ? "border-[#8fd0c6]/30 bg-[#8fd0c6]/10 shadow-[0_0_0_1px_rgba(143,208,198,0.12)]"
                          : "border-[var(--border)] bg-[var(--surface-soft)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft-hover)]",
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
            ) : (
              <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
                No teams available. Switch to `Dispatch` to provision runnable teams.
              </div>
            )}
          </aside>

          <div className="min-w-0">
            {selectedTeam ? (
              <Suspense fallback={<RuntimeTeamsPanelFallback label="team details" />}>
                <RuntimeTeamDetailsPanel
                  ackingMessageId={ackingMessageId}
                  claimCheckError={claimCheckError}
                  claimCheckState={claimCheckState}
                  details={details}
                  detailsError={detailsError}
                  graphEdgeCount={graphEdgeCount}
                  graphMissingCount={graphMissingCount}
                  isCheckingClaims={isCheckingClaims}
                  isDetailsLoading={isDetailsLoading}
                  isSendingMailbox={isSendingMailbox}
                  mailboxBodyDraft={mailboxBodyDraft}
                  mailboxError={mailboxError}
                  mailboxFromDraft={mailboxFromDraft}
                  mailboxKindDraft={mailboxKindDraft}
                  mailboxTaskDraft={mailboxTaskDraft}
                  mailboxToDraft={mailboxToDraft}
                  onAckMailboxMessage={(messageId) =>
                    void handleAckMailboxMessage(messageId)
                  }
                  onCheckPathClaims={() => void handleCheckPathClaims()}
                  onMailboxBodyDraftChange={setMailboxBodyDraft}
                  onMailboxFromDraftChange={setMailboxFromDraft}
                  onMailboxKindDraftChange={setMailboxKindDraft}
                  onMailboxTaskDraftChange={setMailboxTaskDraft}
                  onMailboxToDraftChange={setMailboxToDraft}
                  onReadPathDraftChange={setReadPathDraft}
                  onSendMailboxMessage={() => void handleSendMailboxMessage()}
                  onWritePathDraftChange={setWritePathDraft}
                  readPathDraft={readPathDraft}
                  selectedSummary={selectedSummary}
                  selectedTeam={selectedTeam}
                  visibleEvents={visibleEvents}
                  visibleMailbox={visibleMailbox}
                  visiblePathClaims={visiblePathClaims}
                  visibleTasks={visibleTasks}
                  visibleTeammates={visibleTeammates}
                  writePathDraft={writePathDraft}
                />
              </Suspense>
            ) : (
              <div className="rounded-[0.95rem] border border-dashed border-[var(--border)] bg-[var(--surface-softer)] px-5 py-8 text-center">
                <div className="text-sm font-semibold text-[var(--foreground)]">
                  No team selected
                </div>
                <p className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
                  Pick a team from the directory to inspect its snapshot, mailbox,
                  path claims, timeline, and final summary.
                </p>
              </div>
            )}
          </div>
        </div>
      )}
    </section>
  );
}

function RuntimeTeamsPanelFallback({ label }: { label: string }) {
  return (
    <div className="rounded-[0.95rem] border border-[var(--border)] bg-[var(--surface-softer)] px-4 py-3 text-sm text-[var(--muted-foreground)]">
      正在加载 {label}…
    </div>
  );
}

