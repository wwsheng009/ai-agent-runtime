// @vitest-environment jsdom

// P2-1A：会话检索弹层交互单测（提交筛选 / 命中行 / 空态 / 上限 / 降级与重试）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import type { RuntimeSessionSearchResponse } from "@/types/runtime";

import { SessionSearchDialog } from "./session-search-dialog";

const { searchRuntimeSessionsMock } = vi.hoisted(() => ({
  searchRuntimeSessionsMock: vi.fn(),
}));

vi.mock("@/api/runtime/session-search", async () => {
  const actual =
    await vi.importActual<typeof import("@/api/runtime/session-search")>(
      "@/api/runtime/session-search",
    );
  return { ...actual, searchRuntimeSessions: searchRuntimeSessionsMock };
});

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

function makeResponse(
  overrides: Partial<RuntimeSessionSearchResponse> = {},
): RuntimeSessionSearchResponse {
  return {
    sessions: [
      {
        id: "sess-support",
        state: "archived",
        createdAt: "2026-09-13T09:00:00.000Z",
        updatedAt: "2026-09-13T10:00:00.000Z",
        metadata: { title: "支持会话", tags: ["support", "billing"] },
      },
    ],
    count: 1,
    filters: { tags: ["support"], limit: 50, offset: 0 },
    ...overrides,
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

function setSelectValue(select: HTMLSelectElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLSelectElement.prototype,
    "value",
  )?.set;
  setter?.call(select, value);
  select.dispatchEvent(new Event("change", { bubbles: true }));
}

describe("SessionSearchDialog", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  const onClose = vi.fn();
  const onSelectSession = vi.fn();

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    onClose.mockReset();
    onSelectSession.mockReset();
    searchRuntimeSessionsMock.mockReset();
    searchRuntimeSessionsMock.mockResolvedValue(makeResponse());
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function renderDialog() {
    await act(async () => {
      root?.render(
        <SessionSearchDialog
          onClose={onClose}
          onSelectSession={onSelectSession}
          open
          users={[{ user_id: "web-console", session_count: 3 }]}
        />,
      );
    });
    await act(flush);
  }

  function tagInput() {
    return document.body.querySelector(
      'input[aria-label="标签"]',
    ) as HTMLInputElement | null;
  }

  function stateSelect() {
    return document.body.querySelector(
      'select[aria-label="状态"]',
    ) as HTMLSelectElement | null;
  }

  async function submitSearch() {
    const submitButton = Array.from(
      document.body.querySelectorAll("button"),
    ).find((button) => button.textContent?.includes("检索"));
    expect(submitButton).toBeInstanceOf(HTMLButtonElement);
    await act(async () => {
      submitButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await act(flush);
  }

  it("提交筛选后按服务端条件请求并渲染命中行，点击行回调会话 id", async () => {
    await renderDialog();
    expect(document.body.textContent).toContain("设置筛选条件后点击「检索」。");

    const input = tagInput();
    const select = stateSelect();
    expect(input).toBeInstanceOf(HTMLInputElement);
    expect(select).toBeInstanceOf(HTMLSelectElement);

    await act(async () => {
      if (input) {
        setInputValue(input, "support, billing");
      }
      if (select) {
        setSelectValue(select, "archived");
      }
    });

    await submitSearch();

    expect(searchRuntimeSessionsMock).toHaveBeenCalledWith(
      {
        tags: ["support", "billing"],
        state: "archived",
        userId: undefined,
        limit: 50,
        offset: 0,
      },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );

    const row = document.body.querySelector(
      '[data-testid="session-search-row"]',
    );
    expect(row).toBeInstanceOf(HTMLButtonElement);
    expect(document.body.textContent).toContain("支持会话");
    expect(document.body.textContent).toContain("命中 1 条会话");
    expect(row?.textContent).toContain("已归档");

    await act(async () => {
      row?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onSelectSession).toHaveBeenCalledWith("sess-support");
  });

  it("零命中时显示空态且不渲染结果行", async () => {
    searchRuntimeSessionsMock.mockResolvedValue(
      makeResponse({ sessions: [], count: 0 }),
    );
    await renderDialog();
    await submitSearch();

    expect(document.body.textContent).toContain("没有匹配的会话");
    expect(
      document.body.querySelector('[data-testid="session-search-row"]'),
    ).toBeNull();
  });

  it("命中数达到单页上限时给出细化提示", async () => {
    searchRuntimeSessionsMock.mockResolvedValue(
      makeResponse({
        sessions: [
          { id: "sess-a", state: "active" },
          { id: "sess-b", state: "active" },
        ],
        count: 2,
        filters: { limit: 2, offset: 0 },
      }),
    );
    await renderDialog();
    await submitSearch();

    const hint = document.body.querySelector(
      '[data-testid="session-search-limit-hint"]',
    );
    expect(hint?.textContent).toContain("已达单页上限 2 条");
  });

  it("503 存储不可用时如实提示降级并可重试", async () => {
    searchRuntimeSessionsMock.mockRejectedValueOnce(
      new RuntimeApiError(503, { error: "session store unavailable" }),
    );
    await renderDialog();
    await submitSearch();

    const errorBox = document.body.querySelector(
      '[data-testid="session-search-error"]',
    );
    expect(errorBox?.textContent).toContain("会话检索失败");
    expect(errorBox?.textContent).toContain("HTTP 503");
    expect(
      document.body.querySelector('[data-testid="session-search-row"]'),
    ).toBeNull();

    searchRuntimeSessionsMock.mockResolvedValueOnce(makeResponse());
    const retryButton = Array.from(
      errorBox?.querySelectorAll("button") ?? [],
    )[0];
    await act(async () => {
      retryButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await act(flush);

    expect(searchRuntimeSessionsMock).toHaveBeenCalledTimes(2);
    expect(document.body.textContent).toContain("支持会话");
  });

  it("非降级失败显示后端错误信息（不谎报不可用）", async () => {
    searchRuntimeSessionsMock.mockRejectedValueOnce(
      new RuntimeApiError(500, { error: "internal error" }),
    );
    await renderDialog();
    await submitSearch();

    const errorBox = document.body.querySelector(
      '[data-testid="session-search-error"]',
    );
    expect(errorBox?.textContent).toContain("internal error");
    expect(errorBox?.textContent).not.toContain("HTTP 500");
  });
});
