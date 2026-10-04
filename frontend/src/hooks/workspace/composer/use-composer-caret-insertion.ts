// `+`（manual）菜单的无触发 token 插入：跟踪最近一次真实光标位置，
// 供技能/引用在光标处落 token（此前 manual 模式没有触发 token ⇒ 点选是死入口）。
// 与 use-composer-menu.ts 分址的原因：该 hook 受 500 非空行门禁约束。

import { useCallback, useRef } from "react";

import { applyComposerCaretInsertion } from "@/lib/composer-trigger";

export type ComposerCaretInsertion = {
  /** 记录最近一次真实光标位置（输入 / 选择事件）。 */
  rememberCaret: (caret: number) => void;
  /** 在最近光标处插入 token；未跟踪到光标时收敛到文本末尾。 */
  insertAtCaret: (token: string, options?: { trailingSpace?: boolean }) => void;
};

export function useComposerCaretInsertion({
  value,
  onValueChange,
}: {
  value: string;
  onValueChange: (next: string, caret: number) => void;
}): ComposerCaretInsertion {
  const caretRef = useRef<number | null>(null);

  const rememberCaret = useCallback((caret: number) => {
    caretRef.current = caret;
  }, []);

  const insertAtCaret = useCallback(
    (token: string, options?: { trailingSpace?: boolean }) => {
      const caret = caretRef.current ?? value.length;
      const next = applyComposerCaretInsertion(value, caret, token, options);
      caretRef.current = next.caret;
      onValueChange(next.value, next.caret);
    },
    [onValueChange, value],
  );

  return { rememberCaret, insertAtCaret };
}
