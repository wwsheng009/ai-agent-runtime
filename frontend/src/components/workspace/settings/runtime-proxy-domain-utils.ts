export type RuntimeProxyConfigSummary = {
  enabled: boolean;
  http: string;
  https: string;
  noProxy: string;
};

export function getRuntimeProxyConfig(value: unknown): RuntimeProxyConfigSummary {
  const providersRoot =
    isConfigRecord(value) && isConfigRecord(value.providers) ? value.providers : {};
  return readRuntimeProxyConfig(providersRoot.proxy);
}

export function readRuntimeProxyConfig(
  value: unknown,
): RuntimeProxyConfigSummary {
  const proxyRoot = isConfigRecord(value) ? value : {};

  return {
    enabled: Boolean(proxyRoot.enabled),
    http: readText(proxyRoot.http),
    https: readText(proxyRoot.https),
    noProxy: readText(proxyRoot.no_proxy),
  };
}

export function hasRuntimeProxyConfig(
  config: RuntimeProxyConfigSummary,
): boolean {
  return (
    config.enabled ||
    config.http.trim() !== "" ||
    config.https.trim() !== "" ||
    config.noProxy.trim() !== ""
  );
}

export function buildRuntimeProxyRecord(
  config: RuntimeProxyConfigSummary,
): Record<string, unknown> {
  return {
    enabled: config.enabled,
    http: config.http.trim(),
    https: config.https.trim(),
    no_proxy: config.noProxy.trim(),
  };
}

export function summarizeRuntimeProxyConfig(
  config: RuntimeProxyConfigSummary,
): string {
  if (!hasRuntimeProxyConfig(config)) {
    return "环境变量 / 直连";
  }

  const parts: string[] = [];
  if (config.http.trim()) {
    parts.push("HTTP");
  }
  if (config.https.trim()) {
    parts.push("HTTPS");
  }
  if (config.noProxy.trim()) {
    parts.push("NO_PROXY");
  }

  if (parts.length === 0) {
    return config.enabled ? "已启用" : "已关闭";
  }

  return parts.join(" + ");
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

function isConfigRecord(
  value: unknown,
): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
