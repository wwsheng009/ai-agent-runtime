import { describe, expect, it } from "vitest";

import {
  getRuntimeWebsocketConfig,
  normalizeWebsocketProtocols,
} from "./runtime-websocket-domain-utils";

describe("runtime-websocket-domain-utils", () => {
  it("reads websocket summary for the dedicated form editor", () => {
    expect(
      getRuntimeWebsocketConfig({
        websocket: {
          enabled: true,
          responses: {
            ingress_enabled: true,
            http_bridge_enabled: false,
            compat_bridge_enabled: true,
            compat_bridge_source_protocols: ["openai", "codex"],
            allow_passthrough_only: false,
            capacity: {
              max_active_connections: 0,
            },
            metrics: {
              enabled: true,
              close_code_labels_enabled: true,
            },
            connection_pooling_enabled: true,
            affinity_ttl: "65m",
            pre_first_event_retry_once: true,
            handshake_max_retries: 2,
            failover_on_handshake_error: true,
          },
          realtime: {
            ingress_enabled: true,
            capacity: {
              max_active_connections: 10,
            },
            metrics: {
              enabled: true,
              close_code_labels_enabled: false,
            },
            handshake_max_retries: 1,
            failover_on_handshake_error: true,
          },
        },
      }),
    ).toMatchObject({
      enabled: true,
      responsesIngressEnabled: true,
      responsesCompatBridgeEnabled: true,
      responsesCompatBridgeSourceProtocolsText: "openai\ncodex",
      responsesMaxActiveConnections: "0",
      responsesHandshakeMaxRetries: "2",
      realtimeIngressEnabled: true,
      realtimeMaxActiveConnections: "10",
      realtimeHandshakeMaxRetries: "1",
      realtimeCloseCodeLabelsEnabled: false,
    });
  });

  it("normalizes websocket protocols input", () => {
    expect(normalizeWebsocketProtocols("openai\n codex, realtime")).toEqual([
      "openai",
      "codex",
      "realtime",
    ]);
  });
});
