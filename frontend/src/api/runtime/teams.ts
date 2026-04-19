import type {
  RuntimeAckTeamMailboxResponse,
  RuntimeCheckTeamPathClaimsRequest,
  RuntimeCheckTeamPathClaimsResponse,
  RuntimeCreateTeamRequest,
  RuntimeCreateTeamResponse,
  RuntimeCreateTeamTaskRequest,
  RuntimeCreateTeamTaskResponse,
  RuntimeSendTeamMailboxRequest,
  RuntimeSendTeamMailboxResponse,
  RuntimeTaskGraphQuery,
  RuntimeTaskGraphResponse,
  RuntimeTeamEventsQuery,
  RuntimeTeamEventsResponse,
  RuntimeTeamFinalSummaryResponse,
  RuntimeTeamMailboxQuery,
  RuntimeTeamMailboxResponse,
  RuntimeTeamPathClaimsQuery,
  RuntimeTeamPathClaimsResponse,
  RuntimeTeamSummariesQuery,
  RuntimeTeamSummariesResponse,
  RuntimeTeamTasksQuery,
  RuntimeTeamTasksResponse,
  RuntimeTeamTeammatesQuery,
  RuntimeTeammatesResponse,
  RuntimeTeamsQuery,
  RuntimeTeamsResponse,
  RuntimeUpsertTeammateRequest,
  RuntimeUpsertTeammateResponse,
} from "@/types/runtime";

import {
  buildRuntimeUrl,
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "./shared";

export async function createRuntimeTeam(
  request: RuntimeCreateTeamRequest,
): Promise<RuntimeCreateTeamResponse> {
  return fetchRuntimeJson<RuntimeCreateTeamResponse>(
    buildRuntimeUrl("/api/runtime/teams"),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
}

export async function listRuntimeTeams(
  query: RuntimeTeamsQuery = {},
): Promise<RuntimeTeamsResponse> {
  return fetchRuntimeJson<RuntimeTeamsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/teams", {
      limit: query.limit,
      status: query.status,
      workspace_id: query.workspaceId,
    }),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function listRuntimeTeamSummaries(
  query: RuntimeTeamSummariesQuery = {},
): Promise<RuntimeTeamSummariesResponse> {
  return fetchRuntimeJson<RuntimeTeamSummariesResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/teams/summary", {
      light: query.light,
      limit: query.limit,
      team_ids: query.teamIds?.join(","),
      include_mailbox: query.includeMailbox,
      include_path_claims: query.includePathClaims,
      include_teammate_states: query.includeTeammateStates,
    }),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function getRuntimeTeamFinalSummary(
  teamId: string,
): Promise<RuntimeTeamFinalSummaryResponse> {
  return fetchRuntimeJson<RuntimeTeamFinalSummaryResponse>(
    buildRuntimeUrl(
      `/api/runtime/teams/${encodeURIComponent(teamId)}/summary/final`,
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function listRuntimeTeamTeammates(
  teamId: string,
  query: RuntimeTeamTeammatesQuery = {},
): Promise<RuntimeTeammatesResponse> {
  return fetchRuntimeJson<RuntimeTeammatesResponse>(
    buildRuntimeUrlWithQuery(
      `/api/runtime/teams/${encodeURIComponent(teamId)}/teammates`,
      {
        limit: query.limit,
        state: query.state,
      },
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function upsertRuntimeTeammate(
  teamId: string,
  request: RuntimeUpsertTeammateRequest,
): Promise<RuntimeUpsertTeammateResponse> {
  return fetchRuntimeJson<RuntimeUpsertTeammateResponse>(
    buildRuntimeUrl(`/api/runtime/teams/${encodeURIComponent(teamId)}/teammates`),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
}

export async function listRuntimeTeamTasks(
  teamId: string,
  query: RuntimeTeamTasksQuery = {},
): Promise<RuntimeTeamTasksResponse> {
  return fetchRuntimeJson<RuntimeTeamTasksResponse>(
    buildRuntimeUrlWithQuery(
      `/api/runtime/teams/${encodeURIComponent(teamId)}/tasks`,
      {
        assignee: query.assignee,
        include_dependencies: query.includeDependencies,
        include_dependents: query.includeDependents,
        limit: query.limit,
        parent_task_id: query.parentTaskId,
        status: query.status?.join(","),
        task_ids: query.taskIds?.join(","),
      },
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function createRuntimeTeamTask(
  teamId: string,
  request: RuntimeCreateTeamTaskRequest,
): Promise<RuntimeCreateTeamTaskResponse> {
  return fetchRuntimeJson<RuntimeCreateTeamTaskResponse>(
    buildRuntimeUrl(`/api/runtime/teams/${encodeURIComponent(teamId)}/tasks`),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
}

export async function getRuntimeTeamTaskGraph(
  teamId: string,
  query: RuntimeTaskGraphQuery = {},
): Promise<RuntimeTaskGraphResponse> {
  return fetchRuntimeJson<RuntimeTaskGraphResponse>(
    buildRuntimeUrlWithQuery(
      `/api/runtime/teams/${encodeURIComponent(teamId)}/tasks/graph`,
      {
        assignee: query.assignee,
        include_external: query.includeExternal,
        limit: query.limit,
        parent_task_id: query.parentTaskId,
        status: query.status?.join(","),
        task_ids: query.taskIds?.join(","),
      },
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function listRuntimeTeamEvents(
  teamId: string,
  query: RuntimeTeamEventsQuery = {},
): Promise<RuntimeTeamEventsResponse> {
  return fetchRuntimeJson<RuntimeTeamEventsResponse>(
    buildRuntimeUrlWithQuery(
      `/api/runtime/teams/${encodeURIComponent(teamId)}/events`,
      {
        after: query.after,
        event_type: query.eventType,
        limit: query.limit,
        since: query.since,
        until: query.until,
      },
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function listRuntimeTeamMailbox(
  teamId: string,
  query: RuntimeTeamMailboxQuery = {},
): Promise<RuntimeTeamMailboxResponse> {
  return fetchRuntimeJson<RuntimeTeamMailboxResponse>(
    buildRuntimeUrlWithQuery(
      `/api/runtime/teams/${encodeURIComponent(teamId)}/mailbox`,
      {
        agent_id: query.agentId,
        from_agent: query.fromAgent,
        include_broadcast: query.includeBroadcast,
        kind: query.kind,
        limit: query.limit,
        mark_read: query.markRead,
        parent_task_id: query.parentTaskId,
        since: query.since,
        task_id: query.taskId,
        to_agent: query.toAgent,
        unread_only: query.unreadOnly,
      },
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function listRuntimeTeamPathClaims(
  teamId: string,
  query: RuntimeTeamPathClaimsQuery = {},
): Promise<RuntimeTeamPathClaimsResponse> {
  return fetchRuntimeJson<RuntimeTeamPathClaimsResponse>(
    buildRuntimeUrlWithQuery(
      `/api/runtime/teams/${encodeURIComponent(teamId)}/path-claims`,
      {
        active_only: query.activeOnly,
        as_of: query.asOf,
        limit: query.limit,
        mode: query.mode,
        owner_agent_id: query.ownerAgentId,
        task_id: query.taskId,
      },
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function sendRuntimeTeamMailboxMessage(
  teamId: string,
  request: RuntimeSendTeamMailboxRequest,
): Promise<RuntimeSendTeamMailboxResponse> {
  return fetchRuntimeJson<RuntimeSendTeamMailboxResponse>(
    buildRuntimeUrl(`/api/runtime/teams/${encodeURIComponent(teamId)}/mailbox`),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
}

export async function ackRuntimeTeamMailboxMessage(
  teamId: string,
  messageId: string,
  agentId?: string,
): Promise<RuntimeAckTeamMailboxResponse> {
  const queryString =
    agentId && agentId.trim()
      ? `?${new URLSearchParams({ agent_id: agentId.trim() }).toString()}`
      : "";

  return fetchRuntimeJson<RuntimeAckTeamMailboxResponse>(
    buildRuntimeUrl(
      `/api/runtime/teams/${encodeURIComponent(teamId)}/mailbox/${encodeURIComponent(messageId)}/ack${queryString}`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function checkRuntimeTeamPathClaims(
  teamId: string,
  request: RuntimeCheckTeamPathClaimsRequest,
): Promise<RuntimeCheckTeamPathClaimsResponse> {
  return fetchRuntimeJson<RuntimeCheckTeamPathClaimsResponse>(
    buildRuntimeUrl(
      `/api/runtime/teams/${encodeURIComponent(teamId)}/path-claims/check`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        read_paths: request.readPaths,
        write_paths: request.writePaths,
      }),
    },
  );
}
