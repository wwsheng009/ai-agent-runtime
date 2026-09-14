import type { StreamingCodeFence } from "./types";

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

export function findLastUnclosedFenceStart(content: string) {
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
  const body = match[3] ?? "";
  const code = body.replace(/\n$/, "");
  // 第二前沿：整行代码与仍在增长的 partial 行分开——半行代码交给 Prism 会得到
  // 错误 token（例如还没闭合的字符串会把后续内容整体染色），因此 partial 行按
  // 纯文本渲染，整行部分才是可缓存的高亮输入。
  const partialLine = body.endsWith("\n")
    ? ""
    : body.slice(body.lastIndexOf("\n") + 1);
  const stableCode = body.slice(0, body.length - partialLine.length);
  return {
    code,
    info,
    language: normalizeFenceLanguage(info),
    marker,
    partialLine,
    stableCode,
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
