// 由 hooks/workspace/use-session-backtrack.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import type { ChatMessage } from "@/data/mock";

import type { SessionBacktrackTarget } from "./types";

/** When edit text differs from the original, send edit_prompt to the runtime. */
export function resolveBacktrackEditPrompt(
  editPrompt: string,
  originalText: string,
): string | undefined {
  const edited = editPrompt.replace(/\r\n/g, "\n");
  const original = originalText.replace(/\r\n/g, "\n");
  if (edited.trim() === original.trim()) {
    return undefined;
  }
  // Allow intentionally clearing to empty only when original was non-empty.
  if (!edited.trim() && !original.trim()) {
    return undefined;
  }
  return edited;
}

/**
 * Seed the backtrack dialog edit box.
 * Inline transcript edit passes options.editPrompt; bare Backtrack uses original fullText.
 */
export function resolveSeededBacktrackEditPrompt(
  options: { editPrompt?: string } | undefined,
  originalText: string,
): string {
  return typeof options?.editPrompt === "string" ? options.editPrompt : originalText;
}

export function countUserTurnIndex(
  messages: ChatMessage[],
  messageId: string,
): { userTurnIndex: number; messageIndex: number } | null {
  let userTurnIndex = -1;
  for (let i = 0; i < messages.length; i++) {
    const message = messages[i];
    if (message.role === "user") {
      userTurnIndex += 1;
    }
    if (message.id === messageId) {
      if (message.role !== "user" || userTurnIndex < 0) {
        return null;
      }
      return { userTurnIndex, messageIndex: i };
    }
  }
  return null;
}

export function extractUserMessageText(message: ChatMessage): string {
  return message.segments
    .filter(
      (segment): segment is Extract<ChatMessage["segments"][number], { type: "text" }> =>
        segment.type === "text",
    )
    .map((segment) => segment.content)
    .join("\n")
    .replace(/\r\n/g, "\n")
    .trim();
}

export function extractUserMessagePreview(message: ChatMessage): string {
  const text = extractUserMessageText(message).replace(/\s+/g, " ").trim();
  if (!text) {
    return "(empty)";
  }
  return text.length > 120 ? `${text.slice(0, 117).trimEnd()}...` : text;
}

export function resolveUserTurnTargets(messages: ChatMessage[]): SessionBacktrackTarget[] {
  const targets: SessionBacktrackTarget[] = [];
  messages.forEach((message, messageIndex) => {
    if (message.role !== "user") {
      return;
    }
    const fullText = extractUserMessageText(message);
    targets.push({
      messageId: message.id,
      messageIndex,
      userTurnIndex: targets.length,
      preview: extractUserMessagePreview(message),
      fullText,
    });
  });
  return targets;
}

/** Prefer the latest user turn when entering transcript navigation. */
export function resolveInitialBacktrackNavigationId(
  targets: SessionBacktrackTarget[],
  preferredMessageId?: string | null,
): string | null {
  if (targets.length === 0) {
    return null;
  }
  if (preferredMessageId) {
    const preferred = targets.find((target) => target.messageId === preferredMessageId);
    if (preferred) {
      return preferred.messageId;
    }
  }
  return targets[targets.length - 1]?.messageId ?? null;
}

/** Move selection within user-turn targets; delta of -1 is older, +1 is newer. */
export function moveBacktrackNavigationSelection(
  targets: SessionBacktrackTarget[],
  selectedMessageId: string | null,
  delta: number,
): string | null {
  if (targets.length === 0) {
    return null;
  }
  const currentIndex = targets.findIndex((target) => target.messageId === selectedMessageId);
  const fallbackIndex = targets.length - 1;
  const baseIndex = currentIndex >= 0 ? currentIndex : fallbackIndex;
  const nextIndex = Math.max(0, Math.min(targets.length - 1, baseIndex + delta));
  return targets[nextIndex]?.messageId ?? null;
}

export function isEditableKeyboardTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  if (target.isContentEditable) {
    return true;
  }
  const tag = target.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}

/** Prefer durable runtime message_id; skip synthetic UI ids like `${session}-history-N`. */
export function resolveBacktrackMessageSelector(
  messageId: string,
  sessionId?: string,
): string | undefined {
  const id = messageId.trim();
  if (!id) {
    return undefined;
  }
  if (id.startsWith("msg_")) {
    return id;
  }
  if (sessionId && id.startsWith(`${sessionId}-history-`)) {
    return undefined;
  }
  if (/-history-\d+$/.test(id)) {
    return undefined;
  }
  // Accept legacy durable ids already present in metadata.
  return id;
}
