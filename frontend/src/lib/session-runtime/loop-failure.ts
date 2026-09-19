// fire-and-forget 订阅循环的拒绝兜底（口径见 `entry.ts` 的 `startLoop`）。
//
// 循环体已把「网络失败 / 流被中止」收敛进快照（catch + abort 判定），但 dispose /
// 模式切换与 fetch/流 Promise 的竞态下，浏览器可能把被中止请求的拒绝直接挂到该
// Promise 上——live 实测会以 "Uncaught (in promise)"（栈指向 abort 调用点）泄漏到
// 控制台。这里只做泄漏兜底：仍在任的循环出现意外拒绝时收敛到可重试的失败态
// （被中止 / 过期的循环由调用方静默丢弃，不进这里）。
import type { SessionRuntimeEntrySnapshot } from "./types";

export function sessionRuntimeLoopFailurePatch(
  error: unknown,
): Partial<SessionRuntimeEntrySnapshot> {
  return {
    status: "offline",
    lastError:
      error instanceof Error && error.message.trim()
        ? error.message.trim()
        : "session runtime loop failed",
  };
}
