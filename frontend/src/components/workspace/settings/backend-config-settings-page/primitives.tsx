// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { InfoIcon, type LucideIcon } from "lucide-react";
import { type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";


export function StatCard({
  detail,
  icon: Icon,
  label,
  value,
}: {
  detail: string;
  icon: LucideIcon;
  label: string;
  value: string;
}) {
  return (
    <div
      title={detail}
      className="rounded-card border border-border bg-surface-solid px-2.5 py-2"
    >
      <div className="flex items-center gap-2">
        <span className="inline-flex size-5 shrink-0 items-center justify-center rounded-field border border-border bg-surface-softer text-accent-primary">
          <Icon size={11} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
            {label}
          </div>
          <div className="mt-0.5 truncate text-xs font-semibold text-foreground">
            {value}
          </div>
        </div>
      </div>
      <p className="mt-1 truncate text-[11px] leading-4 text-muted-foreground">
        {detail}
      </p>
    </div>
  );
}

export function SummaryPill({ label, value }: { label: string; value: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-control border border-border bg-surface-solid px-2 py-0.5">
      <span className="app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
        {label}
      </span>
      <span className="text-xs font-semibold text-foreground">
        {value}
      </span>
    </span>
  );
}
export function ControlPanel({
  children,
  description,
  title,
}: {
  children: ReactNode;
  description: string;
  title: string;
}) {
  return (
    <div className="rounded-panel border border-border bg-surface-softer p-3">
      <div className="flex items-center gap-2">
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          {title}
        </div>
        <span
          title={description}
          className="inline-flex size-5 items-center justify-center rounded-[0.6rem] border border-border bg-surface-solid text-muted-foreground"
        >
          <InfoIcon size={12} />
        </span>
      </div>
      <div className="mt-3">{children}</div>
    </div>
  );
}
export function MenuButton({
  active,
  badge,
  description,
  disabled = false,
  icon: Icon,
  label,
  onClick,
}: {
  active: boolean;
  badge?: string;
  description: string;
  disabled?: boolean;
  icon: LucideIcon;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      title={description}
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "min-w-0 max-w-full w-full rounded-card border px-3 py-2 text-left transition disabled:cursor-not-allowed disabled:opacity-60",
        active
          ? "border-accent-primary-border bg-accent-primary-soft"
          : "border-border bg-surface-solid hover:border-border-strong hover:bg-surface-soft",
      )}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <span className="rounded-control border border-border bg-surface-softer p-1.5 text-accent-primary">
            <Icon size={14} />
          </span>
          <div className="min-w-0 truncate app-text-13 font-semibold text-foreground">
            {label}
          </div>
        </div>
        {badge ? <Badge>{badge}</Badge> : null}
      </div>
    </button>
  );
}
export function ConfigEditorLoadingCard({ label }: { label: string }) {
  const { t } = useTranslation("runtimeConfig");
  return (
    <div className="rounded-panel border border-border bg-surface-softer p-4">
      <div className="text-sm font-semibold text-foreground">
        {t("editor.loadingCard.title", { label })}
      </div>
      <div className="mt-2 text-sm leading-6 text-muted-foreground">
        {t("editor.loadingCard.body")}
      </div>
    </div>
  );
}
