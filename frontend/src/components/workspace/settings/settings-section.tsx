import type { ReactNode } from "react";

type SettingsSectionProps = {
  title: string;
  description: string;
  children: ReactNode;
};

export function SettingsSection({
  title,
  description,
  children,
}: SettingsSectionProps) {
  return (
    <section className="space-y-3">
      <div>
        <div className="text-[13px] font-semibold text-[var(--foreground)]">{title}</div>
        <p className="mt-1 text-sm leading-6 text-[var(--muted-foreground)]">
          {description}
        </p>
      </div>
      {children}
    </section>
  );
}
