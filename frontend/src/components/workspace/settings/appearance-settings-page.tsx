import { useTranslation } from "react-i18next";

import {
  CODE_FONT_FAMILY_STACKS,
  FONT_FAMILY_STACKS,
  useAppSettings,
} from "@/core/settings";

import { AppearanceAccentSection } from "./appearance-settings-page/accent-section";
import { AppearanceFontFamilySection } from "./appearance-settings-page/font-family-section";
import { AppearanceMotionSection } from "./appearance-settings-page/motion-section";
import {
  buildAccentOptions,
  buildCodeFontOptions,
  buildFontFamilyOptions,
  buildThemeOptions,
} from "./appearance-settings-page/options";
import { AppearancePreviewSection } from "./appearance-settings-page/preview-section";
import { AppearanceSizeSection } from "./appearance-settings-page/size-section";
import { AppearanceThemeSection } from "./appearance-settings-page/theme-section";

export function AppearanceSettingsPage() {
  const { t } = useTranslation("settings");
  const { resolvedTheme, settings, systemTheme, updateSection } = useAppSettings();
  const currentFontStack = FONT_FAMILY_STACKS[settings.appearance.fontFamily];
  const currentCodeFontStack =
    CODE_FONT_FAMILY_STACKS[settings.appearance.codeFontFamily];
  const themeValueLabel =
    settings.appearance.themeMode === "system"
      ? t("appearance.themeSystemResolved", {
          resolved:
            resolvedTheme === "dark"
              ? t("appearance.themeOptions.dark.label")
              : t("appearance.themeOptions.light.label"),
        })
      : t(`appearance.themeOptions.${settings.appearance.themeMode}.label`);

  const accentOptions = buildAccentOptions(t);
  const themeOptions = buildThemeOptions(t);
  const fontFamilyOptions = buildFontFamilyOptions(t);
  const codeFontOptions = buildCodeFontOptions(t);

  return (
    <div className="space-y-6">
      <AppearanceThemeSection
        settings={settings}
        systemTheme={systemTheme}
        t={t}
        themeOptions={themeOptions}
        themeValueLabel={themeValueLabel}
        updateSection={updateSection}
      />

      <AppearanceAccentSection
        accentOptions={accentOptions}
        settings={settings}
        t={t}
        updateSection={updateSection}
      />

      <AppearanceFontFamilySection
        codeFontOptions={codeFontOptions}
        fontFamilyOptions={fontFamilyOptions}
        settings={settings}
        t={t}
        updateSection={updateSection}
      />

      <AppearanceSizeSection
        settings={settings}
        t={t}
        updateSection={updateSection}
      />

      <AppearanceMotionSection
        settings={settings}
        t={t}
        updateSection={updateSection}
      />

      <AppearancePreviewSection
        currentCodeFontStack={currentCodeFontStack}
        currentFontStack={currentFontStack}
        settings={settings}
        t={t}
      />
    </div>
  );
}
