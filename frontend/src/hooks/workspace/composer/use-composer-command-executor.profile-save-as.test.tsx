// @vitest-environment jsdom

// Batch 13 slice 10：composer `/profile save-as <name> [--to user|project]` 的**执行分支**单测
// ——G1/D24 的「从当前会话创建」（前端入口；差分与落盘在后端，D36）。
//
// 从 `use-composer-command-executor.profile.test.tsx` 拆出（P0-2 行数门禁：单文件非空行
// ≤ 500）；共享脚手架见 `use-composer-command-executor.test-helpers.tsx`。
//
// 覆盖口径：成功路径的请求体（from_session 取当前会话、layer 缺省不传）、
// 参数解析四态（缺名 / 未知开关 / 非法层 / 多余参数）、无会话前置失败、
// 并发保护（上一次固化未完成不重入）、失败不伪装成功，以及
// **子命令不落到「切换 profile」分支**（回执与切换互不串台）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { createRuntimeProfileMock, exportSessionTrajectoryJsonlMock, executeSkillMock } =
  vi.hoisted(() => ({
    createRuntimeProfileMock: vi.fn(),
    exportSessionTrajectoryJsonlMock: vi.fn(),
    executeSkillMock: vi.fn(),
  }));

vi.mock("@/lib/trajectory/export-session", () => ({
  exportSessionTrajectoryJsonl: exportSessionTrajectoryJsonlMock,
}));

vi.mock("@/api/runtime/skills", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/runtime/skills")>()),
  executeSkill: executeSkillMock,
}));

vi.mock("@/api/runtime/profiles", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/runtime/profiles")>()),
  createRuntimeProfile: createRuntimeProfileMock,
}));

import type { RuntimeProfileCreateResponse } from "@/types/runtime";
import { buildComposerBuiltinCommands } from "@/lib/composer-builtin-commands";
import {
  createComposerCommandRegistry,
  findComposerCommand,
  type ComposerCommand,
} from "@/lib/composer-commands";
import { createComposerExecutorHarness } from "./use-composer-command-executor.test-helpers";
import type { UseComposerCommandExecutorOptions } from "./use-composer-command-executor";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

// `/profile` 只在宿主确认后端能力广告（`session_switch`）后注册（R20），静态清单
// 里没有它——这里单独构造带能力广告的注册表来验证执行分支本身。
const profileRegistry = createComposerCommandRegistry(
  buildComposerBuiltinCommands({ profileSwitchSupported: true }),
);

function profileCommand(): ComposerCommand {
  const found = findComposerCommand(profileRegistry, "profile");
  if (!found) {
    throw new Error("test setup: command profile is not registered");
  }
  return found;
}

/** 固化响应：与后端 `mode=save_as` 报告面同形（`surface` 是扁平计数摘要，D36）。 */
function saveAsResponse(
  overrides: Partial<RuntimeProfileCreateResponse> = {},
): RuntimeProfileCreateResponse {
  return {
    name: "review-2",
    root: "/home/u/.aicli/profiles/review-2",
    layer: "user",
    files: ["profile.yaml", "agents/reviewer.yaml"],
    ref: "user:review-2",
    registered: false,
    defaultProfileSet: false,
    affects: "new_sessions_only",
    configPath: "",
    mode: "save_as",
    fromSession: "session-1",
    baseline: "review",
    surface: { "tools.allowlist": 2, "skills.denylist": 1, "tools.read_only": false },
    omitted: ["prompt（会话 prompt 可能来自临时上下文，D24 明确不固化）；需要时在 profile 里另加 prompt 文件"],
    ...overrides,
  };
}

describe("useComposerCommandExecutor /profile save-as", () => {
  const harness = createComposerExecutorHarness();

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    harness.mount();
    createRuntimeProfileMock.mockReset();
    exportSessionTrajectoryJsonlMock.mockReset();
    executeSkillMock.mockReset();
  });

  afterEach(() => {
    harness.unmount();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  const current = () => harness.current();

  function render(options: UseComposerCommandExecutorOptions) {
    return harness.render(options);
  }

  /** 派发 `/profile ...`（注册表带能力广告）并等状态回填。 */
  async function runProfile(args: string) {
    return harness.run(profileCommand(), args);
  }

  it("/profile save-as <name>：以当前会话固化，回执给差分字段数与「未激活」指引", async () => {
    createRuntimeProfileMock.mockResolvedValue(saveAsResponse());
    const applyProfile = vi.fn();
    await render({
      sessionId: "session-1",
      profileSelection: {
        candidates: [],
        applyProfile,
        openDialog: vi.fn(),
      },
    });

    const handled = await runProfile("save-as review-2");

    expect(handled).toBe(true);
    // 关键契约：`from_session` 取当前会话；未指定 `--to` 时不传 layer（走后端默认层）。
    expect(createRuntimeProfileMock).toHaveBeenCalledWith({
      name: "review-2",
      layer: undefined,
      fromSession: "session-1",
    });
    // 子命令不得落到「切换 profile」分支（回执互不串台）。
    expect(applyProfile).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.profile.saveAs.done",
      values: { profile: "user:review-2", fields: 2, omitted: 1 },
    });
  });

  it("/profile save-as <name> --to project：显式层随请求体透传", async () => {
    createRuntimeProfileMock.mockResolvedValue(saveAsResponse({ layer: "project" }));
    await render({ sessionId: "session-1" });

    await runProfile("save-as review-2 --to project");

    expect(createRuntimeProfileMock).toHaveBeenCalledWith({
      name: "review-2",
      layer: "project",
      fromSession: "session-1",
    });
  });

  it("缺名 / 未知开关 / 非法层 / 多余参数：如实报参数错，不发请求", async () => {
    await render({ sessionId: "session-1" });

    await runProfile("save-as");
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.saveAs.needName",
    });

    await runProfile("save-as review-2 --force");
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.saveAs.unknownFlag",
      values: { flag: "--force" },
    });

    await runProfile("save-as review-2 --to team");
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.saveAs.badLayer",
      values: { value: "team" },
    });

    await runProfile("save-as review-2 extra");
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.saveAs.unexpectedArgument",
      values: { value: "extra" },
    });

    expect(createRuntimeProfileMock).not.toHaveBeenCalled();
  });

  it("无会话：前置失败，不发请求（不伪造「已固化」）", async () => {
    await render({});

    await runProfile("save-as review-2");

    expect(createRuntimeProfileMock).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.saveAs.noSession",
    });
  });

  it("并发保护：上一次固化未完成时第二次调用被拒（不并发写盘）", async () => {
    // 明确赋值断言：resolver 由 mockImplementation 的 executor 回调内部分配，
    // TS 的流分析看不到闭包内赋值（会窄化成 null/never）。
    let release!: (value: RuntimeProfileCreateResponse) => void;
    createRuntimeProfileMock.mockImplementation(
      () =>
        new Promise<RuntimeProfileCreateResponse>((resolve) => {
          release = resolve;
        }),
    );
    await render({ sessionId: "session-1" });

    await runProfile("save-as first");
    expect(createRuntimeProfileMock).toHaveBeenCalledTimes(1);

    await runProfile("save-as second");
    expect(createRuntimeProfileMock).toHaveBeenCalledTimes(1);
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.saveAs.inProgress",
    });

    release(saveAsResponse());
    await harness.flush();
    expect(current().notice?.messageKey).toBe("composer.builtin.profile.saveAs.done");
  });

  it("后端失败：错误回执带原因（不伪装成功）", async () => {
    createRuntimeProfileMock.mockRejectedValue(new Error("当前无差异，无需固化"));
    await render({ sessionId: "session-1" });

    await runProfile("save-as review-2");

    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.saveAs.failed",
      values: { message: "当前无差异，无需固化" },
    });
  });
});
