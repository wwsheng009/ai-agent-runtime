import { type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Button, type ButtonProps } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type SettingsDialogFooterProps = {
  confirmLabel: ReactNode;
  onCancel: () => void;
  onConfirm: () => void;
  actionsBefore?: ReactNode;
  buttonSize?: ButtonProps["size"];
  cancelLabel?: ReactNode;
  className?: string;
  confirmVariant?: ButtonProps["variant"];
  note?: ReactNode;
  noteClassName?: string;
};

export function SettingsDialogFooter({
  confirmLabel,
  onCancel,
  onConfirm,
  actionsBefore,
  buttonSize,
  cancelLabel,
  className,
  confirmVariant,
  note,
  noteClassName,
}: SettingsDialogFooterProps) {
  const { t } = useTranslation("common");
  const resolvedCancelLabel = cancelLabel ?? t("actions.cancel");

  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-3",
        note ? "justify-between" : "justify-end",
        className,
      )}
    >
      {note ? (
        <div className={cn("text-sm text-[var(--muted-foreground)]", noteClassName)}>
          {note}
        </div>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        {actionsBefore}
        <Button variant="ghost" size={buttonSize} onClick={onCancel}>
          {resolvedCancelLabel}
        </Button>
        <Button variant={confirmVariant} size={buttonSize} onClick={onConfirm}>
          {confirmLabel}
        </Button>
      </div>
    </div>
  );
}
