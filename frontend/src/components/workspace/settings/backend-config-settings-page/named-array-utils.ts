// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { setConfigValueAtPath } from "../runtime-config-editor-utils";
import { isConfigRecord } from "../runtime-provider-config-utils";


export function upsertNamedArrayRecord(
  root: unknown,
  key: string,
  previousName: string | null,
  nextName: string,
  record: Record<string, unknown>,
) {
  const rootRecord = isConfigRecord(root) ? root : {};
  const currentItems = Array.isArray(rootRecord[key]) ? rootRecord[key] : [];
  let replaced = false;

  const nextItems = currentItems
    .map((item) => {
      if (
        previousName &&
        isConfigRecord(item) &&
        typeof item.name === "string" &&
        item.name === previousName
      ) {
        replaced = true;
        return record;
      }
      return item;
    })
    .filter((item) => {
      if (
        previousName &&
        previousName !== nextName &&
        isConfigRecord(item) &&
        typeof item.name === "string" &&
        item.name === nextName
      ) {
        return false;
      }
      return true;
    });

  if (!replaced) {
    nextItems.push(record);
  }

  return setConfigValueAtPath(root, [key], nextItems);
}

export function removeNamedArrayRecord(root: unknown, key: string, name: string) {
  const rootRecord = isConfigRecord(root) ? root : {};
  const currentItems = Array.isArray(rootRecord[key]) ? rootRecord[key] : [];
  return setConfigValueAtPath(
    root,
    [key],
    currentItems.filter(
      (item) =>
        !(
          isConfigRecord(item) &&
          typeof item.name === "string" &&
          item.name === name
        ),
    ),
  );
}
