// useComposerMenu 的公开类型契约（options / state / controller / notice）。

import type { KeyboardEvent } from "react";

import type {
  ComposerCommand,
  ComposerCommandDefinition,
  ComposerSubmitClassification,
} from "@/lib/composer-commands";
import type {
  ComposerMenuMode,
  ComposerMenuSnapshot,
  ComposerReferenceGroup,
  ComposerWorkspaceSelection,
} from "@/lib/composer-menu";

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
  /** 新会话工作区选择；选择动作不写入草稿。 */
  workspaceSelection?: ComposerWorkspaceSelection;
  onWorkspaceSelect?: (directoryId: string | null) => void;
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
  backToRoot: () => void;
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