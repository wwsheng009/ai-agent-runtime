import { Select } from "@/components/ui/select";
import { useAppSettings } from "@/core/settings";
import { useTranslation } from "react-i18next";

const localeOptions = [
  { value: "system", labelKey: "localization.system" },
  { value: "zh-CN", labelKey: "localization.simplifiedChinese" },
  { value: "en-US", labelKey: "localization.english" },
] as const;

export function LocalizationSettings() {
  const { settings, updateSection } = useAppSettings();
  const { t } = useTranslation("settings");

  return (
    <div className="mb-6 grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,0.55fr)]">
      <div>
        <div className="text-[13px] font-semibold text-[var(--foreground)]">
          {t("localization.title")}
        </div>
        <p className="mt-1 text-sm leading-6 text-[var(--muted-foreground)]">
          {t("localization.description")}
        </p>
      </div>
      <div className="flex items-end gap-3">
        <div className="min-w-0 flex-1">
          <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            {t("localization.title")}
          </div>
          <div className="mt-2">
            <Select
              ariaLabel={t("localization.title")}
              value={settings.localization.locale}
              onChange={(value) =>
                updateSection("localization", {
                  locale: value as typeof settings.localization.locale,
                })
              }
              options={localeOptions.map((option) => ({
                value: option.value,
                label: t(option.labelKey),
              }))}
              className="w-full"
              triggerClassName="w-full text-sm"
            />
          </div>
        </div>
      </div>
    </div>
  );
}
