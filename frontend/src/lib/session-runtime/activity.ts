/**
 * Batch 3（多会话并发运行时 §4.7.1）：注册表条目 → 侧栏活动投影。
 *
 * 约束（D1-B）：后台会话不写 `threads`、不渲染消息；侧栏只消费这份轻量信号。
 * 优先级判定仍由 `resolveSidebarSessionRowStatus` 负责，本模块只做「谁有什么信号」。
 */

import { normalizeSessionId } from "@/lib/session-id";

import type { SessionRuntimeEntrySnapshot } from "./types";

/** 与 `SidebarSessionActivity` 结构等价的信号集（lib 层不依赖组件类型）。 */
export type SessionActivitySignal = {
  pendingApprovals?: number;
  planPending?: boolean;
  waitingAnswer?: boolean;
  running?: boolean;
  runningAgents?: number;
};

const EMPTY_ACTIVITY: Record<string, SessionActivitySignal> = {};

export function projectSessionActivity(
  snapshots: Iterable<SessionRuntimeEntrySnapshot>,
  options: { runningSessionIds?: Iterable<string> } = {},
): Record<string, SessionActivitySignal> {
  const localRunning = new Set<string>();
  for (const sessionId of options.runningSessionIds ?? []) {
    const normalized = normalizeSessionId(sessionId) || sessionId.trim();
    if (normalized) {
      localRunning.add(normalized);
    }
  }

  const result: Record<string, SessionActivitySignal> = {};
  const seen = new Set<string>();
  for (const snapshot of snapshots) {
    const rawSessionId = snapshot.sessionId?.trim();
    if (!rawSessionId) {
      continue;
    }
    const sessionId = normalizeSessionId(rawSessionId) || rawSessionId;
    seen.add(sessionId);

    const activity: SessionActivitySignal = {};
    if (snapshot.pending.approvals > 0) {
      activity.pendingApprovals = snapshot.pending.approvals;
    }
    if (snapshot.pending.planPending) {
      activity.planPending = true;
    }
    if (snapshot.pending.questions > 0) {
      activity.waitingAnswer = true;
    }
    // 运行中判据：服务端在途回合，或本地回合仍在跑（后台 chat 请求尚未收尾）。
    if (snapshot.activeTurn != null || localRunning.has(sessionId)) {
      activity.running = true;
    }
    if (snapshot.runningAgents > 0) {
      activity.runningAgents = snapshot.runningAgents;
    }

    if (Object.keys(activity).length > 0) {
      result[sessionId] = activity;
    }
  }

  // 本地在途会话即使还没有注册表条目（例如服务端 sessionId 尚未回填），
  // 也要在侧栏显示「运行中」——否则 A 后台跑着、切到 B 后 A 的行会闪回空闲。
  for (const sessionId of localRunning) {
    if (seen.has(sessionId) && result[sessionId]?.running) {
      continue;
    }
    result[sessionId] = { ...(result[sessionId] ?? EMPTY_ACTIVITY), running: true };
  }

  return result;
}

/** 合并多份投影：后者覆盖同名会话的同名字段。 */
export function mergeSessionActivity(
  ...records: Array<Record<string, SessionActivitySignal> | undefined>
): Record<string, SessionActivitySignal> {
  const merged: Record<string, SessionActivitySignal> = {};
  for (const record of records) {
    if (!record) {
      continue;
    }
    for (const [sessionId, activity] of Object.entries(record)) {
      merged[sessionId] = { ...(merged[sessionId] ?? {}), ...activity };
    }
  }
  return merged;
}
