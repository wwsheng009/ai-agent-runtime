import { type TFunction } from "i18next";

import { Select } from "@/components/ui/select";

import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type RuntimeAgentRoutingConfigSummary } from "../runtime-agent-routing-domain-utils";

import { reasoningPolicyValues } from "./format";

export function RoutingLimitsSection({
  config,
  inherited,
  t,
  updateConfig,
}: {
  config: RuntimeAgentRoutingConfigSummary;
  inherited: boolean;
  t: TFunction<"runtimeConfig">;
  updateConfig: (
    update: (next: RuntimeAgentRoutingConfigSummary) => void,
  ) => void;
}) {
  return (
    <>
      <div className="grid gap-3 md:grid-cols-2">
        <ConfigFormField
          label={t("editor.agentRouting.maxExpertConcurrency.label")}
          description={t("editor.agentRouting.maxExpertConcurrency.description")}
        >
          <input
            className={editorControlClassName}
            disabled={inherited}
            min={0}
            type="number"
            value={config.maxExpertConcurrency}
            onChange={(event) =>
              updateConfig((next) => {
                next.maxExpertConcurrency = event.target.value;
              })
            }
          />
        </ConfigFormField>
        <ConfigFormField
          label={t("editor.agentRouting.reasoningPolicy.label")}
          description={t("editor.agentRouting.reasoningPolicy.description")}
        >
          <Select
            ariaLabel={t("editor.agentRouting.reasoningPolicy.label")}
            disabled={inherited}
            options={reasoningPolicyValues.map((value) => ({
              value,
              label: t(`editor.agentRouting.reasoningPolicies.${value}`),
            }))}
            value={config.unsupportedReasoningPolicy}
            onChange={(value) =>
              updateConfig((next) => {
                next.unsupportedReasoningPolicy = value;
              })
            }
          />
        </ConfigFormField>
      </div>
    </>
  );
}
