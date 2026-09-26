// S5：运行时附件上传客户端单测（FormData 载荷 / 归一化 / 三段错误分类）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import {
  describeRuntimeUploadFailure,
  isRuntimeUploadValidationError,
  isRuntimeUploadsUnavailable,
  normalizeRuntimeUploadPayload,
  RUNTIME_UPLOAD_PATH,
  uploadRuntimeAttachment,
} from "@/api/runtime/uploads";

type FetchCall = { url: string; init?: RequestInit };

let calls: FetchCall[];
const originalFetch = globalThis.fetch;

function installFetch(response: Response | (() => Promise<Response>)) {
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init });
    return typeof response === "function" ? response() : response;
  }) as unknown as typeof fetch;
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function makeFile(name = "shot.png", type = "image/png") {
  return new File([new Uint8Array([137, 80, 78, 71])], name, { type });
}

describe("normalizeRuntimeUploadPayload", () => {
  it("逐条归一化附件并保留后端上报的 accepted", () => {
    const result = normalizeRuntimeUploadPayload({
      ok: true,
      accepted: 2,
      attachments: [
        {
          name: "a.png",
          path: "/tmp/uploads/a.png",
          bytes: 12,
          width: 4,
          height: 4,
          note: "已压缩",
          skipped: false,
        },
        { name: "b.png", skipped: true, note: "不是可识别的图片" },
      ],
    });

    expect(result.ok).toBe(true);
    expect(result.accepted).toBe(2);
    expect(result.attachments).toEqual([
      {
        name: "a.png",
        path: "/tmp/uploads/a.png",
        bytes: 12,
        width: 4,
        height: 4,
        note: "已压缩",
        skipped: false,
      },
      {
        name: "b.png",
        path: "",
        bytes: 0,
        width: 0,
        height: 0,
        note: "不是可识别的图片",
        skipped: true,
      },
    ]);
  });

  it("attachments 非数组即抛错，不伪造空态", () => {
    expect(() => normalizeRuntimeUploadPayload({ ok: true, accepted: 0 })).toThrow(
      /missing `attachments` array/,
    );
    expect(() => normalizeRuntimeUploadPayload({ attachments: "nope" })).toThrow(
      /missing `attachments` array/,
    );
    expect(() => normalizeRuntimeUploadPayload(null)).toThrow(/not an object/);
  });

  it("缺 name 的坏条目只丢该条，不影响其余条目", () => {
    const result = normalizeRuntimeUploadPayload({
      ok: true,
      attachments: [{ path: "/tmp/uploads/anon.png" }, { name: "ok.png", path: "/p" }],
    });
    expect(result.attachments.map((item) => item.name)).toEqual(["ok.png"]);
  });

  it("既无 path 又未标记跳过的条目按坏载荷丢弃（不伪造已上传）", () => {
    const result = normalizeRuntimeUploadPayload({
      ok: true,
      attachments: [{ name: "ghost.png" }],
    });
    expect(result.attachments).toEqual([]);
    expect(result.accepted).toBe(0);
  });
});

describe("uploadRuntimeAttachment", () => {
  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("以 multipart 字段 file 上传，且不手写 Content-Type（浏览器补 boundary）", async () => {
    installFetch(() =>
      Promise.resolve(
        jsonResponse({
          ok: true,
          accepted: 1,
          attachments: [{ name: "shot.png", path: "/tmp/uploads/shot.png", bytes: 4 }],
        }),
      ),
    );

    const result = await uploadRuntimeAttachment(makeFile());

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(RUNTIME_UPLOAD_PATH);
    expect(calls[0].init?.method).toBe("POST");
    const form = calls[0].init?.body as FormData;
    expect(form).toBeInstanceOf(FormData);
    expect(form.get("file")).toBeInstanceOf(File);
    const headers = calls[0].init?.headers as Record<string, string>;
    expect(headers["Content-Type"]).toBeUndefined();
    expect(result.attachments[0].path).toBe("/tmp/uploads/shot.png");
  });

  it("无名文件直接抛错，不发无效请求", async () => {
    installFetch(() => Promise.resolve(jsonResponse({ ok: true, attachments: [] })));
    await expect(uploadRuntimeAttachment(makeFile(""))).rejects.toThrow(
      /requires a named file/,
    );
    expect(calls).toHaveLength(0);
  });

  it("失败时抛 RuntimeApiError，并保留后端 reason", async () => {
    installFetch(() =>
      Promise.resolve(
        jsonResponse(
          { ok: false, error: "uploads_unavailable", reason: "本服务未配置附件上传目录" },
          503,
        ),
      ),
    );

    const error = await uploadRuntimeAttachment(makeFile()).catch((err) => err);
    expect(error).toBeInstanceOf(RuntimeApiError);
    expect(isRuntimeUploadsUnavailable(error)).toBe(true);
    expect(isRuntimeUploadValidationError(error)).toBe(false);
    expect(describeRuntimeUploadFailure(error)).toEqual({
      code: "uploads_unavailable",
      reason: "本服务未配置附件上传目录",
      status: 503,
    });
  });
});

describe("上传失败分类", () => {
  it("三段互不混用：不可用 / 校验失败 / 真实失败", () => {
    const unavailable = new RuntimeApiError(503, { error: "uploads_unavailable" });
    const missing = new RuntimeApiError(400, { error: "missing_file" });
    const tooMany = new RuntimeApiError(400, { error: "too_many_files" });
    const boom = new RuntimeApiError(500, { error: "disk exploded" });

    expect(isRuntimeUploadsUnavailable(unavailable)).toBe(true);
    expect(isRuntimeUploadsUnavailable(missing)).toBe(false);
    expect(isRuntimeUploadValidationError(missing)).toBe(true);
    expect(isRuntimeUploadValidationError(tooMany)).toBe(true);
    expect(isRuntimeUploadValidationError(boom)).toBe(false);
    expect(isRuntimeUploadsUnavailable(boom)).toBe(false);
    expect(isRuntimeUploadsUnavailable(new Error("network"))).toBe(false);
  });

  it("非 RuntimeApiError 时回落到 Error.message，不当成端点不可用", () => {
    expect(describeRuntimeUploadFailure(new Error("failed to fetch"))).toEqual({
      code: "",
      reason: "failed to fetch",
      status: null,
    });
  });
});
