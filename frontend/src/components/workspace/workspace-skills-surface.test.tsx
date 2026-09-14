// @vitest-environment jsdom

// P2-1B：工作台「技能」页签（目录列表 + 详情对话框）渲染与交互单测。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { getRuntimeSkillDetail, listRuntimeSkills } from "@/api/runtime/skills";
import type { RuntimeSkill } from "@/types/runtime";

import { WorkspaceSkillsSurface } from "./workspace-skills-surface";

vi.mock("@/api/runtime/skills", () => ({
  getRuntimeSkillDetail: vi.fn(),
  isSkillsForbidden: vi.fn(() => false),
  isSkillsUnavailable: vi.fn(() => false),
  listRuntimeSkills: vi.fn(),
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const listMock = vi.mocked(listRuntimeSkills);
const detailMock = vi.mocked(getRuntimeSkillDetail);

function skill(partial: Partial<RuntimeSkill> & { name: string }): RuntimeSkill {
  return {
    description: "",
    version: "",
    category: "",
    capabilities: [],
    tags: [],
    triggers: [],
    tools: [],
    systemPrompt: "",
    userPrompt: "",
    workflowSteps: [],
    contextFiles: [],
    contextEnvironment: [],
    contextSymbols: [],
    permissions: [],
    source: null,
    ...partial,
  };
}

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

describe("WorkspaceSkillsSurface", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    listMock.mockReset();
    detailMock.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function renderSurface() {
    await act(async () => {
      root?.render(<WorkspaceSkillsSurface />);
    });
    await act(flush);
  }

  it("渲染运行时技能目录，点击行打开详情对话框并现取最新定义", async () => {
    listMock.mockResolvedValue({
      skills: [
        skill({ name: "aicli", category: "platform", version: "1.0.0", description: "委托本地 aicli" }),
        skill({ name: "imagegen", category: "media", tags: ["image"] }),
      ],
      count: 2,
    });
    detailMock.mockResolvedValue(
      skill({
        name: "aicli",
        category: "platform",
        version: "1.2.0",
        description: "委托本地 aicli（热重载后的最新定义）",
      }),
    );

    await renderSurface();

    expect(document.body.querySelectorAll('[data-testid="workspace-skills-row"]')).toHaveLength(2);
    expect(document.body.querySelector('[data-testid="workspace-skills-count"]')?.textContent).toContain(
      "共 2 个技能",
    );
    expect(document.body.querySelector('[data-testid="workspace-skills-dialog"]')).toBeNull();

    const firstRow = document.body.querySelector('[data-testid="workspace-skills-row"]');
    await act(async () => {
      firstRow?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await act(flush);

    expect(detailMock).toHaveBeenCalledWith(
      "aicli",
      expect.objectContaining({ signal: expect.anything() }),
    );
    expect(document.body.querySelector('[data-testid="workspace-skills-dialog"]')).not.toBeNull();
    expect(document.body.textContent).toContain("技能详情");
    expect(document.body.textContent).toContain("委托本地 aicli（热重载后的最新定义）");
  });

  it("详情对话框可关闭", async () => {
    listMock.mockResolvedValue({ skills: [skill({ name: "imagegen" })], count: 1 });
    detailMock.mockResolvedValue(skill({ name: "imagegen" }));

    await renderSurface();

    await act(async () => {
      document.body
        .querySelector('[data-testid="workspace-skills-row"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await act(flush);
    expect(document.body.querySelector('[data-testid="workspace-skills-dialog"]')).not.toBeNull();

    await act(async () => {
      document.body
        .querySelector('[data-testid="workspace-skills-dialog-close"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(document.body.querySelector('[data-testid="workspace-skills-dialog"]')).toBeNull();
  });

  it("目录加载失败：按真实错误呈现并可重试（不伪装空目录）", async () => {
    listMock.mockRejectedValue(new Error("skills backend down"));

    await renderSurface();

    const errorBox = document.body.querySelector('[data-testid="workspace-skills-error"]');
    expect(errorBox).not.toBeNull();
    expect(errorBox?.textContent).toContain("请求失败，请查看日志后重试。");
    expect(document.body.querySelector('[data-testid="workspace-skills-empty"]')).toBeNull();

    await act(async () => {
      errorBox?.querySelector("button")?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(listMock).toHaveBeenCalledTimes(2);
  });

  it("目录为空：显示空态", async () => {
    listMock.mockResolvedValue({ skills: [], count: 0 });

    await renderSurface();

    expect(document.body.querySelector('[data-testid="workspace-skills-empty"]')).not.toBeNull();
    expect(document.body.querySelectorAll('[data-testid="workspace-skills-row"]')).toHaveLength(0);
  });
});
