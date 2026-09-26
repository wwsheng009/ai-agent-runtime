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
//   * 事件缺 `session_id` 时宁可不建条目（无法归属的挂起态绝不挂到别家会话）。

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

/** 归约状态：按 session 存放；同一会话至多一条（挂起边沿触发）。 */
export type ParkedTurnState = {
  bySession: Record<string, ParkedTurnSnapshot>;
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
  turn: ParkedTurnSnapshot;
  taskCounts: ParkedTurnTaskCounts | null;
};

export function emptyParkedTurnState(): ParkedTurnState {
  return { bySession: {} };
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

/** 单事件归约：`turn.suspended` 建条（覆盖式、幂等）/ `turn.resumed` 清除。 */
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
    // 同一 turn 的重复投递（重连重放）不产生新引用，避免无意义重渲染。
    if (
      current &&
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
    };
  }

  if (type === "turn.resumed") {
    const sessionId = parkedTurnSessionId(event);
    if (!sessionId || !state.bySession[sessionId]) {
      return state;
    }
    const resumedTurnId = readString(event.payload, "turn_id", "turnId");
    const parkedTurnId = state.bySession[sessionId].turnId;
    // 迟到 / 串 turn 的 resume 不误清当前挂起的另一个 turn。
    if (resumedTurnId && parkedTurnId && resumedTurnId !== parkedTurnId) {
      return state;
    }
    const bySession = { ...state.bySession };
    delete bySession[sessionId];
    return { bySession };
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
