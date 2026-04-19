export type ConfigPathSegment = string | number;

export type ConfigValueKind =
  | "string"
  | "number"
  | "boolean"
  | "object"
  | "array"
  | "null";

export function inferConfigValueKind(value: unknown): ConfigValueKind {
  if (Array.isArray(value)) {
    return "array";
  }
  if (value === null || value === undefined) {
    return "null";
  }
  if (typeof value === "string") {
    return "string";
  }
  if (typeof value === "number") {
    return "number";
  }
  if (typeof value === "boolean") {
    return "boolean";
  }
  return "object";
}

export function defaultConfigValueForKind(kind: ConfigValueKind): unknown {
  switch (kind) {
    case "array":
      return [];
    case "boolean":
      return false;
    case "number":
      return 0;
    case "object":
      return {};
    case "string":
      return "";
    case "null":
    default:
      return null;
  }
}

export function cloneConfigValue<T>(value: T): T {
  if (typeof structuredClone === "function") {
    return structuredClone(value);
  }
  return JSON.parse(JSON.stringify(value)) as T;
}

export function convertConfigValueKind(
  value: unknown,
  kind: ConfigValueKind,
): unknown {
  switch (kind) {
    case "string":
      return typeof value === "string" ? value : value == null ? "" : String(value);
    case "number": {
      if (typeof value === "number" && Number.isFinite(value)) {
        return value;
      }
      const parsed = Number(value);
      return Number.isFinite(parsed) ? parsed : 0;
    }
    case "boolean":
      return Boolean(value);
    case "object":
      return typeof value === "object" && value && !Array.isArray(value)
        ? value
        : {};
    case "array":
      return Array.isArray(value) ? value : [];
    case "null":
    default:
      return null;
  }
}

export function getConfigValueAtPath(
  root: unknown,
  path: ConfigPathSegment[],
): unknown {
  let current = root;
  for (const segment of path) {
    if (Array.isArray(current) && typeof segment === "number") {
      current = current[segment];
      continue;
    }
    if (
      current &&
      typeof current === "object" &&
      !Array.isArray(current) &&
      typeof segment === "string"
    ) {
      current = (current as Record<string, unknown>)[segment];
      continue;
    }
    return undefined;
  }
  return current;
}

export function setConfigValueAtPath(
  root: unknown,
  path: ConfigPathSegment[],
  nextValue: unknown,
): unknown {
  if (path.length === 0) {
    return cloneConfigValue(nextValue);
  }

  const [head, ...rest] = path;
  if (typeof head === "number") {
    const source = Array.isArray(root) ? [...root] : [];
    source[head] = setConfigValueAtPath(source[head], rest, nextValue);
    return source;
  }

  const source =
    root && typeof root === "object" && !Array.isArray(root)
      ? { ...(root as Record<string, unknown>) }
      : {};
  source[head] = setConfigValueAtPath(source[head], rest, nextValue);
  return source;
}

export function removeConfigValueAtPath(
  root: unknown,
  path: ConfigPathSegment[],
): unknown {
  if (path.length === 0) {
    return root;
  }

  const [head, ...rest] = path;
  if (rest.length === 0) {
    if (typeof head === "number") {
      if (!Array.isArray(root)) {
        return root;
      }
      const next = [...root];
      next.splice(head, 1);
      return next;
    }
    if (!root || typeof root !== "object" || Array.isArray(root)) {
      return root;
    }
    const next = { ...(root as Record<string, unknown>) };
    delete next[head];
    return next;
  }

  if (typeof head === "number") {
    if (!Array.isArray(root)) {
      return root;
    }
    const next = [...root];
    next[head] = removeConfigValueAtPath(next[head], rest);
    return next;
  }

  if (!root || typeof root !== "object" || Array.isArray(root)) {
    return root;
  }
  const next = { ...(root as Record<string, unknown>) };
  next[head] = removeConfigValueAtPath(next[head], rest);
  return next;
}
