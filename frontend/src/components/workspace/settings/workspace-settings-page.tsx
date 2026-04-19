import { PanelRightOpenIcon, Rows4Icon } from "lucide-react";

import { useAppSettings } from "@/core/settings";

import { SettingsChoiceCard } from "./settings-choice-card";
import { SettingsSection } from "./settings-section";
import { SettingsToggleCard } from "./settings-toggle-card";

const densityOptions = [
  {
    id: "comfortable",
    label: "舒展",
    description: "保留更大的段落间距和侧栏留白，适合长时间阅读。",
  },
  {
    id: "compact",
    label: "紧凑",
    description: "减少消息和导航区间距，适合在较小屏幕上查看更多内容。",
  },
] as const;

export function WorkspaceSettingsPage() {
  const { settings, updateSection } = useAppSettings();

  return (
    <div className="space-y-6">
      <SettingsSection
        title="界面密度"
        description="影响侧栏、消息区域和输入区的垂直留白。"
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
        title="文件栏行为"
        description="控制运行时产生新工件时右侧文件面板的默认打开方式。"
      >
        <SettingsToggleCard
          checked={settings.workspace.autoOpenArtifacts}
          onChange={(checked) =>
            updateSection("workspace", {
              autoOpenArtifacts: checked,
            })
          }
          title="自动打开文件面板"
          description="开启后，运行时把新工件自动选中时会联动展开右侧栏；关闭后只记录选择，不强制展开。"
          icon={<PanelRightOpenIcon size={16} />}
          iconWrapperClassName="text-[var(--accent-secondary)]"
        />
      </SettingsSection>
    </div>
  );
}
