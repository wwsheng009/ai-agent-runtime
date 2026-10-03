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

/**
 * 单线程产物驻留上限：新→旧保留。
 *
 * 每个回合都会追加多个 JSON 产物（tool-events / orchestration / planning /
 * request…），产物内容是该回合完整 payload 的序列化副本；不设上限时
 * `thread.artifacts` 随回合数线性增长，且会话切换后仍常驻（线程 store 不做载荷
 * 淘汰）。保留最近 64 个（约十几回合的完整记录 + 历史/运行事件产物）已足够面板
 * 与 `@` 引用使用，更早的产物在被引用处按「缺失即忽略」处理（见 message-list /
 * message-row 的 artifactMap 解析），不会破坏渲染。
 */
export const MAX_THREAD_ARTIFACTS = 64;

export function upsertArtifact(artifacts: Artifact[], artifact: Artifact) {
  const nextArtifacts = [...artifacts];
  const existingIndex = nextArtifacts.findIndex((item) => item.id === artifact.id);
  if (existingIndex >= 0) {
    nextArtifacts[existingIndex] = artifact;
    return nextArtifacts;
  }
  // 新产物插到最前；超出上限时从尾部（最旧）截断。
  return [artifact, ...nextArtifacts].slice(0, MAX_THREAD_ARTIFACTS);
}

export function upsertArtifacts(artifacts: Artifact[], nextItems: Artifact[]) {
  return nextItems.reduce((current, artifact) => upsertArtifact(current, artifact), artifacts);
}
