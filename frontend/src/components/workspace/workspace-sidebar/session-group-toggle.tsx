// P2-6 子片 3：组内展开 / 折叠（Show N more）。
// 只影响呈现：折叠不改排序账目，也不隐藏被选中的会话（可见性判定会自动展开）。

import { ChevronDownIcon } from "lucide-react";
import { type TFunction } from "i18next";

import { cn } from "@/lib/utils";

type WorkspaceSidebarSessionGroupToggleProps = {
  expanded: boolean;
  hiddenCount: number;
  onToggle: () => void;
  t: TFunction<"workspace">;
};

export function WorkspaceSidebarSessionGroupToggle({
  expanded,
  hiddenCount,
  onToggle,
  t,
}: WorkspaceSidebarSessionGroupToggleProps) {
  return (
    <button
      type="button"
      aria-expanded={expanded}
      onClick={onToggle}
      data-testid="sidebar-session-group-toggle"
      className="flex w-full items-center justify-center gap-1.5 rounded-control border border-dashed border-border bg-surface-softer px-2 py-1 app-text-10 text-muted-foreground transition hover:text-foreground"
    >
      <ChevronDownIcon
        size={12}
        className={cn(
          "shrink-0 transition-transform duration-200",
          expanded ? "rotate-0" : "-rotate-90",
        )}
      />
      {expanded
        ? t("sidebar.sessionGrouping.collapse")
        : t("sidebar.sessionGrouping.showMore", { count: hiddenCount })}
    </button>
  );
}
