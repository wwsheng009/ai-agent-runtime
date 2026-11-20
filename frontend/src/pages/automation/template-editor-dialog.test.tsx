// @vitest-environment jsdom

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { TemplateEditorDialog } from "./template-editor-dialog";
import {
  emptyAutomationTemplateForm,
  type AutomationTemplateForm,
  type AutomationTemplateFormErrors,
} from "./templates";

const catalogs = vi.hoisted(() => ({
  listRuntimeModels: vi.fn(),
  listRuntimeProfiles: vi.fn(),
  listWorkspaceDirectories: vi.fn(),
}));

vi.mock("@/api/runtime/models", () => ({
  listRuntimeModels: catalogs.listRuntimeModels,
}));
vi.mock("@/api/runtime/profiles", () => ({
  listRuntimeProfiles: catalogs.listRuntimeProfiles,
}));
vi.mock("@/api/runtime/workspace-directories", () => ({
  listWorkspaceDirectories: catalogs.listWorkspaceDirectories,
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

let latestForm: AutomationTemplateForm | null = null;

function Harness({
  error = null,
  fieldErrors = {},
  initialForm,
}: {
  error?: string | null;
  fieldErrors?: AutomationTemplateFormErrors;
  initialForm: AutomationTemplateForm;
}) {
  const [form, setForm] = useState(initialForm);
  latestForm = form;
  return (
    <TemplateEditorDialog
      busy={false}
      error={error}
      fieldErrors={fieldErrors}
      form={form}
      mode="edit"
      onChange={(patch) =>
        setForm((current) => ({ ...current, ...patch }))
      }
      onClearError={() => undefined}
      onClose={() => undefined}
      onSubmit={() => undefined}
    />
  );
}

function interactionForm(): AutomationTemplateForm {
  return {
    ...emptyAutomationTemplateForm(),
    id: "template-1",
    title: "Project check",
    taskTitle: "Check project",
    prompt: "Inspect the project",
    kind: "daily",
    clockTime: "09:00",
    timeZone: "Asia/Shanghai",
    targetKind: "workspace",
    goal: "Report actionable risks",
  };
}

async function flushCatalogPromises() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

function click(element: Element) {
  act(() => {
    (element as HTMLElement).click();
  });
}

function chooseOption(label: string) {
  const option = Array.from(
    document.body.querySelectorAll<HTMLElement>('[role="option"]'),
  ).find((item) => item.textContent?.includes(label));
  if (!option) {
    throw new Error(`missing select option: ${label}`);
  }
  click(option);
}

function trigger(testId: string): HTMLButtonElement {
  const element = document.body.querySelector<HTMLButtonElement>(
    `[data-testid="${testId}"]`,
  );
  if (!element) {
    throw new Error(`missing select trigger: ${testId}`);
  }
  return element;
}

describe("TemplateEditorDialog", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let originalScrollIntoView: typeof HTMLElement.prototype.scrollIntoView;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    latestForm = null;
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    originalScrollIntoView = HTMLElement.prototype.scrollIntoView;
    HTMLElement.prototype.scrollIntoView = vi.fn();
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 0;
    });

    catalogs.listRuntimeProfiles.mockResolvedValue({
      profiles: [{ ref: "user:reviewer", name: "Reviewer" }],
      count: 1,
    });
    catalogs.listWorkspaceDirectories.mockResolvedValue({
      directories: [{ id: "ws-1", name: "Demo", path: "E:/demo" }],
      count: 1,
    });
    catalogs.listRuntimeModels.mockResolvedValue({
      providers: [
        {
          name: "gateway",
          models: ["model-a", "model-b"],
          model_capabilities: {
            "model-a": { reasoning_efforts: ["low", "high"] },
          },
        },
      ],
      count: 1,
    });
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
      root = null;
    }
    document.body.innerHTML = "";
    latestForm = null;
    catalogs.listRuntimeModels.mockReset();
    catalogs.listRuntimeProfiles.mockReset();
    catalogs.listWorkspaceDirectories.mockReset();
    HTMLElement.prototype.scrollIntoView = originalScrollIntoView;
    vi.unstubAllGlobals();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderDialog(
    form = interactionForm(),
    options: {
      error?: string | null;
      fieldErrors?: AutomationTemplateFormErrors;
    } = {},
  ) {
    act(() => {
      root?.render(
        <Harness
          error={options.error}
          fieldErrors={options.fieldErrors}
          initialForm={form}
        />,
      );
    });
  }

  it("uses catalog-backed workspace/profile/provider/model selects and cascades changes", async () => {
    renderDialog();
    await flushCatalogPromises();

    click(trigger("automation-template-workspace"));
    chooseOption("Demo · E:/demo");
    expect(latestForm?.targetWorkspace).toBe("E:/demo");

    click(trigger("automation-template-profile"));
    chooseOption("Reviewer · user:reviewer");
    expect(latestForm?.targetProfile).toBe("user:reviewer");

    expect(trigger("automation-template-model").disabled).toBe(true);
    click(trigger("automation-template-provider"));
    chooseOption("gateway");
    expect(latestForm?.provider).toBe("gateway");
    expect(latestForm?.model).toBe("");
    expect(trigger("automation-template-model").disabled).toBe(false);

    click(trigger("automation-template-model"));
    chooseOption("model-a");
    expect(latestForm?.model).toBe("model-a");

    click(trigger("automation-template-reasoning-effort"));
    chooseOption("high");
    expect(latestForm?.reasoningEffort).toBe("high");
  });

  it("falls back to manual provider/model/reasoning inputs when the catalog fails", async () => {
    catalogs.listRuntimeModels.mockRejectedValue(new Error("offline"));
    renderDialog();
    await flushCatalogPromises();

    expect(
      document.body.querySelector<HTMLInputElement>(
        'input[data-testid="automation-template-provider"]',
      ),
    ).not.toBeNull();
    expect(
      document.body.querySelector<HTMLInputElement>(
        'input[data-testid="automation-template-model"]',
      ),
    ).not.toBeNull();
    expect(document.body.textContent).toContain(
      "部分执行选项加载失败，可手动输入对应值。",
    );
  });

  it("focuses a linked error summary and associates inline errors with fields", async () => {
    renderDialog(interactionForm(), {
      fieldErrors: { title: "required", prompt: "required" },
    });
    await flushCatalogPromises();

    const summary = document.body.querySelector<HTMLElement>(
      '[data-testid="automation-template-error-summary"]',
    );
    expect(summary?.textContent).toContain("请修正标出的字段后重试。");
    expect(document.activeElement).toBe(summary);
    expect(
      document.body
        .querySelector<HTMLInputElement>('[data-testid="automation-template-title"]')
        ?.getAttribute("aria-describedby"),
    ).toBe("automation-template-error-title");
  });
});
