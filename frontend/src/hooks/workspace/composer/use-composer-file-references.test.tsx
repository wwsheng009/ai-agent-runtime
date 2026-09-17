// @vitest-environment jsdom

// P0-5：composer `@` 工作区文件引用 hook 单测。
//
// 断言口径：只依赖归一化后的可见状态（group/status/scope/请求参数），
// 竞态用可控 deferred 精确构造「旧响应后到」的顺序；roots 端点降级按 404/503 语义模拟。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import { FS_SEARCH_DEBOUNCE_COMPOSER_MS } from "@/hooks/workspace/use-fs-search";
import type {
  FsEntry,
  FsListingResult,
  FsRoot,
  FsSearchItem,
  FsSearchResult,
} from "@/types/runtime/fs-browser";

const { fetchFsRootsMock, fetchFsListingMock, fetchFsSearchMock, isFsRootsUnavailableMock } =
  vi.hoisted(() => ({
    fetchFsRootsMock: vi.fn(),
    fetchFsListingMock: vi.fn(),
    fetchFsSearchMock: vi.fn(),
    isFsRootsUnavailableMock: vi.fn(() => false),
  }));

vi.mock("@/api/runtime/fs-roots", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-roots")>();
  return {
    ...actual,
    fetchFsRoots: fetchFsRootsMock,
    isFsRootsUnavailable: isFsRootsUnavailableMock,
  };
});

vi.mock("@/api/runtime/fs-list", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-list")>();
  return { ...actual, fetchFsListing: fetchFsListingMock };
});

vi.mock("@/api/runtime/fs-search", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-search")>();
  return { ...actual, fetchFsSearch: fetchFsSearchMock };
});

import {
  COMPOSER_FILE_REFERENCE_GROUP_ID,
  COMPOSER_FILE_REFERENCE_LIMIT,
  clearComposerFileReferenceRootCache,
  useComposerFileReferences,
  type ComposerFileReferencesOptions,
  type ComposerFileReferencesResult,
} from "./use-composer-file-references";

type ReactActEnvironmentGlobal = typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };

const LABELS = {
  group: "工作区文件",
  loading: "加载中…",
  empty: "没有匹配文件",
  error: "工作区文件不可用",
  truncated: "结果已截断",
};

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, reject, resolve };
}

function root(partial: Partial<FsRoot> & { scope: string }): FsRoot {
  return {
    kind: "session",
    name: partial.scope,
    path: "/workspace",
    exists: true,
    isGitRepo: false,
    ...partial,
  };
}

function entry(partial: Partial<FsEntry> & { name: string; path: string }): FsEntry {
  return { type: "file", size: 12, mtime: 0, ...partial };
}

function searchItem(partial: Partial<FsSearchItem> & { name: string; path: string }): FsSearchItem {
  return { type: "file", size: 12, mtime: 0, score: 90, ...partial };
}

function searchPage(overrides: Partial<FsSearchResult> = {}): FsSearchResult {
  return {
    scope: "session:s1",
    query: "compmenu",
    base: "",
    items: [],
    nextCursor: null,
    hasMore: false,
    scanned: 5,
    truncated: false,
    truncatedReasons: [],
    elapsedMs: 2,
    limit: COMPOSER_FILE_REFERENCE_LIMIT,
    ...overrides,
  };
}

/** 推进 P1 搜索防抖并落定微任务（composer 固定 200ms）。 */
async function advanceDebounce(ms = FS_SEARCH_DEBOUNCE_COMPOSER_MS) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
    await flush();
    await vi.advanceTimersByTimeAsync(0);
  });
}

/**
 * 落定请求 Promise 链 + setState + 重渲染：act 之外的 setState 经 React 调度器（setImmediate）
 * 排队，fake timers 下不推进 0ms 就永远不会提交（与 use-fs-search.test.tsx 同口径）。
 */
async function settle() {
  await act(async () => {
    for (let index = 0; index < 4; index += 1) {
      await flush();
      await vi.advanceTimersByTimeAsync(0);
    }
  });
}

function page(overrides: Partial<FsListingResult> = {}): FsListingResult {
  return {
    dir: { path: "", absPath: "/workspace", parent: "", isRoot: true },
    entries: [],
    nextCursor: null,
    hasMore: false,
    truncated: false,
    sort: "type_then_name",
    ...overrides,
  };
}

describe("useComposerFileReferences", () => {
  let container: HTMLDivElement;
  let reactRoot: Root | null = null;
  let latest: ComposerFileReferencesResult | null = null;
  let options: ComposerFileReferencesOptions;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    reactRoot = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    latest = null;
    options = { sessionId: "s1", query: "", enabled: true, labels: LABELS };
    clearComposerFileReferenceRootCache();
    fetchFsRootsMock.mockReset();
    fetchFsListingMock.mockReset();
    fetchFsSearchMock.mockReset();
    isFsRootsUnavailableMock.mockReset();
    isFsRootsUnavailableMock.mockReturnValue(false);
  });

  afterEach(() => {
    if (reactRoot) {
      act(() => reactRoot?.unmount());
    }
    reactRoot = null;
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function Harness({
    next,
    onSnapshot,
  }: {
    next: ComposerFileReferencesOptions;
    onSnapshot: (result: ComposerFileReferencesResult) => void;
  }) {
    onSnapshot(useComposerFileReferences(next));
    return null;
  }

  /** 渲染期快照：与仓库既有 hook 测试同款（不在组件体内直接赋值外层变量）。 */
  function snapshot(result: ComposerFileReferencesResult) {
    latest = result;
  }

  function render(overrides: Partial<ComposerFileReferencesOptions> = {}) {
    options = { ...options, ...overrides };
    act(() => {
      reactRoot?.render(<Harness next={options} onSnapshot={snapshot} />);
    });
  }

  async function rerender(overrides: Partial<ComposerFileReferencesOptions>) {
    options = { ...options, ...overrides };
    await act(async () => {
      reactRoot?.render(<Harness next={options} onSnapshot={snapshot} />);
      await flush();
    });
  }

  it("解析会话根并拉取根层首屏小批量：只保留文件、过滤目录与内部项", async () => {
    fetchFsRootsMock.mockResolvedValue({
      roots: [
        root({ scope: "workspace:1", kind: "workspace" }),
        root({ scope: "session:s1", kind: "session" }),
      ],
      skipped: 0,
    });
    fetchFsListingMock.mockResolvedValue(
      page({
        entries: [
          entry({ name: "index.ts", path: "index.ts" }),
          entry({ name: "src", path: "src", type: "dir" }),
          entry({ name: "secret.json", path: ".aicli/secret.json", internal: true }),
          entry({ name: "link.ts", path: "link.ts", type: "symlink" }),
        ],
        hasMore: true,
        truncated: true,
      }),
    );

    render();
    await act(async () => {
      await flush();
    });

    expect(fetchFsRootsMock).toHaveBeenCalledTimes(1);
    expect(fetchFsRootsMock.mock.calls[0][0]).toMatchObject({ sessionId: "s1" });

    const [request] = fetchFsListingMock.mock.calls.at(-1) as [
      Record<string, unknown>,
      { signal?: AbortSignal },
    ];
    expect(request).toMatchObject({
      scope: "session:s1",
      path: "",
      limit: COMPOSER_FILE_REFERENCE_LIMIT,
      sort: "type_then_name",
      showHidden: false,
      dirsFirst: true,
    });

    expect(latest?.scope).toBe("session:s1");
    expect(latest?.group?.id).toBe(COMPOSER_FILE_REFERENCE_GROUP_ID);
    expect(latest?.group?.label).toBe("工作区文件");
    expect(latest?.group?.items.map((item) => item.label)).toEqual(["index.ts"]);
    expect(latest?.group?.items[0]).toMatchObject({
      insertText: "index.ts",
      description: "index.ts",
    });
    expect(latest?.group?.status).toBe("ready");
    expect(latest?.group?.hasMore).toBe(true);
    expect(latest?.group?.truncated).toBe(true);
    expect(latest?.group?.truncatedText).toBe("结果已截断");
  });

  it("菜单关闭（enabled=false）不发请求，打开后才解析并拉取", async () => {
    fetchFsRootsMock.mockResolvedValue({ roots: [root({ scope: "session:s1" })], skipped: 0 });
    fetchFsListingMock.mockResolvedValue(page({ entries: [entry({ name: "a.ts", path: "a.ts" })] }));

    render({ enabled: false });
    await act(async () => {
      await flush();
    });
    expect(fetchFsRootsMock).not.toHaveBeenCalled();
    expect(fetchFsListingMock).not.toHaveBeenCalled();

    await rerender({ enabled: true });
    expect(fetchFsRootsMock).toHaveBeenCalledTimes(1);
    expect(fetchFsListingMock).toHaveBeenCalledTimes(1);
    expect(latest?.group?.items.map((item) => item.label)).toEqual(["a.ts"]);
  });

  it("同一会话重复打开复用作用域缓存：不再重复请求 fs/roots", async () => {
    fetchFsRootsMock.mockResolvedValue({ roots: [root({ scope: "session:s1" })], skipped: 0 });
    fetchFsListingMock.mockResolvedValue(page({ entries: [entry({ name: "a.ts", path: "a.ts" })] }));

    render();
    await act(async () => {
      await flush();
    });
    await rerender({ enabled: false });
    await rerender({ enabled: true });

    expect(fetchFsRootsMock).toHaveBeenCalledTimes(1);
    expect(fetchFsListingMock).toHaveBeenCalledTimes(2);
  });

  it("fs/roots 不可用（404/503）：不给分组，且不发起列表请求（宿主回退 artifacts）", async () => {
    isFsRootsUnavailableMock.mockReturnValue(true);
    fetchFsRootsMock.mockRejectedValue(new DOMException("unavailable", "Error"));

    render();
    await act(async () => {
      await flush();
    });

    expect(latest?.group).toBeNull();
    expect(latest?.scope).toBeNull();
    expect(fetchFsListingMock).not.toHaveBeenCalled();
  });

  it("没有可用根（exists=false / roots 为空）：不给分组", async () => {
    fetchFsRootsMock.mockResolvedValue({
      roots: [root({ scope: "session:s1", exists: false })],
      skipped: 0,
    });

    render();
    await act(async () => {
      await flush();
    });

    expect(latest?.group).toBeNull();
    expect(fetchFsListingMock).not.toHaveBeenCalled();
  });

  it("首屏加载中：空组也带 loading 状态进入菜单（打开瞬间不闪空）", async () => {
    fetchFsRootsMock.mockResolvedValue({ roots: [root({ scope: "session:s1" })], skipped: 0 });
    const pending = deferred<FsListingResult>();
    fetchFsListingMock.mockReturnValue(pending.promise);

    render();
    await act(async () => {
      await flush();
    });

    expect(latest?.group).toMatchObject({
      id: COMPOSER_FILE_REFERENCE_GROUP_ID,
      status: "loading",
      statusText: "加载中…",
      items: [],
    });
  });

  it("切换会话：清空旧结果并作废在途请求（旧响应后到被丢弃）", async () => {
    const staleList = deferred<FsListingResult>();
    const freshList = deferred<FsListingResult>();
    fetchFsRootsMock
      .mockResolvedValueOnce({ roots: [root({ scope: "session:s1" })], skipped: 0 })
      .mockResolvedValueOnce({ roots: [root({ scope: "session:s2" })], skipped: 0 });
    fetchFsListingMock.mockReturnValueOnce(staleList.promise).mockReturnValueOnce(freshList.promise);

    render();
    await act(async () => {
      await flush();
    });

    await rerender({ sessionId: "s2" });
    await act(async () => {
      freshList.resolve(page({ entries: [entry({ name: "b.ts", path: "b.ts" })] }));
      await flush();
    });
    await act(async () => {
      staleList.resolve(page({ entries: [entry({ name: "a.ts", path: "a.ts" })] }));
      await flush();
    });

    expect(latest?.scope).toBe("session:s2");
    expect(latest?.group?.items.map((item) => item.label)).toEqual(["b.ts"]);
  });

  it("主动取消（AbortError）不落 error 态", async () => {
    fetchFsRootsMock.mockResolvedValue({ roots: [root({ scope: "session:s1" })], skipped: 0 });
    const pending = deferred<FsListingResult>();
    fetchFsListingMock.mockReturnValue(pending.promise);

    render();
    await act(async () => {
      await flush();
    });
    await rerender({ enabled: false });
    await act(async () => {
      pending.reject(new DOMException("aborted", "AbortError"));
      await flush();
    });

    expect(latest?.group?.status).not.toBe("error");
    expect(latest?.status).not.toBe("error");
  });

  it("拉取失败：保留上一次成功结果，仅置 error 态供 UI 提示", async () => {
    fetchFsRootsMock.mockResolvedValue({ roots: [root({ scope: "session:s1" })], skipped: 0 });
    fetchFsListingMock.mockResolvedValueOnce(
      page({ entries: [entry({ name: "keep.ts", path: "keep.ts" })] }),
    );

    render();
    await act(async () => {
      await flush();
    });
    expect(latest?.group?.items.map((item) => item.label)).toEqual(["keep.ts"]);

    fetchFsListingMock.mockRejectedValueOnce(new Error("boom"));
    await rerender({ enabled: false });
    await rerender({ enabled: true });

    expect(latest?.group?.items.map((item) => item.label)).toEqual(["keep.ts"]);
    expect(latest?.group?.status).toBe("error");
    expect(latest?.group?.statusText).toBe("工作区文件不可用");
  });

  it("P1：query 非空防抖后发 fs/search，结果以 serverFiltered 分组下发（不再重复拉首屏）", async () => {
    vi.useFakeTimers();
    try {
      fetchFsRootsMock.mockResolvedValue({ roots: [root({ scope: "session:s1" })], skipped: 0 });
      fetchFsListingMock.mockResolvedValue(
        page({ entries: [entry({ name: "a.ts", path: "a.ts" })] }),
      );
      fetchFsSearchMock.mockResolvedValue(
        searchPage({
          items: [
            searchItem({ name: "composer-menu.ts", path: "frontend/src/lib/composer-menu.ts" }),
            searchItem({ name: "src", path: "frontend/src", type: "dir" }),
            searchItem({ name: "link.ts", path: "link.ts", type: "unknown" }),
          ],
        }),
      );

      render();
      await settle();
      expect(fetchFsListingMock).toHaveBeenCalledTimes(1);

      await rerender({ query: "compmenu" });
      // 防抖未到点：不发请求（此时仍展示首屏结果，不闪空）。
      expect(fetchFsSearchMock).not.toHaveBeenCalled();

      await advanceDebounce();
      await settle();

      const [request, init] = fetchFsSearchMock.mock.calls.at(-1) as [
        Record<string, unknown>,
        { signal?: AbortSignal },
      ];
      expect(request).toMatchObject({
        scope: "session:s1",
        query: "compmenu",
        cursor: null,
        limit: COMPOSER_FILE_REFERENCE_LIMIT,
        kinds: "file",
        showHidden: false,
      });
      expect(init.signal).toBeInstanceOf(AbortSignal);

      // 远端已过滤/排序：原样下发（serverFiltered=true），且防御性过滤掉非文件项。
      expect(latest?.group?.serverFiltered).toBe(true);
      expect(latest?.group?.items.map((item) => item.insertText)).toEqual([
        "frontend/src/lib/composer-menu.ts",
      ]);
      expect(latest?.group?.items[0]).toMatchObject({
        label: "composer-menu.ts",
        description: "frontend/src/lib/composer-menu.ts",
      });
      expect(latest?.group?.status).toBe("ready");
      expect(latest?.status).toBe("ready");
      // query 非空期间不再重复拉首屏。
      expect(fetchFsListingMock).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("P1：连续输入只有最后响应写回（旧响应后到被丢弃）", async () => {
    vi.useFakeTimers();
    try {
      fetchFsRootsMock.mockResolvedValue({ roots: [root({ scope: "session:s1" })], skipped: 0 });
      fetchFsListingMock.mockResolvedValue(page({ entries: [] }));
      const slow = deferred<FsSearchResult>();
      const fast = deferred<FsSearchResult>();
      fetchFsSearchMock.mockReturnValueOnce(slow.promise).mockReturnValueOnce(fast.promise);

      render();
      await settle();

      await rerender({ query: "comp" });
      await advanceDebounce();
      await rerender({ query: "compmenu" });
      await advanceDebounce();
      expect(fetchFsSearchMock).toHaveBeenCalledTimes(2);

      fast.resolve(
        searchPage({ query: "compmenu", items: [searchItem({ name: "b.ts", path: "b.ts" })] }),
      );
      await settle();
      slow.resolve(searchPage({ query: "comp", items: [searchItem({ name: "a.ts", path: "a.ts" })] }));
      await settle();

      expect(latest?.group?.items.map((item) => item.insertText)).toEqual(["b.ts"]);
      expect(latest?.group?.status).toBe("ready");
    } finally {
      vi.useRealTimers();
    }
  });

  it("P1：搜索失败保留上次结果并置 error（下次输入自动重试）", async () => {
    vi.useFakeTimers();
    try {
      fetchFsRootsMock.mockResolvedValue({ roots: [root({ scope: "session:s1" })], skipped: 0 });
      fetchFsListingMock.mockResolvedValue(page({ entries: [] }));
      fetchFsSearchMock
        .mockResolvedValueOnce(
          searchPage({ items: [searchItem({ name: "a.ts", path: "a.ts" })] }),
        )
        .mockRejectedValueOnce(new Error("boom"));

      render();
      await settle();

      await rerender({ query: "a" });
      await advanceDebounce();
      await settle();
      expect(latest?.group?.items.map((item) => item.insertText)).toEqual(["a.ts"]);

      await rerender({ query: "ab" });
      await advanceDebounce();
      await settle();

      expect(latest?.group?.status).toBe("error");
      expect(latest?.group?.items.map((item) => item.insertText)).toEqual(["a.ts"]);
      expect(latest?.group?.statusText).toBe("工作区文件不可用");
    } finally {
      vi.useRealTimers();
    }
  });

  it("P1：搜索端点 404 → 粘性降级回首屏 + 客户端过滤（不再打搜索请求）", async () => {
    vi.useFakeTimers();
    try {
      fetchFsRootsMock.mockResolvedValue({ roots: [root({ scope: "session:s1" })], skipped: 0 });
      fetchFsListingMock.mockResolvedValue(
        page({ entries: [entry({ name: "composer-menu.ts", path: "composer-menu.ts" })] }),
      );
      fetchFsSearchMock.mockRejectedValueOnce(new RuntimeApiError(404, { code: "not_found" }));

      render();
      await settle();

      await rerender({ query: "comp" });
      await advanceDebounce();
      await settle();
      expect(fetchFsSearchMock).toHaveBeenCalledTimes(1);

      // 降级：复用已加载的首屏结果，交回菜单本地过滤（不带 serverFiltered）。
      expect(latest?.group?.serverFiltered).not.toBe(true);
      expect(latest?.group?.status).toBe("ready");
      expect(latest?.group?.items.map((item) => item.insertText)).toEqual(["composer-menu.ts"]);
      expect(fetchFsListingMock).toHaveBeenCalledTimes(1);

      // 粘性：继续输入也不再打搜索端点，首屏也不重复拉。
      await rerender({ query: "compos" });
      await advanceDebounce();
      await settle();
      expect(fetchFsSearchMock).toHaveBeenCalledTimes(1);
      expect(fetchFsListingMock).toHaveBeenCalledTimes(1);
      expect(latest?.group?.serverFiltered).not.toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});
