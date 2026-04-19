import { isConfigRecord } from "./runtime-provider-config-utils";

export type RateLimitApiKeyDraftInput = {
  apiKeyPattern: string;
  blockDuration: string;
  extraJson: string;
  qpd: string;
  qpm: string;
  qps: string;
};

export type RateLimitPathDraftInput = {
  burst: string;
  extraJson: string;
  path: string;
  requestsPerMinute: string;
};

export function buildRateLimitApiKeyRecordFromDraft(
  draft: RateLimitApiKeyDraftInput,
): { error: string | null; record: Record<string, unknown> | null } {
  const extraFields = parseJsonRecord(
    draft.extraJson,
    "`扩展字段 JSON` 必须是 JSON 对象。",
  );
  if (!extraFields.record) {
    return { error: extraFields.error, record: null };
  }

  const apiKeyPattern = draft.apiKeyPattern.trim();
  if (!apiKeyPattern) {
    return { error: "api_key_pattern 不能为空。", record: null };
  }

  return {
    error: null,
    record: {
      ...extraFields.record,
      api_key_pattern: apiKeyPattern,
      ...buildOptionalScalarRecord({
        qps: draft.qps,
        qpd: draft.qpd,
        qpm: draft.qpm,
      }),
      ...buildOptionalStringRecord({
        block_duration: draft.blockDuration,
      }),
    },
  };
}

export function buildRateLimitPathRecordFromDraft(
  draft: RateLimitPathDraftInput,
): { error: string | null; path: string | null; record: Record<string, unknown> | null } {
  const extraFields = parseJsonRecord(
    draft.extraJson,
    "`扩展字段 JSON` 必须是 JSON 对象。",
  );
  if (!extraFields.record) {
    return { error: extraFields.error, path: null, record: null };
  }

  const path = draft.path.trim();
  if (!path) {
    return { error: "路径不能为空。", path: null, record: null };
  }

  return {
    error: null,
    path,
    record: {
      ...extraFields.record,
      ...buildOptionalScalarRecord({
        requests_per_minute: draft.requestsPerMinute,
        burst: draft.burst,
      }),
    },
  };
}

function buildOptionalStringRecord(
  entries: Record<string, string>,
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(entries).flatMap(([key, value]) => {
      const trimmed = value.trim();
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
