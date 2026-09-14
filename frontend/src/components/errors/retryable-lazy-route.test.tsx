// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RetryableLazyRoute } from "./retryable-lazy-route";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

describe("RetryableLazyRoute（chunk 加载失败面与手动重试）", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    vi.restoreAllMocks();
  });

  it("自动重试到达上限后显示错误面，手动重试成功后渲染页面", async () => {
    let calls = 0;
    const loader = vi.fn(async () => {
      calls += 1;
      if (calls <= 3) {
        throw new Error("Failed to fetch dynamically imported module");
      }
      return { default: () => <p>懒加载页面</p> };
    });
    const sleep = vi.fn(async () => {});

    await act(async () => {
      root.render(
        <RetryableLazyRoute
          fallback={<p>加载中…</p>}
          loader={loader}
          retryOptions={{ sleep }}
        />,
      );
    });

    // 自动尝试恰好 3 次（首次 + 2 次退避重试），失败后进入可见错误面。
    expect(loader).toHaveBeenCalledTimes(3);
    const alert = document.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("资源加载失败");
    expect(alert?.textContent).toContain("3");

    await act(async () => {
      findButton("重新加载")?.click();
    });

    // 手动重试重新发起一轮受上限约束的加载，成功后错误面消失。
    expect(loader).toHaveBeenCalledTimes(4);
    expect(document.querySelector('[role="alert"]')).toBeNull();
    expect(document.body.textContent).toContain("懒加载页面");
  });

  it("手动重试仍失败时每轮只尝试到上限，不出现自动死循环", async () => {
    const loader = vi.fn(async () => {
      throw new Error("Failed to fetch dynamically imported module");
    });
    const sleep = vi.fn(async () => {});

    await act(async () => {
      root.render(
        <RetryableLazyRoute loader={loader} retryOptions={{ sleep }} />,
      );
    });

    expect(loader).toHaveBeenCalledTimes(3);
    expect(document.querySelector('[role="alert"]')).not.toBeNull();

    await act(async () => {
      findButton("重新加载")?.click();
    });

    expect(loader).toHaveBeenCalledTimes(6);
    expect(document.querySelector('[role="alert"]')?.textContent).toContain(
      "资源加载失败",
    );
  });
});
