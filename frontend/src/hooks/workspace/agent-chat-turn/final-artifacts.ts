// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import type { Artifact } from "@/data/mock";
import { buildTurnJsonArtifact } from "@/lib/workspace-thread-state";
import type {
  AgentChatResult,
  AgentChatStreamChunkPayload,
  AgentChatStreamDonePayload,
} from "@/types/runtime";

export function buildFinalArtifacts(
  turnId: string,
  payload: AgentChatStreamDonePayload,
  currentSessionId: string,
  currentSource: string,
  finalResult: AgentChatResult | null,
  planningPayload: Record<string, unknown> | null,
  orchestrationPayload: Record<string, unknown> | null,
  routePayload: Record<string, unknown> | null,
  observationPayloads: Record<string, unknown>[],
  subagentPayloads: Record<string, unknown>[],
  toolPayloads: AgentChatStreamChunkPayload[],
  generatedImageArtifacts: Artifact[],
) {
  const artifacts: Artifact[] = [
    buildTurnJsonArtifact(
      turnId,
      "agent-chat-response",
      "Final SSE response envelope reconstructed in the frontend workspace.",
      {
        session_id: currentSessionId,
        agent_id: payload.agent_id ?? "",
        source: payload.source ?? currentSource,
        status: payload.status ?? "completed",
        result: finalResult,
        planning: planningPayload,
        orchestration: orchestrationPayload,
        route: routePayload,
        observations: observationPayloads,
        subagents: subagentPayloads,
        tool_events: toolPayloads,
      },
    ),
  ];

  if (planningPayload) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "planning",
        "Planning payload emitted by /api/agent/chat SSE.",
        planningPayload,
      ),
    );
  }
  if (orchestrationPayload) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "orchestration",
        "Structured orchestration summary emitted by /api/agent/chat SSE.",
        orchestrationPayload,
      ),
    );
  }
  if (toolPayloads.length > 0) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "tool-events",
        "Tool events observed during agent chat SSE.",
        toolPayloads,
      ),
    );
  }
  if (routePayload) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "route",
        "Route metadata emitted by static SSE execution.",
        routePayload,
      ),
    );
  }
  if (observationPayloads.length > 0) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "observations",
        "Observation events emitted by static SSE execution.",
        observationPayloads,
      ),
    );
  }
  if (subagentPayloads.length > 0) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "subagents",
        "Subagent events emitted by static SSE execution.",
        subagentPayloads,
      ),
    );
  }

  if (generatedImageArtifacts.length > 0) {
    artifacts.push(...generatedImageArtifacts);
  }

  return artifacts;
}
