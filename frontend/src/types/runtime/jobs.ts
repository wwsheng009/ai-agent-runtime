// P2-1A：后台任务（Jobs）运行时域类型。
//
// 数据源：`/api/runtime/background/jobs*`（backend/internal/background）。
// 注意：后端 `background.Job` 未加 json tag，序列化键为 Go 字段名（PascalCase）；
// 前端一律经 `api/runtime/jobs.ts` 的 normalize* 归一化后使用，不在组件里读原始键。

export type RuntimeJobStatus =
  | "pending"
  | "paused"
  | "running"
  | "completed"
  | "failed"
  | "timed_out"
  | "cancelled"
  | "orphaned"
  | "interrupted"
  | "expired"
  | "abandoned";

/** 未知状态值不在类型里新增枚举，归一化时回落 `pending` 并保留原始 status_text。 */
export type RuntimeJob = {
  id: string;
  sessionId: string;
  /** P3：调度该任务的 runtime 实例 id（旧后端可能缺省）。 */
  ownerInstanceId?: string;
  kind: string;
  command: string;
  cwd: string;
  priority: number;
  restartPolicy: string;
  status: RuntimeJobStatus;
  message: string;
  createdAt: string;
  /** P3：进入队列时间（旧后端可能缺省）。 */
  queuedAt?: string;
  /** P3：调度截止时间（旧后端可能缺省）。 */
  deadlineAt?: string;
  startedAt: string;
  finishedAt: string;
  exitCode: number | null;
  logPath: string;
  /** 交互会话（write_stdin）字段（2026-10-11）；旧后端/夹具可缺省。 */
  interactive?: boolean;
  tty?: boolean;
  cols?: number;
  rows?: number;
};

/** `POST .../requeue` 的响应：新任务 + 被重排队的旧任务 id。 */
export type RuntimeJobRequeueResponse = {
  job: RuntimeJob;
  /** 后端顶层字段 `requeued_from`，缺失时为空串。 */
  requeuedFrom: string;
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
  /** 交互会话字段（2026-10-11）：等待条件与进程资源指标。 */
  waitCondition: string;
  waitedMs: number;
  osPid: number | null;
  cpuPercent: number | null;
  rssKb: number | null;
};

/** `POST .../{id}/input` 请求：向交互会话写 stdin（2026-10-11）。 */
export type RuntimeJobInputRequest = {
  chars?: string;
  input?: string;
  yieldTimeMs?: number;
  closeStdin?: boolean;
};

/** 一次 write_stdin 交换的前端投影。 */
export type RuntimeJobExchange = {
  jobId: string;
  status: string;
  output: string;
  offset: number;
  nextOffset: number;
  waitCondition: string;
  waitedMs: number;
  exited: boolean;
  exitCode: number | null;
  message: string;
  osPid: number | null;
  cpuPercent: number | null;
  rssKb: number | null;
};

export type RuntimeJobInputResponse = {
  exchange: RuntimeJobExchange;
};

export type RuntimeJobResizeRequest = {
  cols: number;
  rows: number;
};

export type RuntimeJobResizeResponse = {
  resized: boolean;
  cols: number;
  rows: number;
};

export type RuntimeTerminalsCleanResponse = {
  closed: number;
};
