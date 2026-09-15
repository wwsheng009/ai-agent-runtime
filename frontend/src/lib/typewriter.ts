/**
 * typewriter —— 流式文本「渐进揭示」的纯状态机（无 DOM / 无定时器，可单测）。
 *
 * 背景：批次 D3（9bd162e8）曾删除旧打字机，根因是旧实现用 `content.slice(0, shown)`
 * 且 `shown` 会在 active 翻转 / 内容重建时**回退**，导致 `MessageMarkdown` 的冻结
 * 前缀比对（`stableContent.startsWith(上一帧)`）判成「内容被改写」→ `generation++`
 * → 已冻结块 remount 重解析。本状态机据此定下四条硬约束：
 *
 * 1. **单调**：同一段目标文本的揭示量只增不减，任何中间态都是上一帧的**前缀扩展**
 *    —— 冻结块因此永远命中前缀复用，不会 remount。
 * 2. **只对「正在增长」的文本打字**：挂载即完整（历史回放 / 历史同步重建）时直挂
 *    全文，不从头重打；只有观察到目标文本在公共前缀之后**变长**才进入逐字模式。
 * 3. **按时间步进 + 自适应追赶**：速率随积压量线性增长，大 chunk（一次性长文）能
 *    快速追平，小 chunk 保留逐字节奏；单帧计入时长有上限，长任务 / 后台标签页
 *    恢复后不会一次性冲刷出整段。
 * 4. **零成本退化**：非流式（历史消息 / 已结束 / 被打断）时返回原文，不进入动画。
 */

/** 基础揭示速率（字符/秒）：无积压时的「打字」手感。 */
export const BASE_CPS = 90;
/** 追赶系数：每 1 个积压字符额外增加的速率（字符/秒）。 */
export const CATCH_UP_CPS_PER_PENDING = 6;
/** 速率上限（字符/秒）：防止极端积压下单帧写出巨量 DOM。 */
export const MAX_CPS = 24000;
/** 单帧最大计入时长（毫秒）：长任务 / 后台标签页恢复后的上限。 */
export const MAX_FRAME_MS = 100;

export type TypewriterState = {
  /** 最近一次看到的目标全文（判定「追加」还是「改写」的基准）。 */
  target: string;
  /** 已揭示的字符数（高水位）。 */
  revealed: number;
  /** 是否仍在逐字追赶。 */
  animating: boolean;
};

/** 初始状态：挂载即完整（历史回放不会被从零重打）。 */
export function createTypewriterState(content: string): TypewriterState {
  return { target: content, revealed: content.length, animating: false };
}

/** 两个字符串的公共前缀长度（判定「追加 / 改写」）。 */
export function commonPrefixLength(a: string, b: string): number {
  const max = Math.min(a.length, b.length);
  let index = 0;
  while (index < max && a.charCodeAt(index) === b.charCodeAt(index)) {
    index += 1;
  }
  return index;
}

/**
 * 把揭示落点吸附到码点边界。落点正好压在代理对的 low surrogate 上时：
 * 能退到代理对起点就退（下一帧再成对揭示），否则（退回去等于没进展）整对揭示。
 */
export function snapToCodePointBoundary(
  text: string,
  from: number,
  to: number,
): number {
  if (to <= from || to >= text.length) {
    return to;
  }
  const code = text.charCodeAt(to);
  if (code < 0xdc00 || code > 0xdfff) {
    return to;
  }
  const pairStart = to - 1;
  return pairStart > from ? pairStart : to + 1;
}

/** 目标文本变化时的状态推进（纯函数，渲染期可安全调用）。 */
export function advanceOnTargetChange(
  prev: TypewriterState,
  content: string,
  active: boolean,
): TypewriterState {
  if (prev.target === content) {
    return prev;
  }
  if (!active || content.length === 0) {
    return { target: content, revealed: content.length, animating: false };
  }
  const common = commonPrefixLength(prev.target, content);
  if (common < prev.target.length) {
    // 改写（不是纯追加）：保留不了任何「已显示」的前缀语义，直接全量显示。
    // 这里刻意不做「从头重打」——那会让整段文本在视觉上回退一次，并且会连带
    // 把冻结块全部 remount（D3 的根因）。
    return { target: content, revealed: content.length, animating: false };
  }
  // 纯追加：上一轮在动画中则保持当前水位（不回退），否则从上一轮全长起打。
  const revealed = prev.animating ? prev.revealed : common;
  return {
    target: content,
    revealed,
    animating: revealed < content.length,
  };
}

/**
 * 单帧推进（纯函数）：按已用时长与积压量决定本帧揭示多少字符。
 * 已用时长在函数内截断到 `MAX_FRAME_MS`（长任务 / 后台标签页恢复后不会一次性
 * 冲刷出整段）；无进展时返回原对象（调用方可据此跳过 setState）。
 */
export function advanceReveal(
  prev: TypewriterState,
  elapsedMs: number,
): TypewriterState {
  if (!prev.animating) {
    return prev;
  }
  const pending = prev.target.length - prev.revealed;
  if (pending <= 0) {
    return { ...prev, animating: false };
  }
  const frameMs = Math.min(MAX_FRAME_MS, Math.max(0, elapsedMs));
  const cps = Math.min(MAX_CPS, BASE_CPS + pending * CATCH_UP_CPS_PER_PENDING);
  const step = Math.max(1, Math.round((cps * frameMs) / 1000));
  const next = snapToCodePointBoundary(
    prev.target,
    prev.revealed,
    prev.revealed + Math.min(step, pending),
  );
  if (next >= prev.target.length) {
    return { ...prev, revealed: prev.target.length, animating: false };
  }
  return { ...prev, revealed: next };
}

/** 当前应渲染的文本：非流式或已追平 → 全文（零成本直挂）。 */
export function visibleTypewriterText(
  state: TypewriterState,
  content: string,
  active: boolean,
): string {
  if (!active || !state.animating) {
    return content;
  }
  return content.slice(0, state.revealed);
}
