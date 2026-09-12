// 由 components/workspace/settings/harness-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { PlugIcon } from "lucide-react";
import { type Dispatch, type SetStateAction } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  type RuntimeHarnessPlugin,
  type RuntimeHarnessPluginAction,
} from "@/lib/runtime-api";

import { SettingsEmptyState } from "../settings-empty-state";
import { SettingsPanelCard } from "../settings-panel-card";
import { SettingsSection } from "../settings-section";

export function HarnessPluginsSection({
  actionPending,
  plugins,
  setLocalActionError,
  t,
  updatePlugin,
}: {
  actionPending: boolean;
  plugins: RuntimeHarnessPlugin[];
  setLocalActionError: Dispatch<SetStateAction<string | null>>;
  t: TFunction<"settings">;
  updatePlugin: (
    pluginId: string,
    action: RuntimeHarnessPluginAction,
  ) => Promise<void>;
}) {
  return (
      <SettingsSection
        title={t("harness.pluginsTitle")}
        description={t("harness.pluginsDescription")}
      >
        {plugins.length > 0 ? (
          <div className="space-y-2">
            {plugins.map((plugin) => (
              <SettingsPanelCard
                key={plugin.id}
                title={plugin.name || plugin.id}
                icon={
                  <PlugIcon
                    size={16}
                    className="text-[var(--accent-secondary)]"
                  />
                }
                description={
                  plugin.description ||
                  t("harness.pluginNoDescription")
                }
                headerAside={
                  <div className="flex flex-wrap gap-2">
                    <Badge>{plugin.trust || "untrusted"}</Badge>
                    <Badge>
                      {plugin.active
                        ? t("harness.pluginActive")
                        : t("harness.pluginInactive")}
                    </Badge>
                  </div>
                }
              >
                <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--muted-foreground)]">
                  <span className="app-inline-mono">{plugin.id}</span>
                  {plugin.version ? <span>v{plugin.version}</span> : null}
                  {plugin.root ? (
                    <span className="app-inline-mono break-all">
                      {plugin.root}
                    </span>
                  ) : null}
                </div>
                {(plugin.warnings?.length ?? 0) > 0 ? (
                  <div className="mt-2 space-y-1 text-sm text-[var(--muted-foreground)]">
                    {plugin.warnings?.map((warning) => (
                      <div key={`${plugin.id}-${warning}`}>{warning}</div>
                    ))}
                  </div>
                ) : null}
                <div className="mt-3 flex flex-wrap gap-2">
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={actionPending}
                    onClick={() => {
                      setLocalActionError(null);
                      void updatePlugin(
                        plugin.id,
                        plugin.trust === "trusted" ? "untrust" : "trust",
                      ).catch((actionError) => {
                        setLocalActionError(
                          actionError instanceof Error
                            ? actionError.message
                            : t("harness.pluginUpdateFailed"),
                        );
                      });
                    }}
                  >
                    {plugin.trust === "trusted"
                      ? t("harness.untrust")
                      : t("harness.trust")}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={actionPending}
                    onClick={() => {
                      setLocalActionError(null);
                      void updatePlugin(
                        plugin.id,
                        plugin.enabled ? "disable" : "enable",
                      ).catch((actionError) => {
                        setLocalActionError(
                          actionError instanceof Error
                            ? actionError.message
                            : t("harness.pluginUpdateFailed"),
                        );
                      });
                    }}
                  >
                    {plugin.enabled
                      ? t("harness.disable")
                      : t("harness.enable")}
                  </Button>
                </div>
              </SettingsPanelCard>
            ))}
          </div>
        ) : (
          <SettingsEmptyState variant="dashed">
            {t("harness.noPlugins")}
          </SettingsEmptyState>
        )}
      </SettingsSection>
  );
}
