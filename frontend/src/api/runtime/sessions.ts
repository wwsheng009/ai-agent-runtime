import type {
  RuntimeCreateSessionRequest,
  RuntimeCreateSessionResponse,
  RuntimeSessionBacktrackAuditResponse,
  RuntimeSessionBacktrackRequest,
  RuntimeSessionBacktrackResponse,
  RuntimeSessionBatchActionResponse,
  RuntimeSessionCheckpointFilesResponse,
  RuntimeSessionCheckpointPreviewMode,
  RuntimeSessionCheckpointPreviewResponse,
  RuntimeSessionCheckpointRestoreResponse,
  RuntimeSessionCheckpointsQuery,
  RuntimeSessionCheckpointsResponse,
  RuntimeSessionPlanMode,
  RuntimeSessionPlanModeUpdateRequest,
  RuntimeSessionPermissionMode,
  RuntimeSessionPermissionModeUpdateRequest,
  RuntimeSessionRecord,
  RuntimeSessionStateChangeResponse,
  RuntimeSessionTurnsResponse,
  RuntimeSessionUsersResponse,
  RuntimeSessionsQuery,
  RuntimeSessionsResponse,
} from "@/types/runtime";

import {
  buildRuntimeUrl,
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "./shared";

// 会话事件 / 历史的窗口读取见 `./session-event-window`（P0-2 拆文件）；
// 此处 barrel re-export 保持 `@/api/runtime/sessions` 的既有调用方与
// `vi.mock("@/api/runtime/sessions")` 不变。
export {
  fetchSessionRuntimeEvents,
  getSessionHistory,
  type RuntimeSessionEventsQuery,
  type RuntimeSessionEventsResponse,
  type SessionHistoryQuery,
} from "./session-event-window";

export async function createRuntimeSession(
  request: RuntimeCreateSessionRequest,
): Promise<RuntimeCreateSessionResponse> {
  return fetchRuntimeJson<RuntimeCreateSessionResponse>(
    buildRuntimeUrl("/api/runtime/sessions"),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
}

export async function listRuntimeSessions(
  query: RuntimeSessionsQuery = {},
): Promise<RuntimeSessionsResponse> {
  return fetchRuntimeJson<RuntimeSessionsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/sessions", {
      user_id: query.userId,
    }),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function listRuntimeSessionUsers(): Promise<RuntimeSessionUsersResponse> {
  return fetchRuntimeJson<RuntimeSessionUsersResponse>(
    buildRuntimeUrl("/api/runtime/sessions/users"),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function getRuntimeSession(
  sessionId: string,
): Promise<{ session: RuntimeSessionRecord }> {
  return fetchRuntimeJson<{ session: RuntimeSessionRecord }>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}`),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export type RuntimeUpdateSessionRequest = {
  title?: string;
  context?: Record<string, unknown>;
};

export async function updateRuntimeSession(
  sessionId: string,
  request: RuntimeUpdateSessionRequest,
): Promise<{ session: RuntimeSessionRecord }> {
  return fetchRuntimeJson<{ session: RuntimeSessionRecord }>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}`),
    {
      method: "PATCH",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
}

export async function deleteRuntimeSession(
  sessionId: string,
): Promise<{ deleted: boolean; id: string }> {
  return fetchRuntimeJson<{ deleted: boolean; id: string }>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}`),
    {
      method: "DELETE",
      headers: {
        Accept: "application/json",
      },
    },
  );
}

/** 归档会话（P1-9）：列表默认隐藏归档，`activateRuntimeSession` 可恢复。 */
export async function archiveRuntimeSession(
  sessionId: string,
): Promise<RuntimeSessionStateChangeResponse> {
  return fetchRuntimeJson<RuntimeSessionStateChangeResponse>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/archive`),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
      },
    },
  );
}

/** 恢复（激活）已归档会话。 */
export async function activateRuntimeSession(
  sessionId: string,
): Promise<RuntimeSessionStateChangeResponse> {
  return fetchRuntimeJson<RuntimeSessionStateChangeResponse>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/activate`),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
      },
    },
  );
}

/** 关闭会话（保留数据，状态置 closed）。 */
export async function closeRuntimeSession(
  sessionId: string,
): Promise<RuntimeSessionStateChangeResponse> {
  return fetchRuntimeJson<RuntimeSessionStateChangeResponse>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/close`),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
      },
    },
  );
}

/** 批量归档（后端 body `{session_ids: [...]}`）。 */
export async function batchArchiveRuntimeSessions(
  sessionIds: string[],
): Promise<RuntimeSessionBatchActionResponse> {
  return fetchRuntimeJson<RuntimeSessionBatchActionResponse>(
    buildRuntimeUrl("/api/runtime/sessions/batch/archive"),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ session_ids: sessionIds }),
    },
  );
}

/**
 * 批量删除（非破坏语义由调用方保证：仅移除引用后刷新列表，
 * 不连带删除工作区或其他会话数据）。
 */
export async function batchDeleteRuntimeSessions(
  sessionIds: string[],
): Promise<RuntimeSessionBatchActionResponse> {
  return fetchRuntimeJson<RuntimeSessionBatchActionResponse>(
    buildRuntimeUrl("/api/runtime/sessions/batch/delete"),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ session_ids: sessionIds }),
    },
  );
}

export async function listSessionCheckpoints(
  sessionId: string,
  query: RuntimeSessionCheckpointsQuery = {},
): Promise<RuntimeSessionCheckpointsResponse> {
  return fetchRuntimeJson<RuntimeSessionCheckpointsResponse>(
    buildRuntimeUrlWithQuery(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/checkpoints`,
      {
        limit: query.limit,
        offset: query.offset,
      },
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function getSessionCheckpointFiles(
  sessionId: string,
  checkpointId: string,
): Promise<RuntimeSessionCheckpointFilesResponse> {
  return fetchRuntimeJson<RuntimeSessionCheckpointFilesResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/checkpoints/${encodeURIComponent(checkpointId)}/files`,
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function previewSessionCheckpoint(
  sessionId: string,
  checkpointId: string,
  mode: RuntimeSessionCheckpointPreviewMode = "both",
): Promise<RuntimeSessionCheckpointPreviewResponse> {
  return fetchRuntimeJson<RuntimeSessionCheckpointPreviewResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/checkpoints/${encodeURIComponent(checkpointId)}/preview`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ mode }),
    },
  );
}

export async function restoreSessionCheckpoint(
  sessionId: string,
  checkpointId: string,
  mode: RuntimeSessionCheckpointPreviewMode = "both",
): Promise<RuntimeSessionCheckpointRestoreResponse> {
  return fetchRuntimeJson<RuntimeSessionCheckpointRestoreResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/checkpoints/${encodeURIComponent(checkpointId)}/restore`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ mode }),
    },
  );
}

export async function listSessionTurns(
  sessionId: string,
): Promise<RuntimeSessionTurnsResponse> {
  return fetchRuntimeJson<RuntimeSessionTurnsResponse>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/turns`),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function previewSessionBacktrack(
  sessionId: string,
  request: RuntimeSessionBacktrackRequest,
): Promise<RuntimeSessionBacktrackResponse> {
  return fetchRuntimeJson<RuntimeSessionBacktrackResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/backtrack/preview`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        ...request,
        preview_only: true,
        auto_submit: false,
      }),
    },
  );
}

export async function applySessionBacktrack(
  sessionId: string,
  request: RuntimeSessionBacktrackRequest,
): Promise<RuntimeSessionBacktrackResponse> {
  return fetchRuntimeJson<RuntimeSessionBacktrackResponse>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/backtrack`),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        ...request,
        preview_only: false,
      }),
    },
  );
}

export async function listSessionBacktrackAudit(
  sessionId: string,
): Promise<RuntimeSessionBacktrackAuditResponse> {
  return fetchRuntimeJson<RuntimeSessionBacktrackAuditResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/backtrack/audit`,
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function getSessionPlanMode(
  sessionId: string,
): Promise<RuntimeSessionPlanMode> {
  return fetchRuntimeJson<RuntimeSessionPlanMode>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/plan`),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function updateSessionPlanMode(
  sessionId: string,
  body: RuntimeSessionPlanModeUpdateRequest,
): Promise<RuntimeSessionPlanMode> {
  return fetchRuntimeJson<RuntimeSessionPlanMode>(
    buildRuntimeUrl(`/api/runtime/sessions/${encodeURIComponent(sessionId)}/plan`),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    },
  );
}

/**
 * 读取会话当前权限模式与后端支持的模式清单。
 *
 * 模式真值（含 `accept_edits` / `bypass_permissions` 这类下划线式枚举）
 * 由后端 `internal/policy` 提供，前端不写死枚举，只按 value 渲染文案。
 */
export async function getSessionPermissionMode(
  sessionId: string,
): Promise<RuntimeSessionPermissionMode> {
  return fetchRuntimeJson<RuntimeSessionPermissionMode>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/permission-mode`,
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

/**
 * 切换会话权限模式；会话运行中同样立即生效。
 *
 * 未知模式由后端 400 拒绝（不会静默降级），调用方需把错误透出到 UI。
 */
export async function updateSessionPermissionMode(
  sessionId: string,
  body: RuntimeSessionPermissionModeUpdateRequest,
): Promise<RuntimeSessionPermissionMode> {
  return fetchRuntimeJson<RuntimeSessionPermissionMode>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/permission-mode`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    },
  );
}

export type ResolveSessionToolApprovalRequest = {
  /** `approval_requested` 事件里的 `request_id`（actor 侧 pending 审批主键）。 */
  requestId: string;
  allow: boolean;
  /** 可选：批准时替换工具参数（对齐 `approve_tool` 命令的 `patched_args`）。 */
  patchedArgs?: Record<string, unknown>;
};

/** 运行时命令（审批 / 提问）的公共调用选项。 */
export type SessionRuntimeCommandOptions = {
  /** 取消信号（P1-7）：中止投递后由调用方按 `cancelled` 收敛待交互条目。 */
  signal?: AbortSignal;
};

/**
 * 审批联动（P1-5 方案 4）：对指定会话的 pending 工具审批给出决定。
 *
 * 复用既有 `POST /runtime/sessions/{id}/runtime/commands` 的 `approve_tool`
 * 分支（`backend/internal/api/skills/session_runtime_handlers.go:770-784`），
 * 因此父会话自身的审批与子会话下钻的 inline 审批走同一条 actor 路径：
 * `actor.ApproveToolWithArgs` 会唤醒阻塞中的工具调用并落 `approval_resolved`。
 * 会话 ID 由调用方给定（前端下钻传子会话 ID），服务端不做父子改写。
 */
export async function resolveSessionToolApproval(
  sessionId: string,
  request: ResolveSessionToolApprovalRequest,
  options: SessionRuntimeCommandOptions = {},
): Promise<Record<string, unknown>> {
  return fetchRuntimeJson<Record<string, unknown>>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime/commands`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        type: "approve_tool",
        request_id: request.requestId,
        allow: request.allow,
        ...(request.patchedArgs ? { patched_args: request.patchedArgs } : {}),
      }),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
}

export type AnswerSessionQuestionRequest = {
  /** `question_asked` 事件里的 `question_id`（actor 侧 pending 提问主键）。 */
  questionId: string;
  /** 回答内容；允许空串（后端 `answer_question` 分支显式放行）。 */
  answer: string;
};

/**
 * 提问联动（P1-7）：对指定会话的 pending 提问提交回答。
 *
 * 复用既有 `POST /runtime/sessions/{id}/runtime/commands` 的 `answer_question`
 * 分支（`backend/internal/api/skills/session_runtime_handlers.go:785-795` →
 * `actor.AnswerQuestion`），唤醒阻塞中的提问并落 `question_answered`。
 */
export async function answerSessionQuestion(
  sessionId: string,
  request: AnswerSessionQuestionRequest,
  options: SessionRuntimeCommandOptions = {},
): Promise<Record<string, unknown>> {
  return fetchRuntimeJson<Record<string, unknown>>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime/commands`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        type: "answer_question",
        question_id: request.questionId,
        answer: request.answer,
      }),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
}
