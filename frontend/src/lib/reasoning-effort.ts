import type {
  RuntimeModelCapabilitySpec,
  RuntimeModelProviderRecord,
} from "@/types/runtime";

/**
 * 会话级 reasoning effort 选择的空值语义：跟随后端 config.yaml
 * （aicli.chat.reasoning_effort）配置的默认档位，不显式下发字段。
 */
export const DEFAULT_REASONING_EFFORT_VALUE = "";

/** 会话上下文中的存储键（与后端 sessionmeta.ReasoningEffort 保持一致）。 */
export const SESSION_REASONING_EFFORT_CONTEXT_KEY = "reasoning_effort";

/** 模型能力未声明档位时的兜底档位（与设置页/适配器约定一致）。 */
export const FALLBACK_REASONING_EFFORT_OPTIONS = [
  "minimal",
  "low",
  "medium",
  "high",
];

export function normalizeReasoningEffort(value: unknown): string {
  if (typeof value !== "string") {
    return "";
  }
  return value.trim();
}

/**
 * 从会话 context 中读取会话级 reasoning effort；未设置或非法值返回空字符串
 * （空字符串代表"跟随配置默认"）。
 */
export function readSessionReasoningEffort(context: unknown): string {
  if (!context || typeof context !== "object") {
    return "";
  }

  const record = context as Record<string, unknown>;
  return normalizeReasoningEffort(record[SESSION_REASONING_EFFORT_CONTEXT_KEY]);
}

/**
 * 解析 provider 下指定模型的模型能力；优先精确匹配，其次回退到 `*` 通配能力。
 */
export function findModelCapability(
  provider: RuntimeModelProviderRecord | null | undefined,
  model: string,
): RuntimeModelCapabilitySpec | null {
  const capabilities = provider?.model_capabilities;
  if (!capabilities || typeof capabilities !== "object") {
    return null;
  }

  const normalizedModel = normalizeReasoningEffort(model);
  if (normalizedModel) {
    const exact = capabilities[normalizedModel];
    if (exact && typeof exact === "object") {
      return exact;
    }
  }

  const wildcard = capabilities["*"];
  if (wildcard && typeof wildcard === "object") {
    return wildcard;
  }
  return null;
}

function readCapabilityEfforts(
  capability: RuntimeModelCapabilitySpec | null,
): string[] {
  if (!capability || !Array.isArray(capability.reasoning_efforts)) {
    return [];
  }

  const seen = new Set<string>();
  const efforts: string[] = [];
  for (const item of capability.reasoning_efforts) {
    const effort = normalizeReasoningEffort(item);
    const key = effort.toLowerCase();
    if (!effort || seen.has(key)) {
      continue;
    }
    seen.add(key);
    efforts.push(effort);
  }
  return efforts;
}

/**
 * 按模型能力解析可选档位：
 * - 能力显式声明 `reasoning_efforts` → 只保留声明的档位（动态过滤）；
 * - 能力标记 `reasoning_model: true` 但未列档位 → 使用通用档位；
 * - 能力显式标记不支持推理 → 无选项（不展示选择器）；
 * - provider 未提供模型能力信息 → 沿用通用档位（保持既有可选能力）。
 */
export function resolveModelReasoningEffortOptions(
  provider: RuntimeModelProviderRecord | null | undefined,
  model: string,
): string[] {
  const capability = findModelCapability(provider, model);
  const declared = readCapabilityEfforts(capability);
  if (declared.length > 0) {
    return declared;
  }

  if (capability) {
    return capability.reasoning_model === true
      ? [...FALLBACK_REASONING_EFFORT_OPTIONS]
      : [];
  }

  return [...FALLBACK_REASONING_EFFORT_OPTIONS];
}

/** 模型能力中声明的默认档位（如 `default_reasoning_effort`）。 */
export function resolveModelDefaultReasoningEffort(
  provider: RuntimeModelProviderRecord | null | undefined,
  model: string,
): string {
  const capability = findModelCapability(provider, model);
  return normalizeReasoningEffort(capability?.default_reasoning_effort);
}

/**
 * 计算会话选择在当前模型下的有效值：档位不被当前模型支持时回落到默认。
 */
export function resolveEffectiveReasoningEffort(
  selected: string,
  options: readonly string[],
): string {
  const normalized = normalizeReasoningEffort(selected);
  if (!normalized) {
    return DEFAULT_REASONING_EFFORT_VALUE;
  }
  if (options.length === 0) {
    return DEFAULT_REASONING_EFFORT_VALUE;
  }

  const supported = options.some(
    (option) => option.toLowerCase() === normalized.toLowerCase(),
  );
  return supported ? normalized : DEFAULT_REASONING_EFFORT_VALUE;
}

/**
 * 解析"默认"档位用于展示：优先模型能力默认值，其次 config.yaml 默认值。
 */
export function resolveDefaultReasoningEffort(
  provider: RuntimeModelProviderRecord | null | undefined,
  model: string,
  configDefault: string | undefined,
): string {
  return (
    resolveModelDefaultReasoningEffort(provider, model) ||
    normalizeReasoningEffort(configDefault)
  );
}

export function isSupportedReasoningEffort(
  effort: string,
  options: readonly string[],
): boolean {
  const normalized = normalizeReasoningEffort(effort);
  if (!normalized) {
    return false;
  }
  return options.some(
    (option) => option.toLowerCase() === normalized.toLowerCase(),
  );
}
