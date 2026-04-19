import { useEffect, useState } from "react";

import {
  listRuntimeTeams,
  listRuntimeTeamSummaries,
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/lib/runtime-api";

type RuntimeTeamsDataOptions = {
  limit?: number;
};

export function useRuntimeTeamsData({
  limit = 6,
}: RuntimeTeamsDataOptions = {}) {
  const [runtimeTeams, setRuntimeTeams] = useState<RuntimeTeamRecord[]>([]);
  const [runtimeTeamSummaries, setRuntimeTeamSummaries] = useState<
    RuntimeTeamSummaryEntry[]
  >([]);
  const [runtimeTeamsError, setRuntimeTeamsError] = useState<string | null>(null);
  const [runtimeTeamsLoading, setRuntimeTeamsLoading] = useState(true);
  const [runtimeTeamsRefreshing, setRuntimeTeamsRefreshing] = useState(false);

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      setRuntimeTeamsLoading(true);

      try {
        const [teamsResponse, summariesResponse] = await Promise.all([
          listRuntimeTeams({ limit }),
          listRuntimeTeamSummaries({ limit, light: true }),
        ]);

        if (cancelled) {
          return;
        }

        setRuntimeTeams(teamsResponse.teams);
        setRuntimeTeamSummaries(summariesResponse.teams);
        setRuntimeTeamsError(null);
      } catch (error) {
        if (cancelled) {
          return;
        }

        const message =
          error instanceof Error ? error.message : "failed to load runtime teams";
        setRuntimeTeamsError(message);
      } finally {
        if (!cancelled) {
          setRuntimeTeamsLoading(false);
          setRuntimeTeamsRefreshing(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [limit]);

  function refreshRuntimeTeams() {
    setRuntimeTeamsRefreshing(true);
    setRuntimeTeamsError(null);

    void Promise.all([
      listRuntimeTeams({ limit }),
      listRuntimeTeamSummaries({ limit, light: true }),
    ])
      .then(([teamsResponse, summariesResponse]) => {
        setRuntimeTeams(teamsResponse.teams);
        setRuntimeTeamSummaries(summariesResponse.teams);
      })
      .catch((error) => {
        const message =
          error instanceof Error ? error.message : "failed to refresh runtime teams";
        setRuntimeTeamsError(message);
      })
      .finally(() => {
        setRuntimeTeamsLoading(false);
        setRuntimeTeamsRefreshing(false);
      });
  }

  return {
    refreshRuntimeTeams,
    runtimeTeamSummaries,
    runtimeTeams,
    runtimeTeamsError,
    runtimeTeamsLoading,
    runtimeTeamsRefreshing,
  };
}
