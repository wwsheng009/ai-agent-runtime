// 由 components/workspace/settings/appearance-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";

import {
  CODE_FONT_FAMILY_STACKS,
  FONT_FAMILY_STACKS,
  type AppSettings,
  type UpdateSettingsSection,
} from "@/core/settings";

import { SettingsSection } from "../settings-section";
import { FontChoiceCard } from "./font-choice-card";
import { type CodeFontOption, type FontFamilyOption } from "./options";

export function AppearanceFontFamilySection({
  codeFontOptions,
  fontFamilyOptions,
  settings,
  t,
  updateSection,
}: {
  codeFontOptions: readonly CodeFontOption[];
  fontFamilyOptions: readonly FontFamilyOption[];
  settings: AppSettings;
  t: TFunction<"settings">;
  updateSection: UpdateSettingsSection;
}) {
  return (
      <SettingsSection
        title={t("appearance.fontFamily")}
        description={t("appearance.fontFamilyDescription")}
      >
        <div className="grid gap-4 xl:grid-cols-2">
          <div className="space-y-4">
            <div>
              <div className="text-sm font-semibold text-foreground">
                {t("appearance.bodyFont")}
              </div>
              <div className="mt-1 text-sm leading-6 text-muted-foreground">
                {t("appearance.bodyFontDescription")}
              </div>
            </div>
            <div className="grid gap-3">
              {fontFamilyOptions.map((option) => (
                <FontChoiceCard
                  key={option.id}
                  active={settings.appearance.fontFamily === option.id}
                  description={option.description}
                  label={option.label}
                  sample={option.sample}
                  style={{ fontFamily: FONT_FAMILY_STACKS[option.id].sans }}
                  onClick={() =>
                    updateSection("appearance", { fontFamily: option.id })
                  }
                />
              ))}
            </div>
          </div>

          <div className="space-y-4">
            <div>
              <div className="text-sm font-semibold text-foreground">
                {t("appearance.codeFont")}
              </div>
              <div className="mt-1 text-sm leading-6 text-muted-foreground">
                {t("appearance.codeFontDescription")}
              </div>
            </div>
            <div className="grid gap-3">
              {codeFontOptions.map((option) => (
                <FontChoiceCard
                  key={option.id}
                  active={settings.appearance.codeFontFamily === option.id}
                  description={option.description}
                  label={option.label}
                  sample={option.sample}
                  style={{ fontFamily: CODE_FONT_FAMILY_STACKS[option.id] }}
                  onClick={() =>
                    updateSection("appearance", { codeFontFamily: option.id })
                  }
                />
              ))}
            </div>
          </div>
        </div>
      </SettingsSection>
  );
}
