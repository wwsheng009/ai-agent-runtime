// Composer 触发菜单的纯类型模型（渲染层与 hook 共享，无运行时依赖）。

import type {
  ComposerCommand,
  ComposerCommandOption,
} from "@/lib/composer-commands";

/** `commands` = `/` 触发；`references` = `@` 触发；`skills` = `$` 触发；`all` = `+` 按钮（同源菜单）。 */
export type ComposerMenuMode = "commands" | "references" | "skills" | "all";

export type ComposerMenuLevel =
  | { kind: "root" }
  | { kind: "group"; groupId: string }
  /** 命令专属候选（`popupSelect` 选中后的第二级）。 */
  | { kind: "command-options"; commandKey: string };

export type ComposerMenuItemAction =
  | { kind: "command"; name: string }
  /** 命令专属候选：`name` 回查命令，`value` 作为参数派发。 */
  | { kind: "command-option"; name: string; value: string }
  | { kind: "reference"; text: string }
  /** `$` 技能提及：在触发 token 处补全为 `$name `。 */
  | { kind: "skill"; name: string }
  /** 工作区选择由宿主持有状态；不写入 composer 草稿。 */
  | { kind: "workspace"; directoryId: string | null }
  | { kind: "attach" };

export type ComposerMenuItem = {
  id: string;
  groupId: string;
  /** 已解析的展示文本；给 `labelKey` 时渲染层优先用 t(labelKey)。 */
  label: string;
  labelKey?: string;
  description?: string;
  descriptionKey?: string;
  /** 分组 launcher 的候选数量（drill-down 前的「单组 launcher」）。 */
  count?: number;
  /** 非当前键盘焦点的持久选择状态（例如当前已选工作区）。 */
  selected?: boolean;
  level: "launcher" | "leaf";
  action: ComposerMenuItemAction;
};

export type ComposerMenuGroup = {
  id: string;
  label: string;
  labelKey?: string;
  items: ComposerMenuItem[];
  /** 引用组的数据源状态（异步 loader 的 loading / error 表达）。 */
  status?: ComposerReferenceStatus;
  /** true = 还有下一页（服务端语义；仅展示"继续输入缩小范围"提示）。 */
  hasMore?: boolean;
  /** true = 服务端因深度/扫描量/预算截断，结果可能不完备。 */
  truncated?: boolean;
  /** 状态行文案（已本地化；loading/error 时渲染）。 */
  statusText?: string;
  /** 就绪但无候选时的空态文案（已本地化）。 */
  emptyText?: string;
  /** truncated=true 时的页脚文案（已本地化）。 */
  truncatedText?: string;
};

export type ComposerMenuSnapshot = {
  mode: ComposerMenuMode;
  level: ComposerMenuLevel;
  query: string;
  groups: ComposerMenuGroup[];
  items: ComposerMenuItem[];
  activeId: string | null;
  empty: boolean;
};

export type ComposerReferenceItem = {
  id: string;
  label: string;
  /** 实际插入草稿的文本（不含 `@`）。 */
  insertText: string;
  description?: string;
  /** 附加检索词（不展示；如技能分类），仅参与本地过滤命中。 */
  keywords?: string;
};

export type ComposerReferenceStatus = "loading" | "ready" | "error";

export type ComposerReferenceGroup = {
  id: string;
  /** 已由调用方本地化的分组名。 */
  label: string;
  items: readonly ComposerReferenceItem[];
  /**
   * 异步数据源状态：`loading` 时空组也进入 launcher（打开瞬间不闪空），
   * 缺省视为同步数据源（与既有行为完全一致）。
   */
  status?: ComposerReferenceStatus;
  /**
   * true = 本组结果已由服务端过滤/排序，客户端**禁止**再做 `rankMatch` 与顺序重排
   * （模糊命中不是子串，二次过滤会误杀）。
   */
  serverFiltered?: boolean;
  /** 服务端提示：还有下一页 / 结果被截断（渲染页脚用）。 */
  hasMore?: boolean;
  truncated?: boolean;
  /** 状态行文案（已本地化；loading/error 时渲染）。 */
  statusText?: string;
  /** 就绪但无候选时的空态文案（已本地化）。 */
  emptyText?: string;
  /** truncated=true 时的页脚文案（已本地化）。 */
  truncatedText?: string;
};

export type ComposerWorkspaceOption = {
  id: string;
  label: string;
  path: string;
};

/** `+` 菜单中新会话可用的工作区选择数据；文案由宿主本地化。 */
export type ComposerWorkspaceSelection = {
  label: string;
  options: readonly ComposerWorkspaceOption[];
  selectedId: string | null;
  clearLabel: string;
  clearDescription?: string;
  statusText?: string;
  emptyText?: string;
};

export type ComposerMenuSource = {
  mode: ComposerMenuMode;
  level: ComposerMenuLevel;
  query: string;
  commands: readonly ComposerCommand[];
  referenceGroups: readonly ComposerReferenceGroup[];
  /** `$` 技能提及候选（`skills` 模式与 `all` 模式消费）。 */
  skillGroups?: readonly ComposerReferenceGroup[];
  /** `level.kind === "command-options"` 时的候选项来源；缺省按无候选处理。 */
  commandOptions?: ComposerCommandOptionsSource | null;
  /** 是否提供「添加附件」动作。 */
  hasAttachAction: boolean;
  /** 「添加附件」的本地化文案（模型不持文案）。 */
  attachLabel: string;
  /** 仅新会话传入；未提供时不展示工作区选择。 */
  workspaceSelection?: ComposerWorkspaceSelection;
};

export type ComposerCommandOptionsSource = {
  commandKey: string;
  /** 命令名（不含前导 `/`），候选派发时回传。 */
  commandName: string;
  /** 分组标题：命令名（数据原文，非文案）。 */
  label: string;
  options: readonly ComposerCommandOption[];
};