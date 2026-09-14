// 批次 F3（§8.5 turn-process / §13 C2）：回合级统计折叠行。
// 33px = 24px 文本 + 8px 下内距 + 0.5px 底边（`.app-chat-turn-process` 提供）。
// 收起态：该回合过程行**不渲染**（由宿主按 `expanded` 决定），只保留统计行 + 最终回答。
// 统计口径来自 `lib/chat-view` 的折叠摘要（tools / replies / subagents），文案走 i18n。

import { ChevronRightIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { CollapsedTurnSummary } from "@/lib/chat-view";
import { cn } from "@/lib/utils";

export function TurnProcessRow({
  anchorKey,
  expanded,
  flowKey,
  onToggle,
  summary,
}: {
  anchorKey?: string;
  expanded: boolean;
  flowKey?: string;
  onToggle: () => void;
  summary: CollapsedTurnSummary;
}) {
  const { t } = useTranslation("workspace");
  const label = summary.empty
    ? t("panels.messages.collapsedSummary.empty")
    : summary.parts
        .map((part) =>
          t(`panels.messages.collapsedSummary.${part.kind}`, {
            count: part.count,
          }),
        )
        .join(" · ");

  return (
    <div
      className="app-chat-turn-process text-muted-foreground"
      data-chat-anchor-key={anchorKey}
      data-chat-flow-key={flowKey}
      data-chat-flow-kind="turn-process"
      data-state={expanded ? "open" : "closed"}
    >
      <button
        aria-expanded={expanded}
        className="group flex min-w-0 flex-1 items-center gap-2 rounded-md text-left transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        onClick={onToggle}
        type="button"
      >
        <ChevronRightIcon
          aria-hidden="true"
          className={cn(
            "size-4 shrink-0 transition-transform duration-200",
            expanded ? "rotate-90" : "rotate-0",
          )}
        />
        <span className="min-w-0 truncate">{label}</span>
        <span className="sr-only">
          {expanded
            ? t("panels.messages.collapsedSummary.collapse")
            : t("panels.messages.collapsedSummary.expand")}
        </span>
      </button>
    </div>
  );
}
