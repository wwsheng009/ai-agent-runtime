import { isConfigRecord } from "./runtime-provider-config-utils";

export type ProviderQueueProviderDraftInput = {
  extraJson: string;
  maxConcurrency: string;
  provider: string;
  queueSize: string;
  queueTimeout: string;
};

export function buildProviderQueueProviderRecordFromDraft(
  draft: ProviderQueueProviderDraftInput,
): { error: string | null; provider: string | null; record: Record<string, unknown> | null } {
  const provider = draft.provider.trim();
  if (!provider) {
    return { error: "Provider 名称不能为空。", provider: null, record: null };
  }

  const extraFields = parseJsonRecord(
    draft.extraJson,
    "`扩展字段 JSON` 必须是 JSON 对象。",
  );
  if (!extraFields.record) {
    return { error: extraFields.error, provider: null, record: null };
  }

  return {
    error: null,
    provider,
    record: {
      ...extraFields.record,
      ...buildOptionalScalarRecord({
        max_concurrency: draft.maxConcurrency,
        queue_size: draft.queueSize,
      }),
      ...buildOptionalStringRecord({
        queue_timeout: draft.queueTimeout,
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
