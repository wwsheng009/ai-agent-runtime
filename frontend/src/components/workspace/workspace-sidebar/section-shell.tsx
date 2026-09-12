// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ChevronDownIcon, type LucideIcon } from "lucide-react";
import { type ReactNode } from "react";
import { cn } from "@/lib/utils";
import { type SidebarSectionId } from "./types";

export type SidebarSectionProps = {
  children: ReactNode;
  count?: ReactNode;
  icon: LucideIcon;
  iconClassName: string;
  id: SidebarSectionId;
  isOpen: boolean;
  /** Optional header action (e.g. "add directory") outside the toggle button. */
  action?: ReactNode;
  onToggle: (id: SidebarSectionId) => void;
  title: string;
};

export function SidebarSection({
  children,
  count,
  icon: Icon,
  iconClassName,
  id,
  isOpen,
  action,
  onToggle,
  title,
}: SidebarSectionProps) {
  return (
    <section>
      <div className="mb-2 flex w-full items-center justify-between gap-3 rounded-[0.7rem] px-1.5 py-1 transition hover:bg-[var(--surface-softer)]">
        <button
          type="button"
          onClick={() => onToggle(id)}
          aria-expanded={isOpen}
          className="flex min-w-0 flex-1 items-center justify-between gap-3 text-left"
        >
          <span className="inline-flex items-center gap-2 text-base uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
            <Icon size={14} className={iconClassName} />
            {title}
          </span>
          <span className="inline-flex items-center gap-2">
            {count}
            <ChevronDownIcon
              size={14}
              className={cn(
                "text-[var(--muted-foreground)] transition-transform duration-200",
                isOpen ? "rotate-0" : "-rotate-90",
              )}
            />
          </span>
        </button>
        {action}
      </div>

      {isOpen ? children : null}
    </section>
  );
}
