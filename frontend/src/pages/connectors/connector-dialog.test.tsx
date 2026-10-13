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
      // telegram 的真实形状：顶层 modes 与 key=mode 的 enum 字段并存。
      modes: ["polling"],
      fields: [
        { key: "bot_token", label: "Bot Token", type: "secret", required: true },
        {
          key: "mode",
          label: "接入模式",
          type: "enum",
          default: "polling",
          options: [
            { value: "polling", label: "长轮询（推荐）" },
            { value: "webhook", label: "Webhook" },
          ],
        },
        { key: "proxy_url", label: "Proxy", type: "url" },
      ],
    },
  ],
};

const SCHEMA_NO_MODE_FIELD: ConnectorSchema = {
  version: "1",
  providers: [
    {
      provider: "qq",
      label: "QQ",
      modes: ["gateway", "other"],
      fields: [{ key: "token", label: "Token", type: "text" }],
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

function findSelect(ariaLabel: string) {
  return document.querySelector<HTMLSelectElement>(`select[aria-label="${ariaLabel}"]`);
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

function setSelectValue(select: HTMLSelectElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLSelectElement.prototype,
    "value",
  )?.set;
  act(() => {
    setter?.call(select, value);
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

function dispatchEscape() {
  window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
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

  it("telegram 真实形状：只渲染一个模式控件，payload.mode 与 settings.mode 一致", async () => {
    const onSubmit = vi.fn<(payload: ConnectorSubmitPayload) => void>();
    await render(<ConnectorDialog {...dialogProps({ onSubmit })} />);

    // 顶层「模式」下拉不再渲染；模式只由 schema 的 mode 字段承载。
    expect(findSelect("模式")).toBeNull();
    const modeSelect = findSelect("接入模式");
    expect(modeSelect).not.toBeNull();
    expect(document.querySelectorAll("select")).toHaveLength(1);

    setSelectValue(modeSelect as HTMLSelectElement, "webhook");

    act(() => {
      findButton("保存")?.click();
    });

    const payload = onSubmit.mock.calls[0][0];
    expect(payload.settings?.mode).toBe("webhook");
    expect(payload.mode).toBe("webhook");
  });

  it("无 mode 字段时保留顶层「模式」下拉，payload.mode 取顶层值", async () => {
    const onSubmit = vi.fn<(payload: ConnectorSubmitPayload) => void>();
    await render(
      <ConnectorDialog
        {...dialogProps({ schema: SCHEMA_NO_MODE_FIELD, existing: null, onSubmit })}
      />,
    );

    const topMode = findSelect("模式");
    expect(topMode).not.toBeNull();
    expect(topMode?.value).toBe("gateway");
    setSelectValue(topMode as HTMLSelectElement, "other");
    setInputValue(findInput("ID") as HTMLInputElement, "qq-1");

    act(() => {
      findButton("保存")?.click();
    });

    const payload = onSubmit.mock.calls[0][0];
    expect(payload.mode).toBe("other");
    expect(payload.settings?.mode).toBeUndefined();
  });

  it("Escape：saving=false 关闭；saving=true 拦截", async () => {
    const onClose = vi.fn();
    await render(<ConnectorDialog {...dialogProps({ saving: true, onClose })} />);

    act(() => {
      dispatchEscape();
    });
    expect(onClose).not.toHaveBeenCalled();

    await render(<ConnectorDialog {...dialogProps({ saving: false, onClose })} />);
    act(() => {
      dispatchEscape();
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("本地校验错误在字段变更时清除对应 key", async () => {
    const props = dialogProps({ existing: null });
    await render(<ConnectorDialog {...props} />);

    act(() => {
      findButton("保存")?.click();
    });
    expect(document.body.textContent).toContain("请填写 ID");

    setInputValue(findInput("ID") as HTMLInputElement, "tg-1");

    expect(document.body.textContent).not.toContain("请填写 ID");
    expect(document.body.textContent).toContain("必填");
  });
});
