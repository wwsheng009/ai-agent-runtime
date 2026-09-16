// P1：截断式预览客户端单测（kind 白名单 / 必需字段 / 降级判据）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  FS_PREVIEW_PATH,
  fetchFsPreview,
  isFsPreviewUnavailable,
  normalizeFsPreviewPayload,
} from "@/api/runtime/fs-preview";
import { RuntimeApiError } from "@/api/runtime/shared";

describe("normalizeFsPreviewPayload", () => {
  it("text 预览带出截断信息与上限", () => {
    const result = normalizeFsPreviewPayload({
      kind: "text",
      path: "logs/app.log",
      abs_path: "E:/repo/logs/app.log",
      size: 5_000_000,
      mtime: 1720000000,
      mime: "text/plain",
      text: "line1\nline2",
      truncated: true,
      limit_bytes: 1_000_000,
    });

    expect(result).toEqual({
      kind: "text",
      path: "logs/app.log",
      absPath: "E:/repo/logs/app.log",
      size: 5_000_000,
      mtime: 1720000000,
      mime: "text/plain",
      text: "line1\nline2",
      truncated: true,
      limitBytes: 1_000_000,
    });
  });

  it("image 预览带出 base64；binary/too_large 带出 reason", () => {
    const image = normalizeFsPreviewPayload({
      kind: "image",
      path: "shot.png",
      size: 2048,
      data_base64: "aGVsbG8=",
      truncated: false,
    });
    expect(image.dataBase64).toBe("aGVsbG8=");
    expect(image.text).toBeUndefined();

    const binary = normalizeFsPreviewPayload({
      kind: "binary",
      path: "app.exe",
      size: 1024,
      reason: "nul-byte",
      truncated: false,
    });
    expect(binary).toMatchObject({ kind: "binary", reason: "nul-byte", size: 1024 });

    const tooLarge = normalizeFsPreviewPayload({
      kind: "too_large",
      path: "huge.bin",
      size: 2_000_000_000,
      limit_bytes: 1_000_000,
      truncated: false,
    });
    expect(tooLarge).toMatchObject({ kind: "too_large", limitBytes: 1_000_000 });
  });

  it("kind=text 缺 text、kind=image 缺 data_base64 直接抛错", () => {
    expect(() => normalizeFsPreviewPayload({ kind: "text", path: "a.txt" })).toThrow(
      /requires `text`/,
    );
    expect(() => normalizeFsPreviewPayload({ kind: "image", path: "a.png" })).toThrow(
      /requires `data_base64`/,
    );
  });

  it("未知/缺失 kind 直接抛错，不猜渲染方式", () => {
    expect(() => normalizeFsPreviewPayload({ path: "a.txt" })).toThrow(/unsupported fs preview kind/);
    expect(() => normalizeFsPreviewPayload({ kind: "hologram" })).toThrow(
      /unsupported fs preview kind/,
    );
    expect(() => normalizeFsPreviewPayload("nope")).toThrow(/not an object/);
  });

  it("size 非法收口 -1，truncated 缺省 false", () => {
    const result = normalizeFsPreviewPayload({
      kind: "text",
      path: "a.txt",
      size: "12",
      text: "",
    });

    expect(result.size).toBe(-1);
    expect(result.truncated).toBe(false);
  });
});

describe("fetchFsPreview", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("GET /fs/preview 并下发 scope/path/max_bytes", async () => {
    respondWith({ kind: "text", path: "README.md", text: "# hi", truncated: false });

    const result = await fetchFsPreview({
      scope: "workspace:wd-1",
      path: "README.md",
      maxBytes: 262_144,
    });

    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.pathname).toBe(FS_PREVIEW_PATH);
    expect(url.searchParams.get("scope")).toBe("workspace:wd-1");
    expect(url.searchParams.get("path")).toBe("README.md");
    expect(url.searchParams.get("max_bytes")).toBe("262144");
    expect(calls[0].init?.method).toBe("GET");
    expect(result.text).toBe("# hi");
  });

  it("缺 scope 或 path 不发请求", async () => {
    respondWith({ kind: "text", path: "a.txt", text: "", truncated: false });

    await expect(fetchFsPreview({ scope: "cwd", path: "  " })).rejects.toThrow(
      /requires scope and path/,
    );
    await expect(fetchFsPreview({ scope: " ", path: "a.txt" })).rejects.toThrow(
      /requires scope and path/,
    );
    expect(calls).toHaveLength(0);
  });

  it("503 未注入服务时判为不可用（UI 退化为仅下载）", async () => {
    respondWith({ error: "file browse service not configured" }, 503);

    const error = await fetchFsPreview({ scope: "cwd", path: "a.txt" }).catch(
      (caught: unknown) => caught,
    );

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect(isFsPreviewUnavailable(error)).toBe(true);
    expect(isFsPreviewUnavailable(new Error("boom"))).toBe(false);
  });
});
