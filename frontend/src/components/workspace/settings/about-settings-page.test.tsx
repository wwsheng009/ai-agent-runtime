import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { APP_SETTINGS_STORAGE_KEY } from "@/core/settings";
import { RUNTIME_CLIENT_STORAGE_KEY } from "@/lib/runtime-client";

import { AboutSettingsPage } from "./about-settings-page";

describe("AboutSettingsPage", () => {
  it("renders the runtime identity summary and storage keys", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/workspace/chats/new"]}>
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
      </MemoryRouter>,
    );

    expect(markup).toContain("Runtime identity");
    expect(markup).toContain("web-console:workspace-alpha:client-alpha");
    expect(markup).toContain("scope: workspace-alpha");
    expect(markup).toContain("E:/projects/ai/ai-agent-runtime");
    expect(markup).toContain("重置本地 runtime client id");
    expect(markup).toContain(APP_SETTINGS_STORAGE_KEY);
    expect(markup).toContain(RUNTIME_CLIENT_STORAGE_KEY);
    expect(markup).toContain("/workspace/chats/new");
  });
});
