// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { removeConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { getRuntimeDefaultProvider, getRuntimeProviderRecord, listRuntimeProviderSummaries } from "../../runtime-provider-config-utils";
import { type ProviderDraftInput, buildProviderRecordFromDraft } from "../../runtime-provider-domain-form-utils";
import { type ConfigEditorCore } from "../use-config-core";


export function createProvidersDomain(core: ConfigEditorCore) {
  const {
    providerGroups,
    providers,
    setDraftParsed,
    setError,
    setStatusMessage,
    t,
  } = core;

  function handleSetDefaultProvider(name: string) {
    setDraftParsed((current: unknown) =>
      setConfigValueAtPath(current, ["providers", "default_provider"], name),
    );
    setStatusMessage(t("editor.messages.defaultProviderSet", { name }));
  }

  function handleApplyProviderAccountFields(
    name: string,
    fields: Record<string, unknown>,
  ) {
    setDraftParsed((current: unknown) => {
      const provider = getRuntimeProviderRecord(current, name);
      if (!provider) {
        return current;
      }
      return setConfigValueAtPath(current, ["providers", "items", name], {
        ...provider,
        ...fields,
      });
    });
    setError(null);
    setStatusMessage(t("editor.messages.providerUpdated", { name }));
  }

  function handleSaveProvider(
    draft: ProviderDraftInput,
    previousName: string | null,
  ) {
    const name = draft.name.trim();
    if (!name) {
      return t("editor.validation.providerNameRequired");
    }
    if (
      providers.some(
        (provider) => provider.name === name && provider.name !== previousName,
      )
    ) {
      return t("editor.validation.providerExists", { name });
    }

    const nextProvider = buildProviderRecordFromDraft(draft);
    if (!nextProvider.record) {
      return nextProvider.error ?? t("editor.validation.providerInvalid");
    }

    setDraftParsed((current: unknown) => {
      const previousDefault = getRuntimeDefaultProvider(current);
      let nextValue = current;
      if (previousName && previousName !== name) {
        nextValue = removeConfigValueAtPath(nextValue, [
          "providers",
          "items",
          previousName,
        ]);
      }
      nextValue = setConfigValueAtPath(
        nextValue,
        ["providers", "items", name],
        nextProvider.record,
      );
      if (
        draft.setAsDefault ||
        !getRuntimeDefaultProvider(nextValue) ||
        (previousName && previousDefault === previousName)
      ) {
        nextValue = setConfigValueAtPath(
          nextValue,
          ["providers", "default_provider"],
          name,
        );
      }
      return nextValue;
    });
    setError(null);
    setStatusMessage(
      previousName
        ? t("editor.messages.providerUpdated", { name })
        : t("editor.messages.providerCreated", { name }),
    );
    return null;
  }

  function handleDeleteProvider(name: string) {
    const relatedGroups = providerGroups.filter((group) =>
      group.providers.some((provider) => provider.name === name),
    );
    const relatedHint =
      relatedGroups.length > 0
        ? t("editor.messages.relatedProviderGroups", {
            count: relatedGroups.length,
          })
        : "";
    if (!window.confirm(t("editor.messages.confirmDeleteProvider", { name, relatedHint }))) {
      return;
    }

    setDraftParsed((current: unknown) => {
      let nextValue = removeConfigValueAtPath(current, [
        "providers",
        "items",
        name,
      ]);
      if (getRuntimeDefaultProvider(nextValue) === name) {
        const nextProviders = listRuntimeProviderSummaries(nextValue);
        nextValue = setConfigValueAtPath(
          nextValue,
          ["providers", "default_provider"],
          nextProviders[0]?.name ?? "",
        );
      }
      return nextValue;
    });
    setStatusMessage(t("editor.messages.providerDeleted", { name }));
  }


  return {
    handleSetDefaultProvider,
    handleApplyProviderAccountFields,
    handleSaveProvider,
    handleDeleteProvider,
  };
}

export type ProvidersDomain = ReturnType<typeof createProvidersDomain>;
