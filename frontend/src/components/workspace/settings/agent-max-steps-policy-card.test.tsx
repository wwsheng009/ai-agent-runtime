// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { APP_SETTINGS_STORAGE_KEY, SettingsProvider } from "@/core/settings";

import { AgentMaxStepsPolicyCard } from "./agent-max-steps-policy-card";

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

function setNumberInput(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("AgentMaxStepsPolicyCard", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify({ localization: { locale: "zh-CN" } }),
    );
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

  function renderCard() {
    return (
      <SettingsProvider>
        <AgentMaxStepsPolicyCard />
      </SettingsProvider>
    );
  }

  it("loads the server default and saves the edited value to the runtime config", async () => {
    const fetchMock = vi.fn<
      (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>
    >(async (input, init) => {
      expect(String(input)).toContain(MAX_STEPS_ENDPOINT);
      if ((init?.method ?? "GET").toUpperCase() === "GET") {
        return jsonResponse({
          limit: 100,
          max_steps: 12,
          config_file: CONFIG_FILE,
        });
      }
      const body = JSON.parse(String(init?.body)) as { max_steps: number };
      return jsonResponse({
        updated: true,
        max_steps: body.max_steps,
        config_file: CONFIG_FILE,
      });
    });
    globalThis.fetch = fetchMock as unknown as typeof fetch;

    await act(async () => {
      root?.render(renderCard());
    });

    expect(document.body.textContent).toContain("服务端缺省：12");
    expect(document.body.textContent).toContain(CONFIG_FILE);
    expect(document.body.textContent).toContain("取值范围 0–100");

    const input = container.querySelector<HTMLInputElement>(
      'input[type="number"]',
    );
    expect(input?.value).toBe("12");

    await act(async () => {
      if (input) {
        setNumberInput(input, "17");
      }
    });
    await act(async () => {
      findButton("保存服务端缺省")?.click();
    });

    const putCall = fetchMock.mock.calls.find(
      ([, init]) => (init?.method ?? "GET").toUpperCase() === "PUT",
    );
    expect(putCall).toBeDefined();
    expect(JSON.parse(String(putCall?.[1]?.body))).toEqual({ max_steps: 17 });
    expect(document.body.textContent).toContain("已保存：服务端缺省 17");
  });

  it("reports the load failure and disables saving", async () => {
    globalThis.fetch = vi.fn(async () =>
      jsonResponse({ error: "runtime config is not loaded" }, 500),
    ) as unknown as typeof fetch;

    await act(async () => {
      root?.render(renderCard());
    });

    expect(document.body.textContent).toContain("读取服务端缺省值失败");
    expect(document.body.textContent).toContain("runtime config is not loaded");
    expect(findButton("保存服务端缺省")?.disabled).toBe(true);
  });
});
