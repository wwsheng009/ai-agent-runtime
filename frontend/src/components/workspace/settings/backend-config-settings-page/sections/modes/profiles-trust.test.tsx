// @vitest-environment jsdom

// Batch 14 slice 5（Q22）前端闭环测试：工作区未信任时的"部分内容未应用"徽标 +
// 一键信任入口。覆盖：
//   * 未信任：列表请求带 workspace 参数，提示条与逐条徽标都出现（数据全部来自响应）；
//   * 信任是两步动作：第一次点击只展开确认，不发请求；确认后才 POST 且 body 是
//     后端契约（action=grant + workspace_path）；
//   * 授予后重新拉列表：徽标与提示条随**响应**消失（不靠本地状态猜），并给出重载指引；
//   * 特性关闭（feature_enabled=false）：不渲染提示条（门控关闭时 trusted 恒真）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { i18n } from "@/i18n";

import { ProfilesModeSection } from "./profiles";

const PROFILES_URL = "/api/runtime/profiles";
const TRUST_URL = "/api/runtime/harness/trust";
const WORKSPACE = "E:/ws/project";
const REF = "project:review";

type RecordedCall = { url: string; method: string; body: string | null };

const originalFetch = globalThis.fetch;

/** 与组件同源取词（键来自测试里的动态拼装，显式放宽字面量键约束）。 */
const t = i18n.getFixedT(null, "runtimeConfig") as unknown as (
  key: string,
  options?: Record<string, unknown>,
) => string;

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

let trusted = false;
let featureEnabled = true;
let suppressed = true;
let calls: RecordedCall[] = [];

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** 列表响应：字段名与 Go runtimeProfileEntry / profiles_handlers 的 json tag 一致。 */
function listPayload() {
  return {
    count: 1,
    default_profile: "",
    profiles: [
      {
        ref: REF,
        name: "review",
        description: "",
        layer: "project",
        path: "E:/ws/project/.aicli/profiles/review/profile.yaml",
        valid: true,
        error: "",
        is_default: false,
        default_agent: "",
        writable: true,
        prompt_suppressed: suppressed,
        prompt_suppression_reason: suppressed ? "项目级 profile 的提示词因工作区未信任而未应用" : "",
      },
    ],
    workspace_path: WORKSPACE,
    workspace_trusted: trusted,
    workspace_trust_feature_enabled: featureEnabled,
  };
}

function installFetchMock() {
  calls = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: typeof init?.body === "string" ? init.body : null,
    });
    const [pathname] = url.split("?");

    if (pathname === PROFILES_URL && method === "GET") {
      return jsonResponse(listPayload());
    }
    if (pathname === TRUST_URL && method === "POST") {
      // 与 harness_handlers.go 的授予语义一致：持久化后回读即 trusted，
      // 下一次列表请求不再有扣留标记。
      trusted = true;
      suppressed = false;
      return jsonResponse({
        workspace_path: WORKSPACE,
        feature_enabled: true,
        trusted: true,
        source: "recorded",
        action: "grant",
      });
    }
    return jsonResponse({ error: "unexpected request" }, 404);
  });
  globalThis.fetch = fetchMock as unknown as typeof fetch;
}

function profilesCalls() {
  return calls.filter((call) => call.url.split("?")[0] === PROFILES_URL);
}

describe("ProfilesModeSection 工作区信任闭环（Q22）", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    trusted = false;
    featureEnabled = true;
    suppressed = true;
    vi.stubEnv("VITE_RUNTIME_WORKSPACE_PATH", WORKSPACE);
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
    globalThis.fetch = originalFetch;
    vi.unstubAllEnvs();
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

  function byTestId(testId: string) {
    return document.body.querySelector(`[data-testid="${testId}"]`);
  }

  it("未信任：列表请求带 workspace，提示条与徽标来自响应", async () => {
    installFetchMock();

    await renderSection();

    expect(byTestId("profiles-trust-notice")).not.toBeNull();
    expect(byTestId(`profiles-suppressed-${REF}`)).not.toBeNull();
    expect(document.body.textContent ?? "").toContain(t("profiles.list.promptSuppressed"));
    expect(document.body.textContent ?? "").toContain(t("profiles.trust.title"));
    // 工作区路径必须随请求发出，否则后端不会回信任上下文（Q22 数据面）。
    expect(profilesCalls()).toHaveLength(1);
    expect(decodeURIComponent(profilesCalls()[0].url)).toContain(`workspace=${WORKSPACE}`);
    // 未确认前不产生任何信任写请求。
    expect(calls.some((call) => call.url.split("?")[0] === TRUST_URL)).toBe(false);
  });

  it("两步确认后授予信任：POST 契约正确，徽标随刷新后的响应消失", async () => {
    installFetchMock();

    await renderSection();

    // 第一次点击只展开确认，不发请求（信任是权限提升动作）。
    await click(byTestId("profiles-trust-grant"));
    expect(calls.some((call) => call.method === "POST")).toBe(false);
    expect(byTestId("profiles-trust-confirm")).not.toBeNull();

    await click(byTestId("profiles-trust-confirm"));

    const grantCalls = calls.filter(
      (call) => call.method === "POST" && call.url.split("?")[0] === TRUST_URL,
    );
    expect(grantCalls).toHaveLength(1);
    expect(grantCalls[0].body).toContain('"action":"grant"');
    expect(grantCalls[0].body).toContain(`"workspace_path":"${WORKSPACE}"`);

    // 授予后重新拉列表：提示条与徽标都随响应消失，并给出会话内重载指引。
    expect(profilesCalls()).toHaveLength(2);
    expect(byTestId("profiles-trust-notice")).toBeNull();
    expect(byTestId(`profiles-suppressed-${REF}`)).toBeNull();
    expect(byTestId("profiles-status")?.textContent ?? "").toContain(t("profiles.trust.granted"));
  });

  it("特性关闭：即使 trusted=false 也不渲染提示条（门控未启用时不制造假警告）", async () => {
    featureEnabled = false;
    // 门控关闭时 foldertrust 语义恒 trusted → 后端也不会给出扣留标记。
    suppressed = false;
    installFetchMock();

    await renderSection();

    expect(byTestId("profiles-trust-notice")).toBeNull();
    expect(byTestId(`profiles-suppressed-${REF}`)).toBeNull();
  });
});