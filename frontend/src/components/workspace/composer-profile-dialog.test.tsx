// @vitest-environment jsdom

// Batch 12：`/profile` 弹窗组件单测（口径对齐 `composer-model-dialog.test.tsx`）。
//
// 覆盖口径（只断言稳定 data 钩子与行为，不绑定样式）：
// - 关闭时不渲染；
// - 目录未就绪的三种形态各自如实呈现：加载中 / 失败（带原因，可重试）/ 空目录；
// - 只列**可解析**的 profile：解析失败的条目不在这里伪装成可选（回执里如实说明）；
// - 点选与键盘（方向键 + Enter）都把 ref 原文交回宿主处理器，弹窗自己不执行切换；
// - 检索是本地行为（空查询原样、无匹配与空目录是两种状态）。

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

import { ComposerProfileDialog } from "./composer-profile-dialog";
import type { ComposerProfileCandidate } from "@/lib/composer-profile-options";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const candidates: ComposerProfileCandidate[] = [
  {
    ref: "review",
    label: "Review",
    layer: "user",
    isDefault: true,
    valid: true,
    invalidReason: "",
  },
  {
    ref: "ops",
    label: "Ops",
    layer: "project",
    isDefault: false,
    valid: true,
    invalidReason: "",
  },
  {
    ref: "broken",
    label: "Broken",
    layer: "project",
    isDefault: false,
    valid: false,
    invalidReason: "yaml: mapping values are not allowed here",
  },
];

describe("ComposerProfileDialog", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    // jsdom 未实现 scrollIntoView；弹窗对无该能力已做守卫，这里仍补一个空实现。
    if (!Element.prototype.scrollIntoView) {
      Element.prototype.scrollIntoView = () => {};
    }
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(
    overrides: Partial<Parameters<typeof ComposerProfileDialog>[0]> = {},
  ) {
    const props = {
      candidates,
      error: null as string | null,
      loading: false,
      onClose: vi.fn(),
      onRetry: undefined as (() => void) | undefined,
      onSelect: vi.fn(),
      open: true,
      ...overrides,
    };
    act(() => {
      root.render(<ComposerProfileDialog {...props} />);
    });
    return props;
  }

  function query(selector: string): Element | null {
    return container.querySelector(selector);
  }

  function options(): NodeListOf<Element> {
    return container.querySelectorAll("[data-composer-profile-option]");
  }

  function searchInput(): HTMLInputElement {
    const input = container.querySelector<HTMLInputElement>(
      "[data-composer-profile-search]",
    );
    if (!input) {
      throw new Error("search input missing");
    }
    return input;
  }

  function typeSearch(value: string): void {
    const input = searchInput();
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )?.set;
    setter?.call(input, value);
    act(() => {
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  function pressKey(target: Element, key: string): void {
    act(() => {
      target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    });
  }

  it("open=false 时不渲染", () => {
    render({ open: false });

    expect(query("[data-composer-profile-search]")).toBeNull();
  });

  it("只列可解析的 profile，并标记默认项（解析失败项不在这里伪装成可选）", () => {
    render();

    const refs = Array.from(options()).map((option) =>
      option.getAttribute("data-composer-profile-option"),
    );
    expect(refs).toEqual(["review", "ops"]);
    expect(query('[data-composer-profile-option="broken"]')).toBeNull();

    // 默认徽标只出现在 isDefault 项上。
    const defaultRow = query('[data-composer-profile-option="review"]');
    expect(defaultRow?.textContent).toContain("defaultBadge");
    expect(query('[data-composer-profile-option="ops"]')?.textContent).not.toContain(
      "defaultBadge",
    );
  });

  it("点选把 ref 原文交回宿主并关闭弹窗（弹窗自己不执行切换）", () => {
    const props = render();

    act(() => {
      (query('[data-composer-profile-option="ops"]') as HTMLButtonElement).click();
    });

    expect(props.onSelect).toHaveBeenCalledWith("ops");
    expect(props.onClose).toHaveBeenCalledTimes(1);
  });

  it("方向键 + Enter 选择高亮行（首行预高亮）", () => {
    const props = render();

    const input = searchInput();
    expect(query('[data-composer-profile-option-active="true"]')?.getAttribute(
      "data-composer-profile-option",
    )).toBe("review");

    pressKey(input, "ArrowDown");
    expect(query('[data-composer-profile-option-active="true"]')?.getAttribute(
      "data-composer-profile-option",
    )).toBe("ops");

    pressKey(input, "Enter");
    expect(props.onSelect).toHaveBeenCalledWith("ops");
  });

  // Escape 由 DialogOverlay 的窗口级监听（use-dialog-lifecycle）独占处理；
  // 这里钉住「单次触发」——输入框若再处理一次会双调 onClose。
  it("Escape 关闭弹窗（单次触发）", () => {
    const props = render();

    pressKey(searchInput(), "Escape");

    expect(props.onClose).toHaveBeenCalledTimes(1);
    expect(props.onSelect).not.toHaveBeenCalled();
  });

  it("本地检索：命中 label / ref / layer，未命中与空目录是两种状态", () => {
    render();

    typeSearch("proj");
    const refs = Array.from(options()).map((option) =>
      option.getAttribute("data-composer-profile-option"),
    );
    expect(refs).toEqual(["ops"]);

    typeSearch("nope");
    expect(options()).toHaveLength(0);
    expect(query('[data-composer-profile-dialog-empty-state="no-match"]')).not.toBeNull();
    expect(query('[data-composer-profile-dialog-empty-state="empty"]')).toBeNull();
  });

  it("加载中显示加载态（不显示空态）", () => {
    render({ candidates: [], loading: true });

    expect(query("[data-composer-profile-dialog-loading]")).not.toBeNull();
    expect(query('[data-composer-profile-dialog-empty-state="empty"]')).toBeNull();
  });

  it("失败时显示错误与原因，重试按钮交回宿主（缺省不给假按钮）", () => {
    const onRetry = vi.fn();
    render({ candidates: [], error: "catalog request failed", onRetry });

    expect(query("[data-composer-profile-dialog-error]")?.textContent).toContain(
      "catalog request failed",
    );
    expect(query('[data-composer-profile-dialog-empty-state="empty"]')).toBeNull();

    const retry = query("[data-composer-profile-dialog-error] button");
    act(() => {
      (retry as HTMLButtonElement).click();
    });
    expect(onRetry).toHaveBeenCalledTimes(1);

    // 未提供 onRetry 时只呈现错误，不渲染重试按钮。
    const second = render({ candidates: [], error: "again", onRetry: undefined });
    expect(second.onRetry).toBeUndefined();
    expect(query("[data-composer-profile-dialog-error] button")).toBeNull();
  });

  it("目录为空且非加载/失败时显示空态（不补占位 profile）", () => {
    render({ candidates: [] });

    expect(query('[data-composer-profile-dialog-empty-state="empty"]')).not.toBeNull();
    expect(options()).toHaveLength(0);
  });
});
