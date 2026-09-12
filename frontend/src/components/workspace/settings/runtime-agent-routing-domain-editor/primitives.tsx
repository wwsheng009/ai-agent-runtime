import { BotIcon } from "lucide-react";
import { type ReactNode } from "react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export function PreviewValue({ label, value }: { label: string; value?: string }) {
  return (
    <div className="min-w-0">
      <div className="text-xs text-[var(--muted-foreground)]">{label}</div>
      <div className="mt-1 break-words text-sm font-semibold text-[var(--foreground)]">
        {value || "-"}
      </div>
    </div>
  );
}

export function HealthBadge({
  children,
  tone,
}: {
  children: ReactNode;
  tone: "error" | "neutral" | "ready" | "warning";
}) {
  return (
    <Badge
      className={cn(
        "normal-case tracking-normal",
        tone === "ready"
          ? "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[var(--foreground)]"
          : tone === "error"
            ? "border-[#f59e7d]/38 bg-[#f59e7d]/12 text-[#f5c7b8]"
            : tone === "warning"
              ? "border-[#e7d58c]/28 bg-[#e7d58c]/10 text-[var(--foreground)]"
              : undefined,
      )}
    >
      {children}
    </Badge>
  );
}

export function ScopeButton({
  active,
  icon: Icon,
  label,
  onClick,
}: {
  active: boolean;
  icon: typeof BotIcon;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "flex min-h-9 items-center justify-center gap-2 rounded-[0.55rem] px-3 text-sm font-medium transition",
        active
          ? "bg-[var(--accent-primary-soft)] text-[var(--foreground)]"
          : "text-[var(--muted-foreground)] hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)]",
      )}
    >
      <Icon size={15} />
      <span>{label}</span>
    </button>
  );
}

export function LabeledCell({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div className="min-w-0">
      <div className="mb-1 text-xs font-medium text-[var(--muted-foreground)] lg:hidden">
        {label}
      </div>
      {children}
    </div>
  );
}
