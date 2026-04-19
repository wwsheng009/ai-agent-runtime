export function buildNamedMapConfigSnippet(
  name: string,
  value: Record<string, unknown>,
) {
  return `${formatYamlKey(name)}:\n${stringifyYamlObject(value, 2)}`;
}

export function buildArrayItemConfigSnippet(
  value: Record<string, unknown>,
  preferredKeys: string[] = [],
) {
  const orderedValue = reorderRecordKeys(value, preferredKeys);
  return stringifyYamlArray([orderedValue], 0);
}

function reorderRecordKeys(
  value: Record<string, unknown>,
  preferredKeys: string[],
) {
  if (preferredKeys.length === 0) {
    return value;
  }

  const orderedEntries = preferredKeys.flatMap((key) =>
    Object.prototype.hasOwnProperty.call(value, key) ? [[key, value[key]]] : [],
  );
  const remainingEntries = Object.entries(value).filter(
    ([key]) => !preferredKeys.includes(key),
  );

  return Object.fromEntries([...orderedEntries, ...remainingEntries]);
}

function stringifyYamlObject(
  value: Record<string, unknown>,
  indent: number,
): string {
  const entries = Object.entries(value);
  if (entries.length === 0) {
    return `${" ".repeat(indent)}{}`;
  }

  return entries
    .map(([key, childValue]) =>
      stringifyYamlEntry(formatYamlKey(key), childValue, indent),
    )
    .join("\n");
}

function stringifyYamlArray(value: unknown[], indent: number): string {
  if (value.length === 0) {
    return `${" ".repeat(indent)}[]`;
  }

  return value
    .map((item) => {
      const padding = " ".repeat(indent);
      if (isYamlScalar(item)) {
        return `${padding}- ${formatYamlScalar(item)}`;
      }
      if (Array.isArray(item) && item.length === 0) {
        return `${padding}- []`;
      }
      if (isConfigRecord(item) && Object.keys(item).length === 0) {
        return `${padding}- {}`;
      }
      return `${padding}-\n${stringifyYamlValue(item, indent + 2)}`;
    })
    .join("\n");
}

function stringifyYamlEntry(
  key: string,
  value: unknown,
  indent: number,
): string {
  const padding = " ".repeat(indent);
  if (isYamlScalar(value)) {
    return `${padding}${key}: ${formatYamlScalar(value)}`;
  }
  if (Array.isArray(value) && value.length === 0) {
    return `${padding}${key}: []`;
  }
  if (isConfigRecord(value) && Object.keys(value).length === 0) {
    return `${padding}${key}: {}`;
  }
  return `${padding}${key}:\n${stringifyYamlValue(value, indent + 2)}`;
}

function stringifyYamlValue(
  value: unknown,
  indent: number,
): string {
  if (Array.isArray(value)) {
    return stringifyYamlArray(value, indent);
  }
  if (isConfigRecord(value)) {
    return stringifyYamlObject(value, indent);
  }
  return `${" ".repeat(indent)}${formatYamlScalar(value)}`;
}

function isYamlScalar(value: unknown) {
  return (
    value == null ||
    typeof value === "boolean" ||
    typeof value === "number" ||
    typeof value === "string"
  );
}

function formatYamlScalar(value: unknown) {
  if (value == null) {
    return "null";
  }
  if (typeof value === "boolean" || typeof value === "number") {
    return String(value);
  }
  return JSON.stringify(String(value));
}

function formatYamlKey(value: string) {
  return /^[A-Za-z0-9_.-]+$/.test(value) ? value : JSON.stringify(value);
}

function isConfigRecord(
  value: unknown,
): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
