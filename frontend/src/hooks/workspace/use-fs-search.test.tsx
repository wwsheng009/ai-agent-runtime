// @vitest-environment jsdom

// P1-8：共享模糊搜索 hook 单测（防抖合并 / 竞态与 abort / 分页去重 / 游标失效 / 降级）。
//
// 断言口径：只看归一化后的可见状态（items/status/hasMore/truncated/unavailable）与请求参数；
// 竞态用可控 deferred 精确构造「旧响应后到」的顺序；防抖用 fake timers 推进。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import type { FsSearchItem, FsSearchResult } from "@/types/runtime/fs-browser";

const { fetchFsSearchMock } = vi.hoisted(() => ({
  fetchFsSearchMock: vi.fn(),
}));

vi.mock("@/api/runtime/fs-search", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-search")>();
  return { ...actual, fetchFsSearch: fetchFsSearchMock };
});

import {
  FS_SEARCH_DEBOUNCE_COMPOSER_MS,
  useFsSearchQuery,
  type UseFsSearchQueryOptions,
  type UseFsSearchQueryResult,
} from "./use-fs-search";

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

function item(path: string, overrides: Partial<FsSearchItem> = {}): FsSearchItem {
  return {
    name: path.split("/").pop() ?? path,
    path,
    type: "file",
    size: 10,
    mtime: 0,
    score: 100,
    ...overrides,
  };
}

function page(overrides: Partial<FsSearchResult> = {}): FsSearchResult {
  return {
    scope: "session:s1",
    query: "comp",
    base: "",
    items: [],
    nextCursor: null,
    hasMore: false,
    scanned: 5,
    truncated: false,
    truncatedReasons: [],
    elapsedMs: 3,
    limit: 20,
    ...overrides,
  };
}

/** 推进防抖并落定微任务（默认 debounce 200ms）。 */
async function advance(ms = FS_SEARCH_DEBOUNCE_COMPOSER_MS) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
    await flush();
  });
}

/**
 * 落定请求 Promise 链 + setState + 重渲染（resolve 后的断言必须走这里，单个 flush 不够）。
 * 必须同时推进 0ms 定时器：act 之外的 setState 会经 React 调度器（setImmediate）排队，
 * fake timers 下不推进就永远不会提交。
 */
async function settle() {
  await act(async () => {
    for (let i = 0; i < 4; i += 1) {
      await flush();
      await vi.advanceTimersByTimeAsync(0);
    }
  });
}

function Harness({
  onSnapshot,
  options,
}: {
  onSnapshot: (result: UseFsSearchQueryResult) => void;
  options: UseFsSearchQueryOptions;
}) {
  onSnapshot(useFsSearchQuery(options));
  return null;
}

let latest: UseFsSearchQueryResult | null = null;

/** 渲染期快照：与仓库既有 hook 测试同款（不在组件体内直接赋值外层变量）。 */
function snapshot(result: UseFsSearchQueryResult) {
  latest = result;
}

// 用例默认入参：显式列一次，避免在对象字面量里重复书写同名键（TS2783）。
const BASE_OPTIONS: UseFsSearchQueryOptions = { scope: "session:s1", query: "comp", enabled: true };

describe("useFsSearchQuery", () => {
  let container: HTMLDivElement;
  let reactRoot: Root | null = null;
  let options: UseFsSearchQueryOptions;

  function render(next?: Partial<UseFsSearchQueryOptions>) {
    options = { ...BASE_OPTIONS, ...options, ...next };
    act(() => {
      reactRoot?.render(<Harness onSnapshot={snapshot} options={options} />);
    });
  }

  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement("div");
    document.body.appendChild(container);
    reactRoot = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    latest = null;
    options = { ...BASE_OPTIONS };
    fetchFsSearchMock.mockReset();
  });

  afterEach(() => {
    if (reactRoot) {
      act(() => reactRoot?.unmount());
    }
    reactRoot = null;
    container.remove();
    document.body.innerHTML = "";
    vi.useRealTimers();
  });

  it("防抖合并：连续输入只发最后一次请求，旧响应后到不覆盖新结果", async () => {
    const stale = deferred<FsSearchResult>();
    const fresh = deferred<FsSearchResult>();
    fetchFsSearchMock.mockReturnValueOnce(stale.promise).mockReturnValueOnce(fresh.promise);

    render({ query: "comp" });
    await advance();
    expect(fetchFsSearchMock).toHaveBeenCalledTimes(1);

    render({ query: "compmenu" });
    await advance();
    expect(fetchFsSearchMock).toHaveBeenCalledTimes(2);
    expect(fetchFsSearchMock.mock.calls[1][0]).toMatchObject({ query: "compmenu", limit: 20, kinds: "file" });

    fresh.resolve(page({ items: [item("frontend/src/lib/composer-menu.ts")] }));
    await settle();
    expect(latest?.items.map((entry) => entry.path)).toEqual(["frontend/src/lib/composer-menu.ts"]);
    expect(latest?.status).toBe("ready");

    stale.resolve(page({ items: [item("stale.ts")], query: "comp" }));
    await settle();
    expect(latest?.items.map((entry) => entry.path)).toEqual(["frontend/src/lib/composer-menu.ts"]);
  });

  it("输入变化保留旧结果并置 loading（不闪空）", async () => {
    fetchFsSearchMock.mockResolvedValueOnce(page({ items: [item("a.ts")] }));
    render({ query: "a" });
    await advance();
    expect(latest?.items).toHaveLength(1);

    const pending = deferred<FsSearchResult>();
    fetchFsSearchMock.mockReturnValueOnce(pending.promise);
    render({ query: "ab" });
    await flush();
    expect(latest?.status).toBe("loading");
    expect(latest?.items.map((entry) => entry.path)).toEqual(["a.ts"]);

    pending.resolve(page({ items: [item("a.ts"), item("ab.ts")] }));
    await advance();
    expect(latest?.items.map((entry) => entry.path)).toEqual(["a.ts", "ab.ts"]);
  });

  it("query 清空或 enabled=false 时置 idle、清空结果且不发请求", async () => {
    fetchFsSearchMock.mockResolvedValueOnce(page({ items: [item("a.ts")] }));
    render({ query: "a" });
    await advance();
    expect(latest?.items).toHaveLength(1);

    render({ query: "   " });
    await advance();
    expect(latest?.status).toBe("idle");
    expect(latest?.items).toEqual([]);

    render({ query: "a", enabled: false });
    await advance();
    expect(latest?.status).toBe("idle");
    expect(fetchFsSearchMock).toHaveBeenCalledTimes(1);
  });

  it("scope 变化清空结果并作废在途（旧作用域响应不写回）", async () => {
    const stale = deferred<FsSearchResult>();
    fetchFsSearchMock.mockReturnValueOnce(stale.promise);
    render({ scope: "session:s1", query: "a" });
    await advance();
    expect(fetchFsSearchMock).toHaveBeenCalledTimes(1);

    const fresh = deferred<FsSearchResult>();
    fetchFsSearchMock.mockReturnValueOnce(fresh.promise);
    render({ scope: "session:s2", query: "a" });
    await flush();
    expect(latest?.items).toEqual([]);

    stale.resolve(page({ items: [item("wrong.ts")] }));
    await flush();
    expect(latest?.items).toEqual([]);

    fresh.resolve(page({ items: [item("right.ts")] }));
    await advance();
    expect(latest?.items.map((entry) => entry.path)).toEqual(["right.ts"]);
  });

  it("主动取消（AbortError）不落 error 态", async () => {
    fetchFsSearchMock.mockImplementation(
      (_request: unknown, opts: { signal?: AbortSignal }) =>
        new Promise((_resolve, reject) => {
          opts.signal?.addEventListener("abort", () => {
            const error = new Error("aborted");
            error.name = "AbortError";
            reject(error);
          });
        }),
    );

    render({ query: "a" });
    await advance();
    render({ query: "ab" });
    await flush();

    expect(latest?.status).toBe("loading");
    expect(latest?.error).toBeNull();
  });

  it("翻页按 path 去重追加，has_more/truncated/scanned 透传", async () => {
    fetchFsSearchMock.mockResolvedValueOnce(
      page({ items: [item("a.ts")], nextCursor: "c1", hasMore: true, scanned: 10, truncated: true, truncatedReasons: ["budget"] }),
    );
    render({ query: "a" });
    await advance();
    expect(latest?.hasMore).toBe(true);
    expect(latest?.truncated).toBe(true);
    expect(latest?.truncatedReasons).toEqual(["budget"]);

    fetchFsSearchMock.mockResolvedValueOnce(
      page({ items: [item("a.ts"), item("b.ts")], nextCursor: null, hasMore: false, scanned: 7 }),
    );
    act(() => latest?.loadMore());
    await settle();

    expect(fetchFsSearchMock.mock.calls[1][0]).toMatchObject({ cursor: "c1" });
    expect(latest?.items.map((entry) => entry.path)).toEqual(["a.ts", "b.ts"]);
    expect(latest?.hasMore).toBe(false);
    expect(latest?.scanned).toBe(17);
    // 追加页未截断时保留首页的截断标记（联合语义，不因翻页丢失）。
    expect(latest?.truncated).toBe(true);
  });

  it("hasMore=true 但没有游标时不提供加载更多（不臆造下一页）", async () => {
    fetchFsSearchMock.mockResolvedValueOnce(page({ items: [item("a.ts")], hasMore: true, nextCursor: null }));
    render({ query: "a" });
    await advance();

    expect(latest?.hasMore).toBe(false);
    act(() => latest?.loadMore());
    await settle();
    expect(fetchFsSearchMock).toHaveBeenCalledTimes(1);
  });

  it("cursor_invalid：翻页失败重置到第一页，不重试同一游标", async () => {
    fetchFsSearchMock.mockResolvedValueOnce(page({ items: [item("a.ts")], nextCursor: "c1", hasMore: true }));
    render({ query: "a" });
    await advance();

    fetchFsSearchMock.mockRejectedValueOnce(new RuntimeApiError(400, { code: "cursor_invalid" }));
    fetchFsSearchMock.mockResolvedValueOnce(page({ items: [item("b.ts")], nextCursor: null }));
    act(() => latest?.loadMore());
    await settle();

    expect(fetchFsSearchMock).toHaveBeenCalledTimes(3);
    expect(fetchFsSearchMock.mock.calls[2][0]).toMatchObject({ cursor: null });
    expect(latest?.status).toBe("ready");
    expect(latest?.items.map((entry) => entry.path)).toEqual(["b.ts"]);
  });

  it("失败保留上次成功结果并置 error；retry 立即重发（不再防抖）", async () => {
    fetchFsSearchMock.mockResolvedValueOnce(page({ items: [item("a.ts")] }));
    render({ query: "a" });
    await advance();

    fetchFsSearchMock.mockRejectedValueOnce(new Error("network down"));
    render({ query: "ab" });
    await advance();

    expect(latest?.status).toBe("error");
    expect(latest?.items.map((entry) => entry.path)).toEqual(["a.ts"]);
    expect(latest?.unavailable).toBe(false);

    fetchFsSearchMock.mockResolvedValueOnce(page({ items: [item("ab.ts")] }));
    act(() => latest?.retry());
    await settle();
    expect(fetchFsSearchMock).toHaveBeenCalledTimes(3);
    expect(latest?.status).toBe("ready");
    expect(latest?.items.map((entry) => entry.path)).toEqual(["ab.ts"]);
  });

  it("503 标记 unavailable=true（调用方退回本地过滤且不再重试）", async () => {
    fetchFsSearchMock.mockRejectedValueOnce(new RuntimeApiError(503, null));
    render({ query: "a" });
    await advance();

    expect(latest?.status).toBe("error");
    expect(latest?.unavailable).toBe(true);
    await advance();
    expect(fetchFsSearchMock).toHaveBeenCalledTimes(1);
  });

  it("showHidden 变化重新请求并带上新参数", async () => {
    fetchFsSearchMock.mockResolvedValue(page({ items: [] }));
    render({ query: "a", showHidden: false });
    await advance();
    expect(fetchFsSearchMock.mock.calls[0][0]).toMatchObject({ showHidden: false });

    render({ showHidden: true });
    await advance();
    expect(fetchFsSearchMock).toHaveBeenCalledTimes(2);
    expect(fetchFsSearchMock.mock.calls[1][0]).toMatchObject({ showHidden: true });
  });
});
