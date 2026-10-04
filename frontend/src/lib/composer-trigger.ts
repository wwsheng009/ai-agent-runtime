// P1-4 子片 3：光标处触发器（`/` 命令、`@` 引用、`$` 技能提及）的纯几何判定与替换。
// 不读 DOM、不持有状态：输入值 + 光标位置 → 触发上下文 / 替换结果。

export type ComposerTriggerKind = "slash" | "reference" | "skill";

export type ComposerTrigger = {
  kind: ComposerTriggerKind;
  /** 触发符与光标之间的原始查询串（不含触发符）。 */
  query: string;
  /** 触发符下标。 */
  start: number;
  /** 光标位置（token 结束下标，exclusive）。 */
  end: number;
  /** 稳定身份键：用于「Esc 关闭后同一 token 不再自动弹开」与列表重置。 */
  key: string;
};

function clampCaret(value: string, caret: number): number {
  if (!Number.isFinite(caret)) {
    return value.length;
  }
  return Math.max(0, Math.min(Math.trunc(caret), value.length));
}

/**
 * `$` 提及要忽略的常见环境变量/占位符名单（小写；对齐 TUI
 * `chat_skill_mentions.go` 的 `skillMentionIgnoredNames` 与 PowerShell 形态）。
 */
export const SKILL_MENTION_IGNORED_NAMES: ReadonlySet<string> = new Set([
  "home",
  "path",
  "pwd",
  "user",
  "temp",
  "uid",
  "shell",
  "env",
  "null",
  "_",
  "pshome",
]);

/** `$name` 名称字节集（对齐 TUI `isSkillMentionNameByte`：`[A-Za-z0-9_-]`）。 */
function isSkillMentionNameByte(char: string): boolean {
  return /^[A-Za-z0-9_-]$/.test(char);
}

/**
 * 技能名是否可被 `$name` token 完整引用（对齐 TUI `isSkillMentionCompletableName`）：
 * 名称必须非空且全部是名称字节——带空格/Unicode 的显示名永远不会被词法解析，
 * 因此不得进入补全候选（避免补出永远无法命中的 `$name`）。
 */
export function isCompletableSkillMentionName(name: string): boolean {
  if (name.length === 0) {
    return false;
  }
  for (const char of name) {
    if (!isSkillMentionNameByte(char)) {
      return false;
    }
  }
  return true;
}

/**
 * 光标处是否位于代码豁免区（围栏代码块或行内代码）：仅 `$` 技能提及使用。
 * - 行内代码：所在行 token 左侧反引号计数为奇；
 * - 围栏代码：`$` 之前以 ``` 开头的行计数为奇（与 TUI 的翻转语义一致）。
 */
function isInsideComposerCode(value: string, index: number): boolean {
  const lineStart = value.lastIndexOf("\n", index - 1) + 1;
  let backticks = 0;
  for (let cursor = lineStart; cursor < index; cursor += 1) {
    if (value[cursor] === "`") {
      backticks += 1;
    }
  }
  if (backticks % 2 === 1) {
    return true;
  }
  let fence = false;
  let scan = 0;
  while (scan < lineStart) {
    const nextBreak = value.indexOf("\n", scan);
    const lineEnd = nextBreak === -1 ? value.length : nextBreak;
    if (value.slice(scan, lineEnd).trimStart().startsWith("```")) {
      fence = !fence;
    }
    scan = lineEnd + 1;
  }
  return fence;
}

/**
 * 判定光标的 `$` 技能提及 token（对齐 TUI `detectChatSkillMentionToken`）：
 * - 从光标向左最近的 `$` 起，到光标之间必须全是名称字节（允许空名 = `$`）；
 * - `$` 前一字符是名称字节或 `$`（`a$b`、`$$`）时拒绝；
 * - 行内代码 / 围栏代码内拒绝；env 名单与纯数字 token 拒绝（右侧字符不参与）。
 * 返回 null 表示不是可补全的技能提及。
 */
function detectSkillMentionTrigger(value: string, end: number): ComposerTrigger | null {
  let dollar = -1;
  for (let index = end - 1; index >= 0; index -= 1) {
    const char = value[index];
    if (char === "$") {
      dollar = index;
      break;
    }
    if (!isSkillMentionNameByte(char)) {
      return null;
    }
  }
  if (dollar < 0) {
    return null;
  }
  const before = dollar === 0 ? "" : value[dollar - 1];
  if (before !== "" && (before === "$" || isSkillMentionNameByte(before))) {
    return null;
  }
  if (isInsideComposerCode(value, dollar)) {
    return null;
  }
  const query = value.slice(dollar + 1, end);
  if (query.length > 0) {
    const lower = query.toLowerCase();
    if (SKILL_MENTION_IGNORED_NAMES.has(lower) || /^[0-9]+$/.test(query)) {
      return null;
    }
  }
  return {
    kind: "skill",
    query,
    start: dollar,
    end,
    key: `skill:${dollar}:${query}`,
  };
}

/**
 * 判定光标处的触发上下文。
 * - `/`：必须位于行首（行首允许……不允许，前导空白会破坏「行首」判定）且与光标之间无空白；
 * - `@`：必须位于行首或空白之后，且与光标之间无空白（`foo@bar` 不触发）。
 * - `$`：光标向左的 `$name` token（名称字符集、env/纯数字豁免、代码块豁免）。
 */
export function detectComposerTrigger(value: string, caret: number): ComposerTrigger | null {
  const end = clampCaret(value, caret);
  if (end <= 0) {
    return null;
  }

  const lineStart = value.lastIndexOf("\n", end - 1) + 1;
  if (value[lineStart] === "/" && end > lineStart) {
    const query = value.slice(lineStart + 1, end);
    if (!/\s/.test(query)) {
      return {
        kind: "slash",
        query,
        start: lineStart,
        end,
        key: `slash:${lineStart}:${query}`,
      };
    }
  }

  for (let index = end - 1; index >= 0; index -= 1) {
    const char = value[index];
    if (char === "@") {
      const before = index === 0 ? "" : value[index - 1];
      if (before !== "" && !/\s/.test(before)) {
        return null;
      }
      const query = value.slice(index + 1, end);
      if (/\s/.test(query)) {
        return null;
      }
      return {
        kind: "reference",
        query,
        start: index,
        end,
        key: `reference:${index}:${query}`,
      };
    }
    if (/\s/.test(char)) {
      break;
    }
  }

  return detectSkillMentionTrigger(value, end);
}

/** `@` 引用文本：含空白时用双引号包裹，保证引用 token 自身不被打断。 */
export function composerReferenceText(raw: string): string {
  const text = raw.trim();
  if (text.length === 0) {
    return "@";
  }
  const needsQuotes = /\s/.test(text);
  return needsQuotes ? `@"${text}"` : `@${text}`;
}

/**
 * 用补全文本替换触发 token；若 token 之后紧跟非空白内容，
 * 自动补一个空格避免与后续文本粘连。
 */
export function applyComposerTriggerInsertion(
  value: string,
  trigger: ComposerTrigger,
  inserted: string,
): { value: string; caret: number } {
  const before = value.slice(0, trigger.start);
  const after = value.slice(trigger.end);
  const needsSpace = after.length > 0 && !/^[\s,.;:!?、。)）\]】]/.test(after);
  const text = `${inserted}${needsSpace ? " " : ""}`;
  return { value: `${before}${text}${after}`, caret: before.length + text.length };
}

/**
 * 技能提及补全文本：`$name`。
 * 选中插入时由 `applyComposerSkillMentionInsertion` 追加分隔空格。
 */
export function composerSkillMentionText(name: string): string {
  return `$${name.trim()}`;
}

/**
 * 用 `$name ` 替换技能触发 token（对齐 TUI「唯一命中补全为 `$name `」）：
 * - token 之后是空白时不重复补空格；在行尾或紧跟非空白内容时补一个尾随空格，
 *   便于用户继续输入下一个提及（多技能引用）。
 */
export function applyComposerSkillMentionInsertion(
  value: string,
  trigger: ComposerTrigger,
  name: string,
): { value: string; caret: number } {
  const before = value.slice(0, trigger.start);
  const after = value.slice(trigger.end);
  const needsSpace = after.length === 0 || !/^\s/.test(after);
  const text = `${composerSkillMentionText(name)}${needsSpace ? " " : ""}`;
  return { value: `${before}${text}${after}`, caret: before.length + text.length };
}

/**
 * Tab 的「公共前缀延伸」：把触发 token 替换为 `$prefix`（**不加**尾随空格），
 * 供用户继续收窄候选；对齐 TUI 多候选且公共前缀可加深时的 Tab 语义。
 */
export function applyComposerSkillPrefixInsertion(
  value: string,
  trigger: ComposerTrigger,
  prefix: string,
): { value: string; caret: number } {
  const before = value.slice(0, trigger.start);
  const text = composerSkillMentionText(prefix);
  return {
    value: `${before}${text}${value.slice(trigger.end)}`,
    caret: before.length + text.length,
  };
}

/**
 * 无触发 token 的按光标插入（`+`/manual 菜单路径：技能/引用在光标处落 token）：
 * - 插入点越界时收敛到文本两端；
 * - 插入点前一个字符是非空白时补前置分隔空格；`trailingSpace` 在后续文本非空白
 *   （或到行尾）时补尾随空格，便于继续输入（技能提及用）。
 */
export function applyComposerCaretInsertion(
  value: string,
  caret: number,
  inserted: string,
  options?: { trailingSpace?: boolean },
): { value: string; caret: number } {
  const position = Math.max(0, Math.min(caret, value.length));
  const before = value.slice(0, position);
  const after = value.slice(position);
  const needsLeadingSpace = before.length > 0 && !/\s$/.test(before);
  const needsTrailingSpace =
    (options?.trailingSpace ?? false) && (after.length === 0 || !/^\s/.test(after));
  const text = `${needsLeadingSpace ? " " : ""}${inserted}${needsTrailingSpace ? " " : ""}`;
  return {
    value: `${before}${text}${after}`,
    caret: before.length + text.length,
  };
}
