import {
  isConfigRecord,
  normalizeStringArrayInput,
} from "./runtime-provider-config-utils";
import { type TransformerModifierScope } from "./runtime-transformer-domain-utils";

export type TransformerModifierDraftInput = {
  enabled: boolean;
  extraJson: string;
  modelsText: string;
  paramsJson: string;
  type: string;
};

export type BuildTransformerModifierResult = {
  error: string | null;
  record: Record<string, unknown> | null;
};

export function buildTransformerModifierRecordFromDraft(
  draft: TransformerModifierDraftInput,
  scope: TransformerModifierScope,
): BuildTransformerModifierResult {
  const type = draft.type.trim();
  if (!type) {
    return {
      error: `${scope === "request" ? "请求" : "响应"}修改器类型不能为空。`,
      record: null,
    };
  }

  const paramsResult = parseJsonRecord(
    draft.paramsJson,
    "`params JSON` 必须是 JSON 对象。",
  );
  if (!paramsResult.record) {
    return { error: paramsResult.error, record: null };
  }

  const extraResult = parseJsonRecord(
    draft.extraJson,
    "`扩展字段 JSON` 必须是 JSON 对象。",
  );
  if (!extraResult.record) {
    return { error: extraResult.error, record: null };
  }

  const models = normalizeStringArrayInput(draft.modelsText);

  return {
    error: null,
    record: {
      ...extraResult.record,
      type,
      enabled: draft.enabled,
      ...(models.length > 0 ? { models } : {}),
      ...(Object.keys(paramsResult.record).length > 0
        ? { params: paramsResult.record }
        : {}),
    },
  };
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
