// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Dispatch, type SetStateAction, useEffect, useRef } from "react";

import {
  getRuntimeTeamPreflight,
  getRuntimeTeamFinalSummary,
  getRuntimeTeamTaskGraph,
  listRuntimeTeamEvents,
  listRuntimeTeamMailbox,
  listRuntimeTeamPathClaims,
  listRuntimeTeamTasks,
  listRuntimeTeamTeammates,
} from "@/lib/runtime-api";

import {
  createEmptyDetails,
  mergeTeamFinalSummary,
  mergeTeamEvents,
  sortMailbox,
  sortPathClaims,
  sortTasks,
  sortTeammates,
  type ClaimCheckState,
  type TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared";

type UseTeamDetailsLoaderOptions = {
  selectedTeamId: string;
  setClaimCheckError: Dispatch<SetStateAction<string | null>>;
  setClaimCheckState: Dispatch<SetStateAction<ClaimCheckState>>;
  setDetails: Dispatch<SetStateAction<TeamDetailsState>>;
  setDetailsError: Dispatch<SetStateAction<string | null>>;
  setIsDetailsLoading: Dispatch<SetStateAction<boolean>>;
  setMailboxBodyDraft: Dispatch<SetStateAction<string>>;
  setMailboxError: Dispatch<SetStateAction<string | null>>;
  setMailboxFromDraft: Dispatch<SetStateAction<string>>;
  setMailboxKindDraft: Dispatch<SetStateAction<string>>;
  setMailboxTaskDraft: Dispatch<SetStateAction<string>>;
  setMailboxToDraft: Dispatch<SetStateAction<string>>;
  setReadPathDraft: Dispatch<SetStateAction<string>>;
  setWritePathDraft: Dispatch<SetStateAction<string>>;
};

export function useTeamDetailsLoader({
  selectedTeamId,
  setClaimCheckError,
  setClaimCheckState,
  setDetails,
  setDetailsError,
  setIsDetailsLoading,
  setMailboxBodyDraft,
  setMailboxError,
  setMailboxFromDraft,
  setMailboxKindDraft,
  setMailboxTaskDraft,
  setMailboxToDraft,
  setReadPathDraft,
  setWritePathDraft,
}: UseTeamDetailsLoaderOptions) {
  const previousTeamId = useRef("");
  const eventCursor = useRef(0);

  useEffect(() => {
    if (!selectedTeamId) {
      previousTeamId.current = "";
      eventCursor.current = 0;
      setDetails(createEmptyDetails());
      setDetailsError(null);
      setMailboxError(null);
      setClaimCheckError(null);
      setClaimCheckState(null);
      setMailboxBodyDraft("");
      setMailboxFromDraft("lead");
      setMailboxToDraft("*");
      setMailboxKindDraft("info");
      setMailboxTaskDraft("");
      return;
    }

    const isNewTeam = previousTeamId.current !== selectedTeamId;
    previousTeamId.current = selectedTeamId;
    if (isNewTeam) {
      // Never display one Team's last-known-good data under another Team ID.
      setDetails(createEmptyDetails());
      eventCursor.current = 0;
    }

    setMailboxError(null);
    setClaimCheckError(null);
    setClaimCheckState(null);
    setMailboxBodyDraft("");
    setMailboxFromDraft("lead");
    setMailboxToDraft("*");
    setMailboxKindDraft("info");
    setMailboxTaskDraft("");
    setReadPathDraft("");
    setWritePathDraft("");

    let cancelled = false;
    const controller = new AbortController();
    setIsDetailsLoading(true);
    setDetailsError(null);
    void (async () => {
      try {
        const [
          summaryResult,
          teammatesResult,
          tasksResult,
          graphResult,
          eventsResult,
          mailboxResult,
          pathClaimsResult,
          preflightResult,
        ] = await Promise.allSettled([
            getRuntimeTeamFinalSummary(selectedTeamId, { signal: controller.signal }),
            listRuntimeTeamTeammates(selectedTeamId, {
              limit: 8,
              signal: controller.signal,
            }),
            listRuntimeTeamTasks(selectedTeamId, {
              includeDependencies: true,
              includeDependents: true,
              limit: 12,
              signal: controller.signal,
            }),
            getRuntimeTeamTaskGraph(selectedTeamId, {
              limit: 40,
              signal: controller.signal,
            }),
            listRuntimeTeamEvents(selectedTeamId, {
              after: eventCursor.current,
              limit: 12,
              signal: controller.signal,
            }),
            listRuntimeTeamMailbox(selectedTeamId, {
              includeBroadcast: true,
              limit: 10,
              signal: controller.signal,
            }),
            listRuntimeTeamPathClaims(selectedTeamId, {
              activeOnly: true,
              limit: 10,
              signal: controller.signal,
            }),
            getRuntimeTeamPreflight(selectedTeamId, { signal: controller.signal }),
          ] as const);

        if (cancelled || controller.signal.aborted) {
          return;
        }

        const partialFailures: string[] = [];
        if (summaryResult.status === "rejected") partialFailures.push("final summary");
        if (teammatesResult.status === "rejected") partialFailures.push("teammates");
        if (tasksResult.status === "rejected") partialFailures.push("tasks");
        if (graphResult.status === "rejected") partialFailures.push("task graph");
        if (eventsResult.status === "rejected") partialFailures.push("events");
        if (mailboxResult.status === "rejected") partialFailures.push("mailbox");
        if (pathClaimsResult.status === "rejected") partialFailures.push("path claims");
        if (preflightResult.status === "rejected") partialFailures.push("preflight");

        setDetails((current) => {
          const nextDetails = { ...current, isStale: partialFailures.length > 0 };
          const summaryResponse =
            summaryResult.status === "fulfilled" ? summaryResult.value : undefined;
          const summary = mergeTeamFinalSummary(current, summaryResponse);
          nextDetails.finalSummary = summary.finalSummary;
          nextDetails.summaryState = summary.summaryState;
          if (teammatesResult.status === "fulfilled") {
            nextDetails.teammates = sortTeammates(teammatesResult.value.teammates);
          }
          if (tasksResult.status === "fulfilled") {
            nextDetails.tasks = sortTasks(tasksResult.value.tasks);
          }
          if (graphResult.status === "fulfilled") {
            nextDetails.graph = graphResult.value;
          }
          if (eventsResult.status === "fulfilled") {
            const response = eventsResult.value;
            nextDetails.events = mergeTeamEvents(current.events, response.events);
            const pageCursor =
              response.next_cursor ??
              response.events.reduce(
                (cursor, event) => Math.max(cursor, event.seq),
                eventCursor.current,
              );
            eventCursor.current = Math.max(eventCursor.current, pageCursor);
            nextDetails.eventCursor = Math.max(current.eventCursor, pageCursor);
            nextDetails.latestEventSeq = Math.max(
              current.latestEventSeq,
              response.latest_seq ?? pageCursor,
            );
          }
          if (mailboxResult.status === "fulfilled") {
            nextDetails.mailbox = sortMailbox(mailboxResult.value.messages);
          }
          if (pathClaimsResult.status === "fulfilled") {
            nextDetails.pathClaims = sortPathClaims(pathClaimsResult.value.claims);
          }
          if (preflightResult.status === "fulfilled") {
            nextDetails.preflight = preflightResult.value;
          }
          return nextDetails;
        });
        setDetailsError(
          partialFailures.length > 0
            ? `partially loaded team details: ${partialFailures.join(", ")}`
            : null,
        );
      } catch (fetchError) {
        if (cancelled) {
          return;
        }
        const message =
          fetchError instanceof Error
            ? fetchError.message
            : "failed to load runtime team details";
        setDetails((current) => ({ ...current, isStale: true }));
        setDetailsError(message);
      } finally {
        if (!cancelled) {
          setIsDetailsLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
      controller.abort();
    };
    // P0-2 机械搬迁：父组件透传的 setState 引用稳定，依赖数组与原实现保持一致。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedTeamId]);
}
