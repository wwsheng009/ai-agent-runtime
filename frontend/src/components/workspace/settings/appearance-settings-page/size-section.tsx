// 由 components/workspace/settings/appearance-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";

import {
  APP_FONT_SIZE_DEFAULT,
  CHAT_FONT_SIZE_DEFAULT,
  CODE_FONT_SIZE_DEFAULT,
  type AppSettings,
  type UpdateSettingsSection,
} from "@/core/settings";

import { SettingsSection } from "../settings-section";
import { FontSizeControlCard } from "./font-size-control-card";

export function AppearanceSizeSection({
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
        title={t("appearance.size")}
        description={t("appearance.sizeDescription")}
      >
        <div className="grid gap-4 xl:grid-cols-3">
          <FontSizeControlCard
            title={t("appearance.workspaceSample")}
            description={t("appearance.sizeDescription")}
            defaultValue={APP_FONT_SIZE_DEFAULT}
            value={settings.appearance.textSize}
            onChange={(nextValue) =>
              updateSection("appearance", { textSize: nextValue })
            }
          />
          <FontSizeControlCard
            title={t("appearance.chatSample")}
            description={t("appearance.sizeDescription")}
            defaultValue={CHAT_FONT_SIZE_DEFAULT}
            value={settings.appearance.chatTextSize}
            onChange={(nextValue) =>
              updateSection("appearance", { chatTextSize: nextValue })
            }
          />
          <FontSizeControlCard
            title={t("appearance.codeSample")}
            description={t("appearance.sizeDescription")}
            defaultValue={CODE_FONT_SIZE_DEFAULT}
            value={settings.appearance.codeTextSize}
            onChange={(nextValue) =>
              updateSection("appearance", { codeTextSize: nextValue })
            }
          />
        </div>
      </SettingsSection>
  );
}
