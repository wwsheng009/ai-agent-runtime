// P1-4 子片 3：Composer 触发菜单的 owner hook。
// 三入口同源：`/`（命令）、`@`（引用）、`+` 按钮（全部）共用同一份菜单模型与派发路径。

import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";

import {
  classifyComposerSubmit,
  createComposerCommandRegistry,
  findComposerCommand,
  parseComposerCommandLine,
  type ComposerCommand,
  type ComposerCommandDefinition,
  type ComposerSubmitClassification,
} from "@/lib/composer-commands";
import {
  buildComposerMenu,
  clampComposerMenuActive,
  findComposerMenuItem,
  moveComposerMenuActive,
  resolveComposerMenuTab,
  type ComposerCommandOptionsSource,
  type ComposerMenuLevel,
  type ComposerMenuMode,
  type ComposerMenuSnapshot,
  type ComposerReferenceGroup,
} from "@/lib/composer-menu";
import {
  applyComposerSkillMentionInsertion,
  applyComposerTriggerInsertion,
  composerReferenceText,
  composerSkillMentionText,
  detectComposerTrigger,
  type ComposerTrigger,
} from "@/lib/composer-trigger";
import {
  isSkillMentionCaretEcho,
  resolveSkillMentionTabExtension,
} from "@/lib/composer-skill-mentions";

import { useComposerCaretInsertion } from "./use-composer-caret-insertion";

export type ComposerMenuNotice =
  | { kind: "unknown-command"; name: string }
  | { kind: "incomplete-command" }
  | { kind: "no-executor"; name: string };

export type UseComposerMenuOptions = {
  value: string;
  /** 命令定义（hook 内部注册；非法名/冲突在此 fail loudly）。 */
  commands: readonly ComposerCommandDefinition[];
  referenceGroups: readonly ComposerReferenceGroup[];
  /** `$` 技能提及候选分组（仅 `skills` 模式消费）。 */
  skillGroups?: readonly ComposerReferenceGroup[];
  hasAttachAction: boolean;
  /** 已本地化的「添加附件」文案（模型层不持文案）。 */
  attachLabel: string;
  onValueChange: (next: string, caret: number) => void;
  onAttachRequest: () => void;
  /**
   * 菜单可见状态回调：宿主据此决定是否拉取 `@` 引用候选数据
   * （菜单关闭/未打开时不得触发网络请求）。用 effect 收敛，避免渲染期调 setState。
   */
  onMenuStateChange?: (state: ComposerMenuState) => void;
  /** 返回 true 表示命令已被处理；否则给出「暂未接入执行器」提示，绝不降级为 prompt。 */
  onCommand?: (
    command: ComposerCommand,
    args: string,
    source: "pick" | "submit",
  ) => boolean | void;
};

export type ComposerMenuState = {
  open: boolean;
  mode: ComposerMenuMode;
  /** `@` 后的查询串（manual/命令模式为空串）。 */
  query: string;
};

export type ComposerMenuController = {
  open: boolean;
  snapshot: ComposerMenuSnapshot;
  activeId: string | null;
  activeDescendantId: string | null;
  listboxId: string;
  notice: ComposerMenuNotice | null;
  /** 当前草稿是命令行（行首 `/`）——不会作为普通消息发送。 */
  commandLine: boolean;
  handleValueChange: (next: string, caret: number) => void;
  handleCaretChange: (caret: number) => void;
  handleKeyDown: (event: KeyboardEvent<HTMLTextAreaElement>) => boolean;
  selectItem: (itemId: string | null) => void;
  hoverItem: (itemId: string) => void;
  openFromButton: () => void;
  close: () => void;
  dismissNotice: () => void;
  classifySubmit: () => ComposerSubmitClassification;
  /** 提交被阻塞时登记提示（组件在提交路径调用）。 */
  reportBlocked: (classification: ComposerSubmitClassification) => void;
  dispatchCommand: (
    command: ComposerCommand,
    args: string,
    source: "pick" | "submit",
  ) => boolean;
};

export const COMPOSER_MENU_LISTBOX_ID = "composer-command-menu";

export function composerMenuItemDomId(itemId: string): string {
  return `composer-menu-option-${itemId.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
}

type MenuState = {
  manual: boolean;
  trigger: ComposerTrigger | null;
  dismissedKey: string | null;
  level: ComposerMenuLevel;
  activeId: string | null;
};

const INITIAL_MENU_STATE: MenuState = {
  manual: false,
  trigger: null,
  dismissedKey: null,
  level: { kind: "root" },
  activeId: null,
};

export function useComposerMenu({
  value,
  commands,
  referenceGroups,
  skillGroups,
  hasAttachAction,
  attachLabel,
  onValueChange,
  onAttachRequest,
  onMenuStateChange,
  onCommand,
}: UseComposerMenuOptions): ComposerMenuController {
  const [state, setState] = useState<MenuState>(INITIAL_MENU_STATE);
  const [notice, setNotice] = useState<ComposerMenuNotice | null>(null);
  // `+`（manual）菜单的插入点：无触发 token 时技能/引用按最近光标落 token。
  const { rememberCaret, insertAtCaret } = useComposerCaretInsertion({ value, onValueChange });

  const registry = useMemo(() => createComposerCommandRegistry(commands), [commands]);

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
  const commandOptionsSource = useMemo<ComposerCommandOptionsSource | null>(() => {
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
    ],
  );

  const activeId = open ? clampComposerMenuActive(snapshot.items, state.activeId) : null;
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

  const handleValueChange = useCallback((next: string, caret: number) => {
    rememberCaret(caret);
    onValueChange(next, caret);
    setNotice(null);
    setState((previous) => ({
      ...previous,
      trigger: detectComposerTrigger(next, caret),
      level: { kind: "root" },
      activeId: null,
    }));
  }, [onValueChange, rememberCaret]);

  // 程序化写入后可能补发携带旧 token 的 select 回声：只在回声形态（同起点 + query 严格回退）时忽略。
  const pendingStaleCaretGuardRef = useRef<{ tokenStart: number; query: string } | null>(null);

  const handleCaretChange = useCallback((caret: number) => {
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
      return { ...previous, trigger, level: { kind: "root" }, activeId: null };
    });
  }, [rememberCaret, value]);

  const dispatchCommand = useCallback((
    command: ComposerCommand,
    args: string,
    source: "pick" | "submit",
  ): boolean => {
    const handled = onCommand?.(command, args, source) === true;
    if (!handled) {
      setNotice({ kind: "no-executor", name: command.name });
    }
    return handled;
  }, [onCommand]);

  const completeWith = useCallback((
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
  }, [onValueChange, state.trigger, value]);

  // 技能提及补全：`$` 路径替换触发 token；`+` 路径无触发 token，按最近光标插入 `$name `。
  const completeWithSkill = useCallback((skillName: string) => {
    const trigger = state.trigger;
    if (trigger && skillName.trim().length > 0) {
      const next = applyComposerSkillMentionInsertion(value, trigger, skillName);
      onValueChange(next.value, next.caret);
    } else if (skillName.trim().length > 0) {
      insertAtCaret(composerSkillMentionText(skillName), { trailingSpace: true });
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
  }, [insertAtCaret, onValueChange, state.trigger, value]);

  const selectItem = useCallback((itemId: string | null) => {
    const item = findComposerMenuItem(snapshot.items, itemId ?? activeId);
    if (!item) {
      return;
    }
    setNotice(null);
    if (item.action.kind === "attach") {
      onAttachRequest();
      close();
      return;
    }
    // 分组 launcher：Enter / Tab / 点选统一语义 = 下钻展开该组。
    if (item.level === "launcher") {
      setState((previous) => ({
        ...previous,
        level: { kind: "group", groupId: item.groupId },
        activeId: null,
      }));
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
    if (command.kind === "popupSelect" && (command.options?.length ?? 0) > 0) {
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
  }, [
    activeId,
    close,
    completeWith,
    completeWithSkill,
    dispatchCommand,
    insertAtCaret,
    onAttachRequest,
    registry,
    snapshot.items,
    state.trigger,
  ]);

  const handleKeyDown = useCallback((event: KeyboardEvent<HTMLTextAreaElement>): boolean => {
    if (!open) {
      return false;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      close();
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
    if (event.key === "Tab") {
      // `$` 多候选：Tab 先加深公共前缀（不落空格、菜单保持打开），与 TUI 一致
      // （前缀子集判定在 lib）；唯一候选/不可再加深时回落到通用 pick。
      if (state.trigger?.kind === "skill") {
        const extension = resolveSkillMentionTabExtension(value, state.trigger, snapshot.items);
        if (extension) {
          event.preventDefault();
          pendingStaleCaretGuardRef.current = {
            tokenStart: state.trigger.start,
            query: extension.trigger.query,
          };
          onValueChange(extension.value, extension.caret);
          setState((previous) => ({ ...previous, trigger: extension.trigger }));
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
        if (state.trigger?.kind === "slash") {
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
        if (state.trigger?.kind === "reference") {
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
  }, [activeId, close, onValueChange, open, query, selectItem, snapshot.items, state.trigger, value]);

  const reportBlocked = useCallback((classification: ComposerSubmitClassification) => {
    if (classification.kind === "unknown-command") {
      setNotice({ kind: "unknown-command", name: classification.name });
      return;
    }
    if (classification.kind === "incomplete-command") {
      setNotice({ kind: "incomplete-command" });
    }
  }, []);

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
