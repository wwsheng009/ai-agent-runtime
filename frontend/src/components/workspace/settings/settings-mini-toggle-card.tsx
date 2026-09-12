import { useTranslation } from "react-i18next";

import { CheckboxInput } from "@/components/ui/checkbox";
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
  checkedLabel,
  uncheckedLabel,
}: SettingsMiniToggleCardProps) {
  const { t } = useTranslation("common");
  const resolvedCheckedLabel = checkedLabel ?? t("states.enabled");
  const resolvedUncheckedLabel = uncheckedLabel ?? t("states.disabled");

  return (
    <SettingsMiniCard title={label} description={description}>
      <label className={`mt-3 ${editorToggleRowClassName}`}>
        <span>{checked ? resolvedCheckedLabel : resolvedUncheckedLabel}</span>
        <CheckboxInput
          checked={checked}
          onChange={(event) => onCheckedChange(event.target.checked)}
        />
      </label>
    </SettingsMiniCard>
  );
}
