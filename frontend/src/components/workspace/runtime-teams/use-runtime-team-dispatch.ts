import { useEffect, useEffectEvent, useMemo, useState } from "react";

import {
  createRuntimeSession,
  createRuntimeTeam,
  createRuntimeTeamTask,
  listRuntimeTeamEvents,
  listRuntimeTeamMailbox,
  listRuntimeTeamTasks,
  listRuntimeTeamTeammates,
  upsertRuntimeTeammate,
  type RuntimeCreateSessionResponse,
  type RuntimeCreateTeamResponse,
  type RuntimeCreateTeamTaskResponse,
  type RuntimeTeamMailboxMessage,
  type RuntimeTeamRecord,
} from "@/lib/runtime-api";
import {
  countDispatchMonitorStatuses,
  type DispatchMonitorEntry,
  type DispatchTeamReadiness,
  type DispatchTemplateMode,
  type MultiTeamDispatchResult,
  normalizeTaskTitle,
  parsePathLines,
  resolveDispatchRolePlan,
  shouldPollDispatchMonitor,
  sortDispatchMonitor,
  uniqueStrings,
} from "@/components/workspace/runtime-teams/shared";

type UseRuntimeTeamDispatchOptions = {
  onRefresh?: () => void;
  onRefreshSelectedTeamTasksAndEvents?: (teamId: string) => Promise<void>;
  selectedTeamId: string;
  selectedTeamWorkspaceId?: string;
  teams: RuntimeTeamRecord[];
};

type DispatchTaskRequest = {
  deliverables: string[];
  goal: string;
  inputs: string[];
  priority: number;
  status: "ready";
  title: string;
};

type DispatchTaskDraftState = {
  deliverablesDraft: string;
  goalDraft: string;
  inputsDraft: string;
  priorityDraft: string;
  titleDraft: string;
};

type DispatchTeamIdentifier = Pick<RuntimeTeamRecord, "id">;

export type UseRuntimeTeamDispatchReturn = {
  dispatchMonitor: DispatchMonitorEntry[];
  dispatchMonitorCounts: Record<string, number>;
  dispatchMonitorError: string | null;
  dispatchTaskDeliverablesDraft: string;
  dispatchTaskError: string | null;
  dispatchTaskGoalDraft: string;
  dispatchTaskInputsDraft: string;
  dispatchTaskPriorityDraft: string;
  dispatchTaskResults: MultiTeamDispatchResult[];
  dispatchTaskTitleDraft: string;
  dispatchTeamReadiness: Record<string, DispatchTeamReadiness>;
  dispatchTemplateMode: DispatchTemplateMode;
  isDispatchMonitorLoading: boolean;
  isDispatchReadinessLoading: boolean;
  isDispatchingTask: boolean;
  isProvisioningDispatch: boolean;
  onDispatchTaskDeliverablesDraftChange: (value: string) => void;
  onDispatchTaskGoalDraftChange: (value: string) => void;
  onDispatchTaskInputsDraftChange: (value: string) => void;
  onDispatchTaskPriorityDraftChange: (value: string) => void;
  onDispatchTaskTitleDraftChange: (value: string) => void;
  onDispatchTaskToTeams: () => Promise<void>;
  onDispatchTemplateModeChange: (mode: DispatchTemplateMode) => void;
  onProvisionStrategyDraftChange: (value: string) => void;
  onProvisionTeamCountDraftChange: (value: string) => void;
  onProvisionTeammateNamePrefixDraftChange: (value: string) => void;
  onProvisionTeammateProfileDraftChange: (value: string) => void;
  onProvisionTeamsAndDispatch: () => Promise<void>;
  onProvisionUserPrefixDraftChange: (value: string) => void;
  onProvisionWorkspaceDraftChange: (value: string) => void;
  onRefreshDispatchMonitor: () => Promise<void>;
  onToggleDispatchTeam: (teamId: string) => void;
  provisionStrategyDraft: string;
  provisionTeamCountDraft: string;
  provisionTeammateNamePrefixDraft: string;
  provisionTeammateProfileDraft: string;
  provisionUserPrefixDraft: string;
  provisionWorkspaceDraft: string;
  selectedDispatchTeamIds: string[];
};

export function buildDispatchTaskRequest(
  drafts: DispatchTaskDraftState,
): { error: string | null; request: DispatchTaskRequest | null } {
  const goal = drafts.goalDraft.trim();
  const title = normalizeTaskTitle(drafts.titleDraft, goal);
  if (!goal && !title) {
    return {
      error: "enter a task title or goal before dispatching",
      request: null,
    };
  }

  const priority = Number.parseInt(drafts.priorityDraft, 10);
  return {
    error: null,
    request: {
      deliverables: parsePathLines(drafts.deliverablesDraft),
      goal: goal || title,
      inputs: parsePathLines(drafts.inputsDraft),
      priority: Number.isNaN(priority) ? 50 : priority,
      status: "ready",
      title,
    },
  };
}

export function resolveSelectedDispatchTeamIds(
  current: string[],
  teams: DispatchTeamIdentifier[],
): string[] {
  const availableIds = new Set(teams.map((team) => team.id));
  const filtered = current.filter((id) => availableIds.has(id));
  if (filtered.length > 0) {
    return filtered;
  }

  return teams.map((team) => team.id);
}

function buildRoleAwareDispatchRequest(
  baseRequest: DispatchTaskRequest,
  mode: DispatchTemplateMode,
  index: number,
) {
  const role = resolveDispatchRolePlan(mode, index);
  if (mode === "mirror") {
    return {
      request: baseRequest,
      role,
    };
  }

  return {
    request: {
      ...baseRequest,
      deliverables: uniqueStrings([
        ...(baseRequest.deliverables ?? []),
        ...role.deliverables,
      ]),
      goal: uniqueStrings([baseRequest.goal, role.goalInstruction || ""]).join("\n\n"),
      inputs: uniqueStrings([...(baseRequest.inputs ?? []), ...role.inputHints]),
      title: `${role.label}: ${baseRequest.title}`,
    },
    role,
  };
}

async function loadDispatchTeamReadiness(
  teams: RuntimeTeamRecord[],
): Promise<Record<string, DispatchTeamReadiness>> {
  const settled = await Promise.allSettled(
    teams.map(async (team) => {
      const teammatesResponse = await listRuntimeTeamTeammates(team.id, { limit: 24 });
      const runnableTeammates = teammatesResponse.teammates.filter((mate) => {
        const state = (mate.state || "").trim().toLowerCase();
        return Boolean(mate.session_id?.trim()) && state !== "offline";
      });
      const readiness: DispatchTeamReadiness = {
        executable:
          (team.status || "").trim().toLowerCase() === "active" &&
          runnableTeammates.length > 0,
        reason:
          (team.status || "").trim().toLowerCase() !== "active"
            ? "team is not active"
            : runnableTeammates.length === 0
              ? "no runnable teammate session"
              : `${runnableTeammates.length} runnable teammates`,
        runnableTeammates: runnableTeammates.length,
        totalTeammates: teammatesResponse.teammates.length,
      };
      return { readiness, teamId: team.id };
    }),
  );

  const nextMap: Record<string, DispatchTeamReadiness> = {};
  settled.forEach((item, index) => {
    const teamId = teams[index].id;
    if (item.status === "fulfilled") {
      nextMap[teamId] = item.value.readiness;
      return;
    }
    nextMap[teamId] = {
      executable: false,
      reason:
        item.reason instanceof Error
          ? item.reason.message
          : "failed to inspect team readiness",
      runnableTeammates: 0,
      totalTeammates: 0,
    };
  });

  return nextMap;
}

export function buildDispatchMailboxPreview(
  messages: Pick<RuntimeTeamMailboxMessage, "body" | "kind">[],
) {
  return messages
    .slice(0, 2)
    .map((message) => message.body.trim() || message.kind || "message");
}

async function buildDispatchMonitorEntries(
  results: MultiTeamDispatchResult[],
): Promise<DispatchMonitorEntry[]> {
  const createdResults = results.filter(
    (item): item is MultiTeamDispatchResult & { status: "created"; taskId: string } =>
      item.status === "created" && Boolean(item.taskId),
  );
  if (createdResults.length === 0) {
    return [];
  }

  const settled = await Promise.allSettled(
    createdResults.map(async (item) => {
      const [tasksResponse, eventsResponse, mailboxResponse] = await Promise.all([
        listRuntimeTeamTasks(item.teamId, {
          includeDependencies: true,
          includeDependents: true,
          taskIds: [item.taskId],
        }),
        listRuntimeTeamEvents(item.teamId, { limit: 12 }),
        listRuntimeTeamMailbox(item.teamId, {
          includeBroadcast: true,
          limit: 8,
          taskId: item.taskId,
        }),
      ]);
      const task = tasksResponse.tasks.find((entry) => entry.id === item.taskId);
      const relatedEvent = eventsResponse.events.find((event) => {
        const payloadTaskID = event.payload?.task_id;
        return typeof payloadTaskID === "string" && payloadTaskID === item.taskId;
      });
      return {
        assignee: task?.assignee,
        lastEventType: relatedEvent?.type,
        mailboxPreview: buildDispatchMailboxPreview(mailboxResponse.messages),
        status: task?.status || "unknown",
        summary: task?.summary,
        taskId: item.taskId,
        teamId: item.teamId,
        updatedAt: task?.updated_at || relatedEvent?.timestamp,
      } satisfies DispatchMonitorEntry;
    }),
  );

  return settled.map((item, index) => {
    const fallback = createdResults[index];
    if (item.status === "fulfilled") {
      return item.value;
    }
    return {
      error:
        item.reason instanceof Error
          ? item.reason.message
          : "failed to load dispatch monitor entry",
      mailboxPreview: [],
      status: "unknown",
      taskId: fallback.taskId,
      teamId: fallback.teamId,
    } satisfies DispatchMonitorEntry;
  });
}

export function useRuntimeTeamDispatch({
  onRefresh,
  onRefreshSelectedTeamTasksAndEvents,
  selectedTeamId,
  selectedTeamWorkspaceId,
  teams,
}: UseRuntimeTeamDispatchOptions): UseRuntimeTeamDispatchReturn {
  const [selectedDispatchTeamIds, setSelectedDispatchTeamIds] = useState<string[]>([]);
  const [dispatchTeamReadiness, setDispatchTeamReadiness] = useState<
    Record<string, DispatchTeamReadiness>
  >({});
  const [isDispatchReadinessLoading, setIsDispatchReadinessLoading] = useState(false);
  const [dispatchTemplateMode, setDispatchTemplateMode] =
    useState<DispatchTemplateMode>("review_implement_verify");
  const [dispatchTaskTitleDraft, setDispatchTaskTitleDraft] = useState("");
  const [dispatchTaskGoalDraft, setDispatchTaskGoalDraft] = useState("");
  const [dispatchTaskInputsDraft, setDispatchTaskInputsDraft] = useState("");
  const [dispatchTaskDeliverablesDraft, setDispatchTaskDeliverablesDraft] = useState("");
  const [dispatchTaskPriorityDraft, setDispatchTaskPriorityDraft] = useState("50");
  const [isDispatchingTask, setIsDispatchingTask] = useState(false);
  const [isProvisioningDispatch, setIsProvisioningDispatch] = useState(false);
  const [provisionTeamCountDraft, setProvisionTeamCountDraft] = useState("2");
  const [provisionWorkspaceDraft, setProvisionWorkspaceDraft] = useState("fanout-workspace");
  const [provisionStrategyDraft, setProvisionStrategyDraft] = useState("parallel-fanout");
  const [provisionUserPrefixDraft, setProvisionUserPrefixDraft] = useState("fanout-user");
  const [provisionTeammateNamePrefixDraft, setProvisionTeammateNamePrefixDraft] =
    useState("Fanout Worker");
  const [provisionTeammateProfileDraft, setProvisionTeammateProfileDraft] =
    useState("parallel execution worker");
  const [dispatchTaskError, setDispatchTaskError] = useState<string | null>(null);
  const [dispatchTaskResults, setDispatchTaskResults] = useState<MultiTeamDispatchResult[]>([]);
  const [dispatchMonitor, setDispatchMonitor] = useState<DispatchMonitorEntry[]>([]);
  const [dispatchMonitorError, setDispatchMonitorError] = useState<string | null>(null);
  const [isDispatchMonitorLoading, setIsDispatchMonitorLoading] = useState(false);

  useEffect(() => {
    setSelectedDispatchTeamIds((current) => {
      return resolveSelectedDispatchTeamIds(current, teams);
    });
  }, [teams]);

  useEffect(() => {
    if (teams.length === 0) {
      setDispatchTeamReadiness({});
      setIsDispatchReadinessLoading(false);
      return;
    }

    let cancelled = false;
    setIsDispatchReadinessLoading(true);

    void loadDispatchTeamReadiness(teams)
      .then((nextMap) => {
        if (cancelled) {
          return;
        }
        setDispatchTeamReadiness(nextMap);
      })
      .finally(() => {
        if (!cancelled) {
          setIsDispatchReadinessLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [teams]);

  useEffect(() => {
    if (
      selectedTeamWorkspaceId &&
      (!provisionWorkspaceDraft.trim() || provisionWorkspaceDraft === "fanout-workspace")
    ) {
      setProvisionWorkspaceDraft(selectedTeamWorkspaceId);
    }
  }, [provisionWorkspaceDraft, selectedTeamWorkspaceId]);

  useEffect(() => {
    setDispatchTaskError(null);
    setDispatchTaskResults([]);
    setDispatchMonitor([]);
    setDispatchMonitorError(null);
  }, [selectedTeamId]);

  const dispatchMonitorCounts = useMemo(
    () => countDispatchMonitorStatuses(dispatchMonitor),
    [dispatchMonitor],
  );

  async function refreshDispatchMonitor(results: MultiTeamDispatchResult[]) {
    if (!results.some((item) => item.status === "created" && Boolean(item.taskId))) {
      setDispatchMonitor([]);
      setDispatchMonitorError(null);
      setIsDispatchMonitorLoading(false);
      return;
    }

    setIsDispatchMonitorLoading(true);
    setDispatchMonitorError(null);
    try {
      const entries = await buildDispatchMonitorEntries(results);
      setDispatchMonitor(sortDispatchMonitor(entries));
    } catch (monitorError) {
      setDispatchMonitor([]);
      setDispatchMonitorError(
        monitorError instanceof Error
          ? monitorError.message
          : "failed to refresh dispatch monitor",
      );
    } finally {
      setIsDispatchMonitorLoading(false);
    }
  }

  const pollDispatchMonitor = useEffectEvent(() => {
    void refreshDispatchMonitor(dispatchTaskResults);
  });

  useEffect(() => {
    if (
      !shouldPollDispatchMonitor(
        dispatchMonitor,
        dispatchTaskResults,
        isDispatchMonitorLoading,
      )
    ) {
      return;
    }

    const timer = window.setTimeout(() => {
      pollDispatchMonitor();
    }, 4000);

    return () => {
      window.clearTimeout(timer);
    };
  }, [dispatchMonitor, dispatchTaskResults, isDispatchMonitorLoading]);

  function toggleDispatchTeam(teamId: string) {
    setSelectedDispatchTeamIds((current) =>
      current.includes(teamId)
        ? current.filter((id) => id !== teamId)
        : [...current, teamId],
    );
  }

  async function handleDispatchTaskToTeams() {
    const { error: requestError, request } = buildDispatchTaskRequest({
      deliverablesDraft: dispatchTaskDeliverablesDraft,
      goalDraft: dispatchTaskGoalDraft,
      inputsDraft: dispatchTaskInputsDraft,
      priorityDraft: dispatchTaskPriorityDraft,
      titleDraft: dispatchTaskTitleDraft,
    });
    if (!request) {
      setDispatchTaskError(requestError);
      setDispatchTaskResults([]);
      return;
    }
    if (selectedDispatchTeamIds.length === 0) {
      setDispatchTaskError("select at least one team");
      setDispatchTaskResults([]);
      return;
    }

    setIsDispatchingTask(true);
    setDispatchTaskError(null);
    setDispatchTaskResults([]);
    setDispatchMonitor([]);
    setDispatchMonitorError(null);

    try {
      const preflightFailures: MultiTeamDispatchResult[] = [];
      const executableTeamIds = selectedDispatchTeamIds.filter((teamId) => {
        const team = teams.find((item) => item.id === teamId);
        const readiness = dispatchTeamReadiness[teamId];
        if (!team) {
          preflightFailures.push({
            error: "team not found in current runtime snapshot",
            status: "failed",
            teamId,
          });
          return false;
        }
        if ((team.status || "").trim().toLowerCase() !== "active") {
          preflightFailures.push({
            error: "team is not active",
            status: "failed",
            teamId,
          });
          return false;
        }
        if (!readiness?.executable) {
          preflightFailures.push({
            error: readiness?.reason || "team is not executable",
            status: "failed",
            teamId,
          });
          return false;
        }
        return true;
      });

      if (executableTeamIds.length === 0) {
        setDispatchTaskError("no executable teams selected");
        setDispatchTaskResults(preflightFailures);
        return;
      }

      const settled = await Promise.allSettled(
        executableTeamIds.map(async (teamId, index) => {
          const { request: roleRequest } = buildRoleAwareDispatchRequest(
            request,
            dispatchTemplateMode,
            index,
          );
          const response: RuntimeCreateTeamTaskResponse = await createRuntimeTeamTask(
            teamId,
            roleRequest,
          );
          return {
            status: "created" as const,
            taskId: response.task.id,
            teamId,
          };
        }),
      );

      const createResults: MultiTeamDispatchResult[] = settled.map((item, index) => {
        const teamId = executableTeamIds[index];
        if (item.status === "fulfilled") {
          return item.value;
        }
        return {
          error:
            item.reason instanceof Error ? item.reason.message : "failed to create task",
          status: "failed" as const,
          teamId,
        };
      });
      const results: MultiTeamDispatchResult[] = [...createResults, ...preflightFailures];

      setDispatchTaskResults(results);
      await refreshDispatchMonitor(results);
      const failedCount = results.filter((item) => item.status === "failed").length;
      if (failedCount > 0) {
        setDispatchTaskError(`${failedCount} team dispatches failed`);
      }

      onRefresh?.();
      if (selectedTeamId && executableTeamIds.includes(selectedTeamId)) {
        await onRefreshSelectedTeamTasksAndEvents?.(selectedTeamId);
      }

      if (results.some((item) => item.status === "created")) {
        setDispatchTaskTitleDraft("");
        setDispatchTaskGoalDraft("");
        setDispatchTaskInputsDraft("");
        setDispatchTaskDeliverablesDraft("");
      }
    } catch (actionError) {
      setDispatchTaskError(
        actionError instanceof Error
          ? actionError.message
          : "failed to dispatch task to teams",
      );
      setDispatchTaskResults([]);
    } finally {
      setIsDispatchingTask(false);
    }
  }

  async function handleProvisionTeamsAndDispatch() {
    const { error: requestError, request } = buildDispatchTaskRequest({
      deliverablesDraft: dispatchTaskDeliverablesDraft,
      goalDraft: dispatchTaskGoalDraft,
      inputsDraft: dispatchTaskInputsDraft,
      priorityDraft: dispatchTaskPriorityDraft,
      titleDraft: dispatchTaskTitleDraft,
    });
    if (!request) {
      setDispatchTaskError(requestError);
      setDispatchTaskResults([]);
      return;
    }

    const teamCount = Number.parseInt(provisionTeamCountDraft, 10);
    if (Number.isNaN(teamCount) || teamCount <= 0) {
      setDispatchTaskError("enter a valid team count");
      setDispatchTaskResults([]);
      return;
    }

    const workspaceId = provisionWorkspaceDraft.trim() || "fanout-workspace";
    const strategy = provisionStrategyDraft.trim() || "parallel-fanout";
    const userPrefix = provisionUserPrefixDraft.trim() || "fanout-user";
    const teammateNamePrefix =
      provisionTeammateNamePrefixDraft.trim() || "Fanout Worker";
    const teammateProfile =
      provisionTeammateProfileDraft.trim() || "parallel execution worker";
    const batchStamp = Date.now().toString(36);

    setIsProvisioningDispatch(true);
    setDispatchTaskError(null);
    setDispatchTaskResults([]);
    setDispatchMonitor([]);
    setDispatchMonitorError(null);

    try {
      const settled = await Promise.allSettled(
        Array.from({ length: teamCount }, async (_, index) => {
          const ordinal = index + 1;
          const { request: roleRequest, role } = buildRoleAwareDispatchRequest(
            request,
            dispatchTemplateMode,
            index,
          );
          const leadSession: RuntimeCreateSessionResponse = await createRuntimeSession({
            title: `${workspaceId} ${role.label.toLowerCase()} lead ${ordinal}`,
            user_id: `${userPrefix}-lead-${batchStamp}-${ordinal}`,
          });
          const workerSession: RuntimeCreateSessionResponse = await createRuntimeSession({
            title: `${workspaceId} ${role.label.toLowerCase()} worker ${ordinal}`,
            user_id: `${userPrefix}-worker-${batchStamp}-${ordinal}`,
          });
          const teamResponse: RuntimeCreateTeamResponse = await createRuntimeTeam({
            lead_session_id: leadSession.session.id,
            max_teammates: 1,
            max_writers: 1,
            status: "active",
            strategy:
              dispatchTemplateMode === "mirror" || !role.strategySuffix
                ? strategy
                : `${strategy}:${role.strategySuffix}`,
            workspace_id: workspaceId,
          });
          const teammateID = `worker-${batchStamp}-${ordinal}`;
          await upsertRuntimeTeammate(teamResponse.team.id, {
            id: teammateID,
            name:
              dispatchTemplateMode === "mirror"
                ? `${teammateNamePrefix} ${ordinal}`
                : `${teammateNamePrefix} ${ordinal} (${role.label})`,
            profile: uniqueStrings([
              teammateProfile,
              role.teammateProfileSuffix || "",
            ]).join(" | "),
            session_id: workerSession.session.id,
            state: "idle",
          });
          const taskResponse: RuntimeCreateTeamTaskResponse = await createRuntimeTeamTask(
            teamResponse.team.id,
            roleRequest,
          );
          return {
            status: "created" as const,
            taskId: taskResponse.task.id,
            teamId: teamResponse.team.id,
          };
        }),
      );

      const results: MultiTeamDispatchResult[] = settled.map((item, index) => {
        if (item.status === "fulfilled") {
          return item.value;
        }
        return {
          error:
            item.reason instanceof Error
              ? item.reason.message
              : "failed to provision and dispatch",
          status: "failed" as const,
          teamId: `new-team-${index + 1}`,
        };
      });

      const createdTeamIds = results
        .filter((item) => item.status === "created")
        .map((item) => item.teamId);

      setDispatchTaskResults(results);
      await refreshDispatchMonitor(results);
      if (createdTeamIds.length > 0) {
        setSelectedDispatchTeamIds(createdTeamIds);
      }

      const failedCount = results.filter((item) => item.status === "failed").length;
      if (failedCount > 0) {
        setDispatchTaskError(`${failedCount} provisioned team dispatches failed`);
      }

      onRefresh?.();

      if (createdTeamIds.length > 0) {
        setDispatchTaskTitleDraft("");
        setDispatchTaskGoalDraft("");
        setDispatchTaskInputsDraft("");
        setDispatchTaskDeliverablesDraft("");
      }
    } catch (actionError) {
      setDispatchTaskError(
        actionError instanceof Error
          ? actionError.message
          : "failed to provision runnable teams and dispatch task",
      );
      setDispatchTaskResults([]);
    } finally {
      setIsProvisioningDispatch(false);
    }
  }

  async function handleRefreshDispatchMonitor() {
    await refreshDispatchMonitor(dispatchTaskResults);
  }

  return {
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
  };
}
