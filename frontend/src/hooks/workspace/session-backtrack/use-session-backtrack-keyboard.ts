// 由 hooks/workspace/use-session-backtrack.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect } from "react";

import { isEditableKeyboardTarget } from "./helpers";

type UseSessionBacktrackKeyboardOptions = {
  canBacktrack: boolean;
  confirmNavigationSelection: () => void;
  dialogOpen: boolean;
  draft: string;
  enterNavigation: () => void;
  exitNavigation: () => void;
  moveNavigation: (delta: number) => void;
  navigationActive: boolean;
  targetCount: number;
};

/** Transcript keyboard navigation: Esc enter/exit, arrows cycle, Enter confirm. */
export function useSessionBacktrackKeyboard({
  canBacktrack,
  confirmNavigationSelection,
  dialogOpen,
  draft,
  enterNavigation,
  exitNavigation,
  moveNavigation,
  navigationActive,
  targetCount,
}: UseSessionBacktrackKeyboardOptions) {
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

      if (dialogOpen) {
        return;
      }

      // Do not steal keys from other modal dialogs (settings, artifacts, etc.).
      const modalOpen =
        typeof document !== "undefined" &&
        Boolean(document.querySelector('[aria-modal="true"]'));
      if (modalOpen && !navigationActive) {
        return;
      }

      if (navigationActive) {
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
      if (!canBacktrack || targetCount === 0) {
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
    dialogOpen,
    draft,
    enterNavigation,
    exitNavigation,
    moveNavigation,
    navigationActive,
    targetCount,
  ]);
}
