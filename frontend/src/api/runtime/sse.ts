import {
  type AgentChatRequest,
  type AgentChatResult,
  type AgentChatStreamChunkPayload,
  type AgentChatStreamDonePayload,
  type AgentChatStreamMetaPayload,
  type SessionRuntimeEvent,
} from "@/types/runtime";

import {
  buildRuntimeUrl,
  buildRuntimeUrlWithQuery,
  parseErrorPayload,
  RuntimeApiError,
} from "./shared";

type SseConsumeHandlers = {
  onClose?: () => void;
  onEvent: (eventName: string, payload: Record<string, unknown>) => void;
  onOpen?: () => void;
};

/**
 * SSE 读取循环的让出预算（毫秒）。
 *
 * `await reader.read()` 在浏览器缓冲区已有数据时会立即 resolve，**不会**让出事件
 * 循环：快模型（每帧 1–2 字符、单回合 700+ 帧）会在同一个 task 里把整条流排空，
 * 主线程既不能渲染也不能响应输入——CDP 实测单个 long task 1.5s（修复前 64s），
 * 用户看到的是「等流结束才整体蹦出」。这里按预算主动切一次宏任务，把一个大 task
 * 拆成多个小 task，让 React 有机会提交中间态（逐字生长）。
 */
const SSE_YIELD_BUDGET_MS = 8;

function nowMs() {
  return typeof performance !== "undefined" &&
    typeof performance.now === "function"
    ? performance.now()
    : Date.now();
}

/**
 * 让出到宏任务队列。MessageChannel 不受 `setTimeout(0)` 的 4ms 嵌套钳制，
 * 让出本身的代价可忽略；环境不支持时退回 setTimeout。
 */
const yieldToEventLoop = (() => {
  let channel: MessageChannel | null = null;
  return () =>
    new Promise<void>((resolve) => {
      if (typeof MessageChannel === "function") {
        channel ??= new MessageChannel();
        channel.port1.onmessage = () => resolve();
        channel.port2.postMessage(null);
        return;
      }
      setTimeout(resolve, 0);
    });
})();

type SessionRuntimeStreamHandlers = {
  after?: number;
  /**
   * 订阅宿主进程内的 live-only 事件（如 `tool.progress`）；这些事件不落
   * EventStore，只在 `live=1` 时随 SSE 投递（payload.live=true）。
   */
  live?: boolean;
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
  let lastYieldAt = nowMs();

  // 只有真的花掉预算才让出：短流（测试 / 小回合）完全不受影响，长流被切成
  // 多个 ≤ 预算的小任务，浏览器有机会在中间渲染。
  const maybeYield = async () => {
    const now = nowMs();
    if (now - lastYieldAt < SSE_YIELD_BUDGET_MS) {
      return;
    }
    lastYieldAt = now;
    await yieldToEventLoop();
  };

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
          await maybeYield();
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
        live: handlers.live ? 1 : undefined,
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
    throw new RuntimeApiError(response.status, payload);
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
    throw new RuntimeApiError(response.status, payload);
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
