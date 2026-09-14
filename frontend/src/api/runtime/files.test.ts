// P2-1A：运行时文件读取客户端单测（请求体 / 载荷归一化 / 降级判据）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  FILE_READ_PATH,
  isFileReadUnavailable,
  normalizeFileReadPayload,
  readRuntimeFile,
} from "@/api/runtime/files";
import { RuntimeApiError } from "@/api/runtime/shared";

function toBase64(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary);
}

describe("normalizeFileReadPayload", () => {
  it("读取 file.path / data_base64 / byte_count", () => {
    const result = normalizeFileReadPayload({
      file: {
        path: "/repo/frontend/package.json",
        data_base64: toBase64("hello"),
        byte_count: 5,
      },
    });

    expect(result).toEqual({
      path: "/repo/frontend/package.json",
      dataBase64: toBase64("hello"),
      byteCount: 5,
    });
  });

  it("结构缺失或坏载荷直接抛错，不伪装成空文件", () => {
    expect(() => normalizeFileReadPayload({})).toThrow(/missing `file`/);
    expect(() => normalizeFileReadPayload(null)).toThrow(/missing `file`/);
    expect(() =>
      normalizeFileReadPayload({ file: { data_base64: "", byte_count: 0 } }),
    ).toThrow(/missing `file\.path`/);
    expect(() =>
      normalizeFileReadPayload({ file: { path: "/a.txt", data_base64: 42 } }),
    ).toThrow(/missing `file\.data_base64`/);
    expect(() =>
      normalizeFileReadPayload({ file: { path: "/a.txt", data_base64: "???" } }),
    ).toThrow(/not valid base64/);
  });

  it("byte_count 缺失或非法时按 base64 实际字节数收口", () => {
    const dataBase64 = toBase64("hello");
    expect(
      normalizeFileReadPayload({ file: { path: "/a.txt", data_base64: dataBase64 } }).byteCount,
    ).toBe(5);
    expect(
      normalizeFileReadPayload({
        file: { path: "/a.txt", data_base64: dataBase64, byte_count: -1 },
      }).byteCount,
    ).toBe(5);
  });

  it("空文件（data_base64 为空串）是合法载荷", () => {
    expect(
      normalizeFileReadPayload({ file: { path: "/empty.txt", data_base64: "", byte_count: 0 } }),
    ).toEqual({ path: "/empty.txt", dataBase64: "", byteCount: 0 });
  });
});

describe("readRuntimeFile", () => {
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

  it("POST /api/runtime/fs/read-file，请求体为 {path}", async () => {
    respondWith({
      file: { path: "/repo/frontend/package.json", data_base64: toBase64("{}"), byte_count: 2 },
    });

    const result = await readRuntimeFile("  frontend/package.json  ");

    expect(calls).toHaveLength(1);
    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.pathname).toBe(FILE_READ_PATH);
    expect(calls[0].init?.method).toBe("POST");
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      path: "frontend/package.json",
    });
    expect(result.path).toBe("/repo/frontend/package.json");
    expect(result.byteCount).toBe(2);
  });

  it("空路径不发请求，直接抛错", async () => {
    respondWith({ file: { path: "/a.txt", data_base64: "", byte_count: 0 } });

    await expect(readRuntimeFile("   ")).rejects.toThrow(/file path is required/);
    expect(calls).toHaveLength(0);
  });

  it("503 未注入 file transfer 服务时抛出 RuntimeApiError 并标记不可用", async () => {
    respondWith({ error: "file transfer service not configured" }, 503);

    const error = await readRuntimeFile("frontend/package.json").catch(
      (caught: unknown) => caught,
    );

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect((error as RuntimeApiError).status).toBe(503);
    expect(isFileReadUnavailable(error)).toBe(true);
  });
});

describe("isFileReadUnavailable", () => {
  it("404/405/501/503 视为端点不可用", () => {
    for (const status of [404, 405, 501, 503]) {
      expect(isFileReadUnavailable(new RuntimeApiError(status, null))).toBe(true);
    }
  });

  it("读盘失败（500）与未知错误按真实失败呈现", () => {
    expect(isFileReadUnavailable(new RuntimeApiError(500, null))).toBe(false);
    expect(isFileReadUnavailable(new RuntimeApiError(400, null))).toBe(false);
    expect(isFileReadUnavailable(new Error("boom"))).toBe(false);
    expect(isFileReadUnavailable(undefined)).toBe(false);
  });
});
