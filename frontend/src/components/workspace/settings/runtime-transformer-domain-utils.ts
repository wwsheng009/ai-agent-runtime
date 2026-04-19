import { isConfigRecord } from "./runtime-provider-config-utils";

const KNOWN_TRANSFORMER_MODIFIER_KEYS = new Set([
  "type",
  "enabled",
  "models",
  "params",
]);

export type TransformerModifierScope = "request" | "response";

export type RuntimeTransformerConfigSummary = {
  cacheAdapters: boolean;
  highPerf: boolean;
  httpTransformStageEnabled: boolean;
  streamNullFilter: boolean;
};

export type RuntimeTransformerModifierSummary = {
  enabled: boolean;
  extraFieldCount: number;
  id: string;
  index: number;
  modelCount: number;
  models: string[];
  paramsKeyCount: number;
  raw: Record<string, unknown>;
  scope: TransformerModifierScope;
  type: string;
};

export function getRuntimeTransformerConfig(
  value: unknown,
): RuntimeTransformerConfigSummary {
  const transformerRoot = getTransformerRoot(value) ?? {};

  return {
    highPerf: Boolean(transformerRoot.high_perf),
    httpTransformStageEnabled: Boolean(
      transformerRoot.http_transform_stage_enabled,
    ),
    cacheAdapters: Boolean(transformerRoot.cache_adapters),
    streamNullFilter: Boolean(transformerRoot.stream_null_filter),
  };
}

export function listRuntimeTransformerModifierSummaries(
  value: unknown,
  scope: TransformerModifierScope,
): RuntimeTransformerModifierSummary[] {
  const bodyModifiers = getBodyModifiersRoot(value);
  const items = Array.isArray(bodyModifiers?.[scope]) ? bodyModifiers[scope] : [];

  return items.flatMap((item, index) =>
    isConfigRecord(item) ? [buildModifierSummary(item, scope, index)] : [],
  );
}

function getTransformerRoot(value: unknown) {
  if (!isConfigRecord(value)) {
    return null;
  }
  return isConfigRecord(value.transformer) ? value.transformer : null;
}

function getBodyModifiersRoot(value: unknown) {
  const transformerRoot = getTransformerRoot(value);
  return isConfigRecord(transformerRoot?.body_modifiers)
    ? transformerRoot.body_modifiers
    : null;
}

function buildModifierSummary(
  raw: Record<string, unknown>,
  scope: TransformerModifierScope,
  index: number,
): RuntimeTransformerModifierSummary {
  const models = readStringArray(raw.models);
  const params = isConfigRecord(raw.params) ? raw.params : {};

  return {
    id: `${scope}:${index}:${readText(raw.type)}`,
    scope,
    index,
    raw,
    type: readText(raw.type),
    enabled: raw.enabled !== false,
    models,
    modelCount: models.length,
    paramsKeyCount: Object.keys(params).length,
    extraFieldCount: Object.keys(raw).filter(
      (key) => !KNOWN_TRANSFORMER_MODIFIER_KEYS.has(key),
    ).length,
  };
}

function readText(value: unknown) {
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number") {
    return String(value);
  }
  return "";
}

function readStringArray(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => (typeof item === "string" ? item.trim() : ""))
    .filter(Boolean);
}
