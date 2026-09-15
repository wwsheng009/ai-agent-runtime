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

import { getRuntimeDeltaKind, matchesActiveTurn, type RuntimeBridgeKind } from "./deltas";
import {
  buildGeneratedImagePlaceholderSegment,
  upsertGeneratedImageSegment,
} from "./generated-images";
import { readFirstTextValue } from "./history-mapping";
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
};

/**
 * live 通道接收器：只由「已 claim 成功」的那条通道在事件到达时调用。两条传输通道共享
 * RuntimeDeltaCoordinator —— 每个增量只会被一方 claim，到达顺序即渲染顺序。
 * 见 lib/live-stream-text.ts 与 use-session-runtime-stream。
 */
export type RuntimeLiveDeltaSink = (delta: RuntimeLiveDelta) => void;

/** 单个增量段（正文 / 推理）的文本载荷。 */
type LiveDeltaText = { kind: RuntimeLiveDelta["kind"]; text: string };

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

  // live 通道目标：与写 store 的路径共用同一 predicate（同一条消息）。
  const liveTargetId = liveSink
    ? (findLatestAssistantMessage(thread, (message) =>
        isLiveAssistantMessage(message, eventTurnId),
      )?.message.id ?? "")
    : "";

  const updateLiveAssistant = (
    updater: (message: ChatMessage) => ChatMessage,
    live?: LiveDeltaText,
  ) => {
    let applied = false;
    const nextThread = updateLatestAssistantMessage(
      thread,
      (message) => {
        applied = true;
        return updater(message);
      },
      (message) => isLiveAssistantMessage(message, eventTurnId),
    );
    if (applied && live && liveSink && liveTargetId)
      liveSink({ ...live, messageId: liveTargetId });
    return applied ? nextThread : thread;
  };

  // 分类统一走 getRuntimeDeltaKind：它是「事件名 → 增量种类」的唯一真源
  // （含 `assistant.delta` 等总线双拼写别名），避免这里再维护一份 event.type
  // 白名单——两份名单一旦漂移，dot 形态的增量会在本层被 default 静默吞掉。
  switch (getRuntimeDeltaKind(event.type)) {
    case "text":
      return appendAssistantTextDelta(thread, event, updateLiveAssistant);
    case "reasoning":
      return appendAssistantReasoningDelta(thread, event, updateLiveAssistant);
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
  return updateLiveAssistant((message) => {
    return {
      ...message,
      segments: appendReasoningToMessageSegments(message.segments, deltaText),
    };
  }, { kind: "reasoning", text: deltaText });
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

function appendReasoningToMessageSegments(
  segments: MessageSegment[],
  delta: string,
): MessageSegment[] {
  const nextSegments = [...segments];
  for (let index = nextSegments.length - 1; index >= 0; index--) {
    const segment = nextSegments[index];
    if (segment.type !== "reasoning") {
      continue;
    }
    const previous = segment.content;
    // A persisted/final reasoning block may be followed by the first live
    // block for this turn.  Keep the visual separator used by the chat SSE
    // path, while preserving raw chunk boundaries once streaming is running.
    const separator =
      previous.length > 0 && segment.running !== true && !previous.endsWith("\n")
        ? "\n"
        : "";
    nextSegments[index] = {
      ...segment,
      content: previous + separator + delta,
      running: true,
    };
    return nextSegments;
  }
  nextSegments.push({ type: "reasoning", content: delta, running: true });
  return nextSegments;
}

function findLatestAssistantMessage(
  thread: Thread,
  predicate: (message: ChatMessage) => boolean = () => true,
) {
  for (let index = thread.messages.length - 1; index >= 0; index--) {
    const message = thread.messages[index];
    if (message.role !== "assistant" || !predicate(message)) {
      continue;
    }
    return { index, message };
  }

  return null;
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
): Thread {
  const payload = resolveBridgeToolPayload(event);
  if (!payload) {
    return thread;
  }
  const target = findLatestAssistantMessage(thread, (message) =>
    isLiveAssistantMessage(message, getRuntimeEventTurnId(event)),
  );
  if (!target) {
    return thread;
  }

  const closeReasoning = () => {
    const segments = closeRunningReasoningSegments(target.message.segments);
    // 没有在跑的推理段就返回原引用：单回合上千帧，逐帧换身份会让下游 memo 全部失效。
    return segments === target.message.segments
      ? thread
      : replaceMessageSegments(thread, target.index, segments);
  };

  if (frame.kind !== "tool") {
    return closeReasoning();
  }

  const toolName = getToolName(payload);
  // 既无工具调用 id、也无真实工具名（旧帧/残缺帧）时不落行：upsertToolSegment
  // 的键会退化成兜底名 “tool”，把互不相干的调用合并成一行假工具。
  if (!getToolCallId(payload) && toolName === "tool") {
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
    thread,
    target.index,
    segments,
    `${event.type.replace(/^chat\.sse\./, "")}:${toolName}`,
  );
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
  if (event.type.startsWith("chat.sse.")) {
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

function isLiveAssistantMessage(
  message: ChatMessage,
  eventTurnId: string,
): boolean {
  // New messages carry an explicit streaming bit. The label fallback keeps
  // compatibility with callers/tests created before the bit was introduced.
  if (message.streaming === false || message.interrupted) {
    return false;
  }
  if (message.streaming !== true && message.label !== "streaming") {
    return false;
  }
  // 与 matchesActiveTurn / RuntimeDeltaCoordinator.claim 共用同一语义：
  // 「未知」不等于「其他 turn」，只有两边都明确且不一致才拒绝。
  //
  // 旧实现要求「两边都为空」才放行，于是真后端最常见的两种形态——消息带
  // chat turn 身份而 runtime 事件缺 turn_id，或反过来（两条通道的 turn 身份
  // 空间本就不同）——会被整条拒绝：增量帧全部到达却一帧也写不进消息，
  // 打字机退化成「turn 结束后一次性定型」（实测两路各约 1000 帧、DOM 全程
  // 不动，24s 时整块蹦出）。回放安全由 hook 层 renderLiveDeltas 闸门兜底：
  // 只在请求进行中应用增量，历史回放/reload 不走这条路径。
  if (message.runtimeTurnId && eventTurnId) {
    return message.runtimeTurnId === eventTurnId;
  }
  return true;
}
