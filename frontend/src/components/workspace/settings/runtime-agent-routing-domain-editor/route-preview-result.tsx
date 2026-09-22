import { TriangleAlertIcon } from "lucide-react";
import { type TFunction } from "i18next";

import { type RuntimeAgentRoutePreviewResult } from "@/types/runtime";

import { previewTranslation, taskTypeLabel } from "./format";
import { HealthBadge, PreviewValue } from "./primitives";

export function RoutePreviewResult({
  result,
  t,
}: {
  result: RuntimeAgentRoutePreviewResult;
  t: TFunction<"runtimeConfig">;
}) {
  const decision = result.decision;
  const warnings = decision.warnings ?? [];
  return (
    <div className="mt-3 border-t border-border pt-3">
      <div className="flex flex-wrap items-center gap-1.5">
        <HealthBadge tone={result.routing_enabled ? "ready" : "neutral"}>
          {result.routing_enabled
            ? t("editor.agentRouting.preview.routingEnabled")
            : t("editor.agentRouting.preview.routingDisabled")}
        </HealthBadge>
        <HealthBadge tone="neutral">
          {previewTranslation(
            t,
            "routingSources",
            result.routing_source,
          )}
        </HealthBadge>
        {decision.source ? (
          <HealthBadge tone="neutral">
            {previewTranslation(t, "sources", decision.source)}
          </HealthBadge>
        ) : null}
        {decision.difficulty_source ? (
          <HealthBadge tone="neutral">
            {previewTranslation(
              t,
              "difficultySources",
              decision.difficulty_source,
            )}
          </HealthBadge>
        ) : null}
        {decision.fallback_used ? (
          <HealthBadge tone="warning">
            {t("editor.agentRouting.preview.fallback")}
          </HealthBadge>
        ) : null}
      </div>

      <div className="mt-3 grid gap-x-5 gap-y-3 sm:grid-cols-2 xl:grid-cols-4">
        <PreviewValue
          label={t("editor.agentRouting.preview.provider")}
          value={decision.provider}
        />
        <PreviewValue
          label={t("editor.agentRouting.preview.model")}
          value={decision.model}
        />
        <PreviewValue
          label={t("editor.agentRouting.preview.reasoning")}
          value={decision.reasoning_effort}
        />
        <PreviewValue
          label={t("editor.agentRouting.preview.difficulty")}
          value={
            decision.difficulty
              ? previewTranslation(t, "difficulties", decision.difficulty)
              : ""
          }
        />
        <PreviewValue
          label={t("editor.agentRouting.preview.taskType")}
          value={
            decision.task_type ? taskTypeLabel(t, decision.task_type) : ""
          }
        />
      </div>

      {decision.task_subject ? (
        <div className="mt-2 break-words text-xs leading-5 text-muted-foreground">
          {t("editor.agentRouting.preview.taskSubject")}: {decision.task_subject}
        </div>
      ) : null}

      <div className="mt-3 text-xs leading-5 text-muted-foreground">
        {t("editor.agentRouting.preview.parent")}: {result.parent.provider || "-"}
        {" / "}
        {result.parent.model || "-"}
        {result.parent.reasoning_effort
          ? ` / ${result.parent.reasoning_effort}`
          : ""}
      </div>

      {warnings.length > 0 ? (
        <div className="mt-3 space-y-1 border-t border-border pt-2.5">
          {warnings.map((warning) => (
            <div
              key={warning}
              className="flex items-start gap-1.5 text-xs leading-5 text-muted-foreground"
            >
              <TriangleAlertIcon size={13} className="mt-1 shrink-0" />
              <span>{previewTranslation(t, "warnings", warning)}</span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}
