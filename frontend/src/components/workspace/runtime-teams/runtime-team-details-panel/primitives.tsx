import { ChevronDownIcon, LoaderCircleIcon } from "lucide-react";

import { cn } from "@/lib/utils";

import type { TeamDetailsSectionProps } from "./types";

export function TeamDetailsSection({
  badge,
  children,
  loading = false,
  onToggle,
  open,
  subtitle,
  title,
}: TeamDetailsSectionProps) {
  return (
    <section className="mt-3 rounded-[0.8rem] border border-border bg-surface-softer px-3 py-2.5">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center justify-between gap-3 text-left"
      >
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-base uppercase tracking-[0.14em] text-muted-foreground">
            <span>{title}</span>
            {loading ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : null}
          </div>
          {subtitle ? (
            <div className="mt-1.5 text-xs leading-5 text-muted-foreground">
              {subtitle}
            </div>
          ) : null}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {badge}
          <ChevronDownIcon
            size={16}
            className={cn(
              "text-muted-foreground transition-transform duration-200",
              open ? "rotate-0" : "-rotate-90",
            )}
          />
        </div>
      </button>

      {open ? <div className="mt-2.5">{children}</div> : null}
    </section>
  );
}
