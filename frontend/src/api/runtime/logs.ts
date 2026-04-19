import {
  type RuntimeLogEntry,
  type RuntimeLogStreamReadyPayload,
  type RuntimeLogStreamResetPayload,
  type RuntimeLogsQuery,
  type RuntimeLogsResponse,
} from "@/types/runtime";

import {
  buildErrorMessage,
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
  parseErrorPayload,
} from "./shared";
import { consumeSseResponse } from "./sse";

type RuntimeLogsRequestOptions = RuntimeLogsQuery & {
  adminToken?: string;
};

type RuntimeLogStreamHandlers = RuntimeLogsRequestOptions & {
  after?: number;
  onClose?: () => void;
  onErrorEvent?: (payload: Record<string, unknown>) => void;
  onLog?: (entry: RuntimeLogEntry) => void;
  onOpen?: () => void;
  onReady?: (payload: RuntimeLogStreamReadyPayload) => void;
  onReset?: (payload: RuntimeLogStreamResetPayload) => void;
  pollMs?: number;
  signal?: AbortSignal;
};

function buildRuntimeLogHeaders(adminToken?: string) {
  const token = adminToken?.trim();
  if (!token) {
    return {} as Record<string, string>;
  }
  return {
    Authorization: `Bearer ${token}`,
  } satisfies Record<string, string>;
}

export async function listRuntimeLogs(
  options: RuntimeLogsRequestOptions = {},
): Promise<RuntimeLogsResponse> {
  return fetchRuntimeJson<RuntimeLogsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/logs", {
      limit: options.limit,
      level: options.level,
      query: options.query,
    }),
    {
      headers: buildRuntimeLogHeaders(options.adminToken),
    },
  );
}

export async function streamRuntimeLogs(handlers: RuntimeLogStreamHandlers) {
  const response = await fetch(
    buildRuntimeUrlWithQuery("/api/runtime/logs/stream", {
      after: handlers.after,
      level: handlers.level,
      poll_ms: handlers.pollMs,
      query: handlers.query,
    }),
    {
      headers: {
        Accept: "text/event-stream",
        ...buildRuntimeLogHeaders(handlers.adminToken),
      },
      signal: handlers.signal,
    },
  );

  if (!response.ok) {
    const payload = await parseErrorPayload(response);
    throw new Error(buildErrorMessage(response.status, payload));
  }

  await consumeSseResponse(response, {
    onClose: handlers.onClose,
    onOpen: handlers.onOpen,
    onEvent: (eventName, payload) => {
      switch (eventName) {
        case "ready":
          handlers.onReady?.(payload as RuntimeLogStreamReadyPayload);
          return;
        case "reset":
          handlers.onReset?.(payload as RuntimeLogStreamResetPayload);
          return;
        case "log":
          handlers.onLog?.(payload as RuntimeLogEntry);
          return;
        case "error":
          handlers.onErrorEvent?.(payload);
          return;
        default:
          return;
      }
    },
  });
}
