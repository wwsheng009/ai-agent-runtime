import { useEffect, useRef, type Dispatch, type SetStateAction } from "react";

import {
  getRuntimeTeam,
  getRuntimeTeamFinalSummary,
  listRuntimeTeamEvents,
  listRuntimeTeamSummaries,
  listRuntimeTeamTasks,
} from "@/lib/runtime-api";
import {
  getTeamDetailsPollDelay,
  isTerminalTeamStatus,
  mergeTeamEvents,
  mergeTeamFinalSummary,
  shouldPollTeamDetails,
  sortTasks,
  type TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared";
import type { RuntimeTeamSummaryState } from "@/types/runtime";

type UseTeamDetailsPollingOptions = {
  eventCursor: number;
  isLoading: boolean;
  selectedTeamId: string;
  setDetails: Dispatch<SetStateAction<TeamDetailsState>>;
  setDetailsError: Dispatch<SetStateAction<string | null>>;
  summaryState: RuntimeTeamSummaryState;
  teamStatus?: string;
};

/**
 * Polls only the selected legacy Team while Runtime reports it as observable.
 * Terminal Teams receive at most eight summary observations. Poll/network
 * failures only mark the retained snapshot stale; they never synthesize a
 * failed Team/task state or cancel the Runtime operation.
 */
export function useTeamDetailsPolling({
  eventCursor,
  isLoading,
  selectedTeamId,
  setDetails,
  setDetailsError,
  summaryState,
  teamStatus,
}: UseTeamDetailsPollingOptions) {
  const cursor = useRef({ teamId: "", seq: 0 });
  const teamStatusRef = useRef(teamStatus ?? "");
  const summaryStateRef = useRef(summaryState);
  teamStatusRef.current = teamStatus ?? "";
  summaryStateRef.current = summaryState;
  if (cursor.current.teamId !== selectedTeamId) {
    cursor.current = { teamId: selectedTeamId, seq: eventCursor };
  } else {
    cursor.current.seq = Math.max(cursor.current.seq, eventCursor);
  }

  useEffect(() => {
    if (!selectedTeamId || isLoading) {
      return;
    }

    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let activeRequestController: AbortController | undefined;
    let observedStatus = teamStatusRef.current;
    let observedSummaryState = summaryStateRef.current;
    let consecutivePolls = 0;
    let terminalPollCount = 0;
    const isDocumentHidden = () => document.visibilityState === "hidden";

    const schedule = () => {
      if (
        cancelled ||
        isDocumentHidden() ||
        !shouldPollTeamDetails(
          observedStatus,
          observedSummaryState,
          terminalPollCount,
        )
      ) {
        return;
      }
      timer = setTimeout(
        () => void poll(),
        getTeamDetailsPollDelay(
          observedStatus,
          observedSummaryState,
          consecutivePolls,
        ),
      );
    };

    const poll = async () => {
      if (cancelled || isDocumentHidden()) {
        return;
      }

      const afterSeq = cursor.current.seq;
      const requestController = new AbortController();
      activeRequestController = requestController;
      const [teamResult, summaryResult, tasksResult, eventsResult, finalResult] =
        await Promise.allSettled([
          getRuntimeTeam(selectedTeamId, { signal: requestController.signal }),
          listRuntimeTeamSummaries({
            teamIds: [selectedTeamId],
            limit: 1,
            light: true,
            signal: requestController.signal,
          }),
          listRuntimeTeamTasks(selectedTeamId, {
            includeDependencies: true,
            includeDependents: true,
            limit: 12,
            signal: requestController.signal,
          }),
          listRuntimeTeamEvents(selectedTeamId, {
            afterSeq,
            limit: 12,
            signal: requestController.signal,
          }),
          getRuntimeTeamFinalSummary(selectedTeamId, {
            signal: requestController.signal,
          }),
        ] as const);

      if (
        cancelled ||
        requestController.signal.aborted ||
        isDocumentHidden()
      ) {
        return;
      }
      activeRequestController = undefined;

      const failures: string[] = [];
      if (teamResult.status === "rejected") failures.push("Team status");
      if (summaryResult.status === "rejected") failures.push("task summary");
      if (tasksResult.status === "rejected") failures.push("tasks");
      if (eventsResult.status === "rejected") failures.push("events");
      if (finalResult.status === "rejected") failures.push("final summary");

      const runtimeStatus =
        teamResult.status === "fulfilled"
          ? teamResult.value.team.status?.trim()
          : "";
      if (runtimeStatus) {
        observedStatus = runtimeStatus;
      }

      const eventPage =
        eventsResult.status === "fulfilled" ? eventsResult.value : undefined;
      const nextCursor =
        eventPage?.next_cursor ??
        eventPage?.events.reduce(
          (value, event) => Math.max(value, event.seq),
          afterSeq,
        ) ??
        afterSeq;
      const receivedNewEvent =
        eventPage?.events.some((event) => event.seq > afterSeq) ?? false;
      cursor.current = {
        teamId: selectedTeamId,
        seq: Math.max(cursor.current.seq, nextCursor),
      };

      if (isTerminalTeamStatus(observedStatus)) {
        terminalPollCount += 1;
      } else {
        terminalPollCount = 0;
      }
      consecutivePolls = failures.length > 0 || !receivedNewEvent
        ? consecutivePolls + 1
        : 0;

      setDetails((current) => {
        const next = {
          ...current,
          // A successful observation makes the retained snapshot current
          // again.  Failed sections remain last-known-good and are still
          // represented by the refresh error; do not leave a sticky stale bit
          // after the Runtime has recovered.
          isStale: failures.length > 0 ? true : false,
        };
        if (runtimeStatus) {
          next.liveTeamStatus = runtimeStatus;
        }
        if (
          summaryResult.status === "fulfilled" &&
          summaryResult.value.teams.length > 0
        ) {
          next.liveSummary = summaryResult.value.teams[0];
        }
        if (tasksResult.status === "fulfilled") {
          next.tasks = sortTasks(tasksResult.value.tasks);
        }
        if (eventPage) {
          next.events = mergeTeamEvents(current.events, eventPage.events);
          next.eventCursor = Math.max(current.eventCursor, cursor.current.seq);
          next.latestEventSeq = Math.max(
            current.latestEventSeq,
            eventPage.latest_seq ?? cursor.current.seq,
          );
        }
        if (finalResult.status === "fulfilled") {
          const merged = mergeTeamFinalSummary(current, finalResult.value);
          next.finalSummary = merged.finalSummary;
          next.summaryState = merged.summaryState;
        }
        return next;
      });
      if (finalResult.status === "fulfilled") {
        const responseState = finalResult.value.summary_state;
        if (responseState && responseState !== "unknown") {
          observedSummaryState = responseState;
        } else if (finalResult.value.summary?.trim()) {
          observedSummaryState = "ready";
        }
      }

      setDetailsError(
        failures.length > 0
          ? `failed to refresh ${failures.join(", ")}`
          : null,
      );
      schedule();
    };

    const onVisibilityChange = () => {
      if (isDocumentHidden()) {
        if (timer !== undefined) {
          clearTimeout(timer);
          timer = undefined;
        }
        activeRequestController?.abort();
        activeRequestController = undefined;
      } else {
        schedule();
      }
    };

    document.addEventListener("visibilitychange", onVisibilityChange);
    schedule();
    return () => {
      cancelled = true;
      document.removeEventListener("visibilitychange", onVisibilityChange);
      if (timer !== undefined) {
        clearTimeout(timer);
      }
      activeRequestController?.abort();
    };
  }, [
    isLoading,
    selectedTeamId,
    setDetails,
    setDetailsError,
  ]);
}
