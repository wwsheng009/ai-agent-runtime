export type RuntimeSessionPlanModeStatus =
  | "inactive"
  | "active"
  | "exited"
  | string;

export type RuntimeSessionPlanModeExitDecision =
  | ""
  | "approve"
  | "request_changes"
  | "quit"
  | string;

export type RuntimeSessionPlanModeAction =
  | "enter"
  | "exit"
  | "approve"
  | "request_changes"
  | "quit"
  | "on"
  | "off"
  | "status";

export type RuntimeSessionPlanMode = {
  session_id: string;
  active: boolean;
  status: RuntimeSessionPlanModeStatus;
  plan_path?: string;
  write_allow_paths?: string[];
  previous_mode?: string;
  permission_mode: string;
  pending_exit_request?: boolean;
  exit_decision?: RuntimeSessionPlanModeExitDecision;
  notes?: string;
  entered_at?: string;
  exited_at?: string;
  workspace_path?: string;
  plan_content: string;
  plan_content_available: boolean;
  plan_content_truncated?: boolean;
  plan_content_error?: string;
  action?: string;
};

export type RuntimeSessionPlanModeUpdateRequest = {
  action?: RuntimeSessionPlanModeAction | string;
  decision?: RuntimeSessionPlanModeExitDecision | string;
  plan_path?: string;
  notes?: string;
};

// --- 归档计划（planstore）阅读面：GET /api/runtime/plans 系列端点 -------------

/** 归档计划的评审状态；后端目前只有三种，未知值按原文透传（不写死枚举）。 */
export type RuntimeStoredPlanStatus =
  | "pending"
  | "approved"
  | "not_implemented"
  | string;

/** 一轮评审记录（approve / request_changes / quit / enter）。 */
export type RuntimeStoredPlanRound = {
  version: number;
  decision?: string;
  notes?: string;
  source?: string;
  /** store 根目录相对路径；正文由详情端点的 content 字段内联返回。 */
  snapshot?: string;
  created_at?: string;
};

/** 一条归档计划记录；详情端点额外返回最新快照正文（content*）。 */
export type RuntimeStoredPlan = {
  id: string;
  session_id?: string;
  project_slug?: string;
  project_path?: string;
  /** 兼容别名：部分宿主用 project 下发项目标识。 */
  project?: string;
  plan_path?: string;
  title?: string;
  status: RuntimeStoredPlanStatus;
  version: number;
  rounds?: RuntimeStoredPlanRound[];
  created_at?: string;
  updated_at?: string;
  content?: string;
  content_available?: boolean;
  content_truncated?: boolean;
  content_error?: string;
};

/** GET /api/runtime/plans 响应。 */
export type RuntimeStoredPlanListResponse = {
  plans: RuntimeStoredPlan[];
  count: number;
};
