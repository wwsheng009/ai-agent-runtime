// Profiles 客户端单测：请求形状（URL / method / body）+ 归一化 + 错误信封分类。
//
// 覆盖纪律（与 api/runtime/profiles.ts 顶部注释一致）：
//   * ref 必须整段 URL 编码（允许 `:` `/` 等分隔符）；
//   * 契约字段缺失（profiles 非数组、ref 为空、references 非对象）必须抛错，
//     不能降级成「空列表」；
//   * 非 2xx 必须抛 RuntimeApiError 并保留后端 code（501 由 UI 降级提示）；
//   * 删除被引用阻断 = 409 + 错误体里的 `references`，必须能回填强制删除对话框。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  applyRuntimeProfile,
  buildRuntimeProfileApplyUrl,
  buildRuntimeProfileUrl,
  createRuntimeProfile,
  deleteRuntimeProfile,
  duplicateRuntimeProfile,
  getRuntimeProfile,
  listRuntimeProfileReferences,
  listRuntimeProfiles,
  moveRuntimeProfile,
  previewRuntimeProfile,
  readProfileDeleteBlockingReferences,
  renameRuntimeProfile,
  setDefaultRuntimeProfile,
  updateRuntimeProfile,
  validateRuntimeProfile,
} from "@/api/runtime/profiles";
import { isRuntimeApiErrorCode, readErrorEnvelope, RuntimeApiError } from "@/api/runtime/shared";
import { CREATE_PAYLOAD, ENCODED_REF, REF, VIEW_PAYLOAD } from "./profiles.test-fixtures";


function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function mockFetch(
  handler: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>,
) {
  const fetchMock = vi.fn(handler);
  globalThis.fetch = fetchMock as unknown as typeof fetch;
  return fetchMock;
}

function readBody(init?: RequestInit) {
  return JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
}

describe("runtime profiles 客户端", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    globalThis.fetch = originalFetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it("ref 含分隔符时整段编码，各子端点复用同一句柄", () => {
    expect(buildRuntimeProfileUrl(REF)).toBe(`/api/runtime/profiles/${ENCODED_REF}`);
    expect(buildRuntimeProfileApplyUrl(REF)).toBe(
      `/api/runtime/profiles/${ENCODED_REF}/apply`,
    );
  });

  it("列表归一化：snake_case → camelCase；valid/writable 缺省即合法可写", async () => {
    mockFetch(async () =>
      jsonResponse({
        profiles: [
          {
            ref: REF,
            name: "reviewer",
            description: "code review",
            layer: "user",
            path: "profiles/reviewer.yaml",
            is_default: true,
            default_agent: "reviewer",
          },
          { ref: "project:dev", valid: false, error: "yaml parse failed", writable: false },
        ],
        count: 2,
        default_profile: REF,
        default_root: "/home/u/.aicli",
      }),
    );

    const result = await listRuntimeProfiles();

    expect(result.count).toBe(2);
    expect(result.defaultProfile).toBe(REF);
    expect(result.defaultRoot).toBe("/home/u/.aicli");
    expect(result.profiles[0]).toMatchObject({
      ref: REF,
      name: "reviewer",
      defaultAgent: "reviewer",
      isDefault: true,
      valid: true,
      writable: true,
      error: "",
    });
    expect(result.profiles[1]).toMatchObject({
      ref: "project:dev",
      // 名称缺失时用 ref 兜底，避免列表出现空标题。
      name: "project:dev",
      valid: false,
      writable: false,
      error: "yaml parse failed",
      isDefault: false,
    });
  });

  it("列表契约回归：profiles 缺失或条目缺 ref 时抛错，不降级成空列表", async () => {
    mockFetch(async () => jsonResponse({ count: 0 }));
    await expect(listRuntimeProfiles()).rejects.toThrow(/profiles must be an array/);

    mockFetch(async () => jsonResponse({ profiles: [{ name: "no-ref" }] }));
    await expect(listRuntimeProfiles()).rejects.toThrow(/profiles\[0\]\.ref/);
  });

  it("view 归一化：只读面按 view 分组读取，写回基线取 spec", async () => {
    mockFetch(async () => jsonResponse(VIEW_PAYLOAD));

    const view = await getRuntimeProfile(REF);

    expect(view.tools.allowlist).toEqual(["read_file", "grep"]);
    expect(view.tools.effective).toEqual(["read_file", "grep"]);
    expect(view.tools.sources).toEqual(["profile", "global"]);
    expect(view.skills).toMatchObject({ exposureMode: "top_k", topK: 5, dirs: ["skills"] });
    expect(view.mcp).toMatchObject({
      useServers: ["chrome"],
      excludeServers: ["legacy"],
      serverCount: 2,
      usedCount: 1,
    });
    expect(view.mcp.servers).toEqual([
      { name: "chrome", used: true, excluded: false },
      { name: "legacy", used: false, excluded: true },
    ]);
    expect(view.prompts.mode).toBe("append");
    expect(view.prompts.system).toEqual({ path: "prompts/system.md", exists: true });
    expect(view.prompts.role).toEqual({ path: "", exists: false });
    expect(view.agents.defaultAgent).toBe("reviewer");
    expect(view.agents.entries).toEqual([
      {
        id: "reviewer",
        provider: "anthropic",
        model: "claude",
        tools: { allowlist: ["read_file"], denylist: [] },
      },
    ]);
    expect(view.overrides).toMatchObject({ keys: ["retry.max_attempts"], count: 1 });
    expect(view.overrides.entries[0]).toMatchObject({
      key: "retry.max_attempts",
      value: "3",
      allowed: true,
    });
    expect(view.preferences).toMatchObject({
      permissionMode: "plan",
      permissionModeSource: "profile",
    });
    expect(view.estimate.totalTokens).toBe(2000);
    expect(view.document).toEqual({
      profile: { name: "reviewer" },
      tools: { allowlist: ["read_file"] },
    });
    expect(view.mtime).toBe("2026-09-24T10:00:00Z");
    expect(view.preview).toBe(true);
  });

  it("view 契约回归：ref 缺失时抛错", async () => {
    mockFetch(async () => jsonResponse({ name: "reviewer" }));
    await expect(getRuntimeProfile(REF)).rejects.toThrow(/ref must be a non-empty string/);
  });

  it("创建/复制/改名/迁移/默认：请求体使用后端 snake_case 字段", async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    mockFetch(async (input, init) => {
      const url = String(input);
      calls.push({ url, init });
      return jsonResponse(url === "/api/runtime/profiles" ? CREATE_PAYLOAD : {});
    });

    const created = await createRuntimeProfile({
      name: "new",
      layer: "user",
      template: "minimal",
    });
    await duplicateRuntimeProfile(REF, { name: "copy", layer: "project" });
    await renameRuntimeProfile(REF, { name: "renamed" });
    await moveRuntimeProfile(REF, { layer: "project" });
    await setDefaultRuntimeProfile(REF);

    expect(calls[0].url).toBe("/api/runtime/profiles");
    expect(calls[0].init?.method).toBe("POST");
    // 后端 profileCreateRequest 没有 description 字段：写进去只会被忽略。
    expect(readBody(calls[0].init)).toEqual({
      name: "new",
      layer: "user",
      root: "",
      template: "minimal",
      from_ref: "",
      // D24/D36：会话固化（save-as）与创建/复制共用同一端点，字段常驻、空串 = 未指定。
      from_session: "",
      agent: "",
      force: false,
      use: false,
      set_default: false,
    });
    expect(created).toMatchObject({
      ref: "user:new",
      name: "new",
      layer: "user",
      registered: true,
      defaultProfileSet: false,
      affects: "new_sessions_only",
      configPath: "/home/u/.aicli/config.yaml",
    });
    expect(created.files).toEqual(["profile.yaml", "prompts/system.md"]);

    expect(calls[1].url).toBe(`/api/runtime/profiles/${ENCODED_REF}/duplicate`);
    expect(readBody(calls[1].init)).toEqual({
      name: "copy",
      layer: "project",
      root: "",
      template: "",
      from_ref: REF,
      from_session: "",
      agent: "",
      force: false,
      use: false,
      set_default: false,
    });

    expect(calls[2].url).toBe(`/api/runtime/profiles/${ENCODED_REF}/rename`);
    expect(readBody(calls[2].init)).toEqual({ name: "renamed" });

    expect(calls[3].url).toBe(`/api/runtime/profiles/${ENCODED_REF}/move`);
    expect(readBody(calls[3].init)).toEqual({ layer: "project", root: "" });

    expect(calls[4].url).toBe(`/api/runtime/profiles/${ENCODED_REF}/default`);
    expect(calls[4].init?.method).toBe("POST");
    expect(readBody(calls[4].init)).toEqual({ register: true });
  });

  it("写回：PUT body 用 spec + expected_mtime，返回归一化 view 与 mtime", async () => {
    const fetchMock = mockFetch(async () =>
      jsonResponse({
        profile: VIEW_PAYLOAD,
        mtime: "2026-09-24T11:00:00Z",
        warning_count: 1,
        issues: [],
        name_mismatch: false,
        hint: "",
      }),
    );

    const result = await updateRuntimeProfile(REF, {
      document: { profile: { name: "reviewer" }, tools: { allowlist: ["read_file"] } },
      expectedMtime: "2026-09-24T10:00:00Z",
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`/api/runtime/profiles/${ENCODED_REF}`);
    expect(init.method).toBe("PUT");
    expect(readBody(init)).toEqual({
      spec: { profile: { name: "reviewer" }, tools: { allowlist: ["read_file"] } },
      expected_mtime: "2026-09-24T10:00:00Z",
    });
    expect(result.view.ref).toBe(REF);
    expect(result.mtime).toBe("2026-09-24T11:00:00Z");

    // 契约回归：写回响应没有 profile 视图时抛错，不能返回半截结果。
    mockFetch(async () => jsonResponse({ mtime: "2026-09-24T11:00:00Z" }));
    await expect(updateRuntimeProfile(REF, { document: {}, expectedMtime: "" })).rejects.toThrow(
      /profile view missing/,
    );
  });

  it("删除：默认不带 force，显式 force 时拼查询串；成功响应按 deleted 归一化", async () => {
    const calls: string[] = [];
    mockFetch(async (input) => {
      calls.push(String(input));
      return jsonResponse({
        deleted: true,
        ref: REF,
        root: "/home/u/.aicli",
        removed_files: ["profiles/reviewer.yaml"],
        removed_file_count: 1,
        config_items_removed: ["reviewer"],
        default_cleared: true,
        references: { ref: REF, blocking: [], warnings: [], files: [] },
        recoverable: false,
      });
    });

    const plain = await deleteRuntimeProfile(REF);
    const forced = await deleteRuntimeProfile(REF, { force: true });

    expect(calls[0]).toBe(`/api/runtime/profiles/${ENCODED_REF}`);
    expect(calls[1]).toBe(`/api/runtime/profiles/${ENCODED_REF}?force=true`);
    expect(plain.deleted).toBe(true);
    expect(forced.removedFileCount).toBe(1);
    expect(forced.configItemsRemoved).toEqual(["reviewer"]);
    expect(forced.defaultCleared).toBe(true);
    expect(forced.references?.blocking).toEqual([]);
  });

  it("删除被引用阻断：409 错误体里的 references 可回填，非 409 一律返回 null", async () => {
    mockFetch(async () =>
      jsonResponse(
        {
          deleted: false,
          error: "profile 仍被引用，需 force=true 才能删除",
          references: {
            ref: REF,
            root: "/home/u/.aicli",
            is_default: true,
            blocking: ["config 默认 profile 指向该条目"],
            warnings: ["1 个会话正在使用"],
            config_items: [{ name: "reviewer", root: "/home/u/.aicli", is_default: true }],
            files: ["profiles/reviewer.yaml"],
            file_count: 1,
          },
        },
        409,
      ),
    );

    const error = await deleteRuntimeProfile(REF).catch((caught: unknown) => caught);
    const blocked = readProfileDeleteBlockingReferences(error);

    expect(blocked?.blocking).toEqual(["config 默认 profile 指向该条目"]);
    expect(blocked?.configItems).toEqual([
      { name: "reviewer", root: "/home/u/.aicli", isDefault: true },
    ]);
    expect(blocked?.fileCount).toBe(1);

    mockFetch(async () => jsonResponse({ error: "boom" }, 500));
    const serverError = await deleteRuntimeProfile(REF).catch((caught: unknown) => caught);
    expect(readProfileDeleteBlockingReferences(serverError)).toBeNull();

    // 409 但没带 references：不能假装拿到了阻断清单。
    mockFetch(async () => jsonResponse({ deleted: false, error: "blocked" }, 409));
    const bare = await deleteRuntimeProfile(REF).catch((caught: unknown) => caught);
    expect(readProfileDeleteBlockingReferences(bare)).toBeNull();
  });

  it("校验：请求体是 {spec, agent}，valid/counts/issues 归一化", async () => {
    const fetchMock = mockFetch(async () =>
      jsonResponse({
        ref: REF,
        name: "reviewer",
        path: "profiles/reviewer.yaml",
        root: "/home/u/.aicli",
        agent: "reviewer",
        valid: false,
        error_count: 1,
        warning_count: 1,
        issues: [
          { path: "tools.allowlist", message: "unknown tool", severity: "error" },
          { path: "skills.top_k", message: "large top_k", severity: "warning" },
        ],
      }),
    );

    const report = await validateRuntimeProfile(
      REF,
      { profile: { name: "reviewer" } },
      "reviewer",
    );

    expect(report.valid).toBe(false);
    expect(report.errorCount).toBe(1);
    expect(report.warningCount).toBe(1);
    expect(report.issues).toHaveLength(2);
    expect(report.issues[1]).toMatchObject({ path: "skills.top_k", severity: "warning" });
    expect(fetchMock.mock.calls[0][0]).toBe(`/api/runtime/profiles/${ENCODED_REF}/validate`);
    expect(readBody(fetchMock.mock.calls[0][1] as RequestInit)).toEqual({
      spec: { profile: { name: "reviewer" } },
      agent: "reviewer",
    });

    // 非法 severity 一律归一化成 error；message 缺失时用 path 兜底，避免空行。
    mockFetch(async () => jsonResponse({ valid: true, issues: [{ path: "x.y", severity: "fatal" }] }));
    const fallback = await validateRuntimeProfile(REF, {});
    expect(fallback.issues[0]).toEqual({ path: "x.y", message: "x.y", severity: "error" });
  });

  it("预览：与 GET 同构返回 resolved view（preview=true），不是 diff 文本", async () => {
    const fetchMock = mockFetch(async () => jsonResponse(VIEW_PAYLOAD));

    const preview = await previewRuntimeProfile(
      REF,
      { profile: { name: "reviewer" } },
      "reviewer",
    );

    expect(preview.ref).toBe(REF);
    expect(preview.preview).toBe(true);
    expect(preview.tools.effective).toEqual(["read_file", "grep"]);
    expect(readBody(fetchMock.mock.calls[0][1] as RequestInit)).toEqual({
      spec: { profile: { name: "reviewer" } },
      agent: "reviewer",
    });
  });

  it("应用：POST /apply 带 session_id；501 not_implemented 保留后端 code", async () => {
    const fetchMock = mockFetch(async () =>
      jsonResponse({ ok: true, changed: { profile: REF }, warnings: [] }),
    );
    const applied = await applyRuntimeProfile(REF, "sess-1");
    expect(readBody(fetchMock.mock.calls[0][1] as RequestInit)).toEqual({
      session_id: "sess-1",
    });
    expect(applied.ok).toBe(true);

    mockFetch(async () =>
      jsonResponse({ error: { code: "not_implemented", message: "apply pending" } }, 501),
    );
    const error = await applyRuntimeProfile(REF).catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(RuntimeApiError);
    expect((error as RuntimeApiError).status).toBe(501);
    expect(isRuntimeApiErrorCode(error, "not_implemented")).toBe(true);
  });

  it("引用清单：非对象载荷抛错，引用面按 blocking/warnings/configItems/files 归一化", async () => {
    mockFetch(async () =>
      jsonResponse({
        ref: REF,
        root: "/home/u/.aicli",
        default_profile: REF,
        is_default: true,
        blocking: ["builtin 层不可删除"],
        warnings: ["1 个会话正在使用"],
        config_items: [{ name: "reviewer", root: "/home/u/.aicli", is_default: true }],
        session_scan: { scanned: 3 },
        agent_references: [{ id: "reviewer" }],
        agent_reference_note: "agent 引用不阻断删除",
        files: ["profiles/reviewer.yaml"],
        file_count: 1,
      }),
    );

    const result = await listRuntimeProfileReferences(REF);

    expect(result.ref).toBe(REF);
    expect(result.defaultProfile).toBe(REF);
    expect(result.isDefault).toBe(true);
    expect(result.blocking).toEqual(["builtin 层不可删除"]);
    expect(result.configItems).toEqual([
      { name: "reviewer", root: "/home/u/.aicli", isDefault: true },
    ]);
    expect(result.agentReferences).toEqual([{ id: "reviewer" }]);
    expect(result.agentReferenceNote).toBe("agent 引用不阻断删除");
    expect(result.fileCount).toBe(1);

    // 契约回归：载荷不是对象时抛错，不能返回一份「全空但看起来正常」的引用面。
    mockFetch(async () => jsonResponse(null));
    await expect(listRuntimeProfileReferences(REF)).rejects.toThrow(/object expected/);
  });

  it("写回冲突：409 走 RuntimeApiError，错误体无 code，调用方按 status 判定", async () => {
    mockFetch(async () =>
      jsonResponse(
        {
          updated: false,
          error: "profile.yaml 已被其他写入者修改（mtime 冲突），请 reload 后重试",
          expected_mtime: "2026-09-24T10:00:00Z",
          current_mtime: "2026-09-24T11:00:00Z",
        },
        409,
      ),
    );

    const error = await updateRuntimeProfile(REF, {
      document: {},
      expectedMtime: "2026-09-24T10:00:00Z",
    }).catch((caught: unknown) => caught);

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect((error as RuntimeApiError).status).toBe(409);
    // 该错误体是扁平信封且**不带 code**：不能靠 isRuntimeApiErrorCode 判定冲突，
    // UI 必须按 status=409 处理，并原样展示后端消息提示 reload。
    expect(isRuntimeApiErrorCode(error, "profile_conflict")).toBe(false);
    expect(readErrorEnvelope((error as RuntimeApiError).payload).message).toContain("mtime 冲突");
  });
});

