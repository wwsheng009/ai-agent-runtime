// 会话路由 API 客户端单测：契约字段、写入路径与错误透传。
// 只 mock `globalThis.fetch`，不触碰真实后端（与 session-compact.test.ts 同一范式）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  getSessionRouting,
  normalizeSessionRoutingResponse,
  updateSessionRouting,
} from "@/api/runtime/session-routing";
import { RuntimeApiError } from "@/api/runtime/shared";

function buildResponseBody() {
  return {
    session_id: "session-1",
    scope: "main",
    target_layer: "session",
    target_path: "",
    actor_invalidated: false,
    updated: true,
    routing: {
      schema_version: 1,
      enabled: true,
      level: "hard",
      provider: "opencode.ai",
      model: "deepseek-v4.1",
      reasoning: "high",
      source: "session",
      disabled: false,
      warnings: ["expensive guard active"],
      revision: "2026-09-22T00:00:00Z by web",
      effective_from: "next_turn",
    },
    sub_agent: {
      schema_version: 1,
      enabled: false,
      level: "",
      provider: "",
      model: "",
      reasoning: "",
      source: "default",
      disabled: true,
      warnings: [],
      revision: "",
      effective_from: "next_turn",
    },
    panel: {
      scope: "main",
      child_session: false,
      session_override: true,
      workspace_override: false,
      config_override: true,
      workspace_path: "E:/ws",
      workspace_prefs_path: "E:/ws/.aicli/chat-prefs.yaml",
      config_path: "C:/Users/vince/.aicli/config.yaml",
      config_layer: "user",
      writable_layers: ["session", "workspace", "config"],
      levels: [
        {
          level: "hard",
          enabled: true,
          provider: "opencode.ai",
          model: "deepseek-v4.1",
          reasoning: "high",
          source: "session",
          expensive: false,
        },
      ],
    },
    warnings: ["panel warning"],
  };
}

describe("会话路由 API 客户端", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];
  let responder: (url: string, init?: RequestInit) => Response = () =>
    new Response(JSON.stringify(buildResponseBody()), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });

  beforeEach(() => {
    calls = [];
    responder = () =>
      new Response(JSON.stringify(buildResponseBody()), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        calls.push({ url, init });
        return responder(url, init);
      },
    ) as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("GET 走 /sessions/{id}/routing 并归一化 snake_case 投影", async () => {
    const result = await getSessionRouting("session/1", { adminToken: " tok " });

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/api/runtime/sessions/session%2F1/routing");
    expect(calls[0].init?.method).toBe("GET");
    expect(
      (calls[0].init?.headers as Record<string, string>).Authorization,
    ).toBe("Bearer tok");
    expect(result.routing.effectiveFrom).toBe("next_turn");
    expect(result.routing.warnings).toEqual(["expensive guard active"]);
    expect(result.panel.workspacePrefsPath).toBe("E:/ws/.aicli/chat-prefs.yaml");
    expect(result.panel.configLayer).toBe("user");
    expect(result.panel.writableLayers).toEqual(["session", "workspace", "config"]);
    expect(result.panel.levels[0]).toMatchObject({
      level: "hard",
      provider: "opencode.ai",
      source: "session",
      expensive: false,
    });
    expect(result.warnings).toEqual(["panel warning"]);
  });

  it("PATCH 透传写入层、二次确认与字段级补丁", async () => {
    await updateSessionRouting("session-1", {
      target_layer: "config",
      confirm: true,
      main_agent: { enabled: false, profiles: { hard: { model: "next-model" } } },
      clear_fields: ["main_agent.profiles.easy.provider"],
      updated_by: "web",
    });

    expect(calls[0].init?.method).toBe("PATCH");
    expect(
      (calls[0].init?.headers as Record<string, string>)["Content-Type"],
    ).toBe("application/json");
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      target_layer: "config",
      confirm: true,
      main_agent: { enabled: false, profiles: { hard: { model: "next-model" } } },
      clear_fields: ["main_agent.profiles.easy.provider"],
      updated_by: "web",
    });
  });

  it("reset 只发 target_layer + clear", async () => {
    await updateSessionRouting("session-1", {
      target_layer: "workspace",
      clear: true,
    });

    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      target_layer: "workspace",
      clear: true,
    });
  });

  it("非 2xx 抛 RuntimeApiError 并保留后端原文", async () => {
    responder = () =>
      new Response(
        JSON.stringify({
          error: "config layer requires confirm=true",
          code: "invalid_request",
        }),
        { status: 400, headers: { "Content-Type": "application/json" } },
      );

    await expect(
      updateSessionRouting("session-1", { target_layer: "config" }),
    ).rejects.toMatchObject({
      name: "RuntimeApiError",
      status: 400,
      message: "config layer requires confirm=true",
    });
  });

  it("缺字段的响应不臆造可写层与档位（置灰优先）", () => {
    const normalized = normalizeSessionRoutingResponse({
      session_id: "session-1",
      panel: { scope: "sub" },
    });

    expect(normalized.panel.writableLayers).toEqual([]);
    expect(normalized.panel.levels).toEqual([]);
    expect(normalized.panel.scope).toBe("sub");
    expect(normalized.routing.enabled).toBe(false);
    expect(normalized.routing.disabled).toBe(false);
    expect(normalized.panel.subAgent).toBeNull();
  });

  it("RuntimeApiError 类型可被 isRuntimeApiErrorCode 风格的分支识别", async () => {
    responder = () =>
      new Response(JSON.stringify({ error: "forbidden" }), {
        status: 403,
        headers: { "Content-Type": "application/json" },
      });

    const error = await getSessionRouting("session-1").catch((err: unknown) => err);
    expect(error).toBeInstanceOf(RuntimeApiError);
    expect((error as RuntimeApiError).status).toBe(403);
  });
});
