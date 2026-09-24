// Profiles 传输客户端单测（Batch 13 slice 8）：导出（POST + zip 二进制）与
// 导入（zip 请求体 + 查询参数）的请求形状、响应头/报告归一化、错误分类。
//
// 覆盖纪律：
//   * 导出响应头缺失（ref / 文件数 / 文件名）时给确定的兜底，不猜成功；
//   * 导出失败体仍是 JSON 错误信封 → RuntimeApiError（保留后端 code）；
//   * 导入的 name/layer/dry_run 一律在查询串，请求体是 zip 本身；
//   * 400 + 报告体（valid/paths/issues）是**领域结果**：返回报告而不是抛错；
//     409 同名冲突等其余状态码必须继续抛错（不能被报告分支吞掉）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { exportRuntimeProfile, importRuntimeProfile } from "@/api/runtime/profiles";
import { RuntimeApiError, isRuntimeApiErrorCode } from "@/api/runtime/shared";

const REF = "user:reviewer plan";

type RecordedCall = { url: string; method: string; body: unknown; contentType: string };

let calls: RecordedCall[] = [];

function mockFetch(handler: (input: RequestInfo | URL, init?: RequestInit) => Response) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const headers = (init?.headers ?? {}) as Record<string, string>;
    calls.push({
      url: String(input),
      method: (init?.method ?? "GET").toUpperCase(),
      body: init?.body ?? null,
      contentType: headers["Content-Type"] ?? "",
    });
    return handler(input, init);
  });
  globalThis.fetch = fetchMock as unknown as typeof fetch;
  return fetchMock;
}

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("runtime profiles 传输客户端", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    globalThis.fetch = originalFetch;
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it("导出：POST 到整段编码的 /export，并按响应头归一化文件名与文件数", async () => {
    mockFetch(
      () =>
        new Response(new Uint8Array([80, 75, 3, 4]), {
          status: 200,
          headers: {
            "Content-Type": "application/zip",
            "Content-Disposition": 'attachment; filename="user-reviewer.zip"',
            "X-Aicli-Profile-Ref": REF,
            "X-Aicli-Profile-File-Count": "3",
          },
        }),
    );

    const bundle = await exportRuntimeProfile(REF);

    expect(calls).toHaveLength(1);
    expect(calls[0].method).toBe("POST");
    // ref 含 `:` 与空格：必须整段 encodeURIComponent（与其它单资源端点同纪律）。
    expect(calls[0].url).toBe("/api/runtime/profiles/user%3Areviewer%20plan/export");
    expect(bundle.ref).toBe(REF);
    expect(bundle.filename).toBe("user-reviewer.zip");
    expect(bundle.fileCount).toBe(3);
    // 二进制原样透传：jsdom 的 Blob 没有 arrayBuffer()，这里用 size/type 断言
    // （4 字节 PK 头 + 响应 Content-Type 变成 blob 的 type）。
    expect(bundle.blob.size).toBe(4);
    expect(bundle.blob.type).toBe("application/zip");
  });

  it("导出：响应头缺失时回填请求 ref 与 `<ref>.zip` 兜底名，计数为 0", async () => {
    mockFetch(() => new Response(new Uint8Array([1]), { status: 200 }));

    const bundle = await exportRuntimeProfile("project/foo");

    expect(bundle.ref).toBe("project/foo");
    expect(bundle.filename).toBe("project-foo.zip");
    expect(bundle.fileCount).toBe(0);
  });

  it("导出：非 2xx 抛 RuntimeApiError 并保留后端 code（不吞原因）", async () => {
    mockFetch(() => jsonResponse({ error: "forbidden", code: "FORBIDDEN" }, 403));

    const error = await exportRuntimeProfile(REF).catch((caught: unknown) => caught);

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect(isRuntimeApiErrorCode(error, "FORBIDDEN")).toBe(true);
  });

  it("导入：请求体是 zip 本身，name/layer/dry_run 走查询参数", async () => {
    mockFetch(() =>
      jsonResponse({ ok: true, imported: true, activated: false, valid: true, dry_run: false }, 201),
    );
    const bundle = new Blob([new Uint8Array([80, 75])], { type: "application/zip" });

    const report = await importRuntimeProfile(bundle, { name: "reviewer", layer: "project" });

    expect(calls).toHaveLength(1);
    expect(calls[0].method).toBe("POST");
    expect(calls[0].url).toBe("/api/runtime/profiles/import?name=reviewer&layer=project");
    expect(calls[0].contentType).toBe("application/zip");
    expect(calls[0].body).toBe(bundle);
    expect(report.imported).toBe(true);
    expect(report.activated).toBe(false);
  });

  it("导入：dryRun=true 只带 dry_run 查询参数（name 留空即不出现）", async () => {
    mockFetch(() =>
      jsonResponse({
        ok: true,
        valid: true,
        dry_run: true,
        imported: false,
        activated: false,
        layer: "user",
        name: "reviewer",
        paths: ["profile.yaml"],
        file_count: 1,
      }),
    );

    const report = await importRuntimeProfile(new Blob([new Uint8Array([80])]), {
      layer: "user",
      dryRun: true,
    });

    expect(calls[0].url).toBe("/api/runtime/profiles/import?layer=user&dry_run=true");
    expect(report.dryRun).toBe(true);
    expect(report.paths).toEqual(["profile.yaml"]);
  });

  it("导入：400 + 报告体是领域结果（返回报告而不是抛错），issues/paths 原样交给 UI", async () => {
    mockFetch(() =>
      jsonResponse(
        {
          ok: false,
          imported: false,
          activated: false,
          dry_run: true,
          layer: "user",
          valid: false,
          error: "导入包未通过 validate：修正后再导入（D28）",
          error_count: 1,
          warning_count: 0,
          issues: [{ path: "profile.yaml", message: "unknown field", severity: "error" }],
          paths: ["profile.yaml"],
          file_count: 1,
        },
        400,
      ),
    );

    const report = await importRuntimeProfile(new Blob([new Uint8Array([80])]), {
      layer: "user",
      dryRun: true,
    });

    expect(report.valid).toBe(false);
    expect(report.imported).toBe(false);
    expect(report.errorCount).toBe(1);
    expect(report.issues).toEqual([
      { path: "profile.yaml", message: "unknown field", severity: "error" },
    ]);
  });

  it("导入：409 同名冲突必须抛错（不能被报告分支吞掉）", async () => {
    mockFetch(() =>
      jsonResponse({ error: "目标层已存在同名 profile", code: "CONFIG_INVALID" }, 409),
    );

    const error = await importRuntimeProfile(new Blob([new Uint8Array([80])]), {
      layer: "user",
    }).catch((caught: unknown) => caught);

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect((error as RuntimeApiError).status).toBe(409);
    expect(isRuntimeApiErrorCode(error, "CONFIG_INVALID")).toBe(true);
  });

  it("导入：普通 400 错误信封（无报告字段）仍抛错，不降级成空报告", async () => {
    mockFetch(() => jsonResponse({ error: "导入包为空", code: "VALIDATION_FAILED" }, 400));

    const error = await importRuntimeProfile(new Blob([new Uint8Array([80])])).catch(
      (caught: unknown) => caught,
    );

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect(isRuntimeApiErrorCode(error, "VALIDATION_FAILED")).toBe(true);
  });
});
