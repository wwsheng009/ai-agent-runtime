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
