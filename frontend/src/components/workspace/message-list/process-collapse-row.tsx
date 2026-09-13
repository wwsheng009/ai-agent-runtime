// P1-1：折叠摘要行（过程证据汇总 + 展开/折叠开关）。
// 只消费 chat-view 投影的结构化摘要，文案全部来自 i18n。

import { ChevronDownIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { CollapsedTurnSummary } from "@/lib/chat-view";
import { cn } from "@/lib/utils";

export function ProcessCollapseRow({
  expanded,
  onToggle,
  summary,
}: {
  expanded: boolean;
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
    <button
      type="button"
      aria-expanded={expanded}
      onClick={onToggle}
      className="flex w-full items-center gap-2 rounded-field border border-border bg-surface-softer px-3 py-1.5 text-left app-text-11 text-muted-foreground transition-colors hover:border-border-strong hover:text-foreground"
    >
      <ChevronDownIcon
        size={14}
        className={cn(
          "shrink-0 transition-transform duration-200",
          expanded ? "rotate-0" : "-rotate-90",
        )}
      />
      <span className="min-w-0 truncate">{label}</span>
      <span className="sr-only">
        {expanded
          ? t("panels.messages.collapsedSummary.collapse")
          : t("panels.messages.collapsedSummary.expand")}
      </span>
    </button>
  );
}
