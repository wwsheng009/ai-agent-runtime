import { RouteIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";

import { ConfigFormField } from "./config-form-field";
import { editorControlClassName } from "./editor-control-class";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsInlineToggleCard } from "./settings-inline-toggle-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import {
  hasRuntimeProxyConfig,
  summarizeRuntimeProxyConfig,
  type RuntimeProxyConfigSummary,
} from "./runtime-proxy-domain-utils";

type RuntimeProxyDomainEditorProps = {
  config: RuntimeProxyConfigSummary;
  onChange: (next: RuntimeProxyConfigSummary) => void;
};

export function RuntimeProxyDomainEditor({
  config,
  onChange,
}: RuntimeProxyDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");

  function update(patch: Partial<RuntimeProxyConfigSummary>) {
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
              <RouteIcon size={15} />
            </SettingsPanelIcon>
            <div>
              <div className="text-base font-semibold text-foreground">
                {t("editor.proxy.title")}
              </div>
              <div className="mt-1 text-sm text-muted-foreground">
                {t("editor.proxy.description")}
              </div>
            </div>
          </div>
          <SettingsBadgeList>
            <Badge>
              {config.enabled
                ? t("editor.proxy.badges.enabledOn")
                : t("editor.proxy.badges.enabledOff")}
            </Badge>
            <Badge>{summarizeRuntimeProxyConfig(config)}</Badge>
            <Badge>
              {hasRuntimeProxyConfig(config)
                ? t("editor.proxy.badges.explicit")
                : t("editor.proxy.badges.envFallback")}
            </Badge>
          </SettingsBadgeList>
        </div>
      </div>

      <SettingsInlineToggleCard
        checked={config.enabled}
        label={t("editor.proxy.fields.enabled")}
        description={t("editor.proxy.enabledDescription")}
        onCheckedChange={(checked) => update({ enabled: checked })}
      />

      <div className="grid gap-3 xl:grid-cols-2">
        <ConfigFormField
          label={t("editor.proxy.fields.http")}
          description={t("editor.proxy.httpDescription")}
        >
          <input
            className={editorControlClassName}
            value={config.http}
            onChange={(event) => update({ http: event.target.value })}
            placeholder={t("editor.proxy.placeholders.proxyUrl")}
          />
        </ConfigFormField>

        <ConfigFormField
          label={t("editor.proxy.fields.https")}
          description={t("editor.proxy.httpsDescription")}
        >
          <input
            className={editorControlClassName}
            value={config.https}
            onChange={(event) => update({ https: event.target.value })}
            placeholder={t("editor.proxy.placeholders.proxyUrl")}
          />
        </ConfigFormField>
      </div>

      <ConfigFormField
        label={t("editor.proxy.fields.noProxy")}
        description={t("editor.proxy.noProxyDescription")}
      >
        <textarea
          className={`${editorControlClassName} min-h-28 resize-y font-mono`}
          value={config.noProxy}
          onChange={(event) => update({ noProxy: event.target.value })}
          placeholder={t("editor.proxy.placeholders.noProxy")}
        />
      </ConfigFormField>
    </div>
  );
}
