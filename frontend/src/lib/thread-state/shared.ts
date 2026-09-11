// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact, type Thread } from "@/data/mock";
import { type AgentChatStreamChunkPayload } from "@/types/runtime";

export function getErrorMessage(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback;
}

export function getFirstArtifactId(thread: Thread | undefined) {
  return thread?.artifacts[0]?.id ?? null;
}

export function getStreamTextDelta(payload: AgentChatStreamChunkPayload) {
  if (payload.type !== "text") {
    return "";
  }
  if (typeof payload.content === "string") {
    return payload.content;
  }
  if (payload.text && typeof payload.text.content === "string") {
    return payload.text.content;
  }
  return "";
}

export function getToolName(payload: AgentChatStreamChunkPayload) {
  if (payload.tool && typeof payload.tool.name === "string") {
    return payload.tool.name;
  }
  if (payload.tool_call && typeof payload.tool_call.name === "string") {
    return payload.tool_call.name;
  }
  return "tool";
}

export function mergeUniqueStrings(...values: Array<string | undefined | null>) {
  const merged = new Set<string>();
  for (const value of values) {
    if (!value) {
      continue;
    }
    merged.add(value);
  }
  return [...merged];
}

export function upsertArtifact(artifacts: Artifact[], artifact: Artifact) {
  const nextArtifacts = [...artifacts];
  const existingIndex = nextArtifacts.findIndex((item) => item.id === artifact.id);
  if (existingIndex >= 0) {
    nextArtifacts[existingIndex] = artifact;
    return nextArtifacts;
  }
  return [artifact, ...nextArtifacts];
}

export function upsertArtifacts(artifacts: Artifact[], nextItems: Artifact[]) {
  return nextItems.reduce((current, artifact) => upsertArtifact(current, artifact), artifacts);
}
