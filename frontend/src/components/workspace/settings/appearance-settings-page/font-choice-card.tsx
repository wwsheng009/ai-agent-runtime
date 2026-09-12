// 由 components/workspace/settings/appearance-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type CSSProperties } from "react";

import { SettingsChoiceCard } from "../settings-choice-card";

type FontChoiceCardProps = {
  active: boolean;
  description: string;
  label: string;
  onClick: () => void;
  sample: string;
  style?: CSSProperties;
};

export function FontChoiceCard({
  active,
  description,
  label,
  onClick,
  sample,
  style,
}: FontChoiceCardProps) {
  return (
    <SettingsChoiceCard active={active} onClick={onClick}>
      <div className="text-base font-semibold text-[var(--foreground)]">{label}</div>
      <p className="mt-1.5 text-base leading-6 text-[var(--muted-foreground)]">
        {description}
      </p>
      <div
        style={style}
        className="mt-3 rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5 text-base leading-6 text-[var(--foreground)]"
      >
        {sample}
      </div>
    </SettingsChoiceCard>
  );
}
