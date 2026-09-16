// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type ChatMessage, type MessageSegment } from "@/data/mock";

import { buildStreamingMessageSegments } from "./events";
import { type GeneratedImageAttachments, isGeneratedImageSegment, upsertGeneratedImageSegment } from "./generated-images";

export const STREAM_PLACEHOLDER_TEXT = "...";

export type ToolMessageSegment = Extract<MessageSegment, { type: "tool" }>;

export type ReasoningMessageSegment = Extract<
  MessageSegment,
  { type: "reasoning" }
>;

/**
 * Reconcile a live assistant buffer with a terminal result without allowing a
 * shorter terminal snapshot to erase text that was already displayed.  When
 * the two buffers diverge, the longer one wins; when one is a prefix of the
 * other this also preserves the normal authoritative-result extension case.
 */
export function reconcileRuntimeText(
  liveText: string | null | undefined,
  resultText: string | null | undefined,
): string {
  const live = typeof liveText === "string" ? liveText : "";
  const result = typeof resultText === "string" ? resultText : "";
  if (!live.trim()) {
    return result;
  }
  if (!result.trim()) {
    return live;
  }
  if (live === result) {
    return result;
  }
  if (live.startsWith(result)) {
    return live;
  }
  if (result.startsWith(live)) {
    return result;
  }
  return result.length >= live.length ? result : live;
}

/** Extract text already rendered for one assistant message (placeholder-safe). */
export function getAssistantMessageText(message: ChatMessage): string {
  const values = message.segments
    .filter((segment): segment is Extract<MessageSegment, { type: "text" }> =>
      segment.type === "text",
    )
    .map((segment) => segment.content);
  if (
    message.streaming &&
    values.length === 1 &&
    values[0] === STREAM_PLACEHOLDER_TEXT
  ) {
    return "";
  }
  return values.join("");
}

/** Extract reasoning already rendered for one assistant message. */
export function getAssistantMessageReasoning(message: ChatMessage): string {
  return message.segments
    .filter(
      (segment): segment is ReasoningMessageSegment =>
        segment.type === "reasoning",
    )
    .map((segment) => segment.content)
    .join("");
}

/**
 * 结束当前仍在跑的推理段（`running: true` → `false`）。
 *
 * 推理行只有在 `running` 为真时才显示「推理中…」与转圈（见
 * components/workspace/message-reasoning-row.tsx）。此前这个标记只会被
 * 「整条消息按最终快照重建」清掉，于是模型已经进入工具/正文阶段、推理块早就
 * 写完了，行上仍挂着运行态——观感就是「页面一直在推理」。工具帧与首个正文
 * 分片到达时调用本函数，让渲染层及时切回已完成的推理行。
 *
 * 无段可改时返回**原数组引用**：runtime 事件按帧触发（单回合上千帧），
 * 每次分配新数组会让下游 memo 全部失效。
 */
export function closeRunningReasoningSegments(segments: MessageSegment[]) {
  let changed = false;
  const nextSegments = segments.map((segment) => {
    if (segment.type !== "reasoning" || segment.running !== true) {
      return segment;
    }
    changed = true;
    return { ...segment, running: false };
  });
  return changed ? nextSegments : segments;
}

type AssistantMessageSegmentOptions = {
  status?: "streaming" | "stopped";
  reasoningRunning?: boolean;
  existingSegments?: MessageSegment[];
  generatedImages?: GeneratedImageAttachments;
  /**
   * 分块推理：第 k 项是第 k 块推理的完整文本（与消息里第 k 个推理段一一对应），
   * `reasoningBlockToolCounts[k]` 是该块开始时已经存在的工具行数。
   *
   * 一个回合里推理与工具是**交替**出现的（推理 → 工具 → 工具 → 推理 → 工具…），
   * 而 `reasoning` 参数只是整轮的纯文本拼接。旧实现每次提交都用它重建「唯一一段
   * 推理」并排到工具行之前，于是「推理 → 工具 → 推理 → 工具」被压成
   * 「推理(全部) → 工具 → 工具」——多轮思考只剩一行。
   */
  reasoningBlocks?: string[];
  reasoningBlockToolCounts?: number[];
};

export function buildAssistantMessageSegments(
  text: string,
  source: string,
  reasoning: string,
  options?: AssistantMessageSegmentOptions,
) {
  const built = buildStreamingMessageSegments(text, source, reasoning, {
    status: options?.status,
    reasoningRunning: options?.reasoningRunning,
  });
  const textSegments = built.filter((segment) => segment.type === "text");
  const callouts = built.filter((segment) => segment.type === "callout");
  const fallbackReasoning =
    built.find(
      (segment): segment is ReasoningMessageSegment =>
        segment.type === "reasoning",
    ) ?? null;
  const existing = options?.existingSegments ?? [];

  // 过程区（推理 / 工具 / 图片 / 富内容）的结构以**已渲染段**为准：顺序即到达
  // 顺序。旧实现从零重建过程区（只把工具与图片 upsert 回来），推理必然塌成
  // 一段，且推理相对工具行的位置信息（哪些工具在它之后）整段丢失。
  let body: MessageSegment[] = existing.filter(
    (segment) => segment.type !== "text" && segment.type !== "callout",
  );

  for (const segment of options?.generatedImages?.segments ?? []) {
    if (isGeneratedImageSegment(segment)) {
      body = upsertGeneratedImageSegment(body, segment);
      continue;
    }
    body.push(segment);
  }

  body = syncReasoningSegments(body, {
    blocks: options?.reasoningBlocks,
    blockToolCounts: options?.reasoningBlockToolCounts,
    fallback: fallbackReasoning,
    running: options?.reasoningRunning === true,
  });

  if (textSegments.length === 0) {
    return [...body, ...callouts];
  }

  // 正文段的位置必须是**稳定**的，否则直连通道每个 flush 都会重排一次：
  //
  // - 已有正文段：按它当前相对工具行的位置延续（在工具前就继续在前，在工具后
  //   就继续在后）。实时回合里正文是最后出现的阶段，第一次落位就在工具行之后，
  //   后续 flush 必须留在原地，不能跳回顶部。
  // - 还没有正文段：工具行已经存在时，正文排在它们之后（推理 → 工具 → 答复）。
  //   旧实现无条件「正文最前」，于是最终答复一开始流式就跳到工具行上方，
  //   与时间顺序相反，也与历史重放（工具回执独立成条、排在答复之前）不一致。
  const toolIndex = existing.findIndex((segment) => segment.type === "tool");
  const textIndex = existing.findIndex((segment) => segment.type === "text");
  const textGoesLast =
    textIndex >= 0
      ? toolIndex >= 0 && textIndex > toolIndex
      : body.some((segment) => segment.type === "tool");

  if (textGoesLast) {
    return [...body, ...textSegments, ...callouts];
  }

  // 正文落位：排在**起始推理块之后**，其余段（工具行 / 图片 / 富内容）相对顺序不变。
  // 旧实现无条件把正文放回数组首位，于是「推理 + 正文」的回合里推理行被挤到
  // 回答下方——与流式期的时间顺序（先思考、后作答）相反。
  let textInsertAt = 0;
  while (
    textInsertAt < body.length &&
    body[textInsertAt].type === "reasoning"
  ) {
    textInsertAt += 1;
  }
  return [
    ...body.slice(0, textInsertAt),
    ...textSegments,
    ...body.slice(textInsertAt),
    ...callouts,
  ];
}

type ReasoningSyncOptions = {
  blocks?: string[];
  blockToolCounts?: Array<number | undefined>;
  fallback: ReasoningMessageSegment | null;
  running: boolean;
};

/**
 * 把推理块同步进过程区：**已存在的推理段就地更新，缺失的块按工具行边界插入**，
 * 任何情况下都不把多段推理合并成一段。
 *
 * 两条写入通道（`/api/agent/chat` 的合帧结构快照 / runtime/stream 的逐帧增量）
 * 都以「到达顺序」为准：第 k 块推理落在第 `blockToolCounts[k]` 个工具行之后。
 * 没有显式分块信息时走 `deriveReasoningSync`：块结构从**现有段**读，整轮聚合文本
 * 只用来补它比已渲染内容多出来的那条尾巴。
 */
function syncReasoningSegments(
  segments: MessageSegment[],
  options: ReasoningSyncOptions,
): MessageSegment[] {
  const explicitBlocks = options.blocks ?? [];
  const derived = explicitBlocks.length
    ? { blocks: explicitBlocks, blockToolCounts: options.blockToolCounts ?? [] }
    : deriveReasoningSync(segments, options.fallback);
  if (!derived) {
    return segments;
  }
  const blockTexts = reconcileBlockTexts(derived.blocks, options.fallback);
  // 只有最后一块可以是「运行中」——更早的块都被工具 / 正文结束过了
  // （与 closeRunningReasoningSegments 同语义）。
  const blocks = blockTexts.map((content, index) => ({
    content,
    running: index === blockTexts.length - 1 ? options.running : false,
  }));

  const next = [...segments];
  for (let blockIndex = 0; blockIndex < blocks.length; blockIndex += 1) {
    const block = blocks[blockIndex];
    const targetIndex = findReasoningSegmentIndexes(next)[blockIndex];
    if (targetIndex === undefined) {
      const insertAt = reasoningBlockInsertIndex(
        next,
        derived.blockToolCounts[blockIndex],
      );
      next.splice(insertAt, 0, {
        type: "reasoning",
        content: block.content,
        running: block.running,
      });
      continue;
    }
    const current = next[targetIndex];
    if (current.type !== "reasoning") {
      continue;
    }
    next[targetIndex] = {
      ...current,
      // 两条通道都可能写同一块：保留更长的副本（与 reconcileRuntimeText 同语义），
      // 短的一方回退不会截断已经渲染出来的推理文本。
      content:
        block.content.length >= current.content.length
          ? block.content
          : current.content,
      running: block.running,
    };
  }

  // 不变式：只有**最后一段**推理可以是运行态（更早的块都被工具 / 正文结束过）。
  // 段数多于块数时（直连通道多写了一块）同样以末段为准，避免旧块一直转圈。
  const reasoningIndexes = findReasoningSegmentIndexes(next);
  const runningIndex = reasoningIndexes[reasoningIndexes.length - 1];
  return next.map((segment, index) =>
    segment.type === "reasoning"
      ? { ...segment, running: index === runningIndex ? options.running : false }
      : segment,
  );
}

function findReasoningSegmentIndexes(segments: MessageSegment[]): number[] {
  const indexes: number[] = [];
  segments.forEach((segment, index) => {
    if (segment.type === "reasoning") {
      indexes.push(index);
    }
  });
  return indexes;
}

/**
 * 第 k 块推理的落位：
 * - `toolCount` 未给出（历史 / 单块回退）或为 0 → 落在**首个工具行之前**
 *   （没有工具行时就是过程区最前），保持「先思考、后调工具」的时间顺序；
 * - 否则 → 落在第 `toolCount` 个工具行之后：该工具行结束了上一块推理，
 *   本块是工具执行之后新起的思考；
 * - 工具行不足 `toolCount` 个（另一条通道少写了行）→ 追加到过程区末尾。
 */
function reasoningBlockInsertIndex(
  segments: MessageSegment[],
  toolCount: number | undefined,
): number {
  const remainingTarget = toolCount ?? 0;
  if (remainingTarget <= 0) {
    const firstTool = segments.findIndex((segment) => segment.type === "tool");
    return firstTool >= 0 ? firstTool : 0;
  }

  let remaining = remainingTarget;
  for (let index = 0; index < segments.length; index += 1) {
    if (segments[index].type !== "tool") {
      continue;
    }
    remaining -= 1;
    if (remaining === 0) {
      return index + 1;
    }
  }
  return segments.length;
}

/** 过程区里的推理块正文（顺序 = 到达顺序：第 k 个推理段就是第 k 块）。 */
function readReasoningBlockTexts(segments: MessageSegment[]): string[] {
  return segments
    .filter(
      (segment): segment is ReasoningMessageSegment =>
        segment.type === "reasoning",
    )
    .map((segment) => segment.content);
}

/** 最后一个推理段的下标（没有推理段时为 -1）。 */
function lastReasoningSegmentIndex(segments: MessageSegment[]): number {
  for (let index = segments.length - 1; index >= 0; index -= 1) {
    if (segments[index].type === "reasoning") {
      return index;
    }
  }
  return -1;
}

/**
 * 尾块是否已经被工具行「关上」。关上的块不再吸收后续推理文本——工具之后到达的
 * 推理属于**新的一块**（一个回合里推理与工具交替出现：推理 → 工具 → 推理 → …）。
 */
function isLastReasoningBlockClosed(segments: MessageSegment[]): boolean {
  const lastReasoningIndex = lastReasoningSegmentIndex(segments);
  if (lastReasoningIndex < 0) {
    return false;
  }
  return segments
    .slice(lastReasoningIndex + 1)
    .some((segment) => segment.type === "tool");
}

/** 插到 `index` 位置之前的工具行数（`blockToolCounts` 的计量单位）。 */
function countToolSegmentsBefore(
  segments: MessageSegment[],
  index: number,
): number {
  let count = 0;
  for (let current = 0; current < index; current += 1) {
    if (segments[current]?.type === "tool") {
      count += 1;
    }
  }
  return count;
}

/**
 * 没有显式分块信息时的兜底：块结构从**现有段**读（顺序即到达顺序），整轮聚合文本
 * （`fallback` = 结果快照 / 定稿协调后的更长副本 / 直连通道的 `reasoningText`）只用
 * 来补它比已渲染内容多出来的那条尾巴：
 * - 尾块**还没被工具行关上** → 尾巴并进尾块（同一块的增长，且保留更长副本）；
 * - 尾块已被工具行关上 → 尾巴是**新的一块**，插到那串工具行之后。
 *
 * 绝不用聚合文本把多块压回一块：那正是「推理 → 工具 → 推理 → 工具」被渲染成
 * 「一段推理 + 一串工具」的老 bug。
 */
function deriveReasoningSync(
  segments: MessageSegment[],
  fallback: ReasoningMessageSegment | null,
): { blocks: string[]; blockToolCounts: Array<number | undefined> } | null {
  const rendered = readReasoningBlockTexts(segments);
  const flat = fallback?.content ?? "";
  if (rendered.length === 0) {
    // 还没有任何推理段：只有聚合文本可用，落在首个工具行之前（先思考、后调工具）。
    return flat ? { blocks: [flat], blockToolCounts: [0] } : null;
  }
  const joined = rendered.join("");
  if (!flat || !flat.startsWith(joined) || flat.length <= joined.length) {
    // 聚合文本没有新内容（或与已渲染内容对不上）：只修正运行态，不动块结构。
    return { blocks: rendered, blockToolCounts: [] };
  }
  const tail = flat.slice(joined.length);
  if (!isLastReasoningBlockClosed(segments)) {
    const extended = [...rendered];
    extended[extended.length - 1] += tail;
    return { blocks: extended, blockToolCounts: [] };
  }
  const reasoningIndex = lastReasoningSegmentIndex(segments);
  let insertAt = reasoningIndex + 1;
  while (insertAt < segments.length && segments[insertAt].type === "tool") {
    insertAt += 1;
  }
  const blockToolCounts: Array<number | undefined> = new Array(
    rendered.length + 1,
  ).fill(undefined);
  blockToolCounts[rendered.length] = countToolSegmentsBefore(segments, insertAt);
  return { blocks: [...rendered, tail], blockToolCounts };
}

/**
 * 聚合推理文本比各块之和尚长时，多出来的尾巴属于**最后一块**（块划分已经确定：
 * 要么来自显式分块信息，要么来自现有段 + 工具行边界，因此这里合并尾巴不会把两块
 * 并成一块）。两条通道都可能只写了一半：定稿快照 / 结果里的 reasoning 往往比已经
 * 渲染出来的更长，短的一方回退不能截断已经显示的文本。
 */
function reconcileBlockTexts(
  blocks: string[],
  fallback: ReasoningMessageSegment | null,
): string[] {
  const flat = fallback?.content ?? "";
  if (blocks.length === 0 || !flat) {
    return blocks;
  }
  const joined = blocks.join("");
  if (flat.length <= joined.length || !flat.startsWith(joined)) {
    return blocks;
  }
  const reconciled = [...blocks];
  reconciled[reconciled.length - 1] += flat.slice(joined.length);
  return reconciled;
}
