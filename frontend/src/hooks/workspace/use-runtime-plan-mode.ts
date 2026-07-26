import { useCallback, useEffect, useState } from "react";

import {
  getSessionPlanMode,
  updateSessionPlanMode,
  type RuntimeSessionPlanMode,
  type RuntimeSessionPlanModeExitDecision,
} from "@/lib/runtime-api";

type UseRuntimePlanModeOptions = {
  lastRuntimeEventType?: string;
  sessionId?: string;
};

type ShouldReloadRuntimePlanModeOptions = {
  hasPlan: boolean;
  lastHandledEventType?: string;
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
  hasPlan,
  lastHandledEventType,
  lastRuntimeEventType,
  loadedPlanSessionId,
  sessionId,
}: ShouldReloadRuntimePlanModeOptions) {
  if (!sessionId) {
    return false;
  }

  if (loadedPlanSessionId !== sessionId) {
    return true;
  }

  if (!hasPlan) {
    return true;
  }

  if (!lastRuntimeEventType) {
    return false;
  }

  if (!PLAN_MODE_RELOAD_EVENTS.has(lastRuntimeEventType)) {
    return false;
  }

  return lastRuntimeEventType !== lastHandledEventType;
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
  sessionId,
}: UseRuntimePlanModeOptions) {
  const [plan, setPlan] = useState<RuntimeSessionPlanMode | null>(null);
  const [planError, setPlanError] = useState<string | null>(null);
  const [planLoading, setPlanLoading] = useState(false);
  const [planActionPending, setPlanActionPending] = useState(false);
  const [loadedPlanSessionId, setLoadedPlanSessionId] = useState("");
  const [lastHandledEventType, setLastHandledEventType] = useState<string | undefined>();
  const [notesDraft, setNotesDraft] = useState("");

  const reloadPlan = useCallback(async () => {
    if (!sessionId) {
      setPlan(null);
      setPlanError(null);
      setLoadedPlanSessionId("");
      setLastHandledEventType(undefined);
      setNotesDraft("");
      return;
    }

    setPlanLoading(true);
    setPlanError(null);

    try {
      const response = await getSessionPlanMode(sessionId);
      setPlan(response);
      setLoadedPlanSessionId(sessionId);
      setLastHandledEventType(lastRuntimeEventType);
      setNotesDraft(response.notes ?? "");
    } catch (error) {
      setPlan(null);
      setPlanError(error instanceof Error ? error.message : "failed to load plan mode");
    } finally {
      setPlanLoading(false);
    }
  }, [lastRuntimeEventType, sessionId]);

  useEffect(() => {
    if (
      !shouldReloadRuntimePlanMode({
        hasPlan: plan !== null,
        lastHandledEventType,
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
          setLastHandledEventType(undefined);
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
        setLastHandledEventType(lastRuntimeEventType);
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
    lastHandledEventType,
    lastRuntimeEventType,
    loadedPlanSessionId,
    plan,
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
