/**
 * Batch 3（多会话并发运行时 §4.7.3）：后台会话的「完成 / 待交互」页内通知。
 *
 * 定位：桌面通知（`maybeShowDesktopNotification`）只在页面不可见时生效，且仍要求
 * 浏览器已授权；用户停在页面上看着 B 会话时，A 的完成 / 待审批必须有页内落点，
 * 否则侧栏图标是唯一线索。本模块提供最小可测的通知 store：
 *
 * - `diffSessionRuntimeNotices` 是纯函数：吃「上一次条目快照 + 本次投影」，
 *   产出应当提示的通知（选中会话不提示——前台 UI 已经呈现）；
 * - store 用 `useSyncExternalStore` 可直接订阅，快照引用只在内容变化时更新；
 * - 只做页内呈现所需的字段，不掺业务写路径（D1-B：后台不写 `threads`）。
 */

import { normalizeSessionId } from "@/lib/session-id";

import type { SessionRuntimeEntrySnapshot } from "./types";

export type SessionRuntimeNoticeKind =
  | "turn_finished" // 后台回合收尾（有产物、可切回查看）
  | "approval" // 待审批
  | "question" // 待回答
  | "plan_review"; // 计划评审待裁决

export type SessionRuntimeNotice = {
  /** 稳定 id：同一会话同一类通知去重（`${sessionId}#${kind}`）。 */
  id: string;
  sessionId: string;
  kind: SessionRuntimeNoticeKind;
  /** 生成时间（毫秒）；呈现层只用它排序 / 自动消失，不参与业务判定。 */
  createdAt: number;
};

/** 页内最多同时挂几条：后台会话再多也不淹没当前会话（§4.6 内存约束）。 */
export const MAX_SESSION_RUNTIME_NOTICES = 3;

function noticeId(sessionId: string, kind: SessionRuntimeNoticeKind): string {
  return `${sessionId}#${kind}`;
}

/** 优先级：等用户裁决的在前，完成类在后（与侧栏行状态优先级同口径）。 */
const KIND_PRIORITY: Record<SessionRuntimeNoticeKind, number> = {
  approval: 0,
  plan_review: 1,
  question: 2,
  turn_finished: 3,
};

/**
 * 条目变化 → 应提示的通知（纯函数）。
 *
 * 规则：
 * - 选中会话跳过（前台消息流 / 卡片已经呈现，重复提示是噪音）；
 * - `turn_finished`：上一次有 `activeTurn`、本次没有（后台回合收尾）；
 * - 待交互：`approvals` / `questions` / `planPending` 从「无」跨到「有」才提示
 *   （数量变化不重复提示，避免审批排队时刷屏）；
 * - 上一份快照缺失时按全零基线比较：新出现的等待类条目同样提示（用户需要知道
 *   「哪个会话在等我」）。
 */
export function diffSessionRuntimeNotices(
  previous: ReadonlyMap<string, SessionRuntimeEntrySnapshot>,
  next: readonly SessionRuntimeEntrySnapshot[],
  options: { selectedSessionId?: string | null; now?: number } = {},
): SessionRuntimeNotice[] {
  const selected = options.selectedSessionId
    ? normalizeSessionId(options.selectedSessionId) ||
      options.selectedSessionId.trim()
    : "";
  const now = options.now ?? Date.now();
  const notices: SessionRuntimeNotice[] = [];

  for (const snapshot of next) {
    const rawSessionId = snapshot.sessionId?.trim();
    if (!rawSessionId) {
      continue;
    }
    const sessionId = normalizeSessionId(rawSessionId) || rawSessionId;
    if (sessionId === selected) {
      continue;
    }
    const before = previous.get(sessionId);

    if (before?.activeTurn != null && snapshot.activeTurn == null) {
      notices.push({
        id: noticeId(sessionId, "turn_finished"),
        sessionId,
        kind: "turn_finished",
        createdAt: now,
      });
    }

    const pendingBefore = before?.pending;
    if (
      snapshot.pending.approvals > 0 &&
      (pendingBefore?.approvals ?? 0) === 0
    ) {
      notices.push({
        id: noticeId(sessionId, "approval"),
        sessionId,
        kind: "approval",
        createdAt: now,
      });
    }
    if (
      snapshot.pending.questions > 0 &&
      (pendingBefore?.questions ?? 0) === 0
    ) {
      notices.push({
        id: noticeId(sessionId, "question"),
        sessionId,
        kind: "question",
        createdAt: now,
      });
    }
    if (snapshot.pending.planPending && !(pendingBefore?.planPending ?? false)) {
      notices.push({
        id: noticeId(sessionId, "plan_review"),
        sessionId,
        kind: "plan_review",
        createdAt: now,
      });
    }
  }

  return notices.sort(
    (left, right) => KIND_PRIORITY[left.kind] - KIND_PRIORITY[right.kind],
  );
}

// ── store（模块级单例，形态与 `live-diagnostics/store.ts` 一致） ─────────────

let notices: readonly SessionRuntimeNotice[] = [];
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) {
    listener();
  }
}

export function subscribeSessionRuntimeNotices(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function getSessionRuntimeNoticesSnapshot(): readonly SessionRuntimeNotice[] {
  return notices;
}

/**
 * 追加通知：同会话同类通知只保留最新一条（去重置顶），总量按
 * `MAX_SESSION_RUNTIME_NOTICES` 截断——后台会话再多也不挤占当前会话。
 */
export function pushSessionRuntimeNotices(
  incoming: readonly SessionRuntimeNotice[],
): void {
  if (incoming.length === 0) {
    return;
  }
  const deduped = incoming.filter(
    (notice) =>
      notice.sessionId.trim().length > 0 &&
      !notices.some(
        (current) => current.sessionId === notice.sessionId && current.kind === notice.kind,
      ),
  );
  if (deduped.length === 0) {
    return;
  }
  notices = [...deduped, ...notices].slice(0, MAX_SESSION_RUNTIME_NOTICES);
  emit();
}

export function dismissSessionRuntimeNotice(id: string): void {
  const next = notices.filter((notice) => notice.id !== id);
  if (next.length === notices.length) {
    return;
  }
  notices = next;
  emit();
}

/** 切回某会话时清掉它的通知（用户已经看到现场，不再提示）。 */
export function dismissSessionRuntimeNoticesForSession(sessionId: string): void {
  const normalized = normalizeSessionId(sessionId) || sessionId.trim();
  if (!normalized) {
    return;
  }
  const next = notices.filter((notice) => notice.sessionId !== normalized);
  if (next.length === notices.length) {
    return;
  }
  notices = next;
  emit();
}

export function clearSessionRuntimeNotices(): void {
  if (notices.length === 0) {
    return;
  }
  notices = [];
  emit();
}
