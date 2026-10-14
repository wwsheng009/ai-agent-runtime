// 由 lib/workspace-thread-state → history-units 拆分链路继续下沉（P0-2 行数门禁）。
// 本文件承载「历史投影 ↔ 既有实时消息」的段落合并/匹配辅助，以及回合收尾后的
// 尾部孤儿占位吸收（见 absorbTrailingOrphanTurnTailIntoHistory）。

import { type ChatMessage, type MessageSegment } from "@/data/mock";
// barrel（@/lib/chat-view/index）未导出 visible-text 的这两个判定，直接按子模块导入。
import { answerTextOf, hasVisibleText } from "@/lib/chat-view/visible-text";

import { mergeUniqueStrings } from "./shared";

/**
 * 段落的稳定去重键：工具按 toolCallId（无 id 回退名称+入参）、图片按
 * imageId/artifactId、文本类按内容。带内容键的段落保证「重复同步幂等」：
 * 同一段正文/推理/代码在实时段里已经渲染过时，历史侧不再追加第二份。
 */
export function segmentDedupeKey(segment: MessageSegment): string {
  if (segment.type === "tool") {
    return segment.toolCallId
      ? `tool:${segment.toolCallId}`
      : `tool:${segment.name}:${segment.argsSummary ?? ""}`;
  }
  if (segment.type === "text") {
    return `text:${segment.content}`;
  }
  if (segment.type === "reasoning") {
    return `reasoning:${segment.content}`;
  }
  if (segment.type === "code") {
    return `code:${segment.language}:${segment.code}`;
  }
  if (segment.type === "image") {
    if (segment.artifactId) {
      return `image:${segment.artifactId}`;
    }
    return segment.imageId ? `image:${segment.imageId}` : "";
  }
  if (segment.type === "image-placeholder") {
    return segment.imageId ? `image:${segment.imageId}` : "";
  }
  return "";
}

export function getMessageSegmentKeys(message: ChatMessage): Set<string> {
  const keys = new Set<string>();
  message.segments.forEach((segment) => {
    const key = segmentDedupeKey(segment);
    if (key) {
      keys.add(key);
    }
  });
  return keys;
}

/** 终答文本：最后一段 text 的内容（匹配时与 primary text 双口径比对）。 */
export function getFinalTextContent(message: ChatMessage): string {
  for (let index = message.segments.length - 1; index >= 0; index -= 1) {
    const segment = message.segments[index];
    if (segment.type === "text") {
      return segment.content.trim();
    }
  }
  return "";
}

/** 回合接近度：数字型 turn_id 按数值、其余按字典序；仅用于在候选间消歧。 */
function getHistoryTurnIdRecency(turnId: string): number | string | null {
  const trimmed = turnId.trim();
  if (!trimmed) {
    return null;
  }
  return /^\d+$/.test(trimmed) ? Number(trimmed) : trimmed;
}

export function isCloserTurnCandidate(candidate: ChatMessage, best: ChatMessage): boolean {
  const candidateTurnId = candidate.runtimeTurnId?.trim() ?? "";
  const bestTurnId = best.runtimeTurnId?.trim() ?? "";
  if (!candidateTurnId || !bestTurnId) {
    return false;
  }
  const candidateKey = getHistoryTurnIdRecency(candidateTurnId);
  const bestKey = getHistoryTurnIdRecency(bestTurnId);
  if (candidateKey === null || bestKey === null) {
    return candidateKey !== null;
  }
  if (typeof candidateKey === "number" && typeof bestKey === "number") {
    return candidateKey > bestKey;
  }
  if (typeof candidateKey === "string" && typeof bestKey === "string") {
    return candidateKey > bestKey;
  }
  return typeof candidateKey === "number";
}

/**
 * 历史聚合消息 → 既有实时消息的合并。
 *
 * 原则：身份（message_id / turn）决定「是不是同一条消息」，段落内容只决定
 * 「这一段有没有表达过」。
 * - 实时过程区（推理分块 / 工具行位置 / 代码段 / 正在增长的 running 标记）整体
 *   透传：历史只保存「合并后的一块推理 + 最终正文」，没有逐帧顺序信息，用它覆盖
 *   会把「推理 → 工具 → 推理」压成一段；
 * - 实时侧已有推理块时，历史推理是同一段思考的降级表达，丢弃；
 * - 历史里的工具行按 toolCallId（无 id 时按名称+入参）去重后才追加：同一调用
 *   在实时段里已经渲染过，重复追加就是「一轮对话两份工具结果」；
 * - 正文/代码/图片同理按内容键去重，保证重复同步（挂载、回合结束、降级对账）
 *   是幂等的。
 */
export function mergeHistoryIntoLiveMessage(
  matched: ChatMessage,
  fallback: ChatMessage,
  stableId: string,
): ChatMessage {
  const matchedKeys = getMessageSegmentKeys(matched);
  const liveHasReasoning = matched.segments.some(
    (segment) => segment.type === "reasoning",
  );
  const liveTextSegments = matched.segments.filter(
    (segment) => segment.type === "text",
  );
  const durableTextSegments = fallback.segments.filter(
    (segment) => segment.type === "text",
  );
  const liveTextContent = liveTextSegments
    .map((segment) => segment.content.trim())
    .join("\n");
  const durableTextContent = durableTextSegments
    .map((segment) => segment.content.trim())
    .join("\n");
  const textInSync =
    liveTextSegments.length > 0 && liveTextContent === durableTextContent;
  // 过程区以实时为准：推理分块 / 工具行位置 / 代码段 / 图片整体透传（历史没有
  // 逐帧顺序信息，覆盖会把「推理 → 工具 → 推理」压成一段）。正文与历史一致时
  // 也原样保留，避免「丢弃后重排」把既有段序打乱；不一致时由历史正文收口。
  const liveProcessSegments = matched.segments.filter(
    (segment) => segment.type !== "text" || textInSync,
  );
  // 实时侧没有推理块时，历史推理是唯一表达：插到正文之前（思考在上、回答在下）。
  const leadingReasoning = liveHasReasoning
    ? []
    : fallback.segments.filter((segment) => segment.type === "reasoning");
  // 正文以历史为准：与实时不一致（含历史为空）时实时正文被取代——实时正文停在
  // 半截时由持久化正文收口；历史为空则不补任何文本。
  const resolvedTextSegments = textInSync ? [] : durableTextSegments;
  const durableExtraSegments = fallback.segments.filter(
    (segment) => segment.type !== "text" && segment.type !== "reasoning",
  );
  const durableAdditions = durableExtraSegments.filter((segment) => {
    const key = segmentDedupeKey(segment);
    return !key || !matchedKeys.has(key);
  });
  const relatedArtifactIds = mergeUniqueStrings(
    ...(matched.relatedArtifactIds ?? []),
    ...(fallback.relatedArtifactIds ?? []),
  );

  return {
    ...matched,
    // Prefer durable runtime message_id once history exposes it.
    id: stableId || matched.id || fallback.id,
    role: fallback.role,
    author: matched.author || fallback.author,
    label: matched.label || fallback.label,
    runtimeTurnId: matched.runtimeTurnId || fallback.runtimeTurnId,
    relatedArtifactIds:
      relatedArtifactIds.length > 0 ? relatedArtifactIds : undefined,
    // 过程区在前、历史正文在后（与流式渲染的「思考在上、回答在下」一致）。
    segments: [
      ...leadingReasoning,
      ...liveProcessSegments,
      ...durableAdditions,
      ...resolvedTextSegments,
    ],
  };
}

/** 末个命中下标（ES2023 findLastIndex 之外的保守实现）。 */
function findLastAssistantIndex(messages: readonly ChatMessage[]): number {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index].role === "assistant") {
      return index;
    }
  }
  return -1;
}

type TrailingTurnTailAbsorption = {
  /** 被吸收的孤儿占位（无正文，只携带迟到过程段）。 */
  orphanIndex: number;
  /** 吸收目标：权威历史已确认的回合尾投影。 */
  projectionIndex: number;
};

/**
 * 回合收尾后的「尾部孤儿回合吸收」判据。
 *
 * 背景（2026-10-14 实测 session_20261013175416_O9Ughlac）：回合收尾后，runtime
 * 通道迟到桥接帧（tool_started/tool_finished 等）经 resolveLiveAssistantTarget
 * 补建的占位消息（`turn-<本地chat turn UUID>-assistant`）与历史投影消息（durable
 * message_id）并存——两个 turn 身份空间不同（服务端 `turn_…` vs chat 层 UUID），
 * mapSessionHistoryToMessages 的「turn_id 相等」合并判据永远打不中，占位作为
 * live-only 消息被追加在权威历史之后，工具过程行整块落在结论正文后面。
 *
 * 判据刻意不依赖跨空间 turn_id，只看「时序 + 内容形态 + 权威性」三重护栏：
 * 1. 占位必须是 thread 里**最后一条**助手消息，且无可见正文（answerTextOf 为空）
 *    ——带正文的占位是直连通道的正常在途/已停止回答，属于 live-only 保护语义；
 * 2. 其紧前一条必须是**权威历史已确认**的 assistant 投影（本页映射结果按 id 命中）
 *    ——活跃回合 / 未知回合绝不吸收（投影不在历史里 = 服务端还没确认）；
 * 3. 投影必须已结束流式（streaming !== true）——不往仍在直播的消息里写历史侧内容。
 *
 * 次级变体（重放/重载遗留）：紧前一条是**卡死在 streaming 标记**的占位（S）、
 * 再往前才是权威投影，且孤儿已非流式——同样吸收，S 原样保留（它有自己的收尾
 * 通道，不在历史同步里越权改写）。
 */
function findTrailingTurnTailAbsorption(
  messages: readonly ChatMessage[],
  mappedHistory: readonly ChatMessage[],
): TrailingTurnTailAbsorption | undefined {
  if (messages.length < 2) {
    return undefined;
  }
  const orphanIndex = findLastAssistantIndex(messages);
  if (orphanIndex <= 0) {
    return undefined;
  }
  const orphan = messages[orphanIndex];
  // 孤儿自身若还在流式（真正在途的直连回合），交给 live-only 语义，不吸收。
  if (orphan.streaming === true || orphan.interrupted === true) {
    return undefined;
  }
  if (hasVisibleText(answerTextOf(orphan))) {
    return undefined;
  }

  const tailIndex = orphanIndex - 1;
  const tail = messages[tailIndex];
  if (tail.role !== "assistant") {
    return undefined;
  }
  const tailId = tail.id.trim();
  if (!tailId || tailId === orphan.id.trim()) {
    return undefined;
  }
  if (tail.streaming !== true && mappedHistory.some((m) => m.id === tailId)) {
    return { orphanIndex, projectionIndex: tailIndex };
  }
  // 次级变体只认「本地卡死占位」：S 仍在 streaming 且**未被本页历史命中**。
  // S 已在 mappedHistory 里却仍 streaming 属歧义态（历史确认了身份但本地收尾
  // 未到达），归入更早消息可能张冠李戴——放弃吸收，交由下一次同步/会话刷新收敛。
  if (
    tail.streaming !== true ||
    orphanIndex < 2 ||
    mappedHistory.some((m) => m.id === tailId)
  ) {
    return undefined;
  }
  const previous = messages[tailIndex - 1];
  const previousId = previous.id.trim();
  if (
    previous.role === "assistant" &&
    previousId &&
    previousId !== tailId &&
    previous.streaming !== true &&
    mappedHistory.some((m) => m.id === previousId)
  ) {
    return { orphanIndex, projectionIndex: tailIndex - 1 };
  }
  return undefined;
}

/**
 * 回合收尾后，把「落在权威历史尾部之后、仅携带迟到过程段的无正文占位」并入
 * 时间线上同回合的投影消息。幂等：合并走 mergeHistoryIntoLiveMessage 的段落
 * 键去重，重复同步不会产生第二份工具行/推理/正文；无守卫命中时原样返回。
 *
 * 合并方向（契约与 mapSessionHistoryToMessages 相同）：matched = live 侧（孤儿
 * 占位，携带迟到过程段）、fallback = durable 侧（投影，携带权威正文）；结果的
 * 身份字段随后回填为投影的——吸收的语义是「孤儿把迟到过程段交给投影后消失」，
 * 投影的 message_id / author / label / runtimeTurnId 是唯一可信身份。
 */
export function absorbTrailingOrphanTurnTailIntoHistory(
  messages: readonly ChatMessage[],
  mappedHistory: readonly ChatMessage[],
): ChatMessage[] {
  const absorption = findTrailingTurnTailAbsorption(messages, mappedHistory);
  if (!absorption) {
    return [...messages];
  }
  const projection = messages[absorption.projectionIndex];
  const orphan = messages[absorption.orphanIndex];
  const merged = mergeHistoryIntoLiveMessage(orphan, projection, projection.id);
  const nextMessages = [...messages];
  // 孤儿被并入投影：原占位行不再单独存在（这正是本次修复的目标——迟到工具行
  // 不再作为独立消息垫在结论正文后面）。
  nextMessages.splice(absorption.orphanIndex, 1);
  nextMessages[absorption.projectionIndex] = {
    ...merged,
    author: projection.author,
    label: projection.label,
    runtimeTurnId: projection.runtimeTurnId || merged.runtimeTurnId,
    streaming: projection.streaming,
    interrupted: projection.interrupted,
    usage: projection.usage ?? merged.usage,
  };
  return nextMessages;
}
