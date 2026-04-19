import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

export function Badge({
  className,
  children,
}: ComponentProps<"span">) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5 app-text-10 font-semibold uppercase tracking-[0.12em] text-[var(--muted-foreground)]",
        className,
      )}
    >
      {children}
    </span>
  );
}
