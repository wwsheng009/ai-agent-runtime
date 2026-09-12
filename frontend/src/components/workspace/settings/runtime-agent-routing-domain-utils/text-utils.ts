// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

export function readText(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

export function parseNonNegativeInteger(value: string) {
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
}

export function setOptionalText(
  target: Record<string, unknown>,
  key: string,
  value: string,
) {
  const normalized = value.trim();
  if (normalized) {
    target[key] = normalized;
  } else {
    delete target[key];
  }
}
