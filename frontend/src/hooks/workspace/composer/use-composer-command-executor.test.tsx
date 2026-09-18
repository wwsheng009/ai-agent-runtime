// @vitest-environment jsdom

// P2-7：composer 内置命令执行器单测。
//
// 覆盖口径：认领语义（未认领交回 composer）/ 参数校验前置失败 / 成功与失败通知 /
// 错误隔离（一条命令失败不影响后续命令）。
//
// 注意：`notice` 是渲染快照的一部分，断言一律读**最近一次渲染**的控制器（`current()`），
// 而不是发起命令时那个旧引用。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
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

import {
  useComposerCommandExecutor,
  type ComposerCommandExecutor,
  type UseComposerCommandExecutorOptions,
} from "./use-composer-command-executor";
import { COMPOSER_BUILTIN_COMMANDS } from "@/lib/composer-builtin-commands";
import {
  createComposerCommandRegistry,
  findComposerCommand,
  type ComposerCommand,
} from "@/lib/composer-commands";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const registry = createComposerCommandRegistry(COMPOSER_BUILTIN_COMMANDS);

function command(key: string): ComposerCommand {
  const found = findComposerCommand(registry, key);
  if (!found) {
    throw new Error(`test setup: command ${key} is not registered`);
  }
  return found;
}

function Harness({
  onSnapshot,
  options,
}: {
  onSnapshot: (executor: ComposerCommandExecutor) => void;
  options: UseComposerCommandExecutorOptions;
}) {
  const executor = useComposerCommandExecutor(options);
  onSnapshot(executor);
  return null;
}

describe("useComposerCommandExecutor", () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: ComposerCommandExecutor | null;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    latest = null;
    exportSessionTrajectoryJsonlMock.mockReset();
    executeSkillMock.mockReset();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  /** 最近一次渲染的控制器快照（notice 随之更新）。 */
  function current(): ComposerCommandExecutor {
    if (!latest) {
      throw new Error("test setup: harness has not rendered yet");
    }
    return latest;
  }

  async function render(options: UseComposerCommandExecutorOptions) {
    await act(async () => {
      root.render(
        <Harness onSnapshot={(value) => (latest = value)} options={options} />,
      );
    });
    return current();
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  /** 派发一条命令并等状态回填。 */
  async function run(key: string, args: string) {
    let handled = false;
    await act(async () => {
      handled = current().run(command(key), args);
    });
    await flush();
    return handled;
  }

  it("未认领的命令返回 false（交回 composer 显示未接入提示）", async () => {
    await render({ sessionId: "session-1" });
    const unknown = {
      key: "not-a-builtin",
      name: "not-a-builtin",
      kind: "execute",
    } as ComposerCommand;

    let handled = true;
    await act(async () => {
      handled = current().run(unknown, "");
    });

    expect(handled).toBe(false);
    expect(current().notice).toBeNull();
  });

  it("/feedback 空正文前置失败：不写日志、不产生回执", async () => {
    await render({ sessionId: "session-1" });

    const handled = await run("feedback", "   ");

    expect(handled).toBe(true);
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.feedback.needText",
    });
  });

  it("/feedback 为 log-only：回执如实说明已记录（不声称已上报）", async () => {
    await render({ sessionId: "session-1" });

    await run("feedback", "  the slash menu is handy  ");

    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.feedback.recorded",
    });
  });

  it("/skill 提交为普通回合：回调携带 skill 名与用户 prompt，不再调 executeSkill，成功无回执", async () => {
    const onRunSkillTurn = vi.fn().mockResolvedValue(undefined);
    await render({
      sessionId: "session-1",
      skillNames: ["run_shell_command"],
      onRunSkillTurn,
    });

    const handled = await run("skill", "run_shell_command echo hello world");

    expect(handled).toBe(true);
    expect(onRunSkillTurn).toHaveBeenCalledWith(
      "run_shell_command",
      "echo hello world",
    );
    expect(executeSkillMock).not.toHaveBeenCalled();
    // 成功路径不显示「执行成功」回执：消息会出现在线程里。
    expect(current().notice).toBeNull();
  });

  it("/skill 无回合提交回调时回退 executeSkill REST（模型驱动）并保留成功回执", async () => {
    executeSkillMock.mockResolvedValue({
      skill: "run_shell_command",
      status: "completed",
    });
    await render({
      sessionId: "session-1",
      skillNames: ["run_shell_command"],
    });

    const handled = await run("skill", "run_shell_command pwd");

    expect(handled).toBe(true);
    expect(executeSkillMock).toHaveBeenCalledWith("run_shell_command", {
      prompt: "pwd",
      sessionId: "session-1",
      options: { execution_mode: "model" },
    });
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.skill.applied",
      values: { skill: "run_shell_command" },
    });
  });

  it("/skill 回合提交失败：不走 executeSkill，回执如实报失败", async () => {
    const onRunSkillTurn = vi.fn().mockRejectedValue(new Error("session busy"));
    await render({
      sessionId: "session-1",
      skillNames: ["run_shell_command"],
      onRunSkillTurn,
    });

    await run("skill", "run_shell_command pwd");

    expect(executeSkillMock).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.skill.failed",
      values: { skill: "run_shell_command" },
    });
  });

  it("/skill 有名称无 prompt：不伪造空回合，宿主有弹窗时打开弹窗", async () => {
    const openSkillDialog = vi.fn();
    const onRunSkillTurn = vi.fn();
    await render({
      sessionId: "session-1",
      skillNames: ["run_shell_command"],
      openSkillDialog,
      onRunSkillTurn,
    });

    await run("skill", "run_shell_command");

    expect(openSkillDialog).toHaveBeenCalledTimes(1);
    expect(onRunSkillTurn).not.toHaveBeenCalled();
    expect(executeSkillMock).not.toHaveBeenCalled();
  });

  it("/skill 有名称无 prompt 且无弹窗：回执提示补 prompt", async () => {
    const onRunSkillTurn = vi.fn();
    await render({
      sessionId: "session-1",
      skillNames: ["run_shell_command"],
      onRunSkillTurn,
    });

    await run("skill", "run_shell_command");

    expect(onRunSkillTurn).not.toHaveBeenCalled();
    expect(executeSkillMock).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.skill.needPrompt",
      values: { skill: "run_shell_command" },
    });
  });

  it("/skill 未知名称前置失败：不发请求，回执如实说明不存在", async () => {
    const onRunSkillTurn = vi.fn();
    await render({
      sessionId: "session-1",
      skillNames: ["run_shell_command"],
      onRunSkillTurn,
    });

    const handled = await run("skill", "unknown_skill pwd");

    expect(handled).toBe(true);
    expect(onRunSkillTurn).not.toHaveBeenCalled();
    expect(executeSkillMock).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.skill.notFound",
      values: { skill: "unknown_skill" },
    });
  });

  it("/model 无参数打开弹窗（不调用 applyModel、不留回执）", async () => {
    const applyModel = vi.fn();
    const openDialog = vi.fn();
    await render({
      modelSelection: { applyModel, modelIds: ["deepseek-chat"], openDialog },
      sessionId: "session-1",
    });

    await run("model", "   ");

    expect(openDialog).toHaveBeenCalledTimes(1);
    expect(applyModel).not.toHaveBeenCalled();
    expect(current().notice).toBeNull();
  });

  it("/model <id> 精确匹配走常驻座位的同一处理器并回填模型", async () => {
    const applyModel = vi.fn();
    await render({
      modelSelection: {
        applyModel,
        modelIds: ["deepseek-chat", "deepseek-reasoner"],
        openDialog: vi.fn(),
      },
      sessionId: "session-1",
    });

    await run("model", "deepseek-reasoner");

    expect(applyModel).toHaveBeenCalledWith("deepseek-reasoner");
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.model.applied",
      values: { model: "deepseek-reasoner" },
    });
  });

  it("/model <id> 大小写不敏感且唯一命中时应用目录里的原文 id", async () => {
    const applyModel = vi.fn();
    await render({
      modelSelection: {
        applyModel,
        modelIds: ["DeepSeek-Chat"],
        openDialog: vi.fn(),
      },
      sessionId: "session-1",
    });

    await run("model", "deepseek-chat");

    expect(applyModel).toHaveBeenCalledWith("DeepSeek-Chat");
  });

  it("/model <id> 歧义或未命中时报「不存在」且带原文，不改动选择", async () => {
    const applyModel = vi.fn();
    await render({
      modelSelection: {
        applyModel,
        modelIds: ["model-a", "MODEL-A", "other"],
        openDialog: vi.fn(),
      },
      sessionId: "session-1",
    });

    // 跨 provider 同名（大小写不同）按歧义处理：不猜、不取首条。
    await run("model", "Model-A");
    expect(applyModel).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.model.notFound",
      values: { model: "Model-A" },
    });

    await run("model", "nope");
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.model.notFound",
      values: { model: "nope" },
    });
  });

  it("目录未就绪时报「不可用」而非「模型不存在」（不误导）", async () => {
    const applyModel = vi.fn();
    await render({
      modelSelection: { applyModel, modelIds: [], openDialog: vi.fn() },
      sessionId: "session-1",
    });

    await run("model", "deepseek-chat");

    expect(applyModel).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.model.unavailable",
    });
  });

  it("宿主未接线 /model 时如实报不可用（含无参数打开弹窗）", async () => {
    await render({ sessionId: "session-1" });

    await run("model", "");
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.model.unavailable",
    });
  });

  it("/rename 缺标题时不调用重命名处理器，并给出可见错误", async () => {
    const onRenameSession = vi.fn().mockResolvedValue(undefined);
    await render({ onRenameSession, sessionId: "session-1" });

    const handled = await run("rename", "   ");

    expect(handled).toBe(true);
    expect(onRenameSession).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.rename.needTitle",
    });
  });

  it("/rename 调用与侧栏同一处理器，成功回填标题", async () => {
    const onRenameSession = vi.fn().mockResolvedValue(undefined);
    await render({ onRenameSession, sessionId: "session-1" });

    await run("rename", '"Renamed by command"');

    expect(onRenameSession).toHaveBeenCalledWith(
      "session-1",
      "Renamed by command",
    );
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.rename.done",
      values: { title: "Renamed by command" },
    });
  });

  it("重命名失败只产出错误通知，后续命令仍可执行（错误隔离）", async () => {
    const onRenameSession = vi.fn().mockRejectedValue(new Error("boom"));
    exportSessionTrajectoryJsonlMock.mockResolvedValue({
      eventCount: 3,
      filename: "trajectory-session-1.jsonl",
      redacted: false,
    });
    await render({ onRenameSession, sessionId: "session-1" });

    await run("rename", "next");

    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.rename.failed",
      values: { message: "boom" },
    });

    await run("export", "");

    expect(exportSessionTrajectoryJsonlMock).toHaveBeenCalledWith("session-1", {
      redact: false,
    });
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.export.done",
      values: { count: 3, filename: "trajectory-session-1.jsonl" },
    });
  });

  it("/export --redact 走脱敏导出并回填脱敏通知", async () => {
    exportSessionTrajectoryJsonlMock.mockResolvedValue({
      eventCount: 12,
      filename: "trajectory-session-1-redacted.jsonl",
      redacted: true,
    });
    await render({ sessionId: "session-1" });

    await run("export", "--redact");

    expect(exportSessionTrajectoryJsonlMock).toHaveBeenCalledWith("session-1", {
      redact: true,
    });
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.export.doneRedacted",
      values: { count: 12, filename: "trajectory-session-1-redacted.jsonl" },
    });
  });

  it("/export 参数非法时不发起导出", async () => {
    await render({ sessionId: "session-1" });

    await run("export", "--json");
    expect(exportSessionTrajectoryJsonlMock).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.export.unknownFlag",
      values: { flag: "--json" },
    });

    await run("export", "now");
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.export.unexpectedArgument",
      values: { value: "now" },
    });
  });

  it("无会话 key 时如实报错，不伪造导出结果", async () => {
    await render({ sessionId: undefined });

    await run("export", "");

    expect(exportSessionTrajectoryJsonlMock).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.export.noSession",
    });
  });

  it("导出失败回填错误通知（含原因）", async () => {
    exportSessionTrajectoryJsonlMock.mockRejectedValue(
      new Error("network down"),
    );
    await render({ sessionId: "session-1" });

    await run("export", "");

    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.export.failed",
      values: { message: "network down" },
    });
  });

  it("dismissNotice 清空通知后不残留", async () => {
    await render({ sessionId: "session-1" });

    await run("export", "--json");
    expect(current().notice).not.toBeNull();

    await act(async () => current().dismissNotice());
    expect(current().notice).toBeNull();
  });
});
