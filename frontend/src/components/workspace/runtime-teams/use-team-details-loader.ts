// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Dispatch, type SetStateAction, useEffect } from "react";

import {
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
  sortEvents,
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
  useEffect(() => {
    if (!selectedTeamId) {
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
        ] = await Promise.allSettled([
            getRuntimeTeamFinalSummary(selectedTeamId),
            listRuntimeTeamTeammates(selectedTeamId, { limit: 8 }),
            listRuntimeTeamTasks(selectedTeamId, {
              includeDependencies: true,
              includeDependents: true,
              limit: 12,
            }),
            getRuntimeTeamTaskGraph(selectedTeamId, { limit: 40 }),
            listRuntimeTeamEvents(selectedTeamId, { limit: 12 }),
            listRuntimeTeamMailbox(selectedTeamId, {
              includeBroadcast: true,
              limit: 10,
            }),
            listRuntimeTeamPathClaims(selectedTeamId, {
              activeOnly: true,
              limit: 10,
            }),
          ] as const);

        if (cancelled) {
          return;
        }

        const nextDetails = createEmptyDetails();
        const partialFailures: string[] = [];

        if (summaryResult.status === "fulfilled") {
          nextDetails.finalSummary = summaryResult.value.summary?.trim() || "";
        } else {
          partialFailures.push("final summary");
        }

        if (teammatesResult.status === "fulfilled") {
          nextDetails.teammates = sortTeammates(teammatesResult.value.teammates);
        } else {
          partialFailures.push("teammates");
        }

        if (tasksResult.status === "fulfilled") {
          nextDetails.tasks = sortTasks(tasksResult.value.tasks);
        } else {
          partialFailures.push("tasks");
        }

        if (graphResult.status === "fulfilled") {
          nextDetails.graph = graphResult.value;
        } else {
          partialFailures.push("task graph");
        }

        if (eventsResult.status === "fulfilled") {
          nextDetails.events = sortEvents(eventsResult.value.events);
        } else {
          partialFailures.push("events");
        }

        if (mailboxResult.status === "fulfilled") {
          nextDetails.mailbox = sortMailbox(mailboxResult.value.messages);
        } else {
          partialFailures.push("mailbox");
        }

        if (pathClaimsResult.status === "fulfilled") {
          nextDetails.pathClaims = sortPathClaims(pathClaimsResult.value.claims);
        } else {
          partialFailures.push("path claims");
        }

        setDetails(nextDetails);
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
        setDetails(createEmptyDetails());
        setDetailsError(message);
      } finally {
        if (!cancelled) {
          setIsDetailsLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
    // P0-2 机械搬迁：父组件透传的 setState 引用稳定，依赖数组与原实现保持一致。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedTeamId]);
}
