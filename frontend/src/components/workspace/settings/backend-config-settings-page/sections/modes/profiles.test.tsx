// @vitest-environment jsdom

// Profiles 设置页交互测试：mock 全局 fetch（走真实 API 客户端），覆盖列表页与
// Batch 8 后端契约的关键交互：
//   * ref 含 `:`/空格时，单资源端点整段 encodeURIComponent；
//   * 只读条目（writable=false）的写操作按钮禁用而不隐藏；
//   * 过滤只收窄可见行，页头汇总胶囊仍报总数；
//   * 新建向导：非法名称不能进入下一步；提交体是后端契约字段（snake_case）；
//   * rename 响应是 new_ref/root（不是 ref/name/path）：列表必须换成新句柄（回归）；
//   * move 只换层级与根目录：层级徽标要跟着响应更新（回归）；
//   * delete 被引用 409：回填阻断数并禁用确认，勾选强制后才允许，且第二次带 ?force=true；
//   * apply 在本页只做引导：按钮禁用且不发请求（真实切换在会话内用 /profile）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { i18n } from "@/i18n";

import { ProfilesModeSection } from "./profiles";

const PROFILES_URL = "/api/runtime/profiles";
/** 含 `:` 与空格的 ref：所有单资源端点都必须整段编码，不能做路径拼接假设。 */
const REF_SPACED = "user:reviewer plan";
const REF_BUILTIN = "builtin:core";

/** 后端列表条目：字段名与 Go runtimeProfileEntry 的 json tag 一致。 */
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
  /** FR-14：后端 runtimeProfileEntry.is_bound（旧后端缺失 = 不显示徽标）。 */
  is_bound?: boolean;
};

type RecordedCall = { url: string; method: string; body: string | null };

const originalFetch = globalThis.fetch;

let entries: ListEntry[] = [];
let defaultProfile = "";
let calls: RecordedCall[] = [];
/** 下一次 DELETE 返回 409 引用阻断（模拟 profiles_lifecycle_handlers 的语义）。 */
let deleteBlocked = false;
/** 列表响应里的 FR-14 项目绑定（null = 旧后端/未声明工作区：不发该字段）。 */
let projectBinding: Record<string, unknown> | null = null;

/**
 * 与组件同源取词：固定 runtimeConfig 命名空间；键来自测试用的动态拼装，
 * 显式放宽成 (key: string) 形态，避免与类型化 t 的字面量键约束互搏。
 */
const t = i18n.getFixedT(null, "runtimeConfig") as unknown as (
  key: string,
  options?: Record<string, unknown>,
) => string;

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

function installFetchMock(
  seed: ListEntry[],
  options: { binding?: Record<string, unknown> | null } = {},
) {
  entries = seed.map((item) => ({ ...item }));
  defaultProfile = entries.find((item) => item.is_default)?.name ?? "";
  calls = [];
  deleteBlocked = false;
  projectBinding = options.binding ?? null;

  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({ url, method, body: typeof init?.body === "string" ? init.body : null });
    const [pathname, query = ""] = url.split("?");
    const readBody = () => JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;

    if (pathname === PROFILES_URL && method === "GET") {
      return jsonResponse({
        count: entries.length,
        default_profile: defaultProfile,
        profiles: entries,
        // FR-14：后端只在请求带 workspace 参数时回填 project_binding；这里用
        // "字段是否存在"复现旧后端/新后端两种响应，避免前端为旧后端猜测状态。
        ...(projectBinding ? { project_binding: projectBinding } : {}),
        workspace_path: "/ws",
        workspace_trusted: true,
        workspace_trust_feature_enabled: false,
      });
    }

    if (pathname === PROFILES_URL && method === "POST") {
      const body = readBody();
      const name = String(body.name ?? "");
      const layer = String(body.layer || "user");
      const created = entry(`${layer}:${name}`, { name, layer });
      entries = [...entries, created];
      return jsonResponse({
        created: true,
        name,
        root: created.path,
        layer,
        files: ["profile.yaml"],
        profile: { ref: created.ref, name, layer },
        registered: true,
        default_profile_set: false,
        affects: "new_sessions_only",
      });
    }

    if (!pathname.startsWith(`${PROFILES_URL}/`)) {
      return jsonResponse({ error: "unexpected request" }, 404);
    }

    const tail = pathname.slice(PROFILES_URL.length + 1);
    const separator = tail.lastIndexOf("/");
    const ref = decodeURIComponent(separator >= 0 ? tail.slice(0, separator) : tail);
    const action = separator >= 0 ? tail.slice(separator + 1) : "";
    const current = entries.find((item) => item.ref === ref);
    if (!current) {
      return jsonResponse({ error: `unknown profile ref: ${ref}` }, 404);
    }

    if (method === "DELETE" && action === "") {
      if (deleteBlocked && !query.includes("force=true")) {
        return jsonResponse(
          {
            deleted: false,
            error: "profile is still referenced",
            references: {
              ref,
              root: current.path,
              blocking: ["session s1 (agent chat)"],
              warnings: [],
              files: [],
            },
          },
          409,
        );
      }
      entries = entries.filter((item) => item.ref !== ref);
      return jsonResponse({ deleted: true, ref, references: null });
    }

    if (method === "POST" && action === "rename") {
      const name = String(readBody().name ?? "");
      const nextRef = `${current.layer}:${name}`;
      entries = entries.map((item) => (item.ref === ref ? { ...item, ref: nextRef, name } : item));
      return jsonResponse({
        renamed: true,
        old_ref: ref,
        new_ref: nextRef,
        root: current.path,
        layer: current.layer,
        config_updated: true,
        error: "",
        session_note: "",
        references: null,
      });
    }

    if (method === "POST" && action === "move") {
      const layer = String(readBody().layer ?? "");
      const to = `/profiles/${layer}/profile.yaml`;
      entries = entries.map((item) =>
        item.ref === ref ? { ...item, layer, path: to } : item,
      );
      return jsonResponse({
        moved: true,
        ref,
        from: current.path,
        to,
        layer,
        config_updated: true,
        error: "",
        references: null,
      });
    }

    if (method === "POST" && action === "default") {
      defaultProfile = current.name;
      return jsonResponse({ default_profile: current.name, registered: true });
    }

    return jsonResponse({ error: `unexpected ${method} ${action}` }, 404);
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

function testidButton(id: string) {
  return document.body.querySelector<HTMLButtonElement>(`[data-testid="${id}"]`);
}

function actionButton(action: string, ref: string) {
  return document.body.querySelector<HTMLButtonElement>(
    `[data-testid="profiles-action-${action}-${ref}"]`,
  );
}

function rowFor(ref: string) {
  return document.body.querySelector(`[data-profile-ref="${ref}"]`);
}

/** 页头汇总胶囊取值（SummaryPill = 标签 span + 值 span，标签用 app-text-10）。 */
function summaryValues() {
  const values: string[] = [];
  for (const label of document.body.querySelectorAll("span.app-text-10")) {
    const value = label.nextElementSibling;
    if (value) {
      values.push(value.textContent?.trim() ?? "");
    }
  }
  return values;
}

function dialogElement() {
  return document.body.querySelector('[role="dialog"]');
}

function dialogInput() {
  return dialogElement()?.querySelector<HTMLInputElement>('input[type="text"]') ?? null;
}

/** React 受控 input：必须走原生 setter + input 事件，直接赋值不触发 onChange。 */
function setInputValue(input: HTMLInputElement | null, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  if (!input || !setter) {
    throw new Error("input not found");
  }
  setter.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("ProfilesModeSection", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    // jsdom 不实现 scrollIntoView：Select 展开列表时会调用，测试里补一个空实现。
    Element.prototype.scrollIntoView = vi.fn();
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

  /** 受控输入必须走原生 setter + input 事件，且状态更新要包在 act 里。 */
  async function type(input: HTMLInputElement | null, value: string) {
    await act(async () => {
      setInputValue(input, value);
    });
    await act(flush);
  }

  it("渲染列表：只读条目写操作按钮禁用但不隐藏", async () => {
    installFetchMock([
      entry(REF_SPACED, { is_default: true }),
      entry(REF_BUILTIN, { name: "core", writable: false }),
    ]);

    await renderSection();

    expect(rowFor(REF_SPACED)).not.toBeNull();
    expect(rowFor(REF_BUILTIN)).not.toBeNull();
    expect(actionButton("edit", REF_SPACED)?.disabled).toBe(false);
    expect(actionButton("delete", REF_SPACED)?.disabled).toBe(false);
    expect(actionButton("edit", REF_BUILTIN)).not.toBeNull();
    expect(actionButton("edit", REF_BUILTIN)?.disabled).toBe(true);
    expect(actionButton("delete", REF_BUILTIN)?.disabled).toBe(true);
  });

  it("过滤只收窄可见行，页头汇总仍报总数", async () => {
    installFetchMock([entry(REF_SPACED, { is_default: true }), entry(REF_BUILTIN, { name: "core" })]);

    await renderSection();

    expect(document.body.querySelectorAll("[data-profile-ref]")).toHaveLength(2);
    const totals = summaryValues();
    expect(totals).toContain(t("profiles.list.count", { count: 2 }));

    await type(document.body.querySelector<HTMLInputElement>('input[type="text"]'), "core");

    expect(document.body.querySelectorAll("[data-profile-ref]")).toHaveLength(1);
    expect(rowFor(REF_BUILTIN)).not.toBeNull();
    expect(rowFor(REF_SPACED)).toBeNull();
    expect(summaryValues()).toEqual(totals);
  });

  it("新建向导：非法名称不能下一步，提交体使用后端契约字段", async () => {
    installFetchMock([entry(REF_SPACED)]);

    await renderSection();
    await click(testidButton("profiles-create-open"));

    await type(dialogInput(), "Bad Name");
    expect(dialogElement()?.querySelector('[role="alert"]')?.textContent).toContain(
      t("profiles.create.nameInvalid"),
    );
    expect(testidButton("profiles-create-next")?.disabled).toBe(true);

    await type(dialogInput(), "reviewer-v2");
    expect(testidButton("profiles-create-next")?.disabled).toBe(false);

    await click(testidButton("profiles-create-next"));
    await click(testidButton("profiles-create-next"));
    await click(testidButton("profiles-create-submit"));

    const create = calls.find((call) => call.method === "POST" && call.url === PROFILES_URL);
    expect(create).toBeDefined();
    expect(JSON.parse(create?.body ?? "{}")).toEqual({
      name: "reviewer-v2",
      layer: "user",
      root: "",
      template: "minimal",
      from_ref: "",
      // D24/D36：save-as 的会话固化走同一创建端点（三模式合一入口），
      // 契约字段与 from_ref/template 一样常驻，空串 = 未指定（后端 TrimSpace 后忽略）。
      from_session: "",
      agent: "",
      force: false,
      use: false,
      set_default: false,
    });
    expect(dialogElement()).toBeNull();
    expect(rowFor("user:reviewer-v2")).not.toBeNull();
    expect(document.body.querySelector('[data-testid="profiles-status"]')).not.toBeNull();
  });

  it("rename：响应形状是 new_ref/root，列表句柄必须换成新 ref", async () => {
    installFetchMock([entry(REF_SPACED)]);

    await renderSection();
    await click(actionButton("rename", REF_SPACED));

    await type(dialogInput(), "reviewer-v2");
    await click(testidButton("profiles-dialog-confirm"));

    const rename = calls.find((call) => call.url.includes("/rename"));
    expect(rename?.method).toBe("POST");
    expect(rename?.url).toBe(`${PROFILES_URL}/${encodeURIComponent(REF_SPACED)}/rename`);
    expect(JSON.parse(rename?.body ?? "{}")).toEqual({ name: "reviewer-v2" });
    expect(rowFor("user:reviewer-v2")).not.toBeNull();
    expect(rowFor(REF_SPACED)).toBeNull();
  });

  it("move：层级徽标跟随响应更新，端点仍用旧 ref 整段编码", async () => {
    installFetchMock([entry(REF_SPACED)]);

    await renderSection();
    expect(rowFor(REF_SPACED)?.textContent).toContain(t("profiles.layers.user"));
    await click(actionButton("move", REF_SPACED));

    // 迁移对话框默认选中「另一层」，无需再点下拉即可确认。
    const targetLabel = t("profiles.layers.project");
    expect(dialogElement()?.textContent).toContain(targetLabel);
    await click(testidButton("profiles-dialog-confirm"));

    const move = calls.find((call) => call.url.includes("/move"));
    expect(move?.url).toBe(`${PROFILES_URL}/${encodeURIComponent(REF_SPACED)}/move`);
    expect(JSON.parse(move?.body ?? "{}")).toMatchObject({ layer: "project" });
    expect(rowFor(REF_SPACED)?.textContent).toContain(targetLabel);
    expect(rowFor(REF_SPACED)?.textContent).not.toContain(t("profiles.layers.user"));
  });

  it("delete 被引用阻断：回填阻断数并禁用确认，强制删除才带 ?force=true", async () => {
    installFetchMock([entry(REF_SPACED), entry(REF_BUILTIN, { name: "core" })]);
    deleteBlocked = true;

    await renderSection();
    await click(actionButton("delete", REF_SPACED));
    await click(testidButton("profiles-dialog-confirm"));

    const first = calls.filter((call) => call.method === "DELETE");
    expect(first).toHaveLength(1);
    expect(first[0]?.url).toBe(`${PROFILES_URL}/${encodeURIComponent(REF_SPACED)}`);
    expect(document.body.querySelector('[data-testid="profiles-error"]')).not.toBeNull();
    expect(testidButton("profiles-dialog-confirm")?.disabled).toBe(true);

    const forceToggle =
      dialogElement()?.querySelector<HTMLInputElement>('input[type="checkbox"]') ?? null;
    expect(forceToggle).not.toBeNull();
    await click(forceToggle);
    expect(testidButton("profiles-dialog-confirm")?.disabled).toBe(false);

    await click(testidButton("profiles-dialog-confirm"));
    const deletes = calls.filter((call) => call.method === "DELETE");
    expect(deletes).toHaveLength(2);
    expect(deletes[1]?.url).toBe(
      `${PROFILES_URL}/${encodeURIComponent(REF_SPACED)}?force=true`,
    );
    expect(rowFor(REF_SPACED)).toBeNull();
    expect(rowFor(REF_BUILTIN)).not.toBeNull();
  });

  it("apply 在本页只做引导：按钮禁用、不发请求（切换在会话内用 /profile）", async () => {
    installFetchMock([entry(REF_SPACED)]);

    await renderSection();

    // 设置页没有会话上下文，apply 必须显式给 session_id（服务端不推断「当前会话」）：
    // 因此按钮可见但禁用，label 直接指向会话内的 composer `/profile`。
    const applyButton = actionButton("apply", REF_SPACED);
    expect(applyButton?.disabled).toBe(true);
    expect(applyButton?.getAttribute("aria-label")).toBe(t("profiles.list.applyDisabled"));
    expect(applyButton?.getAttribute("aria-label")).toContain("/profile");
    expect(calls.some((call) => call.url.includes("/apply"))).toBe(false);
  });

  /** FR-14 绑定载荷：字段名与 Go ProjectProfileBinding 的 json tag 一致。 */
  function bindingPayload(overrides: Record<string, unknown> = {}) {
    return {
      present: true,
      valid: true,
      ref: "demo",
      workspace_path: "/ws/demo",
      path: "/ws/demo/.aicli/profile",
      profile_root: "/ws/demo/.aicli/profiles/demo",
      layer: "project",
      source: "project_binding",
      error: "",
      prompt_suppressed: false,
      prompt_suppression_reason: "",
      ...overrides,
    };
  }

  it("旧后端响应无 project_binding：不渲染绑定卡片，也不制造错误", async () => {
    installFetchMock([entry(REF_SPACED)]);

    await renderSection();

    expect(document.body.querySelector('[data-testid="profiles-project-binding"]')).toBeNull();
    expect(
      document.body.querySelector('[data-testid="profiles-project-binding-error"]'),
    ).toBeNull();
  });

  it("没有绑定文件（present=false）：不渲染卡片，也不标注任何行", async () => {
    installFetchMock([entry("project:demo", { name: "demo" })], {
      binding: bindingPayload({ present: false, valid: false, ref: "" }),
    });

    await renderSection();

    expect(document.body.querySelector('[data-testid="profiles-project-binding"]')).toBeNull();
    expect(document.body.querySelector('[data-testid="profiles-bound-project:demo"]')).toBeNull();
  });

  it("项目绑定：只读卡片 + 行内绑定徽标，且不自动 apply / 不设默认", async () => {
    installFetchMock([entry("project:demo", { name: "demo", is_bound: true })], {
      binding: bindingPayload(),
    });

    await renderSection();

    const card = document.body.querySelector('[data-testid="profiles-project-binding"]');
    expect(card).not.toBeNull();
    expect(card?.textContent).toContain(t("profiles.projectBinding.statusValid"));
    expect(card?.textContent).toContain("/ws/demo/.aicli/profiles/demo");
    // 只读：给出"会话内切换"的可执行指引，而不是暴露一个点了会报错的按钮。
    const hint = t("profiles.projectBinding.applyHint", { ref: "demo" });
    expect(card?.textContent).toContain(hint);
    // 两个会话面语法不同，文案必须各写各的：Web composer 的 `/profile <ref>` 把整串当
    // ref（argumentHint `[profile | save-as …]`），终端 TUI 的 `/profile` 需要 `use`
    // 子命令（裸 ref 会落到 unknown subcommand）。只给一种写法就会让另一面的用户照着敲出报错。
    expect(hint).toContain("/profile demo");
    expect(hint).toContain("/profile use demo");
    expect(document.body.querySelector('[data-testid="profiles-bound-project:demo"]')).not.toBeNull();

    // 发现是只读的：列表加载不得触发 apply / default 写端点。
    expect(
      calls.some((call) => call.url.includes("/apply") || call.url.includes("/default")),
    ).toBe(false);
  });

  it("绑定不可用：展示错误与路径，隐藏应用指引且不标注行", async () => {
    installFetchMock([entry("project:demo", { name: "demo" })], {
      binding: bindingPayload({
        valid: false,
        error: "project profile target 不存在：/ws/demo/.aicli/profiles/demo/profile.yaml",
      }),
    });

    await renderSection();

    const error = document.body.querySelector('[data-testid="profiles-project-binding-error"]');
    expect(error?.textContent).toContain("不存在");
    expect(
      document.body.querySelector('[data-testid="profiles-project-binding-apply-hint"]'),
    ).toBeNull();
    expect(document.body.querySelector('[data-testid="profiles-bound-project:demo"]')).toBeNull();
  });

  it("未信任工作区：绑定卡片显示提示词扣留警告", async () => {
    installFetchMock([entry("project:demo", { name: "demo", is_bound: true })], {
      binding: bindingPayload({
        prompt_suppressed: true,
        prompt_suppression_reason: "工作区未信任：项目级 prompts 未应用",
      }),
    });

    await renderSection();

    const warning = document.body.querySelector(
      '[data-testid="profiles-project-binding-suppressed"]',
    );
    expect(warning).not.toBeNull();
    expect(warning?.textContent).toContain(t("profiles.projectBinding.suppressed"));
    expect(warning?.getAttribute("title")).toBe("工作区未信任：项目级 prompts 未应用");
  });
});
