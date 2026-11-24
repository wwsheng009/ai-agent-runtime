import { useEffect, useRef, useState } from "react";

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
  const requestControllerRef = useRef<AbortController | null>(null);

  useEffect(() => {
    let cancelled = false;
    requestControllerRef.current?.abort();
    const controller = new AbortController();
    requestControllerRef.current = controller;

    void (async () => {
      setRuntimeTeamsLoading(true);

      try {
        const [teamsResponse, summariesResponse] = await Promise.all([
          listRuntimeTeams({ limit, signal: controller.signal }),
          listRuntimeTeamSummaries({
            limit,
            light: true,
            signal: controller.signal,
          }),
        ]);

        if (cancelled || controller.signal.aborted) {
          return;
        }

        setRuntimeTeams(teamsResponse.teams);
        setRuntimeTeamSummaries(summariesResponse.teams);
        setRuntimeTeamsError(null);
      } catch (error) {
        if (cancelled || controller.signal.aborted) {
          return;
        }

        const message =
          error instanceof Error ? error.message : "failed to load runtime teams";
        setRuntimeTeamsError(message);
      } finally {
        if (
          !cancelled &&
          !controller.signal.aborted &&
          requestControllerRef.current === controller
        ) {
          setRuntimeTeamsLoading(false);
          setRuntimeTeamsRefreshing(false);
        }
      }
    })();

    return () => {
      cancelled = true;
      requestControllerRef.current?.abort();
      controller.abort();
      if (requestControllerRef.current === controller) {
        requestControllerRef.current = null;
      }
    };
  }, [limit]);

  function refreshRuntimeTeams() {
    requestControllerRef.current?.abort();
    const controller = new AbortController();
    requestControllerRef.current = controller;
    setRuntimeTeamsRefreshing(true);
    setRuntimeTeamsError(null);

    void Promise.all([
      listRuntimeTeams({ limit, signal: controller.signal }),
      listRuntimeTeamSummaries({
        limit,
        light: true,
        signal: controller.signal,
      }),
    ])
      .then(([teamsResponse, summariesResponse]) => {
        if (
          controller.signal.aborted ||
          requestControllerRef.current !== controller
        ) {
          return;
        }
        setRuntimeTeams(teamsResponse.teams);
        setRuntimeTeamSummaries(summariesResponse.teams);
      })
      .catch((error) => {
        if (
          controller.signal.aborted ||
          requestControllerRef.current !== controller
        ) {
          return;
        }
        const message =
          error instanceof Error ? error.message : "failed to refresh runtime teams";
        setRuntimeTeamsError(message);
      })
      .finally(() => {
        if (
          !controller.signal.aborted &&
          requestControllerRef.current === controller
        ) {
          setRuntimeTeamsLoading(false);
          setRuntimeTeamsRefreshing(false);
        }
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
