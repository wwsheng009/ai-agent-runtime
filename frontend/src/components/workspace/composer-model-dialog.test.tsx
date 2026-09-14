// @vitest-environment jsdom

// P2-7 子片 3：`/model` 弹窗组件单测。
//
// 覆盖口径（只断言稳定 data 钩子与行为，不绑定样式/文案）：
// - 关闭时不渲染；
// - 目录未就绪的三种形态各自如实呈现：加载中 / 失败（带原因）/ 空目录；
// - 分组与「当前选中」标记来自宿主数据（provider + 模型名同时匹配才算当前）；
// - 选中回调把模型 id 原文交回宿主处理器（弹窗不猜 provider）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    // 测试只关心「插值是否到达 DOM」：把 key 与插值原文拼接，不引入真实词条。
    t: (key: string, values?: Record<string, unknown>) =>
      values ? `${key} ${Object.values(values).join(" ")}` : key,
  }),
}));

import { ComposerModelDialog } from "./composer-model-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const groups = [
  { provider: "deepseek", models: ["deepseek-chat", "deepseek-reasoner"] },
  { provider: "openai", models: ["gpt-5"] },
];

describe("ComposerModelDialog", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(overrides: Partial<Parameters<typeof ComposerModelDialog>[0]> = {}) {
    const props = {
      error: null as string | null,
      groups,
      loading: false,
      onClose: vi.fn(),
      onSelect: vi.fn(),
      open: true,
      selectedModel: "deepseek-chat",
      selectedProvider: "deepseek",
      ...overrides,
    };
    act(() => {
      root.render(<ComposerModelDialog {...props} />);
    });
    return props;
  }

  function query(selector: string): Element | null {
    return container.querySelector(selector);
  }

  it("open=false 时不渲染", () => {
    render({ open: false });
    expect(query("[data-composer-model-dialog]")).toBeNull();
  });

  it("按 provider 分组渲染全部模型，并标记当前选中（provider + 模型同时匹配）", () => {
    render();

    expect(query('[data-composer-model-provider="deepseek"]')).not.toBeNull();
    expect(query('[data-composer-model-provider="openai"]')).not.toBeNull();
    expect(container.querySelectorAll("[data-composer-model-option]")).toHaveLength(3);

    const current = container.querySelectorAll("[data-composer-model-option-current]");
    expect(current).toHaveLength(1);
    expect(current[0]?.getAttribute("data-composer-model-option")).toBe(
      "deepseek-chat",
    );
  });

  it("同名模型属于其它 provider 时不算当前选中", () => {
    render({ groups: [{ provider: "other", models: ["deepseek-chat"] }] });

    expect(query("[data-composer-model-option-current]")).toBeNull();
  });

  it("选中即把模型 id 原文交回宿主处理器", () => {
    const props = render();

    const option = container.querySelector(
      '[data-composer-model-option="deepseek-reasoner"]',
    );
    act(() => {
      (option as HTMLButtonElement).click();
    });

    expect(props.onSelect).toHaveBeenCalledWith("deepseek-reasoner");
  });

  it("加载中且尚无模型时显示加载态（不显示空态）", () => {
    render({ groups: [], loading: true });

    expect(query("[data-composer-model-dialog-loading]")).not.toBeNull();
    expect(query("[data-composer-model-dialog-empty]")).toBeNull();
  });

  it("失败时显示错误（不显示空态）", () => {
    render({ error: "catalog request failed", groups: [], loading: false });

    expect(query("[data-composer-model-dialog-error]")?.textContent).toContain(
      "catalog request failed",
    );
    expect(query("[data-composer-model-dialog-empty]")).toBeNull();
  });

  it("目录为空且非加载/失败时显示空态（不补占位模型）", () => {
    render({ groups: [] });

    expect(query("[data-composer-model-dialog-empty]")).not.toBeNull();
    expect(container.querySelectorAll("[data-composer-model-option]")).toHaveLength(0);
  });
});
