/**
 * Provider 单模型能力草稿（`model_capabilities.<model>` 的编辑态）。
 *
 * 参照 aicli micro web client 的模型编辑器：模型列表的每一项都可以打开
 * 详情面板做深度配置，草稿与 config.yaml 的 `ModelCapabilitySpec` 字段一一对应。
 *
 * 设计口径：
 *   * 数值输入在草稿里保持字符串：空串 = 未声明；序列化时只写正数；
 *   * 布尔字段只有 true 会被写入配置（false 与空数组一样表示「移除该字段」）；
 *   * `replay_reasoning_content` 是三态：null = 未声明（回退内置启发式）；
 *   * 未识别的能力字段保留在 `extraFields`，保存时合并回去，避免丢手写字段；
 *   * 与 fetch-models / auto-import 的合并统一为「非空覆盖」：只写响应真正
 *     提供的（非空）字段，空值不抹平已有声明。
 */

import type {
  ProviderModelCapabilitySpec,
  ProviderModelMetadata,
} from "@/types/runtime";

import { isConfigRecord } from "../runtime-provider-config-utils";

/** 单模型能力草稿：所有字段都允许为空（未声明）。 */
export type ProviderModelDraft = {
  extraFields: Record<string, unknown>;
  reasoningModel: boolean;
  /** 三态：null=未声明（回退启发式）；true=强制回传；false=禁止注入。 */
  replayReasoningContent: boolean | null;
  inputModalities: string[];
  reasoningEffortsText: string;
  reasoningEffortBudgetsText: string;
  defaultReasoningEffort: string;
  compactReasoningEffort: string;
  maxContextTokensText: string;
  maxTokensText: string;
  autoCompactRatioText: string;
  autoCompactTokenLimitText: string;
  autoCompactMode: string;
  supportsRemoteCompact: boolean;
  imageGeneration: boolean;
  imagesGenerationsApi: boolean;
};

/** 已建模的能力字段集合；其余 key 视为扩展字段原样保留。 */
const KNOWN_CAPABILITY_KEYS = new Set([
  "input_modalities",
  "native_tools",
  "reasoning_model",
  "replay_reasoning_content",
  "reasoning_efforts",
  "reasoning_effort_budgets",
  "default_reasoning_effort",
  "max_context_tokens",
  "max_tokens",
  "auto_compact_ratio",
  "auto_compact_token_limit",
  "auto_compact_mode",
  "supports_remote_compact",
  "compact_reasoning_effort",
]);

/** fetch-models metadata 的展示字段：不是能力声明，不进入 extraFields。 */
const NON_CAPABILITY_KEYS = new Set(["id", "name"]);

export function emptyProviderModelDraft(): ProviderModelDraft {
  return {
    extraFields: {},
    reasoningModel: false,
    replayReasoningContent: null,
    inputModalities: [],
    reasoningEffortsText: "",
    reasoningEffortBudgetsText: "",
    defaultReasoningEffort: "",
    compactReasoningEffort: "",
    maxContextTokensText: "",
    maxTokensText: "",
    autoCompactRatioText: "",
    autoCompactTokenLimitText: "",
    autoCompactMode: "",
    supportsRemoteCompact: false,
    imageGeneration: false,
    imagesGenerationsApi: false,
  };
}

/** 去空 + 去重（保序）的模型 ID 列表。 */
export function normalizeProviderModelIDs(
  models: unknown,
): string[] {
  const result: string[] = [];
  if (!Array.isArray(models)) {
    return result;
  }
  for (const raw of models) {
    const id = String(raw ?? "").trim();
    if (id && !result.includes(id)) {
      result.push(id);
    }
  }
  return result;
}

/** 手动添加模型时的输入切分：空白 / 半角逗号 / 全角逗号都算分隔符。 */
export function splitProviderModelIDInput(text: string): string[] {
  return normalizeProviderModelIDs(text.split(/[\s,，]+/));
}

/** 支持模型文本（换行 / 逗号 / 空白混合）→ 模型 ID 列表。 */
export function parseProviderModelIDsText(text: string): string[] {
  return normalizeProviderModelIDs(text.split(/[\s,，]+/));
}

/** 列表 → 文本域内容（每行一个）。 */
export function formatProviderModelIDsText(models: string[]): string {
  return normalizeProviderModelIDs(models).join("\n");
}

/** reasoning 档位列表文本（逗号 / 空白分隔）→ 去重保序数组。 */
export function parseReasoningEffortsText(text: string): string[] {
  return normalizeProviderModelIDs(text.split(/[,，\s]+/));
}

/** 档位预算文本（每行 `effort: budget`）→ 正数 map；非法行忽略。 */
export function parseReasoningEffortBudgetsText(text: string): Record<string, number> {
  const budgets: Record<string, number> = {};
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line) {
      continue;
    }
    const index = line.lastIndexOf(":");
    if (index <= 0) {
      continue;
    }
    const effort = line.slice(0, index).trim();
    const value = Number.parseInt(line.slice(index + 1).trim(), 10);
    if (effort && Number.isFinite(value) && value > 0) {
      budgets[effort] = value;
    }
  }
  return budgets;
}

/** 档位预算 map → 文本（按 key 排序，保证稳定 diff）。 */
export function formatReasoningEffortBudgetsText(
  budgets: Record<string, number> | null | undefined,
): string {
  return Object.keys(budgets ?? {})
    .sort()
    .flatMap((effort) => {
      const value = Number(budgets?.[effort]);
      return Number.isFinite(value) ? [`${effort}: ${value}`] : [];
    })
    .join("\n");
}

/** 正整数字符串：非正数 / 非有限值 → 空串（未声明）。 */
function positiveIntText(value: unknown): string {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) {
    return "";
  }
  return String(Math.floor(number));
}

/** 正浮点字符串：非正数 / 非有限值 → 空串（未声明）。 */
function positiveNumberText(value: unknown): string {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) {
    return "";
  }
  return String(number);
}

function readText(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

/**
 * 能力字段对象（config 里的 spec / fetch-models metadata / auto-import 返回的
 * `model_capabilities.<model>`）→ 草稿。未识别字段收进 `extraFields`。
 */
export function providerModelDraftFromCapabilityFields(
  fields: Record<string, unknown> | null | undefined,
): ProviderModelDraft {
  const draft = emptyProviderModelDraft();
  if (!isConfigRecord(fields)) {
    return draft;
  }

  draft.reasoningModel = fields.reasoning_model === true;
  const replay = fields.replay_reasoning_content;
  draft.replayReasoningContent =
    replay === true || replay === false ? replay : null;
  draft.inputModalities = normalizeProviderModelIDs(fields.input_modalities);
  draft.reasoningEffortsText = normalizeProviderModelIDs(
    fields.reasoning_efforts,
  ).join(", ");
  draft.reasoningEffortBudgetsText = formatReasoningEffortBudgetsText(
    isConfigRecord(fields.reasoning_effort_budgets)
      ? (fields.reasoning_effort_budgets as Record<string, number>)
      : null,
  );
  draft.defaultReasoningEffort = readText(fields.default_reasoning_effort);
  draft.compactReasoningEffort = readText(fields.compact_reasoning_effort);
  draft.maxContextTokensText = positiveIntText(fields.max_context_tokens);
  draft.maxTokensText = positiveIntText(fields.max_tokens);
  draft.autoCompactRatioText = positiveNumberText(fields.auto_compact_ratio);
  draft.autoCompactTokenLimitText = positiveIntText(
    fields.auto_compact_token_limit,
  );
  draft.autoCompactMode = readText(fields.auto_compact_mode);
  draft.supportsRemoteCompact = fields.supports_remote_compact === true;

  const nativeTools = isConfigRecord(fields.native_tools)
    ? fields.native_tools
    : {};
  draft.imageGeneration = nativeTools.image_generation === true;
  draft.imagesGenerationsApi = nativeTools.images_generations_api === true;

  for (const [key, value] of Object.entries(fields)) {
    if (!KNOWN_CAPABILITY_KEYS.has(key) && !NON_CAPABILITY_KEYS.has(key)) {
      draft.extraFields[key] = value;
    }
  }
  return draft;
}

/** `providers.items.<name>.model_capabilities` 原始值 → 模型 ID → 草稿。 */
export function providerModelDraftsFromRecord(
  value: unknown,
): Record<string, ProviderModelDraft> {
  if (!isConfigRecord(value)) {
    return {};
  }
  const drafts: Record<string, ProviderModelDraft> = {};
  for (const [rawModel, fields] of Object.entries(value)) {
    const model = rawModel.trim();
    if (model && isConfigRecord(fields)) {
      drafts[model] = providerModelDraftFromCapabilityFields(fields);
    }
  }
  return drafts;
}

function isBlankDraftValue(value: string | string[]): boolean {
  return Array.isArray(value) ? value.length === 0 : value.trim() === "";
}

/**
 * 「非空覆盖」合并单个模型草稿：incoming 只覆盖自己有值的字段。
 * 布尔字段只有 true 视为有值（响应里的 false 视为未提供）；三态字段 null 不覆盖。
 */
export function mergeProviderModelDraft(
  base: ProviderModelDraft,
  incoming: ProviderModelDraft,
): ProviderModelDraft {
  return {
    extraFields:
      Object.keys(incoming.extraFields).length > 0
        ? { ...base.extraFields, ...incoming.extraFields }
        : base.extraFields,
    reasoningModel: base.reasoningModel || incoming.reasoningModel,
    replayReasoningContent:
      incoming.replayReasoningContent !== null
        ? incoming.replayReasoningContent
        : base.replayReasoningContent,
    inputModalities:
      incoming.inputModalities.length > 0
        ? [...incoming.inputModalities]
        : base.inputModalities,
    reasoningEffortsText: !isBlankDraftValue(incoming.reasoningEffortsText)
      ? incoming.reasoningEffortsText
      : base.reasoningEffortsText,
    reasoningEffortBudgetsText: !isBlankDraftValue(
      incoming.reasoningEffortBudgetsText,
    )
      ? incoming.reasoningEffortBudgetsText
      : base.reasoningEffortBudgetsText,
    defaultReasoningEffort: !isBlankDraftValue(incoming.defaultReasoningEffort)
      ? incoming.defaultReasoningEffort
      : base.defaultReasoningEffort,
    compactReasoningEffort: !isBlankDraftValue(incoming.compactReasoningEffort)
      ? incoming.compactReasoningEffort
      : base.compactReasoningEffort,
    maxContextTokensText: !isBlankDraftValue(incoming.maxContextTokensText)
      ? incoming.maxContextTokensText
      : base.maxContextTokensText,
    maxTokensText: !isBlankDraftValue(incoming.maxTokensText)
      ? incoming.maxTokensText
      : base.maxTokensText,
    autoCompactRatioText: !isBlankDraftValue(incoming.autoCompactRatioText)
      ? incoming.autoCompactRatioText
      : base.autoCompactRatioText,
    autoCompactTokenLimitText: !isBlankDraftValue(
      incoming.autoCompactTokenLimitText,
    )
      ? incoming.autoCompactTokenLimitText
      : base.autoCompactTokenLimitText,
    autoCompactMode: !isBlankDraftValue(incoming.autoCompactMode)
      ? incoming.autoCompactMode
      : base.autoCompactMode,
    supportsRemoteCompact:
      base.supportsRemoteCompact || incoming.supportsRemoteCompact,
    imageGeneration: base.imageGeneration || incoming.imageGeneration,
    imagesGenerationsApi:
      base.imagesGenerationsApi || incoming.imagesGenerationsApi,
  };
}

function draftsEqual(
  left: ProviderModelDraft,
  right: ProviderModelDraft,
): boolean {
  return JSON.stringify(left) === JSON.stringify(right);
}

/**
 * fetch-models 的 `metadata` → 合并进现有模型草稿（逐模型、逐字段非空覆盖）。
 * 无变化时返回 null，调用方跳过草稿写入。
 */
export function mergeProviderModelDraftsWithMetadata(
  current: Record<string, ProviderModelDraft>,
  metadata: Record<string, ProviderModelMetadata> | null | undefined,
): Record<string, ProviderModelDraft> | null {
  return mergeProviderModelDraftsWithEntries(
    current,
    Object.entries(metadata ?? {}).map(([model, fields]) => [
      model,
      fields as Record<string, unknown>,
    ]),
  );
}

/**
 * auto-import 的 `model_capabilities` → 合并进现有模型草稿。
 * 后端返回的是全量结构（零值字段代表未匹配），因此沿用「非空覆盖」。
 */
export function mergeProviderModelDraftsWithCapabilities(
  current: Record<string, ProviderModelDraft>,
  capabilities: Record<string, ProviderModelCapabilitySpec> | null | undefined,
): Record<string, ProviderModelDraft> | null {
  return mergeProviderModelDraftsWithEntries(
    current,
    Object.entries(capabilities ?? {}),
  );
}

function mergeProviderModelDraftsWithEntries(
  current: Record<string, ProviderModelDraft>,
  entries: Array<[string, Record<string, unknown>]>,
): Record<string, ProviderModelDraft> | null {
  let changed = false;
  const next: Record<string, ProviderModelDraft> = { ...current };
  for (const [rawModel, fields] of entries) {
    const model = rawModel.trim();
    if (!model || !isConfigRecord(fields)) {
      continue;
    }
    const base = next[model] ?? emptyProviderModelDraft();
    const merged = mergeProviderModelDraft(
      base,
      providerModelDraftFromCapabilityFields(fields),
    );
    if (!draftsEqual(base, merged)) {
      next[model] = merged;
      changed = true;
    }
  }
  return changed ? next : null;
}

/** 草稿是否为空：空草稿不写入配置，避免留下 `model_capabilities: {model: {}}` 噪音。 */
export function providerModelDraftIsEmpty(draft: ProviderModelDraft): boolean {
  if (Object.keys(draft.extraFields).length > 0) {
    return false;
  }
  if (
    draft.reasoningModel ||
    draft.supportsRemoteCompact ||
    draft.imageGeneration ||
    draft.imagesGenerationsApi
  ) {
    return false;
  }
  if (draft.replayReasoningContent !== null) {
    return false;
  }
  if (draft.inputModalities.length > 0) {
    return false;
  }
  const texts = [
    draft.reasoningEffortsText,
    draft.reasoningEffortBudgetsText,
    draft.defaultReasoningEffort,
    draft.compactReasoningEffort,
    draft.maxContextTokensText,
    draft.maxTokensText,
    draft.autoCompactRatioText,
    draft.autoCompactTokenLimitText,
    draft.autoCompactMode,
  ];
  return texts.every((text) => text.trim() === "");
}

/**
 * 草稿 → `model_capabilities.<model>` 记录；空草稿返回 null（不写节点）。
 * 只写有值的字段：清空一个字段 = 从配置移除该字段。
 */
export function providerModelDraftToSpec(
  draft: ProviderModelDraft,
): Record<string, unknown> | null {
  if (providerModelDraftIsEmpty(draft)) {
    return null;
  }
  const spec: Record<string, unknown> = { ...draft.extraFields };
  if (draft.reasoningModel) {
    spec.reasoning_model = true;
  }
  if (draft.replayReasoningContent !== null) {
    spec.replay_reasoning_content = draft.replayReasoningContent;
  }
  const modalities = normalizeProviderModelIDs(draft.inputModalities);
  if (modalities.length > 0) {
    spec.input_modalities = modalities;
  }
  const efforts = parseReasoningEffortsText(draft.reasoningEffortsText);
  if (efforts.length > 0) {
    spec.reasoning_efforts = efforts;
  }
  const budgets = parseReasoningEffortBudgetsText(
    draft.reasoningEffortBudgetsText,
  );
  if (Object.keys(budgets).length > 0) {
    spec.reasoning_effort_budgets = budgets;
  }
  if (draft.defaultReasoningEffort.trim()) {
    spec.default_reasoning_effort = draft.defaultReasoningEffort.trim();
  }
  if (draft.compactReasoningEffort.trim()) {
    spec.compact_reasoning_effort = draft.compactReasoningEffort.trim();
  }
  const maxContext = Number(draft.maxContextTokensText);
  if (Number.isFinite(maxContext) && maxContext > 0) {
    spec.max_context_tokens = Math.floor(maxContext);
  }
  const maxTokens = Number(draft.maxTokensText);
  if (Number.isFinite(maxTokens) && maxTokens > 0) {
    spec.max_tokens = Math.floor(maxTokens);
  }
  const compactRatio = Number(draft.autoCompactRatioText);
  if (Number.isFinite(compactRatio) && compactRatio > 0) {
    spec.auto_compact_ratio = compactRatio;
  }
  const compactLimit = Number(draft.autoCompactTokenLimitText);
  if (Number.isFinite(compactLimit) && compactLimit > 0) {
    spec.auto_compact_token_limit = Math.floor(compactLimit);
  }
  if (draft.autoCompactMode.trim()) {
    spec.auto_compact_mode = draft.autoCompactMode.trim();
  }
  if (draft.supportsRemoteCompact) {
    spec.supports_remote_compact = true;
  }
  const nativeTools: Record<string, unknown> = {};
  if (draft.imageGeneration) {
    nativeTools.image_generation = true;
  }
  if (draft.imagesGenerationsApi) {
    nativeTools.images_generations_api = true;
  }
  if (Object.keys(nativeTools).length > 0) {
    spec.native_tools = nativeTools;
  }
  return spec;
}

/** 全部模型草稿 → `model_capabilities` 记录（跳过空草稿）。 */
export function buildProviderModelCapabilitiesRecord(
  drafts: Record<string, ProviderModelDraft>,
): Record<string, unknown> {
  const record: Record<string, unknown> = {};
  for (const [rawModel, draft] of Object.entries(drafts)) {
    const model = rawModel.trim();
    if (!model) {
      continue;
    }
    const spec = providerModelDraftToSpec(draft);
    if (spec) {
      record[model] = spec;
    }
  }
  return record;
}

