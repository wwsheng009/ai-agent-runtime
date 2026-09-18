// 由 lib/thread-state/events.ts 机械拆分而来（P0-2「单文件 ≤ 500 非空行」门禁），
// 仅搬迁不改语义。本文件承载「runtime live 通道 → thread」的三块逻辑：
// 1) 打字机增量（正文 / 推理 / 图片占位）应用；
// 2) 流式助手消息定位与 turn 归属判定（增量与桥接帧共用同一 predicate）；
// 3) `chat.sse.*` 桥接帧（工具生命周期 / 阶段推进）应用。

import { type ChatMessage, type MessageSegment, type Thread } from "@/data/mock";
import {
  type AgentChatStreamChunkPayload,
  type SessionRuntimeEvent,
} from "@/types/runtime";
import { CHAT_SSE_EVENT_PREFIX } from "@/types/runtime/event-contract";

import { getRuntimeDeltaKind, matchesActiveTurn, type RuntimeBridgeKind } from "./deltas";
import {
  buildGeneratedImagePlaceholderSegment,
  upsertGeneratedImageSegment,
} from "./generated-images";
import { readFirstTextValue } from "./history-mapping";
import {
  findLatestAssistantMessage,
  resolveLiveAssistantTarget,
} from "./live-assistant-target";
import {
  closeRunningReasoningSegments,
  STREAM_PLACEHOLDER_TEXT,
  type ToolMessageSegment,
} from "./messages";
import { getToolName } from "./shared";
import { readRawTextValue, readTextDelta } from "./text-utils";
import {
  buildToolSegmentFromPayload,
  getToolCallId,
  getToolErrorMessage,
  upsertToolSegment,
} from "./tools";

/** `chat.sse.<name>` → `<name>`；非帧名原样返回（与旧 replace 行为一致）。 */
function chatSseFrameName(eventType: string): string {
  return eventType.startsWith(CHAT_SSE_EVENT_PREFIX)
    ? eventType.slice(CHAT_SSE_EVENT_PREFIX.length)
    : eventType;
}

/**
 * 方案B：把 runtime/stream 实时到达的打字机增量事件应用到 thread。
 *
 * 只在“正在请求中”（渲染 gate=true）时由 useSessionRuntimeStream 调用；
 * 历史回放/reload 不会触发（否则会把旧 turn 的增量误渲染到当前消息）。
 * 覆盖三类增量：
 * - assistant_delta        → 最新 assistant 消息 text segment 追加增量
 * - assistant_reasoning    → 最新 assistant 消息 reasoning segment 追加增量
 * - assistant.image_progress → 图片生成占位段 upsert（原 applyRuntimeEventToThread 迁移）
 *
 * 与 /api/agent/chat 的最终 result 天然不冲突：result 到达时 onChunk 以
 * 完整文本重建 text segment（替换而非追加），文本不会翻倍。
 */
/** live 通道写入目标（lib/live-stream-text.ts）：消息 id + 本帧增量文本。 */
export type RuntimeLiveDelta = {
  kind: "reasoning" | "text";
  messageId: string;
  text: string;
  /**
   * reasoning 专用：本帧开启了**新的一块**推理（上一块已被工具行 / 正文顶下来）。
   * live 层按块寻址，收到它必须**覆盖**当前块的文本而不是追加，否则尾行会把整轮
   * 推理拼成一段（「推理 → 工具 → 推理 → 工具」又变回一行）。
   */
  blockStart?: boolean;
};

/**
 * live 通道接收器：只由「已 claim 成功」的那条通道在事件到达时调用。两条传输通道共享
 * RuntimeDeltaCoordinator —— 每个增量只会被一方 claim，到达顺序即渲染顺序。
 * 见 lib/live-stream-text.ts 与 use-session-runtime-stream。
 */
export type RuntimeLiveDeltaSink = (delta: RuntimeLiveDelta) => void;

/** 单个增量段（正文 / 推理）的文本载荷。 */
type LiveDeltaText = {
  kind: RuntimeLiveDelta["kind"];
  text: string;
  blockStart?: boolean;
};

export function applyRuntimeDeltaToThread(
  thread: Thread,
  event: SessionRuntimeEvent,
  expectedTurnId?: string,
  liveSink?: RuntimeLiveDeltaSink,
): Thread {
  const eventTurnId = getRuntimeEventTurnId(event);
  // 与 useSessionRuntimeStream 的实时门控共用同一判定（见 matchesActiveTurn）：
  // 事件未携带 turn 身份时视为「未知」而非「其他 turn」。
  if (!matchesActiveTurn(expectedTurnId, eventTurnId)) {
    return thread;
  }

  // 写目标：本回合仍在 streaming 的助手消息；没有且帧的回合归属可判定时补建一条
  // 占位消息（断流 / 重放后据此重建在途回合，见 resolveLiveAssistantTarget）。
  const target = resolveLiveAssistantTarget(thread, eventTurnId, expectedTurnId, true);
  if (!target) {
    return thread;
  }

  const updateLiveAssistant = (
    updater: (message: ChatMessage) => ChatMessage,
    live?: LiveDeltaText,
  ) => {
    const nextThread: Thread = {
      ...target.thread,
      messages: target.thread.messages.map((message, index) =>
        index === target.index ? updater(message) : message,
      ),
    };
    // live 通道目标与写 store 的目标是同一条消息（含本次补建的占位）。
    if (live && liveSink) {
      liveSink({ ...live, messageId: target.message.id });
    }
    return nextThread;
  };

  // 分类统一走 getRuntimeDeltaKind：它是「事件名 → 增量种类」的唯一真源
  // （含 `assistant.delta` 等总线双拼写别名），避免这里再维护一份 event.type
  // 白名单——两份名单一旦漂移，dot 形态的增量会在本层被 default 静默吞掉。
  switch (getRuntimeDeltaKind(event.type)) {
    case "text":
      return appendAssistantTextDelta(thread, event, updateLiveAssistant);
    case "reasoning":
      return appendAssistantReasoningDelta(
        thread,
        event,
        updateLiveAssistant,
        target.message.segments,
      );
    case "image":
      return appendAssistantImageProgress(thread, event, updateLiveAssistant);
    default:
      return thread;
  }
}

export function getRuntimeEventTurnId(event: SessionRuntimeEvent): string {
  const payload = event.payload;
  if (!payload) {
    return "";
  }
  for (const key of ["turn_id", "turnId", "turn"]) {
    const value = payload[key];
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

function appendAssistantTextDelta(
  thread: Thread,
  event: SessionRuntimeEvent,
  updateLiveAssistant: (updater: (message: ChatMessage) => ChatMessage, live?: LiveDeltaText) => Thread,
): Thread {
  const deltaText = readTextDelta(event.payload);
  if (!deltaText) {
    return thread;
  }
  return updateLiveAssistant((message) => {
    const segments = appendTextToMessageSegments(message.segments, deltaText);
    return { ...message, segments };
  }, { kind: "text", text: deltaText });
}

function appendAssistantReasoningDelta(
  thread: Thread,
  event: SessionRuntimeEvent,
  updateLiveAssistant: (updater: (message: ChatMessage) => ChatMessage, live?: LiveDeltaText) => Thread,
  currentSegments: MessageSegment[],
): Thread {
  const payload = event.payload ?? {};
  const reasoningBlock =
    payload.reasoning && typeof payload.reasoning === "object"
      ? (payload.reasoning as Record<string, unknown>)
      : null;
  const deltaText =
    readRawTextValue(reasoningBlock ?? {}, "summary", "content", "delta") ||
    readRawTextValue(payload, "content", "delta");
  if (!deltaText) {
    return thread;
  }
  // 本帧是「续写尾块」还是「新开一块」由写之前的段结构决定（判定与下面的写入
  // 用的是同一份 segments，纯函数、可重放）。
  const blockStart = !continuesTrailingReasoningSegment(currentSegments);
  return updateLiveAssistant((message) => {
    return {
      ...message,
      segments: appendReasoningToMessageSegments(message.segments, deltaText),
    };
  }, { kind: "reasoning", text: deltaText, blockStart });
}

function appendAssistantImageProgress(
  thread: Thread,
  event: SessionRuntimeEvent,
  updateLiveAssistant: (
    updater: (message: ChatMessage) => ChatMessage,
  ) => Thread,
): Thread {
  const imageSegment = buildGeneratedImagePlaceholderSegment(event.payload);
  if (!imageSegment) {
    return thread;
  }
  return updateLiveAssistant((message) => {
    return {
      ...message,
      segments: upsertGeneratedImageSegment(message.segments, imageSegment),
    };
  });
}

function appendTextToMessageSegments(
  segments: MessageSegment[],
  delta: string,
): MessageSegment[] {
  const nextSegments = [...segments];
  for (let index = nextSegments.length - 1; index >= 0; index--) {
    const segment = nextSegments[index];
    if (segment.type !== "text") {
      continue;
    }
    const previous = segment.content;
    const base = previous === STREAM_PLACEHOLDER_TEXT ? "" : previous;
    nextSegments[index] = { ...segment, content: base + delta };
    return nextSegments;
  }
  nextSegments.push({ type: "text", content: delta });
  return nextSegments;
}

/**
 * 尾段（数组最后一段）是不是仍在增长的推理段。只有它才继续接收推理增量。
 *
 * 旧实现从数组**末尾往前找第一个**推理段往里追加，于是「推理 → 工具 → 推理 →
 * 工具…」的回合里，工具之后到达的推理全被并回第一块——页面上只剩一段推理。
 * 段顺序就是到达顺序：推理段一旦被工具行 / 正文 / 图片顶下来，它就写完了。
 */
function continuesTrailingReasoningSegment(segments: MessageSegment[]): boolean {
  const tail = segments[segments.length - 1];
  return Boolean(tail && tail.type === "reasoning");
}

function appendReasoningToMessageSegments(
  segments: MessageSegment[],
  delta: string,
): MessageSegment[] {
  const nextSegments = [...segments];
  const tailIndex = nextSegments.length - 1;
  const tail = nextSegments[tailIndex];
  if (tail && tail.type === "reasoning") {
    const previous = tail.content;
    // A persisted/final reasoning block may be followed by the first live
    // block for this turn.  Keep the visual separator used by the chat SSE
    // path, while preserving raw chunk boundaries once streaming is running.
    const separator =
      previous.length > 0 && tail.running !== true && !previous.endsWith("\n")
        ? "\n"
        : "";
    nextSegments[tailIndex] = {
      ...tail,
      content: previous + separator + delta,
      running: true,
    };
    return nextSegments;
  }
  // 尾段不是推理段：工具执行完之后模型重新开始思考 —— 新起一块。
  nextSegments.push({ type: "reasoning", content: delta, running: true });
  return nextSegments;
}

function replaceMessageSegments(
  thread: Thread,
  messageIndex: number,
  segments: MessageSegment[],
  lastRuntimeEventType?: string,
) {
  return {
    ...thread,
    ...(lastRuntimeEventType ? { lastRuntimeEventType } : {}),
    messages: thread.messages.map((message, index) =>
      index === messageIndex ? { ...message, segments } : message,
    ),
  };
}

export function updateLatestAssistantMessage(
  thread: Thread,
  updater: (message: ChatMessage) => ChatMessage,
  predicate: (message: ChatMessage) => boolean = () => true,
) {
  const target = findLatestAssistantMessage(thread, predicate);
  if (!target) {
    return thread;
  }
  return {
    ...thread,
    messages: thread.messages.map((current, currentIndex) =>
      currentIndex === target.index ? updater(current) : current,
    ),
  };
}

/**
 * 把一条 `chat.sse.*` 桥接帧应用到「当前仍在 streaming 的助手消息」上。
 *
 * - tool（tool_start / tool_call / tool_end）→ 归并工具行：键取
 *   `tool_call.id`（帧里还有 `tool` / `delta` 两种载体，buildToolSegmentFromPayload
 *   已覆盖），状态按帧类型与 metadata.error 得到 started → running →
 *   finished/error。与 /api/agent/chat 通道用同一个 upsert，两条通道先后到达
 *   只会收敛成同一行。
 * - phase（chunk / observation）→ 只做阶段推进：把仍在跑的推理段收尾。正文与
 *   推理文本**不在这里追加**——同一段文本已由 `assistant_delta` /
 *   `assistant.reasoning` 写入（这正是 `getRuntimeBridgeKind` 不把它们当增量的
 *   原因），在这里再追加一次会让每段内容翻倍。`chat.sse.reasoning` 是推理增量
 *   的孪生副本，既不写正文也不收尾（收尾会与增量侧「仍在推理」的标记打架）。
 */
export function applyChatSseBridgeFrame(
  thread: Thread,
  event: SessionRuntimeEvent,
  frame: RuntimeBridgeKind,
  expectedTurnId?: string,
): Thread {
  const payload = resolveBridgeToolPayload(event);
  if (!payload) {
    return thread;
  }
  const eventTurnId = getRuntimeEventTurnId(event);
  const toolName = getToolName(payload);
  // 既无工具调用 id、也无真实工具名（旧帧/残缺帧）时不落行：upsertToolSegment
  // 的键会退化成兜底名 “tool”，把互不相干的调用合并成一行假工具。
  const hasToolRow = Boolean(getToolCallId(payload)) || toolName !== "tool";

  // 目标：本回合仍在 streaming 的助手消息；没有且「帧确实能落一行工具」且回合
  // 归属可判定时补建占位消息——阶段帧不携带可渲染内容，不建空占位。
  const target = resolveLiveAssistantTarget(
    thread,
    eventTurnId,
    expectedTurnId,
    frame.kind === "tool" && hasToolRow,
  );
  if (!target) {
    return thread;
  }

  const closeReasoning = () => {
    const segments = closeRunningReasoningSegments(target.message.segments);
    // 没有在跑的推理段就返回原引用：单回合上千帧，逐帧换身份会让下游 memo 全部失效。
    return segments === target.message.segments
      ? target.thread
      : replaceMessageSegments(target.thread, target.index, segments);
  };

  if (frame.kind !== "tool" || !hasToolRow) {
    return closeReasoning();
  }

  const status: ToolMessageSegment["status"] = getToolErrorMessage(payload)
    ? "error"
    : frame.status;
  const segments = upsertToolSegment(
    closeRunningReasoningSegments(target.message.segments),
    buildToolSegmentFromPayload(payload, status),
  );
  // 与直连通道同形（`tool_end:shell`）：线程条与历史重载键读的是同一个字段。
  return replaceMessageSegments(
    target.thread,
    target.index,
    segments,
    `${chatSseFrameName(event.type)}:${toolName}`,
  );
}

/**
 * 回合终态帧：runtime/stream 上的权威收尾信号。
 *
 * 直连 `/api/agent/chat` 中断时本地 stall 路径刻意不定稿（见
 * `agent-chat-turn/stall.ts`：尾巴消息保持 streaming，等 runtime 通道续写），
 * 而 runtime 通道此前只认工具/阶段帧，`chat.sse.done` 无人消费——直连一断，
 * 回合在服务端正常结束也不会收敛（实测 UI 永久「响应中」）。
 *
 * 这里把「终态帧 → 清 streaming 标记」的兜底收口在桥接层：不做文本定稿
 * （正文由 assistant_delta / 直连 result 写入），只幂等地摘掉标记。
 */
export function isChatSseTerminalFrame(eventType: string): boolean {
  return (
    eventType === `${CHAT_SSE_EVENT_PREFIX}done` ||
    eventType === `${CHAT_SSE_EVENT_PREFIX}error`
  );
}

/**
 * 把终态帧应用到 thread：清掉同一回合仍在 streaming 的助手消息标记。
 *
 * 归属判定与 `isLiveAssistantMessage` 同语义：两侧都明确且不一致才拒绝，
 * 事件缺 turn_id（旧帧）时按在途消息处理。已定稿消息原样返回（引用不变），
 * 重连重放同一终态帧不会产生多余渲染。
 */
export function finalizeRuntimeTurnInThread(
  thread: Thread,
  event: SessionRuntimeEvent,
): Thread {
  const eventTurnId = getRuntimeEventTurnId(event);
  let changed = false;
  const messages = thread.messages.map((message) => {
    if (message.role !== "assistant" || message.streaming !== true) {
      return message;
    }
    if (
      message.runtimeTurnId &&
      eventTurnId &&
      message.runtimeTurnId !== eventTurnId
    ) {
      return message;
    }
    changed = true;
    return {
      ...message,
      streaming: false,
      // `isLiveAssistantMessage` 的 label 兜底会把 label==="streaming" 的消息
      // 继续当作可写目标；终态后必须同时摘掉 label，防止迟到增量写进已定稿消息。
      label: message.label === "streaming" ? "runtime" : message.label,
    };
  });
  return changed ? { ...thread, messages } : thread;
}

/** runtime 生命周期工具事件里可当作工具卡定位信息的字段。 */
const BRIDGE_TOOL_LOCATION_KEYS = ["directory", "file_path", "command_text"] as const;

/**
 * 把桥接帧载荷规范成工具行解析器认识的形状。
 *
 * - `chat.sse.*` 帧：载荷已经是 `tool` / `tool_call` / `delta` 三载体形态，原样返回。
 * - runtime 生命周期帧（`tool_started` / `tool_finished`，含 `tool.requested` /
 *   `tool.completed` 点形态别名）：agent loop 发的是另一套字段名
 *   （`tool_call_id` / `logical_tool` / `arg_preview` / `summary`），这里补成
 *   `tool` + `tool_call` 两种载体，让两条通道共用同一个
 *   `buildToolSegmentFromPayload`，并按**真实 provider call id** 收敛成同一行：
 *   实时行先出现，随后到达的 `chat.sse.tool_end`（含观测里的完整 arguments /
 *   output）按同一 id upsert，就地补全而不是新增一行。
 */
function resolveBridgeToolPayload(
  event: SessionRuntimeEvent,
): AgentChatStreamChunkPayload | undefined {
  const raw = event.payload as AgentChatStreamChunkPayload | undefined;
  if (!raw) {
    return undefined;
  }
  if (event.type.startsWith(CHAT_SSE_EVENT_PREFIX)) {
    return raw;
  }

  const rawRecord = raw as Record<string, unknown>;
  const id = readFirstTextValue(rawRecord, "tool_call_id", "toolCallId", "id");
  const name =
    readFirstTextValue(rawRecord, "logical_tool", "tool_name", "toolName", "name") ||
    (event.tool_name ?? "").trim();
  if (!id && !name) {
    return undefined;
  }

  const args = readFirstTextValue(rawRecord, "arg_preview", "command_text");
  const content = readFirstTextValue(
    rawRecord,
    "summary",
    "render_output",
    "content",
    "output",
  );
  const tool: Record<string, unknown> = { id, status: event.type };
  const toolCall: Record<string, unknown> = { id };
  // 工具名缺席时**不写空串**：`getToolName` 只认「字符串且存在」，写了空串会把
  // 兜底名 "tool" 挤掉，行标题变成空白（也绕开上层「无 id 且名为兜底名」的跳过判定）。
  if (name) {
    tool.name = name;
    toolCall.name = name;
  }
  if (args) {
    tool.args = args;
    toolCall.arguments = args;
  }
  if (content) {
    tool.content = content;
  }
  for (const key of BRIDGE_TOOL_LOCATION_KEYS) {
    const value = rawRecord[key];
    if (typeof value === "string" && value.trim()) {
      tool[key] = value;
    }
  }

  return {
    ...raw,
    content: content ?? raw.content,
    // metadata 保留原始载荷（step / trace_id / error 等）：状态判定读的是
    // `metadata.error`，丢掉它会把失败的工具画成成功。
    metadata: raw,
    tool_call: toolCall,
    tool,
  };
}

