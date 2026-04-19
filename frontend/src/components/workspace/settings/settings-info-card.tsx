import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type SettingsInfoCardProps = {
  children?: ReactNode;
  description?: ReactNode;
  icon?: ReactNode;
  title?: ReactNode;
  className?: string;
  contentClassName?: string;
  descriptionClassName?: string;
  size?: "compact" | "default";
  tone?: "solid" | "softer";
};

export function SettingsInfoCard({
  children,
  description,
  icon,
  title,
  className,
  contentClassName,
  descriptionClassName,
  size = "default",
  tone = "solid",
}: SettingsInfoCardProps) {
  const hasHeader = icon !== undefined || title !== undefined;
  const hasDescription = description !== undefined;

  return (
    <div
      className={cn(
        "border border-[var(--border)]",
        tone === "softer"
          ? "rounded-[0.9rem] bg-[var(--surface-softer)]"
          : "rounded-[0.85rem] bg-[var(--surface-solid)]",
        size === "compact" ? "px-3 py-2.5" : "p-3.5",
        className,
      )}
    >
      {hasHeader ? (
        <div className="flex items-center gap-3 text-sm font-semibold text-[var(--foreground)]">
          {icon}
          {title}
        </div>
      ) : null}
      {hasDescription ? (
        <div
          className={cn(
            hasHeader ? "mt-3" : null,
            "text-sm leading-6 text-[var(--muted-foreground)]",
            descriptionClassName,
          )}
        >
          {description}
        </div>
      ) : null}
      {children ? (
        <div
          className={cn(
            hasHeader || hasDescription ? "mt-3" : null,
            contentClassName,
          )}
        >
          {children}
        </div>
      ) : null}
    </div>
  );
}
