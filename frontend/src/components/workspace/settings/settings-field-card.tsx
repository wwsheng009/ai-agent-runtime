import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type SettingsFieldCardProps = {
  children: ReactNode;
  title: ReactNode;
  bodyClassName?: string;
  className?: string;
  icon?: ReactNode;
  titleClassName?: string;
};

export function SettingsFieldCard({
  children,
  title,
  bodyClassName,
  className,
  icon,
  titleClassName,
}: SettingsFieldCardProps) {
  return (
    <div
      className={cn(
        "rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3.5",
        className,
      )}
    >
      <div
        className={cn(
          "flex items-center gap-3 text-sm font-semibold text-[var(--foreground)]",
          titleClassName,
        )}
      >
        {icon}
        {title}
      </div>
      <div className={cn("mt-2.5", bodyClassName)}>{children}</div>
    </div>
  );
}
