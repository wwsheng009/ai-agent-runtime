// 连接器对话框的交互单测：密钥「留空=不改 / 填写=提交 / 清除=__clear__」。
// 组件级渲染用 createRoot + act（与仓库既有组件测试同一范式，不引入新依赖）。

import { act } from "react";
import type { ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ConnectorSchema, ConnectorSubmitPayload, ConnectorView } from "@/types/runtime";

import { ConnectorDialog } from "./connector-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SCHEMA: ConnectorSchema = {
  version: "1",
  providers: [
    {
      provider: "telegram",
      label: "Telegram",
      modes: ["polling", "webhook"],
      fields: [
        { key: "bot_token", label: "Bot Token", type: "secret", required: true },
        { key: "proxy_url", label: "Proxy", type: "url" },
      ],
    },
  ],
};

const EXISTING: ConnectorView = {
  id: "tg-main",
  provider: "telegram",
  name: "Telegram @my_bot",
  enabled: true,
  mode: "polling",
  settings: { proxy_url: "http://127.0.0.1:7890" },
  secret_keys: ["bot_token"],
  policy: {},
};

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

function toggleCheckbox(input: HTMLInputElement) {
  act(() => {
    input.click();
  });
}

describe("ConnectorDialog", () => {
  const reactActEnvironment = globalThis as ReactActEnvironmentGlobal;
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
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

  function dialogProps(overrides: Partial<Parameters<typeof ConnectorDialog>[0]> = {}) {
    return {
      open: true,
      schema: SCHEMA,
      existing: EXISTING,
      saving: false,
      serverError: null,
      serverFieldErrors: [],
      onSubmit: vi.fn(),
      onClose: vi.fn(),
      ...overrides,
    };
  }

  it("编辑时密钥留空 = 不提交该字段（后端保持原值）", async () => {
    const onSubmit = vi.fn<(payload: ConnectorSubmitPayload) => void>();
    await render(<ConnectorDialog {...dialogProps({ onSubmit })} />);

    act(() => {
      findButton("保存")?.click();
    });

    expect(onSubmit).toHaveBeenCalledTimes(1);
    const payload = onSubmit.mock.calls[0][0];
    expect(payload.secrets).toEqual({});
    expect(payload.settings).toMatchObject({ proxy_url: "http://127.0.0.1:7890" });
    expect(payload.id).toBeUndefined();
    expect(payload.provider).toBeUndefined();
  });

  it("填写密钥后提交该字段（两端去空格）", async () => {
    const onSubmit = vi.fn<(payload: ConnectorSubmitPayload) => void>();
    await render(<ConnectorDialog {...dialogProps({ onSubmit })} />);

    const tokenInput = findInput("Bot Token");
    expect(tokenInput).not.toBeNull();
    setInputValue(tokenInput as HTMLInputElement, " 123:abc ");

    act(() => {
      findButton("保存")?.click();
    });

    expect(onSubmit.mock.calls[0][0].secrets).toEqual({ bot_token: "123:abc" });
  });

  it("勾选「清除」提交 __clear__，密钥输入框禁用", async () => {
    const onSubmit = vi.fn<(payload: ConnectorSubmitPayload) => void>();
    await render(<ConnectorDialog {...dialogProps({ onSubmit })} />);

    const clearCheckbox = findInput("清除");
    expect(clearCheckbox).not.toBeNull();
    toggleCheckbox(clearCheckbox as HTMLInputElement);

    expect(findInput("Bot Token")?.disabled).toBe(true);

    act(() => {
      findButton("保存")?.click();
    });

    expect(onSubmit.mock.calls[0][0].secrets).toEqual({ bot_token: "__clear__" });
  });

  it("新建缺必填时不提交并显示校验文案", async () => {
    const props = dialogProps({ existing: null });
    await render(<ConnectorDialog {...props} />);

    act(() => {
      findButton("保存")?.click();
    });

    expect(props.onSubmit).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("请填写 ID");
    expect(document.body.textContent).toContain("必填");
  });
});
