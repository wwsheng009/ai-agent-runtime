// P1-4 子片 3：光标处触发器（`/` 命令、`@` 引用）的纯几何判定与替换。
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
 * 判定光标处的触发上下文。
 * - `/`：必须位于行首（行首允许……不允许，前导空白会破坏「行首」判定）且与光标之间无空白；
 * - `@`：必须位于行首或空白之后，且与光标之间无空白（`foo@bar` 不触发）。
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
      return null;
    }
  }

  return null;
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
