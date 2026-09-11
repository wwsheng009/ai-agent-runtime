// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import type { Thread } from "@/data/mock";

export function createThreadFromPrompt(prompt: string): Thread {
  const now = new Date().toISOString();
  const normalized = prompt.replace(/\s+/g, " ").trim();
  const title =
    normalized.length > 56 ? `${normalized.slice(0, 56).trimEnd()}...` : normalized;

  return {
    id: `thread-${crypto.randomUUID()}`,
    title: title || "New chat",
    summary:
      normalized.length > 120
        ? `${normalized.slice(0, 120).trimEnd()}...`
        : normalized,
    updatedAt: now,
    status: "draft",
    transport: "mock",
    runtimeEventCount: 0,
    lastError: null,
    tags: ["new", "workspace"],
    prompts: [],
    messages: [],
    artifacts: [],
  };
}
