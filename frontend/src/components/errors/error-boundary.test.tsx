// @vitest-environment jsdom

import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { logger } from "@/core/logger";
import { ChunkLoadError } from "@/lib/lazy-retry";

import {
  GlobalErrorBoundary,
  PanelErrorBoundary,
  RouteErrorBoundary,
} from "./boundaries";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

function Boom({ message }: { message: string }): never {
  throw new Error(message);
}

describe("三层错误边界", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    // React 与 logger 都会向 console.error 输出，测试中静音。
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    vi.restoreAllMocks();
  });

  function render(node: ReactElement) {
    act(() => {
      root.render(node);
    });
  }

  it("全局边界渲染可见错误面，重试后恢复内容", async () => {
    let failing = true;
    function Flaky() {
      if (failing) {
        throw new Error("boom-global");
      }
      return <p>已恢复的界面</p>;
    }

    render(
      <GlobalErrorBoundary>
        <Flaky />
      </GlobalErrorBoundary>,
    );

    const alert = document.querySelector('[role="alert"]');
    expect(alert).not.toBeNull();
    expect(alert?.getAttribute("data-error-scope")).toBe("global");
    expect(alert?.textContent).toContain("页面出现错误");
    expect(alert?.textContent).toContain("boom-global");

    failing = false;
    await act(async () => {
      findButton("重试")?.click();
    });

    expect(document.querySelector('[role="alert"]')).toBeNull();
    expect(document.body.textContent).toContain("已恢复的界面");
  });

  it("全局边界的「返回首页」触发注入的恢复动作", () => {
    const onHome = vi.fn();

    render(
      <GlobalErrorBoundary onHome={onHome}>
        <Boom message="boom-home" />
      </GlobalErrorBoundary>,
    );

    act(() => {
      findButton("返回首页")?.click();
    });

    expect(onHome).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[role="alert"]')).not.toBeNull();
  });

  it("路由边界把普通错误呈现为页面加载失败错误面", () => {
    render(
      <RouteErrorBoundary>
        <Boom message="route-boom" />
      </RouteErrorBoundary>,
    );

    const alert = document.querySelector('[role="alert"]');
    expect(alert?.getAttribute("data-error-scope")).toBe("route");
    expect(alert?.textContent).toContain("页面加载失败");
    expect(alert?.textContent).toContain("route-boom");
  });

  it("路由边界把 chunk 加载失败呈现为可手动重试的错误面", () => {
    const onRetry = vi.fn();
    function ChunkBoom(): never {
      throw new ChunkLoadError(3, new Error("404 chunk missing"));
    }

    render(
      <RouteErrorBoundary onRetry={onRetry}>
        <ChunkBoom />
      </RouteErrorBoundary>,
    );

    const alert = document.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("资源加载失败");
    expect(alert?.textContent).toContain("3");

    act(() => {
      findButton("重新加载")?.click();
    });
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("面板边界局部化错误面，重试调用注入回调", () => {
    const onRetry = vi.fn();

    render(
      <PanelErrorBoundary onRetry={onRetry} title="会话用量">
        <Boom message="panel-boom" />
      </PanelErrorBoundary>,
    );

    const alert = document.querySelector('[role="alert"]');
    expect(alert?.getAttribute("data-error-scope")).toBe("panel");
    expect(alert?.textContent).toContain("会话用量");
    expect(alert?.textContent).toContain("这个面板暂时无法显示");

    act(() => {
      findButton("重试")?.click();
    });

    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("错误统一经 logger 出口上报（含 scope 上下文）", () => {
    const errorSpy = vi.spyOn(logger, "error").mockImplementation(() => {});

    render(
      <GlobalErrorBoundary>
        <Boom message="boom-log" />
      </GlobalErrorBoundary>,
    );

    expect(errorSpy).toHaveBeenCalledTimes(1);
    expect(errorSpy.mock.calls[0]?.[0]).toContain("error boundary");
    expect(errorSpy.mock.calls[0]?.[2]).toMatchObject({ scope: "global" });
  });
});
