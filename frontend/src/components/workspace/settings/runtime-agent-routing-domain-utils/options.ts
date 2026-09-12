// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeProviderSummary } from "../runtime-provider-config-utils";

import {
  findConfiguredModelCapability,
  readCapabilityReasoningEfforts,
} from "./capabilities";

export function providerModelOptions(
  providers: RuntimeProviderSummary[],
  providerName: string,
  currentModel: string,
) {
  const provider = providers.find((item) => item.name === providerName);
  const models = new Set<string>();
  if (currentModel.trim()) models.add(currentModel.trim());
  if (provider?.defaultModel.trim()) models.add(provider.defaultModel.trim());
  for (const model of provider?.supportedModels ?? []) {
    if (model.trim()) models.add(model.trim());
  }
  return [...models].map((model) => ({ label: model, value: model }));
}

export function providerReasoningEffortOptions(
  providers: RuntimeProviderSummary[],
  providerName: string,
  modelName: string,
  currentEffort: string,
) {
  const provider = providers.find((item) => item.name === providerName.trim());
  const model = modelName.trim() || provider?.defaultModel.trim() || "";
  const capability = provider && model
    ? findConfiguredModelCapability(provider, model)
    : null;
  const declared = readCapabilityReasoningEfforts(capability);
  const values = new Set<string>();

  if (declared.length > 0) {
    for (const effort of declared) values.add(effort);
  } else if (!capability || capability.reasoning_model === true) {
    for (const effort of ["none", "minimal", "low", "medium", "high", "xhigh", "max"]) {
      values.add(effort);
    }
  }
  if (currentEffort.trim()) values.add(currentEffort.trim());
  return [...values].map((value) => ({ label: value, value }));
}
