// 由 components/workspace/settings/appearance-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";

import { type AppSettings } from "@/core/settings";

import { SettingsInfoCard } from "../settings-info-card";
import { SettingsSection } from "../settings-section";

export function AppearancePreviewSection({
  currentCodeFontStack,
  currentFontStack,
  settings,
  t,
}: {
  currentCodeFontStack: string;
  currentFontStack: { sans: string; serif: string };
  settings: AppSettings;
  t: TFunction<"settings">;
}) {
  return (
      <SettingsSection
        title={t("appearance.preview")}
        description={t("appearance.previewDescription")}
      >
        <div className="grid gap-4 xl:grid-cols-2">
          <SettingsInfoCard
            title={t("appearance.workspaceSample")}
            description={t("appearance.previewSampleText")}
            className="font-sans"
            contentClassName="space-y-3"
          >
            <div
              className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] p-3"
              style={{ fontSize: `${settings.appearance.textSize}px` }}
            >
              <div className="text-sm leading-7 text-[var(--foreground)]">
                {t("appearance.previewWorkspaceBody")}
              </div>
            </div>
          </SettingsInfoCard>
          <SettingsInfoCard
            title={t("appearance.codeSample")}
            description={t("appearance.currentCodeStack")}
            contentClassName="space-y-3"
          >
            <div
              className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] p-3 font-mono"
              style={{ fontSize: `${settings.appearance.codeTextSize}px` }}
            >
              <div className="whitespace-pre-wrap leading-6 text-[var(--foreground)]">
                {`const workspace = await runtime.openThread("new");\nawait workspace.ask("Summarize the failing trace and propose a fix.");\nreturn workspace.receipts.latest();`}
              </div>
            </div>
            <div className="grid gap-2 text-sm leading-6 text-[var(--muted-foreground)]">
              <div>
                {t("appearance.currentUIStack")} {currentFontStack.sans}
              </div>
              <div>
                {t("appearance.currentSerifStack")} {currentFontStack.serif}
              </div>
              <div>
                {t("appearance.currentCodeStack")} {currentCodeFontStack}
              </div>
            </div>
          </SettingsInfoCard>
        </div>
      </SettingsSection>
  );
}
