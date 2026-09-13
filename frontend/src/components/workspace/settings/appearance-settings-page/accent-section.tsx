// 由 components/workspace/settings/appearance-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { SparklesIcon } from "lucide-react";

import { type AppSettings, type UpdateSettingsSection } from "@/core/settings";
import { cn } from "@/lib/utils";

import { SettingsChoiceCard } from "../settings-choice-card";
import { SettingsSection } from "../settings-section";
import { type AccentOption } from "./options";

export function AppearanceAccentSection({
  accentOptions,
  settings,
  t,
  updateSection,
}: {
  accentOptions: readonly AccentOption[];
  settings: AppSettings;
  t: TFunction<"settings">;
  updateSection: UpdateSettingsSection;
}) {
  return (
      <SettingsSection
        title={t("appearance.accent")}
        description={t("appearance.accentDescription")}
      >
        <div className="grid gap-3 lg:grid-cols-3">
          {accentOptions.map((option) => {
            const active = settings.appearance.accentTone === option.id;

            return (
              <SettingsChoiceCard
                key={option.id}
                active={active}
                onClick={() =>
                  updateSection("appearance", { accentTone: option.id })
                }
              >
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <div className="text-base font-semibold text-foreground">
                      {option.label}
                    </div>
                    <p className="mt-2 text-sm leading-6 text-muted-foreground">
                      {option.description}
                    </p>
                  </div>
                  <SparklesIcon
                    size={16}
                    className={cn(
                      active
                        ? "text-accent-primary"
                        : "text-muted-foreground",
                    )}
                  />
                </div>
                <div
                  className={cn(
                    "mt-3 h-14 rounded-[0.75rem] border border-border bg-gradient-to-r",
                    option.previewClassName,
                  )}
                />
              </SettingsChoiceCard>
            );
          })}
        </div>
      </SettingsSection>
  );
}
