// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。

/** 连接 runtime 的软超时：超过该时长仍未收到任何 SSE 事件即判定为连接失败。 */
export const RUNTIME_CONNECT_TIMEOUT_MS = 15_000;

/**
 * `/api/agent/chat` 读侧静默看门狗：连续该时长没有任何字节（注释帧也算）
 * 即判定本页的 chat 流已死。
 *
 * 为什么是 120s 而不是 `/runtime/stream` 的 45s：这条流**没有服务端 keepalive**
 * （只有 `/runtime/stream` 与 `/web/api/events` 每 15s 写一行 `: keepalive`），
 * 慢模型的首次 token 与长工具调用都可能是合法的长时间静默，阈值必须留足余量。
 * 命中后的代价可控：回合本身 detached（resume_on_disconnect），本地流死掉只
 * 影响本页接收，服务端照常执行、runtime 流按游标续传。
 *
 * 后续：给 chat 流补上 15s keepalive 后，这里可收紧到 45s 与另一条流对齐。
 */
export const CHAT_STREAM_IDLE_TIMEOUT_MS = 120_000;

export function shouldIgnoreTerminalStreamError(options: {
  finalized: boolean;
  aborted: boolean;
}) {
  return options.finalized || options.aborted;
}

/** 已物化会话不逐轮携带 workspace_path（绑定不漂移）；仅新草稿线程首轮携带身份级路径。 */
export function resolveChatTurnWorkspacePath(
  sessionId: string | null | undefined,
  identityWorkspacePath: string | undefined,
): string | undefined {
  if (sessionId && sessionId.trim() !== "") {
    return undefined;
  }
  return identityWorkspacePath?.trim() || undefined;
}
