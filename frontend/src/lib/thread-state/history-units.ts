// 由 history-mapping.ts 拆分而来（P0-2 行数门禁）：历史渲染单元聚合 + 实时合并。
//
// 历史消息行是**协议消息**而非 UI 消息：一个回合会落成「仅推理的 assistant 行 +
// 多条 tool 回执行 + 最终 assistant 行」。旧实现逐行投影，仅推理行与工具行因
// 为没有正文可比对，永远匹配不上实时消息，于是一轮对话被拆成多条顶层消息（过程
// 行按历史行序排在前、聚合消息在末尾），且同一份推理/工具会在合并消息里再出现
// 一次。这里按回合聚合、按身份合并、按段落键去重，保证重复同步幂等。

import { type Artifact, type ChatMessage, type MessageSegment } from "@/data/mock";
import { type SessionHistoryMessage, type SessionHistoryToolCall } from "@/types/runtime";

import { extractGeneratedImagesFromAssistantMessage } from "./generated-images";
import {
  buildHistoryArtifacts,
  normalizeSessionHistoryMessages,
} from "./history-artifacts";
import {
  buildHistoryToolSegment,
  extractHistoryReasoningText,
  indexHistoryToolCalls,
  readHistoryMessageIdentity,
} from "./history-mapping";
import { mergeUniqueStrings } from "./shared";
import { getHistoryMessageAuthor, getPrimaryTextContent } from "./text-utils";

export type HistoryMessageMapping = {
  artifacts: Artifact[];
  message: ChatMessage;
  /**
   * 命中并吸收了哪条既有消息（缺省 = 本单元产出的是新消息）。
   *
   * 上游据此做到两件事：`applySessionHistoryToThread` 不再把已被吸收的在途
   * 消息当 liveOnly 重复追加；`prependSessionHistoryToThread` 不会把命中既有
   * 消息的单元再前插成第二份。
   */
  matchedMessageId?: string;
};

type HistoryRowRef = {
  item: SessionHistoryMessage;
  index: number;
};

/**
 * 历史渲染单元：user/system 各自成条；assistant / tool 行按回合聚合为一条。
 *
 * 历史消息行是**协议消息**而非 UI 消息：一个回合会落成「仅推理的 assistant 行 +
 * 多条 tool 回执行 + 最终 assistant 行」。旧实现逐行投影，仅推理行与工具行因
 * 为没有正文可比对，永远匹配不上实时消息，于是一轮对话被拆成多条顶层消息（过程
 * 行按历史行序排在前、聚合消息在末尾），与实时消息合并后还会出现同一份推理/工具
 * 的两份表达。这里改为按回合聚合：过程行只作为聚合消息的 segment，顺序即历史行序。
 *
 * 回合身份优先用后端读取历史时补齐的 `metadata.turn_id`
 * （backend/internal/types.EnsureHistoryMessageIdentities）；缺失时退回
 * 「user/system 行即回合边界」的相邻分组。
 */
type HistoryRenderUnit =
  | { kind: "user" | "standalone"; rows: HistoryRowRef[]; turnId: string }
  | { kind: "assistant"; rows: HistoryRowRef[]; turnId: string };

function readHistoryRole(item: SessionHistoryMessage): string {
  return typeof item.role === "string" ? item.role.trim().toLowerCase() : "";
}

function readHistoryTurnId(item: SessionHistoryMessage): string {
  const metadata =
    item.metadata && typeof item.metadata === "object" ? item.metadata : undefined;
  const raw = metadata?.["turn_id"];
  return typeof raw === "string" ? raw.trim() : "";
}

function buildHistoryRenderUnits(
  history: SessionHistoryMessage[],
): HistoryRenderUnit[] {
  const units: HistoryRenderUnit[] = [];
  let current: Extract<HistoryRenderUnit, { kind: "assistant" }> | null = null;

  history.forEach((item, index) => {
    const row: HistoryRowRef = { item, index };
    const role = readHistoryRole(item);
    const turnId = readHistoryTurnId(item);
    if (role === "user" || role === "system") {
      units.push({
        kind: role === "user" ? "user" : "standalone",
        rows: [row],
        turnId,
      });
      current = null;
      return;
    }
    // assistant / tool（以及未知角色）：同一回合的过程条目聚合进一条消息。
    if (!current || (turnId && current.turnId && turnId !== current.turnId)) {
      current = { kind: "assistant", rows: [], turnId };
      units.push(current);
    }
    if (!current.turnId && turnId) {
      current.turnId = turnId;
    }
    current.rows.push(row);
  });

  return units;
}

/** 单条历史行 → segment 与产物；行本身不再决定消息边界（由渲染单元决定）。 */
function buildHistoryRowProjection(
  sessionId: string,
  row: HistoryRowRef,
  toolCalls: ReadonlyMap<string, SessionHistoryToolCall>,
): { artifacts: Artifact[]; segments: MessageSegment[] } {
  const generatedImageAttachments = extractGeneratedImagesFromAssistantMessage(
    row.item,
    sessionId,
  );
  const artifacts = buildHistoryArtifacts(
    sessionId,
    row.index,
    row.item,
    generatedImageAttachments.artifacts,
  );
  // 工具回执：历史里 role="tool" 独立成行，按其配对调用还原 tool segment，
  // 由过程区按 24px 工具行呈现（不再是通用「上下文注入」行）。
  if (readHistoryRole(row.item) === "tool") {
    const toolCallId =
      typeof row.item.tool_call_id === "string" ? row.item.tool_call_id.trim() : "";
    return {
      artifacts,
      segments: [
        buildHistoryToolSegment(
          row.item,
          toolCallId ? toolCalls.get(toolCallId) : undefined,
        ),
      ],
    };
  }
  const reasoningText = extractHistoryReasoningText(row.item.metadata);
  // 空消息不占位（§12.1.4）：工具回合 / 仅推理 / 仅附件的 assistant 消息 content
  // 为空是正常协议形态，不能降级成 "[empty message]" 文本段顶到过程区上屏。
  const contentText = row.item.content?.trim() ?? "";
  return {
    artifacts,
    // 推理先于正文落位：思考过程在上、正式回答在下。历史条目只保存最终正文
    // 与合并后的推理块（没有逐帧顺序信息），恢复时按固定顺序还原；
    // 旧实现把正文放前，页面就成了「先正文、后推理过程」。
    segments: [
      ...(reasoningText
        ? [{ type: "reasoning" as const, content: reasoningText }]
        : []),
      ...(contentText ? [{ type: "text" as const, content: contentText }] : []),
      ...generatedImageAttachments.segments,
    ],
  };
}

/**
 * 聚合消息的身份锚点：优先最后一条带上游 message_id 的 assistant 行（合并匹配用），
 * 其次最后一条带正文的 assistant 行（终答），都没有时退回最后一行。
 */
function pickHistoryUnitAnchor(
  unit: HistoryRenderUnit,
): HistoryRowRef | undefined {
  for (let index = unit.rows.length - 1; index >= 0; index -= 1) {
    const row = unit.rows[index];
    if (readHistoryRole(row.item) === "assistant" && readHistoryMessageIdentity(row.item)) {
      return row;
    }
  }
  for (let index = unit.rows.length - 1; index >= 0; index -= 1) {
    const row = unit.rows[index];
    if (readHistoryRole(row.item) === "assistant" && row.item.content?.trim()) {
      return row;
    }
  }
  return unit.rows[unit.rows.length - 1] ?? unit.rows[0];
}

type HistoryUnitProjection = {
  artifacts: Artifact[];
  stableId: string | undefined;
  /** 显式收窄为 ChatMessage：调用方拿到的映射结果不应是收窄的结构字面量 */
  message: ChatMessage;
};

function buildHistoryUnitMessage(
  sessionId: string,
  unit: HistoryRenderUnit,
  toolCalls: ReadonlyMap<string, SessionHistoryToolCall>,
): HistoryUnitProjection {
  const artifacts: Artifact[] = [];
  const segments: MessageSegment[] = [];
  const seenToolCallIds = new Set<string>();

  unit.rows.forEach((row) => {
    const projection = buildHistoryRowProjection(sessionId, row, toolCalls);
    artifacts.push(...projection.artifacts);
    projection.segments.forEach((segment) => {
      if (segment.type === "tool") {
        const toolCallId = segment.toolCallId?.trim() ?? "";
        if (toolCallId) {
          if (seenToolCallIds.has(toolCallId)) {
            return;
          }
          seenToolCallIds.add(toolCallId);
        }
      }
      segments.push(segment);
    });
  });

  const firstRow = unit.rows[0];
  const anchor = pickHistoryUnitAnchor(unit) ?? firstRow;
  const isUser = unit.kind === "user";
  const isStandalone = unit.kind === "standalone";
  const anchorRole = anchor ? readHistoryRole(anchor.item) : "";
  const stableId = anchor ? readHistoryMessageIdentity(anchor.item) : undefined;
  const relatedArtifactIds = artifacts.map((artifact) => artifact.id);

  const message: ChatMessage = {
    id:
      stableId ||
      // 无上游 message_id 时用回合身份兜底：与实时占位消息（turn-<id>-assistant）
      // 同形，重放 / 分页重投影时 id 不再随页内下标漂移。
      (!isUser && !isStandalone && unit.turnId
        ? `turn-${unit.turnId}-assistant`
        : `${sessionId}-history-${anchor?.index ?? 0}`),
    role: isUser ? "user" : "assistant",
    author: getHistoryMessageAuthor(
      isUser ? "user" : isStandalone ? anchorRole : "assistant",
    ),
    label: isUser
      ? firstRow?.item.role || "user"
      : isStandalone
        ? anchorRole || "runtime"
        : "assistant",
    relatedArtifactIds:
      relatedArtifactIds.length > 0 ? relatedArtifactIds : undefined,
    runtimeTurnId: unit.turnId || undefined,
    segments,
  };

  return { artifacts, stableId, message };
}

/**
 * 段落的稳定去重键：工具按 toolCallId（无 id 回退名称+入参）、图片按
 * imageId/artifactId、文本类按内容。带内容键的段落保证「重复同步幂等」：
 * 同一段正文/推理/代码在实时段里已经渲染过时，历史侧不再追加第二份。
 */
function segmentDedupeKey(segment: MessageSegment): string {
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

function getMessageSegmentKeys(message: ChatMessage): Set<string> {
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
function getFinalTextContent(message: ChatMessage): string {
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

function isCloserTurnCandidate(candidate: ChatMessage, best: ChatMessage): boolean {
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
function mergeHistoryIntoLiveMessage(
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

export function mapSessionHistoryToMessages(
  sessionId: string,
  history: SessionHistoryMessage[] | null | undefined,
  existingMessages: ChatMessage[],
) {
  const usedMessageIds = new Set<string>();
  const normalizedHistory = normalizeSessionHistoryMessages(history);
  const toolCalls = indexHistoryToolCalls(normalizedHistory);

  return buildHistoryRenderUnits(normalizedHistory).map((unit) => {
    const fallback = buildHistoryUnitMessage(sessionId, unit, toolCalls);
    const stableId = fallback.stableId?.trim() ?? "";
    const fallbackText = getPrimaryTextContent(fallback.message);
    const fallbackFinalText = getFinalTextContent(fallback.message);
    const anchorTurnId = fallback.message.runtimeTurnId?.trim() ?? "";
    // 匹配仅允许「身份」证据：上游 message_id、同一 turn（仅 assistant 聚合单元；
    // turn_id ↔ runtimeTurnId，含占位消息 id turn-<id>-assistant）、或正文一致。
    // 仅推理 / 工具行没有独立身份，绝不单独匹配，也绝不产出顶层消息——它们只是
    // 聚合消息的过程区。
    const canMatchByTurn = unit.kind === "assistant" && Boolean(anchorTurnId);
    const canMatchByText = Boolean(fallbackText);
    // user / system 行仍按 message_id / 正文与既有消息对齐，只是不参与 turn 匹配。
    const canMatch = Boolean(stableId) || canMatchByTurn || canMatchByText;

    let matched: ChatMessage | undefined;
    if (canMatch) {
      for (const message of existingMessages) {
        if (usedMessageIds.has(message.id)) {
          continue;
        }
        const isCandidate =
          (Boolean(stableId) && message.id === stableId) ||
          (canMatchByTurn && message.runtimeTurnId === anchorTurnId) ||
          (canMatchByText &&
            message.role === fallback.message.role &&
            (getPrimaryTextContent(message) === fallbackText ||
              getFinalTextContent(message) === fallbackFinalText));
        if (!isCandidate) {
          continue;
        }
        if (!matched || isCloserTurnCandidate(message, matched)) {
          matched = message;
        }
      }
    }

    if (!matched) {
      return {
        artifacts: fallback.artifacts,
        message: fallback.message,
      } satisfies HistoryMessageMapping;
    }

    usedMessageIds.add(matched.id);
    if (stableId) {
      usedMessageIds.add(stableId);
    }
    // 合并时按段落键去重：已表达过的工具结果 / 正文 / 图片绝不重复追加，
    // 未表达过的按历史行序补齐（见 mergeHistoryIntoLiveMessage）。
    return {
      artifacts: fallback.artifacts,
      message: mergeHistoryIntoLiveMessage(matched, fallback.message, stableId),
      matchedMessageId: matched.id,
    } satisfies HistoryMessageMapping;
  });
}
