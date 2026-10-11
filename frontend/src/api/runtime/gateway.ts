import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

export type RuntimeGatewayState =
  | "disabled"
  | "disconnected"
  | "connecting"
  | "connected"
  | "reconnecting"
  | "disconnecting"
  | "error";

export type RuntimeGatewayStatus = {
  state: RuntimeGatewayState;
  node_id?: string;
  gateway_url?: string;
  console_url?: string;
  connected_at?: string;
  last_heartbeat?: string;
  last_error?: string;
  error_code?: string;
  retryable?: boolean;
  reconnect_count: number;
  sessions: number;
  mode: string;
  operator_disconnected: boolean;
  token_hint?: string;
};

export type RuntimeGatewayConfig = {
  enabled: boolean;
  url: string;
  name: string;
  auto_connect: boolean;
  routes?: string[];
  token_hint?: string;
};

export type RuntimeGatewayConfigUpdate = {
  url?: string;
  name?: string;
  token?: string;
  auto_connect?: boolean;
};

export type RuntimeGatewaySnapshot = {
  config: RuntimeGatewayConfig;
  status: RuntimeGatewayStatus;
};

const gatewayStatusUrl = buildRuntimeUrl("/api/runtime/gateway/status");
const gatewayConnectUrl = buildRuntimeUrl("/api/runtime/gateway/connect");
const gatewayDisconnectUrl = buildRuntimeUrl("/api/runtime/gateway/disconnect");
const gatewayReconnectUrl = buildRuntimeUrl("/api/runtime/gateway/reconnect");
const gatewayConfigUrl = buildRuntimeUrl("/api/runtime/gateway/config");

export async function getGatewayStatus(): Promise<RuntimeGatewayStatus> {
  const response = await fetchRuntimeJson<{ status: RuntimeGatewayStatus }>(
    gatewayStatusUrl,
  );
  return response.status;
}

async function postGatewayAction(url: string): Promise<RuntimeGatewayStatus> {
  const response = await fetchRuntimeJson<{ status: RuntimeGatewayStatus }>(
    url,
    { method: "POST" },
  );
  return response.status;
}

export function connectGateway(): Promise<RuntimeGatewayStatus> {
  return postGatewayAction(gatewayConnectUrl);
}

export function disconnectGateway(): Promise<RuntimeGatewayStatus> {
  return postGatewayAction(gatewayDisconnectUrl);
}

export function reconnectGateway(): Promise<RuntimeGatewayStatus> {
  return postGatewayAction(gatewayReconnectUrl);
}

export async function getGatewayConfig(): Promise<RuntimeGatewaySnapshot> {
  return fetchRuntimeJson<RuntimeGatewaySnapshot>(gatewayConfigUrl);
}

export async function updateGatewayConfig(
  update: RuntimeGatewayConfigUpdate,
): Promise<RuntimeGatewaySnapshot> {
  return fetchRuntimeJson<RuntimeGatewaySnapshot>(gatewayConfigUrl, {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(update),
  });
}
