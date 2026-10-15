// 会话 goal 管理端点的客户端封装（2026-10-15 起后端已提供 REST）。
//
// 端点事实（backend/internal/api/runtimeapi/session_goal_handlers.go）：
//   * GET  /api/runtime/sessions/{id}/goal
//        → {session_id, goal: {...} | null, remaining_tokens}（无 goal 是 200 + null，不是 404）
//   * POST /api/runtime/sessions/{id}/goal
//        body {action: set|pause|resume|complete|clear|budget, objective?, token_budget?, summary?}
//        → {session_id, action, applied, message, goal: {...} | null, remaining_tokens}
//
// 语义与 CLI `/goal` 一一对应（校验与状态机在服务端 internal/goal，前端不重算）：
//   * `applied=false` 表示合并策略按守卫保留了原状态（如 complete 终态、预算未提高的
//     budget_limited）——调用方必须如实呈现 message，不得当成成功。
//   * 写端点要求回环 / admin token / admin role；被拒时抛 RuntimeApiError(403)。
//
// 载荷形状与工具结果**同构**，因此解析复用 lib/session-goal/derive 的
// `parseSessionGoalEnvelope`（单一解析器，避免两条路径漂移）。

import {
  parseSessionGoalEnvelope,
  type GoalObservation,
} from "@/lib/session-goal/derive";

import { asRecord, buildRuntimeUrl, fetchRuntimeJson, readString } from "./shared";

export const RUNTIME_SESSIONS_PATH = "/api/runtime/sessions";

/** 会话 goal 端点路径（session id 逐段编码，避免特殊字符破坏路由）。 */
export function buildSessionGoalPath(sessionId: string) {
  return `${RUNTIME_SESSIONS_PATH}/${encodeURIComponent(sessionId.trim())}/goal`;
}

export type SessionGoalAction =
  | "set"
  | "pause"
  | "resume"
  | "complete"
  | "clear"
  | "budget";

export type UpdateSessionGoalRequest = {
  action: SessionGoalAction;
  /** action=set 必填。 */
  objective?: string;
  /** action=set|budget 使用；0 表示清除预算。 */
  tokenBudget?: number;
  /** action=complete 使用（完成证据摘要）。 */
  summary?: string;
};

export type SessionGoalMutationResult = {
  observation: GoalObservation;
  /** 服务端合并策略是否采纳了本次写入（false 时必须展示 message）。 */
  applied: boolean;
  message: string;
};

/** 读取权威快照；`goal: null` → `{kind: "absent"}`（此刻确实没有目标）。 */
export async function fetchSessionGoal(
  sessionId: string,
): Promise<GoalObservation> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(buildSessionGoalPath(sessionId)),
  );
  return parseSessionGoalEnvelope(payload);
}

/** 执行一次 goal 管理动作（set/pause/resume/complete/clear/budget）。 */
export async function updateSessionGoal(
  sessionId: string,
  request: UpdateSessionGoalRequest,
): Promise<SessionGoalMutationResult> {
  const body: Record<string, unknown> = { action: request.action };
  if (request.objective !== undefined) {
    body.objective = request.objective;
  }
  if (request.tokenBudget !== undefined) {
    body.token_budget = request.tokenBudget;
  }
  if (request.summary !== undefined) {
    body.summary = request.summary;
  }

  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(buildSessionGoalPath(sessionId)),
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    },
  );

  const record = asRecord(payload);
  return {
    observation: parseSessionGoalEnvelope(payload),
    applied: record?.["applied"] === true,
    message: readString(record?.["message"]),
  };
}
