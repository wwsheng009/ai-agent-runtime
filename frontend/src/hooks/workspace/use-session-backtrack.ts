// 由 hooks/workspace/use-session-backtrack.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { useCallback, useEffect, useMemo, useState } from "react";

import {
  applySessionBacktrack,
  getSessionHistory,
  previewSessionBacktrack,
  type RuntimeSessionBacktrackMode,
} from "@/lib/runtime-api";

import {
  buildBacktrackRequestFields,
  formatBacktrackApplyNotice,
} from "@/hooks/workspace/session-backtrack/format";
import {
  moveBacktrackNavigationSelection,
  resolveBacktrackEditPrompt,
  resolveInitialBacktrackNavigationId,
  resolveSeededBacktrackEditPrompt,
  resolveUserTurnTargets,
} from "@/hooks/workspace/session-backtrack/helpers";
import {
  type SessionBacktrackDialogState,
  type SessionBacktrackNavigationState,
  type SessionBacktrackTarget,
  type UseSessionBacktrackOptions,
  initialDialogState,
  initialNavigationState,
} from "@/hooks/workspace/session-backtrack/types";
import {
  useSessionBacktrackKeyboard,
} from "@/hooks/workspace/session-backtrack/use-session-backtrack-keyboard";

export type {
  SessionBacktrackDialogState,
  SessionBacktrackNavigationState,
  SessionBacktrackTarget,
} from "@/hooks/workspace/session-backtrack/types";

export {
  countUserTurnIndex,
  extractUserMessagePreview,
  extractUserMessageText,
  isEditableKeyboardTarget,
  moveBacktrackNavigationSelection,
  resolveBacktrackEditPrompt,
  resolveBacktrackMessageSelector,
  resolveInitialBacktrackNavigationId,
  resolveSeededBacktrackEditPrompt,
  resolveUserTurnTargets,
} from "@/hooks/workspace/session-backtrack/helpers";

export {
  formatBacktrackApplyNotice,
  formatBacktrackAuditEntry,
} from "@/hooks/workspace/session-backtrack/format";

export function useSessionBacktrack({
  applySessionHistoryToThread,
  isResponding,
  selectedThread,
  setDraft,
  setThreads,
  draft = "",
}: UseSessionBacktrackOptions) {
  const [dialog, setDialog] = useState<SessionBacktrackDialogState>(initialDialogState);
  const [navigation, setNavigation] =
    useState<SessionBacktrackNavigationState>(initialNavigationState);
  const [bannerError, setBannerError] = useState<string | null>(null);
  const [bannerNotice, setBannerNotice] = useState<string | null>(null);

  const targets = useMemo(
    () => resolveUserTurnTargets(selectedThread?.messages ?? []),
    [selectedThread?.messages],
  );
  const targetByMessageId = useMemo(
    () => new Map(targets.map((target) => [target.messageId, target])),
    [targets],
  );

  const canBacktrack =
    Boolean(selectedThread?.sessionId) &&
    !isResponding &&
    selectedThread?.transport !== "error";

  const refreshHistory = useCallback(
    async (threadId: string, sessionId: string) => {
      const response = await getSessionHistory(sessionId);
      setThreads((current) =>
        current.map((thread) =>
          thread.id === threadId ? applySessionHistoryToThread(thread, response) : thread,
        ),
      );
    },
    [applySessionHistoryToThread, setThreads],
  );

  const closeDialog = useCallback(() => {
    setDialog(initialDialogState);
  }, []);

  const exitNavigation = useCallback(() => {
    setNavigation(initialNavigationState);
  }, []);

  const enterNavigation = useCallback(
    (preferredMessageId?: string | null) => {
      if (!canBacktrack || targets.length === 0 || dialog.open) {
        if (targets.length === 0) {
          setBannerError("No user turns available to backtrack.");
          setBannerNotice(null);
        }
        return;
      }
      const selectedMessageId = resolveInitialBacktrackNavigationId(
        targets,
        preferredMessageId ?? navigation.selectedMessageId,
      );
      if (!selectedMessageId) {
        return;
      }
      setBannerError(null);
      setBannerNotice(null);
      setNavigation({
        active: true,
        selectedMessageId,
      });
    },
    [canBacktrack, dialog.open, navigation.selectedMessageId, targets],
  );

  const selectNavigationMessage = useCallback(
    (messageId: string) => {
      if (!targetByMessageId.has(messageId)) {
        return;
      }
      setNavigation({
        active: true,
        selectedMessageId: messageId,
      });
    },
    [targetByMessageId],
  );

  const moveNavigation = useCallback(
    (delta: number) => {
      setNavigation((current) => {
        if (!current.active) {
          return current;
        }
        const nextId = moveBacktrackNavigationSelection(
          targets,
          current.selectedMessageId,
          delta,
        );
        if (!nextId || nextId === current.selectedMessageId) {
          return current;
        }
        return {
          active: true,
          selectedMessageId: nextId,
        };
      });
    },
    [targets],
  );

  const loadPreview = useCallback(
    async (
      sessionId: string,
      target: SessionBacktrackTarget,
      mode: RuntimeSessionBacktrackMode,
    ) => {
      const response = await previewSessionBacktrack(sessionId, {
        ...buildBacktrackRequestFields(target, sessionId),
        mode,
      });
      if (!response.ok || !response.result) {
        throw new Error(response.error || "Failed to preview backtrack");
      }
      return response.result;
    },
    [],
  );

  const backtrackToMessage = useCallback(
    async (
      messageId: string,
      mode: RuntimeSessionBacktrackMode = "conversation",
      options?: { editPrompt?: string },
    ) => {
      const sessionId = selectedThread?.sessionId;
      if (!sessionId) {
        setBannerError("Session is not attached.");
        setBannerNotice(null);
        return;
      }
      if (isResponding) {
        setBannerError("Cannot backtrack while a response is active.");
        setBannerNotice(null);
        return;
      }
      const target = targetByMessageId.get(messageId);
      if (!target) {
        setBannerError("Only user messages can be used as backtrack anchors.");
        setBannerNotice(null);
        return;
      }

      const seededEditPrompt = resolveSeededBacktrackEditPrompt(options, target.fullText);

      setBannerError(null);
      setBannerNotice(null);
      setNavigation(initialNavigationState);
      setDialog({
        ...initialDialogState,
        open: true,
        busy: true,
        target,
        mode,
        editPrompt: seededEditPrompt,
      });

      try {
        const preview = await loadPreview(sessionId, target, mode);
        setDialog((current) => ({
          ...current,
          busy: false,
          preview,
          error: null,
        }));
      } catch (error) {
        setDialog((current) => ({
          ...current,
          busy: false,
          preview: null,
          error: error instanceof Error ? error.message : "Failed to preview backtrack",
        }));
      }
    },
    [isResponding, loadPreview, selectedThread?.sessionId, targetByMessageId],
  );

  const confirmNavigationSelection = useCallback(() => {
    if (!navigation.active || !navigation.selectedMessageId) {
      return;
    }
    void backtrackToMessage(navigation.selectedMessageId, "conversation");
  }, [backtrackToMessage, navigation.active, navigation.selectedMessageId]);

  const setDialogMode = useCallback(
    async (mode: RuntimeSessionBacktrackMode) => {
      const sessionId = selectedThread?.sessionId;
      let target: SessionBacktrackTarget | null = null;
      let shouldPreview = false;
      setDialog((current) => {
        target = current.target;
        if (!current.open || !current.target || !sessionId) {
          return { ...current, mode };
        }
        shouldPreview = true;
        return {
          ...current,
          mode,
          busy: true,
          error: null,
        };
      });

      if (!sessionId || !target || !shouldPreview) {
        return;
      }
      try {
        const preview = await loadPreview(sessionId, target, mode);
        setDialog((current) => ({
          ...current,
          mode,
          preview,
          busy: false,
          error: null,
        }));
      } catch (error) {
        setDialog((current) => ({
          ...current,
          mode,
          busy: false,
          error: error instanceof Error ? error.message : "Failed to preview backtrack",
        }));
      }
    },
    [loadPreview, selectedThread?.sessionId],
  );

  const setPrefillComposer = useCallback((prefillComposer: boolean) => {
    setDialog((current) => ({
      ...current,
      prefillComposer,
    }));
  }, []);

  const setEditPrompt = useCallback((editPrompt: string) => {
    setDialog((current) => ({
      ...current,
      editPrompt,
    }));
  }, []);

  const confirmBacktrack = useCallback(async () => {
    const sessionId = selectedThread?.sessionId;
    const threadId = selectedThread?.id;
    const target = dialog.target;
    if (!sessionId || !threadId || !target || dialog.busy) {
      return;
    }

    setDialog((current) => ({
      ...current,
      busy: true,
      error: null,
    }));

    try {
      const editPrompt = resolveBacktrackEditPrompt(dialog.editPrompt, target.fullText);
      const response = await applySessionBacktrack(sessionId, {
        ...buildBacktrackRequestFields(target, sessionId),
        mode: dialog.mode,
        auto_submit: false,
        ...(editPrompt !== undefined ? { edit_prompt: editPrompt } : {}),
      });
      if (!response.ok || !response.result) {
        throw new Error(response.error || "Failed to apply backtrack");
      }

      await refreshHistory(threadId, sessionId);

      if (dialog.prefillComposer) {
        const composerPrompt =
          response.result.composer_prompt?.trim() ||
          response.result.edited_prompt?.trim() ||
          dialog.editPrompt.trim() ||
          target.fullText.trim() ||
          target.preview;
        if (composerPrompt && composerPrompt !== "(empty)") {
          setDraft(composerPrompt);
        }
      }

      if (response.result.warnings && response.result.warnings.length > 0) {
        setBannerError(response.result.warnings.join("; "));
        setBannerNotice(formatBacktrackApplyNotice(response.result));
      } else {
        setBannerError(null);
        setBannerNotice(formatBacktrackApplyNotice(response.result));
      }
      setDialog(initialDialogState);
    } catch (error) {
      setDialog((current) => ({
        ...current,
        busy: false,
        error: error instanceof Error ? error.message : "Backtrack failed",
      }));
    }
  }, [
    dialog.busy,
    dialog.editPrompt,
    dialog.mode,
    dialog.prefillComposer,
    dialog.target,
    refreshHistory,
    selectedThread?.id,
    selectedThread?.sessionId,
    setDraft,
  ]);

  // Keep navigation selection valid when history changes; exit when backtrack is unavailable.
  useEffect(() => {
    if (!navigation.active) {
      return;
    }
    if (!canBacktrack || targets.length === 0 || dialog.open) {
      setNavigation(initialNavigationState);
      return;
    }
    if (
      navigation.selectedMessageId &&
      targets.some((target) => target.messageId === navigation.selectedMessageId)
    ) {
      return;
    }
    setNavigation({
      active: true,
      selectedMessageId: resolveInitialBacktrackNavigationId(targets),
    });
  }, [canBacktrack, dialog.open, navigation.active, navigation.selectedMessageId, targets]);

  useSessionBacktrackKeyboard({
    canBacktrack,
    confirmNavigationSelection,
    dialogOpen: dialog.open,
    draft,
    enterNavigation,
    exitNavigation,
    moveNavigation,
    navigationActive: navigation.active,
    targetCount: targets.length,
  });

  return {
    backtrackDialog: dialog,
    backtrackError: bannerError,
    backtrackNotice: bannerNotice,
    backtrackPendingMessageId: dialog.open && dialog.busy ? dialog.target?.messageId ?? null : null,
    backtrackNavigationActive: navigation.active,
    backtrackSelectedMessageId: navigation.active ? navigation.selectedMessageId : null,
    backtrackTargets: targets,
    backtrackToMessage,
    canBacktrack,
    closeBacktrackDialog: closeDialog,
    confirmBacktrack,
    enterBacktrackNavigation: enterNavigation,
    exitBacktrackNavigation: exitNavigation,
    selectBacktrackNavigationMessage: selectNavigationMessage,
    setBacktrackEditPrompt: setEditPrompt,
    setBacktrackMode: setDialogMode,
    setBacktrackPrefill: setPrefillComposer,
  };
}
