// 由 components/workspace/settings/appearance-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { WavesIcon } from "lucide-react";

import { type AppSettings, type UpdateSettingsSection } from "@/core/settings";

import { SettingsSection } from "../settings-section";
import { SettingsToggleCard } from "../settings-toggle-card";

export function AppearanceMotionSection({
  settings,
  t,
  updateSection,
}: {
  settings: AppSettings;
  t: TFunction<"settings">;
  updateSection: UpdateSettingsSection;
}) {
  return (
      <SettingsSection
        title={t("appearance.motion")}
        description={t("appearance.motionDescription")}
      >
        <SettingsToggleCard
          checked={settings.appearance.reducedMotion}
          onChange={(checked) =>
            updateSection("appearance", {
              reducedMotion: checked,
            })
          }
          title={t("appearance.reducedMotion")}
          description={t("appearance.reducedMotionDescription")}
          icon={<WavesIcon size={16} />}
          iconWrapperClassName="text-accent-secondary"
        />
      </SettingsSection>
  );
}
