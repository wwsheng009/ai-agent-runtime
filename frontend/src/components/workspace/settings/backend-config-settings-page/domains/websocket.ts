// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { type RuntimeWebsocketConfigSummary, normalizeWebsocketProtocols } from "../../runtime-websocket-domain-utils";
import { parseLooseScalar } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function createWebsocketDomain(core: ConfigEditorCore) {
  const {
    setDraftParsed,
  } = core;

  function handleWebsocketConfigChange(
    nextWebsocketConfig: RuntimeWebsocketConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentWebsocketValue = getConfigValueAtPath(current, [
        "websocket",
      ]);
      const currentWebsocket = isConfigRecord(currentWebsocketValue)
        ? currentWebsocketValue
        : {};
      const currentResponses = isConfigRecord(currentWebsocket.responses)
        ? currentWebsocket.responses
        : {};
      const currentResponsesCapacity = isConfigRecord(currentResponses.capacity)
        ? currentResponses.capacity
        : {};
      const currentResponsesMetrics = isConfigRecord(currentResponses.metrics)
        ? currentResponses.metrics
        : {};
      const currentRealtime = isConfigRecord(currentWebsocket.realtime)
        ? currentWebsocket.realtime
        : {};
      const currentRealtimeCapacity = isConfigRecord(currentRealtime.capacity)
        ? currentRealtime.capacity
        : {};
      const currentRealtimeMetrics = isConfigRecord(currentRealtime.metrics)
        ? currentRealtime.metrics
        : {};

      return setConfigValueAtPath(current, ["websocket"], {
        ...currentWebsocket,
        enabled: nextWebsocketConfig.enabled,
        responses: {
          ...currentResponses,
          ingress_enabled: nextWebsocketConfig.responsesIngressEnabled,
          http_bridge_enabled: nextWebsocketConfig.responsesHttpBridgeEnabled,
          compat_bridge_enabled:
            nextWebsocketConfig.responsesCompatBridgeEnabled,
          compat_bridge_source_protocols: normalizeWebsocketProtocols(
            nextWebsocketConfig.responsesCompatBridgeSourceProtocolsText,
          ),
          allow_passthrough_only:
            nextWebsocketConfig.responsesAllowPassthroughOnly,
          capacity: {
            ...currentResponsesCapacity,
            max_active_connections: parseLooseScalar(
              nextWebsocketConfig.responsesMaxActiveConnections,
            ),
          },
          metrics: {
            ...currentResponsesMetrics,
            enabled: nextWebsocketConfig.responsesMetricsEnabled,
            close_code_labels_enabled:
              nextWebsocketConfig.responsesCloseCodeLabelsEnabled,
          },
          connection_pooling_enabled:
            nextWebsocketConfig.responsesConnectionPoolingEnabled,
          affinity_ttl: nextWebsocketConfig.responsesAffinityTtl,
          pre_first_event_retry_once:
            nextWebsocketConfig.responsesPreFirstEventRetryOnce,
          handshake_max_retries: parseLooseScalar(
            nextWebsocketConfig.responsesHandshakeMaxRetries,
          ),
          failover_on_handshake_error:
            nextWebsocketConfig.responsesFailoverOnHandshakeError,
        },
        realtime: {
          ...currentRealtime,
          ingress_enabled: nextWebsocketConfig.realtimeIngressEnabled,
          capacity: {
            ...currentRealtimeCapacity,
            max_active_connections: parseLooseScalar(
              nextWebsocketConfig.realtimeMaxActiveConnections,
            ),
          },
          metrics: {
            ...currentRealtimeMetrics,
            enabled: nextWebsocketConfig.realtimeMetricsEnabled,
            close_code_labels_enabled:
              nextWebsocketConfig.realtimeCloseCodeLabelsEnabled,
          },
          handshake_max_retries: parseLooseScalar(
            nextWebsocketConfig.realtimeHandshakeMaxRetries,
          ),
          failover_on_handshake_error:
            nextWebsocketConfig.realtimeFailoverOnHandshakeError,
        },
      });
    });
  }


  return {
    handleWebsocketConfigChange,
  };
}

export type WebsocketDomain = ReturnType<typeof createWebsocketDomain>;
