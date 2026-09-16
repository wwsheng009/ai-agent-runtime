// 会话权限模式（composer 权限选择器）。
//
// P1-x：模式枚举与后端 `internal/policy` 的 runtimepolicy.Mode 对齐
// （canonical 值为下划线式：accept_edits / bypass_permissions），
// 但「可选清单/展示文案」始终取后端 `supported_modes`，前端不写死列表，
// 避免权限语义在两侧漂移（拼错的值后端 400 拒绝，不会静默降级）。
export type RuntimePermissionMode =
  | "default"
  | "accept_edits"
  | "plan"
  | "bypass_permissions"
  | string;

export type RuntimePermissionModeOption = {
  value: string;
  label: string;
  description?: string;
  /** 危险模式（跳权）在 UI 上需要显式提示。 */
  dangerous?: boolean;
  /** 需要走 plan 模式入口（携带 plan_path / previous_mode 记录）。 */
  requires_plan_entry?: boolean;
};

export type RuntimeSessionPermissionMode = {
  session_id: string;
  mode: RuntimePermissionMode;
  requested_mode?: string;
  previous_mode?: string;
  plan_active?: boolean;
  plan_status?: string;
  /** 本次响应是否由一次切换动作产生。 */
  updated?: boolean;
  supported_modes: RuntimePermissionModeOption[];
};

export type RuntimeSessionPermissionModeUpdateRequest = {
  mode: RuntimePermissionMode;
};
