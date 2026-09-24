// Batch 12：composer `/profile` 的运行时目录（宿主数据源）。
//
// 边界：只拉取清单端点（`GET /api/runtime/profiles`）一次，把目录与加载 / 失败态
// 原样交给命令面；不全局缓存、不轮询、不推断能力（`session_switch` 能力广告
// 由后端给出，R20）。
//
// 失败语义：保留错误文案并让 `catalog=null` —— 命令面据此进入「目录未就绪」
// 空态（不产生候选），不把网络故障伪装成「没有 profile」，也不注册注定失败的命令。

import { useCallback, useEffect, useState } from "react";

import { listRuntimeProfiles } from "@/api/runtime/profiles";
import { createLogger } from "@/core/logger";
import type { RuntimeProfileListResponse } from "@/types/runtime";

const logger = createLogger("use-runtime-profile-catalog");

export type RuntimeProfileCatalogState = {
  /** 目录；null = 未就绪（加载中 / 失败）。 */
  catalog: RuntimeProfileListResponse | null;
  /** 失败原因（成功或未加载时为 null）。 */
  error: string | null;
  loading: boolean;
  /** 重新拉取（切换后目录可能变化：如新建 profile）。 */
  refresh: () => void;
};

export function useRuntimeProfileCatalog(enabled = true): RuntimeProfileCatalogState {
  const [catalog, setCatalog] = useState<RuntimeProfileListResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [reloadToken, setReloadToken] = useState(0);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    let cancelled = false;
    setLoading(true);
    void (async () => {
      try {
        const result = await listRuntimeProfiles();
        if (cancelled) {
          return;
        }
        setCatalog(result);
        setError(null);
      } catch (cause) {
        if (cancelled) {
          return;
        }
        logger.warn("runtime profile catalog unavailable", { error: cause });
        setError(
          cause instanceof Error && cause.message.trim().length > 0
            ? cause.message
            : String(cause),
        );
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [enabled, reloadToken]);

  const refresh = useCallback(() => setReloadToken((token) => token + 1), []);

  return { catalog, error, loading, refresh };
}
