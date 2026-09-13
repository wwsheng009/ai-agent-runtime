// 工作台右侧「会话用量」面板的数据 hook：按会话拉取 runtime.analytics.v1 用量，
// 在用量相关运行时事件后做一次防抖刷新，会话进行中再做低频兜底轮询，
// 并提供手动 refresh。
// 数据只读，不写库；空 sessionId 直接置空，避免发出无意义的请求。

import { useCallback, useEffect, useRef, useState } from "react";

import { buildSessionUsageReloadKey } from "@/components/workspace/session-usage-panel-shared";
import { readAdminToken } from "@/lib/admin-token";
import { getAnalyticsSessionUsage } from "@/lib/runtime-api";
import type { AnalyticsSessionUsageDetail } from "@/types/runtime";

// 会话用量在 LLM 请求结束后同步落库，事件到达后再等一拍，避免拿到落库前的旧值。
const runtimeEventRefreshDelayMs = 1000;

// 兜底低频轮询：事件流里的刷新事件只是「用量可能已变化」的代理信号
//（见 isUsageRelevantRuntimeEvent），且事件计数存在上限截断，因此本地会话
// 正在响应时按固定间隔重取，保证面板与后端落库数据最终一致。
const liveRefreshIntervalMs = 10_000;

export type UseSessionUsageOptions = {
  sessionId: string;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  /** 会话正在响应（本地 turn 进行中）：开启低频兜底轮询。 */
  live?: boolean;
};

export function useSessionUsage({
  live = false,
  sessionId,
  lastRuntimeEventType,
  runtimeEventCount,
}: UseSessionUsageOptions) {
  const [usage, setUsage] = useState<AnalyticsSessionUsageDetail | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [manualRefreshToken, setManualRefreshToken] = useState(0);
  const [eventRefreshKey, setEventRefreshKey] = useState("");
  const [liveRefreshToken, setLiveRefreshToken] = useState(0);
  const requestIdRef = useRef(0);
  const sid = sessionId?.trim() ?? "";
  const runtimeEventKey = buildSessionUsageReloadKey(
    lastRuntimeEventType,
    runtimeEventCount,
  );

  useEffect(() => {
    if (!runtimeEventKey) {
      return;
    }

    const timer = setTimeout(() => {
      setEventRefreshKey(runtimeEventKey);
    }, runtimeEventRefreshDelayMs);
    return () => {
      clearTimeout(timer);
    };
  }, [runtimeEventKey]);

  const refresh = useCallback(() => {
    setManualRefreshToken((token) => token + 1);
  }, []);

  useEffect(() => {
    if (!live || !sid) {
      return;
    }

    const timer = setInterval(() => {
      setLiveRefreshToken((token) => token + 1);
    }, liveRefreshIntervalMs);
    return () => {
      clearInterval(timer);
    };
  }, [live, sid]);

  useEffect(() => {
    if (!sid) {
      setUsage(null);
      setError(null);
      setLoading(false);
      return;
    }

    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;

    async function load() {
      setLoading(true);
      setError(null);
      try {
        const detail = await getAnalyticsSessionUsage(sid, {
          adminToken: readAdminToken(),
        });
        if (requestIdRef.current === requestId) {
          setUsage(detail);
        }
      } catch (caught) {
        if (requestIdRef.current === requestId) {
          setError(caught instanceof Error ? caught.message : String(caught));
        }
      } finally {
        if (requestIdRef.current === requestId) {
          setLoading(false);
        }
      }
    }

    void load();
  }, [sid, manualRefreshToken, eventRefreshKey, liveRefreshToken]);

  return { error, loading, refresh, usage };
}
