import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { APP_SETTINGS_STORAGE_KEY, SettingsProvider } from "@/core/settings";

import { HarnessSettingsPage } from "./harness-settings-page";

const permissionsResponse = {
  workspace_path: "E:/projects/ai/ai-agent-runtime",
  source_path: "E:/projects/ai/ai-agent-runtime/.aicli/permissions.yaml",
  exists: true,
  version: 1,
  deny_tools: ["shell"],
  allow_tools: ["view"],
  rules: [
    {
      name: "ask-writes",
      tools: ["write", "edit"],
      decision: "ask",
      reason: "review_writes",
    },
  ],
};

const grantsResponse = {
  workspace_path: "E:/projects/ai/ai-agent-runtime",
  store_path: "E:/projects/ai/ai-agent-runtime/.aicli/grants.json",
  grants: [{ tool: "write", pattern: "README.md", scope: "project" }],
  count: 1,
};

const memoryResponse = {
  workspace_path: "E:/projects/ai/ai-agent-runtime",
  notes: [
    {
      id: "note-1",
      text: "prefer apply_patch for multi-hunk edits",
      tags: ["workflow"],
      source: "settings",
      created_at: "2026-07-25T10:00:00Z",
    },
  ],
  count: 1,
};

const pluginsResponse = {
  workspace_path: "E:/projects/ai/ai-agent-runtime",
  plugins: [
    {
      id: "demo-plugin",
      name: "Demo Plugin",
      version: "0.1.0",
      description: "harness test plugin",
      trust: "untrusted",
      enabled: true,
      active: false,
    },
  ],
  count: 1,
};

describe("HarnessSettingsPage", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/api/runtime/harness/permissions")) {
        return new Response(JSON.stringify(permissionsResponse), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.includes("/api/runtime/harness/grants")) {
        return new Response(JSON.stringify(grantsResponse), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.includes("/api/runtime/harness/memory")) {
        return new Response(JSON.stringify(memoryResponse), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.includes("/api/runtime/harness/plugins")) {
        return new Response(JSON.stringify(pluginsResponse), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify({ error: "not found" }), {
        status: 404,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("renders harness panels for permissions, grants, memory, and plugins", () => {
    const previousSettings = window.localStorage.getItem(APP_SETTINGS_STORAGE_KEY);
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify({ localization: { locale: "zh-CN" } }),
    );

    try {
      const markup = renderToStaticMarkup(
        <SettingsProvider>
          <HarnessSettingsPage
            runtimeClient={{
              clientId: "client-alpha",
              userId: "web-console:workspace-alpha:client-alpha",
              workspacePath: "E:/projects/ai/ai-agent-runtime",
              workspaceScope: "workspace-alpha",
            }}
          />
        </SettingsProvider>,
      );

      expect(markup).toContain("Harness");
      expect(markup).toContain("E:/projects/ai/ai-agent-runtime");
      expect(markup).toContain("权限规则");
      expect(markup).toContain("记忆笔记");
      expect(markup).toContain("插件目录");
      expect(markup).toContain("记住授权");
      expect(markup).toContain("permissions.yaml");
      expect(markup).toContain("grants.json");
    } finally {
      if (previousSettings == null) {
        window.localStorage.removeItem(APP_SETTINGS_STORAGE_KEY);
      } else {
        window.localStorage.setItem(APP_SETTINGS_STORAGE_KEY, previousSettings);
      }
    }
  });
});
