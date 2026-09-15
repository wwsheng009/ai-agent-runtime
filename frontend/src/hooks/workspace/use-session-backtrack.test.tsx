// @vitest-environment jsdom

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ChatMessage, Thread } from "@/data/mock";
import {
  previewSessionBacktrack,
  type RuntimeSessionBacktrackMode,
  type RuntimeSessionBacktrackResult,
} from "@/lib/runtime-api";

import { useSessionBacktrack } from "@/hooks/workspace/use-session-backtrack";

import { MessageBacktrackDialog } from "@/components/workspace/message-backtrack-dialog";

vi.mock("@/lib/runtime-api", () => ({
  applySessionBacktrack: vi.fn(),
  getSessionHistory: vi.fn(),
  previewSessionBacktrack: vi.fn(),
}));

const mockPreview = vi.mocked(previewSessionBacktrack);

// 用户诉求（第 2 轮）：切换「还原模式」后，确认按钮必须仍然可点（预览结束后立刻恢复），
// 不能因为模式切换而永久卡在 pending / error。
function previewResult(mode: RuntimeSessionBacktrackMode): RuntimeSessionBacktrackResult {
  return {
    session_id: "session-1",
    mode,
    user_turn_index: 0,
    message_index: 0,
    truncated_to_message_count: 1,
    removed_message_count: 0,
    removed_user_turns: 0,
    anchor_preview: `${mode} preview`,
  };
}

function userMessage(id: string, text: string): ChatMessage {
  return {
    id,
    role: "user",
    author: "user",
    label: "You",
    segments: [{ type: "text", content: text }],
  };
}

function createThread(): Thread {
  return {
    id: "thread-1",
    title: "Thread",
    summary: "Summary",
    updatedAt: "2026-04-05T00:00:00Z",
    status: "active",
    sessionId: "session-1",
    transport: "live",
    lastError: null,
    tags: [],
    prompts: [],
    messages: [userMessage("message-1", "把右侧栏合并成一个面板")],
    artifacts: [],
  };
}

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type HarnessApi = ReturnType<typeof useSessionBacktrack>;

function Harness({
  onApi,
  thread,
}: {
  onApi: (api: HarnessApi) => void;
  thread: Thread;
}) {
  const api = useSessionBacktrack({
    applySessionHistoryToThread: (current) => current,
    isResponding: false,
    selectedThread: thread,
    setDraft: () => {},
    setThreads: () => {},
  });
  // 捕获点放在 effect 里：渲染期写外部变量会触发 react-hooks/globals。
  useEffect(() => {
    onApi(api);
  }, [api, onApi]);
  return (
    <MessageBacktrackDialog
      onApply={() => {
        void api.confirmBacktrack();
      }}
      onClose={api.closeBacktrackDialog}
      onEditPromptChange={api.setBacktrackEditPrompt}
      onModeChange={(mode) => {
        void api.setBacktrackMode(mode);
      }}
      onPrefillChange={api.setBacktrackPrefill}
      state={api.backtrackDialog}
    />
  );
}

describe("useSessionBacktrack 还原模式切换", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let latest: HarnessApi | null = null;

  const renderHarness = (thread: Thread) => {
    root = createRoot(container);
    root.render(<Harness onApi={(api) => { latest = api; }} thread={thread} />);
  };

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    latest = null;
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    mockPreview.mockReset();
    mockPreview.mockImplementation((async (_sessionId, request) => ({
      ok: true,
      result: previewResult(request.mode),
    })) as never);
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("切换还原模式后重新取预览，预览结束按钮即可用", async () => {
    await act(async () => {
      renderHarness(createThread());
    });

    await act(async () => {
      await latest!.backtrackToMessage("message-1", "conversation");
    });
    expect(latest!.backtrackDialog.previewing).toBe(false);
    expect(latest!.backtrackDialog.preview?.mode).toBe("conversation");

    // 同一次事件里先更新对话框其它字段，再切换还原模式：
    // 此时组件 fiber 上已有待处理更新，React 不会同步求值 updater，
    // 旧实现因此读不到 target 直接 return，把 previewing 永久留在 true（确认按钮不可点）。
    await act(async () => {
      latest!.setBacktrackPrefill(false);
      void latest!.setBacktrackMode("code");
      await Promise.resolve();
    });
    await act(async () => {});

    expect(mockPreview).toHaveBeenCalledTimes(2);
    expect(mockPreview.mock.calls[1]?.[1]?.mode).toBe("code");
    expect(latest!.backtrackDialog.mode).toBe("code");
    expect(latest!.backtrackDialog.previewing).toBe(false);
    expect(latest!.backtrackDialog.error).toBeNull();
    expect(latest!.backtrackDialog.preview?.mode).toBe("code");
  });

  it("切换模式后较早的预览响应不会覆盖新模式", async () => {
    const releases: Array<() => void> = [];
    mockPreview.mockImplementation((async (_sessionId, request) => {
      await new Promise<void>((resolve) => {
        releases.push(resolve);
      });
      return { ok: true, result: previewResult(request.mode) };
    }) as never);

    await act(async () => {
      renderHarness(createThread());
    });

    let opening: Promise<void> = Promise.resolve();
    await act(async () => {
      opening = latest!.backtrackToMessage("message-1", "conversation");
      await Promise.resolve();
    });
    await act(async () => {
      void latest!.setBacktrackMode("code");
      await Promise.resolve();
    });

    // 先返回较新的（code）响应，再返回较早的（conversation）响应。
    await act(async () => {
      releases[1]?.();
      await Promise.resolve();
    });
    await act(async () => {
      releases[0]?.();
      await opening;
    });

    expect(latest!.backtrackDialog.mode).toBe("code");
    expect(latest!.backtrackDialog.preview?.mode).toBe("code");
    expect(latest!.backtrackDialog.previewing).toBe(false);
  });

  it("点选还原模式后确认按钮只是暂时禁用，预览结束即恢复可点", async () => {
    const releases: Array<() => void> = [];
    mockPreview.mockImplementationOnce((async (_sessionId, request) => ({
      ok: true,
      result: previewResult(request.mode),
    })) as never);
    mockPreview.mockImplementationOnce((async (_sessionId, request) => {
      await new Promise<void>((resolve) => {
        releases.push(resolve);
      });
      return { ok: true, result: previewResult(request.mode) };
    }) as never);

    const confirmButton = () =>
      Array.from(document.querySelectorAll("button")).find((button) =>
        button.textContent?.includes("确认回溯"),
      );

    await act(async () => {
      renderHarness(createThread());
    });
    await act(async () => {
      await latest!.backtrackToMessage("message-1", "conversation");
    });
    expect(confirmButton()?.disabled).toBe(false);

    const codeInput = document.querySelector<HTMLInputElement>(
      'input[name="backtrack-mode"][value="code"]',
    );
    expect(codeInput).toBeTruthy();
    await act(async () => {
      codeInput?.click();
      await Promise.resolve();
    });
    // 预览在途只是暂时禁用，不能永久卡死。
    expect(confirmButton()?.disabled).toBe(true);

    await act(async () => {
      releases[0]?.();
      await Promise.resolve();
    });
    expect(confirmButton()?.disabled).toBe(false);
    expect(latest!.backtrackDialog.mode).toBe("code");
  });
});
