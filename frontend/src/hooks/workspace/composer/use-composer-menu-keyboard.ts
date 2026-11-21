// useComposerMenu 的键盘子 hook：Escape / 方向键 / Tab / Enter 语义。

import {
  useCallback,
  type Dispatch,
  type KeyboardEvent,
  type SetStateAction,
} from "react";

import {
  findComposerMenuItem,
  moveComposerMenuActive,
  resolveComposerMenuTab,
  type ComposerMenuLevel,
  type ComposerMenuSnapshot,
} from "@/lib/composer-menu";
import {
  resolveSkillMentionTabExtension,
} from "@/lib/composer-skill-mentions";
import type { ComposerTrigger } from "@/lib/composer-trigger";

import type { ComposerMenuInternalState } from "./use-composer-menu-state";
import type { ComposerMenuNotice } from "./use-composer-menu-types";

type PendingStaleCaretGuardRef = {
  current: { tokenStart: number; query: string } | null;
};

export type ComposerMenuKeyboardOptions = {
  open: boolean;
  query: string;
  value: string;
  activeId: string | null;
  snapshot: ComposerMenuSnapshot;
  trigger: ComposerTrigger | null;
  levelKind: ComposerMenuLevel["kind"];
  backToRoot: () => void;
  close: () => void;
  selectItem: (itemId: string | null) => void;
  onValueChange: (next: string, caret: number) => void;
  pendingStaleCaretGuardRef: PendingStaleCaretGuardRef;
  setState: Dispatch<SetStateAction<ComposerMenuInternalState>>;
  setNotice: Dispatch<SetStateAction<ComposerMenuNotice | null>>;
};

export function useComposerMenuKeyboard({
  open,
  query,
  value,
  activeId,
  snapshot,
  trigger,
  levelKind,
  backToRoot,
  close,
  selectItem,
  onValueChange,
  pendingStaleCaretGuardRef,
  setState,
  setNotice,
}: ComposerMenuKeyboardOptions): (
  event: KeyboardEvent<HTMLTextAreaElement>,
) => boolean {
  return useCallback(
    (event: KeyboardEvent<HTMLTextAreaElement>): boolean => {
      if (!open) {
        return false;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        if (levelKind === "group") {
          backToRoot();
        } else {
          close();
        }
        return true;
      }
      if (event.key === "ArrowLeft" && levelKind === "group") {
        event.preventDefault();
        backToRoot();
        return true;
      }
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const delta = event.key === "ArrowDown" ? 1 : -1;
        setState((previous) => ({
          ...previous,
          activeId: moveComposerMenuActive(snapshot.items, activeId, delta),
        }));
        return true;
      }
      if (event.key === "ArrowRight") {
        const item = findComposerMenuItem(snapshot.items, activeId);
        if (!item || item.level !== "launcher") {
          return false;
        }
        event.preventDefault();
        setState((previous) => ({
          ...previous,
          level: { kind: "group", groupId: item.groupId },
          activeId: null,
        }));
        return true;
      }
      if (event.key === "Tab") {
        // `$` 多候选：Tab 先加深公共前缀（不落空格、菜单保持打开），与 TUI 一致
        // （前缀子集判定在 lib）；唯一候选/不可再加深时回落到通用 pick。
        if (trigger?.kind === "skill") {
          const extension = resolveSkillMentionTabExtension(
            value,
            trigger,
            snapshot.items,
          );
          if (extension) {
            event.preventDefault();
            pendingStaleCaretGuardRef.current = {
              tokenStart: trigger.start,
              query: extension.trigger.query,
            };
            onValueChange(extension.value, extension.caret);
            setState((previous) => ({
              ...previous,
              trigger: extension.trigger,
            }));
            return true;
          }
        }
        const intent = resolveComposerMenuTab(snapshot.items, activeId);
        if (intent === "pass") {
          return false;
        }
        event.preventDefault();
        if (intent === "drill") {
          const item = findComposerMenuItem(snapshot.items, activeId);
          if (item) {
            setState((previous) => ({
              ...previous,
              level: { kind: "group", groupId: item.groupId },
              activeId: null,
            }));
          }
          return true;
        }
        selectItem(activeId);
        return true;
      }
      if (
        event.key === "Enter" &&
        !event.metaKey &&
        !event.ctrlKey &&
        !event.shiftKey
      ) {
        if (snapshot.items.length === 0) {
          // 命令行绝不静默降级：无候选时 Enter 阻塞提交并提示，而不是当普通消息发送。
          if (trigger?.kind === "slash") {
            event.preventDefault();
            setNotice(
              query.length === 0
                ? { kind: "incomplete-command" }
                : { kind: "unknown-command", name: query },
            );
            return true;
          }
          // `@` 引用同语义：候选为空（加载中/无命中/失败）时不得把半截 token 当普通消息发出；
          // 用户可先 Esc 关闭菜单再提交（此时 open=false，不进本分支）。
          if (trigger?.kind === "reference") {
            event.preventDefault();
            return true;
          }
          // `$` 提及例外：未知技能名是合法普通文本（对齐 TUI/Codex「未命中不报错」），
          // 空候选时放行提交，绝不把 `$foo` 当成必须修复的引用。
          return false;
        }
        event.preventDefault();
        selectItem(activeId);
        return true;
      }
      return false;
    },
    [
      activeId,
      backToRoot,
      close,
      levelKind,
      onValueChange,
      open,
      pendingStaleCaretGuardRef,
      query,
      selectItem,
      setNotice,
      setState,
      snapshot.items,
      trigger,
      value,
    ],
  );
}