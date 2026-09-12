// 由 hooks/workspace/use-session-backtrack.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type RuntimeSessionBacktrackResult,
  type RuntimeSessionBacktrackTombstone,
} from "@/lib/runtime-api";

import { resolveBacktrackMessageSelector } from "./helpers";
import type { SessionBacktrackTarget } from "./types";

export function formatBacktrackApplyNotice(
  result: RuntimeSessionBacktrackResult,
): string {
  const tombstone = result.tombstone;
  const removedMessages =
    tombstone?.removed_message_count ?? result.removed_message_count ?? 0;
  const removedTurns =
    tombstone?.removed_user_turns ?? result.removed_user_turns ?? 0;
  const mode = (tombstone?.mode || result.mode || "conversation").trim();
  const turnIndex =
    typeof tombstone?.user_turn_index === "number"
      ? tombstone.user_turn_index
      : result.user_turn_index;
  const parts = [
    `Backtracked to turn ${turnIndex}`,
    `removed ${removedMessages} message${removedMessages === 1 ? "" : "s"}`,
    `${removedTurns} later user turn${removedTurns === 1 ? "" : "s"}`,
    `mode=${mode}`,
  ];
  if (tombstone?.id) {
    parts.push(`audit ${tombstone.id.slice(0, 12)}`);
  }
  if (result.code_restore?.checkpoint_id) {
    parts.push(`files via ${result.code_restore.checkpoint_id.slice(0, 12)}`);
  } else if (result.base_checkpoint_id) {
    parts.push(`base ${result.base_checkpoint_id.slice(0, 12)}`);
  }
  return `${parts[0]} (${parts.slice(1).join(", ")}).`;
}

export function formatBacktrackAuditEntry(
  entry: RuntimeSessionBacktrackTombstone,
): {
  title: string;
  detail: string;
  preview: string;
} {
  const mode = (entry.mode || "conversation").trim() || "conversation";
  const title = `Turn ${entry.user_turn_index} · ${mode}`;
  const detail = `Removed ${entry.removed_message_count} message${
    entry.removed_message_count === 1 ? "" : "s"
  } / ${entry.removed_user_turns} later user turn${
    entry.removed_user_turns === 1 ? "" : "s"
  } · kept ${entry.truncated_to_message_count}`;
  const preview = (entry.anchor_preview || entry.reason || "").trim() || "(empty)";
  return { title, detail, preview };
}

export function buildBacktrackRequestFields(
  target: SessionBacktrackTarget,
  sessionId?: string,
): {
  message_id?: string;
  user_turn_index?: number;
  message_index?: number;
} {
  const messageId = resolveBacktrackMessageSelector(target.messageId, sessionId);
  // Prefer message_id alone so index drift after history refresh cannot fail a stable anchor.
  if (messageId) {
    return { message_id: messageId };
  }
  return {
    user_turn_index: target.userTurnIndex,
    message_index: target.messageIndex,
  };
}
