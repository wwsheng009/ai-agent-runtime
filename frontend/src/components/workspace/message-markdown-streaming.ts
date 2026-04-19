export type StreamingMarkdownTailMode = "plain" | "markdown" | null;

export type StreamingMarkdownParts = {
  stableContent: string;
  tailContent: string;
  tailMode: StreamingMarkdownTailMode;
};

export type StreamingCodeFence = {
  code: string;
  info: string;
  language: string;
  marker: string;
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

const EMPTY_STREAMING_MARKDOWN_PARTS: StreamingMarkdownParts = {
  stableContent: "",
  tailContent: "",
  tailMode: null,
};

const STRUCTURED_BLOCK_PATTERNS = [
  /^[ \t]{0,3}(?:[-*+] |\d+\. )/m,
  /^[ \t]{0,3}> /m,
  /^[ \t]{0,3}#{1,6}\s/m,
  /^[ \t]{0,3}(?:`{3,}|~{3,})/m,
  /^[ \t]{0,3}\|.*\|/m,
  /^[ \t]{0,3}(?:-{3,}|\*{3,}|_{3,})\s*$/m,
];

type FenceState = {
  index: number;
  marker: string;
};

export function normalizeMarkdownLineEndings(content: string) {
  return content.replace(/\r\n?/g, "\n");
}

function normalizeFenceLanguage(info: string) {
  const firstToken = info.trim().match(/^([^\s{]+)/)?.[1];
  return firstToken?.trim() || "text";
}

function splitTableRow(line: string) {
  return line
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((cell) => cell.trim());
}

function parseTableAlignmentCell(
  cell: string,
): "left" | "center" | "right" | null {
  if (!/^:?-{3,}:?$/.test(cell.trim())) {
    return null;
  }
  if (cell.startsWith(":") && cell.endsWith(":")) {
    return "center";
  }
  if (cell.endsWith(":")) {
    return "right";
  }
  if (cell.startsWith(":")) {
    return "left";
  }
  return null;
}

function isStructuredMarkdownBlock(content: string) {
  return STRUCTURED_BLOCK_PATTERNS.some((pattern) => pattern.test(content));
}

function findLastUnclosedFenceStart(content: string) {
  const fencePattern = /^[ \t]{0,3}(`{3,}|~{3,})[^\n]*$/gm;
  let activeFence: FenceState | null = null;

  for (const match of content.matchAll(fencePattern)) {
    const marker = match[1];
    const index = match.index ?? 0;
    if (!activeFence) {
      activeFence = { index, marker };
      continue;
    }

    if (
      marker[0] === activeFence.marker[0] &&
      marker.length >= activeFence.marker.length
    ) {
      activeFence = null;
      continue;
    }

    activeFence = { index, marker };
  }

  return activeFence?.index ?? null;
}

export function parseStreamingCodeFence(content: string): StreamingCodeFence | null {
  const normalized = normalizeMarkdownLineEndings(content);
  if (findLastUnclosedFenceStart(normalized) !== 0) {
    return null;
  }

  const match = /^(?:[ \t]{0,3})(`{3,}|~{3,})([^\n]*)(?:\n([\s\S]*))?$/.exec(
    normalized,
  );
  if (!match) {
    return null;
  }

  const marker = match[1];
  const info = (match[2] ?? "").trim();
  const code = (match[3] ?? "").replace(/\n$/, "");
  return {
    code,
    info,
    language: normalizeFenceLanguage(info),
    marker,
  };
}

export function parseStreamingStructuredTail(
  content: string,
): StreamingStructuredTail | null {
  const normalized = normalizeMarkdownLineEndings(content).trim();
  if (!normalized) {
    return null;
  }

  const blockquoteLines = normalized.split("\n");
  if (
    blockquoteLines.every((line) => !line.trim() || /^[ \t]{0,3}> ?/.test(line))
  ) {
    const paragraphs = blockquoteLines
      .map((line) => line.replace(/^[ \t]{0,3}> ?/, ""))
      .join("\n")
      .split(/\n{2,}/)
      .map((paragraph) => paragraph.trim())
      .filter(Boolean);
    if (paragraphs.length > 0) {
      return {
        kind: "blockquote",
        paragraphs,
      };
    }
  }

  const listLines = normalized.split("\n");
  const listItems: string[] = [];
  let listOrdered: boolean | null = null;
  let orderedStart: number | null = null;
  let currentItemIndex = -1;

  for (const line of listLines) {
    if (!line.trim()) {
      if (currentItemIndex >= 0) {
        listItems[currentItemIndex] = `${listItems[currentItemIndex]}\n`;
        continue;
      }
      currentItemIndex = -1;
      break;
    }

    const unorderedMatch = /^[ \t]{0,3}[-*+] +(.*)$/.exec(line);
    const orderedMatch = /^[ \t]{0,3}(\d+)\. +(.*)$/.exec(line);
    if (unorderedMatch || orderedMatch) {
      const isOrdered = Boolean(orderedMatch);
      if (listOrdered === null) {
        listOrdered = isOrdered;
      } else if (listOrdered !== isOrdered) {
        currentItemIndex = -1;
        break;
      }

      currentItemIndex += 1;
      if (orderedMatch && orderedStart === null) {
        orderedStart = Number(orderedMatch[1]);
      }
      listItems.push((unorderedMatch?.[1] ?? orderedMatch?.[2] ?? "").trim());
      continue;
    }

    if (/^[ \t]{2,}\S/.test(line) && currentItemIndex >= 0) {
      listItems[currentItemIndex] = `${listItems[currentItemIndex]}\n${line.trim()}`;
      continue;
    }

    currentItemIndex = -1;
    break;
  }

  if (listItems.length > 0 && currentItemIndex === listItems.length - 1) {
    return {
      kind: "list",
      items: listItems,
      ordered: Boolean(listOrdered),
      start: listOrdered ? orderedStart : null,
    };
  }

  const tableLines = normalized.split("\n");
  if (tableLines.length >= 2 && tableLines.every((line) => line.includes("|"))) {
    const headers = splitTableRow(tableLines[0]);
    const alignments = splitTableRow(tableLines[1]).map(parseTableAlignmentCell);
    const rows = tableLines.slice(2).map(splitTableRow);
    const isAlignmentRowValid =
      alignments.length === headers.length &&
      alignments.every((alignment) => alignment !== null);
    const areRowsValid =
      rows.length > 0 &&
      rows.every((row) => row.length === headers.length);

    if (headers.length > 0 && isAlignmentRowValid && areRowsValid) {
      return {
        kind: "table",
        headers,
        alignments,
        rows,
      };
    }
  }

  return null;
}

export function parseStreamingPlainTail(content: string): StreamingPlainTail | null {
  const normalized = normalizeMarkdownLineEndings(content);
  if (!normalized) {
    return null;
  }

  if (normalized.includes("\n")) {
    const lastLineBreak = normalized.lastIndexOf("\n");
    return {
      activeText: normalized.slice(lastLineBreak + 1),
      mode: "line",
      stableText: normalized.slice(0, lastLineBreak + 1),
    };
  }

  const sentenceBoundaryPattern = /[.!?。！？](?:["')\]]+)?\s+/g;
  let sentenceBoundaryIndex = -1;
  for (const match of normalized.matchAll(sentenceBoundaryPattern)) {
    sentenceBoundaryIndex = (match.index ?? 0) + match[0].length;
  }

  if (sentenceBoundaryIndex > 0 && sentenceBoundaryIndex < normalized.length) {
    return {
      activeText: normalized.slice(sentenceBoundaryIndex),
      mode: "sentence",
      stableText: normalized.slice(0, sentenceBoundaryIndex),
    };
  }

  const fallbackWordBoundary = normalized.lastIndexOf(" ");
  if (
    fallbackWordBoundary > 0 &&
    fallbackWordBoundary < normalized.length - 1 &&
    normalized.length >= 48
  ) {
    return {
      activeText: normalized.slice(fallbackWordBoundary + 1),
      mode: "sentence",
      stableText: `${normalized.slice(0, fallbackWordBoundary + 1)}`,
    };
  }

  return {
    activeText: normalized,
    mode: "sentence",
    stableText: "",
  };
}

export function normalizeMarkdown(content: string, streaming: boolean) {
  const normalized = normalizeMarkdownLineEndings(content);
  if (!streaming) {
    return normalized;
  }

  const unclosedFenceIndex = findLastUnclosedFenceStart(normalized);
  if (unclosedFenceIndex === null) {
    return normalized;
  }

  const markerMatch = /^[ \t]{0,3}(`{3,}|~{3,})/m.exec(
    normalized.slice(unclosedFenceIndex),
  );
  const closingMarker = markerMatch?.[1] ?? "```";
  return `${normalized}\n${closingMarker}`;
}

export function splitStreamingMarkdown(content: string): StreamingMarkdownParts {
  const normalized = normalizeMarkdownLineEndings(content);
  if (!normalized.trim()) {
    return EMPTY_STREAMING_MARKDOWN_PARTS;
  }

  const unclosedFenceIndex = findLastUnclosedFenceStart(normalized);
  if (unclosedFenceIndex !== null) {
    return {
      stableContent: normalized.slice(0, unclosedFenceIndex).replace(/\n+$/, ""),
      tailContent: normalized.slice(unclosedFenceIndex),
      tailMode: "markdown",
    };
  }

  if (normalized.endsWith("\n")) {
    return {
      stableContent: normalized,
      tailContent: "",
      tailMode: null,
    };
  }

  const lastParagraphBoundary = normalized.lastIndexOf("\n\n");
  if (lastParagraphBoundary < 0) {
    return {
      stableContent: "",
      tailContent: normalized,
      tailMode: isStructuredMarkdownBlock(normalized) ? "markdown" : "plain",
    };
  }

  const stableContent = normalized
    .slice(0, lastParagraphBoundary)
    .replace(/\n+$/, "");
  const tailContent = normalized.slice(lastParagraphBoundary + 2);
  return {
    stableContent,
    tailContent,
    tailMode: tailContent
      ? isStructuredMarkdownBlock(tailContent)
        ? "markdown"
        : "plain"
      : null,
  };
}
