// 由 components/workspace/runtime-teams/shared.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 展示格式化（标识截断 / 状态色 / 事件与信箱描述 / 冲突摘要）

import {
  type RuntimePathClaimConflict,
  type RuntimeTeamEventRecord,
  type RuntimeTeamMailboxMessage,
} from "@/lib/runtime-api";

export function truncateIdentifier(value: string | undefined, length = 14) {
  if (!value) {
    return "";
  }
  if (value.length <= length) {
    return value;
  }
  return `${value.slice(0, length)}...`;
}

export function statusTone(value: string | undefined) {
  switch ((value || "").toLowerCase()) {
    case "running":
    case "busy":
      return "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]";
    case "done":
    case "idle":
      return "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]";
    case "failed":
    case "blocked":
    case "offline":
      return "border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#f59e7d]";
    default:
      return "border-white/10 bg-white/6 text-[var(--muted-foreground)]";
  }
}

export function prettyEventType(value: string) {
  return value.replaceAll(".", " / ");
}

export function describeMailboxRoute(message: RuntimeTeamMailboxMessage) {
  const from = message.from_agent?.trim() || "system";
  const rawTo = message.to_agent?.trim();
  const to = !rawTo || rawTo === "*" ? "broadcast" : rawTo;
  const parts = [`from ${from}`, `to ${to}`];
  if (message.task_id) {
    parts.push(`task ${truncateIdentifier(message.task_id, 12)}`);
  }
  const metadataCount = Object.keys(message.metadata ?? {}).length;
  if (metadataCount > 0) {
    parts.push(`${metadataCount} meta`);
  }
  return parts.join(" · ");
}

export function summarizeConflict(conflict: RuntimePathClaimConflict) {
  return [
    `request ${conflict.path}`,
    `existing ${conflict.existing_path}`,
    `owner ${truncateIdentifier(conflict.existing_owner, 14)}`,
    `task ${truncateIdentifier(conflict.existing_task_id, 14)}`,
    `mode ${conflict.existing_mode}`,
  ].join(" · ");
}

export function describeEventPayload(event: RuntimeTeamEventRecord) {
  const payload = event.payload ?? {};
  const stringKeys = [
    "summary",
    "title",
    "task_id",
    "teammate_id",
    "assignee",
    "from_agent",
    "to_agent",
    "message_id",
    "workspace_id",
  ] as const;

  for (const key of stringKeys) {
    const raw = payload[key];
    if (typeof raw === "string" && raw.trim()) {
      return `${key}: ${raw.trim()}`;
    }
  }

  if (typeof payload.count === "number") {
    return `count: ${payload.count}`;
  }

  const keys = Object.keys(payload);
  if (keys.length === 0) {
    return "no payload";
  }

  return keys.slice(0, 3).join(", ");
}
