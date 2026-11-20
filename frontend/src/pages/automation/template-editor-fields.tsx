import { type LucideIcon } from "lucide-react";
import { type ReactNode, useId } from "react";

import { HelpTip } from "@/components/workspace/schedule-panel/help-tip";
import { cn } from "@/lib/utils";

export function TemplateField({
  action,
  children,
  className,
  error,
  errorId,
  help,
  helpLabel,
  label,
  required = false,
}: {
  action?: ReactNode;
  children: ReactNode;
  className?: string;
  error?: string;
  errorId?: string;
  help?: string;
  helpLabel?: string;
  label: ReactNode;
  required?: boolean;
}) {
  const hasHelp = Boolean(help && helpLabel);

  return (
    <div className={cn("grid gap-1.5", className)}>
      <div className="flex items-start justify-between gap-2">
        <label className="grid min-w-0 flex-1 gap-1.5">
          <span className="flex items-center gap-1 text-xs font-medium text-foreground">
            {label}
            {required ? (
              <span aria-hidden="true" className="text-accent-orange">
                *
              </span>
            ) : null}
          </span>
          {children}
        </label>
        {action || hasHelp ? (
          <div className="mt-0.5 flex shrink-0 items-center gap-1">
            {action}
            {hasHelp ? (
              <HelpTip label={helpLabel as string} text={help as string} />
            ) : null}
          </div>
        ) : null}
      </div>
      {error ? (
        <span
          className="text-xs text-destructive"
          data-testid="automation-template-field-error"
          id={errorId}
          role="alert"
        >
          {error}
        </span>
      ) : null}
    </div>
  );
}

export function TemplateSection({
  children,
  description,
  helpLabel,
  icon: Icon,
  title,
}: {
  children: ReactNode;
  description?: string;
  helpLabel?: string;
  icon: LucideIcon;
  title: string;
}) {
  const titleId = useId();
  const hasHelp = Boolean(description && helpLabel);

  return (
    <section
      aria-labelledby={titleId}
      className="rounded-card border border-border bg-surface-softer p-3.5 sm:p-4"
    >
      <div className="mb-3 flex items-center gap-2">
        <span className="grid size-6 shrink-0 place-items-center rounded-field border border-border bg-surface-solid text-accent-secondary">
          <Icon aria-hidden="true" size={13} />
        </span>
        <h3
          className="min-w-0 flex-1 text-sm font-semibold tracking-[-0.01em] text-foreground"
          id={titleId}
        >
          {title}
        </h3>
        {hasHelp ? (
          <HelpTip label={helpLabel as string} text={description as string} />
        ) : null}
      </div>
      {children}
    </section>
  );
}
