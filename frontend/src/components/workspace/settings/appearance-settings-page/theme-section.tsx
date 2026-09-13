// 由 components/workspace/settings/appearance-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";

import {
  type AppSettings,
  type ResolvedTheme,
  type UpdateSettingsSection,
} from "@/core/settings";
import { cn } from "@/lib/utils";

import { SettingsChoiceCard } from "../settings-choice-card";
import { SettingsInfoCard } from "../settings-info-card";
import { SettingsSection } from "../settings-section";
import { type ThemeOption } from "./options";

export function AppearanceThemeSection({
  settings,
  systemTheme,
  t,
  themeOptions,
  themeValueLabel,
  updateSection,
}: {
  settings: AppSettings;
  systemTheme: ResolvedTheme;
  t: TFunction<"settings">;
  themeOptions: readonly ThemeOption[];
  themeValueLabel: string;
  updateSection: UpdateSettingsSection;
}) {
  return (
      <SettingsSection
        title={t("appearance.theme")}
        description={t("appearance.themeDescription")}
      >
        <div className="grid gap-3 lg:grid-cols-3">
          {themeOptions.map((option) => {
            const active = settings.appearance.themeMode === option.id;
            const Icon = option.icon;

            return (
              <SettingsChoiceCard
                key={option.id}
                active={active}
                onClick={() =>
                  updateSection("appearance", { themeMode: option.id })
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
                  <Icon
                    size={16}
                    className={cn(
                      active
                        ? "text-accent-primary"
                        : "text-muted-foreground",
                    )}
                  />
                </div>
                <div className="mt-3 rounded-[0.75rem] border border-border bg-surface-soft p-2.5">
                  <div className="flex items-center justify-between gap-3 text-xs uppercase tracking-[0.16em] text-muted-foreground">
                    <span>{t("appearance.themeApplied")}</span>
                    <span className="text-foreground">
                      {option.id === "system"
                        ? t("appearance.themeSystemResolved", {
                            resolved:
                              systemTheme === "dark"
                                ? t("appearance.themeOptions.dark.label")
                                : t("appearance.themeOptions.light.label"),
                          })
                        : option.label}
                    </span>
                  </div>
                  <div
                    className={cn(
                      "mt-2.5 h-14 rounded-[0.7rem] border",
                      option.id === "dark" ||
                        (option.id === "system" && systemTheme === "dark")
                        ? "border-white/10 bg-[linear-gradient(180deg,#111318,#0c0d10)]"
                        : "border-slate-300/60 bg-[linear-gradient(180deg,#ffffff,#eef2f7)]",
                    )}
                  />
                </div>
              </SettingsChoiceCard>
            );
          })}
        </div>

      <SettingsInfoCard
        className="p-3"
        description={
          <>
              {t("appearance.currentlySetTo")}{" "}
              <span className="text-foreground">
                {themeValueLabel}
              </span>
              。
          </>
          }
        />
      </SettingsSection>
  );
}
