// P2-1A：Skills 客户端归一化与请求形状单测。
//
// 覆盖纪律（与 skills.ts 的注释一一对应）：
//   * 结构异常（缺 `skills` / `stats` 数组）必须抛错，不能降级成「空市场」；
//   * 策略 / 向量检索字段缺席即 null，UI 显示未知；
//   * 写操作状态码分类：403 = 策略拒绝，503 = 热重载未配置，两者不混淆。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  getHotReloadStats,
  getRuntimeSkillDetail,
  getRuntimeSkillsStats,
  isSkillsForbidden,
  isSkillsUnavailable,
  listRuntimeSkills,
  normalizeHotReloadStats,
  normalizeRuntimeSkill,
  normalizeSkillCatalog,
  normalizeSkillSearchResult,
  normalizeSkillStats,
  reloadHotReload,
  searchRuntimeSkills,
  startHotReload,
  stopHotReload,
  SKILLS_ADMIN_TOKEN_HEADER,
} from "@/api/runtime/skills";
import type { RuntimeApiError } from "@/api/runtime/shared";

const SKILL_PAYLOAD = {
  name: "code-review",
  description: "Review a diff",
  version: "1.2.0",
  category: "quality",
  capabilities: ["review"],
  tags: ["git", "review"],
  triggers: [
    { type: "keyword", values: ["review"], weight: 0.7 },
    { type: "manual" },
    { values: [] },
  ],
  tools: ["read_file"],
  systemPrompt: "be strict",
  userPrompt: "review this",
  workflow: {
    steps: [
      { id: "collect", tool: "read_file", dependsOn: ["scan"], condition: "" },
      { tool: "lint" },
    ],
  },
  context: {
    files: ["AGENTS.md"],
    environment: ["CI"],
    symbols: ["Reviewer"],
  },
  permissions: ["fs.read"],
  source: {
    path: "D:/skills/code-review/SKILL.md",
    dir: "D:/skills",
    layer: "project",
    prompt_path: "D:/skills/code-review/prompt.md",
  },
};

describe("skills 归一化", () => {
  it("技能条目保留后端字段，缺省集合为空（不臆造触发权重）", () => {
    const skill = normalizeRuntimeSkill(SKILL_PAYLOAD);

    expect(skill).toMatchObject({
      name: "code-review",
      version: "1.2.0",
      category: "quality",
      capabilities: ["review"],
      tools: ["read_file"],
      contextFiles: ["AGENTS.md"],
      contextEnvironment: ["CI"],
      contextSymbols: ["Reviewer"],
      permissions: ["fs.read"],
      source: {
        path: "D:/skills/code-review/SKILL.md",
        dir: "D:/skills",
        layer: "project",
        promptPath: "D:/skills/code-review/prompt.md",
      },
    });
    // 空值触发条件（type 与 values 都缺）被丢弃；无 weight 的触发条件保持 null。
    expect(skill?.triggers).toEqual([
      { type: "keyword", values: ["review"], weight: 0.7 },
      { type: "manual", values: [], weight: null },
    ]);
    // 缺 id 的工作流步骤保留（不用序号臆造 id），缺 dependsOn 回落空数组。
    expect(skill?.workflowSteps).toEqual([
      { id: "collect", name: "", tool: "read_file", args: {}, dependsOn: ["scan"], condition: "" },
      { id: "", name: "", tool: "lint", args: {}, dependsOn: [], condition: "" },
    ]);
  });

  it("缺 name 的技能不算条目", () => {
    expect(normalizeRuntimeSkill({ description: "no name" })).toBeNull();
    expect(normalizeRuntimeSkill(null)).toBeNull();
  });

  it("目录缺 skills 数组时抛错（不伪装空市场）", () => {
    expect(() => normalizeSkillCatalog({ count: 0 })).toThrow(/skills/);
  });

  it("目录保留后端上报的 count（与数组长度不等时以前者为准）", () => {
    const catalog = normalizeSkillCatalog({ skills: [SKILL_PAYLOAD], count: 9 });
    expect(catalog.skills).toHaveLength(1);
    expect(catalog.count).toBe(9);
  });

  it("检索结果保留 requested/resolved mode 与 used_embedding", () => {
    const view = normalizeSkillSearchResult({
      query: "review",
      results: [SKILL_PAYLOAD],
      matches: [{ skill: SKILL_PAYLOAD, score: 0.91, matched_by: "embedding", details: "hit" }],
      count: 1,
      limit: 20,
      requested_mode: "semantic",
      resolved_mode: "lexical",
      used_embedding: false,
    });

    expect(view.resolvedMode).toBe("lexical");
    expect(view.requestedMode).toBe("semantic");
    expect(view.usedEmbedding).toBe(false);
    expect(view.matches).toEqual([
      {
        skill: expect.objectContaining({ name: "code-review" }),
        score: 0.91,
        matchedBy: "embedding",
        details: "hit",
      },
    ]);
  });

  it("统计缺 stats 数组时抛错；policy / embedding 缺席即 null", () => {
    expect(() => normalizeSkillStats({ total_skills: 3 })).toThrow(/stats/);

    const view = normalizeSkillStats({
      stats: [{ name: "code-review", call_count: 4, success_rate: 0.75, avg_duration_ms: 1200 }],
      total_skills: 7,
      skill_dirs: ["D:/skills"],
      source_summary: { project: 2, "": 1, bad: "x" },
    });

    expect(view.rows).toEqual([
      {
        name: "code-review",
        category: "",
        callCount: 4,
        successRate: 0.75,
        avgDurationMs: 1200,
        sourceDir: "",
        sourcePath: "",
        sourceLayer: "",
      },
    ]);
    expect(view.totalSkills).toBe(7);
    expect(view.policy).toBeNull();
    expect(view.embeddingEnabled).toBeNull();
    // 非数值来源计数被丢弃，空字符串来源键保留（后端确实上报了该键）。
    expect(view.sourceSummary).toEqual({ project: 2, "": 1 });
  });

  it("统计保留 mutation_policy 三态（缺席即 null，不当作 false）", () => {
    const view = normalizeSkillStats({
      stats: [],
      mutation_policy: { read_only: true, disable_hot_reload: false, disable_import: null },
    });

    expect(view.policy).toEqual({
      readOnly: true,
      disableImport: null,
      disablePersist: null,
      disableReloadOps: null,
      disableHotReload: false,
    });
  });

  it("热重载统计缺 stats 即抛错，只有 skillDir 时回落为单元素列表", () => {
    expect(() => normalizeHotReloadStats({})).toThrow(/stats/);

    const view = normalizeHotReloadStats({
      stats: {
        enabled: true,
        watching: true,
        skillDir: "D:/skills",
        skillCount: 12,
        callbackCount: 2,
        debounceTime: "500ms",
        extraFlag: true,
      },
    });

    expect(view.skillDirs).toEqual(["D:/skills"]);
    expect(view.skillCount).toBe(12);
    expect(view.debounceTime).toBe("500ms");
    expect(view.raw.extraFlag).toBe(true);
  });
});

describe("skills API 调用", () => {
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

  function adminHeader(init?: RequestInit) {
    return new Headers(init?.headers).get(SKILLS_ADMIN_TOKEN_HEADER);
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("目录请求带来源过滤，详情路径做 URL 编码", async () => {
    respondWith({ skills: [SKILL_PAYLOAD], count: 1 });
    await listRuntimeSkills({ layer: "project", dir: "D:/skills" });

    expect(calls[0].url).toContain("/api/runtime/skills?");
    expect(calls[0].url).toContain("source_layer=project");
    expect(calls[0].url).toContain("source_dir=D%3A%2Fskills");

    respondWith(SKILL_PAYLOAD);
    await getRuntimeSkillDetail("team/review skill");

    expect(calls[1].url).toContain("/api/runtime/skills/team%2Freview%20skill");
    expect(calls[1].init?.method ?? "GET").toBe("GET");
  });

  it("检索 limit 越界时收敛到上限 200", async () => {
    respondWith({ results: [], matches: [], query: "x" });
    await searchRuntimeSkills({ query: "x", limit: 999 });

    expect(calls[0].url).toContain("limit=200");
    expect(calls[0].url).toContain("q=x");
  });

  it("热重载写操作带管理令牌与 JSON body", async () => {
    respondWith({ stats: { enabled: true, watching: true, skillDirs: ["D:/skills"] } });

    await startHotReload([" D:/skills ", "", "E:/shared"], {
      adminToken: " secret ",
      debounceMs: 750,
    });

    expect(calls[0].url).toContain("/api/runtime/skills/hot-reload/start");
    expect(calls[0].init?.method).toBe("POST");
    expect(adminHeader(calls[0].init)).toBe("secret");
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      dirs: ["D:/skills", "E:/shared"],
      debounce_ms: 750,
    });
  });

  it("空目录清单在本地即拒绝（不发请求）", async () => {
    respondWith({ stats: {} });
    await expect(startHotReload(["  "])).rejects.toThrow(/directory/);
    expect(calls).toHaveLength(0);
  });

  it("无令牌时不发送鉴权头，stop / reload 走各自端点", async () => {
    respondWith({ stats: { enabled: true, watching: false } });

    await stopHotReload();
    await reloadHotReload();

    expect(adminHeader(calls[0].init)).toBeNull();
    expect(calls[0].url).toContain("/hot-reload/stop");
    expect(calls[1].url).toContain("/hot-reload/reload");
    expect(calls[1].init?.body).toBeUndefined();
  });

  it("403 归类为策略拒绝，503 归类为不可用", async () => {
    respondWith({ error: "admin token required" }, 403);
    const forbidden = await getHotReloadStats().catch((error: RuntimeApiError) => error);

    expect(isSkillsForbidden(forbidden)).toBe(true);
    expect(isSkillsUnavailable(forbidden)).toBe(false);

    respondWith({ error: "hot reload not configured" }, 503);
    const unavailable = await getRuntimeSkillsStats().catch((error: RuntimeApiError) => error);

    expect(isSkillsUnavailable(unavailable)).toBe(true);
    expect(isSkillsForbidden(unavailable)).toBe(false);
  });
});
