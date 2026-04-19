import { isConfigRecord } from "./runtime-provider-config-utils";

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

export type RuntimeRoutingConfigSummary = {
  failover: boolean;
  strategy: string;
};

export type RuntimeRouteSummary = {
  excludeModelRegexes: string[];
  excludeModels: string[];
  extraFieldCount: number;
  group: string;
  id: string;
  index: number;
  matchModelRegexes: string[];
  matchModels: string[];
  matchPath: string;
  matchType: string;
  pipeline: string;
  priority: string;
  protocol: string;
  raw: Record<string, unknown>;
};

export function getRuntimeRoutingConfig(value: unknown): RuntimeRoutingConfigSummary {
  const routingRoot =
    isConfigRecord(value) && isConfigRecord(value.routing) ? value.routing : {};

  return {
    strategy: readText(routingRoot.strategy),
    failover: Boolean(routingRoot.failover),
  };
}

export function listRuntimeRouteSummaries(value: unknown): RuntimeRouteSummary[] {
  const routingRoot =
    isConfigRecord(value) && isConfigRecord(value.routing) ? value.routing : {};
  const routes = Array.isArray(routingRoot.routes) ? routingRoot.routes : [];

  return routes.flatMap((route, index) =>
    isConfigRecord(route) ? [buildRouteSummary(route, index)] : [],
  );
}

export function createDefaultRuntimeRoute() {
  return {
    match_path: "/v1/chat",
    match_type: "prefix",
    group: "",
  };
}

function buildRouteSummary(
  raw: Record<string, unknown>,
  index: number,
): RuntimeRouteSummary {
  const extraFieldCount = Object.keys(raw).filter((key) => !KNOWN_ROUTE_KEYS.has(key))
    .length;

  const matchPath = readText(raw.match_path);
  const matchType = readText(raw.match_type);
  const group = readText(raw.group);

  return {
    id: `${index}:${matchPath}:${group}:${matchType}`,
    index,
    raw,
    matchPath,
    matchType,
    group,
    pipeline: readText(raw.pipeline),
    protocol: readText(raw.protocol),
    priority: readText(raw.priority),
    matchModels: readStringArray(raw.match_models),
    matchModelRegexes: readStringArray(raw.match_model_regexes),
    excludeModels: readStringArray(raw.exclude_models),
    excludeModelRegexes: readStringArray(raw.exclude_model_regexes),
    extraFieldCount,
  };
}

function readText(value: unknown) {
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number") {
    return String(value);
  }
  return "";
}

function readStringArray(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => (typeof item === "string" ? item.trim() : ""))
    .filter(Boolean);
}
