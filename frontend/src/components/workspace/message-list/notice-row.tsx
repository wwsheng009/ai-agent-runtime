// 批次 A1/F5：列内「非消息」提示行 —— 单行 + tone 色点，无卡片外壳（§8.3 / §8.5 notice）。
// 承载：空态提示、回溯导航提示/错误/回执、连接提示、turn-error / turn-max-tokens 收敛行。
// 形制约束：行高不随内容增长（内容换行时色点与首行对齐），不得新增 border / bg / shadow。

import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

export type NoticeTone = "error" | "info" | "warn";

const DOT_CLASS: Record<NoticeTone, string> = {
  error: "bg-accent-orange",
  info: "bg-accent-teal",
  warn: "bg-accent-gold",
};

const TITLE_CLASS: Record<NoticeTone, string> = {
  error: "text-accent-orange",
  info: "text-foreground",
  warn: "text-accent-gold",
};

type NoticeRowProps = {
  children: ReactNode;
  className?: string;
  /** 加粗标题（可选）：与正文同一行内呈现，不额外占行。 */
  title?: string;
  tone?: NoticeTone;
};

export function NoticeRow({
  children,
  className,
  title,
  tone = "info",
}: NoticeRowProps) {
  return (
    <div
      className={cn(
        "flex items-start gap-2 app-chat-process-row text-muted-foreground",
        className,
      )}
      data-chat-flow-kind="notice"
      data-notice-tone={tone}
    >
      <span
        aria-hidden="true"
        className={cn(
          "mt-[0.45em] size-1.5 shrink-0 rounded-full",
          DOT_CLASS[tone],
        )}
        data-notice-dot={tone}
      />
      <span className="min-w-0 flex-1">
        {title ? (
          <span className={cn("font-semibold", TITLE_CLASS[tone])}>{title}</span>
        ) : null}
        {title ? <span className="text-muted-foreground"> · </span> : null}
        {children}
      </span>
    </div>
  );
}
