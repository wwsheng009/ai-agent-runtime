import { isConfigRecord } from "./runtime-provider-config-utils";
import { buildArrayItemConfigSnippet } from "./runtime-config-yaml-utils";

export type RuntimeProviderGroupMember = {
  enabled: boolean;
  name: string;
  role: string;
  weight: string;
};

export type RuntimeProviderGroupSummary = {
  failoverEnabled: boolean;
  failoverMode: string;
  failoverScope: string;
  maxRetries: string;
  name: string;
  providerCount: number;
  providers: RuntimeProviderGroupMember[];
  raw: Record<string, unknown>;
  retryDelay: string;
  strategy: string;
  truncationEnabled: boolean;
  truncationMaxRetries: string;
  truncationStep: string;
  truncationStrategy: string;
};

export type RuntimeAuthConfigSummary = {
  accessAuthAllowAnonymous: boolean;
  accessAuthEnabled: boolean;
  accessKeySecret: string;
  adminAuthEnabled: boolean;
  adminToken: string;
  jwtExpire: string;
  jwtSecret: string;
  maxApiCreateTimes: string;
  sessionTimeout: string;
};

export function createDefaultProviderGroup(name: string) {
  return {
    name,
    strategy: "round_robin",
    max_retries: 3,
    retry_delay: "1s",
    failover: {
      enabled: true,
      mode: "primary_standby",
      scope: "model_key",
    },
    truncation: {
      enabled: false,
      max_retries: 3,
      strategy: "percentage",
      step: 10,
    },
    providers: [],
  };
}

export function listRuntimeProviderGroupSummaries(
  value: unknown,
): RuntimeProviderGroupSummary[] {
  if (!isConfigRecord(value) || !Array.isArray(value.provider_groups)) {
    return [];
  }

  return value.provider_groups
    .flatMap((item) => (isConfigRecord(item) ? [buildProviderGroupSummary(item)] : []))
    .sort((left, right) => left.name.localeCompare(right.name));
}

export function buildProviderGroupCreateConfigSnippet(
  group: RuntimeProviderGroupSummary,
) {
  return buildArrayItemConfigSnippet(group.raw, [
    "name",
    "strategy",
    "max_retries",
    "retry_delay",
    "failover",
    "truncation",
    "providers",
  ]);
}

export function getRuntimeAuthConfig(value: unknown): RuntimeAuthConfigSummary {
  const authRoot =
    isConfigRecord(value) && isConfigRecord(value.auth) ? value.auth : {};
  const accessAuth =
    isConfigRecord(authRoot.access_auth) ? authRoot.access_auth : {};

  return {
    jwtSecret: readText(authRoot.jwt_secret),
    accessKeySecret: readText(authRoot.access_key_secret),
    jwtExpire: readText(authRoot.jwt_expire),
    sessionTimeout: readText(authRoot.session_timeout),
    maxApiCreateTimes: readText(authRoot.max_api_create_times),
    adminAuthEnabled: Boolean(authRoot.admin_auth_enabled),
    adminToken: readText(authRoot.admin_token),
    accessAuthEnabled: Boolean(accessAuth.enabled),
    accessAuthAllowAnonymous: Boolean(accessAuth.allow_anonymous),
  };
}

function buildProviderGroupSummary(
  raw: Record<string, unknown>,
): RuntimeProviderGroupSummary {
  const failover = isConfigRecord(raw.failover) ? raw.failover : {};
  const truncation = isConfigRecord(raw.truncation) ? raw.truncation : {};
  const providers = Array.isArray(raw.providers)
    ? raw.providers.flatMap((item) =>
        isConfigRecord(item)
          ? [
              {
                name: readText(item.name),
                role: readText(item.role),
                weight: readText(item.weight),
                enabled: item.enabled !== false,
              },
            ]
          : [],
      )
    : [];

  return {
    name: readText(raw.name),
    raw,
    strategy: readText(raw.strategy),
    maxRetries: readText(raw.max_retries),
    retryDelay: readText(raw.retry_delay),
    failoverEnabled: Boolean(failover.enabled),
    failoverMode: readText(failover.mode),
    failoverScope: readText(failover.scope),
    truncationEnabled: Boolean(truncation.enabled),
    truncationMaxRetries: readText(truncation.max_retries),
    truncationStrategy: readText(truncation.strategy),
    truncationStep: readText(truncation.step),
    providers,
    providerCount: providers.length,
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
