import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type SettingsEmptyStateProps = {
  children: ReactNode;
  className?: string;
  variant?: "inline" | "dashed";
};

export function SettingsEmptyState({
  children,
  className,
  variant = "inline",
}: SettingsEmptyStateProps) {
  return (
    <div
      className={cn(
        "text-sm leading-6 text-[var(--muted-foreground)]",
        variant === "dashed"
          ? "rounded-[0.75rem] border border-dashed border-[var(--border)] px-3 py-3"
          : "px-3 py-6",
        className,
      )}
    >
      {children}
    </div>
  );
}
