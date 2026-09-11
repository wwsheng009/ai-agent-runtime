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
