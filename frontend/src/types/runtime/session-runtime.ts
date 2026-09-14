/**
 * P2-1A：会话运行时状态快照（`GET /api/runtime/sessions/{id}/runtime`）。
 *
 * 后端契约：
 * - 响应 `{ state, ...execution_route }`（`session_runtime_handlers.go:375-408`，
 *   `attachSessionExecutionRoute` 附加执行路由信息）；
 * - `state` 形状见 `pkg/skillsapi/client.go:1570-1585`（`SessionRuntimeState`）。
 *
 * 口径：`pending_approval.id` 即 `approval_requested.request_id`（`approve_tool`
 * 命令的主键），`pending_question.id` 即 `question_id`；两者是 P1-7 待交互注册表
 * 在「重载 / 重连」后的重建来源。
 */

export type RuntimeSessionApproval = {
  id: string;
  sessionId: string;
  toolName: string;
  reason: string;
  riskLevel: string;
  /** ISO 时间；后端 30min 超时终态的唯一依据。 */
  expiresAt?: string;
};

export type RuntimeSessionQuestion = {
  id: string;
  sessionId: string;
  prompt: string;
  required: boolean;
  suggestions: string[];
  expiresAt?: string;
};

export type RuntimeSessionState = {
  sessionId: string;
  /** 后端状态机原样透传（`running` / `waiting_approval` …），前端不做枚举收窄。 */
  status: string;
  currentTurnId?: string;
  currentCheckpointId?: string;
  /** 未决工具审批（无则 null）；与 `approval_requested` 事件同身份。 */
  pendingApproval: RuntimeSessionApproval | null;
  /** 未决提问（无则 null）；与 `question_asked` 事件同身份。 */
  pendingQuestion: RuntimeSessionQuestion | null;
  headOffset: number;
  /** 该会话当前活跃的后台任务 id（Jobs 面板同源）。 */
  activeJobIds: string[];
  updatedAt?: string;
};

export type RuntimeSessionSnapshot = {
  state: RuntimeSessionState;
  /** `attachSessionExecutionRoute` 的附加信息（原样透传，本批次未消费）。 */
  executionRoute?: Record<string, unknown>;
};
