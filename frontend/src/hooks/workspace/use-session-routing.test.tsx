// @vitest-environment jsdom

// useSessionRouting 的行为单测（区块级单测见 session-detail-routing-section.test.tsx）：
//   * 会话切换清空投影：新会话加载期间不显示上一个会话的数据；
//   * 在途写入的响应按 sid 判定丢弃——旧会话回包不得覆盖新会话、也不作废新会话的 GET；
//   * 写入成功后以服务端返回的投影为准。
//
// 网络层整模块 mock（`@/api/runtime/session-routing`），不依赖真实后端。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SessionRoutingResponse } from "@/types/runtime";

import { useSessionRouting } from "./use-session-routing";

const { getSessionRoutingMock, updateSessionRoutingMock } = vi.hoisted(() => ({
  getSessionRoutingMock: vi.fn(),
  updateSessionRoutingMock: vi.fn(),
}));

vi.mock("@/api/runtime/session-routing", () => ({
  getSessionRouting: getSessionRoutingMock,
  updateSessionRouting: updateSessionRoutingMock,
}));

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

function routingFixture(
  sessionId: string,
  model: string,
): SessionRoutingResponse {
  return {
    sessionId,
    scope: "main",
    targetLayer: "",
    targetPath: "",
    actorInvalidated: false,
    updated: false,
    routing: {
      schemaVersion: 1,
      enabled: true,
      level: "hard",
      provider: "opencode.ai",
      model,
      reasoning: "high",
      source: "session",
      disabled: false,
      warnings: [],
      revision: "",
      effectiveFrom: "next_turn",
    },
    subAgent: {
      schemaVersion: 1,
      enabled: false,
      level: "",
      provider: "",
      model: "",
      reasoning: "",
      source: "default",
      disabled: true,
      warnings: [],
      revision: "",
      effectiveFrom: "next_turn",
    },
    panel: {
      scope: "main",
      childSession: false,
      sessionOverride: false,
      workspaceOverride: false,
      configOverride: false,
      workspacePath: "E:/ws",
      workspacePrefsPath: "E:/ws/.aicli/chat-prefs.yaml",
      configPath: "C:/Users/vince/.aicli/config.yaml",
      configLayer: "user",
      writableLayers: ["session", "workspace", "config"],
      levels: [],
      subAgent: null,
    },
    warnings: [],
  };
}

function Harness({ sessionId }: { sessionId: string }) {
  const { snapshot, error, saving, write } = useSessionRouting(sessionId);
  return (
    <div>
      <span data-testid="snapshot-sid">{snapshot?.sessionId ?? ""}</span>
      <span data-testid="snapshot-model">{snapshot?.routing.model ?? ""}</span>
      <span data-testid="error">{error ?? ""}</span>
      <span data-testid="saving">{saving ? "1" : "0"}</span>
      <button
        data-testid="write"
        onClick={() => {
          void write({
            targetLayer: "session",
            mainAgent: { profiles: { hard: { model: "next-model" } } },
          });
        }}
        type="button"
      >
        write
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

function click(testId: string) {
  const node = document.body.querySelector<HTMLElement>(
    `[data-testid="${testId}"]`,
  );
  if (!node) {
    throw new Error(`missing node: ${testId}`);
  }
  act(() => {
    node.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

async function render(sessionId: string) {
  await act(async () => {
    root?.render(<Harness sessionId={sessionId} />);
  });
  await act(async () => {
    await flush();
  });
}

describe("useSessionRouting", () => {
  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    getSessionRoutingMock.mockReset();
    updateSessionRoutingMock.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container?.remove();
    container = null;
    root = null;
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("会话切换立即丢弃上一个会话的投影（加载期间不显示旧数据）", async () => {
    getSessionRoutingMock.mockResolvedValueOnce(
      routingFixture("session-1", "model-1"),
    );
    await render("session-1");
    expect(text("snapshot-model")).toBe("model-1");

    const pending = createDeferred<SessionRoutingResponse>();
    getSessionRoutingMock.mockReturnValueOnce(pending.promise);
    await render("session-2");

    // 新会话的 GET 还没回来：旧会话数据必须已经不可见。
    expect(text("snapshot-sid")).toBe("");
    expect(text("snapshot-model")).toBe("");

    await act(async () => {
      pending.resolve(routingFixture("session-2", "model-2"));
      await flush();
    });
    expect(text("snapshot-sid")).toBe("session-2");
    expect(text("snapshot-model")).toBe("model-2");
  });

  it("在途写入的响应属于已切走的会话：丢弃且不覆盖新会话", async () => {
    getSessionRoutingMock.mockResolvedValueOnce(
      routingFixture("session-1", "model-1"),
    );
    await render("session-1");

    const writeDeferred = createDeferred<SessionRoutingResponse>();
    updateSessionRoutingMock.mockReturnValueOnce(writeDeferred.promise);
    click("write");
    await act(async () => {
      await flush();
    });
    expect(text("saving")).toBe("1");

    // 写入在途时切到 session-2，并让新会话的 GET 先落地。
    getSessionRoutingMock.mockResolvedValueOnce(
      routingFixture("session-2", "model-2"),
    );
    await render("session-2");
    expect(text("snapshot-model")).toBe("model-2");

    // 旧会话的写入响应迟到：不得覆盖新会话界面。
    await act(async () => {
      writeDeferred.resolve(routingFixture("session-1", "written-1"));
      await flush();
    });

    expect(text("snapshot-sid")).toBe("session-2");
    expect(text("snapshot-model")).toBe("model-2");
    expect(text("error")).toBe("");
  });

  it("写入成功后以服务端返回的投影为准", async () => {
    getSessionRoutingMock.mockResolvedValueOnce(
      routingFixture("session-1", "model-1"),
    );
    await render("session-1");

    updateSessionRoutingMock.mockResolvedValueOnce(
      routingFixture("session-1", "written-1"),
    );
    click("write");
    await act(async () => {
      await flush();
    });

    expect(text("snapshot-model")).toBe("written-1");
    expect(text("saving")).toBe("0");
  });
});
