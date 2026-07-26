import {
  type Dispatch,
  type SetStateAction,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react";

import type { ChatMessage, Thread } from "@/data/mock";
import {
  applySessionBacktrack,
  getSessionHistory,
  previewSessionBacktrack,
  type RuntimeSessionBacktrackTombstone,
  type RuntimeSessionBacktrackMode,
  type RuntimeSessionBacktrackResult,
  type SessionHistoryResponse,
} from "@/lib/runtime-api";

export type SessionBacktrackTarget = {
  messageId: string;
  messageIndex: number;
  userTurnIndex: number;
  preview: string;
  /** Full original user text used to seed the edit box (not truncated). */
  fullText: string;
};

export type SessionBacktrackDialogState = {
  open: boolean;
  busy: boolean;
  error: string | null;
  target: SessionBacktrackTarget | null;
  preview: RuntimeSessionBacktrackResult | null;
  mode: RuntimeSessionBacktrackMode;
  prefillComposer: boolean;
  /** Editable prompt; empty means keep original anchor text for prefill. */
  editPrompt: string;
};

export type SessionBacktrackNavigationState = {
  active: boolean;
  selectedMessageId: string | null;
};

type UseSessionBacktrackOptions = {
  applySessionHistoryToThread: (
    thread: Thread,
    response: SessionHistoryResponse,
  ) => Thread;
  isResponding: boolean;
  selectedThread: Thread | undefined;
  setDraft: (value: string) => void;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  /** Current composer draft; used for empty-composer Esc navigation. */
  draft?: string;
};

const initialDialogState: SessionBacktrackDialogState = {
  open: false,
  busy: false,
  error: null,
  target: null,
  preview: null,
  mode: "conversation",
  prefillComposer: true,
  editPrompt: "",
};

const initialNavigationState: SessionBacktrackNavigationState = {
  active: false,
  selectedMessageId: null,
};

/** When edit text differs from the original, send edit_prompt to the runtime. */
export function resolveBacktrackEditPrompt(
  editPrompt: string,
  originalText: string,
): string | undefined {
  const edited = editPrompt.replace(/\r\n/g, "\n");
  const original = originalText.replace(/\r\n/g, "\n");
  if (edited.trim() === original.trim()) {
    return undefined;
  }
  // Allow intentionally clearing to empty only when original was non-empty.
  if (!edited.trim() && !original.trim()) {
    return undefined;
  }
  return edited;
}

/**
 * Seed the backtrack dialog edit box.
 * Inline transcript edit passes options.editPrompt; bare Backtrack uses original fullText.
 */
export function resolveSeededBacktrackEditPrompt(
  options: { editPrompt?: string } | undefined,
  originalText: string,
): string {
  return typeof options?.editPrompt === "string" ? options.editPrompt : originalText;
}

export function countUserTurnIndex(
  messages: ChatMessage[],
  messageId: string,
): { userTurnIndex: number; messageIndex: number } | null {
  let userTurnIndex = -1;
  for (let i = 0; i < messages.length; i++) {
    const message = messages[i];
    if (message.role === "user") {
      userTurnIndex += 1;
    }
    if (message.id === messageId) {
      if (message.role !== "user" || userTurnIndex < 0) {
        return null;
      }
      return { userTurnIndex, messageIndex: i };
    }
  }
  return null;
}

export function extractUserMessageText(message: ChatMessage): string {
  return message.segments
    .filter(
      (segment): segment is Extract<ChatMessage["segments"][number], { type: "text" }> =>
        segment.type === "text",
    )
    .map((segment) => segment.content)
    .join("\n")
    .replace(/\r\n/g, "\n")
    .trim();
}

export function extractUserMessagePreview(message: ChatMessage): string {
  const text = extractUserMessageText(message).replace(/\s+/g, " ").trim();
  if (!text) {
    return "(empty)";
  }
  return text.length > 120 ? `${text.slice(0, 117).trimEnd()}...` : text;
}

export function resolveUserTurnTargets(messages: ChatMessage[]): SessionBacktrackTarget[] {
  const targets: SessionBacktrackTarget[] = [];
  messages.forEach((message, messageIndex) => {
    if (message.role !== "user") {
      return;
    }
    const fullText = extractUserMessageText(message);
    targets.push({
      messageId: message.id,
      messageIndex,
      userTurnIndex: targets.length,
      preview: extractUserMessagePreview(message),
      fullText,
    });
  });
  return targets;
}

/** Prefer the latest user turn when entering transcript navigation. */
export function resolveInitialBacktrackNavigationId(
  targets: SessionBacktrackTarget[],
  preferredMessageId?: string | null,
): string | null {
  if (targets.length === 0) {
    return null;
  }
  if (preferredMessageId) {
    const preferred = targets.find((target) => target.messageId === preferredMessageId);
    if (preferred) {
      return preferred.messageId;
    }
  }
  return targets[targets.length - 1]?.messageId ?? null;
}

/** Move selection within user-turn targets; delta of -1 is older, +1 is newer. */
export function moveBacktrackNavigationSelection(
  targets: SessionBacktrackTarget[],
  selectedMessageId: string | null,
  delta: number,
): string | null {
  if (targets.length === 0) {
    return null;
  }
  const currentIndex = targets.findIndex((target) => target.messageId === selectedMessageId);
  const fallbackIndex = targets.length - 1;
  const baseIndex = currentIndex >= 0 ? currentIndex : fallbackIndex;
  const nextIndex = Math.max(0, Math.min(targets.length - 1, baseIndex + delta));
  return targets[nextIndex]?.messageId ?? null;
}

export function isEditableKeyboardTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  if (target.isContentEditable) {
    return true;
  }
  const tag = target.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}

/** Prefer durable runtime message_id; skip synthetic UI ids like `${session}-history-N`. */
export function resolveBacktrackMessageSelector(
  messageId: string,
  sessionId?: string,
): string | undefined {
  const id = messageId.trim();
  if (!id) {
    return undefined;
  }
  if (id.startsWith("msg_")) {
    return id;
  }
  if (sessionId && id.startsWith(`${sessionId}-history-`)) {
    return undefined;
  }
  if (/-history-\d+$/.test(id)) {
    return undefined;
  }
  // Accept legacy durable ids already present in metadata.
  return id;
}

export function formatBacktrackApplyNotice(
  result: RuntimeSessionBacktrackResult,
): string {
  const tombstone = result.tombstone;
  const removedMessages =
    tombstone?.removed_message_count ?? result.removed_message_count ?? 0;
  const removedTurns =
    tombstone?.removed_user_turns ?? result.removed_user_turns ?? 0;
  const mode = (tombstone?.mode || result.mode || "conversation").trim();
  const turnIndex =
    typeof tombstone?.user_turn_index === "number"
      ? tombstone.user_turn_index
      : result.user_turn_index;
  const parts = [
    `Backtracked to turn ${turnIndex}`,
    `removed ${removedMessages} message${removedMessages === 1 ? "" : "s"}`,
    `${removedTurns} later user turn${removedTurns === 1 ? "" : "s"}`,
    `mode=${mode}`,
  ];
  if (tombstone?.id) {
    parts.push(`audit ${tombstone.id.slice(0, 12)}`);
  }
  if (result.code_restore?.checkpoint_id) {
    parts.push(`files via ${result.code_restore.checkpoint_id.slice(0, 12)}`);
  } else if (result.base_checkpoint_id) {
    parts.push(`base ${result.base_checkpoint_id.slice(0, 12)}`);
  }
  return `${parts[0]} (${parts.slice(1).join(", ")}).`;
}

export function formatBacktrackAuditEntry(
  entry: RuntimeSessionBacktrackTombstone,
): {
  title: string;
  detail: string;
  preview: string;
} {
  const mode = (entry.mode || "conversation").trim() || "conversation";
  const title = `Turn ${entry.user_turn_index} · ${mode}`;
  const detail = `Removed ${entry.removed_message_count} message${
    entry.removed_message_count === 1 ? "" : "s"
  } / ${entry.removed_user_turns} later user turn${
    entry.removed_user_turns === 1 ? "" : "s"
  } · kept ${entry.truncated_to_message_count}`;
  const preview = (entry.anchor_preview || entry.reason || "").trim() || "(empty)";
  return { title, detail, preview };
}

function buildBacktrackRequestFields(
  target: SessionBacktrackTarget,
  sessionId?: string,
): {
  message_id?: string;
  user_turn_index?: number;
  message_index?: number;
} {
  const messageId = resolveBacktrackMessageSelector(target.messageId, sessionId);
  // Prefer message_id alone so index drift after history refresh cannot fail a stable anchor.
  if (messageId) {
    return { message_id: messageId };
  }
  return {
    user_turn_index: target.userTurnIndex,
    message_index: target.messageIndex,
  };
}

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

  // Transcript keyboard navigation: Esc enter/exit, arrows cycle, Enter confirm.
  useEffect(() => {
    if (typeof window === "undefined") {
      return;
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing) {
        return;
      }
      if (event.metaKey || event.ctrlKey || event.altKey) {
        return;
      }

      if (dialog.open) {
        return;
      }

      // Do not steal keys from other modal dialogs (settings, artifacts, etc.).
      const modalOpen =
        typeof document !== "undefined" &&
        Boolean(document.querySelector('[aria-modal="true"]'));
      if (modalOpen && !navigation.active) {
        return;
      }

      if (navigation.active) {
        if (event.key === "Escape") {
          event.preventDefault();
          exitNavigation();
          return;
        }
        if (event.key === "ArrowUp" || event.key === "k" || event.key === "K") {
          event.preventDefault();
          moveNavigation(-1);
          return;
        }
        if (event.key === "ArrowDown" || event.key === "j" || event.key === "J") {
          event.preventDefault();
          moveNavigation(1);
          return;
        }
        if (event.key === "Enter" && !event.shiftKey) {
          event.preventDefault();
          confirmNavigationSelection();
        }
        return;
      }

      if (event.key !== "Escape") {
        return;
      }
      if (!canBacktrack || targets.length === 0) {
        return;
      }

      // Codex-style: bare Esc with empty composer enters turn selection.
      // Never steal Esc from non-empty focused inputs/textareas (composer or
      // transcript inline edit). Empty focused fields may still enter nav.
      const draftEmpty = draft.trim().length === 0;
      if (!draftEmpty) {
        return;
      }
      if (isEditableKeyboardTarget(event.target)) {
        const focusedValue =
          event.target instanceof HTMLInputElement ||
          event.target instanceof HTMLTextAreaElement
            ? event.target.value
            : "";
        if (focusedValue.trim().length > 0) {
          return;
        }
      }

      event.preventDefault();
      enterNavigation();
    };

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [
    canBacktrack,
    confirmNavigationSelection,
    dialog.open,
    draft,
    enterNavigation,
    exitNavigation,
    moveNavigation,
    navigation.active,
    targets.length,
  ]);

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
