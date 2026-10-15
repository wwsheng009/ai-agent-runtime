// S2b（docs/bots/13-event-subscription-routing-design.md §3.1/§3.4/§6.1）：事件订阅
// 编辑器的纯逻辑——词表 × 投递形态映射、面板分组、形态推导、相容性扫描与一键拆分。
//
// 与微前端 backend/cmd/aicli/commands/web/js/connectors.js 同一份口径
// （EVENT_TYPE_FORMS / EVENT_TYPE_SECTION_ORDER / DELIVER_ALL）。本文件是 React 侧
// 的唯一数据源：面板分组、形态推导、告警、拆分共用同一映射——结构不会和校验口径漂移。
//
// 独立文件原因：react-refresh 要求组件文件（subscription-editor.tsx）只导出组件。

import type { ConnectorEventSubscriptionView } from "@/types/runtime";

export type SubscriptionDeliver = NonNullable<ConnectorEventSubscriptionView["deliver"]>;

/** 未选择可判定类型时的自由选择集合。 */
export const DELIVER_ALL: readonly SubscriptionDeliver[] = [
  "notice",
  "content",
  "digest",
  "stream",
];

/** 面板展示顺序（规范名；别名仅参与推导/自定义输入，不单独出选项）。 */
export const EVENT_TYPE_ORDER: readonly string[] = [
  "assistant_message",
  "assistant_delta",
  "approval_requested",
  "question_asked",
  "agent.turn.finished",
  "turn.*",
  "tool.requested",
  "tool.completed",
  "tool.denied",
  "subagent.completed",
  "subagent.batch.completed",
  "work.*",
  "user_message",
  "agent.turn.started",
  "turn.suspended",
  "turn.resumed",
  "session_start",
  "session_end",
  "session.interrupted",
  "error",
];

/** 族通配在面板上的小字哨兵值（渲染时走 i18n）。 */
export const WILDCARD_FAMILY = "通配";

/** 条目辅助信息（族名；分组已按形态表达，族作为右侧小字）。 */
export const EVENT_TYPE_FAMILY: Record<string, string> = {
  assistant_message: "conversation",
  user_message: "conversation",
  assistant_delta: "conversation",
  "agent.turn.started": "turn",
  "agent.turn.finished": "turn",
  "turn.suspended": "turn",
  "turn.resumed": "turn",
  session_start: "turn",
  session_end: "turn",
  "session.interrupted": "turn",
  approval_requested: "interaction",
  question_asked: "interaction",
  error: "system",
  "tool.requested": "work",
  "tool.completed": "work",
  "tool.denied": "work",
  "subagent.completed": "work",
  "subagent.batch.completed": "work",
  "turn.*": WILDCARD_FAMILY,
  "work.*": WILDCARD_FAMILY,
};

/**
 * 每种类型的可用投递形态（docs/bots/13 §3.1/§4）：用于「类型 × 投递形态」相容性
 * 告警——形态不匹配的类型会被后端静默忽略（2026-10-15 实测：assistant_message 配
 * notice 不投递），必须在保存前显式提示。
 *
 * 空数组 = 词表已登记但出站映射未接（勾了也不会投递）；通配/词表外值不做静态判定。
 */
export const EVENT_TYPE_FORMS: Record<string, readonly SubscriptionDeliver[]> = {
  assistant_message: ["content"],
  user_message: [],
  assistant_delta: ["stream"],
  "agent.turn.started": [],
  "agent.turn.finished": ["notice", "digest"],
  "turn.suspended": [],
  "turn.resumed": [],
  session_start: [],
  session_end: [],
  "session.interrupted": [],
  approval_requested: ["notice"],
  question_asked: ["notice"],
  error: [],
  "tool.requested": ["digest"],
  "tool.completed": ["digest"],
  "tool.denied": ["digest"],
  "subagent.completed": ["digest"],
  "subagent.batch.completed": ["digest"],
  // 别名（与词表同源）：让存量配置/自定义输入也能参与推导。
  completion: ["notice", "digest"],
  approval: ["notice"],
  question: ["notice"],
  "assistant.message": ["content"],
  "assistant.delta": ["stream"],
  "tool.started": ["digest"],
  "tool.finished": ["digest"],
  tool_finished: ["digest"],
  // 族通配：按族内可用形态的并集收敛。
  "turn.*": ["notice", "digest"],
  "work.*": ["digest"],
};

/** 形态分组顺序（键 = 允许形态的 join("/")；空串 = 未接线）。 */
export const EVENT_TYPE_SECTION_ORDER: readonly string[] = [
  "content",
  "stream",
  "notice",
  "notice/digest",
  "digest",
  "",
];

export interface EventTypeSection {
  form: string;
  types: string[];
}

/** 由 EVENT_TYPE_FORMS 生成「按形态分组」的面板结构（types↔deliver 关联由结构表达）。 */
export function eventTypeSections(): EventTypeSection[] {
  const byForm = new Map<string, string[]>();
  for (const name of EVENT_TYPE_ORDER) {
    const forms = EVENT_TYPE_FORMS[name];
    if (!forms) {
      continue;
    }
    const key = forms.length ? forms.join("/") : "";
    const list = byForm.get(key);
    if (list) {
      list.push(name);
    } else {
      byForm.set(key, [name]);
    }
  }
  const sections: EventTypeSection[] = [];
  for (const form of EVENT_TYPE_SECTION_ORDER) {
    const types = byForm.get(form);
    if (types && types.length) {
      sections.push({ form, types });
    }
  }
  return sections;
}

/** 词表外（不在面板中的）既有值：追加为「自定义(既有)」勾选项，往返不丢。 */
export function customPanelTypes(types: readonly string[] | undefined): string[] {
  const panel = new Set(EVENT_TYPE_ORDER);
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of types ?? []) {
    const name = String(raw).trim();
    if (!name || seen.has(name) || panel.has(name)) {
      continue;
    }
    seen.add(name);
    out.push(name);
  }
  return out;
}

export interface SubscriptionScan {
  /** 允许的投递形态并集（空 = 无可判定类型）。 */
  allowed: SubscriptionDeliver[];
  /** 与当前 deliver 不兼容的类型（需切形态或拆分）。 */
  conflicts: { type: string; forms: readonly SubscriptionDeliver[] }[];
  /** 未接出站映射的类型（勾选也不会投递）。 */
  unwired: string[];
}

/** 扫描所选类型（勾选 + 自定义输入），推导允许形态 / 冲突 / 未接线。 */
export function scanSubscriptionTypes(
  types: readonly string[] | undefined,
  deliver: SubscriptionDeliver,
): SubscriptionScan {
  const allowedSet = new Set<SubscriptionDeliver>();
  const allowed: SubscriptionDeliver[] = [];
  const conflicts: SubscriptionScan["conflicts"] = [];
  const unwired: string[] = [];
  const seen = new Set<string>();
  for (const raw of types ?? []) {
    const type = String(raw).trim();
    if (!type || seen.has(type)) {
      continue;
    }
    seen.add(type);
    const forms = EVENT_TYPE_FORMS[type];
    if (!forms) {
      continue; // 词表外：不做静态判定。
    }
    if (forms.length === 0) {
      unwired.push(type);
      continue;
    }
    for (const form of forms) {
      if (!allowedSet.has(form)) {
        allowedSet.add(form);
        allowed.push(form);
      }
    }
    if (!forms.includes(deliver)) {
      conflicts.push({ type, forms });
    }
  }
  return { allowed, conflicts, unwired };
}

export interface ResolvedDeliver {
  deliver: SubscriptionDeliver;
  options: SubscriptionDeliver[];
  /** 唯一形态：锁定（用户无需感知）。 */
  locked: boolean;
  /** 无可判定类型：保留全量可选。 */
  free: boolean;
}

/** 「细项 → 粗项」：唯一形态锁定、多形态收敛、无可判定类型时自由选择。 */
export function resolveSubscriptionDeliver(
  types: readonly string[] | undefined,
  current: SubscriptionDeliver | undefined,
): ResolvedDeliver {
  const base: SubscriptionDeliver = current ?? "notice";
  const allowed = scanSubscriptionTypes(types, base).allowed;
  if (allowed.length === 0) {
    return { deliver: base, options: [...DELIVER_ALL], locked: false, free: true };
  }
  const deliver = allowed.includes(base) ? base : allowed[0];
  return { deliver, options: allowed, locked: allowed.length === 1, free: false };
}

/** 形态推导的写回：仅在当前形态不被所选类型允许时产出新对象（引用相等 = 无变化）。 */
export function withResolvedDeliver(
  sub: ConnectorEventSubscriptionView,
): ConnectorEventSubscriptionView {
  const resolved = resolveSubscriptionDeliver(sub.types, sub.deliver);
  if (resolved.deliver === (sub.deliver ?? "notice")) {
    return sub;
  }
  return { ...sub, deliver: resolved.deliver };
}

/** 勾选/取消一个事件类型（保持既有顺序，新值追加在末尾）。 */
export function toggleType(
  types: readonly string[] | undefined,
  name: string,
  checked: boolean,
): string[] {
  const current = (types ?? []).filter((item) => item !== name);
  return checked ? [...current, name] : current;
}

/**
 * 自定义/别名输入的差分合并：把「输入框里被删掉的 token」从 types 移除、把新增
 * token 追加到末尾；既有词表外值（未出现在输入框中）不受影响——往返不丢。
 */
export function mergeCustomTokens(
  current: readonly string[] | undefined,
  previousTokens: readonly string[],
  nextTokens: readonly string[],
): string[] {
  const nextSet = new Set(nextTokens);
  const removed = new Set(previousTokens.filter((token) => !nextSet.has(token)));
  const out = (current ?? []).filter((token) => !removed.has(token));
  const seen = new Set(out);
  for (const token of nextTokens) {
    if (!seen.has(token)) {
      seen.add(token);
      out.push(token);
    }
  }
  return out;
}

export interface TypesSummary {
  total: number;
  shown: string[];
  more: boolean;
}

/** 汇总行数据（折叠面板收起时的唯一可见信息）。 */
export function typesSummary(types: readonly string[] | undefined): TypesSummary {
  const seen = new Set<string>();
  const list: string[] = [];
  for (const raw of types ?? []) {
    const name = String(raw).trim();
    if (!name || seen.has(name)) {
      continue;
    }
    seen.add(name);
    list.push(name);
  }
  return { total: list.length, shown: list.slice(0, 3), more: list.length > 3 };
}

/**
 * 一键拆分：按解析形态分组（多形态类型按当前 deliver，不适用取 forms[0]），
 * 词表外/未接线类型留在首条；续行 id 追加 -2/-3。一条订阅一种形态——这是
 * 「细项推导粗项」的收尾方式，用户无需手工配置形态。
 */
export function splitSubscriptionModel(
  sub: ConnectorEventSubscriptionView,
): ConnectorEventSubscriptionView[] {
  const current = sub.deliver ?? "notice";
  const keep: string[] = [];
  const groups = new Map<SubscriptionDeliver, string[]>();
  for (const raw of sub.types ?? []) {
    const type = String(raw).trim();
    if (!type) {
      continue;
    }
    const forms = EVENT_TYPE_FORMS[type];
    if (!forms || forms.length === 0) {
      keep.push(type);
      continue;
    }
    const form = forms.includes(current) ? current : forms[0];
    const list = groups.get(form);
    if (list) {
      list.push(type);
    } else {
      groups.set(form, [type]);
    }
  }
  const order = [...groups.keys()];
  if (order.length <= 1) {
    return [sub];
  }
  return order.map((form, index) => {
    const types = groups.get(form) ?? [];
    return {
      ...sub,
      deliver: form,
      types: index === 0 ? [...keep, ...types] : [...types],
      ...(index > 0 && sub.id ? { id: `${sub.id}-${index + 1}` } : {}),
    };
  });
}