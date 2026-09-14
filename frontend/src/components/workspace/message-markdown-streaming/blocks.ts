import type { MarkdownBlockSpan } from "./types";

/**
 * 前缀冻结时允许不稳定的尾部块数：除尾部这么多块外全部冻结。
 *
 * 取 2 而不是 1，是因为一个「看起来已结束」的块仍可能被后一个块改写容器归属：
 * 松散列表（`- a` 空行 `- b`）、多段引用、跨空行的表格都跨空行、却属于同一个解析块，
 * 提前冻结会把它们渲染成两个独立块，settled 后又要跳变。
 */
export const UNSTABLE_TAIL_BLOCKS = 2;

type MarkdownBlockDraft = MarkdownBlockSpan & {
  lastLineEnd: number;
};

function classifyContainerLine(
  line: string,
  current: MarkdownBlockDraft | null,
): MarkdownBlockSpan["container"] {
  if (/^[ \t]{0,3}> ?/.test(line)) {
    return "quote";
  }
  if (/^[ \t]{0,3}(?:[-*+] |\d+[.)] )/.test(line)) {
    return "list";
  }
  // 缩进续行沿用当前容器（列表项换行 / 引用内部换行）。
  if (current && current.container !== "plain" && /^[ \t]{2,}\S/.test(line)) {
    return current.container;
  }
  return "plain";
}

/**
 * 以空行切分顶层块，并把同一容器上下文（列表 / 引用）跨空行合并为一块。
 * 切割点取块的 `end`（下一块起点），因此任意 `content.slice(0, end)` 都是逐字一致的前缀。
 */
export function scanMarkdownBlocks(content: string): MarkdownBlockSpan[] {
  const blocks: MarkdownBlockSpan[] = [];
  if (!content) {
    return blocks;
  }

  let offset = 0;
  let current: MarkdownBlockDraft | null = null;
  let sawBlankLine = false;

  const closeCurrent = (end: number) => {
    if (current) {
      blocks.push({
        container: current.container,
        end,
        start: current.start,
      });
    }
    current = null;
  };

  for (const line of content.split("\n")) {
    const lineStart = offset;
    const lineEnd = offset + line.length;
    offset = lineEnd + 1;

    if (!line.trim()) {
      sawBlankLine = true;
      continue;
    }

    const container = classifyContainerLine(line, current);
    if (!current) {
      current = {
        container,
        end: lineEnd,
        lastLineEnd: lineEnd,
        start: lineStart,
      };
      sawBlankLine = false;
      continue;
    }

    const continuesContainer =
      sawBlankLine && container !== "plain" && container === current.container;
    if (!sawBlankLine || continuesContainer) {
      current.end = lineEnd;
      current.lastLineEnd = lineEnd;
      current.container = continuesContainer ? current.container : container;
      sawBlankLine = false;
      continue;
    }

    closeCurrent(lineStart);
    current = {
      container,
      end: lineEnd,
      lastLineEnd: lineEnd,
      start: lineStart,
    };
    sawBlankLine = false;
  }

  closeCurrent(content.length);
  return blocks;
}
