/**
 * P1-3：会话滚动所有权契约（纯函数 + 跨挂载锚点记忆）。
 *
 * 滚动宿主把「阅读位置」表达为**语义锚点**（消息 id + 相对视口顶部的偏移），
 * 而不是裸 `scrollTop`：prepend/回流/remount 之后按锚点重算滚动位置，
 * 「贴底跟随」与「阅读保顶」互斥切换（§7.4），active Turn 由 reading-line
 * 几何命中决定。这里只放不依赖 DOM 的算术与记忆，DOM 读写在
 * `hooks/workspace/use-conversation-scroll.ts`。
 */

/** 距底部小于该值视为「贴底」；与既有 SCROLL_FOLLOW_THRESHOLD 语义一致。 */
export const SCROLL_FOLLOW_THRESHOLD = 120;

/** reading-line：视口顶部往下 1/3 处，作为 active Turn 的几何基准。 */
export const READING_LINE_RATIO = 1 / 3;

export type ScrollMetrics = {
  clientHeight: number;
  scrollHeight: number;
  scrollTop: number;
};

/** 语义行（一条消息 = 一行）在滚动视口坐标系中的位置。 */
export type ReadingLineRow = {
  bottom: number;
  id: string;
  top: number;
};

export type ConversationScrollAnchor = {
  messageId: string;
  /** 锚点行相对滚动视口顶部的偏移（px）。 */
  offsetTop: number;
};

export function distanceFromBottom(metrics: ScrollMetrics): number {
  return metrics.scrollHeight - metrics.scrollTop - metrics.clientHeight;
}

export function isAwayFromBottom(
  metrics: ScrollMetrics,
  threshold: number = SCROLL_FOLLOW_THRESHOLD,
): boolean {
  return distanceFromBottom(metrics) > threshold;
}

export function maxScrollTop(metrics: ScrollMetrics): number {
  return Math.max(0, metrics.scrollHeight - metrics.clientHeight);
}

export function clampScrollTop(value: number, max: number): number {
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.max(0, Math.min(value, Math.max(0, max)));
}

/**
 * 锚点恢复：让锚点行停留在原来的视口偏移上。
 *
 * `currentOffsetTop` 是锚点行**现在**的偏移（布局已变），把它拉回
 * `anchorOffsetTop` 所需的 scrollTop 增量就是两者之差；越界由 clamp 收口
 * （锚点贴近底部时对齐最大滚动量，等价于贴底）。
 */
export function resolveAnchorScrollTop(input: {
  anchorOffsetTop: number;
  currentOffsetTop: number;
  max: number;
  scrollTop: number;
}): number {
  return clampScrollTop(
    input.scrollTop + (input.currentOffsetTop - input.anchorOffsetTop),
    input.max,
  );
}

/**
 * reading-line 命中的行下标：最后一个 `top <= readingLine` 的行；
 * 行都在基准线之下时取第 0 行，都在之上时取最后一行。空数组返回 -1。
 */
export function pickReadingLineIndex(
  rows: readonly ReadingLineRow[],
  readingLine: number,
): number {
  if (rows.length === 0) {
    return -1;
  }
  let index = 0;
  for (let i = 0; i < rows.length; i += 1) {
    if (rows[i].top <= readingLine) {
      index = i;
    } else {
      break;
    }
  }
  return index;
}

/** 从语义行里取锚点：reading-line 命中的那行就是「正在阅读的行」。 */
export function pickConversationAnchor(
  rows: readonly ReadingLineRow[],
  clientHeight: number,
): ConversationScrollAnchor | null {
  const index = pickReadingLineIndex(rows, clientHeight * READING_LINE_RATIO);
  if (index < 0) {
    return null;
  }
  const row = rows[index];
  return { messageId: row.id, offsetTop: row.top };
}

// ---- 跨挂载锚点记忆（会话切换 / remount 后恢复阅读位置）----

const anchorMemory = new Map<string, ConversationScrollAnchor>();

export function rememberConversationAnchor(
  key: string,
  anchor: ConversationScrollAnchor,
): void {
  anchorMemory.set(key, anchor);
}

export function recallConversationAnchor(
  key: string,
): ConversationScrollAnchor | null {
  return anchorMemory.get(key) ?? null;
}

export function forgetConversationAnchor(key: string): void {
  anchorMemory.delete(key);
}

/** 测试用：清空记忆，避免用例间互相污染。 */
export function resetConversationScrollMemory(): void {
  anchorMemory.clear();
}
