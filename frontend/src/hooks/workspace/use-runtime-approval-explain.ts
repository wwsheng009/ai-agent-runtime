import { useCallback, useEffect, useRef, useState } from "react";

import { getRuntimeApprovalExplainSettings } from "@/lib/runtime-api";
import type { RuntimeApprovalExplainMode } from "@/types/runtime/config";

export type RuntimeApprovalExplainSnapshot = {
  /** 后端当前生效的解释模式。 */
  mode: RuntimeApprovalExplainMode;
  /** 后端下发的可选模式清单（前端只渲染，不写死枚举）。 */
  supportedModes: RuntimeApprovalExplainMode[];
};

export type RuntimeApprovalExplainController = {
  /** 读取失败时的可读原因；成功时为 null。 */
  error: string | null;
  loading: boolean;
  /** 尚未读到（或读取失败）时为 null。 */
  snapshot: RuntimeApprovalExplainSnapshot | null;
  refresh: () => Promise<void>;
};

/**
 * 「审批解释模式」读取器：GET `/api/runtime/config/approval-explain`。
 *
 * 该值是**进程级临时开关**（off / on_demand / pre_generate），决定审批「解释」
 * 按钮是否/何时真的调用模型：改动对后续解释立即生效，但不落盘，runtime 重启后
 * 回到 `AICLI_APPROVAL_EXPLAIN_MODE` 或默认值。
 *
 * 只负责读取：保存由调用方走 `saveRuntimeApprovalExplainSettings`，成功后调
 * `refresh()` 重新取权威值（PUT 响应里的 supported_modes 与 GET 同源，但以读回
 * 的当前值为准更稳）。
 */
export function useRuntimeApprovalExplainSettings(): RuntimeApprovalExplainController {
  const [snapshot, setSnapshot] =
    useState<RuntimeApprovalExplainSnapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const mountedRef = useRef(true);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const response = await getRuntimeApprovalExplainSettings();
      if (!mountedRef.current) {
        return;
      }
      setSnapshot({
        mode: response.mode,
        supportedModes: response.supported_modes ?? [],
      });
      setError(null);
    } catch (fetchError) {
      if (!mountedRef.current) {
        return;
      }
      setSnapshot(null);
      setError(
        fetchError instanceof Error ? fetchError.message : String(fetchError),
      );
    } finally {
      if (mountedRef.current) {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    void refresh();
    return () => {
      mountedRef.current = false;
    };
  }, [refresh]);

  return { error, loading, refresh, snapshot };
}
