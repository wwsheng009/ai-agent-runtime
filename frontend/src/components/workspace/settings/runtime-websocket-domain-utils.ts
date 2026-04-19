import {
  isConfigRecord,
  normalizeStringArrayInput,
} from "./runtime-provider-config-utils";

export type RuntimeWebsocketConfigSummary = {
  enabled: boolean;
  realtimeCloseCodeLabelsEnabled: boolean;
  realtimeFailoverOnHandshakeError: boolean;
  realtimeHandshakeMaxRetries: string;
  realtimeIngressEnabled: boolean;
  realtimeMaxActiveConnections: string;
  realtimeMetricsEnabled: boolean;
  responsesAffinityTtl: string;
  responsesAllowPassthroughOnly: boolean;
  responsesCloseCodeLabelsEnabled: boolean;
  responsesCompatBridgeEnabled: boolean;
  responsesCompatBridgeSourceProtocolsText: string;
  responsesConnectionPoolingEnabled: boolean;
  responsesFailoverOnHandshakeError: boolean;
  responsesHandshakeMaxRetries: string;
  responsesHttpBridgeEnabled: boolean;
  responsesIngressEnabled: boolean;
  responsesMaxActiveConnections: string;
  responsesMetricsEnabled: boolean;
  responsesPreFirstEventRetryOnce: boolean;
};

export function getRuntimeWebsocketConfig(
  value: unknown,
): RuntimeWebsocketConfigSummary {
  const websocketRoot =
    isConfigRecord(value) && isConfigRecord(value.websocket) ? value.websocket : {};
  const responses = isConfigRecord(websocketRoot.responses)
    ? websocketRoot.responses
    : {};
  const responsesCapacity = isConfigRecord(responses.capacity)
    ? responses.capacity
    : {};
  const responsesMetrics = isConfigRecord(responses.metrics)
    ? responses.metrics
    : {};
  const realtime = isConfigRecord(websocketRoot.realtime) ? websocketRoot.realtime : {};
  const realtimeCapacity = isConfigRecord(realtime.capacity)
    ? realtime.capacity
    : {};
  const realtimeMetrics = isConfigRecord(realtime.metrics)
    ? realtime.metrics
    : {};

  return {
    enabled: Boolean(websocketRoot.enabled),
    responsesIngressEnabled: Boolean(responses.ingress_enabled),
    responsesHttpBridgeEnabled: Boolean(responses.http_bridge_enabled),
    responsesCompatBridgeEnabled: Boolean(responses.compat_bridge_enabled),
    responsesCompatBridgeSourceProtocolsText: readTextArray(
      responses.compat_bridge_source_protocols,
    ).join("\n"),
    responsesAllowPassthroughOnly: Boolean(responses.allow_passthrough_only),
    responsesMaxActiveConnections: readText(
      responsesCapacity.max_active_connections,
    ),
    responsesMetricsEnabled: Boolean(responsesMetrics.enabled),
    responsesCloseCodeLabelsEnabled: Boolean(
      responsesMetrics.close_code_labels_enabled,
    ),
    responsesConnectionPoolingEnabled: Boolean(
      responses.connection_pooling_enabled,
    ),
    responsesAffinityTtl: readText(responses.affinity_ttl),
    responsesPreFirstEventRetryOnce: Boolean(
      responses.pre_first_event_retry_once,
    ),
    responsesHandshakeMaxRetries: readText(responses.handshake_max_retries),
    responsesFailoverOnHandshakeError: Boolean(
      responses.failover_on_handshake_error,
    ),
    realtimeIngressEnabled: Boolean(realtime.ingress_enabled),
    realtimeMaxActiveConnections: readText(realtimeCapacity.max_active_connections),
    realtimeMetricsEnabled: Boolean(realtimeMetrics.enabled),
    realtimeCloseCodeLabelsEnabled: Boolean(
      realtimeMetrics.close_code_labels_enabled,
    ),
    realtimeHandshakeMaxRetries: readText(realtime.handshake_max_retries),
    realtimeFailoverOnHandshakeError: Boolean(
      realtime.failover_on_handshake_error,
    ),
  };
}

export function normalizeWebsocketProtocols(value: string) {
  return normalizeStringArrayInput(value);
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

function readTextArray(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => (typeof item === "string" ? item.trim() : ""))
    .filter(Boolean);
}
