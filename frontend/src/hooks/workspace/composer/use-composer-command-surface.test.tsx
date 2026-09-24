// @vitest-environment jsdom

// P2-7：composer 命令面（`useComposerCommandSurface`）的 `/skill` 策略单测。
// Batch 12：补 `/profile` 的能力门控（R20）与「弹窗点选走同一派发路径」口径。
//
// 覆盖口径：
// - 二级候选点选（source="pick"）只回填 `/skill <name> ` 到草稿，**不直接执行**；
// - 提交 `/skill <name> <prompt>`（source="submit"）才进入执行器，prompt 透传
//   回合提交回调（P2 回合化；宿主持有该回调时不走 executeSkill REST）；
// - 弹窗点选与菜单点选共用同一回填入口（`selectSkill`）；
// - `/skill` 无参数提交仍打开弹窗（原语义不回归）；
// - `/profile` 只在后端广告 `session_switch` 时注册；未注册时 `applyProfile`
//   返回 false 且不产生任何回执（不制造「已切换」假象）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { executeSkillMock } = vi.hoisted(() => ({
  executeSkillMock: vi.fn(),
}));

vi.mock("@/api/runtime/skills", () => ({
  executeSkill: executeSkillMock,
}));

import {
  useComposerCommandSurface,
  type ComposerCommandSurface,
  type UseComposerCommandSurfaceOptions,
} from "./use-composer-command-surface";
import type { SessionProfileSwitchReport } from "@/api/runtime/profiles";
import { buildComposerBuiltinCommands } from "@/lib/composer-builtin-commands";
import {
  createComposerCommandRegistry,
  findComposerCommand,
  type ComposerCommand,
} from "@/lib/composer-commands";
import type {
  RuntimeProfileListResponse,
  RuntimeSkill,
  RuntimeSkillCatalog,
} from "@/types/runtime";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const registry = createComposerCommandRegistry(
  buildComposerBuiltinCommands({
    skillOptions: [{ value: "translate", label: "translate", description: "text" }],
  }),
);

function skillCommand(): ComposerCommand {
  const found = findComposerCommand(registry, "skill");
  if (!found) {
    throw new Error("test setup: /skill is not registered");
  }
  return found;
}

// 能力广告为 true 时的清单：用于验证 `/profile` 的派发与弹窗路径。
const profileRegistry = createComposerCommandRegistry(
  buildComposerBuiltinCommands({ profileSwitchSupported: true }),
);

function profileCommand(): ComposerCommand {
  const found = findComposerCommand(profileRegistry, "profile");
  if (!found) {
    throw new Error("test setup: /profile is not registered");
  }
  return found;
}

function skillFixture(name: string): RuntimeSkill {
  return {
    name,
    description: "",
    version: "",
    category: "text",
    capabilities: [],
    tags: [],
    triggers: [],
    tools: [],
    systemPrompt: "",
    userPrompt: "",
    workflowSteps: [],
    contextFiles: [],
    contextEnvironment: [],
    contextSymbols: [],
    permissions: [],
    source: null,
  };
}

const CATALOG: RuntimeSkillCatalog = {
  skills: [skillFixture("translate")],
  count: 1,
};

/** profile 目录：一条可解析 + 一条解析失败（后者不进菜单候选）。 */
function profileCatalog(
  overrides: Partial<RuntimeProfileListResponse> = {},
): RuntimeProfileListResponse {
  return {
    count: 2,
    defaultProfile: "review",
    defaultRoot: "/root",
    sessionSwitch: true,
    workspacePath: "",
    workspaceTrusted: true,
    workspaceTrustFeatureEnabled: false,
    profiles: [
      {
        ref: "review",
        name: "Review",
        description: "",
        layer: "user",
        path: "/profiles/review",
        valid: true,
        error: "",
        isDefault: true,
        defaultAgent: "",
        writable: true,
        promptSuppressed: false,
        promptSuppressionReason: "",
      },
      {
        ref: "broken",
        name: "Broken",
        description: "",
        layer: "project",
        path: "/profiles/broken",
        valid: false,
        error: "yaml: mapping values are not allowed here",
        isDefault: false,
        defaultAgent: "",
        writable: true,
        promptSuppressed: false,
        promptSuppressionReason: "",
      },
    ],
    ...overrides,
  };
}

function switchReport(): SessionProfileSwitchReport {
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
  };
}

describe("useComposerCommandSurface /skill 点选语义", () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: ComposerCommandSurface | null;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    latest = null;
    executeSkillMock.mockReset();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function current(): ComposerCommandSurface {
    if (!latest) {
      throw new Error("test setup: harness has not rendered yet");
    }
    return latest;
  }

  function Harness(props: {
    onSnapshot: (surface: ComposerCommandSurface) => void;
    options: UseComposerCommandSurfaceOptions;
  }) {
    const surface = useComposerCommandSurface(props.options);
    props.onSnapshot(surface);
    return null;
  }

  async function render(
    overrides: Partial<UseComposerCommandSurfaceOptions> = {},
  ): Promise<UseComposerCommandSurfaceOptions> {
    const options: UseComposerCommandSurfaceOptions = {
      runtimeSkills: CATALOG,
      onDraftChange: vi.fn(),
      onModelChange: vi.fn(),
      sessionId: "session-1",
      ...overrides,
    };
    await act(async () => {
      root.render(
        <Harness
          onSnapshot={(value) => {
            latest = value;
          }}
          options={options}
        />,
      );
    });
    return options;
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("二级候选点选（pick）只回填 `/skill <name> `，不调用执行接口", async () => {
    const onDraftChange = vi.fn();
    await render({ onDraftChange });

    let handled = false;
    await act(async () => {
      handled = current().onCommand(skillCommand(), "translate", "pick");
    });
    await flush();

    expect(handled).toBe(true);
    expect(onDraftChange).toHaveBeenCalledWith("/skill translate ");
    expect(executeSkillMock).not.toHaveBeenCalled();
    expect(current().commandResult).toBeNull();
  });

  it("提交 `/skill <name> <prompt>`（submit）才提交回合，prompt 透传宿主", async () => {
    const onRunSkillTurn = vi.fn();
    const onDraftChange = vi.fn();
    await render({ onDraftChange, onRunSkillTurn });

    await act(async () => {
      current().onCommand(skillCommand(), "translate 把这段翻译成中文", "submit");
    });
    await flush();

    expect(onRunSkillTurn).toHaveBeenCalledWith("translate", "把这段翻译成中文");
    expect(executeSkillMock).not.toHaveBeenCalled();
    // 回填只属于「点选」；提交不追加草稿（清空由 composer 提交路径负责）。
    expect(onDraftChange).not.toHaveBeenCalled();
  });

  it("弹窗点选与菜单点选共用回填入口 `selectSkill`", async () => {
    const onDraftChange = vi.fn();
    await render({ onDraftChange });

    await act(async () => {
      current().selectSkill("translate");
    });

    expect(onDraftChange).toHaveBeenCalledWith("/skill translate ");
    expect(executeSkillMock).not.toHaveBeenCalled();
  });

  it("`/skill` 无参数提交仍打开弹窗（不执行、不回填）", async () => {
    const onDraftChange = vi.fn();
    await render({ onDraftChange });

    await act(async () => {
      current().onCommand(skillCommand(), "", "submit");
    });

    expect(current().skillDialogOpen).toBe(true);
    expect(executeSkillMock).not.toHaveBeenCalled();
    expect(onDraftChange).not.toHaveBeenCalled();
  });

  it("R20：后端未广告 `session_switch` 时不注册 `/profile`，点选不派发、无回执", async () => {
    const onProfileSwitch = vi.fn();
    await render({
      runtimeProfiles: profileCatalog({ sessionSwitch: false }),
      onProfileSwitch,
    });

    expect(current().commands.map((command) => command.name)).not.toContain("profile");
    // 目录本身可用（候选不隐藏），但命令没注册 ⇒ 没有任何执行入口。
    expect(current().profileCandidates).toHaveLength(2);

    let handled = true;
    await act(async () => {
      handled = current().applyProfile("review");
    });
    await flush();

    expect(handled).toBe(false);
    expect(onProfileSwitch).not.toHaveBeenCalled();
    expect(current().commandResult).toBeNull();
  });

  it("目录未就绪（无 runtimeProfiles）时不注册 `/profile`", async () => {
    await render();

    expect(current().commands.map((command) => command.name)).not.toContain("profile");
    expect(current().profileCandidates).toEqual([]);
  });

  it("能力广告为 true 时注册 `/profile`：二级候选只含可解析项，弹窗候选保留全集", async () => {
    await render({
      runtimeProfiles: profileCatalog(),
      onProfileSwitch: vi.fn(),
    });

    const profile = current().commands.find((command) => command.name === "profile");
    expect(profile).toBeDefined();
    expect(profile?.options).toEqual([
      { value: "review", label: "Review", description: "user" },
    ]);
    // 不可解析项不进菜单（不承诺注定失败的切换），但仍在候选全集里，
    // 用户手写 ref 时会得到「存在但不可用 + 原因」的回执。
    expect(current().profileCandidates.map((candidate) => candidate.ref)).toEqual([
      "review",
      "broken",
    ]);
  });

  it("`applyProfile`（弹窗点选）走与命令行同一条派发路径并产生成功回执", async () => {
    const onProfileSwitch = vi.fn().mockResolvedValue(switchReport());
    await render({ runtimeProfiles: profileCatalog(), onProfileSwitch });

    let handled = false;
    await act(async () => {
      handled = current().applyProfile("review");
    });
    await flush();

    expect(handled).toBe(true);
    expect(onProfileSwitch).toHaveBeenCalledWith("review");
    expect(current().commandResult).toEqual({
      tone: "success",
      text: "已切换 profile：Review（下一轮生效）",
    });
  });

  it("`/profile` 无参数提交打开弹窗（与 `/skill` 同一交互口径）", async () => {
    const onProfileSwitch = vi.fn();
    await render({ runtimeProfiles: profileCatalog(), onProfileSwitch });

    expect(current().profileDialogOpen).toBe(false);
    await act(async () => {
      current().onCommand(profileCommand(), "", "submit");
    });

    expect(current().profileDialogOpen).toBe(true);
    expect(onProfileSwitch).not.toHaveBeenCalled();

    await act(async () => current().closeProfileDialog());
    expect(current().profileDialogOpen).toBe(false);
  });
});
