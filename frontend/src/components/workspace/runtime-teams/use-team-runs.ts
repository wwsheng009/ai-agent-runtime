import { useCallback, useEffect, useRef, useState } from "react";

import {
  listRuntimeTeamRuns,
  type RuntimeTeamProductCapabilities,
  type RuntimeTeamRun,
} from "@/lib/runtime-api";

function isOlderRuntimeTeamRunSnapshot(
  existingRun: RuntimeTeamRun,
  updatedRun: RuntimeTeamRun,
): boolean {
  return (
    updatedRun.version < existingRun.version ||
    updatedRun.event_seq < existingRun.event_seq
  );
}

export function mergeRuntimeTeamRunSnapshot(
  current: RuntimeTeamRun[],
  updatedRun: RuntimeTeamRun,
): RuntimeTeamRun[] {
  const index = current.findIndex((run) => run.id === updatedRun.id);
  if (index < 0) {
    return [updatedRun, ...current];
  }

  const existingRun = current[index];
  if (isOlderRuntimeTeamRunSnapshot(existingRun, updatedRun)) {
    return current;
  }

  const next = current.slice();
  next[index] = updatedRun;
  return next;
}

export function mergeRuntimeTeamRunList(
  current: RuntimeTeamRun[],
  incoming: RuntimeTeamRun[],
): RuntimeTeamRun[] {
  const currentByID = new Map(current.map((run) => [run.id, run]));
  return incoming.map((run) => {
    const existingRun = currentByID.get(run.id);
    return existingRun && isOlderRuntimeTeamRunSnapshot(existingRun, run)
      ? existingRun
      : run;
  });
}

export function useTeamRuns() {
  const [runs, setRuns] = useState<RuntimeTeamRun[]>([]);
  const [capabilities, setCapabilities] =
    useState<RuntimeTeamProductCapabilities>({});
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const requestControllerRef = useRef<AbortController | null>(null);

  const refresh = useCallback(async () => {
    requestControllerRef.current?.abort();
    const controller = new AbortController();
    requestControllerRef.current = controller;
    setIsLoading(true);
    setError(null);
    try {
      const response = await listRuntimeTeamRuns({
        limit: 20,
        signal: controller.signal,
      });
      if (controller.signal.aborted || requestControllerRef.current !== controller) {
        return;
      }
      setRuns((current) => mergeRuntimeTeamRunList(current, response.runs));
      setCapabilities(response.capabilities ?? {});
    } catch (loadError) {
      if (
        controller.signal.aborted ||
        requestControllerRef.current !== controller
      ) {
        return;
      }
      setError(
        loadError instanceof Error
          ? loadError.message
          : "failed to load Team Runs",
      );
    } finally {
      if (
        !controller.signal.aborted &&
        requestControllerRef.current === controller
      ) {
        setIsLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    void refresh();
    return () => {
      requestControllerRef.current?.abort();
      requestControllerRef.current = null;
    };
  }, [refresh]);

  const updateRun = useCallback((updatedRun: RuntimeTeamRun) => {
    setRuns((current) => mergeRuntimeTeamRunSnapshot(current, updatedRun));
  }, []);

  return { capabilities, error, isLoading, refresh, runs, updateRun };
}
