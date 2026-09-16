// 「会话详情」面的纯派生口径：状态徽标配色与时间戳格式化。
// 与组件分离的原因同 session-usage-panel-shared：可单测、无 IO、无 React。

import { type RuntimeSessionRecord } from "@/types/runtime";

export type SessionDetailStateDisplay = {
  /** 徽标配色；状态文案由组件翻译，本模块不持有自然语言。 */
  className: string;
  /** i18n key（`workspace` 命名空间）：`panels.sessionDetail.states.*`。 */
  labelKey: string;
};

const STATE_DISPLAY: Record<string, SessionDetailStateDisplay> = {
  active: {
    className:
      "border-accent-primary/30 bg-accent-primary/10 text-accent-primary",
    labelKey: "panels.sessionDetail.states.active",
  },
  idle: {
    className: "border-border bg-surface-soft text-muted-foreground",
    labelKey: "panels.sessionDetail.states.idle",
  },
  closed: {
    className: "border-border bg-surface-soft text-muted-foreground",
    labelKey: "panels.sessionDetail.states.closed",
  },
  archived: {
    className: "border-accent-gold/24 bg-accent-gold/10 text-accent-gold",
    labelKey: "panels.sessionDetail.states.archived",
  },
};

/** 会话状态 → 徽标样式 + 文案 key；未知/缺失状态如实回落，不臆造。 */
export function resolveSessionDetailState(
  state: RuntimeSessionRecord["state"],
): SessionDetailStateDisplay {
  const normalized = (state ?? "").trim().toLowerCase();
  return (
    STATE_DISPLAY[normalized] ?? {
      className: "border-border bg-surface-soft text-muted-foreground",
      labelKey: "panels.sessionDetail.states.unknown",
    }
  );
}

/** 时间戳 → 本地可读时间；缺失或不可解析时返回 null（调用方跳过该行）。 */
export function formatSessionDetailTimestamp(
  value: string | null | undefined,
): string | null {
  const raw = value?.trim();
  if (!raw) {
    return null;
  }
  const parsed = Date.parse(raw);
  return Number.isNaN(parsed) ? null : new Date(parsed).toLocaleString();
}

/** 标签去空、去重后原序返回；空数组视为无标签。 */
export function normalizeSessionDetailTags(
  tags: readonly string[] | undefined,
): string[] {
  if (!tags || tags.length === 0) {
    return [];
  }
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const tag of tags) {
    const trimmed = tag.trim();
    if (!trimmed || seen.has(trimmed)) {
      continue;
    }
    seen.add(trimmed);
    normalized.push(trimmed);
  }
  return normalized;
}
