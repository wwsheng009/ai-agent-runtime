import { useCallback, useRef, useState } from "react";

export type SessionRefreshOptions = {
  /** 本会话是否正在生成回复（按会话归属判定；后台会话的回合不影响本会话刷新）。 */
  responding: boolean;
  /** 权威历史重拉（成功即把降级收敛回在线）；无会话 / 无入口时缺省。 */
  recoverHistory?: () => Promise<boolean>;
  /** 会话列表投影刷新（侧栏标题 / 摘要 / 状态）。 */
  refreshRuntimeSessions?: () => void;
  /** 运行时状态快照重拉（重载 / 重连后重建未决审批与提问）。 */
  refreshRuntimeState?: () => void;
};

export type SessionRefreshController = {
  /** 刷新当前会话；在途期间重复调用直接忽略（按钮同时被禁用）。 */
  refreshSession: () => Promise<void>;
  /** 刷新在途标记：顶栏刷新按钮禁用并转圈。 */
  sessionRefreshing: boolean;
};

/**
 * 「刷新当前会话」控制器：一次点击重拉三份权威数据——运行时状态快照、会话列表投影、
 * 权威历史（消息列表 + 分页游标）。本会话正在生成回复时跳过历史重拉：服务端快照会覆盖
 * 在途的流式消息，等 turn 结束再看更诚实；其余两份与回合无关，照常刷新。
 *
 * 回调经 ref 取最新值（这些入口的引用会随渲染变化），因此 `refreshSession` 身份稳定，
 * 在途守卫也不必依赖闭包里的旧标记。
 */
export function useSessionRefresh({
  recoverHistory,
  refreshRuntimeSessions,
  refreshRuntimeState,
  responding,
}: SessionRefreshOptions): SessionRefreshController {
  const [sessionRefreshing, setSessionRefreshing] = useState(false);
  const inFlightRef = useRef(false);
  const latestRef = useRef({
    recoverHistory,
    refreshRuntimeSessions,
    refreshRuntimeState,
    responding,
  });
  latestRef.current = {
    recoverHistory,
    refreshRuntimeSessions,
    refreshRuntimeState,
    responding,
  };

  const refreshSession = useCallback(async () => {
    if (inFlightRef.current) {
      return;
    }
    inFlightRef.current = true;
    setSessionRefreshing(true);
    const latest = latestRef.current;
    try {
      latest.refreshRuntimeState?.();
      latest.refreshRuntimeSessions?.();
      if (!latest.responding) {
        await latest.recoverHistory?.();
      }
    } finally {
      inFlightRef.current = false;
      setSessionRefreshing(false);
    }
  }, []);

  return { refreshSession, sessionRefreshing };
}
