// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { type TransformerModifierDraftInput, buildTransformerModifierRecordFromDraft } from "../../runtime-transformer-domain-form-utils";
import { type RuntimeTransformerConfigSummary, type TransformerModifierScope } from "../../runtime-transformer-domain-utils";
import { type ConfigEditorCore } from "../use-config-core";


export function createTransformerDomain(core: ConfigEditorCore) {
  const {
    setDraftParsed,
    setError,
    setStatusMessage,
    t,
  } = core;

  function handleTransformerConfigChange(
    nextTransformerConfig: RuntimeTransformerConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentTransformerValue = getConfigValueAtPath(current, [
        "transformer",
      ]);
      const currentTransformer = isConfigRecord(currentTransformerValue)
        ? currentTransformerValue
        : {};

      return setConfigValueAtPath(current, ["transformer"], {
        ...currentTransformer,
        high_perf: nextTransformerConfig.highPerf,
        http_transform_stage_enabled:
          nextTransformerConfig.httpTransformStageEnabled,
        cache_adapters: nextTransformerConfig.cacheAdapters,
        stream_null_filter: nextTransformerConfig.streamNullFilter,
      });
    });
  }

  function handleSaveTransformerModifier(
    scope: TransformerModifierScope,
    draft: TransformerModifierDraftInput,
    editingIndex: number | null,
  ) {
    const nextModifier = buildTransformerModifierRecordFromDraft(draft, scope);
    const modifierRecord = nextModifier.record;
    if (!modifierRecord) {
      return nextModifier.error ?? t("editor.validation.transformerModifierInvalid");
    }

    setDraftParsed((current: unknown) => {
      const currentTransformerValue = getConfigValueAtPath(current, [
        "transformer",
      ]);
      const currentTransformer = isConfigRecord(currentTransformerValue)
        ? currentTransformerValue
        : {};
      const currentBodyModifiers = isConfigRecord(
        currentTransformer.body_modifiers,
      )
        ? currentTransformer.body_modifiers
        : {};
      const currentScopeItems = Array.isArray(currentBodyModifiers[scope])
        ? [...currentBodyModifiers[scope]]
        : [];

      if (editingIndex == null) {
        currentScopeItems.push(modifierRecord);
      } else {
        currentScopeItems[editingIndex] = modifierRecord;
      }

      return setConfigValueAtPath(current, ["transformer"], {
        ...currentTransformer,
        body_modifiers: {
          ...currentBodyModifiers,
          [scope]: currentScopeItems,
        },
      });
    });
    setError(null);
    setStatusMessage(
      editingIndex == null
        ? t("editor.messages.transformerModifierCreated", {
            scopeLabel: t(`editor.scopes.${scope}`),
          })
        : t("editor.messages.transformerModifierUpdated", {
            scopeLabel: t(`editor.scopes.${scope}`),
            index: String(editingIndex + 1),
          }),
    );
    return null;
  }

  function handleDeleteTransformerModifier(
    scope: TransformerModifierScope,
    index: number,
  ) {
    if (
      !window.confirm(
        t("editor.messages.confirmDeleteTransformerModifier", {
          index: String(index + 1),
          scopeLabel: t(`editor.scopes.${scope}`),
        }),
      )
    ) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentTransformerValue = getConfigValueAtPath(current, [
        "transformer",
      ]);
      const currentTransformer = isConfigRecord(currentTransformerValue)
        ? currentTransformerValue
        : {};
      const currentBodyModifiers = isConfigRecord(
        currentTransformer.body_modifiers,
      )
        ? currentTransformer.body_modifiers
        : {};
      const currentScopeItems = Array.isArray(currentBodyModifiers[scope])
        ? [...currentBodyModifiers[scope]]
        : [];
      currentScopeItems.splice(index, 1);

      return setConfigValueAtPath(current, ["transformer"], {
        ...currentTransformer,
        body_modifiers: {
          ...currentBodyModifiers,
          [scope]: currentScopeItems,
        },
      });
    });
    setStatusMessage(
      t("editor.messages.transformerModifierDeleted", {
        scopeLabel: t(`editor.scopes.${scope}`),
        index: String(index + 1),
      }),
    );
  }

  function handleMoveTransformerModifier(
    scope: TransformerModifierScope,
    index: number,
    direction: "up" | "down",
  ) {
    setDraftParsed((current: unknown) => {
      const currentTransformerValue = getConfigValueAtPath(current, [
        "transformer",
      ]);
      const currentTransformer = isConfigRecord(currentTransformerValue)
        ? currentTransformerValue
        : {};
      const currentBodyModifiers = isConfigRecord(
        currentTransformer.body_modifiers,
      )
        ? currentTransformer.body_modifiers
        : {};
      const currentScopeItems = Array.isArray(currentBodyModifiers[scope])
        ? [...currentBodyModifiers[scope]]
        : [];
      const targetIndex = direction === "up" ? index - 1 : index + 1;

      if (
        index < 0 ||
        index >= currentScopeItems.length ||
        targetIndex < 0 ||
        targetIndex >= currentScopeItems.length
      ) {
        return current;
      }

      const [item] = currentScopeItems.splice(index, 1);
      currentScopeItems.splice(targetIndex, 0, item);

      return setConfigValueAtPath(current, ["transformer"], {
        ...currentTransformer,
        body_modifiers: {
          ...currentBodyModifiers,
          [scope]: currentScopeItems,
        },
      });
    });
    setStatusMessage(
      direction === "up"
        ? t("editor.messages.transformerModifierMovedUp", {
            scopeLabel: t(`editor.scopes.${scope}`),
            index: String(index + 1),
          })
        : t("editor.messages.transformerModifierMovedDown", {
            scopeLabel: t(`editor.scopes.${scope}`),
            index: String(index + 1),
          }),
    );
  }


  return {
    handleTransformerConfigChange,
    handleSaveTransformerModifier,
    handleDeleteTransformerModifier,
    handleMoveTransformerModifier,
  };
}

export type TransformerDomain = ReturnType<typeof createTransformerDomain>;
