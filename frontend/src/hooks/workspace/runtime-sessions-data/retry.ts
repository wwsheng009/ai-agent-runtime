// 由 hooks/workspace/use-runtime-sessions-data.ts 机械拆分而来（P0-2），仅搬迁不改语义。

const runtimeSessionsRetryDelaysMs = [1200, 2500, 5000, 8000];

export function resolveRuntimeSessionsRetryDelay(attempt: number) {
  if (attempt <= 0) {
    return runtimeSessionsRetryDelaysMs[0];
  }

  return runtimeSessionsRetryDelaysMs[
    Math.min(attempt, runtimeSessionsRetryDelaysMs.length - 1)
  ];
}
