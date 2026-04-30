import { PanelRightOpenIcon, Rows4Icon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useAppSettings } from "@/core/settings";

import { SettingsChoiceCard } from "./settings-choice-card";
import { SettingsSection } from "./settings-section";
import { SettingsToggleCard } from "./settings-toggle-card";

export function WorkspaceSettingsPage() {
  const { t } = useTranslation("settings");
  const { settings, updateSection } = useAppSettings();
  const densityOptions = [
    {
      id: "comfortable",
      label: t("workspace.densityOptions.comfortable.label"),
      description: t("workspace.densityOptions.comfortable.description"),
    },
    {
      id: "compact",
      label: t("workspace.densityOptions.compact.label"),
      description: t("workspace.densityOptions.compact.description"),
    },
  ] as const;

  return (
    <div className="space-y-6">
      <SettingsSection
        title={t("workspace.density")}
        description={t("workspace.densityDescription")}
      >
        <div className="grid gap-3 md:grid-cols-2">
          {densityOptions.map((option) => {
            const active = settings.workspace.density === option.id;

            return (
              <SettingsChoiceCard
                key={option.id}
                active={active}
                onClick={() =>
                  updateSection("workspace", { density: option.id })
                }
              >
                <div className="flex items-center gap-3">
                  <span className="inline-flex size-8 items-center justify-center rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--accent-primary)]">
                    <Rows4Icon size={16} />
                  </span>
                  <div className="text-base font-semibold text-[var(--foreground)]">
                    {option.label}
                  </div>
                </div>
                <p className="mt-2 text-base leading-6 text-[var(--muted-foreground)]">
                  {option.description}
                </p>
              </SettingsChoiceCard>
            );
          })}
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("workspace.fileBar")}
        description={t("workspace.fileBarDescription")}
      >
        <SettingsToggleCard
          checked={settings.workspace.autoOpenArtifacts}
          onChange={(checked) =>
            updateSection("workspace", {
              autoOpenArtifacts: checked,
            })
          }
          title={t("workspace.autoOpenArtifacts")}
          description={t("workspace.autoOpenArtifactsDescription")}
          icon={<PanelRightOpenIcon size={16} />}
          iconWrapperClassName="text-[var(--accent-secondary)]"
        />
      </SettingsSection>
    </div>
  );
}
