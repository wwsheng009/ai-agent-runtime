// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。

/**
 * 连接 runtime 的软超时：超过该时长仍未从 socket 读到**任何字节**（含
 * `: open`/`: keepalive` 注释帧，与读侧静默看门狗同口径）即判定连接失败。
 *
 * 2026-09-28：服务端已把 SSE 开流前移到请求入口——响应头 + `: open` 首帧
 * 先于会话获取/租约排队等不可控步骤出站，其后静默期每 15s 补 `: keepalive`。
 * 因此本预算量的是「首字节延迟」而不是「服务端前置工作耗时」，与
 * `/runtime/stream` 对齐到 45s：只有真正连不上/被吞掉的请求才会命中。
 */
export const RUNTIME_CONNECT_TIMEOUT_MS = 45_000;

/**
 * `/api/agent/chat` 读侧静默看门狗：连续该时长没有任何字节（注释帧也算）
 * 即判定本页的 chat 流已死。
 *
 * 2026-09-28：服务端已补上 15s `: keepalive`（开流即首帧，静默期每 15s
 * 一条注释帧），因此从 120s 收紧到 45s，与 `/runtime/stream` 对齐。
 * 命中后的代价可控：回合本身 detached（resume_on_disconnect），本地流死掉只
 * 影响本页接收，服务端照常执行、runtime 流按游标续传。
 */
export const CHAT_STREAM_IDLE_TIMEOUT_MS = 45_000;

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
