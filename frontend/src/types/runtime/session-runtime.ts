/**
 * P2-1A：会话运行时状态快照（`GET /api/runtime/sessions/{id}/runtime`）。
 *
 * 后端契约：
 * - 响应 `{ state, ...execution_route }`（`session_runtime_handlers.go:380-447`，
 *   `attachSessionExecutionRoute` 附加执行路由信息）；
 * - `{ session_id, state: null }` = 会话存在、但从未进入 durable session actor
 *   （例如只经无状态 `/api/agent/chat` 的 web 会话）：正常空态，客户端归一化为
 *   `null` 快照，不虚构 `RuntimeSessionState`；
 * - 两个分支都附带 `active_turn`（P4-刷新续传，`session_active_turn.go`）：
 *   本进程此刻在该会话上的在途回合，无则显式 `null`；
 * - 会话不存在 → 404 `SESSION_NOT_FOUND`；存储故障 → 503/504 `STORE_*`；
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

/**
 * P4-刷新续传：本进程此刻在该会话上执行的在途回合（`/runtime` 的 `active_turn`）。
 *
 * 页面刷新会 abort 在途的 `/api/agent/chat` POST，服务端回合却继续执行
 * （`resume_on_disconnect`）。刷新后的新页面只能从快照得知「还有回合在跑、
 * 它叫什么」。据此重新挂载回合身份后，`/runtime/stream` 上落库的增量帧才会
 * 继续渲染到同一条 streaming 消息（`renderLiveDeltas` 门控）。
 */
export type RuntimeSessionActiveTurn = {
  sessionId: string;
  turnId: string;
  /** 回合来源（`agent_chat_stream` = web 直连 `/api/agent/chat`）。 */
  source: string;
  /** true = 客户端断开后仍继续执行（刷新可续传）。 */
  detached: boolean;
  /** ISO 时间：回合开始时刻。 */
  startedAt?: string;
};

export type RuntimeSessionSnapshot = {
  /**
   * durable runtime state；`null` = 会话存在但从未进入 durable session actor
   * （web 直连会话的常态）。此时 `activeTurn` 仍可能非空——那正是刷新续传
   * 依赖的在途回合信号，因此不能把「state 为空」等同于「整份快照为空」。
   */
  state: RuntimeSessionState | null;
  /** P4-刷新续传：本会话此刻的在途回合，无则 null。 */
  activeTurn: RuntimeSessionActiveTurn | null;
  /** `attachSessionExecutionRoute` 的附加信息（原样透传，本批次未消费）。 */
  executionRoute?: Record<string, unknown>;
};
