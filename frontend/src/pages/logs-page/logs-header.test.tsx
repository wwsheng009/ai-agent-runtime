// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { LogsHeaderSection } from "./logs-header";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function baseProps() {
  return {
    activeChips: [],
    adminToken: "",
    connectionState: "open" as const,
    copiedViewLink: false,
    error: null,
    filePath: "",
    follow: false,
    level: "" as const,
    logFileExists: true,
    onAdminTokenChange: vi.fn(),
    onClearAll: vi.fn(),
    onClearChip: vi.fn(),
    onCopyViewLink: vi.fn(),
    onFollowChange: vi.fn(),
    onLevelChange: vi.fn(),
    onQueryChange: vi.fn(),
    onRefresh: vi.fn(),
    query: "",
    refreshing: false,
    streamError: null,
  };
}

function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("LogsHeaderSection search draft", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
      root = null;
    }
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("URL 状态落后于输入时不回退已输入字符", () => {
    const props = baseProps();
    act(() => {
      root?.render(
        <MemoryRouter>
          <LogsHeaderSection {...props} />
        </MemoryRouter>,
      );
    });

    const input = container.querySelector<HTMLInputElement>("input:not([type])");
    if (!input) throw new Error("missing search input");

    act(() => {
      input.focus();
      setInputValue(input, "headless");
    });
    expect(input.value).toBe("headless");

    // 模拟 React Router 过渡期间 URL 仍然只追到前两个字符。
    act(() => {
      root?.render(
        <MemoryRouter>
          <LogsHeaderSection {...props} query="he" />
        </MemoryRouter>,
      );
    });
    expect(input.value).toBe("headless");

    act(() => {
      input.blur();
      root?.render(
        <MemoryRouter>
          <LogsHeaderSection {...props} query="" />
        </MemoryRouter>,
      );
    });
    expect(input.value).toBe("");
  });
});
