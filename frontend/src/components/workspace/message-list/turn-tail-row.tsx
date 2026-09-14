// 批次 C2（§8.5 turn-tail）：28px 动作组 + 统计文本（默认隐藏，hover / 键盘焦点显现）。
// 统计口径说明：本地消息模型无时间戳（无「用时 / 首 token」数据源），故统计轴取既有
// `lib/turn-usage` 的 token 用量；无完整用量时整段隐藏（不显示 0）。

import { CopyIcon, RotateCcwIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { type ChatMessage } from "@/data/mock";
import { type TurnUsage } from "@/lib/turn-usage";

type TurnTailRowProps = {
  anchorKey?: string;
  flowKey?: string;
  message: ChatMessage;
  /** 重试入口（宿主提供才渲染；本地当前无回合级重试事件）。 */
  onRetry?: () => void;
  usage: TurnUsage | null;
};

/** 回合可见文本（复制目标）：只取正文段，工具/推理不参与。 */
function answerText(message: ChatMessage): string {
  return message.segments
    .filter(
      (segment): segment is Extract<ChatMessage["segments"][number], { type: "text" }> =>
        segment.type === "text",
    )
    .map((segment) => segment.content)
    .join("\n\n")
    .trim();
}

export function TurnTailRow({
  anchorKey,
  flowKey,
  message,
  onRetry,
  usage,
}: TurnTailRowProps) {
  const { t } = useTranslation("workspace");
  const [copied, setCopied] = useState(false);
  const text = answerText(message);

  const copy = async () => {
    if (!text) return;
    try {
      await navigator.clipboard?.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      // 剪贴板不可用（权限 / 非安全上下文）时静默：不阻塞会话阅读。
    }
  };

  return (
    <div
      className="group -ml-1.5 flex h-7 items-center gap-2.5"
      data-chat-anchor-key={anchorKey}
      data-chat-flow-key={flowKey}
      data-chat-flow-kind="turn-tail"
    >
      <button
        aria-label={t("panels.messages.turnTail.copyAriaLabel")}
        className="inline-flex size-7 shrink-0 items-center justify-center rounded-full text-muted-foreground transition hover:bg-surface-soft hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none disabled:opacity-40"
        disabled={!text}
        onClick={() => {
          void copy();
        }}
        type="button"
      >
        <CopyIcon aria-hidden="true" className="size-4" />
      </button>
      {onRetry ? (
        <button
          aria-label={t("panels.messages.turnTail.retryAriaLabel")}
          className="inline-flex size-7 shrink-0 items-center justify-center rounded-full text-muted-foreground transition hover:bg-surface-soft hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          onClick={onRetry}
          type="button"
        >
          <RotateCcwIcon aria-hidden="true" className="size-4" />
        </button>
      ) : null}
      {usage ? (
        <span
          className="app-hover-reveal min-w-0 truncate app-text-11 text-muted-foreground"
          data-chat-turn-stats="true"
          title={t("panels.messages.turnTail.statsLabel")}
        >
          {copied
            ? t("panels.messages.turnTail.copied")
            : t("panels.messages.turnUsage.summary", {
                prompt: usage.promptTokens.toLocaleString(),
                completion: usage.completionTokens.toLocaleString(),
                total: usage.totalTokens.toLocaleString(),
              })}
        </span>
      ) : null}
    </div>
  );
}
