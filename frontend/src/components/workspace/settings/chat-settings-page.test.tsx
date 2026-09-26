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
const APPROVAL_EXPLAIN_ENDPOINT = "/api/runtime/config/approval-explain";
const APPROVAL_EXPLAIN_SUPPORTED_MODES = ["off", "on_demand", "pre_generate"];
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

function approvalExplainTrigger() {
  return document.querySelector<HTMLButtonElement>(
    'button[aria-label="审批解释模式"]',
  );
}

function findOption(label: string) {
  return Array.from(
    document.querySelectorAll<HTMLElement>('[role="option"]'),
  ).find((option) => option.textContent?.includes(label));
}

function mockRuntimeFetch(options: {
  configFile?: string;
  defaultMaxSteps?: number;
  saveError?: string;
  saveStatus?: number;
  approvalExplainMode?: string;
  approvalExplainGetError?: string;
  approvalExplainGetStatus?: number;
  approvalExplainSaveError?: string;
  approvalExplainSaveStatus?: number;
}) {
  const configFile = options.configFile ?? CONFIG_FILE;
  let approvalExplainMode = options.approvalExplainMode ?? "on_demand";

  return vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(
    async (input, init) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();

      if (url.includes(APPROVAL_EXPLAIN_ENDPOINT)) {
        if (method === "GET") {
          if (
            options.approvalExplainGetStatus &&
            options.approvalExplainGetStatus >= 400
          ) {
            return jsonResponse(
              { error: options.approvalExplainGetError ?? "not found" },
              options.approvalExplainGetStatus,
            );
          }
          return jsonResponse({
            mode: approvalExplainMode,
            supported_modes: APPROVAL_EXPLAIN_SUPPORTED_MODES,
          });
        }
        if (
          options.approvalExplainSaveStatus &&
          options.approvalExplainSaveStatus >= 400
        ) {
          return jsonResponse(
            { error: options.approvalExplainSaveError ?? "save failed" },
            options.approvalExplainSaveStatus,
          );
        }

        const body = JSON.parse(String(init?.body)) as { mode: string };
        approvalExplainMode = body.mode;
        return jsonResponse({
          mode: body.mode,
          supported_modes: APPROVAL_EXPLAIN_SUPPORTED_MODES,
          updated: true,
        });
      }

      expect(url).toContain(MAX_STEPS_ENDPOINT);
      if (method === "GET") {
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
    // jsdom 未实现 scrollIntoView；Select 打开菜单时会调用它。
    Element.prototype.scrollIntoView = vi.fn();
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
    const fetchMock = mockRuntimeFetch({ defaultMaxSteps: 12 });
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
    const fetchMock = mockRuntimeFetch({
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
    const fetchMock = mockRuntimeFetch({ defaultMaxSteps: 12 });
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
    const fetchMock = mockRuntimeFetch({ defaultMaxSteps: 64 });
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

  describe("approval explain mode", () => {
    async function selectApprovalExplainOption(label: string) {
      await act(async () => {
        approvalExplainTrigger()?.click();
      });
      await act(async () => {
        findOption(label)?.click();
      });
    }

    it("reflects the current mode and saves a switch", async () => {
      const fetchMock = mockRuntimeFetch({ approvalExplainMode: "on_demand" });
      seedLocalMaxSteps(9, fetchMock as unknown as typeof fetch);

      await act(async () => {
        root?.render(renderPage());
      });

      expect(approvalExplainTrigger()?.textContent).toContain("按需生成");
      expect(document.body.textContent).toContain("当前模式：按需生成");
      expect(document.body.textContent).toContain("该开关不持久化");

      await selectApprovalExplainOption("预生成");
      await act(async () => {
        findButton("保存解释模式")?.click();
      });

      const putCall = fetchMock.mock.calls.find(
        ([input, init]) =>
          String(input).includes(APPROVAL_EXPLAIN_ENDPOINT) &&
          (init?.method ?? "GET").toUpperCase() === "PUT",
      );
      expect(putCall).toBeDefined();
      expect(JSON.parse(String(putCall?.[1]?.body))).toEqual({
        mode: "pre_generate",
      });
      expect(document.body.textContent).toContain("已切换为「预生成」");
      expect(approvalExplainTrigger()?.textContent).toContain("预生成");
    });

    it("surfaces the runtime error when the switch fails", async () => {
      const fetchMock = mockRuntimeFetch({
        approvalExplainMode: "on_demand",
        approvalExplainSaveError: "unsupported approval explain mode",
        approvalExplainSaveStatus: 400,
      });
      seedLocalMaxSteps(9, fetchMock as unknown as typeof fetch);

      await act(async () => {
        root?.render(renderPage());
      });
      await selectApprovalExplainOption("关闭");
      await act(async () => {
        findButton("保存解释模式")?.click();
      });

      expect(document.body.textContent).toContain("切换失败");
      expect(document.body.textContent).toContain(
        "unsupported approval explain mode",
      );
      // 失败不丢弃草稿：下拉仍停在用户选择的模式，可以直接重试。
      expect(approvalExplainTrigger()?.textContent).toContain("关闭");
    });

    it("degrades gracefully when the endpoint is unavailable", async () => {
      const fetchMock = mockRuntimeFetch({
        approvalExplainGetError: "not found",
        approvalExplainGetStatus: 404,
      });
      seedLocalMaxSteps(9, fetchMock as unknown as typeof fetch);

      await act(async () => {
        root?.render(renderPage());
      });

      expect(document.body.textContent).toContain("未能读取解释模式");
      expect(document.body.textContent).toContain("not found");
      expect(approvalExplainTrigger()?.disabled).toBe(true);
      expect(findButton("保存解释模式")?.disabled).toBe(true);
      // 其余设置照常渲染，不因该端点缺失而整体失败。
      expect(maxStepsInput()?.value).toBe("9");
      expect(document.body.textContent).not.toContain("切换失败");
    });
  });
});
