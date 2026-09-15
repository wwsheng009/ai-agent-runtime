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

type IndexedThread = {
  index: number;
  thread: Thread;
  normalizedId: string;
  normalizedSessionId: string;
};

type DerivedRuntimeSession = {
  sessionId: string;
  title: string;
  summary: string;
  updatedAt: string;
  status: Thread["status"];
  tags: string[];
  forkedFrom: Thread["forkedFrom"];
  reasoningEffort: Thread["reasoningEffort"];
};

/**
 * 每个 session 记录的派生字段（标题 / 标签 / 时间戳 / 谱系…）按对象身份缓存。
 *
 * `mergeRuntimeSessionsIntoThreads` 会在每个 SSE 事件后重跑一遍（既来自
 * `use-workspace-thread-selection` 的 `setThreads` 包装，也来自它的 useMemo）。
 * 300+ 会话时，每事件都要重新拼标题字符串、建标签数组、求 ISO 时间戳。
 * 列表快照里的记录对象在一次拉取周期内身份稳定，派生结果也随之稳定，可安全复用。
 *
 * 顺带修掉一个「内容没变但结果恒变」的缺陷：缺 `updatedAt`/`createdAt` 的会话
 * 此前每次合并都会生成新的 `new Date().toISOString()`，于是 `changed` 恒为 true、
 * 线程数组每事件换新、下游 memo 全部失效（每事件一次全页重渲染）。缓存后回退
 * 时间戳在会话对象生命周期内是常量。
 */
const derivedRuntimeSessionCache = new WeakMap<
  RuntimeSessionRecord,
  DerivedRuntimeSession
>();

function deriveRuntimeSession(
  session: RuntimeSessionRecord,
): DerivedRuntimeSession | null {
  if (!session) {
    return null;
  }
  const cached = derivedRuntimeSessionCache.get(session);
  if (cached) {
    return cached;
  }
  const sessionId = normalizeSessionId(session.id);
  if (!sessionId) {
    return null;
  }
  const derived: DerivedRuntimeSession = {
    sessionId,
    title:
      session.metadata?.title?.trim() ||
      `Runtime session ${sessionId.slice(0, 10)}`,
    summary:
      session.metadata?.summary?.trim() ||
      "Restored runtime session from /api/runtime/sessions.",
    updatedAt:
      session.updatedAt || session.createdAt || new Date().toISOString(),
    status: mapSessionStateToThreadStatus(session.state),
    tags: mergeUniqueStrings(
      "runtime-session",
      session.state ? `state:${session.state}` : null,
      ...(session.metadata?.lastAgent
        ? [`agent:${session.metadata.lastAgent}`]
        : []),
      ...(session.metadata?.lastSkill
        ? [`skill:${session.metadata.lastSkill}`]
        : []),
      ...(session.metadata?.title ? ["restored"] : []),
    ),
    forkedFrom: readSessionLineage(session.metadata?.context),
    reasoningEffort: readSessionReasoningEffort(session.metadata?.context),
  };
  derivedRuntimeSessionCache.set(session, derived);
  return derived;
}

function threadIndexKeys(entry: IndexedThread) {
  const keys = [entry.normalizedId];
  if (
    entry.normalizedSessionId &&
    entry.normalizedSessionId !== entry.normalizedId
  ) {
    keys.push(entry.normalizedSessionId);
  }
  return keys.filter((key) => Boolean(key));
}

export function mergeRuntimeSessionsIntoThreads(
  threads: Thread[],
  sessions: RuntimeSessionRecord[],
) {
  const nextThreads = [...threads];
  const droppedThreadIds = new Set<string>();
  let changed = false;

  // 性能：线程 id 只归一化一次并建索引。此前每个 session 都要对全部线程重新
  // normalize（O(sessions × threads)）；真实数据 300+ 会话时单次合并约 18 万次
  // 调用（~46ms），而它在每个 SSE 事件后都会随 threadState 重跑，会把主线程
  // 冻成几十秒的单个 long task。改为 O(sessions + threads)。
  const indexedThreads: IndexedThread[] = nextThreads.map((thread, index) => ({
    index,
    thread,
    normalizedId: normalizeSessionId(thread.id),
    normalizedSessionId: normalizeSessionId(thread.sessionId),
  }));
  // 归一化 id → 线程的反向索引：把「每个 session 都 filter 一遍全部线程」
  // （300+ 会话时单次合并约 9 万次比较）降成两次 Map 查询。
  const threadsByKey = new Map<string, IndexedThread[]>();
  const addToIndex = (entry: IndexedThread) => {
    for (const key of threadIndexKeys(entry)) {
      const bucket = threadsByKey.get(key);
      if (bucket) {
        bucket.push(entry);
      } else {
        threadsByKey.set(key, [entry]);
      }
    }
  };
  const replaceInIndex = (previous: IndexedThread, next: IndexedThread) => {
    const previousKeys = threadIndexKeys(previous);
    const nextKeys = threadIndexKeys(next);
    for (const key of previousKeys) {
      const bucket = threadsByKey.get(key);
      if (!bucket) {
        continue;
      }
      const at = bucket.indexOf(previous);
      if (at < 0) {
        continue;
      }
      if (nextKeys.includes(key)) {
        bucket[at] = next;
      } else {
        bucket.splice(at, 1);
      }
    }
    for (const key of nextKeys) {
      if (previousKeys.includes(key)) {
        continue;
      }
      const bucket = threadsByKey.get(key);
      if (bucket) {
        bucket.push(next);
      } else {
        threadsByKey.set(key, [next]);
      }
    }
  };
  for (const entry of indexedThreads) {
    addToIndex(entry);
  }

  for (const session of sessions) {
    const derived = deriveRuntimeSession(session);
    if (!derived) {
      continue;
    }
    const { sessionId } = derived;
    const matches = threadsByKey.get(sessionId) ?? [];
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
    // 标题 / 标签 / 时间戳等派生字段由 deriveRuntimeSession 计算并按对象身份缓存
    // （每个 SSE 事件都会重跑本函数，见函数头注释）。
    const { title, summary, updatedAt, reasoningEffort, forkedFrom, tags } =
      derived;

    if (existingIndex < 0) {
      changed = true;
      const created: Thread = {
        id: sessionId,
        title,
        summary,
        updatedAt,
        status: derived.status,
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
      };
      nextThreads.push(created);
      const createdEntry: IndexedThread = {
        index: nextThreads.length - 1,
        thread: created,
        normalizedId: normalizeSessionId(created.id),
        normalizedSessionId: normalizeSessionId(created.sessionId),
      };
      indexedThreads.push(createdEntry);
      addToIndex(createdEntry);
      continue;
    }

    const current = nextThreads[existingIndex];
    const merged = {
      ...current,
      id: current.sessionId ? current.id : sessionId,
      title,
      summary,
      updatedAt,
      status: derived.status,
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
      const previousEntry = indexedThreads[existingIndex];
      const nextEntry: IndexedThread = {
        index: existingIndex,
        thread: merged,
        normalizedId: normalizeSessionId(merged.id),
        normalizedSessionId: normalizeSessionId(merged.sessionId),
      };
      nextThreads[existingIndex] = merged;
      indexedThreads[existingIndex] = nextEntry;
      if (previousEntry) {
        replaceInIndex(previousEntry, nextEntry);
      } else {
        addToIndex(nextEntry);
      }
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
  candidate: IndexedThread,
  best: IndexedThread,
  sessionId: string,
): IndexedThread {
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
