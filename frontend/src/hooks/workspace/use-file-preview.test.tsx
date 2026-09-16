// @vitest-environment jsdom

// P2-1A 修正：消息列表预览的「工具行相对路径 → 会话根绝对路径」解析单测。
//
// 背景（本机实测）：`POST /fs/read-file` 的既有契约是「相对路径按运行时进程工作目录解析」，
// 而运行时进程 cwd 落在 `<repo>/backend`，agent 工具行给出的却是相对**会话工作目录**的写法，
// 直接透传必然解析成 `<repo>/backend/frontend/...` 并报 path not found。
//
// 断言原则：只看 hook 暴露的可见状态与实际发出的读取请求路径；不假设请求次数之外的实现细节。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { FsRoot } from "@/types/runtime/fs-browser";
import type { RuntimeFileReadResult } from "@/types/runtime";

const { fetchFsRootsMock, readRuntimeFileMock } = vi.hoisted(() => ({
  fetchFsRootsMock: vi.fn(),
  readRuntimeFileMock: vi.fn(),
}));

// 只替换「发请求」的两个函数，归一化/降级判据仍走真实实现（与生产同口径）。
vi.mock("@/api/runtime/fs-roots", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/runtime/fs-roots")>()),
  fetchFsRoots: fetchFsRootsMock,
}));

vi.mock("@/api/runtime/files", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/runtime/files")>()),
  readRuntimeFile: readRuntimeFileMock,
}));

import { useFilePreview, type UseFilePreviewResult } from "./use-file-preview";

type ReactActEnvironmentGlobal = typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };

const SESSION_ID = "session_20260916072500_iF200Qzh";
const SESSION_ROOT = "E:\\projects\\ai\\ai-agent-runtime";
const CWD_ROOT = "E:\\projects\\ai\\ai-agent-runtime\\backend";
const RELATIVE_PATH = "frontend/src/components/workspace/file-browser/scope-header.tsx";
const RESOLVED_PATH = `${SESSION_ROOT}\\frontend\\src\\components\\workspace\\file-browser\\scope-header.tsx`;

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

function fsRoot(kind: FsRoot["kind"], scope: string, path: string): FsRoot {
  return { scope, kind, name: scope, path, exists: true, isGitRepo: false };
}

function rootsResult(roots: FsRoot[]) {
  return { roots, skipped: 0 };
}

function readResult(path: string, content = "first line\nsecond line\n"): RuntimeFileReadResult {
  return {
    path,
    dataBase64: Buffer.from(content, "utf8").toString("base64"),
    byteCount: Buffer.byteLength(content, "utf8"),
  };
}

function Harness({
  sessionId,
  onSnapshot,
}: {
  sessionId?: string | null;
  onSnapshot: (result: UseFilePreviewResult) => void;
}) {
  onSnapshot(useFilePreview({ sessionId }));
  return null;
}

describe("useFilePreview 路径解析", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  let latest: UseFilePreviewResult | null = null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    latest = null;
    fetchFsRootsMock.mockReset();
    readRuntimeFileMock.mockReset();
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

  function renderHook(sessionId: string | null = SESSION_ID) {
    act(() => {
      root?.render(
        <Harness
          onSnapshot={(result) => {
            latest = result;
          }}
          sessionId={sessionId}
        />,
      );
    });
  }

  async function open(path: string) {
    await act(async () => {
      latest?.open(path);
      await flush();
    });
  }

  it("相对路径先按会话根解析成绝对路径再读；展示仍保留工具行原始路径", async () => {
    fetchFsRootsMock.mockResolvedValue(
      rootsResult([
        fsRoot("workspace", "workspace:935daf325fe6", "E:\\projects\\ai\\ai-agent-runtime"),
        fsRoot("session", `session:${SESSION_ID}`, SESSION_ROOT),
        fsRoot("cwd", "cwd", CWD_ROOT),
      ]),
    );
    readRuntimeFileMock.mockResolvedValue(readResult(RESOLVED_PATH));

    renderHook();
    await open(RELATIVE_PATH);

    expect(fetchFsRootsMock.mock.calls[0][0]).toMatchObject({ sessionId: SESSION_ID });
    expect(readRuntimeFileMock).toHaveBeenCalledTimes(1);
    expect(readRuntimeFileMock.mock.calls[0][0]).toBe(RESOLVED_PATH);
    expect(latest?.requestedPath).toBe(RELATIVE_PATH);
    expect(latest?.status).toBe("ready");
  });

  it("绝对路径原样透传，且不为此去问作用域根", async () => {
    readRuntimeFileMock.mockResolvedValue(readResult(RESOLVED_PATH));

    renderHook();
    await open(RESOLVED_PATH);

    expect(readRuntimeFileMock.mock.calls[0][0]).toBe(RESOLVED_PATH);
    expect(fetchFsRootsMock).not.toHaveBeenCalled();
    expect(latest?.status).toBe("ready");
  });

  it("基准根一次会话只取一次：连续打开不同相对路径复用缓存", async () => {
    fetchFsRootsMock.mockResolvedValue(
      rootsResult([fsRoot("session", `session:${SESSION_ID}`, SESSION_ROOT)]),
    );
    readRuntimeFileMock.mockResolvedValue(readResult(SESSION_ROOT + "\\frontend\\src\\app.tsx"));

    renderHook();
    await open("frontend/src/app.tsx");
    await open("frontend/src/main.tsx");

    expect(fetchFsRootsMock).toHaveBeenCalledTimes(1);
    expect(readRuntimeFileMock.mock.calls.map((call) => call[0])).toEqual([
      `${SESSION_ROOT}\\frontend\\src\\app.tsx`,
      `${SESSION_ROOT}\\frontend\\src\\main.tsx`,
    ]);
  });

  it("作用域根不可用时不猜测：退回原样路径，读取失败如实落错误态", async () => {
    fetchFsRootsMock.mockRejectedValue(new Error("service unavailable"));
    readRuntimeFileMock.mockRejectedValue(new Error("open E:\\projects\\ai\\ai-agent-runtime\\backend\\frontend\\src\\app.tsx: The system cannot find the path specified."));

    renderHook();
    await open("frontend/src/app.tsx");

    expect(readRuntimeFileMock.mock.calls[0][0]).toBe("frontend/src/app.tsx");
    expect(latest?.status).toBe("error");
    expect((latest?.error as Error).message).toContain("cannot find the path");
  });

  it("解析根期间关闭弹层：不落错误态、也不发读取请求", async () => {
    const pending = deferred<unknown>();
    fetchFsRootsMock.mockReturnValue(pending.promise);
    renderHook();
    await open(RELATIVE_PATH);
    expect(latest?.status).toBe("loading");

    await act(async () => {
      latest?.close();
      await flush();
    });
    pending.reject(Object.assign(new Error("aborted"), { name: "AbortError" }));
    await act(async () => {
      await flush();
    });

    expect(latest?.status).toBe("closed");
    expect(readRuntimeFileMock).not.toHaveBeenCalled();
  });
});
