// @vitest-environment jsdom

// P4-1 写操作状态机单测：乐观搬移 → 服务端结论覆盖 / 失败回滚 / 单飞 / 结论缺失不伪造空状态。
//
// 断言纪律：只断言「用户可见结论」——写操作在途时按钮必须能拿到 pending 信号、失败必须回到请求前快照、
// 服务端返回 status 时以服务端为准；diff 内容不是本测试对象（只断言被操作文件的 diff 会重新拉取）。

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { GitDiffResult, GitStatusResult } from "@/types/runtime/git-browse";

import { useGitChanges, type UseGitChangesResult } from "./use-git-changes";

const { fetchFsRootsMock, fetchGitStatusMock, fetchGitDiffMock, fetchGitCommitsMock, submitGitStageMock } = vi.hoisted(() => ({
  fetchFsRootsMock: vi.fn(),
  fetchGitStatusMock: vi.fn(),
  fetchGitDiffMock: vi.fn(),
  fetchGitCommitsMock: vi.fn(),
  submitGitStageMock: vi.fn(),
}));

vi.mock("@/api/runtime/fs-roots", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-roots")>();
  return { ...actual, fetchFsRoots: fetchFsRootsMock };
});

// 只替换请求函数：`isGitRepoMissing` 等降级判据必须走真实实现。
vi.mock("@/api/runtime/git", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/git")>();
  return {
    ...actual,
    fetchGitStatus: fetchGitStatusMock,
    fetchGitDiff: fetchGitDiffMock,
    fetchGitCommits: fetchGitCommitsMock,
    submitGitStage: submitGitStageMock,
  };
});

type ReactActEnvironmentGlobal = typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };

let container: HTMLDivElement;
let root: Root | null = null;

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

function file(path: string, status: string) {
  return { path, status, insertions: 1, deletions: 1, binary: false };
}

function statusResult(overrides: Partial<GitStatusResult> = {}): GitStatusResult {
  return {
    repo: { root: "E:/ws", branch: "main", detached: false, head: "abc", ahead: 0, behind: 0, isBare: false },
    clean: false,
    staged: [],
    unstaged: [],
    untracked: [],
    conflicts: [],
    warnings: [],
    generatedAt: 1,
    ...overrides,
  };
}

const DIFF_FIXTURE = {
  file: { path: "a.ts", status: ".M", isBinary: false, isSubmodule: false },
  target: "working",
  context: 3,
  whitespace: "show",
  insertions: 1,
  deletions: 0,
  hunks: [],
  raw: "",
  parseError: "",
} as unknown as GitDiffResult;

const COMMITS_FIXTURE = { commits: [], nextCursor: null, hasMore: false, warnings: [] };

function probeHolder() {
  const holderRef: { current: UseGitChangesResult | null } = { current: null };
  return holderRef;
}

function Harness({ holderRef }: { holderRef: { current: UseGitChangesResult | null } }) {
  const result = useGitChanges({ sessionId: "s1", workspacePath: "E:/ws" });
  // 测试探针：在 effect 里写外部 ref（渲染期写 ref 会被 react-hooks/refs 拦下）。
  useEffect(() => {
    holderRef.current = result;
  });
  return null;
}

async function settle() {
  for (let index = 0; index < 4; index += 1) {
    await act(async () => {
      await flush();
    });
  }
}

async function mount(holder: { current: UseGitChangesResult | null }) {
  await act(async () => {
    root?.render(<Harness holderRef={holder} />);
  });
  await settle();
}

describe("useGitChanges 写操作（stage/unstage）", () => {
  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    fetchFsRootsMock.mockReset();
    fetchGitStatusMock.mockReset();
    fetchGitDiffMock.mockReset();
    fetchGitCommitsMock.mockReset();
    submitGitStageMock.mockReset();

    fetchFsRootsMock.mockResolvedValue({
      roots: [{ scope: "session:s1", kind: "session", name: "ws", path: "E:/ws", exists: true, isGitRepo: true }],
    });
    fetchGitStatusMock.mockResolvedValue(statusResult({ unstaged: [file("a.ts", ".M")] }));
    fetchGitDiffMock.mockResolvedValue(DIFF_FIXTURE);
    fetchGitCommitsMock.mockResolvedValue(COMMITS_FIXTURE);

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
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("成功：先乐观搬到「已暂存」，再以服务端 status 覆盖，并重新拉取被操作文件的 diff", async () => {
    const serverStatus = statusResult({ staged: [file("a.ts", "M.")] });
    submitGitStageMock.mockResolvedValue({ action: "stage", files: ["a.ts"], status: serverStatus, generatedAt: 2 });

    const holder = probeHolder();
    await mount(holder);
    const diffCallsBefore = fetchGitDiffMock.mock.calls.length;

    act(() => holder.current?.stageFiles(["a.ts"], "stage"));

    // 乐观阶段：本地已搬移，且 UI 能拿到在途信号（按钮据此禁用）。
    expect(holder.current?.stagePending).toBe(true);
    expect(holder.current?.stagingPaths).toEqual(["a.ts"]);
    expect(holder.current?.status.data?.staged.map((item) => item.path)).toEqual(["a.ts"]);
    expect(holder.current?.status.data?.unstaged).toEqual([]);

    await settle();

    expect(submitGitStageMock).toHaveBeenCalledWith(
      { scope: "session:s1", path: "E:/ws", action: "stage", files: ["a.ts"] },
      expect.objectContaining({ signal: expect.anything() }),
    );
    // 服务端是唯一结论：对象身份就是服务端返回的那份。
    expect(holder.current?.status.data).toBe(serverStatus);
    expect(holder.current?.stagePending).toBe(false);
    expect(holder.current?.stagingPaths).toEqual([]);
    expect(holder.current?.stageError).toBeNull();
    expect(fetchGitDiffMock.mock.calls.length).toBeGreaterThan(diffCallsBefore);
  });

  it("失败：回滚到请求前快照并给出错误；clearStageError 只清错误不吞结论", async () => {
    const baseline = statusResult({ unstaged: [file("a.ts", ".M")] });
    fetchGitStatusMock.mockResolvedValue(baseline);
    submitGitStageMock.mockRejectedValue(new Error("boom"));

    const holder = probeHolder();
    await mount(holder);

    act(() => holder.current?.stageFiles(["a.ts"], "stage"));
    await settle();

    expect(holder.current?.status.data).toEqual(baseline);
    expect(holder.current?.stagePending).toBe(false);
    expect(holder.current?.stageError).toBeInstanceOf(Error);
    expect((holder.current?.stageError as Error).message).toBe("boom");

    act(() => holder.current?.clearStageError());
    expect(holder.current?.stageError).toBeNull();
    expect(holder.current?.status.data).toEqual(baseline);
  });

  it("单飞：在途时重复调用（含取消暂存）被忽略，不会把同一文件 add 两次", async () => {
    submitGitStageMock.mockReturnValue(new Promise(() => undefined));

    const holder = probeHolder();
    await mount(holder);

    act(() => holder.current?.stageFiles(["a.ts"], "stage"));
    act(() => holder.current?.stageFiles(["a.ts"], "stage"));
    act(() => holder.current?.stageFiles(["a.ts"], "unstage"));

    expect(submitGitStageMock).toHaveBeenCalledTimes(1);
    expect(holder.current?.stagePending).toBe(true);
  });

  it("空文件列表是真正的空操作（不发请求、不改状态）", async () => {
    const holder = probeHolder();
    await mount(holder);

    act(() => holder.current?.stageFiles([], "stage"));

    expect(submitGitStageMock).not.toHaveBeenCalled();
    expect(holder.current?.stagePending).toBe(false);
  });

  it("服务端未返回 status：保留乐观结果，不退化成「工作区干净」", async () => {
    submitGitStageMock.mockResolvedValue({ action: "stage", files: ["a.ts"], status: null, generatedAt: 3 });

    const holder = probeHolder();
    await mount(holder);

    act(() => holder.current?.stageFiles(["a.ts"], "stage"));
    await settle();

    expect(holder.current?.status.data?.staged.map((item) => item.path)).toEqual(["a.ts"]);
    expect(holder.current?.stageError).toBeNull();
    expect(holder.current?.stagePending).toBe(false);
  });
});
