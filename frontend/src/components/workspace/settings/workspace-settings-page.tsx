import { PanelRightOpenIcon, Rows4Icon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useAppSettings } from "@/core/settings";

import { PanelIcon } from "@/components/ui/panel-icon";
// P0-2：右侧栏宽度区块（滑块 + 当前值 + 恢复自适应），与拖拽手柄共用同一宽度出口。
import { RailWidthSection } from "@/components/workspace/workspace-shell/rail-width-section";

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
                  <PanelIcon>
                    <Rows4Icon size={16} />
                  </PanelIcon>
                  <div className="text-base font-semibold text-foreground">
                    {option.label}
                  </div>
                </div>
                <p className="mt-2 text-base leading-6 text-muted-foreground">
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
          iconWrapperClassName="text-accent-secondary"
        />
      </SettingsSection>

      <RailWidthSection />
    </div>
  );
}
