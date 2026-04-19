import type {
  AgentChatRequest,
  AgentChatResponse,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

export async function sendAgentChat(
  request: AgentChatRequest,
): Promise<AgentChatResponse> {
  return fetchRuntimeJson<AgentChatResponse>(buildRuntimeUrl("/api/agent/chat"), {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      ...request,
      stream: false,
    }),
  });
}
