/**
 * 推理内容稳定窗口裁剪（P2-6）。
 *
 * 对齐 DeepSeek-Reasonix `STREAMING_REASONING_WINDOW_STEP_CHARS` 思路：
 * 超长推理只渲染末尾稳定窗口，头部以省略提示折叠——流式期间渲染
 * 内容长度恒定（布局稳定），且避免每字符全量重渲染的 O(n²) 开销。
 *
 * - REASONING_WINDOW_STEP_CHARS：窗口推进粒度（每 N 字符更新一次显示）；
 * - REASONING_WINDOW_MAX_CHARS：保留的尾部窗口上限。
 */

export const REASONING_WINDOW_STEP_CHARS = 2000;

export const REASONING_WINDOW_MAX_CHARS = 8000;

export interface ReasoningWindowDisplay {
  /** 实际渲染的尾部窗口文本。 */
  visible: string;
  /** 被折叠的头部字符数（0 = 未裁剪）。 */
  droppedChars: number;
  /** 原始总长度。 */
  totalChars: number;
}

export function displayReasoningText(
  content: string,
  maxChars = REASONING_WINDOW_MAX_CHARS,
): ReasoningWindowDisplay {
  if (content.length <= maxChars) {
    return { visible: content, droppedChars: 0, totalChars: content.length };
  }
  return {
    visible: content.slice(-maxChars),
    droppedChars: content.length - maxChars,
    totalChars: content.length,
  };
}
