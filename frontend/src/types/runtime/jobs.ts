// P2-1A：后台任务（Jobs）运行时域类型。
//
// 数据源：`/api/runtime/background/jobs*`（backend/internal/background）。
// 注意：后端 `background.Job` 未加 json tag，序列化键为 Go 字段名（PascalCase）；
// 前端一律经 `api/runtime/jobs.ts` 的 normalize* 归一化后使用，不在组件里读原始键。

export type RuntimeJobStatus =
  | "pending"
  | "running"
  | "completed"
  | "failed"
  | "timed_out"
  | "cancelled"
  | "orphaned";

/** 未知状态值不在类型里新增枚举，归一化时回落 `pending` 并保留原始 status_text。 */
export type RuntimeJob = {
  id: string;
  sessionId: string;
  kind: string;
  command: string;
  cwd: string;
  priority: number;
  restartPolicy: string;
  status: RuntimeJobStatus;
  message: string;
  createdAt: string;
  startedAt: string;
  finishedAt: string;
  exitCode: number | null;
  logPath: string;
};

export type RuntimeJobListQuery = {
  sessionId?: string;
  /** 逗号分隔的状态过滤（`pending,running,...`），与后端 `status` 参数同义。 */
  status?: string;
  limit?: number;
  offset?: number;
};

export type RuntimeJobListResponse = {
  jobs: RuntimeJob[];
  count: number;
};

export type RuntimeJobEvent = {
  seq: number;
  jobId: string;
  type: string;
  payload: Record<string, unknown> | null;
  createdAt: string;
};

export type RuntimeJobEventsQuery = {
  after?: number;
  limit?: number;
};

export type RuntimeJobEventsResponse = {
  events: RuntimeJobEvent[];
  count: number;
};

export type RuntimeJobOutputQuery = {
  offset?: number;
  limit?: number;
};

/** `TaskOutputResult` 的前端投影（只取面板需要的字段）。 */
export type RuntimeJobOutput = {
  jobId: string;
  status: string;
  output: string;
  nextOffset: number;
  exitCode: number | null;
  message: string;
  errorCode: string;
};
