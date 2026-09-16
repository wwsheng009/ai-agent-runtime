// composer 模型面板的纯展示模型（从 components/workspace/composer-model-panel.tsx 抽出，P0-2 行数约束）。
//
// 只做「候选 + 当前选中 + 文案 → 段落 / 视图 / 待确认」的换算，无副作用：
//   * 只渲染宿主投影的候选，不拉取 / 不缓存 / 不推断默认选中；
//   * 某段候选为空 → 整段隐藏（三段都空时连触发器都不渲染，由组件判断 sections.length === 0）；
//   * 换供应商后的「待重选」只由这里的状态机推进：用户不显式确认，就不把这组选择当作已定。

export type ComposerModelPanelSectionId = "provider" | "model" | "reasoning";

export type ComposerModelPanelOption = {
  value: string;
  label: string;
  selected: boolean;
  onSelect: () => void;
};

export type ComposerModelPanelSection = {
  id: ComposerModelPanelSectionId;
  label: string;
  /** 一级行显示的当前生效值。 */
  value: string;
  options: ComposerModelPanelOption[];
};

/** 二级菜单视图：一级是摘要行，二级是某一项的候选列表。 */
export type ComposerModelPanelView =
  | { level: "root" }
  | { level: "section"; section: ComposerModelPanelSectionId };

/** 换供应商后待用户显式重选的两项。 */
export type ComposerModelPanelPending = {
  model: boolean;
  reasoning: boolean;
};

/** 面板最小宽度：候选列表同屏可读，窄视口由定位函数夹到视口内。 */
export const PANEL_MIN_WIDTH = 320;
export const PANEL_MAX_HEIGHT = 380;
export const PANEL_MIN_HEIGHT = 160;

export type ComposerModelPanelSectionsInput = {
  providerOptions: readonly string[];
  modelOptions: readonly string[];
  reasoningEffortOptions: readonly string[];
  selectedProvider: string;
  selectedModel: string;
  selectedReasoningEffort: string;
  /** 段标题（i18n 由调用方提供）。 */
  labels: { provider: string; model: string; reasoning: string };
  /** 未选中时的占位文案（i18n 由调用方提供）。 */
  unselectedLabel: string;
  /** 推理等级当前生效值的展示文案（默认档位经 i18n 包装）。 */
  reasoningValue: string;
  /** 默认推理档位的展示文案。 */
  defaultEffortLabel: string;
  onProviderSelect: (provider: string) => void;
  onModelSelect: (model: string) => void;
  onReasoningSelect: (effort: string) => void;
};

/** 段顺序固定为 provider → model → reasoning：与旧工具条从左到右的阅读顺序一致。 */
export function buildComposerModelSections(
  input: ComposerModelPanelSectionsInput,
): ComposerModelPanelSection[] {
  const {
    defaultEffortLabel,
    labels,
    modelOptions,
    onModelSelect,
    onProviderSelect,
    onReasoningSelect,
    providerOptions,
    reasoningEffortOptions,
    reasoningValue,
    selectedModel,
    selectedProvider,
    selectedReasoningEffort,
    unselectedLabel,
  } = input;
  const sections: ComposerModelPanelSection[] = [];

  if (providerOptions.length > 1) {
    sections.push({
      id: "provider",
      label: labels.provider,
      value: selectedProvider || unselectedLabel,
      options: providerOptions.map((provider) => ({
        value: provider,
        label: provider,
        selected: provider === selectedProvider,
        onSelect: () => {
          onProviderSelect(provider);
        },
      })),
    });
  }
  if (modelOptions.length > 0) {
    sections.push({
      id: "model",
      label: labels.model,
      value: selectedModel || unselectedLabel,
      options: modelOptions.map((model) => ({
        value: model,
        label: model,
        selected: model === selectedModel,
        onSelect: () => {
          onModelSelect(model);
        },
      })),
    });
  }
  if (reasoningEffortOptions.length > 0) {
    sections.push({
      id: "reasoning",
      label: labels.reasoning,
      value: reasoningValue,
      options: [
        {
          value: "",
          label: defaultEffortLabel,
          selected: selectedReasoningEffort === "",
          onSelect: () => {
            onReasoningSelect("");
          },
        },
        ...reasoningEffortOptions.map((effort) => ({
          value: effort,
          label: effort,
          selected: effort === selectedReasoningEffort,
          onSelect: () => {
            onReasoningSelect(effort);
          },
        })),
      ],
    });
  }

  return sections;
}

/** 选择推进结果：`view` 是推进后应停留的层级，`pending` 是需要写回的「待确认」状态。 */
export type ComposerModelPanelAdvance = {
  pending: ComposerModelPanelPending;
  view: ComposerModelPanelView;
};

/** 换供应商的推进结果：`pending` 为 null = 保持原状（原地重选同一供应商，不新增也不清空）。 */
export type ComposerModelPanelProviderAdvance = {
  pending: ComposerModelPanelPending | null;
  view: ComposerModelPanelView;
};

/**
 * 换供应商：宿主会重解析模型（仍被支持的才保留，否则回落）并钳制推理档位。
 * 面板不猜宿主结果，只把「重选」这一步显式化并顺序引导：模型 → 推理。
 */
export function advanceAfterProviderSelect(input: {
  providerChanged: boolean;
  hasModel: boolean;
  hasReasoning: boolean;
}): ComposerModelPanelProviderAdvance {
  const { hasModel, hasReasoning, providerChanged } = input;
  if (!providerChanged) {
    return { pending: null, view: { level: "root" } };
  }

  const pending: ComposerModelPanelPending = { model: hasModel, reasoning: hasReasoning };
  if (hasModel) {
    return { pending, view: { level: "section", section: "model" } };
  }
  if (hasReasoning) {
    return { pending, view: { level: "section", section: "reasoning" } };
  }
  return { pending, view: { level: "root" } };
}

/** 选模型：null = 不是「换供应商后的重选」，留在候选列表里继续比较。 */
export function advanceAfterModelSelect(input: {
  pendingModel: boolean;
  hasReasoning: boolean;
}): ComposerModelPanelAdvance | null {
  if (!input.pendingModel) {
    return null;
  }
  const pending: ComposerModelPanelPending = {
    model: false,
    reasoning: input.hasReasoning,
  };
  if (input.hasReasoning) {
    return { pending, view: { level: "section", section: "reasoning" } };
  }
  return { pending, view: { level: "root" } };
}

/** 调推理档位：null = 普通调档，留在候选列表里继续比较。 */
export function advanceAfterReasoningSelect(
  pendingReasoning: boolean,
): ComposerModelPanelAdvance | null {
  if (!pendingReasoning) {
    return null;
  }
  return {
    pending: { model: false, reasoning: false },
    view: { level: "root" },
  };
}

/** 面板内上下键的环形步进：没有命中项时从首（↓）/ 末（↑）进入。 */
export function nextOptionIndex(
  currentIndex: number,
  length: number,
  step: 1 | -1,
): number {
  if (length <= 0) {
    return -1;
  }
  if (currentIndex < 0) {
    return step === 1 ? 0 : length - 1;
  }
  return (currentIndex + step + length) % length;
}
