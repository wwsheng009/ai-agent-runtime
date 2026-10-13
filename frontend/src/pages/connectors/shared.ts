// 连接器配置页的纯逻辑：Schema 查找、表单状态与提交体构造。
//
// 保持无 React 依赖，便于单测覆盖两条密钥纪律：
//   * 密钥留空 = 不提交该字段（后端保持原值）；
//   * 点「清除」= 提交 __clear__（api.CONNECTOR_CLEAR_SECRET）。

import type {
  ConnectorProviderSchema,
  ConnectorSchema,
  ConnectorSubmitPayload,
  ConnectorView,
} from "@/types/runtime";

import { CONNECTOR_CLEAR_SECRET } from "@/api/runtime/connectors";

export interface ConnectorFormState {
  id: string;
  provider: string;
  name: string;
  mode: string;
  enabled: boolean;
  /** 每个 Schema 字段的当前输入；密钥字段为空串 = 保持原值。 */
  values: Record<string, string>;
  /** 显式勾选「清除」的密钥字段。 */
  clearedSecrets: Record<string, boolean>;
}

export interface ConnectorFormError {
  key: string;
  code: "required" | "invalid_id";
}

/** restapi 的 id 约束：字母数字开头，允许 `._-`，最长 64。 */
const ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

export function findProviderSchema(
  schema: ConnectorSchema | null,
  provider: string,
): ConnectorProviderSchema | null {
  if (!schema) {
    return null;
  }
  const trimmed = provider.trim();
  return schema.providers.find((item) => item.provider === trimmed) ?? null;
}

export function defaultValuesFor(
  providerSchema: ConnectorProviderSchema | null,
): Record<string, string> {
  const values: Record<string, string> = {};
  for (const field of providerSchema?.fields ?? []) {
    values[field.key] = field.default ?? "";
  }
  return values;
}

export function createEmptyForm(schema: ConnectorSchema | null): ConnectorFormState {
  const provider = schema?.providers[0]?.provider ?? "";
  const providerSchema = findProviderSchema(schema, provider);
  return {
    id: "",
    provider,
    name: "",
    mode: providerSchema?.modes?.[0] ?? "",
    enabled: true,
    values: defaultValuesFor(providerSchema),
    clearedSecrets: {},
  };
}

/** 编辑既有连接器：settings 回填；密钥字段保持空串（留空=不改）。 */
export function formFromConnector(view: ConnectorView): ConnectorFormState {
  const values: Record<string, string> = {};
  for (const [key, value] of Object.entries(view.settings ?? {})) {
    values[key] = value;
  }
  return {
    id: view.id,
    provider: view.provider,
    name: view.name,
    mode: view.mode,
    enabled: view.enabled,
    values,
    clearedSecrets: {},
  };
}

/** 新建时切换通道：用新 Schema 的默认值重建字段区（保留 id / name）。 */
export function switchProvider(
  form: ConnectorFormState,
  schema: ConnectorSchema | null,
  provider: string,
): ConnectorFormState {
  const providerSchema = findProviderSchema(schema, provider);
  return {
    ...form,
    provider,
    mode: providerSchema?.modes?.[0] ?? "",
    values: defaultValuesFor(providerSchema),
    clearedSecrets: {},
  };
}

/**
 * 前端预校验（后端仍会兜底校验）：
 *   * 新建时要求 id 合法；
 *   * 必填字段：新建时必须填写；编辑时密钥字段留空且已配置视为「保持不变」。
 */
export function validateConnectorForm(
  form: ConnectorFormState,
  providerSchema: ConnectorProviderSchema | null,
  options: { requireID: boolean; existing: ConnectorView | null },
): ConnectorFormError[] {
  const errors: ConnectorFormError[] = [];
  if (options.requireID) {
    const id = form.id.trim();
    if (!id) {
      errors.push({ key: "id", code: "required" });
    } else if (!ID_PATTERN.test(id)) {
      errors.push({ key: "id", code: "invalid_id" });
    }
  }
  if (!providerSchema) {
    errors.push({ key: "provider", code: "required" });
    return errors;
  }
  const existingSecrets = new Set(options.existing?.secret_keys ?? []);
  for (const field of providerSchema.fields) {
    if (!field.required) {
      continue;
    }
    const value = (form.values[field.key] ?? "").trim();
    if (field.type === "secret") {
      if (form.clearedSecrets[field.key]) {
        continue; // 清除是否可接受由后端最终裁决
      }
      if (value || existingSecrets.has(field.key)) {
        continue;
      }
      errors.push({ key: field.key, code: "required" });
      continue;
    }
    if (!value) {
      errors.push({ key: field.key, code: "required" });
    }
  }
  return errors;
}

/**
 * 构造提交体：
 *   * 非密钥字段全部提交（空串 = 清空，与后端 MergeSubmit 语义一致）；
 *   * 密钥字段：勾选清除 → `__clear__`；有输入 → 提交；留空 → 不提交（保持原值）；
 *   * 新建提交 id / provider / enabled；编辑不提交这两项（provider 不可变更）。
 */
export function buildSubmitPayload(
  form: ConnectorFormState,
  providerSchema: ConnectorProviderSchema,
  options: { isCreate: boolean },
): ConnectorSubmitPayload {
  const settings: Record<string, string> = {};
  const secrets: Record<string, string> = {};
  for (const field of providerSchema.fields) {
    const raw = form.values[field.key] ?? "";
    if (field.type === "secret") {
      if (form.clearedSecrets[field.key]) {
        secrets[field.key] = CONNECTOR_CLEAR_SECRET;
        continue;
      }
      const value = raw.trim();
      if (value) {
        secrets[field.key] = value;
      }
      continue;
    }
    settings[field.key] = raw;
  }

  const payload: ConnectorSubmitPayload = {
    settings,
    secrets,
    mode: form.mode,
  };
  if (options.isCreate) {
    payload.id = form.id.trim();
    payload.provider = providerSchema.provider;
    payload.name = form.name.trim() || form.id.trim();
    payload.enabled = true;
  } else {
    payload.name = form.name.trim();
  }
  return payload;
}
