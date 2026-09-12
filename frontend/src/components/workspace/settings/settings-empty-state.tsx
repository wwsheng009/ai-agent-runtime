import { type ReactNode } from "react";

import { surfaceCardVariants } from "@/components/ui/surface-card";
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
          ? surfaceCardVariants({
              frame: "dashed",
              surface: "none",
              radius: "sm",
              density: "roomy",
            })
          : "px-3 py-6",
        className,
      )}
    >
      {children}
    </div>
  );
}
