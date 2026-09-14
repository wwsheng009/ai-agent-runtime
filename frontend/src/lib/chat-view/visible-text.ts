/**
 * §12.1.4：空内容不占位（单一判定源）。
 *
 * 协议里「正文为空」是正常形态（工具回合 / 仅推理 / 仅附件），不是待渲染内容：
 * - 空文本段不得生成行节点（空行仍会被父级 flex gap 撑出空白）；
 * - 空内容不得降级成 `[empty message]` 之类的占位文案；
 * - 无可见文本时复制入口不渲染（避免每行一颗无效的复制图标）。
 *
 * 判定统一收口在此，避免渲染层各处 `trim()` 口径漂移。
 */
export function hasVisibleText(value: string | null | undefined): boolean {
  return (value ?? "").trim().length > 0;
}
