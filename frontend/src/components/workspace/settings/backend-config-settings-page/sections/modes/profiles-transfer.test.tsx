// @vitest-environment jsdom

// Profiles 导出/导入入口交互测试（Batch 13 slice 8 / G5 / D28 / D32 / D33）：
//   * 导出：POST 到 /export，响应体交给浏览器下载（createObjectURL + <a download>）；
//   * 导入：先预演（dry_run=true）后落盘；预演未通过时确认禁用、issues 可见；
//   * 落盘后关闭对话框、刷新列表，且提示「未激活」（D28：导入绝不自动激活）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { i18n } from "@/i18n";

import { ProfilesModeSection } from "./profiles";

const PROFILES_URL = "/api/runtime/profiles";
const REF = "user:reviewer";

/**
 * 与组件同源取词（同时确保 i18n 已初始化）：键来自字面量，这里放宽成
 * (key: string) 形态，避免与类型化 t 的字面量键约束互搏（同 profiles.test.tsx）。
 */
const t = i18n.getFixedT(null, "runtimeConfig") as unknown as (
  key: string,
  options?: Record<string, unknown>,
) => string;

type ListEntry = {
  ref: string;
  name: string;
  description: string;
  layer: string;
  path: string;
  valid: boolean;
  error: string;
  is_default: boolean;
  default_agent: string;
  writable: boolean;
};

type RecordedCall = { url: string; method: string; body: unknown };

const originalFetch = globalThis.fetch;

let entries: ListEntry[] = [];
let calls: RecordedCall[] = [];
/** 下一次导入（真实落盘）返回的报告；默认成功。 */
let importReport: Record<string, unknown> = {};

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function entry(ref: string, overrides: Partial<ListEntry> = {}): ListEntry {
  const [layer, ...rest] = ref.split(":");
  return {
    ref,
    name: rest.join(":") || ref,
    description: "",
    layer,
    path: `/profiles/${layer}/profile.yaml`,
    valid: true,
    error: "",
    is_default: false,
    default_agent: "",
    writable: true,
    ...overrides,
  };
}

function installFetchMock() {
  entries = [entry(REF)];
  calls = [];
  importReport = {
    ok: true,
    valid: true,
    imported: true,
    activated: false,
    dry_run: false,
    layer: "user",
    name: "reviewer",
    root: "/profiles/user/reviewer",
    paths: ["profile.yaml"],
    file_count: 1,
    hint: "已导入并落在列表",
  };

  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({ url, method, body: init?.body ?? null });
    const [pathname, query = ""] = url.split("?");
    const params = new URLSearchParams(query);

    if (pathname === PROFILES_URL && method === "GET") {
      return jsonResponse({
        count: entries.length,
        default_profile: "",
        profiles: entries,
      });
    }

    if (pathname.endsWith("/export") && method === "POST") {
      return new Response(new Uint8Array([80, 75, 3, 4]), {
        status: 200,
        headers: {
          "Content-Type": "application/zip",
          "Content-Disposition": 'attachment; filename="reviewer.zip"',
          "X-Aicli-Profile-Ref": REF,
          "X-Aicli-Profile-File-Count": "2",
        },
      });
    }

    if (pathname === `${PROFILES_URL}/import` && method === "POST") {
      if (params.get("dry_run") === "true") {
        return jsonResponse({
          ok: true,
          valid: true,
          imported: false,
          activated: false,
          dry_run: true,
          layer: params.get("layer") ?? "user",
          name: "reviewer",
          paths: ["profile.yaml", "prompts/system.md"],
          file_count: 2,
        });
      }
      const report = importReport;
      if (report.imported === true) {
        entries = [...entries, entry("user:reviewer-copy", { name: "reviewer" })];
      }
      return jsonResponse(report, report.imported === true ? 201 : 400);
    }

    return jsonResponse({ error: `unexpected ${method} ${pathname}` }, 404);
  });

  globalThis.fetch = fetchMock as unknown as typeof fetch;
}

function flush() {
  let chain = Promise.resolve();
  for (let index = 0; index < 6; index += 1) {
    chain = chain.then(() => undefined);
  }
  return chain;
}

function testidElement<T extends Element>(id: string) {
  return document.body.querySelector<T>(`[data-testid="${id}"]`);
}

function importCalls() {
  return calls.filter((call) => call.url.startsWith(`${PROFILES_URL}/import`));
}

describe("ProfilesModeSection 导出/导入入口", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  let createObjectURL: ReturnType<typeof vi.fn>;
  let revokeObjectURL: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    // jsdom 不实现 scrollIntoView 与 blob 下载：分别补空实现与可断言的桩。
    Element.prototype.scrollIntoView = vi.fn();
    createObjectURL = vi.fn(() => "blob:mock");
    revokeObjectURL = vi.fn();
    Object.assign(URL, { createObjectURL, revokeObjectURL });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT;
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
    delete (URL as unknown as { createObjectURL?: unknown }).createObjectURL;
    delete (URL as unknown as { revokeObjectURL?: unknown }).revokeObjectURL;
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  async function renderSection() {
    await act(async () => {
      root?.render(<ProfilesModeSection />);
    });
    await act(flush);
  }

  async function click(element: Element | null) {
    if (!element) {
      throw new Error("click target not found");
    }
    await act(async () => {
      (element as HTMLElement).click();
    });
    await act(flush);
  }

  /** React 的文件输入：files 只能通过属性描述符注入，再派发 change 事件。 */
  async function chooseFile(file: File) {
    const input = testidElement<HTMLInputElement>("profiles-import-file");
    if (!input) {
      throw new Error("file input not found");
    }
    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    await act(async () => {
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(flush);
  }

  function bundleFile() {
    return new File([new Uint8Array([80, 75])], "reviewer.zip", { type: "application/zip" });
  }

  it("导出：POST /export 后把响应体交给浏览器下载，并提示导出成功", async () => {
    installFetchMock();
    await renderSection();

    await click(testidElement(`profiles-action-export-${REF}`));

    const exportCall = calls.find((call) => call.url.endsWith("/export"));
    expect(exportCall?.method).toBe("POST");
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).toHaveBeenCalledTimes(1);
    expect(HTMLAnchorElement.prototype.click).toHaveBeenCalledTimes(1);
    expect(testidElement("profiles-status")?.textContent).toBe(
      t("profiles.transfer.exportSucceeded", { name: "reviewer", count: 2 }),
    );
    expect(testidElement("profiles-error")).toBeNull();
  });

  it("导入：先预演（dry_run=true）再落盘；成功后关对话框、刷新列表并提示未激活", async () => {
    installFetchMock();
    await renderSection();

    await click(testidElement("profiles-import-open"));
    expect(testidElement("profiles-import-file")).not.toBeNull();
    await chooseFile(bundleFile());

    await click(testidElement("profiles-import-preview"));
    expect(importCalls()[0].url).toBe(`${PROFILES_URL}/import?layer=user&dry_run=true`);
    const preview = testidElement("profiles-import-preview-result");
    expect(preview?.textContent).toContain("prompts/system.md");

    // 预演通过后确认按钮才可用：点击落盘（不带 dry_run），并关闭对话框。
    await click(testidElement("profiles-dialog-confirm"));
    expect(importCalls()[1].url).toBe(`${PROFILES_URL}/import?layer=user`);
    expect(testidElement("profiles-import-file")).toBeNull();
    expect(testidElement("profiles-status")?.textContent).toBe(
      t("profiles.transfer.importSucceeded", { name: "reviewer" }),
    );
    // 列表在导入后刷新（GET 至少两次：初始 + 导入后）。
    expect(calls.filter((call) => call.url === PROFILES_URL && call.method === "GET").length).toBeGreaterThanOrEqual(2);
  });

  it("导入：预演未通过时确认禁用、issues 可见（D28-1 拒绝而不是假成功）", async () => {
    installFetchMock();
    await renderSection();

    await click(testidElement("profiles-import-open"));
    await chooseFile(bundleFile());
    await click(testidElement("profiles-import-preview"));

    // 让下一次预演返回「未通过」报告：重开对话框并再次预演。
    const fetchMock = globalThis.fetch as unknown as ReturnType<typeof vi.fn>;
    fetchMock.mockImplementationOnce(async () =>
      jsonResponse(
        {
          ok: false,
          valid: false,
          imported: false,
          activated: false,
          dry_run: true,
          layer: "user",
          error: "导入包未通过 validate",
          error_count: 1,
          warning_count: 0,
          issues: [{ path: "profile.yaml", message: "unknown field: foo", severity: "error" }],
          paths: ["profile.yaml"],
          file_count: 1,
        },
        400,
      ),
    );
    await click(testidElement("profiles-import-preview"));

    const preview = testidElement("profiles-import-preview-result");
    expect(preview?.textContent).toContain("unknown field: foo");
    const confirm = testidElement<HTMLButtonElement>("profiles-dialog-confirm");
    expect(confirm?.disabled).toBe(true);
  });
});
