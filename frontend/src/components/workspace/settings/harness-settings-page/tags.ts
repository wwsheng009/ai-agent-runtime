// 由 components/workspace/settings/harness-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

export function splitTags(value: string) {
  return value
    .split(/[,，\s]+/)
    .map((tag) => tag.trim())
    .filter(Boolean);
}
