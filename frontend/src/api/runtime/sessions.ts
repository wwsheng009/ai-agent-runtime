import type {
  RuntimeCreateSessionRequest,
  RuntimeCreateSessionResponse,
  RuntimeSessionCheckpointFilesResponse,
  RuntimeSessionCheckpointPreviewMode,
  RuntimeSessionCheckpointPreviewResponse,
  RuntimeSessionCheckpointsQuery,
  RuntimeSessionCheckpointsResponse,
  RuntimeSessionRecord,
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
