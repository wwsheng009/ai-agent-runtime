// P2-1A：会话运行时状态快照 REST 客户端。
//
// 端点（backend/internal/api/skills/handler.go；实现 session_runtime_handlers.go:375-408）：
//   GET /api/runtime/sessions/{id}/runtime
//     - 200 { state, ...execution_route }        —— 该会话有 durable runtime state；
//     - 200 { session_id, state: null }          —— 会话存在但从未进入 durable
//       session actor（例如只经无状态 `/api/agent/chat` 的 web 会话）。这是显式
//       空态：`getSessionRuntimeState` 归一化为 null，不抛错、不伪造状态；
//     - 404 SESSION_NOT_FOUND                    —— 会话不存在（已删除 / 未知 id）；
//     - 503/504 STORE_*                          —— 会话存储故障（可重试）。
//
// P4-刷新续传：两个 200 分支都带 `active_turn`（`session_active_turn.go`）。它是
// `state` 的兄弟字段而非其子字段，因此在 `state: null` 的 web 会话上同样有效——
// 那正是「刷新后回合仍在跑」的唯一信号：只要它非空，快照就不是空态。
//
// 归一化说明：同时接受 snake_case / camelCase（与 jobs 同策略），避免后端补
// json tag 时前端二次改动；既非「显式空态」又缺 `state` 视为契约不满足
// （抛错而非伪造空态）。

import type {
  RuntimeSessionActiveTurn,
  RuntimeSessionApproval,
  RuntimeSessionQuestion,
  RuntimeSessionSnapshot,
  RuntimeSessionState,
} from "@/types/runtime";

import {
  buildRuntimeUrl,
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "./shared";

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function pickString(record: RawRecord, ...keys: string[]): string {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

function pickOptionalString(
  record: RawRecord,
  ...keys: string[]
): string | undefined {
  const value = pickString(record, ...keys);
  return value ? value : undefined;
}

function pickNumber(record: RawRecord, ...keys: string[]): number {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "number" && Number.isFinite(value)) {
      return value;
    }
  }
  return 0;
}

function pickStringArray(record: RawRecord, ...keys: string[]): string[] {
  for (const key of keys) {
    const value = record[key];
    if (Array.isArray(value)) {
      return value
        .map((item) => (typeof item === "string" ? item.trim() : ""))
        .filter((item) => item.length > 0);
    }
  }
  return [];
}

/** `SessionApprovalRequest` → 归一化审批（缺 id 返回 null：绝不伪造身份）。 */
export function normalizeSessionApproval(
  raw: unknown,
): RuntimeSessionApproval | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const id = pickString(record, "id", "request_id", "requestId");
  if (!id) {
    return null;
  }
  return {
    id,
    sessionId: pickString(record, "session_id", "sessionId"),
    toolName: pickString(record, "tool_name", "toolName"),
    reason: pickString(record, "reason"),
    riskLevel: pickString(record, "risk_level", "riskLevel"),
    ...(pickOptionalString(record, "expires_at", "expiresAt")
      ? { expiresAt: pickString(record, "expires_at", "expiresAt") }
      : {}),
  };
}

/** `SessionQuestionRequest` → 归一化提问（缺 id 返回 null）。 */
export function normalizeSessionQuestion(
  raw: unknown,
): RuntimeSessionQuestion | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const id = pickString(record, "id", "question_id", "questionId");
  if (!id) {
    return null;
  }
  const required = record.required;
  return {
    id,
    sessionId: pickString(record, "session_id", "sessionId"),
    prompt: pickString(record, "prompt", "question"),
    required: typeof required === "boolean" ? required : false,
    suggestions: pickStringArray(record, "suggestions"),
    ...(pickOptionalString(record, "expires_at", "expiresAt")
      ? { expiresAt: pickString(record, "expires_at", "expiresAt") }
      : {}),
  };
}

export function normalizeSessionRuntimeState(
  raw: unknown,
): RuntimeSessionState | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const sessionId = pickString(record, "session_id", "sessionId");
  const status = pickString(record, "status");
  if (!sessionId || !status) {
    return null;
  }
  return {
    sessionId,
    status,
    ...(pickOptionalString(record, "current_turn_id", "currentTurnId")
      ? { currentTurnId: pickString(record, "current_turn_id", "currentTurnId") }
      : {}),
    ...(pickOptionalString(
      record,
      "current_checkpoint_id",
      "currentCheckpointId",
    )
      ? {
          currentCheckpointId: pickString(
            record,
            "current_checkpoint_id",
            "currentCheckpointId",
          ),
        }
      : {}),
    pendingApproval: normalizeSessionApproval(
      record.pending_approval ?? record.pendingApproval,
    ),
    pendingQuestion: normalizeSessionQuestion(
      record.pending_question ?? record.pendingQuestion,
    ),
    headOffset: pickNumber(record, "head_offset", "headOffset"),
    activeJobIds: pickStringArray(record, "active_job_ids", "activeJobIds"),
    ...(pickOptionalString(record, "updated_at", "updatedAt")
      ? { updatedAt: pickString(record, "updated_at", "updatedAt") }
      : {}),
  };
}

/**
 * P4-刷新续传：`active_turn` → 归一化在途回合（缺 turn_id 返回 null：回合身份
 * 是续传的唯一钥匙，绝不能伪造）。
 */
export function normalizeSessionRuntimeActiveTurn(
  raw: unknown,
): RuntimeSessionActiveTurn | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const turnId = pickString(record, "turn_id", "turnId");
  if (!turnId) {
    return null;
  }
  const detached = record.detached;
  return {
    sessionId: pickString(record, "session_id", "sessionId"),
    turnId,
    source: pickString(record, "source"),
    detached: typeof detached === "boolean" ? detached : false,
    ...(pickOptionalString(record, "started_at", "startedAt")
      ? { startedAt: pickString(record, "started_at", "startedAt") }
      : {}),
  };
}

export function normalizeSessionRuntimeSnapshot(
  raw: unknown,
): RuntimeSessionSnapshot | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const state = normalizeSessionRuntimeState(record.state ?? record);
  const activeTurn = normalizeSessionRuntimeActiveTurn(
    record.active_turn ?? record.activeTurn,
  );
  // 既无 durable state 又无在途回合 = 显式空态（旧后端只有这一种形态）；
  // 只有 active_turn 的响应（web 会话 + 在途回合）是有效快照，刷新续传依赖它。
  if (!state && !activeTurn) {
    return null;
  }
  const executionRoute =
    asRecord(record.execution_route) ?? asRecord(record.executionRoute);
  return {
    state,
    activeTurn,
    ...(executionRoute ? { executionRoute } : {}),
  };
}

/**
 * 显式空快照判定：`{ session_id, state: null }`。
 *
 * 这是后端「会话存在、但从未进入 durable session actor」的正常终态（旧实现把它
 * 与「会话不存在」一起写成 404，调用方无法区分，只能在控制台留下误导性 404）。
 * 归一化函数对它返回 null，因此需要用本判定与「坏响应」区分：
 * 显式空态 → 返回空态；坏响应 → 抛错。
 *
 * P4-刷新续传：带 `active_turn` 的 `state: null` **不是**空态——回合正在服务端
 * 执行，刷新后的页面必须拿到这条身份去续传（否则表现为「刷新即断流」）。
 */
export function isEmptySessionRuntimeSnapshot(raw: unknown): boolean {
  const record = asRecord(raw);
  if (!record) {
    return false;
  }
  if (!("state" in record) || record.state !== null) {
    return false;
  }
  return (
    normalizeSessionRuntimeActiveTurn(
      record.active_turn ?? record.activeTurn,
    ) === null
  );
}

export type SessionRuntimeStateOptions = {
  signal?: AbortSignal;
  /**
   * `light`：轻量视图（后端 `?view=light`）——省略 stable_tool_surface 等大字段，
   * 供后台会话低频轮询使用；缺省 = 完整快照（前台与刷新续传仍走完整视图）。
   */
  view?: "light";
};

/**
 * 读取会话运行时状态快照。
 *
 * 返回 `null` = 后端显式声明「该会话没有 durable runtime state」（空态）；
 * 抛出 = 契约不满足或传输/存储故障（404 = 会话不存在，带 `status` 供调用方按空态
 * 处理；503/504 = 存储故障，属于可重试错误）。
 */
export async function getSessionRuntimeState(
  sessionId: string,
  options: SessionRuntimeStateOptions = {},
): Promise<RuntimeSessionSnapshot | null> {
  const trimmed = sessionId.trim();
  if (!trimmed) {
    throw new Error("session id is required");
  }
  const path = `/api/runtime/sessions/${encodeURIComponent(trimmed)}/runtime`;
  const raw = await fetchRuntimeJson<unknown>(
    options.view === "light"
      ? buildRuntimeUrlWithQuery(path, { view: "light" })
      : buildRuntimeUrl(path),
    { signal: options.signal },
  );
  if (isEmptySessionRuntimeSnapshot(raw)) {
    return null;
  }
  const snapshot = normalizeSessionRuntimeSnapshot(raw);
  if (!snapshot) {
    throw new Error("invalid session runtime state payload");
  }
  return snapshot;
}
