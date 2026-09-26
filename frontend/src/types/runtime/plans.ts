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
  /** 触发轮结果（§4.4 自动修订回合）：true 表示已起一轮修订。 */
  revision_triggered?: boolean;
  /** 触发失败的说明；决策已落地，评审意见仍在下一轮输入时交付。 */
  revision_error?: string;
};

export type RuntimeSessionPlanModeUpdateRequest = {
  action?: RuntimeSessionPlanModeAction | string;
  decision?: RuntimeSessionPlanModeExitDecision | string;
  plan_path?: string;
  notes?: string;
  /**
   * 仅与 `request_changes` 搭配：请求后端在裁决落地后立刻起一轮修订
   * （要求 notes 非空），实现「一次提交即一轮修订」。
   */
  trigger_revision?: boolean;
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

/** 轮次对比（GET /api/runtime/plans/{id}/diff）的查询参数；0/缺省由后端推导。 */
export type RuntimePlanDiffOptions = {
  /** 起始轮次；0/未给 = 上一轮。 */
  from?: number;
  /** 目标轮次；0/未给 = 最新轮。 */
  to?: number;
  /** 上下文行数（0..10，0 用后端默认 3）。 */
  context?: number;
  /** 渲染行数上限（0..2000，0 用后端默认 400）。 */
  maxLines?: number;
};

/**
 * 一次轮次对比结果。`text` 是统一 diff 文本（带 `--- v1 …` / `+++ v3 …` 框架行）；
 * `identical=true` 时正文只剩框架行，判等应以该字段为准。
 */
export type RuntimePlanDiffResult = {
  plan_id: string;
  from_version: number;
  to_version: number;
  identical: boolean;
  added: number;
  removed: number;
  old_lines: number;
  new_lines: number;
  /** 变更过大、退化成整块删除+插入（不做行级匹配）。 */
  coarse: boolean;
  /** 渲染行数触顶，`text` 被截断。 */
  truncated: boolean;
  text: string;
};

/** 一行 diff 的语义分类（供渲染上色；`text` 保留原始行，含标记符）。 */
export type RuntimePlanDiffLineKind = "context" | "add" | "del" | "hunk" | "meta";

export type RuntimePlanDiffLine = {
  kind: RuntimePlanDiffLineKind;
  text: string;
};
