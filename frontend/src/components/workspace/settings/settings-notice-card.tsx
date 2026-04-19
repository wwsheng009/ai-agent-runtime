import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type SettingsNoticeCardProps = {
  children: ReactNode;
  className?: string;
  tone?: "warning" | "warning-soft" | "neutral" | "muted";
};

export function SettingsNoticeCard({
  children,
  className,
  tone = "neutral",
}: SettingsNoticeCardProps) {
  return (
    <div
      className={cn(
        "rounded-[0.75rem] border",
        tone === "warning-soft"
          ? "border-[#f59e7d]/24 bg-[#f59e7d]/10 px-3 py-2.5 text-sm text-[var(--foreground)]"
          : tone === "warning"
            ? "border-[#f59e7d]/20 bg-[#f59e7d]/8 px-3 py-2.5 text-sm text-[#f59e7d]"
            : tone === "muted"
              ? "border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2 text-xs leading-6 text-[var(--muted-foreground)]"
              : "border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5 text-sm text-[var(--foreground)]",
        className,
      )}
    >
      {children}
    </div>
  );
}
