import { editorToggleRowClassName } from "./editor-control-class";
import { SettingsMiniCard } from "./settings-mini-card";

type SettingsMiniToggleCardProps = {
  checked: boolean;
  description?: string;
  label: string;
  onCheckedChange: (checked: boolean) => void;
  checkedLabel?: string;
  uncheckedLabel?: string;
};

export function SettingsMiniToggleCard({
  checked,
  description,
  label,
  onCheckedChange,
  checkedLabel = "已启用",
  uncheckedLabel = "已关闭",
}: SettingsMiniToggleCardProps) {
  return (
    <SettingsMiniCard title={label} description={description}>
      <label className={`mt-3 ${editorToggleRowClassName}`}>
        <span>{checked ? checkedLabel : uncheckedLabel}</span>
        <input
          type="checkbox"
          className="h-4 w-4 accent-[var(--accent-primary)]"
          checked={checked}
          onChange={(event) => onCheckedChange(event.target.checked)}
        />
      </label>
    </SettingsMiniCard>
  );
}
