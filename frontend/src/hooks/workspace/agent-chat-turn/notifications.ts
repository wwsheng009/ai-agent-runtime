// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。

export function buildNotificationBody(content: string, source: string) {
  const normalized = content.replace(/\s+/g, " ").trim();
  if (!normalized) {
    return `Runtime stream from ${source || "runtime"} finished.`;
  }

  return normalized.length > 140
    ? `${normalized.slice(0, 137).trimEnd()}...`
    : normalized;
}

export function maybeShowDesktopNotification(
  enabled: boolean,
  title: string,
  body: string,
  tag: string,
) {
  if (!enabled || typeof Notification === "undefined" || typeof document === "undefined") {
    return;
  }

  if (Notification.permission !== "granted" || document.visibilityState === "visible") {
    return;
  }

  try {
    new Notification(title, {
      body,
      tag,
    });
  } catch {
    // Ignore browser notification failures and keep the chat flow intact.
  }
}
