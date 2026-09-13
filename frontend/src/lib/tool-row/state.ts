// P1-6：工具行状态机（纯逻辑）。组件只消费这里的呈现结论，不在 JSX 里散落条件判断。

import { type ToolMessageSegment } from "@/lib/thread-state/messages";

import { resolveToolSegmentDetails, type ToolSegmentDetails } from "./details";
import { resolveToolCardKind, type ToolCardKind } from "./kind";

export type ToolStatus = ToolMessageSegment["status"];

export type ToolStatusTone = "pending" | "running" | "success" | "danger";

export type ToolRowSummaryPart =
  | { type: "path"; path: string }
  | { type: "url"; url: string }
  | { type: "text"; text: string }
  | { type: "exitCode"; code: number }
  | { type: "diff"; additions?: number; removals?: number };

export type ToolRowSummary = {
  tone: "default" | "danger";
  parts: ToolRowSummaryPart[];
};

export type ToolRowPresentation = {
  kind: ToolCardKind;
  isFailure: boolean;
  expandable: boolean;
  summary: ToolRowSummary;
  attributes: Record<string, string>;
  /** 失败态禁用文件链接；成功态交由宿主解析后决定是否可交互。 */
  filePath: string | null;
  fileLinkDisabled: boolean;
};

export const STATUS_TONE: Record<ToolStatus, ToolStatusTone> = {
  started: "pending",
  running: "running",
  finished: "success",
  error: "danger",
};

export const STATUS_LABEL_KEY = {
  started: "panels.messages.toolRow.status.started",
  running: "panels.messages.toolRow.status.running",
  finished: "panels.messages.toolRow.status.finished",
  error: "panels.messages.toolRow.status.failed",
} as const satisfies Record<ToolStatus, string>;

export function isToolRowExpandable(segment: ToolMessageSegment) {
  return Boolean(segment.argsSummary?.trim());
}

function summaryForKind(
  kind: ToolCardKind,
  details: ToolSegmentDetails | undefined,
  segment: ToolMessageSegment,
): ToolRowSummaryPart[] {
  if (!details) {
    return [];
  }
  const parts: ToolRowSummaryPart[] = [];
  const path = details.filePath?.trim();

  if (kind === "read") {
    if (path) {
      parts.push({ type: "path", path });
    } else if (segment.resultSummary?.trim()) {
      parts.push({ type: "text", text: segment.resultSummary });
    }
    return parts;
  }

  if (kind === "diff") {
    if (path) {
      parts.push({ type: "path", path });
    }
    if (details.diff) {
      parts.push({ type: "diff", additions: details.diff.additions, removals: details.diff.removals });
    }
    return parts;
  }

  if (kind === "terminal") {
    if (details.command) {
      parts.push({ type: "text", text: details.command });
    }
    if (details.exitCode !== undefined && details.exitCode !== 0) {
      parts.push({ type: "exitCode", code: details.exitCode });
    }
    return parts;
  }

  if (kind === "search") {
    if (details.query) {
      parts.push({ type: "text", text: details.query });
    } else if (segment.resultSummary?.trim()) {
      parts.push({ type: "text", text: segment.resultSummary });
    }
    return parts;
  }

  if (kind === "web") {
    if (details.url) {
      parts.push({ type: "url", url: details.url });
    }
    return parts;
  }

  if (kind === "image") {
    if (path) {
      parts.push({ type: "path", path });
    }
    return parts;
  }

  return parts;
}

export function resolveToolRowPresentation(segment: ToolMessageSegment): ToolRowPresentation {
  const kind = resolveToolCardKind(segment.name);
  const isFailure = segment.status === "error";
  const details = resolveToolSegmentDetails(segment);

  // 失败态：输出块由错误块替换（不追加）；行内摘要只保留可跳转目标本身（禁用态）。
  const summary: ToolRowSummary = isFailure
    ? {
        tone: "danger",
        parts: details?.filePath?.trim()
          ? [{ type: "path", path: details.filePath.trim() }]
          : [],
      }
    : { tone: "default", parts: summaryForKind(kind, details, segment) };

  return {
    kind,
    isFailure,
    expandable: isToolRowExpandable(segment),
    summary,
    attributes: {
      "data-tool-row": "true",
      "data-tool-row-kind": kind,
      "data-tool-row-status": segment.status,
      "data-tool-row-name": segment.name,
      "data-tool-row-expandable": String(isToolRowExpandable(segment)),
      "data-tool-row-has-summary": String(summary.parts.length > 0),
    },
    filePath: details?.filePath?.trim() || null,
    fileLinkDisabled: isFailure,
  };
}
