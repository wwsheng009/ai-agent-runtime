import {
  isConfigRecord,
  normalizeStringArrayInput,
} from "./runtime-provider-config-utils";

export type RouteDraftInput = {
  excludeModelRegexesText: string;
  excludeModelsText: string;
  extraJson: string;
  group: string;
  matchModelRegexesText: string;
  matchModelsText: string;
  matchPath: string;
  matchType: string;
  pipeline?: string;
  priority: string;
  protocol: string;
};

export function buildRouteRecordFromDraft(
  draft: RouteDraftInput,
): { error: string | null; record: Record<string, unknown> | null } {
  const extraFields = parseJsonRecord(
    draft.extraJson,
    "`扩展字段 JSON` 必须是 JSON 对象。",
  );
  if (!extraFields.record) {
    return { error: extraFields.error, record: null };
  }

  const matchPath = draft.matchPath.trim();
  if (!matchPath) {
    return { error: "match_path 不能为空。", record: null };
  }

  const group = draft.group.trim();
  if (!group) {
    return { error: "group 不能为空。", record: null };
  }

  return {
    error: null,
    record: {
      ...extraFields.record,
      match_path: matchPath,
      match_type: draft.matchType.trim() || "prefix",
      group,
      ...buildOptionalStringRecord({
        pipeline: draft.pipeline,
        protocol: draft.protocol,
      }),
      ...buildOptionalScalarRecord({
        priority: draft.priority,
      }),
      ...buildOptionalArrayRecord({
        match_models: draft.matchModelsText,
        match_model_regexes: draft.matchModelRegexesText,
        exclude_models: draft.excludeModelsText,
        exclude_model_regexes: draft.excludeModelRegexesText,
      }),
    },
  };
}

function buildOptionalStringRecord(
  entries: Record<string, string | undefined>,
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(entries).flatMap(([key, value]) => {
      const trimmed = typeof value === "string" ? value.trim() : "";
      return trimmed ? [[key, trimmed]] : [];
    }),
  );
}

function buildOptionalScalarRecord(
  entries: Record<string, string>,
): Record<string, unknown> {
  const pairs: Array<[string, unknown]> = [];

  for (const [key, value] of Object.entries(entries)) {
    const trimmed = value.trim();
    if (!trimmed) {
      continue;
    }
    if (/^-?\d+(?:\.\d+)?$/.test(trimmed)) {
      pairs.push([key, Number(trimmed)]);
      continue;
    }
    pairs.push([key, trimmed]);
  }

  return Object.fromEntries(pairs);
}

function buildOptionalArrayRecord(
  entries: Record<string, string>,
): Record<string, string[]> {
  return Object.fromEntries(
    Object.entries(entries).flatMap(([key, value]) => {
      const items = normalizeStringArrayInput(value);
      return items.length > 0 ? [[key, items]] : [];
    }),
  );
}

function parseJsonRecord(
  value: string,
  errorMessage: string,
): { error: string | null; record: Record<string, unknown> | null } {
  const trimmed = value.trim();
  if (!trimmed) {
    return { error: null, record: {} };
  }

  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (!isConfigRecord(parsed)) {
      return { error: errorMessage, record: null };
    }
    return { error: null, record: parsed };
  } catch {
    return { error: errorMessage, record: null };
  }
}
