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

/**
 * 文本帧是否携带「权威全文替换」语义（`mode=replace|snapshot`）。
 *
 * 静态分支（非 ReAct 的 skill-route / fallback 收口）现在也会开流：增量先经
 * runtime 通道逐步渲染，回合末再由 `streamStaticResult` 下发同一段全文。该帧
 * 必须覆盖而不是追加，否则整段正文会被拼接两遍。缺省/未知 mode 保持历史
 * append 语义（旧后端兼容）。
 */
export function isReplaceStreamChunk(payload: AgentChatStreamChunkPayload) {
  const mode = payload.mode?.trim().toLowerCase();
  return mode === "replace" || mode === "snapshot";
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
