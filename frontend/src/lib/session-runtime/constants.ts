// Batch 2（多会话并发运行时）：注册表相关的共享常量。
//
// 放在 lib 层是为了让 `lib/session-runtime/entry.ts`（headless 订阅引擎）与
// `hooks/workspace/session-stream-stall.ts`（前台适配层）共用同一个阈值，
// 避免"lib 反向依赖 hooks"。

/**
 * 读侧静默看门狗阈值：3 个 keepalive 周期（15s × 3）内零字节即判定本页流已死。
 *
 * 服务端在这条流上每 15s 写一行 `: keepalive`（`session_runtime_stream.go`），
 * 半开连接不会触发 fetch 错误，只有按字节计时的看门狗能把它变成可处理的错误。
 */
export const RUNTIME_STREAM_IDLE_TIMEOUT_MS = 45_000;

/** live 循环：正常收流（服务端/代理按空闲超时关闭长连接）后的重连间隔。 */
export const RUNTIME_STREAM_IDLE_RECONNECT_MS = 2_000;

/** live 循环：失败后的重连间隔（与既有 `use-session-runtime-stream` 同口径）。 */
export const RUNTIME_STREAM_FAILURE_RECONNECT_MS = 1_000;

/**
 * 连续失败达到该阈值才把 entry 标记为 offline（防瞬断抖动）。
 * 与 `use-session-runtime-stream` 的 STREAM_FAILURE_THRESHOLD 保持一致。
 */
export const RUNTIME_STREAM_FAILURE_THRESHOLD = 3;

/** `poll` 模式轮询周期：起点 3s，按 1.5 倍指数退避到 30s（§4.6）。 */
export const SESSION_RUNTIME_POLL_INITIAL_MS = 3_000;
export const SESSION_RUNTIME_POLL_MAX_MS = 30_000;
export const SESSION_RUNTIME_POLL_BACKOFF_FACTOR = 1.5;
