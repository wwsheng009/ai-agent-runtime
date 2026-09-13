// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SessionSwitchConfirmDialog } from "./session-switch-confirm-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

describe("SessionSwitchConfirmDialog", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
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

  async function renderDialog(open: boolean, sessionTitle: string) {
    const onCancel = vi.fn();
    const onConfirm = vi.fn();
    await act(async () => {
      root?.render(
        <SessionSwitchConfirmDialog
          open={open}
          sessionTitle={sessionTitle}
          onCancel={onCancel}
          onConfirm={onConfirm}
        />,
      );
    });
    return { onCancel, onConfirm };
  }

  it("renders the confirm dialog with the target session title", async () => {
    await renderDialog(true, "重构运行时");

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.getAttribute("aria-modal")).toBe("true");
    expect(document.body.textContent).toContain("会话正在生成回复");
    expect(document.body.textContent).toContain("重构运行时");
  });

  it("switches only after the confirm button is pressed", async () => {
    const { onCancel, onConfirm } = await renderDialog(true, "重构运行时");

    await act(async () => {
      findButton("切换")?.click();
    });

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("cancels on the cancel button and on Escape", async () => {
    const { onCancel, onConfirm } = await renderDialog(true, "重构运行时");

    await act(async () => {
      findButton("取消")?.click();
    });
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });

    expect(onCancel).toHaveBeenCalledTimes(2);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("renders nothing while closed", async () => {
    await renderDialog(false, "重构运行时");

    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });
});
