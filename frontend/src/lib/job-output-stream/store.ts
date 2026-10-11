// 逐任务实时输出流（P2）：把 runtime 事件流里的 `job_output` 增量
// （payload: job_id / offset / next_offset / size / chunk）汇入一个进程内订阅表，
// 供 jobs 面板在「展开的 live 任务」上做**事件驱动**的增量跟随。
//
// 设计取舍：本 store 只广播「有新数据」信号与最新 offset，**不在这里拼接文本**。
// 面板仍用权威 REST `/background/jobs/{id}/output`（offset 分页）读取字节，避免与
// 分页游标出现双份拼接；事件只用来决定「何时去读下一段」。
//
// 事件来源：workspace 的 SSE 通道经 `onRuntimeEvent` 注入（见 workspace-page）。

export type JobOutputSignal = {
  /** 事件报告的下一个输出 offset（用于判断是否有新数据）。 */
  nextOffset: number;
  /** 收到信号的时间戳（ms）。 */
  at: number;
};

const signals = new Map<string, JobOutputSignal>();
const jobListeners = new Map<string, Set<() => void>>();
const anyListeners = new Set<(jobId: string) => void>();

function notify(jobId: string): void {
  const listeners = jobListeners.get(jobId);
  if (listeners) {
    for (const listener of listeners) {
      listener();
    }
  }
  for (const listener of anyListeners) {
    listener(jobId);
  }
}

/**
 * ingestJobOutputEvent 处理一条 runtime 事件：仅 `job_output` 类型被接受。
 * 陈旧或重复（next_offset 不前进）的事件被忽略，避免同一段字节触发多次读取。
 */
export function ingestJobOutputEvent(event: unknown): void {
  if (!event || typeof event !== "object") {
    return;
  }
  const record = event as { type?: unknown; payload?: unknown };
  if (record.type !== "job_output") {
    return;
  }
  const payload = record.payload;
  if (!payload || typeof payload !== "object") {
    return;
  }
  const fields = payload as Record<string, unknown>;
  const jobId = typeof fields.job_id === "string" ? fields.job_id.trim() : "";
  if (!jobId) {
    return;
  }
  const nextOffset =
    typeof fields.next_offset === "number" && Number.isFinite(fields.next_offset)
      ? fields.next_offset
      : 0;
  const size =
    typeof fields.size === "number" && Number.isFinite(fields.size) ? fields.size : 0;
  if (nextOffset <= 0 && size <= 0) {
    return;
  }
  const existing = signals.get(jobId);
  if (existing && nextOffset <= existing.nextOffset) {
    return;
  }
  signals.set(jobId, { nextOffset, at: Date.now() });
  notify(jobId);
}

/** subscribeJobOutput 订阅某个任务的输出信号。 */
export function subscribeJobOutput(jobId: string, listener: () => void): () => void {
  let set = jobListeners.get(jobId);
  if (!set) {
    set = new Set();
    jobListeners.set(jobId, set);
  }
  set.add(listener);
  return () => {
    const current = jobListeners.get(jobId);
    if (!current) {
      return;
    }
    current.delete(listener);
    if (current.size === 0) {
      jobListeners.delete(jobId);
    }
  };
}

/** subscribeAnyJobOutput 订阅任意任务的输出信号（用于全局徽标/日志）。 */
export function subscribeAnyJobOutput(listener: (jobId: string) => void): () => void {
  anyListeners.add(listener);
  return () => {
    anyListeners.delete(listener);
  };
}

/** jobOutputSignal 读取某任务的最新输出信号（无则 undefined）。 */
export function jobOutputSignal(jobId: string): JobOutputSignal | undefined {
  return signals.get(jobId);
}

/** resetJobOutputStreams 清空状态（测试或会话切换时使用）。 */
export function resetJobOutputStreams(): void {
  signals.clear();
  jobListeners.clear();
  anyListeners.clear();
}
