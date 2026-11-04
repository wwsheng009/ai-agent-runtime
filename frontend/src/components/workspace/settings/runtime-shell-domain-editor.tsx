import { AlertTriangleIcon, ArrowDownIcon, ArrowUpIcon, CheckCircle2Icon, RefreshCcwIcon, TerminalIcon, XIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { probeRuntimeShellSettings } from "@/api/runtime/config";
import type { RuntimeShellCandidate, RuntimeShellSettingsResponse } from "@/types/runtime";

import { ConfigFormField } from "./config-form-field";
import {
  editorControlClassName,
  editorToggleRowClassName,
} from "./editor-control-class";
import { SettingsActionButton, SettingsActionGroup } from "./settings-action-group";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsNoticeCard } from "./settings-notice-card";
import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import {
  addShellPreferItem,
  formatShellPrefer,
  moveShellPreferItem,
  removeShellPreferItem,
  runtimeShellCandidateIds,
  type RuntimeShellConfigSummary,
} from "./runtime-shell-domain-utils";

type RuntimeShellDomainEditorProps = {
  config: RuntimeShellConfigSummary;
  onChange: (next: RuntimeShellConfigSummary) => void;
};

/**
 * Shell 执行环境域编辑器。
 *
 * 两类信息刻意分开：
 * - 上半部分是**草稿**（随表单保存进 runtime.yaml）；
 * - 下半部分是**探测结果**（当前进程实际会选中的 shell + 候选矩阵），
 *   只由「检测」按钮刷新，不会被误当成已保存配置。
 */
export function RuntimeShellDomainEditor({ config, onChange }: RuntimeShellDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [probeResult, setProbeResult] = useState<RuntimeShellSettingsResponse | null>(null);
  const [probeError, setProbeError] = useState<string | null>(null);
  const [probing, setProbing] = useState(false);

  const candidates: RuntimeShellCandidate[] = probeResult?.candidates ?? [];
  const effective = probeResult?.effective;

  async function handleProbe() {
    setProbing(true);
    setProbeError(null);
    try {
      const response = await probeRuntimeShellSettings({
        auto_detect: config.autoDetect,
        prefer: config.prefer,
        path: config.path,
        probe_timeout: config.probeTimeout,
        strict_probe: config.strictProbe,
        script_delivery: config.scriptDelivery,
      });
      setProbeResult(response);
      if (response.error) {
        setProbeError(response.error);
      }
    } catch (error) {
      setProbeError(error instanceof Error ? error.message : String(error));
    } finally {
      setProbing(false);
    }
  }

  const availableToAdd = runtimeShellCandidateIds.filter(
    (id) => !config.prefer.includes(id),
  );

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={<span className="text-base">{t("editor.shell.title")}</span>}
        icon={
          <SettingsPanelIcon>
            <TerminalIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.shell.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>
              {config.autoDetect
                ? t("editor.shell.badges.autoOn")
                : t("editor.shell.badges.autoOff")}
            </Badge>
            <Badge>
              {t("editor.shell.badges.candidateCount", {
                count: config.prefer.length,
              })}
            </Badge>
            {effective ? (
              <Badge>{effective.id || effective.dialect}</Badge>
            ) : null}
          </SettingsBadgeList>
        }
      >
        <div className="grid gap-3 xl:grid-cols-2">
          <div className="rounded-card border border-border bg-surface-softer p-3">
            <div className="text-sm font-semibold text-foreground">
              {t("editor.shell.fields.autoDetect")}
            </div>
            <div className="mt-1 text-xs leading-5 text-muted-foreground">
              {t("editor.shell.autoDetectHint")}
            </div>
            <label className={`mt-3 ${editorToggleRowClassName}`}>
              <span>
                {config.autoDetect
                  ? t("editor.shell.enabled")
                  : t("editor.shell.disabled")}
              </span>
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent-primary"
                checked={config.autoDetect}
                onChange={(event) =>
                  onChange({ ...config, autoDetect: event.target.checked })
                }
              />
            </label>
          </div>

          <div className="rounded-card border border-border bg-surface-softer p-3">
            <div className="text-sm font-semibold text-foreground">
              {t("editor.shell.fields.strictProbe")}
            </div>
            <div className="mt-1 text-xs leading-5 text-muted-foreground">
              {t("editor.shell.strictProbeHint")}
            </div>
            <label className={`mt-3 ${editorToggleRowClassName}`}>
              <span>
                {config.strictProbe
                  ? t("editor.shell.enabled")
                  : t("editor.shell.disabled")}
              </span>
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent-primary"
                checked={config.strictProbe}
                onChange={(event) =>
                  onChange({ ...config, strictProbe: event.target.checked })
                }
              />
            </label>
          </div>

          <ConfigFormField label={t("editor.shell.fields.path")}>
            <input
              className={editorControlClassName}
              value={config.path}
              onChange={(event) =>
                onChange({ ...config, path: event.target.value })
              }
              placeholder={t("editor.shell.placeholders.path")}
            />
          </ConfigFormField>

          <div className="grid gap-3 md:grid-cols-2">
            <ConfigFormField label={t("editor.shell.fields.probeTimeout")}>
              <input
                className={editorControlClassName}
                value={config.probeTimeout}
                onChange={(event) =>
                  onChange({ ...config, probeTimeout: event.target.value })
                }
                placeholder={t("editor.shell.placeholders.probeTimeout")}
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.shell.fields.scriptDelivery")}>
              <select
                className={editorControlClassName}
                value={config.scriptDelivery}
                onChange={(event) =>
                  onChange({ ...config, scriptDelivery: event.target.value })
                }
              >
                <option value="stdin">
                  {t("editor.shell.deliveryOptions.stdin")}
                </option>
                <option value="argv">
                  {t("editor.shell.deliveryOptions.argv")}
                </option>
              </select>
            </ConfigFormField>
          </div>
        </div>

        <div className="mt-4 space-y-2">
          <div className="flex items-center justify-between">
            <div className="text-sm font-semibold text-foreground">
              {t("editor.shell.prefer.title")}
            </div>
            <span className="text-xs text-muted-foreground">
              {t("editor.shell.prefer.hint")}
            </span>
          </div>
          {config.prefer.length === 0 ? (
            <SettingsNoticeCard tone="neutral">
              {t("editor.shell.prefer.empty")}
            </SettingsNoticeCard>
          ) : (
            <ul className="space-y-1">
              {config.prefer.map((id, index) => (
                <li
                  key={id}
                  className="flex items-center justify-between rounded-card border border-border bg-surface-softer px-3 py-2"
                >
                  <span className="font-mono text-sm">{id}</span>
                  <SettingsActionGroup>
                    <SettingsActionButton
                      variant="ghost"
                      icon={<ArrowUpIcon size={14} />}
                      label={t("editor.shell.prefer.moveUp")}
                      onClick={() =>
                        onChange({
                          ...config,
                          prefer: moveShellPreferItem(config.prefer, index, -1),
                        })
                      }
                    />
                    <SettingsActionButton
                      variant="ghost"
                      icon={<ArrowDownIcon size={14} />}
                      label={t("editor.shell.prefer.moveDown")}
                      onClick={() =>
                        onChange({
                          ...config,
                          prefer: moveShellPreferItem(config.prefer, index, 1),
                        })
                      }
                    />
                    <SettingsActionButton
                      variant="ghost"
                      icon={<XIcon size={14} />}
                      label={t("editor.shell.prefer.remove")}
                      onClick={() =>
                        onChange({
                          ...config,
                          prefer: removeShellPreferItem(config.prefer, id),
                        })
                      }
                    />
                  </SettingsActionGroup>
                </li>
              ))}
            </ul>
          )}
          {availableToAdd.length > 0 ? (
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs text-muted-foreground">
                {t("editor.shell.prefer.addLabel")}
              </span>
              {availableToAdd.map((id) => (
                <SettingsActionButton
                  key={id}
                  variant="secondary"
                  label={id}
                  onClick={() =>
                    onChange({
                      ...config,
                      prefer: addShellPreferItem(config.prefer, id),
                    })
                  }
                />
              ))}
            </div>
          ) : null}
          <div className="text-xs text-muted-foreground">
            {t("editor.shell.prefer.current", {
              chain: formatShellPrefer(config.prefer) || t("editor.shell.prefer.builtin"),
            })}
          </div>
        </div>
      </SettingsPanelCard>

      <SettingsPanelCard
        title={
          <span className="text-base">{t("editor.shell.probe.title")}</span>
        }
        icon={
          <SettingsPanelIcon>
            <RefreshCcwIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.shell.probe.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsActionButton
            variant="secondary"
            label={
              probing
                ? t("editor.shell.probe.running")
                : t("editor.shell.probe.action")
            }
            onClick={handleProbe}
          />
        }
      >
        {effective ? (
          <div className="mb-3 rounded-card border border-border bg-surface-softer p-3 text-sm">
            <div className="font-semibold">
              {t("editor.shell.probe.effective", {
                shell: effective.id || effective.dialect,
                path: effective.path,
              })}
            </div>
            <div className="mt-1 text-xs text-muted-foreground">
              {t("editor.shell.probe.effectiveMeta", {
                dialect: effective.dialect,
                source: effective.source,
                ms: String(effective.probe_ms ?? 0),
              })}
            </div>
          </div>
        ) : (
          <SettingsNoticeCard tone="neutral">
            {t("editor.shell.probe.idle")}
          </SettingsNoticeCard>
        )}

        {probeError ? (
          <div className="mb-3">
            <SettingsNoticeCard tone="warning">{probeError}</SettingsNoticeCard>
          </div>
        ) : null}

        {candidates.length > 0 ? (
          <ul className="space-y-1">
            {candidates.map((candidate) => (
              <li
                key={candidate.id}
                className="flex flex-wrap items-center justify-between gap-2 rounded-card border border-border px-3 py-2 text-sm"
              >
                <span className="flex items-center gap-2 font-mono">
                  {candidate.status === "pass" ? (
                    <CheckCircle2Icon size={14} className="text-accent-primary" />
                  ) : candidate.status === "fail" ? (
                    <AlertTriangleIcon size={14} className="text-accent-orange" />
                  ) : (
                    <XIcon size={14} className="text-muted-foreground" />
                  )}
                  {candidate.id}
                </span>
                <span className="flex-1 truncate text-xs text-muted-foreground">
                  {candidate.path || candidate.reason || "--"}
                </span>
                <span className="text-xs text-muted-foreground">
                  {candidate.status === "pass" && candidate.probe_ms
                    ? `${candidate.probe_ms}ms`
                    : candidate.status.toUpperCase()}
                </span>
              </li>
            ))}
          </ul>
        ) : null}
      </SettingsPanelCard>
    </div>
  );
}
