// P2-1A：会话运行时状态快照 REST 客户端。
//
// 端点（backend/internal/api/skills/handler.go；实现 session_runtime_handlers.go:375-408）：
//   GET /api/runtime/sessions/{id}/runtime → { state, ...execution_route }
//
// 归一化说明：同时接受 snake_case / camelCase（与 jobs 同策略），避免后端补
// json tag 时前端二次改动；缺 `state` 视为契约不满足（抛错而非伪造空态）。

import type {
  RuntimeSessionApproval,
  RuntimeSessionQuestion,
  RuntimeSessionSnapshot,
  RuntimeSessionState,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

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

export function normalizeSessionRuntimeSnapshot(
  raw: unknown,
): RuntimeSessionSnapshot | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const state = normalizeSessionRuntimeState(record.state ?? record);
  if (!state) {
    return null;
  }
  const executionRoute =
    asRecord(record.execution_route) ?? asRecord(record.executionRoute);
  return {
    state,
    ...(executionRoute ? { executionRoute } : {}),
  };
}

export type SessionRuntimeStateOptions = {
  signal?: AbortSignal;
};

/** 读取会话运行时状态快照（404 = 该会话尚无 runtime state，由调用方按空态处理）。 */
export async function getSessionRuntimeState(
  sessionId: string,
  options: SessionRuntimeStateOptions = {},
): Promise<RuntimeSessionSnapshot> {
  const trimmed = sessionId.trim();
  if (!trimmed) {
    throw new Error("session id is required");
  }
  const raw = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(trimmed)}/runtime`,
    ),
    { signal: options.signal },
  );
  const snapshot = normalizeSessionRuntimeSnapshot(raw);
  if (!snapshot) {
    throw new Error("invalid session runtime state payload");
  }
  return snapshot;
}
