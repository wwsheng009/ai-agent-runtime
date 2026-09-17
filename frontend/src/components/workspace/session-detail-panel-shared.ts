// 「会话详情」面的纯派生口径：状态徽标配色与时间戳格式化。
// 与组件分离的原因同 session-usage-panel-shared：可单测、无 IO、无 React。

import {
  type WorkspaceThreadRelationKind,
  type WorkspaceThreadTransportKind,
} from "@/components/workspace/workspace-shell-shared";
import { type RuntimeSessionRecord } from "@/types/runtime";

// 「会话详情」的容器/文字配方集中在此，供面级布局与网络观测块共用一套：
// 三档容器（面板卡片 → 子卡片 → 徽标）+ 两档小标题，避免同一条侧栏里出现
// 多种内边距/圆角/字号各自为政。

/** 面板级卡片：面的直接子块（关联状态、字段、网络详情、状态卡）。 */
export const SESSION_DETAIL_CARD_CLASS =
  "rounded-card border border-border bg-surface-softer px-2.5 py-2";

/** 卡片内子块（通道 / 闸门 / DOM / 帧列表）：比面板卡片低一档底色。 */
export const SESSION_DETAIL_SUBCARD_CLASS =
  "rounded-card border border-border bg-surface-soft px-2 py-1.5";

/** 小标题：10px 大写 + 字距，章节标题与两列标签共用。 */
export const SESSION_DETAIL_SECTION_LABEL_CLASS =
  "app-text-10 uppercase tracking-[0.12em] text-muted-foreground";

/** 状态徽标基底：底色/描边/文字色由各状态表（状态/关联/传输/判读）追加。 */
export const SESSION_DETAIL_CHIP_CLASS =
  "inline-flex shrink-0 items-center gap-1 rounded-full border px-1.5 py-0.5 app-text-10 tracking-[0.08em]";

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

/** 关联状态徽标的配色 + 文案 key；关联判据本身来自 workspace-shell-shared。 */
export type SessionDetailRelationDisplay = {
  className: string;
  /** i18n key（`workspace` 命名空间）：`panels.sessionDetail.relation.states.*`。 */
  labelKey: string;
  /** i18n key：`panels.sessionDetail.relation.details.*`（tooltip 与正文共用）。 */
  detailKey: string;
};

const RELATION_DISPLAY: Record<
  WorkspaceThreadRelationKind,
  SessionDetailRelationDisplay
> = {
  attached: {
    className: "border-accent-gold/24 bg-accent-gold/10 text-accent-gold",
    labelKey: "panels.sessionDetail.relation.states.attached",
    detailKey: "panels.sessionDetail.relation.details.attached",
  },
  restored: {
    className: "border-accent-teal/24 bg-accent-teal/10 text-accent-teal",
    labelKey: "panels.sessionDetail.relation.states.restored",
    detailKey: "panels.sessionDetail.relation.details.restored",
  },
  error: {
    className:
      "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
    labelKey: "panels.sessionDetail.relation.states.error",
    detailKey: "panels.sessionDetail.relation.details.error",
  },
  pending: {
    className: "border-border bg-surface-soft text-muted-foreground",
    labelKey: "panels.sessionDetail.relation.states.pending",
    detailKey: "panels.sessionDetail.relation.details.pending",
  },
};

/** 关联四态 → 徽标样式 + 文案 key（配色与侧栏行状态同一口径）。 */
export function resolveSessionDetailRelation(
  kind: WorkspaceThreadRelationKind,
): SessionDetailRelationDisplay {
  return RELATION_DISPLAY[kind];
}

/**
 * 传输通道徽标：状态名与解释都复用顶栏同一份 key
 * （`topbar.threadTransport.*` + `topbar.threadTransportHint.*`）——
 * 同一个传输状态在顶栏与「会话详情」里给出一致的名称与解释，避免两处各写一份造成漂移。
 */
export type SessionDetailTransportDisplay = {
  /** 只染文字色：底/描边保持中性，避免与关联状态徽标的强色抢注意力。 */
  toneClassName: string;
  /** i18n key：`topbar.threadTransport.*`。 */
  labelKey: string;
  /** i18n key：`topbar.threadTransportHint.*`。 */
  hintKey: string;
};

const TRANSPORT_DISPLAY: Record<
  WorkspaceThreadTransportKind,
  SessionDetailTransportDisplay
> = {
  live: {
    toneClassName: "text-connection-online",
    labelKey: "topbar.threadTransport.live",
    hintKey: "topbar.threadTransportHint.live",
  },
  error: {
    toneClassName: "text-connection-offline",
    labelKey: "topbar.threadTransport.error",
    hintKey: "topbar.threadTransportHint.error",
  },
  seeded: {
    toneClassName: "text-muted-foreground",
    labelKey: "topbar.threadTransport.seeded",
    hintKey: "topbar.threadTransportHint.seeded",
  },
};

export function resolveSessionDetailTransport(
  transport: WorkspaceThreadTransportKind,
): SessionDetailTransportDisplay {
  return TRANSPORT_DISPLAY[transport];
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
