import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  createRuntimeTeamRunDraft: vi.fn(),
}));

vi.mock("@/lib/runtime-api", () => ({
  ...mocks,
  isRuntimeTeamProductCapabilitySupported: (
    capabilities: Record<string, string> | undefined,
    capability: string,
  ) => capabilities?.[capability] === "supported",
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) =>
      values ? `${key} ${Object.values(values).join(" ")}` : key,
  }),
}));

import type {
  RuntimeTeamProductCapabilities,
  RuntimeTeamRun,
} from "@/types/runtime";

import { TeamStudioPanel } from "./team-studio-panel";

const createdRun = {
  id: "run-created-1",
  owner_id: "owner-1",
  workspace_id: "workspace-1",
  goal: "Review the change",
  status: "draft",
  phase: "draft",
  compatibility_mode: "none",
  snapshot_schema_version: 1,
  config_snapshot: {},
  snapshot_checksum: "checksum",
  version: 1,
  event_seq: 0,
  summary_state: "not_applicable",
  created_at: "2026-11-22T12:00:00Z",
} satisfies RuntimeTeamRun;

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function setInputValue(input: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function findLabeledInput(
  container: HTMLElement,
  labelPart: string,
  occurrence = 0,
): HTMLInputElement {
  const inputs = Array.from(container.querySelectorAll("label"))
    .filter((label) => label.textContent?.includes(labelPart))
    .map((label) => label.querySelector("input"))
    .filter((input): input is HTMLInputElement => input !== null);
  const input = inputs[occurrence];
  if (!input) {
    throw new Error(`input with label ${labelPart} not found`);
  }
  return input;
}

function findLabeledSelect(
  container: HTMLElement,
  labelPart: string,
  occurrence = 0,
): HTMLSelectElement {
  const selects = Array.from(container.querySelectorAll("label"))
    .filter((label) => label.textContent?.includes(labelPart))
    .map((label) => label.querySelector("select"))
    .filter((select): select is HTMLSelectElement => select !== null);
  const select = selects[occurrence];
  if (!select) {
    throw new Error(`select with label ${labelPart} not found`);
  }
  return select;
}

function setSelectValue(select: HTMLSelectElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLSelectElement.prototype,
    "value",
  )?.set;
  setter?.call(select, value);
  select.dispatchEvent(new Event("change", { bubbles: true }));
}

describe("TeamStudioPanel", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mocks.createRuntimeTeamRunDraft.mockReset();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(
    capabilities: RuntimeTeamProductCapabilities = {
      run_draft_create: "supported",
    },
    onCreated = vi.fn(),
  ) {
    act(() => {
      root.render(
        <TeamStudioPanel
          capabilities={capabilities}
          onCancel={() => undefined}
          onCreated={onCreated}
        />,
      );
    });
    return onCreated;
  }

  function createButton(): HTMLButtonElement {
    const button = Array.from(container.querySelectorAll("button")).find(
      (candidate) =>
        candidate.textContent === "panels.teamsPanels.studio.create",
    );
    if (!(button instanceof HTMLButtonElement)) {
      throw new Error("Team Studio create button not found");
    }
    return button;
  }

  it("does not expose draft creation when the Runtime capability is unsupported", () => {
    render({ run_draft_create: "unsupported" });

    expect(container.textContent).toContain(
      "panels.teamsPanels.studio.unsupported",
    );
    expect(
      Array.from(container.querySelectorAll("button")).some(
        (button) =>
          button.textContent === "panels.teamsPanels.studio.create",
      ),
    ).toBe(false);
    expect(mocks.createRuntimeTeamRunDraft).not.toHaveBeenCalled();
  });

  it("validates the goal and graph references before sending a request", async () => {
    render();
    const goalInput = container.querySelector("input");
    expect(goalInput).toBeInstanceOf(HTMLInputElement);

    await act(async () => {
      createButton().dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    expect(container.textContent).toContain(
      "panels.teamsPanels.studio.validation.goal",
    );
    expect(mocks.createRuntimeTeamRunDraft).not.toHaveBeenCalled();

    await act(async () => {
      setInputValue(goalInput as HTMLInputElement, "Review the change");
      setInputValue(
        findLabeledInput(container, "panels.teamsPanels.studio.priority"),
        "1.5",
      );
      createButton().dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    expect(container.textContent).toContain(
      "panels.teamsPanels.studio.validation.taskPriority task-1",
    );
    expect(mocks.createRuntimeTeamRunDraft).not.toHaveBeenCalled();

    await act(async () => {
      setInputValue(
        findLabeledInput(container, "panels.teamsPanels.studio.priority"),
        "0",
      );
      setInputValue(
        findLabeledInput(container, "panels.teamsPanels.studio.assignee"),
        "missing-member",
      );
      createButton().dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    expect(container.textContent).toContain(
      "panels.teamsPanels.studio.validation.unknownAssignee task-1 missing-member",
    );
    expect(mocks.createRuntimeTeamRunDraft).not.toHaveBeenCalled();
  });

  it("persists a two-member graph snapshot with an idempotency key", async () => {
    mocks.createRuntimeTeamRunDraft.mockResolvedValueOnce({ run: createdRun });
    const onCreated = render();
    const goalInput = container.querySelector("input");
    expect(goalInput).toBeInstanceOf(HTMLInputElement);

    await act(async () => {
      setInputValue(goalInput as HTMLInputElement, "Review the change");
      setSelectValue(
        findLabeledSelect(container, "panels.teamsPanels.studio.difficulty"),
        "hard",
      );
      setSelectValue(
        findLabeledSelect(container, "panels.teamsPanels.studio.taskType"),
        "security",
      );
      setInputValue(
        findLabeledInput(container, "panels.teamsPanels.studio.capabilities"),
        "review, documentation",
      );
      setInputValue(
        findLabeledInput(container, "panels.teamsPanels.studio.priority"),
        "7",
      );
      setInputValue(
        findLabeledInput(
          container,
          "panels.teamsPanels.studio.difficultyRationale",
        ),
        "Requires cross-team security review.",
      );
      setInputValue(
        findLabeledInput(container, "panels.teamsPanels.studio.inputs"),
        "spec.md, security notes",
      );
      setInputValue(
        findLabeledInput(container, "panels.teamsPanels.studio.readPaths"),
        "docs, src",
      );
      setInputValue(
        findLabeledInput(container, "panels.teamsPanels.studio.writePaths"),
        "docs/review.md",
      );
      setInputValue(
        findLabeledInput(container, "panels.teamsPanels.studio.deliverables"),
        "review report, risk list",
      );
      createButton().dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });

    expect(mocks.createRuntimeTeamRunDraft).toHaveBeenCalledTimes(1);
    const [request, options] = mocks.createRuntimeTeamRunDraft.mock.calls[0];
    expect(request).toEqual({
      goal: "Review the change",
      config: {
        strategy: "manual",
        members: [
          {
            id: "member-1",
            name: "Reviewer",
            role: "reviewer",
            profile: "reviewer",
            capabilities: ["review", "documentation"],
            session_id: "",
            state: "idle",
          },
          {
            id: "member-2",
            name: "Implementer",
            role: "implementer",
            profile: "implementer",
            capabilities: [],
            session_id: "",
            state: "idle",
          },
        ],
        tasks: [
          {
            id: "task-1",
            title: "Review the scope",
            goal: "Review the requested change and report constraints.",
            assignee: "member-1",
            priority: 7,
            difficulty: "hard",
            difficulty_rationale: "Requires cross-team security review.",
            task_type: "security",
            task_subject: "scope review",
            inputs: ["spec.md", "security notes"],
            read_paths: ["docs", "src"],
            write_paths: ["docs/review.md"],
            deliverables: ["review report", "risk list"],
          },
          {
            id: "task-2",
            title: "Implement the change",
            goal: "Implement the change after the review is complete.",
            assignee: "member-2",
            priority: 0,
            difficulty: "normal",
            difficulty_rationale: "",
            task_type: "implement",
            task_subject: "requested change",
            inputs: [],
            read_paths: [],
            write_paths: [],
            deliverables: [],
          },
        ],
        dependencies: [{ task_id: "task-2", depends_on: "task-1" }],
      },
    });
    expect(options).toEqual({
      idempotencyKey: expect.stringMatching(/^web-team-run-draft-/),
      signal: expect.any(AbortSignal),
    });
    expect(onCreated).toHaveBeenCalledWith(createdRun);
  });

  it("reuses the key for a retry and starts a fresh key after success", async () => {
    mocks.createRuntimeTeamRunDraft
      .mockRejectedValueOnce(new Error("temporary network failure"))
      .mockResolvedValueOnce({ run: createdRun })
      .mockResolvedValueOnce({ run: { ...createdRun, id: "run-created-2" } })
      .mockResolvedValueOnce({ run: { ...createdRun, id: "run-created-3" } });
    render();
    const goalInput = container.querySelector("input");
    expect(goalInput).toBeInstanceOf(HTMLInputElement);

    await act(async () => {
      setInputValue(goalInput as HTMLInputElement, "Retry this draft");
      createButton().dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    const firstKey = mocks.createRuntimeTeamRunDraft.mock.calls[0]?.[1]
      .idempotencyKey;

    await act(async () => {
      createButton().dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    const retryKey = mocks.createRuntimeTeamRunDraft.mock.calls[1]?.[1]
      .idempotencyKey;

    expect(retryKey).toBe(firstKey);

    await act(async () => {
      setInputValue(goalInput as HTMLInputElement, "Create another draft");
      createButton().dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    const nextKey = mocks.createRuntimeTeamRunDraft.mock.calls[2]?.[1]
      .idempotencyKey;

    expect(nextKey).not.toBe(firstKey);

    await act(async () => {
      setInputValue(goalInput as HTMLInputElement, "Retry this draft");
      createButton().dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    const afterSuccessKey = mocks.createRuntimeTeamRunDraft.mock.calls[3]?.[1]
      .idempotencyKey;

    expect(afterSuccessKey).not.toBe(firstKey);
  });
});
