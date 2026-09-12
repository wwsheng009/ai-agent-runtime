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
      className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2.5"
    >
      <div className="flex items-center gap-3">
        <span className="inline-flex size-7 shrink-0 items-center justify-center rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--accent-primary)]">
          <Icon size={14} />
        </span>
        <div className="min-w-0">
          <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            {label}
          </div>
          <div className="mt-0.5 truncate text-sm font-semibold text-[var(--foreground)]">
            {value}
          </div>
        </div>
      </div>
      <p className="mt-2 truncate text-xs leading-5 text-[var(--muted-foreground)]">
        {detail}
      </p>
    </div>
  );
}

export function SummaryPill({ label, value }: { label: string; value: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-solid)] px-2 py-0.5">
      <span className="app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
        {label}
      </span>
      <span className="text-xs font-semibold text-[var(--foreground)]">
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
    <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
      <div className="flex items-center gap-2">
        <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
          {title}
        </div>
        <span
          title={description}
          className="inline-flex size-5 items-center justify-center rounded-[0.6rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)]"
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
        "min-w-0 max-w-full w-full rounded-[0.8rem] border px-3 py-2 text-left transition disabled:cursor-not-allowed disabled:opacity-60",
        active
          ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)]"
          : "border-[var(--border)] bg-[var(--surface-solid)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
      )}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-softer)] p-1.5 text-[var(--accent-primary)]">
            <Icon size={14} />
          </span>
          <div className="min-w-0 truncate text-[13px] font-semibold text-[var(--foreground)]">
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
    <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-4">
      <div className="text-sm font-semibold text-[var(--foreground)]">
        {t("editor.loadingCard.title", { label })}
      </div>
      <div className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
        {t("editor.loadingCard.body")}
      </div>
    </div>
  );
}
