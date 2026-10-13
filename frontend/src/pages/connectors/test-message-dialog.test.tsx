// 测试消息对话框交互单测：Escape 关闭语义（发送中拦截）。
// 组件级渲染用 createRoot + act（与仓库既有组件测试同一范式，不引入新依赖）。

import { act } from "react";
import type { ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ConnectorView } from "@/types/runtime";

vi.mock("@/api/runtime/connectors", () => ({
  sendConnectorTestMessage: vi.fn(),
}));

import { sendConnectorTestMessage } from "@/api/runtime/connectors";

import { TestMessageDialog } from "./test-message-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const CONNECTOR: ConnectorView = {
  id: "tg-main",
  provider: "telegram",
  name: "Telegram @my_bot",
  enabled: true,
  mode: "polling",
  settings: {},
  secret_keys: ["bot_token"],
  policy: {},
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

function findInput(ariaLabel: string) {
  return document.querySelector<HTMLInputElement>(`input[aria-label="${ariaLabel}"]`);
}

function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  act(() => {
    setter?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

function dispatchEscape() {
  window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
}

describe("TestMessageDialog", () => {
  const reactActEnvironment = globalThis as ReactActEnvironmentGlobal;
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    vi.mocked(sendConnectorTestMessage).mockReset();
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = false;
  });

  async function render(element: ReactElement) {
    await act(async () => {
      root?.render(element);
    });
  }

  it("Escape 关闭；发送中拦截关闭", async () => {
    const pending = deferred<void>();
    vi.mocked(sendConnectorTestMessage).mockReturnValueOnce(pending.promise);
    const onClose = vi.fn();
    await render(
      <TestMessageDialog open connector={CONNECTOR} onClose={onClose} />,
    );

    setInputValue(findInput("目标会话（chat id）") as HTMLInputElement, "123456");

    // 空闲时 Escape → 关闭。
    act(() => {
      dispatchEscape();
    });
    expect(onClose).toHaveBeenCalledTimes(1);
    onClose.mockClear();

    // 发送中 Escape → 拦截（与遮罩/取消按钮一致）。
    act(() => {
      findButton("发送")?.click();
    });
    expect(findButton("发送中…")).not.toBeNull();
    act(() => {
      dispatchEscape();
    });
    expect(onClose).not.toHaveBeenCalled();

    // 发送完成后 Escape → 恢复关闭。
    await act(async () => {
      pending.resolve();
    });
    act(() => {
      dispatchEscape();
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
