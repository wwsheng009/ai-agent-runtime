// P2-1A：子代理控制面状态机（lineage 面包屑 + 后代目录 + 独立 Stop / Resume）。
//
// 数据来源：AgentControl 身份图（`GET /api/runtime/agent-control/agents`）。
// 两段式加载：
//   ① 按 `session_id` 查当前会话自己的身份行 → 得到 `root_session_id` 与
//      `agent_path`（子会话的 root 不是自己）；
//   ② 按 `root_session_id` 拉整棵身份树（`include_closed=true`，默认上限 500），
//      再在前端派生 lineage / 后代。
//
// 收口纪律：
//   * 一次加载 = 一个 AbortController + 序号：切换会话 / 卸载立即 abort，
//     过期响应按序号丢弃，主动 abort 不落错误态；
//   * 身份行缺失 ≠ 错误：`hasIdentity=false` 如实呈现「未登记身份」，
//     不伪造一条 root 记录；
//   * 触顶口径用后端 `count` 与返回行数比较（`truncated`），不按数组长度改写；
//   * close / resume 成功只接受响应里返回的身份行覆盖本地该行，
//     失败保留错误交给 UI 分类（403 / 404 / 503 / 500），不本地预改状态。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  closeRuntimeAgent,
  isAgentControlUnavailable,
  listRuntimeAgents,
  resumeRuntimeAgent,
} from "@/api/runtime/agents";

import type { RuntimeAgentCatalog, RuntimeAgentRecord, RuntimeAgentStatus } from "@/types/runtime";

export type AgentControlStatus = "idle" | "loading" | "ready" | "error" | "unavailable";

export type UseSessionAgentsOptions = {
  /** 当前会话 id；空串表示未附着会话（状态恒为 idle，不发请求）。 */
  sessionId?: string;
  /** 整棵树的 limit（默认 500，前端上限 500）。 */
  limit?: number;
};

export type SessionAgentTree = {
  currentAgent: RuntimeAgentRecord | null;
  /** root → … → 当前会话（含当前）；无身份行时为 []。 */
  lineage: RuntimeAgentRecord[];
  /** 当前会话的后代（含孙代），按深度 / 路径排序。 */
  descendants: RuntimeAgentRecord[];
  /** 当前会话自己的身份行是否存在（后端投影未覆盖时为 false）。 */
  hasIdentity: boolean;
};

export type UseSessionAgentsResult = {
  status: AgentControlStatus;
  error: unknown;
  catalog: RuntimeAgentCatalog | null;
  /** 后端 `count` 大于返回行数：目录触顶，UI 需如实提示。 */
  truncated: boolean;
  tree: SessionAgentTree;
  refresh: () => void;
  /** 停止一个子代理；返回是否成功（失败原因见 `actionError`）。 */
  closeAgent: (agentId: string) => Promise<boolean>;
  /** 恢复一个已关闭的子代理；返回是否成功。 */
  resumeAgent: (agentId: string) => Promise<boolean>;
  pendingAgentId: string | null;
  actionError: unknown;
  actionErrorAgentId: string | null;
};

const STATUS_RANK: Record<RuntimeAgentStatus, number> = {
  active: 0,
  stale: 1,
  closed: 2,
  unknown: 3,
};

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

/**
 * 从身份行里挑出当前会话自己的一条。
 *
 * 同一会话理论上只有一行身份；若出现多行（历史投影残留），按
 * 「非终态优先 → 路径更深优先 → agent_id 稳定排序」挑选，保证结果确定。
 */
export function pickCurrentAgent(
  agents: RuntimeAgentRecord[],
  sessionId: string,
): RuntimeAgentRecord | null {
  const target = sessionId.trim();
  if (!target) {
    return null;
  }
  const matches = agents.filter((agent) => agent.sessionId === target);
  if (matches.length === 0) {
    return null;
  }
  return [...matches].sort((a, b) => {
    const rank = STATUS_RANK[a.status] - STATUS_RANK[b.status];
    if (rank !== 0) {
      return rank;
    }
    const depth = (b.depth ?? 0) - (a.depth ?? 0);
    if (depth !== 0) {
      return depth;
    }
    return a.agentId.localeCompare(b.agentId);
  })[0]!;
}

/** 从当前身份行向上回溯 parent_session_id，得到 root → current 链。 */
export function buildAgentLineage(
  agents: RuntimeAgentRecord[],
  current: RuntimeAgentRecord | null,
): RuntimeAgentRecord[] {
  if (!current) {
    return [];
  }
  const bySessionId = new Map<string, RuntimeAgentRecord>();
  for (const agent of agents) {
    if (agent.sessionId && !bySessionId.has(agent.sessionId)) {
      bySessionId.set(agent.sessionId, agent);
    }
  }

  const chain: RuntimeAgentRecord[] = [current];
  const seen = new Set<string>([current.agentId]);
  let cursor = current;
  while (cursor.parentSessionId) {
    const parent = bySessionId.get(cursor.parentSessionId);
    if (!parent || seen.has(parent.agentId)) {
      break;
    }
    chain.push(parent);
    seen.add(parent.agentId);
    cursor = parent;
  }
  return chain.reverse();
}

/** 当前会话的后代：agent_path 以 `<current>/` 开头的全部身份行。 */
export function listAgentDescendants(
  agents: RuntimeAgentRecord[],
  current: RuntimeAgentRecord | null,
): RuntimeAgentRecord[] {
  const basePath = current?.agentPath;
  if (!basePath) {
    return [];
  }
  const prefix = `${basePath}/`;
  return agents
    .filter((agent) => agent.agentPath !== null && agent.agentPath.startsWith(prefix))
    .sort((a, b) => {
      const depth = (a.depth ?? 0) - (b.depth ?? 0);
      if (depth !== 0) {
        return depth;
      }
      return (a.agentPath ?? "").localeCompare(b.agentPath ?? "");
    });
}

export function deriveSessionAgentTree(
  agents: RuntimeAgentRecord[],
  sessionId: string,
): SessionAgentTree {
  const currentAgent = pickCurrentAgent(agents, sessionId);
  return {
    currentAgent,
    lineage: buildAgentLineage(agents, currentAgent),
    descendants: listAgentDescendants(agents, currentAgent),
    hasIdentity: currentAgent !== null,
  };
}

export function useSessionAgents(options: UseSessionAgentsOptions = {}): UseSessionAgentsResult {
  const { sessionId = "", limit = 500 } = options;
  const trimmedSessionId = sessionId.trim();

  const [status, setStatus] = useState<AgentControlStatus>("idle");
  const [error, setError] = useState<unknown>(null);
  const [catalog, setCatalog] = useState<RuntimeAgentCatalog | null>(null);
  const [agents, setAgents] = useState<RuntimeAgentRecord[]>([]);
  const [reloadKey, setReloadKey] = useState(0);
  const [pendingAgentId, setPendingAgentId] = useState<string | null>(null);
  const [actionError, setActionError] = useState<unknown>(null);
  const [actionErrorAgentId, setActionErrorAgentId] = useState<string | null>(null);

  const mountedRef = useRef(true);
  const seqRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      seqRef.current += 1;
    };
  }, []);

  useEffect(() => {
    if (!trimmedSessionId) {
      return;
    }
    const controller = new AbortController();
    const seq = ++seqRef.current;
    setStatus("loading");
    setError(null);

    const load = async () => {
      const scoped = await listRuntimeAgents({
        sessionId: trimmedSessionId,
        includeClosed: true,
        limit: 20,
        signal: controller.signal,
      });
      const current = pickCurrentAgent(scoped.agents, trimmedSessionId);
      const rootSessionId = current?.rootSessionId ?? current?.sessionId ?? trimmedSessionId;
      const tree = await listRuntimeAgents({
        rootSessionId,
        includeClosed: true,
        limit,
        signal: controller.signal,
      });
      return tree;
    };

    void load()
      .then((tree) => {
        if (!mountedRef.current || seqRef.current !== seq) {
          return;
        }
        setAgents(tree.agents);
        setCatalog(tree);
        setStatus("ready");
      })
      .catch((loadError: unknown) => {
        if (!mountedRef.current || seqRef.current !== seq || isAbortError(loadError)) {
          return;
        }
        setAgents([]);
        setCatalog(null);
        setStatus(isAgentControlUnavailable(loadError) ? "unavailable" : "error");
        setError(loadError);
      });

    return () => {
      controller.abort();
    };
  }, [trimmedSessionId, limit, reloadKey]);

  const refresh = useCallback(() => {
    setActionError(null);
    setActionErrorAgentId(null);
    setReloadKey((value) => value + 1);
  }, []);

  const runMutation = useCallback(
    async (agentId: string, action: "close" | "resume"): Promise<boolean> => {
      const trimmedAgentId = agentId.trim();
      if (!trimmedAgentId || !trimmedSessionId) {
        return false;
      }
      setPendingAgentId(trimmedAgentId);
      setActionError(null);
      setActionErrorAgentId(null);
      try {
        const updated =
          action === "close"
            ? await closeRuntimeAgent(trimmedSessionId, trimmedAgentId)
            : await resumeRuntimeAgent(trimmedSessionId, trimmedAgentId);
        if (!mountedRef.current) {
          return false;
        }
        setAgents((current) =>
          current.map((agent) => (agent.agentId === updated.agentId ? updated : agent)),
        );
        return true;
      } catch (mutationError: unknown) {
        if (!mountedRef.current || isAbortError(mutationError)) {
          return false;
        }
        setActionError(mutationError);
        setActionErrorAgentId(trimmedAgentId);
        return false;
      } finally {
        if (mountedRef.current) {
          setPendingAgentId(null);
        }
      }
    },
    [trimmedSessionId],
  );

  const closeAgent = useCallback(
    (agentId: string) => runMutation(agentId, "close"),
    [runMutation],
  );
  const resumeAgent = useCallback(
    (agentId: string) => runMutation(agentId, "resume"),
    [runMutation],
  );

  const tree = useMemo(
    () => deriveSessionAgentTree(agents, trimmedSessionId),
    [agents, trimmedSessionId],
  );

  const truncated = catalog !== null && catalog.count > agents.length;

  return {
    status: trimmedSessionId ? status : "idle",
    error,
    catalog,
    truncated,
    tree,
    refresh,
    closeAgent,
    resumeAgent,
    pendingAgentId,
    actionError,
    actionErrorAgentId,
  };
}
