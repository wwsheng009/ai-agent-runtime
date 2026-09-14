// P2-1A：会话检索弹层的纯函数（标签输入解析 / 状态文案 / 行标题与时间），
// 与组件分离便于单测（对齐 jobs-panel-shared 的既有拆分方式）。

import { formatRelativeTimestamp } from "@/lib/utils";
import type { RuntimeSessionRecord } from "@/types/runtime";

/** 状态文案 key 的完备联合：保证 `t(...)` 走类型化 key（动态字符串会被编译期拒绝）。 */
export type SessionSearchStateLabelKey =
  | "panels.sessionSearch.state.active"
  | "panels.sessionSearch.state.idle"
  | "panels.sessionSearch.state.closed"
  | "panels.sessionSearch.state.archived"
  | "panels.sessionSearch.state.unknown";

const stateLabelKeys: Record<string, SessionSearchStateLabelKey> = {
  active: "panels.sessionSearch.state.active",
  idle: "panels.sessionSearch.state.idle",
  closed: "panels.sessionSearch.state.closed",
  archived: "panels.sessionSearch.state.archived",
};

/** 输入框的逗号/顿号分隔标签文本 → 去空白、去空串的标签数组（保持顺序）。 */
export function parseSessionSearchTagInput(input: string): string[] {
  return input
    .split(/[,，、]/)
    .map((tag) => tag.trim())
    .filter((tag) => tag.length > 0);
}

/** 后端状态枚举 → i18n key；未知状态如实落到 `state.unknown`。 */
export function sessionSearchStateLabelKey(
  state?: string,
): SessionSearchStateLabelKey {
  const normalized = state?.trim().toLowerCase() ?? "";
  return stateLabelKeys[normalized] ?? "panels.sessionSearch.state.unknown";
}

/** 行标题：优先服务端标题，其次会话 id（不伪造占位标题）。 */
export function sessionSearchRowTitle(session: RuntimeSessionRecord): string {
  const title = session.metadata?.title?.trim();
  return title && title.length > 0 ? title : session.id;
}

/**
 * 行时间：无有效时间戳时返回 null，由调用方渲染 `result.timeUnknown`，
 * 避免把 Invalid Date 当成真实时间展示。
 */
export function sessionSearchRowTime(
  session: RuntimeSessionRecord,
): string | null {
  const timestamp = session.updatedAt || session.createdAt || "";
  if (!timestamp || Number.isNaN(Date.parse(timestamp))) {
    return null;
  }
  return formatRelativeTimestamp(timestamp);
}

export function sessionSearchRowTags(session: RuntimeSessionRecord): string[] {
  return session.metadata?.tags ?? [];
}
