import { useTranslation } from "react-i18next";

import type { TFunction } from "i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import type { ProviderProbeResult } from "@/types/runtime";

import { collectProbeSupportedModels } from "@/api/runtime";

import {
  canResolveProviderOpsTarget,
  groupProbeResultsByModel,
  resolveProbeModels,
  summarizeProbeResults,
} from "../provider-ops-utils";
import { type ProviderDraftInput } from "../runtime-provider-domain-form-utils";
import { SettingsNoticeCard } from "../settings-notice-card";

export type ProviderModelsAction = "auto-import" | "fetch" | "probe" | null;

type ProviderModelsSectionProps = {
  assumedModelIDs: string[];
  busy: ProviderModelsAction;
  draft: ProviderDraftInput;
  editingProviderName: string | null;
  error: string | null;
  modelsNotice: string | null;
  onAutoImport: () => void;
  onFetchModels: () => void;
  onMergeModels: (modelIDs: string[]) => void;
  onProbeModels: () => void;
  probeResult: ProviderProbeResult | null;
};

export function ProviderModelsSection({
  assumedModelIDs,
  busy,
  draft,
  editingProviderName,
  error,
  modelsNotice,
  onAutoImport,
  onFetchModels,
  onMergeModels,
  onProbeModels,
  probeResult,
}: ProviderModelsSectionProps) {
  const { t } = useTranslation("runtimeConfig");
  const probeSummary = summarizeProbeResults(probeResult);
  const probeRows = groupProbeResultsByModel(probeResult);
  const verifiedModelIDs = collectProbeSupportedModels(probeResult?.results);
  const disabled = busy !== null;
  const probeTargets = resolveProbeModels(assumedModelIDs, draft);
  // 新 provider（未保存）必须先有 base_url：否则后端既无法从快照补齐、也无法
  // 探测协议。已保存 provider 只传 name 即可，故仅在无名称时要求 base_url。
  const hasResolvableTarget = canResolveProviderOpsTarget({
    baseUrl: draft.baseUrl,
    providerName: editingProviderName,
  });

  return (
    <div className="rounded-card border border-border bg-surface-softer p-3">
      <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="app-text-13 font-semibold text-foreground">
            {t("editor.providers.models.title")}
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {t("editor.providers.models.description")}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="secondary"
            disabled={disabled || !hasResolvableTarget}
            onClick={() => void onFetchModels()}
          >
            {busy === "fetch"
              ? t("editor.providers.models.fetching")
              : t("editor.providers.models.fetch")}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={disabled || !hasResolvableTarget}
            onClick={() => void onAutoImport()}
          >
            {busy === "auto-import"
              ? t("editor.providers.models.autoImporting")
              : t("editor.providers.models.autoImport")}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={disabled || probeTargets.length === 0 || !hasResolvableTarget}
            onClick={() => void onProbeModels()}
          >
            {busy === "probe"
              ? t("editor.providers.models.probing")
              : t("editor.providers.models.probe")}
          </Button>
        </div>
      </div>

      <div className="text-xs text-muted-foreground">
        {editingProviderName
          ? t("editor.providers.models.savedHint", { name: editingProviderName })
          : t("editor.providers.models.draftHint")}
      </div>

      {assumedModelIDs.length > 0 ? (
        <div className="mt-3 rounded-card border border-border bg-surface p-2.5">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="text-xs text-muted-foreground">
              {t("editor.providers.models.assumedCount", {
                count: assumedModelIDs.length,
              })}
            </div>
            <Button
              size="sm"
              variant="ghost"
              disabled={disabled}
              onClick={() => void onMergeModels(assumedModelIDs)}
            >
              {t("editor.providers.models.mergeAssumed")}
            </Button>
          </div>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {assumedModelIDs.map((modelId) => (
              <Badge key={modelId}>{modelId}</Badge>
            ))}
          </div>
          <div className="mt-2 text-xs text-muted-foreground">
            {t("editor.providers.models.assumedHint")}
          </div>
        </div>
      ) : null}

      {modelsNotice ? (
        <SettingsNoticeCard tone="neutral" className="mt-3">
          {modelsNotice}
        </SettingsNoticeCard>
      ) : null}
      {error ? (
        <SettingsNoticeCard tone="warning-soft" className="mt-3">
          {error}
        </SettingsNoticeCard>
      ) : null}

      {probeRows.length > 0 ? (
        <div className="mt-3 rounded-card border border-border bg-surface p-2.5">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="text-xs text-muted-foreground">
              {t("editor.providers.models.probeSummary", {
                total: String(probeSummary.total),
                ok: String(probeSummary.ok),
                unsupported: String(probeSummary.unsupported),
                error: String(probeSummary.error),
              })}
            </div>
            {verifiedModelIDs.length > 0 ? (
              <Button
                size="sm"
                variant="ghost"
                disabled={disabled}
                onClick={() => void onMergeModels(verifiedModelIDs)}
              >
                {t("editor.providers.models.mergeVerified", {
                  count: verifiedModelIDs.length,
                })}
              </Button>
            ) : null}
          </div>
          <div className="mt-2 space-y-1.5">
            {probeRows.map((row) => (
              <div
                key={row.modelId}
                className="flex flex-wrap items-center gap-1.5 text-xs"
              >
                <span className="font-mono text-foreground">{row.modelId}</span>
                {row.probes.map((probe) => (
                  <Badge
                    key={`${row.modelId}:${probe.protocol}`}
                  >
                    {probe.protocol || "?"} · {probeVerdictLabel(probe.verdict, t)}
                    {probe.status_code ? ` (${probe.status_code})` : ""}
                  </Badge>
                ))}
              </div>
            ))}
          </div>
          {probeRows.some((row) => row.probes.some((probe) => probe.message)) ? (
            <div className="mt-2 space-y-0.5 text-xs text-muted-foreground">
              {probeRows.flatMap((row) =>
                row.probes
                  .filter((probe) => probe.message)
                  .map((probe) => (
                    <div key={`${row.modelId}:${probe.protocol}:message`}>
                      {row.modelId} · {probe.protocol || "?"}: {probe.message}
                    </div>
                  )),
              )}
            </div>
          ) : null}
          <div className="mt-2 text-xs text-muted-foreground">
            {t("editor.providers.models.probeHint")}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function probeVerdictLabel(
  verdict: string,
  t: TFunction<"runtimeConfig">,
): string {
  switch (verdict) {
    case "ok":
      return t("editor.providers.models.verdictOk");
    case "unsupported":
      return t("editor.providers.models.verdictUnsupported");
    default:
      return t("editor.providers.models.verdictUnknown");
  }
}
