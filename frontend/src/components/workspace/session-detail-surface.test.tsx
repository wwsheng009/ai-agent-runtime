// @vitest-environment jsdom

// 「会话详情」面级单测：只断言用户可见的口径——
//   * 状态徽标与关键字段来自会话快照，缺失字段不渲染空行；
//   * 读取失败如实给出原因并允许重试（不白屏、不静默吞错）；
//   * 无会话时不发请求，给出空态。
// 刷新语义（会话切换作废在途请求）由 hook 的请求序号实现，不在面级重复覆盖。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type RuntimeSessionRecord } from "@/types/runtime";

import { type WorkspacePanelThreadRelation } from "./panel-registry";
import { SessionDetailSurface } from "./session-detail-surface";

const { getRuntimeSessionMock } = vi.hoisted(() => ({
  getRuntimeSessionMock: vi.fn(),
}));

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return { ...actual, getRuntimeSession: getRuntimeSessionMock };
});

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

let container: HTMLDivElement | null = null;
let root: Root | null = null;

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

function sessionFixture(
  overrides: Partial<RuntimeSessionRecord> = {},
): RuntimeSessionRecord {
  return {
    id: "session-1",
    userId: "alice",
    state: "active",
    metadata: {
      title: "右栏详情会话",
      summary: "用于验证会话详情面。",
      tags: ["alpha", "alpha", " beta "],
      totalTurns: 7,
      lastAgent: "coding-agent",
      lastModel: "gpt-test",
      createdBy: "web",
    },
    createdAt: "2026-09-16T02:00:00.000Z",
    updatedAt: "2026-09-16T03:00:00.000Z",
    ...overrides,
  };
}

function fieldText(key: string) {
  return (
    document.body.querySelector(`[data-testid="session-detail-field-${key}"]`)
      ?.textContent ?? ""
  );
}

async function mountSurface(
  sessionId = "session-1",
  workspacePath?: string,
  threadRelation?: WorkspacePanelThreadRelation,
) {
  await act(async () => {
    root?.render(
      <SessionDetailSurface
        sessionId={sessionId}
        threadRelation={threadRelation}
        workspacePath={workspacePath}
      />,
    );
  });
  await act(async () => {
    await flush();
  });
}

function relationNode(testId: string) {
  return document.body.querySelector(`[data-testid="${testId}"]`);
}

function findButtonByText(text: string) {
  return [...document.body.querySelectorAll("button")].find((button) =>
    button.textContent?.includes(text),
  );
}

describe("SessionDetailSurface", () => {
  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    getRuntimeSessionMock.mockReset();
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container?.remove();
    container = null;
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("渲染状态徽标与关键字段，空字段不占位", async () => {
    getRuntimeSessionMock.mockResolvedValue({
      session: sessionFixture({ state: "closed" }),
    });

    await mountSurface("session-1", "E:/ws");

    expect(getRuntimeSessionMock).toHaveBeenCalledWith("session-1");
    expect(
      document.body.querySelector('[data-testid="session-detail-state"]')
        ?.textContent,
    ).toBe("已关闭");
    expect(fieldText("id")).toBe("session-1");
    expect(fieldText("workspace")).toBe("E:/ws");
    expect(fieldText("totalTurns")).toBe("7");
    expect(fieldText("tags")).toBe("alpha, beta");
    expect(fieldText("updatedAt")).toBe(
      new Date("2026-09-16T03:00:00.000Z").toLocaleString(),
    );
    // 快照未提供 lastSkill / expiresAt：不渲染对应行，也不写「未知」。
    expect(fieldText("lastSkill")).toBe("");
    expect(fieldText("expiresAt")).toBe("");
  });

  it("未绑定工作目录时如实说明，不臆造路径", async () => {
    getRuntimeSessionMock.mockResolvedValue({ session: sessionFixture() });

    await mountSurface("session-1");

    expect(fieldText("workspace")).toBe("未绑定工作目录");
  });

  it("未知状态回落为「未知状态」而不是默认文案", async () => {
    getRuntimeSessionMock.mockResolvedValue({
      session: sessionFixture({ state: "mystery" }),
    });

    await mountSurface("session-1");

    expect(
      document.body.querySelector('[data-testid="session-detail-state"]')
        ?.textContent,
    ).toBe("未知状态");
  });

  it("读取失败给出原因并可重试", async () => {
    getRuntimeSessionMock
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce({ session: sessionFixture() });

    await mountSurface("session-1");

    const panelText = document.body.textContent ?? "";
    expect(panelText).toContain("会话详情加载失败");
    expect(panelText).toContain("boom");

    const retry = findButtonByText("重试");
    expect(retry).toBeInstanceOf(HTMLButtonElement);

    await act(async () => {
      retry?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await act(async () => {
      await flush();
    });

    expect(getRuntimeSessionMock).toHaveBeenCalledTimes(2);
    expect(fieldText("id")).toBe("session-1");
  });

  it("无会话时不发请求，展示空态", async () => {
    await mountSurface("");

    expect(getRuntimeSessionMock).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("尚未选择会话");
  });

  it("给出关联快照时展示关联状态与传输通道，tooltip 同时带名称与解释", async () => {
    getRuntimeSessionMock.mockResolvedValue({ session: sessionFixture() });

    await mountSurface("session-1", undefined, {
      kind: "attached",
      transport: "live",
    });

    const relation = relationNode("session-detail-relation-state");
    expect(relation?.textContent).toBe("已附着运行时会话");
    expect(relation?.getAttribute("data-relation-kind")).toBe("attached");
    expect(relation?.getAttribute("title")).toContain("已附着运行时会话");
    expect(relation?.getAttribute("title")).toContain(
      "已附着到当前工作区流程中的运行时会话。",
    );

    const transport = relationNode("session-detail-relation-transport");
    expect(transport?.textContent).toBe("在线运行时");
    expect(transport?.getAttribute("data-transport-kind")).toBe("live");
    expect(transport?.getAttribute("title")).toContain(
      "本会话已附着实时运行时",
    );
  });

  it("关联四态用不同图标与文案：恢复态不自称已附着", async () => {
    getRuntimeSessionMock.mockResolvedValue({ session: sessionFixture() });

    await mountSurface("session-1", undefined, {
      kind: "restored",
      transport: "seeded",
    });

    expect(relationNode("session-detail-relation-state")?.textContent).toBe(
      "已恢复运行时会话",
    );
    expect(relationNode("session-detail-relation-transport")?.textContent).toBe(
      "预置预览",
    );
  });

  it("未提供关联快照时不渲染关联区块（独立挂载不臆造关联）", async () => {
    getRuntimeSessionMock.mockResolvedValue({ session: sessionFixture() });

    await mountSurface("session-1");

    expect(relationNode("session-detail-relation-state")).toBeNull();
    expect(relationNode("session-detail-relation-transport")).toBeNull();
  });
});
