import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  getUsageLedger,
  getUsagePolicy,
  getUsageStats,
  normalizeUsageLedger,
  normalizeUsagePolicy,
  normalizeUsageStats,
} from "@/api/runtime/usage";

const SCOPED_STATS = {
  tracking_enabled: true,
  policy: {
    tracking_enabled: true,
    ledger_enabled: true,
    quota_enabled: true,
    default_max_requests: 100,
    default_max_tokens: 50_000,
    tenant_quota_count: 1,
    project_quota_count: 0,
    user_quota_count: 2,
  },
  scope: {
    tenant_id: "tenant-a",
    project_id: "project-a",
    user_id: "alice",
    scope_key: "tenant-a/project-a/alice",
  },
  quota: {
    scope_key: "tenant-a/project-a/alice",
    enabled: true,
    max_requests: 100,
    max_tokens: 50_000,
    remaining_requests: 93,
    remaining_tokens: 41_500,
    resolved_from: "user",
  },
  usage: {
    tenant_id: "tenant-a",
    project_id: "project-a",
    user_id: "alice",
    scope_key: "tenant-a/project-a/alice",
    request_count: 7,
    execute_count: 5,
    agent_chat_count: 2,
    success_count: 6,
    failure_count: 1,
    prompt_tokens: 6_000,
    completion_tokens: 2_500,
    total_tokens: 8_500,
    last_skill: "ledger-skill",
    last_entrypoint: "execute",
    last_request_at: "2026-09-13T10:00:00Z",
  },
};

const GLOBAL_STATS = {
  tracking_enabled: true,
  policy: SCOPED_STATS.policy,
  usage: {
    scope_count: 2,
    user_count: 2,
    request_count: 9,
    execute_count: 6,
    agent_chat_count: 3,
    success_count: 8,
    failure_count: 1,
    prompt_tokens: 7_000,
    completion_tokens: 3_000,
    total_tokens: 10_000,
  },
  scopes: [
    { tenant_id: "tenant-a", project_id: "", user_id: "", scope_key: "tenant-a" },
    { tenant_id: "", project_id: "", user_id: "bob", scope_key: "bob" },
  ],
};

const POLICY = {
  policy: {
    tracking_enabled: true,
    ledger_enabled: true,
    quota_enabled: true,
    default_max_requests: 100,
    default_max_tokens: 50_000,
    tenants: { "tenant-a": { max_requests: 500 } },
    projects: {},
    users: {
      "tenant-a/project-a/alice": { max_requests: 100, max_tokens: 50_000 },
    },
  },
};

const LEDGER = {
  records: [
    {
      id: "rec-2",
      request_id: "req-2",
      model_id: "deepseek-chat",
      provider_id: "deepseek",
      input_tokens: 900,
      output_tokens: 300,
      total_tokens: 1_200,
      message_count: 3,
      max_tokens: 50_000,
      success: true,
      status_code: 200,
      metadata: {
        subsystem: "skill_runtime",
        scope_key: "tenant-a/project-a/alice",
        entrypoint: "execute",
        skill: "ledger-skill",
      },
      created_at: "2026-09-13T10:00:00Z",
    },
    {
      id: "rec-1",
      request_id: "req-1",
      model_id: "deepseek-chat",
      provider_id: "deepseek",
      input_tokens: 100,
      output_tokens: 0,
      total_tokens: 100,
      message_count: 1,
      max_tokens: 0,
      success: false,
      status_code: 500,
      metadata: { entrypoint: "agent_chat" },
      created_at: "2026-09-13T09:00:00Z",
    },
  ],
  count: 2,
  filters: { scope: null, entrypoint: "", skill: "", success: null, since: "0001-01-01T00:00:00Z", limit: 20 },
};

describe("normalizeUsagePolicy", () => {
  it("把各层配额 map 展开为按 key 排序的条目", () => {
    const policy = normalizeUsagePolicy(POLICY);

    expect(policy).toMatchObject({
      tracking_enabled: true,
      ledger_enabled: true,
      quota_enabled: true,
      default_max_requests: 100,
      default_max_tokens: 50_000,
    });
    expect(policy?.tenants).toEqual([
      { level: "tenant", key: "tenant-a", max_requests: 500, max_tokens: null },
    ]);
    expect(policy?.projects).toEqual([]);
    expect(policy?.users).toEqual([
      {
        level: "user",
        key: "tenant-a/project-a/alice",
        max_requests: 100,
        max_tokens: 50_000,
      },
    ]);
  });

  it("缺 policy 包装或类型非法时返回 null（不伪造默认策略）", () => {
    expect(normalizeUsagePolicy(null)).toBeNull();
    expect(normalizeUsagePolicy({})).toBeNull();
    expect(normalizeUsagePolicy({ policy: [] })).toBeNull();
  });
});

describe("normalizeUsageStats", () => {
  it("scoped 响应保留 scope / quota / usage 计数", () => {
    const view = normalizeUsageStats(SCOPED_STATS);

    expect(view?.scope?.scope_key).toBe("tenant-a/project-a/alice");
    expect(view?.quota).toMatchObject({
      enabled: true,
      max_requests: 100,
      remaining_tokens: 41_500,
      resolved_from: "user",
    });
    expect(view?.usage).toMatchObject({
      request_count: 7,
      total_tokens: 8_500,
      last_skill: "ledger-skill",
      last_entrypoint: "execute",
    });
    expect(view?.scopes).toEqual([]);
  });

  it("全局响应 scope/quota 为 null，scopes 列表逐条归一化", () => {
    const view = normalizeUsageStats(GLOBAL_STATS);

    expect(view?.scope).toBeNull();
    expect(view?.quota).toBeNull();
    expect(view?.usage.scope_count).toBe(2);
    expect(view?.usage.total_tokens).toBe(10_000);
    expect(view?.scopes.map((scope) => scope.scope_key)).toEqual([
      "tenant-a",
      "bob",
    ]);
  });

  it("缺 usage / policy 或 scope 形状非法时返回 null", () => {
    expect(normalizeUsageStats({ policy: SCOPED_STATS.policy })).toBeNull();
    expect(normalizeUsageStats({ usage: SCOPED_STATS.usage })).toBeNull();
    expect(
      normalizeUsageStats({ ...SCOPED_STATS, scope: { tenant_id: "t" } }),
    ).toBeNull();
    expect(normalizeUsageStats("nope")).toBeNull();
  });

  it("scoped 响应缺 quota 时保留 null（降级呈现，不影响 token 用量）", () => {
    const view = normalizeUsageStats({
      ...SCOPED_STATS,
      quota: null,
    });

    expect(view?.scope?.scope_key).toBe("tenant-a/project-a/alice");
    expect(view?.quota).toBeNull();
    expect(view?.usage.total_tokens).toBe(8_500);
  });
});

describe("normalizeUsageLedger", () => {
  it("逐条归一化并回显后端 limit", () => {
    const view = normalizeUsageLedger(LEDGER);

    expect(view?.count).toBe(2);
    expect(view?.limit).toBe(20);
    expect(view?.records[0]).toMatchObject({
      id: "rec-2",
      total_tokens: 1_200,
      success: true,
      metadata: { skill: "ledger-skill", entrypoint: "execute" },
    });
    expect(view?.records[1].success).toBe(false);
  });

  it("丢缺少 id 的行（不臆造主键），缺 filters 时回落请求 limit", () => {
    const view = normalizeUsageLedger(
      { records: [{ request_id: "req-x" }, LEDGER.records[0]] },
      30,
    );

    expect(view?.records.map((record) => record.id)).toEqual(["rec-2"]);
    expect(view?.limit).toBe(30);
  });

  it("缺 records 数组时返回 null（不伪造空账本）", () => {
    expect(normalizeUsageLedger({ records: null })).toBeNull();
    expect(normalizeUsageLedger({})).toBeNull();
  });

  it("未请求分组（响应无 groups）时 profileGroups / groupedTotal 如实为 null", () => {
    const view = normalizeUsageLedger(LEDGER);

    expect(view?.profileGroups).toBeNull();
    expect(view?.groupedTotal).toBeNull();
  });

  it("解析 group_by=profile 的 groups 与 grouped_total（空 profile 是「未归属」组身份）", () => {
    const view = normalizeUsageLedger({
      ...LEDGER,
      group_by: "profile",
      groups: [
        {
          profile: "reviewer",
          requests: 2,
          failures: 1,
          input_tokens: 900,
          output_tokens: 300,
          total_tokens: 1_200,
        },
        {
          profile: "",
          requests: 1,
          failures: 0,
          input_tokens: 100,
          output_tokens: 0,
          total_tokens: 100,
        },
        { requests: 5 },
      ],
      grouped_total: 3,
    });

    expect(view?.profileGroups).toEqual([
      {
        profile: "reviewer",
        requests: 2,
        failures: 1,
        input_tokens: 900,
        output_tokens: 300,
        total_tokens: 1_200,
      },
      {
        profile: "",
        requests: 1,
        failures: 0,
        input_tokens: 100,
        output_tokens: 0,
        total_tokens: 100,
      },
    ]);
    expect(view?.groupedTotal).toBe(3);
  });

  it("groups 非数组 / grouped_total 非法时如实置 null（不推算）", () => {
    const notArray = normalizeUsageLedger({ ...LEDGER, groups: "nope", grouped_total: 3 });
    expect(notArray?.profileGroups).toBeNull();
    expect(notArray?.groupedTotal).toBeNull();

    const empty = normalizeUsageLedger({ ...LEDGER, groups: [], grouped_total: -1 });
    expect(empty?.profileGroups).toEqual([]);
    expect(empty?.groupedTotal).toBeNull();
  });
});

describe("usage API 调用", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  }

  function authHeader(init?: RequestInit) {
    return new Headers(init?.headers).get("Authorization");
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("getUsageStats 带 scope 查询与 Bearer token", async () => {
    respondWith(SCOPED_STATS);

    const view = await getUsageStats({
      tenantId: "tenant-a",
      projectId: "project-a",
      userId: "alice",
      adminToken: " admin-secret ",
    });

    const [call] = calls;
    expect(call.url).toContain("/api/runtime/usage/stats?");
    expect(call.url).toContain("tenant_id=tenant-a");
    expect(call.url).toContain("user_id=alice");
    expect(authHeader(call.init)).toBe("Bearer admin-secret");
    expect(view.usage.request_count).toBe(7);
  });

  it("getUsageStats 不传 scope 时请求全局聚合", async () => {
    respondWith(GLOBAL_STATS);

    const view = await getUsageStats();

    expect(calls[0].url).toContain("/api/runtime/usage/stats");
    expect(calls[0].url).not.toContain("?");
    expect(view.scope).toBeNull();
    expect(view.scopes).toHaveLength(2);
  });

  it("getUsageStats 结构不满足时抛错（不返回伪造空态）", async () => {
    respondWith({});

    await expect(getUsageStats()).rejects.toThrow(/invalid usage stats payload/);
  });

  it("getUsageStats 非 2xx（403）保留 status 供权限提示", async () => {
    respondWith({ error: "forbidden" }, 403);

    await expect(getUsageStats()).rejects.toMatchObject({ status: 403 });
  });

  it("getUsageLedger 传 entrypoint/skill/success/limit 并归一化", async () => {
    respondWith(LEDGER);

    const view = await getUsageLedger({
      entrypoint: "execute",
      skill: "ledger-skill",
      success: false,
      limit: 20,
      adminToken: "admin-secret",
    });

    const [call] = calls;
    expect(call.url).toContain("entrypoint=execute");
    expect(call.url).toContain("skill=ledger-skill");
    expect(call.url).toContain("success=false");
    expect(call.url).toContain("limit=20");
    expect(view.records).toHaveLength(2);
    expect(view.limit).toBe(20);
  });

  it("getUsageLedger 缺 records 时抛错", async () => {
    respondWith({ count: 0 });

    await expect(getUsageLedger()).rejects.toThrow(/invalid usage ledger payload/);
  });

  it("getUsageLedger 传 group_by=profile 并解析分组（聚合基于截断前集合）", async () => {
    respondWith({
      ...LEDGER,
      group_by: "profile",
      groups: [
        {
          profile: "reviewer",
          requests: 2,
          failures: 0,
          input_tokens: 1_800,
          output_tokens: 600,
          total_tokens: 2_400,
        },
      ],
      grouped_total: 2,
    });

    const view = await getUsageLedger({ groupBy: "profile", limit: 20 });

    expect(calls[0].url).toContain("group_by=profile");
    expect(view.profileGroups).toHaveLength(1);
    expect(view.profileGroups?.[0].profile).toBe("reviewer");
    expect(view.groupedTotal).toBe(2);
  });

  it("未传 groupBy 时不发 group_by（旧版响应兼容）", async () => {
    respondWith(LEDGER);

    const view = await getUsageLedger();

    expect(calls[0].url).not.toContain("group_by");
    expect(view.profileGroups).toBeNull();
  });

  it("getUsagePolicy 解包 policy 并带 token", async () => {
    respondWith(POLICY);

    const policy = await getUsagePolicy({ adminToken: "admin-secret" });

    expect(calls[0].url).toContain("/api/runtime/usage/policy");
    expect(authHeader(calls[0].init)).toBe("Bearer admin-secret");
    expect(policy.users[0].key).toBe("tenant-a/project-a/alice");
  });

  it("getUsagePolicy 结构不满足时抛错", async () => {
    respondWith({ policy: null });

    await expect(getUsagePolicy()).rejects.toThrow(/invalid usage policy payload/);
  });
});
