// 由 components/workspace/settings/runtime-transformer-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { Settings2Icon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import { type RuntimeTransformerConfigSummary } from "../runtime-transformer-domain-utils";
import { SettingsBadgeList } from "../settings-badge-list";
import { SettingsPanelCard } from "../settings-panel-card";
import { SettingsPanelIcon } from "../settings-panel-icon";

import { TransformerToggleCard } from "./toggle-card";

export function RuntimeTransformerOverviewSection({
  config,
  enabledModifierCount,
  onChangeConfig,
  t,
  totalModifierCount,
}: {
  config: RuntimeTransformerConfigSummary;
  enabledModifierCount: number;
  onChangeConfig: (next: RuntimeTransformerConfigSummary) => void;
  t: TFunction<"runtimeConfig">;
  totalModifierCount: number;
}) {
  return (
      <SettingsPanelCard
        title={<span className="text-base">{t("editor.transformer.title")}</span>}
        icon={
          <SettingsPanelIcon>
            <Settings2Icon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.transformer.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>
              {config.httpTransformStageEnabled
                ? t("editor.transformer.status.httpStageOn")
                : t("editor.transformer.status.httpStageOff")}
            </Badge>
            <Badge>
              {t("editor.transformer.summary.enabledModifiers", {
                enabled: String(enabledModifierCount),
                total: String(totalModifierCount),
              })}
            </Badge>
            <Badge>
              {config.highPerf
                ? t("editor.transformer.status.highPerfOn")
                : t("editor.transformer.status.highPerfOff")}
            </Badge>
          </SettingsBadgeList>
        }
      >
        <div className="grid gap-3 xl:grid-cols-4">
          <TransformerToggleCard
            label={t("editor.transformer.fields.highPerfLabel")}
            description={t("editor.transformer.toggles.highPerf")}
            checked={config.highPerf}
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, highPerf: checked })
            }
          />
          <TransformerToggleCard
            label={t("editor.transformer.fields.httpTransformStageLabel")}
            description={t("editor.transformer.toggles.httpTransformStage")}
            checked={config.httpTransformStageEnabled}
            onCheckedChange={(checked) =>
              onChangeConfig({
                ...config,
                httpTransformStageEnabled: checked,
              })
            }
          />
          <TransformerToggleCard
            label={t("editor.transformer.fields.cacheAdaptersLabel")}
            description={t("editor.transformer.toggles.cacheAdapters")}
            checked={config.cacheAdapters}
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, cacheAdapters: checked })
            }
          />
          <TransformerToggleCard
            label={t("editor.transformer.fields.streamNullFilterLabel")}
            description={t("editor.transformer.toggles.streamNullFilter")}
            checked={config.streamNullFilter}
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, streamNullFilter: checked })
            }
          />
        </div>
      </SettingsPanelCard>
  );
}
