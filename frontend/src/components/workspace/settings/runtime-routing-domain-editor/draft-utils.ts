// 由 components/workspace/settings/runtime-routing-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  createDefaultRuntimeRoute,
  type RuntimeRouteSummary,
} from "../runtime-routing-domain-utils";
import { type RouteDraftInput } from "../runtime-routing-domain-form-utils";

const KNOWN_ROUTE_KEYS = new Set([
  "match_path",
  "match_type",
  "group",
  "pipeline",
  "protocol",
  "priority",
  "match_models",
  "match_model_regexes",
  "exclude_models",
  "exclude_model_regexes",
]);

export function createRouteDraftInput(route: RuntimeRouteSummary | null): RouteDraftInput {
  if (!route) {
    const defaults = createDefaultRuntimeRoute();
    return {
      matchPath:
        typeof defaults.match_path === "string" ? defaults.match_path : "/v1/chat",
      matchType:
        typeof defaults.match_type === "string" ? defaults.match_type : "prefix",
      group: typeof defaults.group === "string" ? defaults.group : "",
      pipeline: "",
      protocol: "",
      priority: "",
      matchModelsText: "",
      matchModelRegexesText: "",
      excludeModelsText: "",
      excludeModelRegexesText: "",
      extraJson: "{}",
    };
  }

  const extraFields = Object.fromEntries(
    Object.entries(route.raw).filter(([key]) => !KNOWN_ROUTE_KEYS.has(key)),
  );

  return {
    matchPath: route.matchPath,
    matchType: route.matchType || "prefix",
    group: route.group,
    pipeline: route.pipeline,
    protocol: route.protocol,
    priority: route.priority,
    matchModelsText: route.matchModels.join("\n"),
    matchModelRegexesText: route.matchModelRegexes.join("\n"),
    excludeModelsText: route.excludeModels.join("\n"),
    excludeModelRegexesText: route.excludeModelRegexes.join("\n"),
    extraJson: JSON.stringify(extraFields, null, 2),
  };
}
