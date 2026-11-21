import type {
  RuntimeAccessLoginResponse,
  RuntimeAccessLogoutResponse,
  RuntimeAccessStatus,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

const accessStatusUrl = buildRuntimeUrl("/api/runtime/auth/access/status");
const accessLoginUrl = buildRuntimeUrl("/api/runtime/auth/access/login");
const accessLogoutUrl = buildRuntimeUrl("/api/runtime/auth/access/logout");

export function getRuntimeAccessStatus() {
  return fetchRuntimeJson<RuntimeAccessStatus>(accessStatusUrl, {
    cache: "no-store",
  });
}

export function loginRuntimeAccess(token: string) {
  return fetchRuntimeJson<RuntimeAccessLoginResponse>(accessLoginUrl, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token: token.trim() }),
  });
}

export function logoutRuntimeAccess() {
  return fetchRuntimeJson<RuntimeAccessLogoutResponse>(accessLogoutUrl, {
    method: "POST",
  });
}
