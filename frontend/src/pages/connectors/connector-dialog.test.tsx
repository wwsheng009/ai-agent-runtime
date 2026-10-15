// 连接器对话框的交互单测：密钥「留空=不改 / 填写=提交 / 清除=__clear__」。
// 组件级渲染用 createRoot + act（与仓库既有组件测试同一范式，不引入新依赖）。

import { act } from "react";
import type { ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ConnectorSchema, ConnectorSubmitPayload, ConnectorView } from "@/types/runtime";

vi.mock("@/api/runtime/connectors", () => ({
  CONNECTOR_CLEAR_SECRET: "__clear__",
  createConnector: vi.fn(),
  deleteConnector: vi.fn(),
  runConnectorAction: vi.fn(),
  updateConnector: vi.fn(),
}));

import {
  createConnector,
  deleteConnector,
  runConnectorAction,
} from "@/api/runtime/connectors";

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

const WECHAT_SCHEMA: ConnectorSchema = {
  version: "1",
  providers: [
    {
      provider: "wechat",
      label: "微信",
      modes: ["polling"],
      fields: [
        {
          key: "base_url",
          label: "iLink 基址",
          type: "url",
          default: "https://ilinkai.weixin.qq.com",
        },
        { key: "account_id", label: "账号 ID", type: "text" },
      ],
      actions: [{ key: "start_login", label: "扫码登录", kind: "qr" }],
    },
  ],
};

const WECHAT_CREATED: ConnectorView = {
  id: "wechat-abc",
  provider: "wechat",
  name: "微信",
  enabled: true,
  mode: "polling",
  settings: { base_url: "https://ilinkai.weixin.qq.com" },
  secret_keys: [],
  policy: {},
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

function toggleTypeCheckbox(name: string) {
  toggleCheckbox(findInput(name) as HTMLInputElement);
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
    vi.mocked(createConnector).mockReset();
    vi.mocked(deleteConnector).mockReset();
    vi.mocked(runConnectorAction).mockReset();
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

  it("扫码通道：新增面板用扫码入口替代凭据字段，ID 自动生成", async () => {
    await render(
      <ConnectorDialog {...dialogProps({ schema: WECHAT_SCHEMA, existing: null })} />,
    );

    expect(findButton("扫码登录")).not.toBeNull();
    // 凭据字段区被扫码入口替代（细节收进「高级设置」）。
    expect(document.body.textContent).not.toContain("凭据与设置");
    // ID 自动生成，用户无需手填。
    expect(findInput("ID")?.value).toMatch(/^wechat-/);
  });

  it("点击扫码登录：按自动 ID 创建连接器（基址取 Schema 默认值）", async () => {
    vi.mocked(createConnector).mockResolvedValue(WECHAT_CREATED);
    vi.mocked(runConnectorAction).mockResolvedValue({
      login_id: "login-1",
      status: "wait",
      qr_code_content: "data:image/png;base64,QQ==",
      raw: {},
    });

    await render(
      <ConnectorDialog {...dialogProps({ schema: WECHAT_SCHEMA, existing: null })} />,
    );

    await act(async () => {
      findButton("扫码登录")?.click();
    });
    await act(async () => undefined);

    expect(createConnector).toHaveBeenCalledTimes(1);
    const [payload] = vi.mocked(createConnector).mock.calls[0];
    expect(payload.provider).toBe("wechat");
    expect(payload.id).toMatch(/^wechat-/);
    expect(payload.settings?.base_url).toBe("https://ilinkai.weixin.qq.com");
  });

  it("扫码未确认即关闭：清理草稿连接器，避免留下空连接器", async () => {
    vi.mocked(createConnector).mockResolvedValue(WECHAT_CREATED);
    vi.mocked(deleteConnector).mockResolvedValue(undefined);
    vi.mocked(runConnectorAction).mockResolvedValue({
      login_id: "login-1",
      status: "wait",
      qr_code_content: "data:image/png;base64,QQ==",
      raw: {},
    });
    const onClose = vi.fn();

    await render(
      <ConnectorDialog
        {...dialogProps({ schema: WECHAT_SCHEMA, existing: null, onClose })}
      />,
    );
    await act(async () => {
      findButton("扫码登录")?.click();
    });
    await act(async () => undefined);

    // 扫码面板已就绪，未扫码确认直接关闭。
    await act(async () => {
      findButton("关闭")?.click();
    });
    await act(async () => undefined);

    expect(deleteConnector).toHaveBeenCalledWith("wechat-abc", expect.anything());
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("S2：编辑态事件订阅随提交体 PATCH，且保留 D6 路由", async () => {
    const onSubmit = vi.fn<(payload: ConnectorSubmitPayload) => void>();
    const existing: ConnectorView = {
      ...EXISTING,
      policy: { route: { provider: "workbuddy", model: "m1", reasoning_effort: "medium" } },
    };
    await render(<ConnectorDialog {...dialogProps({ existing, onSubmit })} />);

    // 订阅行常显；勾选事件类型会自动打开总开关（避免保存成 enabled:false 的无效订阅）。
    const enabled = findInput("启用事件订阅（policy.events）") as HTMLInputElement;
    expect(enabled.checked).toBe(false);

    act(() => {
      findButton("添加订阅")?.click();
    });

    // 类型改为「按投递形态分组多选」：assistant_message 唯一形态 content（自动推导），
    // 叠加 turn.* 后跨形态冲突可见（告警 + 一键拆分）。
    toggleTypeCheckbox("assistant_message");
    toggleTypeCheckbox("turn.*");

    expect((findInput("启用事件订阅（policy.events）") as HTMLInputElement).checked).toBe(true);
    expect((findSelect("投递形态") as HTMLSelectElement).value).toBe("content");
    expect(document.body.textContent).toContain("turn.* 需要 deliver=notice/digest");
    expect(findButton("一键拆分为多条订阅")).not.toBeNull();

    act(() => {
      findButton("保存")?.click();
    });

    const payload = onSubmit.mock.calls[0][0];
    expect(payload.policy?.events).toEqual({
      enabled: true,
      subscriptions: [
        {
          id: "",
          sessions: { mode: "bound" },
          types: ["assistant_message", "turn.*"],
          deliver: "content",
        },
      ],
    });
    expect(payload.policy?.route).toEqual({
      provider: "workbuddy",
      model: "m1",
      reasoning_effort: "medium",
    });
  });

  it("S2：关闭事件订阅时提交 enabled:false（清空订阅）", async () => {
    const onSubmit = vi.fn<(payload: ConnectorSubmitPayload) => void>();
    const existing: ConnectorView = {
      ...EXISTING,
      policy: {
        events: {
          enabled: true,
          subscriptions: [{ id: "ops", types: ["turn.*"], deliver: "notice" }],
        },
      },
    };
    await render(<ConnectorDialog {...dialogProps({ existing, onSubmit })} />);

    const enabled = findInput("启用事件订阅（policy.events）") as HTMLInputElement;
    expect(enabled.checked).toBe(true);
    // 既有订阅回填为分组多选面板（汇总行可见）。
    expect(findInput("turn.*")?.checked).toBe(true);

    toggleCheckbox(enabled);
    act(() => {
      findButton("保存")?.click();
    });

    expect(onSubmit.mock.calls[0][0].policy?.events).toEqual({ enabled: false });
  });

  it("S4：单条订阅可显式停用（enabled:false 随提交体提交）", async () => {
    const onSubmit = vi.fn<(payload: ConnectorSubmitPayload) => void>();
    const existing: ConnectorView = {
      ...EXISTING,
      policy: {
        events: {
          enabled: true,
          subscriptions: [{ id: "ops", types: ["turn.*"], deliver: "notice" }],
        },
      },
    };
    await render(<ConnectorDialog {...dialogProps({ existing, onSubmit })} />);

    const rowToggle = findInput("启用") as HTMLInputElement;
    expect(rowToggle.checked).toBe(true);
    toggleCheckbox(rowToggle);

    act(() => {
      findButton("保存")?.click();
    });

    expect(onSubmit.mock.calls[0][0].policy?.events).toEqual({
      enabled: true,
      subscriptions: [{ id: "ops", types: ["turn.*"], deliver: "notice", enabled: false }],
    });
  });
});
