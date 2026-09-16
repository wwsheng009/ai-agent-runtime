// @vitest-environment jsdom

// P0-6：文件浏览器数据 hook 单测（纯函数口径 + 竞态/取消/游标失效状态机）。
//
// 断言原则：只依赖「归一化后」的可见状态（layers 状态 / entries / hasMore / cursorExpired），
// 不假设请求次数之外的实现细节；竞态用可控 deferred 精确构造「旧响应后到」的顺序。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { FsEntry, FsListingResult } from "@/types/runtime/fs-browser";

const { fetchFsListingMock } = vi.hoisted(() => ({ fetchFsListingMock: vi.fn() }));

vi.mock("@/api/runtime/fs-list", () => ({
  fetchFsListing: fetchFsListingMock,
  isFsListingCursorError: (error: unknown) => error instanceof Error && error.message === "cursor_invalid",
}));

import {
  applyListingPage,
  createIdleListing,
  isAbortError,
  isFreshListing,
  useFileBrowser,
  type UseFileBrowserResult,
} from "./use-file-browser";

type ReactActEnvironmentGlobal = typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };

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

function entry(partial: Partial<FsEntry> & { name: string; path: string }): FsEntry {
  return { type: "file", size: 12, mtime: 0, ...partial };
}

function page(overrides: Partial<FsListingResult> = {}): FsListingResult {
  return {
    dir: { path: "", absPath: "E:/workspace", parent: "", isRoot: true },
    entries: [],
    nextCursor: null,
    hasMore: false,
    truncated: false,
    sort: "type_then_name",
    ...overrides,
  };
}

function Harness({ onSnapshot, scope }: { onSnapshot: (result: UseFileBrowserResult) => void; scope: string }) {
  onSnapshot(useFileBrowser({ scope }));
  return null;
}

describe("use-file-browser 纯函数", () => {
  it("createIdleListing 不臆造分页字段：无更多、无游标、非截断", () => {
    const listing = createIdleListing();
    expect(listing.status).toBe("idle");
    expect(listing.entries).toEqual([]);
    expect(listing.hasMore).toBe(false);
    expect(listing.nextCursor).toBeNull();
    expect(listing.truncated).toBe(false);
    expect(listing.cursorExpired).toBe(false);
  });

  it("applyListingPage：首页替换，续页按 path 去重追加，并原样采纳服务端分页标志", () => {
    const first = applyListingPage(
      undefined,
      page({ entries: [entry({ name: "a", path: "a" }), entry({ name: "b", path: "b" })], nextCursor: "c1", hasMore: true }),
      1_000,
    );
    expect(first.status).toBe("ready");
    expect(first.loadedAt).toBe(1_000);
    expect(first.hasMore).toBe(true);
    expect(first.nextCursor).toBe("c1");

    const second = applyListingPage(
      first,
      page({ entries: [entry({ name: "b", path: "b" }), entry({ name: "c", path: "c" })], nextCursor: null, hasMore: false, truncated: true }),
      2_000,
    );
    expect(second.entries.map((item) => item.path)).toEqual(["a", "b", "c"]);
    expect(second.entries.filter((item) => item.path === "b")).toHaveLength(1);
    expect(second.hasMore).toBe(false);
    expect(second.nextCursor).toBeNull();
    expect(second.truncated).toBe(true);
    expect(second.loadedAt).toBe(2_000);
  });

  it("isFreshListing：只在 TTL 内且 ready 时复用缓存", () => {
    const ready = { ...createIdleListing(), status: "ready" as const, loadedAt: 1_000 };
    expect(isFreshListing(ready, 1_500, 1_000)).toBe(true);
    expect(isFreshListing(ready, 2_500, 1_000)).toBe(false);
    expect(isFreshListing({ ...ready, status: "loading" }, 1_100, 1_000)).toBe(false);
    expect(isFreshListing(undefined, 1_100, 1_000)).toBe(false);
  });

  it("isAbortError：识别 DOMException/Error 两种 AbortError，普通错误不误判", () => {
    expect(isAbortError(new DOMException("aborted", "AbortError"))).toBe(true);
    const error = new Error("aborted");
    error.name = "AbortError";
    expect(isAbortError(error)).toBe(true);
    expect(isAbortError(new Error("boom"))).toBe(false);
    expect(isAbortError(undefined)).toBe(false);
  });
});

describe("useFileBrowser 状态机", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  let latest: UseFileBrowserResult | null = null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    latest = null;
    fetchFsListingMock.mockReset();
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

  function renderHook() {
    act(() => {
      root?.render(<Harness scope="workspace:1" onSnapshot={(result) => { latest = result; }} />);
    });
  }

  it("挂载即拉取根层，并在层里保留排序/隐藏开关的服务端分页参数", async () => {
    fetchFsListingMock.mockResolvedValue(page({ entries: [entry({ name: "notes.md", path: "notes.md" })] }));
    renderHook();
    await act(async () => {
      latest?.loadListing("", "first");
      await flush();
    });
    const [request, options] = fetchFsListingMock.mock.calls.at(-1) as [Record<string, unknown>, { signal?: AbortSignal }];
    expect(request).toMatchObject({ scope: "workspace:1", path: "", sort: "type_then_name", showHidden: false });
    expect(options.signal).toBeInstanceOf(AbortSignal);
    expect(latest?.listings[""]?.status).toBe("ready");
    expect(latest?.listings[""]?.entries.map((item) => item.name)).toEqual(["notes.md"]);
  });

  it("并发加载同一目录：旧响应后到时被丢弃，只保留最新一次结果", async () => {
    const stale = deferred<FsListingResult>();
    const fresh = deferred<FsListingResult>();
    // 挂载自动拉取消耗 #1，强制刷新消耗 #2；先回新、后回旧。
    fetchFsListingMock.mockReturnValueOnce(stale.promise).mockReturnValueOnce(fresh.promise);
    renderHook();
    await act(async () => {
      latest?.loadListing("", "first", true);
      await flush();
    });
    await act(async () => {
      fresh.resolve(page({ entries: [entry({ name: "new.md", path: "new.md" })] }));
      await flush();
    });
    await act(async () => {
      stale.resolve(page({ entries: [entry({ name: "stale.md", path: "stale.md" })] }));
      await flush();
    });
    expect(latest?.listings[""]?.entries.map((item) => item.name)).toEqual(["new.md"]);
  });

  it("卸载时中止在途请求，且主动取消不落 error 态", async () => {
    let signal: AbortSignal | undefined;
    fetchFsListingMock.mockImplementation((_request: unknown, options: { signal?: AbortSignal } = {}) => {
      signal = options.signal;
      return new Promise<FsListingResult>(() => {});
    });
    renderHook();
    await act(async () => {
      latest?.loadListing("", "first");
      await flush();
    });
    act(() => root?.unmount());
    root = null;
    expect(signal?.aborted).toBe(true);

    fetchFsListingMock.mockReset();
    fetchFsListingMock.mockRejectedValue(new DOMException("aborted", "AbortError"));
    root = createRoot(container);
    renderHook();
    await act(async () => {
      latest?.loadListing("", "first");
      await flush();
    });
    expect(latest?.listings[""]?.status ?? "idle").not.toBe("error");
  });

  it("游标失效：标记 cursorExpired 并复位分页，不重试同一游标", async () => {
    fetchFsListingMock.mockResolvedValueOnce(page({ entries: [entry({ name: "a", path: "a" })], nextCursor: "c1", hasMore: true }));
    renderHook();
    await act(flush);
    expect(latest?.listings[""]?.hasMore).toBe(true);
    expect(latest?.listings[""]?.nextCursor).toBe("c1");

    fetchFsListingMock.mockRejectedValueOnce(new Error("cursor_invalid"));
    await act(async () => {
      latest?.loadListing("", "more");
      await flush();
    });
    const listing = latest?.listings[""];
    expect(listing?.cursorExpired).toBe(true);
    expect(listing?.hasMore).toBe(false);
    expect(listing?.nextCursor).toBeNull();
    expect(fetchFsListingMock).toHaveBeenCalledTimes(2);
  });

  it("切换目录只影响新层：旧层状态保留，进入目录会带上当前排序", async () => {
    fetchFsListingMock.mockImplementation((request: { path?: string }) =>
      Promise.resolve(page({ entries: [entry({ name: `${request.path || "root"}.txt`, path: `${request.path || "root"}.txt` })] })),
    );
    renderHook();
    await act(async () => {
      latest?.loadListing("", "first");
      await flush();
    });
    await act(async () => {
      latest?.enterDir("nested");
      await flush();
    });
    await act(async () => {
      latest?.loadListing("nested", "first");
      await flush();
    });
    expect(Object.keys(latest?.listings ?? {}).sort()).toEqual(["", "nested"]);
    expect(latest?.currentDir).toBe("nested");
  });
});
