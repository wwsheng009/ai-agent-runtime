import {
  type AgentChatRequest,
  type AgentChatResult,
  type AgentChatStreamChunkPayload,
  type AgentChatStreamDonePayload,
  type AgentChatStreamMetaPayload,
  type SessionRuntimeEvent,
} from "@/types/runtime";
import { beginLiveChannel } from "@/lib/live-diagnostics/store";
import { type LiveDiagnosticsChannelId } from "@/lib/live-diagnostics/types";

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
  /**
   * 读侧静默看门狗（毫秒）：连续该时长没有从 socket 读到**任何字节**
   * （`: keepalive` 注释帧同样算存活证据）即判定连接已死。
   *
   * 为什么必须有：半开连接不会让浏览器报错，`reader.read()` 可以永远挂着
   * ——实测故障现场 CDP 网络层只到达 13 字节、页面侧消费 21 字节，而
   * `isResponding` 永远为 true，UI 永久停在 "Streaming output…"。只有
   * 「按字节计时的看门狗」能把这种「连着但没数据」变成可处理的错误。
   *
   * 命中后取消底层 body（浏览器据此 abort 请求）并抛 SseIdleTimeoutError，
   * 由调用方决定重连（runtime stream 按游标续传）或降级（chat 流标记
   * transport=error 并给重试入口）。缺省不启用。
   */
  idleTimeoutMs?: number;
  /**
   * 观测通道（右侧「会话详情 → 网络详情」）：把本次读取循环的字节数、keepalive、
   * 业务事件与静默超时写入 live-diagnostics store。缺省不观测——其它 SSE 消费方
   * 无需改动，也不承担任何额外开销。
   */
  diagnostics?: { channel: LiveDiagnosticsChannelId; sessionId?: string | null };
};

/**
 * 读侧静默看门狗命中时抛出的错误。
 *
 * 单独成类型是为了让调用方把它与「网络错误」「用户 abort」区分开：
 * 前者要自愈/降级，后者要静默收尾（例如 stopResponding 的 abort）。
 */
export class SseIdleTimeoutError extends Error {
  readonly idleTimeoutMs: number;

  constructor(idleTimeoutMs: number) {
    super(`runtime stream idle timeout: no bytes for ${idleTimeoutMs}ms`);
    this.name = "SseIdleTimeoutError";
    this.idleTimeoutMs = idleTimeoutMs;
  }
}

export function isSseIdleTimeoutError(
  error: unknown,
): error is SseIdleTimeoutError {
  return error instanceof SseIdleTimeoutError;
}

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
 *
 * 并发正确性（多流共享单例通道）：前台 runtime / 后台 runtime / chat 多个
 * 消费循环可能同时命中让出预算。旧实现每次调用覆写 `port1.onmessage`，
 * 后一次调用会顶掉前一次的 resolve —— 被顶掉的流永久停在 `await`（字节仍
 * 到达、UI 静默停更），且该挂起不可恢复。这里改为等待队列：消息到达统一
 * 唤醒全部等待者，并发调用互不丢失。
 */
export const yieldToEventLoop = (() => {
  let channel: MessageChannel | null = null;
  const waiters: Array<() => void> = [];
  const drainWaiters = () => {
    for (const wake of waiters.splice(0, waiters.length)) {
      wake();
    }
  };
  return () =>
    new Promise<void>((resolve) => {
      if (typeof MessageChannel === "function") {
        if (!channel) {
          channel = new MessageChannel();
          channel.port1.onmessage = drainWaiters;
        }
        waiters.push(resolve);
        channel.port2.postMessage(null);
        return;
      }
      setTimeout(resolve, 0);
    });
})();

type SessionRuntimeStreamHandlers = {
  after?: number;
  /** 读侧静默看门狗（见 SseConsumeHandlers.idleTimeoutMs）；缺省不启用。 */
  idleTimeoutMs?: number;
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
  /** 读侧静默看门狗（见 SseConsumeHandlers.idleTimeoutMs）；缺省不启用。 */
  idleTimeoutMs?: number;
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
  diagnostics: ReturnType<typeof beginLiveChannel> | null,
) {
  const payload = parseSsePayload(dataLines);
  if (payload === null) {
    return;
  }
  diagnostics?.event(eventName, payload);
  handlers.onEvent(eventName, payload);
}

export async function consumeSseResponse(
  response: Response,
  handlers: SseConsumeHandlers,
) {
  if (!response.body) {
    throw new Error("runtime stream response body is empty");
  }

  // 观测句柄在建连时创建：open/close/字节/事件/静默超时全部落到同一个会话键上。
  const diagnostics = handlers.diagnostics
    ? beginLiveChannel(handlers.diagnostics)
    : null;
  handlers.onOpen?.();
  diagnostics?.open();

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let eventName = "message";
  let dataLines: string[] = [];
  let lastYieldAt = nowMs();

  // 静默看门狗：定时器在「每次 read 返回」后重置，因此它度量的是
  // 「距离最后一次收到字节」的时长，而不是「距离最后一次业务事件」。
  // 这一点很关键：空闲会话（after == latest_seq）唯一的存活证据就是
  // 服务端每 15s 一行的 `: keepalive`，按事件计时会把健康连接误判成死连接。
  const idleTimeoutMs = handlers.idleTimeoutMs ?? 0;
  let idleTimer: ReturnType<typeof setTimeout> | undefined;
  let idleTimedOut = false;
  // 看门狗兜底通道：`reader.cancel()` 只保证「请求被取消」，不保证挂起的
  // `reader.read()` 一定收尾——半开连接上实测出现过 cancel 与 read 双双不兑现，
  // 看门狗命中了、消费循环却永远停在 await，回合因此永不收尾（提交闸门永久
  // 关闭）。这里额外给循环一条 reject 通道，保证无论 read 是否兑现都能在
  // 下一次 await 上失败。`Promise.race` 会消费这条 promise 的拒绝，创建时先挂
  // 空 catch，避免无人等待时成为未处理拒绝。
  let rejectStalled: ((error: SseIdleTimeoutError) => void) | null = null;
  const stalledSignal = new Promise<never>((_, reject) => {
    rejectStalled = reject;
  });
  stalledSignal.catch(() => {});
  const clearIdleTimer = () => {
    if (idleTimer !== undefined) {
      clearTimeout(idleTimer);
      idleTimer = undefined;
    }
  };
  const armIdleTimer = () => {
    if (idleTimeoutMs <= 0) {
      return;
    }
    clearIdleTimer();
    idleTimer = setTimeout(() => {
      idleTimedOut = true;
      // 静默看门狗命中是「连接还在、字节没了」的唯一现场证据，先记账再收连接。
      diagnostics?.stall(idleTimeoutMs);
      // cancel 让挂起的 read 以 done 收尾并释放连接（浏览器据此 abort 请求）。
      // 真正的错误在循环结束后统一抛出，避免在定时器回调里制造未捕获拒绝。
      try {
        void reader.cancel().catch(() => {});
      } catch {
        // 流已关闭/已锁定时 cancel 可能同步抛错：忽略，兜底通道会收尾循环。
      }
      // 先摘引用再拒绝：重复命中不会制造第二次拒绝。
      const reject = rejectStalled;
      rejectStalled = null;
      reject?.(new SseIdleTimeoutError(idleTimeoutMs));
    }, idleTimeoutMs);
  };
  armIdleTimer();

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

  // 无看门狗（idleTimeoutMs <= 0）时不引入额外的 race 开销：stalledSignal 永不
  // 拒绝，直接读即可。
  const readOnce = () =>
    idleTimeoutMs > 0 ? Promise.race([reader.read(), stalledSignal]) : reader.read();

  try {
    while (true) {
      const { done, value } = await readOnce().catch((error: unknown) => {
        // 看门狗命中时不同浏览器可能以 done 收尾、也可能让 read 拒绝或永不兑现：
        // 三种都归一成同一个可识别错误，调用方只处理一种形态。
        if (idleTimedOut) {
          throw new SseIdleTimeoutError(idleTimeoutMs);
        }
        throw error;
      });
      if (done) {
        break;
      }

      armIdleTimer();
      // 字节数按读取分片累加：keepalive 也算流量——「只剩 keepalive」与
      // 「完全没字节」是两种不同的故障，面板要能分开看。
      diagnostics?.bytes(value.byteLength);
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split(/\r?\n/);
      buffer = lines.pop() ?? "";

      for (const line of lines) {
        if (line === "") {
          flushSseMessage(eventName, dataLines, handlers, diagnostics);
          eventName = "message";
          dataLines = [];
          await maybeYield();
          continue;
        }
        if (line.startsWith(":")) {
          // 注释帧：服务端每 15s 一行的 `: keepalive`。读侧看门狗据此判活。
          diagnostics?.keepalive();
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

    if (idleTimedOut) {
      throw new SseIdleTimeoutError(idleTimeoutMs);
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

    flushSseMessage(eventName, dataLines, handlers, diagnostics);
  } finally {
    clearIdleTimer();
    handlers.onClose?.();
    diagnostics?.close();
    try {
      reader.releaseLock();
    } catch {
      // 挂起的 read 未兑现时 releaseLock 会抛（规范不允许释放仍有未结读取的
      // reader）。连接已被 cancel，这里吞掉即可——绝不能让它掩盖真正的
      // SseIdleTimeoutError。
    }
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
    diagnostics: { channel: "runtime", sessionId },
    idleTimeoutMs: handlers.idleTimeoutMs,
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
    diagnostics: { channel: "chat", sessionId: request.session_id },
    idleTimeoutMs: handlers.idleTimeoutMs,
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
