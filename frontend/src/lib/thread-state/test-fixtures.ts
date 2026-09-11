// P0-2 随源拆分：workspace-thread-state 测试共享夹具。
import type { Thread } from "@/data/mock";

export function createThread(): Thread {
  return {
    id: "thread-1",
    title: "Thread",
    summary: "Summary",
    updatedAt: "2026-03-31T00:00:00Z",
    status: "active",
    tags: [],
    prompts: [],
    artifacts: [],
    messages: [
      {
        id: "assistant-existing",
        role: "assistant",
        author: "Runtime stream",
        label: "streaming",
        segments: [
          {
            type: "text",
            content: "Merged answer",
          },
          {
            type: "code",
            language: "json",
            title: "Reasoning snapshot",
            code: '{"ok":true}',
          },
        ],
      },
    ],
  };
}
