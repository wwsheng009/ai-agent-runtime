// Git 面三个只读通道共用的请求纪律：**递增序号 + AbortController**。
//
// 为什么单独成文件：status / diff / commits 三条通道必须共享同一份竞态口径
// （切换文件、切 target、切空白开关、刷新都会中止在途请求；旧响应不得覆盖新选择），
// 各写一遍必然漂移。这里是纯机制层：不依赖 React，也不认识任何端点。

export type GitLoadStatus = "idle" | "loading" | "ready" | "error";

export type GitSnapshot<T> = {
  status: GitLoadStatus;
  data: T | null;
  error: unknown;
  /** 仓库缺失 / 端点不可用（不是「无改动」）。 */
  unavailable: boolean;
};

export function idleSnapshot<T>(): GitSnapshot<T> {
  return { status: "idle", data: null, error: null, unavailable: false };
}

/** 主动取消（AbortController / AbortSignal.timeout）与真实失败必须区分：前者不落错误态。 */
export function isAbortError(error: unknown): boolean {
  if (typeof DOMException !== "undefined" && error instanceof DOMException) {
    return error.name === "AbortError";
  }
  return error instanceof Error && error.name === "AbortError";
}

/**
 * 单个请求通道：序号 + AbortController。只有最新序号的响应能写回（旧响应直接丢弃）。
 * 抽成工厂是为了让 status/diff/commits 三条通道共享同一份竞态纪律，而不是各写一遍。
 */
export function createRequestChannel() {
  let seq = 0;
  let controller: AbortController | null = null;
  return {
    abort() {
      controller?.abort();
      controller = null;
    },
    /** 当前是否已有在途请求（写操作的「单飞」判据之一）。 */
    get inFlight() {
      return controller !== null;
    },
    run<T>(
      task: (signal: AbortSignal) => Promise<T>,
      onSuccess: (value: T) => void,
      onError: (error: unknown) => void,
    ) {
      seq += 1;
      const current = seq;
      controller?.abort();
      controller = new AbortController();
      const signal = controller.signal;
      void (async () => {
        try {
          const value = await task(signal);
          if (seq === current) {
            onSuccess(value);
          }
        } catch (caught) {
          if (seq === current && !isAbortError(caught)) {
            onError(caught);
          }
        } finally {
          if (seq === current) {
            controller = null;
          }
        }
      })();
      return current;
    },
    /** 失效当前通道（例如清空选择）：在途响应必须被丢弃。 */
    invalidate() {
      seq += 1;
      controller?.abort();
      controller = null;
    },
  };
}
