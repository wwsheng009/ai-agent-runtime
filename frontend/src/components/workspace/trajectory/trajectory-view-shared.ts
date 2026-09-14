/**
 * 轨迹视图共享纯函数：筛选 / 搜索 / 摘要 / 标签。
 *
 * - 明细单行摘要与详情面板共用同一 Item 对象（P2-1 验收②：无两套数据）；
 * - 搜索为线性过滤（Q3 增量索引推迟至 Phase 3，P2-7）。
 */
import type { TrajectoryItem } from "@/lib/trajectory/types";

export type TrajectoryViewFilter = "all" | "tools" | "messages" | "structured";

/** 筛选标签的 i18n key（由调用方用 workspace ns 的 t 翻译）。 */
export const TRAJECTORY_VIEW_FILTERS = [
  { id: "all", labelKey: "panels.shell.trajectory.filters.all" },
  { id: "tools", labelKey: "panels.shell.trajectory.filters.tools" },
  { id: "messages", labelKey: "panels.shell.trajectory.filters.messages" },
  { id: "structured", labelKey: "panels.shell.trajectory.filters.structured" },
] as const satisfies ReadonlyArray<{ id: TrajectoryViewFilter; labelKey: string }>;

/** 轨迹 item kind 的 i18n key（由调用方用 workspace ns 的 t 翻译）。 */
export const TRAJECTORY_ITEM_KIND_KEYS = {
  user: "panels.shell.trajectory.kinds.user",
  assistant: "panels.shell.trajectory.kinds.message",
  reasoning: "panels.shell.trajectory.kinds.reasoning",
  tool: "panels.shell.trajectory.kinds.tool",
  planning: "panels.shell.trajectory.kinds.planning",
  orchestration: "panels.shell.trajectory.kinds.orchestration",
  route: "panels.shell.trajectory.kinds.route",
  observation: "panels.shell.trajectory.kinds.observation",
  subagent: "panels.shell.trajectory.kinds.subagent",
  result: "panels.shell.trajectory.kinds.result",
  system: "panels.shell.trajectory.kinds.system",
} as const satisfies Record<TrajectoryItem["kind"], string>;

export function trajectoryItemKindKey(
  kind: TrajectoryItem["kind"],
): (typeof TRAJECTORY_ITEM_KIND_KEYS)[TrajectoryItem["kind"]] {
  return TRAJECTORY_ITEM_KIND_KEYS[kind];
}

/** 轨迹 item 状态文案的 i18n key（由调用方用 workspace ns 的 t 翻译）。 */
export const TRAJECTORY_ITEM_STATUS_KEYS = {
  pending: "panels.shell.trajectory.statuses.pending",
  running: "panels.shell.trajectory.statuses.running",
  completed: "panels.shell.trajectory.statuses.completed",
  failed: "panels.shell.trajectory.statuses.failed",
  canceled: "panels.shell.trajectory.statuses.canceled",
} as const satisfies Record<TrajectoryItem["status"], string>;

export function trajectoryItemStatusKey(
  status: TrajectoryItem["status"],
): (typeof TRAJECTORY_ITEM_STATUS_KEYS)[TrajectoryItem["status"]] {
  return TRAJECTORY_ITEM_STATUS_KEYS[status];
}

/** 渲染语义文本（搜索与详情共用）：text/reasoning → 内容；tool → name + 摘要；structured → JSON。 */
export function trajectoryItemText(item: TrajectoryItem): string {
  switch (item.head.kind) {
    case "text":
    case "reasoning":
      return item.head.content;
    case "tool": {
      const parts = [item.head.name];
      if (item.head.argsSummary) parts.push(item.head.argsSummary);
      if (item.head.resultSummary) parts.push(item.head.resultSummary);
      if (item.head.errorMessage) parts.push(item.head.errorMessage);
      return parts.join("\n");
    }
    case "structured":
      return JSON.stringify(item.head.payload ?? {});
    case "system":
      return item.head.note;
    default:
      return "";
  }
}

/** 单行摘要（长度裁剪，虚拟列表行内展示）。 */
export function trajectoryItemSummary(item: TrajectoryItem, maxLength = 120): string {
  const text = trajectoryItemText(item).replace(/\s+/g, " ").trim();
  if (text.length <= maxLength) {
    return text;
  }
  return `${text.slice(0, maxLength).trimEnd()}…`;
}

export function trajectoryItemMatches(item: TrajectoryItem, query: string): boolean {
  const needle = query.trim().toLowerCase();
  if (!needle) {
    return true;
  }
  return trajectoryItemText(item).toLowerCase().includes(needle);
}

export function trajectoryItemPassesFilter(
  item: TrajectoryItem,
  filter: TrajectoryViewFilter,
): boolean {
  switch (filter) {
    case "all":
      return true;
    case "tools":
      return item.kind === "tool";
    case "messages":
      return (
        item.kind === "user" ||
        item.kind === "assistant" ||
        item.kind === "reasoning"
      );
    case "structured":
      return (
        item.kind === "planning" ||
        item.kind === "orchestration" ||
        item.kind === "route" ||
        item.kind === "observation" ||
        item.kind === "subagent" ||
        item.kind === "result" ||
        item.kind === "system"
      );
  }
}

export function filterTrajectoryItems(
  items: TrajectoryItem[],
  filter: TrajectoryViewFilter,
  query: string,
): TrajectoryItem[] {
  return items.filter(
    (item) => trajectoryItemPassesFilter(item, filter) && trajectoryItemMatches(item, query),
  );
}
