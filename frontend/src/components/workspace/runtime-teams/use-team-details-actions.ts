import {
  useCallback,
  type Dispatch,
  type SetStateAction,
} from "react";

import {
  getRuntimeTeamTaskGraph,
  listRuntimeTeamEvents,
  listRuntimeTeamMailbox,
  listRuntimeTeamTasks,
} from "@/lib/runtime-api";

import {
  mergeTeamEvents,
  sortMailbox,
  sortTasks,
  type TeamDetailsState,
} from "./shared";

type UseTeamDetailsActionsOptions = {
  details: TeamDetailsState;
  selectedTeamIdRef: { current: string };
  setDetails: Dispatch<SetStateAction<TeamDetailsState>>;
  setDetailsError: Dispatch<SetStateAction<string | null>>;
};

export function useTeamDetailsActions({
  details,
  selectedTeamIdRef,
  setDetails,
  setDetailsError,
}: UseTeamDetailsActionsOptions) {
  const refreshMailboxAndEvents = useCallback(
    async (teamId: string): Promise<string | null> => {
      const [mailboxResult, eventsResult] = await Promise.allSettled([
        listRuntimeTeamMailbox(teamId, {
          includeBroadcast: true,
          limit: 10,
        }),
        listRuntimeTeamEvents(teamId, {
          afterSeq: details.eventCursor,
          limit: 12,
        }),
      ]);

      if (selectedTeamIdRef.current !== teamId) {
        return null;
      }
      const failures: string[] = [];
      if (mailboxResult.status === "rejected") failures.push("mailbox");
      if (eventsResult.status === "rejected") failures.push("events");

      setDetails((current) => {
        if (selectedTeamIdRef.current !== teamId) {
          return current;
        }
        const next = { ...current, isStale: failures.length > 0 };
        if (mailboxResult.status === "fulfilled") {
          next.mailbox = sortMailbox(mailboxResult.value.messages);
        }
        if (eventsResult.status === "fulfilled") {
          const response = eventsResult.value;
          next.events = mergeTeamEvents(current.events, response.events);
          const pageCursor =
            response.next_cursor ??
            response.events.reduce(
              (cursor, event) => Math.max(cursor, event.seq),
              current.eventCursor,
            );
          next.eventCursor = Math.max(current.eventCursor, pageCursor);
          next.latestEventSeq = Math.max(
            current.latestEventSeq,
            response.latest_seq ?? pageCursor,
          );
        }
        return next;
      });
      return failures.length > 0
        ? `failed to refresh ${failures.join(" and ")}`
        : null;
    },
    [details.eventCursor, selectedTeamIdRef, setDetails],
  );

  const refreshSelectedTeamTasksAndEvents = useCallback(
    async (teamId: string) => {
      const [tasksResult, graphResult, eventsResult] = await Promise.allSettled([
        listRuntimeTeamTasks(teamId, {
          includeDependencies: true,
          includeDependents: true,
          limit: 12,
        }),
        getRuntimeTeamTaskGraph(teamId, { limit: 40 }),
        listRuntimeTeamEvents(teamId, {
          afterSeq: details.eventCursor,
          limit: 12,
        }),
      ]);

      if (selectedTeamIdRef.current !== teamId) {
        return;
      }
      const failures: string[] = [];
      if (tasksResult.status === "rejected") failures.push("tasks");
      if (graphResult.status === "rejected") failures.push("task graph");
      if (eventsResult.status === "rejected") failures.push("events");

      setDetails((current) => {
        if (selectedTeamIdRef.current !== teamId) {
          return current;
        }
        const next = { ...current, isStale: failures.length > 0 };
        if (tasksResult.status === "fulfilled") {
          next.tasks = sortTasks(tasksResult.value.tasks);
        }
        if (graphResult.status === "fulfilled") {
          next.graph = graphResult.value;
        }
        if (eventsResult.status === "fulfilled") {
          const response = eventsResult.value;
          next.events = mergeTeamEvents(current.events, response.events);
          const pageCursor =
            response.next_cursor ??
            response.events.reduce(
              (cursor, event) => Math.max(cursor, event.seq),
              current.eventCursor,
            );
          next.eventCursor = Math.max(current.eventCursor, pageCursor);
          next.latestEventSeq = Math.max(
            current.latestEventSeq,
            response.latest_seq ?? pageCursor,
          );
        }
        return next;
      });
      if (failures.length > 0) {
        setDetailsError(`partially refreshed team details: ${failures.join(", ")}`);
      }
    },
    [details.eventCursor, selectedTeamIdRef, setDetails, setDetailsError],
  );

  return { refreshMailboxAndEvents, refreshSelectedTeamTasksAndEvents };
}
