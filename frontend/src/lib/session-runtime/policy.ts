// Batch 4（多会话并发运行时 §4.2 / §4.6）：订阅策略的纯函数层。
//
// 分工：本模块只回答「谁该被订阅、以什么理由、谁该被释放」；连接预算（live 名额）
// 与模式切换由 `registry.ts` 收敛。这样策略可以脱离 React / 网络单测，页面层
// （`use-session-stream-supervisor.ts`）只负责把最新输入喂进来并执行指令。
//
// 缺省口径与方案一致：选中会话 `selected`；候选里已知有在途回合 `active-turn`
// （live）；最近活动窗口内 `recent`（先 poll，注册表按预算升级）；窗口外 `idle`
// 释放；不在候选集合里的已订阅会话 `deselected` 释放。

import type {
  SessionRuntimeEnsureReason,
  SessionRuntimeReleaseReason,
} from "./registry";

/** 会话列表 → 订阅候选（由页面把会话元数据投影成最小字段）。 */
export type SessionStreamSupervisorCandidate = {
  sessionId: string;
  /** 会话元数据更新时间（ISO）；用于「最近活动」窗口判定。 */
  updatedAt?: string | null;
  /** 已知有在途回合（本地提交或 `/runtime` 快照）→ 直接按活跃回合升级 live。 */
  hasActiveTurn?: boolean;
};

/** 「最近活动」窗口（§4.2）：窗口内的后台会话值得订阅（先 poll，按预算升级）。 */
export const DEFAULT_RECENT_WINDOW_MS = 10 * 60_000;

export type SessionSubscriptionPlanEntry = {
  sessionId: string;
  reason: SessionRuntimeEnsureReason;
};

export type SessionSubscriptionReleaseEntry = {
  sessionId: string;
  reason: SessionRuntimeReleaseReason;
};

export type SessionSubscriptionPlan = {
  ensure: readonly SessionSubscriptionPlanEntry[];
  release: readonly SessionSubscriptionReleaseEntry[];
};

export type SessionSubscriptionPlanInput = {
  selectedSessionId?: string | null;
  candidates: readonly SessionStreamSupervisorCandidate[];
  /** 注册表当前已知的条目（用于释放「已不在候选集合」的订阅）。 */
  knownSessionIds?: readonly string[];
  /** 策略求值时刻（毫秒）；由调用方传入，保持纯函数可测。 */
  now: number;
  recentWindowMs?: number;
};

/**
 * 把「选中会话 + 候选集合 + 现有条目」翻译成 ensure / release 指令。
 *
 * 幂等：同一输入重复调用得到同一计划；注册表侧 `ensure` 按模式收敛，不会重复建连。
 * 去重：同一会话重复出现时后一条候选生效（页面投影可能按目录分组重复出现）。
 */
export function planSessionSubscriptions(
  input: SessionSubscriptionPlanInput,
): SessionSubscriptionPlan {
  const {
    selectedSessionId,
    candidates,
    knownSessionIds = [],
    now,
    recentWindowMs = DEFAULT_RECENT_WINDOW_MS,
  } = input;

  const selected = selectedSessionId?.trim() ?? "";
  const ensure: SessionSubscriptionPlanEntry[] = [];
  const release: SessionSubscriptionReleaseEntry[] = [];
  const keep = new Set<string>();

  if (selected) {
    ensure.push({ sessionId: selected, reason: "selected" });
    keep.add(selected);
  }

  // 候选去重：同一会话只结算一次，最后一条候选的字段生效。
  const deduped = new Map<string, SessionStreamSupervisorCandidate>();
  for (const candidate of candidates) {
    const sessionId = candidate.sessionId?.trim();
    if (!sessionId) {
      continue;
    }
    deduped.set(sessionId, { ...candidate, sessionId });
  }

  for (const candidate of deduped.values()) {
    const { sessionId } = candidate;
    keep.add(sessionId);
    if (sessionId === selected) {
      continue;
    }
    if (candidate.hasActiveTurn) {
      // 后台活跃回合：live（受后台预算约束，超预算自动降 poll，名额空出时补位）。
      ensure.push({ sessionId, reason: "active-turn" });
      continue;
    }
    const updatedAtMs = candidate.updatedAt
      ? Date.parse(candidate.updatedAt)
      : Number.NaN;
    const recent =
      Number.isFinite(updatedAtMs) && now - updatedAtMs <= recentWindowMs;
    if (recent) {
      ensure.push({ sessionId, reason: "recent" });
    } else {
      release.push({ sessionId, reason: "idle" });
    }
  }

  // 不再出现在候选集合里的会话（归档 / 删除 / 超出列表窗口）立即释放。
  for (const sessionId of knownSessionIds) {
    if (!keep.has(sessionId)) {
      release.push({ sessionId, reason: "deselected" });
    }
  }

  return { ensure, release };
}
