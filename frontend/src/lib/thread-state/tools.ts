// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type MessageSegment } from "@/data/mock";
import { extractToolDetails } from "@/lib/tool-row/details";
import { type AgentChatStreamChunkPayload } from "@/types/runtime";

import { readFirstTextValue, readFirstValue } from "./history-mapping";
import { type ToolMessageSegment } from "./messages";
import { getToolName } from "./shared";
import { truncateText } from "./text-utils";

export function getToolCallId(payload: AgentChatStreamChunkPayload) {
  if (payload.tool_call && typeof payload.tool_call === "object") {
    const id = readFirstTextValue(payload.tool_call, "id", "tool_call_id");
    if (id) {
      return id;
    }
  }
  if (payload.tool && typeof payload.tool === "object") {
    const id = readFirstTextValue(payload.tool, "id", "tool_call_id");
    if (id) {
      return id;
    }
  }
  if (payload.delta && typeof payload.delta === "object") {
    const id = readFirstTextValue(payload.delta, "id");
    if (id) {
      return id;
    }
  }
  return "";
}

export function getToolArgumentsSummary(payload: AgentChatStreamChunkPayload) {
  for (const source of [
    payload.tool,
    payload.tool_call,
    payload.delta,
  ]) {
    if (!source || typeof source !== "object") {
      continue;
    }
    const args =
      readFirstValue(source, "args", "arguments", "input", "params") ??
      source["arguments_json"];
    if (args === undefined || args === null) {
      continue;
    }
    if (typeof args === "string") {
      const trimmed = args.trim();
      if (trimmed) {
        return trimmed.length > 320 ? `${trimmed.slice(0, 317)}...` : trimmed;
      }
      continue;
    }
    if (typeof args === "object") {
      try {
        const serialized = JSON.stringify(args);
        return serialized.length > 320
          ? `${serialized.slice(0, 317)}...`
          : serialized;
      } catch {
        return String(args);
      }
    }
  }
  return "";
}

export function getToolResultSummary(payload: AgentChatStreamChunkPayload) {
  if (payload.tool && typeof payload.tool === "object") {
    const content = readFirstTextValue(payload.tool, "content", "result");
    if (content) {
      return truncateText(content, 240);
    }
  }
  if (payload.tool_call && typeof payload.tool_call === "object") {
    const content = readFirstTextValue(
      payload.tool_call,
      "content",
      "result",
      "output",
    );
    if (content) {
      return truncateText(content, 240);
    }
  }
  if (payload.content && payload.content.trim()) {
    return truncateText(payload.content, 240);
  }
  return "";
}

export function getToolErrorMessage(payload: AgentChatStreamChunkPayload) {
  if (payload.tool && typeof payload.tool === "object") {
    const error = readFirstTextValue(
      payload.tool,
      "error",
      "error_message",
      "message",
    );
    if (error) {
      return error;
    }
  }
  if (payload.tool_call && typeof payload.tool_call === "object") {
    const error = readFirstTextValue(
      payload.tool_call,
      "error",
      "error_message",
      "message",
    );
    if (error) {
      return error;
    }
  }
  if (payload.metadata && typeof payload.metadata === "object") {
    const error = readFirstTextValue(
      payload.metadata,
      "error",
      "error_message",
      "message",
    );
    if (error) {
      return error;
    }
  }
  return "";
}

export function buildToolSegmentFromPayload(
  payload: AgentChatStreamChunkPayload,
  status: ToolMessageSegment["status"],
): ToolMessageSegment {
  const name = getToolName(payload);
  return {
    type: "tool",
    toolCallId: getToolCallId(payload) || undefined,
    name,
    status,
    argsSummary: getToolArgumentsSummary(payload) || undefined,
    resultSummary:
      status === "finished" ? getToolResultSummary(payload) || undefined : undefined,
    errorMessage:
      status === "error"
        ? getToolErrorMessage(payload) || "Tool execution failed."
        : undefined,
    // P1-6：在截断 argsSummary 之前从原始入参/事件字段提取结构化明细（真实缺失即 undefined）。
    details: extractToolDetails(payload, name),
  };
}

export function getToolSegmentKey(segment: ToolMessageSegment) {
  return segment.toolCallId?.trim() || segment.name;
}

/**
 * 同一行的明细是**单调累积**的：实时帧只带预览（command / pattern），回合末尾巴才补
 * 完整入参与行级字段。后到帧缺失的字段必须保留先前解析结果，否则工具收尾的瞬间摘要
 * 行会把已经显示出来的参数擦掉（`{...previous, ...next}` 对未赋值键等价于覆盖）。
 */
function mergeToolDetails(
  previous: ToolMessageSegment["details"],
  next: ToolMessageSegment["details"],
) {
  if (!next) {
    return previous;
  }
  return previous ? { ...previous, ...next } : next;
}

export function upsertToolSegment(
  segments: MessageSegment[],
  nextSegment: ToolMessageSegment,
) {
  const key = getToolSegmentKey(nextSegment);
  if (!key) {
    return [...segments, nextSegment];
  }

  const merged: MessageSegment[] = [];
  let matched = false;

  for (const segment of segments) {
    if (segment.type !== "tool") {
      merged.push(segment);
      continue;
    }

    const currentKey = getToolSegmentKey(segment);
    if (currentKey !== key) {
      merged.push(segment);
      continue;
    }

    if (!matched) {
      matched = true;
      merged.push({
        ...segment,
        ...nextSegment,
        argsSummary: nextSegment.argsSummary?.trim()
          ? nextSegment.argsSummary
          : segment.argsSummary,
        details: mergeToolDetails(segment.details, nextSegment.details),
      });
    }
  }

  if (!matched) {
    merged.push(nextSegment);
  }

  return merged;
}
