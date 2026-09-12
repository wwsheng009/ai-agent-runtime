// 由 components/workspace/settings/runtime-transformer-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { SettingsMiniToggleCard } from "../settings-mini-toggle-card";

export function TransformerToggleCard({
  checked,
  description,
  label,
  onCheckedChange,
}: {
  checked: boolean;
  description: string;
  label: string;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <SettingsMiniToggleCard
      checked={checked}
      description={description}
      label={label}
      onCheckedChange={onCheckedChange}
    />
  );
}
