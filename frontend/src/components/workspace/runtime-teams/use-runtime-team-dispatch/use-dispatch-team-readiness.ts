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
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setDispatchTeamReadiness({});
      setIsDispatchReadinessLoading(false);
      return;
    }

    let cancelled = false;
    setIsDispatchReadinessLoading(true);

    void loadDispatchTeamReadiness(teams)
      .then((nextMap) => {
        if (cancelled) {
          return;
        }
        setDispatchTeamReadiness(nextMap);
      })
      .finally(() => {
        if (!cancelled) {
          setIsDispatchReadinessLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [teams]);

  return { dispatchTeamReadiness, isDispatchReadinessLoading };
}
