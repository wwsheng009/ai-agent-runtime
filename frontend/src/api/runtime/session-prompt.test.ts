// S5：带图提交 prompt 客户端单测（请求体口径 / 响应归一化 / 错误分类）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  isSessionPromptUnavailable,
  isSessionPromptValidationError,
  normalizeRuntimePromptImages,
  normalizeRuntimeSessionPromptPayload,
  sessionRuntimeCommandsPath,
  submitRuntimeSessionPrompt,
} from "@/api/runtime/session-prompt";
import { RuntimeApiError } from "@/api/runtime/shared";

type FetchCall = { url: string; init?: RequestInit };

let calls: FetchCall[];
const originalFetch = globalThis.fetch;

function installFetch(response: Response) {
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init });
    return response;
  }) as unknown as typeof fetch;
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("submitRuntimeSessionPrompt", () => {
  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("带 images 时投递 submit_prompt 并归一化 attached_images / image_notes", async () => {
    installFetch(
      jsonResponse({
        result: { output: "done" },
        attached_images: 2,
        image_notes: ["a.png 已压缩", "b.png 不在附件目录内"],
      }),
    );

    const result = await submitRuntimeSessionPrompt("session/1", "look", {
      images: ["/uploads/a.png", "/uploads/a.png", "/uploads/b.png"],
    });

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toContain(
      "/api/runtime/sessions/session%2F1/runtime/commands",
    );
    expect(calls[0].init?.method).toBe("POST");
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "submit_prompt",
      prompt: "look",
      // 去重后按首次出现顺序投递。
      images: ["/uploads/a.png", "/uploads/b.png"],
    });
    expect(result).toEqual({
      pending: false,
      attachedImages: 2,
      imageNotes: ["a.png 已压缩", "b.png 不在附件目录内"],
      result: { output: "done" },
    });
  });

  it("无图时不发送 images 字段（保持旧响应契约）", async () => {
    installFetch(jsonResponse({ result: "plain" }));

    const result = await submitRuntimeSessionPrompt("s-1", "hi");

    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "submit_prompt",
      prompt: "hi",
    });
    expect(result.attachedImages).toBeNull();
    expect(result.imageNotes).toEqual([]);
    expect(result.pending).toBe(false);
  });

  it("202 pending 如实回传（会话忙，本轮未执行）", async () => {
    installFetch(jsonResponse({ ok: true, pending: true, state: { status: "running" } }, 202));

    const result = await submitRuntimeSessionPrompt("s-1", "hi", { images: ["/a.png"] });

    expect(result.pending).toBe(true);
    expect(result.attachedImages).toBeNull();
  });

  it("会话 id / prompt 为空即抛错，不发无效请求", async () => {
    installFetch(jsonResponse({ result: "x" }));

    await expect(submitRuntimeSessionPrompt("  ", "hi")).rejects.toThrow(
      /session id is required/,
    );
    await expect(submitRuntimeSessionPrompt("s-1", "   ")).rejects.toThrow(
      /prompt is required/,
    );
    expect(calls).toHaveLength(0);
  });

  it("image_notes 非数组即抛错，不静默吞掉后端说明", () => {
    expect(() =>
      normalizeRuntimeSessionPromptPayload({ result: "x", image_notes: "oops" }),
    ).toThrow(/non-array `image_notes`/);
    expect(() => normalizeRuntimeSessionPromptPayload(null)).toThrow(/not an object/);
  });

  it("空串与空白 path 在归一化时被剔除", () => {
    expect(normalizeRuntimePromptImages(["  ", "/a.png", " /a.png "])).toEqual([
      "/a.png",
    ]);
    expect(normalizeRuntimePromptImages([])).toEqual([]);
  });
});

describe("错误分类与路径", () => {
  it("不可用 / 校验失败互不混用", () => {
    expect(isSessionPromptUnavailable(new RuntimeApiError(503, { error: "x" }))).toBe(
      true,
    );
    expect(isSessionPromptValidationError(new RuntimeApiError(400, { error: "x" }))).toBe(
      true,
    );
    expect(isSessionPromptValidationError(new RuntimeApiError(503, { error: "x" }))).toBe(
      false,
    );
    expect(isSessionPromptUnavailable(new RuntimeApiError(400, { error: "x" }))).toBe(
      false,
    );
  });

  it("命令端点路径对 session id 做编码", () => {
    expect(sessionRuntimeCommandsPath("a/b c")).toBe(
      "/api/runtime/sessions/a%2Fb%20c/runtime/commands",
    );
  });
});
