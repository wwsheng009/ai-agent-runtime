// @vitest-environment jsdom

// useSessionDetail 的行为单测（面级单测见 session-detail-surface.test.tsx）：
//   * 会话切换 / 卸载时作废在途请求——旧会话的迟到回包不得落到新会话上；
//   * 页面重新可见时静默重取（不回落 loading，避免回到前台闪一下加载态）；
//   * 手动刷新保留旧数据并在点击事件内标记「刷新中」；
//   * 无会话 id 完全不发请求。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type RuntimeSessionRecord } from "@/types/runtime";

import { useSessionDetail } from "./use-session-detail";

const { getRuntimeSessionMock } = vi.hoisted(() => ({
  getRuntimeSessionMock: vi.fn(),
}));

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return { ...actual, getRuntimeSession: getRuntimeSessionMock };
});

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason: unknown) => void;
};

function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolveFn, rejectFn) => {
    resolve = resolveFn;
    reject = rejectFn;
  });
  return { promise, resolve, reject };
}

function sessionFixture(
  overrides: Partial<RuntimeSessionRecord> = {},
): RuntimeSessionRecord {
  return {
    id: "session-1",
    userId: "alice",
    state: "active",
    metadata: { title: "详情会话" },
    createdAt: "2026-09-16T02:00:00.000Z",
    updatedAt: "2026-09-16T03:00:00.000Z",
    ...overrides,
  };
}

function Harness({ sessionId }: { sessionId: string }) {
  const { error, reload, session, status } = useSessionDetail(sessionId);
  return (
    <div>
      <span data-testid="status">{status}</span>
      <span data-testid="session">{session?.id ?? ""}</span>
      <span data-testid="error">{error ?? ""}</span>
      <button data-testid="reload" onClick={reload} type="button">
        reload
      </button>
    </div>
  );
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

function text(testId: string) {
  return (
    document.body.querySelector(`[data-testid="${testId}"]`)?.textContent ?? ""
  );
}

async function render(sessionId: string) {
  await act(async () => {
    root?.render(<Harness sessionId={sessionId} />);
  });
  await act(async () => {
    await flush();
  });
}

describe("useSessionDetail", () => {
  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    getRuntimeSessionMock.mockReset();
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container?.remove();
    container = null;
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("会话切换后丢弃旧会话的迟到回包", async () => {
    const stale = createDeferred<{ session: RuntimeSessionRecord }>();
    getRuntimeSessionMock.mockImplementation((id: string) =>
      id === "session-a"
        ? stale.promise
        : Promise.resolve({ session: sessionFixture({ id: "session-b" }) }),
    );

    await render("session-a");
    expect(text("status")).toBe("loading");

    await render("session-b");
    expect(text("session")).toBe("session-b");

    // 旧会话的响应此刻才回来：不得覆盖新会话的数据。
    await act(async () => {
      stale.resolve({ session: sessionFixture({ id: "session-a" }) });
      await flush();
    });

    expect(text("session")).toBe("session-b");
    expect(text("status")).toBe("ready");
  });

  it("页面重新可见时静默重取，不回落 loading", async () => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    getRuntimeSessionMock
      .mockResolvedValueOnce({ session: sessionFixture({ state: "active" }) })
      .mockResolvedValueOnce({ session: sessionFixture({ state: "closed" }) });

    await render("session-1");
    expect(text("status")).toBe("ready");

    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });

    // 事件同步段即刻可读：仍是 ready（静默重取），拿到新快照后才更新内容。
    expect(text("status")).toBe("ready");

    await act(async () => {
      await flush();
    });

    expect(getRuntimeSessionMock).toHaveBeenCalledTimes(2);
    expect(text("session")).toBe("session-1");
  });

  it("手动刷新保留旧数据并标记刷新中", async () => {
    const pending = createDeferred<{ session: RuntimeSessionRecord }>();
    getRuntimeSessionMock
      .mockResolvedValueOnce({ session: sessionFixture() })
      .mockReturnValueOnce(pending.promise);

    await render("session-1");
    expect(text("status")).toBe("ready");

    await act(async () => {
      document.body
        .querySelector<HTMLButtonElement>('[data-testid="reload"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(text("status")).toBe("loading");
    // 刷新期间保留上一份快照，避免字段整块闪没。
    expect(text("session")).toBe("session-1");

    await act(async () => {
      pending.resolve({ session: sessionFixture({ state: "archived" }) });
      await flush();
    });

    expect(text("status")).toBe("ready");
  });

  it("无会话 id 不发请求，reload 也为空操作", async () => {
    await render("");

    expect(getRuntimeSessionMock).not.toHaveBeenCalled();
    expect(text("status")).toBe("idle");

    await act(async () => {
      document.body
        .querySelector<HTMLButtonElement>('[data-testid="reload"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await flush();
    });

    expect(getRuntimeSessionMock).not.toHaveBeenCalled();
    expect(text("status")).toBe("idle");
  });
});
