// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type ChatMessage, type MessageSegment } from "@/data/mock";

export function sanitizeArtifactToken(value: string) {
  return value
    .trim()
    .replace(/[^a-zA-Z0-9_-]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .toLowerCase();
}

export function stripFileExtension(value: string) {
  return value.replace(/\.[^.]+$/, "");
}

export function filepathBase(value: string) {
  return value.split(/[\\/]/).filter(Boolean).pop() || "";
}

export function truncateText(value: string, limit: number) {
  const normalized = value.trim().replace(/\s+/g, " ");
  if (normalized.length <= limit) {
    return normalized;
  }
  if (limit <= 3) {
    return normalized.slice(0, limit);
  }
  return `${normalized.slice(0, limit - 3)}...`;
}

export function getHistoryMessageAuthor(role: string) {
  switch (role) {
    case "assistant":
      return "Runtime assistant";
    case "system":
      return "System context";
    case "tool":
      return "Tool receipt";
    default:
      return "You";
  }
}

export function getPrimaryTextContent(message: ChatMessage) {
  return message.segments
    .filter(
      (segment): segment is Extract<MessageSegment, { type: "text" }> =>
        segment.type === "text",
    )
    .map((segment) => segment.content)
    .join("\n\n")
    .trim();
}

// 以下两个提取器原先住在 events.ts；因该文件触到 500 非空行门禁（方案 T3 只允许加一个
// 兄弟节点、不为门禁放宽阈值），按门禁建议搬到本「文本工具」模块，语义未改。

/** 不 trim 的文本提取：打字机增量必须保留原始空白（delta 语义）。 */
export function readRawTextValue(
  source: Record<string, unknown>,
  ...keys: string[]
) {
  for (const key of keys) {
    const value = source[key];
    if (typeof value === "string" && value.length > 0) {
      return value;
    }
  }
  return "";
}

export function readTextDelta(payload: Record<string, unknown> | undefined) {
  if (!payload) {
    return "";
  }
  return (
    readRawTextValue(payload, "delta", "content") ||
    (payload.text && typeof payload.text === "object"
      ? readRawTextValue(payload.text as Record<string, unknown>, "content", "delta")
      : "")
  );
}
