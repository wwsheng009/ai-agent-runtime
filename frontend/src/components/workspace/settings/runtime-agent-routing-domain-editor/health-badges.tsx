import { TriangleAlertIcon } from "lucide-react";
import { type TFunction } from "i18next";

import { cn } from "@/lib/utils";
import {
  type RuntimeAgentRouteHealth,
  type RuntimeAgentRouteHealthIssue,
  type RuntimeAgentRouteProfile,
} from "../runtime-agent-routing-domain-utils";
import { routeHealthIssueMessage } from "./format";
import { HealthBadge } from "./primitives";

export function RouteHealthBadge({
  health,
  t,
}: {
  health: RuntimeAgentRouteHealth;
  t: TFunction<"runtimeConfig">;
}) {
  if (health.issues.some((issue) => issue.severity === "error")) {
    return (
      <HealthBadge tone="error">
        {t("editor.agentRouting.health.routeError")}
      </HealthBadge>
    );
  }
  if (health.issues.length > 0) {
    return (
      <HealthBadge tone="warning">
        {t("editor.agentRouting.health.routeWarning")}
      </HealthBadge>
    );
  }

  const labelKey =
    health.mode === "configured"
      ? "configured"
      : health.mode === "providerDefault"
        ? "providerDefault"
        : health.mode === "disabled"
          ? "routingDisabled"
          : "parentInherited";
  return (
    <HealthBadge
      tone={
        health.mode === "configured" || health.mode === "providerDefault"
          ? "ready"
          : "neutral"
      }
    >
      {t(`editor.agentRouting.health.${labelKey}`)}
    </HealthBadge>
  );
}

export function RouteHealthIssueText({
  health,
  issue,
  profile,
  t,
}: {
  health: RuntimeAgentRouteHealth;
  issue: RuntimeAgentRouteHealthIssue;
  profile: RuntimeAgentRouteProfile;
  t: TFunction<"runtimeConfig">;
}) {
  const message = routeHealthIssueMessage(t, issue, health, profile);
  return (
    <div
      className={cn(
        "flex items-start gap-1.5 text-xs leading-5",
        issue.severity === "error"
          ? "text-[#f5c7b8]"
          : "text-[var(--muted-foreground)]",
      )}
    >
      <TriangleAlertIcon size={13} className="mt-1 shrink-0" />
      <span>{message}</span>
    </div>
  );
}
