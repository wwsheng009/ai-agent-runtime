// §6.8 托管挂起（parked turn）的可观测状态：类型与纯函数归约。
//
// 数据来源（后端已登记契约，生成物见 `types/runtime/event-contract.ts`）：
//   * `turn.suspended`：durable 宿主把父 turn 置为挂起（等待子 Agent / Team 的
//     obligation 终态）后发出，**边沿触发、同一 turn 只发一次**；
//     载荷：`turn_id` / `session_id` / `batch_id` / `obligation_count` /
//     `resume_queue_count` / `parked_at`。
//   * `turn.resumed`：宿主把 wake 真正投递成一次 resume episode 后发出
//     （投递失败不发）；载荷：`session_id` / `turn_id` / `trigger` /
//     `pending_count` / `status` / `terminal` / `wake_ids` / `wake_reasons` /
//     `summary`。
//
// 收口纪律（对齐设计口径 §6.8）：
//   * 状态按 `session_id` 隔离存放，呈现方按当前会话 select——会话切换不串台；
//   * 只有 `turn.resumed` 清除挂起态；`agent.turn.finished` 只表示「本次 run
//     结束」，**不得**据此清掉挂起表达；
//   * `turn.resumed` 带 turn 身份时只清同一 turn（迟到的 resume 不误清新挂起）；
//   * 事件缺 `session_id` 时宁可不建条目（无法归属的挂起态绝不挂到别家会话）；
//   * 「迟到唤醒」（gap 3b）：父 turn 正常结束、从未 suspended 时，若干分钟后的
//     `turn.resumed` 仍按会话记一条瞬时通知；它与挂起态分开存放、互不冒充，
//     同会话新一轮 `turn.suspended` 到达即清除（新生命周期开始）。

/** 单条挂起快照（只保留 UI 需要的字段，未知字段不透传）。 */
export type ParkedTurnSnapshot = {
  sessionId: string;
  turnId: string;
  batchId: string;
  /** 本 turn 的义务数（batch 行 + 任务行）。 */
  obligationCount: number;
  resumeQueueCount: number;
  parkedAt: string;
};

/**
 * 最近一次「被监督机制恢复 / 续跑」的瞬时通知（gap 3b 迟到唤醒表达）。
 *
 * 与 `ParkedTurnSnapshot` 分开存放的原因：挂起是**常驻状态**（义务未终态期间
 * 一直显示），本通知是**瞬时事件**（到点自动消失 / 下一轮生命周期清除），
 * 两者混存会让「已恢复」冒充「仍挂起」。
 */
export type ResumedTurnNotice = {
  sessionId: string;
  turnId: string;
  /** 唤醒触发源（后端 `trigger`，如 `terminal`）；缺失为空串。 */
  trigger: string;
  /** 恢复后的回合是否已进终态。 */
  terminal: boolean;
  /** 唤醒原因（后端 `wake_reasons` 原样投影）；缺失为空数组。 */
  wakeReasons: string[];
  /** 事件时间戳；缺失为空串。 */
  resumedAt: string;
};

/** 归约状态：按 session 存放；挂起同一会话至多一条（边沿触发）。 */
export type ParkedTurnState = {
  bySession: Record<string, ParkedTurnSnapshot>;
  /** 最近一次被监督恢复的通知，按 session 存放；新一轮挂起清除。 */
  lastResumeBySession: Record<string, ResumedTurnNotice>;
};

/**
 * 呈现面：托管挂起快照 + 迟到唤醒通知（两者皆空 = 无表达）。
 *
 * 打包成一个对象下传，与 `ParkedTurnView` 同一理由（调用方不拼两个可空 prop）；
 * 字段分开而非合并，保证「挂起」与「已恢复」在渲染层不互相冒充。
 */
export type ParkedTurnSurface = {
  turn: ParkedTurnSnapshot | null;
  resumedNotice: ResumedTurnNotice | null;
};

/**
 * 任务投影计数（来自 `useSessionAgents` 的身份行目录，见 components/workspace/
 * session-agents-panel-shared.ts 的 `projectParkedTurnTaskCounts`）：
 * 用于把「等待 N 个义务」升级为设计文案「N 个任务运行中（M 完成 / K 异常）」。
 */
export type ParkedTurnTaskCounts = {
  running: number;
  completed: number;
  failed: number;
};

/**
 * 呈现端的挂起视图：事件快照 + 可选的目录投影计数。
 *
 * 打包成一个视图对象的原因：两者同属「托管中」这一条表达，一起下传避免
 * 调用方（shell / dock）分别拼装两个可空 prop；`taskCounts` 为 null 时按
 * `turn.obligationCount` 降级呈现。
 */
export type ParkedTurnView = {
  /** 当前挂起快照；仅迟到唤醒通知时为 null。 */
  turn: ParkedTurnSnapshot | null;
  taskCounts: ParkedTurnTaskCounts | null;
  /** 最近一次被监督恢复的瞬时通知；无 / 已过期时为 null。 */
  resumedNotice: ResumedTurnNotice | null;
};

export function emptyParkedTurnState(): ParkedTurnState {
  return { bySession: {}, lastResumeBySession: {} };
}

export function normalizeParkedTurnEventType(type: string): string {
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

/** 计数字段：只接受非负有限数；缺失 / 非法回退 0（不伪造义务数）。 */
function readCount(
  payload: Record<string, unknown> | undefined,
  ...keys: string[]
): number {
  if (!payload) {
    return 0;
  }
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === "number" && Number.isFinite(value) && value > 0) {
      return Math.floor(value);
    }
  }
  return 0;
}

/** 布尔字段：只接受真布尔；缺失 / 非法回退 false（不把未知当终态）。 */
function readBoolean(
  payload: Record<string, unknown> | undefined,
  ...keys: string[]
): boolean {
  if (!payload) {
    return false;
  }
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === "boolean") {
      return value;
    }
  }
  return false;
}

/** 字符串数组字段：逐项 trim、剔除空项；缺失 / 非法回退空数组。 */
function readStringArray(
  payload: Record<string, unknown> | undefined,
  ...keys: string[]
): string[] {
  if (!payload) {
    return [];
  }
  for (const key of keys) {
    const value = payload[key];
    if (Array.isArray(value)) {
      return value
        .map((item) => (typeof item === "string" ? item.trim() : ""))
        .filter((item) => item !== "");
    }
  }
  return [];
}

/** 删除会话键；无该键时原样返回（保持引用稳定，避免无意义重渲染）。 */
function omitSession<T>(
  map: Record<string, T>,
  sessionId: string,
): Record<string, T> {
  if (!(sessionId in map)) {
    return map;
  }
  const next = { ...map };
  delete next[sessionId];
  return next;
}

/** 两条恢复通知是否同一条（重连重放判定，避免无意义重渲染）。 */
function sameResumedTurnNotice(
  a: ResumedTurnNotice,
  b: ResumedTurnNotice,
): boolean {
  return (
    a.sessionId === b.sessionId &&
    a.turnId === b.turnId &&
    a.trigger === b.trigger &&
    a.terminal === b.terminal &&
    a.resumedAt === b.resumedAt &&
    a.wakeReasons.length === b.wakeReasons.length &&
    a.wakeReasons.every((reason, index) => reason === b.wakeReasons[index])
  );
}

/**
 * 归属会话：载荷优先，回落事件信封的 `session_id`。
 *
 * 后端两个事件都把 `session_id` 写进载荷；信封字段是 wire 兼容的兜底。
 */
export function parkedTurnSessionId(
  event: { session_id?: string; payload?: Record<string, unknown> },
): string {
  return (
    readString(event.payload, "session_id", "sessionId") ||
    (event.session_id ?? "").trim()
  );
}

/**
 * 单事件归约：
 *   * `turn.suspended` 建 / 覆盖挂起条，并清除同会话旧恢复通知（新生命周期）；
 *   * `turn.resumed` 清除同 turn 挂起条，并按会话记一条恢复通知（迟到唤醒也记）；
 *   * `agent.turn.finished` 等其它事件一律忽略（本次 run 结束 ≠ 义务终态）。
 */
export function applyParkedTurnEvent(
  state: ParkedTurnState,
  event: { type: string; session_id?: string; payload?: Record<string, unknown>; timestamp?: string },
): ParkedTurnState {
  const type = normalizeParkedTurnEventType(event.type);

  if (type === "turn.suspended") {
    const sessionId = parkedTurnSessionId(event);
    if (!sessionId) {
      return state;
    }
    const turnId = readString(event.payload, "turn_id", "turnId");
    const obligationCount = readCount(
      event.payload,
      "obligation_count",
      "obligationCount",
    );
    const resumeQueueCount = readCount(
      event.payload,
      "resume_queue_count",
      "resumeQueueCount",
    );
    const current = state.bySession[sessionId];
    const previousNotice = state.lastResumeBySession[sessionId];
    // 同一 turn 的重复投递（重连重放）且无待清通知：不产生新引用，避免无意义重渲染。
    if (
      current &&
      !previousNotice &&
      current.turnId === turnId &&
      current.obligationCount === obligationCount &&
      current.resumeQueueCount === resumeQueueCount
    ) {
      return state;
    }
    return {
      bySession: {
        ...state.bySession,
        [sessionId]: {
          sessionId,
          turnId,
          batchId: readString(event.payload, "batch_id", "batchId"),
          obligationCount,
          resumeQueueCount,
          parkedAt:
            readString(event.payload, "parked_at", "parkedAt") ||
            event.timestamp ||
            "",
        },
      },
      // 新一轮生命周期开始：旧「已恢复」通知不得继续挂着。
      lastResumeBySession: omitSession(state.lastResumeBySession, sessionId),
    };
  }

  if (type === "turn.resumed") {
    const sessionId = parkedTurnSessionId(event);
    if (!sessionId) {
      return state;
    }
    const resumedTurnId = readString(event.payload, "turn_id", "turnId");
    const parked = state.bySession[sessionId];
    const parkedTurnId = parked?.turnId ?? "";
    // 迟到 / 串 turn 的 resume 不误清当前挂起的另一个 turn，也不写恢复通知
    // （通知必须属于该会话当前可见的生命周期，否则「已恢复」会与挂起态打架）。
    if (resumedTurnId && parkedTurnId && resumedTurnId !== parkedTurnId) {
      return state;
    }
    const notice: ResumedTurnNotice = {
      sessionId,
      turnId: resumedTurnId,
      trigger: readString(event.payload, "trigger"),
      terminal: readBoolean(event.payload, "terminal"),
      wakeReasons: readStringArray(event.payload, "wake_reasons", "wakeReasons"),
      resumedAt: event.timestamp ?? "",
    };
    const currentNotice = state.lastResumeBySession[sessionId];
    // 同一恢复通知的重复投递（重连重放）不产生新引用。
    if (!parked && currentNotice && sameResumedTurnNotice(currentNotice, notice)) {
      return state;
    }
    return {
      bySession: omitSession(state.bySession, sessionId),
      lastResumeBySession: { ...state.lastResumeBySession, [sessionId]: notice },
    };
  }

  // `agent.turn.finished` 等其它事件一律不改挂起态：本次 run 结束 ≠ 义务终态。
  return state;
}

/** 按当前会话取挂起快照（会话隔离的呈现入口）。 */
export function selectParkedTurn(
  state: ParkedTurnState,
  sessionId: string | null | undefined,
): ParkedTurnSnapshot | null {
  const key = sessionId?.trim() ?? "";
  if (!key) {
    return null;
  }
  return state.bySession[key] ?? null;
}

/** 按当前会话取「最近一次被监督恢复」通知（会话隔离的呈现入口）。 */
export function selectResumedTurnNotice(
  state: ParkedTurnState,
  sessionId: string | null | undefined,
): ResumedTurnNotice | null {
  const key = sessionId?.trim() ?? "";
  if (!key) {
    return null;
  }
  return state.lastResumeBySession[key] ?? null;
}

/**
 * 清除给定的恢复通知（瞬时表达的自动消失入口）。
 *
 * 按引用比对：只清掉仍是当前这条通知的条目；若已被新一轮生命周期替换 /
 * 清除，迟到的定时器不得误删新通知（保持引用则返回原状态）。
 */
export function clearResumedTurnNotice(
  state: ParkedTurnState,
  notice: ResumedTurnNotice,
): ParkedTurnState {
  if (state.lastResumeBySession[notice.sessionId] !== notice) {
    return state;
  }
  return {
    ...state,
    lastResumeBySession: omitSession(state.lastResumeBySession, notice.sessionId),
  };
}
