/**
 * 会话交互命令（审批 / 提问）：`POST /runtime/sessions/{id}/runtime/commands`
 * 的 `approve_tool` 与 `answer_question` 分支。
 *
 * P0-2 拆文件：`./sessions` 已接近 500 非空行上限（verify-max-lines），
 * 这里与 `./session-event-window` 同一模式——由 `./sessions` barrel re-export，
 * 保持 `@/api/runtime/sessions` 的既有调用方与 `vi.mock` 不变。
 */
import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

export type ResolveSessionToolApprovalRequest = {
  /** `approval_requested` 事件里的 `request_id`（actor 侧 pending 审批主键）。 */
  requestId: string;
  allow: boolean;
  /** 可选：批准时替换工具参数（对齐 `approve_tool` 命令的 `patched_args`）。 */
  patchedArgs?: Record<string, unknown>;
  /**
   * §4.8 审批记忆作用域：`session` 只记本会话、`project` 落
   * `<workspace>/.aicli/grants.json`（新会话仍生效）、`once` 不记。
   * 仅在 `allow` 为真时发送；拒绝永远不产生记忆。
   */
  rememberScope?: SessionApprovalRememberScope;
  /**
   * 可选自由文本说明。拒绝时随决策原因转达给模型（`user feedback: …`），
   * 批准时作为约束说明附在决策上。
   */
  feedback?: string;
};

/** 与后端 `policy.RememberScope*` 对齐；未知值后端按 400 拒绝。 */
export type SessionApprovalRememberScope = "once" | "session" | "project";

/** 运行时命令（审批 / 提问）的公共调用选项。 */
export type SessionRuntimeCommandOptions = {
  /** 取消信号（P1-7）：中止投递后由调用方按 `cancelled` 收敛待交互条目。 */
  signal?: AbortSignal;
};

/**
 * 审批联动（P1-5 方案 4）：对指定会话的 pending 工具审批给出决定。
 *
 * 复用既有 `POST /runtime/sessions/{id}/runtime/commands` 的 `approve_tool`
 * 分支（`backend/internal/api/runtimeapi/session_runtime_handlers.go`），
 * 因此父会话自身的审批与子会话下钻的 inline 审批走同一条 actor 路径：
 * `actor.ApproveToolWithDecision` 会唤醒阻塞中的工具调用并落 `approval_resolved`。
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
        // 拒绝不携带 remember_scope（后端也会忽略）：记忆只能来自批准。
        ...(request.allow && request.rememberScope && request.rememberScope !== "once"
          ? { remember_scope: request.rememberScope }
          : {}),
        ...(request.feedback?.trim() ? { feedback: request.feedback.trim() } : {}),
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

/** §4.13 审批解释：模型摘要（`model`）或规则模板降级（`rules`）。 */
export type SessionApprovalExplanation = {
  /** 解释正文（要点式纯文本；纯 UI 展示，不进入工具调用决策链）。 */
  explanation: string;
  /** `model` = 由当前会话模型摘要；`rules` = 未调用模型时的规则降级。 */
  source: "model" | "rules";
  /** 生成解释所用的模型名（`source=model` 时给出）。 */
  model?: string;
};

/**
 * §4.13 按需解释：对指定会话的 pending 审批做一次只读解释（不改变任何决定）。
 *
 * 服务端把该审批的工具名 + 完整参数 + 风险级别 + 准入原因交给会话模型做摘要；
 * 模型不可用时降级为规则模板解释。结果按 request_id 缓存于服务端。
 */
export async function explainSessionApproval(
  sessionId: string,
  requestId: string,
  options: SessionRuntimeCommandOptions = {},
): Promise<SessionApprovalExplanation> {
  return fetchRuntimeJson<SessionApprovalExplanation>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime/approvals/${encodeURIComponent(requestId)}/explain`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({}),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
}

/**
 * 提问联动（P1-7）：对指定会话的 pending 提问提交回答。
 *
 * 复用既有 `POST /runtime/sessions/{id}/runtime/commands` 的 `answer_question`
 * 分支（`session_runtime_handlers.go` 的 `actor.AnswerQuestion`），唤醒阻塞中
 * 的提问并落 `question_answered`。
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
