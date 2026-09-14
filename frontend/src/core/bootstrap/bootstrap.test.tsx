// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  STARTUP_READY_ATTRIBUTE,
  StartupReadinessError,
  collectStartupReadinessIssues,
  hasVisibleFailureSurface,
  markStartupReady,
  renderStartupFailure,
  resolveRootElement,
  waitForStartupReadiness,
} from "./index";

describe("启动失败面（#root 缺失 / 启动抛错）", () => {
  beforeEach(() => {
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.documentElement.removeAttribute(STARTUP_READY_ATTRIBUTE);
    document.body.innerHTML = "";
  });

  it("#root 缺失时把可见错误面挂到 body，且「重新加载」可点击", () => {
    const onReload = vi.fn();

    renderStartupFailure({
      error: new Error("missing #root mount node"),
      onReload,
    });

    const alert = document.querySelector('[role="alert"]');
    expect(alert).not.toBeNull();
    expect(alert?.getAttribute("data-startup-failure")).toBe("true");
    expect(alert?.textContent).toContain("应用启动失败");
    expect(alert?.textContent).toContain("missing #root mount node");

    const reload = Array.from(document.querySelectorAll("button")).find(
      (button) => button.textContent?.includes("重新加载"),
    );
    expect(reload).toBeDefined();
    reload?.click();
    expect(onReload).toHaveBeenCalledTimes(1);
  });

  it("存在 #root 时错误面替换其内容，避免与半加载界面叠加", () => {
    const root = document.createElement("div");
    root.id = "root";
    root.innerHTML = "<p>stale-view</p>";
    document.body.appendChild(root);

    renderStartupFailure({ error: new Error("bootstrap exploded") });

    expect(root.querySelector('[role="alert"]')).not.toBeNull();
    expect(root.textContent).not.toContain("stale-view");
  });

  it("resolveRootElement 对缺失的 #root 抛错", () => {
    expect(() => resolveRootElement(document)).toThrow(/missing #root/);
  });

  it("hasVisibleFailureSurface 识别错误边界已渲染的错误面", () => {
    expect(hasVisibleFailureSurface(document)).toBe(false);

    const surface = document.createElement("div");
    surface.setAttribute("role", "alert");
    document.body.appendChild(surface);

    expect(hasVisibleFailureSurface(document)).toBe(true);
  });
});

describe("启动完整性检查", () => {
  beforeEach(() => {
    // 超时路径会经 logger 输出错误，属于被测行为，测试中静音。
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.documentElement.removeAttribute(STARTUP_READY_ATTRIBUTE);
    document.body.innerHTML = "";
  });

  it("根节点有内容 + 应用壳就绪标记 + i18n 就绪时通过", async () => {
    const root = document.createElement("div");
    root.id = "root";
    root.appendChild(document.createElement("div"));
    document.body.appendChild(root);
    markStartupReady(document);

    await expect(
      waitForStartupReadiness({ isI18nReady: () => true, timeoutMs: 50 }),
    ).resolves.toBeUndefined();
    expect(collectStartupReadinessIssues(document, () => true)).toEqual([]);
  });

  it("未就绪时超时报错并列出缺失项（走可见错误面）", async () => {
    await expect(
      waitForStartupReadiness({ isI18nReady: () => false, timeoutMs: 0 }),
    ).rejects.toBeInstanceOf(StartupReadinessError);

    const issues = collectStartupReadinessIssues(document, () => false);
    expect(issues).toContain("app-shell");
    expect(issues).toContain("i18n");
  });
});
