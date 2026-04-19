import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type SettingsBadgeListProps = {
  children: ReactNode;
  className?: string;
  compact?: boolean;
};

export function SettingsBadgeList({
  children,
  className,
  compact = false,
}: SettingsBadgeListProps) {
  return (
    <div
      className={cn(
        "flex flex-wrap",
        compact ? "gap-1.5" : "gap-2",
        className,
      )}
    >
      {children}
    </div>
  );
}
