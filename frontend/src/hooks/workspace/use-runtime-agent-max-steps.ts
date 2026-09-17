import { useCallback, useEffect, useRef, useState } from "react";

import { getRuntimeAgentMaxSteps } from "@/lib/runtime-api";
import type { RuntimeConfigLayer } from "@/types/runtime/config";

export type RuntimeAgentMaxStepsSnapshot = {
  /** 服务端缺省的落盘位置（runtime 配置文件）。 */
  configFile: string;
  /** 后端接受的取值上限。 */
  limit: number;
  /** 服务端缺省值：请求未携带 max_steps 时生效，0 = 不限制。 */
  maxSteps: number;
  /** runtime.yaml 的层栈（分层）：候选文件、只读层与写入目标。 */
  layers: RuntimeConfigLayer[];
};

export type RuntimeAgentMaxStepsController = {
  /** 读取失败时的可读原因；成功时为 null。 */
  error: string | null;
  loading: boolean;
  /** 尚未读到（或读取失败）时为 null。 */
  snapshot: RuntimeAgentMaxStepsSnapshot | null;
  refresh: () => Promise<void>;
};

/**
 * 「服务端缺省最大步骤数」读取器：GET `/api/runtime/config/agent/max-steps`。
 *
 * 该值的作用域是**服务端缺省**（请求未携带 max_steps 时生效）；工作区设置里的
 * maxSteps 属于**每轮请求值**，会覆盖它。两处共用同一个后端键（runtime 配置
 * 的 agent.maxSteps），因此页面展示必须区分这两个作用域。
 *
 * 只负责读取：保存由调用方走 `saveRuntimeAgentMaxSteps`，成功后调 `refresh()`
 * 重新取权威值（PUT 响应不带 limit，本地拼装容易与后端常量漂移）。
 */
export function useRuntimeAgentMaxSteps(): RuntimeAgentMaxStepsController {
  const [snapshot, setSnapshot] = useState<RuntimeAgentMaxStepsSnapshot | null>(
    null,
  );
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const mountedRef = useRef(true);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const response = await getRuntimeAgentMaxSteps();
      if (!mountedRef.current) {
        return;
      }
      setSnapshot({
        configFile: response.config_file,
        limit: response.limit,
        maxSteps: response.max_steps,
        layers: response.layers ?? [],
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
