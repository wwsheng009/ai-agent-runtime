import { useTranslation } from "react-i18next";

import {
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
  const { settings, systemTheme, updateSection } = useAppSettings();

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
        settings={settings}
        t={t}
      />
    </div>
  );
}
