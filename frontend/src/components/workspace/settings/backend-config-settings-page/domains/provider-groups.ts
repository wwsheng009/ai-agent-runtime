// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type ProviderGroupDraftInput, buildProviderGroupRecordFromDraft } from "../../runtime-provider-groups-domain-form-utils";
import { removeNamedArrayRecord, upsertNamedArrayRecord } from "../named-array-utils";
import { type ConfigEditorCore } from "../use-config-core";


export function createProviderGroupsDomain(core: ConfigEditorCore) {
  const {
    providerGroups,
    setDraftParsed,
    setError,
    setStatusMessage,
    t,
  } = core;

  function handleSaveProviderGroup(
    draft: ProviderGroupDraftInput,
    previousName: string | null,
  ) {
    const name = draft.name.trim();
    if (
      providerGroups.some(
        (group) => group.name === name && group.name !== previousName,
      )
    ) {
      return t("editor.validation.providerGroupExists", { name });
    }

    const nextGroup = buildProviderGroupRecordFromDraft(draft);
    const groupRecord = nextGroup.record;
    if (!groupRecord) {
      return nextGroup.error ?? t("editor.validation.providerGroupInvalid");
    }

    setDraftParsed((current: unknown) =>
      upsertNamedArrayRecord(
        current,
        "provider_groups",
        previousName,
        name,
        groupRecord,
      ),
    );
    setError(null);
    setStatusMessage(
      previousName
        ? t("editor.messages.providerGroupUpdated", { name })
        : t("editor.messages.providerGroupCreated", { name }),
    );
    return null;
  }

  function handleDeleteProviderGroup(name: string) {
    if (!window.confirm(t("editor.messages.confirmDeleteProviderGroup", { name }))) {
      return;
    }
    setDraftParsed((current: unknown) =>
      removeNamedArrayRecord(current, "provider_groups", name),
    );
    setStatusMessage(t("editor.messages.providerGroupDeleted", { name }));
  }


  return {
    handleSaveProviderGroup,
    handleDeleteProviderGroup,
  };
}

export type ProviderGroupsDomain = ReturnType<typeof createProviderGroupsDomain>;
