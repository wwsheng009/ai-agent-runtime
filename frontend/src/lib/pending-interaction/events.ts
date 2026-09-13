/**
 * P1-7：运行时事件 → 待交互状态归约（纯函数，无 React / 无 DOM）。
 *
 * 事件口径取自后端（§6.3 P2-1A）：
 * - `approval_requested`：`{ request_id, tool_name, reason, risk_level, tool_call_id?, expires_at? }`
 * - `approval_resolved`：`{ request_id?, allow?, resolution? }`（`resolution="expired"` 为 30min 超时终态）
 * - `question_asked`：`{ question_id?, id?, prompt, required, suggestions?, turn_id? }`
 * - `question_answered`：`{ question_id?, id?, answer? }`
 * - 会话终止（`session_end` / `session.interrupted`）→ 未决条目优雅收敛为 cancelled。
 */

import type { SessionRuntimeEvent } from "@/types/runtime";

import {
  type PendingApprovalInteraction,
  type PendingInteraction,
  type PendingInteractionConvergeReason,
  type PendingInteractionState,
  type PendingInteractionStatus,
  type PendingQuestionInteraction,
  emptyPendingInteractionState,
} from "./types";

const APPROVAL_REQUESTED_TYPES = new Set([
  "approval_requested",
  "approval.requested",
]);
const APPROVAL_RESOLVED_TYPES = new Set([
  "approval_resolved",
  "approval.resolved",
]);
const QUESTION_ASKED_TYPES = new Set(["question_asked", "question.asked"]);
const QUESTION_ANSWERED_TYPES = new Set([
  "question_answered",
  "question.answered",
]);

/** 会话终止事件：未决交互不再悬挂（delegate-on-remove 的收口点）。 */
const SESSION_CONVERGE_TYPES: Record<string, PendingInteractionConvergeReason> = {
  session_end: "session_end",
  "session.end": "session_end",
  session_interrupted: "session_interrupted",
  "session.interrupted": "session_interrupted",
  session_closed: "session_removed",
  "session.closed": "session_removed",
  session_deleted: "session_removed",
  "session.deleted": "session_removed",
};

export function normalizePendingInteractionEventType(type: string): string {
  return type.trim().toLowerCase();
}

function readString(
  payload: Record<string, unknown> | undefined,
  ...keys: string[]
): string {
  if (!payload) {
    return "";
  }
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
    if (typeof value === "number" && Number.isFinite(value)) {
      return String(value);
    }
  }
  return "";
}

function readBoolean(
  payload: Record<string, unknown> | undefined,
  key: string,
): boolean | undefined {
  const value = payload?.[key];
  return typeof value === "boolean" ? value : undefined;
}

function readStringArray(
  payload: Record<string, unknown> | undefined,
  key: string,
): string[] {
  const value = payload?.[key];
  if (Array.isArray(value)) {
    return value
      .map((item) => (typeof item === "string" ? item.trim() : ""))
      .filter((item) => item.length > 0);
  }
  if (typeof value === "string" && value.trim()) {
    return [value.trim()];
  }
  return [];
}

/** `approval_requested` → 审批条目（缺 `request_id` 不建入口，绝不伪造身份）。 */
export function pendingApprovalFromRuntimeEvent(
  event: SessionRuntimeEvent,
): PendingApprovalInteraction | null {
  const payload = event.payload;
  const id = readString(payload, "request_id", "requestId");
  if (!id) {
    return null;
  }
  const toolCallId = readString(payload, "tool_call_id", "toolCallId");
  const expiresAt = readString(payload, "expires_at", "expiresAt");
  const turnId = readString(payload, "turn_id", "turnId");
  return {
    kind: "approval",
    id,
    status: "pending",
    sessionId: event.session_id ?? undefined,
    createdAt: event.timestamp,
    ...(turnId ? { turnId } : {}),
    toolName:
      readString(payload, "tool_name", "toolName") ||
      event.tool_name?.trim() ||
      "",
    reason: readString(payload, "reason"),
    riskLevel: readString(payload, "risk_level", "riskLevel"),
    ...(toolCallId ? { toolCallId } : {}),
    ...(expiresAt ? { expiresAt } : {}),
  };
}

/** `question_asked` → 提问条目（缺 id 不建入口）。 */
export function pendingQuestionFromRuntimeEvent(
  event: SessionRuntimeEvent,
): PendingQuestionInteraction | null {
  const payload = event.payload;
  const id = readString(payload, "question_id", "questionId", "id");
  if (!id) {
    return null;
  }
  const turnId = readString(payload, "turn_id", "turnId");
  return {
    kind: "question",
    id,
    status: "pending",
    sessionId: event.session_id ?? undefined,
    createdAt: event.timestamp,
    ...(turnId ? { turnId } : {}),
    prompt: readString(payload, "prompt", "question"),
    required: readBoolean(payload, "required") ?? false,
    suggestions: readStringArray(payload, "suggestions"),
  };
}

/** 注册（幂等：同 id 已存在则保留注册序、刷新字段与状态）。 */
export function registerPendingInteraction(
  state: PendingInteractionState,
  interaction: PendingInteraction,
): PendingInteractionState {
  const index = state.items.findIndex((item) => item.id === interaction.id);
  if (index < 0) {
    return { items: [...state.items, interaction] };
  }
  const items = [...state.items];
  items[index] = { ...interaction, createdAt: items[index].createdAt };
  return { items };
}

/** 投递决定后进入 resolving（不可重复提交，直到回填或失败）。 */
export function markPendingInteractionResolving(
  state: PendingInteractionState,
  id: string,
): PendingInteractionState {
  return updateItem(state, id, (item) =>
    item.status === "pending"
      ? { ...item, status: "resolving", error: undefined }
      : item,
  );
}

/** 结果回填（成功 / 失败都走这里；失败保留可重试入口）。 */
export function settlePendingInteraction(
  state: PendingInteractionState,
  id: string,
  options: {
    status: Exclude<PendingInteractionStatus, "pending" | "resolving">;
    resolution?: string;
    error?: string;
  },
): PendingInteractionState {
  return updateItem(state, id, (item) => ({
    ...item,
    status: options.status,
    ...(options.resolution ? { resolution: options.resolution } : {}),
    ...(options.error ? { error: options.error } : {}),
  }));
}

/** 失败回退：从 resolving 回到 pending，并携带失败原因。 */
export function failPendingInteractionResolve(
  state: PendingInteractionState,
  id: string,
  error: string,
): PendingInteractionState {
  return updateItem(state, id, (item) =>
    item.status === "resolving"
      ? { ...item, status: "pending", error }
      : item,
  );
}

/**
 * 会话收敛（delegate-on-remove）：把未决条目统一置为 cancelled / expired，
 * 不留下悬挂卡片。已 resolved / cancelled / expired 的条目保持原状。
 */
export function convergePendingInteractions(
  state: PendingInteractionState,
  options: {
    sessionId?: string;
    reason: PendingInteractionConvergeReason;
  },
): PendingInteractionState {
  const sessionId = options.sessionId?.trim();
  return {
    items: state.items.map((item) => {
      if (item.status !== "pending" && item.status !== "resolving") {
        return item;
      }
      if (sessionId && item.sessionId && item.sessionId !== sessionId) {
        return item;
      }
      return {
        ...item,
        status:
          options.reason === "expired"
            ? ("expired" as const)
            : ("cancelled" as const),
        resolution: options.reason,
      };
    }),
  };
}

/** 本地超时守卫：`expires_at` 已过的审批不再可操作（后端仍会落 `expired` 终态）。 */
export function expirePendingInteractions(
  state: PendingInteractionState,
  now: number,
): PendingInteractionState {
  let changed = false;
  const items = state.items.map((item) => {
    if (
      item.kind !== "approval" ||
      item.status !== "pending" ||
      !item.expiresAt
    ) {
      return item;
    }
    const deadline = Date.parse(item.expiresAt);
    if (!Number.isFinite(deadline) || deadline > now) {
      return item;
    }
    changed = true;
    return { ...item, status: "expired" as const, resolution: "expired" };
  });
  return changed ? { items } : state;
}

/**
 * 单事件归约入口：注册 / 回填 / 会话收敛三态。
 *
 * 无身份的结果事件（缺 request_id / question_id）按会话保守清除——同一会话
 * 同一时刻至多一个同类待交互（actor 侧 pending 单槽语义）。
 */
export function applyPendingInteractionEvent(
  state: PendingInteractionState,
  event: SessionRuntimeEvent,
): PendingInteractionState {
  const type = normalizePendingInteractionEventType(event.type);
  const convergeReason = SESSION_CONVERGE_TYPES[type];
  if (convergeReason) {
    return convergePendingInteractions(state, {
      sessionId: event.session_id ?? undefined,
      reason: convergeReason,
    });
  }

  if (APPROVAL_REQUESTED_TYPES.has(type)) {
    const approval = pendingApprovalFromRuntimeEvent(event);
    return approval ? registerPendingInteraction(state, approval) : state;
  }

  if (APPROVAL_RESOLVED_TYPES.has(type)) {
    const id = readString(event.payload, "request_id", "requestId");
    const toolCallId = readString(event.payload, "tool_call_id", "toolCallId");
    const resolution = readString(event.payload, "resolution");
    const allow = readBoolean(event.payload, "allow");
    return resolveByKey(state, {
      kind: "approval",
      id: id || undefined,
      fallbackKey: toolCallId || undefined,
      sessionId: event.session_id ?? undefined,
      status: resolution === "expired" ? "expired" : "resolved",
      resolution: resolution || (allow === false ? "deny" : "allow"),
    });
  }

  if (QUESTION_ASKED_TYPES.has(type)) {
    const question = pendingQuestionFromRuntimeEvent(event);
    return question ? registerPendingInteraction(state, question) : state;
  }

  if (QUESTION_ANSWERED_TYPES.has(type)) {
    const id = readString(event.payload, "question_id", "questionId", "id");
    const answer = readString(event.payload, "answer");
    return resolveByKey(state, {
      kind: "question",
      id: id || undefined,
      sessionId: event.session_id ?? undefined,
      status: "resolved",
      resolution: answer ? `answered:${answer}` : "answered",
    });
  }

  return state;
}

/** 把一串运行时事件按到达序归约为待交互快照（历史回放 / reload 共用）。 */
export function pendingInteractionsFromRuntimeEvents(
  events: readonly SessionRuntimeEvent[],
): PendingInteractionState {
  return events.reduce<PendingInteractionState>(
    (state, event) => applyPendingInteractionEvent(state, event),
    emptyPendingInteractionState(),
  );
}

function resolveByKey(
  state: PendingInteractionState,
  options: {
    kind: PendingInteraction["kind"];
    id?: string;
    fallbackKey?: string;
    sessionId?: string;
    status: Exclude<PendingInteractionStatus, "pending" | "resolving">;
    resolution?: string;
  },
): PendingInteractionState {
  const { kind, id, fallbackKey, sessionId, status, resolution } = options;
  const matches = (item: PendingInteraction) => {
    if (item.kind !== kind) {
      return false;
    }
    if (id) {
      return item.id === id;
    }
    if (fallbackKey) {
      return (
        item.kind === "approval" &&
        "toolCallId" in item &&
        item.toolCallId === fallbackKey
      );
    }
    if (sessionId) {
      return !item.sessionId || item.sessionId === sessionId;
    }
    return true;
  };

  const index = state.items.findIndex(
    (item) =>
      (item.status === "pending" || item.status === "resolving") && matches(item),
  );
  if (index < 0) {
    return state;
  }
  const items = [...state.items];
  items[index] = {
    ...items[index],
    status,
    ...(resolution ? { resolution } : {}),
  };
  return { items };
}

function updateItem(
  state: PendingInteractionState,
  id: string,
  updater: (item: PendingInteraction) => PendingInteraction,
): PendingInteractionState {
  const index = state.items.findIndex((item) => item.id === id);
  if (index < 0) {
    return state;
  }
  const items = [...state.items];
  items[index] = updater(items[index]);
  return items[index] === state.items[index] ? state : { items };
}
