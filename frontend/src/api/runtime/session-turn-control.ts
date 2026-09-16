import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

/**
 * 建议 3（后端 cancel 契约）：回合停止的服务端一侧。
 *
 * 为什么需要它：`resume_on_disconnect` 生效后，abort 本地 `POST /api/agent/chat`
 * 只让 UI 立即收尾，服务端 detached 回合仍在跑（刷新续传就是为了让它跑完）；
 * 用户点「停止」时必须把 `interrupt` 命令真的投给服务端，否则停止按钮是假的。
 * 后端优先取消进程内在途回合（activeTurnRegistry），否则回退 durable actor，
 * 并如实回报取消作用在哪条通道、哪个回合上。
 */

/** 取消实际作用的执行通道（后端 `channel` 字段）。 */
export type SessionTurnInterruptChannel = "active_turn" | "session_actor";

/** 取消结果原因（后端 `reason` 字段，见 sessionTurnInterruptPayload）。 */
export type SessionTurnInterruptReason =
  | "cancelled"
  | "already_cancelled"
  | "no_active_turn"
  | "not_cancelable";

/** 被取消回合的身份（无在途回合时整组缺省，不伪造身份）。 */
export type SessionTurnInterruptTurn = {
  session_id?: string;
  turn_id?: string;
  source?: string;
  detached?: boolean;
  started_at?: string;
  cancel_source?: string;
};

export type SessionTurnInterruptResponse = {
  ok: boolean;
  /**
   * 本次调用是否真的触发了取消。重复 stop → false + `already_cancelled`
   * （同一回合已取消，仍属成功语义，不该报错）。
   */
  cancelled: boolean;
  reason: SessionTurnInterruptReason;
  channel: SessionTurnInterruptChannel;
  turn_id?: string;
  turn?: SessionTurnInterruptTurn;
  /** 取消来源（用户主动停止 = `user_interrupt`）。 */
  cancel_source?: string;
};

export type InterruptSessionTurnOptions = {
  /**
   * 在途回合身份。与当前在途回合不一致时后端回 409 `turn_mismatch`
   * （迟到的 stop 不得误伤新回合）；空值表示「取消当前在途回合」。
   */
  turnId?: string;
  signal?: AbortSignal;
};

/** 投递 `interrupt` 命令；非 2xx（含 409 turn_mismatch）会按 `fetchRuntimeJson` 约定抛错。 */
export async function interruptSessionTurn(
  sessionId: string,
  options: InterruptSessionTurnOptions = {},
): Promise<SessionTurnInterruptResponse> {
  const turnId = options.turnId?.trim();
  return fetchRuntimeJson<SessionTurnInterruptResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime/commands`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        type: "interrupt",
        ...(turnId ? { turn_id: turnId } : {}),
      }),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
}

/**
 * 停止按钮用的 best-effort 封装。
 *
 * 本地 abort 已经让 UI 收尾，服务端停止失败（网络抖动 / 409 回合已换代 /
 * 会话无在途回合）不该把「停止」本身变成错误弹窗——因此这里吞掉异常，
 * 只用返回值区分「投递成功」与「未投递或失败」（null）。
 */
export async function requestSessionTurnInterrupt(
  sessionId: string | null | undefined,
  turnId?: string | null,
): Promise<SessionTurnInterruptResponse | null> {
  const normalizedSessionId = (sessionId ?? "").trim();
  if (!normalizedSessionId) {
    // 草稿会话（还没落库）没有服务端回合可停，连请求都不发。
    return null;
  }
  try {
    return await interruptSessionTurn(normalizedSessionId, {
      ...(turnId?.trim() ? { turnId } : {}),
    });
  } catch {
    return null;
  }
}
