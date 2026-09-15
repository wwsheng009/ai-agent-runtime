// 由 hooks/workspace/use-session-backtrack.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

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
  const [dialog, setDialogState] = useState<SessionBacktrackDialogState>(initialDialogState);
  // 对话框状态的同步镜像：切换「还原模式」必须在同一个事件里读到刚写入的状态。
  // 不能依赖 React 是否同步求值 setState 的 updater——组件 fiber 上存在待处理更新时
  // （workspace 页几乎一直在更新）updater 会被推迟，读到的空 target 会让模式切换直接
  // return，而排队中的 previewing=true 仍然生效 → 确认按钮永久不可点。
  // 回归用例见 hooks/workspace/use-session-backtrack.test.tsx。
  const dialogRef = useRef<SessionBacktrackDialogState>(initialDialogState);
  const setDialog = useCallback(
    (
      update:
        | SessionBacktrackDialogState
        | ((current: SessionBacktrackDialogState) => SessionBacktrackDialogState),
    ) => {
      const next = typeof update === "function" ? update(dialogRef.current) : update;
      dialogRef.current = next;
      setDialogState(next);
    },
    [],
  );
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
  }, [setDialog]);

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
        previewing: true,
        target,
        mode,
        editPrompt: seededEditPrompt,
      });

      try {
        const preview = await loadPreview(sessionId, target, mode);
        // 丢弃过期响应：期间用户可能已切换还原模式，旧模式的预览不能当作新模式的预览。
        setDialog((current) =>
          current.mode === mode && current.target?.messageId === target.messageId
            ? { ...current, previewing: false, preview, error: null }
            : current,
        );
      } catch (error) {
        setDialog((current) =>
          current.mode === mode && current.target?.messageId === target.messageId
            ? {
                ...current,
                previewing: false,
                preview: null,
                error:
                  error instanceof Error ? error.message : "Failed to preview backtrack",
              }
            : current,
        );
      }
    },
    [isResponding, loadPreview, selectedThread?.sessionId, setDialog, targetByMessageId],
  );

  const confirmNavigationSelection = useCallback(() => {
    if (!navigation.active || !navigation.selectedMessageId) {
      return;
    }
    void backtrackToMessage(navigation.selectedMessageId, "conversation");
  }, [backtrackToMessage, navigation.active, navigation.selectedMessageId]);

  // 选中「还原模式」只切换选项并刷新预览，绝不触发应用：
  // 真正的回滚只能由对话框底部的确认按钮触发（confirmBacktrack）。
  const setDialogMode = useCallback(
    async (mode: RuntimeSessionBacktrackMode) => {
      const sessionId = selectedThread?.sessionId;
      // 决策必须基于同步镜像，而不是 setState 的 updater 副作用：
      // updater 被推迟时会读不到 target，直接 return 却留下 previewing=true（按钮卡死）。
      const current = dialogRef.current;
      const target = current.target;
      if (!current.open || !target || !sessionId) {
        // 对话框未打开时只记住选项，不取预览。
        setDialog({ ...current, mode });
        return;
      }
      if (current.mode === mode && current.preview && !current.error) {
        // 同一模式且预览仍有效：不重复请求。
        return;
      }
      setDialog({ ...current, mode, previewing: true, error: null });
      try {
        const preview = await loadPreview(sessionId, target, mode);
        setDialog((current) =>
          // 丢弃过期响应：用户可能已经切到别的模式。
          current.mode === mode
            ? {
                ...current,
                preview,
                previewing: false,
                error: null,
              }
            : current,
        );
      } catch (error) {
        setDialog((current) =>
          current.mode === mode
            ? {
                ...current,
                previewing: false,
                error:
                  error instanceof Error ? error.message : "Failed to preview backtrack",
              }
            : current,
        );
      }
    },
    [loadPreview, selectedThread?.sessionId, setDialog],
  );

  const setPrefillComposer = useCallback((prefillComposer: boolean) => {
    setDialog((current) => ({
      ...current,
      prefillComposer,
    }));
  }, [setDialog]);

  const setEditPrompt = useCallback((editPrompt: string) => {
    setDialog((current) => ({
      ...current,
      editPrompt,
    }));
  }, [setDialog]);

  const confirmBacktrack = useCallback(async () => {
    const sessionId = selectedThread?.sessionId;
    const threadId = selectedThread?.id;
    // 以同步镜像为准：点击瞬间的状态才是用户确认的状态（预览模式、编辑框内容）。
    const current = dialogRef.current;
    const target = current.target;
    // 应用闸门：预览在途或已失败时不允许执行，避免用未确认的模式回滚。
    if (!sessionId || !threadId || !target || current.busy || current.previewing) {
      return;
    }
    if (current.error) {
      return;
    }

    setDialog((current) => ({
      ...current,
      busy: true,
      error: null,
    }));

    try {
      const editPrompt = resolveBacktrackEditPrompt(current.editPrompt, target.fullText);
      const response = await applySessionBacktrack(sessionId, {
        ...buildBacktrackRequestFields(target, sessionId),
        mode: current.mode,
        auto_submit: false,
        ...(editPrompt !== undefined ? { edit_prompt: editPrompt } : {}),
      });
      if (!response.ok || !response.result) {
        throw new Error(response.error || "Failed to apply backtrack");
      }

      await refreshHistory(threadId, sessionId);

      if (current.prefillComposer) {
        const composerPrompt =
          response.result.composer_prompt?.trim() ||
          response.result.edited_prompt?.trim() ||
          current.editPrompt.trim() ||
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
  }, [refreshHistory, selectedThread?.id, selectedThread?.sessionId, setDialog, setDraft]);

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
