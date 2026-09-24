// @vitest-environment jsdom

// Batch 12：composer `/profile` 命令的**执行分支**单测。
//
// 从 `use-composer-command-executor.test.tsx` 拆出（P0-2 行数门禁：单文件非空行
// ≤ 500）；共享脚手架见 `use-composer-command-executor.test-helpers.tsx`。
//
// 覆盖口径：R20 注册前提之外的执行面——无参数开弹窗 / 精确与大小写不敏感解析 /
// 「不存在」vs「存在但不可用」vs「目录未就绪」三态区分 / 无会话前置失败 /
// 在途回合的「本回合仍走旧面」回执 / 差异告警条数与首条 / 失败不伪装成功 /
// 宿主未接线时带参数与无参数两条路径一致。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { exportSessionTrajectoryJsonlMock, executeSkillMock } = vi.hoisted(() => ({
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

import type { SessionProfileSwitchReport } from "@/api/runtime/profiles";
import { buildComposerBuiltinCommands } from "@/lib/composer-builtin-commands";
import {
  createComposerCommandRegistry,
  findComposerCommand,
  type ComposerCommand,
} from "@/lib/composer-commands";
import type { ComposerProfileCandidate } from "@/lib/composer-profile-options";
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

/** 目录候选：一条可解析 + 一条解析失败（用于区分「不存在」与「存在但不可用」）。 */
function profileCandidates(): ComposerProfileCandidate[] {
  return [
    {
      ref: "review",
      label: "Review",
      layer: "user",
      isDefault: false,
      valid: true,
      invalidReason: "",
    },
    {
      ref: "broken",
      label: "Broken",
      layer: "project",
      isDefault: false,
      valid: false,
      invalidReason: "profile.yaml: mapping values are not allowed here",
    },
  ];
}

function switchReport(
  overrides: Partial<SessionProfileSwitchReport> = {},
): SessionProfileSwitchReport {
  return {
    from: "",
    to: "review",
    changed: {
      toolsAdded: [],
      toolsRemoved: [],
      skillsAdded: [],
      skillsRemoved: [],
      mcpAdded: [],
      mcpRemoved: [],
      promptChanged: false,
      providerChanged: false,
      modelChanged: false,
      permissionModeChanged: false,
    },
    effectiveAt: "next_turn",
    cacheNotice: "",
    warnings: [],
    inFlightTurn: false,
    anchorCleared: true,
    toolSurfaceInvalidated: true,
    toolSurfaceScope: "actor",
    actorEvicted: true,
    contextTokenCountReset: false,
    ...overrides,
  };
}

describe("useComposerCommandExecutor /profile", () => {
  const harness = createComposerExecutorHarness();

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    harness.mount();
    exportSessionTrajectoryJsonlMock.mockReset();
    executeSkillMock.mockReset();
  });

  afterEach(() => {
    harness.unmount();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  /** 最近一次渲染的控制器快照（notice 随之更新）。 */
  const current = () => harness.current();

  function render(options: UseComposerCommandExecutorOptions) {
    return harness.render(options);
  }

  /** 派发 `/profile`（注册表带能力广告）并等状态回填。 */
  async function runProfile(args: string) {
    return harness.run(profileCommand(), args);
  }

  it("/profile 无参数打开弹窗（不触发切换、不留回执）", async () => {
    const applyProfile = vi.fn();
    const openDialog = vi.fn();
    await render({
      sessionId: "session-1",
      profileSelection: {
        candidates: profileCandidates(),
        applyProfile,
        openDialog,
      },
    });

    const handled = await runProfile("   ");

    expect(handled).toBe(true);
    expect(openDialog).toHaveBeenCalledTimes(1);
    expect(applyProfile).not.toHaveBeenCalled();
    expect(current().notice).toBeNull();
  });

  it("/profile <ref> 走宿主切换处理器，回执带可读名与「下一轮生效」", async () => {
    const applyProfile = vi.fn().mockResolvedValue(switchReport());
    await render({
      sessionId: "session-1",
      profileSelection: {
        candidates: profileCandidates(),
        applyProfile,
        openDialog: vi.fn(),
      },
    });

    await runProfile("review");

    expect(applyProfile).toHaveBeenCalledWith("review");
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.profile.applied",
      values: { profile: "Review", count: 0, warning: "" },
    });
  });

  it("/profile <ref> 大小写不敏感且唯一命中时应用目录里的原文 ref", async () => {
    const applyProfile = vi.fn().mockResolvedValue(switchReport());
    await render({
      sessionId: "session-1",
      profileSelection: {
        candidates: [
          {
            ref: "Review-Prod",
            label: "Review Prod",
            layer: "user",
            isDefault: false,
            valid: true,
            invalidReason: "",
          },
        ],
        applyProfile,
        openDialog: vi.fn(),
      },
    });

    await runProfile("review-prod");

    expect(applyProfile).toHaveBeenCalledWith("Review-Prod");
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.profile.applied",
      values: { profile: "Review Prod", count: 0, warning: "" },
    });
  });

  it("/profile 未命中报「不存在」（带原文，不触发切换）", async () => {
    const applyProfile = vi.fn();
    await render({
      sessionId: "session-1",
      profileSelection: {
        candidates: profileCandidates(),
        applyProfile,
        openDialog: vi.fn(),
      },
    });

    await runProfile("nope");

    expect(applyProfile).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.notFound",
      values: { profile: "nope" },
    });
  });

  it("/profile 存在但解析失败：如实报不可用 + 原因（不伪装成「不存在」）", async () => {
    const applyProfile = vi.fn();
    await render({
      sessionId: "session-1",
      profileSelection: {
        candidates: profileCandidates(),
        applyProfile,
        openDialog: vi.fn(),
      },
    });

    await runProfile("broken");

    expect(applyProfile).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.invalid",
      values: {
        profile: "Broken",
        reason: "profile.yaml: mapping values are not allowed here",
      },
    });
  });

  it("/profile 目录未就绪（候选为空）时报「不可用」而非「不存在」", async () => {
    const applyProfile = vi.fn();
    await render({
      sessionId: "session-1",
      profileSelection: { candidates: [], applyProfile, openDialog: vi.fn() },
    });

    await runProfile("review");

    expect(applyProfile).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.unavailable",
    });
  });

  it("/profile 无会话时如实报错，不发切换请求", async () => {
    const applyProfile = vi.fn();
    await render({
      profileSelection: {
        candidates: profileCandidates(),
        applyProfile,
        openDialog: vi.fn(),
      },
    });

    await runProfile("review");

    expect(applyProfile).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.noSession",
    });
  });

  it("/profile 在途回合：回执显式说明本回合仍走旧面", async () => {
    const applyProfile = vi
      .fn()
      .mockResolvedValue(switchReport({ inFlightTurn: true }));
    await render({
      sessionId: "session-1",
      profileSelection: {
        candidates: profileCandidates(),
        applyProfile,
        openDialog: vi.fn(),
      },
    });

    await runProfile("review");

    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.profile.appliedAfterTurn",
      values: { profile: "Review", count: 0, warning: "" },
    });
  });

  it("/profile 有差异告警：回执带条数与首条（不吞掉 provider/model 差异）", async () => {
    const applyProfile = vi.fn().mockResolvedValue(
      switchReport({
        warnings: ["provider differs: openai → anthropic", "model differs"],
      }),
    );
    await render({
      sessionId: "session-1",
      profileSelection: {
        candidates: profileCandidates(),
        applyProfile,
        openDialog: vi.fn(),
      },
    });

    await runProfile("review");

    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.profile.appliedWithWarnings",
      values: {
        profile: "Review",
        count: 2,
        warning: "provider differs: openai → anthropic",
      },
    });
  });

  it("/profile 切换失败回填错误通知（含原因），不伪装成功", async () => {
    const applyProfile = vi.fn().mockRejectedValue(new Error("HTTP 400"));
    await render({
      sessionId: "session-1",
      profileSelection: {
        candidates: profileCandidates(),
        applyProfile,
        openDialog: vi.fn(),
      },
    });

    await runProfile("review");

    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.failed",
      values: { message: "HTTP 400" },
    });
  });

  it("宿主未接线 /profile 时如实报不可用（带参数与无参数两条路径一致）", async () => {
    await render({ sessionId: "session-1" });

    await runProfile("");
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.unavailable",
    });

    await runProfile("review");
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.profile.unavailable",
    });
  });
});
