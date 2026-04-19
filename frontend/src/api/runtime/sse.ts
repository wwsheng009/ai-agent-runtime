import {
  type AgentChatRequest,
  type AgentChatResult,
  type AgentChatStreamChunkPayload,
  type AgentChatStreamDonePayload,
  type AgentChatStreamMetaPayload,
  type SessionRuntimeEvent,
} from "@/types/runtime";

import {
  buildErrorMessage,
  buildRuntimeUrl,
  buildRuntimeUrlWithQuery,
  parseErrorPayload,
} from "./shared";

type SseConsumeHandlers = {
  onClose?: () => void;
  onEvent: (eventName: string, payload: Record<string, unknown>) => void;
  onOpen?: () => void;
};

type SessionRuntimeStreamHandlers = {
  after?: number;
  onClose?: () => void;
  onErrorEvent?: (payload: Record<string, unknown>) => void;
  onEvent?: (event: SessionRuntimeEvent) => void;
  onOpen?: () => void;
  pollMs?: number;
  signal?: AbortSignal;
};

type AgentChatStreamHandlers = {
  onChunk?: (payload: AgentChatStreamChunkPayload) => void;
  onClose?: () => void;
  onDone?: (payload: AgentChatStreamDonePayload) => void;
  onErrorEvent?: (payload: Record<string, unknown>) => void;
  onMeta?: (payload: AgentChatStreamMetaPayload) => void;
  onObservation?: (payload: Record<string, unknown>) => void;
  onOpen?: () => void;
  onOrchestration?: (payload: Record<string, unknown>) => void;
  onPlanning?: (payload: Record<string, unknown>) => void;
  onReasoning?: (payload: AgentChatStreamChunkPayload) => void;
  onResult?: (payload: AgentChatResult) => void;
  onRoute?: (payload: Record<string, unknown>) => void;
  onSubagent?: (payload: Record<string, unknown>) => void;
  onToolCall?: (payload: AgentChatStreamChunkPayload) => void;
  onToolEnd?: (payload: AgentChatStreamChunkPayload) => void;
  onToolStart?: (payload: AgentChatStreamChunkPayload) => void;
  signal?: AbortSignal;
};

export function parseSsePayload(dataLines: string[]) {
  if (dataLines.length === 0) {
    return null;
  }

  const rawData = dataLines.join("\n");
  if (!rawData.trim()) {
    return {};
  }

  try {
    return JSON.parse(rawData) as Record<string, unknown>;
  } catch {
    return {
      raw: rawData,
    };
  }
}

function flushSseMessage(
  eventName: string,
  dataLines: string[],
  handlers: SseConsumeHandlers,
) {
  const payload = parseSsePayload(dataLines);
  if (payload === null) {
    return;
  }
  handlers.onEvent(eventName, payload);
}

export async function consumeSseResponse(
  response: Response,
  handlers: SseConsumeHandlers,
) {
  if (!response.body) {
    throw new Error("runtime stream response body is empty");
  }

  handlers.onOpen?.();

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let eventName = "message";
  let dataLines: string[] = [];

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split(/\r?\n/);
      buffer = lines.pop() ?? "";

      for (const line of lines) {
        if (line === "") {
          flushSseMessage(eventName, dataLines, handlers);
          eventName = "message";
          dataLines = [];
          continue;
        }
        if (line.startsWith(":")) {
          continue;
        }
        if (line.startsWith("event:")) {
          eventName = line.slice(6).trim() || "message";
          continue;
        }
        if (line.startsWith("data:")) {
          dataLines.push(line.slice(5).trimStart());
        }
      }
    }

    if (buffer.trim()) {
      const trailingLines = buffer.split(/\r?\n/);
      for (const line of trailingLines) {
        if (line.startsWith("event:")) {
          eventName = line.slice(6).trim() || eventName;
          continue;
        }
        if (line.startsWith("data:")) {
          dataLines.push(line.slice(5).trimStart());
        }
      }
    }

    flushSseMessage(eventName, dataLines, handlers);
  } finally {
    handlers.onClose?.();
    reader.releaseLock();
  }
}

export async function streamSessionRuntime(
  sessionId: string,
  handlers: SessionRuntimeStreamHandlers,
) {
  const response = await fetch(
    buildRuntimeUrlWithQuery(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime/stream`,
      {
        after: handlers.after,
        poll_ms: handlers.pollMs,
      },
    ),
    {
      headers: {
        Accept: "text/event-stream",
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
      if (eventName === "runtime_event") {
        handlers.onEvent?.(payload as SessionRuntimeEvent);
        return;
      }

      if (eventName === "error") {
        handlers.onErrorEvent?.(payload);
      }
    },
  });
}

export async function streamAgentChat(
  request: AgentChatRequest,
  handlers: AgentChatStreamHandlers,
) {
  const response = await fetch(buildRuntimeUrl("/api/agent/chat"), {
    method: "POST",
    headers: {
      Accept: "text/event-stream",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      ...request,
      stream: true,
    }),
    signal: handlers.signal,
  });

  if (!response.ok) {
    const payload = await parseErrorPayload(response);
    throw new Error(buildErrorMessage(response.status, payload));
  }

  await consumeSseResponse(response, {
    onClose: handlers.onClose,
    onOpen: handlers.onOpen,
    onEvent: (eventName, payload) => {
      switch (eventName) {
        case "meta":
          handlers.onMeta?.(payload as AgentChatStreamMetaPayload);
          return;
        case "chunk":
          handlers.onChunk?.(payload as AgentChatStreamChunkPayload);
          return;
        case "reasoning":
          handlers.onReasoning?.(payload as AgentChatStreamChunkPayload);
          return;
        case "tool_start":
          handlers.onToolStart?.(payload as AgentChatStreamChunkPayload);
          return;
        case "tool_call":
          handlers.onToolCall?.(payload as AgentChatStreamChunkPayload);
          return;
        case "tool_end":
          handlers.onToolEnd?.(payload as AgentChatStreamChunkPayload);
          return;
        case "planning":
          handlers.onPlanning?.(payload);
          return;
        case "orchestration":
          handlers.onOrchestration?.(payload);
          return;
        case "route":
          handlers.onRoute?.(payload);
          return;
        case "observation":
          handlers.onObservation?.(payload);
          return;
        case "subagent":
          handlers.onSubagent?.(payload);
          return;
        case "result":
          handlers.onResult?.(payload as AgentChatResult);
          return;
        case "done":
          handlers.onDone?.(payload as AgentChatStreamDonePayload);
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
