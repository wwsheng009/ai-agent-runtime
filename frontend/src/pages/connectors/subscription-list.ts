// S2b（docs/bots/13 §8）：订阅编辑器的纯列表解析辅助。
// 独立文件原因：react-refresh 要求组件文件（subscription-editor.tsx）只导出组件。

/** 逗号/空格/中文逗号分隔列表解析（去空、去重、保持原序）。 */
export function parseSubscriptionList(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const token of text.split(/[\s,，]+/u)) {
    const value = token.trim();
    if (!value || seen.has(value)) {
      continue;
    }
    seen.add(value);
    out.push(value);
  }
  return out;
}

/** 列表 → 输入框回显（逗号+空格分隔）。 */
export function formatSubscriptionList(items: readonly string[] | undefined): string {
  return (items ?? []).join(", ");
}
