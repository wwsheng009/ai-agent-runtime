// 由 components/workspace/settings/runtime-provider-queue-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

export function stringifyExtraFields(raw: Record<string, unknown>) {
  const extra = Object.fromEntries(
    Object.entries(raw).filter(
      ([key]) =>
        key !== "max_concurrency" && key !== "queue_size" && key !== "queue_timeout",
    ),
  );
  return Object.keys(extra).length > 0 ? JSON.stringify(extra, null, 2) : "";
}
