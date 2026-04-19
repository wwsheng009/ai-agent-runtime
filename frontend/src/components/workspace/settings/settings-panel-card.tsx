import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type SettingsPanelCardProps = {
  children?: ReactNode;
  description?: ReactNode;
  headerAside?: ReactNode;
  icon?: ReactNode;
  title?: ReactNode;
  asideClassName?: string;
  bodyClassName?: string;
  className?: string;
  descriptionClassName?: string;
  headerClassName?: string;
  tone?: "solid" | "softer";
};

export function SettingsPanelCard({
  children,
  description,
  headerAside,
  icon,
  title,
  asideClassName,
  bodyClassName,
  className,
  descriptionClassName,
  headerClassName,
  tone = "softer",
}: SettingsPanelCardProps) {
  const hasHeader = title !== undefined || icon !== undefined || description !== undefined;

  return (
    <div
      className={cn(
        "rounded-[0.9rem] border border-[var(--border)] p-3.5",
        tone === "softer"
          ? "bg-[var(--surface-softer)]"
          : "bg-[var(--surface-solid)]",
        className,
      )}
    >
      {hasHeader ? (
        <div
          className={cn(
            "flex items-start justify-between gap-3",
            headerClassName,
          )}
        >
          <div className="min-w-0">
            {title !== undefined || icon !== undefined ? (
              <div className="flex items-center gap-3 text-sm font-semibold text-[var(--foreground)]">
                {icon}
                {title}
              </div>
            ) : null}
            {description !== undefined ? (
              <div
                className={cn(
                  title !== undefined || icon !== undefined ? "mt-2" : null,
                  "text-sm leading-6 text-[var(--muted-foreground)]",
                  descriptionClassName,
                )}
              >
                {description}
              </div>
            ) : null}
          </div>
          {headerAside ? (
            <div className={cn("shrink-0", asideClassName)}>{headerAside}</div>
          ) : null}
        </div>
      ) : null}
      {children ? (
        <div className={cn(hasHeader ? "mt-3" : null, bodyClassName)}>{children}</div>
      ) : null}
    </div>
  );
}
