/**
 * 「模型发现与探测」动作 hook：从 runtime-provider-domain-editor.tsx 机械抽出，
 * 让编辑器组件只做组装（同时守住 verify-max-lines 的 500 非空行门禁）。
 * 语义不变：所有动作只改当前编辑草稿，保存仍走既有保存链路。
 */

import { type Dispatch, type SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import {
  appendProviderModels,
  autoImportRuntimeProvider,
  fetchRuntimeProviderModels,
  probeRuntimeProviderModels,
} from "@/api/runtime";
import type { ProviderProbeResult } from "@/types/runtime";

import {
  buildProviderOpsRequestFromDraft,
  canResolveProviderOpsTarget,
  joinProviderOpsWarnings,
  normalizeProviderModelIDs,
  parseSupportedModelsText,
  providerAutoImportPatch,
  providerModelsPatch,
  resolveProbeModels,
  summarizeProbeResults,
} from "../provider-ops-utils";
import { type ProviderDraftInput } from "../runtime-provider-domain-form-utils";

import { describeAccountError } from "./draft-utils";
import { type ProviderModelsAction } from "./provider-models-section";

type ProviderModelsActionsOptions = {
  assumedModelIDs: string[];
  draft: ProviderDraftInput;
  editingProviderName: string | null;
  setAssumedModelIDs: Dispatch<SetStateAction<string[]>>;
  setDraft: Dispatch<SetStateAction<ProviderDraftInput>>;
  setModelsBusy: Dispatch<SetStateAction<ProviderModelsAction>>;
  setModelsError: Dispatch<SetStateAction<string | null>>;
  setModelsNotice: Dispatch<SetStateAction<string | null>>;
  setProbeResult: Dispatch<SetStateAction<ProviderProbeResult | null>>;
};

export function useProviderModelsActions({
  assumedModelIDs,
  draft,
  editingProviderName,
  setAssumedModelIDs,
  setDraft,
  setModelsBusy,
  setModelsError,
  setModelsNotice,
  setProbeResult,
}: ProviderModelsActionsOptions) {
  const { t } = useTranslation("runtimeConfig");

  function describeModelsError(error: unknown, fallback: string) {
    return describeAccountError(error, fallback);
  }

  function modelsWarningsSuffix(warnings: string[] | undefined) {
    const joined = joinProviderOpsWarnings(warnings);
    return joined ? t("editor.providers.models.warningsSuffix", { warnings: joined }) : "";
  }

  async function handleFetchModels() {
    if (!canResolveProviderOpsTarget({ baseUrl: draft.baseUrl, providerName: editingProviderName })) {
      setModelsError(t("editor.providers.models.fetchRequiresBaseUrl"));
      return;
    }

    setModelsBusy("fetch");
    setModelsError(null);
    setModelsNotice(null);
    try {
      const result = await fetchRuntimeProviderModels(
        buildProviderOpsRequestFromDraft(draft, editingProviderName),
      );
      const assumed = normalizeProviderModelIDs(result.assumed_model_ids);
      const fetched = normalizeProviderModelIDs(result.model_ids);
      setAssumedModelIDs(assumed);
      setProbeResult(null);
      const patch = providerModelsPatch(result);
      if (patch) {
        setDraft((current) => ({ ...current, ...patch }));
      }
      const warnings = modelsWarningsSuffix(result.warnings);
      if (fetched.length > 0) {
        setModelsNotice(
          t("editor.providers.models.fetchSuccess", {
            count: fetched.length,
            endpoint: result.endpoint || "models",
            warnings,
          }),
        );
      } else {
        setModelsNotice(
          `${t("editor.providers.models.fetchEmpty")}${warnings}`.trim(),
        );
      }
    } catch (error) {
      setModelsError(
        describeModelsError(error, t("editor.providers.models.fetchFailed")),
      );
    } finally {
      setModelsBusy(null);
    }
  }

  async function handleAutoImport() {
    if (!canResolveProviderOpsTarget({ baseUrl: draft.baseUrl, providerName: editingProviderName })) {
      setModelsError(t("editor.providers.models.autoImportRequiresBaseUrl"));
      return;
    }

    setModelsBusy("auto-import");
    setModelsError(null);
    setModelsNotice(null);
    try {
      const result = await autoImportRuntimeProvider({
        ...buildProviderOpsRequestFromDraft(draft, editingProviderName),
        default_model: draft.defaultModel.trim() || undefined,
      });
      setDraft((current) => ({ ...current, ...providerAutoImportPatch(result) }));
      const imported = normalizeProviderModelIDs(result.supported_models);
      setAssumedModelIDs(imported);
      setProbeResult(null);
      setModelsNotice(
        t("editor.providers.models.autoImportSuccess", {
          warnings: modelsWarningsSuffix(result.warnings),
        }),
      );
    } catch (error) {
      setModelsError(
        describeModelsError(error, t("editor.providers.models.autoImportFailed")),
      );
    } finally {
      setModelsBusy(null);
    }
  }

  function handleMergeModels(modelIDs: string[]) {
    const current = parseSupportedModelsText(draft.supportedModelsText);
    const merged = appendProviderModels(current, modelIDs);
    if (merged.length === current.length) {
      return;
    }
    setDraft((currentDraft) => ({
      ...currentDraft,
      supportedModelsText: merged.join("\n"),
    }));
    setModelsError(null);
    setModelsNotice(
      t("editor.providers.models.mergeAssumedNotice", {
        count: merged.length - current.length,
      }),
    );
  }

  async function handleProbeModels() {
    const models = resolveProbeModels(assumedModelIDs, draft);
    if (models.length === 0) {
      setModelsError(t("editor.providers.models.probeRequiresModels"));
      return;
    }
    setModelsBusy("probe");
    setModelsError(null);
    setModelsNotice(null);
    try {
      const result = await probeRuntimeProviderModels({
        ...buildProviderOpsRequestFromDraft(draft, editingProviderName),
        models,
      });
      setProbeResult(result);
      const summary = summarizeProbeResults(result);
      setModelsNotice(
        t("editor.providers.models.probeSuccess", {
          total: String(summary.total),
          ok: String(summary.ok),
        }),
      );
    } catch (error) {
      setProbeResult(null);
      setModelsError(
        describeModelsError(error, t("editor.providers.models.probeFailed")),
      );
    } finally {
      setModelsBusy(null);
    }
  }

  return {
    handleAutoImport,
    handleFetchModels,
    handleMergeModels,
    handleProbeModels,
  };
}
