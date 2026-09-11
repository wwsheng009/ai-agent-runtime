// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。

/** 连接 runtime 的软超时：超过该时长仍未收到任何 SSE 事件即判定为连接失败。 */
export const RUNTIME_CONNECT_TIMEOUT_MS = 15_000;

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
