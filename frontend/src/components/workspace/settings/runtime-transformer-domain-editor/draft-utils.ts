// 由 components/workspace/settings/runtime-transformer-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeTransformerModifierSummary } from "../runtime-transformer-domain-utils";
import { type TransformerModifierDraftInput } from "../runtime-transformer-domain-form-utils";
import { isConfigRecord } from "../runtime-provider-config-utils";

const KNOWN_TRANSFORMER_MODIFIER_KEYS = new Set([
  "type",
  "enabled",
  "models",
  "params",
]);

export function createTransformerModifierDraft(
  item: RuntimeTransformerModifierSummary | null,
): TransformerModifierDraftInput {
  if (!item) {
    return {
      type: "",
      enabled: true,
      modelsText: "",
      paramsJson: "",
      extraJson: "",
    };
  }

  const params = isConfigRecord(item.raw.params) ? item.raw.params : {};
  const extraFields = Object.fromEntries(
    Object.entries(item.raw).filter(([key]) => !KNOWN_TRANSFORMER_MODIFIER_KEYS.has(key)),
  );

  return {
    type: item.type,
    enabled: item.enabled,
    modelsText: item.models.join("\n"),
    paramsJson: stringifyJsonObject(params),
    extraJson: stringifyJsonObject(extraFields),
  };
}

function stringifyJsonObject(value: Record<string, unknown>) {
  return Object.keys(value).length > 0 ? JSON.stringify(value, null, 2) : "";
}
