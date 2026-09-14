// @vitest-environment jsdom

// P2-7 子片 3：`/model` 弹窗组件单测。
// P2-7 子片 4：补候选面语义（打开即聚焦检索框并预高亮当前座位、本地检索、
//   方向键 + Enter 应用高亮行、无匹配与目录为空是两种状态）。
//
// 覆盖口径（只断言稳定 data 钩子与行为，不绑定样式/文案）：
// - 关闭时不渲染；
// - 目录未就绪的三种形态各自如实呈现：加载中 / 失败（带原因）/ 空目录；
// - 分组与「当前选中」标记来自宿主数据（provider + 模型名同时匹配才算当前）；
// - 选中回调把模型 id 原文交回宿主处理器（弹窗不猜 provider）；
// - 检索与高亮都是本地行为，不新增数据通道。

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

  function searchInput(): HTMLInputElement {
    const input = container.querySelector<HTMLInputElement>("[data-composer-model-search]");
    if (!input) {
      throw new Error("search input missing");
    }
    return input;
  }

  function typeSearch(value: string): HTMLInputElement {
    const input = searchInput();
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
    setter?.call(input, value);
    act(() => {
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    return input;
  }

  function pressKey(target: Element, key: string): void {
    act(() => {
      target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    });
  }

  function activeModel(): string | null {
    return query("[data-composer-model-option-active]")?.getAttribute(
      "data-composer-model-option",
    ) ?? null;
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

  it("打开时焦点交给检索框，并预高亮当前座位", () => {
    render();

    expect(document.activeElement).toBe(searchInput());
    expect(activeModel()).toBe("deepseek-chat");
  });

  it("目录晚到：检索框挂载后接住焦点（不把焦点丢在遮罩上）", () => {
    const props = render({ groups: [], loading: true });
    expect(container.querySelector("[data-composer-model-search]")).toBeNull();

    act(() => {
      root.render(<ComposerModelDialog {...props} groups={groups} loading={false} />);
    });

    expect(document.activeElement).toBe(searchInput());
    expect(activeModel()).toBe("deepseek-chat");
  });

  it("本地检索按模型名 / provider 名过滤，落空分组不留空壳", () => {
    render();

    typeSearch("openai");
    expect(query('[data-composer-model-provider="deepseek"]')).toBeNull();
    expect(query('[data-composer-model-provider="openai"]')).not.toBeNull();
    expect(container.querySelectorAll("[data-composer-model-option]")).toHaveLength(1);

    typeSearch("zzz");
    expect(query("[data-composer-model-dialog-no-match]")).not.toBeNull();
    // 「检索无匹配」不是「目录为空」：两种状态各说各话。
    expect(query("[data-composer-model-dialog-empty]")).toBeNull();
    expect(container.querySelectorAll("[data-composer-model-option]")).toHaveLength(0);

    typeSearch("");
    expect(container.querySelectorAll("[data-composer-model-option]")).toHaveLength(3);
    expect(query("[data-composer-model-dialog-no-match]")).toBeNull();
  });

  it("方向键移动高亮（边界钳制）、Enter 应用高亮行", () => {
    const props = render();
    const input = searchInput();

    pressKey(input, "ArrowUp");
    expect(activeModel()).toBe("deepseek-chat");

    pressKey(input, "ArrowDown");
    expect(activeModel()).toBe("deepseek-reasoner");

    pressKey(input, "Enter");
    expect(props.onSelect).toHaveBeenCalledWith("deepseek-reasoner");
  });

  it("检索后 Enter 应用的是过滤结果里的高亮行", () => {
    const props = render();

    const input = typeSearch("gpt");
    pressKey(input, "Enter");

    expect(props.onSelect).toHaveBeenCalledWith("gpt-5");
    expect(props.onSelect).toHaveBeenCalledTimes(1);
  });
});
