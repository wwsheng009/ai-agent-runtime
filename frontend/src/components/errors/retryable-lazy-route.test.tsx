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

describe("RetryableLazyRoute（chunk 加载失败面与手动重试）", () => {
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

// 路由切换回归：<Routes> 会在同一位置复用同一个 RetryableLazyRoute 实例
// （props 变、state 不变）。修复前会继续渲染上一条路由的页面，表现为
// 「URL 变了但页面没变」的假跳转；上一条路由的错误面同样会残留。
describe("RetryableLazyRoute（路由切换时按 loader 重建页面）", () => {
  it("loader 变化后渲染新页面，不残留上一条路由的内容", async () => {
    const workspaceLoader = vi.fn(async () => ({
      default: () => <p>工作台首页</p>,
    }));
    const configLoader = vi.fn(async () => ({
      default: () => <p>后端配置工作台</p>,
    }));

    await act(async () => {
      root.render(<RetryableLazyRoute loader={workspaceLoader} />);
    });
    expect(document.body.textContent).toContain("工作台首页");

    await act(async () => {
      root.render(<RetryableLazyRoute loader={configLoader} />);
    });

    expect(document.body.textContent).toContain("后端配置工作台");
    expect(document.body.textContent).not.toContain("工作台首页");
    expect(configLoader).toHaveBeenCalledTimes(1);
  });

  it("同一 loader 重复渲染时复用 lazy 身份，不重复加载", async () => {
    const loader = vi.fn(async () => ({ default: () => <p>技能市场</p> }));

    await act(async () => {
      root.render(
        <RetryableLazyRoute
          loader={loader}
          retryOptions={{ sleep: async () => {} }}
        />,
      );
    });

    // retryOptions 每次渲染都是新对象，但 lazy 组件身份只由 (loader, generation) 决定，
    // 否则每次渲染都会卸载重挂并重新请求 chunk。
    await act(async () => {
      root.render(
        <RetryableLazyRoute
          loader={loader}
          retryOptions={{ sleep: async () => {} }}
        />,
      );
    });

    expect(loader).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("技能市场");
  });

  it("切换路由后不再显示上一条路由的错误面", async () => {
    const brokenLoader = vi.fn(async () => {
      throw new Error("Failed to fetch dynamically imported module");
    });
    const freshLoader = vi.fn(async () => ({
      default: () => <p>日志页面</p>,
    }));
    const sleep = vi.fn(async () => {});

    await act(async () => {
      root.render(
        <RetryableLazyRoute loader={brokenLoader} retryOptions={{ sleep }} />,
      );
    });
    expect(document.querySelector('[role="alert"]')).not.toBeNull();

    await act(async () => {
      root.render(<RetryableLazyRoute loader={freshLoader} />);
    });

    expect(document.querySelector('[role="alert"]')).toBeNull();
    expect(document.body.textContent).toContain("日志页面");
  });
});
