// @vitest-environment jsdom
// P2-6 子片 1：排序接线层的行为测试（设置域偏好 + 浏览器本地账目）。

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SettingsProvider } from "@/core/settings";
import {
  APP_SETTINGS_STORAGE_KEY,
  mergeAppSettings,
} from "@/core/settings/local";
import {
  SESSION_ORDER_MODES,
  type SessionOrderCandidate,
} from "@/lib/workspace/session-order";
import { SESSION_ORDER_STORAGE_KEY } from "@/lib/workspace/session-order-store";

import { useSessionOrder, type SessionOrderController } from "./use-session-order";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function session(id: string, updatedAt: string): SessionOrderCandidate {
  return { id, updatedAt };
}

describe("useSessionOrder", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let controller: SessionOrderController | null;

  function setController(next: SessionOrderController) {
    controller = next;
  }

  function Probe({
    onReady,
  }: {
    onReady: (next: SessionOrderController) => void;
  }) {
    const current = useSessionOrder();
    useEffect(() => {
      onReady(current);
    }, [current, onReady]);
    return null;
  }

  beforeEach(() => {
    window.localStorage.clear();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    controller = null;
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderProbe() {
    act(() => {
      root?.render(
        <SettingsProvider>
          <Probe onReady={setController} />
        </SettingsProvider>,
      );
    });
  }

  function render() {
    renderProbe();
    expect(controller).not.toBeNull();
    return controller as unknown as SessionOrderController;
  }

  it("默认按最近更新排序", () => {
    const current = render();

    expect(current.mode).toBe("updated");
    expect(
      current
        .orderFor("dir", [
          session("a", "2026-01-01T00:00:00Z"),
          session("b", "2026-02-01T00:00:00Z"),
        ])
        .map((item) => item.id),
    ).toEqual(["b", "a"]);
  });

  it("切换模式写入设置偏好（跨挂载保留）", () => {
    const current = render();

    act(() => current.setMode("manual"));

    const stored = JSON.parse(
      window.localStorage.getItem(APP_SETTINGS_STORAGE_KEY) ?? "{}",
    ) as { workspace?: { sessionOrder?: string } };
    expect(stored.workspace?.sessionOrder).toBe("manual");
    expect(controller?.mode).toBe("manual");
  });

  it("手动模式按账目顺序，未入账会话按最近更新追加到末尾", () => {
    const current = render();
    act(() => current.setMode("manual"));

    act(() => current.commitOrder("dir", ["a"]));

    const ordered = controller?.orderFor("dir", [
      session("b", "2026-02-01T00:00:00Z"),
      session("a", "2026-01-01T00:00:00Z"),
    ]);
    expect((ordered ?? []).map((item) => item.id)).toEqual(["a", "b"]);
  });

  it("提交顺序写入本地账目，重复提交同一顺序不再写存储", () => {
    const current = render();
    const setItem = vi.spyOn(Storage.prototype, "setItem");

    act(() => current.commitOrder("dir", ["b", "a"]));

    const document = JSON.parse(
      window.localStorage.getItem(SESSION_ORDER_STORAGE_KEY) ?? "{}",
    ) as { version?: number; accounts?: Record<string, string[]> };
    expect(document).toEqual({ version: 1, accounts: { dir: ["b", "a"] } });

    const writesAfterFirstCommit = setItem.mock.calls.filter(
      ([key]) => key === SESSION_ORDER_STORAGE_KEY,
    ).length;
    act(() => controller?.commitOrder("dir", ["b", "a"]));
    expect(
      setItem.mock.calls.filter(([key]) => key === SESSION_ORDER_STORAGE_KEY)
        .length,
    ).toBe(writesAfterFirstCommit);

    setItem.mockRestore();
  });

  it("两种模式切换不丢手动顺序（验收：同一次挂载往返）", () => {
    const current = render();
    act(() => current.setMode("manual"));
    act(() => current.commitOrder("dir", ["a", "b"]));

    const sessions = [
      session("a", "2026-01-01T00:00:00Z"),
      session("b", "2026-03-01T00:00:00Z"),
    ];
    expect(controller?.orderFor("dir", sessions).map((item) => item.id)).toEqual([
      "a",
      "b",
    ]);

    act(() => controller?.setMode("updated"));
    expect(controller?.orderFor("dir", sessions).map((item) => item.id)).toEqual([
      "b",
      "a",
    ]);

    act(() => controller?.setMode("manual"));
    expect(controller?.orderFor("dir", sessions).map((item) => item.id)).toEqual([
      "a",
      "b",
    ]);
  });

  it("账目在重新挂载后从本地存储恢复", () => {
    const current = render();
    act(() => current.setMode("manual"));
    act(() => current.commitOrder("dir", ["b", "a"]));

    act(() => root?.unmount());
    root = createRoot(container);
    renderProbe();

    expect(controller?.mode).toBe("manual");
    expect(controller?.accounts.dir).toEqual(["b", "a"]);
  });

  it("设置规范器与排序模式的取值集合一致（防口径漂移）", () => {
    for (const mode of SESSION_ORDER_MODES) {
      expect(
        mergeAppSettings({ workspace: { sessionOrder: mode } }).workspace
          .sessionOrder,
      ).toBe(mode);
    }
    expect(
      mergeAppSettings({ workspace: { sessionOrder: "recency" as never } })
        .workspace.sessionOrder,
    ).toBe("updated");
  });
});
