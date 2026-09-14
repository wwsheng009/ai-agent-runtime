// @vitest-environment jsdom

// P2-1A：文件预览弹层单测（文本 / 空文件 / 二进制 / 超限 / 降级错误 / 关闭路径）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import type { UseFilePreviewResult } from "@/hooks/workspace/use-file-preview";

import { FilePreviewDialog } from "./file-preview-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function toBase64Text(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary);
}

function makePreview(overrides: Partial<UseFilePreviewResult> = {}): UseFilePreviewResult {
  return {
    status: "ready",
    requestedPath: "frontend/package.json",
    result: {
      path: "/repo/frontend/package.json",
      dataBase64: toBase64Text("{\n}\n"),
      byteCount: 4,
    },
    body: { kind: "text", text: "{\n}\n", lineCount: 2 },
    error: null,
    unavailable: false,
    tooLarge: false,
    open: vi.fn(),
    close: vi.fn(),
    retry: vi.fn(),
    ...overrides,
  };
}

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

describe("FilePreviewDialog", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function renderDialog(preview: UseFilePreviewResult) {
    await act(async () => {
      root?.render(<FilePreviewDialog preview={preview} />);
    });
    await act(flush);
  }

  it("closed 状态不渲染弹层", async () => {
    await renderDialog(makePreview({ status: "closed", requestedPath: "" }));
    expect(document.querySelector('[data-testid="file-preview-dialog"]')).toBeNull();
  });

  it("文本文件呈现内容、解析路径与字节/行数", async () => {
    await renderDialog(makePreview());

    const text = document.querySelector('[data-testid="file-preview-text"]');
    expect(text?.textContent).toBe("{\n}\n");
    expect(
      document.querySelector('[data-testid="file-preview-resolved-path"]')?.textContent,
    ).toContain("/repo/frontend/package.json");
    expect(document.querySelector('[data-testid="file-preview-bytes"]')?.textContent).toContain(
      "4 字节",
    );
    expect(document.querySelector('[data-testid="file-preview-lines"]')?.textContent).toContain(
      "2 行",
    );
  });

  it("loading 状态给出读取中提示，不渲染内容", async () => {
    await renderDialog(
      makePreview({ status: "loading", result: null, body: null }),
    );

    expect(document.querySelector('[data-testid="file-preview-loading"]')).not.toBeNull();
    expect(document.querySelector('[data-testid="file-preview-text"]')).toBeNull();
  });

  it("空文件如实提示 0 字节，不渲染空文本块", async () => {
    await renderDialog(
      makePreview({
        result: { path: "/repo/empty.txt", dataBase64: "", byteCount: 0 },
        body: { kind: "empty" },
      }),
    );

    expect(document.querySelector('[data-testid="file-preview-empty"]')).not.toBeNull();
    expect(document.querySelector('[data-testid="file-preview-bytes"]')?.textContent).toContain(
      "0 字节",
    );
    expect(document.querySelector('[data-testid="file-preview-text"]')).toBeNull();
  });

  it("二进制内容只呈现判定原因，不渲染文本", async () => {
    await renderDialog(
      makePreview({
        result: { path: "/repo/blob.bin", dataBase64: "AA==", byteCount: 1 },
        body: { kind: "binary", reason: "nul-byte" },
      }),
    );

    expect(document.querySelector('[data-testid="file-preview-binary"]')?.textContent).toContain(
      "含 NUL 字节",
    );
    expect(document.querySelector('[data-testid="file-preview-text"]')).toBeNull();
  });

  it("超过预览上限时不渲染内容，只提示真实大小", async () => {
    await renderDialog(
      makePreview({
        result: { path: "/repo/big.log", dataBase64: "", byteCount: 4_000_000 },
        body: null,
        tooLarge: true,
      }),
    );

    const notice = document.querySelector('[data-testid="file-preview-too-large"]');
    expect(notice?.textContent).toContain("3.8 MB");
    expect(notice?.textContent).toContain("977 KB");
    expect(document.querySelector('[data-testid="file-preview-text"]')).toBeNull();
  });

  it("端点不可用时如实提示 HTTP 状态并可重试", async () => {
    const retry = vi.fn();
    await renderDialog(
      makePreview({
        status: "error",
        result: null,
        body: null,
        error: new RuntimeApiError(503, { error: "file transfer service not configured" }),
        unavailable: true,
        retry,
      }),
    );

    const alert = document.querySelector('[data-testid="file-preview-error"]');
    expect(alert?.textContent).toContain("HTTP 503");
    expect(alert?.textContent).toContain("不做本地替代");

    const retryButton = Array.from(alert?.querySelectorAll("button") ?? []).find((button) =>
      button.textContent?.includes("重试"),
    );
    await act(async () => {
      retryButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(retry).toHaveBeenCalledTimes(1);
  });

  it("真实读取失败照常给出错误消息（不降级为不可用）", async () => {
    await renderDialog(
      makePreview({
        status: "error",
        result: null,
        body: null,
        error: new Error("读取文件失败: open /repo/missing.ts: no such file or directory"),
      }),
    );

    const alert = document.querySelector('[data-testid="file-preview-error"]');
    expect(alert?.textContent).toContain("no such file or directory");
    expect(alert?.textContent).not.toContain("HTTP 500");
  });

  it("关闭按钮与 Esc 都触发 close", async () => {
    const close = vi.fn();
    await renderDialog(makePreview({ close }));

    const closeButton = document.querySelector('button[aria-label="关闭文件预览"]');
    await act(async () => {
      closeButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(close).toHaveBeenCalledTimes(1);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(close).toHaveBeenCalledTimes(2);
  });
});
