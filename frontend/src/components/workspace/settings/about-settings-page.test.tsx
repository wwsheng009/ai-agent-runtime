import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { APP_SETTINGS_STORAGE_KEY, SettingsProvider } from "@/core/settings";
import { RUNTIME_CLIENT_STORAGE_KEY } from "@/lib/runtime-client";

import { AboutSettingsPage } from "./about-settings-page";

describe("AboutSettingsPage", () => {
  it("renders the runtime identity summary and storage keys", () => {
    const previousSettings = window.localStorage.getItem(APP_SETTINGS_STORAGE_KEY);
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify({ localization: { locale: "zh-CN" } }),
    );

    try {
      const markup = renderToStaticMarkup(
        <MemoryRouter initialEntries={["/workspace/chats/new"]}>
          <SettingsProvider>
            <AboutSettingsPage
              onResetRuntimeClientIdentity={vi.fn()}
              providerOptions={["openai", "deepseek"]}
              runtimeClient={{
                clientId: "client-alpha",
                userId: "web-console:workspace-alpha:client-alpha",
                workspacePath: "E:/projects/ai/ai-agent-runtime",
                workspaceScope: "workspace-alpha",
              }}
              runtimeSessionsSummary={{
                activeCount: 2,
                archivedCount: 1,
                latestSessionId: "session-123",
                latestUpdatedAt: "2026-04-06T09:00:00Z",
                recoverableCount: 2,
                totalCount: 3,
              }}
              runtimeTeams={[
                { id: "team-1", status: "active" },
                { id: "team-2", status: "idle" },
              ]}
              selectedModel="gpt-5.4"
              selectedProvider="openai"
            />
          </SettingsProvider>
        </MemoryRouter>,
      );

      expect(markup).toContain("运行时身份");
      expect(markup).toContain("web-console:workspace-alpha:client-alpha");
      expect(markup).toContain("范围: workspace-alpha");
      expect(markup).toContain("E:/projects/ai/ai-agent-runtime");
      expect(markup).toContain("最近更新");
      expect(markup).toContain("openai / gpt-5.4");
      expect(markup).toContain("设置数据存于浏览器 localStorage");
      expect(markup).toContain("重置本地 runtime client id");
      expect(markup).toContain(APP_SETTINGS_STORAGE_KEY);
      expect(markup).toContain(RUNTIME_CLIENT_STORAGE_KEY);
      expect(markup).toContain("/workspace/chats/new");
    } finally {
      if (previousSettings == null) {
        window.localStorage.removeItem(APP_SETTINGS_STORAGE_KEY);
      } else {
        window.localStorage.setItem(APP_SETTINGS_STORAGE_KEY, previousSettings);
      }
    }
  });
});
