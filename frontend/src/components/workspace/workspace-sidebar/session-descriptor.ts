// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Thread } from "@/data/mock";
import { type RuntimeSessionRecord } from "@/lib/runtime-api";

export function buildSessionDescriptorThread(
  session: RuntimeSessionRecord,
  title: string,
): Thread {
  return {
    id: session.id,
    title,
    summary: session.metadata?.summary ?? "",
    updatedAt: session.updatedAt || session.createdAt || "",
    status: "active",
    sessionId: session.id,
    tags: ["runtime-session"],
    prompts: [],
    messages: [],
    artifacts: [],
  };
}
