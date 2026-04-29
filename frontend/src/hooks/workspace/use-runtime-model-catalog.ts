import { useEffect, useState } from "react";

import {
  listRuntimeModels,
  type RuntimeModelProviderRecord,
  type RuntimeModelsResponse,
} from "@/lib/runtime-api";
import { subscribeRuntimeModelCatalogInvalidation } from "@/lib/runtime-model-catalog-sync";

const runtimeModelSelectionStorageKey = "workspace.runtime.modelSelection";

export type RuntimeModelSelection = {
  provider: string;
  model: string;
};

function normalizeSelectionValue(value: string | undefined) {
  return value?.trim() ?? "";
}

function hasSelection(
  selection: Partial<RuntimeModelSelection> | null | undefined,
) {
  return Boolean(
    normalizeSelectionValue(selection?.provider) ||
    normalizeSelectionValue(selection?.model),
  );
}

function getBrowserStorage() {
  if (typeof window === "undefined") {
    return null;
  }
  return window.localStorage;
}

export function readStoredRuntimeModelSelection(
  storage: Storage | null | undefined,
): RuntimeModelSelection | null {
  if (!storage) {
    return null;
  }

  try {
    const raw = storage.getItem(runtimeModelSelectionStorageKey);
    if (!raw) {
      return null;
    }

    const parsed = JSON.parse(raw) as Partial<RuntimeModelSelection>;
    if (!hasSelection(parsed)) {
      return null;
    }

    return {
      provider: normalizeSelectionValue(parsed.provider),
      model: normalizeSelectionValue(parsed.model),
    };
  } catch {
    return null;
  }
}

export function writeStoredRuntimeModelSelection(
  storage: Storage | null | undefined,
  selection: RuntimeModelSelection,
) {
  if (!storage) {
    return;
  }

  if (!hasSelection(selection)) {
    storage.removeItem(runtimeModelSelectionStorageKey);
    return;
  }

  storage.setItem(runtimeModelSelectionStorageKey, JSON.stringify(selection));
}

export function findRuntimeProviderRecord(
  providers: RuntimeModelProviderRecord[],
  providerName: string,
) {
  const normalizedProviderName = normalizeSelectionValue(providerName);
  if (!normalizedProviderName) {
    return undefined;
  }
  return providers.find((provider) => provider.name === normalizedProviderName);
}

function getProviderModels(
  provider: RuntimeModelProviderRecord | null | undefined,
) {
  if (!provider || !Array.isArray(provider.models)) {
    return [] as string[];
  }

  return provider.models.filter(
    (model): model is string =>
      typeof model === "string" && model.trim().length > 0,
  );
}

function providerSupportsModel(
  provider: RuntimeModelProviderRecord | null | undefined,
  modelName: string,
) {
  const normalizedModelName = normalizeSelectionValue(modelName);
  if (!provider || !normalizedModelName) {
    return false;
  }

  return (
    getProviderModels(provider).includes(normalizedModelName) ||
    normalizeSelectionValue(provider.default_model) === normalizedModelName
  );
}

export function findProviderForModel(
  providers: RuntimeModelProviderRecord[],
  modelName: string,
) {
  const normalizedModelName = normalizeSelectionValue(modelName);
  if (!normalizedModelName) {
    return "";
  }

  const provider = providers.find(
    (candidate) =>
      getProviderModels(candidate).includes(normalizedModelName) ||
      normalizeSelectionValue(candidate.default_model) === normalizedModelName,
  );
  return provider?.name ?? "";
}

function resolveProviderModel(
  provider: RuntimeModelProviderRecord | undefined,
  preferredModel: string,
  defaultModel: string,
) {
  if (!provider) {
    return "";
  }

  const normalizedPreferredModel = normalizeSelectionValue(preferredModel);
  const normalizedDefaultModel = normalizeSelectionValue(defaultModel);
  const providerDefaultModel = normalizeSelectionValue(provider.default_model);
  const providerModels = getProviderModels(provider);

  if (
    normalizedPreferredModel &&
    providerModels.includes(normalizedPreferredModel)
  ) {
    return normalizedPreferredModel;
  }
  if (
    normalizedDefaultModel &&
    providerModels.includes(normalizedDefaultModel)
  ) {
    return normalizedDefaultModel;
  }
  if (providerDefaultModel) {
    return providerDefaultModel;
  }
  return providerModels[0] ?? "";
}

export function resolveRuntimeModelSelection(
  catalog: RuntimeModelsResponse | null | undefined,
  preferredSelection?: Partial<RuntimeModelSelection> | null,
): RuntimeModelSelection {
  if (!catalog || catalog.providers.length === 0) {
    return { provider: "", model: "" };
  }

  const preferredProvider = normalizeSelectionValue(
    preferredSelection?.provider,
  );
  const preferredModel = normalizeSelectionValue(preferredSelection?.model);
  const defaultProvider = normalizeSelectionValue(catalog.default_provider);
  const defaultModel = normalizeSelectionValue(catalog.default_model);
  const preferredProviderRecord = findRuntimeProviderRecord(
    catalog.providers,
    preferredProvider,
  );

  let providerName =
    preferredProviderRecord &&
    (!preferredModel ||
      providerSupportsModel(preferredProviderRecord, preferredModel))
      ? preferredProvider
      : "";
  if (!providerName) {
    providerName = findProviderForModel(catalog.providers, preferredModel);
  }
  if (
    !providerName &&
    preferredProvider &&
    preferredProviderRecord
  ) {
    providerName = preferredProvider;
  }
  if (!providerName) {
    providerName = findProviderForModel(catalog.providers, defaultModel);
  }
  if (
    !providerName &&
    defaultProvider &&
    findRuntimeProviderRecord(catalog.providers, defaultProvider)
  ) {
    providerName = defaultProvider;
  }
  if (!providerName) {
    providerName = catalog.providers[0]?.name ?? "";
  }

  const provider = findRuntimeProviderRecord(catalog.providers, providerName);
  return {
    provider: providerName,
    model: resolveProviderModel(provider, preferredModel, defaultModel),
  };
}

export function useRuntimeModelCatalog() {
  const [runtimeModels, setRuntimeModels] =
    useState<RuntimeModelsResponse | null>(null);
  const [runtimeModelsError, setRuntimeModelsError] = useState<string | null>(
    null,
  );
  const [runtimeModelsLoading, setRuntimeModelsLoading] = useState(true);
  const [reloadToken, setReloadToken] = useState(0);
  const [selection, setSelection] = useState<RuntimeModelSelection>({
    provider: "",
    model: "",
  });

  useEffect(() => {
    return subscribeRuntimeModelCatalogInvalidation(() => {
      setReloadToken((current) => current + 1);
    });
  }, []);

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      setRuntimeModelsLoading(true);

      try {
        const response = await listRuntimeModels();
        if (cancelled) {
          return;
        }

        setRuntimeModels(response);
        setSelection((currentSelection) =>
          resolveRuntimeModelSelection(
            response,
            hasSelection(currentSelection)
              ? currentSelection
              : readStoredRuntimeModelSelection(getBrowserStorage()),
          ),
        );
        setRuntimeModelsError(null);
      } catch (error) {
        if (cancelled) {
          return;
        }

        setRuntimeModelsError(
          error instanceof Error
            ? error.message
            : "failed to load runtime models",
        );
      } finally {
        if (!cancelled) {
          setRuntimeModelsLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [reloadToken]);

  useEffect(() => {
    if (!hasSelection(selection)) {
      return;
    }

    writeStoredRuntimeModelSelection(getBrowserStorage(), selection);
  }, [selection]);

  const providerRecords = runtimeModels?.providers ?? [];
  const selectedProviderRecord = findRuntimeProviderRecord(
    providerRecords,
    selection.provider,
  );
  const modelOptions = getProviderModels(selectedProviderRecord);

  function setSelectedProvider(providerName: string) {
    setSelection((currentSelection) =>
      resolveRuntimeModelSelection(runtimeModels, {
        provider: providerName,
        model: providerSupportsModel(
          findRuntimeProviderRecord(providerRecords, providerName),
          currentSelection.model,
        )
          ? currentSelection.model
          : "",
      }),
    );
  }

  function setSelectedModel(modelName: string) {
    setSelection((currentSelection) =>
      resolveRuntimeModelSelection(runtimeModels, {
        provider: currentSelection.provider,
        model: modelName,
      }),
    );
  }

  return {
    modelOptions,
    providerOptions: providerRecords.map((provider) => provider.name),
    runtimeModels,
    runtimeModelsError,
    runtimeModelsLoading,
    selectedModel: selection.model,
    selectedProvider: selection.provider,
    setSelectedModel,
    setSelectedProvider,
  };
}
