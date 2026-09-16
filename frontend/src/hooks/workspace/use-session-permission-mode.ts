import { useCallback, useEffect, useState } from "react";

import {
  getSessionPermissionMode,
  updateSessionPlanMode,
  updateSessionPermissionMode,
  type RuntimePermissionMode,
  type RuntimePermissionModeOption,
  type RuntimeSessionPermissionMode,
} from "@/lib/runtime-api";
import { buildRuntimeEventReloadKey } from "@/hooks/workspace/use-runtime-checkpoints";

// P1-x 会话权限模式（composer 权限选择器）：真值由本 hook 持有，
// 组件只做渲染 + 回调。模式清单来自后端 `supported_modes`，前端不写死枚举。
type UseSessionPermissionModeOptions = {
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  sessionId?: string;
};

type ShouldReloadPermissionModeOptions = {
  lastHandledEventKey?: string;
  lastRuntimeEventKey?: string;
  lastRuntimeEventType?: string;
  loadedPermissionSessionId: string;
  sessionId?: string;
};

// 计划模式进出会改写会话权限模式，因此与 plan 面板共享同一批重载信号。
const PERMISSION_MODE_RELOAD_EVENTS = new Set([
  "tool.completed",
  "tool_completed",
  "permission_mode_changed",
  "plan_mode_changed",
  "plan_updated",
  "session_updated",
]);

export function shouldReloadSessionPermissionMode({
  lastHandledEventKey,
  lastRuntimeEventKey,
  lastRuntimeEventType,
  loadedPermissionSessionId,
  sessionId,
}: ShouldReloadPermissionModeOptions) {
  if (!sessionId) {
    return false;
  }

  // 切会话必然重载一次；加载成功后即使载荷为空也停止循环。
  if (loadedPermissionSessionId !== sessionId) {
    return true;
  }

  if (!lastRuntimeEventType || !lastRuntimeEventKey) {
    return false;
  }

  if (!PERMISSION_MODE_RELOAD_EVENTS.has(lastRuntimeEventType)) {
    return false;
  }

  return lastRuntimeEventKey !== lastHandledEventKey;
}

export function useSessionPermissionMode({
  lastRuntimeEventType,
  runtimeEventCount,
  sessionId,
}: UseSessionPermissionModeOptions) {
  const [state, setState] = useState<RuntimeSessionPermissionMode | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [pending, setPending] = useState(false);
  const [loadedSessionId, setLoadedSessionId] = useState("");
  const [lastHandledEventKey, setLastHandledEventKey] = useState("");
  const lastRuntimeEventKey = buildRuntimeEventReloadKey(
    lastRuntimeEventType,
    runtimeEventCount,
  );

  useEffect(() => {
    if (
      !shouldReloadSessionPermissionMode({
        lastHandledEventKey,
        lastRuntimeEventKey,
        lastRuntimeEventType,
        loadedPermissionSessionId: loadedSessionId,
        sessionId,
      })
    ) {
      return;
    }

    if (!sessionId) {
      setState(null);
      setError(null);
      setLoadedSessionId("");
      setLastHandledEventKey("");
      return;
    }

    let cancelled = false;
    setLoading(true);
    setError(null);

    void (async () => {
      try {
        const response = await getSessionPermissionMode(sessionId);
        if (cancelled) {
          return;
        }
        setState(response);
      } catch (loadError) {
        if (cancelled) {
          return;
        }
        setState(null);
        setError(
          loadError instanceof Error
            ? loadError.message
            : "failed to load permission mode",
        );
      } finally {
        if (!cancelled) {
          setLoading(false);
          // 失败也标记为已加载，避免事件流反复打同一个接口。
          setLoadedSessionId(sessionId);
          setLastHandledEventKey(lastRuntimeEventKey);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [
    lastHandledEventKey,
    lastRuntimeEventKey,
    lastRuntimeEventType,
    loadedSessionId,
    sessionId,
  ]);

  const setPermissionMode = useCallback(
    async (mode: RuntimePermissionMode) => {
      if (!sessionId) {
        return;
      }

      setPending(true);
      setError(null);

      try {
        if (mode === "plan") {
          // plan 拥有独立持久生命周期（plan_path / previous_mode），
          // 因此走 plan 入口而不是权限模式端点；随后回读模式快照。
          await updateSessionPlanMode(sessionId, { action: "enter" });
          const response = await getSessionPermissionMode(sessionId);
          setState(response);
        } else {
          const response = await updateSessionPermissionMode(sessionId, { mode });
          setState(response);
        }
        setLoadedSessionId(sessionId);
        setLastHandledEventKey(lastRuntimeEventKey);
      } catch (updateError) {
        setError(
          updateError instanceof Error
            ? updateError.message
            : "failed to update permission mode",
        );
      } finally {
        setPending(false);
      }
    },
    [lastRuntimeEventKey, sessionId],
  );

  const options: RuntimePermissionModeOption[] = state?.supported_modes ?? [];

  return {
    error,
    loading,
    mode: state?.mode ?? "",
    options,
    pending,
    planActive: Boolean(state?.plan_active),
    previousMode: state?.previous_mode ?? "",
    sessionPermissionMode: state,
    setPermissionMode,
  };
}
