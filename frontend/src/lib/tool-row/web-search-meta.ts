// P3-1：web_search 结构化结果的防御式解析。
//
// 数据来源：tool.completed 载荷与历史消息 metadata 里的 `sources` /
// `has_answer` / `answer_queries` / `truncated`（后端 P3-1 提升，见
// agent/tool_runtime_events.go 的 copyToolWebSearchMetadata）。
//
// 契约：只读取真实存在且类型正确的字段；任何畸形（非对象、字段类型不符、
// URL 缺失、重复 URL）都跳过对应条目；整体无可用信息时返回 null，调用方
// 回退现有文本渲染，绝不从渲染文本反推结构化数据。

const MAX_SOURCES = 20;

export type WebSearchSourceView = {
  title: string;
  url: string;
  snippet: string;
  publishedAt: string;
};

export type WebSearchMetaView = {
  sources: WebSearchSourceView[];
  hasAnswer: boolean;
  truncated: boolean;
  answerQueries: string[];
  /** P3-3 展示面：评分 tier（ok/low/empty）与可解释信号表；缺失为 null。 */
  quality: string | null;
  qualitySignals: Record<string, number> | null;
  /** P3-2 展示面：外部取消时被丢弃的查询；缺失为空。 */
  cancelledQueries: string[];
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function readText(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

function readSource(value: unknown): WebSearchSourceView | null {
  if (!isRecord(value)) {
    return null;
  }
  const url = readText(value.url);
  if (!url) {
    return null;
  }
  return {
    url,
    title: readText(value.title),
    snippet: readText(value.snippet),
    publishedAt: readText(value.published_at) || readText(value.publishedAt),
  };
}

function sourcesFrom(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  const sources: WebSearchSourceView[] = [];
  const seen = new Set<string>();
  for (const item of value) {
    if (sources.length >= MAX_SOURCES) {
      break;
    }
    const source = readSource(item);
    if (!source || seen.has(source.url)) {
      continue;
    }
    seen.add(source.url);
    sources.push(source);
  }
  return sources;
}

function answerQueriesFrom(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => readText(item))
    .filter((item) => item.length > 0)
    .slice(0, MAX_SOURCES);
}

function qualitySignalsFrom(value: unknown): Record<string, number> | null {
  if (!isRecord(value)) {
    return null;
  }
  const signals: Record<string, number> = {};
  for (const [key, raw] of Object.entries(value)) {
    if (typeof raw === "number" && Number.isFinite(raw)) {
      signals[key] = raw;
    }
  }
  return Object.keys(signals).length > 0 ? signals : null;
}

function textListFrom(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => readText(item))
    .filter((item) => item.length > 0)
    .slice(0, MAX_SOURCES);
}

function metaFromRecord(record: Record<string, unknown>): WebSearchMetaView | null {
  const sources = sourcesFrom(record.sources);
  // `has_answer` 是本仓库的显式键；`answer` 字符串兼容答案型后端的裸答案字段。
  const hasAnswer = record.has_answer === true || readText(record.answer) !== "";
  // P3-2/P3-3 展示面：取消清单/质量信号单独成立（失败结果没有 sources，
  // 但 cancelled_queries 也应在卡片上可解释）。
  const cancelledQueries = textListFrom(record.cancelled_queries);
  const qualitySignals = qualitySignalsFrom(record.quality_signals);
  if (sources.length === 0 && !hasAnswer && cancelledQueries.length === 0 && !qualitySignals) {
    return null;
  }
  return {
    sources,
    hasAnswer,
    truncated: record.truncated === true,
    answerQueries: answerQueriesFrom(record.answer_queries),
    quality: readText(record.quality) || null,
    qualitySignals,
    cancelledQueries,
  };
}

/**
 * 从候选记录集解析 web_search metadata：按 `metadata` / `meta` / `result` /
 * `output` 向下探索嵌套记录（真实载荷里 metadata 可能嵌在 tool / tool_call /
 * delta 内），命中第一条可用记录即返回；visited 集合兜底自引用结构。
 */
export function parseWebSearchMetaFromCandidates(
  candidates: readonly unknown[],
): WebSearchMetaView | null {
  const queue: unknown[] = [...candidates];
  const visited = new Set<unknown>();
  while (queue.length > 0) {
    const candidate = queue.shift();
    if (!isRecord(candidate) || visited.has(candidate)) {
      continue;
    }
    visited.add(candidate);
    const meta = metaFromRecord(candidate);
    if (meta) {
      return meta;
    }
    for (const key of [
      "metadata",
      "meta",
      "result",
      "output",
      "tool",
      "tool_call",
      "delta",
    ] as const) {
      const nested = candidate[key];
      if (isRecord(nested)) {
        queue.push(nested);
      }
    }
  }
  return null;
}

/** 域名列表主文本：解析失败时回退原始 URL（绝不隐藏数据）。 */
export function webSearchSourceDomain(url: string): string {
  const trimmed = url.trim();
  if (!trimmed) {
    return "";
  }
  try {
    const parsed = new URL(trimmed);
    return parsed.hostname.replace(/^www\./, "") || trimmed;
  } catch {
    return trimmed;
  }
}

// P3-1：把防御解析出的 web_search metadata 挂到工具明细上（details.ts 直接
// 复用；解析失败/无可用信息时不写入，面板保持原始文本块）。
export function attachWebSearchMetadata(
  details: { webSearch?: WebSearchMetaView },
  candidates: readonly unknown[],
) {
  const meta = parseWebSearchMetaFromCandidates(candidates);
  if (meta) {
    details.webSearch = meta;
  }
}
