// @vitest-environment jsdom

// 键值行编辑器契约：默认只展示已设置项、按行编辑写回 JSON、空键行不落盘、外部改动可重同步。

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ProviderKeyValueRows } from "./provider-key-value-rows";

function Harness({
  initialJson,
  onJson,
}: {
  initialJson: string;
  onJson?: (json: string) => void;
}) {
  const [json, setJson] = useState(initialJson);
  return (
    <ProviderKeyValueRows
      addLabel="Add row"
      emptyHint="Nothing configured yet."
      json={json}
      keyPlaceholder="Name"
      onChangeJson={(next) => {
        setJson(next);
        onJson?.(next);
      }}
      removeLabel={(index) => `Remove row ${index}`}
      valuePlaceholder="Value"
    />
  );
}

function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("ProviderKeyValueRows", () => {
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

  const inputs = () =>
    Array.from(container.querySelectorAll<HTMLInputElement>("input"));
  const findButton = (label: string) =>
    Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes(label),
    );
  const findButtonByAriaLabel = (label: string) =>
    Array.from(container.querySelectorAll("button")).find(
      (button) => button.getAttribute("aria-label") === label,
    );

  it("shows the empty hint with no configured entries, and only those entries otherwise", () => {
    act(() => root.render(<Harness initialJson="{}" key="empty" />));
    expect(container.textContent).toContain("Nothing configured yet.");
    expect(inputs()).toHaveLength(0);

    act(() =>
      root.render(
        <Harness initialJson='{"X-Org":"42","X-Trace":"t"}' key="configured" />,
      ),
    );
    // 两条已设置项 = 2 行 × (名称 + 值)，没有空占位行。
    expect(inputs()).toHaveLength(4);
    expect(inputs()[0]?.value).toBe("X-Org");
    expect(inputs()[1]?.value).toBe("42");
    expect(container.textContent).not.toContain("Nothing configured yet.");
  });

  it("edits existing rows and writes trimmed keys into the JSON record", () => {
    const onJson = vi.fn();
    act(() => root.render(<Harness initialJson='{"X-Org":"42"}' onJson={onJson} />));

    const [keyInput, valueInput] = inputs();
    act(() => setInputValue(valueInput as HTMLInputElement, "43"));
    expect(JSON.parse(onJson.mock.calls.at(-1)?.[0] ?? "{}")).toEqual({
      "X-Org": "43",
    });

    act(() => setInputValue(keyInput as HTMLInputElement, "  X-Org  "));
    expect(JSON.parse(onJson.mock.calls.at(-1)?.[0] ?? "{}")).toEqual({
      "X-Org": "43",
    });
    // 输入框保留用户键入的原文，只有写回 JSON 时去空白。
    expect(inputs()[0]?.value).toBe("  X-Org  ");
  });

  it("adds a row, keeps empty keys out of the record, and removes rows", () => {
    const onJson = vi.fn();
    act(() => root.render(<Harness initialJson="{}" onJson={onJson} />));

    act(() =>
      findButton("Add row")?.dispatchEvent(new MouseEvent("click", { bubbles: true })),
    );
    expect(inputs()).toHaveLength(2);

    // 只填值不填键：仍处于编辑态，不写进 JSON。
    act(() => setInputValue(inputs()[1] as HTMLInputElement, "orphan"));
    expect(onJson).not.toHaveBeenCalledWith(
      expect.stringContaining("orphan"),
    );

    act(() => setInputValue(inputs()[0] as HTMLInputElement, "X-New"));
    expect(JSON.parse(onJson.mock.calls.at(-1)?.[0] ?? "{}")).toEqual({
      "X-New": "orphan",
    });

    act(() =>
      findButtonByAriaLabel("Remove row 1")?.dispatchEvent(
        new MouseEvent("click", { bubbles: true }),
      ),
    );
    expect(inputs()).toHaveLength(0);
    expect(JSON.parse(onJson.mock.calls.at(-1)?.[0] ?? "{}")).toEqual({});
    expect(container.textContent).toContain("Nothing configured yet.");
  });

  it("resyncs when the json prop changes externally", async () => {
    function ExternalHarness() {
      const [json, setJson] = useState("{}");
      return (
        <div>
          <button onClick={() => setJson('{"external":"1"}')} type="button">
            external change
          </button>
          <ProviderKeyValueRows
            addLabel="Add row"
            emptyHint="Nothing configured yet."
            json={json}
            keyPlaceholder="Name"
            onChangeJson={setJson}
            removeLabel={(index) => `Remove row ${index}`}
            valuePlaceholder="Value"
          />
        </div>
      );
    }

    act(() => root.render(<ExternalHarness />));
    expect(inputs()).toHaveLength(0);

    await act(async () => {
      Array.from(container.querySelectorAll("button"))
        .find((button) => button.textContent?.includes("external change"))
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(inputs()).toHaveLength(2);
    expect(inputs()[0]?.value).toBe("external");
    expect(inputs()[1]?.value).toBe("1");
  });
});
