// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { APP_SETTINGS_STORAGE_KEY, SettingsProvider } from "@/core/settings";

import { ChatSettingsPage } from "./chat-settings-page";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const MAX_STEPS_ENDPOINT = "/api/runtime/config/agent/max-steps";
const CONFIG_FILE = "E:/configs/runtime.yaml";

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

function maxStepsInput() {
  return document.querySelector<HTMLInputElement>('input[type="number"]');
}

function mockRuntimeMaxStepsFetch(options: {
  configFile?: string;
  defaultMaxSteps?: number;
  saveError?: string;
  saveStatus?: number;
}) {
  const configFile = options.configFile ?? CONFIG_FILE;

  return vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(
    async (input, init) => {
      expect(String(input)).toContain(MAX_STEPS_ENDPOINT);
      if ((init?.method ?? "GET").toUpperCase() === "GET") {
        return jsonResponse({
          limit: 100,
          max_steps: options.defaultMaxSteps ?? 12,
          config_file: configFile,
        });
      }
      if (options.saveStatus && options.saveStatus >= 400) {
        return jsonResponse(
          { error: options.saveError ?? "save failed" },
          options.saveStatus,
        );
      }

      const body = JSON.parse(String(init?.body)) as { max_steps: number };
      return jsonResponse({
        updated: true,
        max_steps: body.max_steps,
        config_file: configFile,
      });
    },
  );
}

function renderPage() {
  return (
    <MemoryRouter initialEntries={["/workspace/settings"]}>
      <SettingsProvider>
        <ChatSettingsPage
          modelOptions={["gpt-4o-mini"]}
          onModelChange={vi.fn()}
          onProviderChange={vi.fn()}
          providerOptions={["openai"]}
          runtimeModelsError={null}
          runtimeModelsLoading={false}
          selectedModel="gpt-4o-mini"
          selectedProvider="openai"
        />
      </SettingsProvider>
    </MemoryRouter>
  );
}

describe("ChatSettingsPage max steps", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  const originalFetch = globalThis.fetch;

  function seedLocalMaxSteps(maxSteps: number, fetchImpl: typeof fetch) {
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify({
        localization: { locale: "zh-CN" },
        chat: { maxSteps },
      }),
    );
    globalThis.fetch = fetchImpl;
  }

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    window.localStorage.removeItem(APP_SETTINGS_STORAGE_KEY);
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    globalThis.fetch = originalFetch;
    window.localStorage.removeItem(APP_SETTINGS_STORAGE_KEY);
  });

  it("saves the local max steps value to the runtime config endpoint", async () => {
    const fetchMock = mockRuntimeMaxStepsFetch({ defaultMaxSteps: 12 });
    seedLocalMaxSteps(9, fetchMock as unknown as typeof fetch);

    await act(async () => {
      root?.render(renderPage());
    });
    expect(document.body.textContent).toContain("保存到后端");
    expect(document.body.textContent).toContain("后端缺省：12");

    await act(async () => {
      findButton("保存到后端")?.click();
    });

    const putCall = fetchMock.mock.calls.find(
      ([, init]) => (init?.method ?? "GET").toUpperCase() === "PUT",
    );
    expect(putCall).toBeDefined();
    expect(JSON.parse(String(putCall?.[1]?.body))).toEqual({ max_steps: 9 });
    expect(document.body.textContent).toContain("已保存：最大步骤数 9");
    expect(document.body.textContent).toContain(CONFIG_FILE);
  });

  it("surfaces the runtime error when the save fails", async () => {
    const fetchMock = mockRuntimeMaxStepsFetch({
      saveError: "disk is read-only",
      saveStatus: 500,
    });
    seedLocalMaxSteps(9, fetchMock as unknown as typeof fetch);

    await act(async () => {
      root?.render(renderPage());
    });

    await act(async () => {
      findButton("保存到后端")?.click();
    });

    expect(document.body.textContent).toContain("保存失败");
    expect(document.body.textContent).toContain("disk is read-only");
  });

  it("flags the shadowed server default and adopts it on demand", async () => {
    const fetchMock = mockRuntimeMaxStepsFetch({ defaultMaxSteps: 12 });
    seedLocalMaxSteps(0, fetchMock as unknown as typeof fetch);

    await act(async () => {
      root?.render(renderPage());
    });

    expect(maxStepsInput()?.value).toBe("0");
    expect(document.body.textContent).toContain("会覆盖后端缺省 12");

    await act(async () => {
      findButton("采用后端缺省")?.click();
    });

    expect(maxStepsInput()?.value).toBe("12");
    expect(document.body.textContent).not.toContain("会覆盖后端缺省");
  });

  it("clamps an adopted server default above the workspace limit", async () => {
    const fetchMock = mockRuntimeMaxStepsFetch({ defaultMaxSteps: 64 });
    seedLocalMaxSteps(6, fetchMock as unknown as typeof fetch);

    await act(async () => {
      root?.render(renderPage());
    });

    expect(document.body.textContent).toContain("超出工作区上限 20");

    await act(async () => {
      findButton("采用后端缺省")?.click();
    });

    expect(maxStepsInput()?.value).toBe("20");
  });
});
