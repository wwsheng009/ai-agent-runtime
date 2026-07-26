import { useCallback, useEffect, useState } from "react";

import {
  getSessionPlanMode,
  updateSessionPlanMode,
  type RuntimeSessionPlanMode,
  type RuntimeSessionPlanModeExitDecision,
} from "@/lib/runtime-api";
import { buildRuntimeEventReloadKey } from "@/hooks/workspace/use-runtime-checkpoints";

type UseRuntimePlanModeOptions = {
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  sessionId?: string;
};

type ShouldReloadRuntimePlanModeOptions = {
  lastHandledEventKey?: string;
  lastRuntimeEventKey?: string;
  lastRuntimeEventType?: string;
  loadedPlanSessionId: string;
  sessionId?: string;
};

const PLAN_MODE_RELOAD_EVENTS = new Set([
  "tool.completed",
  "tool_completed",
  "permission_mode_changed",
  "plan_mode_changed",
  "plan_updated",
  "session_updated",
]);

export function shouldReloadRuntimePlanMode({
  lastHandledEventKey,
  lastRuntimeEventKey,
  lastRuntimeEventType,
  loadedPlanSessionId,
  sessionId,
}: ShouldReloadRuntimePlanModeOptions) {
  if (!sessionId) {
    return false;
  }

  // Session switch always reloads once; after a successful load we stop looping
  // even when the plan payload is null/unavailable.
  if (loadedPlanSessionId !== sessionId) {
    return true;
  }

  if (!lastRuntimeEventType || !lastRuntimeEventKey) {
    return false;
  }

  if (!PLAN_MODE_RELOAD_EVENTS.has(lastRuntimeEventType)) {
    return false;
  }

  return lastRuntimeEventKey !== lastHandledEventKey;
}

export function formatPlanModeStatusLabel(plan: RuntimeSessionPlanMode | null) {
  if (!plan) {
    return "Unavailable";
  }
  if (plan.active) {
    return "Active";
  }
  if (plan.status === "exited") {
    return "Exited";
  }
  return "Inactive";
}

export function canSubmitPlanModeDecision(plan: RuntimeSessionPlanMode | null) {
  return Boolean(plan?.active);
}

export function useRuntimePlanMode({
  lastRuntimeEventType,
  runtimeEventCount,
  sessionId,
}: UseRuntimePlanModeOptions) {
  const [plan, setPlan] = useState<RuntimeSessionPlanMode | null>(null);
  const [planError, setPlanError] = useState<string | null>(null);
  const [planLoading, setPlanLoading] = useState(false);
  const [planActionPending, setPlanActionPending] = useState(false);
  const [loadedPlanSessionId, setLoadedPlanSessionId] = useState("");
  const [lastHandledEventKey, setLastHandledEventKey] = useState("");
  const [notesDraft, setNotesDraft] = useState("");
  const lastRuntimeEventKey = buildRuntimeEventReloadKey(
    lastRuntimeEventType,
    runtimeEventCount,
  );

  const reloadPlan = useCallback(async () => {
    if (!sessionId) {
      setPlan(null);
      setPlanError(null);
      setLoadedPlanSessionId("");
      setLastHandledEventKey("");
      setNotesDraft("");
      return;
    }

    setPlanLoading(true);
    setPlanError(null);

    try {
      const response = await getSessionPlanMode(sessionId);
      setPlan(response);
      setLoadedPlanSessionId(sessionId);
      setLastHandledEventKey(lastRuntimeEventKey);
      setNotesDraft(response.notes ?? "");
    } catch (error) {
      setPlan(null);
      setPlanError(error instanceof Error ? error.message : "failed to load plan mode");
      // Mark session as loaded even on error so we do not hammer the API in a loop.
      setLoadedPlanSessionId(sessionId);
      setLastHandledEventKey(lastRuntimeEventKey);
    } finally {
      setPlanLoading(false);
    }
  }, [lastRuntimeEventKey, sessionId]);

  useEffect(() => {
    if (
      !shouldReloadRuntimePlanMode({
        lastHandledEventKey,
        lastRuntimeEventKey,
        lastRuntimeEventType,
        loadedPlanSessionId,
        sessionId,
      })
    ) {
      return;
    }

    let cancelled = false;

    void (async () => {
      if (!sessionId) {
        if (!cancelled) {
          setPlan(null);
          setPlanError(null);
          setLoadedPlanSessionId("");
          setLastHandledEventKey("");
          setNotesDraft("");
        }
        return;
      }

      setPlanLoading(true);
      setPlanError(null);

      try {
        const response = await getSessionPlanMode(sessionId);
        if (cancelled) {
          return;
        }
        setPlan(response);
        setLoadedPlanSessionId(sessionId);
        setLastHandledEventKey(lastRuntimeEventKey);
        setNotesDraft((current) =>
          current.trim().length > 0 ? current : (response.notes ?? ""),
        );
      } catch (error) {
        if (cancelled) {
          return;
        }
        setPlan(null);
        setPlanError(
          error instanceof Error ? error.message : "failed to load plan mode",
        );
        setLoadedPlanSessionId(sessionId);
        setLastHandledEventKey(lastRuntimeEventKey);
      } finally {
        if (!cancelled) {
          setPlanLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [
    lastHandledEventKey,
    lastRuntimeEventKey,
    lastRuntimeEventType,
    loadedPlanSessionId,
    sessionId,
  ]);

  const submitDecision = useCallback(
    async (decision: Exclude<RuntimeSessionPlanModeExitDecision, "">) => {
      if (!sessionId) {
        return;
      }

      setPlanActionPending(true);
      setPlanError(null);

      try {
        const response = await updateSessionPlanMode(sessionId, {
          action: decision,
          notes: notesDraft.trim() || undefined,
        });
        setPlan(response);
        setLoadedPlanSessionId(sessionId);
        setNotesDraft(response.notes ?? notesDraft);
      } catch (error) {
        setPlanError(
          error instanceof Error ? error.message : "failed to update plan mode",
        );
      } finally {
        setPlanActionPending(false);
      }
    },
    [notesDraft, sessionId],
  );

  const enterPlanMode = useCallback(
    async (planPath?: string) => {
      if (!sessionId) {
        return;
      }

      setPlanActionPending(true);
      setPlanError(null);

      try {
        const response = await updateSessionPlanMode(sessionId, {
          action: "enter",
          plan_path: planPath?.trim() || undefined,
        });
        setPlan(response);
        setLoadedPlanSessionId(sessionId);
        setNotesDraft(response.notes ?? "");
      } catch (error) {
        setPlanError(
          error instanceof Error ? error.message : "failed to enter plan mode",
        );
      } finally {
        setPlanActionPending(false);
      }
    },
    [sessionId],
  );

  return {
    canSubmitDecision: canSubmitPlanModeDecision(plan),
    enterPlanMode,
    notesDraft,
    onNotesDraftChange: setNotesDraft,
    plan,
    planActionPending,
    planError,
    planLoading,
    planStatusLabel: formatPlanModeStatusLabel(plan),
    reloadPlan,
    submitDecision,
  };
}
