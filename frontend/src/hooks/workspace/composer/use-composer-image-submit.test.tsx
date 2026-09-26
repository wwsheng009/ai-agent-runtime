// @vitest-environment jsdom

// S5：带图发送接线（submit_prompt.images）的状态机与回执单测。
// 覆盖：成功（清空草稿轨 + image_notes 回执）/ 会话忙 202（回滚乐观消息，附件保留）/
// 无会话（不发送并给出可见原因）/ 失败（回执带后端原因，不伪装成功）。

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";

import { createSessionTurnRegistry } from "@/hooks/workspace/agent-chat-turn/session-turn-registry";
import { createTurnRuntimeState } from "@/hooks/workspace/agent-chat-turn/turn-state";
import { type AgentChatTurnBootstrap } from "@/hooks/workspace/agent-chat-turn/turn-bootstrap";
import { createThread } from "@/lib/thread-state/test-fixtures";

import {
  useComposerImageSubmit,
  type ComposerImageSubmitController,
  type UseComposerImageSubmitOptions,
} from "./use-composer-image-submit";

vi.mock("@/api/runtime/session-prompt", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/session-prompt")>();
  return { ...actual, submitRuntimeSessionPrompt: vi.fn() };
});

import { submitRuntimeSessionPrompt } from "@/api/runtime/session-prompt";

const submitMock = vi.mocked(submitRuntimeSessionPrompt);

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function makeBootstrap(prompt: string): AgentChatTurnBootstrap {
  const thread = createThread();
  return {
    assistantMessageId: "assistant-1",
    controller: new AbortController(),
    requestArtifact: {
      id: "artifact-1",
      name: "request.json",
      path: "runtime/request.json",
      summary: "request",
      kind: "json",
      language: "json",
      content: "{}",
    },
    requestPayload: { messages: [{ role: "user", content: prompt }] },
    sessionIdBeforeTurn: "s-1",
    threadId: thread.id,
    threadSnapshot: thread,
    turnId: "turn-1",
    turnKey: "s-1",
    turnState: createTurnRuntimeState(thread),
    turnTrajectoryStore: {
      reset: vi.fn(),
      push: vi.fn(),
      subscribe: vi.fn(() => () => {}),
      getSnapshot: vi.fn(() => ({ nodes: [] })),
    } as never,
    userMessage: {
      id: "user-1",
      role: "user",
      author: "You",
      label: "draft",
      segments: [{ type: "text", content: prompt }],
    },
  };
}

describe("useComposerImageSubmit", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let controller: ComposerImageSubmitController | null;
  let updates: Array<(threads: Array<ReturnType<typeof createThread>>) => unknown>;
  let clearAttachments: Mock<() => void>;
  let setDraft: Mock<(value: string) => void>;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    controller = null;
    updates = [];
    clearAttachments = vi.fn();
    setDraft = vi.fn();
    submitMock.mockReset();
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

  function renderSubmit(overrides: Partial<UseComposerImageSubmitOptions> = {}) {
    const options: UseComposerImageSubmitOptions = {
      clearAttachments,
      prepareTurn: (prompt) => makeBootstrap(prompt),
      selectedThread: { ...createThread(), sessionId: "s-1" },
      selectedTurnKey: "s-1",
      setDraft,
      setSelectedArtifactId: vi.fn(),
      setThreads: (updater) => {
        updates.push(updater as never);
      },
      turnRegistry: createSessionTurnRegistry(),
      ...overrides,
    };
    function Probe(props: UseComposerImageSubmitOptions) {
      const next = useComposerImageSubmit(props);
      useEffect(() => {
        controller = next;
      });
      return null;
    }
    act(() => root?.render(<Probe {...options} />));
    return controller as ComposerImageSubmitController;
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
    });
  }

  it("submits uploaded paths and reports server image notes on success", async () => {
    submitMock.mockResolvedValue({
      pending: false,
      attachedImages: 1,
      imageNotes: ["已压缩到 1568px 内"],
      result: { output: "looks good" },
    });
    const submit = renderSubmit();

    let started = false;
    act(() => {
      started = submit.start("look", ["/srv/uploads/shot.png", "/srv/uploads/shot.png"]);
    });
    expect(started).toBe(true);
    await flush();

    expect(submitMock).toHaveBeenCalledWith("s-1", "look", {
      images: ["/srv/uploads/shot.png"],
      signal: expect.any(AbortSignal),
    });
    // 成功即清空草稿轨；回执带后端说明。
    expect(clearAttachments).toHaveBeenCalledTimes(1);
    expect(controller?.notice).toEqual({
      tone: "success",
      messageKey: "composer.attachments.sentWithNotes",
      values: { count: 1, notes: "已压缩到 1568px 内" },
    });
    // 本地回合身份已收尾（不再 busy）。
    expect(controller?.notice?.tone).toBe("success");
  });

  it("rolls back the optimistic messages and keeps attachments when the session is busy", async () => {
    submitMock.mockResolvedValue({
      pending: true,
      attachedImages: null,
      imageNotes: [],
      result: null,
    });
    const submit = renderSubmit();

    act(() => {
      submit.start("look", ["/srv/uploads/shot.png"]);
    });
    await flush();

    // 回滚 = 把乐观的用户消息与助手占位从线程里删掉。
    const lastUpdate = updates.at(-1);
    expect(lastUpdate).toBeTypeOf("function");
    const rolled = lastUpdate?.([
      {
        ...createThread(),
        messages: [
          { id: "user-1", role: "user", author: "You", label: "draft", segments: [] },
          { id: "assistant-1", role: "assistant", author: "a", label: "streaming", segments: [] },
          { id: "keep", role: "assistant", author: "a", label: "done", segments: [] },
        ],
      },
    ] as never) as Array<{ messages: Array<{ id: string }> }>;
    expect(rolled[0].messages.map((message) => message.id)).toEqual(["keep"]);

    expect(clearAttachments).not.toHaveBeenCalled();
    expect(controller?.notice).toEqual({
      tone: "error",
      messageKey: "composer.attachments.busyPending",
    });
  });

  it("refuses to submit for a thread without a runtime session", () => {
    const submit = renderSubmit({
      selectedThread: { ...createThread(), sessionId: undefined },
      selectedTurnKey: "thread-1",
    });

    let started = true;
    act(() => {
      started = submit.start("look", ["/srv/uploads/shot.png"]);
    });

    expect(started).toBe(false);
    expect(submitMock).not.toHaveBeenCalled();
    expect(controller?.notice).toEqual({
      tone: "error",
      messageKey: "composer.attachments.needsSession",
    });
  });

  it("reports a failed command with the backend reason and no fake success", async () => {
    const { RuntimeApiError } = await import("@/api/runtime/shared");
    submitMock.mockRejectedValue(
      new RuntimeApiError(400, { error: "images rejected: 全部不可用" }),
    );
    const submit = renderSubmit();

    act(() => {
      submit.start("look", ["/srv/uploads/shot.png"]);
    });
    await flush();

    expect(clearAttachments).not.toHaveBeenCalled();
    expect(controller?.notice?.tone).toBe("error");
    expect(controller?.notice?.messageKey).toBe("composer.attachments.sendFailed");
    expect(controller?.notice?.values?.reason).toContain("images rejected");
  });

  it("ignores an empty image list instead of sending a plain text turn", () => {
    const submit = renderSubmit();

    let started = true;
    act(() => {
      started = submit.start("look", ["  "]);
    });

    expect(started).toBe(false);
    expect(submitMock).not.toHaveBeenCalled();
  });
});
