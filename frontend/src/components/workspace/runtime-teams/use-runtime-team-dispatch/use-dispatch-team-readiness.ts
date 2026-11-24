// 由 components/workspace/runtime-teams/use-runtime-team-dispatch.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect, useState } from "react";

import type { RuntimeTeamRecord } from "@/lib/runtime-api";
import type { DispatchTeamReadiness } from "@/components/workspace/runtime-teams/shared";

import { loadDispatchTeamReadiness } from "./dispatch-monitor";

/** 团队可执行性就绪度：teams 变化时重算 readiness。 */
export function useDispatchTeamReadiness(teams: RuntimeTeamRecord[]) {
  const [dispatchTeamReadiness, setDispatchTeamReadiness] = useState<
    Record<string, DispatchTeamReadiness>
  >({});
  const [isDispatchReadinessLoading, setIsDispatchReadinessLoading] = useState(false);

  useEffect(() => {
    if (teams.length === 0) {
      // P0-2 机械搬迁：保留原「teams 清空即同步复位 readiness 与 loading」语义。

      setDispatchTeamReadiness({});
      setIsDispatchReadinessLoading(false);
      return;
    }

    let cancelled = false;
    const controller = new AbortController();
    setDispatchTeamReadiness({});
    setIsDispatchReadinessLoading(true);

    void loadDispatchTeamReadiness(teams, controller.signal)
      .then((nextMap) => {
        if (cancelled || controller.signal.aborted) {
          return;
        }
        setDispatchTeamReadiness(nextMap);
      })
      .finally(() => {
        if (!cancelled && !controller.signal.aborted) {
          setIsDispatchReadinessLoading(false);
        }
      });

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [teams]);

  return { dispatchTeamReadiness, isDispatchReadinessLoading };
}
