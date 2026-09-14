export type StreamingMarkdownTailMode = "plain" | "markdown" | null;

export type StreamingMarkdownParts = {
  stableContent: string;
  tailContent: string;
  tailMode: StreamingMarkdownTailMode;
  /** 冻结切割点（= 尾部起点）在全文中的绝对 offset，`stableContent` 即 `[0, stableEndOffset)` 切片。 */
  stableEndOffset: number;
  /** 输入（已归一化行尾）总长度，供渲染层生成绝对 offset key。 */
  totalOffset: number;
  /** 冻结前缀的块切片（绝对 offset），渲染层按 `start` 生成跨边界稳定的 key。 */
  stableBlocks: MarkdownBlockSpan[];
  /** 不稳定尾部的块切片；最后一个块的 `start` 即尾部元素的 key。 */
  tailBlocks: MarkdownBlockSpan[];
};

export type StreamingCodeFence = {
  code: string;
  info: string;
  language: string;
  marker: string;
  /** 第二前沿：已完结的整行代码（保持 `\n` 结尾，可能为空串）。 */
  stableCode: string;
  /** 第二前沿：仍在增长的 partial 行（最后一个 `\n` 之后的文本，可能为空串）。 */
  partialLine: string;
};

export type MarkdownBlockSpan = {
  /** 块在全文中的绝对起始 offset（渲染 key 的来源，跨冻结边界稳定）。 */
  start: number;
  /** 下一块的起始 offset：`[start, end)` 含块文本与尾随分隔空白，切片逐字一致且无空洞。 */
  end: number;
  /** 容器上下文，用于把跨空行的松散列表 / 多段引用合并为同一个块。 */
  container: "list" | "quote" | "plain";
};

export type StreamingStructuredTail =
  | {
      kind: "blockquote";
      paragraphs: string[];
    }
  | {
      kind: "list";
      items: string[];
      ordered: boolean;
      start: number | null;
    }
  | {
      alignments: Array<"left" | "center" | "right" | null>;
      headers: string[];
      kind: "table";
      rows: string[][];
    };

export type StreamingPlainTail = {
  activeText: string;
  mode: "line" | "sentence";
  stableText: string;
};
