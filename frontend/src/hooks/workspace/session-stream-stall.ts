// 读侧静默看门狗（会话运行时流）：把「连着但没数据」的半开连接变成可处理的错误。
//
// 服务端在这条流上每 15s 写一行 `: keepalive`（与业务事件无关，见 backend
// session_runtime_stream.go），因此 3 个 keepalive 周期内没有任何字节即可判定连接
// 已死：半开连接不会触发 fetch 错误、onOpen 也早已发生过，旧实现只能永久挂着——
// 表现为徽标在线、增量却再也不出现。
import { isSseIdleTimeoutError } from "@/api/runtime/sse";

/** 看门狗阈值：3 个 keepalive 周期（15s × 3）内零字节即判定本页流已死。 */
export const RUNTIME_STREAM_IDLE_TIMEOUT_MS = 45_000;

/**
 * 单次重连周期的静默状态机：`onOpen` 只代表响应头到位，不代表有数据。命中静默
 * 超时后必须等真正收到字节（onEvent / 显式 error 帧）才允许宣告在线，否则
 * 「重连 → 静默 → 重连」的循环会永远显示在线且永不降级。
 */
export function createStallGuard() {
  let stalled = false;
  return {
    /** 建连成功但上一轮死于静默：保持 reconnecting，等数据。 */
    get holdReconnecting() {
      return stalled;
    },
    markAlive() {
      stalled = false;
    },
    /** 归一化一次失败：静默超时给可读文案，其余交给调用方 fallback。 */
    noteFailure(
      error: unknown,
      fallback: (error: unknown, message: string) => string,
    ) {
      if (isSseIdleTimeoutError(error)) {
        stalled = true;
        const seconds = Math.round(error.idleTimeoutMs / 1000);
        return `静默超时：${seconds} 秒未收到任何数据（含 keepalive），已按游标重连`;
      }
      stalled = false;
      return fallback(error, "failed to connect runtime stream");
    },
  };
}
