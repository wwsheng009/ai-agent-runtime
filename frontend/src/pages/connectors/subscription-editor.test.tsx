// S2b（docs/bots/13 §3.4/§6.1）：订阅编辑器组件级门禁——按投递形态分组多选、
// 形态推导（锁定/收敛）、跨形态告警与一键拆分、勾选自动打开总开关、词表外值往返不丢。
// 组件为纯受控（value + onChange），用 useState 外壳驱动并读取模型快照。

import { act, useState } from "react";
import type { ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { ConnectorEventSubscriptionView } from "@/types/runtime";

import { SubscriptionEditor, type SubscriptionEditorValue } from "./subscription-editor";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function Harness({ initial }: { initial: SubscriptionEditorValue }) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <SubscriptionEditor value={value} onChange={setValue} />
      <pre data-testid="model">{JSON.stringify(value)}</pre>
    </>
  );
}

function findInput(ariaLabel: string) {
  return document.querySelector<HTMLInputElement>(`input[aria-label="${ariaLabel}"]`);
}

function findSelect(ariaLabel: string) {
  return document.querySelector<HTMLSelectElement>(`select[aria-label="${ariaLabel}"]`);
}

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

function checkbox(ariaLabel: string) {
  return document.querySelector<HTMLInputElement>(
    `input[type="checkbox"][aria-label="${ariaLabel}"]`,
  );
}

function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  act(() => {
    setter?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

function setSelectValue(select: HTMLSelectElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")?.set;
  act(() => {
    setter?.call(select, value);
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

function toggleCheckbox(input: HTMLInputElement) {
  act(() => {
    input.click();
  });
}

function readModel(): SubscriptionEditorValue {
  const node = document.querySelector('[data-testid="model"]');
  return JSON.parse(node?.textContent ?? "{}") as SubscriptionEditorValue;
}

function sub(
  overrides: Partial<ConnectorEventSubscriptionView> = {},
): ConnectorEventSubscriptionView {
  return { id: "ops", sessions: { mode: "bound" }, types: [], deliver: "notice", ...overrides };
}

describe("SubscriptionEditor", () => {
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

  it("按投递形态分组（含未接线与自定义(既有)组），汇总行显示已选", async () => {
    await render(
      <Harness
        initial={{ enabled: true, subscriptions: [sub({ types: ["turn.*", "my.custom"] })] }}
      />,
    );

    const text = document.body.textContent ?? "";
    expect(text).toContain("content(正文副本)");
    expect(text).toContain("notice / digest(即时或聚合)");
    expect(text).toContain("未接线(勾选不会投递)");
    expect(text).toContain("自定义(既有)");
    expect(text).toContain("词表外");
    expect(text).toContain("已选 2 项:turn.*、my.custom");

    expect(checkbox("turn.*")?.checked).toBe(true);
    expect(checkbox("my.custom")?.checked).toBe(true);
    expect(checkbox("assistant_message")?.checked).toBe(false);
    expect(readModel()).toEqual({
      enabled: true,
      subscriptions: [sub({ types: ["turn.*", "my.custom"] })],
    });
  });

  it("勾选唯一形态类型：形态锁定为推导值并自动打开总开关", async () => {
    await render(<Harness initial={{ enabled: false, subscriptions: [sub()] }} />);
    expect((findInput("启用事件订阅（policy.events）") as HTMLInputElement).checked).toBe(false);

    toggleCheckbox(checkbox("assistant_message") as HTMLInputElement);

    const deliver = findSelect("投递形态") as HTMLSelectElement;
    expect(deliver.value).toBe("content");
    expect(deliver.disabled).toBe(true);
    expect(deliver.options).toHaveLength(1);
    expect(document.body.textContent).toContain("由所选类型推导");
    expect((findInput("启用事件订阅（policy.events）") as HTMLInputElement).checked).toBe(true);

    expect(readModel()).toEqual({
      enabled: true,
      subscriptions: [sub({ types: ["assistant_message"], deliver: "content" })],
    });
  });

  it("多形态类型收敛选项（completion → notice/digest 可选）", async () => {
    await render(
      <Harness initial={{ enabled: true, subscriptions: [sub({ types: ["completion"] })] }} />,
    );

    const deliver = findSelect("投递形态") as HTMLSelectElement;
    expect(Array.from(deliver.options).map((option) => option.value)).toEqual(["notice", "digest"]);
    expect(deliver.disabled).toBe(false);
    expect(document.body.textContent).toContain("可选:notice/digest");

    setSelectValue(deliver, "digest");
    expect((findSelect("投递形态") as HTMLSelectElement).value).toBe("digest");
    expect(readModel().subscriptions[0].deliver).toBe("digest");
  });

  it("跨形态冲突：告警可见，一键拆分按形态拆成两条并保留 id 后缀", async () => {
    await render(
      <Harness
        initial={{ enabled: true, subscriptions: [sub({ types: ["assistant_message", "turn.*"] })] }}
      />,
    );

    expect(document.body.textContent).toContain("assistant_message 需要 deliver=content");
    expect(findButton("一键拆分为多条订阅")).not.toBeNull();

    act(() => {
      findButton("一键拆分为多条订阅")?.click();
    });

    expect(readModel()).toEqual({
      enabled: true,
      subscriptions: [
        sub({ types: ["assistant_message"], deliver: "content" }),
        sub({ id: "ops-2", types: ["turn.*"], deliver: "notice" }),
      ],
    });
    expect(findButton("一键拆分为多条订阅")).toBeUndefined();
  });

  it("未接线类型告警（勾选不会投递），且保留形态自由选择", async () => {
    await render(
      <Harness initial={{ enabled: true, subscriptions: [sub({ types: ["user_message"] })] }} />,
    );

    expect(document.body.textContent).toContain("user_message 暂未接出站映射(不会投递)");
    expect(findButton("一键拆分为多条订阅")).toBeUndefined();
    expect((findSelect("投递形态") as HTMLSelectElement).options).toHaveLength(4);
  });

  it("自定义/别名输入：差分合并进 types，删除即移除", async () => {
    await render(<Harness initial={{ enabled: true, subscriptions: [sub()] }} />);

    const custom = findInput("自定义/别名") as HTMLInputElement;
    setInputValue(custom, "approval");
    expect(readModel().subscriptions[0].types).toEqual(["approval"]);
    expect(document.body.textContent).toContain("已选 1 项:approval");

    setInputValue(custom, "approval, my.custom");
    expect(readModel().subscriptions[0].types).toEqual(["approval", "my.custom"]);

    setInputValue(custom, "my.custom");
    expect(readModel().subscriptions[0].types).toEqual(["my.custom"]);
  });

  it("会话范围联动：非 ids 模式禁用会话列表并提示", async () => {
    await render(
      <Harness
        initial={{
          enabled: true,
          subscriptions: [sub({ sessions: { mode: "bound", ids: ["s1"] } })],
        }}
      />,
    );

    const ids = findInput("会话 ID 列表") as HTMLInputElement;
    expect(ids.disabled).toBe(true);
    expect(ids.placeholder).toBe("(mode=ids 时生效)");

    setSelectValue(findSelect("会话范围") as HTMLSelectElement, "ids");

    const idsAfter = findInput("会话 ID 列表") as HTMLInputElement;
    expect(idsAfter.disabled).toBe(false);
    expect(idsAfter.value).toBe("s1");
    expect(idsAfter.placeholder).toBe("session_a, session_b");
  });
});