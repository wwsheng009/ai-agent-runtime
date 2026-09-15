// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";

import type { SessionBacktrackDialogState } from "@/hooks/workspace/use-session-backtrack";
import type { RuntimeSessionBacktrackMode } from "@/lib/runtime-api";

import { MessageBacktrackDialog } from "./message-backtrack-dialog";

// 用户诉求：对话框里的「还原模式」选项只负责选择，必须点确认按钮才执行回溯。
describe("MessageBacktrackDialog restore mode selection", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let onApply: Mock<() => void>;
  let onClose: Mock<() => void>;
  let onModeChange: Mock<(mode: RuntimeSessionBacktrackMode) => void>;

  type ReactActEnvironmentGlobal = typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean;
  };

  const state: SessionBacktrackDialogState = {
    open: true,
    busy: false,
    previewing: false,
    error: null,
    target: {
      messageId: "message-1",
      messageIndex: 2,
      userTurnIndex: 1,
      preview: "把右侧栏合并成一个面板",
      fullText: "把右侧栏合并成一个面板",
    },
    preview: {
      session_id: "session-1",
      mode: "conversation",
      user_turn_index: 1,
      message_index: 2,
      truncated_to_message_count: 3,
      removed_message_count: 4,
      removed_user_turns: 2,
      anchor_preview: "把右侧栏合并成一个面板",
    },
    mode: "conversation",
    prefillComposer: false,
    editPrompt: "",
  };

  const render = (override: Partial<SessionBacktrackDialogState> = {}) => {
    act(() => {
      root = createRoot(container);
      root.render(
        <MessageBacktrackDialog
          onApply={onApply}
          onClose={onClose}
          onEditPromptChange={() => {}}
          onModeChange={onModeChange}
          onPrefillChange={() => {}}
          state={{ ...state, ...override }}
        />,
      );
    });
  };

  const modeInputs = () =>
    Array.from(
      document.querySelectorAll<HTMLInputElement>('input[name="backtrack-mode"]'),
    );

  const confirmButton = () =>
    Array.from(document.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("确认回溯"),
    );

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    onApply = vi.fn<() => void>();
    onClose = vi.fn<() => void>();
    onModeChange = vi.fn<(mode: RuntimeSessionBacktrackMode) => void>();
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
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

  it("点还原模式选项只切换模式，不执行回溯", () => {
    render();

    const filesOnly = modeInputs().find((input) => input.value === "code");
    expect(filesOnly).toBeTruthy();
    act(() => {
      filesOnly?.click();
    });

    expect(onModeChange).toHaveBeenCalledWith("code");
    expect(onApply).not.toHaveBeenCalled();
  });

  it("点确认按钮才执行回溯", () => {
    render();

    act(() => {
      confirmButton()?.click();
    });
    expect(onApply).toHaveBeenCalledTimes(1);
  });

  it("预览在途时不执行回溯（底部按钮保持禁用）", () => {
    render({ previewing: true });

    expect(confirmButton()?.disabled).toBe(true);
    act(() => {
      confirmButton()?.click();
    });
    expect(onApply).not.toHaveBeenCalled();
  });
});
