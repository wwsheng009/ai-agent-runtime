import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

export function Badge({
  className,
  children,
}: ComponentProps<"span">) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-control border border-border bg-surface-soft px-2 py-0.5 text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground",
        className,
      )}
    >
      {children}
    </span>
  );
}
