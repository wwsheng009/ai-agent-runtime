import type {
  RuntimeCreateSessionRequest,
  RuntimeCreateSessionResponse,
  RuntimeSessionBacktrackAuditResponse,
  RuntimeSessionBacktrackRequest,
  RuntimeSessionBacktrackResponse,
  RuntimeSessionCheckpointFilesResponse,
  RuntimeSessionCheckpointPreviewMode,
  RuntimeSessionCheckpointPreviewResponse,
  RuntimeSessionCheckpointRestoreResponse,
  RuntimeSessionCheckpointsQuery,
  RuntimeSessionCheckpointsResponse,
  RuntimeSessionPlanMode,
  RuntimeSessionPlanModeUpdateRequest,
  RuntimeSessionRecord,
  RuntimeSessionTurnsResponse,
  RuntimeSessionUsersResponse,
  RuntimeSessionsQuery,
  RuntimeSessionsResponse,
  SessionHistoryResponse,
} from "@/types/runtime";

import {
  buildRuntimeUrl,
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "./shared";

export async function getSessionHistory(
  sessionId: string,
): Promise<SessionHistoryResponse> {
  return fetchRuntimeJson<SessionHistoryResponse>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/history`),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function createRuntimeSession(
  request: RuntimeCreateSessionRequest,
): Promise<RuntimeCreateSessionResponse> {
  return fetchRuntimeJson<RuntimeCreateSessionResponse>(
    buildRuntimeUrl("/api/runtime/sessions"),
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

export async function listRuntimeSessions(
  query: RuntimeSessionsQuery = {},
): Promise<RuntimeSessionsResponse> {
  return fetchRuntimeJson<RuntimeSessionsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/sessions", {
      user_id: query.userId,
    }),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function listRuntimeSessionUsers(): Promise<RuntimeSessionUsersResponse> {
  return fetchRuntimeJson<RuntimeSessionUsersResponse>(
    buildRuntimeUrl("/api/runtime/sessions/users"),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function getRuntimeSession(
  sessionId: string,
): Promise<{ session: RuntimeSessionRecord }> {
  return fetchRuntimeJson<{ session: RuntimeSessionRecord }>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}`),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function listSessionCheckpoints(
  sessionId: string,
  query: RuntimeSessionCheckpointsQuery = {},
): Promise<RuntimeSessionCheckpointsResponse> {
  return fetchRuntimeJson<RuntimeSessionCheckpointsResponse>(
    buildRuntimeUrlWithQuery(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/checkpoints`,
      {
        limit: query.limit,
        offset: query.offset,
      },
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function getSessionCheckpointFiles(
  sessionId: string,
  checkpointId: string,
): Promise<RuntimeSessionCheckpointFilesResponse> {
  return fetchRuntimeJson<RuntimeSessionCheckpointFilesResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/checkpoints/${encodeURIComponent(checkpointId)}/files`,
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function previewSessionCheckpoint(
  sessionId: string,
  checkpointId: string,
  mode: RuntimeSessionCheckpointPreviewMode = "both",
): Promise<RuntimeSessionCheckpointPreviewResponse> {
  return fetchRuntimeJson<RuntimeSessionCheckpointPreviewResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/checkpoints/${encodeURIComponent(checkpointId)}/preview`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ mode }),
    },
  );
}

export async function restoreSessionCheckpoint(
  sessionId: string,
  checkpointId: string,
  mode: RuntimeSessionCheckpointPreviewMode = "both",
): Promise<RuntimeSessionCheckpointRestoreResponse> {
  return fetchRuntimeJson<RuntimeSessionCheckpointRestoreResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/checkpoints/${encodeURIComponent(checkpointId)}/restore`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ mode }),
    },
  );
}

export async function listSessionTurns(
  sessionId: string,
): Promise<RuntimeSessionTurnsResponse> {
  return fetchRuntimeJson<RuntimeSessionTurnsResponse>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/turns`),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function previewSessionBacktrack(
  sessionId: string,
  request: RuntimeSessionBacktrackRequest,
): Promise<RuntimeSessionBacktrackResponse> {
  return fetchRuntimeJson<RuntimeSessionBacktrackResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/backtrack/preview`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        ...request,
        preview_only: true,
        auto_submit: false,
      }),
    },
  );
}

export async function applySessionBacktrack(
  sessionId: string,
  request: RuntimeSessionBacktrackRequest,
): Promise<RuntimeSessionBacktrackResponse> {
  return fetchRuntimeJson<RuntimeSessionBacktrackResponse>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/backtrack`),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        ...request,
        preview_only: false,
      }),
    },
  );
}

export async function listSessionBacktrackAudit(
  sessionId: string,
): Promise<RuntimeSessionBacktrackAuditResponse> {
  return fetchRuntimeJson<RuntimeSessionBacktrackAuditResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/backtrack/audit`,
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function getSessionPlanMode(
  sessionId: string,
): Promise<RuntimeSessionPlanMode> {
  return fetchRuntimeJson<RuntimeSessionPlanMode>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/plan`),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function updateSessionPlanMode(
  sessionId: string,
  body: RuntimeSessionPlanModeUpdateRequest,
): Promise<RuntimeSessionPlanMode> {
  return fetchRuntimeJson<RuntimeSessionPlanMode>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/plan`),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    },
  );
}
