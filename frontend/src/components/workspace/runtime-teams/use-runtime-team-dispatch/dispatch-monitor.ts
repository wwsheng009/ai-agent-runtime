// 由 components/workspace/runtime-teams/use-runtime-team-dispatch.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  getRuntimeTeamReadiness,
  listRuntimeTeamEvents,
  listRuntimeTeamMailbox,
  listRuntimeTeamTasks,
  listRuntimeTeamTeammates,
  type RuntimeTeamMailboxMessage,
  type RuntimeTeamRecord,
} from "@/lib/runtime-api";
import {
  type DispatchMonitorEntry,
  type DispatchTeamReadiness,
  type MultiTeamDispatchResult,
} from "@/components/workspace/runtime-teams/shared";

export async function loadDispatchTeamReadiness(
  teams: RuntimeTeamRecord[],
  signal?: AbortSignal,
): Promise<Record<string, DispatchTeamReadiness>> {
  const settled = await Promise.allSettled(
    teams.map(async (team) => {
      const [response, teammatesResponse] = await Promise.all([
        getRuntimeTeamReadiness(team.id, { signal }),
        listRuntimeTeamTeammates(team.id, { limit: 24, signal }),
      ]);
      const runnableTeammates = teammatesResponse.teammates.filter((mate) => {
        const state = (mate.state || "").trim().toLowerCase();
        return Boolean(mate.session_id?.trim()) && state !== "offline";
      });
      const blockers = [
        ...(response.preflight.blockers ?? []),
        ...(response.preflight.findings?.filter(
          (item) => item.severity === "blocker",
        ) ?? []),
      ].filter((finding, index, all) => {
        const key = [
          finding.code,
          finding.team_id ?? "",
          finding.teammate_id ?? "",
          finding.task_id ?? "",
          finding.message,
        ].join("\u0000");
        return (
          all.findIndex((candidate) =>
            [
              candidate.code,
              candidate.team_id ?? "",
              candidate.teammate_id ?? "",
              candidate.task_id ?? "",
              candidate.message,
            ].join("\u0000") === key,
          ) === index
        );
      });
      // Dispatch creates a task; it does not start a run. An empty task graph
      // is therefore the expected precondition for the first dispatch, but
      // every other Runtime blocker remains authoritative.
      const dispatchBlockers = blockers.filter(
        (finding) => finding.code !== "team_tasks_empty",
      );
      const emptyTaskGraphOnly =
        response.readiness === false &&
        response.preflight.ready === false &&
        response.preflight.blocker_count === 1 &&
        blockers.length === 1 &&
        blockers[0].code === "team_tasks_empty";
      const preflightAllowsDispatch =
        (response.readiness === true &&
          response.preflight.ready === true &&
          response.preflight.blocker_count === 0 &&
          blockers.length === 0) ||
        emptyTaskGraphOnly;
      const teamStatus = (response.team_status ?? team.status ?? "")
        .trim()
        .toLowerCase();
      const teamIsActive = teamStatus === "active";
      const reason = !teamIsActive
        ? "team is not active"
        : runnableTeammates.length === 0
          ? "no runnable teammate session"
          : !preflightAllowsDispatch
            ? (dispatchBlockers[0]?.message ??
              "Runtime preflight did not confirm task dispatch readiness")
            : "Runtime preflight permits task dispatch";
      const readiness: DispatchTeamReadiness = {
        executable:
          teamIsActive &&
          runnableTeammates.length > 0 &&
          preflightAllowsDispatch,
        reason,
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
          : "failed to inspect Runtime preflight",
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

export async function buildDispatchMonitorEntries(
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
