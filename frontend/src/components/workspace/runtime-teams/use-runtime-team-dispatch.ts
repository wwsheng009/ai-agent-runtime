// 由 components/workspace/runtime-teams/use-runtime-team-dispatch.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect, useEffectEvent, useMemo, useState } from "react";

import {
  createRuntimeSession,
  createRuntimeTeam,
  createRuntimeTeamTask,
  upsertRuntimeTeammate,
  type RuntimeCreateSessionResponse,
  type RuntimeCreateTeamResponse,
  type RuntimeCreateTeamTaskResponse,
} from "@/lib/runtime-api";
import {
  countDispatchMonitorStatuses,
  shouldPollDispatchMonitor,
  sortDispatchMonitor,
  uniqueStrings,
  type DispatchMonitorEntry,
  type DispatchTemplateMode,
  type MultiTeamDispatchResult,
} from "@/components/workspace/runtime-teams/shared";
import {
  buildDispatchTaskRequest,
  buildRoleAwareDispatchRequest,
  resolveSelectedDispatchTeamIds,
} from "@/components/workspace/runtime-teams/use-runtime-team-dispatch/dispatch-requests";
import { buildDispatchMonitorEntries } from "@/components/workspace/runtime-teams/use-runtime-team-dispatch/dispatch-monitor";
import type {
  UseRuntimeTeamDispatchOptions,
  UseRuntimeTeamDispatchReturn,
} from "@/components/workspace/runtime-teams/use-runtime-team-dispatch/types";
import { useDispatchTeamReadiness } from "@/components/workspace/runtime-teams/use-runtime-team-dispatch/use-dispatch-team-readiness";

export type { UseRuntimeTeamDispatchReturn } from "@/components/workspace/runtime-teams/use-runtime-team-dispatch/types";
export {
  buildDispatchTaskRequest,
  resolveSelectedDispatchTeamIds,
} from "@/components/workspace/runtime-teams/use-runtime-team-dispatch/dispatch-requests";
export { buildDispatchMailboxPreview } from "@/components/workspace/runtime-teams/use-runtime-team-dispatch/dispatch-monitor";

export function useRuntimeTeamDispatch({
  onRefresh,
  onRefreshSelectedTeamTasksAndEvents,
  selectedTeamId,
  selectedTeamWorkspaceId,
  teams,
}: UseRuntimeTeamDispatchOptions): UseRuntimeTeamDispatchReturn {
  const [selectedDispatchTeamIds, setSelectedDispatchTeamIds] = useState<string[]>([]);
  const { dispatchTeamReadiness, isDispatchReadinessLoading } =
    useDispatchTeamReadiness(teams);
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
