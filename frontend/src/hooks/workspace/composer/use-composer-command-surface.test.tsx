// @vitest-environment jsdom

// P2-7：composer 命令面（`useComposerCommandSurface`）的 `/skill` 策略单测。
//
// 覆盖口径：
// - 二级候选点选（source="pick"）只回填 `/skill <name> ` 到草稿，**不直接执行**；
// - 提交 `/skill <name> <prompt>`（source="submit"）才进入执行器，prompt 透传后端；
// - 弹窗点选与菜单点选共用同一回填入口（`selectSkill`）；
// - `/skill` 无参数提交仍打开弹窗（原语义不回归）。

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
import { buildComposerBuiltinCommands } from "@/lib/composer-builtin-commands";
import {
  createComposerCommandRegistry,
  findComposerCommand,
  type ComposerCommand,
} from "@/lib/composer-commands";
import type { RuntimeSkill, RuntimeSkillCatalog } from "@/types/runtime";

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

  it("提交 `/skill <name> <prompt>`（submit）才执行，prompt 透传执行器", async () => {
    executeSkillMock.mockResolvedValue({});
    const onDraftChange = vi.fn();
    await render({ onDraftChange });

    await act(async () => {
      current().onCommand(skillCommand(), "translate 把这段翻译成中文", "submit");
    });
    await flush();

    expect(executeSkillMock).toHaveBeenCalledWith("translate", {
      prompt: "把这段翻译成中文",
      sessionId: "session-1",
    });
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
});
