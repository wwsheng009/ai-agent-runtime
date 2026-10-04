// @vitest-environment jsdom

// 模型能力编辑器交互回归：手动增删模型、撤销、逐模型详情编辑与摘要 chip。
// 交互走真实受控回流（Harness 把 onChange 写回 state），覆盖组件与调用方契约。

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { i18n } from "@/i18n";

import type { ProviderModelDraft } from "./model-capability-draft";
import { emptyProviderModelDraft } from "./model-capability-draft";
import { ProviderModelEditor } from "./provider-model-editor";

const t = i18n.getFixedT(null, "runtimeConfig") as unknown as (
  key: string,
  options?: Record<string, unknown>,
) => string;

function Harness({
  defaultModel = "",
  drafts: initialDrafts = {},
  models: initialModels,
}: {
  defaultModel?: string;
  drafts?: Record<string, ProviderModelDraft>;
  models: string[];
}) {
  const [models, setModels] = useState(initialModels);
  const [drafts, setDrafts] = useState<Record<string, ProviderModelDraft>>(
    initialDrafts,
  );
  return (
    <ProviderModelEditor
      defaultModel={defaultModel}
      drafts={drafts}
      models={models}
      onChange={(next) => {
        setModels(next.models);
        setDrafts(next.drafts);
      }}
    />
  );
}

function findButton(container: HTMLElement, label: string) {
  return Array.from(container.querySelectorAll("button")).find((button) =>
    (button.textContent ?? "").includes(label),
  );
}

function findButtonByAriaLabel(container: HTMLElement, label: string) {
  return Array.from(container.querySelectorAll("button")).find(
    (button) => button.getAttribute("aria-label") === label,
  );
}

function findModelRow(container: HTMLElement, model: string) {
  return Array.from(
    container.querySelectorAll<HTMLButtonElement>("button[aria-pressed]"),
  ).find((button) => (button.textContent ?? "").includes(model));
}

function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("ProviderModelEditor", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    delete (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT;
  });

  it("renders summary chips and pins the default model", () => {
    act(() =>
      root?.render(
        <Harness
          defaultModel="gpt-4o"
          drafts={{
            "gpt-4o": {
              ...emptyProviderModelDraft(),
              reasoningModel: true,
              maxContextTokensText: "128000",
            },
          }}
          models={["gpt-4o", "o3"]}
        />,
      ),
    );

    expect(container.textContent).toContain("ctx 128K");
    expect(container.textContent).toContain(
      t("editor.providers.models.editor.defaultBadge"),
    );
    // 默认模型固定：不提供移除按钮。
    expect(
      findButtonByAriaLabel(
        container,
        t("editor.providers.models.editor.removeTitle", { model: "gpt-4o" }),
      ),
    ).toBeUndefined();
  });

  it("adds multiple models from one paste and reports duplicates", () => {
    act(() => root?.render(<Harness models={["gpt-4o"]} />));

    const addInput = Array.from(container.querySelectorAll("input")).find(
      (input) =>
        input.getAttribute("placeholder") ===
        t("editor.providers.models.editor.addPlaceholder"),
    ) as HTMLInputElement;
    expect(addInput).toBeTruthy();
    act(() => setInputValue(addInput, "o3, new-model"));

    const addButton = findButton(
      container,
      t("editor.providers.models.editor.add"),
    );
    act(() => addButton?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

    const rows = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button[aria-pressed]"),
    ).map((button) => button.textContent ?? "");
    expect(rows.some((text) => text.includes("o3"))).toBe(true);
    expect(rows.some((text) => text.includes("new-model"))).toBe(true);
    // 新添加的首个模型自动展开详情面板。
    expect(container.textContent).toContain("o3");

    const nextAddInput = Array.from(container.querySelectorAll("input")).find(
      (input) =>
        input.getAttribute("placeholder") ===
        t("editor.providers.models.editor.addPlaceholder"),
    ) as HTMLInputElement;
    act(() => setInputValue(nextAddInput, "o3"));
    act(() =>
      findButton(container, t("editor.providers.models.editor.add"))?.dispatchEvent(
        new MouseEvent("click", { bubbles: true }),
      ),
    );
    expect(container.textContent).toContain(
      t("editor.providers.models.editor.addDuplicate", { models: "o3" }),
    );
  });

  it("removes a model with its draft and restores both via undo", () => {
    act(() =>
      root?.render(
        <Harness
          drafts={{
            "o3": {
              ...emptyProviderModelDraft(),
              maxContextTokensText: "128000",
            },
          }}
          models={["gpt-4o", "o3"]}
        />,
      ),
    );

    act(() =>
      findButtonByAriaLabel(
        container,
        t("editor.providers.models.editor.removeTitle", { model: "o3" }),
      )?.dispatchEvent(new MouseEvent("click", { bubbles: true })),
    );
    expect(
      Array.from(
        container.querySelectorAll<HTMLButtonElement>("button[aria-pressed]"),
      ).some((button) => (button.textContent ?? "").includes("o3")),
    ).toBe(false);
    expect(container.textContent).toContain(
      t("editor.providers.models.editor.removed", {
        model: "o3",
        configured: t("editor.providers.models.editor.removedConfigured"),
      }),
    );

    act(() =>
      findButton(container, t("editor.providers.models.editor.undo"))?.dispatchEvent(
        new MouseEvent("click", { bubbles: true }),
      ),
    );
    const restoredRow = findModelRow(container, "o3");
    expect(restoredRow).toBeTruthy();
    // 草稿一并恢复：摘要 chip 仍是移除前的 ctx 128K。
    expect(restoredRow?.textContent).toContain("ctx 128K");
  });

  it("edits a per-model field from the detail panel", () => {
    act(() => root?.render(<Harness models={["o3"]} />));

    act(() =>
      findModelRow(container, "o3")?.dispatchEvent(
        new MouseEvent("click", { bubbles: true }),
      ),
    );
    const contextInput = Array.from(container.querySelectorAll("input")).find(
      (input) => input.getAttribute("placeholder") === "128000",
    ) as HTMLInputElement;
    expect(contextInput).toBeTruthy();
    act(() => setInputValue(contextInput, "32000"));

    const row = findModelRow(container, "o3");
    expect(row?.textContent).toContain("ctx 32K");
  });

  it("toggles a modality chip on the detail panel", () => {
    act(() => root?.render(<Harness models={["o3"]} />));

    act(() =>
      findModelRow(container, "o3")?.dispatchEvent(
        new MouseEvent("click", { bubbles: true }),
      ),
    );
    const imageChip = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button[aria-pressed]"),
    ).find((button) => button.textContent?.trim() === "image" && !button.hasAttribute("aria-label"));
    expect(imageChip).toBeTruthy();
    act(() =>
      imageChip?.dispatchEvent(new MouseEvent("click", { bubbles: true })),
    );
    expect(imageChip?.getAttribute("aria-pressed")).toBe("true");
    expect(findModelRow(container, "o3")?.textContent).toContain("image");
  });
});
