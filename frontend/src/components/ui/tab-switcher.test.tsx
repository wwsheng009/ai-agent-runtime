// @vitest-environment jsdom

// TabSwitcher 契约：roving tabindex、方向键/Home/End 循环选择并跟随焦点、disabled 跳过、badge 渲染。

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { TabSwitcher, type TabSwitcherItem } from "./tab-switcher";

type Key = "a" | "b" | "c";

function Harness({ items }: { items: ReadonlyArray<TabSwitcherItem<Key>> }) {
  const [value, setValue] = useState<Key>("a");
  return (
    <TabSwitcher ariaLabel="test tabs" items={items} onChange={setValue} value={value} />
  );
}

const items: TabSwitcherItem<Key>[] = [
  { id: "a", label: "Alpha", badge: "2" },
  { id: "b", label: "Beta" },
  { id: "c", label: "Gamma" },
];

describe("TabSwitcher", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT;
  });

  const tabs = () =>
    Array.from(container.querySelectorAll<HTMLButtonElement>('[role="tab"]'));
  const selected = () => tabs().find((tab) => tab.getAttribute("aria-selected") === "true");

  it("renders one roving tabindex and highlights the selected tab", () => {
    act(() => root.render(<Harness items={items} />));

    expect(tabs()).toHaveLength(3);
    expect(selected()?.textContent).toContain("Alpha");
    expect(selected()?.textContent).toContain("2");
    expect(tabs().filter((tab) => tab.getAttribute("tabindex") === "0")).toHaveLength(1);
    expect(container.querySelector('[role="tablist"]')?.getAttribute("aria-label")).toBe(
      "test tabs",
    );
  });

  it("moves selection and focus with arrow keys, Home and End (wrapping)", () => {
    act(() => root.render(<Harness items={items} />));

    const press = (key: string) =>
      act(() => {
        selected()?.dispatchEvent(
          new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }),
        );
      });

    press("ArrowRight");
    expect(selected()?.textContent).toContain("Beta");
    expect(document.activeElement).toBe(selected());

    press("End");
    expect(selected()?.textContent).toContain("Gamma");

    press("ArrowRight");
    expect(selected()?.textContent).toContain("Alpha");

    press("ArrowLeft");
    expect(selected()?.textContent).toContain("Gamma");

    press("Home");
    expect(selected()?.textContent).toContain("Alpha");

    press("Escape");
    expect(selected()?.textContent).toContain("Alpha");
  });

  it("skips disabled tabs and keeps them unfocusable", () => {
    act(() =>
      root.render(
        <Harness
          items={[items[0], { ...items[1], disabled: true }, items[2]]}
        />,
      ),
    );

    expect(tabs()[1]?.disabled).toBe(true);
    expect(tabs()[1]?.getAttribute("tabindex")).toBe("-1");

    act(() => {
      selected()?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }),
      );
    });
    expect(selected()?.textContent).toContain("Gamma");
    expect(document.activeElement?.textContent).toBe("Gamma");
  });

  it("selects on click and forwards aria/test attributes", () => {
    act(() =>
      root.render(
        <Harness
          items={[
            { id: "a", label: "Alpha", tabId: "tab-a", panelId: "panel-a" },
            ...items.slice(1),
          ]}
        />,
      ),
    );

    const alpha = tabs()[0];
    expect(alpha?.id).toBe("tab-a");
    expect(alpha?.getAttribute("aria-controls")).toBe("panel-a");

    act(() => {
      tabs()[1]?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(selected()?.textContent).toContain("Beta");
  });
});
