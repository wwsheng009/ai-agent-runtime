// @vitest-environment jsdom
//
// 批次 4（§5.6）：分支编排 hook 的单测 —— 可用性门 / pending / 成功后的
// refresh→reset→navigate 编排 / 失败不改路由。

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { branchRuntimeSession } from "@/api/runtime/session-branch";

import { useSessionBranch } from "@/hooks/workspace/use-session-branch";

const navigate = vi.fn();

vi.mock("@/api/runtime/session-branch", () => ({
  branchRuntimeSession: vi.fn(),
}));

vi.mock("react-router-dom", () => ({
  useNavigate: () => navigate,
}));

const mockBranchRuntimeSession = vi.mocked(branchRuntimeSession);

type Controller = ReturnType<typeof useSessionBranch>;
type Options = Parameters<typeof useSessionBranch>[0];

let controller: Controller | null;

function Harness({
  onController,
  options,
}: {
  onController: (value: Controller) => void;
  options: Options;
}) {
  const api = useSessionBranch(options);
  // 捕获点放在 effect 里：渲染期写外部变量会触发 react-hooks/globals。
  useEffect(() => {
    onController(api);
  }, [api, onController]);
  return null;
}

function options(overrides: Partial<Options> = {}): Options {
  return {
    clientUserId: "client-user",
    onResetTrajectory: vi.fn(),
    refreshSessions: vi.fn(),
    selectedUserId: "",
    ...overrides,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, reject, resolve };
}

describe("useSessionBranch", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    controller = null;
    navigate.mockReset();
    mockBranchRuntimeSession.mockReset();
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    container.remove();
  });

  function render(opts: Options) {
    act(() => {
      root = createRoot(container);
      root.render(
        <Harness
          onController={(value) => {
            controller = value;
          }}
          options={opts}
        />,
      );
    });
  }

  it("sends the anchor and reuses the fork suffix for message-level branches", async () => {
    const order: string[] = [];
    const opts = options({
      onResetTrajectory: vi.fn(() => order.push("reset")),
      refreshSessions: vi.fn(() => order.push("refresh")),
      selectedUserId: "selected-user",
    });
    mockBranchRuntimeSession.mockResolvedValue({
      session: { id: "session-branch-1" },
    } as Awaited<ReturnType<typeof branchRuntimeSession>>);
    render(opts);

    await act(async () => {
      await controller?.branchFromMessage("session-1", "原会话", "msg_1");
    });

    expect(mockBranchRuntimeSession).toHaveBeenCalledWith("session-1", {
      anchor_message_id: "msg_1",
      include_anchor: true,
      title: "原会话（分支）",
      user_id: "selected-user",
    });
    expect(order).toEqual(["refresh", "reset"]);
    expect(navigate).toHaveBeenCalledWith(
      "/workspace/sessions/session-branch-1",
    );
    expect(controller?.branchError).toBeNull();
  });

  it("treats a missing anchor as a whole-session branch", async () => {
    const opts = options({ clientUserId: "client-user" });
    mockBranchRuntimeSession.mockResolvedValue({
      session: { id: "session-branch-2" },
    } as Awaited<ReturnType<typeof branchRuntimeSession>>);
    render(opts);

    await act(async () => {
      await controller?.forkSession("session-1", "  ");
    });

    expect(mockBranchRuntimeSession).toHaveBeenCalledWith("session-1", {
      title: "session-1（分支）",
      user_id: "client-user",
    });
  });

  it("keeps the route on failure and surfaces the localized reason", async () => {
    const opts = options();
    mockBranchRuntimeSession.mockRejectedValue(new Error("409 conflict"));
    render(opts);

    await act(async () => {
      await controller?.forkSession("session-1", "原会话");
    });

    expect(navigate).not.toHaveBeenCalled();
    expect(opts.refreshSessions).not.toHaveBeenCalled();
    expect(controller?.branchError).toContain("409 conflict");
  });

  it("exposes the pending anchor and drops concurrent requests", async () => {
    const opts = options();
    const gate = deferred<Awaited<ReturnType<typeof branchRuntimeSession>>>();
    mockBranchRuntimeSession.mockReturnValue(gate.promise);
    render(opts);

    let first: Promise<void> | undefined;
    act(() => {
      first = controller?.branchFromMessage("session-1", "原会话", "msg_1");
    });

    expect(controller?.branchPendingMessageId).toBe("msg_1");
    expect(controller?.branchPendingSessionId).toBe("session-1");

    await act(async () => {
      await controller?.branchFromMessage("session-1", "原会话", "msg_2");
    });
    expect(mockBranchRuntimeSession).toHaveBeenCalledTimes(1);

    await act(async () => {
      gate.resolve({
        session: { id: "session-branch-3" },
      } as Awaited<ReturnType<typeof branchRuntimeSession>>);
      await first;
    });

    expect(controller?.branchPendingMessageId).toBeNull();
    expect(controller?.branchPendingSessionId).toBeNull();
  });
});
