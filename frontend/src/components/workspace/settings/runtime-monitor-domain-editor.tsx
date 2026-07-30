import { ActivityIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";

import { ConfigFormField } from "./config-form-field";
import { editorControlClassName } from "./editor-control-class";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsInlineToggleCard } from "./settings-inline-toggle-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import {
  type RuntimeMonitorConfigSummary,
} from "./runtime-monitor-domain-utils";

type RuntimeMonitorDomainEditorProps = {
  config: RuntimeMonitorConfigSummary;
  onChange: (next: RuntimeMonitorConfigSummary) => void;
};

export function RuntimeMonitorDomainEditor({
  config,
  onChange,
}: RuntimeMonitorDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");

  function update(patch: Partial<RuntimeMonitorConfigSummary>) {
    onChange({
      ...config,
      ...patch,
    });
  }

  return (
    <div className="space-y-3">
      <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <SettingsPanelIcon>
              <ActivityIcon size={15} />
            </SettingsPanelIcon>
            <div>
              <div className="text-base font-semibold text-[var(--foreground)]">
                {t("editor.monitor.title")}
              </div>
              <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                {t("editor.monitor.description")}
              </div>
            </div>
          </div>
          <SettingsBadgeList>
            <Badge>
              {config.enabled
                ? t("editor.monitor.badges.enabledOn")
                : t("editor.monitor.badges.enabledOff")}
            </Badge>
            <Badge>
              {config.metricsEnabled
                ? t("editor.monitor.badges.metricsOn")
                : t("editor.monitor.badges.metricsOff")}
            </Badge>
            <Badge>
              {config.tracingEnabled
                ? t("editor.monitor.badges.tracingOn")
                : t("editor.monitor.badges.tracingOff")}
            </Badge>
            <Badge>
              {config.alertEnabled
                ? t("editor.monitor.badges.alertOn")
                : t("editor.monitor.badges.alertOff")}
            </Badge>
          </SettingsBadgeList>
        </div>
      </div>

      <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="mb-3 flex items-center justify-between gap-3">
          <div>
            <div className="text-[13px] font-semibold text-[var(--foreground)]">
              {t("editor.monitor.master.title")}
            </div>
            <div className="mt-1 text-xs text-[var(--muted-foreground)]">
              {t("editor.monitor.master.description")}
            </div>
          </div>
          <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
            <input
              type="checkbox"
              className="h-4 w-4 accent-[var(--accent-primary)]"
              checked={config.enabled}
              onChange={(event) => update({ enabled: event.target.checked })}
            />
            {t("editor.monitor.enable")}
          </label>
        </div>
      </div>

      <div className="grid gap-3 xl:grid-cols-2">
        <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="mb-3 flex items-center justify-between gap-3">
            <div>
              <div className="text-[13px] font-semibold text-[var(--foreground)]">
                {t("editor.monitor.metrics.title")}
              </div>
              <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                {t("editor.monitor.metrics.description")}
              </div>
            </div>
            <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.metricsEnabled}
                onChange={(event) =>
                  update({ metricsEnabled: event.target.checked })
                }
              />
              {t("editor.monitor.enable")}
            </label>
          </div>
          <div className="grid gap-3">
            <ConfigFormField label="metrics.path">
              <input
                className={editorControlClassName}
                value={config.metricsPath}
                onChange={(event) => update({ metricsPath: event.target.value })}
              />
            </ConfigFormField>
            <SettingsInlineToggleCard
              checked={config.metricsAggregation}
              label="metrics.aggregation"
              description={t("editor.monitor.metrics.aggregationDescription")}
              onCheckedChange={(checked) =>
                update({ metricsAggregation: checked })
              }
            />
          </div>
        </div>

        <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="mb-3 flex items-center justify-between gap-3">
            <div>
              <div className="text-[13px] font-semibold text-[var(--foreground)]">
                {t("editor.monitor.tracing.title")}
              </div>
              <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                {t("editor.monitor.tracing.description")}
              </div>
            </div>
            <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.tracingEnabled}
                onChange={(event) =>
                  update({ tracingEnabled: event.target.checked })
                }
              />
              {t("editor.monitor.enable")}
            </label>
          </div>
          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label="tracing.sampler">
              <input
                className={editorControlClassName}
                value={config.tracingSampler}
                onChange={(event) => update({ tracingSampler: event.target.value })}
              />
            </ConfigFormField>
            <ConfigFormField label="tracing.exporter">
              <input
                className={editorControlClassName}
                value={config.tracingExporter}
                onChange={(event) =>
                  update({ tracingExporter: event.target.value })
                }
              />
            </ConfigFormField>
            <ConfigFormField label="tracing.server_addr">
              <input
                className={editorControlClassName}
                value={config.tracingServerAddr}
                onChange={(event) =>
                  update({ tracingServerAddr: event.target.value })
                }
              />
            </ConfigFormField>
          </div>
        </div>
      </div>

      <div className="grid gap-3 xl:grid-cols-2">
        <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="mb-3 flex items-center justify-between gap-3">
            <div>
              <div className="text-[13px] font-semibold text-[var(--foreground)]">
                {t("editor.monitor.alert.title")}
              </div>
              <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                {t("editor.monitor.alert.description")}
              </div>
            </div>
            <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.alertEnabled}
                onChange={(event) => update({ alertEnabled: event.target.checked })}
              />
              {t("editor.monitor.enable")}
            </label>
          </div>
          <div className="grid gap-3">
            <ConfigFormField label="alert.webhook_url">
              <textarea
                className={`${editorControlClassName} min-h-24 resize-y font-mono`}
                value={config.alertWebhookUrl}
                onChange={(event) =>
                  update({ alertWebhookUrl: event.target.value })
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="alert.channels"
              description={t("editor.monitor.alert.channelsDescription")}
            >
              <textarea
                className={`${editorControlClassName} min-h-24 resize-y font-mono`}
                value={config.alertChannelsText}
                onChange={(event) =>
                  update({ alertChannelsText: event.target.value })
                }
              />
            </ConfigFormField>
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="alert.min_threshold">
                <input
                  className={editorControlClassName}
                  value={config.alertMinThreshold}
                  onChange={(event) =>
                    update({ alertMinThreshold: event.target.value })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="alert.severity">
                <input
                  className={editorControlClassName}
                  value={config.alertSeverity}
                  onChange={(event) => update({ alertSeverity: event.target.value })}
                />
              </ConfigFormField>
            </div>
          </div>
        </div>

        <div className="space-y-3">
          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="mb-3 flex items-center justify-between gap-3">
              <div>
                <div className="text-[13px] font-semibold text-[var(--foreground)]">
                  {t("editor.monitor.pprof.title")}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {t("editor.monitor.pprof.description")}
                </div>
              </div>
              <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
                <input
                  type="checkbox"
                  className="h-4 w-4 accent-[var(--accent-primary)]"
                  checked={config.pprofEnabled}
                  onChange={(event) => update({ pprofEnabled: event.target.checked })}
                />
                {t("editor.monitor.enable")}
              </label>
            </div>
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="pprof.listen_addr">
                <input
                  className={editorControlClassName}
                  value={config.pprofListenAddr}
                  onChange={(event) =>
                    update({ pprofListenAddr: event.target.value })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="pprof.gc_interval">
                <input
                  className={editorControlClassName}
                  value={config.pprofGcInterval}
                  onChange={(event) =>
                    update({ pprofGcInterval: event.target.value })
                  }
                />
              </ConfigFormField>
            </div>
          </div>

          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="mb-3 flex items-center justify-between gap-3">
              <div>
                <div className="text-[13px] font-semibold text-[var(--foreground)]">
                  {t("editor.monitor.memory.title")}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {t("editor.monitor.memory.description")}
                </div>
              </div>
              <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
                <input
                  type="checkbox"
                  className="h-4 w-4 accent-[var(--accent-primary)]"
                  checked={config.memoryEnabled}
                  onChange={(event) => update({ memoryEnabled: event.target.checked })}
                />
                {t("editor.monitor.enable")}
              </label>
            </div>
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="memory.sample_interval">
                <input
                  className={editorControlClassName}
                  value={config.memorySampleInterval}
                  onChange={(event) =>
                    update({ memorySampleInterval: event.target.value })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="memory.alert_threshold_mb">
                <input
                  className={editorControlClassName}
                  value={config.memoryAlertThresholdMb}
                  onChange={(event) =>
                    update({ memoryAlertThresholdMb: event.target.value })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="memory.leak_threshold_percent">
                <input
                  className={editorControlClassName}
                  value={config.memoryLeakThresholdPercent}
                  onChange={(event) =>
                    update({ memoryLeakThresholdPercent: event.target.value })
                  }
                />
              </ConfigFormField>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
