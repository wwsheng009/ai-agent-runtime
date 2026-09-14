// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Thread } from "@/data/mock";
import { readSessionReasoningEffort } from "@/lib/reasoning-effort";
import { normalizeSessionId } from "@/lib/session-id";
import {
  isSameSessionLineage,
  readSessionLineage,
} from "@/lib/workspace/session-lineage";
import { type RuntimeSessionRecord, type SessionRuntimeEvent } from "@/types/runtime";

import { mergeUniqueStrings } from "./shared";

export function mergeRuntimeSessionsIntoThreads(
  threads: Thread[],
  sessions: RuntimeSessionRecord[],
) {
  const nextThreads = [...threads];
  const droppedThreadIds = new Set<string>();
  let changed = false;

  for (const session of sessions) {
    const sessionId = normalizeSessionId(session?.id);
    if (!sessionId) {
      continue;
    }
    const matches = nextThreads
      .map((thread, index) => ({ index, thread }))
      .filter(
        ({ thread }) =>
          normalizeSessionId(thread.sessionId) === sessionId ||
          normalizeSessionId(thread.id) === sessionId,
      );
    // 一个 runtime session 只能对应一个 thread：本地在途会话（id !== sessionId，
    // 例如刚提交、已由 SSE meta 认领 sessionId 的新会话）优先于列表中物化出来的
    // “restored session” 占位线程，否则选中态会落到空占位上，把正在流式的消息
    // 挡在渲染之外（e2e G2/G8a/G8b 回归）。
    const existingIndex =
      matches.length > 0
        ? matches.reduce((best, candidate) =>
            compareSessionThreadCandidates(candidate, best, sessionId),
          ).index
        : -1;
    for (const match of matches) {
      if (match.index !== existingIndex) {
        droppedThreadIds.add(match.thread.id);
        changed = true;
      }
    }
    const title =
      session.metadata?.title?.trim() || `Runtime session ${sessionId.slice(0, 10)}`;
    const summary =
      session.metadata?.summary?.trim() ||
      "Restored runtime session from /api/runtime/sessions.";
    const updatedAt = session.updatedAt || session.createdAt || new Date().toISOString();
    const reasoningEffort = readSessionReasoningEffort(
      session.metadata?.context,
    );
    // 批次 3（§5.5）：快照 metadata.context 的谱系键 → Thread.forkedFrom
    // （纯前端视图字段，不入后端请求；缺键 = 非分支会话）。
    const forkedFrom = readSessionLineage(session.metadata?.context);
    const tags = mergeUniqueStrings(
      "runtime-session",
      session.state ? `state:${session.state}` : null,
      ...(session.metadata?.lastAgent ? [`agent:${session.metadata.lastAgent}`] : []),
      ...(session.metadata?.lastSkill ? [`skill:${session.metadata.lastSkill}`] : []),
      ...(session.metadata?.title ? ["restored"] : []),
    );

    if (existingIndex < 0) {
      changed = true;
      nextThreads.push({
        id: sessionId,
        title,
        summary,
        updatedAt,
        status: mapSessionStateToThreadStatus(session.state),
        sessionId,
        transport: "live",
        runtimeSource: session.metadata?.lastAgent || session.metadata?.lastSkill || "runtime",
        lastError: null,
        reasoningEffort,
        forkedFrom,
        tags,
        prompts: [
          "Sync the latest authoritative session history",
          "Inspect runtime evidence and restore points",
          "Continue this restored runtime session",
        ],
        messages: [],
        artifacts: [],
      });
      continue;
    }

    const current = nextThreads[existingIndex];
    const merged = {
      ...current,
      id: current.sessionId ? current.id : sessionId,
      title,
      summary,
      updatedAt,
      status: mapSessionStateToThreadStatus(session.state),
      sessionId,
      transport: current.transport === "error" ? "error" : "live",
      runtimeSource:
        current.runtimeSource ||
        session.metadata?.lastAgent ||
        session.metadata?.lastSkill ||
        "runtime",
      reasoningEffort,
      forkedFrom,
      tags,
    } satisfies Thread;

    if (
      merged.title !== current.title ||
      merged.summary !== current.summary ||
      merged.updatedAt !== current.updatedAt ||
      merged.status !== current.status ||
      merged.sessionId !== current.sessionId ||
      merged.transport !== current.transport ||
      merged.runtimeSource !== current.runtimeSource ||
      merged.reasoningEffort !== current.reasoningEffort ||
      !isSameSessionLineage(merged.forkedFrom, current.forkedFrom) ||
      merged.tags.join("|") !== current.tags.join("|")
    ) {
      changed = true;
      nextThreads[existingIndex] = merged;
    }
  }

  if (!changed) {
    return threads;
  }

  const dedupedThreads =
    droppedThreadIds.size > 0
      ? nextThreads.filter((thread) => !droppedThreadIds.has(thread.id))
      : nextThreads;

  return [...dedupedThreads].sort((left, right) => {
    const leftTime = Date.parse(left.updatedAt);
    const rightTime = Date.parse(right.updatedAt);
    return rightTime - leftTime;
  });
}

/**
 * 同一 session 出现多个线程时挑选“真身”：
 * 1. 优先本地线程（id !== sessionId，即用户自己创建/正在流式的那条）；
 * 2. 其次消息更多的线程（已有历史投影/流式内容）；
 * 3. 最后保持原有顺序（reduce 稳定性）。
 */
function compareSessionThreadCandidates(
  candidate: { index: number; thread: Thread },
  best: { index: number; thread: Thread },
  sessionId: string,
) {
  const candidateIsAlias = normalizeSessionId(candidate.thread.id) !== sessionId;
  const bestIsAlias = normalizeSessionId(best.thread.id) !== sessionId;
  if (candidateIsAlias !== bestIsAlias) {
    return candidateIsAlias ? candidate : best;
  }
  return candidate.thread.messages.length > best.thread.messages.length
    ? candidate
    : best;
}

export function getRuntimeEventSeq(event: SessionRuntimeEvent) {
  const rawSeq = event.payload?.seq;
  if (typeof rawSeq === "number" && Number.isFinite(rawSeq)) {
    return rawSeq;
  }
  if (typeof rawSeq === "string") {
    const parsed = Number(rawSeq);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return 0;
}

function mapSessionStateToThreadStatus(
  state: string | undefined,
): Thread["status"] {
  switch ((state || "").trim().toLowerCase()) {
    case "archived":
    case "closed":
      return "review";
    case "draft":
      return "draft";
    default:
      return "active";
  }
}
