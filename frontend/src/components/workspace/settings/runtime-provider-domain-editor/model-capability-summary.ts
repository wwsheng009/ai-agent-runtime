/**
 * 模型能力草稿的展示层工具：列表行摘要、token 展示格式化、输入模态词表。
 * 与 `model-capability-draft.ts` 的读写转换分开，便于独立测试与行数门禁。
 */

import {
  parseReasoningEffortsText,
  type ProviderModelDraft,
} from "./model-capability-draft";

/** 列表行摘要（组件据此渲染 chip 文案）。 */
export type ProviderModelDraftSummary = {
  autoCompactMode: string;
  autoCompactRatio: number;
  autoCompactTokenLimit: number;
  compactReasoningEffort: string;
  defaultReasoningEffort: string;
  imageGeneration: boolean;
  imagesGenerationsApi: boolean;
  inputModalities: string[];
  maxContextTokens: number;
  maxTokens: number;
  reasoningEffortCount: number;
  reasoningModel: boolean;
  replayReasoningContent: boolean | null;
  supportsRemoteCompact: boolean;
};

export function summarizeProviderModelDraft(
  draft: ProviderModelDraft,
): ProviderModelDraftSummary {
  return {
    reasoningModel: draft.reasoningModel,
    reasoningEffortCount: parseReasoningEffortsText(draft.reasoningEffortsText)
      .length,
    defaultReasoningEffort: draft.defaultReasoningEffort.trim(),
    compactReasoningEffort: draft.compactReasoningEffort.trim(),
    maxContextTokens: Number(draft.maxContextTokensText) || 0,
    maxTokens: Number(draft.maxTokensText) || 0,
    autoCompactRatio: Number(draft.autoCompactRatioText) || 0,
    autoCompactTokenLimit: Number(draft.autoCompactTokenLimitText) || 0,
    autoCompactMode: draft.autoCompactMode.trim(),
    supportsRemoteCompact: draft.supportsRemoteCompact,
    replayReasoningContent: draft.replayReasoningContent,
    inputModalities: [...draft.inputModalities],
    imageGeneration: draft.imageGeneration,
    imagesGenerationsApi: draft.imagesGenerationsApi,
  };
}

/** 128000 → "128K"，1048576 → "1M"，其余原样。纯展示，不影响提交值。 */
export function formatTokenCount(value: number): string {
  const number = Number(value) || 0;
  if (number >= 1_000_000) {
    return `${Math.round((number / 1_000_000) * 10) / 10}M`;
  }
  if (number >= 1_000) {
    return `${Math.round((number / 1_000) * 10) / 10}K`;
  }
  return String(number);
}

/** 输入模态词表；honored 标注该值是否真被运行时消费（纯展示提示）。 */
export const PROVIDER_MODALITY_VOCAB = [
  { code: "text", label: "text", honored: true },
  { code: "image", label: "image", honored: true },
  { code: "audio", label: "audio", honored: false },
  { code: "video", label: "video", honored: false },
  { code: "file", label: "file", honored: false },
] as const;

/** 切换模态勾选（保持词表顺序归一化，便于 diff 与断言）。 */
export function toggleProviderModality(
  modalities: string[],
  modality: string,
  checked: boolean,
): string[] {
  const next = new Set(modalities);
  if (checked) {
    next.add(modality);
  } else {
    next.delete(modality);
  }
  const order: string[] = PROVIDER_MODALITY_VOCAB.map((entry) => entry.code);
  return [...next].sort(
    (left, right) =>
      (order.indexOf(left) + 1 || order.length + 1) -
      (order.indexOf(right) + 1 || order.length + 1),
  );
}
