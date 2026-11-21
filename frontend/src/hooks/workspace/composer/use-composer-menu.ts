// P1-4 子片 3：Composer 触发菜单的 owner hook。
// 三入口同源：`/`（命令）、`@`（引用）、`+` 按钮（全部）共用同一份菜单模型与派发路径。
//
// 公开类型在 use-composer-menu-types.ts，键盘子 hook 在 use-composer-menu-keyboard.ts；
// 本文件保留状态机、菜单快照派生、补全与命令派发，并作为公开 barrel 保持既有 import 路径。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  classifyComposerSubmit,
  createComposerCommandRegistry,
  findComposerCommand,
  parseComposerCommandLine,
  type ComposerCommand,
  type ComposerSubmitClassification,
} from "@/lib/composer-commands";
import {
  buildComposerMenu,
  clampComposerMenuActive,
  findComposerMenuItem,
  type ComposerCommandOptionsSource,
  type ComposerMenuMode,
} from "@/lib/composer-menu";
import {
  applyComposerSkillMentionInsertion,
  applyComposerTriggerInsertion,
  composerReferenceText,
  composerSkillMentionText,
  detectComposerTrigger,
} from "@/lib/composer-trigger";
import { isSkillMentionCaretEcho } from "@/lib/composer-skill-mentions";

import { useComposerCaretInsertion } from "./use-composer-caret-insertion";
import { useComposerMenuKeyboard } from "./use-composer-menu-keyboard";
import {
  INITIAL_COMPOSER_MENU_STATE,
  type ComposerMenuInternalState,
} from "./use-composer-menu-state";
import type {
  ComposerMenuController,
  ComposerMenuNotice,
  ComposerMenuState,
  UseComposerMenuOptions,
} from "./use-composer-menu-types";

export type {
  ComposerMenuController,
  ComposerMenuNotice,
  ComposerMenuState,
  UseComposerMenuOptions,
};

export const COMPOSER_MENU_LISTBOX_ID = "composer-command-menu";

export function composerMenuItemDomId(itemId: string): string {
  return `composer-menu-option-${itemId.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
}

export function useComposerMenu({
  value,
  commands,
  referenceGroups,
  skillGroups,
  hasAttachAction,
  attachLabel,
  workspaceSelection,
  onWorkspaceSelect,
  onValueChange,
  onAttachRequest,
  onMenuStateChange,
  onCommand,
}: UseComposerMenuOptions): ComposerMenuController {
  const [state, setState] = useState<ComposerMenuInternalState>(
    INITIAL_COMPOSER_MENU_STATE,
  );
  const [notice, setNotice] = useState<ComposerMenuNotice | null>(null);
  // `+`（manual）菜单的插入点：无触发 token 时技能/引用按最近光标落 token。
  const { rememberCaret, insertAtCaret } = useComposerCaretInsertion({
    value,
    onValueChange,
  });

  const registry = useMemo(
    () => createComposerCommandRegistry(commands),
    [commands],
  );

  const mode: ComposerMenuMode = state.manual
    ? "all"
    : state.trigger?.kind === "slash"
      ? "commands"
      : state.trigger?.kind === "skill"
        ? "skills"
        : "references";
  const query = state.trigger?.query ?? "";
  const open =
    state.manual ||
    (state.trigger !== null && state.dismissedKey !== state.trigger.key);

  // 命令专属候选（P2-7 子片 3）：候选来自命令定义（由宿主用真实目录组装），
  // 菜单层只负责展示与派发，不自己编造选项。
  const commandOptionsSource =
    useMemo<ComposerCommandOptionsSource | null>(() => {
      if (state.level.kind !== "command-options") {
        return null;
      }
      const command = registry.byKey.get(state.level.commandKey);
      if (!command) {
        return null;
      }
      return {
        commandKey: command.key,
        commandName: command.name,
        label: `/${command.name}`,
        options: command.options ?? [],
      };
    }, [registry, state.level]);

  const snapshot = useMemo(
    () =>
      buildComposerMenu({
        mode,
        level: state.level,
        query,
        commands: registry.commands,
        referenceGroups,
        skillGroups,
        commandOptions: commandOptionsSource,
        hasAttachAction,
        attachLabel,
        workspaceSelection,
      }),
    [
      mode,
      state.level,
      query,
      registry.commands,
      referenceGroups,
      skillGroups,
      commandOptionsSource,
      hasAttachAction,
      attachLabel,
      workspaceSelection,
    ],
  );

  const activeId = open
    ? clampComposerMenuActive(snapshot.items, state.activeId)
    : null;
  const activeDescendantId = activeId ? composerMenuItemDomId(activeId) : null;
  const commandLine = parseComposerCommandLine(value) !== null;

  // 回调身份变化不触发重放（宿主可传内联箭头函数）；只有「打开态/模式/查询」变化才回调。
  const onMenuStateChangeRef = useRef(onMenuStateChange);
  useEffect(() => {
    onMenuStateChangeRef.current = onMenuStateChange;
  });
  useEffect(() => {
    onMenuStateChangeRef.current?.({ open, mode, query });
  }, [open, mode, query]);

  const close = useCallback(() => {
    setState((previous) => ({
      ...previous,
      manual: false,
      dismissedKey: previous.trigger?.key ?? null,
      level: { kind: "root" },
      activeId: null,
    }));
  }, []);

  const backToRoot = useCallback(() => {
    setState((previous) => {
      if (previous.level.kind !== "group") {
        return previous;
      }
      return {
        ...previous,
        level: { kind: "root" },
        activeId: null,
      };
    });
  }, []);

  const handleValueChange = useCallback(
    (next: string, caret: number) => {
      rememberCaret(caret);
      onValueChange(next, caret);
      setNotice(null);
      setState((previous) => ({
        ...previous,
        trigger: detectComposerTrigger(next, caret),
        level: { kind: "root" },
        activeId: null,
      }));
    },
    [onValueChange, rememberCaret],
  );

  // 程序化写入后可能补发携带旧 token 的 select 回声：只在回声形态（同起点 + query 严格回退）时忽略。
  const pendingStaleCaretGuardRef = useRef<{
    tokenStart: number;
    query: string;
  } | null>(null);

  const handleCaretChange = useCallback(
    (caret: number) => {
      const trigger = detectComposerTrigger(value, caret);
      const guard = pendingStaleCaretGuardRef.current;
      if (guard) {
        pendingStaleCaretGuardRef.current = null;
        if (isSkillMentionCaretEcho(trigger, guard)) {
          return;
        }
      }
      rememberCaret(caret);
      setState((previous) => {
        if (previous.trigger?.key === trigger?.key) {
          return previous;
        }
        return {
          ...previous,
          trigger,
          level: { kind: "root" },
          activeId: null,
        };
      });
    },
    [rememberCaret, value],
  );

  const dispatchCommand = useCallback(
    (
      command: ComposerCommand,
      args: string,
      source: "pick" | "submit",
    ): boolean => {
      const handled = onCommand?.(command, args, source) === true;
      if (!handled) {
        setNotice({ kind: "no-executor", name: command.name });
      }
      return handled;
    },
    [onCommand],
  );

  const completeWith = useCallback(
    (
      inserted: string,
      keepMenu: boolean,
      /** 无触发 token 时的整串替换（如第二级候选点选后清空命令行）。 */
      fallbackValue?: string,
    ) => {
      const trigger = state.trigger;
      if (trigger) {
        const next = applyComposerTriggerInsertion(value, trigger, inserted);
        onValueChange(next.value, next.caret);
      } else if (fallbackValue !== undefined) {
        onValueChange(fallbackValue, fallbackValue.length);
      }
      setState((previous) => ({
        ...previous,
        manual: keepMenu,
        trigger: null,
        // 补全后浏览器可能补发一次携带旧 token 的 select/caret 事件：
        // 把「已完成的 token key」记为 dismissed，防止菜单被旧 token 立刻重新打开。
        dismissedKey: trigger?.key ?? null,
        level: { kind: "root" },
        activeId: null,
      }));
    },
    [onValueChange, state.trigger, value],
  );

  // 技能提及补全：`$` 路径替换触发 token；`+` 路径无触发 token，按最近光标插入 `$name `。
  const completeWithSkill = useCallback(
    (skillName: string) => {
      const trigger = state.trigger;
      if (trigger && skillName.trim().length > 0) {
        const next = applyComposerSkillMentionInsertion(
          value,
          trigger,
          skillName,
        );
        onValueChange(next.value, next.caret);
      } else if (skillName.trim().length > 0) {
        insertAtCaret(composerSkillMentionText(skillName), {
          trailingSpace: true,
        });
      }
      setState((previous) => ({
        ...previous,
        manual: false,
        trigger: null,
        // 同 completeWith：抑制补全后的旧 token 重开（多提及靠用户继续输入新 token）。
        dismissedKey: trigger?.key ?? null,
        level: { kind: "root" },
        activeId: null,
      }));
    },
    [insertAtCaret, onValueChange, state.trigger, value],
  );

  const selectItem = useCallback(
    (itemId: string | null) => {
      const item = findComposerMenuItem(snapshot.items, itemId ?? activeId);
      if (!item) {
        return;
      }
      setNotice(null);
      // 分组 launcher：Enter / Tab / 点选统一语义 = 下钻展开该组。
      // 必须先于具体 action dispatch；launcher 会复用 attach/workspace
      // action 作为分类标识，不能在根层直接执行叶子动作。
      if (item.level === "launcher") {
        setState((previous) => ({
          ...previous,
          level: { kind: "group", groupId: item.groupId },
          activeId: null,
        }));
        return;
      }
      if (item.action.kind === "attach") {
        onAttachRequest();
        close();
        return;
      }
      if (item.action.kind === "workspace") {
        onWorkspaceSelect?.(item.action.directoryId);
        close();
        return;
      }
      if (item.action.kind === "reference") {
        const reference = composerReferenceText(item.action.text);
        if (state.trigger) {
          completeWith(reference, false);
        } else {
          // `+`（manual）路径：无触发 token，按最近光标插入 `@path` 后关闭菜单。
          insertAtCaret(reference);
          close();
        }
        return;
      }
      // `$` 技能提及：补全为 `$name ` 并关闭菜单（多提及 = 重复触发各自补全）。
      if (item.action.kind === "skill") {
        completeWithSkill(item.action.name);
        return;
      }
      // 命令专属候选：补全为「命令 + 参数」并立即派发（点选即执行，与命令行提交同语义）。
      if (item.action.kind === "command-option") {
        const optionCommand = findComposerCommand(registry, item.action.name);
        if (!optionCommand) {
          return;
        }
        completeWith("", false, "");
        dispatchCommand(optionCommand, item.action.value, "pick");
        return;
      }
      const command = findComposerCommand(registry, item.action.name);
      if (!command) {
        return;
      }
      if (command.kind === "action") {
        completeWith("", false);
        dispatchCommand(command, "", "pick");
        return;
      }
      // popupSelect：有专属候选时补全命令名并下钻到第二级候选，否则只补全命令名。
      if (
        command.kind === "popupSelect" &&
        (command.options?.length ?? 0) > 0
      ) {
        completeWith(`/${command.name}`, true);
        setState((previous) => ({
          ...previous,
          manual: true,
          trigger: null,
          dismissedKey: null,
          level: { kind: "command-options", commandKey: command.key },
          activeId: null,
        }));
        return;
      }
      completeWith(`/${command.name}`, command.kind === "popupSelect");
    },
    [
      activeId,
      close,
      completeWith,
      completeWithSkill,
      dispatchCommand,
      insertAtCaret,
      onAttachRequest,
      onWorkspaceSelect,
      registry,
      snapshot.items,
      state.trigger,
    ],
  );

  const handleKeyDown = useComposerMenuKeyboard({
    open,
    query,
    value,
    activeId,
    snapshot,
    trigger: state.trigger,
    levelKind: state.level.kind,
    backToRoot,
    close,
    selectItem,
    onValueChange,
    pendingStaleCaretGuardRef,
    setState,
    setNotice,
  });

  const reportBlocked = useCallback(
    (classification: ComposerSubmitClassification) => {
      if (classification.kind === "unknown-command") {
        setNotice({ kind: "unknown-command", name: classification.name });
        return;
      }
      if (classification.kind === "incomplete-command") {
        setNotice({ kind: "incomplete-command" });
      }
    },
    [],
  );

  return {
    open,
    snapshot,
    activeId,
    activeDescendantId,
    listboxId: COMPOSER_MENU_LISTBOX_ID,
    notice,
    commandLine,
    handleValueChange,
    handleCaretChange,
    handleKeyDown,
    selectItem,
    hoverItem: (itemId: string) => {
      setState((previous) => ({ ...previous, activeId: itemId }));
    },
    backToRoot,
    openFromButton: () => {
      setNotice(null);
      setState({
        manual: true,
        trigger: null,
        dismissedKey: null,
        level: { kind: "root" },
        activeId: null,
      });
    },
    close,
    dismissNotice: () => setNotice(null),
    classifySubmit: () => classifyComposerSubmit(value, registry),
    reportBlocked,
    dispatchCommand,
  };
}