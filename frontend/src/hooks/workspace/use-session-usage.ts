// 会话用量数据 hook：按会话拉取 runtime.analytics.v1 用量，在用量相关运行时事件后
// 做一次防抖刷新，会话进行中再做低频兜底轮询，并提供手动 refresh。
// 消费方：右侧「会话用量」面板（session-usage-panel）与 composer 上下文控件。
// 数据只读，不写库；空 sessionId 直接置空，避免发出无意义的请求。
//
// 两条与「新建会话」直接相关的语义（见下方 state 派生）：
//   * 结果按 sessionId 打标签：切换会话时上一份用量立即失效，不会顶着上一个会话的
//     快照显示（新建会话后环上/面板残留旧 token 是本 hook 早前的真实缺陷）；
//   * 会话尚未在分析库落库（404 API_NOT_FOUND）按「还没有数据」处理，而不是读失败：
//     新会话在第一次带用量上报的 LLM 请求之前，分析库里必然没有它的行。

import { useCallback, useEffect, useRef, useState } from "react";

import { buildSessionUsageReloadKey } from "@/components/workspace/session-usage-panel-shared";
import { readAdminToken } from "@/lib/admin-token";
import {
  getAnalyticsSessionUsage,
  isRuntimeApiErrorCode,
  RuntimeApiError,
} from "@/lib/runtime-api";
import type { AnalyticsSessionUsageDetail } from "@/types/runtime";

// 会话用量在 LLM 请求结束后同步落库，事件到达后再等一拍，避免拿到落库前的旧值。
const runtimeEventRefreshDelayMs = 1000;

// 兜底低频轮询：事件流里的刷新事件只是「用量可能已变化」的代理信号
//（见 isUsageRelevantRuntimeEvent），且事件计数存在上限截断，因此本地会话
// 正在响应时按固定间隔重取，保证面板与后端落库数据最终一致。
const liveRefreshIntervalMs = 10_000;

/**
 * 「分析库里还没有这个会话」= 空态，不是读失败。
 *
 * 后端 usageanalytics 只在 IsNotFound 时返回 404
 * （backend/internal/api/runtimeapi/analytics_handlers.go），
 * 因此这里的 404 语义唯一：会话还没产生任何用量记录。
 */
function isSessionUsageMissing(error: unknown): boolean {
  if (isRuntimeApiErrorCode(error, "API_NOT_FOUND")) {
    return true;
  }
  return error instanceof RuntimeApiError && error.status === 404;
}

/** 用量结果连同「它属于哪个会话」一起存，供渲染期按 sid 过滤。 */
type SessionUsageState = {
  sessionId: string;
  detail: AnalyticsSessionUsageDetail | null;
  error: string | null;
  /** 会话在分析库中尚不存在（新建会话的正常状态）。 */
  missing: boolean;
};

const EMPTY_STATE: SessionUsageState = {
  sessionId: "",
  detail: null,
  error: null,
  missing: false,
};

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
  const [state, setState] = useState<SessionUsageState>(EMPTY_STATE);
  const [loading, setLoading] = useState(false);
  const [manualRefreshToken, setManualRefreshToken] = useState(0);
  const [eventRefreshKey, setEventRefreshKey] = useState("");
  const [liveRefreshToken, setLiveRefreshToken] = useState(0);
  const requestIdRef = useRef(0);
  const sid = sessionId?.trim() ?? "";
  const runtimeEventKey = buildSessionUsageReloadKey(
    lastRuntimeEventType,
    runtimeEventCount,
  );

  // 纯派生：state.sessionId 与当前 sid 不一致 = 这份结果属于上一个会话，直接丢弃。
  // 用 effect 清状态会让旧数据多闪一帧，因此按 sid 过滤而不是清空。
  const current = state.sessionId === sid;
  const usage = current ? state.detail : null;
  const error = current ? state.error : null;
  const sessionMissing = current && state.missing;

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
      // 作废在途请求：切到草稿会话后，旧响应回来不得再写回用量/错误。
      requestIdRef.current += 1;
      setLoading(false);
      return;
    }

    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;

    async function load() {
      setLoading(true);
      try {
        const detail = await getAnalyticsSessionUsage(sid, {
          adminToken: readAdminToken(),
        });
        if (requestIdRef.current === requestId) {
          setState({ sessionId: sid, detail, error: null, missing: false });
        }
      } catch (caught) {
        if (requestIdRef.current !== requestId) {
          return;
        }
        if (isSessionUsageMissing(caught)) {
          // 会话还没落库：当作「暂无数据」，不把空态渲染成一条报错。
          setState({ sessionId: sid, detail: null, error: null, missing: true });
          return;
        }
        setState({
          sessionId: sid,
          detail: null,
          error: caught instanceof Error ? caught.message : String(caught),
          missing: false,
        });
      } finally {
        if (requestIdRef.current === requestId) {
          setLoading(false);
        }
      }
    }

    void load();
  }, [sid, manualRefreshToken, eventRefreshKey, liveRefreshToken]);

  return { error, loading, refresh, sessionMissing, usage };
}
