// P2-1A：子代理控制面客户端单测（URL/请求口径 / 载荷归一化 / 降级判据）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  AGENT_CONTROL_AGENTS_PATH,
  AGENT_LIMIT_MAX,
  DEFAULT_AGENT_LIMIT,
  agentControlCommandPath,
  closeRuntimeAgent,
  isAgentControlUnavailable,
  listRuntimeAgents,
  normalizeAgentCatalog,
  normalizeAgentMutation,
  normalizeRuntimeAgent,
  normalizeRuntimeAgentStatus,
  resolveAgentLimit,
  resumeRuntimeAgent,
} from "@/api/runtime/agents";
import { RuntimeApiError } from "@/api/runtime/shared";

const fullRecord = {
  agent_id: "child-1",
  root_session_id: "sess-root",
  parent_agent_id: "root",
  parent_session_id: "sess-root",
  session_id: "sess-child-1",
  agent_path: "/root/child-1",
  depth: 1,
  agent_type: "child",
  nickname: "researcher",
  workflow: "spawn_agent",
  team_id: "team-a",
  teammate_id: "member-1",
  provider: "anthropic",
  model: "fallback-model",
  effective_provider: "deepseek",
  effective_model: "deepseek-chat",
  difficulty: "hard",
  status: "active",
  created_at: "2026-09-14T01:00:00Z",
  updated_at: "2026-09-14T01:05:00Z",
  closed_at: "2026-09-14T01:06:00Z",
  route_warnings: [" provider fallback ", "", "model downgraded"],
};

describe("normalizeRuntimeAgentStatus", () => {
  it("只承认 active / stale / closed，其余一律 unknown", () => {
    expect(normalizeRuntimeAgentStatus("active")).toBe("active");
    expect(normalizeRuntimeAgentStatus(" STALE ")).toBe("stale");
    expect(normalizeRuntimeAgentStatus("closed")).toBe("closed");
    expect(normalizeRuntimeAgentStatus("running")).toBe("unknown");
    expect(normalizeRuntimeAgentStatus("")).toBe("unknown");
    expect(normalizeRuntimeAgentStatus(undefined)).toBe("unknown");
  });
});

describe("normalizeRuntimeAgent", () => {
  it("映射字段并优先取 effective provider / model", () => {
    const agent = normalizeRuntimeAgent(fullRecord);
    expect(agent).not.toBeNull();
    expect(agent).toMatchObject({
      agentId: "child-1",
      rootSessionId: "sess-root",
      parentAgentId: "root",
      parentSessionId: "sess-root",
      sessionId: "sess-child-1",
      agentPath: "/root/child-1",
      depth: 1,
      nickname: "researcher",
      provider: "deepseek",
      model: "deepseek-chat",
      status: "active",
      closedAt: "2026-09-14T01:06:00Z",
    });
    expect(agent?.routeWarnings).toEqual(["provider fallback", "model downgraded"]);
  });

  it("缺 agent_id 时丢弃该条；非对象同样丢弃", () => {
    expect(normalizeRuntimeAgent({ nickname: "no-id" })).toBeNull();
    expect(normalizeRuntimeAgent("child-1")).toBeNull();
    expect(normalizeRuntimeAgent(null)).toBeNull();
  });

  it("状态缺失收口为 unknown，不回退 provider 原始字段", () => {
    const agent = normalizeRuntimeAgent({ agent_id: "a1", provider: "openai" });
    expect(agent?.status).toBe("unknown");
    expect(agent?.provider).toBe("openai");
    expect(agent?.model).toBeNull();
    expect(agent?.depth).toBeNull();
  });
});

describe("normalizeAgentCatalog", () => {
  it("保留后端上报的 count（可大于数组长度），坏行只丢单条", () => {
    const catalog = normalizeAgentCatalog(
      {
        agents: [fullRecord, { nickname: "broken" }],
        count: 42,
        source: "agent_control_agents",
      },
      { limit: DEFAULT_AGENT_LIMIT, includeClosed: false },
    );

    expect(catalog.agents).toHaveLength(1);
    expect(catalog.count).toBe(42);
    expect(catalog.source).toBe("agent_control_agents");
    expect(catalog.limit).toBe(DEFAULT_AGENT_LIMIT);
    expect(catalog.includeClosed).toBe(false);
  });

  it("count 缺失 / 非法时按数组长度收口", () => {
    const catalog = normalizeAgentCatalog(
      { agents: [fullRecord], count: -3 },
      { limit: 10, includeClosed: true },
    );
    expect(catalog.count).toBe(1);
    expect(catalog.includeClosed).toBe(true);
  });

  it("agents 非数组直接抛错，不伪装成空目录", () => {
    expect(() =>
      normalizeAgentCatalog({ count: 0 }, { limit: 10, includeClosed: false }),
    ).toThrow(/missing `agents`/);
    expect(() =>
      normalizeAgentCatalog({ agents: {} }, { limit: 10, includeClosed: false }),
    ).toThrow(/missing `agents`/);
    expect(() =>
      normalizeAgentCatalog(null, { limit: 10, includeClosed: false }),
    ).toThrow(/not an object/);
  });
});

describe("resolveAgentLimit", () => {
  it("默认 200、下取整、上限 500，非法值回退默认", () => {
    expect(resolveAgentLimit(undefined)).toBe(DEFAULT_AGENT_LIMIT);
    expect(resolveAgentLimit(0)).toBe(DEFAULT_AGENT_LIMIT);
    expect(resolveAgentLimit(Number.NaN)).toBe(DEFAULT_AGENT_LIMIT);
    expect(resolveAgentLimit(12.9)).toBe(12);
    expect(resolveAgentLimit(9999)).toBe(AGENT_LIMIT_MAX);
  });
});

describe("agentControlCommandPath", () => {
  it("对 session / agent id 做 URL 编码", () => {
    expect(agentControlCommandPath("sess 1", "/root/child-1", "close")).toBe(
      "/api/runtime/sessions/sess%201/agents/%2Froot%2Fchild-1/close",
    );
    expect(agentControlCommandPath("sess-1", "child-1", "resume")).toBe(
      "/api/runtime/sessions/sess-1/agents/child-1/resume",
    );
  });
});

describe("listRuntimeAgents", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    });
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("按过滤条件拼参数，空过滤不发（limit 由前端兜底）", async () => {
    respondWith({ agents: [fullRecord], count: 1, source: "projection" });

    const catalog = await listRuntimeAgents({
      rootSessionId: " sess-root ",
      pathPrefix: "/root/",
      includeClosed: true,
    });

    expect(catalog.count).toBe(1);
    const url = calls[0]?.url ?? "";
    expect(url).toContain(`${AGENT_CONTROL_AGENTS_PATH}?`);
    const query = new URLSearchParams(url.slice(url.indexOf("?") + 1));
    expect(query.get("root_session_id")).toBe("sess-root");
    expect(query.get("path_prefix")).toBe("/root/");
    expect(query.get("include_closed")).toBe("true");
    expect(query.get("limit")).toBe(String(DEFAULT_AGENT_LIMIT));
    expect(query.get("session_id")).toBeNull();
    expect(calls[0]?.init?.method).toBeUndefined();
  });

  it("只按 session 过滤时不带 include_closed", async () => {
    respondWith({ agents: [], count: 0 });

    await listRuntimeAgents({ sessionId: "sess-child-1", limit: 5 });

    const url = calls[0]?.url ?? "";
    const query = new URLSearchParams(url.slice(url.indexOf("?") + 1));
    expect(query.get("session_id")).toBe("sess-child-1");
    expect(query.get("limit")).toBe("5");
    expect(query.get("include_closed")).toBeNull();
  });

  it("HTTP 503 抛 RuntimeApiError（供降级分类）", async () => {
    respondWith({ error: "agent session controller not configured" }, 503);

    await expect(listRuntimeAgents()).rejects.toBeInstanceOf(RuntimeApiError);
  });
});

describe("closeRuntimeAgent / resumeRuntimeAgent", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    });
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("close 走 POST 并回传单条身份", async () => {
    respondWith({ agent: { ...fullRecord, status: "closed" } });

    const agent = await closeRuntimeAgent("sess-root", "child-1");

    expect(agent.status).toBe("closed");
    expect(calls[0]?.init?.method).toBe("POST");
    expect(calls[0]?.url).toContain("/api/runtime/sessions/sess-root/agents/child-1/close");
  });

  it("resume 走 POST；agent 结构缺失直接抛错", async () => {
    respondWith({ agent: { ...fullRecord, status: "active" } });
    await expect(resumeRuntimeAgent("sess-root", "child-1")).resolves.toMatchObject({
      agentId: "child-1",
      status: "active",
    });
    expect(calls[0]?.url).toContain("/resume");

    respondWith({});
    await expect(resumeRuntimeAgent("sess-root", "child-1")).rejects.toThrow(
      /missing `agent`/,
    );
  });

  it("空 id 不发请求直接抛错", async () => {
    respondWith({ agent: fullRecord });
    await expect(closeRuntimeAgent("  ", "child-1")).rejects.toThrow(/session id is required/);
    await expect(closeRuntimeAgent("sess-root", " ")).rejects.toThrow(/agent id is required/);
    expect(calls).toHaveLength(0);
  });

  it("normalizeAgentMutation 拒绝空载荷", () => {
    expect(() => normalizeAgentMutation(null)).toThrow(/missing `agent`/);
    expect(() => normalizeAgentMutation({ agent: { nickname: "no-id" } })).toThrow(
      /missing `agent`/,
    );
  });
});

describe("isAgentControlUnavailable", () => {
  it("404 / 405 / 501 / 503 视为不可用，500 按真实失败", () => {
    expect(isAgentControlUnavailable(new RuntimeApiError(404, null))).toBe(true);
    expect(isAgentControlUnavailable(new RuntimeApiError(503, { error: "no controller" }))).toBe(
      true,
    );
    expect(isAgentControlUnavailable(new RuntimeApiError(500, { error: "boom" }))).toBe(false);
    expect(isAgentControlUnavailable(new Error("boom"))).toBe(false);
  });
});
