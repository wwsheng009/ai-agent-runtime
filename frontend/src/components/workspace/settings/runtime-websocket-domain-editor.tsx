import { WifiIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";

import { ConfigFormField } from "./config-form-field";
import { editorControlClassName } from "./editor-control-class";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsInlineToggleCard } from "./settings-inline-toggle-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import { type RuntimeWebsocketConfigSummary } from "./runtime-websocket-domain-utils";

type RuntimeWebsocketDomainEditorProps = {
  config: RuntimeWebsocketConfigSummary;
  onChange: (next: RuntimeWebsocketConfigSummary) => void;
};

export function RuntimeWebsocketDomainEditor({
  config,
  onChange,
}: RuntimeWebsocketDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");

  function update(patch: Partial<RuntimeWebsocketConfigSummary>) {
    onChange({
      ...config,
      ...patch,
    });
  }

  return (
    <div className="space-y-3">
      <div className="rounded-panel border border-border bg-surface-softer p-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <SettingsPanelIcon>
              <WifiIcon size={15} />
            </SettingsPanelIcon>
            <div>
              <div className="text-base font-semibold text-foreground">
                {t("editor.websocket.title")}
              </div>
              <div className="mt-1 text-sm text-muted-foreground">
                {t("editor.websocket.description")}
              </div>
            </div>
          </div>
          <SettingsBadgeList>
            <Badge>
              {config.enabled
                ? t("editor.websocket.badges.enabledOn")
                : t("editor.websocket.badges.enabledOff")}
            </Badge>
            <Badge>
              {config.responsesIngressEnabled
                ? t("editor.websocket.badges.responsesOn")
                : t("editor.websocket.badges.responsesOff")}
            </Badge>
            <Badge>
              {config.realtimeIngressEnabled
                ? t("editor.websocket.badges.realtimeOn")
                : t("editor.websocket.badges.realtimeOff")}
            </Badge>
          </SettingsBadgeList>
        </div>
      </div>

      <div className="rounded-card border border-border bg-surface-softer p-3">
        <div className="mb-3 flex items-center justify-between gap-3">
          <div>
            <div className="app-text-13 font-semibold text-foreground">
              {t("editor.websocket.master.title")}
            </div>
            <div className="mt-1 text-xs text-muted-foreground">
              {t("editor.websocket.master.description")}
            </div>
          </div>
          <label className="flex items-center gap-2 text-sm text-foreground">
            <input
              type="checkbox"
              className="h-4 w-4 accent-accent-primary"
              checked={config.enabled}
              onChange={(event) => update({ enabled: event.target.checked })}
            />
            {t("editor.websocket.enable")}
          </label>
        </div>
      </div>

      <div className="grid gap-3 xl:grid-cols-2">
        <div className="rounded-card border border-border bg-surface-softer p-3">
          <div className="mb-3 flex items-center justify-between gap-3">
            <div>
              <div className="app-text-13 font-semibold text-foreground">
                {t("editor.websocket.responses.title")}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                {t("editor.websocket.responses.description")}
              </div>
            </div>
            <label className="flex items-center gap-2 text-sm text-foreground">
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent-primary"
                checked={config.responsesIngressEnabled}
                onChange={(event) =>
                  update({ responsesIngressEnabled: event.target.checked })
                }
              />
              {t("editor.websocket.enableIngress")}
            </label>
          </div>

          <div className="grid gap-3">
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField
                label={t("editor.websocket.fields.maxActiveConnections")}
              >
                <input
                  className={editorControlClassName}
                  value={config.responsesMaxActiveConnections}
                  onChange={(event) =>
                    update({
                      responsesMaxActiveConnections: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label={t("editor.websocket.fields.affinityTtl")}>
                <input
                  className={editorControlClassName}
                  value={config.responsesAffinityTtl}
                  onChange={(event) =>
                    update({ responsesAffinityTtl: event.target.value })
                  }
                />
              </ConfigFormField>
              <ConfigFormField
                label={t("editor.websocket.fields.handshakeMaxRetries")}
              >
                <input
                  className={editorControlClassName}
                  value={config.responsesHandshakeMaxRetries}
                  onChange={(event) =>
                    update({
                      responsesHandshakeMaxRetries: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
            </div>

            <ConfigFormField
              label={t("editor.websocket.fields.compatBridgeSourceProtocols")}
              description={t(
                "editor.websocket.responses.compatProtocolsDescription",
              )}
            >
              <textarea
                className={`${editorControlClassName} min-h-24 resize-y font-mono`}
                value={config.responsesCompatBridgeSourceProtocolsText}
                onChange={(event) =>
                  update({
                    responsesCompatBridgeSourceProtocolsText: event.target.value,
                  })
                }
              />
            </ConfigFormField>

            <div className="grid gap-3 xl:grid-cols-2">
              <ToggleCard
                label={t("editor.websocket.fields.httpBridgeEnabled")}
                checked={config.responsesHttpBridgeEnabled}
                onChange={(checked) =>
                  update({ responsesHttpBridgeEnabled: checked })
                }
              />
              <ToggleCard
                label={t("editor.websocket.fields.compatBridgeEnabled")}
                checked={config.responsesCompatBridgeEnabled}
                onChange={(checked) =>
                  update({ responsesCompatBridgeEnabled: checked })
                }
              />
              <ToggleCard
                label={t("editor.websocket.fields.allowPassthroughOnly")}
                checked={config.responsesAllowPassthroughOnly}
                onChange={(checked) =>
                  update({ responsesAllowPassthroughOnly: checked })
                }
              />
              <ToggleCard
                label={t("editor.websocket.fields.metricsEnabled")}
                checked={config.responsesMetricsEnabled}
                onChange={(checked) =>
                  update({ responsesMetricsEnabled: checked })
                }
              />
              <ToggleCard
                label={t("editor.websocket.fields.closeCodeLabelsEnabled")}
                checked={config.responsesCloseCodeLabelsEnabled}
                onChange={(checked) =>
                  update({ responsesCloseCodeLabelsEnabled: checked })
                }
              />
              <ToggleCard
                label={t("editor.websocket.fields.connectionPoolingEnabled")}
                checked={config.responsesConnectionPoolingEnabled}
                onChange={(checked) =>
                  update({ responsesConnectionPoolingEnabled: checked })
                }
              />
              <ToggleCard
                label={t("editor.websocket.fields.preFirstEventRetryOnce")}
                checked={config.responsesPreFirstEventRetryOnce}
                onChange={(checked) =>
                  update({ responsesPreFirstEventRetryOnce: checked })
                }
              />
              <ToggleCard
                label={t("editor.websocket.fields.failoverOnHandshakeError")}
                checked={config.responsesFailoverOnHandshakeError}
                onChange={(checked) =>
                  update({ responsesFailoverOnHandshakeError: checked })
                }
              />
            </div>
          </div>
        </div>

        <div className="rounded-card border border-border bg-surface-softer p-3">
          <div className="mb-3 flex items-center justify-between gap-3">
            <div>
              <div className="app-text-13 font-semibold text-foreground">
                {t("editor.websocket.realtime.title")}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                {t("editor.websocket.realtime.description")}
              </div>
            </div>
            <label className="flex items-center gap-2 text-sm text-foreground">
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent-primary"
                checked={config.realtimeIngressEnabled}
                onChange={(event) =>
                  update({ realtimeIngressEnabled: event.target.checked })
                }
              />
              {t("editor.websocket.enableIngress")}
            </label>
          </div>

          <div className="grid gap-3">
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField
                label={t("editor.websocket.fields.maxActiveConnections")}
              >
                <input
                  className={editorControlClassName}
                  value={config.realtimeMaxActiveConnections}
                  onChange={(event) =>
                    update({
                      realtimeMaxActiveConnections: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField
                label={t("editor.websocket.fields.handshakeMaxRetries")}
              >
                <input
                  className={editorControlClassName}
                  value={config.realtimeHandshakeMaxRetries}
                  onChange={(event) =>
                    update({
                      realtimeHandshakeMaxRetries: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
            </div>

            <div className="grid gap-3 xl:grid-cols-2">
              <ToggleCard
                label={t("editor.websocket.fields.metricsEnabled")}
                checked={config.realtimeMetricsEnabled}
                onChange={(checked) =>
                  update({ realtimeMetricsEnabled: checked })
                }
              />
              <ToggleCard
                label={t("editor.websocket.fields.closeCodeLabelsEnabled")}
                checked={config.realtimeCloseCodeLabelsEnabled}
                onChange={(checked) =>
                  update({ realtimeCloseCodeLabelsEnabled: checked })
                }
              />
              <ToggleCard
                label={t("editor.websocket.fields.failoverOnHandshakeError")}
                checked={config.realtimeFailoverOnHandshakeError}
                onChange={(checked) =>
                  update({ realtimeFailoverOnHandshakeError: checked })
                }
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function ToggleCard({
  checked,
  label,
  onChange,
}: {
  checked: boolean;
  label: string;
  onChange: (next: boolean) => void;
}) {
  return (
    <SettingsInlineToggleCard
      checked={checked}
      label={label}
      onCheckedChange={onChange}
    />
  );
}
