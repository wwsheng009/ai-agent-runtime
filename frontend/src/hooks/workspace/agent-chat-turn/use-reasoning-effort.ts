// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。
//
// 会话级 reasoning effort：以 selectedThread.reasoningEffort（会话 context 投影）为准，
// 提交时乐观覆盖 pending 值，待会话刷新带回后再清空。

import { useEffect, useRef, useState } from "react";

import { type Thread } from "@/data/mock";
import { findRuntimeProviderRecord } from "@/hooks/workspace/use-runtime-model-catalog";
import { updateRuntimeSession } from "@/lib/runtime-api";
import {
  SESSION_REASONING_EFFORT_CONTEXT_KEY,
  normalizeReasoningEffort,
  resolveDefaultReasoningEffort,
  resolveEffectiveReasoningEffort,
  resolveModelReasoningEffortOptions,
} from "@/lib/reasoning-effort";
import { normalizeSessionId } from "@/lib/session-id";
import type { RuntimeModelsResponse } from "@/types/runtime";

export function useChatTurnReasoningEffort(options: {
  onSessionTouched?: () => void;
  runtimeModels: RuntimeModelsResponse | null;
  selectedModel: string;
  selectedProvider: string;
  selectedThread: Thread | undefined;
}) {
  const { onSessionTouched, runtimeModels, selectedModel, selectedProvider, selectedThread } =
    options;
  const [pendingReasoningEffort, setPendingReasoningEffort] = useState<{
    sessionId: string;
    effort: string;
  } | null>(null);
  const [draftReasoningEffort, setDraftReasoningEffort] = useState("");
  const draftReasoningEffortRef = useRef("");
  const [reasoningEffortError, setReasoningEffortError] = useState<
    string | null
  >(null);
  const activeSessionId = normalizeSessionId(selectedThread?.sessionId ?? "");
  const selectedProviderRecord = findRuntimeProviderRecord(
    runtimeModels?.providers ?? [],
    selectedProvider,
  );
  const reasoningEffortOptions = resolveModelReasoningEffortOptions(
    selectedProviderRecord,
    selectedModel,
  );
  const configuredReasoningEffortDefault = resolveDefaultReasoningEffort(
    selectedProviderRecord,
    selectedModel,
    runtimeModels?.default_reasoning_effort,
  );
  const sessionReasoningEffort = normalizeReasoningEffort(
    selectedThread?.reasoningEffort,
  );
  const requestedReasoningEffort =
    activeSessionId === ""
      ? draftReasoningEffort
      : pendingReasoningEffort?.sessionId === activeSessionId
        ? pendingReasoningEffort.effort
        : sessionReasoningEffort;
  const selectedReasoningEffort = resolveEffectiveReasoningEffort(
    requestedReasoningEffort,
    reasoningEffortOptions,
  );

  useEffect(() => {
    if (!pendingReasoningEffort) {
      return;
    }
    if (pendingReasoningEffort.sessionId !== activeSessionId) {
      return;
    }
    // 会话记录已带回该档位：清空乐观覆盖，避免长期遮蔽服务端真值。
    if (sessionReasoningEffort === pendingReasoningEffort.effort) {
      setPendingReasoningEffort(null);
    }
  }, [activeSessionId, pendingReasoningEffort, sessionReasoningEffort]);

  async function setReasoningEffort(nextEffort: string) {
    const effort = normalizeReasoningEffort(nextEffort);
    if (!activeSessionId) {
      // 草稿线程尚无会话：先本地保留，首轮落库时再写入新会话。
      draftReasoningEffortRef.current = effort;
      setDraftReasoningEffort(effort);
      return;
    }

    setReasoningEffortError(null);
    setPendingReasoningEffort({ sessionId: activeSessionId, effort });
    try {
      await updateRuntimeSession(activeSessionId, {
        context: { [SESSION_REASONING_EFFORT_CONTEXT_KEY]: effort },
      });
      onSessionTouched?.();
    } catch (error) {
      setPendingReasoningEffort((current) =>
        current?.sessionId === activeSessionId && current.effort === effort
          ? null
          : current,
      );
      setReasoningEffortError(
        error instanceof Error
          ? error.message
          : "failed to update reasoning effort",
      );
    }
  }

  /**
   * 草稿线程首轮结束后会话才落库：把用户预先选择的档位补写进新会话。
   * 由发送编排在 turn 收尾时调用（仅当本轮之前尚无已物化会话）。
   */
  function flushDraftReasoningEffort(createdSessionId: string) {
    const draftEffort = draftReasoningEffortRef.current;
    if (!createdSessionId || !draftEffort) {
      return;
    }
    draftReasoningEffortRef.current = "";
    setDraftReasoningEffort("");
    setPendingReasoningEffort({ sessionId: createdSessionId, effort: draftEffort });
    void updateRuntimeSession(createdSessionId, {
      context: { [SESSION_REASONING_EFFORT_CONTEXT_KEY]: draftEffort },
    })
      .then(() => {
        onSessionTouched?.();
      })
      .catch((error: unknown) => {
        setPendingReasoningEffort((current) =>
          current?.sessionId === createdSessionId &&
          current.effort === draftEffort
            ? null
            : current,
        );
        setReasoningEffortError(
          error instanceof Error
            ? error.message
            : "failed to update reasoning effort",
        );
      });
  }

  return {
    flushDraftReasoningEffort,
    reasoningEffortDefault: configuredReasoningEffortDefault,
    reasoningEffortError,
    reasoningEffortOptions,
    selectedReasoningEffort,
    setReasoningEffort,
  };
}
