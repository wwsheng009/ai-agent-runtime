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
import { type ChatMessage } from "@/data/mock";

export function hasVisibleText(value: string | null | undefined): boolean {
  return (value ?? "").trim().length > 0;
}

/**
 * 回合正文（复制目标 / 分支复现内容）：只取 text 段，工具与推理不参与。
 *
 * 收口在此的理由同 `hasVisibleText`：复制入口与分支锚点必须对「什么是这条回答的正文」
 * 保持同一口径，否则会出现「能复制但不能分支」或反过来的错位。
 */
export function answerTextOf(message: Pick<ChatMessage, "segments">): string {
  return message.segments
    .filter(
      (
        segment,
      ): segment is Extract<ChatMessage["segments"][number], { type: "text" }> =>
        segment.type === "text",
    )
    .map((segment) => segment.content)
    .join("\n\n")
    .trim();
}
